package intercept

import (
	"context"
	"strings"
	"time"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/online"
)

// 每日推荐的第三层：大模型推荐。
//
// # 插在哪
//
// 现有两层是「在线推荐」（`dailyPlaylist` 里按种子搜）→「本地兜底」
// （`localDailyFill`，只补空位）。这一层插在**中间**：
//
//	在线（预留之后剩下的名额） → 大模型（vDailyLLMQuota 个名额） → 本地兜底（补空位）
//
// 为什么不是「只补空位」：参考实现是这么插的（`/tmp/fnme-z/proxy/recommend.py:1876`，
// llm 排在 netease-daily 之后、local-random 之前，只有前面不足 20 首才轮到它）。
// 但曲率这一层的**上游比它强得多** —— 在线层按 6 个种子词逐个搜、每平台 30 条候选，
// 几乎总能自己填满 20 个名额。照它那样「只补空位」，这一层实际上永远不会运行，
// 等于没做。所以给它**预留**名额，让它是真的在贡献。
//
// 为什么预留 5 个（不是更多）：在线那两层是**可播性最确定**的（在线层自己解析过直链、
// 本地层文件就在磁盘上），而这一层的输入是模型的文字、不可信。5/20 让它是看得见的
// 一层，同时把「模型胡说」的代价封在四分之一以内。
//
// # 开了开关不会让推荐变少
//
// 这一层没交出歌（没配置 / 超时 / 空回复 / 匹配不上 / 全被排除集滤掉）时，
// `dailyPlaylist` 会把名额**还给在线层**再补一轮，最后才轮到本地兜底。
// 开关关着时配额为 0，`dailyPlaylist` 走的代码路径与 v2.1.117 **逐字节相同**。
//
// # 走不走排除集：走
//
// 这一层出的每一首都要过 `vDailyExclude.has` —— 与在线层、本地层**同一套**。
// 理由：提示词里虽然写了「不要推荐他已经收藏 / 最近听过的」，但那是**请求**，
// 不是**保证** —— 模型可能没照做，也可能把同一首歌写成别的平台的写法而看起来没重复。
// 排除集里有「虚拟 id」与「歌名+歌手身份键」两条口径，后者正好挡住跨平台的重复。
// 另外这一层还要跟**已经在列表里的曲目**去重（`seen` 按真实 id、`titleArtistKey`
// 按歌名+歌手），否则模型推荐一首在线层已经推过的歌，用户会看到两条。
//
// # 发出去的是什么（字段级）
//
// 见 `pkg/ai.RecommendRequest` 的注释，拼装只发生在 `llmSeeds` / `llmSeedOf`
// 两处。一句话：**歌名 + 歌手 + 专辑名**，最多 20 条最近收听 + 20 条收藏。
// **不发**播放时间戳、播放次数、平台名、任何 id、用户标识、任何密钥。
//
// # 不许阻塞：后台跑 + 短等待
//
// 大模型调用慢（用户那台网关还忽略 `stream:false`、按 SSE 逐片返回），而每日推荐是
// 在**列歌单的请求里同步算出来的** —— 这一层绝不能把官方界面拖住。所以：
//
//  1. 这一路**发到后台**去跑（`startLLMCandidates`），硬上限 `vDailyLLMTimeout`；
//  2. 它是在做在线搜索**之前**发出去的，所以它的等待被在线搜索的时间遮住大半；
//  3. 请求路径只为它等 `vDailyLLMSyncWait`（默认 3 秒，短）。等不到就当这一层不存在，
//     照常交在线 + 本地两层的结果；
//  4. 后台那一路跑完后会把结果写进**当天的缓存**，并作废当天那份歌单缓存 ——
//     于是**下一次**请求就能拿到这一层，不用一直等。
//
// 第 4 条是为「一天只打开一次应用」的用户准备的：只等 3 秒的话他那天可能一次都看不到
// 这一层；有了它，至少下一次打开能看到。
//
// # 与参考实现的差异（照着做的部分见各函数注释）
//
//   - 参考实现把 llm 当「补空位」的层；这里改成**预留名额**（理由见上）。
//   - 参考实现不要求歌名严格匹配（`_match_score` 打完分就取最高那条，0 分也取）；
//     这里要求过 `sameTitle`（硬判据），宁可不推也不推错版本。
//   - 参考实现每首候选还要过一次 `verify_track_playable`（解析直链 + 头部探活）；
//     这里的 `collectOnline` 内部已经做过解析级的可播过滤，**不另加探活** ——
//     多一次 HEAD 请求换不来更多信息，还要多花时间。
//   - 参考实现把「已收藏 / 最近播放」写进提示词让模型自己避开，同时也在下游过滤；
//     这里照做（提示词里写了约束 2，下游也过排除集）。
//   - 参考实现没有独立开关（配了 `FNMUSIC_LLM_*` 就启用）；这里要求用户显式打开
//     `daily_llm_enabled`（理由见 `pkg/config.AppConfig.DailyLLMEnabled`）。

