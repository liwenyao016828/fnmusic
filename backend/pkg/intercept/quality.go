package intercept

// 「替换音质」：扫曲库 → 看每首歌在每条取音渠道**实际**能拿到什么档位 → 用最高档位
// 把现在的低音质文件换掉。
//
// # 为什么要有它
//
// 自动下载的补强轮（见 upgrade_sweeper.go）只敢对**自己下过**的歌动手，而且一次
// 最多三首、失败要等一天。用户真正面对的是「我这一整库 mp3，哪些其实能换成无损」——
// 那需要一个**看得见、点得动**的入口：先如实列出来，再由用户决定换哪些。
//
// # 与自动下载的分工（口径必须一致，别在这里另立一套）
//
// 取音计划、渠道顺序、档位判据全部复用自动下载那套（`collectDownloadAttempts` /
// `channelTiersOf` / `downloadTiers`），落盘也走同一条 `postDownload` 通道 ——
// 唯一的区别是「谁来按下按钮」：那边是收藏事件，这边是用户。
//
// # 扫描为什么是异步的
//
// 一首歌要问 2–3 条渠道（洛雪一条失败要 8 秒 × 脚本数），几百首就是十几分钟。
// 同步接口早超时了，所以这里是「起一个任务 + 轮询状态」，与 musicdl 试运行同一个
// 形状（见 pkg/api/musicdl.go 的 SetMusicDLPreview）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/online"
)

const (
	// 状态取值（与前端契约一致，改这里要同步改 frontend/src/api/client.js 的注释）。
	QualityStateIdle    = "idle"
	QualityStateRunning = "running"
	QualityStateDone    = "done"
	QualityStateFailed  = "failed"

	// 扫描并发度：探测是「等外网」的重活，4 条并行已经能把上传带宽之外的等待盖住；
	// 再高会让 musicdl/洛雪侧排队，反而更慢（洛雪宿主是单进程串行处理请求的）。
	qualityScanWorkers = 4
	// 内存里的结果上限：界面一屏也放不下更多，超出只计数不保留明细（`truncated`）。
	qualityScanMaxEntries = 500
	// 用户输入的 limit 上限：防止一次扫到几十万首把整机拖住。
	qualityScanMaxLimit = 2000
)

// 三道预算（变量而不是常量：测试要把围栏缩到毫秒级）。
var (
	// 单首探测的总时长围栏。
	//
	// 为什么要有：探测是「等外网」的重活，而外网的时间不是我们能定的 —— 实测最坏
	// 一首要 80s（2026-11-21，NAS 上 30 首冷扫 580.9s ≈ 19.4s/首）。4 个 worker
	// 里只要有一个卡住，那一格就废了，整批的墙钟被最慢的那首拖着走。
	//
	// 到点怎么办：**记「没探出」**，不是「探出没有」。区别很要紧 ——
	// 「没有更高音质」是结论，可以据此跳过重扫 7 天；「没探出」没有结论，
	// 下次还得再探（见 qualityEntryFresh 里对 TimedOut 的处理）。
	qualityProbeBudget = 25 * time.Second

	// 单个 musicdl 源在**探测腿**上最多等多久。
	//
	// 只掐探测腿，不掐下载腿：下载要的是「能下下来」，慢而全是对的
	// （v2.13.0 那轮把 sidecar 预算从 12s 提到 120s 就是因为掐太死会「取音池恒空」）。
	// 探测腿只是问「有没有更高音质」，等太久没有意义 —— 实测多数源 1~2s 就回，
	// 少数慢源 3~15s，等满 15s 也未必有结果。
	qualitySourceBudget = 8 * time.Second

	// 洛雪那一路在**探测腿**上最多等多久。
	//
	// 为什么要单独一条：洛雪是按脚本逐个问的（6 个脚本各问一遍，单次实测 3~7s），
	// 而它不受 musicdl 单源预算的约束 —— v2.34.1 实测单首「洛雪」一段最高 16.6s，
	// 比其它所有段加起来还长。下载腿仍然不限（要真直链）。
	qualityLxBudget = 6 * time.Second

	// 替换那条腿的围栏：用户已经点了「替换」，正等着 —— 这时候按 25s 掐掉探测，
	// 界面就会显示「各渠道都没有更高的档位」这种**结论性**的话，而实际只是
	// 没探完。所以替换腿给一个宽裕的围栏，且**不限**单源等待（它要的是真直链）。
	qualityReplaceProbeBudget = 120 * time.Second
)

// 无损容器。**按容器判**，不看文件名里有没有「无损」两个字 ——
// 真机见过 `xxx 无损.flac.mp3`（一个 mp3，名字里写着无损）。
var qualityLosslessExts = map[string]bool{
	".flac": true, ".ape": true, ".wav": true, ".dsf": true, ".dff": true,
}

// 认得出是音频的容器（其余文件跳过：封面、歌词、cue 都不该进扫描）。
var qualityAudioExts = func() map[string]bool {
	m := map[string]bool{".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wma": true}
	for k := range qualityLosslessExts {
		m[k] = true
	}
	return m
}()

