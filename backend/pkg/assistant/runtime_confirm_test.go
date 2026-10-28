package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeExecutor 记录被执行的请求，按预设返回。
type fakeExecutor struct {
	status int
	body   []byte
	calls  []*http.Request
}

func (f *fakeExecutor) Do(req *http.Request) (int, []byte) {
	// net/http 的请求体是流，读一次就没了；后面还要检查请求内容，所以读出来回填。
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}
	f.calls = append(f.calls, req)
	if f.status == 0 {
		return 200, []byte(`{"code":200,"message":"ok","data":{"queued":1}}`)
	}
	return f.status, f.body
}

func newTestRuntime(exec Executor) *Runtime {
	return NewRuntime(nil, NewSessionStore(), NewSettingsStore(""), NewActionStore(), exec)
}

func collectEvents() (func(Event), *[]Event) {
	var got []Event
	return func(ev Event) { got = append(got, ev) }, &got
}

// pendingDownload 造一条等待确认的下载操作（下载类是按需确认能力）。
func pendingDownload(t *testing.T, rt *Runtime) string {
	t.Helper()
	emit, got := collectEvents()
	out := rt.executeCall(context.Background(), "s1",
		APICall{ID: "call_1", Name: "download_song", Args: map[string]any{"name": "晴天", "singer": "周杰伦"}},
		ModeConfirmWrites, nil, emit)
	if out.actionID == "" {
		t.Fatalf("应产生待确认操作，实际事件：%v", eventTypes(*got))
	}
	return out.actionID
}

// terminalStatus 取 action 事件里的终态（executed / failed / cancelled）
func terminalStatus(evs []Event) (status, result string) {
	for _, ev := range evs {
		if ev.Type != "action" {
			continue
		}
		switch s, _ := ev.Data["status"].(string); s {
		case "executed", "failed", "cancelled":
			status = s
			result, _ = ev.Data["result"].(string)
		}
	}
	return
}

// 确认卡必须能被前端挂上按钮：action 事件的 id 与 action_id 都要有，
// 且 tool 事件的 awaiting_confirmation 要带 action_id。
// 捕获的 bug：action 事件只给 id、tool 事件给 action_id，前端按 action_id
// 找卡片找不到 → 用户点不了确认。
func TestExecuteCallConfirmationEmitsActionID(t *testing.T) {
	rt := newTestRuntime(&fakeExecutor{})
	emit, got := collectEvents()

	out := rt.executeCall(context.Background(), "s1",
		APICall{ID: "call_1", Name: "download_song", Args: map[string]any{"name": "晴天", "singer": "周杰伦"}},
		ModeConfirmWrites, nil, emit)

	if out.actionID == "" {
		t.Fatal("确认类能力应返回 actionID")
	}
	var actEvt, toolEvt *Event
	for i := range *got {
		switch (*got)[i].Type {
		case "action":
			actEvt = &(*got)[i]
		case "tool":
			if (*got)[i].Data["status"] == "awaiting_confirmation" {
				toolEvt = &(*got)[i]
			}
		}
	}
	if actEvt == nil {
		t.Fatal("应发出 action 事件")
	}
	if actEvt.Data["action_id"] != out.actionID {
		t.Errorf("action 事件缺少 action_id：%v", actEvt.Data)
	}
	if actEvt.Data["id"] != out.actionID {
		t.Errorf("action 事件的 id 不匹配：%v", actEvt.Data)
	}
	detail, _ := actEvt.Data["detail"].(string)
	if !strings.Contains(detail, "晴天") {
		t.Errorf("确认卡应显示要做什么（含歌名），实际 %q", detail)
	}
	if toolEvt == nil || toolEvt.Data["action_id"] != out.actionID {
		t.Errorf("tool 事件应带 action_id")
	}
}

