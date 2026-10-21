package intercept

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/online"
)

// 「收藏 / 加入歌单 → 自动下载并绑定本地」。
//
// # 参考实现与曲率的差别
//
// fnmusic-ext 的做法（`/tmp/fnme-z/proxy/app.py:4508` 的 `_register_fav_autobind`）：
// 登记绑定意图 → 已在库则补歌词并调度「官方绑定」→ 有滚动缓存就提升成正式文件 →
// 否则后台整轨下载；下完再把官方那条收藏**回写**成本地曲目。
//
// 曲率这边**不需要回写官方库**：在线曲目在官方库里根本不存在（它是一条我们造的
// 虚拟 guid），想回写也回写不了。所以「绑定」在这里的含义更简单也更强：
//
//	下完之后，那个虚拟 guid 的**取流与元数据直接服务本地文件**。
//
// 收藏条目继续可用，但已经在听硬盘上那份 —— 而且不依赖重扫、不依赖官方接口。
//
// # 为什么必须后台
//
// 下载一首无损是几十兆、几秒到几十秒。用户点红心时**不能**等它 —— 收藏是即时
// 反馈的操作。所以这里起一个 goroutine，收藏请求立刻返回。

// favAutoDownloadOn 读开关（nil = 关）。
func (i *Interceptor) favAutoDownloadOn() bool {
	return i.favAutoDownload != nil && i.favAutoDownload()
}

// markDownloading 保证「同一首歌只下一次」。
//
// 收藏与「加入歌单」可能几乎同时触发（用户手快），而两次下载会把同一个文件写两遍
// —— 更糟的是两个 goroutine 同时往同一个路径写。返回 false = 已经有人在下了。
func (i *Interceptor) markDownloading(fake string) bool {
	i.dlMu.Lock()
	defer i.dlMu.Unlock()
	if i.dlSet == nil {
		i.dlSet = map[string]bool{}
	}
	if i.dlSet[fake] {
		return false
	}
	i.dlSet[fake] = true
	return true
}

func (i *Interceptor) unmarkDownloading(fake string) {
	i.dlMu.Lock()
	delete(i.dlSet, fake)
	i.dlMu.Unlock()
}

// 自动下载的触发来源。真身在 pkg/online（它们是记录表 schema 的一部分，
// 读写两侧必须同一套字符串），这里只是本地别名，省掉每处调用点的跨包前缀。
const (
	TriggerFavorite  = online.TriggerFavorite  // 在飞牛音乐里点了收藏
	TriggerPlaylist  = online.TriggerPlaylist  // 在飞牛音乐里加进了歌单
	TriggerPageClick = online.TriggerPageClick // 在飞牛页面里点了「下载到曲率」
)

// autoDownload 后台把一首在线曲目整轨下到本地并登记。
//
// 失败只记日志：用户点的是「收藏」，收藏本身已经成功了 —— 下载是附加动作，
// 它失败不该让收藏看起来失败。
//
// 但**记录要记全**（成功与失败都记，见 pkg/online/autolog.go）：失败只写日志的话，
// 用户在界面上看不到任何痕迹，只能去翻后端日志 —— 而那正是用户报的问题。
func (i *Interceptor) autoDownload(fake string, t online.Track, trigger string) {
	if !i.favAutoDownloadOn() {
		return
	}
	if i.downloaded == nil || i.localAPI == "" {
		return
	}
	if item, ok := i.downloaded.Get(fake); ok {
		// 已经在本地了 —— 但如果本地那份是**有损**、而这次能拿无损，允许重下一次
		// 把它换掉（见 shouldRetryUpgrade）。只试一次，失败就不再纠缠。
		if !i.shouldRetryUpgrade(item) {
			return
		}
		item.UpgradeTried = true
		item.At = time.Now().Unix()
		if err := i.downloaded.Put(item); err != nil {
			i.logf("[INTERCEPT] 标记音质升级尝试失败 %s：%v", fake, err)
		}
	}
	if !i.markDownloading(fake) {
		return
	}

	go func() {
		defer i.unmarkDownloading(fake)

		// 给足时间：无损一首几十兆，解析 + 抓取 + 写标签 + 落盘。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		item, err := i.downloadToLibrary(ctx, fake, t)
		if err != nil {
			i.logf("[INTERCEPT] %s 自动下载失败 %s（%s）— %s：%v",
				triggerLabel(trigger), fake, t.Platform, t.Title, err)
			i.recordAutoDownload(online.AutoLogEntry{
				Trigger: trigger, GUID: fake,
				Title: t.Title, Artist: t.Artist(),
				OK: false, Error: err.Error(),
			})
			return
		}
		if err := i.downloaded.Put(item); err != nil {
			i.logf("[INTERCEPT] 已下载但登记失败 %s（%s）：%v", fake, item.Path, err)
			i.recordAutoDownload(online.AutoLogEntry{
				Trigger: trigger, GUID: fake,
				Title: t.Title, Artist: t.Artist(),
				Source: item.Source, Quality: item.Quality, Size: item.Size, Path: item.Path,
				OK: false, Error: "文件已落盘但登记失败：" + err.Error(),
			})
			return
		}
		i.logf("[INTERCEPT] %s 自动下载完成并已绑定本地 %q — %s → %s",
			triggerLabel(trigger), t.Title, t.Artist(), item.Path)
		i.recordAutoDownload(online.AutoLogEntry{
			Trigger: trigger, GUID: fake,
			Title: t.Title, Artist: t.Artist(),
			Source: item.Source, Quality: item.Quality, Size: item.Size, Path: item.Path,
			OK: true,
		})
	}()
}