// QualityEntry 曲库里的一首歌 + 各渠道实测结果。
type QualityEntry struct {
	Path        string        `json:"path"`
	Title       string        `json:"title"`
	Artist      string        `json:"artist"`
	Ext         string        `json:"ext"`
	CurrentTier string        `json:"current_tier"`
	Channels    []channelTier `json:"channels"`
	Best        *channelTier  `json:"best"`
	Upgradeable bool          `json:"upgradeable"`
	// TimedOut 表示「这次探测没跑完就被围栏掐了」，也就是**没有结论**。
	//
	// 它和 Upgradeable=false 是两回事：
	//   - Upgradeable=false 且 TimedOut=false → 「探过了，确实没有更高音质」（结论）
	//   - TimedOut=true                      → 「没探出」（无结论，下次要重探）
	// 免扫名单只认前者（见 qualityEntryFresh）。
	TimedOut bool `json:"timed_out,omitempty"`
	// ScannedAt 是这条结论的探测时间（Unix 秒，0 = 老数据/未知）。
	// 有了它界面才能显示「上次扫描」，也才能判断这条结论能不能跳过重扫。
	ScannedAt int64 `json:"scanned_at"`
}

// QualityStatus 扫描任务的状态（也是 /api/quality/scan 的 data）。
type QualityStatus struct {
	State     string `json:"state"`
	Mode      string `json:"mode"`
	Total     int    `json:"total"`
	Scanned   int    `json:"scanned"`
	Truncated bool   `json:"truncated"`
	// Skipped 是「因为最近扫过而跳过」的条数（不重扫、结论直接沿用）。
	Skipped     int `json:"skipped"`
	Upgradeable int `json:"upgradeable"`
	// Updated 是结果里最新的扫描时间（Unix 秒，0 = 从来没扫过）。
	// 界面用它显示「上次扫描」。
	Updated int64          `json:"updated"`
	Error   string         `json:"error"`
	Entries []QualityEntry `json:"entries"`
	// Records 是「替换音质」的**历史记录**（最近 N 条，落盘，重启还在）。
	//
	// 为什么必须落盘：替换是**原地覆盖**，用户在文件管理器里看不出任何变化，
	// 界面一刷新更是「做过什么」全没了 —— 用户第一句反馈就是「怎么看不到记录」。
	// 它也是这一功能唯一的凭据：某首歌哪天从 128k 换成了 320k、走的哪条渠道。
	Records []QualityRecord `json:"records"`
}

// QualityRecord 是一条「替换音质」的历史记录。
//
// 成功与失败都记（失败的 Note 里是原因）：只记成功的话，「我以为换了其实没换」
// 这种情况永远查不出来。
type QualityRecord struct {
	At      int64  `json:"at"` // Unix 秒
	Path    string `json:"path"`
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	From    string `json:"from"` // 替换前盘上的实际档位
	To      string `json:"to"`   // 替换后的实际档位（失败时为空）
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Note    string `json:"note,omitempty"`
	// By 记录是谁做的：`""`（缺省）= 用户在「替换音质」页手动点的；
	// `"upgrade"` = 后台音质补强轮自己试的。
	//
	// 为什么要分开：两件事结果相同（都是把有损换成无损），但**要不要动手**完全不同 ——
	// 手动那条是用户点了他就等着；后台那条是轮子每 20 分钟试一次，失败还会再来。
	// 不标出来，用户会以为「我什么时候点过这首歌？」（2026-10-23 的原话就是
	// 「出现音质补强的记录是什么意思，替换音质页面怎么没有同步的记录」）。
	By string `json:"by,omitempty"`
}

// qualityRecordLimit 是历史记录上限。替换是人工批量动作，100 条≈几十次操作，
// 再多就是存档而不是「最近做了什么」。
const qualityRecordLimit = 100

type qualityScanState struct {
	mu      sync.Mutex
	status  QualityStatus
	stop    chan struct{}
	running bool
}

// qualityCurrentTier 按**容器**判档位（无损容器 = 无损，其余 = 320k）。
//
// 名字与标签都可能骗人（真机见过 `xxx 无损.flac.mp3`），所以不看名字。
func qualityCurrentTier(ext string) string {
	if qualityLosslessExts[strings.ToLower(ext)] {
		return online.TierLossless
	}
	return online.Tier320k
}

// qualityCurrentTierOfFile 读盘上这个文件**实际**的档位。
//
// 判据在 `pkg/audioext.TierOfFile`：那里与下载器共用同一套 —— 两边各判一次的话，
// 会出现「界面说这条能升到 320k」而下载器回一句「已存在」的互相打脸（v2.17.2 修）。
func qualityCurrentTierOfFile(path, ext string) string {
	return audioext.TierOfFile(path)
}

// qualityNameKey 归一化歌名/歌手，用于「搜出来的这条是不是这首歌」的判断。
//
// 只做保守归一：小写、去空白、去常见括号后缀与标点。**不做**繁简/别名映射 ——
// 猜错一次就会把别人的无损换进你的库，那是比「没扫到」更糟的结果。
func qualityNameKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "（("); i > 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff: // CJK：保留
			b.WriteRune(r)
		}
	}
	return b.String()
}

