package intercept

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// ③「洛雪(LX)音源脚本的搜索进下载源池」的用例。
//
// 这一层最大的风险**不是**「洛雪源没接上」，而是**接上之后把池的语义改掉**：
// 池挑的是「同名同版录音里最好的那份」，洛雪候选必须过**同一套**筛，
// 而且**缺字段要还是缺**（不许拿 0 当「最小」、也不许给它编一个音质档位）。
// 所以下面对每一条都钉死「谁该进、谁该出、谁排前面」。

// lxSong 造一条洛雪那边的搜索结果（形状照 lxnode.Song，字段缺失就留零值）。
func lxSong(source, id, name, singer string, dur int) lxnode.Song {
	return lxnode.Song{Script: "合成音源.js", Source: source, ID: id, Name: name, Singer: singer, Duration: dur}
}

// lxWith 给 harness 装上洛雪源（nil = 没接，缺省状态）。
func lxWith(h *harness, fn func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error)) *harness {
	h.it.lxSearch = fn
	return h
}

// 洛雪候选进池后必须**过同一套筛**：同名 + 同一版录音（时长对得上）。
//
// 这一条用例的形状是「三个候选各犯一个错」——只有第一个该活下来：
//
//	· S-ok   同名 + 时长对得上（245 vs 245）      → 进池
//	· S-live 同名但是现场版（时长差 85 秒）        → 拒（durationMatches 判成另一版）
//	· S-other 完全不同的歌名                       → 拒（sameTitle）
func TestLxCandidatesGoThroughTheSameFilters(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
	lxWith(h, func(_ context.Context, keyword string, limit int) ([]lxnode.Song, error) {
		if !strings.Contains(keyword, "富士山下") {
			t.Errorf("该拿「歌名 歌手」当关键词问洛雪，得到 %q", keyword)
		}
		if limit != lxSearchLimit {
			t.Errorf("每次该问 %d 条，得到 %d", lxSearchLimit, limit)
		}
		return []lxnode.Song{
			lxSong("qsvip", "S-ok", "富士山下", "陈奕迅", 245),
			lxSong("qsvip", "S-live", "富士山下 (Live)", "陈奕迅", 330),
			lxSong("qsvip", "S-other", "完全不同的歌", "陈奕迅", 245),
		}, nil
	})

	got := h.it.rankedSources(context.Background(), "富士山下", "陈奕迅", 245)
	if len(got) != 1 {
		t.Fatalf("该只剩「同名 + 同一版录音」那一条，得到 %+v", got)
	}
	if got[0].Platform != "lx:qsvip" || got[0].PlatformID != "S-ok" {
		t.Fatalf("该是洛雪那条（带 lx: 前缀），得到 %s/%s", got[0].Platform, got[0].PlatformID)
	}
	if !strings.HasPrefix(got[0].Platform, lxSourcePrefix) {
		t.Fatalf("洛雪候选的平台标识该带 %q 前缀（否则会和平台代号撞名）：%q", lxSourcePrefix, got[0].Platform)
	}
}

// 候选**没报时长**时不许被拒 —— 那是「脚本没给这个字段」，不是「对不上」。
//
// （池里 durationMatches 的既有口径：任一方为 0 就不参与判定。这里只验洛雪
// 这条路上的确是按这个口径走的，没有另加一条「洛雪必须有时长」。）
func TestLxCandidateWithoutDurationStillEnters(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
	lxWith(h, func(_ context.Context, _ string, _ int) ([]lxnode.Song, error) {
		return []lxnode.Song{lxSong("qsvip", "S-nodur", "富士山下", "陈奕迅", 0)}, nil
	})
	got := h.it.rankedSources(context.Background(), "富士山下", "陈奕迅", 245)
	if len(got) != 1 || got[0].PlatformID != "S-nodur" {
		t.Fatalf("洛雪没给时长时不该被拒：%+v", got)
	}
}

