package charts

// QQ 音乐公开歌单详情。
//
// ⚠️ **别再退回 `c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg`** ——
// 2026-09-21 实测它对匿名请求只返回
//
//	{"code":0,"subcode":4000,"msg":"check privacy error!"}
//
// 2026-10-23 复测**仍然是这一条**：同一个公开歌单（disstid=7707261125），
// 老接口照旧 `check privacy error!`，所以它对匿名请求是彻底不可用了。
//
// 这个接口原先被 `charts`（歌单详情页）和 `push`（按链接订阅）**各写了一份**，
// 所以是**一起坏的**。现在实现收敛到本文件，两边共用 —— 别再各写一份。
//
// # 当前走两条匿名通道（都实测过，2026-10-23）
//
//  1. `u.y.qq.com/cgi-bin/musicu.fcg` 的 `music.srfDissInfo.aiDissInfo`
//     （POST JSON）—— **首选**。它是 QQ 网页端在用的通道，返回
//     `dirinfo` + `songlist`，并且给了 `total_song_num` / `hasmore`，
//     所以能靠 `song_begin` **翻页拿全**（实测 1283 首的歌单：1000 + 283）。
//     不存在的歌单：`req.data.code=-100006`，`msg="get diss info from tmem error"`。
//
//  2. `c.y.qq.com/v8/fcg-bin/fcg_v8_playlist_cp.fcg`（GET）—— **兜底**。
//     一次返回全部曲目（实测 1524 首一次拿全），但只有一个 URL、没有翻页语义。
//     不存在的 id：`{"code":10,"data":{"cdlist":[],...}}`；
//     更短的 7 位「dissid」（老式 `/n/yqq/playlist/<dissid>.html` 链接里的那种）
//     同样返回 `code=10` —— **这正是用户看到「歌单可能已删除」的典型来路**，
//     哪怕歌单本身好好的。
//
// 为什么还要两条：QQ 对匿名请求的风控是**按接口/按 IP 分别**判的。一条通道被
// 判成爬虫（返回空 `cdlist`、`check privacy error!`、或直接给 HTML），另一条
// 往往还能读到。多试一条能把「上游临时拒绝」和「歌单真没了」区分开，而不是
// 一律告诉用户「可能已删除」—— 用户 2026-10-23 报障时明确说过歌单没删、而且是公开的。
//
// 顺带实测过、但**没有采用**的：
//   - `music.playlist.PlaylistInfo` / `get_playlist_info` →
//     `{"code":500003,"subcode":860100001}`（模块/方法不存在）
//   - `https://y.qq.com/n/ryqq/playlist/<id>` 网页 → 无 cookie 时 302，
//     不是可用通道（曲目在前端 JS 里渲染）
//
// # ⚠️ 新版返回的字段名，不是旧的 `songname`/`songmid`
//
//	songname  → name
//	songmid   → mid
//	albumname → album.name
//	albummid  → album.mid
//
// v8 还把 `cdlist` 从顶层挪到了 `data.cdlist`。只换 URL 不换字段名的话，会
// 「不报错但解析出空歌单」—— 更难查。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/useragent"
)

const (
	// musicu 是 QQ 的通用 CGI，POST JSON；`aiDissInfo` 模块匿名可读公开歌单。
	qqMusicuURL    = "https://u.y.qq.com/cgi-bin/musicu.fcg"
	qqMusicuModule = "music.srfDissInfo.aiDissInfo"
	qqMusicuMethod = "uniform_get_Dissinfo"
	// 一页最多 1000 首（实测上限，再多上游会自己截到 1000）。
	qqMusicuPageSize = 1000
	// 防御性上限：60000 首。真撞上说明上游没按 song_begin 前进；
	// 这时不能把这一截当完整歌单返回，而是明确报「没读全」（见 qqMusicuTrunc）。
	qqMusicuMaxPages = 60

	// v8 兜底通道（GET，一次返回全部曲目）。
	qqPlaylistDetailURL = "https://c.y.qq.com/v8/fcg-bin/fcg_v8_playlist_cp.fcg" +
		"?id=%s&format=json&newsong=1&platform=yqq&inCharset=utf8&outCharset=utf-8"

	// 上游明确表示「没有这个歌单」的返回码。
	qqV8NotFoundCode    = 10
	qqMusicuNotFoundErr = -100006 // msg 通常是 "get diss info from tmem error"
)

