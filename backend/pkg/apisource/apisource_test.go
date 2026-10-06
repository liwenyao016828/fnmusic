package apisource

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noPrivateEnv 显式清空 FN_ALLOW_PRIVATE_NET，让用例走**生产路径**。
//
// 服务型音源地址是用户在「音源管理」里显式配置的可信目标（与 pkg/ai 的 AI 服务地址同理），
// 因此 pkg/apisource 自己放开内网访问，**不依赖**任何环境变量 —— 飞牛 FPK 也没给用户
// 配环境变量的入口。清空它就是为了证明：接内网自建音源（httptest 就是 127.0.0.1）不需要 env。
func noPrivateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// 标准 LX Server：GET /api/music/url
func TestDetectLXServer(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/music/url" {
			writeJSON(w, 200, map[string]any{"url": "https://cdn.example.com/a.flac"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, err := Detect(srv.URL, "")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if res.Protocol != ProtocolLxServer {
		t.Errorf("协议 = %q，期望 %q", res.Protocol, ProtocolLxServer)
	}
}

// 私有 REST：GET /url
func TestDetectPrivateREST(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/url" {
			writeJSON(w, 200, map[string]any{"code": 200, "url": "https://cdn.example.com/b.mp3"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, err := Detect(srv.URL, "")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if res.Protocol != ProtocolLxScript {
		t.Errorf("协议 = %q，期望 %q", res.Protocol, ProtocolLxScript)
	}
}

// 自建中转：POST /music/url
func TestDetectMusicAPI(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/music/url" && r.Method == http.MethodPost {
			writeJSON(w, 200, map[string]any{"code": 200, "url": "https://cdn.example.com/c.mp3"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, err := Detect(srv.URL, "")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if res.Protocol != ProtocolLxAPI {
		t.Errorf("协议 = %q，期望 %q", res.Protocol, ProtocolLxAPI)
	}
}

// 探针歌曲解析失败（code 非 200）也算协议匹配 ——
// 否则「探针歌曲下架」会被误判成「音源坏了」
func TestDetectAcceptsJSONWithoutURL(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/music/url" {
			writeJSON(w, 200, map[string]any{"code": 404, "message": "歌曲不存在"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, err := Detect(srv.URL, "")
	if err != nil {
		t.Fatalf("不该因为探针歌曲失败就判为不可用: %v", err)
	}
	if res.Protocol != ProtocolLxServer {
		t.Errorf("协议 = %q，期望 %q", res.Protocol, ProtocolLxServer)
	}
}

// 三种路由都 404 → 报错，且错误里要带三种尝试结果
func TestDetectAllMiss(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	_, err := Detect(srv.URL, "")
	if err == nil {
		t.Fatal("三种协议都不通时应报错")
	}
	msg := err.Error()
	for _, want := range []string{"私有 REST", "标准 LX Server", "自建中转 API"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应列出三种尝试，缺 %q：%s", want, msg)
		}
	}
}

// 返回 HTML（不是音源接口）应被拒绝
func TestDetectRejectsNonJSON(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()

	if _, err := Detect(srv.URL, ""); err == nil {
		t.Fatal("返回 HTML 时不该识别成音源")
	}
}

func TestDetectRejectsBadBaseURL(t *testing.T) {
	cases := []struct{ in, wantSubstr string }{
		{"", "不能为空"},
		{"ftp://a.com", "http:// 或 https://"},
		{"a.com", "http:// 或 https://"},
		{"git@github.com:u/r.git", "http:// 或 https://"},
	}
	for _, c := range cases {
		_, err := Detect(c.in, "")
		if err == nil {
			t.Errorf("Detect(%q) 应当报错", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("Detect(%q) 错误 %q 应含 %q", c.in, err.Error(), c.wantSubstr)
		}
	}
}

// 取链：三种协议都要能拿到地址，且兼容 {url} 与 {data:{url}} 两种形状
func TestResolveAllProtocols(t *testing.T) {
	noPrivateEnv(t)
	const want = "https://cdn.example.com/song.flac"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/url":
			// 私有 REST 用 songId 参数
			if r.URL.Query().Get("songId") == "" {
				writeJSON(w, 400, map[string]any{"code": 400, "message": "缺 songId"})
				return
			}
			writeJSON(w, 200, map[string]any{"code": 200, "url": want})
		case r.URL.Path == "/api/music/url":
			// 标准 LX Server 用 id 参数，且可能把 url 包在 data 里
			if r.URL.Query().Get("id") == "" {
				writeJSON(w, 400, map[string]any{"code": 400, "message": "缺 id"})
				return
			}
			writeJSON(w, 200, map[string]any{"code": 200, "data": map[string]any{"url": want}})
		case r.URL.Path == "/music/url":
			// 自建中转用 musicId，且是 POST body
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["musicId"] == nil || body["musicId"] == "" {
				writeJSON(w, 400, map[string]any{"code": 400, "message": "缺 musicId"})
				return
			}
			writeJSON(w, 200, map[string]any{"code": 200, "url": want})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	for _, p := range []string{ProtocolLxScript, ProtocolLxServer, ProtocolLxAPI} {
		got, err := Resolve(p, srv.URL, "", "wy", "421423808", "320k")
		if err != nil {
			t.Errorf("%s 取链失败: %v", p, err)
			continue
		}
		if got != want {
			t.Errorf("%s 得到 %q，期望 %q", p, got, want)
		}
	}
}

func TestResolveValidation(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"url": "x"})
	}))
	defer srv.Close()

	cases := []struct {
		name, protocol, platform, songID, wantSubstr string
	}{
		{"未知协议", "bogus", "wy", "1", "未知协议"},
		{"不支持的平台", ProtocolLxScript, "mg", "1", "不支持的平台"},
		{"缺歌曲 ID", ProtocolLxScript, "wy", "", "缺少歌曲 ID"},
	}
	for _, c := range cases {
		_, err := Resolve(c.protocol, srv.URL, "", c.platform, c.songID, "320k")
		if err == nil {
			t.Errorf("%s: 应当报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("%s: 错误 %q 应含 %q", c.name, err.Error(), c.wantSubstr)
		}
	}
}

// 上游返回错误时要把状态码与响应带出来，便于排查
func TestResolveSurfacesUpstreamError(t *testing.T) {
	noPrivateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 401, map[string]any{"message": "INVALID TOKEN"})
	}))
	defer srv.Close()

	_, err := Resolve(ProtocolLxScript, srv.URL, "", "wy", "1", "320k")
	if err == nil {
		t.Fatal("上游 401 时应报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误应带状态码：%v", err)
	}
}

