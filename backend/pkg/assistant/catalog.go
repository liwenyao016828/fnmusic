package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/config"
)

// Capability 一个助手能力：映射到后端一条真实路由。
//
// 字段说明：
//   - Name：工具名（模型调用用，英文蛇形）
//   - Method/Path：对应 routeTable 里的路由（executor 按此进程内直调）
//   - Summary：给模型与界面看的中文说明
//   - Exposure/Risk：策略裁决依据
//   - Params：参数说明（写进工具描述，模型据此构造 arguments）
//   - Build：把模型给的 args 构造成一次 HTTP 请求（方法+路径+query+body）
type Capability struct {
	Name     string
	Method   string
	Path     string
	Summary  string
	DetailFn func(args map[string]any) string
	Exposure Exposure
	Risk     Risk
	Params   string
	Build    func(args map[string]any) (*http.Request, error)
}

// actionDetail 返回确认卡上要显示的「人话」描述。
// 能力自己实现了 DetailFn 就用它（例如把参数拼成「下载《晴天》- 周杰伦」）；
// 没实现时退回能力简介，至少让用户知道是哪类操作。
func (c *Capability) Detail(args map[string]any) string {
	if c == nil {
		return ""
	}
	if c.DetailFn != nil {
		if s := strings.TrimSpace(c.DetailFn(args)); s != "" {
			return s
		}
	}
	return c.Summary
}

