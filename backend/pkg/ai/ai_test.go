package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── 配置与密钥处理 ──

func TestAvailableRequiresAllFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"全空", Config{}, false},
		{"只有 enabled", Config{Enabled: true}, false},
		{"缺 key", Config{Enabled: true, BaseURL: "https://x"}, false},
		{"缺 enabled", Config{BaseURL: "https://x", APIKey: "k"}, false},
		{"齐全", Config{Enabled: true, BaseURL: "https://x", APIKey: "k"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Available(); got != tc.want {
				t.Errorf("Available = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

func TestMaskKey(t *testing.T) {
	if got := MaskKey(""); got != "" {
		t.Errorf("空密钥 = %q", got)
	}
	if got := MaskKey("abc"); got != "****" {
		t.Errorf("短密钥应全遮蔽，得到 %q", got)
	}
	got := MaskKey("sk-1234567890abcdef")
	if !strings.Contains(got, "****") {
		t.Errorf("应含遮蔽标记，得到 %q", got)
	}
	if strings.Contains(got, "56789") {
		t.Errorf("不应泄露中段，得到 %q", got)
	}
}

// TestMergeSecret 密钥脱敏保持：前端整体回传配置时不能把 Key 抹掉
func TestMergeSecret(t *testing.T) {
	existing := "sk-real-key-value"

	if got := MergeSecret("", existing); got != existing {
		t.Errorf("空值应沿用旧密钥，得到 %q", got)
	}
	if got := MergeSecret("sk-1****ef", existing); got != existing {
		t.Errorf("含 **** 的脱敏值应沿用旧密钥，得到 %q", got)
	}
	if got := MergeSecret("sk-new-key", existing); got != "sk-new-key" {
		t.Errorf("新值应覆盖，得到 %q", got)
	}
}

func TestUpdateConfigKeepsSecretOnMaskedInput(t *testing.T) {
	c := NewClient(Config{Enabled: true, BaseURL: "https://x", APIKey: "sk-secret", Model: "m"})

	// 模拟前端把脱敏后的配置整体回传
	c.UpdateConfig(Config{Enabled: true, BaseURL: "https://y", APIKey: "sk-****et", Model: "m2"})

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cfg.APIKey != "sk-secret" {
		t.Errorf("脱敏回传不应覆盖真实密钥，得到 %q", c.cfg.APIKey)
	}
	if c.cfg.BaseURL != "https://y" {
		t.Errorf("BaseURL 应更新，得到 %q", c.cfg.BaseURL)
	}
	if c.cfg.Model != "m2" {
		t.Errorf("Model 应更新，得到 %q", c.cfg.Model)
	}
}

func TestConfigMasksKeyInOutput(t *testing.T) {
	c := NewClient(Config{Enabled: true, BaseURL: "https://x", APIKey: "sk-1234567890"})
	out := c.Config()
	key, _ := out["api_key"].(string)
	if strings.Contains(key, "2345678") {
		t.Errorf("配置输出不应泄露密钥，得到 %q", key)
	}
	if available, _ := out["available"].(bool); !available {
		t.Error("应报告可用")
	}
}

// ── 静默降级 ──

func TestChatSilentlyDegradesWhenNotConfigured(t *testing.T) {
	c := NewClient(DefaultConfig()) // 未启用

	raw, u, err := c.Chat(context.Background(), "test", "sys", "user")
	if err != nil {
		t.Errorf("未配置时不应报错（静默降级），得到 %v", err)
	}
	if raw != "" {
		t.Errorf("未配置时应返回空串，得到 %q", raw)
	}
	if u.TotalTokens != 0 {
		t.Errorf("不应记录 token，得到 %+v", u)
	}
	// 未配置不应计入调用次数
	if s := c.Usage(10); s.TotalCalls != 0 {
		t.Errorf("未配置时不应计入调用次数，得到 %d", s.TotalCalls)
	}
}

func TestParseNameSilentlyDegrades(t *testing.T) {
	c := NewClient(DefaultConfig())
	got := c.ParseName(context.Background(), "【无损】周杰伦 - 晴天.flac")

	if got.Artist != "" || got.Title != "" {
		t.Errorf("未配置时应返回空结果，得到 %+v", got)
	}
}

func TestTestRequiresConfig(t *testing.T) {
	c := NewClient(DefaultConfig())
	if _, err := c.Test(context.Background()); err == nil {
		t.Error("未配置时连通性测试应报错（这是用户主动操作，需要明确反馈）")
	}
}

// ── 真实调用（本地测试服务器） ──

func chatServer(t *testing.T, content string, usage map[string]int, status int) *httptest.Server {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": content}},
			},
		}
		if usage != nil {
			resp["usage"] = usage
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestChatSuccessAndUsage(t *testing.T) {
	srv := chatServer(t, "正常", map[string]int{
		"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
	}, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
	raw, u, err := c.Chat(context.Background(), "test", "sys", "user")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if raw != "正常" {
		t.Errorf("返回内容 = %q", raw)
	}
	if u.TotalTokens != 15 || u.PromptTokens != 10 {
		t.Errorf("用量解析不正确: %+v", u)
	}
	if u.Missing {
		t.Error("接口返回了 usage，不应标记 missing")
	}

	s := c.Usage(10)
	if s.TotalCalls != 1 || s.TotalTokens != 15 {
		t.Errorf("用量汇总不正确: %+v", s)
	}
	if s.ByKind["test"].Calls != 1 {
		t.Errorf("应按用途归类: %+v", s.ByKind)
	}
}

// TestUsageRecordedWhenProviderOmitsUsage 不能把「没回明细」当成「没花钱」
func TestUsageRecordedWhenProviderOmitsUsage(t *testing.T) {
	srv := chatServer(t, "hello", nil, 0) // 不返回 usage
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, u, err := c.Chat(context.Background(), "test", "sys", "user")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if !u.Missing {
		t.Error("接口未返回 usage 时应标记 missing")
	}

	s := c.Usage(10)
	if s.TotalCalls != 1 {
		t.Errorf("仍应计入调用次数，得到 %d", s.TotalCalls)
	}
	if s.MissingUsage != 1 {
		t.Errorf("应记录 missing 次数，得到 %d", s.MissingUsage)
	}
}

func TestChatHTTPErrorRecordsFailure(t *testing.T) {
	srv := chatServer(t, "", nil, http.StatusInternalServerError)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k"})
	if _, _, err := c.Chat(context.Background(), "test", "sys", "user"); err == nil {
		t.Error("HTTP 500 应报错")
	}
	if s := c.Usage(10); s.TotalCalls != 1 || s.MissingUsage != 1 {
		t.Errorf("失败的调用也应记一笔（避免漏记）: %+v", s)
	}
}

func TestEndpointForms(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com":                     "https://api.example.com/v1/chat/completions",
		"https://api.example.com/":                    "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1":                  "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v3":                  "https://api.example.com/v3/chat/completions",
		"https://api.example.com/v1/chat/completions": "https://api.example.com/v1/chat/completions",
	}
	for base, want := range cases {
		c := NewClient(Config{BaseURL: base})
		if got := c.endpoint(); got != want {
			t.Errorf("endpoint(%q) = %q, 期望 %q", base, got, want)
		}
	}
}

// ── JSON 防御性解析 ──

func TestParseJSONObjectThreeLayers(t *testing.T) {
	type out struct {
		Artist string `json:"artist"`
		Title  string `json:"title"`
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"纯 JSON", `{"artist":"周杰伦","title":"晴天"}`, "周杰伦"},
		{"代码块包裹", "```json\n{\"artist\":\"周杰伦\",\"title\":\"晴天\"}\n```", "周杰伦"},
		{"无语言标记的代码块", "```\n{\"artist\":\"周杰伦\",\"title\":\"晴天\"}\n```", "周杰伦"},
		{"前后有解释文字", `好的，解析结果如下：{"artist":"周杰伦","title":"晴天"} 希望有帮助`, "周杰伦"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o out
			if err := ParseJSONObject(tc.raw, &o); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if o.Artist != tc.want {
				t.Errorf("artist = %q, 期望 %q", o.Artist, tc.want)
			}
		})
	}
}

func TestParseJSONObjectFailures(t *testing.T) {
	var o map[string]interface{}
	if err := ParseJSONObject("", &o); err == nil {
		t.Error("空输入应报错")
	}
	if err := ParseJSONObject("完全没有 JSON 的一段话", &o); err == nil {
		t.Error("无 JSON 内容应报错")
	}
}

func TestParseNameWithLocalServer(t *testing.T) {
	srv := chatServer(t, `{"artist":"周杰伦","title":"晴天","album":"叶惠美","style":"流行"}`, nil, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k"})
	got := c.ParseName(context.Background(), "【无损】周杰伦 - 晴天.flac")

	if got.Artist != "周杰伦" || got.Title != "晴天" {
		t.Errorf("解析结果不正确: %+v", got)
	}
	if got.Album != "叶惠美" {
		t.Errorf("专辑 = %q", got.Album)
	}
}

func TestParseNameHandlesBadJSON(t *testing.T) {
	srv := chatServer(t, "模型胡说八道，没有 JSON", nil, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k"})
	got := c.ParseName(context.Background(), "x.mp3")
	if got.Artist != "" || got.Title != "" {
		t.Errorf("非法 JSON 应返回空结果而不是崩溃，得到 %+v", got)
	}
}

// ── 提示词契约 ──

func TestBuildSystemPromptKeepsContract(t *testing.T) {
	got := BuildSystemPrompt("识别歌名", `只输出 {"artist":"","title":""}`)

	if !strings.Contains(got, "识别歌名") {
		t.Error("应包含任务描述")
	}
	if !strings.Contains(got, `{"artist":"","title":""}`) {
		t.Error("应包含输出契约")
	}
	if !strings.Contains(got, "不得更改格式") {
		t.Error("应明确格式不可更改")
	}
}

// TestWithUserPromptKeepsContract 用户提示词可覆盖内容但不能破坏格式
func TestWithUserPromptKeepsContract(t *testing.T) {
	contract := `只输出 {"artist":"","title":""}`
	got := WithUserPrompt("请优先识别中文歌手名", contract)

	if !strings.Contains(got, "请优先识别中文歌手名") {
		t.Error("应包含用户要求")
	}
	if !strings.Contains(got, contract) {
		t.Error("必须保留输出契约，否则下游 JSON 解析会全挂")
	}
	if !strings.Contains(got, "不得改变输出格式") {
		t.Error("应声明用户要求不得改变格式")
	}
}

func TestWithUserPromptEmptyUserReturnsContract(t *testing.T) {
	contract := "契约内容"
	if got := WithUserPrompt("", contract); got != contract {
		t.Errorf("无用户提示词时应直接返回契约，得到 %q", got)
	}
}

// ── 用量 ──

func TestUsageSummaryAndReset(t *testing.T) {
	srv := chatServer(t, "ok", map[string]int{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k"})
	ctx := context.Background()
	_, _, _ = c.Chat(ctx, "kindA", "s", "u")
	_, _, _ = c.Chat(ctx, "kindB", "s", "u")

	s := c.Usage(10)
	if s.TotalCalls != 2 || s.TotalTokens != 14 {
		t.Errorf("汇总不正确: %+v", s)
	}
	if len(s.ByKind) != 2 {
		t.Errorf("应有两个用途分类: %+v", s.ByKind)
	}
	if len(s.Recent) != 2 {
		t.Errorf("最近记录数 = %d", len(s.Recent))
	}

	c.ResetUsage()
	if s2 := c.Usage(10); s2.TotalCalls != 0 {
		t.Errorf("重置后应为 0，得到 %d", s2.TotalCalls)
	}
}

func TestDefaultConfigIsDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("默认应为禁用状态（避免无配置时误调用）")
	}
	if cfg.Available() {
		t.Error("默认配置不应可用")
	}
	if cfg.Timeout <= 0 {
		t.Error("应有默认超时")
	}
}

// ── SSE 流式响应（自建网关常见，即使请求 stream:false 也可能返回 SSE） ──

func TestParseChatResponseHandlesSSE(t *testing.T) {
	// 复刻用户实际网关的返回格式
	sse := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"正常\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n"

	parsed, err := parseChatResponse([]byte(sse))
	if err != nil {
		t.Fatalf("SSE 应能解析: %v", err)
	}
	if len(parsed.Choices) == 0 {
		t.Fatal("应解析出 choices")
	}
	if got := parsed.Choices[0].Message.Content; got != "正常" {
		t.Errorf("拼接内容 = %q, 期望「正常」", got)
	}
	if parsed.Usage.TotalTokens != 12 {
		t.Errorf("usage 未解析: %+v", parsed.Usage)
	}
}

func TestParseChatResponseSSEMultiChunk(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"呀\"}}]}\n\n" +
		"data: [DONE]\n"

	parsed, err := parseChatResponse([]byte(sse))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Choices[0].Message.Content; got != "你好呀" {
		t.Errorf("多片段应拼接，得到 %q", got)
	}
}

func TestParseChatResponseSSEWithCRLF(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\r\n\r\ndata: [DONE]\r\n"
	parsed, err := parseChatResponse([]byte(sse))
	if err != nil {
		t.Fatalf("CRLF 换行也应支持: %v", err)
	}
	if got := parsed.Choices[0].Message.Content; got != "ok" {
		t.Errorf("内容 = %q", got)
	}
}

func TestParseChatResponsePlainJSONStillWorks(t *testing.T) {
	plain := `{"choices":[{"message":{"content":"普通响应"}}],"usage":{"total_tokens":5}}`
	parsed, err := parseChatResponse([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Choices[0].Message.Content; got != "普通响应" {
		t.Errorf("内容 = %q", got)
	}
	if parsed.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", parsed.Usage)
	}
}

func TestParseChatResponseSSEError(t *testing.T) {
	sse := "data: {\"error\":\"API key required for remote API access\"}\n\n"
	if _, err := parseChatResponse([]byte(sse)); err == nil {
		t.Error("SSE 中的错误应被识别")
	}
}

// TestExtractErrorMessage 兼容字符串与对象两种 error 形态
func TestExtractErrorMessage(t *testing.T) {
	cases := map[string]string{
		`"API key required"`:         "API key required",
		`{"message":"rate limited"}`: "rate limited",
		`{"type":"invalid_request"}`: "invalid_request",
		``:                           "",
		`null`:                       "",
	}
	for raw, want := range cases {
		got := extractErrorMessage(json.RawMessage(raw))
		if got != want {
			t.Errorf("extractErrorMessage(%s) = %q, 期望 %q", raw, got, want)
		}
	}
}

// ── JudgeName：候选消歧 + 防幻觉 ──
//
// 这组用例的重点**不是「模型答得对不对」，而是「模型答错时我们挡不挡得住」**。
// 参考实现 music-tidy v1.14.0 的经验：光靠 prompt 说「不许编」是不够的，
// 返回前必须自己校验「结果是否出自输入」。

func TestJudgeNameAcceptsResultFromFilename(t *testing.T) {
	srv := chatServer(t, `{"artist":"周杰伦","title":"晴天"}`, nil, 0)
	defer srv.Close()

	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
	got, ok := c.JudgeName(context.Background(), "好听的歌曲推荐-下山 - 麦小兜.mp3", []NameCandidate{
		{Artist: "好听的歌曲推荐-下山", Title: "麦小兜"},
		{Artist: "麦小兜", Title: "好听的歌曲推荐-下山"},
	})
	// 这个用例里模型返回的内容不在文件名里 → 必须被挡下
	if ok {
		t.Errorf("模型返回的内容不在文件名里，应被防幻觉校验挡下，却通过了：%+v", got)
	}
}

func TestJudgeNameNormalCases(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		reply    string
		wantOK   bool
		wantA    string
		wantT    string
	}{
		{
			name: "正常命中", filename: "麦小兜 - 下山.mp3",
			reply:  `{"artist":"麦小兜","title":"下山"}`,
			wantOK: true, wantA: "麦小兜", wantT: "下山",
		},
		{
			// 文件名带空格，模型去掉了空格 —— 归一化后应算命中
			name: "归一化后命中", filename: "Jay Chou - 晴天.mp3",
			reply:  `{"artist":"JayChou","title":"晴天"}`,
			wantOK: true, wantA: "JayChou", wantT: "晴天",
		},
		{
			// 模型说歌手是「五月天」，但文件名里根本没有 —— 典型的幻觉
			name: "幻觉：歌手不在文件名里", filename: "周杰伦 - 晴天.mp3",
			reply:  `{"artist":"五月天","title":"晴天"}`,
			wantOK: false,
		},
		{
			// 歌名被换成另一首歌 —— 同样要挡
			name: "幻觉：歌名不在文件名里", filename: "周杰伦 - 晴天.mp3",
			reply:  `{"artist":"周杰伦","title":"稻香"}`,
			wantOK: false,
		},
		{
			name: "判不出来", filename: "周杰伦 - 晴天.mp3",
			reply:  `{"artist":"","title":""}`,
			wantOK: false,
		},
		{
			name: "返回的不是 JSON", filename: "周杰伦 - 晴天.mp3",
			reply:  `我觉得是周杰伦的晴天`,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := chatServer(t, tc.reply, nil, 0)
			defer srv.Close()

			c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
			got, ok := c.JudgeName(context.Background(), tc.filename, nil)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v（返回 %+v）", ok, tc.wantOK, got)
			}
			if tc.wantOK && (got.Artist != tc.wantA || got.Title != tc.wantT) {
				t.Errorf("结果 = (%q, %q)，期望 (%q, %q)", got.Artist, got.Title, tc.wantA, tc.wantT)
			}
		})
	}
}

func TestJudgeNameSkipsWhenUnavailable(t *testing.T) {
	// 未配置时不该发请求，直接返回 false（静默降级）
	c := NewClient(Config{Enabled: false})
	if _, ok := c.JudgeName(context.Background(), "周杰伦 - 晴天.mp3", nil); ok {
		t.Error("未配置 AI 时应返回 false")
	}
	if _, ok := c.JudgeName(context.Background(), "", nil); ok {
		t.Error("文件名为空时应返回 false")
	}
}

func TestNameAppearsIn(t *testing.T) {
	cases := []struct {
		filename, s string
		want        bool
	}{
		{"周杰伦 - 晴天.mp3", "周杰伦", true},
		{"周杰伦 - 晴天.mp3", "晴天", true},
		{"Jay Chou - 晴天.mp3", "JayChou", true}, // 忽略空格
		{"【无损】周杰伦 - 晴天.flac", "周杰伦", true},
		{"周杰伦 - 晴天.mp3", "五月天", false},
		{"周杰伦 - 晴天.mp3", "稻香", false},
		{"周杰伦 - 晴天.mp3", "", true}, // 空串表示「判不出来」，放行
	}
	for _, c := range cases {
		if got := nameAppearsIn(c.filename, c.s); got != c.want {
			t.Errorf("nameAppearsIn(%q, %q) = %v，期望 %v", c.filename, c.s, got, c.want)
		}
	}
}

// ── PickSongMatch：搜索结果消歧 ──
//
// 这组用例的重点同样是**「模型答错时挡不挡得住」**：
// 挑错歌会把别的歌的歌词封面写进用户文件，比「没配上」严重得多。

func TestPickSongMatchCases(t *testing.T) {
	want := SongQuery{Title: "晴天", Artist: "周杰伦", Album: "叶惠美"}
	cands := []SongCandidate{
		{Name: "晴天", Singer: "RyaVocal", Source: "wy"},          // 0 翻唱
		{Name: "晴天", Singer: "周杰伦", Album: "叶惠美", Source: "tx"}, // 1 正确
		{Name: "晴天 (Live)", Singer: "周杰伦", Source: "kg"},        // 2
	}

	cases := []struct {
		name   string
		reply  string
		wantOK bool
		wantI  int
	}{
		{"挑中正确的那条", `{"pick":1}`, true, 1},
		{"拿不准返回 null", `{"pick":null}`, false, 0},
		{"越界（候选只有 3 条，回了 9）", `{"pick":9}`, false, 0},
		{"负数", `{"pick":-1}`, false, 0},
		{"返回的不是 JSON", `我觉得是周杰伦那条`, false, 0},
		{"JSON 里没有 pick", `{"reason":"看起来像"}`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := chatServer(t, tc.reply, nil, 0)
			defer srv.Close()

			c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
			idx, ok := c.PickSongMatch(context.Background(), want, cands)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v（返回 %d）", ok, tc.wantOK, idx)
			}
			if tc.wantOK && idx != tc.wantI {
				t.Errorf("序号 = %d，期望 %d", idx, tc.wantI)
			}
		})
	}
}

