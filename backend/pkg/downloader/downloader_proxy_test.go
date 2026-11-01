package downloader

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"fn-lx-player/pkg/security"
)

// ─────────────────────────────────────────────────────────────
// 回归：经代理下载大文件时，响应体不能被「代理尝试上限」掐断
//
// 2026-10-30 实测的 bug： NAS 上有全局 HTTP_PROXY，所有公网取音都走代理。
// security.proxyFallbackTransport 给「经代理的那一次尝试」加了 5 秒上限，
// 用的却是 context.WithTimeout —— 而 WithTimeout 留的是 **Deadline**，
// net/http 的读循环在 ctx 到期/被 cancel 时会掐掉**正在读的响应体**。
//
// 表现：取链成功、开始写盘、写到一半被 cancel → downloader.go:611
// 「写入文件失败: context canceled」，而且必然发生（音频远大于 5 秒的量）。
//
// 这里用一条「假代理」把请求转到目标上，并让 body 在响应头之后 6.5 秒才到 ——
// 正好跨过 5 秒那道「等响应头」的上限：
//   - 修复前：body 被 Deadline 掐断 → 报「写入文件失败: context …」；
//   - 修复后：上限只管「等响应头」，body 慢慢读完 → 下载成功。
//
// 假代理必须像真代理一样**先把响应头发给客户端**（Flush）再慢慢转发 body ——
// 真代理（Clash 等）就是这么干的。如果响应头被攒在缓冲区里，客户端会先撞上
// 5 秒上限走「代理不通 → 直连」分支，测的就不是同一个窗口了。
// ─────────────────────────────────────────────────────────────

// proxyAttemptCap 是 security 里给「经代理那一次尝试」的上限（当前 5 秒）。
// 测试里的 body 延迟必须明显超过它，否则测不到被掐断的那个窗口。
const proxyAttemptCap = 5 * time.Second

// TestDownloadThroughProxyKeepsResponseBodyAlive 经代理下载：响应头之后，
// 响应体必须还能慢慢读完（原来 5 秒一到就被掐断）。
func TestDownloadThroughProxyKeepsResponseBodyAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("需要等 body 慢慢到（超过 5 秒的代理尝试上限）")
	}

	// 600KB 的「mp3」：ID3 头 + 填充（> 300KB 才不被判成「文件过小」）。
	payload := append([]byte("ID3"), make([]byte, 600*1024)...)

	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		// 先给响应头，body 稍后才到 —— 这正是原来被掐断的窗口。
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(proxyAttemptCap + 1500*time.Millisecond)
		_, _ = w.Write(payload)
	}))
	defer target.Close()

	var proxyHits int
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
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
		// 真代理会先把响应头发给客户端，再慢慢转发 body。
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	// 与 NewDownloader 同款的客户端，但显式指定代理出口 + 强制本地目标也走代理
	// （否则 127.0.0.1 会被 proxyFunc 判成内网目标直接直连，测不到代理这条路径）。
	opts := security.DefaultOptions()
	opts.AllowPrivateNet = true
	opts.ForceProxyForLocal = true
	opts.ProxyURL = proxySrv.URL
	d.httpClient = security.NewSafeStreamClient(opts, 10*time.Minute)

	res, err := d.downloadSingleSong(SongPayload{
		Name: "乙", Singer: "甲", URL: target.URL + "/a.mp3", Quality: "320k",
		DestDir: outDir,
	})
	if err != nil {
		t.Fatalf("经代理下载应当成功（响应体不该被代理尝试上限掐断）: %v", err)
	}
	if proxyHits == 0 {
		t.Fatal("这条请求根本没走代理，测的不是代理这条路径")
	}
	// 目标只该被访问一次：若发生了「代理不通 → 直连」降级，目标会被访问两次。
	if targetHits != 1 {
		t.Errorf("目标被访问 %d 次，期望 1 次（说明这次请求没走到「响应头已到手」这条路）", targetHits)
	}
	if res.Status != "success" {
		t.Fatalf("状态 %q，期望 success", res.Status)
	}
	fi, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("下载产物不存在: %v", err)
	}
	// 落盘文件会被 applyTags 补写内嵌标签（ID3），所以只会比原始 payload 大，不会小。
	if fi.Size() < int64(len(payload)) {
		t.Errorf("落盘 %d 字节，比原始音频 %d 字节还小", fi.Size(), len(payload))
	}
	if n := countAudioFiles(t, outDir); n != 1 {
		t.Errorf("该只有 1 个音频文件，实际 %d 个", n)
	}
}

// TestNewDownloaderGivesAudioStreamsEnoughTime 钉住超时预算：
// http.Client.Timeout 覆盖的是**整个响应体读取**，45 秒会把大文件掐断。
func TestNewDownloaderGivesAudioStreamsEnoughTime(t *testing.T) {
	d := newTestDownloader(t, t.TempDir())
	if d.httpClient == nil {
		t.Fatal("Downloader 没有 httpClient")
	}
	if d.httpClient.Timeout < 5*time.Minute {
		t.Errorf("httpClient.Timeout = %v，取音频流至少要 5 分钟预算（原来 45s 会掐断大文件）", d.httpClient.Timeout)
	}
}
