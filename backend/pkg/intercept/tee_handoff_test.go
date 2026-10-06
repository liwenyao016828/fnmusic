package intercept

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
)

// 「被切歌打断的下载，改成后台续传」的用例（见 tee.go 的「后台续传」一节）。
//
// 这一层要证的是**行为**，不是「不报错」：
//   - 客户端中途走了 → 文件最终完整 + 进了登记表 + 补标签被调用过；
//   - 续传本身失败（源不认 Range / 报错 / 长度对不上 / 解析不出直链）→ 什么都不留：
//     没有登记条目、没有曲库文件、没有 `.part`、没补过标签；
//   - 有界：并发上限、满了就丢（不排队）、收尾要能停。
//
// ⚠️ 这些都是**单测**：NAS 真机验收这次做不了（见交接说明）。

// errClientGone 模拟「写客户端失败」。
var errClientGone = errors.New("客户端已断开（用例模拟）")

// ── 假 CDN：支持 Range，行为可按用例调 ──────────────────────────────────

type fakeCDN struct {
	body []byte

	total  atomic.Int64 // 整轨（不带 Range）请求次数
	ranged atomic.Int64 // 带 Range 请求次数

	// gate 非 nil 时，Range 请求在写字节之前先等它关闭 —— 用来把一次续传
	// 钉在「在途」（验并发上限 / 收尾 / 不阻塞下一个人）。
	gate chan struct{}
	// status 非 0 时，Range 请求回这个状态码（200 = 源根本不认 Range）。
	status int
	// totalOverride 非 0 时，Content-Range 的总长用它（验「总长对不上」）。
	totalOverride int64
	// startOverride 非 0 时，Content-Range 的起点用它（验「起点对不上」）。
	startOverride int64
	// noContentRange 时不发 Content-Range 头。
	noContentRange bool
	// truncate 非 0 时，只给这么多余量就结束（验「上游给不够」）。
	truncate int64
	// holdAfter 非 0 时，整轨请求先给这么多字节就停住，等 gate 放行才给剩下的
	// —— 用它造出「客户端断开时，服务端正卡在读上游」这个**确定**的现场
	// （只要上游不再给字节，服务端把已到的那些转发完就只能停在读上）。
	holdAfter int64
}

func (c *fakeCDN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rng := strings.TrimSpace(r.Header.Get("Range"))
	if rng == "" {
		c.total.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(c.body)))
		if c.holdAfter > 0 {
			_, _ = w.Write(c.body[:c.holdAfter])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-c.gate:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(c.body[c.holdAfter:])
			return
		}
		_, _ = w.Write(c.body)
		return
	}
	c.ranged.Add(1)
	if c.gate != nil {
		// 等用例放行 —— 但请求被取消（收尾 / 用例结束）时必须自己退出去：
		// 卡住的 handler 会让 httptest.Server.Close 一直等，把「实现坏了」变成
		// 「用例挂住」，那是两回事。
		select {
		case <-c.gate:
		case <-r.Context().Done():
			return
		}
	}
	start := rangeStart(rng)
	if start < 0 || start > int64(len(c.body)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	status := c.status
	if status == 0 {
		status = http.StatusPartialContent
	}
	if status != http.StatusPartialContent {
		w.WriteHeader(status)
		return
	}
	rest := c.body[start:]
	if c.truncate > 0 && int64(len(rest)) > c.truncate {
		rest = rest[:c.truncate]
	}
	begin := start
	if c.startOverride > 0 {
		begin = c.startOverride
	}
	total := int64(len(c.body))
	if c.totalOverride > 0 {
		total = c.totalOverride
	}
	if !c.noContentRange {
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", begin, begin+int64(len(rest))-1, total))
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(rest)))
	w.WriteHeader(status)
	_, _ = w.Write(rest)
}

