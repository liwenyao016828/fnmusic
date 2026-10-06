package intercept

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 「边听边下」（tee）：播放在线曲目时，把**同一条流**顺手写进磁盘，之后这首歌的
// 取流自动走本地。
//
// # 为什么值得做
//
// 这是唯一一种「不多花一分钱带宽」的下载：字节本来就从这儿过。参考实现里对应
// `_full_fetch_download` + `_promote_cached_to_library`。
//
// # 两条路径，由开关决定走哪条（互不污染）
//
//   - **开关开着** → 落进曲库（`<下载目录>/<歌手> - <标题>.<ext>`）+ 登记表登记，
//     重播走「收藏自动绑定本地」那条既有通路。这条路径已经真机验收过，行为不要改。
//   - **开关关着** → 只留**滚动试听缓存**（`<数据目录>/rolling-cache/<虚拟id>.<ext>`）：
//     最近 `teeRollingKeep` 首留在本地，重播直接喂文件、不出网；但它**不进曲库、
//     不进登记表**，被淘汰就是没了 —— 开关关着的语义仍然是「不留永久曲库副本」，
//     滚动缓存只是临时试听缓存。参考实现对应 `_tee_finalize` 的 else 分支
//     （`proxy/app.py:3866-3882`）与 `purge_rolling`（`proxy/cache_gc.py:63`）。
//
// 两条路径的共同点（也是本文件存在的理由）：都搭的是客户端正在听的那条流。
//
// # 两条硬规矩
//
//  1. **播放优先**：写盘失败绝不能影响听歌 —— 先写给客户端，再写盘；盘出错就把
//     文件句柄丢掉继续播（见 `teeWriter.Write`）。tee 是搭便车的，不是主角。
//  2. **只提升完整文件**：半截文件进了曲库比没有更糟 —— 它会一直播到一半停，
//     而用户以为已经下好了。所以必须「拿到的字节数 == 上游声明的总长」才提升。
//     这条对滚动缓存同样成立：缓存一个半截文件会让「重播不出网」变成
//     「重播播到一半停」。曲率对半截文件**有意**丢弃（见 `finishTee`），
//     滚动缓存沿用同一条规矩。
const teeSubdir = ".qulv-tee"

// teeRollingSubdir 是滚动试听缓存的目录名，落在**数据目录**下（与 favorites /
// play_history / downloaded.json 那几张表并列）。
//
// ⚠️ **刻意不放在下载目录里**，这是「开关关着 = 不留永久曲库副本」那条红线的
// 结构性保证：下载目录就是飞牛的曲库目录，放进去（哪怕藏在 `.` 子目录里）就有
// 被曲库扫描当成真曲目的风险 —— 那正是这条红线要防的事。参考实现同样把滚动缓存
// 放在自己的 cache_dir，而不是曲库目录。
const teeRollingSubdir = "rolling-cache"

// teeRollingKeep 是滚动试听缓存保留的曲目数上限（按**最近使用**淘汰，不是按写入）。
//
// 定 5 的理由：
//   - 要覆盖的用法是「刚才那首再听一遍 / 前后几首来回听 / 小歌单循环」，
//     只留 1 首（当前曲目）或 2 首（加个上一首）都嫌紧；
//   - 它必须是有限值：这是**临时**缓存，不能长成第二个曲库。5 首 320k 约 50MB，
//     就算整轨无损也在 150MB 量级 —— 对应用数据盘是可控的常数。
//
// 0 的语义是「一条不留」（见 `purgeRolling`），所以把它改成 0 就等于关掉滚动缓存。
const teeRollingKeep = 5

// teeRollingExts 是滚动缓存可能出现的扩展名（查缓存时要逐个试）。
//
// 唯一写入方是 `finishRolling`（用 `extForFormat`），两边必须同源；
// `TestRollingExtsMatchExtForFormat` 钉住这件事。
var teeRollingExts = []string{".mp3", ".flac", ".m4a", ".wav", ".ape"}

