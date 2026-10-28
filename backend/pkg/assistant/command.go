package assistant

// 指令条（注入面板）：一句话 → 结果卡片。
//
// 注入进飞牛音乐官方页面的那个面板（见 pkg/intercept/assets/ui.js）里没有
// 聊天窗口的位置，也放不下流式对话 —— 所以给它的是一句自然的指令 + 卡片式
// 结果：用户打「关掉每日 AI 歌单」就渲染出一张带开关状态的卡片；打「状态」
// 就渲染诊断卡片；打「心动歌单」就给歌单选项。全部本地规则匹配，不依赖大
// 模型 —— 只有兜底才可选地走一次 AI，AI 没配置时退化为帮助卡片。
//
// 匹配原则：开关别名优先，其次固定意图；全都不中才交给 AI。

import (
	"fmt"
	"sort"
	"strings"

	"fn-lx-player/pkg/config"
)

// Widget 是指令条的结果卡片。面板按 Kind 决定怎么渲染：
//
//	switches  → 开关列表（每行可点，直接调 /api/assistant/command 切）
//	status    → 关键指标（曲库 / 下载队列 / 来源数 / 版本）
//	playlist  → 歌单选项（按钮）
//	download  → 「下载当前歌曲」操作卡片
//	help / text → 纯文本说明
type Widget struct {
	Kind     string      `json:"kind"`
	Title    string      `json:"title,omitempty"`
	Message  string      `json:"message,omitempty"`
	Switches []SwitchRow `json:"switches,omitempty"`
	Actions  []CmdAction `json:"actions,omitempty"`
	Status   []KV        `json:"status,omitempty"`
}

// SwitchRow 是一行开关。On 为 nil 表示「配置里没写过」（三态），面板按
// DefaultOn 显示为默认值，并在旁边标一个「默认」。
type SwitchRow struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Group     string `json:"group,omitempty"`
	On        *bool  `json:"on"`
	DefaultOn bool   `json:"default_on"`
}

// CmdAction 是卡片上的一个按钮。ID 由前端解释（panel 自己实现动作）。
//
// 名字带 Cmd 前缀：assistant 包里已经有一个 Action（运行时的待确认操作），
// 两者语义完全不同 —— 那个是「助手要写东西，等用户点头」，这个是「卡片上
// 摆一个按钮」。同名会让两个概念在同一个包里互相污染。
type CmdAction struct {
	Label string `json:"label"`
	ID    string `json:"id"`
}

// KV 是一条键值指标。
type KV struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CommandIntent 是本地规则匹配的结果。
type CommandIntent struct {
	// Kind: switches / toggle / status / playlist / download / help / ai
	Kind string
	// Switches 是 toggle 意图命中的开关 key（可能多个，比如「关掉歌词和封面」）。
	Switches []string
	// On 是 toggle 的目标状态。
	On bool
	// Text 是原始输入，AI 兜底时带过去。
	Text string
}

// SwitchDef 描述一个对用户暴露的功能开关。
type SwitchDef struct {
	Key       string // 与 config.AppConfig 的 JSON 名一致
	Label     string
	Group     string
	DefaultOn bool
}

// SwitchDefs 是面板能展示/切换的开关全集。
//
// ⚠️ 表本身**不在这里** —— 它住在 pkg/config/feature_switch.go，因为「没设置过
// 时算开还是算关」的三态口径必须只有一个地方决定（config 里那几个
// `XxxOn()` 访问器才是后端真正执行的判断）。这里只是把那张表转成面板要用的
// 形状；曾经这里另抄了一份，两边默认值对不上（封面内嵌 / LX 服务），表现是
// 面板显示的状态和实际生效的状态不一致。
var SwitchDefs = switchDefsFromConfig()

func switchDefsFromConfig() []SwitchDef {
	out := make([]SwitchDef, 0, len(config.FeatureSwitches))
	for _, s := range config.FeatureSwitches {
		out = append(out, SwitchDef{Key: s.Key, Label: s.Label, Group: s.Group, DefaultOn: s.DefaultOn})
	}
	return out
}

// FindSwitchDef 按 key 找开关定义。
func FindSwitchDef(key string) (SwitchDef, bool) {
	for _, d := range SwitchDefs {
		if d.Key == key {
			return d, true
		}
	}
	return SwitchDef{}, false
}

