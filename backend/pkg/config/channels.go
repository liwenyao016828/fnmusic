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
var DefaultDownloadChannels = []string{DLChannelLx, DLChannelMusicDL, DLChannelNative}

// NormalizeDownloadChannels 清洗用户传来的渠道列表：去空白、丢掉认不出的值、
// 去重，**保持顺序**（顺序就是优先级）。
//
// 认不出的值直接丢而不是报错：配置是从盘上读回来的，一次升级改了取值表
// 不该让「保存」这个动作失败。全空（包括「传了但全是错值」）回落到默认三档
// 全开 —— 自动下载是收藏之后的附加动作，宁可多试几条路，也不要因为一个空
// 列表让用户点了红心却什么都没下。
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
