package backenddl

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/online"
)

// 「平台没有你要的那一档」时怎么办 —— 这一组测试盯的是**降级兜底**的边界。
//
// 2026-10-30 重构后的语义：全局只有一个「下载音质」三选一模式
//（跟随页面 / 仅下载最高 / 自动降档），不再有第二开关、不再有逐条意愿：
//   - follow / auto_lower：缺档就降级用平台实际有的那档；
//   - top_only：只问点名的最高档，缺就失败，不降。
//
// 背景：ResolveLadder 只问「用户点名的那一档 + 预设阶梯里更低的档」。
// 而平台**实际**能给的档可能根本不在被问的集合里 —— 最典型的是 QQ：
// 按档点名去问（ResolveAtTier）时匿名账号经常全档「没有这一档」，但让平台
// 自己挑（Resolve）就能拿到 320k。这正是用户看到「没有对应的音质档位」
// 却没降档的根因。

// fakeTierLess 对**任何**档位都答「没有这一档」，但裸 Resolve 时会给一档
// 具体的（模拟免登录接口不按档位走，只给固定的那一档）。
//
// 它同时把两条路径的结果**记在返回值上**，因为 Pool 有缓存：ResolveTier 把
// ErrTierUnavailable 负缓存进 `platform\x00id\x00tier`，裸 Resolve 用的是
// `platform\x00id` —— 两条键不同，缓存不会互相污染，但测试要能看清各自
// 被调了几次。
type fakeTierLess struct {
	actual   string // 裸 Resolve 时给的档位
	atTier   *int   // ResolveAtTier 被调次数（可 nil）
	naked    *int   // 裸 Resolve 被调次数（可 nil）
}

func (f fakeTierLess) Platform() string { return "wy" }
func (f fakeTierLess) Resolve(ctx context.Context, id string) (*online.Resolved, error) {
	if f.naked != nil {
		*f.naked++
	}
	return &online.Resolved{URL: "https://native/" + f.actual, Tier: f.actual, Format: "mp3", Bitrate: 320000}, nil
}
func (f fakeTierLess) ResolveBatch(ctx context.Context, ids []string) (map[string]*online.Resolved, error) {
	return nil, nil
}
func (f fakeTierLess) ResolveAtTier(ctx context.Context, id, tier string) (*online.Resolved, error) {
	if f.atTier != nil {
		*f.atTier++
	}
	return nil, online.ErrTierUnavailable
}

func fallbackCfg(mode string) FetcherConfig {
	return FetcherConfig{
		Pool:        online.NewPool(fakeTierLess{actual: online.Tier320k}),
		Channels:    func() []string { return []string{"native"} },
		QualityMode: func() string { return mode },
	}
}

// 点名无损、平台只有 320k、模式允许降 → 拿 320k 把它下下来，而不是失败。
func TestFallbackLowerAcceptsLowerTierWhenAllowed(t *testing.T) {
	for _, mode := range []string{config.DownloadQualityFollow, config.DownloadQualityAutoLower} {
		cfg := fallbackCfg(mode)
		it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "flac"}
		list, err := planAttempts(context.Background(), cfg, it)
		if err != nil {
			t.Fatalf("模式 %q 允许降级时不该失败：%v", mode, err)
		}
		if len(list) == 0 {
			t.Fatalf("模式 %q：没有候选", mode)
		}
		if list[0].tier != online.Tier320k {
			t.Fatalf("模式 %q：应降级到平台实际有的 320k，得到 %+v", mode, list[0])
		}
		if !strings.Contains(list[0].label, "降级") {
			t.Fatalf("模式 %q：降级来源要在 label 里标出来（排障要看）：%+v", mode, list[0])
		}
	}
}

// 「仅下载最高」→ 不降级，照旧报缺档。
func TestFallbackLowerRefusesWhenTopOnly(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityTopOnly)
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "flac"}
	_, err := planAttempts(context.Background(), cfg, it)
	if err == nil {
		t.Fatal("仅下载最高时不该降级，也不该成功")
	}
	if !strings.Contains(err.Error(), "该平台没有这一档音质") {
		t.Fatalf("失败原因该是缺档，得到：%v", err)
	}
}

