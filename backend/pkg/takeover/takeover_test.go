//go:build linux

package takeover

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// 这些测试全部离线可跑：不碰 /var/run、不需要官方 daemon、不需要 root。
// 用的是真 Unix socket 与真 renameat2 —— 那正是最容易出事的部分，不 mock。

// fakeOfficial 模拟官方 trim-music daemon：监听一个 socket，识别
// /_qulv/livez 之外的任何请求并回一段可辨认的内容。
func fakeOfficial(t *testing.T, path string) (stop func()) {
	t.Helper()
	ln, err := listenUnixNoUnlink(path)
	if err != nil {
		t.Fatalf("起假官方 daemon 失败: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_qulv/livez" {
			// 官方 daemon 不认识这个端点，回自己的东西。
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "OFFICIAL:%s", r.URL.Path)
	})}
	go func() { _ = srv.Serve(ln) }()
	markFake(path, StateOfficial)
	return func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(path)
		unmarkFake(path)
	}
}

// 测试用的身份注册表。
//
// 被测试的接管层和假 daemon 跑在同一个进程里，SO_PEERCRED 给出的 pid
// 必然相同，/proc/<pid>/exe 也必然指向测试二进制 —— 内核那两条判据在
// 进程内测试里天然失效。这里用 classifyOverride 显式告诉 inspect
// 「这个路径在测试里算官方 / 算陌生进程」，等价于真机上内核给出的答案。
var (
	fakeMu    sync.Mutex
	fakeKinds = map[string]State{}
)

func markFake(path string, st State) {
	fakeMu.Lock()
	fakeKinds[path] = st
	fakeMu.Unlock()
}

func unmarkFake(path string) {
	fakeMu.Lock()
	delete(fakeKinds, path)
	fakeMu.Unlock()
}

func TestMain(m *testing.M) {
	classifyOverride = func(path string) (State, bool) {
		fakeMu.Lock()
		defer fakeMu.Unlock()
		st, ok := fakeKinds[path]
		return st, ok
	}
	os.Exit(m.Run())
}

// fakeForeignProxy 模拟别人的代理（例如 fnmusic-ext）：占住 socket 但
// 不认我们的 livez 端点。
func fakeForeignProxy(t *testing.T, path string) (stop func()) {
	t.Helper()
	ln, err := listenUnixNoUnlink(path)
	if err != nil {
		t.Fatalf("起假第三方代理失败: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"fnmusic-ext","pid":1}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	markFake(path, StateOtherProxy)
	return func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(path)
		unmarkFake(path)
	}
}

// fakeDeadSocket 造一个「文件在但没人监听」的死 socket。
func fakeDeadSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := listenUnixNoUnlink(path)
	if err != nil {
		t.Fatalf("造死 socket 失败: %v", err)
	}
	_ = ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("本平台 Close 会删掉 socket 文件，无法构造 stale 场景: %v", err)
	}
}

func newTestManager(t *testing.T, dir string) (*Manager, string, string) {
	t.Helper()
	target := filepath.Join(dir, "trim_music.socket")
	upstream := filepath.Join(dir, "trim_music_upstream.socket")

	// 归属表由 Manager 自己的 oursPath 登记（Enable 成功后登记 target）。
	// 这里只补一个兜底：被测试代码起的 staging 路径由它自己认。
	m := New(Options{
		Target:   target,
		Upstream: upstream,
		DataDir:  filepath.Join(dir, "state"),
		// 日志走 t.Logf，失败时能看到接管过程的每一步。
		Logf:         t.Logf,
		BootWait:     1500 * time.Millisecond,
		UpstreamWait: 1500 * time.Millisecond,
		// 假 daemon 的「身份」是挂在路径上的，socket 文件改名后要跟着搬。
		OnMoved: func(oldPath, newPath string) {
			fakeMu.Lock()
			st, ok := fakeKinds[oldPath]
			if ok {
				delete(fakeKinds, oldPath)
				fakeKinds[newPath] = st
			}
			fakeMu.Unlock()
		},
	})
	return m, target, upstream
}

func enableTakeover(t *testing.T) {
	t.Helper()
	t.Setenv(EnvEnable, "1")
}

