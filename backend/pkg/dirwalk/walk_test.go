package dirwalk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── helpers ──

func write(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func walkPaths(t *testing.T, files []File) []string {
	t.Helper()
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func hasSuffix(list []string, suffix string) bool {
	for _, s := range list {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func endsWith(s, suffix string) bool {
	return strings.HasSuffix(s, suffix)
}

func warningsContain(list []string, needle string) bool {
	for _, w := range list {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// ── 用例 ──

func TestWalkCountsFilesAndAudio(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mp3"), 10)
	write(t, filepath.Join(root, "b.FLAC"), 20)
	write(t, filepath.Join(root, "readme.txt"), 5)
	write(t, filepath.Join(root, "sub", "c.m4a"), 30)
	write(t, filepath.Join(root, "sub", "cover.jpg"), 40)

	files, st := Walk([]string{root}, Options{})

	if st.FilesTotal != 5 {
		t.Errorf("FilesTotal = %d, 期望 5", st.FilesTotal)
	}
	if st.AudioTotal != 3 {
		t.Errorf("AudioTotal = %d, 期望 3", st.AudioTotal)
	}
	if len(files) != 3 {
		t.Fatalf("返回 %d 个音频，期望 3：%v", len(files), walkPaths(t, files))
	}
	if !st.Complete() {
		t.Errorf("干净目录树应判定为完整，warnings=%v missing=%v", st.Warnings, st.MissingRoots)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("干净目录树不该有告警：%v", st.Warnings)
	}

	// 大小写不敏感 + 扩展名归一化 + 斜杠形式
	var flac *File
	for i := range files {
		if strings.HasSuffix(files[i].Path, "b.FLAC") {
			flac = &files[i]
		}
	}
	if flac == nil {
		t.Fatalf("大写的 .FLAC 没被认出来：%v", walkPaths(t, files))
	}
	if flac.Format != "flac" {
		t.Errorf("Format = %q, 期望 flac（小写不带点）", flac.Format)
	}
	if flac.Size != 20 {
		t.Errorf("Size = %d, 期望 20", flac.Size)
	}
	if flac.MTime <= 0 {
		t.Errorf("MTime = %d, 期望 Unix 秒", flac.MTime)
	}
	for _, f := range files {
		if strings.Contains(f.Path, "\\") || !strings.HasPrefix(f.Path, "/") {
			t.Errorf("路径应是统一斜杠形式的绝对路径：%q", f.Path)
		}
	}
	if st.ElapsedMs < 0 {
		t.Errorf("ElapsedMs 不该是负数：%d", st.ElapsedMs)
	}
}

func TestWalkExcludesDefaultAndExtraDirs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "keep.mp3"), 1)
	write(t, filepath.Join(root, "@eaDir", "thumb.mp3"), 1)
	write(t, filepath.Join(root, "#recycle", "gone.mp3"), 1)
	write(t, filepath.Join(root, "node_modules", "dep.mp3"), 1)
	write(t, filepath.Join(root, "my-trash", "gone.mp3"), 1)

	files, st := Walk([]string{root}, Options{ExcludeDirs: []string{"my-trash", "  "}})

	got := walkPaths(t, files)
	if len(got) != 1 || !hasSuffix(got, "keep.mp3") {
		t.Fatalf("只应枚举到 keep.mp3，实际：%v", got)
	}
	// 被排除的目录不进 FilesTotal/AudioTotal
	if st.AudioTotal != 1 {
		t.Errorf("AudioTotal = %d, 期望 1（被排除目录里的歌不该计数）", st.AudioTotal)
	}
	if st.Skipped != 0 {
		t.Errorf("排除目录不是 Skipped（那是 AcceptFile 的事），实际 %d", st.Skipped)
	}
}

func TestWalkSkipsHiddenDirsButNotHiddenRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ok.mp3"), 1)
	write(t, filepath.Join(root, ".hidden", "hidden.mp3"), 1)

	files, _ := Walk([]string{root}, Options{})
	if got := walkPaths(t, files); len(got) != 1 || !endsWith(got[0], "ok.mp3") {
		t.Fatalf("隐藏目录应被跳过，实际：%v", got)
	}

	// 根目录自己是隐藏目录时照常遍历（.dev/music 这种布局）
	hiddenRoot := filepath.Join(root, ".hidden")
	files, _ = Walk([]string{hiddenRoot}, Options{})
	if got := walkPaths(t, files); len(got) != 1 || !endsWith(got[0], "hidden.mp3") {
		t.Fatalf("根目录本身是隐藏目录时不该被跳过，实际：%v", got)
	}

	// IncludeHidden 打开时隐藏目录也进去
	files, _ = Walk([]string{root}, Options{IncludeHidden: true})
	if len(files) != 2 {
		t.Fatalf("IncludeHidden 时应有 2 首，实际：%v", walkPaths(t, files))
	}
}

func TestWalkDepthLimitIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "l1", "a.mp3"), 1)
	write(t, filepath.Join(root, "l1", "l2", "b.mp3"), 1)
	write(t, filepath.Join(root, "l1", "l2", "l3", "c.mp3"), 1)

	files, st := Walk([]string{root}, Options{MaxDepth: 2})

	if got := walkPaths(t, files); len(got) != 1 {
		t.Fatalf("MaxDepth=2 只应到 l1/a.mp3，实际：%v", got)
	}
	if st.DepthTruncated == 0 {
		t.Error("DepthTruncated 应 > 0")
	}
	if !warningsContain(st.Warnings, "目录层级超过上限") {
		t.Errorf("应有深度截断告警：%v", st.Warnings)
	}
	if st.Complete() {
		t.Error("有截断时不得判定为完整（完整性门禁靠它）")
	}
}

