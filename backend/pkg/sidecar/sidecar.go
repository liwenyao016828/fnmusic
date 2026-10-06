// Package sidecar 掌管 musicdl sidecar 这个「外挂进程」的生命周期。
//
// # 为什么由 Go 进程来管，而不是写进 fpk 的 start/stop 脚本
//
// 那个脚本（`fpk-package/cmd/main`）是飞牛调用的，它只知道「起主进程 / 杀主进程」。
// 把 sidecar 塞进去意味着要在 shell 里处理：venv 建了没、依赖装完没、上次留下的
// 残留进程、停止时的优雅顺序 —— 而这些在 Go 里都是几行的事，还能顺手把进度
// 报给界面（「正在安装 Python 依赖」这种提示，用户等一两分钟时很需要）。
//
// # 三条硬约束
//
//  1. **绝不阻塞主进程**。venv + pip install 要一两分钟，全部在后台 goroutine 里做；
//     准备期间曲率照常工作（只是少几个平台）。
//  2. **不留孤儿**。sidecar 放进自己的进程组，停止时对整个组发信号。
//  3. **失败不致命**。任何一步失败都只是 `state=failed` + 一条错误说明，曲率继续跑。
package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 状态取值。
const (
	StateOff       = "off"       // 没开（配置里关着，或没找到 sidecar 目录）
	StatePreparing = "preparing" // 建 venv / 装依赖
	StateStarting  = "starting"  // 进程起来了，等健康检查
	StateReady     = "ready"     // 可用
	StateFailed    = "failed"    // 起不来（原因见 LastError）
	StateStopped   = "stopped"   // 主动停掉
)

// Config 是 Manager 的构造参数。
type Config struct {
	// Dir 是 sidecar 源码目录（含 app.py / core.py / requirements.txt）。
	Dir string
	// DataDir 是持久化目录（venv 与标记文件），必须是**升级不丢**的位置。
	DataDir string
	// Port 是回环监听端口。
	Port int
	// Sources 是要启用的平台短码（mg/bq/bi…）。
	Sources []string
	// Limit 是单源返回条数。
	Limit int
	// PythonPath 是用于建 venv 的解释器，空 = 自动找。
	PythonPath string
	Logf       func(format string, args ...any)
}

// Manager 管一个 sidecar 进程。
type Manager struct {
	cfg Config

	mu       sync.RWMutex
	cmd      *exec.Cmd
	state    string
	lastErr  string
	pid      int
	started  time.Time
	prepNote string // 「正在安装 Python 依赖」这类进度说明
	done     chan struct{}
}

// New 组装 Manager。不会启动任何东西 —— 要显式调 `Start`。
func New(cfg Config) *Manager {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Port <= 0 {
		cfg.Port = 8901
	}
	return &Manager{cfg: cfg, state: StateOff}
}

// Base 返回 sidecar 的基地址（形如 `http://127.0.0.1:8901`）。
func (m *Manager) Base() string {
	return "http://127.0.0.1:" + strconv.Itoa(m.cfg.Port)
}

// State 返回当前状态。
func (m *Manager) State() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// Ready 报告现在能不能用。
func (m *Manager) Ready() bool { return m.State() == StateReady }

// LastError 返回最后一次失败原因（没有则空串）。
func (m *Manager) LastError() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErr
}

// Status 是给界面看的快照。
func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]any{
		"state":   m.state,
		"port":    m.cfg.Port,
		"sources": append([]string(nil), m.cfg.Sources...),
		"pid":     m.pid,
	}
	if m.lastErr != "" {
		out["error"] = m.lastErr
	}
	if m.prepNote != "" {
		out["note"] = m.prepNote
	}
	if !m.started.IsZero() {
		out["started_at"] = m.started.Unix()
		out["uptime_s"] = int(time.Since(m.started).Seconds())
	}
	return out
}

func (m *Manager) setState(state, note string) {
	m.mu.Lock()
	m.state = state
	m.prepNote = note
	m.mu.Unlock()
}

func (m *Manager) fail(err error) {
	m.mu.Lock()
	m.state = StateFailed
	m.lastErr = err.Error()
	m.prepNote = ""
	m.mu.Unlock()
	m.cfg.Logf("[SIDECAR] 启动失败：%v", err)
}

// pythonBin 找建 venv 用的解释器。
//
// 优先 /usr/bin/python3（飞牛上就是 3.11，系统自带、不需要用户装东西），
// 其次 PATH 里的 python3。**不接受 `python`**（那是 py2 的历史名字）。
func (m *Manager) pythonBin() (string, error) {
	if p := strings.TrimSpace(m.cfg.PythonPath); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("指定的 Python 不存在：%s", p)
	}
	for _, cand := range []string{"/usr/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p, nil
	}
	return "", errors.New("没找到 python3（sidecar 需要 Python 3.9+）")
}

