package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dispatcher 真正的发现与下载由外部注入，避免本包依赖具体音源实现。
//
// Discover 负责「按监控配置抓取曲目」——在曲率里它由前端（浏览器中的
// 洛雪 lx 脚本）或 AI 解析桥完成，后端只接收结果。
// Download 负责「下载单曲」——复用已有的 pkg/downloader。
type Dispatcher interface {
	// Discover 抓取曲目；返回曲目与提示信息（如歌单解析失败的原因）
	Discover(mon *Monitor) ([]Song, []string, error)
	// Download 下载单曲；返回落盘路径
	Download(song Song) (path string, err error)
}

// URLResolver 可选能力：声明「我能替无直链曲目解析下载地址」。
//
// 由外部注入的 Dispatcher 实现（本项目里是 pkg/api 的 monitorDispatcher，
// 走已配置的**服务型音源**）。监控包自身不依赖任何音源实现，只认这个接口 ——
// 没实现时行为与以前完全一致（无直链曲目登记 pending，等网页端补下载）。
type URLResolver interface {
	// ResolveURL 解析出可直接下载的地址。失败返回 error（调用方登记 pending，
	// 下一轮监控自动重试，不标 failed）。
	// 传入 mon 是为了让实现拿到目标音质档位与降级策略。
	ResolveURL(mon *Monitor, song Song) (string, error)
	// CanResolve 当前是否真的有可用的取链通道（例如已配置并启用了服务型音源）。
	// 返回 false 时监控维持旧行为：无直链曲目登记 pending、等网页端「补下载」——
	// 不能因为「接口实现了」就让所有曲目变成取链失败。
	CanResolve() bool
}

// URLResolverCandidates 可选能力：一次给出最多 max 条**互不相同**的候选直链（按优先顺序）。
//
// 存在的原因是「取链成功 ≠ 能下载成功」：音源返回的直链可能已失效（CDN 403）、
// 返回的不是音频、只有试听片段。ResolveURL 拿到第一条就返回，落盘失败后就没得换了；
// 实现这个接口后监控可以拿着候选列表逐个试。
//
// 不实现时退化为只试 ResolveURL 的单条链（与旧行为完全一致）。
type URLResolverCandidates interface {
	ResolveURLCandidates(mon *Monitor, song Song, max int) ([]string, error)
}

// maxSourceAttempts 单曲在一次监控轮里最多换几个音源候选。
//
// 不是越大越好：每个候选都要真下一次（落盘失败才知道坏），试太多既慢又打上游。
// 3 个够覆盖「主源 + 一两个备用源」，再多属于配置有问题而不是需要更多重试。
const maxSourceAttempts = 3

// Handler 监控相关的 HTTP 接口
type Handler struct {
	store  *Store
	disp   Dispatcher
	mu     sync.Mutex
	active map[string]bool // 正在运行的监控 ID
	// TaskStats 任务统计（最近一次运行的概况）
	lastRun map[string]*Run
}

// NewHandler 创建处理器
func NewHandler(store *Store, disp Dispatcher) *Handler {
	return &Handler{
		store:   store,
		disp:    disp,
		active:  make(map[string]bool),
		lastRun: make(map[string]*Run),
	}
}

// Dispatcher 返回当前注入的发现/下载通道（供上层做类型断言，例如干跑预览）
func (h *Handler) Dispatcher() Dispatcher {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.disp
}

// SetDispatcher 注入发现/下载通道。
// 单独提供 setter 是为了避免「Server 构造 → 需要 Server 引用」的循环依赖。
func (h *Handler) SetDispatcher(disp Dispatcher) {
	h.mu.Lock()
	h.disp = disp
	h.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(body)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"code": status, "message": msg})
}

// HandleMonitors 监控集合：GET 列表 / POST 创建
func (h *Handler) HandleMonitors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listMonitors(w, r)
	case http.MethodPost:
		h.createMonitor(w, r)
	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (h *Handler) listMonitors(w http.ResponseWriter, _ *http.Request) {
	monitors := h.store.ListMonitors()

	h.mu.Lock()
	active := make([]string, 0, len(h.active))
	for id, running := range h.active {
		if running {
			active = append(active, id)
		}
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"monitors":        monitors,
			"running":         active,
			"stats":           h.store.TrackStats(""),
			"default_quality": string(QualityLossless),
		},
	})
}

