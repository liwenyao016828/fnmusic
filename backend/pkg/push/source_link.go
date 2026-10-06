package push

// 手动输入歌单链接的来源。
//
// 与 KindPlaylist 的区别：**不依赖已登录账号** —— 走平台的公开歌单接口，
// 所以没登录网易 / QQ 也能订阅一个公开歌单。
//
// 适用场景：从 App「复制链接」拿到一个公开歌单，想推送到飞牛歌单，
// 但不想为此专门登录账号。
//
// 支持的平台只有网易云 / QQ 音乐：这两个平台的公开歌单接口不需要 cookie
// （网易的 playlist/detail、QQ 的 v8/fcg-bin/fcg_v8_playlist_cp.fcg 都允许匿名读公开歌单）。
// ⚠️ QQ 那个接口 2026-09-21 换过一次（旧 qzone/fcg_ucc_getcdinfo 已对匿名请求返回
// 「check privacy error!」）—— 抓取实现统一在 charts.FetchQQPlaylistDetail，别再各写一份。
// 酷狗 / 酷我虽然也能从链接里解析出 id，但公开歌单接口形态不同，暂不支持。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/charts"
	"fn-lx-player/pkg/security"
)

// KindLink 来源类型：手动输入的歌单分享链接。
// 此时 Task.SourceID 里存的是**链接**而不是歌单 id。
const KindLink = "link"

// linkClient 抓公开歌单用的客户端（带 SSRF 校验）
var linkClient = security.NewSafeClient(security.DefaultOptions(), 15*time.Second)

// FetchSourceByLink 按歌单分享链接抓取曲目（公开接口，无需登录）。
//
// 短链（163cn.tv / c6.y.qq.com/base/fcgi-bin/u 等）由 charts.ResolvePlaylistLink
// 负责展开，这里不用重复处理。
func FetchSourceByLink(link string) (*Source, error) {
	source, id, err := charts.ResolvePlaylistLink(strings.TrimSpace(link), nil)
	if err != nil {
		return nil, err
	}
	switch source {
	case "wy":
		return fetchPublicNeteasePlaylist(id)
	case "tx":
		return fetchPublicQQPlaylist(id)
	}
	return nil, fmt.Errorf("暂不支持用链接订阅「%s」的歌单，目前支持网易云 / QQ 音乐", source)
}

// getJSONBody 发 GET 并读回响应体
func getJSONBody(apiURL, referer, cookie string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := linkClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return security.LimitedRead(resp.Body, security.DefaultOptions())
}

func fetchPublicNeteasePlaylist(id string) (*Source, error) {
	apiURL := fmt.Sprintf("https://music.163.com/api/playlist/detail?id=%s&s=0&n=1000", id)
	body, err := getJSONBody(apiURL, "https://music.163.com/", "os=pc; osver=Microsoft-Windows-10; appver=2.9.7;")
	if err != nil {
		return nil, fmt.Errorf("读取网易云歌单失败：%w", err)
	}

	var raw struct {
		Code   int `json:"code"`
		Result struct {
			Name        string `json:"name"`
			CoverImgUrl string `json:"coverImgUrl"`
			Creator     struct {
				Nickname string `json:"nickname"`
			} `json:"creator"`
			Tracks []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Duration int    `json:"duration"` // 毫秒
				Artists  []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					Name   string `json:"name"`
					PicUrl string `json:"picUrl"`
				} `json:"album"`
			} `json:"tracks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("网易云歌单返回不是有效 JSON（可能被风控拦截）")
	}
	if raw.Code != 200 {
		return nil, fmt.Errorf("网易云歌单读取失败（code=%d）—— 歌单可能已删除、或需要登录才能查看", raw.Code)
	}

	out := &Source{
		ID:       id,
		Title:    raw.Result.Name,
		CoverURL: strings.Replace(raw.Result.CoverImgUrl, "http://", "https://", 1),
		Owner:    raw.Result.Creator.Nickname,
	}
	for _, t := range raw.Result.Tracks {
		names := make([]string, 0, len(t.Artists))
		for _, a := range t.Artists {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		artist := strings.Join(names, ", ")
		if artist == "" {
			artist = "未知歌手"
		}
		out.Tracks = append(out.Tracks, Track{
			Name:     t.Name,
			Artist:   artist,
			Album:    t.Album.Name,
			Duration: t.Duration / 1000,
			SongID:   fmt.Sprintf("%d", t.ID),
			Cover:    t.Album.PicUrl,
		})
	}
	if len(out.Tracks) == 0 {
		return nil, fmt.Errorf("这个网易云歌单没有曲目（可能是空歌单，或需要登录才能查看）")
	}
	return out, nil
}

func fetchPublicQQPlaylist(disstid string) (*Source, error) {
	// ⚠️ 抓取 + 解析交给 charts.FetchQQPlaylistDetail ——
	// 这里原本自己写了一份（同一个 QQ 老接口），2026-09-21 QQ 换接口时
	// 它和歌单详情页**一起坏了**。别再复制一份，改接口只需要改 qq_playlist.go。
	pl, err := charts.FetchQQPlaylistDetail(linkClient, disstid)
	if err != nil {
		return nil, err
	}
	if len(pl.Songs) == 0 {
		return nil, fmt.Errorf("这个 QQ 歌单没有曲目（可能是空歌单，或需要登录才能查看）")
	}

	out := &Source{ID: pl.ID, Title: pl.Name, CoverURL: pl.Cover}
	for _, s := range pl.Songs {
		artist := strings.Join(s.Singers, ", ")
		if artist == "" {
			artist = "未知歌手"
		}
		out.Tracks = append(out.Tracks, Track{
			Name:     s.Name,
			Artist:   artist,
			Album:    s.AlbumName,
			Duration: s.Interval,
			SongID:   s.Songmid,
			Cover:    s.Cover,
		})
	}
	return out, nil
}
