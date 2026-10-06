package intercept

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
)

// 「边听边存**关着**」时的滚动试听缓存用例。
//
// 这条路径要同时守住两件互相拉扯的事：
//  1. 重播**不出网**（连解析都不该发生）—— 否则「关着开关就没有任何缓存」的老毛病还在；
//  2. 缓存**不是曲库**：不进登记表、不进曲库目录、不补标签，且必须有上限。
//     开关关着的语义从第一天起就是「不留永久曲库副本」，滚动缓存不能把它偷换掉。
//
// 注意 tee_test.go 里那些「开关关着不该 tee」的旧断言已经不成立 —— 现在关着走的是
// 另一条路（`teeTarget.rolling`），那条断言在本文件里被拆成两层来验。

// rollingHarness 造一个开关**关着**的 harness（缺省就是关），返回曲库（下载）目录。
func rollingHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	dl := t.TempDir()
	h.it.cfg.DownloadDir = func() string { return dl }
	// TeeEnabled 不接线 = nil = 关，正是这条路径要验的状态。
	if h.it.teeOn() {
		t.Fatal("前提错了：这个 harness 的开关必须是关的")
	}
	return h, dl
}

// fakeID 造一个像虚拟 id 的 32 位 hex —— 滚动缓存的文件名就是它。
func fakeID(n int) string { return fmt.Sprintf("%032x", n+1) }

// writeRolling 直接往缓存目录放一条完整缓存，mtime 设成 ago 之前。
func writeRolling(t *testing.T, dir, fake, ext, body string, ago time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fake+ext)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-ago)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
	return p
}

// rollingComplete 列出缓存目录里的**完整**音频文件名（`.part` 不算），已排序。
func rollingComplete(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && rollingAudioName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// swapMediaClient 把取媒体的 HTTP 客户端换成不走代理的那只。
//
// 真实 `mediaClient` 走 `ProxyFromEnvironment`；开发机上有代理环境变量，
// 直连 127.0.0.1 的假 CDN 会被代理搅乱。
func swapMediaClient(t *testing.T) {
	t.Helper()
	old := mediaClient
	mediaClient = &http.Client{Transport: &http.Transport{}}
	t.Cleanup(func() { mediaClient = old })
}

// countingResolver 记「解析被调了几次」。
//
// 「重播不出网」不能只看媒体字节数：解析本身也是一次外网往返
// （网易/QQ 的取链接口）。缓存命中时它必须一次都不发生。
type countingResolver struct {
	*stubResolver
	calls int
}

func (c *countingResolver) Resolve(ctx context.Context, id string) (*online.Resolved, error) {
	c.calls++
	return c.stubResolver.Resolve(ctx, id)
}

func (c *countingResolver) ResolveBatch(ctx context.Context, ids []string) (map[string]*online.Resolved, error) {
	c.calls++
	return c.stubResolver.ResolveBatch(ctx, ids)
}

// stubMedia 起一个假 CDN，返回服务器与被取次数。
func stubMedia(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// ── 主体：重播不出网 ────────────────────────────────────────────────────

// TestRollingCacheServesReplayWithoutNetwork 是这条路径存在的理由。
//
// 开关关着、完整播一首 → 最近 N 首留在本地；再播同一首（含拖动 Range）时，
// **解析与媒体**两次外网往返都不该再发生，字节直接来自磁盘。
func TestRollingCacheServesReplayWithoutNetwork(t *testing.T) {
	h, dl := rollingHarness(t)
	swapMediaClient(t)

	body := "ID3\x03\x00\x00\x00rolling-cache-payload-0123456789"
	srv, hits := stubMedia(t, body)

	tr := teeTrack("77", "缓存歌", "某人")
	fake := h.it.registry.Put(tr)
	res := &countingResolver{stubResolver: &stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"77": {URL: srv.URL + "/a.mp3", Format: "mp3", Size: int64(len(body))},
	}}}
	h.it.pool = online.NewPool(res)

	// ① 第一次播放：出网一次，字节进客户端，同时留下滚动缓存。
	w1, handled := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, "", nil)
	if !handled {
		t.Fatal("取流该被拦截层处理")
	}
	if w1.Code != http.StatusOK {
		t.Fatalf("第一次取流该 200，得到 %d：%s", w1.Code, w1.Body.String())
	}
	if got := w1.Body.String(); got != body {
		t.Fatalf("客户端该收到完整字节，得到 %q", got)
	}
	if hits.Load() != 1 || res.calls != 1 {
		t.Fatalf("第一次播放该出网各一次，得到 media=%d resolve=%d", hits.Load(), res.calls)
	}

	cached := filepath.Join(h.it.teeRollingDir(), fake+".mp3")
	if b, err := os.ReadFile(cached); err != nil || string(b) != body {
		t.Fatalf("完整播完该在缓存目录留下这份字节：%v（%s）", err, cached)
	}

	// ② 重播：**不出网**（媒体与解析都不动）。
	w2, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, "", nil)
	if got := w2.Body.String(); got != body {
		t.Fatalf("重播该从本地喂完整字节，得到 %q", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("⚠️ 重播不该再打媒体接口，却被取了 %d 次 —— 滚动缓存没生效", hits.Load())
	}
	if res.calls != 1 {
		t.Fatalf("⚠️ 重播连解析都不该发生，却解析了 %d 次", res.calls)
	}

	// ③ Range（拖动进度条）：缓存也要支持，且同样不出网。
	wr, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, "",
		map[string]string{"Range": "bytes=0-4"})
	if wr.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求该回 206，得到 %d", wr.Code)
	}
	if got := wr.Body.String(); got != body[:5] {
		t.Fatalf("Range 该回前 5 字节，得到 %q", got)
	}
	if hits.Load() != 1 || res.calls != 1 {
		t.Fatalf("Range 命中也该完全不出网：media=%d resolve=%d", hits.Load(), res.calls)
	}

	// 曲库目录必须干干净净：滚动缓存的归宿不是曲库。
	if names := rollingComplete(t, dl); len(names) != 0 {
		t.Fatalf("开关关着时曲库目录不该多出文件：%v", names)
	}
}

