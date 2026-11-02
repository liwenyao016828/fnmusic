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

// 2026-11-02 真机事故：带卸载向导的应用不能用命令行卸载。
//
// v2.24.5 起 fpk 里多了 wizard/uninstall（问「数据保留还是删除」）。飞牛应用中心
// 命令行不支持回答卸载向导，uninstall 在无交互环境下直接被拒 —— 而且**返回值
// 仍是 0**，输出又被 `>/dev/null` 吞掉。于是老脚本一路往下走：
//
//	uninstall 没真执行 → install-fpk 对已安装应用是空操作（只打印
//	「Application [yinshu-ai] is installed.」）→ 磁盘上还是旧版
//	→ 按版本号验收必然失败 → 用户看到「手动点更新仍然失败」。
//
// 所以脚本现在**根本不该再调用 uninstall**，也不该再依赖 install-fpk。
func TestApplyScriptDoesNotUninstall(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/data/updates/a.fpk",
		EnvFile: "/tmp/yinshu-update/install.env", Stage: "/tmp/yinshu-update",
		Log: "/tmp/yinshu-update/install.log", Version: "2.24.19",
	})
	for _, banned := range []string{
		"appcenter-cli uninstall",
		"appcenter-cli install-fpk",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("脚本不应再调用 %q：带卸载向导的应用会被应用中心命令行拒绝，且失败被返回码 0 掩盖", banned)
		}
	}
}

// 就地升级必须「先摊包校验、后动线上文件」，且失败了要能回退。
func TestApplyScriptUnpacksBeforeTouchingAppDir(t *testing.T) {
	body := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/data/updates/a.fpk",
		EnvFile: "/tmp/yinshu-update/install.env", Stage: "/tmp/yinshu-update",
		Log: "/tmp/yinshu-update/install.log", Version: "2.24.19",
	})
	// 摊包的位置要排在第一次动应用目录之前
	unpack := strings.Index(body, `tar -xzf "$FPK" -C "$UNPACK"`)
	backup := strings.Index(body, `log "已备份旧程序到 $BK"`)
	if unpack < 0 {
		t.Fatal("脚本没有摊包校验步骤")
	}
	if backup < 0 {
		t.Fatal("脚本没有备份旧程序的步骤")
	}
	if unpack > backup {
		t.Error("摊包校验必须排在备份/替换之前：解不开的包不该动线上文件")
	}
	// 必须有回退
	for _, want := range []string{
		"已放弃本次更新", // 前置校验失败时明确告知「什么都没发生」
		"开始回退",     // 验收不过时回退
		"已回退到旧版",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("脚本缺少 %q", want)
		}
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

// manifest 必须跟着换：应用中心的「已安装版本」读的是 /var/apps/<pkg>/manifest，
// 只替换 target/ 会让它永远显示旧版本号（2026-11-02 收尾自查发现的洞）。
func TestApplyScriptUpdatesManifest(t *testing.T) {
	s := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/tmp/x.fpk", EnvFile: "/tmp/x.env",
		Stage: "/tmp/yinshu-update", Log: "/tmp/yinshu-update/install.log", Version: "2.28.0",
	})
	if !strings.Contains(s, `cp -a "$SRC/manifest" "$APP_DIR/manifest"`) {
		t.Fatal("脚本没有把新 manifest 覆盖到 $APP_DIR，应用中心会一直显示旧版本")
	}
	if !strings.Contains(s, "manifest 已更新") {
		t.Fatal("脚本没有记录 manifest 更新结果")
	}
	// 备份与回退：旧 manifest 要进备份目录，并且回退时还原到 $APP_DIR（不是 $DEST）
	if !strings.Contains(s, `cp -a "$APP_DIR/manifest" "$BK/manifest"`) {
		t.Fatal("旧 manifest 没有进备份目录")
	}
	if !strings.Contains(s, `cp -a "$item" "$APP_DIR/manifest"`) {
		t.Fatal("回退分支没有还原 $APP_DIR/manifest")
	}
}

// 回退分支也要把 manifest 还原到 $APP_DIR —— 第 8 节那个循环如果照搬 $DEST，
// 会在 target/ 下留一个没人读的 manifest 副本，应用中心还是显示新版号。
func TestApplyScriptRestorePutsManifestBackToAppDir(t *testing.T) {
	s := BuildApplyScript(ScriptConfig{
		Pkg: "yinshu-ai", Fpk: "/tmp/x.fpk", EnvFile: "/tmp/x.env",
		Stage: "/tmp/yinshu-update", Log: "/tmp/yinshu-update/install.log", Version: "2.28.0",
	})
	// 两处（替换失败回退 + 版本验收失败回退）都得有 $APP_DIR/manifest 的还原
	if n := strings.Count(s, `cp -a "$item" "$APP_DIR/manifest"`); n != 2 {
		t.Fatalf("回退分支还原 $APP_DIR/manifest 的次数 = %d，期望 2（替换失败 + 验收失败）", n)
	}
}
