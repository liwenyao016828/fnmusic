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
func (s *Server) HandleFnosStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"open_api_available": fnos.Available(),
	}

	// 官方开放 API：管理员授权目录
	if fnos.Available() {
		if folders, err := fnos.SharedFolders(); err == nil {
			status["shared_folders"] = folders
		} else {
			status["shared_folders_error"] = err.Error()
		}
	}

	// 飞牛音乐应用接口：令牌与账号
	music := fnos.NewMusic("")
	status["music_available"] = music.Available()
	// 令牌来源（manual / env / db / none）—— 「飞牛同步管理」页要展示这个，
	// 让用户知道当前用的是手工令牌还是自动读取的
	status["token_source"] = fnos.TokenSource()
	if account, err := music.Me(); err == nil {
		status["music_account"] = account
	} else {
		status["music_error"] = err.Error()
	}

	hint := "未检测到飞牛环境属正常现象：本地开发时 open_api_available 与 music_available 均为 false，其余功能不受影响。"
	if !music.Available() {
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
