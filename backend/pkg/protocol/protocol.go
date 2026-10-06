// Package protocol 定义「音源协议」这一概念。
//
// # 为什么要显式建模「协议」
//
// 本项目原先只有一种协议：用户在浏览器里导入的 **LX 自定义源脚本**。
// 但后端本身早已内置了官方平台适配（搜索 / 榜单 / 歌单 / 歌词，见 pkg/search 与 pkg/charts），
// 这些能力**不依赖任何脚本**就能工作。
//
// 把两者都显式建模为「协议」之后，能力边界就清楚了：
//
//	official —— 后端内置，免脚本，负责 搜索 / 榜单 / 歌单 / 歌词
//	lx       —— 浏览器沙箱执行用户脚本，负责 取直链（音乐地址）
//
// 二者是**互补**关系，不是替代关系：没有 lx 音源时，官方协议依然能搜索、能逛榜单、
// 能看歌词；只有「点击播放」这一步需要 lx 协议把音乐地址解析出来。
//
// # 不可动摇的合规约束
//
// 后端**不内置、不执行**任何第三方 JS。因此 lx 协议的 Location 恒为 "browser"：
// 后端只做注册与描述，绝不参与脚本执行。想改这一点之前请先读 HANDOVER.md 的规则一。
package protocol

// Capability 协议能提供的能力。
type Capability string

const (
	CapSearch   Capability = "search"    // 关键词搜索
	CapCharts   Capability = "charts"    // 榜单
	CapPlaylist Capability = "playlist"  // 歌单（列表与详情）
	CapLyric    Capability = "lyric"     // 歌词
	CapMusicURL Capability = "music_url" // 取直链（播放地址）
)

// Location 协议的执行位置。
const (
	LocBackend = "backend" // 后端内置实现
	LocBrowser = "browser" // 浏览器沙箱执行用户脚本
)

// 协议 ID。
const (
	IDOfficial = "official"
	IDLx       = "lx"
)

// OfficialPlatforms 官方协议实际覆盖的平台。
// 与 pkg/search 的 switch 分支、pkg/charts 的 switch 分支保持一致，改一处要同步改三处。
var OfficialPlatforms = []string{"wy", "tx", "kg", "kw"}

// Protocol 一种音源协议的描述。
//
// 前 8 个字段是**静态描述**（由本包定义，与运行环境无关）；
// Ready / SourceCount 是**运行时状态**，由 API 层在返回前填入，
// 本包的 List() 不会设置它们（保持零值）。
type Protocol struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Description    string       `json:"description"`
	Location       string       `json:"location"`        // backend | browser
	Builtin        bool         `json:"builtin"`         // 是否内置（无需用户导入）
	RequiresSource bool         `json:"requires_source"` // 是否需要用户先导入音源
	Platforms      []string     `json:"platforms"`       // 支持的平台代号
	Capabilities   []Capability `json:"capabilities"`    // 提供的能力
	Notes          string       `json:"notes,omitempty"` // 给 AI / 接手者的补充说明

	// ── 以下为运行时状态，由 pkg/api 填充 ──
	Ready       bool `json:"ready"`                  // 当前是否可用
	SourceCount int  `json:"source_count,omitempty"` // 该协议下已导入的音源数（仅 lx 有意义）
}

// List 返回全部协议。顺序即推荐阅读顺序：先内置官方，后用户脚本。
func List() []Protocol {
	return []Protocol{
		{
			ID:             IDOfficial,
			Name:           "官方协议",
			Description:    "免脚本即可用：搜索、榜单、歌单、歌词都由它提供。",
			Location:       LocBackend,
			Builtin:        true,
			RequiresSource: false,
			Platforms:      append([]string(nil), OfficialPlatforms...),
			Capabilities: []Capability{
				CapSearch, CapCharts, CapPlaylist, CapLyric,
			},
			Notes: "不需要导入任何音源。但它只负责搜索与浏览；播放/下载要先把歌曲解析成直链，那一步得靠 lx 协议。",
		},
		{
			ID:             IDLx,
			Name:           "LX 自定义源协议",
			Description:    "你导入的第三方脚本，负责把歌曲解析成可播放的直链 —— 不导入就无法播放与下载。",
			Location:       LocBrowser,
			Builtin:        false,
			RequiresSource: true,
			Platforms:      []string{"wy", "tx", "kg", "kw", "mg"},
			Capabilities: []Capability{
				CapMusicURL, CapSearch, CapLyric,
			},
			Notes: "合规约束：后端不内置、不执行第三方 JS，脚本只在浏览器里跑。搜索与歌词优先走官方协议，脚本主要承担取直链。",
		},
	}
}

// Get 按 ID 取协议描述。
func Get(id string) (Protocol, bool) {
	for _, p := range List() {
		if p.ID == id {
			return p, true
		}
	}
	return Protocol{}, false
}

// CapabilitiesOf 返回指定协议的能力集合。
func CapabilitiesOf(id string) []Capability {
	p, ok := Get(id)
	if !ok {
		return nil
	}
	return p.Capabilities
}

// HasCapability 判断某个协议是否提供某项能力。
func HasCapability(id string, cap Capability) bool {
	for _, c := range CapabilitiesOf(id) {
		if c == cap {
			return true
		}
	}
	return false
}
