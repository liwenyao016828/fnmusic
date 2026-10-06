package intercept

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 在线专辑：官方「专辑搜索 / 专辑详情 / 专辑曲目列表」三条**只读**路由的认领。
//
// # 为什么值得认领
//
// 接管层已经把在线曲目合并进官方的搜索 / 歌单 / 收藏 / 历史，但**专辑**这条路
// 一直是空的：官方 App 里点「专辑」搜不到在线专辑，点进去也只有官方自己的库。
// 原因是这三条端点此前**没被认领** —— 请求原样透传给官方后端，而官方对它库里
// 没有的 id 只会回「找不到该专辑」（参考实现 app.py:7501 记的 `100002 invalid
// arguments` 是同一类失败）。
//
// # 复用了哪套机器（不另起一套）
//
//   - **虚拟 id**：专辑 id 不是新概念 —— 它就是**既有约定**「曲目虚拟 id + `:album`」
//     （vo.go 的每个 VO 都在下发 `album.guid = <曲目 fake>:album`，lookup.go 的
//     expandGUID 早就认得这个后缀）。所以「反解一张专辑」= 剥后缀 + 查登记表，
//     一行新映射都不用造。
//   - **登记表**：专辑的锚点是一条**真实在线曲目**（不是凭空造的 album id），
//     `registry.Put` 之后 `<fake>:album` 才反解得到；重启后由 store.AllTracks()
//     的 Warm 重建（见 online/registry.go）。
//   - **曲目来源**：collectOnline —— 多源并发、单源失败只记一笔、5 分钟关键词
//     缓存、结果**已过滤成可播**。专辑页的曲目就是拿「专辑名」当关键词去问它。
//   - **可播过滤**：playableOnly（与搜索 / 榜单 / 虚拟歌单同一条口径：
//     查得到点不动比查不到更糟）。
//   - **分页**：与 writeVirtualTrackList（虚拟歌单曲目列表）同一套 page/size 约定，
//     含官方「`size == -1` 表示全量」。
//   - **本地快照**：store.Favorites / store.History（同账号隔离、零网络）。
//   - **封面**：vPickCover（借第一首带封面曲目的虚拟 id，`/static/cover` 认它）。
//
// # 与参考实现（zouclang/fnos_music_ext 2.8.0）的差别（**刻意的**，见报告）
//
//  1. **没有「网易真实专辑接口」这一路**。参考实现的 `_netease_album_detail_payload`
//     （app.py:8288）走 musicbox `/api/v1/album/{id}`，而它能这么走是因为它在
//     `/search/album` 时就拿到了**真实专辑 id**（`_netease_album_search_rows`，
//     app.py:8606）。曲率这边：搜索层的 `UnifiedSong` 里没有网易专辑 id
//     （只有 QQ 的 `AlbumMID`），musicbox 也不是本项目的组件 —— 照抄就等于往
//     用户会点的路径上接一个**形状未验证**的上游接口。所以只做参考实现的**另一条**
//     路：`_aggregate_album_detail_payload`（app.py:8389）的「按专辑名聚合」口径。
//  2. **专辑锚点必须是真实曲目**。参考实现会造纯专辑锚点
//     （`online:netease:album:<album_id>`，app.py:8640），好处是 trackCount 能用
//     真实专辑的总曲目数。那需要一套**独立于曲目**的专辑登记表（它的
//     `_FAKE_ALBUM_REGISTRY`）；本实现按「复用既有登记表」的要求，锚点只能是登记表
//     里的曲目 —— 于是专辑卡片一定带得动至少一首可播曲目。
//  3. **专辑锚点不落盘**。参考实现把 `/search/album` 的锚点写进
//     `album_registry.json`（256 条 / 14 天 TTL，app.py:2764-2800），因为那些锚点
//     不在任何持久快照里、重启后 warm 重建不到。这边**不新建持久化机制**：重启后
//     「更多会话里拿到的」专辑 guid 会反解不出 → 原样透传给官方（= 官方说找不到，
//     与今天的行为一致）。这是**已知缺口**，写进报告。
//  4. **artists[].guid 用曲目锚点的虚拟 id**（`<曲目 fake>:artist`，本项目 vo.go
//     的既有约定），参考实现用的是专辑 guid + `:artist`。原因：本项目的 `:artist`
//     反解靠 expandGUID 剥**一层**后缀，`<fake>:album:artist` 剥完是
//     `<fake>:album`，在登记表里查不到 —— 那样艺术家的封面请求只会拿到占位图。
//  5. **曲目条目是「超集」**：参考实现用它的 `build_online_track`（搜索 VO）。这里
//     以 FavoriteVO（本项目**已在真机验证**的列表条目形状，虚拟歌单曲目列表用的
//     就是它）为底版，再把 SearchVO 的别名并上去，见 albumTrackVO。
//
// # 形状纪律（本文件最重要的一段）
//
// 官方这三条端点的真实请求 / 响应形状，本机拿不到（官方页面只在 NAS 上可达）。
// 所以：
//
//   - `/search/album`：只有「HTTP 成功 + code == 0 + data 是对象 + data.list 是
//     对象数组」才合并在线卡片；**其余一律逐字节原样透传**（= 今天的行为），
//     并打**一条**一次性诊断日志（只打类型与键名，不打值）。
//   - `/album`、`/track/album-detail/list`：只有「id 能在登记表里反解出锚点曲目」
//     才自己应答；**其余一律逐字节原样透传**。诊断只在两种可行动的场合打一条：
//     拿到的是**本层下发的专辑 guid 形态**（`<32hex>:album`）却查不到锚点（= 登记表
//     没覆盖到），以及**一个 id 候选都没找到**（= 参数名对不上）。
//
// 最坏情况因此是「在线专辑消失」，而不是「官方专辑页被我们搞坏」。

