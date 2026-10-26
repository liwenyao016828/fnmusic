package intercept

import (
	"os"
	"sort"
	"time"

	"fn-lx-player/pkg/online"
)

// 后台「音质补强」轮。
//
// # 为什么要有这个东西
//
// 用户 2026-10-21 说的两句话合起来才是完整要求（m02588）：
//
//	「收藏自动下载不需要立刻下载，优先最高音质，慢一点没关系」
//
// 前半句（不必立刻、可以慢）由 v2.17 的取音计划满足：收藏触发后先把**所有渠道探测一遍**
// 再落盘，一次只落一份，宁可花几十秒也不抢着先下一份 320k。
//
// 后半句是这一文件要回答的：**这次没拿到的无损，以后要自己补上**。
// v2.15/2.16 的做法是「为音质重下一次，只试一次」，失败就把 `upgrade_tried` 永远钉上 ——
// 而这条链失败的原因绝大多数是「第三方站此刻不给」（脚本 404、CDN 限流、网易云免登录
// 静默降档），这些东西是天级会变的。一次定生死等于用户提的要求里那句「慢一点没关系」
// 根本没被听见。现在改成：失败记一个 24 小时的冷却位，冷却到了由这个轮子再试，
// 累计 5 回（跨 5 天）还没有就收手 —— 剩下的交给「替换音质」页如实列出来让人自己决定。
//
// # 为什么不需要新起一张队列表
//
// 补强的候选**已经在登记表里**：`downloaded` 记着每条曲目的虚拟 guid、落盘路径与实际
// 档位，而虚拟 guid 在启动时由 `store.AllTracks()` 重放回 `registry`（见 intercept.go
// 里的 Warm）。所以「哪些歌还欠一份无损」= 登记表里有损的那批，不需要再多存一份状态，
// 也不会有两份状态互相打嘴架。
//
// # 节奏为什么这么保守
//
// 一轮最多派 `upgradeSweepBatch` 首。一次探测在洛雪渠道上要几十秒，一轮补 20 首会把
// 整个补强变成几分钟的连续外呼，把用户此刻的收藏下载挤到后面去 —— 补强是背景活动，
// 它不该跟前台抢。
//
// # v2.22 之后的「可控化」（用户 2026-10-25，m00286）
//
// 用户原话：「音质补强是不是有个自动补强？下载-自动记录里有个记录，但是我没法控制呢？
// 增加相应的定时按钮，补强音质来源，音质。将这个自动下载功能设置增加到设置。」
//
// 那条记录以前是**没开关、没节奏、没来源、没目标档位**可调的 —— 全写死在这个文件里。
// 现在四个维度都从 config 现取（见 pkg/config 的 UpgradeSweep* 字段）：
//
//	开关    UpgradeSweepOn()            独立于「收藏自动下载」
//	节奏    UpgradeSweepInterval()      界面上选 15m / 1h / 4h / 12h / 1d
//	批量    UpgradeSweepPerRound()      一轮 1~10 首
//	来源    AutoDownloadChannels()      与自动下载共用「取音渠道」
//	档位    downloadTiers()             与自动下载共用「下载音质」
//
// 候选筛选里还有一条硬规则来自档位：用户点名只要 320k 时**不在后台追无损**
// （那是逆着他的选择花流量）。
//
// 配置改动**热生效**：api 层保存后调 ReloadUpgradeSweeper，间隔立刻按新值重排，
// 不需要重启应用（与「播放音质现取」同一个承诺）。

// upgradeRetryCooldown / upgradeMaxTries 见 pkg/online/downloaded.go 与
// autodownload.go 里的定义（冷却位 = 24 小时、累计 5 回收手）。本文件只说节奏，
// 不重复定义这两个数字 —— 两处各写一份迟早漂。

// upgradeSweepStatus 是「当前这一轮跑到哪了」的现场快照，给接口与界面看。
//
// 为什么要单独存：以前用户在界面上只能从「自动记录」里倒推它在跑 —— 那是**事后的**
// 一条流水，看不出「现在这一轮派了几首、下次什么时候跑、还剩多少欠账」。
type upgradeSweepStatus struct {
	// Running 这一轮是否正在派活（派活本身很快，主要是在探测）。
	Running bool `json:"running"`
	// LastRunAt / LastDispatched 上一轮跑的时刻与派出去的条数。
	LastRunAt      int64 `json:"last_run_at"`
	LastDispatched int   `json:"last_dispatched"`
	// NextRunAt 下一轮最早什么时候跑（界面据此显示「下次 3 小时后」）。
	NextRunAt int64 `json:"next_run_at"`
	// Round 已经跑过几轮（本次进程内）。
	Round int `json:"round"`
}

