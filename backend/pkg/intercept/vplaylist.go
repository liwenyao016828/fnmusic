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

// 虚拟歌单：由拦截层实时生成、插进官方歌单列表的「推荐歌单」。
//
// # 为什么不写官方库
//
// 官方 `music.db` 的 `playlist` 表是**用户在用的资产** —— 能改名、能删、能分享。
// 推荐歌单是每天变的派生内容，写进去会搅乱用户自己的歌单列表，还得处理过期清理。
// 所以只做拦截层内的现造：请求进来时算出来，请求走了只剩缓存，官方库里一行不多。
// （已查证：fnmusic-ext 用的也是这个做法，它的 `recommend.py` 里 4 处
// `sqlite3.connect` 全是 `mode=ro`；见 HANDOVER §9。）
//
// # 沿用 fnmusic-ext 的 guid 命名
//
// 命名与它完全一致，这样用户在两个扩展之间切换时，看到的名字与链接不会变。
// 这是**唯一**从它那里照搬的东西；生成策略按本项目自己的模型重写（见各函数注释）。

const (
	vDailyPrefix = "online:playlist:daily:"
	vHotPrefix   = "online:playlist:hot:"
	vNMPrefix    = "online:playlist:nm:"

	// vDailySize 是每日推荐的曲目数。
	vDailySize = 20
	// vHotSize 是热门推荐的曲目数上限。
	vHotSize = 50

	// vCacheTTL 是「一个虚拟歌单算出来之后留多久」。
	//
	// 不能太短：每算一次都要打搜索/解析（每日推荐）或网易公开接口（热门推荐），
	// 而官方前端在歌单页会反复请求同一份列表。也不能太长：推荐内容得能变。
	vCacheTTL = 30 * time.Minute

	// vFetchTimeout 是单次抓网易公开接口的预算。
	vFetchTimeout = 6 * time.Second
	// vSongBatch 是网易 `song/detail` 一次批量取多少首。
	vSongBatch = 100
)

// vChartID 是「热门推荐」取的网易公开榜单。
//
// 3778678 = 热歌榜。该接口**不需要登录**（实测 `200` + 完整 200 条 `trackIds`），
// 与 `pkg/online` 已经在打的 `music.163.com` 属同一类请求，不算新增依赖。
const vChartID = "3778678"

// vDailyKeywords 是每日推荐在「本地信号不足」时用来补足的种子词。
//
// 为什么不用网易的每日推荐接口：那个要登录，而且它的内容**每天只有一份、
// 当天只有第一个构建者能用**（fnmusic-ext 的 docstring 明写「占用音源名额」）。
// 我们不进那个队列 —— 改用「用户自己的听歌记录 + 按日期轮换的种子」，
// 这样每天每个用户拿到的都是属于他自己的。
var vDailyKeywords = []string{
	"华语流行", "经典老歌", "民谣", "轻音乐", "粤语金曲",
	"摇滚", "爵士", "钢琴曲", "英文流行", "日语流行",
	"新歌速递", "现场版", "翻唱", "女声", "男声",
}

// vEntry 缓存一个算好的虚拟歌单。
type vEntry struct {
	pl vPlaylist
	ts time.Time
}

// vPlaylist 是一个虚拟歌单：元数据 + 曲目。
type vPlaylist struct {
	GUID      string
	Name      string
	CoverID   string
	CreatedAt int64
	UpdatedAt int64
	Tracks    []online.Track
}

// isVirtualPlaylistGUID 判断一个 guid 是不是我们生成的推荐歌单。
func isVirtualPlaylistGUID(guid string) bool {
	s := strings.TrimSpace(guid)
	return strings.HasPrefix(s, vDailyPrefix) ||
		strings.HasPrefix(s, vHotPrefix) ||
		strings.HasPrefix(s, vNMPrefix) ||
		strings.HasPrefix(s, vChartPrefix)
}

