package security

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateURLBlocksUnsafeTargets(t *testing.T) {
	opts := Options{} // 默认禁止内网

	blocked := []string{
		"http://127.0.0.1/x",
		"http://127.0.0.1:8899/api/health",
		"http://10.0.0.5/x",
		"http://192.168.1.1/x",
		"http://172.16.0.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/x",
		"http://0.0.0.0/x",
		"http://[::1]/x",
		"ftp://example.com/x",
		"file:///etc/passwd",
		"",
		"   ",
	}
	for _, raw := range blocked {
		if _, err := ValidateURL(raw, opts); err == nil {
			t.Errorf("ValidateURL(%q) 应当被拒绝，但通过了", raw)
		}
	}
}

func TestValidateURLAcceptsPublicTargets(t *testing.T) {
	opts := Options{}
	allowed := []string{
		"https://music.163.com/api/song/lyric?id=1",
		"http://8.8.8.8/x",
		"https://c.y.qq.com/soso/fcgi-bin/client_search_cp?w=test",
	}
	for _, raw := range allowed {
		if _, err := ValidateURL(raw, opts); err != nil {
			t.Errorf("ValidateURL(%q) 应当通过，却被拒绝: %v", raw, err)
		}
	}
}

func TestValidateURLAllowPrivateOptIn(t *testing.T) {
	opts := Options{AllowPrivateNet: true}
	if _, err := ValidateURL("http://127.0.0.1:8899/x", opts); err != nil {
		t.Errorf("显式允许内网时不应被拒绝: %v", err)
	}
}

func TestValidateURLHostAllowlist(t *testing.T) {
	opts := Options{HostAllowlist: []string{"music.163.com"}}

	if _, err := ValidateURL("https://music.163.com/x", opts); err != nil {
		t.Errorf("白名单内的域名应通过: %v", err)
	}
	if _, err := ValidateURL("https://a.music.163.com/x", opts); err != nil {
		t.Errorf("白名单域名的子域名应通过: %v", err)
	}
	if _, err := ValidateURL("https://evil.com/x", opts); err == nil {
		t.Error("白名单外的域名应被拒绝")
	}
	// 后缀混淆：notmusic.163.com 不应命中 music.163.com
	if _, err := ValidateURL("https://notmusic.163.com/x", opts); err == nil {
		t.Error("后缀混淆域名应被拒绝")
	}
}

// TestSafeClientBlocksLoopback 直接验证连接期防护：
// 即使域名能解析，Dialer.Control 也会拦下回环地址。
func TestSafeClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	client := NewSafeClient(Options{}, 3*time.Second)
	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("安全客户端不应能访问回环地址 %s", srv.URL)
	}
	if !strings.Contains(err.Error(), "禁止") {
		t.Errorf("错误信息应说明被安全策略拒绝，实际: %v", err)
	}
}

func TestSafeClientAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := NewSafeClient(Options{AllowPrivateNet: true}, 3*time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("显式允许内网时应可访问: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", resp.StatusCode)
	}
}

func TestLimitedRead(t *testing.T) {
	opts := Options{MaxBodyBytes: 4}

	data, err := LimitedRead(strings.NewReader("abcd"), opts)
	if err != nil {
		t.Fatalf("恰好等于上限应通过: %v", err)
	}
	if string(data) != "abcd" {
		t.Errorf("内容 = %q", data)
	}

	if _, err := LimitedRead(strings.NewReader("abcde"), opts); err == nil {
		t.Error("超过上限应返回错误而不是静默截断")
	}

	// 未设置上限时使用默认值
	if _, err := LimitedRead(strings.NewReader("hello"), Options{}); err != nil {
		t.Errorf("默认上限下短内容应通过: %v", err)
	}
}

func TestDefaultsFromEnv(t *testing.T) {
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	opts := DefaultOptions()
	if !opts.AllowPrivateNet {
		t.Error("FN_ALLOW_PRIVATE_NET=1 应打开内网访问")
	}

	t.Setenv("FN_ALLOW_PRIVATE_NET", "")
	if DefaultOptions().AllowPrivateNet {
		t.Error("未设置时应默认禁止内网访问")
	}
}

// ── 代理分流（修复「代理本身是内网地址被 SSRF 拦下」的死锁） ──

func TestIsLocalTarget(t *testing.T) {
	local := []string{
		"localhost", "127.0.0.1", "::1", "192.168.1.3", "10.0.0.5",
		"172.16.0.1", "169.254.169.254", "100.64.0.1", "nas", "ai-server",
	}
	for _, h := range local {
		if !IsLocalTarget(h) {
			t.Errorf("IsLocalTarget(%q) 应为 true（内网/回环）", h)
		}
	}

	public := []string{
		"api.deepseek.com", "8.8.8.8", "example.com", "1.1.1.1",
	}
	for _, h := range public {
		if IsLocalTarget(h) {
			t.Errorf("IsLocalTarget(%q) 应为 false（公网）", h)
		}
	}
}

