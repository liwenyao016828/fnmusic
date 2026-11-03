package search

// ── 流式搜索（NDJSON）──
//
// 为什么要有它：/api/search 是「等全部平台跑完 → 一次性返回单个 JSON」。
// 外挂源（musicdl sidecar）单源预算最长 120s（online/musicdl.go 的注释口径），
// 哪个源卡住整个请求就卡住；前端 axios 超时 30s —— 2026-11-03 实测
// 「晴天」全源冷搜 107s+、「周杰伦」26.7s，冷启动必超时（ECONNABORTED），
// 界面只看到「请求超时，详情见日志」。
//
// 流式版把「等全部」改成「谁先到先推谁」：每平台一个 goroutine，出结果立刻
// flush 一行；整体 20s 封顶，超时的源如实报 platform_timeout，不再拖死整个请求。
//
// 事件流（一行一个 JSON）：
//   {"type":"meta","platforms":["wy","tx",...],"page":1}
//   {"type":"partial","platform":"tx","list":[...]}   — list 可为空（该源无结果）
//   {"type":"platform_timeout","platform":"mg"}        — 预算内没等到
//   {"type":"done","total":N,"timeout_platforms":M,"elapsed_ms":T}
//
// /api/search 原样保留：它是 AI/外部程序的稳定契约（见 pkg/api/catalog.go），
// 流式版只服务界面上的「逐源返回」体验。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// searchStreamBudget 是流式搜索的整体预算。
// 原生四家 httpClient 8s、sidecar 单源预算 120s —— 都比它长，因此 20s 一到，
// 还没回来的源一律按超时处理。冷启动实测大多数源在 10s 内出结果，20s 足够宽。
const searchStreamBudget = 20 * time.Second

// searchOnePlatformCtx 与 Search() 完全同一套「外挂优先、没结果退回原生」的口径，
// 区别只有一点：把 ctx 传给外挂搜索器 —— musicdl 的 postJSON 会在 ctx 到点时
// 真正掐断 HTTP 请求（否则预算到了 sidecar 还在白跑）。原生实现不收 ctx
// （内部 httpClient 各自 8s），晚到的结果由上层 select 丢弃。
func searchOnePlatformCtx(ctx context.Context, keyword, platform string, page, pageSize int) []UnifiedSong {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 20
	}
	plat := strings.ToLower(strings.TrimSpace(platform))

	// 与 Search() 同款注释的口径：外挂优先，因为搜索与解析必须共用同一 id 空间；
	// 外挂没结果退回原生（如果这个平台有原生实现）。
	if s := lookupSearcher(plat); s != nil {
		if out := s.Search(ctx, keyword, page, pageSize*2); len(out) > 0 {
			return filterAndRankSongs(keyword, out)
		}
	}
	switch plat {
	case "wy":
		return filterAndRankSongs(keyword, searchNetEase(keyword, page, pageSize*2))
	case "tx":
		return filterAndRankSongs(keyword, searchQQ(keyword, page, pageSize*2))
	case "kg":
		return filterAndRankSongs(keyword, searchKuGou(keyword, page, pageSize*2))
	case "kw":
		return filterAndRankSongs(keyword, searchKuWo(keyword, page, pageSize*2))
	}
	return nil
}

// HandleSearchStream GET /api/search/stream?q=..&source=..&page=..&page_size=..
// 参数与 /api/search 一致；响应是 NDJSON 事件流（见文件头注释）。
func HandleSearchStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	keyword := strings.TrimSpace(q.Get("q"))
	if keyword == "" {
		keyword = strings.TrimSpace(q.Get("keyword"))
	}
	if keyword == "" {
		// 前置错误走普通 JSON（同 /api/search 的空关键词口径，但状态码如实 400）
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "empty keyword"})
		return
	}

	platform := strings.ToLower(q.Get("source"))
	if platform == "" {
		platform = strings.ToLower(q.Get("platform"))
	}
	if platform == "" {
		platform = "all"
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 20
	}

	// 平台清单：单平台照旧；all = 原生四家 + 外挂注册表现取（sidecar 关着时它
	// 就不在清单里，不会白等一轮超时）—— 与 HandleSearch 的 all 分支同口径。
	perPlatform := pageSize * 2
	plats := []string{platform}
	if platform == "all" {
		perPlatform = 16
		plats = nil
		seen := map[string]bool{}
		for _, p := range append([]string{"wy", "tx", "kg", "kw"}, ExternalSearcherPlatforms()...) {
			if !seen[p] {
				seen[p] = true
				plats = append(plats, p)
			}
		}
	}

	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// 反向代理常见：X-Accel-Buffering=no 提示 nginx 这类别缓冲，逐行透传
	w.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(ev map[string]interface{}) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_, _ = w.Write(append(b, '\n'))
		if canFlush {
			flusher.Flush()
		}
	}

	start := time.Now()
	writeEvent(map[string]interface{}{"type": "meta", "platforms": plats, "page": page})

	ctx, cancel := context.WithTimeout(r.Context(), searchStreamBudget)
	defer cancel()

	type platResult struct {
		platform string
		list     []UnifiedSong
		timedOut bool
	}
	ch := make(chan platResult, len(plats))
	for _, p := range plats {
		go func(p string) {
			resCh := make(chan platResult, 1)
			go func() {
				resCh <- platResult{platform: p, list: searchOnePlatformCtx(ctx, keyword, p, page, perPlatform)}
			}()
			select {
			case res := <-resCh:
				ch <- res
			case <-ctx.Done():
				// 预算到点；但若结果恰好已经落进缓冲，如实用它而不是报超时
				select {
				case res := <-resCh:
					ch <- res
				default:
					ch <- platResult{platform: p, timedOut: true}
				}
			}
		}(p)
	}

	total, timeouts := 0, 0
	for i := 0; i < len(plats); i++ {
		res := <-ch
		if res.timedOut {
			timeouts++
			writeEvent(map[string]interface{}{"type": "platform_timeout", "platform": res.platform})
			continue
		}
		// 空列表也发 partial：界面靠它把「这个源搜完了、0 条」画出来
		total += len(res.list)
		list := res.list
		if list == nil {
			list = []UnifiedSong{}
		}
		writeEvent(map[string]interface{}{"type": "partial", "platform": res.platform, "list": list})
	}
	writeEvent(map[string]interface{}{
		"type":              "done",
		"total":             total,
		"timeout_platforms": timeouts,
		"elapsed_ms":        time.Since(start).Milliseconds(),
	})
}
