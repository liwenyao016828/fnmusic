package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/appupdate"
	"fn-lx-player/pkg/config"
)

// 应用内更新（检查 / 下载 / 安装）。
//
// 为什么会话状态放在 Server 上而不是每次请求新建：下载进度与「安装中」是
// 进程级状态，见 pkg/appupdate 的说明。这里的处理器只做三件事：
// 校验方法、转调 Manager、按接口契约包一层 JSON。

func (s *Server) appUpdateManager() *appupdate.Manager {
	if s == nil {
		return nil
	}
	return s.appUpdate
}

// HandleAppUpdateCheck 检查 GitHub 上的最新 release。
//
//	GET /api/app/update/check[?refresh=1]
//
// 默认走 10 分钟缓存；refresh=1 强制打 GitHub（界面上的「重新检查」按钮）。
// 网络错误也返回 code=200：data.error 有值即表示检查失败，current 仍然可用，
// 这样前端能同时显示「你现在是 2.18.1」和「检查失败：…」。
func (s *Server) HandleAppUpdateCheck(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	refresh := false
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh"))) {
	case "1", "true", "yes":
		refresh = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), appupdate.CheckTimeout)
	defer cancel()
	res := m.Check(ctx, refresh)
	// 检查完顺手对一下磁盘：这个版本的 fpk 如果已经下过了（上次下好没来得及
	// 装、或者进程重启丢了内存状态），直接把状态推进到 ready，界面就能立刻
	// 出现「安装」按钮，不用再等一遍下载。（用户 2026-11-02 第 1 项要求）
	if res.AssetName != "" && res.Latest != "" && m.AdoptDownloaded(res.Latest) {
		st := m.State()
		res.Downloaded = true
		res.DownloadedPath = st.Path
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": res})
}

