package intercept

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
)

// ── 洛雪「取链那一半」（见 lxresolve.go）────────────────────────────────
//
// 这一组钉的是**接上了没有**：在这之前池里没有 `lx:<src>` 的解析器，
// 洛雪候选能进池却取不到直链 —— 表现是「下载永远回落曲目自己平台」，
// 而日志里只有一句「换下一个候选」，看不出是这里断的。

// lxHarness 造一个只带洛雪那套东西的拦截层。
func lxHarness(t *testing.T, resolve func(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error),
	search func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error)) *Interceptor {
	t.Helper()
	i := New(Config{
		DataDir:   t.TempDir(),
		Upstream:  nil, // 下面手动补一个非 nil 的假上游
		Logf:      func(string, ...any) {},
		LxSearch:  search,
		LxResolve: resolve,
	})
	// New 在 Upstream==nil 时返回 nil；这里换一个最小的替身让构造继续。
	if i == nil {
		i = New(Config{
			DataDir:   t.TempDir(),
			Upstream:  &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
			Logf:      func(string, ...any) {},
			LxSearch:  search,
			LxResolve: resolve,
		})
	}
	return i
}

func TestLxCandidatesRegistersResolverAndKeepsRaw(t *testing.T) {
	var gotInfo map[string]any
	var gotTier string
	i := lxHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error) {
			gotInfo, gotTier = info, quality
			return "https://cdn.example/x.flac", 0, nil
		},
		func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error) {
			return []lxnode.Song{{
				Script: "s.js", Source: "qsvip", ID: "42", Name: "晴天", Singer: "周杰伦",
				Quality: "flac24bit",
				Raw: map[string]any{
					"songmid": "42", "name": "晴天", "singer": "周杰伦",
					// 脚本自己声明的档位表：取链时要原样喂回给脚本
					"_types": map[string]any{"128k": map[string]any{}, "320k": map[string]any{}, "flac24bit": map[string]any{}},
				},
			}}, nil
		})

	cands := i.lxCandidates(context.Background(), "晴天 周杰伦")
	if len(cands) != 1 {
		t.Fatalf("应拿到 1 条候选，得到 %d", len(cands))
	}
	if cands[0].Source != "lx:qsvip" {
		t.Fatalf("候选的源应带 lx: 前缀，得到 %q", cands[0].Source)
	}
	// 池里必须**真的注册了**这个源的解析器 —— 这是这一轮补的那一半
	if !i.pool.Supports("lx:qsvip") {
		t.Fatalf("池里应注册 lx:qsvip 的解析器（没有它 = 候选取不到直链）")
	}

	res, err := i.pool.Resolve(context.Background(), "lx:qsvip", "42")
	if err != nil {
		t.Fatalf("应解析成功，得到 %v", err)
	}
	if res.URL == "" {
		t.Fatalf("应拿到直链")
	}
	// 档位要按**脚本自己声明的最高档**去要（flac24bit 而不是默认 320k）
	if gotTier != "flac24bit" {
		t.Fatalf("应向脚本要它声明的最高档 flac24bit，得到 %q", gotTier)
	}
	// 搜索时那条记录要原样喂回去（脚本的 musicUrl 读它）
	if gotInfo == nil || gotInfo["songmid"] != "42" {
		t.Fatalf("应把搜索时的记录喂回脚本，得到 %v", gotInfo)
	}
	if _, ok := gotInfo["_types"]; !ok {
		t.Fatalf("记录里的 _types 必须一起喂回去（脚本靠它选直链）")
	}
	if res.Tier != "flac24bit" {
		t.Fatalf("解析结果的档位应是脚本声明的 flac24bit，得到 %q", res.Tier)
	}
}

func TestLxResolverWithoutRawFallsBackToMinimalInfo(t *testing.T) {
	var gotInfo map[string]any
	var gotQuality string
	i := lxHarness(t,
		func(ctx context.Context, source string, info map[string]any, quality string) (string, int64, error) {
			gotInfo = info
			gotQuality = quality
			return "https://cdn.example/x.mp3", 0, nil
		},
		func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error) { return nil, nil })

	// 直接解析一个**没搜过**的 id（进程重启过、或候选从别处进来）
	r := lxResolver{i: i, src: "qsvip"}
	res, err := r.Resolve(context.Background(), "999")
	if err != nil {
		t.Fatalf("应能用最小 info 兜底解析，得到 %v", err)
	}
	if gotInfo["songmid"] != "999" {
		t.Fatalf("最小 info 里应带 songmid，得到 %v", gotInfo)
	}
	// 没有 _types（也就没有任何档位声明）→ 直接问无损：脚本会按它声明的
	// qualitys 自己往下压（mapQuality clamp），所以「问高一点」是安全的。
	if gotQuality != online.TierLossless {
		t.Fatalf("没有档位声明时该直接问无损，实际问了 %q", gotQuality)
	}
	// 但**结果**必须诚实：脚本回的是 .mp3 直链 → 档位当场降成 320k，
	// 不能把「问了 flac」当成「拿到了 flac」（这是最难查的一类偏差）。
	if res.Tier != online.Tier320k {
		t.Fatalf("直链是 .mp3 时档位应如实降为 320k，得到 %q", res.Tier)
	}
}

