package charts

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// rewriteTransport 把**任意**绝对 URL 的请求都转给桩服务器。
//
// ⚠️ 不能只用 `srv.Client()`：它只给客户端装上测试服务器的 TLS 配置，
// **不会改写 host** —— 被测代码写的是 `https://u.y.qq.com/...` / `https://c.y.qq.com/...`，
// 于是请求会真的打到公网（第一次写测试时就是这么被骗的：断言看到的是线上真实歌单的 66 首）。
type rewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	return t.base.RoundTrip(r)
}

// qqHit 是桩服务器收到的一次请求。
//
// 为什么连 body 一起记：musicu 是 POST JSON，接口名在 body 里而不在 URL 里；
// 只断言 path 的话，「打到新域名但 body 还是老模块」这种回归就漏掉了。
type qqHit struct {
	Method string
	Path   string
	Body   string
}

// newQQStub 起桩服务器；respond 按请求内容返回状态码与响应体（这样能模拟翻页）。
// 返回的第二个函数取到目前为止收到的全部请求。
func newQQStub(t *testing.T, respond func(h qqHit) (status int, body string)) (*http.Client, func() []qqHit) {
	t.Helper()
	var (
		mu   sync.Mutex
		hits []qqHit
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		h := qqHit{Method: r.Method, Path: r.URL.Path, Body: string(raw)}

		mu.Lock()
		hits = append(hits, h)
		mu.Unlock()

		status, body := respond(h)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return &http.Client{Transport: &rewriteTransport{target: srv.URL, base: http.DefaultTransport}},
		func() []qqHit {
			mu.Lock()
			defer mu.Unlock()
			out := make([]qqHit, len(hits))
			copy(out, hits)
			return out
		}
}

// newStaticQQStub 是对上面那个的简化：所有请求都回同一个响应体。
func newStaticQQStub(t *testing.T, status int, body string) (*http.Client, func() []qqHit) {
	t.Helper()
	return newQQStub(t, func(qqHit) (int, string) { return status, body })
}

// ── 响应样本（都是 2026-10-23 抓到的真实形状，只删了无关字段） ──

const qqMusicuSongA = `{"name":"你的","mid":"002xTzGb2UBQRk","interval":163,` +
	`"album":{"name":"你的","mid":"0023VbHy1oT80v"},` +
	`"singer":[{"name":"DouDou"},{"name":"Viva宋佩豫"}]}`

const qqMusicuSongB = `{"name":"第二首","mid":"003aaaaaaaaaaa","interval":200,` +
	`"album":{"name":"","mid":""},"singer":[]}`

const qqMusicuSongD = `{"name":"第四首","mid":"005ddddddddddd","interval":210,` +
	`"album":{"name":"","mid":""},"singer":[]}`

const qqMusicuSongC = `{"name":"第三首","mid":"004bbbbbbbbbbb","interval":180,` +
	`"album":{"name":"别的专辑","mid":"004ccccccccccc"},"singer":[{"name":"某某"}]}`

// qqMusicuBodyWith 拼一个 musicu.fcg 的响应。
// total 是 total_song_num，hasmore 是上游的「还有下一批吗」。
func qqMusicuBodyWith(total, hasmore int, songs ...string) string {
	return fmt.Sprintf(`{"code":0,"req":{"code":0,"data":{`+
		`"code":0,"subcode":0,"msg":"",`+
		`"total_song_num":%d,"hasmore":%d,`+
		`"dirinfo":{"id":7707261125,"title":"甜度爆表 &amp; 旋律说唱",`+
		`"picurl":"http://qpic.y.qq.com/music_cover/abc/300?n=1",`+
		`"desc":"不怕rapper有文化","host_nick":"我想要两颗西柚","songnum":%d},`+
		`"songlist":[%s]}}}`, total, hasmore, total, strings.Join(songs, ","))
}

// qqMusicuPrivacyBody 是 musicu 通道对「不让匿名看」的响应形状（privacy 校验失败）。
const qqMusicuPrivacyBody = `{"code":0,"req":{"code":0,"data":{"code":0,"subcode":4000,"msg":"check privacy error!"}}}`

// qqMusicuNotFoundBody 是 musicu 对**不存在**的歌单的真实响应（2026-10-23 实测）。
const qqMusicuNotFoundBody = `{"code":0,"req":{"code":0,"data":{"code":-100006,"msg":"get diss info from tmem error"}}}`

// qqV8NewShapeBody 是 v8 兜底通道的成功形状：cdlist 在 `data` 下，
// 曲目字段是 name / mid / album.{name,mid}。
const qqV8NewShapeBody = `{
  "code": 0,
  "subcode": 0,
  "data": {
    "cdlist": [{
      "disstid": "7707261125",
      "dissname": "甜度爆表 &amp; 旋律说唱",
      "logo": "http://qpic.y.qq.com/music_cover/abc/300?n=1",
      "desc": "不怕rapper有文化",
      "songlist": [{
        "name": "你的",
        "mid": "002xTzGb2UBQRk",
        "interval": 163,
        "album": {"name": "你的", "mid": "0023VbHy1oT80v"},
        "singer": [{"name": "DouDou"}, {"name": "Viva宋佩豫"}]
      }, {
        "name": "第二首",
        "mid": "003aaaaaaaaaaa",
        "interval": 200,
        "album": {"name": "", "mid": ""},
        "singer": []
      }]
    }]
  }
}`

// qqV8NotFoundBody 是 v8 对不存在/非数字歌单 id 的真实响应（2026-10-23 实测）。
const qqV8NotFoundBody = `{"code":10,"data":{"cdlist":[],"cdnum":0,"realcdnum":0},"login_check":"0","subcode":0}`

// qqOldShapeBody 是**改接口之前**的形状（顶层 cdlist + songname/songmid）。
// 它必须**报错**而不是「解析成空歌单」—— 否则只换 URL 不换字段名会静默失败。
const qqOldShapeBody = `{
  "code": 0,
  "cdlist": [{
    "disstid": "7707261125",
    "dissname": "旧形状",
    "songlist": [{"songname": "你的", "songmid": "002xTzGb2UBQRk", "interval": 163}]
  }]
}`

// ── 用例 ──

// 首选通道（musicu）要能解析出归一化后的字段。
func TestFetchQQPlaylistDetailParsesMusicuShape(t *testing.T) {
	client, hits := newStaticQQStub(t, http.StatusOK, qqMusicuBodyWith(2, 0, qqMusicuSongA, qqMusicuSongB))

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	if pl.ID != "7707261125" {
		t.Errorf("ID = %q，期望 7707261125", pl.ID)
	}
	// HTML 实体要解码
	if pl.Name != "甜度爆表 & 旋律说唱" {
		t.Errorf("Name = %q，期望「甜度爆表 & 旋律说唱」", pl.Name)
	}
	if pl.Desc != "不怕rapper有文化" {
		t.Errorf("Desc = %q", pl.Desc)
	}
	// 封面要统一成 https
	if !strings.HasPrefix(pl.Cover, "https://") {
		t.Errorf("歌单封面应为 https，实际 %q", pl.Cover)
	}
	if len(pl.Songs) != 2 {
		t.Fatalf("曲目数 = %d，期望 2", len(pl.Songs))
	}

	s := pl.Songs[0]
	if s.Name != "你的" || s.Songmid != "002xTzGb2UBQRk" {
		t.Errorf("第一首 name/mid 解析错：%+v", s)
	}
	if s.AlbumName != "你的" || s.AlbumMid != "0023VbHy1oT80v" {
		t.Errorf("专辑字段解析错（musicu 是 album.name / album.mid）：%+v", s)
	}
	if s.Interval != 163 {
		t.Errorf("Interval = %d，期望 163", s.Interval)
	}
	if len(s.Singers) != 2 || s.Singers[0] != "DouDou" {
		t.Errorf("歌手解析错：%v", s.Singers)
	}
	if !strings.Contains(s.Cover, "0023VbHy1oT80v") {
		t.Errorf("有专辑 mid 时单曲封面应由它拼出，实际 %q", s.Cover)
	}

	// 没有专辑 mid → 回落到歌单封面，不能是空串
	second := pl.Songs[1]
	if second.Cover != pl.Cover {
		t.Errorf("没有专辑 mid 时应回落到歌单封面，实际 %q（歌单封面 %q）", second.Cover, pl.Cover)
	}
	if len(second.Singers) != 0 {
		t.Errorf("空歌手数组不该被填东西，实际 %v", second.Singers)
	}

	// 成功时不该再去打兜底通道
	if got := hits(); len(got) != 1 {
		t.Fatalf("成功路径只该请求 1 次，实际 %d 次：%+v", len(got), got)
	}
}

// 这条是**回归测试的重点**：必须打 musicu.fcg 的 aiDissInfo 模块，
// 且**绝不能**退回那个对匿名请求恒返回 check privacy error 的老 qzone 接口。
func TestFetchQQPlaylistDetailUsesMusicuNotLegacy(t *testing.T) {
	client, hits := newStaticQQStub(t, http.StatusOK, qqMusicuBodyWith(2, 0, qqMusicuSongA, qqMusicuSongB))

	if _, err := FetchQQPlaylistDetail(client, "7707261125"); err != nil {
		t.Fatalf("请求失败：%v", err)
	}

	got := hits()
	if len(got) == 0 {
		t.Fatal("没有发出任何请求")
	}
	first := got[0]
	if first.Path != "/cgi-bin/musicu.fcg" {
		t.Errorf("首个请求路径 = %q，期望 /cgi-bin/musicu.fcg", first.Path)
	}
	if first.Method != http.MethodPost {
		t.Errorf("musicu 要用 POST，实际 %s", first.Method)
	}
	if !strings.Contains(first.Body, "music.srfDissInfo.aiDissInfo") {
		t.Errorf("请求体里没有 aiDissInfo 模块名：%q", first.Body)
	}
	if !strings.Contains(first.Body, "uniform_get_Dissinfo") {
		t.Errorf("请求体里没有 uniform_get_Dissinfo 方法名：%q", first.Body)
	}
	// disstid 必须是数字（上游只认 int）
	if !strings.Contains(first.Body, `"disstid":7707261125`) {
		t.Errorf("disstid 应以数字传：%q", first.Body)
	}
	if !strings.Contains(first.Body, `"song_begin":0`) {
		t.Errorf("首页应从 song_begin=0 开始：%q", first.Body)
	}

	for _, h := range got {
		if strings.Contains(h.Path, "fcg_ucc_getcdinfo") || strings.Contains(h.Path, "qzone") {
			t.Errorf("又退回已失效的老接口了：%q（它对匿名请求恒返回 check privacy error）", h.Path)
		}
	}
}

// 超过一页（1000 首）必须靠 song_begin 翻页拿全，不能只拿第一页。
func TestFetchQQPlaylistDetailPaginates(t *testing.T) {
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if strings.Contains(h.Body, `"song_begin":2`) {
			return http.StatusOK, qqMusicuBodyWith(3, 0, qqMusicuSongC)
		}
		return http.StatusOK, qqMusicuBodyWith(3, 1, qqMusicuSongA, qqMusicuSongB)
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("翻页解析失败：%v", err)
	}
	if len(pl.Songs) != 3 {
		t.Fatalf("曲目数 = %d，期望 3（两页拼起来）", len(pl.Songs))
	}
	if pl.Songs[2].Songmid != "004bbbbbbbbbbb" {
		t.Errorf("第二页的曲目没被拼进来：%+v", pl.Songs[2])
	}

	got := hits()
	if len(got) != 2 {
		t.Fatalf("应请求 2 次（两页），实际 %d 次", len(got))
	}
	if !strings.Contains(got[1].Body, `"song_begin":2`) {
		t.Errorf("第二次请求应带 song_begin=2：%q", got[1].Body)
	}
}

