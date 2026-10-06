package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/sidecar"
)

// 试运行（`musicdl_wiring.go` 的 StartPreview / PreviewStatus / StopPreview）的用例。
//
// 这一层最容易出的问题是**预览态泄漏**：临时拉起的源悄悄留在了配置里、留在了
// 搜索里、或者 TTL 到点进程没停。所以每条用例都在钉「预览**没有**动到什么」，
// 而不只是「预览起来了」。

// starts / stops 是 fakeProc 那两个计数的**加锁**读法。
//
// 试运行的回收跑在 `time.AfterFunc` 的 goroutine 里，会在用例断言的同时写这两个
// 字段 —— 直接读字段就是数据竞争（`-race` 会红，而且它掩盖的是真问题：那一刻
// 确实有两个 goroutine 在碰同一块内存）。既有的接线用例没有并发写，所以没暴露。
func (f *fakeProc) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeProc) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// previewHarness 与 wiringHarness 相同，只是把 TTL 调短（真值 5 分钟，用例里干等不值当）。
func previewHarness(t *testing.T, ttl time.Duration) (*musicdlWiring, *online.Pool, *[]*fakeProc) {
	t.Helper()
	old := previewTTL
	previewTTL = ttl
	t.Cleanup(func() { previewTTL = old })
	return wiringHarness(t)
}

func TestPreviewDoesNotLeakIntoSearch(t *testing.T) {
	// 「预览期间搜索里不出现它」——这条是试运行**能不能存在**的前提：
	// 一个没被启用的源绝不该因为「有人点了试运行」而进入搜索结果。
	w, pool, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)

	// 总开关关着（＝这个源绝对没被正式启用）
	w.Apply(config.AppConfig{})
	if len(*procs) != 0 {
		t.Fatalf("前置条件：关着的时候不该有进程，得到 %d 个", len(*procs))
	}

	st, err := w.StartPreview("bq")
	if err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	if st["active"] != true || st["source"] != "bq" {
		t.Fatalf("状态该说清在试运行哪个源：%+v", st)
	}
	if st["own_process"] != true {
		t.Fatalf("关着的时候，试运行该自己把进程拉起来：%+v", st)
	}
	if len(*procs) != 1 || (*procs)[0].starts() != 1 {
		t.Fatalf("该起 1 个试运行进程：%d 个 / started=%d", len(*procs), (*procs)[0].starts())
	}

	// ⚠️ 下面四条是「不影响正在搜索的结果」的判据：
	// 解析池、外挂搜索器、状态里的启用名单、真正走一次搜索。
	if pool.Supports("bq") {
		t.Fatalf("试运行不该把 %s 注册进解析池：%v", "bq", pool.Platforms())
	}
	if got := searcherPlatforms(); len(got) != 0 {
		t.Fatalf("试运行不该注册外挂搜索器：%v", got)
	}
	if src, _ := w.Status()["sources"].([]string); len(src) != 0 {
		t.Fatalf("状态里的启用名单不该有它：%v", src)
	}
	if got := search.Search("海阔天空", "bq", 1, 5); len(got) != 0 {
		t.Fatalf("试运行期间搜索 %s 不该出结果，得到 %d 条", "bq", len(got))
	}
}

func TestPreviewDoesNotWriteConfig(t *testing.T) {
	// 「预览的源不能被写进配置」——配置文件在上面跑完试运行之后必须一个字节都没变。
	dir := t.TempDir()
	cm, err := config.NewConfigManager(dir, 8898)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	on := false
	if err := cm.Update(config.AppConfig{MusicDLEnabled: &on, MusicDLSources: []string{"mg"}}); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	path := filepath.Join(dir, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读配置失败：%v", err)
	}

	w, _, _ := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(cm.Get())
	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	if _, err := w.StartPreview("bi"); err != nil { // 换一个源试
		t.Fatalf("换源试运行该成功：%v", err)
	}
	w.StopPreview()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("再读配置失败：%v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("试运行改了配置文件：\nbefore %s\nafter  %s", before, after)
	}
	got := cm.Get()
	if got.MusicDLOn() {
		t.Fatal("试运行不该把总开关打开")
	}
	if src := got.MusicDLActiveSources(); strings.Contains(strings.Join(src, ","), "bq") ||
		strings.Contains(strings.Join(src, ","), "bi") {
		t.Fatalf("试运行不该改动启用名单：%v", src)
	}
}

