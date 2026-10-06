package intercept

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 「每日推荐的大模型层」（见 vdaily_llm.go）的用例。
//
// 这一层有四条硬约束，用例逐条盯：
//  1. **开关关着 / 没注入 / 没配置 / 超时 / 空回复 → 这一层整个不存在**，
//     每日推荐照常交在线 + 本地两层，不多不少 20 首；
//  2. **模型的文字不可信** —— 必须走搜索 + `sameTitle` + 可播过滤，不能直接进列表；
//  3. **发出去的只有歌名 / 歌手 / 专辑**，没有时间戳、平台、id；
//  4. **不许每次请求都打模型**（按 (用户, 当天) 缓存）。

// ── 装具 ────────────────────────────────────────────────────────────────

// llmFixture 在既有 harness 上装一个「按关键词给结果」的搜索替身，
// 和一个可编程的大模型替身。
type llmFixture struct {
	*harness

	mu        sync.Mutex
	byKeyword map[string][]search.UnifiedSong
	calls     int
	reqs      []ai.RecommendRequest
	reply     []ai.RecommendCandidate
	block     chan struct{}
	switchOn  bool
}

func newLLMFixture(t *testing.T) *llmFixture {
	t.Helper()
	f := &llmFixture{harness: newHarness(t), byKeyword: map[string][]search.UnifiedSong{}}

	// 搜索替身：按关键词返回。真实现里在线层与这一层都走同一个搜索入口，
	// 所以这里也必须让两边看到**同一份**结果 —— 否则验不出跨层去重。
	//
	// ⚠️ 改的是 `it.searcher` 而不是 `cfg.Searcher`：后者在 `New()` 里已经被取成
	// 字段了，构造之后再改 cfg 是不生效的。
	f.it.searcher = func(keyword, platform string, page, size int) []search.UnifiedSong {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]search.UnifiedSong(nil), f.byKeyword[strings.ToLower(strings.TrimSpace(keyword))]...)
	}

	// 大模型替身。开关与回复都**在锁里现取**：这一路跑在后台 goroutine 里，
	// 直接读测试字段会撞上数据竞争。
	f.it.cfg.RecommendLLM = func(ctx context.Context, req ai.RecommendRequest) []ai.RecommendCandidate {
		f.mu.Lock()
		f.calls++
		f.reqs = append(f.reqs, req)
		reply := append([]ai.RecommendCandidate(nil), f.reply...)
		block := f.block
		f.mu.Unlock()
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
			}
		}
		return reply
	}
	f.it.cfg.DailyLLMEnabled = func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.switchOn
	}

	// 收尾：等所有在途的后台任务跑完再结束用例。
	//
	// 不等的话，后台那一路会带着 `i.logf`（= t.Logf）活到用例结束之后 ——
	// 那是「测试已结束还往 t 里写日志」，race 检测器会报，也会污染后面的用例。
	t.Cleanup(f.waitBackground)
	return f
}

// holdBackground 先卡住模型这一路，返回「放行」函数。
func (f *llmFixture) holdBackground() func() {
	gate := make(chan struct{})
	f.mu.Lock()
	f.block = gate
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// blockUntilTimeout 让模型这一路一直等到它自己的超时（不返回）。
func (f *llmFixture) blockUntilTimeout() {
	f.mu.Lock()
	f.block = make(chan struct{}) // 永不关闭 → 只能等 ctx
	f.mu.Unlock()
}

// waitBackground 等当前所有在途的大模型后台任务收掉。
func (f *llmFixture) waitBackground() {
	f.it.llmMu.Lock()
	entries := make([]*llmEntry, 0, len(f.it.llmCache))
	for _, e := range f.it.llmCache {
		entries = append(entries, e)
	}
	f.it.llmMu.Unlock()
	for _, e := range entries {
		select {
		case <-e.done:
		case <-time.After(10 * time.Second):
			// 用例已经结束了，这里只能用标准错误报（不能用 t.Fatal）。
			fmt.Fprintln(os.Stderr, "大模型后台任务没收掉，用例会留下一个活 goroutine")
			return
		}
	}
}

// llmSong 造一条搜索结果。id 就是平台内 id —— 断言里要拿它证明
// 「列表里那条来自搜索，不是模型给的字符串」。
func llmSong(id, title, artist string) search.UnifiedSong {
	return search.UnifiedSong{
		ID: id, Songmid: id, Name: title, Singer: artist,
		Album: "专辑", Source: "wy", Duration: 240,
	}
}

// addKeyword 让某个关键词搜出这些曲目，并把这些曲目登记成「能解析出直链」。
//
// 不登记的话 `collectOnline` 的可播过滤会把它们全滤掉，用例就只能看到空结果。
func (f *llmFixture) addKeyword(t *testing.T, keyword string, songs ...search.UnifiedSong) {
	t.Helper()
	f.mu.Lock()
	f.byKeyword[strings.ToLower(strings.TrimSpace(keyword))] = append([]search.UnifiedSong(nil), songs...)
	playable := map[string]bool{}
	for _, list := range f.byKeyword {
		for _, s := range list {
			playable[s.Songmid] = true
		}
	}
	f.mu.Unlock()
	f.it.pool = poolFrom(playable)
}

// onlineSongs 造 n 首「在线层会搜到」的曲目（前缀区分批次）。
func onlineSongs(prefix string, n int) []search.UnifiedSong {
	out := make([]search.UnifiedSong, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, llmSong(fmt.Sprintf("%s%02d", prefix, i),
			fmt.Sprintf("%s曲目%02d", prefix, i), "在线歌手"))
	}
	return out
}

