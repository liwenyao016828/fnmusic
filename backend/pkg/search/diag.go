package search

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"
)

// 平台请求的健康度统计。
//
// 为什么要有这个文件：这个包在失败路径上原本只有 `return nil`，一声不响。
// QQ 的搜索接口下线之后，界面上唯一的症状是「搜到 0 条」—— 没有状态码、
// 没有日志，看着就像「这家平台没版权」。那次的排查力气全花在这个缺口上，
// 所以这里把「谁、什么时候、以什么方式失败」留下来。
//
// 记录点在 doHTTP（包内唯一的出站出口），平台代号由 URL 推断，
// 所以新增端点不需要记得来注册 —— 漏统计是静默的，不能靠记性。
//
// 两类信号分开计数，别混：
//
//   - failed：请求根本没成功（HTTP 非 200 或传输错误）—— 可以确定是故障。
//   - empty：请求成功但一条都没搜到 —— 不确定。冷门关键词会这样，
//     平台「静默返回空」（QQ 那次的形态）也会这样，所以它单独看，
//     算进 SuccessRate 会把「平台坏了」伪装成「这歌没有」。

// PlatformHealth 单个平台的累计健康状况。
type PlatformHealth struct {
	Platform        string  `json:"platform"`
	Requests        int64   `json:"requests"`
	OK              int64   `json:"ok"`
	HTTPFailed      int64   `json:"http_failed"`
	TransportFailed int64   `json:"transport_failed"`
	Empty           int64   `json:"empty"`
	SuccessRate     float64 `json:"success_rate"`
	LastError       string  `json:"last_error,omitempty"`
	LastErrorAt     string  `json:"last_error_at,omitempty"`
	LastEmptyAt     string  `json:"last_empty_at,omitempty"`
}

// DiagEvent 一条失败/空结果明细。
type DiagEvent struct {
	Platform string `json:"platform"`
	Kind     string `json:"kind"` // http_status | transport | empty
	Detail   string `json:"detail"`
	At       string `json:"at"`
}

const (
	// diagRingMax 明细环形缓冲容量。够看「刚才发生了什么」，不当日志用
	//（长期日志在 applog 里，这里只留结构化计数 + 最近若干条）。
	diagRingMax = 50
	// diagBodyHead 非 200 响应体只留开头这么多字节。错误原因通常就在头两百字节里；
	// QQ 那次的 500 是**空体**，而「它什么都不说」本身就是关键证据。
	diagBodyHead = 200
	// diagOtherLabel 未识别平台的桶名（例如新增平台还没在 platformOf 里登记）。
	diagOtherLabel = "other"
)

type platformStat struct {
	requests        int64
	ok              int64
	httpFailed      int64
	transportFailed int64
	empty           int64
	lastError       string
	lastErrorAt     time.Time
	lastEmptyAt     time.Time
}

type diagState struct {
	mu     sync.Mutex
	stats  map[string]*platformStat
	events []DiagEvent // 环形，最新在前
}

var diag = &diagState{stats: map[string]*platformStat{}}

// normPlatform 把空平台名归一成 other。
func normPlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return diagOtherLabel
	}
	return platform
}

// statForLocked 取（或建）平台统计，调用方必须持有 diag.mu。
func (d *diagState) statForLocked(platform string) *platformStat {
	st := d.stats[platform]
	if st == nil {
		st = &platformStat{}
		d.stats[platform] = st
	}
	return st
}

// pushEventLocked 把事件插到最前面并裁剪容量，调用方必须持有 diag.mu。
func (d *diagState) pushEventLocked(ev DiagEvent) {
	d.events = append([]DiagEvent{ev}, d.events...)
	if len(d.events) > diagRingMax {
		d.events = d.events[:diagRingMax]
	}
}

// failureCount 读某平台已累计的硬失败数（HTTP 非 200 + 网络层）。
//
// 给 cachedSearch 用：请求失败后调用方只看到空气列表，若照记一条 empty，
// 「平台挂了」就会同时写成 failed 和 empty —— 两种信号搅在一起，看的人分不清
// 是「这首歌不存在」还是「这个平台不存在」。空结果只统计请求本身成功的那些。
// 只读不建表：没见过的平台不该因为一次查询凭空多出一行零。
func failureCount(platform string) int64 {
	platform = normPlatform(platform)
	diag.mu.Lock()
	defer diag.mu.Unlock()
	st := diag.stats[platform]
	if st == nil {
		return 0
	}
	return st.httpFailed + st.transportFailed
}

// recordOK 记一次成功请求。
func recordOK(platform string) {
	platform = normPlatform(platform)
	diag.mu.Lock()
	defer diag.mu.Unlock()
	st := diag.statForLocked(platform)
	st.requests++
	st.ok++
}

