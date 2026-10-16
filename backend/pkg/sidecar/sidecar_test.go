package sidecar

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 这一组盯的是 sidecar 的**生命周期**，不碰真实网络与真实 pip。
//
// 手法：造一个假的「venv 里的 python3」—— 一个忽略参数、直接起一个只会答
// `/healthz` 的小 HTTP 服务的脚本。于是 spawn / 健康检查 / 就绪判定 / 停止
// 这几段真的代码全部跑到，而不用等一两分钟的 pip install。

// fakePython 是一段「假装自己是 venv 里的 python3」的脚本。
//
// 它必须从环境变量读端口（真实实现是 uvicorn 读 `--port` 参数，这里怎么读不重要，
// 重要的是 manager 得把端口通过环境变量传下去 —— 顺带把这条约定也测了）。
const fakePython = `#!/bin/sh
# 「这个 venv 到底能不能用」的判据是**能不能 import pip**（不是 bin/python3 在不在），
# 所以这条要放最前面：它模拟的是「已经装好依赖的正常 venv」。
case "$*" in
  *"-c import pip"*) exit 0 ;;
esac
# 只有「依赖没装好」那条路径才会走到这里。让它**快速失败**而不是挂住 ——
# 否则「标记匹配就跳过安装」那条用例在变异下会卡死（而不是干脆地变红）。
case "$*" in
  *pip*) echo "fake pip: 不该跑到这里（说明依赖标记没生效）" >&2; exit 1 ;;
esac
sleep "${FAKE_PY_SLEEP:-0}"
exec "$FAKE_PY_REAL" -c '
import http.server, os
port = int(os.environ["QULV_SIDECAR_PORT"])
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"{\"ok\": true}")
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
'
`

// fakeSystemPython 扮演「系统里的 python3」：给它 `-m venv <dir>` 时真的造出一个
// **可用**的 venv（bin/python3 指回自己 + pyvenv.cfg），于是建 venv → 装依赖这条
// 路径能在几毫秒内跑完，不用等真的 pip。
const fakeSystemPython = `#!/bin/sh
case "$*" in
  *"-m venv"*)
    d="$3"
    mkdir -p "$d/bin"
    printf 'home = /usr/bin\ninclude-system-site-packages = false\n' > "$d/pyvenv.cfg"
    ln -sf "$0" "$d/bin/python3"
    exit 0 ;;
  *"-c import pip"*) exit 0 ;;
  *"-m pip install"*) echo "Successfully installed fake"; exit 0 ;;
esac
exit 1
`

// fakeNoPipPython 是一个「看起来是 venv 里的 python、其实没有 pip」的半成品：
// 能执行、能应答别的调用，但一问 import pip 就失败。这正是
// `python3 -m venv` 在 ensurepip 缺失时留下的那种目录。
const fakeNoPipPython = `#!/bin/sh
case "$*" in
  *"-c import pip"*) exit 1 ;;
esac
exit 0
`

// writeFile 写一个测试用的文件（带权限），省得每处都写三行错误处理。
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// markVenvUsable 给假 venv 补上 pyvenv.cfg —— venvUsable 要求它存在，
// 因为「只有 bin/python3」正是半成品的形状。
func markVenvUsable(t *testing.T, m *Manager) {
	t.Helper()
	writeFile(t, filepath.Join(m.venvDir(), "pyvenv.cfg"), "home = /usr/bin\n", 0o644)
}