// TestRollingPartNamesAreUnique 钉住「同一条曲目的两次并发取流各写各的暂存」。
//
// 共用一个 `<虚拟id>.part` 时，两股字节会交错写进同一个文件，而两边都认为自己
// 「写够了 expected」—— 于是一条坏缓存被当成完整缓存转正，之后每次重播都放坏音频。
func TestRollingPartNamesAreUnique(t *testing.T) {
	h, _ := rollingHarness(t)
	body := "ID3\x03\x00\x00\x00body"
	resp := func() *http.Response {
		return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	}
	tr := teeTrack("40", "并发取的歌", "某人")
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(40), nil)

	a := h.it.beginTee(fakeID(40), tr, resp(), req)
	b := h.it.beginTee(fakeID(40), tr, resp(), req)
	if a == nil || b == nil {
		t.Fatal("两次都该开出暂存")
	}
	defer func() {
		_ = a.f.Close()
		_ = b.f.Close()
	}()
	if a.part == b.part {
		t.Fatalf("⚠️ 同一条曲目的两次并发取流共用了同一个暂存文件：%s", a.part)
	}
	for _, tt := range []*teeTarget{a, b} {
		if !strings.HasSuffix(tt.part, ".part") {
			t.Errorf("暂存名仍该以 .part 结尾（启动清扫靠它认残骸）：%s", tt.part)
		}
		if !strings.HasPrefix(filepath.Base(tt.part), fakeID(40)) {
			t.Errorf("暂存名该以虚拟 id 开头（便于排查）：%s", tt.part)
		}
	}
}

