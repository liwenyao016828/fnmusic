package api

// AI 助手接口（2026-10-26 新增）。
//
// 设计要点：
//   - /api/assistant/chat 是 SSE 流：模型回复、工具执行、待确认操作都通过事件流回来；
//   - /api/assistant/capabilities 暴露能力目录（界面展示「助手能做什么」）；
//   - /api/assistant/confirm 是写入类操作的确认闸口（ConfirmWrites 模式下模型调用
//     写入类工具时，前端拿到 action 事件后必须调这里才会真正执行）；
//   - 执行器是进程内直调（Executor.Do），不经过网络——助手调 /api/* 与前端调用
//     走同一套 routeTable handler，行为完全一致。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"fn-lx-player/pkg/assistant"
)

// assistantExecutor 把助手的能力调用接到 routeTable 处理器上（进程内直调）。
//
// 为什么进程内直调而不是 HTTP 自调：助手是应用内置的，本机直调没有网络开销，
// 而且天然绕过鉴权（本地可信环境）。行为与前端调用完全一致，因为走的是同一个 handler。
type assistantExecutor struct {
	s *Server
}

func (e *assistantExecutor) Do(req *http.Request) (int, []byte) {
	w := &captureResponseWriter{ResponseWriter: noopResponseWriter{}}
	e.s.handleAssistantProxy(w, req)
	return w.status, w.body
}

// noopResponseWriter 吞掉 handler 对 ResponseWriter 的所有输出（写 body 由
// captureResponseWriter 自己记录，不需要真写出到网络）。
type noopResponseWriter struct{}

func (noopResponseWriter) Header() http.Header         { return http.Header{} }
func (noopResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (noopResponseWriter) WriteHeader(code int)        {}
func (noopResponseWriter) Flush()                      {}

// captureResponseWriter 记录 handler 写出的 status 与 body
type captureResponseWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *captureResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	w.body = append(w.body, p...)
	return w.ResponseWriter.Write(p)
}

// handleAssistantProxy 把 /api/* 请求分发到 routeTable 的处理器。
// 这是 Executor.Do 的实现：助手调 /api/catalog 就真的走 HandleCatalog。
func (s *Server) handleAssistantProxy(w http.ResponseWriter, r *http.Request) {
	for _, rt := range routeTable {
		if pathMatches(rt.Path, r.URL.Path) {
			rt.Handler(s)(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// pathMatches 判断路由表路径是否匹配请求路径（支持 {param} 模板）
func pathMatches(pattern, path string) bool {
	if pattern == path {
		return true
	}
	patterns := strings.Split(strings.Trim(pattern, "/"), "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patterns) != len(parts) {
		return false
	}
	for i, p := range patterns {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			continue
		}
		if p != parts[i] {
			return false
		}
	}
	return true
}

// assistantRuntime 懒构造助手运行时（首次请求时建好，之后复用）
func (s *Server) assistantRuntime() *assistant.Runtime {
	s.assistantMu.Lock()
	defer s.assistantMu.Unlock()
	if s.assistantRT == nil {
		// 设置落盘，重启不丢（assistant_settings.json 与 ai_config.json 同级）
		settings := assistant.NewSettingsStore(s.cfgMgr.DataDir() + "/assistant_settings.json")
		// 会话也落盘（assistant_sessions.json）：用户明确要会话记录，
		// 重开抽屉能接着聊、能翻历史
		sessions := assistant.NewFileSessionStore(s.cfgMgr.DataDir() + "/assistant_sessions.json")
		rt := assistant.NewRuntime(
			s.aiClient,
			sessions,
			settings,
			assistant.NewActionStore(),
			&assistantExecutor{s: s},
		)
		s.assistantRT = rt
	}
	return s.assistantRT
}

// HandleAssistantChat POST /api/assistant/chat —— SSE 流式对话
//
// 请求体：{ session_id, message }
// 事件流：meta → (role → text | tool | action | error) → done
func (s *Server) HandleAssistantChat(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()

	var req struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体需包含 message"})
		return
	}
	if req.SessionID == "" {
		req.SessionID = "default"
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "SSE 不支持"})
		return
	}

	emit := func(ev assistant.Event) {
		b, _ := json.Marshal(ev)
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		flusher.Flush()
	}

	_ = rt.Chat(r.Context(), req.SessionID, req.Message, emit)
}

