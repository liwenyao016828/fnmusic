package intercept

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// playlistGUIDFrom 从 query 或 body 里取歌单 guid。
func playlistGUIDFrom(r *http.Request, body map[string]any) string {
	return queryOrBody(r, body, "playlistGUID", "playlistGuid", "playlistguid", "guid", "playlistId")
}

// atoiSigned 解析可带负号的整数。
//
// 单独写一个是因为官方有 `size == -1` 表示「全量」的约定，而通用的
// atoiDefault 会把非正数一律换成默认值 —— 那会把「全量」悄悄变成「第一页」。
func atoiSigned(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// bumpTrackCounts 把在线附加条目数加进官方歌单的 trackCount。
//
// 官方前端的歌单卡片直接显示这个数字。不补的话会出现「歌单里有 12 首、
// 卡片写着 10 首」这种一眼可见的不一致。
func bumpTrackCounts(list []any, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	for _, raw := range list {
		m := asMap(raw)
		if m == nil {
			continue
		}
		guid := firstString(m, "guid", "id")
		extra := counts[guid]
		if extra == 0 {
			continue
		}
		base := asInt(m["trackCount"])
		if base == 0 {
			base = asInt(m["track_count"])
		}
		m["trackCount"] = base + extra
		m["track_count"] = base + extra
	}
}

// handlePlaylistList 接管 `GET /music/api/v1/playlist/list`。
//
// 两件事：
//
//  1. 把在线附加条目数补进官方歌单的 trackCount。
//  2. 把**推荐歌单**（虚拟歌单）插在列表最前 —— 官方库里没有它们的记录，
//     是这一层实时造的，见 vplaylist.go 的文件头注释。
//
// 插入位置是**最前**：推荐是「今天听什么」的入口，混在用户自己的歌单中间
// 会被当成用户资产（会被试着改名、删除）。
func (i *Interceptor) handlePlaylistList(w http.ResponseWriter, r *http.Request) bool {
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
	bumpTrackCounts(asSlice(data["list"]), i.store.PlaylistTrackCounts(i.userKey(r)))

	if virtual := i.vPlaylistCards(r); len(virtual) > 0 {
		official := asSlice(data["list"])
		merged := make([]any, 0, len(virtual)+len(official))
		merged = append(merged, virtual...)
		merged = append(merged, official...)
		data["list"] = merged
		// total 也要跟着加，否则客户端按 total 判断「还有下一页」时会翻到空页。
		if total := asInt(data["total"]); total >= 0 {
			data["total"] = total + len(virtual)
		}
	}

	obj["data"] = data
	writeJSON(w, http.StatusOK, obj)
	return true
}

// handlePlaylistDetail 接管 `GET /music/api/v1/playlist/detail`。
func (i *Interceptor) handlePlaylistDetail(w http.ResponseWriter, r *http.Request) bool {
	// 虚拟歌单官方库里没有记录 —— **必须在打上游之前短路**。否则会先拿到
	// 官方「找不到该歌单」的业务错误，再原样透传回去，客户端就什么都看不到。
	if pl, ok := i.vPlaylistFor(playlistGUIDFrom(r, nil), r); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0, "msg": "", "data": vPlaylistDetail(pl),
		})
		return true
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
	guid := playlistGUIDFrom(r, nil)
	if guid == "" {
		guid = firstString(data, "guid", "id")
	}
	extra := i.store.PlaylistTrackCounts(i.userKey(r))[guid]
	if extra > 0 {
		base := asInt(data["trackCount"])
		if base == 0 {
			base = asInt(data["track_count"])
		}
		data["trackCount"] = base + extra
		data["track_count"] = base + extra
	}
	obj["data"] = data
	writeJSON(w, http.StatusOK, obj)
	return true
}