// musicu 被拦（这里用 500 + 非 JSON 模拟）时要自动退 v8，而不是直接把错误甩给用户。
func TestFetchQQPlaylistDetailFallsBackToV8(t *testing.T) {
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			return http.StatusInternalServerError, "<html>risk control</html>"
		}
		return http.StatusOK, qqV8NewShapeBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("musicu 失败后应退 v8 并成功，实际报错：%v", err)
	}
	if len(pl.Songs) != 2 || pl.Songs[0].Songmid != "002xTzGb2UBQRk" {
		t.Errorf("v8 兜底的解析结果不对：%+v", pl.Songs)
	}

	got := hits()
	if len(got) != 2 {
		t.Fatalf("应先 musicu 再 v8，共 2 次请求，实际 %d 次", len(got))
	}
	if got[0].Path != "/cgi-bin/musicu.fcg" {
		t.Errorf("首个请求应为 musicu，实际 %q", got[0].Path)
	}
	if !strings.Contains(got[1].Path, "fcg_v8_playlist_cp.fcg") {
		t.Errorf("兜底请求应打 v8，实际 %q", got[1].Path)
	}
}

// 只换 URL 不换字段名 → 会「不报错但空歌单」，这里把它钉成错误。
func TestFetchQQPlaylistDetailRejectsOldShape(t *testing.T) {
	client, _ := newStaticQQStub(t, http.StatusOK, qqOldShapeBody)

	if _, err := FetchQQPlaylistDetail(client, "7707261125"); err == nil {
		t.Fatal("旧字段形状（顶层 cdlist + songname）应报错，实际解析成功了 —— " +
			"说明字段名没跟着接口一起改，会静默拿到空歌单")
	}
}

