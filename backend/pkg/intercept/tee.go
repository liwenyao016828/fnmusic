package intercept

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 「边听边下」（tee）：播放在线曲目时，把**同一条流**顺手写进磁盘；完整拿到就提升成
// 曲库文件，之后这首歌的取流自动走本地（与「收藏自动下载」共用同一张登记表）。
//
// # 为什么值得做
//
// 这是唯一一种「不多花一分钱带宽」的下载：字节本来就从这儿过。参考实现里对应
// `_full_fetch_download` + `_promote_cached_to_library`。
//
// # 两条硬规矩
//
//  1. **播放优先**：写盘失败绝不能影响听歌 —— 先写给客户端，再写盘；盘出错就把
//     文件句柄丢掉继续播（见 `teeWriter.Write`）。tee 是搭便车的，不是主角。
//  2. **只提升完整文件**：半截文件进了曲库比没有更糟 —— 它会一直播到一半停，
//     而用户以为已经下好了。所以必须「拿到的字节数 == 上游声明的总长」才提升。
const teeSubdir = ".qulv-tee"

// teeEnrichTimeout 是「补标签」的预算：它要抓歌词与封面（各一次外部请求），
// 比下载短得多，但也不能让它挂住 —— 失败只是少几个标签，不影响已下好的歌。
const teeEnrichTimeout = 45 * time.Second

// teeTarget 是一次 tee 的现场。
type teeTarget struct {
	f     *os.File
	part  string
	wrote int64
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

// beginTee 为一次完整转发开一个 tee。返回 nil = 这次不 tee。
//
// 不 tee 的几种情形，每一种都有理由：
//   - 开关关着 / 没有下载目录 / 没有登记表 —— 无从下手；
//   - 已经在库里 —— 别再存一份；
//   - 不是 200（206 分片）或是 HEAD —— 拼不成完整文件；
//   - 客户端要的是**中段** Range（拖动/续传）—— 同理；
//   - 上游没给 Content-Length —— 就没法判断「完整」，宁可不存。
func (i *Interceptor) beginTee(fake string, t online.Track, resp *http.Response, r *http.Request) *teeTarget {
	if !i.teeOn() || i.downloaded == nil || fake == "" {
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

// finishTee 收尾：完整就提升进曲库，不完整就删掉。
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

// cleanTeeCache 清掉上次运行留下的半截暂存文件。
//
// ⚠️ 为什么必须清：`.part` 只在 `finishTee` 里被删或被改名。进程如果**中途被杀**
// （升级、OOM、断电），那个 `.part` 就永远留在下载目录里 —— 每听一首歌没听完就升级，
// 就多一个几十兆的残骸，而且**没有任何地方会提到它**（界面上看不到、日志里没有）。
//
// 只在启动时清：那时候不可能有正在进行的 tee，所以不用考虑并发。
func (i *Interceptor) cleanTeeCache() {
	dir := i.teeDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-teeStaleAfter)
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
		i.logf("[INTERCEPT] 清掉 %d 个上次运行留下的半截暂存文件（%.1f MB）",
			removed, float64(freed)/(1<<20))
	}
}
