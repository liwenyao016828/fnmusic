package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/bridge"
)

// ── 鉴权模式解析 ──

func TestResolveAuthMode(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		token string
		want  string
	}{
		{"默认无 Token → lan", "", "", AuthModeLAN},
		{"默认有 Token → token", "", "s3cret", AuthModeToken},
		{"显式 none 即使有 Token 也放开", "none", "s3cret", AuthModeNone},
		{"显式 lan", "lan", "", AuthModeLAN},
		{"显式 token 无凭据也按 token（fail-closed）", "token", "", AuthModeToken},
		{"大小写与空格容错", "  NONE  ", "s3cret", AuthModeNone},
		{"auto 关键字等同未设", "auto", "", AuthModeLAN},
		{"无法识别的取值按默认，不静默放开", "yolo", "", AuthModeLAN},
		{"无法识别的取值 + 有 Token → token", "yolo", "s3cret", AuthModeToken},
	}
	for _, c := range cases {
		if got := ResolveAuthMode(c.env, c.token); got != c.want {
			t.Errorf("%s: ResolveAuthMode(%q,%q) = %q, want %q", c.name, c.env, c.token, got, c.want)
		}
	}
}

// ── 来源地址信任判定 ──

func TestTrustedPeerClassification(t *testing.T) {
	trusted := []string{
		"127.0.0.1", "::1", "192.168.1.30", "10.0.0.5", "172.20.3.4",
		"169.254.10.20", "fe80::1", "fd7a:115c:a1e0::1", // Tailscale IPv6
		"100.101.102.103", // carrier-grade NAT / Tailscale IPv4
		"", "not-an-ip",   // 无对端信息：进程内调用，按可信处理
	}
	for _, ip := range trusted {
		if !isTrustedPeer(ip, nil) {
			t.Errorf("isTrustedPeer(%q) = false, 期望 true", ip)
		}
	}

	untrusted := []string{"8.8.8.8", "1.1.1.1", "203.0.113.9", "172.32.0.1", "2001:4860:4860::8888"}
	for _, ip := range untrusted {
		if isTrustedPeer(ip, nil) {
			t.Errorf("isTrustedPeer(%q) = true, 期望 false", ip)
		}
	}
}

func TestParseTrustedCIDRs(t *testing.T) {
	nets := ParseTrustedCIDRs(" 203.0.113.0/24 , 198.51.100.7 ,, 不合法 , fd00:1234::/32 ")
	if len(nets) != 3 {
		t.Fatalf("解析出 %d 个网段，期望 3（非法项应跳过）", len(nets))
	}
	if !isTrustedPeer("203.0.113.9", nets) {
		t.Error("203.0.113.9 应落在 203.0.113.0/24 内")
	}
	if !isTrustedPeer("198.51.100.7", nets) {
		t.Error("单个 IP 应被当成 /32")
	}
	if isTrustedPeer("198.51.100.8", nets) {
		t.Error("198.51.100.8 不在 198.51.100.7/32 内")
	}
	if !isTrustedPeer("fd00:1234::abcd", nets) {
		t.Error("fd00:1234::abcd 应落在 fd00:1234::/32 内")
	}
	if len(ParseTrustedCIDRs("")) != 0 {
		t.Error("空环境变量应解析出 0 个网段")
	}
}

func TestIsLoopbackIP(t *testing.T) {
	if !isLoopbackIP("127.0.0.1") || !isLoopbackIP("::1") {
		t.Error("回环地址应被识别")
	}
	if isLoopbackIP("192.168.1.1") || isLoopbackIP("") || isLoopbackIP("nope") {
		t.Error("非回环地址不应被识别")
	}
}

// ── 中间件行为 ──

func authTestHandler(mode, token, cidrs string) http.Handler {
	s := &Server{authMode: mode, apiToken: strings.TrimSpace(token)}
	if cidrs != "" {
		s.trustedCIDRs = ParseTrustedCIDRs(cidrs)
	}
	return s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
}

