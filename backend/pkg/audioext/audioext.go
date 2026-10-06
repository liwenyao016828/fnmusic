// Package audioext 音频扩展名的**唯一清单**。
//
// 为什么单独开一个包：这份清单以前在 pkg/library、pkg/nas、pkg/downloader、
// pkg/complete、pkg/tidy、pkg/nameparse 里各写了一份，而且内容并不一致 ——
// pkg/library 认 14 种（含 .opus/.wma/.aiff/.wv），pkg/nas 只认 9 种。
// 后果不是「风格不统一」这么轻：**同一个文件在「音库索引」里看得见、
// 在「NAS 浏览器」里看不见**，而且两份清单各自演进，偏差只会越来越大。
//
// 这里放两组，含义不同，别混用：
//
//   - Exts     能当音乐播放 / 索引的格式（扫描、浏览、计数用）
//   - Writable 本应用**能写标签**的格式（补全、整理会真往文件里写字段）
//
// Writable 远小于 Exts，因为写入器只实现了 MP3 与 FLAC（见 pkg/tags）。
// 拿 Exts 当 Writable 用，会让「补全成功」变成一句空话。
package audioext

import (
	"path/filepath"
	"sort"
	"strings"
)

// Exts 参与音乐库扫描 / 索引 / 浏览的扩展名（小写，带点）。
//
// 目录遍历不止一处（pkg/library 建索引、pkg/nas 给界面浏览），它们必须认
// 同一份清单，否则同一次操作里两个页面会互相打脸。
var Exts = map[string]bool{
	".mp3":  true,
	".flac": true,
	".wav":  true,
	".ape":  true,
	".m4a":  true,
	".aac":  true,
	".ogg":  true,
	".opus": true,
	".wma":  true,
	".wv":   true,
	".aiff": true,
	".aif":  true,
	".dsf":  true,
	".dff":  true,
}

// Writable 本应用能**写标签**的扩展名。
//
// 与 pkg/tags 的 Format 一一对应：写入器目前只有 MP3 与 FLAC 两条实现
// （见 tags.Write 的 switch）。将来扩了写入器（m4a 的 iTunes atom、
// wav 的 ID3 chunk…），**只改这里**。
//
// 界面层要用它来解释「为什么这首歌补全完还是没年份」，而不是静默跳过。
var Writable = map[string]bool{
	".mp3":  true,
	".flac": true,
}

// Has 判断路径是不是音频文件（按扩展名，大小写不敏感）。
func Has(path string) bool {
	return Exts[strings.ToLower(filepath.Ext(path))]
}

// CanWriteTags 判断路径是否属于「能写标签」的格式。
func CanWriteTags(path string) bool {
	return Writable[strings.ToLower(filepath.Ext(path))]
}

// CanWriteFormat 判断「格式名」是否属于能写标签的格式。
//
// 与 CanWriteTags 查的是同一张表，两个入口而已：曲库索引里存的是格式名
// （`library.Entry.Format`，如 "flac"，不带点），那种场合手上没有完整路径。
//
// 空字符串返回 false（老索引可能没记格式）：宁可把它单列成「不支持」，
// 也不要排进补全队列里每轮白跑一次。
func CanWriteFormat(format string) bool {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return false
	}
	if !strings.HasPrefix(f, ".") {
		f = "." + f
	}
	return Writable[f]
}

// WritableFormats 可写格式的名字列表（升序，如 ["flac","mp3"]）。
//
// 接口/界面要说清「补全能写哪些格式」时用它，别在别处再抄一份清单。
func WritableFormats() []string {
	out := make([]string, 0, len(Writable))
	for ext := range Writable {
		out = append(out, strings.TrimPrefix(ext, "."))
	}
	sort.Strings(out)
	return out
}
