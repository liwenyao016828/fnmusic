package api

// A 方案：没有浏览器上报时，后端用已登录账号（或公开接口）自动抓取歌单/收藏。
//
// 背景：洛雪音源脚本解析直链只能在浏览器里执行，但「歌单里有哪些歌」并不需要
// 音源脚本——网易/QQ 的歌单接口后端自己就能调（登录后还能拉私有歌单）。
// 因此「发现」这一步不再依赖网页端上报：
//   - kind=playlist：优先走「链接 + 已登录账号」通道（能拉私有歌单，字段口径也和推送一致）；
//     没有账号时回退平台**公开歌单接口**（匿名可读公开单）。
//     **只给了 ID 时会先由 `charts.BuildPlaylistLink` 拼成标准链接**，
//     所以「精选歌单 / 官方榜单」这种只有 id 的订阅入口**不需要登录也能跑**；
//     拼不出链接的平台（kg / kw）才退回「按 ID + 必须有账号」的老路径。
//   - kind=favorites：PlaylistIDs 逐个拉；为空时自动识别账号里的「喜欢」歌单。
//   - kind=chart：仍要浏览器上报（榜单数据来自 lx 脚本，后端没有通道）。
//
// 后端抓到的曲目**没有直链**（URL 为空），BuildPlan 会把它们登记成 pending，
// 下载仍由网页端「补下载」完成 —— 合规边界不变。

import (
	"fmt"
	"strings"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/charts"
	"fn-lx-player/pkg/monitor"
	"fn-lx-player/pkg/push"
)

// 真实通道放包级变量，便于离线测试替换分支逻辑
var (
	bdFetchSource  = push.FetchSource
	bdFetchByLink  = push.FetchSourceByLink
	bdResolveLink  = func(link string) (string, string, error) { return charts.ResolvePlaylistLink(link, nil) }
	bdNeteaseLists = func(cookie string) ([]map[string]any, error) { return account.NeteaseUserPlaylists(cookie, "", 200) }
	bdQQLists      = func(cookies map[string]string) ([]map[string]any, error) { return account.QQPlaylists(cookies) }
)

// monitorPlatformOf provider → 曲目 source 代号（与浏览器上报、账号 tracks 接口同一口径）
func monitorPlatformOf(provider string) string {
	switch provider {
	case account.ProviderNetease:
		return "wy"
	case account.ProviderQQ:
		return "tx"
	}
	return ""
}

// providerOfMonitorSource 把 wy/tx/netease/qq 归一化成 provider，未知返回空串
func providerOfMonitorSource(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "wy", "netease", "ne", "网易", "网易云":
		return account.ProviderNetease
	case "tx", "qq", "qq音乐":
		return account.ProviderQQ
	}
	return ""
}

func (s *Server) cookieOf(provider string) string {
	if s == nil || s.accountStore == nil {
		return ""
	}
	return s.accountStore.Cookie(provider)
}

// backendDiscover 后端自抓曲目。
//
// handled=false 表示该监控类型（目前只有 chart）后端没有抓取通道，
// 调用方应回退「浏览器上报」通道。
func (s *Server) backendDiscover(mon *monitor.Monitor) (songs []monitor.Song, warnings []string, handled bool, err error) {
	switch mon.Kind {
	case monitor.KindPlaylist:
		songs, warnings, err = s.discoverPlaylists(mon)
		return songs, warnings, true, err
	case monitor.KindFavorites:
		songs, warnings, err = s.discoverFavorites(mon)
		return songs, warnings, true, err
	}
	return nil, nil, false, nil
}