// setupFakeSidecar 造一个「依赖已装好」的 sidecar 环境：
// 源码目录 + 假 venv python + 与 requirements 内容匹配的依赖标记。
func setupFakeSidecar(t *testing.T, port int, sleep string) *Manager {
	t.Helper()
	dir := t.TempDir()
	data := t.TempDir()

	req := "musicdl>=2.14.0\nfastapi>=0.110\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(req), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(Config{
		Dir: dir, DataDir: data, Port: port,
		Sources: []string{"mg"}, Limit: 15, Logf: t.Logf,
	})

	// 假 venv python
	pyDir := filepath.Join(data, "venv", "bin")
	if err := os.MkdirAll(pyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(fakePython, "$FAKE_PY_REAL", realPython(t))
	if err := os.WriteFile(filepath.Join(pyDir, "python3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// pyvenv.cfg：venvUsable 认它是「这是个真 venv」的形状标记
	markVenvUsable(t, m)

	// 依赖标记：内容与 requirements.txt 的哈希一致 → ensureDeps 直接返回，
	// 不会去跑 pip（这正是「每次启动不重装」的那条路径）
	want, err := m.requirementsHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.markerPath(), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PY_SLEEP", sleep)
	return m
}

func realPython(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("这台机器上没有 python3，跳过 sidecar 生命周期用例")
	return ""
}

// freePort 找一个空着的端口（避免与真在跑的 sidecar 撞）。
//
// 用「时间纳秒取模」而不是 net.Listen 探测：探测要 listen 再关，中间那段窗口
// 别的进程可能正好占上；而这个区间（18800–18999）只给测试用，撞上的概率极低，
// 真撞上了健康检查会失败、用例会红 —— 不会静默通过。
func freePort(t *testing.T) int {
	t.Helper()
	return 18800 + int(time.Now().UnixNano()%200)
}

// listenTCP 起一个监听（给「谁在这个端口上」那条用例用）。
func listenTCP(port int) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
}

// syscallKillZero 是 kill(pid, 0)：只探测进程在不在，不真的发信号。
func syscallKillZero(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// ── 基础 ──────────────────────────────────────────────────────────────

func TestNewDefaultsAndStatus(t *testing.T) {
	m := New(Config{})
	if m.Base() != "http://127.0.0.1:8901" {
		t.Fatalf("默认端口该是 8901，得到 %s", m.Base())
	}
	if m.State() != StateOff {
		t.Fatalf("新建时该是 off，得到 %s", m.State())
	}
	if m.Ready() {
		t.Fatal("没启动就不该报 ready")
	}
	st := m.Status()
	if st["state"] != StateOff || st["port"] != 8901 {
		t.Fatalf("状态快照不对：%+v", st)
	}
}

func TestStatusCarriesSourcesAndProgress(t *testing.T) {
	m := New(Config{Port: 8902, Sources: []string{"mg", "bq"}, Limit: 10})
	st := m.Status()
	if src, ok := st["sources"].([]string); !ok || len(src) != 2 {
		t.Fatalf("状态里该带上启用的平台：%+v", st["sources"])
	}
	m.setState(StatePreparing, "正在安装 Python 依赖")
	st = m.Status()
	if st["note"] != "正在安装 Python 依赖" {
		t.Fatalf("准备阶段该把进度说明带出来（用户要等一两分钟，看不到进度会以为卡死）：%+v", st)
	}
}

// ── 依赖判定 ──────────────────────────────────────────────────────────

func TestRequirementsHashChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(req, []byte("musicdl>=2.14.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Dir: dir})

	first, err := m.requirementsHash()
	if err != nil {
		t.Fatal(err)
	}
	// 同样内容 → 同样哈希（否则每次启动都会重装一遍依赖）
	again, _ := m.requirementsHash()
	if first != again {
		t.Fatal("内容没变时哈希必须稳定")
	}
	// 内容变了（升级 fpk 带来新依赖）→ 哈希变 → 触发重装
	if err := os.WriteFile(req, []byte("musicdl>=2.15.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _ := m.requirementsHash()
	if changed == first {
		t.Fatal("内容变了哈希必须变，否则升级后不会补装依赖")
	}

	// 文件不在 → 报错（而不是当成「没依赖」）
	if _, err := New(Config{Dir: filepath.Join(dir, "nope")}).requirementsHash(); err == nil {
		t.Fatal("requirements.txt 不存在时该报错")
	}
}

func TestEnsureDepsSkipsInstallWhenMarkerMatches(t *testing.T) {
	// 这条守的是「别每次启动都跑 pip」：标记匹配 + venv python 在 → 直接返回，
	// 一次外部命令都不跑。跑 pip 要一两分钟，每次启动都跑是不可接受的。
	m := setupFakeSidecar(t, freePort(t), "0")
	start := time.Now()
	if err := m.ensureDeps(context.Background()); err != nil {
		t.Fatalf("标记匹配时该直接返回：%v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("跳过安装这条路该是毫秒级的，实际花了 %v", d)
	}
}

func TestEnsureDepsFailsWithoutPython(t *testing.T) {
	// 没有 python3 时必须**如实报错**（而不是静默当成没事），
	// 否则现象是「sidecar 一直起不来但不知道为什么」。
	m := New(Config{
		Dir:        t.TempDir(),
		DataDir:    t.TempDir(),
		PythonPath: "/nonexistent/python3",
	})
	if err := os.WriteFile(filepath.Join(m.cfg.Dir, "requirements.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := m.ensureDeps(context.Background())
	if err == nil {
		t.Fatal("指定的解释器不存在时该报错")
	}
	if !strings.Contains(err.Error(), "Python") {
		t.Fatalf("错误信息该说清是 Python 的问题：%v", err)
	}
}

// ── venv 的「真能用」判据（2026-10-16 真机事故的回归）──────────────────
//
// 事故现场：NAS 上没装 python3-venv，`python3 -m venv` 失败但**照样留下目录**
// （bin/python3 只是指向系统解释器的符号链接 + pyvenv.cfg）。当时的判据是
// 「os.Stat(bin/python3) 成功即已就绪」，于是半成品被当成装好的 venv，
// 跳过建 venv 直接 `-m pip`，报「No module named pip」，而且点多少次
// 「重试启动」都被同一条件短路、永远修不好。
// 下面三条把「怎么算能用」「坏了要重建」「失败要清干净」分别钉住。

func TestVenvUsableRejectsHalfBuiltVenv(t *testing.T) {
	ctx := context.Background()
	m := New(Config{DataDir: t.TempDir()})

	// 只有 bin/python3、没有 pyvenv.cfg：正是 ensurepip 缺失留下的形状
	writeFile(t, m.venvPython(), "#!/bin/sh\nexit 0\n", 0o755)
	if m.venvUsable(ctx) {
		t.Fatal("只有 bin/python3 的半成品 venv 不该被判成可用")
	}

	// 补上 pyvenv.cfg，且解释器真的能 import pip → 这才算可用
	markVenvUsable(t, m)
	if !m.venvUsable(ctx) {
		t.Fatal("pyvenv.cfg 在 + 能 import pip 时该判成可用")
	}
}

func TestVenvUsableRejectsVenvWithoutPip(t *testing.T) {
	// pyvenv.cfg 在、python3 也能跑，但 venv 里没有 pip（ensurepip 没跑成）。
	// 这种 venv 拿去 `-m pip install` 必然报「No module named pip」。
	m := New(Config{DataDir: t.TempDir()})
	writeFile(t, m.venvPython(), fakeNoPipPython, 0o755)
	markVenvUsable(t, m)
	if m.venvUsable(context.Background()) {
		t.Fatal("不能 import pip 的 venv 不该被判成可用")
	}
}

func TestEnsureDepsRebuildsHalfBuiltVenv(t *testing.T) {
	// 关键回归：标记匹配（说明上次「装完了」）但 venv 是半成品 ——
	// 必须**重建**，而不是被标记短路掉。
	dir, data := t.TempDir(), t.TempDir()
	py := filepath.Join(t.TempDir(), "python3")
	writeFile(t, py, fakeSystemPython, 0o755)

	m := New(Config{Dir: dir, DataDir: data, PythonPath: py, Logf: t.Logf})
	writeFile(t, filepath.Join(dir, "requirements.txt"), "musicdl>=2.14.0\n", 0o644)

	// 半成品 venv（只有符号链接，没有 pyvenv.cfg） + 与 requirements 匹配的标记
	writeFile(t, m.venvPython(), "#!/bin/sh\nexit 0\n", 0o755)
	want, err := m.requirementsHash()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, m.markerPath(), want, 0o644)

	if err := m.ensureDeps(context.Background()); err != nil {
		t.Fatalf("半成品 venv 该被重建而不是直接失败：%v", err)
	}
	if !m.venvUsable(context.Background()) {
		t.Fatal("重建之后 venv 该真的可用")
	}
	if got, err := os.ReadFile(m.markerPath()); err != nil || strings.TrimSpace(string(got)) != want {
		t.Fatalf("重建后该重新写下依赖标记：%q %v", got, err)
	}
}

func TestEnsureDepsCleansUpFailedVenv(t *testing.T) {
	// 建 venv 失败时必须**把半成品目录清掉**，否则用户点「重试启动」时
	// 面对的仍是那堆残留；同时报错里要带上「装 python3-venv」这句人话，
	// 而不是只 tail 一段英文。
	dir, data := t.TempDir(), t.TempDir()
	py := filepath.Join(t.TempDir(), "python3")
	writeFile(t, py, `#!/bin/sh
echo "The virtual environment was not created successfully because ensurepip is not" >&2
echo "available.  On Debian/Ubuntu systems, you need to install the python3-venv" >&2
echo "package using the following command." >&2
exit 1
`, 0o755)

	m := New(Config{Dir: dir, DataDir: data, PythonPath: py, Logf: t.Logf})
	writeFile(t, filepath.Join(dir, "requirements.txt"), "musicdl>=2.14.0\n", 0o644)

	err := m.ensureDeps(context.Background())
	if err == nil {
		t.Fatal("建 venv 失败时该报错")
	}
	if !strings.Contains(err.Error(), "python3-venv") {
		t.Fatalf("错误信息该告诉用户装 python3-venv：%v", err)
	}
	if _, statErr := os.Stat(m.venvDir()); !os.IsNotExist(statErr) {
		t.Fatalf("失败的 venv 目录该被清掉（否则重试启动永远修不好）：%v", statErr)
	}
}

func TestEnsureDepsKeepsVenvWhenPipInstallFails(t *testing.T) {
	// 与上一条相反：venv 本身是好的、只是 pip install 失败（出不去外网最典型）。
	// 这时**不能**删 venv —— 换个网络重试就行，重建 venv 纯属浪费。
	dir, data := t.TempDir(), t.TempDir()
	py := filepath.Join(t.TempDir(), "python3")
	writeFile(t, py, `#!/bin/sh
case "$*" in
  *"-m venv"*)
    d="$3"; mkdir -p "$d/bin"
    printf 'home = /usr/bin\n' > "$d/pyvenv.cfg"
    ln -sf "$0" "$d/bin/python3"; exit 0 ;;
  *"-c import pip"*) exit 0 ;;
  *"-m pip install"*) echo "ERROR: Could not find a version that satisfies the requirement musicdl" >&2; exit 1 ;;
esac
exit 1
`, 0o755)

	m := New(Config{Dir: dir, DataDir: data, PythonPath: py, Logf: t.Logf})
	writeFile(t, filepath.Join(dir, "requirements.txt"), "musicdl>=2.14.0\n", 0o644)

	err := m.ensureDeps(context.Background())
	if err == nil {
		t.Fatal("pip install 失败时该报错")
	}
	if !strings.Contains(err.Error(), "装依赖失败") {
		t.Fatalf("错误信息该区分「装依赖失败」而不是「建 venv 失败」：%v", err)
	}
	if _, statErr := os.Stat(m.venvDir()); statErr != nil {
		t.Fatalf("只是装依赖失败时不该删掉可用的 venv：%v", statErr)
	}
	if _, statErr := os.Stat(m.markerPath()); !os.IsNotExist(statErr) {
		t.Fatal("装依赖失败时不该写依赖标记（否则下次会被当成装好了）")
	}
}

// ── 完整生命周期（假 venv python + 假健康服务）──────────────────────

func TestStartReachesReadyAndStopsCleanly(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("依赖 /bin/sh 与 python3")
	}
	port := freePort(t)
	m := setupFakeSidecar(t, port, "0")

	start := time.Now()
	m.Start(context.Background())
	if d := time.Since(start); d > 500*time.Millisecond {
		// Start 必须立刻返回：它背后要做 venv / pip / 等健康检查，全是慢操作。
		// 阻塞住就等于「开应用时界面卡两分钟」。
		t.Fatalf("Start 必须立即返回，实际阻塞了 %v", d)
	}
	if !m.WaitReady(20 * time.Second) {
		t.Fatalf("该到达 ready，实际 %s（%s）", m.State(), m.LastError())
	}
	if !m.Ready() {
		t.Fatal("Ready() 该为真")
	}
	st := m.Status()
	if st["pid"] == 0 || st["started_at"] == nil {
		t.Fatalf("就绪后状态里该有 pid 与启动时间：%+v", st)
	}
	if err := pingHealth(m.Base()); err != nil {
		t.Fatalf("就绪后 /healthz 该能打通：%v", err)
	}

	m.Stop()
	// 停止后端口必须空出来 —— 否则下次启动会因为端口被占而静默失败
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := pingHealth(m.Base()); err != nil {
			return // 已经没了
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Stop 之后端口上还有东西在应答（会留下孤儿进程）")
}

func TestStartIsIdempotentWhileRunning(t *testing.T) {
	port := freePort(t)
	m := setupFakeSidecar(t, port, "0")
	m.Start(context.Background())
	if !m.WaitReady(20 * time.Second) {
		t.Fatalf("该到达 ready：%s %s", m.State(), m.LastError())
	}
	defer m.Stop()

	before := m.Status()["pid"]
	m.Start(context.Background()) // 再调一次不该另起一个
	time.Sleep(300 * time.Millisecond)
	if after := m.Status()["pid"]; after != before {
		t.Fatalf("运行中再调 Start 不该换进程：%v → %v", before, after)
	}
}

func TestStartFailsClearlyWhenAppPyMissing(t *testing.T) {
	m := New(Config{Dir: t.TempDir(), DataDir: t.TempDir(), Port: freePort(t), Logf: t.Logf})
	if err := os.WriteFile(filepath.Join(m.cfg.Dir, "requirements.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 让依赖检查直接过，把失败点逼到「目录里没有 app.py」：
	// 标记匹配 **且** venv 真的可用（有 pyvenv.cfg + 能 import pip）才会短路。
	want, _ := m.requirementsHash()
	writeFile(t, m.venvPython(), "#!/bin/sh\nexit 0\n", 0o755)
	markVenvUsable(t, m)
	_ = os.WriteFile(m.markerPath(), []byte(want), 0o644)

	m.Start(context.Background())
	m.WaitReady(5 * time.Second)
	if m.State() != StateFailed {
		t.Fatalf("该是 failed，得到 %s", m.State())
	}
	if !strings.Contains(m.LastError(), "app.py") {
		t.Fatalf("错误信息该指出缺 app.py：%s", m.LastError())
	}
}

func TestStartFailsWhenHealthNeverComes(t *testing.T) {
	// 假 python 只是个睡不醒的进程：进程活着，但 /healthz 永远不通。
	// 这正是「进程起来了 ≠ 能用」的情形 —— 必须判成失败，不能报 ready。
	m := setupFakeSidecar(t, freePort(t), "600")
	// 缩短等待：直接调 run 会等 45 秒，这里用带取消的 ctx 把它打断
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.setState(StateStarting, "")
	if err := m.run(ctx); err == nil {
		t.Fatal("健康检查不通时该报错")
	}
	m.Stop()
}

// ── 残留进程清理 ──────────────────────────────────────────────────────

func TestKillStaleIsNoopWhenPortFree(t *testing.T) {
	// 端口空着时不该去动任何进程 —— 按端口找 pid 一旦误判就会杀掉别人的服务
	m := New(Config{Port: freePort(t), Logf: t.Logf})
	m.killStale() // 不 panic、不杀东西即为通过
	if m.State() != StateOff {
		t.Fatalf("killStale 不该改状态：%s", m.State())
	}
}

func TestPidsOnPortFindsListener(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc 只在 Linux 上有")
	}
	port := freePort(t)
	// 自己起一个监听，再问「谁在这个端口上」
	ln, err := listenTCP(port)
	if err != nil {
		t.Skipf("端口 %d 起不来：%v", port, err)
	}
	defer func() { _ = ln.Close() }()

	pids := pidsOnPort(port)
	if len(pids) == 0 {
		t.Skip("这个环境读不到 /proc/net/tcp（容器/受限），跳过")
	}
	self := os.Getpid()
	found := false
	for _, p := range pids {
		if p == self {
			found = true
		}
	}
	if !found {
		t.Fatalf("该认出自己（pid %d）在监听 %d，实际 %v", self, port, pids)
	}
}

func TestPidsOnPortEmptyForUnusedPort(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc 只在 Linux 上有")
	}
	if pids := pidsOnPort(freePort(t)); len(pids) != 0 {
		t.Fatalf("没人监听的端口不该报出 pid：%v", pids)
	}
}

// ── 进程组 ────────────────────────────────────────────────────────────

func TestStopKillsWholeProcessGroup(t *testing.T) {
	// 假 python 会 exec 一个真实 python 进程。停止时必须**整组**都没了，
	// 否则 uvicorn 拉起的子进程会变成孤儿（这正是「不留孤儿进程」那条要求）。
	port := freePort(t)
	m := setupFakeSidecar(t, port, "0")
	m.Start(context.Background())
	if !m.WaitReady(20 * time.Second) {
		t.Fatalf("该到达 ready：%s %s", m.State(), m.LastError())
	}
	pid := 0
	if v, ok := m.Status()["pid"].(int); ok {
		pid = v
	}
	m.Stop()

	if pid > 0 {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(pid) {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("pid %d 在 Stop 之后还活着", pid)
	}
}

func processAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		// 非 Linux：退回 kill -0
		return syscallKillZero(pid)
	}
	return true
}