// TestProxyBypassedForLocalTargets 内网目标必须直连，否则代理自身会被 SSRF 规则拦下
func TestProxyBypassedForLocalTargets(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://192.168.1.3:7890")
	t.Setenv("HTTPS_PROXY", "http://192.168.1.3:7890")

	opts := Options{AllowPrivateNet: true}
	proxyFor := proxyFunc(opts)

	cases := []struct {
		url       string
		wantProxy bool
	}{
		{"http://192.168.1.66:20128/v1/chat/completions", false}, // 局域网 AI 服务 → 直连
		{"http://127.0.0.1:11434/v1/chat/completions", false},    // 本机 Ollama → 直连
		{"https://api.deepseek.com/v1/chat/completions", true},   // 公网 → 走代理
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		u, err := proxyFor(req)
		if err != nil {
			t.Fatalf("proxyFunc(%s) 报错: %v", tc.url, err)
		}
		gotProxy := u != nil
		if gotProxy != tc.wantProxy {
			t.Errorf("%s 走代理 = %v, 期望 %v", tc.url, gotProxy, tc.wantProxy)
		}
	}
}

// TestLocalAIClientReachesLoopback AI 客户端应能访问局域网自建服务
func TestLocalAIClientReachesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	// 模拟用户环境里配置了代理（代理本身也是内网地址）
	t.Setenv("HTTP_PROXY", "http://192.168.1.3:7890")

	opts := Options{AllowPrivateNet: true}
	client := NewSafeClient(opts, 5*time.Second)

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("允许内网时不应被代理死锁拦住: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", resp.StatusCode)
	}
}

// clearProxyEnv 清掉代理相关环境变量，避免开发机上的全局代理干扰用例。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

// TestProxyExemptAddrsFromEnv 代理出口地址从环境变量读取（含默认端口与大小写变体）
func TestProxyExemptAddrsFromEnv(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", "http://192.168.1.3:7890")
	t.Setenv("https_proxy", "https://proxy.internal")
	t.Setenv("ALL_PROXY", "socks5://192.168.1.3:7891")

	got := proxyExemptAddrs()
	want := []string{"192.168.1.3:7890", "proxy.internal:443", "192.168.1.3:7891"}
	if len(got) != len(want) {
		t.Fatalf("proxyExemptAddrs() = %v, 期望 %d 项", got, len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("proxyExemptAddrs() 缺少 %s，实际 %v", w, got)
		}
	}

	clearProxyEnv(t)
	if len(proxyExemptAddrs()) != 0 {
		t.Errorf("没有配代理时不应有任何豁免，实际 %v", proxyExemptAddrs())
	}
}

// TestDialControlExemptsOnlyConfiguredProxy 只放行「环境里配的那个代理出口」，别的内网地址照旧拦
func TestDialControlExemptsOnlyConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", "http://192.168.1.3:7890")
	ctl := dialControl(Options{}, proxyExemptAddrs())

	if err := ctl("tcp", "192.168.1.3:7890", nil); err != nil {
		t.Fatalf("已配置的代理出口不应被拦: %v", err)
	}
	if err := ctl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("公网地址不应被拦: %v", err)
	}
	blocked := []string{"192.168.1.3:7891", "192.168.1.9:80", "127.0.0.1:8899", "169.254.169.254:80"}
	for _, addr := range blocked {
		if err := ctl("tcp", addr, nil); err == nil {
			t.Errorf("%s 必须仍被拦下（豁免只限已配置的代理）", addr)
		}
	}

	clearProxyEnv(t)
	ctlNoProxy := dialControl(Options{}, proxyExemptAddrs())
	if err := ctlNoProxy("tcp", "192.168.1.3:7890", nil); err == nil {
		t.Error("没配代理时 192.168.1.3:7890 必须被拦")
	}
}

// TestPublicRequestThroughConfiguredPrivateProxy 端到端：环境里配了内网代理时，公网出站必须真的能走通。
// 这是「取链 502 / 流中继 502」的回归：Control 看到的是代理自己的地址，只做目标侧分流不够。
func TestPublicRequestThroughConfiguredPrivateProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			t.Errorf("正向代理收到的应是绝对 URI，实际 %q", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("proxied:" + r.URL.Host))
	}))
	defer proxy.Close()

	// 用「显式代理 + 显式豁免表」构造，绕开 http.ProxyFromEnvironment 的进程级缓存
	// （它只在进程内第一次被调用时读环境变量，之后改 env 不生效，会让用例互相干扰）。
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := newSafeTransport(Options{}, map[string]bool{normalizeAddr(proxyURL.Host): true}, http.ProxyURL(proxyURL))
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get("http://public.example.invalid/song")
	if err != nil {
		t.Fatalf("公网目标经内网代理应能走通: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "proxied:public.example.invalid" {
		t.Errorf("经代理返回的内容 = %q", body)
	}

	// 对照组：没有豁免表时，同一个内网代理会被自己的规则拦下 —— 这就是修复前的表现。
	bad := &http.Client{Timeout: 5 * time.Second, Transport: newSafeTransport(Options{}, nil, http.ProxyURL(proxyURL))}
	if _, err := bad.Get("http://public.example.invalid/song"); err == nil {
		t.Error("没有豁免表时应当被拦下（对照组）")
	}
}