func (h *Handler) createMonitor(w http.ResponseWriter, r *http.Request) {
	var m Monitor
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&m); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	created, err := h.store.CreateMonitor(&m)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": created,
		"hint": "首次执行会先建立基线（只登记曲目、不批量下载），第二轮才开始下载新增曲目",
	})
}

// HandleMonitorDetail 单个监控：GET 详情 / PATCH 更新 / DELETE 删除
func (h *Handler) HandleMonitorDetail(w http.ResponseWriter, r *http.Request) {
	id := extractMonitorID(r.URL.Path)
	if id == "" {
		errJSON(w, http.StatusBadRequest, "缺少监控 ID")
		return
	}

	switch r.Method {
	case http.MethodGet:
		mon, ok := h.store.GetMonitor(id)
		if !ok {
			errJSON(w, http.StatusNotFound, "监控不存在")
			return
		}
		runs := h.store.ListRuns(id, 20)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"monitor": mon,
				"runs":    runs,
				"stats":   h.store.TrackStats(id),
			},
		})

	case http.MethodPatch, http.MethodPost:
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}
		var patch Monitor
		if err := json.Unmarshal(raw, &patch); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}
		// ⚠️ 本接口是**局部更新**，但 bool 字段的零值与「没传」在 struct 里不可区分。
		// 所以额外解一遍「请求里到底出现了哪些键」，只有**显式出现**的才写回。
		//
		// 曾经的写法是 `if patch.AutoDownload != m.AutoDownload` —— 看着像「值变了才改」，
		// 实际效果是**没传也会改**：请求里没有 auto_download（零值 false）≠ 当前值 true
		// → 被覆盖成 false。后果很隐蔽：「停止更新」只发 {enabled} 就会把「自动下载」关掉，
		// 恢复时也回不来（恢复同样不带该字段），订阅功能静默失效。
		var present map[string]json.RawMessage
		_ = json.Unmarshal(raw, &present)
		has := func(k string) bool { _, ok := present[k]; return ok }

		updated, err := h.store.UpdateMonitor(id, func(m *Monitor) {
			if patch.Name != "" {
				m.Name = patch.Name
			}
			if patch.Quality != "" {
				m.Quality = NormalizeQuality(string(patch.Quality))
			}
			if patch.Fallback != "" {
				m.Fallback = patch.Fallback
			}
			if patch.IntervalMinutes > 0 {
				m.IntervalMinutes = patch.IntervalMinutes
			}
			if patch.MaxDownloads > 0 {
				m.MaxDownloads = patch.MaxDownloads
			}
			if patch.IncludeKW != "" || patchIncludeExplicit(r) {
				m.IncludeKW = patch.IncludeKW
			}
			if patch.ExcludeKW != "" || patchExcludeExplicit(r) {
				m.ExcludeKW = patch.ExcludeKW
			}
			if patch.Sources != nil {
				m.Sources = patch.Sources
			}
			// 只有显式传了才改 —— 见上面关于 `!=` 那个坑的说明
			if has("auto_download") {
				m.AutoDownload = patch.AutoDownload
			}
			if has("embed") {
				m.Embed = patch.Embed
			}
			// 同理：不带 enabled 的局部更新不应该把监控停掉
			if has("enabled") {
				m.Enabled = patch.Enabled
			}
			if len(patch.Target.Charts) > 0 || len(patch.Target.Playlists) > 0 || len(patch.Target.PlaylistIDs) > 0 {
				m.Target = patch.Target
			}
		})
		if err != nil {
			errJSON(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": updated})

	case http.MethodDelete:
		if err := h.store.DeleteMonitor(id); err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已删除"})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func patchIncludeExplicit(r *http.Request) bool { return r.URL.Query().Get("include_kw") == "1" }
func patchExcludeExplicit(r *http.Request) bool { return r.URL.Query().Get("exclude_kw") == "1" }

// HandlePreview 干跑：只做发现 + 过滤，不下载不写库。
//
// 对 AI 调用场景尤其有用：可以先 dry-run 看过滤效果再决定是否执行。
func (h *Handler) HandlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if h.disp == nil {
		errJSON(w, http.StatusServiceUnavailable,
			"当前没有可用的曲目发现通道：请保持曲率 页面开着（音源脚本只能在浏览器里跑），或先配置音源")
		return
	}

	// 支持两种用法：传 monitor_id（用已存配置）或直接传草稿配置
	var body struct {
		MonitorID string   `json:"monitor_id"`
		Monitor   *Monitor `json:"monitor"`
		Limit     int      `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	var mon *Monitor
	if body.MonitorID != "" {
		existing, ok := h.store.GetMonitor(body.MonitorID)
		if !ok {
			errJSON(w, http.StatusNotFound, "监控不存在")
			return
		}
		mon = existing
	} else if body.Monitor != nil {
		mon = body.Monitor
		if mon.Quality == "" {
			mon.Quality = QualityLossless
		}
	} else {
		errJSON(w, http.StatusBadRequest, "请提供 monitor_id 或 monitor 配置")
		return
	}

	songs, warnings, err := h.disp.Discover(mon)
	if err != nil {
		errJSON(w, http.StatusBadGateway, "曲目发现失败: "+err.Error())
		return
	}
	// 干跑不应受首轮基线影响，始终按「真实过滤结果」展示
	previewMon := *mon
	previewMon.BaselineDone = true

	res := Preview(&previewMon, songs)
	res.Warnings = append(res.Warnings, warnings...)

	limit := body.Limit
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if len(res.Songs) > limit {
		res.Songs = res.Songs[:limit]
	}
	if len(res.Rejected) > limit {
		res.Rejected = res.Rejected[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": res})
}

// HandleRun 手动触发一轮监控（异步执行，立即返回）
func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	id := extractActionPath(r.URL.Path, "run")
	if id == "" {
		errJSON(w, http.StatusBadRequest, "缺少监控 ID")
		return
	}
	mon, ok := h.store.GetMonitor(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "监控不存在")
		return
	}

	// 「停止」之后不该还能被触发跑一轮。
	// 定时调度那侧本来就有 Enabled 判定（`DueMonitors`），只有这个手动入口漏了 ——
	// 前端定时器、外部程序都能调到这里，于是「点了停止一会儿又开始跑」。
	if !mon.Enabled {
		errJSON(w, http.StatusConflict, "该订阅已停止，请先恢复更新再执行")
		return
	}

	if !h.tryAcquire(id) {
		errJSON(w, http.StatusConflict, "该监控正在运行中")
		return
	}

	go h.RunOnce(mon)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "已开始执行",
		"data": map[string]interface{}{"monitor_id": id, "baseline": !mon.BaselineDone},
	})
}

// HandleRuns 运行历史（全局或按监控）
func (h *Handler) HandleRuns(w http.ResponseWriter, r *http.Request) {
	monitorID := strings.TrimSpace(r.URL.Query().Get("monitor_id"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"runs": h.store.ListRuns(monitorID, limit)},
	})
}

// HandleTracks 曲目记录
func (h *Handler) HandleTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := strings.TrimSpace(q.Get("status"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	tracks, total := h.store.ListTracks(q.Get("monitor_id"), status, limit, offset)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"items":       tracks,
			"total":       total,
			"limit":       limit,
			"offset":      offset,
			"stats":       h.store.TrackStats(q.Get("monitor_id")),
			"status_list": []string{StatusPending, StatusDownloaded, StatusSkipped, StatusFailed, StatusMissing},
		},
	})
}

// HandleTracksMark 网页端「补下载」完成后回写曲目状态。
//
// POST /api/monitor/tracks/mark
// body: {"monitor_id":"...","items":[{"source":"wy","song_id":"123","file_path":"..."}],"status":"downloaded"}
//
// status 缺省为 downloaded；只接受 downloaded / pending / failed 三种回写。
func (h *Handler) HandleTracksMark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body struct {
		MonitorID string `json:"monitor_id"`
		Status    string `json:"status"`
		Items     []struct {
			Source   string `json:"source"`
			SongID   string `json:"song_id"`
			FilePath string `json:"file_path"`
		} `json:"items"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}
	if strings.TrimSpace(body.MonitorID) == "" {
		errJSON(w, http.StatusBadRequest, "缺少 monitor_id")
		return
	}
	switch body.Status {
	case "", StatusDownloaded, StatusPending, StatusFailed:
	default:
		errJSON(w, http.StatusBadRequest, "status 只能是 downloaded / pending / failed")
		return
	}
	status := body.Status
	if status == "" {
		status = StatusDownloaded
	}

	marked := 0
	for _, it := range body.Items {
		if it.Source == "" || it.SongID == "" {
			continue
		}
		if h.store.MarkTrack(body.MonitorID, it.Source, it.SongID, status, it.FilePath) {
			marked++
		}
	}
	if err := h.store.Save(); err != nil {
		errJSON(w, http.StatusInternalServerError, "状态已更新但落盘失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"marked": marked, "status": status},
	})
}

// HandleDue 返回当前到点的监控（供外部调度器驱动，也可用于诊断）
func (h *Handler) HandleDue(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"due": h.store.DueMonitors(limit)},
	})
}

