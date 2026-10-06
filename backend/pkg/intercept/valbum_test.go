package intercept

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 在线专辑（见 valbum.go）的用例。
//
// # 关于官方响应的形状
//
// 三条端点的官方夹具都**不是真机抓包**：`/search/album` 的形状取自参考实现
// （/tmp/fnme-z/proxy/app.py:8755 合并的是 `data.list`），`/album` 与
// `/track/album-detail/list` 的形状取自它的 `_album_detail_payload` /
// `_album_list_envelope`（app.py:8260 / 8472）—— 那个实现代理的是同一个
// `trim_music` unix socket，且作者在真机上渲染过。开发机连不上官方页面
// （只有曲率的 8898 / 旧版 8899 / AI 网关 20128），所以本地的形状只能到这个程度。
//
// 正因为是二手的，用例里**一半在钉「形状不认识时怎么办」**：非预期形状一律
// 逐字节原样透传、绝不改官方那一个字。这样最坏情况是「在线专辑没进去」，
// 而不是「官方专辑页被搞坏」。

// albumSong 造一条搜索结果，带指定专辑名与封面。
func albumSong(id, title, artist, album string) search.UnifiedSong {
	return search.UnifiedSong{
		ID: id, Songmid: id, Name: title, Singer: artist, Album: album,
		Duration: 240, Cover: "https://cdn.invalid/" + id + ".jpg",
	}
}

// albumTrack 造一条「登记表里的曲目」（专辑的锚点）。
func albumTrack(platform, id, title, artist, album string) online.Track {
	return online.Track{
		Platform: platform, PlatformID: id, Title: title,
		Artists: []string{artist}, Album: album, Duration: 240,
		CoverURL: "https://cdn.invalid/" + platform + "-" + id + ".jpg",
	}
}

// albumList 取响应里 data.list，要求元素都是对象。
func albumList(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	raw, ok := dataOf(t, w)["list"].([]any)
	if !ok {
		t.Fatalf("data.list 不是数组：%#v", dataOf(t, w)["list"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("data.list 里有非对象元素：%#v", item)
		}
		out = append(out, m)
	}
	return out
}

// albumNames 取出列表里的 name 字段（顺序敏感）。
func albumNames(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, asString(m["name"]))
	}
	return out
}

// captureLogs 把拦截层的日志收进切片。
//
// 诊断日志的用例要断言两件事：**每类只打一条**、以及**只打类型与键名、不打值**。
// 这两件事只能从日志本身看。
func captureLogs(h *harness) func() []string {
	var mu sync.Mutex
	var lines []string
	h.it.logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// logsMatching 返回包含某个子串的日志行。
func logsMatching(lines []string, substr string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// ── 专辑搜索：合并 ──────────────────────────────────────────────────────

func TestAlbumSearchMergesOnlineAlbumsAfterOfficial(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true, "3": true},
		albumSong("1", "七里香", "周杰伦", "七里香"),
		albumSong("2", "借口", "周杰伦", "七里香"),
		albumSong("3", "晴天", "周杰伦", "叶惠美"),
	)
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("a")+`","name":"本地专辑","trackCount":2}],"total":1}}`)

	w, handled := h.do(http.MethodGet, "/music/api/v1/search/album?q=周杰伦", "", nil)
	if !handled {
		t.Fatal("search/album 该被拦截层认领（认领了才谈得上合并）")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("状态码该保持 200，得到 %d", w.Code)
	}
	list := albumList(t, w)
	if got := albumNames(list); len(got) != 3 || got[0] != "本地专辑" {
		t.Fatalf("官方专辑必须在前、在线专辑补在后：%v", got)
	}
	// 曲目数多的组排前面（与参考实现同一条排序口径）。
	if list[1]["name"] != "七里香" || list[2]["name"] != "叶惠美" {
		t.Fatalf("在线专辑应按曲目数降序：%v", albumNames(list[1:]))
	}
	if list[1]["trackCount"] != float64(2) || list[2]["trackCount"] != float64(1) {
		t.Fatalf("trackCount 该是本组看到的曲目数：%v / %v", list[1]["trackCount"], list[2]["trackCount"])
	}
	// 专辑 guid 必须是「曲目虚拟 id + :album」——本层既有的虚拟 id 约定。
	guid := asString(list[1]["guid"])
	fake := strings.TrimSuffix(guid, ":album")
	if guid == fake || !online.IsHex32(fake) {
		t.Fatalf("专辑 guid 该是 <32hex>:album，得到 %q", guid)
	}
	if asString(list[1]["coverId"]) != fake {
		t.Fatalf("coverId 该是锚点曲目的虚拟 id（封面端点认它），得到 %v", list[1]["coverId"])
	}
	arts, _ := list[1]["artists"].([]any)
	if len(arts) != 1 || asString(arts[0].(map[string]any)["name"]) != "周杰伦" {
		t.Fatalf("artists 该是非空对象数组：%#v", list[1]["artists"])
	}
	if dataOf(t, w)["total"] != float64(3) {
		t.Fatalf("total 该是官方 1 + 在线 2 = 3，得到 %v", dataOf(t, w)["total"])
	}
}

