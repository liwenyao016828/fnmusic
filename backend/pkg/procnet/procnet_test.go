package procnet

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// portOf 把 listener 的实际端口拆出来。
func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("拆监听地址失败：%v", err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("端口不是数字：%v", err)
	}
	return n
}

// TestHoldersFindsOwnListener 是最核心的那条路径：本进程监听一个端口，
// procnet 必须能从 /proc 反查出**本进程**。
//
// 跑测试的进程与被查的进程是同一个，所以 /proc/<pid>/fd 一定可读，
// 不受「非 root 看不了别人」的限制 —— 用例在任何 uid 下都成立。
func TestHoldersFindsOwnListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机起不了 tcp4 监听：%v", err)
	}
	defer ln.Close()
	port := portOf(t, ln)

	holders := Holders(port)
	found := false
	for _, h := range holders {
		if h.PID == os.Getpid() {
			found = true
			if h.UID != os.Getuid() {
				t.Errorf("uid 反查错误：拿到 %d，期望 %d", h.UID, os.Getuid())
			}
		}
	}
	if !found {
		t.Fatalf("没有在端口 %d 的持有者里找到本进程 pid=%d（拿到 %+v）", port, os.Getpid(), holders)
	}
}

// TestPortFreeReflectsListener 钉住「空闲 ⇄ 有 LISTEN 记录」这个对应关系。
func TestPortFreeReflectsListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机起不了 tcp4 监听：%v", err)
	}
	port := portOf(t, ln)
	if PortFree(port) {
		t.Errorf("端口 %d 正被监听，PortFree 却报告空闲", port)
	}
	_ = ln.Close()
	if !PortFree(port) {
		t.Errorf("监听已关闭，端口 %d 仍被报告为占用", port)
	}
}

// TestHoldersRejectsInvalidPort：端口参数不合法时不能扫一遍 /proc 然后乱报。
func TestHoldersRejectsInvalidPort(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		if got := Holders(p); len(got) != 0 {
			t.Errorf("Holders(%d) = %+v，期望空", p, got)
		}
		if !PortFree(p) {
			t.Errorf("PortFree(%d) = false，非法端口不该被报成占用", p)
		}
	}
}

// TestHoldersOnlyMatchesExactPort 钉住端口匹配的边界：
// `strings.HasSuffix(local, ":0050")` 这种写法不能匹配到 `:10050` ——
// 因为后缀里带了冒号。这个用例守住「不许为了匹配而放宽成纯数字后缀」。
func TestHoldersOnlyMatchesExactPort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机起不了 tcp4 监听：%v", err)
	}
	defer ln.Close()
	port := portOf(t, ln)
	// 造一个「数字后缀相同、但多一位」的端口；超出范围就跳过。
	other := port + 10000
	if other > 65535 {
		t.Skipf("端口 %d 太大，没法构造后缀碰撞用例", port)
	}
	for _, h := range Holders(other) {
		if h.PID == os.Getpid() {
			t.Errorf("把端口 %d 的监听误算到端口 %d 头上", port, other)
		}
	}
}

// TestCanSignal 钉住自保判据：同 uid 可以、root 可以、明确的别人不行。
func TestCanSignal(t *testing.T) {
	self := Holder{PID: os.Getpid(), UID: os.Getuid()}
	if !CanSignal(self) {
		t.Error("同 uid 的进程被判定为不能发信号")
	}
	if os.Getuid() != 0 {
		other := Holder{PID: 1, UID: os.Getuid() + 1}
		if CanSignal(other) {
			t.Error("非 root 却被判定为能给别的 uid 的进程发信号")
		}
	}
	unknown := Holder{PID: 1, UID: -1}
	if !CanSignal(unknown) {
		t.Error("属主未知时应交给内核判定，而不是直接禁止")
	}
}

// TestHolderStringNeverPanics：cmdline 拿不到时也要能打印（排障日志一定会走到这）。
func TestHolderStringNeverPanics(t *testing.T) {
	for _, h := range []Holder{{PID: 1, UID: -1}, {PID: 2, UID: 0, Cmdline: "node x.mjs"}} {
		if s := h.String(); !strings.Contains(s, fmt.Sprintf("pid=%d", h.PID)) {
			t.Errorf("String() 里没有 pid：%q", s)
		}
	}
}
