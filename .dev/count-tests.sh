#!/bin/bash
# 统计 Go 测试结果
export GOCACHE="$HOME/.cache/go-build"
export GOPATH="$HOME/go"
cd /home/liwenyao/projects/music-v2/backend || exit 1

OUT=$(go test ./... -v 2>&1)
PASS=$(printf '%s\n' "$OUT" | grep -c '^--- PASS')
FAIL=$(printf '%s\n' "$OUT" | grep -c '^--- FAIL')

echo "PASS=$PASS"
echo "FAIL=$FAIL"

if [ "$FAIL" != "0" ]; then
  echo "--- 失败项 ---"
  printf '%s\n' "$OUT" | grep '^--- FAIL'
  exit 1
fi

echo "--- 各包结果 ---"
go test ./... 2>&1 | grep -E '^(ok|FAIL|---)' 
echo
echo "全部通过"
