package search

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubRT 顶替真实网络：只回一个状态码和一段 body。
type stubRT struct {
	status int
	body   string
	err    error
}

func (s stubRT) RoundTrip(*http.Request) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

// useStubClient 换掉出站客户端并关掉限速，测试结束后恢复。
//
// 关限速不是为了快：真节流器会 sleep 到下一个时间片（默认 300ms 起、带抖动），
// 一个测四个请求的用例会平白慢一两秒 —— 久了就没人愿意跑。
func useStubClient(t *testing.T, st stubRT) {
	t.Helper()
	oldClient, oldInterval := httpClient, MinInterval()
	httpClient = &http.Client{Transport: st}
	SetMinInterval(0)
	ResetDiagnostics()
	t.Cleanup(func() {
		httpClient = oldClient
		SetMinInterval(oldInterval)
		ResetDiagnostics()
	})
}

func healthFor(t *testing.T, platform string) PlatformHealth {
	t.Helper()
	for _, h := range PlatformDiagnostics() {
		if h.Platform == platform {
			return h
		}
	}
	t.Fatalf("诊断里没有 %q 这个平台，实际有 %d 个", platform, len(PlatformDiagnostics()))
	return PlatformHealth{}
}

func TestRecordCountsAndSuccessRate(t *testing.T) {
	ResetDiagnostics()
	recordOK("wy")
	recordOK("wy")
	recordFailure("wy", "http_status", "HTTP 500 （响应体为空）")
	recordFailure("wy", "transport", "dial tcp: i/o timeout")

	h := healthFor(t, "wy")
	if h.Requests != 4 || h.OK != 2 || h.HTTPFailed != 1 || h.TransportFailed != 1 {
		t.Errorf("计数不对：requests=%d ok=%d http=%d transport=%d，期望 4/2/1/1",
			h.Requests, h.OK, h.HTTPFailed, h.TransportFailed)
	}
	// 失败率只按「请求是否成功」算，empty 不参与（empty 由下一个用例守）
	if h.SuccessRate != 0.5 {
		t.Errorf("success_rate = %v，期望 0.5", h.SuccessRate)
	}
	if h.LastError != "dial tcp: i/o timeout" {
		t.Errorf("last_error = %q，期望最后一次失败", h.LastError)
	}
	if h.LastErrorAt == "" {
		t.Error("last_error_at 应该被填上")
	}
}

// 空结果是「不确定」信号，不能混进失败率 —— 否则「平台坏了」会被读成「这首歌没有」。
func TestNoteEmptyIsNotAFailure(t *testing.T) {
	ResetDiagnostics()
	recordOK("tx")
	recordOK("tx")
	noteEmpty("tx", "某首冷门歌")

	h := healthFor(t, "tx")
	if h.Empty != 1 {
		t.Errorf("empty = %d，期望 1", h.Empty)
	}
	if h.HTTPFailed != 0 || h.TransportFailed != 0 {
		t.Errorf("空结果不该算失败：http=%d transport=%d", h.HTTPFailed, h.TransportFailed)
	}
	if h.SuccessRate != 1 {
		t.Errorf("success_rate = %v，期望 1（两次请求都成功了，只是没结果）", h.SuccessRate)
	}
	if h.LastEmptyAt == "" {
		t.Error("last_empty_at 应该被填上")
	}
}

func TestDiagRingKeepsNewestAndCapsSize(t *testing.T) {
	ResetDiagnostics()
	for i := 0; i < diagRingMax+10; i++ {
		recordFailure("kg", "http_status", "HTTP 500 #"+time.Now().Format("150405.000000")+"-"+string(rune('a'+i%26)))
	}
	events := RecentFailures(0)
	if len(events) != diagRingMax {
		t.Fatalf("环形缓冲留下 %d 条，期望 %d", len(events), diagRingMax)
	}
	// 最新在前：最后写入的那条 detail 必须出现在第一条
	if !strings.Contains(events[0].Detail, "-") || events[0].Platform != "kg" {
		t.Errorf("第一条不是最新的：%+v", events[0])
	}
	if got := len(RecentFailures(5)); got != 5 {
		t.Errorf("RecentFailures(5) 返回 %d 条", got)
	}
	// limit 超过现存条数时按现存返回，不 panic
	if got := len(RecentFailures(999)); got != diagRingMax {
		t.Errorf("RecentFailures(999) 返回 %d 条，期望 %d", got, diagRingMax)
	}
}

