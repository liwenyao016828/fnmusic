package api

// 推送任务接口：把账号的歌单/日推定时同步到飞牛音乐歌单。
//
// 与 POST /api/fnos/push（单次、曲目由调用方给出）的区别：
// 这里是「订阅」模型 —— 只需指定来源（平台 + 歌单/日推）与目标飞牛歌单，
// 后端自己拉取曲目、匹配、写入，并可定时重复执行。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/push"
)

// pushSources 列出可作为来源的账号歌单（含「每日推荐」）。
// 两个平台返回的字段名不同，这里统一成 {kind,id,name,cover_url,count}。
func (s *Server) pushSources(provider string) ([]map[string]any, error) {
	out := []map[string]any{
		{"kind": push.KindDaily, "id": "", "name": "每日推荐", "cover_url": "", "count": 0},
	}

	switch provider {
	case account.ProviderNetease:
		cookie := s.accountStore.Cookie(account.ProviderNetease)
		if cookie == "" {
			return nil, errors.New("尚未连接网易云账号，请先扫码登录")
		}
		playlists, err := account.NeteaseUserPlaylists(cookie, "", 200)
		if err != nil {
			return nil, err
		}
		for _, pl := range playlists {
			// 空 id 的行点了只会得到「需提供 source_id」的坏请求，源头拦掉
			id := neteaseStr(pl["id"])
			if id == "" {
				continue
			}
			out = append(out, map[string]any{
				"kind":      push.KindPlaylist,
				"id":        id,
				"name":      neteaseStr(pl["name"]),
				"cover_url": strings.Replace(neteaseStr(pl["coverImgUrl"]), "http://", "https://", 1),
				"count":     neteaseInt(pl["trackCount"]),
			})
		}

	case account.ProviderQQ:
		cookies := account.ParseQQCookies(s.accountStore.Cookie(account.ProviderQQ))
		if len(cookies) == 0 {
			return nil, errors.New("尚未连接 QQ 音乐账号，请先扫码登录")
		}
		playlists, err := account.QQPlaylists(cookies)
		if err != nil {
			return nil, err
		}
		for _, pl := range playlists {
			id := neteaseStr(pl["id"])
			if id == "" {
				continue
			}
			out = append(out, map[string]any{
				"kind":      push.KindPlaylist,
				"id":        id,
				"name":      neteaseStr(pl["name"]),
				"cover_url": neteaseStr(pl["cover_url"]),
				"count":     neteaseInt(pl["count"]),
				// QQ 额外标注自建/收藏
				"source_group": neteaseStr(pl["kind"]),
			})
		}

	default:
		return nil, fmt.Errorf("不支持的平台: %q（可用：netease / qq）", provider)
	}
	return out, nil
}

// pushPreview 干跑：拉取来源 + 匹配，但不写入飞牛歌单。
func (s *Server) pushPreview(task push.Task) (*push.Result, error) {
	src, err := push.FetchSource(s.accountStore, task.Provider, task.SourceKind, task.SourceID)
	if err != nil {
		return nil, err
	}
	m := fnos.NewMusic("")
	if !m.Available() {
		return nil, errors.New("飞牛音乐不可用：未找到可用登录令牌或音乐服务未运行")
	}
	target := strings.TrimSpace(task.TargetTitle)
	if target == "" {
		target = src.Title
	}
	return push.PushTracks(m, src.Tracks, push.Options{
		Title:     target,
		FnosGUID:  task.FnosGUID,
		CoverURL:  src.CoverURL,
		SyncCover: false, // 预览不写封面
		DryRun:    true,
	})
}

// neteaseStr / neteaseInt 读取任意平台的字符串/数字字段（字段名不固定）。
func neteaseStr(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		if s == float64(int64(s)) {
			return strconv.FormatInt(int64(s), 10)
		}
		return fmt.Sprintf("%v", s)
	case int:
		return strconv.Itoa(s)
	}
	return ""
}

func neteaseInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if out, err := strconv.Atoi(n); err == nil {
			return out
		}
	}
	return 0
}

// HandlePushSources 列出可作为来源的账号歌单（含日推）
//
// GET /api/push/sources?provider=netease|qq
//
// 便于调用方挑选要同步的歌单 ID。
func (s *Server) HandlePushSources(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		errJSON(w, http.StatusBadRequest, "请提供 provider（netease / qq）")
		return
	}

	sources, err := s.pushSources(provider)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"provider": provider, "sources": sources, "count": len(sources),
			"hint": "把返回的 kind 与 id 填入 POST /api/push/tasks 即可建立同步任务。",
		},
	})
}

