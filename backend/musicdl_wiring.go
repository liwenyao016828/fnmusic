package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	// preview 是「试运行」的临时态（见 StartPreview）。nil = 没有在预览。
	preview *musicdlPreview
}

// previewTTL 是试运行的存活时长：TTL 内没保存就把临时态收回去。
//
// 与参考实现 fnme-z 的 `PREVIEW_TTL = 300.0` 同值（5 分钟）—— 那是「用户点选
// 一个源、试一下、要么保存要么放弃」的合理窗口。是**变量**不是常量：用例要把它
// 调小（否则一条回收用例要干等 5 分钟）。
var previewTTL = 5 * time.Minute

// musicdlPreview 是一次试运行的状态。
//
// # 为什么要把「预览态」单独放一份，而不是塞进 w.mgr / w.sources
//
// 那两样东西的定义是「**当前配置**产生的东西」：`w.sources` 是注册进解析池 +
// 搜索表的平台集合，`w.mgr` 是配置驱动的那个 sidecar 进程。试运行的要求恰好相反 ——
// **不能写进配置、不能影响正在搜索的结果**。混在一起就会出现「预览完一个源之后
// 它悄悄留在搜索里」这种最难查的脏状态。所以：预览只可能额外起**一个自己拥有的**
// 进程（proc 非 nil），且**一个解析器/搜索器都不注册**。
type musicdlPreview struct {
	// source 是正在被试运行的源短码。
	source string
	// until 是回收时刻。
	until time.Time
	// proc/client 只在「正式配置没开、试运行自己把 sidecar 拉起来」时非 nil。
	// 正式进程（w.mgr）在跑时复用那一个，这里保持 nil —— 于是回收时**不会**
	// 去动正式启用的东西。
	proc   sidecarProcess
	client *online.MusicDL
	// timer 到点触发回收（不是靠轮询：没有请求进来时也要能收回去）。
	timer *time.Timer
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

	// 一次保存 = 试运行的终点：要么**转正**（保存的名单里有它，正式路径接管），
	// 要么**清退**（用户没要它，临时进程立刻停掉）。与参考实现
	// `preview_reconcile_after_save()` 同一口径 —— 否则「试运行着、顺手保存了
	// 别的设置」会让一个没被启用的源一直挂着进程。
	w.reconcilePreviewLocked(cfg)

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
	// 试运行的进程也是子进程，不主动收同样会成孤儿。
	w.reapPreviewLocked("应用退出")
	w.teardownLocked()
}

// ── 试运行（预览一个未启用的源）─────────────────────────────────────────
//
// # 它解决什么
//
// musicdl 有 56 个源，而「注册了」不代表「能用」（真机实测一大半在国内出不了
// 结果）。此前要判断一个源行不行，只能先把它**真的启用**（=写进配置、进搜索与
// 解析池），不合适再关掉 —— 而启用一个坏源是有代价的：每次搜索都要为它等满
// sidecar 的单源预算。这里给一个「先试、不生效」的窗口。
//
// # 与「正式启用」的区别（这是全部要点）
//
//	                正式启用            试运行
//	写进配置         是                  **否**
//	解析器/搜索器     注册                **一个都不注册**
//	影响搜索结果      是                  **否**
//	生命周期         用户开关决定         **TTL 到点自动回收**（默认 5 分钟）
//
// # 与参考实现的差别（如实记下来）
//
// fnme-z 里「源」= 一个独立的 supervisor 程序（musicdl / musicbox / lxmusic
// 各一个进程），所以 `/api/preview` 的动作就是「启动那个进程」。曲率这边所有
// musicdl 平台**共用一个 sidecar 进程**，且 probe/search 的入参自带源名单
// （不依赖进程的启用名单）—— 所以「临时拉起一个源」在这里的含义是：
//
//  1. **进程没在跑时**（总开关关着 / sidecar 挂了）：按需把它拉起来，
//     源名单就是这一个源，**不注册**；
//  2. 进程已经在跑时：复用那一个（不去重启它 —— 重启会掐断正在解析的请求，
//     那才是「影响正在搜索」）。
//
// 两种情况都不进搜索，所以「试运行期间搜索里不出现它」是**结构保证**，不是靠
// 记得摘注册。用例 `TestPreviewDoesNotLeakIntoSearch` 钉着这一条。

// StartPreview 开始（或续期）一个源的试运行。返回给接口的状态快照。
func (w *musicdlWiring) StartPreview(source string) (map[string]any, error) {
	src := strings.ToLower(strings.TrimSpace(source))
	if src == "" {
		return nil, errors.New("source 不能为空")
	}
	if !isSourceCode(src) {
		return nil, fmt.Errorf("不像是源短码：%q（字母/数字/下划线/短横线，≤32 位）", source)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// 已经在试运行同一个源 → 续期（用户又点了一下，说明还在看）。
	if w.preview != nil && w.preview.source == src {
		w.renewPreviewLocked()
		return w.previewStatusLocked(), nil
	}

	// 已正式启用：进程本来就常驻，不需要试运行（与参考实现同一口径：
	// 它也会回一句「该音源已启用，进程常驻」而不是再起一个）。
	if w.isRegisteredLocked(src) {
		return map[string]any{
			"active": false, "preview": false, "source": src,
			"note": "该源已启用，进程常驻（直接用它即可，无需试运行）",
		}, nil
	}

	// 换一个源试运行：把上一个先收回去（界面上一次只点一个，同时留两个
	// 预览进程没有意义，还会让「谁该被回收」变得含糊）。
	w.reapPreviewLocked("换源试运行")

	w.preview = &musicdlPreview{source: src}
	if err := w.ensurePreviewProcessLocked(); err != nil {
		// 进程起不来（比如这个构建没带 sidecar 源码）→ 不留半截状态。
		w.preview = nil
		return nil, err
	}
	w.renewPreviewLocked()
	w.logf("[MUSICDL] 试运行源 %s（%s 后未保存自动回收；不会写进配置、不进搜索）",
		src, previewTTL)
	return w.previewStatusLocked(), nil
}

// PreviewStatus 返回试运行的当前状态（接口的 GET 用）。
func (w *musicdlWiring) PreviewStatus() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.previewStatusLocked()
}

