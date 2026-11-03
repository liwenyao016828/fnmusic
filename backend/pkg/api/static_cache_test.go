package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"fn-lx-player/pkg/config"
)

// ── 静态资源缓存头（2026-11-03）──
//
// 用户报「应用图标有时能显示、有时不能」。根因之一是：除了 index.html 与 assets/，
// 其余静态文件（favicon.png / logo.png …）**一个缓存头都不发** ——
// 于是每个浏览器/WebView 各自决定缓存多久，行为完全不可预期。
//
// index.html 必须始终 no-store（换版本要立刻生效），图标类给短缓存（配合 ?v=）。

func newStaticServer(t *testing.T) *Server {
	t.Helper()
	mgr, err := config.NewConfigManager(t.TempDir(), 8898)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html></html>")},
		"favicon.png":          &fstest.MapFile{Data: []byte("\x89PNG\r\n\x1a\n")},
		"assets/app-abc123.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"robots.txt":           &fstest.MapFile{Data: []byte("User-agent: *")},
	}
	return NewServer(mgr, nil, fsys)
}

// 鉴权在测试里统一关掉：这一组测的是静态缓存头与 SPA 回落，
// 与真实部署的 FN_AUTH_MODE 无关（AuthModeNone 时整条鉴权链直接放行）。
func TestStaticCacheHeaders(t *testing.T) {
	t.Setenv("FN_AUTH_MODE", "none")
	h := newStaticServer(t).Router()

	cases := []struct{ path, want string }{
		{"/", "no-cache, no-store, must-revalidate"},
		{"/index.html", "no-cache, no-store, must-revalidate"},
		{"/favicon.png", "public, max-age=86400"},
		{"/assets/app-abc123.js", "public, max-age=31536000, immutable"},
		{"/robots.txt", "no-cache"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}

// 未知的前端路径回落 index.html（SPA），且同样 no-store。
func TestStaticFallbackUnknownPathIsIndex(t *testing.T) {
	t.Setenv("FN_AUTH_MODE", "none")
	rec := httptest.NewRecorder()
	newStaticServer(t).Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/whatever", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("SPA fallback 应当回 200，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("SPA fallback 应当回 index.html，得到 %q", rec.Body.String())
	}
}

// 未知 /api/ 路径不能回落 HTML（调用方会误以为成功）。
func TestUnknownAPIPathIsJSON404(t *testing.T) {
	t.Setenv("FN_AUTH_MODE", "none")
	rec := httptest.NewRecorder()
	newStaticServer(t).Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope/nothing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 API 应当 404，得到 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("未知 API 应当回 JSON，得到 %q", ct)
	}
}