// recordFailure 记一次失败请求，并往 applog 里写一行。
//
// applog 那一行的意义：前端上报的错误（网页端 appLog.js）和后端的平台故障
// 于是落在同一条时间轴上，排查「我点了下载没反应」时不用两头对表。
func recordFailure(platform, kind, detail string) {
	platform = normPlatform(platform)
	now := time.Now()
	diag.mu.Lock()
	st := diag.statForLocked(platform)
	st.requests++
	if kind == "transport" {
		st.transportFailed++
	} else {
		st.httpFailed++
	}
	st.lastError = truncRunes(detail, 300)
	st.lastErrorAt = now
	diag.pushEventLocked(DiagEvent{
		Platform: platform,
		Kind:     kind,
		Detail:   truncRunes(detail, 300),
		At:       now.Format(time.RFC3339),
	})
	diag.mu.Unlock()

	applog.Default().AddFrom("search", "warn",
		fmt.Sprintf("平台 %s %s：%s", platform, kindLabel(kind), detail))
}

// noteEmpty 记一次「请求成功但 0 条结果」。
//
// 不写 applog：空结果可能是正常的（冷门关键词），逐条记会把日志淹掉。
// 它的出口是 /api/diagnostics/platforms —— 空结果占比异常高时一眼可见。
func noteEmpty(platform, keyword string) {
	platform = normPlatform(platform)
	now := time.Now()
	diag.mu.Lock()
	defer diag.mu.Unlock()
	st := diag.statForLocked(platform)
	st.empty++
	st.lastEmptyAt = now
	diag.pushEventLocked(DiagEvent{
		Platform: platform,
		Kind:     "empty",
		Detail:   fmt.Sprintf("搜「%s」成功但 0 条结果", truncRunes(keyword, 60)),
		At:       now.Format(time.RFC3339),
	})
}

func kindLabel(kind string) string {
	switch kind {
	case "transport":
		return "网络层失败"
	case "http_status":
		return "HTTP 状态异常"
	case "empty":
		return "空结果"
	}
	return kind
}

// PlatformDiagnostics 返回各平台健康状况，按平台名排序（固定顺序便于肉眼比对）。
//
// Requests 覆盖该平台的**全部**出站请求（搜索 / 歌词 / 专辑详情），
// 因为「这家平台的接口是不是普遍不行」比「搜索接口单独不行」更有用。
func PlatformDiagnostics() []PlatformHealth {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	out := make([]PlatformHealth, 0, len(diag.stats))
	for name, st := range diag.stats {
		h := PlatformHealth{
			Platform:        name,
			Requests:        st.requests,
			OK:              st.ok,
			HTTPFailed:      st.httpFailed,
			TransportFailed: st.transportFailed,
			Empty:           st.empty,
			LastError:       st.lastError,
		}
		if st.requests > 0 {
			h.SuccessRate = math.Round(float64(st.ok)/float64(st.requests)*1000) / 1000
		}
		if !st.lastErrorAt.IsZero() {
			h.LastErrorAt = st.lastErrorAt.Format(time.RFC3339)
		}
		if !st.lastEmptyAt.IsZero() {
			h.LastEmptyAt = st.lastEmptyAt.Format(time.RFC3339)
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out
}

// RecentFailures 返回最近的失败 / 空结果明细，最新在前。
func RecentFailures(limit int) []DiagEvent {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	if limit <= 0 || limit > len(diag.events) {
		limit = len(diag.events)
	}
	out := make([]DiagEvent, limit)
	copy(out, diag.events[:limit])
	return out
}

// ResetDiagnostics 清空统计与明细（供 DELETE /api/diagnostics/platforms 与测试使用）。
func ResetDiagnostics() {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	diag.stats = map[string]*platformStat{}
	diag.events = nil
}

// platformOf 从请求 URL 推断平台代号（wy / tx / kg / kw，未识别归 other）。
//
// 为什么按 URL 猜而不是让调用点传：这个包里有十个 `doHTTP` 调用点，各自记得
// 传平台名，就迟早有一个忘传 —— 而漏统计是静默的，跟这个包以前的毛病一样。
// URL 是请求自带的事实，不依赖调用者的记性。
func platformOf(rawURL string) string {
	switch {
	case strings.Contains(rawURL, "music.163.com"):
		return "wy"
	case strings.Contains(rawURL, "kugou.com"):
		return "kg"
	case strings.Contains(rawURL, "kuwo.cn"):
		return "kw"
	case strings.Contains(rawURL, "qq.com"):
		return "tx"
	}
	return diagOtherLabel
}

// bodySnippet 把响应体开头压成一行，供失败明细显示；空体明确写出来。
func bodySnippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" {
		return "（响应体为空）"
	}
	return truncRunes(s, 160)
}

// truncRunes 按字符（不是字节）截断，避免把汉字切成半个。
func truncRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
