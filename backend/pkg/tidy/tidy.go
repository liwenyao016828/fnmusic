// Package tidy 提供音乐库的自动刮削与整理能力。
//
// 移植自 music-tidy 的刮削流水线思路（补歌词/封面/标签、目录整理），
// 但做了两处关键改进：
//   - 封面**只认逐曲同名图**，绝不写目录级 cover.jpg
//     （目录级封面会让整个文件夹显示同一张图，是 music-tidy 明确踩过的坑）；
//   - 所有写盘操作走原子替换（复用 pkg/tags），失败不会损坏原文件。
package tidy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/tags"
)

// Item 一首待整理的曲目
type Item struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Genre    string `json:"genre,omitempty"` // 风格：写入内嵌标签供飞牛音乐「风格」页归类
	Duration int    `json:"duration"`

	// 飞牛「歌曲信息」里的另外三栏（2026-09-22 按字段补全）。
	// ⚠️ **0 = 不写入**（不是「写 0」）—— 平台没给就别往文件里塞假值。
	Year  int `json:"year,omitempty"`
	Track int `json:"track,omitempty"`
	Disc  int `json:"disc,omitempty"`

	// Lyric / CoverURL 由外部（音源/刮削源）提供
	Lyric    string `json:"lyric,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
	// CoverBytes 已下载好的封面字节（优先于 CoverURL）
	CoverBytes []byte `json:"-"`
	CoverMime  string `json:"-"`
}

// Options 整理选项
type Options struct {
	// EmbedLyric 是否把歌词写进音频标签
	EmbedLyric bool `json:"embed_lyric"`
	// WriteLRC 是否写出同名 .lrc 外挂歌词
	WriteLRC bool `json:"write_lrc"`
	// EmbedCover 是否把封面写进音频标签
	EmbedCover bool `json:"embed_cover"`
	// WriteFolderCover 是否写出逐曲同名封面图（不是目录级 cover.jpg）
	WriteCoverFile bool `json:"write_cover_file"`
	// OverwriteLyric 已有歌词时是否覆盖
	OverwriteLyric bool `json:"overwrite_lyric"`
	// OverwriteCover 已有封面时是否覆盖
	OverwriteCover bool `json:"overwrite_cover"`
	// WriteMetadata 是否写入标题/歌手/专辑/风格/年份/曲序/光盘
	WriteMetadata bool `json:"write_metadata"`
}

// DefaultOptions 默认选项：补歌词与封面，但不覆盖已有内容
func DefaultOptions() Options {
	return Options{
		EmbedLyric:     true,
		WriteLRC:       true,
		EmbedCover:     true,
		WriteCoverFile: false,
		OverwriteLyric: false,
		OverwriteCover: false,
		WriteMetadata:  true,
	}
}

// Result 单曲整理结果
type Result struct {
	Path        string `json:"path"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	LyricEmbed  bool   `json:"lyric_embedded"`
	LyricFile   bool   `json:"lyric_file"`
	CoverEmbed  bool   `json:"cover_embedded"`
	CoverFile   bool   `json:"cover_file"`
	MetaWritten bool   `json:"meta_written"`
	// Skipped 说明为何没有改动（如已有歌词且未开启覆盖）
	Skipped []string `json:"skipped,omitempty"`
	Format  string   `json:"format,omitempty"`
}

