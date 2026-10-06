package intercept

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// ── 测试替身 ────────────────────────────────────────────────────────────

// fakeUpstream 代替官方后端。
//
// 真实的 Upstream 是「经 Unix socket 打到官方 daemon」，单测不能依赖它。
// 这里让每个用例直接给出「这个方法+路径返回什么」，就能精确构造出
// 「官方返回了什么」这个前提。
type fakeUpstream struct {
	mu   sync.Mutex
	seen []string
	fn   func(r *http.Request, body []byte) *http.Response
}

func (f *fakeUpstream) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	f.mu.Lock()
	f.seen = append(f.seen, req.Method+" "+req.URL.Path)
	f.mu.Unlock()
	if f.fn == nil {
		return jsonResp(http.StatusNotFound, `{"code":100005,"msg":"NotFound","data":null}`), nil
	}
	return f.fn(req, body), nil
}

func (f *fakeUpstream) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// stubResolver 代替真实的直链解析器（真实实现要打网易/QQ 的外网接口）。
type stubResolver struct {
	platform string
	answers  map[string]*online.Resolved
}

func (s *stubResolver) Platform() string { return s.platform }

func (s *stubResolver) Resolve(ctx context.Context, id string) (*online.Resolved, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r, ok := s.answers[id]; ok {
		return r, nil
	}
	return nil, online.ErrNotPlayable
}

func (s *stubResolver) ResolveBatch(ctx context.Context, ids []string) (map[string]*online.Resolved, error) {
	// ⚠️ **必须如实模拟「ctx 取消就失败」**：真实解析器都要发网络请求，ctx 取消时
	// 它们就是会失败。假解析器忽略 ctx 的话，「拿一个已过期的 ctx 去解析」这类 bug
	// 在单测里永远测不出来 —— 真机上就漏过去过一次（合并超预算 → 一首在线歌都不出）。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]*online.Resolved, len(ids))
	for _, id := range ids {
		if r, ok := s.answers[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// harness 是一次测试用的拦截层。
type harness struct {
	it  *Interceptor
	up  *fakeUpstream
	dir string

	mu       sync.Mutex
	online   []search.UnifiedSong
	playable map[string]bool
	lyric    string
	nm       []online.Track

	// 榜单（见 vchart.go）：内容与「勾了哪些榜」都走替身。
	chart      []online.Track
	chartName  string
	chartErr   error
	chartCalls int
	charts     []string
	// chartsOff = 榜单注入总开关被关掉（名单仍在 `charts` 里）。
	chartsOff bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{playable: map[string]bool{}}
	h.dir = t.TempDir()
	h.up = &fakeUpstream{fn: func(r *http.Request, body []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
	}}
	h.it = New(Config{
		DataDir:   h.dir,
		Upstream:  h.up,
		Logf:      t.Logf,
		Platforms: []string{"wy"},
		Searcher: func(keyword, platform string, page, size int) []search.UnifiedSong {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]search.UnifiedSong(nil), h.online...)
		},
		Pool: online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{}}),
		LyricFetcher: func(online.Track) string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.lyric
		},
		// 必须注入：真实实现要打 music.163.com。而推荐歌单会在**每次列歌单时**
		// 被算一次 —— 不注入的话整个套件都会依赖外网，一次网络抖动就全红。
		NeteasePlaylist: func(_ context.Context, id string, limit int) (string, string, []online.Track) {
			h.mu.Lock()
			defer h.mu.Unlock()
			tracks := append([]online.Track(nil), h.nm...)
			if limit > 0 && len(tracks) > limit {
				tracks = tracks[:limit]
			}
			if len(tracks) == 0 {
				return "", "", nil
			}
			return "网易歌单 " + id, "https://cdn.invalid/cover.jpg", tracks
		},
		// 榜单内容同样必须注入：真实实现走本机回环 `/api/charts/detail`，
		// 而它同样会在**每次列歌单时**被算一次 —— 不注入就等于让套件依赖
		// 自己那个 HTTP 端口，还会顺手打真网络。
		ChartDetail: func(_ context.Context, source, id string, limit int) (string, string, []online.Track, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.chartCalls++
			if h.chartErr != nil {
				return "", "", nil, h.chartErr
			}
			tracks := append([]online.Track(nil), h.chart...)
			if limit > 0 && len(tracks) > limit {
				tracks = tracks[:limit]
			}
			if len(tracks) == 0 {
				return "", "", nil, nil
			}
			name := h.chartName
			if name == "" {
				name = "榜单 " + id
			}
			return name, "https://cdn.invalid/chart-" + id + ".jpg", tracks, nil
		},
		// 故意做成函数：榜单管理页保存后要立刻生效，「改完得重启」是 bug。
		ChartPlaylists: func() []string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]string(nil), h.charts...)
		},
		// 总开关默认开（与 `AppConfig.ChartsInjectEnabled` 的「没设置过 = 开」一致）。
		ChartPlaylistsEnabled: func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return !h.chartsOff
		},
	})
	if h.it == nil {
		t.Fatal("New 返回 nil")
	}
	return h
}

// setNetease 设定「网易公开歌单」返回什么（推荐歌单 / 热门榜单的曲目来源）。
//
// 在 setOnline 之后调用：它会把这些曲目的 id 一起登记成「可解析」，否则
// 推荐歌单里的歌会被 `playableOnly` 全部滤掉，测试就只能看到空歌单。
func (h *harness) setNetease(tracks ...online.Track) {
	h.mu.Lock()
	if h.playable == nil {
		h.playable = map[string]bool{}
	}
	h.nm = tracks
	for _, t := range tracks {
		h.playable[t.PlatformID] = true
	}
	playable := h.playable
	h.mu.Unlock()
	h.it.pool = poolFrom(playable)
}

