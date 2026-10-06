package intercept

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// 往官方页面里插一行脚本（阶段 2「页面注入」）。
//
// # 为什么是在「转发请求」这一层做，而不是官方目录里塞文件
//
// 官方音乐的文件属于别的应用，升级/校验都会把它抹掉；而且它的静态资源目录
// 是只读的（真机上属主是 root）。拦截层本来就在官方入口的必经之路上，在这里
// 改一行 HTML 是最轻、也最可逆的做法：`opts.Interceptor` 拿掉，一切照旧。
//
// # 认领范围刻意压到最小
//
// 只有「浏览器要一份文档」的请求（GET + `Accept` 含 `text/html`）才认领。
// 其余——静态资源、接口、下载、WebSocket 升级——一律返回 false，交给接管层的
// 反向代理原样处理。那里的语义（Upgrade、Range、超时）是调好的，不该由我们重造。

// pageShellPrefix 是官方 SPA 的路径前缀。
//
// 它是路由表里**最短**的一条（`/music`），靠 `buildRoutes` 的「长前缀优先」
// 排在最后，正好当兜底：所有 `/music/api/v1/…`、`/music/_qulv/…` 都排在它前面。
const pageShellPrefix = "/music"

// uiScriptTagFormat 是注入的那一行。
//
// `defer` 是必须的：脚本要在官方 SPA 挂载之前就把 fetch/XHR 旁听装好，
// 否则首屏那次曲目列表请求会漏掉（漏掉的表现只是「第一次点菜单没有下载项」，
// 很难反查）。`?v=` 用版本串，方便对着页面源码查「这行是哪个版本发的」。
const uiScriptTagFormat = `<script defer src="%s?v=%s"></script>`

// pageShellMaxBytes 是允许缓冲的文档上限。
//
// 官方外壳 10.6 KB；给到 1 MB 已经是「它整个换实现了」的量级。超过就不注入、
// 直接原样转发 —— 宁可不注入，也不能把一个大响应憋在内存里。
const pageShellMaxBytes = 1 << 20

// uiRevision 是注入脚本内容的指纹，参与 `?v=`。
//
// 为什么不用「应用版本号」就够：脚本改动**不一定**伴随版本号变化（改完忘了
// bump 就会发出去），而浏览器缓存只认 URL。指纹挂在 URL 上，脚本一变地址就变，
// 无论版本号有没有动，用户都不会拿到旧脚本。
var uiRevision = func() string {
	sum := sha256.Sum256(uiScript)
	return hex.EncodeToString(sum[:4])
}()

// revision 返回注入标签上的版本串。
func (i *Interceptor) revision() string {
	if v := strings.TrimSpace(i.cfg.Revision); v != "" {
		return v + "-" + uiRevision
	}
	return uiRevision
}