// TidyOne 整理单首曲目。
//
// 只处理 MP3 / FLAC（本项目 tags 包支持的范围）；其它容器会明确报错而不是静默失败。
func TidyOne(item Item, opts Options) Result {
	res := Result{Path: item.Path}

	path := strings.TrimSpace(item.Path)
	if path == "" {
		res.Error = "路径为空"
		return res
	}

	format, err := tags.SniffFormat(path)
	if err != nil {
		res.Error = "无法识别音频格式: " + err.Error()
		return res
	}
	res.Format = string(format)
	if format != tags.FormatMP3 && format != tags.FormatFLAC {
		res.Error = fmt.Sprintf("暂不支持写入 %s 格式（目前支持 MP3 / FLAC）", format)
		return res
	}

	// 读取既有状态，用于「是否覆盖」的判断
	existing, _ := tags.Read(path)
	hasLyric := strings.TrimSpace(existing.Lyric) != ""
	hasCover := len(existing.Cover) > 0

	meta := tags.Metadata{}

	if opts.WriteMetadata {
		if v := strings.TrimSpace(item.Title); v != "" {
			meta.Title = v
		}
		if v := strings.TrimSpace(item.Artist); v != "" {
			meta.Artist = v
		}
		if v := strings.TrimSpace(item.Album); v != "" {
			meta.Album = v
		}
		// 风格：飞牛音乐「风格」页读的就是内嵌 genre，不写就归不了类
		if v := strings.TrimSpace(item.Genre); v != "" {
			meta.Genre = v
		}
		// 年份/曲序/光盘：**只有 > 0 才写**（0 代表平台没给，写进去就是脏数据）
		if item.Year > 0 {
			meta.Year = item.Year
		}
		if item.Track > 0 {
			meta.Track = item.Track
		}
		if item.Disc > 0 {
			meta.Disc = item.Disc
		}
	}

	// 歌词：已有且不允许覆盖时跳过
	lyric := strings.TrimSpace(item.Lyric)
	if lyric != "" {
		if hasLyric && !opts.OverwriteLyric {
			res.Skipped = append(res.Skipped, "已有内嵌歌词，未覆盖")
		} else if opts.EmbedLyric {
			meta.Lyric = lyric
		}
	}

	// 封面
	cover := item.CoverBytes
	if len(cover) == 0 && strings.HasPrefix(strings.TrimSpace(item.CoverURL), "http") {
		// 下载交给调用方（后端有统一的 SSRF 防护与体积校验），这里只做占位判断
		res.Skipped = append(res.Skipped, "封面需由调用方下载后以 base64/字节传入")
	}
	if len(cover) > 0 {
		if hasCover && !opts.OverwriteCover {
			res.Skipped = append(res.Skipped, "已有内嵌封面，未覆盖")
		} else if opts.EmbedCover {
			meta.Cover = cover
			meta.CoverMime = item.CoverMime
		}
	}

	// 写入标签
	if !meta.IsEmpty() {
		if _, err := tags.Write(path, meta); err != nil {
			res.Error = "写入标签失败: " + err.Error()
			return res
		}
		// 「写了元数据」= 标题/歌手/专辑/风格/年份/曲序/光盘 任一。
		// 之前漏了风格与数字字段，导致「只补了 genre」时 GenreFilled 报 false。
		res.MetaWritten = meta.Title != "" || meta.Artist != "" || meta.Album != "" ||
			meta.Genre != "" || meta.Year > 0 || meta.Track > 0 || meta.Disc > 0
		res.LyricEmbed = strings.TrimSpace(meta.Lyric) != ""
		res.CoverEmbed = len(meta.Cover) > 0
	}

	// 外挂歌词：与音频同目录、同 basename
	if opts.WriteLRC && lyric != "" {
		lrcPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".lrc"
		if existingLRC := readFileString(lrcPath); existingLRC != "" && !opts.OverwriteLyric {
			res.Skipped = append(res.Skipped, "已有外挂歌词，未覆盖")
		} else {
			if err := writeFileAtomic(lrcPath, []byte(lyric)); err != nil {
				res.Skipped = append(res.Skipped, "写入外挂歌词失败: "+err.Error())
			} else {
				res.LyricFile = true
			}
		}
	}

	// 逐曲同名封面：刻意不写目录级 cover.jpg（会让整个文件夹显示同一张图）
	if opts.WriteCoverFile && len(cover) > 0 {
		ext := extForMime(item.CoverMime)
		coverPath := strings.TrimSuffix(path, filepath.Ext(path)) + ext
		if fileExists(coverPath) && !opts.OverwriteCover {
			res.Skipped = append(res.Skipped, "已有同名封面图，未覆盖")
		} else {
			if err := writeFileAtomic(coverPath, cover); err != nil {
				res.Skipped = append(res.Skipped, "写入封面图失败: "+err.Error())
			} else {
				res.CoverFile = true
			}
		}
	}

	// 只要动过文件（内嵌标签 / 外挂歌词 / 封面图），就把音频与附属文件的
	// 时间戳推到当下 —— 飞牛按「路径 + mtime/size」做增量指纹，
	// 不推它就不会重新读取（见 pkg/tags/rescan.go）
	if res.MetaWritten || res.LyricEmbed || res.CoverEmbed || res.LyricFile || res.CoverFile {
		tags.TouchForRescan(path)
	}

	res.OK = res.Error == ""
	return res
}

// BatchResult 批量整理结果
type BatchResult struct {
	Total   int      `json:"total"`
	OK      int      `json:"ok"`
	Failed  int      `json:"failed"`
	Results []Result `json:"results"`
	Errors  []string `json:"errors,omitempty"`
}

