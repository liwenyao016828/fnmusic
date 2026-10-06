package monitor

import (
	"errors"
	"strings"
	"testing"
)

// fakeDisp 只实现 Dispatcher（没有取链能力），用来验证旧行为不被破坏。
type fakeDisp struct {
	songs    []Song
	dlPath   string
	dlErr    error
	dlCalled []Song
}

func (f *fakeDisp) Discover(*Monitor) ([]Song, []string, error) {
	return f.songs, nil, nil
}

func (f *fakeDisp) Download(s Song) (string, error) {
	f.dlCalled = append(f.dlCalled, s)
	if f.dlErr != nil {
		return "", f.dlErr
	}
	return f.dlPath, nil
}

// fakeResolver 在 fakeDisp 基础上额外实现 URLResolver（模拟已配置服务型音源）。
//
// unavailable 用反向字段（零值 = 可用），避免「零值语义」把默认情况变成不可用。
type fakeResolver struct {
	*fakeDisp
	resolveURL  string
	resolveErr  error
	calls       int
	unavailable bool
}

func (r *fakeResolver) CanResolve() bool { return !r.unavailable }

func (r *fakeResolver) ResolveURL(*Monitor, Song) (string, error) {
	r.calls++
	if r.resolveErr != nil {
		return "", r.resolveErr
	}
	return r.resolveURL, nil
}

func newRunFixture(t *testing.T, disp Dispatcher, songs []Song) (*Handler, *Monitor) {
	t.Helper()
	store := NewStore(t.TempDir())
	mon := &Monitor{
		ID: "m1", Name: "测试歌单", Kind: KindPlaylist,
		AutoDownload: true, BaselineDone: true, Quality: QualityLossless,
		MaxDownloads: 30, IntervalMinutes: 360,
		// ⚠️ 必须显式 Enabled：RunOnce 每一轮都会复查「订阅是否已停止」，
		// 零值 false 会被当成「已停止」而立刻中止（生产里订阅创建时前端传 enabled:true）。
		Enabled: true,
	}
	// RunOnce 的收尾会调 UpdateMonitor / SetNextRun，先把监控登记进去更接近真实路径
	if _, err := store.CreateMonitor(mon); err != nil {
		t.Fatalf("创建监控失败：%v", err)
	}
	// ⚠️ CreateMonitor 会把 BaselineDone 重置为 false（新建即首轮），测试里显式还原
	mon.BaselineDone = true
	if f, ok := disp.(*fakeDisp); ok {
		f.songs = songs
	}
	if r, ok := disp.(*fakeResolver); ok {
		r.songs = songs
	}
	return NewHandler(store, disp), mon
}

// 后端有取链能力时，无直链曲目应「取链 → 落盘」，状态 downloaded。
func TestRunOnceResolvesThenDownloads(t *testing.T) {
	res := &fakeResolver{
		fakeDisp:   &fakeDisp{dlPath: "/music/a.flac"},
		resolveURL: "https://cdn.example/a.flac",
	}
	h, mon := newRunFixture(t, res, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})

	run := h.RunOnce(mon)

	if res.calls != 1 {
		t.Fatalf("应调用一次取链，实际 %d 次", res.calls)
	}
	if len(res.dlCalled) != 1 || res.dlCalled[0].URL != "https://cdn.example/a.flac" {
		t.Fatalf("下载应带上取链结果，实际 %+v", res.dlCalled)
	}
	if run.Downloaded != 1 || run.Failed != 0 || run.ResolveFailed != 0 {
		t.Fatalf("期望 downloaded=1，实际 %+v", run)
	}
	tr, ok := h.store.GetTrack("m1", "wy", "1")
	if !ok || tr.Status != StatusDownloaded || tr.FilePath != "/music/a.flac" {
		t.Fatalf("曲目应落盘为 downloaded，实际 %+v", tr)
	}
}