// 洛雪源**挂了**（宿主没开 / 连不上 / 脚本报错）只该让池里少它一条，别的照常。
//
// 三种失败各验一次，并且要求结果与「洛雪没接」时**逐字段相同** ——
// 洛雪这条来源绝不许有能力把整个池拖坏（对齐 lxnode 的硬约束）。
func TestLxSearchFailureLeavesPoolUnchanged(t *testing.T) {
	build := func(lx func(context.Context, string, int) ([]lxnode.Song, error)) []online.Track {
		h := newHarness(t)
		h.it.cfg.DownloadSources = func() []string { return []string{"mg", "kg"} }
		h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
			if platform == "mg" {
				return []search.UnifiedSong{{ID: "mg1", Songmid: "mg1", Name: "富士山下", Source: "mg", Quality: "无损", Duration: 245}}
			}
			return []search.UnifiedSong{{ID: "kg1", Songmid: "kg1", Name: "富士山下", Source: "kg", Quality: "320k", Duration: 246}}
		}
		if lx != nil {
			lxWith(h, lx)
		}
		return h.it.rankedSources(context.Background(), "富士山下", "陈奕迅", 245)
	}

	want := build(nil) // 洛雪没接 = 改动前的行为
	if len(want) != 2 {
		t.Fatalf("前提：平台源该给出 2 条候选，得到 %+v", want)
	}

	// 三种失败：宿主没开（哨兵错误）/ 连不上 / 脚本报错
	fails := []error{
		fmt.Errorf("%w（off）", lxnode.ErrNotRunning),
		errors.New("dial tcp 127.0.0.1:8920: connect: connection refused"),
		errors.New("qsvip → 上游返回空"),
	}
	for _, err := range fails {
		got := build(func(context.Context, string, int) ([]lxnode.Song, error) { return nil, err })
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("洛雪失败（%v）后池的候选该与没接洛雪时逐字段相同：\n got=%+v\nwant=%+v", err, got, want)
		}
	}

	// 洛雪返回空切片（搜到 0 条，不是错误）同样不该动池
	got := build(func(context.Context, string, int) ([]lxnode.Song, error) { return nil, nil })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("洛雪搜到 0 条时池该照旧：\n got=%+v\nwant=%+v", got, want)
	}
}

// ⚠️ 「宿主没开」这个分支**不该刷日志**（它是用户开关的正常状态，不是故障），
// 「开着但搜索失败了」**该刷**。两者对池的结论一样，但对排障的意义天差地别。
func TestLxNotRunningIsSilentButRealFailuresAreLogged(t *testing.T) {
	logs := func() (*harness, *[]string) {
		h := newHarness(t)
		h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
		h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
		var got []string
		h.it.logf = func(format string, args ...any) { got = append(got, format) }
		return h, &got
	}

	h, got := logs()
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		return nil, fmt.Errorf("%w（off）", lxnode.ErrNotRunning)
	})
	h.it.rankedSources(context.Background(), "甲", "乙", 0)
	if len(*got) != 0 {
		t.Fatalf("宿主没开是正常状态，不该刷日志：%v", *got)
	}

	h2, got2 := logs()
	lxWith(h2, func(context.Context, string, int) ([]lxnode.Song, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	h2.it.rankedSources(context.Background(), "甲", "乙", 0)
	if len(*got2) != 1 || !strings.Contains((*got2)[0], "洛雪源搜索失败") {
		t.Fatalf("开着但失败该留一行日志：%v", *got2)
	}
}

// **缺音质的实际后果**：洛雪的候选进得了池，但按池里的既有判据它排在有档位的那条**之后**，
// 而且过不了 downloadCandidates 的「严格更好」那一道 → 下载决策不变。
//
// 这一条用例是**如实记录现状**用的，不是我们想要的结果：缺口在「LX 搜索结果里没有音质
// 档位」这个数据上（实测，见 lxsource.go 顶部），不该靠在判据里给洛雪开后门来绕。
// 它同时是一条防线：**哪天有人为了让洛雪源生效去放宽 qualityRank / 严格更优，
// 这条用例会立刻红**。
func TestLxCandidateWithoutQualityRanksLastAndIsNotTried(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mg1", Songmid: "mg1", Name: "晴天", Source: "mg", Quality: "320k", Duration: 269}}
	}
	// 洛雪这条没有 Quality（实测的 qsvip 就是这样）
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		return []lxnode.Song{lxSong("qsvip", "q1", "晴天", "周杰伦", 269)}, nil
	})

	ranked := h.it.rankedSources(context.Background(), "晴天", "周杰伦", 269)
	if len(ranked) != 2 {
		t.Fatalf("两条都该在池里（洛雪只是缺档位，不是不许进）：%+v", ranked)
	}
	if ranked[0].Platform != "mg" {
		t.Fatalf("有档位的 320k 该排在「档位未知」之前：%+v", ranked)
	}
	if ranked[1].Platform != "lx:qsvip" {
		t.Fatalf("洛雪那条该排在最后：%+v", ranked)
	}
	if qualityRank(ranked[1].Quality) != 0 {
		t.Fatalf("缺档位就该算最低档（不是我们贴的标签）：%q", ranked[1].Quality)
	}

	// 曲目自己是 320k → 洛雪那条（档位未知）**不是严格更好**，于是不会被拿去试。
	own := onlineTrackFor("wy", "w1", "晴天", "周杰伦")
	own.Quality = "320k"
	cands := h.it.downloadCandidates(context.Background(), own)
	if len(cands) != 1 || cands[0].Platform != "wy" {
		t.Fatalf("缺档位的洛雪候选过不了「严格更好」，该只剩曲目自己那条：%+v", cands)
	}
}

