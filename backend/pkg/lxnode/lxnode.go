// Package lxnode 管理「服务端洛雪(LX)音源宿主」子进程。
//
// 背景：goal-a5eb2a23 的 ⑤ 选了方案 (b) —— 把洛雪脚本从浏览器搬到服务端 Node。
// 这**故意**破掉了原来「后端不执行第三方 JS」的约束，用户 2026-10-06 明确拍的板。
//
// 分工：本包只管**进程生命周期 + 调它**；脚本怎么跑、缺哪些浏览器全局由
// sidecar/lx_host/server.mjs 负责，并且它会自己把「缺什么」报出来（见 /health 的 wants）。
// 这样这里不需要对脚本做任何假设。
//
// 硬约束（对齐 sidecar 的处理）：**子进程挂了不能影响曲率** —— 所有错误都只让
// Resolve 返回失败，调用方照常回落，绝不 panic、绝不阻塞播放链路。
package lxnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// State 是宿主的运行状态（与 sidecar 的取值保持一致，前端复用同一套渲染）。
const (
	StateOff      = "off"
	StateStarting = "starting"
	StateReady    = "ready"
	StateFailed   = "failed"
	StateStopped  = "stopped"
)

// Config 是宿主的启动参数。
type Config struct {
	Enabled bool
	NodeBin string // 默认 "node"
	Script  string // lx_host/server.mjs 的绝对路径
	Dir     string // 音源脚本目录（用户自己的 .js 放这里）
	Port    int    // 默认 8920
}

// readyTimeout 是等宿主起来的上限。首次加载脚本目录可能较慢，给足。
const readyTimeout = 20 * time.Second

// searchTimeout 是**单次**搜索的预算。
//
// 比宿主内部的单源预算（8s）长一点，好让宿主自己那条「哪个源超时了」的
// 记录先发生（它比我们这边一刀砍掉更有信息量）；又必须比下载路径的总预算短得多
// —— 搜不到就回落，不能让一次洛雪搜索拖住整首歌的下载。
const searchTimeout = 12 * time.Second

// ErrNotRunning 表示宿主**没在跑**（开关关着 / 起不来 / 已经退出）。
//
// 单独一个哨兵错误是为了让调用方分得清两件事：
//
//	· 「洛雪源没开」——用户开关的正常状态，池静默少一条来源，不该刷日志；
//	· 「开着，但这次搜索失败了」——要留日志。
//
// 两者对下载池的结论都是「没有候选」，但对排障的意义完全不同。
var ErrNotRunning = errors.New("lx 宿主不可用")

// process 是本包用到的那部分子进程能力（测试替换它）。
type process interface {
	Start() error
	Stop() error
	Exited() <-chan struct{}
}

// Manager 持有宿主子进程。
type Manager struct {
	mu      sync.Mutex
	cfg     Config
	proc    process
	state   string
	err     string
	newProc func(cfg Config) process
}

// New 造一个 Manager（初始为 off）。
func New() *Manager {
	return &Manager{state: StateOff, newProc: func(c Config) process { return &execProc{cfg: c} }}
}

func (m *Manager) logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[LXNODE] "+format+"\n", a...)
}

// Apply 按配置起停。配置没变时不重建（与 sidecar 的语义一致）。
func (m *Manager) Apply(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !cfg.Enabled {
		m.stopLocked()
		m.state, m.err = StateOff, ""
		m.logf("已关闭")
		return
	}
	if m.proc != nil && m.cfg == cfg {
		return
	}
	m.stopLocked()
	m.cfg = cfg
	m.state, m.err = StateStarting, ""
	m.proc = m.newProc(cfg)
	if err := m.proc.Start(); err != nil {
		m.err = err.Error()
		m.state = StateFailed
		m.proc = nil
		m.logf("启动失败：%v", err)
		return
	}
	m.state = StateReady
	m.logf("已启动（脚本目录 %s，端口 %d）", cfg.Dir, cfg.port())
	go m.watch(m.proc)
}

func (c Config) port() int {
	if c.Port > 0 {
		return c.Port
	}
	return 8920
}

func (c Config) base() string { return fmt.Sprintf("http://127.0.0.1:%d", c.port()) }

// watch 盯住进程退出 —— 它死了状态要如实变成 failed，不能还挂着 ready。
func (m *Manager) watch(p process) {
	<-p.Exited()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc != p {
		return // 已经被替换掉的老进程
	}
	if m.state == StateReady || m.state == StateStarting {
		m.state, m.err = StateFailed, "lx 宿主进程退出"
	}
	m.proc = nil
	m.logf("进程已退出，状态置为 %s", m.state)
}

func (m *Manager) stopLocked() {
	if m.proc == nil {
		return
	}
	_ = m.proc.Stop()
	m.proc = nil
	if m.state != StateFailed {
		m.state = StateStopped
	}
}

// Stop 停掉宿主（进程退出时会自己把状态改成 failed，这里显式标记为 stopped）。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// Status 供 HTTP 层直接序列化。
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := map[string]any{"state": m.state}
	if m.err != "" {
		st["error"] = m.err
	}
	if m.state == StateReady {
		st["base"] = m.cfg.base()
		st["dir"] = m.cfg.Dir
	}
	return st
}