// poolFrom 按「哪些平台内 id 能解析出直链」造一个解析池替身。
func poolFrom(playable map[string]bool) *online.Pool {
	answers := map[string]*online.Resolved{}
	for id, ok := range playable {
		if ok {
			answers[id] = &online.Resolved{
				URL: "https://cdn.invalid/" + id + ".mp3", Format: "mp3", Size: 4096,
			}
		}
	}
	return online.NewPool(&stubResolver{platform: "wy", answers: answers})
}

// setOnline 设定在线搜索返回什么，以及其中哪些能解析出直链。
func (h *harness) setOnline(playable map[string]bool, songs ...search.UnifiedSong) {
	h.mu.Lock()
	h.online = songs
	h.playable = playable
	h.mu.Unlock()
	h.it.pool = poolFrom(playable)
}

func (h *harness) setLyric(text string) {
	h.mu.Lock()
	h.lyric = text
	h.mu.Unlock()
	// 歌词是带 6 小时缓存的。测试里改了歌词必须把缓存清掉，否则第一次
	// 「无歌词」的空结果会把后面的断言全部按在地上。
	h.it.lyricMu.Lock()
	h.it.lyricCache = map[string]lyricEntry{}
	h.it.lyricMu.Unlock()
}

// setOfficial 设定官方后端对某个「方法 路径」返回什么。
func (h *harness) setOfficial(route string, status int, body string) {
	prev := h.up.fn
	h.up.fn = func(r *http.Request, in []byte) *http.Response {
		if r.Method+" "+r.URL.Path == route {
			return jsonResp(status, body)
		}
		return prev(r, in)
	}
}

func (h *harness) setOfficialFunc(fn func(r *http.Request, body []byte) *http.Response) {
	h.up.fn = fn
}

// setCharts 设定「榜单管理里勾了哪些榜」（`<source>_<id>` 形态）。
func (h *harness) setCharts(ids ...string) {
	h.mu.Lock()
	h.charts = ids
	h.mu.Unlock()
}

// setChartsOff 拨总开关。关掉时**名单不清空** —— 这正是它与
// `setCharts()` 的区别，也是总开关值得单独存在的理由。
func (h *harness) setChartsOff(off bool) {
	h.mu.Lock()
	h.chartsOff = off
	h.mu.Unlock()
}

// setChartTracks 设定榜单接口返回什么曲目。
//
// 与 `setNetease` 不同，这里**不**顺手把这些 id 登记成可解析 —— 榜单可能是
// 多平台的，池子得由用例自己按平台搭（见 vchart_test.go 的 chartResolverPool）。
func (h *harness) setChartTracks(tracks ...online.Track) {
	h.mu.Lock()
	h.chart = tracks
	h.mu.Unlock()
}

// chartFetchCalls 返回榜单接口被调了几次（用来验「拉失败会不会打上游」）。
func (h *harness) chartFetchCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chartCalls
}

// call 直接对某个拦截层实例发请求，返回 recorder 与 Handle 的返回值。
func call(it *Interceptor, method, target string, body string, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handled := it.Handle(w, r)
	return w, handled
}

// do 发一个请求给本 harness 的拦截层。
func (h *harness) do(method, target string, body string, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	return call(h.it, method, target, body, headers)
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%v\n%s", err, w.Body.String())
	}
	return out
}

func dataOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	env := decode(t, w)
	d, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data 不是对象：%v", env["data"])
	}
	return d
}

func song(id, title, artist string) search.UnifiedSong {
	return search.UnifiedSong{
		ID: id, Songmid: id, Name: title, Singer: artist, Album: "专辑", Duration: 240,
	}
}

// officialGuid 造一个像官方 guid 的 32 位 hex。
func officialGuid(seed string) string {
	return strings.Repeat(seed, 32/len(seed))[:32]
}

// ── 路由 ────────────────────────────────────────────────────────────────

func TestHandleIgnoresPathsOutsideApiPrefix(t *testing.T) {
	h := newHarness(t)
	w, handled := h.do(http.MethodGet, "/music/static/index.html", "", nil)
	if handled {
		t.Fatalf("apiPrefix 之外的路径不该被认领（写了 %d 字节）", w.Body.Len())
	}
}

