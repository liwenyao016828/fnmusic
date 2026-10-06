package account

// 网易云音乐：扫码登录 + 数据接口。
//
// 扫码登录走 **eapi 加密通道**（见 netease_eapi.go 的完整论证）：
//   - POST {eapi}/api/login/qrcode/unikey        params 里 type=3 → unikey
//   - POST {eapi}/api/login/qrcode/client/login  params 里 key=&type=3  轮询
//   - 二维码内容 = https://music.163.com/login?codekey=<unikey> （无 chainId）
//   - 803 时登录 Cookie（MUSIC_U）从 **Set-Cookie 响应头** 取，body.cookie 仅兜底
//   - 申请与轮询共用同一 deviceId（eapi 的会话连续性靠 header，不靠 Cookie jar）
//
// ⚠️ 历史教训（2026-09-19 一天内三次翻转，别再走回头路）：
//   - GET+type=1 明文通道 → 真机 App 拒扫（2.1.1 前）
//   - GET+type=3+伪造 chainId → 真机仍拒（2.1.1）
//   - POST+type=1 明文通道 → 真机仍拒，且**轮询响应里服务端直接回**
//     「请切换其他登录方式」—— 实锤：拒绝发生在服务端渠道判定，与 type 无关。
//     明文 /api/ 通道已死，唯一活路是加密通道（社区库/flow 全靠 eapi 能用）。
//
// 数据接口仍走 legacy /api/ 路径（纯 GET + Cookie，实测无需 weapi 加密）。
//
// 扫码状态码：800 二维码过期 / 801 等待扫码 / 802 待确认 / 803 授权成功。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	neteaseUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	neteaseMaxRaw = 8 << 20
)

// neteaseBase 为网易接口基地址；声明为变量以便测试注入。
var neteaseBase = "https://music.163.com"

var neteaseClient = &http.Client{Timeout: 20 * time.Second}

// QRStatus 扫码状态码
const (
	QRExpired  = 800
	QRWaiting  = 801
	QRScanned  = 802
	QRVerified = 803
)

// QRStatusText 返回状态码的可读说明。
func QRStatusText(code int) string {
	switch code {
	case QRExpired:
		return "二维码已过期，请刷新"
	case QRWaiting:
		return "等待扫码"
	case QRScanned:
		return "已扫码，请在手机上确认"
	case QRVerified:
		return "授权成功"
	}
	return fmt.Sprintf("未知状态 %d", code)
}

// neteaseQRType 扫码登录的端类型。**社区库 eapi 通道实测值就是 3**；
// 明文 /api/ 通道的 type 值（1 或 3）真机都被拒 —— 关键是通道，不是这个数。
const neteaseQRType = 3

// neteaseQRHost 二维码指向的站点。与 neteaseBase（测试会替换）无关。
const neteaseQRHost = "https://music.163.com"

// ── 扫码会话 ──
//
// eapi 通道靠 header.deviceId 串会话（不是 Cookie jar）：
// 申请与轮询必须带同一个 deviceId，服务端才认为是同一台「设备」在扫这张码。

type neteaseQRSession struct {
	deviceID string
	cookies  map[string]string
	until    time.Time
}

var (
	neteaseQRMu       sync.Mutex
	neteaseQRSessions = map[string]*neteaseQRSession{}
)

const neteaseQRTTL = 10 * time.Minute

func neteaseQRSave(key string, s *neteaseQRSession) {
	neteaseQRMu.Lock()
	defer neteaseQRMu.Unlock()
	now := time.Now()
	for k, old := range neteaseQRSessions {
		if now.After(old.until) {
			delete(neteaseQRSessions, k)
		}
	}
	s.until = now.Add(neteaseQRTTL)
	neteaseQRSessions[key] = s
}

func neteaseQRLoad(key string) *neteaseQRSession {
	neteaseQRMu.Lock()
	defer neteaseQRMu.Unlock()
	s, ok := neteaseQRSessions[key]
	if !ok || time.Now().After(s.until) {
		return nil
	}
	return s
}

