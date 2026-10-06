#!/bin/bash
# 实测网易云二维码登录链路，判断到底哪一步失效
set -u
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

echo "=== 1. 申请 unikey ==="
RESP=$(curl -s --noproxy '*' -m 15 "https://music.163.com/api/login/qrcode/unikey?type=1" -H "User-Agent: $UA" -H "Referer: https://music.163.com/")
echo "响应: $RESP"
K=$(echo "$RESP" | sed -n 's/.*"unikey":"\([^"]*\)".*/\1/p')
echo "unikey = $K"
[ -z "$K" ] && exit 1

echo
echo "=== 2. 轮询扫码状态（未扫码时应为 801） ==="
curl -s --noproxy '*' -m 15 "https://music.163.com/api/login/qrcode/client/login?type=1&key=$K" \
  -H "User-Agent: $UA" -H "Referer: https://music.163.com/"
echo

echo
echo "=== 3. 二维码指向的 URL 是否还活着 ==="
for U in \
  "https://music.163.com/login?codekey=$K" \
  "https://music.163.com/login?codekey=$K&chainId=null" \
  ; do
  printf '%-70s ' "$U"
  curl -s --noproxy '*' -m 20 -o /tmp/wy_qr.html -w "HTTP %{http_code} size=%{size_download}\n" -L "$U" -H "User-Agent: $UA"
done

echo
echo "=== 4. 二维码页里是否出现「请切换其他登录方式」字样 ==="
if [ -f /tmp/wy_qr.html ]; then
  if grep -q "请切换其他登录方式" /tmp/wy_qr.html; then
    echo "⚠️ 命中！说明该 URL 已被网易废弃"
    grep -o "[^<>]*请切换其他登录方式[^<>]*" /tmp/wy_qr.html | head -3
  else
    echo "未命中该文案（size=$(wc -c < /tmp/wy_qr.html)）"
  fi
  echo "--- 页面文本片段 ---"
  sed -e 's/<[^>]*>/ /g' /tmp/wy_qr.html | tr -s ' \n' ' \n' | head -20
fi