// albumIDParamNames 是「专辑 guid」可能挂在哪些参数名上（专辑专用名在前）。
//
// 参考实现读 albumGUID / albumGuid / guid / id / albumId（app.py:8497）；
// 这里再补几个别名（coverId 也是专辑卡片上的一个 id，客户端拿它当专辑 id
// 回传是可能的）。多列一个名不花钱，少列一个会让整条路失效。
var albumIDParamNames = []string{
	"albumGUID", "albumGuid", "albumguid", "albumId", "album_id", "album",
	"guid", "coverId", "cover_id", "id",
}

const (
	// albumSearchMax 是一次专辑搜索最多注入几张在线专辑卡片。
	//
	// 与参考实现同为 10（app.py:8565 的 `_ALBUM_SEARCH_LIMIT`）—— 专辑 Tab 一屏
	// 也就这么多，多注入只会把官方专辑挤下去。
	albumSearchMax = 10

	// albumCandidateMax 是合成一张专辑时最多取多少首**候选**（可播过滤之前的上限）。
	//
	// 与 albumTrackMax 分开：上游一次给多少不由我们决定，候选里只有一部分能解析出
	// 直链，所以候选上限必须比曲目上限大。
	albumCandidateMax = 120

	// albumTrackMax 是一张专辑最多下发多少首曲目。
	//
	// 参考实现不截断（真实专辑接口一次给全）。这里必须截：曲目来自关键词搜索，
	// 候选量只受平台返回条数限制，不设上限就是一个能被搜索词放大的量。
	albumTrackMax = 100

	// albumCacheTTL 是「一张专辑的合成结果留多久」。
	//
	// 客户端进专辑页会先调专辑详情、紧接着调曲目列表（参考实现为此专门加了一层
	// 120 秒缓存，app.py:8446-8460），两次请求必须看到同一份曲目；120 秒也正好
	// 覆盖「点进去 → 退出来 → 再点进去」。
	albumCacheTTL = 2 * time.Minute

	// albumCacheMax 是合成缓存的条数上限（超了整体清空，与 vCache 同一套土办法）。
	albumCacheMax = 64
)

// albumSearchBudget 是「等在线那一路」的上限，**并发**于官方转发。
//
// 它是变量不是常量：用例要把它调小（同 onlineSearchBudget / suggestOnlineBudget）。
//
// 为什么是 9 秒：在线那一路复用 collectOnline，而它的最坏情况是「等平台 3 秒
// （onlineSearchWait）+ 解析 5 秒（onlineResolveBudget）」= 8 秒。预算必须盖住它，
// 否则最慢的那一路永远用不上结果。它是**上限**而不是固定等待 —— 在线那一路先回来
// 就先用。参考实现在这里是 10 秒（app.py:8748）。
var albumSearchBudget = 9 * time.Second

// albumResolveBudget 是专辑曲目可播性解析的预算。
//
// 与 onlineResolveBudget（搜索那一路，5 秒）分开：这里是用户**已经点进专辑页**之后的
// 一次批量解析，候选可能上百首，多给一秒换更高命中率值得。
//
// 用**自己的 context**（不是请求的）：搜索那一路踩过「拿一个已过期的 ctx 去解析 →
// 全军覆没 → 一首在线歌都不出」的坑（见 search.go/collectOnline）。
var albumResolveBudget = 6 * time.Second

// albumEntry 缓存一张合成好的专辑。
//
// anchorFake 是锚点曲目的虚拟 id：专辑详情的 `artists[].guid` 要用它（见文件头第 4 条）。
type albumEntry struct {
	pl         vPlaylist
	anchorFake string
	ts         time.Time
}

// ── 路由 ──────────────────────────────────────────────────────────────

