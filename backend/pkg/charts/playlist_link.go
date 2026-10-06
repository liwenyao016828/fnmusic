package charts

// 歌单链接解析。
//
// 用户从各平台 App / 网页「复制链接」得到的是**分享链接**而不是裸 id，
// 所以歌单详情接口需要能直接吃链接 —— 否则用户粘进来只会得到
// 「Unsupported source」这种看不懂的报错（这正是它被反馈的原因）。
//
// 只做「识别平台 + 抽出 id」，不校验 id 是否真实存在（那交给下游接口）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"fn-lx-player/pkg/security"
)

// playlistLinkPatterns 各平台的歌单链接特征。
//
// 每个平台给多条：不同端（App 分享 / 网页 / 小程序）的链接形态不一样，
// 只认一种会漏。host 用「包含」匹配，因为还有 y.qq.com / c6.y.qq.com 这类子域。
var playlistLinkPatterns = []struct {
	source string
	hosts  []string
	// pathRes 从 path 里抽 id（按顺序尝试，命中即用）
	pathRes []*regexp.Regexp
	// queryKeys 从查询串里抽 id（按顺序尝试）
	queryKeys []string
}{
	{
		source: "wy",
		hosts:  []string{"music.163.com", "163cn.tv", "y.music.163.com"},
		// /playlist/123456  /#/playlist?id=123456  /discover/toplist?id=123
		pathRes:   []*regexp.Regexp{regexp.MustCompile(`/playlist/(\d+)`), regexp.MustCompile(`/toplist/(\d+)`)},
		queryKeys: []string{"id"},
	},
	{
		source: "tx",
		hosts:  []string{"y.qq.com", "c6.y.qq.com", "i.y.qq.com", "qqmusic.qq.com"},
		// /n/ryqq/playlist/123456  /playlist/123456
		pathRes:   []*regexp.Regexp{regexp.MustCompile(`/playlist/(\d+)`)},
		queryKeys: []string{"id", "disstid"},
	},
	{
		source: "kg",
		hosts:  []string{"kugou.com", "m.kugou.com", "t1.kugou.com"},
		// /yy/special/single/123456.html
		pathRes:   []*regexp.Regexp{regexp.MustCompile(`/single/(\d+)`), regexp.MustCompile(`/special/(\d+)`)},
		queryKeys: []string{"specialid", "id"},
	},
	{
		source: "kw",
		hosts:  []string{"kuwo.cn", "m.kuwo.cn", "www.kuwo.cn"},
		// /playlist_detail/123456
		pathRes:   []*regexp.Regexp{regexp.MustCompile(`/playlist_detail/(\d+)`), regexp.MustCompile(`/playlist/(\d+)`)},
		queryKeys: []string{"pid", "id"},
	},
}

// LooksLikeURL 判断用户输入是不是链接（而不是搜索关键词）。
// 搜索框据此决定「按链接解析」还是「按关键词搜索」。
func LooksLikeURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ParsePlaylistLink 从歌单分享链接里识别平台并抽出歌单 id。
//
// 识别不出来时返回带指引的错误，而不是让调用方拿到一个空 source
// 去撞下游的 "Unsupported source"。
func ParsePlaylistLink(raw string) (source, id string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("链接为空")
	}

	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("这不是一个有效的链接：%s", truncate(raw, 60))
	}

	host := strings.ToLower(u.Host)
	// 网易云分享短链 163cn.tv 的 id 在 path 里，没有 query
	path := u.Path
	if u.Fragment != "" {
		// 网易云网页版是 /#/playlist?id=xxx，id 藏在 fragment 里
		path += "/" + strings.TrimPrefix(u.Fragment, "/")
	}

	// fragment 里也可能带查询串（/#/playlist?id=1）
	query := u.Query()
	if i := strings.Index(u.Fragment, "?"); i >= 0 {
		if fq, e := url.ParseQuery(u.Fragment[i+1:]); e == nil {
			for k, vs := range fq {
				if len(vs) > 0 {
					query.Set(k, vs[0])
				}
			}
		}
	}

	for _, p := range playlistLinkPatterns {
		if !hostMatches(host, p.hosts) {
			continue
		}
		for _, re := range p.pathRes {
			if m := re.FindStringSubmatch(path); len(m) > 1 {
				return p.source, m[1], nil
			}
		}
		for _, k := range p.queryKeys {
			if v := strings.TrimSpace(query.Get(k)); v != "" && isDigits(v) {
				return p.source, v, nil
			}
		}
		return "", "", fmt.Errorf("识别到 %s 的链接，但没能从里面找到歌单 id；请确认是「歌单」的分享链接，而不是单曲或专辑", platformLabel(p.source))
	}

	return "", "", fmt.Errorf("暂不认识这个平台的歌单链接（支持网易云 / QQ 音乐 / 酷狗 / 酷我）：%s", truncate(host, 40))
}