func TestAlbumSearchDedupesOfficialAlbumNames(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true, "3": true},
		albumSong("1", "七里香", "周杰伦", "七里香"),
		albumSong("2", "借口", "周杰伦", "七里香"),
		albumSong("3", "晴天", "周杰伦", "叶惠美"),
	)
	// 官方已经有一张叫「七里香」的专辑（大小写/空白差异也要算同名）。
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[{"guid":"`+officialGuid("a")+`","name":" 七里香 "}],"total":1}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=七里香", "", nil)
	list := albumList(t, w)
	if got := albumNames(list); len(got) != 2 || got[0] != " 七里香 " || got[1] != "叶惠美" {
		t.Fatalf("与官方同名的在线专辑不该重复注入，且官方条目要原样保留：%v", got)
	}
	if dataOf(t, w)["total"] != float64(2) {
		t.Fatalf("total 该是 1 官方 + 1 在线 = 2，得到 %v", dataOf(t, w)["total"])
	}
}

func TestAlbumSearchTotalWhenOfficialOmitsIt(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, albumSong("1", "A", "甲", "专辑一"))
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[]}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
	d := dataOf(t, w)
	if d["total"] != float64(1) {
		t.Fatalf("官方没给 total 时该补成本页条数，得到 %v", d["total"])
	}
	if len(albumList(t, w)) != 1 {
		t.Fatalf("官方空列表 + 1 张在线专辑：%v", albumNames(albumList(t, w)))
	}
}

func TestAlbumSearchCapsInjectedAlbums(t *testing.T) {
	h := newHarness(t)
	playable := map[string]bool{}
	songs := make([]search.UnifiedSong, 0, 15)
	for n := 0; n < 15; n++ {
		id := fmt.Sprintf("%d", n)
		playable[id] = true
		songs = append(songs, albumSong(id, "歌"+id, "甲", "专辑"+id))
	}
	h.setOnline(playable, songs...)
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
	if got := len(albumList(t, w)); got != albumSearchMax {
		t.Fatalf("最多注入 %d 张，得到 %d", albumSearchMax, got)
	}
	// 顺带钉住「一次搜索不会把登记表灌爆」：只登记被注入的锚点。
	if n := h.it.registry.Len(); n != albumSearchMax {
		t.Fatalf("登记表该只有 %d 个锚点（每个注入的专辑一个），得到 %d", albumSearchMax, n)
	}
}

func TestAlbumSearchSkipsAlbumsWithUnknownName(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, albumSong("1", "无专辑信息", "甲", ""))
	const body = `{"code":0,"msg":"","data":{"list":[],"total":0}}`
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
	// 专辑名缺失（会被兜底成「未知专辑」）→ 不聚合、没有可加的 → 逐字节透传。
	if w.Body.String() != body {
		t.Fatalf("没有可加的专辑时该逐字节透传，得到 %s", w.Body.String())
	}
}

func TestAlbumSearchSingleSourceFailureDoesNotBlockOthers(t *testing.T) {
	h := newHarness(t)
	h.it.platformsStatic = []string{"wy", "tx"}
	h.it.searcher = func(keyword, platform string, page, size int) []search.UnifiedSong {
		if platform == "wy" {
			return nil // 这一路全挂
		}
		return []search.UnifiedSong{
			albumSong("t1", "乙一", "乙", "乙专辑"),
			albumSong("t2", "乙二", "乙", "乙专辑"),
		}
	}
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"tx": {"t1": true, "t2": true},
	})
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
	list := albumList(t, w)
	if len(list) != 1 || list[0]["name"] != "乙专辑" {
		t.Fatalf("一个源失败不该影响另一个源：%v", albumNames(list))
	}
}

// ── 专辑搜索：不认识的形状一律原样透传 ────────────────────────────────────

func TestAlbumSearchPassthroughWhenShapeIsNotObjectList(t *testing.T) {
	bodies := []string{
		`{"code":0,"msg":"","data":{"albums":[{"guid":"x","name":"y"}]}}`, // data 是对象但没有 list
		`{"code":0,"msg":"","data":{"list":["不是对象"]}}`,                    // list 不是对象数组
		`{"code":0,"msg":"","data":"字符串"}`,                                // data 不是对象
	}
	for _, body := range bodies {
		h := newHarness(t)
		h.setOnline(map[string]bool{"1": true}, albumSong("1", "A", "甲", "专辑一"))
		h.setOfficial("GET /music/api/v1/search/album", http.StatusOK, body)

		w, handled := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
		if !handled {
			t.Fatal("这条路由该被认领")
		}
		if w.Body.String() != body {
			t.Fatalf("形状不认识时必须逐字节原样透传。\nwant %s\ngot  %s", body, w.Body.String())
		}
	}
}

