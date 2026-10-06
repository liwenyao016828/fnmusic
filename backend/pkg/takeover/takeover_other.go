//go:build !linux

package takeover

import (
	"errors"
	"net"
	"os"
	"time"
)

// 非 Linux 平台（开发机是 macOS）只保证本包可编译、可跑不依赖接管的单元测试。
// 真正的接管逻辑依赖 Linux 独有的 renameat2 / SO_PEERCRED / /proc，
// 在别的平台上没有等价物，一律拒绝执行 —— 静默退化比直接失败危险得多。

// ErrNotLinux 表示当前平台不支持接管。
var ErrNotLinux = errors.New("takeover: 仅支持 Linux（依赖 renameat2 / SO_PEERCRED / /proc）")

// classifyOverride 见 takeover_linux.go（测试专用）。
var classifyOverride func(path string) (State, bool)

// Probe 是一次 socket 探测的结果。
type Probe struct {
	State  State
	Detail string
}

func listenUnixNoUnlink(path string) (net.Listener, error) {
	return nil, ErrNotLinux
}

func moveNoReplace(oldPath, newPath string) error {
	return ErrNotLinux
}

func flockExclusiveNB(f *os.File) error {
	return ErrNotLinux
}

func funlock(f *os.File) error {
	return ErrNotLinux
}

func inspect(path string, isOurs func(string) bool) Probe {
	return Probe{State: StateUnknown, Detail: ErrNotLinux.Error()}
}

func probeLivez(path string, timeout time.Duration) (bool, error) {
	return false, ErrNotLinux
}

func serviceAt(path string, timeout time.Duration) string {
	return ""
}