// rangeStart 从 `bytes=N-` 里取出起点；解析不出来返回 -1。
func rangeStart(rng string) int64 {
	rest, ok := strings.CutPrefix(rng, "bytes=")
	if !ok {
		return -1
	}
	startPart, _, ok := strings.Cut(rest, "-")
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// ── 模拟「客户端中途走了」 ──────────────────────────────────────────────

// abortSink 是「客户端读到 after 字节就断」的 ResponseWriter。
//
// 它做 net/http 在客户端断开时会做的那两件事：写失败、请求 ctx 被取消。
// 这样用例不必去赌真实 socket 的时序 —— 真机上这两件事由 net/http 负责，
// 这里只是把它的可观察后果搬进用例（真实 TCP 断开另有一条用例）。
type abortSink struct {
	*httptest.ResponseRecorder
	after   int
	cancel  context.CancelFunc
	onAbort func()
	once    sync.Once
}

func (a *abortSink) Write(p []byte) (int, error) {
	if a.Body.Len() >= a.after {
		a.once.Do(func() {
			if a.onAbort != nil {
				a.onAbort()
			}
			a.cancel()
		})
		return 0, errClientGone
	}
	return a.ResponseRecorder.Write(p)
}

// ── 现场 ────────────────────────────────────────────────────────────────

// newHandoffHarness 造一个「开关开着 + 取媒体走假 CDN + 能数补标签次数」的现场。
//
// ids 是这几条曲目的平台内 id（要几个给几个：并发上限那类用例要两首）。
func newHandoffHarness(t *testing.T, cdn *fakeCDN, ids ...string) (*harness, string, *atomic.Int64) {
	t.Helper()
	h, dir := teeHarness(t)
	swapMediaClient(t)
	srv := httptest.NewServer(cdn)
	t.Cleanup(srv.Close)
	answers := make(map[string]*online.Resolved, len(ids))
	for _, id := range ids {
		answers[id] = &online.Resolved{
			URL: srv.URL + "/a.mp3", Format: "mp3", Size: int64(len(cdn.body)),
		}
	}
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: answers})

	enrich := &atomic.Int64{}
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/download/enrich" {
			enrich.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{}}`))
	}))
	t.Cleanup(es.Close)
	h.it.localAPI = es.URL
	return h, dir, enrich
}

// streamAndAbort 发一次取流，并在「客户端」读到 after 字节之后把它丢掉。
//
// 它自己登记一条收尾：用例结束时必须没有在途任务（否则会有 goroutine 漏出这个
// 用例，还会在用例结束后打日志 —— testing 会因此 panic）。用例想放行被 gate
// 挡住的续传，就在**调用它之后**再注册自己的 t.Cleanup（LIFO：先放行，再等收尾）。
func (h *harness) streamAndAbort(t *testing.T, fake string, after int, onAbort func()) *abortSink {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, nil).WithContext(ctx)
	sink := &abortSink{
		ResponseRecorder: httptest.NewRecorder(),
		after:            after, cancel: cancel, onAbort: onAbort,
	}
	if !h.it.Handle(sink, req) {
		t.Fatal("取流该被拦截层认领")
	}
	t.Cleanup(func() {
		cancel()
		if !h.it.tee.drain(10 * time.Second) {
			t.Errorf("后台续传没能在 10 秒内收尾（在途 %d）—— 会有 goroutine 漏出这个用例", h.it.tee.inflight())
		}
		h.it.CloseTee()
	})
	return sink
}

// gateRelease 造一个「只关一次」的放行开关。
//
// 用例体里放行被 gate 钉住的续传（不放行，收尾就等不到它），cleanup 兜底 ——
// 用例提前 fatal 时也必须放行，否则 httptest 的 Close 会卡在「等 handler 返回」上。
func gateRelease(gate chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// waitFor 等到 cond 成立，或超时后让用例失败。
//
// 只用来等**后台状态就位**（后台任务没有回调可等），不用它代替断言。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等了 %v 条件仍未成立", d)
}

// teeParts 列出曲库暂存目录里的文件（无论是否 `.part`）。
func teeParts(t *testing.T, dir string) []string {
	t.Helper()
	got, _ := filepath.Glob(filepath.Join(dir, teeSubdir, "*"))
	return got
}

// ── 主体：被打断 → 后台补完 → 进登记表 + 补标签 ──────────────────────────

// TestTeeHandoffResumesInterruptedStream 是这个功能存在的理由。
//
// 客户端中途走了，服务端该自己把这首拉完，然后走既有的提升→登记→补标签那条路。
// 删掉续传（或改成旧的「直接丢掉」）这个用例就会红：登记表里什么都没有、文件也不在。
func TestTeeHandoffResumesInterruptedStream(t *testing.T) {
	body := bytes.Repeat([]byte{0xA7}, 3<<20) // 3MiB：远大于任何缓冲，必须真的「没拉完」
	cdn := &fakeCDN{body: body}
	h, dir, enrich := newHandoffHarness(t, cdn, "88")
	tr := teeTrack("88", "切歌走了的歌", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 64<<10, nil)

	if !h.it.tee.drain(10 * time.Second) {
		t.Fatalf("后台续传该自己收尾（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 1 {
		t.Fatalf("该用**一次** Range 请求把剩下的字节补上，实际 %d 次", n)
	}
	it, ok := h.it.downloaded.Get(fake)
	if !ok {
		t.Fatal("⚠️ 被切歌打断的下载该在后台补完并进登记表（这正是这个功能的目的）")
	}
	if it.Size != int64(len(body)) {
		t.Fatalf("登记的该是完整大小 %d，得到 %d", len(body), it.Size)
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("落盘文件必须是**完整**的那一首：err=%v len=%d（期望 %d）", err, len(got), len(body))
	}
	if enrich.Load() == 0 {
		t.Fatal("提升之后该补标签 —— 否则飞牛里标题/歌手/封面全是空的")
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("补完之后不该留下暂存文件：%v", left)
	}
}

// TestTeeHandoffResumesWhenReadErrorIsClientsCancellation 钉住 upstreamIntact 的那条判断。
//
// 真机上还有一种形态：客户端断开 → net/http 取消请求 ctx → 我们那条取媒体请求被
// **连坐取消** → io.Copy 停在**读**侧，于是现场里 readErr 非 nil。那不算上游故障，
// 照样要交接续传。
//
// 为什么在收尾入口上验而不是走真实 socket：真实 socket 上这份现场取决于内核缓冲里
// 还剩多少字节（缓冲区里还有数据时读根本不会出错），拿它当断言就是赌时序。
func TestTeeHandoffResumesWhenReadErrorIsClientsCancellation(t *testing.T) {
	body := bytes.Repeat([]byte{0xB1}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, _, enrich := newHandoffHarness(t, cdn, "87")
	tr := teeTrack("87", "停止时读侧被取消", "某人")
	fake := h.it.registry.Put(tr)

	resp := &http.Response{
		StatusCode: 200, ContentLength: int64(len(body)),
		Body: io.NopCloser(strings.NewReader("")),
	}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, nil)
	tt := h.it.beginTee(fake, tr, resp, req)
	if tt == nil {
		t.Fatal("该开 tee")
	}
	if _, err := (&teeWriter{w: io.Discard, t: tt}).Write(bytes.Repeat([]byte{7}, 64<<10)); err != nil {
		t.Fatalf("写暂存失败：%v", err)
	}
	// 现场：读侧报 context.Canceled，但客户端**已经走了**（是它连坐取消了这条读）。
	h.it.finishTeeAfterStream(tt, fake, tr, resp.ContentLength, "mp3", teeInterrupt{
		readErr: context.Canceled, clientGone: true,
	})

	if !h.it.tee.drain(10 * time.Second) {
		t.Fatalf("该交接后台续传并收尾（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 1 {
		t.Fatalf("该续传一次，实际 %d 次", n)
	}
	it, ok := h.it.downloaded.Get(fake)
	if !ok {
		t.Fatal("客户端连坐取消的那次中断也该补完")
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || len(got) != len(body) {
		t.Fatalf("落盘文件该完整：err=%v len=%d（期望 %d）", err, len(got), len(body))
	}
	if enrich.Load() == 0 {
		t.Fatal("提升之后该补标签")
	}
}

// TestTeeHandoffResumesWhenDisconnectStopsTheUpstreamRead 钉住「读侧先被我们发现」
// 那条真机形态：客户端断开时，服务端正**卡在读上游**（而不是卡在写客户端），
// 于是这份现场是「读侧报错 + 请求 ctx 已取消」。
//
// 为什么必须专门造这个形态：那种现场里 readErr 非 nil，如果交接判断只看读侧错误、
// 不看「客户端已经走了」，这次中断就会被误判成「上游断了」而丢掉半截 ——
// 而真机上绝大多数切歌恰恰是这种形态（客户端一关，net/http 立刻取消请求 ctx）。
//
// 怎么把它做成确定的：让 CDN 先给 64KiB 就停住。用例把这 64KiB 全读走之后，
// 服务端手里已经没有「没写完的字节」，它的下一步只能是在等上游 —— 此时断开，
// 停下来的位置一定是读侧。
func TestTeeHandoffResumesWhenDisconnectStopsTheUpstreamRead(t *testing.T) {
	const first = 64 << 10
	body := bytes.Repeat([]byte{0xD4}, 1<<20)
	gate := make(chan struct{})
	cdn := &fakeCDN{body: body, gate: gate, holdAfter: first}
	h, _, enrich := newHandoffHarness(t, cdn, "91")
	tr := teeTrack("91", "断开时正在读上游", "某人")
	fake := h.it.registry.Put(tr)
	release := gateRelease(gate)
	t.Cleanup(release)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.it.Handle(w, r) {
			http.Error(w, "unhandled", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("连不上用例自己的服务：%v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET /music/api/v1/track/stream?guid=%s HTTP/1.1\r\nHost: localhost\r\n\r\n", fake); err != nil {
		t.Fatalf("发请求失败：%v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("读响应头失败：%v", err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, first)); err != nil {
		t.Fatalf("该先收到那 %d 字节：%v", first, err)
	}
	// 到这里：上游没给更多字节，而客户端已经把服务端写出来的全读走了
	// → 服务端一定在等上游。此刻断开。
	_ = conn.Close()

	waitFor(t, 10*time.Second, func() bool { return h.it.tee.inflight() == 1 })
	release()
	if !h.it.tee.drain(15 * time.Second) {
		t.Fatalf("后台续传该自己收尾（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 1 {
		t.Fatalf("该走一次续传（Range 请求），实际 %d 次", n)
	}
	it, ok := h.it.downloaded.Get(fake)
	if !ok {
		t.Fatal("⚠️ 断开时正卡在读上游的那种中断，也必须被当成「客户端走了」并补完")
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("落盘文件必须完整：err=%v len=%d（期望 %d）", err, len(got), len(body))
	}
	if enrich.Load() == 0 {
		t.Fatal("提升之后该补标签")
	}
}

// TestTeeHandoffResumesAfterRealClientDisconnect 用**真实 TCP**把连接掐掉。
//
// 上面那条用例把「net/http 在客户端断开时会写失败 / 取消 ctx」这件事当成前提，
// 这一条就验这个前提本身：裸 socket 读一点就 close，拦截层照样把歌补完。
//
// ⚠️ 这条是**实测**（在本机上真的起了一个 http 服务 + 真的断开），但它仍然不是
// NAS 真机验收：中间那段「官方客户端怎么断流」的路没走。
func TestTeeHandoffResumesAfterRealClientDisconnect(t *testing.T) {
	body := bytes.Repeat([]byte{0x3C}, 8<<20) // 8MiB：绝不会在断开之前被写完
	gate := make(chan struct{})               // 把续传钉住，等用例确认「已经交接」
	cdn := &fakeCDN{body: body, gate: gate}
	h, _, _ := newHandoffHarness(t, cdn, "89")
	tr := teeTrack("89", "真断开", "某人")
	fake := h.it.registry.Put(tr)
	release := gateRelease(gate)
	t.Cleanup(release)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.it.Handle(w, r) {
			http.Error(w, "unhandled", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("连不上用例自己的服务：%v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET /music/api/v1/track/stream?guid=%s HTTP/1.1\r\nHost: localhost\r\n\r\n", fake); err != nil {
		t.Fatalf("发请求失败：%v", err)
	}
	// 读到一点就**粗暴**断开（还有没读完的数据 → 对面收到 RST）。
	buf := make([]byte, 64<<10)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("该先收到一段字节：%v", err)
	}
	_ = conn.Close()

	// 等拦截层真的把这次断开处理成「交接」：在途 1 就意味着请求路径已经返回、
	// 后台任务接手了，而 gate 钉着它不会跑掉 —— 断言因此不赌时序。
	waitFor(t, 10*time.Second, func() bool { return h.it.tee.inflight() == 1 })
	release()
	if !h.it.tee.drain(15 * time.Second) {
		t.Fatalf("后台续传该自己收尾（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 1 {
		// ⚠️ 这条断言是「区分度」的来源：如果拦截层其实把整首都读完了（没断开），
		// 那它会被正常提升，这条用例就会在这里红 —— 而不是假装通过。
		t.Fatalf("该走一次续传（Range 请求），实际 %d 次", n)
	}
	it, ok := h.it.downloaded.Get(fake)
	if !ok {
		t.Fatal("真实断开之后也该由后台补完并进登记表")
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("落盘文件必须完整：err=%v len=%d（期望 %d）", err, len(got), len(body))
	}
}

// ── 失败就是失败：续传不成 → 什么都不留 ─────────────────────────────────

// TestTeeHandoffFailureLeavesNothing 是「不许把不完整文件提升进曲库」那条硬规矩的用例。
//
// 每一种失败都必须走回旧语义：删掉半截、不登记、不补标签。只要有一条被提升，
// 那首歌就会永远播到一半停，而用户以为已经下好了。
func TestTeeHandoffFailureLeavesNothing(t *testing.T) {
	body := bytes.Repeat([]byte{0x5A}, 1<<20)
	cases := []struct {
		name     string
		cdn      *fakeCDN
		swapPool bool
	}{
		{"源不认 Range（回 200）", &fakeCDN{status: http.StatusOK}, false},
		{"源报 500", &fakeCDN{status: http.StatusInternalServerError}, false},
		{"没有 Content-Range 头", &fakeCDN{noContentRange: true}, false},
		{"Content-Range 总长对不上", &fakeCDN{totalOverride: int64(len(body)) + 7}, false},
		{"Content-Range 起点对不上", &fakeCDN{startOverride: 12345}, false},
		{"余量给不够就断", &fakeCDN{truncate: 1000}, false},
		{"续传时解析不出直链", &fakeCDN{}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cdn := tc.cdn
			cdn.body = append([]byte(nil), body...)
			h, dir, enrich := newHandoffHarness(t, cdn, "94")
			tr := teeTrack("94", "续传会失败的歌", "某人")
			fake := h.it.registry.Put(tr)

			var onAbort func()
			if tc.swapPool {
				// 断开的**那一刻**把解析池换成解不出直链的（真机上也正是「续传时
				// 直链已经失效」那种情形）。必须在这一刻换：换完才起后台任务，
				// 所以这里没有数据竞争。
				onAbort = func() {
					h.it.pool = online.NewPool(&stubResolver{
						platform: "wy", answers: map[string]*online.Resolved{},
					})
				}
			}
			h.streamAndAbort(t, fake, 64<<10, onAbort)

			if !h.it.tee.drain(10 * time.Second) {
				t.Fatalf("续传任务该自己收尾（在途 %d）", h.it.tee.inflight())
			}
			if _, ok := h.it.downloaded.Get(fake); ok {
				t.Fatal("⚠️ 续传失败绝不能留下登记条目")
			}
			if n := len(h.it.downloaded.All()); n != 0 {
				t.Fatalf("登记表该是空的，却有 %d 条", n)
			}
			if _, err := os.Stat(h.it.teeDestPath(tr, "mp3")); err == nil {
				t.Fatalf("续传失败绝不能落出曲库文件：%s", h.it.teeDestPath(tr, "mp3"))
			}
			if left := teeParts(t, dir); len(left) != 0 {
				t.Fatalf("续传失败该把半截删掉（留下的就是垃圾）：%v", left)
			}
			if enrich.Load() != 0 {
				t.Fatalf("失败时不该补标签，却调了 %d 次", enrich.Load())
			}
		})
	}
}

// TestTeeHandoffSkipsWhenUpstreamBroke 钉住「上游自己断了 → 不续传」。
//
// 判据只能从收尾时的那份现场看出来，所以在门口直接调收尾入口：
// 读侧出错 + 客户端还在 = 源坏了，按旧语义丢掉（追着坏源续传只是白占名额）。
func TestTeeHandoffSkipsWhenUpstreamBroke(t *testing.T) {
	h, dir := teeHarness(t)
	tr := teeTrack("93", "源断了的歌", "某人")
	fake := fakeID(93)
	resp := &http.Response{
		StatusCode: 200, ContentLength: 4096,
		Body: io.NopCloser(strings.NewReader("")),
	}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, nil)
	tt := h.it.beginTee(fake, tr, resp, req)
	if tt == nil {
		t.Fatal("该开 tee")
	}
	if _, err := (&teeWriter{w: io.Discard, t: tt}).Write(bytes.Repeat([]byte{1}, 2048)); err != nil {
		t.Fatalf("写暂存失败：%v", err)
	}
	h.it.finishTeeAfterStream(tt, fake, tr, resp.ContentLength, "mp3", teeInterrupt{readErr: io.ErrUnexpectedEOF})

	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("上游断了不该起续传，却在途 %d 个", n)
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("该按旧语义把半截丢掉：%v", left)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("不完整绝不能登记")
	}
}

// TestTeeHandoffSkipsWhenPartDoesNotMatchWritten 钉住「现场对不上就不猜」。
//
// 续传要拿 `bytes=<已写>-` 去要余量，前提是磁盘上那段前缀**正好**是「已写」那么长。
// 对不上（写盘中途出错、文件被谁动过）就整个放弃 —— 在错位的偏移上接着写，拼出来的
// 是个坏文件，而且长度检查还真不一定拦得住。
func TestTeeHandoffSkipsWhenPartDoesNotMatchWritten(t *testing.T) {
	body := bytes.Repeat([]byte{0xC3}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, dir, _ := newHandoffHarness(t, cdn, "92")
	tr := teeTrack("92", "现场对不上的歌", "某人")
	fake := h.it.registry.Put(tr)

	resp := &http.Response{
		StatusCode: 200, ContentLength: int64(len(body)),
		Body: io.NopCloser(strings.NewReader("")),
	}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, nil)
	tt := h.it.beginTee(fake, tr, resp, req)
	if tt == nil {
		t.Fatal("该开 tee")
	}
	if _, err := (&teeWriter{w: io.Discard, t: tt}).Write(bytes.Repeat([]byte{7}, 64<<10)); err != nil {
		t.Fatalf("写暂存失败：%v", err)
	}
	// 让磁盘上的长度与「已写」对不上（模拟写盘中途出错那类情形）。
	if err := os.Truncate(tt.part, 32<<10); err != nil {
		t.Fatalf("截断暂存失败：%v", err)
	}
	h.it.finishTeeAfterStream(tt, fake, tr, resp.ContentLength, "mp3", teeInterrupt{clientGone: true})

	if !h.it.tee.drain(10 * time.Second) {
		t.Fatalf("该收尾（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 0 {
		t.Fatalf("现场对不上就不该发续传请求，实际 %d 次", n)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("现场对不上时不能提升")
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("该把对不上的半截丢掉：%v", left)
	}
}

// TestTeeHandoffSkipsTinyInterruption 钉住「太短不续传」：
// 客户端连上就走了（写下 0 字节）时，续传等于整轨重下 —— 那不在这个功能的范围里。
func TestTeeHandoffSkipsTinyInterruption(t *testing.T) {
	body := bytes.Repeat([]byte{0x11}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, dir, _ := newHandoffHarness(t, cdn, "95")
	tr := teeTrack("95", "连上就走", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 0, nil) // after=0 → 第一个字节都没写出去

	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("写下 0 字节时不该起续传，却在途 %d 个", n)
	}
	if n := cdn.ranged.Load(); n != 0 {
		t.Fatalf("不该有续传请求，实际 %d 次", n)
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("该按旧语义把半截丢掉：%v", left)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("没下完不该登记")
	}
}

// ── 开关关着 / 开关被关掉：不引入后台续传 ───────────────────────────────

// TestTeeHandoffNeverAppliesToRollingCache 守住「开关关着时不要引入后台续传」。
//
// 那条路径只有最近 5 首临时试听缓存，半截丢掉是对的；为它起后台下载等于把
// 「不留永久曲库副本」偷换成「偷偷帮你下整曲」。
func TestTeeHandoffNeverAppliesToRollingCache(t *testing.T) {
	body := bytes.Repeat([]byte{0x22}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, dl := rollingHarness(t)
	swapMediaClient(t)
	srv := httptest.NewServer(cdn)
	t.Cleanup(srv.Close)
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"96": {URL: srv.URL + "/a.mp3", Format: "mp3", Size: int64(len(body))},
	}})
	tr := teeTrack("96", "开关关着的歌", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 64<<10, nil)

	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("开关关着时不该起后台续传，却在途 %d 个", n)
	}
	if n := cdn.ranged.Load(); n != 0 {
		t.Fatalf("开关关着时不该有任何续传请求，实际 %d 次", n)
	}
	if entries, err := os.ReadDir(h.it.teeRollingDir()); err == nil && len(entries) != 0 {
		t.Fatalf("半截试听缓存该被丢掉（与以前一样）：%v", entries)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("滚动缓存那条路绝不该进登记表")
	}
	if names := rollingComplete(t, dl); len(names) != 0 {
		t.Fatalf("曲库目录不该多出文件：%v", names)
	}
}

// TestTeeHandoffStopsWhenSwitchTurnsOff 钉住「开关关掉就不再起新下载」：
// 取流已经开始了，用户在播放途中把开关关掉 —— 那一刻断开就不该再起续传。
func TestTeeHandoffStopsWhenSwitchTurnsOff(t *testing.T) {
	body := bytes.Repeat([]byte{0x33}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, dir, _ := newHandoffHarness(t, cdn, "97")
	tr := teeTrack("97", "开着的时候开始听的", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 64<<10, func() {
		h.it.cfg.TeeEnabled = func() bool { return false }
	})

	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("开关已经关了，不该起续传，却在途 %d 个", n)
	}
	if n := cdn.ranged.Load(); n != 0 {
		t.Fatalf("开关已经关了，不该有续传请求，实际 %d 次", n)
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("该按旧语义丢掉半截：%v", left)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("开关关着时不该提升")
	}
}

// TestTeeHandoffDoesNotAdoptRollingPartWhenSwitchFlipsOn 钉住「滚动缓存那份半截
// 绝不会被后台续传偷渡进曲库」。
//
// 这条只在开关**中途被打开**时才看得见：取流开始时开关是关的（所以这份字节的归宿
// 是滚动缓存），用户随后把开关打开，然后客户端断开 —— 那时如果交接判断只看总开关、
// 不看这份字节的归宿，半截就会被当成曲库下载续上去、提升进曲库。
func TestTeeHandoffDoesNotAdoptRollingPartWhenSwitchFlipsOn(t *testing.T) {
	body := bytes.Repeat([]byte{0x88}, 1<<20)
	cdn := &fakeCDN{body: body}
	h, dl := rollingHarness(t) // 开关关着 → 这份字节的归宿是滚动缓存
	swapMediaClient(t)
	srv := httptest.NewServer(cdn)
	t.Cleanup(srv.Close)
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"98": {URL: srv.URL + "/a.mp3", Format: "mp3", Size: int64(len(body))},
	}})
	tr := teeTrack("98", "关着的时候听了一半", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 64<<10, func() {
		// 断开的那一刻开关被打开了 —— 这份 tee 的归宿仍然是滚动缓存。
		h.it.cfg.TeeEnabled = func() bool { return true }
	})

	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("滚动缓存那份半截绝不能被交接续传，却在途 %d 个", n)
	}
	if n := cdn.ranged.Load(); n != 0 {
		t.Fatalf("不该有续传请求，实际 %d 次", n)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("⚠️ 开关关着时留下的半截绝不能进登记表")
	}
	if names := rollingComplete(t, dl); len(names) != 0 {
		t.Fatalf("半截试听缓存该丢掉（与以前一样）：%v", names)
	}
	if entries, err := os.ReadDir(h.it.teeRollingDir()); err == nil && len(entries) != 0 {
		t.Fatalf("缓存目录该是空的：%v", entries)
	}
}

// ── 有界：并发上限 / 同曲去重 / 收尾 / 不阻塞下一个人 ────────────────────

// TestTeeHandoffIsBoundedAndDropsWhenFull 钉住并发上限：满了就**丢掉**，不排队。
func TestTeeHandoffIsBoundedAndDropsWhenFull(t *testing.T) {
	body := bytes.Repeat([]byte{0x44}, 1<<20)
	gate := make(chan struct{})
	cdn := &fakeCDN{body: body, gate: gate}
	h, dir, _ := newHandoffHarness(t, cdn, "80", "81")
	h.it.tee.capOverride = 1 // 上限调成 1：用例不必真起 3 首歌

	first := h.it.registry.Put(teeTrack("80", "第一首", "某人"))
	second := h.it.registry.Put(teeTrack("81", "第二首", "某人"))

	// 第一首：打断 → 交接（gate 把它钉在在途）。
	h.streamAndAbort(t, first, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("第一首该已经交接出去（在途 %d）", n)
	}
	// 第二首：名额满了 → 按旧语义丢掉半截，且**不排队**。
	h.streamAndAbort(t, second, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("在途任务数不能超过上限 1，得到 %d", n)
	}
	if _, ok := h.it.downloaded.Get(second); ok {
		t.Fatal("名额满时第二首不该被提升")
	}
	if left := teeParts(t, dir); len(left) != 1 {
		t.Fatalf("该只剩第一首那份暂存（第二首的被丢掉）：%v", left)
	}

	release := gateRelease(gate)
	t.Cleanup(release)
	release() // 放行被钉住的续传
	if !h.it.tee.drain(10 * time.Second) {
		t.Fatalf("放行之后第一首该补完（在途 %d）", h.it.tee.inflight())
	}
	it, ok := h.it.downloaded.Get(first)
	if !ok {
		t.Fatal("第一首该在放行之后补完并登记")
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("第一首该是完整文件：err=%v len=%d", err, len(got))
	}
	if _, ok := h.it.downloaded.Get(second); ok {
		t.Fatal("第二首自始至终不该进曲库")
	}
}

// TestTeeHandoffDropsDuplicateForSameTrack 钉住「同一条曲目只续一次」。
//
// 同一条曲目被两次取流都打断时（两台设备 / 狂点重播），两份暂存各自续传各自提升
// 只会互相盖掉，所以第二条按旧语义丢掉 —— 第一首补完，曲库里是完整的那一份。
func TestTeeHandoffDropsDuplicateForSameTrack(t *testing.T) {
	body := bytes.Repeat([]byte{0x55}, 1<<20)
	gate := make(chan struct{})
	cdn := &fakeCDN{body: body, gate: gate}
	h, dir, _ := newHandoffHarness(t, cdn, "82")
	tr := teeTrack("82", "被听了两遍", "某人")
	fake := h.it.registry.Put(tr)

	h.streamAndAbort(t, fake, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("第一次打断该交接出去（在途 %d）", n)
	}
	h.streamAndAbort(t, fake, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("同一条曲目不该有第二个续传任务，在途 %d", n)
	}
	if left := teeParts(t, dir); len(left) != 1 {
		t.Fatalf("该只剩第一份暂存：%v", left)
	}

	release := gateRelease(gate)
	t.Cleanup(release)
	release() // 放行被钉住的续传
	if !h.it.tee.drain(10 * time.Second) {
		t.Fatalf("该补完（在途 %d）", h.it.tee.inflight())
	}
	if n := cdn.ranged.Load(); n != 1 {
		t.Fatalf("只该有一次续传，实际 %d 次", n)
	}
	it, ok := h.it.downloaded.Get(fake)
	if !ok {
		t.Fatal("该登记一条")
	}
	got, err := os.ReadFile(it.Path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("曲库里该是完整的一份：err=%v len=%d", err, len(got))
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("补完之后不该留下暂存：%v", left)
	}
}

// TestTeeHandoffDoesNotBlockNextStream 钉住「不能拖累播放」。
//
// 把第一首的续传钉在「在途」，第二首（另一个人点的）必须照样完整取流、
// 而且不用等第一首 —— 后台续传不该占住播放路径的任何东西。
func TestTeeHandoffDoesNotBlockNextStream(t *testing.T) {
	body := bytes.Repeat([]byte{0x66}, 1<<20)
	gate := make(chan struct{})
	cdn := &fakeCDN{body: body, gate: gate}
	h, _, _ := newHandoffHarness(t, cdn, "83", "84")
	first := h.it.registry.Put(teeTrack("83", "在续传的歌", "某人"))
	second := h.it.registry.Put(teeTrack("84", "下一个人要听的", "某人"))

	h.streamAndAbort(t, first, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("第一首该在续传（在途 %d）", n)
	}
	release := gateRelease(gate)
	t.Cleanup(release)

	// 第二首：正常取流（不打断），必须完整 —— 而且是在 gate 还挡着的时候。
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+second, "", nil)
		done <- w
	}()
	select {
	case w := <-done:
		if !bytes.Equal(w.Body.Bytes(), body) {
			t.Fatalf("第二首该拿到完整字节，得到 %d 字节（期望 %d）", w.Body.Len(), len(body))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("⚠️ 第二首被在途的续传挡住了 —— 后台续传占住了播放路径")
	}
}

// TestCloseTeeStopsInFlightHandoff 钉住收尾：CloseTee 之后没有在途任务、
// 半截被删掉、而且**不再起新的**（收尾是单向动作）。
func TestCloseTeeStopsInFlightHandoff(t *testing.T) {
	body := bytes.Repeat([]byte{0x77}, 1<<20)
	gate := make(chan struct{})
	cdn := &fakeCDN{body: body, gate: gate}
	h, dir, _ := newHandoffHarness(t, cdn, "85", "86")
	first := h.it.registry.Put(teeTrack("85", "退出时还在续传", "某人"))
	second := h.it.registry.Put(teeTrack("86", "退出之后才听的", "某人"))

	h.streamAndAbort(t, first, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 1 {
		t.Fatalf("该有一个在途任务，得到 %d", n)
	}
	release := gateRelease(gate)
	t.Cleanup(release)

	h.it.CloseTee() // 取消 + 等在途任务收尾（最多 3 秒）
	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("CloseTee 该等任务收尾，还剩下 %d 个", n)
	}
	if left := teeParts(t, dir); len(left) != 0 {
		t.Fatalf("被取消的续传该把半截删掉（别给下次启动留垃圾）：%v", left)
	}
	if _, ok := h.it.downloaded.Get(first); ok {
		t.Fatal("被取消的续传不该产出登记条目")
	}

	// 收尾之后再来的中断：不再起任务（旧语义：丢掉）。
	h.streamAndAbort(t, second, 64<<10, nil)
	if n := h.it.tee.inflight(); n != 0 {
		t.Fatalf("CloseTee 之后不该再起续传任务，得到 %d", n)
	}
}

// TestTeeHandoffValuesArePinned 钉住这两个取值的**决定**（理由见 tee.go）。
//
// 改它们必须是次有意识的编辑：改完这里会红，改的人就得顺手回答「为什么」。
func TestTeeHandoffValuesArePinned(t *testing.T) {
	if teeHandoffMax != 2 {
		t.Fatalf("并发上限该是 2（理由见 tee.go 的 teeHandoffMax 注释），得到 %d", teeHandoffMax)
	}
	if teeHandoffMax > 4 {
		t.Fatalf("这是**额外**的外网流量，与下一首歌抢同一条上行，不能更大：%d", teeHandoffMax)
	}
	if teeHandoffTimeout < time.Minute || teeHandoffTimeout > 30*time.Minute {
		t.Fatalf("单首时限该在 1–30 分钟之间（太短会掐掉慢源，太长会占着名额）：%v", teeHandoffTimeout)
	}
	if teeHandoffMinBytes < 1024 {
		t.Fatalf("下限不该低于参考实现的 1KiB（再低就等于整轨重下）：%d", teeHandoffMinBytes)
	}
}

// TestParseContentRange 是续传那条验长度规矩的解析器。
//
// 它必须**只认一种形状**：解析不出来就放弃续传（不猜），否则「续的是不是同一个
// 文件」就无从判起。
func TestParseContentRange(t *testing.T) {
	ok := []struct {
		in           string
		start, total int64
	}{
		{"bytes 0-99/100", 0, 100},
		{"bytes 4096-10239/10240", 4096, 10240},
		{"BYTES  4096-10239/10240 ", 4096, 10240},
	}
	for _, tc := range ok {
		start, total, got := parseContentRange(tc.in)
		if !got || start != tc.start || total != tc.total {
			t.Errorf("parseContentRange(%q) = (%d,%d,%v)，期望 (%d,%d,true)", tc.in, start, total, got, tc.start, tc.total)
		}
	}
	bad := []string{"", "bytes 4096-10239", "bytes */10240", "items 0-99/100", "bytes abc-1/2", "bytes 0-99/0", "bytes 0-99/x"}
	for _, in := range bad {
		if _, _, got := parseContentRange(in); got {
			t.Errorf("parseContentRange(%q) 不该被认成有效（宁可放弃续传，不猜）", in)
		}
	}
}