const (
	// vDailyLLMQuota 是大模型这一层在每日推荐里**预留**的名额。
	vDailyLLMQuota = 5

	// vDailyLLMAsk 是要模型给几条候选。
	//
	// 比名额多一倍：模型的候选要过搜索 + 歌名匹配 + 排除集三道，不可能全中。
	// 参考实现的 `LLM_CANDIDATE_COUNT` 默认 36（要填 20 个名额，比例 1.8）；
	// 这里 10 对 5，比例 2.0，同一条思路。
	//
	// ⚠️ 别调太大：每一条候选都要打一次搜索（`collectOnline`），而这是在后台跑的
	// —— 大了会拉长后台那一路，也会多打上游。
	vDailyLLMAsk = 10

	// vDailyLLMNegTTL 是「这次没拿到东西」的缓存时长。
	//
	// 与每日推荐自己的缓存 TTL（`vCacheTTL` 30 分钟）同口径：网关**偶尔返回空
	// content**（HANDOVER §4.2 坑 B，非故障），整天不再试一次就等于这一层当天消失；
	// 而重试的节奏最多与歌单重建同频，**不是每次请求**。
	vDailyLLMNegTTL = 30 * time.Minute
)

// vDailyLLMTimeout 是后台那一路（问模型 + 把候选搜成曲目）的硬上限。
//
// 是**变量**而不是常量：用例要把它调小来验「后台超时 → 这一层不参与」——
// 用真上限（60 秒）跑那条用例要干等一分钟，套件里不值得。同
// `onlineSearchBudget` 的处理方式。
//
// ⚠️ 它是这一路自己的上限；模型调用那一段还有 `pkg/ai` 自己的超时
// （`ai.Config.Timeout`，默认 20 秒）兜着，两者取更短的那个。
var vDailyLLMTimeout = 60 * time.Second

// vDailyLLMSyncWait 是**请求路径**最多为这一层等多久。
//
// 是变量而不是常量，理由同上（用例要把它调小到毫秒级，否则每个用例白等 3 秒）。
//
// 3 秒的来由：与 `onlineSearchWait`（3 秒）同量级 —— 那是项目对「这一步值不值得让
// 用户多等」既有的一把尺子。而且这一路是在线搜索**之前**发出去的，所以真正**多**等
// 的时间通常远小于 3 秒（在线搜索把这段等掉了大半）。
var vDailyLLMSyncWait = 3 * time.Second

// RecommendLLMFunc 是「拿种子换候选」的注入口（见 `Config.RecommendLLM`）。
type RecommendLLMFunc func(ctx context.Context, req ai.RecommendRequest) []ai.RecommendCandidate

// llmEntry 是「某一天、某个用户」的大模型候选在内存里的缓存条目。
//
// 缓存的是**已经搜到、已经确认可播的曲目**，不是模型给的文字。为什么缓存这一层
// 而不是文字：把候选搜成曲目是这个后台任务里**最贵**的一段（每条候选一次搜索），
// 而且搜索结果的平台内 id 是稳定的 —— 缓存它等于把「问模型」和「搜候选」两笔
// 开销一起省掉。排除集与跨层去重**不**在这里做（那要新鲜，见 fillFromLLM）。
type llmEntry struct {
	// done 在这一天的调用**跑完时**被 close（成功失败都 close）。
	//
	// 为什么用 close 当信号、结果放在字段里：同一天同一个用户的并发请求都该看到
	// 同一份结果，而 channel 只能把一份值交给一个接收者 —— 用「传值」的写法，
	// 第二个等待者会一直等不到东西（然后被超时兜住，白等一次）。
	done chan struct{}
	// day 是这条条目属于哪一天（`20060102`）。用来在插入新条目时清掉隔天的残留。
	day string
	// ts 是这一路**发起**的时间，用于判定负结果过期没有。
	ts time.Time
	// tracks 是搜到并确认可播的曲目（还没过排除集）。只在 close(done) 之前写一次。
	tracks []online.Track
}

