package api

// 「补全整理」页头的常驻修复进度（v2.37，用户 2026-11-25 要求）。
//
// # 用户要的是什么
//
// 原话：「在曲库管理中新增一项功能：显示某类修复项的完成进度，格式为「已修复 xx /
// 总歌曲数」，便于直观查看还有多少歌曲未完成修复。例如：低音质替换的完成数量、
// 元数据的缺失数据数量。找个地方长期显示」
//
// # 口径（和用户确认过）
//
// 「已修复 = 总歌曲数 − 当前仍待修的条数」—— 本来就正常的歌也算已完成。
// 落在代码里就是 done = covered - pending。
//
// # 为什么分母用「已评估」而不是「全库总数」
//
// 音质扫描一次只探 200 首（每首要问 2–3 条渠道，全库扫一遍是十几分钟），
// 若按全库数算分母，界面会显示「已修复 1189 / 1234」—— 其中一千多首**从没被检查过**，
// 只是被算成了「已经没问题」。数字好看，但它是假的，而用户要看这个数字做判断。
// 所以每一类都给出 covered（真正评估过的歌），全库总数单独放在顶层给界面显示。
//
// 三类数据来源与新鲜度都不同，接口一并给出 at（该结论的时间）：
//   - 元数据：library_index.json（跑过「扫描并补全标签」才准）
//   - 音频坏文件：repair_scan.json（跑过修复清单扫描才准，见 intercept/repair_store.go）
//   - 低音质替换：quality_scan.json（跑过音质扫描才准，结论 7 天过期）
// 「没扫过」就是 known=false，界面显示「待扫描」——不要把 0 伪装成一个成绩。

import (
	"fmt"
	"net/http"
	"strings"

	"fn-lx-player/pkg/library"
)

// TidyProgressSource 是两类「需要现场扫描才有结论」的统计来源。
//
// 只有拦截层知道这两件事（它的解析池、它的落盘缓存），API 层只负责把它们
// 和索引口径拼成一条给人看的进度。用接口而不是两个闭包：注入点少一个，
// 以后再加一类（比如「重复文件」）也不用改 Set 的签名。
type TidyProgressSource interface {
	AudioRepairFacts(dir string) (scanned, pending int, at int64, known bool)
	AudioRepairBreakdown(dir string) (scanned int, byKind map[string]int, at int64, known bool)
	QualityFacts(dir string) (covered, pending int, at int64, known bool)
}

// SetTidyProgress 注入进度来源。由 main 提供（这里不 import main）。
func (s *Server) SetTidyProgress(src TidyProgressSource) {
	s.tidyProgress = src
}

// tidyProgressItem 是一类修复项的进度。
//
// Pending 为待修条数、Covered 为该类已评估的歌曲数、Done = Covered - Pending。
// 三个数都给出去（而不是只给一个百分比）：AI 调用方要能回答
// 「还有多少首没修」「这个数字覆盖了多少首歌」，百分比回答不了这两个问题。
type tidyProgressItem struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Pending int    `json:"pending"`
	Done    int    `json:"done"`
	Covered int    `json:"covered"`
	Known   bool   `json:"known"`
	At      int64  `json:"at,omitempty"`
	Note    string `json:"note,omitempty"`
	// Sub 是「按类别拆开」的明细：元数据给各字段缺多少、坏文件给各问题类型多少条、
	// 音质给已扫描 / 待替换 / 已修复。界面把它渲染成一排小卡片。
	Sub []tidyProgressSub `json:"sub,omitempty"`
}

// tidyProgressSub 是一张明细小卡片：类别名 + 数字。
type tidyProgressSub struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value int    `json:"value"`
}

// tidyProgressItemOf 组一条进度，并把「待修比评估过的还多」这种不可能的情况压回 0。
//
// 单独抽出来是为了能单测：这类钳位是最容易在改口径时被悄悄写错的地方。
func tidyProgressItemOf(id, label string, covered, pending int, at int64, known bool, note string) tidyProgressItem {
	if covered < 0 {
		covered = 0
	}
	if pending < 0 {
		pending = 0
	}
	done := covered - pending
	if done < 0 {
		done = 0
	}
	return tidyProgressItem{
		ID: id, Label: label, Pending: pending, Done: done,
		Covered: covered, Known: known, At: at, Note: note,
	}
}

// topGapFields 把「缺得最多的几个字段」拼成一句人话，例如「光盘 129 首、风格 128 首」。
//
// 只用于 note/tooltip：不参与任何判定，字段顺序按缺口从大到小，
// 并列时按固定顺序（歌词 → 封面 → 年份 → 曲序 → 光盘 → 风格）保证输出稳定可测。
func topGapFields(gaps library.GapCounts, n int) string {
	if n <= 0 {
		return ""
	}
	all := []struct {
		label string
		count int
	}{
		{"歌词", gaps.Lyric},
		{"封面", gaps.Cover},
		{"年份", gaps.Year},
		{"曲序", gaps.Track},
		{"光盘", gaps.Disc},
		{"风格", gaps.Genre},
	}
	// 稳定排序：先按缺口降序，同缺口保留上面的固定次序。
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].count > all[j-1].count; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	parts := make([]string, 0, n)
	for _, f := range all {
		if len(parts) >= n {
			break
		}
		if f.count <= 0 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d 首", f.label, f.count))
	}
	return strings.Join(parts, "、")
}