// 非公开歌单要给出**用户能采取行动**的话，而不是「解析失败」，更不能说「歌单已删除」。
func TestFetchQQPlaylistDetailExplainsPrivacyError(t *testing.T) {
	client, _ := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			return http.StatusOK, qqMusicuPrivacyBody
		}
		return http.StatusOK, `{"code":0,"subcode":4000,"msg":"check privacy error!"}`
	})

	_, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatal("上游返回 check privacy error 时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "公开") {
		t.Errorf("错误信息要说明「不是公开歌单」，实际：%q", msg)
	}
	if !strings.Contains(msg, "匿名") {
		t.Errorf("错误信息要说清是「拒绝匿名读取」，实际：%q", msg)
	}
	// 用户 2026-10-23 明确说过「歌单没有删除、是公开的」，所以这条分支不许再提「已删除」
	if strings.Contains(msg, "已删除") {
		t.Errorf("上游只是拒绝匿名读取，不该说歌单被删了：%q", msg)
	}
	if strings.Contains(msg, "Failed to parse") {
		t.Errorf("不该把内部解析细节当用户文案：%q", msg)
	}
}

// 上游**明确**说没有这个歌单时（v8 code=10 / musicu -100006），才允许说「可能已删除」。
func TestFetchQQPlaylistDetailReportsNotFound(t *testing.T) {
	client, _ := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			return http.StatusOK, qqMusicuNotFoundBody
		}
		return http.StatusOK, qqV8NotFoundBody
	})

	_, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatal("两条通道都说没有这个歌单时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "找不到") {
		t.Errorf("应说明「找不到这个歌单」，实际：%q", msg)
	}
	if !strings.Contains(msg, "7707261125") {
		t.Errorf("错误信息要带上歌单 id，方便排查：%q", msg)
	}
	// 两个上游原话都要露出来，排障时能直接对账
	if !strings.Contains(msg, "-100006") || !strings.Contains(msg, "code=10") {
		t.Errorf("应保留两条通道各自的原话：%q", msg)
	}
}

