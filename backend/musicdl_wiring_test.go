package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/sidecar"
)

// musicdl 接线（`musicdl_wiring.go`）的用例。
//
// 这一层最容易出的问题是**半开状态**：开关关了但解析器还挂着、平台名单换了但旧的
// 没摘干净、sidecar 挂了却把整次搜索弄坏。所以这里一条条钉死「什么配置 = 什么注册」。
//
// ⚠️ 用例注入假进程（`fakeProc`）：真 `sidecar.New` + `Start` 会建 venv 并
// pip install（一两分钟 + 一两百 MB），单测里绝不能跑那个。

// fakeProc 假装是 sidecar 进程。
type fakeProc struct {
	mu      sync.Mutex
	cfg     sidecar.Config
	started int
	stopped int
	state   string
}

func (f *fakeProc) Base() string  { return "http://127.0.0.1:18999" }
func (f *fakeProc) State() string { f.mu.Lock(); defer f.mu.Unlock(); return f.state }

func (f *fakeProc) Status() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"state": f.state, "port": f.cfg.Port, "pid": 1}
}

func (f *fakeProc) Start(context.Context) {
	f.mu.Lock()
	f.started++
	f.state = sidecar.StateReady
	f.mu.Unlock()
}

func (f *fakeProc) Stop() {
	f.mu.Lock()
	f.stopped++
	f.state = sidecar.StateStopped
	f.mu.Unlock()
}

// wiringHarness 造一个注入了假进程的 wiring。
func wiringHarness(t *testing.T) (*musicdlWiring, *online.Pool, *[]*fakeProc) {
	t.Helper()
	pool := online.NewPool(online.NetEaseResolver{}, online.QQResolver{})
	w := newMusicDLWiring(pool, t.TempDir(), t.Logf)

	procs := &[]*fakeProc{}
	w.newProc = func(cfg sidecar.Config) sidecarProcess {
		p := &fakeProc{cfg: cfg, state: sidecar.StateOff}
		*procs = append(*procs, p)
		return p
	}
	t.Cleanup(w.Stop)
	return w, pool, procs
}

// musicdlOn 造一个「开着 sidecar」的配置。
func musicdlOn(sources []string) config.AppConfig {
	on := true
	return config.AppConfig{MusicDLEnabled: &on, MusicDLSources: sources}
}

// searcherPlatforms 返回当前注册了外挂搜索器的平台（排序后便于比较）。
func searcherPlatforms() []string {
	got := search.ExternalSearcherPlatforms()
	// 排序，避免 map 遍历顺序导致用例抖动
	for i := 0; i < len(got); i++ {
		for j := i + 1; j < len(got); j++ {
			if got[j] < got[i] {
				got[i], got[j] = got[j], got[i]
			}
		}
	}
	return got
}

func cleanupSearchers(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, p := range search.ExternalSearcherPlatforms() {
			search.UnregisterSearcher(p)
		}
	})
}

func TestApplyOffRegistersNothing(t *testing.T) {
	w, pool, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(config.AppConfig{}) // 默认关

	if got := searcherPlatforms(); len(got) != 0 {
		t.Fatalf("关着的时候不该注册任何外挂搜索器：%v", got)
	}
	if len(*procs) != 0 {
		t.Fatalf("关着的时候不该造进程：%d 个", len(*procs))
	}
	// 原生两个平台照旧在（外挂只是「多几个」，不是替换）
	if !pool.Supports("wy") || !pool.Supports("tx") {
		t.Fatalf("原生解析器不该被动过：%v", pool.Platforms())
	}
	if pool.Supports("mg") {
		t.Fatal("关着的时候不该有 mg 解析器")
	}
}

