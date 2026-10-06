// Package nameparse 从音乐文件名里解析出 歌手 / 歌名 / 专辑 / 序号。
//
// ── 为什么需要它 ──
//
// 本项目原来只有两条路：`pkg/complete` 里 20 行的 `guessFromFilename`（只会在
// " - " 上切一刀），和 `pkg/ai.ParseName`（直接交给大模型）。
// 中间**缺了规则层** —— 结果是「要么用最土的规则，要么花 AI 的钱」。
//
// ── 设计要点（对照参考实现 music-tidy v1.14.0 的 _parse_name / _llm_should_rescue）──
//
//  1. **规则优先**：`01. 周杰伦 - 晴天.flac` 这种用正则就能拆，不该花 AI 的钱。
//
//  2. **主动标 `Suspect`** —— 这是本包最重要的设计。
//     「规则拆不出来」好办（字段为空，一看就知道）；危险的是
//     **「规则以为自己对、其实错了」**：把「好听的歌曲推荐」当成歌手，
//     字段非空、看着正常，却悄悄写进了标签。
//     所以规则层要能自己说「这次我不确定」。
//
//  3. **给候选，不给结论**：左右两种解释都留着（`Candidates`），
//     需要时（Suspect 或字段为空）再交给上层挑 —— 包括交给 AI 挑。
//     AI 只做「挑对/纠正」比让它凭空认准得多。
package nameparse

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Candidate 一种可能的「歌手 + 歌名」解释。
type Candidate struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
	// Source 这个候选是怎么来的（"split: - " / "swapped" / "whole"），便于排查
	Source string `json:"source"`
}

// Result 一次解析的结果。
type Result struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Track  string `json:"track"`
	// Suspect 规则自己认为这次**可能拆错了**（注意：不是「没拆出来」）
	Suspect bool `json:"suspect"`
	// Reason 为什么 suspect（进日志用，不直接给用户看）
	Reason string `json:"reason,omitempty"`
	// Candidates 候选解释，按可信度降序。第一个与 Title/Artist 一致。
	Candidates []Candidate `json:"candidates,omitempty"`
}

// NeedsAI 这份解析要不要请 AI 判一次。
//
// 判据与参考实现 `_llm_should_rescue` 一致：**字段为空，或规则自认可疑**。
// 前者是「没拆出来」，后者是「拆了但可能错」—— 两者都要兜。
func (r Result) NeedsAI() bool {
	return strings.TrimSpace(r.Title) == "" ||
		strings.TrimSpace(r.Artist) == "" ||
		r.Suspect
}

// ── 噪声词 ──

// noiseWords 出现在**歌手位置**上就说明拆错了：这些是歌单名 / 推荐语 / 音质标记，
// 不是人。参考实现里举的例子是「好听的歌曲推荐-下山 - 麦小兜」——
// 左边被切成了「好听的歌曲推荐」，非空但是错的。
var noiseWords = []string{
	"好听的歌曲", "歌曲推荐", "推荐", "热门", "经典老歌", "老歌", "抖音热歌", "抖音",
	"车载音乐", "车载", "合集", "歌单", "榜单", "无损", "试听", "网络歌曲", "精选",
	"铃声", "串烧", "慢摇", "dj", "hifi", "hi-res", "hires",
	"flac", "ape", "wav", "320k", "192k", "128k", "hq", "sq", "ogg",
}

// 开头的曲目序号："01." / "01 " / "01-" / "01_" / "1、"
//
// 允许空格作分隔（"01 晴天" 是常见写法）。风险是「7 天」这类歌名会被吃掉 "7"，
// 但那种情况下带不带序号都匹配不上平台曲库，代价相同 —— 不为它把规则复杂化。
var trackNoRe = regexp.MustCompile(`^\s*(\d{1,3})[\s.\-_、]+`)

// 成对括号（中英文 + 方括号 + 书名号）
var bracketRe = regexp.MustCompile(`[\(（\[【][^\)）\]】]*[\)）\]】]`)

// 分隔符，按优先级排列。两侧带空格的连字符最可靠。
var separators = []string{" - ", " – ", " — ", "_-_", " -", "- ", "–", "—", "_"}

