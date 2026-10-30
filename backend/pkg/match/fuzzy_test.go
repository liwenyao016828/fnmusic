package match

import "testing"

// 模糊档（2026-10-29）：只在严格档全落空后才用，命中必须能被用户看出来。
// 这组测试钉的是**边界的三个方向**：
//   ① 该过的要过（繁简 / 错一个字 / 平台加尾巴）；
//   ② 不该过的绝不能过（同结构不同歌、两字歌名的单字差异、版本标记）；
//   ③ 歌手门要起作用（同名不同人 = 翻唱，拒）。

func TestFoldVariantsTraditionalAndPunct(t *testing.T) {
	cases := [][2]string{
		{"後來", "后来"},
		{"說好的幸福呢 (Live)", "说好的幸福呢"},
		{"晴天（現場版）", "晴天"},
		{"浪漫手機", "浪漫手机"},
	}
	for _, c := range cases {
		if got := FoldVariants(c[0]); got != FoldVariants(c[1]) {
			t.Errorf("FoldVariants(%q)=%q 应等于 FoldVariants(%q)=%q", c[0], got, c[1], FoldVariants(c[1]))
		}
	}
}

func TestFuzzyTitleAcceptsRealVariants(t *testing.T) {
	cases := []struct{ want, got string }{
		{"后来", "後來"}, // 繁简（折叠后完全相等）
		{"说好的幸福呢", "说好的幸福呢 (原唱)"}, // 平台加尾巴，括号被剥掉
		{"告白气球", "告白气球甜蜜"},        // 平台多加了个尾巴（差 2 字，包含兜住）
		{"青花瓷", "青花磁"},            // 三字歌名错一个字 = 0.67，过线
		{"月亮代表我的心", "月亮代表我的心啊"},   // 多一个字 = 0.92
	}
	for _, c := range cases {
		score, kind, ok := FuzzyTitle(c.want, c.got)
		if !ok {
			t.Errorf("FuzzyTitle(%q,%q) 应命中，实际被拒（score=%.3f）", c.want, c.got, score)
			continue
		}
		if score <= 0 || score > 1 {
			t.Errorf("FuzzyTitle(%q,%q) 分数越界：%.3f", c.want, c.got, score)
		}
		if kind == "" {
			t.Errorf("FuzzyTitle(%q,%q) 命中必须给出人话原因（预览页要展示）", c.want, c.got)
		}
	}
}

func TestFuzzyTitleRejectsDifferentSongs(t *testing.T) {
	cases := []struct{ want, got, why string }{
		{"晴天", "雨天", "同结构不同歌：两字歌名只差一个字，不能猜"},
		{"晴天", "晴天娃娃", "短的不足 3 字，不能被长标题吞掉"},
		{"童话", "神话", "两字歌名单字差异"},
		{"后来", "后来遇见他", "长度差 3，是另一首歌"},
		{"七里香", "七里外的稻香", "错得太多"},
		{"", "晴天", "一方为空不能算命中"},
	}
	for _, c := range cases {
		if score, kind, ok := FuzzyTitle(c.want, c.got); ok {
			t.Errorf("FuzzyTitle(%q,%q) 不该命中（%s），却以 %.3f/%s 通过了", c.want, c.got, c.why, score, kind)
		}
	}
}

func TestFuzzyAcceptArtistGate(t *testing.T) {
	// 歌名一样、歌手完全不同 → 拒（对方是翻唱 / 同名不同人）
	if _, _, ok := FuzzyAccept("晴天", "周杰伦", "晴天", "五月天"); ok {
		t.Error("歌手差得远时必须拒绝，否则会把别人的版本当成原唱")
	}
	// 多歌手标注：只要有一方包含另一方就放行
	if score, kind, ok := FuzzyAccept("说好的幸福呢", "周杰伦", "说好的幸福呢", "周杰伦/杨瑞代"); !ok {
		t.Errorf("多歌手标注应放行，实际被拒（score=%.3f kind=%s）", score, kind)
	}
	// 一方没标歌手 → 中性，放行（与 ArtistScore 的既有约定一致）
	if _, _, ok := FuzzyAccept("稻香", "周杰伦", "稻香", ""); !ok {
		t.Error("候选没标歌手时不该直接拒（无法判断，交给预览让用户核）")
	}
	// 歌名完全相同、歌手一致 → 歌手一致这个事实要写进原因里
	if _, kind, ok := FuzzyAccept("稻香", "周杰伦", "稻香", "周杰伦"); !ok || kind != "歌名一致·歌手一致" {
		t.Errorf("歌名折叠后完全相等应走 fast path 并标歌手一致，实际 ok=%v kind=%q", ok, kind)
	}
}

func TestFuzzyAcceptEmptyTitleNeverMatches(t *testing.T) {
	if _, _, ok := FuzzyAccept("", "周杰伦", "晴天", "周杰伦"); ok {
		t.Error("目标歌名为空时不能命中任何候选")
	}
}