// upgradeSweepDeps 是这一轮能不能跑的前置条件（单独抽出来是为了能在单测里点名，
// 而不是逼着测试去凑齐一整套注入口）。
//
// ⚠️ 这里**不再**看 favAutoDownload：开关已经独立成 UpgradeSweepOn()，
// 而它 nil 时的语义（跟收藏自动下载同步）在 config 层统一判。
func (i *Interceptor) upgradeSweepDeps() bool {
	return i.upgradeSweepOn() && i.downloaded != nil && i.localAPI != ""
}

// upgradeConfigured 报告「用户/配置层是否允许补强」。没注入时按关处理（宁可不动）。
func (i *Interceptor) upgradeConfigured() bool {
	return i.upgradeSweepOn()
}

// upgradeCandidates 挑出「这一轮该补」的曲目，按欠账久短排好序。**纯选择，不派活**。
//
// 单独成函数有两个原因：① 这段判据是这一层全部的规则，测试要能点名而不必先骗
// autoDownload 起 goroutine；② 派活上限（batch）与「谁该补」是两件事，混在一个
// 循环里迟早有人为了调 batch 把筛选顺序也动了。
func (i *Interceptor) upgradeCandidates(now time.Time) []online.DownloadedItem {
	if !i.upgradeSweepDeps() {
		return nil
	}
	// 用户点名要低档位（320k/192k/128k）时别在后台追无损：那是逆着他的选择花流量。
	tiers := i.downloadTiers()
	if len(tiers) == 0 || online.TierRank(tiers[0]) < online.TierRank(online.TierLossless) {
		return nil
	}
	items := i.downloaded.All()
	// 先按「欠得最久」排：UpgradeNext 是冷却到期时间，越早到期的越该先补；
	// 没有 UpgradeNext（从没试过）= 0，自然排最前 —— 这正是我们要的（新收录的欠账优先）。
	sortUpgradeCandidates(items)
	return i.filterUpgradeCandidates(items, now)
}

// filterUpgradeCandidates 是 upgradeCandidates 的**筛选部分**，拆出来是为了让
// 「无视冷却」这件事只在一处判（两边各写一份迟早漂）。
func (i *Interceptor) filterUpgradeCandidates(items []online.DownloadedItem, now time.Time) []online.DownloadedItem {
	ignoreCooldown := i.upgradeIgnoreCooldown()
	out := make([]online.DownloadedItem, 0, len(items))
	for _, it := range items {
		if it.Path == "" {
			continue
		}
		if !ignoreCooldown && !upgradeSlotOpen(it, now) {
			continue
		}
		if i.landedLossless(it) {
			continue
		}
		if _, err := os.Stat(it.Path); err != nil {
			continue // 文件不在了：那是「重新下载」的事，不归补强管
		}
		if _, ok := i.registry.Lookup(it.GUID); !ok {
			// 启动重放没覆盖到它（这首歌已经从收藏/歌单/历史里删掉了）。
			// 后台不该替用户补一首他已经不要的歌。
			continue
		}
		out = append(out, it)
	}
	return out
}

// UpgradePendingCount 报告「还欠一份无损、且此刻可以补」的曲目数。
//
// ⚠️ 它**不判**「无视冷却」之后的可补数：界面上写的是「欠账 N 首（其中 M 首在冷却中）」，
// 两个数都要，所以这里给的是「不受冷却限制的欠账总数」，冷却中的由 UpgradeCoolingCount 给。
func (i *Interceptor) UpgradePendingCount() (pending int, cooling int) {
	if i.downloaded == nil {
		return 0, 0
	}
	items := i.downloaded.All()
	now := time.Now()
	ready := 0
	for _, it := range items {
		if it.Path == "" || i.landedLossless(it) {
			continue
		}
		if _, err := os.Stat(it.Path); err != nil {
			continue
		}
		if _, ok := i.registry.Lookup(it.GUID); !ok {
			continue
		}
		if upgradeSlotOpen(it, now) {
			ready++
		} else if it.UpgradeTries < upgradeMaxTries {
			cooling++
		}
	}
	return ready + cooling, cooling
}

func (i *Interceptor) upgradeSweepOnce(now time.Time) int {
	return i.upgradeSweepWith(now, i.upgradeSweepBatchSize(), i.upgradeIgnoreCooldown())
}