func TestHandleClaimsUnclaimedApiPath(t *testing.T) {
	h := newHarness(t)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"x":1}}`)
	})
	w, handled := h.do(http.MethodGet, "/music/api/v1/some/brand/new/endpoint", "", nil)
	if !handled {
		t.Fatal("兜底路由应认领 /music/api/v1 下的未拦截路径")
	}
	if !strings.Contains(w.Body.String(), `"x":1`) {
		t.Fatalf("未拦截端点应原样透传，得到 %s", w.Body.String())
	}
}

// ── 搜索合并 ────────────────────────────────────────────────────────────

func TestSearchMergesOfficialFirstThenOnline(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true},
		song("1", "在线歌一", "歌手甲"),
		song("2", "在线歌二", "歌手乙"),
	)
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[`+
			`{"guid":"`+officialGuid("a")+`","title":"本地一","artist":"本地歌手"},`+
			`{"guid":"`+officialGuid("b")+`","title":"本地二","artist":"本地歌手"}],`+
			`"total":2}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=测试&page=1&size=50", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	if len(list) != 4 {
		t.Fatalf("应有 4 条（2 本地 + 2 在线），得到 %d", len(list))
	}
	if d["total"] != float64(4) {
		t.Fatalf("total 应为 4，得到 %v", d["total"])
	}
	// 顺序：官方在前。
	if list[0].(map[string]any)["title"] != "本地一" {
		t.Fatalf("第一条应是官方曲目，得到 %v", list[0].(map[string]any)["title"])
	}
	if list[3].(map[string]any)["title"] != "在线歌二" {
		t.Fatalf("最后一条应是在线曲目，得到 %v", list[3].(map[string]any)["title"])
	}
	// 在线条目的 guid 必须是 32 位 hex 虚拟 id，不能漏出 online: 形态。
	onlineVO := list[2].(map[string]any)
	if !online.IsHex32(onlineVO["guid"].(string)) {
		t.Fatalf("在线条目 guid 应是虚拟 id，得到 %v", onlineVO["guid"])
	}
	if strings.Contains(w.Body.String(), "online:") {
		t.Fatal("响应里漏出了真实 id 前缀 online: —— 结构化映射没做干净")
	}
}

func TestSearchOnlyInjectsPlayable(t *testing.T) {
	h := newHarness(t)
	// 三条候选，只有一条能解析出直链。
	h.setOnline(map[string]bool{"1": true},
		song("1", "能播", "甲"),
		song("2", "不能播", "乙"),
		song("3", "也不能播", "丙"),
	)
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=x", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	if len(list) != 1 {
		t.Fatalf("只该注入能播的那 1 条，得到 %d", len(list))
	}
	// total 必须是**实际可播条数**，不是「查到的条数」。
	if d["total"] != float64(1) {
		t.Fatalf("total 应为实际可播条数 1，得到 %v", d["total"])
	}
}

func TestSearchDropsTracksAlreadyInLocalLibrary(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true},
		song("1", "晴天", "周杰伦"), // 与本地重复
		song("2", "稻香", "周杰伦"), // 本地没有
	)
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[`+
			`{"guid":"`+officialGuid("c")+`","title":"晴天","artist":"周杰伦"}],`+
			`"total":1}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=周杰伦", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	if len(list) != 2 {
		t.Fatalf("应剩 2 条（本地 1 + 在线 1），得到 %d", len(list))
	}
	if list[1].(map[string]any)["title"] != "稻香" {
		t.Fatalf("被去重的应是「晴天」，得到 %v", list[1].(map[string]any)["title"])
	}
}

func TestSearchPassthroughWhenOfficialErrors(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, song("1", "在线歌", "甲"))
	// 官方返回业务错误码 → 不该注入，原样返回。
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":100001,"msg":"unknown error","data":null}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=x", "", nil)
	env := decode(t, w)
	if env["code"] != float64(100001) {
		t.Fatalf("官方错误码应原样返回，得到 %v", env["code"])
	}
	if strings.Contains(w.Body.String(), "在线歌") {
		t.Fatal("官方报错时不该注入在线结果")
	}
}

func TestSearchPassthroughWithoutKeyword(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, song("1", "在线歌", "甲"))
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track", "", nil)
	if strings.Contains(w.Body.String(), "在线歌") {
		t.Fatal("没有关键词时不该注入在线结果")
	}
}

func TestSearchOutOfRangePageDropsLocalSegment(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true},
		song("1", "在线一", "甲"), song("2", "在线二", "乙"))
	// 本地只有 2 首，请求第 3 页（size=2）→ 官方会把第 1 页钳回来。
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[`+
			`{"guid":"`+officialGuid("d")+`","title":"本地一","artist":"A"},`+
			`{"guid":"`+officialGuid("e")+`","title":"本地二","artist":"B"}],`+
			`"total":2}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=x&page=3&size=2", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	for _, raw := range list {
		title := raw.(map[string]any)["title"]
		if title == "本地一" || title == "本地二" {
			t.Fatalf("越界页不该重复显示第 1 页的本地曲目，得到 %v", title)
		}
	}
	if len(list) != 0 {
		t.Fatalf("本地 2 首、在线 2 首，第 3 页应为空，得到 %d 条", len(list))
	}
}

func TestSearchPageTwoShowsOnlineSegment(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true, "3": true},
		song("1", "在线一", "甲"), song("2", "在线二", "乙"), song("3", "在线三", "丙"))
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[`+
			`{"guid":"`+officialGuid("f")+`","title":"本地一","artist":"A"},`+
			`{"guid":"`+officialGuid("0")+`","title":"本地二","artist":"B"}],`+
			`"total":2}}`)

	// 本地段占下标 [0,2)，在线段紧随。第 2 页 size=2 → 在线段 [0:2)。
	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=x&page=2&size=2", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	if len(list) != 2 {
		t.Fatalf("第 2 页应有 2 条在线曲目，得到 %d", len(list))
	}
	if list[0].(map[string]any)["title"] != "在线一" {
		t.Fatalf("第 2 页第一条应是「在线一」，得到 %v", list[0].(map[string]any)["title"])
	}
}

func TestSearchSlicesWhenOfficialIgnoresSize(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{})
	// 官方忽略 size，一次给了 4 条。
	h.setOfficial("GET /music/api/v1/search/track", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[`+
			`{"guid":"`+officialGuid("1")+`","title":"一","artist":"A"},`+
			`{"guid":"`+officialGuid("2")+`","title":"二","artist":"B"},`+
			`{"guid":"`+officialGuid("3")+`","title":"三","artist":"C"},`+
			`{"guid":"`+officialGuid("4")+`","title":"四","artist":"D"}],`+
			`"total":4}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/track?keyword=x&page=2&size=2", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)

	if len(list) != 2 {
		t.Fatalf("应按请求窗口切成 2 条，得到 %d", len(list))
	}
	if list[0].(map[string]any)["title"] != "三" {
		t.Fatalf("第 2 页第一条应是「三」，得到 %v", list[0].(map[string]any)["title"])
	}
}