// handleAlbumSearch 接管 `GET /music/api/v1/search/album`（含子路径）。
//
// 与 handleSearch 同一个形状：官方结果**在前**，在线专辑补在后；官方错误、非 JSON、
// 形状不认识、没有可加的内容 —— 四种情况全部**逐字节原样透传**。
func (i *Interceptor) handleAlbumSearch(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	q := r.URL.Query()
	keyword := strings.TrimSpace(firstNonEmpty(q.Get("keyword"), q.Get("q"), q.Get("query"), q.Get("wd")))

	// 在线那一路与官方转发**并发**跑：串行的话在线那一路再快也要等官方往返
	// （与 suggest.go 同一个理由）。没有关键词时一个请求都不发。
	var ch chan []any
	if keyword != "" {
		ch = make(chan []any, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), albumSearchBudget)
			defer cancel()
			// 缓冲 1：预算用完时调用方会丢弃这个结果，但写入不会永久阻塞
			// （不漏 goroutine）。
			ch <- i.onlineAlbumCards(ctx, keyword)
		}()
	}

	obj, raw, status, ct, ok := i.upstreamJSON(r, nil)
	if !ok {
		// 上游非 JSON / 业务码非 0 / 转发失败 —— 原样透传。
		// 这正是官方自己说的「未登录」「参数不对」，改一个字都是错的。
		writeRaw(w, raw, status, ct)
		return true
	}
	data := asMap(obj["data"])
	if data == nil {
		i.noteAlbumSearchShape(nil, nil)
		writeRaw(w, raw, status, ct)
		return true
	}
	official, isObjectList := asObjectList(data["list"])
	if !isObjectList {
		// 形状与预期不符（参考实现合并的是 `data.list`，见 app.py:8755-8770）。
		// **不猜**：记一条诊断（只打类型与键名），原样透传。
		i.noteAlbumSearchShape(data["list"], data)
		writeRaw(w, raw, status, ct)
		return true
	}

	add := pickAlbumCards(i.waitAlbumSearch(ch), albumNameSet(official))
	if len(add) == 0 {
		// 没有可加的专辑 → 连序列化都不做，**逐字节原样**返回官方那份。
		// 官方专辑搜索是用户已经在用的东西，能不动就不动。
		writeRaw(w, raw, status, ct)
		return true
	}

	merged := make([]any, 0, len(official)+len(add))
	merged = append(merged, official...)
	merged = append(merged, add...)
	data["list"] = merged
	bumpAlbumTotal(data, len(add), len(merged))
	obj["data"] = data
	writeJSON(w, http.StatusOK, obj)
	return true
}

// handleAlbumDetail 接管 `GET /music/api/v1/album`（含 `/album/detail` 这类子路径）。
//
// 认领条件只有一个：id 能在登记表里反解出锚点曲目。**官方专辑（本地库里的、
// 官方能提供的一切）反解不出来 → 原样透传**，所以这条路不会改变官方专辑的行为。
func (i *Interceptor) handleAlbumDetail(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	prefix := apiPrefix + "/album"
	sub := subPathAfter(r.URL.Path, prefix)
	cands := albumRequestIDs(r, sub)

	pl, anchorFake, ok := i.albumFor(cands, r)
	if !ok {
		i.noteAlbumUnknownRequest(r, cands)
		i.passThrough(w, r)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok",
		"data": albumDetailVO(pl, anchorFake, i.store.FavoriteGUIDs(i.userKey(r))),
	})
	return true
}

// handleAlbumTrackList 接管 `GET /music/api/v1/track/album-detail/list`。
//
// 参考实现对这个端点的说明（app.py:8516）：官方 2.5 版客户端在专辑详情之后再调它，
// 信封与 `/track/playlist-detail/list` 同款 `{list, total, sort}`。所以这里的分页
// 口径与虚拟歌单曲目列表完全一致。
func (i *Interceptor) handleAlbumTrackList(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	sub := subPathAfter(r.URL.Path, apiPrefix+"/track/album-detail/list")
	cands := albumRequestIDs(r, sub)

	pl, _, ok := i.albumFor(cands, r)
	if !ok {
		i.noteAlbumUnknownRequest(r, cands)
		i.passThrough(w, r)
		return true
	}

	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiSigned(q.Get("size"), 50)
	tracks := albumWindow(pl.Tracks, page, size)

	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	list := make([]any, 0, len(tracks))
	for _, t := range tracks {
		list = append(list, albumTrackVO(t, pl.CreatedAt, favorites[t.FakeID()]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok",
		"data": map[string]any{
			"list":  list,
			"total": len(pl.Tracks),
			"sort":  strings.TrimSpace(q.Get("sort")),
		},
	})
	return true
}

// albumRejectMethod 挡下「不是只读方法的请求」，返回 true 表示已经应答（透传）。
//
// 三条专辑端点都只读。官方客户端会不会用别的方法发（历史上有端点用 POST 传 body），
// 我们不知道 —— 不认就原样透传（今天的行为），同时在**看到**的时候留一条真机线索
// （这是这份形状证据最缺的那种信息）。
func (i *Interceptor) albumRejectMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	i.albumNoteOnce("method:"+r.Method, func() string {
		return "[INTERCEPT] 专辑：收到非只读方法 " + r.Method + " " + albumPathShape(r.URL.Path) +
			"（query 键 " + albumQueryKeys(r) + "），本层只认 GET/HEAD —— 原样透传。" +
			"若真机上这条日志出现过，说明客户端用了别的方法，下一轮按它适配。"
	})
	i.passThrough(w, r)
	return true
}

// ── 在线专辑搜索 ──────────────────────────────────────────────────────

// onlineAlbumCards 取某个关键词的在线专辑卡片。
//
// 曲目候选**直接复用 collectOnline**（而不是给专辑另写一套多源检索）：
// 多源并发、单源失败只记一笔、5 分钟关键词缓存、结果已过滤成可播 —— 这套口径
// 在搜索/联想/推荐上已经验证过，另写一份只会多一处会漂的地方。
func (i *Interceptor) onlineAlbumCards(ctx context.Context, keyword string) []any {
	groups := albumGroups(i.collectOnline(ctx, keyword), i.platforms())
	if len(groups) == 0 {
		return nil
	}
	ts := time.Now().Unix()
	out := make([]any, 0, albumSearchMax)
	for _, g := range groups {
		if len(out) >= albumSearchMax {
			break
		}
		// 登记锚点：卡片上的 `<fake>:album` 之后才反解得出来（这就是「复用登记表」
		// 的全部代价 —— 一条 Put）。
		fake := i.registry.Put(g.anchor)
		out = append(out, albumCardVO(g.name, g.artist, fake, g.count, ts))
	}
	return out
}