// Parse 解析一个文件名（可带路径，内部只取 basename）。
//
// 永远不会失败：拆不出来时 Title 为整串、Artist 为空，上层用 NeedsAI 决定要不要兜。
func Parse(name string) Result {
	var r Result

	base := filepath.Base(strings.TrimSpace(name))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return r
	}

	// 1) 开头序号
	if m := trackNoRe.FindStringSubmatch(stem); m != nil {
		r.Track = strings.TrimLeft(m[1], "0")
		if r.Track == "" {
			r.Track = "0"
		}
		stem = strings.TrimSpace(stem[len(m[0]):])
	}

	// 2) 剥括号噪声。**剥在开头**的最可疑 —— 多半是歌单前缀（【无损】、【好听的歌曲推荐】）
	headBracket := strings.HasPrefix(stem, "【") || strings.HasPrefix(stem, "(") ||
		strings.HasPrefix(stem, "[") || strings.HasPrefix(stem, "（")
	cleaned := strings.TrimSpace(bracketRe.ReplaceAllString(stem, " "))
	if headBracket && cleaned != stem {
		r.Suspect = true
		r.Reason = "开头有方括号内容，可能是歌单前缀而非歌曲信息"
	}
	cleaned = strings.Join(strings.Fields(cleaned), " ")

	// 3) 按分隔符切
	left, right, sep := splitOnce(cleaned)

	switch {
	case left != "" && right != "":
		// 两种解释都留：左歌手右歌名（常见），以及反过来
		r.Candidates = append(r.Candidates,
			Candidate{Artist: left, Title: right, Source: "split:" + sep},
			Candidate{Artist: right, Title: left, Source: "swapped"},
		)
		r.Artist, r.Title = left, right

		// 歌手位置出现噪声词 → 一定是切错了
		if hit := matchNoise(left); hit != "" {
			r.Suspect = true
			r.Reason = "歌手位置出现噪声词「" + hit + "」，多半是歌单名/音质标记"
			// 左半是歌单名 → **丢掉它**，拿右半再切一刀。
			// 比「左右互换」好：换过来只会让歌名变成「车载音乐」这种更离谱的结果。
			if l2, r2, sep2 := splitOnce(right); l2 != "" && r2 != "" {
				r.Artist, r.Title = l2, r2
				r.Candidates = append([]Candidate{
					{Artist: l2, Title: r2, Source: "drop-prefix:" + sep2},
				}, r.Candidates...)
			} else {
				r.Artist, r.Title = right, left
				r.Candidates[0], r.Candidates[1] = r.Candidates[1], r.Candidates[0]
			}
		} else if directionAmbiguous(left, right) {
			// 一侧中文一侧英文 → 说不准哪边是歌手
			r.Suspect = true
			r.Reason = "中英文混排，无法确定哪一侧是歌手"
		}

	case left != "" || right != "":
		// 只切出一侧（多半是首尾分隔符造成的）
		only := left + right
		r.Title = only
		r.Candidates = append(r.Candidates, Candidate{Title: only, Source: "whole"})

	default:
		r.Title = cleaned
		r.Candidates = append(r.Candidates, Candidate{Title: cleaned, Source: "whole"})
		// 没有分隔符时，噪声词是比长度更准的信号 ——
		// 「好听的歌曲推荐合集一百首经典老歌」里能命中好几个，
		// 而一首正常的超长歌名不该因此被怀疑。
		if hit := matchNoise(cleaned); hit != "" {
			r.Suspect = true
			r.Reason = "整串命中噪声词「" + hit + "」，可能是「歌单名 + 歌名」粘在一起"
		} else if len([]rune(cleaned)) > 30 {
			r.Suspect = true
			r.Reason = "没有分隔符且名字很长，可能是「歌单名 + 歌名」粘在一起"
		}
	}

	// 4) 三段以上（a - b - c）拆不明白，标可疑
	if n := strings.Count(cleaned, " - ") + 1; n >= 3 {
		r.Suspect = true
		if r.Reason == "" {
			r.Reason = "超过两段，无法确定哪段是歌手"
		}
	}

	return r
}

// splitOnce 按优先级找第一个分隔符，切一刀。
func splitOnce(s string) (left, right, sep string) {
	best := -1
	for _, d := range separators {
		i := strings.Index(s, d)
		if i <= 0 {
			continue
		}
		if best < 0 || i < best {
			best = i
			sep = strings.TrimSpace(d)
		}
	}
	if best < 0 {
		return "", "", ""
	}
	// 用实际命中的分隔符长度切（分隔符可能带空格）
	d := findActualSep(s, best)
	if d == "" {
		return "", "", ""
	}
	return strings.TrimSpace(s[:best]), strings.TrimSpace(s[best+len(d):]), sep
}

// findActualSep 在 pos 处找出真正匹配的分隔符原文（用于正确计算右半段起点）。
func findActualSep(s string, pos int) string {
	for _, d := range separators {
		if strings.HasPrefix(s[pos:], d) {
			return d
		}
	}
	return ""
}

// matchNoise 返回命中的噪声词（没有则空串）
func matchNoise(s string) string {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return ""
	}
	for _, w := range noiseWords {
		if strings.Contains(low, w) {
			return w
		}
	}
	return ""
}

// directionAmbiguous 判断「歌手在哪一侧」是否真的说不准。
//
// 中文曲库里 `中文 - 中文` 几乎一定是「歌手 - 歌名」（"周杰伦 - 晴天"），
// `英文 - 英文` 也一样（"The Beatles - Yesterday"），按惯例取左侧即可，不必怀疑。
//
// 真正会搞反的是**一侧中文、一侧英文**的混排命名 ——
// "周杰伦 - JayChou" 和 "JayChou - 周杰伦" 从字面完全分不出方向。
//
// 判据故意保守：**宁可漏判也不误判**。标 suspect 的代价是每首歌多一次 AI 调用，
// 大面积误判会把成本从「处理少数怪名字」变成「处理整个曲库」。
func directionAmbiguous(left, right string) bool {
	return isASCIIOnly(left) != isASCIIOnly(right)
}

func isASCIIOnly(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, c := range s {
		if c > 127 {
			return false
		}
	}
	return true
}
