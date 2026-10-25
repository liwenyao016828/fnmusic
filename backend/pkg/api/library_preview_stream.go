package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"fn-lx-player/pkg/complete"
)

// 补全预览的**流式**接口。
//
//	POST /api/library/complete/preview/stream   {"dir":"...","limit":30,"fields":[...]}
//	     → NDJSON，一行一个事件：
//	       {"type":"start","total":30}
//	       {"type":"item","item":{...},"done":1,"total":30}        ← 每匹配完一首立刻推一条
//	       {"type":"progress","done":5,"total":30,"current":"..."} ← 心跳（每 400ms 最多一条）
//	       {"type":"done","done":30,"total":30,"plan_id":"...","matched":27,...}
//	       {"type":"cancelled","done":12,"total":30,"message":"..."}
//	       {"type":"error","message":"..."}
//
// ── 为什么要有这个接口 ──
//
// 用户 2026-10-24：「补全整理的预览改成流式显示，加暂停 / 继续 / 停止」。
//
// 原来的预览是**一个同步 POST**：complete.Resolve 跑完 30 首（每首都要打外网搜索）
// 才一次性返回，界面只能转圈。拖到几十首时用户完全不知道进度、也没法叫停 ——
// 和「补全」那边已经有 cancel 的待遇完全不一样。
//
// 流式化之后：
//
//	· 每匹配完一首就推一条 item，界面逐行出现（看得见在动）；
//	· 客户端断开连接 = 取消（ctx 由 r.Context() 派生），这就是「停止」。
//	  停止后**不给 plan_id**：只跑了一半的清单拿去执行，用户会觉得「我停了它还是写了」。
//	· 「暂停 / 继续」在客户端做（服务端不支持暂停，见 frontend 的 PreviewStream）：
//	  暂停 = 断开这条流（服务端立刻取消，不再打外网）。继续 = 重新发起一次。
//	  刻意**不做服务端暂停**：连接挂着却什么都不做，只会白占一个槽位、
//	  还把「预览中」的假象留在界面上，收益是零 —— 用户暂停时最需要的恰恰是「别再打了」。
//	  代价是「继续」会从头再跑一遍；但 resume 只是看着免费 —— 半份结果里的匹配参数
//	  （限速、平台顺序、AI 判定）在第二次跑时未必一样，接起来的清单不如整份重跑可信。
//
// ── 与同步预览的关系 ──
//
// 复用的同一套东西：同样的入参解析（parsePreviewParams）、同样的 complete.Resolve、
// 同样的 plan 注册（completePlan.set）—— 只是把 onProgress 变成「往流里写一行」。
// 所以最后返回的 plan_id 仍然能拿去 POST /api/library/complete 执行。
const previewStreamTick = 400 * time.Millisecond

// previewSlot 串行化「预览 / 补全」这类长任务。
//
// 两者都要打外网、都可能跑几十秒；同时跑会互相抢限速额度，而且界面上的
// 「预览中…」和「补全中…」同时亮着，用户也分不清谁在等谁。
// 这里只挡它们彼此，其余接口不受影响。
type previewSlotState struct {
	mu   sync.Mutex
	busy string // "preview" / "complete" / ""
}

var previewSlot previewSlotState

// acquirePreview 尝试占位；被占时返回 false 和当前的占用者（给用户看的是谁在跑）。
func (p *previewSlotState) acquire(owner string) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy != "" {
		return false, p.busy
	}
	p.busy = owner
	return true, ""
}

func (p *previewSlotState) release() {
	p.mu.Lock()
	p.busy = ""
	p.mu.Unlock()
}

