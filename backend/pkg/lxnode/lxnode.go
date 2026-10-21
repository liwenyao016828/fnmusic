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
//
// # 进程级隔离（第三方脚本的真正边界）
//
// sidecar 里那个 `node:vm` + require 白名单**不是安全边界**，2026-10-06 实测过：
// 沙箱的全局兜底是「查宿主真全局」（server.mjs 的 Proxy `get` 会回落 `globalThis[k]`），
// 于是脚本拿得到宿主的 `process`：
//
//	process.getBuiltinModule('node:fs').readFileSync('/etc/hostname')      → 读到内容
//	process.getBuiltinModule('node:child_process').execSync('id -un')      → 执行成功
//	Function('return process')()                                           → 同样拿得到
//
// 所以**边界只能在进程这一层**：用 Node 的权限模型（`--permission`）启动子进程，
// 只放行「宿主脚本自身 + 音源脚本目录」的读权限，其余 fs 读、fs 写、child_process、
// worker、原生插件一律拒绝（都是 ERR_ACCESS_DENIED）。
//
// 逐个启动参数为什么是这个值：
//
//   - `--permission` 打开权限模型。默认全拒，再加 `--allow-fs-read` 逐项放行。
//   - `--allow-fs-read=<脚本目录>` **必须**：宿主 `fs.readdirSync(DIR)` 要列它、
//     `fs.readFileSync(<DIR>/*.js)` 要读它。Node 的目录路径是**递归**语义（子目录里的
//     文件也可读）—— 这是权限模型给不出更窄选项的地方，见 hostArgs 的注释。
//   - `--allow-fs-read=<宿主脚本>` **必须**（其实冗余）：Node 文档里应用入口是
//     自动放行的（本机 v22.20 实测确认：allow 指向别处时入口照样读得到）。这里仍然
//     显式写出来 —— 它不多给任何权限（那个文件本来就可读），但能让「隔离了什么」
//     在参数表里自解释，且不依赖「入口自动放行」这个版本相关行为。
//   - **不给** `--allow-fs-write` / `--allow-child-process` / `--allow-worker` /
//     `--allow-addons` / `--allow-wasi`：宿主一个都不用，脚本更不该用。白拿的收紧。
//   - **故意不给** `--allow-fs-read=*` 之类：那等于没隔离。用例钉着这条（见
//     TestHostArgsAreIsolatedByDefault）。
//
// 权限模型**不管网络**（本机 v22.20 连 `--allow-net` 都还没有），所以 `lx.request`
// 那条出网路径不受影响 —— 这是刻意的：音源脚本的活就是出网，收紧它等于把功能关掉。
//
// 权限模型**也不管环境变量**：`process.env` 照样可读。加上本机实测 `NODE_OPTIONS`
// 里能塞 `--allow-fs-read=/` 直接把权限模型放宽（Node 只禁了 `--permission` 本身，
// 没禁 `--allow-*`），所以子进程的环境变量**由这里显式指定**，只留 PATH/TZ，
// 不继承 NODE_OPTIONS、也不把宿主环境里的 token 暴露给脚本。见 childEnv。
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
	"path/filepath"
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
//
// ⚠️ **故意没有「关掉隔离」的开关**。进程级隔离（见包注释）是这个子进程存在的
// 前提条件之一，不是使用偏好：调用点在 backend/main.go 只有一处，永远想要它开着。
// 若做成 `Config.Isolate bool`，Go 的零值就是 **false** —— 「忘了赋值」会静默退回
// 「只有沙箱、没有边界」的旧状态，而那正是这次要修的东西。所以它硬编码在
// hostArgs() 里，并由 TestHostArgsAreIsolatedByDefault 钉住。
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
	// Scripts / Collect 是「轮询模式」的两个开关，2026-10-21 加。
	//
	// 用户在界面上选中的洛雪音源可能有好几个，语义是「按我的顺序挨个问，
	// 谁给的档位高用谁」。只给 Scripts 不给 Collect 时宿主仍只交回第一条能下的；
	// 两者都给，宿主把所有能下得动的候选都交回来，挑哪条由调用方定 ——
	// **档位高不高只有 Go 侧知道**（要拿时长按字节数反推码率，宿主没有时长）。
	Scripts []string `json:"scripts,omitempty"`
	Collect bool     `json:"collect,omitempty"`
}

