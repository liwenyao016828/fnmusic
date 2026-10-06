package tags

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// ── FLAC 写入：Vorbis Comment（含 LYRICS）+ PICTURE 封面块 ──

type flacBlock struct {
	blockType byte
	data      []byte
}

// FLAC 元数据块类型
const (
	flacBlockStreamInfo    = 0
	flacBlockPadding       = 1
	flacBlockApplication   = 2
	flacBlockSeekTable     = 3
	flacBlockVorbisComment = 4
	flacBlockCueSheet      = 5
	flacBlockPicture       = 6
)

// 由本程序接管（重建）的 Vorbis 字段，其余字段原样保留
var managedVorbisKeys = map[string]bool{
	"TITLE":          true,
	"ARTIST":         true,
	"ALBUM":          true,
	"GENRE":          true,
	"LYRICS":         true,
	"UNSYNCEDLYRICS": true,
	// 飞牛「歌曲信息」里的年份/曲序/光盘（2026-09-22 新增）。
	// DATE 与 YEAR 都列进来：写的时候两个都产出（见 mergeVorbisFields），读回时互为兜底。
	"DATE":        true,
	"YEAR":        true,
	"TRACKNUMBER": true,
	"DISCNUMBER":  true,
}

func metaBlockHeader(last bool, blockType byte, length int) []byte {
	b0 := blockType & 0x7F
	if last {
		b0 |= 0x80
	}
	return []byte{b0, byte(length >> 16), byte(length >> 8), byte(length)}
}

// parseFLACBlocks 读取全部元数据块，读取结束后文件指针停在音频帧起始处
func parseFLACBlocks(f *os.File) ([]flacBlock, error) {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, err
	}
	if !bytes.Equal(magic, []byte("fLaC")) {
		return nil, ErrUnsupportedFormat
	}

	blocks := make([]flacBlock, 0, 8)
	for i := 0; ; i++ {
		if i > 1024 {
			return nil, errors.New("FLAC 元数据块数量异常")
		}
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return nil, err
		}
		last := hdr[0]&0x80 != 0
		blockType := hdr[0] & 0x7F
		length := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		if blockType == 127 {
			return nil, errors.New("FLAC 元数据块类型非法")
		}
		data := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(f, data); err != nil {
				return nil, err
			}
		}
		blocks = append(blocks, flacBlock{blockType: blockType, data: data})
		if last {
			break
		}
	}
	return blocks, nil
}

// parseVorbisFields 解析 Vorbis Comment 块，返回 "KEY=value" 列表
func parseVorbisFields(data []byte) []string {
	pos := 0
	readLE32 := func() (int, bool) {
		if pos+4 > len(data) {
			return 0, false
		}
		v := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4
		return v, true
	}

	vendorLen, ok := readLE32()
	if !ok || vendorLen < 0 || pos+vendorLen > len(data) {
		return nil
	}
	pos += vendorLen

	count, ok := readLE32()
	if !ok {
		return nil
	}
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		n, ok := readLE32()
		if !ok || n < 0 || pos+n > len(data) {
			break
		}
		out = append(out, string(data[pos:pos+n]))
		pos += n
	}
	return out
}

