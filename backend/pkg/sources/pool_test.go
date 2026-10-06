package sources

import (
	"net/http"
	"testing"

	"fn-lx-player/pkg/config"
)

// 多选激活的启用池（用户 2026-09-30）：
//   · active:true  → 追加到池**尾**（池有序 = 取链优先级）
//   · active:false → 从池里摘掉（摘空也允许）
//   · 不带 active  → 旧语义：池里只放它（外部 AI 与老前端仍这么调）

func seedThree(t *testing.T, m *Manager) {
	t.Helper()
	seed(t, m,
		config.CustomSource{ID: "custom_a", Name: "A", Script: "a", CreatedAt: 1},
		config.CustomSource{ID: "custom_b", Name: "B", Script: "b", CreatedAt: 2},
		config.CustomSource{ID: "custom_c", Name: "C", Script: "c", CreatedAt: 3},
	)
}

func TestSetActiveAddsToPoolTail(t *testing.T) {
	m := newTestManager(t)
	seedThree(t, m)

	for _, id := range []string{"custom_a", "custom_c"} {
		code, out := call(t, m, http.MethodPost, "/api/sources/active",
			`{"id":"`+id+`","active":true}`, m.HandleSetActive)
		if code != http.StatusOK {
			t.Fatalf("HTTP %d: %v", code, out)
		}
	}

	got := m.cfgMgr.Get().ActiveIDs()
	if len(got) != 2 || got[0] != "custom_a" || got[1] != "custom_c" {
		t.Fatalf("池应为 [custom_a custom_c]（按点击顺序），得到 %v", got)
	}
	// 兼容字段 = 池首
	if m.cfgMgr.Get().ActiveSourceID != "custom_a" {
		t.Fatalf("active_source_id 应为池首，得到 %q", m.cfgMgr.Get().ActiveSourceID)
	}
}

func TestSetActiveRemoveFromPoolAllowsEmpty(t *testing.T) {
	m := newTestManager(t)
	seedThree(t, m)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_a","active":true}`, m.HandleSetActive)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_b","active":true}`, m.HandleSetActive)

	// 摘掉 a → 只剩 b
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_a","active":false}`, m.HandleSetActive)
	if got := m.cfgMgr.Get().ActiveIDs(); len(got) != 1 || got[0] != "custom_b" {
		t.Fatalf("应只剩 custom_b，得到 %v", got)
	}

	// 再摘掉 b → 允许一个都不启用（用户有权全关）
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_b","active":false}`, m.HandleSetActive)
	cfg := m.cfgMgr.Get()
	if len(cfg.ActiveIDs()) != 0 {
		t.Fatalf("应允许空池，得到 %v", cfg.ActiveIDs())
	}
	if cfg.ActiveSourceIDs == nil {
		t.Fatal("空池必须是非 nil 空数组，否则会被当成'从没设置过'")
	}
}

func TestSetActiveWithoutFlagKeepsLegacySingleSemantics(t *testing.T) {
	m := newTestManager(t)
	seedThree(t, m)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_a","active":true}`, m.HandleSetActive)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_b","active":true}`, m.HandleSetActive)

	// 旧调用方只给 id → 池里只留它
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_c"}`, m.HandleSetActive)
	got := m.cfgMgr.Get().ActiveIDs()
	if len(got) != 1 || got[0] != "custom_c" {
		t.Fatalf("旧语义应把池收敛成单元素，得到 %v", got)
	}
}

func TestListSourcesExposesActivePool(t *testing.T) {
	m := newTestManager(t)
	seedThree(t, m)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_a","active":true}`, m.HandleSetActive)
	call(t, m, http.MethodPost, "/api/sources/active", `{"id":"custom_b","active":true}`, m.HandleSetActive)

	code, out := call(t, m, http.MethodGet, "/api/sources", "", m.HandleListSources)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %v", code, out)
	}
	ids, _ := out["active_source_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("顶层应带 active_source_ids，得到 %v", out["active_source_ids"])
	}
	data := out["data"].(map[string]any)
	ids2, _ := data["active_source_ids"].([]any)
	if len(ids2) != 2 || ids2[0] != "custom_a" {
		t.Fatalf("data 里也应有池且保序，得到 %v", data["active_source_ids"])
	}
	// 兼容字段仍在（外部 AI 只认它）
	if out["active_source_id"] != "custom_a" {
		t.Fatalf("active_source_id 应仍存在且 = 池首，得到 %v", out["active_source_id"])
	}
}

func TestRemoveFromPoolFallsBackWhenBecomesEmpty(t *testing.T) {
	cfg := config.AppConfig{}
	cfg.SetActiveIDs([]string{"x"})
	if !removeFromPool(&cfg, "x", "y") {
		t.Fatal("摘掉存在的 id 应返回 true")
	}
	if got := cfg.ActiveIDs(); len(got) != 1 || got[0] != "y" {
		t.Fatalf("摘空后应兜底启用 fallback，得到 %v", got)
	}
	// 不在池里的 id：不改动
	if removeFromPool(&cfg, "zzz", "y") {
		t.Fatal("摘不存在的 id 不该报告改动")
	}
	// 摘空且没有 fallback（一个源都不剩）→ 允许空
	cfg.SetActiveIDs([]string{"x"})
	removeFromPool(&cfg, "x", "")
	if len(cfg.ActiveIDs()) != 0 {
		t.Fatalf("没有剩余源时应允许空池，得到 %v", cfg.ActiveIDs())
	}
}