// ⚠️ 取链失败：登记 pending、计入 ResolveFailed，但**不能**把整轮标成 failed/error
// —— 音源偶发不可用是常态，下一轮监控会自动重试。
func TestRunOnceResolveFailureStaysPendingNotFailed(t *testing.T) {
	res := &fakeResolver{
		fakeDisp:   &fakeDisp{dlPath: "/music/a.flac"},
		resolveErr: errors.New("音源服务返回 502"),
	}
	h, mon := newRunFixture(t, res, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})

	run := h.RunOnce(mon)

	if run.Failed != 0 {
		t.Fatalf("取链失败不应计入 Failed，实际 %+v", run)
	}
	if run.ResolveFailed != 1 {
		t.Fatalf("应计入 ResolveFailed，实际 %+v", run)
	}
	if run.Status != "ok" {
		t.Fatalf("整轮状态不应被取链失败拖红，实际 %q", run.Status)
	}
	if len(res.dlCalled) != 0 {
		t.Fatal("取链失败不应发起下载")
	}
	tr, ok := h.store.GetTrack("m1", "wy", "1")
	if !ok || tr.Status != StatusPending {
		t.Fatalf("应登记为 pending，实际 %+v", tr)
	}
	if !strings.Contains(tr.Error, "音源服务返回 502") || !strings.Contains(tr.Error, "补下载") {
		t.Fatalf("错误信息应含原因且指向补下载兜底，实际 %q", tr.Error)
	}
}

// 取链成功但服务端返回空地址 → 同样按失败处理，不能拿空 URL 去下载。
func TestRunOnceResolveEmptyURLTreatedAsFailure(t *testing.T) {
	res := &fakeResolver{fakeDisp: &fakeDisp{dlPath: "/music/a.flac"}}
	h, mon := newRunFixture(t, res, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})

	run := h.RunOnce(mon)

	if run.ResolveFailed != 1 || len(res.dlCalled) != 0 {
		t.Fatalf("空地址应按取链失败处理，实际 %+v", run)
	}
}

// 没有取链能力（未配置服务型音源）→ 维持 v2.1.10 行为：登记 pending 等网页端补下载。
func TestRunOnceWithoutResolverKeepsPending(t *testing.T) {
	disp := &fakeDisp{dlPath: "/music/a.flac"}
	h, mon := newRunFixture(t, disp, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})

	run := h.RunOnce(mon)

	if run.ResolveFailed != 0 || len(disp.dlCalled) != 0 {
		t.Fatalf("无取链能力时不应取链也不应下载，实际 %+v", run)
	}
	tr, ok := h.store.GetTrack("m1", "wy", "1")
	if !ok || tr.Status != StatusPending {
		t.Fatalf("应登记 pending，实际 %+v", tr)
	}
}

// 实现了取链接口但当前没有可用音源（CanResolve=false）→ 维持旧行为，
// 不能因为「接口存在」就把所有曲目变成取链失败。
func TestRunOnceFallsBackWhenResolverUnavailable(t *testing.T) {
	res := &fakeResolver{
		fakeDisp:    &fakeDisp{dlPath: "/music/a.flac"},
		resolveURL:  "https://cdn.example/a.flac",
		unavailable: true,
	}
	h, mon := newRunFixture(t, res, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})

	run := h.RunOnce(mon)

	if res.calls != 0 {
		t.Fatalf("没有可用音源时不应取链，实际 %d 次", res.calls)
	}
	if run.ResolveFailed != 0 || run.Failed != 0 {
		t.Fatalf("不应记为失败，实际 %+v", run)
	}
	tr, ok := h.store.GetTrack("m1", "wy", "1")
	if !ok || tr.Status != StatusPending || !strings.Contains(tr.Error, "补下载") {
		t.Fatalf("应登记为待网页端补下载，实际 %+v", tr)
	}
}

// 基线轮即使有取链能力也不得调用音源服务（防打爆上游）。
func TestRunOnceBaselineNeverResolves(t *testing.T) {
	res := &fakeResolver{fakeDisp: &fakeDisp{dlPath: "/music/a.flac"}, resolveURL: "https://cdn.example/a.flac"}
	h, mon := newRunFixture(t, res, []Song{{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦"}})
	mon.BaselineDone = false

	run := h.RunOnce(mon)

	if res.calls != 0 {
		t.Fatalf("基线轮不得取链，实际调用 %d 次", res.calls)
	}
	if !run.Baseline {
		t.Fatal("应标记为基线轮")
	}
	if tr, _ := h.store.GetTrack("m1", "wy", "1"); tr == nil || tr.Status != StatusPending {
		t.Fatalf("基线轮应只登记 pending，实际 %+v", tr)
	}
}