// ── 取流 ────────────────────────────────────────────────────────────────

func TestStreamPassesThroughOfficialGuid(t *testing.T) {
	h := newHarness(t)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"audio/mpeg"}},
			Body:       io.NopCloser(strings.NewReader("LOCAL-AUDIO")),
		}
	})
	w, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+officialGuid("9"), "", nil)
	if w.Body.String() != "LOCAL-AUDIO" {
		t.Fatalf("官方 guid 应原样透传，得到 %q", w.Body.String())
	}
}

func TestStreamProxiesOnlineMedia(t *testing.T) {
	// 第三方 CDN 用一个真 httptest server —— mediaClient 是真实 HTTP 客户端。
	var gotRange string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("ABCD"))
	}))
	defer cdn.Close()

	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, song("1", "在线歌", "甲"))
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(track)
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"1": {URL: cdn.URL + "/1.mp3", Format: "mp3", Size: 8},
	}})

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, "",
		map[string]string{"Range": "bytes=0-3"})

	if gotRange != "bytes=0-3" {
		t.Fatalf("Range 必须转发给 CDN，得到 %q", gotRange)
	}
	if w.Code != http.StatusPartialContent {
		t.Fatalf("应回 206，得到 %d", w.Code)
	}
	if w.Body.String() != "ABCD" {
		t.Fatalf("媒体内容不对：%q", w.Body.String())
	}
	if w.Header().Get("Content-Range") != "bytes 0-3/8" {
		t.Fatalf("Content-Range 应透传，得到 %q", w.Header().Get("Content-Range"))
	}
}

func TestStreamReturns404WhenUnplayable(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "zzz", Title: "不能播"}
	fake := h.it.registry.Put(track)
	// 解析池里没有 zzz → ErrNotPlayable
	h.it.pool = online.NewPool(&stubResolver{platform: "wy"})

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+fake, "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不可播应回 404，得到 %d", w.Code)
	}
	env := decode(t, w)
	if env["msg"] != "online source unavailable" {
		t.Fatalf("错误文案应是官方认识的形状，得到 %v", env["msg"])
	}
}

// ── 元数据 / 歌词 ───────────────────────────────────────────────────────

func TestMetadataForOnlineTrack(t *testing.T) {
	h := newHarness(t)
	h.setLyric("[00:01.00]晴天")
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "晴天", Artists: []string{"周杰伦"}, Duration: 269}
	fake := h.it.registry.Put(track)

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/metadata?guid="+fake, "", nil)
	d := dataOf(t, w)

	inner, ok := d["track"].(map[string]any)
	if !ok {
		t.Fatalf("data.track 必须是对象，得到 %T", d["track"])
	}
	if inner["title"] != "晴天" {
		t.Fatalf("标题不对：%v", inner["title"])
	}
	if _, ok := inner["genres"].([]any); !ok {
		t.Fatalf("data.track.genres 必须是数组，得到 %T", inner["genres"])
	}
	if _, ok := inner["album"].(map[string]any); !ok {
		t.Fatalf("data.track.album 必须是对象，得到 %T", inner["album"])
	}
	if d["hasLyric"] != true {
		t.Fatal("有歌词时 hasLyric 应为 true")
	}
}

func TestMetadataPassesThroughForOfficialGuid(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("GET /music/api/v1/track/metadata", http.StatusOK,
		`{"code":0,"msg":"","data":{"guid":"`+officialGuid("7")+`","local":true}}`)
	w, _ := h.do(http.MethodGet, "/music/api/v1/track/metadata?guid="+officialGuid("7"), "", nil)
	if !strings.Contains(w.Body.String(), `"local":true`) {
		t.Fatalf("官方 guid 应透传，得到 %s", w.Body.String())
	}
}

func TestLyricEndpoints(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "晴天", Artists: []string{"周杰伦"}}
	fake := h.it.registry.Put(track)

	// 无歌词 → 空列表 + preferred 空串。
	w, _ := h.do(http.MethodGet, "/music/api/v1/lyric/list?guid="+fake, "", nil)
	d := dataOf(t, w)
	if list := d["list"].([]any); len(list) != 0 {
		t.Fatalf("无歌词应给空列表，得到 %v", list)
	}

	// 有歌词。
	h.setLyric("[00:01.00]晴天")
	w, _ = h.do(http.MethodGet, "/music/api/v1/lyric/list?guid="+fake, "", nil)
	d = dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("应有 1 条歌词，得到 %d", len(list))
	}
	if list[0].(map[string]any)["guid"] != fake+":"+online.SubKindLyric {
		t.Fatalf("歌词 guid 不对：%v", list[0].(map[string]any)["guid"])
	}

	// track/lyrics 取正文。
	w, _ = h.do(http.MethodGet, "/music/api/v1/track/lyrics?guid="+fake, "", nil)
	d = dataOf(t, w)
	if d["lyric"] != "[00:01.00]晴天" {
		t.Fatalf("歌词正文不对：%v", d["lyric"])
	}

	// detail/lyrics/{guid} 子路径形态。
	w, _ = h.do(http.MethodGet, "/music/api/v1/detail/lyrics/"+fake, "", nil)
	d = dataOf(t, w)
	if d["lyric"] != "[00:01.00]晴天" {
		t.Fatalf("子路径形态取歌词失败：%v", d)
	}

	// 无歌词时 track/lyrics 回 `data:{}`（不是 {guid, lyric:""}）。
	h.setLyric("")
	w, _ = h.do(http.MethodGet, "/music/api/v1/track/lyrics?guid="+fake, "", nil)
	d = dataOf(t, w)
	if len(d) != 0 {
		t.Fatalf("无歌词时 data 应为空对象，得到 %v", d)
	}
}