// defaultQQClient 给没传 client 的调用方兜底（必须带超时，上游会挂）。
var defaultQQClient = &http.Client{Timeout: 15 * time.Second}

// QQPlaylistSong 是 QQ 歌单里的一首歌，字段已归一化（不把上游字段名泄漏给调用方）。
type QQPlaylistSong struct {
	Name      string
	Songmid   string
	AlbumName string
	AlbumMid  string
	Interval  int // 秒
	Singers   []string
	Cover     string // 已解析好的封面地址（https）
}

// QQPlaylist 是 QQ 歌单详情，字段已归一化。
type QQPlaylist struct {
	ID    string
	Name  string
	Cover string // 已解析好的封面地址（https）
	Desc  string
	Songs []QQPlaylistSong
}

// qqAlbumCoverURL 由专辑 mid 拼 300x300 封面；mid 为空时回落到歌单封面。
// 单独抽出来是为了让「歌单封面」和「单曲封面」两处规则一致。
func qqAlbumCoverURL(albumMid, fallback string) string {
	if strings.TrimSpace(albumMid) == "" {
		return fallback
	}
	return fmt.Sprintf("https://y.gtimg.cn/music/photo_new/T002R300x300M000%s.jpg", albumMid)
}

// qqRawSong 是两条通道**归一化之前**的中间形态：字段名统一，来源各拼各的。
// 有了它，解析逻辑只写一遍，换通道不用再抄一份（这一课 2026-09-21 已经踩过）。
type qqRawSong struct {
	Name      string
	Mid       string
	Interval  int
	AlbumName string
	AlbumMid  string
	Singers   []string
}

// qqConvertSongs 把中间形态转成对外的 QQPlaylistSong，顺便补封面与 HTML 实体解码。
func qqConvertSongs(raw []qqRawSong, playlistCover string) []QQPlaylistSong {
	out := make([]QQPlaylistSong, 0, len(raw))
	for _, s := range raw {
		out = append(out, QQPlaylistSong{
			Name:      html.UnescapeString(s.Name),
			Songmid:   s.Mid,
			AlbumName: html.UnescapeString(s.AlbumName),
			AlbumMid:  s.AlbumMid,
			Interval:  s.Interval,
			Singers:   s.Singers,
			Cover:     qqAlbumCoverURL(s.AlbumMid, playlistCover),
		})
	}
	return out
}

// qqFailKind 是「这次为什么没读到」的粗分类，决定最后给用户哪句话。
//
// 分类的意义：用户报障时明确说过「歌单没有删除、是公开的」（2026-10-23），
// 所以**不许**再把所有失败都写成「歌单可能已删除」—— 那会让人白找一圈。
type qqFailKind int

const (
	qqFailUnknown   qqFailKind = iota
	qqFailRefused              // 上游拒绝匿名读取（privacy / 空内容）
	qqFailNotFound             // 上游明确说没有这个歌单
	qqFailHTTP                 // 上游返回非 2xx（限流/风控）
	qqFailTransport            // 网络层没通
	qqFailParse                // 返回的不是 JSON（多为风控 HTML）
)

// qqFetchError 是单条通道的失败记录，保留上游原话，最后拼进用户提示里。
type qqFetchError struct {
	Channel string // 例如 "musicu.fcg" / "v8"
	Kind    qqFailKind
	Code    int
	Subcode int
	Msg     string
	Cause   error
}

func (e *qqFetchError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s：%v", e.Channel, e.Cause)
	}
	if e.Msg != "" {
		return fmt.Sprintf("%s(code=%d/subcode=%d/msg=%s)", e.Channel, e.Code, e.Subcode, e.Msg)
	}
	return fmt.Sprintf("%s(code=%d/subcode=%d)", e.Channel, e.Code, e.Subcode)
}

