// Package tags 提供音频文件标签写入能力：
//   - MP3：ID3v2.3（标题/歌手/专辑/风格/年份/曲序/光盘/封面/内嵌歌词 USLT）
//   - FLAC：Vorbis Comment（TITLE/ARTIST/ALBUM/GENRE/DATE/TRACKNUMBER/DISCNUMBER/LYRICS）+ PICTURE 封面块
//
// 设计目标：零第三方依赖、纯 Go 实现，仅改动文件头部元数据区，
// 音频数据原样保留，写入采用「临时文件 + 原子替换」避免损坏原文件。
package tags

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Format 音频容器格式
type Format string

const (
	FormatMP3     Format = "mp3"
	FormatFLAC    Format = "flac"
	FormatUnknown Format = ""
)

// Metadata 待写入的标签内容，空字段表示不修改/不写入
type Metadata struct {
	Title     string
	Artist    string
	Album     string
	Genre     string // 风格。飞牛音乐的「风格」页读的就是这个内嵌字段，不写它就归不了类
	Lyric     string // LRC 文本，内嵌写入
	Cover     []byte // 封面图片原始字节
	CoverMime string // 例如 image/jpeg、image/png

	// 飞牛「歌曲信息」里还有这三栏（2026-09-22 用户要求按字段补全）。
	// **0 表示不写入**（不是「写 0」）—— 平台没给就别写，免得把文件写脏。
	Year  int // 发行年份，如 2003
	Track int // 碟内曲序，如 3
	Disc  int // 光盘序号，如 1
}

// IsEmpty 判断是否没有任何需要写入的内容
func (m Metadata) IsEmpty() bool {
	return strings.TrimSpace(m.Title) == "" &&
		strings.TrimSpace(m.Artist) == "" &&
		strings.TrimSpace(m.Album) == "" &&
		strings.TrimSpace(m.Genre) == "" &&
		strings.TrimSpace(m.Lyric) == "" &&
		len(m.Cover) == 0 &&
		m.Year <= 0 && m.Track <= 0 && m.Disc <= 0
}

var (
	// ErrUnsupportedFormat 无法识别的音频格式
	ErrUnsupportedFormat = errors.New("不支持的音频格式（仅支持 MP3 / FLAC）")
	// ErrEmptyMetadata 没有任何可写入内容
	ErrEmptyMetadata = errors.New("没有需要写入的标签内容")
)

// atoiPrefix 取字符串开头的数字，够用于标签里的数字字段：
//
//	"3"          → 3      （TRCK/TPOS）
//	"3/12"       → 3      （曲序/总曲数）
//	"2003-07-31" → 2003   （TDRC 这类日期串）
//	""/"abc"     → 0
func atoiPrefix(s string) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// SniffFormat 依据文件魔数推断格式（不依赖扩展名）
func SniffFormat(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	head := make([]byte, 12)
	n, err := io.ReadFull(f, head)
	if err != nil && n < 4 {
		return FormatUnknown, err
	}
	return sniffBytes(head[:n]), nil
}

func sniffBytes(b []byte) Format {
	if len(b) >= 4 && bytes.Equal(b[:4], []byte("fLaC")) {
		return FormatFLAC
	}
	if len(b) >= 3 && bytes.Equal(b[:3], []byte("ID3")) {
		return FormatMP3
	}
	// MPEG 帧同步字：11 位全 1
	if len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0 {
		return FormatMP3
	}
	return FormatUnknown
}

// Write 按实际格式写入标签，返回识别到的格式。
// 采用「写临时文件 + rename」保证原子性，失败不会破坏原文件。
func Write(path string, meta Metadata) (Format, error) {
	if meta.IsEmpty() {
		return FormatUnknown, ErrEmptyMetadata
	}

	format, err := SniffFormat(path)
	if err != nil {
		return FormatUnknown, err
	}

	meta = mergeTextFields(path, meta)

	switch format {
	case FormatMP3:
		err = writeID3v2(path, meta)
	case FormatFLAC:
		err = writeFLAC(path, meta)
	default:
		return FormatUnknown, ErrUnsupportedFormat
	}
	if err != nil {
		return format, err
	}
	return format, nil
}

// mergeTextFields 把 meta 里**没传**的文本字段从文件既有标签补回来。
//
// 两种容器的写入策略都是「删掉受管字段，只按 meta 重建」
// （MP3：skipExistingID3 丢弃整个旧标签；FLAC：mergeVorbisFields 先删受管 key）。
// 也就是说没传的字段会被**静默抹掉** —— 曾经导致「只想换个封面，结果标题/歌手/专辑/
// 风格/歌词全清空」。封面另有各自的保留逻辑（FLAC 原样复用 PICTURE 块，
// MP3 在 writeID3v2 里读回），所以这里不碰 Cover，避免把字节级精确的块换成重编码。
//
// 读不动就原样返回：标签解析失败不该变成「拒绝写入」的理由。
func mergeTextFields(path string, meta Metadata) Metadata {
	old, err := Read(path)
	if err != nil {
		return meta
	}
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = old.Title
	}
	if strings.TrimSpace(meta.Artist) == "" {
		meta.Artist = old.Artist
	}
	if strings.TrimSpace(meta.Album) == "" {
		meta.Album = old.Album
	}
	if strings.TrimSpace(meta.Genre) == "" {
		meta.Genre = old.Genre
	}
	if strings.TrimSpace(meta.Lyric) == "" {
		meta.Lyric = old.Lyric
	}
	if meta.Year <= 0 {
		meta.Year = old.Year
	}
	if meta.Track <= 0 {
		meta.Track = old.Track
	}
	if meta.Disc <= 0 {
		meta.Disc = old.Disc
	}
	return meta
}

// WriteLyric 仅写入歌词的便捷方法
func WriteLyric(path, lyric string) (Format, error) {
	if strings.TrimSpace(lyric) == "" {
		return FormatUnknown, ErrEmptyMetadata
	}
	return Write(path, Metadata{Lyric: lyric})
}

// Read 读取音频文件已有的标签内容（用于校验与「是否已内嵌歌词」判断）
func Read(path string) (Metadata, error) {
	format, err := SniffFormat(path)
	if err != nil {
		return Metadata{}, err
	}
	switch format {
	case FormatMP3:
		return readID3v2(path)
	case FormatFLAC:
		return readFLAC(path)
	}
	return Metadata{}, ErrUnsupportedFormat
}

// atomicRewrite 用 produce 生成的新内容原子替换 path
func atomicRewrite(path string, produce func(src *os.File) ([]byte, error)) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}

	data, err := produce(src)
	_ = src.Close()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fntag-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("写入标签失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("同步文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	// 保持原文件权限
	if fi, statErr := os.Stat(path); statErr == nil {
		_ = os.Chmod(tmpPath, fi.Mode().Perm())
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换原文件失败: %w", err)
	}
	return nil
}
