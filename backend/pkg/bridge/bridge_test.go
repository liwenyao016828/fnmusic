package bridge

import (
	"sync"
	"testing"
	"time"
)

func TestCreateRequiresPlatformAndIdentifier(t *testing.T) {
	m := NewManager()

	if _, err := m.Create(ResolveRequest{Name: "夜曲"}); err == nil {
		t.Error("缺少 platform 应报错")
	}
	if _, err := m.Create(ResolveRequest{Platform: "wy"}); err == nil {
		t.Error("缺少 songmid/id/hash 应报错")
	}

	job, err := m.Create(ResolveRequest{Platform: "WY", Songmid: "186016", Name: "夜曲"})
	if err != nil {
		t.Fatalf("合法请求应创建成功: %v", err)
	}
	if job.Request.Platform != "wy" {
		t.Errorf("platform 应被规范为小写, 得到 %q", job.Request.Platform)
	}
	if job.Request.Quality != "highest" {
		t.Errorf("未指定音质应默认 highest, 得到 %q", job.Request.Quality)
	}
	if job.Status != StatusPending {
		t.Errorf("初始状态应为 pending, 得到 %q", job.Status)
	}
}

func TestClaimIsAtomic(t *testing.T) {
	m := NewManager()
	job, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "1"})

	if _, err := m.Claim(job.ID); err != nil {
		t.Fatalf("首次领取应成功: %v", err)
	}
	if _, err := m.Claim(job.ID); err == nil {
		t.Error("重复领取应失败，避免多标签页重复解析")
	}
}

func TestClaimConcurrentOnlyOneWins(t *testing.T) {
	m := NewManager()
	job, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "1"})

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Claim(job.ID); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("并发领取应恰好成功 1 次, 实际 %d 次", wins)
	}
}

func TestCompleteStoresResult(t *testing.T) {
	m := NewManager()
	job, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "1"})
	_, _ = m.Claim(job.ID)

	done, err := m.Complete(job.ID, ResultInput{
		URL:        "https://cdn.example/a.flac",
		Quality:    "flac",
		SourceID:   "custom_1",
		SourceName: "示例音源",
		Referer:    "https://music.163.com/",
	})
	if err != nil {
		t.Fatalf("回传结果应成功: %v", err)
	}
	if done.Status != StatusDone {
		t.Errorf("状态应为 done, 得到 %q", done.Status)
	}
	if done.URL != "https://cdn.example/a.flac" {
		t.Errorf("URL = %q", done.URL)
	}

	// 幂等：重复回传不应覆盖
	again, err := m.Complete(job.ID, ResultInput{URL: "https://other/x.mp3"})
	if err != nil {
		t.Fatalf("重复回传应幂等返回: %v", err)
	}
	if again.URL != "https://cdn.example/a.flac" {
		t.Errorf("重复回传不应覆盖已有结果, 得到 %q", again.URL)
	}
}

func TestCompleteFailurePath(t *testing.T) {
	m := NewManager()
	job, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "1"})

	failed, err := m.Complete(job.ID, ResultInput{Error: "音源全部熔断"})
	if err != nil {
		t.Fatalf("回传失败结果应成功: %v", err)
	}
	if failed.Status != StatusFailed {
		t.Errorf("状态应为 failed, 得到 %q", failed.Status)
	}
	if failed.Error != "音源全部熔断" {
		t.Errorf("Error = %q", failed.Error)
	}

	// 空 URL 且无错误信息也应判失败
	m2 := NewManager()
	j2, _ := m2.Create(ResolveRequest{Platform: "wy", Songmid: "2"})
	f2, _ := m2.Complete(j2.ID, ResultInput{})
	if f2.Status != StatusFailed {
		t.Errorf("空结果应判失败, 得到 %q", f2.Status)
	}
}

func TestPendingOnlyReturnsUnclaimed(t *testing.T) {
	m := NewManager()
	a, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "a"})
	_, _ = m.Create(ResolveRequest{Platform: "wy", Songmid: "b"})
	_, _ = m.Claim(a.ID)

	pending := m.Pending()
	if len(pending) != 1 {
		t.Fatalf("待处理任务数 = %d, 期望 1", len(pending))
	}
	if pending[0].Request.Songmid != "b" {
		t.Errorf("待处理任务应为未被领取的那个, 得到 %q", pending[0].Request.Songmid)
	}
}

func TestExpiry(t *testing.T) {
	m := NewManager()
	job, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "1"})

	// 手动把过期时间提前
	m.mu.Lock()
	m.jobs[job.ID].ExpiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()

	got, ok := m.Get(job.ID)
	if !ok {
		t.Fatal("任务应仍可查询")
	}
	if got.Status != StatusExpired {
		t.Errorf("超时任务状态应为 expired, 得到 %q", got.Status)
	}
	if got.Error == "" {
		t.Error("超时任务应带有说明性错误信息")
	}

	if _, err := m.Claim(job.ID); err == nil {
		t.Error("过期任务不应还能被领取")
	}
	if len(m.Pending()) != 0 {
		t.Error("过期任务不应出现在待处理列表中")
	}
}

func TestStats(t *testing.T) {
	m := NewManager()
	a, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "a"})
	b, _ := m.Create(ResolveRequest{Platform: "wy", Songmid: "b"})
	_, _ = m.Claim(a.ID)
	_, _ = m.Complete(b.ID, ResultInput{URL: "https://x/y.mp3"})

	stats := m.Stats()
	if stats[StatusClaimed] != 1 {
		t.Errorf("claimed = %d, 期望 1", stats[StatusClaimed])
	}
	if stats[StatusDone] != 1 {
		t.Errorf("done = %d, 期望 1", stats[StatusDone])
	}
	if stats[StatusPending] != 0 {
		t.Errorf("pending = %d, 期望 0", stats[StatusPending])
	}
}

func TestUnknownJobReturnsError(t *testing.T) {
	m := NewManager()
	if _, ok := m.Get("nope"); ok {
		t.Error("不存在的任务应返回 false")
	}
	if _, err := m.Claim("nope"); err == nil {
		t.Error("领取不存在的任务应报错")
	}
	if _, err := m.Complete("nope", ResultInput{URL: "https://x/y.mp3"}); err == nil {
		t.Error("回传不存在的任务应报错")
	}
}
