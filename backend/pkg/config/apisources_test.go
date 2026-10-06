package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Update 是**选择性合并**：只有显式处理的字段才会被写回。
// 漏处理的字段会「保存成功但读出来还是空的」—— APISources 就踩过这个坑
// （表现为接入音源返回成功、列表却是空）。
//
// 这个用例把「新增字段必须登记到 Update」这件事钉住：
// 以后再加字段时，照着这里补一条即可。
func TestUpdatePersistsAPISources(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 新增
	cfg := cm.Get()
	cfg.APISources = []APISource{{
		ID: "api_1", Name: "自建音源", BaseURL: "http://10.0.0.5:8080",
		Protocol: "lx_server", Enabled: true, Note: "解析接口正常",
	}}
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	got := cm.Get().APISources
	if len(got) != 1 || got[0].ID != "api_1" {
		t.Fatalf("内存里没写进去：%+v", got)
	}

	// 重新打开，确认真的落盘了（而不是只在内存里）
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	reloaded := cm2.Get().APISources
	if len(reloaded) != 1 {
		t.Fatalf("落盘后读回 %d 条，期望 1 条", len(reloaded))
	}
	if reloaded[0].BaseURL != "http://10.0.0.5:8080" || reloaded[0].Protocol != "lx_server" {
		t.Errorf("字段没保住：%+v", reloaded[0])
	}

	// 空切片 = 清空（不是「不动」）
	cfg2 := cm2.Get()
	cfg2.APISources = []APISource{}
	if err := cm2.Update(cfg2); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if n := len(cm2.Get().APISources); n != 0 {
		t.Errorf("空切片应清空，实际还剩 %d 条", n)
	}

	// nil = 不动
	cfg3 := cm2.Get()
	cfg3.APISources = []APISource{{ID: "api_2", BaseURL: "http://x"}}
	_ = cm2.Update(cfg3)
	cfg4 := cm2.Get()
	cfg4.APISources = nil
	_ = cm2.Update(cfg4)
	if n := len(cm2.Get().APISources); n != 1 {
		t.Errorf("nil 表示不动，应保留 1 条，实际 %d 条", n)
	}
}

// 配置文件里出现 APISources 时不该被读丢
func TestLoadKeepsAPISources(t *testing.T) {
	dir := t.TempDir()
	raw := `{
	  "port": 8899,
	  "api_sources": [
	    {"id":"api_x","name":"n","base_url":"http://a","protocol":"lx_api","enabled":true}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get().APISources
	if len(got) != 1 || got[0].ID != "api_x" || got[0].Protocol != "lx_api" {
		t.Fatalf("读回不对：%+v", got)
	}
}
