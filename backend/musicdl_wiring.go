package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/sidecar"
)

// musicdlWiring 把「配置 → sidecar 进程 → 解析器/搜索器注册」收在一处。
//
// # 为什么要单独一个文件
//
// 这三件事是**联动**的，散在 main.go 里就会出现「关了开关但解析器还挂着」这种
// 半开状态。收在一个类型里，`Apply(cfg)` 是唯一的入口，幂等 —— 开机调一次、
// 配置一变再调一次，中间不用管它现在是什么状态。
//
// # 它保证的两条不变量
//
//  1. **配置关着 = 什么都没挂**：解析池里没有 musicdl 解析器、搜索表里没有
//     musicdl 搜索器、进程也不在。用户关掉之后不该还留着一半能力。
//  2. **配置开着但 sidecar 没起来 = 曲率照常工作**：注册是立刻做的（不健康也注册），
//     失败时解析返回空、搜索退回原生 —— 少几个平台，不会把整次搜索弄坏。
//
// sidecarProcess 是 wiring 用到的那部分 sidecar 能力。
//
// 为什么要一层接口：**测试里绝不能真去起进程** —— `sidecar.Start` 首次会建 venv
// 并 pip install（一两分钟 + 一两百 MB 下载）。有了这个缝，用例能注入一个假进程，
// 从而把「注册/摘注册/换平台/关掉」这些**判据**测干净，而不碰网络。
type sidecarProcess interface {
	Base() string
	State() string
	Status() map[string]any
	Start(ctx context.Context)
	Stop()
}

type musicdlWiring struct {
	pool    *online.Pool
	dataDir string
	logf    func(format string, args ...any)
	// newProc 造进程（默认 sidecar.New）。测试替换它。
	newProc func(sidecar.Config) sidecarProcess

	mu      sync.Mutex
	mgr     sidecarProcess
	client  *online.MusicDL
	sources []string
	port    int
	python  string
	limit   int
	lastErr string
}

func newMusicDLWiring(pool *online.Pool, dataDir string, logf func(string, ...any)) *musicdlWiring {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &musicdlWiring{
		pool:    pool,
		dataDir: dataDir,
		logf:    logf,
		newProc: func(cfg sidecar.Config) sidecarProcess { return sidecar.New(cfg) },
	}
}

// sidecarSourceDir 找 sidecar 的 Python 源码目录。
//
// 生产上它在可执行文件旁边（`/var/apps/yinshu-ai/target/sidecar/musicdl_service`，
// 由 fpk 的 `app/sidecar/` 解出来）；开发时在仓库根目录下的 `sidecar/musicdl_service`。
// 三处都不在就返回空串 —— 调用方据此跳过（**不报错**：源码目录不在只说明这个
// 构建没带 sidecar，不是故障）。
func sidecarSourceDir() string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(base, "sidecar", "musicdl_service"),
			filepath.Join(base, "..", "sidecar", "musicdl_service"),
		)
	}
	cands = append(cands,
		filepath.Join("sidecar", "musicdl_service"),
		filepath.Join("..", "sidecar", "musicdl_service"),
	)
	for _, dir := range cands {
		if _, err := os.Stat(filepath.Join(dir, "app.py")); err == nil {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return dir
			}
			return abs
		}
	}
	return ""
}

