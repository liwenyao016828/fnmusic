package backenddl

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组盯的是「停止全部」：stop / cancel **不带 ids**。
//
// 前端 DownloadQueuePanel 的「停止全部」走 downloadManager.stopAll() →
// runAction('stop', [])，空 ids 是后端的整队停止契约（见
// frontend/src/services/downloadManager.js）。历史实现把 ids 当唯一来源，
// 空列表 = 一条都不动：用户点停止，当前那首停了（或连当前都没停），worker
// 紧接着把下一首开起来 —— 2026-11-03 用户报的就是这个。
//
// 契约（与 retry_failed 不带 ids 的整队语义同源）：
//   - pending / downloading / paused 全部原子停止；
//   - 在途取音被取消，且不会被 runOne 回写成 failed/success；
//   - success / failed / stopped 这些终态不动；
//   - 整队暂停位（q.paused）不被改动，重复点幂等（第二次 hit=0）；
//   - 停完之后下一首不得开跑 —— 即使随后「继续队列」也不跑（任务已是 stopped）。
func TestStopAllWithoutIDsStopsEveryActiveState(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	var calls int32

	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-ctx.Done() // 一直挂住，只有被取消才返回：证明「在途被收掉」而不是自己结束
		return Result{}, ctx.Err()
	})
	defer q.Close()

	added, _, err := q.Submit([]Item{
		{ClientID: "run", Name: "正在下的"},
		{ClientID: "wait1", Name: "排队一"},
		{ClientID: "wait2", Name: "排队二"},
		{ClientID: "pause", Name: "单独暂停的", Status: StatusPaused},
		{ClientID: "ok", Name: "成功的", Status: StatusSuccess, Path: "/music/done.flac", ActualQuality: "flac", Size: 42},
		{ClientID: "bad", Name: "失败的", Status: StatusFailed, Error: "源挂了", Tries: 2},
		{ClientID: "stop0", Name: "早先停的", Status: StatusStopped, Error: "用户停止"},
	}, false, true)
	if err != nil || len(added) != 7 {
		t.Fatalf("准备数据失败：added=%v err=%v", added, err)
	}

	q.Start()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 一直没开始第一条")
	}
	// 等到真的进入 downloading 再点「停止全部」，模拟用户看到的现场。
	waitStatus(t, q, added[0].ID, StatusDownloading)

	hit, err := q.Apply(ActionStop, nil)
	if err != nil {
		t.Fatalf("停止全部报错：%v", err)
	}
	if hit != 4 {
		t.Fatalf("不带 ids 应停掉 downloading+pending+paused 共 4 条，实际 hit=%d", hit)
	}

	for i, a := range added[:3] {
		it, ok := q.Get(a.ID)
		if !ok || it.Status != StatusStopped {
			t.Fatalf("第 %d 条应被整队停止（当前 %s）", i+1, it.Status)
		}
		if it.Error != "已停止（点重试继续）" {
			t.Fatalf("第 %d 条停止后应留下可恢复提示，实际 %q", i+1, it.Error)
		}
	}
	if it, _ := q.Get(added[3].ID); it.Status != StatusStopped {
		t.Fatalf("单独 paused 的也要停：%+v", it)
	}

	// 终态一条都不能动（成功的历史连 path/size 都要原样）。
	if it, _ := q.Get(added[4].ID); it.Status != StatusSuccess || it.Path != "/music/done.flac" || it.Size != 42 {
		t.Fatalf("已成功的不能动：%+v", it)
	}
	if it, _ := q.Get(added[5].ID); it.Status != StatusFailed || it.Error != "源挂了" || it.Tries != 2 {
		t.Fatalf("已失败的不能动：%+v", it)
	}
	if it, _ := q.Get(added[6].ID); it.Status != StatusStopped || it.Error != "用户停止" {
		t.Fatalf("早就 stopped 的不能动（原因要保留）：%+v", it)
	}

	// 在途那条被取消回来后，状态必须仍是 stopped（不能被回写成 failed）。
	time.Sleep(150 * time.Millisecond)
	if it, _ := q.Get(added[0].ID); it.Status != StatusStopped {
		t.Fatalf("在途取消回来后状态被覆盖成 %s", it.Status)
	}

	// 下一首不会启动：整队都停完了，worker 没有可领取的。
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("停止全部之后不应再有任何取音，实际 %d 次", got)
	}

	// 幂等：再点一次没有活动任务 → hit=0。
	if hit2, err := q.Apply(ActionStop, nil); err != nil || hit2 != 0 {
		t.Fatalf("重复点停止全部应 hit=0，实际 %d err=%v", hit2, err)
	}
}

