package nas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/filename"
)

// newHealthRoot 造一个被 AllowedRoots 认可的临时目录，并把 fnos 共享目录查询关掉，
// 否则测试环境里一旦能连上飞牛，AllowedRoots 会用真机目录而不是临时目录。
func newHealthRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(allowedRootsEnv, dir)

	old := fnosRootsSource
	fnosRootsSource = func() ([]string, error) { return nil, nil }
	t.Cleanup(func() { fnosRootsSource = old })
	// 清掉 30 秒缓存，避免上一个用例的根目录残留
	fnosRootsCache.Lock()
	fnosRootsCache.roots = nil
	fnosRootsCache.at = time.Time{}
	fnosRootsCache.Unlock()

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("解析临时目录失败: %v", err)
	}
	return real
}

func TestScanHealthFindsBadNamesAndJunk(t *testing.T) {
	root := newHealthRoot(t)

	// 1) 非法 UTF-8 文件名（GBK「歌名」）
	badUTF8 := filepath.Join(root, "\xb8\xe8\xc3\xfb.mp3")
	if err := os.WriteFile(badUTF8, []byte("x"), 0o644); err != nil {
		t.Fatalf("造非法 UTF-8 文件失败: %v", err)
	}
	// 2) 控制字符
	ctrl := filepath.Join(root, "歌\x01名.mp3")
	if err := os.WriteFile(ctrl, []byte("x"), 0o644); err != nil {
		t.Fatalf("造控制字符文件失败: %v", err)
	}
	// 3) 残留半成品
	part := filepath.Join(root, "正常歌.mp3.part")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatalf("造 .part 失败: %v", err)
	}
	// 4) 0 字节音频
	zero := filepath.Join(root, "空文件.flac")
	if err := os.WriteFile(zero, nil, 0o644); err != nil {
		t.Fatalf("造 0 字节音频失败: %v", err)
	}
	// 5) 干净文件，不该被报
	clean := filepath.Join(root, "周杰伦 - 稻香.mp3")
	if err := os.WriteFile(clean, []byte("audio"), 0o644); err != nil {
		t.Fatalf("造干净文件失败: %v", err)
	}

	rep, err := ScanHealth(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if rep.Scanned < 5 {
		t.Fatalf("扫描条目数 = %d，期望至少 5", rep.Scanned)
	}

	kinds := map[string]int{}
	byPath := map[string]HealthIssue{}
	for _, it := range rep.Issues {
		kinds[it.Kind]++
		byPath[it.Path] = it
	}
	if kinds[IssueBadUTF8] != 1 {
		t.Errorf("非法 UTF-8 应命中 1 条，实际 %d", kinds[IssueBadUTF8])
	}
	if kinds[IssueControlChar] != 1 {
		t.Errorf("控制字符应命中 1 条，实际 %d", kinds[IssueControlChar])
	}
	if kinds[IssueLeftover] != 1 {
		t.Errorf(".part 残留应命中 1 条，实际 %d", kinds[IssueLeftover])
	}
	if kinds[IssueZeroByte] != 1 {
		t.Errorf("0 字节音频应命中 1 条，实际 %d", kinds[IssueZeroByte])
	}
	if _, ok := byPath[filepath.ToSlash(clean)]; ok {
		t.Errorf("干净文件被误报：%s", clean)
	}

	// 排序：非法 UTF-8 排最前
	if len(rep.Issues) > 0 && rep.Issues[0].Kind != IssueBadUTF8 {
		t.Errorf("第一条应为非法 UTF-8，实际 %s", rep.Issues[0].Kind)
	}
}

func TestFixIssuesRenamesAndDeletes(t *testing.T) {
	root := newHealthRoot(t)

	badUTF8 := filepath.Join(root, "\xb8\xe8\xc3\xfb.mp3")
	if err := os.WriteFile(badUTF8, []byte("x"), 0o644); err != nil {
		t.Fatalf("造非法 UTF-8 文件失败: %v", err)
	}
	part := filepath.Join(root, "x.mp3.part")
	if err := os.WriteFile(part, []byte("p"), 0o644); err != nil {
		t.Fatalf("造 .part 失败: %v", err)
	}

	rep, err := ScanHealth(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(rep.Issues) == 0 {
		t.Fatal("期望有体检问题")
	}

	results := FixIssues(root, rep.Issues, false)
	fixed := 0
	for _, r := range results {
		if r.OK {
			fixed++
		} else {
			t.Logf("未修复 %s（%s）：%s", r.Path, r.Action, r.Error)
		}
	}
	if fixed == 0 {
		t.Fatal("一条都没修成功")
	}

	// 非法 UTF-8 文件应已改名成合法名字
	if _, err := os.Lstat(badUTF8); err == nil {
		t.Errorf("脏名字文件还在：%s", badUTF8)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	audioFound := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".part") {
			t.Errorf(".part 残留没删：%s", name)
		}
		if strings.HasSuffix(name, ".mp3") && name != filepath.Base(badUTF8) {
			audioFound = true
		}
	}
	if !audioFound {
		t.Error("改名后的 mp3 不见了（不应删除音频文件）")
	}

	// 再扫一次应该干净（只剩改名后的干净文件）
	rep2, err := ScanHealth(root)
	if err != nil {
		t.Fatalf("二次扫描失败: %v", err)
	}
	for _, it := range rep2.Issues {
		if it.Kind == IssueBadUTF8 || it.Kind == IssueLeftover {
			t.Errorf("修完仍有问题：%s %s", it.Kind, it.Path)
		}
	}
}

func TestFixIssuesRejectsPathOutsideRoot(t *testing.T) {
	root := newHealthRoot(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "别人的歌.mp3")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatalf("造目录外文件失败: %v", err)
	}

	results := FixIssues(root, []HealthIssue{{
		Path: victim, Kind: IssueLeftover, Fix: FixDelete,
	}}, false)
	if len(results) != 1 {
		t.Fatalf("期望 1 条结果，实际 %d", len(results))
	}
	if results[0].OK {
		t.Fatalf("目录外文件不该被处理：%+v", results[0])
	}
	if _, err := os.Lstat(victim); err != nil {
		t.Fatalf("目录外文件被删了：%v", err)
	}
}

func TestSuggestNameKeepsExtension(t *testing.T) {
	got := suggestName("\xb8\xe8.mp3", false)
	if !strings.HasSuffix(got, ".mp3") {
		t.Fatalf("扩展名丢了：%q", got)
	}
	if !filename.IsClean(got) {
		t.Fatalf("建议名字仍不干净：%q", got)
	}
}