func TestAlbumSearchPassthroughOnOfficialError(t *testing.T) {
	// ⚠️ 两条夹具都要有，而**第二条才是咬住「业务错误守卫」的那条**：
	// `data:null` 会被后面那道「data 必须是对象」的检查兜住（它验的是形状检查，
	// 不是业务错误守卫）；`data` 形状合法时，守卫一没，本地就会把官方的
	// 「未登录」改写成「未登录 + 在线卡片」—— 那是把官方自己的语义改了。
	for _, body := range []string{
		`{"code":401,"msg":"未登录","data":null}`,
		`{"code":401,"msg":"未登录","data":{"list":[],"total":0}}`,
	} {
		h := newHarness(t)
		h.setOnline(map[string]bool{"1": true}, albumSong("1", "A", "甲", "专辑一"))
		h.setOfficial("GET /music/api/v1/search/album", http.StatusOK, body)

		w, handled := h.do(http.MethodGet, "/music/api/v1/search/album?q=x", "", nil)
		if !handled {
			t.Fatal("这条路由该被认领")
		}
		if w.Body.String() != body {
			t.Fatalf("官方业务错误必须逐字节原样透传（那是官方自己的语义）。\nwant %s\ngot  %s", body, w.Body.String())
		}
	}
}

func TestAlbumSearchPassthroughWithoutKeyword(t *testing.T) {
	const body = `{"code":0,"msg":"","data":{"list":[],"total":0}}`
	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true}, albumSong("1", "A", "甲", "专辑一"))
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album", "", nil)
	if w.Body.String() != body {
		t.Fatalf("没有关键词就没有在线部分，该原样透传：%s", w.Body.String())
	}
}

func TestAlbumSearchDiagnosticLogsOnceAndWithoutValues(t *testing.T) {
	h := newHarness(t)
	logs := captureLogs(h)
	h.setOnline(map[string]bool{"1": true}, albumSong("1", "机密歌名", "甲", "机密专辑"))
	// data 里没有 list：形状不认识。
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"items":[{"name":"机密专辑"}]}}`)

	for n := 0; n < 3; n++ {
		h.do(http.MethodGet, "/music/api/v1/search/album?q=机密关键词", "", nil)
	}
	lines := logsMatching(logs(), "专辑搜索：官方 data.list 的形状")
	if len(lines) != 1 {
		t.Fatalf("这类诊断只该打一条，得到 %d 条：%v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "items") {
		t.Fatalf("诊断该把 data 的**键名**打出来（这才是下次适配的依据）：%s", lines[0])
	}
	for _, secret := range []string{"机密歌名", "机密专辑", "机密关键词"} {
		if strings.Contains(lines[0], secret) {
			t.Fatalf("诊断日志不该打值（可能含用户隐私）：%s", lines[0])
		}
	}
}

// ── 专辑详情 ────────────────────────────────────────────────────────────

// albumHarness 造一个「锚点已在登记表里」的场景，返回专辑 guid。
func albumHarness(t *testing.T, anchor online.Track, songs ...search.UnifiedSong) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	playable := map[string]bool{anchor.PlatformID: true}
	for _, s := range songs {
		playable[s.Songmid] = true
	}
	h.setOnline(playable, songs...)
	return h, h.it.registry.Put(anchor)
}

func TestAlbumDetailSynthesizesFromAnchorAndSearch(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor,
		albumSong("2", "甲二", "甲", "同名专辑"),
		albumSong("3", "别的专辑的歌", "甲", "其它专辑"),
		albumSong("4", "别的歌手同名专辑", "乙", "同名专辑"),
	)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":100005,"msg":"官方不认","data":null}`)
	})

	w, handled := h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
	if !handled {
		t.Fatal("该认领")
	}
	d := dataOf(t, w)
	if asString(d["guid"]) != fake+":album" {
		t.Fatalf("专辑 guid 该是规范形态 <fake>:album，得到 %v", d["guid"])
	}
	if asString(d["name"]) != "同名专辑" {
		t.Fatalf("专辑名不对：%v", d["name"])
	}
	if d["trackCount"] != float64(2) {
		t.Fatalf("该只有同平台 + 同专辑名 + 艺术家有交集的 2 首，得到 %v", d["trackCount"])
	}
	if asString(d["coverId"]) != fake {
		t.Fatalf("coverId 该是锚点曲目的虚拟 id，得到 %v", d["coverId"])
	}
	tracks, _ := d["tracks"].([]any)
	if len(tracks) != 2 {
		t.Fatalf("tracks 该有 2 条：%#v", tracks)
	}
	first := tracks[0].(map[string]any)
	if asString(first["guid"]) != fake {
		t.Fatalf("锚点该排在第一（用户就是点它进来的），得到 %v", first["guid"])
	}
	if asString(first["title"]) != "甲一" {
		t.Fatalf("第一条该是锚点：%v", first["title"])
	}
	// 专辑详情的 artists 必须是对象数组（与其它 VO 同一套要求）。
	arts, _ := d["artists"].([]any)
	if len(arts) != 1 || asString(arts[0].(map[string]any)["name"]) != "甲" {
		t.Fatalf("artists 形状不对：%#v", d["artists"])
	}
	if _, ok := first["audioSpec"].(map[string]any); !ok {
		t.Fatalf("曲目条目必须有 audioSpec（官方客户端要读它）：%#v", first)
	}
	if _, ok := first["album"].(map[string]any); !ok {
		t.Fatalf("曲目条目必须有 album 对象：%#v", first)
	}
}