// triggerLabel 把触发来源翻成人话（只用于日志；界面用同一个词表）。
func triggerLabel(trigger string) string {
	switch trigger {
	case TriggerFavorite:
		return "收藏"
	case TriggerPlaylist:
		return "加歌单"
	case TriggerPageClick:
		return "页面下载"
	}
	return "自动"
}

// recordAutoDownload 往外推一条自动下载记录。注入口没接上就静默跳过 ——
// 记录是附加产物，不该因为它把下载路径弄失败。
func (i *Interceptor) recordAutoDownload(e online.AutoLogEntry) {
	if i.cfg.AutoDownloadLog == nil {
		return
	}
	i.cfg.AutoDownloadLog(e)
}

// autoDownloadQuality 读「飞牛页面触发的下载」要的音质档位，归一化。
//
// ⚠️ 空 / 认不出一律当 `highest`（现在的行为）：这个键是后加的，老配置里没有它，
// 把「没设置」当成「固定 128k」会让老用户新下的歌突然变糊 —— 那种退步
// 只会在听的时候被发现。
func (i *Interceptor) autoDownloadQuality() string {
	if i.cfg.AutoDownloadQuality == nil {
		return qualityHighest
	}
	switch strings.ToLower(strings.TrimSpace(i.cfg.AutoDownloadQuality())) {
	case online.TierLossless:
		return online.TierLossless
	case online.Tier320k:
		return online.Tier320k
	case online.Tier192k:
		return online.Tier192k
	case online.Tier128k:
		return online.Tier128k
	}
	return qualityHighest
}

// qualityHighest 是「能无损就无损」的哨兵值：走下载源池原本的排序（最高在前）。
const qualityHighest = "highest"

// preferExactTier 把「正好是目标档位」的候选提到前面，其余保持原顺序。
//
// 为什么是「提前」而不是「过滤掉」：用户点名要 128k，池里只有 320k 时，
// 下到 320k 也比「收藏了一首歌却下不下来」好 —— 实际档位会如实写进自动下载记录
// （含源与档位），用户看得见「要的是 128k、拿到的是 320k」。
// 过滤掉就等于把「一个音质选择」变成了「一个失败」。
//
// 排序用 SliceStable：同档内部保持下载源池原来的顺序（那本身带相关性，打乱没好处）。
func preferExactTier(cands []online.Track, want string) []online.Track {
	if want == "" || want == qualityHighest {
		return cands
	}
	wantRank := online.TierRank(want)
	if wantRank < 0 {
		return cands
	}
	out := append([]online.Track(nil), cands...)
	sort.SliceStable(out, func(a, b int) bool {
		ra := online.TierRank(out[a].Quality) == wantRank
		rb := online.TierRank(out[b].Quality) == wantRank
		if ra == rb {
			return false
		}
		return ra
	})
	return out
}

