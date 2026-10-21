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

const (
	// upgradeSweepEvery 两轮之间隔多久。20 分钟是「重启后不久能试一次、又不刷屏」的折中。
	upgradeSweepEvery = 20 * time.Minute
	// upgradeSweepBatch 一轮最多派几首去补强。
	upgradeSweepBatch = 3
)

// upgradeSweepDeps 是这一轮能不能跑的前置条件（单独抽出来是为了能在单测里点名，
// 而不是逼着测试去凑齐一整套注入口）。
func (i *Interceptor) upgradeSweepDeps() bool {
	return i.favAutoDownloadOn() && i.downloaded != nil && i.localAPI != ""
}

// upgradeSweepOnce 走一遍已下载登记表，把「有损 + 冷却已开」的曲目交给 autoDownload。
// 返回这一轮派出去几首。**不含定时器**，所以单测可以直接调它。
//
// 派活走既有的 `autoDownload` 而不是这里自己下一遍：渠道顺序、档位判据、同曲去重
// （`markDownloading`）、无损覆盖旧有损（`upgrade_if_better`）、成功/失败记录 ——
// 全部复用。补强和收藏下载必须是同一套语义，否则迟早出现「手动那条路能拿到无损、
// 后台那条路拿不到」这种没法解释的偏差。
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

	out := make([]online.DownloadedItem, 0, len(items))
	for _, it := range items {
		if it.Path == "" || !upgradeSlotOpen(it, now) {
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

func (i *Interceptor) upgradeSweepOnce(now time.Time) int {
	items := i.upgradeCandidates(now)

	dispatched := 0
	for _, it := range items {
		if dispatched >= upgradeSweepBatch {
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
	defer i.upgradeMu.Unlock()
	if i.upgradeStop != nil {
		return
	}
	stop := make(chan struct{})
	i.upgradeStop = stop
	go func() {
		// 启动后先等一轮再动手：刚起来时收藏/歌单同步正在抢解析器与带宽，
		// 补强是背景活动，让它排在后面。
		timer := time.NewTimer(upgradeSweepEvery)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				if n := i.upgradeSweepOnce(time.Now()); n > 0 {
					i.logf("[INTERCEPT] 音质补强：这一轮派出 %d 首", n)
				}
				timer.Reset(upgradeSweepEvery)
			}
		}
	}()
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
