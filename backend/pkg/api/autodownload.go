package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"fn-lx-player/pkg/online"
)

// 「自动下载记录」：由**飞牛音乐页面里的操作**（收藏 / 加入歌单 / 点「下载到曲率」）
// 触发的那批下载的流水。
//
// # 为什么读接口在这里、写却在拦截层
//
// 这些下载全部发生在官方页面上，曲率前端看不见它们 —— 只有拦截层是当事方
// （见 pkg/online/autolog.go 顶部那段）。所以拦截层负责**写**，这里只负责
// **读**与**清**：一个数据、一个写入方，避免两处各写一半。
//
// # 「没有记录」与「功能没起来」必须分开
//
// 构造失败（数据目录不可写）时记录功能会降级。这时候返回一个空列表会让人
// 以为「确实还没下过歌」，而真相是「这个功能这次没起来」—— 两者在界面上
// 长得一模一样。所以响应里带一个 `available` 字段，界面据此说清是哪一种。

// SetAutoLog 注入自动下载记录表。
//
// 为什么是 setter 而不是构造参数：记录表由**拦截层**持有（它才是写入方），
// 而拦截层在 api.NewServer 之前就建好了 —— 与 SetMusicDL / SetRecommendLLM
// 同一套做法（见 main.go 里的说明）。
func (s *Server) SetAutoLog(l *online.AutoLog) { s.autoLog = l }

// handleAutoDownloadRecords 读自动下载记录（`GET /api/auto-download/records`）。
//
// 同一路径的 DELETE 走 clearAutoDownloadRecords —— 路由表里同一条路径同时
// 注册了 GET 与 DELETE（见 routes.go），所以这里按方法分派。
func (s *Server) handleAutoDownloadRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.clearAutoDownloadRecords(w, r)
		return
	}

	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	if s.autoLog == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"list": []interface{}{}, "total": 0, "failed": 0, "available": false,
			},
		})
		return
	}

	total, failed := s.autoLog.Stats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"list": s.autoLog.List(limit), "total": total, "failed": failed, "available": true,
		},
	})
}

// clearAutoDownloadRecords 清除记录（`DELETE /api/auto-download/records`）。
//
// body 带 `{"ids":[...]}` 时只清那几条；不带（或空数组）时**全清**。
// 全清是用户明确点的「一键清除」，所以不需要二次确认的接口语义 —— 界面负责问。
func (s *Server) clearAutoDownloadRecords(w http.ResponseWriter, r *http.Request) {
	if s.autoLog == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"code": 503, "message": "自动下载记录未启用（数据目录不可写）",
		})
		return
	}

	var body struct {
		IDs []string `json:"ids"`
	}
	// body 为空是合法的（= 全清），所以解码失败不当错误。
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)

	removed, err := s.autoLog.Remove(body.IDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"code": 500, "message": "清除失败：" + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"removed": removed},
	})
}