// ── 执行 ──

func (h *Handler) tryAcquire(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[id] {
		return false
	}
	h.active[id] = true
	return true
}

func (h *Handler) release(id string) {
	h.mu.Lock()
	delete(h.active, id)
	h.mu.Unlock()
}

// RunOnce 执行一轮监控：发现 → 过滤 → 登记/下载 → 记录结果。
//
// 这是对外暴露的同步入口，供调度器与手动触发共用。
func (h *Handler) RunOnce(mon *Monitor) *Run {
	run := h.store.StartRun(mon.ID, !mon.BaselineDone)
	baseline := !mon.BaselineDone

	defer func() {
		h.store.FinishRun(run)
		// 基线轮完成后置位，下一轮才开始真正下载
		if baseline && run.Status != "error" {
			_, _ = h.store.UpdateMonitor(mon.ID, func(m *Monitor) { m.BaselineDone = true })
		}
		h.store.SetNextRun(mon.ID, mon.IntervalMinutes)
		h.release(mon.ID)
		_ = h.store.Save()
	}()

	var logLines []string
	fail := func(msg string) *Run {
		run.Status = "error"
		run.Message = msg
		logLines = append(logLines, "× "+msg)
		run.Log = strings.Join(logLines, "\n")
		return run
	}

	if h.disp == nil {
		return fail("没有可用的曲目发现通道：请保持曲率 页面开着（音源脚本只能在浏览器里跑）")
	}

	songs, warnings, err := h.disp.Discover(mon)
	if err != nil {
		return fail("曲目发现失败：" + err.Error())
	}
	run.Found = len(songs)
	logLines = append(logLines, "发现 "+strconv.Itoa(len(songs))+" 首曲目")
	for _, w := range warnings {
		logLines = append(logLines, "  ! "+w)
	}

	if baseline {
		logLines = append(logLines, "【首次检查】本轮只登记曲目，不下载")
	}

	existing := h.store.ExistingKeys(mon.ID)
	getTrack := func(source, songID string) (*Track, bool) {
		return h.store.GetTrack(mon.ID, source, songID)
	}

	// 后端是否具备「取链」能力：注入的 Dispatcher 既要实现 URLResolver，
	// 又要报告「当前确实有可用音源」。任一不满足就维持旧行为（登记 pending）。
	resolver, _ := h.disp.(URLResolver)
	canResolve := false
	if resolver != nil {
		canResolve = resolver.CanResolve()
	}

	plan := BuildPlan(mon, songs, existing, DefaultFileExists, getTrack, canResolve)
	logLines = append(logLines, SummarizePlan(plan))

	// recordDownloaded / recordFailed 抽出来给「单链落盘」与「换源落盘」共用
	recordDownloaded := func(item PlanItem, path string) {
		h.store.UpsertTrack(&Track{
			MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
			Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
			Duration: item.Song.Duration, Cover: item.Song.Cover,
			Status: StatusDownloaded, FilePath: path,
			QualityActual: string(mon.Quality),
		})
		run.Downloaded++
		run.NewItems++
		logLines = append(logLines, "  → "+item.Song.Name+" - "+item.Song.Artist+" 已下载")
	}
	recordFailed := func(item PlanItem, errMsg string) {
		h.store.UpsertTrack(&Track{
			MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
			Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
			Duration: item.Song.Duration, Cover: item.Song.Cover,
			Status: StatusFailed, Error: errMsg,
		})
		run.Failed++
		logLines = append(logLines, "  × "+item.Song.Name+" - "+item.Song.Artist+"："+errMsg)
	}

	// doDownload 供 "download" 分支使用（曲目自身已带直链）
	doDownload := func(item PlanItem) {
		path, dlErr := h.disp.Download(item.Song)
		if dlErr != nil {
			recordFailed(item, dlErr.Error())
			return
		}
		recordDownloaded(item, path)
	}

	// resolveCandidates 取候选直链：优先用支持多候选的实现（可换源），
	// 不支持时退化成 ResolveURL 的单条链（与旧行为一致）。
	resolveCandidates := func(song Song, want int) ([]string, error) {
		if multi, ok := h.disp.(URLResolverCandidates); ok {
			return multi.ResolveURLCandidates(mon, song, want)
		}
		if resolver == nil {
			return nil, fmt.Errorf("后端没有可用的取链通道")
		}
		url, rerr := resolver.ResolveURL(mon, song)
		if rerr != nil {
			return nil, rerr
		}
		if strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("音源服务未返回地址")
		}
		return []string{url}, nil
	}

	// downloadWithFailover 拿候选直链逐个落盘：第一个成功的即用。
	//
	// 为什么需要它：**取链成功不等于下载成功** —— 音源可能返回已失效的 CDN 链接
	// （403）、不是音频的页面、或只有试听片段。以前只试第一条链就标 failed，
	// 用户看到「下载失败」却不知道其实换个音源就能下。
	downloadWithFailover := func(item PlanItem) {
		urls, rerr := resolveCandidates(item.Song, maxSourceAttempts)
		if rerr != nil || len(urls) == 0 {
			reason := "后端取链失败："
			if rerr != nil {
				reason += rerr.Error()
			} else {
				reason += "音源服务未返回地址"
			}
			// 一条链都取不到不标 failed —— 音源偶发不可用是常态，
			// 登记 pending 等下一轮自动重试（也仍可由网页端「补下载」兜底）。
			reason += "（等页面打开时补下载）"
			h.store.UpsertTrack(&Track{
				MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
				Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
				Duration: item.Song.Duration, Cover: item.Song.Cover,
				Status: StatusPending, Error: reason,
			})
			run.ResolveFailed++
			run.NewItems++
			logLines = append(logLines, "  ! "+item.Song.Name+" - "+item.Song.Artist+"："+reason)
			return
		}

		var lastErr error
		for i, u := range urls {
			item.Song.URL = u
			path, dlErr := h.disp.Download(item.Song)
			if dlErr == nil {
				if i > 0 {
					logLines = append(logLines, "  ↻ "+item.Song.Name+" - "+item.Song.Artist+
						" 前 "+strconv.Itoa(i)+" 个音源下载失败，换源后成功")
				}
				recordDownloaded(item, path)
				return
			}
			lastErr = dlErr
			if i < len(urls)-1 {
				logLines = append(logLines, "  ↻ "+item.Song.Name+" - "+item.Song.Artist+"："+
					dlErr.Error()+" → 换下一个音源重试")
			}
		}
		// 所有候选都失败 —— 这才如实标 failed
		recordFailed(item, lastErr.Error())
	}

	stoppedEarly := false
	for _, item := range plan.Items {
		// 「停止」要能中断**已经开跑的这一轮** —— 否则用户点了停止，这一轮还会
		// 继续登记「待下载」（配了服务型音源时还会继续落盘），看起来就是「没停掉」。
		// 调度那侧只拦「下一轮」，拦不住正在跑的这一轮。
		if m, ok := h.store.GetMonitor(mon.ID); ok && !m.Enabled {
			stoppedEarly = true
			logLines = append(logLines, "！订阅已停止，本轮中止（剩余曲目未处理）")
			break
		}
		switch item.Action {
		case "skip":
			if item.Reason != "已下载且文件存在" {
				h.store.UpsertTrack(&Track{
					MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
					Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
					Duration: item.Song.Duration, Cover: item.Song.Cover,
					Status: StatusSkipped, Error: item.Reason,
				})
			} else {
				h.store.TouchTrack(mon.ID, item.Song.Source, item.Song.ID)
			}
			run.Skipped++

		case "register":
			status := StatusPending
			if baseline {
				status = StatusPending
			}
			h.store.UpsertTrack(&Track{
				MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
				Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
				Duration: item.Song.Duration, Cover: item.Song.Cover,
				Status: status, Error: item.Reason,
			})
			run.NewItems++

		case "resolve":
			// 后端向已配置的服务型音源取直链并落盘。
			// 取链全失败 → 登记 pending；候选都下载失败 → 才标 failed。
			// 换源重试的细节在 downloadWithFailover。
			if resolver == nil {
				h.store.UpsertTrack(&Track{
					MonitorID: mon.ID, Source: item.Song.Source, SongID: item.Song.ID,
					Name: item.Song.Name, Artist: item.Song.Artist, Album: item.Song.Album,
					Duration: item.Song.Duration, Cover: item.Song.Cover,
					Status: StatusPending, Error: "后端没有可用的取链通道（等页面打开时补下载）",
				})
				run.NewItems++
				continue
			}
			downloadWithFailover(item)

		case "download":
			doDownload(item)
		}
	}

	switch {
	case run.Failed > 0 && run.Downloaded > 0:
		run.Status = "partial"
	case run.Failed > 0:
		run.Status = "error"
	default:
		run.Status = "ok"
	}
	if len(warnings) > 0 {
		run.Message = strings.Join(warnings[:min(3, len(warnings))], "；")
	}
	if stoppedEarly {
		run.Message = "订阅已停止，本轮中止"
	}
	run.Log = strings.Join(logLines, "\n")

	h.mu.Lock()
	h.lastRun[mon.ID] = run
	h.mu.Unlock()

	return run
}

