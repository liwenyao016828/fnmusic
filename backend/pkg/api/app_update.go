package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

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
		// FN_UPDATE_API 换掉「去哪里查新版本」的整条地址（不只是仓库名）：自建
		// GitHub 镜像/内网发布源用得上，也让这台开发机能拿本地假源做端到端验证
		// —— 共享代理出去的匿名 GitHub 请求经常直接 403 限速，真机上也可能遇到。
		APIURL: strings.TrimSpace(os.Getenv("FN_UPDATE_API")),
		Logf: func(format string, args ...any) {
			applog.Default().Add(fmt.Sprintf(format, args...))
		},
	})
}
