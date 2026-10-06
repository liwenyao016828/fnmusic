package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 试运行（`/api/musicdl/preview`）的 HTTP 层用例。
//
// 语义的判据在 `musicdl_wiring.go` 那一层（配置/搜索/回收，见 backend 根目录的
// musicdl_preview_test.go）。这里只钉住接口这一层：方法分发、参数、错误码、
// 以及「没接线时不崩」。

func previewServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.SetMusicDLPreview(
		func(source string) (map[string]any, error) {
			if strings.TrimSpace(source) == "" {
				return nil, errors.New("source 不能为空")
			}
			return map[string]any{"active": true, "source": source, "seconds_left": 300}, nil
		},
		func() map[string]any { return map[string]any{"active": false, "ttl": 300} },
		func() map[string]any { return map[string]any{"active": false, "stopped": "mg"} },
	)
	return s
}

func TestMusicDLPreviewStartBindsToSource(t *testing.T) {
	s := previewServer(t)
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"source":"bq"}`)
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodPost, "/api/musicdl/preview", body))

	env := decodeEnv(t, w)
	if env["code"] != float64(200) {
		t.Fatalf("该回 200：%+v", env)
	}
	d, _ := env["data"].(map[string]any)
	if d["source"] != "bq" || d["active"] != true {
		t.Fatalf("该把要试运行的源传下去：%+v", d)
	}
}

func TestMusicDLPreviewStartAcceptsQuerySource(t *testing.T) {
	// body 里没给就用 ?source= —— 界面与命令行两种调用方式都能用。
	s := previewServer(t)
	w := httptest.NewRecorder()
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodPost, "/api/musicdl/preview?source=bi", nil))
	d, _ := decodeEnv(t, w)["data"].(map[string]any)
	if d["source"] != "bi" {
		t.Fatalf("该从查询串取源：%+v", d)
	}
}

func TestMusicDLPreviewStartRejectsEmptySource(t *testing.T) {
	s := previewServer(t)
	w := httptest.NewRecorder()
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodPost, "/api/musicdl/preview", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空源该回 400（而不是悄悄起一个没名字的进程），得到 %d", w.Code)
	}
	env := decodeEnv(t, w)
	if env["code"] != float64(400) || strings.TrimSpace(env["msg"].(string)) == "" {
		t.Fatalf("该说清哪里不对：%+v", env)
	}
}

func TestMusicDLPreviewStatusAndStop(t *testing.T) {
	s := previewServer(t)

	w := httptest.NewRecorder()
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodGet, "/api/musicdl/preview", nil))
	d, _ := decodeEnv(t, w)["data"].(map[string]any)
	if d["active"] != false {
		t.Fatalf("GET 该读状态：%+v", d)
	}

	w = httptest.NewRecorder()
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodDelete, "/api/musicdl/preview", nil))
	d, _ = decodeEnv(t, w)["data"].(map[string]any)
	if d["stopped"] != "mg" {
		t.Fatalf("DELETE 该手动停：%+v", d)
	}
}

func TestMusicDLPreviewRejectsUnknownMethod(t *testing.T) {
	s := previewServer(t)
	w := httptest.NewRecorder()
	s.handleMusicDLPreview(w, httptest.NewRequest(http.MethodPut, "/api/musicdl/preview", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("该回 405，得到 %d", w.Code)
	}
}

func TestMusicDLPreviewUnavailableWithoutWiring(t *testing.T) {
	// 没接线（外挂音源这套没启用）时三个方法都不该崩，走统一的「不可用」应答。
	s := &Server{}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.handleMusicDLPreview(w, httptest.NewRequest(m, "/api/musicdl/preview", strings.NewReader(`{"source":"mg"}`)))
		env := decodeEnv(t, w)
		if env["code"] != float64(502) {
			t.Fatalf("%s 该回 502（与另三个 handler 同一形状）：%+v", m, env)
		}
	}
}

func decodeEnv(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("应答不是 JSON：%v\n%s", err, w.Body.String())
	}
	return env
}
