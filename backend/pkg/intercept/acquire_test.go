package intercept

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// ②「下载源池 + 最高音质」的用例（v2.1.93）。
//
// 这一层的风险**不是**「没找到更好的源」，而是**下错歌** —— 跨源匹配搜出来一堆
// 改编版。所以判据刻意做得很硬（剥后缀后必须完全相等），用例重点也在这。

func TestCleanTitleStripsDecorations(t *testing.T) {
	cases := map[string]string{
		"海屿你":                   "海屿你",
		"海屿你 (Live)":            "海屿你",
		"海屿你（合唱版）":              "海屿你",
		"海屿你 [feat. 某某]":        "海屿你",
		"海屿你【现场版】":              "海屿你",
		"男人歌 - 合唱版":             "男人歌",
		" 海屿你  ":                "海屿你",
		"海屿你 (Live) (Remaster)": "海屿你",
		"":                      "",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestSameTitleRequiresExactMatchAfterCleaning(t *testing.T) {
	// 剥掉后缀之后必须**完全相等** —— 这一条是「不下错歌」的底线
	if !sameTitle("海屿你 (Live)", "海屿你") {
		t.Fatal("剥掉 (Live) 之后是同一首")
	}
	if !sameTitle("男人歌 - 合唱版", "男人歌") {
		t.Fatal("剥掉 - 合唱版 之后是同一首")
	}
	// ⚠️ 关键反例：包含关系不算同一首 —— 模糊匹配会把「海屿你心碎版」判成「海屿你」
	if sameTitle("海屿你心碎版", "海屿你") {
		t.Fatal("包含关系不是同一首（下错歌比音质没提升糟得多）")
	}
	if sameTitle("", "海屿你") || sameTitle("海屿你", "") {
		t.Fatal("空标题永远不算匹配")
	}
}

func TestQualityRankOrdersContainers(t *testing.T) {
	if !(qualityRank("无损") > qualityRank("AAC") && qualityRank("AAC") > qualityRank("320k")) {
		t.Fatalf("无损 > AAC > 320k：%d/%d/%d",
			qualityRank("无损"), qualityRank("AAC"), qualityRank("320k"))
	}
	if qualityRank("flac") != qualityRank("无损") {
		t.Fatal("flac 与「无损」该同档")
	}
	// 认不出的档位算最低，但不是负分（它仍是个能下的候选）
	if qualityRank("谁知道呢") != 0 || qualityRank("") != 0 {
		t.Fatalf("认不出的档位该是 0：%d", qualityRank("谁知道呢"))
	}
}

func TestPickBestQualityPrefersLossless(t *testing.T) {
	cands := []search.UnifiedSong{
		{Name: "甲", Source: "wy", Quality: "320k"},
		{Name: "甲", Source: "mg", Quality: "无损"},
		{Name: "甲", Source: "kg", Quality: "AAC"},
	}
	got, ok := pickBestQuality(cands)
	if !ok || got.Source != "mg" {
		t.Fatalf("该挑无损那条，得到 %+v", got)
	}
	// 同档并列保持原顺序（各源的排序本身带相关性）
	tie := []search.UnifiedSong{{Source: "a", Quality: "无损"}, {Source: "b", Quality: "无损"}}
	if g, _ := pickBestQuality(tie); g.Source != "a" {
		t.Fatalf("同档该保持原顺序，得到 %s", g.Source)
	}
	if _, ok := pickBestQuality(nil); ok {
		t.Fatal("空候选该返回 false")
	}
}

func TestBestQualityTrackMatchesSameTitleOnly(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg", "kg"} }
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		switch platform {
		case "mg":
			// 同名 + 无损 → 该被选中
			return []search.UnifiedSong{{ID: "1", Songmid: "1", Name: "海屿你 (Live)", Source: "mg", Quality: "无损"}}
		case "kg":
			// 不同名 → 必须被丢掉（哪怕它也是无损）
			return []search.UnifiedSong{{ID: "2", Songmid: "2", Name: "海屿你心碎版", Source: "kg", Quality: "无损"}}
		}
		return nil
	}

	got, ok := h.it.bestQualityTrack(context.Background(), "海屿你", "雷米克斯", 0)
	if !ok {
		t.Fatal("该在池里找到同名的高音质版本")
	}
	if got.Platform != "mg" || got.PlatformID != "1" {
		t.Fatalf("该选中同名那条（剥掉 (Live) 后相等）：%+v", got)
	}
}

func TestBestQualityTrackGivesUpWhenNoSameTitle(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "9", Songmid: "9", Name: "完全不同的歌", Source: "mg", Quality: "无损"}}
	}
	if _, ok := h.it.bestQualityTrack(context.Background(), "海屿你", "雷米克斯", 0); ok {
		t.Fatal("没有同名曲目时该返回 false（让调用方回落到自己的平台）")
	}
}

