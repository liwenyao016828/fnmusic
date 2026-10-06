package sources

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
)

func call(t *testing.T, m *Manager, method, path, body string, h http.HandlerFunc) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func seed(t *testing.T, m *Manager, sources ...config.CustomSource) {
	t.Helper()
	cfg := m.cfgMgr.Get()
	cfg.CustomSources = append(cfg.CustomSources, sources...)
	if cfg.ActiveSourceID == "" && len(cfg.CustomSources) > 0 {
		cfg.ActiveSourceID = cfg.CustomSources[0].ID
	}
	if err := m.cfgMgr.Update(cfg); err != nil {
		t.Fatalf("写入种子数据失败: %v", err)
	}
}

func TestBatchDeleteRemovesAndResetsActive(t *testing.T) {
	m := newTestManager(t)
	seed(t, m,
		config.CustomSource{ID: "custom_a", Name: "A", Script: "a", CreatedAt: 1},
		config.CustomSource{ID: "custom_b", Name: "B", Script: "b", CreatedAt: 2},
		config.CustomSource{ID: "custom_c", Name: "C", Script: "c", CreatedAt: 3},
	)
	// 激活的是 A，删掉 A 与 C
	code, out := call(t, m, http.MethodPost, "/api/sources/batch_delete",
		`{"ids":["custom_a","custom_c"]}`, m.HandleBatchDelete)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %v", code, out)
	}
	data := out["data"].(map[string]any)
	if data["deleted_count"].(float64) != 2 {
		t.Errorf("deleted_count = %v", data["deleted_count"])
	}
	if data["active_reset"] != true {
		t.Error("删除激活音源后应标记 active_reset")
	}
	if data["active_source_id"] != "custom_b" {
		t.Errorf("激活音源应改选 custom_b，实际 %v", data["active_source_id"])
	}
	if data["remaining"].(float64) != 1 {
		t.Errorf("remaining = %v", data["remaining"])
	}

	cfg := m.cfgMgr.Get()
	if len(cfg.CustomSources) != 1 || cfg.CustomSources[0].ID != "custom_b" {
		t.Fatalf("落盘结果不对: %+v", cfg.CustomSources)
	}
}

func TestBatchDeleteReportsUnknownIDs(t *testing.T) {
	m := newTestManager(t)
	seed(t, m, config.CustomSource{ID: "custom_a", Name: "A", Script: "a", CreatedAt: 1})

	_, out := call(t, m, http.MethodPost, "/api/sources/batch_delete",
		`{"ids":["custom_a","custom_missing"]}`, m.HandleBatchDelete)
	data := out["data"].(map[string]any)
	nf := data["not_found"].([]any)
	if len(nf) != 1 || nf[0] != "custom_missing" {
		t.Errorf("应报告未找到的 ID，实际 %v", nf)
	}
}

func TestBatchDeleteRequiresIDs(t *testing.T) {
	m := newTestManager(t)
	code, out := call(t, m, http.MethodPost, "/api/sources/batch_delete", `{}`, m.HandleBatchDelete)
	if code != http.StatusBadRequest {
		t.Fatalf("应 400，实际 %d", code)
	}
	if !strings.Contains(out["message"].(string), "ids") {
		t.Errorf("错误信息应说明缺 ids，实际 %v", out["message"])
	}
}

func TestExportProducesReimportableScripts(t *testing.T) {
	m := newTestManager(t)
	seed(t, m,
		config.CustomSource{ID: "custom_a", Name: "音源A", Version: "1.0", Author: "甲", Script: fakeLXScript("A"), CreatedAt: 1},
		config.CustomSource{ID: "custom_b", Name: "音源B", Script: fakeLXScript("B"), CreatedAt: 2},
	)

	_, out := call(t, m, http.MethodGet, "/api/sources/export", "", m.HandleExportSources)
	data := out["data"].(map[string]any)
	if data["count"].(float64) != 2 {
		t.Fatalf("count = %v", data["count"])
	}

	// scripts 字段应能直接喂给 import_batch 还原
	scripts := data["scripts"].([]any)
	if len(scripts) != 2 {
		t.Fatalf("scripts 应有 2 条，实际 %d", len(scripts))
	}
	first := scripts[0].(map[string]any)
	if first["content"] != fakeLXScript("A") || first["name"] != "音源A" {
		t.Errorf("导出内容不对: %v", first)
	}

	// 还原到新管理器：内容相同，ID 应与原来一致（内容哈希稳定）
	m2 := newTestManager(t)
	bodyBytes, _ := json.Marshal(map[string]any{"scripts": scripts})
	_, out2 := call(t, m2, http.MethodPost, "/api/sources/import_batch", string(bodyBytes), m2.HandleImportBatch)
	if out2["data"].(map[string]any)["imported"].(float64) != 2 {
		t.Fatalf("还原应导入 2 条，实际 %v", out2["data"])
	}
	restored := m2.cfgMgr.Get().CustomSources
	for _, cs := range restored {
		if cs.ID != scriptID(cs.Script) {
			t.Errorf("还原后 ID 应为内容哈希，实际 %s", cs.ID)
		}
	}
}