func TestAlbumDetailAcceptsAllIDForms(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor)

	for _, form := range []string{
		fake,
		fake + ":album",
		"album_" + fake,
		"album_" + fake + ":album",
		"track_" + fake + ":album",
	} {
		w, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+form, "", nil)
		d := dataOf(t, w)
		if asString(d["guid"]) != fake+":album" {
			t.Fatalf("形态 %q 该反解到同一个专辑，得到 guid=%v（body=%s）", form, d["guid"], w.Body.String())
		}
	}
	// 参数名也可以不同（官方不同版本用过几个名字）。
	for _, name := range []string{"albumGUID", "albumGuid", "albumId", "id", "coverId"} {
		w, _ := h.do(http.MethodGet, "/music/api/v1/album?"+name+"="+fake+":album", "", nil)
		if asString(dataOf(t, w)["guid"]) != fake+":album" {
			t.Fatalf("参数名 %q 该被认出来：%s", name, w.Body.String())
		}
	}
}

func TestAlbumDetailPassthroughOfficialGuid(t *testing.T) {
	const body = `{"code":100005,"msg":"NotFound","data":null}`
	h := newHarness(t)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, body)
	})
	for _, target := range []string{
		"/music/api/v1/album?guid=" + officialGuid("b"),
		"/music/api/v1/album?guid=album_" + officialGuid("c"),
		"/music/api/v1/album",
		"/music/api/v1/album/detail?guid=" + officialGuid("d"),
	} {
		w, handled := h.do(http.MethodGet, target, "", nil)
		if !handled {
			t.Fatalf("%s 该被认领（兜底路由认领并透传）", target)
		}
		if w.Body.String() != body {
			t.Fatalf("官方/未知专辑必须逐字节原样透传。\n%s\nwant %s\ngot  %s", target, body, w.Body.String())
		}
	}
}

func TestAlbumDetailPassthroughOnNonReadonlyMethod(t *testing.T) {
	const body = `{"code":0,"msg":"","data":{"ok":true}}`
	// ⚠️ id 必须是**我们自己合成的专辑 guid**：不带 id 的 POST 走的是「找不到 id →
	// 透传」那条路，与 albumRejectMethod 无关（那道守卫被拿掉它照样绿）。
	// 带上自己的 guid 之后，守卫一没，本地就会应答这条本该透传的请求。
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor, albumSong("2", "甲二", "甲", "同名专辑"))
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, body)
	})
	w, handled := h.do(http.MethodPost, "/music/api/v1/album?guid="+fake+":album",
		`{"guid":"`+fake+`:album"}`, nil)
	if !handled {
		t.Fatal("该被认领")
	}
	if w.Body.String() != body {
		t.Fatalf("非只读方法该原样透传（哪怕 id 是我们自己的虚拟专辑）：%s", w.Body.String())
	}
	// 透传必须**真的转给官方**（不是本地猜了一份一样的 body 回来）。
	if calls := h.up.calls(); len(calls) != 1 || calls[0] != "POST /music/api/v1/album" {
		t.Fatalf("该把这条请求原样转给官方，得到 %v", calls)
	}
}

func TestAlbumDetailDiagnosticForUnregisteredVirtualID(t *testing.T) {
	const body = `{"code":100005,"msg":"NotFound","data":null}`
	h := newHarness(t)
	logs := captureLogs(h)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, body)
	})

	// ① 本层下发的专辑 guid 形态、但登记表里没有（重启后登记表没覆盖到）。
	unregistered := officialGuid("e") + ":album"
	h.do(http.MethodGet, "/music/api/v1/album?albumGUID="+unregistered, "", nil)
	h.do(http.MethodGet, "/music/api/v1/album?albumGUID="+unregistered, "", nil)
	lines := logsMatching(logs(), "本层下发的专辑 guid 形态")
	if len(lines) != 1 {
		t.Fatalf("这类诊断只该打一条，得到 %d 条：%v", len(lines), lines)
	}
	if strings.Contains(lines[0], unregistered) {
		t.Fatalf("诊断日志不该打值：%s", lines[0])
	}

	// ② 官方专辑页（裸 32 位 hex）是**正常请求**，不该产生任何诊断。
	if n := len(logsMatching(logs(), "专辑：")); n != 1 {
		t.Fatalf("官方 guid 不该触发诊断，得到 %v", logsMatching(logs(), "专辑："))
	}
}

