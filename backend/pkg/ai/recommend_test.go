package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 「每日推荐的大模型层」的提示词与解析用例。
//
// 这一层有两条容易写错、又都不容易在真机上发现的规矩，用例重点盯它们：
//  1. **发出去的就是那三个字段** —— 提示词里多塞一个字段（时间戳 / 平台 / id）
//     就是把行为数据发了出去，而这是从代码上看不出来的；
//  2. **解析宽容、但不信任** —— 网关随机换模型、偶尔回空 content、还按 SSE 返回。
//     解析必须兜得住这些形态，但兜住的产物仍然是不可信的文字。

// ── 提示词 ──────────────────────────────────────────────────────────────

func TestRecommendPromptCarriesSeedsAndContract(t *testing.T) {
	p := buildRecommendPrompt(RecommendRequest{
		Count:     7,
		Recent:    []RecommendSeed{{Title: "晴天", Artist: "周杰伦", Album: "叶惠美"}},
		Favorites: []RecommendSeed{{Title: "海屿你", Artist: "张杰"}},
	})

	for _, want := range []string{"晴天", "周杰伦", "叶惠美", "海屿你", "张杰"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词里少了种子字段 %q", want)
		}
	}
	// 条数要写进提示词（模型不会自己猜）。
	if !strings.Contains(p, "7") {
		t.Error("提示词里没写要几条")
	}
	// 输出契约：四个字段名 + 「只回 JSON 数组」+ 「不要推荐已有的组合」。
	for _, want := range []string{"title", "artist", "album", "reason", "JSON", "不要推荐"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词里少了契约要素 %q", want)
		}
	}
}

func TestRecommendPromptEmptySeedBlocks(t *testing.T) {
	p := buildRecommendPrompt(RecommendRequest{})
	if !strings.Contains(p, "（暂无最近播放）") || !strings.Contains(p, "（暂无收藏）") {
		t.Errorf("两个种子块都空时应写占位说明，得到：\n%s", p)
	}
}

// TestRecommendPromptCapsWhatItSends 钉住「最多发多少条」。
//
// 这是隐私面的一部分：把上限调大就是多发用户的收听数据出去。
//
// ⚠️ 断言里写的是**字面量 20**，不是 `RecommendMaxRecent`。写常量的话，
// 改常量时造的数据和断言会一起变，用例永远绿 —— 那就等于没钉住。
func TestRecommendPromptCapsWhatItSends(t *testing.T) {
	const limit = 20

	recent := make([]RecommendSeed, 0, limit+5)
	for i := 0; i < limit+5; i++ {
		recent = append(recent, RecommendSeed{Title: recentTitle(i), Artist: "甲"})
	}
	favs := make([]RecommendSeed, 0, limit+5)
	for i := 0; i < limit+5; i++ {
		favs = append(favs, RecommendSeed{Title: favTitle(i), Artist: "乙"})
	}

	p := buildRecommendPrompt(RecommendRequest{Recent: recent, Favorites: favs})

	// 第 limit 条在（下标 limit-1），第 limit+1 条不在。
	if !strings.Contains(p, recentTitle(limit-1)) {
		t.Error("最近收听的第 20 条应该在提示词里")
	}
	if strings.Contains(p, recentTitle(limit)) {
		t.Error("最近收听超过 20 条不该进提示词")
	}
	if !strings.Contains(p, favTitle(limit-1)) {
		t.Error("收藏的第 20 条应该在提示词里")
	}
	if strings.Contains(p, favTitle(limit)) {
		t.Error("收藏超过 20 条不该进提示词")
	}
}

// TestRecommendPromptSkipsEmptyTitles 空歌名的种子不发（发出去是纯噪声）。
func TestRecommendPromptSkipsEmptyTitles(t *testing.T) {
	p := buildRecommendPrompt(RecommendRequest{
		Recent: []RecommendSeed{{Title: "  ", Artist: "甲"}, {Title: "有名字", Artist: "乙"}},
	})
	if !strings.Contains(p, "有名字") {
		t.Error("非空种子应该进提示词")
	}
	if strings.Contains(p, "1. 《》") {
		t.Error("空歌名不该拼成一条种子")
	}
}

func TestRecommendCountDefaultsAndCaps(t *testing.T) {
	if p := buildRecommendPrompt(RecommendRequest{}); !strings.Contains(p, itoaForTest(RecommendDefaultCount)) {
		t.Errorf("没指定条数时应该用默认值 %d", RecommendDefaultCount)
	}
	big := buildRecommendPrompt(RecommendRequest{Count: 999})
	if strings.Contains(big, "999") {
		t.Error("条数应该被上限截住")
	}
	if !strings.Contains(big, itoaForTest(RecommendMaxCount)) {
		t.Errorf("超上限时应截到 %d", RecommendMaxCount)
	}
}

