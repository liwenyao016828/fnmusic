package search

import (
	"sync"
	"testing"
	"time"
)

// restoreThrottle 把全局限速器还原成默认值，避免测试之间互相污染。
func restoreThrottle(t *testing.T) {
	t.Helper()
	prev := MinInterval()
	SetMinInterval(prev)
	t.Cleanup(func() { SetMinInterval(prev) })
}

// TestThrottleSpacesRequests 连续调用必须被拉开间隔（不是立即连发）。
func TestThrottleSpacesRequests(t *testing.T) {
	restoreThrottle(t)
	const interval = 40 * time.Millisecond
	SetMinInterval(interval)

	start := time.Now()
	for i := 0; i < 3; i++ {
		throttle()
	}
	elapsed := time.Since(start)

	// 3 次调用 = 2 个间隔，每个间隔至少 0.5×interval（抖动下限）→ 至少 1×interval。
	if elapsed < interval*9/10 {
		t.Fatalf("3 次 throttle 只用了 %v，间隔没生效（期望 >= %v）", elapsed, interval*9/10)
	}
	// 上限：抖动最多 1.5×interval，2 个间隔 → 3×interval 已经足够宽松。
	if elapsed > interval*4 {
		t.Fatalf("3 次 throttle 用了 %v，抖动超出预期（期望 <= %v）", elapsed, interval*4)
	}
}

// TestThrottleDisabled 间隔为 0 时必须完全不等待。
func TestThrottleDisabled(t *testing.T) {
	restoreThrottle(t)
	SetMinInterval(0)

	start := time.Now()
	for i := 0; i < 200; i++ {
		throttle()
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("限速关闭后 200 次 throttle 仍耗时 %v", elapsed)
	}
}

// TestThrottleConcurrentReservationsSpread 并发调用时，各请求的**发出时刻**仍然
// 被均匀拉开 —— 这正是「预约时间片」而不是「各自 sleep」的意义：
// 每个调用者拿到的 slot 互不重叠，且相邻 slot 至少间隔 0.5×interval。
func TestThrottleConcurrentReservationsSpread(t *testing.T) {
	restoreThrottle(t)
	const interval = 30 * time.Millisecond
	SetMinInterval(interval)

	const n = 5
	var mu sync.Mutex
	sends := make([]time.Time, 0, n)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			throttle()
			mu.Lock()
			sends = append(sends, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(sends) != n {
		t.Fatalf("拿到 %d 个发出时刻，期望 %d", len(sends), n)
	}
	for i := 1; i < len(sends); i++ {
		for j := i; j < len(sends); j++ {
			if sends[j].Before(sends[i]) {
				sends[i], sends[j] = sends[j], sends[i]
			}
		}
	}
	// 抖动下限 0.5×interval，留一点调度误差余量。
	for i := 1; i < len(sends); i++ {
		if gap := sends[i].Sub(sends[i-1]); gap < interval*2/5 {
			t.Fatalf("第 %d 与第 %d 次发出只隔 %v，并发时时间片重叠了", i-1, i, gap)
		}
	}
	// 5 次请求跨 4 个间隔，抖动上限 1.5×interval → 放宽到 8×interval。
	if span := sends[len(sends)-1].Sub(sends[0]); span > interval*8 {
		t.Fatalf("%d 次并发请求跨了 %v，远超预期（期望 <= %v）", n, span, interval*8)
	}
}

// TestMinIntervalRoundTrip 读写一致。
func TestMinIntervalRoundTrip(t *testing.T) {
	restoreThrottle(t)
	SetMinInterval(123 * time.Millisecond)
	if got := MinInterval(); got != 123*time.Millisecond {
		t.Fatalf("MinInterval() = %v，期望 123ms", got)
	}
}

// TestParseMinIntervalEnv 环境变量解析：只接受非负整数毫秒，其余一律回默认。
func TestParseMinIntervalEnv(t *testing.T) {
	cases := []struct {
		in     string
		want   time.Duration
		wantOK bool
	}{
		{"", 0, false},
		{"0", 0, true}, // 显式关闭限速
		{"300", 300 * time.Millisecond, true},
		{"1500", 1500 * time.Millisecond, true},
		{"-1", 0, false},   // 负值忽略
		{"abc", 0, false},  // 非数字忽略
		{"12.5", 0, false}, // 只收整数毫秒
		{" 300", 0, false}, // 带空格不认（避免「看着配了其实没配」）
	}
	for _, c := range cases {
		got, ok := parseMinIntervalEnv(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("parseMinIntervalEnv(%q) = (%v, %v)，期望 (%v, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}
