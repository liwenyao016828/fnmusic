#!/usr/bin/env bash
# 手动重跑当前 main 的检查/发布；日常 push main 会自动触发。
# 不改版本、不暂存工作区、不绕过测试，也不在本机另造发布流程。
set -euo pipefail
if [ "$#" -ne 0 ]; then
  echo '用法: bash scripts/release.sh（重跑远端 main，不接受版本号）' >&2
  exit 2
fi
command -v gh >/dev/null || { echo '需要已登录的 GitHub CLI (gh)' >&2; exit 1; }
gh workflow run release.yml --ref main
printf '%s\n' '已请求运行 Release；用 gh run list --workflow release.yml 查看结果。'
