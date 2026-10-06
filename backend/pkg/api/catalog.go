package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Param 接口参数说明
type Param struct {
	Name     string `json:"name"`
	In       string `json:"in"` // query | body | path
	Required bool   `json:"required"`
	Desc     string `json:"desc"`
}

// Endpoint 一个对外接口的描述
type Endpoint struct {
	Method   string  `json:"method"`
	Path     string  `json:"path"`
	Summary  string  `json:"summary"`
	Category string  `json:"category"`
	Params   []Param `json:"params,omitempty"`
	Example  string  `json:"example,omitempty"`
	Notes    string  `json:"notes,omitempty"`
	Requires string  `json:"requires,omitempty"` // browser / source-script / none
	// Role 区分「外部 AI 会用的编排路径」与「纯界面自用」。
	// 只有 internalEndpoints 名单推导出来，**不要在条目里手写**（散着写必然漂移）。
	Role string `json:"role,omitempty"`
	// Hidden 为 true 时不在前端「开放接口」抽屉里展示。由 Role 推导，保持与 Role 一致。
	// 接口本身完全保留：/api/catalog 仍会返回它，AI 加一个参数就能看到全部。
	Hidden bool `json:"hidden,omitempty"`
}

const (
	// roleCore：AI 替用户操作这个应用时会走到的接口（默认）。
	roleCore = "core"
	// roleInternal：纯界面自用、或调用方不该主动碰的（写队列副本、脚本内部协议、整体配置写入…）。
	//
	// 判定口径只有一条：**AI 调它是帮用户干活，还是根本不该调**。
	// 撤下的是「界面内部状态」与「危险的整体写操作」，不是「我不常用」。
	roleInternal = "internal"
)

// internalEndpoints 是 internal 的**唯一名单**（键 = "METHOD /path"）。
//
// 为什么不按分类整块折叠（旧做法是 hiddenCategories，把「AI 大模型」「飞牛集成」全折起来）：
// 分类粒度太粗 —— 「整理与去重」16 条里既有 AI 要用的补全，也有纯界面的命名规则；
// 「飞牛集成」里 rescan 是补全后要做的，token 手工写入则绝不该被外部调。
//
// 改这张表就是改折叠范围；`catalog_test.go` 会校验每个键都对得上真实条目。
var internalEndpoints = map[string]bool{
	// 系统：窗口记忆与日志是界面自己的事
	"GET /api/ui-prefs":  true,
	"POST /api/ui-prefs": true,
	"GET /api/logs":      true,
	"POST /api/logs":     true, // 网页端上报前端错误
	"DELETE /api/logs":   true,
	// 平台健康度：GET 留给 AI（「为什么这首歌搜不到」就该查它），清空是界面维护动作
	"DELETE /api/diagnostics/platforms": true,
	"POST /api/config":                  true, // 整体写配置太危险；读配置（GET）留给 AI 看当前设置

	// 音乐入口接管：开关本身要改环境变量 + 重启应用，不是 AI 能自行完成的动作。
	// 更重要的是 POST restore 会改变官方音乐的可用性 —— 这种能力不该由外部调用触发。
	"GET /api/takeover/status":   true,
	"POST /api/takeover/restore": true,

	// 音源脚本的批量维护（导入一堆脚本是界面维护动作，AI 不该自行扩面）
	"POST /api/sources/import_url":      true,
	"POST /api/sources/import_batch":    true,
	"POST /api/sources/batch_delete":    true,
	"GET /api/sources/export":           true,
	"POST /api/sources/cleanup":         true,
	"POST /api/sources/custom":          true, // 直接提交脚本正文，与 import_url 同族
	"DELETE /api/sources/custom/delete": true,

	// 下载队列的副本由网页端整体写回，外部写会打乱界面
	"PUT /api/download/queue": true,

	// 解析桥：pending 是网页端脚本领任务用的，claim/result 连目录都不进（见 routes.go 的 undocumentedRoutes）
	"GET /api/bridge/pending": true,

	// 监控调度与「补下载完成」回写都是页面侧闭环
	"GET /api/monitor/due":          true,
	"POST /api/monitor/tracks/mark": true,

	// 命名解析规则：界面里调正则用的，AI 用不到（补全会自动判名）
	"GET /api/library/name-rules":    true,
	"POST /api/library/name-rules":   true,
	"DELETE /api/library/name-rules": true,

	// 索引明细与首屏快照：给界面表格用的，AI 要缺口看 /api/library/gaps
	"GET /api/library/index/list":  true,
	"GET /api/library/index/stats": true,

	// 大模型配置与用量属于本应用自己的设置；AI 生成接口是给界面按钮用的
	"GET /api/ai/config":       true,
	"POST /api/ai/config":      true,
	"GET /api/ai/usage":        true,
	"POST /api/ai/usage/reset": true, // 清计数是界面维护动作
	"POST /api/ai/test":        true,
	"POST /api/ai/complete":    true,

	// 飞牛令牌手工写入与连通性自检是故障排查动作
	"POST /api/fnos/check": true,
	"POST /api/fnos/token": true,

	// 代理是给音源脚本跨域用的，不是通用抓取出口
	"POST /api/proxy/http": true,

	// 外部直链转音频流：播放由界面做，AI 要落盘走 /api/download/song
	"GET /api/player/stream": true,
}

const (
	catSystem   = "系统"
	catSearch   = "搜索与发现"
	catCharts   = "榜单与歌单"
	catSources  = "音源管理"
	catDownload = "下载"
	catNAS      = "本地曲库"
	catBridge   = "AI 解析桥"
	// 「推送」是一件事的两个目标，见 HANDOVER §3.2：
	//   catMonitor = 订阅来源 → 下载落盘到本地曲库
	//   catPush    = 订阅来源 → 匹配飞牛曲库 → 写入飞牛音乐歌单（缺的可补下载）
	catMonitor  = "推送 · 下载到曲库"
	catTidy     = "整理与去重"
	catAI       = "AI 大模型"
	catFnos     = "飞牛集成"
	catAccount  = "账号连接"
	catPush     = "推送 · 飞牛歌单"
	catProxy    = "网络代理"
	catTakeover = "音乐入口接管"
	catMusicDL  = "外挂音源（musicdl）"
)

// Workflow 一条「你说一句，AI 把整件事做完」的食谱。
//
// 目录的主要读者是**代替用户操作本应用的 AI**，不是翻接口手册的人。
// 光给 111 条平铺接口，它得自己猜该串哪几条 —— 而最容易猜错的地方恰好是最要紧的
// 那一步（在线取直链必须有开着的应用页面）。所以「怎么做」写在结构里，不藏在 notes 里。
type Workflow struct {
	Task      string   `json:"task"`
	Summary   string   `json:"summary,omitempty"`
	Steps     []string `json:"steps"`
	Endpoints []string `json:"endpoints"`           // "METHOD /path"，必须能在目录里找到
	WatchOut  string   `json:"watch_out,omitempty"` // 这一步最容易失败/最贵的坑
}

// Guide 是 /api/catalog 的「先告诉 AI 这是干嘛的、该怎么做」那段。
type Guide struct {
	What      []string   `json:"what"`
	Prereq    []string   `json:"prereq"`
	Workflows []Workflow `json:"workflows"`
	Rules     []string   `json:"rules"`
}

