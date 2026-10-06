package intercept

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 这一组盯的是「页面注入」：拦截层在转发官方页面文档时，往 HTML 里补一行
// 脚本；以及那行脚本自己（`/music/_qulv/ui.js`）能不能被取到。
//
// 两个必须守住的不变量：
//   - 注入只发生在**文档**上。静态资源、接口、下载一律不碰 —— 碰了就是把
//     「官方页面能不能用」押在几行字符串拼接上。
//   - 注入过的响应必须重算 Content-Length、丢掉 ETag/Last-Modified。少任一条，
//     真机上就是「升级了但界面没变」这种最难查的状态。

// htmlResp 造一个「官方返回了一份 HTML」的上游响应。
func htmlResp(status int, body string, header http.Header) *http.Response {
	h := http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
	for k, vs := range header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// docHeaders 是浏览器导航时的请求头（`Accept` 里有 text/html 是认领的判据）。
var docHeaders = map[string]string{"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}

func TestPageShellInjectsUIScript(t *testing.T) {
	h := newHarness(t)
	const shell = `<html><head><title>t</title></head><body><div id="root"></div></body></html>`
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return htmlResp(http.StatusOK, shell, http.Header{
			"ETag":          []string{`W/"shell-v1"`},
			"Last-Modified": []string{"Wed, 01 Oct 2026 00:00:00 GMT"},
		})
	})

	w, handled := h.do(http.MethodGet, "/music/", "", docHeaders)
	if !handled {
		t.Fatal("浏览器要文档的请求应被认领（否则注入不会发生）")
	}

	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("状态码应照抄上游 200，得到 %d", w.Code)
	}
	if !strings.Contains(body, `src="`+qulvUIPath+`?v=`) {
		t.Fatalf("HTML 里应插进注入脚本，实际：\n%s", body)
	}
	if !strings.Contains(body, "defer") {
		t.Errorf("脚本必须 defer：它要在官方 SPA 挂载前装好旁听，又不能阻塞首屏")
	}
	if strings.Index(body, qulvUIPath) > strings.Index(body, "</body>") {
		t.Errorf("脚本应插在 </body> 之前，实际在之后：\n%s", body)
	}
	if strings.Index(body, "</body>") > strings.Index(body, "</html>") {
		t.Errorf("插入不应打乱原来的文档结构：\n%s", body)
	}

	// Content-Length 照抄旧值 = 浏览器按旧长度截断 → 脚本被切掉。
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(w.Body.Len()); got != want {
		t.Errorf("Content-Length 应与实体一致：得到 %q，实际体长 %s", got, want)
	}
	// 校验器对应的是**没注入**的那份体，留着它下次就是 304 → 浏览器用旧缓存。
	if got := w.Header().Get("ETag"); got != "" {
		t.Errorf("注入后必须丢掉 ETag（否则 304 会让浏览器用没注入的缓存），得到 %q", got)
	}
	if got := w.Header().Get("Last-Modified"); got != "" {
		t.Errorf("注入后必须丢掉 Last-Modified，得到 %q", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type 应保持 html，得到 %q", ct)
	}
}

func TestPageShellLeavesOtherTrafficAlone(t *testing.T) {
	h := newHarness(t)

	// 直接问页面注入本身：这几种请求都不归它，它要说「不」，把决定权交回去
	// （接口有接口的路由，资源由接管层的反向代理原样透传）。
	//
	// 不拿「整条路由有没有认领」当判据：`/music/api/v1/...` 本来就该被接口
	// 路由认领，那是另一回事；混在一起测，以后改路由表会连累这条用例。
	cases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		{"资源请求不带 text/html", http.MethodGet, "/music/static/assets/index.js", map[string]string{"Accept": "*/*"}},
		{"接口请求", http.MethodGet, "/music/api/v1/track/stream", map[string]string{"Accept": "application/json"}},
		{"没有任何 Accept", http.MethodGet, "/music/", nil},
		{"非 GET（表单文档也只透传）", http.MethodPost, "/music/", docHeaders},
		{"脚本自己的 fetch（Accept 是 */*）", http.MethodGet, "/music/", map[string]string{"Accept": "*/*"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.target, nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if h.it.handlePageShell(httptest.NewRecorder(), r) {
				t.Errorf("%s 不该被页面注入认领（%s %s）", c.name, c.method, c.target)
			}
		})
	}

	// 再顺一遍整条路由：静态文档以外的资源不该被任何路由抢走（会由接管层的
	// 反向代理原样送出去）。这条同时是兜底路由 `/music` 的护栏。
	if _, handled := h.do(http.MethodGet, "/music/static/assets/index.js", "", map[string]string{"Accept": "*/*"}); handled {
		t.Error("静态资源不该被拦截层的任何路由认领")
	}
}

