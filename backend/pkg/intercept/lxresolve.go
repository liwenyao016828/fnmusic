package intercept

import (
	"context"
	"fmt"
	"strings"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
)

// 洛雪候选在池里的**取链**那一半。
//
// ⚠️ 2026-10-08 补。在此之前 `lxCandidates` 只做了「搜索」—— 池里根本没有
// `lx:<src>` 的解析器。于是洛雪候选哪怕被 `pickBestQuality` 选中，
// `pool.Resolve` 也只会返回 `ErrNoResolver`，下载路径继续试下一个候选，
// 最后还是回落曲目自己平台（网易免登录 = 320k mp3）。
// 用户看到的正是「我 lx 音乐怎么没有参与到下载」。
//
// # 为什么取链要的不是一个 id
//
// LX 脚本的 `musicUrl` 读 `info.musicInfo`（宿主替我们嵌好这一层），而很多脚本
// 还要读 `musicInfo._types[档位]` 才能给出直链 —— 也就是**搜索时那条记录**。
// 所以搜索结果要原样暂存下来，解析时原样喂回去（与 musicdl 那条路同一个理由，
// 见 `online.QueryResolver` 的说明）。

// lxItemLimit 是暂存上限。
//
// 它只是「刚才搜过的那几条」，不是索引：一次下载的候选只有几条。
// 到上限就丢一条最旧的（线性删一条即可，n 只有几百，LRU 不值当）。
const lxItemLimit = 512

// ensureLxResolver 给某个洛雪源注册解析器：**池里没有这个平台才注册**。
//
// ⚠️ 不能无条件 `Register` —— 池里的 Register 是**覆盖**语义，而无条件注册会把
// 调用方（单测里那个 stub、或将来别处注入的替身）已经装好的解析器顶掉。
// 实测就是这么红的：用例先装 stub、`rankedSources` 里的搜索再把它盖掉，
// 于是「该解析成功」变成 ErrNoResolver。
//
// 用 Supports 判一下还有第二个好处：不用另维护一张「注册过哪些源」的表
// （那张表本身就是个会漂的状态）。
func (i *Interceptor) ensureLxResolver(src string) {
	if i.pool == nil || src == "" {
		return
	}
	platform := lxSourcePrefix + src
	if i.pool.Supports(platform) {
		return
	}
	i.pool.Register(lxResolver{i: i, src: src})
}

// rememberLxItem 暂存一条搜索记录（键 = 源 + 平台内 id）。
func (i *Interceptor) rememberLxItem(src, id string, raw map[string]any) {
	if src == "" || id == "" || raw == nil {
		return
	}
	i.lxMu.Lock()
	defer i.lxMu.Unlock()
	if i.lxItems == nil {
		i.lxItems = map[string]map[string]any{}
	}
	if len(i.lxItems) >= lxItemLimit {
		for k := range i.lxItems {
			delete(i.lxItems, k)
			break
		}
	}
	i.lxItems[src+"\x00"+id] = raw
}

// lxItem 取回暂存的那条记录；没有就返回 nil。
func (i *Interceptor) lxItem(src, id string) map[string]any {
	i.lxMu.Lock()
	defer i.lxMu.Unlock()
	return i.lxItems[src+"\x00"+id]
}

// lxBestTier 从脚本那条记录里取它**自己声明**的最高档（`_types` / `types` 的键）。
//
// 拿它去要直链：脚本声明过它给得出，我们才要。声明里没有档位就返回空串，
// 交给宿主用它的默认值 —— **不替脚本编一个档位**。
func lxBestTier(info map[string]any) string {
	if info == nil {
		return ""
	}
	types, _ := info["_types"].(map[string]any)
	if types == nil {
		types, _ = info["types"].(map[string]any)
	}
	best := ""
	for k := range types {
		if online.TierRank(k) > online.TierRank(best) {
			best = k
		}
	}
	return best
}

// lxFormatFromTier 给解析结果一个容器名。
//
// 洛雪的直链常常没有扩展名（形如 `https://…/api?token=…`），留空会让
// 「边听边存」落盘的文件没有扩展名；按档位给一个比留空好。
func lxFormatFromTier(tier string) string {
	t := strings.ToLower(strings.TrimSpace(tier))
	switch {
	case strings.HasPrefix(t, "flac"):
		return t
	case t == "ape" || t == "wav" || t == "dsd":
		return t
	}
	return "mp3"
}

// lxResolver 是池里 `lx:<src>` 的解析器。
type lxResolver struct {
	i   *Interceptor
	src string
}

