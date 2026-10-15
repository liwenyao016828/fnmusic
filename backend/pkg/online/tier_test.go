package online

import (
	"context"
	"errors"
	"testing"
)

// ── 试听阶梯（见 tier.go） ────────────────────────────────────────────────
//
// 这一组用例钉的是「降档语义」——它是这一层唯一容易写错、且错了**不会报错**的地方：
// 该降档时没降（一首只有 128k 的歌报「播不了」），或不该降时降了（白打两次请求、
// 拿到同一份东西），两种都只有人肉听歌才能发现。

// fakeTierResolver 是按档位解析的替身。
type fakeTierResolver struct {
	platform string
	// answers 是「档位 → 结果」；没有条目 = 这一档不可用（ErrTierUnavailable）。
	answers map[string]*Resolved
	// fail 覆盖上面的默认行为，用来精确构造「曲目级不可播」这类结论。
	fail map[string]error
	// calls 记**总调用次数**，用来钉住「不该降档时没有多打请求」。
	calls int
	// asked 记下按顺序问过哪些档位。
	asked []string
}

func (f *fakeTierResolver) Platform() string { return f.platform }

func (f *fakeTierResolver) Resolve(ctx context.Context, id string) (*Resolved, error) {
	return f.ResolveAtTier(ctx, id, "")
}