// downloadToLibrary 走「解析 → 调本机下载接口 → 拿回落盘路径」这一条链路。
//
// downloadCandidates 返回「值得试的取音来源」，按优先级排好，**最后一条永远是曲目自己平台**。
//
// 顺序：池里同名的候选（音质高 → 低，只有**严格好于基线**的才排在前面）→ 曲目自己平台。
//
// `minScore` 是**基线**的档位分（`online.TierRank` 标度），由调用方按「曲目自己平台
// 实际解析出来的档位」算好传进来。为什么不用 `t.Quality` 声明值：那只是搜索时平台
// 报的**可用档位**，不是这次真能拿到的 —— 网易云对收费曲目会静默降到 standard，
// 声明 flac 而实际 128k 是常态。按下限过滤会让池里的 320k 候选连试都试不到。
func (i *Interceptor) downloadCandidates(ctx context.Context, t online.Track, minScore int) []online.Track {
	out := make([]online.Track, 0, 4)
	seen := make(map[string]bool, 4)
	add := func(x online.Track) {
		if x.Platform == "" || x.PlatformID == "" {
			return
		}
		key := x.Platform + "/" + x.PlatformID
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, x)
	}
	for _, c := range i.rankedSources(ctx, t.Title, t.Artist(), t.Duration) {
		if qualityTierRank(c.Quality) > minScore {
			add(c)
		}
	}
	add(t) // 兜底：池里没有更好的、或者全试失败了，都用它
	return out
}

// downloadTiers 返回「下载取音」要按哪几档去问平台（高 → 低）。
//
// # 为什么必须按档位问
//
// 以前下载这条路调的是 `Pool.Resolve` —— 档位由平台自己决定。网易云那条免登录接口
// 的普通解析固定 `br=320000`，于是**永远只能拿到 320k mp3**；而同一首歌试听时走的是
// `ResolveLadder`，能拿到 flac。用户 2026-10-20 报的「收藏自动下载怎么下的是 mp3」
// 就是这么来的：同一首歌，听是无损、下是 mp3。
//
// 现在下载与试听共用同一条阶梯（`online.PlaybackLadder`：flac → 320k → 128k）。
//
// # 点名某一档时
//
// 先问那一档，再退到它**下面**的档（下到 320k 也好过下不来），但**不会往上问**：
// 用户选 128k 是为了省空间，给他 flac 是违背选择。
func (i *Interceptor) downloadTiers() []string {
	want := i.autoDownloadQuality()
	if want == qualityHighest {
		return online.PlaybackLadder
	}
	wantRank := online.TierRank(want)
	out := make([]string, 0, len(online.PlaybackLadder))
	out = append(out, want)
	for _, t := range online.PlaybackLadder {
		if online.TierRank(t) < wantRank {
			out = append(out, t)
		}
	}
	return out
}