func TestPreviewTTLReapsOwnProcessAndState(t *testing.T) {
	// 「TTL 到点之后状态回干净」——进程、状态、定时器都要收，一样都不能留。
	w, pool, procs := previewHarness(t, 120*time.Millisecond)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	st, err := w.StartPreview("bq")
	if err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	if st["active"] != true || st["source"] != "bq" {
		t.Fatalf("该报在试运行 bq：%+v", st)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if (*procs)[0].stops() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if (*procs)[0].stops() != 1 {
		t.Fatalf("TTL 到点该把试运行自己拉起的进程停掉：stopped=%d", (*procs)[0].stops())
	}

	after := w.PreviewStatus()
	if after["active"] != false {
		t.Fatalf("TTL 到点后状态该回干净：%+v", after)
	}
	if _, has := after["source"]; has {
		t.Fatalf("回收后不该还留着源：%+v", after)
	}
	if src, _ := w.Status()["sources"].([]string); len(src) != 0 {
		t.Fatalf("回收后启用名单仍该是空的：%v", src)
	}
	if pool.Supports("bq") {
		t.Fatalf("回收后不该有解析器：%v", pool.Platforms())
	}
	if got := searcherPlatforms(); len(got) != 0 {
		t.Fatalf("回收后不该有外挂搜索器：%v", got)
	}
}

func TestPreviewTTLDoesNotTouchEnabledThings(t *testing.T) {
	// 总开关开着时试运行一个**别的**源：进程复用正式那个，回收时**不许动它**。
	// （「回收干净」的反面同样是 bug：把正式启用的源一起收掉了。）
	w, pool, procs := previewHarness(t, 120*time.Millisecond)
	cleanupSearchers(t)
	w.Apply(musicdlOn([]string{"mg"}))
	if !pool.Supports("mg") {
		t.Fatal("前置条件：mg 该已注册")
	}

	st, err := w.StartPreview("bq")
	if err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	if st["own_process"] != false {
		t.Fatalf("正式进程在跑时该复用它，不该另起：%+v", st)
	}
	if len(*procs) != 1 {
		t.Fatalf("不该为试运行多造进程：%d 个", len(*procs))
	}
	// 试运行期间 mg 照常在（它才是正式启用的那个），bq 一个注册都没有。
	if !pool.Supports("mg") {
		t.Fatalf("mg 不该被动：%v", pool.Platforms())
	}
	if pool.Supports("bq") {
		t.Fatalf("试运行的 bq 不该注册进解析池：%v", pool.Platforms())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && w.PreviewStatus()["active"] == true {
		time.Sleep(10 * time.Millisecond)
	}
	if w.PreviewStatus()["active"] != false {
		t.Fatal("TTL 到点该结束试运行")
	}
	if !pool.Supports("mg") || (*procs)[0].stops() != 0 {
		t.Fatalf("回收试运行不该动到正式启用的源：pool=%v stopped=%d",
			pool.Platforms(), (*procs)[0].stops())
	}
	if (*procs)[0].starts() != 1 {
		t.Fatalf("正式进程不该被重启：started=%d", (*procs)[0].starts())
	}
}

func TestPreviewReconcilePromotesOnSave(t *testing.T) {
	// 试运行期间保存了「启用这个源」→ 转正：临时进程收掉，正式路径接管。
	w, pool, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	previewProc := (*procs)[0]

	w.Apply(musicdlOn([]string{"bq"})) // 用户点了开关并保存

	if previewProc.stops() != 1 {
		t.Fatalf("转正该把试运行进程收掉（否则两个进程抢同一个端口）：stopped=%d", previewProc.stops())
	}
	if w.PreviewStatus()["active"] != false {
		t.Fatalf("转正之后就不该再有试运行态：%+v", w.PreviewStatus())
	}
	if !pool.Supports("bq") {
		t.Fatalf("转正后该由正式路径注册解析器：%v", pool.Platforms())
	}
	if got := searcherPlatforms(); len(got) != 1 || got[0] != "bq" {
		t.Fatalf("转正后该由正式路径注册搜索器：%v", got)
	}
	if len(*procs) != 2 || (*procs)[1].starts() != 1 {
		t.Fatalf("转正该按配置起一个新进程：%d 个", len(*procs))
	}
}

func TestPreviewReconcileDropsOnUnrelatedSave(t *testing.T) {
	// 试运行期间保存了**别的**设置（名单里没有它）→ 立刻清退。
	w, pool, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}

	w.Apply(config.AppConfig{Theme: "fresh-mint"}) // 一次不相干的保存

	if (*procs)[0].stops() != 1 {
		t.Fatalf("保存配置时未启用它，试运行进程该被停掉：stopped=%d", (*procs)[0].stops())
	}
	if w.PreviewStatus()["active"] != false {
		t.Fatalf("保存后不该留着试运行态：%+v", w.PreviewStatus())
	}
	if pool.Supports("bq") {
		t.Fatalf("试运行的源不该因为保存而注册：%v", pool.Platforms())
	}
}

func TestPreviewOfEnabledSourceIsNoop(t *testing.T) {
	w, pool, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(musicdlOn([]string{"mg"}))

	st, err := w.StartPreview("mg")
	if err != nil {
		t.Fatalf("已启用的源该回一句说明而不是报错：%v", err)
	}
	if st["active"] == true || st["preview"] == true {
		t.Fatalf("已启用的源不需要试运行：%+v", st)
	}
	if note, _ := st["note"].(string); !strings.Contains(note, "已启用") {
		t.Fatalf("该说清为什么没起预览：%+v", st)
	}
	if len(*procs) != 1 || (*procs)[0].starts() != 1 {
		t.Fatalf("不该多造/重启进程：%d 个", len(*procs))
	}
	if !pool.Supports("mg") {
		t.Fatal("正式注册不该被动")
	}
	if w.PreviewStatus()["active"] != false {
		t.Fatal("不该留下试运行态")
	}
}

func TestPreviewRejectsJunkSource(t *testing.T) {
	w, _, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	for _, bad := range []string{"", "   ", "咪咕音乐", "a/b", strings.Repeat("x", 40)} {
		if _, err := w.StartPreview(bad); err == nil {
			t.Fatalf("试运行会起进程，明显不是短码的值该被拒：%q", bad)
		}
	}
	if len(*procs) != 0 {
		t.Fatalf("被拒的请求不该起进程：%d 个", len(*procs))
	}
	if w.PreviewStatus()["active"] != false {
		t.Fatal("被拒的请求不该留下试运行态")
	}
}

func TestPreviewRenewExtendsDeadline(t *testing.T) {
	w, _, procs := previewHarness(t, 400*time.Millisecond)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	first, err := w.StartPreview("bq")
	if err != nil {
		t.Fatalf("试运行该成功：%v", err)
	}
	time.Sleep(250 * time.Millisecond)
	again, err := w.StartPreview("bq") // 用户又点了一下 → 续期
	if err != nil {
		t.Fatalf("续期不该报错：%v", err)
	}
	if again["seconds_left"].(int) <= first["seconds_left"].(int)-1 {
		t.Fatalf("同一源再点一次该续期：first=%v again=%v", first["seconds_left"], again["seconds_left"])
	}
	if len(*procs) != 1 || (*procs)[0].starts() != 1 {
		t.Fatalf("续期不该重启进程：%d 个", len(*procs))
	}
	// 从续期那一刻起不到 TTL，所以这时还在试运行中
	if w.PreviewStatus()["active"] != true {
		t.Fatalf("续期之后该仍在试运行：%+v", w.PreviewStatus())
	}
}

func TestPreviewSwitchesSource(t *testing.T) {
	// 换一个源试运行：上一个必须收掉（同时留两个预览进程会让「谁该回收」含糊）。
	w, _, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatal(err)
	}
	st, err := w.StartPreview("bi")
	if err != nil {
		t.Fatalf("换源试运行该成功：%v", err)
	}
	if st["source"] != "bi" || st["active"] != true {
		t.Fatalf("该切到新源：%+v", st)
	}
	if (*procs)[0].stops() != 1 {
		t.Fatalf("旧源的试运行进程该被收掉：stopped=%d", (*procs)[0].stops())
	}
	if len(*procs) != 2 || (*procs)[1].starts() != 1 {
		t.Fatalf("该为新源起一个进程：%d 个", len(*procs))
	}
}