// usable 判断这条缓存还能不能用。
//
// 规则：
//   - 还在路上（done 没关）→ 能用（复用同一次调用，不另发一路）；
//   - 有结果 → 能用（键里带日期，隔天自然换）；
//   - 没结果 → 只留 `vDailyLLMNegTTL`，过期就重试（网关偶尔空回复，得给它机会）。
func (e *llmEntry) usable(now time.Time) bool {
	select {
	case <-e.done:
		if len(e.tracks) > 0 {
			return true
		}
		return now.Sub(e.ts) < vDailyLLMNegTTL
	default:
		return true
	}
}

// SetRecommendLLM 注入大模型候选生成器（nil = 这一层不存在）。
//
// 为什么是 setter 而不是构造参数：AI 客户端由 `api.Server` 持有，而拦截层在
// `api.NewServer` **之前**就建好了（见 main.go 的顺序）。所以只能先建、后注。
//
// ⚠️ **必须在开始服务之前调用**（main.go 里是在 `httpServer.Serve` 之前）：
// 它写的是 `cfg`，而 `cfg` 在服务期间被并发读。
func (i *Interceptor) SetRecommendLLM(fn RecommendLLMFunc) {
	if i == nil {
		return
	}
	i.cfg.RecommendLLM = fn
}

// llmQuota 返回这一层预留的名额；0 = 这一层不在场。
//
// 两道门，缺一不可（见 `Config.RecommendLLM` / `Config.DailyLLMEnabled`）：
//   - 有没有模型可调（注入的实现在不在）；
//   - 用户同不同意把收听历史发出去（配置开关）。
//
// 这里**不判**「AI 配好了没有」—— 那是 `pkg/ai` 自己的静默降级（未配置时
// `Recommend` 返回 nil）。两处都判等于把同一条判断写两遍，迟早不一致。
func (i *Interceptor) llmQuota() int {
	if i.cfg.RecommendLLM == nil {
		return 0
	}
	if i.cfg.DailyLLMEnabled == nil || !i.cfg.DailyLLMEnabled() {
		return 0
	}
	return vDailyLLMQuota
}

// llmDayKey 是这一层的缓存键：用户 + 当天。跨天就换了（所以键里带日期，
// 不需要额外的过期扫描）。
func llmDayKey(user string, now time.Time) string {
	return user + "\x00" + now.Format("20060102")
}

// startLLMCandidates 拿到「当天的候选曲目」：已有缓存就用缓存，没有就发一路后台去跑。
//
// 返回值可能是 nil（这一层不在场），调用方必须先判。
func (i *Interceptor) startLLMCandidates(ctx context.Context, user string, now time.Time, quota int) *llmEntry {
	if quota <= 0 || i.cfg.RecommendLLM == nil {
		return nil
	}
	key := llmDayKey(user, now)
	day := now.Format("20060102")

	i.llmMu.Lock()
	if e, ok := i.llmCache[key]; ok && e.usable(now) {
		i.llmMu.Unlock()
		return e
	}
	e := &llmEntry{done: make(chan struct{}), day: day, ts: now}
	i.llmCache[key] = e
	// 顺手清掉不是今天的条目：键里带日期，隔天的永远不会再命中。
	// （不清的话这张表会随「用过的用户数 × 天数」无限长下去。）
	for k, v := range i.llmCache {
		if v.day != day {
			delete(i.llmCache, k)
		}
	}
	i.llmMu.Unlock()

	// 种子在这里、也只在这里收集 —— 见 llmSeeds 的隐私说明。
	//
	// ⚠️ `vDailyLLMTimeout` 在**这里**（调用方 goroutine）读成一个值再交给后台：
	// 后台那一路就不再读这个包级变量了。它是可变的（用例要调小它），而后台 goroutine
	// 与「用例改完再改回来」之间没有任何同步 —— 让后台去读就是一个真实的数据竞争
	// （race 检测器抓到过）。上限本来也是「发起那一刻定的」，读一次正是它的语义。
	timeout := vDailyLLMTimeout
	go i.runLLMCandidates(ctx, user, now, e, i.llmSeeds(user), timeout)
	return e
}