func recentTitle(i int) string { return "最近曲目" + itoaForTest(i) }
func favTitle(i int) string    { return "收藏曲目" + itoaForTest(i) }

func itoaForTest(n int) string { return strconv.Itoa(n) }

// ── 解析 ────────────────────────────────────────────────────────────────

// TestParseRecommendCandidatesTolerant 覆盖模型回复的各种形态。
//
// 「宽容 ≠ 信任」：这里只验「能读出来的都读出来了」，读出来的东西是不是真歌，
// 由 intercept 那侧的搜索匹配负责（见 vdaily_llm_test.go）。
func TestParseRecommendCandidatesTolerant(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []RecommendCandidate
	}{
		{
			name: "纯数组",
			raw:  `[{"title":"晴天","artist":"周杰伦","album":"叶惠美","reason":"同语种"}]`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦", Album: "叶惠美", Reason: "同语种"}},
		},
		{
			name: "markdown 代码块",
			raw:  "```json\n[{\"title\":\"晴天\",\"artist\":\"周杰伦\"}]\n```",
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			// 这一条是「先剥代码块」那一步**唯一**不可替代的输入：回复里在代码块
			// **之前**还有一个方括号，`\[.*\]` 的贪婪匹配会从那个方括号一路抓到
			// 结尾，拼出非法 JSON。先剥出代码块内的完整内容才解得开。
			name: "代码块之前还有一个方括号",
			raw:  "好的[已按要求]，结果如下：\n```json\n[{\"title\":\"晴天\",\"artist\":\"周杰伦\"}]\n```",
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "前后夹带解释文字",
			raw:  "好的，这是推荐：\n[{\"title\":\"晴天\",\"artist\":\"周杰伦\"}]\n希望你喜欢。",
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "数组包在对象里",
			raw:  `{"songs":[{"title":"晴天","artist":"周杰伦"}]}`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			// 对象里在数组**之后**还有一个方括号（说明性文字里带的），
			// `\[.*\]` 的贪婪匹配会连它一起吃进去 → 非法 JSON。
			// 这一条只有「解成对象再按键往下钻」那条路能过。
			name: "对象包裹 + 数组之后还有方括号",
			raw:  `{"songs":[{"title":"晴天","artist":"周杰伦"}],"note":"见[附录]"}`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "换个包裹键",
			raw:  `{"recommendations":[{"title":"晴天","artist":"周杰伦"}]}`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "只回歌名字符串",
			raw:  `["晴天","海屿你"]`,
			want: []RecommendCandidate{{Title: "晴天"}, {Title: "海屿你"}},
		},
		{
			name: "字段名换了",
			raw:  `[{"name":"晴天","singer":"周杰伦"}]`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "歌手是数组",
			raw:  `[{"title":"晴天","artists":["周杰伦","方文山"]}]`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦/方文山"}},
		},
		{
			name: "歌名带书名号与引号",
			raw:  `[{"title":"《晴天》","artist":"\"周杰伦\""}]`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "缺歌名的项被丢掉",
			raw:  `[{"artist":"周杰伦"},{"title":"晴天","artist":"周杰伦"}]`,
			want: []RecommendCandidate{{Title: "晴天", Artist: "周杰伦"}},
		},
		{
			name: "占位歌名被丢掉",
			raw:  `[{"title":"无","artist":"甲"},{"title":"未知","artist":"乙"}]`,
			want: nil,
		},
		{
			name: "空回复",
			raw:  "",
			want: nil,
		},
		{
			name: "只有空白",
			raw:  "   \n\t ",
			want: nil,
		},
		{
			name: "根本不是 JSON",
			raw:  "抱歉，我无法完成这个请求。",
			want: nil,
		},
		{
			name: "是 JSON 但没有数组",
			raw:  `{"error":"boom"}`,
			want: nil,
		},
		{
			name: "数组里全是垃圾",
			raw:  `[1,2,{"x":1},null]`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRecommendCandidates(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("条数 = %d，想要 %d（得到 %+v）", len(got), len(tc.want), got)
			}
			// 「一条都没读出来」必须是 **nil**，不是空切片：调用方靠它一眼分清
			// 「这次没有」与「读出来了但都不可用」。
			if tc.want == nil && got != nil {
				t.Errorf("什么都读不出来时该返回 nil，得到非 nil 的空切片")
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 条 = %+v，想要 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseRecommendCandidatesDedupes(t *testing.T) {
	// 同一个「歌名+歌手」写两遍（大小写 / 空白不同）只留一条。
	raw := `[{"title":"晴天","artist":"周杰伦"},{"title":" 晴天 ","artist":"周杰伦"},` +
		`{"title":"晴天","artist":"周杰伦 "},{"title":"海屿你","artist":"张杰"}]`
	got := parseRecommendCandidates(raw)
	if len(got) != 2 {
		t.Fatalf("去重后应剩 2 条，得到 %d：%+v", len(got), got)
	}
}

func TestParseRecommendCandidatesTruncates(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < RecommendMaxCount+10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title":"曲目`)
		b.WriteString(itoaForTest(i))
		b.WriteString(`","artist":"甲"}`)
	}
	b.WriteString("]")

	got := parseRecommendCandidates(b.String())
	if len(got) != RecommendMaxCount {
		t.Fatalf("应截到 %d 条，得到 %d", RecommendMaxCount, len(got))
	}
}

// ── 调用（含用户那台网关的两种怪癖）──────────────────────────────────────

// TestRecommendSilentWhenNotConfigured 钉住「未配置 AI 时静默降级」。
//
// 这条是项目既有约定（见 pkg/ai 包注释与 HANDOVER §3.5）：所有方法首行返回空结果，
// 主流程零感知。每日推荐那一层靠它决定「这一层整个跳过」。
func TestRecommendSilentWhenNotConfigured(t *testing.T) {
	for _, cfg := range []Config{
		DefaultConfig(),                              // 未启用
		{Enabled: true, BaseURL: "https://x"},        // 缺 key
		{Enabled: true, APIKey: "k"},                 // 缺 base_url
		{Enabled: true, BaseURL: "  ", APIKey: "  "}, // 全是空白
	} {
		c := NewClient(cfg)
		if got := c.Recommend(context.Background(), RecommendRequest{}); got != nil {
			t.Errorf("未配置时该静默返回 nil，得到 %+v", got)
		}
	}
}

// TestRecommendEmptyContentIsNotAnError 是用户那台网关的第一个怪癖：
// **每次随机路由到不同免费模型，偶尔返回空 content（不是 bug，重试即可）**。
//
// 空回复必须表现成「这一层没结果」，不能报错、更不能被当成「模型说没有歌」。
func TestRecommendEmptyContentIsNotAnError(t *testing.T) {
	srv := chatServer(t, "", nil, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5})
	if got := c.Recommend(context.Background(), RecommendRequest{Count: 3}); got != nil {
		t.Fatalf("空 content 应静默返回 nil，得到 %+v", got)
	}
}

// TestRecommendParsesSSEGateway 是第二个怪癖：**网关忽略 stream:false，始终按 SSE 返回**。
func TestRecommendParsesSSEGateway(t *testing.T) {
	srv := sseChatServer(t, `[{"title":"晴天","artist":"周杰伦"}]`)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5})
	got := c.Recommend(context.Background(), RecommendRequest{Count: 1})
	if len(got) != 1 || got[0].Title != "晴天" || got[0].Artist != "周杰伦" {
		t.Fatalf("SSE 网关的回复没解析出来：%+v", got)
	}
}