// APICatalog 返回全部对外接口描述。
//
// 这是「接口文档」的单一数据源：既通过 GET /api/catalog 提供给外部 AI 发现，
// 也被前端「开放接口」抽屉直接渲染。接口**一条都不删**，
// 纯界面自用的那批标成 role=internal（抽屉默认不展示，AI 加看 internal 仍能用）。
//
// ⚠️ 新增接口必须同步登记到这里，否则 AI 发现不了这个能力；
// 对账分两层：源码层是 `catalog_routes_test.go` 的「路由 ↔ 目录」双向比对（路由表在 `routes.go`）；
// 线上层跑 `node .dev/catalog-audit.mjs [baseURL]`，只核「跑着的那个进程是不是源码这一版」。
func APICatalog(baseURL string) []Endpoint {
	if baseURL == "" {
		baseURL = "http://<NAS-IP>:8899"
	}

	list := []Endpoint{
		// ── 系统 ──
		{
			Method: http.MethodGet, Path: "/api/health", Summary: "健康检查与运行状态", Category: catSystem,
			Example: "curl " + baseURL + "/api/health",
			Notes:   "不需要鉴权，可用于探活。`auth_mode` 报 none / lan / token：lan 表示本机与内网免 Token、其它来源会被拒（只认 TCP 对端地址）。",
		},
		{
			Method: http.MethodGet, Path: "/api/catalog", Summary: "获取本接口目录（供 AI 自动发现）", Category: catSystem,
			Example: "curl " + baseURL + "/api/catalog",
			Notes:   "返回结构化的接口清单，AI 可先读它再决定调用哪些接口。`auth_mode` 含义同 /api/health。",
		},
		{
			Method: http.MethodGet, Path: "/api/app/version", Summary: "获取应用版本", Category: catSystem,
			Example: "curl " + baseURL + "/api/app/version",
		},
		{
			Method: http.MethodGet, Path: "/api/config", Summary: "读取应用配置（含主力平台 preferred_platforms）", Category: catSystem,
			Example: "curl " + baseURL + "/api/config",
			Notes:   "改任何设置前**先 GET 拿全量**，改完再整份 POST 回去 —— 见下一条的警告。",
		},
		{
			Method: http.MethodPost, Path: "/api/config", Summary: "更新应用配置", Category: catSystem,
			Params: []Param{
				{Name: "default_nas_dir", In: "body", Desc: "默认曲库目录"},
				{Name: "download_dir", In: "body", Desc: "下载保存目录"},
				{Name: "preferred_platforms", In: "body", Desc: "主力平台有序数组，默认 [wy,tx]；其余平台只在主力没有时兜底"},
			},
			Example: "curl -X POST " + baseURL + "/api/config -H 'Content-Type: application/json' -d '{\"download_dir\":\"/vol1/Music\"}'",
			Notes: "⚠️ 这是**选择性合并**：只有非空字段会写入（nil 切片表示不动）。" +
				"想改一项就先 GET 全量、改完整份送回，别只发一个键 —— 免得踩到没被 patch 覆盖的设置。",
		},
		{
			Method: http.MethodGet, Path: "/api/ui-prefs", Summary: "读取全部窗口记忆（各页面点击状态）", Category: catSystem,
			Example: "curl " + baseURL + "/api/ui-prefs",
			Notes:   "返回前端各页面的选中状态（发现页 Tab、榜单/搜索平台、NAS 目录、播放模式等），持久化在服务端 ui_prefs.json，跨浏览器共享。",
		},
		{
			Method: http.MethodPost, Path: "/api/ui-prefs", Summary: "合并写入窗口记忆", Category: catSystem,
			Params: []Param{
				{Name: "key", In: "body", Desc: "记忆键名，如 discover_tab / search_platform / nas_dir / play_mode"},
				{Name: "value", In: "body", Desc: "任意 JSON 值（字符串 / 数字 / 布尔 / 对象）"},
			},
			Example: "curl -X POST " + baseURL + "/api/ui-prefs -H 'Content-Type: application/json' -d '{\"search_platform\":\"wy\"}'",
			Notes:   "body 为 {key: value, ...} 的扁平对象，逐键合并；非法键名（含路径分隔符/空格/超长）会被拒绝。",
		},
		{
			Method: http.MethodGet, Path: "/api/logs", Summary: "读取后端运行日志", Category: catSystem,
			Params:  []Param{{Name: "limit", In: "query", Desc: "返回条数，默认 200，最大 2000"}},
			Example: "curl '" + baseURL + "/api/logs?limit=100'",
			Notes: "日志只保留在**内存环形缓冲**里（最多 2000 条），**重启应用即清空** —— 不落盘，" +
				"避免日志里可能出现的路径与令牌长期留在 NAS 磁盘上。" +
				"每条带 `source` 区分来源：`backend`（服务端自身）或 `frontend`（网页端上报）。",
		},
		{
			Method: http.MethodDelete, Path: "/api/logs", Summary: "清空后端运行日志缓冲", Category: catSystem,
			Example: "curl -X DELETE '" + baseURL + "/api/logs'",
			Notes:   "只清内存里的记录，不影响任何文件与配置。",
		},
		{
			Method: http.MethodPost, Path: "/api/logs", Summary: "网页端上报前端错误到运行日志", Category: catSystem,
			Example: "curl -X POST '" + baseURL + "/api/logs' -H 'Content-Type: application/json' -d '{\"level\":\"error\",\"message\":\"导出歌单失败\"}'",
			Notes: "**给网页端自己用的**：界面提示只留一句人话，完整信息（接口路径、堆栈）经这里进日志。" +
				"body 为 `{source, level, message}`：`level` 只认 `info` / `warn` / `error`（其他按 `info` 处理），" +
				"`message` 必填、超 2000 字符截断，`source` 省略时按 `frontend` 记。",
		},
		{
			Method: http.MethodGet, Path: "/api/diagnostics/platforms", Summary: "查看各平台请求健康度（失败计数与最近明细）", Category: catSystem,
			Params:  []Param{{Name: "limit", In: "query", Desc: "最近明细条数，默认 20，最大 200"}},
			Example: "curl '" + baseURL + "/api/diagnostics/platforms'",
			Notes: "平台接口悄悄改版时，界面往往只表现为「搜到 0 条」。这里把失败摊开：`failed` = 请求没成功" +
				"（HTTP 非 200 / 网络错误，确定是故障）；`empty` = 请求成功但 0 条结果（**不算失败**，是平台退化的第一信号）。" +
				"统计从进程启动开始，重启清零；不含历史。",
		},
		{
			Method: http.MethodDelete, Path: "/api/diagnostics/platforms", Summary: "清空平台请求健康度统计", Category: catSystem,
			Example: "curl -X DELETE '" + baseURL + "/api/diagnostics/platforms'",
			Notes:   "只清内存里的计数与明细，不影响任何文件与配置。",
		},

		// ── 搜索与发现 ──
		{
			Method: http.MethodGet, Path: "/api/search", Summary: "全网搜索歌曲（多平台聚合去噪）", Category: catSearch,
			Params: []Param{
				{Name: "q", In: "query", Required: true, Desc: "关键词，如「周杰伦 夜曲」"},
				{Name: "source", In: "query", Desc: "平台 wy/tx/kg/kw，默认 all 聚合"},
				{Name: "page", In: "query", Desc: "页码，默认 1"},
			},
			Example:  "curl '" + baseURL + "/api/search?q=周杰伦%20夜曲&source=all'",
			Notes:    "纯服务端能力，不需要浏览器，也不需要音源脚本。",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/music/qualities", Summary: "查询某首歌在指定平台真实可用的音质档位", Category: catSearch,
			Params: []Param{
				{Name: "source", In: "query", Required: true, Desc: "平台 wy/tx/kg/kw"},
				{Name: "id", In: "query", Required: true, Desc: "平台歌曲标识（也可传 songmid / hash）"},
				{Name: "name", In: "query", Desc: "曲名。缺省时无法搜索，将直接返回空音质列表"},
				{Name: "singer", In: "query", Desc: "歌手，提高匹配准确度"},
			},
			Example: "curl '" + baseURL + "/api/music/qualities?source=wy&id=186016&name=晴天&singer=周杰伦'",
			Notes: "实现方式为「搜索 + 按 ID 匹配」：各平台详情接口实测普遍不可用（网易不返回音质、" +
				"QQ 缺无损、酷狗返回空壳、酷我要 token），故改用与 /api/search 同一数据源。" +
				"返回 qualitys 按档位从高到低排列，best 为最高可用档位；" +
				"matched_by 说明匹配方式（id / name_singer / none），none 时 qualitys 为空 —— 绝不臆造档位。",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/search/lyric", Summary: "按歌曲信息抓取 LRC 歌词（含翻译）", Category: catSearch,
			Params: []Param{
				{Name: "source", In: "query", Desc: "wy/tx/kg/kw"},
				{Name: "songmid", In: "query", Desc: "平台歌曲 ID"},
				{Name: "title", In: "query", Desc: "歌名（无 songmid 时用于检索）"},
				{Name: "singer", In: "query", Desc: "歌手"},
				{Name: "duration", In: "query", Desc: "时长（秒），用于匹配校验"},
			},
			Example:  "curl '" + baseURL + "/api/search/lyric?source=wy&songmid=186016&title=夜曲&singer=周杰伦'",
			Notes:    "返回 data.lyric 原文与 data.tlyric 翻译。",
			Requires: "none",
		},

		// ── 榜单与歌单 ──
		{
			Method: http.MethodGet, Path: "/api/charts/toplists", Summary: "获取排行榜列表", Category: catCharts,
			Params:   []Param{{Name: "source", In: "query", Desc: "平台，默认 kw"}},
			Example:  "curl '" + baseURL + "/api/charts/toplists?source=kw'",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/charts/detail", Summary: "获取排行榜详情（歌曲列表）", Category: catCharts,
			Params: []Param{
				{Name: "id", In: "query", Required: true, Desc: "榜单 ID（来自 toplists）"},
				{Name: "source", In: "query", Desc: "平台"},
			},
			Example:  "curl '" + baseURL + "/api/charts/detail?id=93&source=kw'",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/charts/playlists", Summary: "获取分类歌单列表", Category: catCharts,
			Params: []Param{
				{Name: "category", In: "query", Desc: "分类，默认「全部」"},
				{Name: "page", In: "query", Desc: "页码"},
				{Name: "limit", In: "query", Desc: "每页数量，默认 24"},
				{Name: "source", In: "query", Desc: "平台"},
			},
			Example:  "curl '" + baseURL + "/api/charts/playlists?category=全部&limit=24'",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/charts/playlist/categories", Summary: "获取某平台的歌单分类（供前端快捷筛选）", Category: catCharts,
			Params: []Param{
				{Name: "source", In: "query", Desc: "平台 wy/tx/kg/kw，默认 wy"},
			},
			Example: "curl '" + baseURL + "/api/charts/playlist/categories?source=tx'",
			Notes: "**各平台能力不同**：网易云有固定分类表；QQ 从它的分类配置接口**动态拉取**" +
				"（不写死 id 映射，避免它调整后静默失效）；酷狗 / 酷我当前的歌单接口不吃分类参数，" +
				"返回**空数组**（而不是报错），前端据此隐藏筛选条。" +
				"拉取失败同样返回空数组并附 warning，不阻塞页面。",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/charts/playlist/detail", Summary: "获取歌单详情（歌曲列表）", Category: catCharts,
			Params: []Param{
				{Name: "url", In: "query", Desc: "歌单**分享链接**（推荐）。支持网易云 / QQ / 酷狗 / 酷我，会自动识别平台并抽出 id"},
				{Name: "id", In: "query", Desc: "歌单 ID（不传 url 时使用）"},
				{Name: "source", In: "query", Desc: "平台 wy/tx/kg/kw，默认 wy"},
			},
			Example: "curl '" + baseURL + "/api/charts/playlist/detail?url=https%3A%2F%2Fmusic.163.com%2F%23%2Fplaylist%3Fid%3D123456'",
			Notes: "用户从平台 App「复制链接」拿到的是分享链接而不是裸 id，所以这里支持直接传 url。" +
				"**App 短链也能直接传**（163cn.tv / c6.y.qq.com/… / t1.kugou.com 等）—— " +
				"短码里不是 id，后端会跟随重定向或解析其 JSON 拿到真实地址再识别。" +
				"认不出平台、或短码已失效时返回 400 + 中文指引，不会静默当成搜索关键词。",
			Requires: "none",
		},

		// ── 音源管理 ──
		{
			Method: http.MethodGet, Path: "/api/sources/api", Summary: "列出「按服务地址」接入的 API 音源", Category: catSources,
			Example: "curl " + baseURL + "/api/sources/api",
			Notes:   "返回**不回显 token**，只给 has_token 布尔值。协议 label 为中文说明。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/api", Summary: "新增 / 更新一个 API 音源（按服务地址）", Category: catSources,
			Params: []Param{
				{Name: "base_url", In: "body", Required: true, Desc: "服务地址，如 http://192.168.1.10:8080"},
				{Name: "token", In: "body", Desc: "鉴权令牌（可选）。更新时留空表示不修改"},
				{Name: "protocol", In: "body", Desc: "协议 lx_script / lx_server / lx_api；留空则自动探测"},
				{Name: "name", In: "body", Desc: "显示名，默认用服务地址"},
				{Name: "id", In: "body", Desc: "传入则更新已有项"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/api -H 'Content-Type: application/json' -d '{\"base_url\":\"http://192.168.1.10:8080\"}'",
			Notes: "这是**服务型**音源：后端直接请求该地址拿直链，**不执行任何第三方 JS**。" +
				"与脚本型音源（/api/sources/import_url，浏览器执行）互补。" +
				"同地址重复添加视为更新，不会堆重复项。" +
				"支持内网/本机地址（如 http://192.168.1.10:8080、http://127.0.0.1:3000），无需额外配置。",
		},
		{
			Method: http.MethodDelete, Path: "/api/sources/api", Summary: "删除一个 API 音源", Category: catSources,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "音源 ID"}},
			Example: "curl -X DELETE '" + baseURL + "/api/sources/api?id=api_xxx'",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/api/detect", Summary: "探测服务地址属于哪种音源协议（不保存）", Category: catSources,
			Params: []Param{
				{Name: "base_url", In: "body", Required: true, Desc: "服务地址"},
				{Name: "token", In: "body", Desc: "鉴权令牌（可选）"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/api/detect -H 'Content-Type: application/json' -d '{\"base_url\":\"http://192.168.1.10:8080\"}'",
			Notes: "依次尝试三种协议（路由互不相同，不会互相误判）：" +
				"私有 REST（GET /url）→ 标准 LX Server（GET /api/music/url）→ 自建中转 API（POST /music/url）。" +
				"404 视为该协议不存在；返回 JSON 且含 url/code/data 即视为匹配 —— " +
				"探针歌曲本身解析失败也算匹配，避免「歌下架」被误判成「音源坏了」。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/api/resolve", Summary: "用指定的 API 音源取一条可播直链", Category: catSources,
			Params: []Param{
				{Name: "id", In: "body", Required: true, Desc: "API 音源 ID"},
				{Name: "platform", In: "body", Required: true, Desc: "平台 wy / tx / kg / kw"},
				{Name: "song_id", In: "body", Required: true, Desc: "平台内的歌曲 ID"},
				{Name: "quality", In: "body", Desc: "音质档位，默认 320k"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/api/resolve -H 'Content-Type: application/json' -d '{\"id\":\"api_xxx\",\"platform\":\"wy\",\"song_id\":\"421423808\"}'",
			Notes:   "前端播放/下载时调用它取直链，因此**无需在浏览器里执行任何第三方代码**。",
		},
		{
			Method: http.MethodGet, Path: "/api/protocols", Summary: "列出音源协议（官方协议 / LX 协议）及各自能力与就绪状态", Category: catSources,
			Notes: "官方协议由后端内置，免脚本即可搜索 / 逛榜单 / 看歌词；" +
				"LX 协议是浏览器沙箱执行的用户脚本，负责取直链。二者互补，不是替代关系。",
			Example:  "curl " + baseURL + "/api/protocols",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/sources", Summary: "列出已导入音源与当前激活音源", Category: catSources,
			Example:  "curl " + baseURL + "/api/sources",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/sources/script", Summary: "读取某条音源的脚本正文", Category: catSources,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "音源 ID（也可用 /api/sources/{id}/script 路径写法）"}},
			Example: "curl '" + baseURL + "/api/sources/script?id=custom_1700000000'",
			Notes:   "返回原始 JS 正文，用于排查音源问题或导出单条脚本；要备份全部音源用 /api/sources/export。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/active", Summary: "启用 / 停用音源（多选激活）", Category: catSources,
			Params: []Param{
				{Name: "id", In: "body", Required: true, Desc: "音源 ID"},
				{Name: "active", In: "body", Desc: "true=加入启用池（追加到池尾，池有序 = 取链优先级）；false=从池里摘掉；**不传**=旧语义「池里只留它」"},
			},
			Notes: "启用池可以同时有多个音源：取链按池内顺序试，第一个失败自动换下一个。池为空是合法状态（用户有权一个都不启用）。" +
				"响应里的 active_source_id 恒等于池首（给只认单值的旧调用方），完整池见 active_source_ids。",
			Example: "curl -X POST " + baseURL + "/api/sources/active -H 'Content-Type: application/json' -d '{\"id\":\"custom_1700000000\",\"active\":true}'",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/import_url", Summary: "通过 URL 导入第三方音源脚本", Category: catSources,
			Params: []Param{
				{Name: "url", In: "body", Required: true, Desc: "脚本 raw 链接"},
				{Name: "name", In: "body", Desc: "音源别名"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/import_url -H 'Content-Type: application/json' -d '{\"url\":\"https://example.com/latest.js\",\"name\":\"我的音源\"}'",
			Notes:   "仅允许 http/https 公网地址，禁止内网与回环地址（SSRF 防护）。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/import_batch", Summary: "批量导入音源（在线 URL + 本地脚本，自动去重）", Category: catSources,
			Params: []Param{
				{Name: "urls", In: "body", Desc: "在线脚本地址数组，如 [\"https://a.js\",\"https://b.js\"]"},
				{Name: "scripts", In: "body", Desc: "本地脚本数组，每项 {filename, content, name?}"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/import_batch -H 'Content-Type: application/json' -d '{\"urls\":[\"https://example.com/a.js\"],\"scripts\":[{\"filename\":\"mine.js\",\"content\":\"/*! @name 我的音源 */\"}]}'",
			Notes: "两类可混用，单次合计上限 50 条。ID 由脚本内容哈希生成，因此：① 批量导入不会 ID 冲突；" +
				"② 重复导入同一脚本自动跳过（结果里 status=skipped），不会产生副本。" +
				"元数据（@name/@version/@author/@description）自动从脚本头部注释提取，无需手工填写。" +
				"在线地址同样受 SSRF 防护约束。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/custom", Summary: "新增自定义音源（直接提交脚本正文）", Category: catSources,
			Params: []Param{
				{Name: "script", In: "body", Required: true, Desc: "脚本正文"},
				{Name: "name", In: "body", Desc: "音源别名"},
				{Name: "description", In: "body", Desc: "描述"},
				{Name: "version", In: "body", Desc: "版本"},
				{Name: "author", In: "body", Desc: "作者"},
				{Name: "filename", In: "body", Desc: "原始文件名（仅作展示）"},
			},
			Example: "curl -X POST " + baseURL + "/api/sources/custom -H 'Content-Type: application/json' -d '{\"name\":\"我的音源\",\"script\":\"/*! @name 我的音源 */\"}'",
			Notes: "ID 是时间戳（custom_<unix>），与 import_batch 的内容哈希 ID 不同：" +
				"同一条脚本重复提交会产生副本，要幂等导入请用 /api/sources/import_batch 或 POST /api/sources/cleanup 去重。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/batch_delete", Summary: "批量删除音源", Category: catSources,
			Params:  []Param{{Name: "ids", In: "body", Required: true, Desc: "音源 ID 数组"}},
			Example: "curl -X POST " + baseURL + "/api/sources/batch_delete -H 'Content-Type: application/json' -d '{\"ids\":[\"custom_ab12\"]}'",
			Notes:   "删除激活中的音源会自动改选剩余的第一个；返回 not_found 便于核对传错的 ID。",
		},
		{
			Method: http.MethodDelete, Path: "/api/sources/custom/delete", Summary: "删除单条自定义音源", Category: catSources,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "音源 ID（界面用 POST + body 的 id / source_id，两种方法等价）"}},
			Example: "curl -X DELETE '" + baseURL + "/api/sources/custom/delete?id=custom_1700000000'",
			Notes:   "删除激活中的音源会自动改选剩余的第一个；删除 API 型音源请用 DELETE /api/sources/api。",
		},
		{
			Method: http.MethodGet, Path: "/api/sources/export", Summary: "导出全部音源（备份 / 迁移到其他设备）", Category: catSources,
			Example: "curl " + baseURL + "/api/sources/export",
			Notes:   "返回的 data.scripts 可直接作为 POST /api/sources/import_batch 的 body，在新设备一键还原。",
		},
		{
			Method: http.MethodPost, Path: "/api/sources/cleanup", Summary: "清理重复音源（内容相同者只留最早一条）", Category: catSources,
			Params:  []Param{{Name: "dry_run", In: "body", Desc: "默认 true 只报告；传 false 才真删"}},
			Example: "curl -X POST " + baseURL + "/api/sources/cleanup -H 'Content-Type: application/json' -d '{\"dry_run\":true}'",
			Notes:   "兼容历史数据：早期导入用时间戳 ID，可能与后来导入的同内容脚本并存。默认干跑，避免误删。",
		},

		// ── 下载 ──
		{
			Method: http.MethodPost, Path: "/api/download/song", Summary: "下载单曲到 NAS（需先拿到直链）", Category: catDownload,
			Params: []Param{
				{Name: "name", In: "body", Required: true, Desc: "歌名"},
				{Name: "singer", In: "body", Required: true, Desc: "歌手"},
				{Name: "album", In: "body", Desc: "专辑"},
				{Name: "cover", In: "body", Desc: "封面 URL，会写入标签"},
				{Name: "url", In: "body", Required: true, Desc: "音频直链"},
				{Name: "referer", In: "body", Desc: "防盗链 Referer"},
				{Name: "quality", In: "body", Desc: "实际音质，用于决定落盘扩展名"},
				{Name: "embed_lyric", In: "body", Desc: "是否把歌词内嵌进音频标签"},
				{Name: "write_lrc", In: "body", Desc: "是否同时写出同名 .lrc"},
				{Name: "embed_cover", In: "body", Desc: "是否内嵌封面（字段缺失＝内嵌；只有显式 false 才不抓）"},
			},
			Example:  "curl -X POST " + baseURL + "/api/download/song -H 'Content-Type: application/json' -d '{\"name\":\"夜曲\",\"singer\":\"周杰伦\",\"url\":\"https://.../a.flac\",\"embed_lyric\":true,\"write_lrc\":true}'",
			Notes:    "直链需由调用方提供（通常来自 /api/bridge/resolve 或已在播放器解析过的链接）。",
			Requires: "none",
		},
		{
			Method: http.MethodPost, Path: "/api/download/enrich", Summary: "给已落盘的文件补标签（不重下音频）", Category: catDownload,
			Params: []Param{
				{Name: "path", In: "body", Required: true, Desc: "已落盘音频的绝对路径，必须在下载目录内"},
				{Name: "name", In: "body", Required: true, Desc: "歌名"},
				{Name: "singer", In: "body", Required: true, Desc: "歌手"},
				{Name: "album", In: "body", Desc: "专辑"},
				{Name: "cover", In: "body", Desc: "封面 URL，会写入标签"},
				{Name: "embed_lyric", In: "body", Desc: "是否把歌词内嵌进音频标签"},
				{Name: "write_lrc", In: "body", Desc: "是否同时写出同名 .lrc"},
				{Name: "embed_cover", In: "body", Desc: "是否内嵌封面（字段缺失＝内嵌）"},
			},
			Example:  "curl -X POST " + baseURL + "/api/download/enrich -H 'Content-Type: application/json' -d '{\"path\":\"/vol1/Music/a.mp3\",\"name\":\"夜曲\",\"singer\":\"周杰伦\",\"embed_lyric\":true,\"write_lrc\":true}'",
			Notes:    "给**已经存在**的文件补标签，不重新下载音频 —— 「边听边下」提升出来的文件正是靠它补上标题/歌手/封面/歌词。path 必须在下载目录内（这个接口会写盘）。",
			Requires: "none",
		},
		{
			Method: http.MethodPost, Path: "/api/download/batch", Summary: "批量下载（服务端后台并发队列）", Category: catDownload,
			Params:  []Param{{Name: "songs", In: "body", Required: true, Desc: "SongPayload 数组"}},
			Example: "curl -X POST " + baseURL + "/api/download/batch -H 'Content-Type: application/json' -d '{\"songs\":[{...}]}'",
		},
		{
			Method: http.MethodGet, Path: "/api/download/batch/status", Summary: "查询批量下载任务进度", Category: catDownload,
			Params:  []Param{{Name: "task_id", In: "query", Required: true, Desc: "批量任务 ID"}},
			Example: "curl '" + baseURL + "/api/download/batch/status?task_id=task_123'",
		},
		{
			Method: http.MethodPost, Path: "/api/download/check", Summary: "批量检查歌曲是否已下载", Category: catDownload,
			Params:  []Param{{Name: "songs", In: "body", Required: true, Desc: "待检查歌曲数组"}},
			Example: "curl -X POST " + baseURL + "/api/download/check -H 'Content-Type: application/json' -d '{\"songs\":[{\"id\":\"1\",\"name\":\"夜曲\",\"singer\":\"周杰伦\"}]}'",
		},
		{
			Method: http.MethodGet, Path: "/api/download/queue", Summary: "读取下载队列（持久化在 NAS 上）", Category: catDownload,
			Example: "curl " + baseURL + "/api/download/queue",
		},
		{
			Method: http.MethodPut, Path: "/api/download/queue", Summary: "整体替换下载队列", Category: catDownload,
			Params:  []Param{{Name: "tasks", In: "body", Required: true, Desc: "完整队列数组（整体替换，不是追加）"}},
			Example: "curl -X PUT " + baseURL + "/api/download/queue -H 'Content-Type: application/json' -d '{\"tasks\":[]}'",
		},
		{
			Method: http.MethodGet, Path: "/api/download/config", Summary: "读取下载目录配置", Category: catDownload,
			Example: "curl " + baseURL + "/api/download/config",
		},
		{
			Method: http.MethodPost, Path: "/api/download/config", Summary: "设置下载保存目录", Category: catDownload,
			Params:  []Param{{Name: "download_dir", In: "body", Required: true, Desc: "绝对路径，如 /vol1/Music"}},
			Example: "curl -X POST " + baseURL + "/api/download/config -H 'Content-Type: application/json' -d '{\"download_dir\":\"/vol1/Music\"}'",
		},

		// ── 本地曲库 ──
		{
			Method: http.MethodGet, Path: "/api/nas/folders", Summary: "列出可用的曲库根目录", Category: catNAS,
			Example: "curl " + baseURL + "/api/nas/folders",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/browse", Summary: "浏览目录（含音频数量统计）", Category: catNAS,
			Params: []Param{
				{Name: "path", In: "query", Desc: "目录绝对路径"},
				{Name: "files", In: "query", Desc: "传 1 时额外返回普通文件列表（默认不返回，避免放大 JSON）"},
				{Name: "ext", In: "query", Desc: "配合 files=1 过滤扩展名，逗号分隔，如 js,mjs"},
			},
			Example: "curl '" + baseURL + "/api/nas/browse?path=/vol1/Music&files=1&ext=js'",
			Notes:   "仅允许访问配置的曲库根目录，越权路径会被拒绝。",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/read", Summary: "读取文本文件（从 NAS 选音源脚本用）", Category: catNAS,
			Params:  []Param{{Name: "path", In: "query", Required: true, Desc: "文件绝对路径"}},
			Example: "curl '" + baseURL + "/api/nas/read?path=/vol1/scripts/source.js'",
			Notes: "三道约束：路径必须过 ResolveSafePath、扩展名白名单（.js/.mjs/.cjs/.json/.txt）、体积上限 2MB；" +
				"非 UTF-8 内容也会被拒。**这不是通用文件读取接口**，别拿它读配置或密钥。",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/songs", Summary: "扫描并列出目录下的歌曲", Category: catNAS,
			Params: []Param{
				{Name: "dir", In: "query", Desc: "目录绝对路径"},
				{Name: "refresh", In: "query", Desc: "传 1 强制重新扫描"},
			},
			Example: "curl '" + baseURL + "/api/nas/songs?dir=/vol1/Music'",
			Notes:   "与 /api/library/index 同一套枚举规则（排除 @eaDir 等目录、深度上限 12 层、软链接环剪枝）。单次最多 1000 首；被截断或目录读不全都如实返回 truncated/warnings，命中缓存也照样带着。",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/stream", Summary: "流式播放本地音频（支持 Range 拖动）", Category: catNAS,
			Params:  []Param{{Name: "path", In: "query", Required: true, Desc: "音频文件绝对路径"}},
			Example: "curl -I '" + baseURL + "/api/nas/stream?path=/vol1/Music/a.flac'",
			Notes:   "可直接作为 <audio src> 使用。",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/cover", Summary: "提取音频内嵌封面或同目录封面图", Category: catNAS,
			Params:  []Param{{Name: "path", In: "query", Required: true, Desc: "音频文件路径"}},
			Example: "curl -o cover.jpg '" + baseURL + "/api/nas/cover?path=/vol1/Music/a.flac'",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/lyric", Summary: "读取本地歌词（外挂 .lrc 或内嵌标签）", Category: catNAS,
			Params:  []Param{{Name: "path", In: "query", Required: true, Desc: "音频文件路径"}},
			Example: "curl '" + baseURL + "/api/nas/lyric?path=/vol1/Music/a.flac'",
		},
		{
			Method: http.MethodGet, Path: "/api/nas/tags", Summary: "读取音频标签（标题/歌手/专辑/风格/年份/曲序/光盘 + 有无歌词封面）", Category: catNAS,
			Params:  []Param{{Name: "path", In: "query", Required: true, Desc: "MP3 / FLAC 文件路径"}},
			Example: "curl '" + baseURL + "/api/nas/tags?path=/vol1/Music/a.flac'",
			Notes: "回 `title/artist/album/genre/year/track/disc/has_lyric/has_cover/lyric_length/format`；" +
				"没写的数字字段回 0、文本字段回空串。想知道「刚才那次写入到底成没成」，读这里而不是信提交参数。",
		},
		{
			Method: http.MethodPost, Path: "/api/nas/tags", Summary: "写入标签：内嵌歌词 / 封面 / 标题歌手专辑风格", Category: catNAS,
			Params: []Param{
				{Name: "path", In: "body", Required: true, Desc: "MP3 / FLAC 文件路径"},
				{Name: "lyric", In: "body", Desc: "LRC 歌词文本"},
				{Name: "fetch", In: "body", Desc: "为 true 且未提供 lyric 时联网抓取"},
				{Name: "title/artist/album", In: "body", Desc: "元数据"},
				{Name: "genre", In: "body", Desc: "风格（飞牛「风格」页读的就是这个内嵌字段）"},
				{Name: "cover", In: "body", Desc: "封面图片 URL"},
				{Name: "write_lrc", In: "body", Desc: "同时写出同名 .lrc"},
			},
			Example: "curl -X POST " + baseURL + "/api/nas/tags -H 'Content-Type: application/json' -d '{\"path\":\"/vol1/Music/a.flac\",\"fetch\":true,\"write_lrc\":true}'",
			Notes: "只支持 MP3 / FLAC，原子替换，失败不损坏原文件。⚠️ **合并语义**：没传的字段保留文件原值 —— " +
				"只传 `path + cover` 换封面不会抹掉标题/歌手/歌词。封面失败看返回里的 `cover_error`。",
		},

		// ── AI 解析桥 ──
		{
			Method: http.MethodPost, Path: "/api/bridge/resolve", Summary: "提交歌曲直链解析任务（由网页端音源脚本完成）", Category: catBridge,
			Params: []Param{
				{Name: "platform", In: "body", Required: true, Desc: "平台 wy/tx/kg/kw"},
				{Name: "songmid", In: "body", Desc: "平台歌曲 ID（与 id/hash 至少一个）"},
				{Name: "name", In: "body", Desc: "歌名"},
				{Name: "singer", In: "body", Desc: "歌手"},
				{Name: "quality", In: "body", Desc: "highest / flac / 320k / 128k"},
				{Name: "download", In: "body", Desc: "true 表示网页端解析后顺带加入下载队列"},
			},
			Example:  "curl -X POST " + baseURL + "/api/bridge/resolve -H 'Content-Type: application/json' -d '{\"platform\":\"wy\",\"songmid\":\"186016\",\"name\":\"夜曲\",\"singer\":\"周杰伦\",\"quality\":\"highest\",\"download\":true}'",
			Notes:    "返回 job_id。解析依赖曲率 页面保持打开（音源脚本只能在浏览器执行），建议轮询 /api/bridge/jobs/{job_id}。",
			Requires: "browser",
		},
		{
			Method: http.MethodGet, Path: "/api/bridge/jobs/{job_id}", Summary: "查询解析任务结果", Category: catBridge,
			Params:   []Param{{Name: "job_id", In: "path", Required: true, Desc: "任务 ID"}},
			Example:  "curl " + baseURL + "/api/bridge/jobs/job_abc123",
			Notes:    "status: pending/claimed/done/failed/expired。done 时 data.url 为音频直链。",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/bridge/pending", Summary: "【网页端内部使用】列出待处理解析任务", Category: catBridge,
			Example:  "curl " + baseURL + "/api/bridge/pending",
			Requires: "browser",
		},
		{
			Method: http.MethodGet, Path: "/api/bridge/stats", Summary: "解析桥队列概况", Category: catBridge,
			Example: "curl " + baseURL + "/api/bridge/stats",
		},

		// ── 歌单监控 ──
		{
			Method: http.MethodGet, Path: "/api/monitors", Summary: "列出全部监控与运行状态", Category: catMonitor,
			Example: "curl " + baseURL + "/api/monitors",
			Notes:   "返回 monitors（配置）、running（正在跑的 ID）、stats（曲目状态统计）。",
		},
		{
			Method: http.MethodPost, Path: "/api/monitors", Summary: "新建监控（榜单/歌单/收藏夹）", Category: catMonitor,
			Params: []Param{
				{Name: "name", In: "body", Required: true, Desc: "监控名称"},
				{Name: "kind", In: "body", Required: true, Desc: "chart / playlist / favorites"},
				{Name: "target", In: "body", Desc: "目标：{charts:[], playlists:[], playlist_ids:[]}"},
				{Name: "quality", In: "body", Desc: "standard / high / lossless / hires，默认 lossless"},
				{Name: "fallback", In: "body", Desc: "best_effort（默认）/ skip"},
				{Name: "auto_download", In: "body", Desc: "是否自动下载，默认 true"},
				{Name: "interval_minutes", In: "body", Desc: "检查间隔（分钟），默认 360"},
				{Name: "max_downloads", In: "body", Desc: "单轮下载上限，默认 30"},
				{Name: "include_kw", In: "body", Desc: "必含关键词（逗号/空格分隔）"},
				{Name: "exclude_kw", In: "body", Desc: "排除关键词"},
			},
			Example: "curl -X POST " + baseURL + "/api/monitors -H 'Content-Type: application/json' -d '{\"name\":\"每周新歌\",\"kind\":\"playlist\",\"target\":{\"playlists\":[{\"link\":\"https://music.163.com/playlist?id=123\"}]},\"quality\":\"lossless\"}'",
			Notes:   "首次执行会先建立基线（只登记曲目、不批量下载），第二轮才开始下载新增曲目。",
		},
		{
			Method: http.MethodGet, Path: "/api/monitors/{id}", Summary: "监控详情（含最近运行历史与统计）", Category: catMonitor,
			Params:  []Param{{Name: "id", In: "path", Required: true, Desc: "监控 ID"}},
			Example: "curl " + baseURL + "/api/monitors/mon_123",
		},
		{
			Method: http.MethodPatch, Path: "/api/monitors/{id}", Summary: "更新监控配置", Category: catMonitor,
			Params:  []Param{{Name: "id", In: "path", Required: true, Desc: "监控 ID"}},
			Example: "curl -X PATCH " + baseURL + "/api/monitors/mon_123 -H 'Content-Type: application/json' -d '{\"enabled\":false}'",
		},
		{
			Method: http.MethodDelete, Path: "/api/monitors/{id}", Summary: "删除监控（级联删除曲目记录）", Category: catMonitor,
			Params:  []Param{{Name: "id", In: "path", Required: true, Desc: "监控 ID"}},
			Example: "curl -X DELETE " + baseURL + "/api/monitors/mon_123",
		},
		{
			Method: http.MethodPost, Path: "/api/monitors/{id}/discover", Summary: "提交抓取到的曲目列表（由网页端或 AI 提供）", Category: catMonitor,
			Params: []Param{
				{Name: "id", In: "path", Required: true, Desc: "监控 ID"},
				{Name: "songs", In: "body", Required: true, Desc: "曲目数组，每项含 source/id/name/artist/album/duration/cover/url"},
			},
			Example:  "curl -X POST " + baseURL + "/api/monitors/mon_123/discover -H 'Content-Type: application/json' -d '{\"songs\":[{\"source\":\"wy\",\"id\":\"186016\",\"name\":\"夜曲\",\"artist\":\"周杰伦\",\"duration\":227,\"url\":\"https://.../a.flac\"}]}'",
			Notes:    "歌单/收藏类无需上报：后端会用已登录账号自抓（v2.1.10 起）。上报仅用于榜单类或酷狗/酷我等只有浏览器能抓的来源，或想直接带 url 让后端下载时。",
			Requires: "browser",
		},
		{
			Method: http.MethodPost, Path: "/api/monitors/{id}/run", Summary: "手动执行一轮监控", Category: catMonitor,
			Params:  []Param{{Name: "id", In: "path", Required: true, Desc: "监控 ID"}},
			Example: "curl -X POST " + baseURL + "/api/monitors/mon_123/run",
			Notes:   "异步执行，立即返回；用 GET /api/monitor/runs 查看结果。",
		},
		{
			Method: http.MethodPost, Path: "/api/monitor/preview", Summary: "干跑：只做发现与过滤，不下载不写库", Category: catMonitor,
			Params: []Param{
				{Name: "monitor_id", In: "body", Desc: "已有监控 ID（二选一）"},
				{Name: "monitor", In: "body", Desc: "草稿配置（二选一），可先验证规则再创建"},
			},
			Example: "curl -X POST " + baseURL + "/api/monitor/preview -H 'Content-Type: application/json' -d '{\"monitor_id\":\"mon_123\"}'",
			Notes:   "返回通过过滤的曲目与被剔除的曲目及原因，是验证过滤规则最有效的工具。",
		},
		{
			Method: http.MethodGet, Path: "/api/monitor/tracks", Summary: "查询曲目记录", Category: catMonitor,
			Params: []Param{
				{Name: "monitor_id", In: "query", Desc: "按监控过滤"},
				{Name: "status", In: "query", Desc: "pending / downloaded / skipped / failed / missing"},
				{Name: "limit", In: "query", Desc: "默认 100，上限 500"},
				{Name: "offset", In: "query", Desc: "分页偏移"},
			},
			Example: "curl '" + baseURL + "/api/monitor/tracks?status=failed&limit=50'",
		},
		{
			Method: http.MethodPost, Path: "/api/monitor/tracks/mark", Summary: "回写曲目状态（网页端补下载完成后标记已下载）", Category: catMonitor,
			Params: []Param{
				{Name: "monitor_id", In: "body", Required: true, Desc: "监控 ID"},
				{Name: "status", In: "body", Desc: "downloaded（默认）/ pending / failed"},
				{Name: "items", In: "body", Required: true, Desc: "数组，每项 {source, song_id, file_path?}"},
			},
			Example: "curl -X POST " + baseURL + "/api/monitor/tracks/mark -H 'Content-Type: application/json' -d '{\"monitor_id\":\"mon_123\",\"items\":[{\"source\":\"wy\",\"song_id\":\"186016\"}]}'",
			Notes:   "后端自抓的曲目没有音源直链，只能登记为 pending；网页端「补下载」成功后调本接口回写，避免下一轮重复登记。",
		},
		{
			Method: http.MethodGet, Path: "/api/monitor/runs", Summary: "查询运行历史与日志", Category: catMonitor,
			Params: []Param{
				{Name: "monitor_id", In: "query", Desc: "按监控过滤"},
				{Name: "limit", In: "query", Desc: "默认 30，上限 200"},
			},
			Example: "curl '" + baseURL + "/api/monitor/runs?limit=10'",
		},
		{
			Method: http.MethodGet, Path: "/api/monitor/due", Summary: "查看当前到点的监控", Category: catMonitor,
			Example: "curl " + baseURL + "/api/monitor/due",
		},

		// ── 整理与去重 ──
		{
			Method: http.MethodPost, Path: "/api/tidy", Summary: "批量整理：补歌词 / 封面 / 元数据", Category: catTidy,
			Params: []Param{
				{Name: "items", In: "body", Desc: "数组，每项 {path,title,artist,album,lyric,cover_url}"},
				{Name: "paths", In: "body", Desc: "简化用法：只给路径数组"},
				{Name: "options", In: "body", Desc: "{embed_lyric,write_lrc,embed_cover,overwrite_lyric,overwrite_cover,write_metadata}"},
			},
			Example: "curl -X POST " + baseURL + "/api/tidy -H 'Content-Type: application/json' -d '{\"items\":[{\"path\":\"/vol1/Music/a.flac\",\"title\":\"夜曲\",\"artist\":\"周杰伦\",\"lyric\":\"[00:01.00]歌词\"}]}'",
			Notes: "仅支持 MP3 / FLAC；写入采用原子替换，失败不会损坏原文件；默认不覆盖已有歌词封面。\n\n" +
				"⚠️ 这是**被动**接口：歌词与封面要由调用方自己提供。网页端用户请用 " +
				"`POST /api/library/complete`（会自动去平台搜索并取回歌词/封面）。",
		},
		{
			Method: http.MethodPost, Path: "/api/library/complete", Summary: "按字段补全曲库（歌词/封面/风格/年份/曲序/光盘，异步任务）", Category: catTidy,
			Params: []Param{
				{Name: "dir", In: "body", Required: true, Desc: "曲库目录"},
				{Name: "limit", In: "body", Desc: "本次最多处理几首，默认 30，上限 200"},
				{Name: "fields", In: "body", Desc: "要补哪些字段：lyric / cover / genre / year / track / disc，或 all；**不传则只补 lyric+cover**（老口径）"},
				{Name: "only", In: "body", Desc: "老参数：lyric / cover。仅当 fields 缺失时生效"},
				{Name: "platforms", In: "body", Desc: "搜索平台顺序，默认 [wy,tx,kg,wu]"},
				{Name: "overwrite", In: "body", Desc: "已有歌词/封面是否覆盖，默认 false"},
				{Name: "use_ai_genre", In: "body", Desc: "风格拿不到时是否允许 AI 推断（默认不允许，AI 只兜底）"},
				{Name: "dry_run", In: "body", Desc: "**只出预览、不写文件**，返回 plan_id 与逐条对照"},
				{Name: "plan_id", In: "body", Desc: "执行某份已确认的计划（由 dry_run 返回）"},
			},
			Example: "curl -X POST " + baseURL + "/api/library/complete -H 'Content-Type: application/json' -d '{\"dir\":\"/vol1/Music\",\"limit\":30,\"fields\":[\"year\",\"track\",\"genre\"]}'",
			Notes: "**/api/tidy 的主动版**：自己挑出缺字段的曲目、去平台搜索、把结果写进文件。" +
				"值只来自**匹配上的那条命中**，匹配不上就跳过；未勾选的字段不改写。\n\n" +
				"推荐先 `dry_run` 拿 `plan_id` 再执行；写前自动备份，可 undo；" +
				"跑太久可 `POST /api/library/complete/cancel` 叫停；写不了标签的格式在 `unsupported` 里单列。",
		},
		{
			Method: http.MethodGet, Path: "/api/library/complete", Summary: "读补全任务的进度与结果", Category: catTidy,
			Example: "curl '" + baseURL + "/api/library/complete'",
			Notes: "返回**最近一次**补全任务：`running` / `done` / `total` / `current` 与完成后的 " +
				"`summary`（匹配数、补上的字段数、逐条结果）。\n\n" +
				"`cancelled:true` = 这次被 `/api/library/complete/cancel` 停了；" +
				"`summary.cancelled` 是**没处理的曲目数**（不是失败）。只保留最近一次。",
		},
		{
			Method: http.MethodPost, Path: "/api/library/complete/cancel", Summary: "停止正在跑的补全任务（不再派发新曲目）", Category: catTidy,
			Example: "curl -X POST " + baseURL + "/api/library/complete/cancel",
			Notes: "补全一批最多 200 首、每首都要打外网并改写文件，跑起来是分钟级；这个接口是唯一的叫停方式。\n\n" +
				"停止 = **不再开始新的曲目**：已写入的保留（可用 `/api/library/complete/undo` 回退），" +
				"正在写的那一首会写完（半途中断会留下半个标签块）。\n" +
				"没在跑时如实返回「没有正在跑的补全任务」，不假装成功。",
		},
		{
			Method: http.MethodPost, Path: "/api/library/complete/undo", Summary: "撤销最近一次补全（把改过的文件还原）", Category: catTidy,
			Example: "curl -X POST " + baseURL + "/api/library/complete/undo",
			Notes: "把上一次补全改过的文件还原成补全前的样子（改写前都在 `<dataDir>/ai_backup/<时间戳>/` 留了备份）。\n\n" +
				"**只做一次、只还原最近一次**：还原成功就删掉备份目录，没有「重做」；有文件失败则备份保留、可重试。" +
				"只会删**本次新建**的 `.lrc` / `.jpg`，用户本来就有的同名文件不碰。" +
				"还原后曲库索引会按实际文件重判，不需要你再扫一遍。",
		},
		{
			Method: http.MethodDelete, Path: "/api/library/complete/undo", Summary: "丢弃补全备份（不还原，只释放空间）", Category: catTidy,
			Example: "curl -X DELETE " + baseURL + "/api/library/complete/undo",
			Notes:   "确认不需要还原了，只删备份目录释放磁盘。**不会改任何音频文件。**",
		},
		{
			Method: http.MethodGet, Path: "/api/library/name-rules", Summary: "列出用户自定义的命名解析规则", Category: catTidy,
			Example: "curl '" + baseURL + "/api/library/name-rules'",
			Notes: "命名解析规则用于把「歌手 - 歌名.mp3」这类文件名拆成结构化字段。" +
				"内置启发式只覆盖常见命名，遇到用户特有的命名习惯（`【无损】Jay_Chou_晴天_2003`）" +
				"可以自定义一条正则。\n\n" +
				"**用户规则优先于内置启发式**。返回里带 `allowed_fields`，说明正则可用的命名分组。",
		},
		{
			Method: http.MethodPost, Path: "/api/library/name-rules", Summary: "生成 / 校验 / 保存命名解析规则", Category: catTidy,
			Params: []Param{
				{Name: "sample", In: "body", Desc: "文件名样例"},
				{Name: "desc", In: "body", Desc: "对该命名方式的一句解释（生成模式用）"},
				{Name: "regex", In: "body", Desc: "要保存的正则；给了它就进入校验/保存模式"},
				{Name: "dry_run", In: "body", Desc: "只校验不保存"},
				{Name: "id", In: "body", Desc: "更新已有规则时传"},
				{Name: "disabled", In: "body", Desc: "停用该规则"},
			},
			Example: "curl -X POST " + baseURL + "/api/library/name-rules -H 'Content-Type: application/json' " +
				"-d '{\"sample\":\"【无损】Jay_Chou_晴天_2003.mp3\",\"desc\":\"方括号是音质标记，下划线分隔歌手和歌名\"}'",
			Notes: "**三种用法**，靠 body 里有没有 `regex` 区分：① `{sample, desc}` 让 AI 生成正则，" +
				"**只返回建议不保存**（`valid:false` = 没通过回测）；② `{regex, sample, dry_run:true}` 只回测；" +
				"③ `{regex, sample, ...}` 校验通过才落盘。校验不过会返回中文原因。",
		},
		{
			Method: http.MethodDelete, Path: "/api/library/name-rules", Summary: "删除一条命名解析规则", Category: catTidy,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "规则 ID"}},
			Example: "curl -X DELETE '" + baseURL + "/api/library/name-rules?id=rule_xxx'",
		},
		{
			Method: http.MethodGet, Path: "/api/library/cover-candidates", Summary: "搜封面候选图（手动换封面用）", Category: catTidy,
			Params: []Param{
				{Name: "keyword", In: "query", Desc: "搜索词（优先；不传则用 name + artist 拼）"},
				{Name: "name", In: "query", Desc: "歌曲名（与 artist 二选一组合，keyword 为空时使用）"},
				{Name: "artist", In: "query", Desc: "歌手（带上更准）"},
				{Name: "limit", In: "query", Desc: "最多返回几张，默认 8，上限 20"},
			},
			Example: "curl '" + baseURL + "/api/library/cover-candidates?name=夜曲&artist=周杰伦'",
			Notes: "多平台搜索后按封面地址去重，只返回候选、不下载图片。" +
				"选定后 POST /api/nas/tags 携带 cover（图片地址）即可内嵌；只有 MP3 / FLAC 支持内嵌封面。",
		},
		{
			Method: http.MethodGet, Path: "/api/library/audit", Summary: "曲库体检：统计缺歌词/缺封面的曲目", Category: catTidy,
			Params: []Param{
				{Name: "dir", In: "query", Required: true, Desc: "曲库目录"},
				{Name: "sample", In: "query", Desc: "缺失样本数量，默认 50"},
			},
			Example: "curl '" + baseURL + "/api/library/audit?dir=/vol1/Music'",
			Notes:   "只读操作；返回 missing_sample 可直接喂给 POST /api/tidy 批量补齐。",
		},
		{
			Method: http.MethodGet, Path: "/api/library/index", Summary: "曲库增量扫描（返回相对上次的增删改）", Category: catTidy,
			Params:  []Param{{Name: "dir", In: "query", Required: true, Desc: "曲库目录"}},
			Example: "curl '" + baseURL + "/api/library/index?dir=/vol1/Music'",
			Notes: "大曲库友好：只对比 (size,mtime)，仅新增/变化的文件需要读标签。" +
				"枚举不完整时（权限失败/深度截断/数量上限）会跳过删除判定并告警，避免误清索引。",
		},
		{
			Method: http.MethodGet, Path: "/api/library/index/list", Summary: "查看曲库索引内容", Category: catTidy,
			Params:  []Param{{Name: "limit", In: "query", Desc: "默认 100，上限 2000"}},
			Example: "curl '" + baseURL + "/api/library/index/list?limit=20'",
		},
		{
			Method: http.MethodGet, Path: "/api/library/index/stats", Summary: "曲库管家：读取上次整理快照（持久化索引概况）", Category: catTidy,
			Params:  []Param{{Name: "dir", In: "query", Required: true, Desc: "曲库目录"}},
			Example: "curl '" + baseURL + "/api/library/index/stats?dir=/vol1/Music'",
			Notes: "读取持久化的 library_index.json 统计：已索引/已有歌词/缺歌词/缺封面。" +
				"歌词与封面判定与 NAS 扫描同口径（内嵌 + 同名外挂文件，不认目录级封面）。" +
				"未富化过的记录会算入 missing，需先跑一次 /api/library/index 才会落标签数据。",
		},
		{
			Method: http.MethodGet, Path: "/api/library/gaps", Summary: "曲库管家：各字段缺口统计（补全前先看缺什么）", Category: catTidy,
			Params:  []Param{{Name: "dir", In: "query", Required: true, Desc: "曲库目录"}},
			Example: "curl '" + baseURL + "/api/library/gaps?dir=/vol1/Music'",
			Notes: "只读索引、不开文件，所以秒回。返回 `{dir,unread,gaps{...},writable_formats}`；gaps 里 `unsupported`/`unsupported_exts` 是**写不了标签的格式**（只有 MP3/FLAC 能写），不算缺口。\n\n" +
				"`unread`=**还没读过标签**的条数，先跑 `GET /api/library/index` 再看这里才准。",
		},
		{
			Method: http.MethodGet, Path: "/api/duplicates", Summary: "查找重复曲目", Category: catTidy,
			Params: []Param{
				{Name: "dir", In: "query", Required: true, Desc: "曲库目录"},
				{Name: "name", In: "query", Desc: "传 0 关闭「同名同歌手」通道，只查内容完全相同"},
			},
			Example: "curl '" + baseURL + "/api/duplicates?dir=/vol1/Music'",
			Notes: "双通道：content（SHA-1 相同）与 name（同名同歌手但内容不同）。" +
				"保留优先级：无损 > 已整理（有词有封面）> 体积更大。",
		},
		{
			Method: http.MethodPost, Path: "/api/duplicates/resolve", Summary: "执行去重（默认移入回收站）", Category: catTidy,
			Params: []Param{
				{Name: "dir", In: "body", Desc: "曲库目录（自动查找重复）"},
				{Name: "groups", In: "body", Desc: "也可直接传入待处理的重复组"},
				{Name: "mode", In: "body", Desc: "trash（默认，可恢复）/ delete"},
				{Name: "dry_run", In: "body", Desc: "true 只估算不执行"},
			},
			Example: "curl -X POST " + baseURL + "/api/duplicates/resolve -H 'Content-Type: application/json' -d '{\"dir\":\"/vol1/Music\",\"mode\":\"trash\",\"dry_run\":true}'",
			Notes:   "默认移入 .tidy_trash 而不是直接删除，误判可恢复；同名歌词与封面会一起搬走。",
		},

		// ── AI 大模型 ──
		{
			Method: http.MethodGet, Path: "/api/ai/config", Summary: "读取 AI 配置（密钥已脱敏）", Category: catAI,
			Example: "curl " + baseURL + "/api/ai/config",
		},
		{
			Method: http.MethodPost, Path: "/api/ai/config", Summary: "配置 AI（OpenAI 兼容接口）", Category: catAI,
			Params: []Param{
				{Name: "enabled", In: "body", Desc: "是否启用"},
				{Name: "base_url", In: "body", Desc: "接口地址，如 https://api.deepseek.com"},
				{Name: "api_key", In: "body", Desc: "密钥；传空或含 **** 表示沿用旧值"},
				{Name: "model", In: "body", Desc: "模型名"},
				{Name: "timeout", In: "body", Desc: "超时秒数，默认 20"},
			},
			Example: "curl -X POST " + baseURL + "/api/ai/config -H 'Content-Type: application/json' -d '{\"enabled\":true,\"base_url\":\"https://api.deepseek.com\",\"api_key\":\"sk-xxx\",\"model\":\"deepseek-chat\"}'",
			Notes:   "兼容 /chat/completions 与 /v1/chat/completions 两种 base 形式；密钥落盘权限为 0600。",
		},
		{
			Method: http.MethodGet, Path: "/api/ai/usage", Summary: "AI 用量统计（按用途分类）", Category: catAI,
			Params:  []Param{{Name: "recent", In: "query", Desc: "最近记录条数，默认 20"}},
			Example: "curl " + baseURL + "/api/ai/usage",
			Notes:   "上游未返回 usage 明细时也会单独计入 missing_usage，避免漏记调用。",
		},
		{
			Method: http.MethodPost, Path: "/api/ai/usage/reset", Summary: "清空 AI 用量统计", Category: catAI,
			Example: "curl -X POST " + baseURL + "/api/ai/usage/reset",
			Notes:   "只清计数，不动模型配置；只认 POST。",
		},
		{
			Method: http.MethodPost, Path: "/api/ai/test", Summary: "AI 连通性测试", Category: catAI,
			Example: "curl -X POST " + baseURL + "/api/ai/test",
		},
		{
			Method: http.MethodPost, Path: "/api/ai/complete", Summary: "AI 生成 / 文件名语义解析", Category: catAI,
			Params: []Param{
				{Name: "kind", In: "body", Desc: "name_parse 走结构化解析；留空为自由生成"},
				{Name: "filename", In: "body", Desc: "kind=name_parse 时必填"},
				{Name: "prompt", In: "body", Desc: "自由生成时的用户要求"},
				{Name: "content", In: "body", Desc: "自由生成时的输入内容"},
			},
			Example: "curl -X POST " + baseURL + "/api/ai/complete -H 'Content-Type: application/json' -d '{\"kind\":\"name_parse\",\"filename\":\"【无损】周杰伦 - 晴天.flac\"}'",
			Notes:   "未配置 AI 时静默降级返回空结果，不会报错影响主流程。",
		},

		// ── 飞牛集成 ──
		{
			Method: http.MethodGet, Path: "/api/fnos/status", Summary: "飞牛集成状态（开放 API / 音乐接口可用性）", Category: catFnos,
			Example: "curl " + baseURL + "/api/fnos/status",
			Notes:   "非飞牛环境（如本地开发）两个 available 均为 false，属正常，其余功能不受影响。",
		},
		{
			Method: http.MethodGet, Path: "/api/fnos/folders", Summary: "管理员授权给本应用的目录（官方开放 API）", Category: catFnos,
			Example: "curl " + baseURL + "/api/fnos/folders",
			Notes:   "需应用声明 api-scope: trim.file.sharedAccess，且系统版本 >= 1.2.0401。",
		},
		{
			Method: http.MethodPost, Path: "/api/fnos/rescan", Summary: "触发飞牛音乐曲库重扫", Category: catFnos,
			Example: "curl -X POST " + baseURL + "/api/fnos/rescan",
			Notes: "飞牛音乐**不会自己发现**音乐目录里的新文件，它的曲库索引靠 scan-all 刷新。" +
				"下载与整理完成后会自动触发，这个接口用于**手动**触发" +
				"（例如你从别处拷了歌进来）。" +
				"⚠️ scan-all 是**全量扫描**，所以走合并调度器：约 30 秒 debounce + " +
				"两次实际扫描至少间隔 3 分钟；接口**立即返回**，不等待扫描完成。",
			Requires: "fnos",
		},
		{
			Method: http.MethodPost, Path: "/api/fnos/check", Summary: "真实探一次飞牛音乐连接（调 /user/me）", Category: catFnos,
			Example: "curl -X POST " + baseURL + "/api/fnos/check",
			Notes: "与 GET /api/fnos/status 的区别：status 是「看状态」（读本地文件/环境即可），" +
				"这个会**真的调一次飞牛接口**，用来回答「现在到底通不通」。" +
				"返回里带 token_source（manual / env / db / none），便于排障时确认用的是哪个令牌。",
			Requires: "fnos",
		},
		{
			Method: http.MethodPost, Path: "/api/fnos/token", Summary: "保存 / 清除手工飞牛令牌（故障排查）", Category: catFnos,
			Params: []Param{
				{Name: "token", In: "body", Desc: "飞牛音乐令牌；传空串表示清除、回到自动读取"},
			},
			Example: "curl -X POST " + baseURL + "/api/fnos/token -H 'Content-Type: application/json' -d '{\"token\":\"xxx\"}'",
			Notes: "优先级：**手工令牌 > 环境变量 FNOS_TOKEN > 飞牛数据库自动读取**。" +
				"手工排最前，因为它的用途就是「自动读不到时先顶上」。" +
				"⚠️ 通常**不需要**用它：令牌每次调用都从飞牛数据库现读，" +
				"飞牛自己刷新后我们自动跟随，**天然就是自动续期**。保存后立即生效，不用重启。",
			Requires: "fnos",
		},
		{
			Method: http.MethodGet, Path: "/api/fnos/playlists", Summary: "飞牛音乐的歌单列表", Category: catFnos,
			Example: "curl " + baseURL + "/api/fnos/playlists",
			Notes:   "令牌取自飞牛音乐本地数据库，也可用环境变量 FNOS_TOKEN 覆盖。",
		},
		{
			Method: http.MethodPost, Path: "/api/fnos/push", Summary: "把曲目推送为飞牛音乐歌单", Category: catFnos,
			Params: []Param{
				{Name: "title", In: "body", Required: true, Desc: "歌单名称"},
				{Name: "tracks", In: "body", Required: true, Desc: "待推送曲目数组 [{name,artist,album,duration}]"},
				{Name: "description", In: "body", Desc: "歌单描述"},
				{Name: "cover_url", In: "body", Desc: "封面图 URL，配合 sync_cover=true 使用"},
				{Name: "fnos_guid", In: "body", Desc: "已推送歌单的 guid，传入后复用之（避免重名建新单）"},
				{Name: "sync_cover", In: "body", Desc: "是否同步封面，默认 false"},
				{Name: "dry_run", In: "body", Desc: "true 时只返回匹配结果，不写入歌单"},
			},
			Example: "curl -X POST " + baseURL + "/api/fnos/push -H 'Content-Type: application/json' -d '{\"title\":\"我的歌单\",\"dry_run\":true,\"tracks\":[{\"name\":\"晴天\",\"artist\":\"周杰伦\"}]}'",
			Notes:   "在飞牛曲库中按歌名与「歌手+歌名」两次检索，相似度低于阈值视为未命中，不写入。建议先用 dry_run 预览匹配情况。",
		},

		// ── 账号连接 ──
		{
			Method: http.MethodGet, Path: "/api/accounts", Summary: "列出已连接的音乐平台账号（不含 Cookie）", Category: catAccount,
			Example: "curl " + baseURL + "/api/accounts",
			Notes:   "凭据只保存在本机（文件权限 0600），接口不会返回 Cookie 本身。",
		},
		{
			Method: http.MethodDelete, Path: "/api/accounts", Summary: "断开某个平台的账号", Category: catAccount,
			Params:  []Param{{Name: "provider", In: "query", Required: true, Desc: "平台标识，如 netease"}},
			Example: "curl -X DELETE '" + baseURL + "/api/accounts?provider=netease'",
		},
		{
			Method: http.MethodPost, Path: "/api/accounts/netease/qr", Summary: "申请网易云扫码登录二维码", Category: catAccount,
			Example: "curl -X POST " + baseURL + "/api/accounts/netease/qr",
			Notes:   "返回 {key, url}：url 为二维码承载内容，前端据此生成二维码图片。随后轮询下面的 check 接口。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/netease/qr/check", Summary: "轮询扫码状态（成功后自动保存账号）", Category: catAccount,
			Params:  []Param{{Name: "key", In: "query", Required: true, Desc: "上一步返回的 key"}},
			Example: "curl '" + baseURL + "/api/accounts/netease/qr/check?key=xxx'",
			Notes: "建议每 2 秒轮询一次。code: 800 二维码过期 / 801 等待扫码 / 802 已扫码待确认 / 803 授权成功。" +
				"成功时会自动校验凭据并落盘，返回 saved=true 与昵称头像（不回显 Cookie）。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/tracks", Summary: "拉取账号某个来源（日推/歌单）的曲目，已归一化为可播放形状", Category: catAccount,
			Params: []Param{
				{Name: "provider", In: "query", Desc: "netease（默认）/ qq"},
				{Name: "kind", In: "query", Desc: "daily（默认）/ playlist"},
				{Name: "id", In: "query", Desc: "kind=playlist 时必填，歌单 ID"},
				{Name: "limit", In: "query", Desc: "返回条数上限，默认 200，最大 1000"},
			},
			Example: "curl '" + baseURL + "/api/accounts/tracks?provider=netease&kind=daily'",
			Notes: "与 /api/accounts/{provider}/daily 的区别：那个返回平台**原始**对象，" +
				"这个返回带 source/songmid 的**归一化**曲目，可直接交给播放器或下载队列" +
				"（与「推送同步」同一套字段映射）。音质档位不在这里查，下载时走 /api/music/qualities。",
			Requires: "none",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/netease/daily", Summary: "网易云每日推荐（需先扫码登录）", Category: catAccount,
			Example: "curl " + baseURL + "/api/accounts/netease/daily",
			Notes:   "未登录时返回 503 并提示先扫码。返回歌曲结构与 /api/search 的 UnifiedSong 不同，为网易原始字段。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/netease/playlists", Summary: "账号的歌单列表（需先扫码登录）", Category: catAccount,
			Params: []Param{
				{Name: "uid", In: "query", Desc: "目标用户 uid，默认当前登录账号"},
				{Name: "limit", In: "query", Desc: "数量，默认 100，上限 1000"},
			},
			Example: "curl " + baseURL + "/api/accounts/netease/playlists",
		},
		{
			Method: http.MethodPost, Path: "/api/accounts/qq/qr", Summary: "申请 QQ 音乐扫码登录二维码", Category: catAccount,
			Example: "curl -X POST " + baseURL + "/api/accounts/qq/qr",
			Notes:   "直接返回可用的 data URL 图片（上游本就返回图片字节），前端无需自行生成二维码。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/qq/qr/check", Summary: "轮询 QQ 扫码状态（成功后自动保存账号）", Category: catAccount,
			Params:  []Param{{Name: "key", In: "query", Required: true, Desc: "上一步返回的 key（qrsig）"}},
			Example: "curl '" + baseURL + "/api/accounts/qq/qr/check?key=xxx'",
			Notes: "建议每 2 秒轮询一次。state: waiting 待扫 / scanned 已扫（返回昵称）/ expired 过期 / success 成功。" +
				"成功后自动完成「换 p_skey → OAuth 取 code → 换音乐凭据」并落盘，返回 saved=true。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/qq/daily", Summary: "QQ 音乐每日推荐（需先扫码登录）", Category: catAccount,
			Example: "curl " + baseURL + "/api/accounts/qq/daily",
			Notes:   "走网页版网关（music.srfDissInfo.DissInfo），需账号已登录；未登录返回 503。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/qq/playlists", Summary: "QQ 音乐歌单列表（自建 + 收藏）", Category: catAccount,
			Example: "curl " + baseURL + "/api/accounts/qq/playlists",
			Notes:   "kind=created 为自建歌单，kind=collected 为收藏歌单。单侧接口失败不影响另一侧返回。",
		},
		{
			Method: http.MethodGet, Path: "/api/accounts/qq/playlist", Summary: "QQ 音乐歌单详情（含曲目）", Category: catAccount,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "歌单 ID（来自 /api/accounts/qq/playlists）"}},
			Example: "curl '" + baseURL + "/api/accounts/qq/playlist?id=xxx'",
			Notes:   "返回 items 为统一结构的曲目数组（song_id/title/artist/album/duration_ms/cover_url）。",
		},

		// ── 推送同步 ──
		{
			Method: http.MethodGet, Path: "/api/push/sources", Summary: "列出可作为推送来源的账号歌单（含日推）", Category: catPush,
			Params:  []Param{{Name: "provider", In: "query", Required: true, Desc: "netease / qq"}},
			Example: "curl '" + baseURL + "/api/push/sources?provider=netease'",
			Notes:   "返回的 kind 与 id 可直接填入 POST /api/push/tasks 建立同步任务。",
		},
		{
			Method: http.MethodGet, Path: "/api/push/tasks", Summary: "列出全部推送任务", Category: catPush,
			Example: "curl " + baseURL + "/api/push/tasks",
			Notes:   "interval_minutes>0 且 enabled=true 的任务由后端定时执行（每分钟检查一次）。",
		},
		{
			Method: http.MethodPost, Path: "/api/push/tasks", Summary: "新建 / 更新推送任务", Category: catPush,
			Params: []Param{
				{Name: "id", In: "body", Desc: "传入则更新已有任务，留空为新建"},
				{Name: "name", In: "body", Required: true, Desc: "任务名称"},
				{Name: "provider", In: "body", Required: true, Desc: "netease / qq"},
				{Name: "source_kind", In: "body", Required: true, Desc: "daily（日推）/ playlist（歌单）"},
				{Name: "source_id", In: "body", Desc: "歌单 ID（source_kind=playlist 时必填）"},
				{Name: "target_title", In: "body", Desc: "目标飞牛歌单名，默认同任务名"},
				{Name: "fnos_guid", In: "body", Desc: "已推送歌单的 guid，传入后复用之（避免重名建新单）"},
				{Name: "sync_cover", In: "body", Desc: "是否同步封面"},
				{Name: "interval_minutes", In: "body", Desc: "定时间隔（分钟），0 表示只手动执行"},
				{Name: "enabled", In: "body", Desc: "是否启用，新建默认 true"},
				{Name: "retention_mode", In: "body", Desc: "保留期：keep（默认，永久保留）/ days（按天清理）"},
				{Name: "retention_days", In: "body", Desc: "retention_mode=days 时生效，超过该天数的曲目自动从歌单移除"},
			},
			Example: "curl -X POST " + baseURL + "/api/push/tasks -H 'Content-Type: application/json' -d '{\"name\":\"网易日推\",\"provider\":\"netease\",\"source_kind\":\"daily\",\"target_title\":\"网易日推\",\"interval_minutes\":1440,\"retention_mode\":\"days\",\"retention_days\":7}'",
			Notes: "这是「订阅」模型：只需指定来源与目标，后端自己拉取曲目、在飞牛曲库中匹配、写入歌单。" +
				"首次执行成功后会自动记住飞牛歌单 guid，后续复用，避免重名建出新歌单。" +
				"**保留期**适合「每日推荐」这类天天变的内容：不清理的话歌单会一直膨胀。" +
				"清理**只针对本任务推送过的曲目**（后端持久化了一份推送清单），" +
				"用户手动加进同一歌单的歌不会被删；执行结果里的 expired 字段报告本轮清掉了几首。",
		},
		{
			Method: http.MethodDelete, Path: "/api/push/tasks", Summary: "删除推送任务", Category: catPush,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "任务 ID"}},
			Example: "curl -X DELETE '" + baseURL + "/api/push/tasks?id=push_xxx'",
		},
		{
			Method: http.MethodPost, Path: "/api/push/run", Summary: "立即执行一个推送任务", Category: catPush,
			Params:  []Param{{Name: "id", In: "query", Required: true, Desc: "任务 ID"}},
			Example: "curl -X POST '" + baseURL + "/api/push/run?id=push_xxx'",
			Notes:   "同步执行并返回结果；无论成败都会写入执行历史（见 /api/push/runs）。",
		},
		{
			Method: http.MethodPost, Path: "/api/push/preview", Summary: "干跑预览：拉取来源并匹配，但不写入", Category: catPush,
			Params: []Param{
				{Name: "task_id", In: "body", Desc: "已有任务 ID（二选一）"},
				{Name: "provider", In: "body", Desc: "临时配置：netease / qq（二选一）"},
				{Name: "source_kind", In: "body", Desc: "临时配置：daily / playlist"},
				{Name: "source_id", In: "body", Desc: "临时配置：歌单 ID"},
			},
			Example: "curl -X POST " + baseURL + "/api/push/preview -H 'Content-Type: application/json' -d '{\"provider\":\"netease\",\"source_kind\":\"daily\"}'",
			Notes:   "建立任务前先用它确认匹配情况，避免推送一堆匹配不上的曲目。",
		},
		{
			Method: http.MethodGet, Path: "/api/push/runs", Summary: "查询推送执行历史", Category: catPush,
			Params: []Param{
				{Name: "task_id", In: "query", Desc: "按任务过滤，留空为全部"},
				{Name: "limit", In: "query", Desc: "默认 50，上限 200"},
			},
			Example: "curl '" + baseURL + "/api/push/runs?limit=20'",
			Notes:   "记录每次执行的状态、耗时、匹配与写入数量，便于排查。最多保留 200 条。",
		},

		// ── 网络代理 ──
		{
			Method: http.MethodPost, Path: "/api/proxy/http", Summary: "服务端 HTTP 代理（供音源脚本跨域取数）", Category: catProxy,
			Params: []Param{
				{Name: "url", In: "body", Required: true, Desc: "目标 URL（仅公网 http/https）"},
				{Name: "method", In: "body", Desc: "GET / POST / HEAD"},
				{Name: "headers", In: "body", Desc: "请求头对象"},
				{Name: "body", In: "body", Desc: "请求体"},
			},
			Example: "curl -X POST " + baseURL + "/api/proxy/http -H 'Content-Type: application/json' -d '{\"url\":\"https://example.com/api\",\"method\":\"GET\"}'",
			Notes:   "已启用 SSRF 防护：仅允许公网地址，禁止访问内网/回环/云元数据地址。",
		},

		// ── 外挂音源（musicdl sidecar）──
		{
			Method: http.MethodGet, Path: "/api/musicdl/status", Summary: "musicdl sidecar 状态（外挂音源进程）", Category: catMusicDL,
			Example: "curl " + baseURL + "/api/musicdl/status",
			Notes: "state: off / preparing（建 venv 装依赖，首次约 1-2 分钟）/ starting / " +
				"ready / failed（原因见 error）/ stopped。打开后多出咪咕 / 千千 / B站 三个平台" +
				"（酷狗 / 酷我 可选）。开关 musicdl_enabled 默认关；sidecar 掉线时曲率照常工作。",
		},

		{
			Method: http.MethodGet, Path: "/api/musicdl/sources", Summary: "musicdl 注册的全部音源与启用状态", Category: catMusicDL,
			Example: "curl " + baseURL + "/api/musicdl/sources",
			Notes: "registered 是 musicdl 报出的源总数（真机 56 个）；sources 每项含 " +
				"id（曲率短码）/ label / client / enabled / native。" +
				"native=true 表示与曲率原生平台重叠（wy/tx/kg/kw）—— 启用它会**换掉原生实现**。" +
				"外挂音源没开时返回 code 502。",
		},
		{
			Method: http.MethodPost, Path: "/api/musicdl/restart", Summary: "重试启动外挂进程（sidecar 挂掉后不用重启应用）", Category: catMusicDL,
			Example: "curl -X POST " + baseURL + "/api/musicdl/restart",
			Notes:   "拿当前配置再 Apply 一次：配置没变时不会重建，只把挂掉的进程重新拉起。sidecar 会因为瞬时原因挂掉（被 OOM 杀掉、装依赖时断网、端口被占），界面此前只能显示「启动失败」而没有重试入口。",
		},
		{
			Method: http.MethodPost, Path: "/api/musicdl/probe", Summary: "逐个源做一次真实搜索（耗时 + 条数）", Category: catMusicDL,
			Example: "curl -X POST " + baseURL + "/api/musicdl/probe -H 'Content-Type: application/json' -d '{\"sources\":[\"mg\"]}'",
			Notes: "「注册了」不代表「能用」—— 56 个源里一大半在国内出不了结果。" +
				"body 可省略（用当前启用的源 + 默认关键词）。耗时秒级到十几秒（单源预算），" +
				"建议只测几个或分批。",
		},

		// ── 音乐入口接管 ──
		{
			Method: http.MethodGet, Path: "/api/takeover/status", Summary: "音乐入口接管状态（谁在监听官方 socket）", Category: catTakeover,
			Example: "curl " + baseURL + "/api/takeover/status",
			Notes: "target/upstream 各自形态：official（官方在听）/ ours（本应用接管中）/ " +
				"other-proxy（别的扩展在代理）/ stale（文件在但没人听）/ absent / unknown。" +
				"默认关闭，.env 里设 QULV_TAKEOVER=1 开启；别的扩展已接管时自动让路，" +
				"此时多返回 yielded 字段说明原因。",
			Requires: "none",
		},
		{
			Method: http.MethodPost, Path: "/api/takeover/restore", Summary: "还原官方直连（把 socket 还给飞牛音乐）", Category: catTakeover,
			Example: "curl -X POST " + baseURL + "/api/takeover/restore",
			Notes: "把官方 socket 挪回原位并停止接管。上游不是官方时**拒绝执行**（409），" +
				"以免把别人的代理当成官方挪回去、导致官方音乐彻底打不开。停止应用时也会自动还原。",
			Requires: "none",
		},

		// ── 播放与直链校验 ──
		{
			Method: http.MethodGet, Path: "/api/player/check", Summary: "探一条直链是否真的可取（只拉 2 字节）", Category: catDownload,
			Params: []Param{
				{Name: "url", In: "query", Required: true, Desc: "待验证的直链"},
				{Name: "referer", In: "query", Desc: "防盗链需要的 Referer"},
			},
			Example: "curl '" + baseURL + "/api/player/check?url=https://...'",
			Notes:   "返回 {ok,status,content_type,note}。平台常返回 200 但内容是错误页，下载前先过这一道更稳。",
		},
		{
			Method: http.MethodGet, Path: "/api/player/stream", Summary: "把外部直链转成可播音频流（界面播放用）", Category: catDownload,
			Params: []Param{
				{Name: "url", In: "query", Required: true, Desc: "直链地址"},
				{Name: "referer", In: "query", Desc: "防盗链 Referer"},
			},
			Example: "curl -o out.mp3 '" + baseURL + "/api/player/stream?url=https://...'",
			Notes:   "播放走这条；要把歌**存进曲库**请走 POST /api/download/song（带标签内嵌与去重）。",
		},
	}

	// role 只由 internalEndpoints 名单推导；hidden 跟着 role 走，
	// 保证「抽屉里看到的」和「AI 按 role 分流的」是同一份判断。
	for i := range list {
		key := list[i].Method + " " + list[i].Path
		if internalEndpoints[key] {
			list[i].Role = roleInternal
			list[i].Hidden = true
		} else {
			list[i].Role = roleCore
		}
	}
	return list
}

