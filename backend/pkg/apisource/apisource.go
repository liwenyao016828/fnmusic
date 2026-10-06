// Package apisource 支持「按服务地址」接入音源。
//
// 背景：第三方音源不只有「一段 JS 脚本」这一种形态。还有一类是**自建服务**——
// 用户自己（或社区）部署一个中转服务，客户端只负责把 {平台, 歌曲ID, 音质} 发过去、
// 拿回一条可播直链。这种音源没法在浏览器里执行，也不需要执行：
// 它就是一个普通的 HTTP 接口。
//
// 本包实现三种协议（规格对照 fnmusic-flow 的 lx_source.py 核实）：
//
//	lx_script  私有 REST：  GET  {base}/url?source=&songId=&quality=   鉴权 X-API-Key
//	lx_server  标准 LX Server：GET  {base}/api/music/url?source=&id=&quality=  鉴权 x-user-token
//	lx_api     ikun/自建中转：POST {base}/music/url  body{source,musicId,quality}  鉴权 X-Api-Key
//
// 三者的**路由互不相同**（/url、/api/music/url、/music/url），所以可以靠探测区分，
// 不会互相误判。探测判据：目标路由返回 404 视为该协议不存在；
// 返回 JSON 且含 url/code/data 字段即视为协议匹配 ——
// 即使探针歌曲本身解析失败（比如版权下架），也说明端点与协议是对的。
//
// 合规说明：这里**不执行任何第三方 JS**，只是把用户自己填的服务地址当成一个 HTTP 接口调用。
// 出站请求仍走 pkg/security 的守卫（协议白名单、响应体上限、元数据地址拦截），
// 但**本包自己放开内网/回环访问**（见下方 opts() 的理由）——
// 内网自建音源不需要 FN_ALLOW_PRIVATE_NET，那个变量管的是代理等其余路径。
package apisource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fn-lx-player/pkg/security"
)

// 三种协议标识
const (
	ProtocolLxScript = "lx_script"
	ProtocolLxServer = "lx_server"
	ProtocolLxAPI    = "lx_api"
)

// probeSongID 探测用的公开歌曲（网易云）。用一个长期存在的热门曲，
// 避免「探针歌曲下架导致整个音源被误判为不可用」。
const probeSongID = "421423808"

// probeQuality 探测时请求的音质档位：取最低档，减少对上游的压力
const probeQuality = "128k"

// ProtocolLabel 给界面用的中文名
func ProtocolLabel(protocol string) string {
	switch protocol {
	case ProtocolLxScript:
		return "私有 REST（GET /url）"
	case ProtocolLxServer:
		return "标准 LX Server（GET /api/music/url）"
	case ProtocolLxAPI:
		return "自建中转 API（POST /music/url）"
	}
	return protocol
}

// NormalizeProtocol 归一化协议标识；空串按「未识别」处理（由探测决定）
func NormalizeProtocol(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case ProtocolLxScript:
		return ProtocolLxScript
	case ProtocolLxServer:
		return ProtocolLxServer
	case ProtocolLxAPI:
		return ProtocolLxAPI
	}
	return ""
}

// opts 服务型音源的出站安全策略。
//
// 安全取舍（**与 `pkg/ai` 的 AI 服务地址完全一致**）：音源服务地址是用户在
// 「音源管理」里**显式配置**的可信目标，且现实中大量是局域网自建
// （LX Music Server / music-api / 自建中转等）。目标来自用户配置而非请求参数，
// 不存在 SSRF 面，因此这里允许访问内网/回环地址。
//
// ⚠️ 不这么做的话，内网自建音源在**「填地址 → 探测协议」这一步就被拒**（报错
// 「禁止访问回环地址 127.0.0.1」），整个服务型音源功能在内网环境下不可用 ——
// 而后端自动下载（v2.1.11）正是靠它取链的。飞牛 FPK 也没有给用户配环境变量的入口。
//
// 注意作用域：**只放开本包**。`FN_ALLOW_PRIVATE_NET` 的默认拒绝语义对代理等其余路径不变。
func opts() security.Options {
	o := security.DefaultOptions()
	o.AllowPrivateNet = true
	return o
}

// client 探测与取链共用的出站客户端。
// 超时给 20s：自建服务可能在冷启动，太短会误判为不可用。
func client() *http.Client {
	return security.NewSafeClient(opts(), 20*time.Second)
}

