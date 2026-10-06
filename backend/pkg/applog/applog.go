// Package applog 提供一个内存环形缓冲，把后端日志暴露给网页端。
//
// 背景：日志目前只写 stderr，而应用跑在 NAS 上 —— 想看「这个接口为什么失败」
// 只能 SSH 上去翻文件，对用户和其他接手项目的 AI 都很不方便。
// 这里保留最近若干条，通过 GET /api/logs 提供，网页端「日志」页直接渲染。
//
// 只放内存、不落盘：重启即清空。这样既不会撑爆 NAS 磁盘，
// 也不会把日志里可能出现的路径、令牌长期留在盘上。
//
// 前端错误经 POST /api/logs 上报（见 AddFrom），与后端日志汇到同一个缓冲区 ——
// 排查一次操作失败时，服务端和浏览器两侧的信息在同一个时间轴上。
package applog

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// 标准库 log 默认给每条加 "2026/09/18 13:53:13 " 前缀，而 Entry 自己已有 at 字段，
// 界面上会重复显示两遍时间。这里剥掉它 —— stderr 上仍然保留完整前缀，
// 只是缓冲区里不重复存。
var logTimePrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// Entry 一条日志
type Entry struct {
	At      string `json:"at"`     // HH:MM:SS
	Level   string `json:"level"`  // info / warn / error
	Source  string `json:"source"` // backend / frontend
	Message string `json:"message"`
}

// 日志来源。前端错误经 POST /api/logs 上报，与后端日志汇到同一个缓冲区，
// 前端据此区分「服务端发生了什么」和「我这次点击发生了什么」。
const (
	SourceBackend  = "backend"
	SourceFrontend = "frontend"
)

// maxMessageRunes 单条消息上限（按字符而非字节，避免截断多字节字符）。
// 前端上报的是原始错误，可能带整段堆栈 —— 不设上限会让缓冲区被一条撑爆。
const maxMessageRunes = 2000

// defaultMax 默认保留条数。
// 2000 条足以覆盖一次排查（日志多为启动信息与请求摘要），内存占用几百 KB。
const defaultMax = 2000

// Buffer 定长环形缓冲
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	max     int
}

var std = New(defaultMax)

// Default 返回全局缓冲
func Default() *Buffer { return std }

// New 建一个缓冲；max <= 0 时用默认值
func New(max int) *Buffer {
	if max <= 0 {
		max = defaultMax
	}
	return &Buffer{entries: make([]Entry, 0, max), max: max}
}

// Write 实现 io.Writer，供 log.SetOutput 使用。
//
// 标准库 log 每条日志调一次 Write、末尾带换行；这里按行拆开存，
// 避免把多行内容挤成一条。
func (b *Buffer) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.Add(line)
	}
	return len(p), nil
}

// Add 追加一条日志
func (b *Buffer) Add(message string) {
	b.push(Entry{
		At:      time.Now().Format("15:04:05"),
		Level:   levelOf(message),
		Source:  SourceBackend,
		Message: logTimePrefix.ReplaceAllString(message, ""),
	})
}

// AddFrom 追加一条**外部上报**的日志（网页端提交的前端错误）。
//
// 与 Add 的区别：级别由调用方显式给出 —— 前端明确知道自己抛的是 error 还是 warn，
// 不必靠关键词去猜；来源也由调用方指定，方便前端按「服务端 / 浏览器」筛选。
func (b *Buffer) AddFrom(source, level, message string) {
	if level != "warn" && level != "error" {
		level = "info"
	}
	if source == "" {
		source = SourceFrontend
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	if r := []rune(msg); len(r) > maxMessageRunes {
		msg = string(r[:maxMessageRunes]) + "…"
	}
	b.push(Entry{
		At:      time.Now().Format("15:04:05"),
		Level:   level,
		Source:  source,
		Message: msg,
	})
}

// push 入队（含淘汰）
func (b *Buffer) push(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) >= b.max {
		// 一次丢一批而不是一条：避免每条日志都做一次整体搬移
		drop := b.max / 10
		if drop < 1 {
			drop = 1
		}
		b.entries = append(b.entries[:0], b.entries[drop:]...)
	}
	b.entries = append(b.entries, e)
}

// Recent 返回最近 limit 条，按时间正序（前端可直接从上往下渲染）。
func (b *Buffer) Recent(limit int) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > len(b.entries) {
		limit = len(b.entries)
	}
	out := make([]Entry, limit)
	copy(out, b.entries[len(b.entries)-limit:])
	return out
}

// Len 当前条数
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.entries)
}

// Clear 清空（供「清空」按钮使用）
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = b.entries[:0]
}

// levelOf 从文本里粗判级别。
//
// 标准库 log 没有级别概念，这里按关键词判 —— 够前端做颜色区分就行，
// 不追求精确。真正的级别应该在写日志时就带上，那是更大的改动。
func levelOf(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "[error]") || strings.Contains(l, "[fatal]") ||
		strings.Contains(l, "failed") || strings.Contains(l, "error:"):
		return "error"
	case strings.Contains(l, "[warn]") || strings.Contains(l, "warning"):
		return "warn"
	}
	return "info"
}
