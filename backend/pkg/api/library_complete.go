package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/tags"
	"fn-lx-player/pkg/tidy"
)

// 曲库补全任务的状态。
//
// 只保留**最近一次** —— 这是「手动点一次、跑一批」的操作，不需要任务列表；
// 保留最近一次就够前端显示进度与结果（侧栏「任务状态」也读它）。
type completeJobState struct {
	mu       sync.Mutex
	running  bool
	dir      string
	done     int
	total    int
	current  string
	started  string
	finished string
	errMsg   string
	summary  *complete.Summary

	// ctx / cancel 这次任务的停止开关（`POST /api/library/complete/cancel`）。
	//
	// 补全一批最多 200 首，每首都要打外网、还要改写用户的音频文件，跑起来很久。
	// 以前这里喂的是 `context.Background()` —— 一旦开跑就只能干等（再点一次会被 409 挡掉），
	// 连「我点错了、停一下」都做不到。
	ctx       context.Context
	cancel    context.CancelFunc
	cancelled bool
}

var completeJob completeJobState

// completeJobSnapshot 对外快照（加锁后拷贝，避免读的时候任务还在写）
type completeJobSnapshot struct {
	Running    bool              `json:"running"`
	Dir        string            `json:"dir,omitempty"`
	Done       int               `json:"done"`
	Total      int               `json:"total"`
	Current    string            `json:"current,omitempty"`
	StartedAt  string            `json:"started_at,omitempty"`
	FinishedAt string            `json:"finished_at,omitempty"`
	Error      string            `json:"error,omitempty"`
	Summary    *complete.Summary `json:"summary,omitempty"`
	// UndoAvailable 有没有可撤销的补全（从**磁盘**上判断，服务重启后仍然准）
	UndoAvailable bool `json:"undo_available"`
	// LatestBackupDir 最近一次备份目录
	LatestBackupDir string `json:"latest_backup_dir,omitempty"`
	// Cancelled 这次任务是**被停止**的（不是跑完的）。注意与 `summary.cancelled` 区分：
	// 这个说的是任务本身，那个说的是「有几首没处理」。
	Cancelled bool `json:"cancelled,omitempty"`
}

// completeSnapshot 任务快照 + 撤销可用性。
//
// 撤销可用性从磁盘上的备份目录判断，**不是**从内存里的 summary ——
// 服务重启后 summary 就没了，但备份还在，用户仍然应该能撤销。
func (s *Server) completeSnapshot() completeJobSnapshot {
	snap := completeJob.snapshot()
	snap.LatestBackupDir = complete.FindLatest(s.aiBackupRoot)
	snap.UndoAvailable = snap.LatestBackupDir != ""
	return snap
}

func (s *completeJobState) snapshot() completeJobSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return completeJobSnapshot{
		Running:    s.running,
		Dir:        s.dir,
		Done:       s.done,
		Total:      s.total,
		Current:    s.current,
		StartedAt:  s.started,
		FinishedAt: s.finished,
		Error:      s.errMsg,
		Summary:    s.summary,
		Cancelled:  s.cancelled,
	}
}

func (s *completeJobState) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// start 开始一次任务，并**把可取消的 ctx 交回给执行方**。
//
// 由它来建 ctx 而不是调用方用 `context.Background()`：这个任务可能跑几百次外网请求、
// 改几十个文件，中间必须留一个叫停的口子，而那个口子（cancel）只有这里能掌握。
func (s *completeJobState) start(dir string, total int) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 上一轮的 cancel 先释放掉，别在这里攒闭包（ctx 泄漏的经典样子）
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	s.cancelled = false
	s.running = true
	s.dir = dir
	s.done = 0
	s.total = total
	s.current = ""
	s.started = time.Now().Format(time.RFC3339)
	s.finished = ""
	s.errMsg = ""
	s.summary = nil
	return ctx
}

// cancelRun 请求停止当前任务，返回「是否真的有任务在跑」。
//
// 没在跑时返回 false —— 让接口能如实回答「没有在跑的任务」，
// 而不是回一句「已停止」让用户以为停了什么（其实什么都没发生）。
func (s *completeJobState) cancelRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cancel == nil {
		return false
	}
	s.cancelled = true
	s.cancel()
	return true
}

func (s *completeJobState) setProgress(p complete.Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done, s.total, s.current = p.Done, p.Total, p.Current
}