// CatalogGuide 是「先说清这是干嘛的、该怎么做」那段。
//
// 目录的主要读者是替用户操作本应用的外部 AI，所以按**任务**组织：
// 每条食谱给出步骤串与用到的接口，最容易失败的那一步单独写在 watch_out 里。
func CatalogGuide() Guide {
	return Guide{
		What: []string{
			"曲率 是装在飞牛 NAS 上的音乐播放器 + 曲库管家。对 AI 来说它是执行后端：" +
				"你说「把这首下了」「这个歌单订上，以后新歌自动下」「曲库缺封面的补一下」，它做完并回报进度。",
			"能力面：多平台搜索与榜单歌单、下载落盘到 NAS 曲库、订阅增量发现、" +
				"曲库补全（歌词/封面/年份/曲序/光盘/风格）、查重整理、同步进飞牛音乐歌单、平台账号连接。",
			"先读 workflows 决定串哪几条接口，再按需查 endpoints 细节；不要把 100 多条全读一遍。",
		},
		Prereq: []string{
			"⚠️ **在线取直链只能在开着的应用页面里完成**（第三方音源脚本跑在浏览器）。" +
				"POST /api/bridge/resolve 之后若没人开着页面，任务会一直 pending —— 这不是接口坏了。",
			"例外：配了「按服务地址」的 API 音源时后端能自己取链，不需要人开着页面。" +
				"先 GET /api/protocols 看各协议的能力与就绪状态，再决定走哪条路。",
			"要往曲库目录读写文件，目录必须在应用允许的根目录内（环境变量 FN_ALLOWED_ROOTS），否则一律 403。" +
				"可写的曲库根目录用 GET /api/nas/folders 看。",
			"拉账号歌单/日推要先扫码连上平台账号；二维码必须由用户手机扫，AI 自己过不去这一步。",
			"设了 FN_API_TOKEN 时，除 /api/health、/api/catalog、/api/app/version 外都要带 X-API-Token。",
		},
		Workflows: []Workflow{
			{
				Task:      "下载一首指定的歌到 NAS 曲库",
				Summary:   "搜索 → 取直链 → 验链 → 落盘 → 确认",
				Steps:     []string{"1. GET /api/search 找到曲目，记下平台与 id/songmid", "2. 需要特定音质时 GET /api/music/qualities", "3. POST /api/bridge/resolve 提交解析任务，再 GET /api/bridge/jobs/{job_id} 轮询到出结果", "4. GET /api/player/check 确认直链真能取（平台常返回错误页）", "5. POST /api/download/song 落盘，会顺带内嵌歌词与封面", "6. POST /api/download/check 复查是否已在库里"},
				Endpoints: []string{"GET /api/search", "GET /api/music/qualities", "POST /api/bridge/resolve", "GET /api/bridge/jobs/{job_id}", "GET /api/player/check", "POST /api/download/song", "POST /api/download/check"},
				WatchOut:  "第 3 步依赖开着的应用页面（见 prereq）。已经下过的歌直接跳第 5 步会秒回 already_exists。",
			},
			{
				Task:      "批量下载 / 一整个歌单",
				Summary:   "先列清单，再交给服务端并发队列，别自己一首首调",
				Steps:     []string{"1. GET /api/charts/playlist/detail 或 /api/charts/detail 拿到曲目数组", "2. POST /api/download/batch 提交（服务端并发队列 + 失败自动换源）", "3. GET /api/download/batch/status 轮询进度与每首结果"},
				Endpoints: []string{"GET /api/charts/playlist/detail", "GET /api/charts/detail", "POST /api/download/batch", "GET /api/download/batch/status"},
				WatchOut:  "批量任务同样要靠音源取链；没服务型音源时保持应用页面开着。",
			},
			{
				Task:      "订阅一个歌单，以后有新歌就自动下",
				Summary:   "建监控 → 首轮建基线 → 之后每轮自动发现新歌",
				Steps:     []string{"1. 先 POST /api/monitor/preview 干跑，确认过滤规则不会漏抓或抓一堆改编版", "2. POST /api/monitors 建监控（enabled=true，自动下载按能力位开）", "3. 等定时或 POST /api/monitors/{id}/run 手动跑一轮", "4. GET /api/monitors/{id} 与 GET /api/monitor/tracks 看结果"},
				Endpoints: []string{"POST /api/monitor/preview", "POST /api/monitors", "GET /api/monitors", "POST /api/monitors/{id}/run", "GET /api/monitors/{id}", "GET /api/monitor/tracks"},
				WatchOut:  "**首轮只建基线不下歌**，别因为「跑了一轮没下」判断功能坏了。榜单类或酷狗/酷我来源需要浏览器上报曲目，走 POST /api/monitors/{id}/discover。",
			},
			{
				Task:      "停掉一个订阅或正在跑的任务",
				Summary:   "停止是真停止：整轮会中断，不会再自动复活",
				Steps:     []string{"1. PATCH /api/monitors/{id} 传 enabled=false（或推送任务同样字段）", "2. GET /api/monitors/{id} 确认状态", "3. 要彻底清掉记录用 DELETE /api/monitors/{id}"},
				Endpoints: []string{"GET /api/monitors", "PATCH /api/monitors/{id}", "DELETE /api/monitors/{id}", "POST /api/push/tasks"},
				WatchOut:  "停止订阅后手动再点执行会被 409 拒绝（先 enabled=true）。下载队列的暂停/继续只在界面里，接口层没有这个开关。",
			},
			{
				Task:      "把账号歌单同步进飞牛音乐歌单",
				Summary:   "建推送任务 → 干跑看匹配 → 执行 → 看历史",
				Steps:     []string{"1. GET /api/push/sources 看可推的来源（日推/自建/收藏歌单）", "2. POST /api/push/preview 干跑，看匹配率", "3. POST /api/push/tasks 建任务", "4. POST /api/push/run 立即执行", "5. GET /api/push/runs 看结果"},
				Endpoints: []string{"GET /api/push/sources", "POST /api/push/preview", "POST /api/push/tasks", "POST /api/push/run", "GET /api/push/runs"},
				WatchOut:  "推送是「匹配飞牛库里已有的歌」，匹配不上的可以顺带补下载 —— 建任务时用能力位决定要不要下。",
			},
			{
				Task:      "补全曲库缺的信息（歌词/封面/年份/曲序/光盘/风格）",
				Summary:   "看缺什么 → 勾字段 → 预览 → 确认写入 →（要反悔）撤销",
				Steps:     []string{"1. GET /api/library/index?dir= 先增量扫描（补全靠曲库索引挑活）", "2. GET /api/library/gaps?dir= 看各字段缺多少", "3. POST /api/library/complete 带 dry_run=true 出预览与 plan_id", "4. 看着没问题再 POST /api/library/complete 带 plan_id 执行", "5. GET /api/library/complete 轮询进度", "6. 要回滚：POST /api/library/complete/undo"},
				Endpoints: []string{"GET /api/library/index", "GET /api/library/gaps", "POST /api/library/complete", "GET /api/library/complete", "POST /api/library/complete/undo", "GET /api/library/audit"},
				WatchOut: "值只来自**已匹配的搜索结果**，匹配不上就跳过该字段（宁缺毋滥）。" +
					"命中酷狗/酷我这类不带年份/曲序/专辑标识的响应时，会自动按「主力平台」再认一次、**只借元数据**" +
					"（歌词封面直链仍用原命中），借不到就在结果里逐条说明 —— 跳过不是失败。" +
					"改文件前自动备份，撤销只做一次。不传 fields 时只补歌词+封面。",
			},
			{
				Task:      "只换某一首歌的封面",
				Summary:   "搜候选 → 挑一张 → 写进文件",
				Steps:     []string{"1. GET /api/library/cover-candidates?name=&artist= 拿候选图", "2. POST /api/nas/tags 带 path 与 cover（图片地址）"},
				Endpoints: []string{"GET /api/library/cover-candidates", "POST /api/nas/tags", "GET /api/nas/tags"},
				WatchOut:  "只有 MP3 / FLAC 能内嵌封面（飞牛只认内嵌）。写标签是合并语义，没传的字段不动；失败原因看返回里的 cover_error。",
			},
			{
				Task:      "查重并清理重复曲目",
				Summary:   "先干跑看要动哪些，再移入回收站",
				Steps:     []string{"1. GET /api/duplicates?dir= 找重复（内容相同 + 同名同歌手两个通道）", "2. POST /api/duplicates/resolve 带 dry_run=true 预览", "3. 确认后同样的请求去掉 dry_run，mode=trash 移入回收站"},
				Endpoints: []string{"GET /api/duplicates", "POST /api/duplicates/resolve"},
				WatchOut:  "保留优先级：无损 > 已整理 > 体积更大。默认进 .tidy_trash 回收站，不是真删。",
			},
			{
				Task:      "连平台账号（网易云 / QQ 音乐）",
				Summary:   "申请二维码 → 交给用户扫 → 轮询状态",
				Steps:     []string{"1. POST /api/accounts/netease/qr 或 /api/accounts/qq/qr 拿二维码", "2. 把图给用户扫，AI 自己过不了这步", "3. GET .../qr/check 轮询，成功即自动保存账号", "4. GET /api/accounts 确认已连接"},
				Endpoints: []string{"POST /api/accounts/netease/qr", "GET /api/accounts/netease/qr/check", "POST /api/accounts/qq/qr", "GET /api/accounts/qq/qr/check", "GET /api/accounts", "GET /api/accounts/tracks"},
				WatchOut:  "凭据只存服务端（0600），任何接口都不会回传 Cookie。",
			},
		},
		Rules: []string{
			"响应统一 {code,message,data}；错误用 HTTP 状态码 + 中文 message。⚠️ 只有 /api/catalog 例外：endpoints/guide/usage 在**顶层**。",
			"凡是可能跑几分钟的（补全、批量下载、推送、监控执行）都是**立即返回 + 轮询状态接口**，不要同步等，也别重复提交（同一时间只允许一个，重复返回 409）。",
			"会改用户文件的动作都有干跑/预览（dry_run、preview）且写前备份或进回收站：先看再动手。",
			"失败原因在状态接口的逐条结果里（results[].error / note / skipped），不在 HTTP 码里 —— 只看状态码会以为「成功但其实一首没下」。",
			"别并发扫大库：/api/library/index、/api/duplicates、GET /api/nas/songs 都是全目录扫描。",
		},
	}
}

