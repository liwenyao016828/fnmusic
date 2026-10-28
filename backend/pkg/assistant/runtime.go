package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/ai"
)

// Executor 执行一次能力请求。由 api 包注入实现：进程内直调 routeTable handler，
// 不经过网络——助手是应用内置的，本机直调既快又天然绕过鉴权（本地可信）。
type Executor interface {
	Do(req *http.Request) (status int, body []byte)
}

// Action 一条待用户确认的操作记录
type Action struct {
	ID        string    `json:"id"`
	Call      APICall   `json:"call"`
	Status    string    `json:"status"` // pending / executed / cancelled
	Result    string    `json:"result"` // 执行结果摘要（执行后填充）
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ActionStore 待确认操作仓库（内存）
type ActionStore struct {
	mu      sync.Mutex
	actions map[string]*Action
}

// NewActionStore 创建操作仓库
func NewActionStore() *ActionStore {
	return &ActionStore{actions: make(map[string]*Action)}
}

// Create 登记一条待确认操作
func (st *ActionStore) Create(sessionID string, call APICall) *Action {
	st.mu.Lock()
	defer st.mu.Unlock()
	a := &Action{
		ID:        fmt.Sprintf("act_%d", time.Now().UnixNano()),
		Call:      call,
		Status:    "pending",
		SessionID: sessionID,
		CreatedAt: time.Now(),
	}
	st.actions[a.ID] = a
	return a
}

// Get 按 ID 取操作
func (st *ActionStore) Get(id string) *Action {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.actions[id]
}

// Update 更新操作状态与结果
func (st *ActionStore) Update(id, status, result string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if a, ok := st.actions[id]; ok {
		a.Status = status
		a.Result = result
	}
}

// StaleCleanup 清理过期的 pending 操作（创建超过 1 小时的）
func (st *ActionStore) StaleCleanup() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for id, a := range st.actions {
		if a.Status == "pending" && time.Since(a.CreatedAt) > time.Hour {
			delete(st.actions, id)
		}
	}
}

// Event 流式事件（经 SSE 发给前端）
type Event struct {
	Type string         `json:"type"` // meta / role / text / tool / action / error / done
	Data map[string]any `json:"data,omitempty"`
}

// emitEvent 便捷构造
func emitEvent(emit func(Event), eventType string, data map[string]any) {
	if emit != nil {
		emit(Event{Type: eventType, Data: data})
	}
}

// Runtime 助手运行时：编排 提示词 → 工具调用 → 策略裁决 → 执行 → 流式输出
type Runtime struct {
	aiClient *ai.Client
	sessions *SessionStore
	settings *SettingsStore
	actions  *ActionStore
	executor Executor
	maxIter  int
}

// NewRuntime 创建运行时
func NewRuntime(aiClient *ai.Client, sessions *SessionStore, settings *SettingsStore, actions *ActionStore, executor Executor) *Runtime {
	return &Runtime{
		aiClient: aiClient,
		sessions: sessions,
		settings: settings,
		actions:  actions,
		executor: executor,
		maxIter:  6,
	}
}

