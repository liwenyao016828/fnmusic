package api

import (
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/bridge"
	"fn-lx-player/pkg/charts"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/downloader"
	"fn-lx-player/pkg/downloadqueue"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/monitor"
	"fn-lx-player/pkg/nameparse"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/push"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/sources"
	"fn-lx-player/pkg/takeover"
)

// CurrentVersion 应用版本号。发布新版本时与 fpk-package/manifest 的 version 同步修改。
const CurrentVersion = "2.1.118"

// CurrentAppName 对外暴露的应用标识，必须与 fpk-package/manifest 的 appname 一致。
//
// ⚠️ 与 Go module 名 `fn-lx-player` **不是一回事** —— 那是 import 路径，不要动。
// 显示名已更名为「曲率」；技术标识仍是 yinshu-ai（与 fpk manifest 的 appname 一致，
// 保持不动是为了让飞牛应用中心把它认成同一个应用的**升级**）。与更早的「极光音乐」(fn-lx-player) 是两个独立应用。
const CurrentAppName = "yinshu-ai"

func init() {
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	_ = mime.AddExtensionType(".html", "text/html; charset=utf-8")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
	_ = mime.AddExtensionType(".png", "image/png")
	_ = mime.AddExtensionType(".json", "application/json; charset=utf-8")
}

type Server struct {
	cfgMgr     *config.ConfigManager
	sourcesMgr *sources.Manager
	chartMgr   *charts.ChartManager
	downloader *downloader.Downloader
	bridge     *bridge.Handler
	staticFS   fs.FS

	// 歌单监控
	monitorStore *monitor.Store
	monitorAPI   *monitor.Handler
	scheduler    *monitor.Scheduler

	discoveredMu sync.Mutex
	discovered   map[string]*discoveredPayload

	// 整理 / 去重 / AI
	libIndex *library.Index
	aiClient *ai.Client
	aiCfgMu  sync.Mutex
	// 用户自定义的命名解析规则（AI 生成的正则），优先于内置启发式
	nameRules *nameparse.Store
	// aiBackupRoot 补全任务的备份根目录（<dataDir>/ai_backup），供「撤销」还原
	aiBackupRoot string

	// 第三方平台账号（扫码登录，凭据 0600 落盘）
	accountStore *account.Store

	// 推送任务：账号歌单 → 飞牛音乐歌单
	pushStore  *push.Store
	pushRunner *push.Runner

	// 网页端下载队列的持久化（原来只存浏览器 localStorage，换设备就没了）
	downloadQueue *downloadqueue.Store

	// 音乐入口接管（可选能力，默认关闭）。nil = 本进程未开启。
	takeoverMgr *takeover.Manager
	// takeoverErr 记录「环境变量开了接管、但本次没能接管成功」的原因。
	//
	// 没有它的时候，接管失败和没开启在 /api/takeover/status 上长得一模一样
	// （都是 enabled:false + 「设 QULV_TAKEOVER=1 后重启」），真机上排查时
	// 会往「环境变量没生效」的方向白跑 —— 实际原因是别处（例如 rename 跨文件系统）。
	takeoverErr string
	// musicdlStatus 返回 musicdl sidecar 的状态（见 pkg/api/musicdl.go）。
	// 是函数而不是结构体：状态在变（准备中 / 就绪 / 失败），必须是**现取**的。
	musicdlStatus func() map[string]any
	// musicdlClientFn 返回 sidecar 客户端（nil = 没开）。同样是函数：客户端会在
	// 配置变化时重建（见 pkg/api/musicdl.go）。
	musicdlClientFn func() *online.MusicDL
	// musicdlRestart 让界面上的「重试启动」能真的把挂掉的 sidecar 拉起来。
	musicdlRestart func()
	// 试运行（预览一个未启用的源）的三个动作，见 pkg/api/musicdl.go 的
	// handleMusicDLPreview。都是函数：状态在变（进程准备中 / 就绪 / 失败），
	// 而且**必须**现取 —— 抓一份就等于永远指向启动时的那一刻。
	musicdlPreviewStart  func(source string) (map[string]any, error)
	musicdlPreviewStatus func() map[string]any
	musicdlPreviewStop   func() map[string]any

	apiToken string
	// authMode 生效的鉴权模式（none / lan / token），由 FN_AUTH_MODE + FN_API_TOKEN 解析
	authMode string
	// trustedCIDRs = FN_TRUSTED_CIDRS 追加的信任网段，lan 模式下这些来源免 Token
	trustedCIDRs []*net.IPNet
	corsOrigins  []string
	startedAt    time.Time
}

// effectiveAuthMode 返回生效的鉴权模式。字段没初始化时按 FN_API_TOKEN 推断，
// 这样零值 Server（测试里常见）也不会莫名其妙放开。
func (s *Server) effectiveAuthMode() string {
	if s.authMode != "" {
		return s.authMode
	}
	return ResolveAuthMode("", s.apiToken)
}