// resolveRequest 是发给宿主的解析请求体（形状与 host 的 /resolve 对齐）。
type resolveRequest struct {
	Source  string         `json:"source"`
	Info    map[string]any `json:"info"`
	Quality string         `json:"quality"`
}

// Resolve 让宿主解析一条直链。**任何失败都只返回 error**，调用方照常回落。
func (m *Manager) Resolve(ctx context.Context, source string, info map[string]any, quality string) (string, error) {
	m.mu.Lock()
	if m.state != StateReady || m.proc == nil {
		st := m.state
		m.mu.Unlock()
		// 包一层哨兵错误：字符串与以前一致，但调用方能 errors.Is 出「宿主没开」
		// （池里要静默跳过，见 pkg/intercept/lxsource.go）
		return "", fmt.Errorf("%w（%s）", ErrNotRunning, st)
	}
	base := m.cfg.base()
	m.mu.Unlock()

	// ⚠️ info 必须**嵌一层 musicInfo**：LX 脚本的契约是
	//   handler({ source, action, info: { musicInfo, type } })
	// 平铺传 {name, singer} 的话，所有脚本第一个判断
	//   if (!info?.musicInfo) reject('请求参数不完整')
	// 直接短路 —— 真机实测过，表现为 502 且 tried 里全是「请求参数不完整」。
	// 这里替调用方嵌好，免得每个调用点都要记得。
	body, _ := json.Marshal(resolveRequest{
		Source:  source,
		Info:    map[string]any{"musicInfo": info, "type": "musicUrl"},
		Quality: quality,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/resolve", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		URL   string `json:"url"`
		Error string `json:"error"`
		// 宿主会把「每个脚本为什么不行」放在这里。带上它，排障时不用再去翻宿主日志。
		Tried []struct {
			Script string `json:"script"`
			Error  string `json:"error"`
			Got    string `json:"got"`
		} `json:"tried"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.URL == "" {
		msg := out.Error
		if msg == "" {
			msg = resp.Status
		}
		parts := make([]string, 0, len(out.Tried))
		for _, tr := range out.Tried {
			detail := tr.Error
			if detail == "" {
				detail = "返回了非直链：" + tr.Got
			}
			parts = append(parts, tr.Script+" → "+detail)
		}
		if len(parts) > 0 {
			msg += "（" + strings.Join(parts, "；") + "）"
		}
		return "", errors.New(msg)
	}
	if !strings.HasPrefix(out.URL, "http") {
		return "", fmt.Errorf("宿主返回的不是直链：%q", out.URL)
	}
	return out.URL, nil
}

// ── 搜索 ─────────────────────────────────────────────────────────────────
//
// `musicSearch` **不在官方 LX 契约里**（官方文档的 `sources[k].actions` 只认
// musicUrl / lyric / pic）。它是音源脚本作者与第三方宿主之间的约定，形状只能在
// 真脚本上量。2026-10-06 用真脚本 `全豆要-聚合音源-V4.1.js` 的 `qsvip` 源实测到的：
//
//	入参  { source, action: 'musicSearch', info: { keyword, page, pagesize } }
//	      ⚠️ 搜索参数在 info **顶层**（与 musicUrl 的 info.musicInfo 正相反）；
//	         放错层脚本读不到 keyword，**不报错**，静默回 { isEnd: true, list: [] }；
//	         `pagesize` 不能写成 `limit`，否则脚本按默认 30 搜。
//	出参  Promise<{ isEnd, list: [{ id, songmid, hash, name, singer,
//	          albumName, duration(秒), pic, _raw }], total }>
//	失败  没声明 musicSearch 的源 → reject `action not support`
//	      上游挂了 → reject（HTTP 500 / 请求错误: …）
//	空结果**不是失败**：{ isEnd: true, list: [] }
//
// 这些都是宿主（sidecar/lx_host/server.mjs 的 /search）实测后的契约，本包只调它。

// Tried 是宿主报的「这个（脚本, 源）为什么没有结果」。
//
// 带上来是为了排障时不用翻宿主日志 —— 与 Resolve 的 tried 同一个用意。
type Tried struct {
	Script string `json:"script"`
	Source string `json:"source"`
	Error  string `json:"error"`
}

// Song 是宿主 /search 返回的一条归一化搜索结果。
//
// ⚠️ 字段**缺就是缺**，不要在这里补默认值：
//   - Duration 单位是**秒**（宿主已把上游毫秒压成秒），0 = 脚本没给；
//   - Quality / Size 只有脚本**真的给了**才非空/非零 —— 实测的 qsvip **两个都没给**。
//     池里对「缺体积」（不主动占优）与「缺音质」（算最低档）的处理完全不同，
//     在这里编一个默认档位就等于伪造决策依据。
type Song struct {
	Script string `json:"script"` // 哪个 .js 给的（排障用）
	Source string `json:"source"` // 洛雪源标识（qsvip / wy / …）
	ID     string `json:"id"`     // 平台内 id；宿主取 songmid ‖ id ‖ hash
	Name   string `json:"name"`
	Singer string `json:"singer"`
	Album  string `json:"album"`
	// Duration 是秒。0 = 脚本没给（不是「零秒」）。
	Duration int    `json:"duration"`
	Pic      string `json:"pic"`
	Quality  string `json:"quality"` // 缺省空串 = 脚本没给
	Size     int64  `json:"size"`    // 缺省 0 = 脚本没给
}

// searchRequest 是发给宿主的搜索请求体（形状与 host 的 /search 对齐）。
type searchRequest struct {
	Source  string `json:"source"` // 空 = 所有声明了 musicSearch 的源
	Keyword string `json:"keyword"`
	Page    int    `json:"page"`
	Limit   int    `json:"limit"`
}

// Search 让宿主去搜关键词。source 留空 = 所有声明了 musicSearch 的源。
//
// 「哪些脚本支持搜索」**不由这里判断** —— 宿主按每个脚本 inited 里声明的 actions
// 自己过滤，不支持的源它会明确记一笔「已跳过」。Go 侧不假设任何脚本支持搜索
// （这正是「别指望所有脚本都支持」那条要求的落点）。
//
// 硬约束与 Resolve 一致：**任何失败都只返回 error**，不 panic、不阻塞调用方。
// 「搜到 0 条」**不是**错误（搜索本来就可能没有命中），那时返回空切片 + nil。
// 只有「宿主没开」（ErrNotRunning）、连不上、响应不是 200 才算 error。
func (m *Manager) Search(ctx context.Context, source, keyword string, page, limit int) ([]Song, error) {
	m.mu.Lock()
	if m.state != StateReady || m.proc == nil {
		st := m.state
		m.mu.Unlock()
		return nil, fmt.Errorf("%w（%s）", ErrNotRunning, st)
	}
	base := m.cfg.base()
	m.mu.Unlock()

	if strings.TrimSpace(keyword) == "" {
		return nil, errors.New("空的搜索关键词")
	}
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 10
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	body, _ := json.Marshal(searchRequest{Source: source, Keyword: keyword, Page: page, Limit: limit})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		OK      bool    `json:"ok"`
		Results []Song  `json:"results"`
		Tried   []Tried `json:"tried"`
		Error   string  `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = resp.Status
		}
		if s := triedSummary(out.Tried); s != "" {
			msg += "（" + s + "）"
		}
		return nil, errors.New(msg)
	}
	// 200 但一条都没有：**不是错误**（这个关键词本来就可能没歌）。但把宿主给的
	// 「每个源为什么没有结果」播出来 —— 「上游 404」与「本来就没这首歌」在日志里
	// 必须能分清，否则「搜索接进来了但永远搜不到」这种问题根本没法查。
	if len(out.Results) == 0 && len(out.Tried) > 0 {
		m.logf("搜索 %q 无结果：%s", keyword, triedSummary(out.Tried))
	}
	return out.Results, nil
}

