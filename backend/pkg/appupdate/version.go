// Package appupdate 提供「应用内检查更新 + 下载 + 自安装」。
//
// 为什么放在独立包里而不是塞进 pkg/api：
//   - 版本比较、GitHub 响应解析、安装脚本生成都是**纯逻辑**，不碰 HTTP 框架，
//     这样测试不需要起 Server，也不会有「测一个版本比较要构造整个 API」的负担；
//   - 安装动作会**杀掉当前进程**（appcenter-cli uninstall），把它和 HTTP 处理器
//     混在一起，任何一次误调用都会让调试中的服务直接消失。隔离一层之后，
//     「能不能装」的判断集中在 Manager.Install 里，只有显式 POST 才会触发。
//
// 参考实现是 fn-knock（Rust）的 update 模块：检查 GitHub releases/latest →
// 下载 .fpk → 写一个脱离当前进程的 shell 脚本去 stop/uninstall/install。
package appupdate

import (
	"strconv"
	"strings"
)

// CompareVersion 按语义化版本比较 a 与 b：a<b 返回 -1，相等 0，a>b 返回 1。
//
// 只比较**数字段**（缺位补 0），因为本项目的版本就是 2.17.15 / 2.18.0 这种
// 纯数字三段式；GitHub 的 tag 可能带 v 前缀（v2.18.1）或预发布后缀
// （2.19.0-beta.1），这里统一剥掉前缀、忽略 `-` 之后的后缀 ——
// 预发布版本**不当作正式更新**推给用户，否则一个 beta tag 会让所有稳定版用户
// 被提示升级到测试版。
//
// 不使用第三方 semver 库：多一个依赖只为比较三个整数不值得，而且各家 semver
// 库对预发布/build metadata 的处理各不相同，反而要额外解释。
func CompareVersion(a, b string) int {
	as := versionSegments(a)
	bs := versionSegments(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// IsNewer 报告 candidate 是否比 base 新。
func IsNewer(candidate, base string) bool {
	return CompareVersion(candidate, base) > 0
}

// versionSegments 把 "v2.18.1" 拆成 [2,18,1]；非数字段按 0 处理。
func versionSegments(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	// 1.2.3-rc.1 / 1.2.3+build → 只看 1.2.3
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