// TestRollingCacheWorksWithoutDownloadDir 钉住「滚动缓存不依赖下载目录」。
//
// 这条是**有意的**：用户没配曲库目录时，「边听边存」那条路整体不可用（没有落点
// 就没有提升），但「重播不出网」对他一样有用 —— 临时试听缓存落在应用数据目录，
// 与曲库在哪无关。反过来，如果关掉开关又没配目录就完全不留缓存，这个功能的
// 价值就只剩「已经配了下载目录的人」能拿到，而它本来是最省代价的那一半。
func TestRollingCacheWorksWithoutDownloadDir(t *testing.T) {
	h := newHarness(t) // 刻意不接 DownloadDir
	if h.it.cfg.DownloadDir != nil {
		t.Fatal("前置条件：这个 harness 不该有下载目录")
	}
	if h.it.teeDir() != "" {
		t.Fatalf("没有下载目录时不该有曲库暂存目录：%s", h.it.teeDir())
	}

	body := "ID3\x03\x00\x00\x00no-download-dir"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(30), nil)
	tr := teeTrack("30", "没有曲库目录的歌", "某人")

	tt := h.it.beginTee(fakeID(30), tr, resp, req)
	if tt == nil || !tt.rolling {
		t.Fatal("没有下载目录时也该留滚动试听缓存")
	}
	var sink strings.Builder
	_, _ = io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body)
	h.it.finishTee(tt, fakeID(30), tr, resp.ContentLength, "mp3")

	if b, err := os.ReadFile(filepath.Join(h.it.teeRollingDir(), fakeID(30)+".mp3")); err != nil || string(b) != body {
		t.Fatalf("缓存该落在数据目录下：%v", err)
	}
	// 开关开着、但**没有**下载目录时：那条路径整体不做（不能退化成滚动缓存，
	// 否则「开着」这条已经验收过的路径会凭空多出新的写盘行为）。
	h.it.cfg.TeeEnabled = func() bool { return true }
	if tt := h.it.beginTee(fakeID(31), tr, resp, req); tt != nil {
		t.Fatal("开关开着却没有落点时，该整体不落盘，不能退化成滚动缓存")
	}
}