// Platform 实现 online.Resolver。前缀必须带 —— 洛雪脚本自己的源 key
// （wy / tx / kg / mg）与曲率的平台代号会撞名，而那些代号在池里另有解析器。
func (r lxResolver) Platform() string { return lxSourcePrefix + r.src }

// Resolve 实现 online.Resolver。
func (r lxResolver) Resolve(ctx context.Context, platformID string) (*online.Resolved, error) {
	return r.resolveOne(ctx, platformID, "", "", "")
}

// ResolveAtTier 实现 online.TierResolver。
//
// # 为什么洛雪也要能按档位问
//
// 脚本的 `musicUrl` 接受档位参数（宿主放在 `info.type` 上，见 lxnode.Resolve），
// 而且脚本内部会按它声明的 `qualitys` 往下压（`mapQuality` clamp）——
// 也就是说「问 flac」在脚本不支持无损时**不会失败，只会给低一档**。
// 那就没有理由不按阶梯问：问一次 flac，脚本给不出无损自己就降级了，
// 比我们先问 320k 再猜要好。
//
// # 失败一律当「这条候选取不到」
//
// 洛雪宿主的 /resolve 会**逐个脚本**试（每个最多 8s），一条候选失败就要几十秒。
// 报 ErrTierUnavailable 会让阶梯把同一批脚本再问三遍（×3 的等待，拿到的还是同样的结论）。
// 所以这里把错误统一成 ErrNotPlayable —— 池立刻换下一个候选，不再无效重试。
func (r lxResolver) ResolveAtTier(ctx context.Context, platformID, tier string) (*online.Resolved, error) {
	res, err := r.resolveOne(ctx, platformID, "", "", tier)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, online.ErrNotPlayable
	}
	return res, nil
}

// ResolveBatch 实现 online.Resolver：**逐条**解析。
//
// 洛雪宿主是「一次一条」的接口（`POST /resolve`），而脚本不是为并发写的 ——
// 批量并发只会让脚本内部状态互相踩。
func (r lxResolver) ResolveBatch(ctx context.Context, platformIDs []string) (map[string]*online.Resolved, error) {
	out := make(map[string]*online.Resolved, len(platformIDs))
	var lastErr error
	for _, id := range platformIDs {
		res, err := r.resolveOne(ctx, id, "", "", "")
		if err != nil {
			lastErr = err
			continue
		}
		out[id] = res
	}
	return out, lastErr
}

// ResolveQueries 实现 online.QueryResolver：带歌名/歌手进来时用得上
// （缓存里没有那条记录时，至少还能拼一条最小的 info 去试）。
func (r lxResolver) ResolveQueries(ctx context.Context, queries []online.Query) (map[string]*online.Resolved, error) {
	out := make(map[string]*online.Resolved, len(queries))
	var lastErr error
	for _, q := range queries {
		res, err := r.resolveOne(ctx, q.ID, q.Title, q.Artist, "")
		if err != nil {
			lastErr = err
			continue
		}
		out[q.ID] = res
	}
	return out, lastErr
}

