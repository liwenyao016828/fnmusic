// Package takeover 接管飞牛音乐官方后端的 Unix socket。
//
// 背景：飞牛音乐（appname `trim.music`）的网页端、桌面端、手机端都通过
// `/var/run/trim_music.socket` 访问它的后端 daemon。曲率此前一直是这个
// socket 的**客户端**（见 pkg/fnos）；本包让它同时成为**服务端**：把官方
// socket 改名让路，自己在原路径上监听，除少量自有端点外一律原样透传给上游。
//
// 这样做的价值不在「劫持」本身，而在于：官方 UI 里的每一次搜索、取流、
// 歌词、封面、收藏请求都会经过我们，后续阶段才能在这些请求上补足能力。
//
// # 设计纪律（全部来自 fnmusic-ext 的实战教训，不是洁癖）
//
//  1. **身份只信内核**。判断「谁在监听」只用 SO_PEERCRED 拿 peer pid +
//     读 /proc/<pid>/exe，绝不把业务响应当成身份凭据 —— 一个能返回 JSON
//     的 socket 不代表它是官方 daemon。
//  2. **动官方 socket 必须是原子的**。用 renameat2(RENAME_NOREPLACE)，
//     内核不支持就拒绝启动，绝不做「先 unlink 再 rename」这种有窗口的替换。
//  3. **先落 journal 再动 socket**。任何时刻断电/被杀，都能凭 journal 还原。
//  4. **先起私有 staging socket 再发布**。net.Listen 会 unlink 目标路径，
//     直接对着 target 监听会先把官方 socket 删掉。
//  5. **失败必须自动回滚**；回滚本身失败要把两个错误都报出来，不能吞。
//  6. **还原之前先预检**。上游必须能被正识别为官方，否则拒绝还原 ——
//     把别人的代理当成官方挪回去，等于把官方音乐彻底弄死。
//  7. **有疑问就不动**。拓扑不明（target/upstream 状态组合无法解释）时
//     直接拒绝接管。宁可曲率少一个功能，也不能让官方音乐打不开。
package takeover

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 默认路径。与 fnmusic-ext 保持一致，便于两个扩展互相识别。
const (
	DefaultTarget   = "/var/run/trim_music.socket"
	DefaultUpstream = "/var/run/trim_music_upstream.socket"

	// EnvEnable 控制是否接管。默认关闭 —— 接管会影响官方音乐能否使用，
	// 必须是用户显式开启的动作。
	EnvEnable = "QULV_TAKEOVER"

	// EnvTarget / EnvUpstream 覆盖 socket 路径（测试与非常规部署用）。
	EnvTarget   = "QULV_TAKEOVER_TARGET"
	EnvUpstream = "QULV_TAKEOVER_UPSTREAM"

	// EnvTrace 打开后，未拦截端点的透传日志更详细。
	EnvTrace = "QULV_TAKEOVER_TRACE"

	// LivezPath 是接管层的自证端点。inspect() 靠它区分
	// 「曲率在监听」和「别的代理在监听」。
	LivezPath = "/_qulv/livez"

	// HealthzPath 给运维/打包脚本做健康检查。
	HealthzPath = "/_qulv/healthz"

	// livezService 是自证端点返回的服务名。inspect() 用它区分
	// 「曲率在监听」和「别的代理在监听」。
	livezService = "qulv-takeover"

	journalName = "takeover-journal.json"
	lockName    = "takeover.lock"

	journalVersion = 1
)

// 各阶段等待上限。官方 daemon 重启时会抢回 socket，我们需要等这个竞态窗口。
const (
	// defaultUpstreamWait 发布后等上游真正就绪的上限。
	defaultUpstreamWait = 40 * time.Second
	// defaultBootWait 开机竞态：我们的服务可能比官方 daemon 先起来。
	// 两边路径都是空的并不代表拓扑不明，只代表官方还没启动 —— 等它。
	defaultBootWait = 40 * time.Second
	// defaultProbeTimeout 单次探活超时。
	defaultProbeTimeout = 3 * time.Second
	// defaultDialTimeout 连 socket 的超时。
	defaultDialTimeout = 2 * time.Second
)

// State 是一个 socket 路径的形态。
type State string

const (
	// StateAbsent 路径不存在。
	StateAbsent State = "absent"
	// StateOfficial 有活进程在监听，且它是官方 trim-music。
	StateOfficial State = "official"
	// StateOurs 有活进程在监听，且它是我们自己（由 livez 自证）。
	StateOurs State = "ours"
	// StateStale 路径存在但没人监听（进程崩了留下的死文件）。
	StateStale State = "stale"
	// StateOtherProxy 有活进程在监听，是**别人的**代理（例如 fnmusic-ext）。
	StateOtherProxy State = "other-proxy"
	// StateUnknown 无法判定。可能没权限读 /proc，也可能是完全陌生的进程。
	// 出现这个状态时我们一律不动手。
	StateUnknown State = "unknown"
)

