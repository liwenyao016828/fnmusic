package lxnode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 假进程：只验状态机，不碰真 node ──────────────────────────────────────

type fakeProc struct {
	started bool
	stopped bool
	done    chan struct{}
	failOn  error
}

func (f *fakeProc) Start() error {
	if f.failOn != nil {
		return f.failOn
	}
	f.started = true
	f.done = make(chan struct{})
	return nil
}
func (f *fakeProc) Stop() error             { f.stopped = true; return nil }
func (f *fakeProc) Exited() <-chan struct{} { return f.done }

func fakeManager(p *fakeProc) *Manager {
	m := New()
	m.newProc = func(Config) process { return p }
	return m
}

// 关掉时必须停进程、状态回 off —— 这是「用户关了就真关」的最小承诺。
func TestApplyDisabledStopsProcess(t *testing.T) {
	p := &fakeProc{}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	if m.Status()["state"] != StateReady {
		t.Fatalf("该是 ready：%+v", m.Status())
	}
	m.Apply(Config{Enabled: false})
	if !p.stopped {
		t.Fatal("关掉时该停掉子进程")
	}
	if got := m.Status()["state"]; got != StateOff {
		t.Fatalf("状态该是 off，得到 %v", got)
	}
}

// 起不来要如实报 failed 并带上原因，不能装作 ready。
func TestApplyStartFailureReportsFailed(t *testing.T) {
	p := &fakeProc{failOn: os.ErrPermission}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	st := m.Status()
	if st["state"] != StateFailed {
		t.Fatalf("该是 failed：%+v", st)
	}
	if st["error"] == nil || st["error"] == "" {
		t.Fatalf("failed 该带原因：%+v", st)
	}
}

// 进程自己死了，状态要跟着变 —— 否则界面会一直显示「可用」，而其实早没了。
func TestProcessExitMarksFailed(t *testing.T) {
	p := &fakeProc{}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	close(p.done) // 模拟进程退出
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status()["state"] == StateFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程退出后该变成 failed：%+v", m.Status())
}

// 宿主不可用时 Resolve 必须**只返回错误**（调用方回落），不能 panic、不能挂住。
func TestResolveUnavailableReturnsError(t *testing.T) {
	m := New()
	if _, err := m.Resolve(context.Background(), "wy", map[string]any{"name": "x"}, "320k"); err == nil {
		t.Fatal("没起来时 Resolve 该报错")
	}
}

// ── 真集成：起真的 node + 真的 sidecar/lx_host/server.mjs ────────────────

func TestRealHostStartsAndReports(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(root, "sidecar", "lx_host", "server.mjs")
	if _, err := os.Stat(host); err != nil {
		t.Skipf("宿主脚本不在：%v", err)
	}
	dir := t.TempDir()
	// 放一个能解析出直链的合成音源：证明整条链路真的通到「拿到 URL」。
	script := `
lx.send('inited', { sources: { wy: { name: '合成' } } })
lx.on('request', async ({ info }) => 'https://cdn.example.invalid/' + encodeURIComponent(info.name || 'x') + '.mp3')
`
	if err := os.WriteFile(filepath.Join(dir, "ok.js"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New()
	m.Apply(Config{Enabled: true, NodeBin: "node", Script: host, Dir: dir, Port: 18921})
	defer m.Stop()

	st := m.Status()
	if st["state"] != StateReady {
		t.Fatalf("真宿主该起来：%+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url, err := m.Resolve(ctx, "wy", map[string]any{"name": "晴天"}, "320k")
	if err != nil {
		t.Fatalf("该解析出直链：%v", err)
	}
	if !strings.Contains(url, "%E6%99%B4%E5%A4%A9") {
		t.Fatalf("直链该带上曲名（URL 编码后）：%q", url)
	}
	t.Logf("解析得到：%s", url)
}
