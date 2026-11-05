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
	"archive/zip"
	"fmt"
	"io"
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
	// lastErr 最近一次写盘/轮转失败的原因。写失败绝不能反过来打扰主流程
	// （所以这里仍然只记不抛），但**不能静默**：磁盘满、目录被删、权限变了
	// 都会让「默认落盘、出了问题必须能追查」这条承诺悄悄失效，
	// 用户等到要导出时才发现文件根本没有更新。
	lastErr string
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
		sink.lastErr = "" // 用户主动关掉落盘，不再提示历史写盘错误
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sink.lastErr = fmt.Sprintf("创建日志目录 %s 失败：%v", dir, err)
		return err
	}
	path := filepath.Join(dir, "qulv.log")
	if sink.f != nil && sink.path == path {
		return nil // 已经是这个文件了，别白折腾一次开关句柄
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		sink.lastErr = fmt.Sprintf("打开 %s 失败：%v", path, err)
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
	sink.lastErr = "" // 换目录成功，旧目录的错误不该继续显示
	return nil
}

// FilePath 当前落盘文件路径；未启用返回空串（界面据此显示「未开启」）。
func FilePath() string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.path
}

// DiskStatus 落盘状态的只读快照，供日志页与导出包展示。
type DiskStatus struct {
	Enabled bool   // 是否已配置目录且当前持有可写句柄
	Path    string // 当前文件路径；未启用为空
	Size    int64  // 当前文件已写字节数（应用视角，不等同于 stat 的实时大小）
	LastErr string // 最近一次写盘/轮转失败原因；成功写入后清空
}

// Status 返回当前落盘状态。HTTP 层据此在 GET /api/logs 里透出 disk_error，
// 导出包据此在 info.txt 里写明「为什么包里没有持久日志」。
func Status() DiskStatus {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return DiskStatus{
		Enabled: sink.f != nil && sink.path != "",
		Path:    sink.path,
		Size:    sink.size,
		LastErr: sink.lastErr,
	}
}

// readDiskFile 以**有界**方式读取一个落盘日志文件，供打包导出使用。
//
// 为什么不直接 os.ReadFile：单个文件上限 20MB（maxFileBytes），当前 + 上一代
// 一共 40MB。打包过程本身已经在内存里拼 zip，再无界读入会把峰值内存翻倍；
// 更要紧的是**文件可能被外部替换**（用户手工塞了一个几 GB 的 qulv.log），
// 不设上限的 ReadFile 会直接把 NAS 内存吃光。
//
// 调用方拿到 r 后必须 Close。返回的 r 已经套上 io.LimitReader，
// 最多读出 maxFileBytes 个字节；truncated 为真表示文件比上限还大、被截断。
func readDiskFile(path string) (r io.ReadCloser, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, false, fmt.Errorf("不是普通文件")
	}
	if fi.Size() > maxFileBytes {
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(f, maxFileBytes), f}, true, nil
	}
	return f, false, nil
}

// DiskFiles 返回当前落盘文件与上一代轮转文件的路径（存在的才返回）。
// 顺序固定为 [当前, 上一代]，导出包里的条目名也因此稳定。
//
// 只按「stat 得到」判断存在，**不**要求是普通文件：qulv.log 被外部换成目录 /
// 符号链接这种坏状态，正要靠 AddDiskFile 的读失败写进 info.txt 让用户看见，
// 在这里静默过滤掉就成了「包里没有、也不解释为什么」。
func DiskFiles() []string {
	sink.mu.Lock()
	path := sink.path
	sink.mu.Unlock()
	if path == "" {
		return nil
	}
	out := make([]string, 0, 2)
	for _, p := range []string{path, path + ".1"} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// AddDiskFile 把 path 指向的落盘日志以 name 为条目名写进 zip。
//
// 读取失败**不阻断导出**：少一份持久日志只是少一点现场，整个导出包失败
// 用户就什么都拿不到。失败原因由调用方写进 info.txt（见 logs_export.go）。
func AddDiskFile(zw *zip.Writer, path, name string) (written int64, truncated bool, err error) {
	r, truncated, err := readDiskFile(path)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = r.Close() }()
	w, err := zw.Create(name)
	if err != nil {
		return 0, false, err
	}
	// 再加一层上限：即使以后改了 readDiskFile，写出量也不会超过单文件上限。
	n, err := io.Copy(w, io.LimitReader(r, maxFileBytes))
	return n, truncated, err
}

// write 追加一行。未启用直接返回。
//
// 写失败**不抛错、不重试**（日志设施绝不能反过来打扰主流程），但把原因记到
// lastErr，由 Status() 透出到日志页与导出包 —— 见 fileSink.lastErr 的注释。
// 消息里的换行替换成可见符号，免得一条日志在文件里散成多行、没法 grep。
func (s *fileSink) write(t time.Time, level, source, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}
	one := strings.ReplaceAll(message, "\n", " ⏎ ")
	line := fmt.Sprintf("%s [%s][%s] %s\n", t.Format(time.RFC3339), level, source, one)
	n, err := s.f.WriteString(line)
	s.size += int64(n)
	if err != nil {
		s.lastErr = fmt.Sprintf("写入 %s 失败：%v", s.path, err)
		return // 部分写入的字节已计入 size；轮转交给下一次成功写入后判断
	}
	if s.size >= maxFileBytes {
		s.rotateLocked()
	}
	s.lastErr = "" // 有一次完整写入（含轮转）成功，之前的失败不再代表当前状态
}

// rotateLocked 把当前文件挪成 .1（旧 .1 直接覆盖），再开一个空的新文件。
// 只保留一代：日志是排障用的，最近两代足够定位「昨晚出的问题」，
// 留太多代反而让用户不敢开这个功能（占盘）。
//
// 失败不重试（轮转失败最多是文件继续变大，不该影响写日志本身），但记 lastErr：
// rename 失败**不能**当作成功，否则 qulv.log.1 里是好几代之前的旧内容，
// 导出包会误导排障的人。
func (s *fileSink) rotateLocked() {
	if s.f == nil || s.path == "" {
		return
	}
	_ = s.f.Close()
	s.f = nil

	backup := s.path + ".1"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		// 删不掉旧的 .1（权限/占用）→ 不继续 rename：硬 rename 可能失败，
		// 让后续 OpenFile 继续往原文件追加，并把原因暴露出去。
		s.lastErr = fmt.Sprintf("轮转：删除 %s 失败：%v", backup, err)
	} else if err := os.Rename(s.path, backup); err != nil {
		s.lastErr = fmt.Sprintf("轮转：%s 改名为 %s 失败：%v", s.path, backup, err)
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// 新文件开不出来（目录被删/权限变了）→ 关闭落盘，让「日志页」如实显示未启用，
		// 而不是继续往一个已经关掉的句柄里写、假装一切正常。
		s.lastErr = fmt.Sprintf("轮转后重新打开 %s 失败：%v", s.path, err)
		s.path, s.size = "", 0
		return
	}
	s.f, s.size = f, 0
}