// AuthSummary 启动日志用：模式 + 一句话说明 + 追加的信任网段
func (s *Server) AuthSummary() string {
	mode := s.effectiveAuthMode()
	out := mode + "（" + authModeNote(mode) + "）"
	if extra := TrustedCIDRStrings(s.trustedCIDRs); len(extra) > 0 {
		out += " 额外信任网段: " + strings.Join(extra, ", ")
	}
	return out
}

// authModeNote 给 /api/health 用的一句话说明：当前模式到底放行了谁。
func authModeNote(mode string) string {
	switch mode {
	case AuthModeToken:
		return "所有非公开接口都需要 X-API-Token（或 ?token=）"
	case AuthModeNone:
		return "完全不校验：任何能连上本机端口的人都能读写曲库"
	default:
		return "本机/内网/容器地址免校验，其它来源必须带 Token"
	}
}

func NewServer(cfgMgr *config.ConfigManager, sourcesMgr *sources.Manager, staticFS fs.FS) *Server {
	dl := downloader.NewDownloader(cfgMgr)

	// 监控数据与配置同处应用数据目录，便于整体备份
	mStore := monitor.NewStore(cfgMgr.DataDir())
	mHandler := monitor.NewHandler(mStore, nil)

	s := &Server{
		cfgMgr:       cfgMgr,
		sourcesMgr:   sourcesMgr,
		chartMgr:     charts.NewChartManager(),
		downloader:   dl,
		bridge:       bridge.NewHandler(bridge.NewManager()),
		staticFS:     staticFS,
		monitorStore: mStore,
		monitorAPI:   mHandler,
		discovered:   make(map[string]*discoveredPayload),
		corsOrigins:  CORSOrigins(),
		startedAt:    time.Now(),
	}

	// 鉴权：FN_API_TOKEN（凭据）+ FN_AUTH_MODE（none/lan/token，空=auto）
	// + FN_TRUSTED_CIDRS（lan 模式下额外信任的来源，逗号分隔的 IP/CIDR）
	s.apiToken = APIToken()
	s.authMode = ResolveAuthMode(AuthModeEnv(), s.apiToken)
	s.trustedCIDRs = ParseTrustedCIDRs(TrustedCIDRsEnv())

	// 加载目录扫描结果缓存：NAS 页打开时命中缓存即不再全盘重扫
	nas.InitScanCache(cfgMgr.DataDir())

	// 把配置里的手工飞牛令牌注入运行时（非空时优先于环境变量与数据库）。
	// 用户通过 POST /api/fnos/token 改动时会立即重新注入，不用重启。
	fnos.SetManualToken(cfgMgr.Get().FnosToken)

	// 注入调度：发现走「浏览器上报」，下载复用既有下载器
	mHandler.SetDispatcher(&monitorDispatcher{srv: s})
	s.scheduler = monitor.NewScheduler(mHandler, mStore, time.Minute)

	// 曲库增量索引与 AI 客户端
	s.libIndex = library.NewIndex(cfgMgr.DataDir())
	s.aiClient = ai.NewClient(ai.DefaultConfig())
	s.loadAIConfig()

	// 用户自定义的命名解析规则（AI 生成或手写），与配置同目录便于整体备份
	s.nameRules = nameparse.NewStore(filepath.Join(cfgMgr.DataDir(), "name_rules.json"))

	// 补全改文件前的备份根目录 —— 「撤销上一次补全」从这里还原
	s.aiBackupRoot = filepath.Join(cfgMgr.DataDir(), "ai_backup")

	// 第三方平台账号（与配置同目录，便于整体备份）
	s.accountStore = account.NewStore(cfgMgr.DataDir())

	// 推送任务（账号歌单 → 飞牛音乐歌单），调度器随服务启动
	s.pushStore = push.NewStore(cfgMgr.DataDir())
	s.pushRunner = push.NewRunner(s.pushStore, s.accountStore)
	s.downloadQueue = downloadqueue.NewStore(cfgMgr.DataDir())
	s.pushRunner.Start(time.Minute)

	// 曲库重扫的节流状态落盘：`lastRun` 只存内存的话进程重启就归零，
	// 重启后 30 秒又能扫一次（真机「音乐任务一直在扫」的成因之一）。
	// 见 pkg/fnos/rescan.go 的 SetStatePath。
	fnos.SetRescanStatePath(filepath.Join(cfgMgr.DataDir(), "fnos_rescan.json"))

	return s
}

// StartScheduler 启动监控调度（由 main 在服务就绪后调用）
func (s *Server) StartScheduler() {
	if s.scheduler != nil {
		s.scheduler.Start()
	}
}