// 真的 0 首歌的空歌单 **不是** 错误：musicu 会返回 dirinfo（有标题）但 songlist 为空。
// 把它当失败会误导用户，也会让 push 的「空歌单」提示永远走不到。
func TestFetchQQPlaylistDetailEmptyPlaylistIsNotAnError(t *testing.T) {
	client, _ := newStaticQQStub(t, http.StatusOK, qqMusicuBodyWith(0, 0))

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("空歌单不该报错：%v", err)
	}
	if len(pl.Songs) != 0 {
		t.Errorf("曲目数应为 0，实际 %d", len(pl.Songs))
	}
	if pl.Name == "" {
		t.Error("空歌单也要带回标题")
	}
}

// 返回 HTML（风控/登录页）要和「网络不通」分开说，且提示里带 「JSON」 便于用户判断。
func TestFetchQQPlaylistDetailExplainsHTMLInterception(t *testing.T) {
	client, _ := newStaticQQStub(t, http.StatusOK, "<html><body>请登录</body></html>")

	_, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatal("拿到 HTML 时应报错")
	}
	if msg := err.Error(); !strings.Contains(msg, "JSON") {
		t.Errorf("应说明返回的不是有效 JSON：%q", msg)
	}
}

func TestFetchQQPlaylistDetailEmptyID(t *testing.T) {
	if _, err := FetchQQPlaylistDetail(nil, "   "); err == nil {
		t.Fatal("空 id 应报错")
	}
}

