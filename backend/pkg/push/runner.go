package push

// 推送任务的执行与定时调度。

import (
	"errors"
	"log"
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
	stop     chan struct{}
	done     chan struct{}
}

// NewRunner 创建执行器。
func NewRunner(store *Store, accounts *account.Store) *Runner {
	return &Runner{store: store, accounts: accounts}
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
	return res, err
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
		if _, err := r.Execute(t, "schedule"); err != nil {
			log.Printf("[PUSH] 任务 %s(%s) 定时执行失败: %v", t.Name, t.ID, err)
		} else {
			log.Printf("[PUSH] 任务 %s(%s) 定时执行成功", t.Name, t.ID)
		}
	}
}
