package tags

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ── ID3v2.3 写入（MP3） ──
//
// 采用 ID3v2.3：文本帧使用编码 0x01（UTF-16 + BOM），对中文最兼容。
// 写入策略：生成新标签置顶，其后拼接「去掉旧 ID3v2 标签后的原始音频数据」。

const (
	id3HeaderSize   = 10
	id3VersionMajor = 3
)

// id3Frame 构造一个 ID3v2.3 帧（v2.3 帧长度为普通大端 32 位，非 syncsafe）
func id3Frame(id string, body []byte) []byte {
	out := make([]byte, 0, id3HeaderSize+len(body))
	out = append(out, id...)
	n := len(body)
	out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	out = append(out, 0x00, 0x00) // flags
	return append(out, body...)
}

// encodeUTF16BOM 生成 0xFF 0xFE + UTF-16LE 字节序列
func encodeUTF16BOM(s string) []byte {
	if s == "" {
		return nil
	}
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+len(u)*2)
	b = append(b, 0xFF, 0xFE)
	for _, v := range u {
		b = append(b, byte(v), byte(v>>8))
	}
	return b
}

// textFrameBody 文本帧体：编码字节 + UTF-16 BOM 文本
func textFrameBody(s string) []byte {
	return append([]byte{0x01}, encodeUTF16BOM(s)...)
}

// asciiTextFrameBody 数字类帧的帧体：编码 0x00（ISO-8859-1）+ 纯 ASCII。
//
// ⚠️ TYER/TRCK/TPOS 按规范就是数字串，用 ASCII 写兼容性最好 ——
// 中文文本帧才需要 UTF-16（见 textFrameBody）。
func asciiTextFrameBody(s string) []byte {
	return append([]byte{0x00}, []byte(s)...)
}

// usltFrameBody 内嵌歌词帧：编码 + 语言(3) + 描述(终止) + 歌词
func usltFrameBody(lang, desc, lyric string) []byte {
	b := make([]byte, 0, 8+len(lyric)*2)
	b = append(b, 0x01)

	l := []byte(lang)
	for len(l) < 3 {
		l = append(l, ' ')
	}
	b = append(b, l[:3]...)

	b = append(b, encodeUTF16BOM(desc)...)
	b = append(b, 0x00, 0x00) // UTF-16 描述串终止符
	b = append(b, encodeUTF16BOM(lyric)...)
	return b
}

// apicFrameBody 封面帧：编码(0, latin1) + MIME(终止) + 图片类型 + 描述(终止) + 数据
func apicFrameBody(mime string, data []byte) []byte {
	if strings.TrimSpace(mime) == "" {
		mime = "image/jpeg"
	}
	b := make([]byte, 0, 8+len(mime)+len(data))
	b = append(b, 0x00)
	b = append(b, []byte(mime)...)
	b = append(b, 0x00)
	b = append(b, 0x03) // picture type: front cover
	b = append(b, 0x00) // 空描述终止符
	return append(b, data...)
}

// synchsafe 将长度编码为 4 字节 syncsafe 整数（每字节仅低 7 位有效）
func synchsafe(n int) []byte {
	return []byte{
		byte((n >> 21) & 0x7F),
		byte((n >> 14) & 0x7F),
		byte((n >> 7) & 0x7F),
		byte(n & 0x7F),
	}
}