// teeEnrichTimeout 是「补标签」的预算：它要抓歌词与封面（各一次外部请求），
// 比下载短得多，但也不能让它挂住 —— 失败只是少几个标签，不影响已下好的歌。
const teeEnrichTimeout = 45 * time.Second

// teeTarget 是一次 tee 的现场。
type teeTarget struct {
	f    *os.File
	part string
	// rolling 为 true 表示这份字节的归宿是**滚动试听缓存**（开关关着那条路径），
	// 为 false 表示归宿是曲库（开关开着那条路径，见 finishTee）。
	rolling bool
	wrote   int64
}

// teeOn 报告开关（现取，改配置立刻生效）。
func (i *Interceptor) teeOn() bool {
	return i.cfg.TeeEnabled != nil && i.cfg.TeeEnabled()
}

// teeDir 返回 tee 的暂存目录。
//
// 刻意放在**下载目录下面**的隐藏子目录里：与最终落点在同一个文件系统上，
// 「提升」就是一次 rename，不用跨设备搬几十兆字节。
func (i *Interceptor) teeDir() string {
	if i.cfg.DownloadDir == nil {
		return ""
	}
	dir := strings.TrimSpace(i.cfg.DownloadDir())
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, teeSubdir)
}

// teeRollingDir 返回滚动试听缓存的落点（`<数据目录>/rolling-cache`）。
//
// 数据目录拿不到（理论上不会）时返回空串 = 这条路径整体退化掉，
// 表现只是「重播还要出网」，与以前一致。
func (i *Interceptor) teeRollingDir() string {
	dir := strings.TrimSpace(i.cfg.DataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, teeRollingSubdir)
}

// beginTee 为一次完整转发开一条落盘的路。返回 nil = 这次不落盘。
//
// 不落盘的几种情形，每一种都有理由（两条路径**共用**这批判断）：
//   - 没有登记表 / 没有虚拟 id —— 无从下手；
//   - 已经在库里 —— 别再存一份（重播本来就走本地）；
//   - 不是 200（206 分片）或是 HEAD —— 拼不成完整文件；
//   - 客户端要的是**中段** Range（拖动/续传）—— 同理；
//   - 上游没给 Content-Length —— 就没法判断「完整」，宁可不存。
//
// 开关决定的是**归宿**：开着进曲库暂存目录，关着进滚动缓存目录。
// 开关开着但下载目录不可用时**不**退化成滚动缓存 —— 那会让「开着」这条已经
// 真机验收过的路径凭空多出新的写盘行为，两条路径必须保持互不污染。
func (i *Interceptor) beginTee(fake string, t online.Track, resp *http.Response, r *http.Request) *teeTarget {
	if i.downloaded == nil || fake == "" {
		return nil
	}
	if resp.StatusCode != http.StatusOK || r.Method == http.MethodHead {
		return nil
	}
	if rng := strings.TrimSpace(r.Header.Get("Range")); rng != "" && !strings.HasPrefix(rng, "bytes=0-") {
		return nil
	}
	if _, ok := i.downloaded.Get(fake); ok {
		return nil
	}
	if resp.ContentLength <= 0 {
		return nil
	}
	if i.teeOn() {
		return i.beginLibraryTee(fake)
	}
	return i.beginRollingTee(fake)
}

// beginLibraryTee 开一份「提升进曲库」的暂存（开关开着那条路径）。
func (i *Interceptor) beginLibraryTee(fake string) *teeTarget {
	dir := i.teeDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.Create(filepath.Join(dir, fake+".part"))
	if err != nil {
		return nil
	}
	return &teeTarget{f: f, part: filepath.Join(dir, fake+".part")}
}

