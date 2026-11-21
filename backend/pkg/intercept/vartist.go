package intercept

import (
	"context"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 本文件接管四条「在线歌手」相关的只读端点（v2.33.0，对齐上游 v3.2.0 的 issue #141）：
//
//	GET /music/api/v1/search/artist           搜索页「歌手」Tab（官方结果 + 在线补后）
//	GET /music/api/v1/artist [/detail]        歌手详情
//	GET /music/api/v1/track/artist-detail/list  歌手的歌曲列表
//	GET /music/api/v1/album/artist-detail/list  歌手的专辑列表
//
// # 为什么需要「歌手虚拟 id」这条新链路
//
// 曲目虚拟 id 派生自「平台 + 曲目 id」，曲目的 artists[] 以前只有「曲目 id + :artist」——
// 那只能表达「这首歌的歌手叫 X」，点进去时我们不知道用户想看的是**哪一位** X：
// 反解不出平台内歌手 id，只能透传给官方，而官方库里没有在线歌手 → 点了没反应。
//
// 现在搜索层拿到平台内歌手 id（网易 ar[].id）时会登记进歌手表（online.ArtistRegistry），
// 下发**裸 32 位 hex** 的歌手虚拟 id。客户端点回来，我们按同一套哈希反解出
// 「平台 + 歌手 id」，再去问平台的原生歌手接口（pkg/search/artist.go）。
//
// # 只接了网易云歌手
//
// QQ 的老歌手接口（fcg_v8_singer_track_cp / fcg_v8_singer_album_cp）实测已 404 下线；
// 酷狗 / 酷我 / 外挂平台本就没有可用的原生歌手接口。所以本轮只有 wy 的歌手有这四种能力，
// 其它平台的歌手 guid 照旧反解不出来 → 原样透传（= 今天的行为，不退化）。
// QQ 的**专辑**接口（fcg_v8_album_info_cp）是通的，由 valbum.go 的原生专辑详情接。
const (
	// artistSearchLimit 是一次歌手搜索最多补几位在线歌手。
	//
	// 与上游同为 10：歌手 Tab 一屏就这么多，多补只会把官方歌手挤下去。
	artistSearchLimit = 10

	// artistListDefault / artistListMax 是歌手曲目 / 专辑列表的默认与最大每页条数。
	artistListDefault = 50
	artistListMax     = 100

	// artistFetchBudget 是单次原生歌手 / 专辑接口请求的等待上限。
	//
	// 与歌词 / 封面同量级：这些都在「用户已经点进去了」的路径上，等太久不如快点回一个
	// 明确的空列表。它只包住平台请求，不含 VO 构造。
	artistFetchBudget = 6 * time.Second
)

// artistIDParamNames 是所有可能承载「歌手虚拟 id」的查询参数名。
//
// 上游参考实现读 artistGUID / artistGuid / guid / id / artistId；这里把 coverId 也列上
// （歌手头像的 coverId 就是歌手虚拟 id，客户端拿它当歌手 id 回传是可能的）。
var artistIDParamNames = []string{
	"artistGUID", "artistGuid", "artistguid", "artistId", "artist_id",
	"guid", "coverId", "cover_id", "id",
}

// artistRequestIDs 收集一个歌手请求里所有可能是「歌手虚拟 id」的取值。
func artistRequestIDs(r *http.Request, sub string) []string {
	q := r.URL.Query()
	out := make([]string, 0, len(artistIDParamNames)+len(guidParamNames)+2)
	for _, k := range artistIDParamNames {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			out = append(out, v)
		}
	}
	out = append(out, guidCandidates(r, sub)...)
	return out
}

// artistByRequest 把请求里的 id 反解成一位登记过的在线歌手。
//
// 认得的形态：裸 32hex（下发时的原形）与 `<32hex>:artist`（客户端有可能带后缀回来，
// 与其它虚拟 id 的习惯一致）。反解只查登记表 —— 官方歌手的 id 不在表里，
// 「查得到」本身就是「这是我们的」的证据，绝不会拿一个官方 id 去猜歌手身份。
func (i *Interceptor) artistByRequest(r *http.Request, sub string) (online.Artist, bool) {
	for _, c := range artistRequestIDs(r, sub) {
		c = strings.TrimSpace(c)
		c = strings.TrimSuffix(c, ":"+online.SubKindArtist)
		if a, ok := i.artists.Lookup(c); ok {
			return a, true
		}
	}
	return online.Artist{}, false
}

// ── GET /search/artist ─────────────────────────────────────────────────

