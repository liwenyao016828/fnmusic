package complete

import (
	"context"
	"testing"

	"fn-lx-player/pkg/search"
)

// 摆一个平台的候选表，并记录被搜过哪些平台（顺序与次数就是要守的行为）。
func stubPlatforms(t *testing.T, calls *[]string, byPlatform map[string][]search.UnifiedSong) {
	t.Helper()
	orig := searchFn
	searchFn = func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		*calls = append(*calls, platform)
		return byPlatform[platform]
	}
	t.Cleanup(func() { searchFn = orig })

	origMeta := fetchAlbumMeta
	fetchAlbumMeta = func(albumMID string) (search.QQAlbumMeta, bool) {
		return search.QQAlbumMeta{Genre: "Pop 流行", Year: 2003}, true
	}
	t.Cleanup(func() { fetchAlbumMeta = origMeta })
}

func song(platform, mid string, year, track, disc int) search.UnifiedSong {
	s := search.UnifiedSong{
		Name: "晴天", Singer: "周杰伦", Source: platform,
		Songmid: mid, ID: mid, Duration: 269, Cover: "https://img.example/" + mid + ".jpg",
		Album: "叶惠美", Year: year, Track: track, Disc: disc,
	}
	// 只有 QQ 的搜索响应带专辑标识（风格要靠它再查一次专辑详情）
	if platform == "tx" {
		s.AlbumMID = "ALBUM" + mid
	}
	return s
}

func target(lyric, cover, year, track, disc, genre bool) Target {
	return Target{
		Path: "/m/周杰伦 - 晴天.mp3", Title: "晴天", Artist: "周杰伦",
		NeedLyric: lyric, NeedCover: cover, NeedYear: year, NeedTrack: track,
		NeedDisc: disc, NeedGenre: genre,
	}
}

// 没勾那些「只有部分平台才给」的字段时，必须还是第一个命中就停 ——
// 旧行为一次搜索就够，多搜几次是白花钱也白等。
func TestResolveStopsEarlyWhenNoRichFieldsRequested(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {song("wy", "W1", 2004, 3, 0)},
		"tx": {song("tx", "T1", 2003, 3, 1)},
	})

	plan := Resolve(context.Background(), []Target{target(true, true, false, false, false, false)},
		Options{Platforms: []string{"wy", "tx"}}, nil)

	it := plan.Items[0]
	if !it.Matched || it.MatchedBy != "wy" {
		t.Fatalf("应命中网易且立刻停，实际 matched=%v by=%s", it.Matched, it.MatchedBy)
	}
	if len(calls) != 1 || calls[0] != "wy" {
		t.Errorf("只该搜网易一次，实际搜了 %v", calls)
	}
	// 没勾字段 ⇒ 一个数字字段都不该写
	if it.FillYear != 0 || it.FillTrack != 0 || it.FillGenre != "" {
		t.Errorf("未勾选的字段不该出现在计划里：%+v", it)
	}
}

// 勾了年份/风格而先命中的平台给不出时，继续往后找一条给得出的命中。
//
// 这就是 2026-09-23 真机撞到的问题：默认顺序先命中酷狗，而酷狗的搜索响应
// 没有年份/曲序/专辑标识，于是「勾了年份」的那几栏必然全空。
func TestResolvePrefersRicherHitForRequestedFields(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"kg": {song("kg", "K1", 0, 0, 0)},    // 能匹配，但什么字段都给不出
		"tx": {song("tx", "T1", 2003, 3, 1)}, // 同一首，年份/曲序/风格齐全
	})

	plan := Resolve(context.Background(), []Target{target(true, true, true, true, false, true)},
		Options{Platforms: []string{"kg", "tx"}}, nil)

	it := plan.Items[0]
	if it.MatchedBy != "tx" {
		t.Fatalf("应换到信息更全的 QQ 命中，实际 by=%s", it.MatchedBy)
	}
	if it.FillYear != 2003 || it.FillTrack != 3 || it.FillGenre != "Pop 流行" {
		t.Errorf("字段没补齐：%+v", it)
	}
	if len(calls) != 2 {
		t.Errorf("应搜过 kg 与 tx 两个平台，实际 %v", calls)
	}
}

// 全平台都给不出那些字段时，退回第一个命中（歌词/封面照样能补），
// 并逐字段说明为什么没补上 —— 不能因为字段缺失就整首放弃。
func TestResolveFallsBackToFirstHitWhenNobodyHasFields(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {song("wy", "W1", 0, 0, 0)},
		"tx": {song("tx", "T1", 0, 0, 0)},
	})

	plan := Resolve(context.Background(), []Target{target(true, true, true, false, false, false)},
		Options{Platforms: []string{"wy", "tx"}}, nil)

	it := plan.Items[0]
	if !it.Matched || it.MatchedBy != "wy" {
		t.Fatalf("该退回第一个命中（网易），实际 %+v", it)
	}
	if it.FillYear != 0 {
		t.Errorf("平台没给年份就不该有值，实际 %d", it.FillYear)
	}
	if it.Note == "" {
		t.Error("要说明「匹配到的曲目没有年份」，否则用户不知道这一栏为什么空着")
	}
}

// 一条都匹配不上时仍然不写任何字段（宁缺毋滥），也不该被「找更全的命中」带偏。
func TestResolveNoMatchWritesNothing(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {{Name: "别的歌", Singer: "别人", Songmid: "X", ID: "X"}},
		"tx": {{Name: "又一首", Singer: "别人", Songmid: "Y", ID: "Y"}},
	})

	plan := Resolve(context.Background(), []Target{target(true, true, true, true, true, true)},
		Options{Platforms: []string{"wy", "tx"}}, nil)

	it := plan.Items[0]
	if it.Matched {
		t.Fatal("不该判定为已匹配")
	}
	if it.FillYear != 0 || it.FillTrack != 0 || it.FillDisc != 0 || it.FillGenre != "" {
		t.Errorf("没匹配上却带了要写的值：%+v", it)
	}
}
