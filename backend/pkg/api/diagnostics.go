package api

import (
	"net/http"
	"strconv"
	"strings"

	"fn-lx-player/pkg/search"
)

// GET    /api/diagnostics/platforms   各平台请求健康度（结构化计数 + 最近失败明细）
// DELETE /api/diagnostics/platforms   清空统计
//
// 存在的理由：平台接口悄悄改版时，界面上通常只表现为「搜到 0 条」—— 没有
// 状态码、没有日志。QQ 搜索接口下线那次就是这么藏了很久。这个接口把
// 「谁、什么时候、以什么方式失败」摊开，省掉一次手工 curl。
func (s *Server) handlePlatformDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		limit := 20
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		platforms := search.PlatformDiagnostics()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data": map[string]interface{}{
				"platforms":       platforms,
				"recent_failures": search.RecentFailures(limit),
				"degraded":        degradedPlatforms(platforms),
				"hint": "统计从进程启动开始，重启即清零；覆盖该平台的全部出站请求（搜索 / 歌词 / 专辑详情）。" +
					"failed = 请求没成功，确定是故障；empty = 请求成功但 0 条结果，不确定 —— " +
					"既可能是冷门关键词，也可能是平台「静默返回空」（QQ 那次就是 200 + 空列表），所以它不算进 success_rate。",
			},
		})
	case http.MethodDelete:
		search.ResetDiagnostics()
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已清空"})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// degradedPlatforms 挑出「有过硬失败」的平台，给界面一个一眼可看的结论，
// 免得每个调用方各自去算一遍阈值。空结果不算降级 —— 冷门关键词也会空。
func degradedPlatforms(list []search.PlatformHealth) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		if p.HTTPFailed > 0 || p.TransportFailed > 0 {
			out = append(out, p.Platform)
		}
	}
	return out
}