// deadProxyURL 返回一个「保证连不上」的代理地址：起一个临时服务再关掉它。
func deadProxyURL(t *testing.T) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	raw := srv.URL
	srv.Close() // 立刻关掉 → 端口无人监听，拨号必然 ECONNREFUSED
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestProxyFallbackToDirectWhenProxyUnreachable 代理连不上时应改用直连，且只多花一次请求。
func TestProxyFallbackToDirectWhenProxyUnreachable(t *testing.T) {
	defer resetProxyFailureState()
	dead := deadProxyURL(t)
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("direct-ok"))
	}))
	defer target.Close()

	opts := Options{AllowPrivateNet: true}
	proxyFor := http.ProxyURL(dead)
	rt := &proxyFallbackTransport{
		proxied:  newSafeTransport(opts, nil, proxyFor),
		direct:   newSafeTransport(opts, nil, nil),
		proxyFor: proxyFor,
	}

	req, err := http.NewRequest(http.MethodGet, target.URL+"/song.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("期望降级直连成功，实际报错: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "direct-ok" {
		t.Fatalf("状态 %d 正文 %q，期望 200 direct-ok", resp.StatusCode, string(body))
	}
	if hits != 1 {
		t.Fatalf("直连目标被请求 %d 次，期望 1 次（不该重试多遍）", hits)
	}

	// 对照组：没有降级能力的裸 transport 必须失败（这正是「取链 502」的形态）
	plain, err := newSafeTransport(opts, nil, proxyFor).RoundTrip(req.Clone(req.Context()))
	if err == nil {
		t.Fatalf("无降级 transport 竟然成功了（resp=%v）", plain)
	}
}

// TestNoFallbackWhenProxyReturnsResponse 代理给出了响应（哪怕 5xx）就绝不重试：
// 否则会对着一个已经收到请求的服务重复投递。
func TestNoFallbackWhenProxyReturnsResponse(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("proxy-dead"))
	}))
	defer proxy.Close()

	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("direct-ok"))
	}))
	defer target.Close()

	opts := Options{AllowPrivateNet: true}
	pu, _ := url.Parse(proxy.URL)
	proxyFor := http.ProxyURL(pu)
	rt := &proxyFallbackTransport{
		proxied:  newSafeTransport(opts, nil, proxyFor),
		direct:   newSafeTransport(opts, nil, nil),
		proxyFor: proxyFor,
	}

	req, _ := http.NewRequest(http.MethodGet, target.URL+"/x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态 %d，期望原样返回代理的 502", resp.StatusCode)
	}
	if hits != 0 {
		t.Fatalf("拿到响应后仍直连了 %d 次，必须为 0", hits)
	}
}

// TestNoFallbackForNonReplayableBody 请求体没法重放时不做降级重试（宁可不试也不发一半）。
func TestNoFallbackForNonReplayableBody(t *testing.T) {
	dead := deadProxyURL(t)
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer target.Close()

	opts := Options{AllowPrivateNet: true}
	proxyFor := http.ProxyURL(dead)
	rt := &proxyFallbackTransport{
		proxied:  newSafeTransport(opts, nil, proxyFor),
		direct:   newSafeTransport(opts, nil, nil),
		proxyFor: proxyFor,
	}

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("streaming-body"))
		_ = pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, target.URL+"/upload", pr)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("前置条件不成立：io.Pipe 的请求体不该有 GetBody")
	}
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("不可重放的请求体在代理失败后不该重试成功")
	}
	if hits != 0 {
		t.Fatalf("不可重放却直连了 %d 次，必须为 0", hits)
	}
}

// TestProxyFallbackStillBlocksPrivateTargets 直连降级不得绕过 SSRF：默认选项下私网目标照旧拦。
func TestProxyFallbackStillBlocksPrivateTargets(t *testing.T) {
	dead := deadProxyURL(t)
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer target.Close()

	opts := Options{} // 默认禁止内网
	proxyFor := http.ProxyURL(dead)
	rt := &proxyFallbackTransport{
		proxied:  newSafeTransport(opts, nil, proxyFor),
		direct:   newSafeTransport(opts, nil, nil),
		proxyFor: proxyFor,
	}

	req, _ := http.NewRequest(http.MethodGet, target.URL+"/x", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("降级直连竟然打到了私网目标，SSRF 校验被绕过")
	}
	if hits != 0 {
		t.Fatalf("私网目标被请求 %d 次，必须为 0", hits)
	}
}