// nil client 不能 panic（调用方可能懒得传）。
func TestFetchQQPlaylistDetailNilClientFallsBack(t *testing.T) {
	if defaultQQClient == nil {
		t.Fatal("defaultQQClient 不能为 nil，否则 nil client 调用会 panic")
	}
	if defaultQQClient.Timeout <= 0 {
		t.Error("defaultQQClient 必须设超时，上游会挂")
	}
}

// HTTP 429（限流/风控）即使带着看似正常的 JSON body，也不能当成功。
func TestFetchQQPlaylistDetailRejectsHTTPError(t *testing.T) {
	client, _ := newStaticQQStub(t, http.StatusTooManyRequests, qqMusicuBodyWith(1, 0, qqMusicuSongA))
	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatalf("HTTP 429 必须报错，不能被当成成功：%+v", pl)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("错误里应带上 HTTP 429：%v", err)
	}
}

// 错误码可能只出现在外层：只看 data.code 会漏掉整包风控。
func TestFetchQQPlaylistDetailSurfacesEnvelopeError(t *testing.T) {
	cases := []struct {
		name   string
		musicu string
		want   string
	}{
		{"顶层 code", `{"code":-1000,"req":{"code":0,"data":{"code":0,"msg":""}}}`, "-1000"},
		{"req.code", `{"code":0,"req":{"code":-1001,"data":{"code":0,"msg":""}}}`, "-1001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newQQStub(t, func(h qqHit) (int, string) {
				if h.Path == "/cgi-bin/musicu.fcg" {
					return http.StatusOK, tc.musicu
				}
				return http.StatusOK, qqV8NotFoundBody
			})
			_, err := FetchQQPlaylistDetail(client, "7707261125")
			if err == nil {
				t.Fatal("外层 code 非 0 时应报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误里应带上上游 code %s：%v", tc.want, err)
			}
		})
	}
}

// 上游忽略 song_begin（一直回同一页）时，不能把「只读到 2/5 首」当成功。
func TestFetchQQPlaylistDetailRepeatedPageIsNotSilentSuccess(t *testing.T) {
	page := qqMusicuBodyWith(5, 1, qqMusicuSongA, qqMusicuSongB)
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			return http.StatusOK, page
		}
		return http.StatusOK, qqV8NotFoundBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatalf("重复页只读到 2/5 首，必须报错而不是返回残缺歌单：%+v", pl)
	}
	msg := err.Error()
	if !strings.Contains(msg, "没读全") || !strings.Contains(msg, "2 首") || !strings.Contains(msg, "5 首") {
		t.Fatalf("错误应说明只读到 2/5 首：%v", msg)
	}
	if n := len(hits()); n != 3 {
		t.Fatalf("应为 musicu 两次 + v8 一次，实际请求 %d 次", n)
	}
}

// 重复页导致 musicu 不完整时，v8 能补齐就用 v8 的完整结果（不报错、不残缺）。
func TestFetchQQPlaylistDetailTruncatedFallsBackToV8(t *testing.T) {
	page := qqMusicuBodyWith(2, 1, qqMusicuSongA)
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			return http.StatusOK, page
		}
		return http.StatusOK, qqV8NewShapeBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("v8 能补齐时不该失败：%v", err)
	}
	if len(pl.Songs) != 2 || pl.Songs[1].Songmid != "003aaaaaaaaaaa" {
		t.Fatalf("应采用 v8 的完整 2 首，实际：%+v", pl.Songs)
	}
	if n := len(hits()); n != 3 {
		t.Fatalf("应为 musicu 两次 + v8 一次，实际请求 %d 次", n)
	}
}

