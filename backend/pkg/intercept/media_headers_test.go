package intercept

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"fn-lx-player/pkg/online"
)

// TestApplyMediaHeaders 锁住 M1 的核心行为：探活与取流必须用同一份头。
// 脚本给了 Referer/User-Agent 就优先用，没给就回落到平台默认 Referer + 统一 UA。
func TestApplyMediaHeaders(t *testing.T) {
	track := online.Track{Platform: "wy"}

	t.Run("脚本头优先", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://cdn.example.com/a.flac", nil)
		res := &online.Resolved{Headers: map[string]string{
			"referer":    "https://custom.example/",
			"user-agent": "CustomUA/9.9",
		}}
		applyMediaHeaders(req, track, "bytes=0-99", res)
		if got := req.Header.Get("Referer"); got != "https://custom.example/" {
			t.Fatalf("Referer 应取脚本给的值，得到 %q", got)
		}
		if got := req.Header.Get("User-Agent"); got != "CustomUA/9.9" {
			t.Fatalf("User-Agent 应取脚本给的值，得到 %q", got)
		}
		if got := req.Header.Get("Range"); got != "bytes=0-99" {
			t.Fatalf("Range 应原样带上，得到 %q", got)
		}
		if got := req.Header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("Accept-Encoding 应为 identity，得到 %q", got)
		}
	})

	t.Run("缺省回落平台默认", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://cdn.example.com/a.flac", nil)
		applyMediaHeaders(req, track, "", nil)
		if got := req.Header.Get("Referer"); got != mediaReferer("wy") {
			t.Fatalf("Referer 应回落平台默认 %q，得到 %q", mediaReferer("wy"), got)
		}
		if got := req.Header.Get("User-Agent"); got != online.DesktopUA {
			t.Fatalf("User-Agent 应回落统一 UA %q，得到 %q", online.DesktopUA, got)
		}
		if _, ok := req.Header["Range"]; ok {
			t.Fatalf("空 Range 不该写进请求头")
		}
	})

	t.Run("只有一项头时另一项回落", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://cdn.example.com/a.flac", nil)
		res := &online.Resolved{Headers: map[string]string{"referer": "https://only-ref.example/"}}
		applyMediaHeaders(req, track, "", res)
		if got := req.Header.Get("Referer"); got != "https://only-ref.example/" {
			t.Fatalf("Referer 应取脚本值，得到 %q", got)
		}
		if got := req.Header.Get("User-Agent"); got != online.DesktopUA {
			t.Fatalf("脚本没给 UA 时应回落统一 UA，得到 %q", got)
		}
	})
}