// ── 收藏 ────────────────────────────────────────────────────────────────

func TestFavoriteCreateDeleteList(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(track)

	// 官方收藏列表里有一条本地收藏。
	h.setOfficial("GET /music/api/v1/favorite-track/list", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("a")+`","title":"本地收藏"}],"total":1}}`)

	// 加在线收藏。
	w, _ := h.do(http.MethodPost, "/music/api/v1/favorite-track/create",
		`{"trackGUID":"`+fake+`"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("收藏应成功，得到 %v", env)
	}

	// 列表：官方在前、在线在后。
	w, _ = h.do(http.MethodGet, "/music/api/v1/favorite-track/list", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("应有 2 条，得到 %d", len(list))
	}
	if list[0].(map[string]any)["title"] != "本地收藏" {
		t.Fatalf("官方收藏应在前，得到 %v", list[0].(map[string]any)["title"])
	}
	if list[1].(map[string]any)["title"] != "在线歌" {
		t.Fatalf("在线收藏应在后，得到 %v", list[1].(map[string]any)["title"])
	}
	if d["total"] != float64(2) {
		t.Fatalf("total 应为 2，得到 %v", d["total"])
	}

	// 删。
	w, _ = h.do(http.MethodPost, "/music/api/v1/favorite-track/delete",
		`{"trackGUID":"`+fake+`"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("取消收藏应成功，得到 %v", env)
	}
	w, _ = h.do(http.MethodGet, "/music/api/v1/favorite-track/list", "", nil)
	d = dataOf(t, w)
	if len(d["list"].([]any)) != 1 {
		t.Fatalf("取消后应剩 1 条，得到 %d", len(d["list"].([]any)))
	}
}

func TestFavoritePassthroughForOfficialGuid(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("POST /music/api/v1/favorite-track/create", http.StatusOK,
		`{"code":0,"msg":"OFFICIAL-WROTE","data":null}`)
	w, _ := h.do(http.MethodPost, "/music/api/v1/favorite-track/create",
		`{"trackGUID":"`+officialGuid("b")+`"}`, nil)
	if !strings.Contains(w.Body.String(), "OFFICIAL-WROTE") {
		t.Fatalf("官方 guid 的收藏应转发给官方，得到 %s", w.Body.String())
	}
}

func TestFavoritesAreIsolatedPerUser(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(track)

	userA := map[string]string{"Cookie": "sid=alice"}
	userB := map[string]string{"Cookie": "sid=bob"}

	if _, _ = h.do(http.MethodPost, "/music/api/v1/favorite-track/create",
		`{"trackGUID":"`+fake+`"}`, userA); false {
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/favorite-track/list", "", userB)
	d := dataOf(t, w)
	if len(d["list"].([]any)) != 0 {
		t.Fatalf("bob 不该看到 alice 的收藏，得到 %d 条", len(d["list"].([]any)))
	}

	w, _ = h.do(http.MethodGet, "/music/api/v1/favorite-track/list", "", userA)
	d = dataOf(t, w)
	if len(d["list"].([]any)) != 1 {
		t.Fatalf("alice 应看到自己的 1 条收藏，得到 %d 条", len(d["list"].([]any)))
	}
}

// ── 歌单 ────────────────────────────────────────────────────────────────

func TestPlaylistAddTrackOfficialFirstThenLocal(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(track)

	var sawOfficialBody string
	h.setOfficialFunc(func(r *http.Request, body []byte) *http.Response {
		if r.URL.Path == "/music/api/v1/playlist/add-track" {
			sawOfficialBody = string(body)
			return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":null}`)
		}
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
	})

	officialTrack := officialGuid("c")
	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"playlist-1","trackGUIDs":["`+officialTrack+`","`+fake+`"]}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("添加应成功，得到 %v", env)
	}
	// 转给官方的 body 里只能有官方 guid。
	if !strings.Contains(sawOfficialBody, officialTrack) {
		t.Fatalf("官方批次应转发给官方，body=%s", sawOfficialBody)
	}
	if strings.Contains(sawOfficialBody, fake) {
		t.Fatalf("在线 guid 不该转给官方（官方不认识它），body=%s", sawOfficialBody)
	}
	// 本地桶里应有一条。
	if got := h.it.store.PlaylistTracks("shared", "playlist-1"); len(got) != 1 {
		t.Fatalf("本地应记录 1 条，得到 %d", len(got))
	}
}