// Candidate 是宿主在轮询模式下交回来的一个候选直链。
type Candidate struct {
	URL    string `json:"url"`
	Script string `json:"script"`
	// Status / Bytes 是宿主的实测结果（它发过 Range 请求）。
	Status int   `json:"status,omitempty"`
	Bytes  int64 `json:"bytes,omitempty"`
	// Unproven 非空 = 探测本身没结论（超时/DNS/连接重置），**不是死链**。
	// 这种候选排在最后，只在别的都不行时才用（口径与宿主单条模式一致）。
	Unproven string `json:"unproven,omitempty"`
}

// Resolve 让宿主解析一条直链。**任何失败都只返回 error**，调用方照常回落。
//
// 第二个返回值是宿主**实测**到的文件总字节数（它发过 Range 请求，从 content-range
// 读到的尾数），拿不到时为 0。它不是用来显示的：洛雪背后那些第三方站会把点名的
// 无损压成 128k mp3 而**不写在应答里**，字节数 + 时长是唯一能把这件事戳穿的证据
// （见 pkg/intercept/lxresolve.go 的 lxTierFromSize）。
func (m *Manager) Resolve(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error) {
	m.mu.Lock()
	if m.state != StateReady || m.proc == nil {
		st := m.state
		m.mu.Unlock()
		// 包一层哨兵错误：字符串与以前一致，但调用方能 errors.Is 出「宿主没开」
		// （池里要静默跳过，见 pkg/intercept/lxsource.go）
		return "", 0, fmt.Errorf("%w（%s）", ErrNotRunning, st)
	}
	base := m.cfg.base()
	m.mu.Unlock()

	// ⚠️ info 必须**嵌一层 musicInfo**：LX 脚本的契约是
	//   handler({ source, action, info: { musicInfo, type } })
	// 平铺传 {name, singer} 的话，所有脚本第一个判断
	//   if (!info?.musicInfo) reject('请求参数不完整')
	// 直接短路 —— 真机实测过，表现为 502 且 tried 里全是「请求参数不完整」。
	// 这里替调用方嵌好，免得每个调用点都要记得。
	//
	// ⚠️⚠️ `type` 是**档位**，不是动作名。LX 契约里 musicUrl 的档位就放在
	// `info.type` 上（形如 '320k' / 'flac'），`action` 另外单独传。
	//
	// 2026-10-21 修：这里以前写死 `"type": "musicUrl"`，脚本拿到的是
	// `mapQuality('musicUrl')` —— 认不出这个值，按脚本的兜底规则**降到最低档**
	// （真机读 全豆要.js：idx=-1 → 从 128k 起找 → 只要 128k）。结果就是：
	// 哪怕池里点名要无损，洛雪这条路也一直只要到 128k，而且不报错、看不出来。
	// 现在把调用方点的档位原样放进 `type`；空档位时给 "flac" —— 脚本自己会按
	// 它声明的 qualitys 往下压（mapQuality clamp），所以「问高一点」永远安全。
	tier := strings.ToLower(strings.TrimSpace(quality))
	if tier == "" {
		tier = "flac"
	}
	body, _ := json.Marshal(resolveRequest{
		Source:  source,
		Info:    map[string]any{"musicInfo": info, "type": tier},
		Quality: tier,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/resolve", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	var out struct {
		URL   string `json:"url"`
		Error string `json:"error"`
		Bytes int64  `json:"bytes"`
		// 宿主会把「每个脚本为什么不行」放在这里。带上它，排障时不用再去翻宿主日志。
		Tried []struct {
			Script string `json:"script"`
			Error  string `json:"error"`
			Got    string `json:"got"`
			// 2026-10-21 起宿主加了「直链活性校验」，失败原因改放在 `why` 里
			// （`dead` = 有状态码/内容不对，证明是死链；`unproven` = 探测本身没结论）。
			// 以前只读 error/got 的话，报错会退化成「返回了非直链：」这种没用的话。
			Why string `json:"why"`
		} `json:"tried"`
		Probed struct {
			Status int   `json:"status"`
			Bytes  int64 `json:"bytes"`
		} `json:"probed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	size := out.Probed.Bytes
	if size == 0 {
		size = out.Bytes // 老宿主/别的实现可能平铺给 bytes
	}
	if resp.StatusCode != http.StatusOK || out.URL == "" {
		msg := out.Error
		if msg == "" {
			msg = resp.Status
		}
		parts := make([]string, 0, len(out.Tried))
		for _, tr := range out.Tried {
			detail := tr.Error
			if detail == "" {
				detail = tr.Why
			}
			if detail == "" {
				detail = "返回了非直链：" + tr.Got
			}
			parts = append(parts, tr.Script+" → "+detail)
		}
		if len(parts) > 0 {
			msg += "（" + strings.Join(parts, "；") + "）"
		}
		return "", 0, errors.New(msg)
	}
	if !strings.HasPrefix(out.URL, "http") {
		return "", 0, fmt.Errorf("宿主返回的不是直链：%q", out.URL)
	}
	return out.URL, size, nil
}

// ResolveCandidates 按 scripts 的顺序（= 用户排的轮询顺序）问宿主，
// 把所有**能下得动**的候选交回来。挑哪条由调用方决定（见 resolveRequest.Scripts）。
//
// scripts 为空 = 不限制（宿主用它自己加载的全部脚本）。
func (m *Manager) ResolveCandidates(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]Candidate, error) {
	m.mu.Lock()
	if m.state != StateReady || m.proc == nil {
		st := m.state
		m.mu.Unlock()
		return nil, fmt.Errorf("%w（%s）", ErrNotRunning, st)
	}
	base := m.cfg.base()
	m.mu.Unlock()

	tier := strings.ToLower(strings.TrimSpace(quality))
	if tier == "" {
		tier = "flac"
	}
	body, _ := json.Marshal(resolveRequest{
		Source:  source,
		Info:    map[string]any{"musicInfo": info, "type": tier},
		Quality: tier,
		Scripts: scripts,
		Collect: true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/resolve", bytes.NewReader(body))
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
		Candidates []Candidate `json:"candidates"`
		Error      string      `json:"error"`
		Tried      []struct {
			Script string `json:"script"`
			Error  string `json:"error"`
			Got    string `json:"got"`
			Why    string `json:"why"`
		} `json:"tried"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(describeLxFailure(out.Error, resp.Status, out.Tried))
	}
	if len(out.Candidates) == 0 {
		// 200 但一条候选都没有：宿主不该这么回，但真发生了也要给出可读的原因。
		return nil, errors.New(describeLxFailure("宿主没有交回任何候选", resp.Status, out.Tried))
	}
	return out.Candidates, nil
}

// describeLxFailure 把「这次为什么没拿到」拼成人能读的一句话。
//
// 抽出来是因为 /resolve 的单条模式与轮询模式要用**同一份文案**：
// 两边各写一遍，排障时同一件事会有两种说法（真机查日志时最烦这个）。
func describeLxFailure(msg, status string, tried []struct {
	Script string `json:"script"`
	Error  string `json:"error"`
	Got    string `json:"got"`
	Why    string `json:"why"`
}) string {
	if msg == "" {
		msg = status
	}
	parts := make([]string, 0, len(tried))
	for _, tr := range tried {
		detail := tr.Error
		if detail == "" {
			detail = tr.Why
		}
		if detail == "" {
			detail = "返回了非直链：" + tr.Got
		}
		parts = append(parts, tr.Script+" → "+detail)
	}
	if len(parts) > 0 {
		msg += "（" + strings.Join(parts, "；") + "）"
	}
	return msg
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
	// Raw 是脚本那条记录的**原样**（宿主只删掉它自己的 `_raw` 透传字段）。
	//
	// ⚠️ 2026-10-08 补：宿主一直在响应里带 `raw`，但这里没有对应字段 —— 于是它
	// 在 Go 侧被静默丢掉。而**取链要的正是它**：LX 脚本的 `musicUrl` 读
	// `info.musicInfo`，很多脚本还要读 `_types[档位]` 才能给出直链。
	// 没有它就只能拿 id + 歌名去要，那些脚本一律拒绝（表现为「解析失败」）。
	Raw map[string]any `json:"raw,omitempty"`
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

// ── 启动参数：进程级隔离 ────────────────────────────────────────────────

// hostArgs 是**完整**的子进程参数表（不含 argv[0]）。
//
// 单独抽出来是为了让它可测 —— 隔离参数是安全不变量，用例直接钉这张表，
// 免得以后重构把它悄悄改掉（改掉的表现是「一切照旧能用」，没有任何报错）。
func (c Config) hostArgs() []string {
	args := []string{"--permission"}

	// 读权限逐项放行：宿主脚本自身 + 音源脚本目录。
	//
	// 这里**必须**转绝对路径：`--allow-fs-read` 的相对路径是相对**子进程 cwd**
	// 解析的，而 `--dir` 原样透传给宿主（宿主也按 cwd 解析）。两者同源才对得上 ——
	// 转换后仍然同源（子进程继承 Go 进程的 cwd），但参数表里就不再有「相对路径
	// 到底相对于谁」的歧义。
	//
	// ⚠️ 空串会被跳过：`--allow-fs-read=`（空值）会让 node **直接拒绝启动**
	// （`node: --allow-fs-read= requires an argument`），比「目录不存在」更糟 ——
	// 后者只是放行一个永远匹配不上的路径，宿主照常起来（实测）。宁可让权限更窄，
	// 也不能让子进程起不来。
	for _, p := range []string{c.Script, c.Dir} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p //Abs 只在拿不到 cwd 时失败；此时用原值，至少语义和以前一致
		}
		args = append(args, "--allow-fs-read="+abs)
	}

	// 参数顺序不能动：node 的选项必须在脚本路径**之前**，否则会被当成脚本的 argv。
	args = append(args,
		c.Script,
		"--dir", c.Dir,
		"--port", fmt.Sprint(c.port()),
	)
	return args
}

