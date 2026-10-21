package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fn-lx-player/pkg/api"
	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/intercept"
	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/sources"
	"fn-lx-player/pkg/takeover"
)

//go:embed all:dist
var frontendDist embed.FS

func main() {
	port := flag.Int("port", 8899, "HTTP server listening port")
	dataDir := flag.String("data", "./data", "Data directory for persistent configuration")
	flag.Parse()

	// 日志同时写 stderr 与内存环形缓冲：网页端「日志」页读后者。
	// 应用跑在 NAS 上，没有这个就得 SSH 上去翻文件才能知道后端在干什么。
	// 只保留最近若干条且不落盘，重启即清空。
	log.SetOutput(io.MultiWriter(os.Stderr, applog.Default()))

	log.Printf("[INIT] Starting 曲率 (fn-lx-player) on port %d, data directory: %s", *port, *dataDir)

	// 1. Prepare frontend static FS
	var staticFS fs.FS
	sub, err := fs.Sub(frontendDist, "dist")
	if err == nil {
		staticFS = sub
	}

	// 2. Initialize Config & Sources Manager (Pure Custom Sources)
	cfgMgr, err := config.NewConfigManager(*dataDir, *port)
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize config manager: %v", err)
	}

	sourcesMgr := sources.NewManager(cfgMgr, nil, "")

	// 2.1 曲库文件命名格式（`歌曲名 - 歌手名` / `歌手名 - 歌曲名`）
	//
	// 这是个**进程级约定**：下载写入（downloader/tee）与扫描读取（nas/tidy）都按它走，
	// 所以开机设一次、每次保存配置再设一次（pkg/namefmt 用 atomic 存，随时可切）。
	namefmt.Set(cfgMgr.Get().FilenameFormat)
	cfgMgr.OnUpdate(func(c config.AppConfig) { namefmt.Set(c.FilenameFormat) })

	// 2.5 在线解析池 + musicdl sidecar（可选，默认关）
	//
	// 解析池在这里**显式**建好并交给拦截层，而不是让拦截层自己建：sidecar 的解析器
	// 要注册进这个池子（见 musicdl_wiring.go），两边必须是同一个池。
	//
	// 原生解析器（网易 / QQ）照旧在这里注册 —— 外挂进程只是**多几个平台**，
	// 不是替换它们。
	pool := online.NewPool(online.NetEaseResolver{}, online.QQResolver{})
	mdl := newMusicDLWiring(pool, *dataDir, log.Printf)
	mdl.Apply(cfgMgr.Get())    // 开机按配置决定起不起
	cfgMgr.OnUpdate(mdl.Apply) // 开关一翻就热切换（不用重启应用）
	defer mdl.Stop()           // 退出时停进程：sidecar 是子进程，不主动收就成孤儿

	// 服务端洛雪宿主（goal-a5eb2a23 ⑤ 方案 b）—— 缺省关。
	//
	// ⚠️ Manager 在这里先建出来（而不是等到下面那个块），因为拦截层的配置里要用
	// 它的方法值：洛雪音源脚本的搜索也要进下载源池（见 pkg/intercept/lxsource.go）。
	// **起停仍由下面那个块负责** —— 这里只是拿着句柄，不 Apply。
	lxn := lxnode.New()

	// 3. 音乐入口接管（可选，默认关闭）
	//
	// 开启后本应用会监听飞牛官方音乐的 Unix socket，除自己处理的路径外全部透传。
	// 默认关闭是因为它让本应用成为官方音乐的必经之路 —— 进程崩了音乐就打不开，
	// 所以只有明确设了 QULV_TAKEOVER=1 才启用。
	//
	// 三种情况都会安全退出，不阻塞启动：
	//   - 官方 daemon 还没起来（开机竞态）→ 在预算内等它出现，超时则放弃接管；
	//   - 已经有别的扩展在代理（例如 fnmusic-ext）→ 自动让路，不抢位置；
	//   - socket 被陌生进程占着 → 拒绝接管并说明原因。
	// 前一次接管崩在半路时，这里会先按 journal 把官方还原，再重新接管。
	// 「自动下载记录」：飞牛音乐页面里收藏 / 加歌单 / 点「下载到曲率」触发的那批下载
	// 的流水（见 pkg/online/autolog.go）。
	//
	// 在这里建、而不是在拦截层里建：它**不管接管开没开都要存在** —— 接管关着时
	// 一条记录都不会产生，但接口仍要能回一个空列表，而不是 500。
	// （「没有记录」与「功能没起来」是两件事，不该长得一样。）
	autoLog, autoLogErr := online.NewAutoLog(filepath.Join(*dataDir, "online"))
	if autoLogErr != nil {
		log.Printf("[INIT] 自动下载记录打不开（本次不记流水）：%v", autoLogErr)
		autoLog = nil
	}
	// 包一层而不是直接传 Append：Append 会返回写盘错误，而记录是「收藏」的
	// 附加产物 —— 写不进去不该让下载看起来失败，但也不能悄无声息（下次真查
	// 「记录怎么少了一条」时，这行日志就是唯一的线索）。
	var autoLogAppend func(online.AutoLogEntry)
	if autoLog != nil {
		autoLogAppend = func(e online.AutoLogEntry) {
			if err := autoLog.Append(e); err != nil {
				log.Printf("[INTERCEPT] 自动下载记录写盘失败：%v", err)
			}
		}
	}

	var tk *takeover.Manager
	// ic 提到 if 外面：优雅关闭时要调它的 CloseTee（收掉在途的后台续传）。
	var ic *intercept.Interceptor
	// tkErr 非 nil 表示「接管开了但没接上」，交给状态接口如实报告。
	var tkErr error
	if takeover.Enabled() {
		opts := takeover.DefaultOptions(*dataDir)

		// 拦截层：接管生效后，官方客户端的每个请求都先经过它。它只认自己
		// 登记过的在线曲目（搜索合并、在线取流、在线收藏/歌单/历史），其余
		// 一律交回透传。所以就算它整个坏掉，表现也只是「在线曲库消失」，
		// 官方音乐本身照常 —— 这是它必须守住的失效模式。
		//
		// Upstream 指向「让路后的官方 socket」：拦截层要读官方原始响应才能合并。
		ic = intercept.New(intercept.Config{
			DataDir:  filepath.Join(*dataDir, "online"),
			Upstream: takeover.UnixClient(opts.Upstream),
			Logf:     log.Printf,
			// 同源通道要用的本机后端地址：注入到官方页面里的脚本发起的请求
			// 先到这里，再由拦截层转给本机后端（见 pkg/intercept/qulvbridge.go）。
			// 用回环地址，且**不接受请求里的 host** —— 这条通道不引入 SSRF 面。
			LocalAPI: fmt.Sprintf("http://127.0.0.1:%d", *port),
			// 注入脚本标签上的版本串（见 pkg/intercept/pageshell.go）：
			// 对着官方页面源码就能看出「这行脚本是哪个版本发的」。
			Revision: api.CurrentVersion,
			// 榜单歌单勾了哪些榜：**每次现取**（见 pkg/intercept/vchart.go）。
			// 传值就等于把启动瞬间的配置固定住，榜单管理页改完得重启才生效。
			ChartPlaylists: func() []string { return cfgMgr.Get().ChartPlaylists },
			// 总开关：`ChartsInjectEnabled()` 里含「没设置过 = 开」的口径，
			// 别在这里再判一次 nil（两处判空迟早会不一致）。
			ChartPlaylistsEnabled: func() bool { return cfgMgr.Get().ChartsInjectEnabled() },
			// 页面注入的一键退路（见 pkg/intercept/pageshell.go）：关掉之后
			// 官方页面由接管层反向代理逐字节原样透传，与没做注入时完全一致。
			UIInjectEnabled: func() bool { return cfgMgr.Get().InjectUIEnabled() },
			// 试听的音质偏好（见 pkg/intercept/playback_quality.go）。现取：
			// 在飞牛页面里点了切换，**下一首歌**就按新档位取，不用重启应用。
			PlaybackQuality: func() string { return cfgMgr.Get().PlaybackQuality },
			// 「自动下载记录」的落点与「飞牛页面触发的下载」的音质偏好（见
			// pkg/intercept/autodownload.go）。两者都是现取：用户在界面上改了
			// 「音质下载」，下一次收藏就按新档位取。
			AutoDownloadLog: autoLogAppend,
			// 取的是「开关套上去之后」的档位，不是原始的 auto_download_quality ——
			// 「下载音质」开关（下载音质：跟随 / 无损 / 320k）是**盖住**它的，
			// 套不套这一步必须只有一个地方做（见 config.QualityOverride）。
			AutoDownloadQuality: func() string {
				c := cfgMgr.Get()
				return config.QualityOverride(c.AutoDownloadQuality, c.DownloadQualitySetting())
			},
			// 「自动下载源」的允许集与顺序（见 pkg/config/channels.go）。现取：
			// 用户在界面上勾掉某条渠道，下一次收藏就不碰它，不需要重启。
			AutoDownloadChannels: func() []string { return cfgMgr.Get().DownloadChannels() },
			// 写回配置（同源通道的切换按钮用它）。只写这一个键 ——
			// ConfigManager.Update 是选择性合并，空字段不会被碰，所以不会顺手动到别的设置。
			SetPlaybackQuality: func(q string) error {
				return cfgMgr.Update(config.AppConfig{PlaybackQuality: q})
			},
			// 注入脚本的「开发覆盖」：文件存在时优先发它（见 qulvbridge.go）。
			// 注入界面只能在官方页面里看效果，而这个文件平时是编译进二进制的
			// —— 改一行要重打包 + 重装应用（约两分钟）。留这个口子，改完刷新
			// 官方页面就能看到。文件不存在 = 走嵌入的那份，生产行为不变。
			UIDevFile: filepath.Join(*dataDir, "ui.dev.js"),
			// 收藏 / 加入歌单 → 自动下载并绑定本地（默认关）。
			// 现取：用户在界面上打开就该立刻生效。
			FavAutoDownload: func() bool { return cfgMgr.Get().FavAutoDownloadOn() },
			// 下载时要不要带歌词 / 内嵌封面（见 pkg/intercept/autodownload.go）。
			LyricAutoDownload: func() bool { return cfgMgr.Get().LyricAutoDownloadOn() },
			CoverEmbed:        func() bool { return cfgMgr.Get().CoverEmbedOn() },
			// 「边听边下」与它的暂存落点（见 pkg/intercept/tee.go）。
			TeeEnabled:  func() bool { return cfgMgr.Get().TeeOn() },
			DownloadDir: func() string { return cfgMgr.Get().DownloadDir },
			// 缓存/暂存目录（空 = 用默认位置）。现取：改了下一首歌就生效。
			CacheDir: func() string { return cfgMgr.Get().CacheDirPath() },
			// 「每日推荐的大模型层」的用户同意开关（见 pkg/intercept/vdaily_llm.go）。
			//
			// 现取：用户打开就该立刻生效。**这里只有这一个字段** ——
			// 「有没有模型可调」是另一回事，走下面那个 SetRecommendLLM 注入，
			// 不在这里判（两处判同一条判断迟早不一致）。
			DailyLLMEnabled: func() bool { return cfgMgr.Get().DailyLLMOn() },
			// 「下载源池」= 当前启用的外挂平台。下载时先去池里找同名曲目挑最高音质，
			// 找不到才回落用曲目自己平台的直链（见 pkg/intercept/acquire.go）。
			// 外挂音源关着时返回空 → 池不起作用，行为与以前一致。
			DownloadSources: func() []string {
				if !cfgMgr.Get().MusicDLOn() {
					return nil
				}
				return cfgMgr.Get().MusicDLActiveSources()
			},
			// 解析池：**同一个池**，这样外挂平台（musicdl）与原生平台
			// （网易 / QQ）在拦截层眼里没有区别。
			// 洛雪音源脚本的搜索也进池（见 pkg/intercept/lxsource.go）。
			//
			// 传的是宿主的方法值，不在这里判开关：宿主没起来时 SearchAll 返回
			// lxnode.ErrNotRunning，拦截层把它当「源没开」**静默跳过** ——
			// 所以「缺省关」这条不需要在两处各判一次（两处判空迟早会不一致）。
			LxSearch: lxn.SearchAll,
			// 取链那一半（2026-10-08 补）：池里 `lx:<src>` 的解析器调它。
			// 在这之前只注了 SearchAll —— 洛雪候选能进池、却取不到直链。
			LxResolve: lxn.Resolve,
			// 轮询那一半（2026-10-21 加）：用户勾了好几个洛雪音源时，
			// 「挨个问、谁给的高用谁」——候选整批交回来，由拦截层按实测档位挑。
			LxResolveAll: lxn.ResolveCandidates,
			// 选中的洛雪音源脚本名（顺序 = 用户排的轮询顺序）。
			//
			// 是函数不是切片：用户在设置页改完勾选，下一次取链就该按新名单走。
			// 名字要与 pkg/lxnode/sync.go 落盘时用的键一致（`<Name>.js`），
			// 否则宿主匹配不到，会直接回「选中的音源在宿主里不存在」。
			LxSourceNames: func() []string {
				c := cfgMgr.Get()
				active := map[string]bool{}
				for _, id := range c.ActiveIDs() {
					active[id] = true
				}
				out := make([]string, 0, len(c.CustomSources))
				seen := map[string]bool{}
				for _, cs := range c.CustomSources {
					// 与落盘同一口径：active 为空（老配置）时全算选中。
					if len(active) > 0 && !active[cs.ID] {
						continue
					}
					if strings.TrimSpace(cs.Script) == "" || strings.TrimSpace(cs.Name) == "" {
						continue
					}
					// 文件名必须用**落盘那套**转换（lxnode.ScriptFileName）：
					// 同名脚本会落成同一个文件（真机上就有两个都叫「全豆要[聚合音源]」），
					// 所以顺手去重 —— 重复问同一个文件没有意义，只是多等一轮。
					file := lxnode.ScriptFileName(cs.Name)
					if seen[file] {
						continue
					}
					seen[file] = true
					out = append(out, file)
				}
				return out
			},
			Pool: pool,
			// 参与「官方页面搜索合并」的平台 = 内置默认（网易 + QQ）
			// ∪ 当前启用的外挂平台。
			//
			// 是函数不是切片：外挂平台由开关决定，开关一开就该出现在飞牛页面的
			// 搜索里（用户不会想到「还要重启一下」）。
			PlatformsFunc: func() []string {
				out := intercept.DefaultPlatforms()
				if cfgMgr.Get().MusicDLOn() {
					out = append(out, cfgMgr.Get().MusicDLActiveSources()...)
				}
				return dedupePlatforms(out)
			},
		})
		if ic != nil {
			opts.Interceptor = ic
		}

		tk = takeover.New(opts)
		if err := tk.Enable(); err != nil {
			// 接管失败不是致命错误：本应用自己的功能全部照常，只是官方入口没被接管。
			// 原因要留着交给 /api/takeover/status —— 否则「失败了」和「没开」在界面上
			// 长得一样，排查时会去查环境变量（真机上就白跑过一趟）。
			log.Printf("[TAKEOVER] 未接管音乐入口：%v", err)
			tkErr = err
			tk = nil
		}
	}

	// 4. Initialize HTTP API Server
	server := api.NewServer(cfgMgr, sourcesMgr, staticFS)

	// 每日推荐的大模型层：把「问模型要候选」这条路注进拦截层
	// （见 pkg/intercept/vdaily_llm.go）。
	//
	// ⚠️ 只能**这里**注，不能在构造拦截层时注：AI 客户端由 server 持有，
	// 而拦截层在 `api.NewServer` 之前就建好了。
	// ⚠️ 必须在开始服务之前注（下面 httpServer.Serve 之前）—— 它写的是拦截层的
	// 配置，服务期间那份配置会被并发读。
	//
	// 注进去**不等于**这一层就生效：还要用户在设置里打开 `daily_llm_enabled`
	// （那是对「把收听历史发给大模型」这件事的明确同意），见 Config.DailyLLMEnabled。
	if ic != nil {
		ic.SetRecommendLLM(server.RecommendCandidates)
	}
	// 自动下载记录表交给接口层**读**（写入方是拦截层，见 pkg/api/autodownload.go）。
	// 无论接管开没开都要交：接管关着时它只是一张空表，接口照常回空列表。
	server.SetAutoLog(autoLog)
	// 状态与客户端都传**函数**：客户端会在配置变化时重建，构造时抓一份
	// 就等于永远指向旧的那个（见 pkg/api/musicdl.go 的说明）。
	server.SetMusicDL(mdl.Status, mdl.Client)
	// 界面上的「重试启动」= 拿当前配置再 Apply 一次（配置没变就不会重建，只拉起进程）。
	server.SetMusicDLRestart(func() { mdl.Apply(cfgMgr.Get()) })
	// 「试运行」：临时拉起一个**未启用**的源给用户看（不写配置、不进搜索，
	// TTL 到点自动回收），见 musicdl_wiring.go 的 StartPreview。
	server.SetMusicDLPreview(mdl.StartPreview, mdl.PreviewStatus, mdl.StopPreview)

	// 「替换音质」：扫曲库 → 逐首探各取音渠道实际可得档位 → 用最高档位覆盖旧文件。
	// 四个动作都接在拦截层上（取音计划与自动下载共用同一套，见 pkg/intercept/quality.go）。
	//
	// ⚠️ 必须判 `ic != nil`：拦截层只在**接管生效**时才建（见上面的
	// `if takeover.Enabled()`）—— 没接管时 ic 是 nil，直接取方法值会拿到
	// 「nil 接收者」的方法，一调用就 panic（2026-10-21 真机踩过：
	// dev 后端没开接管，点一下扫描接口就是 500 + 一段 nil dereference）。
	if ic != nil {
		server.SetQuality(
			ic.StartQualityScan,
			ic.QualityScanStatus,
			ic.StopQualityScan,
			func(r *http.Request, paths []string) (any, error) {
				done, failed, err := ic.ReplaceQuality(r.Context(), paths)
				if err != nil {
					return nil, err
				}
				return map[string]any{"done": done, "failed": failed}, nil
			},
		)
	}

	// 服务端洛雪宿主（goal-a5eb2a23 ⑤ 方案 b）—— 缺省关。
	// 它与 musicdl 的 sidecar 是两回事：那个跑的是 Python 音乐平台客户端，
	// 这个跑的是用户自己的洛雪音源脚本。同样是「挂了不影响曲率」：Resolve 只返回错误。
	{
		c := cfgMgr.Get()
		// 先把用户脚本正文落盘 —— 宿主是 Node 子进程，只会读 `<Dir>/*.js`，
		// 而脚本正文存在配置里（前端要拿它去浏览器执行）。
		// ⚠️ 少了这一步，宿主起来也是**零脚本**（2026-10-08 实测确认过），
		// 洛雪那条路静默不可用 —— 见 pkg/lxnode/sync.go 顶部。
		scripts := map[string]string{}
		active := map[string]bool{}
		for _, id := range c.ActiveSourceIDs {
			active[id] = true
		}
		for _, cs := range c.CustomSources {
			// 只落**启用中**的那些：池里该出现的是用户勾选过的源。
			// active 为空（老配置没这个字段）时全落 —— 与「没设置过 = 全用」同口径。
			if len(active) > 0 && !active[cs.ID] {
				continue
			}
			if strings.TrimSpace(cs.Script) == "" {
				continue
			}
			scripts[cs.Name] = cs.Script
		}
		lxDir := filepath.Join(*dataDir, "lx_sources")
		if err := lxnode.SyncScripts(lxDir, scripts); err != nil {
			// 落盘失败**不让应用起不来**：洛雪那条路用不了而已，
			// 其余功能（含 musicdl 那条腿）照常。
			log.Printf("[INIT] 洛雪脚本落盘失败（服务端洛雪将不可用）：%v", err)
		} else {
			log.Printf("[INIT] 服务端洛雪脚本已同步 %d 个 → %s", len(scripts), lxDir)
		}
		lxn.Apply(lxnode.Config{
			Enabled: c.LXServerOn(),
			NodeBin: "node",
			// 复用 sidecarSourceDir 的定位逻辑（生产在可执行文件旁、开发在仓库根），
			// 它给的是 sidecar/musicdl_service，取其父目录再进 lx_host。
			Script: filepath.Join(filepath.Dir(sidecarSourceDir()), "lx_host", "server.mjs"),
			Dir:    lxDir,
			Port:   c.LXServerPort,
		})
		// 宿主起来没有、为什么没起来 —— **必须记一笔**。
		// 这个能力的失败以前是静默的（只写进 lxnode 的 stderr），于是
		// 「洛雪源一次都没参与过下载」查不到根因。现在启动后立刻问一次状态。
		if st := lxn.Status(); st["state"] != lxnode.StateReady {
			log.Printf("[INIT] 服务端洛雪宿主未就绪：state=%v err=%v（脚本目录 %s）",
				st["state"], st["error"], lxDir)
		} else {
			log.Printf("[INIT] 服务端洛雪宿主就绪：%v", st["base"])
		}
	}
	defer lxn.Stop()
	server.SetTakeover(tk)
	server.SetTakeoverError(tkErr)
	// 启动歌单监控调度（轮询式，重启后状态天然正确，不会因错过时刻丢任务）
	server.StartScheduler()

	// 把生效的鉴权模式说清楚：「没设 Token = 完全放开」这件事以前是静默的，
	// 端口被映射到公网也不会有人知道。见 pkg/api/middleware.go 顶部的「鉴权模式」。
	log.Printf("[AUTH] %s", server.AuthSummary())

	httpServer := &http.Server{
		Handler: server.Router(),
		// 防御 Slowloris：限制读取请求头的耗时
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// 写超时置 0：本地音频串流是长连接，整体写超时会中断正在播放的音频。
		// 连接回收由 IdleTimeout 与请求上下文负责。
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		// Fallback to IPv4 if dual-stack failed
		listener, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", *port))
		if err != nil {
			log.Fatalf("[FATAL] Failed to bind to port %d: %v", *port, err)
		}
	}

	// 后台「音质补强」轮：把「收藏时各渠道都没给无损」的那批歌，隔天再试一遍。
	//
	// ⚠️ 必须放在 `httpServer.Serve` **之后**启动（虽然它第一轮要等 20 分钟才动手）：
	// 补强最终会走 autoDownload → postDownload，那是**从进程里打回自己 HTTP 接口**的
	// 一条路（`localAPI`）。服务还没 listen 就起循环，第一轮的自调用会全部连不上，
	// 而这些失败是记冷却位的 —— 等于白等一天。
	if ic != nil {
		ic.StartUpgradeSweeper()
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("[READY] Server running and listening on http://0.0.0.0:%d", *port)
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if tk != nil {
			// 进程要退出了，先把官方 socket 交还回去，再谈错误上报。
			if cerr := tk.Close(); cerr != nil {
				log.Printf("[TAKEOVER] 还原官方 socket 失败: %v", cerr)
			}
		}
		log.Fatalf("[FATAL] HTTP server error: %v", err)
	case <-quit:
	}

	log.Println("[SHUTDOWN] Stopping 曲率...")

	server.StopScheduler()

	// 收掉「边听边下」在途的后台续传：取消 + 等它们把半截删掉（最多几秒）。
	// 不调也能退（进程会带走 goroutine），但会留下一堆 `.part`，要等下次启动
	// 清扫而且只在超过 1 小时之后才会被清 —— 见 pkg/intercept/tee.go 的 CloseTee。
	// 放在还 socket 之前：它是纯本地动作，不依赖官方 daemon 还在不在。
	if ic != nil {
		// 先停补强轮：它会在途起 goroutine 打自己的接口，服务已经开始收尾时别再加活。
		ic.CloseUpgradeSweeper()
		ic.CloseTee()
	}

	// 先还 socket，再关 HTTP。
	//
	// 顺序很重要：接管层的 Close 会停掉自己的监听并把官方挪回原位。
	// 如果先关 HTTP 再还 socket，中间那段时间官方音乐仍然是打不开的
	// （socket 还被我们占着，但我们的服务已经不响应了）。
	if tk != nil {
		if err := tk.Close(); err != nil {
			log.Printf("[TAKEOVER] 还原官方 socket 失败: %v", err)
		} else {
			log.Println("[TAKEOVER] 已交还官方音乐入口")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[SHUTDOWN] Graceful shutdown timed out, forcing close: %v", err)
		_ = httpServer.Close()
	} else {
		log.Println("[SHUTDOWN] All connections drained, bye.")
	}
}
