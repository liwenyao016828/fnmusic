package intercept

import (
	"context"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 这一组用例盯的是「试听跨源兜底」（见 playback_fallback.go）。
//
// 背景（2026-10-24 用户拍板）：榜单与在线歌单不再按可播性过滤，列表按上游原样
// 给满。于是本平台解析不出直链的曲目（网易热歌榜 100 首里约 64 首）能响不能响，
// 全看这里。要验的四件事：
//  1. 本平台拿不到 → 真的能从别的源拿到；
//  2. 别的源也没有 → 如实报失败（不许假装成功）；
//  3. 结论要缓存（同一首反复播不该反复搜）；
//  4. 池子空的时候一次搜索都不发（不许无意义地打网络）。

// playTrack 造一条「用户在列表里点的那首歌」。
func playTrack(platform, id, title, artist string, sec int) online.Track {
	return online.Track{
		Platform:   platform,
		PlatformID: id,
		Title:      title,
		Artists:    []string{artist},
		Album:      "专辑",
		Duration:   sec,
	}
}

// fbHarness 造一个「本平台解析不出、池里有一条同名其他平台曲目」的场景。
//
// 返回的 harness 里：
//   - pool 只有 wy 解析器，且 wy/900 不在答案表里 → 本平台必然失败；
//   - 池里注册了 mg（咪咕）解析器，能给 <mgid> 出直链；
//   - 搜索替身按 keyword 返回 h.online 里的候选。
func fbHarness(t *testing.T, mgID string) *harness {
	t.Helper()
	h := newHarness(t)
	h.it.pool = chartResolverPool(map[string]map[string]bool{
		"wy": {"501": true},
		"mg": {mgID: true},
	})
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	return h
}

func (h *harness) setSongs(songs ...search.UnifiedSong) {
	h.mu.Lock()
	h.online = songs
	h.mu.Unlock()
}

func TestPlaybackFallsBackToOtherSource(t *testing.T) {
	h := fbHarness(t, "mg-900")
	h.setSongs(search.UnifiedSong{
		ID: "mg-900", Songmid: "mg-900", Name: "甲", Singer: "A",
		Album: "专辑", Source: "mg", Duration: 200, Quality: "320k",
	})

	got, used, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200))
	if err != nil {
		t.Fatalf("本平台播不了时应跨源兜底，却报错：%v", err)
	}
	if got == nil || got.URL == "" {
		t.Fatal("兜底应返回一条可播直链")
	}
	if used.Platform != "mg" || used.PlatformID != "mg-900" {
		t.Errorf("实际取音的应是池里的 mg/mg-900，得到 %s/%s", used.Platform, used.PlatformID)
	}
}

func TestPlaybackFallbackReportsFailureWhenNoMatch(t *testing.T) {
	h := fbHarness(t, "mg-900")
	// 池里只有**别的歌**（歌名与时长都对不上）。
	h.setSongs(search.UnifiedSong{
		ID: "mg-1", Songmid: "mg-1", Name: "乙", Singer: "B",
		Source: "mg", Duration: 111, Quality: "320k",
	})

	if _, _, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200)); err == nil {
		t.Fatal("所有源都没有这首歌时应如实报失败，不许假装成功")
	}
}

func TestPlaybackFallbackCachesResult(t *testing.T) {
	h := fbHarness(t, "mg-900")
	searches := 0
	h.it.searcher = func(keyword, platform string, page, size int) []search.UnifiedSong {
		searches++
		return []search.UnifiedSong{{
			ID: "mg-900", Songmid: "mg-900", Name: "甲", Singer: "A",
			Source: "mg", Duration: 200, Quality: "320k",
		}}
	}
	tr := playTrack("wy", "900", "甲", "A", 200)

	for n := 0; n < 3; n++ {
		if _, _, err := h.it.resolvePlayback(context.Background(), tr); err != nil {
			t.Fatalf("第 %d 次播放失败：%v", n+1, err)
		}
	}
	// 第一次真搜，之后命中缓存 —— 拖动进度条 / 切歌回来不该再搜一轮。
	if searches != 1 {
		t.Errorf("同一首反复播放应只搜一次，实际搜了 %d 次", searches)
	}
}

func TestPlaybackFallbackSkipsSearchWhenPoolEmpty(t *testing.T) {
	h := newHarness(t)
	// 池是空的：没有下载源、也没接洛雪。
	h.it.cfg.DownloadSources = func() []string { return nil }
	searches := 0
	h.it.searcher = func(keyword, platform string, page, size int) []search.UnifiedSong {
		searches++
		return nil
	}

	if _, _, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200)); err == nil {
		t.Fatal("没有任何源时应当失败")
	}
	if searches != 0 {
		t.Errorf("池空时不该发任何搜索，实际发了 %d 次", searches)
	}
}

func TestPlaybackFallbackSkipsOriginalTrackItself(t *testing.T) {
	h := fbHarness(t, "mg-900")
	// 搜索结果里先回一条**原曲目自己**（同一个 wy/900），再回一条 mg 的。
	h.setSongs(
		search.UnifiedSong{ID: "900", Songmid: "900", Name: "甲", Singer: "A", Source: "wy", Duration: 200},
		search.UnifiedSong{ID: "mg-900", Songmid: "mg-900", Name: "甲", Singer: "A", Source: "mg", Duration: 200},
	)

	_, used, err := h.it.resolvePlayback(context.Background(), playTrack("wy", "900", "甲", "A", 200))
	if err != nil {
		t.Fatalf("跨源兜底失败：%v", err)
	}
	// 原曲目自己那条必然解析失败（答案表里没有 wy/900），所以真正取到音的
	// 只能是 mg —— 这条断言盯的是「不许把原曲目当候选又试一遍」。
	if used.Platform != "mg" {
		t.Errorf("应跳过原曲目自身、取到 mg，得到 %s/%s", used.Platform, used.PlatformID)
	}
}
