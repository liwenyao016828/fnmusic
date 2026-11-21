package intercept

import (
	"context"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 这一组用例盯的是「跨源兜底挑谁」（见 playback_fallback.go 的 resolvePlaybackFallback）。
//
// 背景：候选是按**声明**音质排的序（sortByPreference → betterCandidate 看的是
// UnifiedSong.Quality），但「声明」不等于「真的能拿到这个档」—— 外挂源经常
// 标着 flac、实际只给 128k。所以兜底不能「按顺序第一个成功就返回」，而要比
// **实际拿到的档位**：宁可多问一个源，也要拿到能拿到的最高档。
//
// 「能播」仍然优先于「音质高」：所有候选都失败就是失败。

// tierResolverPool 造一个池：每个平台的每条 id 都能解析，并带上指定档位。
func tierResolverPool(answers map[string]map[string]string) *online.Pool {
	resolvers := make([]online.Resolver, 0, len(answers))
	for platform, ids := range answers {
		ans := make(map[string]*online.Resolved, len(ids))
		for id, tier := range ids {
			ans[id] = &online.Resolved{
				URL:    "https://cdn.invalid/" + platform + "-" + id + ".flac",
				Format: "flac",
				Size:   4096,
				Tier:   tier,
			}
		}
		resolvers = append(resolvers, &stubResolver{platform: platform, answers: ans})
	}
	return online.NewPool(resolvers...)
}

func TestPlaybackFallbackPrefersActualTierOverOrder(t *testing.T) {
	h := newHarness(t)
	// mg 声明 flac（所以排在候选第一位），实际只给 128k；
	// kw 声明 320k（排第二），实际给无损。
	h.it.pool = tierResolverPool(map[string]map[string]string{
		"wy": {},
		"mg": {"mg-1": "128k"},
		"kw": {"kw-1": "flac"},
	})
	h.it.cfg.DownloadSources = func() []string { return []string{"mg", "kw"} }
	h.setSongs(
		search.UnifiedSong{ID: "mg-1", Songmid: "mg-1", Name: "甲", Singer: "A", Source: "mg", Duration: 200, Quality: "flac"},
		search.UnifiedSong{ID: "kw-1", Songmid: "kw-1", Name: "甲", Singer: "A", Source: "kw", Duration: 200, Quality: "320k"},
	)

	_, used, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200))
	if err != nil {
		t.Fatalf("跨源兜底失败：%v", err)
	}
	// 声明 flac 的 mg 实际只有 128k；声明 320k 的 kw 实际给的是无损。
	// 应该选实际档位更高的 kw，而不是按顺序截胡。
	if used.Platform != "kw" {
		t.Errorf("应按实际档位选 kw（无损），得到 %s/%s", used.Platform, used.PlatformID)
	}
}

func TestPlaybackFallbackStillPlaysWhenOnlyOneCandidate(t *testing.T) {
	h := newHarness(t)
	// 只有一个候选，且它给的是 128k —— 有损也得播，不能因为「不是最高档」就报失败。
	h.it.pool = tierResolverPool(map[string]map[string]string{
		"wy": {},
		"mg": {"mg-1": "128k"},
	})
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.setSongs(search.UnifiedSong{
		ID: "mg-1", Songmid: "mg-1", Name: "甲", Singer: "A", Source: "mg", Duration: 200, Quality: "320k",
	})

	res, used, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200))
	if err != nil {
		t.Fatalf("只有一个候选时也应能播：%v", err)
	}
	if res == nil || res.URL == "" {
		t.Fatal("应返回一条可播直链")
	}
	if used.Platform != "mg" {
		t.Errorf("应取 mg，得到 %s/%s", used.Platform, used.PlatformID)
	}
}

func TestPlaybackFallbackKeepsPlayingWhenTierUnknown(t *testing.T) {
	h := newHarness(t)
	// 解析器不回报档位（外挂源常见）—— 认不出档位也要能播。
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"wy": {},
		"mg": {"mg-1": true},
	})
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.setSongs(search.UnifiedSong{
		ID: "mg-1", Songmid: "mg-1", Name: "甲", Singer: "A", Source: "mg", Duration: 200, Quality: "320k",
	})

	res, used, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200))
	if err != nil {
		t.Fatalf("认不出档位时也应能播：%v", err)
	}
	if res == nil || res.URL == "" {
		t.Fatal("应返回一条可播直链")
	}
	if used.Platform != "mg" {
		t.Errorf("应取 mg，得到 %s/%s", used.Platform, used.PlatformID)
	}
}
