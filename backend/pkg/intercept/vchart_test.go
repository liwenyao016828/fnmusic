package intercept

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
)

// 这一组用例盯的是「榜单歌单」（见 vchart.go）。
//
// 它与推荐歌单同源却不同风险：推荐的内容是我们自己算的，榜单的内容是**照抄
// 别人的接口**。所以这里除了验「勾了就出现、取消就消失」，重点验三件事：
//   1. 曲目必须能播（不可播的平台、不可播的单曲都不许进卡片）；
//   2. 榜单 guid 绝不能被误当成官方歌单（不然用户点开自己的歌单会看到榜单内容）；
//   3. 拉失败要缓存、但不许缓存成 30 分钟（否则一次抖动 = 榜单消失半小时）。

// chartTrack 造一条「榜单里的曲目」。
func chartTrack(platform, id, title, artist string) online.Track {
	return online.Track{
		Platform:   platform,
		PlatformID: id,
		Title:      title,
		Artists:    []string{artist},
		Album:      "专辑",
		Duration:   200,
		CoverURL:   "https://cdn.invalid/" + platform + "-" + id + ".jpg",
	}
}

// chartResolverPool 造一个按平台分派解析结果的池子。
//
// 不能复用 `poolFrom`：它只造「wy」一个解析器，而榜单要验的正是
// 「有解析器的平台才许进」。map[平台]map[平台内 id]能不能解析。
func chartResolverPool(platforms map[string]map[string]bool) *online.Pool {
	resolvers := make([]online.Resolver, 0, len(platforms))
	for platform, ids := range platforms {
		answers := make(map[string]*online.Resolved, len(ids))
		for id, ok := range ids {
			if ok {
				answers[id] = &online.Resolved{
					URL: "https://cdn.invalid/" + id + ".mp3", Format: "mp3", Size: 4096,
				}
			}
		}
		resolvers = append(resolvers, &stubResolver{platform: platform, answers: answers})
	}
	return online.NewPool(resolvers...)
}

// chartListBody 是官方歌单列表的「既有内容」（本用例的对照物）。
const chartListBody = `{"code":0,"msg":"","data":{"list":[{"guid":"24464c407b854e13b4251e58243ba46a","name":"我的歌单","trackCount":2}],"total":1}}`

// ── 注入 ────────────────────────────────────────────────────────────────

func TestChartPlaylistsInjectedWhenEnabled(t *testing.T) {
	h := newHarness(t)
	// 只勾一个榜。每日推荐 / 热门推荐都没有数据源 → 列表里只有榜单与官方歌单。
	h.setCharts("wy_19723756")
	h.setChartTracks(
		chartTrack("wy", "501", "甲", "A"),
		chartTrack("wy", "502", "乙", "B"),
		chartTrack("wy", "503", "丙", "C"), // 解析不出来 → 不许进榜
	)
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"wy": {"501": true, "502": true, "503": false},
	})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, handled := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	if !handled {
		t.Fatal("playlist/list 应被拦截")
	}
	d := dataOf(t, w)

	list, _ := d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("应有 2 个歌单（1 榜单 + 1 官方），得到 %d", len(list))
	}
	if got := asInt(d["total"]); got != 2 {
		t.Errorf("total 应为 2，得到 %d", got)
	}

	first, _ := list[0].(map[string]any)
	if got, want := asString(first["guid"]), vChartPrefix+"wy_19723756"; got != want {
		t.Errorf("榜单 guid 应为 %q，得到 %q", want, got)
	}
	if got := asString(first["name"]); got != "榜单 19723756" {
		t.Errorf("榜单名应来自榜单接口，得到 %q", got)
	}
	// 榜单卡片必须排在官方歌单**前面**（与推荐歌单同一位置约定）。
	if got := asString(asMap(list[1])["name"]); got != "我的歌单" {
		t.Errorf("官方歌单应排在榜单之后，得到 %q", got)
	}
	// 关键：解析不出直链的那首不许进榜 —— 「点开能看、点了不响」比没有更糟。
	if got := asInt(first["trackCount"]); got != 2 {
		t.Errorf("不可播的曲目不该进榜，应有 2 首，得到 %d", got)
	}
	if got := asString(first["coverId"]); !hex32.MatchString(got) {
		t.Errorf("榜单封面应借某首曲目的虚拟 id（32 位 hex），得到 %q", got)
	}
	if b, _ := first["isDaily"].(bool); !b {
		t.Error("榜单卡片要带 isDaily:true —— 官方前端按这个决定是否画「每日」角标")
	}
}

