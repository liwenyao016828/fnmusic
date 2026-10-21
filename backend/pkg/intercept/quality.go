package intercept

// 「替换音质」：扫曲库 → 看每首歌在每条取音渠道**实际**能拿到什么档位 → 用最高档位
// 把现在的低音质文件换掉。
//
// # 为什么要有它
//
// 自动下载的补强轮（见 upgrade_sweeper.go）只敢对**自己下过**的歌动手，而且一次
// 最多三首、失败要等一天。用户真正面对的是「我这一整库 mp3，哪些其实能换成无损」——
// 那需要一个**看得见、点得动**的入口：先如实列出来，再由用户决定换哪些。
//
// # 与自动下载的分工（口径必须一致，别在这里另立一套）
//
// 取音计划、渠道顺序、档位判据全部复用自动下载那套（`collectDownloadAttempts` /
// `channelTiersOf` / `downloadTiers`），落盘也走同一条 `postDownload` 通道 ——
// 唯一的区别是「谁来按下按钮」：那边是收藏事件，这边是用户。
//
// # 扫描为什么是异步的
//
// 一首歌要问 2–3 条渠道（洛雪一条失败要 8 秒 × 脚本数），几百首就是十几分钟。
// 同步接口早超时了，所以这里是「起一个任务 + 轮询状态」，与 musicdl 试运行同一个
// 形状（见 pkg/api/musicdl.go 的 SetMusicDLPreview）。

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/online"
)

const (
	// 状态取值（与前端契约一致，改这里要同步改 frontend/src/api/client.js 的注释）。
	QualityStateIdle    = "idle"
	QualityStateRunning = "running"
	QualityStateDone    = "done"
	QualityStateFailed  = "failed"

	// 扫描并发度：探测是「等外网」的重活，4 条并行已经能把上传带宽之外的等待盖住；
	// 再高会让 musicdl/洛雪侧排队，反而更慢（洛雪宿主是单进程串行处理请求的）。
	qualityScanWorkers = 4
	// 内存里的结果上限：界面一屏也放不下更多，超出只计数不保留明细（`truncated`）。
	qualityScanMaxEntries = 500
	// 用户输入的 limit 上限：防止一次扫到几十万首把整机拖住。
	qualityScanMaxLimit = 2000
)

// 无损容器。**按容器判**，不看文件名里有没有「无损」两个字 ——
// 真机见过 `xxx 无损.flac.mp3`（一个 mp3，名字里写着无损）。
var qualityLosslessExts = map[string]bool{
	".flac": true, ".ape": true, ".wav": true, ".dsf": true, ".dff": true,
}

// 认得出是音频的容器（其余文件跳过：封面、歌词、cue 都不该进扫描）。
var qualityAudioExts = func() map[string]bool {
	m := map[string]bool{".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wma": true}
	for k := range qualityLosslessExts {
		m[k] = true
	}
	return m
}()

// QualityEntry 曲库里的一首歌 + 各渠道实测结果。
type QualityEntry struct {
	Path        string        `json:"path"`
	Title       string        `json:"title"`
	Artist      string        `json:"artist"`
	Ext         string        `json:"ext"`
	CurrentTier string        `json:"current_tier"`
	Channels    []channelTier `json:"channels"`
	Best        *channelTier  `json:"best"`
	Upgradeable bool          `json:"upgradeable"`
}

// QualityStatus 扫描任务的状态（也是 /api/quality/scan 的 data）。
type QualityStatus struct {
	State       string         `json:"state"`
	Mode        string         `json:"mode"`
	Total       int            `json:"total"`
	Scanned     int            `json:"scanned"`
	Truncated   bool           `json:"truncated"`
	Upgradeable int            `json:"upgradeable"`
	Error       string         `json:"error"`
	Entries     []QualityEntry `json:"entries"`
}

type qualityScanState struct {
	mu      sync.Mutex
	status  QualityStatus
	stop    chan struct{}
	running bool
}

// qualityCurrentTier 曲库里这首歌「现在的档位」。**只能按容器判**：文件名与标签都
// 可能骗人（`无损.flac.mp3`），而读码率要解一整个文件头，扫描阶段不值得为它加一层。
// 代价是「320k 与 128k 的 mp3 都算 320k」—— 所以这里只用来回答「值不值得换」，
// 不用来向用户承诺精确码率。
func qualityCurrentTier(ext string) string {
	if qualityLosslessExts[strings.ToLower(ext)] {
		return online.TierLossless
	}
	return online.Tier320k
}