// venvDir / markerPath / venvPython 是三个约定路径。
func (m *Manager) venvDir() string    { return filepath.Join(m.cfg.DataDir, "venv") }
func (m *Manager) markerPath() string { return filepath.Join(m.cfg.DataDir, ".deps-ok") }
func (m *Manager) venvPython() string { return filepath.Join(m.venvDir(), "bin", "python3") }

// requirementsHash 用来判断「依赖要不要重装」。
//
// 只认 requirements.txt 的内容：内容没变就不重装（pip 那一趟要一两分钟，每次启动
// 都跑一遍是不可接受的）。升级 fpk 带来新依赖时内容会变 → 自动重装。
func (m *Manager) requirementsHash() (string, error) {
	raw, err := os.ReadFile(filepath.Join(m.cfg.Dir, "requirements.txt"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// ensureDeps 保证 venv 与依赖就绪（阻塞，必须在后台 goroutine 里调）。
func (m *Manager) ensureDeps(ctx context.Context) error {
	want, err := m.requirementsHash()
	if err != nil {
		return fmt.Errorf("读不到 requirements.txt：%w", err)
	}
	if got, err := os.ReadFile(m.markerPath()); err == nil && strings.TrimSpace(string(got)) == want {
		if _, err := os.Stat(m.venvPython()); err == nil {
			return nil // 已经装好，直接用
		}
	}

	py, err := m.pythonBin()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.DataDir, 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(m.venvPython()); err != nil {
		m.setState(StatePreparing, "正在创建 Python 虚拟环境")
		m.cfg.Logf("[SIDECAR] 创建 venv：%s", m.venvDir())
		if out, err := runCmd(ctx, "", py, "-m", "venv", m.venvDir()); err != nil {
			return fmt.Errorf("建 venv 失败：%w（%s）", err, tail(out, 300))
		}
	}

	m.setState(StatePreparing, "正在安装 Python 依赖（首次约 1-2 分钟）")
	m.cfg.Logf("[SIDECAR] 安装依赖：%s", filepath.Join(m.cfg.Dir, "requirements.txt"))
	// --disable-pip-version-check 省一次出站；不加 -q 是为了失败时日志里有东西可看
	out, err := runCmd(ctx, m.cfg.Dir, m.venvPython(), "-m", "pip", "install",
		"--disable-pip-version-check", "-r", filepath.Join(m.cfg.Dir, "requirements.txt"))
	if err != nil {
		return fmt.Errorf("装依赖失败：%w（%s）", err, tail(out, 500))
	}

	if err := os.WriteFile(m.markerPath(), []byte(want), 0o644); err != nil {
		// 标记写不上只是「下次会重装一遍」，不该让启动失败
		m.cfg.Logf("[SIDECAR] 警告：写依赖标记失败：%v", err)
	}
	return nil
}

// Start 在后台把 sidecar 拉起来。**立即返回**，进度看 `Status()`。
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.state == StatePreparing || m.state == StateStarting || m.state == StateReady {
		m.mu.Unlock()
		return
	}
	m.state = StatePreparing
	m.lastErr = ""
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	go func() {
		defer close(done)
		if err := m.run(ctx); err != nil {
			m.fail(err)
		}
	}()
}

// WaitReady 等到「就绪」或「失败」或超时（测试与关机时用）。
func (m *Manager) WaitReady(timeout time.Duration) bool {
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
	if done == nil {
		return m.Ready()
	}
	select {
	case <-done:
		return m.Ready()
	case <-time.After(timeout):
		return false
	}
}

func (m *Manager) run(ctx context.Context) error {
	if m.cfg.Dir == "" {
		return errors.New("没配置 sidecar 目录")
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Dir, "app.py")); err != nil {
		return fmt.Errorf("sidecar 目录里没有 app.py：%s", m.cfg.Dir)
	}
	if err := m.ensureDeps(ctx); err != nil {
		return err
	}

	m.setState(StateStarting, "正在启动 sidecar 进程")
	cmd, err := m.spawn()
	if err != nil {
		return err
	}

	// 等健康检查通过。**这是唯一的「真的能用了吗」判据** —— 进程活着不等于
	// uvicorn 起来了，更不等于 musicdl 装好了。
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if err := pingHealth(m.Base()); err == nil {
			m.mu.Lock()
			m.state = StateReady
			m.prepNote = ""
			m.pid = cmd.Process.Pid
			m.started = time.Now()
			m.mu.Unlock()
			m.cfg.Logf("[SIDECAR] 就绪：%s（pid %d，平台 %s）", m.Base(), cmd.Process.Pid,
				strings.Join(m.cfg.Sources, ","))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	_ = m.kill(cmd)
	return errors.New("sidecar 起来了但 45 秒内没通过健康检查（看它的日志）")
}

// spawn 拉起 uvicorn。
func (m *Manager) spawn() (*exec.Cmd, error) {
	py := m.venvPython()
	if _, err := os.Stat(py); err != nil {
		return nil, fmt.Errorf("venv 里的 python 不在：%s", py)
	}
	// 先把上次留下的残留进程清掉 —— 否则端口被占，新进程起不来，
	// 而现象是「健康检查一直不过」，很难反查。
	m.killStale()

	cmd := exec.Command(py, "-m", "uvicorn", "app:app",
		"--host", "127.0.0.1", "--port", strconv.Itoa(m.cfg.Port), "--log-level", "warning")
	cmd.Dir = m.cfg.Dir
	cmd.Env = append(os.Environ(),
		"QULV_SIDECAR_PORT="+strconv.Itoa(m.cfg.Port),
		"QULV_MUSICDL_SOURCES="+strings.Join(m.cfg.Sources, ","),
		"QULV_MUSICDL_LIMIT="+strconv.Itoa(m.cfg.Limit),
		"QULV_MUSICDL_WORKDIR="+filepath.Join(m.cfg.DataDir, "work"),
		"PYTHONUNBUFFERED=1",
	)
	// 自己的进程组：停止时对整组发信号，uvicorn 拉起的子进程不会变成孤儿。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// 输出接到主进程的 stdout/stderr：飞牛把主进程日志写到 /tmp/yinshu-ai.log，
	// sidecar 的报错跟主进程放一起，排障时不用找第二个文件。
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 sidecar 失败：%w", err)
	}
	m.mu.Lock()
	m.cmd = cmd
	m.pid = cmd.Process.Pid
	m.mu.Unlock()

	// 收尸：不等它的话，进程退出后会留一个僵尸。
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		if m.state == StateReady || m.state == StateStarting {
			m.state = StateFailed
			m.lastErr = "sidecar 进程已退出"
			if err != nil {
				m.lastErr = "sidecar 进程退出：" + err.Error()
			}
		}
		m.cmd = nil
		m.mu.Unlock()
		m.cfg.Logf("[SIDECAR] 进程结束：%v", err)
	}()
	return cmd, nil
}