func (s *Server) discoverPlaylists(mon *monitor.Monitor) ([]monitor.Song, []string, error) {
	if len(mon.Target.Playlists) == 0 {
		return nil, nil, fmt.Errorf("歌单监控没有配置来源：请绑定歌单链接或歌单 ID")
	}

	var (
		songs    []monitor.Song
		warnings []string
		firstErr string
	)
	for _, ref := range mon.Target.Playlists {
		src, platform, err := s.fetchPlaylistRef(mon, ref)
		if err != nil {
			w := fmt.Sprintf("来源「%s」抓取失败：%s", refLabel(ref), err)
			warnings = append(warnings, w)
			if firstErr == "" {
				firstErr = w
			}
			continue
		}
		songs = append(songs, sourceToSongs(src, platform)...)
	}

	// 全部失败 → 报错让本轮 run 记 error，避免「抓不到歌」被误判成「歌单清空」而完成基线
	if len(songs) == 0 && firstErr != "" {
		return nil, warnings, fmt.Errorf("所有来源都抓取失败：%s", firstErr)
	}
	return songs, warnings, nil
}

// fetchPlaylistRef 拉单个歌单来源，返回统一曲目源与其平台代号
func (s *Server) fetchPlaylistRef(mon *monitor.Monitor, ref monitor.PlaylistRef) (*push.Source, string, error) {
	link := strings.TrimSpace(ref.Link)
	id := strings.TrimSpace(ref.ID)

	// ⚠️ 只给了 ID 时，**先把它拼成标准歌单链接**，再走「按链接」通道。
	//
	// 两条通道的**门槛不一样**：
	//   按链接 → 平台公开接口，**不需要登录**；
	//   按 ID  → 必须有已登录账号（下面要读 cookie）。
	// 而前端「精选歌单」与「官方榜单」两条订阅入口**本来就只拿得到 id、没有分享链接**
	// （平台接口只返回 id）。不拼链接的话，没连账号的用户会看到「订阅成功但新歌永远不来」——
	// 每轮 run 都记一条「尚未连接 xx 账号，无法按 ID 拉取」。
	//
	// 拼不出来（kg / kw 等公开接口形态不同的平台）就保持原样，走下面的「按 ID」分支，
	// 由它给出「无法判定平台 / 尚未连接账号」这种准确报错。
	if link == "" && id != "" {
		code := strings.TrimSpace(ref.Source)
		if code == "" && len(mon.Sources) == 1 {
			code = mon.Sources[0]
		}
		if l := charts.BuildPlaylistLink(code, id); l != "" {
			link = l
		}
	}

	if link != "" {
		code, pid, rerr := bdResolveLink(link)
		if rerr != nil {
			return nil, "", fmt.Errorf("无法解析歌单链接（%v）", rerr)
		}
		provider := providerOfMonitorSource(code)
		// 已登录优先：能拉私有歌单，字段口径也和推送一致；失败再退回公开接口
		if provider != "" && s.cookieOf(provider) != "" && pid != "" {
			src, err := bdFetchSource(s.accountStore, provider, push.KindPlaylist, pid)
			if err == nil {
				return src, code, nil
			}
		}
		src, err := bdFetchByLink(link)
		if err != nil {
			return nil, "", err
		}
		return src, code, nil
	}

	if id == "" {
		return nil, "", fmt.Errorf("未绑定歌单链接或 ID")
	}
	code := strings.TrimSpace(ref.Source)
	if code == "" && len(mon.Sources) == 1 {
		code = mon.Sources[0]
	}
	provider := providerOfMonitorSource(code)
	if provider == "" {
		return nil, "", fmt.Errorf("无法判定平台：请改用歌单链接，或标注 source（wy/tx）")
	}
	if s.cookieOf(provider) == "" {
		return nil, "", fmt.Errorf("尚未连接 %s 账号，无法按 ID 拉取", provider)
	}
	src, err := bdFetchSource(s.accountStore, provider, push.KindPlaylist, id)
	if err != nil {
		return nil, "", err
	}
	return src, monitorPlatformOf(provider), nil
}