// enabledCapabilities 过滤掉被用户关掉的能力
func enabledCapabilities(caps []*Capability, disabled map[string]bool) []*Capability {
	out := make([]*Capability, 0, len(caps))
	for _, c := range caps {
		if disabled[c.Name] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Settings 返回当前助手设置
func (rt *Runtime) Settings() Settings { return rt.settings.Get() }

// UpdateSettings 更新助手设置
func (rt *Runtime) UpdateSettings(patch SettingsPatch) Settings { return rt.settings.Update(patch) }

// Sessions 返回全部会话
func (rt *Runtime) Sessions() []*Session { return rt.sessions.List() }

// ClearSession 清空指定会话的消息
func (rt *Runtime) ClearSession(id string) { rt.sessions.Clear(id) }

// GetSession 返回单个会话（含消息历史，供界面回显）
func (rt *Runtime) GetSession(id string) *Session { return rt.sessions.Get(id) }

// EnsureSession 取会话，不存在则创建（用户点「新建会话」）
func (rt *Runtime) EnsureSession(id string) *Session { return rt.sessions.GetOrCreate(id) }

// History 返回会话的值拷贝（读取历史，不被并发写入影响）
func (rt *Runtime) History(id string) (Session, bool) { return rt.sessions.History(id) }

// RemoveSession 删除整个会话
func (rt *Runtime) RemoveSession(id string) { rt.sessions.Remove(id) }

// summarizeResult 把执行结果提炼成模型能读的一段文本
// summarizeResult 把接口响应压成模型可读的文本。
//
// filter 是给「清单类接口」用的关键词（来自 /api/catalog 的 ?filter=）：
// 非空时只展开命中的条目，见 appendArrayRows。
func summarizeResult(status int, body []byte, filter string) string {
	if status < 200 || status >= 300 {
		return fmt.Sprintf("执行失败（HTTP %d）：%s", status, truncateText(string(body), 600))
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return truncateText(string(body), 2000)
	}
	var b strings.Builder
	if env.Message != "" && env.Message != "ok" {
		b.WriteString("结果：")
		b.WriteString(env.Message)
		b.WriteString("\n")
	}
	if len(env.Data) > 0 {
		// 格式化成一行预览（模型可读且省 token）
		b.WriteString(truncateText(string(env.Data), 2000))
	}
	// 兜底：有些接口（最典型的是 /api/catalog）把内容直接铺在**顶层**，
	// 既没有 data 也没有 message —— 老逻辑会把它们提炼成空字符串，
	// 模型收到的工具结果就是一片空白，于是出现了「catalog_list 返回空」。
	if b.Len() == 0 {
		return summarizeFlat(body, filter)
	}
	return b.String()
}

// summarizeFlat 给「没有 data 信封」的响应做一份结构化摘要。
//
// 为什么不直接返回原始 JSON：/api/catalog 有 120KB，截断到 2000 字节只能看到
// 半截数组，模型既读不成也读不全 —— 那和返回空没本质区别。这里按顶层字段
// 提炼成「键: 规模」+ 数组里的条目行（METHOD path），模型据此就能判断能力面。
func summarizeFlat(body []byte, filter string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		// 不是对象（数组/纯文本）：原样截断，至少不丢内容
		return truncateText(string(body), 2000)
	}
	// 先报「是什么」，再报规模，最后列条目 —— 顺序固定，模型好读
	head := []string{"app", "version", "auth_mode", "base_url", "code", "message"}
	seen := map[string]bool{}
	var lines []string
	addScalar := func(k string) {
		raw, ok := obj[k]
		if !ok || seen[k] {
			return
		}
		seen[k] = true
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s != "" {
				lines = append(lines, fmt.Sprintf("%s: %s", k, s))
			}
			return
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			lines = append(lines, fmt.Sprintf("%s: %d", k, n))
		}
	}
	for _, k := range head {
		addScalar(k)
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := obj[k]
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			// 标题只由 appendArrayRows 打一次，别在这里也打（会重复成两行）
			lines = appendArrayRows(lines, k, arr, filter)
			continue
		}
		var sub map[string]json.RawMessage
		if err := json.Unmarshal(raw, &sub); err == nil {
			lines = append(lines, fmt.Sprintf("%s: 共 %d 个字段", k, len(sub)))
			continue
		}
		addScalar(k)
	}
	if len(lines) == 0 {
		// 空对象 / 全部字段都是空值：宁可回原始响应体，也别给模型一个空串。
		return truncateText(string(body), 2000)
	}
	return truncateText(strings.Join(lines, "\n"), 6000)
}

// toolPreview 给界面上的工具卡做一行预览：只留开头，避免把整段清单糊在卡片里。
func toolPreview(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateText(s, 200)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（已截断）"
}

