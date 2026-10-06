// 全部 API 路由的登记表。
//
// 为什么要有这张表：这些路由以前是 104 条散落在 Router() 里的 mux.HandleFunc 字面量，
// 而 /api/catalog 是另一份手写的接口清单 —— 两份清单各自维护必然漂移，实际已经漂了：
// 有 5 个具名接口压根没进目录（AI 通过 /api/catalog 驱动这个 app，没登记的接口它发现不了）。
// 现在注册来源唯一化，路由表与目录的关系由 catalog_routes_test.go 双向严格比对：
// 加接口忘了写目录 = 测试红，写目录忘了路由 = 也红。
//
// 表里只有路径 + 处理器构造函数（func(*Server) http.HandlerFunc 而不是直接的 http.HandlerFunc，
// 是为了测试不必构造一个完整的 Server —— 闭包只在真正注册时才求值）。
//
// 「一条路由对应目录里哪一条」由三处决定：
//  1. 路径字面相同（绝大多数）；
//  2. routeCatalogAliases —— 前缀挂载 / 模板路径的服务端写法，一条路由服务一组接口时用它显式映射；
//  3. undocumentedRoutes —— 故意不登记的路由，必须写明理由。
//
// 2、3 两张表本身也在测试里被校验（键必须是真路由、别名目标必须真的在目录里），
// 免得它们变成掩盖漏登记的后门。
//
// 表里不记录「允许的方法」：那需要逐条核实 104 条路由的方法集，改错会静默影响界面；方法维度由
// 目录（方法粒度）自己体现，本表只保证路径这一层不漂移。
package api

import (
	"encoding/json"
	"net/http"

	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/proxy"
	"fn-lx-player/pkg/search"
)

// routeSpec 一条真实挂载的 API 路由。
type routeSpec struct {
	Path    string
	Handler func(*Server) http.HandlerFunc
}