func TestAlbumDetailDiagnosticWhenNoIDFound(t *testing.T) {
	const body = `{"code":100005,"msg":"NotFound","data":null}`
	h := newHarness(t)
	logs := captureLogs(h)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, body)
	})

	h.do(http.MethodGet, "/music/api/v1/album?someUnknownParam=abc", "", nil)
	h.do(http.MethodGet, "/music/api/v1/album?someUnknownParam=abc", "", nil)
	lines := logsMatching(logs(), "一个专辑 id 都没认出来")
	if len(lines) != 1 {
		t.Fatalf("这类诊断只该打一条，得到 %d 条：%v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "someUnknownParam") {
		t.Fatalf("诊断该给出 query 的**键名**（下次按它适配）：%s", lines[0])
	}
}

func TestAlbumDetailCapsTracks(t *testing.T) {
	anchor := albumTrack("wy", "0", "甲0", "甲", "大专辑")
	playable := map[string]bool{}
	songs := make([]search.UnifiedSong, 0, albumCandidateMax)
	for n := 0; n < albumCandidateMax; n++ {
		id := fmt.Sprintf("%d", n)
		playable[id] = true
		songs = append(songs, albumSong(id, "甲"+id, "甲", "大专辑"))
	}
	h, fake := albumHarness(t, anchor, songs...)
	h.setOnline(playable, songs...)

	w, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
	d := dataOf(t, w)
	if d["trackCount"] != float64(albumTrackMax) {
		t.Fatalf("一张专辑最多下发 %d 首，得到 %v", albumTrackMax, d["trackCount"])
	}
}

// ── 专辑曲目列表：分页 ──────────────────────────────────────────────────

func TestAlbumTrackListPaginates(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor,
		albumSong("2", "甲二", "甲", "同名专辑"),
		albumSong("3", "甲三", "甲", "同名专辑"),
	)
	target := "/music/api/v1/track/album-detail/list?albumGUID=" + fake + ":album"

	w, handled := h.do(http.MethodGet, target+"&page=1&size=2&sort=asc", "", nil)
	if !handled {
		t.Fatal("该认领")
	}
	d := dataOf(t, w)
	if d["total"] != float64(3) || asString(d["sort"]) != "asc" {
		t.Fatalf("信封该是 {list,total,sort}：total=%v sort=%v", d["total"], d["sort"])
	}
	if list := albumList(t, w); len(list) != 2 {
		t.Fatalf("第 1 页 2 条：%d", len(list))
	}
	w2, _ := h.do(http.MethodGet, target+"&page=2&size=2", "", nil)
	if list := albumList(t, w2); len(list) != 1 || asString(list[0]["title"]) != "甲三" {
		t.Fatalf("第 2 页该只剩 1 条：%#v", list)
	}
	// 越界页：空列表而 total 不变（客户端据此知道翻到底了）。
	w3, _ := h.do(http.MethodGet, target+"&page=9&size=2", "", nil)
	d3 := dataOf(t, w3)
	if list := albumList(t, w3); len(list) != 0 || d3["total"] != float64(3) {
		t.Fatalf("越界页该是空列表且 total 不变：%v / %v", list, d3["total"])
	}
	// size == -1 = 全量（官方约定）。
	w4, _ := h.do(http.MethodGet, target+"&size=-1", "", nil)
	if list := albumList(t, w4); len(list) != 3 {
		t.Fatalf("size=-1 该给全量 3 条：%d", len(list))
	}
}

func TestAlbumTrackListPassthroughOfficialAlbum(t *testing.T) {
	const body = `{"code":100005,"msg":"NotFound","data":null}`
	h := newHarness(t)
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, body)
	})
	w, _ := h.do(http.MethodGet,
		"/music/api/v1/track/album-detail/list?albumGUID="+officialGuid("f"), "", nil)
	if w.Body.String() != body {
		t.Fatalf("官方专辑该原样透传：%s", w.Body.String())
	}
}

// ── 端到端：从专辑搜索到取流 ─────────────────────────────────────────────