// Chat 处理一次用户提问：跑完整的工具调用循环并流式输出事件。
func (rt *Runtime) Chat(ctx context.Context, sessionID, userMsg string, emit func(Event)) error {
	settings := rt.settings.Get()
	if !settings.Enabled {
		emitEvent(emit, "error", map[string]any{"message": "助手已被关闭，请到设置里重新启用。"})
		emitEvent(emit, "done", nil)
		return nil
	}

	rt.sessions.Add(sessionID, SessionMessage{Role: "user", Content: userMsg})

	caps := enabledCapabilities(Catalog(), settings.Disabled)
	system := SystemPrompt(caps)
	tools := ToolDefs(caps)

	emitEvent(emit, "meta", map[string]any{
		"session_id":   sessionID,
		"mode":         string(settings.Mode),
		"capabilities": capNames(caps),
	})

	for i := 0; i < rt.maxIter; i++ {
		msgs := ToMessages(rt.sessions.Get(sessionID).Messages)

		resp, err := rt.aiClient.ChatWithTools(ctx, "assistant", system, msgs, tools)
		if err != nil {
			emitEvent(emit, "error", map[string]any{"message": err.Error()})
			emitEvent(emit, "done", nil)
			return err
		}
		if resp == nil {
			// 未配置 AI：不是错误，但要明确告知下一步
			emitEvent(emit, "error", map[string]any{
				"message": "尚未配置 AI 大模型（设置 → 账号连接 → AI 大模型），配置完成后即可使用助手。",
			})
			emitEvent(emit, "done", nil)
			return nil
		}

		if len(resp.ToolCalls) == 0 {
			// 最终回答
			emitEvent(emit, "role", map[string]any{"role": "assistant"})
			emitEvent(emit, "text", map[string]any{"text": resp.Content})
			rt.sessions.Add(sessionID, SessionMessage{Role: "assistant", Content: resp.Content})
			emitEvent(emit, "done", nil)
			return nil
		}

		// 模型请求调用工具：先把 assistant 消息（含 tool_calls）入会话，再逐个执行
		rt.sessions.Add(sessionID, SessionMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		for _, call := range resp.ToolCalls {
			outcome := rt.executeCall(ctx, sessionID, APICall{ID: call.ID, Name: call.Function.Name, Args: parseToolArgs(call.Function.Arguments)}, settings.Mode, settings.Disabled, emit)
			msgs = append(msgs, ai.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    outcome.content,
			})
			rt.sessions.Add(sessionID, SessionMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    outcome.content,
				ActionID:   outcome.actionID,
			})
		}
	}

	emitEvent(emit, "error", map[string]any{"message": "工具调用轮次达到上限，已停止（可拆分问题后重试）。"})
	emitEvent(emit, "done", nil)
	return nil
}

type callOutcome struct {
	content  string
	actionID string
}

// executeCall 执行一次工具调用：策略裁决 → 执行/挂起 → 返回给模型的结果
func (rt *Runtime) executeCall(ctx context.Context, sessionID string, call APICall, mode ExecutionMode, disabled map[string]bool, emit func(Event)) callOutcome {
	cap := FindCapability(call.Name)
	if cap == nil {
		return callOutcome{content: fmt.Sprintf("能力 %q 不存在，可用能力：%s", call.Name, capNames(enabledCapabilities(Catalog(), disabled)))}
	}

	emitEvent(emit, "tool", map[string]any{
		"name": cap.Name, "summary": cap.Summary, "status": "executing",
	})

	decision := Decide(cap, mode, call.Confirmed, disabled)
	switch decision {
	case DecisionBlocked:
		reason := "该能力未开放给助手"
		if disabled[cap.Name] {
			reason = "该能力已被用户在助手设置中关闭"
		}
		if mode == ModeReadOnly && cap.Risk != RiskRead {
			reason = "当前是只读模式，不允许执行写入类能力"
		}
		emitEvent(emit, "tool", map[string]any{"name": cap.Name, "status": "blocked", "reason": reason})
		return callOutcome{content: reason + "。请告知用户。"}

	case DecisionConfirmationNeeded:
		action := rt.actions.Create(sessionID, call)
		// 确认卡要让人看懂「到底要做什么」，不能只甩一个能力名。
		// detail 由能力自己渲染成人话（如「下载《晴天》- 周杰伦」），
		// 能力没实现 detail 时退回 summary。
		detail := cap.Detail(call.Args)
		emitEvent(emit, "action", map[string]any{
			"id":         action.ID,
			"action_id":  action.ID,
			"name":       cap.Name,
			"summary":    cap.Summary,
			"detail":     detail,
			"args":       call.Args,
			"risk":       string(cap.Risk),
			"status":     "pending",
			"capability": cap.Name,
		})
		emitEvent(emit, "tool", map[string]any{"name": cap.Name, "status": "awaiting_confirmation", "action_id": action.ID})
		return callOutcome{
			content:  fmt.Sprintf("此操作（%s）需要用户确认，已创建确认请求（编号 %s）。请告知用户在界面上点击确认；在用户确认前不要重复发起该操作。", detail, action.ID),
			actionID: action.ID,
		}

	case DecisionAllowed:
		req, err := cap.Build(call.Args)
		if err != nil {
			emitEvent(emit, "tool", map[string]any{"name": cap.Name, "status": "error", "reason": err.Error()})
			return callOutcome{content: "参数错误：" + err.Error()}
		}
		status, body := rt.executor.Do(req)
		result := summarizeResult(status, body, req.URL.Query().Get("filter"))
		// 界面预览用短摘要；模型拿到的是完整 result（截断只影响展示，不影响推理）
		emitEvent(emit, "tool", map[string]any{"name": cap.Name, "status": "done", "result": toolPreview(result)})
		return callOutcome{content: result}
	}
	return callOutcome{content: "未知裁决结果"}
}