// Stop 优雅停掉 sidecar。**幂等**，可以反复调。
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.state = StateStopped
	m.cmd = nil
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = m.kill(cmd)
	}
	m.killStale()
}

// kill 先 SIGTERM 整组，给 5 秒自己退，再 SIGKILL。
func (m *Manager) kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// 负号 = 整个进程组（spawn 时 Setpgid 了）
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return nil // 已经没了
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	return nil
}

// killStale 清掉「端口上还挂着、但不是我们这次起的」那个进程。
//
// 场景：主进程被 kill -9（升级、断电）→ sidecar 留在那儿占着端口。不清掉的话
// 新进程绑不上端口、静默退出，而现象是「健康检查一直不过」。
// 判据是**端口**而不是进程名：按名字匹配容易误杀别人的 python。
func (m *Manager) killStale() {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(m.cfg.Port))
	if err == nil {
		_ = ln.Close()
		return // 端口空着，没有残留
	}
	pids := pidsOnPort(m.cfg.Port)
	if len(pids) == 0 {
		m.cfg.Logf("[SIDECAR] 端口 %d 被占，但查不到持有者（不猜、不动）", m.cfg.Port)
		return
	}
	for _, pid := range pids {
		m.cfg.Logf("[SIDECAR] 清理上一次残留的 sidecar 进程 pid=%d", pid)
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// pidsOnPort 从 /proc/net/tcp 找出监听该端口的进程 pid。
//
// 只在 Linux 上有意义（飞牛就是 Linux）；读不到就返回空，调用方不会因此出错。
func pidsOnPort(port int) []int {
	raw, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return nil
	}
	// 本地地址形如 `0100007F:22C5`（小端 hex IP + 大端 hex 端口），状态 0A = LISTEN
	want := fmt.Sprintf(":%04X", port)
	var inodes []string
	for _, line := range strings.Split(string(raw), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		if !strings.HasSuffix(fields[1], want) {
			continue
		}
		inodes = append(inodes, fields[9])
	}
	if len(inodes) == 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", e.Name(), "fd"))
		if err != nil {
			continue
		}
		matched := false
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", e.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			for _, inode := range inodes {
				if link == "socket:["+inode+"]" {
					out = append(out, pid)
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}
	return out
}

// pingHealth 打一次 /healthz。
func pingHealth(base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查返回 %d", resp.StatusCode)
	}
	return nil
}

// runCmd 跑一条外部命令，返回合并输出。
func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