// 这条是本次改动的**主用例**：在线专辑能搜到 → 点进去有曲目 → 曲目真能播
// （取流走既有的「虚拟 id → 解析 → 流」链路，不是死条目）。
func TestOnlineAlbumEndToEndFromSearchToStream(t *testing.T) {
	var streams int
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streams++
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("AUDIO"))
	}))
	defer cdn.Close()

	h := newHarness(t)
	h.setOnline(map[string]bool{"1": true, "2": true},
		albumSong("1", "甲一", "甲", "同名专辑"),
		albumSong("2", "甲二", "甲", "同名专辑"),
	)
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"1": {URL: cdn.URL + "/1.mp3", Format: "mp3", Size: 5},
		"2": {URL: cdn.URL + "/2.mp3", Format: "mp3", Size: 5},
	}})
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	// ① 专辑搜索拿到一张在线专辑卡片。
	w, _ := h.do(http.MethodGet, "/music/api/v1/search/album?q=同名专辑", "", nil)
	cards := albumList(t, w)
	if len(cards) != 1 {
		t.Fatalf("该注入 1 张在线专辑：%v", albumNames(cards))
	}
	albumGUID := asString(cards[0]["guid"])

	// ② 点进专辑：详情有曲目。
	w2, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+albumGUID, "", nil)
	d2 := dataOf(t, w2)
	tracks, _ := d2["tracks"].([]any)
	if len(tracks) != 2 {
		t.Fatalf("专辑页该有 2 首：%#v", d2["tracks"])
	}

	// ③ 曲目列表也认同一个 guid（客户端两个端点都会调）。
	w3, _ := h.do(http.MethodGet,
		"/music/api/v1/track/album-detail/list?albumGUID="+albumGUID, "", nil)
	listed := albumList(t, w3)
	if len(listed) != 2 {
		t.Fatalf("曲目列表该有 2 首：%v", len(listed))
	}

	// ④ 曲目 guid 必须真的能取流（走既有解析链路，不是死条目）。
	for _, item := range listed {
		guid := asString(item["guid"])
		if guid == "" {
			t.Fatalf("曲目条目缺 guid：%#v", item)
		}
		ws, _ := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+guid, "", nil)
		if ws.Code != http.StatusOK || ws.Body.String() != "AUDIO" {
			t.Fatalf("专辑里的曲目必须能播（guid=%s）：code=%d body=%q", guid, ws.Code, ws.Body.String())
		}
	}
	if streams != 2 {
		t.Fatalf("该向 CDN 取 2 次流，得到 %d", streams)
	}
}

func TestAlbumTracksExcludeUnplayable(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor, albumSong("2", "甲二", "甲", "同名专辑"))
	// 「2」不在解析池里 → 不可播 → 不许进专辑页（与搜索同一条口径）。
	h.it.pool = online.NewPool(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"1": {URL: "https://cdn.invalid/1.mp3", Format: "mp3"},
	}})

	w, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
	d := dataOf(t, w)
	if d["trackCount"] != float64(1) {
		t.Fatalf("不可播的曲目不该进专辑页，得到 %v 首", d["trackCount"])
	}
}

func TestAlbumDetailUsesLocalSnapshotsWithoutSearch(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor)
	// 收藏里有一首同一张专辑的曲目（本地快照，零网络）。
	if err := h.it.store.AddFavorite("shared", online.FavoriteItem{
		GUID:      online.FakeID(online.RealID("wy", "9")),
		CreatedAt: 1700000000,
		Track:     albumTrack("wy", "9", "甲九", "甲", "同名专辑"),
	}); err != nil {
		t.Fatalf("写收藏失败：%v", err)
	}
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"wy": {"1": true, "9": true},
	})

	w, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
	d := dataOf(t, w)
	if d["trackCount"] != float64(2) {
		t.Fatalf("收藏里同一张专辑的曲目该被并进来，得到 %v 首", d["trackCount"])
	}
	first := d["tracks"].([]any)[0].(map[string]any)
	if asString(first["title"]) != "甲一" {
		t.Fatalf("锚点仍该排第一：%v", first["title"])
	}
}

func TestAlbumSynthesisIsCached(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h := newHarness(t)
	logs := captureLogs(h)
	h.it.searcher = func(keyword, platform string, page, size int) []search.UnifiedSong {
		return []search.UnifiedSong{albumSong("1", "甲一", "甲", "同名专辑")}
	}
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"1": true}})
	fake := h.it.registry.Put(anchor)

	for n := 0; n < 3; n++ {
		w, _ := h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
		if asString(dataOf(t, w)["guid"]) != fake+":album" {
			t.Fatalf("第 %d 次请求没拿到专辑：%s", n+1, w.Body.String())
		}
	}
	// 断言的是「**专辑合成**只发生了一次」，不是 searcher 被调了几次：collectOnline
	// 自己还有一层关键词缓存（search.go 的 onlineCacheTTL），专辑那层缓存坏掉时
	// searcher 照样只被调一次 —— 数它咬不住这条用例名字承诺的东西。
	// 合成路径上唯一可数的观察点就是这条日志：albumFor 命中缓存时**提前返回**，
	// 走不到它（曲目解析池自己也有缓存，所以也不能数解析次数）。
	if got := albumSynthesisLogs(logs()); len(got) != 1 {
		t.Fatalf("合成结果该进缓存（%s），3 次请求只该合成 1 次，得到 %d 次：%v",
			albumCacheTTL, len(got), got)
	}
}

