package applog

// 落盘副本（2026-10-26 用户口径「设置增加日志存储位置」；
// 2026-11-01 用户口径「出了问题必须能追查」——改成默认开、加按大小轮转）。
//
// 开关与目录由 pkg/config 决定（见 ConfigManager.EffectiveLogDir）：
// 默认写到 <数据目录>/logs/qulv.log，用户可在设置里换目录或关掉。
// 每条进入缓冲区的日志（后端 + 前端上报）同时追加一行到 <目录>/qulv.log。
//
// 为什么不写单个无限增长的文件：这台机器是长期开着的 NAS，日志一天几百 KB
// 到几 MB，不轮转迟早把卷写满 —— 而写满日志卷会连带影响同卷的其它数据。
// 轮转策略见 rotateLocked：超过 maxFileBytes 就把 qulv.log 挪成 qulv.log.1，
// 只保留一代（旧的一代直接丢）。两代封顶 ≈ 2 × maxFileBytes。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 单个日志文件的大小上限。20MB 足够装下几天的排障现场，
// 两代封顶 40MB —— 对 NAS 存储卷是安全的量级。
const maxFileBytes int64 = 20 << 20

// fileSink 全局唯一；未启用时 f == nil，write 是空操作。
type fileSink struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64 // 当前文件已写字节数，用于判断轮转时机
}

var sink = &fileSink{}

// EnableFile 启用 / 切换 / 关闭落盘：dir 传空串 = 关闭。
// 目录建不出来、文件打不开等错误原样返回，由 HTTP 层转成 400 给界面。
//
// 切到**同一个目录**时不做任何事：设置页允许反复保存同一个位置，
// 每次重新 OpenFile 会先关掉正在写的句柄（别的 goroutine 此刻可能在写）。
func EnableFile(dir string) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		if sink.f != nil {
			_ = sink.f.Close()
			sink.f, sink.path, sink.size = nil, "", 0
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "qulv.log")
	if sink.f != nil && sink.path == path {
		return nil // 已经是这个文件了，别白折腾一次开关句柄
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	if sink.f != nil {
		_ = sink.f.Close()
	}
	sink.f, sink.path, sink.size = f, path, size
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
	line := fmt.Sprintf("%s [%s][%s] %s\n", t.Format("2006-01-02 15:04:05"), level, source, one)
	n, _ := s.f.WriteString(line)
	s.size += int64(n)
	if s.size >= maxFileBytes {
		s.rotateLocked()
	}
}

// rotateLocked 把当前文件挪成 .1（旧 .1 直接覆盖），再开一个空的新文件。
// 只保留一代：日志是排障用的，最近两代足够定位「昨晚出的问题」，
// 留太多代反而让用户不敢开这个功能（占盘）。
//
// 失败不重试、不报错：轮转失败最多是文件继续变大，不该影响写日志本身。
func (s *fileSink) rotateLocked() {
	if s.f == nil || s.path == "" {
		return
	}
	_ = s.f.Close()
	s.f = nil

	backup := s.path + ".1"
	_ = os.Remove(backup)
	_ = os.Rename(s.path, backup)

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// 新文件开不出来（目录被删/权限变了）→ 关闭落盘，让「日志页」如实显示未启用，
		// 而不是继续往一个已经关掉的句柄里写、假装一切正常。
		s.path, s.size = "", 0
		return
	}
	s.f, s.size = f, 0
}
