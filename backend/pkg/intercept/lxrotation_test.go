package intercept

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
)

// ── 洛雪「多选轮询取最高档位」（见 lxresolve.go 的 lxBestOfRotation）──────
//
// 这一组钉的是用户要的那个语义：选了好几个洛雪音源时，**挨个问**，
// 谁给的实测档位高用谁；同档位时**先用他排在前面的那个**。
//
// 为什么值得单测：这条路上「错」的表现是静默的 —— 挑错了也只是落一个
// 低一点的档位，不报错、不失败，只有用户听出来「还是 mp3」。

// lxRotationHarness 造一个带轮询钩子的拦截层。
func lxRotationHarness(t *testing.T,
	all func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error),
	single func(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error),
	names func() []string) *Interceptor {
	t.Helper()
	cfg := Config{
		DataDir:       t.TempDir(),
		Upstream:      &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
		Logf:          func(string, ...any) {},
		LxResolve:     single,
		LxResolveAll:  all,
		LxSourceNames: names,
	}
	i := New(cfg)
	if i == nil {
		t.Fatalf("New 返回 nil（Upstream 应已给）")
	}
	return i
}

// 275 秒的曲子：4.4MB ≈ 128k，11MB ≈ 320k，59MB ≈ 无损。
const testDurS = 275

func rememberRotationItem(i *Interceptor, src, id string) {
	// 池里 `lx:<src>` 的解析器平时是搜索时顺手注册的（ensureLxResolver）；
	// 这里直接走取链，所以手动补上，否则拿到的是 ErrNoResolver。
	i.ensureLxResolver(src)
	i.rememberLxItem(src, id, map[string]any{
		"duration": testDurS, "name": "不再犹豫", "singer": "Beyond",
		"songmid": id, "_types": map[string]any{"320k": map[string]any{}, "flac": map[string]any{}},
	})
}

func TestLxBestOfRotationPicksHighestMeasuredTier(t *testing.T) {
	i := lxRotationHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
			return []lxnode.Candidate{
				// 排在前面的这条声明无损，但实测只给了 4.4MB/275s ≈ 128k
				{URL: "https://cdn.example/a.flac", Script: "念心音源.js", Status: 206, Bytes: 4_400_000},
				// 后面这条是 mp3，但实测 11MB/275s ≈ 320k
				{URL: "https://cdn.example/b.mp3", Script: "全豆要.js", Status: 206, Bytes: 11_200_000},
			}, nil
		},
		nil, nil)
	rememberRotationItem(i, "wy", "42")

	res, err := i.pool.Resolve(context.Background(), "lx:wy", "42")
	if err != nil {
		t.Fatalf("轮询取链应成功：%v", err)
	}
	// 挑的是**实测档位高**的那条，不是排在前面的那条 —— 这正是「轮询最高音质」
	if res.URL != "https://cdn.example/b.mp3" {
		t.Fatalf("应挑实测 320k 的那条，得到 %q", res.URL)
	}
	if online.TierRank(res.Tier) != online.TierRank(online.Tier320k) {
		t.Fatalf("档位应按实测算成 320k，得到 %q", res.Tier)
	}
}

func TestLxBestOfRotationTieKeepsFirstInOrder(t *testing.T) {
	i := lxRotationHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
			return []lxnode.Candidate{
				{URL: "https://cdn.example/first.mp3", Script: "全豆要.js", Status: 206, Bytes: 11_200_000},
				{URL: "https://cdn.example/second.mp3", Script: "念心音源.js", Status: 206, Bytes: 11_300_000},
			}, nil
		},
		nil, nil)
	rememberRotationItem(i, "wy", "43")

	res, err := i.pool.Resolve(context.Background(), "lx:wy", "43")
	if err != nil {
		t.Fatalf("轮询取链应成功：%v", err)
	}
	// 同为 320k：用**用户排在前面的**那个（数组顺序就是轮询顺序）
	if res.URL != "https://cdn.example/first.mp3" {
		t.Fatalf("同档位应保留排在前面的候选，得到 %q", res.URL)
	}
}

func TestLxBestOfRotationPassesSelectedScriptNames(t *testing.T) {
	var got []string
	i := lxRotationHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
			got = append([]string(nil), scripts...)
			return []lxnode.Candidate{{URL: "https://cdn.example/x.flac", Script: "全豆要.js", Status: 206, Bytes: 60_000_000}}, nil
		},
		nil,
		func() []string { return []string{"全豆要.js", "念心音源.js"} })
	rememberRotationItem(i, "wy", "44")

	if _, err := i.pool.Resolve(context.Background(), "lx:wy", "44"); err != nil {
		t.Fatalf("轮询取链应成功：%v", err)
	}
	if len(got) != 2 || got[0] != "全豆要.js" || got[1] != "念心音源.js" {
		t.Fatalf("应把用户选中的脚本名按顺序交给宿主，得到 %v", got)
	}
}

func TestLxBestOfRotationFallsBackToSingleWhenCollectFails(t *testing.T) {
	singleCalled := false
	i := lxRotationHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
			// 老宿主不认 collect：回 502 → 宿主层翻译成 error
			return nil, errors.New("选中的洛雪音源都没给出能下得动的直链")
		},
		func(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error) {
			singleCalled = true
			return "https://cdn.example/fallback.flac", 60_000_000, nil
		},
		nil)
	rememberRotationItem(i, "wy", "45")

	res, err := i.pool.Resolve(context.Background(), "lx:wy", "45")
	if err != nil {
		t.Fatalf("轮询失败应退回单条，而不是整体失败：%v", err)
	}
	if !singleCalled {
		t.Fatalf("轮询失败后应调用单条 LxResolve")
	}
	if res.URL != "https://cdn.example/fallback.flac" {
		t.Fatalf("应返回单条模式的链，得到 %q", res.URL)
	}
}

func TestLxBestOfRotationSkipsUnprovenWhenProvenExists(t *testing.T) {
	i := lxRotationHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
			return []lxnode.Candidate{
				// 探测没结论（超时）：不是死链，但也没证明能下
				{URL: "https://cdn.example/slow.flac", Script: "全豆要.js", Unproven: "timeout 7s"},
				// 有实测结果的 320k
				{URL: "https://cdn.example/ok.mp3", Script: "念心音源.js", Status: 206, Bytes: 11_200_000},
			}, nil
		},
		nil, nil)
	rememberRotationItem(i, "wy", "46")

	res, err := i.pool.Resolve(context.Background(), "lx:wy", "46")
	if err != nil {
		t.Fatalf("轮询取链应成功：%v", err)
	}
	// 有实测证据的那条优先，哪怕另一条「可能」是无损
	if res.URL != "https://cdn.example/ok.mp3" {
		t.Fatalf("应优先用有实测结果的那条，得到 %q", res.URL)
	}
}
