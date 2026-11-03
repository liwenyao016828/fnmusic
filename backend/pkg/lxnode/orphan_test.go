package lxnode

// 这一组是 2026-11-02 那次「lx 宿主一直 failed」故障的**回归用例**。
//
// 故障链条（真机日志）：
//
//	上一代 Node 宿主被 init 收养，继续占着 8920
//	  → 新后端 ping /health 拿到 200（老进程答的）
//	  → 判定「已就绪」→ 拉起自己的子进程 → EADDRINUSE 秒退
//	  → 2 秒后 watch() 翻成 failed → 下一轮又重来
//
// 三个根因各对应下面几条用例：
//
//  1. spawn 之前不清端口上的残留          → TestClearStaleHostReapsOrphanHost
//  2. 就绪只看 200，不核对身份            → TestCheckIdentityRejectsOtherPID /
//                                           TestCheckIdentityRejectsLegacyHealthWithoutPID
//  3. 只杀单个 pid，留下进程组里的孤儿     → TestStopTerminatesWholeProcessGroup
//
// 另外两条是自保：不能因为「要清残留」就误杀自己、或误杀别人的服务。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"fn-lx-player/pkg/procnet"
)

// freeTCPPort 拿一个当前空闲的回环端口。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机起不了回环监听：%v", err)
	}
	defer ln.Close()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitPortFree 等端口真的让出来（kill 是异步的，立刻断言会抖动）。
func waitPortFree(t *testing.T, port int, budget time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if procnet.PortFree(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return procnet.PortFree(port)
}

// orphanHostScript 是一个**最小的假 lx 宿主**：只答 /health，自报 pid。
//
// 它必须是真进程 —— 这次故障的核心就是「另一个进程在答健康检查」，
// 用 httptest 在本进程里模拟是测不出「清掉别的进程」这件事的。
const orphanHostScript = `import http from 'node:http';
const port = Number(process.argv[2]);
http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'application/json'});
  res.end(JSON.stringify({ ok: true, pid: process.pid, startedAt: Date.now() }));
}).listen(port, '127.0.0.1', () => console.log('orphan host ready'));
`

// spawnOrphanHost 拉起一个**故意脱离我们管理**的宿主进程，模拟被 init 收养的孤儿。
//
// 它有自己的进程组（Setpgid），且测试不持有它的 Wait —— 与真机上
// 「主进程死了、Node 还活着」的形态一致。调用方负责 defer 清理。
func spawnOrphanHost(t *testing.T, port int) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	// 路径里带 lx_host，文件名用 server.mjs 的形式：clearStaleHost 只会清「认得出来是
	// 我们的宿主」的进程，这条自保不能在测试里被绕过。
	dir := filepath.Join(t.TempDir(), "lx_host")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "server.mjs")
	if err := os.WriteFile(script, []byte(orphanHostScript), 0o644); err != nil {
		t.Fatal(err)
	}
	// 命令行里带 lx_host / server.mjs 字样：clearStaleHost 只会清「认得出来是我们
	// 的宿主」的进程，这条自保不能在测试里被绕过。
	cmd := exec.Command("node", script, strconv.Itoa(port))
	cmd.Args[0] = "node"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起孤儿宿主失败：%v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !procnet.PortFree(port) {
			return cmd
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("孤儿宿主没在端口 %d 上监听", port)
	return nil
}

// TestClearStaleHostReapsOrphanHost 是这次故障的**主回归**：
// 端口被上一代宿主占着时，Start 之前必须能把它收掉、让端口空出来。
func TestClearStaleHostReapsOrphanHost(t *testing.T) {
	port := freeTCPPort(t)
	orphan := spawnOrphanHost(t, port)
	orphanPID := orphan.Process.Pid

	p := &execProc{cfg: Config{Port: port}}
	if err := p.clearStaleHost(); err != nil {
		t.Fatalf("该清得掉自己上一代的残留宿主：%v", err)
	}
	if !waitPortFree(t, port, 8*time.Second) {
		t.Fatalf("清完之后端口 %d 仍被占用", port)
	}
	if syscall.Kill(orphanPID, 0) == nil {
		// 进程可能已是僵尸（没人 Wait），所以这只作为附加信息，不作为判据。
		t.Logf("注意：pid %d 仍存在（可能是未收尸的僵尸）；判据以端口是否让出为准", orphanPID)
	}
}

