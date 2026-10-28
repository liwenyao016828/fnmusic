package config

// 功能开关的按 key 读写。
//
// 为什么单独一个文件：这些开关散落在 AppConfig 的十几个 `*bool` 字段里
// （各带三态语义：nil = 没设置过 = 走默认），而**界面**（飞牛音乐页面里的
// 指令条、曲率设置页）需要「给我 key，我告诉你现在开没开、把它切过去」。
//
// 硬编码映射表放在这里，是因为三态口径**必须只有一个地方**决定：
// 每个开关「没设置过时算开还是算关」各不相同（歌词下载与封面内嵌默认开，
// 收藏自动下载默认关），把判断散到调用方，迟早出现「界面显示开着、后端
// 当关着」这种偏差 —— 表现是「开关点了没反应」。
//
// 语义约定：
//   - 读：nil 时按 DefaultOn 口径回答（返回的 bool 就是**实际生效**的状态）；
//   - 写：显式写 true/false（不写 nil），所以「切到关」是可逆的，不会
//     因为「没设置过」被当成「关」而永远切不回来。

import "fmt"

// FeatureSwitch 描述一个可被界面切换的功能开关。
type FeatureSwitch struct {
	// Key 是 AppConfig 里的 JSON 字段名。
	Key string
	// Label 是给人看的名字。
	Label string
	// Group 是分组名（界面按组分区）。
	Group string
	// DefaultOn 是「没设置过」时的实际生效状态。
	DefaultOn bool
}

// FeatureSwitches 是全部可切换开关。顺序即界面顺序。
var FeatureSwitches = []FeatureSwitch{
	{Key: "fav_auto_download", Label: "收藏自动下载", Group: "下载行为", DefaultOn: false},
	{Key: "lyric_auto_download", Label: "歌词自动下载", Group: "下载行为", DefaultOn: true},
	{Key: "cover_embed", Label: "封面内嵌", Group: "下载行为", DefaultOn: true},
	{Key: "tee_enabled", Label: "边听边下", Group: "下载行为", DefaultOn: false},
	{Key: "musicdl_enabled", Label: "musicdl 下载引擎", Group: "下载行为", DefaultOn: false},
	{Key: "ui_inject_enabled", Label: "飞牛音乐注入面板", Group: "功能", DefaultOn: true},
	{Key: "daily_llm_enabled", Label: "每日 AI 歌单", Group: "功能", DefaultOn: false},
	{Key: "chart_playlists_enabled", Label: "榜单歌单注入", Group: "功能", DefaultOn: true},
	{Key: "lx_server_enabled", Label: "LX 音源服务", Group: "功能", DefaultOn: false},
	{Key: "snapshot_schedule_enabled", Label: "定时快照", Group: "功能", DefaultOn: false},
	{Key: "upgrade_sweep_enabled", Label: "老版本补强轮", Group: "功能", DefaultOn: false},
}

// FindFeatureSwitch 按 key 找开关定义。
func FindFeatureSwitch(key string) (FeatureSwitch, bool) {
	for _, s := range FeatureSwitches {
		if s.Key == key {
			return s, true
		}
	}
	return FeatureSwitch{}, false
}

// FeatureSwitchOn 报告某个开关**当前实际生效**的状态（已考虑三态默认）。
//
// 认不出的 key 一律按「关」回答 —— 界面不会拿它去做什么危险动作，
// 但也绝不会把一个不存在的开关说成开着。
func (a *AppConfig) FeatureSwitchOn(key string) bool {
	switch key {
	case "fav_auto_download":
		return a.FavAutoDownloadOn()
	case "lyric_auto_download":
		return a.LyricAutoDownloadOn()
	case "cover_embed":
		return a.CoverEmbedOn()
	case "tee_enabled":
		return a.TeeOn()
	case "musicdl_enabled":
		return a.MusicDLOn()
	case "ui_inject_enabled":
		return a.InjectUIEnabled()
	case "daily_llm_enabled":
		return a.DailyLLMOn()
	case "chart_playlists_enabled":
		return a.ChartsInjectEnabled()
	case "lx_server_enabled":
		return a.LXServerOn()
	case "snapshot_schedule_enabled":
		return a.SnapshotsScheduled()
	case "upgrade_sweep_enabled":
		return a.UpgradeSweepOn()
	default:
		return false
	}
}

// FeatureSwitchState 读一个开关的三态原始值：ok=false 表示配置里没写过
// （nil），on 是它的值。界面据此显示「默认（开）」这类提示。
func (a *AppConfig) FeatureSwitchState(key string) (on bool, ok bool) {
	switch key {
	case "fav_auto_download":
		return derefBool(a.FavAutoDownload)
	case "lyric_auto_download":
		return derefBool(a.LyricAutoDownload)
	case "cover_embed":
		return derefBool(a.CoverEmbed)
	case "tee_enabled":
		return derefBool(a.TeeEnabled)
	case "musicdl_enabled":
		return derefBool(a.MusicDLEnabled)
	case "ui_inject_enabled":
		return derefBool(a.UIInjectEnabled)
	case "daily_llm_enabled":
		return derefBool(a.DailyLLMEnabled)
	case "chart_playlists_enabled":
		return derefBool(a.ChartPlaylistsEnabled)
	case "lx_server_enabled":
		return derefBool(a.LXServerEnabled)
	case "snapshot_schedule_enabled":
		return derefBool(a.SnapshotScheduleEnabled)
	case "upgrade_sweep_enabled":
		return derefBool(a.UpgradeSweepEnabled)
	default:
		return false, false
	}
}

func derefBool(p *bool) (bool, bool) {
	if p == nil {
		return false, false
	}
	return *p, true
}

// SetFeatureSwitch 把某个开关切到指定状态并落盘。
//
// 认不出的 key 直接报错 —— 界面传错了 key 时必须**失败**而不是静默成功，
// 否则用户会以为开关切了其实没切。
func (cm *ConfigManager) SetFeatureSwitch(key string, on bool) error {
	v := on
	patch := AppConfig{}
	switch key {
	case "fav_auto_download":
		patch.FavAutoDownload = &v
	case "lyric_auto_download":
		patch.LyricAutoDownload = &v
	case "cover_embed":
		patch.CoverEmbed = &v
	case "tee_enabled":
		patch.TeeEnabled = &v
	case "musicdl_enabled":
		patch.MusicDLEnabled = &v
	case "ui_inject_enabled":
		patch.UIInjectEnabled = &v
	case "daily_llm_enabled":
		patch.DailyLLMEnabled = &v
	case "chart_playlists_enabled":
		patch.ChartPlaylistsEnabled = &v
	case "lx_server_enabled":
		patch.LXServerEnabled = &v
	case "snapshot_schedule_enabled":
		patch.SnapshotScheduleEnabled = &v
	case "upgrade_sweep_enabled":
		patch.UpgradeSweepEnabled = &v
	default:
		return fmt.Errorf("未知的功能开关：%s", key)
	}
	return cm.Update(patch)
}
