package api

import (
	"encoding/json"
	"net/http"
)

// HandleDownloadQueue 读写网页端的下载队列（落盘到 NAS，不再只存浏览器）。
//
//	GET  /api/download/queue  → {"code":200,"data":{"tasks":[...]}}
//	PUT  /api/download/queue  body {"tasks":[...]} → 整体替换
//
// ⚠️ **整体替换**语义：调用方（前端 `downloadManager`）必须回传完整队列。
// 队列的状态机归前端所有，后端只负责存 —— 所以这里连字段都不解析，
// 直接按不透明的 JSON 数组收下来（见 pkg/downloadqueue 的包注释）。
//
// 上限 4MB：一条任务约几百字节，够放几千条；给个上限是为了别让
// 一个畸形请求把整个数据目录写爆。
func (s *Server) HandleDownloadQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{"tasks": s.downloadQueue.All()},
		})

	case http.MethodPut, http.MethodPost:
		var body struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}
		if err := s.downloadQueue.Replace(body.Tasks); err != nil {
			errJSON(w, http.StatusInternalServerError, "保存下载队列失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{"count": len(body.Tasks)},
		})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