func TestWalkMaxAudioStopsAndFlags(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3", "e.mp3"} {
		write(t, filepath.Join(root, n), 1)
	}

	files, st := Walk([]string{root}, Options{MaxAudio: 3})

	if len(files) != 3 {
		t.Fatalf("MaxAudio=3 应恰好返回 3 首，实际 %d", len(files))
	}
	if !st.LimitHit {
		t.Error("LimitHit 应为 true")
	}
	if st.AudioTotal != 3 {
		t.Errorf("AudioTotal = %d, 期望 3", st.AudioTotal)
	}
	if !warningsContain(st.Warnings, "达到本次上限") {
		t.Errorf("应有上限告警：%v", st.Warnings)
	}
	if st.Complete() {
		t.Error("命中上限时不得判定为完整")
	}
}

func TestWalkSymlinkLoopTerminates(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mp3"), 1)
	sub := filepath.Join(root, "sub")
	write(t, filepath.Join(sub, "b.mp3"), 1)
	// 自己指自己 + 指回父目录：以前 NAS 浏览那条 Walk 会一直转
	if err := os.Symlink(sub, filepath.Join(sub, "loop")); err != nil {
		t.Skipf("环境不支持软链接：%v", err)
	}
	if err := os.Symlink(root, filepath.Join(sub, "up")); err != nil {
		t.Skipf("环境不支持软链接：%v", err)
	}

	files, st := Walk([]string{root}, Options{})

	if len(files) != 2 {
		t.Fatalf("软链接环不该重复计入，实际：%v", walkPaths(t, files))
	}
	if st.DirsVisited > 4 {
		t.Errorf("软链接环没被剪掉，进入了 %d 个目录", st.DirsVisited)
	}
}

func TestWalkMissingRootIsRecorded(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mp3"), 1)
	missing := filepath.Join(root, "nope")

	files, st := Walk([]string{root, missing}, Options{})

	if len(files) != 1 {
		t.Fatalf("存在的根目录照常枚举，实际：%v", walkPaths(t, files))
	}
	if len(st.MissingRoots) != 1 || st.MissingRoots[0] != missing {
		t.Errorf("MissingRoots = %v, 期望 [%s]", st.MissingRoots, missing)
	}
	if st.Complete() {
		t.Error("有缺失根目录时不得判定为完整")
	}
}

