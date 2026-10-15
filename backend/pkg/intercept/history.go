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

// ── 上报体的**真实**形态（2026-10-07 从真机前端 bundle 读到） ──────────────
//
// ⚠️ 这里原先只认「扁平体」：`{"eventType":"track_play","trackGUID":"…"}`。
// 真机前端**根本不是这么发的** —— 那是照想象摆的夹具（HANDOVER §4.x 点名的坑），
// 代价是：拦截层认不出任何一条播放上报，`play_history/<user>.json` 从来没被写过，
// 于是「在线听过的歌进最近播放」在真机上**一次都没生效过**（真机取证：
// `/vol1/@appdata/yinshu-ai/data/online/play_history/` 下没有任何文件，
// 而同目录的 `downloaded.json` 有内容 —— 说明别的链路是活的，只有这条死了）。
//
// 真身来自 NAS 上官方页面的 bundle（`/music/static/assets/<hash>.js`）里的构造代码：
//
//	Cb = e => ({ eventType: Bi.TrackPlay, occurredAt: e.startedAt,
//	             payload: { trackGUID: e.trackId } })
//	wb = e => ({ events: [Cb(e)] })
//
// 即：**事件装在 `events` 数组里，guid 藏在 `payload.trackGUID` 里**。
// 同一个 bundle 的枚举把取值钉死了：`e.TrackPlay=\`track_play\``（所以
// eventTypeIsPlay 的判据本身没错，错的只是**取字段的位置**）。
//
// 还有一条必须一起认的路径：页面卸载时官方会用 `navigator.sendBeacon` 把**同一个
// 形状**补发一次（bundle 里的 `flushTrackPlayed`）。那条请求的 Content-Type 是
// blob，走的也是这个端点 —— 不认它就会丢掉「用户听完就关页面」那批记录。
//
// 因此：嵌套体是**真身**，扁平体只是**兼容**（老用例与更早的客户端形态）。
// 两种都认，取字段时两处都看。

// playEvent 是一条上报事件里我们关心的两件事：它是不是「播放」、它指向哪些 guid。
type playEvent struct {
	isPlay bool
	guids  []string
	// at 是事件自带的**发生时间**（秒，0 = 事件没带或不可信）。
	at int64
}

// parsePlayEvents 把上报体拆成事件列表，嵌套体与扁平体都认。
//
// 返回空 = 这个 body 里没有我们能理解的事件（调用方应原样透传）。
func parsePlayEvents(body map[string]any) []playEvent {
	if body == nil {
		return nil
	}
	if raws := asSlice(body["events"]); len(raws) > 0 {
		out := make([]playEvent, 0, len(raws))
		for _, raw := range raws {
			m := asMap(raw)
			if m == nil {
				continue
			}
			guids := bodyGUIDCandidates(m)
			// 真机形态：guid 在 payload 里（`payload.trackGUID`）。
			// 事件层与 payload 层都收 —— 官方以后把 guid 提上来一层也不用改。
			if p := asMap(m["payload"]); p != nil {
				guids = append(guids, bodyGUIDCandidates(p)...)
			}
			out = append(out, playEvent{
				isPlay: eventTypeIsPlay(firstString(m, "eventType", "event_type", "type")),
				guids:  guids,
				at:     eventOccurredAt(m),
			})
		}
		return out
	}
	// 兼容：扁平体。
	return []playEvent{{
		isPlay: eventTypeIsPlay(firstString(body, "eventType", "event_type", "type")),
		guids:  bodyGUIDCandidates(body),
		at:     eventOccurredAt(body),
	}}
}

// eventOccurredAt 取事件自带的 `occurredAt`，换算成**秒**；拿不到或明显不合理时返回 0。
//
// 为什么不能一律用「收到时间」：官方在页面卸载时会用 sendBeacon 补发，那条请求可能
// 比事件本身晚到很久；用收到时间会把刚听完的那首歌排到列表更靠后的位置。
//
// `occurredAt` 实测是**毫秒**（`occurredAt: e.startedAt`，与 `Date.now()` 同源）。
// 判据用「大于 1e11 就当毫秒」—— 秒级时间戳要到公元 5138 年才会到这个量级。
func eventOccurredAt(m map[string]any) int64 {
	if m == nil {
		return 0
	}
	v := asInt(m["occurredAt"])
	if v <= 0 {
		v = asInt(m["occurred_at"])
	}
	if v <= 0 {
		return 0
	}
	if v > 1e11 {
		v /= 1000
	}
	// 明显是未来时间（客户端时钟飘了）就不采信，交给调用方用当前时间。
	if int64(v) > time.Now().Unix()+3600 {
		return 0
	}
	return int64(v)
}

// handleEventReport 接管 `POST /music/api/v1/event/report`。
//
// 这是官方客户端**唯一**会上报「用户播放了某首歌」的地方，所以在线曲目的
// 播放历史只能从这里记。
//
// 在线曲目不转发给官方：官方的数据库里没有这条曲目，转发过去只会换来一个
// 「找不到该曲目」的错误，然后把客户端的播放统计链路弄脏。
// 一条都没认出在线曲目时**原样透传** —— 本地曲目的播放统计照旧归官方。
func (i *Interceptor) handleEventReport(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	if body == nil {
		i.passThroughBody(w, r, raw)
		return true
	}

	recorded := 0
	for _, ev := range parsePlayEvents(body) {
		if !ev.isPlay {
			continue
		}
		track, fake, ok := i.lookupTrackAny(ev.guids)
		if !ok {
			continue // 本地曲目 / 与在线登记表无关的 guid
		}
		at := ev.at
		if at <= 0 {
			at = time.Now().Unix()
		}
		item := online.HistoryItem{GUID: fake, PlayedAt: at, Track: track}
		if err := i.store.RecordPlay(i.userKey(r), item); err != nil {
			// 记不上历史不影响播放本身，别把错误抛给客户端 —— 它已经在播了。
			i.logf("[INTERCEPT] 记在线播放历史失败 %s：%v", fake, err)
			continue
		}
		recorded++
	}
	if recorded == 0 {
		i.passThroughBody(w, r, raw)
		return true
	}
	// 真机客户端一次只发一条事件（`events:[Cb(e)]`），所以「至少认出一条」等价于
	// 「这条请求就是关于在线曲目的」——直接应答，不往上送。
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}