func (f *llmFixture) setSwitch(on bool) {
	f.mu.Lock()
	f.switchOn = on
	f.mu.Unlock()
}

func (f *llmFixture) setReply(cands ...ai.RecommendCandidate) {
	f.mu.Lock()
	f.reply = cands
	f.mu.Unlock()
}

func (f *llmFixture) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *llmFixture) requests() []ai.RecommendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.RecommendRequest(nil), f.reqs...)
}

// waitCalls 等到模型被调用 n 次（后台那一路是异步的）。
func (f *llmFixture) waitCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.callCount() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等模型被调用 %d 次超时（实际 %d）", n, f.callCount())
}

// daily 直接算一次当天的每日推荐（不走 vCache，所以能连算两次看缓存行为）。
func (f *llmFixture) daily(t *testing.T, now time.Time) []online.Track {
	t.Helper()
	r := mustRequest(t, "/music/api/v1/playlist/detail", "{}")
	return f.it.dailyPlaylist(context.Background(), r, now).Tracks
}

func (f *llmFixture) sharedUser(t *testing.T) string {
	t.Helper()
	return dailySharedUser(t, f.harness)
}

// llmBaseline 是多数用例的起点：用户听过「在线歌手」的一首歌（于是种子词里
// 有「在线歌手」），那个关键词能搜出 20 首在线曲目。
//
// 返回 fixture 与那个用户键。
func llmBaseline(t *testing.T) (*llmFixture, string) {
	t.Helper()
	f := newLLMFixture(t)
	user := f.sharedUser(t)

	// 听过一首「种子曲」：它会把「在线歌手」顶成第一个种子词。
	// ⚠️ 它自己会进排除集（最近播放窗口），所以在线池里不能再有它。
	recordPlay(t, f.harness, user, online.Track{
		Platform: "wy", PlatformID: "seed", Title: "种子曲", Artists: []string{"在线歌手"},
	}, time.Now().Unix())

	f.addKeyword(t, "在线歌手", onlineSongs("在线", 20)...)
	return f, user
}

// titles 把曲目折成歌名，便于断言「谁在、谁不在」。
func titles(ts []online.Track) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Title)
	}
	return out
}

func hasTitle(ts []online.Track, title string) bool {
	for _, t := range ts {
		if t.Title == title {
			return true
		}
	}
	return false
}

func countTitle(ts []online.Track, title string) int {
	n := 0
	for _, t := range ts {
		if t.Title == title {
			n++
		}
	}
	return n
}

// ── 1. 不在场时：行为与加这一层之前一致 ─────────────────────────────────

// TestDailyLLMAbsentByDefault 钉住「没打开开关 → 这一层不存在」。
//
// 这是最重要的一条：AI 相关的东西**未配置时必须静默降级**（项目既有约定），
// 而这一层比别的多一道门 —— 就算模型配好了、也注进来了，用户没同意就还是不发数据。
func TestDailyLLMAbsentByDefault(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	if q := f.it.llmQuota(); q != 0 {
		t.Fatalf("开关关着时配额该是 0，得到 %d", q)
	}

	got := f.daily(t, time.Now())
	if f.callCount() != 0 {
		t.Fatalf("开关关着时一次都不该打模型，实际 %d 次", f.callCount())
	}
	if hasTitle(got, "模型歌") {
		t.Fatal("开关关着时不该出现模型推荐的歌")
	}
	if len(got) != vDailySize {
		t.Fatalf("歌单该是 %d 首，得到 %d", vDailySize, len(got))
	}
}