// httpOverUnix 直接对 Unix socket 发一个 HTTP 请求。
func httpOverUnix(t *testing.T, sock, path string) string {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("连 %s 失败: %v", sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + path + " HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	idx := strings.Index(string(raw), "\r\n\r\n")
	if idx < 0 {
		t.Fatalf("响应缺少头部结束标记: %q", raw)
	}
	return string(raw[idx+4:])
}

// ---------- moveNoReplace ----------

func TestMoveNoReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.sock")
	dst := filepath.Join(dir, "dst.sock")

	ln, err := listenUnixNoUnlink(src)
	if err != nil {
		t.Fatalf("起监听器失败: %v", err)
	}
	defer ln.Close()

	if err := moveNoReplace(src, dst); err != nil {
		t.Fatalf("renameat2 失败: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("源路径应该不存在了，stat err = %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("目标路径应该存在: %v", err)
	}
}

// 目标已存在时必须拒绝，绝不覆盖 —— 覆盖意味着把别人（很可能是官方）的
// socket 直接抹掉，这是本包最不能犯的错。
func TestMoveNoReplaceRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.sock")
	dst := filepath.Join(dir, "dst.sock")

	ln, err := listenUnixNoUnlink(src)
	if err != nil {
		t.Fatalf("起监听器失败: %v", err)
	}
	defer ln.Close()

	if err := os.WriteFile(dst, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = moveNoReplace(src, dst)
	if err == nil {
		t.Fatal("目标已存在时应该拒绝")
	}
	if !errors.Is(err, ErrUnsafe) {
		t.Errorf("应该是 ErrUnsafe，得到 %v", err)
	}
	body, _ := os.ReadFile(dst)
	if string(body) != "occupied" {
		t.Errorf("目标文件被改动了: %q", body)
	}
}

// ---------- inspect 身份判定 ----------

func TestInspectAbsentAndStale(t *testing.T) {
	dir := t.TempDir()

	if got := inspect(filepath.Join(dir, "nope.sock"), nil); got.State != StateAbsent {
		t.Errorf("不存在的路径应为 absent，得到 %s", got.State)
	}

	dead := filepath.Join(dir, "dead.sock")
	fakeDeadSocket(t, dead)
	if got := inspect(dead, nil); got.State != StateStale {
		t.Errorf("死 socket 应为 stale，得到 %s (%s)", got.State, got.Detail)
	}
}

func TestInspectForeignProxy(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "foreign.sock")
	stop := fakeForeignProxy(t, sock)
	defer stop()

	got := inspect(sock, nil)
	if got.State != StateOtherProxy {
		t.Errorf("第三方代理应为 other-proxy，得到 %s (%s)", got.State, got.Detail)
	}
}

// ---------- 接管与还原 ----------

