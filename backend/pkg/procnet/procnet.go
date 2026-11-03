// Package procnet 回答一个问题：**是谁正占着这个端口？**
//
// # 为什么需要它
//
// 曲率会拉起两类子进程（Python sidecar、Node 的 lx 宿主），它们各自监听一个回环端口。
// 应用升级 / 被 kill -9 时，主进程死了，子进程却被 init 收养继续活着（实测 PPID=1），
// 端口还握在手里。下一代进程起来之后：
//
//	· 自己去 bind → EADDRINUSE 秒退；
//	· 或者更隐蔽的 —— 健康检查**由那个老进程应答**，于是新进程以为自己「已就绪」，
//	  等到真正拉起自己的子进程时才撞上端口占用。
//
// 两条路都得先知道「端口上是谁」才能处理。判据**必须是端口**而不是进程名：
// 按名字匹配会误杀用户自己跑的别的 node / python。
//
// # 实现与边界
//
// 数据来源是 `/proc/net/tcp` 的 socket inode → 扫 `/proc/<pid>/fd` 反查持有者。
// 这是 Linux 专有路径（飞牛就是 Linux），读不到就返回空，调用方按「查不到」处理。
//
// **只认 IPv4、只认 LISTEN 状态**：我们的子进程都 bind 在 127.0.0.1 上，
// 而 TIME_WAIT / ESTABLISHED 之类的连接记录不是「端口被占」，混进来会误杀无关进程。
//
// # 权限天花板（不要假装能越过）
//
// 非 root 只能读**自己 uid** 进程的 `/proc/<pid>/fd`，读别人的会 EACCES。
// 所以「端口被 root 的残留进程占着」这种情况，这里会**查不到持有者** ——
// 调用方必须明说「不猜、不动手，请人工确认」，绝不能因为查不到就退回按进程名 pkill。
package procnet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Holder 是「占着某个监听端口的进程」的一份快照。
type Holder struct {
	PID int
	// Cmdline 是 /proc/<pid>/cmdline 的可读形式（NUL 换成空格，去首尾空白）。
	// 拿不到时是空串 —— 它只是排障线索，**不是**判据。
	Cmdline string
	// UID 是进程的属主 uid；拿不到时为 -1。用来给「杀不掉」一个可解释的原因。
	UID int
}

// String 供日志直接打印。
func (h Holder) String() string {
	if h.Cmdline == "" {
		return fmt.Sprintf("pid=%d", h.PID)
	}
	return fmt.Sprintf("pid=%d uid=%d cmd=%s", h.PID, h.UID, h.Cmdline)
}

// listenState 是 /proc/net/tcp 里 LISTEN 的状态码（内核 TCP_LISTEN = 10）。
const listenState = "0A"

// selfUID 缓存本进程 uid，用于调用方做「别杀自己」的判断。
func selfUID() int { return os.Getuid() }

// Holders 返回所有**正在 LISTEN** 指定端口的进程。
//
// port 非法（<=0）时返回空。读不到 /proc 或找不到持有者都返回空切片 + nil error ——
// 调用方无法也不该区分「端口没人占」与「占着但我没权限看」，这两种情况下的正确动作
// 都是「不猜、不动手」。
func Holders(port int) []Holder {
	if port <= 0 || port > 65535 {
		return nil
	}
	inodes := listenInodes(port)
	if len(inodes) == 0 {
		return nil
	}
	return holdersOfInodes(inodes)
}

// PortFree 报告这个回环端口**当前是否空闲**。
//
// 它只看 /proc 里的 LISTEN 记录，不去真的 bind —— bind 有副作用（会真的占住端口），
// 而且 TIME_WAIT 等状态会让 bind 失败但这里显示空闲。调用方拿它做「要不要清理」的预判，
// 真正的冲突仍由子进程的 listen 结果兜底。
func PortFree(port int) bool { return len(listenInodes(port)) == 0 }

// listenInodes 收集该端口上所有 LISTEN socket 的 inode 号。
//
// /proc/net/tcp 每行形如：
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
//	0: 0100007F:22C5 00000000:0000 0A 00000000:00000000 00:00000000 00000000  895        0 12345 1 ...
//
// local_address 是小端 hex IP + 大端 hex 端口；我们只匹配端口后缀，
// 因为 bind 在 0.0.0.0 与 127.0.0.1 上都算「占着这个端口」。
func listenInodes(port int) []string {
	raw, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return nil
	}
	want := fmt.Sprintf(":%04X", port)
	var inodes []string
	for _, line := range strings.Split(string(raw), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[3] != listenState {
			continue
		}
		if !strings.HasSuffix(fields[1], want) {
			continue
		}
		inodes = append(inodes, fields[9])
	}
	return inodes
}

// holdersOfInodes 扫 /proc/<pid>/fd，把 socket:[inode] 反查成进程。
func holdersOfInodes(inodes []string) []Holder {
	want := make(map[string]struct{}, len(inodes))
	for _, in := range inodes {
		want["socket:["+in+"]"] = struct{}{}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []Holder
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // 别人的进程 / 已退出：不是错误，只是看不见
		}
		matched := false
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if _, ok := want[link]; ok {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, describe(pid))
		}
	}
	return out
}

// describe 读一份进程快照（cmdline / uid）。读不到就留零值，不影响 PID 这个主判据。
func describe(pid int) Holder {
	h := Holder{PID: pid, UID: -1}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	if raw, err := os.ReadFile(filepath.Join(base, "cmdline")); err == nil {
		h.Cmdline = strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	}
	if raw, err := os.ReadFile(filepath.Join(base, "status")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			f := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(f) > 0 {
				if uid, err := strconv.Atoi(f[0]); err == nil {
					h.UID = uid
				}
			}
			break
		}
	}
	return h
}

// CanSignal 报告本进程**有没有可能**给目标进程发信号。
//
// root 可以；同 uid 可以；其余基本没戏（还有 CAP_KILL 这种例外，但飞牛应用
// 跑在 no-new-privileges 下，不必为它写复杂判定）。false 时调用方应直接提示人工处理，
// 不要先试 kill 再报一个莫名其妙的「操作被拒绝」。
func CanSignal(h Holder) bool {
	if selfUID() == 0 {
		return true
	}
	if h.UID < 0 {
		// 不知道属主：不假装知道。让调用方去试一次 syscall，由内核给结论。
		return true
	}
	return h.UID == selfUID()
}
