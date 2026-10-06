package intercept

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
)

// 这一组用例盯的是「虚拟歌单」（推荐歌单）这条链路。
//
// 它的风险与别的功能不同：虚拟歌单是在**列歌单时**临时算出来的，算错了会
// 直接影响用户在官方界面里看到的歌单列表 —— 所以这里既验「算得对」，
// 也验「算不出来时不会伤害官方数据」。

// hex32 是虚拟曲目 id 的硬约束：官方客户端只认这个形状。
var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// neteaseTrack 造一条来自网易的在线曲目（推荐歌单的候选）。
func neteaseTrack(id, title, artist string) online.Track {
	return online.Track{
		Platform:   "wy",
		PlatformID: id,
		Title:      title,
		Artists:    []string{artist},
		Album:      "专辑",
		Duration:   240,
		CoverURL:   "https://cdn.invalid/" + id + ".jpg",
	}
}

func dailyGuidNow() string {
	return vDailyGUID(time.Now(), "shared")
}

func hotGuidNow() string {
	return vHotGUID(time.Now(), "shared")
}

// ── 注入到官方歌单列表 ──────────────────────────────────────────────────

func TestVirtualPlaylistsInjectedAtTopOfList(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true, "w2": true},
		song("w1", "甲", "A"), song("w2", "乙", "B"))
	h.setNetease(neteaseTrack("9001", "丙", "C"), neteaseTrack("9002", "丁", "D"))
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("ab")+`","name":"我的歌单","trackCount":2}],"total":1}}`)

	w, handled := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	if !handled {
		t.Fatal("playlist/list 应被拦截")
	}
	d := dataOf(t, w)
	list, _ := d["list"].([]any)
	if len(list) != 3 {
		t.Fatalf("应有 3 个歌单（每日推荐 + 热门推荐 + 1 个官方），得到 %d", len(list))
	}

	first, _ := list[0].(map[string]any)
	if got := asString(first["name"]); got != "每日推荐 "+time.Now().Format("01-02") {
		t.Errorf("第一个应是每日推荐，得到 %q", got)
	}
	if got := asString(first["guid"]); got != dailyGuidNow() {
		t.Errorf("每日推荐 guid 应为 %q，得到 %q", dailyGuidNow(), got)
	}
	if got := asString(first["coverId"]); got == "" {
		t.Error("每日推荐的 coverId 不应为空")
	}

	second, _ := list[1].(map[string]any)
	if got := asString(second["name"]); got != "热门推荐" {
		t.Errorf("第二个应是热门推荐，得到 %q", got)
	}
	// 热门推荐的曲目来自网易（替身里带封面），所以封面**必须**借到某首曲目的
	// 虚拟 id —— 那是封面端点认得的东西，歌单卡片因此能显示真实封面。
	if got := asString(second["coverId"]); !hex32.MatchString(got) {
		t.Errorf("热门推荐的封面应借曲目的虚拟 id（32 位 hex），得到 %q", got)
	}

	// 官方那条必须还在，而且排在最后 —— 推荐是入口，不能盖掉用户自己的歌单。
	last, _ := list[2].(map[string]any)
	if got := asString(last["name"]); got != "我的歌单" {
		t.Errorf("官方歌单应原样保留在最后，得到 %q", got)
	}

	// total 要跟着加，否则客户端按 total 判断还有下一页时会翻到空页。
	if got := asInt(d["total"]); got != 3 {
		t.Errorf("total 应为 3，得到 %d", got)
	}
}

