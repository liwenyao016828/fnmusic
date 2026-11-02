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

// BuildApplyScript 生成自更新脚本。
//
// 这个脚本是整条更新链里最危险的一环：它要替换掉正在运行的自己。
// 顺序不能随便调整：
//  1. 先提权（非 root 时 exec sudo 重新执行自己，而不是每行命令前加 sudo ——
//     后者会漏掉某一行就只失败一半）；
//  2. **先摊包校验**：把新 fpk 解到 /tmp 下，确认里面有可执行的本体与
//     cmd/main，再动线上的应用目录。摊包失败=什么都没发生，旧版照跑；
//  3. 备份旧的程序文件（target/ 下除数据之外的部分），失败才用得上；
//  4. 停进程 → 就地替换程序文件（**不再 uninstall**，见下）→ 拉起；
//  5. 按**版本号**验收：/api/app/version 必须等于目标版本且进程在跑；
//  6. 版本没变就自动回退到备份，并把原因写进日志。
//
// # 为什么不用 uninstall / install-fpk（2026-11-02 用户「手动点击更新仍然失败」）
//
// 老流程是 stop → `appcenter-cli uninstall` → `appcenter-cli install-fpk`。
// 从 v2.24.5 起 fpk 里多了 `wizard/uninstall`（卸载前问「数据保留还是删除」），
// 飞牛应用中心命令行**不支持**回答卸载向导，于是 uninstall 在无交互环境下
// 直接被拒（返回值仍是 0，输出被 `>/dev/null` 吞掉）。后果是一条静默失败链：
//
//	uninstall 没真的执行（应用还在）→ install-fpk 对已安装应用是空操作
//	（只打印「Application [yinshu-ai] is installed.」）→ 旧进程还在 running
//	→ 磁盘上还是旧版 → 按版本号验收必然失败。
//
// 2026-11-02 用户拍板：卸载向导去掉（改成纯说明文字，不再要求选择），
// 但**升级链路仍然不走 uninstall/install-fpk** —— 官方 CLI 对已安装应用的
// install-fpk 就是空操作这条坑（HANDOVER §1.4.1）不随向导消失而消失，
// 而「就地升级」既不碰应用登记、也不碰数据目录，是唯一一条在真机上
// 可靠跑通的路径（2026-10-30 之前能成功的那套流程也一样依赖卸载）。
//
// 所以这里保留「就地升级」：摊包校验 → 备份旧程序 → 停进程 → 覆盖程序文件
// → 同步 manifest → 启动 → 按版本号验收 20 轮 → 不达标自动回退。
// 数据目录（var/ → @appdata）与配置目录（etc/ → @appconf）在应用目录之外，
// 全程不被触碰。
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
	b.WriteString("# 为什么放在 /tmp：更新过程中应用目录会被整体替换，脚本留在里面会被自己删掉。\n")
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
    log "更新流程失败（exit $rc），尝试拉起应用"
    appcenter-cli start "$PKG" >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap on_exit EXIT

log "开始更新：pkg=$PKG fpk=$FPK"
mkdir -p "$STAGE" /tmp/appcenter

# ── 3. 结果目录：兼容「平铺」与「带 app/ 子目录」两种布局 ────────────────
# target/ 是应用中心托管程序的那一层（只有 uninstall 才删，升级不动它）。
DEST="$APP_DIR/target"
if [ ! -d "$DEST" ]; then
  DEST="$APP_DIR"
fi

# ── 4. 摊包校验：先把新包解开，确认能装再动线上文件 ─────────────────────
# 摊包失败=什么都没发生，旧版继续跑。这是整个脚本唯一「必须成功才能继续」的前置。
UNPACK="$STAGE/unpack"
rm -rf "$UNPACK"
mkdir -p "$UNPACK"

# fpk 是 gzip 包着的 tar；里面通常还有一层 app.tgz（程序本体）。
if ! tar -xzf "$FPK" -C "$UNPACK" 2>/dev/null; then
  log "解包失败：安装包可能不完整，已放弃本次更新（应用未做任何改动）"
  exit 1
fi

if [ -f "$UNPACK/app.tgz" ]; then
  if ! tar -xzf "$UNPACK/app.tgz" -C "$UNPACK" 2>/dev/null; then
    log "解包失败：app.tgz 损坏，已放弃本次更新（应用未做任何改动）"
    exit 1
  fi
fi

# 找程序根：本体可能在顶层，也可能在 app/ 下。
if [ -f "$UNPACK/$PKG" ]; then
  SRC="$UNPACK"
elif [ -f "$UNPACK/app/$PKG" ]; then
  SRC="$UNPACK/app"
else
  log "安装包里找不到可执行文件 $PKG，已放弃本次更新（应用未做任何改动）"
  exit 1
fi
if [ ! -f "$SRC/cmd/main" ]; then
  log "安装包里找不到 cmd/main（飞牛用它启动应用），已放弃本次更新"
  exit 1