// 导出错误。调用方用 errors.Is 判断。
var (
	// ErrDisabled 未开启接管。
	ErrDisabled = errors.New("takeover: 未开启（QULV_TAKEOVER 未设为 1）")
	// ErrUnsafe 拓扑不允许安全操作。这是本包最重要的错误：
	// 它意味着「拒绝动手」，而不是「动手失败了」。
	ErrUnsafe = errors.New("takeover: 拓扑不允许安全操作")
	// ErrNoJournal 没有接管记录，无从还原。
	ErrNoJournal = errors.New("takeover: 没有接管记录")
)

// Options 是接管配置。
type Options struct {
	// Target 官方 socket 路径（我们要占据的位置）。
	Target string
	// Upstream 让路后的官方 socket 路径。
	Upstream string
	// DataDir 存放 journal 与锁的目录。
	DataDir string
	// Logf 日志输出；nil 则用标准 log。
	Logf func(format string, args ...any)
	// Trace 打开未拦截端点全量日志。
	Trace bool

	// BootWait 覆盖「等官方 daemon 出现」的预算（0 = 用默认 40s）。
	// 存在只为让测试不必真等 40 秒。
	BootWait time.Duration
	// UpstreamWait 覆盖「等上游就绪」的预算（0 = 用默认 40s）。
	UpstreamWait time.Duration

	// OnMoved 在 socket 文件被改名后调用（oldPath → newPath）。
	//
	// 生产环境留空。测试用它把「假 daemon 现在挂在哪个路径上」跟着搬，
	// 因为真机上身份由内核按路径给出，测试里必须手工维护。
	OnMoved func(oldPath, newPath string)

	// IsOurs 是一个额外的「这个 socket 是不是我们自己」判据，按路径判断。
	//
	// 生产环境留空即可：inspect 靠 SO_PEERCRED + livez 自证已经足够。
	// 存在的理由是**测试**：测试里的假 daemon 和被测代码在同一个进程里，
	// pid 必然相同，光靠 pid + livez 分不清「官方在监听」和「我们自己」。
	IsOurs func(path string) bool

	// Interceptor 是「先看一眼、处理不了就透传」的钩子（阶段 2 的拦截层）。
	//
	// 用本地接口而不是直接依赖 pkg/intercept：接管层不该知道拦截层长什么样，
	// 拦截层也不该知道 socket 是怎么被接管的。两边只在 main.go 里碰面。
	Interceptor Interceptor
}

// Interceptor 是拦截层的形状。
//
// Handle 返回 true 表示「这个请求我已经答复了」，返回 false 表示「不关我事，
// 请透传给官方」。这个布尔值是整条链路上唯一的契约 —— 拦截层永远不能让一个
// 它处理不了的请求变成 500，只能交回去。
type Interceptor interface {
	Handle(w http.ResponseWriter, r *http.Request) bool
}

// UnixClient 返回一个把**所有**请求都打到指定 Unix socket 的 HTTP 客户端。
//
// 拦截层需要它来读官方响应（合并列表时要拿官方的原始结果）。注意两点：
//   - 不继承进程级 http_proxy —— NAS 上常设全局代理，继承会让本地 socket
//     请求被发去外部代理；
//   - 不设 Timeout。官方有些接口（曲库扫描、批量详情）本来就要几十秒，
//     而 Timeout 是「整个响应读完」的上限。
func UnixClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				d := net.Dialer{Timeout: defaultDialTimeout}
				return d.DialContext(ctx, "unix", socketPath)
			},
			MaxIdleConns:          64,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     false,
		},
	}
}

func (o Options) bootWait() time.Duration {
	if o.BootWait > 0 {
		return o.BootWait
	}
	return defaultBootWait
}

func (o Options) upstreamWait() time.Duration {
	if o.UpstreamWait > 0 {
		return o.UpstreamWait
	}
	return defaultUpstreamWait
}

// Manager 持有接管状态与监听器。
type Manager struct {
	opts Options
	logf func(string, ...any)

	mu       sync.Mutex
	listener net.Listener
	httpSrv  *http.Server
	lockFile *os.File
	active   bool

	// yielded 记录「本来可以接管、但按约定让给了别人」的原因。
	//
	// 接管没生效有两种完全不同的情况：①没开开关、或拓扑不明而拒绝；
	// ②检测到别的扩展已经在代理，主动让路。后者在启动日志里只出现一行，
	// 用户回头看 /api/takeover/status 时需要一个看得见的理由，
	// 否则「我明明设了 QULV_TAKEOVER=1，怎么 active 还是 false」无从查起。
	yielded string

	// 未拦截端点的采样计数（只打首见 + 打开 Trace 后每 N 次一条，避免刷屏）。
	seenMu sync.Mutex
	seen   map[string]int

	// oursPath 记录本进程真正监听中的 socket 路径。
	//
	// 这是比 livez 探测更早、更可靠的自我认定：我们是那个 listen 的人，
	// 自己知道。livez 只在「不是本进程登记的路径」时才需要用来判定
	// 「是不是别人的代理」。
	oursMu   sync.Mutex
	oursPath map[string]bool
}

