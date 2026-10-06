// Package match 提供歌曲版本识别、过滤与匹配打分能力。
//
// 思路移植自 music-monitor 的 pipeline.py（版本标记过滤 + 正式版优先）
// 与 quality.py（候选择优），并做了两处改进：
//   - 补上时长校验（music-monitor 有，music-tidy 完全没有）；
//   - 关键词支持中英双语与词边界，减少「正式版被误杀」。
package match

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// MatchMinScore 匹配接受阈值（0-1）。
// 低于该分数视为「不是同一首歌」，宁缺毋滥，避免张冠李戴。
const MatchMinScore = 0.72

// StrongMatchScore 强匹配阈值，可直接采用无需人工确认
const StrongMatchScore = 0.85

// markBoundaryL / markBoundaryR 版本标记的「词边界」：标记两侧不得与 ASCII 字母紧邻。
//
// 用「非 ASCII 字母」而不是 `\b`，因为标记词表里混着中英文：
//   - `Alive`（live 前是 A）、`Demons` / `Demolition`（demo 后是 n）不算标记；
//   - 而 `晴天Live`（前是汉字）、`光年之外 DJ版`（后是「版」）仍算标记。
//
// 裸子串匹配的老毛病一并修掉：`Ghost`（含 ost）、`Tourist`（含 tour）、
// `Sampler`（含 sample）以前都会被判成改编版。
//
// ⚠️ 边界只挡 ASCII 字母，不挡数字 —— `8D`、`Demo2011` 这类仍按标记处理。
const (
	markBoundaryL = `(?:^|[^A-Za-z])`
	markBoundaryR = `(?:$|[^A-Za-z])`
)

// variantPattern 版本标记：命中即视为「非正式版」
//
// 注意：这些词只在**歌名或专辑**里匹配，不在歌手里匹配——
// 否则歌手名里含 "DJ" 的正常歌曲会被误杀。
//
// `remastered` 必须排在 `remaster` 前面：只写 `remaster` 时，"Remastered 2011"
// 的 'e' 是 ASCII 字母，会被后缀边界否掉，反而漏判。
var variantPattern = regexp.MustCompile(`(?i)` + markBoundaryL + `(?:` + strings.Join([]string{
	// 现场类
	`live`, `现场`, `演唱會`, `演唱会`, `跨年`, `音乐会`, `音樂會`, `演出`, `tour`,
	// 版本类
	`acoustic`, `unplugged`, `demo`, `试听`, `試聽`, `片段`, `snippet`, `preview`,
	`remix`, `混音`, `dj`, `加长版`, `加長版`, `extended`, `伴奏`, `instrumental`,
	`karaoke`, `off\s*vocal`, `纯音乐`, `純音樂`,
	// 广播/影视类
	`广播剧`, `廣播劇`, `radio\s*edit`, `ost`, `原声版`, `原聲版`,
	// 变速类
	`sped\s*up`, `slowed`, `加速版`, `减速版`, `變速`, `变速`, `升调`, `降调`,
	// 翻唱/其他
	`cover`, `翻唱`, `重制`, `remastered`, `remaster`, `重混`, `rework`, `bootleg`, `mashup`,
	`8d`, `环绕`, `環繞`, `铃声`, `鈴聲`, `ringtone`,
}, "|") + `)` + markBoundaryR)

// previewPattern 明确的试听片段标记（同样带词边界，避免 Sampler / Demon 里的
// sample / demo 被当试听片段）
var previewPattern = regexp.MustCompile(`(?i)` + markBoundaryL + `(?:demo|试听|試聽|片段|snippet|preview|sample)` + markBoundaryR)

// PreviewDurationSec 「疑似试听片段」的时长上限（秒）
const PreviewDurationSec = 75

// DurationTolerance 与原曲时长允许的最大偏差：
// max(DurationToleranceAbs, 原曲时长 * DurationToleranceRatio)
const (
	DurationToleranceAbs   = 10
	DurationToleranceRatio = 0.15
)

