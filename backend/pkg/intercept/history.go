package intercept

import (
	"net/http"
	"time"

	"fn-lx-player/pkg/online"
)

// eventTypeIsPlay 判断一个上报事件是不是「播放了一首歌」。
//
// 官方前端在不同版本里用了两种写法（`track_play` / `TrackPlay`），都要认。
func eventTypeIsPlay(s string) bool {
	switch s {
	case "track_play", "TrackPlay", "TRACK_PLAY", "trackPlay":
		return true
	}
	return false
}

// handleHistoryList 接管 `GET /music/api/v1/play-history/list`。
//
// 顺序是**在线在前、官方在后**（对齐 fnmusic-ext）。这跟收藏/歌单是反的，
// 看起来不一致，但符合各自的语义：收藏和歌单是「我的资产」，本地为主体；
// 播放历史是「最近发生了什么」，时间近的在上面 —— 我们记的在线播放是刚刚
// 发生的，自然排在最前。
func (i *Interceptor) handleHistoryList(w http.ResponseWriter, r *http.Request) bool {
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
	items := i.store.History(i.userKey(r))

	merged := make([]any, 0, len(official)+len(items))
	// 听过的曲子未必收藏过 —— 照实算 isFavorite（官方前端用它反推 favoriteIds）。
	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	for _, it := range items {
		i.registry.Put(it.Track)
		merged = append(merged, online.FavoriteVO(it.Track, online.Playback{}, it.PlayedAt, favorites[it.Track.FakeID()], nil))
	}
	merged = append(merged, official...)

	data["list"] = merged
	data["total"] = asInt(data["total"]) + len(items)
	obj["data"] = data

	writeJSON(w, http.StatusOK, obj)
	return true
}

// handleHistoryDelete 接管 `POST|DELETE /music/api/v1/play-history/delete`。
//
// 官方这个端点同时收 POST 和 DELETE，且 guid 可能在 body 也可能在 query。
// 全都要认 —— 少认一种，用户在 App 上删一条历史就会「删了又回来」。
func (i *Interceptor) handleHistoryDelete(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)

	candidates := bodyGUIDCandidates(body)
	candidates = append(candidates, guidCandidates(r, "")...)

	var onlineFakes []string
	var official []string
	seen := make(map[string]bool, len(candidates))
	for _, g := range candidates {
		if seen[g] {
			continue
		}
		seen[g] = true
		if _, fake, ok := i.lookupTrackAny([]string{g}); ok {
			onlineFakes = append(onlineFakes, fake)
			continue
		}
		official = append(official, g)
	}
	if len(onlineFakes) == 0 {
		i.passThroughBody(w, r, raw)
		return true
	}

	if len(official) > 0 {
		resp, err := i.forward(r, rebuildTrackGUIDs(body, official))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"code": 502, "msg": "upstream unavailable", "data": nil,
			})
			return true
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			copyResponse(w, resp)
			return true
		}
	}

	if _, err := i.store.RemoveHistory(i.userKey(r), onlineFakes); err != nil {
		i.logf("[INTERCEPT] 删在线播放历史失败：%v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 100001, "msg": "删除播放记录失败", "data": nil,
		})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}

// handleEventReport 接管 `POST /music/api/v1/event/report`。
//
// 这是官方客户端**唯一**会上报「用户播放了某首歌」的地方，所以在线曲目的
// 播放历史只能从这里记。
//
// 在线曲目不转发给官方：官方的数据库里没有这条曲目，转发过去只会换来一个
// 「找不到该曲目」的错误，然后把客户端的播放统计链路弄脏。
func (i *Interceptor) handleEventReport(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	if body == nil {
		i.passThroughBody(w, r, raw)
		return true
	}

	eventType := firstString(body, "eventType", "event_type", "type")
	if !eventTypeIsPlay(eventType) {
		i.passThroughBody(w, r, raw)
		return true
	}

	track, fake, ok := i.lookupTrackAny(bodyGUIDCandidates(body))
	if !ok {
		i.passThroughBody(w, r, raw)
		return true
	}

	item := online.HistoryItem{
		GUID:     fake,
		PlayedAt: time.Now().Unix(),
		Track:    track,
	}
	if err := i.store.RecordPlay(i.userKey(r), item); err != nil {
		// 记不上历史不影响播放本身，别把错误抛给客户端 —— 它已经在播了。
		i.logf("[INTERCEPT] 记在线播放历史失败 %s：%v", fake, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}