// New 构造 Manager。DataDir 为空则用系统临时目录下的 qulv-takeover。
func New(opts Options) *Manager {
	if opts.Target == "" {
		opts.Target = DefaultTarget
	}
	if opts.Upstream == "" {
		opts.Upstream = DefaultUpstream
	}
	if opts.DataDir == "" {
		opts.DataDir = filepath.Join(os.TempDir(), "qulv-takeover")
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &Manager{
		opts:     opts,
		logf:     logf,
		seen:     make(map[string]int),
		oursPath: make(map[string]bool),
	}
}

// Enabled 读环境变量判断是否该接管。默认关闭。
func Enabled() bool {
	v := os.Getenv(EnvEnable)
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// DefaultOptions 从环境变量组装配置。
func DefaultOptions(dataDir string) Options {
	o := Options{
		Target:   DefaultTarget,
		Upstream: DefaultUpstream,
		DataDir:  dataDir,
		Trace:    os.Getenv(EnvTrace) == "1",
	}
	if v := os.Getenv(EnvTarget); v != "" {
		o.Target = v
	}
	if v := os.Getenv(EnvUpstream); v != "" {
		o.Upstream = v
	}
	return o
}

// Status 是一次拓扑快照，供日志、CLI 与健康接口使用。
type Status struct {
	Target        string `json:"target"`
	Upstream      string `json:"upstream"`
	TargetState   State  `json:"target_state"`
	UpstreamState State  `json:"upstream_state"`
	Active        bool   `json:"active"`
	Detail        string `json:"detail,omitempty"`
	// Yielded 非空表示「本来可以接管，但让给了别人」，值是让路的理由。
	// 与 Active=false 的区别很重要：前者是主动礼让，后者是没启用或被拒绝。
	Yielded string `json:"yielded,omitempty"`
}

// Snapshot 探测当前拓扑。不改动任何东西。
func (m *Manager) Snapshot() Status {
	m.mu.Lock()
	active := m.active
	yielded := m.yielded
	m.mu.Unlock()

	target := m.inspect(m.opts.Target)
	upstream := m.inspect(m.opts.Upstream)

	st := Status{
		Target:        m.opts.Target,
		Upstream:      m.opts.Upstream,
		TargetState:   target.State,
		UpstreamState: upstream.State,
		Active:        active,
		Yielded:       yielded,
	}
	if target.Detail != "" {
		st.Detail = "target: " + target.Detail
	}
	if upstream.Detail != "" {
		if st.Detail != "" {
			st.Detail += "; "
		}
		st.Detail += "upstream: " + upstream.Detail
	}
	return st
}

// Enable 执行接管。失败时保证已回滚，不会留下半接管状态。
func (m *Manager) Enable() error {
	if !Enabled() {
		return ErrDisabled
	}

	// 0. 同机只允许一个曲率接管。两个进程抢同一个 socket 只会互相打架。
	if err := m.acquireLock(); err != nil {
		return err
	}

	// 1. 上次是不是崩在半路？先复原，再按干净状态重新开始。
	if err := m.recoverIfNeeded(); err != nil {
		m.releaseLock()
		return err
	}

	// 2. 判定拓扑。
	snap := m.Snapshot()

	// 2a. 开机竞态：两边都是空的（或只剩死文件）说明官方 daemon 还没起来。
	// 这不是「拓扑不明」，是有界的等待问题 —— 我们的启动钩子可能跑在它前面。
	// 不在这里等的话，重启后接管会随机失败，用户得手动重启一次。
	if (snap.TargetState == StateAbsent || snap.TargetState == StateStale) &&
		(snap.UpstreamState == StateAbsent || snap.UpstreamState == StateStale) {
		if err := m.waitForOfficial(m.opts.bootWait()); err != nil {
			m.releaseLock()
			return err
		}
		snap = m.Snapshot()
	}

	switch {
	case snap.TargetState == StateOurs:
		// 已经在监听（例如同进程重复调用）。什么都不做。
		m.logf("[TAKEOVER] %s 已由本进程监听，无需重复接管", m.opts.Target)
		m.mu.Lock()
		m.active = true
		m.mu.Unlock()
		return nil

	case snap.UpstreamState == StateOfficial && snap.TargetState != StateOfficial:
		// 上游躺着**活的官方 daemon**，而官方入口又不在官方手里 ——
		// 只可能是有人已经把官方 socket 挪到了上游路径，自己顶在 target 上。
		// 这是任何「接管式扩展」的拓扑指纹，不管它是谁。
		//
		// 为什么不能只靠 StateOtherProxy 判：对手的代理对不认识的路径通常
		// 直接透传给官方，而官方对 /_qulv/livez 会回 200 + 前端 HTML。
		// 我们的自证探针要的是 JSON，拿不到就只能落到 unknown。
		// 靠拓扑判就不用认识对手是谁，也不用指望它实现我们的探针路径。
		m.yield(fmt.Sprintf(
			"上游 %s 是活的官方 daemon、但入口 %s 不在官方手里（%s）——已有其它扩展在代理，本应用按约定让路",
			m.opts.Upstream, m.opts.Target, snap.TargetState))
		return nil

	case snap.UpstreamState == StateOtherProxy:
		// 已经有人在代理了（最可能是官方那个 fnmusic-ext 扩展）。
		// 按约定让路：我们只跑自己的功能，不去抢它的位置。
		m.yield(fmt.Sprintf("%s 上已有第三方代理", m.opts.Upstream))
		return nil

	case snap.TargetState == StateOtherProxy:
		// 别人的代理已经占住我们要的位置。同样让路 —— 我们的目标是给用户
		// 多一层能力，不是把另一个扩展挤掉。
		m.yield(fmt.Sprintf("%s 已被第三方代理占据", m.opts.Target))
		return nil

	case snap.TargetState == StateUnknown || snap.UpstreamState == StateUnknown:
		m.releaseLock()
		return fmt.Errorf("%w: 拓扑不明（target=%s upstream=%s），拒绝接管以免弄坏官方音乐",
			ErrUnsafe, snap.TargetState, snap.UpstreamState)
	}

	// 3. 起 staging 监听器。必须先起在别处并自证可用，再改 target 的名字 ——
	//    绝不能直接对着 target 监听，bind 会把官方 socket 删掉。
	//    位置必须在 target 同级目录（见 stageListener 的注释：rename 不能跨文件系统）。
	staging, err := m.stageListener()
	if err != nil {
		m.releaseLock()
		return err
	}
	// 失败路径要靠它清文件：srv.Close() 只关 fd，不删 socket 文件。
	stagingFile := stagingPath(staging)

	// 4. 先起服务，再发布。顺序不能反：发布流程要靠 livez 自证每一步，
	//    而应答 livez 的就是这个服务。先发布再起服务 = 自己把自己判失败。
	m.mu.Lock()
	m.listener = staging
	m.httpSrv = &http.Server{
		Handler: m.handler(),
		// 本地 socket，没有 Slowloris 问题；但读头超时给足。
		ReadHeaderTimeout: 10 * time.Second,
		// 写超时置 0：音频流是长连接，整体写超时会中断正在播放的音频。
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	m.mu.Unlock()

	go func() {
		if err := m.httpSrv.Serve(staging); err != nil && err != http.ErrServerClosed {
			m.logf("[TAKEOVER] 接管层监听结束: %v", err)
		}
	}()

	// 5. 发布：先落 journal，再原子改名。
	if err := m.publish(staging); err != nil {
		// 发布失败已经内部回滚，这里只需把自己起的服务收掉、把 staging 文件清掉。
		m.mu.Lock()
		srv := m.httpSrv
		m.httpSrv = nil
		m.listener = nil
		m.mu.Unlock()
		if srv != nil {
			_ = srv.Close()
		}
		_ = os.Remove(stagingFile)
		m.releaseLock()
		return err
	}

	m.mu.Lock()
	m.active = true
	m.mu.Unlock()

	m.logf("[TAKEOVER] 已接管 %s（官方让路到 %s）", m.opts.Target, m.opts.Upstream)
	return nil
}

// Close 停止监听并还原官方 socket。
//
// 顺序很重要：先停监听（不再接受新连接），再把官方挪回原位。
// 如果先还原再停监听，中间窗口里官方的新连接会打到我们身上。
func (m *Manager) Close() error {
	m.mu.Lock()
	srv, ln, active := m.httpSrv, m.listener, m.active
	m.httpSrv, m.listener, m.active = nil, nil, false
	m.mu.Unlock()

	if !active {
		m.releaseLock()
		return nil
	}

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := srv.Shutdown(ctx)
		cancel()
		if err != nil {
			m.logf("[TAKEOVER] 优雅关闭超时，强制关闭: %v", err)
			_ = srv.Close()
		}
	}
	if ln != nil {
		// 监听器不删 socket 文件（见 listenUnixNoUnlink），文件生命周期由我们管。
		_ = ln.Close()
	}

	err := m.restore(true)
	m.releaseLock()
	return err
}

// Restore 在进程外还原官方 socket。供 CLI 与打包脚本的停止钩子使用。
func Restore(opts Options) error {
	m := New(opts)
	return m.restore(true)
}

// restore 把上游的官方 socket 挪回 target。
//
// preflight=true 时先确认上游确实是官方 —— 把别人的代理挪回去会把官方音乐弄死。
func (m *Manager) restore(preflight bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 闸一：只撤销**我们自己记录过**的接管。没有 journal 就说明我们从没 publish 过。
	//
	// 这道闸不能省，否则会踩到「主动让路」这个形态：别人已经在代理官方入口，
	// 于是 upstream 恰好是官方（官方被那个扩展挪过去了）、target 是别人的 socket。
	// 只看 upstream 判据会认为「该还原」，把别人的 socket 挪走再 os.Remove 掉 ——
	// 我们自己什么都没接管过，却毁掉了别人的接管。
	if _, err := m.readJournal(); err != nil {
		if errors.Is(err, ErrNoJournal) {
			return nil
		}
		return err
	}

	up := m.inspect(m.opts.Upstream)
	if up.State == StateAbsent {
		// 上游不存在：接管没成过，或已经被还原过了。
		_ = os.Remove(m.journalPath())
		return nil
	}
	if preflight && up.State != StateOfficial {
		return fmt.Errorf("%w: 上游 %s 状态为 %s，不是官方 daemon，拒绝还原",
			ErrUnsafe, m.opts.Upstream, up.State)
	}

	// 闸二：入口上蹲着一个自称不是我们的代理 —— 说明入口已经换人了，我们这条 journal
	// 是上一轮的遗留（典型场景：我们被 kill -9 留下 journal，机器重启后别人的扩展
	// 先起来接管了入口）。动它就是毁别人的接管，宁可不还原，只把作废的记录清掉。
	if svc := serviceAt(m.opts.Target, defaultProbeTimeout); svc != "" && svc != livezService {
		_ = os.Remove(m.journalPath())
		m.logf("[TAKEOVER] 放弃还原：入口 %s 现在是 %q 的代理，本应用上次的接管记录已作废",
			m.opts.Target, svc)
		return nil
	}

	// 把当前 target 挪到一边而不是直接删 —— 万一 rename 失败还能挪回来。
	backup := m.opts.Target + ".qulv-restore"
	_ = os.Remove(backup)
	tgt := m.inspect(m.opts.Target)
	if tgt.State != StateAbsent {
		if err := m.move(m.opts.Target, backup); err != nil {
			return fmt.Errorf("takeover: 清理 target 失败: %w", err)
		}
	}

	if err := m.move(m.opts.Upstream, m.opts.Target); err != nil {
		// 回退：把 backup 挪回原位，保持现场不变。
		if tgt.State != StateAbsent {
			if rbErr := m.move(backup, m.opts.Target); rbErr != nil {
				return fmt.Errorf("takeover: 还原失败(%v)，且回退也失败(%v) —— 现场留在 %s",
					err, rbErr, backup)
			}
		}
		return fmt.Errorf("takeover: 还原失败: %w", err)
	}

	_ = os.Remove(backup)
	_ = os.Remove(m.journalPath())
	m.unmarkOurs(m.opts.Target)
	m.logf("[TAKEOVER] 已还原官方 socket: %s → %s", m.opts.Upstream, m.opts.Target)
	return nil
}

// RestoreNow 供运行时主动交还官方 socket（例如 API 调用）。
//
// 与 Close 的区别：Close 会先停掉自己的监听服务（应用正在退出），
// RestoreNow 假设监听服务还活着 —— 于是它**必须先停掉自己**，
// 否则「把自己的 socket 挪走、把官方挪回来」之后，我们那个监听器
// 还挂在已经改名的路径上，会让官方 socket 处于被占用的假象里。
func (m *Manager) RestoreNow() error {
	m.mu.Lock()
	srv := m.httpSrv
	ln := m.listener
	m.httpSrv = nil
	m.listener = nil
	m.active = false
	m.mu.Unlock()

	if srv != nil {
		// 不再接受新连接；已建立的音频流由 Shutdown 的宽限期决定生死。
		// 这里给 2 秒：还原是「立刻交还官方」的动作，不该被长连接拖住。
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}

	return m.restore(true)
}

// move 是本包唯一动 socket 文件的地方：改名 + 通知（测试用）钩子。
func (m *Manager) move(oldPath, newPath string) error {
	if err := moveNoReplace(oldPath, newPath); err != nil {
		return err
	}
	if m.opts.OnMoved != nil {
		m.opts.OnMoved(oldPath, newPath)
	}
	return nil
}

// markOurs 登记一个由本进程监听的路径。
func (m *Manager) markOurs(path string) {
	if path == "" {
		return
	}
	m.oursMu.Lock()
	m.oursPath[path] = true
	m.oursMu.Unlock()
}

// unmarkOurs 注销一个路径。我们自己不再监听它时必须调用 ——
// 否则 inspect 会把「官方刚被挪回来的 socket」继续认成我们自己。
func (m *Manager) unmarkOurs(path string) {
	if path == "" {
		return
	}
	m.oursMu.Lock()
	delete(m.oursPath, path)
	m.oursMu.Unlock()
}

func (m *Manager) isOurs(path string) bool {
	m.oursMu.Lock()
	own := m.oursPath[path]
	m.oursMu.Unlock()
	if own {
		return true
	}
	if m.opts.IsOurs != nil {
		return m.opts.IsOurs(path)
	}
	return false
}

// inspect 用本 Manager 的配置探测一个 socket 路径。
func (m *Manager) inspect(path string) Probe {
	return inspect(path, m.isOurs)
}

// verifyOurselves 确认 path 上确实是我们自己在监听。
//
// 发布流程的每一步都要自证：只 rename 完就宣布成功，等于把「接管失败」
// 伪装成「接管成功」，之后官方 UI 会打到一个没人应答的 socket 上。
func (m *Manager) verifyOurselves(path string) error {
	ok, err := probeLivez(path, defaultProbeTimeout)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("socket 上没有接管层应答")
	}
	return nil
}

// publish 把 staging 监听器发布到 target 位置。
//
// 原子性由 renameat2(RENAME_NOREPLACE) 保证；内核不支持时直接拒绝，
// 不做非原子替换。
func (m *Manager) publish(staging net.Listener) error {
	// 先自证 staging 确实是我们：连上去打 livez。
	if err := m.verifyOurselves(stagingPath(staging)); err != nil {
		dropStaging(staging)
		return fmt.Errorf("takeover: staging socket 自证失败: %w", err)
	}

	// 我们的 socket 必须继承官方那个的权限位。
	//
	// bind 出来的文件受进程 umask 影响（默认 022 → srwxr-xr-x），而官方入口一直是
	// srw-rw-rw-（0666）。差别在 root 眼里完全看不出来 —— root 连得上，日志也干净 ——
	// 但 nginx 或官方应用的非 root 组件会直接 connect 失败，表现是「接管之后官方界面
	// 打不开音乐」。真机上就是这么发现的：同一句 curl，root 跑得到 livez，uid 895 报
	// 「Failed to connect」。
	//
	// 继承而不是写死 0666：万一官方哪天收紧成 0600，我们跟着收紧才叫权限不变。
	// 官方 socket 不在时（开机竞态）退回 0666 —— 那是这个入口路径一贯的权限。
	if err := os.Chmod(stagingPath(staging), socketMode(m.opts.Target)); err != nil {
		dropStaging(staging)
		return fmt.Errorf("takeover: 设置 staging socket 权限失败: %w", err)
	}

	// 再让官方 daemon 把 socket 让出来。
	tgt := m.inspect(m.opts.Target)
	if tgt.State == StateOfficial || tgt.State == StateStale {
		// 先落 journal：这一步之后任何崩溃都能凭它还原。
		if err := m.writeJournal(stagingPath(staging)); err != nil {
			dropStaging(staging)
			return err
		}
		if err := m.move(m.opts.Target, m.opts.Upstream); err != nil {
			_ = os.Remove(m.journalPath())
			dropStaging(staging)
			return fmt.Errorf("takeover: 官方 socket 让路失败: %w", err)
		}
	}

	// 发布 staging。
	if err := m.move(stagingPath(staging), m.opts.Target); err != nil {
		// 回滚：把官方挪回来。
		rbErr := m.move(m.opts.Upstream, m.opts.Target)
		if rbErr != nil {
			return fmt.Errorf("takeover: 发布失败(%v)，且回滚失败(%v) —— 官方 socket 现在在 %s",
				err, rbErr, m.opts.Upstream)
		}
		_ = os.Remove(m.journalPath())
		dropStaging(staging)
		return fmt.Errorf("takeover: 发布失败，已回滚: %w", err)
	}

	// 发布成功：从现在起 target 是本进程在监听。
	m.markOurs(m.opts.Target)

	// 验证我们真的占住了位置。
	if err := m.verifyOurselves(m.opts.Target); err != nil {
		rbErr := m.rollbackPublished()
		if rbErr != nil {
			return fmt.Errorf("takeover: 发布后自证失败(%v)，且回滚失败(%v)", err, rbErr)
		}
		dropStaging(staging)
		return fmt.Errorf("takeover: 发布后自证失败，已回滚: %w", err)
	}

	// 最后确认上游官方确实在（否则我们接管了一个没有上游的代理，等于断网）。
	if err := m.waitUpstream(); err != nil {
		rbErr := m.rollbackPublished()
		if rbErr != nil {
			return fmt.Errorf("takeover: 上游不可达(%v)，且回滚失败(%v)", err, rbErr)
		}
		dropStaging(staging)
		return fmt.Errorf("takeover: 上游不可达，已回滚: %w", err)
	}

	return nil
}

// rollbackPublished 在已经发布到 target 之后回滚。
func (m *Manager) rollbackPublished() error {
	// 把 target 上的自己挪开，再把官方挪回来。
	if err := m.move(m.opts.Target, m.opts.Target+".qulv-failed"); err != nil {
		return fmt.Errorf("清理 target 失败: %w", err)
	}
	if err := m.move(m.opts.Upstream, m.opts.Target); err != nil {
		return fmt.Errorf("恢复官方 socket 失败: %w", err)
	}
	_ = os.Remove(m.opts.Target + ".qulv-failed")
	_ = os.Remove(m.journalPath())
	return nil
}

// waitUpstream 等官方 daemon 在上游路径上就绪。
func (m *Manager) waitUpstream() error {
	deadline := time.Now().Add(m.opts.upstreamWait())
	for {
		st := m.inspect(m.opts.Upstream)
		if st.State == StateOfficial {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待上游就绪超时（最后状态 %s）", st.State)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForOfficial 等官方 daemon 出现。
//
// 只在「两边路径都没有活监听」时调用。这是开机/重启竞态：官方 daemon 可能
// 比我们晚起。等不到就拒绝接管 —— 绝不能在没有上游的情况下占住 target，
// 那会让官方 daemon 之后 bind 失败（EADDRINUSE），等于把官方音乐弄死。
func (m *Manager) waitForOfficial(budget time.Duration) error {
	deadline := time.Now().Add(budget)
	waited := false
	for {
		t := m.inspect(m.opts.Target)
		u := m.inspect(m.opts.Upstream)
		if t.State == StateOfficial || u.State == StateOfficial {
			if waited {
				m.logf("[TAKEOVER] 官方 daemon 已出现，继续接管")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: 等待官方 daemon 超时（target=%s upstream=%s）—— 官方音乐可能没启动",
				ErrUnsafe, t.State, u.State)
		}
		if !waited {
			m.logf("[TAKEOVER] 官方 daemon 尚未启动，最多等 %s", budget)
			waited = true
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// stageListener 在 **target 同级目录** 里起一个监听器。
//
// 必须与 target 同目录，不能放到 data 目录：发布那一步是 rename(staging, target)，
// 而 rename 不允许跨文件系统（EXDEV）。开发机上 --data 和 socket 都在 /tmp，看不出
// 问题；真机上 data 在 /vol1、socket 在 /var/run（不同挂载），必然报
// 「发布失败，已回滚: invalid cross-device link」。
//
// 早先怕「net.Listen 会 unlink 已存在路径、误删官方 socket」才挪去私有目录，
// 那个顾虑用「名字唯一 + 先清自己」解决，而不是靠换目录：路径里带 pid 且以点开头，
// 不可能撞上官方 socket；bind 之前先 os.Remove 掉的只可能是我们自己上一轮的残留。
func (m *Manager) stageListener() (net.Listener, error) {
	dir := filepath.Dir(m.opts.Target)
	m.sweepStaleStaging(dir)

	path := filepath.Join(dir, fmt.Sprintf(".qulv-stage-%d.sock", os.Getpid()))
	// 清掉同名死文件（只可能是本 pid 上一轮留下的）。
	_ = os.Remove(path)
	ln, err := listenUnixNoUnlink(path)
	if err != nil {
		return nil, fmt.Errorf("takeover: 在 %s 起 staging 监听器失败（该目录必须可写，否则无法发布）: %w", dir, err)
	}
	m.markOurs(path)
	return ln, nil
}

// sweepStaleStaging 清掉 target 目录里上一轮留下的 staging socket 文件。
//
// staging 文件在成功路径上会被 rename 到 target，失败路径由 dropStaging 清掉，
// 但 kill -9 之后没人收尾。留着会污染 /var/run，所以每次启动扫一遍。
// 前缀是本包独有的，且 takeover.lock 保证单实例，所以全删是安全的。
func (m *Manager) sweepStaleStaging(dir string) {
	matches, err := filepath.Glob(filepath.Join(dir, ".qulv-stage-*.sock"))
	if err != nil {
		return
	}
	for _, p := range matches {
		_ = os.Remove(p)
	}
}

// socketMode 返回接管层自己的 socket 该用什么权限：官方入口现在是什么，就是什么。
//
// 官方不在时（开机竞态、或 socket 已被别人挪走）退回 0666 —— 那是这个入口路径
// 一贯的权限（`srw-rw-rw-`），也是「谁都能连官方音乐后端」这个既定契约。
func socketMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		if p := fi.Mode().Perm(); p != 0 {
			return p
		}
	}
	return 0o666
}

// dropStaging 关闭 staging 监听器并清掉它的 socket 文件。
//
// listenUnixNoUnlink 故意不删文件（成功路径上它要被 rename 到 target，删了就发布不了），
// 所以失败路径必须自己清 —— 否则会在 /var/run 里堆下一串 .qulv-stage-*.sock。
// 对已经发布成功的监听器调用它是无害的：那个路径早已不存在，Remove 是空操作。
func dropStaging(ln net.Listener) {
	if ln == nil {
		return
	}
	p := stagingPath(ln)
	_ = ln.Close()
	if p != "" {
		_ = os.Remove(p)
	}
}

// stagingPath 从监听器取回它的路径。
func stagingPath(ln net.Listener) string {
	if a, ok := ln.Addr().(*net.UnixAddr); ok {
		return a.Name
	}
	return ""
}

// handler 构造接管层的 HTTP 处理器。
//
// 自有端点（/_qulv/*）本地应答；其余一律原样透传给上游官方 daemon。
// 阶段 1 不做任何拦截 —— 先证明「接管了但官方一切照旧」，再谈拦什么。
func (m *Manager) handler() http.Handler {
	proxy := m.newReverseProxy()

	// 透传是兜底，拦截层是前置。顺序不能反：拦截层要靠读官方响应才能合并，
	// 而它判断「这个 guid 是不是在线的」只看自己内存里的登记表。
	var root http.Handler = proxy
	if ic := m.opts.Interceptor; ic != nil {
		next := root
		root = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ic.Handle(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LivezPath:
			// 自证端点。inspect() 靠 service 名 + pid 认定「这个 socket 是我们的」，
			// 所以这两个字段不能改语义。
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			fmt.Fprintf(w, `{"service":%q,"pid":%d}`+"\n", livezService, os.Getpid())
			return
		case HealthzPath:
			snap := m.Snapshot()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			fmt.Fprintf(w, `{"ok":%t,"target":%q,"upstream":%q,"target_state":%q,"upstream_state":%q}`+"\n",
				snap.UpstreamState == StateOfficial, snap.Target, snap.Upstream, snap.TargetState, snap.UpstreamState)
			return
		}

		// 未拦截端点采样日志：官方更新加了新端点时，靠这个当天就能发现。
		//
		// 装了拦截层时**不在这里打**：这一行跑在 root 之前，还没有人判断过
		// 这个请求到底会不会被拦截，于是「首见未拦截端点 GET search/track」
		// 会为每一个被拦截的路径也打一条 —— 日志反而把真正没拦到的端点淹了。
		// 拦截层自己会在它的兜底分支里打同样的一条。
		if m.opts.Interceptor == nil {
			m.traceForward(r)
		}

		root.ServeHTTP(w, r)
	})
}

// newReverseProxy 构造到上游官方 socket 的反向代理。
func (m *Manager) newReverseProxy() *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: "unix"}

	transport := &http.Transport{
		// 关键：不继承进程级代理设置。NAS 上常设了全局 http_proxy，
		// 一旦继承，本地 socket 请求会被发去外部代理。
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: defaultDialTimeout}
			return d.DialContext(ctx, "unix", m.opts.Upstream)
		},
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     false,
	}

	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Host 头保持客户端原样 —— 官方 daemon 可能用它做校验。
			req.Header.Set("X-Forwarded-For", clientIP(req))
		},
		Transport: transport,
		// -1 = 流式立刻 flush。音频串流靠这个，不能攒。
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.logf("[TAKEOVER] 透传 %s %s 失败: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"message":"upstream unavailable"}`))
		},
	}
}

