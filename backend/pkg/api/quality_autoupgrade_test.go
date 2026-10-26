package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
)

// 后台「音质补强」设置面（pkg/api/quality_autoupgrade.go，v2.22）。
//
// 这一层钉的是**三态语义**：`enabled` / `ignore_cooldown` 用指针接，
// 「没传」与「传了 false」必须能分开 —— 否则用户第一次点关闭时什么都不会发生
// （而「关掉补强」恰恰是这一段最想做的事）。

func newUpgradeTestServer(t *testing.T) (*Server, *config.ConfigManager) {
	t.Helper()
	cm, err := config.NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfgMgr: cm}
	return s, cm
}

func postUpgrade(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/quality/auto-upgrade", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleQualityAutoUpgrade(rec, req)
	return rec
}

// 「改成 false」必须真的落库（与「没传」分开）。
func TestUpgradePostThreeState(t *testing.T) {
	s, cm := newUpgradeTestServer(t)
	if got := postUpgrade(t, s, `{"enabled":true,"ignore_cooldown":true}`); got.Code != http.StatusOK {
		t.Fatalf("该 200，得到 %d：%s", got.Code, got.Body.String())
	}
	if !cm.Get().UpgradeSweepOn() {
		t.Fatal("enabled:true 之后该是开的")
	}
	// 只改别的键：enabled 不能被顺手掀掉
	postUpgrade(t, s, `{"batch":5}`)
	if !cm.Get().UpgradeSweepOn() {
		t.Fatal("patch 里没提 enabled 时不该动它")
	}
	if cm.Get().UpgradeSweepPerRound() != 5 {
		t.Fatalf("batch 该是 5，实际 %d", cm.Get().UpgradeSweepPerRound())
	}
	// 显式关掉
	postUpgrade(t, s, `{"enabled":false}`)
	if cm.Get().UpgradeSweepOn() {
		t.Fatal("enabled:false 之后该关掉")
	}
	if !cm.Get().UpgradeSweepIgnoreCooldownOn() {
		t.Fatal("ignore_cooldown 没被这次 patch 提到，不该被改回去")
	}
}

// 数值夹区间、来源归一、档位归一，都在这一条请求里一次做完。
func TestUpgradePostNormalizes(t *testing.T) {
	s, cm := newUpgradeTestServer(t)
	postUpgrade(t, s, `{"interval_minutes":1,"batch":999,"channels":["native","lx"],"quality":" 320K "}`)
	c := cm.Get()
	if c.UpgradeSweepIntervalMinutes != config.UpgradeSweepMinIntervalMinutes {
		t.Fatalf("间隔该被夹到下限，得到 %d", c.UpgradeSweepIntervalMinutes)
	}
	if c.UpgradeSweepBatch != config.UpgradeSweepMaxBatch {
		t.Fatalf("批量该被夹到上限，得到 %d", c.UpgradeSweepBatch)
	}
	if got := c.DownloadChannels(); len(got) != 2 || got[0] != "native" || got[1] != "lx" {
		t.Fatalf("渠道该按给的顺序存下来，得到 %v", got)
	}
	if c.AutoDownloadQuality != "320k" {
		t.Fatalf("档位该去空白并小写，得到 %q", c.AutoDownloadQuality)
	}
}

// 设置存完之后必须**通知运行态重排**（开关从关到开要重新起循环）。
func TestUpgradePostTriggersReload(t *testing.T) {
	s, _ := newUpgradeTestServer(t)
	called := 0
	s.upgradeReload = func() { called++ }
	postUpgrade(t, s, `{"enabled":true}`)
	if called != 1 {
		t.Fatalf("保存之后该调用一次 reload，实际 %d 次", called)
	}
}

// 拦截层没起来时 GET 给 502 形状的可用性应答（与其它 quality 接口同口径）。
func TestUpgradeGetUnavailable(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/quality/auto-upgrade", nil)
	rec := httptest.NewRecorder()
	s.handleQualityAutoUpgrade(rec, req)
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("该给出「没起来」的应答，得到 %s", rec.Body.String())
	}
}

// /run 把 clearCooldown 原样透给后端，并回报派活条数。
func TestUpgradeRunForwardsNow(t *testing.T) {
	s := &Server{}
	var gotNow bool
	s.upgradeRun = func(now bool) (int, int, string) {
		gotNow = now
		return 3, 12, ""
	}
	req := httptest.NewRequest(http.MethodPost, "/api/quality/auto-upgrade/run", strings.NewReader(`{"now":true}`))
	rec := httptest.NewRecorder()
	s.handleQualityAutoUpgradeRun(rec, req)
	if !gotNow {
		t.Fatal("now:true 该透给补强轮（它据此清冷却位）")
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `"dispatched":3`) {
		t.Fatalf("该回报派活条数，得到 %s", body)
	}
}

// 后端说「没开」时 /run 是 400（不是静默成功）：界面要能说清楚为什么没补。
func TestUpgradeRunReportsRefusal(t *testing.T) {
	s := &Server{}
	s.upgradeRun = func(bool) (int, int, string) { return 0, 0, "补强轮没开" }
	req := httptest.NewRequest(http.MethodPost, "/api/quality/auto-upgrade/run", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleQualityAutoUpgradeRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("该 400，得到 %d", rec.Code)
	}
}
