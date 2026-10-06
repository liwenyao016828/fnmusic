package api

// 飞牛音乐曲库重扫。
//
// 为什么需要这个接口：飞牛音乐**不会自己发现**音乐目录里的新文件 ——
// 它的曲库索引靠 POST /shared-library/scan-all 刷新。
//
// 下载与整理完成后我们会**自动**触发（合并 + 节流，见 pkg/fnos/rescan.go），
// 但手工触发同样有用：
//   - 用户直接从别处往目录里拷了歌，想让飞牛立刻看到
//   - 外部程序/AI 自己写完文件后调用
//
// 注意 scan-all 是**全量扫描**，大曲库下很贵 —— 所以这个接口走的是同一个
// 合并调度器，而不是每次请求都直接扫。

import (
	"net/http"

	"fn-lx-player/pkg/fnos"
)

// HandleFnosRescan 请求一次曲库重扫。
//
//	POST /api/fnos/rescan
//
// 立即返回（不等待扫描完成）—— 真正的扫描在 debounce 之后执行，
// 期间重复调用会被合并成一次。
func (s *Server) HandleFnosRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}

	m := fnos.NewMusic("")
	if !m.Available() {
		errJSON(w, http.StatusServiceUnavailable,
			"飞牛音乐接口不可用（非飞牛环境，或未授权 music 接口）")
		return
	}

	// 走调度器：合并 + 节流，避免被反复调用打成连续全量扫描
	fnos.ScheduleLibraryRescan()

	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"scheduled": true,
			"note": "已排入重扫队列。scan-all 是全量扫描，会做合并与节流（约 30 秒后执行，" +
				"两次实际扫描至少间隔 3 分钟），因此这里不返回扫描结果。",
		},
	})
}
