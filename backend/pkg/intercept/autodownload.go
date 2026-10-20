package intercept

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

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
	if _, ok := i.downloaded.Get(fake); ok {
		return // 已经在本地了
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

// downloadToLibrary 逐个候选试下载，成功即返回。
//
// 与 `handleQulvDownloadOnline` 共用同一套动作，只是调用方不同（那边是注入脚本
// 点按钮，这边是收藏后自动触发）。
func (i *Interceptor) downloadToLibrary(ctx context.Context, fake string, t online.Track) (online.DownloadedItem, error) {
	tiers := i.downloadTiers()

	// 基线：曲目自己平台按档位阶梯解析出来的那一条。
	//
	// 这里只取直链、**不落盘**，作用有两个：
	//   1. 知道「这首歌本来能拿到多好」，池里的候选只有**真的更好**才换源
	//      （只看声明档位会在「自己平台能无损、池里那条只声明 320k」时把无损降成 320k）；
	//   2. 候选过滤也用它当门槛 —— 声明比基线低的候选连试都不必试。
	baseRes, baseErr := i.pool.ResolveLadder(ctx, t.Platform, t.PlatformID, tiers)
	baseScore := qualityTierRank(t.Quality)
	if baseErr == nil && baseRes != nil {
		if s := online.TierRank(baseRes.Tier); s > baseScore {
			baseScore = s
		}
	}

	cands := i.downloadCandidates(ctx, t, baseScore)
	// 「飞牛页面触发的下载」的音质偏好（榜单管理里的「音质下载」）：
	// 点名要某一档时，把正好是那一档的候选提到最前（见 preferExactTier）。
	if want := i.autoDownloadQuality(); want != qualityHighest {
		cands = preferExactTier(cands, want)
	}

	var lastErr error = baseErr
	for idx, cand := range cands {
		if cand.Platform == t.Platform && cand.PlatformID == t.PlatformID {
			// 曲目自己平台：复用上面那次解析，别重复问一遍平台。
			// 基线解析不出来时这条也没法兜底，直接跳过。
			if baseErr != nil || baseRes == nil {
				continue
			}
			item, err := i.postDownload(ctx, fake, t, cand, baseRes.URL, baseRes.Tier)
			if err != nil {
				lastErr = err
				if len(cands) > 1 {
					i.logf("[INTERCEPT] 自己平台下载失败：%s/%s %v", cand.Platform, cand.PlatformID, err)
				}
				continue
			}
			return item, nil
		}

		// 池里的候选：声明档位不比基线高就不换 —— 那是**降级**。
		// 用户要的是「各音源里最高的那一档」，不是「换个源」。
		if qualityTierRank(cand.Quality) <= baseScore {
			continue
		}
		res, err := i.pool.ResolveLadder(ctx, cand.Platform, cand.PlatformID, tiers)
		if err != nil {
			lastErr = err
			// 只有多个候选时才刷日志 —— 单候选（= 曲目自己平台）失败的情况，
			// 上层会把错误报给用户，这里再打一条只是噪音。
			if len(cands) > 1 {
				i.logf("[INTERCEPT] 取音源不可用（%d/%d）：%s/%s %v",
					idx+1, len(cands), cand.Platform, cand.PlatformID, err)
			}
			continue
		}
		// 声明归声明，实际拿到的档位才是真的 —— 拿到的比基线还差就不换，
		// 免得「为了追一个声明的无损，把实际的无损换成 mp3」。
		if s := online.TierRank(res.Tier); s >= 0 && s < baseScore {
			i.logf("[INTERCEPT] 候选实际只有 %q，比基线低，不换源：%s/%s",
				res.Tier, cand.Platform, cand.PlatformID)
			continue
		}
		item, err := i.postDownload(ctx, fake, t, cand, res.URL, res.Tier)
		if err != nil {
			lastErr = err
			if len(cands) > 1 {
				i.logf("[INTERCEPT] 取音源下载失败，换下一个（%d/%d）：%s/%s %v",
					idx+1, len(cands), cand.Platform, cand.PlatformID, err)
			}
			continue
		}
		if cand.Platform != t.Platform || cand.PlatformID != t.PlatformID {
			i.logf("[INTERCEPT] 下载换源取更高音质：%s/%s（%s）→ %s/%s（%s）",
				t.Platform, t.PlatformID, t.Quality, cand.Platform, cand.PlatformID, cand.Quality)
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
		// 档位优先用解析回读的实际值，没有才退回候选的声明值。
		Source: used.Platform, Quality: firstNonEmpty(actualTier, used.Quality),
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
