package charts

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLooksLikeURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://music.163.com/#/playlist?id=123", true},
		{"http://y.qq.com/n/ryqq/playlist/123", true},
		{"周杰伦 晴天", false},
		{"", false},
		{"music.163.com/playlist/123", false}, // 没有 scheme，当关键词处理
		{"https://a.com/x y", false},          // 带空格，当关键词
	}
	for _, c := range cases {
		if got := LooksLikeURL(c.in); got != c.want {
			t.Errorf("LooksLikeURL(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestBuildPlaylistLinkRoundTrip(t *testing.T) {
	// 核心判据：**拼出来的链接必须能被解析回同一对 (source, id)** ——
	// 否则「把裸 id 拼成链接走公开接口」这条捷径会静默失效。
	cases := []struct{ source, id, wantSource string }{
		{"wy", "19723756", "wy"},
		{"wy", "3778678", "wy"},
		{"netease", "123456", "wy"},
		{"tx", "7011264340", "tx"},
		{"qq", "7890123", "tx"},
		{"WY", "123456", "wy"}, // 大小写不敏感
	}
	for _, c := range cases {
		link := BuildPlaylistLink(c.source, c.id)
		if link == "" {
			t.Fatalf("BuildPlaylistLink(%q,%q) 不该返回空串", c.source, c.id)
		}
		gotSource, gotID, err := ParsePlaylistLink(link)
		if err != nil {
			t.Fatalf("拼出来的链接解析失败: link=%q err=%v", link, err)
		}
		if gotSource != c.wantSource || gotID != c.id {
			t.Errorf("往返不一致: BuildPlaylistLink(%q,%q)=%q → 解析回 (%q,%q)，期望 (%q,%q)",
				c.source, c.id, link, gotSource, gotID, c.wantSource, c.id)
		}
	}
}

func TestBuildPlaylistLinkRejectsUnsupported(t *testing.T) {
	// 认不出的平台 / 非法 id 一律返回空串 —— 调用方据此走「按 ID」的老路径并给出准确报错，
	// **不要**拼一条下游必然解析不了的链接（那会把「不支持」变成「链接失效」）。
	cases := []struct{ source, id string }{
		{"kg", "1234567"},    // 酷狗公开歌单接口形态不同，暂不支持
		{"kw", "98765"},      // 酷我同上
		{"", "123456"},       // 缺平台
		{"wy", ""},           // 缺 id
		{"wy", "abc"},        // id 不是纯数字
		{"wy", "123 456"},    // 含空格
		{"unknown", "12345"}, // 未知平台
	}
	for _, c := range cases {
		if got := BuildPlaylistLink(c.source, c.id); got != "" {
			t.Errorf("BuildPlaylistLink(%q,%q) 应返回空串，实际 %q", c.source, c.id, got)
		}
	}
}

func TestParsePlaylistLink(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantSource string
		wantID     string
	}{
		// 网易云：网页版是 /#/playlist?id=xxx，id 在 fragment 里
		{"网易云 fragment + query", "https://music.163.com/#/playlist?id=123456", "wy", "123456"},
		{"网易云 path 形式", "https://music.163.com/playlist/123456", "wy", "123456"},
		{"网易云 query 形式", "https://music.163.com/playlist?id=123456", "wy", "123456"},
		{"网易云带多余参数", "https://music.163.com/#/playlist?id=123456&userid=9&from=qr", "wy", "123456"},
		{"网易云 toplist", "https://music.163.com/#/discover/toplist?id=3778678", "wy", "3778678"},

		// QQ 音乐
		{"QQ 网页版", "https://y.qq.com/n/ryqq/playlist/7890123", "tx", "7890123"},
		{"QQ 分享页 id 参数", "https://i.y.qq.com/n2/m/share/details/taoge.html?id=7890123", "tx", "7890123"},
		{"QQ disstid", "https://y.qq.com/n/ryqq/playlist?disstid=7890123", "tx", "7890123"},

		// 酷狗
		{"酷狗 special/single", "https://www.kugou.com/yy/special/single/1234567.html", "kg", "1234567"},
		{"酷狗 specialid", "https://www.kugou.com/yy/special/single/?specialid=1234567", "kg", "1234567"},

		// 酷我
		{"酷我 playlist_detail", "https://www.kuwo.cn/playlist_detail/98765", "kw", "98765"},
		{"酷我 pid", "https://www.kuwo.cn/playlist/index?pid=98765", "kw", "98765"},
	}

	for _, c := range cases {
		source, id, err := ParsePlaylistLink(c.url)
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c.name, err)
			continue
		}
		if source != c.wantSource || id != c.wantID {
			t.Errorf("%s: 得到 (%s,%s)，期望 (%s,%s)", c.name, source, id, c.wantSource, c.wantID)
		}
	}
}

