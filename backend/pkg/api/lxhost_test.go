package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── /api/lx/host 的响应形状 ───────────────────────────────────────────────
//
// 这组用例钉的是**界面能不能看懂**，不是后端逻辑。2026-11-02 真机故障：
// 前端按 `data.enabled` 决定开关显示「开启宿主」还是「已开启 · 点此关闭」，
// 而这个接口当时不回 enabled —— 后端已经 ready，按钮却永远写「开启宿主」，
// 用户点了看不出任何变化，报「开启宿主，点击没反应」。
//
// 所以要看两件事：① 开关态字段必须**明确是布尔**（不是缺省、不是字符串）；
// ② 动作接口回的数据要和查询接口同一形状（前端会直接拿它填一次界面）。

func lxHostGet(t *testing.T, status func() map[string]any) map[string]any {
	t.Helper()
	s := &Server{}
	s.SetLxHost(status, func(bool, int) error { return nil })
	w := httptest.NewRecorder()
	s.handleLxHost(w, httptest.NewRequest(http.MethodGet, "/api/lx/host", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 该是 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, w.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应里没有 data 对象：%s", w.Body.String())
	}
	return data
}

// 开关态必须是布尔，且原样透传 status 给的 enabled / running / state。
func TestLxHostGetPassesSwitchStateThrough(t *testing.T) {
	data := lxHostGet(t, func() map[string]any {
		return map[string]any{"state": "ready", "enabled": true, "running": true}
	})
	if data["enabled"] != true {
		t.Fatalf("enabled 必须原样透传（界面开关全靠它）：%+v", data)
	}
	if data["running"] != true {
		t.Fatalf("running 必须原样透传：%+v", data)
	}
	if data["state"] != "ready" {
		t.Fatalf("state 必须原样透传：%+v", data)
	}
	if data["available"] != true {
		t.Fatalf("接线了就必须是 available：%+v", data)
	}
}

// 「开着但起不来」：enabled=true、running=false、state=failed —— 三样要能同时表达。
func TestLxHostGetDistinguishesFailedFromOff(t *testing.T) {
	data := lxHostGet(t, func() map[string]any {
		return map[string]any{"state": "failed", "enabled": true, "running": false, "error": "端口被占"}
	})
	if data["enabled"] != true || data["running"] != false {
		t.Fatalf("起不来时该是 enabled=true running=false：%+v", data)
	}
	if data["error"] == nil || data["error"] == "" {
		t.Fatalf("failed 必须带原因：%+v", data)
	}
}

// 没接线（旧后端）时必须明确 available=false —— 界面据此退回只读展示，
// 不能让人以为「开关存在但点了没用」。
func TestLxHostGetWithoutWiringIsUnavailable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleLxHost(w, httptest.NewRequest(http.MethodGet, "/api/lx/host", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if body.Data["available"] != false {
		t.Fatalf("没接线时 available 必须是 false：%+v", body.Data)
	}
	if body.Data["enabled"] != false {
		t.Fatalf("没接线时 enabled 必须是 false（不能默认成开着）：%+v", body.Data)
	}
}

// POST 成功时回的 data 必须和 GET 同一形状（前端拿它先填一次界面）。
func TestLxHostPostReturnsSameShape(t *testing.T) {
	enabled := true
	var applied bool
	s := &Server{}
	s.SetLxHost(
		func() map[string]any {
			return map[string]any{"state": "ready", "enabled": enabled, "running": enabled}
		},
		func(e bool, _ int) error { applied = true; enabled = e; return nil },
	)
	w := httptest.NewRecorder()
	s.handleLxHost(w, httptest.NewRequest(http.MethodPost, "/api/lx/host",
		bytes.NewBufferString(`{"enabled":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("POST 该是 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var body struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if body.Code != 200 {
		t.Fatalf("code 该是 200：%+v", body)
	}
	if !applied {
		t.Fatal("POST 必须真的调到 apply")
	}
	if body.Data["enabled"] != true || body.Data["running"] != true {
		t.Fatalf("POST 回的 data 要和 GET 同形状：%+v", body.Data)
	}
}
