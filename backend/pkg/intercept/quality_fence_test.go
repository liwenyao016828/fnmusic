package intercept

import (
	"context"
	"testing"
	"time"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/search"
)

// TestQualityProbeFenceMarksTimedOut 单首围栏到点要**收手**，
// 并且把这条记成「没探出」（TimedOut=true）而不是「探出没有」。
//
// 为什么钉这条：实测最坏一首要 80s，4 个 worker 里卡住一个就把整批的墙钟拖死；
// 而「超时」如果被当成「没有更高音质」，界面就会给用户一个**结论性**的错答案，
// 而且这条还会进 7 天免扫名单，之后再也不会被重探。
func TestQualityProbeFenceMarksTimedOut(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	// 把围栏缩到毫秒级（本来的 25s 是给真机用的）。
	prev := qualityProbeBudget
	qualityProbeBudget = 30 * time.Millisecond
	t.Cleanup(func() { qualityProbeBudget = prev })

	// 搜索替身：一直卡到 ctx 被掐断 —— 模拟「外网不响」。
	h.it.searcherCtx = func(ctx context.Context, _ string, _ string, _, _ int) []search.UnifiedSong {
		<-ctx.Done()
		return nil
	}

	path := writeQualityFile(t, root, "慢歌.mp3")
	entry := h.it.probeQuality(context.Background(), path)

	if !entry.TimedOut {
		t.Fatalf("围栏到点该记成超时，得到 %+v", entry)
	}
	if entry.Best != nil {
		t.Fatalf("超时不是结论，不该有赢家：%+v", entry.Best)
	}
	if entry.Upgradeable {
		t.Fatal("超时不是「探出能换」，不该标成可替换")
	}
}

// TestQualityProbeFenceDoesNotBlockNextScan 超时的那首**不能**进免扫名单：
// 下次扫描还得再探它一次（网络恢复后要能探出结果）。
func TestQualityProbeFenceDoesNotBlockNextScan(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	path := writeQualityFile(t, root, "慢歌.mp3")
	tier := qualityCurrentTierOfFile(path, ".mp3")

	h.it.qualityStore.put(QualityEntry{
		Path: path, Ext: ".mp3", CurrentTier: tier,
		ScannedAt: time.Now().Unix(), TimedOut: true,
	})

	if h.it.qualityEntryFresh(QualityEntry{
		Path: path, Ext: ".mp3", CurrentTier: tier,
		ScannedAt: time.Now().Unix(), TimedOut: true,
	}, time.Now()) {
		t.Fatal("超时的结论不该能免掉重扫")
	}
}

// TestQualityProbeKeepsGoodEntryWhenLaterProbeTimesOut 已经有一条有效结论时，
// 后来的超时**不能**把它抹掉（抹掉＝界面上的「能换」凭空消失）。
func TestQualityProbeKeepsGoodEntryWhenLaterProbeTimesOut(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	path := writeQualityFile(t, root, "歌.mp3")
	tier := qualityCurrentTierOfFile(path, ".mp3")

	good := QualityEntry{Path: path, Ext: ".mp3", CurrentTier: tier, ScannedAt: time.Now().Unix()}
	h.it.qualityStore.put(good)
	h.it.qualityStore.put(QualityEntry{
		Path: path, Ext: ".mp3", CurrentTier: tier,
		ScannedAt: time.Now().Unix(), TimedOut: true,
	})

	got, ok := h.it.qualityStoreLookup(path)
	if !ok {
		t.Fatal("这条不该被抹掉")
	}
	if got.TimedOut {
		t.Fatalf("有效的结论不该被超时覆盖：%+v", got)
	}
}

// TestNativeTrackForOnlyAsksNativePlatforms 「原生渠道」这一段只问曲率自己会解析的
// 平台（网易/QQ），**不**把用户启用的外挂源也算进来。
//
// 为什么钉这条：platforms() 是「官方页面搜索要合并哪些平台」，含全部 musicdl 源
// （真机 dev 上 20 个）。v2.34.1 之前 nativeTrackFor 用的就是它 —— 于是「原生搜索」
// 这一段实际在串行搜 20 个外挂源，吃满 25s 围栏，而直连网易只要 0.3s。
func TestNativeTrackForOnlyAsksNativePlatforms(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.PlatformsFunc = func() []string {
		return []string{"wy", "mg", "apple", "tx", "lizhi", "bi"}
	}
	var asked []string
	h.it.searcherCtx = func(_ context.Context, _ string, platform string, _, _ int) []search.UnifiedSong {
		asked = append(asked, platform)
		return nil
	}
	h.it.nativeTrackFor(context.Background(), "晴天", "周杰伦")

	for _, p := range asked {
		switch p {
		case "wy", "tx", "kg", "kw":
		default:
			t.Fatalf("原生渠道不该去问外挂源 %q（问了：%v）", p, asked)
		}
	}
	if len(asked) != 2 || asked[0] != "wy" || asked[1] != "tx" {
		t.Fatalf("该按顺序问 wy/tx 两个原生平台，得到 %v", asked)
	}
}