// HandleAssistantCapabilities GET /api/assistant/capabilities —— 能力目录
func (s *Server) HandleAssistantCapabilities(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()
	settings := rt.Settings()

	caps := assistant.Catalog()
	out := make([]map[string]any, 0, len(caps))
	for _, c := range caps {
		out = append(out, map[string]any{
			"name":     c.Name,
			"method":   c.Method,
			"path":     c.Path,
			"summary":  c.Summary,
			"exposure": string(c.Exposure),
			"risk":     string(c.Risk),
			"params":   c.Params,
			"disabled": settings.Disabled[c.Name],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":         200,
		"capabilities": out,
		"settings":     settings,
	})
}

// HandleAssistantSettings GET/POST /api/assistant/settings —— 助手设置
func (s *Server) HandleAssistantSettings(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "settings": rt.Settings()})
	case http.MethodPost:
		var patch struct {
			Enabled  *bool           `json:"enabled"`
			Mode     string          `json:"mode"`
			Disabled map[string]bool `json:"disabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体格式错误"})
			return
		}
		updated := rt.UpdateSettings(assistant.SettingsPatch{
			Enabled:  patch.Enabled,
			Mode:     assistant.ExecutionMode(patch.Mode),
			Disabled: &patch.Disabled,
		})
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "settings": updated})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "方法不允许"})
	}
}

// HandleAssistantConfirm POST /api/assistant/confirm —— 用户确认写入类操作
//
// 请求体：{ action_id, approved }
// 响应：SSE 流（与 /api/assistant/chat 同格式）。确认后要真正执行并让模型
// 接着把结果说给用户听，所以这里必须流式——旧版传了空 emit，执行结果和
// 后续的助手回复全丢了，界面只看到「一片空白，也没有弹出让我确认什么」。
func (s *Server) HandleAssistantConfirm(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()

	var req struct {
		ActionID string `json:"action_id"`
		Approved bool   `json:"approved"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.ActionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体需包含 action_id"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "SSE 不支持"})
		return
	}

	emit := func(ev assistant.Event) {
		b, _ := json.Marshal(ev)
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		flusher.Flush()
	}

	err := rt.ResolveAction(r.Context(), req.ActionID, req.Approved, emit)
	if err != nil {
		emit(assistant.Event{Type: "error", Data: map[string]any{"message": err.Error()}})
		emit(assistant.Event{Type: "done"})
	}
}

// HandleAssistantSessions GET /api/assistant/sessions —— 会话列表（历史）
//
// List() 已按 UpdatedAt 新→旧排序：界面里新的在最上面。
func (s *Server) HandleAssistantSessions(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()
	sessions := rt.Sessions()
	out := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, map[string]any{
			"id":         sess.ID,
			"title":      sessionTitle(sess),
			"created_at": sess.CreatedAt.Format("2006-01-02 15:04:05"),
			"updated_at": sess.UpdatedAt.Format("2006-01-02 15:04:05"),
			"messages":   len(sess.Messages),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "sessions": out})
}

// sessionTitle 会话标题 = 第一条用户消息的首行（超长截断），没有则「新会话」。
// 界面列表只认标题不认 ID，标题必须能让人一眼认出这是哪段对话。
func sessionTitle(sess *assistant.Session) string {
	for _, m := range sess.Messages {
		if m.Role != "user" {
			continue
		}
		line := strings.TrimSpace(m.Content)
		if i := strings.IndexAny(line, "\r\n"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > 30 {
			return string(r[:30]) + "…"
		}
		return string(r)
	}
	return "新会话"
}

// HandleAssistantSession GET/POST/DELETE /api/assistant/session —— 单个会话
//
//	GET    ?id=   → 完整历史（含消息），不存在回 404
//	POST          → 新建会话，返回 {session:{id}}
//	DELETE ?id=   → 删除会话（消息与上下文一并清掉）
func (s *Server) HandleAssistantSession(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		sess, ok := rt.History(id)
		if !ok {
			errJSON(w, http.StatusNotFound, "没有这个会话")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200,
			"session": map[string]any{
				"id":         sess.ID,
				"title":      sessionTitle(&sess),
				"created_at": sess.CreatedAt.Format("2006-01-02 15:04:05"),
				"updated_at": sess.UpdatedAt.Format("2006-01-02 15:04:05"),
				"messages":   sess.Messages,
			},
		})
	case http.MethodPost:
		id := assistant.NewSessionID()
		rt.EnsureSession(id)
		writeJSON(w, http.StatusOK, map[string]any{
			"code":    200,
			"session": map[string]any{"id": id, "title": "新会话", "messages": []any{}},
		})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			errJSON(w, http.StatusBadRequest, "缺少会话 ID")
			return
		}
		rt.RemoveSession(id)
		writeJSON(w, http.StatusOK, map[string]any{"code": 200})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "方法不允许")
	}
}

// HandleAssistantClear POST /api/assistant/clear —— 清空会话
func (s *Server) HandleAssistantClear(w http.ResponseWriter, r *http.Request) {
	rt := s.assistantRuntime()
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体需包含 session_id"})
		return
	}
	rt.ClearSession(req.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

// HandleAssistantModels GET /api/assistant/models —— 推荐模型列表
func (s *Server) HandleAssistantModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200,
		"models": []map[string]string{
			{"id": "deepseek-chat", "label": "DeepSeek Chat"},
			{"id": "deepseek-reasoner", "label": "DeepSeek Reasoner（推理）"},
			{"id": "glm-4-plus", "label": "GLM-4 Plus"},
			{"id": "glm-4-flash", "label": "GLM-4 Flash"},
			{"id": "qwen-plus", "label": "Qwen Plus"},
			{"id": "qwen-turbo", "label": "Qwen Turbo"},
			{"id": "gpt-4o-mini", "label": "GPT-4o mini"},
			{"id": "gpt-4o", "label": "GPT-4o"},
		},
	})
}