func TestPlaylistAddTrackOfficialFailureWritesNothing(t *testing.T) {
	h := newHarness(t)
	track := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(track)

	// 官方批次失败。
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		if r.URL.Path == "/music/api/v1/playlist/add-track" {
			return jsonResp(http.StatusOK, `{"code":160001,"msg":"PlaylistNameExists","data":null}`)
		}
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":null}`)
	})

	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"playlist-1","trackGUIDs":["`+officialGuid("d")+`","`+fake+`"]}`, nil)
	if env := decode(t, w); env["code"] != float64(160001) {
		t.Fatalf("应把官方的错误原样返回，得到 %v", env)
	}
	// 关键：官方失败时本地一条都不能写，否则会出现「官方报错但歌单多了歌」。
	if got := h.it.store.PlaylistTracks("shared", "playlist-1"); len(got) != 0 {
		t.Fatalf("官方批次失败时本地不该写入，得到 %d 条", len(got))
	}
}

func TestPlaylistTrackListPaginationAndAll(t *testing.T) {
	h := newHarness(t)
	// 官方歌单有 2 首。**官方自己会分页**：total 恒为 2，list 只给当前页。
	// 所以拦截层不该再去裁官方那一段，只负责把在线附加条目接到窗口里。
	local := []map[string]any{
		{"guid": officialGuid("1"), "title": "本地一", "artist": "A"},
		{"guid": officialGuid("2"), "title": "本地二", "artist": "B"},
	}
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		if r.URL.Path != "/music/api/v1/track/playlist-detail/list" {
			return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
		}
		q := r.URL.Query()
		size := atoiSigned(q.Get("size"), 50)
		page := atoiDefault(q.Get("page"), 1)
		var pageItems []map[string]any
		if size < 0 {
			pageItems = local
		} else {
			start := (page - 1) * size
			end := start + size
			if start < len(local) {
				if end > len(local) {
					end = len(local)
				}
				pageItems = local[start:end]
			}
		}
		body, _ := json.Marshal(map[string]any{
			"code": 0, "msg": "",
			"data": map[string]any{"list": pageItems, "total": len(local)},
		})
		return jsonResp(http.StatusOK, string(body))
	})

	// 加 2 首在线曲目。
	for _, id := range []string{"p1", "p2"} {
		tr := online.Track{Platform: "wy", PlatformID: id, Title: "在线" + id, Artists: []string{"甲"}}
		fake := h.it.registry.Put(tr)
		w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
			`{"guid":"pl","trackGUIDs":["`+fake+`"]}`, nil)
		if env := decode(t, w); env["code"] != float64(0) {
			t.Fatalf("%s 添加失败：%v", id, env)
		}
	}

	// size = -1 表示全量：4 条都要出来。
	w, _ := h.do(http.MethodGet, "/music/api/v1/track/playlist-detail/list?playlistGUID=pl&page=1&size=-1", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 4 {
		t.Fatalf("size=-1 应返回全量 4 条，得到 %d", len(list))
	}
	if d["total"] != float64(4) {
		t.Fatalf("total 应为 4，得到 %v", d["total"])
	}
	if list[0].(map[string]any)["title"] != "本地一" {
		t.Fatalf("官方曲目应在前，得到 %v", list[0].(map[string]any)["title"])
	}

	// size=2 第 2 页 → 官方段已空，在线段占满本页 [0:2)。
	w, _ = h.do(http.MethodGet, "/music/api/v1/track/playlist-detail/list?playlistGUID=pl&page=2&size=2", "", nil)
	d = dataOf(t, w)
	list = d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("第 2 页应只给在线段 2 条，得到 %d", len(list))
	}
	if !strings.HasPrefix(list[0].(map[string]any)["title"].(string), "在线") {
		t.Fatalf("第 2 页应是在线段，得到 %v", list[0].(map[string]any)["title"])
	}
	if d["total"] != float64(4) {
		t.Fatalf("第 2 页 total 仍是总数 4，得到 %v", d["total"])
	}

	// 第 3 页越界 → 空。
	w, _ = h.do(http.MethodGet, "/music/api/v1/track/playlist-detail/list?playlistGUID=pl&page=3&size=2", "", nil)
	d = dataOf(t, w)
	if len(d["list"].([]any)) != 0 {
		t.Fatalf("越界页应为空，得到 %d 条", len(d["list"].([]any)))
	}
}

func TestPlaylistListBumpsTrackCount(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"pl","name":"我的歌单","trackCount":2}],"total":1}}`)

	tr := online.Track{Platform: "wy", PlatformID: "x", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)
	_, _ = h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"pl","trackGUIDs":["`+fake+`"]}`, nil)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("应有 1 个歌单，得到 %d", len(list))
	}
	if list[0].(map[string]any)["trackCount"] != float64(3) {
		t.Fatalf("trackCount 应为 2+1=3，得到 %v", list[0].(map[string]any)["trackCount"])
	}
}

func TestPlaylistDeleteCascades(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("POST /music/api/v1/playlist/delete", http.StatusOK, `{"code":0,"msg":"","data":null}`)

	tr := online.Track{Platform: "wy", PlatformID: "x", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)
	_, _ = h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"pl","trackGUIDs":["`+fake+`"]}`, nil)
	if got := h.it.store.PlaylistTracks("shared", "pl"); len(got) != 1 {
		t.Fatalf("前置条件不成立：本地应有 1 条，得到 %d", len(got))
	}

	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/delete", `{"guid":"pl"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("删歌单应成功，得到 %v", env)
	}
	if got := h.it.store.PlaylistTracks("shared", "pl"); len(got) != 0 {
		t.Fatalf("删歌单应级联清掉本地条目，仍剩 %d 条", len(got))
	}
}

// ── 播放历史 ────────────────────────────────────────────────────────────

