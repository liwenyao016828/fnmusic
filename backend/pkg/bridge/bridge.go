// Package bridge 提供「AI/外部调用者 ↔ 浏览器音源运行时」的解析桥。
//
// 背景：本项目的第三方音源脚本（洛雪 lx 规范）只在浏览器里执行，
// 后端不内置、也无法执行任何在线音源解析逻辑（合规要求）。
// 因此当外部 AI 想下载某个平台的歌曲时，需要一个通道把「解析请求」
// 交给正在运行的网页端，由网页端调用音源脚本取得直链后再回传。
//
// 工作流：
//
//	AI:      POST /api/bridge/resolve        -> 拿到 job_id（状态 pending）
//	网页端:  GET  /api/bridge/pending        -> 轮询待处理任务
//	网页端:  POST /api/bridge/claim          -> 原子领取（避免多标签页重复处理）
//	网页端:  POST /api/bridge/result         -> 回传直链 / 错误
//	AI:      GET  /api/bridge/jobs/{job_id}  -> 轮询最终结果
//
// 若没有网页端在运行，任务会保持在 pending 直至过期。
package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 任务状态
const (
	StatusPending = "pending" // 等待网页端领取
	StatusClaimed = "claimed" // 网页端已领取，解析中
	StatusDone    = "done"    // 解析成功
	StatusFailed  = "failed"  // 解析失败
	StatusExpired = "expired" // 超时未被处理
)

// 默认过期时间
const (
	defaultTTL        = 5 * time.Minute
	defaultResultTTL  = 15 * time.Minute
	maxPendingPerPoll = 20
)

// ResolveRequest 解析请求（由外部调用者提供）
type ResolveRequest struct {
	Platform string `json:"platform"` // wy / tx / kg / kw
	Songmid  string `json:"songmid"`
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	Name     string `json:"name"`
	Singer   string `json:"singer"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Quality  string `json:"quality"`  // highest / flac / 320k ...
	Download bool   `json:"download"` // true 表示网页端解析后直接加入下载队列
}

// Job 一次解析任务
type Job struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Request    ResolveRequest `json:"request"`
	URL        string         `json:"url,omitempty"`
	Quality    string         `json:"quality,omitempty"`
	SourceID   string         `json:"source_id,omitempty"`
	SourceName string         `json:"source_name,omitempty"`
	Referer    string         `json:"referer,omitempty"`
	Error      string         `json:"error,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

// Manager 解析任务管理器（内存态，进程内并发安全）
type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewManager() *Manager {
	return &Manager{jobs: make(map[string]*Job)}
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(b)
}

// Create 创建解析任务
func (m *Manager) Create(req ResolveRequest) (*Job, error) {
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.Platform == "" {
		return nil, errors.New("platform 必填（wy / tx / kg / kw）")
	}
	if strings.TrimSpace(req.Songmid) == "" && strings.TrimSpace(req.ID) == "" && strings.TrimSpace(req.Hash) == "" {
		return nil, errors.New("songmid / id / hash 至少提供一个")
	}
	if strings.TrimSpace(req.Quality) == "" {
		req.Quality = "highest"
	}

	now := time.Now()
	job := &Job{
		ID:        newID(),
		Status:    StatusPending,
		Request:   req,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(defaultTTL),
	}

	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	return job, nil
}

// Get 查询任务（顺带处理过期）
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	m.expireLocked(job)
	return job, true
}

// Pending 返回当前待处理的任务（不改变状态）
func (m *Manager) Pending() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*Job, 0, maxPendingPerPoll)
	for _, job := range m.jobs {
		m.expireLocked(job)
		if job.Status != StatusPending {
			continue
		}
		out = append(out, job)
		if len(out) >= maxPendingPerPoll {
			break
		}
	}
	return out
}

// Claim 原子领取任务，避免多个标签页重复解析
func (m *Manager) Claim(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	if !ok {
		return nil, errors.New("任务不存在")
	}
	m.expireLocked(job)
	if job.Status == StatusExpired {
		return nil, errors.New("任务已过期")
	}
	if job.Status != StatusPending {
		return nil, fmt.Errorf("任务已被处理（当前状态 %s）", job.Status)
	}

	job.Status = StatusClaimed
	job.UpdatedAt = time.Now()
	return job, nil
}

// ResultInput 网页端回传的解析结果
type ResultInput struct {
	URL        string `json:"url"`
	Quality    string `json:"quality"`
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	Referer    string `json:"referer"`
	Error      string `json:"error"`
}

// Complete 回传解析结果（成功或失败）
func (m *Manager) Complete(id string, in ResultInput) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	if !ok {
		return nil, errors.New("任务不存在")
	}
	if job.Status == StatusDone || job.Status == StatusFailed {
		return job, nil // 幂等
	}
	if job.Status == StatusExpired {
		return nil, errors.New("任务已过期")
	}

	now := time.Now()
	job.UpdatedAt = now
	job.ExpiresAt = now.Add(defaultResultTTL)

	if strings.TrimSpace(in.Error) != "" || strings.TrimSpace(in.URL) == "" {
		job.Status = StatusFailed
		job.Error = strings.TrimSpace(in.Error)
		if job.Error == "" {
			job.Error = "页面未返回有效直链"
		}
		return job, nil
	}

	job.Status = StatusDone
	job.URL = strings.TrimSpace(in.URL)
	job.Quality = in.Quality
	job.SourceID = in.SourceID
	job.SourceName = in.SourceName
	job.Referer = in.Referer
	return job, nil
}

// Stats 返回队列概况
func (m *Manager) Stats() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := map[string]int{
		StatusPending: 0, StatusClaimed: 0, StatusDone: 0, StatusFailed: 0, StatusExpired: 0,
	}
	for _, job := range m.jobs {
		m.expireLocked(job)
		out[job.Status]++
	}
	return out
}

// GC 清理过期任务
func (m *Manager) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	now := time.Now()
	for id, job := range m.jobs {
		m.expireLocked(job)
		// 过期后再保留一段时间的可查询窗口
		if now.After(job.ExpiresAt.Add(defaultResultTTL)) {
			delete(m.jobs, id)
			removed++
		}
	}
	return removed
}

func (m *Manager) expireLocked(job *Job) {
	if (job.Status == StatusPending || job.Status == StatusClaimed) && time.Now().After(job.ExpiresAt) {
		job.Status = StatusExpired
		if job.Error == "" {
			job.Error = "任务超时：没有页面在运行或未及时处理。请打开曲率 后重试。"
		}
	}
}
