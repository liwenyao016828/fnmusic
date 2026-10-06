#!/usr/bin/env bash
# ============================================================================
#  极光音乐 · 前端热加载开发环境
#
#  一条命令即可在浏览器里看到真实的前端界面，改代码立即热更新，
#  完全不需要打包 FPK、也不需要安装。
#
#  用法：
#      bash scripts/dev.sh                 # 默认前端 3000，开发后端 8900
#      bash scripts/dev.sh --port 3100     # 自定义前端端口
#      bash scripts/dev.sh --no-backend    # 只起前端（复用已在运行的后端）
#      FN_ALLOWED_ROOTS=/your/music bash scripts/dev.sh   # 指定可浏览的曲库根
#
#  说明：本机若已安装运行了正式版极光音乐（占用 8899），开发后端会自动避开，
#        使用独立端口 + 独立数据目录，不会影响正式实例。
# ============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_DIR="$ROOT/frontend"
DEV_DIR="$ROOT/.dev"
DEV_DATA_DIR="${FN_DEV_DATA:-$DEV_DIR/data}"
DEV_MUSIC_DIR="${FN_DEV_MUSIC:-$DEV_DIR/music}"

FRONTEND_PORT=3000
BACKEND_PORT="${FN_DEV_PORT:-8900}"
START_BACKEND=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)        FRONTEND_PORT="$2"; shift 2 ;;
    --backend-port) BACKEND_PORT="$2"; shift 2 ;;
    --no-backend)  START_BACKEND=0; shift ;;
    -h|--help)
      sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "未知参数: $1（用 --help 查看用法）" >&2; exit 1 ;;
  esac
done

log()  { printf '\033[36m[dev]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[dev]\033[0m %s\n' "$*"; }

# ── 1. 探测 Go ──
find_go() {
  if command -v go >/dev/null 2>&1; then command -v go; return; fi
  local c
  for c in "$ROOT/.tools/go/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
    [[ -x "$c" ]] && { echo "$c"; return; }
  done
  echo ""
}

# ── 2. 确保前端依赖已安装 ──
if [[ ! -d "$FRONTEND_DIR/node_modules" ]]; then
  warn "未检测到 frontend/node_modules，正在安装依赖…"
  # 默认 npm 缓存目录在本机可能是 root 属主，必须指定可写的缓存目录
  (cd "$FRONTEND_DIR" && npm install --no-audit --no-fund --cache "$ROOT/.npm-cache")
fi

mkdir -p "$DEV_DATA_DIR" "$DEV_MUSIC_DIR"

# ── 3. 准备开发数据目录（复用正式实例的音源配置，但改掉目录指向）──
PROD_CONFIG="/vol1/@appdata/fn-lx-player/data/config.json"
DEV_CONFIG="$DEV_DATA_DIR/config.json"
if [[ ! -f "$DEV_CONFIG" && -f "$PROD_CONFIG" ]]; then
  log "复用正式实例的音源配置（复制到 $DEV_CONFIG）"
  python3 - "$PROD_CONFIG" "$DEV_CONFIG" "$DEV_MUSIC_DIR" "$BACKEND_PORT" <<'PY'
import json, shutil, sys
src, dst, music, port = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
shutil.copyfile(src, dst)
try:
    d = json.load(open(dst, encoding='utf-8'))
except Exception:
    sys.exit(0)
