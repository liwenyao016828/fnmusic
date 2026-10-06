package intercept

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// onlineSearchBudget 是整个在线检索的总预算（所有平台共享，不是每个平台一份）。
//
// 官方前端对搜索有自己的超时；我们必须在它放弃之前交出结果。宁可少几首
// 在线歌，也不能让官方界面转圈。
//
// 是**变量**而不是常量：用例要把它调小来验「慢平台不拖垮整次搜索」——
// 用真预算（8 秒）跑那条用例要干等 8 秒，套件里不值得。
var onlineSearchBudget = 8 * time.Second

// 在线搜索的规模与缓存。
// onlineSearchWait 是「等各平台搜索返回」的上限。
//
// ⚠️ 与 onlineSearchBudget（8s，整次在线检索的总预算）是**两件事**：
// 这个是「等搜索」的上限，那个是「从开始到交结果」的上限。
//
// 为什么比总预算短得多：原生平台通常 <1 秒就回来了，外挂平台（musicdl）慢的
// 直接放弃这一次 —— 等来的那几首抵不上用户多等 5 秒。真机实测：千千会跑满
// sidecar 的单源预算，25 秒版让飞牛里每次搜索都卡住。
var onlineSearchWait = 3 * time.Second

// onlineResolveBudget 是「把候选曲目解析成直链」的预算。
//
// ⚠️ 它**必须用自己的 context**，不能用上面那个已经可能过期的 ——
// 真机上就栽在这里：等搜索等到超预算，接着拿那个**已取消**的 ctx 去解析，
// 于是 `ResolveMany` 全军覆没，表现为「合并超预算 → 一首在线歌都不出」。
const onlineResolveBudget = 5 * time.Second

const (
	// onlinePerPlatform 是每个平台取多少条候选。
	onlinePerPlatform = 30
	// onlineCacheTTL 是「某关键词的在线结果全集」的缓存时长。
	//
	// 为什么要缓存全集而不是每页各查一次：官方前端翻页时会用同一个关键词
	// 反复请求，而各平台的搜索接口本身是分页的，两次分页之间的结果未必稳定。
	// 缓存一份全集再按窗口切片，翻页时看到的就是同一个稳定列表。
	onlineCacheTTL = 5 * time.Minute
)

// onlineCacheEntry 缓存一个关键词的在线结果全集。
type onlineCacheEntry struct {
	items []online.Track
	ts    time.Time
}

// handleSearch 拦截官方搜索，把在线曲目合并进 `data.list`。
//
// 合并顺序是**官方在前、在线在后**（对齐 fnmusic-ext 与官方「本地优先」的
// 产品语义）。分页时把全局页码切成两段：本地段占 `[0, localTotal)`，
// 在线段紧随其后。
func (i *Interceptor) handleSearch(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	keyword := strings.TrimSpace(firstNonEmpty(q.Get("keyword"), q.Get("q"), q.Get("query")))
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 50)

	obj, raw, status, ct, ok := i.upstreamJSON(r, nil)
	if !ok {
		writeRaw(w, raw, status, ct)
		return true
	}
	data := asMap(obj["data"])
	if data == nil {
		writeRaw(w, raw, status, ct)
		return true
	}
	if keyword == "" {
		// 没有关键词就没有在线部分；官方可能仍在按分页返回「全部」。
		writeRaw(w, raw, status, ct)
		return true
	}

	localList := asSlice(data["list"])
	localTotal := asInt(data["total"])
	if localTotal < 0 {
		localTotal = 0
	}
	if localTotal < len(localList) {
		// 官方报的 total 比实际给的少（分页信息不一致时会发生）——以实际为准，
		// 否则本地段与在线段会在同一个下标上重叠。
		localTotal = len(localList)
	}

	start := (page - 1) * size
	if start < 0 {
		start = 0
	}
	end := start + size

	// 本地段：官方已经把本页切好了，但有两种情况要修正。
	localPage := localList
	switch {
	case start >= localTotal:
		// 本页完全落在在线区间里。官方对越界页会**钳回第 1 页**返回，
		// 那份数据不属于本页 —— 不清空就会在第 3 页重复显示第 1 页的本地歌。
		localPage = nil
	case len(localList) > size:
		// 官方忽略了 size、把全量都返回了：按本页窗口原地切片。
		lo := start
		if lo > len(localList) {
			lo = len(localList)
		}
		hi := start + size
		if hi > len(localList) {
			hi = len(localList)
		}
		localPage = localList[lo:hi]
	}

	// 在线段：只取落在本页窗口内、且在本地段之后的那部分。
	onlineAll := i.collectOnline(r.Context(), keyword)
	onlineAll = dropLocalDuplicates(onlineAll, localList)

	merged := make([]any, 0, len(localPage)+size)
	merged = append(merged, localPage...)

	onlineStart := start - localTotal
	if onlineStart < 0 {
		onlineStart = 0
	}
	onlineEnd := end - localTotal
	if onlineEnd > len(onlineAll) {
		onlineEnd = len(onlineAll)
	}
	if onlineEnd > onlineStart {
		favorites := i.store.FavoriteGUIDs(i.userKey(r))
		for _, t := range onlineAll[onlineStart:onlineEnd] {
			fake := i.registry.Put(t)
			merged = append(merged, online.SearchVO(t, online.Playback{}, favorites[fake]))
		}
	}

	data["list"] = merged
	// total 用**实际可播的在线条数**，不是「查到的条数」。
	//
	// fnmusic-ext 这里是不一致的：它的 total 统计在可播性过滤之前，所以关掉某个
	// 源之后 total 会比实际条数多，客户端翻到最后一页会看到空列表。用实际条数
	// 就没有这个问题。
	data["total"] = localTotal + len(onlineAll)
	obj["data"] = data

	writeJSON(w, http.StatusOK, obj)
	return true
}