// 识别不出来的情况要给**能看懂的中文指引**，而不是空 source 去撞下游报错
func TestParsePlaylistLinkErrors(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantSubstr string
	}{
		{"空链接", "", "链接为空"},
		{"不是链接", "随便一句话", "不是"},
		{"不认识的平台", "https://example.com/playlist/123", "暂不认识这个平台"},
		{"网易云但没 id", "https://music.163.com/#/playlist", "没能从里面找到歌单 id"},
		{"QQ 但没 id", "https://y.qq.com/n/ryqq/playlist", "没能从里面找到歌单 id"},
	}
	for _, c := range cases {
		_, _, err := ParsePlaylistLink(c.url)
		if err == nil {
			t.Errorf("%s: 应当报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("%s: 错误信息 %q 应包含 %q", c.name, err.Error(), c.wantSubstr)
		}
	}
}

// 子域也要认（y.qq.com / c6.y.qq.com / m.kugou.com 这类）
func TestParsePlaylistLinkSubdomains(t *testing.T) {
	cases := []struct{ url, source string }{
		{"https://c6.y.qq.com/n/ryqq/playlist/111", "tx"},
		{"https://m.kugou.com/yy/special/single/222.html", "kg"},
		{"https://m.kuwo.cn/playlist_detail/333", "kw"},
	}
	for _, c := range cases {
		source, id, err := ParsePlaylistLink(c.url)
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c.url, err)
			continue
		}
		if source != c.source || id == "" {
			t.Errorf("%s: 得到 (%s,%s)，期望 source=%s 且有 id", c.url, source, id, c.source)
		}
	}
}

// 伪造 host 不能被当成真平台（防止 example.com/music.163.com/... 这类绕过）
func TestParsePlaylistLinkRejectsLookalikeHost(t *testing.T) {
	if _, _, err := ParsePlaylistLink("https://evil.com/music.163.com/playlist/123"); err == nil {
		t.Error("把平台域名放在 path 里不应被识别为网易云")
	}
}

// ── 分享短链 ──
//
// 背景：手机 App「复制链接」给的是短链，path 里是短码不是 id。
// 实测这三种形态在修复前全部返回 400：
//
//	https://163cn.tv/abcXYZ12                      网易云
//	https://c6.y.qq.com/base/fcgi-bin/u?__=abc123  QQ 音乐
//	https://t1.kugou.com/abc123                    酷狗

