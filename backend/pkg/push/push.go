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
	"sort"
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
	LocalMatched int `json:"local_matched"`
	// LocalAdopted 本地命中的曲目里，经「触发曲库扫描 → 重新检索」已经真正
	// 加进飞牛歌单的数量（2026-10-24 收编逻辑）。
	LocalAdopted int `json:"local_adopted,omitempty"`
	// ScanTriggered 本轮是否成功触发了飞牛曲库重扫（为收编本地文件）。
	ScanTriggered bool `json:"scan_triggered,omitempty"`
	// ScanWarning 触发扫描 / 收编过程的警告（不影响已完成的推送结果）。
	ScanWarning    string       `json:"scan_warning,omitempty"`
	Added          int          `json:"added"`
	AlreadyPresent int          `json:"already_present"`
	Missing        []ItemResult `json:"missing"`
	Items          []ItemResult `json:"items"`
	CoverWarning   string       `json:"cover_warning,omitempty"`
	DryRun         bool         `json:"dry_run"`

	// Expired 本轮因超出保留期而从歌单移除的曲目数（见 retention.go）
	Expired int `json:"expired,omitempty"`
	// SkippedManual 本轮**故意没加回去**的曲目数：用户在歌单里手动删过它们。
	SkippedManual int `json:"skipped_manual,omitempty"`
	// ManualRemoved / ManualRemovedTitles 本轮新认出「用户手动删掉」的曲目数与歌名，
	// 给界面一句说明（「你删掉的这 3 首以后不会再自动加回来」）。
	ManualRemoved       int      `json:"manual_removed,omitempty"`
	ManualRemovedTitles []string `json:"manual_removed_titles,omitempty"`
	// Revived 用户删掉后又在网页端自己加回来的曲目数（本轮已清掉标记）。
	Revived int `json:"revived,omitempty"`
	// Pushed 清理后仍保留的「本任务推送过」清单，调用方需覆盖回任务持久化
	Pushed []PushedTrack `json:"pushed,omitempty"`
	// PruneWarning 保留期清理失败的原因（不影响本轮推送结果）
	PruneWarning string `json:"prune_warning,omitempty"`

	// ── 下面几个是给界面看的「清单快照」，也用于排查 ──

	// ManualRemovedGUIDs 当前仍处于「用户手动删掉、以后不再加回」状态的曲目 guid。
	// 界面拿它列出黑名单，也给「恢复这些曲目」的入口做依据。
	ManualRemovedGUIDs []string `json:"manual_removed_guids,omitempty"`
	// RevivedTitles 本轮因为用户自己又加回歌单而清掉删除标记的曲目名。
	RevivedTitles []string `json:"revived_titles,omitempty"`
	// PushedTotal 本轮结束时推送清单（Pushed）的总条数。
	PushedTotal int `json:"pushed_total,omitempty"`
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
	// localPending 记录「本地命中、待收编」的曲目在 res.Items 里的下标，
	// 供第 4.5 步扫描后收编进歌单。
	localPending := make([]int, 0)
	for _, o := range outcomes {
		res.Items = append(res.Items, o.item)
		if o.guid == "" {
			// 本地已有也算「这首歌不缺」—— 不进 Missing，否则前端会当成待下载再下一遍。
			if o.localPath != "" {
				res.LocalMatched++
				localPending = append(localPending, len(res.Items)-1)
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

	// 4. 写入歌单（跳过已存在的、跳过用户手动删掉的）
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

	// 4.1 用户手动删掉的曲目：认出来 → 记进清单 → 本轮及以后都不再加回。
	//
	// 用户 2026-10-24：「歌单里我手动删掉的歌，下次同步不要再加回来」。
	// 之前 toAdd = matched - current 的算法把「不在歌单里」一律当成「还没推」，
	// 于是用户每删一次、下一轮就补一次 —— 等于删不掉。
	//
	// 只认**我们推送过**、且过了宽限期（防止刚推上去还没读到的竞态）；
	// 用户后来又自己加回来的，清掉标记恢复成正常曲目。
	pushedBase := opts.Pushed
	if idx := PushedIndex(opts.Pushed); len(idx) > 0 {
		if revived := RevivedManuallyAdded(opts.Pushed, current); len(revived) > 0 {
			pushedBase = ClearRemovals(opts.Pushed, revived)
			res.Revived = len(revived)
			byGUID := make(map[string]string, len(opts.Pushed))
			for _, p := range opts.Pushed {
				byGUID[p.GUID] = p.Title
			}
			for g := range revived {
				if t := byGUID[g]; t != "" {
					res.RevivedTitles = append(res.RevivedTitles, t)
				}
			}
			sort.Strings(res.RevivedTitles)
		}
		if gone := DetectManualRemovals(pushedBase, current, time.Now()); len(gone) > 0 {
			pushedBase = MergePushed(pushedBase, gone)
			res.ManualRemoved = len(gone)
			for _, p := range gone {
				if p.Title != "" {
					res.ManualRemovedTitles = append(res.ManualRemovedTitles, p.Title)
				}
			}
		}
	}
	skip := ManualRemovals(pushedBase)
	for g := range skip {
		res.ManualRemovedGUIDs = append(res.ManualRemovedGUIDs, g)
	}
	sort.Strings(res.ManualRemovedGUIDs)
	res.PushedTotal = len(pushedBase)

	toAdd := make([]string, 0, len(matched))
	for _, g := range matched {
		if skip[g] {
			res.SkippedManual++
			continue
		}
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

	// 4.5 收编「本地已有」的曲目（2026-10-24 用户：「推送歌单的时候发现本地有
	// 歌曲是在该歌单里，就替换原有在线播放」）。
	//
	// 背景：matchLocal 命中的歌**只**被标记为 LocalMatched 然后跳过 —— 但本地文件
	// 此刻不在飞牛共享曲库里，歌单里就永远缺这首。正确做法是触发一次飞牛曲库
	// 扫描把本地文件收进去，然后在扫描结果里重新找它们、加进歌单。
	// 扫描是异步的，所以给一小段有界轮询；没等到的留待下一轮订阅任务收编
	//（runDue 会定期重跑，最终一致）。
	if len(localPending) > 0 {
		if err := m.ScanLibrary(); err != nil {
			// 扫描触发失败不废掉已完成的推送，只记警告。
			res.ScanWarning = "触发飞牛曲库扫描失败: " + err.Error()
		} else {
			res.ScanTriggered = true
			adoptLocalIntoPlaylist(m, guid, localPending, res, current)
		}
	}

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
	// 注意：第 4.5 步收编的曲目已经写进 res.Pushed 了，这里要在它的基础上
	// 并进任务历史与新增 —— 直接用 opts.Pushed 会把收编清单整个抹掉。
	// pushedBase 而不是 opts.Pushed：第 4.1 步刚认出的「用户手动删掉的」
	// 与「又加回来的」都在 pushedBase 里，用 opts.Pushed 会把它们冲掉。
	res.Pushed = MergePushed(append(append(pushedBase, res.Pushed...), added...), nil)

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
				// 歌手硬门槛：总分过了、但歌手差得远（翻唱 / 同名不同人）也不收。
				// 总分是「歌名 70% + 歌手 30%」，同名歌光靠歌名就能拿 0.7+，
				// 挡不住「另一首别人翻唱的歌被当作目标歌推上歌单」（2026-10-24 报障）。
				// 只在两边都标了歌手时才否决；缺一边时以总分为准。
				if artistKnown(source, c) && match.ArtistScore(source, c) < artistHardFloor {
					continue
				}
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

// artistHardFloor 歌手硬门槛：两边都标了歌手、而歌手相似度低于这个值的候选，
// 无论总分多高一律不收。0.5 意味着「完全不同的两个人」（0 分）会被拒，
// 「其中一个名字出现在另一方里」的常见多歌手写法（0.9 分）不受影响。
const artistHardFloor = 0.5

// artistKnown 判断两边是否都标了歌手（缺一边时歌手比较没有意义，不否决）。
func artistKnown(a, b match.Catalog) bool {
	return strings.TrimSpace(a.Artist) != "" && strings.TrimSpace(b.Artist) != ""
}

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
			// 与 matchInLibrary 同一把歌手硬门槛，理由见那边注释。
			if artistKnown(src, c) && match.ArtistScore(src, c) < artistHardFloor {
				continue
			}
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

// adoptScanRounds / adoptPollDelay 收编「本地命中」曲目时的轮询预算：
// 触发飞牛曲库扫描后，扫描是异步的，这里等一小会儿、再搜一遍，
// 每轮间隔 adoptPollDelay。全等不到就放弃 —— 下一轮订阅任务（runDue 定时
// 重跑）会在曲库已收录后自然把它们匹配进歌单，最终一致。
const (
	adoptScanRounds = 3
	// adoptPollDelay 每轮之间的等待。测试里会被改成 0（见 push_test.go）。
	// 用 var 而不是 const 就是为了这个。
)

var adoptPollDelay = 8 * time.Second

// adoptLocalIntoPlaylist 在触发飞牛曲库扫描后，把「本地已有、飞牛曲库还没收录」
// 的曲目收编进歌单：轮询检索这些曲目，等曲库把它们扫出来，再写入歌单。
//
// current 是歌单现有曲目 GUID 集（第 4 步已取过，复用免得再打一次接口）。
// idxs 是这些曲目在 res.Items 里的下标 —— 收编成功会把对应 ItemResult 改成
// Matched 并补上 FnosTrackGUID，界面上「已匹配」的语义因此保持一致：
// 只要歌真正进了歌单，就显示为已匹配（用户 2026-10-24：「飞牛同步历史显示的
// 已匹配，是指的匹配不同平台的、能够推送到飞牛歌曲吗」—— 是的，指的就是
// 这件事：能进飞牛歌单的才算已匹配）。
func adoptLocalIntoPlaylist(m *fnos.Music, guid string, idxs []int, res *Result, current map[string]bool) {
	pending := make([]ItemResult, 0, len(idxs))
	for _, i := range idxs {
		pending = append(pending, res.Items[i])
	}

	var adopted []string
	for round := 0; round < adoptScanRounds && len(pending) > 0; round++ {
		if round > 0 {
			time.Sleep(adoptPollDelay)
		}
		var still []ItemResult
		for _, it := range pending {
			src := match.Catalog{Name: it.Name, Artist: it.Artist}
			found, score, ok := matchInLibrary(m, src)
			if !ok {
				still = append(still, it)
				continue
			}
			if current[found.ID] {
				// 已在歌单里（可能是上一轮推送加过）：不重复加，但状态要改成已匹配。
				markAdopted(res, idxs, it, found, score)
				continue
			}
			adopted = append(adopted, found.ID)
			markAdopted(res, idxs, it, found, score)
		}
		pending = still
	}
	if len(adopted) == 0 {
		return
	}
	if err := m.AddTracks(guid, adopted); err != nil {
		// 加进去失败：不要谎报收编成功。把已标记的条目回滚成「本地已有」，
		// 等下一轮订阅任务重试。
		res.ScanWarning = "本地曲目收编进歌单失败: " + err.Error()
		for _, i := range idxs {
			if res.Items[i].FnosTrackGUID != "" && res.Items[i].Source == SourceFnos {
				// 收编加歌失败：状态回滚成「本地已有」（Matched 仍为 true ——
				// 本地命中态本来就是 true，界面看 Source 区分），等下一轮订阅任务重试。
				res.Items[i].FnosTrackGUID = ""
				res.Items[i].FnosTitle = ""
				res.Items[i].Matched = true
				res.Items[i].Source = SourceLocal
				res.Items[i].Reason = "飞牛曲库暂无，本地曲库已有（无需重新下载）"
			}
		}
		return
	}
	// 收编的也算「本任务推送过」，保留期清理才认得它们。
	now := time.Now().Unix()
	added := make([]PushedTrack, 0, len(adopted))
	for _, g := range adopted {
		added = append(added, PushedTrack{GUID: g, AddedAt: now})
	}
	res.Pushed = MergePushed(res.Pushed, added)
	res.LocalAdopted = len(adopted)
	res.Matched += len(adopted)
	res.Added += len(adopted)
}

// markAdopted 把 res.Items 里对应条目（由 idxs 定位）改成「飞牛曲库已命中」。
//
// 注意：本地命中态本来就是 Matched=true + Source=SourceLocal（界面靠 Source 区分
// 「进歌单了」和「本地有但还没进歌单」），所以定位条件不能写 !Matched，
// 要认 Source 还是 SourceLocal、且 GUID 尚未填。
func markAdopted(res *Result, idxs []int, it ItemResult, found match.Catalog, score float64) {
	for _, i := range idxs {
		if res.Items[i].Name == it.Name && res.Items[i].Artist == it.Artist &&
			res.Items[i].Source == SourceLocal && res.Items[i].FnosTrackGUID == "" {
			res.Items[i].Matched = true
			res.Items[i].Source = SourceFnos
			res.Items[i].Score = round2(score)
			res.Items[i].FnosTrackGUID = found.ID
			res.Items[i].FnosTitle = found.Name
			res.Items[i].Reason = "本地文件已由曲库扫描收编，进歌单"
			return
		}
	}
}