// TestClearStaleHostRefusesForeignService 是自保：端口上跑的是**别人的**服务时，
// 宁可起不来，也不能替用户把它杀掉。
func TestClearStaleHostRefusesForeignService(t *testing.T) {
	// 在本进程里起一个「与 lx 无关」的 HTTP 服务。cmdline 是本测试二进制，
	// 显然不像 lx 宿主 —— 但持有者就是本进程，会先撞上「别杀自己」那条。
	// 所以这里直接验证判定函数本身，再用下面那条验证「本进程」分支。
	for _, cmdline := range []string{
		"python3 -m http.server 8920",
		"/usr/bin/node /home/user/myapp/index.js",
		"nginx: master process",
		"", // 读不到 cmdline 时同样不许当成「是我们」
	} {
		if looksLikeLxHost(cmdline) {
			t.Errorf("把别人的服务误认为 lx 宿主：%q", cmdline)
		}
	}
	for _, cmdline := range []string{
		"node /vol1/@appdata/yinshu-ai/sidecar/lx_host/server.mjs --dir /x --port 8920",
		"node /opt/qulv/lx_host/server.mjs --dir /x",
	} {
		if !looksLikeLxHost(cmdline) {
			t.Errorf("没认出我们自己的 lx 宿主：%q", cmdline)
		}
	}
}

// TestClearStaleHostRefusesToKillSelf：持有者就是本进程时必须拒绝。
func TestClearStaleHostRefusesToKillSelf(t *testing.T) {
	port := freeTCPPort(t)
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("端口 %d 起不来：%v", port, err)
	}
	defer ln.Close()
	if len(procnet.Holders(port)) == 0 {
		t.Skip("这个环境读不到 /proc/net/tcp，跳过")
	}

	p := &execProc{cfg: Config{Port: port}}
	err = p.clearStaleHost()
	if err == nil {
		t.Fatal("持有者是本进程时居然返回成功 —— 那等于把自己杀了")
	}
	if !strings.Contains(err.Error(), "本进程") {
		t.Fatalf("报错该点明是「自己占着」，实际：%v", err)
	}
	// 端口必须还在：没动过任何东西。
	if procnet.PortFree(port) {
		t.Fatal("自我清理把端口搞没了，说明真的对自己动手了")
	}
}

// TestClearStaleHostReportsUnknownHolder：查不到持有者时要「明说不动手」，
// 而不是悄悄返回成功（那会让后面的 EADDRINUSE 变成一句看不懂的报错）。
func TestClearStaleHostReportsUnknownHolder(t *testing.T) {
	port := freeTCPPort(t)
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("端口 %d 起不来：%v", port, err)
	}
	defer ln.Close()
	if len(procnet.Holders(port)) != 0 {
		t.Skip("这个环境能反查到持有者，构造不出「查不到」的场景")
	}
	p := &execProc{cfg: Config{Port: port}}
	err = p.clearStaleHost()
	if err == nil || !strings.Contains(err.Error(), "查不到持有者") {
		t.Fatalf("该报「查不到持有者，请人工确认」，实际：%v", err)
	}
}

// healthStub 是一个可指定「自报 pid」的 /health 应答器。
//
// 用例靠它模拟三种真实情况：我们这次拉起的、上一代残留的、以及老版本宿主
// （不报 pid 的那种）。
func healthStub(t *testing.T, reportedPID int, withPID bool) (base string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机起不了回环监听：%v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"ok": true, "startedAt": time.Now().UnixMilli()}
		if withPID {
			body["pid"] = reportedPID
		}
		_ = json.NewEncoder(w).Encode(body)
	})}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }
}

// TestCheckIdentityAcceptsOwnPID：身份对得上才算就绪。
func TestCheckIdentityAcceptsOwnPID(t *testing.T) {
	base, closeFn := healthStub(t, 4321, true)
	defer closeFn()
	p := &execProc{cfg: Config{baseOverride: base}}
	ok, err := p.checkIdentity(4321)
	if !ok || err != nil {
		t.Fatalf("自己的 pid 该被判为就绪：ok=%v err=%v", ok, err)
	}
}

// TestCheckIdentityRejectsOtherPID 是**这次故障的直接原因**：
// /health 答 200 但 pid 是别人的，绝不能算「已就绪」。
func TestCheckIdentityRejectsOtherPID(t *testing.T) {
	base, closeFn := healthStub(t, 143686, true) // 真机那个残留的 pid
	defer closeFn()
	p := &execProc{cfg: Config{baseOverride: base}}
	ok, err := p.checkIdentity(9999)
	if ok {
		t.Fatal("应答的是上一代残留进程，却被判定为就绪 —— 这正是本次故障")
	}
	if err == nil || !strings.Contains(err.Error(), "残留") {
		t.Fatalf("该把「应答的是另一个 pid」说清楚，实际：%v", err)
	}
}

