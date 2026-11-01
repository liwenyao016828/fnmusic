package api

import (
	"encoding/json"
	"net/http"
)

// 服务端洛雪宿主的状态与开关接口（GET / POST /api/lx/host）。
//
// # 为什么需要它
//
// 宿主是个 Node 子进程（pkg/lxnode），此前只有 `main.go` 启动时 Apply 一次：
// 界面上要么「一直是开着的」，要么「必须重启整个应用才能打开」。用户 2026-10-30
// 的原话是「音源管理底部应该有块『服务端洛雪宿主』并能在里面开启」—— 意思很明确：
// 这是一个**开关**，不是一个只读指示灯。
//
// # 为什么动作在 main 里而不是这里
//
// 「开启」= 写配置 + 把启用中的音源脚本重新落盘 + 起停子进程（见 pkg/lxnode/sync.go）。
// 这三步都要拿到 lxnode.Manager 与 dataDir，而 api 包不该反向依赖 main。
// 所以这里只做 HTTP 皮：解析、调用注入的动作、回状态 —— 与 handleMusicDLRestart 同一套。

// SetLxHost 注入服务端洛雪宿主的状态查询与起停动作。由 main 提供。
//
// `status` 是函数而不是值：状态一直在变（未开启 / 启动中 / 已就绪 / 启动失败），
// 抓一份就等于永远返回启动那一刻的快照。
func (s *Server) SetLxHost(status func() map[string]any, apply func(enabled bool, port int) error) {
	s.lxHostStatus = status
	s.lxHostApply = apply
}

// lxHostState 取一次状态；没接线时回一个「不可用」而不是 nil，
// 免得界面上的开关渲染成永远开着的样子。
func (s *Server) lxHostState() map[string]any {
	if s.lxHostStatus == nil {
		return map[string]any{"available": false, "enabled": false, "state": "off"}
	}
	st := s.lxHostStatus()
	if st == nil {
		st = map[string]any{}
	}
	st["available"] = true
	return st
}

// handleLxHost GET 读状态 / POST 开关。
//
// 一条路径两个方法：它们是同一件事的两个面（看它开没开、把它开或关），
// 拆成两条路径只会让界面和 AI 多记一个名字。
func (s *Server) handleLxHost(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.lxHostState()})
		return
	case http.MethodPost:
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 GET / POST"})
		return
	}

	if s.lxHostApply == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"code":    501,
			"message": "当前后端不支持运行时开关服务端洛雪宿主（只能改配置后重启应用）",
			"data":    s.lxHostState(),
		})
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
		Port    int   `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "请求体不是合法 JSON：" + err.Error()})
		return
	}
	// enabled 必须显式给：缺省成 false 会把「只是改个端口」的调用变成关掉宿主。
	if body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "缺少 enabled（true = 开启宿主，false = 关闭）"})
		return
	}
	if body.Port < 0 || body.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "端口必须在 0-65535 之间（0 = 用默认 8920）"})
		return
	}

	if err := s.lxHostApply(*body.Enabled, body.Port); err != nil {
		// 配置落盘失败这类错误是**真的没生效**，必须让调用方看见原因，
		// 不能回一句「已开启」然后宿主根本没起来。
		writeJSON(w, http.StatusOK, map[string]any{
			"code":    500,
			"message": err.Error(),
			"data":    s.lxHostState(),
		})
		return
	}

	msg := "已开启服务端洛雪宿主"
	if !*body.Enabled {
		msg = "已关闭服务端洛雪宿主"
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "message": msg, "data": s.lxHostState()})
}