func doAuth(h http.Handler, method, path, remote string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestAuthMiddlewareLanModeAllowsPrivateRejectsPublic(t *testing.T) {
	h := authTestHandler(AuthModeLAN, "", "")

	for _, remote := range []string{"127.0.0.1:5555", "192.168.1.230:41234", "[fd7a:115c:a1e0::1]:9000"} {
		if rec := doAuth(h, http.MethodGet, "/api/library/gaps", remote, nil); rec.Code != http.StatusOK {
			t.Errorf("内网来源 %s 应放行，得到 %d", remote, rec.Code)
		}
	}

	rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:41234", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("公网来源应被拒，得到 %d", rec.Code)
	}
	body := decodeBody(t, rec)
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "FN_API_TOKEN") || !strings.Contains(msg, "203.0.113.9") {
		t.Errorf("拒绝原因应说明来源与怎么放行，实际: %q", msg)
	}
	if body["peer_ip"] != "203.0.113.9" {
		t.Errorf("peer_ip = %v, 期望 203.0.113.9", body["peer_ip"])
	}
	if body["auth_mode"] != AuthModeLAN {
		t.Errorf("auth_mode = %v, 期望 lan", body["auth_mode"])
	}
}

func TestAuthMiddlewarePublicPathsAndPreflightBypassLan(t *testing.T) {
	h := authTestHandler(AuthModeLAN, "", "")
	for _, p := range []string{"/api/health", "/api/catalog", "/api/app/version"} {
		if rec := doAuth(h, http.MethodGet, p, "203.0.113.9:1", nil); rec.Code != http.StatusOK {
			t.Errorf("公开路径 %s 应放行，得到 %d", p, rec.Code)
		}
	}
	if rec := doAuth(h, http.MethodOptions, "/api/library/gaps", "203.0.113.9:1", nil); rec.Code == http.StatusUnauthorized {
		t.Error("OPTIONS 预检不应被拒（否则浏览器连错误信息都读不到）")
	}
}

func TestAuthMiddlewareLanModeStillNeedsTokenForPublicPeer(t *testing.T) {
	h := authTestHandler(AuthModeLAN, "s3cret", "")

	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "192.168.1.30:1234", nil); rec.Code != http.StatusOK {
		t.Errorf("内网来源在 lan 模式下免 Token，得到 %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:1", map[string]string{"X-API-Token": "s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("公网来源带对 Token 应放行，得到 %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:1", map[string]string{"X-API-Token": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("公网来源带错 Token 应被拒，得到 %d", rec.Code)
	}
	// ?token= 也认（兼容既有约定）
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps?token=s3cret", "203.0.113.9:1", nil); rec.Code != http.StatusOK {
		t.Errorf("查询参数里的 Token 应被接受，得到 %d", rec.Code)
	}
}

func TestAuthMiddlewareTokenMode(t *testing.T) {
	h := authTestHandler(AuthModeToken, "s3cret", "")
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "127.0.0.1:1", nil); rec.Code != http.StatusUnauthorized {
		t.Error("token 模式下本机也要带 Token")
	}
	if rec := doAuth(h, http.MethodGet, "/api/health", "127.0.0.1:1", nil); rec.Code != http.StatusOK {
		t.Error("token 模式下探活仍应免鉴权")
	}
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "127.0.0.1:1", map[string]string{"X-API-Token": "s3cret"}); rec.Code != http.StatusOK {
		t.Error("带对 Token 应放行")
	}
}

func TestAuthMiddlewareTokenModeWithoutTokenIsClosed(t *testing.T) {
	// FN_AUTH_MODE=token 但没设 FN_API_TOKEN：没有凭据可比对，只能全拒（fail-closed），
	// 并且要说清楚「是配置漏了」，而不是让人对着 401 猜。
	h := authTestHandler(AuthModeToken, "", "")
	rec := doAuth(h, http.MethodGet, "/api/library/gaps", "127.0.0.1:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据的 token 模式应拒绝，得到 %d", rec.Code)
	}
	if msg, _ := decodeBody(t, rec)["message"].(string); !strings.Contains(msg, "FN_API_TOKEN") {
		t.Errorf("应提示未设置 FN_API_TOKEN，实际: %q", msg)
	}
}

func TestAuthMiddlewareNoneModeIsFullyOpen(t *testing.T) {
	h := authTestHandler(AuthModeNone, "", "")
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:1", nil); rec.Code != http.StatusOK {
		t.Errorf("none 模式应完全放开（含公网来源），得到 %d", rec.Code)
	}
}

