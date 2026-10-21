package intercept

import (
	"context"
	"strings"

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
	if r.i == nil || r.i.lxResolve == nil {
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
	url, err := r.i.lxResolve(ctx, r.src, info, tier)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(url) == "" {
		return nil, online.ErrNotPlayable
	}
	actual := tier
	// 诚实：脚本可能把无损请求压成有损（mapQuality clamp），而直链会露馅。
	// 声明「无损」却落一个 mp3 是最难查的一类偏差 —— 宁可当场降档。
	if online.TierRank(actual) >= online.TierRank(online.TierLossless) && lxURLLooksLossy(url) {
		actual = online.Tier320k
	}
	return &online.Resolved{
		URL:    url,
		Format: lxFormatFromTier(actual),
		// 档位是「我们点的那一档」或脚本声明的档位，不是随手猜的。
		Tier: actual,
	}, nil
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