// traceForward 对未拦截端点做采样日志。
func (m *Manager) traceForward(r *http.Request) {
	key := r.Method + " " + r.URL.Path
	m.seenMu.Lock()
	m.seen[key]++
	n := m.seen[key]
	m.seenMu.Unlock()

	switch {
	case n == 1:
		m.logf("[TAKEOVER] 首见未拦截端点 %s（已原样透传）", key)
	case m.opts.Trace && n%50 == 0:
		m.logf("[TAKEOVER] 未拦截端点 %s 第 %d 次", key, n)
	}
}

// acquireLock 用文件锁保证同机只有一个曲率在接管。
func (m *Manager) acquireLock() error {
	if err := os.MkdirAll(m.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("takeover: 创建数据目录失败: %w", err)
	}
	f, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("takeover: 打开锁文件失败: %w", err)
	}
	if err := flockExclusiveNB(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: 另一个曲率实例已接管 %s", ErrUnsafe, m.opts.Target)
	}
	m.mu.Lock()
	m.lockFile = f
	m.mu.Unlock()
	return nil
}

func (m *Manager) releaseLock() {
	m.mu.Lock()
	f := m.lockFile
	m.lockFile = nil
	m.mu.Unlock()
	if f != nil {
		_ = funlock(f)
		_ = f.Close()
	}
}