// resolveOne 解析一条。tier 为空 = 用脚本声明的最高档（再没有就交给宿主默认）。
func (r lxResolver) resolveOne(ctx context.Context, id, title, artist, tier string) (*online.Resolved, error) {
	// 只有「单条」与「轮询」两个钩子**都没有**时才算没有解析器 ——
	// 轮询模式自己就能拿链，不需要 lxResolve 存在（单测/新宿主都走这条）。
	if r.i == nil || (r.i.lxResolve == nil && r.i.lxResolveAll == nil) {
		return nil, online.ErrNoResolver
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, online.ErrNotPlayable
	}

	info := r.i.lxItem(r.src, id)
	if info == nil {
		// 暂存里没有（进程重启过、或这条候选是别的路进来的）：拼一条最小的。
		// 有些脚本只认 `_types`，这条会失败 —— 但那是一次**明确的失败**，
		// 上层会换下一个候选，而不是静默下错东西。
		info = map[string]any{"songmid": id, "id": id, "name": title, "singer": artist}
	}

	if tier == "" {
		// 没点名档位：用脚本**自己声明**的最高档（`_types` 的键）。
		tier = lxBestTier(info)
	}
	if tier == "" {
		// 连声明都没有：问无损。脚本自己会按它声明的 qualitys 往下压
		// （mapQuality clamp），所以「问高一点」不会失败，只会得到它给得起的档位。
		tier = online.TierLossless
	}
	// ── 选链：多选洛雪音源时按用户排的顺序**轮询**，取实际档位最高的那条 ──
	//
	// 2026-10-21 用户要求：「多选显示轮询，实际功能也是多选轮询最高音质」。
	// 在此之前宿主是「第一个吐出直链的脚本就赢」——顺序还是目录顺序，不是用户排的，
	// 而且第一个给 128k 就再无机会拿到第二个能给的无损。
	if r.i.lxResolveAll != nil {
		scripts := r.i.lxScriptNames()
		cands, err := r.i.lxResolveAll(ctx, r.src, info, tier, scripts)
		if err == nil {
			return r.i.pickLxCandidate(r.src, id, info, tier, scripts, cands)
		}
		// 轮询整体失败不直接放弃：宿主可能是老版本（不认 collect，回 502），
		// 而单条那条路以前就是能用的 —— 不能因为升级了取链方式就把它弄坏。
		if r.i.lxResolve == nil {
			return nil, err
		}
		r.i.logf("[INTERCEPT] 洛雪轮询取链失败，退回单条问一次：%s/%s %v", r.src, id, err)
	}

	url, size, err := r.i.lxResolve(ctx, r.src, info, tier)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(url) == "" {
		return nil, online.ErrNotPlayable
	}
	actual := r.i.lxHonestTier(r.src, id, info, tier, url, size)
	return &online.Resolved{
		URL:    url,
		Format: lxFormatFromTier(actual),
		// 档位是「我们点的那一档」或脚本声明的档位，不是随手猜的。
		Tier: actual,
	}, nil
}

// lxScriptNames 取用户选中且在排序里的洛雪音源脚本名（nil = 没接这条线）。
func (i *Interceptor) lxScriptNames() []string {
	if i == nil || i.lxSourceNames == nil {
		return nil
	}
	names := i.lxSourceNames()
	// 拷贝一份：调用方（配置管理器）给回来的切片不该被我们改，
	// 而这里后面会按用户顺序排序。
	out := make([]string, 0, len(names))
	for _, n := range names {
		if strings.TrimSpace(n) != "" {
			out = append(out, n)
		}
	}
	return out
}

// lxHonestTier 把「脚本声称的档位」校正成**实际**档位（两道诚实判据）。
//
// 第一道看直链后缀（脚本可能把无损请求压成有损），第二道用字节数 + 时长反推码率
// ——「3 分钟 4MB」无论叫什么名字都不是无损。抽成函数是因为轮询模式下每条候选
// 都要走同一套校正，各写一遍迟早两套口径。
func (i *Interceptor) lxHonestTier(src, id string, info map[string]any, tier, url string, size int64) string {
	actual := tier
	if online.TierRank(actual) >= online.TierRank(online.TierLossless) && lxURLLooksLossy(url) {
		actual = online.Tier320k
	}
	if fixed, why := lxTierFromSize(size, lxDurationOf(info), actual); fixed != actual {
		i.logf("[INTERCEPT] 洛雪档位按实测校正：%s/%s 声明 %s → 按 %s（%s）", src, id, actual, fixed, why)
		actual = fixed
	}
	return actual
}

// pickLxCandidate 从轮询交回来的候选里挑一条：**实际档位最高的**，同档取顺序靠前的。
//
// 顺序就是用户在设置页排的顺序（传给宿主的 scripts 数组顺序）——「轮询」落在两处：
// 宿主按这个顺序挨个问，同档位时先问到的赢（稳定、可预期，不会每次换一条）。
// 探测「没结论」的候选（Unproven）排在最后：它们既没被证明能下、也没被证明死了，
// 只在别的候选都不行时才用。
func (i *Interceptor) pickLxCandidate(src, id string, info map[string]any, tier string, scripts []string, cands []lxnode.Candidate) (*online.Resolved, error) {
	order := map[string]int{}
	for n, name := range scripts {
		order[name] = n
	}
	type scored struct {
		cand     lxnode.Candidate
		actual   string
		rank     int
		pos      int
		unproven bool
	}
	var best *scored
	// better 判断「a 是否该顶掉当前最优 b」，三档优先级（顺序即语义）：
	//   1. **证明了能下**的永远优先于「探测没结论」的，哪怕档位低一点
	//      （没结论 = 连 2KB 都没读到，拿它当主力是在赌网络）；
	//   2. 档位高的优先（这就是「轮询最高音质」的本体）；
	//   3. 同档位时**用户排在前面的**优先（数组顺序 = 轮询顺序，稳定可预期，
	//      不会每次换一条 —— 用户选了「全豆要」在前，就别偷偷换别的源）。
	better := func(a, b scored) bool {
		if a.unproven != b.unproven {
			return b.unproven
		}
		if a.rank != b.rank {
			return a.rank > b.rank
		}
		return a.pos < b.pos
	}
	for _, c := range cands {
		if strings.TrimSpace(c.URL) == "" || !strings.HasPrefix(c.URL, "http") {
			continue
		}
		actual := i.lxHonestTier(src, id, info, tier, c.URL, c.Bytes)
		pos, ok := order[c.Script]
		if !ok {
			pos = len(order) // 不在名单里（不该发生）排最后，但仍是候选
		}
		s := scored{cand: c, actual: actual, rank: online.TierRank(actual), pos: pos, unproven: c.Unproven != ""}
		if best == nil || better(s, *best) {
			sb := s
			best = &sb
		}
	}
	if best == nil {
		return nil, online.ErrNotPlayable
	}
	if len(cands) > 1 {
		i.logf("[INTERCEPT] 洛雪轮询选中 %s（%s，档位 %s，共 %d 个候选）", best.cand.Script, src+"/"+id, best.actual, len(cands))
	}
	return &online.Resolved{
		URL:    best.cand.URL,
		Format: lxFormatFromTier(best.actual),
		Tier:   best.actual,
	}, nil
}