// collectOnline 取某个关键词的在线结果全集（带缓存）。
//
// 返回的曲目**已经过滤成可播的**：查得到点不动比查不到更糟，所以解析不出
// 直链的曲目不进列表。
func (i *Interceptor) collectOnline(ctx context.Context, keyword string) []online.Track {
	key := strings.ToLower(strings.TrimSpace(keyword))

	i.onlineMu.Lock()
	if e, ok := i.onlineCache[key]; ok && time.Since(e.ts) < onlineCacheTTL {
		i.onlineMu.Unlock()
		return e.items
	}
	i.onlineMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, onlineSearchBudget)
	defer cancel()

	type result struct {
		platform string
		songs    []search.UnifiedSong
	}
	// 平台列表**现取**：外挂平台（咪咕/千千/B站）是用户开关决定的，开关一开
	// 就该出现在飞牛页面的搜索里，不该等重启。
	plats := i.platforms()
	results := make([]result, len(plats))
	var wg sync.WaitGroup
	for idx, platform := range plats {
		wg.Add(1)
		go func(idx int, platform string) {
			defer wg.Done()
			songs := i.searcher(keyword, platform, 1, onlinePerPlatform)
			results[idx] = result{platform: platform, songs: songs}
		}(idx, platform)
	}
	// ⚠️ 每个平台各自限时：**一个慢平台不该拖垮整次搜索**。
	//
	// `i.searcher` 是同步的、没有 ctx，所以这里用「等 or 超时」而不是取消 ——
	// 超时后那个 goroutine 仍会跑完（结果写进自己的槽位），只是这次不用它。
	// 平台间写的是**不同的下标**，所以没有数据竞争。
	//
	// 为什么必须有这道闸：musicdl 的平台（尤其千千）实测会跑满 sidecar 的单源预算
	// （25 秒），而官方页面的搜索是用户点一下在等的操作 —— 没有这道闸，打开外挂音源
	// 之后飞牛里每次搜索都要等二十多秒。
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(onlineSearchWait):
		i.logf("[INTERCEPT] 等平台搜索超过 %s，本次只用已经回来的平台（%s）",
			onlineSearchWait, strings.Join(plats, "/"))
	}

	var out []online.Track
	seen := make(map[string]bool, 64)
	for _, res := range results {
		if len(res.songs) == 0 {
			continue
		}
		candidates := make([]online.Track, 0, len(res.songs))
		ids := make([]string, 0, len(res.songs))
		for _, s := range res.songs {
			t, ok := trackFromSong(res.platform, s)
			if !ok {
				continue
			}
			if seen[t.RealID()] {
				continue
			}
			seen[t.RealID()] = true
			candidates = append(candidates, t)
			ids = append(ids, t.PlatformID)
		}
		if len(candidates) == 0 {
			continue
		}
		// 一次批量解析就能知道这一页里哪些真能播。
		//
		// ⚠️ 用**新**的 context：上面等搜索可能已经把 ctx 用过期了，拿一个已取消的
		// ctx 去解析 → 全部失败 → 一首在线歌都不出（真机现象）。
		rctx, rcancel := context.WithTimeout(context.Background(), onlineResolveBudget)
		playable := i.pool.ResolveMany(rctx, res.platform, ids)
		rcancel()
		for _, t := range candidates {
			if _, ok := playable[t.PlatformID]; ok {
				out = append(out, t)
			}
		}
	}

	i.onlineMu.Lock()
	// 简单的容量控制：关键词缓存不会太多（家庭场景同时搜的词有限），
	// 超过 128 个就整体清空。
	if len(i.onlineCache) > 128 {
		i.onlineCache = make(map[string]onlineCacheEntry, 32)
	}
	i.onlineCache[key] = onlineCacheEntry{items: out, ts: time.Now()}
	i.onlineMu.Unlock()

	if len(out) > 0 {
		i.logf("[INTERCEPT] 在线搜索 %q → %d 首可播（%s）", keyword, len(out), strings.Join(plats, "/"))
	}
	return out
}