// normalizeBase 去掉尾部斜杠并校验地址合法
func normalizeBase(baseURL string) (string, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return "", fmt.Errorf("服务地址不能为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "", fmt.Errorf("服务地址必须以 http:// 或 https:// 开头，收到 %q", raw)
	}
	u, err := security.ValidateURL(raw, opts())
	if err != nil {
		return "", err
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// DetectResult 探测结果
type DetectResult struct {
	Protocol string `json:"protocol"`
	Label    string `json:"label"`
	Detail   string `json:"detail"`
}

// Detect 依次尝试三种协议，返回识别出的协议。
//
// 顺序：私有 REST → 标准 LX Server → 自建中转 API。
// 三者路由不同，先命中先返回；都不通时返回带三种尝试结果的错误，便于排查。
func Detect(baseURL, token string) (*DetectResult, error) {
	base, err := normalizeBase(baseURL)
	if err != nil {
		return nil, err
	}
	c := client()

	type attempt struct {
		protocol string
		fn       func(*http.Client, string, string) (bool, string)
	}
	attempts := []attempt{
		{ProtocolLxScript, probePrivateREST},
		{ProtocolLxServer, probeLXServer},
		{ProtocolLxAPI, probeMusicAPI},
	}

	var tried []string
	for _, a := range attempts {
		ok, detail := a.fn(c, base, token)
		if ok {
			return &DetectResult{
				Protocol: a.protocol,
				Label:    ProtocolLabel(a.protocol),
				Detail:   detail,
			}, nil
		}
		tried = append(tried, fmt.Sprintf("%s：%s", ProtocolLabel(a.protocol), detail))
	}

	return nil, fmt.Errorf("三种协议都没探通，请确认服务地址是否正确、服务是否在运行。\n%s",
		strings.Join(tried, "\n"))
}

// ── 三种协议的探测 ──

// probePrivateREST 私有 REST：GET {base}/url
func probePrivateREST(c *http.Client, base, token string) (bool, string) {
	u := fmt.Sprintf("%s/url?source=wy&songId=%s&quality=%s", base, probeSongID, probeQuality)
	headers := map[string]string{"Accept": "application/json", "User-Agent": "fn-lx-player/probe"}
	if token != "" {
		headers["X-API-Key"] = token
	}
	return probeGET(c, u, headers)
}

// probeLXServer 标准 LX Server：GET {base}/api/music/url
func probeLXServer(c *http.Client, base, token string) (bool, string) {
	u := fmt.Sprintf("%s/api/music/url?source=wy&id=%s&quality=%s", base, probeSongID, probeQuality)
	headers := map[string]string{"Accept": "application/json", "User-Agent": "fn-lx-player/probe"}
	if token != "" {
		headers["x-user-token"] = token
	}
	return probeGET(c, u, headers)
}

// probeMusicAPI 自建中转：POST {base}/music/url
func probeMusicAPI(c *http.Client, base, token string) (bool, string) {
	body, _ := json.Marshal(map[string]any{
		"source": "wy", "musicId": probeSongID, "quality": probeQuality,
	})
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   "fn-lx-player/probe",
	}
	if token != "" {
		headers["X-Api-Key"] = token
	}
	return probePOST(c, base+"/music/url", headers, body)
}

// probeGET 发一次 GET 并判定是否为该协议。
// 404 明确表示「该路由不存在」→ 不是这个协议；其余状态码只要返回 JSON 就认。
func probeGET(c *http.Client, rawURL string, headers map[string]string) (bool, string) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false, "请求构造失败"
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doProbe(c, req)
}

func probePOST(c *http.Client, rawURL string, headers map[string]string, body []byte) (bool, string) {
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return false, "请求构造失败"
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doProbe(c, req)
}

func doProbe(c *http.Client, req *http.Request) (bool, string) {
	resp, err := c.Do(req)
	if err != nil {
		return false, shortErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, "路由不存在（404）"
	}
	// 只读前 64KB 判断形状即可
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return false, fmt.Sprintf("返回的不是 JSON（HTTP %d）", resp.StatusCode)
	}

	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return false, fmt.Sprintf("JSON 解析失败（HTTP %d）", resp.StatusCode)
	}
	// 只要出现了这些字段之一，就说明端点与协议对上了 ——
	// 探针歌曲解析失败（code 非 200）也算匹配，否则会把「歌下架」误判成「音源坏了」
	for _, k := range []string{"url", "code", "data", "message", "msg"} {
		if _, ok := data[k]; ok {
			if u, _ := data["url"].(string); u != "" {
				return true, "解析接口正常，已返回可播地址"
			}
			if inner, ok := data["data"].(map[string]any); ok {
				if u, _ := inner["url"].(string); u != "" {
					return true, "解析接口正常，已返回可播地址"
				}
			}
			return true, "解析接口可达（探针歌曲未返回地址，但协议正确）"
		}
	}
	return false, "返回的 JSON 里没有 url/code/data 字段，不像音源接口"
}

