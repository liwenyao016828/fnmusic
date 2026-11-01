package appupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultAppName 是飞牛应用标识（/var/apps/<appname>）。
// 必须与 fpk-package/manifest 的 appname 一致；pkg/api 里有一个测试把
// CurrentAppName、本常量、manifest 三方对齐，避免「两处写死不一致」。
const DefaultAppName = "yinshu-ai"

// ScriptConfig 是安装脚本需要的全部信息。
type ScriptConfig struct {
	Pkg     string // 包名，如 yinshu-ai
	Fpk     string // 已下载的安装包绝对路径
	EnvFile string // 安装环境变量文件
	Stage   string // 脚本工作目录（/tmp/yinshu-update）
	Log     string // 日志路径（脚本自身 stdout 已重定向，仅用于文本提示）
	// Version 是这次要装成的版本号（来自 release 的 latest）。
	// **必须有**：安装成功的判据是「装完之后的版本等于它」，不是「进程在 running」。
	Version string
}

// BuildApplyScript 生成自安装脚本。
//
// 这个脚本是整条更新链里最危险的一环：它會 uninstall 掉正在运行的自己。
// 因此下面每一条命令的顺序都不能随便调整：
//  1. 先提权（非 root 时 exec sudo 重新执行自己，而不是每行命令前加 sudo ——
//     后者会漏掉某一行就只失败一半）；
//  2. 先备份 cmd/main：飞牛的 uninstall 会删掉整个应用目录，而 install-fpk
//     需要入口脚本存在才会把应用登记为可启动；先 tar 出来覆盖回去，
//     是「卸载后还能装回来」的保险；
//  3. stop → uninstall → install-fpk，**每一步都要查结果**；
//  4. 按**版本号**验收：装完之后的 /api/app/version 必须等于目标版本，
//     并且进程真的在跑。只看 running 不算成功（见下）；
//  5. 最多 10 次 start+status 轮询：安装完成不代表进程已起；
//  6. trap 里至少 start 一次：中途任何一步失败，旧应用还能被拉起来。
//
// # 为什么必须按版本验收（2026-11-01 用户「下载完成后刷新还是旧版本」）
//
// 旧版脚本只判断 `appcenter-cli status` 里有没有 `running` 字样，而且
// `uninstall` / `install-fpk` 的失败全被 `|| true` 吞掉。这两件事凑在一起就是
// 一个**静默失败**的闭环：
//
//	uninstall 静默失败（应用其实还在）→ install-fpk 对已安装应用是空操作
//	（HANDOVER §1.4.1 实测：只打印「is installed」然后什么都不做）
//	→ 旧进程还在 running → 脚本看到 running 就打印「更新完成」并 exit 0。
//
// 用户看到的就是「下载完成了、安装也提示成功了，刷新还是旧版本」。
// 现在改成：uninstall 失败就重试一次并如实报错；install-fpk 之后**必须**用
// 版本号确认真的换成了新版，否则脚本 exit 1 并把原因写进 install.log。
func BuildApplyScript(cfg ScriptConfig) string {
	pkg := shellQuote(cfg.Pkg)
	fpk := shellQuote(cfg.Fpk)
	envf := shellQuote(cfg.EnvFile)
	stage := shellQuote(cfg.Stage)
	logPath := shellQuote(cfg.Log)
	want := shellQuote(strings.TrimSpace(cfg.Version))

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# 曲率（yinshu-ai）自更新脚本 —— 应用自动生成，请勿手改。\n")
	b.WriteString("# 为什么放在 /tmp：下一步会 uninstall 应用目录，脚本留在里面会把自己删掉。\n")
	b.WriteString("set -eu\n\n")
	fmt.Fprintf(&b, "PKG=%s\n", pkg)
	fmt.Fprintf(&b, "FPK=%s\n", fpk)
	fmt.Fprintf(&b, "ENVF=%s\n", envf)
	fmt.Fprintf(&b, "STAGE=%s\n", stage)
	fmt.Fprintf(&b, "LOG=%s\n", logPath)
	fmt.Fprintf(&b, "WANT_VERSION=%s\n", want)
	b.WriteString("APP_DIR=\"/var/apps/${PKG}\"\n\n")
	b.WriteString("log() { echo \"[$(date '+%F %T')] $*\" >&2; }\n\n")
	b.WriteString(`# ── 1. 提权 ──────────────────────────────────────────────────────────────
if [ "$(id -u)" != "0" ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    log "非 root，使用 sudo 重新执行本脚本"
    exec sudo -n /bin/sh "$0" "$@"
  fi
  echo "当前进程不是 root，且 sudo 免密不可用：请到飞牛应用中心手动安装新版 fpk。" >&2
  exit 1
fi

# ── 2. 失败兜底：至少把应用拉起来一次 ───────────────────────────────────
on_exit() {
  rc=$?
  if [ "$rc" -ne 0 ]; then
    log "安装流程失败（exit $rc），尝试拉起应用"
    appcenter-cli start "$PKG" >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap on_exit EXIT

log "开始更新：pkg=$PKG fpk=$FPK"
mkdir -p "$STAGE" /tmp/appcenter

# ── 3. 保住启动入口 ─────────────────────────────────────────────────────
# uninstall 会删掉 /var/apps/<pkg>；install-fpk 之后若 cmd/main 缺失，应用
# 就会「装着但起不来」。先把新包里的 cmd/main 覆盖到现目录，卸载再装时
# 飞牛能沿用它判定入口。
if [ -e "$APP_DIR/cmd/main" ]; then
  if tar -xOzf "$FPK" cmd/main > "$STAGE/cmd-main.new" 2>/dev/null && [ -s "$STAGE/cmd-main.new" ]; then
    chmod +x "$STAGE/cmd-main.new"
    mv -f "$STAGE/cmd-main.new" "$APP_DIR/cmd/main"
    log "已保留启动入口 cmd/main"
  else
    log "警告：未能从 fpk 提取 cmd/main，跳过入口保护"
  fi
fi

# ── 4. 选卷：有 @appcenter 标记的那个卷才是应用中心卷 ────────────────────
VOL=""
for d in /vol[1-9]*; do
  if [ -d "$d/@appcenter" ]; then
    VOL="$d"
    break
  fi
done
VOLNUM=""
if [ -n "$VOL" ]; then
  VOLNUM="${VOL#/vol}"
fi
log "安装包：$FPK（卷：${VOLNUM:-自动}，目标版本 ${WANT_VERSION:-未知}）"

# ── 5. 卸载旧版 → 安装新版（每一步都查结果，不再 || true 吞掉） ──────────
appcenter-cli stop "$PKG" >/dev/null 2>&1 || true

# uninstall 失败会让后面 install-fpk 变成空操作（对已安装应用它是 no-op），
# 于是「装成功了」其实是「什么都没做」。所以这里失败要重试一次，再失败就报错。
if ! appcenter-cli uninstall "$PKG" >/dev/null 2>&1; then
  log "uninstall 第一次失败，重试一次"
  sleep 2
  if ! appcenter-cli uninstall "$PKG" >/dev/null 2>&1; then
    log "卸载旧版失败：应用可能仍处于已安装状态。请到飞牛应用中心手动卸载后再装 ${FPK}"
    exit 1
  fi
fi

if [ -n "$VOLNUM" ]; then
  if ! out="$(appcenter-cli install-fpk "$FPK" --env "$ENVF" --volume "$VOLNUM" 2>&1)"; then
    log "install-fpk 失败（卷 ${VOLNUM}）：$out"
    log "请到飞牛应用中心手动安装 $FPK"
    exit 1
  fi
else
  if ! out="$(appcenter-cli install-fpk "$FPK" --env "$ENVF" 2>&1)"; then
    log "install-fpk 失败：$out"
    log "请到飞牛应用中心手动安装 $FPK"
    exit 1
  fi
fi
log "install-fpk 输出：$out"

# ── 6. 等它真的起来，并**按版本号验收** ─────────────────────────────────
# 光看 status 里的 running 是不够的：uninstall 没生效时旧进程照样 running，
# 脚本会误报「更新完成」，用户刷新页面还是旧版本（2026-11-01 就是这个现象）。
# 所以除了 running，还要求 /api/app/version 报的版本等于目标版本。
#
# 版本探测只用 curl（飞牛镜像里一定有）；curl 不可用时退回 status-only，
# 并把「无法按版本验收」写进日志，不假装成功。
running_version() {
  if ! command -v curl >/dev/null 2>&1; then
    echo ""
    return 0
  fi
  # ⚠️ sed 用的是 **BRE**：分组括号必须写成 \( \)，写成 ( ) 会被当成字面量，
  # 匹配不到就永远返回空 → 每一轮都判「版本不对」，脚本必然超时失败。
  curl -fsS --max-time 3 "http://127.0.0.1:8898/api/app/version" 2>/dev/null \
    | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'
}

i=1
while [ "$i" -le 15 ]; do
  st="$(appcenter-cli status "$PKG" 2>&1 || true)"
  case "$st" in
    *running*)
      got="$(running_version)"
      if [ -z "$WANT_VERSION" ]; then
        log "警告：本次没有目标版本号，无法按版本验收；只确认了 $PKG 在 running（实报版本 ${got:-未知}）"
        exit 0
      fi
      if [ "$got" = "$WANT_VERSION" ]; then
        log "更新完成：$PKG 已运行，版本 $got"
        exit 0
      fi
      log "第 $i 轮：进程在 running，但版本是 ${got:-未知}（期望 $WANT_VERSION），继续等待"
      ;;
    *)
      log "第 $i 轮：状态不是 running（$st）"
      ;;
  esac
  appcenter-cli start "$PKG" >/dev/null 2>&1 || true
  sleep 2
  i=$((i + 1))
done

log "安装后 15 轮仍未验收通过（期望版本 $WANT_VERSION，实报 ${got:-未知}）：请到飞牛应用中心检查，或手动安装 $FPK"
exit 1
`)
	return b.String()
}