// qualityMatchTitle 去掉文件名开头的曲序（`01 夜曲` → `夜曲`）。
//
// 为什么要：曲库里的文件常带曲序前缀（`01 夜曲.flac`、`03 - 歌名.mp3`），而平台的
// 曲名里没有。严格相等比对就会一律对不上 —— 真机实证：v2.34.1 修好「原生渠道根本
// 没跑」之后，`01 夜曲` 这类文件的原生列仍然是空的，因为它拿 `01夜曲` 去跟 `夜曲` 比。
//
// 只认「数字 + 分隔符」这一种形态：`10000 Reasons`、`7 Rings` 这种以数字开头的
// **歌名**不能被切（判据是紧跟数字的字符必须是空格或分隔符，且数字不超过 4 位）。
// 调用方还会同时用原样标题比一次，所以切错了也不会丢匹配。
func qualityMatchTitle(title string) string {
	s := strings.TrimSpace(title)
	n := 0
	for n < len(s) && n < 4 && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 || n >= len(s) {
		return s
	}
	// 紧跟数字的必须是分隔符。ASCII 的直接看字节；CJK 的分隔符是多字节，
	// 按 rune 解一个再比（byte 层面比会把 `、` 切成两半）。
	tail := s[n:]
	switch tail[0] {
	case ' ', '.', '-', '_', ')', ']':
		tail = tail[1:]
	default:
		if r, size := utf8.DecodeRuneInString(tail); r == '、' || r == '）' || r == '】' {
			tail = tail[size:]
		} else {
			return s
		}
	}
	rest := strings.TrimSpace(strings.TrimLeft(tail, " .-_)]、）】"))
	if rest == "" {
		return s
	}
	return rest
}

// nativeTrackFor 在**原生平台**上搜一次这首歌，用于回答「原生渠道能拿到什么档位」。
//
// 曲库文件没有平台 id，所以这条渠道必须从搜索开始 —— 而搜索是模糊的，这里只接受
// 歌名归一化后完全相等的那条（宁可这一列空着，也不要拿别人那首的结果去比）。
func (i *Interceptor) nativeTrackFor(ctx context.Context, title, artist string) online.Track {
	if !i.searchReady() || strings.TrimSpace(title) == "" {
		return online.Track{}
	}
	// 关键词用去掉曲序的标题去搜（`01 夜曲` → 搜 `夜曲`，命中率差很多）。
	base := qualityMatchTitle(title)
	keyword := strings.TrimSpace(base + " " + artist)
	if keyword == "" {
		keyword = strings.TrimSpace(title + " " + artist)
	}
	// 两个键都收：原样标题与去曲序标题。都算命中，`7 Rings` 这类数字开头的歌名
	// 就不会因为切错而丢匹配。
	want := qualityNameKey(title)
	wantTrimmed := qualityNameKey(base)
	// ⚠️ 只问**原生**平台（网易/QQ），不是 platforms() 那一长串 ——
	// 后者含用户启用的全部外挂源，串行搜一遍就是几十秒（见 nativePlatforms）。
	for _, p := range i.nativePlatforms() {
		for _, s := range i.nativeSearchCtx(ctx, keyword, p, 1, 3) {
			if k := qualityNameKey(s.Name); k != want && k != wantTrimmed {
				continue
			}
			id := songPlatformID(s)
			if id == "" {
				continue
			}
			return online.Track{
				Platform: p, PlatformID: id,
				Title: strings.TrimSpace(s.Name), Artists: []string{strings.TrimSpace(s.Singer)},
				Album: strings.TrimSpace(s.Album), Duration: durationSeconds(s),
				Quality: strings.TrimSpace(s.Quality),
			}.Normalized()
		}
	}
	return online.Track{}
}

// probeQuality 探一首歌在各渠道的可得档位。返回的 entry 里 `Channels` 按用户配的
// 渠道顺序排（与界面上那三行的顺序一致），`Best` 是其中实际档位最高的那条。
//
// 三道保险（2026-11-21）：
//  1. 单首围栏 qualityProbeBudget —— 到点就收手，不再等外网。
//  2. 单源预算 qualitySourceBudget —— 通过 ctx 传给 musicdl 侧，只掐探测腿。
//  3. 按段计时 —— 日志里能看到这秒数花在找候选还是解析，方便继续优化。
//
// 超时的语义是「**没探出**」而不是「探出没有」：entry.Best 为空、Upgradeable=false，
// 但 TimedOut=true —— 这条不会被算进 7 天免扫名单（见 qualityEntryFresh）。
func (i *Interceptor) probeQuality(ctx context.Context, path string) QualityEntry {
	return i.probeQualityWithBudget(ctx, path, qualityProbeBudget, qualitySourceBudget)
}

