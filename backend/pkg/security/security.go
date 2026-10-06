// Package security 提供出站请求的 SSRF 防护与响应体限额。
//
// 设计要点：
//   - 协议白名单（仅 http/https）
//   - 目标 IP 校验：拒绝回环、内网、链路本地、CGNAT 与云元数据地址
//   - 在 TCP 连接建立前对「实际解析出的 IP」二次校验（Dialer.Control），
//     可防 DNS rebinding：域名先解析到公网、校验通过后再解析到内网的攻击
//   - 可选的域名白名单
//   - 统一的响应体大小上限，避免单个畸形大响应打爆内存
package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultMaxBodyBytes 出站响应体的默认上限（8MB）
const DefaultMaxBodyBytes int64 = 8 << 20

// Options 出站请求安全策略
type Options struct {
	// AllowPrivateNet 是否允许访问内网/回环地址（默认关闭）。
	// 若用户确实需要代理内网音源，可通过环境变量 FN_ALLOW_PRIVATE_NET=1 打开。
	AllowPrivateNet bool
	// HostAllowlist 非空时，仅允许访问这些域名及其子域名
	HostAllowlist []string
	// MaxBodyBytes 响应体上限，<=0 时使用 DefaultMaxBodyBytes
	MaxBodyBytes int64
	// DisableProxy 强制不走代理（用于「公网直连」重试）
	DisableProxy bool
	// ForceProxyForLocal 强制内网目标也走代理（用于「内网目标经代理可达」的场景）
	ForceProxyForLocal bool
}

// DefaultOptions 返回默认策略（禁止访问内网）
func DefaultOptions() Options {
	return Options{
		AllowPrivateNet: envBool("FN_ALLOW_PRIVATE_NET"),
		MaxBodyBytes:    DefaultMaxBodyBytes,
	}
}

// OptionsWithAllowlist 在默认策略基础上附加域名白名单
func OptionsWithAllowlist(hosts []string) Options {
	opts := DefaultOptions()
	opts.HostAllowlist = hosts
	return opts
}

func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (o Options) maxBody() int64 {
	if o.MaxBodyBytes <= 0 {
		return DefaultMaxBodyBytes
	}
	return o.MaxBodyBytes
}