// beginRollingTee 开一份「滚动试听缓存」的暂存（开关关着那条路径）。
//
// 暂存直接建在**缓存目录里**：转正就是一次同目录 rename，不会跨设备搬字节
// （参考实现也是直接往 cache_dir 写 .part）。
//
// ⚠️ 暂存名带一个**唯一后缀**（`<虚拟id>.<随机>.part`）：同一条曲目被并发取两次
// （多设备 / 重试与首次重叠）时，两次写盘绝不能共用一个文件 —— 共用了就是两股字节
// 交错写进同一个文件，两边各自都「写够了 expected」，于是**一条坏缓存被当成完整的
// 缓存转正**，之后每次重播都放一份坏音频。带随机后缀之后，各写各的，最后一次
// rename 赢，赢的那份是完整且自洽的。
//
// 参考实现同样用 uuid 后缀（`proxy/app.py:4020`）。**曲库那条路径不动**：
// 它已经真机验收过，这里只修这条新路径。
func (i *Interceptor) beginRollingTee(fake string) *teeTarget {
	dir := i.teeRollingDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	// CreateTemp 的默认权限是 0600（临时文件语义）；缓存不需要比曲库文件更严，
	// 但保持与包内其它落盘一致（0644），免得以后有人对着两种权限猜原因。
	f, err := os.CreateTemp(dir, fake+".*.part")
	if err != nil {
		return nil
	}
	_ = f.Chmod(0o644)
	return &teeTarget{f: f, part: f.Name(), rolling: true}
}

// finishTee 收尾：完整就转正（进曲库或进滚动缓存，看这次是哪种路径），不完整就删掉。
func (i *Interceptor) finishTee(tt *teeTarget, fake string, t online.Track, expect int64, format string) {
	if tt == nil {
		return
	}
	_ = tt.f.Close()

	if expect <= 0 || tt.wrote != expect {
		// 半截文件留着只会占地方，而且下次播放会误以为有缓存
		_ = os.Remove(tt.part)
		i.logf("[INTERCEPT] 边听边下未完成，已丢弃（%d/%d 字节）：%s", tt.wrote, expect, t.Title)
		return
	}
	if tt.rolling {
		i.finishRolling(tt, fake, t, format)
		return
	}
	dst := i.teeDestPath(t, format)
	if dst == "" {
		_ = os.Remove(tt.part)
		return
	}
	if err := os.Rename(tt.part, dst); err != nil {
		// rename 失败（权限/意外跨设备）→ 退化成复制，但**别把半截留在暂存里**
		if cerr := copyLocalFile(tt.part, dst); cerr != nil {
			_ = os.Remove(tt.part)
			i.logf("[INTERCEPT] 边听边下提升失败：%v", cerr)
			return
		}
		_ = os.Remove(tt.part)
	}

	item := online.DownloadedItem{
		GUID: fake, Path: dst, Title: t.Title, Artist: t.Artist(), Size: tt.wrote,
		// tee 存的就是客户端正在听的那条流 —— 取音来源必然是曲目自己平台
		// （下载源池只管下载，管不到播放），所以这里如实填它。
		Source: t.Platform, Quality: t.Quality,
	}
	if err := i.downloaded.Put(item); err != nil {
		i.logf("[INTERCEPT] 边听边下登记失败：%v", err)
		return
	}
	i.logf("[INTERCEPT] 边听边下完成并已绑定本地 %q — %s → %s", t.Title, t.Artist(), dst)
	i.enrichAfterTee(fake, item, t)
	// 顺手把滚动缓存收敛到上限：开关在开/关之间切过之后，老缓存不该无限期占着地方。
	i.purgeRolling(teeRollingKeep)
}