func neteaseQRDrop(key string) {
	neteaseQRMu.Lock()
	defer neteaseQRMu.Unlock()
	delete(neteaseQRSessions, key)
}

// setCookiePairs 抽取响应 Set-Cookie 头里的 name=value 对。
func setCookiePairs(resp *http.Response) []string {
	var out []string
	for _, raw := range resp.Header.Values("Set-Cookie") {
		pair := raw
		if idx := strings.Index(raw, ";"); idx >= 0 {
			pair = raw[:idx]
		}
		if strings.Contains(pair, "=") {
			out = append(out, strings.TrimSpace(pair))
		}
	}
	return out
}

// QRLogin 一次扫码登录会话。
type QRLogin struct {
	Key string `json:"key"`
	// URL 为二维码承载的内容，前端据此生成二维码图片
	URL string `json:"url"`
}

// NeteaseQRKey 申请一个二维码 key（eapi 通道）。
func NeteaseQRKey() (QRLogin, error) {
	deviceID, anon := neteaseAnonIdentity()
	jar := make(map[string]string, len(anon))
	for k, v := range anon {
		jar[k] = v
	}

	out, pairs, merged, err := eapiPost("/api/login/qrcode/unikey",
		map[string]any{"type": neteaseQRType}, deviceID, jar)
	if err != nil {
		log.Printf("[网易云扫码] 申请 unikey 失败: %v", err)
		return QRLogin{}, err
	}
	_ = pairs
	unikey, _ := out["unikey"].(string)
	if unikey == "" {
		log.Printf("[网易云扫码] 响应里没有 unikey（code=%v）", out["code"])
		return QRLogin{}, fmt.Errorf("网易未返回二维码 key（code=%d）", numField(out, "code"))
	}
	neteaseQRSave(unikey, &neteaseQRSession{deviceID: deviceID, cookies: merged})
	log.Printf("[网易云扫码] unikey 申请成功（eapi 通道，type=%d，游客凭据=%v）",
		neteaseQRType, merged["MUSIC_A"] != "")
	return QRLogin{
		Key: unikey,
		URL: neteaseQRHost + "/login?codekey=" + url.QueryEscape(unikey),
	}, nil
}

// NeteaseQRCheck 查询扫码状态。code 为 803 时返回可用于后续请求的 Cookie。
func NeteaseQRCheck(key string) (code int, message, cookie string, err error) {
	sess := neteaseQRLoad(key)
	if sess == nil {
		// 会话丢了（重启/过期）也要能继续轮询出明确状态码，用一次性 deviceId
		sess = &neteaseQRSession{deviceID: eapiDeviceID(), cookies: map[string]string{}}
	}

	out, respCookies, merged, err := eapiPost("/api/login/qrcode/client/login",
		map[string]any{"key": key, "type": neteaseQRType}, sess.deviceID, sess.cookies)
	if err != nil {
		log.Printf("[网易云扫码] 轮询失败: %v", err)
		return 0, "", "", err
	}

	code = numField(out, "code")
	message, _ = out["message"].(string)
	if message == "" {
		message = QRStatusText(code)
	}

	switch code {
	case QRVerified:
		neteaseQRDrop(key)
		// 官方形态：登录 Cookie 走 Set-Cookie 响应头下发；body.cookie 是老接口的兜底。
		head := strings.Join(respCookies, "; ")
		bodyCookie, _ := out["cookie"].(string)
		src := "empty"
		switch {
		case strings.Contains(head, "MUSIC_U"):
			cookie, src = head, "响应头"
		case strings.Contains(bodyCookie, "MUSIC_U"):
			cookie, src = bodyCookie, "响应体(兜底)"
		default:
			cookie = firstNonEmpty(head, bodyCookie)
			if cookie != "" {
				src = "无MUSIC_U的凭据"
			}
		}
		log.Printf("[网易云扫码] 803 授权成功，Cookie 来源=%s 长度=%d", src, len(cookie))
	case QRExpired:
		neteaseQRDrop(key)
		log.Printf("[网易云扫码] 二维码已过期(800)")
	default:
		sess.cookies = merged
		neteaseQRSave(key, sess)
		// 服务端在轮询响应里回「切换登录方式」= 该 unikey 被判异常渠道，必须记下来
		if strings.Contains(message, "切换") || strings.Contains(message, "升级") {
			log.Printf("[网易云扫码] 服务端拒绝该渠道：code=%d message=%q", code, message)
		}
	}
	return code, message, cookie, nil
}