// 自动档（highest）阶梯全灭时也要兜底 —— 2026-10-30 用户实打实踩到的坑：
// quality=highest 的两条任务报「没有对应的音质档位」，因为旧实现把自动档
// 挡在降级之外。自动档的语义是「要不到就退」，不是「要不到就失败」。
func TestFallbackLowerCoversAutoQuality(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityFollow)
	it := Item{Name: "晨钟暮鼓", Singer: "一鱼", Platform: "wy", PlatformID: "456", Duration: 200, Quality: "highest"}
	list, err := planAttempts(context.Background(), cfg, it)
	if err != nil {
		t.Fatalf("自动档阶梯全灭时该走降级兜底：%v", err)
	}
	if len(list) == 0 || list[0].tier != online.Tier320k {
		t.Fatalf("应拿到平台实际有的 320k，得到 %+v", list)
	}
}

// 裸 Resolve 真的被问过了 —— 这是上面几条的行为断言之外的**机制**断言。
//
// 为什么单独盯这一条：Pool 对 ErrTierUnavailable 做了负缓存，键是
// `platform\x00id\x00tier`；而兜底用的是裸 Resolve，键是 `platform\x00id`。
// 两个键只要哪天被写成同一个，兜底就会直接吃到阶梯留下的负缓存、一次网络
// 请求都不发 —— 现象正是用户 2026-10-30 看到的「没有对应的音质档位」。
func TestFallbackLowerActuallyAsksPlatform(t *testing.T) {
	var atTier, naked int
	cfg := FetcherConfig{
		Pool:        online.NewPool(fakeTierLess{actual: online.Tier320k, atTier: &atTier, naked: &naked}),
		Channels:    func() []string { return []string{"native"} },
		QualityMode: func() string { return config.DownloadQualityFollow },
	}
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "ask-platform", Duration: 269, Quality: "flac"}
	if _, err := planAttempts(context.Background(), cfg, it); err != nil {
		t.Fatalf("应当降级成功：%v", err)
	}
	if atTier == 0 {
		t.Fatal("阶梯一次都没问过平台（ResolveLadder 没跑）")
	}
	if naked != 1 {
		t.Fatalf("兜底应当恰好问一次裸 Resolve，实际 %d 次", naked)
	}
}

// 没接模式开关（QualityMode == nil，比如老部署）→ 默认允许降级，
// 与「跟随页面」一致 —— 用户的原始诉求就是「平台没有这一档，也可以下载平台有的」。
func TestFallbackLowerDefaultsToAllowed(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityFollow)
	cfg.QualityMode = nil
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "flac"}
	list, err := planAttempts(context.Background(), cfg, it)
	if err != nil {
		t.Fatalf("没接开关也该按默认允许降级：%v", err)
	}
	if len(list) == 0 || list[0].tier != online.Tier320k {
		t.Fatalf("应降级到 320k，得到 %+v", list)
	}
}

// 绝不升档：用户点名 128k（省空间），平台「实际有的」是 320k ——
// 那是比用户要的更好，但也不是他要的。降级只往低走。
func TestFallbackLowerNeverUpsgrades(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityAutoLower)
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "128k"}
	if _, err := planAttempts(context.Background(), cfg, it); err == nil {
		t.Fatal("点名 128k 时不该收下平台的 320k")
	}
}

// 「仅下载最高」把阶梯砍到只剩一档：点名 128k 时阶梯里不能再有 128k 以外的档。
// （砍阶梯发生在 planAttempts，不在 fallbackLower —— 这里从行为上验证。）
func TestTopOnlyTrimsLadder(t *testing.T) {
	// fakeAllTiers：每一档都给得出直链 —— 阶梯砍没砍，看它被问了几次。
	fake := &fakeAllTiers{}
	cfg := FetcherConfig{
		Pool:        online.NewPool(fake),
		Channels:    func() []string { return []string{"native"} },
		QualityMode: func() string { return config.DownloadQualityTopOnly },
	}
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "highest"}
	list, err := planAttempts(context.Background(), cfg, it)
	if err != nil {
		t.Fatalf("仅下载最高不该失败（最高档有货）：%v", err)
	}
	if len(fake.asked) != 1 {
		t.Fatalf("仅下载最高只该问一档（砍掉阶梯），实际问了 %d 档：%v", len(fake.asked), fake.asked)
	}
	if len(list) != 1 || list[0].tier != online.TierLossless {
		t.Fatalf("应只拿到最高档 flac，得到 %+v", list)
	}
}

// fakeAllTiers 每一档都给得出直链，并记录被问过的档位。
type fakeAllTiers struct {
	asked []string
}