// handlePlaylistBatchDetail 接管 `GET /music/api/v1/playlist/batch-detail`。
func (i *Interceptor) handlePlaylistBatchDetail(w http.ResponseWriter, r *http.Request) bool {
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
	bumpTrackCounts(asSlice(data["list"]), i.store.PlaylistTrackCounts(i.userKey(r)))
	obj["data"] = data
	writeJSON(w, http.StatusOK, obj)
	return true
}

// handlePlaylistTrackList 接管 `GET /music/api/v1/track/playlist-detail/list`。
//
// 顺序是**官方在前、在线附加在后**（对齐 fnmusic-ext）。本地曲目是歌单的
// 主体，在线附加是用户后加进来的，放后面才符合「本地优先」。
func (i *Interceptor) handlePlaylistTrackList(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	playlistGUID := playlistGUIDFrom(r, nil)
	page := atoiDefault(q.Get("page"), 1)
	// ⚠️ 用 atoiSigned：`size == -1` 是官方的「全量」约定，必须在
	// 「小于 1 就兜底」之前保留下来。
	size := atoiSigned(q.Get("size"), 50)

	// 虚拟歌单的曲目全部来自在线，**没有官方段** —— 自己应答，不打上游。
	if pl, ok := i.vPlaylistFor(playlistGUID, r); ok {
		i.writeVirtualTrackList(w, r, pl, page, size)
		return true
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

	official := asSlice(data["list"])
	officialTotal := asInt(data["total"])
	if officialTotal < len(official) {
		officialTotal = len(official)
	}

	items := i.store.PlaylistTracks(i.userKey(r), playlistGUID)
	merged := make([]any, 0, len(official)+len(items))
	merged = append(merged, official...)

	// 分页：官方段占 `[0, officialTotal)`，在线段紧随其后。
	// size == -1（全量）时不做窗口裁剪。
	if size > 0 && len(items) > 0 {
		start := (page - 1) * size
		end := start + size
		lo := start - officialTotal
		if lo < 0 {
			lo = 0
		}
		hi := end - officialTotal
		if hi > len(items) {
			hi = len(items)
		}
		if hi > lo {
			items = items[lo:hi]
		} else {
			items = nil
		}
	}

	// 歌单里的在线曲目未必被收藏过 —— isFavorite 必须照实算，写死 true 会让官方
	// 前端把它们全标成已收藏（它用 isFavorite 反推 favoriteIds）。
	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	for _, it := range items {
		i.registry.Put(it.Track)
		merged = append(merged, online.FavoriteVO(it.Track, online.Playback{}, it.AddedAt, favorites[it.Track.FakeID()], nil))
	}

	data["list"] = merged
	data["total"] = officialTotal + len(items)
	obj["data"] = data

	writeJSON(w, http.StatusOK, obj)
	return true
}

// handlePlaylistAddTrack 接管 `POST /music/api/v1/playlist/add-track`。
//
// 一个请求里可能同时有官方曲目和在线曲目。处理顺序很关键：
//  1. **官方批次先行**：官方曲目交给官方后端，官方失败就整体失败、本地一条不写。
//     反过来先写本地，会出现「官方报错了但歌单里多了几首在线歌」的鬼状态。
//  2. 官方成功后，在线条目写进本地桶。
func (i *Interceptor) handlePlaylistAddTrack(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	playlistGUID := playlistGUIDFrom(r, body)
	guids := bodyGUIDList(body, "trackGUIDs", "trackGuids", "guids", "tracks")

	// 推荐歌单是**只读**的。官方不认识这个 guid，交给官方会得到正确的拒绝；
	// 我们要做的只是绝不能把条目写进本地桶 —— 否则那些记录永远不会有界面
	// 去展示它们（歌单明天就不是那个 guid 了），变成查不出来的脏数据。
	if isVirtualPlaylistGUID(playlistGUID) {
		i.passThroughBody(w, r, raw)
		return true
	}

	var official []string
	var onlineTracks []online.Track
	for _, g := range guids {
		if t, _, ok := i.lookupTrackAny([]string{g}); ok {
			onlineTracks = append(onlineTracks, t)
			continue
		}
		official = append(official, g)
	}
	if len(onlineTracks) == 0 {
		// ⚠️ 这条日志是**排查必需**的，别删。
		//
		// 以前这里是静默透传的：于是「往歌单里加一首在线歌没反应」既没有日志也没有
		// 报错 —— 请求被原样转给官方，官方回一句 `100002 invalid arguments`
		// （官方对**它库里没有**的 track guid 就回这个，见参考实现
		// `/tmp/fnme-z/proxy/app.py:7501`），看起来像「官方不认这个歌单」，
		// 实际是**我们没认出那个 guid**。2026-10-06 真机排查时正卡在这里：
		// 同一个 guid 在 metadata 与收藏两条路径上都认得出，只有这里认不出。
		// 把**顶层键**和一段原始 body 一起打出来：`guids` 为空有两种截然不同的原因 ——
		// 「字段名对不上」和「body 压根没解析出来」，只看 guids=[] 分不清。
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		snippet := string(raw)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		i.logf("[INTERCEPT] 歌单加曲：%d 个 guid 里一个在线曲目都没认出（官方 %d 个），透传给官方；playlist=%q guids=%v bodyKeys=%v rawLen=%d raw=%s",
			len(guids), len(official), playlistGUID, guids, keys, len(raw), snippet)
		i.passThroughBody(w, r, raw)
		return true
	}
	if playlistGUID == "" {
		// 不知道往哪个歌单加，就没法写本地桶。交给官方，它会给出正确的错误。
		i.logf("[INTERCEPT] 歌单加曲：没解析出 playlist guid（在线曲目 %d 个），透传", len(onlineTracks))
		i.passThroughBody(w, r, raw)
		return true
	}

	// ① 官方批次先行。
	//
	// 必须走 upstreamJSON 而不是只看 StatusCode：官方 API 的业务错误是
	// **HTTP 200 + 非 0 的 code**（比如 160002「歌单已达上限」）。只看状态码
	// 会把这类失败当成成功，接着往本地桶里写 —— 于是出现「界面报错、歌单里
	// 却多了一首只有本机看得见的歌」这种最难排查的脏数据。
	if len(official) > 0 {
		if _, rawResp, status, ct, ok := i.upstreamJSON(r, rebuildTrackGUIDs(body, official)); !ok {
			// 官方批次失败 → 原样把官方的错误回给客户端，本地**一条都不写**。
			writeRaw(w, rawResp, status, ct)
			return true
		}
	}

	// ② 官方成功后写本地。
	now := time.Now().Unix()
	items := make([]online.PlaylistItem, 0, len(onlineTracks))
	for _, t := range onlineTracks {
		fake := i.registry.Put(t)
		items = append(items, online.PlaylistItem{GUID: fake, AddedAt: now, Track: t})
	}
	if err := i.store.AddPlaylistTracks(i.userKey(r), playlistGUID, items); err != nil {
		i.logf("[INTERCEPT] 写在线歌单条目失败 %s：%v", playlistGUID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 100001, "msg": "添加到歌单失败", "data": nil,
		})
		return true
	}

	// ③ 「加入歌单」也要自动下载并绑定本地。
	//
	// 用户指令里写的是「收藏 / **加入歌单** → 自动下载并绑定本地」—— 两条路径必须是
	// 同一个行为。以前只有收藏那条挂了钩子，往歌单里加一首在线歌什么都不会发生
	// （参考实现 fnmusic-ext 的 `_register_fav_autobind` 正是在 `:7172` 收藏与
	// `:7667` 加入歌单**两处**都调用它）。
	//
	// 位置与收藏那条同理：放在**写本地成功之后** —— 先保证记录成立，再触发后台下载。
	// `autoDownload` 自己查开关、查是否已下载、查是否已有同曲在下（关着时立刻返回），
	// 这里不用再判一次 —— 两处判空迟早会不一致。
	for _, it := range items {
		i.autoDownload(it.GUID, it.Track)
	}

	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}