func TestEnableThenCloseRestoresOfficial(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	if got := m.inspect(target); got.State != StateOfficial {
		t.Fatalf("前置条件不成立：target 应为 official，得到 %s (%s)", got.State, got.Detail)
	}

	if err := m.Enable(); err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	if got := m.inspect(target); got.State != StateOurs {
		t.Errorf("接管后 target 应为 ours，得到 %s (%s)", got.State, got.Detail)
	}
	if got := m.inspect(upstream); got.State != StateOfficial {
		t.Errorf("接管后 upstream 应为 official，得到 %s (%s)", got.State, got.Detail)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	if got := m.inspect(target); got.State != StateOfficial {
		t.Errorf("还原后 target 应为 official，得到 %s (%s)", got.State, got.Detail)
	}
	if got := m.inspect(upstream); got.State != StateAbsent {
		t.Errorf("还原后 upstream 应已清空，得到 %s", got.State)
	}
}

// 接管期间普通请求必须原样到达官方 —— 阶段 1 的全部意义就在这里：
// 接管了，但官方一切照旧。
func TestEnablePassesThroughToOfficial(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	if err := m.Enable(); err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	defer func() { _ = m.Close() }()

	body := httpOverUnix(t, target, "/music/api/search?q=x")
	if !strings.Contains(body, "OFFICIAL:/music/api/search") {
		t.Errorf("透传结果不对: %q", body)
	}

	// 自有端点由接管层本地应答，不能透传。
	livez := httpOverUnix(t, target, LivezPath)
	if !strings.Contains(livez, livezService) {
		t.Errorf("livez 应本地应答，得到 %q", livez)
	}
	health := httpOverUnix(t, target, HealthzPath)
	if !strings.Contains(health, `"ok":true`) {
		t.Errorf("healthz 应报告 ok，得到 %q", health)
	}
}

// 已经在别人的代理后面时，我们必须让路而不是抢位置。
func TestEnableYieldsToForeignProxy(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	// 拓扑：官方在 upstream，别人的代理占了 target。
	stopOfficial := fakeOfficial(t, upstream)
	defer stopOfficial()
	stopForeign := fakeForeignProxy(t, target)
	defer stopForeign()

	if err := m.Enable(); err != nil {
		t.Fatalf("检测到第三方代理时不应报错，而是放弃接管: %v", err)
	}
	if got := m.inspect(target); got.State != StateOtherProxy {
		t.Errorf("第三方代理不应被挤掉，得到 %s", got.State)
	}
	if got := m.inspect(upstream); got.State != StateOfficial {
		t.Errorf("官方应仍在 upstream，得到 %s", got.State)
	}
}

// 真机上的真实场景：别人的代理对不认识的路径直接透传给官方，
// 于是官方对 /_qulv/livez 回 200 + 前端 HTML —— 我们的自证探针拿不到
// JSON，target 只能落到 unknown。若只认 StateOtherProxy，这里会误判成
// 「拓扑不明」而拒绝接管并报错，日志里看不出「其实是别人在代理」。
//
// 靠拓扑判（上游是活的官方 + 入口不在官方手里）就能正确让路，
// 且不需要认识对手是谁。
func TestEnableYieldsWhenUpstreamOfficialButTargetUnrecognized(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	// 官方被挪到上游。
	stopOfficial := fakeOfficial(t, upstream)
	defer stopOfficial()

	// 顶在入口上的代理：不认我们的 livez，一律透传成前端 HTML。
	// 故意**不**登记进 fakeKinds —— 模拟「我们认不出它是谁」。
	ln, err := listenUnixNoUnlink(target)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defer os.Remove(target)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><title>飞牛音乐</title></html>"))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if err := m.Enable(); err != nil {
		t.Fatalf("上游是官方、入口被别人占着时应让路而不是报错: %v", err)
	}
	if st := m.Snapshot(); st.Active {
		t.Error("不应接管")
	}
	if m.yielded == "" {
		t.Error("让路必须留下理由，否则用户查不出为什么没接管")
	}
	// 别人的代理不能被挤掉，官方也不能被挪走。
	if _, err := os.Stat(target); err != nil {
		t.Errorf("别人的代理不应被动过: %v", err)
	}
	if got := m.inspect(upstream); got.State != StateOfficial {
		t.Errorf("官方应仍在 upstream，得到 %s", got.State)
	}
}

// target 被陌生进程占据时必须拒绝，不能盲动。
func TestEnableRefusesUnknownTarget(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	// 一个既不是官方、也不认我们 livez 的监听者。
	ln, err := listenUnixNoUnlink(target)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	markFake(target, StateUnknown)
	defer unmarkFake(target)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	err = m.Enable()
	if err == nil {
		t.Fatal("拓扑不明时应拒绝接管")
	}
	if !errors.Is(err, ErrUnsafe) {
		t.Errorf("应该是 ErrUnsafe，得到 %v", err)
	}
	if got := m.inspect(target); got.State == StateOurs {
		t.Error("拒绝接管后不应占住 target")
	}
}

// 两边都空时不能立刻下手：官方 daemon 可能只是还没起来。
// 等不到就必须失败 —— 占住 target 会让官方之后 bind 失败（EADDRINUSE）。
func TestEnableWaitsForOfficialAtBoot(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	start := time.Now()
	err := m.Enable()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("官方 daemon 始终没出现，应拒绝接管")
	}
	if !errors.Is(err, ErrUnsafe) {
		t.Errorf("应该是 ErrUnsafe，得到 %v", err)
	}
	if elapsed < 1200*time.Millisecond {
		t.Errorf("应该等满启动预算（%v），实际只等了 %v", 1500*time.Millisecond, elapsed)
	}
	if got := m.inspect(target); got.State == StateOurs {
		t.Error("超时后不应占住 target")
	}
}

