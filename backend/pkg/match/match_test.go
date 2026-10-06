package match

import "testing"

func TestIsVariantDetectsCommonVariants(t *testing.T) {
	variants := []string{
		"夜曲 (Live)", "夜曲 现场版", "晴天 演唱会", "富士山下 (Remix)",
		"光年之外 DJ版", "平凡之路 伴奏", "起风了 纯音乐", "小幸运 翻唱",
		"稻香 (Acoustic)", "告白气球 铃声", "演员 变速版", "后来 加长版",
		"岁月神偷 (Demo)", "夜空中最亮的星 跨年", "成都 音乐会",
		"体面 (Cover)", "认真的雪 8D环绕", "消愁 sped up",
		// 重制类：`remaster` 与 `remastered` 两种写法都要命中
		"告白气球 (Remastered 2011)", "晴天 重制版",
	}
	for _, name := range variants {
		if !IsVariant(Catalog{Name: name}) {
			t.Errorf("%q 应被识别为改编版", name)
		}
	}
}

func TestIsVariantDoesNotKillFormalSongs(t *testing.T) {
	formal := []string{
		"夜曲", "晴天", "富士山下", "光年之外", "平凡之路",
		"起风了", "小幸运", "稻香", "告白气球", "演员",
		"Oceans", "Believer", "Counting Stars", "The Nights",
		"Yellow", "Fix You", "Viva La Vida",
		// 含版本标记的**子串**、但标记不在词边界上（② 修掉的裸子串误判）
		"Alive", "Stayin' Alive", "Demons", "Demolition",
		"Ghost", "Tourist", "Sampler",
	}
	for _, name := range formal {
		if IsVariant(Catalog{Name: name}) {
			t.Errorf("%q 是正式版，不应被误判为改编版", name)
		}
	}
}

// TestIsVariantIgnoresArtistField 版本标记只看歌名/专辑，不看歌手。
// 否则艺名里含 DJ 的正常歌曲会被误伤。
func TestIsVariantIgnoresArtistField(t *testing.T) {
	c := Catalog{Name: "Faded", Artist: "DJ Snake"}
	if IsVariant(c) {
		t.Error("歌手名含 DJ 不应导致歌曲被判为改编版")
	}
}

// BracketedVariant 只看括号里的版本标记 —— 这是它比 IsVariant 更适合
// 「判断平台给的候选是不是改编版」的原因：平台标题的版本后缀都写在括号里，
// 而括号内容本身是独立片段，不受标题正文里的正常单词干扰。
func TestBracketedVariantOnlyLooksInsideBrackets(t *testing.T) {
	variants := []string{
		"夜曲 (Live)", "夜曲（现场版）", "晴天 [Remix]", "起风了【伴奏】",
		"消愁 (Acoustic)", "稻香 (Cover)", "演员 (Karaoke)",
	}
	for _, name := range variants {
		if !BracketedVariant(name) {
			t.Errorf("%q 括号内是版本标记，应判为改编版", name)
		}
	}

	formal := []string{
		// 括号外含标记子串，但括号里没有版本标记
		"Alive", "Stayin' Alive", "Demons", "Demolition",
		// 括号里不是版本标记
		"夜曲", "晴天", "晴天 (原唱 周杰伦)", "晴天（R&B版）", "稻香 (MV版)",
	}
	for _, name := range formal {
		if BracketedVariant(name) {
			t.Errorf("%q 不是改编版，不应被误判", name)
		}
	}
}

// TestVariantPatternRequiresWordBoundary variantPattern / previewPattern 带上词边界后，
// 标记只有「不与 ASCII 字母紧邻」时才算 —— 这是 ② 修掉裸子串误判的核心。
func TestVariantPatternRequiresWordBoundary(t *testing.T) {
	// 标记被嵌在单词中间：都不算标记
	embedded := map[string]string{
		"Alive":         "live 前是字母 A",
		"Stayin' Alive": "同上",
		"Demons":        "demo 后是字母 n",
		"Demolition":    "同上",
		"Ghost":         "ost 前是字母 G",
		"Tourist":       "tour 后是字母 i",
		"Sampler":       "sample 后是字母 r",
		"Nightlive":     "live 前是字母 t",
	}
	for name, why := range embedded {
		if IsVariant(Catalog{Name: name}) {
			t.Errorf("%q 被误判为改编版（%s）", name, why)
		}
		if IsPreview(Catalog{Name: name, Duration: 240}) {
			t.Errorf("%q 被误判为试听片段（%s）", name, why)
		}
	}

	// 标记紧邻**汉字**（无空格分隔）仍算标记：边界只挡 ASCII 字母
	tight := []string{"晴天Live", "晴天live版", "光年之外DJ版", "认真的雪8D环绕"}
	for _, name := range tight {
		if !IsVariant(Catalog{Name: name}) {
			t.Errorf("%q 的版本标记紧邻汉字，仍应判为改编版", name)
		}
	}
}

