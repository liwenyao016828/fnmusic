package intercept

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
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

// autoDownload 后台把一首在线曲目整轨下到本地并登记。
//
// 失败只记日志：用户点的是「收藏」，收藏本身已经成功了 —— 下载是附加动作，
// 它失败不该让收藏看起来失败。
func (i *Interceptor) autoDownload(fake string, t online.Track) {
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
			i.logf("[INTERCEPT] 收藏自动下载失败 %s（%s）— %s：%v",
				fake, t.Platform, t.Title, err)
			return
		}
		if err := i.downloaded.Put(item); err != nil {
			i.logf("[INTERCEPT] 已下载但登记失败 %s（%s）：%v", fake, item.Path, err)
			return
		}
		i.logf("[INTERCEPT] 收藏自动下载完成并已绑定本地 %q — %s → %s",
			t.Title, t.Artist(), item.Path)
	}()
}

// downloadToLibrary 走「解析 → 调本机下载接口 → 拿回落盘路径」这一条链路。
//
// 与 `handleQulvDownloadOnline` 共用同一套动作，只是调用方不同（那边是注入脚本
// 点按钮，这边是收藏后自动触发）。
// downloadCandidates 返回「值得试的取音来源」，按优先级排好，**最后一条永远是曲目自己平台**。
//
// 顺序：池里同名的候选（音质高 → 低，只有**严格更好**的才排在前面）→ 曲目自己平台。
//
// 为什么要一整列而不是只挑一条：某一个源坏了（解析不出直链、下下来不是音频、
// CDN 挂了）不该让整首歌失败 —— 换下一个源再试。参考实现里对应「坏档换 mp3 重试」。
func (i *Interceptor) downloadCandidates(ctx context.Context, t online.Track) []online.Track {
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
		if qualityRank(c.Quality) > qualityRank(t.Quality) {
			add(c)
		}
	}
	add(t) // 兜底：池里没有更好的、或者全试失败了，都用它
	return out
}

// downloadToLibrary 逐个候选试下载，成功即返回。
//
// 与 `handleQulvDownloadOnline` 共用同一套动作，只是调用方不同（那边是注入脚本
// 点按钮，这边是收藏后自动触发）。
func (i *Interceptor) downloadToLibrary(ctx context.Context, fake string, t online.Track) (online.DownloadedItem, error) {
	cands := i.downloadCandidates(ctx, t)
	var lastErr error
	for idx, cand := range cands {
		res, err := i.pool.Resolve(ctx, cand.Platform, cand.PlatformID)
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
		item, err := i.postDownload(ctx, fake, t, cand, res.URL)
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
func (i *Interceptor) postDownload(ctx context.Context, fake string, t, used online.Track, url string) (online.DownloadedItem, error) {
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
		// 如实记录实际取音来源（可能已被下载源池换过源）
		Source: used.Platform, Quality: used.Quality,
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