// waitAlbumSearch 等在线那一路，超时返回 nil（不是错误：官方结果照常返回）。
func (i *Interceptor) waitAlbumSearch(ch chan []any) []any {
	if ch == nil {
		return nil
	}
	timer := time.NewTimer(albumSearchBudget)
	defer timer.Stop()
	select {
	case cards := <-ch:
		return cards
	case <-timer.C:
		i.logf("[INTERCEPT] 在线专辑搜索超过 %s 没回来，这次只回官方结果", albumSearchBudget)
		return nil
	}
}

// pickAlbumCards 从在线卡片里挑出**可以注入**的那些。
//
//  1. 与官方已有专辑同名的不注入（忽略大小写与空白差异）—— 用户看到两张同名卡片
//     会以为是重复项，而官方的那个才对得上他的库；
//  2. 顺序保持在线那一路算出来的顺序（按曲目数降序，见 albumGroups）。
func pickAlbumCards(cards []any, official map[string]bool) []any {
	if len(cards) == 0 {
		return nil
	}
	out := make([]any, 0, len(cards))
	for _, c := range cards {
		m := asMap(c)
		if m == nil {
			continue
		}
		if official[albumKey(asString(m["name"]))] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// albumNameSet 收集官方结果里的专辑名（归一化后），用于「同名不重复注入」。
// 官方条目的专辑名可能在 `name` 也可能在 `albumName`（与其它端点的一致习惯）。
func albumNameSet(list []any) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, raw := range list {
		m := asMap(raw)
		if m == nil {
			continue
		}
		if k := albumKey(firstNonEmpty(asString(m["name"]), asString(m["albumName"]))); k != "" {
			out[k] = true
		}
	}
	return out
}

// albumGroup 是一组「同一个平台的同一张专辑」。
type albumGroup struct {
	platform string
	name     string
	artist   string
	anchor   online.Track
	count    int
}

// albumGroups 把在线曲目按「平台 + 专辑名」聚合，并按「曲目数降序 → 平台顺序 → 专辑名」
// 排序。
//
// 为什么排序要定死：官方专辑 Tab 里卡片的位置不该每次刷新都换一换。曲目数降序与
// 参考实现一致（app.py:8703 的 `sorted(..., key=lambda kv: -len(kv[1]))`）。
//
// 专辑名缺失的曲目**不参与聚合**：Normalized 会把空专辑名兜底成「未知专辑」，
// 把占位名当成一张专辑去聚合就是一堆互不相干的东西挤在一起。
func albumGroups(tracks []online.Track, platformOrder []string) []albumGroup {
	rank := make(map[string]int, len(platformOrder)+2)
	for n, p := range platformOrder {
		if _, ok := rank[p]; !ok {
			rank[p] = n
		}
	}
	unknown := albumKey(online.UnknownAlbum)

	idx := make(map[string]int, 16)
	groups := make([]albumGroup, 0, 16)
	for _, t := range tracks {
		t = t.Normalized()
		if t.Platform == "" || t.PlatformID == "" {
			continue
		}
		key := albumKey(t.Album)
		if key == "" || key == unknown {
			continue
		}
		if _, ok := rank[t.Platform]; !ok {
			// 平台列表里没有它（外挂平台刚启用、配置刚改）→ 排在已知平台之后。
			rank[t.Platform] = len(platformOrder)
		}
		gid := t.Platform + "\x1f" + key
		if n, ok := idx[gid]; ok {
			groups[n].count++
			continue
		}
		idx[gid] = len(groups)
		groups = append(groups, albumGroup{
			platform: t.Platform,
			name:     strings.TrimSpace(t.Album),
			artist:   t.Artist(),
			anchor:   t,
			count:    1,
		})
	}

	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].count != groups[b].count {
			return groups[a].count > groups[b].count
		}
		if rank[groups[a].platform] != rank[groups[b].platform] {
			return rank[groups[a].platform] < rank[groups[b].platform]
		}
		return albumKey(groups[a].name) < albumKey(groups[b].name)
	})
	return groups
}

// albumCardVO 构造专辑搜索 / 列表里的一张在线专辑卡片。
//
// 字段集照参考实现的 `_album_list_obj`（app.py:8569）—— 那个形状是在真机上被
// 官方前端渲染过的（二手中最硬的一份）。`coverId` 取锚点曲目的虚拟 id：封面端点
// 对曲目的虚拟 id 本来就能出图（cover.go），不必为专辑再造一条封面链路。
func albumCardVO(name, artist, anchorFake string, trackCount int, ts int64) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		name = online.UnknownAlbum
	}
	artist = strings.TrimSpace(artist)
	if artist == "" {
		artist = online.UnknownArtist
	}
	return map[string]any{
		"guid": anchorFake + ":" + online.SubKindAlbum,
		"name": name,
		"artists": []any{map[string]any{
			"guid":      anchorFake + ":" + online.SubKindArtist,
			"name":      artist,
			"coverId":   nil,
			"createdAt": ts,
			"updatedAt": ts,
		}},
		"coverId":     anchorFake,
		"releaseDate": nil, // ⚠️ 官方 album 对象这里是 null，不是 0 也不是 ""
		"barcode":     nil,
		"createdAt":   ts,
		"updatedAt":   ts,
		"trackCount":  trackCount,
	}
}

