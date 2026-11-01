package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// gatewayPrefix 必须与 fpk-package/app/ui/config 里的 gatewayPrefix 一致。
// 这条断言就是为了防止「改了 ui/config 忘了改后端」—— 那种情况下真机表现是
// 页面打开一片 404，而本地单测全绿，排查成本极高。
const uiConfigGatewayPrefix = "/app/yinshu-ai"

func TestGatewayPrefixMatchesUIConfig(t *testing.T) {
	if gatewayPrefix != uiConfigGatewayPrefix {
		t.Fatalf("gatewayPrefix 与 app/ui/config 不一致：后端=%q，ui/config=%q", gatewayPrefix, uiConfigGatewayPrefix)
	}
}

func TestStripGatewayPrefixRewritesPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/app/yinshu-ai/api/health", "/api/health"},
		{"/app/yinshu-ai/assets/index-abc.js", "/assets/index-abc.js"},
		{"/app/yinshu-ai", "/"},
		{"/app/yinshu-ai/", "/"},
		// 不带的原样放行（端口直连、本机自调用、dev）
		{"/api/health", "/api/health"},
		{"/", "/"},
		// 别的应用的前缀不该被本应用剥掉
		{"/app/miyin/api/health", "/app/miyin/api/health"},
		// 前缀只是「像」但不是同一段（/app/yinshu-aiX 不是本应用）
		{"/app/yinshu-aiX/api/health", "/app/yinshu-aiX/api/health"},
	}
	for _, c := range cases {
		var got string
		h := stripGatewayPrefix(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, c.in, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got != c.want {
			t.Errorf("stripGatewayPrefix(%q) → %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestStripGatewayPrefixKeepsQuery(t *testing.T) {
	var gotPath, gotQuery string
	h := stripGatewayPrefix(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
	}))
	req := httptest.NewRequest(http.MethodGet, "/app/yinshu-ai/api/nas/browse?path=%2Fvol1&x=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if gotPath != "/api/nas/browse" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "path=%2Fvol1&x=1" {
		t.Fatalf("query = %q（剥前缀不能吃掉查询串）", gotQuery)
	}
}
