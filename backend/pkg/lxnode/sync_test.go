package lxnode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 脚本落盘这一步（见 sync.go）。
//
// 它原先**完全不存在**，而缺口是静默的：宿主是 Node 子进程、只读 `<Dir>/*.js`，
// 目录空着就等于「服务端洛雪零脚本」，界面上没有任何提示。

func TestSyncScriptsWritesAndCleans(t *testing.T) {
	dir := t.TempDir()

	if err := SyncScripts(dir, map[string]string{"全豆要": "// A", "qsvip": "// B"}); err != nil {
		t.Fatalf("首次同步失败：%v", err)
	}
	got := readDir(t, dir)
	if len(got) != 2 {
		t.Fatalf("应落 2 个脚本，得到 %v", got)
	}

	// 同步语义：删掉的脚本不能留在目录里继续被搜索
	if err := SyncScripts(dir, map[string]string{"qsvip": "// B2"}); err != nil {
		t.Fatalf("二次同步失败：%v", err)
	}
	got = readDir(t, dir)
	if len(got) != 1 || got[0] != "qsvip.js" {
		t.Fatalf("删掉的脚本应被清掉，得到 %v", got)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "qsvip.js"))
	if string(body) != "// B2" {
		t.Fatalf("正文应被覆盖成新的，得到 %q", body)
	}
}

// 空正文跳过；其它后缀与子目录不动（只删自己认得的）。
func TestSyncScriptsKeepsForeignFilesAndSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncScripts(dir, map[string]string{"ok": "// ok", "empty": "   "}); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("不该动非 .js 文件：%v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "inner.js")); err != nil {
		t.Fatalf("不该递归进子目录：%v", err)
	}
	if got := readDir(t, dir); len(got) != 1 || got[0] != "ok.js" {
		t.Fatalf("空正文应跳过，得到 %v", got)
	}
}

// 脚本名可能带路径分隔符 —— 直接拼路径就是路径穿越。
func TestSafeScriptNameBlocksTraversal(t *testing.T) {
	for name, want := range map[string]string{
		"../evil": "_evil.js", // `/`→`_` 再去掉首尾的点，结果里不可能出现 `..`
		"a/b":     "a_b.js",
		"..":      "source.js",
		"":        "source.js",
		"全豆要-聚合":  "全豆要-聚合.js",
	} {
		if got := safeScriptName(name); got != want {
			t.Fatalf("脚本名 %q：期望 %q，得到 %q", name, want, got)
		}
	}
	if got := safeScriptName("../evil"); filepath.Base(got) != got {
		t.Fatalf("结果里不该再有路径分隔符：%q", got)
	}
}

func readDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range es {
		// 只数我们管的 .js —— 目录里允许有别的文件（见另一个用例）
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			out = append(out, e.Name())
		}
	}
	return out
}