// finishRolling 把一条完整的试听缓存转正到滚动目录，并按「最近使用」收敛到上限。
//
// ⚠️ **红线**：这里**不写登记表、不补标签、不落歌词**。滚动缓存不是「已进曲库」——
// 一旦登记进 downloaded，取流就会去读一个迟早被淘汰掉的临时文件：登记表里躺着
// 一条指向「明天可能没了」的路径，而「收藏里那首歌 404」正是这张表要避免的事。
// 参考实现的 `_tee_finalize` 里，开关关着那条分支同样只搬文件、不写任何元数据。
func (i *Interceptor) finishRolling(tt *teeTarget, fake string, t online.Track, format string) {
	dir := i.teeRollingDir()
	if dir == "" {
		_ = os.Remove(tt.part)
		return
	}
	dst := filepath.Join(dir, fake+extForFormat(format))
	if err := os.Rename(tt.part, dst); err != nil {
		if cerr := copyLocalFile(tt.part, dst); cerr != nil {
			_ = os.Remove(tt.part)
			i.logf("[INTERCEPT] 试听缓存落盘失败：%v", cerr)
			return
		}
		_ = os.Remove(tt.part)
	}
	// 刚写完 = 刚用过。mtime 就是「最近使用」那条时间线（命中时也会刷新，见
	// serveRollingIfAny），淘汰按它排序。显式设一次而不是靠文件系统给的时间，
	// 是为了让「写入」和「命中」两条时间线用同一个时钟。
	now := time.Now()
	_ = os.Chtimes(dst, now, now)

	i.logf("[INTERCEPT] 试听缓存已保留 %d 首（不出网重播）：%q — %s（%.1f MB）",
		teeRollingKeep, t.Title, t.Artist(), float64(tt.wrote)/(1<<20))
	i.purgeRolling(teeRollingKeep)
}

// serveRollingIfAny 用滚动试听缓存应答取流：命中就完全不出网。
//
// 返回 true = 已经应答（调用方不要再往下走）。这是「开关关着时重播不出网」
// 的落地点 —— 与「已进曲库」那条路（serveDownloadedIfAny）刻意分开：
// 那条读登记表（永久），这条只认缓存目录里的文件（临时、随时可能被淘汰）。
func (i *Interceptor) serveRollingIfAny(w http.ResponseWriter, r *http.Request, fake string) bool {
	path := i.rollingPath(fake)
	if path == "" {
		return false
	}
	// 命中就是「刚用过」：刷新 mtime，让淘汰按使用而不是按写入排。
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	i.logf("[INTERCEPT] 试听缓存命中 %s → %s", fake, path)
	// http.ServeFile 自带 Range / If-Modified-Since / Content-Type 推断 ——
	// 官方客户端拖动进度条靠 Range，自己写一遍容易漏（与 serveDownloadedIfAny 同理）。
	http.ServeFile(w, r, path)
	return true
}