// mergeVorbisFields 丢弃受管字段后追加新值，保留其它自定义字段
func mergeVorbisFields(existing []string, meta Metadata) []string {
	out := make([]string, 0, len(existing)+4)
	for _, f := range existing {
		key := f
		if i := strings.IndexByte(f, '='); i >= 0 {
			key = f[:i]
		}
		if managedVorbisKeys[strings.ToUpper(strings.TrimSpace(key))] {
			continue
		}
		out = append(out, f)
	}
	if v := strings.TrimSpace(meta.Title); v != "" {
		out = append(out, "TITLE="+v)
	}
	if v := strings.TrimSpace(meta.Artist); v != "" {
		out = append(out, "ARTIST="+v)
	}
	if v := strings.TrimSpace(meta.Album); v != "" {
		out = append(out, "ALBUM="+v)
	}
	// GENRE = 风格。飞牛音乐「风格」页靠它归类，不写就归不了
	if v := strings.TrimSpace(meta.Genre); v != "" {
		out = append(out, "GENRE="+v)
	}
	if v := strings.TrimSpace(meta.Lyric); v != "" {
		out = append(out, "LYRICS="+v)
	}
	// 年份写两份：DATE（ISO 8601，其它工具认）+ YEAR（纯数字 4 位）。
	//
	// 为什么不是"只写一个就够"：参考实现 music-meta-web v1.4.8 在真机上对照过
	// （writer.py:11-15 / 127-143）—— 飞牛服务端读 FLAC/OGG 年份**只认 YEAR**，
	// 只写 date 的文件服务端返回 year=null，「歌曲信息」年份栏是空的。
	// 这与 MP3 侧同时写 TYER + TDRC 是同一个思路（见 id3v2.go 的说明）。
	// 读的时候两个都认（见 readFLAC）。
	if meta.Year > 0 {
		year := strconv.Itoa(meta.Year)
		out = append(out, "DATE="+year, "YEAR="+year)
	}
	if meta.Track > 0 {
		out = append(out, "TRACKNUMBER="+strconv.Itoa(meta.Track))
	}
	if meta.Disc > 0 {
		out = append(out, "DISCNUMBER="+strconv.Itoa(meta.Disc))
	}
	return out
}

// buildVorbisComment 构造 Vorbis Comment 块（长度字段均为小端）
func buildVorbisComment(vendor string, fields []string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(vendor)))
	b.WriteString(vendor)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(fields)))
	for _, f := range fields {
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(f)))
		b.WriteString(f)
	}
	return b.Bytes()
}

// buildPictureBlock 构造 FLAC PICTURE 块（长度字段均为大端）
func buildPictureBlock(mime string, data []byte, picType uint32) []byte {
	if strings.TrimSpace(mime) == "" {
		mime = "image/jpeg"
	}
	w, h, depth := imageDimensions(data)

	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, picType)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(mime)))
	b.WriteString(mime)
	_ = binary.Write(&b, binary.BigEndian, uint32(0)) // 描述为空
	_ = binary.Write(&b, binary.BigEndian, uint32(w))
	_ = binary.Write(&b, binary.BigEndian, uint32(h))
	_ = binary.Write(&b, binary.BigEndian, uint32(depth))
	_ = binary.Write(&b, binary.BigEndian, uint32(0)) // 索引色数量
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// parsePictureBlock 解析 FLAC PICTURE 块
func parsePictureBlock(data []byte) (string, []byte) {
	pos := 0
	readBE32 := func() (int, bool) {
		if pos+4 > len(data) {
			return 0, false
		}
		v := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		return v, true
	}

	if _, ok := readBE32(); !ok { // 图片类型
		return "", nil
	}
	mimeLen, ok := readBE32()
	if !ok || mimeLen < 0 || pos+mimeLen > len(data) {
		return "", nil
	}
	mime := string(data[pos : pos+mimeLen])
	pos += mimeLen

	descLen, ok := readBE32()
	if !ok || descLen < 0 || pos+descLen > len(data) {
		return mime, nil
	}
	pos += descLen

	// width / height / depth / colors
	for i := 0; i < 4; i++ {
		if _, ok := readBE32(); !ok {
			return mime, nil
		}
	}
	dataLen, ok := readBE32()
	if !ok || dataLen < 0 || pos+dataLen > len(data) {
		return mime, nil
	}
	return mime, data[pos : pos+dataLen]
}