// ResolveAction 执行一条已确认的操作（用户点了确认卡）并把结果回填会话，
// 然后继续对话循环（流式输出）。
func (rt *Runtime) ResolveAction(ctx context.Context, actionID string, approved bool, emit func(Event)) error {
	action := rt.actions.Get(actionID)
	if action == nil {
		emitEvent(emit, "error", map[string]any{"message": "确认请求不存在或已过期。"})
		emitEvent(emit, "done", nil)
		return nil
	}

	// 一条确认请求只能被处理一次：界面上的按钮可能被连点，
	// 重复执行等于把同一个下载/整理动作做两遍。
	if st := action.Status; st != "pending" {
		msg := "这条确认请求已经处理过了。"
		if st == "executed" || st == "failed" || st == "cancelled" || st == "executing" {
			msg = map[string]string{
				"executed":  "这条操作已经执行过了。",
				"failed":    "这条操作已经执行过（失败）。",
				"cancelled": "这条操作已经被取消了。",
				"executing": "这条操作正在执行中。",
			}[st]
		}
		emitEvent(emit, "action", map[string]any{
			"id": actionID, "action_id": actionID, "status": st, "result": action.Result, "duplicate": true,
		})
		emitEvent(emit, "error", map[string]any{"message": msg})
		emitEvent(emit, "done", nil)
		return nil
	}

	if !approved {
		rt.actions.Update(actionID, "cancelled", "用户取消了操作")
		rt.sessions.Add(action.SessionID, SessionMessage{
			Role:       "tool",
			ToolCallID: action.Call.ID,
			Content:    "用户取消了该操作。",
			ActionID:   actionID,
		})
		emitEvent(emit, "action", map[string]any{"id": actionID, "status": "cancelled"})
		// 取消后也继续对话：让模型知道结果
		return rt.continueConversation(ctx, action.SessionID, emit)
	}

	rt.actions.Update(actionID, "executing", "")
	cap := FindCapability(action.Call.Name)
	if cap == nil {
		rt.actions.Update(actionID, "failed", "能力不存在")
		emitEvent(emit, "error", map[string]any{"message": fmt.Sprintf("能力 %q 不存在", action.Call.Name)})
		emitEvent(emit, "done", nil)
		return nil
	}

	call := APICall{ID: action.Call.ID, Name: action.Call.Name, Args: action.Call.Args, Confirmed: true}
	// 关键：用户已经点过确认，必须以 Confirmed=true 参与裁决，
	// 否则「确认写入」模式会再次判成需要确认，永远执行不了，且没有任何反馈。
	// 关键：用户已经点过确认，必须以 Confirmed=true 参与裁决，否则「确认写入」
	// 模式会再判一次「需要确认」，操作永远执行不了、界面上也毫无反应。
	// 但「用户关掉了这项能力」「只读模式」这类拦截依然要拦，所以裁决参数要
	// 把 Confirmed 置真而不是把结果直接放行。
	userEnabled := rt.settings.Get().Disabled
	if userEnabled == nil {
		userEnabled = map[string]bool{}
	}
	decision := Decide(cap, rt.settings.Get().Mode, true, userEnabled)
	if decision != DecisionAllowed {
		rt.actions.Update(actionID, "failed", "策略不允许")
		emitEvent(emit, "error", map[string]any{"message": "当前执行模式不允许执行该操作。"})
		emitEvent(emit, "done", nil)
		return nil
	}

	req, buildErr := cap.Build(call.Args)
	if buildErr != nil {
		rt.actions.Update(actionID, "failed", buildErr.Error())
		emitEvent(emit, "action", map[string]any{"id": actionID, "action_id": actionID, "status": "failed", "message": buildErr.Error()})
		emitEvent(emit, "error", map[string]any{"message": "参数错误：" + buildErr.Error()})
		emitEvent(emit, "done", nil)
		return nil
	}

	emitEvent(emit, "tool", map[string]any{"name": cap.Name, "status": "executing"})
	status, body := rt.executor.Do(req)
	result := summarizeResult(status, body, req.URL.Query().Get("filter"))
	ok := status >= 200 && status < 300
	if ok {
		rt.actions.Update(actionID, "executed", result)
	} else {
		rt.actions.Update(actionID, "failed", result)
	}

	emitEvent(emit, "action", map[string]any{
		"id":        actionID,
		"action_id": actionID,
		"name":      cap.Name,
		"status":    map[bool]string{true: "executed", false: "failed"}[ok],
		"result":    truncateText(result, 400),
	})
	emitEvent(emit, "tool", map[string]any{
		"name": cap.Name, "status": map[bool]string{true: "done", false: "error"}[ok],
		"result": truncateText(result, 200),
	})

	rt.sessions.Add(action.SessionID, SessionMessage{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    result,
		ActionID:   actionID,
	})
	return rt.continueConversation(ctx, action.SessionID, emit)
}