// StopPreview 手动结束试运行（界面上的「停止」）。
func (w *musicdlWiring) StopPreview() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	src := ""
	if w.preview != nil {
		src = w.preview.source
	}
	w.reapPreviewLocked("手动停止")
	out := w.previewStatusLocked()
	out["stopped"] = src
	return out
}

// ensurePreviewProcessLocked 保证「试运行期间有一个能问的 sidecar」。
//
// 正式进程在跑就直接用它；否则按试运行的那个源拉一个**自己拥有的**进程
// （不注册、不碰 w.mgr / w.sources）。调用方必须持锁。
func (w *musicdlWiring) ensurePreviewProcessLocked() error {
	if w.preview == nil || w.mgr != nil {
		return nil
	}
	dir := sidecarSourceDir()
	if dir == "" {
		return errors.New("没找到 sidecar 源码目录（应该在可执行文件旁的 sidecar/musicdl_service）")
	}
	port := w.port
	if port <= 0 {
		port = config.MusicDLDefaultPort
	}
	proc := w.newProc(sidecar.Config{
		Dir:        dir,
		DataDir:    filepath.Join(w.dataDir, "sidecar"),
		Port:       port,
		Sources:    []string{w.preview.source},
		Limit:      w.limit,
		PythonPath: w.python,
		Logf:       w.logf,
	})
	w.preview.proc = proc
	w.preview.client = online.NewMusicDL(proc.Base(), w.logf)
	proc.Start(context.Background())
	return nil
}

// renewPreviewLocked 重设回收时刻并重新起表。调用方必须持锁。
func (w *musicdlWiring) renewPreviewLocked() {
	if w.preview == nil {
		return
	}
	if w.preview.timer != nil {
		w.preview.timer.Stop()
	}
	w.preview.until = time.Now().Add(previewTTL)
	w.preview.timer = time.AfterFunc(previewTTL, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.reapPreviewLocked("TTL 到点")
	})
}

// reapPreviewLocked 把试运行**彻底**收回去：定时器、自己拉起的进程、状态。
//
// ⚠️ 只停 `preview.proc`（试运行自己起的那个）。正式进程（w.mgr）以及它注册的
// 解析器/搜索器**一个都不动** —— 试运行不拥有它们。调用方必须持锁。
func (w *musicdlWiring) reapPreviewLocked(reason string) {
	if w.preview == nil {
		return
	}
	src := w.preview.source
	if w.preview.timer != nil {
		w.preview.timer.Stop()
	}
	if w.preview.proc != nil {
		w.preview.proc.Stop()
	}
	w.preview = nil
	w.logf("[MUSICDL] 试运行 %s 已回收（%s）", src, reason)
}

// reconcilePreviewLocked 配置保存后的收尾：转正（名单里有它）或清退。
//
// 两种都先收回临时态：转正时正式路径会在下面自己把进程建起来（**必须**先停掉
// 试运行那个 —— 它占着同一个端口），清退时本来就该停。调用方必须持锁。
func (w *musicdlWiring) reconcilePreviewLocked(cfg config.AppConfig) {
	if w.preview == nil {
		return
	}
	src := w.preview.source
	if cfg.MusicDLOn() && containsString(cfg.MusicDLActiveSources(), src) {
		w.logf("[MUSICDL] 试运行的源 %s 已随保存转正，由正式配置接管", src)
		w.reapPreviewLocked("已转正")
		return
	}
	w.reapPreviewLocked("保存配置时未启用")
}

// previewStatusLocked 组装试运行的状态快照。调用方必须持锁。
func (w *musicdlWiring) previewStatusLocked() map[string]any {
	out := map[string]any{
		"active": false,
		"ttl":    int(previewTTL / time.Second),
	}
	if w.preview == nil {
		return out
	}
	left := int(time.Until(w.preview.until) / time.Second)
	if left < 0 {
		left = 0
	}
	out["active"] = true
	out["preview"] = true
	out["source"] = w.preview.source
	out["seconds_left"] = left
	// 「进程起来没」：试运行自己起的报它的状态，复用正式进程时报正式那个。
	// 两者都取不到时不给 sidecar 那一段（前端据此显示「还没有进程」）。
	proc := w.preview.proc
	if proc == nil {
		proc = w.mgr
	}
	if proc != nil {
		st := proc.Status()
		out["sidecar"] = st
		out["state"] = st["state"]
	}
	out["own_process"] = w.preview.proc != nil
	return out
}

// isRegisteredLocked 报告某个源是否在「正式启用的注册集合」里。调用方必须持锁。
func (w *musicdlWiring) isRegisteredLocked(src string) bool {
	return containsString(w.sources, src)
}

// containsString 是小写敏感的包含判断：调用方传进来的短码都已归一成小写。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// isSourceCode 判断字符串像不像一个源短码。
//
// 真正的白名单在 sidecar（musicdl 的注册表，56 个源且上游还在加），这里只挡住
// 明显不是短码的输入（空、中文提示词、路径、超长串）—— 试运行会**起进程**，
// 不该拿一个明显不对的值去起。
func isSourceCode(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
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
	// 试运行那一段也带上：界面刷新状态时一次请求就能同时看到「正式」与「试运行」，
	// 不用为了一个倒计时再打一次接口。
	out["preview"] = w.previewStatusLocked()
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
