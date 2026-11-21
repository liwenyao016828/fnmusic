package fslock

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 整理（排他）要能挡住下载（共享），并在对方占着时报忙而不是死等。
func TestExclusiveBlocksWhileSharedHeld(t *testing.T) {
	release := Shared()

	if _, err := TryExclusive(120 * time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("共享被持有时应当报忙，得到 %v", err)
	}
	release()

	rel, err := TryExclusive(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("共享释放后应当能拿到排他：%v", err)
	}
	rel()
}

// 排他期间新的共享请求必须等它结束 —— 这正是「整理时下载不许写盘」的保证。
func TestSharedWaitsForExclusive(t *testing.T) {
	rel, err := TryExclusive(time.Second)
	if err != nil {
		t.Fatalf("拿排他失败: %v", err)
	}

	var got atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := Shared()
		got.Store(true)
		r()
	}()

	time.Sleep(80 * time.Millisecond)
	if got.Load() {
		t.Fatal("排他未释放时共享不该拿到")
	}
	rel()
	wg.Wait()
	if !got.Load() {
		t.Fatal("排他释放后共享应当拿到")
	}
}

// 多个下载可以同时持共享（不能因为「已经有一个在跑」就把第二个拦住）。
func TestMultipleSharedCoexist(t *testing.T) {
	done := make(chan struct{})
	a := Shared()
	go func() {
		b := Shared()
		b()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("第二个共享被拦住了")
	}
	a()
}
