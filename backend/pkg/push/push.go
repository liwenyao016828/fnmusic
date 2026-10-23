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
	"path/filepath"
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
	// Source 这条是怎么判定的：fnos（飞牛曲库命中）/ local（本地文件命中）/
	// local_version_diff（本地有一版，但时长对不上，让用户选）。
	// 空 = 老调用方 / 未命中，按 fnos 理解。
	Source string `json:"source,omitempty"`
	// LocalPath 命中的本地文件绝对路径（Source 为 local* 时必有）。
	// 界面拿它显示「本地已有：xxx.flac」，也拿它做「用本地版本」的依据。
	LocalPath string `json:"local_path,omitempty"`
	// LocalDuration / SourceDuration 只有版本存疑时才填，给界面说明差异在哪。
	LocalDuration  int `json:"local_duration,omitempty"`
	SourceDuration int `json:"source_duration,omitempty"`
}

// Result 一次推送的结果。
type Result struct {
	GUID            string `json:"guid"`
	Title           string `json:"title"`
	CreatedPlaylist bool   `json:"created_playlist"`
	Total           int    `json:"total"`
	Matched         int    `json:"matched"`
	// LocalMatched 飞牛曲库没有、但**本地曲库已有**的曲目数。
	// 它们不算 Matched（没推上去，飞牛歌单里不会有），也不算 Missing（歌已经有了）。
	LocalMatched   int          `json:"local_matched"`
	Added          int          `json:"added"`
	AlreadyPresent int          `json:"already_present"`
	Missing        []ItemResult `json:"missing"`
	Items          []ItemResult `json:"items"`
	CoverWarning   string       `json:"cover_warning,omitempty"`
	DryRun         bool         `json:"dry_run"`

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

	// LocalIndex 本地曲库索引（可为 nil）。传了就先在**本地文件**里找一遍：
	// 找到说明这歌用户早就下好了，只是飞牛那边还没收录 —— 这种曲目不该报「缺」
	// 让前端去重下一遍，也不该在歌单里留个空位。
	//
	// 用户 2026-10-23 的原话：「同一个歌单里的歌曲，我提前下载了，然后同步该歌单。
	// 怎么没有匹配该歌曲？可否增加功能，对现有的本地曲库的歌曲索引。」
	LocalIndex func() []LocalTrack
}

// LocalTrack 是本地曲库里的一个文件（由调用方从 library.Index 转好再传进来，
// 免得 push 包为了一个字段去依赖 library 包）。
type LocalTrack struct {
	Path     string
	Name     string
	Artist   string
	Album    string
	Duration int // 秒；0 = 未知
	Format   string
}

// ItemResult.Source 的取值，说明这条曲目是**怎么**找到的。
const (
	// SourceFnos = 在飞牛服务端曲库里命中的（正常路径）
	SourceFnos = "fnos"
	// SourceLocal = 飞牛曲库没命中，但**本地文件**里有（用户早就下好了）
	SourceLocal = "local"
	// SourceLocalVersionDiff = 本地有，但版本看起来不一样（现场版/翻唱/时长差很多）。
	//
	// 这一类**不自动决断**：用户说的是「如果歌曲版本不同，则在任务状态里显示让用户
	// 抉择本地歌曲还是重新下载其他版本」。所以它照样进 Missing（让前端列为待处理），
	// 但 Reason 说清「本地有另一版」，界面据此给两个按钮。
	SourceLocalVersionDiff = "local_version_diff"
)

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
		// localPath 本地文件命中（飞牛曲库没有、但用户盘上有）。
		// 这种曲目**不进 Missing**：歌已经有了，不该再让前端下一次。
		localPath string
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
				src := match.Catalog{
					Name: t.Name, Artist: t.Artist, Album: t.Album, Duration: t.Duration,
				}
				found, score, ok := matchInLibrary(m, src)
				item := ItemResult{Name: t.Name, Artist: t.Artist, Score: round2(score)}
				if ok {
					item.Matched = true
					item.Source = SourceFnos
					item.FnosTrackGUID = found.ID
					item.FnosTitle = found.Name
					outcomes[i] = outcome{item: item, guid: found.ID}
					return
				}
				// 飞牛曲库没命中 → 再看**本地文件**（用户早就下好的那种）。
				// 不这样做，用户「先下载、再同步歌单」就会在歌单里看到这首歌缺失，
				// 然后被前端判成「待下载」又下一遍 —— 正是 2026-10-23 报的那件事。
				if loc, lscore, diff := matchLocal(opts.LocalIndex, src); loc != nil {
					item.Score = round2(lscore)
					item.LocalPath = loc.Path
					if diff != "" {
						// 版本存疑：**不**替用户决定。留在 Missing 里并说明原因，
						// 界面上给「用本地这一版 / 重新下载其他版本」两个选择。
						item.Source = SourceLocalVersionDiff
						item.Reason = diff
						item.LocalDuration = loc.Duration
						item.SourceDuration = t.Duration
						outcomes[i] = outcome{item: item}
						return
					}
					item.Matched = true
					item.Source = SourceLocal
					item.Reason = "飞牛曲库暂无，本地曲库已有（无需重新下载）"
					outcomes[i] = outcome{item: item, localPath: loc.Path}
					return
				}
				item.Reason = "飞牛曲库和本地曲库都没有这首歌"
				outcomes[i] = outcome{item: item}
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
			// 本地已有也算「这首歌不缺」—— 不进 Missing，否则前端会当成待下载再下一遍。
			if o.localPath != "" {
				res.LocalMatched++
				continue
			}
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

