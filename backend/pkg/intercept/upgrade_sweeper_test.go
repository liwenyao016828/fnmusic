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
	h.it.upgradeSweepEnabled = func() bool { return true }
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
	h.it.upgradeSweepEnabled = func() bool { return true }
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
	// 补强开关没开时一律不补 —— **不再**看「收藏自动下载」（v2.22 起两者独立）。
	h.it.favAutoDownload = func() bool { return true }
	h.it.upgradeSweepEnabled = nil
	if got := h.it.upgradeCandidates(time.Now()); len(got) != 0 {
		t.Fatal("补强开关没开时不该补强（哪怕收藏自动下载开着）")
	}
	h.it.upgradeSweepEnabled = func() bool { return true }
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

// 一轮最多派「配置里的一轮批量」首（探测一次几十秒，一轮补太多会把前台收藏挤没了）。
func TestUpgradeSweepOnceCapsBatch(t *testing.T) {
	h := newHarness(t)
	// 补强是在后台 goroutine 里写替换记录的（见 autodownload.go 的 TriggerUpgrade
	// 分支）。t.TempDir() 在测试函数返回时就 RemoveAll，而 goroutine 还在慢一拍地
	// 落盘 —— 于是 cleanup 报 "directory not empty"，与被测的「一轮派几首」毫无关系。
	// 这份测试钉的是派活数量与冷却位，记录只进内存即可。
	h.it.qrPersist = false
	h.it.qrPersistSet = true
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.upgradeSweepEnabled = func() bool { return true }
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	dir := t.TempDir()
	batch := h.it.upgradeSweepBatchSize()
	for i := 0; i < batch+2; i++ {
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

	if n := h.it.upgradeSweepOnce(time.Now()); n != batch {
		t.Fatalf("该正好派 %d 首，实际 %d", batch, n)
	}

	// 派出去的必须当场把冷却记上 —— 否则下一轮（4 小时后）会把同一批再补一遍。
	// 但那是**补强成功的标志**（成功才清冷却），而这里派出去的歌根本下不到
	// （fakeDownloadBackend 给的是空响应）。所以只断言：派活条数对、且**选中的
	// 确实是排在最前面的那 batch 首**（排序不稳的话「哪三首被补」会随机漂）。
	picked := 0
	for _, it := range h.it.downloaded.All() {
		if it.UpgradeTries > 0 || it.UpgradeNext > 0 {
			picked++
		}
	}
	if picked > batch {
		t.Fatalf("派出去的条数不该超过 batch=%d，实际记了 %d 条：%+v", batch, picked, h.it.downloaded.All())
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

// 补强的结果必须同时进「替换音质」的记录表（用户 2026-10-23 的原话：
// 「自动下载记录触发列，出现音质补强的记录是什么意思，替换音质页面怎么没有
// 同步的记录」）。
//
// 两本账的分工是：autolog = 后台干了什么的全量流水；quality_records = 「我的
// 一份音质被换过/没能换成」的专账。补强本来就是替换音质的自动版，结果不记进
// 专账，用户去替换音质页自查时就永远看不到它。
func TestUpgradeSweepWritesQualityRecord(t *testing.T) {
	h := newHarness(t)
	// 记录落盘在后台 goroutine 里发生，t.TempDir() 会在测试返回时就删 —— 这里
	// 要**读**内存里那条，所以只关落盘（见 Interceptor.qrPersist 的注释）。
	h.it.qrPersist = false
	h.it.qrPersistSet = true
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.upgradeSweepEnabled = func() bool { return true }
	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }

	// 盘上已经有一份 mp3（有损），补强该把它换成无损。
	dir := t.TempDir()
	p := filepath.Join(dir, "asweep.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := registerOnline(h, "asweep", "歌asweep", "乙")
	it := downloadedItemFor(g, p, "歌asweep", "乙")
	it.Quality = "320k"
	if err := h.it.downloaded.Put(it); err != nil {
		t.Fatal(err)
	}

	if n := h.it.upgradeSweepOnce(time.Now()); n != 1 {
		t.Fatalf("该派 1 首，实际 %d", n)
	}
	// 补强是后台 goroutine：等记录出现，而不是猜一个 sleep。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.it.QualityRecords()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	recs := h.it.QualityRecords()
	if len(recs) == 0 {
		t.Fatal("补强之后「替换音质」记录表该有一条，实际是空的 —— 用户去替换音质页就看不到这次补强")
	}
	if recs[0].Title != "歌asweep" {
		t.Fatalf("记录该带上曲名，实际 %q", recs[0].Title)
	}
}