// ── 分享短链 ──
//
// 手机 App 里「复制链接」拿到的常常不是上面那些规整形态，而是**短链**：
//
//	https://163cn.tv/abcXYZ12                      网易云
//	https://c6.y.qq.com/base/fcgi-bin/u?__=abc123  QQ 音乐
//	https://t1.kugou.com/abc123                    酷狗
//
// 这些链接的 path 里是**短码**而不是歌单 id，纯文本解析必然失败 ——
// 用户粘进来只会看到 400「没能从里面找到歌单 id」，而这条链接在别的软件里是能打开的。
// 必须跟随重定向拿到真实地址再解析一次。

// shortLinkHosts 短链域名白名单。
//
// 白名单化的意义：**不把任意用户输入都拿去做网络请求**（SSRF 面）。
// 只有这些已知的分享域才值得发一次请求。
var shortLinkHosts = []string{
	"163cn.tv",     // 网易云 App 分享
	"t1.kugou.com", // 酷狗分享
	"t2.kugou.com",
	"t3.kugou.com",
}

// shortLinkClient 展开短链用的客户端。
// 复用 security 的安全客户端：它会**逐跳校验**重定向目标，防止短链跳进内网。
var shortLinkClient = security.NewSafeClient(security.DefaultOptions(), 8*time.Second)

// BuildPlaylistLink 是 `ParsePlaylistLink` 的**反向操作**：由平台代号 + 歌单 id 拼一条标准歌单链接。
//
// 为什么需要它：有些入口**只拿得到 id、没有分享链接** —— 前端的「精选歌单」与
// 「官方榜单」都是这样（平台返回的只有 id）。而后端抓歌单的**两条通道门槛不一样**：
//
//	按链接 → 走平台公开接口，**不需要登录**
//	按 ID  → 必须有已登录账号（要读 cookie）
//
// 所以把裸 id 拼成链接再走链接通道，能让「没连账号」的用户也订阅成功。
//
// 拼出来的形态必须能被 `ParsePlaylistLink` **原样解析回去**（有往返测试守着）。
// 认不出的平台（如 kg / kw 的公开歌单接口形态不同）返回空串，由调用方决定怎么报错。
func BuildPlaylistLink(source, id string) string {
	id = strings.TrimSpace(id)
	if id == "" || !isDigits(id) {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "wy", "netease":
		return "https://music.163.com/playlist?id=" + id
	case "tx", "qq":
		return "https://y.qq.com/n/ryqq/playlist/" + id
	}
	return ""
}

// looksLikeShortLink 判断这个链接是否「需要跟随重定向才能拿到 id」。
//
// 只在解析失败后调用，所以宽松判定的代价很低；但也不能太宽 ——
// 否则用户随便粘个错链接都会触发一次出站请求。
func looksLikeShortLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, h := range shortLinkHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	// QQ 的短链挂在正常域下，靠 path 区分：c6.y.qq.com/base/fcgi-bin/u?__=xxx
	if hostMatches(host, []string{"y.qq.com"}) && strings.Contains(u.Path, "/base/fcgi-bin/u") {
		return true
	}
	return false
}