// qqClassifyEmpty 把「响应是合法 JSON，但里面只有空壳」归类。
//
// 约定：code 命中「不存在」码才算真没了；其余空壳一律当「匿名被拒/风控」
// 或「登录态不足」，这样用户不会被误导去删歌单重订阅。
//
// 实测（2026-10-23）：匿名读公开歌单正常时 code/subcode 都是 0；
// 需要登录时 code=0 & subcode=4000；歌单 id 不存在时 code=-100006。
func qqClassifyEmpty(code, subcode int, msg string) qqFailKind {
	lower := strings.ToLower(msg)
	if code == qqV8NotFoundCode || code == qqMusicuNotFoundErr {
		return qqFailNotFound
	}
	if subcode == 4000 || strings.Contains(lower, "privacy") ||
		strings.Contains(lower, "login") || strings.Contains(lower, "permission") {
		return qqFailRefused
	}
	return qqFailRefused
}

// ── 通道一：musicu.fcg（首选） ──

// qqMusicuPage 是 musicu 响应里我们用得到的部分。
type qqMusicuPage struct {
	Code int `json:"code"`
	Req  struct {
		Code int `json:"code"`
		Data struct {
			Code         int    `json:"code"`
			Subcode      int    `json:"subcode"`
			Msg          string `json:"msg"`
			TotalSongNum int    `json:"total_song_num"`
			HasMore      *int   `json:"hasmore"` // 实测总带这个字段；缺失按「未知」继续翻，不能默认 0 当结束
			Dirinfo      struct {
				ID       int64  `json:"id"`
				Title    string `json:"title"`
				PicURL   string `json:"picurl"`
				Desc     string `json:"desc"`
				HostNick string `json:"host_nick"`
				SongNum  int    `json:"songnum"`
			} `json:"dirinfo"`
			Songlist []struct {
				Name     string `json:"name"`
				Mid      string `json:"mid"`
				Interval int    `json:"interval"`
				Album    struct {
					Name string `json:"name"`
					Mid  string `json:"mid"`
				} `json:"album"`
				Singer []struct {
					Name string `json:"name"`
				} `json:"singer"`
			} `json:"songlist"`
		} `json:"data"`
	} `json:"req"`
}

// qqMusicuTrunc 是「这次只读到一部分」的证据（不是网络错误，是上游没给全）。
//
// 两个来源：①上游没按 song_begin 前进（返回重复页）；②翻到页数上限还没读完。
// 带上它说明 raws 可能比歌单短，调用方**不能**把它当完整歌单 ——
// FetchQQPlaylistDetail 会先试 v8 补读，补不齐就明确报错。
type qqMusicuTrunc struct {
	Reason string // 为什么停
	Got    int    // 实际读到的曲目数
	Total  int    // 上游声称的总数（0 = 未知）
}

