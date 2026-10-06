package library

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFindsAudioAndCountsNonAudio(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.mp3"), 100)
	writeFile(t, filepath.Join(root, "sub", "b.flac"), 200)
	writeFile(t, filepath.Join(root, "readme.txt"), 10)
	writeFile(t, filepath.Join(root, "sub", "cover.jpg"), 20)

	seen, audit := Scan([]string{root}, ScanOptions{})

	if len(seen) != 2 {
		t.Fatalf("音频数 = %d, 期望 2", len(seen))
	}
	if audit.AudioTotal != 2 {
		t.Errorf("audit.AudioTotal = %d, 期望 2", audit.AudioTotal)
	}
	if audit.FilesTotal != 4 {
		t.Errorf("audit.FilesTotal = %d, 期望 4", audit.FilesTotal)
	}
	if !audit.Complete {
		t.Errorf("干净目录应判定为完整枚举，warnings=%v", audit.Warnings)
	}
}

func TestScanSkipsExcludedAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.mp3"), 10)
	writeFile(t, filepath.Join(root, "@eaDir", "skip.mp3"), 10)
	writeFile(t, filepath.Join(root, ".hidden", "skip.mp3"), 10)
	writeFile(t, filepath.Join(root, ".tidy_trash", "skip.mp3"), 10)

	seen, _ := Scan([]string{root}, ScanOptions{})

	if len(seen) != 1 {
		t.Fatalf("应只保留 1 首，实际 %d：%v", len(seen), keysOf(seen))
	}
}

func TestScanMissingRootMakesAuditIncomplete(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.mp3"), 10)

	_, audit := Scan([]string{root, filepath.Join(root, "does-not-exist")}, ScanOptions{})

	if audit.Complete {
		t.Error("存在缺失根目录时，枚举必须判定为不完整（安全门）")
	}
	if len(audit.MissingRoots) != 1 {
		t.Errorf("MissingRoots = %v, 期望 1 项", audit.MissingRoots)
	}
}

func TestScanLimitHitMakesAuditIncomplete(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(root, "s", itoaName(i)+".mp3"), 10)
	}

	seen, audit := Scan([]string{root}, ScanOptions{MaxAudio: 3})

	if len(seen) != 3 {
		t.Errorf("命中上限时应停止在 3 首，实际 %d", len(seen))
	}
	if !audit.LimitHit {
		t.Error("命中上限应置 LimitHit")
	}
	if audit.Complete {
		t.Error("命中上限时枚举不完整，禁止删库（安全门）")
	}
}

func TestScanDepthTruncationMakesAuditIncomplete(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i < 6; i++ {
		deep = filepath.Join(deep, "d")
	}
	writeFile(t, filepath.Join(deep, "deep.mp3"), 10)

	_, audit := Scan([]string{root}, ScanOptions{MaxDepth: 2})

	if audit.DepthTruncated == 0 {
		t.Error("应记录深度截断")
	}
	if audit.Complete {
		t.Error("深度截断时枚举不完整，禁止删库（安全门）")
	}
}

func keysOf(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func itoaName(i int) string {
	return string(rune('a' + i))
}

// ── 索引与安全门 ──

func TestIndexPruneDetectsAddedChangedRemoved(t *testing.T) {
	idx := NewIndex(t.TempDir())

	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 100, MTime: 1000})
	idx.Upsert(Entry{Path: "/m/b.mp3", Size: 200, MTime: 2000})
	idx.Upsert(Entry{Path: "/m/gone.mp3", Size: 300, MTime: 3000})

	seen := map[string]Entry{
		"/m/a.mp3":   {Path: "/m/a.mp3", Size: 100, MTime: 1000}, // 未变化
		"/m/b.mp3":   {Path: "/m/b.mp3", Size: 250, MTime: 2000}, // size 变化
		"/m/new.mp3": {Path: "/m/new.mp3", Size: 400, MTime: 4000},
	}

	delta := idx.Prune(seen, Audit{Complete: true})

	if delta.Unchanged != 1 {
		t.Errorf("未变化 = %d, 期望 1", delta.Unchanged)
	}
	if len(delta.Changed) != 1 || delta.Changed[0].Path != "/m/b.mp3" {
		t.Errorf("变化项 = %v", delta.Changed)
	}
	if len(delta.Added) != 1 || delta.Added[0].Path != "/m/new.mp3" {
		t.Errorf("新增项 = %v", delta.Added)
	}
	if len(delta.Removed) != 1 || delta.Removed[0] != "/m/gone.mp3" {
		t.Errorf("删除项 = %v", delta.Removed)
	}
}

