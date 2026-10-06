package intercept

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fn-lx-player/pkg/search"
)

// 搜索联想（`/music/api/v1/search/suggest`）的用例，实现在 suggest.go。
//
// # 关于官方响应的形状
//
// 这里用的官方夹具 `{"code":0,"msg":"ok","data":["本地周杰伦"]}` **不是真机抓包** ——
// 它取自参考实现 fnme-z 的用例（`proxy/tests/test_merge.py::test_search_suggest_merge`，
// 那个实现代理的是同一个 `trim_music` unix socket）。开发机上官方页面不可达
// （只有曲率的 8898 / 旧版 8899 / AI 网关 20128），所以本地的形状只能到这个程度。
//
// 正因为是二手的，用例里**一半在钉「形状不认识时怎么办」**：
// 非字符串数组一律原样透传、绝不改官方那一个字。这样最坏情况是「在线词没进去」，
// 而不是「官方联想被搞坏」。

// setSuggest 设定官方联想接口返回什么。
func (h *harness) setSuggest(status int, body string) {
	h.setOfficial("GET /music/api/v1/search/suggest", status, body)
}

// suggestWords 取响应里的 data（联想词数组）。
func suggestWords(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	env := decode(t, w)
	raw, ok := env["data"].([]any)
	if !ok {
		t.Fatalf("data 不是数组：%#v", env["data"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("data 里有非字符串元素：%#v", v)
		}
		out = append(out, s)
	}
	return out
}

func equalWords(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── 合并 ────────────────────────────────────────────────────────────────

func TestSuggestMergesOnlineTitlesAfterOfficial(t *testing.T) {
	h := newHarness(t)
	h.setOnline(nil,
		song("1", "周杰伦 晴天", "周杰伦"),
		song("2", "周杰伦 七里香", "周杰伦"),
	)
	// 官方那一条用参考实现里同一个形状（见文件头）。
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"ok","data":["本地周杰伦"]}`)

	w, handled := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=周杰伦", "", nil)
	if !handled {
		t.Fatal("这条路由该被拦截层认领（认领了才谈得上合并）")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("状态码该保持 200，得到 %d", w.Code)
	}
	got := suggestWords(t, w)
	want := []string{"本地周杰伦", "周杰伦 晴天", "周杰伦 七里香"}
	if !equalWords(got, want) {
		t.Fatalf("官方词在前、在线词补在后：want %v\ngot  %v", want, got)
	}
	// 官方信封的其它字段一个都不能动
	env := decode(t, w)
	if env["msg"] != "ok" {
		t.Fatalf("msg 该原样保留，得到 %v", env["msg"])
	}
	if env["code"] != float64(0) {
		t.Fatalf("code 该原样保留，得到 %v", env["code"])
	}
}

func TestSuggestDeduplicatesOfficialAndOnline(t *testing.T) {
	h := newHarness(t)
	// 在线源给出：一条与官方完全重合、一条只有大小写/空白差异、一条新的。
	h.setOnline(nil,
		song("1", "本地周杰伦", "周杰伦"),
		song("2", "  周杰伦   晴天 ", "周杰伦"),
		song("3", "周杰伦 晴天", "周杰伦"), // 归一化后与上一条重合
	)
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"","data":["本地周杰伦","Hello"]}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=hei", "", nil)
	got := suggestWords(t, w)
	// 官方词原样、不重复；在线词去掉首尾空白、只留一条。
	want := []string{"本地周杰伦", "Hello", "周杰伦   晴天"}
	if !equalWords(got, want) {
		t.Fatalf("去重后的结果不对：want %v\ngot  %v", want, got)
	}
}

func TestSuggestDedupeIgnoresCase(t *testing.T) {
	h := newHarness(t)
	h.setOnline(nil, song("1", "hello", "x"))
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"","data":["Hello"]}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=hello", "", nil)
	if got := suggestWords(t, w); !equalWords(got, []string{"Hello"}) {
		t.Fatalf("大小写不同不该重复出现：%v", got)
	}
}

func TestSuggestCapsOnlineWords(t *testing.T) {
	h := newHarness(t)
	// 12 条在线歌名（比上限多），只看并入的前 5 条。
	many := make([]search.UnifiedSong, 0, 12)
	for i := 0; i < 12; i++ {
		many = append(many, song(fmt.Sprintf("id%d", i), fmt.Sprintf("在线歌%d", i), "甲"))
	}
	h.setOnline(nil, many...)
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"","data":["官方词"]}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=test", "", nil)
	got := suggestWords(t, w)
	if len(got) != 1+suggestOnlineMax {
		t.Fatalf("在线词最多并入 %d 条（官方 1 条 + 在线 %d 条），得到 %d：%v",
			1+suggestOnlineMax, suggestOnlineMax, len(got), got)
	}
	if got[0] != "官方词" {
		t.Fatalf("官方词必须在最前：%v", got)
	}
}

// ── 降级：不许拖慢、不许变成错误 ─────────────────────────────────────────

func TestSuggestSlowOnlineReturnsOfficialOnly(t *testing.T) {
	// 联想是每次按键都发的请求 —— 在线源卡住时必须只回官方那份，
	// 既不能等它、更不能变成错误。
	old := suggestOnlineBudget
	suggestOnlineBudget = 100 * time.Millisecond
	t.Cleanup(func() { suggestOnlineBudget = old })

	h := newHarness(t)
	// 假平台慢到 2 秒（**有界**的慢，不是永久挂住）：这样「等它的那条分支」
	// 就算被改坏也不会把用例挂死，而是**超时失败** —— 变异校验才跑得动。
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		time.Sleep(2 * time.Second)
		return []search.UnifiedSong{song("1", "太晚了", "甲")}
	}
	const official = `{"code":0,"msg":"ok","data":["官方词"]}`
	h.setSuggest(http.StatusOK, official)

	start := time.Now()
	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=慢", "", nil)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("在线源挂住不该拖慢联想（预算 %s），实际等了 %v", suggestOnlineBudget, elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("超时不是错误：状态码该是 200，得到 %d", w.Code)
	}
	if got := suggestWords(t, w); !equalWords(got, []string{"官方词"}) {
		t.Fatalf("超时后该只回官方结果：%v", got)
	}
	// 而且必须是**逐字节**原样：没有可合并的在线词时连序列化都不做。
	if w.Body.String() != official {
		t.Fatalf("没有在线词时该原样透传官方响应：\nwant %s\ngot  %s", official, w.Body.String())
	}
}

func TestSuggestOnlineFailureIsSilent(t *testing.T) {
	// 在线那一路返回空（源全挂 / 关键词搜不到）—— 官方结果照常。
	h := newHarness(t)
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
	const official = `{"code":0,"msg":"","data":["官方词"]}`
	h.setSuggest(http.StatusOK, official)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=空", "", nil)
	if w.Code != http.StatusOK || w.Body.String() != official {
		t.Fatalf("在线无结果时该原样返回官方那份：code=%d body=%s", w.Code, w.Body.String())
	}
}

// ── 不该发请求的时候一个都别发 ──────────────────────────────────────────

func TestSuggestNoSourcesFiresNoRequest(t *testing.T) {
	// 「在线那一路要能在没有可用源时立刻返回，不发无谓请求」。
	// 配置层明确说「没有在线源」（PlatformsFunc 返回空）时就**不回落**默认平台 ——
	// 联想每次按键都发，多发的每一条都是白打第三方接口。
	h := newHarness(t)
	h.it.cfg.PlatformsFunc = func() []string { return nil }
	var calls int32
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	const official = `{"code":0,"msg":"","data":["官方词"]}`
	h.setSuggest(http.StatusOK, official)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=有词", "", nil)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("没有可用在线源时不该发任何搜索请求，发了 %d 次", n)
	}
	if w.Body.String() != official {
		t.Fatalf("该原样透传：%s", w.Body.String())
	}
}

func TestSuggestEmptyKeywordFiresNoRequest(t *testing.T) {
	h := newHarness(t)
	var calls int32
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"","data":["热门搜索"]}`)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=", "", nil)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("没有关键词就没有在线那一路，发了 %d 次", n)
	}
	if got := suggestWords(t, w); !equalWords(got, []string{"热门搜索"}) {
		t.Fatalf("官方词该原样返回：%v", got)
	}
}