// TestNativePlatformsFallsBackWhenAllExternal 用户把平台列表配成「全是外挂源」时，
// 原生渠道仍要有平台可问（否则这一列永远空着，等于功能没了）。
func TestNativePlatformsFallsBackWhenAllExternal(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.PlatformsFunc = func() []string { return []string{"mg", "apple"} }
	got := h.it.nativePlatforms()
	if len(got) == 0 {
		t.Fatal("全外挂时也该回落到内置原生平台")
	}
	for _, p := range got {
		switch p {
		case "wy", "tx", "kg", "kw":
		default:
			t.Fatalf("回落名单里混进了外挂源 %q", p)
		}
	}
}

// TestQualityMatchTitleStripsTrackNumber 文件名开头的曲序要去掉再比，
// 但**以数字开头的歌名**不能被切。
//
// 为什么钉这条：曲库文件普遍带 `01 夜曲` 这种前缀，而平台曲名是 `夜曲` ——
// 不去前缀，原生渠道那一列对这类文件永远是空的（v2.34.1 真机就是这个现象）。
func TestQualityMatchTitleStripsTrackNumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"01 夜曲", "夜曲"},
		{"03 - 歌名", "歌名"},
		{"12.晴天", "晴天"},
		{"07_某歌", "某歌"},
		{"01、某歌", "某歌"},
		{"10000 Reasons", "10000 Reasons"}, // 5 位数字：不是曲序
		{"7 Rings", "Rings"},               // 4 位以内 + 空格：按曲序切（但另一路键仍能匹配）
		{"夜曲", "夜曲"},
		{"01", "01"}, // 切完没剩东西 → 原样
	}
	for _, c := range cases {
		if got := qualityMatchTitle(c.in); got != c.want {
			t.Fatalf("qualityMatchTitle(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

// TestNativeTrackForMatchesTitleWithoutTrackNumber 带曲序的文件名要能匹配到
// 平台上的同名曲目（用去曲序的标题搜、两个键都收）。
func TestNativeTrackForMatchesTitleWithoutTrackNumber(t *testing.T) {
	h := newHarness(t)
	var keywords []string
	h.it.searcherCtx = func(_ context.Context, keyword, platform string, _, _ int) []search.UnifiedSong {
		keywords = append(keywords, keyword)
		return []search.UnifiedSong{{
			Songmid: "186016", Name: "夜曲", Singer: "周杰伦", Album: "十一月的萧邦",
			Interval: 226, Source: platform,
		}}
	}
	got := h.it.nativeTrackFor(context.Background(), "01 夜曲", "周杰伦")
	if got.PlatformID != "186016" {
		t.Fatalf("带曲序的文件名该匹配上（搜的关键词是 %v），得到 %+v", keywords, got)
	}
}

// TestLxBudgetCapsLxLeg 探测腿给洛雪那一路划的 6s 上限要真的生效
// （洛雪按脚本逐个问，6 个脚本各问一遍实测能到 16s）。
func TestLxBudgetCapsLxLeg(t *testing.T) {
	h := newHarness(t)
	var sawDeadline bool
	h.it.lxSearch = func(ctx context.Context, _ string, _ int) ([]lxnode.Song, error) {
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= 6*time.Second+time.Second {
			sawDeadline = true
		}
		return nil, nil
	}
	ctx := withLxBudget(context.Background(), 6*time.Second)
	h.it.lxCandidates(ctx, "晴天")
	if !sawDeadline {
		t.Fatal("洛雪那一路没拿到 6s 的期限")
	}

	// 下载腿：ctx 里没有预算 → 不许自己造期限（要等真直链）。
	sawDeadline = false
	h.it.lxCandidates(context.Background(), "晴天")
	if sawDeadline {
		t.Fatal("下载腿不该被探测预算限制")
	}
}
