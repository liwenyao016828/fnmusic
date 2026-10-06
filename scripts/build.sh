#!/usr/bin/env bash
# ==============================================================================
#  fnOS (飞牛 NAS) 极光音乐 / fnmusic —— Linux / macOS 一键构建打包脚本
#
#  流程：前端构建(npm run build) → Go 交叉编译 linux/amd64 →
#        组装 app.tgz → 组装外层 .fpk → 打包后自检
#
#  实际打包逻辑全部在 scripts/build_fpk.py（纯 Python 标准库，无第三方依赖）。
#
#  用法：
#     ./scripts/build.sh                        # 完整构建（含前端）
#     ./scripts/build.sh --skip-frontend        # 跳过前端，复用 backend/dist
#     ./scripts/build.sh --version 1.2.8        # 指定 manifest 版本号
#     ./scripts/build.sh --output /tmp/a.fpk    # 指定输出路径
#     ./scripts/build.sh --go /path/to/go       # 指定 go 可执行文件
#     ./scripts/build.sh --legacy-modes         # 使用与原 fpk 相同的 0666/0777 权限位
#
#  本脚本会自动探测 Go 工具链，包括仓库内置的 .tools/go。
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

log()  { printf '\033[36m[信息]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
err()  { printf '\033[31m[错误]\033[0m %s\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# 1. 探测 Python 3
# ---------------------------------------------------------------------------
PY=""
for cand in python3 python; do
  if command -v "$cand" >/dev/null 2>&1; then
    if "$cand" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' >/dev/null 2>&1; then
      PY="$(command -v "$cand")"
      break
    fi
  fi
done
if [ -z "$PY" ]; then
  err "未找到 Python 3（>=3.8）。打包脚本 scripts/build_fpk.py 需要 python3。"
  exit 1
fi
log "Python: $PY ($("$PY" -V 2>&1))"

# ---------------------------------------------------------------------------
# 2. 探测 Go 工具链（PATH → 仓库内置 .tools/go → 常见安装位置）
# ---------------------------------------------------------------------------
GO_EXE=""
GO_CANDIDATES=()
if [ -n "${GO:-}" ]; then GO_CANDIDATES+=("${GO}"); fi
if command -v go >/dev/null 2>&1; then GO_CANDIDATES+=("$(command -v go)"); fi
GO_CANDIDATES+=(
  "${ROOT_DIR}/.tools/go/bin/go"
  "/usr/local/go/bin/go"
  "/usr/lib/go/bin/go"
  "${HOME}/go/bin/go"
  "${HOME}/sdk/go/bin/go"
)

for cand in "${GO_CANDIDATES[@]}"; do
  if [ -x "$cand" ]; then GO_EXE="$cand"; break; fi
done

if [ -z "$GO_EXE" ]; then
  err "未找到 Go 工具链。请安装 Go 1.22+，或设置 GO=/path/to/go，"
  err "或将 go 放到 ${ROOT_DIR}/.tools/go/bin/go。"
  exit 1
fi

# 若 go 不在 PATH 中，导出其运行所需环境（与 README/CI 约定一致）
GO_DIR="$(cd "$(dirname "$GO_EXE")" && pwd)"
if ! command -v go >/dev/null 2>&1; then
  export GOROOT="${GOROOT:-$(cd "${GO_DIR}/.." && pwd)}"
  export PATH="${GO_DIR}:${PATH}"
fi
export GOCACHE="${GOCACHE:-${ROOT_DIR}/.tools/gocache}"
export GOPATH="${GOPATH:-${ROOT_DIR}/.tools/gopath}"
[ -d "$GOCACHE" ] || mkdir -p "$GOCACHE"
[ -d "$GOPATH" ]  || mkdir -p "$GOPATH"
log "Go:     $GO_EXE ($("$GO_EXE" version 2>&1))"
log "GOROOT=${GOROOT:-<自动>}  GOCACHE=${GOCACHE}  GOPATH=${GOPATH}"

# ---------------------------------------------------------------------------
# 3. 调用零依赖 Python 打包器（透传全部参数）
# ---------------------------------------------------------------------------
log "仓库根目录：${ROOT_DIR}"
cd "${ROOT_DIR}"
exec "$PY" "${SCRIPT_DIR}/build_fpk.py" --go "$GO_EXE" "$@"
