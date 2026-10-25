package config

import (
	"net/url"
	"strings"
)

// ── 应用内更新的「镜像」清单（用户 2026-10-25）──
//
// 用户原话：「增加默认镜像站，可删减增加。」「可以填自己的」「回退」。
//
// 设计要点（都是踩过坑之后定的，改之前请先读）：
//
//  1. **公共「GitHub 加速镜像」几乎都不代理 api.github.com** —— 它们只代理
//     github.com / raw / objects.githubusercontent.com。而更新的第一步就是请求
//     https://api.github.com/repos/<repo>/releases/latest。所以「只加速下载」的
//     镜像在这条链路上等于没配：第一步就 i/o timeout，走不到下载那步。
//     实测（2026-10-25）：能同时吃下 api.github.com 前缀的只有 gh-proxy.com；
//     ghfast.top / ghproxy.net 返回 "Invalid input."，ghproxy.cn 返人机验证页，
//     gh-proxy.net 返 JS 跳转页，ghps.cc 403，gitmirror / moeyy 不通，
//     github.com.cnpmjs.org 与 hub.fastgit.org 自 2022 年就关停了。
//     所以**默认清单只有 gh-proxy.com 一条**（用户拍板：「只放」）。
//
//  2. 清单末尾永远有「GitHub 官方源」这条**内置兜底**（用户拍板：「回退」）。
//     它不落盘、不可删除，只能临时停用（停用后真就只剩镜像了，界面上要说清楚）。
//
//  3. 允许**自建**：要么填一个纯前缀（`https://你的域名/`，我们拼在原地址前面），
//     要么填一个「更新源」完整地址（带 `{repo}` 占位，比如把自己托管的
//     releases/latest JSON 放上去）。两种形态各有用处，别只留一种。

// UpdateMirror 是一条镜像配置。
type UpdateMirror struct {
	// Name 是界面上的显示名（可空，空时按值推导）。
	Name string `json:"name,omitempty"`
	// URL 是镜像地址。
	//
	//	前缀型：`https://gh-proxy.com` / `https://你的反代/gh/`
	//	        使用时拼成 `URL + 原始地址`（原地址原样跟在后面）。
	//	源型：  `https://你的域名/yinshu/{repo}/latest.json`
	//	        含 `{repo}` 占位，直接当作「releases/latest」的完整地址。
	URL string `json:"url"`
	// Disabled 为 true 表示保留但不用（界面上是「停用」而不是「删除」）。
	Disabled bool `json:"disabled,omitempty"`
}

// OfficialMirrorURL 是内置 GitHub 官方源在清单里的占位 URL（只表示「被停用」）。
// 与 appupdate.OfficialMirrorURL 同值；定义在这里是因为**校验**发生在这一层。
const OfficialMirrorURL = "github://official"

// BuiltinMirrorID 是内置 GitHub 官方源的稳定标识（前端用来画「不可删除」）。
const BuiltinMirrorID = "official"

// UpdateMirrorsConfigured 判断配置里是否**显式写过**镜像清单。
//
// 为什么需要这个：JSON 里「空数组」和「没这个键」解码后都是 len==0，但语义
// 完全不同 —— 前者是「我要用官方源，别给我加镜像」，后者是「没配过，用默认」。
// 用 *[]UpdateMirror 才能分开（config 里镜像清单就是用指针存的）。
func UpdateMirrorsConfigured(list *[]UpdateMirror) bool { return list != nil }

// DefaultUpdateMirrors 返回内置默认镜像清单。
//
// 返回副本，调用方随便改 —— 直接返回内部切片会让「界面上的增删」改到默认值上，
// 下次「恢复默认」就恢复不回来了。
func DefaultUpdateMirrors() []UpdateMirror {
	return []UpdateMirror{{Name: "gh-proxy", URL: "https://gh-proxy.com"}}
}

// UpdateMirrorsForUI 返回**给界面看**的完整清单：用户清单 + 官方源那一条。
//
// 官方源不落盘（它是内置兜底），但界面必须能看见它，否则「停用官方源」这个开关
// 没有落点，用户也看不到「现在到底会用哪些源」。所以：
//   - 清单里已经有 github://official 占位（= 被停用过）就原样用；
//   - 没有就补一条 `{name:"GitHub 官方", url:OfficialMirrorURL, disabled:false}` 放末尾。
//
// 直接返回 NormalizeUpdateMirrorList 的结果，前端「原样回显」即可，不需要再补默认值。
func (a AppConfig) UpdateMirrorsForUI() []UpdateMirror {
	list := a.UpdateMirrorList()
	has := false
	for _, m := range list {
		if m.URL == OfficialMirrorURL {
			has = true
			break
		}
	}
	if !has {
		list = append(list, UpdateMirror{Name: "GitHub 官方", URL: OfficialMirrorURL})
	}
	return list
}

