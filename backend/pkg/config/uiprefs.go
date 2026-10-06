package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// UiPrefsFileName 窗口记忆（各页面点击状态）的持久化文件名，与 config.json 同目录。
const UiPrefsFileName = "ui_prefs.json"

// MaxUIPrefsBytes 单个窗口记忆文件上限，防止异常写入撑爆磁盘。
// 各页面的 Tab / 平台 / 目录选择只是少量字符串，64KB 绰绰有余。
const MaxUIPrefsBytes = 64 << 10

var uiPrefKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// UIPrefsManager 管理「窗口点击记忆」的读写。
//
// 背景：前端各页面（发现页 Tab、榜单平台、搜索平台、NAS 目录、播放模式、当前音源）
// 的选中状态，原来只存在浏览器的 localStorage，换浏览器/清缓存就丢，多端也不共享。
// 这里把它们落到应用数据目录的 ui_prefs.json，前端通过 GET/POST /api/ui-prefs 读写。
type UIPrefsManager struct {
	mu       sync.RWMutex
	filePath string
	prefs    map[string]json.RawMessage
}

func NewUIPrefsManager(dataDir string) *UIPrefsManager {
	if dataDir == "" {
		dataDir = "./data"
	}
	m := &UIPrefsManager{
		filePath: filepath.Join(dataDir, UiPrefsFileName),
		prefs:    make(map[string]json.RawMessage),
	}
	m.load()
	return m
}

func (m *UIPrefsManager) load() {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return
	}
	if len(data) > MaxUIPrefsBytes {
		return
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	m.prefs = make(map[string]json.RawMessage, len(p))
	for k, v := range p {
		if !validUIPrefKey(k) {
			continue
		}
		cp := append(json.RawMessage(nil), v...)
		m.prefs[k] = cp
	}
}

// All 返回全部窗口记忆的拷贝。
func (m *UIPrefsManager) All() map[string]json.RawMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]json.RawMessage, len(m.prefs))
	for k, v := range m.prefs {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// FilePath 返回持久化文件路径（便于日志与排查）。
func (m *UIPrefsManager) FilePath() string {
	return m.filePath
}

// Merge 合并写入一批窗口记忆（调用方已做过大小限制），非法 key 直接报错。
// 合并成功后原子落盘：临时文件 + rename，避免中途崩溃留下半截 JSON。
func (m *UIPrefsManager) Merge(patch map[string]json.RawMessage) error {
	for k := range patch {
		if !validUIPrefKey(k) {
			return fmt.Errorf("非法的记忆键: %q", k)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	next := make(map[string]json.RawMessage, len(m.prefs)+len(patch))
	for k, v := range m.prefs {
		next[k] = v
	}
	for k, v := range patch {
		next[k] = append(json.RawMessage(nil), v...)
	}

	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(data) > MaxUIPrefsBytes {
		return fmt.Errorf("窗口记忆过大（%d 字节，上限 %d）", len(data), MaxUIPrefsBytes)
	}

	if err := atomicWriteFile(m.filePath, data, 0644); err != nil {
		return err
	}
	m.prefs = next
	return nil
}

func validUIPrefKey(k string) bool {
	return uiPrefKeyPattern.MatchString(k)
}

// atomicWriteFile 临时文件 + rename 原子写（同目录，保证 rename 原子性）。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