// StopScheduler 停止监控调度与推送调度
func (s *Server) StopScheduler() {
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
	if s.pushRunner != nil {
		s.pushRunner.Stop()
	}
	// 待执行的重扫定时器一并取消（`StopLibraryRescan` 以前是没人调的死代码）
	fnos.StopLibraryRescan()
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	// API 路由全部登记在 routes.go 的 routeTable 里，这里只按表挂载。
	// 表与 /api/catalog 的一致性由 catalog_routes_test.go 把关。
	for _, rt := range routeTable {
		mux.HandleFunc(rt.Path, rt.Handler(s))
	}

	// 以下静态兜底不进 routeTable：它不是 /api 接口，也不必出现在目录里。
	if s.staticFS != nil {
		fileServer := http.FileServer(http.FS(s.staticFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}

			// 未知 API 路径不应回落到前端页面：否则调用方会拿到 200 + HTML，
			// 误以为接口调用成功。统一返回 404 JSON 并提示查阅接口目录。
			if strings.HasPrefix(path, "api/") {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":    404,
					"message": "接口不存在: /" + path + "。请调用 GET /api/catalog 获取全部可用接口。",
				})
				return
			}

			f, err := s.staticFS.Open(path)
			if err != nil {
				// SPA fallback to index.html
				indexFile, err := s.staticFS.Open("index.html")
				if err != nil {
					http.NotFound(w, r)
					return
				}
				defer indexFile.Close()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
				_, _ = io.Copy(w, indexFile)
				return
			}
			_ = f.Close()

			if path == "index.html" {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			} else if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
		})
	}

	// 中间件顺序：CORS（含 preflight 短路）→ 鉴权 → 路由
	return s.corsMiddleware(s.authMiddleware(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":   200,
		"status": "ok",
		"data": map[string]interface{}{
			"app":              "qulv (yinshu-ai)",
			"version":          CurrentVersion,
			"uptime_seconds":   int(time.Since(s.startedAt).Seconds()),
			"auth_mode":        s.effectiveAuthMode(),
			"auth_note":        authModeNote(s.effectiveAuthMode()),
			"trusted_cidrs":    TrustedCIDRStrings(s.trustedCIDRs),
			"cors_origins":     s.corsOrigins,
			"bridge":           s.bridge.StatsSnapshot(),
			"catalog_endpoint": "/api/catalog",
		},
	})
}

// handleUIPrefs 读写「窗口记忆」（各页面点击状态）。
//
// 前端各页面的选中状态（发现页 Tab、榜单平台、搜索平台、NAS 目录、播放模式、
// 当前音源）原来只存在浏览器 localStorage，换浏览器/清缓存就丢，多端也不共享。
// 这里把它们落到应用数据目录的 ui_prefs.json，供 GET/POST /api/ui-prefs 读写。
func (s *Server) handleUIPrefs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		all := s.cfgMgr.UIPrefs().All()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data":    all,
		})
	case http.MethodPost:
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&patch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(patch) > 64 {
			http.Error(w, "窗口记忆键过多（上限 64）", http.StatusBadRequest)
			return
		}
		if err := s.cfgMgr.UIPrefs().Merge(patch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		all := s.cfgMgr.UIPrefs().All()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data":    all,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLogs 读取 / 清空后端日志缓冲。
//
// GET    /api/logs?limit=200   读取最近若干条
// DELETE /api/logs             清空
//
// 日志只存内存（见 pkg/applog），重启即清空 —— 不落盘，
// 避免日志里可能出现的路径与令牌长期留在 NAS 磁盘上。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		limit := 200
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
				limit = n
			}
		}
		entries := applog.Default().Recent(limit)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"entries": entries,
				"count":   len(entries),
				"total":   applog.Default().Len(),
				"hint":    "日志只保留在内存里，重启应用即清空。",
			},
		})
	case http.MethodDelete:
		applog.Default().Clear()
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已清空"})
	case http.MethodPost:
		// 网页端上报前端错误（见 frontend/src/services/appLog.js）。
		// 与后端日志汇到同一个缓冲区 —— 排查一次操作失败时，
		// 服务端和浏览器两侧的信息落在同一条时间轴上。
		var req struct {
			Source  string `json:"source"`
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			errJSON(w, http.StatusBadRequest, "message 不能为空")
			return
		}
		applog.Default().AddFrom(req.Source, req.Level, req.Message)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok"})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// 鉴权模式的解析/说明见 middleware.go 顶部的「鉴权模式」注释与 authModeNote()。