func TestPageShellRelaysNonHTMLUntouched(t *testing.T) {
	h := newHarness(t)
	const js = `console.log("官方资源")`
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/javascript"}, "ETag": []string{"keep-me"}},
			Body:       io.NopCloser(strings.NewReader(js)),
		}
	})

	// 认领了（Accept 像导航），但上游给的不是 HTML —— 必须原样回写，
	// 不能因为「以为自己在处理文档」就往 JS 里插一行 <script>。
	w, handled := h.do(http.MethodGet, "/music/static/assets/index.js", "", docHeaders)
	if !handled {
		t.Fatal("这条路径应被认领（认领的判据是请求头，不是路径）")
	}
	if w.Body.String() != js {
		t.Errorf("非 HTML 的体不能被改：得到 %q", w.Body.String())
	}
	if got := w.Header().Get("ETag"); got != "keep-me" {
		t.Errorf("没注入就不该动校验器，得到 %q", got)
	}
}

func TestPageShellDropsConditionalRequestHeaders(t *testing.T) {
	h := newHarness(t)
	var seenIfNoneMatch, seenIfModifiedSince string
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		seenIfNoneMatch = r.Header.Get("If-None-Match")
		seenIfModifiedSince = r.Header.Get("If-Modified-Since")
		return htmlResp(http.StatusOK, `<html><body></body></html>`, nil)
	})

	headers := map[string]string{
		"Accept":            "text/html",
		"If-None-Match":     `W/"shell-v1"`,
		"If-Modified-Since": "Wed, 01 Oct 2026 00:00:00 GMT",
	}
	w, _ := h.do(http.MethodGet, "/music/", "", headers)

	if seenIfNoneMatch != "" || seenIfModifiedSince != "" {
		t.Errorf("条件请求头必须丢掉（否则上游回 304，浏览器用注入之前的缓存）："+
			"If-None-Match=%q If-Modified-Since=%q", seenIfNoneMatch, seenIfModifiedSince)
	}
	if !strings.Contains(w.Body.String(), qulvUIPath) {
		t.Error("丢掉条件头之后应拿到完整的 200 文档并注入")
	}
}

func TestPageShellInjectsOnlyOnce(t *testing.T) {
	h := newHarness(t)
	// 上游文档里已经有这一行了（例如官方自己接了一层、或上一轮注入留下来了）。
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return htmlResp(http.StatusOK, `<html><body><script src="`+qulvUIPath+`?v=old"></script></body></html>`, nil)
	})

	w, _ := h.do(http.MethodGet, "/music/", "", docHeaders)
	if n := strings.Count(w.Body.String(), qulvUIPath); n != 1 {
		t.Errorf("已经有一行时不该再插，页面上出现了 %d 次", n)
	}
}

func TestPageShellCanBeTurnedOff(t *testing.T) {
	h := newHarness(t)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return htmlResp(http.StatusOK, `<html><body></body></html>`, nil)
	})
	h.it.uiInject = func() bool { return false }

	// 关掉时**不认领**（而不是「认领了但不插」）：请求原样落到接管层的反向代理，
	// 官方页面与没做注入时逐字节一致 —— 这是注入把页面弄坏时的退路。
	if _, handled := h.do(http.MethodGet, "/music/", "", docHeaders); handled {
		t.Error("关掉注入后，文档请求不该被拦截层认领")
	}
	if calls := h.up.calls(); len(calls) != 0 {
		t.Errorf("关掉注入后连上游都不该打，实际打了 %v", calls)
	}

	// 开关一开又正常认领（现取，不缓存 —— 用户在设置里改完不用重启）。
	h.it.uiInject = func() bool { return true }
	w, handled := h.do(http.MethodGet, "/music/", "", docHeaders)
	if !handled || !strings.Contains(w.Body.String(), qulvUIPath) {
		t.Error("开关打开后应恢复注入")
	}
}

func TestPageShellStreamsOversizedDocuments(t *testing.T) {
	h := newHarness(t)
	// 比缓冲上限还大的「HTML」：宁可不注入，也不能把它整个憋进内存。
	big := "<html><body>" + strings.Repeat("x", pageShellMaxBytes+1024) + "</body></html>"
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return htmlResp(http.StatusOK, big, nil)
	})

	w, handled := h.do(http.MethodGet, "/music/huge", "", docHeaders)
	if !handled {
		t.Fatal("应被认领")
	}
	if got := w.Body.Len(); got != len(big) {
		t.Errorf("超大文档应原样流出去（%d 字节），实际 %d", len(big), got)
	}
	if strings.Contains(w.Body.String(), qulvUIPath) {
		t.Error("超大文档不该被注入")
	}
	if got := w.Header().Get("Content-Length"); got != "" {
		t.Errorf("已经读过一段、又还要流剩下的，就不能给 Content-Length，得到 %q", got)
	}
}

