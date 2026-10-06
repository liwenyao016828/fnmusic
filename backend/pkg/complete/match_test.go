package complete

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/search"
)

// mockAIServer 假 OpenAI 兼容服务：把**用户消息**（不含 system）交给 pick 决定回什么。
// 只取 user 消息是因为 system prompt 里也含「编号」等字样，混在一起会干扰断言。
func mockAIServer(t *testing.T, pick func(userMsg string) any) *httptest.Server {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := ""
		for _, m := range req.Messages {
			if m.Role != "system" {
				user += m.Content
			}
		}
		body, _ := json.Marshal(map[string]any{"pick": pick(user)})
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": string(body)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newTestAIClient(t *testing.T, url string) *ai.Client {
	t.Helper()
	return ai.NewClient(ai.Config{Enabled: true, BaseURL: url, APIKey: "k", Model: "m"})
}

// pickByAI：候选池 → AI 挑 → 返回对应的 UnifiedSong
func TestPickByAIMapsIndexBackToSong(t *testing.T) {
	// 假模型：候选里第 2 条（编号1）是「周杰伦」，就选它
	srv := mockAIServer(t, func(user string) any {
		if containsStr(user, "编号1：晴天 (Live) | 周杰伦") {
			return 1
		}
		return nil
	})
	defer srv.Close()

	pool := []search.UnifiedSong{
		{Name: "晴天", Singer: "RyaVocal", Source: "wy", Songmid: "wrong"},
		{Name: "晴天 (Live)", Singer: "周杰伦", Source: "tx", Songmid: "right"},
	}

	got, ok := pickByAI(context.Background(), newTestAIClient(t, srv.URL), "晴天 Live", "周杰伦", "", pool)
	if !ok {
		t.Fatal("应该挑中一条")
	}
	if got.Songmid != "right" {
		t.Errorf("挑错了：拿到 %q，期望 right（%+v）", got.Songmid, got)
	}
}

// 模型说「拿不准」→ 返回 false，调用方沿用「没匹配上」
func TestPickByAIRespectsNull(t *testing.T) {
	srv := mockAIServer(t, func(string) any { return nil })
	defer srv.Close()

	pool := []search.UnifiedSong{{Name: "晴天", Singer: "周杰伦", Songmid: "x"}}
	if _, ok := pickByAI(context.Background(), newTestAIClient(t, srv.URL), "晴天", "周杰伦", "", pool); ok {
		t.Error("模型返回 null 时不该判定为命中")
	}
}

// 模型返回越界编号 → 判失败（宁可漏配也不能挑错）
func TestPickByAIRejectsOutOfRange(t *testing.T) {
	srv := mockAIServer(t, func(string) any { return 99 })
	defer srv.Close()

	pool := []search.UnifiedSong{{Name: "晴天", Singer: "周杰伦", Songmid: "x"}}
	if _, ok := pickByAI(context.Background(), newTestAIClient(t, srv.URL), "晴天", "周杰伦", "", pool); ok {
		t.Error("越界编号应判失败")
	}
}

// 候选上限：四个平台各 10 条不该全塞给模型
func TestPickByAITruncatesPool(t *testing.T) {
	var seen int
	srv := mockAIServer(t, func(user string) any {
		seen = countOccurrences(user, "编号")
		return nil
	})
	defer srv.Close()

	pool := make([]search.UnifiedSong, 40)
	for i := range pool {
		pool[i] = search.UnifiedSong{Name: "歌", Singer: "人", Songmid: "x"}
	}
	_, _ = pickByAI(context.Background(), newTestAIClient(t, srv.URL), "歌", "人", "", pool)

	if seen != maxMatchCandidates {
		t.Errorf("交给模型的候选数 = %d，期望截断到 %d", seen, maxMatchCandidates)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || countOccurrences(s, sub) > 0
}

func countOccurrences(s, sub string) int {
	if sub == "" {
		return 0
	}
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