// 上次崩在半路（官方停在 upstream）时，下一次启动必须先复原再重新接管。
func TestEnableRecoversFromInterruptedTakeover(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	// 制造「上次接管崩在半路」的现场：官方在 upstream，target 是死文件。
	stop := fakeOfficial(t, upstream)
	defer stop()
	fakeDeadSocket(t, target)

	// 以及那份崩溃时留下的 journal。
	if err := m.writeJournal(filepath.Join(dir, "stale-staging.sock")); err != nil {
		t.Fatalf("写 journal 失败: %v", err)
	}

	if err := m.Enable(); err != nil {
		t.Fatalf("应从半接管状态恢复并继续接管: %v", err)
	}
	defer func() { _ = m.Close() }()

	if got := m.inspect(target); got.State != StateOurs {
		t.Errorf("恢复后 target 应为 ours，得到 %s (%s)", got.State, got.Detail)
	}
	if got := m.inspect(upstream); got.State != StateOfficial {
		t.Errorf("恢复后 upstream 应为 official，得到 %s (%s)", got.State, got.Detail)
	}
	if body := httpOverUnix(t, target, "/music/api/x"); !strings.Contains(body, "OFFICIAL:") {
		t.Errorf("恢复后透传应正常，得到 %q", body)
	}
}

// ---------- 还原的预检 ----------

// 上游不是官方时拒绝还原 —— 把别人的代理当成官方挪回 target，
// 会让官方音乐彻底打不开。
func TestRestoreRefusesNonOfficialUpstream(t *testing.T) {
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)
	// 先落一条 journal。没有它的话「我们从没接管过」会先短路掉，就测不到上游预检了。
	if err := m.writeJournal(""); err != nil {
		t.Fatalf("写 journal 失败: %v", err)
	}

	stopForeign := fakeForeignProxy(t, upstream)
	defer stopForeign()

	err := Restore(Options{
		Target:   target,
		Upstream: upstream,
		DataDir:  filepath.Join(dir, "state"),
		Logf:     t.Logf,
	})
	if err == nil {
		t.Fatal("上游不是官方时应拒绝还原")
	}
	if !errors.Is(err, ErrUnsafe) {
		t.Errorf("应该是 ErrUnsafe，得到 %v", err)
	}
}

func TestRestoreNoopWhenNothingToRestore(t *testing.T) {
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	err := Restore(Options{
		Target:   target,
		Upstream: upstream,
		DataDir:  filepath.Join(dir, "state"),
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatalf("上游不存在时应是无操作: %v", err)
	}
	if got := m.inspect(target); got.State != StateOfficial {
		t.Errorf("无操作不应动 target，得到 %s", got.State)
	}
}

// 让路之后用户点了「还原」：我们没接管过，绝不能动别人的 socket。
// 这是 v2.1.69 修的回归 —— restore 原来只看上游是不是官方，
// 而让路时上游恰好就是官方（被那个扩展挪过去的），于是会把别人的 socket 挪走并删掉。
func TestRestoreNowDoesNotClobberForeignProxyAfterYield(t *testing.T) {
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)
	enableTakeover(t)

	stopOfficial := fakeOfficial(t, upstream)
	defer stopOfficial()
	stopForeign := fakeForeignProxy(t, target)
	defer stopForeign()

	if err := m.Enable(); err != nil {
		t.Fatalf("检测到别人在代理时应当让路而不是报错: %v", err)
	}
	if st := m.Snapshot(); st.Active {
		t.Fatal("别人已经在代理时不该接管")
	}
	if st := m.Snapshot(); st.Yielded == "" {
		t.Error("让路时应当记下理由")
	}

	if err := m.RestoreNow(); err != nil {
		t.Fatalf("没接管过时还原应当是无操作: %v", err)
	}
	if st := inspect(target, nil); st.State != StateOtherProxy {
		t.Errorf("别人的代理被动了: target 现在是 %s", st.State)
	}
	if st := inspect(upstream, nil); st.State != StateOfficial {
		t.Errorf("官方被动了: upstream 现在是 %s", st.State)
	}
	for _, p := range []string{target, upstream} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 不见了: %v", p, err)
		}
	}
}

