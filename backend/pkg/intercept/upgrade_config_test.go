package intercept

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 「补强轮可配置化」这一层的钉子（v2.22，用户 2026-10-25 m00286）。
//
// 这一批测的是**四个维度真的能改**：开关、节奏（间隔）、批量、以及对冷却位的态度。
// 以前它们全是这个文件旁边的常量，用户既看不见也改不了 —— 于是「自动记录里有一条
// 音质补强」成了唯一的口子：看得见，控制不了。

// 开关独立于「收藏自动下载」：关掉收藏下载不该顺手把补强停掉，反之亦然。
func TestUpgradeSweepSwitchIsIndependent(t *testing.T) {
	h := newHarness(t)
	h.it.localAPI = "http://127.0.0.1:1"
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := registerOnline(h, "ind1", "甲", "乙")
	it := downloadedItemFor(g, p, "甲", "乙")
	it.Quality = "320k"
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}

	// 收藏自动下载关、补强开 → 该补（旧版会在这里返回 0 条，因为 deps 里第一条
	// 就是 favAutoDownloadOn）。
	h.it.favAutoDownload = func() bool { return false }
	h.it.upgradeSweepEnabled = func() bool { return true }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 1 {
		t.Fatalf("补强开关开着就该有候选（哪怕收藏自动下载是关的）：%+v", got)
	}

	// 反过来：收藏下载开、补强关 → 不该补。
	h.it.favAutoDownload = func() bool { return true }
	h.it.upgradeSweepEnabled = func() bool { return false }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 0 {
		t.Fatalf("补强开关关着就不该有候选（哪怕收藏自动下载是开的）：%+v", got)
	}
}

// 一轮几首是可以调的：给 5 就该派 5。
func TestUpgradeSweepBatchIsConfigurable(t *testing.T) {
	h := newHarness(t)
	h.it.qrPersist = false
	h.it.qrPersistSet = true
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.upgradeSweepEnabled = func() bool { return true }
	h.it.upgradeSweepBatchFn = func() int { return 5 }
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		id := string(rune('a'+i)) + "cfg"
		p := filepath.Join(dir, id+".mp3")
		if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		g := registerOnline(h, id, "歌"+id, "乙")
		it := downloadedItemFor(g, p, "歌"+id, "乙")
		it.Quality = "320k"
		if err := h.it.downloaded.Put(it); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.it.upgradeSweepOnce(time.Now()); n != 5 {
		t.Fatalf("批量设成 5 就该正好派 5 首，实际 %d", n)
	}
}

// 「无视冷却」打开时，冷却没到的也要参与补。
func TestUpgradeSweepIgnoreCooldown(t *testing.T) {
	h := newHarness(t)
	h.it.localAPI = "http://127.0.0.1:1"
	h.it.upgradeSweepEnabled = func() bool { return true }
	h.it.upgradeIgnoreCooldownFn = func() bool { return false } // 先按默认：不看冷却
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	p := filepath.Join(dir, "cooling.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := registerOnline(h, "cool1", "甲", "乙")
	it := downloadedItemFor(g, p, "甲", "乙")
	it.Quality = "320k"
	it.UpgradeTried = true // 老版本只写这个布尔位，判据也认它
	it.UpgradeTries = 1
	it.UpgradeNext = time.Now().Add(time.Hour).Unix() // 还在冷却里
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if got := h.it.upgradeCandidates(now); len(got) != 0 {
		t.Fatalf("没开「无视冷却」时冷却中的不该参与：%+v", got)
	}
	h.it.upgradeIgnoreCooldownFn = func() bool { return true }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 1 {
		t.Fatalf("开了「无视冷却」之后该参与：%+v", got)
	}
}

// 欠账计数要分得清「等得起」与「在冷却中」：界面上写的是
// 「欠 N 首（其中 M 首在冷却中）」，两个数都来自这里。
func TestUpgradePendingCountSplitsCooling(t *testing.T) {
	h := newHarness(t)
	h.it.localAPI = "http://127.0.0.1:1"
	h.it.upgradeSweepEnabled = func() bool { return true }

	dir := t.TempDir()
	mk := func(id string, quality string, tries int, next int64) {
		p := filepath.Join(dir, id+".mp3")
		if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		g := registerOnline(h, id, "歌"+id, "乙")
		it := downloadedItemFor(g, p, "歌"+id, "乙")
		it.Quality = quality
		it.UpgradeTries = tries
		it.UpgradeNext = next
		if err := h.it.downloaded.Put(it); err != nil {
			t.Fatal(err)
		}
	}
	mk("p1", "320k", 0, 0)                                // 等得起
	mk("p2", "320k", 1, time.Now().Add(time.Hour).Unix()) // 冷却中
	mk("p3", "flac", 0, 0)                                // 已经是无损
	pending, cooling := h.it.UpgradePendingCount()
	if pending != 2 || cooling != 1 {
		t.Fatalf("该是「欠 2 首、其中 1 首冷却中」，实际 (%d, %d)", pending, cooling)
	}
}

// 手动补一轮会把冷却位清掉（用户点「立即补一轮」的意思就是「再试一次」），
// 但**不动** UpgradeTries（总次数上限仍然有效，否则就是无限重试）。
func TestUpgradeSweepNowClearsCooldownKeepsTries(t *testing.T) {
	h := newHarness(t)
	h.it.qrPersist = false
	h.it.qrPersistSet = true
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.upgradeSweepEnabled = func() bool { return true }
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	p := filepath.Join(dir, "m1.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := registerOnline(h, "m1", "甲", "乙")
	it := downloadedItemFor(g, p, "甲", "乙")
	it.Quality = "320k"
	it.UpgradeTries = 2
	it.UpgradeNext = time.Now().Add(20 * time.Hour).Unix()
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}

	n, _, msg := h.it.UpgradeSweepNow(true)
	if msg != "" {
		t.Fatalf("不该报错：%s", msg)
	}
	if n != 1 {
		t.Fatalf("该派出 1 首，实际 %d", n)
	}
	got, _ := h.it.downloaded.Get(g)
	if got.UpgradeTries < 2 {
		t.Fatalf("UpgradeTries 不该被清掉（总次数上限要留着）：%d", got.UpgradeTries)
	}
}

// 补强轮起停：Start 两次只有一个在跑，Close 之后就没了（幂等）。
func TestUpgradeSweeperStartStop(t *testing.T) {
	h := newHarness(t)
	h.it.upgradeSweepEnabled = func() bool { return false } // 循环醒来也不派活
	h.it.StartUpgradeSweeper()
	h.it.StartUpgradeSweeper() // 第二次必须是空操作
	if !h.it.UpgradeSweeperRunning() {
		t.Fatal("Start 之后该在跑")
	}
	h.it.CloseUpgradeSweeper()
	if h.it.UpgradeSweeperRunning() {
		t.Fatal("Close 之后不该再在跑")
	}
	h.it.CloseUpgradeSweeper() // 再关一次也不能 panic
}
