package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 解析桥的 HTTP 处理器
type Handler struct {
	mgr *Manager
}

func NewHandler(mgr *Manager) *Handler {
	return &Handler{mgr: mgr}
}

func writeJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// HandleResolve POST /api/bridge/resolve
// 外部调用者提交解析请求，返回 job_id 供后续轮询。
func (h *Handler) HandleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}

	var req ResolveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "invalid payload: " + err.Error()})
		return
	}

	job, err := h.mgr.Create(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"job_id":     job.ID,
			"status":     job.Status,
			"expires_at": job.ExpiresAt,
			"poll_url":   "/api/bridge/jobs/" + job.ID,
			"hint":       "请保持曲率 页面开着以完成音源解析",
		},
	})
}

// HandlePending GET /api/bridge/pending
// 供网页端轮询待处理任务（不改变任务状态）。
func (h *Handler) HandlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}
	jobs := h.mgr.Pending()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":  200,
		"data":  map[string]interface{}{"items": jobs, "total": len(jobs)},
		"items": jobs,
	})
}

// HandleClaim POST /api/bridge/claim  body: {"job_id":"..."}
func (h *Handler) HandleClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}
	var req struct {
		JobID string `json:"job_id"`
		ID    string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "invalid payload"})
		return
	}
	id := req.JobID
	if id == "" {
		id = req.ID
	}
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "job_id required"})
		return
	}

	job, err := h.mgr.Claim(id)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{"code": 409, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": job})
}

// HandleResult POST /api/bridge/result
func (h *Handler) HandleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}
	var req struct {
		JobID string `json:"job_id"`
		ID    string `json:"id"`
		ResultInput
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "invalid payload"})
		return
	}
	id := req.JobID
	if id == "" {
		id = req.ID
	}
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "job_id required"})
		return
	}

	job, err := h.mgr.Complete(id, req.ResultInput)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{"code": 409, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": job})
}

// HandleJob GET /api/bridge/jobs/{job_id}
func (h *Handler) HandleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}

	id := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		// 从 /api/bridge/jobs/{id} 路径中解析
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		for i, p := range parts {
			if p == "jobs" && i+1 < len(parts) {
				id = parts[i+1]
				break
			}
		}
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"code": 400, "message": "job_id required"})
		return
	}

	job, ok := h.mgr.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"code": 404, "message": "任务不存在或已被清理"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": job})
}

// HandleStats GET /api/bridge/stats
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": h.mgr.Stats()})
}

// StatsSnapshot 返回队列概况（供健康检查等复用）
func (h *Handler) StatsSnapshot() map[string]int {
	return h.mgr.Stats()
}