// Apply 按配置把 sidecar 起/停/换平台。**幂等**，可以从任意状态调到任意状态。
func (w *musicdlWiring) Apply(cfg config.AppConfig) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !cfg.MusicDLOn() {
		if w.mgr != nil {
			w.logf("[MUSICDL] 开关关闭，停掉 sidecar 并摘掉注册")
		}
		w.teardownLocked()
		return
	}

	dir := sidecarSourceDir()
	if dir == "" {
		w.lastErr = "没找到 sidecar 源码目录（应该在可执行文件旁的 sidecar/musicdl_service）"
		w.logf("[MUSICDL] %s，跳过", w.lastErr)
		return
	}

	sources := cfg.MusicDLActiveSources()
	port := cfg.MusicDLEffectivePort()
	python := cfg.MusicDLPython
	limit := cfg.MusicDLEffectiveLimit()

	// 任何一项变了都要**重建**：这几个值都是 spawn 时通过环境变量 / 参数定死的，
	// 进程跑起来之后改不了。
	if w.mgr != nil && (!sameStrings(w.sources, sources) || w.port != port ||
		w.python != python || w.limit != limit) {
		w.logf("[MUSICDL] 配置变了（平台/端口/解释器/条数），重建 sidecar")
		w.teardownLocked()
	}

	if w.mgr == nil {
		w.mgr = w.newProc(sidecar.Config{
			Dir:        dir,
			DataDir:    filepath.Join(w.dataDir, "sidecar"),
			Port:       port,
			Sources:    sources,
			Limit:      limit,
			PythonPath: python,
			Logf:       w.logf,
		})
		w.client = online.NewMusicDL(w.mgr.Base(), w.logf)
		w.sources, w.port, w.python, w.limit = sources, port, python, limit
		// 立刻注册（**不等就绪**）：注册是幂等的、失败时自然降级；
		// 等就绪再注册的话，用户在「正在装依赖」那两分钟里搜不到新平台，
		// 而那是他自己刚打开的开关。
		w.registerLocked()
		w.logf("[MUSICDL] 启用平台 %s（sidecar %s）", strings.Join(sources, ","), w.mgr.Base())
	}

	switch w.mgr.State() {
	case sidecar.StateOff, sidecar.StateFailed, sidecar.StateStopped:
		w.mgr.Start(context.Background())
	}
}

// Stop 关机时调：停进程 + 摘注册。
func (w *musicdlWiring) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.teardownLocked()
}

// teardownLocked 停进程 + 摘掉所有注册。调用方必须持锁。
func (w *musicdlWiring) teardownLocked() {
	if w.mgr != nil {
		w.mgr.Stop()
		w.mgr = nil
	}
	w.unregisterLocked()
	w.client = nil
	w.sources = nil
}

func (w *musicdlWiring) registerLocked() {
	if w.client == nil {
		return
	}
	for _, plat := range w.sources {
		// 解析器：每个平台一个薄壳，共用同一个 sidecar 客户端
		w.pool.Register(w.client.Resolver(plat))
	}
	// 搜索器：**必须与解析器覆盖同一批平台** —— 搜索给出的 id 要能被解析认出来
	// （musicdl 的平台内 id 只有它自己认识），一半原生一半外挂就是「搜得到、放不出」。
	search.NewMusicDLSearcher(w.client, w.limit).RegisterPlatforms(w.sources)
}

func (w *musicdlWiring) unregisterLocked() {
	for _, plat := range w.sources {
		w.pool.Unregister(plat)
		search.UnregisterSearcher(plat)
	}
}

// Client 返回当前的 sidecar 客户端（nil = 没开或还没建）。
//
// 是**方法**而不是字段：客户端会在配置变化时被重建（换平台/换端口），
// 调用方每次都要现取，抓一份留着就会指向旧的那个。
func (w *musicdlWiring) Client() *online.MusicDL {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.client
}

// Status 是给接口 / 界面看的快照。
func (w *musicdlWiring) Status() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := map[string]any{
		"available": sidecarSourceDir() != "",
		"sources":   append([]string(nil), w.sources...),
	}
	if w.mgr != nil {
		out["sidecar"] = w.mgr.Status()
	} else {
		out["sidecar"] = map[string]any{"state": sidecar.StateOff}
	}
	if w.lastErr != "" {
		out["error"] = w.lastErr
	}
	return out
}

// dedupePlatforms 去重并丢掉空串、统一小写。
//
// 平台列表是「内置默认 ∪ 启用的外挂平台」拼出来的，重复项会让同一次搜索把同一个
// 平台打两遍（多一轮上游请求，还可能让同一个平台的曲目出现两次）。
func dedupePlatforms(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