// Scheduler 轮询式调度器：每 tick 检查到点的监控。
//
// 采用「数据库字段比较」而非 cron，好处是重启后状态天然正确：
// 到点就跑，不会因为错过某个时刻而丢任务。
type Scheduler struct {
	handler   *Handler
	store     *Store
	tick      time.Duration
	maxParal  int
	stop      chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
}

// NewScheduler 创建调度器
func NewScheduler(h *Handler, s *Store, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = time.Minute
	}
	if tick < 10*time.Second {
		tick = 10 * time.Second
	}
	return &Scheduler{
		handler: h, store: s, tick: tick, maxParal: 3,
		stop: make(chan struct{}), stopped: make(chan struct{}),
	}
}

// Start 启动调度循环
func (sc *Scheduler) Start() {
	sc.startOnce.Do(func() {
		go sc.loop()
	})
}

// Stop 停止调度
func (sc *Scheduler) Stop() {
	close(sc.stop)
	<-sc.stopped
}

func (sc *Scheduler) loop() {
	defer close(sc.stopped)
	ticker := time.NewTicker(sc.tick)
	defer ticker.Stop()

	// 启动时先收尾残留的 running 记录，避免界面永久显示「运行中」
	if n := sc.store.RecoverRunningRuns(); n > 0 {
		_ = sc.store.Save()
	}

	for {
		select {
		case <-sc.stop:
			return
		case <-ticker.C:
			sc.tickOnce()
		}
	}
}

func (sc *Scheduler) tickOnce() {
	sc.handler.mu.Lock()
	slots := sc.maxParal - len(sc.handler.active)
	sc.handler.mu.Unlock()
	if slots <= 0 {
		return
	}

	for _, mon := range sc.store.DueMonitors(slots) {
		if !sc.handler.tryAcquire(mon.ID) {
			continue
		}
		// 先推进下次运行时间，避免长任务在运行期间被反复选中
		sc.store.SetNextRun(mon.ID, mon.IntervalMinutes)
		go sc.handler.RunOnce(mon)
		slots--
		if slots <= 0 {
			return
		}
	}
}

// extractMonitorID 从 /api/monitors/{id} 之类的路径中取出 ID
func extractMonitorID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "monitors" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractActionPath 从 /api/monitors/{id}/{action} 中取出 ID
func extractActionPath(path, action string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "monitors" && i+1 < len(parts) {
			if i+2 < len(parts) && parts[i+2] == action {
				return parts[i+1]
			}
			return parts[i+1]
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var errNoDispatcher = errors.New("no dispatcher")