// TestDailyLLMQuotaNeedsBothGates 钉住「两道门缺一不可」。
func TestDailyLLMQuotaNeedsBothGates(t *testing.T) {
	f, _ := llmBaseline(t)

	// 没注入实现 → 就算开关开着也不在。
	f.it.cfg.RecommendLLM = nil
	f.setSwitch(true)
	if q := f.it.llmQuota(); q != 0 {
		t.Fatalf("没注入实现时配额该是 0，得到 %d", q)
	}

	// 注入了但开关关着 → 还是不在。
	f = newLLMFixture(t)
	f.setSwitch(false)
	if q := f.it.llmQuota(); q != 0 {
		t.Fatalf("开关关着时配额该是 0，得到 %d", q)
	}

	// 两个都开 → 在。
	f.setSwitch(true)
	if q := f.it.llmQuota(); q != vDailyLLMQuota {
		t.Fatalf("两道门都开时配额该是 %d，得到 %d", vDailyLLMQuota, q)
	}
}

// ── 2. 在场时：模型那一层真的在贡献 ─────────────────────────────────────

// TestDailyLLMUsesReservedQuota 钉住「预留名额」这条设计（而不是「只补空位」）。
//
// 在线层能自己填满 20 首 —— 照参考实现那样「只补空位」，这一层永远不会运行。
func TestDailyLLMUsesReservedQuota(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	got := f.daily(t, time.Now())

	if f.callCount() != 1 {
		t.Fatalf("该打一次模型，实际 %d 次", f.callCount())
	}
	if !hasTitle(got, "模型歌") {
		t.Fatalf("模型推荐的歌该进列表，得到 %v", titles(got))
	}
	if len(got) != vDailySize {
		t.Fatalf("歌单该是 %d 首（名额还给在线层补齐），得到 %d", vDailySize, len(got))
	}
	// 名字对不上就说明这一条不是从搜索里来的。
	var model online.Track
	for _, t2 := range got {
		if t2.Title == "模型歌" {
			model = t2
		}
	}
	if model.PlatformID != "m1" {
		t.Errorf("列表里的曲目必须来自**搜索**（平台内 id = m1），得到 %q", model.PlatformID)
	}
}

// TestDailyLLMGivesQuotaBackWhenModelFails 钉住「开了开关不会让推荐变少」。
//
// 模型胡说八道（搜不到任何东西）时，预留的 5 个名额要**还给在线层**，
// 而不是丢给本地兜底、更不是空着。
func TestDailyLLMGivesQuotaBackWhenModelFails(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(
		ai.RecommendCandidate{Title: "根本不存在的歌", Artist: "不存在的歌手"},
		ai.RecommendCandidate{Title: "另一首不存在的", Artist: "不存在的人"},
	)
	// 故意不给这两个关键词注册任何结果 —— 模型在胡说。

	got := f.daily(t, time.Now())

	if f.callCount() != 1 {
		t.Fatalf("该打一次模型，实际 %d 次", f.callCount())
	}
	if len(got) != vDailySize {
		t.Fatalf("歌单该是 %d 首，得到 %d：%v", vDailySize, len(got), titles(got))
	}
	for _, bad := range []string{"根本不存在的歌", "另一首不存在的"} {
		if hasTitle(got, bad) {
			t.Errorf("模型胡说的歌不该进列表：%q", bad)
		}
	}
	// 名额该还给在线层：20 首全是「在线」那批。
	if n := len(got); n != vDailySize {
		t.Fatalf("在线层该补满，得到 %d 首", n)
	}
	for _, tt := range titles(got) {
		if !strings.HasPrefix(tt, "在线曲目") {
			t.Errorf("名额还给在线层之后不该混进别的：%q", tt)
		}
	}
}

// TestDailyLLMEmptyReplyIsNotAnError 钉住「空回复（非故障）→ 这一层跳过」。
func TestDailyLLMEmptyReplyIsNotAnError(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply() // 网关随机路由到某个模型，这次回了空 content

	got := f.daily(t, time.Now())
	f.waitCalls(t, 1)

	if len(got) != vDailySize {
		t.Fatalf("空回复时歌单该照旧 %d 首，得到 %d", vDailySize, len(got))
	}
	for _, tt := range titles(got) {
		if !strings.HasPrefix(tt, "在线曲目") {
			t.Errorf("空回复时不该混进别的：%q", tt)
		}
	}
}