// bumpAlbumTotal 把注入的卡片数加进 `total`。
//
// 规则与参考实现一致（app.py:8764-8768）：**只有整数**才相加；字段缺失时补成
// **合并后**的条数；其余形态（字符串 / 对象 / null）**一个字都不动** —— 那是官方
// 换了表达方式，猜错了比不加更糟。
//
// ⚠️ 缺失分支用的是 `mergedLen`（已经包含在线卡片），不是「官方条数 + 在线数」——
// 写反了会把 total 多算一倍。
func bumpAlbumTotal(data map[string]any, added, mergedLen int) {
	raw, present := data["total"]
	if !present {
		data["total"] = mergedLen
		return
	}
	if n, ok := jsonInt(raw); ok {
		data["total"] = n + added
	}
}

// jsonInt 试着把 JSON 数值取成 int；不是整数值时返回 false。
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// ── 在线专辑详情 ──────────────────────────────────────────────────────

// albumFor 取一张在线专辑的合成结果（带缓存）。
//
// 第二个返回值是锚点曲目的虚拟 id（详情的 artists[].guid 要用），第三个表示
// 「这个 id 是我们的虚拟专辑吗」—— **非虚拟 id 立刻返回 false，不做任何网络动作**，
// 调用方据此原样透传。这是整个拦截层最重要的不变式：认不出来就交给官方，绝不猜。
func (i *Interceptor) albumFor(candidates []string, r *http.Request) (vPlaylist, string, bool) {
	anchor, anchorFake, ok := i.albumAnchor(candidates)
	if !ok {
		return vPlaylist{}, "", false
	}
	guid := anchorFake + ":" + online.SubKindAlbum

	i.albumMu.Lock()
	if e, ok := i.albumCache[guid]; ok {
		// 空结果（整张专辑都不可播）只留 vChartNegTTL：一次上游抖动不该让专辑页
		// 空两分钟，但也不能变成「每次请求都重新搜一遍」。
		ttl := albumCacheTTL
		if len(e.pl.Tracks) == 0 {
			ttl = vChartNegTTL
		}
		if time.Since(e.ts) < ttl {
			i.albumMu.Unlock()
			return e.pl, e.anchorFake, true
		}
	}
	i.albumMu.Unlock()

	now := time.Now()
	tracks := i.albumTracks(r.Context(), anchor, i.userKey(r))
	for _, t := range tracks {
		// 曲目登记进虚拟 id 表：详情 / 曲目列表下发的 guid 之后要能取流、取歌词、
		// 取封面（否则就是「查得到点不动」的死条目）。
		i.registry.Put(t)
	}

	ts := now.Unix()
	pl := vPlaylist{
		GUID:      guid,
		Name:      anchor.AlbumName(),
		CreatedAt: ts,
		UpdatedAt: ts,
		Tracks:    tracks,
	}
	pl.CoverID = i.vPickCover(tracks, guid)

	i.albumMu.Lock()
	if len(i.albumCache) >= albumCacheMax {
		i.albumCache = make(map[string]albumEntry, 8)
	}
	i.albumCache[guid] = albumEntry{pl: pl, anchorFake: anchorFake, ts: now}
	i.albumMu.Unlock()

	i.logf("[INTERCEPT] 在线专辑 %s（%s）→ %d 首可播", pl.Name, pl.GUID, len(pl.Tracks))
	return pl, anchorFake, true
}

// albumAnchor 把客户端回传的专辑 id 反解成锚点曲目。
//
// 认得的形态（都只可能是**本层下发过**的虚拟 id —— 官方 id 不在登记表里，
// 所以「查得到」本身就是「这是我们的」的证据）：
//
//	<32hex>          vo.go 里 `coverId` 的形态
//	<32hex>:album    vo.go 里 `album.guid` 的形态（客户端进专辑页用的就是它）
//	album_<32hex>    官方**本地** coverId 的前缀形态（docs/飞牛音乐API.md 的
//	                 `/track/playlist-detail/list` 示例里是 `album_<hex>`），
//	                 客户端有可能带着前缀回传
//	track_<32hex>    参考实现的客户端伪装前缀（fnmusic-ext 的 disguise_client_json）
//
// 反解一律走**既有登记表**（前缀/后缀只是形态归一，不新建任何映射）。
func (i *Interceptor) albumAnchor(candidates []string) (online.Track, string, bool) {
	for _, c := range candidates {
		for _, form := range albumIDForms(c) {
			if t, ok := i.registry.Lookup(form); ok {
				return t, form, true
			}
		}
	}
	return online.Track{}, "", false
}