// trackFromSong 把搜索层的统一歌曲转成在线曲目描述符。
func trackFromSong(platform string, s search.UnifiedSong) (online.Track, bool) {
	// 平台内 id 一律取 Songmid —— 曲率既有的搜索层已经把四家的原生 id 都归一到
	// 这个字段（网易 song id、QQ songmid、酷狗 file hash、酷我 rid）。
	id := strings.TrimSpace(s.Songmid)
	if id == "" {
		id = strings.TrimSpace(s.ID)
	}
	if id == "" {
		return online.Track{}, false
	}
	title := strings.TrimSpace(s.Name)
	if title == "" {
		return online.Track{}, false
	}

	artists := splitArtists(s.Singer)
	duration := s.Duration
	if duration <= 0 {
		duration = s.Interval
	}
	// 曲率搜索层的 duration 单位在不同平台不统一（网易是毫秒、QQ 是秒），
	// 统一压到秒。超过 1 小时的时长一定是毫秒没换算。
	if duration > 3600 {
		duration = duration / 1000
	}

	return online.Track{
		Platform:   platform,
		PlatformID: id,
		Title:      title,
		Artists:    artists,
		Album:      strings.TrimSpace(s.Album),
		Duration:   duration,
		CoverURL:   strings.TrimSpace(s.Cover),
		Year:       s.Year,
	}.Normalized(), true
}

// splitArtists 把「A/B」「A、B」「A;B」等拼接串拆成艺术家列表。
func splitArtists(singer string) []string {
	singer = strings.TrimSpace(singer)
	if singer == "" {
		return nil
	}
	fields := strings.FieldsFunc(singer, func(r rune) bool {
		switch r {
		case '/', '、', ';', '；', '&', '，', ',':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if s := strings.TrimSpace(f); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{singer}
	}
	return out
}

// dropLocalDuplicates 丢掉与官方本地结果重复的在线曲目。
//
// 判据是「标题 + 艺术家」的小写形式。官方本地已有的歌不该再出现一条在线版本 ——
// 用户点哪条都行，但看到两条一模一样的会以为出了问题。
//
// 只按标题+艺术家，不带专辑：同一首歌在不同专辑里重复出现是常态，
// 带上专辑会让去重失效。
func dropLocalDuplicates(items []online.Track, local []any) []online.Track {
	if len(local) == 0 || len(items) == 0 {
		return items
	}
	seen := make(map[string]bool, len(local))
	for _, raw := range local {
		m := asMap(raw)
		if m == nil {
			continue
		}
		title := firstNonEmpty(asString(m["title"]), asString(m["name"]))
		artist := asString(m["artist"])
		if artist == "" {
			if artists := asSlice(m["artists"]); len(artists) > 0 {
				names := make([]string, 0, len(artists))
				for _, a := range artists {
					if am := asMap(a); am != nil {
						if n := asString(am["name"]); n != "" {
							names = append(names, n)
						}
					}
				}
				artist = strings.Join(names, "/")
			}
		}
		if k := titleArtistKey(title, artist); k != "" {
			seen[k] = true
		}
	}

	out := make([]online.Track, 0, len(items))
	for _, t := range items {
		if seen[titleArtistKey(t.Title, t.Artist())] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// titleArtistKey 生成去重用的键。空标题返回空串（调用方应忽略）。
func titleArtistKey(title, artist string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}
	// 去掉空白与常见标点差异，避免「晴天 (Live)」与「晴天(Live)」被当成两首。
	replacer := strings.NewReplacer(" ", "", "\t", "", "（", "(", "）", ")", "【", "[", "】", "]")
	return replacer.Replace(title) + "\x00" + replacer.Replace(strings.ToLower(strings.TrimSpace(artist)))
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
