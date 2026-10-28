package assistant

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"fn-lx-player/pkg/ai"
)

// SessionMessage 会话里的一条消息（含工具调用与工具结果）
type SessionMessage struct {
	Role       string        `json:"role"` // user / assistant / tool / system
	Content    string        `json:"content"`
	ToolCalls  []ai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ActionID   string        `json:"action_id,omitempty"` // 该条是「待确认操作」结果时，关联的 action id
	At         time.Time     `json:"at"`
}

// Session 一个对话会话
type Session struct {
	ID        string           `json:"id"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Messages  []SessionMessage `json:"messages,omitempty"`
}

// sessionFile 落盘结构（version 便于以后迁移）
type sessionFile struct {
	Version  int       `json:"version"`
	Sessions []Session `json:"sessions"`
}

// SessionStore 会话仓库（2026-10-27 起支持落盘）。
//
// 之前只存内存，理由是「助手是工具执行者而非审计日志」；用户明确要
// **会话记录**（重开抽屉还能接着聊、能翻历史），这条不再成立。
// 现在的行为：path 为空 → 纯内存（测试用）；path 非空 → 每次变更后
// 原子落盘（临时文件 + rename）。落盘只保留「有消息」的会话，按
// UpdatedAt 新→旧截断到 maxSessions 条；被截断的旧会话同步从内存删除，
// 避免界面上挂着「重启即消失」的幽灵条目。
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// maxMessages 单会话保留的最大消息数（超出裁剪最旧的，保留上下文尾部）
	maxMessages int
	// maxSessions 落盘与列表保留的最大会话数
	maxSessions int
	// path 落盘文件路径；空 = 不落盘
	path string
}

// NewSessionStore 创建纯内存会话仓库（测试 / 无需历史的环境）
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions:    make(map[string]*Session),
		maxMessages: 60,
		maxSessions: 50,
	}
}

// NewFileSessionStore 创建落盘会话仓库并加载历史。
//
// 文件缺失（首次启动）或损坏（半截 JSON）都从空库开始：会话记录是
// 聊天上下文不是账本，不因半截文件拒绝启动，也不为此报错刷屏。
func NewFileSessionStore(path string) *SessionStore {
	s := NewSessionStore()
	s.path = path
	s.load()
	return s
}

// load 启动时从磁盘恢复历史（调用方保证此时无并发）
func (s *SessionStore) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var shape sessionFile
	if err := json.Unmarshal(b, &shape); err != nil {
		return
	}
	for i := range shape.Sessions {
		sess := shape.Sessions[i]
		if sess.ID == "" {
			continue
		}
		s.sessions[sess.ID] = &sess
	}
	s.trimLocked()
}

// saveLocked 落盘（调用方需持锁）。尽力而为：写失败不影响对话本身。
func (s *SessionStore) saveLocked() {
	if s.path == "" {
		return
	}
	out := s.trimForDiskLocked()
	b, err := json.Marshal(sessionFile{Version: 1, Sessions: out})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path) // 同目录 rename，原子
}

// trimForDiskLocked 计算落盘集合并同步修剪内存：
// 只保留有消息的会话，按 UpdatedAt 新→旧截断到 maxSessions。
func (s *SessionStore) trimForDiskLocked() []Session {
	kept := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if len(sess.Messages) > 0 {
			kept = append(kept, sess)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].UpdatedAt.After(kept[j].UpdatedAt) })
	if len(kept) > s.maxSessions {
		for _, sess := range kept[s.maxSessions:] {
			delete(s.sessions, sess.ID)
		}
		kept = kept[:s.maxSessions]
	}
	out := make([]Session, 0, len(kept))
	for _, sess := range kept {
		cp := *sess
		cp.Messages = append([]SessionMessage(nil), sess.Messages...)
		out = append(out, cp)
	}
	return out
}

// trimLocked 启动加载后按 maxSessions 截断（内存层面）
func (s *SessionStore) trimLocked() {
	if len(s.sessions) <= s.maxSessions {
		return
	}
	kept := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		kept = append(kept, sess)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].UpdatedAt.After(kept[j].UpdatedAt) })
	for _, sess := range kept[s.maxSessions:] {
		delete(s.sessions, sess.ID)
	}
}

// NewSessionID 生成可读的会话 ID：s + 日期时间 + 短随机后缀（防同一秒撞名）
func NewSessionID() string {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand 在正常环境不会失败；真失败了用时间戳兜底，宁可难看不要撞名
		return "s" + time.Now().Format("20060102-150405.000000000")
	}
	return "s" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix)
}

// Get 取会话（不存在返回 nil）
func (s *SessionStore) Get(id string) *Session {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// GetOrCreate 取会话，不存在则创建
func (s *SessionStore) GetOrCreate(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		sess = &Session{ID: id, CreatedAt: time.Now()}
		s.sessions[id] = sess
	}
	sess.UpdatedAt = time.Now()
	return sess
}

// Add 追加一条消息（自动裁剪超长会话）
func (s *SessionStore) Add(id string, msg SessionMessage) *Session {
	sess := s.GetOrCreate(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	msg.At = time.Now()
	sess.Messages = append(sess.Messages, msg)
	if len(sess.Messages) > s.maxMessages {
		// 保留首条（保留用户第一条提问做主题锚点）+ 尾部
		first := sess.Messages[:1]
		tail := sess.Messages[len(sess.Messages)-(s.maxMessages-1):]
		sess.Messages = append(first, tail...)
	}
	s.saveLocked()
	return sess
}

// ToMessages 把会话消息转成模型可消费的消息序列
func ToMessages(msgs []SessionMessage) []ai.Message {
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, ai.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}
	return out
}

// Clear 清空会话消息（会话本身保留，空会话不落盘）
func (s *SessionStore) Clear(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		sess.Messages = nil
		sess.UpdatedAt = time.Now()
		s.saveLocked()
	}
}

// Remove 删除整个会话（用户在会话列表里点删除）
func (s *SessionStore) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return
	}
	delete(s.sessions, id)
	s.saveLocked()
}

// History 取会话的**值拷贝**（消息切片深拷贝，避免调用方读到并发修改中的数据）
func (s *SessionStore) History(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	cp := *sess
	cp.Messages = append([]SessionMessage(nil), sess.Messages...)
	return cp, true
}

// List 列出全部会话，按 UpdatedAt 新→旧排序（用于会话列表：新的在最上面）
func (s *SessionStore) List() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}