// handlePageShell 转发官方页面的文档请求，并在 HTML 末尾插一行脚本。
//
// 返回 false 表示「这不是我要处理的请求」——调用方（接管层）会交给反向代理。
func (i *Interceptor) handlePageShell(w http.ResponseWriter, r *http.Request) bool {
	// 只有浏览器导航才会带 `text/html`；资源请求、接口请求、WebSocket 升级
	// 都不会。这一条既是范围控制，也是「别碰升级请求」的保护。
	if r.Method != http.MethodGet || !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		return false
	}

	// 一键退路：关掉注入时**根本不认领**（而不是「认领了但不插」）。
	// 这样响应完全由接管层的反向代理透传，与这个功能存在之前逐字节一致 ——
	// 「万一注入把官方页弄坏了」时，用户有一个立刻能用的开关。
	if i.uiInject != nil && !i.uiInject() {
		return false
	}

	// ⚠️ 必须丢掉条件请求头。
	//
	// 官方对这层外壳给的是 `Cache-Control: no-cache, must-revalidate`，浏览器每次
	// 都会带 If-None-Match/If-Modified-Since 回来。上游一旦回 304，浏览器就用它
	// 自己那份缓存 —— 而那份可能是**注入之前**存的，于是「升级了但页面上没变化」。
	// 外壳只有 10 KB，每次都要一份完整的比排查 304 便宜得多。
	r.Header.Del("If-None-Match")
	r.Header.Del("If-Modified-Since")

	resp, err := i.forward(r, nil)
	if err != nil {
		i.logf("[INTERCEPT] 转发页面 %s 失败：%v", r.URL.Path, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": 502, "msg": "upstream unavailable", "data": nil})
		return true
	}
	defer func() { _ = resp.Body.Close() }()

	// 非 200、不是 HTML、或者上游压过（压过的体没法做字符串替换）——原样回写。
	// 注意判断全在**读 body 之前**做，这样「不注入」的那条路仍然是流式转发。
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") ||
		resp.Header.Get("Content-Encoding") != "" {
		copyResponse(w, resp)
		return true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, pageShellMaxBytes+1))
	if err != nil {
		// 读到一半断了：上游给的东西不完整，原样回写也救不回来，如实报 502。
		i.logf("[INTERCEPT] 读取页面 %s 失败：%v", r.URL.Path, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": 502, "msg": "upstream read failed", "data": nil})
		return true
	}

	if len(body) > pageShellMaxBytes {
		// 超上限：不注入，把已经读到的这段和后面剩下的原样流出去
		// （丢掉 Content-Length，让 Go 用分块编码收尾）。
		writeStreamedShell(w, resp, body)
		return true
	}

	out, changed := injectUIScript(body, i.revision())
	if !changed {
		writeRaw(w, body, resp.StatusCode, resp.Header.Get("Content-Type"))
		return true
	}
	writeInjected(w, resp, out)
	return true
}

// injectUIScript 在 `</body>` 前插一行脚本。
//
// 返回 changed=false 表示文档里已经有这一行了（上游自带、或上一轮注入留下的），
// 此时不改动内容 —— 重复插会执行两遍（脚本自己有幂等判据，但没必要靠它兜）。
func injectUIScript(html []byte, revision string) ([]byte, bool) {
	if bytes.Contains(html, []byte(qulvUIPath)) {
		return html, false
	}
	tag := []byte(fmt.Sprintf(uiScriptTagFormat, qulvUIPath, revision))
	lower := bytes.ToLower(html)

	at := bytes.LastIndex(lower, []byte("</body>"))
	if at < 0 {
		at = bytes.LastIndex(lower, []byte("</html>"))
	}
	out := make([]byte, 0, len(html)+len(tag)+1)
	if at < 0 {
		// 没有 `</body>` 也没有 `</html>`：追加到末尾。`<script>` 放在哪都能跑，
		// 所以这不算错误处理，算「兜到就行」。
		out = append(out, html...)
		out = append(out, '\n')
		out = append(out, tag...)
		return out, true
	}
	out = append(out, html[:at]...)
	out = append(out, tag...)
	out = append(out, '\n')
	out = append(out, html[at:]...)
	return out, true
}

// writeInjected 回写改过的文档。
//
// 三件必须做的事，少一件都会在真机上变成难查的问题：
//   - 重算 Content-Length（体变长了，照抄旧值浏览器会截断）；
//   - 丢掉 ETag / Last-Modified（校验器对应的是**没注入**的那份，留着它
//     下次就是 304 → 浏览器用旧缓存 → 脚本不见了）；
//   - 丢掉 Content-Encoding（我们插过字节，那个编码已经不对了）。
func writeInjected(w http.ResponseWriter, resp *http.Response, body []byte) {
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		switch strings.ToLower(k) {
		case "content-length", "content-encoding", "etag", "last-modified":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// writeStreamedShell 用于「超过缓冲上限、不注入」的文档：先把已读到的写出去，
// 再流式转发剩下的。不设 Content-Length，交给 Go 用分块编码收尾。
func writeStreamedShell(w http.ResponseWriter, resp *http.Response, head []byte) {
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		if strings.EqualFold(k, "content-length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(head)
	_, _ = io.Copy(w, resp.Body)
}
