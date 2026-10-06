package intercept

import (
	"net/http"
	"time"

	"fn-lx-player/pkg/online"
)

// handleFavoriteCreate 接管 `POST /music/api/v1/favorite-track/create`。
//
// 在线曲目没有官方数据库记录，收藏只存在我们这边。用户看到的效果与本地歌
// 完全一样（红心立刻变实），但这条记录不会被写进官方库 —— 阶段 3 的
// 「收藏自动绑定本地」会把它落成真实的本地文件，那时才回写官方。
func (i *Interceptor) handleFavoriteCreate(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	track, fake, ok := i.lookupTrackAny(bodyGUIDCandidates(body))
	if !ok {
		i.passThroughBody(w, r, raw)
		return true
	}

	item := online.FavoriteItem{
		GUID:      fake,
		CreatedAt: time.Now().Unix(),
		Track:     track,
	}
	if err := i.store.AddFavorite(i.userKey(r), item); err != nil {
		i.logf("[INTERCEPT] 写在线收藏失败 %s：%v", fake, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 100001, "msg": "收藏保存失败", "data": nil,
		})
		return true
	}
	// 收藏**成功之后**（不是之前）才触发自动下载：
	// 用户点红心是即时反馈的操作，不能等一首无损几十兆下完。
	// `autoDownload` 自己会查开关、查是否已下载、查是否已有同曲在下 —— 关着时它
	// 立刻返回，这里不用再判一次（两处判空迟早会不一致）。
	i.autoDownload(fake, track)

	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}

// handleFavoriteDelete 接管 `POST /music/api/v1/favorite-track/delete`。
func (i *Interceptor) handleFavoriteDelete(w http.ResponseWriter, r *http.Request) bool {
	raw, body := readBody(r)
	_, fake, ok := i.lookupTrackAny(bodyGUIDCandidates(body))
	if !ok {
		i.passThroughBody(w, r, raw)
		return true
	}

	removed, err := i.store.RemoveFavorite(i.userKey(r), fake)
	if err != nil {
		i.logf("[INTERCEPT] 删在线收藏失败 %s：%v", fake, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 100001, "msg": "取消收藏失败", "data": nil,
		})
		return true
	}
	// 本来就不在收藏里也算成功：客户端的心愿是「它不该是红的」，
	// 返回错误只会让界面回滚成红的。
	_ = removed
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": nil})
	return true
}

// handleFavoriteList 接管 `GET /music/api/v1/favorite-track/list`。
//
// 顺序是**官方在前、在线在后**（对齐 fnmusic-ext）—— 用户自己的本地收藏
// 排在前面，在线收藏追加在后面，与「本地优先」的产品语义一致。
func (i *Interceptor) handleFavoriteList(w http.ResponseWriter, r *http.Request) bool {
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
	items := i.store.Favorites(i.userKey(r))

	merged := make([]any, 0, len(official)+len(items))
	merged = append(merged, official...)
	for _, it := range items {
		fake := i.registry.Put(it.Track)
		merged = append(merged, online.FavoriteVO(it.Track, online.Playback{}, it.CreatedAt, true, nil))
		_ = fake
	}

	data["list"] = merged
	data["total"] = asInt(data["total"]) + len(items)
	obj["data"] = data

	writeJSON(w, http.StatusOK, obj)
	return true
}