fi
chmod +x "$SRC/$PKG" "$SRC"/cmd/* 2>/dev/null || true
log "摊包完成：$SRC（目标版本 ${WANT_VERSION:-未知}）"

# ── 5. 备份旧程序（数据不在这一层，不会被碰到）──────────────────────────
BK="$STAGE/backup"
rm -rf "$BK"
mkdir -p "$BK"
for item in "$DEST"/*; do
  [ -e "$item" ] || continue
  name="$(basename "$item")"
  case "$name" in
    var|etc|home|meta|tmp|shares) continue ;;
    app.sock) continue ;;
  esac
  cp -a "$item" "$BK/" 2>/dev/null || true
done
log "已备份旧程序到 $BK"

# ── 6. 停进程 → 就地替换 → 拉起 ─────────────────────────────────────────
appcenter-cli stop "$PKG" >/dev/null 2>&1 || true
sleep 1

# 旧进程如果没被停掉，替换会失败（Text file busy），所以再硬停一次。
if command -v pkill >/dev/null 2>&1; then
  pkill -f "$DEST/$PKG" >/dev/null 2>&1 || true
  sleep 1
fi

replace_ok=1
for item in "$SRC"/*; do
  [ -e "$item" ] || continue
  name="$(basename "$item")"
  case "$name" in
    app.tgz|manifest|ICON*.PNG|manifest.json) continue ;;
  esac
  rm -rf "$DEST/$name"
  if ! cp -a "$item" "$DEST/" 2>/dev/null; then
    log "替换失败：$name 没能写到 $DEST"
    replace_ok=0
    break
  fi
done

if [ "$replace_ok" = "0" ]; then
  log "程序文件替换失败，回退到备份"
  for item in "$BK"/*; do
    [ -e "$item" ] || continue
    name="$(basename "$item")"
    # 备份里的 manifest 属于 $APP_DIR（不是 $DEST），单独还原。
    if [ "$name" = "manifest" ]; then
      cp -a "$item" "$APP_DIR/manifest" 2>/dev/null || true
      continue
    fi
    rm -rf "$DEST/$name"
    cp -a "$item" "$DEST/" 2>/dev/null || true
  done
  appcenter-cli start "$PKG" >/dev/null 2>&1 || true
  log "已回退到旧版。请到飞牛应用中心手动安装 $FPK"
  log "提示：卸载曲率时数据默认保留在 /vol1/@appdata/yinshu-ai，卸载前会自动备份到卷根目录 /vol1/yinshu-ai-backup-<时间戳>.tar.gz"
  exit 1
fi
chmod +x "$DEST/$PKG" "$DEST"/cmd/* 2>/dev/null || true

# manifest 也要跟着换：应用中心「已安装版本」读的是 $APP_DIR/manifest，
# 只换 target/ 会让应用中心永远显示旧版本（用户下次看到「可更新」又点一遍）。
# 先把旧 manifest 存进备份目录（回退时用得上），覆盖失败不算致命。
if [ -f "$SRC/manifest" ]; then
  cp -a "$APP_DIR/manifest" "$BK/manifest" 2>/dev/null || true
  if cp -a "$SRC/manifest" "$APP_DIR/manifest" 2>/dev/null; then
    log "manifest 已更新（应用中心将显示 ${WANT_VERSION:-新版本}）"
  else
    log "警告：manifest 没能更新，应用中心可能仍显示旧版本号（程序本体已替换）"
  fi
fi

log "程序文件已替换为 ${WANT_VERSION:-新版本}"

appcenter-cli start "$PKG" >/dev/null 2>&1 || true

# ── 7. 等它真的起来，并**按版本号验收** ─────────────────────────────────
# 只看 running 不算成功：老版本进程可能还没走，会误报「更新完成」。
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

# 版本已经对了就直接成功，不再多等。
i=1
while [ "$i" -le 20 ]; do
  got="$(running_version)"
  if [ -z "$WANT_VERSION" ]; then
    st="$(appcenter-cli status "$PKG" 2>&1 || true)"
    case "$st" in
      *running*)
        log "警告：本次没有目标版本号，无法按版本验收；只确认了 $PKG 在 running（实报版本 ${got:-未知}）"
        exit 0
        ;;
    esac
  elif [ "$got" = "$WANT_VERSION" ]; then
    log "更新完成：$PKG 已运行，版本 $got"
    exit 0
  fi
  log "第 $i 轮：实报版本 ${got:-未知}（期望 ${WANT_VERSION:-未知}），继续等待"
  appcenter-cli start "$PKG" >/dev/null 2>&1 || true
  sleep 2
  i=$((i + 1))
done

# ── 8. 验收没过：回退到备份，别把用户丢在一个起不来的版本上 ─────────────
log "更新后 20 轮仍未验收通过（期望版本 ${WANT_VERSION:-未知}，实报 ${got:-未知}），开始回退"
appcenter-cli stop "$PKG" >/dev/null 2>&1 || true
sleep 1
for item in "$BK"/*; do
  [ -e "$item" ] || continue
  name="$(basename "$item")"
  # manifest 属于 $APP_DIR（不在 $DEST 里），回退时要还原到它自己的位置，
  # 否则会在 target/ 下多出一个没人读的 manifest 副本，而应用中心仍显示新版号。
  if [ "$name" = "manifest" ]; then
    cp -a "$item" "$APP_DIR/manifest" 2>/dev/null || true
    continue
  fi
  rm -rf "$DEST/$name"
  cp -a "$item" "$DEST/" 2>/dev/null || true
done
chmod +x "$DEST/$PKG" 2>/dev/null || true
appcenter-cli start "$PKG" >/dev/null 2>&1 || true
sleep 2
log "已回退到旧版（实报版本 $(running_version)）。请到飞牛应用中心手动安装 $FPK"
log "提示：卸载曲率时数据默认保留在 /vol1/@appdata/yinshu-ai，卸载前会自动备份到卷根目录 /vol1/yinshu-ai-backup-<时间戳>.tar.gz"
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
