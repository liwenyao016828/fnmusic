package intercept

// 试听的「跨源兜底」：本平台解析不出直链时，去下载源池 / 洛雪里找同名同版录音。
//
// # 为什么要它（2026-10-24 用户拍板）
//
// 用户原话：「网易云不能播放的歌曲，用其他源代替。飞牛音乐上显示歌单还是保证
// 是真实，歌曲都在的状态。比如 100 首，就显示 100 首源歌单，网易有 36 能播，
// 还有 64 就用其他源去补充」。
//
// 于是榜单 / 在线歌单**不再**按可播性过滤（见 vchart.go 的 chartPlaylist），
// 列表按源榜单原样给满。代价是列表里会出现本平台拿不到直链的曲目 —— 这一步
// 就是那 64 首的出路。
//
// # 判据与下载完全共用
//
// 候选来自 `rankedSources`（acquire.go），匹配判据是 sameTitle + 时长对得上，
// 与「收藏自动下载」「注入页点下载」是同一份代码。这样不会出现「下载找得到、
// 试听找不到」这种两套语义 —— 那是最容易让用户觉得「随机抽风」的一类 bug。
//
// # 为什么带缓存
//
// 一次跨源搜索要并发打四五个源（musicdl 单条约 16s 才返回），放在**首次播放**
// 的路径上是实打实的等待。而同一首歌会被反复播（拖动进度条、切歌回来、多端）。
// 所以：
//   - 找得到的缓存 30 分钟（与榜单缓存同量级）；
//   - 找不到的也缓存 5 分钟 —— 一首全平台都没有的歌，不该每次播放都白搜一遍；
//   - 搜索超时**不**写缓存（那只是这次没等到，不代表没有）。

import (
	"context"
	"time"

	"fn-lx-player/pkg/online"
)

const (
	// fbCacheTTL 是「找得到替代源」的缓存时长。
	fbCacheTTL = 30 * time.Minute
	// fbNegTTL 是「找不到替代源」的缓存时长。
	fbNegTTL = 5 * time.Minute
	// fbCacheMax 是缓存条数上限（一首一条，1024 条足够覆盖几天的试听）。
	fbCacheMax = 1024
	// fbCandidates 是一次兜底最多记住几个候选源。
	//
	// 3 个：候选按优先级排过序，前三个都不行基本就是「这首歌哪都没有」。
	// 再多只是把首次播放的等待拉长，命中率提升很有限。
	fbCandidates = 3
	// fbBudget 是一次兜底搜索的时间上限。
	//
	// 取流那条路整体是 20s（见 stream.go），这里留 12s 给它 —— 超了就放弃，
	// 让上层按「播不了」处理，而不是把一次播放拖到二十几秒。
	fbBudget = 12 * time.Second
)

// fbEntry 是一条兜底缓存。
type fbEntry struct {
	tracks []online.Track
	expire time.Time
}

// resolvePlaybackFallback 在兜底候选里挑**能播且音质最好**的那条。
//
// # 顺序：音质优先，不是「谁排前面听谁的」
//
// 候选来自 rankedSources（acquire.go），已经按优先级排过序 —— 但那个顺序是
// 「哪个源更可能找到同一首歌」，不是「哪个源音质更好」。早期实现是「按顺序
// 试，**第一条解析成功就返回**」，于是：排在前面那个源只有 128k，后面那个源
// 有无损，也永远听不到无损。
//
// 上游（proxy/app.py:4171 起的 _LX_URL_INFLIGHT 与 app.py 的兜底顺序）是
// 「质量 → 脚本 → 平台」：先在所有候选里找最高档，找不到再降一档。所以这里
// 改成：逐个候选解析，**记下目前最好的那档**，只有当某个候选给出的档位已经
// 到达阶梯顶端时才提前收手（避免每次都白问后面几个源，多等十几秒）。
//
// 「能播」永远优先于「音质高」：候选全都失败就是失败，不会因为追求无损
// 而让一首本来能听的歌变成「播不了」。
func (i *Interceptor) resolvePlaybackFallback(ctx context.Context, t online.Track) (online.Track, *online.Resolved, bool) {
	cands := i.playbackFallbacks(ctx, t)
	if len(cands) == 0 {
		return online.Track{}, nil, false
	}
	tiers := i.playbackTiers()
	topTier := ""
	if len(tiers) > 0 {
		topTier = tiers[0]
	}

	var bestTrack online.Track
	var bestRes *online.Resolved
	bestRank := -1
	haveBest := false
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		res, err := i.pool.ResolveLadder(ctx, c.Platform, c.PlatformID, tiers)
		if err != nil || res == nil {
			continue
		}
		tier := playbackTierOf(res, c)
		rank := online.TierRank(tier)
		// 档位认不出来（rank = -1）也算数 —— 能播就比播不了强，
		// 所以用 haveBest 单独记「有没有解出来」，不拿 rank 当存在性。
		if !haveBest || rank > bestRank {
			bestTrack, bestRes, bestRank, haveBest = c, res, rank, true
		}
		// 已经拿到阶梯顶端的档位 → 后面几个候选不可能更好，收手。
		if topTier != "" && rank >= online.TierRank(topTier) {
			break
		}
	}
	if bestRes == nil {
		return online.Track{}, nil, false
	}
	i.logf("[INTERCEPT] 试听跨源兜底：%s/%s 取不到 → %s/%s（%s）",
		t.Platform, t.PlatformID, bestTrack.Platform, bestTrack.PlatformID, playbackTierOf(bestRes, bestTrack))
	return bestTrack, bestRes, true
}