// unsynchsafe 解析 4 字节 syncsafe 整数
func unsynchsafe(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

// buildID3v2Tag 依据 Metadata 生成完整 ID3v2.3 标签字节
func buildID3v2Tag(meta Metadata) []byte {
	var frames bytes.Buffer

	if v := strings.TrimSpace(meta.Title); v != "" {
		frames.Write(id3Frame("TIT2", textFrameBody(v)))
	}
	if v := strings.TrimSpace(meta.Artist); v != "" {
		frames.Write(id3Frame("TPE1", textFrameBody(v)))
	}
	if v := strings.TrimSpace(meta.Album); v != "" {
		frames.Write(id3Frame("TALB", textFrameBody(v)))
	}
	// TCON = 风格。飞牛音乐「风格」页靠它归类，不写就归不了
	if v := strings.TrimSpace(meta.Genre); v != "" {
		frames.Write(id3Frame("TCON", textFrameBody(v)))
	}
	if v := strings.TrimSpace(meta.Lyric); v != "" {
		frames.Write(id3Frame("USLT", usltFrameBody("chi", "", v)))
	}
	if len(meta.Cover) > 0 {
		frames.Write(id3Frame("APIC", apicFrameBody(meta.CoverMime, meta.Cover)))
	}
	// 年份：**同时写 TYER(v2.3) 与 TDRC(v2.4)**，兼容两种读法。
	// 我们文件头声明的是 v2.3，规范帧是 TYER；但不少现代播放器（含一些 NAS 应用）
	// 只认 v2.4 的 TDRC。飞牛到底读哪个**本地无法验证**，两个都写最稳
	// （见 HANDOVER §4.13 的「未验证项」）。
	if meta.Year > 0 {
		y := strconv.Itoa(meta.Year)
		frames.Write(id3Frame("TYER", asciiTextFrameBody(y)))
		frames.Write(id3Frame("TDRC", asciiTextFrameBody(y)))
	}
	if meta.Track > 0 {
		frames.Write(id3Frame("TRCK", asciiTextFrameBody(strconv.Itoa(meta.Track))))
	}
	if meta.Disc > 0 {
		frames.Write(id3Frame("TPOS", asciiTextFrameBody(strconv.Itoa(meta.Disc))))
	}

	body := frames.Bytes()
	header := make([]byte, 0, id3HeaderSize)
	header = append(header, 'I', 'D', '3', id3VersionMajor, 0x00, 0x00)
	header = append(header, synchsafe(len(body))...)
	return append(header, body...)
}

// writeID3v2 写入 MP3 标签（原子替换）
func writeID3v2(path string, meta Metadata) error {
	// ⚠️ 本次没带封面时，**保留文件里已有的 APIC**。
	//
	// MP3 的写入策略是「丢弃旧 ID3 + 只重建受管帧」（见 skipExistingID3），
	// 所以 meta.Cover 为空 = 原来内嵌的封面会被抹掉。
	// FLAC 那侧有保留逻辑（`flac.go` 的 `len(meta.Cover) == 0` 分支），MP3 一直没有 ——
	// 后果是「只想改个歌词，封面却没了」，而**飞牛只认内嵌封面**，
	// 用户看到的就是封面凭空消失（2026-09-22 反馈「飞牛里歌曲不显示封面」的成因之一）。
	if len(meta.Cover) == 0 {
		if old, err := readID3v2(path); err == nil && len(old.Cover) > 0 {
			meta.Cover = old.Cover
			if strings.TrimSpace(meta.CoverMime) == "" {
				meta.CoverMime = old.CoverMime
			}
		}
	}

	tag := buildID3v2Tag(meta)
	return atomicRewrite(path, func(src *os.File) ([]byte, error) {
		rest, err := skipExistingID3(src)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(tag)+len(rest))
		out = append(out, tag...)
		out = append(out, rest...)
		return out, nil
	})
}

// skipExistingID3 跳过文件头部已有的 ID3v2 标签，返回其后（音频）全部数据。
// 若首部不是 ID3v2，则返回包含这 10 字节在内的完整文件内容。
func skipExistingID3(src *os.File) ([]byte, error) {
	head := make([]byte, id3HeaderSize)
	n, err := io.ReadFull(src, head)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return head[:n], nil
		}
		return nil, err
	}
	head = head[:n]

	if !bytes.Equal(head[:3], []byte("ID3")) {
		rest, readErr := io.ReadAll(src)
		if readErr != nil {
			return nil, readErr
		}
		return append(head, rest...), nil
	}

	size := int64(unsynchsafe(head[6:10]))
	// flags 的 0x10 位表示存在 10 字节 footer（ID3v2.4）
	if head[5]&0x10 != 0 {
		size += id3HeaderSize
	}
	if _, err := src.Seek(size, io.SeekCurrent); err != nil {
		return nil, err
	}
	return io.ReadAll(src)
}

// ── 读取（用于校验与「已有歌词」判断） ──

