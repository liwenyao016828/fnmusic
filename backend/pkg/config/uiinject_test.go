package config

import (
	"os"
	"path/filepath"
	"testing"
)

// `ui_inject_enabled` 是「往飞牛官方页面注入脚本」的一键退路。
//
// 这个开关的价值全在**它真的能关**：注入改的是别人家的页面，万一某次官方改版
// 被我们插的一行脚本弄坏了，用户需要能在不卸载、不回滚版本的前提下让它立刻消失。
// 所以这里钉三件事：
//  1. 没设置过 = 开（新用户不用先去点一下开关）；
//  2. 显式关掉**重启后仍然是关的**（load 与 Update 是两处独立的合并，两边都要登记 ——
//     `PreferredPlatforms` 真的漏过一次，症状是「改完能用、重启变回默认」）；
//  3. patch 里没这个键时不动原值（nil ≠ false）。
func TestInjectUIEnabledDefaultsOn(t *testing.T) {
	cases := []struct {
		name string
		cfg  AppConfig
		want bool
	}{
		{"没设置过（nil）视为开", AppConfig{}, true},
		{"显式开", AppConfig{UIInjectEnabled: boolPtr(true)}, true},
		{"显式关", AppConfig{UIInjectEnabled: boolPtr(false)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.InjectUIEnabled(); got != c.want {
				t.Errorf("InjectUIEnabled() 应为 %v，得到 %v", c.want, got)
			}
		})
	}
}

func TestLoadKeepsUIInjectSwitch(t *testing.T) {
	dir := t.TempDir()
	raw := `{"port": 8899, "ui_inject_enabled": false}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cm.Get().UIInjectEnabled == nil {
		t.Fatal("load 没有登记 ui_inject_enabled：重启后这个开关会自己弹回「开」")
	}
	if cm.Get().InjectUIEnabled() {
		t.Error("文件里写的是 false，重启后应仍然是关的")
	}
}

func TestUpdatePersistsUIInjectSwitch(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	if !cm.Get().InjectUIEnabled() {
		t.Fatal("新装的默认应是开")
	}

	// 1) 关掉 → 立刻生效，并且写进文件。
	cfg := cm.Get()
	cfg.UIInjectEnabled = boolPtr(false)
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if cm.Get().InjectUIEnabled() {
		t.Fatal("关掉之后应立刻生效")
	}

	// 2) 重新打开进程读回来 —— 仍然是关的（不是「保存成功、重启又开了」）。
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if cm2.Get().InjectUIEnabled() {
		t.Error("重启后应保持关闭")
	}

	// 3) patch 里不带这个键（nil）时**不动原值** —— 别的设置页保存一次
	// 就把退路开关弹回「开」，等于这个退路随时会失效。
	patch := AppConfig{Port: 8899}
	if err := cm2.Update(patch); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if cm2.Get().InjectUIEnabled() {
		t.Error("patch 没带这个键时不该改动它（nil ≠ false）")
	}

	// 4) 再显式打开，能回来。
	back := cm2.Get()
	back.UIInjectEnabled = boolPtr(true)
	if err := cm2.Update(back); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !cm2.Get().InjectUIEnabled() {
		t.Error("显式打开后应恢复")
	}
}

func boolPtr(v bool) *bool { return &v }