// routeTable 注册顺序即挂载顺序（ServeMux 按最长前缀匹配，顺序实际上不敏感，但保持一致便于对读）。
var routeTable = []routeSpec{

	// 0. 健康检查与接口自描述（无需鉴权）
	{Path: "/api/health", Handler: func(s *Server) http.HandlerFunc { return s.handleHealth }},
	{Path: "/api/catalog", Handler: func(s *Server) http.HandlerFunc { return s.HandleCatalog }},
	{
		Path: "/api/app/version",
		Handler: func(s *Server) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code": 200,
					"data": map[string]interface{}{
						"version": CurrentVersion,
						"name":    CurrentAppName,
					},
				})
			}
		},
	},

	// 1. Proxy（已启用 SSRF 防护）
	{Path: "/api/proxy/http", Handler: func(s *Server) http.HandlerFunc { return proxy.HandleProxyHTTP }},

	// 音乐入口接管：状态查询 + 还原（开启走环境变量，不在这里开）
	{Path: "/api/takeover/status", Handler: func(s *Server) http.HandlerFunc { return s.handleTakeoverStatus }},
	{Path: "/api/takeover/restore", Handler: func(s *Server) http.HandlerFunc { return s.handleTakeoverRestore }},

	// 2. Sources
	{Path: "/api/sources", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleListSources }},
	{Path: "/api/sources/script", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleGetScript }},
	{Path: "/api/sources/active", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleSetActive }},
	{Path: "/api/sources/custom", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleAddCustom }},
	{Path: "/api/sources/upload", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleAddCustom }},
	{Path: "/api/sources/import_url", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleImportURL }},
	{Path: "/api/sources/import_batch", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleImportBatch }},

	// 「按服务地址」接入的音源（后端直连，不执行第三方 JS）
	{Path: "/api/sources/api", Handler: func(s *Server) http.HandlerFunc { return s.HandleAPISources }},
	{Path: "/api/sources/api/detect", Handler: func(s *Server) http.HandlerFunc { return s.HandleAPISourceDetect }},
	{Path: "/api/sources/api/resolve", Handler: func(s *Server) http.HandlerFunc { return s.HandleAPISourceResolve }},
	{Path: "/api/sources/batch_delete", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleBatchDelete }},
	{Path: "/api/sources/export", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleExportSources }},
	{Path: "/api/sources/cleanup", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleCleanupSources }},
	{Path: "/api/sources/custom/delete", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleDeleteCustom }},
	{Path: "/api/sources/", Handler: func(s *Server) http.HandlerFunc { return s.sourcesMgr.HandleSourcesRest }},

	// 2.4 音源协议：官方协议（后端内置）与 LX 协议（浏览器脚本）的能力边界
	{Path: "/api/protocols", Handler: func(s *Server) http.HandlerFunc { return s.HandleProtocols }},

	// 2.5 Charts & Playlists
	{Path: "/api/charts/toplists", Handler: func(s *Server) http.HandlerFunc { return s.chartMgr.HandleToplists }},
	{Path: "/api/charts/detail", Handler: func(s *Server) http.HandlerFunc { return s.chartMgr.HandleChartDetail }},

	// 2.6 musicdl sidecar（外挂音源进程，v2.1.83）
	{Path: "/api/musicdl/status", Handler: func(s *Server) http.HandlerFunc { return s.handleMusicDLStatus }},
	{Path: "/api/musicdl/sources", Handler: func(s *Server) http.HandlerFunc { return s.handleMusicDLSources }},
	{Path: "/api/musicdl/probe", Handler: func(s *Server) http.HandlerFunc { return s.handleMusicDLProbe }},
	{Path: "/api/musicdl/restart", Handler: func(s *Server) http.HandlerFunc { return s.handleMusicDLRestart }},
	{Path: "/api/charts/playlists", Handler: func(s *Server) http.HandlerFunc { return s.chartMgr.HandlePlaylists }},
	{Path: "/api/charts/playlist/detail", Handler: func(s *Server) http.HandlerFunc { return s.chartMgr.HandlePlaylistDetail }},
	{Path: "/api/charts/playlist/categories", Handler: func(s *Server) http.HandlerFunc { return s.chartMgr.HandlePlaylistCategories }},

	// 2.6 Downloader to NAS
	{
		Path: "/api/download/config",
		Handler: func(s *Server) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					s.downloader.HandleSetConfig(w, r)
				} else {
					s.downloader.HandleGetConfig(w, r)
				}
			}
		},
	},
	{Path: "/api/download/song", Handler: func(s *Server) http.HandlerFunc { return s.downloader.HandleDownloadSong }},
	{Path: "/api/download/enrich", Handler: func(s *Server) http.HandlerFunc { return s.downloader.HandleEnrichExisting }},
	{Path: "/api/download/batch", Handler: func(s *Server) http.HandlerFunc { return s.downloader.HandleDownloadBatch }},
	{Path: "/api/download/batch/status", Handler: func(s *Server) http.HandlerFunc { return s.downloader.HandleBatchStatus }},
	{Path: "/api/download/check", Handler: func(s *Server) http.HandlerFunc { return s.downloader.HandleCheckDownloaded }},

	// 下载队列持久化：原来只存浏览器，换设备/清缓存就丢（2026-09-22）
	{Path: "/api/download/queue", Handler: func(s *Server) http.HandlerFunc { return s.HandleDownloadQueue }},

	// 2.7 AI 解析桥（外部调用者 ↔ 浏览器音源运行时）
	{Path: "/api/bridge/resolve", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleResolve }},
	{Path: "/api/bridge/pending", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandlePending }},
	{Path: "/api/bridge/claim", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleClaim }},
	{Path: "/api/bridge/result", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleResult }},
	{Path: "/api/bridge/stats", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleStats }},
	{Path: "/api/bridge/jobs", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleJob }},
	{Path: "/api/bridge/jobs/", Handler: func(s *Server) http.HandlerFunc { return s.bridge.HandleJob }},

	// 2.8 歌单监控（榜单/歌单/收藏夹的定时增量下载）
	{Path: "/api/monitors", Handler: func(s *Server) http.HandlerFunc { return s.monitorAPI.HandleMonitors }},
	{Path: "/api/monitors/", Handler: func(s *Server) http.HandlerFunc { return s.handleMonitorSubroutes }},
	{Path: "/api/monitor/preview", Handler: func(s *Server) http.HandlerFunc { return s.HandleMonitorPreview }},
	{Path: "/api/monitor/tracks", Handler: func(s *Server) http.HandlerFunc { return s.monitorAPI.HandleTracks }},
	{Path: "/api/monitor/tracks/mark", Handler: func(s *Server) http.HandlerFunc { return s.monitorAPI.HandleTracksMark }},
	{Path: "/api/monitor/runs", Handler: func(s *Server) http.HandlerFunc { return s.monitorAPI.HandleRuns }},
	{Path: "/api/monitor/due", Handler: func(s *Server) http.HandlerFunc { return s.monitorAPI.HandleDue }},

	// 2.9 整理 / 去重 / 曲库索引
	{Path: "/api/tidy", Handler: func(s *Server) http.HandlerFunc { return s.HandleTidy }},
	{Path: "/api/library/audit", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryAudit }},

	// 手动换封面的候选图搜索（本地页每首歌的「封面」按钮用）
	{Path: "/api/library/cover-candidates", Handler: func(s *Server) http.HandlerFunc { return s.HandleCoverCandidates }},
	{Path: "/api/library/index", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryIndex }},
	{Path: "/api/library/index/list", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryIndexList }},
	{Path: "/api/library/index/stats", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryIndexStats }},

	// 各字段缺口统计（曲库管家「AI 补全」子页第一步：先看缺什么，再勾选补什么）
	{Path: "/api/library/gaps", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryGaps }},

	// 曲库补全（主动）：挑缺歌词/封面的 → 搜索匹配 → 取歌词/封面 → 写标签。
	// POST 启动异步任务，GET 读进度与结果，/cancel 停止正在跑的（不再派发新曲目）。
	{Path: "/api/library/complete", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryComplete }},
	{Path: "/api/library/complete/cancel", Handler: func(s *Server) http.HandlerFunc { return s.HandleLibraryCompleteCancel }},

	// 用户自定义的命名解析规则（AI 生成正则 / 手写正则）
	{Path: "/api/library/name-rules", Handler: func(s *Server) http.HandlerFunc { return s.HandleNameRules }},

	// 撤销最近一次补全（把改过的文件从备份还原）
	{Path: "/api/library/complete/undo", Handler: func(s *Server) http.HandlerFunc { return s.HandleCompleteUndo }},
	{Path: "/api/duplicates", Handler: func(s *Server) http.HandlerFunc { return s.HandleDuplicates }},
	{Path: "/api/duplicates/resolve", Handler: func(s *Server) http.HandlerFunc { return s.HandleDuplicatesResolve }},

	// 2.10 AI 大模型
	{Path: "/api/ai/config", Handler: func(s *Server) http.HandlerFunc { return s.HandleAIConfig }},
	{Path: "/api/ai/usage", Handler: func(s *Server) http.HandlerFunc { return s.HandleAIUsage }},
	{Path: "/api/ai/usage/reset", Handler: func(s *Server) http.HandlerFunc { return s.HandleAIResetUsage }},
	{Path: "/api/ai/test", Handler: func(s *Server) http.HandlerFunc { return s.HandleAITest }},
	{Path: "/api/ai/complete", Handler: func(s *Server) http.HandlerFunc { return s.HandleAIComplete }},

	// 2.11 飞牛 fnOS 集成（官方开放 API + 音乐应用接口）
	{Path: "/api/fnos/status", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosStatus }},
	{Path: "/api/fnos/rescan", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosRescan }},
	{Path: "/api/fnos/check", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosCheck }},
	{Path: "/api/fnos/token", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosToken }},
	{Path: "/api/fnos/folders", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosFolders }},
	{Path: "/api/fnos/playlists", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosPlaylists }},
	{Path: "/api/fnos/push", Handler: func(s *Server) http.HandlerFunc { return s.HandleFnosPush }},

	// 2.12 第三方平台账号（扫码登录）
	{Path: "/api/accounts", Handler: func(s *Server) http.HandlerFunc { return s.HandleAccounts }},
	{Path: "/api/accounts/tracks", Handler: func(s *Server) http.HandlerFunc { return s.HandleAccountTracks }},
	{Path: "/api/accounts/netease/qr", Handler: func(s *Server) http.HandlerFunc { return s.HandleNeteaseQR }},
	{Path: "/api/accounts/netease/qr/check", Handler: func(s *Server) http.HandlerFunc { return s.HandleNeteaseQRCheck }},
	{Path: "/api/accounts/netease/daily", Handler: func(s *Server) http.HandlerFunc { return s.HandleNeteaseDaily }},
	{Path: "/api/accounts/netease/playlists", Handler: func(s *Server) http.HandlerFunc { return s.HandleNeteasePlaylists }},
	{Path: "/api/accounts/qq/qr", Handler: func(s *Server) http.HandlerFunc { return s.HandleQQQR }},
	{Path: "/api/accounts/qq/qr/check", Handler: func(s *Server) http.HandlerFunc { return s.HandleQQQRCheck }},
	{Path: "/api/accounts/qq/daily", Handler: func(s *Server) http.HandlerFunc { return s.HandleQQDaily }},
	{Path: "/api/accounts/qq/playlists", Handler: func(s *Server) http.HandlerFunc { return s.HandleQQPlaylists }},
	{Path: "/api/accounts/qq/playlist", Handler: func(s *Server) http.HandlerFunc { return s.HandleQQPlaylistDetail }},

	// 2.13 推送任务（账号歌单 → 飞牛音乐歌单）
	{Path: "/api/push/sources", Handler: func(s *Server) http.HandlerFunc { return s.HandlePushSources }},
	{Path: "/api/push/tasks", Handler: func(s *Server) http.HandlerFunc { return s.HandlePushTasks }},
	{Path: "/api/push/run", Handler: func(s *Server) http.HandlerFunc { return s.HandlePushRun }},
	{Path: "/api/push/preview", Handler: func(s *Server) http.HandlerFunc { return s.HandlePushPreview }},
	{Path: "/api/push/runs", Handler: func(s *Server) http.HandlerFunc { return s.HandlePushRuns }},

	// 3. NAS Local Music
	{Path: "/api/nas/folders", Handler: func(s *Server) http.HandlerFunc { return nas.ListFolders }},
	{Path: "/api/nas/directories", Handler: func(s *Server) http.HandlerFunc { return nas.ListFolders }},
	{Path: "/api/nas/browse", Handler: func(s *Server) http.HandlerFunc { return nas.BrowseDirectory }},
	// 读文本文件（「从 NAS 选择音源脚本」用）：扩展名白名单 + 体积上限 + 路径守卫，见 pkg/nas/readfile.go
	{Path: "/api/nas/read", Handler: func(s *Server) http.HandlerFunc { return nas.ReadTextFile }},
	{Path: "/api/nas/songs", Handler: func(s *Server) http.HandlerFunc { return nas.ScanSongs }},
	{Path: "/api/nas/scan", Handler: func(s *Server) http.HandlerFunc { return nas.ScanSongs }},
	{Path: "/api/nas/stream", Handler: func(s *Server) http.HandlerFunc { return nas.StreamAudio }},
	{Path: "/api/nas/cover", Handler: func(s *Server) http.HandlerFunc { return nas.ExtractCover }},
	{Path: "/api/nas/lyric", Handler: func(s *Server) http.HandlerFunc { return nas.HandleNasLyric }},

	// 3.5 音频标签（歌词内嵌 / 封面 / 元数据）
	{
		Path: "/api/nas/tags",
		Handler: func(s *Server) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					nas.HandleReadTags(w, r)
				} else {
					nas.HandleEmbedTags(w, r)
				}
			}
		},
	},

	// 4. Search & Direct Audio Resolver
	{Path: "/api/search", Handler: func(s *Server) http.HandlerFunc { return search.HandleSearch }},
	{Path: "/api/search/lyric", Handler: func(s *Server) http.HandlerFunc { return search.HandleLyric }},
	{Path: "/api/music/qualities", Handler: func(s *Server) http.HandlerFunc { return search.HandleQualities }},
	{
		Path: "/api/player/resolve",
		Handler: func(s *Server) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":  403,
					"error": "本播放器为纯本地播放器容器，不内置在线音源直链解析。请使用 /api/bridge/resolve 交由网页端第三方音源脚本解析。",
				})
			}
		},
	},
	{Path: "/api/player/stream", Handler: func(s *Server) http.HandlerFunc { return handleAudioStream }},
	{Path: "/api/player/check", Handler: func(s *Server) http.HandlerFunc { return handlePlayerCheck }},

	// 5. Config
	{Path: "/api/config", Handler: func(s *Server) http.HandlerFunc { return s.handleConfig }},

	// 5.1 窗口记忆（各页面点击状态）：前端偏好落到服务端文件 ui_prefs.json
	{Path: "/api/ui-prefs", Handler: func(s *Server) http.HandlerFunc { return s.handleUIPrefs }},

	// 5.2 后端日志（内存环形缓冲，重启即清空）—— 网页端「日志」页读它
	{Path: "/api/logs", Handler: func(s *Server) http.HandlerFunc { return s.handleLogs }},
	{Path: "/api/diagnostics/platforms", Handler: func(s *Server) http.HandlerFunc { return s.handlePlatformDiagnostics }},
}

