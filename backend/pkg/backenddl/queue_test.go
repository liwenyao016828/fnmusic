package backenddl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组测试盯的是「用户看得见的行为」：不重复下载、暂停不会被覆盖、
// 重启后停掉的任务不自己跑、磁盘坏了不假装成功。

func newTestQueue(t *testing.T, fetch Fetcher) *Queue {
	t.Helper()
	dir := t.TempDir()
	return New(dir, fetch, func(string, ...any) {})
}

func waitStatus(t *testing.T, q *Queue, id, want string) Item {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := q.Get(id); ok && it.Status == want {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := q.Get(id)
	t.Fatalf("等待状态 %q 超时，当前是 %q（%s）", want, it.Status, it.Error)
	return Item{}
}

func TestSubmitIsIdempotentPerClientID(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/music/" + it.Name + ".flac", Quality: "flac"}, nil
	})
	it := Item{ClientID: "c1", Name: "夜曲", Singer: "周杰伦"}

	added, _, err := q.Submit([]Item{it}, false, false)
	if err != nil || len(added) != 1 {
		t.Fatalf("首次提交：added=%v err=%v", added, err)
	}
	id := added[0].ID

	// 队员重复提交（网络超时重试）必须回同一条确认，而不是再排一条。
	added2, _, err := q.Submit([]Item{it}, false, false)
	if err != nil || len(added2) != 1 || added2[0].ID != id || !added2[0].Existing {
		t.Fatalf("重复提交应回原任务确认：%+v err=%v", added2, err)
	}
	if n := len(q.List()); n != 1 {
		t.Fatalf("队列里应为 1 条，实际 %d", n)
	}
}

func TestSubmitSuccessStillConfirmsWithoutRedownload(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/music/a.flac", Quality: "flac"}, nil
	})
	// 先手动放一条已成功的记录：重复提交不能把它再下一遍。
	q.items = []Item{{ID: "bd-1", ClientID: "c1", Name: "夜曲", Singer: "周杰伦", Status: StatusSuccess, Path: "/music/a.flac"}}
	q.started = false

	added, _, err := q.Submit([]Item{{ClientID: "c1", Name: "夜曲", Singer: "周杰伦"}}, false, false)
	if err != nil || len(added) != 1 || !added[0].Existing || added[0].ID != "bd-1" {
		t.Fatalf("已成功的任务应回确认：%+v err=%v", added, err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("不应触发任何下载，实际 %d 次", got)
	}
}

func TestForceReenqueueReusesID(t *testing.T) {
	q := newTestQueue(t, nil)
	q.items = []Item{{ID: "bd-1", ClientID: "c1", Name: "夜曲", Singer: "周杰伦", Status: StatusSuccess, Path: "/old.flac", Tries: 2}}

	added, _, err := q.Submit([]Item{{ClientID: "c1", Name: "夜曲", Singer: "周杰伦", Quality: "flac"}}, true, false)
	if err != nil || len(added) != 1 || added[0].ID != "bd-1" || added[0].Existing {
		t.Fatalf("force 应原地重排并复用 id：%+v err=%v", added, err)
	}
	got, _ := q.Get("bd-1")
	if got.Status != StatusPending || got.Path != "" || got.Tries != 0 || got.Quality != "flac" {
		t.Fatalf("重排后状态不对：%+v", got)
	}
}

func TestSubmitRollsBackWhenDiskFails(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := New(blocked, nil, func(string, ...any) {}) // 路径变成 blocked/backend_download_queue.json，必然写不进去

	added, skipped, err := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false)
	if err == nil {
		t.Fatalf("落盘失败必须返回错误，却得到 added=%v skipped=%v", added, skipped)
	}
	if len(q.List()) != 0 {
		t.Fatalf("落盘失败必须整体回滚，队列却留下了 %d 条", len(q.List()))
	}
}