// probeQualityWithBudget 探一首，可指定两道预算；budget<=0 表示不额外加围栏
// （仍受传入 ctx 的约束）。
func (i *Interceptor) probeQualityWithBudget(ctx context.Context, path string, budget, sourceBudget time.Duration) QualityEntry {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	artist, title := namefmt.Parse(base)
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSpace(base)
	}
	entry := QualityEntry{
		Path: path, Title: strings.TrimSpace(title), Artist: strings.TrimSpace(artist),
		Ext: ext, CurrentTier: qualityCurrentTierOfFile(path, ext),
	}

	// 围栏从「开始找候选」就掐起 —— 慢的地方正是这里。
	pctx := ctx
	if budget > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	pctx = online.WithSourceBudget(pctx, sourceBudget)
	pctx = withLxBudget(pctx, qualityLxBudget)

	t0 := time.Now()
	track := i.nativeTrackFor(pctx, entry.Title, entry.Artist)
	searchElapsed := time.Since(t0)
	track.Title = entry.Title
	track.Artists = []string{entry.Artist}

	tm := &dlTimings{}
	attempts := i.collectDownloadAttemptsWith(pctx, track, i.downloadTiers(), tm)
	entry.Channels = channelTiersOf(attempts, i.downloadChannels())

	// 赢家：实际档位最高、同档位取渠道顺序靠前的那条（与落盘时的排序口径一致）。
	for n := range entry.Channels {
		ct := entry.Channels[n]
		if ct.Tier == "" {
			continue
		}
		if entry.Best == nil || offlineBetterChannel(ct, *entry.Best, i.downloadChannels()) {
			cp := ct
			entry.Best = &cp
		}
	}
	entry.Upgradeable = entry.Best != nil &&
		online.TierRank(entry.Best.Tier) > online.TierRank(entry.CurrentTier)

	// 超时 → 这条没有结论，不能进免扫名单。
	//
	// ⚠️ 只认**围栏**超时，不认「用户叫停」：叫停时 ctx 本身被取消，pctx 也会
	// 跟着报错，但那是「用户主动停的」，不是「探测不出来」，而且上面已经探到的
	// 渠道结果不该被抹掉。
	timedOut := pctx.Err() != nil && ctx.Err() == nil
	if timedOut {
		entry.TimedOut = true
		entry.Best = nil
		entry.Upgradeable = false
	}

	// 按段计时：这行是「优化到底有没有用」的证据。
	i.logf("[INTERCEPT] 音质探测 %s：总 %v（原生搜索 %v｜找候选 %v｜原生解析 %v｜洛雪 %v｜外挂池 %v）%s",
		entry.Title, time.Since(t0), searchElapsed, tm.Search, tm.Native, tm.Lx, tm.DL,
		map[bool]string{true: "［超时，没探出］", false: ""}[entry.TimedOut])
	return entry
}

// offlineBetterChannel 比较两条渠道结果：档位高的赢；同档位按**用户配的顺序**（越前越好）。
func offlineBetterChannel(a, b channelTier, channels []string) bool {
	ra, rb := online.TierRank(a.Tier), online.TierRank(b.Tier)
	if ra != rb {
		return ra > rb
	}
	return channelOrderIndex(channels, a.Channel) < channelOrderIndex(channels, b.Channel)
}

// ourChannelOrderIndex 取渠道在用户配置里的位置（不在列表里排最后）。
func channelOrderIndex(channels []string, want string) int {
	for n, ch := range channels {
		if ch == want {
			return n
		}
	}
	return len(channels)
}

// ── 扫描任务 ──

// StartQualityScan 起一次扫描。
//
// refresh=false（默认）会**跳过最近扫过、结论仍然有效的歌**：曲库里绝大多数文件
// 这几天的结论没变，重问一遍等于白等十几分钟（用户 2026-11-21 的反馈）。
// refresh=true 是「全部重扫」，用于怀疑结论过时的场合（例如刚换了音源）。
//
// 已有任务在跑时**返回当前状态**而不是并起第二个（扫描是外呼重活，两份并行
// 只是互相拖慢）。
func (i *Interceptor) StartQualityScan(mode string, limit int, refresh bool) (QualityStatus, error) {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}, fmt.Errorf("拦截层未就绪")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "all" {
		mode = "lossy"
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > qualityScanMaxLimit {
		limit = qualityScanMaxLimit
	}

	i.quality.mu.Lock()
	if i.quality.running {
		st := i.quality.status
		i.quality.mu.Unlock()
		return st, nil
	}
	i.quality.mu.Unlock()

	paths, truncated, err := i.qualityListLibraryFiles(mode, limit)
	if err != nil {
		return QualityStatus{State: QualityStateFailed, Error: err.Error()}, nil
	}

	// 历史结论放在锁外取：要逐条 stat 文件判断是否还新鲜，不该占着状态锁。
	prev := i.qualityStoreSnapshot()
	now := time.Now()
	todo := make([]string, 0, len(paths))
	skipped := 0
	if refresh {
		todo = append(todo, paths...)
	} else {
		for _, p := range paths {
			if e, ok := i.qualityStoreLookup(p); ok && i.qualityEntryFresh(e, now) {
				skipped++
				continue
			}
			todo = append(todo, p)
		}
	}

	i.quality.mu.Lock()
	if i.quality.running { // 两次加锁之间别人已经起了任务
		st := i.quality.status
		i.quality.mu.Unlock()
		return st, nil
	}
	i.quality.running = true
	i.quality.stop = make(chan struct{})
	// Entries 先放**上次的结论**：点「继续扫描」时看到的是「历史 + 这一次新增」，
	// 而不是一屏空白（「扫出来的歌全没了」有一半就是这么来的）。
	i.quality.status = QualityStatus{
		State: QualityStateRunning, Mode: mode,
		Total: len(todo), Truncated: truncated, Skipped: skipped,
		Entries: prev, Upgradeable: countQualityUpgradeable(prev),
	}
	i.quality.status.Updated = i.qualityStoreNewest()
	st := i.quality.status
	i.quality.mu.Unlock()

	if len(todo) == 0 {
		// 全都在 7 天内扫过 → 不必起 goroutine，直接把已有结论当本轮结果。
		i.finishQualityScan()
		return i.QualityScanStatus(), nil
	}
	go i.runQualityScan(todo)
	return st, nil
}