// NeteaseProfile 账号资料
type NeteaseProfile struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// NeteaseAccount 校验 Cookie 并返回账号资料。未登录时返回错误。
func NeteaseAccount(cookie string) (NeteaseProfile, error) {
	var out struct {
		Code    int `json:"code"`
		Account *struct {
			ID int64 `json:"id"`
		} `json:"account"`
		Profile *struct {
			UserID   int64  `json:"userId"`
			Nickname string `json:"nickname"`
			Avatar   string `json:"avatarUrl"`
		} `json:"profile"`
	}
	if err := neteaseGet("/api/nuser/account/get", cookie, &out); err != nil {
		return NeteaseProfile{}, err
	}
	if out.Profile == nil && out.Account == nil {
		return NeteaseProfile{}, fmt.Errorf("网易登录态无效或已过期")
	}

	p := NeteaseProfile{}
	if out.Profile != nil {
		p.Nickname = out.Profile.Nickname
		p.Avatar = out.Profile.Avatar
		p.UID = fmt.Sprintf("%d", out.Profile.UserID)
	}
	if p.UID == "" && out.Account != nil {
		p.UID = fmt.Sprintf("%d", out.Account.ID)
	}
	return p, nil
}

// NeteaseDailySongs 每日推荐。需要已登录的 Cookie。
func NeteaseDailySongs(cookie string) ([]map[string]any, error) {
	var out struct {
		Code      int              `json:"code"`
		Recommend []map[string]any `json:"recommend"`
	}
	if err := neteaseGet("/api/v1/discovery/recommend/songs", cookie, &out); err != nil {
		return nil, err
	}
	if len(out.Recommend) == 0 {
		return nil, fmt.Errorf("每日推荐为空：可能未登录、登录态已过期，或该账号无日推权限")
	}
	return out.Recommend, nil
}

// NeteaseUserPlaylists 用户创建与收藏的歌单。uid 为空时使用已连接账号的 uid。
func NeteaseUserPlaylists(cookie, uid string, limit int) ([]map[string]any, error) {
	if uid == "" {
		p, err := NeteaseAccount(cookie)
		if err != nil {
			return nil, err
		}
		uid = p.UID
	}
	if limit <= 0 {
		limit = 100
	}
	apiPath := fmt.Sprintf("/api/user/playlist?uid=%s&limit=%d&offset=0", url.QueryEscape(uid), limit)

	var out struct {
		Code     int              `json:"code"`
		Playlist []map[string]any `json:"playlist"`
	}
	if err := neteaseGet(apiPath, cookie, &out); err != nil {
		return nil, err
	}
	return out.Playlist, nil
}