// routeCatalogAliases 路径在路由表里与目录里写法不同的映射。
// 键必须是 routeTable 里真实存在的路径，值必须真的出现在目录里（测试校验）。
// 内容是「服务端一条路由服务一组接口」或「目录用 {id} 模板写法」这两种情况。
var routeCatalogAliases = map[string][]string{
	// /api/bridge/jobs 与 /api/bridge/jobs/ 是同一个接口的两种写法（裸路径只有 ?job_id= 可用）
	"/api/bridge/jobs":  {"/api/bridge/jobs/{job_id}"},
	"/api/bridge/jobs/": {"/api/bridge/jobs/{job_id}"},
	// 前缀挂载：后缀分发到 /api/monitors/{id}、…/run、…/discover
	"/api/monitors/": {
		"/api/monitors/{id}",
		"/api/monitors/{id}/run",
		"/api/monitors/{id}/discover",
	},
	// 前缀挂载：/api/sources/{id} 与 /api/sources/{id}/script 的路径写法
	"/api/sources/": {
		"/api/sources/script",
		"/api/sources/custom/delete",
	},
}

// undocumentedRoutes 故意不进 /api/catalog 的路由，值 = 理由（测试要求理由非空）。
// 两种情况：同处理器历史别名（登记会诱导调用方写死旧路径）、网页端音源脚本内部协议（AI 不该调）。
var undocumentedRoutes = map[string]string{
	"/api/nas/directories": "与 /api/nas/folders 同一处理器，历史别名",
	"/api/nas/scan":        "与 /api/nas/songs 同一处理器，历史别名",
	"/api/sources/upload":  "与 /api/sources/custom 同一处理器（界面文件上传入口），登记会重复",
	"/api/bridge/claim":    "网页端音源脚本内部协议，AI 直接调会污染候选队列",
	"/api/bridge/result":   "网页端音源脚本内部协议，AI 直接调会污染候选队列",
	"/api/player/resolve":  "恒 403 的历史桩，调用方应改用 /api/bridge/resolve",
}