func TestChartPlaylistsAbsentWhenNothingChecked(t *testing.T) {
	h := newHarness(t)
	// 内容备好了，但一个榜都没勾。
	h.setChartTracks(chartTrack("wy", "501", "甲", "A"))
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"501": true}})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("一个榜都没勾时不该多出卡片，应有 1 个歌单，得到 %d", len(list))
	}
	// 而且**一次都不该去抓榜单**：没勾的榜还去抓，等于每次列歌单都白打一遍上游。
	if got := h.chartFetchCalls(); got != 0 {
		t.Errorf("没勾选任何榜单时不该去取榜单内容，却取了 %d 次", got)
	}
}

// 总开关关掉时：一张卡片都不注入，而且**一次都不去抓榜单** ——
// 但勾选名单必须原样留着（用户再打开开关时不该发现自己的勾没了）。
func TestChartMasterSwitchOffKeepsPicksButInjectsNothing(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")
	h.setChartTracks(chartTrack("wy", "501", "甲", "A"))
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"501": true}})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	h.setChartsOff(true)
	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("总开关关掉时不该多出卡片，应有 1 个歌单，得到 %d", len(list))
	}
	if got := h.chartFetchCalls(); got != 0 {
		t.Errorf("总开关关掉时不该去取榜单内容，却取了 %d 次", got)
	}

	// 名单还在（关开关 ≠ 清名单）—— 这是这个用例存在的意义：
	// 如果实现方图省事把总开关做成「等于清空名单」，这里就会红。
	h.setChartsOff(false)
	w2, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list2, _ := dataOf(t, w2)["list"].([]any)
	if len(list2) != 2 {
		t.Fatalf("重新打开总开关后勾选的榜应立刻回来，应有 2 个歌单，得到 %d", len(list2))
	}
}

func TestChartPlatformsWithoutResolverAreIgnored(t *testing.T) {
	h := newHarness(t)
	// 酷狗能列榜、但曲率解析不出它的直链 → 勾了也不该出现。
	h.setCharts("kg_8888", "wy_19723756")
	h.setChartTracks(chartTrack("wy", "501", "甲", "A"))
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"501": true}})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("只有 wy 该出现，应有 2 个歌单，得到 %d", len(list))
	}
	if got := asString(asMap(list[0])["guid"]); got != vChartPrefix+"wy_19723756" {
		t.Errorf("出现的应是可播平台的榜，得到 %q", got)
	}
	if got := h.chartFetchCalls(); got != 1 {
		t.Errorf("不可播平台的榜不该去取内容，取了 %d 次", got)
	}
}

func TestChartPlaylistWithNoPlayableTrackIsHidden(t *testing.T) {
	h := newHarness(t)
	h.setCharts("tx_26")
	h.setChartTracks(chartTrack("tx", "901", "甲", "A"))
	// QQ 侧只给 wy 解析器 → 整榜不可播。
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"501": true}})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("整榜都不可播时不该出现空卡片，应有 1 个歌单，得到 %d", len(list))
	}
}