func TestWalkShouldStopMarksIncomplete(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mp3"), 1)

	calls := 0
	files, st := Walk([]string{root}, Options{
		ShouldStop: func() bool {
			calls++
			return calls > 1 // 第一次放行，随后整体停
		},
	})

	if len(files) != 0 {
		t.Errorf("取消后不该再返回文件：%v", walkPaths(t, files))
	}
	if !st.Stopped {
		t.Error("Stopped 应为 true")
	}
	if !warningsContain(st.Warnings, "主动取消") {
		t.Errorf("应有取消告警：%v", st.Warnings)
	}
	if st.Complete() {
		t.Error("被取消时不得判定为完整")
	}
}

func TestWalkAcceptFileSkipsAndCounts(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ok.mp3"), 1)
	write(t, filepath.Join(root, "secret.mp3"), 1)

	files, st := Walk([]string{root}, Options{
		AcceptFile: func(path string, _ os.FileMode) bool {
			return !strings.HasSuffix(path, "secret.mp3")
		},
	})

	if got := walkPaths(t, files); len(got) != 1 || !endsWith(got[0], "ok.mp3") {
		t.Fatalf("被过滤的文件不该出现：%v", got)
	}
	if st.Skipped != 1 {
		t.Errorf("Skipped = %d, 期望 1（跳过多少条要看得见）", st.Skipped)
	}
	if st.AudioTotal != 1 || st.FilesTotal != 1 {
		t.Errorf("被跳过的文件不该计入计数：audio=%d files=%d", st.AudioTotal, st.FilesTotal)
	}
	if !st.Complete() {
		t.Errorf("按规则跳过不算枚举不完整：%v", st.Warnings)
	}
}

func TestWalkUnreadableDirCountsInaccessible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视 0000 权限，这条测不了")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mp3"), 1)
	locked := filepath.Join(root, "locked")
	write(t, filepath.Join(locked, "b.mp3"), 1)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("chmod 失败：%v", err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()

	files, st := Walk([]string{root}, Options{})

	if len(files) != 1 {
		t.Fatalf("读不了的目录不该让整次枚举失败：%v", walkPaths(t, files))
	}
	if st.Inaccessible == 0 {
		t.Error("Inaccessible 应 > 0")
	}
	if !warningsContain(st.Warnings, "无法读取目录") {
		t.Errorf("应有不可读告警：%v", st.Warnings)
	}
	if st.Complete() {
		t.Error("有读不了的目录时不得判定为完整")
	}
}

func TestWalkProgressReports(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 25; i++ {
		write(t, filepath.Join(root, "d", string(rune('a'+i%26))+string(rune('0'+i/26)), "x.mp3"), 1)
	}

	var lastDirs, lastFiles, lastAudio int
	calls := 0
	Walk([]string{root}, Options{OnProgress: func(d, f, a int) {
		calls++
		lastDirs, lastFiles, lastAudio = d, f, a
	}})

	if calls == 0 {
		t.Fatal("每 20 个目录应回调一次，最后至少一次收尾回调")
	}
	if lastFiles != 25 || lastAudio != 25 {
		t.Errorf("收尾回调应带完整计数，实际 files=%d audio=%d", lastFiles, lastAudio)
	}
	if lastDirs == 0 {
		t.Error("DirsVisited 应 > 0")
	}
}

func TestWalkEmptyRoots(t *testing.T) {
	files, st := Walk(nil, Options{})
	if len(files) != 0 {
		t.Errorf("没有根目录时应返回空列表，实际 %v", walkPaths(t, files))
	}
	if len(st.MissingRoots) != 0 {
		t.Errorf("没有根目录不是「缺失根目录」：%v", st.MissingRoots)
	}
	// 空根目录集合是完整的一次枚举（调用方自己判断要不要扫）
	if !st.Complete() {
		t.Error("空 roots 应判定为完整")
	}
}