// TestDailyLLMTimeoutFallsBackToTwoLayers 钉住「绝不许阻塞」。
//
// 大模型那一路一直不回来时，请求路径只等 vDailyLLMSyncWait，然后照常交
// 在线 + 本地两层的结果。
func TestDailyLLMTimeoutFallsBackToTwoLayers(t *testing.T) {
	// ⚠️ 两个值要拉开距离：请求路径的上限（50ms）远小于后台的上限（2s）。
	// 只把后台也设成很小的话，「请求路径其实在无限等」这种改法也能蒙混过关
	// （后台自己 150ms 就回来了）—— 变异校验抓到过这一点。
	oldSync, oldTimeout := vDailyLLMSyncWait, vDailyLLMTimeout
	vDailyLLMSyncWait = 50 * time.Millisecond
	vDailyLLMTimeout = 2 * time.Second
	defer func() { vDailyLLMSyncWait, vDailyLLMTimeout = oldSync, oldTimeout }()

	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	// 让模型这一路卡到自己的超时。
	f.blockUntilTimeout()

	start := time.Now()
	got := f.daily(t, time.Now())
	elapsed := time.Since(start)

	// 阈值（1s）卡在「请求路径的上限」（50ms）与「后台的上限」（2s）之间：
	// 只要请求路径真的在等后台跑完，这里就会超。
	if elapsed > time.Second {
		t.Fatalf("请求路径不该被大模型拖住，实际等了 %s", elapsed)
	}
	if len(got) != vDailySize {
		t.Fatalf("超时后该照旧交 %d 首（在线 + 本地两层），得到 %d", vDailySize, len(got))
	}
	if hasTitle(got, "模型歌") {
		t.Fatal("超时的那一路不该有任何产出")
	}
}

// ── 3. 模型的输出不可信：必须过既有的匹配链路 ───────────────────────────

// TestDailyLLMRequiresSameTitle 钉住「歌名要过 sameTitle，不做模糊包含」。
//
// 模型说《海屿你》，搜出来的是《海屿你心碎版》—— 名字不一样，不能当同一首。
func TestDailyLLMRequiresSameTitle(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "海屿你", Artist: "张杰"})
	// 三个关键词都只搜得到「名字很像但不一样」的那首。
	lookalike := llmSong("look", "海屿你心碎版", "张杰")
	f.addKeyword(t, "张杰 海屿你", lookalike)
	f.addKeyword(t, "海屿你", lookalike)
	f.addKeyword(t, "张杰", lookalike)

	got := f.daily(t, time.Now())
	f.waitCalls(t, 1)

	if hasTitle(got, "海屿你心碎版") {
		t.Fatalf("名字对不上的版本不该被当成同一首推出来：%v", titles(got))
	}
	if len(got) != vDailySize {
		t.Fatalf("名额该还给在线层，得到 %d 首", len(got))
	}
}

// TestDailyLLMRejectsWrongArtist 钉住「歌手对不上就不认」。
func TestDailyLLMRejectsWrongArtist(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "晴天", Artist: "周杰伦"})
	// 同名但是别人的歌。
	wrong := llmSong("w1", "晴天", "五月天")
	f.addKeyword(t, "周杰伦 晴天", wrong)
	f.addKeyword(t, "晴天", wrong)
	f.addKeyword(t, "周杰伦", wrong)

	got := f.daily(t, time.Now())
	f.waitCalls(t, 1)

	if hasTitle(got, "晴天") {
		t.Fatalf("歌手对不上的同名曲目不该进列表：%v", titles(got))
	}
}

// TestDailyLLMFallsBackToArtistOnlySearch 钉住「主关键词搜不到时退到只搜歌手」。
//
// 模型把歌名写成《海屿你》，平台上的正式写法是《海屿你 (Live)》——
// `sameTitle` 会剥掉括号，所以这两者是同一首；但按「歌手 + 歌名」搜不到，
// 得退到只搜歌手才捞得着。
func TestDailyLLMFallsBackToArtistOnlySearch(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "海屿你", Artist: "张杰"})
	// 前两个关键词搜不到，只有「张杰」搜得到。
	f.addKeyword(t, "张杰", llmSong("live", "海屿你 (Live)", "张杰"))

	got := f.daily(t, time.Now())

	if !hasTitle(got, "海屿你 (Live)") {
		t.Fatalf("退到只搜歌手应该能捞到同一首（括号注释由 sameTitle 剥掉）：%v", titles(got))
	}
}

// TestDailyLLMDedupesAgainstOnlineLayer 钉住跨层去重。
//
// 模型推的歌如果在线层已经推过（**另一个平台**的同名曲目 → 另一个虚拟 id），
// 用户不该看到两条。
func TestDailyLLMDedupesAgainstOnlineLayer(t *testing.T) {
	f, _ := llmBaseline(t)
	// 在线层第一条就是《重歌》。
	f.addKeyword(t, "在线歌手", llmSong("dup1", "重歌", "在线歌手"))
	f.addKeyword(t, "在线歌手 重歌", llmSong("dup2", "重歌", "在线歌手"))
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "重歌", Artist: "在线歌手"})

	got := f.daily(t, time.Now())
	f.waitCalls(t, 1)

	if n := countTitle(got, "重歌"); n != 1 {
		t.Fatalf("《重歌》该只出现一次，实际 %d 次：%v", n, titles(got))
	}
}