// continueConversation 把已入会话的 tool 结果喂回模型，继续输出
func (rt *Runtime) continueConversation(ctx context.Context, sessionID string, emit func(Event)) error {
	settings := rt.settings.Get()
	caps := enabledCapabilities(Catalog(), settings.Disabled)
	system := SystemPrompt(caps)
	tools := ToolDefs(caps)

	for i := 0; i < rt.maxIter; i++ {
		msgs := ToMessages(rt.sessions.Get(sessionID).Messages)
		// aiClient 为 nil 表示未配置模型：和 Chat 路径一样静默降级，
		// 不能直接解引用（曾经导致确认/取消后整个请求 panic）。
		if rt.aiClient == nil {
			emitEvent(emit, "error", map[string]any{"message": "尚未配置 AI 大模型。"})
			emitEvent(emit, "done", nil)
			return nil
		}
		resp, err := rt.aiClient.ChatWithTools(ctx, "assistant", system, msgs, tools)
		if err != nil {
			emitEvent(emit, "error", map[string]any{"message": err.Error()})
			emitEvent(emit, "done", nil)
			return err
		}
		if resp == nil {
			emitEvent(emit, "error", map[string]any{"message": "尚未配置 AI 大模型。"})
			emitEvent(emit, "done", nil)
			return nil
		}
		if len(resp.ToolCalls) == 0 {
			emitEvent(emit, "role", map[string]any{"role": "assistant"})
			emitEvent(emit, "text", map[string]any{"text": resp.Content})
			rt.sessions.Add(sessionID, SessionMessage{Role: "assistant", Content: resp.Content})
			emitEvent(emit, "done", nil)
			return nil
		}
		rt.sessions.Add(sessionID, SessionMessage{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			outcome := rt.executeCall(ctx, sessionID, APICall{ID: call.ID, Name: call.Function.Name, Args: parseToolArgs(call.Function.Arguments)}, settings.Mode, settings.Disabled, emit)
			msgs = append(msgs, ai.Message{Role: "tool", ToolCallID: call.ID, Content: outcome.content})
			rt.sessions.Add(sessionID, SessionMessage{
				Role: "tool", ToolCallID: call.ID, Content: outcome.content, ActionID: outcome.actionID,
			})
		}
	}
	emitEvent(emit, "error", map[string]any{"message": "工具调用轮次达到上限，已停止。"})
	emitEvent(emit, "done", nil)
	return nil
}

func capNames(caps []*Capability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, c.Name)
	}
	return out
}

// parseToolArgs 把模型 tool_call 的 arguments（JSON 字符串）解析成参数 map。
// 解析失败返回空 map——Build 函数会报「缺少参数」而不是 panic。
func parseToolArgs(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}