// ── 红线：滚动缓存不是「已进曲库」 ──────────────────────────────────────
// TestRollingCacheNeverEntersRegistryOrLibrary 是任务 B 的红线用例。
//
// 滚动缓存是**临时试听缓存**：它不能被下载登记表认领（登记表里的每条都意味着
// 「取流去读这个路径」，而缓存随时会被淘汰 → 收藏里那首歌 404），不能落进曲库
// 目录，也不能触发补标签（补标签等于把它当曲库文件对待）。
func TestRollingCacheNeverEntersRegistryOrLibrary(t *testing.T) {
	h, dl := rollingHarness(t)

	enriched := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enriched.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{}}`))
	}))
	t.Cleanup(srv.Close)
	h.it.localAPI = srv.URL

	body := "ID3\x03\x00\x00\x00fake"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(1), nil)
	tr := teeTrack("1", "晴天", "周杰伦")
	fake := fakeID(1)

	tt := h.it.beginTee(fake, tr, resp, req)
	if tt == nil {
		t.Fatal("开关关着也该留滚动缓存（这是这次改动的目的）")
	}
	if !tt.rolling {
		t.Fatal("开关关着时应走滚动缓存那条路，而不是提升进曲库")
	}
	var sink strings.Builder
	if _, err := io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	h.it.finishTee(tt, fake, tr, resp.ContentLength, "mp3")

	// 文件确实进了滚动缓存（否则「红线」就是靠什么都没做守住的，没有意义）。
	if b, err := os.ReadFile(filepath.Join(h.it.teeRollingDir(), fake+".mp3")); err != nil || string(b) != body {
		t.Fatalf("该留在滚动缓存里：%v", err)
	}

	// ① 登记表一条都不能有 —— 也不能出现在 downloaded.json 里。
	if it, ok := h.it.downloaded.Get(fake); ok {
		t.Fatalf("⚠️ 滚动缓存绝不能被登记表认领，却登记了：%+v", it)
	}
	if n := len(h.it.downloaded.All()); n != 0 {
		t.Fatalf("登记表该是空的，却有 %d 条", n)
	}
	if b, err := os.ReadFile(h.it.downloaded.Path()); err == nil && strings.Contains(string(b), fake) {
		t.Fatalf("download.json 里不该出现滚动缓存的虚拟 id：%s", b)
	}

	// ② 曲库目录里不能多出文件（连 `.qulv-tee` 暂存都不该有）。
	if names := rollingComplete(t, dl); len(names) != 0 {
		t.Fatalf("曲库目录不该多出文件：%v", names)
	}
	if _, err := os.Stat(h.it.teeDestPath(tr, "mp3")); err == nil {
		t.Fatalf("滚动缓存不该落成曲库文件：%s", h.it.teeDestPath(tr, "mp3"))
	}
	if entries, err := os.ReadDir(dl); err == nil && len(entries) != 0 {
		t.Fatalf("曲库目录该是空的：%v", entries)
	}

	// ③ 缓存目录必须**不在**下载目录底下 —— 这条是红线的结构性保证：
	//    放进曲库目录（哪怕藏在 `.` 子目录里）就有被曲库扫描当成真曲目的风险。
	rolling, dlAbs := h.it.teeRollingDir(), dl
	if strings.HasPrefix(filepath.Clean(rolling), filepath.Clean(dlAbs)+string(os.PathSeparator)) {
		t.Fatalf("滚动缓存不能放在曲库目录里：%s", rolling)
	}

	// ④ 不补标签：补标签是「曲库文件」才有的待遇。
	if enriched.Load() != 0 {
		t.Fatalf("滚动缓存不该触发补标签，却调了 %d 次", enriched.Load())
	}
}

// ── 上限与淘汰 ──────────────────────────────────────────────────────────

// TestRollingCacheKeepsOnlyRecentN 钉住「有上限」这件事本身。
func TestRollingCacheKeepsOnlyRecentN(t *testing.T) {
	h, _ := rollingHarness(t)
	dir := h.it.teeRollingDir()

	// 放 N+2 条，mtime 依次变新：fakeID(0) 最旧 … fakeID(N+1) 最新。
	for i := 0; i < teeRollingKeep+2; i++ {
		writeRolling(t, dir, fakeID(i), ".mp3", "x", time.Duration(teeRollingKeep+2-i)*time.Minute)
	}
	// 正在进行中的 `.part` 既不该被淘汰，也不该占 keep 名额。
	part := writeRolling(t, dir, fakeID(90), ".part", "half", 10*time.Minute)

	if got := len(rollingComplete(t, dir)); got != teeRollingKeep+2 {
		t.Fatalf("前置条件：该有 %d 条完整缓存，得到 %d", teeRollingKeep+2, got)
	}
	if removed := h.it.purgeRolling(teeRollingKeep); removed != 2 {
		t.Fatalf("该淘汰 2 条（超出上限的部分），得到 %d", removed)
	}

	names := rollingComplete(t, dir)
	if len(names) != teeRollingKeep {
		t.Fatalf("淘汰后该只剩 %d 条，得到 %v", teeRollingKeep, names)
	}
	// 留下的必须是**最近**的那些：fakeID(2)..fakeID(N+1)，最旧的两条被淘汰。
	for i := 2; i < teeRollingKeep+2; i++ {
		want := fakeID(i) + ".mp3"
		if !contains(names, want) {
			t.Errorf("最近使用的 %s 该被留住，得到 %v", want, names)
		}
	}
	if contains(names, fakeID(0)+".mp3") || contains(names, fakeID(1)+".mp3") {
		t.Errorf("最旧的两条该被淘汰，得到 %v", names)
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("进行中的 .part 绝不能被淘汰：%v", err)
	}
}

// TestRollingWriteTriggersEviction 钉住「写一条 = 顺手淘汰」这条接线：
// 上限不是靠调用方记得调 purgeRolling 维持的，而是写在 finishRolling 里。
func TestRollingWriteTriggersEviction(t *testing.T) {
	h, _ := rollingHarness(t)
	dir := h.it.teeRollingDir()
	for i := 0; i < teeRollingKeep; i++ {
		writeRolling(t, dir, fakeID(i), ".mp3", "old", time.Duration(teeRollingKeep-i)*time.Minute)
	}

	body := "ID3\x03\x00\x00\x00new"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(50), nil)
	tr := teeTrack("50", "新歌", "某人")
	tt := h.it.beginTee(fakeID(50), tr, resp, req)
	if tt == nil {
		t.Fatal("该开滚动缓存")
	}
	var sink strings.Builder
	_, _ = io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body)
	h.it.finishTee(tt, fakeID(50), tr, resp.ContentLength, "mp3")

	names := rollingComplete(t, dir)
	if len(names) != teeRollingKeep {
		t.Fatalf("写一条之后该仍只有 %d 条，得到 %v", teeRollingKeep, names)
	}
	if !contains(names, fakeID(50)+".mp3") {
		t.Errorf("新写的这条必须在，得到 %v", names)
	}
	if contains(names, fakeID(0)+".mp3") {
		t.Errorf("最旧的那条该被淘汰，得到 %v", names)
	}
}

// TestRollingCacheHitRefreshesRecency 钉住淘汰口径是「最近**使用**」而不是「最近写入」。
//
// 关着开关的人最常见的动作就是「刚才那首再听一遍」—— 如果命中不刷新时间线，
// 这首刚听完的歌会被下一首歌挤掉，缓存对他就等于没有。
func TestRollingCacheHitRefreshesRecency(t *testing.T) {
	h, _ := rollingHarness(t)
	dir := h.it.teeRollingDir()

	// 填满缓存：fakeID(0) 最旧 … fakeID(N-1) 最新。
	for i := 0; i < teeRollingKeep; i++ {
		writeRolling(t, dir, fakeID(i), ".mp3", "x", time.Duration(teeRollingKeep-i)*time.Minute)
	}
	// 「听一遍」最旧的那条：命中即刷新它的使用时间。
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(0), nil)
	w := httptest.NewRecorder()
	if !h.it.serveRollingIfAny(w, req, fakeID(0)) {
		t.Fatal("该命中缓存")
	}
	if w.Body.Len() == 0 {
		t.Fatal("命中缓存该把字节喂给客户端")
	}
	// 再写一条新的，逼出一次淘汰：该淘汰的是**没被用过**的 fakeID(1)，不是刚听的 fakeID(0)。
	body := "ID3\x03\x00\x00\x00new"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	tr := teeTrack("60", "又一首", "某人")
	tt := h.it.beginTee(fakeID(60), tr, resp, httptest.NewRequest(http.MethodGet, "/x", nil))
	var sink strings.Builder
	_, _ = io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body)
	h.it.finishTee(tt, fakeID(60), tr, resp.ContentLength, "mp3")

	names := rollingComplete(t, dir)
	if !contains(names, fakeID(0)+".mp3") {
		t.Errorf("刚听过的那条必须留住（淘汰要按最近使用），得到 %v", names)
	}
	if contains(names, fakeID(1)+".mp3") {
		t.Errorf("最久没用过的那条该被淘汰，得到 %v", names)
	}
}

// TestRollingPurgeZeroEmpties 钉住 `keep <= 0` 的语义（0 = 关掉滚动缓存）。
func TestRollingPurgeZeroEmpties(t *testing.T) {
	h, _ := rollingHarness(t)
	dir := h.it.teeRollingDir()
	for i := 0; i < 3; i++ {
		writeRolling(t, dir, fakeID(i), ".mp3", "x", time.Duration(i+1)*time.Minute)
	}
	part := writeRolling(t, dir, fakeID(80), ".part", "half", time.Minute)
	if removed := h.it.purgeRolling(0); removed != 3 {
		t.Fatalf("keep=0 该清空 3 条，得到 %d", removed)
	}
	if names := rollingComplete(t, dir); len(names) != 0 {
		t.Fatalf("keep=0 之后不该剩下完整缓存：%v", names)
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf(".part 不属于完整缓存，不该被 purgeRolling 碰：%v", err)
	}
}

// ── 不完整必须丢弃（与「开着」那条路径同一条规矩）─────────────────────

func TestRollingCacheDiscardsIncomplete(t *testing.T) {
	h, _ := rollingHarness(t)
	// 上游声明 100 字节，实际只转发了 7 字节（切歌 / 断流）。
	resp := &http.Response{StatusCode: 200, ContentLength: 100, Body: io.NopCloser(strings.NewReader(""))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fakeID(70), nil)
	tr := teeTrack("70", "半截歌", "某人")

	tt := h.it.beginTee(fakeID(70), tr, resp, req)
	if tt == nil || !tt.rolling {
		t.Fatal("该开滚动缓存")
	}
	_, _ = (&teeWriter{w: &strings.Builder{}, t: tt}).Write([]byte("ID3fake"))
	h.it.finishTee(tt, fakeID(70), tr, resp.ContentLength, "mp3")

	if names := rollingComplete(t, h.it.teeRollingDir()); len(names) != 0 {
		t.Fatalf("⚠️ 半截缓存必须丢弃 —— 否则「重播不出网」会变成「重播播到一半停」：%v", names)
	}
	if entries, err := os.ReadDir(h.it.teeRollingDir()); err == nil && len(entries) != 0 {
		t.Fatalf("连 .part 都不该留下：%v", entries)
	}
	if _, ok := h.it.downloaded.Get(fakeID(70)); ok {
		t.Fatal("半截文件更不能进登记表")
	}
}

// ── 常量之间的约定 + 启动清扫 ──────────────────────────────────────────

// TestRollingExtsMatchExtForFormat 钉住「查缓存试的扩展名」与「写缓存用的扩展名」同源。
//
// 两边不同源时，缓存写进去了却永远查不中 —— 表现是「重播仍然出网」，
// 而且日志里连一行命中都没有。
func TestRollingExtsMatchExtForFormat(t *testing.T) {
	for _, format := range []string{"mp3", "flac", "m4a", "aac", "wav", "ape", "", "谁知道"} {
		ext := extForFormat(format)
		if !contains(teeRollingExts, ext) {
			t.Errorf("extForFormat(%q) = %q 不在 teeRollingExts 里，写进去的缓存永远查不中", format, ext)
		}
	}
	if teeRollingKeep <= 0 {
		t.Fatalf("上限必须是正的：%d", teeRollingKeep)
	}
}

// TestRollingKeepValueIsPinned 钉住上限的**取值**本身。
//
// 这条断言看着像把常量抄了一遍，但它守的是一个决定而不是实现细节：
// 上限多小 = 重播命中率，多大 = 临时缓存能吃多少磁盘。改它必须是一次
// 有意识的编辑（改完这里会红，改的人就得顺手回答「为什么」）。
func TestRollingKeepValueIsPinned(t *testing.T) {
	if teeRollingKeep != 5 {
		t.Fatalf("滚动缓存上限该是 5 首（理由见 tee.go 的 teeRollingKeep 注释），得到 %d", teeRollingKeep)
	}
	if teeRollingKeep > 50 {
		t.Fatalf("上限不能大到让临时缓存长成第二个曲库：%d", teeRollingKeep)
	}
}

// TestRollingCleanupSweepsStaleParts 钉住启动清扫覆盖到滚动目录。
//
// `.part` 只在 finishTee 里被删或改名；进程被杀（升级/断电）就会留下残骸。
func TestRollingCleanupSweepsStaleParts(t *testing.T) {
	h, _ := rollingHarness(t)
	dir := h.it.teeRollingDir()
	stale := writeRolling(t, dir, fakeID(0), ".part", "residue", 3*time.Hour)
	fresh := writeRolling(t, dir, fakeID(1), ".part", "in-flight", time.Minute)
	// 启动时顺手收敛上限：超过 N 条的老缓存该被清掉。
	for i := 0; i < teeRollingKeep+1; i++ {
		writeRolling(t, dir, fakeID(10+i), ".mp3", "x", time.Duration(i+1)*time.Minute)
	}

	h.it.cleanTeeCache()

	if _, err := os.Stat(stale); err == nil {
		t.Fatalf("上次运行留下的半截残骸该被清掉：%s", stale)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("刚写了一半的不该被清（启动时不清新鲜的）：%v", err)
	}
	if names := rollingComplete(t, dir); len(names) != teeRollingKeep {
		t.Fatalf("启动时该把缓存收敛到 %d 条，得到 %v", teeRollingKeep, names)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
