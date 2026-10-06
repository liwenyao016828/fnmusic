package intercept

import (
	"context"
	"sort"
	"strings"
	"sync"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 「下载源池」：按歌名 + 歌手去多个源找同名曲目，挑**最高音质**的那条来取音。
//
// # 为什么下载不直接用「这首歌自己平台的直链」
//
// 匹配判据是「歌名 + 歌手 + **时长**」（用户 2026-10-06 指令）：光比歌名会在
// 不同版录音之间撞名，时长兜一道。
//
// 用户的分工（2026-10-06 指令，paraphrased）：网易云 / QQ 是**发现**来源（排行榜、
// 歌单、私人订阅），musicdl 那批平台是**取音**来源。同名的歌在不同平台上音质不一样
// —— 网易云给的常常是 128k/320k mp3，而咪咕 / 酷狗那边可能有 FLAC。下载要的是
// 「最好那份」，不是「这首歌来源平台给的那份」。
//
// 池里找不到更好的就**回落**到曲目自己平台的直链 —— 否则会出现「收藏了却下不来」。
//
// # 宁可不换源，也不下错歌
//
// 跨源匹配是**有风险**的：搜「海屿你」会搜出一堆改编版。所以这里的判据很硬 ——
// 剥掉括号注释与连字符后缀之后必须**完全相等**才认（见 `sameTitle`），不做模糊包含。
// 匹配不上就回落，最坏结果是「音质没提升」，而不是「下错一首」。

// durationToleranceSec 是「同名曲目算不算同一版录音」的时长容差（秒）。
//
// 5 秒够宽：同一版录音在不同平台的报时差通常只有一两秒（前奏裁剪、报时取整）；
// 也够窄：现场版 / 重录版 / 不同专辑版动辄差十几秒到一分钟。
const durationToleranceSec = 5

// durationSeconds 把搜索层的时长统一压到秒。
//
// ⚠️ 各平台单位不统一（网易给毫秒、QQ 给秒），`> 3600` 一律当成毫秒没换算 ——
// 与 `trackFromSong`（pkg/intercept/search.go）同一条口径。
func durationSeconds(s search.UnifiedSong) int {
	d := s.Duration
	if d <= 0 {
		d = s.Interval
	}
	if d > 3600 {
		d /= 1000
	}
	return d
}

// durationMatches 判断候选与原曲目是不是**同一版录音**。
//
// 为什么要比时长：歌名 + 歌手相同但不是同一版录音的情况很常见 —— 不同专辑版、
// 重录版、以及**剥掉括号注释之后撞名**的现场版（`cleanTitle` 会把「甲 (Live)」
// 和「甲」判成同一首，那正是我们想要的，但也就更需要时长兜一道）。
//
// 任一方没有时长（0）时**不参与判定**：那是「平台没给这个字段」，不是「对不上」——
// 拿它当「对不上」会让所有不报时长的源都用不上。
func durationMatches(want, got int) bool {
	if want <= 0 || got <= 0 {
		return true
	}
	d := want - got
	if d < 0 {
		d = -d
	}
	return d <= durationToleranceSec
}

// qualityRank 把音质档位映射成可比较的等级。
//
// ⚠️ 只按**容器**分档，不猜码率：外挂源只告诉我们「这是 flac 还是 mp3」，
// 猜码率会在界面上显示成假信息（与 `pkg/search/provider.go` 的 `qualityFromExt`
// 同一条口径）。
func qualityRank(q string) int {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "无损", "flac", "ape", "wav", "dsd":
		return 3
	case "aac", "m4a":
		return 2
	case "320k", "320":
		return 1
	default:
		// 认不出的档位算最低档，但**不是负分** —— 它仍然是个能下的候选，
		// 只是比不过已知的 320k。全认不出时第一条胜出（见 pickBestQuality）。
		return 0
	}
}

// pickBestQuality 从候选里挑最值得下的那条。
//
// 规则（用户 2026-10-06 指令）：**无损优先，同档比体积**。
//
//   - 档位不同 → 档位高的赢；
//   - 档位相同 → **体积大的赢**（同一首歌、同一档位，文件更大 = 码率更高）；
//   - 体积未知（0）的一方**不主动占优** —— 它是「平台没给这个字段」，不是「更小」，
//     而外挂音源里缺这个字段是常态。所以「同档 + 双方都没体积」保持原顺序：
//     各源的排序本身带相关性（搜索层已按相关度排过），打乱它没有好处。
//
// 空候选返回 false。
func pickBestQuality(cands []search.UnifiedSong) (search.UnifiedSong, bool) {
	ranked := sortByPreference(cands)
	if len(ranked) == 0 {
		return search.UnifiedSong{}, false
	}
	return ranked[0], true
}