func (s *Server) discoverFavorites(mon *monitor.Monitor) ([]monitor.Song, []string, error) {
	providers := make([]string, 0, 2)
	for _, c := range mon.Sources {
		p := providerOfMonitorSource(c)
		if p != "" && s.cookieOf(p) != "" && !containsStr(providers, p) {
			providers = append(providers, p)
		}
	}
	if len(providers) == 0 {
		for _, p := range []string{account.ProviderNetease, account.ProviderQQ} {
			if s.cookieOf(p) != "" {
				providers = append(providers, p)
			}
		}
	}
	if len(providers) == 0 {
		return nil, nil, fmt.Errorf("收藏监控需要至少连接一个账号（网易云 / QQ 音乐）")
	}

	ids := mon.Target.PlaylistIDs
	if len(ids) > 0 && len(providers) > 1 {
		providers = providers[:1]
	}

	var (
		songs    []monitor.Song
		warnings []string
		firstErr string
		tried    int
	)
	for _, provider := range providers {
		if s.cookieOf(provider) == "" {
			w := fmt.Sprintf("跳过 %s：尚未连接账号", provider)
			warnings = append(warnings, w)
			continue
		}
		listIDs := ids
		if len(listIDs) == 0 {
			liked, err := s.likedPlaylistIDs(provider)
			if err != nil {
				w := fmt.Sprintf("读取 %s 歌单列表失败：%s", provider, err)
				warnings = append(warnings, w)
				if firstErr == "" {
					firstErr = w
				}
				continue
			}
			if len(liked) == 0 {
				w := fmt.Sprintf("%s 账号里没有找到「喜欢」歌单", provider)
				warnings = append(warnings, w)
				continue
			}
			listIDs = liked
		}
		for _, id := range listIDs {
			tried++
			src, err := bdFetchSource(s.accountStore, provider, push.KindPlaylist, strings.TrimSpace(id))
			if err != nil {
				w := fmt.Sprintf("拉取 %s 收藏歌单 %s 失败：%s", provider, id, err)
				warnings = append(warnings, w)
				if firstErr == "" {
					firstErr = w
				}
				continue
			}
			songs = append(songs, sourceToSongs(src, monitorPlatformOf(provider))...)
		}
	}

	if len(songs) == 0 && tried > 0 && firstErr != "" {
		return nil, warnings, fmt.Errorf("所有收藏来源都抓取失败：%s", firstErr)
	}
	return songs, warnings, nil
}

// likedPlaylistIDs 从账号歌单列表里识别「喜欢」歌单（网易「我喜欢的音乐」/ QQ「我喜欢」）
func (s *Server) likedPlaylistIDs(provider string) ([]string, error) {
	var (
		rows []map[string]any
		err  error
	)
	switch provider {
	case account.ProviderNetease:
		rows, err = bdNeteaseLists(s.cookieOf(provider))
	case account.ProviderQQ:
		rows, err = bdQQLists(account.ParseQQCookies(s.cookieOf(provider)))
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, 1)
	for _, r := range rows {
		name := neteaseStr(r["name"])
		id := neteaseStr(r["id"])
		if id != "" && strings.Contains(name, "喜欢") {
			out = append(out, id)
		}
	}
	return out, nil
}

// sourceToSongs push.Source → monitor.Song（带单源曲目上限，与监控既有口径一致）
func sourceToSongs(src *push.Source, platform string) []monitor.Song {
	if src == nil || platform == "" {
		return nil
	}
	origin := src.Title
	if origin == "" {
		origin = src.ID
	}
	out := make([]monitor.Song, 0, len(src.Tracks))
	for _, t := range src.Tracks {
		if len(out) >= monitor.DefaultMaxTracksPerSource {
			break
		}
		if strings.TrimSpace(t.SongID) == "" || strings.TrimSpace(t.Name) == "" {
			continue
		}
		out = append(out, monitor.Song{
			Source:   platform,
			ID:       t.SongID,
			Name:     t.Name,
			Artist:   t.Artist,
			Album:    t.Album,
			Duration: t.Duration,
			Cover:    t.Cover,
			Origin:   origin,
		})
	}
	return out
}

func refLabel(ref monitor.PlaylistRef) string {
	if n := strings.TrimSpace(ref.Name); n != "" {
		return n
	}
	if l := strings.TrimSpace(ref.Link); l != "" {
		return l
	}
	if i := strings.TrimSpace(ref.ID); i != "" {
		return "ID " + i
	}
	return "（未配置）"
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
