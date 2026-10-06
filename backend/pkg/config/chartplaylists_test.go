package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 榜单名单（`chart_playlists`）是「榜单管理」页保存下来的东西 ——
// 它直接决定飞牛音乐的歌单列表里多出哪几张卡片。
//
// 这里钉的是三件事：
//  1. Update（界面保存）之后**重新打开进程**读回来还在；
//  2. `load` 与 `Update` 是两处独立的选择性合并，**两边都要登记**；
//     漏一处就表现为「保存成功、重启后自己变空」—— `PreferredPlatforms`
//     真的漏过（见下面第三条用例），APISources 也漏过。
//  3. 清洗规则（小写、去空白、去重）在两条路上一致。
func TestUpdatePersistsChartPlaylists(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	cfg := cm.Get()
	// 故意写脏：大写、前后空白、重复 —— 清洗是配置层的责任。
	cfg.ChartPlaylists = []string{" WY_19723756 ", "wy_19723756", "TX_26", ""}
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	got := cm.Get().ChartPlaylists
	want := []string{"wy_19723756", "tx_26"}
	if len(got) != len(want) {
		t.Fatalf("清洗后应有 %d 条，得到 %+v", len(want), got)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Fatalf("第 %d 条应为 %q，得到 %q（全部：%+v）", n, want[n], got[n], got)
		}
	}

	// 重新打开：确认真的落盘了，而不是只在内存里。
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	reloaded := cm2.Get().ChartPlaylists
	if len(reloaded) != 2 || reloaded[0] != "wy_19723756" || reloaded[1] != "tx_26" {
		t.Fatalf("重启后读回不对：%+v", reloaded)
	}

	// 空切片 = 清空（榜单管理页「全部取消」走的就是这条路）
	cfg2 := cm2.Get()
	cfg2.ChartPlaylists = []string{}
	if err := cm2.Update(cfg2); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if n := len(cm2.Get().ChartPlaylists); n != 0 {
		t.Errorf("空切片应清空，实际还剩 %d 条", n)
	}

	// nil = 不动
	cfg3 := cm2.Get()
	cfg3.ChartPlaylists = []string{"wy_19723756"}
	_ = cm2.Update(cfg3)
	cfg4 := cm2.Get()
	cfg4.ChartPlaylists = nil
	_ = cm2.Update(cfg4)
	if n := len(cm2.Get().ChartPlaylists); n != 1 {
		t.Errorf("nil 表示不动，应保留 1 条，实际 %d 条", n)
	}
}

// 配置文件里已经有 chart_playlists 时，`load` 必须把它读进来。
func TestLoadKeepsChartPlaylists(t *testing.T) {
	dir := t.TempDir()
	raw := `{
	  "port": 8899,
	  "chart_playlists": ["wy_19723756", " WY_3778678 ", "kg_8888"]
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get().ChartPlaylists
	// 注意 kg_8888 会留下来：配置层**不判平台合法性**（那是拦截层的事）。
	// 这样以后接入酷狗解析器时，用户之前勾过的榜直接就生效。
	want := []string{"wy_19723756", "wy_3778678", "kg_8888"}
	if len(got) != len(want) {
		t.Fatalf("应有 %d 条，得到 %+v", len(want), got)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Errorf("第 %d 条应为 %q，得到 %q", n, want[n], got[n])
		}
	}
}

// 这条盯的是一个**真 bug**：`PreferredPlatforms` 原来只登记在 Update 里，
// `load` 里漏了 —— 表现为「界面上改主力平台当时能用，重启后自己变回默认」。
//
// 它与榜单名单是同一类错误（新增字段必须两边都登记），所以放在一起 ——
// 谁再犯，这两条会一起红。
func TestLoadKeepsPreferredPlatforms(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 8899, "preferred_platforms": ["tx"]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get().PreferredPlatforms
	if len(got) != 1 || got[0] != "tx" {
		t.Fatalf("重启后主力平台应保持 [tx]，得到 %+v（默认值把它盖掉了？）", got)
	}
}

// 清洗规则本身：空白、大小写、重复、空元素。
func TestNormalizeChartPlaylists(t *testing.T) {
	got := NormalizeChartPlaylists([]string{" WY_1 ", "", "wy_1", "Tx_2", "  ", "wy_1"})
	want := []string{"wy_1", "tx_2"}
	if len(got) != len(want) {
		t.Fatalf("应有 %d 条，得到 %+v", len(want), got)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Errorf("第 %d 条应为 %q，得到 %q", n, want[n], got[n])
		}
	}
	// 全空输入退化成空切片（而不是 nil）：调用方靠这个区分「清空」与「不动」。
	if out := NormalizeChartPlaylists(nil); len(out) != 0 {
		t.Errorf("nil 输入应得到空切片，得到 %+v", out)
	}
}

// 总开关的三态口径：**没设置过 = 开**。
//
// 这条口径很重要：新装的用户不该先去界面点一下才能用；而显式关掉的人
// 重启后必须仍然是关的（我们存的是 false，不是「没存」）。
func TestChartsInjectEnabledDefaultsToOn(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		val  *bool
		want bool
	}{
		{"没设置过", nil, true},
		{"显式开", &on, true},
		{"显式关", &off, false},
	}
	for _, c := range cases {
		got := AppConfig{ChartPlaylistsEnabled: c.val}.ChartsInjectEnabled()
		if got != c.want {
			t.Errorf("%s：应为 %v，得到 %v", c.name, c.want, got)
		}
	}
}

// 关掉总开关**不能**把勾选名单一起丢掉 —— 这是它作为独立开关的全部理由。
// 也顺带钉住「重启后开关状态还在」（load 侧登记）。
func TestUpdatePersistsChartsMasterSwitch(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 第一步：像榜单管理页勾选后保存那样，先把名单存进去。
	cfg := cm.Get()
	cfg.ChartPlaylists = []string{"wy_19723756", "wy_3778678"}
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("保存名单失败: %v", err)
	}

	// 第二步：像只拨总开关那样，发一份**只带这一个键**的局部 patch ——
	// POST /api/config 收的就是这种 patch（前端切换开关时也只发这一个键）。
	off := false
	patch := AppConfig{ChartPlaylistsEnabled: &off}
	if err := cm.Update(patch); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	got := cm.Get()
	if got.ChartsInjectEnabled() {
		t.Error("显式写入 false 后应当视为关闭")
	}

	// patch 里没出现的键必须原样保留（Update 是选择性合并）。
	if n := len(got.ChartPlaylists); n != 2 {
		t.Errorf("只改总开关不该影响名单，应有 2 条，实际 %d 条", n)
	}

	// 重启：开关与名单都要在。
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	after := cm2.Get()
	if after.ChartsInjectEnabled() {
		t.Error("重启后总开关应保持关闭（load 侧漏登记就会变回默认开）")
	}
	if n := len(after.ChartPlaylists); n != 2 {
		t.Errorf("重启后名单应保住 2 条，实际 %d 条", n)
	}
	// 而且必须是**真的** false 落到了盘上，而不是靠 nil 兜底：
	if after.ChartPlaylistsEnabled == nil {
		t.Error("盘上应当存着显式的 false，而不是缺少这个键")
	}
}
