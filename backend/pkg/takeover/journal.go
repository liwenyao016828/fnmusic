package takeover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// journal 是接管动作的持久记录。
//
// 存在的唯一理由是「崩了能还原」：在动官方 socket 之前先把它落盘，
// 之后无论进程被 kill -9、断电、还是升级途中失败，下一次启动都能凭它
// 把官方 socket 挪回原位。没有它，「接管失败」就会变成「官方音乐永久打不开」。
type journal struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	PID       int    `json:"pid"`
	Target    string `json:"target"`
	Upstream  string `json:"upstream"`
	Staging   string `json:"staging,omitempty"`
}

func (m *Manager) journalPath() string {
	return filepath.Join(m.opts.DataDir, journalName)
}

func (m *Manager) lockPath() string {
	return filepath.Join(m.opts.DataDir, lockName)
}

// writeJournal 先写临时文件再 rename，保证磁盘上永远是一份完整的 journal。
func (m *Manager) writeJournal(staging string) error {
	j := journal{
		Version:   journalVersion,
		CreatedAt: time.Now().Format(time.RFC3339),
		PID:       os.Getpid(),
		Target:    m.opts.Target,
		Upstream:  m.opts.Upstream,
		Staging:   staging,
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("takeover: 序列化 journal 失败: %w", err)
	}
	if err := os.MkdirAll(m.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("takeover: 创建数据目录失败: %w", err)
	}
	path := m.journalPath()
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("takeover: 写 journal 失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("takeover: 落盘 journal 失败: %w", err)
	}
	return nil
}

// readJournal 读接管记录。没有记录时返回 ErrNoJournal。
func (m *Manager) readJournal() (*journal, error) {
	raw, err := os.ReadFile(m.journalPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoJournal
		}
		return nil, fmt.Errorf("takeover: 读 journal 失败: %w", err)
	}
	var j journal
	if err := json.Unmarshal(raw, &j); err != nil {
		// 坏掉的 journal 不能当成「没有接管」——那会让我们在不明状态下动手。
		// 但也确实没法从中还原，所以报错让上层决定。
		return nil, fmt.Errorf("takeover: journal 损坏（%v），请人工检查 %s", err, m.journalPath())
	}
	if j.Version != journalVersion {
		return nil, errors.New("takeover: journal 版本不匹配")
	}
	return &j, nil
}