// TestDailyLLMExcludesRecentlyPlayed 钉住「模型这一层也走同一套排除集」。
//
// 提示词里虽然写了「不要推荐他最近听过的」，但那是**请求**不是**保证** ——
// 下游必须自己再判一次。
func TestDailyLLMExcludesRecentlyPlayed(t *testing.T) {
	f, user := llmBaseline(t)
	// 用户刚刚听过《模型歌》—— 刻意用一个**不同的平台内 id**，
	// 这样它靠的是排除集里「歌名+歌手」那条身份键（而不是虚拟 id）把模型那条挡住。
	// 真实场景：他刚在网易听完，模型推荐的是 QQ 上那一条。
	recordPlay(t, f.harness, user, online.Track{
		Platform: "wy", PlatformID: "played-other", Title: "模型歌", Artists: []string{"模型歌手"},
	}, time.Now().Unix())

	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	got := f.daily(t, time.Now())

	if hasTitle(got, "模型歌") {
		t.Fatalf("刚听过的歌不该被推出来：%v", titles(got))
	}
	if len(got) != vDailySize {
		t.Fatalf("名额该还给在线层，得到 %d 首", len(got))
	}
}

// TestDailyLLMExcludesFavorited 钉住「收藏过的也不推」。
//
// ⚠️ 用的是**同一个平台内 id**，因为既有的 `vDailyExclude.has` 对收藏只认虚拟 id
// 这一条口径（`recentKey` 那条身份键只管最近播放）。所以「收藏了网易版、
// 被推了 QQ 版」这种跨平台重复**现在挡不住** —— 那是既有排除集的缺口，不是这一层
// 引入的，本轮按「只做这一件事」不动它，已记进报告。
func TestDailyLLMExcludesFavorited(t *testing.T) {
	f, user := llmBaseline(t)
	fav := online.Track{
		Platform: "wy", PlatformID: "m1", Title: "模型歌", Artists: []string{"模型歌手"},
	}
	fake := f.it.registry.Put(fav)
	if err := f.it.store.AddFavorite(user, online.FavoriteItem{
		GUID: fake, Track: fav, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("记收藏失败：%v", err)
	}

	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	got := f.daily(t, time.Now())

	if hasTitle(got, "模型歌") {
		t.Fatalf("收藏过的歌不该被推出来：%v", titles(got))
	}
}

// ── 4. 缓存：不许每次请求都打模型 ───────────────────────────────────────

// TestDailyLLMCachesPerUserPerDay 钉住「按 (用户, 当天) 缓存」。
func TestDailyLLMCachesPerUserPerDay(t *testing.T) {
	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	now := time.Now()
	first := f.daily(t, now)
	if !hasTitle(first, "模型歌") {
		t.Fatalf("第一次该拿到模型推荐的歌：%v", titles(first))
	}

	// 同一天再算两次 —— 不该再打模型。
	second := f.daily(t, now)
	third := f.daily(t, now)

	if n := f.callCount(); n != 1 {
		t.Fatalf("同一天该只打一次模型，实际 %d 次", n)
	}
	for i, list := range [][]online.Track{second, third} {
		if !hasTitle(list, "模型歌") {
			t.Errorf("第 %d 次重算该命中缓存、仍带模型推荐：%v", i+2, titles(list))
		}
		if len(list) != vDailySize {
			t.Errorf("第 %d 次重算该还是 %d 首，得到 %d", i+2, vDailySize, len(list))
		}
	}

	// 隔天要重新问（键里带日期）。
	f.daily(t, now.AddDate(0, 0, 1))
	if n := f.callCount(); n != 2 {
		t.Fatalf("隔天该重新问一次模型，实际共 %d 次", n)
	}
}

// ── 5. 隐私：发出去的只有那三个字段 ─────────────────────────────────────

// TestLLMSeedsSendOnlyPublicMetadata 是隐私面的**字段级**用例。
//
// `ai.RecommendSeed` 只有 Title / Artist / Album 三个字段，所以「不发时间戳、
// 不发平台、不发 id」在类型上就成立；这条用例把「拼装那一处没往里塞别的东西」
// 也钉住（整个结构体 dump 出来找标记）。
func TestLLMSeedsSendOnlyPublicMetadata(t *testing.T) {
	f, user := llmBaseline(t)

	// 用一眼能认出来的标记当平台内 id / 播放时间戳。
	const secretID = "PLATFORMID-SECRET-9F3A"
	const secretTs = int64(1712345678)
	tr := online.Track{
		Platform: "wy", PlatformID: secretID,
		Title: "晴天", Artists: []string{"周杰伦"}, Album: "叶惠美",
	}
	recordPlay(t, f.harness, user, tr, secretTs)

	req := f.it.llmSeeds(user)

	// 正面：三个字段都在。
	var found bool
	for _, s := range req.Recent {
		if s.Title == "晴天" {
			found = true
			if s.Artist != "周杰伦" || s.Album != "叶惠美" {
				t.Errorf("种子字段不对：%+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("最近收听里该有那首歌，得到 %+v", req.Recent)
	}

	// 反面：整个请求 dump 出来都不该出现平台内 id、时间戳、平台名、用户键。
	dump := fmt.Sprintf("%+v", req)
	for _, leak := range []string{secretID, "1712345678", "wy", user, "Platform"} {
		if strings.Contains(dump, leak) {
			t.Errorf("发出去的种子里不该出现 %q：%s", leak, dump)
		}
	}
	if req.Count != vDailyLLMAsk {
		t.Errorf("条数该是 %d，得到 %d", vDailyLLMAsk, req.Count)
	}
}

// TestLLMSeedsCapWhatIsSent 钉住「最多发多少条」（上限在 pkg/ai，这里验它真的生效）。
func TestLLMSeedsCapWhatIsSent(t *testing.T) {
	f, user := llmBaseline(t)

	// ⚠️ 造数据与断言都用**字面量 20**（不是 ai 的常量）：写常量的话，改常量时
	// 造的数据和断言会一起变，用例永远绿 —— 等于没钉住。
	const cap = 20
	base := time.Now().Unix()
	for i := 0; i < cap+20; i++ {
		recordPlay(t, f.harness, user, online.Track{
			Platform: "wy", PlatformID: fmt.Sprintf("h%02d", i),
			Title: fmt.Sprintf("历史%02d", i), Artists: []string{"甲"},
		}, base-int64(i))
	}
	for i := 0; i < cap+20; i++ {
		tr := online.Track{
			Platform: "wy", PlatformID: fmt.Sprintf("f%02d", i),
			Title: fmt.Sprintf("收藏%02d", i), Artists: []string{"乙"},
		}
		fake := f.it.registry.Put(tr)
		if err := f.it.store.AddFavorite(user, online.FavoriteItem{
			GUID: fake, Track: tr, CreatedAt: base,
		}); err != nil {
			t.Fatalf("记收藏失败：%v", err)
		}
	}

	req := f.it.llmSeeds(user)
	if len(req.Recent) != cap {
		t.Errorf("最近收听该截到 %d 条，得到 %d", cap, len(req.Recent))
	}
	if len(req.Favorites) != cap {
		t.Errorf("收藏该截到 %d 条，得到 %d", cap, len(req.Favorites))
	}
}

// ── 6. 纯函数：歌手匹配 ────────────────────────────────────────────────

func TestArtistMatches(t *testing.T) {
	track := func(artists ...string) online.Track {
		return online.Track{Platform: "wy", PlatformID: "x", Title: "歌", Artists: artists}
	}

	cases := []struct {
		name string
		want string
		tr   online.Track
		ok   bool
	}{
		{"完全相同", "周杰伦", track("周杰伦"), true},
		{"候选多写一个", "周杰伦 / 方文山", track("周杰伦"), true},
		{"结果多写一个", "周杰伦", track("周杰伦", "方文山"), true},
		{"大小写与空白无关", " jay chou ", track("Jay Chou"), true},
		{"对不上", "周杰伦", track("五月天"), false},
		{"候选没给歌手（不参与判定）", "", track("五月天"), true},
		{"结果没给歌手（不参与判定）", "周杰伦", track(online.UnknownArtist), true},
		{"结果歌手是空的（不参与判定）", "周杰伦", track("  "), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := artistMatches(tc.want, tc.tr); got != tc.ok {
				t.Errorf("artistMatches(%q, %v) = %v，想要 %v", tc.want, tc.tr.Artists, got, tc.ok)
			}
		})
	}
}

// TestDailyLLMDedupeIsVisibleToLocalFallback 钉住「这一层写回的 `seen` 对后面的
// 本地兜底是有效的」。
//
// 场景：模型推荐的那首**同时也在本地曲库里**（同一平台同一 id —— 边听边下 /
// 收藏自动下载之后就是这种状态）。大模型层先入列，本地兜底必须看到 `seen` 里已经有
// 它，否则同一首歌会在列表里出现两次。
//
// ⚠️ 在线池故意只给 10 首：不这样的话在线层会把 20 个名额填满，本地兜底根本不会
// 运行 —— 这条用例就会「验了一件没发生的事」（变异校验抓到过）。
func TestDailyLLMDedupeIsVisibleToLocalFallback(t *testing.T) {
	f, user := llmBaseline(t)
	f.addKeyword(t, "在线歌手", onlineSongs("在线", 10)...)

	// 本地曲库里放一首「和模型推荐同一平台同一 id」的歌。
	localTrack(t, f.harness, "shared-1", "模型歌", "模型歌手")

	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("shared-1", "模型歌", "模型歌手"))

	now := time.Now()
	got := f.daily(t, now)

	// 先确认这一层真的落地了（不是超时 / 空回复跳过）—— 否则下面的断言证明不了什么。
	f.it.llmMu.Lock()
	e := f.it.llmCache[llmDayKey(user, now)]
	landed := e != nil && len(e.tracks) == 1
	f.it.llmMu.Unlock()
	if !landed {
		t.Fatal("大模型那一层该已经落地（不是超时跳过），否则这条用例验不到去重")
	}

	// 在线 10 + 大模型 1 = 11；本地那一首与它同 id，必须被 `seen` 挡在门外。
	if len(got) != 11 {
		t.Fatalf("该是 11 首（在线 10 + 大模型 1），得到 %d：%v", len(got), titles(got))
	}
	ids := make(map[string]int, len(got))
	for _, tr := range got {
		ids[tr.RealID()]++
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("曲目 %s 出现了 %d 次（大模型层写回的 seen 没被本地兜底看到）", id, n)
		}
	}
}

// TestLLMDayKeySeparatesUsersAndDays 是缓存键的**单元**用例。
//
// 按 (用户, 当天) 缓存是硬要求：少了用户维度，一台机器上两个账户会串味；
// 少了日期维度，昨天的候选会一直用下去。
func TestLLMDayKeySeparatesUsersAndDays(t *testing.T) {
	day1 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local)

	if llmDayKey("alice", day1) == llmDayKey("bob", day1) {
		t.Error("不同用户必须有不同的缓存键（否则两个账户串味）")
	}
	if llmDayKey("alice", day1) == llmDayKey("alice", day2) {
		t.Error("不同日期必须有不同的缓存键（否则昨天的候选一直用下去）")
	}
	// 同一天里的不同时刻必须是同一个键（否则一天里会反复问模型）。
	if llmDayKey("alice", day1) != llmDayKey("alice", day1.Add(11*time.Hour)) {
		t.Error("同一天的不同时刻该是同一个键")
	}
}