// lxDurationOf 从喂回脚本的那条记录里取时长（秒）。
//
// 时长是「按字节数反推码率」的分母，拿不到就整个判据作废（宁可不判，也不要瞎降档）。
// 洛雪的记录里这个键各处不一：`duration`（qsvip 搜索）/`interval`（部分平台原始字段），
// 所以两个都读，谁先用谁。
func lxDurationOf(info map[string]any) int {
	if info == nil {
		return 0
	}
	for _, k := range []string{"duration", "interval"} {
		if d := asInt(info[k]); d > 0 {
			return d
		}
	}
	return 0
}

// lxTierFromSize 用**实测字节数 + 时长**反推真实档位，只降不升。
//
// 洛雪背后那些第三方站最常见的偏差不是报错，而是「你点无损，它给一条 128k 的 mp3，
// 然后按无损的名义回话」。光看 URL 后缀拦不住它（很多直链没有后缀，或者后缀与容器
// 无关），但「这首歌 295 秒只给了 4.1MB」骗不了人 —— 约 113kbps，就是 128k。
//
// 真机实测到的例子（2026-10-21，Beyond《不再犹豫》wy/2063748306，点 flac）：
// 宿主活性校验探到 content-range 总长 4,186,741 字节、时长 295 秒 → 113kbps，
// 而脚本回话的档位是 flac。不校正的话登记表演出来是「无损」、盘上是个 128k 的 mp3，
// 用户看到的还是「说好的无损呢」。
//
// 返回 (档位, 依据说明)。size/duration 缺任一 → 原样返回 claimed、说明为空：
// **没有证据就不能假装知道**，此时上层还有落盘扩展名那道校正（tierFromLandedFile）。
func lxTierFromSize(size int64, durationS int, claimed string) (string, string) {
	if size <= 0 || durationS <= 0 || claimed == "" {
		return claimed, ""
	}
	kbps := float64(size) * 8 / float64(durationS) / 1000
	// 分档用中点：flac 至少 500kbps（16/44 的成曲常在 700–1000，24bit 更高）；
	// 320k CBR 实测 315–330，留到 260 起算，免得把 VBR 的 320k 误降成 192k。
	observed := online.Tier128k
	switch {
	case kbps >= 500:
		observed = online.TierLossless
	case kbps >= 260:
		observed = online.Tier320k
	case kbps >= 160:
		observed = online.Tier192k
	}
	if online.TierRank(observed) >= online.TierRank(claimed) {
		return claimed, "" // 只降不升：字节数说不清无损内部的分层，别拿它吹高
	}
	return observed, fmt.Sprintf("%.1fMB/%ds ≈ %.0fk", float64(size)/1048576, durationS, kbps)
}

// lxURLLooksLossy 判断直链看起来是不是有损（按路径扩展名，不看 query）。
//
// 判不出来（没有扩展名 / 认不出）就返回 false —— 那是「不知道」，不是「有损」；
// 反正脚本自己也会拦「无损请求给有损流」。
func lxURLLooksLossy(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.HasSuffix(u, ".mp3") || strings.HasSuffix(u, ".m4a") ||
		strings.HasSuffix(u, ".aac") || strings.HasSuffix(u, ".wma")
}