func TestLoadRecoveryKeepsUserStopAndAutoResumesOnlyDownloading(t *testing.T) {
	dir := t.TempDir()
	p := persisted{Paused: true, Items: []Item{
		{ID: "a", Name: "下到一半", Status: StatusDownloading, Tries: 1},
		{ID: "b", Name: "多次中断", Status: StatusDownloading, Tries: 5},
		{ID: "c", Name: "用户停止", Status: StatusStopped},
		{ID: "d", Name: "用户暂停", Status: StatusPaused},
		{ID: "e", Name: "已下好", Status: StatusSuccess, Path: "/x.flac"},
	}}
	data, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "backend_download_queue.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	q := New(dir, nil, func(string, ...any) {})
	if !q.Paused() {
		t.Fatalf("重启后「暂停全部」必须保留")
	}
	if it, _ := q.Get("a"); it.Status != StatusPending {
		t.Fatalf("downloading（1 次）应重排 pending，实际 %s", it.Status)
	}
	if it, _ := q.Get("b"); it.Status != StatusFailed {
		t.Fatalf("多次中断应标 failed，实际 %s", it.Status)
	}
	if it, _ := q.Get("c"); it.Status != StatusStopped {
		t.Fatalf("用户停止的任务重启后不能自己跑，实际 %s", it.Status)
	}
	if it, _ := q.Get("d"); it.Status != StatusPaused {
		t.Fatalf("用户暂停的任务重启后不能自己跑，实际 %s", it.Status)
	}
	if it, _ := q.Get("e"); it.Status != StatusSuccess || it.Path != "/x.flac" {
		t.Fatalf("成功历史不能丢：%+v", it)
	}
}

func TestStopCancelsInflightAndDoesNotBecomeFailed(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	added, _, err := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	id := added[0].ID
	q.Start()
	defer q.Close()
	<-started

	if _, err := q.Apply(ActionStop, []string{id}); err != nil {
		t.Fatal(err)
	}
	if it, _ := q.Get(id); it.Status != StatusStopped {
		t.Fatalf("停止后应是 stopped，实际 %s", it.Status)
	}
	// 给 worker 一点时间回来；它不能把用户明确停止的任务覆盖成 failed/success。
	time.Sleep(150 * time.Millisecond)
	if it, _ := q.Get(id); it.Status != StatusStopped {
		t.Fatalf("worker 回写覆盖了用户操作：%s（%s）", it.Status, it.Error)
	}
}

func TestPauseDownloadingCancelsAndKeepsPaused(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	added, _, _ := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false)
	id := added[0].ID
	q.Start()
	defer q.Close()
	<-started

	if _, err := q.Apply(ActionPause, []string{id}); err != nil {
		t.Fatal(err)
	}
	if it, _ := q.Get(id); it.Status != StatusPaused {
		t.Fatalf("暂停后应是 paused，实际 %s", it.Status)
	}
	time.Sleep(150 * time.Millisecond)
	if it, _ := q.Get(id); it.Status != StatusPaused {
		t.Fatalf("worker 回写覆盖了暂停：%s", it.Status)
	}
	// 继续 → pending，worker 会再领它（这里只验证状态迁移）。
	if hit, err := q.Apply(ActionResume, []string{id}); err != nil || hit != 1 {
		t.Fatalf("resume：hit=%d err=%v", hit, err)
	}
}

func TestQueuePausePreventsClaiming(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/x.flac"}, nil
	})
	q.Start()
	defer q.Close()
	if hit, err := q.Apply(ActionPause, nil); err != nil || hit != 1 {
		t.Fatalf("整队暂停：hit=%d err=%v", hit, err)
	}
	if _, _, err := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("暂停时不能开跑，实际跑了 %d 次", got)
	}
	if it := q.List()[0]; it.Status != StatusPending {
		t.Fatalf("暂停时提交的任务应停在 pending，实际 %s", it.Status)
	}
	if _, err := q.Apply(ActionResume, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatalf("继续后应立即开跑")
	}
}

func TestRemoveRejectsDownloading(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	added, _, _ := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false)
	id := added[0].ID
	q.Start()
	defer q.Close()
	<-started

	if hit, err := q.Apply(ActionRemove, []string{id}); err != nil || hit != 0 {
		t.Fatalf("正在下载的不能删：hit=%d err=%v", hit, err)
	}
	if _, ok := q.Get(id); !ok {
		t.Fatalf("正在下载的记录被删掉了")
	}
}