// rollingPath 找 `<rolling>/<fake>.<ext>`；没有返回空串。
//
// 按扩展名逐个试而不是记一张表：目录里最多 `teeRollingKeep` 个文件，
// 几次 stat 的成本可以忽略，而少维护一张「文件名 → 曲目」的映射就少一处会不一致的状态。
func (i *Interceptor) rollingPath(fake string) string {
	dir := i.teeRollingDir()
	if dir == "" || fake == "" {
		return ""
	}
	// fake 是登记表里的虚拟 id（32 位 hex），但它一路从请求里传进来 ——
	// 拼路径前挡一次路径分隔符，免得将来有人把别的来源接进来。
	if strings.ContainsAny(fake, `/\`) || strings.Contains(fake, "..") {
		return ""
	}
	for _, ext := range teeRollingExts {
		p := filepath.Join(dir, fake+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p
		}
	}
	return ""
}

// rollingFiles 列出滚动缓存里的**完整**音频（按最近使用新→旧，mtime 相同则按文件名）。
//
// `.part` 是**正在进行**的缓存，不算数：它既不该被淘汰，也不该占 keep 名额。
func (i *Interceptor) rollingFiles() []string {
	dir := i.teeRollingDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		path  string
		mtime int64
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !rollingAudioName(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	// 稳定排序：同一次 Chtimes 的秒级/纳秒级并列（以及某些文件系统的秒级 mtime）
	// 不应该让「淘汰哪一条」变成随机 —— 那会让行为在真机上不可复现。
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].mtime != items[b].mtime {
			return items[a].mtime > items[b].mtime
		}
		return items[a].path < items[b].path
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.path)
	}
	return out
}

// rollingAudioName 判断一个文件名是不是滚动缓存的完整音频（而不是半截的 `.part`）。
func rollingAudioName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, want := range teeRollingExts {
		if ext == want {
			return true
		}
	}
	return false
}

// purgeRolling 把滚动缓存收敛到 keep 条（保留最近使用的那些），返回删除条数。
//
// keep <= 0 = 一条不留（把 `teeRollingKeep` 改成 0 就等于关掉滚动缓存）。
//
// 只删**完整音频**，`.part` 一律不碰：那是正在下载/正在被下一段字节续写的现场，
// 删了它等于把一次进行中的播放写盘搞坏（这是它与启动清扫 `cleanTeeCache` 的分工
// —— 后者清的是上次运行留下的孤儿 `.part`）。
func (i *Interceptor) purgeRolling(keep int) int {
	files := i.rollingFiles()
	if keep < 0 {
		keep = 0
	}
	if len(files) <= keep {
		return 0
	}
	removed := 0
	for _, p := range files[keep:] {
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	if removed > 0 {
		i.logf("[INTERCEPT] 试听缓存淘汰 %d 条（上限 %d 首）", removed, keep)
	}
	return removed
}

// enrichAfterTee 给 tee 提升出来的文件补标签（**不重下音频**）。
//
// ⚠️ 为什么必须补：tee 是直接复制流字节、没走下载接口，所以落盘的文件**没有任何
// 标签** —— 歌能听、能绑定，但在飞牛里标题/歌手/专辑/封面/歌词全是空的。
// 重新下载一遍会把 tee 省下的带宽又花回去，所以只调「补标签」那条路径。
//
// 失败只记日志：文件已经在库里、已经能听，补标签是锦上添花 —— 不能因为抓不到
// 封面就把一首已经下好的歌算成失败。
func (i *Interceptor) enrichAfterTee(fake string, item online.DownloadedItem, t online.Track) {
	// ⚠️ 判 `i.localAPI` 而不是 `i.cfg.LocalAPI` —— 真正发请求的 postLocalAPI 用的是
	// 前者（构造时从 cfg 解析并去掉尾斜杠）。判错了会在测试环境里静默不补标签。
	if i.localAPI == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), teeEnrichTimeout)
	defer cancel()
	payload := map[string]any{
		"path":     item.Path,
		"name":     t.Title,
		"singer":   t.Artist(),
		"album":    t.Album,
		"cover":    t.CoverURL,
		"source":   t.Platform,
		"songmid":  t.PlatformID,
		"duration": t.Duration,
		"year":     t.Year,
	}
	for k, v := range i.downloadPrefs() {
		payload[k] = v
	}
	if _, status, err := i.postLocalAPI(ctx, "/api/download/enrich", payload); err != nil || status != 200 {
		i.logf("[INTERCEPT] 边听边下补标签失败（文件已在库里，不影响播放）：status=%d err=%v", status, err)
		return
	}
	// 补完标签文件**变大了**（封面/歌词进了标签）—— 登记表里那份大小要跟上。
	// 否则「登记说 10.4 MB、磁盘上 10.5 MB」这种对不上，排查时会让人以为是别的问题
	// （真机实测：流下来 10,878,476 → 落盘 11,029,926）。
	if fi, statErr := os.Stat(item.Path); statErr == nil && fi.Size() != item.Size {
		item.Size = fi.Size()
		if err := i.downloaded.Put(item); err != nil {
			i.logf("[INTERCEPT] 边听边下更新登记大小失败：%v", err)
		}
	}
}

// teeDestPath 决定提升后的落点：`<下载目录>/<歌手> - <标题>.<扩展名>`。
//
// 与下载链路同一个命名习惯 —— 同一个曲库里不该有两种命名。
func (i *Interceptor) teeDestPath(t online.Track, format string) string {
	if i.cfg.DownloadDir == nil {
		return ""
	}
	dir := strings.TrimSpace(i.cfg.DownloadDir())
	if dir == "" {
		return ""
	}
	name := sanitizeFileName(t.Artist() + " - " + t.Title)
	if name == "" {
		name = sanitizeFileName(t.Title)
	}
	if name == "" {
		return ""
	}
	return filepath.Join(dir, name+extForFormat(format))
}

// teeWriter 一边把字节交给客户端，一边写进暂存文件。
//
// ⚠️ **顺序不能反**：先写给客户端（播放优先），再写盘。盘写失败就把句柄丢掉，
// 绝不让磁盘问题变成一个「听不了」的错误。
type teeWriter struct {
	w io.Writer
	t *teeTarget
}

func (tw *teeWriter) Write(p []byte) (int, error) {
	n, err := tw.w.Write(p)
	if n > 0 && tw.t.f != nil {
		if _, werr := tw.t.f.Write(p[:n]); werr != nil {
			_ = tw.t.f.Close()
			tw.t.f = nil // 之后只转发，不再碰盘
		} else {
			tw.t.wrote += int64(n)
		}
	}
	return n, err
}

// sanitizeFileName 把文件名里的路径分隔符与控制字符换掉 —— 歌名是外部数据，
// 带 `/` 的话会把文件写到别的地方去。
func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}
	out := strings.Map(repl, s)
	out = strings.Trim(out, " .")
	if len(out) > 120 {
		out = strings.TrimSpace(out[:120])
	}
	return out
}

// extForFormat 把解析出来的格式映射成扩展名（认不出就当 mp3）。
func extForFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "flac":
		return ".flac"
	case "m4a", "aac":
		return ".m4a"
	case "wav":
		return ".wav"
	case "ape":
		return ".ape"
	}
	return ".mp3"
}

// copyLocalFile 是 rename 失败时的退路。
func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// teeStaleAfter 是暂存文件的「过期」门槛。
//
// 只在**启动时**清一次，且只清超过这个时长的 —— 启动时不可能有正在进行的 tee，
// 但门槛仍然留着，避免误删上一次运行刚写了一半、马上要被续上的文件。
const teeStaleAfter = time.Hour

// cleanTeeCache 清掉上次运行留下的半截暂存文件，并把滚动缓存收敛到上限。
//
// ⚠️ 为什么必须清：`.part` 只在 `finishTee` 里被删或被改名。进程如果**中途被杀**
// （升级、OOM、断电），那个 `.part` 就永远留在对应目录里 —— 每听一首歌没听完就升级，
// 就多一个几十兆的残骸，而且**没有任何地方会提到它**（界面上看不到、日志里没有）。
//
// 两个目录都要扫（曲库暂存 + 滚动缓存），因为两条路径都会留下 `.part`。
// 顺手 `purgeRolling`：上限被调小之后，老缓存要能在启动时就清掉。
//
// 只在启动时清：那时候不可能有正在进行的 tee，所以不用考虑并发。
func (i *Interceptor) cleanTeeCache() {
	cutoff := time.Now().Add(-teeStaleAfter)
	for _, dir := range []string{i.teeDir(), i.teeRollingDir()} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		removed, freed := 0, int64(0)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				removed++
				freed += info.Size()
			}
		}
		if removed > 0 {
			i.logf("[INTERCEPT] 清掉 %d 个上次运行留下的半截暂存文件（%.1f MB）：%s",
				removed, float64(freed)/(1<<20), dir)
		}
	}
	i.purgeRolling(teeRollingKeep)
}
