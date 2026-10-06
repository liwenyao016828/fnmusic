package nameparse

import (
	"os"
	"path/filepath"
	"testing"
)

// ── 校验：这是 AI 产出正则的硬门槛 ──
//
// 重点**不是「好正则能不能过」，而是「坏正则挡不挡得住」**。
// AI 可能给出语法错的、匹配不上的、提取不出字段的、甚至从别处变出内容的规则。

func TestValidateRuleRejectsBadRules(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want string // 错误信息里应包含的关键词
	}{
		{
			name: "空正则",
			rule: Rule{Regex: ""},
			want: "不能为空",
		},
		{
			name: "语法错误",
			rule: Rule{Regex: `(?P<title>[a-z`},
			want: "语法错误",
		},
		{
			name: "组名不在白名单",
			rule: Rule{Regex: `(?P<singer>.+?)\s*-\s*(?P<title>.+)`},
			want: "不支持的字段名",
		},
		{
			name: "没有 artist 也没有 title",
			rule: Rule{Regex: `(?P<album>.+)`},
			want: "至少要有",
		},
		{
			name: "匹配不上样例",
			rule: Rule{Regex: `(?P<artist>[A-Z]+)\s*-\s*(?P<title>.+)`, Sample: "周杰伦 - 晴天.mp3"},
			want: "匹配不上",
		},
		{
			name: "没提取出歌手歌名",
			rule: Rule{
				Regex:  `^(?P<artist>)(?P<title>)$`,
				Sample: "周杰伦 - 晴天.mp3",
			},
			want: "匹配不上", // 空匹配会被 applyRule 判为无效
		},
		{
			// 防幻觉：正则用了固定文本，提取出的内容不在样例里
			name: "提取结果不在样例里",
			rule: Rule{
				Regex:  `(?P<artist>五月天)\s*-\s*(?P<title>.+)`,
				Sample: "周杰伦 - 晴天.mp3",
			},
			want: "匹配不上",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRule(c.rule)
			if err == nil {
				t.Fatalf("这条规则应被拒绝：%+v", c.rule)
			}
			if !contains(err.Error(), c.want) {
				t.Errorf("错误信息 = %q，应包含 %q", err.Error(), c.want)
			}
		})
	}
}

func TestValidateRuleAcceptsGoodRules(t *testing.T) {
	cases := []Rule{
		// 常见：歌手 - 歌名
		{Regex: `^(?P<artist>[^-]+?)\s*-\s*(?P<title>.+?)(?:\.[^.]+)?$`, Sample: "周杰伦 - 晴天.mp3"},
		// 带序号
		{Regex: `^(?P<track>\d+)[.\s]+(?P<artist>.+?)\s*-\s*(?P<title>.+?)(?:\.[^.]+)?$`, Sample: "01. 周杰伦 - 晴天.flac"},
		// 下划线风格
		{Regex: `^(?P<artist>[^_]+)_(?P<title>.+?)(?:\.[^.]+)?$`, Sample: "Jay_Chou_晴天.mp3"},
		// 只有歌名（title 一个也允许）
		{Regex: `^(?P<title>.+?)(?:\.[^.]+)?$`, Sample: "晴天.mp3"},
	}
	for _, r := range cases {
		if err := ValidateRule(r); err != nil {
			t.Errorf("这条规则应通过：%s → %v", r.Regex, err)
		}
	}
}

// 没给样例时只做静态检查（不能因为没样例就拒绝）
func TestValidateRuleWithoutSample(t *testing.T) {
	if err := ValidateRule(Rule{Regex: `^(?P<artist>.+?) - (?P<title>.+)$`}); err != nil {
		t.Errorf("没给样例时应只做静态检查，却报错：%v", err)
	}
}

// ── Store：持久化 + 优先于内置启发式 ──

func TestStoreUpsertAndParse(t *testing.T) {
	s := NewStore("")

	// 一条内置启发式拆不好的规则：「【无损】Jay_Chou_晴天_2003」
	r := Rule{
		Regex:  `^【[^】]*】(?P<artist>[^_]+)_(?P<artist2>[^_]+)_(?P<title>[^_]+)_\d+`,
		Sample: "【无损】Jay_Chou_晴天_2003.mp3",
	}
	if err := ValidateRule(r); err == nil {
		t.Fatal("artist2 不在白名单，应被拒绝")
	}

	r = Rule{
		Regex:  `^【[^】]*】(?P<artist>Jay_Chou)_(?P<title>[^_]+)_\d+(?:\.[^.]+)?$`,
		Sample: "【无损】Jay_Chou_晴天_2003.mp3",
	}
	// 这条正则用了固定文本 Jay_Chou，但样例里确实有 → 应通过
	if err := ValidateRule(r); err != nil {
		t.Fatalf("这条规则应通过：%v", err)
	}
	saved, err := s.Upsert(r)
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if saved.ID == "" {
		t.Error("应自动生成 ID")
	}

	got := s.Parse("【无损】Jay_Chou_晴天_2003.mp3")
	if got.Artist != "Jay_Chou" || got.Title != "晴天" {
		t.Errorf("用户规则应生效，实际 (%q, %q)", got.Artist, got.Title)
	}
	if got.Suspect {
		t.Error("用户规则命中不该标可疑 —— 用户明确告诉过我们这类名字怎么读")
	}
	if got.Candidates[0].Source != "rule" {
		t.Errorf("候选来源应为 rule，实际 %q", got.Candidates[0].Source)
	}
}

