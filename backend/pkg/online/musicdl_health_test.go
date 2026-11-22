package online

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMusicDL 一个假的 sidecar：按源名决定「报错」「给结果」还是「安静地没有」。
type fakeMusicDL struct {
	mu       sync.Mutex
	requests [][]string // 每次 /search 收到的 sources
	bad      map[string]bool
	silent   map[string]bool // 搜得到这个源，但没这首歌 → 返回 0 条且不报错
}

func (f *fakeMusicDL) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keyword string   `json:"keyword"`
		Sources []string `json:"sources"`
		Limit   int      `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.requests = append(f.requests, body.Sources)
	f.mu.Unlock()

	resp := musicdlSearchResp{OK: true}
	for _, s := range body.Sources {
		if f.bad[s] {
			if resp.Errors == nil {
				resp.Errors = map[string]MusicDLSideError{}
			}
			resp.Errors[s] = MusicDLSideError{Kind: "unavailable", Message: "源暂时不可用"}
			continue
		}
		if f.silent[s] {
			continue
		}
		resp.Items = append(resp.Items, MusicDLSong{
			ID: s + "-1", Source: s, PlatformID: s + "-1",
			Title: "测试曲", Artist: "测试歌手", DownloadURL: "https://example.invalid/a.mp3",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeMusicDL) lastRequest() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// TestMusicDLBreaksFailingSource 熔断的核心：报错的源进冷却，
// 下一轮**不再发给 sidecar**（问它就是白等它超时）。
func TestMusicDLBreaksFailingSource(t *testing.T) {
	fake := &fakeMusicDL{bad: map[string]bool{"bad": true}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)

	// 第一轮：bad 报错 → 记一次失败。
	if _, err := m.Search(context.Background(), "测试", []string{"bad", "good"}, 3); err != nil {
		t.Fatalf("第一轮不该整体失败：%v", err)
	}
	if got := m.SourceCooldownRemaining("bad"); got <= 0 {
		t.Fatalf("报错的源该进冷却，剩余 %v", got)
	}

	// 第二轮：bad 不该再被发出去。
	if _, err := m.Search(context.Background(), "测试", []string{"bad", "good"}, 3); err != nil {
		t.Fatalf("第二轮不该整体失败：%v", err)
	}
	last := fake.lastRequest()
	for _, s := range last {
		if s == "bad" {
			t.Fatalf("冷却期内的源不该再被问，实际发了 %v", last)
		}
	}
	if len(last) != 1 || last[0] != "good" {
		t.Fatalf("只有健康的源该被问，实际 %v", last)
	}
}

// TestMusicDLZeroResultsIsNotFailure 「搜到 0 条」**不算失败** ——
// 那个源可能只是没有这首歌（实测 16 个源里有一半搜别的歌就 0 条）。
// 把它当失败会误伤：好源会被无谓地熔断掉。
func TestMusicDLZeroResultsIsNotFailure(t *testing.T) {
	fake := &fakeMusicDL{bad: map[string]bool{}, silent: map[string]bool{"quiet": true}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)
	if _, err := m.Search(context.Background(), "测试", []string{"quiet"}, 3); err != nil {
		t.Fatalf("0 条不该是错误：%v", err)
	}
	if got := m.SourceCooldownRemaining("quiet"); got != 0 {
		t.Fatalf("0 条不该触发熔断，剩余 %v", got)
	}
}

// TestMusicDLSuccessClearsCooldown 源恢复后（又给结果了）冷却要清掉。
func TestMusicDLSuccessClearsCooldown(t *testing.T) {
	fake := &fakeMusicDL{bad: map[string]bool{"flaky": true}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)
	_, _ = m.Search(context.Background(), "测试", []string{"flaky"}, 3)
	if got := m.SourceCooldownRemaining("flaky"); got <= 0 {
		t.Fatalf("该先进冷却，剩余 %v", got)
	}

	// 源修好了 → 手动清冷却（冷却期过后）→ 再问一次成功 → 计数归零。
	fake.mu.Lock()
	fake.bad["flaky"] = false
	fake.mu.Unlock()

	// 把冷却推到过去，模拟「时间到了」。
	m.mu.Lock()
	m.health["flaky"].cooldownEnd = time.Now().Add(-time.Second)
	m.mu.Unlock()

	if _, err := m.Search(context.Background(), "测试", []string{"flaky"}, 3); err != nil {
		t.Fatalf("恢复后不该失败：%v", err)
	}
	if got := m.SourceCooldownRemaining("flaky"); got != 0 {
		t.Fatalf("成功一次后冷却该清掉，剩余 %v", got)
	}
}

// TestMusicDLSearchBudgetApplies 单源预算：探测腿要的是「别等太久」。
func TestMusicDLSearchBudgetApplies(t *testing.T) {
	fake := &fakeMusicDL{bad: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)
	ctx := WithSourceBudget(context.Background(), 3*time.Second)
	if _, err := m.SearchWithBudget(ctx, "测试", []string{"good"}, 3, 3*time.Second); err != nil {
		t.Fatalf("带预算的搜索不该失败：%v", err)
	}
	if len(fake.lastRequest()) != 1 {
		t.Fatalf("该问一个源，实际 %v", fake.lastRequest())
	}
}

// TestSourceBudgetInContext 预算存取（探测腿与下载腿的口径分界靠它）。
func TestSourceBudgetInContext(t *testing.T) {
	if d, ok := SourceBudget(context.Background()); ok || d != 0 {
		t.Fatalf("没有预算时不该取到值，实际 (%v, %v)", d, ok)
	}
	ctx := WithSourceBudget(context.Background(), 7*time.Second)
	d, ok := SourceBudget(ctx)
	if !ok || d != 7*time.Second {
		t.Fatalf("预算该原样取出，实际 (%v, %v)", d, ok)
	}
	// 非正数视为「没设」。
	if _, ok := SourceBudget(WithSourceBudget(context.Background(), 0)); ok {
		t.Fatal("0 不该算作预算")
	}
}

// TestMusicDLAllSourcesCoolingDown 全在冷却 → 不发请求，直接空手回。
func TestMusicDLAllSourcesCoolingDown(t *testing.T) {
	fake := &fakeMusicDL{bad: map[string]bool{"a": true, "b": true}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)
	_, _ = m.Search(context.Background(), "测试", []string{"a", "b"}, 3)
	before := len(fake.requests)

	items, err := m.Search(context.Background(), "测试", []string{"a", "b"}, 3)
	if err != nil {
		t.Fatalf("全在冷却不该报错：%v", err)
	}
	if len(items) != 0 {
		t.Fatalf("全在冷却就该空手回，得到 %d 条", len(items))
	}
	if len(fake.requests) != before {
		t.Fatalf("全在冷却时不该再发请求，实际发了 %d 次", len(fake.requests)-before)
	}
}

// TestMusicDLInvalidSourceAlsoBreaks 参数错误（我们自己的问题）也要熔断，
// 否则每次都会白问一次。
func TestMusicDLInvalidSourceAlsoBreaks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"items":[],"errors":{"weird":{"kind":"invalid","message":"参数不对"}}}`))
	}))
	defer srv.Close()

	m := NewMusicDL(srv.URL, nil)
	_, _ = m.Search(context.Background(), "测试", []string{"weird"}, 3)
	if got := m.SourceCooldownRemaining("weird"); got <= 0 {
		t.Fatalf("参数错的源也该熔断，剩余 %v", got)
	}
	if !strings.Contains(m.SourceCooldownRemaining("weird").String(), "m") {
		t.Fatalf("冷却时长不该是 0")
	}
}