func TestMapPlatform(t *testing.T) {
	cases := map[string]string{
		"wy": "netease", "netease": "netease",
		"tx": "qq", "qq": "qq",
		"kg": "kugou", "kugou": "kugou",
		"kw": "kuwo", "kuwo": "kuwo",
		"mg": "", "": "",
	}
	for in, want := range cases {
		if got := mapPlatform(in); got != want {
			t.Errorf("mapPlatform(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestNormalizeProtocol(t *testing.T) {
	cases := map[string]string{
		"lx_script": ProtocolLxScript,
		"LX_SERVER": ProtocolLxServer,
		" lx_api ":  ProtocolLxAPI,
		"bogus":     "",
		"":          "",
	}
	for in, want := range cases {
		if got := NormalizeProtocol(in); got != want {
			t.Errorf("NormalizeProtocol(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestFirstURL(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"顶层 url", map[string]any{"url": "a"}, "a"},
		{"data 里 url", map[string]any{"data": map[string]any{"url": "b"}}, "b"},
		{"带空白", map[string]any{"url": "  c  "}, "c"},
		{"都没有", map[string]any{"code": 200}, ""},
		{"url 为空串", map[string]any{"url": ""}, ""},
	}
	for _, c := range cases {
		if got := firstURL(c.in); got != c.want {
			t.Errorf("%s: 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
}

// 内网自建音源**不需要任何环境变量**即可接入与取链。
//
// 这条固化的是一次真实事故：原先 pkg/apisource 用 security.DefaultOptions()，被
// FN_ALLOW_PRIVATE_NET 的默认拒绝卡住 —— 内网音源连「填地址 → 探测协议」都过不去，
// 而飞牛 FPK 并没有给用户配环境变量的入口，等于服务型音源在内网环境下完全不可用。
// 判据与 pkg/ai 的 AI 服务地址一致：地址来自用户显式配置，不是请求参数，不存在 SSRF 面。
func TestPrivateNetAllowedWithoutEnv(t *testing.T) {
	noPrivateEnv(t)

	// ① 校验层：回环地址必须能通过（否则「音源管理」里根本加不进去）
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://192.168.1.9:3000"} {
		if _, err := normalizeBase(raw); err != nil {
			t.Errorf("normalizeBase(%q) 不应报错，得到 %v", raw, err)
		}
	}

	// ② 请求层：真的能打到 127.0.0.1 上的服务
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/music/url" {
			writeJSON(w, 200, map[string]any{"url": "https://cdn.example.com/a.flac"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := Detect(srv.URL, ""); err != nil {
		t.Fatalf("未设 FN_ALLOW_PRIVATE_NET 时应能探测内网音源，得到 %v", err)
	}
	if _, err := Resolve(ProtocolLxServer, srv.URL, "", "wy", "123", "320k"); err != nil {
		t.Fatalf("未设 FN_ALLOW_PRIVATE_NET 时应能从内网音源取链，得到 %v", err)
	}
}