// handlePlaylistRemoveTrack 接管 `POST /music/api/v1/playlist/remove-track`。
func (i *Interceptor) handlePlaylistRemoveTrack(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	playlistGUID := playlistGUIDFrom(r, body)
	guids := bodyGUIDList(body, "trackGUIDs", "trackGuids", "guids", "tracks")

	// 与 add-track 同理：推荐歌单只读，不进本地桶。
	if isVirtualPlaylistGUID(playlistGUID) {
		i.passThroughBody(w, r, raw)
		return true
	}

	var official []string
	var online []string
	for _, g := range guids {
		if _, fake, ok := i.lookupTrackAny([]string{g}); ok {
			online = append(online, fake)
			continue
		}
		official = append(official, g)
	}
	if len(online) == 0 {
		i.passThroughBody(w, r, raw)
		return true
	}

	// 官方批次先行，与 add-track 同理（业务错误也是 HTTP 200）。
	if len(official) > 0 {
		if _, rawResp, status, ct, ok := i.upstreamJSON(r, rebuildTrackGUIDs(body, official)); !ok {
			writeRaw(w, rawResp, status, ct)
			return true
		}
	}

	if playlistGUID != "" {
		if _, err := i.store.RemovePlaylistTracks(i.userKey(r), playlistGUID, online); err != nil {
			i.logf("[INTERCEPT] 删在线歌单条目失败 %s：%v", playlistGUID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"code": 100001, "msg": "从歌单移除失败", "data": nil,
			})
			return true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}

// handlePlaylistDelete 接管 `POST /music/api/v1/playlist/delete`。
//
// 删歌单要**级联清掉**这个歌单下的在线条目，否则那些条目会变成永远看不到、
// 也删不掉的孤儿数据。
func (i *Interceptor) handlePlaylistDelete(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	playlistGUID := playlistGUIDFrom(r, body)

	// 推荐歌单只读：删不掉（也不该能删掉）。交给官方会让它按「歌单不存在」
	// 拒绝，比我们自己编一个错误更准；关键是不能走到下面的 DropPlaylist。
	if isVirtualPlaylistGUID(playlistGUID) {
		i.passThroughBody(w, r, raw)
		return true
	}

	_, rawResp, status, ct, ok := i.upstreamJSON(r, raw)
	if !ok {
		writeRaw(w, rawResp, status, ct)
		return true
	}
	// 官方**业务成功**之后才清本地：宁可留下「官方没了、本地还有」的残留
	// （下次进歌单会看到几首幽灵曲目，但至少不丢数据），也不要反过来
	// 「本地删干净了、官方歌单其实还在」—— 那会让用户反复删同一首歌。
	if playlistGUID != "" {
		if err := i.store.DropPlaylist(i.userKey(r), playlistGUID); err != nil {
			i.logf("[INTERCEPT] 清在线歌单条目失败 %s：%v", playlistGUID, err)
		}
	}
	writeRaw(w, rawResp, status, ct)
	return true
}

// rebuildTrackGUIDs 复制一份 body，把 trackGUIDs 换成指定的子集。
//
// 用于「一个请求里官方曲目和在线曲目混在一起」时，把官方那部分单独转给官方
// 后端 —— 在线 guid 交给官方只会换来一个「找不到该曲目」的错误。
func rebuildTrackGUIDs(body map[string]any, guids []string) []byte {
	clone := make(map[string]any, len(body))
	for k, v := range body {
		clone[k] = v
	}
	vals := make([]any, 0, len(guids))
	for _, g := range guids {
		vals = append(vals, g)
	}
	// 三个别名都写，免得官方按另一个名字找字段时又拿到空数组。
	for _, k := range []string{"trackGUIDs", "trackGuids", "guids"} {
		if _, ok := clone[k]; ok {
			clone[k] = vals
		}
	}
	if _, ok := clone["trackGUIDs"]; !ok {
		clone["trackGUIDs"] = vals
	}
	return encodeJSON(clone)
}