// tierFromLandedFile 用落盘文件的扩展名校正档位。
//
// 落盘时扩展名是**魔数嗅探**出来的（见 pkg/downloader 的 detectAudioExtByMagic），
// 所以它比「平台声明」「脚本承诺」都硬。两个方向都要校：
//
//   - 声明无损、落成 mp3 → 就是有损（第三方 API 把 flac 请求压成了 320k，很常见）；
//   - 声明 320k、落成 flac → 就是无损（别把好东西记成坏的，用户会以为没生效）。
//
// 认不出的扩展名（空 / 无扩展名）一律不动：那是「不知道」，不是「有损」。
func tierFromLandedFile(path, claimed string) string {
	if strings.TrimSpace(path) == "" {
		return claimed
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	lossless := online.TierRank(claimed) >= online.TierRank(online.TierLossless)
	switch ext {
	case "flac", "ape", "wav", "dsf", "dff":
		if !lossless {
			return online.TierLossless
		}
		return claimed
	case "mp3", "m4a", "aac", "ogg", "wma":
		if lossless {
			return online.Tier320k
		}
	}
	return claimed
}

// shouldRetryUpgrade 判断「已经下载过的这条曲目」要不要为了音质再下一次。
//
// 只有同时满足下面几条才会重下（任何一条不满足都保持「已下载」不动）：
//
//  1. 还没试过升级（UpgradeTried=false）—— 平台不给无损时别每次都白下一遍；
//  2. 记录里的档位不是无损；
//  3. 盘上那份文件还在，且不是无损容器（用户可能已经手动换成 flac 了）；
//  4. 当前的下载档位偏好确实想要无损（用户点名 320k 时别逆着他的选择）。
func (i *Interceptor) shouldRetryUpgrade(item online.DownloadedItem) bool {
	if item.UpgradeTried || item.Path == "" {
		return false
	}
	if online.TierRank(item.Quality) >= online.TierRank(online.TierLossless) {
		return false
	}
	switch strings.ToLower(filepath.Ext(item.Path)) {
	case ".flac", ".ape", ".wav", ".dsf", ".dff":
		return false
	}
	if _, err := os.Stat(item.Path); err != nil {
		// 文件没了（用户删了/移走了）——那不是「升级」，是「重新下载」，
		// 但记录还在会让取流指向不存在的文件。这一条留给上层处理，这里不动。
		return false
	}
	tiers := i.downloadTiers()
	return len(tiers) > 0 && online.TierRank(tiers[0]) >= online.TierRank(online.TierLossless)
}

// ── 渠道化的取音计划 ──────────────────────────────────────────────────────
//
// 用户 2026-10-21 的两句话要合起来读才是完整要求（m02588）：
//   · 「增加一个自动下载源（方式：lx，网易云，dl）」→ 三条路要能选、能排序；
//   · 「收藏自动下载不是需要立刻下载的场景，优先最高音质，慢一点没关系」→ 允许慢。
//
// 这里原来的形状是「先问原生拿基线，基线够看就不再问别人」。而免登录网易云对收费
// 曲目恒回 320k（实测要 lossless 给 standard/128k），于是**每一条收藏都停在 320k**：
// 洛雪那条常常真能给无损的路只被叫一次，外挂池又只收「声明比基线严格更好」的候选。
// 现在改成按**用户给的渠道顺序**逐条探测，把每条渠道**实际**拿到的档位收进一张表，
// 最后按实际档位排序、并列时按渠道顺序 —— 多花几秒，拿到的是各途径里真最高的那一档。
//
// 三条不变量一个字没改（v2.13.0 定下的口径）：
//  1. 判断好坏只看**实际**档位（`Resolved.Tier`，落盘后再用魔数校一次），不看声明值；
//  2. 不比已经拿到的更好就不换 —— 换源不是目的，升音质才是；
//  3. 一次收藏**只落盘一份**：探测阶段一个字节都不写盘，赢家才开始下（失败才换下一条）。

// 渠道串的取值表在 `pkg/config/channels.go`（那些串同时是配置值与接口值），
// 这里只是起本地别名，省掉调用点上的包前缀。
const (
	dlChanLx      = config.DLChannelLx
	dlChanMusicDL = config.DLChannelMusicDL
	dlChanNative  = config.DLChannelNative
)

// dlProbePerChannel 一条渠道最多探几条候选。
//
// 上限存在的理由只有时间：musicdl 解析一条要 1–3 秒，洛雪一条失败的脚本能拖到
// 8 秒 × 脚本数。收藏是后台动作、用户认了慢，但**没有上限**会把队列堵死。
const dlProbePerChannel = 3

// dlAttempt 一次取音探测的完整记录：哪条渠道、哪条候选、实际拿到什么档位。
type dlAttempt struct {
	Channel string
	Cand    online.Track
	Res     *online.Resolved
	Score   int // 实际档位分（拿不到实际档位时才是声明分）
	Rank    int // 该渠道在用户顺序里的位次（并列时打破平局，小的赢）
}

// Tier 这次探测实际拿到的档位；宿主没回读时是候选自己的声明值。
func (a dlAttempt) Tier() string {
	if a.Res != nil && strings.TrimSpace(a.Res.Tier) != "" {
		return a.Res.Tier
	}
	return a.Cand.Quality
}

// dlAttemptScore 给一次探测打分：**优先用实际档位**，实际档位读不出来才退回声明值。
//
// 为什么两个都要：老解析器（musicdl / 洛雪）不回读档位，落盘明明可能是 flac 却报空；
// 而搜索结果又常常只有一句「无损」当声明。只看声明会让「声明无损、实际 128k」的源
// 插队（这正是 v2.13.0 之前那批 mp3 的来源），只看实际又会把不报档位的源一律垫底。
func dlAttemptScore(res *online.Resolved, declared string) int {
	if res != nil {
		if s := online.TierRank(res.Tier); s >= 0 {
			return s
		}
	}
	return qualityTierRank(declared)
}

// dlTiebreak 同档位时谁赢：**原生 0 < 外挂池 1 < 洛雪 2**。
//
// 为什么不直接照用户排的渠道顺序打破平局：用户排「lx 在最前」的意图是
// 「去别处找更高的档位」，不是「同样是 320k，我要第三方那一条」。同档位时
// 官方平台那条更可信（封面/标签齐全、不容易死链），换源本身还是失败面 ——
// v2.13.0 的「同档不换」口径就是为此定的，这里必须继续成立。
func dlTiebreak(ch string) int {
	switch ch {
	case dlChanNative:
		return 0
	case dlChanMusicDL:
		return 1
	default:
		return 2
	}
}

// downloadChannels 生效的取音渠道顺序（没配 = 默认三条全开，洛雪在最前）。
func (i *Interceptor) downloadChannels() []string {
	if i.cfg.AutoDownloadChannels == nil {
		return append([]string(nil), config.DefaultDownloadChannels...)
	}
	return config.NormalizeDownloadChannels(i.cfg.AutoDownloadChannels())
}

// channelOfCandidate 池里的一条候选属于哪条渠道。
//
// `lx:` 前缀是我们给「搜得到、但曲率没有自家解析器」的平台（酷狗 / 酷我）改道出来的
// （见 lxsource.go 的 `lxSourcePrefix`），所以它算洛雪渠道，不算原生。
// 剩下的短码里，在 `download_sources`（= musicdl 启用的那几家）里的算外挂池，
// 其余按原生平台处理。
func channelOfCandidate(platform string, dlSources []string) string {
	if strings.HasPrefix(platform, lxSourcePrefix) {
		return dlChanLx
	}
	p := strings.ToLower(strings.TrimSpace(platform))
	for _, s := range dlSources {
		if s == p {
			return dlChanMusicDL
		}
	}
	return dlChanNative
}

// lxProbe 用「同一个平台 id」向洛雪脚本要直链，**只解析不落盘**。
//
// 与老的 `downloadViaLx` 的区别就是这一句：老函数一边解析一边下载，于是
// 「洛雪先给了个 320k」会当场把 320k 下完，后面那条真能出无损的外挂池再没机会。
// 拆成纯探测之后，所有渠道的结果才排在同一张表里比高低。
//
// 为什么这条路值得单独存在：脚本按 `getSongId = hash ‖ songmid ‖ id` 取曲目，
// **不需要脚本自己会搜**（实测用户装的 6 个脚本里只有 1 个声明了 musicSearch）。
func (i *Interceptor) lxProbe(ctx context.Context, t online.Track, tiers []string) (*online.Resolved, error) {
	plat := strings.ToLower(strings.TrimSpace(t.Platform))
	if plat == "" || strings.HasPrefix(plat, lxSourcePrefix) || t.PlatformID == "" {
		return nil, online.ErrNoResolver
	}
	// 先把「这条曲目的 id 长什么样」交给洛雪（脚本按 hash ‖ songmid ‖ id 取，三个都填）。
	i.rememberLxItem(plat, t.PlatformID, map[string]any{
		"id":        t.PlatformID,
		"songmid":   t.PlatformID,
		"hash":      t.PlatformID,
		"name":      t.Title,
		"singer":    t.Artist(),
		"albumName": t.Album,
		"album":     t.Album,
		"duration":  t.Duration,
	})
	i.ensureLxResolver(plat)
	return i.pool.ResolveLadder(ctx, lxSourcePrefix+plat, t.PlatformID, tiers)
}

// lxTrackOf 把「洛雪用同一个 id 解析出来的那条」表示成一个候选身份。
//
// 登记表要用它（`lx:wy` 这种），界面才知道这首歌其实是从洛雪脚本下来的。
func lxTrackOf(t online.Track, tier string) online.Track {
	return online.Track{
		Platform:   lxSourcePrefix + strings.ToLower(strings.TrimSpace(t.Platform)),
		PlatformID: t.PlatformID,
		Title:      t.Title,
		Artists:    t.Artists,
		Album:      t.Album,
		Duration:   t.Duration,
		Quality:    tier,
	}
}

// collectDownloadAttempts 按渠道顺序逐条探测，返回**排好序**的取音计划（最好的在最前）。
//
// 排序键：实际档位降序 → 渠道位次升序（并列时用户排前的渠道赢）。
// 探测途中一旦拿到「用户允许的顶」（`tiers[0]`，highest 就是无损）立刻收工 ——
// 已经不可能更好了，再问别的渠道只是白等。
func (i *Interceptor) collectDownloadAttempts(ctx context.Context, t online.Track, tiers []string) []dlAttempt {
	channels := i.downloadChannels()
	if len(tiers) == 0 || len(channels) == 0 {
		return nil
	}
	dlSources := i.downloadSources()
	ceiling := online.TierRank(tiers[0])
	losslessRank := online.TierRank(online.TierLossless)

	out := make([]dlAttempt, 0, 6)
	best := -1
	push := func(ch string, cand online.Track, res *online.Resolved) {
		rank := config.ChannelRank(channels, ch)
		if rank < 0 || res == nil {
			return
		}
		s := dlAttemptScore(res, cand.Quality)
		out = append(out, dlAttempt{Channel: ch, Cand: cand, Res: res, Score: s, Rank: rank})
		if s > best {
			best = s
		}
	}

	// 池里的候选：**声明值只用来决定「先探谁」**，不参与最后谁赢的判断。
	// （声明值为什么不能当判据：musicdl 的 mp3 一律被标成 320k，而洛雪脚本报的
	// 「无损」实测有四成是 128k 的转码 —— v2.13.0 那轮就是这么定的。）
	cands := i.rankedSources(ctx, t.Title, t.Artist(), t.Duration)
	sort.SliceStable(cands, func(a, b int) bool {
		return qualityTierRank(cands[a].Quality) > qualityTierRank(cands[b].Quality)
	})
	probePool := func(want string) {
		n := 0
		for _, c := range cands {
			if n >= dlProbePerChannel {
				return
			}
			if channelOfCandidate(c.Platform, dlSources) != want {
				continue
			}
			n++
			res, err := i.pool.ResolveLadder(ctx, c.Platform, c.PlatformID, tiers)
			if err != nil {
				i.logf("[INTERCEPT] %s 渠道候选解析失败：%s/%s %v", want, c.Platform, c.PlatformID, err)
				continue
			}
			push(want, c, res)
			if best >= ceiling {
				return
			}
		}
	}

	// 原生排第一个探（即使用户把它排在后面）：一次调用就拿到「不降级」的参照，
	// 而且**兜底候选永远在表里** —— 别的渠道给再高的档，链接也可能是死链，
	// 下不动时还得回落曲目自己平台那条（用例 `TestDownloadToLibraryRetriesNextCandidate`
	// 钉的就是这个形状）。用户关掉原生则不探。
	order := make([]string, 0, len(channels))
	if config.ChannelEnabled(channels, dlChanNative) {
		order = append(order, dlChanNative)
	}
	for _, ch := range channels {
		if ch != dlChanNative {
			order = append(order, ch)
		}
	}

	for _, ch := range order {
		if ctx.Err() != nil {
			break
		}
		switch ch {
		case dlChanNative:
			// 原生 = 曲目自己平台按档位阶梯解析（网易云 / QQ / 酷狗 …）。
			res, err := i.pool.ResolveLadder(ctx, t.Platform, t.PlatformID, tiers)
			if err != nil {
				if ctx.Err() == nil {
					i.logf("[INTERCEPT] 原生解析拿不到：%s/%s %v", t.Platform, t.PlatformID, err)
				}
				continue
			}
			push(dlChanNative, t, res)

		case dlChanLx:
			// (a) 同一个平台 id 直接问洛雪（不需要脚本会搜）。
			//
			// ⚠️ 只在「用户允许的顶 ≥ 无损」时问：点名 320k / 128k 是**省流量**，
			// 为一个省流量的选择去等洛雪几十秒是违背用户意图（这条是 v2.14.0 定的，
			// 用例 `TestDownloadToLibrarySkipsLxWhenUserAsksLowerTier` 盯着）。
			if i.lxResolve != nil && ceiling >= losslessRank && best < losslessRank {
				res, err := i.lxProbe(ctx, t, tiers)
				if err != nil {
					if ctx.Err() == nil {
						i.logf("[INTERCEPT] 洛雪取链失败（%s/%s）：%v", t.Platform, t.PlatformID, err)
					}
				} else {
					push(dlChanLx, lxTrackOf(t, res.Tier), res)
				}
			}
			// (b) 洛雪搜出来的同名候选（酷狗 / 酷我改道出来的 `lx:kg` 这类）。
			probePool(dlChanLx)

		case dlChanMusicDL:
			probePool(dlChanMusicDL)
		}
		if best >= ceiling {
			break // 到顶了，别的渠道再问也不可能更好
		}
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		ta, tb := dlTiebreak(out[a].Channel), dlTiebreak(out[b].Channel)
		if ta != tb {
			return ta < tb // 同档位不换源：原生 > 外挂池 > 洛雪
		}
		return out[a].Rank < out[b].Rank
	})
	return out
}

