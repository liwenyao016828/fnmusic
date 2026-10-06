#!/bin/bash
# 探测 type 合法取值
set -u
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
for T in 0 1 2 3 4 5 6 7 10 99; do
  R=$(curl -s --noproxy '*' -m 12 "https://music.163.com/api/login/qrcode/unikey?type=${T}" \
      -H "User-Agent: $UA" -H "Referer: https://music.163.com/")
  echo "type=${T}  ->  ${R}"
done