// upgradeSweepWith 是带参数的版本：批量与是否无视冷却由调用方给。
//
// `manual=true` 的手动补一轮走的是同一段逻辑（只是 batch 给满、冷却位放行），
// 保证「手动那条路」和「后台那条路」的语义完全一致 —— 否则迟早出现「手动补到的、
// 后台补不到」这种没法解释的偏差。
func (i *Interceptor) upgradeSweepWith(now time.Time, batch int, ignoreCooldown bool) int {
	if !i.upgradeSweepDeps() {
		return 0
	}
	// 档位门槛与候选筛选都复用一套：手动补一轮也不许逆着用户选的档位去追无损。
	tiers := i.downloadTiers()
	if len(tiers) == 0 || online.TierRank(tiers[0]) < online.TierRank(online.TierLossless) {
		return 0
	}
	items := i.downloaded.All()
	sortUpgradeCandidates(items)
	items = i.filterUpgradeCandidates(items, now)
	_ = ignoreCooldown // 已由 filterUpgradeCandidates 按配置判过

	if batch <= 0 {
		batch = 1
	}
	dispatched := 0
	for _, it := range items {
		if dispatched >= batch {
			break
		}
		t, ok := i.registry.Lookup(it.GUID)
		if !ok {
			continue // 选完之后被并发删掉：这一首不派，下一轮重新选
		}
		i.autoDownload(it.GUID, t, TriggerUpgrade)
		dispatched++
	}
	return dispatched
}

// UpgradeSweepNow 立刻补一轮（界面上的「立即补一轮」）。
//
// `clearCooldown=true` 时先把这个进程里所有登记项的冷却位清掉 —— 用户点这个按钮的
// 意思就是「我知道它们刚失败过，但我现在想再试一次」。**不动 UpgradeTries**：
// 总尝试次数上限仍然有效，否则就是无限重试。
//
// 返回派出去的条数。**同步执行**（派活本身只是起 goroutine，几十毫秒就回来）。
func (i *Interceptor) UpgradeSweepNow(clearCooldown bool) (dispatched int, pending int, err string) {
	if i.downloaded == nil {
		return 0, 0, "已下载登记表没起来（数据目录不可写？）"
	}
	if !i.upgradeConfigured() {
		return 0, 0, "补强轮没开（设置 → 自动下载 → 后台音质补强）"
	}
	if !i.upgradeSweepDeps() {
		return 0, 0, "补强轮没开，或本地接口没就绪"
	}
	if clearCooldown {
		i.clearUpgradeCooldown()
	}
	pending, _ = i.UpgradePendingCount()
	// 手动补一轮不受 batch 限制那么死，但也别一次派完整个曲库：
	// 给「一轮上限 × 2」，让他点两下就能多补一点，又不至于把前台全挤掉。
	batch := i.upgradeSweepBatchSize() * 2
	n := i.upgradeSweepWith(time.Now(), batch, true)
	i.noteUpgradeRound(n)
	return n, pending, ""
}

// clearUpgradeCooldown 把登记表里所有条目的冷却位清空（UpgradeTries 保留）。
func (i *Interceptor) clearUpgradeCooldown() {
	items := i.downloaded.All()
	for _, it := range items {
		if it.UpgradeNext == 0 {
			continue
		}
		it.UpgradeNext = 0
		// Put 会按 guid 覆盖并落盘（见 pkg/online/downloaded.go）
		_ = i.downloaded.Put(it)
	}
}

// sortUpgradeCandidates 原地把候选排成「冷却越早到期越靠前」，同值按 guid 定序。
//
// 单独成函数是为了让测试能直接钉这个顺序（谁先补）；guid 兜底是因为 `All()` 的输出
// 顺序取决于 map，不钉死的话「一轮补哪三首」会随机漂，测试也就没法断言。
func sortUpgradeCandidates(items []online.DownloadedItem) {
	sort.SliceStable(items, func(a, b int) bool {
		na, nb := items[a].UpgradeNext, items[b].UpgradeNext
		if na != nb {
			return na < nb
		}
		return items[a].GUID < items[b].GUID
	})
}

// StartUpgradeSweeper 起后台轮子（幂等：调多次只有一个在跑）。
//
// ⚠️ 由 main.go 显式启动，而不是塞进 New()：`New()` 在几十个赞测里被调用，
// 构造函数里起常驻 goroutine 会让测试进程留一堆不会停的循环（而且它们会真的
// 去 os.Stat 别人的路径）。
func (i *Interceptor) StartUpgradeSweeper() {
	i.upgradeMu.Lock()
	if i.upgradeStop != nil {
		i.upgradeMu.Unlock()
		return
	}
	i.upgradeGen++ // 每一代一个号：重置定时器时旧的那一代会自己退场
	gen := i.upgradeGen
	stop := make(chan struct{})
	i.upgradeStop = stop
	i.upgradeMu.Unlock()

	go i.upgradeLoop(stop, gen)
}