// vUserSuffix 取官方 user guid 里的字母数字前 12 位，用于按账户隔离歌单 guid。
//
// 算法与 fnmusic-ext 一致（先剔非字母数字、再截 12 位）—— 这样算出来的 guid
// 才和它对齐，用户切换扩展时看到的是同一个歌单。
func vUserSuffix(user string) string {
	var b strings.Builder
	for _, r := range user {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			if b.Len() >= 12 {
				break
			}
		}
	}
	return b.String()
}

// vDailyGUID / vHotGUID / vNMGUID 拼出各类虚拟歌单的 guid。
//
// 形状（与 fnmusic-ext 一致）：
//
//	online:playlist:daily:<YYYYMMDD>:<用户前缀>
//	online:playlist:hot:<YYYYMMDD>:<用户前缀>
//	online:playlist:nm:<网易歌单 id>
//
// 歌单 guid 里带 `:` 是安全的 —— 官方对**歌单 guid** 与**曲目 guid** 走不同的
// 校验路径。曲目 guid 仍然必须是 32 位纯 hex。
func vDailyGUID(now time.Time, user string) string {
	g := vDailyPrefix + now.Format("20060102")
	if s := vUserSuffix(user); s != "" {
		g += ":" + s
	}
	return g
}

func vHotGUID(now time.Time, user string) string {
	g := vHotPrefix + now.Format("20060102")
	if s := vUserSuffix(user); s != "" {
		g += ":" + s
	}
	return g
}

func vNMGUID(id string) string { return vNMPrefix + strings.TrimSpace(id) }

// ── 生成 ────────────────────────────────────────────────────────────────

// dailyPlaylist 生成「每日推荐」。
//
// 确定性：同一天、同一个用户算出来的是同一份（guid 带日期，结果进缓存）。
// 种子优先级：
//
//  1. 播放历史里出现**次数最多**的歌手（他真在听谁）
//  2. 收藏里的歌手（没有历史时兜底）
//  3. 按日期轮换的关键词（信号不足时补足）
//
// 候选全部来自 `collectOnline`，它内部已经做了**可播放性**过滤 —— 查得到点不动
// 比查不到更糟，所以解析不出直链的曲目根本不进列表。已经收藏过的也会被排除：
// 推一首他早就收藏的歌没有意义。
func (i *Interceptor) dailyPlaylist(ctx context.Context, r *http.Request, now time.Time) vPlaylist {
	user := i.userKey(r)
	favs := i.store.FavoriteGUIDs(user)

	var out []online.Track
	seen := make(map[string]bool, vDailySize*2)

	for _, seed := range i.dailySeeds(user, now) {
		if len(out) >= vDailySize {
			break
		}
		for _, t := range i.collectOnline(ctx, seed) {
			if len(out) >= vDailySize {
				break
			}
			if seen[t.RealID()] || favs[t.FakeID()] {
				continue
			}
			seen[t.RealID()] = true
			out = append(out, t)
		}
	}

	ts := now.Unix()
	return vPlaylist{
		GUID:      vDailyGUID(now, user),
		Name:      "每日推荐 " + now.Format("01-02"),
		CreatedAt: ts,
		UpdatedAt: ts,
		Tracks:    out,
	}
}

// dailySeeds 排出每日推荐要搜的种子词。
func (i *Interceptor) dailySeeds(user string, now time.Time) []string {
	counts := make(map[string]int, 32)
	add := func(t online.Track) {
		for _, a := range t.Artists {
			if a = strings.TrimSpace(a); a != "" {
				counts[a]++
			}
		}
	}
	for _, h := range i.store.History(user) {
		add(h.Track)
	}
	if len(counts) == 0 {
		// 没有播放历史（新装的用户）时退回收藏，再不行就全靠关键词。
		for _, f := range i.store.Favorites(user) {
			add(f.Track)
		}
	}

	type kv struct {
		name string
		n    int
	}
	ranked := make([]kv, 0, len(counts))
	for k, v := range counts {
		ranked = append(ranked, kv{k, v})
	}
	// 并列时按名字排 —— 否则顺序取决于 map 遍历，同一个用户刷两次会拿到
	// 两个不同的推荐歌单，看起来像 bug。
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].n != ranked[b].n {
			return ranked[a].n > ranked[b].n
		}
		return ranked[a].name < ranked[b].name
	})

	out := make([]string, 0, 8)
	for idx, e := range ranked {
		if idx >= 3 {
			break
		}
		out = append(out, e.name)
	}

	// 关键词兜底：起点由日期决定，所以同一天是稳定的、隔天会换。
	start := 0
	for _, c := range now.Format("20060102") {
		start += int(c)
	}
	for n := 0; n < len(vDailyKeywords) && len(out) < 6; n++ {
		out = append(out, vDailyKeywords[(start+n)%len(vDailyKeywords)])
	}
	return out
}

