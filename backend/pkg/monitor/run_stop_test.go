package monitor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 「停止」之后，手动执行入口必须拒绝 —— 定时调度那侧本来就有 Enabled 判定，
// 只有这个入口漏了，于是前端定时器 / 外部程序还能在停止后触发一轮。
// 用户原话（2026-09-22）：「我停止后，一会儿又开始推送」。
func TestHandleRunRejectsDisabledMonitor(t *testing.T) {
	store := NewStore(t.TempDir())
	mon := &Monitor{
		ID: "m1", Name: "已停止的订阅", Kind: KindPlaylist,
		AutoDownload: true, Enabled: false,
		Quality: QualityLossless, IntervalMinutes: 360, MaxDownloads: 30,
	}
	if _, err := store.CreateMonitor(mon); err != nil {
		t.Fatalf("创建监控失败：%v", err)
	}
	h := NewHandler(store, nil)

	rec := httptest.NewRecorder()
	h.HandleRun(rec, httptest.NewRequest(http.MethodPost, "/api/monitors/m1/run", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("停用的监控应被拒绝（409），实际 %d %s", rec.Code, rec.Body.String())
	}
	// 被拒绝的请求不该占用「正在运行」位，否则用户恢复更新后会被误报「正在运行中」
	if h.active["m1"] {
		t.Error("被拒绝的请求不该占用运行位")
	}
}

// 反向保护：别把守卫写成「什么都拒绝」。
func TestHandleRunAcceptsEnabledMonitor(t *testing.T) {
	store := NewStore(t.TempDir())
	mon := &Monitor{
		ID: "m1", Name: "正常订阅", Kind: KindPlaylist,
		AutoDownload: true, Enabled: true,
		Quality: QualityLossless, IntervalMinutes: 360, MaxDownloads: 30,
	}
	if _, err := store.CreateMonitor(mon); err != nil {
		t.Fatalf("创建监控失败：%v", err)
	}
	h := NewHandler(store, nil)

	rec := httptest.NewRecorder()
	h.HandleRun(rec, httptest.NewRequest(http.MethodPost, "/api/monitors/m1/run", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("启用中的监控应被接受（200），实际 %d %s", rec.Code, rec.Body.String())
	}
	waitRunIdle(t, h, "m1")
}

// waitRunIdle 等这一轮异步执行真正收尾。
//
// HandleRun 内部是 `go h.RunOnce(mon)`：测试若直接结束，goroutine 会在
// t.TempDir() 被清理之后继续往目录里写文件，表现为偶发的
// "TempDir RemoveAll cleanup: directory not empty"。这个抖动以前一直被当成
// 「测试不稳定」，其实是测试没等异步任务 —— 异步是本包的正常设计，不该改产品代码。
func waitRunIdle(t *testing.T, h *Handler, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		running := h.active[id]
		h.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("监控 %s 在 5s 内没有跑完", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stopMidDisp 在第一次落盘成功后把监控停掉，模拟「一轮跑到一半用户点了停止」。
type stopMidDisp struct {
	songs  []Song
	dlHits int
	onHit  func()
}

func (d *stopMidDisp) Discover(*Monitor) ([]Song, []string, error) { return d.songs, nil, nil }

func (d *stopMidDisp) Download(Song) (string, error) {
	d.dlHits++
	if d.onHit != nil {
		fn := d.onHit
		d.onHit = nil
		fn()
	}
	return "/music/a.flac", nil
}

// 已经开跑的这一轮必须能被停止中断 —— 否则用户点了停止，这一轮还会继续
// 登记「待下载」甚至继续落盘，看起来就是「没停掉」。
func TestRunOnceAbortsWhenMonitorDisabledMidRound(t *testing.T) {
	songs := []Song{
		{Source: "wy", ID: "1", Name: "一", Artist: "A", URL: "https://cdn/1"},
		{Source: "wy", ID: "2", Name: "二", Artist: "B", URL: "https://cdn/2"},
		{Source: "wy", ID: "3", Name: "三", Artist: "C", URL: "https://cdn/3"},
	}
	disp := &stopMidDisp{songs: songs}
	h, mon := newRunFixture(t, disp, songs)
	disp.onHit = func() {
		if _, err := h.store.UpdateMonitor("m1", func(m *Monitor) { m.Enabled = false }); err != nil {
			t.Errorf("停止监控失败：%v", err)
		}
	}

	run := h.RunOnce(mon)

	if disp.dlHits != 1 {
		t.Fatalf("停止后不该继续下载，实际下载 %d 次", disp.dlHits)
	}
	if run.Downloaded != 1 {
		t.Fatalf("应只完成第一首，实际 %+v", run)
	}
	if run.Message != "订阅已停止，本轮中止" {
		t.Fatalf("应说明本轮被停止，实际 %q", run.Message)
	}
	if tr, _ := h.store.GetTrack("m1", "wy", "2"); tr != nil {
		t.Fatalf("停止后不该继续处理后续曲目，实际 %+v", tr)
	}
}