func TestDoHTTPForRecordsStatusAndBodyEvidence(t *testing.T) {
	useStubClient(t, stubRT{status: http.StatusInternalServerError})

	req, _ := http.NewRequest("GET", "https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp?w=x", nil)
	resp, err := doHTTP(req)
	if err != nil {
		t.Fatalf("非 200 不该返回 error（调用方只看状态码）：%v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	// body 要被装回去：调用方的语义不能因为「我们偷看了一眼」而变化
	back, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(back) != "" {
		t.Errorf("body 应保持为空，实际 %q", string(back))
	}

	h := healthFor(t, "tx")
	if h.HTTPFailed != 1 || h.Requests != 1 {
		t.Errorf("tx 计数：http_failed=%d requests=%d，期望 1/1", h.HTTPFailed, h.Requests)
	}
	if !strings.Contains(h.LastError, "HTTP 500") || !strings.Contains(h.LastError, "响应体为空") {
		t.Errorf("last_error = %q，期望带上状态码和「响应体为空」", h.LastError)
	}
}

func TestDoHTTPForKeepsBodyForCallers(t *testing.T) {
	useStubClient(t, stubRT{status: http.StatusOK, body: `{"code":0,"data":{"ok":1}}`})

	req, _ := http.NewRequest("GET", "https://music.163.com/api/search/get", nil)
	resp, err := doHTTP(req)
	if err != nil {
		t.Fatalf("成功路径不该报错：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"ok":1`) {
		t.Errorf("成功响应体被吃掉了：%q", string(body))
	}
	if h := healthFor(t, "wy"); h.OK != 1 {
		t.Errorf("wy ok = %d，期望 1", h.OK)
	}
}

func TestDoHTTPForRecordsTransportError(t *testing.T) {
	useStubClient(t, stubRT{err: errors.New("dial tcp 1.2.3.4:443: i/o timeout")})

	req, _ := http.NewRequest("GET", "https://songsearch.kugou.com/song_search_v2?keyword=x", nil)
	if _, err := doHTTP(req); err == nil {
		t.Fatal("传输错误必须原样返回给调用方")
	}
	h := healthFor(t, "kg")
	if h.TransportFailed != 1 {
		t.Errorf("kg transport_failed = %d，期望 1", h.TransportFailed)
	}
	if !strings.Contains(h.LastError, "i/o timeout") {
		t.Errorf("last_error = %q", h.LastError)
	}
}

// 平台代号由 URL 推导：调用点忘传标签是静默失真，所以这里把映射钉死。
func TestPlatformOf(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://music.163.com/api/search/get/web", "wy"},
		{"https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp?w=x", "tx"},
		{"https://u.y.qq.com/cgi-bin/musicu.fcg", "tx"},
		{"https://songsearch.kugou.com/song_search_v2?keyword=x", "kg"},
		{"https://www.kuwo.cn/api/www/search/searchMusicBykeyWord", "kw"},
		{"https://example.com/whatever", diagOtherLabel},
		{"", diagOtherLabel},
	}
	for _, c := range cases {
		if got := platformOf(c.url); got != c.want {
			t.Errorf("platformOf(%q) = %q，期望 %q", c.url, got, c.want)
		}
	}
}

// 没登记的新平台不能把统计弄丢：一律落进 other 桶。
func TestUnknownPlatformFallsIntoOtherBucket(t *testing.T) {
	ResetDiagnostics()
	recordOK("")
	recordFailure("   ", "http_status", "HTTP 502")
	h := healthFor(t, diagOtherLabel)
	if h.Requests != 2 || h.HTTPFailed != 1 {
		t.Errorf("other 桶计数：requests=%d http_failed=%d，期望 2/1", h.Requests, h.HTTPFailed)
	}
}

func TestBodySnippetAndTruncRunes(t *testing.T) {
	if got := bodySnippet(nil); got != "（响应体为空）" {
		t.Errorf("空体 = %q", got)
	}
	if got := bodySnippet([]byte("  <html>\n\n 502 Bad Gateway </html>\n")); got != "<html> 502 Bad Gateway </html>" {
		t.Errorf("空白没压成一行：%q", got)
	}
	// 按字符截断：不能把汉字切成半个（截断后必须是合法 UTF-8）
	long := strings.Repeat("测", 300)
	cut := truncRunes(long, 160)
	if len([]rune(cut)) != 161 { // 160 个字 + 省略号
		t.Errorf("截断后 %d 个字符，期望 161", len([]rune(cut)))
	}
	if !strings.HasSuffix(cut, "…") {
		t.Errorf("截断应带省略号：%q", cut)
	}
	if strings.ContainsAny(cut, "\ufffd") {
		t.Errorf("截断切坏了字符：%q", cut)
	}
	if truncRunes("短", 10) != "短" {
		t.Error("不需要截断时应原样返回")
	}
}

// 诊断记录会被多个平台 goroutine 并发写（搜索是并发扇出的），
// 计数必须精确 —— 这个用例要在 -race 下跑。
func TestDiagConcurrentRecording(t *testing.T) {
	ResetDiagnostics()
	const goroutines, perGoroutine = 16, 25

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				recordOK("wy")
				recordFailure("kg", "http_status", "HTTP 500 并发")
				noteEmpty("tx", "并发")
			}
		}(i)
	}
	wg.Wait()

	total := int64(goroutines * perGoroutine)
	if h := healthFor(t, "wy"); h.OK != total {
		t.Errorf("wy ok = %d，期望 %d", h.OK, total)
	}
	if h := healthFor(t, "kg"); h.HTTPFailed != total {
		t.Errorf("kg http_failed = %d，期望 %d", h.HTTPFailed, total)
	}
	if h := healthFor(t, "tx"); h.Empty != total {
		t.Errorf("tx empty = %d，期望 %d", h.Empty, total)
	}
	if got := len(RecentFailures(0)); got != diagRingMax {
		t.Errorf("环形缓冲 %d 条，期望封顶在 %d", got, diagRingMax)
	}
}

