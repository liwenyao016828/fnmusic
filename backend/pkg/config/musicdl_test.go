package config

import (
	"os"
	"path/filepath"
	"testing"
)

// musicdl sidecar 的配置项（v2.1.83 新增）。
//
// 这一组钉的是**「默认不动现有行为」与「重启后还是我设的那样」**：
//
//  1. 默认**关**。打开它会占磁盘（venv 一两百 MB）且首次启动要联网装依赖 ——
//     这种代价该由用户明确同意，不能因为「能力更好」就替他决定。
//  2. 默认平台名单只含曲率原生**没有**的那三个（mg/bq/bi）。把 kg/kw 放进去会
//     改变现有行为（那两个平台原生的搜索能用但拿不到直链，接了 sidecar 之后
//     搜索与解析都会换实现），所以必须用户显式加。
//  3. load 与 Update **两处都要登记** —— `PreferredPlatforms` 真漏过一次，
//     症状是「界面上改完能用、重启后自己变回默认」。
func TestMusicDLDefaultsOff(t *testing.T) {
	cases := []struct {
		name string
		cfg  AppConfig
		want bool
	}{
		{"没设置过（nil）= 关", AppConfig{}, false},
		{"显式开", AppConfig{MusicDLEnabled: boolPtr(true)}, true},
		{"显式关", AppConfig{MusicDLEnabled: boolPtr(false)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.MusicDLOn(); got != c.want {
				t.Errorf("MusicDLOn() 应为 %v，得到 %v", c.want, got)
			}
		})
	}
}

func TestMusicDLDefaultSourcesAreAdditiveOnly(t *testing.T) {
	// 默认名单里**不能**出现曲率已经原生支持的平台：那会把现有行为换掉。
	// wy/tx 原生全支持（纯重复），kg/kw 原生搜索能用（换了就是行为变更）。
	for _, banned := range []string{"wy", "tx", "kg", "kw"} {
		for _, s := range MusicDLDefaultSources {
			if s == banned {
				t.Fatalf("默认名单不该含 %s（会改变现有行为）：%v", banned, MusicDLDefaultSources)
			}
		}
	}
	if got := (AppConfig{}).MusicDLActiveSources(); len(got) != len(MusicDLDefaultSources) {
		t.Fatalf("空配置该回落到默认名单，得到 %v", got)
	}
}

func TestNormalizeMusicDLSources(t *testing.T) {
	got := NormalizeMusicDLSources([]string{" MG ", "bq", "mg", "", "  ", "BI"})
	want := []string{"mg", "bq", "bi"}
	if len(got) != len(want) {
		t.Fatalf("该去重 + 小写 + 去空白：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序该保持首次出现的顺序：%v", got)
		}
	}
	// 认不出的**不在这里丢**：交给 sidecar（它会如实报告生效了哪些）。
	// 上游加平台时这边不用跟着改。
	if got := NormalizeMusicDLSources([]string{"brandnew"}); len(got) != 1 || got[0] != "brandnew" {
		t.Fatalf("未知平台该原样留着：%v", got)
	}
	if got := NormalizeMusicDLSources(nil); len(got) != 0 {
		t.Fatalf("nil 该得到空切片：%v", got)
	}
}

func TestMusicDLEffectiveValues(t *testing.T) {
	// 条数：非法值回落默认，并**封顶 50** —— musicdl 按这个数量翻页且每条都先
	// 解析直链，给太大整次搜索会超时前交不出东西。
	cases := []struct {
		in, want int
	}{
		{0, MusicDLDefaultLimit}, {-3, MusicDLDefaultLimit}, {10, 10},
		{50, 50}, {999, 50},
	}
	for _, c := range cases {
		if got := (AppConfig{MusicDLLimit: c.in}).MusicDLEffectiveLimit(); got != c.want {
			t.Errorf("limit=%d 应为 %d，得到 %d", c.in, c.want, got)
		}
	}

	// 端口：非法值回落 8901。⚠️ 默认**不是 8899** —— 那是并存的旧「极光音乐」
	// 在用的端口，撞上会很难查。
	if got := (AppConfig{}).MusicDLEffectivePort(); got != MusicDLDefaultPort {
		t.Errorf("默认端口应为 %d，得到 %d", MusicDLDefaultPort, got)
	}
	if got := (AppConfig{MusicDLPort: 99999}).MusicDLEffectivePort(); got != MusicDLDefaultPort {
		t.Errorf("非法端口该回落默认，得到 %d", got)
	}
	if got := (AppConfig{MusicDLPort: 8910}).MusicDLEffectivePort(); got != 8910 {
		t.Errorf("合法端口该原样用，得到 %d", got)
	}
}

func TestLoadKeepsMusicDLSettings(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 8899, "musicdl_enabled": true, "musicdl_sources": ["MG","kw"], "musicdl_limit": 8, "musicdl_port": 8912}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get()
	if got.MusicDLEnabled == nil {
		t.Fatal("load 没有登记 musicdl_enabled：重启后这个开关会自己弹回「关」")
	}
	if !got.MusicDLOn() {
		t.Error("文件里写的是 true，重启后应仍然是开的")
	}
	if len(got.MusicDLSources) != 2 || got.MusicDLSources[0] != "mg" {
		t.Errorf("load 该登记 musicdl_sources 并归一化，得到 %v", got.MusicDLSources)
	}
	if got.MusicDLEffectiveLimit() != 8 || got.MusicDLEffectivePort() != 8912 {
		t.Errorf("load 该登记 limit/port，得到 %d / %d", got.MusicDLEffectiveLimit(), got.MusicDLEffectivePort())
	}
}

