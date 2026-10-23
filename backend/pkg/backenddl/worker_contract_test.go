package backenddl

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组补的是「关掉网页也照下」最容易被破坏、但平时看不出来的三条性质：
// 串行、取音不继承提交请求的 ctx、失败后能重试。已有 queue_test.go 覆盖了
// 幂等、落盘回滚、暂停/停止、重启恢复；这里不重复。

// 两条任务同时排队时，任何时刻最多一条在取音。
func TestWorkerRunsSerially(t *testing.T) {
	var inflight, maxInflight int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})

	q := newTestQueue(t, func(ctx context.Context, it Item) (Result, error) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&maxInflight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInflight, old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		atomic.AddInt32(&inflight, -1)
		return Result{Path: "/music/" + it.ID + ".flac", Quality: "flac"}, nil
	})
	q.Start()
	defer q.Close()

	added, _, err := q.Submit([]Item{{Name: "一"}, {Name: "二"}}, false, false)
	if err != nil || len(added) != 2 {
		t.Fatalf("Submit: added=%d err=%v", len(added), err)
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 一直没开始取音")
	}
	select {
	case <-entered:
		t.Fatal("第二条在第一条没结束时就开始取音：队列本应串行")
	case <-time.After(120 * time.Millisecond):
	}
	close(release)
	waitStatus(t, q, added[0].ID, StatusSuccess)
	waitStatus(t, q, added[1].ID, StatusSuccess)
	if got := atomic.LoadInt32(&maxInflight); got != 1 {
		t.Fatalf("最大并发 = %d，想要 1", got)
	}
}

// 取音用的是 worker 自己的 5 分钟 ctx，不是提交请求的 ctx。
// 这条如果破了，「浏览器刷新/接口超时」就会把后台任务一起取消。
func TestWorkerContextIsDetachedFromSubmit(t *testing.T) {
	var ok int32
	q := newTestQueue(t, func(ctx context.Context, _ Item) (Result, error) {
		dl, has := ctx.Deadline()
		if has && time.Until(dl) > 4*time.Minute && ctx.Err() == nil {
			atomic.StoreInt32(&ok, 1)
		}
		return Result{Path: "/music/a.flac"}, nil
	})
	q.Start()
	defer q.Close()

	// 模拟一个「提交后立刻结束」的请求 ctx：它被取消不应影响后台任务。
	reqCtx, cancel := context.WithCancel(context.Background())
	added, _, err := q.Submit([]Item{{Name: "夜曲"}}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := reqCtx.Err(); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitStatus(t, q, added[0].ID, StatusSuccess)
	if atomic.LoadInt32(&ok) != 1 {
		t.Fatal("取音 ctx 没有自己的 5 分钟上限（或已被提交请求取消）")
	}
}

// failed → retry 能再跑一次；Tries 累加，成功后清掉错误。
func TestWorkerRetryAfterFailure(t *testing.T) {
	var calls int32
	q := newTestQueue(t, func(context.Context, Item) (Result, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return Result{}, errors.New("源暂时不可用")
		}
		return Result{Path: "/music/ok.flac", Quality: "flac"}, nil
	})
	q.Start()
	defer q.Close()

	added, _, err := q.Submit([]Item{{Name: "夜曲"}}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	id := added[0].ID
	it := waitStatus(t, q, id, StatusFailed)
	if it.Tries != 1 || it.Error == "" {
		t.Fatalf("失败后 tries=%d error=%q", it.Tries, it.Error)
	}
	if hit, err := q.Apply(ActionRetry, []string{id}); err != nil || hit != 1 {
		t.Fatalf("retry: hit=%d err=%v", hit, err)
	}
	it = waitStatus(t, q, id, StatusSuccess)
	if it.Tries != 2 {
		t.Fatalf("重试后 tries=%d，想要 2", it.Tries)
	}
	if it.Error != "" {
		t.Fatalf("成功后 error 没清掉：%q", it.Error)
	}
}