// TestCheckIdentityRejectsLegacyHealthWithoutPID：老宿主不报 pid 时不能算就绪。
//
// 这条保护的是升级现场：端口上还在跑**旧版本**的宿主，它的 /health 没有 pid。
// 若把「缺 pid」当成通过，身份校验就等于形同虚设。
func TestCheckIdentityRejectsLegacyHealthWithoutPID(t *testing.T) {
	base, closeFn := healthStub(t, 0, false)
	defer closeFn()
	p := &execProc{cfg: Config{baseOverride: base}}
	if ok, _ := p.checkIdentity(9999); ok {
		t.Fatal("没有 pid 的 /health 应答不该被当成「本次拉起的进程已就绪」")
	}
}

// TestCheckIdentityWaitsWhenNothingListens：连不上属于「还没起来」，不是错误。
// 否则正常启动过程会被误判成失败。
func TestCheckIdentityWaitsWhenNothingListens(t *testing.T) {
	p := &execProc{cfg: Config{baseOverride: fmt.Sprintf("http://127.0.0.1:%d", freeTCPPort(t))}}
	ok, err := p.checkIdentity(1234)
	if ok || err != nil {
		t.Fatalf("连接被拒该返回 (false, nil) 继续等，实际 ok=%v err=%v", ok, err)
	}
}

// TestStopTerminatesWholeProcessGroup：Stop 必须整组收掉，而不是只杀父进程。
func TestStopTerminatesWholeProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	script := filepath.Join(t.TempDir(), "server.mjs")
	if err := os.WriteFile(script, []byte(orphanHostScript), 0o644); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	p := &execProc{cfg: Config{Port: port}}
	p.cmd = exec.Command("node", script, strconv.Itoa(port))
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.done = make(chan struct{})
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("拉起子进程失败：%v", err)
	}
	pid := p.cmd.Process.Pid
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	deadline := time.Now().Add(10 * time.Second)
	for procnet.PortFree(port) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if procnet.PortFree(port) {
		t.Fatalf("子进程没在端口 %d 上监听", port)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop 失败：%v", err)
	}
	if !waitPortFree(t, port, 8*time.Second) {
		t.Fatalf("Stop 之后端口 %d 仍被占着 —— 进程组没被收干净", port)
	}
	// 进程组里不该还有活口（对空进程组发信号会返回 ESRCH）。
	if syscall.Kill(-pid, 0) == nil {
		t.Fatalf("Stop 之后进程组 %d 里仍有存活进程", pid)
	}
}

// TestStartSpawnsIntoOwnProcessGroup：startRealHost 拉起的孩子必须在自己那一组，
// 否则 Stop 的 `kill(-pid)` 会把**我们自己这一组**也一起打（包括测试进程）。
func TestStartSpawnsIntoOwnProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	dir := t.TempDir()
	writeSources(t, dir, map[string]string{
		"ok.js": `lx.send('inited', { sources: { wy: { name: '合成' } } })
lx.on('request', async () => 'https://cdn.example.invalid/a.mp3')`,
	})
	m := startRealHost(t, dir, freeTCPPort(t))
	p, ok := m.proc.(*execProc)
	if !ok || p.cmd == nil || p.cmd.Process == nil {
		t.Fatal("拿不到真实子进程")
	}
	pid := p.cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Skipf("读不到 pgid：%v", err)
	}
	if pgid != pid {
		t.Fatalf("子进程没在自己的进程组里：pid=%d pgid=%d", pid, pgid)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatal("子进程与我们同组 —— Stop 的整组信号会打到自己")
	}
}

// TestRealHostHealthReportsIdentity：真宿主的 /health 必须真的把 pid 报出来，
// 且就是它自己的 pid（不是随便一个数字）。
func TestRealHostHealthReportsIdentity(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	dir := t.TempDir()
	writeSources(t, dir, map[string]string{
		"ok.js": `lx.send('inited', { sources: { wy: { name: '合成' } } })
lx.on('request', async () => 'https://cdn.example.invalid/a.mp3')`,
	})
	m := startRealHost(t, dir, freeTCPPort(t))
	p := m.proc.(*execProc)
	want := p.cmd.Process.Pid

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.base()+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health struct {
		PID       int   `json:"pid"`
		StartedAt int64 `json:"startedAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.PID != want {
		t.Fatalf("/health 自报 pid=%d，但真实子进程是 %d", health.PID, want)
	}
	if health.StartedAt <= 0 {
		t.Fatalf("startedAt 该是自报启动时刻，实际 %d", health.StartedAt)
	}
}