// albumIDForms 列出一个候选 id 的等价形态。
//
// 与 lookup.go 的 expandGUID 是同一套后缀（`:album`），这里另写一遍是因为要处理
// 「前缀 + 后缀」的组合（`track_<hex>:album`），而 expandGUID 只剥后缀 —— 它服务
// 的是「任何端点的 guid 候选」，不该为专辑多认一种前缀。
func albumIDForms(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := []string{s}
	base := s
	for _, p := range []string{"album_", "track_", "playlist_"} {
		if b, found := strings.CutPrefix(base, p); found && b != "" {
			base = b
			break
		}
	}
	if base != s {
		out = append(out, base)
	}
	if b, found := strings.CutSuffix(base, ":"+online.SubKindAlbum); found && b != "" {
		out = append(out, b)
	}
	return out
}

// albumRequestIDs 收集一个专辑请求里所有可能是「专辑 guid」的取值。
//
// 专辑专用参数名在前（参考实现读的是 albumGUID / albumGuid / guid / id / albumId），
// 然后是通用的 guid 候选与路径尾段。
func albumRequestIDs(r *http.Request, sub string) []string {
	q := r.URL.Query()
	out := make([]string, 0, len(albumIDParamNames)+len(guidParamNames)+1)
	for _, k := range albumIDParamNames {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			out = append(out, v)
		}
	}
	out = append(out, guidCandidates(r, sub)...)
	return out
}

// albumTracks 合成一张在线专辑的曲目。
//
// 顺序：**锚点在前**（用户就是点它进来的），然后本地快照（收藏 / 历史），
// 最后用「专辑名」搜索补足。全部候选都要过 `playableOnly` —— 与搜索/榜单/虚拟歌单
// 同一条口径：**查得到点不动比查不到更糟**。
//
// 三条过滤（都在 `add` 里）：
//  1. **同平台**：专辑是平台内的概念，网易的《叶惠美》与 QQ 的《叶惠美》不是
//     同一条 id，混在一起会出现「点进去播放时换了平台」这种鬼状态；
//  2. **同专辑名**：聚合的全部依据；
//  3. **艺术家有交集**：防同名专辑污染（不同歌手的《精选》）。锚点艺术家未知时
//     不做这条判断（没有可用判据，宁可不筛）。
func (i *Interceptor) albumTracks(ctx context.Context, anchor online.Track, user string) []online.Track {
	anchor = anchor.Normalized()
	cands := make([]online.Track, 0, 32)
	seen := make(map[string]bool, 32)
	cands = append(cands, anchor)
	seen[anchor.RealID()] = true

	key := albumKey(anchor.Album)
	known := key != "" && key != albumKey(online.UnknownAlbum)
	add := func(t online.Track) {
		t = t.Normalized()
		if !known || t.Platform != anchor.Platform {
			return
		}
		if albumKey(t.Album) != key {
			return
		}
		if !artistOverlap(t.Artists, anchor.Artists) {
			return
		}
		if seen[t.RealID()] {
			return
		}
		seen[t.RealID()] = true
		cands = append(cands, t)
	}

	// ① 本地快照：收藏 + 播放历史里同一张专辑的曲目。零网络，而且它们本来就
	//    存在磁盘上 —— 用户收藏过 / 听过的那几首不该在专辑页里凭空消失。
	for _, f := range i.store.Favorites(user) {
		add(f.Track)
	}
	for _, h := range i.store.History(user) {
		add(h.Track)
	}

	// ② 搜索补足：拿专辑名当关键词。**复用 collectOnline**（多源并发 + 失败只记一笔
	//    + 5 分钟缓存 + 已过滤成可播），不另写一份单平台检索。
	if known && len(cands) < albumCandidateMax {
		for _, t := range i.collectOnline(ctx, anchor.Album) {
			add(t)
			if len(cands) >= albumCandidateMax {
				break
			}
		}
	}

	rctx, cancel := context.WithTimeout(context.Background(), albumResolveBudget)
	defer cancel()
	out := i.playableOnly(rctx, anchor.Platform, cands)
	if len(out) > albumTrackMax {
		out = out[:albumTrackMax]
	}
	return out
}

// artistOverlap 判断两组艺术家是否有交集（忽略大小写与空白差异）。
//
// 占位名（「未知艺术家」）不算交集依据：一方未知时一律返回 true —— 那时没有可用
// 判据，宁可不筛（筛错的代价是专辑页少几首歌，比多出别的专辑的歌更难发现）。
func artistOverlap(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, n := range a {
		if k := artistKey(n); k != "" {
			set[k] = true
		}
	}
	if len(set) == 0 {
		return true
	}
	for _, n := range b {
		if k := artistKey(n); k != "" && set[k] {
			return true
		}
	}
	// 一边完全没有可用艺术家名（全是占位）时不该判定为「不匹配」。
	return len(b) == 0 || onlyUnknownArtist(b)
}

// onlyUnknownArtist 报告一组名字是否全是占位名。
func onlyUnknownArtist(names []string) bool {
	for _, n := range names {
		if artistKey(n) != "" {
			return false
		}
	}
	return true
}

// artistKey 归一化一个艺术家名；占位名与空串返回空串（= 没有判据）。
func artistKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == strings.ToLower(online.UnknownArtist) {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// albumKey 归一化一个专辑名，用于聚合与去重。
//
// 只做「小写 + 空白折叠」：专辑名里的标点（`(`、`-`）是名字的一部分，
// 抹掉会让不同的专辑撞在一起（与 titleArtistKey 的取舍不同，那个要匹配
// 「同一首歌」，只要不重复）。
func albumKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	return strings.Join(strings.Fields(name), " ")
}

