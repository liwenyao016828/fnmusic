//go:build linux

package takeover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// listenUnixNoUnlink 在 path 上起一个不自动删文件的 Unix 监听器。
//
// 必须关掉 Go 的自动 unlink：我们要把这个监听器的 socket 文件 rename 到
// target 位置，如果 Go 在 Close 时按原路径 unlink，轻则删不掉（路径已不在），
// 重则删掉别人后来在该路径上建的 socket。
func listenUnixNoUnlink(path string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			}); err != nil {
				return err
			}
			return serr
		},
	}
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, err
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	return ln, nil
}

// moveNoReplace 用 renameat2(RENAME_NOREPLACE) 原子改名，且拒绝覆盖已存在的目标。
//
// 这是本包唯一允许动 socket 文件的方式：内核保证「要么完整发生，要么完全没发生」，
// 不会出现「目标被删了但新的还没到位」的窗口。内核/文件系统不支持时直接失败，
// 绝不用「先删再改名」退化 —— 那正是会把官方音乐弄死的做法。
func moveNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, unix.EINVAL):
		return fmt.Errorf("%w: renameat2(RENAME_NOREPLACE) 不被内核或文件系统支持，拒绝非原子替换", ErrUnsafe)
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("%w: 目标 %s 已存在，拒绝覆盖", ErrUnsafe, newPath)
	default:
		return err
	}
}

// flockExclusiveNB 取非阻塞独占文件锁。
func flockExclusiveNB(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func funlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// Probe 是一次 socket 探测的结果。
type Probe struct {
	State  State
	Detail string
}

// inspect 判定一个 socket 路径的形态。**无副作用**，可以随便调。
//
// isOurs 是可选的额外判据（见 Options.IsOurs），生产环境传 nil。
func inspect(path string, isOurs func(string) bool) Probe {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Probe{State: StateAbsent}
		}
		return Probe{State: StateUnknown, Detail: err.Error()}
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return Probe{State: StateUnknown, Detail: "路径存在但不是 socket"}
	}
	ino := inodeOf(fi)

	conn, err := net.DialTimeout("unix", path, defaultDialTimeout)
	if err != nil {
		// 文件在但连不上：多半是进程崩了留下的死文件。
		// 但必须复查 inode —— 期间可能已经被别人替换成活的 socket 了。
		if isRefused(err) && inodeOfPath(path) == ino {
			return Probe{State: StateStale}
		}
		return Probe{State: StateUnknown, Detail: err.Error()}
	}
	defer conn.Close()

	peer, ok := peerPID(conn)
	if !ok {
		return Probe{State: StateUnknown, Detail: "无法取得 peer 凭据"}
	}
	exe, exeErr := exeOf(peer)

	// 身份判定顺序很关键：
	//
	//  1. isOurs(path) —— 我们自己登记的路径，最可靠，不需要网络往返。
	//     生产环境这个回调为空；测试里它用来区分「同进程内的假 daemon」
	//     和「同进程内的被测代码」，光看 pid 是分不出来的。
	//  2. 官方可执行文件名 —— 内核给的，不可伪造。
	//  3. 剩下的一律靠 livez 自证：能应答 livez 的是别人的代理
	//     （例如 fnmusic-ext），应答不了的就是陌生进程，一律不动。
	if isOurs != nil && isOurs(path) {
		return Probe{State: StateOurs}
	}
	if isOfficialExe(exe) {
		return Probe{State: StateOfficial, Detail: exe}
	}
	// 测试专用的身份覆盖。见 classifyOverride 的注释。
	if classifyOverride != nil {
		if st, ok := classifyOverride(path); ok {
			return Probe{State: st, Detail: "测试覆盖"}
		}
	}
	if peer == os.Getpid() {
		return Probe{State: StateUnknown,
			Detail: "peer 是本进程但未登记为自有 socket，且 exe 不是官方: " + exe}
	}
	if live, liveErr := probeLivez(path, defaultProbeTimeout); liveErr == nil && live {
		// 能应答 livez 但不是官方 —— 别人的代理（例如 fnmusic-ext）。
		return Probe{State: StateOtherProxy}
	}
	detail := "peer pid " + strconv.Itoa(peer)
	if exe != "" {
		detail += " exe " + exe
	}
	if exeErr != nil {
		detail += " (读 /proc 失败: " + exeErr.Error() + ")"
	}
	return Probe{State: StateUnknown, Detail: detail}
}