// ValidateURL 校验出站 URL 的协议与主机是否被允许。
// 注意：字面量 IP 会在此被校验；域名形式的地址依赖 NewSafeTransport 的连接期校验。
func ValidateURL(raw string, opts Options) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("URL 不能为空")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https 协议，收到 %q", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return nil, errors.New("URL 缺少主机名")
	}

	if len(opts.HostAllowlist) > 0 && !HostAllowed(host, opts.HostAllowlist) {
		return nil, fmt.Errorf("主机 %s 不在允许访问的域名列表中", host)
	}

	if ip := net.ParseIP(host); ip != nil {
		if err := CheckIP(ip, opts); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// HostAllowed 判断 host 是否命中白名单（支持子域名）
func HostAllowed(host string, allowlist []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, a := range allowlist {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// CheckIP 拒绝不安全的目标地址
func CheckIP(ip net.IP, opts Options) error {
	if ip == nil {
		return errors.New("目标地址无法解析")
	}
	if ip.IsLoopback() && !opts.AllowPrivateNet {
		return fmt.Errorf("禁止访问回环地址 %s", ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("禁止访问链路本地地址 %s", ip)
	}
	if ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("禁止访问该地址 %s", ip)
	}
	if isPrivateOrMetadata(ip) && !opts.AllowPrivateNet {
		return fmt.Errorf("禁止访问内网/元数据地址 %s", ip)
	}
	return nil
}

// isPrivateOrMetadata 覆盖 RFC1918、ULA、CGNAT(100.64/10) 与云元数据网段
func isPrivateOrMetadata(ip net.IP) bool {
	if ip.IsPrivate() {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 0:
		return true
	case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // 100.64.0.0/10 CGNAT
		return true
	case ip4[0] == 169 && ip4[1] == 254: // 169.254.0.0/16 含 169.254.169.254 元数据
		return true
	case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24
		return true
	case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19): // 198.18.0.0/15 基准测试网段
		return true
	}
	return false
}

// NewSafeTransport 返回带 SSRF 防护的 Transport。
// Control 回调在 TCP 连接真正建立前执行，此时域名已解析为具体 IP，
// 因此可以拦截 DNS rebinding。
//
// 代理分流（重要）：
// NAS 上常给整个环境配置 HTTP_PROXY/HTTPS_PROXY（例如代理跑在另一台内网机器），
// 而 SSRF 防护又要拦截内网地址，两者叠加就形成死锁：
//
//	proxyconnect tcp: dial tcp 192.168.x.x:7890: 禁止访问内网/元数据地址
//
// 也就是「请求要走代理，但代理自己就是内网地址，被自己的规则拦下」。
// 因此这里按目标地址分流：
//   - 内网/回环目标（自建 AI 服务、局域网音源）→ 直连，绕过代理；
//   - 公网目标 → 走环境变量配置的代理。
//
// ⚠️ 只做目标侧分流是不够的：走代理时 Control 看到的是**代理自己的地址**，
// 于是公网出站仍然会死在同一条错误上（取链 502、流中继 502）。
// 所以拨号校验还要放行「环境变量里配置的那个代理出口」，见 dialControl。
func NewSafeTransport(opts Options) *http.Transport {
	return newSafeTransport(opts, proxyExemptAddrs(), proxyFunc(opts))
}

// newSafeTransport 是 NewSafeTransport 的可注入版本（测试用显式代理与豁免表，
// 因为 http.ProxyFromEnvironment 在进程内只读一次环境变量、缓存后改不动）。
func newSafeTransport(opts Options, exempt map[string]bool, proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   dialControl(opts, exempt),
	}

	return &http.Transport{
		Proxy:       proxy,
		DialContext: dialer.DialContext,
		// 不启用 HTTP/2：不少音乐/接口站挂在 CDN 边缘（openresty / EdgeOne）上，
		// 明明宣告支持 h2，但 Go 的 h2 客户端会被挂住 —— 实测「响应头 20s 超时」
		// 与「偶发 5s 延迟」，而同一地址走 HTTP/1.1 只要 0.2s（2026-09-27 真机）。
		// 出站请求不需要多路复用，稳定优先。
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
}

// dialControl 返回 Dialer.Control：在 TCP 连接真正建立前校验「实际拨号的地址」。
//
// 唯一豁免：环境变量里配置的代理出口（HTTP_PROXY / HTTPS_PROXY / ALL_PROXY）。
// 这是用户自己指定的出站通道，不是被请求方控制的 SSRF 目标；而一个请求只有在
// proxyFunc 判定「目标是公网」时才会交给它，所以放行「拨号到代理本身」不会绕过
// 目标侧的校验（目标侧仍由 ValidateURL 的字面量校验 + 其它直连路径的 CheckIP 兜住）。
// 没写在环境里的私网地址照旧一律拦截 —— 豁免是一张固定白名单，不是「私网全开」。
func dialControl(opts Options, exempt map[string]bool) func(network, address string, _ syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if len(exempt) > 0 && exempt[normalizeAddr(address)] {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("目标地址格式非法: %s", address)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("目标地址无法解析: %s", host)
		}
		return CheckIP(ip, opts)
	}
}

// normalizeAddr 把 "192.168.1.3:7890" / "[::1]:7890" 归一成小写 host:port，便于比较。
func normalizeAddr(address string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(address))
	}
	return strings.ToLower(net.JoinHostPort(host, port))
}

// proxyExemptAddrs 收集环境变量里配置的代理出口地址（host:port）。
// Go 的 http.ProxyFromEnvironment 认 HTTP_PROXY / HTTPS_PROXY / NO_PROXY（大小写都读），
// ALL_PROXY 是其它工具链的约定，这里一并收进来，免得 socks 代理踩同一个坑。
func proxyExemptAddrs() map[string]bool {
	out := map[string]bool{}
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			if strings.EqualFold(u.Scheme, "https") {
				port = "443"
			} else {
				port = "80"
			}
		}
		out[strings.ToLower(net.JoinHostPort(u.Hostname(), port))] = true
	}
	return out
}

