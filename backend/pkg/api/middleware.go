package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"
)

// publicPaths 即使开启了 API Token 也无需鉴权的路径（探活与自描述）
var publicPaths = map[string]bool{
	"/api/health":      true,
	"/api/catalog":     true,
	"/api/app/version": true,
}

// ── 鉴权模式 ──
//
// 历史行为是「设了 FN_API_TOKEN 才校验，否则完全放开」。局域网里开箱即用没问题，
// 但这台机器的端口一旦被映射 / 反代 / 隧道到公网，全库的文件读写、删除、下载就对
// 任何能连上的人敞开，而且没有任何提示。现在按「来源地址」分三层：
//
//	none  不校验。等同历史行为，需要显式设置 FN_AUTH_MODE=none。
//	lan   本机、内网、链路本地、carrier-grade NAT 免校验（10/8、172.16/12、192.168/16、
//	      169.254/16、100.64/10、fc00::/7、fe80::/10、127/8、::1）；其它来源必须带
//	      Token，服务端没设 Token 就拒绝。FN_TRUSTED_CIDRS 可再追加白名单网段。
//	token 一律校验（publicPaths 与 OPTIONS 除外）。
//
// 默认（FN_AUTH_MODE 未设 = auto）：设了 FN_API_TOKEN 按 token，没设按 lan。
//
// 注意：X-Forwarded-For / X-Real-IP 一律不采信 —— 那是调用方随便写的头，只看 TCP
// 对端地址。反代场景下对端就是反代自己，把它加进 FN_TRUSTED_CIDRS，或者直接设 Token。
const (
	AuthModeNone  = "none"
	AuthModeLAN   = "lan"
	AuthModeToken = "token"
)

// ResolveAuthMode 把 FN_AUTH_MODE 与环境里的 Token 解析成生效模式。
// 无法识别的取值按默认（auto）处理 —— 打错字不会静默放开。
func ResolveAuthMode(raw, token string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case AuthModeNone:
		return AuthModeNone
	case AuthModeLAN:
		return AuthModeLAN
	case AuthModeToken:
		return AuthModeToken
	}
	if strings.TrimSpace(token) != "" {
		return AuthModeToken
	}
	return AuthModeLAN
}

// AuthModeEnv 读取 FN_AUTH_MODE（空 = auto）
func AuthModeEnv() string { return strings.TrimSpace(os.Getenv("FN_AUTH_MODE")) }

// TrustedCIDRsEnv 读取 FN_TRUSTED_CIDRS（逗号分隔的 IP 或 CIDR，空 = 只用内置内网段）
func TrustedCIDRsEnv() string { return strings.TrimSpace(os.Getenv("FN_TRUSTED_CIDRS")) }

// cgnatNet = 100.64.0.0/10：Tailscale、运营商级 NAT、部分容器网络都在这里面。
// 这些地址无法从公网直接路由过来，与 RFC1918 同等对待。
var cgnatNet = func() *net.IPNet {
	_, n, err := net.ParseCIDR("100.64.0.0/10")
	if err != nil {
		panic("内置 CIDR 解析失败: " + err.Error())
	}
	return n
}()

