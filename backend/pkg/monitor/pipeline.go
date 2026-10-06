package monitor

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"fn-lx-player/pkg/match"
)

// Song 一首待处理曲目（由外部发现逻辑填充）
type Song struct {
	Source   string `json:"source"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Cover    string `json:"cover"`
	URL      string `json:"url,omitempty"`    // 已解析的直链（可选）
	Origin   string `json:"origin,omitempty"` // 来源标签（榜单名/歌单名）
}

// ToCatalog 转换为匹配用的结构
func (s Song) ToCatalog() match.Catalog {
	return match.Catalog{
		ID:       s.ID,
		Source:   s.Source,
		Name:     s.Name,
		Artist:   s.Artist,
		Album:    s.Album,
		Duration: s.Duration,
		Cover:    s.Cover,
	}
}

// FilterVerdict 过滤结论
type FilterVerdict struct {
	Keep   bool   `json:"keep"`
	Reason string `json:"reason,omitempty"`
}

var keywordSplitRe = regexp.MustCompile(`[,，;；\s]+`)

// SplitKeywords 切分关键词（与 music-monitor 的 `[,，;；\s]+` 一致）
func SplitKeywords(raw string) []string {
	out := make([]string, 0)
	for _, k := range keywordSplitRe.Split(strings.ToLower(raw), -1) {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// MatchKeywords 关键词过滤：include 需命中任一，exclude 命中任一即剔除。
// 匹配范围是 歌名 + 歌手 + 专辑 三字段拼接。
func MatchKeywords(s Song, include, exclude []string) FilterVerdict {
	haystack := strings.ToLower(s.Name + " " + s.Artist + " " + s.Album)

	if len(include) > 0 {
		hit := false
		for _, k := range include {
			if strings.Contains(haystack, k) {
				hit = true
				break
			}
		}
		if !hit {
			return FilterVerdict{Keep: false, Reason: "未命中包含关键词"}
		}
	}
	for _, k := range exclude {
		if strings.Contains(haystack, k) {
			return FilterVerdict{Keep: false, Reason: "命中排除关键词：" + k}
		}
	}
	return FilterVerdict{Keep: true}
}

// FilterSong 对单首歌做完整过滤判定。
//
// referenceDuration 为原曲时长（用于时长校验，0 表示未知、跳过该校验）。
// 返回的 Keep=false 时 Reason 可直接展示给用户与 AI。
func FilterSong(s Song, include, exclude []string, referenceDuration int, allowVariant bool) FilterVerdict {
	if strings.TrimSpace(s.Name) == "" {
		return FilterVerdict{Keep: false, Reason: "歌名为空"}
	}
	if strings.TrimSpace(s.ID) == "" {
		return FilterVerdict{Keep: false, Reason: "缺少平台曲目 ID"}
	}

	if v := MatchKeywords(s, include, exclude); !v.Keep {
		return v
	}

	if reason := match.RejectReason(s.ToCatalog(), referenceDuration, allowVariant); reason != "" {
		return FilterVerdict{Keep: false, Reason: reason}
	}
	return FilterVerdict{Keep: true}
}

// DedupeSongs 按 (source, id) 去重，保留首次出现
func DedupeSongs(songs []Song) []Song {
	seen := make(map[string]bool, len(songs))
	out := make([]Song, 0, len(songs))
	for _, s := range songs {
		if s.Source == "" || s.ID == "" {
			continue
		}
		k := TrackKey(s.Source, s.ID)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// FileExistsChecker 判断已下载文件是否仍在磁盘上。
//
// 这是防「监控僵死」的关键：若只看数据库记录就跳过，用户手动删掉文件后
// 该曲目将永远不会被重新下载。与 music-monitor 的 _file_exists 口径一致。
type FileExistsChecker func(path string) bool

// DefaultFileExists 默认的磁盘存在性检查
func DefaultFileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// PlanItem 计划中的一项
type PlanItem struct {
	Song   Song   `json:"song"`
	Action string `json:"action"` // download | resolve | register | skip
	Reason string `json:"reason,omitempty"`
}

// Plan 一轮监控的执行计划
type Plan struct {
	Baseline   bool       `json:"baseline"` // 是否为基线轮
	Items      []PlanItem `json:"items"`
	Found      int        `json:"found"`
	ToDownload int        `json:"to_download"` // 含 ToResolve（取链成功后同样会下载）
	ToResolve  int        `json:"to_resolve"`  // 其中需要先向后端音源服务取链的
	Registered int        `json:"registered"`
	Skipped    int        `json:"skipped"`
}

// BuildPlan 依据监控配置与已登记曲目，计算本轮要做的事。
//
// canResolve 表示外部注入的通道是否具备「后端取链」能力（见 URLResolver）：
// 为 true 时，无直链的新曲目产出 action "resolve"（**与 download 共用单轮预算**），
// 由 RunOnce 调音源服务解析出地址后落盘；为 false 时保持旧行为（登记 pending，
// 等网页端「补下载」）。
//
// 与 music-monitor 的差异（有意为之）：
//   - **首轮基线**：BaselineDone 为 false 时，本轮只登记曲目不下载，
//     并把 BaselineDone 置为 true，避免首次监控几百首歌单时瞬间灌满下载队列。
//     基线轮**绝不取链**：否则几百首会一次性打爆音源服务。
//   - 已登记且文件仍在 → 跳过；文件已丢失 → 重新下载。
func BuildPlan(mon *Monitor, songs []Song, existing map[string]bool, exists FileExistsChecker, getTrack func(source, songID string) (*Track, bool), canResolve bool) Plan {
	if exists == nil {
		exists = DefaultFileExists
	}
	include := SplitKeywords(mon.IncludeKW)
	exclude := SplitKeywords(mon.ExcludeKW)

	plan := Plan{Baseline: !mon.BaselineDone}
	songs = DedupeSongs(songs)
	plan.Found = len(songs)

	budget := mon.MaxDownloads
	if budget <= 0 {
		budget = DefaultMaxDownloads
	}
	if budget > MaxDownloadsPerRun {
		budget = MaxDownloadsPerRun
	}

	for _, s := range songs {
		key := TrackKey(s.Source, s.ID)

		// 已登记且文件确实还在（或经网页端补下载确认、无路径可查）→ 跳过（不占用预算）
		if existing[key] {
			if tr, ok := getTrack(s.Source, s.ID); ok &&
				tr.Status == StatusDownloaded &&
				(strings.TrimSpace(tr.FilePath) == "" || exists(tr.FilePath)) {
				plan.Items = append(plan.Items, PlanItem{Song: s, Action: "skip", Reason: "已下载且文件存在"})
				plan.Skipped++
				continue
			}
		}

		// 基线轮：只登记，不下载
		if plan.Baseline {
			plan.Items = append(plan.Items, PlanItem{Song: s, Action: "register", Reason: "首次检查，只登记不下载"})
			plan.Registered++
			continue
		}

		verdict := FilterSong(s, include, exclude, 0, false)
		if !verdict.Keep {
			plan.Items = append(plan.Items, PlanItem{Song: s, Action: "skip", Reason: verdict.Reason})
			plan.Skipped++
			continue
		}

		if !mon.AutoDownload {
			plan.Items = append(plan.Items, PlanItem{Song: s, Action: "register", Reason: "未开启自动下载，仅登记"})
			plan.Registered++
			continue
		}

		// 后端自抓的曲目没有直链（音源脚本只在浏览器执行）。
		// ① 后端具备取链能力（已配置服务型音源）→ 产出 resolve，与 download 共用单轮预算；
		// ② 否则登记成待补下载，由网页端「补下载」搜索音源入队，不占用下载预算。
		if strings.TrimSpace(s.URL) == "" {
			if !canResolve {
				plan.Items = append(plan.Items, PlanItem{Song: s, Action: "register", Reason: "等页面打开时一键补下载（后端自抓无音源直链）"})
				plan.Registered++
				continue
			}
			if budget <= 0 {
				plan.Items = append(plan.Items, PlanItem{Song: s, Action: "skip", Reason: "已达单轮下载上限"})
				plan.Skipped++
				continue
			}
			budget--
			plan.Items = append(plan.Items, PlanItem{Song: s, Action: "resolve"})
			plan.ToDownload++
			plan.ToResolve++
			continue
		}

		if budget <= 0 {
			plan.Items = append(plan.Items, PlanItem{Song: s, Action: "skip", Reason: "已达单轮下载上限"})
			plan.Skipped++
			continue
		}

		budget--
		plan.Items = append(plan.Items, PlanItem{Song: s, Action: "download"})
		plan.ToDownload++
	}

	return plan
}

// PreviewResult 干跑结果（用于新建监控前验证配置）
type PreviewResult struct {
	Total    int        `json:"total"`
	Filtered int        `json:"filtered"`
	Baseline bool       `json:"baseline"`
	Warnings []string   `json:"warnings,omitempty"`
	Songs    []Song     `json:"songs"`
	Rejected []Rejected `json:"rejected,omitempty"`
}

// Rejected 被过滤掉的曲目及原因
type Rejected struct {
	Song   Song   `json:"song"`
	Reason string `json:"reason"`
}

// Preview 干跑：只做发现 + 过滤，不下载、不写库。
//
// 对应 music-monitor 的 /api/preview，是验证歌单链接与过滤规则的关键工具，
// 对 AI 调用场景尤其有用（可先 dry-run 再决定是否真的执行）。
func Preview(mon *Monitor, songs []Song) PreviewResult {
	include := SplitKeywords(mon.IncludeKW)
	exclude := SplitKeywords(mon.ExcludeKW)

	res := PreviewResult{
		Baseline: !mon.BaselineDone,
		Songs:    make([]Song, 0),
		Rejected: make([]Rejected, 0),
	}

	songs = DedupeSongs(songs)
	res.Total = len(songs)

	for _, s := range songs {
		v := FilterSong(s, include, exclude, 0, false)
		if v.Keep {
			res.Songs = append(res.Songs, s)
		} else {
			res.Rejected = append(res.Rejected, Rejected{Song: s, Reason: v.Reason})
		}
	}
	res.Filtered = len(res.Songs)

	if res.Baseline {
		// ⚠️ 这条警告**直接进界面**（干跑结果里返回）→ 必须说人话。
		// 不要写「基线」这类内部术语（2026-09-21 用户反馈：「待建基线什么意思」）。
		res.Warnings = append(res.Warnings,
			"这个订阅还没开始追新：第一次执行只会记下现有曲目，不会下载")
	}
	if len(songs) == 0 {
		res.Warnings = append(res.Warnings, "没有发现任何曲目（歌单链接可能已失效或需要登录）")
	}
	return res
}

// SummarizePlan 生成可读的计划摘要
func SummarizePlan(p Plan) string {
	var b strings.Builder
	if p.Baseline {
		b.WriteString("【首次检查】")
	}
	fmt.Fprintf(&b, "发现 %d 首，计划下载 %d 首，登记 %d 首，跳过 %d 首",
		p.Found, p.ToDownload, p.Registered, p.Skipped)
	if p.ToResolve > 0 {
		fmt.Fprintf(&b, "（其中 %d 首需先向后端音源取链）", p.ToResolve)
	}
	return b.String()
}