func (s *completeJobState) finish(sum *complete.Summary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.summary = sum
	s.current = ""
	s.finished = time.Now().Format(time.RFC3339)
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

func (s *completeJobState) fail(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.errMsg = msg
	s.current = ""
	s.finished = time.Now().Format(time.RFC3339)
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// 待确认的计划。
//
// 只保留**最近一次**：补全是「手动点一次跑一批」的操作，用户确认的就是刚刚看到的那份。
// 保留多份只会让人点错 —— 而且计划里存着匹配到的曲目标识，
// 万一用户确认了旧的计划，就会写出与预览不符的结果。
type pendingPlan struct {
	mu   sync.Mutex
	id   string
	dir  string
	plan complete.Plan
	at   time.Time
}

var completePlan pendingPlan

func (p *pendingPlan) set(dir string, plan complete.Plan) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id = fmt.Sprintf("plan_%d", time.Now().UnixNano())
	p.dir = dir
	p.plan = plan
	p.at = time.Now()
	return p.id
}

// take 取出并清空 —— 计划**只能用一次**。
// 重复执行同一份计划没有意义（第二次会因为没有 NeedLyric/NeedCover 而空转），
// 而且会让人误以为「点了两次写了两次」。
func (p *pendingPlan) take(id string) (complete.Plan, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.id == "" || p.id != id {
		return complete.Plan{}, "", false
	}
	plan, dir := p.plan, p.dir
	p.id, p.plan, p.dir = "", complete.Plan{}, ""
	return plan, dir, true
}

func (p *pendingPlan) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id, p.plan, p.dir = "", complete.Plan{}, ""
}

// parseCompleteFields 解析「这次要补哪些字段」。
//
// 没传 fields 时保持**老口径**（只补歌词 + 封面）：接口早就有调用方在用，
// 默认值突然扩到六个字段会让它们悄悄改写下标题之外的标签。
// `only` 是更早的口径（lyric / cover），只在 fields 缺失时生效。
func parseCompleteFields(list []string, only string) library.Fields {
	var f library.Fields
	for _, raw := range list {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "all":
			return library.AllFields()
		case "lyric":
			f.Lyric = true
		case "cover":
			f.Cover = true
		case "genre":
			f.Genre = true
		case "year":
			f.Year = true
		case "track":
			f.Track = true
		case "disc":
			f.Disc = true
		}
	}
	if f.Any() {
		return f
	}
	switch strings.ToLower(strings.TrimSpace(only)) {
	case "lyric":
		return library.Fields{Lyric: true}
	case "cover":
		return library.Fields{Cover: true}
	default:
		return library.Fields{Lyric: true, Cover: true}
	}
}