// TestIndexPruneSafetyGate 核心回归：枚举不完整时绝不能删索引
func TestIndexPruneSafetyGate(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 100, MTime: 1000})
	idx.Upsert(Entry{Path: "/m/b.mp3", Size: 200, MTime: 2000})
	idx.Upsert(Entry{Path: "/m/c.mp3", Size: 300, MTime: 3000})

	// 只枚举到 1 首（模拟挂载抖动 / 权限失败 / 深度截断）
	seen := map[string]Entry{
		"/m/a.mp3": {Path: "/m/a.mp3", Size: 100, MTime: 1000},
	}

	delta := idx.Prune(seen, Audit{Complete: false})

	if len(delta.Removed) != 0 {
		t.Fatalf("枚举不完整时禁止删除索引，却删除了 %v", delta.Removed)
	}
	if delta.RemovedSkipped != 2 {
		t.Errorf("RemovedSkipped = %d, 期望 2（b 与 c 被跳过）", delta.RemovedSkipped)
	}

	// 索引内容应保持不变
	if idx.Len() != 3 {
		t.Errorf("索引条数 = %d, 期望仍为 3", idx.Len())
	}
}

func TestIndexChangeDetection(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 100, MTime: 1000})

	added, changed := idx.IsChanged("/m/a.mp3", 100, 1000)
	if added || changed {
		t.Error("完全一致的 (size,mtime) 不应判为变化")
	}

	added, changed = idx.IsChanged("/m/a.mp3", 100, 1001)
	if added || changed {
		t.Error("mtime 相差 1 秒内应被容忍（SMB/NFS 挂载精度），不应判为变化")
	}

	added, changed = idx.IsChanged("/m/a.mp3", 100, 1005)
	if added || !changed {
		t.Error("mtime 相差超过容差应判为变化")
	}

	added, changed = idx.IsChanged("/m/a.mp3", 999, 1000)
	if added || !changed {
		t.Error("size 不同应判为变化")
	}

	added, changed = idx.IsChanged("/m/new.mp3", 1, 1)
	if !added || changed {
		t.Error("未知路径应判为新增")
	}
}

func TestIndexPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex(dir)
	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 100, MTime: 1000, Title: "夜曲", Artist: "周杰伦"})
	idx.Upsert(Entry{Path: "/m/b.flac", Size: 200, MTime: 2000})
	if err := idx.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded := NewIndex(dir)
	if reloaded.Len() != 2 {
		t.Fatalf("重新加载后条数 = %d, 期望 2", reloaded.Len())
	}
	e, ok := reloaded.Get("/m/a.mp3")
	if !ok {
		t.Fatal("应能查到 /m/a.mp3")
	}
	if e.Title != "夜曲" || e.Artist != "周杰伦" {
		t.Errorf("元数据未保留: %+v", e)
	}
}

func TestIndexCorruptedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "library_index.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewIndex(dir)
	if idx.Len() != 0 {
		t.Errorf("索引损坏时应从空开始，实际 %d 条", idx.Len())
	}
}

func TestScanOnProgressCalled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 25; i++ {
		writeFile(t, filepath.Join(root, "d"+itoaName(i%20), "s.mp3"), 10)
	}

	called := 0
	Scan([]string{root}, ScanOptions{OnProgress: func(dirs, files, audio int) { called++ }})

	if called == 0 {
		t.Error("应至少回调一次进度（避免长扫描看起来像卡死）")
	}
}