// HandleAppUpdateDownload 启动后台下载（幂等）。
//
//	POST /api/app/update/download
//
// 不等待下载完成：返回的 data.status 是 downloading，进度用 /state 轮询。
// 已下载完成时重复调用返回 ready 与已有路径，不会重复下载。
func (s *Server) HandleAppUpdateDownload(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	st, err := m.StartDownload(r.Context())
	if err != nil {
		// 下载失败在状态机里也是合法结果（status=error + error 文案），
		// 所以这里仍回 200：前端统一从 data 读状态，不必区分「HTTP 失败」。
		st.Status = appupdate.StatusError
		if st.Error == "" {
			st.Error = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": st})
}

// HandleAppUpdateState 返回下载/安装的当前状态。
//
//	GET /api/app/update/state
func (s *Server) HandleAppUpdateState(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	st := m.State()
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": st})
}

// HandleAppUpdateInstall 写脚本并后台执行自更新。
//
//	POST /api/app/update/install
//
// 403 = 没有 root/免密 sudo（提示用户去应用中心手动装）；
// 409 = 安装包还没下载好；
// 500 = 写脚本/环境文件失败。
// 成功只表示「脚本已启动」——真正的结果在 /tmp/yinshu-update/install.log，
// 而且装完本进程会重启，前端应把它当「请求可能中断」处理。
func (s *Server) HandleAppUpdateInstall(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	res, err := m.Install()
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "安装权限"):
			errJSON(w, http.StatusForbidden, msg)
		case strings.Contains(msg, "还没有下载好"), strings.Contains(msg, "安装包不存在"):
			errJSON(w, http.StatusConflict, msg)
		case strings.Contains(msg, "正在安装"):
			errJSON(w, http.StatusConflict, msg)
		default:
			errJSON(w, http.StatusInternalServerError, msg)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": res})
}

// newAppUpdateManager 构造进程级更新管理器。
//
// AppName 用运行时解析（TRIM_APPNAME / 可执行文件在 /var/apps/<pkg>/ 下 / manifest），
// 而不是直接写 CurrentAppName：开发环境与真机的包名来源不同，解析失败才回退常量，
// 这样同一个二进制在真机和 .dev 下都能跑，且不会「卸载错应用」。
func newAppUpdateManager(cfgMgr *config.ConfigManager) *appupdate.Manager {
	dataDir := ""
	if cfgMgr != nil {
		dataDir = cfgMgr.DataDir()
	}
	appName := CurrentAppName
	if exe, err := os.Executable(); err == nil {
		if n := appupdate.ResolveAppName(exe, os.Getenv); n != "" {
			appName = n
		}
	}
	return appupdate.New(appupdate.Options{
		DataDir:        dataDir,
		AppName:        appName,
		CurrentVersion: CurrentVersion,
		Token:          strings.TrimSpace(os.Getenv("FN_GITHUB_TOKEN")),
		// 个人 token（用户 2026-10-25）：用函数而不是构造时读一次，
		// 这样在「系统 → 设置」里填完，下一次检查/下载就生效，不必重启应用。
		// 环境变量优先（开发/排障时能临时盖住配置），其次读配置。
		TokenFunc: func() string {
			// 环境变量里的 token 优先：有人把 secret 挂在 FN_GITHUB_TOKEN 上，
			// 不想让它被界面上的一次误改覆盖。
			if v := strings.TrimSpace(os.Getenv("FN_GITHUB_TOKEN")); v != "" {
				return v
			}
			if cfgMgr == nil {
				return ""
			}
			return cfgMgr.Get().UpdateGitHubToken
		},
		// 更新源镜像清单（用户 2026-10-25）：「增加默认镜像站，可删减增加」「可以填自己的」。
		// 同样是函数：在设置里增删镜像后，下一次检查立刻按新清单试。
		MirrorsFunc: func() []config.UpdateMirror {
			if cfgMgr == nil {
				return config.DefaultUpdateMirrors()
			}
			return cfgMgr.Get().UpdateMirrorList()
		},
		// 应用内代理（用户 2026-10-25）：同样是函数，改完下一次请求就生效。
		// 环境变量 FN_UPDATE_PROXY 优先（排障时能临时盖住配置）。
		ProxyFunc: func() string {
			if v := strings.TrimSpace(os.Getenv("FN_UPDATE_PROXY")); v != "" {
				return v
			}
			if cfgMgr == nil {
				return ""
			}
			return cfgMgr.Get().UpdateProxyURL()
		},
		// FN_UPDATE_API 换掉「去哪里查新版本」的整条地址（不只是仓库名）：自建
		// GitHub 镜像/内网发布源用得上，也让这台开发机能拿本地假源做端到端验证
		// —— 共享代理出去的匿名 GitHub 请求经常直接 403 限速，真机上也可能遇到。
		APIURL: strings.TrimSpace(os.Getenv("FN_UPDATE_API")),
		Logf: func(format string, args ...any) {
			applog.Default().Add(fmt.Sprintf(format, args...))
		},
	})
}

// HandleAppUpdateNotice 返回「更新完成后第一次进入应用」的提示内容。
//
//	GET /api/app/update/notice
//
// 不联网：只读数据目录里的 last_run.json 与当前版本号比一下，启动路径要快。
// 判断规则见 pkg/appupdate/notice.go —— 关键是「版本号变了就提示一次」，
// 覆盖应用内升级、手动装 fpk、应用中心升级（用户 2026-10-25：「变化就提示」）。
func (s *Server) HandleAppUpdateNotice(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	n := m.NoticeNotes(m.Notice())
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": n})
}

// HandleAppUpdateNoticeAck 把「版本变化提示」标记为已展示。
//
//	POST /api/app/update/notice/ack   body: {"version":"2.21.1"}
//
// 必须由用户看到提示后显式调用：不能一读就作废，否则提示还没渲染就被标记已读，
// 用户永远看不到（页面上两个标签页同时开着时尤其明显）。
func (s *Server) HandleAppUpdateNoticeAck(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	ok := m.AckNotice(body.Version)
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": map[string]interface{}{"ok": ok}})
}

// HandleAppUpdateInstallLog 读安装脚本日志的尾部。
//
//	GET /api/app/update/install/log[?lines=40]
//
// 为什么要有它（2026-11-01 用户「下载完成后刷新还是旧版本」）：安装脚本跑在
// 独立会话里，它的失败原因只落在 /tmp/yinshu-update/install.log；界面以前只拿到
// 一个路径字符串，用户既不知道装成功没有、也不知道卡在哪一步。装完应用会重启，
// 但脚本日志在 /tmp 里**不会**随之消失，所以重启后仍然能读出来复盘。
//
// 只读接口：文件不存在时回 ok=false（不是 404），界面据此显示「还没有安装日志」。
func (s *Server) HandleAppUpdateInstallLog(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	lines := 40
	if v := strings.TrimSpace(r.URL.Query().Get("lines")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			lines = n
		}
	}
	tail, ok := m.InstallLogTail(lines)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200,
		"data": map[string]interface{}{
			"ok":    ok,
			"path":  m.InstallLogPath(),
			"tail":  tail,
			"lines": lines,
		},
	})
}

// HandleAppUpdateProxyProbe 测一次「经当前配置的代理能不能真的访问更新源」。
//
//	GET /api/app/update/proxy/probe
//
// 用户 2026-11-02 第 6 项：「若已配置代理，自动测试代理速度；测试成功则优先
// 使用代理，失败则回退到官方源」。检查更新（/check）内部已经会自动做这件事，
// 这个接口是给**界面上的手动重测**用的 —— 用户刚填完代理、或刚把代理软件拉起来，
// 想立刻知道通不通，不必再去点一次「检查更新」等完整结果。
//
// 只读：不改配置、不动下载状态。没配代理时返回 {configured:false}，不是错误。
func (s *Server) HandleAppUpdateProxyProbe(w http.ResponseWriter, r *http.Request) {
	m := s.appUpdateManager()
	if m == nil {
		errJSON(w, http.StatusServiceUnavailable, "更新模块未初始化")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := m.ProbeProxy(ctx)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200,
		"data": res,
	})
}
