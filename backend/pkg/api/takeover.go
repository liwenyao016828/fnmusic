package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"fn-lx-player/pkg/takeover"
)

// 音乐入口接管（pkg/takeover）的对外窗口。
//
// 接管层做的事：把飞牛官方音乐后端的 Unix socket 挪到一边，自己监听原路径，
// 除显式处理的路径外全部透传。它是**可选**能力（环境变量 QULV_TAKEOVER=1），
// 因为一旦接管，本应用就成了官方音乐的必经之路 —— 出问题音乐就打不开了。
//
// 这里只暴露「看状态」和「还原」两个动作，不开「接管」：
// 开启要改环境变量并重启应用，是运维动作；而**还原**必须能从应用内部触发，
// 因为接管层出问题时用户需要一条不依赖 SSH 的退路。
//
// 停止应用时 main.go 也会自动还原（见 tk.Close()），这里的接口是给
// 「进程还活着但想立刻交还官方」这种场景用的。

// SetTakeover 注入接管管理器。为 nil 表示本进程没开启接管（或开启了但没接管成功）。
func (s *Server) SetTakeover(m *takeover.Manager) {
	s.takeoverMgr = m
}

// SetTakeoverError 记录「环境变量开了接管、但这次没接上」的原因。
//
// 必须与 SetTakeover(nil) 区分开：只报 enabled:false 会把「接管失败」伪装成
// 「没开」，让人去查环境变量（往往是白跑）。真机上就撞过一次 ——
// 环境变量明明在，实际是发布那一步 rename 跨了文件系统。
func (s *Server) SetTakeoverError(err error) {
	if err == nil {
		s.takeoverErr = ""
		return
	}
	s.takeoverErr = err.Error()
}

// handleTakeoverStatus 报告官方 socket 现在由谁监听。
//
// 两个路径分别报告形态，因为「接管到一半」是最需要被看见的状态：
// target 是我们、upstream 却不是官方，说明中间出过事。
func (s *Server) handleTakeoverStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.takeoverMgr == nil {
		data := map[string]interface{}{
			"enabled": false,
			"active":  false,
			"note":    "接管未开启。设 QULV_TAKEOVER=1 后重启应用即可启用。",
		}
		if s.takeoverErr != "" {
			// 环境变量是开的，只是这次没接上。enabled 报 true，别把原因藏起来。
			data["enabled"] = true
			data["error"] = s.takeoverErr
			data["note"] = "接管已开启，但本次未能接管官方音乐入口：" + s.takeoverErr
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": data})
		return
	}

	st := s.takeoverMgr.Snapshot()
	data := map[string]interface{}{
		"enabled":        true,
		"active":         st.Active,
		"target":         st.Target,
		"upstream":       st.Upstream,
		"target_state":   string(st.TargetState),
		"upstream_state": string(st.UpstreamState),
		"detail":         st.Detail,
	}
	if st.Yielded != "" {
		data["yielded"] = st.Yielded
		data["note"] = "接管已启用，但检测到其它扩展已经在代理官方音乐入口，本应用按约定让路（不干扰其它扩展）。停用那个扩展后重启本应用即可接管。"
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200,
		"data": data,
	})
}

// handleTakeoverRestore 把官方 socket 挪回原位，交还官方直连。
//
// 失败（尤其上游不是官方）时返回 409 而不是 500：那不是「服务器出错」，
// 而是「当前拓扑不允许安全地做这件事」，调用方该看 message 而不是重试。
func (s *Server) handleTakeoverRestore(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.takeoverMgr == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"code":    409,
			"message": "接管未开启，无需还原。",
		})
		return
	}

	if err := s.takeoverMgr.RestoreNow(); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, takeover.ErrUnsafe) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]interface{}{
			"code":    status,
			"message": "还原失败：" + err.Error(),
		})
		return
	}

	st := s.takeoverMgr.Snapshot()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200,
		"data": map[string]interface{}{
			"restored":       true,
			"target_state":   string(st.TargetState),
			"upstream_state": string(st.UpstreamState),
		},
	})
}
