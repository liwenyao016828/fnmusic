package assistant

import "testing"

// 指令条匹配的回归测试。
//
// 这些规则决定了「用户打一句自然语言会发生什么」，一旦改别名或改动词表就
// 可能把「关掉收藏自动下载」匹配到别的开关上 —— 那是**误操作**，比匹配不上
// 严重得多。所以逐条钉死。

func TestMatchCommandSwitches(t *testing.T) {
	cases := []struct {
		text string
		kind string
		keys []string
	}{
		{"收藏自动下载", "switches", []string{"fav_auto_download"}},
		{"关掉收藏自动下载", "toggle", []string{"fav_auto_download"}},
		{"打开收藏自动下载", "toggle", []string{"fav_auto_download"}},
		{"关掉每日AI歌单", "toggle", []string{"daily_llm_enabled"}},
		{"开启歌词自动下载", "toggle", []string{"lyric_auto_download"}},
		{"关掉封面内嵌", "toggle", []string{"cover_embed"}},
		{"打开边听边下", "toggle", []string{"tee_enabled"}},
		{"关掉飞牛音乐注入面板", "toggle", []string{"ui_inject_enabled"}},
		{"有哪些开关", "switches", nil},
		{"开关列表", "switches", nil},
		{"状态", "status", nil},
		{"健康检查", "status", nil},
		{"下载当前歌曲", "download", nil},
		{"帮助", "help", nil},
		{"", "help", nil},
	}
	for _, c := range cases {
		got := MatchCommand(c.text)
		if got.Kind != c.kind {
			t.Fatalf("MatchCommand(%q).Kind = %q，期望 %q", c.text, got.Kind, c.kind)
		}
		if len(c.keys) == 0 {
			continue
		}
		if len(got.Switches) != len(c.keys) {
			t.Fatalf("MatchCommand(%q).Switches = %v，期望 %v", c.text, got.Switches, c.keys)
		}
		for i, k := range c.keys {
			if got.Switches[i] != k {
				t.Fatalf("MatchCommand(%q).Switches = %v，期望 %v", c.text, got.Switches, c.keys)
			}
		}
	}
}

// 「关」与「开」的判定必须分开：offWords 里有「关」，onWords 里有「开」，
// 两者都在时以「关」优先（用户更常先关掉一个东西）。
func TestMatchCommandOffWins(t *testing.T) {
	got := MatchCommand("关掉收藏自动下载")
	if got.Kind != "toggle" || got.On {
		t.Fatalf("期望 toggle+关闭，实际 %+v", got)
	}
	got = MatchCommand("打开收藏自动下载")
	if got.Kind != "toggle" || !got.On {
		t.Fatalf("期望 toggle+开启，实际 %+v", got)
	}
}

// 长别名优先：「收藏自动下载」命中 fav_auto_download，不能被更短的
// 「自动下载歌词」之类吞掉。这里的关键断言是**只命中一个**开关。
func TestMatchCommandLongAliasWins(t *testing.T) {
	got := MatchCommand("收藏自动下载")
	if len(got.Switches) != 1 || got.Switches[0] != "fav_auto_download" {
		t.Fatalf("期望只命中 fav_auto_download，实际 %v", got.Switches)
	}
}

// 开关表与 config 的口径必须一致（同一个 key 只能在一处定义）。
func TestSwitchDefsCoverAllFeatureSwitches(t *testing.T) {
	if len(SwitchDefs) == 0 {
		t.Fatal("SwitchDefs 为空")
	}
	seen := map[string]bool{}
	for _, d := range SwitchDefs {
		if seen[d.Key] {
			t.Fatalf("开关 key 重复：%s", d.Key)
		}
		seen[d.Key] = true
		if d.Label == "" {
			t.Fatalf("开关 %s 没有中文名", d.Key)
		}
	}
}

func TestFindSwitchDef(t *testing.T) {
	if _, ok := FindSwitchDef("fav_auto_download"); !ok {
		t.Fatal("找不到 fav_auto_download")
	}
	if _, ok := FindSwitchDef("no_such_switch"); ok {
		t.Fatal("不存在的开关不该被找到")
	}
}

func TestToggleDescription(t *testing.T) {
	got := ToggleDescription([]string{"fav_auto_download"}, false)
	if got != "已关闭 收藏自动下载" {
		t.Fatalf("实际 %q", got)
	}
	got = ToggleDescription([]string{"fav_auto_download"}, true)
	if got != "已开启 收藏自动下载" {
		t.Fatalf("实际 %q", got)
	}
}