// collectInstallEnv 从当前进程环境里挑出「安装时需要原样传回」的变量。
//
// install-fpk 的 --env 用于把用户首次安装时填的参数（数据目录、端口等）
// 在升级时带回去。飞牛没有统一的「安装参数」读取接口，只能从当前进程环境
// 反推：应用是带这些变量启动的。
//
// 采取白名单而不是全量透传：全量会把 PATH/HOME/代理等运行时变量塞进安装
// 脚本环境，反而可能改变安装行为。白名单覆盖 PKG_*（安装参数约定前缀）、
// QULV_*（本项目自有开关）与两个数据目录键；一个都没有时返回空字符串，
// 生成空文件也是合法的（knock 在拿不到 env 时同样传空文件）。
func collectInstallEnv(environ []string) string {
	var lines []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if !strings.HasPrefix(k, "PKG_") &&
			!strings.HasPrefix(k, "QULV_") &&
			k != "TRIM_PKGVAR" && k != "TRIM_APPDEST" && k != "TRIM_APPNAME" {
			continue
		}
		// 换行会把 env 文件拆坏；跳过而不是转义（安装参数里不该有换行）。
		if strings.ContainsAny(v, "\r\n") {
			continue
		}
		lines = append(lines, k+"="+v)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// ResolveAppName 解析当前应用在 /var/apps 下的目录名。
//
// 优先级：TRIM_APPNAME（飞牛启动时注入）→ 可执行文件路径里的 /var/apps/<name>/
// → 从可执行文件所在目录向上找 manifest 的 appname → 返回空（调用方用默认值）。
// 为什么要逐级兜底：开发环境跑的是 .dev/fn-dev-backend，没有 TRIM_APPNAME，
// 也没有 /var/apps；上了真机这两个都可能有，但不同版本飞牛的注入方式不完全一致，
// 多兜一层就少一次「更新把包名搞错、卸载错应用」的事故。
func ResolveAppName(exePath string, getenv func(string) string) string {
	if getenv != nil {
		if v := strings.TrimSpace(getenv("TRIM_APPNAME")); v != "" {
			return v
		}
	}
	exePath = strings.TrimSpace(exePath)
	if i := strings.Index(exePath, "/var/apps/"); i >= 0 {
		rest := exePath[i+len("/var/apps/"):]
		if j := strings.IndexByte(rest, '/'); j > 0 {
			return rest[:j]
		}
	}
	if exePath != "" {
		dir := filepath.Dir(exePath)
		for i := 0; i < 5 && dir != "" && dir != "/" && dir != "."; i++ {
			if name := appNameFromManifest(filepath.Join(dir, "manifest")); name != "" {
				return name
			}
			dir = filepath.Dir(dir)
		}
	}
	return ""
}

// appNameFromManifest 读 manifest 里的 appname= 行（飞牛 manifest 是 key=value 文本）。
func appNameFromManifest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "appname=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "appname="))
		}
	}
	return ""
}

// launchDetached 用独立会话启动安装脚本。
//
// 两个要点：
//   - setsid：让脚本脱离当前进程的会话/进程组，本进程被 uninstall 杀掉时
//     脚本不会被一起带走（没有 setsid 就退回 nohup，至少不被 SIGHUP 干掉）；
//   - 立即返回：脚本在后台跑，HTTP 响应必须先把「已开始」写回去。
func launchDetached(script, logPath string) error {
	cmd := fmt.Sprintf(
		`if command -v setsid >/dev/null 2>&1; then setsid /bin/sh %[1]s >>%[2]s 2>&1 </dev/null & else nohup /bin/sh %[1]s >>%[2]s 2>&1 </dev/null & fi`,
		shellQuote(script), shellQuote(logPath))
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = os.Environ()
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v：%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// detectSudo 判断能否免密 sudo。`sudo -n true` 不读密码、不弹终端；
// 失败即视为不可用（要求输密码的场景在无人值守的升级里没有意义）。
func detectSudo() bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// shellQuote 把任意路径安全地放进 sh 单引号里。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