// fetchQQPlaylistMusicu 走 musicu.fcg 的 aiDissInfo 模块读公开歌单。
//
// 为什么首选它：POST 到 u.y.qq.com，和网页端同一个通道；返回里有
// total_song_num / hasmore，所以**能翻页拿全**（v8 虽然一次给全，
// 但那套 c.y.qq.com 的旧 CGI 已经在 2026-09 废掉过一个，不能只押它）。
//
// 第三个返回值非 nil 表示「拿到了内容但不完整」（此时 err 必为 nil）：
// 这是有意和真失败分开的 —— 不完整要补读或明确报错，真失败要换通道。
func fetchQQPlaylistMusicu(client *http.Client, disstid string) (*QQPlaylist, *qqFetchError, *qqMusicuTrunc) {
	channel := "musicu.fcg"

	numericID, err := strconv.ParseInt(disstid, 10, 64)
	if err != nil {
		return nil, &qqFetchError{
			Channel: channel,
			Kind:    qqFailUnknown,
			Cause:   fmt.Errorf("歌单 id 不是数字：%q", disstid),
		}, nil
	}

	var (
		out   *QQPlaylist
		raws  []qqRawSong
		begin int
		total int
		pages int
		trunc *qqMusicuTrunc
		seen  = make(map[string]struct{}, qqMusicuPageSize) // 前面页已收的 mid
	)

	for page := 0; page < qqMusicuMaxPages; page++ {
		pages = page + 1
		param := map[string]any{
			"disstid":      numericID,
			"enc_host_uin": "",
			"tag":          1,
			"userinfo":     1,
			"song_begin":   begin,
			"song_num":     qqMusicuPageSize,
			"onlysonglist": 0,
		}
		payload := map[string]any{
			"comm": map[string]any{
				"ct": 11, "cv": "1003006", "v": "1003006", "uin": "0", "format": "json",
			},
			"req": map[string]any{
				"module": qqMusicuModule,
				"method": qqMusicuMethod,
				"param":  param,
			},
		}
		body, merr := json.Marshal(payload)
		if merr != nil {
			return nil, &qqFetchError{Channel: channel, Kind: qqFailParse, Cause: merr}, nil
		}

		req, rerr := http.NewRequest(http.MethodPost, qqMusicuURL, bytes.NewReader(body))
		if rerr != nil {
			return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: rerr}, nil
		}
		req.Header.Set("User-Agent", useragent.Desktop)
		req.Header.Set("Referer", "https://y.qq.com/")
		req.Header.Set("Content-Type", "application/json")

		resp, derr := client.Do(req)
		if derr != nil {
			return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: derr}, nil
		}
		// 非 2xx 别猜 body 含义（风控/限流常回 403/429，body 可能仍是 JSON）。
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, &qqFetchError{
				Channel: channel,
				Kind:    qqFailHTTP,
				Cause:   fmt.Errorf("上游返回 HTTP %d", resp.StatusCode),
			}, nil
		}
		respBody, berr := security.LimitedRead(resp.Body, security.DefaultOptions())
		resp.Body.Close()
		if berr != nil {
			return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: berr}, nil
		}

		var raw qqMusicuPage
		if uerr := json.Unmarshal(respBody, &raw); uerr != nil {
			return nil, &qqFetchError{Channel: channel, Kind: qqFailParse, Cause: uerr}, nil
		}

		d := raw.Req.Data
		// 错误码可能只出现在某一层：实测正常响应三层 code 都是 0，出错时错误码在
		// data.code（-100006=歌单不存在）；外层 code / req.code 一起看是防御
		// 「整包风控」这种只改外层的返回 —— 只看 data.code 会漏掉后者。
		code, subcode, msg := d.Code, d.Subcode, d.Msg
		if code == 0 && raw.Req.Code != 0 {
			code, subcode, msg = raw.Req.Code, 0, d.Msg
		}
		if code == 0 && raw.Code != 0 {
			code, subcode, msg = raw.Code, 0, d.Msg
		}
		if code != 0 {
			return nil, &qqFetchError{
				Channel: channel,
				Kind:    qqClassifyEmpty(code, subcode, msg),
				Code:    code,
				Subcode: subcode,
				Msg:     msg,
			}, nil
		}
		// code=0 且连 dirinfo 都是空壳 → 上游没给内容（匿名被拒/风控）。
		// 注意不能把「确实 0 首的空歌单」当失败：那种情况 dirinfo.title 是有值的。
		if d.Dirinfo.Title == "" && d.Dirinfo.ID == 0 && len(d.Songlist) == 0 {
			return nil, &qqFetchError{
				Channel: channel,
				Kind:    qqFailRefused,
				Code:    d.Code,
				Subcode: d.Subcode,
				Msg:     "上游返回空歌单信息（匿名请求可能被拒绝）",
			}, nil
		}

		if out == nil {
			out = &QQPlaylist{
				ID:    strconv.FormatInt(d.Dirinfo.ID, 10),
				Name:  cleanSourceName(html.UnescapeString(d.Dirinfo.Title)),
				Cover: strings.Replace(d.Dirinfo.PicURL, "http://", "https://", 1),
				Desc:  cleanSourceName(html.UnescapeString(d.Dirinfo.Desc)),
			}
			if out.ID == "0" || out.ID == "" {
				out.ID = disstid
			}
		}
		if d.TotalSongNum > 0 {
			total = d.TotalSongNum
		}

		if len(d.Songlist) == 0 {
			// 实测正常收尾就是「hasmore=0 + 空页」。total_song_num 可能大于实际可取数
			// （版权受限曲目被上游过滤），所以空页算正常结束，不算截断。
			break
		}

		// 跨页去重：只和前面页已收的 mid 比 —— 页内的重复是歌单自身内容，要保留。
		pageIDs := make([]string, 0, len(d.Songlist))
		added := 0
		for _, s := range d.Songlist {
			if s.Mid != "" {
				if _, dup := seen[s.Mid]; dup {
					continue // 交叠/重复页带回来的老曲目
				}
				pageIDs = append(pageIDs, s.Mid)
			}
			names := make([]string, 0, len(s.Singer))
			for _, a := range s.Singer {
				if strings.TrimSpace(a.Name) != "" {
					names = append(names, a.Name)
				}
			}
			raws = append(raws, qqRawSong{
				Name:      s.Name,
				Mid:       s.Mid,
				Interval:  s.Interval,
				AlbumName: s.Album.Name,
				AlbumMid:  s.Album.Mid,
				Singers:   names,
			})
			added++
		}
		for _, mid := range pageIDs {
			seen[mid] = struct{}{}
		}

		if added == 0 {
			// 整页都是前面页收过的曲目 → 上游没按 song_begin 前进（重复页）。
			// 已经收齐就不算截断；否则明确带出截断，绝不静默当成功。
			if total > 0 && len(raws) >= total {
				break
			}
			trunc = &qqMusicuTrunc{
				Reason: fmt.Sprintf("第 %d 页（song_begin=%d）与已收曲目完全重复，上游没按偏移前进", pages, begin),
			}
			break
		}

		if total > 0 && len(raws) >= total {
			break
		}
		// hasmore 缺失（nil）不能默认按 0 处理，否则会静默截断；实测上游总带这个字段。
		if d.HasMore != nil && *d.HasMore == 0 {
			break
		}
		begin += len(d.Songlist)
	}

	if out == nil {
		// 首轮空页已被上面的空壳检查拦掉；这里只做防御，避免 nil 解引用。
		return nil, &qqFetchError{
			Channel: channel,
			Kind:    qqFailRefused,
			Msg:     "上游没有返回歌单信息",
		}, nil
	}
	out.Songs = qqConvertSongs(raws, out.Cover)

	if trunc == nil && pages >= qqMusicuMaxPages && (total == 0 || len(raws) < total) {
		trunc = &qqMusicuTrunc{
			Reason: fmt.Sprintf("翻到页数上限（%d 页 x %d 首）还没读完", qqMusicuMaxPages, qqMusicuPageSize),
		}
	}
	if trunc != nil {
		trunc.Got = len(raws)
		trunc.Total = total
	}
	return out, nil, trunc
}