// sortByPreference 把候选按「更值得下」排好（规则见 betterCandidate）。
//
// 用 `SliceStable`：同档且体积也一样时**保持各源原本的顺序** —— 搜索层已经按相关度
// 排过，打乱它没有好处。
func sortByPreference(cands []search.UnifiedSong) []search.UnifiedSong {
	out := append([]search.UnifiedSong(nil), cands...)
	sort.SliceStable(out, func(a, b int) bool { return betterCandidate(out[a], out[b]) })
	return out
}

// betterCandidate 判断 b 是否比 best 更值得下（见 pickBestQuality 的规则）。
func betterCandidate(b, best search.UnifiedSong) bool {
	rb, rBest := qualityRank(b.Quality), qualityRank(best.Quality)
	if rb != rBest {
		return rb > rBest
	}
	return b.FileSize > best.FileSize
}

// cleanTitle 剥掉括号注释与连字符后缀，得到「干净的歌名」用于跨源匹配。
//
// 直接移植参考实现的 `_clean_track_title`（`/tmp/fnme-z/proxy/app.py:4845`）：
// 剥掉 `(feat. xxx)` / `[xxx]` / `（xxx）` / `【xxx】`，以及 ` - xxx` 后缀。
// 不剥的话「海屿你 (Live)」与「海屿你」会被判成两首歌，跨源匹配直接漏掉 ——
// 而那恰恰是最常见的情形（各平台对同一首歌的标题写法差别就在这些后缀上）。
func cleanTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}
	// 括号整段剥掉（半角/全角都算），支持嵌套
	var b strings.Builder
	depth := 0
	for _, r := range t {
		switch r {
		case '(', '[', '（', '【':
			depth++
			continue
		case ')', ']', '）', '】':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if i := strings.Index(out, " - "); i > 0 {
		out = strings.TrimSpace(out[:i])
	}
	return out
}

// normTitle 归一化歌名：剥后缀 + 去空白（含全角空格）+ 小写。
func normTitle(s string) string {
	c := cleanTitle(s)
	c = strings.ReplaceAll(c, " ", "")
	c = strings.ReplaceAll(c, "\u3000", "")
	return strings.ToLower(c)
}

// sameTitle 判断两个歌名是不是同一首。
//
// ⚠️ **只认完全相等**（归一化之后），不做模糊包含 —— 下载场景里「下错一首」比
// 「音质没提升」糟得多，而模糊包含会把「海屿你」和「海屿你心碎版」判成同一首。
func sameTitle(a, b string) bool {
	na, nb := normTitle(a), normTitle(b)
	return na != "" && na == nb
}

// downloadSources 返回「下载源池」的平台列表（现取，见 Config.DownloadSources）。
func (i *Interceptor) downloadSources() []string {
	if i.cfg.DownloadSources == nil {
		return nil
	}
	out := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, p := range i.cfg.DownloadSources() {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// bestQualityTrack 在下载源池里找这首歌的最佳取音来源。
//
// 返回 false = 池里没有同名曲目（或池是空的）→ 调用方回落到曲目自己平台。
// 各源**并发**搜（串行跑三四个源就要十几秒，而这是下载路径上的等待）。
func (i *Interceptor) bestQualityTrack(ctx context.Context, title, artist string, wantSec int) (online.Track, bool) {
	list := i.rankedSources(ctx, title, artist, wantSec)
	if len(list) == 0 {
		return online.Track{}, false
	}
	return list[0], true
}

// rankedSources 把池里同名的候选**按优先级排成一列**，而不是只挑一条。
//
// 为什么要一整列：某一个源坏了（解析不出直链 / 下下来不是音频 / CDN 挂了）不该让
// 整首歌失败 —— 换下一个源再试。参考实现里对应「坏档换 mp3 重试」那一步。
//
// 返回空 = 池里没有同名同版录音（或池是空的）→ 调用方回落到曲目自己平台。
func (i *Interceptor) rankedSources(ctx context.Context, title, artist string, wantSec int) []online.Track {
	plats := i.downloadSources()
	keyword := strings.TrimSpace(title + " " + artist)
	// 池空 = musicdl 那批平台一个都没有、**而且**洛雪源也没接 —— 那就一次搜索都不发。
	// （洛雪没接时这个条件与改动前完全一致：len(plats)==0 就直接返回。）
	lxOn := i.lxSearch != nil
	if keyword == "" || (len(plats) == 0 && !lxOn) {
		return nil
	}

	results := make([][]search.UnifiedSong, len(plats))
	var (
		wg      sync.WaitGroup
		lxSongs []search.UnifiedSong
	)
	for idx, p := range plats {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			// 每个源只取前几首：跨源匹配要的是「有没有同名的高音质版本」，
			// 不是「把这个源的结果全捞回来」。
			results[idx] = i.searcher(keyword, p, 1, 5)
		}(idx, p)
	}
	// 洛雪源与平台源**并发**问：它只是池里的另一条来源，没理由串在平台后面多等一轮
	//（这一层在下载路径上，等待就是用户体验）。它自己那点失败在 lxCandidates 里
	// 就吞掉了，不会影响平台源的结论。
	if lxOn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lxSongs = i.lxCandidates(ctx, keyword)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}

	// 过筛的判据只有**这一份**：洛雪候选与平台候选共用它，池里不该有两套匹配语义。
	accept := func(s search.UnifiedSong) bool {
		return sameTitle(s.Name, title) &&
			// 同名还不够：还要是**同一版录音**（时长对得上）
			durationMatches(wantSec, durationSeconds(s))
	}

	var matched []search.UnifiedSong
	for _, songs := range results {
		for _, s := range songs {
			if accept(s) {
				matched = append(matched, s)
			}
		}
	}
	// 洛雪那批（声明了 musicSearch 的脚本给的）与平台候选一起排序、一起挑。
	for _, s := range lxSongs {
		if accept(s) {
			matched = append(matched, s)
		}
	}
	ranked := sortByPreference(matched)
	out := make([]online.Track, 0, len(ranked))
	seen := make(map[string]bool, len(ranked))
	for _, pick := range ranked {
		t, ok := trackFromSong(pick.Source, pick)
		if !ok {
			continue
		}
		key := t.Platform + "/" + t.PlatformID
		if seen[key] {
			continue
		}
		seen[key] = true
		// ⚠️ trackFromSong 只搬 id / 标题 / 歌手 / 时长，**不带音质档位** —— 而调用方
		// 要拿它跟「曲目自己那份」比谁更好。不在这里补上，它恒为 ""，就永远比不上，
		// 换源永远不发生（用例 TestResolveForDownloadUpgrades 抓的就是这个）。
		t.Quality = pick.Quality
		out = append(out, t)
	}
	return out
}

// resolveForDownload 决定「这首歌从哪个源取音」并解析出直链。
//
// 顺序：**下载源池里同名的、且音质严格更高的那份** → 否则用曲目自己平台。
// 取音来源换了，但**曲目身份不变**（标题/歌手/专辑仍用原来那份写标签）——
// 用户收藏的是那首歌，不是某个平台的某条记录。
//
// ⚠️ 必须比「曲目自己那份」更好才换。真机实测（2026-10-06）：搜「晴天」时
// 网易云那边是 flac、咪咕那边只有 320k —— 只挑「池里最好的」会**把无损降成 320k**，
// 正好与「下载一律最高音质」相反。
// 返回的第二个值就是**实际取音**的那条（没换源时就是入参本身）—— 调用方要用它
// 如实记录「从哪儿、什么音质下来的」，不能拿原平台糊弄界面。
func (i *Interceptor) resolveForDownload(ctx context.Context, t online.Track) (*online.Resolved, online.Track, error) {
	if better, ok := i.bestQualityTrack(ctx, t.Title, t.Artist(), t.Duration); ok && qualityRank(better.Quality) > qualityRank(t.Quality) {
		i.logf("[INTERCEPT] 下载换源取更高音质：%s/%s（%s）→ %s/%s（%s）",
			t.Platform, t.PlatformID, t.Quality, better.Platform, better.PlatformID, better.Quality)
		t = better
	}
	res, err := i.pool.Resolve(ctx, t.Platform, t.PlatformID)
	return res, t, err
}