// handleArtistSearch 接管搜索页「歌手」Tab。
//
// 与 handleAlbumSearch 同一个形状：官方在前、在线补在后；官方非 JSON / 业务码非 0 /
// 形状不认识 / 没有可加的 —— 一律逐字节原样透传（能不动官方结果就不动）。
func (i *Interceptor) handleArtistSearch(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	q := r.URL.Query()
	keyword := strings.TrimSpace(firstNonEmpty(q.Get("keyword"), q.Get("q"), q.Get("query"), q.Get("wd")))

	// 在线那一路与官方转发并发跑（串行的话再快也要等官方往返）。
	var ch chan []online.Artist
	if keyword != "" {
		ch = make(chan []online.Artist, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), artistFetchBudget)
			defer cancel()
			hits := i.artistSearch()(ctx, keyword, artistSearchLimit)
			arts := make([]online.Artist, 0, len(hits))
			for _, h := range hits {
				a, ok := (online.Artist{
					Platform:   "wy",
					ArtistID:   h.ID,
					Name:       h.Name,
					CoverURL:   h.Cover,
					AlbumCount: h.AlbumCount,
					TrackCount: h.TrackCount,
				}).Normalized()
				if ok {
					arts = append(arts, a)
				}
			}
			ch <- arts
		}()
	}

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
	official, isObjectList := asObjectList(data["list"])
	if !isObjectList {
		writeRaw(w, raw, status, ct)
		return true
	}

	// 与官方重名的不注入（用户看到两个「周杰伦」只会以为是重复项）。
	seen := make(map[string]bool, len(official))
	for _, rawA := range official {
		if m := asMap(rawA); m != nil {
			if n := strings.TrimSpace(asString(m["name"])); n != "" {
				seen[artistKey(n)] = true
			}
		}
	}
	add := make([]any, 0, artistSearchLimit)
	for _, a := range i.waitArtistSearch(ch) {
		k := artistKey(a.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		fake := i.artists.Put(a)
		if fake == "" {
			continue
		}
		add = append(add, artistCardVO(a, fake))
	}
	if len(add) == 0 {
		writeRaw(w, raw, status, ct)
		return true
	}

	merged := make([]any, 0, len(official)+len(add))
	merged = append(merged, official...)
	merged = append(merged, add...)
	data["list"] = merged
	bumpAlbumTotal(data, len(add), len(merged)) // 逻辑通用：给 data.total 加上注入条数
	obj["data"] = data
	writeJSON(w, http.StatusOK, obj)
	return true
}

func (i *Interceptor) waitArtistSearch(ch chan []online.Artist) []online.Artist {
	if ch == nil {
		return nil
	}
	timer := time.NewTimer(artistFetchBudget)
	defer timer.Stop()
	select {
	case arts := <-ch:
		return arts
	case <-timer.C:
		return nil
	}
}

// artistCardVO 是搜索结果 / 歌手列表里的一行歌手。
//
// 形状照上游 `_artist_list_obj`：`coverId` 就是歌手虚拟 id 本身 —— 客户端拿它去
// `/static/cover?coverId=<guid>` 取头像（cover.go 里加了对歌手 / 专辑的兜底解析）。
func artistCardVO(a online.Artist, fake string) map[string]any {
	return map[string]any{
		"guid":       fake,
		"name":       a.Name,
		"coverId":    fake,
		"albumCount": a.AlbumCount,
		"trackCount": a.TrackCount,
	}
}

// ── GET /artist [/detail] ──────────────────────────────────────────────

// handleArtistDetail 返回一位在线歌手的详情。
//
// 字段给得比上游的原生 artist 对象多一些（`guid` / `cover` / `coverId` 等别名）——
// 官方客户端的歌手页读哪几个字段我们没有真机抓包，多给字段没有副作用（前端按名读），
// 少给一个就是那一栏空白。核心身份（`guid` / `name`）一定在。
func (i *Interceptor) handleArtistDetail(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	sub := subPathAfter(r.URL.Path, apiPrefix+"/artist")
	a, ok := i.artistByRequest(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), artistFetchBudget)
	defer cancel()
	info, err := i.artistDetail()(ctx, a.ArtistID)
	if err != nil || strings.TrimSpace(info.Name) == "" {
		// 平台没给详情：透传官方（而不是回一个空壳让界面空白）。
		i.passThrough(w, r)
		return true
	}
	fake := a.FakeID()
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok",
		"data": map[string]any{
			"guid":       fake,
			"id":         info.ID,
			"name":       info.Name,
			"cover":      info.Cover,
			"picUrl":     info.Cover,
			"coverId":    fake,
			"albumCount": info.AlbumCount,
			"trackCount": info.TrackCount,
			"albumSize":  info.AlbumCount,
			"musicSize":  info.TrackCount,
			"briefDesc":  info.BriefDesc,
		},
	})
	return true
}

