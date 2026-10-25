package appupdate

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 应用内配置的代理必须真的被用上，而且**改了即时生效**。
//
// 用户 2026-10-25 的现场：NAS 上「检查更新」能通、「下载安装包」失败于
// `dial tcp 20.205.243.166:443: i/o timeout` —— release 资产在
// objects.githubusercontent.com，该网络下直连出不去；而应用由飞牛拉起，
// 读不到 shell 的 HTTP_PROXY。于是要在「系统 → 设置 → 版本更新」里填代理。
//
// 这个用例同时锁住三件事：
//  1. 配了代理，出站真的经它（不是配了但没用）；
//  2. 代理值变了，客户端会重建（不是「要重启应用才生效」）；
//  3. 值没变时复用同一个 client（别每个请求都新建连接池）。
func TestConfiguredProxyIsUsedAndRebuiltOnChange(t *testing.T) {
	var hits int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxy.Close()

	proxyAddr := ""
	m := New(Options{
		DataDir:   t.TempDir(),
		Logf:      t.Logf,
		ProxyFunc: func() string { return proxyAddr },
	})

	direct := m.httpClient() // 未配代理时的 client

	// ① 配上一个代理 → 公网目标应当经它出站。
	proxyAddr = proxy.URL
	proxied := m.httpClient()
	if proxied == direct {
		t.Fatal("代理值变了却复用了旧 client：等于「改完要重启才生效」")
	}
	resp, err := proxied.Get("http://public.example.invalid/yinshu.fpk")
	if err != nil {
		t.Fatalf("配了代理却走不通: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "via-proxy" || hits == 0 {
		t.Fatalf("请求没有经过配置的代理：body=%q hits=%d", body, hits)
	}

	// ② 值没变 → 复用，不重建。
	if again := m.httpClient(); again != proxied {
		t.Error("代理值没变时不该重建 client（会白白丢掉连接池）")
	}

	// ③ 清掉代理 → 重建，并且不再走这个代理。
	// 不断言「直连一定失败」：这台机器（或部署环境）可能本来就有 HTTP_PROXY 环境变量，
	// 清掉配置后回落到环境变量是**正确**行为。只断言不再经过刚才那个代理。
	before := hits
	proxyAddr = ""
	back := m.httpClient()
	if back == proxied {
		t.Fatal("清掉代理后 client 没重建，代理还在生效")
	}
	_, _ = back.Get("http://public.example.invalid/yinshu.fpk") // 结果不重要，可能被环境代理接住
	if hits != before {
		t.Errorf("代理已清除，请求却又经过了它（hits %d → %d）", before, hits)
	}
}

// 测试注入的 client 必须原样使用：否则用 httptest.Server 写的用例会突然去连真代理。
func TestInjectedClientIgnoresProxyFunc(t *testing.T) {
	injected := &http.Client{}
	m := New(Options{
		DataDir:   t.TempDir(),
		Client:    injected,
		ProxyFunc: func() string { return "http://192.168.1.3:7890" },
	})
	if got := m.httpClient(); got != injected {
		t.Error("注入了 client 时不该按 ProxyFunc 重建")
	}
	// 没有 ProxyFunc 时也必须原样返回注入值（老行为，不能回归）。
	m2 := New(Options{DataDir: t.TempDir(), Client: injected})
	if got := m2.httpClient(); got != injected {
		t.Error("没有 ProxyFunc 时注入的 client 应当原样返回")
	}
}
