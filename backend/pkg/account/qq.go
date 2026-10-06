package account

// QQ 音乐扫码登录。
//
// 移植自 fnmusic-flow 内置的 pockettune-server（qqmusic/qq-auth.mjs）。
// 完整流程：
//  1. GET  ssl.ptlogin2.qq.com/ptqrshow          取二维码图片与 qrsig
//  2. GET  ssl.ptlogin2.qq.com/ptqrlogin         轮询；ptqrtoken = hash33(qrsig)
//     回调 ptuiCB 首参：65 过期 / 66 未扫 / 67 已扫 / 0 成功（带跳转地址）
//  3. GET  {跳转地址}（不跟随重定向）              换取 p_skey
//  4. POST graph.qq.com/oauth2.0/authorize       用 p_skey 换 code（g_tk = hash33(p_skey, 5381)）
//  5. POST u.y.qq.com/cgi-bin/musicu.fcg         QQConnectLogin.LoginServer.QQLogin 换音乐凭据
//
// 凭据以 Cookie 形式保存，字段映射见 credentialToSession。

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	qqAppID    = "716027609"
	qqClientID = "100497308"
	qqWebUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	qqMobileUA = "QQMusic 14090008(android 15)"
	qqMaxBody  = 4 << 20
)

// 各上游主机；声明为变量以便测试注入。
var (
	qqPtloginBase = "https://ssl.ptlogin2.qq.com"
	// qqGraphPtloginBase 是换 p_skey 的专用入口，与 qqPtloginBase 不是同一台。
	// ⚠️ 见 qqFinishLogin 的注释：漏掉这一步是「授权没有返回 p_skey」的根因。
	qqGraphPtloginBase = "https://ssl.ptlogin2.graph.qq.com"
	qqGraphBase        = "https://graph.qq.com"
	qqMusicBase        = "https://u.y.qq.com"
	// qqCGIBase 为网页版 fcg 网关，日推以外的歌单类接口走这里
	qqCGIBase = "https://c.y.qq.com"
)

// 扫码状态
const (
	QQStateWaiting = "waiting"
	QQStateScanned = "scanned"
	QQStateExpired = "expired"
	QQStateSuccess = "success"
)

// ptuiCB 回调首参
const (
	qqCBExpired = "65"
	qqCBScanned = "67"
	qqCBSuccess = "0"
)

// hash33 QQ 的 ptqrtoken / g_tk 哈希函数。
func hash33(value string, seed uint32) uint32 {
	hash := seed
	for _, ch := range value {
		hash = ((hash << 5) + hash + uint32(ch)) & 0xffffffff
	}
	return hash & 0x7fffffff
}

// ── Cookie 辅助 ──

// cookiesToHeader 把 Cookie map 拼成请求头值。
func cookiesToHeader(cookies map[string]string) string {
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// parseCookieHeader 解析 Cookie 头为 map。
func parseCookieHeader(header string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(header, ";") {
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:idx])
		value := strings.TrimSpace(part[idx+1:])
		if name != "" && value != "" {
			out[name] = value
		}
	}
	return out
}

// mergeResponseCookies 把响应里的 Set-Cookie 合并进 existing。
func mergeResponseCookies(resp *http.Response, existing map[string]string) map[string]string {
	out := make(map[string]string, len(existing))
	for k, v := range existing {
		out[k] = v
	}
	for _, raw := range resp.Header.Values("Set-Cookie") {
		pair := raw
		if idx := strings.Index(raw, ";"); idx >= 0 {
			pair = raw[:idx]
		}
		if idx := strings.Index(pair, "="); idx > 0 {
			name := strings.TrimSpace(pair[:idx])
			value := strings.TrimSpace(pair[idx+1:])
			if name != "" {
				out[name] = value
			}
		}
	}
	return out
}

// qqAccountID 从 Cookie 中推导账号 ID，与上游 getQQAccountId 一致。
func qqAccountID(cookies map[string]string) string {
	for _, key := range []string{"qm_str_musicid", "uin", "wxuin", "p_uin"} {
		if v := cookies[key]; v != "" {
			v = strings.TrimPrefix(v, "o")
			// 去掉前导零（但保留单个 0）
			trimmed := strings.TrimLeft(v, "0")
			if trimmed == "" {
				trimmed = "0"
			}
			return trimmed
		}
	}
	return ""
}

// ── HTTP 辅助 ──

// noRedirectClient 不跟随重定向，便于读取 Location 头。
var noRedirectClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var followClient = &http.Client{Timeout: 20 * time.Second}

