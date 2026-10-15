#!/bin/bash
# ============================================================================
#  极光音乐 · WSL(Linux) 侧统一入口
#
#  仓库位置：/home/liwenyao/projects/music-v2 （WSL 原生 ext4，inotify 正常）
#
#  用法：
#      bash .dev/wsl-run.sh status      # 工具链与仓库状态
#      bash .dev/wsl-run.sh test        # 全部测试（后端 go test + 前端 node --test）
#      bash .dev/wsl-run.sh build       # 仅编译
#      bash .dev/wsl-run.sh fpk         # 完整打包 FPK（含前端）
#      bash .dev/wsl-run.sh fpk-fast    # 打包 FPK（跳过前端，复用 dist）
#      bash .dev/wsl-run.sh dev         # 开发环境（前端 :3000 热更新 + 后端 :8900）
#      bash .dev/wsl-run.sh dev-fe      # 仅前端热加载
#      bash .dev/wsl-run.sh dev-be      # 仅后端
#
#  从 Windows 调用：
#      wsl.exe -d Debian -- bash /home/liwenyao/projects/music-v2/.dev/wsl-run.sh test
# ============================================================================
set -u

REPO=/home/liwenyao/projects/music-v2
export GOCACHE="$HOME/.cache/go-build"
export GOPATH="$HOME/go"
export FN_ALLOWED_ROOTS="$REPO/.dev/music"
mkdir -p "$GOCACHE" "$GOPATH" "$REPO/.dev/music" "$REPO/.dev/data"

cmd_status() {
  echo "=== 工具链 ==="
  echo "go   : $(go version 2>/dev/null || echo 未安装)"
  echo "node : $(node -v 2>/dev/null || echo 未安装)  ($(command -v node))"
  echo "npm  : $(npm -v 2>/dev/null || echo 未安装)"
  echo "py   : $(python3 -V 2>&1)"
  echo
  echo "=== 仓库 ==="
  if [ -d "$REPO" ]; then
    echo "路径    : $REPO"
    echo "文件数  : $(find "$REPO" -type f -not -path '*/node_modules/*' 2>/dev/null | wc -l)（不含 node_modules）"
    echo "版本号  : $(grep -oE 'CurrentVersion = \"[^\"]+\"' "$REPO/backend/pkg/api/server.go" 2>/dev/null | head -1)"
    echo "manifest: $(grep -E '^version=' "$REPO/fpk-package/manifest" 2>/dev/null)"
    echo "依赖    : node_modules $([ -d "$REPO/frontend/node_modules" ] && echo 已装 || echo 未装)"
  else
    echo "仓库不可达: $REPO"; return 1
  fi
  echo
  echo "=== 文件系统（inotify 支持检测）==="
  echo "仓库挂载点: $(df -T "$REPO" 2>/dev/null | tail -1 | awk '{print $1" ("$2")"}')"
  echo "（ext4/overlay = 原生，inotify 可用，热更新正常；drvfs/9p = /mnt 挂载，热更新需轮询）"
}

cmd_build() { cd "$REPO/backend" && go build ./... && echo "编译通过 ✓"; }

cmd_test() {
  local rc=0

  echo "=== 后端（go test ./...）==="
  cd "$REPO/backend" || return 1
  go test ./... 2>&1 | tail -22
  local pass fail
  pass=$(go test ./... -v 2>&1 | grep -cE '^--- PASS')
  fail=$(go test ./... -v 2>&1 | grep -cE '^--- FAIL')
  echo "PASS=$pass  FAIL=$fail"
  [ "$fail" = "0" ] || { echo "失败项："; go test ./... -v 2>&1 | grep -E '^--- FAIL'; rc=1; }

  echo
  echo "=== 前端（node --test src/**/*.test.mjs）==="
  # 前端测试是**零依赖**的 node --test 脚本（引擎层纯逻辑，不需要 jsdom）。
  # 之前没接进这个入口，等于没人跑 —— 现在一起跑。
  # ⚠️ 必须用 `"src/**/*.test.mjs"` 这种**带引号的 glob 字符串**（Node 自己展开），
  # 不能写 `src/engine/*.test.mjs` —— 那样会漏掉 `src/services/` 下的用例
  # （appLog / userMsg / subscribe），跑出来是 52 而不是 81，**静默少跑**。
  # 也不能写 `node --test src/`：Node 22 会把目录当模块路径解析，直接 MODULE_NOT_FOUND。
  cd "$REPO/frontend" || return 1
  local fe_out fe_pass fe_fail
  fe_out=$(node --test "src/**/*.test.mjs" 2>&1)
  fe_pass=$(printf '%s\n' "$fe_out" | grep -oE '^# pass [0-9]+' | awk '{print $3}')
  fe_fail=$(printf '%s\n' "$fe_out" | grep -oE '^# fail [0-9]+' | awk '{print $3}')
  echo "PASS=${fe_pass:-?}  FAIL=${fe_fail:-?}"
  if [ "${fe_fail:-1}" != "0" ]; then
    echo "失败项："
    printf '%s\n' "$fe_out" | grep -E '^not ok'
    rc=1
  fi

  echo
  [ "$rc" = "0" ] && echo "全部通过 ✓" || echo "存在失败 ✗"
  return $rc
}

cmd_fpk()      { cd "$REPO" && bash scripts/build.sh "$@" 2>&1 | tail -8; ls -la "$REPO"/*.fpk; }
cmd_fpk_fast() { cd "$REPO" && bash scripts/build.sh --skip-frontend "$@" 2>&1 | tail -8; ls -la "$REPO"/*.fpk; }

cmd_dev() {
  cd "$REPO" || return 1
  echo "开发环境：前端 :3000（热更新）+ 后端 :8900"
  echo "浏览器访问 http://localhost:3000/"
  bash scripts/dev.sh
}

cmd_dev_fe() {
  cd "$REPO/frontend" || return 1
  export VITE_API_TARGET="http://127.0.0.1:${FN_DEV_PORT:-8900}"
  exec npm run dev -- --port 3000 --strictPort
}

cmd_dev_be() {
  cd "$REPO/backend" || return 1
  go build -o "$REPO/.dev/fn-dev-backend" . || return 1
  cd "$REPO/.dev"
  exec ./fn-dev-backend -port "${FN_DEV_PORT:-8900}" -data "$REPO/.dev/data"
}

case "${1:-status}" in
  status)   cmd_status ;;
  build)    cmd_build ;;
  test)     cmd_test ;;
  fpk)      shift; cmd_fpk "$@" ;;
  fpk-fast) shift; cmd_fpk_fast "$@" ;;
  dev)      cmd_dev ;;
  dev-fe)   cmd_dev_fe ;;
  dev-be)   cmd_dev_be ;;
  *) echo "未知命令: $1"; echo "可用: status | build | test | fpk | fpk-fast | dev | dev-fe | dev-be"; exit 1 ;;
esac
