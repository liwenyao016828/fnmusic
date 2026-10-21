package intercept

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
)

// 补强轮的候选筛选（`upgrade_sweeper.go` 的 upgradeCandidates）。
//
// 这里**不**测派活：autoDownload 自身的语义在 autodownload_test.go 里已经钉过，
// 这一层要钉的是「谁该补、谁不该补、谁先补」—— 也就是那一串 continue 的判据。
func TestUpgradeCandidatesFilters(t *testing.T) {
	h := newHarness(t)
	h.it.favAutoDownload = func() bool { return true }
	h.it.localAPI = "http://127.0.0.1:1" // 只要非空（真实实现由 main.go 注入本机回环）
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	mp3 := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	guid := func(id string) string { return registerOnline(h, id, "歌"+id, "乙") }

	put := func(g, path, quality string, tries int, next int64) {
		it := downloadedItemFor(g, path, "歌", "乙")
		it.Quality = quality
		it.UpgradeTries = tries
		it.UpgradeNext = next
		it.UpgradeTried = tries > 0 || next > 0
		if err := h.it.downloaded.Put(it); err != nil {
			t.Fatal(err)
		}
	}

	gWant := guid("u1") // 从没试过 → 该补
	gDue := guid("u2")  // 冷却过了 → 该补
	gSoon := guid("u3") // 冷却没到 → 不补
	gMax := guid("u4")  // 试满上限 → 不补
	gFlac := guid("u5") // 盘上已是无损 → 不补
	gGone := guid("u6") // 文件没了 → 不补（那是「重新下载」的事）
	gLie := guid("u7")  // 记录说无损、盘上是 mp3 → 不补（按记录算，别反复折腾）

	now := time.Now()
	put(gWant, mp3("want.mp3"), "320k", 0, 0)
	put(gDue, mp3("due.mp3"), "320k", 2, now.Add(-time.Hour).Unix())
	put(gSoon, mp3("soon.mp3"), "320k", 1, now.Add(time.Hour).Unix())
	put(gMax, mp3("max.mp3"), "320k", upgradeMaxTries, now.Add(-time.Hour).Unix())
	put(gFlac, mp3("real.flac"), "320k", 0, 0)
	// 「文件没了」这条要**先建再删**：登记表 Put 时会 os.Stat 校验路径存在，
	// 直接拿一个没建过的路径会先撞上 Put 的校验，测不到候选筛选那一层。
	gonePath := mp3("gone.mp3")
	put(gGone, gonePath, "320k", 0, 0)
	if err := os.Remove(gonePath); err != nil {
		t.Fatal(err)
	}
	put(gLie, mp3("lie.mp3"), online.TierLossless, 0, 0)

	got := h.it.upgradeCandidates(now)
	if len(got) != 2 {
		t.Fatalf("该只有 2 条候选，得到 %d：%+v", len(got), got)
	}
	// 从没试过的（UpgradeNext=0）排最前：新欠账优先，别让它排在试过多天的老账后面。
	if got[0].GUID != gWant || got[1].GUID != gDue {
		t.Fatalf("顺序该是「欠得最早的第 0、刚冷却的第 1」：%+v %+v", got[0], got[1])
	}
}

// 登记表里出现过的 guid 如果已经不在 registry（用户把这首歌从收藏/歌单里删了），
// 后台**不该**再替它补 —— 这是 upgradeCandidates 里那条 Lookup 判据的存在理由。
func TestUpgradeCandidatesSkipUnregistered(t *testing.T) {
	h := newHarness(t)
	h.it.favAutoDownload = func() bool { return true }
	h.it.localAPI = "http://127.0.0.1:1"
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	p := filepath.Join(dir, "ghost.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := downloadedItemFor("guid-nobody-knows", p, "幽灵", "乙")
	it.Quality = "320k"
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 0 {
		t.Fatalf("查不到曲目描述符的不该派活：%+v", got)
	}
}

// 开关与档位偏好是这一层的两道总闸：任一不合就不该有候选。
func TestUpgradeCandidatesGates(t *testing.T) {
	h := newHarness(t)
	h.it.localAPI = "http://127.0.0.1:1"

	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := registerOnline(h, "g1", "甲", "乙")
	it := downloadedItemFor(g, p, "甲", "乙")
	it.Quality = "320k"
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}

	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }
	h.it.favAutoDownload = func() bool { return false }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 0 {
		t.Fatal("收藏自动下载开关关着时不该补强")
	}
	h.it.favAutoDownload = func() bool { return true }
	h.it.cfg.AutoDownloadQuality = func() string { return "320k" }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 0 {
		t.Fatal("用户点名 320k（省流量）时后台不该去追无损")
	}
	// 关掉档位偏好也不该顺手把开关本身弄坏：调回来就该又有候选。
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 1 {
		t.Fatalf("档位回到 highest 之后该重新有候选：%+v", got)
	}
}

// 一轮最多派 upgradeSweepBatch 首（探测一次几十秒，一轮补太多会把前台收藏挤没了）。
func TestUpgradeSweepOnceCapsBatch(t *testing.T) {
	h := newHarness(t)
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	for i := 0; i < upgradeSweepBatch+2; i++ {
		id := string(rune('a'+i)) + "sweep"
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

	if n := h.it.upgradeSweepOnce(time.Now()); n != upgradeSweepBatch {
		t.Fatalf("该正好派 %d 首，实际 %d", upgradeSweepBatch, n)
	}

	// 派出去的必须当场把冷却记上 —— 否则下一轮（20 分钟后）会把同一批再补一遍。
	// 这一步在 autoDownload 里、起 goroutine **之前**就做，所以不用等下载结束。
	tried := 0
	for _, it := range h.it.downloaded.All() {
		if it.UpgradeTries == 1 && it.UpgradeNext > time.Now().Unix() {
			tried++
		}
	}
	if tried != upgradeSweepBatch {
		t.Fatalf("记了冷却的该有 %d 条，实际 %d：%+v", upgradeSweepBatch, tried, h.it.downloaded.All())
	}
}

// 冷却位的长度是「一天」这件事得有个人钉着：它是 upgradeMaxTries 之外的第二个
// 流量闸门，改成分钟级会让补强变成持续外呼。
func TestUpgradeCooldownLength(t *testing.T) {
	if upgradeRetryCooldown < 12*time.Hour {
		t.Fatalf("冷却短于 12 小时就等于在频繁外呼第三方站：%v", upgradeRetryCooldown)
	}
	if upgradeRetryCooldown > 7*24*time.Hour {
		t.Fatalf("冷却长于 7 天就等于不回应用户「慢一点没关系」：%v", upgradeRetryCooldown)
	}
}