// ── GET /track/artist-detail/list ──────────────────────────────────────

// handleArtistTrackList 返回一位在线歌手的歌曲列表。
//
// 曲目走原生歌手歌曲接口（FetchArtistTracksNETEASE），再复用 search.trackFromSong →
// registry → albumTrackVO 这条既有链路：下发的曲目 VO 与搜索 / 专辑页**完全同形**，
// 因此取流 / 取歌词 / 取封面 / 收藏都直接可用，不需要为歌手页再造一条。
func (i *Interceptor) handleArtistTrackList(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	sub := subPathAfter(r.URL.Path, apiPrefix+"/track/artist-detail/list")
	a, ok := i.artistByRequest(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}

	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiSigned(q.Get("size"), artistListDefault)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > artistListMax {
		size = artistListDefault
	}
	sortKey := firstNonEmpty(q.Get("sort"), q.Get("order"))
	order := "hot"
	lower := strings.ToLower(sortKey)
	if strings.Contains(lower, "createdat") || lower == "time" || lower == "new" {
		order = "time"
	}
	offset := (page - 1) * size

	ctx, cancel := context.WithTimeout(r.Context(), artistFetchBudget)
	defer cancel()
	songs, total, err := i.artistTracks()(ctx, a.ArtistID, offset, size, order)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{"list": []any{}, "total": 0, "sort": sortKey},
		})
		return true
	}

	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	ts := time.Now().Unix()
	list := make([]any, 0, len(songs))
	for _, s := range songs {
		t, ok := trackFromSong(a.Platform, s)
		if !ok {
			continue
		}
		i.registry.Put(t) // 曲目要能被取流 / 取歌词 / 取封面反解出来
		list = append(list, albumTrackVO(t, ts, favorites[t.FakeID()]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok",
		"data": map[string]any{"list": list, "total": total, "sort": sortKey},
	})
	return true
}

// ── GET /album/artist-detail/list ──────────────────────────────────────

// handleArtistAlbumList 返回一位在线歌手的专辑列表。
//
// 平台给出的专辑卡片只有身份信息（id / 名字 / 封面 / 曲目数），一首曲目都不带 ——
// 所以这里登记的是**纯专辑锚点**（online.AlbumRef），下发虚拟 id；点进专辑页时由
// albumFor 反解成专辑锚点、走原生专辑接口取完整曲目（见 valbum.go）。
func (i *Interceptor) handleArtistAlbumList(w http.ResponseWriter, r *http.Request) bool {
	if i.albumRejectMethod(w, r) {
		return true
	}
	sub := subPathAfter(r.URL.Path, apiPrefix+"/album/artist-detail/list")
	a, ok := i.artistByRequest(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}

	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiSigned(q.Get("size"), artistListDefault)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > artistListMax {
		size = artistListDefault
	}
	offset := (page - 1) * size

	ctx, cancel := context.WithTimeout(r.Context(), artistFetchBudget)
	defer cancel()
	albums, total, err := i.artistAlbums()(ctx, a.ArtistID, offset, size)
	if err != nil || len(albums) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{"list": []any{}, "total": total},
		})
		return true
	}

	ts := time.Now().Unix()
	singer := a.Name
	list := make([]any, 0, len(albums))
	for _, al := range albums {
		ref := online.AlbumRef{
			Platform:   a.Platform,
			AlbumID:    al.ID,
			Name:       al.Name,
			Artist:     firstNonEmpty(al.Artist, singer),
			CoverURL:   al.Cover,
			TrackCount: al.Size,
			Year:       al.Year,
		}
		fake := i.albumRefs.Put(ref)
		if fake == "" {
			continue
		}
		list = append(list, map[string]any{
			"guid": fake,
			"name": al.Name,
			"artists": []any{map[string]any{
				"guid":      a.FakeID(),
				"name":      singer,
				"coverId":   nil,
				"createdAt": ts,
				"updatedAt": ts,
			}},
			"coverId":     fake,
			"releaseDate": nil,
			"barcode":     nil,
			"createdAt":   ts,
			"updatedAt":   ts,
			"trackCount":  al.Size,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok",
		"data": map[string]any{"list": list, "total": total},
	})
	return true
}