func TestApplyOnRegistersSourcesAndStarts(t *testing.T) {
	w, pool, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn([]string{"mg", "bq"}))

	for _, p := range []string{"mg", "bq"} {
		if !pool.Supports(p) {
			t.Fatalf("%s 该有解析器了：%v", p, pool.Platforms())
		}
	}
	got := searcherPlatforms()
	if len(got) != 2 || got[0] != "bq" || got[1] != "mg" {
		t.Fatalf("搜索器该与解析器覆盖同一批平台：%v", got)
	}
	if len(*procs) != 1 {
		t.Fatalf("该造 1 个进程，得到 %d", len(*procs))
	}
	if (*procs)[0].started != 1 {
		t.Fatalf("该启动 1 次，得到 %d", (*procs)[0].started)
	}
	if !sameStrings((*procs)[0].cfg.Sources, []string{"mg", "bq"}) {
		t.Fatalf("平台名单该传给进程：%v", (*procs)[0].cfg.Sources)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	// 开机调一次 + 每次保存配置再调一次 —— 状态没变时必须什么都不做，
	// 否则「保存一下设置」会把 sidecar 重启一遍（用户正在听歌）。
	w, _, procs := wiringHarness(t)
	cleanupSearchers(t)

	cfg := musicdlOn([]string{"mg", "bq"})
	w.Apply(cfg)
	w.Apply(cfg)
	w.Apply(cfg)

	if len(*procs) != 1 {
		t.Fatalf("同配置反复 Apply 不该重建进程：造了 %d 个", len(*procs))
	}
	if (*procs)[0].started != 1 || (*procs)[0].stopped != 0 {
		t.Fatalf("不该反复启停：started=%d stopped=%d", (*procs)[0].started, (*procs)[0].stopped)
	}
}

func TestApplySwitchesSources(t *testing.T) {
	w, pool, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn([]string{"mg", "bq"}))
	w.Apply(musicdlOn([]string{"mg", "bi"}))

	if pool.Supports("bq") {
		t.Fatalf("换掉的平台该摘干净：%v", pool.Platforms())
	}
	if !pool.Supports("mg") || !pool.Supports("bi") {
		t.Fatalf("新名单该生效：%v", pool.Platforms())
	}
	got := searcherPlatforms()
	if len(got) != 2 || got[0] != "bi" || got[1] != "mg" {
		t.Fatalf("搜索器该跟着换：%v", got)
	}
	// 平台是通过 spawn 时的环境变量定死的 → 必须重建进程
	if len(*procs) != 2 {
		t.Fatalf("换平台该重建进程：造了 %d 个", len(*procs))
	}
	if (*procs)[0].stopped != 1 {
		t.Fatalf("旧进程该被停掉：stopped=%d", (*procs)[0].stopped)
	}
}

func TestApplyTurnOffTeardownEverything(t *testing.T) {
	w, pool, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn([]string{"mg", "bq", "bi"}))
	w.Apply(config.AppConfig{}) // 关掉

	if got := searcherPlatforms(); len(got) != 0 {
		t.Fatalf("关掉后该摘干净搜索器：%v", got)
	}
	for _, p := range []string{"mg", "bq", "bi"} {
		if pool.Supports(p) {
			t.Fatalf("关掉后 %s 解析器该摘掉：%v", p, pool.Platforms())
		}
	}
	if (*procs)[0].stopped != 1 {
		t.Fatalf("关掉该停进程：stopped=%d", (*procs)[0].stopped)
	}
	// 原生不受影响
	if !pool.Supports("wy") || !pool.Supports("tx") {
		t.Fatalf("原生解析器不该被动：%v", pool.Platforms())
	}
}

func TestApplyEmptySourcesFallsBackToDefault(t *testing.T) {
	w, pool, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn(nil)) // 没写名单 = 默认

	if len((*procs)[0].cfg.Sources) != len(config.MusicDLDefaultSources) {
		t.Fatalf("空名单该回落到默认：%v", (*procs)[0].cfg.Sources)
	}
	for _, p := range config.MusicDLDefaultSources {
		if !pool.Supports(p) {
			t.Fatalf("默认平台 %s 该注册上：%v", p, pool.Platforms())
		}
	}
}

func TestApplyPortChangeRebuildsProcess(t *testing.T) {
	// 端口是 spawn 时定死的 → 改了必须重建，否则新端口上什么都没有
	w, _, procs := wiringHarness(t)
	cleanupSearchers(t)

	cfg := musicdlOn([]string{"mg"})
	w.Apply(cfg)
	cfg.MusicDLPort = 8912
	w.Apply(cfg)

	if len(*procs) != 2 {
		t.Fatalf("换端口该重建进程：造了 %d 个", len(*procs))
	}
	if (*procs)[1].cfg.Port != 8912 {
		t.Fatalf("新进程该用新端口，得到 %d", (*procs)[1].cfg.Port)
	}
}

func TestApplyStartsFailedProcessAgain(t *testing.T) {
	// sidecar 挂了之后，用户再保存一次配置（或重启应用）应该能重试 ——
	// 否则「装依赖时断网」就变成永久的了。
	w, _, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn([]string{"mg"}))
	(*procs)[0].mu.Lock()
	(*procs)[0].state = sidecar.StateFailed
	(*procs)[0].mu.Unlock()

	w.Apply(musicdlOn([]string{"mg"}))
	if (*procs)[0].started != 2 {
		t.Fatalf("failed 状态该允许重试：started=%d", (*procs)[0].started)
	}
}