d['port'] = port
d['default_nas_dir'] = music
d['download_dir'] = music
json.dump(d, open(dst, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
PY
fi

# ── 4. 构建并启动开发后端 ──
BACKEND_PID=""
cleanup() {
  if [[ -n "$BACKEND_PID" ]] && kill -0 "$BACKEND_PID" 2>/dev/null; then
    log "停止开发后端 (PID $BACKEND_PID)"
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

if [[ "$START_BACKEND" == "1" ]]; then
  GO_BIN="$(find_go)"
  if [[ -z "$GO_BIN" ]]; then
    warn "未找到 Go 工具链，跳过开发后端构建（可用 --no-backend 并自备后端）"
  else
    log "构建开发后端…"
    BIN="$DEV_DIR/fn-dev-backend"
    (
      cd "$ROOT/backend"
      # MSYS 只自动转换命令行参数，不转换环境变量值：Git Bash 下 $ROOT 形如 /d/...，
      # 直接塞进 GOROOT/GOCACHE/GOPATH 会被原生 go 判为非法（cannot find GOROOT
      # directory / not an absolute path）。有 cygpath 时转成原生路径；Linux 上无此命令，保持原样。
      to_native() {
        if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
      }
      # go 常常是符号链接（如 /usr/local/bin/go -> /usr/local/go/bin/go，或用 update-alternatives
      # 管理）。必须先解析出真实路径再推导 GOROOT，否则 dirname 两次会得到 /usr/local 这类
      # 错误前缀，导致 "package context is not in std (/usr/local/src/context)"。
      GO_REAL="$GO_BIN"
      if command -v readlink >/dev/null 2>&1; then
        GO_REAL="$(readlink -f "$GO_BIN" 2>/dev/null || printf '%s' "$GO_BIN")"
      fi
      GOROOT_DERIVED="$(dirname "$(dirname "$GO_REAL")")"
      # 仅当推导结果确实像一个 GOROOT（含 src 或 pkg）且用户未显式指定时才导出
      if [ -z "${GOROOT:-}" ] && { [ -d "$GOROOT_DERIVED/src" ] || [ -d "$GOROOT_DERIVED/pkg" ]; }; then
        export GOROOT="$(to_native "$GOROOT_DERIVED")"
      fi
      export PATH="$(dirname "$GO_BIN"):$PATH"
      export GOCACHE="$(to_native "$ROOT/.tools/gocache")"
      export GOPATH="$(to_native "$ROOT/.tools/gopath")"
      "$GO_BIN" build -o "$BIN" .
    )

    # 端口冲突时自动后移
    while (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -q ":${BACKEND_PORT} "; do
      warn "端口 $BACKEND_PORT 已被占用，尝试 $((BACKEND_PORT + 1))"
      BACKEND_PORT=$((BACKEND_PORT + 1))
    done

    log "启动开发后端 :$BACKEND_PORT（数据目录 $DEV_DATA_DIR）"
    (
      cd "$DEV_DIR"
      exec "$BIN" -port "$BACKEND_PORT" -data "$DEV_DATA_DIR"
    ) >"$DEV_DIR/backend.log" 2>&1 &
    BACKEND_PID=$!
    sleep 1.5

    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      warn "开发后端启动失败，日志末尾："
      tail -20 "$DEV_DIR/backend.log" || true
      exit 1
    fi

    # 等健康检查通过
    for _ in $(seq 1 20); do
      if curl -fsS -m 2 "http://127.0.0.1:$BACKEND_PORT/api/health" >/dev/null 2>&1; then
        log "开发后端就绪 ✓"
        break
      fi
      sleep 0.3
    done
  fi
fi

# ── 5. 启动 Vite 热加载 ──
echo
printf '\033[32m  ┌────────────────────────────────────────────────┐\033[0m\n'
printf '\033[32m  │  打开浏览器访问（改代码会立即热更新）          │\033[0m\n'
printf '\033[32m  │  本机：  http://localhost:%s/\033[0m\n' "$FRONTEND_PORT"
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
if [[ -n "${LAN_IP:-}" ]]; then
  printf '\033[32m  │  局域网：http://%s:%s/\033[0m\n' "$LAN_IP" "$FRONTEND_PORT"
fi
printf '\033[32m  │  停止：  Ctrl+C                                 │\033[0m\n'
printf '\033[32m  └────────────────────────────────────────────────┘\033[0m\n'
echo

cd "$FRONTEND_DIR"
export VITE_API_TARGET="http://127.0.0.1:$BACKEND_PORT"
# 不使用 exec：保留 trap，Ctrl+C 时能一并停掉开发后端
npm run dev -- --port "$FRONTEND_PORT" --strictPort
