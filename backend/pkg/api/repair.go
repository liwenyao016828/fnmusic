package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"fn-lx-player/pkg/intercept"
)

// 歌曲修复（v2.35.0）的两个接口。
//
// # 为什么独立成段
//
// 它与「替换音质」长得像，但问题不同：替换音质针对**好的但档位低**的文件，
// 这里针对**坏的**文件（扩展名与内容不符、时长过短、假无损）。坏文件压根不该拿
// 「能不能升档」去判 —— 一个 30 秒的 flac 已经算「无损」了，档位比不出来。
//
// # 搜索候选为什么不再开接口
//
// 「通过搜索歌名返回各种版本让用户选」用的就是已有的 `GET /api/search`
// （多平台聚合、去噪、带 platform + songmid + 档位），前端直接调它即可。
// 这里只负责「扫坏文件」和「把一个选中的候选覆盖到坏文件上」两件事。

// SetRepair 注入歌曲修复的两个动作（由 main 提供，避免 api 包 import main）。
func (s *Server) SetRepair(
	scan func(r *http.Request, dir string) (intercept.RepairScanResult, error),
	replace func(r *http.Request, req intercept.TrackReplaceRequest) (intercept.TrackReplaceResult, error),
) {
	s.repairScan = scan
	s.repairReplace = replace
}

// handleRepairScan 扫描曲库里的坏文件。
//
// POST /api/library/repair/scan
// body: {"dir":"/vol3/1000/音乐"}（dir 省略 = 默认曲库根目录）
// data: {dir, scanned, issues:[{path,title,artist,kind,detail,container,duration_sec,size,kbps}]}
func (s *Server) handleRepairScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.repairScan == nil {
		repairUnavailable(w)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	}
	dir := strings.TrimSpace(body.Dir)
	if dir == "" {
		dir = s.libraryDirOrDefault("")
	}
	data, err := s.repairScan(r, dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": data})
}

// handleRepairReplace 用用户挑中的候选版本覆盖一个坏文件。
//
// POST /api/library/repair/replace
// body: {"path":"...", "platform":"wy", "platform_id":"186016",
//
//	"title":"夜曲", "artist":"周杰伦", "album":"...", "duration":265,
//	"cover_url":"...", "quality":"flac", "artists":["周杰伦"]}
//
// data: {path, ok, note, before:{...}, after:{...}, tier}
func (s *Server) handleRepairReplace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.repairReplace == nil {
		repairUnavailable(w)
		return
	}
	var req intercept.TrackReplaceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": "请求体不是合法 JSON"})
		return
	}
	data, err := s.repairReplace(r, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": err.Error(), "data": data})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": data})
}

func repairUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 502,
		"msg":  "修复通道没起来（应用还没就绪）",
		"data": map[string]any{"available": false},
	})
}
