package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// summarizeResult 绝不能把成功的响应提炼成空字符串。
//
// 守的是用户实测报回来的坑：助手调 catalog_list 后「返回空，没有可替代的统计入口」。
// 根因是 /api/catalog 把内容铺在**顶层**（没有 data 信封、message 又是 "ok"），
// 老逻辑只拼 data + message，于是 b.Len()==0 返回空串 —— 模型拿到的工具结果
// 是一片空白，只能回一句「查不到」。这类接口不止一个，所以兜底放在 summarizeResult，
// 不放单个能力里。
func TestSummarizeResultNeverReturnsEmpty(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // 必须包含的片段
	}{
		{
			name: "顶层铺开的对象（/api/catalog 这类）",
			body: `{"code":200,"message":"ok","app":"qulv (yinshu-ai)","version":"2.26.0",
			        "endpoints":[{"method":"GET","path":"/api/health","summary":"健康检查"},
			                             {"method":"POST","path":"/api/download/song","summary":"下载"}],
			        "guide":{"what":["a","b"]},"usage":["x"]}`,
			want: "/api/health",
		},
		{
			name: "标准 data 信封",
			body: `{"code":200,"message":"ok","data":{"total":4,"with_lyric":2}}`,
			want: "with_lyric",
		},
		{
			name: "非 JSON 响应",
			body: `plain text body`,
			want: "plain text",
		},
		{
			name: "空对象",
			body: `{}`,
			want: "{",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summarizeResult(200, []byte(c.body), "")
			if strings.TrimSpace(got) == "" {
				t.Fatalf("summarizeResult 返回了空字符串（模型会以为没查到）")
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("结果里应包含 %q，实际：%s", c.want, got)
			}
		})
	}
}

// 顶层数组条目要按「METHOD path — 摘要」列出来，而不是丢半截 JSON。
func TestSummarizeFlatListsEndpointRows(t *testing.T) {
	body := `{"code":200,"message":"ok","app":"qulv","version":"2.26.0",
	          "endpoints":[{"method":"GET","path":"/api/a","summary":"甲"},
	                       {"method":"GET","path":"/api/b","summary":"乙"}]}`
	got := summarizeFlat([]byte(body), "")
	for _, want := range []string{"app: qulv", "version: 2.26.0", "endpoints: 共 2 条", "GET /api/a — 甲", "GET /api/b — 乙"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要里应有 %q，实际：\n%s", want, got)
		}
	}
}

// library_stats 能力必须能把 dir 传出去（可选），否则接口侧的默认目录永远收不到它。
//
// 两边是一个契约的两半：能力不带 dir + 接口硬要 dir = 该能力恒 400（用户遇到的正是这个）；
// 只修接口不修能力，模型仍然没法显式指定目录。
func TestLibraryStatsCapabilityForwardsDir(t *testing.T) {
	cap := FindCapability("library_stats")
	if cap == nil {
		t.Fatal("library_stats 能力不存在")
	}

	// 不传 dir：请求里不能出现 dir（由后端回退配置目录）
	req, err := cap.Build(map[string]any{})
	if err != nil {
		t.Fatalf("Build 失败：%v", err)
	}
	if q := req.URL.Query().Get("dir"); q != "" {
		t.Errorf("省略 dir 时不应凭空塞一个，实际 dir=%q", q)
	}
	if req.URL.Path != "/api/library/index/stats" {
		t.Errorf("路径 = %q", req.URL.Path)
	}

	// 传了 dir：必须原样转发
	req, err = cap.Build(map[string]any{"dir": "/vol1/Music"})
	if err != nil {
		t.Fatalf("Build 失败：%v", err)
	}
	if q := req.URL.Query().Get("dir"); q != "/vol1/Music" {
		t.Errorf("dir 应转发，实际 %q", q)
	}

	// 工具 schema 里要声明 dir，否则模型根本不知道能传。
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(ToolParamsSchema(cap), &schema); err != nil {
		t.Fatalf("解析 schema 失败：%v", err)
	}
	if _, ok := schema.Properties["dir"]; !ok {
		t.Error("library_stats 的 schema 应包含 dir 属性（可选）")
	}
}

// 端到端：catalog_list 走一遍真实执行循环，模型拿到的话术不能是空，
// 且必须是可用的接口清单摘要（而不是「查不到」）。
func TestCatalogListToolResultIsUsable(t *testing.T) {
	exec := &fakeExecutor{
		status: 200,
		body: []byte(`{"code":200,"message":"ok","app":"qulv (yinshu-ai)","version":"2.26.0",
		               "endpoints":[{"method":"GET","path":"/api/library/index/stats","summary":"曲库统计"}]}`),
	}
	rt := newTestRuntime(exec)
	emit, got := collectEvents()

	out := rt.executeCall(context.Background(), "s1",
		APICall{ID: "call_1", Name: "catalog_list", Args: map[string]any{}},
		ModeReadOnly, nil, emit)

	if strings.TrimSpace(out.content) == "" {
		t.Fatalf("catalog_list 的工具结果为空（模型只能回「查不到」）：%v", eventTypes(*got))
	}
	for _, want := range []string{"/api/library/index/stats", "qulv"} {
		if !strings.Contains(out.content, want) {
			t.Errorf("工具结果应包含 %q，实际：\n%s", want, out.content)
		}
	}
	if len(exec.calls) != 1 || exec.calls[0].URL.Path != "/api/catalog" {
		t.Errorf("应只调用一次 /api/catalog，实际：%v", exec.calls)
	}
}