// qualityNameKey 归一化歌名/歌手，用于「搜出来的这条是不是这首歌」的判断。
//
// 只做保守归一：小写、去空白、去常见括号后缀与标点。**不做**繁简/别名映射 ——
// 猜错一次就会把别人的无损换进你的库，那是比「没扫到」更糟的结果。
func qualityNameKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "（("); i > 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff: // CJK：保留
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nativeTrackFor 在**原生平台**上搜一次这首歌，用于回答「原生渠道能拿到什么档位」。
//
// 曲库文件没有平台 id，所以这条渠道必须从搜索开始 —— 而搜索是模糊的，这里只接受
// 歌名归一化后完全相等的那条（宁可这一列空着，也不要拿别人那首的结果去比）。
func (i *Interceptor) nativeTrackFor(title, artist string) online.Track {
	if i.searcher == nil || strings.TrimSpace(title) == "" {
		return online.Track{}
	}
	keyword := strings.TrimSpace(title + " " + artist)
	want := qualityNameKey(title)
	for _, p := range i.platforms() {
		for _, s := range i.searcher(keyword, p, 1, 3) {
			if qualityNameKey(s.Name) != want {
				continue
			}
			id := songPlatformID(s)
			if id == "" {
				continue
			}
			return online.Track{
				Platform: p, PlatformID: id,
				Title: strings.TrimSpace(s.Name), Artists: []string{strings.TrimSpace(s.Singer)},
				Album: strings.TrimSpace(s.Album), Duration: durationSeconds(s),
				Quality: strings.TrimSpace(s.Quality),
			}.Normalized()
		}
	}
	return online.Track{}
}

// probeQuality 探一首歌在各渠道的可得档位。返回的 entry 里 `Channels` 按用户配的
// 渠道顺序排（与界面上那三行的顺序一致），`Best` 是其中实际档位最高的那条。
func (i *Interceptor) probeQuality(ctx context.Context, path string) QualityEntry {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	artist, title := namefmt.Parse(base)
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSpace(base)
	}
	entry := QualityEntry{
		Path: path, Title: strings.TrimSpace(title), Artist: strings.TrimSpace(artist),
		Ext: ext, CurrentTier: qualityCurrentTier(ext),
	}

	track := i.nativeTrackFor(entry.Title, entry.Artist)
	track.Title = entry.Title
	track.Artists = []string{entry.Artist}
	attempts := i.collectDownloadAttempts(ctx, track, i.downloadTiers())
	entry.Channels = channelTiersOf(attempts, i.downloadChannels())

	// 赢家：实际档位最高、同档位取渠道顺序靠前的那条（与落盘时的排序口径一致）。
	for n := range entry.Channels {
		ct := entry.Channels[n]
		if ct.Tier == "" {
			continue
		}
		if entry.Best == nil || offlineBetterChannel(ct, *entry.Best, i.downloadChannels()) {
			cp := ct
			entry.Best = &cp
		}
	}
	entry.Upgradeable = entry.Best != nil &&
		online.TierRank(entry.Best.Tier) > online.TierRank(entry.CurrentTier)
	return entry
}

// offlineBetterChannel 比较两条渠道结果：档位高的赢；同档位按**用户配的顺序**（越前越好）。
func offlineBetterChannel(a, b channelTier, channels []string) bool {
	ra, rb := online.TierRank(a.Tier), online.TierRank(b.Tier)
	if ra != rb {
		return ra > rb
	}
	return channelOrderIndex(channels, a.Channel) < channelOrderIndex(channels, b.Channel)
}

// ourChannelOrderIndex 取渠道在用户配置里的位置（不在列表里排最后）。
func channelOrderIndex(channels []string, want string) int {
	for n, ch := range channels {
		if ch == want {
			return n
		}
	}
	return len(channels)
}

// ── 扫描任务 ──

// StartQualityScan 起一次扫描。已有任务在跑时**返回当前状态**而不是并起第二个
// （扫描是外呼重活，两份并行只是互相拖慢）。
func (i *Interceptor) StartQualityScan(mode string, limit int) (QualityStatus, error) {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}, fmt.Errorf("拦截层未就绪")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "all" {
		mode = "lossy"
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > qualityScanMaxLimit {
		limit = qualityScanMaxLimit
	}

	i.quality.mu.Lock()
	if i.quality.running {
		st := i.quality.status
		i.quality.mu.Unlock()
		return st, nil
	}
	paths, truncated, err := i.qualityListLibraryFiles(mode, limit)
	if err != nil {
		i.quality.mu.Unlock()
		return QualityStatus{State: QualityStateFailed, Error: err.Error()}, nil
	}
	i.quality.running = true
	i.quality.stop = make(chan struct{})
	i.quality.status = QualityStatus{
		State: QualityStateRunning, Mode: mode,
		Total: len(paths), Truncated: truncated,
		Entries: make([]QualityEntry, 0, len(paths)),
	}
	st := i.quality.status
	i.quality.mu.Unlock()

	go i.runQualityScan(paths)
	return st, nil
}

