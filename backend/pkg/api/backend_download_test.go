package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/backenddl"
	"fn-lx-player/pkg/online"
)

// 接口层的测试只钉契约与失败口径：参数校验、逐条 skip、落盘失败必须 500、
// 未知动作 400。真正的队列行为在 pkg/backenddl 里测。

func backendTestServer(t *testing.T, cfg backenddl.FetcherConfig) *Server {
	t.Helper()
	fetch := func(ctx context.Context, it backenddl.Item) (backenddl.Result, error) {
		return backenddl.Result{Path: "/music/test.flac", Quality: "flac"}, nil
	}
	q := backenddl.New(t.TempDir(), fetch, t.Logf)
	t.Cleanup(q.Close)
	s := &Server{}
	s.EnableBackendDownload(q, cfg)
	return s
}

func backendTestRequest(t *testing.T, h http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/download/backend/test", nil)
	} else {
		r = httptest.NewRequest(method, "/api/download/backend/test", bytes.NewBufferString(body))
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

type backendTestEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) backendTestEnvelope {
	t.Helper()
	var env backendTestEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是 JSON：%v body=%s", err, w.Body.String())
	}
	return env
}

func TestBackendDownloadStatusWithoutQueue(t *testing.T) {
	s := &Server{}
	w := backendTestRequest(t, s.HandleBackendDownloadStatus, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("没有队列时 status 也应 200，得到 %d", w.Code)
	}
	var data struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	env := decodeEnvelope(t, w)
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Ready || data.Reason == "" {
		t.Fatalf("没有队列时必须 ready=false 且写明原因：%+v", data)
	}
}

func TestBackendDownloadQueueAndActionsWithoutQueue(t *testing.T) {
	s := &Server{}
	if w := backendTestRequest(t, s.HandleBackendDownloadQueue, http.MethodGet, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("没有队列时 queue 应 503，得到 %d", w.Code)
	}
	if w := backendTestRequest(t, s.HandleBackendDownloadActions, http.MethodPost, `{"action":"retry"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("没有队列时 actions 应 503，得到 %d", w.Code)
	}
	if w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost, `{"items":[{"name":"x"}]}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("没有队列时 submit 应 503，得到 %d", w.Code)
	}
}