// ParseTrustedCIDRs 解析 FN_TRUSTED_CIDRS：逗号分隔，元素可以是 192.168.1.9、
// 203.0.113.0/24、fd7a:115c:a1e0::/48。解析不了的元素直接跳过（不 panic）。
func ParseTrustedCIDRs(raw string) []*net.IPNet {
	out := []*net.IPNet{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if ip := net.ParseIP(part); ip != nil {
			out = append(out, &net.IPNet{IP: ip, Mask: fullMask(ip)})
			continue
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// TrustedCIDRStrings 把网段还原成字符串，供 /api/health 回显（确认环境变量真的生效了）
func TrustedCIDRStrings(nets []*net.IPNet) []string {
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		out = append(out, n.String())
	}
	return out
}

func fullMask(ip net.IP) net.IPMask {
	if ip.To4() != nil {
		return net.CIDRMask(32, 32)
	}
	return net.CIDRMask(128, 128)
}

// clientIP 取 TCP 对端的 IP 字面量（去掉端口与 IPv6 zone）。
// 不采信 X-Forwarded-For / X-Real-IP。
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if i := strings.IndexByte(host, '%'); i >= 0 { // fe80::1%eth0
		host = host[:i]
	}
	return host
}

// isTrustedPeer 判断来源地址是否属于「无法从公网直连」的地址。
// 地址为空或无法解析时返回 true：进程内调用、unix socket 这类情形没有对端信息，
// 而真实 TCP 连接一定有对端地址，所以这条不构成公网绕过。
func isTrustedPeer(ipStr string, extra []*net.IPNet) bool {
	if ipStr == "" {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && cgnatNet.Contains(v4) {
		return true
	}
	for _, n := range extra {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isLoopbackIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	return ip != nil && ip.IsLoopback()
}

// ipNotifier 让同一来源的记录在窗口期内只写一次，
// 避免被公网扫描时把内存日志缓冲刷爆。
type ipNotifier struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	limit int
}

const ipNotifyWindow = time.Minute

func newIPNotifier(limit int) *ipNotifier {
	return &ipNotifier{seen: make(map[string]time.Time), limit: limit}
}

func (n *ipNotifier) shouldLog(ip string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if t, ok := n.seen[ip]; ok && now.Sub(t) < ipNotifyWindow {
		return false
	}
	if len(n.seen) >= n.limit { // 满了整体重置，不让 map 无界增长
		n.seen = make(map[string]time.Time)
	}
	n.seen[ip] = now
	return true
}

// ParseCORSOrigins 解析 FN_CORS_ORIGINS（逗号分隔）。
// 返回空切片表示「仅同源」：不下发任何 CORS 头，浏览器跨站请求会被拦截，
// 从而避免任意网页跨站驱动 NAS。服务端到服务端的调用不受 CORS 影响。
func ParseCORSOrigins(raw string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func originAllowed(origin string, allowlist []string) bool {
	for _, a := range allowlist {
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

// corsMiddleware 按 FN_CORS_ORIGINS 下发 CORS 头；未配置时保持同源（不发头）
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && len(s.corsOrigins) > 0 && originAllowed(origin, s.corsOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, Authorization, X-API-Token")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware 按生效模式校验来源与 Token（见文件顶部「鉴权模式」注释）。
// 被拒绝时顺便回一条「为什么 + 怎么放行」，调用方不会只拿到一个光秃秃的 401。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	mode := s.effectiveAuthMode()
	if mode == AuthModeNone {
		return next
	}
	token := strings.TrimSpace(s.apiToken)
	extra := s.trustedCIDRs
	notify := newIPNotifier(256)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		peer := clientIP(r)
		if mode == AuthModeLAN && isTrustedPeer(peer, extra) {
			// 内网来源免校验，但第一次见到某个地址时记一条：谁在直连这台机器。
			// 历史行为是完全静默的，要收紧时至少得有据可查。
			if peer != "" && !isLoopbackIP(peer) && notify.shouldLog(peer) {
				applog.Default().AddFrom("api", "info", fmt.Sprintf(
					"内网来源 %s 免 Token 访问（本机/内网/容器地址）。收紧请设 FN_API_TOKEN，或 FN_AUTH_MODE=none 完全放开。", peer))
			}
			next.ServeHTTP(w, r)
			return
		}

		provided := strings.TrimSpace(r.Header.Get("X-API-Token"))
		if provided == "" {
			provided = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		msg := "缺少或错误的 API Token。请在请求头 X-API-Token 或查询参数 ?token= 中提供。"
		if token == "" {
			msg = fmt.Sprintf("来源 %s 不在内网信任范围，而服务端未设置 FN_API_TOKEN，没有凭据可校验，已拒绝。"+
				"请设置 FN_API_TOKEN（并在请求头 X-API-Token 里带上），或把 FN_AUTH_MODE 设为 none 完全放开。", peer)
		}
		if notify.shouldLog(peer) {
			applog.Default().AddFrom("api", "warn", fmt.Sprintf("已拒绝 %s 的请求 %s：%s", peer, r.URL.Path, msg))
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":      401,
			"message":   msg,
			"peer_ip":   peer,
			"auth_mode": mode,
		})
	})
}

// APIToken 读取配置的 API Token（环境变量 FN_API_TOKEN）
func APIToken() string {
	return strings.TrimSpace(os.Getenv("FN_API_TOKEN"))
}

// CORSOrigins 读取允许的跨域来源（环境变量 FN_CORS_ORIGINS，逗号分隔）
func CORSOrigins() []string {
	return ParseCORSOrigins(os.Getenv("FN_CORS_ORIGINS"))
}