// 反过来：**只要脚本给了音质档位**，整条链路就该真跑通 —— 换源、解析、用洛雪那份取音。
//
// 这条是给未来留的门：一旦有脚本（或宿主补的字段）能给出档位，池不需要任何改动
// 就能用上洛雪源。现在实测的 qsvip 给不出，所以这条走的是**注入的**假数据。
func TestLxCandidateWithQualityWinsThePool(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true}, song("w1", "晴天", "周杰伦"))
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mg1", Songmid: "mg1", Name: "晴天", Source: "mg", Quality: "320k", Duration: 269}}
	}
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		s := lxSong("qsvip", "Q-9", "晴天", "周杰伦", 269)
		s.Quality = "flac" // ← 假设脚本给了档位（实测的 qsvip 没给）
		s.Size = 30 << 20
		return []lxnode.Song{s}, nil
	})
	// 洛雪候选要真能取音，池里得有一个 `lx:qsvip` 解析器（生产侧由 lxnode 注册）
	h.it.pool.Register(&stubResolver{platform: "lx:qsvip", answers: map[string]*online.Resolved{
		"Q-9": {URL: "https://cdn.invalid/Q-9.flac", Format: "flac", Size: 30 << 20},
	}})

	own := onlineTrackFor("wy", "w1", "晴天", "周杰伦")
	own.Quality = "320k"

	// ① 池里它该压过 320k 那条（无损优先）
	ranked := h.it.rankedSources(context.Background(), "晴天", "周杰伦", 269)
	if len(ranked) == 0 || ranked[0].Platform != "lx:qsvip" || ranked[0].Quality != "flac" {
		t.Fatalf("给了档位就该排第一并带着档位出去：%+v", ranked)
	}
	// ② 下载候选该把它排在自己平台前面
	cands := h.it.downloadCandidates(context.Background(), own)
	if len(cands) != 2 || cands[0].Platform != "lx:qsvip" {
		t.Fatalf("洛雪那条（无损）该排在曲目自己（320k）前面：%+v", cands)
	}
	// ③ 真的换源取音，并且如实回报「实际从哪儿取」
	res, used, err := h.it.resolveForDownload(context.Background(), own)
	if err != nil {
		t.Fatalf("该解析成功：%v", err)
	}
	if !strings.Contains(res.URL, "Q-9") {
		t.Fatalf("该从洛雪源取音：%s", res.URL)
	}
	if used.Platform != "lx:qsvip" || used.Quality != "flac" {
		t.Fatalf("实际取音来源该如实回填：%+v", used)
	}
}

// 洛雪脚本自己的源 key 与曲率的平台代号**会撞名**（脚本里也有 wy / tx / kg）。
// `lx:` 前缀就是为这件事存在的：撞名时必须是两条互不干扰的候选，各找各的解析器。
func TestLxSourceKeyDoesNotCollideWithPlatformCode(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"wy"} } // 平台 wy 也在池里
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "P-1", Songmid: "P-1", Name: "晴天", Source: "wy", Quality: "无损", Duration: 269, FileSize: 20 << 20}}
	}
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		s := lxSong("wy", "Q-1", "晴天", "周杰伦", 269) // 洛雪脚本也叫 wy
		s.Quality = "无损"
		s.Size = 40 << 20 // 同档比体积 → 洛雪那条更大
		return []lxnode.Song{s}, nil
	})
	// 同名同档两条：曲率平台的 wy 与洛雪的 wy，必须是两个平台、各走各的解析器
	h.it.pool.Register(&stubResolver{platform: "wy", answers: map[string]*online.Resolved{
		"P-1": {URL: "https://cdn.invalid/platform-wy.mp3", Format: "mp3"},
	}})
	h.it.pool.Register(&stubResolver{platform: "lx:wy", answers: map[string]*online.Resolved{
		"Q-1": {URL: "https://cdn.invalid/lx-wy.flac", Format: "flac"},
	}})

	ranked := h.it.rankedSources(context.Background(), "晴天", "周杰伦", 269)
	if len(ranked) != 2 {
		t.Fatalf("该有两条互不覆盖的候选：%+v", ranked)
	}
	if ranked[0].Platform != "lx:wy" || ranked[0].PlatformID != "Q-1" {
		t.Fatalf("同档比体积：洛雪那条更大，该排第一：%+v", ranked)
	}
	res, err := h.it.pool.Resolve(context.Background(), ranked[0].Platform, ranked[0].PlatformID)
	if err != nil || !strings.Contains(res.URL, "lx-wy") {
		t.Fatalf("lx:wy 该走洛雪的解析器：%+v err=%v", res, err)
	}
	res2, err := h.it.pool.Resolve(context.Background(), "wy", "P-1")
	if err != nil || !strings.Contains(res2.URL, "platform-wy") {
		t.Fatalf("平台 wy 该走平台自己的解析器：%+v err=%v", res2, err)
	}
}

