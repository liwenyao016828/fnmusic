package charts

// QQ 音乐公开歌单详情。
//
// ⚠️ **别再退回 `c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg`** ——
// 2026-09-21 实测它对匿名请求只返回
//
//	{"code":0,"subcode":4000,"msg":"check privacy error!"}
//
// 用户看到的是「歌单详情 500 / Failed to parse QQ playlist detail」，
// 以及 QQ 歌单链接订阅静默失效。
//
// 这个接口原先被 `charts`（歌单详情页）和 `push`（按链接订阅）**各写了一份**，
// 所以是**一起坏的**。现在实现收敛到本文件，两边共用 —— 别再各写一份。
//
// # 为什么选 v8 而不是新版 musicu.fcg
//
// 候选 `music.srfDissInfo.aiDissInfo`（`u.y.qq.com/cgi-bin/musicu.fcg`）也能用，
// 但它**截断到 1000 首**且要自己翻页；而 v8 一次返回全部曲目
// （实测 1254 首的歌单一次拿全）。
//
// # ⚠️ 新版返回的字段名变了，不是旧的 `songname`/`songmid`
//
//	songname  → name
//	songmid   → mid
//	albumname → album.name
//	albummid  → album.mid
//
// 而且 `cdlist` 从顶层挪到了 `data.cdlist`。
// 只换 URL 不换字段名的话，会「不报错但解析出空歌单」—— 更难查。

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/security"
)

const qqPlaylistDetailURL = "https://c.y.qq.com/v8/fcg-bin/fcg_v8_playlist_cp.fcg" +
	"?id=%s&format=json&newsong=1&platform=yqq&inCharset=utf8&outCharset=utf-8"

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

// FetchQQPlaylistDetail 抓取 QQ 音乐**公开**歌单详情（匿名可读，不需要 cookie）。
//
// client 传 nil 时用带超时的默认客户端。
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

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(qqPlaylistDetailURL, disstid), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://y.qq.com/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 QQ 歌单失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := security.LimitedRead(resp.Body, security.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("读取 QQ 歌单响应失败：%w", err)
	}

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
					// ⚠️ 新版字段名：name / mid / album.{name,mid}
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
		return nil, fmt.Errorf("QQ 歌单返回不是有效 JSON（可能被风控拦截）")
	}
	if len(raw.Data.Cdlist) == 0 {
		// 上游明确说「隐私校验失败」时，给一句用户能采取行动的话 ——
		// 而不是让他对着「解析失败」猜（见 HANDOVER §3.3.3 报错口径）。
		if raw.Subcode == 4000 || strings.Contains(strings.ToLower(raw.Msg), "privacy") {
			return nil, fmt.Errorf("这个 QQ 歌单不是公开歌单，上游拒绝匿名读取 —— 换一个公开歌单，或先登录 QQ 音乐账号")
		}
		return nil, fmt.Errorf("QQ 歌单读取失败（code=%d）—— 歌单可能已删除、或需要登录才能查看", raw.Code)
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

	out.Songs = make([]QQPlaylistSong, 0, len(cd.Songlist))
	for _, s := range cd.Songlist {
		names := make([]string, 0, len(s.Singer))
		for _, a := range s.Singer {
			if strings.TrimSpace(a.Name) != "" {
				names = append(names, a.Name)
			}
		}
		out.Songs = append(out.Songs, QQPlaylistSong{
			Name:      html.UnescapeString(s.Name),
			Songmid:   s.Mid,
			AlbumName: html.UnescapeString(s.Album.Name),
			AlbumMid:  s.Album.Mid,
			Interval:  s.Interval,
			Singers:   names,
			Cover:     qqAlbumCoverURL(s.Album.Mid, cover),
		})
	}

	return out, nil
}
