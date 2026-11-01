package applog

// 日志里的「地址」只记域名（2026-11-01 用户口径：「日志不得泄露带 token 的完整直链（只记域名）」）。
//
// 为什么必须统一走这里：音源解析出来的直链几乎都带签名查询串
//（`?token=…&sign=…&expire=…`，或把参数字段塞进 path），整条打进日志等于把
// **可用的下载凭据**写进内存缓冲、进而写进用户可能开启的 qulv.log。日志是给人
// 排查「为什么下不下来」用的，需要的是「哪个 CDN / 哪个域名」，不是那条链接本身。
//
// 用法：`applog.HostOf(a.url)` → `"cdn.migu.cn"`。取不出来时返回固定占位符，
// 绝不回退到原文（回退等于把兜底路径又变成泄露点）。

import (
	"net/url"
	"strings"
)

// hostUnknown 无法解析出域名时的占位符。
const hostUnknown = "(地址无法解析)"

// HostOf 从一个 URL 里只取域名（含子域，去掉端口与路径/查询串）。
//
// 行为约定：
//   - `https://a.b.c:8443/x?token=1` → `a.b.c`；
//   - 协议相对写法的 `//a.b.c/x` 也能识别（音源脚本里常见）；
//   - 空串 / 解析失败 / 没有 host → `(地址无法解析)`；
//   - 结果一律小写：同一域名在日志里要能 grep 到一处。
func HostOf(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return hostUnknown
	}
	// 协议相对写法（`//host/path`）补个 scheme 才能被 url.Parse 正确拆出 host。
	if strings.HasPrefix(s, "//") {
		s = "http:" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return hostUnknown
	}
	h := strings.ToLower(u.Hostname())
	if h == "" {
		return hostUnknown
	}
	return h
}
