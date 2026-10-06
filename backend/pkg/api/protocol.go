package api

import (
	"encoding/json"
	"net/http"

	"fn-lx-player/pkg/protocol"
)

// HandleProtocols 列出当前可用的音源协议，并附带运行时状态。
//
// 这个接口的存在意义：把「项目支持哪些协议、每种协议负责什么」变成**可发现的事实**，
// 而不是散落在前端代码里的隐式约定。外部 AI 与程序据此就能知道：
//   - 不导入任何音源也能用官方协议搜索 / 逛榜单 / 看歌词
//   - 只有「取直链」这一步需要 lx 协议
//
// GET /api/protocols
func (s *Server) HandleProtocols(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	list := protocol.List()

	// 运行时状态：官方协议是内置的，永远就绪；lx 协议要看用户导入了几个音源。
	cfg := s.cfgMgr.Get()
	lxSourceCount := len(cfg.CustomSources)

	for i := range list {
		switch list[i].ID {
		case protocol.IDOfficial:
			list[i].Ready = true
		case protocol.IDLx:
			list[i].Ready = lxSourceCount > 0
			list[i].SourceCount = lxSourceCount
		}
	}

	// 能力总览：让调用方一眼看出「哪些能力已就绪、缺什么」。
	capabilityStatus := map[string]bool{
		string(protocol.CapSearch):   true, // 官方协议提供
		string(protocol.CapCharts):   true,
		string(protocol.CapPlaylist): true,
		string(protocol.CapLyric):    true,
		string(protocol.CapMusicURL): lxSourceCount > 0,
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"protocols":          list,
			"capability_status":  capabilityStatus,
			"active_source_id":   cfg.ActiveSourceID,
			"official_platforms": protocol.OfficialPlatforms,
			// 一句话说清当前处境，方便直接展示给用户或 AI。
			"summary": protocolSummary(lxSourceCount),
		},
	})
}

// protocolSummary 生成人类可读的当前协议状态说明。
func protocolSummary(lxSourceCount int) string {
	if lxSourceCount > 0 {
		return "官方协议（搜索 / 榜单 / 歌单 / 歌词）与 LX 协议（取直链）均已就绪，可正常搜索并播放。"
	}
	return "官方协议已就绪：无需导入音源即可搜索、浏览榜单与查看歌词。要播放音乐，还需在「音源管理」导入一个 LX 音源脚本用于取直链。"
}
