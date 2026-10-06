package api

// 账号来源的曲目拉取（日推 / 歌单）。
//
// 与既有接口的分工：
//   - GET /api/accounts/{provider}/daily        → 平台**原始**对象（调试 / 外部程序按需自解析）
//   - GET /api/push/sources                     → **轻量**列表（只有 kind/id/name/cover/count，不含曲目）
//   - GET /api/accounts/tracks（本文件）         → **归一化后的可播放曲目**，前端可直接入列表播放/下载
//
// 归一化复用 pkg/push 的 FetchSource —— 它与「推送同步」用的是同一套拉取与字段映射逻辑，
// 避免出现「推送看到的曲目」和「推荐页看到的曲目」两处口径不一致。

import (
	"net/http"
	"strconv"
	"strings"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/push"
)

// HandleAccountTracks 拉取某平台某个来源的曲目，返回可直接播放的形状。
//
//	GET /api/accounts/tracks?provider=netease&kind=daily
//	GET /api/accounts/tracks?provider=netease&kind=playlist&id=<歌单ID>
//	GET /api/accounts/tracks?provider=qq&kind=daily&limit=50
//
// 返回的每首歌都带 `source`（wy / tx）与 `songmid`，可直接交给播放器或下载队列。
// 音质档位**不在这里查**（那需要对每首歌再搜一次，代价过高）——
// 下载时会由 /api/music/qualities 按需查询。
func (s *Server) HandleAccountTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := strings.ToLower(strings.TrimSpace(q.Get("provider")))
	kind := strings.ToLower(strings.TrimSpace(q.Get("kind")))
	id := strings.TrimSpace(q.Get("id"))

	if provider == "" {
		provider = account.ProviderNetease
	}
	if kind == "" {
		kind = push.KindDaily
	}

	// 平台代号：与 pkg/search 的 wy/tx 对齐，前端据此选音源脚本的对应接口
	platform := "wy"
	switch provider {
	case account.ProviderNetease:
		platform = "wy"
	case account.ProviderQQ:
		platform = "tx"
	default:
		errJSON(w, http.StatusBadRequest, "不支持的平台，仅支持 netease / qq")
		return
	}

	limit := 200
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
			if limit > 1000 {
				limit = 1000
			}
		}
	}

	src, err := push.FetchSource(s.accountStore, provider, kind, id)
	if err != nil {
		// 未登录 / 登录态过期 / 来源为空，都归到 502 并原样带出中文原因
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}

	songs := make([]map[string]any, 0, len(src.Tracks))
	for i, t := range src.Tracks {
		if i >= limit {
			break
		}
		// id 加平台前缀，避免不同平台同 ID 在列表里 key 冲突
		songs = append(songs, map[string]any{
			"id":       platform + "_" + t.SongID,
			"songmid":  t.SongID,
			"name":     t.Name,
			"singer":   t.Artist,
			"album":    t.Album,
			"cover":    t.Cover,
			"duration": t.Duration,
			"interval": t.Duration,
			"source":   platform,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"provider": provider,
			"kind":     kind,
			"id":       src.ID,
			"title":    src.Title,
			"cover":    src.CoverURL,
			"owner":    src.Owner,
			"platform": platform,
			"songs":    songs,
			"count":    len(songs),
		},
	})
}