func TestEventReportRecordsPlay(t *testing.T) {
	h := newHarness(t)
	tr := online.Track{Platform: "wy", PlatformID: "1", Title: "在线歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)

	w, _ := h.do(http.MethodPost, "/music/api/v1/event/report",
		`{"eventType":"track_play","trackGUID":"`+fake+`"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("上报应成功，得到 %v", env)
	}
	if got := h.it.store.History("shared"); len(got) != 1 {
		t.Fatalf("应记 1 条播放历史，得到 %d", len(got))
	}
}

func TestEventReportPassthroughForNonPlayEvents(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("POST /music/api/v1/event/report", http.StatusOK,
		`{"code":0,"msg":"FORWARDED","data":null}`)
	w, _ := h.do(http.MethodPost, "/music/api/v1/event/report",
		`{"eventType":"page_view"}`, nil)
	if !strings.Contains(w.Body.String(), "FORWARDED") {
		t.Fatalf("非播放事件应转发给官方，得到 %s", w.Body.String())
	}
}

func TestHistoryListPutsOnlineFirst(t *testing.T) {
	h := newHarness(t)
	h.setOfficial("GET /music/api/v1/play-history/list", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("5")+`","title":"本地历史"}],"total":1}}`)

	tr := online.Track{Platform: "wy", PlatformID: "1", Title: "在线历史", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)
	_, _ = h.do(http.MethodPost, "/music/api/v1/event/report",
		`{"eventType":"track_play","trackGUID":"`+fake+`"}`, nil)

	w, _ := h.do(http.MethodGet, "/music/api/v1/play-history/list", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("应有 2 条，得到 %d", len(list))
	}
	if list[0].(map[string]any)["title"] != "在线历史" {
		t.Fatalf("在线历史应在前（最近发生的），得到 %v", list[0].(map[string]any)["title"])
	}
	if d["total"] != float64(2) {
		t.Fatalf("total 应为 2，得到 %v", d["total"])
	}
}

func TestHistoryDeleteRemovesOnlineEntry(t *testing.T) {
	h := newHarness(t)
	tr := online.Track{Platform: "wy", PlatformID: "1", Title: "在线历史", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)
	_, _ = h.do(http.MethodPost, "/music/api/v1/event/report",
		`{"eventType":"track_play","trackGUID":"`+fake+`"}`, nil)

	// DELETE 方法与 query 参数形态也要认。
	w, _ := h.do(http.MethodDelete, "/music/api/v1/play-history/delete?guid="+fake, "", nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("删除应成功，得到 %v", env)
	}
	if got := h.it.store.History("shared"); len(got) != 0 {
		t.Fatalf("删除后应为空，得到 %d", len(got))
	}
}

// ── 虚拟 id 与重启 ──────────────────────────────────────────────────────

func TestVirtualIDSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	tr := online.Track{Platform: "wy", PlatformID: "2652820720", Title: "晴天", Artists: []string{"周杰伦"}}
	fake := h.it.registry.Put(tr)
	_, _ = h.do(http.MethodPost, "/music/api/v1/favorite-track/create",
		`{"trackGUID":"`+fake+`"}`, nil)

	// 模拟重启：同一个 DataDir 上重建拦截层，登记表靠 Warm 从磁盘重放恢复。
	h2 := New(Config{
		DataDir:   h.dir,
		Upstream:  h.up,
		Logf:      t.Logf,
		Platforms: []string{"wy"},
		Searcher:  func(string, string, int, int) []search.UnifiedSong { return nil },
		Pool:      online.NewPool(&stubResolver{platform: "wy"}),
		LyricFetcher: func(online.Track) string {
			return ""
		},
	})
	if h2 == nil {
		t.Fatal("重建失败")
	}

	w, _ := call(h2, http.MethodGet, "/music/api/v1/track/metadata?guid="+fake, "", nil)
	d := dataOf(t, w)
	inner, ok := d["track"].(map[string]any)
	if !ok {
		t.Fatalf("重启后虚拟 id 应仍能反解，得到 %v", d)
	}
	if inner["title"] != "晴天" {
		t.Fatalf("重启后反解出的曲目不对：%v", inner["title"])
	}
}

func TestGuidSuffixesResolveToSameTrack(t *testing.T) {
	h := newHarness(t)
	tr := online.Track{Platform: "wy", PlatformID: "1", Title: "歌", Artists: []string{"甲"}}
	fake := h.it.registry.Put(tr)
	h.setLyric("[00:01.00]歌")

	// 带 :lyric / :album / :artist 后缀的 guid 都应指向同一条曲目。
	for _, suffix := range []string{":lyric", ":album", ":artist"} {
		w, _ := h.do(http.MethodGet, "/music/api/v1/track/lyrics?guid="+fake+suffix, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("后缀 %s 没被认出来（状态 %d）", suffix, w.Code)
		}
	}
}

func TestUnknownGuidNeverGuesses(t *testing.T) {
	h := newHarness(t)
	// 一个 32 位 hex 但从未登记过的 guid → 必须透传，不能瞎认。
	h.setOfficial("GET /music/api/v1/track/metadata", http.StatusOK,
		`{"code":0,"msg":"","data":{"from":"official"}}`)
	unknown := officialGuid("e")
	w, _ := h.do(http.MethodGet, "/music/api/v1/track/metadata?guid="+unknown, "", nil)
	if !strings.Contains(w.Body.String(), `"from":"official"`) {
		t.Fatalf("未登记的 guid 应透传给官方，得到 %s", w.Body.String())
	}
}

// TestForwardBuildsAbsoluteURL 钉死一件很容易回归的事：转给上游的请求必须是
// **绝对 URL**。
//
// 上游走 Unix socket 时 host 其实没用（transport 无视它去拨 socket），但
// http.Client.Do 会先检查 URL 是不是绝对的 —— 用只有 path+query 的相对地址
// 会直接失败在 "unsupported protocol scheme"，而且错在发出去之前：日志里
// 只有一个空的 502，看起来像「上游挂了」，其实一个字节都没发出去。
//
// 这个用例用真 *http.Client（httptest 的 client 会无视 URL host、直接拨测试
// 服务器的 listener），所以相对 URL 在这里必然失败。fakeUpstream 抓不到这个
// bug —— 它根本不看 URL。
func TestForwardBuildsAbsoluteURL(t *testing.T) {
	var gotURI, gotHost, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		gotHost = r.Host
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"list":[],"total":0}}`))
	}))
	defer srv.Close()

	// 自己搭 transport：httptest.Server.Client() 在不同 Go 版本里对
	// DialContext 的处理不一致，这里显式钉死「无论 URL 的 host 是什么，
	// 都拨到测试服务器的 listener」。
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
			},
		},
	}

	it := New(Config{
		DataDir:   t.TempDir(),
		Upstream:  client,
		Logf:      t.Logf,
		Platforms: []string{"wy"},
		Pool:      online.NewPool(&stubResolver{platform: "wy"}),
		Searcher:  func(string, string, int, int) []search.UnifiedSong { return nil },
		LyricFetcher: func(online.Track) string {
			return ""
		},
	})
	if it == nil {
		t.Fatal("New 返回 nil")
	}

	w, handled := call(it, http.MethodGet,
		"/music/api/v1/playlist/list?page=2", "",
		map[string]string{"Cookie": "sid=alice"})
	if !handled {
		t.Fatal("路径没被认领")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("上游应收到请求并回 200，得到 %d（相对 URL 会在发出去之前就失败）", w.Code)
	}
	if gotURI != "/music/api/v1/playlist/list?page=2" {
		t.Fatalf("上游收到的 path+query 不对：%q", gotURI)
	}
	if gotCookie != "sid=alice" {
		t.Fatalf("凭据必须转发给上游（官方靠它认用户），得到 %q", gotCookie)
	}
	if gotHost == "unix" {
		t.Fatal("Host 头被占位 host 覆盖了 —— 官方可能按 Host 分租户")
	}
}

// TestOnlineIsFavoriteReflectsStore 是真机 bug 的回归测试（歌单 + 历史两条路径）。
//
// 症状（NAS 真机上对着官方前端代码发现）：官方前端从列表反推收藏集合
//
//	favoriteIds = list.filter(x => x.isFavorite).map(x => x.guid)
//
// 我们三个列表共用 FavoriteVO，里面把 isFavorite 写死成 true —— 于是歌单和历史里
// 每一首在线曲目都显示成已收藏（红心常亮），哪怕从没收藏过。
//
// 同时钉三件事：没收藏的必须 false、收藏过的必须 true（别修成一律 false）、
// is_online 必须为 true。
func TestOnlineIsFavoriteReflectsStore(t *testing.T) {
	h := newHarness(t)
	// 官方这些端点都回空列表：本用例只关心在线那一段。
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
	})

	lonely := h.it.registry.Put(online.Track{Platform: "wy", PlatformID: "lonely", Title: "没收藏的", Artists: []string{"甲"}})
	loved := h.it.registry.Put(online.Track{Platform: "wy", PlatformID: "loved", Title: "收藏过的", Artists: []string{"乙"}})

	// 两首都进歌单、都播过；但只收藏其中一首。
	for _, g := range []string{lonely, loved} {
		w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
			`{"guid":"pl","trackGUIDs":["`+g+`"]}`, nil)
		if env := decode(t, w); env["code"] != float64(0) {
			t.Fatalf("加歌单失败 %s：%v", g, env)
		}
		w, _ = h.do(http.MethodPost, "/music/api/v1/event/report",
			`{"eventType":"track_play","trackGUID":"`+g+`"}`, nil)
		if env := decode(t, w); env["code"] != float64(0) {
			t.Fatalf("上报播放失败 %s：%v", g, env)
		}
	}
	w, _ := h.do(http.MethodPost, "/music/api/v1/favorite-track/create",
		`{"trackGUID":"`+loved+`"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("收藏失败：%v", env)
	}

	check := func(what string, d map[string]any) {
		byGUID := make(map[string]map[string]any, len(d["list"].([]any)))
		for _, raw := range d["list"].([]any) {
			m := raw.(map[string]any)
			byGUID[m["guid"].(string)] = m
		}
		for _, tc := range []struct {
			guid string
			want bool
		}{{lonely, false}, {loved, true}} {
			m, ok := byGUID[tc.guid]
			if !ok {
				t.Fatalf("%s：列表里找不到 %s", what, tc.guid)
			}
			if m["isFavorite"] != tc.want {
				t.Fatalf("%s：%s 的 isFavorite 应为 %v，得到 %v（写死 true 会让官方前端把它们全标成已收藏）",
					what, tc.guid, tc.want, m["isFavorite"])
			}
			if m["is_online"] != true {
				t.Fatalf("%s：%s 的 is_online 应为 true，得到 %v", what, tc.guid, m["is_online"])
			}
		}
	}

	w, _ = h.do(http.MethodGet, "/music/api/v1/track/playlist-detail/list?playlistGUID=pl&page=1&size=50", "", nil)
	check("歌单", dataOf(t, w))

	w, _ = h.do(http.MethodGet, "/music/api/v1/play-history/list", "", nil)
	check("历史", dataOf(t, w))

	// 收藏列表按定义全 true。
	w, _ = h.do(http.MethodGet, "/music/api/v1/favorite-track/list", "", nil)
	if m := dataOf(t, w)["list"].([]any)[0].(map[string]any); m["isFavorite"] != true {
		t.Fatalf("收藏列表里的条目必须 isFavorite=true，得到 %v", m["isFavorite"])
	}
}