// TestIsProxyConnError 连接层错误才算「可以换通道」，业务错误不算。
func TestIsProxyConnError(t *testing.T) {
	if !isProxyConnError(io.EOF) {
		t.Error("io.EOF 应算连接层错误")
	}
	if !isProxyConnError(&url.Error{Op: "Post", URL: "https://x/y", Err: errors.New("proxyconnect tcp: dial tcp 1.2.3.4:7890: connect: connection refused")}) {
		t.Error("proxyconnect 文案应算连接层错误")
	}
	if isProxyConnError(errors.New("bad request: invalid musicId")) {
		t.Error("业务错误不该算连接层错误")
	}
	if isProxyConnError(nil) {
		t.Error("nil 不该算连接层错误")
	}
}

// TestProxyAttemptCapFallsBackToDirectToAvoidHang 代理「能连上但不干活」时必须尽快掉头走直连：
// 不能把调用方的整个超时预算耗在代理上（这是 15s 预算里取链前 12s 都卡住的成因）。
func TestProxyAttemptCapFallsBackToDirectToAvoidHang(t *testing.T) {
	defer resetProxyFailureState()

	block := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 只接受连接，不回应
	}))
	defer func() { close(block); hang.Close() }()

	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("direct-ok"))
	}))
	defer target.Close()

	opts := Options{AllowPrivateNet: true}
	pu, _ := url.Parse(hang.URL)
	proxyFor := http.ProxyURL(pu)
	rt := &proxyFallbackTransport{
		proxied:        newSafeTransport(opts, nil, proxyFor),
		direct:         newSafeTransport(opts, nil, nil),
		proxyFor:       proxyFor,
		attemptTimeout: 150 * time.Millisecond,
	}

	req, _ := http.NewRequest(http.MethodPost, target.URL+"/resolve", strings.NewReader(`{"a":1}`))
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("期望放弃代理后直连成功，实际报错: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "direct-ok" {
		t.Fatalf("正文 %q，期望 direct-ok", string(body))
	}
	if elapsed > 3*time.Second {
		t.Fatalf("耗时 %v：卡在代理上太久了（上限应尽快生效）", elapsed)
	}
	if hits != 1 {
		t.Fatalf("直连目标被请求 %d 次，期望 1 次", hits)
	}
	if !proxyRecentlyFailed() {
		t.Error("代理被判定不通后应进入冷却期")
	}
}

// TestProxyFailureCooldownSkipsProxy 冷却期内所有请求都不再尝试代理，冷却结束自动恢复。
func TestProxyFailureCooldownSkipsProxy(t *testing.T) {
	clearProxyEnv(t)
	defer resetProxyFailureState()
	t.Setenv("HTTP_PROXY", "http://192.168.1.3:7890")
	t.Setenv("HTTPS_PROXY", "http://192.168.1.3:7890")

	opts := Options{}
	proxyFor := proxyFunc(opts)
	req, _ := http.NewRequest(http.MethodGet, "https://api.deepseek.com/v1/chat/completions", nil)

	resetProxyFailureState()
	u, err := proxyFor(req)
	if err != nil {
		t.Fatalf("proxyFunc 报错: %v", err)
	}
	if u == nil {
		t.Fatal("冷却期外应当走代理")
	}

	noteProxyFailure()
	u2, err := proxyFor(req)
	if err != nil {
		t.Fatalf("proxyFunc 报错: %v", err)
	}
	if u2 != nil {
		t.Fatalf("冷却期内应当直连，实际仍走代理 %v", u2)
	}

	resetProxyFailureState()
	u3, _ := proxyFor(req)
	if u3 == nil {
		t.Fatal("冷却结束后应当恢复走代理")
	}
}

// 出站请求不启用 HTTP/2：不少音乐/接口站挂在 CDN 边缘（openresty / EdgeOne）上，
// 明明宣告支持 h2，但 Go 的 h2 客户端会被挂到「响应头 20s 超时」才失败，
// 而同一地址走 HTTP/1.1 只要 0.2s（2026-09-27 真机）。这条把结论钉住。
func TestOutboundTransportDoesNotForceHTTP2(t *testing.T) {
	tr := NewSafeTransport(DefaultOptions())
	if tr.ForceAttemptHTTP2 {
		t.Fatal("出站 transport 不应启用 HTTP/2：CDN 边缘会挂住响应头 20s")
	}
}
