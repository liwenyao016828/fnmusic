package charts

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// rewriteTransport 把**任意**绝对 URL 的请求都转给桩服务器。
//
// ⚠️ 不能只用 `srv.Client()`：它只给客户端装上测试服务器的 TLS 配置，
// **不会改写 host** —— 被测代码写的是 `https://c.y.qq.com/...`，
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

// newQQStub 起一个桩服务器，返回给定的响应体，并把收到的请求路径记到 gotPath。
func newQQStub(t *testing.T, status int, body string, gotPath *string) *http.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotPath != nil {
			*gotPath = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &rewriteTransport{target: srv.URL, base: http.DefaultTransport}}
}

// qqNewShapeBody 是**当前**QQ 接口的响应形状：
// cdlist 在 `data` 下，曲目字段是 name / mid / album.{name,mid}。
const qqNewShapeBody = `{
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

// qqPrivacyBody 是 QQ 对**非公开歌单 / 匿名请求**的实际响应（2026-09-21 实测）。
const qqPrivacyBody = `{"code":0,"subcode":4000,"msg":"check privacy error!"}`

func TestFetchQQPlaylistDetailParsesNewShape(t *testing.T) {
	var gotPath string
	client := newQQStub(t, http.StatusOK, qqNewShapeBody, &gotPath)

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
		t.Errorf("专辑字段解析错（新版是 album.name / album.mid）：%+v", s)
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
}

// 这条是**回归测试的重点**：必须打到新域名，不能再退回那个已经失效的接口。
func TestFetchQQPlaylistDetailUsesV8Endpoint(t *testing.T) {
	var gotPath string
	client := newQQStub(t, http.StatusOK, qqNewShapeBody, &gotPath)

	if _, err := FetchQQPlaylistDetail(client, "7707261125"); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if !strings.Contains(gotPath, "fcg_v8_playlist_cp.fcg") {
		t.Errorf("请求路径 = %q，期望含 fcg_v8_playlist_cp.fcg", gotPath)
	}
	if strings.Contains(gotPath, "fcg_ucc_getcdinfo") {
		t.Errorf("又退回已失效的老接口了：%q（它对匿名请求返回 check privacy error）", gotPath)
	}
}

// 只换 URL 不换字段名 → 会「不报错但空歌单」，这里把它钉成错误。
func TestFetchQQPlaylistDetailRejectsOldShape(t *testing.T) {
	client := newQQStub(t, http.StatusOK, qqOldShapeBody, nil)

	if _, err := FetchQQPlaylistDetail(client, "7707261125"); err == nil {
		t.Fatal("旧字段形状（顶层 cdlist + songname）应报错，实际解析成功了 —— " +
			"说明字段名没跟着接口一起改，会静默拿到空歌单")
	}
}

// 非公开歌单要给出**用户能采取行动**的话，而不是「解析失败」。
func TestFetchQQPlaylistDetailExplainsPrivacyError(t *testing.T) {
	client := newQQStub(t, http.StatusOK, qqPrivacyBody, nil)

	_, err := FetchQQPlaylistDetail(client, "7707261125")
	if err == nil {
		t.Fatal("上游返回 check privacy error 时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "公开") {
		t.Errorf("错误信息要说明「不是公开歌单」，实际：%q", msg)
	}
	if strings.Contains(msg, "Failed to parse") {
		t.Errorf("不该把内部解析细节当用户文案：%q", msg)
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