// cancel 是 stop 的别名，不带 ids 同样是整队停止。
func TestCancelWithoutIDsAlsoStopsWholeQueue(t *testing.T) {
	q := newTestQueue(t, nil)
	added, _, err := q.Submit([]Item{
		{ClientID: "a", Name: "排队一"},
		{ClientID: "b", Name: "排队二"},
	}, false, true)
	if err != nil || len(added) != 2 {
		t.Fatalf("准备数据失败：added=%v err=%v", added, err)
	}
	hit, err := q.Apply(ActionCancel, nil)
	if err != nil || hit != 2 {
		t.Fatalf("cancel 不带 ids 应停掉 2 条，实际 hit=%d err=%v", hit, err)
	}
	for i, a := range added {
		if it, _ := q.Get(a.ID); it.Status != StatusStopped {
			t.Fatalf("第 %d 条应被停止，实际 %s", i+1, it.Status)
		}
	}
}

// 整队暂停位不吃「停止全部」：停的是任务，不是暂停开关。
// 队列处于暂停态时（任务都还 pending），停全部照样把它们停掉，
// 且 paused 保持 true；随后「继续队列」也不会让它们跑起来（已经是 stopped）。
func TestStopAllKeepsQueuePauseFlagAndDoesNotRestart(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(context.Context, Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/music/x.flac"}, nil
	})
	defer q.Close()

	added, _, err := q.Submit([]Item{
		{ClientID: "a", Name: "排队一"},
		{ClientID: "b", Name: "排队二", Status: StatusPaused},
		{ClientID: "c", Name: "成功的", Status: StatusSuccess, Path: "/music/c.flac"},
	}, false, true)
	if err != nil || len(added) != 3 {
		t.Fatalf("准备数据失败：added=%v err=%v", added, err)
	}
	if hit, err := q.Apply(ActionPause, nil); err != nil || hit != 1 {
		t.Fatalf("整队暂停应 hit=1，实际 %d err=%v", hit, err)
	}
	if !q.Paused() {
		t.Fatal("整队暂停位没打开")
	}

	hit, err := q.Apply(ActionStop, nil)
	if err != nil || hit != 2 {
		t.Fatalf("暂停态下停全部应停 2 条活动任务，实际 hit=%d err=%v", hit, err)
	}
	if !q.Paused() {
		t.Fatal("「停止全部」把整队暂停位改掉了（应为不动）")
	}
	if it, _ := q.Get(added[2].ID); it.Status != StatusSuccess || it.Path != "/music/c.flac" {
		t.Fatalf("终态不该被动：%+v", it)
	}

	// 继续整队：已经没有 pending 可领，一次取音都不该发生。
	q.Start()
	if hit, err := q.Apply(ActionResume, nil); err != nil || hit != 1 {
		t.Fatalf("继续整队应 hit=1，实际 %d err=%v", hit, err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("停止全部后再继续队列不应开跑，实际取音 %d 次", got)
	}
}

// 停止全部只动了它该动的：显式点名（带 ids）仍然是「只停这几条」。
func TestStopWithIDsStillOnlyTouchesGiven(t *testing.T) {
	q := newTestQueue(t, nil)
	added, _, err := q.Submit([]Item{
		{ClientID: "a", Name: "点名停的"},
		{ClientID: "b", Name: "没点名的"},
	}, false, true)
	if err != nil || len(added) != 2 {
		t.Fatalf("准备数据失败：added=%v err=%v", added, err)
	}
	hit, err := q.Apply(ActionStop, []string{added[0].ID})
	if err != nil || hit != 1 {
		t.Fatalf("带 ids 应只停 1 条，实际 hit=%d err=%v", hit, err)
	}
	if it, _ := q.Get(added[1].ID); it.Status != StatusPending {
		t.Fatalf("没点名的任务不该被停：%+v", it)
	}
}

// claimNext 把状态翻成 downloading 之后、runOne 登记 inflight 之前，用户点了
// 「停止全部」：这条没有取消句柄可收，若不补检查就会照常取音并在结束后把用户
// 的 stopped 覆盖成 success/failed（正是「停了又自己下起来」的微观成因之一）。
func TestRunOneSkipsFetchStoppedInClaimWindow(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(context.Context, Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/music/should-not-happen.flac"}, nil
	})
	defer q.Close()

	// 模拟「已领取（worker 手里是 downloading 副本）但队列里已被改成 stopped」。
	q.mu.Lock()
	q.items = []Item{{ID: "bd-window", ClientID: "w1", Name: "窗口里的歌", Status: StatusStopped, Error: "已停止（点重试继续）"}}
	q.mu.Unlock()

	q.runOne(Item{ID: "bd-window", ClientID: "w1", Name: "窗口里的歌", Status: StatusDownloading})

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("已被停止的任务不能再取音，实际取音 %d 次", got)
	}
	if it, _ := q.Get("bd-window"); it.Status != StatusStopped {
		t.Fatalf("状态被覆盖成 %s", it.Status)
	}
}