// handleMonitorSubroutes 分发 /api/monitors/{id}[/action] 下的子路由
func (s *Server) handleMonitorSubroutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/run"):
		s.monitorAPI.HandleRun(w, r)
	case strings.HasSuffix(path, "/discover"):
		s.HandleDiscover(w, r)
	default:
		s.monitorAPI.HandleMonitorDetail(w, r)
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Get()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data":    cfg,
			"config":  cfg,
		})
	case http.MethodPost:
		var patch config.AppConfig
		raw, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(raw, &patch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// ⚠️ `Update` 对 FnosToken 是**无条件覆盖**（空串就表示清除令牌），
		// 而这里收到的是一份**局部 patch**：不补这一步，用户改个主题就会把
		// 手工飞牛令牌悄悄清掉。所以 body 里没出现 fnos_token 就沿用当前值。
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err == nil {
			if _, ok := keys["fnos_token"]; !ok {
				patch.FnosToken = s.cfgMgr.Get().FnosToken
			}
		}
		if err := s.cfgMgr.Update(patch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg := s.cfgMgr.Get()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data":    cfg,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// playerStreamUA 中继与校验出站时使用的 UA。
// 不少音乐 CDN 会按 UA 拒绝（默认 Go UA 常被挡），所以统一伪装成浏览器。
const playerStreamUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

// streamClient 用于把第三方直链转成浏览器可播放的流。
// 复用连接池；不做整体超时（长音频流），由请求上下文与 ResponseHeaderTimeout 兜底。
var streamClient = &http.Client{
	// 流式响应体不能被「单次尝试上限」掐断，所以用 stream 变体：
	// 只保留「连接层失败 → 直连一次」的降级能力
	Transport: security.NewSafeStreamRoundTripper(security.DefaultOptions()),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		_, err := security.ValidateURL(req.URL.String(), security.DefaultOptions())
		return err
	},
}

func handleAudioStream(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	targetURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if targetURL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	// SSRF 防护：仅允许公网 http/https
	if _, err := security.ValidateURL(targetURL, security.DefaultOptions()); err != nil {
		http.Error(w, "拒绝访问该地址: "+err.Error(), http.StatusForbidden)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	outReq.Header.Set("User-Agent", playerStreamUA)

	customReferer := strings.TrimSpace(r.URL.Query().Get("referer"))
	if ref := downloader.GetStreamReferer(targetURL, customReferer); ref != "" {
		outReq.Header.Set("Referer", ref)
	}

	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		outReq.Header.Set("Range", rangeHeader)
	}

	resp, err := streamClient.Do(outReq)
	if err != nil {
		http.Error(w, "stream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "audio/mpeg")
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handlePlayerCheck 轻量校验一个播放地址**是否真的能取到音频**。
//
//	GET /api/player/check?url=<直链>&referer=<可选>
//
// 为什么需要：音源（尤其「服务型」聚合接口）会返回**形状合法但文件不存在**的地址。
// 实测 `聚合API接口 v3` 对 QQ 音乐返回 `RS02...mp3?guid=api.vkeys.cn&...`，
// 上游直接回 `{"errorcode":-46628,"errormsg":"file not exist"}`。
// 而前端只判断「是不是 http(s) 链接」，于是认为取链成功 → 播放器再去拉就 404，
// 用户看到的是「无法在线播放」，界面上没有任何提示、也看不出是哪一环坏了。
//
// 这里用 `Range: bytes=0-1` **只取 2 字节就断开**，绝不把整个音频拉下来。
// 返回 {ok, status, content_type, note}；判据是 200 / 206 才算可取。
func handlePlayerCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	targetURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if targetURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "url required"})
		return
	}
	// 与中继同一套 SSRF 防护：仅允许公网 http/https
	if _, err := security.ValidateURL(targetURL, security.DefaultOptions()); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": 403, "message": "拒绝访问该地址: " + err.Error()})
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "invalid url"})
		return
	}
	outReq.Header.Set("User-Agent", playerStreamUA)
	outReq.Header.Set("Range", "bytes=0-1")
	if ref := downloader.GetStreamReferer(targetURL, strings.TrimSpace(r.URL.Query().Get("referer"))); ref != "" {
		outReq.Header.Set("Referer", ref)
	}

	resp, err := streamClient.Do(outReq)
	if err != nil {
		// 取不到也算「校验不通过」，但仍用 200 返回结构化结果 —— 这是业务判定，不是接口错误
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "ok",
			"data": map[string]any{"ok": false, "note": "取不到音频：" + err.Error()},
		})
		return
	}
	defer resp.Body.Close()

	// 只读最多 2 字节就断开 —— 不要为了校验把整个音频拉下来
	_, _ = io.CopyN(io.Discard, resp.Body, 2)

	ok := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent
	note := ""
	if !ok {
		note = "上游返回 " + resp.Status
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"ok":           ok,
			"status":       resp.StatusCode,
			"content_type": resp.Header.Get("Content-Type"),
			"note":         note,
		},
	})
}
