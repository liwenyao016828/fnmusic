package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/appupdate"
)

// 这一组测试固定「接口契约」本身：字段名、code 包装、状态机取值。
// 逻辑细节（版本比较/下载校验）由 pkg/appupdate 自己的测试负责。

func newUpdateTestServer(t *testing.T) (*Server, *httptest.Server, []byte) {
	t.Helper()
	payload := []byte(strings.Repeat("F", 4096))
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tag_name":"v9.9.9","body":"notes","published_at":"2026-10-24T01:02:03Z",
			"html_url":"https://example.com/r",
			"assets":[{"name":"y.fpk","browser_download_url":"%s/fpk","size":4096}]}`, srv.URL)
	})
	mux.HandleFunc("/fpk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	m := appupdate.New(appupdate.Options{
		DataDir:        t.TempDir(),
		AppName:        "yinshu-ai",
		CurrentVersion: "1.0.0",
		Repo:           "o/r",
		APIURL:         srv.URL + "/release",
		Client:         srv.Client(),
		ScriptDir:      t.TempDir(),
		IsRoot:         func() bool { return false },
		SudoAvailable:  func() bool { return false },
	})
	return &Server{appUpdate: m}, srv, payload
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v\n%s", err, rec.Body.String())
	}
	if body.Code != 200 {
		t.Fatalf("code=%d body=%s", body.Code, rec.Body.String())
	}
	return body.Data
}

func TestAppUpdateStateStartsIdle(t *testing.T) {
	s, _, _ := newUpdateTestServer(t)
	rec := httptest.NewRecorder()
	s.HandleAppUpdateState(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/state", nil))
	if data := decodeData(t, rec); data["status"] != appupdate.StatusIdle {
		t.Fatalf("初始状态应为 idle，实际 %v", data)
	}
}

func TestAppUpdateCheckContract(t *testing.T) {
	s, _, _ := newUpdateTestServer(t)
	rec := httptest.NewRecorder()
	s.HandleAppUpdateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	data := decodeData(t, rec)
	for _, key := range []string{"current", "latest", "has_update", "notes", "published_at", "asset_name", "asset_url", "size", "html_url", "checked_at", "error"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data 缺少字段 %q：%v", key, data)
		}
	}
	if data["has_update"] != true || data["latest"] != "9.9.9" {
		t.Fatalf("更新判断不对：%v", data)
	}
}

func TestAppUpdateDownloadAndStateContract(t *testing.T) {
	s, _, payload := newUpdateTestServer(t)
	// 先检查，才有 asset
	rec := httptest.NewRecorder()
	s.HandleAppUpdateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/check", nil))

	rec = httptest.NewRecorder()
	s.HandleAppUpdateDownload(rec, httptest.NewRequest(http.MethodPost, "/api/app/update/download", nil))
	data := decodeData(t, rec)
	if data["status"] != appupdate.StatusDownloading {
		t.Fatalf("下载应立即返回 downloading：%v", data)
	}

	deadline := time.Now().Add(5 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		s.HandleAppUpdateState(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/state", nil))
		st = decodeData(t, rec)
		if st["status"] == appupdate.StatusReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st["status"] != appupdate.StatusReady {
		t.Fatalf("等下载完成超时：%v", st)
	}
	if st["size"].(float64) != float64(len(payload)) || st["progress"].(float64) != 100 {
		t.Fatalf("大小/进度不对：%v", st)
	}
	if _, ok := st["path"].(string); !ok || st["path"] == "" {
		t.Fatalf("path 缺失：%v", st)
	}
}

func TestAppUpdateInstallWithoutPermissionReturns403(t *testing.T) {
	s, _, _ := newUpdateTestServer(t)
	// 造一个 ready 状态：直接走下载再等，保证状态机一致
	rec := httptest.NewRecorder()
	s.HandleAppUpdateCheck(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/check", nil))
	rec = httptest.NewRecorder()
	s.HandleAppUpdateDownload(rec, httptest.NewRequest(http.MethodPost, "/api/app/update/download", nil))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.appUpdate.State().Status != appupdate.StatusReady {
		time.Sleep(10 * time.Millisecond)
	}

	rec = httptest.NewRecorder()
	s.HandleAppUpdateInstall(rec, httptest.NewRequest(http.MethodPost, "/api/app/update/install", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无权限应返回 403，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "应用中心") {
		t.Fatalf("提示应引导去应用中心：%s", rec.Body.String())
	}
}

func TestAppUpdateMethodNotAllowed(t *testing.T) {
	s, _, _ := newUpdateTestServer(t)
	rec := httptest.NewRecorder()
	s.HandleAppUpdateInstall(rec, httptest.NewRequest(http.MethodGet, "/api/app/update/install", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET install 应 405，实际 %d", rec.Code)
	}
}

// CurrentAppName 与更新模块的默认包名必须一致（否则真机上会去卸载另一个应用）。
func TestCurrentAppNameMatchesUpdateDefault(t *testing.T) {
	if CurrentAppName != appupdate.DefaultAppName {
		t.Fatalf("CurrentAppName=%q 与 appupdate.DefaultAppName=%q 不一致", CurrentAppName, appupdate.DefaultAppName)
	}
}