// readID3v2 解析 MP3 的 ID3v2 标签内容
func readID3v2(path string) (Metadata, error) {
	var meta Metadata

	f, err := os.Open(path)
	if err != nil {
		return meta, err
	}
	defer f.Close()

	head := make([]byte, id3HeaderSize)
	if _, err := io.ReadFull(f, head); err != nil {
		return meta, err
	}
	if !bytes.Equal(head[:3], []byte("ID3")) {
		return meta, nil
	}
	size := unsynchsafe(head[6:10])
	if size <= 0 || size > 64*1024*1024 {
		return meta, nil
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(f, body); err != nil {
		return meta, nil
	}

	// v2.4 的帧长度为 syncsafe，v2.3 为普通大端
	isV24 := head[3] == 4
	pos := 0
	for pos+id3HeaderSize <= len(body) {
		id := string(body[pos : pos+4])
		if id == "\x00\x00\x00\x00" {
			break // 到达 padding 区
		}
		var frameSize int
		if isV24 {
			frameSize = unsynchsafe(body[pos+4 : pos+8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(body[pos+4 : pos+8]))
		}
		if frameSize <= 0 || pos+id3HeaderSize+frameSize > len(body) {
			break
		}
		data := body[pos+id3HeaderSize : pos+id3HeaderSize+frameSize]
		pos += id3HeaderSize + frameSize
		id = strings.TrimRight(id, "\x00")

		switch id {
		case "TIT2":
			meta.Title = decodeID3Text(data)
		case "TPE1":
			meta.Artist = decodeID3Text(data)
		case "TALB":
			meta.Album = decodeID3Text(data)
		case "TCON":
			meta.Genre = decodeID3Text(data)
		case "TYER":
			// v2.3 的年份帧
			if meta.Year == 0 {
				meta.Year = atoiPrefix(decodeID3Text(data))
			}
		case "TDRC":
			// v2.4 的录制时间，可能是 "2003" 也可能是 "2003-07-31"
			if meta.Year == 0 {
				meta.Year = atoiPrefix(decodeID3Text(data))
			}
		case "TRCK":
			meta.Track = atoiPrefix(decodeID3Text(data))
		case "TPOS":
			meta.Disc = atoiPrefix(decodeID3Text(data))
		case "USLT", "SYLT":
			meta.Lyric = decodeUSLTLyric(data)
		case "APIC":
			if mime, pic := decodeAPIC(data); len(pic) > 0 {
				meta.Cover = pic
				meta.CoverMime = mime
			}
		}
	}
	return meta, nil
}

// decodeID3Text 依据首字节编码标识解码文本帧
func decodeID3Text(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	enc := data[0]
	raw := data[1:]

	switch enc {
	case 0x00: // ISO-8859-1
		runes := make([]rune, 0, len(raw))
		for _, b := range raw {
			runes = append(runes, rune(b))
		}
		return strings.Trim(string(runes), "\x00")
	case 0x01: // UTF-16 + BOM
		return strings.Trim(decodeUTF16(raw), "\x00")
	case 0x02: // UTF-16BE，无 BOM
		return strings.Trim(decodeUTF16BE(raw), "\x00")
	case 0x03: // UTF-8
		return strings.Trim(string(raw), "\x00")
	}
	if utf8.Valid(raw) {
		return strings.Trim(string(raw), "\x00")
	}
	return ""
}

func decodeUTF16(raw []byte) string {
	if len(raw) < 2 {
		return ""
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if raw[0] == 0xFE && raw[1] == 0xFF {
		order = binary.BigEndian
		raw = raw[2:]
	} else if raw[0] == 0xFF && raw[1] == 0xFE {
		raw = raw[2:]
	}
	return utf16BytesToString(raw, order)
}

func decodeUTF16BE(raw []byte) string {
	return utf16BytesToString(raw, binary.BigEndian)
}

func utf16BytesToString(raw []byte, order binary.ByteOrder) string {
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		v := order.Uint16(raw[i : i+2])
		if v == 0 {
			break
		}
		u = append(u, v)
	}
	return string(utf16.Decode(u))
}

// decodeUSLTLyric 解析 USLT 帧：编码 + 语言(3) + 描述(终止) + 歌词
func decodeUSLTLyric(data []byte) string {
	if len(data) < 4 {
		return ""
	}
	enc := data[0]
	content := data[4:] // 跳过编码 + 3 字节语言

	// 跳过描述串
	if enc == 0x01 || enc == 0x02 {
		i := 0
		for i+1 < len(content) {
			if content[i] == 0 && content[i+1] == 0 {
				i += 2
				break
			}
			i += 2
		}
		content = content[i:]
	} else {
		if idx := bytes.IndexByte(content, 0); idx >= 0 {
			content = content[idx+1:]
		}
	}

	withEnc := append([]byte{enc}, content...)
	return decodeID3Text(withEnc)
}

// decodeAPIC 解析 APIC 帧，返回 MIME 与图片数据
func decodeAPIC(data []byte) (string, []byte) {
	if len(data) < 4 {
		return "", nil
	}
	enc := data[0]
	rest := data[1:]

	// MIME 始终为 latin1 且以 0 结尾
	mimeEnd := bytes.IndexByte(rest, 0)
	if mimeEnd < 0 {
		return "", nil
	}
	mime := string(rest[:mimeEnd])
	rest = rest[mimeEnd+1:]

	if len(rest) < 1 {
		return mime, nil
	}
	rest = rest[1:] // 图片类型

	// 描述串
	if enc == 0x01 || enc == 0x02 {
		i := 0
		for i+1 < len(rest) {
			if rest[i] == 0 && rest[i+1] == 0 {
				i += 2
				break
			}
			i += 2
		}
		rest = rest[i:]
	} else {
		if idx := bytes.IndexByte(rest, 0); idx >= 0 {
			rest = rest[idx+1:]
		}
	}
	return mime, rest
}

// HasLyric 判断音频文件是否已内嵌歌词
func HasLyric(path string) bool {
	format, err := SniffFormat(path)
	if err != nil {
		return false
	}
	switch format {
	case FormatMP3:
		meta, err := readID3v2(path)
		return err == nil && strings.TrimSpace(meta.Lyric) != ""
	case FormatFLAC:
		meta, err := readFLAC(path)
		return err == nil && strings.TrimSpace(meta.Lyric) != ""
	}
	return false
}