// 用户点确认后：必须真的执行，并且把结果通过事件流回去。
// 捕获的 bug：ResolveAction 当时传了空 emit，执行结果和后续回复全部丢失。
func TestResolveActionEmitsResult(t *testing.T) {
	exec := &fakeExecutor{}
	rt := newTestRuntime(exec)

	id := pendingDownload(t, rt)
	if len(exec.calls) != 0 {
		t.Fatal("确认前不应执行")
	}

	emit2, got2 := collectEvents()
	_ = rt.ResolveAction(context.Background(), id, true, emit2)

	if len(exec.calls) != 1 {
		t.Fatalf("确认后应执行一次，实际 %d 次；事件：%v", len(exec.calls), eventTypes(*got2))
	}
	status, result := terminalStatus(*got2)
	if status != "executed" {
		t.Errorf("应发出 executed 结果事件，实际 status=%q events=%v", status, eventTypes(*got2))
	}
	if !strings.Contains(result, "queued") {
		t.Errorf("结果事件应带执行结果，实际 %q", result)
	}
	// 未配置模型时也要正常收尾（不能 panic、不能挂住）
	if last := (*got2)[len(*got2)-1]; last.Type != "done" {
		t.Errorf("事件流应以 done 收尾，实际 %q", last.Type)
	}

	if got := rt.actions.Get(id); got == nil || got.Status != "executed" {
		t.Errorf("操作记录状态应为 executed")
	}
}

// 执行失败要如实标 failed，而不是假装完成。
func TestResolveActionFailureStatus(t *testing.T) {
	exec := &fakeExecutor{status: 500, body: []byte(`{"error":"内部错误"}`)}
	rt := newTestRuntime(exec)

	id := pendingDownload(t, rt)
	emit2, got2 := collectEvents()
	_ = rt.ResolveAction(context.Background(), id, true, emit2)

	status, _ := terminalStatus(*got2)
	if status != "failed" {
		t.Errorf("HTTP 500 应标 failed，实际 %q events=%v", status, eventTypes(*got2))
	}
	if len(exec.calls) != 1 {
		t.Fatalf("应执行一次，实际 %d", len(exec.calls))
	}
}

// 拒绝时不执行，并发出 cancelled。
func TestResolveActionRejectDoesNotExecute(t *testing.T) {
	exec := &fakeExecutor{}
	rt := newTestRuntime(exec)

	id := pendingDownload(t, rt)
	emit2, got2 := collectEvents()
	_ = rt.ResolveAction(context.Background(), id, false, emit2)

	if len(exec.calls) != 0 {
		t.Fatal("拒绝后不应执行")
	}
	status, _ := terminalStatus(*got2)
	if status != "cancelled" {
		t.Errorf("应发出 cancelled 事件，实际 %v", eventTypes(*got2))
	}
	if got := rt.actions.Get(id); got == nil || got.Status != "cancelled" {
		t.Errorf("操作记录状态应为 cancelled")
	}
}

// 同一个确认请求不能被重复执行（防双击）。
func TestResolveActionTwiceOnlyExecutesOnce(t *testing.T) {
	exec := &fakeExecutor{}
	rt := newTestRuntime(exec)

	id := pendingDownload(t, rt)
	emit2, _ := collectEvents()
	_ = rt.ResolveAction(context.Background(), id, true, emit2)
	emit3, got3 := collectEvents()
	_ = rt.ResolveAction(context.Background(), id, true, emit3)

	if len(exec.calls) != 1 {
		t.Fatalf("重复确认应只执行一次，实际 %d 次", len(exec.calls))
	}
	// 第二次只回放历史结果（duplicate 标记），不能当成新的一次执行
	var dup bool
	for _, ev := range *got3 {
		if ev.Type == "action" && ev.Data["duplicate"] == true {
			dup = true
		}
	}
	if !dup {
		t.Errorf("第二次确认应标记 duplicate：%v", eventTypes(*got3))
	}
}

// 每个确认类能力都要能自己说清「要做什么」，否则确认卡只能显示能力名。
func TestConfirmCapabilitiesHaveDetail(t *testing.T) {
	cases := map[string]map[string]any{
		"download_song":         {"name": "晴天", "singer": "周杰伦"},
		"tidy_tracks":           {"paths": []any{"/a.mp3", "/b.mp3"}},
		"toggle_feature_switch": {"key": "fav_auto_download", "value": true},
		"ai_complete":           {"filename": "周杰伦 - 晴天.mp3"},
	}
	for name, args := range cases {
		cap := FindCapability(name)
		if cap == nil {
			t.Fatalf("能力 %s 不存在", name)
		}
		if cap.DetailFn == nil {
			t.Errorf("%s 应有 DetailFn（确认卡要显示人话）", name)
			continue
		}
		d := cap.Detail(args)
		if d == "" || d == cap.Summary {
			t.Errorf("%s 的 detail 应具体化，实际 %q", name, d)
		}
	}
}

func eventTypes(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		b, _ := json.Marshal(e.Data)
		out = append(out, e.Type+":"+string(b))
	}
	return out
}