// TestRecommendRecordsUsage 钉住「这一层要计成本」。
//
// 空回复也要记一笔 —— 「没回明细就当没花钱」是明令禁止的（见 pkg/ai 包注释）。
func TestRecommendRecordsUsage(t *testing.T) {
	srv := chatServer(t, "", nil, 0) // 空回复，且响应里没有 usage 明细
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5})
	_ = c.Recommend(context.Background(), RecommendRequest{})

	sum := c.Usage(10)
	st, ok := sum.ByKind["recommend"]
	if !ok || st.Calls != 1 {
		t.Fatalf("用量里应有 1 笔 kind=recommend，得到 %+v", sum.ByKind)
	}
	if sum.MissingUsage != 1 {
		t.Errorf("接口没回 usage 明细时应记一笔 missing，得到 %d", sum.MissingUsage)
	}
}

// TestRecommendHTTPErrorIsNotFatal 网关 500 时也只是「这一层没结果」。
func TestRecommendHTTPErrorIsNotFatal(t *testing.T) {
	srv := chatServer(t, "", nil, http.StatusInternalServerError)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5})
	if got := c.Recommend(context.Background(), RecommendRequest{}); got != nil {
		t.Fatalf("网关报错时应静默返回 nil，得到 %+v", got)
	}
}

// sseChatServer 造一个「忽略 stream:false、按 SSE 逐片返回」的网关替身，
// 并把 content 拆成几片发（真网关就是这么发的）。
func sseChatServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		// 先来一片只有 role 的（真网关会这样），再逐片给正文。
		write(`{"choices":[{"delta":{"role":"assistant"}}]}`)
		runes := []rune(content)
		for i := 0; i < len(runes); i += 8 {
			end := i + 8
			if end > len(runes) {
				end = len(runes)
			}
			b, _ := json.Marshal(string(runes[i:end]))
			write(`{"choices":[{"delta":{"content":` + string(b) + `}}]}`)
		}
		write(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`)
		write(`[DONE]`)
	}))
}
