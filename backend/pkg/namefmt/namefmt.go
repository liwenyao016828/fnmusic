// Package namefmt 统一「下载 / 入库文件名」的构造方向与反解析。
//
// 为什么需要它：文件名的形状（`歌手 - 歌名` 还是 `歌名 - 歌手`）同时被**两个
// 方向**使用 —— 写入侧（下载器、边听边存的暂存名）按它拼文件名，读取侧（曲库
// 扫描、查重、外挂歌词候选）又要从文件名把歌手 / 歌名拆回来。
//
// 两边各自写死一份分隔符时，一旦顺序改了，写入侧看着完全正常，读取侧却会把
// 歌手和歌名**反过来**写进曲库 —— 这是静默的数据损坏，不是报错。
//
// 所以方向只存一份，两个方向都从这里取。
//
// 它是**叶子包**（不依赖任何本项目包），任何包都能引用而不会引入循环依赖。
package namefmt

import (
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// 支持的两种书写方向。
const (
	// ArtistTitle 是「歌手 - 歌名」。历史默认值，也是**当前默认值** ——
	// 老曲库里的文件全是这个方向，换默认值会让扫描把已有文件读反。
	ArtistTitle = "artist-title"
	// TitleArtist 是「歌名 - 歌手」。
	TitleArtist = "title-artist"
	// sep 是歌手与歌名之间的分隔符。
	sep = " - "
)

// current 是当前生效的方向。用 atomic 而不是普通变量：它在配置保存时被写入，
// 却被下载、扫描等多个 goroutine 并发读。
var current atomic.Value

func init() {
	current.Store(ArtistTitle)
}

// Normalize 归一化外部传入的方向值：认不出的一律回落到默认值。
//
// 这里**刻意不报错**：这个值可能来自老配置（没有这个键）或者将来新增而当前
// 版本还不认识的值，让配置读不进来、程序起不来是最糟的处理方式。
func Normalize(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), TitleArtist) {
		return TitleArtist
	}
	return ArtistTitle
}

// Set 设置当前方向，返回归一化后**实际生效**的值（调用方据此回显）。
func Set(v string) string {
	n := Normalize(v)
	current.Store(n)
	return n
}

// Current 返回当前生效的方向。
func Current() string {
	if v, ok := current.Load().(string); ok && v != "" {
		return v
	}
	return ArtistTitle
}

// IsTitleArtist 报告当前是否「歌名在前」。
func IsTitleArtist() bool { return Current() == TitleArtist }

// MaxBaseBytes 是文件名主干（不含扩展名、不含 " (2)" 这类去重后缀）的字节上限。
//
// 为什么必须有一个总上限：filename.Sanitize 是**逐段**截到 180 字节的，歌手和
// 歌名各占满 180，中间再加 " - "，主干就接近 370 字节；加上扩展名和去重后缀，
// 已经越过多数文件系统的 255 字节上限（ext4/FUSE/NTFS 同名），表现为
// ENAMETOOLONG —— 这首歌**每次下载都失败**，而且 .part 残件按同一个名字生成，
// 永远清不掉（2026-11-20 审计 M1）。
//
// 180 字节的预算给扩展名（.flac）、去重后缀（" (12)" ~ 5 字节）和收尾留足余量。
const MaxBaseBytes = 180

// Clamp 把字符串按字节截到上限，且不切断 UTF-8 字符。
//
// 切在半个汉字上会得到乱码文件名（扫库时再被当非法 UTF-8 洗成 _），
// 所以这里按 rune 边界回退；顺手把截断处留下的 " - " 尾巴去掉。
func Clamp(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	cut = strings.TrimSpace(cut)
	cut = strings.TrimSpace(strings.TrimRight(cut, "-"))
	return cut
}

// Build 按当前方向拼出文件名主干（**不含扩展名**）。
//
// 两个入参都应已经过调用方的非法字符清洗（见 downloader.sanitizeFilename）。
// 结果按 MaxBaseBytes 再夹一次总长（见上面的说明）。
//
// 有一边为空时**不拼分隔符**：老写法会拼出 "- 歌名" 这种带前导破折号的名字，
// 扫库时再被当成「歌名是 "- 歌名"」，界面上一眼看过去像是坏文件。
func Build(artist, title string) string {
	artist = strings.TrimSpace(artist)
	title = strings.TrimSpace(title)
	var s string
	switch {
	case artist == "":
		s = title
	case title == "":
		s = artist
	case IsTitleArtist():
		s = title + sep + artist
	default:
		s = artist + sep + title
	}
	return Clamp(s, MaxBaseBytes)
}

// Parse 从文件名主干（不含扩展名）拆回歌手与歌名，与 Build 严格互逆。
//
// 两个刻意的选择：
//
//  1. 只在**第一个**分隔符上切一刀。歌名里本来就可能有「 - 」（比如
//     `Love - Live`），用 Split 会把它切碎、后半截直接丢掉；这里的规则是
//     「Artist - Love - Live」→ artist=Artist、title=Love - Live。
//  2. 切不出分隔符、或者切出来有一边是空的，就当「没有歌手」处理：返回
//     artist 为空、title = 整个主干，由调用方决定兜底文案（扫描层用的是
//     「本地歌手」）。这样裸文件名不会变成 artist=整串。
func Parse(base string) (artist, title string) {
	i := strings.Index(base, sep)
	if i < 0 {
		return "", strings.TrimSpace(base)
	}
	left := strings.TrimSpace(base[:i])
	right := strings.TrimSpace(base[i+len(sep):])
	if left == "" || right == "" {
		return "", strings.TrimSpace(base)
	}
	if IsTitleArtist() {
		return right, left
	}
	return left, right
}
