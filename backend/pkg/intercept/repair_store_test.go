package intercept

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepairScanSnapshotRoundTrip 落盘 → 读回：换过曲库目录的快照不能拿来算新目录的进度。
//
// 这条是「已修复 xx / 已评估 xx」的地基：分母来自快照的 Scanned。
// 一旦把 Dir 忽略掉，用户换了曲库目录后会看到上一份快照的数字 —— 分母错、分子也不对，
// 而且看起来完全正常（有一组自洽的假数字），最坏的一类 bug。
func TestRepairScanSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	i := &Interceptor{cfg: Config{DataDir: dir}}

	res := RepairScanResult{
		Dir:     "/vol1/Music",
		Scanned: 42,
		Issues: []RepairIssue{
			{Path: "/vol1/Music/a.mp3", Kind: "too_short"},
			{Path: "/vol1/Music/sub/b.flac", Kind: "fake_lossless"},
		},
	}
	i.saveRepairScan(res)

	scanned, pending, at, known := i.AudioRepairFacts("/vol1/Music")
	if !known {
		t.Fatal("刚存过快照，known 不应为 false")
	}
	if scanned != 42 || pending != 2 {
		t.Fatalf("scanned/pending = %d/%d，期望 42/2", scanned, pending)
	}
	if at <= 0 {
		t.Fatalf("时间戳 = %d，期望正数", at)
	}

	// 换目录：宁可说「没扫过」，也不能给一份别的目录的数字。
	if _, _, _, known := i.AudioRepairFacts("/vol1/Other"); known {
		t.Fatal("目录对不上时不应返回可用的快照")
	}

	// 末尾斜杠不该让同一目录变成两份。
	if _, _, _, known := i.AudioRepairFacts("/vol1/Music/"); !known {
		t.Fatal("末尾斜杠应当被归一化")
	}
}

// TestRepairScanSnapshotMissing 没有快照时 known=false（界面显示「待扫描」，而不是 0/0）。
func TestRepairScanSnapshotMissing(t *testing.T) {
	i := &Interceptor{cfg: Config{DataDir: t.TempDir()}}
	if _, _, _, known := i.AudioRepairFacts("/vol1/Music"); known {
		t.Fatal("没有快照时 known 应为 false")
	}
}

// TestQualityFactsCountsUpgradeable 音质口径：分母只算**扫过**的歌，分子只算还能升级的。
func TestQualityFactsCountsUpgradeable(t *testing.T) {
	dir := t.TempDir()
	i := &Interceptor{cfg: Config{DataDir: dir}, qualityStore: newQualityScanStore(dir)}
	i.qualityStore.put(QualityEntry{Path: "/vol1/Music/a.mp3", CurrentTier: "128k", Upgradeable: true, ScannedAt: 100})
	i.qualityStore.put(QualityEntry{Path: "/vol1/Music/b.mp3", CurrentTier: "flac", Upgradeable: false, ScannedAt: 200})
	i.qualityStore.put(QualityEntry{Path: "/vol1/Other/c.mp3", CurrentTier: "128k", Upgradeable: true, ScannedAt: 300})

	covered, pending, at, known := i.QualityFacts("/vol1/Music")
	if !known {
		t.Fatal("有结论时 known 应为 true")
	}
	if covered != 2 || pending != 1 {
		t.Fatalf("covered/pending = %d/%d，期望 2/1（别的目录那条不能算进来）", covered, pending)
	}
	if at != 200 {
		t.Fatalf("at = %d，期望 200（取该目录下最新的扫描时间）", at)
	}
}

// TestUnderDir 目录归属判断：前缀像不等于在里面（/vol1/Music2 不是 /vol1/Music 的子目录）。
func TestUnderDir(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/vol1/Music/a.mp3", "/vol1/Music", true},
		{"/vol1/Music/sub/a.mp3", "/vol1/Music/", true},
		{"/vol1/Music2/a.mp3", "/vol1/Music", false},
		{"/vol1/Music", "/vol1/Music", true},
		{"/anything", "", true},
	}
	for _, c := range cases {
		if got := underDir(c.path, c.dir); got != c.want {
			t.Fatalf("underDir(%q, %q) = %v，期望 %v", c.path, c.dir, got, c.want)
		}
	}
}

// TestRepairScanSnapshotSurvivesBadFile 半个 JSON / 版本号不对，都当作「没扫过」。
func TestRepairScanSnapshotSurvivesBadFile(t *testing.T) {
	dir := t.TempDir()
	i := &Interceptor{cfg: Config{DataDir: dir}}

	if err := os.WriteFile(filepath.Join(dir, "repair_scan.json"), []byte(`{"version":1,"dir":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, known := i.AudioRepairFacts("/vol1/Music"); known {
		t.Fatal("坏文件不应被当成可用快照")
	}
}