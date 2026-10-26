package api

// 飞牛 fnOS 集成接口。
//
// 设计说明：本项目的首要用途是供其他 AI / 外部程序调用，因此这些能力以 HTTP
// 接口形式暴露，不依赖前端页面。前端按需调用其中一部分即可。
//
// 涉及两套飞牛接口，注意区别：
//   - 官方开放 API（pkg/fnos 的 Call/SharedFolders）：认证用 Bearer TRIM_API_TOKEN，
//     由系统自动注入，可读取管理员授权目录。
//   - 飞牛音乐应用接口（pkg/fnos 的 Music）：认证用音乐应用的登录令牌（裸令牌），
//     可读写歌单与曲库。
//
// 推送相关逻辑在 pkg/push，本文件只做 HTTP 适配。

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/push"
)

// HandleFnosStatus 查询飞牛集成状态
//
// GET /api/fnos/status
//
// ⚠️ **对外只给布尔与枚举，不给内容**（2026-10-25 安全整改）。
// 这个接口是老接口，靠「内网免校验」跑了很多版本（默认 auth_mode=lan），
// 而它原来把管理员授权目录的绝对路径、飞牛账号名、上游报错原文一起吐出来 ——
// 同网段的任何设备扫一下就能拿到你这台 NAS 的目录结构与账号，用来做后续的路径探测。
//
// 现在按「谁在问」分两档：
//   - 本机回环（= 用户自己开着 App 页面，iframe 就来自 /var/apps 下的代理/浏览器同机）
//     → 还带 shared_folders / music_account 这些**用于界面展示**的内容；
//   - 其它来源（同网段、外网）→ 只给布尔与枚举（token_source 本身就是枚举，界面要用）。
//
// 判断依据是 TCP 对端地址，不是请求头，改不了。
func (s *Server) HandleFnosStatus(w http.ResponseWriter, r *http.Request) {
	openAPI := fnos.Available()
	musicReady, account, accountErr := fnos.MusicStatus()
	status := map[string]interface{}{
		"open_api_available": openAPI,
		"music_available":    musicReady,
		// 令牌来源（manual / env / db / none）—— 「飞牛同步管理」页要展示这个，
		// 让用户知道当前用的是手工令牌还是自动读取的。它是**枚举**，不含令牌本身。
		"token_source": fnos.TokenSource(),
	}

	detailed := isLocalRequest(r)

	// 官方开放 API：管理员授权目录（仅本机可见）
	if openAPI {
		if folders, err := fnos.SharedFolders(); err == nil {
			if detailed {
				status["shared_folders"] = folders
			} else {
				// 数量本身够界面用（「已授权 3 个目录」），路径不给。
				status["shared_folder_count"] = len(folders)
			}
		} else if detailed {
			status["shared_folders_error"] = err.Error()
		}
	}

	// 飞牛音乐应用接口：账号（仅本机可见，且只回名字与角色）
	if musicReady {
		if accountErr == nil {
			if detailed {
				status["music_account"] = account
			} else {
				status["music_account_ready"] = true
			}
		} else if detailed {
			status["music_error"] = accountErr.Error()
		}
	}

	hint := "未检测到飞牛环境属正常现象：本地开发时 open_api_available 与 music_available 均为 false，其余功能不受影响。"
	if !musicReady {
		// ⚠️ 这段会**原样显示在界面上**，只写「缺什么 + 你该怎么做」：
		// 不写库路径 / socket 路径 / 接口地址，也不写 Markdown 标记（Vue 不解析，会显示成字面量星号）。
		// 逐项诊断结果走日志，见下面的 log.Printf 与 fnos.UnavailableDetail()。
		reason := fnos.UnavailableReason()
		if reason == "" {
			reason = "原因未知"
		}
		hint = reason + "。请确认飞牛音乐已在 fnOS 上登录，然后点「检查连接」重试。"
		if detail := fnos.UnavailableDetail(); detail != "" {
			log.Printf("[fnos] 飞牛音乐不可用，逐项检查：%s", detail)
		}
	}
	status["hint"] = hint

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": status,
	})
}