// QualityScanStatus 读当前状态（未起过任务时是 idle）。
func (i *Interceptor) QualityScanStatus() QualityStatus {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}
	}
	i.quality.mu.Lock()
	defer i.quality.mu.Unlock()
	st := i.quality.status
	if st.Entries == nil {
		st.Entries = []QualityEntry{}
	}
	return st
}

// StopQualityScan 收回当前任务：已经探完的那部分**保留在状态里**（用户可以据此先换
// 一批），状态回到 done，再由下一次 start 覆盖。
func (i *Interceptor) StopQualityScan() QualityStatus {
	if i.quality == nil {
		return QualityStatus{State: QualityStateIdle}
	}
	i.quality.mu.Lock()
	if i.quality.running && i.quality.stop != nil {
		close(i.quality.stop)
		i.quality.stop = nil
	}
	i.quality.mu.Unlock()
	return i.QualityScanStatus()
}

func (i *Interceptor) runQualityScan(paths []string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 外部叫停 → 取消在途探测（洛雪那条一次要好几秒，不能等它自己走完）。
	go func() {
		i.quality.mu.Lock()
		stop := i.quality.stop
		i.quality.mu.Unlock()
		if stop == nil {
			return
		}
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < qualityScanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				entry := i.probeQuality(ctx, paths[n])
				i.quality.mu.Lock()
				if len(i.quality.status.Entries) < qualityScanMaxEntries {
					i.quality.status.Entries = append(i.quality.status.Entries, entry)
				}
				i.quality.status.Scanned++
				if entry.Upgradeable {
					i.quality.status.Upgradeable++
				}
				i.quality.mu.Unlock()
			}
		}()
	}
	for n := range paths {
		select {
		case jobs <- n:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	i.quality.mu.Lock()
	i.quality.running = false
	i.quality.stop = nil
	// 结果按「可替换优先 + 路径」排一下：用户打开这一页最想看的是能换的那批，
	// 而不是按文件系统遍历顺序（那个顺序在他眼里就是随机的）。
	sort.SliceStable(i.quality.status.Entries, func(a, b int) bool {
		ea, eb := i.quality.status.Entries[a], i.quality.status.Entries[b]
		if ea.Upgradeable != eb.Upgradeable {
			return ea.Upgradeable
		}
		return ea.Path < eb.Path
	})
	i.quality.status.State = QualityStateDone
	i.quality.mu.Unlock()
}

// qualityLibraryRoots 返回「扫哪些目录」。
//
// ⚠️ 用**曲库目录**（设置里的 download_dir），**不是** `nas.AllowedRoots()`。
// 后者是「允许访问哪些路径」的**安全边界**（真机上是 `/vol1` `/vol2` `/vol3`
// 三个整卷），拿它当扫描根目录会去遍历整个卷 —— 2026-10-21 真机实测：扫出来的
// 40 条里全是应用自己数据目录下的 `tts_*` / uuid 命名的中间产物，一首真歌都没进
// 结果（而且那 40 条已经把 limit 的名额占满了）。安全边界与「音乐在哪」是两件事。
func (i *Interceptor) qualityLibraryRoots() []string {
	if i.cfg.DownloadDir != nil {
		if dir := strings.TrimSpace(i.cfg.DownloadDir()); dir != "" {
			return []string{dir}
		}
	}
	return nil
}

// qualityListLibraryFiles 列出曲库里要扫描的文件。
//
// mode=lossy（默认）只列**有损**文件：无损的没什么可换的，扫它纯属浪费外呼。
// mode=all 全列（用户想看看「我这库到底什么水平」）。
func (i *Interceptor) qualityListLibraryFiles(mode string, limit int) ([]string, bool, error) {
	roots := i.qualityLibraryRoots()
	if len(roots) == 0 {
		return nil, false, fmt.Errorf("没拿到曲库目录，先在设置里配好存储目录")
	}
	out := make([]string, 0, limit)
	truncated := false
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// 单个目录读不动（权限/被删）不该让整次扫描失败：跳过继续。
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// 回收站与隐藏目录不进扫描（那是整理流程的中间态）。
				if name := d.Name(); strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if !qualityAudioExts[ext] {
				return nil
			}
			if mode != "all" && qualityLosslessExts[ext] {
				return nil
			}
			if len(out) >= limit {
				truncated = true
				return fs.SkipAll
			}
			out = append(out, p)
			return nil
		})
		if err != nil && err != fs.SkipAll {
			return out, truncated, err
		}
		if truncated {
			break
		}
	}
	sort.Strings(out)
	return out, truncated, nil
}

// ── 替换 ──

