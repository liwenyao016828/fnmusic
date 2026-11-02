package appupdate

import (
	"os"
	"os/exec"
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

// ── 安装脚本必须按**版本号**验收，不能只看 running（2026-11-01）──
//
// 用户原话：「在线更新有问题，下载完成后。刷新页面或者等一会儿，进来还是旧版本」。
//
// 旧脚本的失败闭环：uninstall 失败被 `|| true` 吞掉 → 应用其实还在 →
// install-fpk 对已安装应用是空操作（HANDOVER §1.4.1 实测）→ 旧进程还在 running
// → 脚本看到 running 就打印「更新完成」并 exit 0。用户以为装好了，其实什么都没变。
//
// 下面这几条把这个闭环钉死：验收必须比对版本、卸载/安装失败必须暴露。

func TestApplyScriptVerifiesVersionNotJustRunning(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{
		Pkg:     "yinshu-ai",
		Fpk:     "/data/updates/yinshu-ai-2.24.18.fpk",
		EnvFile: "/tmp/yinshu-update/install.env",
		Stage:   "/tmp/yinshu-update",
		Log:     "/tmp/yinshu-update/install.log",
		Version: "2.24.18",
	})
	for _, want := range []string{
		// 目标版本要进脚本，否则无从验收
		"WANT_VERSION='2.24.18'",
		// 探测运行中实例的真实版本
		"/api/app/version",
		// 版本对上才算成功
		`if [ "$got" = "$WANT_VERSION" ]`,
		// 版本对不上要继续等，而不是直接报成功
		"继续等待",
		// 等不到就失败，不许假装成功
		"exit 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("脚本缺少版本验收片段 %q", want)
		}
	}
	// 反向：不能再出现「只要 running 就 exit 0」的老逻辑
	if strings.Contains(body, `*running*)
      log "更新完成：$PKG 已运行"
      exit 0`) {
		t.Error("脚本仍是「看到 running 就报成功」的老逻辑，必须按版本验收")
	}
}

func TestApplyScriptSurfacesUninstallAndInstallFailures(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/data/updates/a.fpk",
		EnvFile: "/tmp/yinshu-update/install.env", Stage: "/tmp/yinshu-update",
		Log: "/tmp/yinshu-update/install.log", Version: "2.24.18",
	})
	// uninstall 不能再被 `|| true` 吞掉：失败要重试一次，再失败就报错退出
	if strings.Contains(body, `appcenter-cli uninstall "$PKG" >/dev/null 2>&1 || true`) {
		t.Error("uninstall 的失败仍被 || true 吞掉 —— 这正是「装成功但版本没变」的根因")
	}
	if !strings.Contains(body, "卸载旧版失败") {
		t.Error("卸载失败没有如实报出来")
	}
	// install-fpk 失败也要暴露，并把 cli 的原始输出写进日志
	if !strings.Contains(body, "install-fpk 失败") {
		t.Error("install-fpk 失败没有如实报出来")
	}
	if !strings.Contains(body, "install-fpk 输出") {
		t.Error("install-fpk 的输出应当写进日志（排障唯一线索）")
	}
}

// 没有目标版本号时，脚本要明说「无法按版本验收」，而不是静默当成功。
func TestApplyScriptWarnsWhenNoTargetVersion(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{Pkg: "yinshu-ai", Fpk: "/a.fpk"})
	if !strings.Contains(body, "无法按版本验收") {
		t.Error("缺目标版本时应当写明无法按版本验收")
	}
}

// running_version 的 sed 必须是 **BRE 转义括号**（2026-11-01 自查发现）。
//
// 写成 `s/.*"version":"([^"]*)".*/\1/p` 时，BRE 把 ( ) 当字面量 —— 版本号永远
// 提取不出来，于是每一轮都判「版本不对」，脚本必然超时失败。这条把正则钉住，
// 并用真实响应串验一遍提取结果。
func TestApplyScriptVersionSedIsBRE(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/a.fpk", EnvFile: "/e", Stage: "/s", Log: "/l",
		Version: "2.24.18",
	})
	if strings.Contains(body, `"version":"([^"]*)"`) {
		t.Fatal("sed 用了未转义括号（BRE 下是字面量，版本永远提取不出来）")
	}
	if !strings.Contains(body, `"version":"\([^"]*\)"`) {
		t.Fatal("sed 分组括号应写成 \\( \\)")
	}
}

// 用 sh 跑一遍真实的提取逻辑，钉住「能提取出 2.24.18」这个行为（不依赖 curl）。
func TestApplyScriptVersionExtractionWorks(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("没有 sh")
	}
	// 与脚本里 running_version 的 sed 完全同一串
	const sedExpr = `s/.*"version":"\([^"]*\)".*/\1/p`
	payload := `{"code":200,"data":{"name":"yinshu-ai","version":"2.24.18"}}`
	cmd := exec.Command("sh", "-c", `printf '%s' "$1" | sed -n "$2"`, "sh", payload, sedExpr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sed 执行失败：%v（%s）", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "2.24.18" {
		t.Fatalf("版本提取得到 %q，期望 2.24.18", got)
	}
}