// localMatchMinScore 本地匹配的接受阈值，比飞牛那边（match.MatchMinScore=0.72）
// 更严一点。
//
// 理由：本地索引里的歌是**用户自己的文件**，标错一次就永远错（要他去删文件）；
// 而飞牛那边错了只是歌单里多一首可以手动去掉。宁可漏判、让用户手点「补下载」，
// 也不要把他收藏的另一首同名歌当成这一首。
const localMatchMinScore = 0.80

// localDurationTolerance 判定「同一版本」的时长容差（秒）。
// 现场版与录音室版通常差十几秒以上；同一版本因编码/静音裁切差 1-2 秒很常见。
const localDurationTolerance = 8

// matchLocal 在本地曲库索引里找这首歌（索引快照由调用方给，便于测试直接塞数据）。
//
// 返回 (命中项, 分数, 版本存疑原因)。第三个返回值非空表示**读到了本地某一版，
// 但它与要推的这首看起来不是同一个版本**（时长差太多）—— 这种情况由界面让用户选，
// 函数本身不替用户做决定。
func matchLocal(snapshot func() []LocalTrack, src match.Catalog) (*LocalTrack, float64, string) {
	if snapshot == nil || strings.TrimSpace(src.Name) == "" {
		return nil, 0, ""
	}
	list := snapshot()
	if len(list) == 0 {
		return nil, 0, ""
	}

	var (
		best      *LocalTrack
		bestScore float64
		bestCand  match.Catalog
	)
	for k := range list {
		c := match.Catalog{
			Name: list[k].Name, Artist: list[k].Artist,
			Album: list[k].Album, Duration: list[k].Duration,
		}
		if strings.TrimSpace(c.Name) == "" {
			// 没有标题，就用文件名（去扩展名）当歌名 —— 曲库里大量文件其实只靠文件名。
			c.Name = strings.TrimSuffix(filepath.Base(list[k].Path), filepath.Ext(list[k].Path))
		}
		score := match.Similarity(src, c)
		if score > bestScore {
			best, bestScore, bestCand = &list[k], score, c
		}
	}
	if best == nil || bestScore < localMatchMinScore {
		return nil, bestScore, ""
	}

	// 时长校验：两边都有时长且差太多 → 不是同一版本。
	// 只看**时长**这一个硬指标，不看歌名后缀：名字里带「现场版」也可能只是命名习惯。
	if src.Duration > 0 && bestCand.Duration > 0 {
		if d := src.Duration - bestCand.Duration; d > localDurationTolerance || d < -localDurationTolerance {
			return best, bestScore, fmt.Sprintf(
				"本地曲库里有一版，但时长差得多（本地 %d 秒 / 这首 %d 秒），可能是现场版或别的版本",
				bestCand.Duration, src.Duration)
		}
	}
	return best, bestScore, ""
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