// runLLMCandidates 是后台那一路：问模型 → 把候选搜成可播曲目 → 落进缓存。
//
// `timeout` 由调用方读好传进来（见 startLLMCandidates 的说明），这里不读包级变量。
//
// 它**不**碰排除集、**不**碰已经在列表里的曲目 —— 那两件事要按「用的时候」的状态
// 来判（用户可能刚刚听完其中一首），所以留在请求路径上做（见 fillFromLLM）。
func (i *Interceptor) runLLMCandidates(ctx context.Context, user string, now time.Time, e *llmEntry, req ai.RecommendRequest, timeout time.Duration) {
	defer close(e.done)

	// ⚠️ 用 `WithoutCancel` 派生：这次请求就算被客户端放弃（用户切走了、官方前端
	// 超时了），这一路也会跑完并把结果写进当天的缓存 —— 下一个请求（下一次重建
	// 歌单）就直接命中。不用它的话，今天第一个请求一取消，这一层整天都拿不到。
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	cands := i.cfg.RecommendLLM(cctx, req)
	tracks := i.matchLLMCandidates(cctx, cands)

	// 加锁写、加锁读。
	//
	// ⚠️ 说实话：**这把锁是冗余的**。真正提供同步的是下面那句 `defer close(e.done)`
	// —— 写发生在 close 之前，而所有读（`fillFromLLM` 与 `usable`）都发生在「观察到
	// close」之后，happens-before 已经成立（变异校验把这两行锁去掉，用例与 -race
	// 照样全绿）。留着它是不想让正确性依赖这条推理：将来有人在**没等 done** 的地方
	// 读 `e.tracks` 也不会错。
	i.llmMu.Lock()
	e.tracks = tracks
	i.llmMu.Unlock()

	if len(tracks) == 0 {
		i.logf("[INTERCEPT] 大模型推荐：%d 条候选没有一条搜成可播曲目，这一层本次不参与",
			len(cands))
		return
	}
	i.logf("[INTERCEPT] 大模型推荐：%d 条候选 → %d 首可播（已进当天缓存）",
		len(cands), len(tracks))

	// 作废当天那份歌单缓存，让**下一次**请求重算时把这一层带上。
	//
	// 为什么要这一步：请求路径只等 vDailyLLMSyncWait（短），慢网关这一路基本等不到；
	// 不作废的话那份「还没有大模型层」的歌单会被缓存 30 分钟，用户再打开也看不到。
	//
	// ⚠️ 这是**尽力而为**，不是保证：如果这次作废恰好发生在某个请求「算完还没写缓存」
	// 的那一瞬间，那一份仍然会把这一层漏掉，要等下一个 30 分钟的窗口才补上。
	// 代价只是「晚一个缓存窗口看到」，不值得为它加锁串行化。
	i.invalidateDailyPlaylist(user, now)
}

// invalidateDailyPlaylist 把当天那份「每日推荐」从歌单缓存里摘掉，逼下一次请求重算。
func (i *Interceptor) invalidateDailyPlaylist(user string, now time.Time) {
	guid := vDailyGUID(now, user)
	i.vMu.Lock()
	delete(i.vCache, guid)
	i.vMu.Unlock()
}

// fillFromLLM 把大模型那一层并进列表（最多补到 target）。
//
// 这里做的事**全是廉价、且必须按「用的时候」的状态来做的**：
//   - 等一会儿（最多到 deadline，从这一路发起那一刻算起）；
//   - 过**当前的**排除集（不是发起那一刻的 —— 他可能刚听完其中一首）；
//   - 与已经在列表里的曲目去重（真实 id + 歌名/歌手身份键）。
//
// 贵的部分（问模型、把候选搜成曲目）都在后台，见 runLLMCandidates。
func (i *Interceptor) fillFromLLM(e *llmEntry, deadline time.Time, ex vDailyExclude, seen map[string]bool, out *[]online.Track, target int) {
	if e == nil || len(*out) >= target {
		return
	}
	if !waitLLMReady(e, deadline) {
		return
	}

	i.llmMu.Lock()
	tracks := e.tracks
	i.llmMu.Unlock()

	// 与已经在列表里的曲目去重。`seen` 只按真实 id —— 而模型推荐的是**歌名文字**，
	// 搜出来可能是**另一个平台**的同名曲目（虚拟 id 是「平台 + 平台内 id」的哈希，
	// 只比 id 挡不住）。所以这里再按「歌名 + 歌手」比一次。
	picked := make(map[string]bool, len(*out)+len(tracks))
	for _, t := range *out {
		if k := titleArtistKey(t.Title, t.Artist()); k != "" {
			picked[k] = true
		}
	}

	for _, t := range tracks {
		if len(*out) >= target {
			break
		}
		if seen[t.RealID()] || ex.has(t.FakeID(), t) {
			continue
		}
		k := titleArtistKey(t.Title, t.Artist())
		if k != "" && picked[k] {
			continue
		}
		if k != "" {
			picked[k] = true
		}
		seen[t.RealID()] = true
		*out = append(*out, t)
	}
}

