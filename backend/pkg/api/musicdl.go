package api

import (
	"encoding/json"
	"net/http"

	"fn-lx-player/pkg/online"
)

// musicdl sidecar 的状态接口（v2.1.83）。
//
// 为什么要有它：sidecar 的启动是**异步且可能失败**的（首次要建 venv + 装依赖，
// 一两分钟；装不上就永远起不来）。没有这个接口，用户看到的只是「多了几个平台但
// 搜不到东西」，没有任何线索 —— 而这正是「装依赖失败」和「平台被风控」两种
// 完全不同的原因会长得一模一样的地方。
//
// 状态里**必然**包含 sidecar 的 state（off/preparing/starting/ready/failed）
// 与 error，界面据此说清「现在是什么情况、下一步该做什么」。

// SetMusicDL 注入状态查询与 sidecar 客户端。由 main 提供（这里不 import main，
// 避免反向依赖）。
//
// `client` 是**函数**而不是值：sidecar 的客户端在配置变化时会被重建
// （换平台/换端口），构造时抓一份就等于永远指向旧的那个。
func (s *Server) SetMusicDL(status func() map[string]any, client func() *online.MusicDL) {
	s.musicdlStatus = status
	s.musicdlClientFn = client
}

// SetMusicDLRestart 注入「重试启动」的动作（见 handleMusicDLRestart）。
//
// 与 SetMusicDL 分开是为了不动它的签名 —— 那边两个函数是「读状态」，这边是「做动作」，
// 混在一起会让 main 的调用点变成三个参数、且语义不清。
func (s *Server) SetMusicDLRestart(fn func()) { s.musicdlRestart = fn }

// musicdlUnavailable 是「外挂音源没开 / 没起来」时的统一应答。
//
// 统一成一处：三个 handler 各自编一份文案的话，界面会同时看到三种说法。
func musicdlUnavailable(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 502,
		"msg":  msg,
		"data": map[string]any{"available": false},
	})
}

// handleMusicDLSources 返回 musicdl 注册的全部源（真机 56 个）与启用状态。
func (s *Server) handleMusicDLSources(w http.ResponseWriter, r *http.Request) {
	c := s.musicdl()
	if c == nil {
		musicdlUnavailable(w, "外挂音源没开（到「音源管理」里打开）")
		return
	}
	list, err := c.Sources(r.Context())
	if err != nil {
		musicdlUnavailable(w, "sidecar 没响应："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": list})
}

// handleMusicDLProbe 让 sidecar 对指定源各做一次真实搜索（耗时 + 条数）。
func (s *Server) handleMusicDLProbe(w http.ResponseWriter, r *http.Request) {
	c := s.musicdl()
	if c == nil {
		musicdlUnavailable(w, "外挂音源没开（到「音源管理」里打开）")
		return
	}
	var body struct {
		Sources []string `json:"sources"`
		Keyword string   `json:"keyword"`
		Limit   int      `json:"limit"`
	}
	if r.Body != nil {
		// 空 body 也要能跑（用当前启用的源、默认关键词）
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	}
	results, err := c.Probe(r.Context(), body.Sources, body.Keyword, body.Limit)
	if err != nil {
		musicdlUnavailable(w, "检测失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200,
		"data": map[string]any{"keyword": body.Keyword, "results": results},
	})
}

// musicdl 取当前客户端（nil = 没开或还没起来）。
// handleMusicDLRestart 让用户从界面上「重试启动」外挂进程。
//
// 为什么需要它：sidecar 会因为**瞬时**原因挂掉（被 OOM 杀掉、venv 装到一半断网、
// 端口被占），而界面此前只能显示「启动失败：<原因>」—— 用户没有任何办法重试，
// 只能去改一个音源开关（那会顺带触发一次 Apply）或者重启整个应用。
//
// 语义就是「拿当前配置再 Apply 一次」：配置没变时不会重建，只把挂掉的进程重新拉起。
func (s *Server) handleMusicDLRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.musicdlRestart == nil {
		musicdlUnavailable(w, "外挂音源没启用")
		return
	}
	s.musicdlRestart()
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "message": "已触发重启", "data": s.musicdlStatus()})
}

func (s *Server) musicdl() *online.MusicDL {
	if s.musicdlClientFn == nil {
		return nil
	}
	c := s.musicdlClientFn()
	if c == nil || c.Base() == "" {
		return nil
	}
	return c
}

// handleMusicDLStatus 返回 sidecar 的当前状态。
func (s *Server) handleMusicDLStatus(w http.ResponseWriter, r *http.Request) {
	if s.musicdlStatus == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200,
			"data": map[string]any{"available": false, "enabled": false},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.musicdlStatus()})
}