// switchAliases 是每个开关的中文别名。匹配时**长别名优先**（先排 "收藏自动下载"
// 再排 "收藏下载"），避免"自动下载"这类短词把别的开关一起吞掉。
var switchAliases = map[string][]string{
	"fav_auto_download":         {"收藏自动下载", "收藏时自动下载", "收藏时下载", "收藏下载", "自动下载收藏"},
	"lyric_auto_download":       {"歌词自动下载", "自动下载歌词", "下载歌词"},
	"cover_embed":               {"封面内嵌", "内嵌封面", "嵌入封面", "下载封面"},
	"tee_enabled":               {"边听边下", "边听边缓存", "边听边存"},
	"musicdl_enabled":           {"musicdl", "下载引擎", "外部下载引擎"},
	"ui_inject_enabled":         {"注入面板", "飞牛音乐面板", "面板注入", "音乐页注入", "挂件"},
	"daily_llm_enabled":         {"每日ai歌单", "每日ai", "ai歌单", "每日推荐", "大模型推荐"},
	"chart_playlists_enabled":   {"榜单歌单", "榜单注入", "榜单"},
	"lx_server_enabled":         {"lx音源", "lx 服务", "lx", "音源服务"},
	"snapshot_schedule_enabled": {"定时快照", "快照"},
	"upgrade_sweep_enabled":     {"补强轮", "老版本补强", "版本补强", "升级补强"},
}

// offWords 表示「关」的动词。顺序无关，但要注意「关闭/关掉/禁用」都包含「关」。
var offWords = []string{"关闭", "关掉", "禁用", "停用", "不要", "去掉", "别开", "关"}

// onWords 表示「开」的动词。
var onWords = []string{"打开", "开启", "启用", "恢复", "开"}

// MatchCommand 把一句话匹配成意图。纯本地规则，不依赖网络/大模型。
//
// 带 key/on 的直连请求（面板点开关时）不走这里 —— 那是 handleAssistantCommand
// 里的显式分支，见 API 层。
func MatchCommand(text string) CommandIntent {
	raw := strings.TrimSpace(text)
	t := strings.ToLower(raw)
	if t == "" {
		return CommandIntent{Kind: "help", Text: raw}
	}

	// 1) 开关别名命中（长别名优先，见 switchAliases 注释）
	hits := matchSwitchAliases(t)
	if len(hits) > 0 {
		// 有动词 → 直接切；没动词 → 展示这些开关（由用户点）
		on := false
		hasOff := containsAny(t, offWords)
		hasOn := containsAny(t, onWords)
		switch {
		case hasOff:
			on = false
		case hasOn:
			on = true
		default:
			return CommandIntent{Kind: "switches", Switches: hits, Text: raw}
		}
		return CommandIntent{Kind: "toggle", Switches: hits, On: on, Text: raw}
	}

	// 2) 显式要开关列表
	if containsAny(t, []string{"开关", "设置", "功能", "选项", "有哪些"}) {
		return CommandIntent{Kind: "switches", Text: raw}
	}

	// 3) 状态 / 诊断
	if containsAny(t, []string{"状态", "健康", "统计", "诊断", "检查", "情况", "怎么样"}) {
		return CommandIntent{Kind: "status", Text: raw}
	}

	// 4) 歌单
	if containsAny(t, []string{"歌单", "心动"}) {
		return CommandIntent{Kind: "playlist", Text: raw}
	}

	// 5) 下载当前
	if containsAny(t, []string{"下载当前", "下载这首", "下载正在播放", "这首歌", "当前歌曲"}) {
		return CommandIntent{Kind: "download", Text: raw}
	}

	// 6) 帮助
	if containsAny(t, []string{"帮助", "能干嘛", "能干什么", "怎么用", "指令", "支持什么", "help", "？", "?"}) {
		return CommandIntent{Kind: "help", Text: raw}
	}

	return CommandIntent{Kind: "ai", Text: raw}
}

// matchSwitchAliases 返回命中的开关 key。同一文本命中多个别名时，
// 较长的别名优先（"收藏自动下载" 命中 fav，不命中 "自动下载"）。
func matchSwitchAliases(t string) []string {
	type cand struct {
		key string
		pos int
	}
	var cands []cand
	for key, aliases := range switchAliases {
		for _, a := range aliases {
			if i := strings.Index(t, a); i >= 0 {
				cands = append(cands, cand{key: key, pos: i})
				break // 同一开关只算一次
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].pos < cands[j].pos })
	// 去重（保持出现顺序）
	seen := make(map[string]bool)
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if !seen[c.key] {
			seen[c.key] = true
			out = append(out, c.key)
		}
	}
	return out
}

// containsAny 判断 t 是否包含 words 中任意一个。
func containsAny(t string, words []string) bool {
	for _, w := range words {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// ToggleDescription 生成「已关闭 收藏自动下载」这样的标题，供卡片展示。
func ToggleDescription(keys []string, on bool) string {
	verb := "已关闭"
	if on {
		verb = "已开启"
	}
	labels := make([]string, 0, len(keys))
	for _, k := range keys {
		if d, ok := FindSwitchDef(k); ok {
			labels = append(labels, d.Label)
		} else {
			labels = append(labels, k)
		}
	}
	return fmt.Sprintf("%s %s", verb, strings.Join(labels, "、"))
}