// roundTripFunc 让测试能伪造 HTTP 响应，完全不碰网络
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestLooksLikeShortLink(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://163cn.tv/abcXYZ12", true},
		{"https://t1.kugou.com/abc123", true},
		{"https://t2.kugou.com/abc123", true},
		{"https://c6.y.qq.com/base/fcgi-bin/u?__=abc123", true},
		// 下面这些是「形态规整」的链接，不该走短链展开（省一次出站请求）
		{"https://music.163.com/#/playlist?id=123", false},
		{"https://y.qq.com/n/ryqq/playlist/123", false},
		{"https://c6.y.qq.com/n/ryqq/playlist/123", false}, // 同域但不是 fcgi 短链路径
		{"https://example.com/x", false},
		{"", false},
		{"周杰伦 晴天", false},
	}
	for _, c := range cases {
		if got := looksLikeShortLink(c.in); got != c.want {
			t.Errorf("looksLikeShortLink(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// 短链应被展开后正确解析出平台与 id
func TestResolvePlaylistLinkExpandsShortLink(t *testing.T) {
	const finalURL = "https://music.163.com/#/playlist?id=999888"

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "music.163.com" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}
			h := make(http.Header)
			h.Set("Location", finalURL)
			return &http.Response{
				StatusCode: http.StatusFound,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     h,
				Request:    req,
			}, nil
		}),
	}

	// 同包测试可直接改包级白名单，把测试用域放进去
	old := shortLinkHosts
	shortLinkHosts = []string{"163cn.tv"}
	defer func() { shortLinkHosts = old }()

	src, id, err := ResolvePlaylistLink("https://163cn.tv/abcXYZ12", client)
	if err != nil {
		t.Fatalf("短链展开失败：%v", err)
	}
	if src != "wy" || id != "999888" {
		t.Errorf("短链解析结果 = (%q, %q)，期望 (wy, 999888)", src, id)
	}
}

// 普通链接解析失败时**不该**发起网络请求 —— 否则用户随便粘个错链接都会打一次外网
func TestResolvePlaylistLinkDoesNotHitNetworkForPlainLink(t *testing.T) {
	called := false
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("不该发起网络请求")
		}),
	}

	// 能识别平台，但里面确实没有歌单 id（专辑链接），且不是短链
	_, _, err := ResolvePlaylistLink("https://music.163.com/album/123", client)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if called {
		t.Error("普通链接解析失败时不应发起网络请求")
	}
}

// 短链展开后仍认不出平台时，错误信息要说清是「展开后仍无法识别」，而不是原始错误
func TestResolvePlaylistLinkShortLinkToUnknownHost(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "163cn.tv" {
				h := make(http.Header)
				h.Set("Location", "https://unknown-site.example/x")
				return &http.Response{
					StatusCode: http.StatusFound,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     h,
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	old := shortLinkHosts
	shortLinkHosts = []string{"163cn.tv"}
	defer func() { shortLinkHosts = old }()

	_, _, err := ResolvePlaylistLink("https://163cn.tv/abcXYZ12", client)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "展开后的地址不是歌单页") {
		t.Errorf("错误信息应说明是短链展开后无法识别，实际：%v", err)
	}
}

// 酷狗类短链不重定向，而是返回 JSON 把真实地址放在 data 里 —— 必须也能展开
func TestResolvePlaylistLinkExpandsJSONShortLink(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// 不重定向，直接返回 JSON
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"status":0,"err_code":0,"type":0,"data":"https://www.kugou.com/yy/special/single/1234567.html","err_msg":""}`)),
				Header:  make(http.Header),
				Request: req,
			}, nil
		}),
	}

	old := shortLinkHosts
	shortLinkHosts = []string{"t1.kugou.com"}
	defer func() { shortLinkHosts = old }()

	src, id, err := ResolvePlaylistLink("https://t1.kugou.com/abcXYZ", client)
	if err != nil {
		t.Fatalf("JSON 短链展开失败：%v", err)
	}
	if src != "kg" || id != "1234567" {
		t.Errorf("JSON 短链解析结果 = (%q, %q)，期望 (kg, 1234567)", src, id)
	}
}

// JSON 里没有地址（如短码失效返回 data:null）时，应给出明确错误而不是崩
func TestResolvePlaylistLinkJSONShortLinkWithoutURL(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"status":0,"data":null,"err_msg":""}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	old := shortLinkHosts
	shortLinkHosts = []string{"t1.kugou.com"}
	defer func() { shortLinkHosts = old }()

	_, _, err := ResolvePlaylistLink("https://t1.kugou.com/abcXYZ", client)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "没有返回跳转地址") {
		t.Errorf("错误信息应说明短链未返回地址，实际：%v", err)
	}
}