// 我们被 kill -9 留下 journal，机器重启后别人的扩展先起来接管了入口。
// 这条陈旧的 journal 不能拿来动别人的 socket。
func TestRecoverDoesNotClobberForeignProxyWithStaleJournal(t *testing.T) {
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	if err := m.writeJournal(""); err != nil {
		t.Fatalf("写 journal 失败: %v", err)
	}
	stopOfficial := fakeOfficial(t, upstream)
	defer stopOfficial()
	stopForeign := fakeForeignProxy(t, target)
	defer stopForeign()

	if err := m.recoverIfNeeded(); err != nil {
		t.Fatalf("恢复时遇到别人的代理不该报错: %v", err)
	}
	if st := inspect(target, nil); st.State != StateOtherProxy {
		t.Errorf("别人的代理被动了: target 现在是 %s", st.State)
	}
	if st := inspect(upstream, nil); st.State != StateOfficial {
		t.Errorf("官方被动了: upstream 现在是 %s", st.State)
	}
	if _, err := os.Stat(m.journalPath()); !os.IsNotExist(err) {
		t.Error("作废的 journal 应当被清掉")
	}
}

// 没有 journal 的裸还原一律是无操作，不管上游长什么样。
func TestRestoreNoopWithoutJournal(t *testing.T) {
	dir := t.TempDir()
	m, target, upstream := newTestManager(t, dir)

	stopOfficial := fakeOfficial(t, upstream)
	defer stopOfficial()
	stopForeign := fakeForeignProxy(t, target)
	defer stopForeign()

	if err := m.restore(true); err != nil {
		t.Fatalf("没有 journal 时应当是无操作: %v", err)
	}
	if st := inspect(target, nil); st.State != StateOtherProxy {
		t.Errorf("别人的代理被动了: target 现在是 %s", st.State)
	}
	if st := inspect(upstream, nil); st.State != StateOfficial {
		t.Errorf("官方被动了: upstream 现在是 %s", st.State)
	}
}

// ---------- 未开启 ----------

func TestEnableDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	if err := m.Enable(); !errors.Is(err, ErrDisabled) {
		t.Fatalf("默认应返回 ErrDisabled，得到 %v", err)
	}
	if got := m.inspect(target); got.State != StateOfficial {
		t.Errorf("未开启时不应动 target，得到 %s", got.State)
	}
}

// ---------- 并发 ----------

// 同一个 DataDir 上两个 Manager 不能同时接管。
func TestLockPreventsSecondTakeover(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m1, target, upstream := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	if err := m1.Enable(); err != nil {
		t.Fatalf("第一个接管失败: %v", err)
	}
	defer func() { _ = m1.Close() }()

	m2 := New(Options{
		Target:       target,
		Upstream:     upstream,
		DataDir:      filepath.Join(dir, "state"),
		Logf:         t.Logf,
		BootWait:     500 * time.Millisecond,
		UpstreamWait: 500 * time.Millisecond,
	})
	err := m2.Enable()
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("第二个实例应被锁挡住，得到 %v", err)
	}
}

// 并发 Snapshot 不应与接管流程抢出数据竞争（go test -race 会验证）。
func TestSnapshotConcurrentWithEnable(t *testing.T) {
	enableTakeover(t)
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Snapshot()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	if err := m.Enable(); err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	wg.Wait()
	if err := m.Close(); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
}

// stubInterceptor 只认一个路径，其余返回 false 让它透传。
//
// 用它来钉死一件事：拦截层「处理不了就交回去」的契约必须真的成立 ——
// 拦截层自己出问题时，官方音乐不能跟着一起坏。
type stubInterceptor struct {
	path string
	body string
	seen int32
}

func (s *stubInterceptor) Handle(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != s.path {
		return false
	}
	atomic.AddInt32(&s.seen, 1)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(s.body))
	return true
}