// ── 通道二：v8（兜底） ──

// fetchQQPlaylistV8 走 `c.y.qq.com/v8/fcg-bin/fcg_v8_playlist_cp.fcg`。
//
// 它一次返回全部曲目，但接口形态老旧、没有翻页语义；保留它是因为它是
// **另一套 CGI 命名空间**：musicu 被风控时它常常还能读到（2026-10-23 实测两边都通）。
func fetchQQPlaylistV8(client *http.Client, disstid string) (*QQPlaylist, *qqFetchError) {
	channel := "v8"

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(qqPlaylistDetailURL, disstid), nil)
	if err != nil {
		return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: err}
	}
	req.Header.Set("User-Agent", useragent.Desktop)
	req.Header.Set("Referer", "https://y.qq.com/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: err}
	}
	defer resp.Body.Close()

	// 非 2xx 先按状态码判失败：风控/限流常回 403/429（body 可能仍是 JSON 或 HTML），
	// 拿状态码才分得清「被拦」和「歌单不存在」。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &qqFetchError{
			Channel: channel,
			Kind:    qqFailHTTP,
			Cause:   fmt.Errorf("上游返回 HTTP %d", resp.StatusCode),
		}
	}

	body, err := security.LimitedRead(resp.Body, security.DefaultOptions())
	if err != nil {
		return nil, &qqFetchError{Channel: channel, Kind: qqFailTransport, Cause: err}
	}

	// 注意字段：新版是 name / mid / album.{name,mid}，老版是 songname / songmid。
	// 这里只认新版；老形状会解析成空 cdlist，直接落到下面的错误分支。
	var raw struct {
		Code    int    `json:"code"`
		Subcode int    `json:"subcode"`
		Msg     string `json:"msg"`
		Data    struct {
			Cdlist []struct {
				Disstid  string `json:"disstid"`
				Dissname string `json:"dissname"`
				Logo     string `json:"logo"`
				Desc     string `json:"desc"`
				Songlist []struct {
					Name     string `json:"name"`
					Mid      string `json:"mid"`
					Interval int    `json:"interval"`
					Album    struct {
						Name string `json:"name"`
						Mid  string `json:"mid"`
					} `json:"album"`
					Singer []struct {
						Name string `json:"name"`
					} `json:"singer"`
				} `json:"songlist"`
			} `json:"cdlist"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		// 拿到的多半是风控/登录页 HTML —— 单独归一类，别和「网络不通」混在一起。
		return nil, &qqFetchError{
			Channel: channel,
			Kind:    qqFailParse,
			Cause:   fmt.Errorf("返回不是有效 JSON（可能被风控拦截）"),
		}
	}
	if len(raw.Data.Cdlist) == 0 {
		return nil, &qqFetchError{
			Channel: channel,
			Kind:    qqClassifyEmpty(raw.Code, raw.Subcode, raw.Msg),
			Code:    raw.Code,
			Subcode: raw.Subcode,
			Msg:     raw.Msg,
		}
	}

	cd := raw.Data.Cdlist[0]
	cover := strings.Replace(cd.Logo, "http://", "https://", 1)

	out := &QQPlaylist{
		ID:    cd.Disstid,
		Name:  cleanSourceName(html.UnescapeString(cd.Dissname)),
		Cover: cover,
		Desc:  cleanSourceName(cd.Desc),
	}
	if out.ID == "" {
		out.ID = disstid
	}

	raws := make([]qqRawSong, 0, len(cd.Songlist))
	for _, s := range cd.Songlist {
		names := make([]string, 0, len(s.Singer))
		for _, a := range s.Singer {
			if strings.TrimSpace(a.Name) != "" {
				names = append(names, a.Name)
			}
		}
		raws = append(raws, qqRawSong{
			Name:      s.Name,
			Mid:       s.Mid,
			Interval:  s.Interval,
			AlbumName: s.Album.Name,
			AlbumMid:  s.Album.Mid,
			Singers:   names,
		})
	}
	out.Songs = qqConvertSongs(raws, cover)
	return out, nil
}

// qqExplainFailure 把「多条通道各自的失败」翻译成一句用户能行动的话。
//
// 判据顺序：拒绝 > 不存在 > HTTP > 网络 > 解析 > 空壳。拒绝排最前是因为它最可行动
// （换公开歌单 / 登录账号），且绝不能写成「已删除」—— 用户 2026-10-23 报障时
// 已经明确否认过「歌单被删」这个猜测。
func qqExplainFailure(disstid string, errs []*qqFetchError) error {
	details := make([]string, 0, len(errs))
	has := map[qqFailKind]bool{}
	for _, e := range errs {
		has[e.Kind] = true
		details = append(details, e.Error())
	}
	trail := strings.Join(details, "；")

	switch {
	case has[qqFailRefused]:
		return fmt.Errorf("QQ 上游拒绝匿名读取这个歌单（id=%s）—— 它不是公开歌单，或需要登录 QQ 音乐账号；"+
			"也可能是上游风控临时拦截，稍后重试。上游原话：%s", disstid, trail)
	case has[qqFailNotFound]:
		return fmt.Errorf("QQ 找不到这个歌单（id=%s）—— 歌单可能已被删除，或链接里的 id 不是歌单 id；"+
			"上游原话：%s", disstid, trail)
	case has[qqFailHTTP]:
		return fmt.Errorf("QQ 上游拒绝了这次请求（HTTP 状态异常，id=%s）—— 通常是限流或风控，稍后重试；上游原话：%s", disstid, trail)
	case has[qqFailTransport]:
		return fmt.Errorf("读取 QQ 歌单失败（网络问题，id=%s）：%s", disstid, trail)
	case has[qqFailParse]:
		return fmt.Errorf("QQ 歌单返回的不是有效 JSON（可能被风控或登录页拦截，id=%s）：%s", disstid, trail)
	default:
		return fmt.Errorf("QQ 上游没有给匿名请求返回歌单内容（id=%s）—— 歌单可能不是公开的（仅自己/仅好友可见），"+
			"或触发了上游风控。上游原话：%s", disstid, trail)
	}
}

// FetchQQPlaylistDetail 抓取 QQ 音乐**公开**歌单详情（匿名可读，不需要 cookie）。
//
// client 传 nil 时用带超时的默认客户端。
//
// 先 musicu.fcg（能翻页拿全），失败再退 v8（一次给全）。两条都失败时按
// 「拒绝匿名读取 / 歌单不存在 / HTTP / 网络 / 解析」分类报错，不再一律说「可能已删除」。
//
// 分页拿到结果**不完整**时（重复页 / 翻页上限），先让 v8 补读；v8 也补不齐就
// 明确报「没读全」，绝不把残缺歌单当成功交出去。
//
// 返回的字段都已归一化，调用方不需要知道上游长什么样。
func FetchQQPlaylistDetail(client *http.Client, disstid string) (*QQPlaylist, error) {
	disstid = strings.TrimSpace(disstid)
	if disstid == "" {
		return nil, fmt.Errorf("缺少歌单 id")
	}
	if client == nil {
		client = defaultQQClient
	}

	pl, firstErr, trunc := fetchQQPlaylistMusicu(client, disstid)
	if trunc != nil {
		// musicu 拿到了内容但不完整：用 v8 补读（它一次给全）。
		full, verr := fetchQQPlaylistV8(client, disstid)
		if verr == nil && full != nil && (pl == nil || len(full.Songs) >= len(pl.Songs)) {
			return full, nil
		}
		detail := "v8 也没能补齐"
		if verr != nil {
			detail = "v8 补读失败：" + verr.Error()
		} else if full != nil {
			detail = fmt.Sprintf("v8 只返回了 %d 首", len(full.Songs))
		}
		return nil, qqExplainTruncation(disstid, trunc, detail)
	}
	if firstErr == nil {
		return pl, nil
	}

	// musicu 失败不一定代表歌单有问题：可能是风控只拦了这一条通道。
	// 再试 v8，既多一次成功机会，也让最终报错能带上两边各自的原话。
	pl2, secondErr := fetchQQPlaylistV8(client, disstid)
	if secondErr == nil {
		return pl2, nil
	}

	return nil, qqExplainFailure(disstid, []*qqFetchError{firstErr, secondErr})
}

// qqExplainTruncation 把「读到了但没读全」翻译成用户能行动的一句话。
//
// 刻意说「没有采用这次结果」：2026-10-23 的报障核心就是「不能把残缺当成功」，
// 所以宁可这一轮失败（可以重试），也不返回一份看起来完整、实则少了几百首的歌单。
func qqExplainTruncation(disstid string, t *qqMusicuTrunc, detail string) error {
	human := fmt.Sprintf("只读到前 %d 首", t.Got)
	if t.Total > 0 {
		human = fmt.Sprintf("只读到前 %d 首（上游称共 %d 首）", t.Got, t.Total)
	}
	trail := "musicu.fcg：" + t.Reason
	if detail != "" {
		trail += "；" + detail
	}
	return fmt.Errorf("这个 QQ 歌单没读全：%s，所以没有采用这次结果（id=%s）—— 避免给你一份缺歌的歌单，"+
		"请稍后重试；如果每次都这样，把歌单 id 一起反馈出来。上游详情：%s", human, disstid, trail)
}