// finishQualityScan 收尾：状态回 done、排序、落盘。
func (i *Interceptor) finishQualityScan() {
	i.quality.mu.Lock()
	i.quality.running = false
	i.quality.stop = nil
	// 结果按「可替换优先 + 路径」排一下：用户打开这一页最想看的是能换的那批，
	// 而不是按文件系统遍历顺序（那个顺序在他眼里就是随机的）。
	sortQualityEntries(i.quality.status.Entries)
	i.quality.status.State = QualityStateDone
	i.quality.status.Upgradeable = countQualityUpgradeable(i.quality.status.Entries)
	i.quality.mu.Unlock()
	if i.qualityStore != nil {
		i.qualityStore.flush()
	}
}

// upsertQualityEntryLocked 把一条探测结果写进当前状态（同路径覆盖，不重复列）。
// 调用方必须持有 i.quality.mu。
//
// ⚠️ 超时的结果**不覆盖**已有的结论：超时是「没探出」，不是「探出没有」——
// 覆盖掉等于把一条有效的结论抹成空白。
func (i *Interceptor) upsertQualityEntryLocked(e QualityEntry) {
	for n := range i.quality.status.Entries {
		if i.quality.status.Entries[n].Path == e.Path {
			if e.TimedOut && !i.quality.status.Entries[n].TimedOut {
				return // 已有结论更值钱，保留
			}
			i.quality.status.Entries[n] = e
			return
		}
	}
	i.quality.status.Entries = append(i.quality.status.Entries, e)
	// 超上限时按「可替换优先 + 路径」裁掉尾巴：宁可少列几条不能换的，
	// 也不要因为历史结论占满名额而把这次新探出来的结果丢掉。
	if len(i.quality.status.Entries) > qualityScanMaxEntries {
		sortQualityEntries(i.quality.status.Entries)
		i.quality.status.Entries = i.quality.status.Entries[:qualityScanMaxEntries]
	}
}

// countQualityUpgradeable 数「能换」的条数（状态里那几个计数都从它来，
// 免得覆盖写、裁尾之后计数与明细对不上）。
func countQualityUpgradeable(list []QualityEntry) int {
	n := 0
	for _, e := range list {
		if e.Upgradeable {
			n++
		}
	}
	return n
}

// QualityScanStatus 读当前状态（未起过任务时是 idle）。
func (i *Interceptor) QualityScanStatus() QualityStatus {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}
	}
	i.quality.mu.Lock()
	st := i.quality.status
	// 刚启动/重启后状态里没有明细 → 用落盘的历史结论填一次（填完就写回 status，
	// 后续轮询不再重复 stat 文件）。
	if len(st.Entries) == 0 && !i.quality.running {
		if hist := i.qualityStoreSnapshot(); len(hist) > 0 {
			st.Entries = hist
			st.Upgradeable = countQualityUpgradeable(hist)
			i.quality.status.Entries = hist
			i.quality.status.Upgradeable = st.Upgradeable
		}
	}
	if st.State == "" {
		st.State = QualityStateIdle
	}
	st.Updated = i.qualityStoreNewest()
	if st.Updated == 0 {
		// 没有落盘时间时退回「明细里最新的一条」，界面至少能显示个时间。
		for _, e := range st.Entries {
			if e.ScannedAt > st.Updated {
				st.Updated = e.ScannedAt
			}
		}
	}
	i.quality.mu.Unlock()
	if st.Entries == nil {
		st.Entries = []QualityEntry{}
	}
	if recs := i.QualityRecords(); len(recs) > 0 {
		st.Records = recs
	} else {
		// 空也要给空数组：JSON 里 null 会让前端多写一层兜底（真机见过）
		st.Records = []QualityRecord{}
	}
	return st
}

// StopQualityScan 收回当前任务：已经探完的那部分**保留在状态里**（用户可以据此先换
// 一批），状态回到 done，再由下一次 start 覆盖。
func (i *Interceptor) StopQualityScan() QualityStatus {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}
	}
	i.quality.mu.Lock()
	if i.quality.running && i.quality.stop != nil {
		close(i.quality.stop)
		i.quality.stop = nil
	}
	i.quality.mu.Unlock()
	// 叫停也要落盘：用户停下来的那一批结论同样是有效的（重启后不该白丢）。
	if i.qualityStore != nil {
		i.qualityStore.flush()
	}
	return i.QualityScanStatus()
}

// qualityStoreLookup / qualityStoreNewest / qualityStoreSnapshot 是带 nil 兜底的
// 三个入口：qualityStore 在测试里可能是 nil（只测扫描逻辑时不必造盘）。
func (i *Interceptor) qualityStoreLookup(path string) (QualityEntry, bool) {
	if i.qualityStore == nil {
		return QualityEntry{}, false
	}
	return i.qualityStore.lookup(path)
}