// childEnv 是子进程的**全部**环境变量。
//
// 为什么不像以前那样直接继承：两个实测过的洞，都只能在这一层堵。
//
//  1. `process.env` 对脚本是**可读的**（权限模型不管环境变量，本机 v22.20 实测），
//     而脚本按设计能出网（lx.request）—— 宿主进程里的 token 直接就是可外带的数据。
//  2. 更糟的是 **`NODE_OPTIONS` 能放宽权限模型本身**：本机实测
//     `NODE_OPTIONS="--allow-fs-read=/"` + `--permission` 之后，原本被拒的
//     `/etc/hostname` 又能读了。Node 只把 `--permission` 本身列为 NODE_OPTIONS 禁用项，
//     `--allow-*` 没禁。也就是说：只要宿主进程的环境里**恰好**有 NODE_OPTIONS，
//     这次加的隔离会**静默失效** —— 没有报错、没有日志，看起来一切正常。
//     显式指定环境变量是最直接的堵法。
//
// nodeSearchHint 是「找不到 node」时提示里带上的已知位置（只用于报错文案）。
const nodeSearchHint = "/vol1/@appcenter/nodejs_v*/bin/node"

// resolveNodeBin 找出 node 可执行文件，返回空串 = 找不到。
//
// # 为什么不能只靠 exec.LookPath
//
// ⚠️ 2026-10-08 真机实测：**飞牛上 node 不在应用的 PATH 里**。
// 应用进程的 PATH 是 `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`，
// 而 node 装在 `/vol1/@appcenter/nodejs_v24/bin/node`（fnOS 的 nodejs 应用）。
// 原来这里只做 `exec.LookPath("node")` → 失败 → 服务端洛雪宿主**根本起不来**。
//
// 更糟的是它**静默**：失败只写进 lxnode 自己的 logf（stderr），界面上一点提示都没有，
// 表现就是用户报的那句「我 lx 音乐怎么没有参与到下载」—— 而且怎么查都查不到
// 「宿主压根没启动」这一层。
//
// 查找顺序（先精确后兜底）：
//  1. 配置里给了**绝对路径**且存在 → 用它；
//  2. 环境变量 `QULV_NODE_BIN`（运维覆盖口）；
//  3. PATH 里能找到 → 用它（开发机 / 通用 Linux 走这条）；
//  4. 已知安装位置：`/vol1/@appcenter/nodejs_vNN/bin/node`（**版本号大的优先**）、
//     `/usr/local/bin/node`、`/usr/bin/node`、`/opt/node*/bin/node`。
func resolveNodeBin(configured string) string {
	configured = strings.TrimSpace(configured)
	// 1. 显式绝对路径
	if filepath.IsAbs(configured) {
		if isExecutableFile(configured) {
			return configured
		}
		// 绝对路径写错时**继续往下找**，而不是直接失败 —— 用户的意图是「用 node」，
		// 不是「用这个特定的路径」。
	}
	// 2. 环境变量覆盖
	if env := strings.TrimSpace(os.Getenv("QULV_NODE_BIN")); env != "" && isExecutableFile(env) {
		return env
	}
	// 3. PATH（配置里给的名字优先，其次 "node"）
	for _, name := range []string{configured, "node"} {
		if name == "" || filepath.IsAbs(name) {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	// 4. 已知安装位置。nodejs_vNN 按**版本号降序**：v24 优先于 v22。
	var candidates []string
	for _, v := range []string{"v24", "v22", "v20", "v18"} {
		candidates = append(candidates, filepath.Join("/vol1/@appcenter/nodejs_"+v, "bin", "node"))
	}
	candidates = append(candidates, "/usr/local/bin/node", "/usr/bin/node")
	for _, v := range []string{"v24", "v22", "v20", "v18"} {
		candidates = append(candidates, filepath.Join("/vol2/@appcenter/nodejs_"+v, "bin", "node"))
	}
	if matches, err := filepath.Glob("/opt/node*/bin/node"); err == nil {
		candidates = append(candidates, matches...)
	}
	for _, c := range candidates {
		if isExecutableFile(c) {
			return c
		}
	}
	return ""
}

// isExecutableFile 判断「这个路径是个可执行文件」。
func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// 只留 PATH（万一 NodeBin 是个需要 PATH 查找的包装脚本）与 TZ（时间语义别变）。
// 宿主本身只用 node: 内建，一个环境变量都不需要 —— 已用真音源脚本实测过。
// 返回非 nil 空切片是**有意义**的：`cmd.Env == nil` 表示「继承」，我们要的是「空」。
func childEnv() []string {
	env := make([]string, 0, 2)
	for _, k := range []string{"PATH", "TZ"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// execProc 是真实的进程实现。
type execProc struct {
	cfg  Config
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProc) Start() error {
	node := resolveNodeBin(p.cfg.NodeBin)
	if node == "" {
		return fmt.Errorf("找不到 node 可执行文件（试过 PATH 与 %s）—— 设 QULV_NODE_BIN 指一个", nodeSearchHint)
	}
	if p.cfg.Script == "" {
		return errors.New("没有配置 lx 宿主脚本路径")
	}
	if _, err := os.Stat(p.cfg.Script); err != nil {
		return fmt.Errorf("lx 宿主脚本不存在：%w", err)
	}
	p.cmd = exec.Command(node, p.cfg.hostArgs()...)
	// 环境变量显式指定（不继承）—— 理由见 childEnv。
	p.cmd.Env = childEnv()
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