// hotPlaylist 生成「热门推荐」：取网易公开榜单。
func (i *Interceptor) hotPlaylist(ctx context.Context, r *http.Request, now time.Time) vPlaylist {
	user := i.userKey(r)
	_, _, tracks := i.nmFetch(ctx, vChartID, vHotSize)

	ts := now.Unix()
	return vPlaylist{
		GUID:      vHotGUID(now, user),
		Name:      "热门推荐",
		CreatedAt: ts,
		UpdatedAt: ts,
		Tracks:    tracks,
	}
}

// nmPlaylist 生成「网易歌单」虚拟歌单：guid 里自包含网易歌单 id，无需反解表。
func (i *Interceptor) nmPlaylist(ctx context.Context, id string, now time.Time) vPlaylist {
	name, _, tracks := i.nmFetch(ctx, id, vHotSize)
	if strings.TrimSpace(name) == "" {
		name = "网易歌单 " + id
	}

	ts := now.Unix()
	return vPlaylist{
		GUID:      vNMGUID(id),
		Name:      name,
		CreatedAt: ts,
		UpdatedAt: ts,
		Tracks:    tracks,
	}
}

// ── 取用 ────────────────────────────────────────────────────────────────

// vPlaylistFor 取一个虚拟歌单（带缓存）。
//
// 第二个返回值表示「这个 guid 是不是虚拟歌单」—— 调用方据此决定是短路自己应答，
// 还是照常把请求转发给官方。**非虚拟 guid 会立刻返回 false**，不做任何网络动作。
func (i *Interceptor) vPlaylistFor(guid string, r *http.Request) (vPlaylist, bool) {
	guid = strings.TrimSpace(guid)
	if !isVirtualPlaylistGUID(guid) {
		return vPlaylist{}, false
	}

	i.vMu.Lock()
	if e, ok := i.vCache[guid]; ok {
		// 空结果（拉取失败 / 整榜都不可播）只留 vChartNegTTL：一次上游抖动
		// 不该让榜单消失半小时，但也不能放成「每次请求都重新打上游」。
		ttl := vCacheTTL
		if len(e.pl.Tracks) == 0 {
			ttl = vChartNegTTL
		}
		if time.Since(e.ts) < ttl {
			i.vMu.Unlock()
			return e.pl, true
		}
	}
	i.vMu.Unlock()

	now := time.Now()
	var pl vPlaylist
	switch {
	case strings.HasPrefix(guid, vDailyPrefix):
		pl = i.dailyPlaylist(r.Context(), r, now)
	case strings.HasPrefix(guid, vHotPrefix):
		pl = i.hotPlaylist(r.Context(), r, now)
	case strings.HasPrefix(guid, vChartPrefix):
		ref, ok := parseVChartRef(strings.TrimPrefix(guid, vChartPrefix))
		if !ok {
			return vPlaylist{}, false
		}
		pl = i.chartPlaylist(r.Context(), ref)
	default:
		pl = i.nmPlaylist(r.Context(), strings.TrimPrefix(guid, vNMPrefix), now)
	}

	// 曲目登记进虚拟 id 表，后面的取流 / 歌词 / 封面才认得出它们。
	for _, t := range pl.Tracks {
		i.registry.Put(t)
	}
	pl.GUID = guid
	pl.CoverID = i.vPickCover(pl.Tracks, guid)

	i.vMu.Lock()
	if len(i.vCache) > 32 {
		i.vCache = make(map[string]vEntry, 8)
	}
	i.vCache[guid] = vEntry{pl: pl, ts: now}
	i.vMu.Unlock()

	i.logf("[INTERCEPT] 虚拟歌单 %s → %d 首（%s）", pl.Name, len(pl.Tracks), pl.GUID)
	return pl, true
}