// SearchAll 搜**所有**声明了 musicSearch 的源，第 1 页。
//
// 参数形状专门对齐下载池的注入口（pkg/intercept.Config.LxSearch）——
// 池只关心「有没有同名同版的那一条」，不需要翻页。
func (m *Manager) SearchAll(ctx context.Context, keyword string, limit int) ([]Song, error) {
	return m.Search(ctx, "", keyword, 1, limit)
}

// triedSummary 把 tried 压成一行：「脚本/源 → 原因；…」。
func triedSummary(tried []Tried) string {
	if len(tried) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tried))
	for _, tr := range tried {
		who := tr.Script
		if tr.Source != "" {
			who += "/" + tr.Source
		}
		parts = append(parts, who+" → "+tr.Error)
	}
	return strings.Join(parts, "；")
}

// execProc 是真实的进程实现。
type execProc struct {
	cfg  Config
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProc) Start() error {
	node := p.cfg.NodeBin
	if node == "" {
		node = "node"
	}
	if _, err := exec.LookPath(node); err != nil {
		return fmt.Errorf("找不到可执行文件 %q：%w", node, err)
	}
	if p.cfg.Script == "" {
		return errors.New("没有配置 lx 宿主脚本路径")
	}
	if _, err := os.Stat(p.cfg.Script); err != nil {
		return fmt.Errorf("lx 宿主脚本不存在：%w", err)
	}
	args := []string{p.cfg.Script, "--dir", p.cfg.Dir, "--port", fmt.Sprint(p.cfg.port())}
	p.cmd = exec.Command(node, args...)
	p.cmd.Stdout, p.cmd.Stderr = os.Stderr, os.Stderr
	p.done = make(chan struct{})
	if err := p.cmd.Start(); err != nil {
		return err
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	// 等它真的能应答再算 ready —— 进程起来了但脚本目录加载失败的情况很常见。
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if ping(p.cfg.base()) {
			return nil
		}
		select {
		case <-p.done:
			return errors.New("lx 宿主启动后立刻退出了（看日志）")
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("lx 宿主在 20 秒内没有就绪")
}

func (p *execProc) Stop() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	return nil
}

func (p *execProc) Exited() <-chan struct{} {
	if p.done == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return p.done
}

// ping 探一次 /health。
func ping(base string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