// ResolvePlaylistLink 解析歌单分享链接，**短链会自动展开**。
//
// 先做纯文本解析（零开销，覆盖绝大多数链接）；
// 只有在解析失败且链接属于已知短链域时，才跟随重定向再解析一次。
// client 传 nil 时使用内置的安全客户端。
func ResolvePlaylistLink(raw string, client *http.Client) (source, id string, err error) {
	source, id, err = ParsePlaylistLink(raw)
	if err == nil {
		return source, id, nil
	}
	if !looksLikeShortLink(raw) {
		return "", "", err
	}

	final, rerr := expandShortLink(raw, client)
	if rerr != nil {
		return "", "", fmt.Errorf("%s（这是一条分享短链，尝试展开时失败：%v）", err.Error(), rerr)
	}
	source, id, perr := ParsePlaylistLink(final)
	if perr != nil {
		// 展开成功但认不出歌单 —— 常见于短码已失效（如网易云会跳到 music.163.com/#404）。
		// 把内层原因也带上，用户才能判断到底是「链接失效」还是「不是歌单链接」。
		return "", "", fmt.Errorf("分享短链展开后的地址不是歌单页：%s（%s）", truncate(final, 80), perr.Error())
	}
	return source, id, nil
}

// expandShortLink 展开分享短链，返回真实地址。
//
// 两种形态都要处理（实测 2026-09-18）：
//
//	① 302 重定向 —— 网易云 163cn.tv 就是这种，最终地址在 Location 里
//	② 200 + JSON —— 酷狗 t1.kugou.com 不重定向，真实地址在响应体的 data 字段里
//	   （实测：无效短码返回 {"status":0,"err_code":0,"data":null}）
//
// 只用前者会漏掉酷狗这类。
func expandShortLink(raw string, client *http.Client) (string, error) {
	if client == nil {
		client = shortLinkClient
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(raw), nil)
	if err != nil {
		return "", err
	}
	// 用移动端 UA：部分短链服务对桌面 UA 会跳到下载页而不是原始内容页
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 14_0 like Mac OS X) AppleWebKit/605.1.15")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// ① 发生了重定向：最终地址就是答案
	if resp.Request != nil && resp.Request.URL != nil {
		if final := resp.Request.URL.String(); final != req.URL.String() {
			return final, nil
		}
	}

	// ② 没有重定向：看响应体里有没有真实地址
	body, rerr := security.LimitedRead(resp.Body, security.DefaultOptions())
	if rerr != nil {
		return "", rerr
	}
	if u := firstURLInJSON(body); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("短链没有返回跳转地址（HTTP %d）", resp.StatusCode)
}

// firstURLInJSON 从响应体里取出第一个 http(s) 链接。
//
// 用于「短链服务不重定向、而是用 JSON 把真实地址给你」这种情况。
// 不假定具体字段名（各平台不一样），递归找第一个看起来像地址的字符串值。
func firstURLInJSON(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return ""
	}
	var v interface{}
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return ""
	}
	return findURLValue(v)
}

func findURLValue(v interface{}) string {
	switch t := v.(type) {
	case string:
		if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
			return t
		}
	case []interface{}:
		for _, e := range t {
			if u := findURLValue(e); u != "" {
				return u
			}
		}
	case map[string]interface{}:
		for _, e := range t {
			if u := findURLValue(e); u != "" {
				return u
			}
		}
	}
	return ""
}

func hostMatches(host string, wants []string) bool {
	for _, w := range wants {
		if host == w || strings.HasSuffix(host, "."+w) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func platformLabel(source string) string {
	switch source {
	case "wy":
		return "网易云音乐"
	case "tx":
		return "QQ 音乐"
	case "kg":
		return "酷狗音乐"
	case "kw":
		return "酷我音乐"
	}
	return source
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