// vPickCover 选歌单封面用的 coverId。
//
// 借的是**第一首带封面的在线曲目的虚拟 id** —— 封面端点对在线曲目的虚拟 id
// 本来就能出图（走 `pkg/intercept/cover.go`），所以歌单卡片能直接显示真实封面，
// 不需要为歌单再造一条封面链路。一首都没有封面时退回歌单自己的 guid，
// 官方前端会显示它的默认样式。
func (i *Interceptor) vPickCover(tracks []online.Track, guid string) string {
	for _, t := range tracks {
		if strings.TrimSpace(t.CoverURL) == "" {
			continue
		}
		if fake := t.FakeID(); fake != "" {
			return fake
		}
	}
	return guid
}

// vPlaylistCards 返回要插进官方歌单列表的推荐歌单卡片。
//
// 顺序：**每日推荐在前、热门推荐在后**（每日推荐是更个人化的那一份）。
//
// **空歌单不注入**：一个点进去什么都没有的「每日推荐」卡片比没有卡片更糟 ——
// 用户会以为功能坏了。算不出曲目时（网易不可达、搜索全挂、曲库太空）卡片就
// 不出现，官方列表照常。这也是整层的失效方向：最坏情况只是推荐消失。
func (i *Interceptor) vPlaylistCards(r *http.Request) []any {
	now := time.Now()
	user := i.userKey(r)

	out := make([]any, 0, 8)
	for _, g := range []string{vDailyGUID(now, user), vHotGUID(now, user)} {
		pl, ok := i.vPlaylistFor(g, r)
		if !ok || len(pl.Tracks) == 0 {
			continue
		}
		out = append(out, vPlaylistCard(pl))
	}
	// 榜单排在推荐之后：推荐是每天变的「给你听的」，榜单是常驻目录。
	for _, pl := range i.chartCards(r.Context(), r) {
		out = append(out, vPlaylistCard(pl))
	}
	return out
}

// vPlaylistCard 按官方歌单卡片的字段形状构造一张卡片。
//
// 字段集照抄 fnmusic-ext 的 `build_playlist_record` —— 那个形状是在真机上
// 被官方前端渲染过的，别自己改字段名。
func vPlaylistCard(pl vPlaylist) map[string]any {
	return map[string]any{
		"guid":       pl.GUID,
		"name":       pl.Name,
		"coverId":    pl.CoverID,
		"createdAt":  pl.CreatedAt,
		"updatedAt":  pl.UpdatedAt,
		"trackCount": len(pl.Tracks),
		"isDaily":    true,
	}
}

// vPlaylistDetail 构造 `playlist/detail` 的 data。
//
// 官方对虚拟歌单没有记录，所以这里给的是卡片字段的超集：详情页要读的
// name/coverId/trackCount 都在，另补 `description` 与 `isFavorite`，
// 免得客户端读到 null 时行为不可预期。
func vPlaylistDetail(pl vPlaylist) map[string]any {
	out := vPlaylistCard(pl)
	out["description"] = ""
	out["isFavorite"] = false
	out["source"] = "qulv"
	return out
}

// writeVirtualTrackList 应答虚拟歌单的曲目列表。
//
// 分页口径与官方歌单一致：`size == -1` 表示全量（官方约定），页码越界返回空
// 列表而 total 保持不变 —— 与官方搜索那边的处理一样，客户端据此才知道翻到底了。
//
// 虚拟歌单没有官方段，所以窗口直接落在自己的曲目上，不需要官方「先占一段」的
// 那套偏移换算（见 handlePlaylistTrackList）。
func (i *Interceptor) writeVirtualTrackList(w http.ResponseWriter, r *http.Request, pl vPlaylist, page, size int) {
	tracks := pl.Tracks
	total := len(tracks)

	if size > 0 {
		start := (page - 1) * size
		if start < 0 {
			start = 0
		}
		switch end := start + size; {
		case start >= total:
			tracks = nil
		case end > total:
			tracks = tracks[start:]
		default:
			tracks = tracks[start:end]
		}
	}

	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	list := make([]any, 0, len(tracks))
	for _, t := range tracks {
		fake := i.registry.Put(t)
		list = append(list, online.FavoriteVO(t, online.Playback{}, pl.CreatedAt, favorites[fake], nil))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0,
		"msg":  "",
		"data": map[string]any{"list": list, "total": total},
	})
}