// 页码交叠（第 2 页带回了第 1 页的 B）要跨页去重，不能重复计入。
func TestFetchQQPlaylistDetailDedupesOverlappingPages(t *testing.T) {
	client, _ := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			if strings.Contains(h.Body, `"song_begin":2`) {
				return http.StatusOK, qqMusicuBodyWith(4, 1, qqMusicuSongB, qqMusicuSongC, qqMusicuSongD)
			}
			return http.StatusOK, qqMusicuBodyWith(4, 1, qqMusicuSongA, qqMusicuSongB)
		}
		return http.StatusOK, qqV8NotFoundBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("交叠页去重后应能读全：%v", err)
	}
	got := make([]string, 0, len(pl.Songs))
	for _, s := range pl.Songs {
		got = append(got, s.Songmid)
	}
	want := []string{"002xTzGb2UBQRk", "003aaaaaaaaaaa", "004bbbbbbbbbbb", "005ddddddddddd"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("去重后曲目不对：got %v want %v", got, want)
	}
}

// hasmore 缺失时不能默认按 0（没有更多）处理，否则会静默截断。
func TestFetchQQPlaylistDetailMissingHasMoreKeepsPaging(t *testing.T) {
	page1 := `{"code":0,"req":{"code":0,"data":{"code":0,"total_song_num":3,` +
		`"dirinfo":{"id":7707261125,"title":"t"},"songlist":[` + qqMusicuSongA + `,` + qqMusicuSongB + `]}}}`
	page2 := `{"code":0,"req":{"code":0,"data":{"code":0,"total_song_num":3,` +
		`"dirinfo":{"id":7707261125,"title":"t"},"songlist":[` + qqMusicuSongC + `]}}}`
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			if strings.Contains(h.Body, `"song_begin":2`) {
				return http.StatusOK, page2
			}
			return http.StatusOK, page1
		}
		return http.StatusOK, qqV8NotFoundBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err != nil {
		t.Fatalf("缺 hasmore 时应靠 total 继续翻页读完：%v", err)
	}
	if len(pl.Songs) != 3 {
		t.Fatalf("应读到 3 首，实际 %d 首", len(pl.Songs))
	}
	if n := len(hits()); n != 2 {
		t.Fatalf("应只请求 musicu 两次（不需要 v8），实际 %d 次", n)
	}
}

// 翻到页数上限还没读完时，必须明确报错，不能把 60 页当完整歌单返回。
func TestFetchQQPlaylistDetailPageCapIsNotSilentSuccess(t *testing.T) {
	client, hits := newQQStub(t, func(h qqHit) (int, string) {
		if h.Path == "/cgi-bin/musicu.fcg" {
			var req struct {
				Req struct {
					Param struct {
						SongBegin int `json:"song_begin"`
					} `json:"param"`
				} `json:"req"`
			}
			if err := json.Unmarshal([]byte(h.Body), &req); err != nil {
				t.Errorf("请求体不是预期 JSON：%v", err)
				return http.StatusInternalServerError, "{}"
			}
			b := req.Req.Param.SongBegin
			return http.StatusOK, fmt.Sprintf(`{"code":0,"req":{"code":0,"data":{"code":0,`+
				`"dirinfo":{"id":7707261125,"title":"cap"},`+
				`"songlist":[{"name":"s%d","mid":"m%d","interval":1}]}}}`, b, b)
		}
		return http.StatusOK, qqV8NotFoundBody
	})

	pl, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatalf("翻到上限仍未读完时必须报错：%+v", pl)
	}
	msg := err.Error()
	if !strings.Contains(msg, "没读全") {
		t.Fatalf("错误应说明没读全：%v", msg)
	}
	if n := len(hits()); n != qqMusicuMaxPages+1 {
		t.Fatalf("应为 musicu %d 次 + v8 一次，实际 %d 次", qqMusicuMaxPages, n)
	}
}
