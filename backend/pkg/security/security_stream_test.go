package security

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// 回归：响应体读取期间不得 cancel「代理尝试」的 ctx
//
// 2026-10-30 实测的 bug：proxyFallbackTransport 给「走代理的那一次尝试」
// 加了 5 秒上限，用的是 context.WithTimeout。WithTimeout 给 ctx 留的是
// **Deadline**，而 net/http 的读循环在 ctx 到期时会掐掉**正在读的响应体**。
//
// 于是「代理已经返回响应头、音频还没读完」就会在 5 秒后变成
// context canceled —— 表现为下载单曲报「写入文件失败: context canceled」，
// 而且必然发生（音频文件远大于 5 秒的量）。
//
// 修复：上限只约束「等响应头」那段，响应头到手后上限即解除，
// 响应体由调用方的 ctx / Timeout 兜底。
// ─────────────────────────────────────────────────────────────

// TestProxyAttemptCapDoesNotCutResponseBody 钉住「上限只管响应头」这条契约：
// 响应体必须在上限过去之后仍能读完（原来 5 秒一到就被掐断）。
func TestProxyAttemptCapDoesNotCutResponseBody(t *testing.T) {
	defer resetProxyFailureState()

	// 目标：先立刻给响应头，body 分两段慢慢吐（第二段在上限之后才到）。
	const total = 4
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("he"))
		// 等到「代理尝试上限」过去之后再写第二段。
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte("ad"))
	}))
	defer target.Close()

	// 一个「假装是代理」的中转：它只是把请求转发给目标（不缓存 body）。
	// 这里真正的关键在 transport 层，所以代理本身只需要能正常转发。
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := http.NewRequest(r.Method, target.URL+r.URL.Path, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultTransport.RoundTrip(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	pu, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyFor := http.ProxyURL(pu)
	opts := Options{AllowPrivateNet: true}
	rt := &proxyFallbackTransport{
		proxied:        newSafeTransport(opts, nil, proxyFor),
		direct:         newSafeTransport(opts, nil, nil),
		proxyFor:       proxyFor,
		attemptTimeout: 150 * time.Millisecond, // 远小于读完 body 所需时间
	}

	req, _ := http.NewRequest(http.MethodGet, target.URL+"/stream.mp3", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip 报错: %v", err)
	}
	defer resp.Body.Close()

	// 上限（150ms）已经过去，这时候才开始读 body —— 原来这里必然 context canceled。
	time.Sleep(250 * time.Millisecond)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败（上限过去后不该再被掐断）: %v", err)
	}
	if string(body) != "head" {
		t.Fatalf("响应体 %q，期望 head", string(body))
	}
}

// TestProxyAttemptCapStillFallsBackWhenHeadersNeverArrive 上限本身不能失效：
// 代理连上但不回响应头时，仍要按时掉头走直连。
func TestProxyAttemptCapStillFallsBackWhenHeadersNeverArrive(t *testing.T) {
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

	req, _ := http.NewRequest(http.MethodGet, target.URL+"/stream.mp3", nil)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("期望放弃代理后直连成功，实际报错: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "direct-ok" {
		t.Fatalf("正文 %q，期望 direct-ok", string(body))
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("耗时 %v：卡在代理上太久了（上限应尽快生效）", elapsed)
	}
	if hits != 1 {
		t.Fatalf("直连目标被请求 %d 次，期望 1 次", hits)
	}
	if !proxyRecentlyFailed() {
		t.Error("代理被判定不通后应进入冷却期")
	}
}

// TestSafeStreamClientKeepsLongBodyAlive 长响应体在客户端超时之前必须能读完。
//
// NewSafeStreamClient 是给「大文件下载」用的：http.Client.Timeout 覆盖整个
// 响应体读取，所以预算必须给足，否则正常下载也会被自己的超时掐断。
func TestSafeStreamClientKeepsLongBodyAlive(t *testing.T) {
	payload := strings.Repeat("x", 512*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		// 分块慢吐，制造「读完要花一会儿」的真实形状。
		for off := 0; off < len(payload); off += 64 * 1024 {
			end := off + 64*1024
			if end > len(payload) {
				end = len(payload)
			}
			_, _ = w.Write([]byte(payload[off:end]))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	client := NewSafeStreamClient(Options{AllowPrivateNet: true}, 30*time.Second)
	resp, err := client.Get(srv.URL + "/big.mp3")
	if err != nil {
		t.Fatalf("GET 报错: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	if len(body) != len(payload) {
		t.Fatalf("读到 %d 字节，期望 %d", len(body), len(payload))
	}
}

// TestNewSafeStreamClientDefaultsHaveBudget 流式客户端必须自带一个像样的超时预算：
// 传 0 也要兜底（0 在 http.Client 里表示「永不超时」，对出站请求不合适）。
func TestNewSafeStreamClientDefaultsHaveBudget(t *testing.T) {
	c := NewSafeStreamClient(Options{}, 0)
	if c.Timeout <= 0 {
		t.Fatalf("流式客户端超时应为正数（0=永不超时，出站请求不合适），实际 %v", c.Timeout)
	}
	if c.Timeout < time.Minute {
		t.Fatalf("流式客户端超时 %v 太短：音频文件动辄几十 MB", c.Timeout)
	}
}

// TestCancelOnCloseBodyReleasesContext cancelOnCloseBody 必须在关闭时释放 ctx，
// 否则每次代理尝试都会漏一个 WithCancel（协程/内存泄漏）。
func TestCancelOnCloseBodyReleasesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelOnCloseBody{ReadCloser: io.NopCloser(strings.NewReader("x")), cancel: cancel}
	if ctx.Err() != nil {
		t.Fatal("初始状态不该已取消")
	}
	// 多次 Close 只取消一次，也不该 panic。
	_ = body.Close()
	_ = body.Close()
	if ctx.Err() == nil {
		t.Fatal("关闭 body 后应释放 ctx")
	}
}
