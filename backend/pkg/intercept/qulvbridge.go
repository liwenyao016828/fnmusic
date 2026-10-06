package intercept

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// uiScript 是注入进官方页面的那个脚本（源码见同目录 assets/ui.js）。
//
// 用 go:embed 而不是写在 Go 字符串里：它是 JS，要有语法高亮、能被 `node --check`
// 单独验，也不该在 Go 源码里再转义一遍。
//
//go:embed assets/ui.js
var uiScript []byte

// 「挂在官方页面同源下的曲率接口」。
//
// # 为什么走同源代理，而不是让页面直连曲率后端
//
// 官方音乐页面的 origin 是 NAS 本身，曲率后端在 `:8898` —— 直连就是跨域。
// 放开 CORS 等于把曲率后端（能下载、能整理、能读整机文件的那一套）暴露给
// 任意站点，代价远大于收益。同源代理之后还有两个附带好处：
//
//   - 注入脚本用**相对路径**就行，不需要知道 NAS 的 IP 和端口；
//   - 页面与接口同源，没有预检请求，点击是即时的。
//
// 代价是这里成了一道**白名单闸门** —— 所以它只放行下面显式登记的那几条，
// 其余 `_qulv/` 前缀下的一切一律 404，绝不转发给官方（官方会把未知路径
// 当成 SPA 路由，返回一坨 HTML，比 404 更难查）。
//
// # ⚠️ 为什么前缀里必须带 `/music`
//
// 官方音乐在 fnOS 的 nginx 里是「`location /music` → `proxy_pass
// http://unix:/var/run/trim_music.socket:`」—— **只有 `/music` 及其子路径
// 会被交给这个 socket**。裸的 `/_qulv/...` 会被主站自己接走（404），
// 压根到不了这里。所以整条通道挂在 `/music/_qulv` 下：注入进页面的脚本
// 用这个绝对路径发请求，正好落在被代理的那棵子树里。
//
// 这条踩过一次才算清楚 —— 设计时写的是 `/_qulv`，真机上必然 404。

const (
	// qulvPrefix 是这个命名空间的根（见上面关于 `/music` 前缀的说明）。
	qulvPrefix = "/music/_qulv"
	// qulvAPIPrefix 是同源 API 的路径前缀。
	qulvAPIPrefix = qulvPrefix + "/api"
	// qulvUIPath 是注入脚本自己的地址。
	qulvUIPath = qulvPrefix + "/ui.js"
	// qulvDownloadTimeout 是「解析 + 落盘」的总预算。
	//
	// 落盘要把整个文件拖下来（无损一首几十兆），所以给得比别的接口宽。
	qulvDownloadTimeout = 5 * time.Minute
)

// handleQulvUIJS 把注入脚本本人交给浏览器。
//
// 为什么脚本要从这里出、而不是打进官方页面的静态资源：官方目录属于别的应用
// （升级会覆盖），而且 nginx 加了 `Cross-Origin-Embedder-Policy: require-corp`,
// 外链脚本会被拦 —— 只能同源，只能由拦截层自己发。
func (i *Interceptor) handleQulvUIJS(w http.ResponseWriter, r *http.Request) bool {
	body := uiScript
	// 开发覆盖（见 Config.UIDevFile）：文件在、且不是空白，就发它。
	// 读失败（不存在、权限不对）一律静默退回嵌入的那份 —— 这是条可选捷径，
	// 不能因为它把注入脚本弄成 404。
	dev := false
	if p := strings.TrimSpace(i.cfg.UIDevFile); p != "" {
		if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
			body, dev = b, true
		}
	}

	// 一律不许缓存：脚本是随版本走的，浏览器留着旧的就会出现「升级了但界面没变」
	// 这种最难查的状态。页面上那行 `<script src="…?v=<版本>">` 只是双保险。
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if dev {
		// 开发态的显眼标记：devtools 里一眼能看出「现在发的不是打进包里的那份」。
		w.Header().Set("X-Qulv-Dev", "1")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}

// handleQulvInfo 把「只有后端知道的事」交给注入脚本。
//
// 现在只有两件：应用版本号（面板上显示，也方便对着截图确认装的是哪一版），
// 以及**曲率自己的地址** —— 注入脚本跑在官方页面的 origin 上，而曲率在另一个
// 端口（fpk manifest 的 `service_port`），脚本无从得知，只能由后端算给它。
func (i *Interceptor) handleQulvInfo(w http.ResponseWriter, r *http.Request) bool {
	writeEnvelope(w, map[string]any{
		"version":  strings.TrimSpace(i.cfg.Revision),
		"qulv_url": i.qulvURL(r),
	})
	return true
}