func (i *Interceptor) qualityStoreNewest() int64 {
	if i.qualityStore == nil {
		return 0
	}
	return i.qualityStore.newest()
}

func (i *Interceptor) runQualityScan(paths []string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 外部叫停 → 取消在途探测（洛雪那条一次要好几秒，不能等它自己走完）。
	go func() {
		i.quality.mu.Lock()
		stop := i.quality.stop
		i.quality.mu.Unlock()
		if stop == nil {
			return
		}
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < qualityScanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				entry := i.probeQuality(ctx, paths[n])
				// 被叫停（用户点「停止」/任务取消）时就**不要**记结论：半途而废的探测
				// 结果会被存成「7 天内不用重扫」，等于把一次中断变成永久误判。
				stopped := ctx.Err() != nil
				if !stopped && entry.ScannedAt == 0 {
					entry.ScannedAt = time.Now().Unix()
				}
				i.quality.mu.Lock()
				i.upsertQualityEntryLocked(entry)
				i.quality.status.Scanned++
				if !stopped && entry.ScannedAt > i.quality.status.Updated {
					i.quality.status.Updated = entry.ScannedAt
				}
				i.quality.status.Upgradeable = countQualityUpgradeable(i.quality.status.Entries)
				i.quality.mu.Unlock()
				if !stopped && i.qualityStore != nil {
					i.qualityStore.put(entry)
				}
			}
		}()
	}
	for n := range paths {
		select {
		case jobs <- n:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	i.finishQualityScan()
}

// qualityLibraryRoots 返回「扫哪些目录」。
//
// ⚠️ 用**曲库目录**（设置里的 download_dir），**不是** `nas.AllowedRoots()`。
// 后者是「允许访问哪些路径」的**安全边界**（真机上是 `/vol1` `/vol2` `/vol3`
// 三个整卷），拿它当扫描根目录会去遍历整个卷 —— 2026-10-21 真机实测：扫出来的
// 40 条里全是应用自己数据目录下的 `tts_*` / uuid 命名的中间产物，一首真歌都没进
// 结果（而且那 40 条已经把 limit 的名额占满了）。安全边界与「音乐在哪」是两件事。
func (i *Interceptor) qualityLibraryRoots() []string {
	if i.cfg.DownloadDir != nil {
		if dir := strings.TrimSpace(i.cfg.DownloadDir()); dir != "" {
			return []string{dir}
		}
	}
	return nil
}

// qualityListLibraryFiles 列出曲库里要扫描的文件。
//
// mode=lossy（默认）只列**有损**文件：无损的没什么可换的，扫它纯属浪费外呼。
// mode=all 全列（用户想看看「我这库到底什么水平」）。
func (i *Interceptor) qualityListLibraryFiles(mode string, limit int) ([]string, bool, error) {
	roots := i.qualityLibraryRoots()
	if len(roots) == 0 {
		return nil, false, fmt.Errorf("没拿到曲库目录，先在设置里配好存储目录")
	}
	out := make([]string, 0, limit)
	truncated := false
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// 单个目录读不动（权限/被删）不该让整次扫描失败：跳过继续。
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// 回收站与隐藏目录不进扫描（那是整理流程的中间态）。
				if name := d.Name(); strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if !qualityAudioExts[ext] {
				return nil
			}
			if mode != "all" && qualityLosslessExts[ext] {
				return nil
			}
			if len(out) >= limit {
				truncated = true
				return fs.SkipAll
			}
			out = append(out, p)
			return nil
		})
		if err != nil && err != fs.SkipAll {
			return out, truncated, err
		}
		if truncated {
			break
		}
	}
	sort.Strings(out)
	return out, truncated, nil
}

// ── 替换 ──

// QualityReplaceDone / Failed 是 /api/quality/replace 的结果项。
type QualityReplaceDone struct {
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	NewPath string `json:"new_path,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Channel string `json:"channel,omitempty"`
	// From/Title/Artist 是给「替换记录」用的：没有它们，记录里只剩一个路径，
	// 用户看不出「这首原来是什么档位」。
	From   string `json:"from,omitempty"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// Note 是「这条为什么不算成功」的人话（例如平档不换）。界面直接显示它。
	Note string `json:"note,omitempty"`
}

type QualityReplaceFailed struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// ReplaceQuality 对给定的曲库文件逐个「按各渠道实际可得档位」重新取音并覆盖。
//
// 串行执行（不是并发）：这是**写**操作，且每条都要跑一遍外呼；串行让 50 条的结果
// 顺序稳定、日志可读，也避免同时往同一个存储写多个大文件。
//
// 一条失败不影响后面的：返回 done/failed 两张表，界面逐条显示。
func (i *Interceptor) ReplaceQuality(ctx context.Context, paths []string) ([]QualityReplaceDone, []QualityReplaceFailed, error) {
	done := make([]QualityReplaceDone, 0, len(paths))
	failed := make([]QualityReplaceFailed, 0)
	for _, raw := range paths {
		select {
		case <-ctx.Done():
			failed = append(failed, QualityReplaceFailed{Path: raw, Error: "已取消"})
			continue
		default:
		}
		item, err := i.replaceOneQuality(ctx, raw)
		if err != nil {
			failed = append(failed, QualityReplaceFailed{Path: raw, Error: err.Error()})
			// 失败也记一笔（Note 是原因）：只记成功的话，「我以为换了其实没换」
			// 这种情况永远查不出来。
			i.appendQualityRecord(QualityRecord{
				Path: raw, From: audioext.TierOfFile(raw), OK: false, Note: err.Error(),
			})
			continue
		}
		done = append(done, item)
		i.appendQualityRecord(QualityRecord{
			Path: item.Path, Title: item.Title, Artist: item.Artist,
			From: item.From, To: item.Tier, Channel: item.Channel, OK: item.OK, Note: item.Note,
		})
	}
	return done, failed, nil
}