// usageFromGuide 从 guide 生成顶层 usage 列表。
//
// 为什么生成而不另写一份：这里原来手写 6 条「使用建议」，
// 加了补全、换封面、推送之后一条都没同步 —— 两份必然漂移。
// 现在 usage 只是 workflows + rules 的扁平视图，给人看的抽屉直接用，AI 也读得到。
func usageFromGuide(g Guide) []string {
	out := make([]string, 0, len(g.Workflows)+len(g.Rules))
	for _, wf := range g.Workflows {
		out = append(out, "【"+wf.Task+"】"+strings.Join(wf.Endpoints, " → "))
	}
	return append(out, g.Rules...)
}

// CatalogResponse 目录响应
type CatalogResponse struct {
	Code      int        `json:"code"`
	Message   string     `json:"message"`
	App       string     `json:"app"`
	Version   string     `json:"version"`
	BaseURL   string     `json:"base_url"`
	AuthMode  string     `json:"auth_mode"`
	Endpoints []Endpoint `json:"endpoints"`
	Guide     Guide      `json:"guide"`
	Usage     []string   `json:"usage"` // 由 Guide 生成，别手写第二份
}

// HandleCatalog GET /api/catalog
func (s *Server) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	base := "http://" + r.Host
	// 与 /api/health 用同一个来源：none（不校验）/ lan（内网免校验） / token（一律校验）
	authMode := s.effectiveAuthMode()

	guide := CatalogGuide()
	resp := CatalogResponse{
		Code:      200,
		Message:   "ok",
		App:       "qulv (yinshu-ai)",
		Version:   CurrentVersion,
		BaseURL:   base,
		AuthMode:  authMode,
		Endpoints: APICatalog(base),
		Guide:     guide,
		Usage:     usageFromGuide(guide),
	}
	_ = json.NewEncoder(w).Encode(resp)
}