// QualityReplaceDone / Failed 是 /api/quality/replace 的结果项。
type QualityReplaceDone struct {
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	NewPath string `json:"new_path,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Channel string `json:"channel,omitempty"`
	// Note 是「这条为什么不算成功」的人话（例如平档不换）。界面直接显示它。
	Note string `json:"note,omitempty"`
}

type QualityReplaceFailed struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// ReplaceQuality 对给定的曲库文件逐个「按各渠道实际可得档位」重新取音并覆盖。
//
// 串行执行（不是并发）：这是**写**操作，且每条都要跑一遍外呼；串行让 50 条的结果
// 顺序稳定、日志可读，也避免同时往同一个存储写多个大文件。
//
// 一条失败不影响后面的：返回 done/failed 两张表，界面逐条显示。
func (i *Interceptor) ReplaceQuality(ctx context.Context, paths []string) ([]QualityReplaceDone, []QualityReplaceFailed, error) {
	done := make([]QualityReplaceDone, 0, len(paths))
	failed := make([]QualityReplaceFailed, 0)
	for _, raw := range paths {
		select {
		case <-ctx.Done():
			failed = append(failed, QualityReplaceFailed{Path: raw, Error: "已取消"})
			continue
		default:
		}
		item, err := i.replaceOneQuality(ctx, raw)
		if err != nil {
			failed = append(failed, QualityReplaceFailed{Path: raw, Error: err.Error()})
			continue
		}
		done = append(done, item)
	}
	return done, failed, nil
}

func (i *Interceptor) replaceOneQuality(ctx context.Context, raw string) (QualityReplaceDone, error) {
	// 路径守卫：只允许曲库根目录内的文件（与整理/删除走同一道闸）。
	path, err := nas.ResolveSafePath(strings.TrimSpace(raw))
	if err != nil {
		return QualityReplaceDone{}, fmt.Errorf("路径不在曲库内：%w", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return QualityReplaceDone{}, fmt.Errorf("文件不存在或是目录")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if qualityLosslessExts[ext] {
		return QualityReplaceDone{}, fmt.Errorf("已经是无损（%s），不需要替换", strings.TrimPrefix(ext, "."))
	}

	entry := i.probeQuality(ctx, path)
	if entry.Best == nil || !entry.Upgradeable {
		return QualityReplaceDone{}, fmt.Errorf("各渠道都没有更高的档位：现在 %s，渠道最高 %s",
			entry.CurrentTier, bestTierText(entry))
	}
	best := *entry.Best

	track := i.nativeTrackFor(entry.Title, entry.Artist)
	track.Title = entry.Title
	track.Artists = []string{entry.Artist}
	tiers := i.downloadTiers()
	attempts := i.collectDownloadAttempts(ctx, track, tiers)
	var pick *dlAttempt
	for n := range attempts {
		if attempts[n].Channel == best.Channel {
			pick = &attempts[n]
			break
		}
	}
	if pick == nil {
		// 两次探测结果不一致（第三方站此刻不给了）：如实报「这一轮没拿到」，
		// 不要拿上一次的结果去下一条可能已经失效的直链。
		return QualityReplaceDone{}, fmt.Errorf("%s 渠道刚才还能给 %s，这次没解析出直链，稍后再试", best.Channel, best.Tier)
	}

	fake := "quality:" + fmt.Sprint(time.Now().UnixNano())
	item, err := i.postDownload(ctx, fake, pick.Cand, pick.Cand, pick.Res.URL, pick.Tier())
	if err != nil {
		return QualityReplaceDone{}, err
	}
	// 下载接口拿到无损时会**替换**旧文件（upgrade_if_better），拿到同档会回原路径。
	// 所以「路径没变」= 其实没换成功 —— 不能报成功，否则用户以为换了、盘上还是 mp3。
	if filepath.Clean(item.Path) == filepath.Clean(path) {
		return QualityReplaceDone{
			Path: path, OK: false, NewPath: item.Path, Tier: item.Quality, Channel: pick.Channel,
			Note: "拿到的是同档位文件，没有覆盖（旧的保留）",
		}, nil
	}
	i.logf("[INTERCEPT] 替换音质：%s → %s（%s 渠道 %s）", filepath.Base(path), filepath.Base(item.Path), pick.Channel, pick.Tier())
	return QualityReplaceDone{
		Path: path, OK: true, NewPath: item.Path, Tier: item.Quality, Channel: pick.Channel,
	}, nil
}

func bestTierText(e QualityEntry) string {
	best := ""
	for _, ch := range e.Channels {
		if ch.Tier == "" {
			continue
		}
		if best == "" || online.TierRank(ch.Tier) > online.TierRank(best) {
			best = ch.Tier
		}
	}
	if best == "" {
		return "没探到"
	}
	return best
}