// NeteasePlaylistDetail 歌单详情（含全部曲目）。
//
// 注意：不加 n 参数时上游只内嵌前 10 首，必须显式传 n（实测 n=1000 可拿全）。
func NeteasePlaylistDetail(cookie, playlistID string) (map[string]any, error) {
	if strings.TrimSpace(playlistID) == "" {
		return nil, fmt.Errorf("请提供歌单 ID")
	}
	apiPath := "/api/v6/playlist/detail?id=" + url.QueryEscape(playlistID) + "&n=1000"

	var out struct {
		Code     int            `json:"code"`
		Playlist map[string]any `json:"playlist"`
	}
	if err := neteaseGet(apiPath, cookie, &out); err != nil {
		return nil, err
	}
	if out.Playlist == nil {
		return nil, fmt.Errorf("歌单不存在或无权访问（id=%s）", playlistID)
	}

	raw, _ := out.Playlist["tracks"].([]any)
	tracks := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strFieldAny(row, "id")
		name := strFieldAny(row, "name")
		if id == "" || name == "" {
			continue
		}

		artists := ""
		if list, ok := row["ar"].([]any); ok {
			names := make([]string, 0, len(list))
			for _, a := range list {
				if m, ok := a.(map[string]any); ok {
					if n := strFieldAny(m, "name"); n != "" {
						names = append(names, n)
					}
				}
			}
			artists = strings.Join(names, " / ")
		}

		album, cover := "", ""
		if al, ok := row["al"].(map[string]any); ok {
			album = strFieldAny(al, "name")
			cover = strFieldAny(al, "picUrl")
		}

		durationMs := intFieldAny(row["dt"])
		tracks = append(tracks, map[string]any{
			"platform":    "netease",
			"song_id":     id,
			"title":       name,
			"artist":      artists,
			"album":       album,
			"duration_ms": durationMs,
			"cover_url":   strings.Replace(cover, "http://", "https://", 1),
		})
	}

	return map[string]any{
		"id":          playlistID,
		"title":       strFieldAny(out.Playlist, "name"),
		"description": strFieldAny(out.Playlist, "description"),
		"cover_url":   strings.Replace(strFieldAny(out.Playlist, "coverImgUrl"), "http://", "https://", 1),
		"owner":       ownerNickname(out.Playlist),
		"track_count": intFieldAny(out.Playlist["trackCount"]),
		"items":       tracks,
		"count":       len(tracks),
	}, nil
}

// NeteaseDailyTracks 每日推荐，统一成与歌单详情一致的结构。
func NeteaseDailyTracks(cookie string) (map[string]any, error) {
	songs, err := NeteaseDailySongs(cookie)
	if err != nil {
		return nil, err
	}
	tracks := make([]map[string]any, 0, len(songs))
	for _, row := range songs {
		id := strFieldAny(row, "id")
		name := strFieldAny(row, "name")
		if id == "" || name == "" {
			continue
		}
		artists := ""
		if list, ok := row["ar"].([]any); ok {
			names := make([]string, 0, len(list))
			for _, a := range list {
				if m, ok := a.(map[string]any); ok {
					if n := strFieldAny(m, "name"); n != "" {
						names = append(names, n)
					}
				}
			}
			artists = strings.Join(names, " / ")
		}
		album, cover := "", ""
		if al, ok := row["al"].(map[string]any); ok {
			album = strFieldAny(al, "name")
			cover = strFieldAny(al, "picUrl")
		}
		tracks = append(tracks, map[string]any{
			"platform":    "netease",
			"song_id":     id,
			"title":       name,
			"artist":      artists,
			"album":       album,
			"duration_ms": intFieldAny(row["dt"]),
			"cover_url":   strings.Replace(cover, "http://", "https://", 1),
		})
	}
	return map[string]any{
		"id": "daily", "title": "网易云 · 每日推荐",
		"description": "每日自动更新", "items": tracks, "count": len(tracks),
	}, nil
}

// ownerNickname 从歌单对象里取创建者昵称。
func ownerNickname(playlist map[string]any) string {
	if creator, ok := playlist["creator"].(map[string]any); ok {
		return strFieldAny(creator, "nickname")
	}
	return ""
}

// strFieldAny 读取字符串字段（数字会转字符串）。
func strFieldAny(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// intFieldAny 读取数字字段。
func intFieldAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var out int
		_, _ = fmt.Sscanf(n, "%d", &out)
		return out
	}
	return 0
}

// neteaseGet 发起一次网易 legacy 接口 GET 请求。
// cookie 非空时带上；返回体统一按 JSON 解析。
func neteaseGet(apiPath, cookie string, out any) error {
	req, err := http.NewRequest(http.MethodGet, neteaseBase+apiPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", neteaseUA)
	req.Header.Set("Referer", "https://music.163.com/")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := neteaseClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求网易失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, neteaseMaxRaw))
	if err != nil {
		return fmt.Errorf("读取网易响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("网易返回 HTTP %d", resp.StatusCode)
	}
	// 部分接口返回的 JSON 含非法控制字符，宽松解析前先清一遍
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, string(body))
	if err := json.Unmarshal([]byte(cleaned), out); err != nil {
		return fmt.Errorf("解析网易响应失败: %w", err)
	}
	return nil
}