func TestPreviewStopManualClearsEverything(t *testing.T) {
	w, pool, procs := previewHarness(t, time.Hour) // TTL 很长：只能是手动停掉的
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatal(err)
	}
	st := w.StopPreview()

	if st["active"] != false {
		t.Fatalf("手动停之后该回干净：%+v", st)
	}
	if st["stopped"] != "bq" {
		t.Fatalf("该告诉界面停掉的是哪个源：%+v", st)
	}
	if (*procs)[0].stops() != 1 {
		t.Fatalf("该停进程：stopped=%d", (*procs)[0].stops())
	}
	if pool.Supports("bq") || len(searcherPlatforms()) != 0 {
		t.Fatal("手动停之后不该有注册残留")
	}
	// TTL 定时器必须在 stop 时就停掉，否则一小时后又来一次「回收」（此时
	// 预览表已空，是空操作，但那是靠巧合而不是靠设计）。
	if w.PreviewStatus()["active"] != false {
		t.Fatal("不该还留着试运行态")
	}
}

func TestStatusCarriesPreviewBlock(t *testing.T) {
	// 界面刷新状态时一次请求就能同时看到「正式」与「试运行」，不用为倒计时再打一次。
	w, _, procs := previewHarness(t, time.Minute)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})

	blk, ok := w.Status()["preview"].(map[string]any)
	if !ok {
		t.Fatalf("状态里该有 preview 那一段：%+v", w.Status())
	}
	if blk["active"] != false {
		t.Fatalf("没试运行时该报 inactive：%+v", blk)
	}
	if blk["ttl"] != int(time.Minute/time.Second) {
		t.Fatalf("该把 TTL 一并告诉界面（用于展示倒计时）：%+v", blk)
	}

	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatal(err)
	}
	blk, _ = w.Status()["preview"].(map[string]any)
	if blk["active"] != true || blk["source"] != "bq" {
		t.Fatalf("试运行中该如实报：%+v", blk)
	}
	if blk["state"] != sidecar.StateReady {
		t.Fatalf("该带上进程状态（界面据此说「起来了没」）：%+v", blk)
	}
	if len(*procs) != 1 {
		t.Fatalf("该只有试运行那一个进程：%d", len(*procs))
	}
}

func TestStopReapsPreviewProcess(t *testing.T) {
	// 应用退出时试运行进程也是子进程，不主动收就成孤儿。
	w, _, procs := previewHarness(t, time.Hour)
	cleanupSearchers(t)
	w.Apply(config.AppConfig{})
	if _, err := w.StartPreview("bq"); err != nil {
		t.Fatal(err)
	}

	w.Stop()

	if (*procs)[0].stops() != 1 {
		t.Fatalf("退出该把试运行进程收掉：stopped=%d", (*procs)[0].stops())
	}
}