func TestApplyReadyProcessIsNotRestarted(t *testing.T) {
	// 就绪之后反复 Apply 不能重启它 —— 重启就是断掉正在跑的解析
	w, _, procs := wiringHarness(t)
	cleanupSearchers(t)

	w.Apply(musicdlOn([]string{"mg"}))
	if (*procs)[0].State() != sidecar.StateReady {
		t.Fatalf("假进程该被标成 ready，得到 %s", (*procs)[0].State())
	}
	w.Apply(musicdlOn([]string{"mg"}))
	if (*procs)[0].started != 1 {
		t.Fatalf("ready 的进程不该被重启：started=%d", (*procs)[0].started)
	}
}

func TestStatusReportsState(t *testing.T) {
	w, _, _ := wiringHarness(t)
	cleanupSearchers(t)

	st := w.Status()
	if st["sidecar"] == nil {
		t.Fatalf("状态里必须有 sidecar 那一块：%+v", st)
	}
	inner, ok := st["sidecar"].(map[string]any)
	if !ok || inner["state"] != sidecar.StateOff {
		t.Fatalf("没开时该报 off：%+v", st)
	}

	w.Apply(musicdlOn([]string{"mg"}))
	st = w.Status()
	inner, _ = st["sidecar"].(map[string]any)
	if inner["state"] != sidecar.StateReady {
		t.Fatalf("开了之后该报 ready：%+v", st)
	}
	if src, _ := st["sources"].([]string); len(src) != 1 || src[0] != "mg" {
		t.Fatalf("状态里该带上启用的平台：%+v", st["sources"])
	}
}

func TestSidecarSourceDirFoundInRepo(t *testing.T) {
	// 开发机（backend/ 目录下跑测试）应该能顺着 ../sidecar/musicdl_service 找到源码。
	// 找不到也不该报错 —— 只说明这个构建没带 sidecar。
	dir := sidecarSourceDir()
	if dir == "" {
		t.Skip("这个环境里没有 sidecar 源码目录（例如从别处跑测试）")
	}
	if !strings.HasSuffix(dir, "musicdl_service") {
		t.Fatalf("该指向 musicdl_service 目录，得到 %s", dir)
	}
}

// 「全关」必须真的一个平台都不注册 —— 这是 v2.1.100 那个**真机 bug 的回归测试**。
//
// 当时 `MusicDLActiveSources()`（`pkg/config/config.go`）用 `len == 0` 判「没配过」，
// 把「用户在音源页把源全关掉」也当成「没配过」→ 悄悄回到默认源 mg：用户明明关了，
// 搜索里还有咪咕的结果。
//
// 配置层当时已有用例，但**这一层**（wiring 真的把注册摘掉）没有 —— 而它才是「生效」
// 的定义。上面那条 `TestApplyEmptySourcesFallsBackToDefault` 验的是 **nil**（从没配过
// → 用默认），与「显式全关」是两种意图，必须分开钉住。
func TestApplyAllSourcesOffRegistersNothing(t *testing.T) {
	w, pool, _ := wiringHarness(t)
	cleanupSearchers(t)

	// 前置条件：先开一个源，确认它**真的**注册上了 ——
	// 否则下面的断言可能只是「本来就没注册」而已。
	w.Apply(musicdlOn([]string{"mg"}))
	if !pool.Supports("mg") {
		t.Fatalf("前置条件不成立：mg 该已注册：%v", pool.Platforms())
	}

	// 显式全关（非 nil 的空切片）→ 必须一个都不剩。
	w.Apply(musicdlOn([]string{}))
	// ⚠️ 只查**外挂平台**：解析池里本来就有原生解析器（wy/tx），全关外挂源不会
	// 也不该把它们摘掉 —— 拿 `pool.Platforms()` 判空是错的（第一版就错在这里）。
	if pool.Supports("mg") {
		t.Fatalf("⚠️ 用户把源全关掉了，mg 不该还在解析池里：%v", pool.Platforms())
	}
	if got := searcherPlatforms(); len(got) != 0 {
		t.Fatalf("⚠️ 用户把源全关掉了，外挂搜索器不该还注册着：%v", got)
	}
}