// albumDetailVO 构造 `album` 端点的 data：专辑对象 + 内嵌曲目。
//
// 字段集照参考实现的 `_album_detail_payload`（app.py:8260）：卡片字段的超集，
// 外加 `trackCount` 与 `tracks`。曲目条目见 albumTrackVO。
func albumDetailVO(pl vPlaylist, anchorFake string, favorites map[string]bool) map[string]any {
	ts := pl.UpdatedAt
	out := albumCardVO(pl.Name, albumArtist(pl.Tracks, anchorFake), anchorFake, len(pl.Tracks), ts)
	out["guid"] = pl.GUID
	out["coverId"] = pl.CoverID
	tracks := make([]any, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		tracks = append(tracks, albumTrackVO(t, pl.CreatedAt, favorites[t.FakeID()]))
	}
	out["tracks"] = tracks
	return out
}

// albumArtist 取专辑的艺术家名。
//
// 曲目列表里第一首（就是锚点）的主艺术家就是专辑艺术家 —— 官方专辑条目也是这么
// 表达的（专辑对象上的 artists 与曲目上的 artists 同源）。列表为空时兜底占位名。
func albumArtist(tracks []online.Track, anchorFake string) string {
	for _, t := range tracks {
		if t.FakeID() != anchorFake {
			continue
		}
		return t.Artist()
	}
	if len(tracks) > 0 {
		return tracks[0].Artist()
	}
	return online.UnknownArtist
}

