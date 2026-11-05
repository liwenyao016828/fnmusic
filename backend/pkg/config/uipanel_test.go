package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 注入面板四个功能开关（ui_panel_*_enabled）的三态与持久化。
//
// 口径与 ui_inject_enabled 完全一致（见 uiinject_test.go）：
//  1. 没设置过 = 开（新用户不用先去点一遍开关，老配置文件没有这几个键也照常全开）；
//  2. 显式关掉**重启后仍然是关的**（load 与 Update 是两处独立的合并，两边都要登记）；
//  3. patch 里没这个键时不动原值（nil ≠ false）。
//
// 还有一层：这四个开关只控制**注入面板上看得见的 widget**（试听音质节、
// 曲率助手节、打开曲率按钮、行菜单下载入口），不动曲率助手本体 —— 所以
// 它们特意**不**登记进 feature_switch.go 的开关目录，测试里也就不验那条路。

func allFeatures(on bool) map[string]bool {
	return map[string]bool{
		"quality": on, "assistant": on, "jump": on, "download": on,
	}
}

func TestUIPanelFeaturesDefaultsOn(t *testing.T) {
	cases := []struct {
		name string
		cfg  AppConfig
		want map[string]bool
	}{
		{"没设置过（nil）全开", AppConfig{}, allFeatures(true)},
		{"显式全开", AppConfig{
			UIPanelQualityEnabled:   boolPtr(true),
			UIPanelAssistantEnabled: boolPtr(true),
			UIPanelJumpEnabled:      boolPtr(true),
			UIPanelDownloadEnabled:  boolPtr(true),
		}, allFeatures(true)},
		{"显式全关", AppConfig{
			UIPanelQualityEnabled:   boolPtr(false),
			UIPanelAssistantEnabled: boolPtr(false),
			UIPanelJumpEnabled:      boolPtr(false),
			UIPanelDownloadEnabled:  boolPtr(false),
		}, allFeatures(false)},
		{"只关音质与下载，其余开", AppConfig{
			UIPanelQualityEnabled:  boolPtr(false),
			UIPanelDownloadEnabled: boolPtr(false),
		}, map[string]bool{
			"quality": false, "assistant": true, "jump": true, "download": false,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.cfg.UIPanelFeatures()
			if len(got) != 4 {
				t.Fatalf("map 应恰好 4 个键，得到 %d：%v", len(got), got)
			}
			for k, want := range c.want {
				if got[k] != want {
					t.Errorf("%s 应为 %v，得到 %v", k, want, got[k])
				}
			}
		})
	}
}

// 老配置文件（没有这四个键）读出来必须全开 —— 面板行为与升级前一模一样。
func TestUIPanelFeaturesLegacyConfigAllOn(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 8899}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get().UIPanelFeatures()
	for k, on := range allFeatures(true) {
		if got[k] != on {
			t.Errorf("老配置下 %s 应默认开，得到 %v", k, got[k])
		}
	}
}

func TestLoadKeepsUIPanelSwitches(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 8899,
		"ui_panel_quality_enabled": false,
		"ui_panel_download_enabled": false,
		"ui_panel_assistant_enabled": true,
		"ui_panel_jump_enabled": true}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get().UIPanelFeatures()
	if got["quality"] {
		t.Error("load 没有登记 ui_panel_quality_enabled：重启后这个开关会自己弹回「开」")
	}
	if got["download"] {
		t.Error("load 没有登记 ui_panel_download_enabled：重启后这个开关会自己弹回「开」")
	}
	if !got["assistant"] || !got["jump"] {
		t.Errorf("文件里写 true 的两个键应仍为开：%v", got)
	}
}

func TestUpdatePersistsUIPanelSwitches(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	if !cm.Get().UIPanelFeatures()["quality"] {
		t.Fatal("新装的默认应是开")
	}

	// 1) 关掉两个 → 立刻生效，并且写进文件。
	cfg := cm.Get()
	cfg.UIPanelQualityEnabled = boolPtr(false)
	cfg.UIPanelAssistantEnabled = boolPtr(false)
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	got := cm.Get().UIPanelFeatures()
	if got["quality"] || got["assistant"] {
		t.Fatalf("关掉之后应立刻生效：%v", got)
	}

	// 2) 重新打开进程读回来 —— 仍然是关的（不是「保存成功、重启又开了」）。
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	got = cm2.Get().UIPanelFeatures()
	if got["quality"] || got["assistant"] {
		t.Errorf("重启后应保持关闭：%v", got)
	}
	if !got["jump"] || !got["download"] {
		t.Errorf("没动过的两个键应仍是默认开：%v", got)
	}

	// 3) patch 里不带这些键（nil）时**不动原值** —— 其它设置页保存一次
	//    就把面板开关弹回「开」，等于这组开关随时会失效。
	if err := cm2.Update(AppConfig{Port: 8899}); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	got = cm2.Get().UIPanelFeatures()
	if got["quality"] || got["assistant"] {
		t.Error("patch 没带这些键时不该改动它们（nil ≠ false）")
	}

	// 4) 再显式打开，能回来。
	back := cm2.Get()
	back.UIPanelAssistantEnabled = boolPtr(true)
	if err := cm2.Update(back); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !cm2.Get().UIPanelFeatures()["assistant"] {
		t.Error("显式打开后应恢复")
	}
}
