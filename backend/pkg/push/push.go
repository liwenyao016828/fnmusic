// Package push 把曲目推送到飞牛音乐歌单。
//
// 本包提供两件事：
//   - 核心推送能力（PushTracks）：在飞牛曲库中匹配曲目 → 写入指定歌单
//   - 来源拉取（FetchTracks）：从已连接的账号拉取网易/QQ 的歌单或日推
//
// 「单次推送」与「订阅任务」都复用这里的逻辑，避免两处实现分叉。
package push

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/security"
)

// matchConcurrency 曲库匹配的并发上限。
//
// 每首歌要打 1~2 次飞牛搜索接口（先按歌名，未命中再按「歌手 + 歌名」），
// 原先串行执行 —— 一个 200 首的日推就是 ~400 次串行往返，慢且没必要。
// 这里改成有界并发：既能显著缩短大歌单的匹配时间，又不会把 NAS 打满。
const matchConcurrency = 6

// Track 待推送的曲目。与来源无关的中间结构。
type Track struct {
	Name     string `json:"name"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Duration int    `json:"duration"` // 秒
	SongID   string `json:"song_id,omitempty"`
	Cover    string `json:"cover_url,omitempty"`
}

// ItemResult 单曲的匹配结果。
type ItemResult struct {
	Name          string  `json:"name"`
	Artist        string  `json:"artist"`
	Score         float64 `json:"score"`
	Matched       bool    `json:"matched"`
	FnosTrackGUID string  `json:"fnos_track_guid,omitempty"`
	FnosTitle     string  `json:"fnos_title,omitempty"`
	Reason        string  `json:"reason,omitempty"`
}

// Result 一次推送的结果。
type Result struct {
	GUID            string       `json:"guid"`
	Title           string       `json:"title"`
	CreatedPlaylist bool         `json:"created_playlist"`
	Total           int          `json:"total"`
	Matched         int          `json:"matched"`
	Added           int          `json:"added"`
	AlreadyPresent  int          `json:"already_present"`
	Missing         []ItemResult `json:"missing"`
	Items           []ItemResult `json:"items"`
	CoverWarning    string       `json:"cover_warning,omitempty"`
	DryRun          bool         `json:"dry_run"`

	// Expired 本轮因超出保留期而从歌单移除的曲目数（见 retention.go）
	Expired int `json:"expired,omitempty"`
	// Pushed 清理后仍保留的「本任务推送过」清单，调用方需覆盖回任务持久化
	Pushed []PushedTrack `json:"pushed,omitempty"`
	// PruneWarning 保留期清理失败的原因（不影响本轮推送结果）
	PruneWarning string `json:"prune_warning,omitempty"`
}

// Options 推送选项。
type Options struct {
	Title       string // 目标歌单名
	Description string
	CoverURL    string
	// FnosGUID 已推送歌单的 guid。传入后复用之（稳定身份，避免重名建新单）。
	FnosGUID  string
	SyncCover bool
	DryRun    bool

	// RetentionMode / RetentionDays 保留期策略，见 retention.go。
	// keep（默认）表示推上去就不动；days 表示超过 RetentionDays 天自动移除。
	RetentionMode string
	RetentionDays int
	// Pushed 该任务此前推送过的曲目记录。
	// 清理只在这个范围内进行 —— 绝不碰用户手动加进歌单的曲目。
	Pushed []PushedTrack
}

// PushTracks 把曲目推送到飞牛音乐歌单。
//
// 匹配策略：在飞牛曲库按「歌名」与「歌手 + 歌名」两次检索，用 match.Similarity 打分，
// 低于 match.MatchMinScore 视为未命中——宁缺毋滥，绝不张冠李戴。
func PushTracks(m *fnos.Music, tracks []Track, opts Options) (*Result, error) {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		return nil, fmt.Errorf("请提供歌单名称")
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("请提供待推送曲目")
	}

	res := &Result{Title: title, Total: len(tracks), DryRun: opts.DryRun}
	res.Missing = make([]ItemResult, 0)
	res.Items = make([]ItemResult, 0, len(tracks))

	// 1. 确定目标歌单：有 guid 就直接用（稳定身份），否则按名称查找或创建
	guid := strings.TrimSpace(opts.FnosGUID)
	if guid == "" {
		g, created, err := m.FindOrCreatePlaylist(title, opts.Description)
		if err != nil {
			return nil, err
		}
		guid, res.CreatedPlaylist = g, created
	}
	res.GUID = guid

	// 2. 逐曲在飞牛曲库中匹配（有界并发；结果按原顺序汇总，保持输出确定性）
	type outcome struct {
		item ItemResult
		guid string // 命中的飞牛曲目 GUID；空表示未命中
	}
	outcomes := make([]outcome, len(tracks))
	{
		sem := make(chan struct{}, matchConcurrency)
		var wg sync.WaitGroup
		for i := range tracks {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()

				t := tracks[i]
				found, score, ok := matchInLibrary(m, match.Catalog{
					Name: t.Name, Artist: t.Artist, Album: t.Album, Duration: t.Duration,
				})
				item := ItemResult{Name: t.Name, Artist: t.Artist, Score: round2(score)}
				if !ok {
					item.Reason = "飞牛曲库中没有足够相似的歌曲"
					outcomes[i] = outcome{item: item}
					return
				}
				item.Matched = true
				item.FnosTrackGUID = found.ID
				item.FnosTitle = found.Name
				outcomes[i] = outcome{item: item, guid: found.ID}
			}(i)
		}
		wg.Wait()
	}

	// 汇总：保持与输入相同的顺序，并按 GUID 去重
	matched := make([]string, 0, len(tracks))
	seen := make(map[string]bool, len(tracks))
	for _, o := range outcomes {
		res.Items = append(res.Items, o.item)
		if o.guid == "" {
			res.Missing = append(res.Missing, o.item)
			continue
		}
		if !seen[o.guid] {
			seen[o.guid] = true
			matched = append(matched, o.guid)
		}
	}
	res.Matched = len(matched)

	// 3. 干跑：只回报匹配结果
	if opts.DryRun {
		return res, nil
	}

	// 4. 写入歌单（跳过已存在的）
	existing, err := m.PlaylistTracks(guid)
	if err != nil {
		return nil, fmt.Errorf("读取目标歌单失败: %w", err)
	}
	current := make(map[string]bool, len(existing))
	for _, row := range existing {
		if g, _ := row["guid"].(string); g != "" {
			current[g] = true
		}
	}
	toAdd := make([]string, 0, len(matched))
	for _, g := range matched {
		if !current[g] {
			toAdd = append(toAdd, g)
		}
	}
	if len(toAdd) > 0 {
		if err := m.AddTracks(guid, toAdd); err != nil {
			return nil, fmt.Errorf("写入歌单失败: %w", err)
		}
	}
	res.Added = len(toAdd)
	res.AlreadyPresent = len(matched) - len(toAdd)

	// 5. 可选：同步封面（失败不影响已加入的曲目）
	if opts.SyncCover && opts.CoverURL != "" {
		if err := syncCover(m, guid, title, opts.CoverURL); err != nil {
			res.CoverWarning = err.Error()
		}
	}

	// 6. 维护「本任务推送过」清单，并按保留期清理超期曲目（见 retention.go）
	now := time.Now().Unix()
	added := make([]PushedTrack, 0, len(toAdd))
	for _, g := range toAdd {
		added = append(added, PushedTrack{GUID: g, AddedAt: now})
	}
	res.Pushed = MergePushed(opts.Pushed, added)

	if NormalizeRetentionMode(opts.RetentionMode) == RetentionDays {
		removed, kept, pruneErr := PruneExpired(m, guid, res.Pushed, opts.RetentionDays)
		res.Pushed = kept
		res.Expired = len(removed)
		if pruneErr != nil {
			// 清理失败不影响本轮推送结果，只记警告（未成功的会留在 Pushed 里下轮重试）
			res.PruneWarning = pruneErr.Error()
		}
	}

	return res, nil
}

// matchInLibrary 在飞牛曲库中为 source 找最佳匹配。
// 依次用「歌名」与「歌手 + 歌名」检索，任一达到阈值即返回。
func matchInLibrary(m *fnos.Music, source match.Catalog) (match.Catalog, float64, bool) {
	if strings.TrimSpace(source.Name) == "" {
		return match.Catalog{}, 0, false
	}
	queries := []string{source.Name}
	if source.Artist != "" {
		queries = append(queries, source.Artist+" "+source.Name)
	}

	var best match.Catalog
	bestScore := 0.0
	seen := make(map[string]bool)
	for _, q := range queries {
		rows, err := m.SearchTracks(q)
		if err != nil {
			continue
		}
		for _, row := range rows {
			c := trackToCatalog(row)
			if c.ID == "" || seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if score := match.Similarity(source, c); score > bestScore {
				best, bestScore = c, score
			}
		}
		if bestScore >= match.MatchMinScore {
			break
		}
	}
	if bestScore < match.MatchMinScore {
		return match.Catalog{}, bestScore, false
	}
	return best, bestScore, true
}

// trackToCatalog 把飞牛曲库返回的曲目对象转成 match.Catalog。
// 飞牛字段命名不完全固定，这里兼容常见几种写法。
func trackToCatalog(row map[string]any) match.Catalog {
	return match.Catalog{
		ID:       firstString(row, "guid", "id", "trackGUID"),
		Name:     firstString(row, "title", "name", "songName"),
		Album:    firstString(row, "albumName", "album"),
		Artist:   fnosArtist(row),
		Duration: fnosDurationSec(row),
	}
}

func firstString(row map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// fnosArtist 兼容 artists(数组) / artist / singer 三种写法。
func fnosArtist(row map[string]any) string {
	switch v := row["artists"].(type) {
	case []any:
		names := make([]string, 0, len(v))
		for _, item := range v {
			switch a := item.(type) {
			case string:
				names = append(names, a)
			case map[string]any:
				if n, _ := a["name"].(string); n != "" {
					names = append(names, n)
				}
			}
		}
		if len(names) > 0 {
			return strings.Join(names, " / ")
		}
	case string:
		if v != "" {
			return v
		}
	}
	return firstString(row, "artist", "singer", "author")
}

// fnosDurationSec 兼容毫秒与秒两种时长单位。
func fnosDurationSec(row map[string]any) int {
	for _, k := range []string{"duration_ms", "durationMs", "duration", "interval"} {
		switch v := row[k].(type) {
		case float64:
			sec := int(v)
			if sec > 10000 { // 明显是毫秒
				sec /= 1000
			}
			return sec
		case int:
			if v > 10000 {
				return v / 1000
			}
			return v
		}
	}
	return 0
}

// syncCover 下载封面并写入歌单。抓取外部 URL 走 SSRF 防护客户端。
func syncCover(m *fnos.Music, guid, title, coverURL string) error {
	u, err := security.ValidateURL(coverURL, security.DefaultOptions())
	if err != nil {
		return err
	}
	client := security.NewSafeClient(security.DefaultOptions(), 20*time.Second)
	resp, err := client.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载封面失败: HTTP %d", resp.StatusCode)
	}
	image, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	name := "cover.jpg"
	switch strings.ToLower(resp.Header.Get("Content-Type")) {
	case "image/png":
		name = "cover.png"
	case "image/webp":
		name = "cover.webp"
	}
	return m.SetPlaylistCover(guid, title, image, name)
}

// round2 保留两位小数，让相似度分数在 JSON 里更易读。
func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