func TestVirtualPlaylistsNotInjectedWhenEmpty(t *testing.T) {
	h := newHarness(t)
	// 搜索引擎什么都没返回 → 每日推荐算不出曲目；网易不可达 → 热门推荐也空。
	h.setOnline(map[string]bool{})
	h.setNetease()
	h.setOfficial("GET /music/api/v1/playlist/list", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("ab")+`","name":"我的歌单","trackCount":2}],"total":1}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/playlist/list", "", nil)
	d := dataOf(t, w)
	list, _ := d["list"].([]any)

	// 空的推荐歌单**不能出现**：点进去什么都没有比没有卡片更让人以为坏了。
	if len(list) != 1 {
		t.Fatalf("算不出曲目时不应注入推荐歌单，得到 %d 个", len(list))
	}
	if got := asString(asMap(list[0])["name"]); got != "我的歌单" {
		t.Errorf("只剩官方歌单时应是它，得到 %q", got)
	}
	if got := asInt(d["total"]); got != 1 {
		t.Errorf("total 应为 1，得到 %d", got)
	}
}

// ── 详情：必须自己应答，不能先打上游 ────────────────────────────────────

func TestVirtualPlaylistDetailServedLocally(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true}, song("w1", "甲", "A"))
	h.setNetease(neteaseTrack("9001", "丙", "C"))

	w, handled := h.do(http.MethodGet,
		"/music/api/v1/playlist/detail?playlistGUID="+dailyGuidNow(), "", nil)
	if !handled {
		t.Fatal("虚拟歌单的详情应被拦截")
	}

	d := dataOf(t, w)
	if got := asString(d["name"]); got != "每日推荐 "+time.Now().Format("01-02") {
		t.Errorf("name 不对：%q", got)
	}
	if got := asBool(d["isDaily"]); !got {
		t.Error("推荐歌单应带 isDaily")
	}
	if got := asInt(d["trackCount"]); got != 1 {
		t.Errorf("trackCount 应为 1（等于实际曲目数），得到 %d", got)
	}
	if got := asString(d["coverId"]); got == "" {
		t.Error("coverId 不应为空")
	}

	// 关键：官方的 playlist/detail **一次都不能打**。官方库里没有这个歌单，
	// 打过去只会拿到「找不到」的业务错误，再透传回去客户端就什么都看不到。
	for _, c := range h.up.calls() {
		if strings.Contains(c, "/playlist/detail") {
			t.Errorf("虚拟歌单的详情不该打上游，却打了 %s", c)
		}
	}
}

// ── 曲目列表：分页口径 + guid 形状 ──────────────────────────────────────

func TestVirtualPlaylistTrackListPagingAndAll(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true, "w2": true, "w3": true},
		song("w1", "甲", "A"), song("w2", "乙", "B"), song("w3", "丙", "C"))
	h.setNetease(neteaseTrack("9001", "丁", "D"))

	guid := dailyGuidNow()

	// 第一页：只要 2 首，但 total 是全集 —— 客户端靠这个知道还有下一页。
	w, _ := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=2&playlistGUID="+guid, "", nil)
	d := dataOf(t, w)
	if got := asInt(d["total"]); got != 3 {
		t.Errorf("total 应为 3，得到 %d", got)
	}
	page, _ := d["list"].([]any)
	if len(page) != 2 {
		t.Fatalf("第一页应有 2 首，得到 %d", len(page))
	}

	// size == -1 是官方的「全量」约定，必须原样支持 —— 官方前端拿全量时用这个。
	w2, _ := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=-1&playlistGUID="+guid, "", nil)
	d2 := dataOf(t, w2)
	all, _ := d2["list"].([]any)
	if len(all) != 3 {
		t.Fatalf("size=-1 应返回全量 3 首，得到 %d", len(all))
	}

	// 越界页：返回空列表但 total 不变（与官方搜索那边的口径一致）。
	w3, _ := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=9&size=2&playlistGUID="+guid, "", nil)
	d3 := dataOf(t, w3)
	empty, _ := d3["list"].([]any)
	if len(empty) != 0 {
		t.Errorf("越界页应为空，得到 %d 首", len(empty))
	}
	if got := asInt(d3["total"]); got != 3 {
		t.Errorf("越界页的 total 应保持 3，得到 %d", got)
	}

	// 每条曲目的 id 必须是 32 位纯 hex —— 官方客户端只认这个形状，
	// 带 `:` 或非 hex 都会被它当成非法 id 丢掉。
	for _, raw := range all {
		m := asMap(raw)
		guid := asString(m["guid"])
		if !hex32.MatchString(guid) {
			t.Errorf("曲目 guid 必须是 32 位小写 hex，得到 %q", guid)
		}
		if got := asBool(m["is_online"]); !got {
			t.Errorf("曲目 %s 应标 is_online", guid)
		}
	}

	// 虚拟歌单没有官方段，所以上游一次都不该被打。
	for _, c := range h.up.calls() {
		if strings.Contains(c, "playlist-detail/list") {
			t.Errorf("虚拟歌单的曲目列表不该打上游，却打了 %s", c)
		}
	}
}

// ── 只读：写操作一律透传，绝不落本地桶 ──────────────────────────────────

func TestVirtualPlaylistWriteEndpointsPassThrough(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
	}{
		{"add-track", "/music/api/v1/playlist/add-track",
			`{"playlistGUID":"` + dailyGuidNow() + `","trackGUIDs":["w1"]}`},
		{"remove-track", "/music/api/v1/playlist/remove-track",
			`{"playlistGUID":"` + dailyGuidNow() + `","trackGUIDs":["w1"]}`},
		{"delete", "/music/api/v1/playlist/delete",
			`{"playlistGUID":"` + dailyGuidNow() + `"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.setOnline(map[string]bool{"w1": true}, song("w1", "甲", "A"))
			h.setNetease(neteaseTrack("9001", "丙", "C"))
			h.setOfficialFunc(func(r *http.Request, body []byte) *http.Response {
				return jsonResp(http.StatusOK, `{"code":100005,"msg":"playlist not found","data":null}`)
			})

			w, handled := h.do(http.MethodPost, c.path, c.body, nil)
			if !handled {
				t.Fatal("应被拦截层处理")
			}

			// 官方的拒绝原样回给客户端 —— 比我们自己编一个错误更准。
			if got := asInt(decode(t, w)["code"]); got != 100005 {
				t.Errorf("应原样透传官方的响应，得到 code=%d", got)
			}

			// 最关键的一条：**本地桶里一条都不能有**。写进去的话，那些记录
			// 永远不会有界面展示（明天 guid 就变了），变成查不出来的脏数据。
			user := h.it.userKey(mustRequest(t, c.path, c.body))
			if n := len(h.it.store.PlaylistTracks(user, dailyGuidNow())); n != 0 {
				t.Errorf("虚拟歌单不该写本地桶，却有 %d 条", n)
			}
		})
	}
}

