package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 「重试启动」：sidecar 因瞬时原因挂掉（OOM / 装依赖断网 / 端口被占）时，
// 界面此前只能显示「启动失败」而没有重试入口 —— 用户只能去动一个音源开关
// （顺带触发 Apply）或者重启整个应用。这个接口补上那条路。
func TestMusicDLRestartInvokesAction(t *testing.T) {
	s := &Server{}
	called := 0
	s.SetMusicDLRestart(func() { called++ })
	s.musicdlStatus = func() map[string]any { return map[string]any{"available": true} }

	w := httptest.NewRecorder()
	s.handleMusicDLRestart(w, httptest.NewRequest(http.MethodPost, "/api/musicdl/restart", nil))
	if called != 1 {
		t.Fatalf("该触发一次重启动作，实际 %d 次", called)
	}
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("应答不是 JSON：%v", err)
	}
	if int(env["code"].(float64)) != 200 {
		t.Fatalf("该回 200：%+v", env)
	}
	// 应答里带上重启后的状态，界面不用再打一次请求
	if env["data"] == nil {
		t.Fatal("该把状态一并带回来")
	}
}

// 只收 POST —— 这是个**有副作用**的动作，GET 不该能触发它
func TestMusicDLRestartRejectsGet(t *testing.T) {
	s := &Server{}
	called := 0
	s.SetMusicDLRestart(func() { called++ })
	w := httptest.NewRecorder()
	s.handleMusicDLRestart(w, httptest.NewRequest(http.MethodGet, "/api/musicdl/restart", nil))
	if called != 0 {
		t.Fatal("⚠️ GET 不该触发重启")
	}
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("该回 405，得到 %d", w.Code)
	}
}

// 没开外挂音源时不该崩，该走统一的「不可用」应答
func TestMusicDLRestartUnavailable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleMusicDLRestart(w, httptest.NewRequest(http.MethodPost, "/api/musicdl/restart", nil))
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if int(env["code"].(float64)) != 502 {
		t.Fatalf("该回 502（与另三个 handler 同一个形状）：%+v", env)
	}
}