// TestDailyLLMNegativeResultIsCachedThenRetried 钉住负结果的缓存时长。
//
// 网关**偶尔返回空 content**（非故障）：整天不再试一次就等于这一层当天消失，
// 所以负结果只留 `vDailyLLMNegTTL`；但也不能每次请求都重试，所以在 TTL 之内
// 必须命中缓存、不再打模型。
func TestDailyLLMNegativeResultIsCachedThenRetried(t *testing.T) {
	f, user := llmBaseline(t)
	f.setSwitch(true)
	f.setReply() // 空回复

	now := time.Now()
	f.daily(t, now)
	f.waitCalls(t, 1)

	// TTL 之内：命中缓存，不再打模型。
	f.daily(t, now)
	if n := f.callCount(); n != 1 {
		t.Fatalf("负结果在 TTL 之内该命中缓存，实际打了 %d 次模型", n)
	}

	// 把条目时间戳拨到「比 vDailyLLMNegTTL 还旧」：下一次该重试。
	key := llmDayKey(user, now)
	f.it.llmMu.Lock()
	e, ok := f.it.llmCache[key]
	if !ok {
		f.it.llmMu.Unlock()
		t.Fatal("当天该有一条缓存条目")
	}
	e.ts = now.Add(-vDailyLLMNegTTL - time.Second)
	f.it.llmMu.Unlock()

	f.daily(t, now)
	f.waitCalls(t, 2)
}