func TestChartTracksAreCappedAtVChartSize(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")

	// 输入 150、期望截到 100 —— **两个都用字面量**。
	//
	// 第一版这里写的是 `vChartSize + 5` 与 `vChartSize`，结果「把 vChartSize
	// 改成 1000」这个变异连同期望一起被改了，测试照样全绿（用变异验出来的）。
	// 上限是个产品决定（与 fnmusic-ext 的 100 对齐），就该钉成数字。
	const input, want = 150, 100
	if vChartSize != want {
		t.Errorf("vChartSize 应为 %d：改了它就要连这条断言一起改（它决定一次响应多大）", want)
	}

	// 故意让替身**无视 limit**：上游一次给多少不由我们决定，截断必须是
	// 拦截层自己的责任（网易那条路一次能回 1000 首）。
	many := make([]online.Track, 0, input)
	playable := map[string]bool{}
	for n := 0; n < input; n++ {
		id := "id" + itoa(n)
		many = append(many, chartTrack("wy", id, "曲"+itoa(n), "A"))
		playable[id] = true
	}
	h.it.chartFetch = func(context.Context, string, string, int) (string, string, []online.Track, error) {
		return "长榜", "", many, nil
	}
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": playable})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) == 0 {
		t.Fatal("榜单卡片没出现")
	}
	if got := asInt(asMap(list[0])["trackCount"]); got != want {
		t.Errorf("榜单曲目应被截到 %d 首，得到 %d", want, got)
	}
}

// TestChartFiltersBeforeTruncating 钉住「先过滤、后截断」的顺序。
//
// 这条顺序是**真机实测倒逼出来的**：网易云「热歌榜」100 首候选里只有 36 首能播
// （榜单越热门 VIP 越多），QQ「巅峰榜·热歌」更低到个位数。所以候选必须多取
// （vChartFetchMax），**过滤完再截到 vChartSize**；反过来「先截 100 再过滤」
// 会把榜单砍成个位数 —— 那正是 v2.1.76 第一版真机跑出来的 36 / 3。
//
// 用例把能播的 100 首**全放在候选尾部**：只要顺序写反，尾部先被截掉，
// 就只剩前 100 首里能播的那 50 首（按 50 失败，而不是 100）。
func TestChartFiltersBeforeTruncating(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")

	const input, playableFrom, want = 150, 50, 100
	many := make([]online.Track, 0, input)
	playable := map[string]bool{}
	for n := 0; n < input; n++ {
		id := "id" + itoa(n)
		many = append(many, chartTrack("wy", id, "曲"+itoa(n), "A"))
		if n >= playableFrom {
			playable[id] = true
		}
	}
	h.it.chartFetch = func(context.Context, string, string, int) (string, string, []online.Track, error) {
		return "半截榜", "", many, nil
	}
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": playable})
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	list, _ := dataOf(t, w)["list"].([]any)
	if len(list) == 0 {
		t.Fatal("榜单卡片没出现")
	}
	if got := asInt(asMap(list[0])["trackCount"]); got != want {
		t.Errorf("能播的有 100 首，截断必须发生在过滤之后（应得 %d，得到 %d）—— "+
			"反过来的话榜单只剩前段里能播的那几首", want, got)
	}
}

// ── 拉失败：缓存，但要短 ────────────────────────────────────────────────

func TestChartFetchFailureIsCachedButOnlyBriefly(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")
	h.mu.Lock()
	h.chartErr = errChartUnreachable
	h.mu.Unlock()
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK, chartListBody)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	if list, _ := dataOf(t, w)["list"].([]any); len(list) != 1 {
		t.Fatalf("拉不到内容时不该出现卡片，应有 1 个歌单，得到 %d", len(list))
	}

	// 第二次请求：**必须命中负缓存**。否则官方前端每进一次歌单页都会把
	// 所有抓不到的榜重抓一遍。
	h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	if got := h.chartFetchCalls(); got != 1 {
		t.Errorf("拉失败的结果应被缓存，榜单接口只该被调 1 次，实际 %d 次", got)
	}

	// 把时间戳拨到「比 vChartNegTTL 久、但远没到 vCacheTTL」的位置：
	// 下一次请求就该重试。这一条同时钉住了「负缓存是短的」——
	// 要是有人把空结果的 TTL 也写成 vCacheTTL，这里就会一直是 1 次。
	if vChartNegTTL >= vCacheTTL {
		t.Errorf("空结果的缓存时长（%s）必须显著短于正常缓存（%s）—— 否则一次上游抖动"+
			"就等于榜单消失半小时", vChartNegTTL, vCacheTTL)
	}
	h.it.vMu.Lock()
	for guid, e := range h.it.vCache {
		e.ts = time.Now().Add(-vChartNegTTL - time.Second)
		h.it.vCache[guid] = e
	}
	h.it.vMu.Unlock()

	h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	if got := h.chartFetchCalls(); got != 2 {
		t.Errorf("过了负缓存期应重试，榜单接口应被调 2 次，实际 %d 次", got)
	}
}