// classifyOverride 是**测试专用**的身份覆盖钩子。
//
// 为什么必须有它：测试里的假官方 daemon 和被测代码跑在同一个进程里，
// SO_PEERCRED 必然给出同一个 pid，`/proc/<pid>/exe` 也必然指向测试二进制。
// 也就是说，内核给的两条判据在进程内测试里天然失效 —— 真机上它们是权威的，
// 在测试里必须能被替换掉，否则要么测不了，要么只能把假 daemon 挪到独立进程
// （那会让测试依赖 fork、变慢、且更难定位失败）。
//
// 生产环境这个变量永远是 nil。
var classifyOverride func(path string) (State, bool)

// officialExeNames 是官方 daemon 可执行文件的名字候选。
//
// 主判据是 `trim-music`（与 fnmusic-ext 的判据一致）。其余是不同版本 / 包装
// 脚本的兜底，避免把官方误判成 unknown 而拒绝接管。
var officialExeNames = map[string]bool{
	"trim-music":  true,
	"trim_music":  true,
	"trim-musicd": true,
	"fnos-music":  true,
}

func isOfficialExe(exe string) bool {
	if exe == "" {
		return false
	}
	base := exe
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return officialExeNames[base]
}

// peerPID 用 SO_PEERCRED 取对端 pid。这是内核给出的身份，不可伪造。
func peerPID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil && cred != nil {
			pid = int(cred.Pid)
		}
	}); err != nil {
		return 0, false
	}
	if serr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// exeOf 读 /proc/<pid>/exe。权限不足时返回错误（调用方据此判 unknown）。
func exeOf(pid int) (string, error) {
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	// 被删掉的可执行文件会带 " (deleted)" 后缀，剥掉再比。
	return strings.TrimSuffix(p, " (deleted)"), nil
}

// probeJSON 打一个 HTTP 请求到 Unix socket 并把响应体当 JSON 解出来。
//
// 手写而非 net/http：探测路径要能明确区分「连不上」「超时」「不是我们」，
// 用 http.Client 会把这些都糊成一个 error。
func probeJSON(path, route string, timeout time.Duration) (map[string]any, error) {
	conn, err := net.DialTimeout("unix", path, defaultDialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := "GET " + route + " HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > 64<<10 {
				return nil, errors.New("probe 响应过大")
			}
		}
		if err != nil {
			break
		}
	}
	idx := strings.Index(string(buf), "\r\n\r\n")
	if idx < 0 {
		return nil, errors.New("probe 响应缺少头部结束标记")
	}
	head := string(buf[:idx])
	if !strings.Contains(head, " 200 ") {
		return nil, fmt.Errorf("probe 状态非 200: %s", strings.SplitN(head, "\r\n", 2)[0])
	}
	var out map[string]any
	if err := json.Unmarshal(buf[idx+4:], &out); err != nil {
		return nil, err
	}
	return out, nil
}

// probeLivez 检查 path 上是不是我们的接管层。
//
// 判据两条：服务名对得上，且它自报的 pid 与内核给的 peer pid 一致。
// 只认服务名的话，任何会回这个 JSON 的东西都能冒充我们。
func probeLivez(path string, timeout time.Duration) (bool, error) {
	conn, err := net.DialTimeout("unix", path, defaultDialTimeout)
	if err != nil {
		return false, err
	}
	peer, ok := peerPID(conn)
	conn.Close()
	if !ok {
		return false, errors.New("无法取得 peer 凭据")
	}
	body, err := probeJSON(path, LivezPath, timeout)
	if err != nil {
		return false, err
	}
	if s, _ := body["service"].(string); s != livezService {
		return false, nil
	}
	switch v := body["pid"].(type) {
	case float64:
		return int(v) == peer, nil
	case string:
		n, _ := strconv.Atoi(v)
		return n == peer, nil
	}
	return false, nil
}

// serviceAt 问 path 上活着的代理自称是什么服务；问不到（没人监听、或对方不认这个探针路径）
// 返回空串。
//
// 与 probeLivez 的区别：**不要求 pid 对上**。这里问的可能是另一个进程里的我们自己，
// 用途只有一个 —— 动 socket 之前确认「入口上蹲着的那个东西」到底是不是我们家的。
func serviceAt(path string, timeout time.Duration) string {
	body, err := probeJSON(path, LivezPath, timeout)
	if err != nil {
		return ""
	}
	s, _ := body["service"].(string)
	return s
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

func inodeOfPath(path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return inodeOf(fi)
}

func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