// channelTiers 把一次探测计划摊成「每条渠道各拿到什么档位」，用于界面展示。
//
// 「替换音质」那一页要回答的就是用户那句「当前歌曲，哪些下载源可以用什么音质下载」
// （m02848）—— 一张渠道 → 档位的表，比一长串候选直观点得多。
type channelTier struct {
	Channel string `json:"channel"`
	Tier    string `json:"tier"`
	Score   int    `json:"score"`
	// Platform / PlatformID 是这条渠道实际取音用的那一条（用户想知道「咪咕那条是哪个 id」）。
	Platform   string `json:"platform,omitempty"`
	PlatformID string `json:"platform_id,omitempty"`
}

func channelTiersOf(attempts []dlAttempt, channels []string) []channelTier {
	out := make([]channelTier, 0, len(channels))
	for _, ch := range channels {
		ct := channelTier{Channel: ch}
		for _, a := range attempts {
			if a.Channel != ch {
				continue
			}
			if online.TierRank(a.Tier()) > online.TierRank(ct.Tier) || ct.Tier == "" {
				ct.Tier = a.Tier()
				ct.Score = a.Score
				ct.Platform = a.Cand.Platform
				ct.PlatformID = a.Cand.PlatformID
			}
		}
		out = append(out, ct)
	}
	return out
}