// HandleTidyProgress 返回各类修复项的完成进度。
// GET /api/library/repair/progress?dir=/vol1/Music
//
// data：`{dir,total,items:[{id,label,pending,done,covered,known,at,note}]}`
//   - total 是曲库总歌曲数（索引口径；索引还没建时退回音频扫描走过的文件数）。
//   - items 恒为三类、顺序固定：meta / audio / quality，界面可以直接照序渲染。
func (s *Server) HandleTidyProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 GET"})
		return
	}
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "还没有被授权访问任何目录，请先在「应用设置 → 授权目录」里授权一个音乐目录")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}

	idx := s.libraryIndex()
	stats := idx.StatsForDir(resolved)
	gaps := idx.FieldGaps(resolved)

	// 元数据：分母 = 能写标签的歌。wav/ape 那些写不进去的格式既不该算「待补」
	// 也不该算「已修好」，单列在 note 里说清楚（与 /api/library/gaps 同一口径）。
	writable := gaps.Total - gaps.Unsupported
	if writable < 0 {
		writable = 0
	}
	metaNote := ""
	if gaps.Unsupported > 0 {
		metaNote = fmt.Sprintf("另有 %d 首格式写不进标签，不算进分母", gaps.Unsupported)
	}
	if unread := writable - gaps.Read; unread > 0 {
		if metaNote != "" {
			metaNote += "；"
		}
		metaNote += fmt.Sprintf("其中 %d 首还没读过标签", unread)
	}
	// 元数据这一类经常显示「0 / 全部」——因为「缺任何一个字段」就算没修完，
	// 而风格、光盘、曲序这些字段绝大多数歌本来就没有。不解释一句，用户只会
	// 以为这个进度坏了。把缺得最多的两个字段写进 note，数字立刻变得可解释。
	if top := topGapFields(gaps, 2); top != "" {
		if metaNote != "" {
			metaNote += "；"
		}
		metaNote += "缺得最多的是 " + top
	}
	meta := tidyProgressItemOf("meta", "元数据", writable, gaps.AnyField, stats.ScannedAt, gaps.Total > 0, metaNote)
	// 补全页要按字段各给一张小卡片（用户 2026-11-25：「元数据需要显示多个卡片：
	// 总数，歌词，封面…」）。字段顺序与补全页的勾选条一致。
	meta.Sub = []tidyProgressSub{
		{"lyric", "歌词", gaps.Lyric},
		{"cover", "封面", gaps.Cover},
		{"year", "年份", gaps.Year},
		{"track", "曲序", gaps.Track},
		{"disc", "光盘", gaps.Disc},
		{"genre", "风格", gaps.Genre},
	}

	total := stats.Total

	audio := tidyProgressItemOf("audio", "音频坏文件", 0, 0, 0, false, "还没扫过修复清单")
	if s.tidyProgress != nil {
		scanned, pending, at, known := s.tidyProgress.AudioRepairFacts(resolved)
		audio = tidyProgressItemOf("audio", "音频坏文件", scanned, pending, at, known, "")
		if !known {
			audio.Note = "还没扫过修复清单"
		}
		if _, byKind, _, ok := s.tidyProgress.AudioRepairBreakdown(resolved); ok {
			audio.Sub = []tidyProgressSub{
				{"ext_mismatch", "格式不对", byKind["ext_mismatch"]},
				{"too_short", "时长过短", byKind["too_short"]},
				{"fake_lossless", "假无损", byKind["fake_lossless"]},
			}
		}
		// 索引还没建（第一次进应用）时，扫描走过的文件数就是仅有的总歌数答案。
		if total == 0 && known && scanned > 0 {
			total = scanned
		}
	}

	quality := tidyProgressItemOf("quality", "低音质替换", 0, 0, 0, false, "还没扫过音质")
	if s.tidyProgress != nil {
		covered, pending, at, known := s.tidyProgress.QualityFacts(resolved)
		quality = tidyProgressItemOf("quality", "低音质替换", covered, pending, at, known, "")
		if !known {
			quality.Note = "还没扫过音质"
		}
		quality.Sub = []tidyProgressSub{
			{"scanned", "已扫描", quality.Covered},
			{"pending", "待替换", quality.Pending},
			{"done", "已修复", quality.Done},
		}
	}

	items := []tidyProgressItem{meta, audio, quality}

	writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"dir":   resolved,
			"total": total,
			"items": items,
		},
	})
}