// albumTrackVO 构造专辑曲目列表里的一条在线曲目。
//
// # 为什么是「两个 VO 的并集」
//
// 参考实现下发的是它的 `build_online_track`（= 本项目的 SearchVO 形状），而本项目
// 既有的**列表**条目（虚拟歌单曲目列表、歌单里的在线附加曲目）用的是 FavoriteVO。
// 专辑页与歌单页是同一批官方客户端代码渲染的两个列表，但我们**没有真机抓包**
// 能证明该用哪一个：
//
//   - 少一个字段的后果是「那一栏空白」；
//   - 多一个字段没有副作用（前端按字段名读）。
//
// 所以以 FavoriteVO（本项目已验证的列表形状）为底版，把 SearchVO 的**别名**并上去
// （`name` / `artist` / `albumName` / `cover_url` / `format` / `ext` / `duration_ms` …）。
// 只补缺失的键，**绝不覆盖** FavoriteVO 已经写好的值 —— 那些值是「官方列表条目
// 该长什么样」的既有结论，不能被别处的猜测顶掉。
func albumTrackVO(t online.Track, createdAt int64, isFavorite bool) map[string]any {
	pb := online.Playback{}
	out := online.FavoriteVO(t, pb, createdAt, isFavorite, nil)
	for k, v := range online.SearchVO(t, pb, isFavorite) {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

// albumWindow 按官方口径切一页。
//
// 与 writeVirtualTrackList（虚拟歌单）同一套约定：`size == -1` 表示全量（官方约定），
// 页码越界返回空列表而 `total` 不变 —— 客户端据此才知道翻到底了。
func albumWindow(tracks []online.Track, page, size int) []online.Track {
	if size == -1 {
		return tracks
	}
	if size < 1 {
		size = 50
	}
	if page < 1 {
		page = 1
	}
	start := (page - 1) * size
	if start >= len(tracks) {
		return nil
	}
	end := start + size
	if end > len(tracks) {
		end = len(tracks)
	}
	return tracks[start:end]
}

// ── 诊断（真机上的唯一线索，每类只打一条）──────────────────────────────

// albumNoteOnce 打一条一次性诊断（同一个 key 只打第一次）。
//
// 与 suggest.go 的 noteSuggestShape 同一个用意：形状是从参考实现 / 间接证据来的，
// 真机上一旦对不上，这里的日志就是**下一次改动唯一能照着实测走的依据**。
// 全部只打**类型与键名**，不打值（值可能含用户隐私：搜索词、歌单/专辑 id）。
func (i *Interceptor) albumNoteOnce(key string, build func() string) {
	i.albumLogMu.Lock()
	if i.albumLogged == nil {
		i.albumLogged = make(map[string]bool, 4)
	}
	if i.albumLogged[key] {
		i.albumLogMu.Unlock()
		return
	}
	i.albumLogged[key] = true
	i.albumLogMu.Unlock()
	i.logf("%s", build())
}

// noteAlbumSearchShape 记下「官方专辑搜索的 data / data.list 形状不是预期的那种」。
func (i *Interceptor) noteAlbumSearchShape(listVal any, data map[string]any) {
	i.albumNoteOnce("search-shape", func() string {
		kind := jsonTypeName(listVal)
		detail := ""
		if listVal != nil {
			if arr, ok := listVal.([]any); ok && len(arr) > 0 && jsonTypeName(arr[0]) != "object" {
				detail = "；首元素类型 " + jsonTypeName(arr[0])
			}
		}
		dataKeys := ""
		if data != nil {
			dataKeys = "；data 的键名 [" + strings.Join(sortedMapKeys(data), " ") + "]"
		} else {
			dataKeys = "；data 不是对象"
		}
		return "[INTERCEPT] 专辑搜索：官方 data.list 的形状是 " + kind + detail + dataKeys +
			" —— 与「对象数组」不符，这次**不合并**、原样透传。" +
			"（形状取自参考实现 app.py:8755，没有真机抓包；按这条日志实测后再适配）"
	})
}

// noteAlbumUnknownRequest 记下「专辑请求里的 id 反解不出锚点」这件事。
//
// 只在两种**可行动**的场合打：
//
//  1. 候选里出现了本层下发的专辑 guid 形态（`<32hex>:album`）—— 说明客户端拿的
//     是我们给过的 id，而登记表里没有（多半是重启后登记表没重建到它，见文件头第 3 条）；
//  2. 一个 id 候选都没找到 —— 说明客户端把 id 放在我们没读的参数名（或别的形态）上，
//     下一轮把那个名字加进 albumIDParamNames 就行。
//
// 官方专辑页的正常请求（裸 32 位 hex、或干脆不带 id）**不会**触发这两条，所以这条
// 日志出现即代表有活要干。
func (i *Interceptor) noteAlbumUnknownRequest(r *http.Request, cands []string) {
	ours := ""
	for _, c := range cands {
		if albumIDIsOurs(c) {
			ours = c
			break
		}
	}
	path := albumPathShape(r.URL.Path)

	if ours != "" {
		i.albumNoteOnce("unregistered", func() string {
			return "[INTERCEPT] 专辑：" + path + " 收到本层下发的专辑 guid 形态（" + albumIDShape(ours) + "）" +
				"，但登记表里没有它 → 原样透传。多半是重启后这条 id 来自更早的一次搜索" +
				"（专辑锚点不落盘，见 valbum.go 文件头第 3 条）。"
		})
		return
	}
	if len(cands) == 0 {
		// 一个候选都没有（参数名不认识、路径里也没有 id）。这是**唯一**能表明
		// 「我们读错了参数名」的信号 —— 官方的正常请求至少会带一个裸 32hex guid。
		i.albumNoteOnce("no-id", func() string {
			return "[INTERCEPT] 专辑：" + r.Method + " " + path + " 里一个专辑 id 都没认出来" +
				"（query 键 [" + albumQueryKeys(r) + "]）→ 原样透传。若真机上这条日志出现过，" +
				"说明客户端把专辑 id 放在别的名字/形态上，下一轮按它适配。"
		})
		return
	}
	// 其余情况（官方专辑 id、或候选里没有本层的形态）是**正常请求**：不打日志。
}

// albumIDIsOurs 判断一个候选 id 的**形态**是不是本层下发的专辑 guid（`<32hex>:album`）。
//
// 官方的 guid 是裸 32 位 hex（见 docs/飞牛音乐API.md 的 `/track/playlist-detail/list`
// 示例、以及 vo.go 里 `serverGUID` 的来源），带 `:album` 后缀的 32hex 只可能是
// 我们发出去的 —— 所以拿它做诊断判据不会误报正常官方请求。
func albumIDIsOurs(s string) bool {
	s = strings.TrimSpace(s)
	b, found := strings.CutSuffix(s, ":"+online.SubKindAlbum)
	return found && online.IsHex32(b)
}

// albumIDShape 描述一个候选 id 的**形态**（诊断日志用，不打值）。
func albumIDShape(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "空"
	case albumIDIsOurs(s):
		return "<32hex>:album"
	case online.IsHex32(s):
		return "裸 32 位 hex"
	case strings.HasPrefix(s, "online:"):
		return "online: 真实 id"
	}
	// 不认识的形态：只给长度与「像不像 hex」，不给值。
	hex := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			hex = false
			break
		}
	}
	kind := "非 hex"
	if hex {
		kind = "纯 hex"
	}
	return "其它（长度 " + itoa(len(s)) + "，" + kind + "）"
}

// albumPathShape 把专辑三条路由里可能带 id 的尾段折叠成 `{id}`（诊断日志不打值）。
//
// 只认这三条前缀：其它路径原样返回 —— 它们的尾段本来就不该有 id，折叠反而会把
// 「路径长什么样」这条信息丢掉。
func albumPathShape(path string) string {
	for _, prefix := range []string{
		apiPrefix + "/search/album",
		apiPrefix + "/track/album-detail/list",
		apiPrefix + "/album",
	} {
		if subPathAfter(path, prefix) != "" {
			return prefix + "/{id}"
		}
	}
	return path
}

// albumQueryKeys 返回请求的 query **键名**（排序后，逗号分隔）——不取值。
func albumQueryKeys(r *http.Request) string {
	keys := make([]string, 0, 8)
	for k := range r.URL.Query() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}

// sortedMapKeys 返回 map 的键名（排序后）。
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// asObjectList 判断 v 是不是「全是对象的数组」。
//
// 空数组也算（官方搜不到东西时就是它）—— 那种情况合并之后正好只剩在线专辑。
// 元素里有非对象（含 null）就**不算**：那说明官方的形状不是我们以为的那个，
// 这时候原样透传比硬塞更安全（与 suggest.go 的 asStringList 同一条纪律）。
func asObjectList(v any) ([]any, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	for _, item := range arr {
		if _, ok := item.(map[string]any); !ok {
			return nil, false
		}
	}
	return arr, true
}