// waitLLMReady 等到这一路跑完（最多到 deadline）；等不到返回 false。
//
// 先做一次非阻塞探测再进 select：`deadline` 已经过去时 `time.After(<=0)` 会立刻就绪，
// 而 select 在多个就绪分支之间是**随机**挑的 —— 不先看一眼就可能把一份已经拿到的
// 结果白跳过去。
func waitLLMReady(e *llmEntry, deadline time.Time) bool {
	select {
	case <-e.done:
		return true
	default:
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return false
	}
	select {
	case <-e.done:
		return true
	case <-time.After(wait):
		return false
	}
}

// matchLLMCandidates 把模型给的歌名 / 歌手**当搜索关键词**，搜出真能播的曲目。
//
// # 为什么不能直接把模型的字符串塞进列表
//
// 它可能编造不存在的歌、可能把歌手写错、可能说了一首歌却指向完全不同的版本。
// 所以每一步都走**既有的**链路：
//
//  1. 关键词搜索 → `collectOnline`。它内部已经做了可播性过滤（解析不出直链的曲目
//     不进结果）—— 与搜索合并、榜单、虚拟歌单**同一条 `playableOnly` 口径**。
//     所以这里不另做一遍可播过滤，也不需要额外的探活。
//  2. 歌名必须过 `sameTitle` —— 既有那套「剥掉括号注释与连字符后缀之后完全相等」
//     的硬判据。**不做模糊包含**：那会把「海屿你」与「海屿你心碎版」判成同一首。
//  3. 歌手宽松匹配（`artistMatches`，一方包含另一方即算过）。
//
// ⚠️ **时长这一关在这里用不上，不是漏了**：`durationMatches` 要一个「原曲时长」
// 当基准，而模型给的候选**没有时长字段**。按项目自己的口径，任一方缺时长时
// `durationMatches` **不参与判定**（见 lxsource.go:27 与 acquire.go:54）——
// 那是「平台没给这个字段」，不是「对不上」。这里根本拿不到基准，所以不判。
//
// 每条候选**最多贡献 1 首**：一条推荐就是一首歌，多取只会让这一层被一个候选占满
// （参考实现 `recommend.py:1301` 的 `want = 1 if title else 3` 同一条口径）。
func (i *Interceptor) matchLLMCandidates(ctx context.Context, cands []ai.RecommendCandidate) []online.Track {
	if len(cands) == 0 {
		return nil
	}
	out := make([]online.Track, 0, len(cands))
	seen := make(map[string]bool, len(cands))

	for idx, c := range cands {
		// 后台那一路有总上限（vDailyLLMTimeout）：到点了就停，别再往下搜 ——
		// 每条候选都是一次真实的上游搜索，不能"反正后台"就无限往下打。
		if ctx.Err() != nil {
			i.logf("[INTERCEPT] 大模型推荐：匹配预算用完，剩下的 %d 条候选不再搜",
				len(cands)-idx)
			break
		}
		for _, t := range i.matchOneLLMCandidate(ctx, c) {
			if seen[t.RealID()] {
				continue
			}
			seen[t.RealID()] = true
			out = append(out, t)
		}
	}
	return out
}