// NormalizeUpdateMirror 归一一条镜像；不合法时返回 ok=false。
//
// 归一做的事：去空白、补 scheme（`gh-proxy.com` → `https://gh-proxy.com`）、
// 前缀型去掉末尾多余的 `/`（拼接时代码负责补）。
// **不校验**域名是否可达：可达性只能靠实测，配置阶段一律放行（自建内网地址
// 也可能合法）。
func NormalizeUpdateMirror(m UpdateMirror) (UpdateMirror, bool) {
	raw := strings.TrimSpace(m.URL)
	if raw == "" {
		return UpdateMirror{}, false
	}
	// 内置官方源的占位 URL 必须**在 url.Parse 之前**特判。
	//
	// ⚠️ 曾经的写法是「解析后 u.Host == "" 才认占位」，看着没问题，实际永远走不到：
	// url.Parse("github://official") 的 Host 是 "official"（非空），随后就被
	// scheme 检查（github 不是 http/https）判成非法 —— 表现为界面里「停用官方源」
	// 一保存就消失，而且没有任何报错。
	//
	// Disabled 要跟着用户填的走，不能强制置 true：真实值是 false 时
	// 「停用官方源」反而会变成「启用官方源」，debug 起来极难看出来。
	if raw == OfficialMirrorURL {
		return UpdateMirror{Name: strings.TrimSpace(m.Name), URL: raw, Disabled: m.Disabled}, true
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return UpdateMirror{}, false
	}
	// 源型镜像（含 {repo} 占位）里的花括号会被 url.Parse 当成非法字符，
	// 直接判 false 会让「自建更新源」这条整个功能静默失效 —— 用户填了、界面
	// 显示存了、实际一个字节都没用上。这里先按去掉花括号的形态校验 host。
	if strings.Contains(raw, "{repo}") {
		probe := strings.ReplaceAll(raw, "{repo}", "repo-probe")
		if u2, err2 := url.Parse(probe); err2 != nil || u2.Host == "" {
			return UpdateMirror{}, false
		}
		return UpdateMirror{
			Name:     pickMirrorName(m.Name, u.Host),
			URL:      strings.TrimRight(raw, "/"),
			Disabled: m.Disabled,
		}, true
	}
	// 镜像必须是 http(s)：其他 scheme 要么不被 http.Client 支持，要么就得改
	// Transport，不值得为「配置项」增加这份复杂度。
	// （这个分支原先写反了：case "http","https" 里返回 false，等于把所有合法
	//  镜像全丢掉 —— 2026-10-25 加镜像功能时踩到的，务必保持下面这版。）
	if sch := strings.ToLower(u.Scheme); sch != "http" && sch != "https" {
		return UpdateMirror{}, false
	}
	// 源型地址里的 `{repo}` 会让 url.Parse 正常通过（{} 在 path 里合法），
	// 这里不能把它当成非法；前缀型去掉末尾 `/` 让拼接只有一个分隔符。
	out := UpdateMirror{
		Name:     strings.TrimSpace(m.Name),
		URL:      strings.TrimRight(raw, "/"),
		Disabled: m.Disabled,
	}
	out.Name = pickMirrorName(m.Name, u.Host)
	return out, true
}

// pickMirrorName 决定显示名：用户填了就用用户的，否则用 host（不带端口太不直观）。
func pickMirrorName(filled, host string) string {
	if n := strings.TrimSpace(filled); n != "" {
		return n
	}
	return host
}

// NormalizeUpdateMirrors 归一整个清单：丢掉非法项、按 URL 去重（保序）。
//
// 去重是必要的：用户可能既填了 `https://gh-proxy.com` 又填了
// `https://gh-proxy.com/`，归一后是同一个地址，不去重就会对同一台镜像
// 白等两轮超时。
func NormalizeUpdateMirrors(list []UpdateMirror) []UpdateMirror {
	out := make([]UpdateMirror, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, m := range list {
		n, ok := NormalizeUpdateMirror(m)
		if !ok {
			continue
		}
		key := strings.ToLower(n.URL)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out
}

// IsSourceMirror 判断一条镜像是不是「更新源」型（自带 {repo} 占位）。
func IsSourceMirror(m UpdateMirror) bool {
	return strings.Contains(m.URL, "{repo}")
}

// MirrorURL 把镜像和「官方地址」拼成真正要请求的地址。
//
// 前缀型返回 `镜像 + 官方地址`（镜像末尾的 `/` 已归一去掉，这里补回）；
// 源型返回把 `{repo}` 换成 owner/repo 后的完整地址 —— 注意源型**忽略**官方地址，
// 因为它的整个含义就是「我不走 GitHub」。
func MirrorURL(m UpdateMirror, officialURL, repo string) string {
	if IsSourceMirror(m) {
		return strings.ReplaceAll(m.URL, "{repo}", repo)
	}
	return m.URL + "/" + officialURL
}
