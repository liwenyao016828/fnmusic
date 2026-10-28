package assistant

import (
	"encoding/json"
	"os"
	"sync"
)

// Settings 助手设置
type Settings struct {
	Enabled  bool            `json:"enabled"`            // 助手总开关
	Mode     ExecutionMode   `json:"mode"`               // 执行模式
	Disabled map[string]bool `json:"disabled,omitempty"` // 用户在界面关掉的能力（能力级开关）
}

// SettingsPatch 局部更新补丁（nil 字段 = 不变；bool 用指针区分「未传」与「传 false」）
type SettingsPatch struct {
	Enabled  *bool
	Mode     ExecutionMode
	Disabled *map[string]bool
}

// DefaultSettings 默认设置。
//
// 默认启用 + ConfirmWrites：用户主动要求做助手，默认开符合预期；
// ConfirmWrites 是安全默认——写入类能力一律先确认。
func DefaultSettings() Settings {
	return Settings{
		Enabled:  true,
		Mode:     ModeConfirmWrites,
		Disabled: map[string]bool{},
	}
}

// SettingsStore 助手设置存储（落盘 assistant_config.json，0600）。
//
// 落盘理由：执行模式是「用户显式选择」的，重启后不应悄悄回到默认；
// 能力开关同理（用户关掉某项能力是长期意图）。
type SettingsStore struct {
	mu   sync.Mutex
	path string
	cfg  Settings
}

// NewSettingsStore 创建设置存储并加载已有配置
func NewSettingsStore(path string) *SettingsStore {
	st := &SettingsStore{path: path, cfg: DefaultSettings()}
	st.load()
	return st
}

// load 读取配置（文件不存在或损坏时保持默认——助手设置不是关键数据）
func (st *SettingsStore) load() {
	data, err := os.ReadFile(st.path)
	if err != nil {
		return
	}
	var cfg Settings
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}
	// 模式非法时回落默认（防止旧配置或手改配置把模式写成怪值）
	if !ValidMode(cfg.Mode) {
		cfg.Mode = ModeConfirmWrites
	}
	if cfg.Disabled == nil {
		cfg.Disabled = map[string]bool{}
	}
	st.cfg = cfg
}

// Get 返回当前设置
func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cfg
}

// Update 合并更新设置并落盘
func (st *SettingsStore) Update(patch SettingsPatch) Settings {
	st.mu.Lock()
	defer st.mu.Unlock()

	if patch.Enabled != nil {
		st.cfg.Enabled = *patch.Enabled
	}
	if patch.Mode != "" {
		if !ValidMode(patch.Mode) {
			// 非法模式静默忽略（界面下拉框不会发出非法值，这里防的是手工调用）
			patch.Mode = ""
		} else {
			st.cfg.Mode = patch.Mode
		}
	}
	if patch.Disabled != nil {
		st.cfg.Disabled = *patch.Disabled
	}
	st.persist()
	return st.cfg
}

// persist 落盘（0600：可能含用户行为偏好，与 ai_config.json 同级）
func (st *SettingsStore) persist() {
	data, err := json.MarshalIndent(st.cfg, "", "  ")
	if err != nil {
		return
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, st.path)
}
