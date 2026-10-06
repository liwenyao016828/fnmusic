#!/bin/bash
cd /home/liwenyao/projects/music-v2 || exit 1

echo "== 后端 =="
find backend -name "*.go" -exec dirname {} \; | sort -u > /tmp/pkgs.txt
printf "pkgs=%s\n" "$(wc -l < /tmp/pkgs.txt)"
printf "files=%s\n" "$(find backend -name '*.go' | wc -l)"
printf "lines=%s\n" "$(find backend -name '*.go' -exec cat {} + | wc -l)"
printf "nontest=%s\n" "$(find backend -name '*.go' ! -name '*_test.go' -exec cat {} + | wc -l)"
printf "test=%s\n" "$(find backend -name '*_test.go' -exec cat {} + | wc -l)"
printf "main=%s\n" "$(cat backend/cmd/main/main.go 2>/dev/null | wc -l)"
printf "testfuncs=%s\n" "$(grep -rhoE '^func Test[A-Za-z0-9_]+' --include='*_test.go' backend | wc -l)"

echo "== 各包行数（含测试）=="
for d in $(find backend -name "*.go" -exec dirname {} \; | sort -u); do
  n=$(find "$d" -maxdepth 1 -name "*.go" -exec cat {} + | wc -l)
  printf "%6d  %s\n" "$n" "${d#backend/}"
done | sort -rn

echo "== 前端 =="
printf "vue=%s\n" "$(find frontend/src -name '*.vue' | wc -l)"
printf "vuejs_lines=%s\n" "$(find frontend/src \( -name '*.vue' -o -name '*.js' \) -exec cat {} + | wc -l)"
printf "fe_tests=%s\n" "$(cd frontend && npm test 2>&1 | grep -E '^# pass' | tr -dc '0-9')"

echo "== 文档 =="
hl=$(wc -l < HANDOVER.md); hb=$(wc -c < HANDOVER.md)
hist=$(find docs/变更记录 -name '*.md' -exec cat {} + 2>/dev/null | wc -l)
histb=$(find docs/变更记录 -name '*.md' -exec cat {} + 2>/dev/null | wc -c)
printf "handover_lines=%s handover_bytes=%s\n" "$hl" "$hb"
printf "changelog_lines=%s changelog_bytes=%s\n" "$hist" "$histb"
# 护栏：HANDOVER.md 是接手者第一个读的东西，只该放「活文档」（§0–§10）；历史条目去 docs/变更记录/
if [ "$hl" -gt 3000 ]; then
  printf "WARN HANDOVER.md 已 %s 行（护栏 3000，基线 2,706）：新条目应写进 docs/变更记录/ 当月文件，而不是继续追加到这里\n" "$hl"
fi