// 洛雪返回的条目缺 id / 缺歌名 / 缺源标识时，**不该进池** ——
// 没有平台内 id 的候选在后面就是一次注定失败的解析（这与 trackFromSong 同一条口径）。
func TestLxCandidateMissingIDOrNameOrSourceIsDropped(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		noID := lxSong("qsvip", "", "富士山下", "陈奕迅", 245)
		noName := lxSong("qsvip", "S-2", "", "陈奕迅", 245)
		noSrc := lxSong("", "S-3", "富士山下", "陈奕迅", 245)
		return []lxnode.Song{noID, noName, noSrc}, nil
	})
	if got := h.it.rankedSources(context.Background(), "富士山下", "陈奕迅", 245); len(got) != 0 {
		t.Fatalf("缺 id / 歌名 / 源标识的都该被丢掉：%+v", got)
	}
}

// 池里**只有**洛雪这一条来源时（musicdl 那边全关着）也该搜 —— 池非空就该工作。
//
// 反过来，洛雪没接时「池空 = 一次搜索都不发」这条**必须保持不变**
// （见 TestBestQualityTrackNoPool：它是改动前就有的承诺）。
func TestLxOnlyPoolStillSearches(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return nil } // musicdl 全关
	sent := false
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { sent = true; return nil }
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) {
		return []lxnode.Song{lxSong("qsvip", "S-1", "富士山下", "陈奕迅", 245)}, nil
	})
	got := h.it.rankedSources(context.Background(), "富士山下", "陈奕迅", 245)
	if len(got) != 1 || got[0].Platform != "lx:qsvip" {
		t.Fatalf("只剩洛雪一条来源时也该找到候选：%+v", got)
	}
	if sent {
		t.Fatal("平台列表为空时不该去问平台源")
	}
}

// 关键词为空（歌名歌手都空）时不该发任何请求 —— 与平台源那条早退是同一条口径。
func TestLxNotSearchedWithoutKeyword(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	called := false
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) { called = true; return nil, nil })
	h.it.rankedSources(context.Background(), "", "", 0)
	if called {
		t.Fatal("没有关键词时不该问洛雪")
	}
}