// matchOneLLMCandidate 把一条候选搜成曲目：先按「歌手 + 歌名」搜，没命中再退到
// 「歌名」、再退到「歌手」。
//
// 「退到只搜歌手」这一步是从参考实现学的（`recommend.py:1277`）：模型有时把歌名
// 写得搜不出来（多带个副标题、换了个译名），但歌手是对的 —— 单搜歌手还能从结果里
// 捞到 `sameTitle` 命中的那一首。**仍然要过 sameTitle**，所以这不是放宽判据，
// 只是换一个关键词去搜。
//
// 退档只在**前一次一条都没命中**时发生，所以正常情况下每条候选只花一次搜索。
func (i *Interceptor) matchOneLLMCandidate(ctx context.Context, c ai.RecommendCandidate) []online.Track {
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return nil
	}
	artist := strings.TrimSpace(c.Artist)

	keywords := make([]string, 0, 3)
	if artist != "" {
		keywords = append(keywords, artist+" "+title)
	}
	keywords = append(keywords, title)
	if artist != "" {
		keywords = append(keywords, artist)
	}

	for _, kw := range keywords {
		for _, t := range i.collectOnline(ctx, kw) {
			if !sameTitle(title, t.Title) {
				continue
			}
			if !artistMatches(artist, t) {
				continue
			}
			return []online.Track{t}
		}
	}
	return nil
}

// artistMatches 判断搜索结果的歌手里有没有候选说的那个歌手。
//
// 宽松（一方包含另一方即算过）而不是相等：模型常写「周杰伦 / 方文山」
// 「周杰伦 feat. 五月天」，而各平台对合作曲目的歌手串写法也不统一。
// 这里的宽松是**安全**的 —— 歌名那一关已经用 `sameTitle` 卡死了，歌手只用来在
// 几个同名结果里挑对的那个。
//
// ⚠️ 结果里**一个歌手都没有**时返回 true（不参与判定），与 `durationMatches`
// 的「缺字段 = 没有信息，不是对不上」是同一条口径 —— 拿它当「对不上」会让所有
// 不报歌手的源都用不上。
func artistMatches(want string, t online.Track) bool {
	w := normalizeArtistText(want)
	if w == "" {
		return true
	}
	known := false
	for _, a := range t.Artists {
		if strings.TrimSpace(a) == "" || a == online.UnknownArtist {
			continue
		}
		known = true
		n := normalizeArtistText(a)
		if strings.Contains(n, w) || strings.Contains(w, n) {
			return true
		}
	}
	return !known
}

// normalizeArtistText 压平歌手串：小写 + 去空白。只用于匹配，不用于展示。
func normalizeArtistText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), "")
}

// llmSeeds 收集要发给模型的种子。
//
// ⚠️ **这是整个项目里唯一一处往提示词里塞用户数据的地方**，而 `llmSeedOf` 是唯一
// 决定「塞哪几个字段」的函数。字段级清单见 `pkg/ai.RecommendRequest`：
// 只放歌名 / 歌手 / 专辑名。
//
// 最近收听取 `History` 的前 N 条 —— store 的 History 已经是「新的在前」，
// 所以前 N 条就是最近 N 次播放（与 `dailyExclude` 的窗口口径一致）。
func (i *Interceptor) llmSeeds(user string) ai.RecommendRequest {
	req := ai.RecommendRequest{Count: vDailyLLMAsk}
	for _, h := range i.store.History(user) {
		if len(req.Recent) >= ai.RecommendMaxRecent {
			break
		}
		req.Recent = append(req.Recent, llmSeedOf(h.Track))
	}
	for _, f := range i.store.Favorites(user) {
		if len(req.Favorites) >= ai.RecommendMaxFavorites {
			break
		}
		req.Favorites = append(req.Favorites, llmSeedOf(f.Track))
	}
	return req
}

// llmSeedOf 把一条曲目压成种子。**改这里就是改隐私面**，别往里加字段。
//
// 只取歌名 / 歌手 / 专辑名三个**唱片封底上就有的公开元数据**。刻意不取：
//   - `PlayedAt` / 播放次数：那是**行为**数据（他什么时候听的、听了几次），
//     而推荐只需要口味；
//   - `Platform` / `PlatformID` / 虚拟 id：那是他在哪个平台听，与口味无关，
//     而且是能对上账户的标识；
//   - 任何用户标识：提示词里根本不需要。
//
// 占位歌手（`未知艺术家`）压成空串：把占位名发给模型是纯噪声。
func llmSeedOf(t online.Track) ai.RecommendSeed {
	artist := strings.TrimSpace(t.Artist())
	if artist == online.UnknownArtist {
		artist = ""
	}
	return ai.RecommendSeed{
		Title:  strings.TrimSpace(t.Title),
		Artist: artist,
		Album:  strings.TrimSpace(t.Album),
	}
}