func TestCleanupDefaultsToDryRun(t *testing.T) {
	m := newTestManager(t)
	// 同内容两条（模拟历史数据：一条时间戳 ID、一条哈希 ID）
	seed(t, m,
		config.CustomSource{ID: "custom_1700000000", Name: "旧", Script: "same-content", CreatedAt: 100},
		config.CustomSource{ID: scriptID("same-content"), Name: "新", Script: "same-content", CreatedAt: 200},
		config.CustomSource{ID: "custom_unique", Name: "唯一", Script: "other-content", CreatedAt: 300},
	)

	// 不传 dry_run → 应默认为干跑，不删任何东西
	code, out := call(t, m, http.MethodPost, "/api/sources/cleanup", `{}`, m.HandleCleanupSources)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %v", code, out)
	}
	data := out["data"].(map[string]any)
	if data["dry_run"] != true {
		t.Error("未指定时 dry_run 应默认为 true")
	}
	if data["duplicate_count"].(float64) != 1 {
		t.Errorf("应发现 1 个重复，实际 %v", data["duplicate_count"])
	}
	if n := len(m.cfgMgr.Get().CustomSources); n != 3 {
		t.Fatalf("干跑不应删除，实际剩 %d 条", n)
	}

	// 保留的是最早的一条
	groups := data["duplicate_groups"].([]any)
	keep := groups[0].(map[string]any)["keep"].(map[string]any)
	if keep["id"] != "custom_1700000000" {
		t.Errorf("应保留创建时间最早的一条，实际 %v", keep["id"])
	}
}

func TestCleanupExecutesWhenDryRunFalse(t *testing.T) {
	m := newTestManager(t)
	seed(t, m,
		config.CustomSource{ID: "custom_old", Name: "旧", Script: "dup", CreatedAt: 100},
		config.CustomSource{ID: "custom_new", Name: "新", Script: "dup", CreatedAt: 200},
	)

	_, out := call(t, m, http.MethodPost, "/api/sources/cleanup", `{"dry_run":false}`, m.HandleCleanupSources)
	data := out["data"].(map[string]any)
	if data["dry_run"] != false {
		t.Error("dry_run 应为 false")
	}
	if data["remaining"].(float64) != 1 {
		t.Fatalf("应剩 1 条，实际 %v", data["remaining"])
	}
	cfg := m.cfgMgr.Get()
	if len(cfg.CustomSources) != 1 || cfg.CustomSources[0].ID != "custom_old" {
		t.Fatalf("应保留最早的 custom_old，实际 %+v", cfg.CustomSources)
	}
}

func TestCleanupNoDuplicates(t *testing.T) {
	m := newTestManager(t)
	seed(t, m,
		config.CustomSource{ID: "custom_a", Name: "A", Script: "aaa", CreatedAt: 1},
		config.CustomSource{ID: "custom_b", Name: "B", Script: "bbb", CreatedAt: 2},
	)
	_, out := call(t, m, http.MethodPost, "/api/sources/cleanup", `{}`, m.HandleCleanupSources)
	data := out["data"].(map[string]any)
	if data["duplicate_count"].(float64) != 0 {
		t.Errorf("无重复时 duplicate_count 应为 0，实际 %v", data["duplicate_count"])
	}
	if n := len(m.cfgMgr.Get().CustomSources); n != 2 {
		t.Fatalf("不应删除任何音源，实际剩 %d", n)
	}
}