func TestInjectUIScriptPure(t *testing.T) {
	tag := `src="` + qulvUIPath + `?v=`

	out, changed := injectUIScript([]byte(`<html><body>x</body></html>`), "2.1.79")
	if !changed {
		t.Fatal("正常文档应被注入")
	}
	if !strings.Contains(string(out), tag+"2.1.79") {
		t.Errorf("版本串应出现在地址上（否则脚本改了用户还拿旧的）：%s", out)
	}

	// 有 </html> 但没有 </body>：插在 </html> 前。
	out, changed = injectUIScript([]byte(`<html><body>x</html>`), "v")
	if !changed || strings.Index(string(out), tag) > strings.Index(string(out), "</html>") {
		t.Errorf("没有 </body> 时应插在 </html> 前，得到 %s", out)
	}

	// 两个都没有：追加到末尾（`<script>` 放哪都能跑，兜到就行）。
	out, changed = injectUIScript([]byte(`no html at all`), "v")
	if !changed || !strings.HasSuffix(string(out), `?v=v"></script>`) {
		t.Errorf("没有闭合标签时应追加到末尾，得到 %s", out)
	}

	// 字节级：只应在原文档里插一段，其余部分逐字节不变。
	src := []byte(`<html><body><p>中文 & "引号"</p></body></html>`)
	out, changed = injectUIScript(src, "v")
	stripped := strings.Replace(string(out), "<script defer "+tag+`v"></script>`+"\n", "", 1)
	if !changed || stripped != string(src) {
		t.Errorf("除了插入的那一行，正文必须逐字节不变：\n%q\n%q", stripped, src)
	}
}

func TestQulvUIPathMustLiveUnderMusicPrefix(t *testing.T) {
	// 这条是设计决定的守卫，不是形式主义：官方音乐在 fnOS 的 nginx 里是
	// `location /music` → socket，裸的 `/_qulv/...` 根本到不了拦截层。
	if !strings.HasPrefix(qulvUIPath, pageShellPrefix+"/") {
		t.Fatalf("注入脚本的地址必须挂在 %s 下（生产环境只有这棵子树会被代理到这里），得到 %s",
			pageShellPrefix, qulvUIPath)
	}
	if !strings.HasPrefix(qulvAPIPrefix, pageShellPrefix+"/") {
		t.Fatalf("同源接口同样必须挂在 %s 下，得到 %s", pageShellPrefix, qulvAPIPrefix)
	}
}

func TestQulvUIScriptServed(t *testing.T) {
	h := newHarness(t)

	w, handled := h.do(http.MethodGet, qulvUIPath, "", nil)
	if !handled {
		t.Fatal("脚本地址应被拦截层应答")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type 应为 javascript，得到 %q", ct)
	}
	// 不许缓存：留着旧脚本就会变成「升级了但界面没变」。
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("必须 no-cache，得到 %q", cc)
	}
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(len(uiScript)); got != want {
		t.Errorf("Content-Length 应为嵌入脚本的长度 %s，得到 %q", want, got)
	}
	if len(uiScript) < 1024 {
		t.Fatalf("嵌入的脚本太短（%d 字节），像是没被 go:embed 进去", len(uiScript))
	}
	body := w.Body.String()
	// 幂等标记 + 同源地址：这两样错了，脚本要么会重复注册，要么打不通后端。
	if !strings.Contains(body, "__QULV_PAGE_UI__") {
		t.Error("脚本里应有重复注入的判据")
	}
	if !strings.Contains(body, "var BASE = '"+qulvPrefix+"'") {
		t.Errorf("脚本里的同源根应是 %s", qulvPrefix)
	}
	if !strings.Contains(body, "'/api/download/online'") {
		t.Error("脚本里应打同源下载接口（BASE + /api/download/online）")
	}
	if strings.Contains(body, `'/_qulv`) || strings.Contains(body, `"/_qulv`) {
		t.Error("脚本里不该出现裸的 /_qulv 路径（生产环境到不了拦截层）")
	}
}

func TestQulvUIScriptHeadHasNoBody(t *testing.T) {
	h := newHarness(t)
	w, handled := h.do(http.MethodHead, qulvUIPath, "", nil)
	if !handled || w.Code != http.StatusOK {
		t.Fatalf("HEAD 应被应答且 200，得到 handled=%v code=%d", handled, w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD 不该有实体，得到 %d 字节", w.Body.Len())
	}
	// 长度仍要如实报（有些客户端靠 HEAD 探大小）。
	if got := w.Header().Get("Content-Length"); got == "" {
		t.Error("HEAD 也要给 Content-Length")
	}
}