func (f *fakeAllTiers) Platform() string { return "wy" }
func (f *fakeAllTiers) Resolve(ctx context.Context, id string) (*online.Resolved, error) {
	return &online.Resolved{URL: "https://native/resolve", Tier: online.Tier320k, Format: "mp3", Bitrate: 320000}, nil
}
func (f *fakeAllTiers) ResolveBatch(ctx context.Context, ids []string) (map[string]*online.Resolved, error) {
	return nil, nil
}
func (f *fakeAllTiers) ResolveAtTier(ctx context.Context, id, tier string) (*online.Resolved, error) {
	f.asked = append(f.asked, tier)
	return &online.Resolved{URL: "https://native/" + tier, Tier: tier, Format: "mp3", Bitrate: 320000}, nil
}

// ── 坏流降档（corruptFallback）──────────────────────────────────────────
//
// 2026-10-30 端到端实测发现的第二类失败：解析成功、文件也下回来了，但逐帧
// CRC 校验判它是坏 FLAC（同一首的无损直链本身就是坏的）。这种失败换条无损
// 直链没意义，换档才有救 —— 320K 是另一条编码链路。

// isCorruptFLAC 判定：downloadFailure 带 error_code=flac_corrupt 要认得出，
// 老格式（纯文本含「坏流」）也要认得出，普通错误不能误判。
func TestIsCorruptFLACClassifies(t *testing.T) {
	df := &downloadFailure{msg: "下载失败：坏流", errCode: "flac_corrupt"}
	if !isCorruptFLAC(df) {
		t.Fatal("error_code=flac_corrupt 必须判定为坏流")
	}
	if isCorruptFLAC(&downloadFailure{msg: "下载失败：403", errCode: ""}) {
		t.Fatal("普通下载失败不能判成坏流")
	}
	if isCorruptFLAC(context.Canceled) {
		t.Fatal("无关错误不能判成坏流")
	}
}

// corruptFallback 生成的重试尝试：档位必须 ≤320k、URL 换成重解析结果、
// label 标出「坏流降320k」。有损档源不再降（无更低目标）。
func TestCorruptFallbackBuildsRetryAttempt(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityAutoLower)
	it := Item{Name: "晨钟暮鼓", Singer: "一鱼", Platform: "wy", PlatformID: "789", Duration: 200, Quality: "flac"}
	src := attempt{channel: config.DLChannelMusicDL, tier: "flac", url: "https://bad/flac", label: "dl/某源"}
	fb := cfg.corruptFallback(context.Background(), src, it)
	if fb == nil {
		t.Fatal("无损档坏流 + 允许降档 → 应生成 320k 重试尝试")
	}
	if fb.tier != online.Tier320k {
		t.Fatalf("重试档位应为 320k，得到 %q", fb.tier)
	}
	if fb.url == "https://bad/flac" {
		t.Fatal("重试 URL 必须换成重解析结果")
	}
	if !strings.Contains(fb.label, "坏流降320k") {
		t.Fatalf("label 要标出坏流降档（排障）：%q", fb.label)
	}
	if fb.channel != config.DLChannelNative {
		t.Fatalf("重试走原生解析器，channel 应为 native，得到 %q", fb.channel)
	}
	// 有损档：无更低目标，不生成重试
	lossy := attempt{channel: config.DLChannelMusicDL, tier: "320k", url: "https://x/320", label: "dl/某源"}
	if fb2 := cfg.corruptFallback(context.Background(), lossy, it); fb2 != nil {
		t.Fatalf("320k 坏流不该再降（无更低目标），得到 %+v", fb2)
	}
}

// top_only 模式下主循环不触发坏流降档（语义：给不了最高就失败）。
// （corruptFallback 本身不查模式 —— 主循环的 cfg.modeAllowsLower() 守卫负责；
//  这里测 corruptFallback 纯函数行为，模式语义在 TestFallbackLowerRefusesWhenTopOnly。）
func TestCorruptFallbackNeedsNativeResolver(t *testing.T) {
	cfg := fallbackCfg(config.DownloadQualityAutoLower)
	// 洛雪伪源平台（lx: 前缀）没有原生重解析入口
	it := Item{Name: "x", Singer: "y", Platform: "lx:foo", PlatformID: "1", Quality: "flac"}
	src := attempt{channel: config.DLChannelLx, tier: "flac", url: "https://bad", label: "lx"}
	if fb := cfg.corruptFallback(context.Background(), src, it); fb != nil {
		t.Fatalf("洛雪伪源无原生解析入口，不该生成重试，得到 %+v", fb)
	}
}