func TestIsPreview(t *testing.T) {
	cases := []struct {
		name string
		c    Catalog
		want bool
	}{
		{"明确试听标记", Catalog{Name: "夜曲 试听片段"}, true},
		{"snippet", Catalog{Name: "Hello (Snippet)"}, true},
		{"时长过短", Catalog{Name: "夜曲", Duration: 60}, true},
		{"边界 75 秒", Catalog{Name: "夜曲", Duration: 75}, true},
		{"正常时长", Catalog{Name: "夜曲", Duration: 240}, false},
		{"时长未知", Catalog{Name: "夜曲", Duration: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPreview(tc.c); got != tc.want {
				t.Errorf("IsPreview = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

func TestRejectReasonDurationMismatch(t *testing.T) {
	// 原曲 300 秒，容差 max(10, 300*0.15=45) = 45 秒
	ref := 300

	if got := RejectReason(Catalog{Name: "夜曲", Duration: 300}, ref, true); got != "" {
		t.Errorf("完全一致不应拒绝，得到 %q", got)
	}
	if got := RejectReason(Catalog{Name: "夜曲", Duration: 340}, ref, true); got != "" {
		t.Errorf("差值 40 秒在容差 45 秒内，不应拒绝，得到 %q", got)
	}
	if got := RejectReason(Catalog{Name: "夜曲", Duration: 400}, ref, true); got == "" {
		t.Error("差值 100 秒超过容差 45 秒，应拒绝")
	}
}

func TestRejectReasonDurationToleranceUsesLargerOfAbsAndRatio(t *testing.T) {
	// 短歌：原曲 40 秒 -> max(10, 6) = 10 秒容差
	if got := RejectReason(Catalog{Name: "x", Duration: 55}, 40, true); got == "" {
		t.Error("40 秒原曲、55 秒候选，超出 10 秒容差，应拒绝")
	}
	// 但 40 秒本身 <= 75，会先命中试听片段判定
	if got := RejectReason(Catalog{Name: "x", Duration: 48}, 40, true); got == "" {
		t.Error("候选时长 48 秒 <= 75，应被判定为疑似试听片段")
	}
}

func TestRejectReasonUnknownDurationIsNotRejected(t *testing.T) {
	// 时长未知（0）不构成拒绝理由，否则大量音源会被误杀
	if got := RejectReason(Catalog{Name: "夜曲", Duration: 0}, 300, true); got != "" {
		t.Errorf("候选时长未知时不应因时长被拒，得到 %q", got)
	}
	if got := RejectReason(Catalog{Name: "夜曲", Duration: 240}, 0, true); got != "" {
		t.Errorf("参考时长未知时不应因时长被拒，得到 %q", got)
	}
}

func TestRejectReasonVariantPolicy(t *testing.T) {
	live := Catalog{Name: "夜曲 (Live)", Duration: 300}

	if got := RejectReason(live, 300, false); got == "" {
		t.Error("allowVariant=false 时应拒绝改编版")
	}
	if got := RejectReason(live, 300, true); got != "" {
		t.Error("allowVariant=true 时应允许改编版作为兜底")
	}
	// 但试听片段无论如何都要拒绝
	preview := Catalog{Name: "夜曲 试听", Duration: 300}
	if got := RejectReason(preview, 300, true); got == "" {
		t.Error("试听片段即使在允许改编版时也应拒绝")
	}
}

// ── 归一化与相似度 ──

func TestNormTitle(t *testing.T) {
	cases := map[string]string{
		"夜曲":              "夜曲",
		"夜曲 (Live)":       "夜曲",
		"Hello [Remix]":   "hello",
		"Shape of You":    "shapeofyou",
		"起风了（正式版）":        "起风了",
		"Faded (feat. X)": "faded",
	}
	for in, want := range cases {
		if got := NormTitle(in); got != want {
			t.Errorf("NormTitle(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestPrimaryArtist(t *testing.T) {
	cases := map[string]string{
		"周杰伦":                    "周杰伦",
		"周杰伦/杨瑞代":                "周杰伦",
		"周杰伦 & 阿信":               "周杰伦",
		"Jay Chou feat. Someone": "Jay Chou",
		"A, B, C":                "A",
	}
	for in, want := range cases {
		if got := PrimaryArtist(in); got != want {
			t.Errorf("PrimaryArtist(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	base := Catalog{Name: "夜曲", Artist: "周杰伦"}

	// 完全相同
	if got := Similarity(base, Catalog{Name: "夜曲", Artist: "周杰伦"}); got != 1 {
		t.Errorf("完全相同相似度 = %v, 期望 1", got)
	}

	// 带版本后缀仍应高度相似（归一化会去掉括号内容）
	if got := Similarity(base, Catalog{Name: "夜曲 (Live)", Artist: "周杰伦"}); got < 0.95 {
		t.Errorf("带版本后缀相似度 = %v, 期望 >= 0.95", got)
	}

	// 多歌手标注：包含关系应给高分
	if got := Similarity(base, Catalog{Name: "夜曲", Artist: "周杰伦/杨瑞代"}); got < 0.9 {
		t.Errorf("多歌手标注相似度 = %v, 期望 >= 0.9（包含关系启发式）", got)
	}

	// 完全不同的歌
	if got := Similarity(base, Catalog{Name: "晴天", Artist: "陈奕迅"}); got > 0.5 {
		t.Errorf("不同歌曲相似度 = %v, 期望 < 0.5", got)
	}

	// 歌名相同歌手不同：应显著低于完全相同
	sameName := Similarity(base, Catalog{Name: "夜曲", Artist: "某某翻唱"})
	if sameName >= 0.9 {
		t.Errorf("同名不同歌手相似度 = %v, 应明显低于 1", sameName)
	}
	if sameName <= 0.5 {
		t.Errorf("同名不同歌手相似度 = %v, 应高于完全不同的歌", sameName)
	}
}

func TestSimilarityArtistMissing(t *testing.T) {
	// 歌手缺失时给中性 0.5，不应因为缺信息就判为不同歌
	got := Similarity(Catalog{Name: "夜曲", Artist: ""}, Catalog{Name: "夜曲", Artist: "周杰伦"})
	if got < 0.8 {
		t.Errorf("歌手缺失时相似度 = %v, 期望 >= 0.8（歌名权重占大头）", got)
	}
}

// ── 候选择优 ──

func TestPickBestPrefersFormalOverVariant(t *testing.T) {
	cands := []Candidate{
		{Catalog: Catalog{Name: "夜曲 (Live)", Duration: 300}, Score: 0.99},
		{Catalog: Catalog{Name: "夜曲", Duration: 300}, Score: 0.95},
	}
	best, ok := PickBest(cands, nil)
	if !ok {
		t.Fatal("应选出候选")
	}
	if best.Name != "夜曲" {
		t.Errorf("正式版优先，选中 %q", best.Name)
	}
}

func TestPickBestFallsBackToVariant(t *testing.T) {
	// 只有改编版可用时才兜底选它
	cands := []Candidate{
		{Catalog: Catalog{Name: "夜曲 (Live)", Duration: 300}, Score: 0.9},
	}
	best, ok := PickBest(cands, nil)
	if !ok {
		t.Fatal("正式版不可用时应兜底选改编版")
	}
	if best.Name != "夜曲 (Live)" {
		t.Errorf("选中 %q", best.Name)
	}
}

func TestPickBestPrefersSatisfying(t *testing.T) {
	cands := []Candidate{
		{Catalog: Catalog{Name: "夜曲", Source: "a"}, Score: 0.95},
		{Catalog: Catalog{Name: "夜曲", Source: "b"}, Score: 0.90},
	}
	satisfies := func(c Candidate) bool { return c.Source == "b" }

	best, ok := PickBest(cands, satisfies)
	if !ok {
		t.Fatal("应选出候选")
	}
	if best.Source != "b" {
		t.Errorf("应优先选达标的候选，选中 %q", best.Source)
	}
}

func TestPickBestWhenNothingSatisfiesReturnsBestAvailable(t *testing.T) {
	cands := []Candidate{
		{Catalog: Catalog{Name: "夜曲", Source: "a"}, Score: 0.95},
	}
	best, ok := PickBest(cands, func(Candidate) bool { return false })
	if !ok {
		t.Fatal("都不达标时也应有兜底候选（交由上层 fallback 策略决定）")
	}
	if best.Source != "a" {
		t.Errorf("选中 %q", best.Source)
	}
}

func TestPickBestEmpty(t *testing.T) {
	if _, ok := PickBest(nil, nil); ok {
		t.Error("空候选应返回 false")
	}
}

func TestPickBestPrefersPrimary(t *testing.T) {
	cands := []Candidate{
		{Catalog: Catalog{Name: "夜曲", Source: "other"}, Score: 0.95, IsPrimary: false},
		{Catalog: Catalog{Name: "夜曲", Source: "origin"}, Score: 0.90, IsPrimary: true},
	}
	best, _ := PickBest(cands, nil)
	if !best.IsPrimary {
		t.Error("原平台候选应优先")
	}
}
