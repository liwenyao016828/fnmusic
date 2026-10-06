// Package account 管理第三方音乐平台的登录账号（当前支持网易云）。
//
// 设计要点：
//   - 账号凭据（Cookie）含隐私，落盘权限限制为 0600，且不写入日志。
//   - 登录采用扫码方式，避免在应用里收集用户密码。
//   - 平台数据接口走 legacy /api/ 路径（纯 GET + Cookie），无需 weapi 加密参数。
package account

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 支持的平台标识
const (
	ProviderNetease = "netease"
	ProviderQQ      = "qq"
)

// Account 一个已连接的音乐平台账号。
type Account struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	Cookie      string `json:"cookie"`
	UID         string `json:"uid,omitempty"`
	Nickname    string `json:"nickname,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	// Status: connected / expired
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at"`
}

// Store 账号存储（JSON 文件，权限 0600）。
type Store struct {
	path  string
	mu    sync.RWMutex
	items map[string]Account
}

// NewStore 创建账号存储。dataDir 通常为 ConfigManager.DataDir()。
func NewStore(dataDir string) *Store {
	s := &Store{
		path:  filepath.Join(dataDir, "accounts.json"),
		items: make(map[string]Account),
	}
	s.load()
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []Account
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, a := range list {
		if a.Provider != "" {
			s.items[a.Provider] = a
		}
	}
}

// persist 原子落盘（写临时文件后改名），并限制权限为 0600。
// 调用方需自行持有写锁。
func (s *Store) persist() error {
	list := make([]Account, 0, len(s.items))
	for _, a := range s.items {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Provider < list[j].Provider })

	data, err := json.MarshalIndent(list, "", "  ")
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

// List 返回全部账号（不含 Cookie，避免凭据外泄到接口响应里）。
func (s *Store) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.items))
	for _, a := range s.items {
		a.Cookie = ""
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Get 取出含 Cookie 的账号，供内部调用平台接口使用。
func (s *Store) Get(provider string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.items[provider]
	return a, ok
}

// Cookie 返回指定平台的 Cookie；未连接时返回空串。
func (s *Store) Cookie(provider string) string {
	a, _ := s.Get(provider)
	return a.Cookie
}

// Save 写入或更新账号。
func (s *Store) Save(a Account) error {
	if a.Provider == "" {
		return fmt.Errorf("provider 不能为空")
	}
	if a.Status == "" {
		a.Status = "connected"
	}
	a.UpdatedAt = time.Now().Unix()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[a.Provider] = a
	return s.persist()
}

// Delete 断开指定平台账号，返回是否确实存在过。
func (s *Store) Delete(provider string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[provider]; !ok {
		return false
	}
	delete(s.items, provider)
	_ = s.persist()
	return true
}