func TestEnableRoutesThroughInterceptor(t *testing.T) {
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)
	enableTakeover(t)
	fakeOfficial(t, target)

	ic := &stubInterceptor{
		path: "/music/api/v1/search/track",
		body: `{"code":0,"msg":"","data":{"list":[],"total":0,"intercepted":true}}`,
	}
	m.opts.Interceptor = ic

	if err := m.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	defer func() { _ = m.Close() }()

	// 拦截层认领的路径 → 拿到拦截层的答复，官方收不到这个请求。
	if got := httpOverUnix(t, target, "/music/api/v1/search/track"); !strings.Contains(got, "intercepted") {
		t.Fatalf("拦截层没有生效，得到 %s", got)
	}
	if n := atomic.LoadInt32(&ic.seen); n != 1 {
		t.Fatalf("拦截层应被调用 1 次，得到 %d", n)
	}

	// 拦截层交回来的路径 → 原样透传到官方。
	if got := httpOverUnix(t, target, "/music/api/v1/playlist/list"); !strings.Contains(got, "OFFICIAL") {
		t.Fatalf("未拦截路径应透传到官方，得到 %s", got)
	}
	if n := atomic.LoadInt32(&ic.seen); n != 1 {
		t.Fatalf("未拦截路径不该再调拦截层，得到 %d 次", n)
	}
}

// deviceOf 返回路径所在文件系统的设备号，用来判断两处路径是否跨挂载点。
func deviceOf(t *testing.T, path string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatalf("stat %s 失败: %v", path, err)
	}
	return uint64(st.Dev)
}

// TestStagingLivesNextToTarget 钉住 staging socket 的位置约束。
//
// 发布那一步是 rename(staging, target)，而 rename 不允许跨文件系统（EXDEV）。
// staging 一旦落在 --data 目录里，真机上就会炸 —— 因为 data 在 /vol1、
// 官方 socket 在 /var/run，是两个挂载点。
func TestStagingLivesNextToTarget(t *testing.T) {
	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	ln, err := m.stageListener()
	if err != nil {
		t.Fatalf("stageListener 失败: %v", err)
	}
	defer dropStaging(ln)

	if got, want := filepath.Dir(stagingPath(ln)), filepath.Dir(target); got != want {
		t.Fatalf("staging socket 落在 %s，必须与 target 同目录 %s —— 否则发布时 rename 会跨文件系统", got, want)
	}
}

// TestEnableAcrossFilesystems 是那个真机 bug 的回归测试。
//
// 症状（NAS 上 2.1.70 首次尝试接管）：
//
//	[TAKEOVER] 未接管音乐入口：takeover: 发布失败，已回滚: invalid cross-device link
//
// 原因是 staging socket 起在 --data 下（/vol1），而 target 在 /var/run。
// 开发机上 --data 和 socket 都在 /tmp，同属一个挂载点，所以这个 bug 一路全绿。
//
// 这里用 /dev/shm（独立 tmpfs）当 target 目录、t.TempDir() 当 data 目录，
// 真实造出「跨文件系统」这个条件，断言接管照样成功。
func TestEnableAcrossFilesystems(t *testing.T) {
	enableTakeover(t)

	dataDir := t.TempDir()
	targetDir, err := os.MkdirTemp("/dev/shm", "qulv-xdev-")
	if err != nil {
		t.Skipf("无法在 /dev/shm 建目录，跳过跨文件系统用例: %v", err)
	}
	defer func() { _ = os.RemoveAll(targetDir) }()

	if deviceOf(t, targetDir) == deviceOf(t, dataDir) {
		t.Skip("/dev/shm 与临时目录同属一个挂载点，复现不了跨文件系统")
	}

	target := filepath.Join(targetDir, "trim_music.socket")
	upstream := filepath.Join(targetDir, "trim_music_upstream.socket")
	stop := fakeOfficial(t, target)
	defer stop()

	m := New(Options{
		Target:       target,
		Upstream:     upstream,
		DataDir:      filepath.Join(dataDir, "state"),
		Logf:         t.Logf,
		BootWait:     1500 * time.Millisecond,
		UpstreamWait: 1500 * time.Millisecond,
		OnMoved: func(oldPath, newPath string) {
			fakeMu.Lock()
			if st, ok := fakeKinds[oldPath]; ok {
				delete(fakeKinds, oldPath)
				fakeKinds[newPath] = st
			}
			fakeMu.Unlock()
		},
	})

	if err := m.Enable(); err != nil {
		t.Fatalf("跨文件系统接管失败（真机上这条就是 invalid cross-device link）: %v", err)
	}
	defer func() { _ = m.Close() }()

	if got := httpOverUnix(t, target, LivezPath); !strings.Contains(got, livezService) {
		t.Fatalf("接管后 %s 上没有接管层应答: %s", target, got)
	}
	if got := httpOverUnix(t, upstream, "/probe"); !strings.Contains(got, "OFFICIAL:/probe") {
		t.Fatalf("官方没有让路到上游 %s: %s", upstream, got)
	}
}