// ── 「开关关着时行为与改动前**逐字段相同**」─────────────────────────────────
//
// 这是本改动最重要的一条承诺：洛雪源缺省没接（Config.LxSearch == nil，
// main.go 里传的是宿主的方法值、宿主没起来就是 ErrNotRunning）时，池的候选与
// 挑选结果必须与改动前**一个字节都不差**。
//
// 用例不比「大概一致」，而是拿一份**写死的期望值**逐字段比。期望值不是我手写的推测：
// 它是把这份用例对**改动前的 acquire.go**（git HEAD 那版）跑一遍打出来的输出，
// 原样抄进来的 —— 也就是说它确实是「改动前的结果」。
func TestPoolUnchangedWhenLxDisabled(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg", "kg"} }
	h.it.searcher = func(_ string, platform string, page, size int) []search.UnifiedSong {
		// 每次都造一份新的：池会改这些结构体，共享切片会让用例之间互相污染
		switch platform {
		case "mg":
			return []search.UnifiedSong{
				{ID: "mg-flac", Songmid: "mg-flac", Source: "mg", Name: "晴天", Singer: "周杰伦", Album: "叶惠美", Duration: 269, Quality: "无损", FileSize: 30 << 20},
				{ID: "mg-live", Songmid: "mg-live", Name: "晴天 (Live)", Singer: "周杰伦", Album: "演唱会", Duration: 330, Quality: "无损", FileSize: 30 << 20},
				{ID: "mg-cover", Songmid: "mg-cover", Name: "晴天娃娃", Singer: "周杰伦", Album: "叶惠美", Duration: 269, Quality: "无损"},
			}
		case "kg":
			return []search.UnifiedSong{
				{ID: "kg-320", Songmid: "kg-320", Source: "kg", Name: "晴天", Singer: "周杰伦", Album: "叶惠美", Duration: 270, Quality: "320k", FileSize: 8 << 20},
			}
		}
		return nil
	}
	// ⚠️ 这里**刻意不设** h.it.lxSearch：那就是「缺省关」。
	if h.it.lxSearch != nil {
		t.Fatal("前提：这个用例要的是「洛雪源没接」的状态")
	}

	wantRanked := []online.Track{
		{Platform: "mg", PlatformID: "mg-flac", Title: "晴天", Artists: []string{"周杰伦"}, Album: "叶惠美", Duration: 269, Quality: "无损"},
		{Platform: "kg", PlatformID: "kg-320", Title: "晴天", Artists: []string{"周杰伦"}, Album: "叶惠美", Duration: 270, Quality: "320k"},
	}
	gotRanked := h.it.rankedSources(context.Background(), "晴天", "周杰伦", 269)
	if !reflect.DeepEqual(gotRanked, wantRanked) {
		t.Fatalf("洛雪没接时 rankedSources 必须与改动前逐字段相同：\n got=%+v\nwant=%+v", gotRanked, wantRanked)
	}

	own := onlineTrackFor("wy", "w1", "晴天", "周杰伦")
	own.Album = "叶惠美"
	own.Duration = 269
	own.Quality = "320k"
	wantCands := append(append([]online.Track(nil), wantRanked[:1]...), own)
	gotCands := h.it.downloadCandidates(context.Background(), own)
	if !reflect.DeepEqual(gotCands, wantCands) {
		t.Fatalf("洛雪没接时 downloadCandidates 必须与改动前逐字段相同：\n got=%+v\nwant=%+v", gotCands, wantCands)
	}

	// 池的挑选结果（挑哪条）也必须一样
	best, ok := h.it.bestQualityTrack(context.Background(), "晴天", "周杰伦", 269)
	if !ok || !reflect.DeepEqual(best, wantRanked[0]) {
		t.Fatalf("洛雪没接时最好的那条该仍是 mg-flac：%+v ok=%v", best, ok)
	}
}

// Config.LxSearch 必须**真的接进字段** —— 上面那些用例都是直接设 h.it.lxSearch
// 的，绕过了 New() 那一步；少了这个，main.go 里传进去的东西会被静默丢掉。
func TestLxSearchConfigHookIsWired(t *testing.T) {
	var gotKeyword string
	it := New(Config{
		DataDir:  t.TempDir(),
		Upstream: &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
		LxSearch: func(_ context.Context, keyword string, _ int) ([]lxnode.Song, error) {
			gotKeyword = keyword
			return []lxnode.Song{lxSong("qsvip", "Q-1", "晴天", "周杰伦", 269)}, nil
		},
		DownloadSources: func() []string { return nil },
	})
	if it == nil {
		t.Fatal("该建出拦截层")
	}
	got, ok := it.bestQualityTrack(context.Background(), "晴天", "周杰伦", 269)
	if !ok || got.Platform != "lx:qsvip" {
		t.Fatalf("Config.LxSearch 没接上字段：%+v ok=%v", got, ok)
	}
	if !strings.Contains(gotKeyword, "晴天") {
		t.Fatalf("该把「歌名 歌手」当关键词：%q", gotKeyword)
	}
}

// 「洛雪搜到 0 条」**不是错误** —— 它是正常结果，池那边只是少一条来源。
// （宿主 200 + results:[] 的路径；上游没歌、或上游现在就是 404 时都会走到这里。）
func TestLxEmptyResultIsNotAnError(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong { return nil }
	var logs []string
	h.it.logf = func(format string, args ...any) { logs = append(logs, format) }
	lxWith(h, func(context.Context, string, int) ([]lxnode.Song, error) { return nil, nil })

	if got := h.it.rankedSources(context.Background(), "晴天", "周杰伦", 269); len(got) != 0 {
		t.Fatalf("0 条就该是空池：%+v", got)
	}
	if len(logs) != 0 {
		t.Fatalf("「搜到 0 条」不是失败，不该刷日志：%v", logs)
	}
}