// HandleLibraryComplete 曲库补全：挑出缺歌词/封面的曲目 → 搜索匹配 → 取歌词/封面 → 写入标签。
//
//	POST /api/library/complete  body: {dir, limit?, platforms?, use_ai_genre?, overwrite?, only?}
//	GET  /api/library/complete  读最近一次任务的进度与结果
//
// 为什么是**异步任务**而不是一次同步请求：一首歌要走「搜索 + 取歌词」两次外网请求，
// 几十首要几分钟 —— 同步请求必然超时，用户也看不到进度。
//
// 挑活用的是曲库索引（`Index.NeedingTidy`），所以**先跑一次「增量扫描」**才有数据。
func (s *Server) HandleLibraryComplete(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok", "data": s.completeSnapshot(),
		})

	case http.MethodPost:
		var req struct {
			Dir        string   `json:"dir"`
			Limit      int      `json:"limit"`
			Platforms  []string `json:"platforms"`
			Fields     []string `json:"fields"` // 要补哪些字段：lyric/cover/genre/year/track/disc（或 "all"）
			UseAIGenre bool     `json:"use_ai_genre"`
			Overwrite  bool     `json:"overwrite"`
			Only       string   `json:"only"` // "" / lyric / cover（老口径，被 fields 取代）
			// DryRun 只出计划、不写文件（「先预览后写」的预览）
			DryRun bool `json:"dry_run"`
			// PlanID 执行某份已确认的计划（由 dry_run 返回）
			PlanID string `json:"plan_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}

		// ① 执行已确认的计划
		//
		// ⚠️ 执行用的是**预览时那份计划**（含匹配到的曲目标识），不重新搜一次 ——
		// 否则搜索结果变了、AI 挑得不一样，用户就会「批准了 A、结果写了 B」。
		if pid := strings.TrimSpace(req.PlanID); pid != "" {
			if completeJob.isRunning() {
				errJSON(w, http.StatusConflict, "上一次补全还在跑，等它结束再开始")
				return
			}
			plan, dir, ok := completePlan.take(pid)
			if !ok {
				errJSON(w, http.StatusBadRequest, "这份计划已失效或已被执行，请重新预览")
				return
			}
			opts := s.buildCompleteOptions(req.Platforms, req.UseAIGenre, req.Overwrite)
			ctx := completeJob.start(dir, len(plan.Items))
			go s.runCompleteJob(ctx, nil, &plan, opts)
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"code": 200, "message": fmt.Sprintf("已开始写入 %d 首", len(plan.Items)),
				"data": s.completeSnapshot(),
			})
			return
		}

		dir := strings.TrimSpace(req.Dir)
		if dir == "" {
			errJSON(w, http.StatusBadRequest, "请提供 dir")
			return
		}
		resolved, err := resolveLibraryPath(dir)
		if err != nil {
			errJSON(w, http.StatusForbidden, err.Error())
			return
		}

		if completeJob.isRunning() {
			errJSON(w, http.StatusConflict, "上一次补全还在跑，等它结束再开始")
			return
		}

		limit := req.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 200 {
			errJSON(w, http.StatusBadRequest, "单次最多补全 200 首（每首要走两次外网请求，再多会跑很久）")
			return
		}

		fields := parseCompleteFields(req.Fields, req.Only)
		// 写不了标签的格式（wav/opus…）不会排进队，但必须如实汇报：
		// 否则用户看着「补全跑完了、这些歌还是没年份」，只能怀疑功能坏了。
		gaps := s.libraryIndex().FieldGaps(resolved)
		entries := s.libraryIndex().NeedingFields(resolved, fields, limit)
		if len(entries) == 0 {
			hint := "索引里没有勾选字段仍缺失的记录。若刚加了新歌，先跑一次「增量扫描」再试。"
			if note := unsupportedNote(gaps, "它们不算在缺口里"); note != "" {
				hint += note
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "没有需要补全的曲目",
				"hint": hint,
				"data": s.completeSnapshot(),
			})
			return
		}

		targets := make([]complete.Target, 0, len(entries))
		for _, e := range entries {
			t := complete.Target{Path: e.Path, Title: e.Title, Artist: e.Artist, Album: e.Album}
			// 逐条按「这条记录缺不缺」决定，而不是整批一刀切：
			// 勾了年份但这首本来就有年份，就不该再为它跑一次搜索。
			if fields.Lyric && !e.HasLyric {
				t.NeedLyric = true
			}
			if fields.Cover && !e.HasCover {
				t.NeedCover = true
			}
			if fields.Genre && e.Genre == "" {
				t.NeedGenre = true
			}
			if fields.Year && e.Year <= 0 {
				t.NeedYear = true
			}
			if fields.Track && e.Track <= 0 {
				t.NeedTrack = true
			}
			if fields.Disc && e.Disc <= 0 {
				t.NeedDisc = true
			}
			if !t.WantsAnything() {
				continue
			}
			targets = append(targets, t)
		}
		if len(targets) == 0 {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "没有需要补全的曲目",
				"hint": "勾选的字段这些记录都已具备（索引可能滞后，先跑一次「增量扫描」）。",
				"data": s.completeSnapshot(),
			})
			return
		}

		opts := s.buildCompleteOptions(req.Platforms, req.UseAIGenre, req.Overwrite)

		// ② 预览：只解析匹配，**不写任何文件**
		//
		// 补全会真正改写用户的音频文件。先让用户看一眼「文件 → 匹配到的歌 → 会写什么」，
		// 确认后才落盘 —— 这比事后撤销体验好得多（**撤销是兜底，不是主防线**）。
		if req.DryRun {
			plan := complete.Resolve(r.Context(), targets, opts, nil)
			plan.Dir = resolved
			planID := completePlan.set(resolved, plan)
			matched, aiPicked := 0, 0
			for _, it := range plan.Items {
				if it.Matched {
					matched++
				}
				if it.MatchedByAI {
					aiPicked++
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code":    200,
				"message": fmt.Sprintf("共 %d 首，匹配到 %d 首。确认后再执行写入。", len(plan.Items), matched) + unsupportedNote(gaps, "已排除"),
				"data": map[string]interface{}{
					"plan_id":         planID,
					"dir":             resolved,
					"total":           len(plan.Items),
					"matched":         matched,
					"matched_by_ai":   aiPicked,
					"items":           plan.Items,
					"nothing_written": true,
					// 写不了标签的条数：界面/调用方据此解释「为什么这些歌补不上」
					"unsupported":      gaps.Unsupported,
					"unsupported_exts": gaps.UnsupportedExts,
				},
			})
			return
		}

		// ③ 直接跑（不预览）。保留这条路是为了兼容只调接口的调用方；
		// 界面上走的是「预览 → 确认」两步。
		completePlan.clear() // 直接跑会让之前那份计划过期
		ctx := completeJob.start(resolved, len(targets))
		go s.runCompleteJob(ctx, targets, nil, opts)

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"code":    200,
			"message": fmt.Sprintf("已开始补全 %d 首", len(targets)) + unsupportedNote(gaps, "已排除"),
			"data":    s.completeSnapshot(),
		})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// HandleLibraryCompleteCancel 停止正在跑的补全任务。
//
// 为什么需要这个口子：一批最多 200 首，每首要打两次外网、还要改写用户的音频文件，
// 跑起来是分钟级的；以前一旦开始就只能等它跑完（再点一次会被 409 挡回「上一次还在跑」）。
// 用户点错了、或发现挑出来的歌不对，没有任何办法叫停 —— 而它正在改文件。
//
// 停止的语义是**不再派发新曲目**：已经写完的文件保留（要回退用撤销，备份清单还在），
// 正在写的那一首会写完（写到一半中断会留下半个标签块，比不写更糟）。
func (s *Server) HandleLibraryCompleteCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !completeJob.cancelRun() {
		// 如实回答「没有在跑的任务」：回一句「已停止」会让用户以为停了什么
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code":    200,
			"message": "没有正在跑的补全任务",
			"data":    s.completeSnapshot(),
		})
		return
	}
	log.Printf("[COMPLETE] 收到停止请求：不再派发新曲目，已写入的保留（可用撤销回退）")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "正在停止：不再开始新的曲目，已写入的会保留。用 GET /api/library/complete 看未处理数量。",
		"data":    s.completeSnapshot(),
	})
}

// unsupportedNote 拼一句「另有 N 首格式写不了标签」的说明，没有就返回空串。
//
// 用在补全的预览/空提示里。这些条数不算缺口（`library.FieldGaps` 已把它们摘出去），
// 但用户必须知道它们存在 —— 不然「补完还是没年份」只能被当成功能坏了，
// 而实际原因是本应用只实现了 MP3/FLAC 两个写入器（见 pkg/audioext.Writable）。
func unsupportedNote(g library.GapCounts, tail string) string {
	if g.Unsupported <= 0 {
		return ""
	}
	exts := make([]string, 0, len(g.UnsupportedExts))
	for ext, n := range g.UnsupportedExts {
		exts = append(exts, fmt.Sprintf("%s×%d", ext, n))
	}
	sort.Strings(exts)
	detail := strings.Join(exts, "、")
	if detail == "" {
		detail = "格式未知"
	}
	if tail == "" {
		tail = "已排除"
	}
	return fmt.Sprintf(" 另有 %d 首是补全写不了的格式（%s；本应用只能写 MP3/FLAC），%s。",
		g.Unsupported, detail, tail)
}

// buildCompleteOptions 组装补全选项。预览与执行**必须用同一份** ——
// 否则「预览时不开 AI、执行时开了」这类不一致会让用户看到的结果和实际写入的不一样。
func (s *Server) buildCompleteOptions(platforms []string, useAIGenre, overwrite bool) complete.Options {
	// 主力平台（默认网易 + QQ）决定两件事：
	// ① 搜索顺序 —— 主力在前、其余殿后，兜底平台只在它们都没有时才用；
	// ② 兜底命中时「回哪家借年份/曲序/风格」。
	// 调用方显式传了 platforms 就**尊重它**，不越权重排（老接口调用方在用这个参数）。
	var preferred []string
	if s.cfgMgr != nil {
		preferred = s.cfgMgr.Get().PlatformPriority()
	}
	if len(platforms) == 0 && len(preferred) > 0 {
		platforms = config.OrderedPlatforms(preferred, complete.DefaultPlatforms)
	}
	return complete.Options{
		Platforms:          platforms,
		PreferredPlatforms: preferred,
		// 文件名没把握时请 AI 判一次（字段为空或规则自认可疑）。
		// 没配 AI 时 Options 里会被忽略，规则照常工作。
		UseAINameJudge: true,
		UseAIMatch:     true,
		UseAIGenre:     useAIGenre,
		AIClient:       s.aiClient,
		NameRules:      s.nameRules,
		// 改文件之前先备份 —— 用户点「撤销」时从这里还原。
		// 备份失败会跳过该文件（宁可不补，也不能剥夺回滚能力）。
		Backup: complete.NewBackup(complete.NewBackupDir(s.aiBackupRoot)),
		Tidy: tidy.Options{
			EmbedLyric:     true,
			WriteLRC:       true,
			EmbedCover:     true,
			WriteMetadata:  true,
			OverwriteLyric: overwrite,
			OverwriteCover: overwrite,
		},
	}
}

// runCompleteJob 跑一次补全任务。
//
// plan 非 nil 时**执行这份已确认的计划**（不重新搜索，保证「预览什么就写什么」）；
// 为 nil 时先用 targets 解析出计划再执行（「直接跑」那条路）。
//
// ctx 来自 `completeJob.start`，被 `POST /api/library/complete/cancel` 取消时：
// 不再派发新曲目，已经写完的文件保留（撤销走备份清单），未处理的数量记在 summary 里。
func (s *Server) runCompleteJob(ctx context.Context, targets []complete.Target, plan *complete.Plan, opts complete.Options) {
	defer func() {
		if rec := recover(); rec != nil {
			completeJob.fail(fmt.Sprintf("任务异常: %v", rec))
		}
	}()

	var p complete.Plan
	if plan != nil {
		p = *plan
	} else {
		p = complete.Resolve(ctx, targets, opts, completeJob.setProgress)
	}
	sum := complete.Execute(ctx, p, opts, completeJob.setProgress)

	// 解析阶段（还没轮到搜）就被停掉的曲目：既不在 p.Items 里，也不会出现在结果里。
	// 不把它们补进总数，汇总就会显示「停了一次，只少了一首」，用户会以为其余几百首都处理过了。
	if p.Cancelled > 0 {
		sum.Total += p.Cancelled
		sum.Cancelled += p.Cancelled
	}
	// 进度数字要说实话：解析阶段每搜完一首就会把 done 推到总数，
	// 所以被停之后 done 会停在 3/3 —— 看起来像「全处理完了」。
	// 真正常处理的只有 results 里那几首，这里按实际处理数回填一次。
	if sum.Cancelled > 0 {
		completeJob.setProgress(complete.Progress{Done: len(sum.Results), Total: sum.Total})
	}
	// ⚠️ 「这次是不是被用户停掉的」必须在 finish 之前取值：
	// finish 自己会 cancel 掉 ctx（释放闭包），取晚了的话**每个跑成功的任务**
	// 都会走下面那条分支，日志里留下一句「任务已停止：未处理 0 首」——
	// 事后只看日志就分不清「跑完了」还是「被叫停的」。2026-09-26 真机冒烟抓到。
	stopped := ctx.Err() != nil
	completeJob.finish(&sum)

	if stopped {
		// 停止是用户的动作，不是故障 —— 但日志里必须留痕，
		// 否则事后只看得到「跑完了」，分不清是跑完还是被叫停的。
		log.Printf("[COMPLETE] 任务已停止：已处理 %d 首，未处理 %d 首（已写入的保留，可用撤销回退）",
			len(sum.Results), sum.Cancelled)
	}

	// 补全改动了文件（标签/歌词/封面/年份…），通知飞牛音乐重扫 ——
	// 它不会自己发现改动，不通知的话「补全了但飞牛里没变」。调度器会合并+节流。
	if sum.LyricFilled > 0 || sum.CoverFilled > 0 || sum.GenreFilled > 0 ||
		sum.YearFilled > 0 || sum.TrackFilled > 0 || sum.DiscFilled > 0 {
		fnos.ScheduleLibraryRescan()
	}

	// 把结果回写索引，否则下一轮还会挑到同一批。
	// ⚠️ 各字段要取「原本就有 || 这次刚补上」——直接用本次结果会把
	// 「本来就有歌词、这次只补了封面」的记录误标成缺歌词。
	// 年份/曲序/风格同理：不回填数值的话索引里永远是 0，下一轮又把它们挑出来重跑搜索。
	for _, r := range sum.Results {
		if !r.OK {
			continue
		}
		old, _ := s.libraryIndex().Get(r.Path)
		s.libraryIndex().MarkEnriched(r.Path, library.Enrich{
			Title:    firstNonEmptyStr(r.Title, old.Title),
			Artist:   firstNonEmptyStr(r.Artist, old.Artist),
			Album:    firstNonEmptyStr(r.Album, old.Album),
			Genre:    firstNonEmptyStr(r.Genre, old.Genre),
			Year:     firstPositive(r.Year, old.Year),
			Track:    firstPositive(r.Track, old.Track),
			Disc:     firstPositive(r.Disc, old.Disc),
			HasLyric: old.HasLyric || r.LyricFilled,
			HasCover: old.HasCover || r.CoverFilled,
		})
	}
	_ = s.libraryIndex().Save()
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// HandleCompleteUndo 撤销最近一次补全：把改过的文件从备份还原。
//
//	POST   /api/library/complete/undo  还原
//	DELETE /api/library/complete/undo  只删备份（确认不用还原了，释放磁盘）
//
// 为什么是「最近一次」而不是「按任务 ID 撤销」：补全是「手动点一次跑一批」的操作，
// 用户想撤销的就是刚刚那次。保留完整历史只会让人选错。
//
// 还原成功后备份目录会被删掉 —— **撤销只做一次**（没有「重做」）。
// 有文件还原失败时备份会保留，用户能重试。
func (s *Server) HandleCompleteUndo(w http.ResponseWriter, r *http.Request) {
	latest := complete.FindLatest(s.aiBackupRoot)
	if latest == "" {
		errJSON(w, http.StatusBadRequest, "没有可撤销的补全记录")
		return
	}

	switch r.Method {
	case http.MethodPost:
		res := complete.Restore(latest)

		// 还原后索引里的记录全部过期（标题/歌词/年份都可能变了）——
		// 用**实际文件**重新读一遍。不刷新的话下一轮补全会因为「索引说有歌词」
		// 而跳过这些文件，用户就会看到「撤销了但再也补不上」。
		for _, p := range res.RestoredPaths {
			meta, err := tags.Read(p)
			if err != nil {
				// 读不回来就清掉「已读」标记，交给下一轮增量扫描重读。
				// 关键是**不能**保留补全后的旧值 —— 那会让索引永久偏离文件真实内容。
				s.libraryIndex().InvalidateTagRead(p)
				continue
			}
			needLyric, needCover, terr := tidy.NeedsTidy(p)
			if terr != nil {
				needLyric = strings.TrimSpace(meta.Lyric) == ""
				needCover = len(meta.Cover) == 0
			}
			s.libraryIndex().MarkEnriched(p, library.Enrich{
				Title:    meta.Title,
				Artist:   meta.Artist,
				Album:    meta.Album,
				Genre:    meta.Genre,
				Year:     meta.Year,
				Track:    meta.Track,
				Disc:     meta.Disc,
				HasLyric: !needLyric,
				HasCover: !needCover,
			})
		}
		if len(res.RestoredPaths) > 0 {
			_ = s.libraryIndex().Save()
		}

		if res.Restored == 0 && res.Failed > 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"code": 500, "message": "还原失败，备份已保留可重试", "data": res,
			})
			return
		}
		msg := fmt.Sprintf("已还原 %d 个文件", res.Restored)
		if res.Removed > 0 {
			msg += fmt.Sprintf("，清理了 %d 个新建的歌词/封面", res.Removed)
		}
		if res.Failed > 0 {
			msg += fmt.Sprintf("，%d 个失败（备份已保留，可重试）", res.Failed)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": msg, "data": res,
		})

	case http.MethodDelete:
		if err := os.RemoveAll(latest); err != nil {
			errJSON(w, http.StatusInternalServerError, "删除备份失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "备份已删除"})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