// upgradeLoop 是补强轮的常驻循环。
//
// 与旧版的差别：间隔**每轮现取**（配置改了下一轮就按新值走，不用重启），
// 并且对外汇报 NextRunAt —— 界面上「下次大概什么时候跑」靠它。
func (i *Interceptor) upgradeLoop(stop chan struct{}, gen int) {
	// 启动后先等一轮再动手：刚起来时收藏/歌单同步正在抢解析器与带宽，
	// 补强是背景活动，让它排在后面。
	wait := i.upgradeSweepInterval()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	i.noteUpgradeNextRun(wait)
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			// 配置可能在等待期间被改了（关掉 / 改间隔）：现取一次再决定跑不跑。
			if !i.upgradeSweepOn() {
				wait = i.upgradeSweepInterval()
				timer.Reset(wait)
				i.noteUpgradeNextRun(wait)
				continue
			}
			i.beginUpgradeRound()
			n := i.upgradeSweepWith(time.Now(), i.upgradeSweepBatchSize(), i.upgradeIgnoreCooldown())
			i.endUpgradeRound(n)
			if n > 0 {
				i.logf("[INTERCEPT] 音质补强：这一轮派出 %d 首", n)
			}
			wait = i.upgradeSweepInterval()
			timer.Reset(wait)
			i.noteUpgradeNextRun(wait)
		}
	}
}

// ReloadUpgradeSweeper 让攒到的新配置立刻生效（间隔重排、开关立刻停/启）。
//
// 实现走「停 + 起」而不是「改一个共享变量」：开关从「关」变「开」时必须**重新起循环**
// （关着的时候循环可能已经被 Stop 掉了），改间隔也一样要重排定时器。代价是每次保存
// 设置都会让「启动后先等一轮」重新计时 —— 这是刻意的：用户刚打开开关时不该马上外呼，
// 他往往还要接着调来源与档位。
func (i *Interceptor) ReloadUpgradeSweeper() {
	i.CloseUpgradeSweeper()
	if i.upgradeSweepOn() {
		i.StartUpgradeSweeper()
	}
}

// CloseUpgradeSweeper 停掉后台轮子（优雅关闭时调；没起过就是空操作）。
//
// 「取出 + 置 nil + 解锁」之后再 close：在锁内 close 的话，同一时刻正在跑的
// StartUpgradeSweeper 可能拿到同一个 chan 再 close 一次 —— panic。
func (i *Interceptor) CloseUpgradeSweeper() {
	i.upgradeMu.Lock()
	stop := i.upgradeStop
	i.upgradeStop = nil
	i.upgradeMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// UpgradeSweeperRunning 报告轮子在不在跑（界面/接口用来显示「已启用/已停用」）。
func (i *Interceptor) UpgradeSweeperRunning() bool {
	i.upgradeMu.Lock()
	defer i.upgradeMu.Unlock()
	return i.upgradeStop != nil
}

// UpgradeSweepStatus 读「这一轮跑到哪」的现场快照。
func (i *Interceptor) UpgradeSweepStatus() upgradeSweepStatus {
	i.upgradeMu.Lock()
	defer i.upgradeMu.Unlock()
	return i.upgradeStat
}

// ── 内部：往 upgradeStat 写的小动作都集中在这里，避免各处各自加锁 ──

func (i *Interceptor) beginUpgradeRound() {
	i.upgradeMu.Lock()
	i.upgradeStat.Running = true
	i.upgradeMu.Unlock()
}

func (i *Interceptor) endUpgradeRound(n int) {
	i.upgradeMu.Lock()
	i.upgradeStat.Running = false
	i.upgradeStat.LastRunAt = time.Now().Unix()
	i.upgradeStat.LastDispatched = n
	i.upgradeStat.Round++
	i.upgradeMu.Unlock()
}

// noteUpgradeRound 记一次**手动**补一轮（不进 Round 的技术统计，但更新时间与条数，
// 界面上一眼能看到刚才那一下确实派活了）。
func (i *Interceptor) noteUpgradeRound(n int) {
	i.upgradeMu.Lock()
	i.upgradeStat.LastRunAt = time.Now().Unix()
	i.upgradeStat.LastDispatched = n
	i.upgradeMu.Unlock()
}

func (i *Interceptor) noteUpgradeNextRun(wait time.Duration) {
	i.upgradeMu.Lock()
	i.upgradeStat.NextRunAt = time.Now().Add(wait).Unix()
	i.upgradeMu.Unlock()
}

// ── 配置现取的薄封装（注入为 nil 时给安全缺省值）──

func (i *Interceptor) upgradeSweepOn() bool {
	return i.upgradeSweepEnabled != nil && i.upgradeSweepEnabled()
}

func (i *Interceptor) upgradeIgnoreCooldown() bool {
	return i.upgradeIgnoreCooldownFn != nil && i.upgradeIgnoreCooldownFn()
}

func (i *Interceptor) upgradeSweepInterval() time.Duration {
	if i.upgradeSweepIntervalFn != nil {
		if d := i.upgradeSweepIntervalFn(); d > 0 {
			return d
		}
	}
	return 4 * time.Hour
}

func (i *Interceptor) upgradeSweepBatchSize() int {
	if i.upgradeSweepBatchFn != nil {
		if n := i.upgradeSweepBatchFn(); n > 0 {
			return n
		}
	}
	return 3
}