// Catalog 参与匹配与过滤的歌曲信息
type Catalog struct {
	ID       string `json:"id,omitempty"`
	Source   string `json:"source,omitempty"`
	Name     string `json:"name"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Duration int    `json:"duration,omitempty"` // 秒
	Cover    string `json:"cover,omitempty"`
}

// VariantKind 版本类型
type VariantKind string

const (
	KindFormal  VariantKind = "formal"  // 正式版
	KindVariant VariantKind = "variant" // 改编版（Live/Remix/DJ 等）
)

// IsPreview 判断是否为试听片段（明确标记，或时长过短）
func IsPreview(c Catalog) bool {
	if previewPattern.MatchString(c.Name) || previewPattern.MatchString(c.Album) {
		return true
	}
	if c.Duration > 0 && c.Duration <= PreviewDurationSec {
		return true
	}
	return false
}

// IsVariant 判断是否为改编版（只看歌名与专辑，不看歌手）
func IsVariant(c Catalog) bool {
	return variantPattern.MatchString(c.Name) || variantPattern.MatchString(c.Album)
}

// bracketedVariantRe 抓出标题里每一段括号内容（中英文圆括号、方括号、书名号）
var bracketedVariantRe = regexp.MustCompile(`[\(\[（【]([^\)\]）】]*)[\)\]）】]`)

// BracketedVariant 判断**括号内**是否带版本标记（`晴天 (Live)`、`夜曲（现场版）`）。
//
// 与 IsVariant 的区别：只在括号内容里找。平台标题的版本后缀恰好都写在括号里，
// 所以「判断某条平台候选是不是改编版」用这个函数更准 —— 它不会被标题正文里的
// 正常单词干扰（`Alive` 这种歌名在 IsVariant 那边也已由词边界挡住，见 markBoundaryL）。
//
// IsVariant 的行为**故意保持宽严不变** —— monitor / push 那边依赖它，
// 只收紧了裸子串误判（Alive / Demons / Ghost 这类），没有改变真标记的判定。
func BracketedVariant(name string) bool {
	for _, m := range bracketedVariantRe.FindAllStringSubmatch(name, -1) {
		if variantPattern.MatchString(m[1]) {
			return true
		}
	}
	return false
}

// Kind 返回版本类型
func Kind(c Catalog) VariantKind {
	if IsVariant(c) {
		return KindVariant
	}
	return KindFormal
}

// RejectReason 判断候选是否应被拒绝，返回原因（空串表示可接受）。
//
// allowVariant 为 false 时，改编版一律拒绝；为 true 时仅拒绝试听片段，
// 改编版留给调用方作为「正式版不可用时的兜底」。
func RejectReason(c Catalog, referenceDuration int, allowVariant bool) string {
	if IsPreview(c) {
		return "疑似试听片段"
	}
	if IsVariant(c) && !allowVariant {
		return "改编版本仅允许作为正式版不可用时的兜底"
	}

	if c.Duration > 0 && c.Duration <= PreviewDurationSec {
		return "时长仅 " + itoa(c.Duration) + " 秒，疑似试听片段"
	}
	if c.Duration > 0 && referenceDuration > 0 {
		tol := DurationToleranceAbs
		if r := int(math.Round(float64(referenceDuration) * DurationToleranceRatio)); r > tol {
			tol = r
		}
		if diff := abs(c.Duration - referenceDuration); diff > tol {
			return "时长 " + itoa(c.Duration) + " 秒与原曲 " + itoa(referenceDuration) + " 秒不匹配"
		}
	}
	return ""
}

// ── 归一化与相似度 ──

var (
	bracketPattern = regexp.MustCompile(`[\(\[（【].*?[\)\]）】]`)
	noiseWordRe    = regexp.MustCompile(`(?i)\b(?:feat|ft|remaster(?:ed)?|version|live|explicit|hq|hires|flac|mp3|320k|128k)\b`)
	artistSplitRe  = regexp.MustCompile(`[&＆,，/、;；]|\s+vs\.?\s+|\s+feat\.?\s+|\s+ft\.?\s+`)
)

// NormTitle 歌名归一化：去括号注释 → 去噪音词 → 只保留字母数字与中文
func NormTitle(s string) string {
	s = strings.ToLower(s)
	s = bracketPattern.ReplaceAllString(s, " ")
	s = noiseWordRe.ReplaceAllString(s, " ")
	return keepAlnumCJK(s)
}

// NormArtist 歌手归一化
func NormArtist(s string) string {
	return keepAlnumCJK(strings.ToLower(s))
}

// PrimaryArtist 取主要歌手（多歌手时取第一个）
func PrimaryArtist(s string) string {
	parts := artistSplitRe.Split(s, -1)
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

func keepAlnumCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一汉字
			b.WriteRune(r)
		case r >= 0x3040 && r <= 0x30FF: // 日文假名
			b.WriteRune(r)
		case r >= 0xAC00 && r <= 0xD7AF: // 韩文
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Similarity 计算两首歌的相似度：歌名 70% + 歌手 30%。
//
// 歌手只要有一方包含另一方（"周杰伦" vs "周杰伦/杨瑞代"）即给高分，
// 这是中文多歌手标注的常见形态。
func Similarity(a, b Catalog) float64 {
	title := ratio(NormTitle(a.Name), NormTitle(b.Name))

	a1, a2 := NormArtist(a.Artist), NormArtist(b.Artist)
	var artist float64
	switch {
	case a1 == "" || a2 == "":
		artist = 0.5
	default:
		artist = ratio(a1, a2)
		if strings.Contains(a1, a2) || strings.Contains(a2, a1) {
			if artist < 0.9 {
				artist = 0.9
			}
		}
	}
	return round4(title*0.7 + artist*0.3)
}

// ratio 基于最长公共子序列的相似度（等价于 difflib.SequenceMatcher.ratio 的常用近似）
func ratio(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	lcs := lcsLen(ra, rb)
	return 2 * float64(lcs) / float64(len(ra)+len(rb))
}

func lcsLen(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// 滚动数组，避免 O(n*m) 空间
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	return prev[len(b)]
}

// Candidate 一个待评估的候选（含来源信息）
type Candidate struct {
	Catalog
	IsPrimary bool    `json:"is_primary"` // 是否来自原始平台
	Score     float64 `json:"score"`      // 与目标歌曲的相似度
}

// RankCandidates 按「原平台优先 → 相似度 → 平台优先级」排序
func RankCandidates(cands []Candidate, sourceRank func(string) int) []Candidate {
	out := append([]Candidate(nil), cands...)
	sortSlice(out, func(a, b Candidate) bool {
		if a.IsPrimary != b.IsPrimary {
			return a.IsPrimary
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return sourceRank(a.Source) < sourceRank(b.Source)
	})
	return out
}

// PickBest 从候选中择优：正式版永远优先，改编版仅在无正式版可用时兜底。
// satisfies 报告该候选是否达标（音质等由调用方判定）。
func PickBest(cands []Candidate, satisfies func(Candidate) bool) (Candidate, bool) {
	formal := make([]Candidate, 0, len(cands))
	variants := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if IsVariant(c.Catalog) {
			variants = append(variants, c)
		} else {
			formal = append(formal, c)
		}
	}

	pool := formal
	if len(pool) == 0 {
		pool = variants
	}
	if len(pool) == 0 {
		return Candidate{}, false
	}

	// 优先选择达标的候选；都不达标时退回全部候选交由上层按 fallback 策略处理
	satisfying := make([]Candidate, 0, len(pool))
	for _, c := range pool {
		if satisfies == nil || satisfies(c) {
			satisfying = append(satisfying, c)
		}
	}
	if len(satisfying) > 0 {
		pool = satisfying
	}

	best := pool[0]
	for _, c := range pool[1:] {
		if better(c, best) {
			best = c
		}
	}
	return best, true
}

func better(a, b Candidate) bool {
	if a.IsPrimary != b.IsPrimary {
		return a.IsPrimary
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return false
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sortSlice 简单的插入排序（候选数量通常个位数，避免引入 sort 依赖的复杂度）
func sortSlice[T any](s []T, less func(a, b T) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
