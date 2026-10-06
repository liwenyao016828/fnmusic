package push

// 推送任务的持久化。
//
// 模型（参考 fnmusic-flow 的 subscriptions + push_targets，做了简化）：
//   - 一个 Task 把「某个源（账号歌单 / 日推）」绑定到「某个飞牛音乐歌单」
//   - IntervalMinutes > 0 时由调度器定时执行；= 0 表示只手动跑
//   - FnosGUID 是已推送歌单的稳定身份：一旦写入就复用它，避免重名建新单

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxRuns 保留的执行历史条数。
const maxRuns = 200

// Task 一个推送任务。
type Task struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`    // netease / qq
	SourceKind string `json:"source_kind"` // daily / playlist / link（link 时 source_id 存的是歌单链接）
	SourceID   string `json:"source_id,omitempty"`
	// SourceTitle 上次拉取到的源名称，便于界面展示
	SourceTitle string `json:"source_title,omitempty"`

	TargetTitle string `json:"target_title"` // 飞牛歌单名
	FnosGUID    string `json:"fnos_guid,omitempty"`
	SyncCover   bool   `json:"sync_cover"`

	// RetentionMode / RetentionDays 保留期策略（见 retention.go）。
	// keep（默认）推上去就不动；days 表示超过 RetentionDays 天自动从歌单移除。
	RetentionMode string `json:"retention_mode,omitempty"`
	RetentionDays int    `json:"retention_days,omitempty"`
	// PushedTracks 本任务推送过的曲目清单（含加入时间）。
	// **保留期清理只在这个范围内进行** —— 绝不碰用户手动加进歌单的曲目。
	PushedTracks []PushedTrack `json:"pushed_tracks,omitempty"`

	// IntervalMinutes 定时同步间隔；0 表示不定时（只手动执行）
	IntervalMinutes int  `json:"interval_minutes"`
	Enabled         bool `json:"enabled"`

	LastRunAt  int64   `json:"last_run_at,omitempty"`
	NextRunAt  int64   `json:"next_run_at,omitempty"`
	LastStatus string  `json:"last_status,omitempty"` // success / failed
	LastError  string  `json:"last_error,omitempty"`
	LastResult *Result `json:"last_result,omitempty"`

	CreatedAt int64 `json:"created_at"`
}

// Run 一次执行记录。
type Run struct {
	ID         int64   `json:"id"`
	TaskID     string  `json:"task_id"`
	TaskName   string  `json:"task_name"`
	StartedAt  int64   `json:"started_at"`
	DurationMs int64   `json:"duration_ms"`
	Status     string  `json:"status"` // success / failed
	Error      string  `json:"error,omitempty"`
	Trigger    string  `json:"trigger"` // manual / schedule
	Result     *Result `json:"result,omitempty"`
}

// Store 任务与执行历史的存储（JSON 文件）。
type Store struct {
	path  string
	mu    sync.RWMutex
	tasks map[string]Task
	runs  []Run
	seq   int64
}

// NewStore 创建存储。dataDir 通常为 ConfigManager.DataDir()。
func NewStore(dataDir string) *Store {
	s := &Store{
		path:  filepath.Join(dataDir, "push_tasks.json"),
		tasks: make(map[string]Task),
	}
	s.load()
	return s
}

type persisted struct {
	Seq   int64  `json:"seq"`
	Tasks []Task `json:"tasks"`
	Runs  []Run  `json:"runs"`
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var p persisted
	if json.Unmarshal(data, &p) != nil {
		return
	}
	for _, t := range p.Tasks {
		if t.ID != "" {
			s.tasks[t.ID] = t
		}
	}
	s.runs = p.Runs
	s.seq = p.Seq
}

// persist 原子落盘。调用方需持写锁。
func (s *Store) persist() error {
	p := persisted{Seq: s.seq, Runs: s.runs}
	for _, t := range s.tasks {
		p.Tasks = append(p.Tasks, t)
	}
	sort.Slice(p.Tasks, func(i, j int) bool { return p.Tasks[i].CreatedAt < p.Tasks[j].CreatedAt })

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List 返回全部任务（按创建时间升序）。
func (s *Store) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Get 取单个任务。
func (s *Store) Get(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

// Save 新建或更新任务。ID 为空时自动生成。
//
// 注意：本方法**不设业务默认值**（不会把 Enabled 改成 true）——
// 因为调度器回写任务时也走这里，若强制启用会把用户禁用的任务重新打开。
// 「新建默认启用」由 API 层负责。
func (s *Store) Save(t Task) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(t.Name) == "" {
		return Task{}, fmt.Errorf("请提供任务名称")
	}
	if t.Provider == "" || t.SourceKind == "" {
		return Task{}, fmt.Errorf("请提供 provider 与 source_kind")
	}
	if t.TargetTitle == "" {
		t.TargetTitle = t.Name
	}
	if t.ID == "" {
		t.ID = fmt.Sprintf("push_%d", time.Now().UnixNano())
		t.CreatedAt = time.Now().Unix()
	} else if old, ok := s.tasks[t.ID]; ok {
		t.CreatedAt = old.CreatedAt
	}
	// 改了间隔就重算下次执行时间
	t.NextRunAt = nextRun(t.IntervalMinutes)
	s.tasks[t.ID] = t

	if err := s.persist(); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Delete 删除任务。
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return false
	}
	delete(s.tasks, id)
	_ = s.persist()
	return true
}

// Due 返回当前到点且启用的任务。
func (s *Store) Due(now time.Time) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0)
	ts := now.Unix()
	for _, t := range s.tasks {
		if t.Enabled && t.IntervalMinutes > 0 && t.NextRunAt > 0 && t.NextRunAt <= ts {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt < out[j].NextRunAt })
	return out
}

// AddRun 记录一次执行结果，并同步更新任务的运行状态。
func (s *Store) AddRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	r.ID = s.seq
	s.runs = append(s.runs, r)
	if len(s.runs) > maxRuns {
		s.runs = s.runs[len(s.runs)-maxRuns:]
	}

	if t, ok := s.tasks[r.TaskID]; ok {
		t.LastRunAt = r.StartedAt
		t.LastStatus = r.Status
		t.LastError = r.Error
		t.LastResult = r.Result
		t.NextRunAt = nextRun(t.IntervalMinutes)
		s.tasks[r.TaskID] = t
	}
	return s.persist()
}

// Runs 返回执行历史（倒序，最多 limit 条；limit<=0 用默认 50）。
func (s *Store) Runs(taskID string, limit int) []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	out := make([]Run, 0, limit)
	for i := len(s.runs) - 1; i >= 0 && len(out) < limit; i-- {
		if taskID == "" || s.runs[i].TaskID == taskID {
			out = append(out, s.runs[i])
		}
	}
	return out
}

// nextRun 按间隔算出下次执行时间；间隔为 0 时返回 0（不定时）。
func nextRun(intervalMinutes int) int64 {
	if intervalMinutes <= 0 {
		return 0
	}
	return time.Now().Add(time.Duration(intervalMinutes) * time.Minute).Unix()
}