// TidyBatch 批量整理（并发上限 2，避免机械硬盘随机写抖动）。
//
// onProgress 每处理完一首回调一次，用于长任务进度上报。
func TidyBatch(items []Item, opts Options, onProgress func(done, total int, current string)) BatchResult {
	out := BatchResult{Total: len(items), Results: make([]Result, 0, len(items))}
	if len(items) == 0 {
		return out
	}

	const concurrency = 2
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(item Item) {
			defer wg.Done()
			defer func() { <-sem }()
			// 单首失败不能拖垮整批
			res := func() (r Result) {
				defer func() {
					if rec := recover(); rec != nil {
						r = Result{Path: item.Path, Error: fmt.Sprintf("内部错误: %v", rec)}
					}
				}()
				return TidyOne(item, opts)
			}()

			mu.Lock()
			out.Results = append(out.Results, res)
			if res.Error != "" {
				out.Failed++
				if len(out.Errors) < 50 {
					out.Errors = append(out.Errors, filepath.Base(item.Path)+": "+res.Error)
				}
			} else {
				out.OK++
			}
			done := out.OK + out.Failed
			mu.Unlock()

			if onProgress != nil {
				onProgress(done, len(items), filepath.Base(item.Path))
			}
		}(it)
	}
	wg.Wait()

	// 结果按路径排序，便于稳定输出与测试
	sort.Slice(out.Results, func(a, b int) bool { return out.Results[a].Path < out.Results[b].Path })
	return out
}

// NeedsTidy 快速判断一首歌是否缺少歌词或封面（用于体检，不读完整标签内容之外的东西）。
//
// 歌词判定口径与 NAS 扫描一致：**同时认同名外挂 .lrc/.txt 与内嵌歌词**，
// 避免同一首歌在 NAS 页显示"有歌词"、在曲库管家却报"缺歌词"的矛盾。
// 封面判定只认**逐曲同名图或内嵌封面**（不认目录级 cover.jpg，避免整文件夹同图）。
func NeedsTidy(path string) (needLyric, needCover bool, err error) {
	meta, err := tags.Read(path)
	if err != nil {
		return false, false, err
	}
	hasEmbedded := strings.TrimSpace(meta.Lyric) != ""
	needLyric = !hasEmbedded && !hasSidecarLyric(path)
	needCover = len(meta.Cover) == 0 && !hasSidecarCover(path)
	return needLyric, needCover, nil
}

// hasSidecarLyric 检查同目录下是否存在同名 .lrc / .txt 外挂歌词。
// 与 NAS 扫描的 checkHasLyric 保持同一套候选规则。
func hasSidecarLyric(filePath string) bool {
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	candidates := []string{
		filepath.Join(dir, base+".lrc"),
		filepath.Join(dir, base+".LRC"),
		filepath.Join(dir, base+".txt"),
	}
	if parts := strings.Split(base, " - "); len(parts) == 2 {
		title := strings.TrimSpace(parts[1])
		if title != "" {
			candidates = append(candidates,
				filepath.Join(dir, title+".lrc"),
				filepath.Join(dir, title+".LRC"),
			)
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

// hasSidecarCover 检查同目录下是否存在逐曲同名封面图（.jpg/.png/.jpeg/.webp）。
// 与刮削规则一致：**不认目录级 cover.jpg / folder.jpg**。
func hasSidecarCover(filePath string) bool {
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		c := filepath.Join(dir, base+ext)
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

// Audit 曲库体检：统计缺歌词/缺封面的曲目
type Audit struct {
	Total      int      `json:"total"`
	NoLyric    int      `json:"no_lyric"`
	NoCover    int      `json:"no_cover"`
	Both       int      `json:"both_missing"`
	Unreadable int      `json:"unreadable"`
	Missing    []string `json:"missing_sample,omitempty"`
}

// AuditPaths 对给定文件列表做体检（只读，不改动任何文件）
func AuditPaths(paths []string, sampleLimit int) Audit {
	if sampleLimit <= 0 {
		sampleLimit = 50
	}
	var a Audit
	a.Total = len(paths)
	for _, p := range paths {
		needLyric, needCover, err := NeedsTidy(p)
		if err != nil {
			a.Unreadable++
			continue
		}
		if needLyric {
			a.NoLyric++
		}
		if needCover {
			a.NoCover++
		}
		if needLyric && needCover {
			a.Both++
		}
		if (needLyric || needCover) && len(a.Missing) < sampleLimit {
			a.Missing = append(a.Missing, p)
		}
	}
	return a
}

// FilterNeedingTidy 从歌曲列表中筛选出需要整理的（供 AI 精确指定范围）
func FilterNeedingTidy(cands []match.Catalog) []match.Catalog {
	out := make([]match.Catalog, 0)
	for _, c := range cands {
		if c.ID == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ── 文件辅助 ──

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func readFileString(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// writeFileAtomic 原子写文件（临时文件 + rename），避免写入中断留下半个文件
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tidy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func extForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}