// downloadToLibrary 按取音计划落盘：最好的先下，下不动才换下一条。
//
// 与 `handleQulvDownloadOnline` 共用同一套动作，只是调用方不同（那边是注入脚本
// 点按钮，这边是收藏后自动触发）。
func (i *Interceptor) downloadToLibrary(ctx context.Context, fake string, t online.Track) (online.DownloadedItem, error) {
	tiers := i.downloadTiers()
	tries := i.collectDownloadAttempts(ctx, t, tiers)
	if len(tries) == 0 {
		return online.DownloadedItem{}, online.ErrNotPlayable
	}

	var lastErr error
	for n, a := range tries {
		item, err := i.postDownload(ctx, fake, t, a.Cand, a.Res.URL, a.Tier())
		if err != nil {
			lastErr = err
			// 只有多条可试时才刷日志 —— 单条失败上层会把错误报给用户，这里再打一条只是噪音。
			if len(tries) > 1 {
				i.logf("[INTERCEPT] 第 %d/%d 条下不来（%s 渠道 %s/%s · %s），换下一条：%v",
					n+1, len(tries), a.Channel, a.Cand.Platform, a.Cand.PlatformID, a.Tier(), err)
			}
			continue
		}
		if a.Channel != dlChanNative {
			i.logf("[INTERCEPT] 取音走 %s 渠道：%s/%s → %s/%s（%s）",
				a.Channel, t.Platform, t.PlatformID, a.Cand.Platform, a.Cand.PlatformID, a.Tier())
		}
		return item, nil
	}
	if lastErr == nil {
		lastErr = online.ErrNotPlayable
	}
	return online.DownloadedItem{}, lastErr
}