// ── 取直链 ──

// Resolve 按指定协议取一条可播直链。
func Resolve(protocol, baseURL, token, platform, songID, quality string) (string, error) {
	base, err := normalizeBase(baseURL)
	if err != nil {
		return "", err
	}
	p := NormalizeProtocol(protocol)
	if p == "" {
		return "", fmt.Errorf("未知协议 %q，请先探测或指定 lx_script / lx_server / lx_api", protocol)
	}

	src := mapPlatform(platform)
	if src == "" {
		return "", fmt.Errorf("不支持的平台 %q（可用 wy / tx / kg / kw）", platform)
	}
	if strings.TrimSpace(songID) == "" {
		return "", fmt.Errorf("缺少歌曲 ID")
	}
	if strings.TrimSpace(quality) == "" || strings.EqualFold(quality, "highest") {
		quality = "320k"
	}

	switch p {
	case ProtocolLxScript:
		return resolvePrivateREST(base, token, src, songID, quality)
	case ProtocolLxServer:
		return resolveLXServer(base, token, src, songID, quality)
	case ProtocolLxAPI:
		return resolveMusicAPI(base, token, src, songID, quality)
	}
	return "", fmt.Errorf("未实现的协议 %q", p)
}

func resolvePrivateREST(base, token, source, songID, quality string) (string, error) {
	u := fmt.Sprintf("%s/url?source=%s&songId=%s&quality=%s",
		base, url.QueryEscape(source), url.QueryEscape(songID), url.QueryEscape(quality))
	headers := map[string]string{"Accept": "application/json"}
	if token != "" {
		headers["X-API-Key"] = token
	}
	data, err := getJSON(u, headers)
	if err != nil {
		return "", err
	}
	if link := firstURL(data); link != "" {
		return link, nil
	}
	return "", fmt.Errorf("音源未返回有效地址（响应：%s）", briefJSON(data))
}

func resolveLXServer(base, token, source, songID, quality string) (string, error) {
	u := fmt.Sprintf("%s/api/music/url?source=%s&id=%s&quality=%s",
		base, url.QueryEscape(source), url.QueryEscape(songID), url.QueryEscape(quality))
	headers := map[string]string{"Accept": "application/json"}
	if token != "" {
		headers["x-user-token"] = token
	}
	data, err := getJSON(u, headers)
	if err != nil {
		return "", err
	}
	if link := firstURL(data); link != "" {
		return link, nil
	}
	return "", fmt.Errorf("LX Server 未返回有效地址（响应：%s）", briefJSON(data))
}

func resolveMusicAPI(base, token, source, songID, quality string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"source": source, "musicId": songID, "quality": quality,
	})
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   "lx-music-request/2.11.0",
	}
	if token != "" {
		headers["X-Api-Key"] = token
	}
	data, err := postJSON(base+"/music/url", headers, body)
	if err != nil {
		return "", err
	}
	if link := firstURL(data); link != "" {
		return link, nil
	}
	return "", fmt.Errorf("音源未返回有效地址（响应：%s）", briefJSON(data))
}

// mapPlatform 把内部平台代号映射成各服务约定俗成的名字。
// 网易云与 QQ 在多数自建服务里用 netease / qq。
func mapPlatform(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "wy", "netease":
		return "netease"
	case "tx", "qq":
		return "qq"
	case "kg", "kugou":
		return "kugou"
	case "kw", "kuwo":
		return "kuwo"
	}
	return ""
}

// firstURL 从响应里取直链：兼容 {url} 与 {data:{url}} 两种形状
func firstURL(data map[string]any) string {
	if u, ok := data["url"].(string); ok && strings.TrimSpace(u) != "" {
		return strings.TrimSpace(u)
	}
	if inner, ok := data["data"].(map[string]any); ok {
		if u, ok := inner["url"].(string); ok && strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
	}
	return ""
}

func getJSON(rawURL string, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doJSON(client(), req)
}

func postJSON(rawURL string, headers map[string]string, body []byte) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doJSON(client(), req)
}

func doJSON(c *http.Client, req *http.Request) (map[string]any, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求音源失败: %s", shortErr(err))
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("音源返回 HTTP %d：%s", resp.StatusCode, briefText(string(raw)))
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("音源返回的不是有效 JSON：%s", briefText(string(raw)))
	}
	return data, nil
}

func briefJSON(data map[string]any) string {
	b, _ := json.Marshal(data)
	return briefText(string(b))
}

func briefText(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

func shortErr(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout") {
		return "请求超时（服务未响应）"
	}
	r := []rune(msg)
	if len(r) > 100 {
		return string(r[:100]) + "…"
	}
	return msg
}