// qulvURL 拼出「曲率自己」的地址，形如 `http://192.168.1.66:8898/`。
//
// 主机名取**请求的 Host**（浏览器访问官方页面用的那个名字，可能是 IP、
// 域名或主机名 —— 用哪个都能到同一台 NAS）；端口取本机回环地址里的那个。
//
// ⚠️ 端口**只从 LocalAPI 取**，绝不从请求里取：请求头是调用方可控的，
// 拿它拼 URL 就等于开了一个 SSRF/开放重定向的面。LocalAPI 是启动时定死的。
//
// 拿不到主机名或端口时返回空串 —— 面板据此把「打开曲率」置灰，而不是给一个
// 点不通的坏链接（宁可少一个按钮，也不要一个骗人的按钮）。
func (i *Interceptor) qulvURL(r *http.Request) string {
	u, err := url.Parse(strings.TrimSpace(i.cfg.LocalAPI))
	if err != nil || u.Port() == "" {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, u.Port()) + "/"
}

// handleQulvDownloadOnline 把一首**在线曲目**下载到 NAS。
//
// 入参只有一个虚拟 guid：解析直链、命名、写标签与封面全部由曲率后端做 ——
// 注入脚本只负责回答「用户点了哪一首」。
//
// 为什么解析必须放在后端：虚拟 id 的映射表（registry）只存在于拦截层，
// 页面拿不到真实平台 id；而且解析要打第三方接口，放浏览器里会被 CORS 拦死。
func (i *Interceptor) handleQulvDownloadOnline(w http.ResponseWriter, r *http.Request) bool {
	var body struct {
		GUID string `json:"guid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求体不是合法 JSON"})
		return true
	}
	guid := strings.TrimSpace(body.GUID)
	if guid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "缺少 guid"})
		return true
	}

	track, _, ok := i.lookupTrackAny([]string{guid})
	if !ok {
		// 认不出来就说认不出来。不猜、不失配 —— 猜错会把别人的歌写进曲库。
		writeJSON(w, http.StatusNotFound, map[string]any{
			"code": 404, "message": "未登记的在线曲目 id：该 id 不是本应用发出去的，或登记表已被回收",
		})
		return true
	}

	if i.localAPI == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code": 503, "message": "曲率后端地址未知，下载通道未配置",
		})
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), qulvDownloadTimeout)
	defer cancel()

	// ② 下载源池：先去池里找同名的最高音质版本，找不到才用这首歌自己平台的直链。
	res, used, err := i.resolveForDownload(ctx, track)
	if err != nil {
		// 解析失败要**如实说清是哪一种**：不可播（稳定结论）与网络故障
		// （临时）对用户的意义完全不同，混成一句「下载失败」等于没说。
		msg := "解析直链失败：" + err.Error()
		if err == online.ErrNotPlayable {
			msg = "这首歌在该平台不可播放（收费 / 无版权 / 地区限制）"
		} else if err == online.ErrNoResolver {
			msg = "该平台没有可用的直链解析器"
		}
		i.logf("[QULV] 下载解析失败 %s（%s/%s）：%v", guid, track.Platform, track.PlatformID, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": 502, "message": msg})
		return true
	}

	payload := map[string]any{
		"name":     track.Title,
		"singer":   track.Artist(),
		"album":    track.Album,
		"cover":    track.CoverURL,
		"source":   track.Platform,
		"songmid":  track.PlatformID,
		"url":      res.URL,
		"duration": track.Duration,
	}
	for k, v := range i.downloadPrefs() {
		payload[k] = v
	}

	out, status, err := i.postLocalAPI(ctx, "/api/download/song", payload)
	if err != nil {
		i.logf("[QULV] 调用本机下载接口失败：%v", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code": 502, "message": "调用曲率下载接口失败：" + err.Error(),
		})
		return true
	}

	i.logf("[QULV] 落盘在线曲目 %q — %s（%s/%s）", track.Title, track.Artist(), track.Platform, track.PlatformID)
	// 如实回传「实际从哪儿、什么音质下来的」——界面要拿它显示，不能只报原平台。
	writeJSON(w, status, map[string]any{
		"code": 200, "message": "success",
		"data": map[string]any{
			"source": used.Platform, "quality": used.Quality,
			"upstream": json.RawMessage(out),
		},
	})
	return true
}

// postLocalAPI 把请求转给**本机**的曲率后端。
//
// 目标地址来自配置（`--port` 决定的回环地址），**不接受请求里的任何 host** ——
// 这就是这一层不引入 SSRF 面的原因：能打到哪儿在启动时就定死了。
func (i *Interceptor) postLocalAPI(ctx context.Context, path string, payload any) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.localAPI+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	// 回环请求免鉴权（后端的 AUTH 中间件对本机/内网地址免校验），所以这里
	// 不复制客户端的任何凭据 —— 注入脚本那边也不需要持有 token。
	client := &http.Client{Timeout: qulvDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// handleQulvNotFound 是 `_qulv/` 下的兜底：白名单之外一律 404。
//
// 故意**不转发给官方**：官方对未知路径会走 SPA fallback 返回 HTML，客户端
// 拿到 200 + HTML 会以为请求成功，比干脆的 404 难查得多。
func (i *Interceptor) handleQulvNotFound(w http.ResponseWriter, r *http.Request) bool {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"code": 404, "message": "曲率同源接口没有这个路径：" + r.URL.Path,
	})
	return true
}
