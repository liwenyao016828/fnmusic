package appupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectInstallEnvKeepsOnlyInstallKeys(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"PKG_VOLUME=1",
		"PKG_DATA_DIR=/vol1/@appdata/yinshu-ai",
		"QULV_TAKEOVER=1",
		"TRIM_PKGVAR=/var/apps/yinshu-ai/var",
		"TRIM_APPDEST=/var/apps/yinshu-ai",
		"OTHER=1",
		"PKG_BAD=line1\nline2",
	}
	got := collectInstallEnv(environ)
	for _, want := range []string{
		"PKG_VOLUME=1",
		"PKG_DATA_DIR=/vol1/@appdata/yinshu-ai",
		"QULV_TAKEOVER=1",
		"TRIM_PKGVAR=/var/apps/yinshu-ai/var",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("应保留 %q，实际：\n%s", want, got)
		}
	}
	for _, bad := range []string{"PATH=", "HOME=", "OTHER=", "PKG_BAD="} {
		if strings.Contains(got, bad) {
			t.Errorf("不应包含 %q，实际：\n%s", bad, got)
		}
	}
	if collectInstallEnv([]string{"PATH=/usr/bin"}) != "" {
		t.Fatal("没有任何安装键时应返回空内容（空 env 文件也是合法的）")
	}
}

func TestResolveAppName(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got := ResolveAppName("/var/apps/foo/app/foo", env(map[string]string{"TRIM_APPNAME": "bar"})); got != "bar" {
		t.Fatalf("TRIM_APPNAME 优先，实际 %q", got)
	}
	if got := ResolveAppName("/var/apps/yinshu-ai/app/yinshu-ai", env(nil)); got != "yinshu-ai" {
		t.Fatalf("/var/apps 路径应解析出包名，实际 %q", got)
	}

	// manifest 向上查找
	root := t.TempDir()
	bin := filepath.Join(root, "app", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "manifest"), []byte("appname=demo-app\nversion=1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAppName(filepath.Join(bin, "demo"), env(nil)); got != "demo-app" {
		t.Fatalf("应从 manifest 读到包名，实际 %q", got)
	}
	if got := ResolveAppName("", env(nil)); got != "" {
		t.Fatalf("无信息时应返回空，实际 %q", got)
	}
}

func TestSafeAssetNameRejectsTraversal(t *testing.T) {
	if got := safeAssetName("../../etc/passwd"); got != "passwd" {
		t.Fatalf("应只保留文件名，实际 %q", got)
	}
	if got := safeAssetName("a/b.fpk"); got != "b.fpk" {
		t.Fatalf("应只保留文件名，实际 %q", got)
	}
	if got := safeAssetName("   "); got != "" {
		t.Fatalf("空名字应返回空，实际 %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a b"); got != "'a b'" {
		t.Fatalf("普通空格应被引起来：%q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("单引号应转义：%q", got)
	}
}

// DefaultAppName 必须与 fpk-package/manifest 的 appname 一致。
// 这是「两处写死不一致」的看门测试：改名时要么两处一起改，要么测试红。
func TestDefaultAppNameMatchesManifest(t *testing.T) {
	path := filepath.Join("..", "..", "..", "fpk-package", "manifest")
	got := appNameFromManifest(path)
	if got == "" {
		t.Skipf("读不到 manifest：%s", path)
	}
	if got != DefaultAppName {
		t.Fatalf("manifest appname=%q 与 DefaultAppName=%q 不一致", got, DefaultAppName)
	}
}