// playbackFallbacks 返回这首歌的兜底候选（已按优先级排序、可能有缓存）。
//
// 返回空 = 这首歌在池里找不到同版录音（或池压根没配）。
func (i *Interceptor) playbackFallbacks(ctx context.Context, t online.Track) []online.Track {
	key := t.RealID()
	if key == "" {
		return nil
	}
	if list, ok := i.fbLookup(key); ok {
		return list
	}

	// 池是空的（没开外挂音源、也没接洛雪）→ 一次搜索都不发。
	// 这条必须先判：rankedSources 在两边都空时会直接返回 nil，
	// 但**只有**在 keyword 非空时才这么判 —— 这里自己再看一眼更省事。
	if len(i.downloadSources()) == 0 && i.lxSearch == nil {
		return nil
	}
	if t.Title == "" {
		return nil
	}

	sctx, cancel := context.WithTimeout(ctx, fbBudget)
	defer cancel()

	// rankedSources 内部的搜索不接 ctx（musicdl 单条约 16s 不受控），
	// 所以把它扔进 goroutine，超时就先走 —— 不能让一次兜底把播放拖死。
	done := make(chan []online.Track, 1)
	go func() {
		done <- i.rankedSources(sctx, t.Title, t.Artist(), t.Duration)
	}()

	var ranked []online.Track
	select {
	case ranked = <-done:
	case <-sctx.Done():
		// 没等到：不写缓存（这次没搜出结论，不代表没有）。
		return nil
	}

	out := make([]online.Track, 0, fbCandidates)
	for _, c := range ranked {
		// 同一个平台同一条 id 就是原曲目本身，试它没有意义。
		if c.Platform == t.Platform && c.PlatformID == t.PlatformID {
			continue
		}
		out = append(out, c)
		if len(out) >= fbCandidates {
			break
		}
	}
	i.fbStore(key, out)
	return out
}

// fbLookup 读缓存。第二个返回值 = 命中（空列表也算命中 = 「确认找不到」）。
func (i *Interceptor) fbLookup(key string) ([]online.Track, bool) {
	i.fbMu.Lock()
	defer i.fbMu.Unlock()
	e, ok := i.fbCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expire) {
		delete(i.fbCache, key)
		return nil, false
	}
	return e.tracks, true
}

// fbStore 写缓存。空结果用更短的寿命。
func (i *Interceptor) fbStore(key string, tracks []online.Track) {
	ttl := fbCacheTTL
	if len(tracks) == 0 {
		ttl = fbNegTTL
	}
	i.fbMu.Lock()
	defer i.fbMu.Unlock()
	if i.fbCache == nil {
		i.fbCache = make(map[string]fbEntry, 16)
	}
	if len(i.fbCache) >= fbCacheMax {
		now := time.Now()
		for k, e := range i.fbCache {
			if now.After(e.expire) {
				delete(i.fbCache, k)
			}
		}
		// 清完还是满的（全都没过期）→ 整体丢掉重建。这张表只是加速，
		// 丢掉的代价是下一次播放多搜一轮，比在遍历里挑最旧一条便宜。
		if len(i.fbCache) >= fbCacheMax {
			i.fbCache = make(map[string]fbEntry, 16)
		}
	}
	i.fbCache[key] = fbEntry{tracks: tracks, expire: time.Now().Add(ttl)}
}
