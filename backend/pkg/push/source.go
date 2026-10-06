package push

// 从已连接的账号拉取源曲目（歌单或日推），统一成 Track 结构。
//
// 两个平台的返回形态不同：
//   - 网易：歌单详情与日推都返回 {title, cover_url, items:[...]}
//   - QQ：日推直接返回曲目数组；歌单详情返回 {title, cover_url, items:[...]}

import (
	"fmt"
	"strings"

	"fn-lx-player/pkg/account"
)

// 来源类型
const (
	KindDaily    = "daily"    // 每日推荐
	KindPlaylist = "playlist" // 歌单
)

// Source 拉取到的源。
type Source struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	CoverURL string  `json:"cover_url,omitempty"`
	Owner    string  `json:"owner,omitempty"`
	Tracks   []Track `json:"tracks"`
}

// FetchSource 从已连接账号拉取源曲目。
//
//	provider: account.ProviderNetease / account.ProviderQQ
//	kind:     KindDaily / KindPlaylist
//	sourceID: 歌单 ID（kind=playlist 时必填）
func FetchSource(store *account.Store, provider, kind, sourceID string) (*Source, error) {
	// 「手动输入链接」来源不依赖账号，优先处理 —— 用户没登录也能订阅公开歌单。
	// 这种来源的平台由链接自己决定，参数里的 provider 被忽略。
	if kind == KindLink {
		return FetchSourceByLink(sourceID)
	}
	if store == nil {
		return nil, fmt.Errorf("账号存储不可用")
	}
	switch provider {
	case account.ProviderNetease:
		return fetchNetease(store, kind, sourceID)
	case account.ProviderQQ:
		return fetchQQ(store, kind, sourceID)
	}
	return nil, fmt.Errorf("不支持的平台: %q（可用：netease / qq）", provider)
}

func fetchNetease(store *account.Store, kind, sourceID string) (*Source, error) {
	cookie := store.Cookie(account.ProviderNetease)
	if cookie == "" {
		return nil, fmt.Errorf("尚未连接网易云账号，请先扫码登录")
	}

	var raw map[string]any
	var err error
	switch kind {
	case KindDaily:
		raw, err = account.NeteaseDailyTracks(cookie)
	case KindPlaylist:
		if strings.TrimSpace(sourceID) == "" {
			return nil, fmt.Errorf("拉取网易歌单需提供 source_id")
		}
		raw, err = account.NeteasePlaylistDetail(cookie, sourceID)
	default:
		return nil, fmt.Errorf("不支持的来源类型: %q（可用：daily / playlist）", kind)
	}
	if err != nil {
		return nil, err
	}

	src := &Source{
		ID:       strOf(raw["id"]),
		Title:    strOf(raw["title"]),
		CoverURL: strOf(raw["cover_url"]),
		Owner:    strOf(raw["owner"]),
		Tracks:   tracksFromItems(raw["items"]),
	}
	if len(src.Tracks) == 0 {
		return nil, fmt.Errorf("该来源没有可用曲目（可能为空歌单，或登录态已过期）")
	}
	return src, nil
}

func fetchQQ(store *account.Store, kind, sourceID string) (*Source, error) {
	cookies := account.ParseQQCookies(store.Cookie(account.ProviderQQ))
	if len(cookies) == 0 {
		return nil, fmt.Errorf("尚未连接 QQ 音乐账号，请先扫码登录")
	}

	switch kind {
	case KindDaily:
		songs, err := account.QQDaily(cookies)
		if err != nil {
			return nil, err
		}
		src := &Source{ID: "daily", Title: "QQ 音乐 · 每日推荐", Tracks: tracksFromItems(songs)}
		if len(src.Tracks) == 0 {
			return nil, fmt.Errorf("每日推荐为空：可能登录态已过期")
		}
		return src, nil

	case KindPlaylist:
		if strings.TrimSpace(sourceID) == "" {
			return nil, fmt.Errorf("拉取 QQ 歌单需提供 source_id")
		}
		raw, err := account.QQPlaylistDetail(cookies, sourceID)
		if err != nil {
			return nil, err
		}
		src := &Source{
			ID:       strOf(raw["id"]),
			Title:    strOf(raw["title"]),
			CoverURL: strOf(raw["cover_url"]),
			Owner:    strOf(raw["owner"]),
			Tracks:   tracksFromItems(raw["items"]),
		}
		if len(src.Tracks) == 0 {
			return nil, fmt.Errorf("该歌单没有可用曲目")
		}
		return src, nil
	}
	return nil, fmt.Errorf("不支持的来源类型: %q（可用：daily / playlist）", kind)
}

// tracksFromItems 把各平台返回的曲目数组统一成 Track。
func tracksFromItems(v any) []Track {
	rows, ok := v.([]any)
	if !ok {
		// QQ 日推返回的是 []map[string]any，需要再转一层
		if typed, ok2 := v.([]map[string]any); ok2 {
			rows = make([]any, len(typed))
			for i, r := range typed {
				rows[i] = r
			}
		} else {
			return nil
		}
	}

	out := make([]Track, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := firstNonEmptyStr(strOf(row["title"]), strOf(row["name"]))
		if name == "" {
			continue
		}
		durationMs := intOf(row["duration_ms"])
		if durationMs == 0 {
			durationMs = intOf(row["dt"])
		}
		out = append(out, Track{
			Name:     name,
			Artist:   firstNonEmptyStr(strOf(row["artist"]), strOf(row["singer"])),
			Album:    firstNonEmptyStr(strOf(row["album"]), strOf(row["albumname"])),
			Duration: durationMs / 1000,
			SongID:   firstNonEmptyStr(strOf(row["song_id"]), strOf(row["id"]), strOf(row["mid"])),
			Cover:    strOf(row["cover_url"]),
		})
	}
	return out
}

func strOf(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		if s == float64(int64(s)) {
			return fmt.Sprintf("%d", int64(s))
		}
		return fmt.Sprintf("%v", s)
	case int:
		return fmt.Sprintf("%d", s)
	}
	return ""
}

func intOf(v any) int {
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

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
