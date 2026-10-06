package nameparse

import "testing"

// ── 常规命名：能拆出来，且**不该**标 suspect（标了就是白花 AI 的钱）──

func TestParseNormalNames(t *testing.T) {
	cases := []struct {
		in       string
		wantArt  string
		wantTit  string
		wantSusp bool
	}{
		{"周杰伦 - 晴天.mp3", "周杰伦", "晴天", false},
		{"01. 周杰伦 - 晴天.flac", "周杰伦", "晴天", false},
		{"1_晴天.mp3", "", "晴天", false},
		{"晴天.mp3", "", "晴天", false},
		// 全英文：按惯例左歌手右歌名，不怀疑
		{"The Beatles - Yesterday.mp3", "The Beatles", "Yesterday", false},
		// 全中文三段式会被标可疑（见下），两段不标
		{"蔡依林 - 倒带.mp3", "蔡依林", "倒带", false},
		// 多歌手组合
		{"周杰伦, 费玉清 - 千里之外.mp3", "周杰伦, 费玉清", "千里之外", false},
	}
	for _, c := range cases {
		got := Parse(c.in)
		if got.Artist != c.wantArt || got.Title != c.wantTit {
			t.Errorf("Parse(%q) = (%q, %q)，期望 (%q, %q)", c.in, got.Artist, got.Title, c.wantArt, c.wantTit)
		}
		if got.Suspect != c.wantSusp {
			t.Errorf("Parse(%q).Suspect = %v（%s），期望 %v", c.in, got.Suspect, got.Reason, c.wantSusp)
		}
	}
}

// ── 本包最重要的行为：规则**自认可疑** ──
//
// 「拆不出来」（字段为空）好办，一看就知道；
// 危险的是「规则以为自己对、其实错了」—— 字段非空、看着正常，却悄悄写进标签。
// 下面每一条都是后者。

func TestParseMarksSuspect(t *testing.T) {
	cases := []struct {
		in          string
		wantSusp    bool
		wantReason  string // 只检查关键词，避免措辞微调就挂
		wantArtist  string // suspect 时也可能已经纠正了方向
		wantTitle   string
		description string
	}{
		{
			// 参考实现举的例子：左边被切成「好听的歌曲推荐」，非空但是错的
			in: "好听的歌曲推荐-下山 - 麦小兜.mp3", wantSusp: true,
			wantReason: "噪声词", wantArtist: "麦小兜", wantTitle: "好听的歌曲推荐-下山",
			description: "歌手位置是歌单名 → 必须 suspect，并尝试纠正方向",
		},
		{
			in: "【无损】周杰伦 - 晴天.flac", wantSusp: true,
			wantReason: "方括号", wantArtist: "周杰伦", wantTitle: "晴天",
			description: "开头方括号多半是歌单前缀",
		},
		{
			in: "周杰伦 - JayChou.mp3", wantSusp: true,
			wantReason: "中英文混排", wantArtist: "周杰伦", wantTitle: "JayChou",
			description: "中英混排 → 方向说不准",
		},
		{
			in: "好听的歌曲推荐合集一百首经典老歌.mp3", wantSusp: true,
			wantReason: "噪声词", wantArtist: "", wantTitle: "好听的歌曲推荐合集一百首经典老歌",
			description: "无分隔符但命中噪声词 → 歌单名+歌名粘在一起",
		},
		{
			in: "车载音乐 - 经典老歌 - 第一辑.mp3", wantSusp: true,
			wantReason: "噪声词", wantArtist: "经典老歌", wantTitle: "第一辑",
			description: "歌单名前缀 → 丢掉前缀再切，而不是左右互换",
		},
	}

	for _, c := range cases {
		got := Parse(c.in)
		if got.Suspect != c.wantSusp {
			t.Errorf("[%s] Parse(%q).Suspect = %v，期望 %v", c.description, c.in, got.Suspect, c.wantSusp)
		}
		if c.wantReason != "" && !contains(got.Reason, c.wantReason) {
			t.Errorf("[%s] Reason = %q，应包含 %q", c.description, got.Reason, c.wantReason)
		}
		if c.wantArtist != "" && got.Artist != c.wantArtist {
			t.Errorf("[%s] Artist = %q，期望 %q", c.description, got.Artist, c.wantArtist)
		}
		if c.wantTitle != "" && got.Title != c.wantTitle {
			t.Errorf("[%s] Title = %q，期望 %q", c.description, got.Title, c.wantTitle)
		}
	}
}

// ── 候选：左右两种解释都要留着，交给上层（必要时交给 AI）挑 ──

func TestParseCandidates(t *testing.T) {
	got := Parse("周杰伦 - 晴天.mp3")
	if len(got.Candidates) < 2 {
		t.Fatalf("两段命名应给出至少 2 个候选，实际 %d 个：%+v", len(got.Candidates), got.Candidates)
	}
	// 第一个候选必须与 Title/Artist 一致，否则上层拿候选去问 AI 会得到矛盾结果
	if got.Candidates[0].Artist != got.Artist || got.Candidates[0].Title != got.Title {
		t.Errorf("首个候选应与结果一致：候选 %+v，结果 (%q,%q)", got.Candidates[0], got.Artist, got.Title)
	}
	// 必须包含互换解释
	found := false
	for _, c := range got.Candidates {
		if c.Artist == "晴天" && c.Title == "周杰伦" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少互换候选（歌手=晴天 歌名=周杰伦）：%+v", got.Candidates)
	}
}

// ── NeedsAI：字段为空 **或** 自认可疑，都要兜 ──

func TestNeedsAI(t *testing.T) {
	if Parse("周杰伦 - 晴天.mp3").NeedsAI() {
		t.Error("正常解析不该要求 AI")
	}
	if !Parse("晴天.mp3").NeedsAI() {
		t.Error("缺歌手应要求 AI")
	}
	if !Parse("【无损】周杰伦 - 晴天.flac").NeedsAI() {
		t.Error("suspect 应要求 AI")
	}
}

// ── 序号与专辑 ──

func TestParseTrackNo(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"01. 周杰伦 - 晴天.mp3", "1"},
		{"007 晴天.mp3", "7"},
		{"1、晴天.mp3", "1"},
		{"周杰伦 - 晴天.mp3", ""},
	} {
		if got := Parse(c.in).Track; got != c.want {
			t.Errorf("Parse(%q).Track = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// ── 边界：不能 panic，空输入要安全 ──

func TestParseEdgeCases(t *testing.T) {
	for _, in := range []string{"", ".mp3", "   ", "周杰伦.mp3", "- 晴天.mp3", "周杰伦 -.mp3"} {
		got := Parse(in) // 只要不 panic 就算过
		_ = got.NeedsAI()
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
