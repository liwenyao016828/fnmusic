package appupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── 「更新完成后第一次进入应用，提示更新完成与版本号」（用户 2026-10-25）──
//
// 需求原话：「更新完成后第一次进入应用，提示更新完成和版本号」。
// 追问时用户拍板「变化就提示」—— 判据是**版本号变了**，不是「走完了应用内安装」。
// 这样手动装 fpk、飞牛应用中心升级、甚至从新版本降回旧版本，都会提示一次。
//
// 实现要点（踩过的坑都在这里）：
//
//  1. 状态必须落在**磁盘**上。页面上原来只有在内存里的 installed 状态，
//     装完备注一刷新就丢，所以「安装成功」提示出现一次就永远看不到了。
//  2. 用「上次运行时的版本号」比对当前版本，而不是「安装成功时写标记」：
//     安装脚本是由独立进程跑的，它没法可靠地回头告诉新进程「我刚装完」；
//     而「进程启动时版本和上次记录的不一样」是**一定成立**的判据。
//  3. 提示只给一次：读完就把 acknowledged 置 true；同一版本不再提醒。
//  4. 首次运行（没有记录文件）**不提示**：那不是「更新」，是「刚装上」。

// LastRun 是「上次运行时」的快照文件，落在数据目录下。
type lastRunFile struct {
	// Version 是上次启动时跑的版本号。
	Version string `json:"version"`
	// Acknowledged 表示这次「版本变化」的提示已经给过用户了。
	Acknowledged bool `json:"acknowledged"`
	// At 是记录时间（RFC3339），排障用。
	At string `json:"at"`
	// FromVersion 是「变化前」的版本号，用于提示文案「从 X 更新到 Y」。
	FromVersion string `json:"from_version,omitempty"`
}

// lastRunName 是快照文件名。放数据目录而不是 /tmp：/tmp 会被系统清理，
// 一清理就会在下次启动时「看不出更新过」。
const lastRunName = "last_run.json"

func (m *Manager) lastRunPath() string {
	if strings.TrimSpace(m.dataDir) == "" {
		return ""
	}
	return filepath.Join(m.dataDir, lastRunName)
}

// UpdateNotice 是 GET /api/app/update/notice 的返回体。
type UpdateNotice struct {
	// Show 为 true 时前端才弹提示。
	Show bool `json:"show"`
	// Version 是当前（新）版本号。
	Version string `json:"version"`
	// FromVersion 是变化前的版本号（首次运行时为空）。
	FromVersion string `json:"from_version,omitempty"`
	// Direction 是 up / down / unknown，前端据此换文案（降级别写「更新成功」）。
	Direction string `json:"direction,omitempty"`
	// Notes 是 release 说明（可能为空：首次启动时还没检查过）。
	Notes string `json:"notes,omitempty"`
}

// Notice 判断是否需要提示「版本变了」。
//
// 语义：把磁盘上记录的版本和当前版本比一次。
//   - 没有记录文件 → 首次运行，写记录、不提示（这不是更新）。
//   - 版本相同     → 不提示；若 acknowledged=false 也顺手置 true（老数据自愈）。
//   - 版本不同     → 提示一次（Show=true），此时**不改** acknowledged：
//     等前端调 AckNotice 才算真的给过了 —— 用户可能在提示出现前就关了页面。
func (m *Manager) Notice() UpdateNotice {
	rec, ok := m.readLastRun()
	cur := strings.TrimSpace(m.current)
	if cur == "" {
		return UpdateNotice{}
	}
	if !ok || strings.TrimSpace(rec.Version) == "" {
		// 首次运行：只落记录，不提示。
		m.writeLastRun(lastRunFile{Version: cur, Acknowledged: true, At: m.now().Format(time.RFC3339)})
		return UpdateNotice{Version: cur}
	}
	if strings.TrimSpace(rec.Version) == cur {
		return UpdateNotice{Version: cur}
	}
	dir := "up"
	switch {
	case IsNewer(cur, rec.Version):
		dir = "up"
	case IsNewer(rec.Version, cur):
		dir = "down"
	default:
		dir = "unknown" // 版本号不可比较（比如 dev 后缀），仍然提示但要弱化措辞
	}
	return UpdateNotice{
		Show:        true,
		Version:     cur,
		FromVersion: rec.Version,
		Direction:   dir,
	}
}

// AckNotice 记录「用户已经看到提示」，并**把当前版本写成基准**。
//
// 为什么在这里才落库而不是在 Notice 里：Notice 可能被前端在后台轮询调用，
// 一调用就落库会让提示「还没渲染就被标记为已读」。落库时机必须是用户确认。
// 参数 version 用来防「旧页面点了确认把新版本的记录覆盖掉」—— 不等就直接忽略。
func (m *Manager) AckNotice(version string) bool {
	cur := strings.TrimSpace(m.current)
	v := strings.TrimSpace(version)
	if v != "" && v != cur {
		// 前端拿的是旧版页面（应用刚被更新过），它的确认没有意义：
		// 若照写，会把「新版本的提示」静默作废。
		return false
	}
	rec, _ := m.readLastRun()
	m.writeLastRun(lastRunFile{
		Version:      cur,
		Acknowledged: true,
		At:           m.now().Format(time.RFC3339),
		FromVersion:  rec.Version,
	})
	return true
}

// NoticeNotes 在检查结果已经拿到说明时，给提示补一段 release 说明。
// 只在前端已经调用过 check 的场景用；Notice 本身不联网（启动路径要快）。
func (m *Manager) NoticeNotes(n UpdateNotice) UpdateNotice {
	m.mu.Lock()
	res := m.cache
	has := m.hasCache
	m.mu.Unlock()
	if !has || strings.TrimSpace(res.Notes) == "" {
		return n
	}
	// 只有当缓存里的版本正好等于当前版本时，说明才是「这次更新」的说明。
	if strings.TrimPrefix(strings.TrimSpace(res.Latest), "v") == n.Version {
		n.Notes = res.Notes
	}
	return n
}

func (m *Manager) readLastRun() (lastRunFile, bool) {
	p := m.lastRunPath()
	if p == "" {
		return lastRunFile{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return lastRunFile{}, false
	}
	var rec lastRunFile
	if err := json.Unmarshal(data, &rec); err != nil {
		// 文件坏了就当没有：宁可少提示一次，也不能让启动路径报错。
		return lastRunFile{}, false
	}
	return rec, true
}

func (m *Manager) writeLastRun(rec lastRunFile) {
	p := m.lastRunPath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.logf("[UPDATE] 写 last_run 失败（目录）：%v", err)
		return
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	// 先写临时文件再 rename：进程可能在写一半时被安装脚本杀掉，
	// 半个 JSON 会让下次启动读不出「上次版本」，等于丢一次提示。
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		m.logf("[UPDATE] 写 last_run 失败：%v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		m.logf("[UPDATE] 替换 last_run 失败：%v", err)
	}
}