func TestAuthMiddlewareIgnoresForwardedHeaders(t *testing.T) {
	h := authTestHandler(AuthModeLAN, "", "")
	// 公网来源伪造 XFF 成本为零，绝不能因此被当成内网
	rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:1", map[string]string{
		"X-Forwarded-For": "192.168.1.30",
		"X-Real-IP":       "192.168.1.30",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("伪造 X-Forwarded-For 不应放行，得到 %d", rec.Code)
	}
	// 反向方向：内网来源带奇怪的头也不该被误伤（只看对端地址）
	rec = doAuth(h, http.MethodGet, "/api/library/gaps", "192.168.1.30:1", map[string]string{
		"X-Forwarded-For": "203.0.113.9",
	})
	if rec.Code != http.StatusOK {
		t.Errorf("内网来源不应因为 XFF 被拒，得到 %d", rec.Code)
	}
}

func TestAuthMiddlewareTrustedCIDRsExtraPeer(t *testing.T) {
	h := authTestHandler(AuthModeLAN, "", "203.0.113.0/24, 198.51.100.7")
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "203.0.113.9:1", nil); rec.Code != http.StatusOK {
		t.Errorf("白名单网段内的来源应放行，得到 %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "198.51.100.7:1", nil); rec.Code != http.StatusOK {
		t.Errorf("白名单单个 IP 应放行，得到 %d", rec.Code)
	}
	if rec := doAuth(h, http.MethodGet, "/api/library/gaps", "198.51.100.9:1", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("白名单外的公网来源仍应被拒，得到 %d", rec.Code)
	}
}

func TestIPNotifierThrottlesRepeats(t *testing.T) {
	n := newIPNotifier(2)
	if !n.shouldLog("1.1.1.1") {
		t.Error("首次应记录")
	}
	if n.shouldLog("1.1.1.1") {
		t.Error("窗口期内重复应被吞掉")
	}
	if !n.shouldLog("2.2.2.2") {
		t.Error("换来源应记录")
	}
	// 到达上限后整体重置，不无界增长（也不 panic）
	if !n.shouldLog("3.3.3.3") {
		t.Error("上限重置后应继续可记录")
	}
	if len(n.seen) > n.limit {
		t.Errorf("seen 大小 %d 超过上限 %d", len(n.seen), n.limit)
	}
}

// ── 对外口径一致性 ──

func TestHealthAndCatalogAgreeOnAuthMode(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		token string
	}{
		{AuthModeNone, "s3cret"},
		{AuthModeLAN, ""},
		{AuthModeLAN, "s3cret"},
		{AuthModeToken, "s3cret"},
		{"", ""},       // 未初始化 → 按 Token 推断
		{"", "s3cret"}, // 未初始化 + 有 Token → token
	} {
		s := &Server{
			bridge:   bridge.NewHandler(bridge.NewManager()),
			apiToken: tc.token,
			authMode: tc.mode,
		}
		want := ResolveAuthMode(tc.mode, tc.token)

		rec := httptest.NewRecorder()
		s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		var health struct {
			Data struct {
				AuthMode string `json:"auth_mode"`
				AuthNote string `json:"auth_note"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
			t.Fatalf("health 解析失败: %v", err)
		}
		if health.Data.AuthMode != want {
			t.Errorf("health auth_mode = %q, 期望 %q", health.Data.AuthMode, want)
		}
		if health.Data.AuthNote == "" {
			t.Error("health 应带上 auth_note 说明")
		}

		rec = httptest.NewRecorder()
		s.HandleCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
		var cat CatalogResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
			t.Fatalf("catalog 解析失败: %v", err)
		}
		if cat.AuthMode != want {
			t.Errorf("catalog auth_mode = %q, 期望 %q（两处口径必须一致）", cat.AuthMode, want)
		}
	}
}

func TestAuthSummaryMentionsExtraCIDRs(t *testing.T) {
	s := &Server{authMode: AuthModeLAN, trustedCIDRs: []*net.IPNet{
		{IP: net.ParseIP("203.0.113.0"), Mask: net.CIDRMask(24, 32)},
	}}
	got := s.AuthSummary()
	if !strings.Contains(got, AuthModeLAN) || !strings.Contains(got, "203.0.113.0/24") {
		t.Errorf("AuthSummary 应包含模式与额外网段，实际: %q", got)
	}
}