// HandleLibraryCompletePreviewStream POST /api/library/complete/preview/stream
func (s *Server) HandleLibraryCompletePreviewStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if completeJob.isRunning() {
		errJSON(w, http.StatusConflict, "补全正在跑，等它结束再预览（否则两边会抢外网额度）")
		return
	}
	if ok, owner := previewSlot.acquire("preview"); !ok {
		msg := "上一次预览还在跑，等它结束（或先在界面上点停止）"
		if owner == "complete" {
			msg = "补全正在跑，等它结束再预览"
		}
		errJSON(w, http.StatusConflict, msg)
		return
	}
	defer previewSlot.release()

	params, errMsg, emptyHint := s.parsePreviewParams(r)
	if errMsg != "" {
		errJSON(w, http.StatusBadRequest, errMsg)
		return
	}

	writePreviewStreamHeader(w)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(v map[string]interface{}) {
		if err := enc.Encode(v); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	if emptyHint != "" {
		// 一条都不用跑：仍然用统一的流格式回一个 done，前端只认一种协议
		send(map[string]interface{}{"type": "start", "total": 0})
		send(map[string]interface{}{
			"type": "done", "done": 0, "total": 0, "message": emptyHint,
			"unsupported": params.Gaps.Unsupported, "unsupported_exts": params.Gaps.UnsupportedExts,
		})
		return
	}

	total := len(params.Targets)
	send(map[string]interface{}{"type": "start", "total": total, "dir": params.Dir})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 每匹配完一首**立刻**把明细推出去 —— 「流式」的全部意义就在这里：
	// 用户要的是逐条看着它出结果，不是等 30 首跑完再看一份清单。
	//
	// 两个节流仍然保留：
	//   · lastTick 限制 `progress` 心跳最多每 previewStreamTick 一条（否则 400ms
	//     内并发的十几首会刷出一串同值心跳）；
	//   · sent 记录已推过的下标，保证同一首不会推两次（Resolve 的回调在并发
	//     worker 里触发，顺序不保证，但每条只触发一次）。
	// 故意**不**靠节流去省带宽：一首一行、几十行，比「界面上什么都不动」值钱得多。
	var mu sync.Mutex
	lastTick := time.Now()
	sent := make(map[int]bool, total)

	plan := complete.Resolve(ctx, params.Targets, s.options(params), func(p complete.Progress) {
		mu.Lock()
		var fresh *complete.PlanItem
		if p.Item != nil && p.Index >= 0 && p.Index < len(params.Targets) && !sent[p.Index] {
			sent[p.Index] = true
			it := *p.Item
			fresh = &it
		}
		now := time.Now()
		tick := now.Sub(lastTick) >= previewStreamTick
		if tick {
			lastTick = now
		}
		mu.Unlock()

		if fresh != nil {
			send(map[string]interface{}{"type": "item", "item": fresh, "done": p.Done, "total": p.Total})
			return
		}
		if tick {
			send(map[string]interface{}{"type": "progress", "done": p.Done, "total": p.Total, "current": p.Current})
		}
	})
	plan.Dir = params.Dir

	// 用户中途停止（浏览器断开）时 ctx 已取消，且计划不完整
	userStopped := ctx.Err() != nil && len(plan.Items) < total
	if userStopped {
		send(map[string]interface{}{
			"type": "cancelled", "done": len(plan.Items), "total": total, "matched": countMatched(plan),
			"message": fmt.Sprintf("已停止：完成 %d / %d 首。这份清单不完整，需要重新预览才能执行", len(plan.Items), total),
		})
		return
	}

	matched, aiPicked := countMatchedAI(plan)

	// 兜底：Resolve 没通过回调给出的条目（理论上不该有）在这里补推一次，
	// 保证界面上的行数 == 计划里的条目数。
	mu.Lock()
	for idx, it := range plan.Items {
		if sent[idx] {
			continue
		}
		sent[idx] = true
		mu.Unlock()
		send(map[string]interface{}{"type": "item", "item": it, "done": idx + 1, "total": total})
		mu.Lock()
	}
	mu.Unlock()

	planID := completePlan.set(params.Dir, plan)
	send(map[string]interface{}{
		"type": "done", "done": len(plan.Items), "total": total,
		"plan_id": planID, "dir": params.Dir, "matched": matched, "matched_by_ai": aiPicked,
		"cancelled": plan.Cancelled, "nothing_written": true,
		"unsupported": params.Gaps.Unsupported, "unsupported_exts": params.Gaps.UnsupportedExts,
		"message": fmt.Sprintf("共 %d 首，匹配到 %d 首。确认后再执行写入。", len(plan.Items), matched),
	})
}

// writePreviewStreamHeader 声明 NDJSON 流。
//
// ⚠️ 不要设 Content-Length，也不要让任何中间层缓冲：
// nginx 需要 X-Accel-Buffering: no 才会逐条下发，否则会攒成一大块才吐给浏览器。
func writePreviewStreamHeader(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// countMatchedAI 统计「匹配到几首 / 其中几首由 AI 定」。
//
// 抽出来是因为停止分支也要报同一个数 —— 以前只有跑完那条路径算，
// 用户中途停下时界面拿不到 matched，只能显示 0（看着像一首都没配上）。
func countMatchedAI(plan complete.Plan) (matched, aiPicked int) {
	for _, it := range plan.Items {
		if it.Matched {
			matched++
		}
		if it.MatchedByAI {
			aiPicked++
		}
	}
	return matched, aiPicked
}

// countMatched 只要一个数（给 cancelled 事件用）
func countMatched(plan complete.Plan) int {
	m, _ := countMatchedAI(plan)
	return m
}