func qqGet(client *http.Client, rawURL string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

// ── 扫码登录 ──

// QQLogin 一次扫码会话。
type QQLogin struct {
	Key string `json:"key"` // qrsig
	// Image 为可直接用于 <img src> 的 data URL
	Image string `json:"image"`
}

// QQCreateQR 申请 QQ 登录二维码。
//
// 2026-09-19 起优先走「QQ音乐 App 扫码」通道（官方网页同款，见 qq_mobile_qr.go，
// 完全绕开 QQ 互联通道的风控点 check_sig）；申请失败时回退旧 QQ 互联通道。
func QQCreateQR() (QQLogin, error) {
	qr, err := QQMobileCreateQR()
	if err == nil {
		return qr, nil
	}
	log.Printf("[QQ扫码] APP 通道申请失败，回退 QQ 互联通道: %v", err)
	return qqCreatePTQR()
}

// qqCreatePTQR 旧的 QQ 互联通道申请（ptqrshow，需手机 QQ 扫）。
func qqCreatePTQR() (QQLogin, error) {
	rawURL := fmt.Sprintf("%s/ptqrshow?appid=%s&e=2&l=M&s=3&d=72&v=4&t=%f&daid=383&pt_3rd_aid=%s",
		qqPtloginBase, qqAppID, float64(time.Now().UnixNano())/1e9, qqClientID)

	resp, err := qqGet(followClient, rawURL, map[string]string{
		"Referer":    "https://xui.ptlogin2.qq.com/",
		"User-Agent": qqWebUA,
	})
	if err != nil {
		return QQLogin{}, fmt.Errorf("获取 QQ 二维码失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return QQLogin{}, fmt.Errorf("获取 QQ 二维码失败: HTTP %d", resp.StatusCode)
	}

	image, err := io.ReadAll(io.LimitReader(resp.Body, qqMaxBody))
	if err != nil {
		return QQLogin{}, fmt.Errorf("读取二维码图片失败: %w", err)
	}
	key := mergeResponseCookies(resp, nil)["qrsig"]
	if key == "" {
		return QQLogin{}, errors.New("QQ 登录没有返回 qrsig")
	}

	return QQLogin{
		Key:   key,
		Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(image),
	}, nil
}

// QQCheckResult 扫码状态查询结果。
type QQCheckResult struct {
	State    string            `json:"state"` // waiting / scanned / expired / success
	Nickname string            `json:"nickname,omitempty"`
	Cookies  map[string]string `json:"-"` // 仅 success 时有值
}

var (
	ptuiCBPattern = regexp.MustCompile(`ptuiCB\((.*?)\)`)
	// 回调参数为单引号字符串，可能含转义
	cbArgPattern = regexp.MustCompile(`'((?:\\.|[^'])*)'`)
)

// QQCheckQR 查询扫码状态；按 key 自动分派 APP 通道 / QQ 互联通道。
func QQCheckQR(key string) (QQCheckResult, error) {
	if qqMobileLoad(key) != nil {
		return QQMobileCheckQR(key)
	}
	return qqCheckPTQR(key)
}

// qqCheckPTQR 旧的 QQ 互联通道轮询（ptqrlogin → check_sig → authorize）。
func qqCheckPTQR(key string) (QQCheckResult, error) {
	if strings.TrimSpace(key) == "" {
		return QQCheckResult{}, errors.New("二维码 key 不能为空")
	}

	query := url.Values{}
	query.Set("u1", "https://graph.qq.com/oauth2.0/login_jump")
	query.Set("ptqrtoken", strconv.FormatUint(uint64(hash33(key, 0)), 10))
	query.Set("ptredirect", "0")
	query.Set("h", "1")
	query.Set("t", "1")
	query.Set("g", "1")
	query.Set("from_ui", "1")
	query.Set("ptlang", "2052")
	query.Set("action", fmt.Sprintf("0-0-%d", time.Now().UnixMilli()))
	query.Set("js_ver", "20102616")
	query.Set("js_type", "1")
	query.Set("pt_uistyle", "40")
	query.Set("aid", qqAppID)
	query.Set("daid", "383")
	query.Set("pt_3rd_aid", qqClientID)
	query.Set("has_onekey", "1")

	resp, err := qqGet(followClient, qqPtloginBase+"/ptqrlogin?"+query.Encode(), map[string]string{
		"Referer":    "https://xui.ptlogin2.qq.com/",
		"Cookie":     "qrsig=" + key + ";",
		"User-Agent": qqWebUA,
	})
	if err != nil {
		return QQCheckResult{}, fmt.Errorf("查询扫码状态失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, qqMaxBody))

	// 未扫码时上游返回 ptuiCB('66', ...)，首参即状态码
	m := ptuiCBPattern.FindStringSubmatch(string(body))
	if len(m) < 2 {
		return QQCheckResult{State: QQStateWaiting}, nil
	}
	argMatches := cbArgPattern.FindAllStringSubmatch(m[1], -1)
	args := make([]string, 0, len(argMatches))
	for _, am := range argMatches {
		args = append(args, am[1])
	}
	if len(args) == 0 {
		return QQCheckResult{State: QQStateWaiting}, nil
	}

	switch args[0] {
	case qqCBExpired:
		return QQCheckResult{State: QQStateExpired}, nil
	case qqCBScanned:
		nickname := ""
		if len(args) > 5 {
			nickname = args[5]
		}
		return QQCheckResult{State: QQStateScanned, Nickname: nickname}, nil
	case qqCBSuccess:
		if len(args) < 3 || !strings.HasPrefix(args[2], "http") {
			return QQCheckResult{}, errors.New("QQ 登录跳转地址无效")
		}
		cookies, err := qqFinishLogin(args[2])
		if err != nil {
			return QQCheckResult{}, err
		}
		return QQCheckResult{State: QQStateSuccess, Cookies: cookies}, nil
	}
	return QQCheckResult{State: QQStateWaiting}, nil
}

// qqPtSigxPattern / qqUinPattern 从 ptqrlogin 返回的跳转地址里抽参数。
//
// 跳转地址形如 https://graph.qq.com/oauth2.0/login_jump?uin=o123&service=...&ptsigx=abc&s_url=...
// 注意 uin 带前缀 `o`（如 o1234567），后面要原样带上去，不要去前缀。
var (
	qqPtSigxPattern = regexp.MustCompile(`(?:\?|&)ptsigx=(.+?)&s_url`)
	qqUinPattern    = regexp.MustCompile(`(?:\?|&)uin=(.+?)&service`)
)

// qqExchangePsKey 用 uin + ptsigx 换 p_skey，并返回 check_sig **新下发**的 Cookie。
//
// 返回整份新下发 Cookie 而不是只返回 skey 字符串：下一步 authorize 必须带上这份
// （含 p_skey 本身）。但**只带这一份** —— 混入 qrsig/ptqrlogin 的会话 Cookie 会引入风控变量。
//
// ⚠️ 两个真机踩过的点（2026-09-19）：
//   - 这是 QQ 扫码里**最容易漏掉的一步**（历史 bug：漏步骤 → 卡「已扫码」）。
//     走的是 ssl.ptlogin2.graph.qq.com/check_sig，与 ptqrlogin 那台不是同一个域名。
//   - check_sig 请求**必须不带任何 Cookie**（对照 QQMusicApi 裸请求）。
//     带上整个会话 jar（superkey/ETK/RK 等）时腾讯回 `p_skey_forbid` + 空 p_skey，
//     表现就是「扫码确认后报没有 p_skey」。
//
// 参数表对照社区维护的 QQMusicApi（L-1124）逐项抄来，实测无法省项：
// pttype=1 / service=ptqrlogin / ptredirect=100 / aid / daid / pt_3rd_aid 都要带。
func qqExchangePsKey(uin, sigx string) (string, map[string]string, error) {
	query := url.Values{}
	query.Set("uin", uin)
	query.Set("pttype", "1")
	query.Set("service", "ptqrlogin")
	query.Set("nodirect", "0")
	query.Set("ptsigx", sigx)
	query.Set("s_url", qqGraphBase+"/oauth2.0/login_jump")
	query.Set("ptlang", "2052")
	query.Set("ptredirect", "100")
	query.Set("aid", qqAppID)
	query.Set("daid", "383")
	query.Set("j_later", "0")
	query.Set("low_login_hour", "0")
	query.Set("regmaster", "0")
	query.Set("pt_login_type", "3")
	query.Set("pt_aid", "0")
	query.Set("pt_aaid", "16")
	query.Set("pt_light", "0")
	query.Set("pt_3rd_aid", qqClientID)

	resp, err := qqGet(noRedirectClient, qqGraphPtloginBase+"/check_sig?"+query.Encode(), map[string]string{
		"Referer":    "https://xui.ptlogin2.qq.com/",
		"User-Agent": qqWebUA,
	})
	if err != nil {
		return "", nil, fmt.Errorf("换取 QQ 会话失败: %w", err)
	}
	defer resp.Body.Close()

	fresh := mergeResponseCookies(resp, nil)
	skey := firstNonEmpty(fresh["p_skey"], fresh["p_sKey"], fresh["skey"], fresh["pskey"])
	if skey == "" {
		names := make([]string, 0, len(fresh))
		for k := range fresh {
			names = append(names, k)
		}
		sort.Strings(names)
		if _, forbidden := fresh["p_skey_forbid"]; forbidden {
			log.Printf("[QQ扫码] check_sig 被风控：HTTP %d，下发 p_skey_forbid（p_skey 为空），Cookie 名=%v",
				resp.StatusCode, names)
			return "", nil, errors.New("QQ 风控拒绝了这次登录（p_skey_forbid）。稍等几分钟再试；反复出现就换个网络出口")
		}
		log.Printf("[QQ扫码] check_sig 未下发 p_skey：HTTP %d，本响应 Cookie 名=%v", resp.StatusCode, names)
		return "", nil, errors.New("QQ 授权没有返回 p_skey（check_sig 未下发，二维码可能已过期，请重新扫码）")
	}
	log.Printf("[QQ扫码] check_sig 成功换到 p_skey（新下发 Cookie %d 项）", len(fresh))
	return skey, fresh, nil
}

// qqFinishLogin 用跳转地址换取 QQ 音乐凭据 Cookie。
//
// 完整链路（2026-09-19 对照 QQMusicApi / fnmusic-flow 修正）：
//
//	ptqrlogin 返回跳转地址（带 uin + ptsigx）
//	  ↓
//	GET ssl.ptlogin2.graph.qq.com/check_sig?uin=&ptsigx=…  ← ⚠️ 这一步换出 p_skey
//	  ↓
//	POST graph.qq.com/oauth2.0/authorize  （带 p_skey 算出的 g_tk）
//	  ↓
//	拿 code 换 musickey
//
// ⚠️ **曾经的 bug**：直接拿跳转地址去请求，指望响应头里带 p_skey。
// 老版本 QQ 确实会带，现在不会了 —— 必须走 check_sig 换。
// 表现就是「已扫码，请在手机上确认」之后卡住，报「QQ 授权没有返回 p_skey」。
func qqFinishLogin(jumpURL string) (map[string]string, error) {
	// 1. 从跳转地址里取出 uin 与 ptsigx（换 p_skey 的必需参数）
	sigxMatch := qqPtSigxPattern.FindStringSubmatch(jumpURL)
	uinMatch := qqUinPattern.FindStringSubmatch(jumpURL)
	if len(sigxMatch) < 2 || len(uinMatch) < 2 {
		return nil, fmt.Errorf("QQ 登录跳转地址缺少必要参数（ptsigx/uin）: %s", jumpURL)
	}
	uin, sigx := uinMatch[1], sigxMatch[1]

	// 2. 用 uin + ptsigx 换 p_skey（check_sig 新下发的 Cookie 是给 authorize 的唯一凭据）
	skey, session, err := qqExchangePsKey(uin, sigx)
	if err != nil {
		return nil, err
	}

	// 3. 用 p_skey 换 code
	authForm := url.Values{}
	authForm.Set("response_type", "code")
	authForm.Set("client_id", qqClientID)
	authForm.Set("redirect_uri", "https://y.qq.com/portal/wx_redirect.html?login_type=1&surl=https://y.qq.com/")
	authForm.Set("scope", "get_user_info,get_app_friends")
	authForm.Set("state", "state")
	authForm.Set("switch", "")
	authForm.Set("from_ptlogin", "1")
	authForm.Set("src", "1")
	authForm.Set("update_auth", "1")
	authForm.Set("openapi", "1010_1030")
	authForm.Set("g_tk", strconv.FormatUint(uint64(hash33(skey, 5381)), 10))
	authForm.Set("auth_time", strconv.FormatInt(time.Now().UnixMilli(), 10))
	authForm.Set("ui", randomUUID())

	authReq, err := http.NewRequest(http.MethodPost, qqGraphBase+"/oauth2.0/authorize", strings.NewReader(authForm.Encode()))
	if err != nil {
		return nil, err
	}
	authReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authReq.Header.Set("Referer", "https://xui.ptlogin2.qq.com/")
	authReq.Header.Set("Cookie", cookiesToHeader(session))
	authReq.Header.Set("User-Agent", qqWebUA)

	authResp, err := noRedirectClient.Do(authReq)
	if err != nil {
		return nil, fmt.Errorf("QQ 授权失败: %w", err)
	}
	defer authResp.Body.Close()

	location := authResp.Header.Get("Location")
	code := ""
	if u, err := url.Parse(location); err == nil {
		code = u.Query().Get("code")
	}
	if code == "" {
		log.Printf("[QQ扫码] authorize 未返回 code：HTTP %d，Location 是否为空=%v，会话 Cookie %d 项",
			authResp.StatusCode, location == "", len(session))
		return nil, errors.New("QQ 授权没有返回 code")
	}
	log.Printf("[QQ扫码] authorize 换到 code，开始换音乐凭据")

	// 3. 用 code 换音乐凭据
	credential, err := qqMusicRequest(nil, "QQConnectLogin.LoginServer", "QQLogin",
		map[string]any{"code": code}, 2)
	if err != nil {
		return nil, err
	}

	fallback := ""
	if u, err := url.Parse(jumpURL); err == nil {
		fallback = u.Query().Get("uin")
	}
	cookies := credentialToSession(credential, 2, fallback)
	if qqAccountID(cookies) == "" || strField(credential, "musickey") == "" {
		return nil, errors.New("QQ 登录响应缺少 QQ 音乐凭据")
	}
	return cookies, nil
}

// ── 音乐接口 ──

// qqMusicRequest 调用 QQ 音乐 musicu.fcg。
func qqMusicRequest(cookies map[string]string, module, method string, param map[string]any, loginType int) (map[string]any, error) {
	return qqMusicRequestWith(nil, cookies, module, method, param, loginType)
}

// qqMusicRequestWith 在 qqMusicRequest 基础上允许覆盖 comm 字段
// （扫码登录的 CreateQRCode/Login 用不同的 ct/tmeLoginType）。
func qqMusicRequestWith(commOver map[string]any, cookies map[string]string, module, method string, param map[string]any, loginType int) (map[string]any, error) {
	accountID := qqAccountID(cookies)
	musicKey := firstNonEmpty(cookies["qm_keyst"], cookies["qqmusic_key"])
	if loginType == 0 {
		loginType = 2
		if strings.HasPrefix(musicKey, "W_X") {
			loginType = 1
		}
	}

	comm := map[string]any{
		"ct": 11, "cv": 14090008, "v": 14090008, "chid": "10003505",
		"os_ver": "15", "phonetype": "24122RKC7C", "tmeAppID": "qqmusic",
		"nettype": "NETWORK_WIFI", "udid": "0", "OpenUDID": "0",
		"QIMEI36": "0", "uin": "0",
	}
	if accountID != "" {
		comm["uin"] = accountID
		comm["qq"] = accountID
	}
	if musicKey != "" {
		comm["authst"] = musicKey
	}
	comm["tmeLoginType"] = loginType
	for k, v := range commOver {
		comm[k] = v
	}

	payload, err := json.Marshal(map[string]any{
		"comm":    comm,
		"request": map[string]any{"module": module, "method": method, "param": param},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, qqMusicBase+"/cgi-bin/musicu.fcg", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", qqMobileUA)
	req.Header.Set("Referer", "https://y.qq.com")
	if h := cookiesToHeader(cookies); h != "" {
		req.Header.Set("Cookie", h)
	}

	resp, err := followClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("QQ 音乐请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("QQ 音乐请求失败: HTTP %d", resp.StatusCode)
	}

	var out struct {
		Code    int `json:"code"`
		Request struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		} `json:"request"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, qqMaxBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 QQ 音乐响应失败: %w", err)
	}
	if out.Code != 0 || out.Request.Code != 0 {
		return nil, fmt.Errorf("QQ 音乐请求失败: outer=%d, inner=%d", out.Code, out.Request.Code)
	}
	return out.Request.Data, nil
}

// credentialToSession 把登录凭据映射为 Cookie 字段（与上游一致）。
func credentialToSession(credential map[string]any, loginType int, fallbackAccountID string) map[string]string {
	accountID := strings.TrimPrefix(
		firstNonEmpty(strField(credential, "str_musicid"), strField(credential, "musicid"), fallbackAccountID), "o")
	musicKey := strField(credential, "musickey")

	session := map[string]string{
		"uin":            accountID,
		"qm_str_musicid": accountID,
		"qm_keyst":       musicKey,
		"qqmusic_key":    musicKey,
		"tmeLoginType":   strconv.Itoa(intOr(credential["loginType"], loginType)),
	}
	if loginType == 1 {
		session["wxuin"] = accountID
	}
	copyIf := func(srcKey, dstKey string) {
		if v := strField(credential, srcKey); v != "" {
			session[dstKey] = v
		}
	}
	copyIf("encryptUin", "euin")
	if v := strField(credential, "openid"); v != "" {
		if loginType == 1 {
			session["wxopenid"] = v
		} else {
			session["psrf_qqopenid"] = v
		}
	}
	copyIf("unionid", "psrf_qqunionid")
	if v := strField(credential, "refresh_token"); v != "" {
		if loginType == 1 {
			session["wxrefresh_token"] = v
		} else {
			session["psrf_qqrefresh_token"] = v
		}
	}
	copyIf("access_token", "psrf_qqaccess_token")
	copyIf("refresh_key", "qm_refresh_key")
	copyIf("expired_at", "psrf_access_token_expiresAt")
	copyIf("musickeyCreateTime", "psrf_musickey_createtime")
	copyIf("keyExpiresIn", "qm_key_expires_in")
	return session
}

// QQProfile QQ 音乐账号资料。
type QQProfile struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// QQFetchProfile 拉取当前登录账号的资料。
func QQFetchProfile(cookies map[string]string) (QQProfile, error) {
	accountID := qqAccountID(cookies)
	if accountID == "" {
		return QQProfile{}, errors.New("QQ 音乐尚未登录")
	}
	data, err := qqMusicRequest(cookies, "music.UserInfo.userInfoServer", "GetLoginUserInfo", map[string]any{}, 0)
	if err != nil {
		return QQProfile{}, err
	}
	info, _ := data["info"].(map[string]any)
	nickname := firstNonEmpty(strField(info, "nick"), strField(info, "nickname"), strField(info, "name"))
	avatar := strings.Replace(strField(info, "logo"), "http://", "https://", 1)
	if avatar == "" {
		avatar = "https://q.qlogo.cn/headimg_dl?dst_uin=" + accountID + "&spec=100"
	}
	return QQProfile{UID: accountID, Nickname: nickname, Avatar: avatar}, nil
}

// ── 对外辅助（供 API 层使用）──

// FormatQQCookies 把 Cookie map 序列化为可直接存储与回放的 Cookie 头字符串。
func FormatQQCookies(cookies map[string]string) string { return cookiesToHeader(cookies) }

// ParseQQCookies 把存储的 Cookie 头字符串还原为 map。
func ParseQQCookies(header string) map[string]string { return parseCookieHeader(header) }

// QQAccountIDOf 从 Cookie map 推导 QQ 音乐账号 ID。
func QQAccountIDOf(cookies map[string]string) string { return qqAccountID(cookies) }

// ── 日推与歌单 ──

// QQDaily 每日推荐（需登录）。移植自 flow 的 QQMusicConnector.daily()。
//
// 注意与 musicu 常规调用不同：
//   - comm 用网页版参数（ct=19、platform=yqq.json），不是移动端那套
//   - 请求体用 req_0 而非 request，响应也从 req_0 取
func QQDaily(cookies map[string]string) ([]map[string]any, error) {
	uin := qqAccountID(cookies)
	key := firstNonEmpty(cookies["qm_keyst"], cookies["qqmusic_key"])
	if uin == "" || key == "" {
		return nil, errors.New("QQ 音乐尚未登录，无法获取账号专属日推")
	}

	comm := map[string]any{
		"ct": 19, "cv": 0, "format": "json", "inCharset": "utf-8", "outCharset": "utf-8",
		"notice": 0, "platform": "yqq.json", "needNewCode": 1,
		"uin": uin, "authst": key,
	}
	if v := cookies["tmeLoginType"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			comm["tmeLoginType"] = n
		}
	}

	body := map[string]any{
		"comm": comm,
		"req_0": map[string]any{
			"module": "music.srfDissInfo.DissInfo",
			"method": "CgiGetDiss",
			"param": map[string]any{
				"dirid": 202, "uinAttached": true,
				"enc_host_uin": uin, "local_time": time.Now().Unix(),
			},
		},
	}
	payload, err := qqPostJSON(qqMusicBase+"/cgi-bin/musicu.fcg", body, cookies)
	if err != nil {
		return nil, err
	}

	if numField(payload, "code") != 0 {
		return nil, errors.New("QQ 音乐登录状态失效或日推接口拒绝访问")
	}
	item, _ := payload["req_0"].(map[string]any)
	if item == nil || numField(item, "code") != 0 {
		return nil, errors.New("QQ 音乐登录状态失效或日推接口拒绝访问")
	}
	data, _ := item["data"].(map[string]any)
	rows, _ := data["songlist"].([]any)
	return normalizeQQTracks(rows), nil
}

// QQPlaylists 返回账号「创建」与「收藏」的歌单（合并，带 kind 字段区分）。
func QQPlaylists(cookies map[string]string) ([]map[string]any, error) {
	uin := qqAccountID(cookies)
	if uin == "" {
		return nil, errors.New("QQ 音乐尚未登录")
	}

	common := map[string]string{
		"format": "json", "inCharset": "utf8", "outCharset": "utf-8",
		"notice": "0", "platform": "yqq.json", "needNewCode": "0",
	}
	headers := map[string]string{
		"Cookie":     cookiesToHeader(cookies),
		"Referer":    "https://y.qq.com/portal/profile.html",
		"User-Agent": qqWebUA,
	}

	type source struct {
		kind   string
		url    string
		params map[string]string
		// listKey 为响应中承载歌单数组的字段名
		listKey string
	}
	sources := []source{
		{
			kind: "created", url: qqCGIBase + "/rsc/fcgi-bin/fcg_user_created_diss",
			listKey: "disslist",
			params: map[string]string{
				"hostUin": "0", "hostuin": uin, "loginUin": "0",
				"sin": "0", "size": "200", "g_tk": "5381",
			},
		},
		{
			kind: "collected", url: qqCGIBase + "/fav/fcgi-bin/fcg_get_profile_order_asset.fcg",
			listKey: "cdlist",
			params: map[string]string{
				"ct": "20", "cid": "205360956", "userid": uin,
				"reqtype": "3", "sin": "0", "ein": "199",
			},
		},
	}

	out := make([]map[string]any, 0)
	var errs []string
	for _, src := range sources {
		query := url.Values{}
		for k, v := range common {
			query.Set(k, v)
		}
		for k, v := range src.params {
			query.Set(k, v)
		}

		payload, err := qqGetJSON(src.url+"?"+query.Encode(), headers)
		if err != nil {
			errs = append(errs, src.kind+": "+err.Error())
			continue
		}
		if code := numField(payload, "code"); code != 0 {
			errs = append(errs, fmt.Sprintf("%s: 接口返回 code=%d", src.kind, code))
			continue
		}
		root, _ := payload["data"].(map[string]any)
		if root == nil {
			continue
		}
		rows, _ := root[src.listKey].([]any)
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			// QQ 的列表会混入「表头汇总行」（有 totalnum 之类但没有 dissid），
			// 原样透出会让前端点出空 id 的坏请求（2026-09-20 真机）
			id := firstNonEmpty(strField(row, "dissid"), strField(row, "disstid"))
			if id == "" {
				continue
			}
			out = append(out, map[string]any{
				"kind":        src.kind,
				"id":          id,
				"name":        firstNonEmpty(strField(row, "dissname"), strField(row, "diss_name")),
				"description": firstNonEmpty(strField(row, "desc"), strField(row, "description")),
				"cover_url":   firstNonEmpty(strField(row, "logo"), strField(row, "diss_cover")),
				"count":       intOr(row["song_cnt"], intOr(row["songnum"], 0)),
				"creator":     firstNonEmpty(strField(row, "nickname"), strField(row, "creator")),
			})
		}
	}

	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.New("获取 QQ 歌单失败: " + strings.Join(errs, "; "))
	}
	return out, nil
}

// QQPlaylistDetail 歌单详情（含曲目）。disstid 为歌单 ID。
//
// 先走 v8 接口，拿不到再回落到老的 qzone 接口（原因见下面两个函数的注释）。
func QQPlaylistDetail(cookies map[string]string, disstid string) (map[string]any, error) {
	if strings.TrimSpace(disstid) == "" {
		return nil, errors.New("请提供歌单 ID")
	}

	v8Res, v8Err := qqPlaylistDetailV8(cookies, disstid)
	if v8Err == nil {
		return v8Res, nil
	}

	// ⚠️ **为什么保留老接口而不是直接删掉**：老接口对**匿名**请求已经失效
	// （2026-09-21 实测，且当时那个歌单是公开的），但「**已登录**时是否仍然可用」
	// 在开发环境没有条件验证 —— 需要真实 QQ 账号。
	// 私密歌单有可能只有带 cookie 的老接口读得到，直接删掉等于赌一把。
	// 等有真实账号验证过「v8 也能读私密歌单」之后再删。
	legacyRes, legacyErr := qqPlaylistDetailLegacy(cookies, disstid)
	if legacyErr == nil {
		return legacyRes, nil
	}

	return nil, fmt.Errorf("读取 QQ 歌单失败：%v；备用接口也失败：%v", v8Err, legacyErr)
}

// qqPlaylistDetailV8 走 `v8/fcg-bin/fcg_v8_playlist_cp.fcg` —— 当前 QQ 网页端在用的接口。
//
// 为什么优先它：
//   - 老的 `qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg` 对**匿名**请求只返回
//     `{"code":0,"subcode":4000,"msg":"check privacy error!"}`（2026-09-21 实测）。
//   - 另一个候选 `u.y.qq.com/cgi-bin/musicu.fcg`（`music.srfDissInfo.aiDissInfo`）也能用，
//     但会**截断到 1000 首**且要自己翻页；v8 一次返回全部曲目
//     （实测 1254 首的歌单一次拿全）。
//
// ⚠️ v8 的 `cdlist` 在 `data` 下（老接口在顶层），所以下面做了兼容取值。
func qqPlaylistDetailV8(cookies map[string]string, disstid string) (map[string]any, error) {
	query := url.Values{}
	for k, v := range map[string]string{
		"id": disstid, "format": "json", "newsong": "1",
		"platform": "yqq", "inCharset": "utf8", "outCharset": "utf-8",
	} {
		query.Set(k, v)
	}

	raw, err := qqGetText(qqCGIBase+"/v8/fcg-bin/fcg_v8_playlist_cp.fcg?"+query.Encode(), map[string]string{
		"Cookie":     cookiesToHeader(cookies),
		"Referer":    "https://y.qq.com/",
		"User-Agent": qqWebUA,
	})
	if err != nil {
		return nil, err
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("解析歌单详情失败: %w", err)
	}

	holder := payload
	if d, ok := payload["data"].(map[string]any); ok {
		holder = d
	}
	cdlist, _ := holder["cdlist"].([]any)
	if len(cdlist) == 0 {
		return nil, errors.New("歌单不存在或无权访问")
	}
	root, _ := cdlist[0].(map[string]any)
	songs, _ := root["songlist"].([]any)
	return qqPlaylistDetailResult(disstid, root, songs), nil
}

// qqPlaylistDetailLegacy 走老的 `qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg`。
//
// 保留原因见 `QQPlaylistDetail` 的注释。它返回的是 **JSONP**，要剥掉回调包装。
func qqPlaylistDetailLegacy(cookies map[string]string, disstid string) (map[string]any, error) {
	query := url.Values{}
	for k, v := range map[string]string{
		"type": "1", "json": "1", "utf8": "1", "onlysong": "0", "new_format": "1",
		"loginUin": "0", "hostUin": "0", "format": "json", "inCharset": "utf8",
		"outCharset": "utf-8", "platform": "yqq.json", "needNewCode": "0",
		"disstid": disstid,
	} {
		query.Set(k, v)
	}

	raw, err := qqGetText(qqCGIBase+"/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg?"+query.Encode(), map[string]string{
		"Cookie":     cookiesToHeader(cookies),
		"Referer":    "https://y.qq.com/",
		"User-Agent": qqWebUA,
	})
	if err != nil {
		return nil, err
	}

	// 该接口返回 JSONP，需剥掉回调包装
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "("); idx >= 0 && strings.HasPrefix(raw, "jsonCallback") {
		raw = strings.TrimSuffix(strings.TrimSpace(raw[idx+1:]), ");")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("解析歌单详情失败: %w", err)
	}

	cdlist, _ := payload["cdlist"].([]any)
	if len(cdlist) == 0 {
		return nil, errors.New("歌单不存在或无权访问")
	}
	root, _ := cdlist[0].(map[string]any)
	songs, _ := root["songlist"].([]any)
	return qqPlaylistDetailResult(disstid, root, songs), nil
}

// qqPlaylistDetailResult 把 QQ 的 cdlist[0] 统一成对外结构。
//
// 两个接口的**曲目字段名不一样**（老：songname/songmid；新：name/mid），
// 但 `normalizeQQTracks` 两边都认，所以这里只需要处理歌单级字段。
func qqPlaylistDetailResult(disstid string, root map[string]any, songs []any) map[string]any {
	return map[string]any{
		"id":          disstid,
		"title":       firstNonEmpty(strField(root, "dissname"), strField(root, "name"), strField(root, "title"), disstid),
		"description": firstNonEmpty(strField(root, "desc"), strField(root, "description")),
		"cover_url":   firstNonEmpty(strField(root, "logo"), strField(root, "cover_url"), strField(root, "picurl")),
		"owner":       firstNonEmpty(strField(root, "nickname"), strField(root, "creator")),
		"items":       normalizeQQTracks(songs),
		"count":       len(songs),
	}
}

// normalizeQQTracks 把 QQ 的曲目对象数组统一成稳定结构。
func normalizeQQTracks(rows []any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mid := firstNonEmpty(strField(row, "songmid"), strField(row, "mid"), strField(row, "id"))
		title := firstNonEmpty(strField(row, "songname"), strField(row, "songName"),
			strField(row, "title"), strField(row, "name"))
		if mid == "" || title == "" {
			continue
		}

		artist := ""
		if singers, ok := row["singer"].([]any); ok {
			names := make([]string, 0, len(singers))
			for _, s := range singers {
				if m, ok := s.(map[string]any); ok {
					if n := strField(m, "name"); n != "" {
						names = append(names, n)
					}
				}
			}
			artist = strings.Join(names, " / ")
		}

		album := strField(row, "albumname")
		if album == "" {
			if a, ok := row["album"].(map[string]any); ok {
				album = strField(a, "name")
			}
		}

		out = append(out, map[string]any{
			"platform":    "qq",
			"song_id":     mid,
			"mid":         mid,
			"title":       title,
			"artist":      artist,
			"album":       album,
			"duration_ms": intOr(row["interval"], 0) * 1000,
			"cover_url":   qqCover(row),
		})
	}
	return out
}

// qqCover 从曲目对象推导封面地址（与 flow 的 _qq_cover 一致）。
func qqCover(row map[string]any) string {
	if album, ok := row["album"].(map[string]any); ok {
		if mid := firstNonEmpty(strField(album, "mid"), strField(album, "pmid")); mid != "" {
			return "https://y.gtimg.cn/music/photo_new/T002R800x800M000" + mid + ".jpg"
		}
	}
	if mid := firstNonEmpty(strField(row, "albummid"), strField(row, "albumMid")); mid != "" {
		return "https://y.gtimg.cn/music/photo_new/T002R800x800M000" + mid + ".jpg"
	}
	return ""
}

// ── HTTP 小工具 ──

// qqPostJSON 发送 JSON 请求并解析响应对象。
func qqPostJSON(rawURL string, body map[string]any, cookies map[string]string) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://y.qq.com")
	req.Header.Set("Referer", "https://y.qq.com/")
	req.Header.Set("User-Agent", qqWebUA)
	if h := cookiesToHeader(cookies); h != "" {
		req.Header.Set("Cookie", h)
	}
	return qqDoJSON(req)
}

func qqGetJSON(rawURL string, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return qqDoJSON(req)
}

func qqDoJSON(req *http.Request) (map[string]any, error) {
	resp, err := followClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("QQ 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("QQ 请求失败: HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, qqMaxBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 QQ 响应失败: %w", err)
	}
	return out, nil
}

func qqGetText(rawURL string, headers map[string]string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := followClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("QQ 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("QQ 请求失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, qqMaxBody))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// numField 读取数字字段（JSON 数字统一是 float64）。
func numField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// ── 小工具 ──

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// strField 从 map 里取字符串字段；数字会被转成字符串。
func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

func intOr(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if parsed, err := strconv.Atoi(n); err == nil {
			return parsed
		}
	}
	return fallback
}

// randomUUID 生成一个 v4 UUID 字符串（用于授权请求的 ui 参数）。
func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