// 宿主没开（lxnode.ErrNotRunning）时：**不报错、返回 nil**，池里少一条来源而已。
func TestLxCandidatesSilentWhenHostDown(t *testing.T) {
	i := lxHarness(t,
		func(context.Context, string, map[string]any, string) (string, int64, error) { return "", 0, nil },
		func(context.Context, string, int) ([]lxnode.Song, error) {
			return nil, errors.New("宿主没开")
		})
	if got := i.lxCandidates(context.Background(), "晴天"); got != nil {
		t.Fatalf("宿主不可用时应返回 nil，得到 %v", got)
	}
}

// 认不出的档位键要被忽略（不编档位），而 `hires` / `flac24bit` 都要认。
func TestLxBestTier(t *testing.T) {
	cases := []struct {
		info map[string]any
		want string
	}{
		{map[string]any{"_types": map[string]any{"128k": 1, "flac": 1}}, "flac"},
		{map[string]any{"_types": map[string]any{"320k": 1, "flac24bit": 1}}, "flac24bit"},
		{map[string]any{"types": map[string]any{"128k": 1, "320k": 1}}, "320k"},
		{map[string]any{"_types": map[string]any{"aac48": 1}}, ""},
		{map[string]any{}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := lxBestTier(c.info); got != c.want {
			t.Fatalf("lxBestTier(%v)：期望 %q，得到 %q", c.info, c.want, got)
		}
	}
}

// 档位与容器的对应（洛雪直链常常没有扩展名，留空会让边听边存的文件没有扩展名）。
func TestLxFormatFromTier(t *testing.T) {
	for tier, want := range map[string]string{
		"flac": "flac", "flac24bit": "flac24bit", "320k": "mp3", "128k": "mp3", "": "mp3",
	} {
		if got := lxFormatFromTier(tier); got != want {
			t.Fatalf("档位 %q：期望 %q，得到 %q", tier, want, got)
		}
	}
}

var _ = online.ErrNoResolver

// 档位按实测字节数校正：第三方站「点无损、给 128k」时不许把它记成无损。
func TestLxTierFromSize(t *testing.T) {
	cases := []struct {
		name    string
		size    int64
		dur     int
		claimed string
		want    string
	}{
		// 真机实测形状：Beyond《不再犹豫》点 flac，脚本回话 flac，实测 4,186,741 字节 / 295 秒 ≈ 113k
		{"真机那条伪无损", 4186741, 295, online.TierLossless, online.Tier128k},
		// 4MB 的 mp3 声明成 320k 时同样要如实降下来（只降不升，不看声明面子）
		{"声明320k实际128k", 4186741, 295, online.Tier320k, online.Tier128k},
		// 11.8MB / 295s ≈ 320k：CBR 320 正常区间（阈值 260 起算），不该误降
		{"CBR320k不误降", 11800000, 295, online.TierLossless, online.Tier320k},
		// 59MB / 275s ≈ 1737k：这就是真无损（v2.16 修 FLAC 校验器时用的那个文件）
		{"真无损保持", 59811934, 275, online.TierLossless, online.TierLossless},
		// 只降不升：字节数说不清 flac 与 flac24bit 的差别，别拿它把 320k 吹成无损
		{"小文件不吹高", 59811934, 275, online.Tier320k, online.Tier320k},
		{"没长度不判", 0, 295, online.TierLossless, online.TierLossless},
		{"没时长不判", 4186741, 0, online.TierLossless, online.TierLossless},
		{"空声明不动", 4186741, 295, "", ""},
	}
	for _, c := range cases {
		got, why := lxTierFromSize(c.size, c.dur, c.claimed)
		if got != c.want {
			t.Errorf("%s: lxTierFromSize(%d, %d, %q) = %q（%s），期望 %q", c.name, c.size, c.dur, c.claimed, got, why, c.want)
		}
		if got != c.claimed && why == "" {
			t.Errorf("%s: 降档了却没给出依据说明，排障时这句话是唯一的线索", c.name)
		}
	}
}

// 整条链上的证据衔接：URL 没有后缀（上面那道 lxURLLooksLossy 拦不住）时，
// 靠宿主回报的字节数 + 记录里的时长把档位压回真实值。
func TestLxResolverDowngradesByMeasuredSize(t *testing.T) {
	i := lxHarness(t,
		func(context.Context, string, map[string]any, string) (string, int64, error) {
			// 无扩展名的直链：形态上看不出色，只有「多大」能说话
			return "https://c.example/stream?sign=abc", 4186741, nil
		},
		func(context.Context, string, int) ([]lxnode.Song, error) { return nil, nil })

	i.rememberLxItem("wy", "2063748306", map[string]any{
		"songmid": "2063748306", "id": "2063748306",
		"name": "不再犹豫", "singer": "Beyond", "duration": 295,
	})
	r := lxResolver{i: i, src: "wy"}
	res, err := r.Resolve(context.Background(), "2063748306")
	if err != nil {
		t.Fatalf("解析该成功：%v", err)
	}
	if res.Tier != online.Tier128k {
		t.Fatalf("4.1MB/295s ≈ 113k，档位该如实记 128k，得到 %q", res.Tier)
	}
	// 容器也跟着档位走：记 flac 而盘上是 mp3，就是用户骂的那个「说好的无损」
	if res.Format != "mp3" {
		t.Fatalf("降档后 Format 该跟着变 mp3，得到 %q", res.Format)
	}
}