func TestUpdatePersistsMusicDLSettings(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 打开
	if err := cm.Update(AppConfig{MusicDLEnabled: boolPtr(true), MusicDLSources: []string{"mg"}}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !cm.Get().MusicDLOn() {
		t.Fatal("保存后该是开的")
	}

	// ⚠️ patch 里**没有** musicdl_enabled 时不能动它 —— 否则「保存别的设置」
	// 会把用户刚打开的 sidecar 关掉。
	if err := cm.Update(AppConfig{VisualizerMode: "bars"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !cm.Get().MusicDLOn() {
		t.Fatal("没提到 musicdl_enabled 的 patch 不该把它关掉（nil ≠ false）")
	}

	// 显式关掉 → 重启（重新 load）后仍然是关的
	if err := cm.Update(AppConfig{MusicDLEnabled: boolPtr(false)}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if cm2.Get().MusicDLOn() {
		t.Fatal("显式关掉之后重启该仍然是关的")
	}
}

// 清空名单 = 「一个外挂平台都不搜」，**不是**「回落到默认源」。
//
// 旧行为（len==0 → 默认源）在真机验收「音源页逐个启停生效」时被证伪：
// 用户在音源页把源全关掉，搜索里**还有咪咕的结果**。
// 想整个关掉 musicdl 请用总开关（`musicdl_enabled`），别拿清空名单当总开关 ——
// 两者是不同的意图，混在一起就会出现「我明明关了它还在搜」。
func TestUpdateEmptySourcesMeansNoSources(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	if err := cm.Update(AppConfig{MusicDLSources: []string{"mg", "bq"}}); err != nil {
		t.Fatal(err)
	}
	if err := cm.Update(AppConfig{MusicDLSources: []string{}}); err != nil {
		t.Fatal(err)
	}
	got := cm.Get().MusicDLActiveSources()
	if len(got) != 0 {
		t.Fatalf("清空名单该是「一个都不搜」，得到 %v", got)
	}
	// 而「从没配过」仍然走默认源（那是另一种意图）
	var never AppConfig
	if def := never.MusicDLActiveSources(); len(def) != len(MusicDLDefaultSources) {
		t.Fatalf("从没配过该用默认源，得到 %v", def)
	}
}

func TestKnownPlatformsAcceptsSidecarPlatforms(t *testing.T) {
	// 主力平台白名单必须认得 sidecar 提供的平台 —— 否则用户在界面上选了咪咕
	// 会被静默丢掉（`PlatformPriority()` 的过滤），表现为「选了没用」。
	for _, p := range MusicDLDefaultSources {
		if !knownPlatforms[p] {
			t.Fatalf("knownPlatforms 该认得 %s（它由 musicdl sidecar 提供）", p)
		}
	}
	got := AppConfig{PreferredPlatforms: []string{"mg", "wy"}}.PlatformPriority()
	if len(got) != 2 || got[0] != "mg" {
		t.Fatalf("主力平台该接受 sidecar 平台：%v", got)
	}
	// 仍然拒绝真正不认识的（这条原来拿 mg 当反例，mg 现在认得了，换成真不存在的）
	if got := (AppConfig{PreferredPlatforms: []string{"nope"}}).PlatformPriority(); len(got) != 2 {
		t.Fatalf("认不出的平台该被丢掉、回落到默认两个：%v", got)
	}
}

func TestMusicDLDefaultLimitStaysInTheOfficialSearchWindow(t *testing.T) {
	// ⚠️ 这条钉的是 v2.1.87 真机上量出来的一个**产品约束**，不是随手定的数：
	//
	//	咪咕实测（同一关键词）：5 条 → 2.1 秒；8 条 → 3.2 秒；12 条 → 7.0 秒
	//
	// 而官方页面搜索合并只等 3 秒（pkg/intercept 的 onlineSearchWait）。默认条数
	// 一旦调大，外挂平台就**永远赶不上那个窗口** —— 现象是「开了外挂音源，却搜不到
	// 咪咕的歌」，而且看不出为什么（日志里只会说「等平台搜索超过 3s」）。
	//
	// 所以这里给默认值设一条上限：调大必须同时改窗口，或者干脆别调大。
	if MusicDLDefaultLimit > 8 {
		t.Fatalf("默认单源条数 %d 太大：实测 12 条要 7 秒，赶不上官方页搜索的 3 秒窗口", MusicDLDefaultLimit)
	}
	if MusicDLDefaultLimit < 1 {
		t.Fatalf("默认单源条数 %d 太小", MusicDLDefaultLimit)
	}
	// 默认名单里不该有实测不可用的平台（千千/B站 >12 秒且 0 条）
	for _, p := range MusicDLDefaultSources {
		if p == "bq" || p == "bi" {
			t.Fatalf("%s 实测超过 12 秒且一条都搜不出来，不该在默认名单里：%v", p, MusicDLDefaultSources)
		}
	}
}
