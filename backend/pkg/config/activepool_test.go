package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 多选激活的启用池（用户 2026-09-30）。这里的重点是**nil 与 `[]` 是两种状态**：
//   · nil  = 从没设置过（老配置）→ 用旧的单值 active_source_id 迁移
//   · []   = 用户显式把全部音源都关掉了 → 必须原样保留，不能又自动启用一个

func TestActiveIDsFallsBackToLegacySingleValue(t *testing.T) {
	legacy := AppConfig{ActiveSourceID: "custom_a"}
	if got := legacy.ActiveIDs(); len(got) != 1 || got[0] != "custom_a" {
		t.Fatalf("老配置应升成单元素池，得到 %v", got)
	}
	empty := AppConfig{}
	if got := empty.ActiveIDs(); got != nil {
		t.Fatalf("从没设置过应返回 nil，得到 %v", got)
	}
}

func TestSetActiveIDsDedupesAndSyncsCompatField(t *testing.T) {
	cfg := AppConfig{}
	cfg.SetActiveIDs([]string{"custom_b", "custom_a", "custom_b", "  "})
	if len(cfg.ActiveSourceIDs) != 2 || cfg.ActiveSourceIDs[0] != "custom_b" || cfg.ActiveSourceIDs[1] != "custom_a" {
		t.Fatalf("应去重且保序，得到 %v", cfg.ActiveSourceIDs)
	}
	// 兼容字段 = 池里优先级最高的那个（旧调用方 / 外部 AI 只认单值）
	if cfg.ActiveSourceID != "custom_b" {
		t.Fatalf("active_source_id 应同步为池首，得到 %q", cfg.ActiveSourceID)
	}

	cfg.SetActiveIDs(nil)
	if len(cfg.ActiveSourceIDs) != 0 || cfg.ActiveSourceIDs == nil {
		t.Fatalf("全部关掉时应是**非 nil 空数组**（区别于'从没设置过'），得到 %#v", cfg.ActiveSourceIDs)
	}
	if cfg.ActiveSourceID != "" {
		t.Fatalf("池空时兼容字段应为空，得到 %q", cfg.ActiveSourceID)
	}
}

func TestActivePoolRoundTripKeepsEmptyDistinctFromMissing(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 用户显式全部关掉
	cfg := cm.Get()
	cfg.SetActiveIDs(nil)
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	// 重新加载：必须还是"全部关掉"，不能自己活过来
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	got := cm2.Get()
	if got.ActiveSourceIDs == nil {
		t.Fatal("空池必须持久化成 []，否则重启后会被当成'从没设置过'而自动启用第一个")
	}
	if len(got.ActiveIDs()) != 0 {
		t.Fatalf("重启后池应仍为空，得到 %v", got.ActiveIDs())
	}
}

func TestLegacyConfigFileMigratesToPool(t *testing.T) {
	dir := t.TempDir()
	// 手写一份老配置：只有单值 active_source_id，没有 active_source_ids
	raw := map[string]any{"port": 8899, "active_source_id": "custom_legacy"}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatalf("写老配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get()
	if len(got.ActiveIDs()) != 1 || got.ActiveIDs()[0] != "custom_legacy" {
		t.Fatalf("老配置应迁移成单元素池，得到 %v", got.ActiveIDs())
	}
}