// 规则不匹配时回落到内置启发式
func TestStoreFallsBackToHeuristic(t *testing.T) {
	s := NewStore("")
	if _, err := s.Upsert(Rule{
		Regex:  `^【[^】]*】(?P<artist>Jay_Chou)_(?P<title>[^_]+)_\d+(?:\.[^.]+)?$`,
		Sample: "【无损】Jay_Chou_晴天_2003.mp3",
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	// 完全不同的命名 → 走内置启发式
	got := s.Parse("周杰伦 - 晴天.mp3")
	if got.Artist != "周杰伦" || got.Title != "晴天" {
		t.Errorf("没命中规则时应回落启发式，实际 (%q, %q)", got.Artist, got.Title)
	}
}

// 用户规则 vs 内置启发式的**对比**：同一个文件名，两条路结果不同。
//
// 这个对比是端到端验证过的（2026-09-18）：用规则解析出的干净歌名
// 「晴天」能搜到并补上歌词封面；启发式给出的「晴天_2003」匹配不上，
// 整首歌就补不了。所以规则不是锦上添花，是能决定成败的。
func TestRuleBeatsHeuristic(t *testing.T) {
	const filename = "【无损】周杰伦_晴天_2003.mp3"

	// 内置启发式：在第一个 "_" 处切一刀，年份留在了歌名里
	heur := Parse(filename)
	if heur.Title != "晴天_2003" {
		t.Fatalf("启发式结果变了（%q），本对比用例需要更新", heur.Title)
	}
	if !heur.Suspect {
		t.Error("开头有方括号，启发式应标可疑")
	}

	// 用户规则：吃掉 _2003，歌名干净
	s := NewStore("")
	if _, err := s.Upsert(Rule{
		Regex:  `^【[^】]*】(?P<artist>[^_]+)_(?P<title>[^_]+)_\d+(?:\.[^.]+)?$`,
		Sample: filename,
		Note:   "方括号是音质标记，下划线后是年份",
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	got := s.Parse(filename)
	if got.Artist != "周杰伦" || got.Title != "晴天" {
		t.Errorf("规则解析 = (%q, %q)，期望 (周杰伦, 晴天)", got.Artist, got.Title)
	}
	if got.Suspect {
		t.Error("用户规则命中不该标可疑")
	}
	if got.Title == heur.Title {
		t.Error("规则与启发式结果相同，说明规则没生效")
	}
}

// 未启用的规则不参与解析
func TestStoreSkipsDisabled(t *testing.T) {
	s := NewStore("")
	if _, err := s.Upsert(Rule{
		Regex:    `^(?P<title>.+?)(?:\.[^.]+)?$`,
		Sample:   "周杰伦 - 晴天.mp3",
		Disabled: true,
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	got := s.Parse("周杰伦 - 晴天.mp3")
	// 未启用 → 走启发式，应拆出歌手
	if got.Artist != "周杰伦" {
		t.Errorf("未启用的规则不该生效，实际 (%q, %q)", got.Artist, got.Title)
	}
}

// 落盘 + 重新加载
func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "name_rules.json")

	s1 := NewStore(path)
	if _, err := s1.Upsert(Rule{
		Regex:  `^(?P<artist>[^-]+?) - (?P<title>.+?)(?:\.[^.]+)?$`,
		Sample: "周杰伦 - 晴天.mp3",
		Name:   "标准命名",
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	s2 := NewStore(path)
	list := s2.List()
	if len(list) != 1 {
		t.Fatalf("重新加载应有 1 条规则，实际 %d 条", len(list))
	}
	if list[0].Name != "标准命名" {
		t.Errorf("规则名丢了：%+v", list[0])
	}
	if got := s2.Parse("A - B.mp3"); got.Artist != "A" || got.Title != "B" {
		t.Errorf("重载后规则应生效，实际 (%q, %q)", got.Artist, got.Title)
	}
}

// 文件损坏时不能把整个功能带崩
func TestStoreCorruptedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "name_rules.json")
	if err := os.WriteFile(path, []byte("{不是合法 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if len(s.List()) != 0 {
		t.Error("损坏文件应被当成空")
	}
	// 内置启发式仍要可用
	if got := s.Parse("周杰伦 - 晴天.mp3"); got.Artist != "周杰伦" {
		t.Errorf("损坏后启发式仍应可用，实际 %q", got.Artist)
	}
}

func TestRemoveRule(t *testing.T) {
	s := NewStore("")
	saved, _ := s.Upsert(Rule{
		Regex:  `^(?P<title>.+?)(?:\.[^.]+)?$`,
		Sample: "晴天.mp3",
	})
	if err := s.Remove(saved.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if len(s.List()) != 0 {
		t.Error("删除后应为空")
	}
}

func TestTestRule(t *testing.T) {
	got, err := TestRule(`^(?P<artist>[^-]+?) - (?P<title>.+?)(?:\.[^.]+)?$`, "周杰伦 - 晴天.mp3")
	if err != nil {
		t.Fatalf("试算失败：%v", err)
	}
	if got.Artist != "周杰伦" || got.Title != "晴天" {
		t.Errorf("试算结果 = (%q, %q)", got.Artist, got.Title)
	}

	if _, err := TestRule(`^(?P<artist>[A-Z]+) - (?P<title>.+)$`, "周杰伦 - 晴天.mp3"); err == nil {
		t.Error("匹配不上时应报错")
	}
}
