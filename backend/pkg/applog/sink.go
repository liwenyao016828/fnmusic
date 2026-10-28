package applog

// 落盘副本（2026-10-26 用户口径：「设置增加日志存储位置」）。
//
// 默认**不落盘**，Buffer 的「内存即清空」行为保持不变 —— 原则见包注释：日志里可能带
// 路径、令牌，默认不该长期留在 NAS 磁盘上。只有用户在设置里明确填了目录才启用：
// 每条进入缓冲区的日志（后端 + 前端上报）同时追加一行到 <目录>/qulv.log。
// 切换目录、改回空串关闭，都即时生效、无需重启。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// fileSink 全局唯一；未启用时 f == nil，write 是空操作。
type fileSink struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

var sink = &fileSink{}

// EnableFile 启用 / 切换 / 关闭落盘：dir 传空串 = 关闭。
// 目录建不出来、文件打不开等错误原样返回，由 HTTP 层转成 400 给界面。
func EnableFile(dir string) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		if sink.f != nil {
			_ = sink.f.Close()
			sink.f, sink.path = nil, ""
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "qulv.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if sink.f != nil {
		_ = sink.f.Close()
	}
	sink.f, sink.path = f, f.Name()
	return nil
}

// FilePath 当前落盘文件路径；未启用返回空串（界面据此显示「未开启」）。
func FilePath() string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.path
}

// write 追加一行。未启用直接返回；写失败也只吞掉 —— 日志设施绝不能反过来打扰主流程。
// 消息里的换行替换成可见符号，免得一条日志在文件里散成多行、没法 grep。
func (s *fileSink) write(t time.Time, level, source, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}
	one := strings.ReplaceAll(message, "\n", " ⏎ ")
	_, _ = fmt.Fprintf(s.f, "%s [%s][%s] %s\n", t.Format("2006-01-02 15:04:05"), level, source, one)
}