// TestDropStagingRemovesFile 钉住失败路径的收尾。
//
// listenUnixNoUnlink 故意不删 socket 文件（成功路径上它要被 rename 到 target），
// 所以失败路径必须自己清，否则 /var/run 里会堆下一串 .qulv-stage-*.sock。
func TestDropStagingRemovesFile(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newTestManager(t, dir)

	ln, err := m.stageListener()
	if err != nil {
		t.Fatalf("stageListener 失败: %v", err)
	}
	p := stagingPath(ln)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("staging 文件没建出来: %v", err)
	}

	dropStaging(ln)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("dropStaging 没清掉 %s（err=%v）", p, err)
	}
	// 幂等：对已经清过的监听器再调一次不能炸。
	dropStaging(nil)
	dropStaging(ln)
}

// TestSweepStaleStaging 钉住「kill -9 之后没人收尾」的清扫。
func TestSweepStaleStaging(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newTestManager(t, dir)

	stale := filepath.Join(dir, ".qulv-stage-99999.sock")
	keep := filepath.Join(dir, "trim_music.socket")
	for _, p := range []string{stale, keep} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatalf("准备 %s 失败: %v", p, err)
		}
	}

	m.sweepStaleStaging(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("残留的 staging 文件没被清掉: %s", stale)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("清扫误删了非 staging 文件 %s: %v", keep, err)
	}
}

// TestSocketModeFollowsTarget 钉住权限继承的取值规则。
func TestSocketModeFollowsTarget(t *testing.T) {
	dir := t.TempDir()

	restrictive := filepath.Join(dir, "s.sock")
	if err := os.WriteFile(restrictive, nil, 0o600); err != nil {
		t.Fatalf("准备 %s 失败: %v", restrictive, err)
	}
	if got := socketMode(restrictive); got != 0o600 {
		t.Fatalf("官方是 0600 时该继承 0600，得到 %04o", got)
	}
	// 官方不在时退回这个入口一贯的 0666。
	if got := socketMode(filepath.Join(dir, "absent.sock")); got != 0o666 {
		t.Fatalf("官方不在时该退回 0666，得到 %04o", got)
	}
}

// TestPublishedSocketModeMatchesOfficial 钉住「接管之后非 root 客户端仍然连得上」。
//
// 症状（NAS 上 2.1.71 第一次接管成功后）：入口 socket 变成 srwxr-xr-x —— 进程 umask 022
// 的产物；而官方入口一直是 srw-rw-rw-。root 连得上、日志干净，uid 895 直接 connect 失败。
// 也就是「接管成功，但除了 root 以外谁都打不开官方音乐」。
func TestPublishedSocketModeMatchesOfficial(t *testing.T) {
	enableTakeover(t)

	dir := t.TempDir()
	m, target, _ := newTestManager(t, dir)

	stop := fakeOfficial(t, target)
	defer stop()
	// 真机上官方入口就是 0666；假 daemon 受测试进程 umask 影响会是 0755，显式摆成一样。
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatalf("准备官方 socket 权限失败: %v", err)
	}

	if err := m.Enable(); err != nil {
		t.Fatalf("Enable 失败: %v", err)
	}
	defer func() { _ = m.Close() }()

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat %s 失败: %v", target, err)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Fatalf("接管后入口 socket 权限是 %04o，必须继承官方的 0666 —— 否则非 root 客户端连不上官方音乐", got)
	}
}
