package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"fn-lx-player/pkg/backenddl"
)

// 后端托管下载队列的 API（见 pkg/backenddl 的包注释）。
//
// 四个接口是一件事的四个面，缺一不可：
//
//	POST /api/download/backend/submit   提交（含旧本地队列的一次性迁移）
//	GET  /api/download/backend/queue    读队列（前端唯一的真相来源）
//	POST /api/download/backend/actions  控制（暂停/继续/停止/重试/删除/清空）
//	GET  /api/download/backend/status   能力探测（能不能下、哪些渠道可用）
//
// **前端不再有第二条执行路径**：它提交、轮询、展示，不取链、不落盘。
// 所以这里的每个状态都必须如实 —— 「假装已接管」会让界面显示成功而文件不存在。

// maxBackendSubmitItems 限制单批条数。前端一次交接最多几百条；没有上限时
// 一个 8MB 的请求可以塞进几万条（逐条做渠道判定 + 写盘），把接口变成放大器。
const maxBackendSubmitItems = 500

// EnableBackendDownload 把后端队列注入 API 层。q 为 nil 表示本进程没有队列
// （老版本/测试），此时 status 会明确回 ready=false。
func (s *Server) EnableBackendDownload(q *backenddl.Queue, cfg backenddl.FetcherConfig) {
	s.backendDL = q
	s.backendCfg = cfg
}

// HandleBackendDownloadSubmit 提交任务。
//
// body:
//
//	{"items":[{client_id,name,singer,album,duration,cover,platform,platform_id,quality}],
//	 "force":false,"migrate":false}
//
// migrate=true 时 Item 里的 status/error/path/actual_quality/size/added_at/
// updated_at 会被原样保留（旧本地队列迁移，见 backenddl.Submit）。
//
// 整批**逐条**处理：能排的排队，不能排的进 skipped 并给原因，绝不为了一条坏
// 数据把整批 400 掉 —— 用户点了「下载 50 首」时，49 首该照下。
func (s *Server) HandleBackendDownloadSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.backendDL == nil {
		errJSON(w, http.StatusServiceUnavailable, "后端下载队列未启用（本进程没有队列），无法提交")
		return
	}

	var body struct {
		Items   []backenddl.Item `json:"items"`
		Force   bool             `json:"force"`
		Migrate bool             `json:"migrate"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}
	if len(body.Items) == 0 {
		errJSON(w, http.StatusBadRequest, "items 不能为空")
		return
	}
	if len(body.Items) > maxBackendSubmitItems {
		errJSON(w, http.StatusBadRequest, "单批最多 500 条")
		return
	}

	skipped := []backenddl.Skipped{}
	items := make([]backenddl.Item, 0, len(body.Items))
	for _, it := range body.Items {
		it.Name = strings.TrimSpace(it.Name)
		it.Singer = strings.TrimSpace(it.Singer)
		if it.Name == "" {
			skipped = append(skipped, backenddl.Skipped{ClientID: it.ClientID, Reason: backenddl.SkipInvalid})
			continue
		}
		// 迁移的历史记录不参与「渠道可用性」判定：已经下好的歌不需要再取链。
		// 其余条目在这里就筛掉「根本没渠道」的，前端据此明确显示失败原因，
		// 而不是排进队列等 5 分钟再失败。
		if !body.Migrate && !s.backendCfg.CanDownload(it) {
			skipped = append(skipped, backenddl.Skipped{ClientID: it.ClientID, Reason: backenddl.SkipNoChannel})
			continue
		}
		items = append(items, it)
	}

	added, skippedByQueue, err := s.backendDL.Submit(items, body.Force, body.Migrate)
	if err != nil {
		// 落盘失败：Submit 已整体回滚，这里如实 500，前端保留本地任务稍后重提。
		errJSON(w, http.StatusInternalServerError, "入队失败（队列未能落盘）："+err.Error())
		return
	}
	skipped = append(skipped, skippedByQueue...)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"added":   added,
			"skipped": skipped,
			"stats":   s.backendDL.Stats(),
			"paused":  s.backendDL.Paused(),
		},
	})
}

// HandleBackendDownloadQueue 读整个队列。前端只认这一份状态（轮询）。
//
// 顺序：最新加入的在前（见 backenddl.Queue.List）。
func (s *Server) HandleBackendDownloadQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.backendDL == nil {
		errJSON(w, http.StatusServiceUnavailable, "后端下载队列未启用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"items":  s.backendDL.List(),
			"stats":  s.backendDL.Stats(),
			"paused": s.backendDL.Paused(),
		},
	})
}

// HandleBackendDownloadActions 控制动作。
//
// body: {"action":"pause|resume|stop|cancel|retry|retry_failed|remove|clear_done|clear_all",
//
//	     "ids":["bd-..."]}
//
//	- pause / resume 不带 ids = 整队暂停/继续；带 ids = 单条（暂停会取消在途取音）。
//	- stop/cancel：pending/paused/downloading → stopped（取消在途）。
//	- retry：failed/stopped/paused → pending。
//	- remove：正在下载的拒绝删除（先 stop），其余可删。
//	- clear_done：只清 success。clear_all：只清 success/failed/stopped。
func (s *Server) HandleBackendDownloadActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.backendDL == nil {
		errJSON(w, http.StatusServiceUnavailable, "后端下载队列未启用")
		return
	}
	var body struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}
	hit, err := s.backendDL.Apply(strings.TrimSpace(body.Action), body.IDs)
	if err != nil {
		if errors.Is(err, backenddl.ErrUnknownAction) {
			errJSON(w, http.StatusBadRequest, "未知动作："+body.Action)
			return
		}
		errJSON(w, http.StatusInternalServerError, "操作失败（队列未能落盘）："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"hit": hit, "stats": s.backendDL.Stats(), "paused": s.backendDL.Paused(),
		},
	})
}

// HandleBackendDownloadStatus 能力探测：**能不能真的下**。
//
// 界面必须据此决定「提交按钮是否可用」—— 没有任何渠道时，前端要把原因如实
// 显示出来（例如「洛雪宿主未开启」），而不是排进队列后一直 pending 到超时。
//
// subscriptions.managed=true 表示「订阅（歌单监控）的自动下载已经由后端队列
// 执行」，前端据此关掉浏览器里的 `pushEngine.autoFillPending`（每 8 秒补下载），
// 否则两边会各下一遍。
func (s *Server) HandleBackendDownloadStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	avail := backenddl.Availability{Reason: "后端下载队列未启用（本进程没有队列）"}
	var stats backenddl.Stats
	paused := false
	if s.backendDL != nil {
		avail = s.backendCfg.Availability()
		stats = s.backendDL.Stats()
		paused = s.backendDL.Paused()
	}
	managed := s.backendDL != nil && avail.Ready
	subReason := ""
	if !managed {
		subReason = avail.Reason
		if subReason == "" {
			subReason = "后端下载队列不可用"
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"ready":    avail.Ready,
			"reason":   avail.Reason,
			"channels": avail.Channels,
			"lx_host":  avail.LxHost,
			// 后台队列实际落盘目录（只读，改动入口在「系统 → 设置 → 存储目录」）。
			"dir":    avail.Dir,
			"paused": paused,
			"stats":  stats,
			"subscriptions": map[string]interface{}{
				"managed": managed,
				"reason":  subReason,
			},
		},
	})
}
