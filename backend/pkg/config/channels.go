package config

import "strings"

// 自动下载的三个取音渠道。这些串**同时是配置值与接口值**（界面按它们存），
// 改名等于改配置格式，别随手改。
const (
	DLChannelLx      = "lx"     // 洛雪音源脚本（sidecar/lx_host）
	DLChannelMusicDL = "dl"     // musicdl 外挂池（咪咕 / 千千 / B 站 …）
	DLChannelNative  = "native" // 曲目自己平台的原生解析（网易云 / QQ / 酷狗 …）
)

// DefaultDownloadChannels 是配置为空时的顺序。
//
// 为什么洛雪排第一：另外两条路各有改不了的硬限制 —— 免登录的网易云 / QQ 对
// 收费曲目会**静默降档**（实测要 lossless 回 standard/128k），musicdl 侧只启用
// mg/bq/bi 三家、它给什么档就只能是那档。而洛雪脚本背后是第三方聚合 API，
// 是唯一常常真能给出无损的那条。把它排最前，用户在 m02588 报的
// 「我明明配了 lx，为什么还是走网易云 mp3」才会真的反过来。
//
// ⚠️ 洛雪**只在真有脚本可用时**才进候选，见 EffectiveDownloadChannels：装了
// 三个脚本但一个都没勾选、或者脚本宿主没开，这时把 lx 排在第一位只是白等一轮。
var DefaultDownloadChannels = []string{DLChannelLx, DLChannelMusicDL, DLChannelNative}

// NormalizeDownloadChannels 清洗用户传来的渠道列表：去空白、丢掉认不出的值、
// 去重，**保持顺序**（顺序就是优先级）。
//
// 认不出的值直接丢而不是报错：配置是从盘上读回来的，一次升级改了取值表
// 不该让「保存」这个动作失败。全空（包括「传了但全是错值」）回落到默认三档
// 全开 —— 自动下载是收藏之后的附加动作，宁可多试几条路，也不要因为一个空
// 列表让用户点了红心却什么都没下。
//
// ⚠️ 这里的「全开」是**配置层的**兜底：用户确实把三条都取消了也不该让下载彻底
// 不能走（那种状态在界面上是被拦住的）。真正的「这条渠道现在能不能用」由
// EffectiveDownloadChannels 判 —— 那里才看脚本有没有、宿主开没开、外挂源有没有。
func NormalizeDownloadChannels(in []string) []string {
	seen := make(map[string]bool, 3)
	out := make([]string, 0, 3)
	for _, raw := range in {
		c := strings.ToLower(strings.TrimSpace(raw))
		switch c {
		case DLChannelLx, DLChannelMusicDL, DLChannelNative:
		default:
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultDownloadChannels...)
	}
	return out
}

// DownloadChannels 返回生效的取音渠道顺序（nil / 空 = 默认）。
func (a AppConfig) DownloadChannels() []string {
	return NormalizeDownloadChannels(a.AutoDownloadChannels)
}

// ChannelEnabled 报告某条渠道在不在允许集里。
func ChannelEnabled(channels []string, want string) bool {
	for _, c := range channels {
		if c == want {
			return true
		}
	}
	return false
}

// ChannelRank 返回某条渠道在顺序里的位次（不在列表里 = -1）。
// 实际档位并列时用它打破平局：排前的渠道赢。
func ChannelRank(channels []string, want string) int {
	for n, c := range channels {
		if c == want {
			return n
		}
	}
	return -1
}

// ChannelAvailability 说明运行时「某条渠道此刻能不能用」。nil 字段 = 用默认判据
// （能被叫到就算可用）；分渠道给判据是因为三条渠道的「可用」根本不是一回事。
type ChannelAvailability struct {
	// LxUsable：洛雪宿主就绪**且**至少勾选了一个脚本。装了脚本但一个没勾 =
	// 宿主在跑也解析不出任何东西，把它算进候选只是白等一轮。
	LxUsable bool
	// MusicDLSources：外挂池实际启用的平台短码。空 = 这条渠道此刻没有源。
	MusicDLSources []string
	// NativeOK：平台直链解析器初始化了没有。
	NativeOK bool
}

// EffectiveDownloadChannels 返回**此刻真的值得去问**的渠道顺序。
//
// 与 DownloadChannels 的区别：后者是用户的配置口径（保留每条渠道的占位，
// 界面要显示「这条被关了」），这里是执行口径 —— 不可用的渠道直接摘掉。
//
// 为什么需要这一层（2026-11-01 用户：歌一直下不动，看不到「为什么」）：
//
//	a) 洛雪在最前，但它常常是空的（脚本没勾 / 宿主没开）。排在第一位就意味着
//	   每条任务都先去等一轮必然失败的解析。
//	b) 用户只勾了 native 时，native 对 QQ 匿名「没有这一档」就整首歌失败 ——
//	   而洛雪 / 外挂池明明能拿到同一首。用户看到的是「下不动」，实际是配置把
//	   另外两条路关掉了。
//
// 规则：用户关掉的渠道**不复活**（那是他的选择，复活等于偷流量）；只有当用户
// 一条可用的都没留时，才回落到「配置层允许的那些 + 此刻可用的」——
// 也就是「你关掉的那条本来就不可用」这种纯粹的空配置，不至于让下载彻底停摆。
func EffectiveDownloadChannels(cfg []string, av ChannelAvailability) []string {
	allowed := NormalizeDownloadChannels(cfg)
	usable := func(ch string) bool {
		switch ch {
		case DLChannelLx:
			return av.LxUsable
		case DLChannelMusicDL:
			return len(av.MusicDLSources) > 0
		case DLChannelNative:
			return av.NativeOK
		}
		return false
	}
	out := make([]string, 0, len(allowed))
	for _, ch := range allowed {
		if usable(ch) {
			out = append(out, ch)
		}
	}
	if len(out) > 0 {
		return out
	}
	// 用户留下的渠道全都不在自己这边可用：别复活他关掉的，保持原顺序返回，
	// 让下游如实失败并给出「这条为什么不可用」—— 静默改道比失败更难排查。
	return allowed
}
