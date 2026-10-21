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
	"io"
	"os"
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

// ── 档位（音质）判定 ──
//
// 为什么放在这个包：`pkg/intercept`（扫描/替换音质）与 `pkg/downloader`（决定要不要
// 覆盖已有文件）**必须用同一套判据**，否则会出现「界面说这条能升到 320k」而下载器
// 回一句「已存在」的互相打脸。这里只依赖容器与帧头，不依赖任何平台包。
//
// 取值与 pkg/online 的 Tier* 完全一致（有交叉用例钉住，见 intercept/quality_test.go）。

// 档位常量（与 pkg/online 的 TierLossless/Tier320k/Tier192k/Tier128k 同值）。
const (
	TierLossless = "flac"
	Tier320k     = "320k"
	Tier192k     = "192k"
	Tier128k     = "128k"
)

// TierRank 档位的高低（越大越好）。认不出的档位返回 -1：
// 「不知道」必须与「128k」分开，否则未知档位会被当成「可以升」的来源。
func TierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "flac", "flac24bit", "hires", "hi-res", "ape", "wav", "dsd", "dsf", "dff", "lossless", "sq", "无损":
		return 4
	case Tier320k, "320", "exhigh", "hq":
		return 3
	case Tier192k, "192", "higher":
		return 2
	case Tier128k, "128", "standard":
		return 1
	}
	return -1
}

// LosslessContainer 判断扩展名是不是无损容器（只按容器，不看码率）。
func LosslessContainer(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".flac", ".ape", ".wav", ".dsf", ".dff":
		return true
	}
	return false
}

// TierOfFile 读盘上这个文件**实际**的档位。
//
//   - 无损容器 → 无损。
//   - mp3 → 解第一帧的 MPEG 头拿标称码率（≥256k 算 320k、≥160k 算 192k、其余 128k）。
//   - 其它容器（m4a/aac/ogg…）→ 320k 兜底。
//
// 读不出来时也回落 320k：**宁可不升，也不拿猜出来的档位去覆盖用户的文件**。
//
// 这条口径是 2026-10-21 真机踩出来的：只看容器的话，一个 128k 的 mp3 会被判成
// 「已是各渠道最高音质」，用户真正需要升的那批反而换不了。
func TierOfFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if LosslessContainer(ext) {
		return TierLossless
	}
	if ext == ".mp3" {
		if kbps := mp3NominalKbps(path); kbps > 0 {
			switch {
			case kbps >= 256:
				return Tier320k
			case kbps >= 160:
				return Tier192k
			default:
				return Tier128k
			}
		}
	}
	return Tier320k
}

// mp3NominalKbps 解出 mp3 第一帧的标称码率（kbps）。失败返回 0。
//
// 只读文件开头 8KiB：跳过可能的 ID3v2 头，再在开头找帧同步字。
// VBR 文件第一帧常是 Xing/Info 帧（那个码率字段不代表整首），所以这个值对 VBR
// 只能算「不低于」——而调用方要回答的是「够不够高、要不要升」，取到偏低的值只会更保守。
func mp3NominalKbps(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	// 先定位音频数据起点：**不能只看开头 8KiB 就放弃** —— 真机上的 mp3 常带
	// 四五十 KB 的 ID3（封面、歌词），第一帧落在缓冲区之外（2026-10-21 踩到：
	// ffmpeg 转出来的 128k 文件 ID3 有 46KB，于是被判成「读不出来」→ 回落 320k）。
	start := int64(0)
	var hdr [10]byte
	if n, _ := io.ReadFull(f, hdr[:]); n >= 10 && string(hdr[:3]) == "ID3" {
		size := 0
		for _, b := range hdr[6:10] {
			size = size<<7 | int(b&0x7f)
		}
		start = 10 + int64(size)
		if hdr[5]&0x10 != 0 { // footer 存在
			start += 10
		}
	}
	// 无论有没有 ID3 都要 seek：上面那次探测读掉了开头 10 字节，
	// 不回到 start 就会从第 10 字节开始找同步字（0 开头的补零区找不到）。
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return 0
	}

	head := make([]byte, 16<<10)
	n, _ := io.ReadFull(f, head)
	if n < 4 {
		return 0
	}
	head = head[:n]

	// MPEG1 Layer3 与 MPEG2/2.5 Layer3 的码率表（索引 0 = free，15 = 非法）。
	mpeg1L3 := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3 := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}

	for i := 0; i+3 < n; i++ {
		if head[i] != 0xff || head[i+1]&0xe0 != 0xe0 {
			continue
		}
		version := (head[i+1] >> 3) & 0x03 // 3=MPEG1，2=MPEG2，0=MPEG2.5，1=保留
		if version == 1 {
			continue
		}
		layer := (head[i+1] >> 1) & 0x03 // 1=Layer3
		if layer != 1 {
			continue
		}
		brIdx := int(head[i+2] >> 4)
		srIdx := int((head[i+2] >> 2) & 0x03)
		if srIdx == 3 || brIdx == 0 || brIdx == 15 {
			continue
		}
		if version == 3 {
			return mpeg1L3[brIdx]
		}
		return mpeg2L3[brIdx]
	}
	return 0
}