// buildRequest 构造一次能力请求。host 用占位值——executor 进程内直调，
// 不经过网络，host 没有意义，但 http.NewRequest 要求 URL 合法。
func buildRequest(method, path string, query url.Values, body []byte) (*http.Request, error) {
	u, err := url.Parse("http://assistant.local" + path)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	var rdr *bytes.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// argsToJSON 把模型给的参数整体序列化（用于无参或全量透传的能力）。
func argsToJSON(args map[string]any) ([]byte, error) {
	if args == nil {
		return nil, nil
	}
	return json.Marshal(args)
}

// pathList 从 args["paths"] 里取出路径数组（兼容 []any / []string）。
func pathList(args map[string]any) []string {
	switch v := args["paths"].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// shorten 截断长文本用于确认卡（按 rune 截，不劈开汉字）。
func shorten(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

var featureSwitchDescriptions = map[string]string{
	"chart_playlists_enabled":   "榜单注入：把榜单挂成飞牛音乐里的在线歌单",
	"ui_inject_enabled":         "页面注入：飞牛自带音乐页里的「下载到曲率」菜单",
	"musicdl_enabled":           "音乐下载：飞牛页面收藏/下载按钮的接管",
	"daily_llm_enabled":         "每日推荐 AI 层：用大模型扩充每日推荐候选",
	"snapshot_schedule_enabled": "曲库定时快照：按间隔自动备份曲库索引",
	"lx_server_enabled":         "LX 服务：外部 LX 客户端接入",
	"tee_enabled":               "Tee 模式：下载分流到双目录",
	"upgrade_sweep_enabled":     "升级清理：更新后清理旧版本文件",
	"fav_auto_download":         "收藏自动下载：在飞牛音乐里收藏一首歌就自动下载到曲库",
	"lyric_auto_download":       "歌词自动下载：下载时顺带把歌词写进文件",
	"cover_embed":               "封面内嵌：下载时把封面写进音频标签",
}

// FeatureSwitches 助手可开关的应用功能开关。
//
// 只暴露「可逆、影响面明确」的布尔开关——不可逆操作（删除曲库、清空历史）
// 不进目录，永远需要用户在界面亲手做。
//
// ⚠️ key 集合**不是**这里写死的：它来自 config.FeatureSwitches（唯一那张
// 开关表，见 pkg/config/feature_switch.go）。以前这里另抄了一份，两边会漂 ——
// 面板里能看到的开关、助手能切的开关、实际生效的默认值必须是同一张表，
// 否则就会出现「面板显示开着、后端当关着」这种只能靠人发现的偏差。
var FeatureSwitches = featureSwitchesFromConfig()

func featureSwitchesFromConfig() map[string]string {
	out := make(map[string]string, len(config.FeatureSwitches))
	for _, s := range config.FeatureSwitches {
		desc := featureSwitchDescriptions[s.Key]
		if desc == "" {
			desc = s.Label
		}
		out[s.Key] = desc
	}
	return out
}

// Catalog 助手能力目录。
//
// 顺序即界面展示顺序（只读在前，写入在后）。
func Catalog() []*Capability {
	caps := []*Capability{
		// ── 只读（Auto）──
		{
			Name:     "catalog_list",
			Method:   http.MethodGet,
			Path:     "/api/catalog",
			Summary:  "列出本应用的全部 API 接口清单（结构化）。用户问「有哪些功能/接口」「能不能做某事」时先调它确认能力面。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params: "filter（可选）：关键词，如 library / download / charts。给了就只列出路径或说明含该词的接口；" +
				"不给只列前 80 条。要从长清单里找某一类能力时务必带上，否则后面的接口会被省略。",
			Build: func(args map[string]any) (*http.Request, error) {
				q := url.Values{}
				if f, ok := args["filter"].(string); ok {
					if f = strings.TrimSpace(f); f != "" {
						q.Set("filter", f)
					}
				}
				return buildRequest(http.MethodGet, "/api/catalog", q, nil)
			},
		},
		{
			Name:     "health_status",
			Method:   http.MethodGet,
			Path:     "/api/health",
			Summary:  "查看应用运行状态：进程是否存活、版本号、运行时间、依赖服务状态。用户问「应用正常吗」「服务在跑吗」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/health", nil, nil)
			},
		},
		{
			Name:     "sources_list",
			Method:   http.MethodGet,
			Path:     "/api/sources",
			Summary:  "查看音源列表（各平台音源是否可用、类型、状态）。用户问「音源」「搜索源」「某平台能不能用」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/sources", nil, nil)
			},
		},
		{
			Name:     "download_queue",
			Method:   http.MethodGet,
			Path:     "/api/download/queue",
			Summary:  "查看下载队列：进行中、等待、已完成/失败的下载任务。用户问「下载到哪了」「队列里有什么」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/download/queue", nil, nil)
			},
		},
		{
			Name:     "logs_recent",
			Method:   http.MethodGet,
			Path:     "/api/logs",
			Summary:  "查看最近的应用日志（运行事件、报错）。用户问「报错」「异常」「日志」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "可选参数 limit：条数（1-2000，默认 200）。",
			Build: func(args map[string]any) (*http.Request, error) {
				q := url.Values{}
				if v := strArg(args, "limit"); v != "" {
					if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
						q.Set("limit", strconv.Itoa(n))
					}
				}
				return buildRequest(http.MethodGet, "/api/logs", q, nil)
			},
		},
		{
			Name:     "library_stats",
			Method:   http.MethodGet,
			Path:     "/api/library/index/stats",
			Summary:  "查看曲库索引统计：歌曲数、文件数、扫描时间等。用户问「曲库有多大」「扫描状态」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "可选参数 dir：曲库目录。**一般不用传** —— 省略时后端自动用配置里生效的曲库目录。",
			Build: func(args map[string]any) (*http.Request, error) {
				q := url.Values{}
				// dir 可选：传了就用调用方指定的目录，不传则由后端回退到配置里的曲库目录。
				// 以前这里恒定不带 dir，而接口又硬要求 dir → 该能力稳定 400，白给。
				if v := strArg(args, "dir"); v != "" {
					q.Set("dir", v)
				}
				return buildRequest(http.MethodGet, "/api/library/index/stats", q, nil)
			},
		},
		{
			Name:     "ai_usage",
			Method:   http.MethodGet,
			Path:     "/api/ai/usage",
			Summary:  "查看 AI 用量统计（调用次数、token 消耗）。用户问「AI 用了多少」「消耗」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "可选参数 recent：最近 N 条明细。",
			Build: func(args map[string]any) (*http.Request, error) {
				q := url.Values{}
				if v := strArg(args, "recent"); v != "" {
					q.Set("recent", v)
				}
				return buildRequest(http.MethodGet, "/api/ai/usage", q, nil)
			},
		},
		{
			Name:     "monitors_list",
			Method:   http.MethodGet,
			Path:     "/api/monitors",
			Summary:  "查看监控任务列表（定时抓取/整理任务）。用户问「有哪些监控任务」「任务列表」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/monitors", nil, nil)
			},
		},
		{
			Name:     "push_tasks",
			Method:   http.MethodGet,
			Path:     "/api/push/tasks",
			Summary:  "查看推送任务（账号歌单 → 飞牛音乐歌单）。用户问「推送」「同步歌单」时调用。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/push/tasks", nil, nil)
			},
		},
		{
			Name:     "config_get",
			Method:   http.MethodGet,
			Path:     "/api/config",
			Summary:  "读取当前应用配置（用于查询功能开关的当前值等）。改任何开关前应先调它确认现状。",
			Exposure: ExposureAuto,
			Risk:     RiskRead,
			Params:   "无参数。返回的配置里密钥已脱敏。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodGet, "/api/config", nil, nil)
			},
		},

		// ── 写入（Confirm）──
		{
			Name:    "tidy_tracks",
			Method:  http.MethodPost,
			Path:    "/api/tidy",
			Summary: "整理曲库文件：重命名文件、写入标签/歌词/封面。会真实改动曲库文件（可逆，但影响文件本身），调用前必须向用户说明要整理哪些文件。",
			DetailFn: func(args map[string]any) string {
				n := len(pathList(args))
				if n == 0 {
					return "整理曲库文件（未提供文件列表）"
				}
				var opts []string
				if m, ok := args["options"].(map[string]any); ok {
					labels := map[string]string{
						"write_lyric": "写歌词", "write_metadata": "写标签",
						"write_cover": "写封面", "rename": "重命名",
					}
					for k, label := range labels {
						if v, ok := m[k].(bool); ok && v {
							opts = append(opts, label)
						}
					}
					sort.Strings(opts)
				}
				what := "整理"
				if len(opts) > 0 {
					what = strings.Join(opts, "、")
				}
				return fmt.Sprintf("%s %d 个曲库文件", what, n)
			},
			Exposure: ExposureConfirm,
			Risk:     RiskReversibleWrite,
			Params:   "参数 paths：要整理的文件绝对路径数组（最多 500 个）。可选 options：整理选项（write_lyric/write_metadata/write_cover/rename 等布尔）。",
			Build: func(args map[string]any) (*http.Request, error) {
				var paths []string
				switch v := args["paths"].(type) {
				case []any:
					for _, item := range v {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							paths = append(paths, strings.TrimSpace(s))
						}
					}
				case []string:
					paths = append(paths, v...)
				}
				if len(paths) == 0 {
					return nil, fmt.Errorf("缺少参数 paths（文件绝对路径数组）")
				}
				if len(paths) > 500 {
					return nil, fmt.Errorf("一次最多整理 500 个文件，当前 %d 个", len(paths))
				}
				body := map[string]any{"paths": paths}
				if opts, ok := args["options"].(map[string]any); ok && len(opts) > 0 {
					body["options"] = opts
				}
				b, err := json.Marshal(body)
				if err != nil {
					return nil, err
				}
				return buildRequest(http.MethodPost, "/api/tidy", nil, b)
			},
		},
		{
			Name:    "download_song",
			Method:  http.MethodPost,
			Path:    "/api/download/song",
			Summary: "下载一首歌曲到曲库。需要歌曲名（及歌手，可选 URL）。会占用磁盘空间并进入下载队列，调用前说明要下载什么。",
			DetailFn: func(args map[string]any) string {
				name := strArg(args, "name")
				singer := strArg(args, "singer")
				if name == "" {
					return "下载歌曲（未提供歌名）"
				}
				if singer != "" {
					return fmt.Sprintf("下载歌曲：《%s》- %s", name, singer)
				}
				return fmt.Sprintf("下载歌曲：《%s》", name)
			},
			Exposure: ExposureConfirm,
			Risk:     RiskReversibleWrite,
			Params:   "参数 name：歌曲名（必填）；singer：歌手（可选，建议提供）；url：音频直链（可选，提供时优先用它）；source：音源名（可选）。",
			Build: func(args map[string]any) (*http.Request, error) {
				name := strArg(args, "name")
				if name == "" {
					return nil, fmt.Errorf("缺少参数 name（歌曲名）")
				}
				body := map[string]any{"name": name}
				if v := strArg(args, "singer"); v != "" {
					body["singer"] = v
				}
				if v := strArg(args, "url"); v != "" {
					body["url"] = v
				}
				if v := strArg(args, "source"); v != "" {
					body["source"] = v
				}
				b, err := json.Marshal(body)
				if err != nil {
					return nil, err
				}
				return buildRequest(http.MethodPost, "/api/download/song", nil, b)
			},
		},
		{
			Name:    "toggle_feature_switch",
			Method:  http.MethodPost,
			Path:    "/api/config",
			Summary: "开关应用功能开关（布尔配置项）。调用前必须先 config_get 确认当前值，并只改用户要求的那一项。",
			DetailFn: func(args map[string]any) string {
				key := strArg(args, "key")
				label := key
				for _, s := range config.FeatureSwitches {
					if s.Key == key {
						label = s.Label
						break
					}
				}
				verb := "切换"
				switch v := args["value"].(type) {
				case bool:
					if v {
						verb = "开启"
					} else {
						verb = "关闭"
					}
				case string:
					if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
						if parsed {
							verb = "开启"
						} else {
							verb = "关闭"
						}
					}
				}
				return fmt.Sprintf("%s功能「%s」", verb, label)
			},
			Exposure: ExposureConfirm,
			Risk:     RiskReversibleWrite,
			Params:   "参数 key：配置键名（只能是 chart_playlists_enabled / ui_inject_enabled / musicdl_enabled / daily_llm_enabled / snapshot_schedule_enabled / lx_server_enabled / tee_enabled / upgrade_sweep_enabled 之一）；value：true 或 false。",
			Build: func(args map[string]any) (*http.Request, error) {
				key := strArg(args, "key")
				if key == "" {
					return nil, fmt.Errorf("缺少参数 key（配置键名）")
				}
				if _, ok := FeatureSwitches[key]; !ok {
					return nil, fmt.Errorf("不支持的配置键 %q（可选：%s）", key, strings.Join(sortedKeys(FeatureSwitches), " / "))
				}
				var value bool
				switch v := args["value"].(type) {
				case bool:
					value = v
				case string:
					parsed, err := strconv.ParseBool(strings.TrimSpace(v))
					if err != nil {
						return nil, fmt.Errorf("value 必须是布尔值 true/false，收到 %q", v)
					}
					value = parsed
				default:
					return nil, fmt.Errorf("缺少参数 value（true/false）")
				}
				b, err := json.Marshal(map[string]any{key: value})
				if err != nil {
					return nil, err
				}
				return buildRequest(http.MethodPost, "/api/config", nil, b)
			},
		},
		{
			Name:    "ai_complete",
			Method:  http.MethodPost,
			Path:    "/api/ai/complete",
			Summary: "用 AI 处理文本/文件名（歌名解析、元数据补全等）。kind=name_parse 时给 filename 解析出歌手艺名；其他情况给 prompt/content 自由生成。",
			DetailFn: func(args map[string]any) string {
				if f := strArg(args, "filename"); f != "" {
					return fmt.Sprintf("用 AI 解析文件名「%s」", f)
				}
				if p := strArg(args, "prompt"); p != "" {
					return "用 AI 处理请求：" + shorten(p, 40)
				}
				if c := strArg(args, "content"); c != "" {
					return "用 AI 处理文本：" + shorten(c, 40)
				}
				return "调用 AI 处理内容"
			},
			Exposure: ExposureConfirm,
			Risk:     RiskReversibleWrite,
			Params:   "参数 kind：name_parse（文件名解析，必填 filename）或留空自由生成（给 prompt 或 content）；filename：文件名；prompt/content：自定义内容。",
			Build: func(args map[string]any) (*http.Request, error) {
				body, err := argsToJSON(args)
				if err != nil {
					return nil, err
				}
				if strArg(args, "filename") == "" && strArg(args, "prompt") == "" && strArg(args, "content") == "" {
					return nil, fmt.Errorf("至少提供 filename / prompt / content 之一")
				}
				return buildRequest(http.MethodPost, "/api/ai/complete", nil, body)
			},
		},
		{
			Name:     "reset_usage",
			Method:   http.MethodPost,
			Path:     "/api/ai/usage/reset",
			Summary:  "清空 AI 用量统计（仅计数清零，不影响任何功能）。调用前向用户说明将清空用量统计。",
			Exposure: ExposureConfirm,
			Risk:     RiskReversibleWrite,
			Params:   "无参数。",
			Build: func(args map[string]any) (*http.Request, error) {
				return buildRequest(http.MethodPost, "/api/ai/usage/reset", nil, []byte("{}"))
			},
		},
	}

	// 防御：同名能力只保留第一个（目录是策略裁决的唯一依据，重复会让人误判覆盖面）
	seen := map[string]bool{}
	out := make([]*Capability, 0, len(caps))
	for _, c := range caps {
		if c == nil || c.Name == "" {
			continue
		}
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// FindCapability 按名称查能力
func FindCapability(name string) *Capability {
	for _, c := range Catalog() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ToolDefs 把能力目录转成 OpenAI 工具定义列表（给模型的工具清单）。
func ToolDefs(caps []*Capability) []ai.ToolDef {
	defs := make([]ai.ToolDef, 0, len(caps))
	for _, c := range caps {
		defs = append(defs, ai.ToolDef{
			Type: "function",
			Function: ai.ToolDefFunction{
				Name:        c.Name,
				Description: c.Summary,
				Parameters:  ToolParamsSchema(c),
			},
		})
	}
	return defs
}

// ToolParamsSchema 生成能力参数的 JSON Schema（宽松：只约束类型，不约束具体取值，
// 具体取值校验在 Build 里做——模型给错时返回可读错误比在 Schema 里写死更灵活）。
func ToolParamsSchema(c *Capability) json.RawMessage {
	props := map[string]map[string]string{}
	required := []string{}

	switch c.Name {
	case "logs_recent":
		props["limit"] = map[string]string{"type": "integer", "description": "条数（1-2000）"}
	case "catalog_list":
		// 接口目录有 180+ 条，不带关键词只能按位置截断；给 filter 才能精确取某一类。
		props["filter"] = map[string]string{"type": "string", "description": "关键词，如 library / download / charts（可选，推荐带上）"}
	case "library_stats":
		// dir 是可选的：不传即用配置里的曲库目录。模型硬造路径反而会被路径守卫 403，
		// 所以参数描述里明确「通常不用传」。
		props["dir"] = map[string]string{"type": "string", "description": "曲库目录（可选，通常省略）"}
	case "ai_usage":
		props["recent"] = map[string]string{"type": "integer", "description": "最近 N 条明细"}
	case "tidy_tracks":
		props["paths"] = map[string]string{"type": "array", "description": "文件绝对路径数组（最多 500）"}
		props["options"] = map[string]string{"type": "object", "description": "整理选项（write_lyric/write_metadata/write_cover/rename 等）"}
		required = append(required, "paths")
	case "download_song":
		props["name"] = map[string]string{"type": "string", "description": "歌曲名"}
		props["singer"] = map[string]string{"type": "string", "description": "歌手"}
		props["url"] = map[string]string{"type": "string", "description": "音频直链（可选）"}
		props["source"] = map[string]string{"type": "string", "description": "音源名（可选）"}
		required = append(required, "name")
	case "toggle_feature_switch":
		props["key"] = map[string]string{"type": "string", "description": "配置键名（见能力说明里的可选值）"}
		props["value"] = map[string]string{"type": "boolean", "description": "目标状态 true/false"}
		required = append(required, "key", "value")
	case "ai_complete":
		props["kind"] = map[string]string{"type": "string", "description": "name_parse 表示文件名解析"}
		props["filename"] = map[string]string{"type": "string", "description": "要解析的文件名"}
		props["prompt"] = map[string]string{"type": "string", "description": "自定义提示词"}
		props["content"] = map[string]string{"type": "string", "description": "输入内容"}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return json.RawMessage(b)
}