// ── 本地应答：详情与曲目列表 ────────────────────────────────────────────

func TestChartPlaylistDetailServedLocally(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")
	h.setChartTracks(
		chartTrack("wy", "501", "甲", "A"),
		chartTrack("wy", "502", "乙", "B"),
	)
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"501": true, "502": true}})
	// 官方对未知 guid 一律报「找不到」—— 我们要证明的是**根本没问它**。
	h.setOfficialFunc(func(r *http.Request, body []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":100005,"msg":"playlist not found","data":null}`)
	})

	guid := vChartPrefix + "wy_19723756"
	w, handled := h.do(http.MethodGet, "/music/api/v1/playlist/detail?playlistGUID="+guid, "", nil)
	if !handled {
		t.Fatal("虚拟歌单的详情应被拦截层处理")
	}
	env := decode(t, w)
	if got := asInt(env["code"]); got != 0 {
		t.Fatalf("应本地应答成功，得到 code=%d（%s）", got, w.Body.String())
	}
	detail, _ := env["data"].(map[string]any)
	if detail == nil {
		t.Fatalf("data 不是对象：%s", w.Body.String())
	}
	if got := asString(detail["guid"]); got != guid {
		t.Errorf("详情里的 guid 应为 %q，得到 %q", guid, got)
	}
	if got := asInt(detail["trackCount"]); got != 2 {
		t.Errorf("详情 trackCount 应为 2，得到 %d", got)
	}
	for _, c := range h.up.calls() {
		if strings.Contains(c, "playlist/detail") {
			t.Errorf("虚拟歌单的详情不该打上游，却打了 %s", c)
		}
	}
}

func TestChartTrackListServedLocally(t *testing.T) {
	h := newHarness(t)
	h.setCharts("wy_19723756")
	h.setChartTracks(
		chartTrack("wy", "501", "甲", "A"),
		chartTrack("wy", "502", "乙", "B"),
		chartTrack("wy", "503", "丙", "C"),
	)
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"wy": {"501": true, "502": true, "503": true},
	})
	h.setOfficialFunc(func(r *http.Request, body []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":100005,"msg":"playlist not found","data":null}`)
	})

	guid := vChartPrefix + "wy_19723756"

	w, handled := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=2&playlistGUID="+guid, "", nil)
	if !handled {
		t.Fatal("虚拟歌单的曲目列表应被拦截层处理")
	}
	d := dataOf(t, w)
	if got := asInt(d["total"]); got != 3 {
		t.Errorf("total 应为 3，得到 %d", got)
	}
	page, _ := d["list"].([]any)
	if len(page) != 2 {
		t.Fatalf("第一页应有 2 首，得到 %d", len(page))
	}
	// 曲目 guid 必须是 32 位纯 hex —— 榜单 guid 里带 `:`，两者不是一回事，
	// 混淆会让官方客户端把整页曲目判成非法。
	for _, raw := range page {
		m := asMap(raw)
		if g := asString(m["guid"]); !hex32.MatchString(g) {
			t.Errorf("曲目 guid 必须是 32 位小写 hex，得到 %q", g)
		}
		if !asBool(m["is_online"]) {
			t.Errorf("榜单曲目应标 is_online")
		}
	}

	// size == -1 是官方的「全量」约定，榜单这条路也要守。
	w2, _ := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=-1&playlistGUID="+guid, "", nil)
	if all, _ := dataOf(t, w2)["list"].([]any); len(all) != 3 {
		t.Errorf("size=-1 应返回全量 3 首，得到 %d", len(all))
	}

	for _, c := range h.up.calls() {
		if strings.Contains(c, "playlist-detail/list") {
			t.Errorf("虚拟歌单的曲目列表不该打上游，却打了 %s", c)
		}
	}
}

