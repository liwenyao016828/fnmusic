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
	"log"
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
		// 文件不存在是首次启动的正常情况，不吵；其它读取错误要留痕，
		// 否则「队列列表空了」会被当成「用户自己清空的」。
		if !os.IsNotExist(err) {
			log.Printf("[DOWNLOADQ] 旧队列文件读不了，按空队列启动：%v", err)
		}
		return
	}
	var p persisted
	if json.Unmarshal(data, &p) != nil {
		// 解析失败是**真**数据丢失风险（用户攒的下载记录），必须记下来。
		log.Printf("[DOWNLOADQ] 旧队列文件解析失败，按空队列启动（文件 %s，%d 字节）", s.path, len(data))
		return
	}
	s.tasks = p.Tasks
	if n := len(s.tasks); n > 0 {
		log.Printf("[DOWNLOADQ] 已载入网页端旧下载队列：%d 条", n)
	}
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
	if err := s.persistLocked(); err != nil {
		// 落盘失败时队列只存在于内存：重启就没了，用户看到的是「记录又丢了」。
		log.Printf("[DOWNLOADQ] 队列写盘失败（本次改动重启后会丢）：%v", err)
		return err
	}
	return nil
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