// ── guid 形状：与 fnmusic-ext 对齐 ──────────────────────────────────────

func TestVirtualPlaylistGUIDShape(t *testing.T) {
	now := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	user := "9db86e47dc0d4fa8a1bfaac1dabac9f5"

	if got, want := vDailyGUID(now, user), "online:playlist:daily:20261005:9db86e47dc0d"; got != want {
		t.Errorf("每日推荐 guid：得到 %q，应为 %q", got, want)
	}
	if got, want := vHotGUID(now, user), "online:playlist:hot:20261005:9db86e47dc0d"; got != want {
		t.Errorf("热门推荐 guid：得到 %q，应为 %q", got, want)
	}
	// 没有用户（未登录/共享）时前后缀都要能正确退化。
	if got, want := vDailyGUID(now, "shared"), "online:playlist:daily:20261005:shared"; got != want {
		t.Errorf("无用户时的 guid：得到 %q，应为 %q", got, want)
	}
	if got, want := vNMGUID("3778678"), "online:playlist:nm:3778678"; got != want {
		t.Errorf("网易歌单 guid：得到 %q，应为 %q", got, want)
	}

	for _, g := range []string{
		vDailyGUID(now, user), vHotGUID(now, user), vNMGUID("1"),
	} {
		if !isVirtualPlaylistGUID(g) {
			t.Errorf("%q 应被认成虚拟歌单", g)
		}
	}
	// 官方 guid 绝不能被误认 —— 误认会让官方歌单走我们的应答，直接看不到歌。
	for _, g := range []string{"", "24464c407b854e13b4251e58243ba46a", "online:playlist", "online:track:1"} {
		if isVirtualPlaylistGUID(g) {
			t.Errorf("%q 不该被认成虚拟歌单", g)
		}
	}
}

// mustRequest 造一个和 call() 同形状的请求，用于直接查 store。
func mustRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