func TestBackendDownloadSubmitValidation(t *testing.T) {
	cfg := backenddl.FetcherConfig{Pool: online.NewPool(), Channels: func() []string { return []string{"native"} }}
	s := backendTestServer(t, cfg)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"非 JSON", `{`, http.StatusBadRequest},
		{"空 items", `{"items":[]}`, http.StatusBadRequest},
		{"超 500 条", `{"items":[` + strings.Repeat(`{"name":"x"},`, 500) + `{"name":"x"}]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost, tc.body); w.Code != tc.want {
				t.Fatalf("得到 %d，想要 %d：%s", w.Code, tc.want, w.Body.String())
			}
		})
	}

	if w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodGet, ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET submit 应 405，得到 %d", w.Code)
	}
}

func TestBackendDownloadSubmitAcceptsAndSkipsByReason(t *testing.T) {
	cfg := backenddl.FetcherConfig{Pool: online.NewPool(), Channels: func() []string { return []string{"native"} }}
	s := backendTestServer(t, cfg)
	body := `{"items":[
		{"client_id":"c1","name":"夜曲","singer":"周杰伦","platform":"wy","platform_id":"1"},
		{"client_id":"c2","name":""},
		{"client_id":"c3","name":"没有平台id的冷门歌"}
	]}`
	w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost, body)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var data struct {
		Added   []backenddl.Submitted `json:"added"`
		Skipped []backenddl.Skipped   `json:"skipped"`
		Stats   backenddl.Stats       `json:"stats"`
	}
	env := decodeEnvelope(t, w)
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Added) != 1 || data.Added[0].ClientID != "c1" || data.Added[0].ID == "" {
		t.Fatalf("有效条目没有被收下：%+v", data.Added)
	}
	if len(data.Skipped) != 2 {
		t.Fatalf("应逐条 skip 2 条，得到 %+v", data.Skipped)
	}
	byClient := map[string]string{}
	for _, sk := range data.Skipped {
		byClient[sk.ClientID] = sk.Reason
	}
	if byClient["c2"] != backenddl.SkipInvalid {
		t.Fatalf("缺歌名应为 invalid，得到 %q", byClient["c2"])
	}
	if byClient["c3"] != backenddl.SkipNoChannel {
		t.Fatalf("没有可用渠道应为 no_channel，得到 %q", byClient["c3"])
	}
	if data.Stats.Pending != 1 || data.Stats.Total != 1 {
		t.Fatalf("stats 不对：%+v", data.Stats)
	}

	// 同一个 client_id 重提：回原 id（existing=true），不再新建。
	w = backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost,
		`{"items":[{"client_id":"c1","name":"夜曲","singer":"周杰伦","platform":"wy","platform_id":"1"}]}`)
	env = decodeEnvelope(t, w)
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Added) != 1 || !data.Added[0].Existing || data.Added[0].ID == "" {
		t.Fatalf("重复提交应回原任务确认：%+v", data.Added)
	}
	if data.Stats.Total != 1 {
		t.Fatalf("重复提交建了第二条：%+v", data.Stats)
	}
}

// 落盘失败必须 500 + 明确原因：前端要据此保留本地队列并稍后重提。
func TestBackendDownloadSubmitDiskFailureIs500(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := func(ctx context.Context, it backenddl.Item) (backenddl.Result, error) {
		return backenddl.Result{}, nil
	}
	q := backenddl.New(blocker, fetch, t.Logf)
	s := &Server{}
	s.EnableBackendDownload(q, backenddl.FetcherConfig{Pool: online.NewPool(), Channels: func() []string { return []string{"native"} }})

	w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost,
		`{"items":[{"name":"夜曲","platform":"wy","platform_id":"1"}]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("写盘失败应 500，得到 %d：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "落盘") {
		t.Fatalf("500 响应应说明未能落盘：%s", w.Body.String())
	}
	if n := len(q.List()); n != 0 {
		t.Fatalf("写盘失败后队列没有回滚，剩 %d 条", n)
	}
}

func TestBackendDownloadActionsUnknownAndMethod(t *testing.T) {
	s := backendTestServer(t, backenddl.FetcherConfig{Pool: online.NewPool(), Channels: func() []string { return []string{"native"} }})
	if w := backendTestRequest(t, s.HandleBackendDownloadActions, http.MethodGet, ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET actions 应 405，得到 %d", w.Code)
	}
	if w := backendTestRequest(t, s.HandleBackendDownloadActions, http.MethodPost, `{"action":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("未知动作应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if w := backendTestRequest(t, s.HandleBackendDownloadActions, http.MethodPost, `{"action":"pause"}`); w.Code != http.StatusOK {
		t.Fatalf("pause 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
}

func TestBackendDownloadQueueShape(t *testing.T) {
	cfg := backenddl.FetcherConfig{Pool: online.NewPool(), Channels: func() []string { return []string{"native"} }}
	s := backendTestServer(t, cfg)
	if w := backendTestRequest(t, s.HandleBackendDownloadSubmit, http.MethodPost,
		`{"items":[{"name":"夜曲","platform":"wy","platform_id":"1"}]}`); w.Code != http.StatusOK {
		t.Fatalf("提交失败：%s", w.Body.String())
	}
	w := backendTestRequest(t, s.HandleBackendDownloadQueue, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("queue 应 200，得到 %d", w.Code)
	}
	var data struct {
		Items  []backenddl.Item `json:"items"`
		Stats  backenddl.Stats  `json:"stats"`
		Paused bool             `json:"paused"`
	}
	env := decodeEnvelope(t, w)
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 1 || data.Items[0].Name != "夜曲" || data.Stats.Pending != 1 {
		t.Fatalf("queue 返回不对：%+v", data)
	}
}