// albumSynthesisLogs 挑出「在线专辑合成了一次」的日志行。
//
// 必须同时匹配「在线专辑」与「首可播」：collectOnline 自己也会打一条「在线搜索 …
// 首可播」（那是关键词缓存那层的，不是专辑合成），预算超时那条含「在线专辑」
// 却不含「首可播」。
func albumSynthesisLogs(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.Contains(l, "在线专辑") && strings.Contains(l, "首可播") {
			out = append(out, l)
		}
	}
	return out
}

// ── 缓存按用户隔离 ──────────────────────────────────────────────────────

// albumAuthKey 算出「带这个凭据头」的请求会落到哪个用户键。
//
// 刻意走拦截层自己的 `userKey`，不自己拼一个 `fp-...`：用例要钉的是
// 「凭据不同 → 缓存不串」，自己拼键就绕过了那道真身，等于在测一个不存在的约定。
func albumAuthKey(t *testing.T, it *Interceptor, auth string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/music/api/v1/album", nil)
	r.Header.Set("Authorization", auth)
	return it.userKey(r)
}

// albumTrackGUIDs 取出专辑详情里曲目的 guid 集合。
//
// 断言比**内容**（guid 集合），不比条数：条数相同但内容不同（各自拿到对方那首）
// 同样是串味，只看 trackCount 是咬不住的。
func albumTrackGUIDs(t *testing.T, w *httptest.ResponseRecorder, handled bool) map[string]bool {
	t.Helper()
	if !handled {
		t.Fatal("专辑路由该被认领（透传的话这条用例什么都没验）")
	}
	raw, ok := dataOf(t, w)["tracks"].([]any)
	if !ok {
		t.Fatalf("data.tracks 不是数组：%s", w.Body.String())
	}
	out := make(map[string]bool, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("data.tracks 里有非对象元素：%#v", item)
		}
		out[asString(m["guid"])] = true
	}
	return out
}

// TestAlbumCacheIsScopedPerUser 钉住「合成缓存按用户分区」。
//
// 合成结果里含**按用户隔离的本地快照**（albumTracks 的 store.Favorites /
// store.History）。缓存键只带专辑 guid 时，两个凭据在 TTL（120 秒）内请求同一张
// 专辑，第二个会直接命中第一个的合成结果 —— 收藏里的曲目串到别人页面上。
//
// 两个凭据必须在**同一个 harness**（同一个 Interceptor、同一个缓存）上请求，
// 这才是「同一台 NAS 上两个人」的形状；换 harness 就等于各有一份缓存，测不到。
func TestAlbumCacheIsScopedPerUser(t *testing.T) {
	const (
		authA = "Bearer user-a"
		authB = "Bearer user-b"
	)
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor)
	// 池子必须认「收藏里那首」：albumTracks 会把收藏曲目并进来，但可播过滤
	// （playableOnly）会把解析不出直链的剔掉 —— 不认它就等于这条用例没造出素材。
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"1": true, "9": true}})

	keyA := albumAuthKey(t, h.it, authA)
	keyB := albumAuthKey(t, h.it, authB)
	if keyA == keyB {
		t.Fatalf("两个凭据该算出不同的用户键（否则这条用例什么都没测）：A=%q B=%q", keyA, keyB)
	}
	if keyA == "shared" || keyB == "shared" {
		t.Fatalf("带凭据的请求不该落到共享桶：A=%q B=%q", keyA, keyB)
	}

	// A 的收藏里有一首同一张专辑的曲目（本地快照，零网络）。
	favFake := online.FakeID(online.RealID("wy", "9"))
	if err := h.it.store.AddFavorite(keyA, online.FavoriteItem{
		GUID:      favFake,
		CreatedAt: 1700000000,
		Track:     albumTrack("wy", "9", "甲九", "甲", "同名专辑"),
	}); err != nil {
		t.Fatalf("写收藏失败：%v", err)
	}

	target := "/music/api/v1/album?guid=" + fake + ":album"

	// ① A 先请求：他的合成结果里该有收藏那首（证明素材真的进了曲目集）。
	wA, okA := h.do(http.MethodGet, target, "", map[string]string{"Authorization": authA})
	gotA := albumTrackGUIDs(t, wA, okA)
	if !gotA[favFake] {
		t.Fatalf("A 的结果里该有他收藏的那首，否则这条用例没有串味的素材：%v", gotA)
	}

	// ② B 在 TTL 内请求**同一个专辑 guid**：只能拿到自己那份。
	wB, okB := h.do(http.MethodGet, target, "", map[string]string{"Authorization": authB})
	gotB := albumTrackGUIDs(t, wB, okB)
	if gotB[favFake] {
		t.Fatalf("B 拿到了 A 收藏里的曲目 —— 合成缓存跨用户串味。\nA 的曲目集：%v\nB 的曲目集：%v", gotA, gotB)
	}
	if !gotB[fake] {
		t.Fatalf("B 该拿到自己那份合成结果（至少含锚点）：%v", gotB)
	}
}

