package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 上游 Content-Type 声明与实际容器不符是常态，不是异常：
// 实测网易云的 FLAC 直链会带 `Content-Type: audio/mpeg`。
//
// 原样透传的后果不是「播放器立刻报错」，而是**更难查的那种**：
// 桌面 Chrome 靠内容嗅探照样能播，可 `canPlayType` / MSE 判定、Safari 与
// 部分移动端 WebView 会按声明类型走 —— 用户看到「明明有链接却不出声」，
// 而每一环的日志都是正常的。
func TestStreamContentTypePrefersMagic(t *testing.T) {
	flacHead := append([]byte("fLaC"), make([]byte, 32)...)
	mp3Head := append([]byte("ID3"), make([]byte, 32)...)

	cases := []struct {
		name     string
		upstream string
		head     []byte
		want     string
	}{
		{
			name:     "网易云把 FLAC 标成 audio/mpeg —— 以魔数为准",
			upstream: "audio/mpeg",
			head:     flacHead,
			want:     "audio/flac",
		},
		{
			name:     "真 mp3 与上游声明一致时也返回标准 MIME",
			upstream: "audio/mpeg",
			head:     mp3Head,
			want:     "audio/mpeg",
		},
		{
			name:     "上游声明正确时不改（flac 标 flac）",
			upstream: "audio/flac",
			head:     flacHead,
			want:     "audio/flac",
		},
		{
			name:     "认不出容器（head 为空：Range 从中间起）→ 沿用上游声明",
			upstream: "audio/x-flac",
			head:     nil,
			want:     "audio/x-flac",
		},
		{
			name:     "认不出容器且上游没声明 → 回落 audio/mpeg（历史行为）",
			upstream: "",
			head:     nil,
			want:     "audio/mpeg",
		},
		{
			name:     "上游回的是错误页并声明 text/html → 不猜成音频",
			upstream: "text/html",
			head:     []byte("<html><body>403 Forbidden"),
			want:     "text/html",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamContentType(tc.upstream, tc.head); got != tc.want {
				t.Fatalf("streamContentType(%q, head) = %q, 期望 %q", tc.upstream, got, tc.want)
			}
		})
	}
}

// 端到端：中转把上游错标的 Content-Type 纠正过来，且**一个字节都不丢**。
// 读头再写回是最容易把流写坏的地方（少写 head、或 Content-Length 与实际不符
// 让浏览器把连接判成截断），所以这里逐字节比对。
func TestHandleAudioStreamCorrectsContentType(t *testing.T) {
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1") // 放行 httptest 的 127.0.0.1

	body := append([]byte("fLaC"), make([]byte, 5000)...)
	for i := 12; i < len(body); i++ {
		body[i] = byte(i % 251)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 这就是实测里网易云的行为：内容是 FLAC，头却写 mp3
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	// streamClient 的 Transport 在包初始化时就绑好了 SSRF 策略（AllowPrivateNet
	// 是那时读的），所以这里换一个普通 client，只测本函数自己的逻辑。
	old := streamClient
	streamClient = &http.Client{}
	defer func() { streamClient = old }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/player/stream?url="+upstream.URL+"/x.flac", nil)
	handleAudioStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%q）", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/flac" {
		t.Fatalf("Content-Type = %q，期望 audio/flac（上游写的是 audio/mpeg）", ct)
	}
	if got := rec.Body.Bytes(); len(got) != len(body) || string(got) != string(body) {
		t.Fatalf("中转后字节数 = %d，期望 %d（读头回写丢了数据）", len(got), len(body))
	}
}

// Range 从文件中间起时，开头那几个字节是音频流中段，魔数判断必然误判 ——
// 此时必须原样沿用上游声明，不能凭「认不出/认错」去改 Content-Type。
func TestHandleAudioStreamKeepsUpstreamTypeForMidRange(t *testing.T) {
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	resetStreamTypeCache() // 缓存是包级状态，别让别的用例预热它

	chunk := []byte("ID3-and-some-mp3-bytes-in-the-middle")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Range", "bytes 1000000-1000032/9999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(chunk)
	}))
	defer upstream.Close()

	old := streamClient
	streamClient = &http.Client{}
	defer func() { streamClient = old }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/player/stream?url="+upstream.URL+"/x.flac", nil)
	req.Header.Set("Range", "bytes=1000000-1000032")
	handleAudioStream(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("Content-Type = %q，期望沿用上游 audio/mpeg", ct)
	}
	if rec.Body.String() != string(chunk) {
		t.Fatalf("中段数据被改写")
	}
	if cr := rec.Header().Get("Content-Range"); !strings.Contains(cr, "1000000-1000032") {
		t.Fatalf("Content-Range 没透传：%q", cr)
	}
}

// 拖动进度条会带来一串 Range 中段请求：这些请求自己判不了类型，必须复用
// 「同一 URL 从文件开头判出来」的那个类型，否则同一次播放会出现
// 「头一段 audio/flac、拖一下就变 audio/mpeg」的割裂。
func TestHandleAudioStreamReusesTypeForMidRangeAfterProbe(t *testing.T) {
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	resetStreamTypeCache()

	flacBody := append([]byte("fLaC"), make([]byte, 4096)...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游永远把 FLAC 标成 mp3（网易云的实际形状）
		w.Header().Set("Content-Type", "audio/mpeg")
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(flacBody)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(flacBody)
			return
		}
		w.Header().Set("Content-Range", "bytes 2000-2032/9999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(flacBody[2000:2033])
	}))
	defer upstream.Close()

	old := streamClient
	streamClient = &http.Client{}
	defer func() { streamClient = old }()

	urlPath := "/api/player/stream?url=" + upstream.URL + "/y.flac"

	// 第一次：从头取 → 以魔数为准，判出 audio/flac 并记住
	rec1 := httptest.NewRecorder()
	handleAudioStream(rec1, httptest.NewRequest(http.MethodGet, urlPath, nil))
	if ct := rec1.Header().Get("Content-Type"); ct != "audio/flac" {
		t.Fatalf("首段 Content-Type = %q，期望 audio/flac", ct)
	}

	// 第二次：中段 Range → 自己没有魔数可判，应复用记住的 audio/flac
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, urlPath, nil)
	req2.Header.Set("Range", "bytes=2000-2032")
	handleAudioStream(rec2, req2)
	if ct := rec2.Header().Get("Content-Type"); ct != "audio/flac" {
		t.Fatalf("中段 Content-Type = %q，期望复用 audio/flac（上游写的是 audio/mpeg）", ct)
	}
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("中段状态码 = %d，期望 206", rec2.Code)
	}
}

// 缓存是纯加速用的：不能因为记过一个 URL 就影响别的 URL。
func TestStreamTypeCacheIsPerURL(t *testing.T) {
	resetStreamTypeCache()
	rememberStreamType("https://a.example/1.flac?x=1", "audio/flac")
	if got := cachedStreamType("https://a.example/1.flac?x=1"); got != "audio/flac" {
		t.Fatalf("同 URL 取回 = %q，期望 audio/flac", got)
	}
	if got := cachedStreamType("https://a.example/2.flac?x=1"); got != "" {
		t.Fatalf("别的 URL 取回 = %q，期望空", got)
	}
	resetStreamTypeCache()
	if got := cachedStreamType("https://a.example/1.flac?x=1"); got != "" {
		t.Fatalf("reset 后仍取回 %q，期望空", got)
	}
}
