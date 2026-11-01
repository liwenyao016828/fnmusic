package api

import (
	"net/http"
	"strings"

	"fn-lx-player/pkg/applog"
)

// ── 飞牛统一网关（微应用）前缀 ────────────────────────────────────────────
//
// 2026-10-31：曲率的桌面入口从「端口直连 iframe」（`http://nas:8898/`）改成
// 官方推荐的「统一网关微应用」：
//
//	fpk-package/app/ui/config:
//	  gatewayPrefix: "/app/yinshu-ai"
//	  gatewaySocket: "app.sock"     ← 必须在 ${TRIM_APPDEST}（即 target 目录）
//	  url:           "/app/yinshu-ai"
//
// 为什么非改不可：JS SDK（`pickSharedFile` / `openAppSetting`，也就是用户要的
// 「授权目录」按钮）**只在微应用形态下可用**。端口直连的 iframe 里
// `getHostCapabilities()` 探不到宿主，按钮永远不出现 —— 之前那套授权 UI 实际上
// 一次都没跑起来过。
//
// ── 网关到底传什么路径 ───────────────────────────────────────────────────
//
// 真机实测（fnOS 1.2.0903，`curl http://127.0.0.1:4398/app/miyin/`）：
// 网关（`trim_http_cgi`）把 `/app/<name>/**` 原样转发给应用的 socket，**不剥前缀**。
// 官方文档也只说「应用通过 gatewayPrefix 注册公开路径」，没承诺剥离。所以应用
// 必须自己剥 —— 否则 `mux` 里登记的 `/api/xxx` 一条都匹配不上，静态文件也会
// 去找 `app/yinshu-ai/assets/...` 而 404。
//
// 为兼容两种转发方式（剥 / 不剥，官方措辞是模糊的，不同版本可能不同），这里
// 只在路径确实以本应用前缀开头时才剥；不带前缀的请求原样放行 —— 于是
// 「端口直连 8898」「网关转发」「本机自调用 127.0.0.1:8898」三种入口全部可用。
//
// ── 为什么不顺手把前缀记进日志 ───────────────────────────────────────────
//
// 剥前缀是每个请求都要做的事，逐条打日志会把日志页刷满。只在**看起来像网关
// 请求但前缀不匹配**（`/app/` 开头却不是本应用的）时记一条 warn：那说明
// ui/config 里的 gatewayPrefix 与实际挂载路径不一致，是个真问题。
const gatewayPrefix = "/app/yinshu-ai"

// stripGatewayPrefix 把 `/app/yinshu-ai/api/...` 改写成 `/api/...`。
//
// 同时改写 `RawPath`（如果存在）与 `RequestURI`：`http.FileServer` 与
// `ServeMux` 在 Go 1.22+ 会看 `EscapedPath()`，只改 `URL.Path` 会在带转义字符
// 的路径上出现「Path 已剥、RawPath 没剥」的不一致，表现为静态资源偶尔 404。
func stripGatewayPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasPrefix(path, gatewayPrefix) {
			rest := path[len(gatewayPrefix):]
			if rest == "" || rest[0] == '/' {
				if rest == "" {
					rest = "/"
				}
				r.URL.Path = rest
				if r.URL.RawPath != "" {
					if strings.HasPrefix(r.URL.RawPath, gatewayPrefix) {
						r.URL.RawPath = r.URL.RawPath[len(gatewayPrefix):]
						if r.URL.RawPath == "" {
							r.URL.RawPath = "/"
						}
					} else {
						// RawPath 对不上就不动它：Go 会退回用 Path 重新转义。
						r.URL.RawPath = ""
					}
				}
				if r.RequestURI != "" {
					r.RequestURI = r.URL.RequestURI()
				}
				next.ServeHTTP(w, r)
				return
			}
		}

		// 网关前缀不匹配：说明 ui/config 与后端不一致，记一条便于排查（同一进程只记一次）。
		if strings.HasPrefix(path, "/app/") {
			applog.Default().AddFrom("api", "warn",
				"请求路径带了 /app/ 前缀但不是本应用的网关前缀 "+gatewayPrefix+
					"，已按原路径处理；若页面打不开请核对 app/ui/config 的 gatewayPrefix。")
		}

		next.ServeHTTP(w, r)
	})
}
