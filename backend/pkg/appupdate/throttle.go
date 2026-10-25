package appupdate

import "strings"

// tokenNow 是「每次用之前现取」的口径：Options.TokenFunc 返回的是当前配置
// （闭包读 config manager），Manager 在每轮检查/下载开头调用一次，
// 所以用户在设置里填完令牌、下一次动作就生效，不用重启进程。
func (m *Manager) tokenNow() string {
	if m.tokenFunc != nil {
		if v := strings.TrimSpace(m.tokenFunc()); v != "" {
			return v
		}
		return m.token
	}
	return m.token
}