// HandlePushTasks 列出 / 新建 / 更新 / 删除推送任务
//
// GET    /api/push/tasks                列出全部任务
// POST   /api/push/tasks                新建或更新（带 id 则更新）
// DELETE /api/push/tasks?id=xxx         删除
func (s *Server) HandlePushTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"tasks": s.pushStore.List(),
				"hint": "interval_minutes>0 且 enabled=true 的任务会定时执行；" +
					"用 POST /api/push/run?id=xxx 可立即执行一次。",
			},
		})

	case http.MethodPost:
		var req struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Provider        string `json:"provider"`
			SourceKind      string `json:"source_kind"`
			SourceID        string `json:"source_id"`
			TargetTitle     string `json:"target_title"`
			FnosGUID        string `json:"fnos_guid"`
			SyncCover       bool   `json:"sync_cover"`
			IntervalMinutes int    `json:"interval_minutes"`
			Enabled         *bool  `json:"enabled"`
			// 保留期策略：keep（默认，永久保留）/ days（超过 retention_days 天自动移除）
			RetentionMode string `json:"retention_mode"`
			RetentionDays int    `json:"retention_days"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}

		task := push.Task{
			ID:              strings.TrimSpace(req.ID),
			Name:            strings.TrimSpace(req.Name),
			Provider:        strings.TrimSpace(req.Provider),
			SourceKind:      strings.TrimSpace(req.SourceKind),
			SourceID:        strings.TrimSpace(req.SourceID),
			TargetTitle:     strings.TrimSpace(req.TargetTitle),
			FnosGUID:        strings.TrimSpace(req.FnosGUID),
			SyncCover:       req.SyncCover,
			IntervalMinutes: req.IntervalMinutes,
			RetentionMode:   push.NormalizeRetentionMode(strings.TrimSpace(req.RetentionMode)),
			RetentionDays:   req.RetentionDays,
		}
		// 更新时保留原有开关状态，未显式传 enabled 则默认启用
		if task.ID != "" {
			if old, ok := s.pushStore.Get(task.ID); ok {
				task.Enabled = old.Enabled
				if task.FnosGUID == "" {
					task.FnosGUID = old.FnosGUID
				}
				// ⚠️ PushedTracks 是运行时累积的状态，请求体里没有 ——
				// 必须从旧任务继承，否则一改配置就把推送清单清空，保留期从此失效。
				task.PushedTracks = old.PushedTracks
			}
		} else {
			task.Enabled = true
		}
		if req.Enabled != nil {
			task.Enabled = *req.Enabled
		}

		saved, err := s.pushStore.Save(task)
		if err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok", "data": saved,
		})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			errJSON(w, http.StatusBadRequest, "请提供 id")
			return
		}
		if !s.pushStore.Delete(id) {
			errJSON(w, http.StatusNotFound, "任务不存在: "+id)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{"deleted": id},
		})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// HandlePushRun 立即执行一个推送任务
//
// POST /api/push/run?id=xxx
func (s *Server) HandlePushRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		errJSON(w, http.StatusBadRequest, "请提供 id")
		return
	}
	task, ok := s.pushStore.Get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "任务不存在: "+id)
		return
	}

	// 「停止」之后不该还能被触发推一次。
	// 定时调度那侧本来就有 Enabled 判定（`push.DueTasks`），只有这个手动入口漏了 ——
	// 前端定时器、外部程序都能调到这里，于是「点了停止一会儿又开始推送」。
	if !task.Enabled {
		errJSON(w, http.StatusConflict, "该订阅已停止同步，请先恢复更新再执行")
		return
	}

	res, err := s.pushRunner.Execute(task, "manual")
	if err != nil {
		// 执行失败也已写入历史，这里把原因一并返回便于排查
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": res,
	})
}

// HandlePushPreview 干跑预览：拉取来源并匹配，但不写入飞牛歌单
//
// POST /api/push/preview
//
// body 二选一：
//
//	{"task_id":"push_xxx"}                      已有任务，按其配置预览
//	{"provider":"netease","source_kind":"daily"} 临时配置预览
func (s *Server) HandlePushPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		TaskID      string `json:"task_id"`
		Provider    string `json:"provider"`
		SourceKind  string `json:"source_kind"`
		SourceID    string `json:"source_id"`
		TargetTitle string `json:"target_title"`
		FnosGUID    string `json:"fnos_guid"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	task := push.Task{
		Provider: req.Provider, SourceKind: req.SourceKind,
		SourceID: req.SourceID, TargetTitle: req.TargetTitle, FnosGUID: req.FnosGUID,
	}
	if id := strings.TrimSpace(req.TaskID); id != "" {
		existing, ok := s.pushStore.Get(id)
		if !ok {
			errJSON(w, http.StatusNotFound, "任务不存在: "+id)
			return
		}
		task = existing
	}
	if task.Provider == "" || task.SourceKind == "" {
		errJSON(w, http.StatusBadRequest, "请提供 task_id，或 provider + source_kind")
		return
	}

	// 预览不写入歌单，因此把 DryRun 打开
	task.IntervalMinutes = 0
	res, err := s.pushPreview(task)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": res,
	})
}

// HandlePushRuns 查询执行历史
//
// GET /api/push/runs?task_id=xxx&limit=50
func (s *Server) HandlePushRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs := s.pushStore.Runs(strings.TrimSpace(r.URL.Query().Get("task_id")), limit)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"runs": runs, "count": len(runs)},
	})
}