// isLocalRequest 报告请求是否来自本机回环。
//
// 用途：决定「这条应答能不能带内容」。它只认 TCP 对端地址（RemoteAddr），
// 不看任何请求头 —— X-Forwarded-For / X-Real-IP 是客户端随便写的，拿它做判断
// 等于把上面那层甄别直接拱手让人。
//
// 为什么不顺便认内网：本期先收窄到回环。App 自己的界面由飞牛桌面 iframe 承载，
// 与后端同机；而「同网段某台设备」恰恰是这次要防的那一方。语义收紧的方向是安全的
// （最多是外部来源少看到几个字段），反过来放宽才是风险。
func isLocalRequest(r *http.Request) bool {
	return isLoopbackIP(clientIP(r))
}

// HandleFnosFolders 查询管理员授权给本应用的目录（官方开放 API）
//
// GET /api/fnos/folders
func (s *Server) HandleFnosFolders(w http.ResponseWriter, r *http.Request) {
	if !fnos.Available() {
		errJSON(w, http.StatusServiceUnavailable,
			"飞牛开放 API 不可用：非飞牛环境，或应用未声明 trim.file.sharedAccess")
		return
	}
	folders, err := fnos.SharedFolders()
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"folders": folders, "count": len(folders)},
	})
}

// HandleFnosPlaylists 列出飞牛音乐的歌单
//
// GET /api/fnos/playlists
func (s *Server) HandleFnosPlaylists(w http.ResponseWriter, r *http.Request) {
	music := fnos.NewMusic("")
	if !music.Available() {
		errJSON(w, http.StatusServiceUnavailable, "飞牛音乐不可用：未找到可用登录令牌或音乐服务未运行")
		return
	}
	playlists, err := music.Playlists()
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"playlists": playlists, "count": len(playlists)},
	})
}

type fnosPushTrack = push.Track

type fnosPushRequest struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	CoverURL    string          `json:"cover_url"`
	FnosGUID    string          `json:"fnos_guid"`
	SyncCover   bool            `json:"sync_cover"`
	DryRun      bool            `json:"dry_run"`
	Tracks      []fnosPushTrack `json:"tracks"`
}

// HandleFnosPush 把一批曲目推送为飞牛音乐歌单
//
// POST /api/fnos/push
//
// body:
//
//	{
//	  "title": "我的歌单",            // 必填
//	  "description": "",              // 选填
//	  "cover_url": "",                // 选填，sync_cover=true 时下载并设为歌单封面
//	  "fnos_guid": "",                // 选填，已推送歌单的 guid（稳定身份，避免重名建新单）
//	  "sync_cover": false,            // 选填
//	  "dry_run": false,               // 选填，true 时只返回匹配结果不写入
//	  "tracks": [{"name":"晴天","artist":"周杰伦","duration":269}]
//	}
//
// 匹配策略见 pkg/push：在飞牛曲库按「歌名」与「歌手 + 歌名」两次检索，
// 相似度低于 match.MatchMinScore 视为未命中，绝不张冠李戴。
func (s *Server) HandleFnosPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req fnosPushRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	music := fnos.NewMusic("")
	if !music.Available() {
		errJSON(w, http.StatusServiceUnavailable, "飞牛音乐不可用：未找到可用登录令牌或音乐服务未运行")
		return
	}

	res, err := push.PushTracks(music, req.Tracks, push.Options{
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		CoverURL:    req.CoverURL,
		FnosGUID:    strings.TrimSpace(req.FnosGUID),
		SyncCover:   req.SyncCover,
		DryRun:      req.DryRun,
		// 本地曲库兜底：用户「先下载、再同步歌单」时，这些歌不该被报成缺（2026-10-23）
		LocalIndex: s.localTrackSnapshot(),
	})
	if err != nil {
		// 参数类错误用 400，其余按上游失败处理
		if strings.Contains(err.Error(), "请提供") {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": res,
	})
}
