package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/library"
)

// resetCompleteJob 把全局任务状态按回「没在跑」，避免用例之间互相影响。
//
// 直接 `completeJob = completeJobState{}` 会连互斥锁一起复制，这里显式改字段。
func resetCompleteJob(t *testing.T) {
	t.Helper()
	completeJob.mu.Lock()
	completeJob.running = false
	completeJob.cancel = nil
	completeJob.ctx = nil
	completeJob.cancelled = false
	completeJob.summary = nil
	completeJob.errMsg = ""
	completeJob.mu.Unlock()
}

func TestCompleteJobCancelRunReturnsFalseWhenIdle(t *testing.T) {
	resetCompleteJob(t)

	if completeJob.cancelRun() {
		t.Fatal("没在跑的任务不该回「已停止」—— 那会让用户以为停了什么")
	}
	// 也不能顺手把「被停止」标记点亮：那个标记是给界面说「上次是被停的」
	if got := completeJob.snapshot().Cancelled; got {
		t.Fatal("空跑一次停止请求不该把任务标成已停止")
	}
}

func TestCompleteJobCancelRunCancelsRunningJob(t *testing.T) {
	resetCompleteJob(t)

	ctx := completeJob.start("/m", 3)
	if ctx.Err() != nil {
		t.Fatal("刚启动的 ctx 不该是已取消的")
	}
	if !completeJob.cancelRun() {
		t.Fatal("正在跑的任务应返回 true")
	}
	if ctx.Err() == nil {
		t.Fatal("停止请求必须真的取消 ctx，否则执行方收不到信号")
	}

	snap := completeJob.snapshot()
	// running 仍为 true：取消只是发信号，任务要等当前这首先写完才收尾
	if !snap.Running {
		t.Fatal("取消信号发出后，任务还没收尾，running 应仍为 true")
	}
	if !snap.Cancelled {
		t.Fatal("快照应标记这次是被停止的")
	}

	// 收尾后：cancel 被释放，再点停止要如实回 false；但「被停止」这件事要留着
	completeJob.finish(&complete.Summary{})
	if completeJob.cancelRun() {
		t.Fatal("任务收尾后不该还能取消")
	}
	if !completeJob.snapshot().Cancelled {
		t.Fatal("收尾后应保留「上次是被停止的」标记，界面靠它区分跑完与被停")
	}
	resetCompleteJob(t)
}

func TestCompleteJobStartClearsCancelledFlag(t *testing.T) {
	resetCompleteJob(t)

	ctx := completeJob.start("/m", 1)
	completeJob.cancelRun()
	<-ctx.Done()

	completeJob.start("/m", 2)
	snap := completeJob.snapshot()
	if snap.Cancelled {
		t.Fatal("新任务不该继承上一轮的「已停止」标记")
	}
	if snap.Done != 0 {
		t.Fatalf("新任务的进度应从 0 开始，得到 %d", snap.Done)
	}
	resetCompleteJob(t)
}

func TestHandleLibraryCompleteCancelWithoutRunningJob(t *testing.T) {
	resetCompleteJob(t)
	s := &Server{}

	rec := httptest.NewRecorder()
	s.HandleLibraryCompleteCancel(rec, httptest.NewRequest(http.MethodPost, "/api/library/complete/cancel", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("没在跑时应 200 并如实说明，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	var body struct {
		Code    int                    `json:"code"`
		Message string                 `json:"message"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应: %v（%s）", err, rec.Body.String())
	}
	if !strings.Contains(body.Message, "没有正在跑的补全任务") {
		t.Fatalf("消息应如实说明没有任务在跑，得到 %q", body.Message)
	}
	if body.Data == nil {
		t.Fatal("应一并返回任务快照，界面才能显示上次结果")
	}
	if _, ok := body.Data["running"]; !ok {
		t.Fatalf("快照里应有 running 字段，得到 %v", body.Data)
	}
	// cancelled 是 omitempty：没停过的时候别凭空冒出一个 false
	if strings.Contains(rec.Body.String(), `"cancelled"`) {
		t.Fatalf("没停过的任务不该带 cancelled 字段，得到 %s", rec.Body.String())
	}
}

func TestHandleLibraryCompleteCancelStopsRunningJob(t *testing.T) {
	resetCompleteJob(t)
	s := &Server{}

	ctx := completeJob.start("/m", 5)

	rec := httptest.NewRecorder()
	s.HandleLibraryCompleteCancel(rec, httptest.NewRequest(http.MethodPost, "/api/library/complete/cancel", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("停止在跑的任务应 200，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	if ctx.Err() == nil {
		t.Fatal("接口必须真的把任务停掉（ctx 被取消）")
	}
	if !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Fatalf("响应快照应标记 cancelled:true，得到 %s", rec.Body.String())
	}
	// 语义要说清楚：已写入的保留，不是「全部撤销」
	if !strings.Contains(rec.Body.String(), "已写入的会保留") {
		t.Fatalf("消息应说明已写入的会保留，得到 %s", rec.Body.String())
	}
	resetCompleteJob(t)
}

func TestHandleLibraryCompleteCancelRejectsGet(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.HandleLibraryCompleteCancel(rec, httptest.NewRequest(http.MethodGet, "/api/library/complete/cancel", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应回 405，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "只支持 POST") {
		t.Fatalf("405 的说明应写明只支持 POST，得到 %s", rec.Body.String())
	}
}

// 停止只影响「还没轮到的」：已写入的必须留着（撤销是用户的选择，不是停止的副作用）。
// 顺带钉住 ctx 与任务的绑定关系 —— 执行方拿到的 ctx 必须就是被取消的那一个。
func TestCompleteJobStartHandsBackCancellableContext(t *testing.T) {
	resetCompleteJob(t)

	ctx := completeJob.start("/m", 1)
	if got := completeJob.ctx; got != ctx {
		t.Fatal("start 交回的 ctx 必须就是内部保存的那一个，否则取消信号送不到执行方")
	}
	completeJob.cancelRun()
	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("取消原因应为 context.Canceled，得到 %v", err)
	}
	resetCompleteJob(t)
}

// 跑完的任务不能在日志里留「任务已停止」。
//
// 这条假日志是 2026-09-26 真机冒烟抓到的：`finish()` 自己会 cancel 掉 ctx（释放闭包），
// 所以「是否被停掉」的取值必须放在 finish **之前** —— 否则每个正常跑完的任务都说自己被停了，
// 事后翻日志分不清「跑完了」还是「被叫停的」，而这正是这条日志存在的唯一理由。
func TestRunCompleteJobLogsStoppedOnlyWhenActuallyStopped(t *testing.T) {
	s := &Server{libIndex: library.NewIndex(t.TempDir())}

	run := func(cancel bool) string {
		t.Helper()
		resetCompleteJob(t)
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(old)

		ctx := completeJob.start("/m", 0)
		if cancel {
			if !completeJob.cancelRun() {
				t.Fatal("正在跑的任务应能收到停止请求")
			}
		}
		// 空计划：不碰网络，只走「执行 → 收尾 → 日志」这条路径
		s.runCompleteJob(ctx, nil, &complete.Plan{}, complete.Options{})
		return buf.String()
	}

	if out := run(false); strings.Contains(out, "任务已停止") {
		t.Fatalf("跑完的任务不该在日志里说自己被停掉（会让日志分不清跑完/叫停）：\n%s", out)
	}
	if out := run(true); !strings.Contains(out, "任务已停止") {
		t.Fatalf("真的被停掉时必须留痕，否则事后查不出是用户叫停的：\n%s", out)
	}
}