// TestAlbumCacheStillHitsForSameUser 是「加维度不是关缓存」的守卫。
//
// 最省事的「修法」是把缓存整个关掉 —— 那样跨用户当然不串，但客户端进专辑页会
// 连打两次完整合成（详情 + 曲目列表），上游搜索与解析翻倍。这条用例钉住：
// **同一个用户的重复请求仍然命中缓存**。
func TestAlbumCacheStillHitsForSameUser(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor)
	logs := captureLogs(h)
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"1": true}})

	headers := map[string]string{"Authorization": "Bearer user-a"}
	target := "/music/api/v1/album?guid=" + fake + ":album"
	for n := 0; n < 3; n++ {
		w, _ := h.do(http.MethodGet, target, "", headers)
		if asString(dataOf(t, w)["guid"]) != fake+":album" {
			t.Fatalf("第 %d 次请求没拿到专辑：%s", n+1, w.Body.String())
		}
	}
	// 与 TestAlbumSynthesisIsCached 同一个观察点（合成路径上唯一可数的日志）。
	if got := albumSynthesisLogs(logs()); len(got) != 1 {
		t.Fatalf("同一个用户的重复请求该命中缓存（%s），3 次只该合成 1 次，得到 %d 次：%v",
			albumCacheTTL, len(got), got)
	}
}

// TestAlbumCacheAnonymousBucketIsShared 钉住「无凭据 → shared」那条既有约定
// 没有被这次改动拆散：所有匿名请求共用**一个**桶（不是每个请求一个新键），
// 所以它们之间仍然互相命中缓存。
func TestAlbumCacheAnonymousBucketIsShared(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor)
	logs := captureLogs(h)
	h.it.pool = chartResolverPool(map[string]map[string]bool{"wy": {"1": true}})

	if got := h.it.userKey(mustRequest(t, "/music/api/v1/album", "")); got != "shared" {
		t.Fatalf("无凭据该落到 shared（既有约定）：%q", got)
	}

	target := "/music/api/v1/album?guid=" + fake + ":album"
	for n := 0; n < 3; n++ {
		w, _ := h.do(http.MethodGet, target, "", nil)
		if asString(dataOf(t, w)["guid"]) != fake+":album" {
			t.Fatalf("第 %d 次匿名请求没拿到专辑：%s", n+1, w.Body.String())
		}
	}
	if got := albumSynthesisLogs(logs()); len(got) != 1 {
		t.Fatalf("匿名请求该共用一个桶（shared），3 次只该合成 1 次，得到 %d 次：%v", len(got), got)
	}
}

// ── 只读 ────────────────────────────────────────────────────────────────

func TestAlbumRoutesAreReadOnly(t *testing.T) {
	anchor := albumTrack("wy", "1", "甲一", "甲", "同名专辑")
	h, fake := albumHarness(t, anchor, albumSong("2", "甲二", "甲", "同名专辑"))
	h.setOfficial("GET /music/api/v1/search/album", http.StatusOK,
		`{"code":0,"msg":"","data":{"list":[],"total":0}}`)

	h.do(http.MethodGet, "/music/api/v1/search/album?q=同名专辑", "", nil)
	h.do(http.MethodGet, "/music/api/v1/album?guid="+fake+":album", "", nil)
	h.do(http.MethodGet, "/music/api/v1/track/album-detail/list?albumGUID="+fake+":album", "", nil)

	// ① 不往官方写任何东西：官方只收到搜索那一条 GET。
	for _, c := range h.up.calls() {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("专辑这几条只读路由不该对官方发写请求，看到 %s", c)
		}
	}
	// ② 不往本地存储写任何东西（收藏 / 歌单 / 历史都不该因为浏览专辑而落盘）。
	//
	// ⚠️ 那三个**目录**是 online.NewStore 构造时就建好的（store.go:71），
	// 所以这里查的是「目录里有没有文件」，不是目录在不在。
	for _, kind := range []string{"favorites", "playlist_tracks", "play_history"} {
		entries, err := os.ReadDir(filepath.Join(h.dir, kind))
		if err != nil {
			t.Fatalf("读 %s 失败：%v", kind, err)
		}
		if len(entries) != 0 {
			t.Fatalf("浏览在线专辑不该往 %s 写文件，看到 %v", kind, entries)
		}
	}
}
