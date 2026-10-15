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

// Build 按当前方向拼出文件名主干（**不含扩展名**）。
//
// 两个入参都应已经过调用方的非法字符清洗（见 downloader.sanitizeFilename）。
func Build(artist, title string) string {
	if IsTitleArtist() {
		return title + sep + artist
	}
	return artist + sep + title
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