// ── 形状不认识 / 上游失败：一个字都不许改 ────────────────────────────────

func TestSuggestPassesThroughUnknownShape(t *testing.T) {
	// 官方 data 不是「全字符串数组」时的每一种形态都必须逐字节透传。
	// 这条用例是「形状没拿到真机抓包」这个前提的保险：猜错了也只是没有在线词。
	cases := []struct {
		name string
		body string
	}{
		{"data 是对象", `{"code":0,"msg":"","data":{"list":["甲","乙"]}}`},
		{"data 是字符串", `{"code":0,"msg":"","data":"甲"}`},
		{"data 是 null", `{"code":0,"msg":"","data":null}`},
		{"data 里混了非字符串", `{"code":0,"msg":"","data":["甲",{"title":"乙"}]}`},
		{"data 是数字数组", `{"code":0,"msg":"","data":[1,2]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.setOnline(nil, song("1", "在线甲", "歌手"))
			h.setSuggest(http.StatusOK, tc.body)

			w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=甲", "", nil)
			if w.Body.String() != tc.body {
				t.Fatalf("形状不认识时必须逐字节原样透传：\nwant %s\ngot  %s", tc.body, w.Body.String())
			}
		})
	}
}

func TestSuggestPassesThroughUpstreamFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"未登录（业务码 99999）", http.StatusOK, `{"code":99999,"msg":"INVALID TOKEN","data":null}`},
		{"非 JSON（网关 HTML）", http.StatusOK, `<html><body>502 Bad Gateway</body></html>`},
		{"HTTP 401", http.StatusUnauthorized, `{"code":401,"msg":"unauthorized","data":null}`},
		{"HTTP 500", http.StatusInternalServerError, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.setOnline(nil, song("1", "在线甲", "歌手"))
			h.setSuggest(tc.status, tc.body)

			w, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=甲", "", nil)
			if w.Code != tc.status {
				t.Fatalf("状态码该原样透传：want %d got %d", tc.status, w.Code)
			}
			if w.Body.String() != tc.body {
				t.Fatalf("响应体该原样透传：\nwant %s\ngot  %s", tc.body, w.Body.String())
			}
		})
	}
}

// ── 路由 ────────────────────────────────────────────────────────────────

func TestSuggestClaimsSubpathForm(t *testing.T) {
	// 官方也用 `/search/suggest/<keyword>` 这种子路径写法（参考实现同样挂了两个
	// 装饰器）—— 一条前缀路由要同时罩住两种形态，**认领**是这里的关键：
	// 认领了至少还能合并；不认领就整个漏给官方。
	//
	// ⚠️ 关键词只从查询串取（keyword/q/query），**不从子路径猜** —— 参考实现的
	// `extract_keyword` 也是这样。子路径不一定是关键词（`/suggest/hot` 之类），
	// 猜错会把无关的在线词并进官方下拉。真机确认官方用的是子路径形态之后，
	// 再照实测把关键词取出来（改动只在 handleSuggest 开头一处）。
	h := newHarness(t)
	h.setOnline(nil, song("1", "在线甲", "歌手"))
	const official = `{"code":0,"msg":"","data":["官方词"]}`
	// 子路径形态走不到 setOfficial 的精确匹配，这里按前缀回。
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		if strings.HasPrefix(r.URL.Path, "/music/api/v1/search/suggest") {
			return jsonResp(http.StatusOK, official)
		}
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
	})

	w, handled := h.do(http.MethodGet, "/music/api/v1/search/suggest/甲", "", nil)
	if !handled {
		t.Fatal("子路径形态也该被认领")
	}
	if w.Body.String() != official {
		t.Fatalf("没有关键词时该原样透传：%s", w.Body.String())
	}

	// 子路径 + 查询串（官方两种写法并存时就是它）→ 照常合并。
	w2, _ := h.do(http.MethodGet, "/music/api/v1/search/suggest/甲?keyword=甲", "", nil)
	if got := suggestWords(t, w2); !equalWords(got, []string{"官方词", "在线甲"}) {
		t.Fatalf("带查询串的子路径形态要合并在线词：%v", got)
	}
}

func TestSuggestShapeNoteLoggedOnce(t *testing.T) {
	// 形状不认识时要留下**一条**诊断日志（多了会刷屏）—— 它是下一次
	// 「照实测适配」唯一的线索。
	var notes int32
	h := newHarness(t)
	h.it.logf = func(format string, args ...any) {
		if strings.Contains(format, "搜索联想：官方 data 的形状是") {
			atomic.AddInt32(&notes, 1)
		}
	}
	h.setSuggest(http.StatusOK, `{"code":0,"msg":"","data":{"list":["甲"]}}`)

	for i := 0; i < 3; i++ {
		if _, handled := h.do(http.MethodGet, "/music/api/v1/search/suggest?keyword=甲", "", nil); !handled {
			t.Fatal("该被认领")
		}
	}
	if n := atomic.LoadInt32(&notes); n != 1 {
		t.Fatalf("形状日志只该记一次，记了 %d 次", n)
	}
}