// ── 只读：写操作一律透传 ────────────────────────────────────────────────

func TestChartWriteEndpointsPassThrough(t *testing.T) {
	guid := vChartPrefix + "wy_19723756"
	cases := []struct {
		name string
		path string
		body string
	}{
		{"add-track", "/music/api/v1/playlist/add-track",
			`{"playlistGUID":"` + guid + `","trackGUIDs":["w1"]}`},
		{"remove-track", "/music/api/v1/playlist/remove-track",
			`{"playlistGUID":"` + guid + `","trackGUIDs":["w1"]}`},
		{"delete", "/music/api/v1/playlist/delete",
			`{"playlistGUID":"` + guid + `"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.setCharts("wy_19723756")
			h.setOfficialFunc(func(r *http.Request, body []byte) *http.Response {
				return jsonResp(http.StatusOK, `{"code":100005,"msg":"playlist not found","data":null}`)
			})

			w, handled := h.do(http.MethodPost, c.path, c.body, nil)
			if !handled {
				t.Fatal("应被拦截层处理")
			}
			if got := asInt(decode(t, w)["code"]); got != 100005 {
				t.Errorf("应原样透传官方的响应，得到 code=%d", got)
			}
			// 榜单歌单在本地一个字节都不该落 —— 它明天还是同一份，
			// 但「本地桶里存着官方库里没有的歌单」本身就是脏数据。
			user := h.it.userKey(mustRequest(t, c.path, c.body))
			if n := len(h.it.store.PlaylistTracks(user, guid)); n != 0 {
				t.Errorf("榜单歌单不该写本地桶，却有 %d 条", n)
			}
		})
	}
}

// ── 解析与形状 ──────────────────────────────────────────────────────────

func TestParseVChartRef(t *testing.T) {
	ok := []struct{ in, source, id string }{
		{"wy_19723756", "wy", "19723756"},
		{"WY_19723756", "wy", "19723756"}, // 大小写不敏感
		{" tx_26 ", "tx", "26"},           // 前后空白要吃掉
		{"wy_a_b", "wy", "a_b"},           // id 里再有下划线，只按第一个下划线切
	}
	for _, c := range ok {
		got, valid := parseVChartRef(c.in)
		if !valid {
			t.Errorf("%q 应被解析成功", c.in)
			continue
		}
		if got.Source != c.source || got.ID != c.id {
			t.Errorf("%q 解析成 %+v，应为 {%s %s}", c.in, got, c.source, c.id)
		}
	}

	bad := []string{
		"",          // 空
		"wy",        // 没有下划线
		"wy_",       // id 空
		"_19723756", // 平台空
		"kg_8888",   // 酷狗：曲率没有它的解析器
		"kw_93",     // 酷我：同上
		"qq_26",     // 平台名要用 tx，不是 qq
	}
	for _, in := range bad {
		if _, valid := parseVChartRef(in); valid {
			t.Errorf("%q 不该被解析成功", in)
		}
	}

	// guid 形状：与 fnmusic-ext 一致，用户切换扩展时看到同一个歌单。
	if got, want := (vChartRef{Source: "wy", ID: "19723756"}).guid(),
		"online:playlist:chart:wy_19723756"; got != want {
		t.Errorf("榜单 guid：得到 %q，应为 %q", got, want)
	}
	if !isVirtualPlaylistGUID("online:playlist:chart:wy_19723756") {
		t.Error("榜单 guid 应被认成虚拟歌单")
	}
	// 官方 guid 绝不能被误认 —— 误认会让官方歌单走我们的应答，用户直接看不到歌。
	for _, g := range []string{"", "24464c407b854e13b4251e58243ba46a", "online:playlist", "online:track:1"} {
		if isVirtualPlaylistGUID(g) {
			t.Errorf("%q 不该被认成虚拟歌单", g)
		}
	}
}

func TestVChartDayStartIsLocalMidnight(t *testing.T) {
	ts := vChartDayStart()
	got := time.Unix(ts, 0).In(time.Local)
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Fatalf("榜单时间戳应是本地零点，得到 %s", got.Format(time.RFC3339))
	}
	now := time.Now()
	if got.Year() != now.Year() || got.Month() != now.Month() || got.Day() != now.Day() {
		t.Errorf("榜单时间戳应是今天，得到 %s", got.Format(time.RFC3339))
	}
	// `time.Now().Truncate(24*time.Hour)` 是相对 UTC 零时截断的，在东八区会
	// 得到当天 08:00 —— 这条断言就是防止有人「顺手简化」成它。
	if got.Hour() == 8 && time.Local.String() != "UTC" {
		t.Error("时间戳落在了 08:00：多半是用了 Truncate(24*time.Hour)")
	}
}

// ── 本机回环取数 ────────────────────────────────────────────────────────

func TestChartDetailViaLocalAPI(t *testing.T) {
	const body = `{"code":200,"data":{"id":19723756,"name":"飙升榜","cover":"https://p2.invalid/chart.jpg","songs":[
		{"id":"wy_111","name":"甲","singer":"A, B","album":"专辑一","duration":200,"cover":"https://cdn.invalid/111.jpg","source":"wy","songmid":""},
		{"id":"tx_222","name":"乙","singer":"C","album":"","duration":180,"cover":"","source":"tx","songmid":"222MID"}
	]}}`

	var mu sync.Mutex
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotQuery = r.URL.RawQuery
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.it.localAPI = srv.URL

	name, cover, tracks, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "19723756", 100)
	if err != nil {
		t.Fatalf("取榜单失败：%v", err)
	}
	mu.Lock()
	query := gotQuery
	mu.Unlock()
	if !strings.Contains(query, "source=wy") || !strings.Contains(query, "id=19723756") {
		t.Errorf("请求参数不对：%s", query)
	}
	if name != "飙升榜" {
		t.Errorf("榜单名应为「飙升榜」，得到 %q", name)
	}
	if cover == "" {
		t.Error("榜单封面不该为空")
	}
	if len(tracks) != 2 {
		t.Fatalf("应解析出 2 首，得到 %d", len(tracks))
	}
	// `id` 是 `<平台>_<平台内 id>` 的复合形态，必须剥掉前缀再喂给解析器 ——
	// 不剥的话解析器会拿着 `wy_111` 去问网易要歌，必然解析失败。
	if got := tracks[0].PlatformID; got != "111" {
		t.Errorf("网易曲目的平台内 id 应为 111，得到 %q", got)
	}
	if got := tracks[0].Artists; len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("艺术家应拆成 [A B]，得到 %v", got)
	}
	// QQ 侧 `songmid` 才是真主键，它优先于复合 id。
	if got := tracks[1].PlatformID; got != "222MID" {
		t.Errorf("QQ 曲目应优先用 songmid，得到 %q", got)
	}

	// limit 要真的生效（调用方靠它给上游的返回封顶）。
	_, _, limited, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "19723756", 1)
	if err != nil {
		t.Fatalf("取榜单失败：%v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limit=1 应只回 1 首，得到 %d", len(limited))
	}
}

func TestChartDetailViaLocalAPIRejectsBadResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"业务失败", http.StatusOK, `{"code":100002,"msg":"invalid arguments","data":null}`},
		{"HTTP 非 200", http.StatusBadGateway, ``},
		{"不是 JSON", http.StatusOK, `<html>502</html>`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			h := newHarness(t)
			h.it.localAPI = srv.URL
			if _, _, _, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "1", 100); err == nil {
				t.Fatal("应报错，却成功了")
			}
		})
	}

	// 没接线本机地址时必须**报错而不是猜** —— 猜一个地址等于给 SSRF 留门。
	h := newHarness(t)
	h.it.localAPI = ""
	if _, _, _, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "1", 100); err == nil {
		t.Fatal("未接线 LocalAPI 时应报错")
	}
}

// errChartUnreachable 是「榜单接口打不通」的替身错误。
var errChartUnreachable = errors.New("榜单接口打不通")