// 请求失败导致的空结果只记 failed，不再补一条 empty —— 两种信号不能混。
func TestEmptyNotRecordedWhenRequestFailed(t *testing.T) {
	useStubClient(t, stubRT{err: errors.New("proxyconnect tcp: dial tcp 127.0.0.1:9: connect: connection refused")})

	const kw = "单元测试唯一词-请求失败"
	cachedSearch("kg", kw, 1, 40, func() []UnifiedSong {
		req, _ := http.NewRequest("GET", "https://songsearch.kugou.com/song_search_v2?keyword=x", nil)
		if resp, _ := doHTTP(req); resp != nil {
			_ = resp.Body.Close()
		}
		return nil
	})

	h := healthFor(t, "kg")
	if h.TransportFailed != 1 {
		t.Errorf("transport_failed = %d，期望 1", h.TransportFailed)
	}
	if h.Empty != 0 {
		t.Errorf("失败导致的空结果不该记 empty，实际 empty=%d", h.Empty)
	}
	for _, ev := range RecentFailures(0) {
		if ev.Kind == "empty" {
			t.Errorf("失败请求混进了一条 empty 事件：%+v", ev)
		}
	}
}

// 反例：请求成功但平台给的是空壳（QQ 那次 200 + 空列表的形态）—— 必须记 empty，
// 而且不能算失败。这条用例就是第一版设计要解决的那个回归。
func TestEmptyRecordedWhenPlatformReturnsEmptyShell(t *testing.T) {
	useStubClient(t, stubRT{status: http.StatusOK, body: `{"code":0,"data":{"song":{"list":[]}}}`})

	cachedSearch("tx", "单元测试唯一词-空壳", 1, 40, func() []UnifiedSong {
		req, _ := http.NewRequest("GET", "https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp?w=x", nil)
		resp, err := doHTTP(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return parseQQMobile(body)
	})

	h := healthFor(t, "tx")
	if h.OK != 1 {
		t.Errorf("ok = %d，期望 1（200 就是成功）", h.OK)
	}
	if h.Empty != 1 {
		t.Errorf("empty = %d，期望 1（这就是静默空壳的可见化）", h.Empty)
	}
	if h.HTTPFailed != 0 || h.TransportFailed != 0 {
		t.Errorf("静默空壳不该算失败：http=%d transport=%d", h.HTTPFailed, h.TransportFailed)
	}
	if h.SuccessRate != 1 {
		t.Errorf("success_rate = %v，期望 1", h.SuccessRate)
	}
}

func TestResetDiagnostics(t *testing.T) {
	ResetDiagnostics()
	recordOK("wy")
	noteEmpty("wy", "x")
	if len(PlatformDiagnostics()) == 0 || len(RecentFailures(0)) == 0 {
		t.Fatal("清空前应该有数据")
	}
	ResetDiagnostics()
	if got := len(PlatformDiagnostics()); got != 0 {
		t.Errorf("清空后还有 %d 个平台", got)
	}
	if got := len(RecentFailures(0)); got != 0 {
		t.Errorf("清空后还有 %d 条明细", got)
	}
}
