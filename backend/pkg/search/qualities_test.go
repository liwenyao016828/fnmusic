package search

import "testing"

func TestNormalizeForMatch(t *testing.T) {
	cases := []struct{ in, want string }{
		{"晴天", "晴天"},
		{" 晴天 ", "晴天"},
		{"晴天 (Live)", "晴天"},
		{"晴天（Live）", "晴天"},
		{"晴天 [Remastered]", "晴天"},
		// 成对括号内容整体剥掉：【无损】属于噪声前缀，剥掉后更利于匹配
		{"【无损】周杰伦 - 晴天", "周杰伦晴天"},
		{"G.E.M. 邓紫棋", "gem邓紫棋"},
		{"A & B / C", "abc"},
		{"Hello, World", "helloworld"},
	}
	for _, c := range cases {
		if got := normalizeForMatch(c.in); got != c.want {
			t.Errorf("normalizeForMatch(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 未闭合的括号不应吞掉后续内容（否则会误匹配）
func TestNormalizeForMatchUnclosedParen(t *testing.T) {
	got := normalizeForMatch("晴天 (Live")
	if got != "晴天(live" {
		t.Errorf("未闭合括号处理异常: %q", got)
	}
}

func TestMatchByID(t *testing.T) {
	songs := []UnifiedSong{
		{ID: "wy_111", Songmid: "111", Name: "晴天"},
		{ID: "tx_0039MnYb0qxYhV", Songmid: "0039MnYb0qxYhV", Name: "稻香"},
		{ID: "kg_ABC123", Songmid: "ABC123", Hash: "ABC123", Name: "告白气球"},
		{ID: "kw_999", Songmid: "999", Name: "青花瓷"},
	}

	cases := []struct{ name, id, hash, wantTitle string }{
		{"按 songmid 命中网易", "111", "", "晴天"},
		{"按 songmid 命中 QQ", "0039MnYb0qxYhV", "", "稻香"},
		{"按完整 ID 命中 QQ", "tx_0039MnYb0qxYhV", "", "稻香"},
		{"按 hash 命中酷狗", "", "ABC123", "告白气球"},
		{"按 songmid 命中酷我", "999", "", "青花瓷"},
		{"命中不到", "nope", "", ""},
	}
	for _, c := range cases {
		s, ok := matchByID(songs, c.id, c.hash)
		if c.wantTitle == "" {
			if ok {
				t.Errorf("%s: 不应命中，却返回 %q", c.name, s.Name)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: 应命中 %q，实际未命中", c.name, c.wantTitle)
			continue
		}
		if s.Name != c.wantTitle {
			t.Errorf("%s: 命中 %q，期望 %q", c.name, s.Name, c.wantTitle)
		}
	}
}

func TestMatchByIDEmptyInput(t *testing.T) {
	if _, ok := matchByID([]UnifiedSong{{ID: "x"}}, "", ""); ok {
		t.Error("id 与 hash 都为空时不应命中")
	}
}

func TestMatchByNameSinger(t *testing.T) {
	songs := []UnifiedSong{
		{Name: "晴天", Singer: "周杰伦"},
		{Name: "布拉格广场", Singer: "蔡依林, 周杰伦"},
		{Name: "稻香 (Live)", Singer: "周杰伦"},
	}

	cases := []struct {
		name, singer, wantTitle string
		wantOK                  bool
	}{
		{"晴天", "周杰伦", "晴天", true},
		{"晴天", "", "晴天", true},           // 不传歌手只看曲名
		{"布拉格广场", "周杰伦", "布拉格广场", true},  // 多歌手包含关系
		{"稻香", "周杰伦", "稻香 (Live)", true}, // 括号后缀不影响
		{"不存在的歌", "周杰伦", "", false},
		{"晴天", "别的歌手", "", false}, // 曲名对但歌手不符
	}
	for _, c := range cases {
		s, ok := matchByNameSinger(songs, c.name, c.singer)
		if ok != c.wantOK {
			t.Errorf("matchByNameSinger(%q,%q) ok=%v，期望 %v", c.name, c.singer, ok, c.wantOK)
			continue
		}
		if ok && s.Name != c.wantTitle {
			t.Errorf("matchByNameSinger(%q,%q) 命中 %q，期望 %q", c.name, c.singer, s.Name, c.wantTitle)
		}
	}
}

func TestMatchByNameSingerEmptyName(t *testing.T) {
	if _, ok := matchByNameSinger([]UnifiedSong{{Name: "晴天"}}, "", "周杰伦"); ok {
		t.Error("曲名为空时不应命中")
	}
}

// 官方协议平台表必须与 pkg/protocol.OfficialPlatforms 一致
func TestOfficialPlatformsCoverage(t *testing.T) {
	for _, p := range []string{"wy", "tx", "kg", "kw"} {
		if !officialPlatforms[p] {
			t.Errorf("缺少平台 %q", p)
		}
	}
	if officialPlatforms["mg"] {
		t.Error("mg 不在官方协议覆盖范围内，不应被接受")
	}
	if len(officialPlatforms) != 4 {
		t.Errorf("官方平台应为 4 个，实际 %d 个", len(officialPlatforms))
	}
}