func (f *fakeTierResolver) ResolveBatch(ctx context.Context, ids []string) (map[string]*Resolved, error) {
	out := map[string]*Resolved{}
	for _, id := range ids {
		if r, err := f.Resolve(ctx, id); err == nil {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeTierResolver) ResolveAtTier(ctx context.Context, id string, tier string) (*Resolved, error) {
	f.calls++
	f.asked = append(f.asked, tier)
	if err, ok := f.fail[id+"\x00"+tier]; ok {
		return nil, err
	}
	if r, ok := f.answers[id+"\x00"+tier]; ok {
		return r, nil
	}
	return nil, ErrTierUnavailable
}

func TestTierRankOrdering(t *testing.T) {
	// 无损的各种封装都算同一档 —— 阶梯里不为它们各开一格。
	for _, lossless := range []string{TierLossless, "flac24bit", "hires", "ape", "wav", "dsd"} {
		if got := TierRank(lossless); got != TierRank(TierLossless) {
			t.Fatalf("%s 应算无损档，得到 rank=%d", lossless, got)
		}
	}
	if !(TierRank(TierLossless) > TierRank(Tier320k) &&
		TierRank(Tier320k) > TierRank(Tier192k) &&
		TierRank(Tier192k) > TierRank(Tier128k)) {
		t.Fatalf("阶梯必须严格递减：flac > 320k > 192k > 128k")
	}
	if TierRank("aac48") != -1 || TierRank("") != -1 {
		t.Fatalf("阶梯外的档位应返回 -1")
	}
	// 大小写与空白都要认（配置是手写的，音源回的档位也不统一）
	if TierRank(" FLAC ") != TierRank(TierLossless) {
		t.Fatalf("档位比较应先归一化大小写与空白")
	}
}

func TestTierFromResolvedInfersTier(t *testing.T) {
	cases := []struct {
		name string
		res  *Resolved
		want string
	}{
		{"平台回读的档位最权威", &Resolved{Tier: TierLossless, Bitrate: 128000}, TierLossless},
		{"按码率推无损", &Resolved{Bitrate: 900000}, TierLossless},
		{"按码率推 320k", &Resolved{Bitrate: 320000}, Tier320k},
		{"按码率推 192k", &Resolved{Bitrate: 192000}, Tier192k},
		{"按码率推 128k", &Resolved{Bitrate: 128000}, Tier128k},
		{"码率低于阶梯下限不产生档位", &Resolved{Bitrate: 48000}, ""},
		{"按容器推无损", &Resolved{Format: "flac"}, TierLossless},
		// mp3 不等于 320k：码率没给就别猜，猜出来的是假信息。
		{"mp3 无码率时不猜档位", &Resolved{Format: "mp3"}, ""},
		{"空结果", nil, ""},
	}
	for _, c := range cases {
		if got := tierFromResolved(c.res); got != c.want {
			t.Fatalf("%s：期望 %q，得到 %q", c.name, c.want, got)
		}
	}
}

// 平台明确说「没有这一档」时，必须降下一档继续试。
func TestResolveLadderStepsDownOnTierUnavailable(t *testing.T) {
	r := &fakeTierResolver{
		platform: "wy",
		answers: map[string]*Resolved{
			"1\x00" + Tier320k: {URL: "http://cdn/x.mp3", Tier: Tier320k, Format: "mp3", Bitrate: 320000},
		},
	}
	p := NewPool(r)

	res, err := p.ResolveLadder(context.Background(), "wy", "1", nil)
	if err != nil {
		t.Fatalf("应降档成功，得到错误 %v", err)
	}
	if res.Tier != Tier320k {
		t.Fatalf("实际档位应是 320k，得到 %q", res.Tier)
	}
	if len(r.asked) != 2 || r.asked[0] != TierLossless || r.asked[1] != Tier320k {
		t.Fatalf("应严格按阶梯顺序问 flac → 320k，实际问了 %v", r.asked)
	}
}

// 曲目级不可播（收费 / 无版权）时**立刻放弃**：换低档也一样听不了，
// 继续试只是白等三次超时。
func TestResolveLadderStopsOnNotPlayable(t *testing.T) {
	r := &fakeTierResolver{
		platform: "wy",
		fail:     map[string]error{"1\x00" + TierLossless: ErrNotPlayable},
		answers: map[string]*Resolved{
			"1\x00" + Tier320k: {URL: "http://cdn/x.mp3", Tier: Tier320k},
		},
	}
	p := NewPool(r)

	_, err := p.ResolveLadder(context.Background(), "wy", "1", nil)
	if !errors.Is(err, ErrNotPlayable) {
		t.Fatalf("应原样返回 ErrNotPlayable，得到 %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("不可播时不该继续降档，实际问了 %d 次", r.calls)
	}
}

// 平台**静默降级**（要 lossless 回 exhigh）不是失败：那是一次成功的解析，
// 照实记下实际档位，**不要**再降档重试 —— 白打请求还拿到同一份东西。
func TestResolveLadderAcceptsSilentDowngrade(t *testing.T) {
	r := &fakeTierResolver{
		platform: "wy",
		answers: map[string]*Resolved{
			"1\x00" + TierLossless: {URL: "http://cdn/x.mp3", Tier: Tier320k, Format: "mp3", Bitrate: 320000},
		},
	}
	p := NewPool(r)

	res, err := p.ResolveLadder(context.Background(), "wy", "1", nil)
	if err != nil {
		t.Fatalf("静默降级应算成功，得到 %v", err)
	}
	if res.Tier != Tier320k {
		t.Fatalf("必须如实报出实际档位 320k，得到 %q", res.Tier)
	}
	if r.calls != 1 {
		t.Fatalf("静默降级只该问一次，实际问了 %d 次", r.calls)
	}
}

// 没实现 TierResolver 的平台（musicdl / 洛雪那类外挂源）退化成问一次 Resolve，
// 且档位按码率/容器补齐 —— 界面上不会出现「这首歌没写音质」。
func TestResolveLadderFallsBackForPlainResolver(t *testing.T) {
	r := &fakeResolver{
		platform: "mg",
		answers:  map[string]*Resolved{"1": {URL: "http://cdn/x.flac", Format: "flac", Bitrate: 900000}},
	}
	p := NewPool(r)

	res, err := p.ResolveLadder(context.Background(), "mg", "1", nil)
	if err != nil {
		t.Fatalf("应退化成普通解析并成功，得到 %v", err)
	}
	if res.Tier != TierLossless {
		t.Fatalf("档位应由容器/码率补齐成无损，得到 %q", res.Tier)
	}
	if r.calls != 1 {
		t.Fatalf("不该为三档各问一次，实际问了 %d 次", r.calls)
	}
}

// 「这一档没有」是**稳定结论**，要进负缓存 —— 否则每切一次歌都重新问一遍
// 明知没有的无损档（三次往返变六次）。
func TestResolveTierCachesTierUnavailable(t *testing.T) {
	r := &fakeTierResolver{platform: "wy", answers: map[string]*Resolved{}}
	p := NewPool(r)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := p.ResolveTier(ctx, "wy", "1", TierLossless); !errors.Is(err, ErrTierUnavailable) {
			t.Fatalf("第 %d 次应回 ErrTierUnavailable，得到 %v", i+1, err)
		}
	}
	if r.calls != 1 {
		t.Fatalf("稳定结论应缓存，实际问了 %d 次", r.calls)
	}
}

// 缓存键必须**含档位**：否则先问 320k 拿到结果之后，再问 flac 会直接命中
// 那条 320k 的缓存 —— 表现为「切了无损还是 320k」，且完全不报错。
func TestResolveTierCacheKeyIncludesTier(t *testing.T) {
	r := &fakeTierResolver{
		platform: "wy",
		answers: map[string]*Resolved{
			"1\x00" + Tier320k:     {URL: "http://cdn/320.mp3", Tier: Tier320k},
			"1\x00" + TierLossless: {URL: "http://cdn/x.flac", Tier: TierLossless, Format: "flac"},
		},
	}
	p := NewPool(r)
	ctx := context.Background()

	if res, _ := p.ResolveTier(ctx, "wy", "1", Tier320k); res == nil || res.Tier != Tier320k {
		t.Fatalf("先取 320k 应拿到 320k，得到 %v", res)
	}
	res, err := p.ResolveTier(ctx, "wy", "1", TierLossless)
	if err != nil {
		t.Fatalf("再取无损不该被 320k 的缓存挡住，得到 %v", err)
	}
	if res.Tier != TierLossless {
		t.Fatalf("应拿到无损，得到 %q", res.Tier)
	}
}

// 固定档位（用户在界面上点名要 128k）只问一次，不降级 —— 给他 320k 是没听话。
func TestResolveLadderFixedTierDoesNotStepDown(t *testing.T) {
	r := &fakeTierResolver{platform: "wy", answers: map[string]*Resolved{}}
	p := NewPool(r)

	_, err := p.ResolveLadder(context.Background(), "wy", "1", []string{Tier128k})
	if !errors.Is(err, ErrTierUnavailable) {
		t.Fatalf("固定档位拿不到就该失败，得到 %v", err)
	}
	if r.calls != 1 || r.asked[0] != Tier128k {
		t.Fatalf("只该问 128k 一次，实际 %v", r.asked)
	}
}

// 网易：请求的档位 ≠ 实际档位。免登录要 lossless，接口回 exhigh/320k mp3，
// **不报错、不给 null**。这条用例钉住「必须回读」。
func TestNeteaseTierOfReadsBackDowngrade(t *testing.T) {
	cases := []struct {
		level string
		br    int
		typ   string
		want  string
	}{
		{"lossless", 900000, "flac", TierLossless},
		{"hires", 1600000, "flac", TierLossless},
		// 真机实测的形状：要 lossless 回 exhigh
		{"exhigh", 320000, "mp3", Tier320k},
		{"higher", 192000, "mp3", Tier192k},
		{"standard", 128000, "mp3", Tier128k},
		// level 认不出时按码率兜（接口版本变化时不至于把档位显示成空）
		{"", 320000, "mp3", Tier320k},
		{"", 0, "flac", TierLossless},
		{"", 0, "mp3", ""},
	}
	for _, c := range cases {
		if got := neteaseTierOf(c.level, c.br, c.typ); got != c.want {
			t.Fatalf("level=%q br=%d type=%q：期望 %q，得到 %q", c.level, c.br, c.typ, c.want, got)
		}
	}
}

func TestNeteaseTierLevelMapping(t *testing.T) {
	cases := map[string]string{
		TierLossless: "lossless",
		Tier320k:     "exhigh",
		Tier192k:     "higher",
		Tier128k:     "standard",
		"aac48":      "", // 认不出的档位不装作能按档位要
	}
	for tier, want := range cases {
		if got := neteaseTierLevel(tier); got != want {
			t.Fatalf("档位 %q：期望 level=%q，得到 %q", tier, want, got)
		}
	}
}

func TestQQTierFilenameMapping(t *testing.T) {
	cases := map[string][2]string{
		TierLossless: {"F000", ".flac"},
		Tier320k:     {"M500", ".mp3"},
		// QQ 没有独立的 192k 档 —— 退到 128k 那档，不编一个不存在的档位去问。
		Tier192k: {"C400", ".m4a"},
		Tier128k: {"C400", ".m4a"},
	}
	for tier, want := range cases {
		prefix, ext, ok := qqTierFilename(tier)
		if !ok || prefix != want[0] || ext != want[1] {
			t.Fatalf("档位 %q：期望 %s/%s，得到 %s/%s (ok=%v)", tier, want[0], want[1], prefix, ext, ok)
		}
	}
	if _, _, ok := qqTierFilename("aac48"); ok {
		t.Fatalf("认不出的档位应返回 ok=false（让上层退回默认解析）")
	}
}