// writeFLAC 写入 FLAC 标签（原子替换），音频帧原样保留
func writeFLAC(path string, meta Metadata) error {
	return atomicRewrite(path, func(src *os.File) ([]byte, error) {
		blocks, err := parseFLACBlocks(src)
		if err != nil {
			return nil, err
		}

		var streamInfo []byte
		var existingPicture *flacBlock
		var fields []string
		others := make([]flacBlock, 0, len(blocks))

		for i := range blocks {
			b := blocks[i]
			switch b.blockType {
			case flacBlockStreamInfo:
				streamInfo = b.data
			case flacBlockVorbisComment:
				fields = append(fields, parseVorbisFields(b.data)...)
			case flacBlockPicture:
				if existingPicture == nil {
					cp := b
					existingPicture = &cp
				}
			case flacBlockPadding:
				// 丢弃 padding，由重建后的布局决定空间
			default:
				others = append(others, b)
			}
		}

		if streamInfo == nil {
			return nil, errors.New("FLAC 缺少 STREAMINFO 块")
		}

		// 组装新的元数据块序列：STREAMINFO 必须首位
		rebuilt := make([]flacBlock, 0, len(others)+2)
		rebuilt = append(rebuilt, flacBlock{blockType: flacBlockStreamInfo, data: streamInfo})
		rebuilt = append(rebuilt, others...)

		fields = mergeVorbisFields(fields, meta)
		vc := buildVorbisComment("fnmusic", fields)
		rebuilt = append(rebuilt, flacBlock{blockType: flacBlockVorbisComment, data: vc})

		if len(meta.Cover) > 0 {
			pb := buildPictureBlock(meta.CoverMime, meta.Cover, 3)
			rebuilt = append(rebuilt, flacBlock{blockType: flacBlockPicture, data: pb})
		} else if existingPicture != nil {
			rebuilt = append(rebuilt, *existingPicture)
		}

		out := make([]byte, 0, 4+len(streamInfo)+len(vc)+4096)
		out = append(out, 'f', 'L', 'a', 'C')
		for i, b := range rebuilt {
			out = append(out, metaBlockHeader(i == len(rebuilt)-1, b.blockType, len(b.data))...)
			out = append(out, b.data...)
		}

		// 拼接音频帧（src 已停在音频起始处）
		audio, err := io.ReadAll(src)
		if err != nil {
			return nil, err
		}
		return append(out, audio...), nil
	})
}

// readFLAC 解析 FLAC 标签内容
func readFLAC(path string) (Metadata, error) {
	var meta Metadata

	f, err := os.Open(path)
	if err != nil {
		return meta, err
	}
	defer f.Close()

	blocks, err := parseFLACBlocks(f)
	if err != nil {
		return meta, err
	}

	for _, b := range blocks {
		switch b.blockType {
		case flacBlockVorbisComment:
			for _, fld := range parseVorbisFields(b.data) {
				i := strings.IndexByte(fld, '=')
				if i < 0 {
					continue
				}
				key := strings.ToUpper(strings.TrimSpace(fld[:i]))
				val := fld[i+1:]
				switch key {
				case "TITLE":
					meta.Title = val
				case "ARTIST":
					meta.Artist = val
				case "ALBUM":
					meta.Album = val
				case "GENRE":
					meta.Genre = val
				case "LYRICS", "UNSYNCEDLYRICS":
					meta.Lyric = val
				case "DATE":
					// DATE 可能是 "2003" 也可能是 "2003-07-31"
					if meta.Year == 0 {
						meta.Year = atoiPrefix(val)
					}
				case "YEAR":
					if meta.Year == 0 {
						meta.Year = atoiPrefix(val)
					}
				case "TRACKNUMBER":
					meta.Track = atoiPrefix(val)
				case "DISCNUMBER":
					meta.Disc = atoiPrefix(val)
				}
			}
		case flacBlockPicture:
			mime, pic := parsePictureBlock(b.data)
			if len(pic) > 0 && len(meta.Cover) == 0 {
				meta.Cover = pic
				meta.CoverMime = mime
			}
		}
	}
	return meta, nil
}