// yield 记下「本来能接管、但按约定让给了别人」并放锁。
//
// 单开一个方法是为了让「让路」这件事在日志和 /api/takeover/status 里
// 都留下痕迹 —— 否则用户设了 QULV_TAKEOVER=1 却看到 active=false，
// 只能靠翻启动日志里那一行才知道是被别的扩展占了位置。
func (m *Manager) yield(reason string) {
	m.releaseLock()
	m.mu.Lock()
	m.yielded = reason
	m.mu.Unlock()
	m.logf("[TAKEOVER] 自动放弃接管（不干扰其它扩展）：%s", reason)
}

// recoverIfNeeded 处理上次崩溃留下的半接管状态。
func (m *Manager) recoverIfNeeded() error {
	if _, err := m.readJournal(); err != nil {
		if errors.Is(err, ErrNoJournal) {
			return nil
		}
		return err
	}

	up := m.inspect(m.opts.Upstream)
	if up.State == StateOfficial {
		// 官方还活着，只是停在上游路径上 —— 挪回去。
		m.logf("[TAKEOVER] 检测到上次未完成的接管，正在恢复")
		return m.restore(true)
	}
	if up.State == StateStale {
		// 死文件，清掉即可。
		_ = os.Remove(m.opts.Upstream)
	}
	// 上游既不活也不是死文件（可能是别人的代理）—— 不碰，只清 journal。
	_ = os.Remove(m.journalPath())
	m.logf("[TAKEOVER] 上次的接管记录已过期（上游状态 %s），已清理", up.State)
	return nil
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