func TestBestQualityTrackNoPool(t *testing.T) {
	// 池空（外挂音源没开 / 没配）→ 直接 false，一次搜索都不发
	h := newHarness(t)
	called := false
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		called = true
		return nil
	}
	if _, ok := h.it.bestQualityTrack(context.Background(), "甲", "乙", 0); ok {
		t.Fatal("池空时该返回 false")
	}
	if called {
		t.Fatal("池空时不该发搜索")
	}
}

func TestResolveForDownloadFallsBackToOwnPlatform(t *testing.T) {
	// 池里没有更好的 → 用曲目自己平台的直链（否则会出现「收藏了却下不来」）
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true}, song("w1", "甲", "乙"))
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }

	res, used, err := h.it.resolveForDownload(context.Background(), onlineTrackFor("wy", "w1", "甲", "乙"))
	if err != nil {
		t.Fatalf("该回落到自己平台并解析成功：%v", err)
	}
	if res == nil || res.URL == "" {
		t.Fatalf("该拿到直链：%+v", res)
	}
	// 回落时「实际取音来源」必须如实回填成它自己的平台
	if used.Platform != "wy" {
		t.Fatalf("没换源时实际来源该是 wy，得到 %q", used.Platform)
	}
}

// onlineTrackFor 造一条在线曲目（用例里手写太多字段读起来吵）。
func onlineTrackFor(platform, id, title, artist string) online.Track {
	return online.Track{Platform: platform, PlatformID: id, Title: title, Artists: []string{artist}}
}

// 池里那份必须**严格更好**才换源。
//
// 真机实测（2026-10-06）：搜「晴天」时网易云是 flac、咪咕只有 320k。
// 只挑「池里最好的」会把无损**降级**成 320k —— 与「下载一律最高音质」正好相反。
func TestResolveForDownloadNeverDowngrades(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true}, song("w1", "晴天", "周杰伦"))
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgid", Songmid: "mgid", Name: "晴天", Source: "mg", Quality: "320k"}}
	}
	// harness 的池默认只注册 wy —— 换源之后得解析得出来，所以这里补上 mg
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgid": {URL: "https://cdn.invalid/mgid.mp3", Format: "mp3", Size: 8192},
	}})
	own := onlineTrackFor("wy", "w1", "晴天", "周杰伦")
	own.Quality = "无损"

	res, used, err := h.it.resolveForDownload(context.Background(), own)
	if err != nil {
		t.Fatalf("该照常解析：%v", err)
	}
	if strings.Contains(res.URL, "mgid") {
		t.Fatalf("池里的 320k 比曲目自己的无损差，不该换源：%s", res.URL)
	}
	if used.Platform != "wy" {
		t.Fatalf("拒绝降级之后实际来源仍该是 wy，得到 %q", used.Platform)
	}
}

// 反过来：池里有更高音质就必须换过去。
func TestResolveForDownloadUpgrades(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true}, song("w1", "晴天", "周杰伦"))
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgid", Songmid: "mgid", Name: "晴天", Source: "mg", Quality: "无损"}}
	}
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgid": {URL: "https://cdn.invalid/mgid.flac", Format: "flac", Size: 8192},
	}})
	own := onlineTrackFor("wy", "w1", "晴天", "周杰伦")
	own.Quality = "320k"

	res, used, err := h.it.resolveForDownload(context.Background(), own)
	if err != nil {
		t.Fatalf("该解析成功：%v", err)
	}
	if !strings.Contains(res.URL, "mgid") {
		t.Fatalf("池里有无损、曲目自己只有 320k → 必须换源：%s", res.URL)
	}
	// ⚠️ 换了源就必须把**实际来源**报出来 —— 界面拿它显示，不能只报原平台
	if used.Platform != "mg" {
		t.Fatalf("换源之后实际来源该是 mg，得到 %q", used.Platform)
	}
	if qualityRank(used.Quality) != qualityRank("无损") {
		t.Fatalf("实际音质该跟着换过去：%q", used.Quality)
	}
}