// ── 7. 慢网关也要能在下一次请求看到这一层 ────────────────────────────────

// TestDailyLLMInvalidatesPlaylistCacheWhenReady 钉住「后台跑完作废当天那份歌单缓存」。
//
// 这条机制是为慢网关准备的：请求路径只等 vDailyLLMSyncWait（短），模型这一路往往
// 还没回来就交卷了；不作废缓存的话，那份「还没有大模型层」的歌单会被缓存 30 分钟，
// 用户再打开也看不到。
func TestDailyLLMInvalidatesPlaylistCacheWhenReady(t *testing.T) {
	oldSync, oldTimeout := vDailyLLMSyncWait, vDailyLLMTimeout
	vDailyLLMSyncWait = time.Millisecond // 请求路径基本不等
	vDailyLLMTimeout = 3 * time.Second
	defer func() { vDailyLLMSyncWait, vDailyLLMTimeout = oldSync, oldTimeout }()

	f, user := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))
	release := f.holdBackground()

	now := time.Now()
	guid := vDailyGUID(now, user)
	r := mustRequest(t, "/music/api/v1/playlist/detail", "{}")

	// 第一次请求：这一路还没回来 → 那份歌单**不带**大模型推荐。
	pl1, ok := f.it.vPlaylistFor(guid, r)
	if !ok {
		t.Fatal("该认领这个虚拟歌单 guid")
	}
	if hasTitle(pl1.Tracks, "模型歌") {
		t.Fatal("后台还没回来时不该有大模型推荐（这一条用例的前提）")
	}

	// 放行后台 → 它跑完、写缓存、并作废当天那份歌单。
	release()
	f.waitBackground()

	// 第二次请求：该重算并带上这一层。
	pl2, _ := f.it.vPlaylistFor(guid, r)
	if !hasTitle(pl2.Tracks, "模型歌") {
		t.Fatalf("后台跑完之后该作废缓存，下一次请求要带上大模型推荐：%v", titles(pl2.Tracks))
	}
}