// proxyFunc 决定单个请求是否走代理。
//
// 内网与回环目标默认直连——因为代理自身通常就在内网，走代理会被 SSRF 规则拦下，
// 形成「proxyconnect tcp: dial tcp 192.168.x.x:7890: 禁止访问内网/元数据地址」的死锁。
//
// 但存在一种真实场景：AI 服务监听在 NAS 自己身上，用局域网 IP（如 192.168.1.66:20128）
// 访问时因 hairpin / 路由策略不通，反而必须经代理才能到达。
// 因此提供 FN_PROXY_PRIVATE_NET=1 让内网目标也走代理，由用户按环境决定。
func proxyFunc(opts Options) func(*http.Request) (*url.URL, error) {
	envProxy := http.ProxyFromEnvironment
	proxyPrivate := envBool("FN_PROXY_PRIVATE_NET")

	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		if opts.DisableProxy {
			return nil, nil // 显式要求直连
		}
		if proxyRecentlyFailed() {
			return nil, nil // 刚发现代理不通：冷却期内直接直连，别让每个请求都先卡一下
		}
		if isLocalTarget(req.URL.Hostname()) && !proxyPrivate && !opts.ForceProxyForLocal {
			return nil, nil // 内网/回环目标直连，绕过代理
		}
		return envProxy(req)
	}
}

// isLocalTarget 判断目标主机是否为内网/回环/链路本地地址（含 localhost 与裸主机名）
func isLocalTarget(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 不含点的裸主机名通常是内网短名（nas、ai-server 等）
		return !strings.Contains(host, ".")
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || isPrivateOrMetadata(ip)
}

// IsLocalTarget 对外暴露的内网目标判断（供调用方决定是否需要放宽策略）
func IsLocalTarget(host string) bool {
	return isLocalTarget(host)
}

// NewSafeClient 返回带 SSRF 防护与超时的 HTTP 客户端
func NewSafeClient(opts Options, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &http.Client{
		Transport: NewSafeRoundTripper(opts),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			// 每一跳都重新校验，防止跳转到内网
			if _, err := ValidateURL(req.URL.String(), opts); err != nil {
				return err
			}
			return nil
		},
	}
}

// LimitedRead 读取至多 opts.MaxBodyBytes 字节；超限返回错误而非静默截断
func LimitedRead(r io.Reader, opts Options) ([]byte, error) {
	max := opts.maxBody()
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("响应体超过上限 %d 字节", max)
	}
	return data, nil
}

// LimitedReadN 读取至多 max 字节；超限返回错误
func LimitedReadN(r io.Reader, max int64) ([]byte, error) {
	return LimitedRead(r, Options{MaxBodyBytes: max, AllowPrivateNet: true})
}

// isProxyConnError 判断出站错误是不是「连接层根本没通」（而不是对端返回的业务错误）。
//
// 只用来决定「要不要换条通道再试一次」：连接层没通意味着这次请求根本没到目标，
// 重试不会造成重复写入；反过来只要已经拿到响应（哪怕 5xx）就绝不重试。
func isProxyConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, e := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ETIMEDOUT, syscall.EPIPE} {
		if errors.Is(err, e) {
			return true
		}
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true // 超时/临时性网络错误
	}
	msg := err.Error()
	for _, frag := range []string{"proxyconnect", "EOF", "connection refused", "connection reset", "no route to host", "i/o timeout", "network is unreachable", "TLS handshake"} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// proxyFallbackTransport 在「走了代理、但代理这条路不通」时改用直连重试一次。
//
// 为什么需要：开发机与 NAS 上常见全局 HTTP_PROXY。代理是加速手段，不是唯一出路 ——
// 代理的 CONNECT 隧道坏掉时（典型表现：明文 HTTP 通、HTTPS 一律 EOF），所有公网出站
// 都会失败，应用层只能看到「取链 502 / 解析失败」且无法自愈。于是：只有在本请求确定
// 交给了代理、且连接层就没通的条件下才直连再试一次；直连再失败则返回**代理那条**错误
// （更贴近用户配置的意图，便于排查）。
//
// 直连通道沿用同一套 SSRF 校验（dialControl + CheckIP + ValidateURL），
// 不是「代理失败就放松安全」。
type proxyFallbackTransport struct {
	proxied  http.RoundTripper
	direct   http.RoundTripper
	proxyFor func(*http.Request) (*url.URL, error)
	// attemptTimeout > 0 时给「经代理的那一次尝试」加个上限（流式中继传 0）。
	attemptTimeout time.Duration
}