// 「无损优先，**同档比体积**」（用户 2026-10-06 指令）。
//
// 同一首歌、同一档位，文件更大 = 码率更高。但体积**未知**（0）的一方不能算「更小」——
// 外挂音源里缺这个字段是常态，把 0 当体积会让「没给字段的源」永远选不上。
func TestPickBestQualityComparesSizeWithinTier(t *testing.T) {
	// 同档：大的赢
	cands := []search.UnifiedSong{
		{Source: "a", Quality: "无损", FileSize: 20 << 20},
		{Source: "b", Quality: "无损", FileSize: 45 << 20},
		{Source: "c", Quality: "无损", FileSize: 30 << 20},
	}
	if got, _ := pickBestQuality(cands); got.Source != "b" {
		t.Fatalf("同档该挑体积最大的（b），得到 %s", got.Source)
	}

	// 档位优先于体积：无损再小也赢过更大的 320k
	mixed := []search.UnifiedSong{
		{Source: "lo", Quality: "320k", FileSize: 90 << 20},
		{Source: "hi", Quality: "无损", FileSize: 8 << 20},
	}
	if got, _ := pickBestQuality(mixed); got.Source != "hi" {
		t.Fatalf("无损优先，体积不能跨档位翻盘：得到 %s", got.Source)
	}

	// 体积未知不主动占优：先到的（有体积）留着
	unknown := []search.UnifiedSong{
		{Source: "known", Quality: "无损", FileSize: 20 << 20},
		{Source: "unknown", Quality: "无损", FileSize: 0},
	}
	if got, _ := pickBestQuality(unknown); got.Source != "known" {
		t.Fatalf("体积未知不该顶掉已知更大的：得到 %s", got.Source)
	}
	// 反过来：先到的未知，后面来了个有体积的 → 有体积的赢（0 不是「很大」）
	reverse := []search.UnifiedSong{
		{Source: "unknown", Quality: "无损", FileSize: 0},
		{Source: "known", Quality: "无损", FileSize: 1 << 20},
	}
	if got, _ := pickBestQuality(reverse); got.Source != "known" {
		t.Fatalf("已知体积的一方该赢过未知的：得到 %s", got.Source)
	}
	// 双方都未知 → 保持原顺序
	both := []search.UnifiedSong{
		{Source: "first", Quality: "无损"},
		{Source: "second", Quality: "无损"},
	}
	if got, _ := pickBestQuality(both); got.Source != "first" {
		t.Fatalf("都没体积时该保持原顺序：得到 %s", got.Source)
	}
}

// 「歌名 + 歌手 + **时长**」（用户 2026-10-06 指令）。
//
// 光比歌名会在不同版录音之间撞名 —— 尤其是 `cleanTitle` 把「甲 (Live)」和「甲」
// 判成同一首之后（那正是我们想要的），就更需要时长兜一道。
// 下错一首比音质没提升糟得多。
func TestDurationMatches(t *testing.T) {
	if !durationMatches(0, 240) || !durationMatches(240, 0) {
		t.Fatal("任一方没时长就不参与判定（那是「没给字段」，不是「对不上」）")
	}
	if !durationMatches(240, 238) {
		t.Fatal("差一两秒是同一版（前奏裁剪 / 报时取整）")
	}
	if !durationMatches(240, 245) {
		t.Fatal("容差 5 秒内算同一版")
	}
	if durationMatches(240, 300) {
		t.Fatal("⚠️ 差一分钟是另一版录音，必须拒掉")
	}
}

func TestDurationSecondsNormalizesUnits(t *testing.T) {
	// 网易给毫秒、QQ 给秒 —— 统一压到秒
	if got := durationSeconds(search.UnifiedSong{Duration: 245000}); got != 245 {
		t.Fatalf("毫秒该压成秒：%d", got)
	}
	if got := durationSeconds(search.UnifiedSong{Duration: 245}); got != 245 {
		t.Fatalf("秒保持原样：%d", got)
	}
	// Duration 空时退回 Interval
	if got := durationSeconds(search.UnifiedSong{Interval: 300}); got != 300 {
		t.Fatalf("该退回 Interval：%d", got)
	}
}

func TestBestQualityTrackRejectsDifferentRecording(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		// 同名同歌手，但是**现场版**（时长差一分半）—— 必须拒掉
		return []search.UnifiedSong{{ID: "live", Songmid: "live", Name: "富士山下", Source: "mg", Quality: "无损", Duration: 330}}
	}
	if _, ok := h.it.bestQualityTrack(context.Background(), "富士山下", "陈奕迅", 245); ok {
		t.Fatal("⚠️ 时长对不上就是另一版录音，宁可回落也不能下错")
	}
	// 换成长度对得上的版本 → 该接受
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "ok", Songmid: "ok", Name: "富士山下", Source: "mg", Quality: "无损", Duration: 246}}
	}
	got, ok := h.it.bestQualityTrack(context.Background(), "富士山下", "陈奕迅", 245)
	if !ok || got.PlatformID != "ok" {
		t.Fatalf("同一版录音该接受：%+v ok=%v", got, ok)
	}
	// 候选不报时长 → 不参与判定，照常接受
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "nodur", Songmid: "nodur", Name: "富士山下", Source: "mg", Quality: "无损"}}
	}
	if _, ok := h.it.bestQualityTrack(context.Background(), "富士山下", "陈奕迅", 245); !ok {
		t.Fatal("候选没给时长时不该被拒（那是「没给字段」）")
	}
}