func TestPickSongMatchSkipsWhenUnavailable(t *testing.T) {
	c := NewClient(Config{Enabled: false})
	cands := []SongCandidate{{Name: "晴天", Singer: "周杰伦"}}
	if _, ok := c.PickSongMatch(context.Background(), SongQuery{Title: "晴天"}, cands); ok {
		t.Error("未配置 AI 时应返回 false")
	}
	// 没有候选 / 没有目标歌名 → 不该发请求
	ok2 := NewClient(Config{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if _, ok := ok2.PickSongMatch(context.Background(), SongQuery{Title: "晴天"}, nil); ok {
		t.Error("没有候选时应返回 false")
	}
	if _, ok := ok2.PickSongMatch(context.Background(), SongQuery{}, cands); ok {
		t.Error("目标歌名为空时应返回 false")
	}
}

// ── VerifyLyric：歌词核验 ──
//
// 这组用例最关键的一条是**「模型没给判定时不能当成不匹配」**——
// 那会把好歌词全丢掉，比「核验不通过」严重得多。

func TestVerifyLyricCases(t *testing.T) {
	const lyric = "[00:00.00]晴天\n[00:12.00]故事的小黄花"

	cases := []struct {
		name       string
		reply      string
		wantOK     bool
		wantMatch  bool
		wantActual string
	}{
		{
			name: "判定匹配", reply: `{"match":true,"confidence":0.9,"reason":"副歌一致"}`,
			wantOK: true, wantMatch: true,
		},
		{
			name:   "判定不匹配并给出实际出处",
			reply:  `{"match":false,"confidence":0.8,"reason":"这是另一首","actual":"五月天 - 温柔"}`,
			wantOK: true, wantMatch: false, wantActual: "五月天 - 温柔",
		},
		{
			// ⚠️ 关键用例：模型只回了个空对象。用 bool 接会取零值 false，
			// 被当成「不匹配」→ 好歌词被丢掉。必须判为「核验没做成」。
			name: "没给 match 字段", reply: `{}`,
			wantOK: false,
		},
		{
			name: "JSON 里没有 match 但有别的字段", reply: `{"reason":"看起来像"}`,
			wantOK: false,
		},
		{
			name: "返回的不是 JSON", reply: `这歌词是对的`,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := chatServer(t, tc.reply, nil, 0)
			defer srv.Close()

			c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
			got, ok := c.VerifyLyric(context.Background(), "周杰伦", "晴天", lyric)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v（返回 %+v）", ok, tc.wantOK, got)
			}
			if tc.wantOK && got.Match != tc.wantMatch {
				t.Errorf("Match = %v，期望 %v", got.Match, tc.wantMatch)
			}
			if tc.wantActual != "" && got.Actual != tc.wantActual {
				t.Errorf("Actual = %q，期望 %q", got.Actual, tc.wantActual)
			}
		})
	}
}

