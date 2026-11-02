package appupdate

// 「已配置代理就先测速，通了优先走代理，不通回退官方源」。
//
// 用户 2026-11-02 的第 6 项要求：「镜像网络代理下拉框：若已配置代理，自动测试
// 代理速度；测试成功则优先使用代理，失败则回退到官方源」。
//
// 为什么需要它：代理是「填进去就一直用」的配置，但代理会失效 —— 关机、换
// 网络、订阅到期、代理软件没起。以前配了代理之后，检查更新会一直撞在一个死代理
// 上直到用户自己想起来去清掉；而在代理其实通的情况下，先试镜像（可能被限速）
// 也不见得比直连代理快。
//
// 这里的判定故意**不猜**，只测一次真东西：经代理请求 release 接口，拿到一个
// 合法的 tag_name 才算「通」。测不到就明确降级成「这次不用代理」，并把结论
// 写进 CheckResult 让界面能说清「为什么这次没走代理」—— 静默换路是最难排查的
// 那种 bug（用户会以为是代理没生效）。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProxyProbeResult 是一次代理测速的结果。
type ProxyProbeResult struct {
	// Configured 用户是否配了代理。false 时其余字段无意义。
	Configured bool `json:"configured"`
	// URL 是归一后的代理地址（凭据已抹掉，界面只做展示）。
	URL string `json:"url,omitempty"`
	// OK 表示经代理真的拿到了 release 接口的合法响应。
	OK bool `json:"ok"`
	// LatencyMS 是这一趟往返的毫秒数（OK 为真时才有意义）。
	LatencyMS int64 `json:"latency_ms,omitempty"`
	// Version 是这次探测拿到的版本号（能确认代理不只是「连得上」）。
	Version string `json:"version,omitempty"`
	// Error 是失败原因（人话，见 briefErr）。
	Error string `json:"error,omitempty"`
	// CheckedAt 是探测时间（RFC3339）。
	CheckedAt string `json:"checked_at,omitempty"`
	// PreferredProxy 是这次探测的结论：true = 后续检查/下载优先走代理。
	PreferredProxy bool `json:"preferred_proxy"`
}

// probeTimeout 是单次代理测速的上限。
//
// 为什么是 8 秒而不是 sourceAttemptWindow(40s)：这是**界面上的一个按钮**，
// 用户盯着等；一个 8 秒还没响应的代理，拿来下几十 MB 只会更惨。
// 宁可判它不通、当场回退官方源，也不要让人对着转圈等 40 秒。
const probeTimeout = 8 * time.Second

// proxyDisplay 把代理地址里的凭据抹掉再返回（界面/日志展示用）。
//
// 代理地址可能形如 http://user:pass@host:port，直接回显会漏到截图、日志里。
func proxyDisplay(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	scheme, rest := "", raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, rest = raw[:i+3], raw[i+3:]
	}
	if j := strings.Index(rest, "@"); j >= 0 {
		return scheme + "***@" + rest[j+1:]
	}
	return raw
}

// ProbeProxy 测一次「经当前配置的代理能不能真的访问更新源」。
//
// 只读、无副作用（不改配置、不动下载状态），可以随意调用。走的是官方 release
// 接口：这是整条更新链的第一步，它通不通决定了后面所有事。
func (m *Manager) ProbeProxy(ctx context.Context) ProxyProbeResult {
	raw := ""
	if m.proxyFunc != nil {
		raw = strings.TrimSpace(m.proxyFunc())
	}
	res := ProxyProbeResult{
		Configured: raw != "",
		URL:        proxyDisplay(raw),
		CheckedAt:  m.now().Format(time.RFC3339),
	}
	if raw == "" {
		return res
	}

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	start := m.now()
	rel, err := FetchLatestRelease(pctx, newUpdateClient(raw), m.officialReleaseURL(), m.repo, m.tokenNow(), m.ua)
	elapsed := m.now().Sub(start)
	res.LatencyMS = elapsed.Milliseconds()
	if err != nil {
		res.Error = briefErr(err)
		m.logf("[UPDATE] 代理测速失败（%s）：%v —— 本次回退到直连/官方源", res.URL, err)
		return res
	}
	if strings.TrimSpace(rel.TagName) == "" {
		res.Error = "代理连上了，但更新源没返回版本号"
		m.logf("[UPDATE] 代理测速异常：%s 返回内容里没有 tag_name，本次回退", res.URL)
		return res
	}
	res.OK = true
	res.Version = strings.TrimSpace(strings.TrimPrefix(rel.TagName, "v"))
	res.PreferredProxy = true
	m.logf("[UPDATE] 代理测速成功：%s 用时 %dms，版本 %s —— 本次优先走代理", res.URL, res.LatencyMS, res.Version)
	return res
}

// proxyPreferred 判断「这次检查/下载要不要用代理」。
//
// 规则（用户 2026-11-02 第 6 项）：
//   - 没配代理 → 不用（返回空串，走原有环境变量/直连逻辑）；
//   - 配了代理 → 先测速；通了就优先用代理，不通就回退（返回空串 = 直连 + 官方源）。
//
// 返回值是**代理地址**而不是 bool：调用方要的是能直接塞进 client 的值，
// 回退时给空串就等于「这一次不绕代理」。检查与下载必须用同一套结论，
// 否则会出现「检查走了代理、下载回退直连」这种前后不一致的行为。
func (m *Manager) proxyPreferred(ctx context.Context) (string, ProxyProbeResult) {
	raw := ""
	if m.proxyFunc != nil {
		raw = strings.TrimSpace(m.proxyFunc())
	}
	if raw == "" {
		return "", ProxyProbeResult{Configured: false, CheckedAt: m.now().Format(time.RFC3339)}
	}
	res := m.ProbeProxy(ctx)
	if !res.OK {
		return "", res
	}
	return raw, res
}

// Info 是给界面看的结论摘要（一行字）。
func (r ProxyProbeResult) Info() string {
	if !r.Configured {
		return "未配置代理"
	}
	if r.OK {
		return fmt.Sprintf("代理可用（%dms），本次优先走代理", r.LatencyMS)
	}
	if r.Error != "" {
		return "代理不可用：" + r.Error + "，本次回退到官方源"
	}
	return "代理不可用，本次回退到官方源"
}
