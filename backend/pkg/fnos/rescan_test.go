package fnos

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// 一批下载会连着调很多次 Schedule，必须合并成**一次**扫描 ——
// scan-all 是全量扫描，每首一次会把 NAS 拖垮。
func TestRescanSchedulerCoalesces(t *testing.T) {
	var calls int32
	s := NewRescanScheduler(60*time.Millisecond, time.Millisecond)
	s.scan = func() error { atomic.AddInt32(&calls, 1); return nil }

	// 5 次请求都落在 debounce 窗口内
	for i := 0; i < 5; i++ {
		s.Schedule()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("5 次请求应合并成 1 次扫描，实际 %d 次", n)
	}
}

// 两次实际扫描之间要有最小间隔；间隔内的请求应**推迟**而不是丢弃
func TestRescanSchedulerDefersWithinMinGap(t *testing.T) {
	var calls int32
	s := NewRescanScheduler(20*time.Millisecond, 200*time.Millisecond)
	s.scan = func() error { atomic.AddInt32(&calls, 1); return nil }

	s.Schedule()
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("第一次应已执行，实际 %d 次", n)
	}

	// 距上次约 60ms，仍在 minGap(200ms) 内 → 不该立刻执行
	s.Schedule()
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("minGap 内不该再次扫描，实际 %d 次", n)
	}

	// 等过 minGap → 应补上那次被推迟的扫描（不是丢弃）
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("minGap 过后应执行第二次（推迟而非丢弃），实际 %d 次", n)
	}
}

func TestRescanSchedulerStopCancels(t *testing.T) {
	var calls int32
	s := NewRescanScheduler(80*time.Millisecond, time.Millisecond)
	s.scan = func() error { atomic.AddInt32(&calls, 1); return nil }

	s.Schedule()
	s.Stop()
	time.Sleep(200 * time.Millisecond)

	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("Stop 后不该执行扫描，实际 %d 次", n)
	}
}

// 非飞牛环境（没有 socket / token）时，默认扫描函数应静默跳过而不是报错
func TestDefaultSchedulerSilentlySkipsOffDevice(t *testing.T) {
	s := NewRescanScheduler(10*time.Millisecond, time.Millisecond)
	// 用默认 scan（NewMusic + Available），在开发机上应当直接返回 nil
	done := make(chan error, 1)
	go func() { done <- s.scan() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("非飞牛环境应静默跳过，实际报错: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("默认 scan 卡住了")
	}
}

func TestRescanSchedulerZeroDurationsUseDefaults(t *testing.T) {
	s := NewRescanScheduler(0, 0)
	if s.debounce != rescanDebounce {
		t.Errorf("debounce 应回退到默认值 %v，实际 %v", rescanDebounce, s.debounce)
	}
	if s.minGap != rescanMinGap {
		t.Errorf("minGap 应回退到默认值 %v，实际 %v", rescanMinGap, s.minGap)
	}
}

// 默认最小间隔必须是 10 分钟 —— 用户 2026-09-22 明确选了「调得更安静」。
// 这条钉住数值：改小时要同时想清楚「飞牛那边看起来一直在扫」会不会回来。
func TestDefaultMinGapIsTenMinutes(t *testing.T) {
	if rescanMinGap != 10*time.Minute {
		t.Errorf("默认最小间隔应为 10 分钟，实际 %v", rescanMinGap)
	}
}

// lastRun 必须落盘：进程重启后不该「归零 → 立刻又扫一次」。
// 这是真机「音乐任务一直在扫」的成因之一（重启后 30 秒就又能扫）。
func TestRescanSchedulerPersistsLastRun(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "fnos_rescan.json")

	var calls1 int32
	s1 := NewRescanScheduler(10*time.Millisecond, time.Hour)
	s1.scan = func() error { atomic.AddInt32(&calls1, 1); return nil }
	s1.SetStatePath(statePath)
	s1.Schedule()
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&calls1); n != 1 {
		t.Fatalf("第一次应执行，实际 %d 次", n)
	}

	// 模拟进程重启：新调度器读同一份状态文件
	var calls2 int32
	s2 := NewRescanScheduler(10*time.Millisecond, time.Hour)
	s2.scan = func() error { atomic.AddInt32(&calls2, 1); return nil }
	s2.SetStatePath(statePath)
	s2.Schedule()
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&calls2); n != 0 {
		t.Errorf("重启后应沿用它落盘的 lastRun（不该立刻再扫），实际 %d 次", n)
	}
}

// 没设 statePath 时不该panic，行为与以前一致（纯内存）
func TestRescanSchedulerWithoutStatePathStillWorks(t *testing.T) {
	var calls int32
	s := NewRescanScheduler(10*time.Millisecond, time.Millisecond)
	s.scan = func() error { atomic.AddInt32(&calls, 1); return nil }
	s.Schedule()
	time.Sleep(60 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("不设 statePath 也应正常扫描，实际 %d 次", n)
	}
}