// postDownload 把解析好的直链交给本机下载接口落盘。
//
// `used` 是**实际取音**的那条（可能已被下载源池换过源），登记表要如实记它 ——
// 界面拿它显示「从哪儿、什么音质下来的」。标签仍用**原曲目** `t` 的元数据。
//
// `actualTier` 是解析结果回读出来的**实际档位**（`Resolved.Tier`）。非空就优先用它：
// 候选的 `Quality` 是**搜索时声明的**，平台可能静默降级（要 lossless 给 320k），
// 拿声明当实际会在登记表里写「无损」而落盘是 mp3 —— 那是最难查的一类偏差。
func (i *Interceptor) postDownload(ctx context.Context, fake string, t, used online.Track, url, actualTier string) (online.DownloadedItem, error) {
	payload := map[string]any{
		"name":     t.Title,
		"singer":   t.Artist(),
		"album":    t.Album,
		"cover":    t.CoverURL,
		"source":   t.Platform,
		"songmid":  t.PlatformID,
		"url":      url,
		"duration": t.Duration,
		// 自动下载的语义是「按最高音质入库」：库里已经有一份 320k mp3 时，
		// 这次拿到无损就该把它换掉，而不是回一句「已存在」把无损挡在门外。
		"upgrade_if_better": true,
	}
	for k, v := range i.downloadPrefs() {
		payload[k] = v
	}
	out, status, err := i.postLocalAPI(ctx, "/api/download/song", payload)
	if err != nil {
		return online.DownloadedItem{}, err
	}

	// 响应形状（`pkg/downloader.HandleDownloadSong`）：`{code,message,data:{path,…}}`。
	// ⚠️ 只有 `code == 200` 且拿到 path 才算成功 —— `already_exists` 也算成功
	// （文件本来就在），所以不按 status 字符串判，只按「有没有 path」判。
	var body struct {
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
			Path   string `json:"path"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return online.DownloadedItem{}, err
	}
	if body.Data.Path == "" {
		msg := body.Data.Error
		if msg == "" {
			msg = strings.TrimSpace(string(out))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return online.DownloadedItem{}, &downloadError{status: status, msg: msg}
	}

	// 落盘字节数：登记表拿它做排障与显示（此前一直是空的）。下载接口的应答里
	// **没有**大小字段，所以自己 stat 一次 —— 文件刚写完，代价可忽略。
	// stat 失败不算失败：大小只用于显示，不能因为它让一首已经下好的歌算失败。
	var size int64
	if fi, err := os.Stat(body.Data.Path); err == nil {
		size = fi.Size()
	}

	return online.DownloadedItem{
		GUID:   fake,
		Path:   body.Data.Path,
		Title:  t.Title,
		Artist: t.Artist(),
		Size:   size,
		At:     time.Now().Unix(),
		// 如实记录实际取音来源（可能已被下载源池换过源）。
		// 档位优先用解析回读的实际值，没有才退回候选的声明值；再用**落盘文件的
		// 扩展名**校正一次 —— 扩展名是魔数嗅探出来的，比任何声明都硬。
		Source: used.Platform, Quality: tierFromLandedFile(body.Data.Path, firstNonEmpty(actualTier, used.Quality)),
	}, nil
}

type downloadError struct {
	status int
	msg    string
}

func (e *downloadError) Error() string {
	if e.msg == "" {
		return "下载接口未返回文件路径"
	}
	return e.msg
}

// fileExists 只判「在不在」—— 不判大小/可读：绑定这一层不该替用户判断
// 「这个文件还算不算数」，那是下载与清理那两处的事。
func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// serveDownloadedIfAny 是「绑定」的落地点：这个虚拟 guid 已经落到本地了就直接
// 服务本地文件（带 Range），不再代理在线流。
//
// 返回 true = 已经应答（调用方不要再往下走）。
func (i *Interceptor) serveDownloadedIfAny(w http.ResponseWriter, r *http.Request, fake string) bool {
	if i.downloaded == nil || fake == "" {
		return false
	}
	item, ok := i.downloaded.Get(fake)
	if !ok || item.Path == "" {
		return false
	}
	// 文件被删了（用户手动清理 / 磁盘操作）→ 摘掉登记并回落到在线流。
	// 不摘的话这里会一直 404，而「明明能在线听」这件事很难让用户想到。
	if !fileExists(item.Path) {
		_ = i.downloaded.Remove(fake)
		i.logf("[INTERCEPT] 已下载登记的文件不在了，回落到在线流：%s", item.Path)
		return false
	}
	i.logf("[INTERCEPT] 服务本地文件 %s → %s", fake, item.Path)
	// http.ServeFile 自带 Range / If-Modified-Since / Content-Type 推断 —— 官方
	// 客户端的拖动进度条靠的就是 Range，自己写一遍容易漏。
	http.ServeFile(w, r, item.Path)
	return true
}

// downloadPrefs 把「下载时要不要带歌词 / 内嵌封面」翻译成下载接口认识的字段。
//
// ⚠️ 两个开关**缺省都是开**（见 pkg/config 的 LyricAutoDownloadOn / CoverEmbedOn）：
// 老配置里没有这两个键，而「下下来的歌没歌词 / 没封面」是明显的退步。
// 所以这里只在「明确关掉」时才往载荷里放字段：
//   - 封面：下载接口把「字段缺失」当作「要」（SongPayload.EmbedCover 是 *bool），
//     所以关掉才需要显式传 false；
//   - 歌词：下载接口把「字段缺失」当作「不要」，所以开启才需要显式传 true。
//
// 收藏 / 加入歌单触发的自动下载与注入菜单里的手动下载走的是同一个方法 ——
// 同一件事在两条路径上不该有两套默认值。
func (i *Interceptor) downloadPrefs() map[string]any {
	out := map[string]any{}
	if i.cfg.LyricAutoDownload == nil || i.cfg.LyricAutoDownload() {
		// 内嵌（让飞牛自己显示）与外挂同名 .lrc（给别的播放器）都要
		out["embed_lyric"] = true
		out["write_lrc"] = true
	}
	if i.cfg.CoverEmbed != nil && !i.cfg.CoverEmbed() {
		out["embed_cover"] = false
	}
	return out
}
