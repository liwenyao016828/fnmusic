package push

// 推送任务的执行与定时调度。

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/fnos"
)

// errFnosUnavailable 飞牛音乐不可用（非飞牛环境、未读到令牌、或服务未运行）。
var errFnosUnavailable = errors.New("飞牛音乐不可用：未找到可用登录令牌或音乐服务未运行")

// Runner 执行推送任务。
type Runner struct {
	store    *Store
	accounts *account.Store
	// localIndex 本地曲库快照（可为 nil）。定时/手动推送都要带上它，
	// 否则「本地早就下好了」的曲目会被一路判成「缺失」，前端再下一遍。
	localIndex func() []LocalTrack
	stop       chan struct{}
	done       chan struct{}
}

// NewRunner 创建执行器。
func NewRunner(store *Store, accounts *account.Store) *Runner {
	return &Runner{store: store, accounts: accounts}
}

// SetLocalIndex 注入本地曲库快照（传 nil 等于不带）。
//
// 这个口子是为了补一个**只有定时/手动推送才会遇到**的错位（2026-11-01，issue #11）：
// 预览路径（api 层）一直传了 LocalIndex，真推路径（本包 doExecute）却漏了，
// 于是同一份歌单「预览说缺 3 首」、「真推也说缺 3 首，但那 3 首本地就有」，
// 而且「收编本地已有曲目」那段逻辑因为索引为 nil 永远不触发。
//
// 调用方应传一个**惰性**取值函数：曲库索引会随扫描/下载变化，抓一份快照存下来
// 就等于永远停在注入那一刻。
func (r *Runner) SetLocalIndex(fn func() []LocalTrack) {
	r.localIndex = fn
}

// Execute 执行一个任务：拉取源曲目 → 推送到飞牛歌单。
//
// 无论成功失败都会写入执行历史（便于排查），返回的 error 仅表示本次失败。
func (r *Runner) Execute(t Task, trigger string) (*Result, error) {
	startedAt := time.Now()
	run := Run{
		TaskID: t.ID, TaskName: t.Name,
		StartedAt: startedAt.Unix(), Trigger: trigger,
	}

	res, err := r.doExecute(t)

	run.DurationMs = time.Since(startedAt).Milliseconds()
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	} else {
		run.Status = "success"
		run.Result = res
	}
	// 历史写入失败不应影响本次结果
	if addErr := r.store.AddRun(run); addErr != nil {
		log.Printf("[PUSH] 写入执行历史失败: %v", addErr)
	}

	// 任务日志：一次推送「推了几首、命中几首、新增几首、失败在哪」必须留在
	// 运行日志里。以前只有定时的 runDue 打「成功/失败」一句话，手动触发那条
	// 路径（api 层）连一句都没有，用户报「推送没反应」时后端日志一片空白。
	// 触发方式（manual/schedule）也记上：同一个任务两种触发的行为差异要靠它分辨。
	if err != nil {
		log.Printf("[PUSH] 任务 %s(%s) 执行失败（触发 %s，耗时 %s）：%v",
			t.Name, t.ID, trigger, briefSpan(run.DurationMs), err)
	} else {
		log.Printf("[PUSH] 任务 %s(%s) 执行完成（触发 %s，耗时 %s）：源曲目 %d，命中 %d，新增 %d，已存在 %d，缺失 %d，本地命中 %d，过期移除 %d%s",
			t.Name, t.ID, trigger, briefSpan(run.DurationMs), res.Total, res.Matched,
			res.Added, res.AlreadyPresent, len(res.Missing), res.LocalMatched, res.Expired,
			pushWarn(res))
	}
	return res, err
}

// briefSpan 把毫秒数写成「850ms」「12.3s」「2m05s」。
func briefSpan(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// pushWarn 把「成功但有话要说」的情况并进同一行（封面/扫描警告）。
//
// 这些警告不影响结果，可一旦只放在返回体里，事后就再没人看得见 ——
// 而用户真正会来问的恰恰是「歌单封面怎么没更新」。
func pushWarn(res *Result) string {
	if res == nil {
		return ""
	}
	var parts []string
	if res.CoverWarning != "" {
		parts = append(parts, "封面："+res.CoverWarning)
	}
	if res.ScanWarning != "" {
		parts = append(parts, "扫描："+res.ScanWarning)
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "；") + "）"
}

func (r *Runner) doExecute(t Task) (*Result, error) {
	// 1. 拉取源曲目
	src, err := FetchSource(r.accounts, t.Provider, t.SourceKind, t.SourceID)
	if err != nil {
		return nil, err
	}

	// 2. 推送（复用单次推送的同一套匹配与写入逻辑）
	m := fnos.NewMusic("")
	if !m.Available() {
		return nil, errFnosUnavailable
	}

	target := t.TargetTitle
	if target == "" {
		target = src.Title
	}
	res, err := PushTracks(m, src.Tracks, Options{
		Title:     target,
		FnosGUID:  t.FnosGUID,
		CoverURL:  src.CoverURL,
		SyncCover: t.SyncCover,
		// 保留期：把策略与「此前推送过哪些」一并传入，清理只在这个范围内进行
		RetentionMode: t.RetentionMode,
		RetentionDays: t.RetentionDays,
		Pushed:        t.PushedTracks,
		// 本地曲库索引：**必须**带上（2026-11-01 修，issue #11）。
		// 漏了它的后果不是「少个字段」：本地早就下好的歌会一路被判「缺失」，
		// 推送结果里的「本地命中」恒为 0，前端据此再下一遍 —— 而预览路径一直
		// 是带了它的，两条路口径不一致，用户看到的就是「预览能对上、真推对不上」。
		LocalIndex: r.localIndex,
	})
	if err != nil {
		return nil, err
	}

	// 3. 首次推送成功后记下 guid，后续复用（稳定身份，避免重名建新单）
	if t.FnosGUID == "" && res.GUID != "" {
		t.FnosGUID = res.GUID
	}
	t.SourceTitle = src.Title
	// 回写推送清单（含本轮新增与清理结果），供下轮判断归属
	t.PushedTracks = res.Pushed
	if _, err := r.store.Save(t); err != nil {
		log.Printf("[PUSH] 回写任务 guid 失败: %v", err)
	}
	return res, nil
}

// Start 启动定时调度，每 interval 检查一次到点任务。
func (r *Runner) Start(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.runDue()
			}
		}
	}()
}

// Stop 停止调度。
func (r *Runner) Stop() {
	if r.stop == nil {
		return
	}
	close(r.stop)
	<-r.done
	r.stop = nil
}

// runDue 执行所有到点任务（串行，避免同时打满飞牛接口）。
func (r *Runner) runDue() {
	for _, t := range r.store.Due(time.Now()) {
		// 成败与细节都由 Execute 统一记录（含触发方式与耗时），这里不再重复打；
		// 只保留「这一轮定时检查跑了哪些任务」的上下文。
		log.Printf("[PUSH] 定时检查：到点任务 %s(%s)", t.Name, t.ID)
		if _, err := r.Execute(t, "schedule"); err != nil {
			continue
		}
	}
}