func (i *Interceptor) replaceOneQuality(ctx context.Context, raw string) (QualityReplaceDone, error) {
	// 路径守卫：只允许曲库根目录内的文件（与整理/删除走同一道闸）。
	path, err := nas.ResolveSafePath(strings.TrimSpace(raw))
	if err != nil {
		return QualityReplaceDone{}, fmt.Errorf("路径不在曲库内：%w", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return QualityReplaceDone{}, fmt.Errorf("文件不存在或是目录")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if qualityLosslessExts[ext] {
		return QualityReplaceDone{}, fmt.Errorf("已经是无损（%s），不需要替换", strings.TrimPrefix(ext, "."))
	}

	entry := i.probeQualityWithBudget(ctx, path, qualityReplaceProbeBudget, 0)
	if entry.Best == nil || !entry.Upgradeable {
		// 区分「探出没有更高档位」与「没探出」—— 这两句话对用户意味着完全不同的
		// 下一步动作（前者别试了，后者等会儿再试）。
		if entry.TimedOut {
			return QualityReplaceDone{}, fmt.Errorf("这一轮没探完（超时），稍后再试一次")
		}
		return QualityReplaceDone{}, fmt.Errorf("各渠道都没有更高的档位：现在 %s，渠道最高 %s",
			entry.CurrentTier, bestTierText(entry))
	}
	best := *entry.Best

	track := i.nativeTrackFor(ctx, entry.Title, entry.Artist)
	track.Title = entry.Title
	track.Artists = []string{entry.Artist}
	tiers := i.downloadTiers()
	attempts := i.collectDownloadAttempts(ctx, track, tiers)
	var pick *dlAttempt
	for n := range attempts {
		if attempts[n].Channel == best.Channel {
			pick = &attempts[n]
			break
		}
	}
	if pick == nil {
		// 两次探测结果不一致（第三方站此刻不给了）：如实报「这一轮没拿到」，
		// 不要拿上一次的结果去下一条可能已经失效的直链。
		return QualityReplaceDone{}, fmt.Errorf("%s 渠道刚才还能给 %s，这次没解析出直链，稍后再试", best.Channel, best.Tier)
	}

	// 下载会**原地覆盖**这个文件，所以「改之前是什么档位」必须现在读 ——
	// 等下载完再读，读到的就是新档位，前后比较永远相等（这个坑当天就踩了一次）。
	before := audioext.TierOfFile(path)

	fake := "quality:" + fmt.Sprint(time.Now().UnixNano())
	// 落到**盘上那份文件本身**：目录与原文件名都按它来。按候选歌名落盘的话，
	// 真机实测会在曲库根目录多出一份新文件、旧的 128k 还赖在原地（2026-10-21）。
	item, err := i.postDownload(ctx, fake, pick.Cand, pick.Cand, pick.Res.URL, pick.Tier(), path)
	if err != nil {
		return QualityReplaceDone{}, err
	}
	// 成不成，看**盘上这份文件的档位有没有真的变高**，而不是看路径有没有变。
	//
	// 踩过的坑（2026-10-21 真机）：替换是**原地**做的（见 downloader 的 dest_dir/dest_base），
	// 所以成功时路径压根不变 —— 早先「路径没变 = 没换成」的判据会把成功的替换报成失败。
	// 反过来只信下载接口的应答也不行：它回 already_exists 时同样给回原路径。
	// 于是用磁盘事实说话：前后各读一次实际档位，没升就是没换。
	after := audioext.TierOfFile(item.Path)
	if audioext.TierRank(after) <= audioext.TierRank(before) {
		return QualityReplaceDone{
			Path: path, OK: false, NewPath: item.Path, Tier: after, Channel: pick.Channel,
			From: before, Title: entry.Title, Artist: entry.Artist,
			Note: fmt.Sprintf("文件还是 %s：这次从 %s 渠道拿到的也是 %s（旧的保留）", before, pick.Channel, after),
		}, nil
	}
	i.logf("[INTERCEPT] 替换音质：%s → %s（%s 渠道 %s）", filepath.Base(path), filepath.Base(item.Path), pick.Channel, pick.Tier())
	// 盘上档位真的变了 → 旧结论（「能换成 320k」）立刻作废，改成新档位 + 刚扫过，
	// 下次点扫描就不用再为这首白问一遍渠道。
	i.noteQualityReplaced(path, after, item.Path)
	return QualityReplaceDone{
		Path: path, OK: true, NewPath: item.Path, Tier: item.Quality, Channel: pick.Channel,
		From: before, Title: entry.Title, Artist: entry.Artist,
	}, nil
}

// noteQualityReplaced 在替换成功后同步「扫描结论」。
//
// 为什么必须同步：结论里存着「盘上档位」与「能不能换」。换了文件不同步，
// 下次加载历史时 qualityEntryFresh 会发现档位对不上而作废——虽然不会出错，
// 但会白重扫一遍；更糟的是界面上还会显示「这首能升级」（旧结论），
// 用户点了才发现已经换过了。
func (i *Interceptor) noteQualityReplaced(path, newTier, finalPath string) {
	if strings.TrimSpace(newTier) == "" {
		return
	}
	if strings.TrimSpace(finalPath) == "" {
		finalPath = path
	}
	ext := strings.ToLower(filepath.Ext(finalPath))
	e, ok := i.qualityStoreLookup(path)
	if !ok {
		base := strings.TrimSuffix(filepath.Base(finalPath), filepath.Ext(finalPath))
		artist, title := namefmt.Parse(base)
		if strings.TrimSpace(title) == "" {
			title = strings.TrimSpace(base)
		}
		e = QualityEntry{
			Path: finalPath, Title: strings.TrimSpace(title), Artist: strings.TrimSpace(artist),
		}
	}
	e.Path = finalPath
	e.Ext = ext
	e.CurrentTier = newTier
	e.Upgradeable = false
	e.Best = nil
	e.ScannedAt = time.Now().Unix()
	if i.qualityStore != nil {
		if finalPath != path {
			i.qualityStore.drop(path)
		}
		i.qualityStore.put(e)
		// 这条是用户当场看到的结论，立刻落盘（不用等攒够 20 条）。
		i.qualityStore.flush()
	}
	i.quality.mu.Lock()
	i.upsertQualityEntryLocked(e)
	i.quality.status.Upgradeable = countQualityUpgradeable(i.quality.status.Entries)
	i.quality.mu.Unlock()
}

func bestTierText(e QualityEntry) string {
	best := ""
	for _, ch := range e.Channels {
		if ch.Tier == "" {
			continue
		}
		if best == "" || online.TierRank(ch.Tier) > online.TierRank(best) {
			best = ch.Tier
		}
	}
	if best == "" {
		return "没探到"
	}
	return best
}

// ── 替换记录（需求 m04229：「替换音质，怎么看不到记录」）──────────────────
//
// 存 <DataDir>/quality_records.json，最多 qualityRecordLimit 条，最新的在前。
// 为什么不用内存环形缓冲：替换是**低频、人工触发、事后要查**的动作，
// 而应用会重启（每次装机/升级都重启一次），内存版本等于「一升级记录就没了」。

// QualityRecords 返回近期的替换记录（最新在前）。
func (i *Interceptor) QualityRecords() []QualityRecord {
	i.qrMu.Lock()
	defer i.qrMu.Unlock()
	if i.qrLoaded {
		return append([]QualityRecord(nil), i.qrList...)
	}
	i.qrLoaded = true
	i.qrList = loadQualityRecords(i.cfg.DataDir)
	return append([]QualityRecord(nil), i.qrList...)
}

// appendQualityRecord 追加一条记录并落盘。落盘失败只打日志：
// 「记录没写下去」不该让替换本身失败（文件已经换好了，那是既成事实）。
func (i *Interceptor) appendQualityRecord(rec QualityRecord) {
	if rec.At == 0 {
		rec.At = time.Now().Unix()
	}
	i.qrMu.Lock()
	if !i.qrLoaded {
		i.qrLoaded = true
		i.qrList = loadQualityRecords(i.cfg.DataDir)
	}
	// 最新在前
	i.qrList = append([]QualityRecord{rec}, i.qrList...)
	if len(i.qrList) > qualityRecordLimit {
		i.qrList = i.qrList[:qualityRecordLimit]
	}
	list := append([]QualityRecord(nil), i.qrList...)
	persist := i.qrPersistValue()
	i.qrMu.Unlock()
	if !persist {
		return
	}
	if err := saveQualityRecords(i.cfg.DataDir, list); err != nil {
		i.logf("[INTERCEPT] 替换记录落盘失败（不影响替换本身）：%v", err)
	}
}

func qualityRecordsPath(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "quality_records.json")
}

func loadQualityRecords(dir string) []QualityRecord {
	p := qualityRecordsPath(dir)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []QualityRecord
	if err := json.Unmarshal(b, &out); err != nil {
		// 坏文件不阻塞：当作没有记录（替换功能本身不受影响）
		return nil
	}
	return out
}

func saveQualityRecords(dir string, list []QualityRecord) error {
	p := qualityRecordsPath(dir)
	if p == "" {
		return nil
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// qrPersistValue 读「这条记录要不要落盘」（缺省 true）。
//
// 用 Once 而不是在 New() 里赋值：New() 有几十个调用点，逐个补参只会漏；
// 而「没设过 = 生产行为」正是我们要的缺省。
func (i *Interceptor) qrPersistValue() bool {
	i.qrPersistOnce.Do(func() {
		if !i.qrPersistSet {
			i.qrPersist = true
		}
	})
	return i.qrPersist
}