// 置信度越界要被夹回来
func TestVerifyLyricClampsConfidence(t *testing.T) {
	srv := chatServer(t, `{"match":true,"confidence":1.7,"reason":"x"}`, nil, 0)
	defer srv.Close()
	c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})
	got, ok := c.VerifyLyric(context.Background(), "周杰伦", "晴天", "[00:01.00]词")
	if !ok || got.Confidence != 1 {
		t.Errorf("置信度应夹到 1，实际 ok=%v conf=%v", ok, got.Confidence)
	}
}

func TestVerifyLyricSkipsWhenNothingToVerify(t *testing.T) {
	c := NewClient(Config{Enabled: false})
	if _, ok := c.VerifyLyric(context.Background(), "周杰伦", "晴天", "[00:01.00]词"); ok {
		t.Error("未配置 AI 时应返回 false")
	}
	c2 := NewClient(Config{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if _, ok := c2.VerifyLyric(context.Background(), "周杰伦", "", "[00:01.00]词"); ok {
		t.Error("歌名为空时应返回 false")
	}
	if _, ok := c2.VerifyLyric(context.Background(), "周杰伦", "晴天", "   "); ok {
		t.Error("歌词为空时应返回 false")
	}
	if _, ok := c2.VerifyLyric(context.Background(), "周杰伦", "晴天", "[00:01.00]"); ok {
		t.Error("只有时间码、没有正文时应返回 false（去掉时间码后是空的）")
	}
}

// ── 推理模型（DeepSeek-R1 / QwQ 等）：content 为空不能静默失败 ──
//
// 背景：推理模型把思维链放在 `reasoning_content`，**content 可能是空的**。
// 不接这个字段的话，配了推理模型会「静默无结果」——
// 上层只看到空串，完全不知道原因。（这个坑是看参考实现 leelaa.playlist 学到的）

// rawChatServer 返回一个**完整的**响应体，用来构造推理模型的各种形态
func rawChatServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestChatReasoningModelFallback(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantText  string
		wantErrIn string // 非空表示期望报错，且错误信息含该关键词
	}{
		{
			name:     "content 有正文（正常模型）",
			body:     `{"choices":[{"message":{"content":"正常"}}]}`,
			wantText: "正常",
		},
		{
			name:     "content 空但 reasoning_content 有内容 → 回退",
			body:     `{"choices":[{"message":{"content":"","reasoning_content":"{\"pick\":1}"}}]}`,
			wantText: `{"pick":1}`,
		},
		{
			name:     "用 reasoning 这个字段名 → 也回退",
			body:     `{"choices":[{"message":{"content":"","reasoning":"思维链"}}]}`,
			wantText: "思维链",
		},
		{
			// 推理吃光 token：必须给出**可行动**的错误，而不是空串
			name:      "两者都空 + finish_reason=length",
			body:      `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`,
			wantErrIn: "token 上限截断",
		},
		{
			name:      "两者都空 + 没有 finish_reason",
			body:      `{"choices":[{"message":{"content":""}}]}`,
			wantErrIn: "reasoning_content",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := rawChatServer(t, tc.body)
			defer srv.Close()
			c := NewClient(Config{Enabled: true, BaseURL: srv.URL, APIKey: "k", Model: "m"})

			got, _, err := c.Chat(context.Background(), "test", "s", "u")
			if tc.wantErrIn != "" {
				if err == nil {
					t.Fatalf("应报错（含 %q），却返回了 %q", tc.wantErrIn, got)
				}
				if !strings.Contains(err.Error(), tc.wantErrIn) {
					t.Errorf("错误信息 = %q，应包含 %q", err.Error(), tc.wantErrIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got != tc.wantText {
				t.Errorf("返回 = %q，期望 %q", got, tc.wantText)
			}
		})
	}
}

// 流式路径同样要能兜住推理模型
func TestParseSSEReasoningFallback(t *testing.T) {
	// 全程只有 reasoning_content，content 一直是空的
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"{\"pick\":"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"2}"}}]}`,
		`data: [DONE]`,
	}, "\n")

	out, err := parseSSE([]byte(sse))
	if err != nil {
		t.Fatalf("应回退到 reasoning_content，却报错: %v", err)
	}
	if got := out.Choices[0].Message.Content; got != `{"pick":2}` {
		t.Errorf("回退内容 = %q", got)
	}
}

func TestParseSSEEmptyGivesActionableError(t *testing.T) {
	sse := `data: {"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n"
	_, err := parseSSE([]byte(sse))
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "token 上限截断") {
		t.Errorf("错误信息应可行动，实际 %q", err.Error())
	}
}
