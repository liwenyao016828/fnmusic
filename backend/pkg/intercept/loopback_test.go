package intercept

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 本机回环请求在 token 模式下必须自己带上凭据。
//
// 这条链路的失败形态是「静默」的：401 → 榜单取回空 → 卡片不注入 →
// 用户只看到榜单歌单**整个不见**，日志里一个字都没有。所以这里直接把
// 「有没有带 X-API-Token」钉死成断言。
func TestLocalRequestCarriesAPIToken(t *testing.T) {
	var mu sync.Mutex
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotToken = r.Header.Get("X-API-Token")
		mu.Unlock()
		if r.Header.Get("X-API-Token") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"name":"飙升榜","songs":[{"id":"wy_1","name":"甲","source":"wy"}]}}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.it.localAPI = srv.URL
	h.it.localToken = "s3cret"

	name, _, tracks, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "1", 100)
	if err != nil {
		t.Fatalf("带凭据的回环请求不该失败：%v", err)
	}
	mu.Lock()
	token := gotToken
	mu.Unlock()
	if token != "s3cret" {
		t.Errorf("回环请求没带上 X-API-Token，服务端收到 %q", token)
	}
	if name != "飙升榜" || len(tracks) != 1 {
		t.Errorf("解析结果不对：name=%q tracks=%d", name, len(tracks))
	}
}

// 没配 token（none / lan 模式）时不能凭空捏一个头出来。
func TestLocalRequestOmitsTokenWhenUnset(t *testing.T) {
	var mu sync.Mutex
	var token string
	var headerPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		token = r.Header.Get("X-API-Token")
		_, headerPresent = r.Header["X-Api-Token"]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"name":"热歌榜","songs":[]}}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.it.localAPI = srv.URL
	h.it.localToken = ""

	if _, _, _, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "1", 100); err != nil {
		t.Fatalf("取榜单失败：%v", err)
	}
	mu.Lock()
	got, present := token, headerPresent
	mu.Unlock()
	if present || got != "" {
		t.Errorf("没配 token 时不该带 X-API-Token，实际 %q", got)
	}
}

// 401 必须变成「有话说」的错误，而不是被当成空榜单吞掉。
func TestLocalRequestReportsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.it.localAPI = srv.URL
	h.it.localToken = ""

	_, _, _, err := h.it.chartDetailViaLocalAPI(context.Background(), "wy", "1", 100)
	if err == nil {
		t.Fatal("401 时应当报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误里应当带上状态码，实际：%v", err)
	}
}

// 总开关关掉时，日志里必须留一条「为什么没有榜单」，且只留一条 ——
// 这个开关每列一次歌单就会被问一次，每次都打日志等于刷屏。
func TestChartDisabledLogsOnce(t *testing.T) {
	h := newHarness(t)

	var mu sync.Mutex
	var logs []string
	h.it.logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	h.it.chartList = func() []string { return []string{"wy_19723756"} }
	h.it.chartEnabled = func() bool { return false }

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, line := range logs {
			if strings.Contains(line, "榜单注入总开关") {
				n++
			}
		}
		return n
	}

	for i := 0; i < 3; i++ {
		if refs := h.it.enabledChartRefs(); len(refs) != 0 {
			t.Fatalf("开关关掉时不该有榜单，得到 %d 个", len(refs))
		}
	}
	if got := count(); got != 1 {
		t.Fatalf("同一状态只该记一次日志，实际记了 %d 次", got)
	}

	// 打开再关掉：这是「新的一次关闭」，要能再提醒一次。
	h.it.chartEnabled = func() bool { return true }
	if refs := h.it.enabledChartRefs(); len(refs) != 1 {
		t.Fatalf("开关打开时应有 1 个榜单，得到 %d 个", len(refs))
	}
	h.it.chartEnabled = func() bool { return false }
	h.it.enabledChartRefs()
	if got := count(); got != 2 {
		t.Errorf("重新关掉后应当再提醒一次，实际共 %d 次", got)
	}
}