func (t *proxyFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var proxyURL *url.URL
	if t.proxyFor != nil {
		u, perr := t.proxyFor(req)
		if perr == nil {
			proxyURL = u
		}
	}

	if proxyURL == nil || t.direct == nil {
		// 本来就走直连（或没有直连通道可用）：照常发，不发生降级
		return t.proxied.RoundTrip(req)
	}

	tryReq := req
	var cancel context.CancelFunc
	if t.attemptTimeout > 0 {
		var ctx context.Context
		ctx, cancel = context.WithTimeout(req.Context(), t.attemptTimeout)
		tryReq = req.Clone(ctx)
	}

	resp, err := t.proxied.RoundTrip(tryReq)
	hitCap := err != nil && cancel != nil && tryReq.Context().Err() == context.DeadlineExceeded && req.Context().Err() == nil
	if cancel != nil {
		cancel()
	}
	if err == nil {
		return resp, nil
	}
	if !hitCap && !isProxyConnError(err) {
		return resp, err // 不是连接层问题（业务错误/对端响应），不降级
	}

	// 代理这条路判定为不通：进入冷却期，之后一段时间内所有请求直接直连
	noteProxyFailure()

	if req.Body != nil && req.GetBody == nil {
		return resp, err // 请求体不能重放，宁可不重试也不发一半
	}
	clone := req.Clone(req.Context())
	if req.GetBody != nil {
		body, berr := req.GetBody()
		if berr != nil {
			return resp, err
		}
		clone.Body = body
	}
	out, derr := t.direct.RoundTrip(clone)
	if derr != nil {
		return resp, err // 原始错误（代理那条）更有诊断价值
	}
	log.Printf("[SECURITY] 代理 %s 不通（%v），已改用直连：%s（%s 内不再尝试代理）",
		proxyURL.Host, err, req.URL.Host, proxyBadCooldown)
	return out, nil
}

// NewSafeRoundTripper 返回带 SSRF 防护 + 「代理不通自动直连」降级的 RoundTripper，
// 供自己管理超时/重定向的调用方使用。
//
// 会给「走代理的那一次尝试」加一个短上限（proxyAttemptTimeout）：代理卡住时不能
// 把调用方的整个超时预算耗光，得留出直连的时间。因此**返回流式响应体**的场景要用
// NewSafeStreamRoundTripper（那里不能有上限，否则长音频流会被掐断）。
func NewSafeRoundTripper(opts Options) http.RoundTripper {
	return newFallbackTransport(opts, proxyAttemptTimeout)
}

// NewSafeStreamRoundTripper 用于流式中继：不加单次尝试上限，只保留
// 「连接层失败 → 直连一次」的降级，保证响应体可以被长时间读取。
func NewSafeStreamRoundTripper(opts Options) http.RoundTripper {
	return newFallbackTransport(opts, 0)
}

func newFallbackTransport(opts Options, attemptTimeout time.Duration) http.RoundTripper {
	exempt := proxyExemptAddrs()
	proxyFor := proxyFunc(opts)
	return &proxyFallbackTransport{
		proxied:        newSafeTransport(opts, exempt, proxyFor),
		direct:         newSafeTransport(opts, exempt, func(*http.Request) (*url.URL, error) { return nil, nil }),
		proxyFor:       proxyFor,
		attemptTimeout: attemptTimeout,
	}
}

// proxyAttemptTimeout 单次「经代理」尝试的上限：代理不通时要尽快掉头走直连。
const proxyAttemptTimeout = 5 * time.Second

// proxyBadCooldown 发现代理不通后，这段时间内不再尝试代理（避免每个请求都先卡一下）。
const proxyBadCooldown = 60 * time.Second

var proxyFailUntil atomic.Int64 // unix 纳秒；0 表示没记录

// proxyRecentlyFailed 代理是否在冷却期内（刚被判定为不通）。
func proxyRecentlyFailed() bool {
	until := proxyFailUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}

// noteProxyFailure 记下「代理不通」，进入冷却期。冷却期结束会自动再试一次代理。
func noteProxyFailure() {
	proxyFailUntil.Store(time.Now().Add(proxyBadCooldown).UnixNano())
}

// resetProxyFailureState 清掉冷却记录（测试用）。
func resetProxyFailureState() {
	proxyFailUntil.Store(0)
}