func TestMigrationPreservesHistoryAndDoesNotRunStopped(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{Path: "/x.flac"}, nil
	})
	q.Start()
	defer q.Close()

	items := []Item{
		{ClientID: "m1", Name: "成功的", Status: StatusSuccess, Path: "/done.flac", ActualQuality: "flac", Size: 123},
		{ClientID: "m2", Name: "停掉的", Status: StatusStopped, Error: "用户停止"},
		{ClientID: "m3", Name: "暂停的", Status: StatusPaused},
		{ClientID: "m4", Name: "失败的", Status: StatusFailed, Error: "源挂了", Tries: 3},
	}
	added, skipped, err := q.Submit(items, false, true)
	if err != nil || len(added) != 4 || len(skipped) != 0 {
		t.Fatalf("迁移：added=%v skipped=%v err=%v", added, skipped, err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("迁移的历史/停掉的任务不能开跑，实际 %d 次", got)
	}
	if it, _ := q.Get(added[0].ID); it.Status != StatusSuccess || it.Path != "/done.flac" || it.Size != 123 {
		t.Fatalf("成功历史必须保留 path/quality/size：%+v", it)
	}
	if it, _ := q.Get(added[1].ID); it.Status != StatusStopped || it.Error != "用户停止" {
		t.Fatalf("停止状态与原因必须保留：%+v", it)
	}
	if it, _ := q.Get(added[3].ID); it.Status != StatusFailed || it.Tries != 3 {
		t.Fatalf("失败状态与尝试次数必须保留：%+v", it)
	}

	// 迁移幂等：再导一次只回确认，不重复建。
	added2, _, err := q.Submit(items, false, true)
	if err != nil || len(added2) != 4 {
		t.Fatalf("重复迁移：%+v err=%v", added2, err)
	}
	for _, a := range added2 {
		if !a.Existing {
			t.Fatalf("重复迁移应回 existing 确认：%+v", a)
		}
	}
	if n := len(q.List()); n != 4 {
		t.Fatalf("重复迁移不应新建记录，实际 %d 条", n)
	}
}

func TestMigrationRejectsUnknownStatus(t *testing.T) {
	q := newTestQueue(t, nil)
	_, skipped, err := q.Submit([]Item{{ClientID: "x", Name: "歌", Status: "cancelled"}}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Reason != SkipInvalid {
		t.Fatalf("未知状态必须 skipped(invalid)，实际 %+v", skipped)
	}
}

func TestWaitReturnsTerminalAndTimesOut(t *testing.T) {
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		return Result{Path: "/ok.flac", Quality: "flac"}, nil
	})
	added, _, _ := q.Submit([]Item{{ClientID: "c1", Name: "夜曲"}}, false, false)
	id := added[0].ID
	q.Start()
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	it, ok := q.Wait(ctx, id)
	if !ok || it.Status != StatusSuccess || it.Path != "/ok.flac" {
		t.Fatalf("Wait 应等到成功：%+v ok=%v", it, ok)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if _, ok := q.Wait(ctx2, "不存在"); ok {
		t.Fatalf("不存在的 id 应返回 false")
	}
}

func TestUnknownAction(t *testing.T) {
	q := newTestQueue(t, nil)
	if _, err := q.Apply("explode", nil); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("未知动作应回 ErrUnknownAction，实际 %v", err)
	}
}

// 迁移进来的历史超过上限时只裁最旧的，不能连新记录一起扔。
// 2026-10-23 实测：老浏览器队列 266 条迁进来，上限 200 让 66 条历史直接消失。
func TestTrimKeepsNewestHistory(t *testing.T) {
	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		return Result{}, errors.New("历史记录不该被执行")
	})
	total := limitTerminal + 12
	items := make([]Item, 0, total)
	for i := 0; i < total; i++ {
		items = append(items, Item{
			ClientID:  fmt.Sprintf("h-%d", i),
			Name:      fmt.Sprintf("歌%d", i),
			Singer:    "测试",
			Status:    StatusSuccess,
			Path:      "/music/x.mp3",
			AddedAt:   int64(1000 + i),
			UpdatedAt: int64(1000 + i),
		})
	}
	added, skipped, err := q.Submit(items, false, true)
	if err != nil {
		t.Fatalf("迁移提交失败：%v", err)
	}
	if len(skipped) != 0 || len(added) != total {
		t.Fatalf("迁移应全数入队：added=%d skipped=%d", len(added), len(skipped))
	}
	list := q.List()
	if len(list) != limitTerminal {
		t.Fatalf("历史应裁到 %d 条，实际 %d", limitTerminal, len(list))
	}
	for _, it := range list {
		if it.ClientID == "h-0" {
			t.Fatalf("最旧的历史应被裁掉")
		}
		if it.ClientID == fmt.Sprintf("h-%d", total-1) {
			return
		}
	}
	t.Fatalf("最新的历史不能被裁")
}