// ── 网易公开接口（免登录）────────────────────────────────────────────────

// 这两个端点实测都返回 `200` 且不需要任何凭据（NAS 与开发机各验过一次）：
//
//	GET /api/v6/playlist/detail?id=<id>  → playlist.name / coverImgUrl / trackIds[].id
//	GET /api/song/detail?ids=[...]       → songs[].{id,name,dt,ar[].name,al.name,al.picUrl}
//
// ⚠️ `playlist` 里只带**前 10 首**完整曲目，完整列表在 `trackIds`（热歌榜是 200 条）。
// 只读 `playlist.tracks` 会让「热歌榜」变成 10 首 —— 必须再打一次 `song/detail` 批量取。

const neteaseAPIBase = "https://music.163.com"

type nmPlaylistPayload struct {
	Playlist struct {
		Name        string `json:"name"`
		CoverImgURL string `json:"coverImgUrl"`
		TrackCount  int    `json:"trackCount"`
		TrackIDs    []struct {
			ID json.Number `json:"id"`
		} `json:"trackIds"`
		Tracks []nmSong `json:"tracks"`
	} `json:"playlist"`
}

type nmSong struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
	// Dt 是毫秒；有的响应给 `duration` 也是毫秒。两个都收。
	Dt       json.Number `json:"dt"`
	Duration json.Number `json:"duration"`
	Ar       []struct {
		Name string `json:"name"`
	} `json:"ar"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Al struct {
		Name   string `json:"name"`
		PicURL string `json:"picUrl"`
	} `json:"al"`
	Album struct {
		Name   string `json:"name"`
		PicURL string `json:"picUrl"`
	} `json:"album"`
}

type nmSongPayload struct {
	Songs []nmSong `json:"songs"`
}

// neteasePlaylist 取一个网易公开歌单：返回歌单名、封面直链、以及**已过滤成可播**的曲目。
//
// 任何一步失败都返回空 —— 虚拟歌单少一份没关系，但绝不能让它把官方歌单列表
// 拖垮或者报错。
func (i *Interceptor) neteasePlaylist(ctx context.Context, id string, limit int) (string, string, []online.Track) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, vFetchTimeout)
	defer cancel()

	var pl nmPlaylistPayload
	if err := i.nmGetJSON(ctx, "/api/v6/playlist/detail?id="+urlQueryEscape(id), &pl); err != nil {
		i.logf("[INTERCEPT] 取网易歌单 %s 失败：%v", id, err)
		return "", "", nil
	}

	ids := make([]string, 0, len(pl.Playlist.TrackIDs))
	for _, t := range pl.Playlist.TrackIDs {
		if s := strings.TrimSpace(t.ID.String()); s != "" && s != "0" {
			ids = append(ids, s)
		}
	}
	// trackIds 为空时退回 playlist.tracks 里那 10 首（端点行为变化时的兜底）。
	if len(ids) == 0 {
		for _, t := range pl.Playlist.Tracks {
			if s := strings.TrimSpace(t.ID.String()); s != "" {
				ids = append(ids, s)
			}
		}
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}

	tracks := i.neteaseSongs(ctx, ids)
	return pl.Playlist.Name, pl.Playlist.CoverImgURL, i.playableOnly(ctx, "wy", tracks)
}

// neteaseSongs 批量取网易曲目详情。
func (i *Interceptor) neteaseSongs(ctx context.Context, ids []string) []online.Track {
	if len(ids) == 0 {
		return nil
	}
	out := make([]online.Track, 0, len(ids))
	for start := 0; start < len(ids); start += vSongBatch {
		end := start + vSongBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		var payload nmSongPayload
		path := "/api/song/detail?ids=[" + strings.Join(batch, ",") + "]"
		if err := i.nmGetJSON(ctx, path, &payload); err != nil {
			i.logf("[INTERCEPT] 取网易曲目详情失败（%d 首）：%v", len(batch), err)
			continue
		}
		for _, s := range payload.Songs {
			if t, ok := nmTrack(s); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// nmTrack 把网易的单曲结构转成本项目的在线曲目描述符。
func nmTrack(s nmSong) (online.Track, bool) {
	id := strings.TrimSpace(s.ID.String())
	title := strings.TrimSpace(s.Name)
	if id == "" || id == "0" || title == "" {
		return online.Track{}, false
	}

	var artists []string
	for _, a := range s.Ar {
		if n := strings.TrimSpace(a.Name); n != "" {
			artists = append(artists, n)
		}
	}
	if len(artists) == 0 {
		for _, a := range s.Artists {
			if n := strings.TrimSpace(a.Name); n != "" {
				artists = append(artists, n)
			}
		}
	}

	album, cover := s.Al.Name, s.Al.PicURL
	if strings.TrimSpace(album) == "" {
		album = s.Album.Name
	}
	if strings.TrimSpace(cover) == "" {
		cover = s.Album.PicURL
	}

	// 时长统一压到秒。网易给的 dt 是毫秒。
	dur := nmMillis(s.Dt)
	if dur <= 0 {
		dur = nmMillis(s.Duration)
	}

	return online.Track{
		Platform:   "wy",
		PlatformID: id,
		Title:      title,
		Artists:    artists,
		Album:      strings.TrimSpace(album),
		Duration:   dur,
		CoverURL:   strings.TrimSpace(cover),
	}.Normalized(), true
}

// nmMillis 把毫秒字段换成秒；值看起来已经是秒时原样返回。
func nmMillis(v json.Number) int {
	n, err := v.Int64()
	if err != nil || n <= 0 {
		return 0
	}
	if n > 3600 {
		return int(n / 1000)
	}
	return int(n)
}

// playableOnly 只保留解析得出直链的曲目。
//
// 虚拟歌单的曲目点开就要能响 —— 这是「查得到点不动比查不到更糟」那条规矩
// 在推荐场景的延伸。
func (i *Interceptor) playableOnly(ctx context.Context, platform string, tracks []online.Track) []online.Track {
	if len(tracks) == 0 || i.pool == nil {
		return tracks
	}
	ids := make([]string, 0, len(tracks))
	for _, t := range tracks {
		ids = append(ids, t.PlatformID)
	}
	resolved := i.pool.ResolveMany(ctx, platform, ids)

	out := make([]online.Track, 0, len(tracks))
	seen := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		if _, ok := resolved[t.PlatformID]; !ok {
			continue
		}
		if seen[t.RealID()] {
			continue
		}
		seen[t.RealID()] = true
		out = append(out, t)
	}
	return out
}

// nmGetJSON 打一次网易公开接口并解出 JSON。
func (i *Interceptor) nmGetJSON(ctx context.Context, path string, out any) error {
	req, err := online.NewRequest(ctx, http.MethodGet, neteaseAPIBase+path, neteaseAPIBase+"/")
	if err != nil {
		return err
	}
	resp, err := online.SharedClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errStatus(resp.StatusCode)
	}
	// 响应体可能很大（600KB 级），但只是解析一次，不退化成流式读。
	return json.NewDecoder(resp.Body).Decode(out)
}

type errStatus int

func (e errStatus) Error() string { return "网易接口返回 " + itoa(int(e)) }

// urlQueryEscape 只转义会破坏 query 的字符。
//
// 歌单 id 是纯数字，本来不需要转义；留着这一层是为了以后接 `nm:<id>` 之外的
// 形态时不会因为拼接而破掉。
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteRune(r)
			continue
		}
		b.WriteString("%")
		const hexDigits = "0123456789ABCDEF"
		b.WriteByte(hexDigits[(r>>4)&0xF])
		b.WriteByte(hexDigits[r&0xF])
	}
	return b.String()
}