// TestDailyLLMBackgroundSurvivesRequestCancel 钉住「后台那一路脱离请求 context」。
//
// 官方前端超时、用户切走 —— 这次请求被取消时，后台那一路**仍要跑完并把结果写进
// 当天的缓存**。不脱离的话，今天第一个请求一被取消，这一层整天都拿不到。
func TestDailyLLMBackgroundSurvivesRequestCancel(t *testing.T) {
	f, user := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 请求在出发前就被取消了

	now := time.Now()
	e := f.it.startLLMCandidates(ctx, user, now, vDailyLLMQuota)
	if e == nil {
		t.Fatal("该发出这一路")
	}
	f.waitBackground()

	f.it.llmMu.Lock()
	got := len(e.tracks)
	f.it.llmMu.Unlock()
	if got != 1 {
		t.Fatalf("请求被取消时这一路也该跑完并落进当天缓存，得到 %d 首", got)
	}
}

// TestDailyLLMCoalescesConcurrentRequests 钉住「同一天并发请求只打一次模型」。
//
// 缺了这条，头几秒里并发进来的每个请求都会各发一路后台任务 —— 白花 token、
// 还多打上游搜索。
func TestDailyLLMCoalescesConcurrentRequests(t *testing.T) {
	oldSync, oldTimeout := vDailyLLMSyncWait, vDailyLLMTimeout
	vDailyLLMSyncWait = 20 * time.Millisecond
	vDailyLLMTimeout = 3 * time.Second
	defer func() { vDailyLLMSyncWait, vDailyLLMTimeout = oldSync, oldTimeout }()

	f, _ := llmBaseline(t)
	f.setSwitch(true)
	f.setReply(ai.RecommendCandidate{Title: "模型歌", Artist: "模型歌手"})
	f.addKeyword(t, "模型歌手 模型歌", llmSong("m1", "模型歌", "模型歌手"))
	release := f.holdBackground()

	now := time.Now()
	r := mustRequest(t, "/music/api/v1/playlist/detail", "{}")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.it.dailyPlaylist(context.Background(), r, now)
		}()
	}
	wg.Wait()
	release()
	f.waitBackground()

	if n := f.callCount(); n != 1 {
		t.Fatalf("同一天并发请求该只打一次模型，实际 %d 次", n)
	}
}

// TestLLMSeedBlanksPlaceholderArtist 占位歌手不该发出去（纯噪声）。
func TestLLMSeedBlanksPlaceholderArtist(t *testing.T) {
	f, user := llmBaseline(t)
	recordPlay(t, f.harness, user, online.Track{
		Platform: "wy", PlatformID: "noartist", Title: "无歌手的歌",
	}, time.Now().Unix())

	req := f.it.llmSeeds(user)
	for _, s := range req.Recent {
		if s.Title != "无歌手的歌" {
			continue
		}
		if s.Artist != "" {
			t.Fatalf("占位歌手该压成空串，得到 %q", s.Artist)
		}
		return
	}
	t.Fatalf("最近收听里该有那首无歌手的歌：%+v", req.Recent)
}
