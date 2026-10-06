package intercept

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/online"
)

// 这一组盯的是「同源通道」：注入到官方页面里的脚本，只能从这条白名单通道
// 回到曲率后端。它是整个注入方案唯一的攻击面 —— 所以既要验「能用」，
// 也要验「白名单之外一步都走不出去」。

// localBackend 起一个假的「本机曲率后端」，记录收到的请求体。
func localBackend(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	seen := &[]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download/song" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["_path"] = r.URL.Path
		*seen = append(*seen, m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"file":"/vol3/音乐/甲.mp3"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// registerOnline 登记一首在线曲目并让它可解析，返回它的虚拟 id。
func registerOnline(h *harness, id, title, artist string) string {
	h.setOnline(map[string]bool{id: true}, song(id, title, artist))
	track := online.Track{
		Platform: "wy", PlatformID: id, Title: title, Artists: []string{artist},
		Album: "专辑", Duration: 245, CoverURL: "https://cdn.invalid/c.jpg",
	}.Normalized()
	return h.it.registry.Put(track)
}

func TestQulvDownloadOnlineResolvesAndPosts(t *testing.T) {
	h := newHarness(t)
	srv, seen := localBackend(t)
	h.it.localAPI = srv.URL

	fake := registerOnline(h, "w700", "甲", "A")

	w, handled := h.do(http.MethodPost, qulvAPIPrefix+"/download/online",
		`{"guid":"`+fake+`"}`, nil)
	if !handled {
		t.Fatal("同源下载接口应被拦截层处理")
	}

	env := decode(t, w)
	if got := asInt(env["code"]); got != 200 {
		t.Fatalf("应原样回后端的结果，得到 %+v", env)
	}
	if len(*seen) != 1 {
		t.Fatalf("本机后端应收到 1 个请求，实际 %d", len(*seen))
	}

	got := (*seen)[0]
	if got["_path"] != "/api/download/song" {
		t.Errorf("应转给落盘接口，实际 %v", got["_path"])
	}
	// 直链由**后端**解析，脚本只报「点了哪一首」—— 这条是设计的核心。
	if got["url"] != "https://cdn.invalid/w700.mp3" {
		t.Errorf("url 应来自解析池，得到 %v", got["url"])
	}
	if got["name"] != "甲" || got["singer"] != "A" || got["source"] != "wy" || got["songmid"] != "w700" {
		t.Errorf("落盘要的元数据不完整：%+v", got)
	}
	if got["duration"] != float64(245) {
		t.Errorf("duration 应为 245，得到 %v", got["duration"])
	}
}

func TestQulvDownloadOnlineRejectsUnknownGuid(t *testing.T) {
	h := newHarness(t)
	srv, seen := localBackend(t)
	h.it.localAPI = srv.URL

	// 一个形状合法但**不是我们发出去**的 id。必须明说认不出来，不能去猜 ——
	// 猜错会把毫不相干的歌写进用户的曲库。
	w, _ := h.do(http.MethodPost, qulvAPIPrefix+"/download/online",
		`{"guid":"`+officialGuid("ab")+`"}`, nil)

	if got := w.Code; got != http.StatusNotFound {
		t.Errorf("未登记的 id 应 404，得到 %d", got)
	}
	if len(*seen) != 0 {
		t.Errorf("认不出来时绝不能打本机后端，实际打了 %d 次", len(*seen))
	}
}

func TestQulvDownloadOnlineReportsUnplayable(t *testing.T) {
	h := newHarness(t)
	srv, seen := localBackend(t)
	h.it.localAPI = srv.URL

	// 登记了、但解析池说不可播。
	h.setOnline(map[string]bool{"w701": false},
		song("w701", "乙", "B"))
	track := online.Track{Platform: "wy", PlatformID: "w701", Title: "乙", Artists: []string{"B"}}.Normalized()
	fake := h.it.registry.Put(track)

	w, _ := h.do(http.MethodPost, qulvAPIPrefix+"/download/online", `{"guid":"`+fake+`"}`, nil)

	if got := w.Code; got != http.StatusBadGateway {
		t.Errorf("不可播应 502，得到 %d", got)
	}
	// 「不可播放」与「网络出错」必须分开说：前者是稳定结论，后者可以重试。
	if msg := asString(decode(t, w)["message"]); msg == "" || !strings.Contains(msg, "不可播放") {
		t.Errorf("应说明是平台不可播，得到 %q", msg)
	}
	if len(*seen) != 0 {
		t.Errorf("解析失败时不该去落盘，实际打了 %d 次", len(*seen))
	}
}

func TestQulvDownloadOnlineRejectsEmptyGUID(t *testing.T) {
	h := newHarness(t)
	srv, seen := localBackend(t)
	h.it.localAPI = srv.URL

	w, handled := h.do(http.MethodPost, qulvAPIPrefix+"/download/online", `{}`, nil)
	if !handled {
		t.Fatal("应被拦截层处理")
	}
	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("缺少 guid 应 400，得到 %d", got)
	}
	if len(*seen) != 0 {
		t.Errorf("参数不全时不该打后端，实际打了 %d 次", len(*seen))
	}
}

func TestQulvWhitelistBlocksEverythingElse(t *testing.T) {
	h := newHarness(t)
	srv, seen := localBackend(t)
	h.it.localAPI = srv.URL

	// `/api/download/song` 本身、以及任何别的路径，都**不能**从同源通道直通 ——
	// 否则这个前缀就等于把曲率后端整个暴露给页面（和放开 CORS 一样糟）。
	paths := []string{
		qulvPrefix + "/api/download/song",
		qulvPrefix + "/api/tidy/dedupe",
		qulvPrefix + "/api/config",
		qulvPrefix + "/anything",
		qulvPrefix + "/",
	}
	for _, p := range paths {
		w, handled := h.do(http.MethodPost, p, `{}`, nil)
		if !handled {
			t.Errorf("%s 应被兜底路由处理（而不是透传给官方）", p)
			continue
		}
		if got := w.Code; got != http.StatusNotFound {
			t.Errorf("%s 应 404，得到 %d", p, got)
		}
		// 官方会把未知路径当 SPA 路由返回 HTML，所以这里也必须确认上游没被碰。
		for _, call := range h.up.calls() {
			if strings.Contains(call, "_qulv") {
				t.Errorf("%s 不该转发给官方，实际调了 %s", p, call)
			}
		}
	}

	// 裸的 `/_qulv/...` 到不了这里：fnOS 的 nginx 只有 `location /music` 是指向
	// 入口 socket 的，`/_qulv/` 那棵树归接管层自己的 livez/healthz 用。
	// 这条断言把前缀固定住 —— 万一有人把它改回裸的 `/_qulv`，症状会是
	// 「注入的脚本装上了，但点一下就 404」，那种问题在现场最难查。
	for _, p := range []string{"/_qulv/", "/_qulv/api/download/online", "/_qulv/anything"} {
		if _, handled := h.do(http.MethodPost, p, `{}`, nil); handled {
			t.Errorf("%s 不该被拦截层认领（生产环境不可达，且与接管层命名空间重叠）", p)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("白名单之外不该打本机后端，实际打了 %d 次", len(*seen))
	}
}

func TestQulvDownloadOnlineWithoutLocalAPI(t *testing.T) {
	h := newHarness(t)
	// 没配本机后端地址时要说清「通道未配置」，而不是报一个看不懂的 URL 错误。
	fake := registerOnline(h, "w702", "丙", "C")

	w, _ := h.do(http.MethodPost, qulvAPIPrefix+"/download/online", `{"guid":"`+fake+`"}`, nil)
	if got := w.Code; got != http.StatusServiceUnavailable {
		t.Errorf("未配置本机后端应 503，得到 %d", got)
	}
}

// ── /info：面板要知道的两件事 ─────────────────────────────────────────

func TestQulvInfoCarriesVersionAndOwnURL(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.Revision = "9.9.9"
	h.it.cfg.LocalAPI = "http://127.0.0.1:8898"

	w, handled := h.do(http.MethodGet, qulvAPIPrefix+"/info", "", nil)
	if !handled {
		t.Fatal("/info 应被拦截层处理")
	}
	d := dataOf(t, w)
	if d["version"] != "9.9.9" {
		t.Errorf("面板要显示版本号，得到 %v", d["version"])
	}
	// 主机名取请求的 Host（httptest 默认 example.com），端口取本机回环地址。
	if d["qulv_url"] != "http://example.com:8898/" {
		t.Errorf("曲率地址应由「请求主机名 + 本机端口」拼出，得到 %v", d["qulv_url"])
	}
}

// 这条盯的是**安全属性**，不是功能：端口只能来自 LocalAPI。
// 请求头是调用方可控的，拿它拼 URL 就是一个开放重定向 / SSRF 面。
func TestQulvInfoPortNeverComesFromRequest(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.LocalAPI = "http://127.0.0.1:8898"

	r := httptest.NewRequest(http.MethodGet, qulvAPIPrefix+"/info", nil)
	r.Host = "evil.example:1234"
	w := httptest.NewRecorder()
	if !h.it.handleQulvInfo(w, r) {
		t.Fatal("/info 应被处理")
	}
	d := dataOf(t, w)
	if d["qulv_url"] != "http://evil.example:8898/" {
		t.Fatalf("端口必须来自 LocalAPI（8898），得到 %v", d["qulv_url"])
	}

	// LocalAPI 没有端口（没配同源通道）→ 给空串，让面板把按钮置灰。
	// 宁可少一个按钮，也不要一个点不通的坏链接。
	h.it.cfg.LocalAPI = "http://127.0.0.1"
	w2 := httptest.NewRecorder()
	h.it.handleQulvInfo(w2, httptest.NewRequest(http.MethodGet, qulvAPIPrefix+"/info", nil))
	if got := dataOf(t, w2)["qulv_url"]; got != "" {
		t.Fatalf("端口未知时应给空串，得到 %v", got)
	}
}

// 白名单没有因为新增 /info 而变松：同一条路径换个方法仍然一步都走不出去。
func TestQulvInfoOnlyAnswersGET(t *testing.T) {
	h := newHarness(t)
	w, handled := h.do(http.MethodPost, qulvAPIPrefix+"/info", "", nil)
	if !handled {
		t.Fatal("_qulv 下的路径仍应由兜底路由认领（而不是漏给官方）")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /info 应 404，得到 %d", w.Code)
	}
}

// ── 注入脚本的「开发覆盖」 ────────────────────────────────────────────

func TestQulvUIServesDevOverride(t *testing.T) {
	h := newHarness(t)
	dev := filepath.Join(t.TempDir(), "ui.dev.js")
	if err := os.WriteFile(dev, []byte("/* dev build */\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.it.cfg.UIDevFile = dev

	w, handled := h.do(http.MethodGet, qulvUIPath, "", nil)
	if !handled {
		t.Fatal("注入脚本应由拦截层发出")
	}
	if got := w.Body.String(); got != "/* dev build */\n" {
		t.Errorf("存在覆盖文件时应发它，得到 %q", got)
	}
	if w.Header().Get("X-Qulv-Dev") != "1" {
		t.Error("开发态应带 X-Qulv-Dev: 1（devtools 里要能一眼看出发的不是包里那份）")
	}

	// 覆盖文件删掉 → 立刻回到嵌入的那份。这是「开发覆盖不影响生产」的判据。
	if err := os.Remove(dev); err != nil {
		t.Fatal(err)
	}
	w2, _ := h.do(http.MethodGet, qulvUIPath, "", nil)
	if !bytes.Equal(w2.Body.Bytes(), uiScript) {
		t.Error("覆盖文件不存在时应发嵌入的脚本")
	}
	if w2.Header().Get("X-Qulv-Dev") != "" {
		t.Error("非开发态不该带 X-Qulv-Dev")
	}
}

// 空文件不能把注入脚本变成空白 —— 那会让官方页面上什么都不出现，
// 而且现象（脚本在、功能全无）比 404 更难查。
func TestQulvUIBlankDevFileFallsBack(t *testing.T) {
	h := newHarness(t)
	dev := filepath.Join(t.TempDir(), "ui.dev.js")
	if err := os.WriteFile(dev, []byte("  \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.it.cfg.UIDevFile = dev

	w, _ := h.do(http.MethodGet, qulvUIPath, "", nil)
	if !bytes.Equal(w.Body.Bytes(), uiScript) {
		t.Error("空白覆盖文件应视为不存在，退回嵌入的脚本")
	}
}