// 界面上工具卡的预览只取首行短摘要；模型拿到的是完整结果。
func TestToolPreviewIsShortFirstLine(t *testing.T) {
	full := "app: qulv\nendpoints: 共 181 条\n  GET /api/health — 健康检查"
	got := toolPreview(full)
	if strings.Contains(got, "\n") {
		t.Errorf("预览不应带换行：%q", got)
	}
	if got != "app: qulv" {
		t.Errorf("预览应为首行，实际 %q", got)
	}
	if long := toolPreview(strings.Repeat("x", 500)); len(long) > 200+len("…（已截断）") {
		t.Errorf("超长预览应截断，实际 %d 字节", len(long))
	}
}

// 过滤词生效时：命中的接口要出现（哪怕排在 181 条的末尾），无关的只报数量。
func TestSummarizeFlatFilterExpandsHits(t *testing.T) {
	var arr []string
	for i := 0; i < 150; i++ {
		arr = append(arr, fmt.Sprintf(`{"method":"GET","path":"/api/misc/%d","summary":"无关"}`, i))
	}
	arr = append(arr, `{"method":"GET","path":"/api/library/gaps","summary":"曲库缺失信息"}`)
	body := `{"code":200,"message":"ok","endpoints":[` + strings.Join(arr, ",") + `]}`

	// 不带过滤：150 条之外的尾部条目会被截掉
	plain := summarizeFlat([]byte(body), "")
	if strings.Contains(plain, "/api/library/gaps") {
		t.Fatalf("不带过滤时应当截断，不该凭空看到尾部条目")
	}

	// 带过滤：尾部条目必须出现，无关的省略
	filtered := summarizeFlat([]byte(body), "library")
	if !strings.Contains(filtered, "GET /api/library/gaps — 曲库缺失信息") {
		t.Errorf("过滤后应展开命中条目，实际：\n%s", filtered)
	}
	if !strings.Contains(filtered, "与「library」无关，已省略") {
		t.Errorf("应交代省略了多少条，实际：\n%s", filtered)
	}
}

// catalog_list 必须把 filter 声明进工具 schema，否则模型永远传不了这个参数
// （实测就出现过：模型说「这个工具不接受参数」，然后拿到被截断的全量清单）。
func TestCatalogListSchemaDeclaresFilter(t *testing.T) {
	cap := FindCapability("catalog_list")
	if cap == nil {
		t.Fatal("catalog_list 能力不存在")
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(ToolParamsSchema(cap), &schema); err != nil {
		t.Fatalf("解析 schema 失败：%v", err)
	}
	if _, ok := schema.Properties["filter"]; !ok {
		t.Fatal("catalog_list 的 schema 应包含 filter")
	}
	req, err := cap.Build(map[string]any{"filter": "library"})
	if err != nil {
		t.Fatalf("Build 失败：%v", err)
	}
	if got := req.URL.Query().Get("filter"); got != "library" {
		t.Errorf("filter 应转发到请求，实际 %q", got)
	}
}

// 「共 N 条」只能出现一次：之前 summarizeFlat 和 appendArrayRows 各打一遍，
// 结果模型看到 `endpoints: 共 28 条` 连着两行。
func TestArrayHeaderPrintedOnce(t *testing.T) {
	body := `{"code":200,"message":"ok","endpoints":[
	    {"method":"GET","path":"/api/a","summary":"甲"},
	    {"method":"GET","path":"/api/b","summary":"乙"}]}`
	got := summarizeFlat([]byte(body), "")
	if n := strings.Count(got, "endpoints: 共 2 条"); n != 1 {
		t.Errorf("「endpoints: 共 2 条」应只出现一次，实际 %d 次：\n%s", n, got)
	}
}

// usage 这类字符串数组不是接口清单：不能套用关键词过滤，
// 也不能对它报「没有匹配关键词」（模型会以为这块内容不存在）。
func TestPlainStringArrayNotFiltered(t *testing.T) {
	body := `{"code":200,"message":"ok","endpoints":[
	    {"method":"GET","path":"/api/library/gaps","summary":"缺口"}],
	    "usage":["【下载一首歌】GET /api/search → POST /api/download/song",
	             "【订阅歌单】POST /api/monitors"]}`
	got := summarizeFlat([]byte(body), "library")
	if strings.Contains(got, "没有匹配「library」的条目") {
		t.Errorf("不该对 usage 报「没有匹配」：\n%s", got)
	}
	if !strings.Contains(got, "usage: 共 2 条") {
		t.Errorf("usage 应照原样列出条数：\n%s", got)
	}
	if !strings.Contains(got, "【下载一首歌】") {
		t.Errorf("usage 内容应保留：\n%s", got)
	}
	if !strings.Contains(got, "/api/library/gaps") {
		t.Errorf("接口清单的过滤仍应生效：\n%s", got)
	}
}
