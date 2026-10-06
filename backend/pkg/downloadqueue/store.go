// Package downloadqueue 把网页端的下载队列持久化到 NAS。
//
// **为什么需要它**：下载队列以前只存在浏览器 `localStorage` 里 ——
// 换个浏览器、清一次缓存、或者用手机打开，记录就没了
// （2026-09-22 用户反馈：「歌曲下载记录没有了，是只保留在浏览器吗？可以保存到设备的」）。
// 队列描述的是「这台 NAS 上下过/正在下什么」，本来就该跟着设备走，而不是跟着浏览器。
//
// **这里刻意做得很薄**：整体读、整体写，后端**不解释**任务里的任何字段。
// 队列的状态机归前端 `downloadManager` 所有（任务字段还在演进），
// 后端只当它是一个不透明的 JSON 数组 —— 这样前端加字段不用改后端。
package downloadqueue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store 下载队列的 JSON 文件存储。
type Store struct {
	path string

	mu    sync.RWMutex
	tasks []json.RawMessage
}

// NewStore 创建存储。dataDir 通常为 ConfigManager.DataDir()。
func NewStore(dataDir string) *Store {
	s := &Store{path: filepath.Join(dataDir, "download_queue.json")}
	s.load()
	return s
}

type persisted struct {
	Tasks []json.RawMessage `json:"tasks"`
}

// load 读取磁盘上的队列。文件不存在/损坏时保持空队列（fail open：
// 队列丢了只是列表空，不该让整个应用起不来）。
func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var p persisted
	if json.Unmarshal(data, &p) != nil {
		return
	}
	s.tasks = p.Tasks
}

// All 返回全部任务（原样，不做任何解释）。返回的切片是副本，调用方可安全持有。
func (s *Store) All() []json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]json.RawMessage, len(s.tasks))
	copy(out, s.tasks)
	return out
}

// Replace 整体替换队列并落盘。
//
// ⚠️ 是**整体替换**而不是追加：队列的删除/暂停/重排都在前端做，
// 前端回传的就是「当前完整队列」。想改成增量接口的话，
// 先想清楚「前端删了一条、后端却还留着」怎么收场。
func (s *Store) Replace(tasks []json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tasks == nil {
		tasks = []json.RawMessage{}
	}
	s.tasks = tasks
	return s.persistLocked()
}

// persistLocked 原子落盘（先写 .tmp 再 rename，避免写一半断电留下半个文件）。
// 调用方需持写锁。
func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(persisted{Tasks: s.tasks}, "", "  ")
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
