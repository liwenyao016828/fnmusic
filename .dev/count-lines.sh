#!/bin/bash
# 统计后端各包总行数（生产+测试），供更新 HANDOVER 的代码地图
cd /home/liwenyao/projects/music-v2/backend || exit 1

echo "=== 各包总行数（含测试）==="
for d in pkg/*/; do
  n=$(basename "$d")
  lines=$(find "$d" -maxdepth 1 -name '*.go' -exec cat {} + 2>/dev/null | wc -l)
  printf '%s %d\n' "$n" "$lines"
done | sort -k2 -rn | awk '{printf "  %-14s %6d\n", $1, $2}'

echo
echo "=== 总计 ==="
prod=$(find pkg/ -name '*.go' ! -name '*_test.go' -exec cat {} + | wc -l)
tst=$(find pkg/ -name '*_test.go' -exec cat {} + | wc -l)
main=$(cat main.go | wc -l)
printf '  生产代码 : %s 行\n' "$prod"
printf '  测试代码 : %s 行\n' "$tst"
printf '  main.go  : %s 行\n' "$main"
printf '  合计     : %s 行\n' "$((prod + tst + main))"
printf '  包数量   : %s\n' "$(ls -d pkg/*/ | wc -l)"
