// Package flacok 校验一个 FLAC 文件是不是「完好」的 —— 逐帧走一遍结构，
// 用帧头 CRC-8 与整帧 CRC-16 逐帧验签。
//
// 为什么需要它：酷我等音源的无损直链偶尔会返回**文件头正常、正文含坏帧**的流
// （文件头合法、扩展名 .flac、体积也正常，但播到一半就坏）。以前下载侧只校验
// HTTP 状态码与文件魔数，坏流会被照常改名入库，表现是「下载成功但播不了 /
// 转码下载失败（errno 68157441 transcoding error）」。参见参考实现
// fnmusic-ext v2.6.3 的三层防护（它的做法是 ffmpeg 解码探针；本机没有 ffmpeg，
// 所以用帧级结构 + CRC 校验等价替代）。
//
// 校验的判据是**CRC**，不是启发式：CRC-16 覆盖整个帧（含子帧数据），
// 对得上就说明这一帧逐字节完好，对不上就一定是坏的（误判概率 1/65536）。
// 所以这个校验器一报错就该重下，而不是「可能有点小问题」—— 它不会冤枉好文件。
//
// 走帧需要解析子帧（定长/变长分区 Rice 编码），因为只有走到帧尾才知道 CRC-16
// 该盖住哪些字节。这里只**数位**不还原采样，所以比一个真正的解码器轻得多。
package flacok

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// 校验失败的几种典型原因（导出为错误变量，调用方可据此决定要不要换档重下）。
var (
	// ErrNotFLAC 文件不是 FLAC（魔数不对 / 空文件）
	ErrNotFLAC = errors.New("不是 FLAC 文件")
	// ErrHeaderCorrupt 元数据区损坏（块头非法、STREAMINFO 缺失或长度不对）
	ErrHeaderCorrupt = errors.New("FLAC 元数据区损坏")
	// ErrFrameCorrupt 音频帧损坏（同步码丢失、结构非法、CRC 对不上）
	ErrFrameCorrupt = errors.New("FLAC 音频帧损坏")

	errReservedBit = errors.New("保留位非 0")
	errBadUTF8     = errors.New("帧号编码非法")
)

// maxMetaBlocks 元数据块数量上限：真文件最多十来块（STREAMINFO/PADDING/VORBIS/
// PICTURE/SEEKTABLE），超过就当损坏，避免坏文件把循环拖死。
const maxMetaBlocks = 256

// streamInfo 是走帧要用到的 STREAMINFO 字段（帧头里码值为 0 时从这里取）。
type streamInfo struct {
	sampleRate int // Hz
	channels   int
	bps        int // 采样位宽
}

// ValidateFile 校验 path 指向的 FLAC 文件是否完好。
//
// 返回 nil 表示可以入库；非 nil 一定是「不该入库」（调用方应丢弃并换档重下）。
func ValidateFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return Validate(f)
}

// Validate 校验一个 FLAC 字节流。见 ValidateFile。
func Validate(r io.Reader) error {
	br := newBitReader(r)

	// 允许文件最前面挂一段 ID3v2（有些工具会这么干）：跳过它再找 fLaC。
	skipID3(br)

	// ── 魔数 ──
	var magic [4]byte
	for i := range magic {
		b, ok := br.readByteNoCRC()
		if !ok {
			return ErrNotFLAC
		}
		magic[i] = b
	}
	if magic != [4]byte{'f', 'L', 'a', 'C'} {
		return ErrNotFLAC
	}

	// ── 元数据块 ──
	var si streamInfo
	sawStreamInfo := false
	for i := 0; ; i++ {
		if i >= maxMetaBlocks {
			return ErrHeaderCorrupt
		}
		b0, ok := br.readByteNoCRC()
		if !ok {
			return ErrHeaderCorrupt
		}
		last := b0&0x80 != 0
		blockType := b0 & 0x7F
		if blockType == 127 {
			return ErrHeaderCorrupt // 127 会与 0xFF 开头的帧同步码混淆，规范禁止
		}
		var lenBytes [3]byte
		for j := range lenBytes {
			c, ok := br.readByteNoCRC()
			if !ok {
				return ErrHeaderCorrupt
			}
			lenBytes[j] = c
		}
		length := int(lenBytes[0])<<16 | int(lenBytes[1])<<8 | int(lenBytes[2])
		if i == 0 && (blockType != 0 || length != 34) {
			// STREAMINFO 必须是第一块且固定 34 字节，否则编码器不认
			return ErrHeaderCorrupt
		}
		if blockType == 0 {
			if length != 34 {
				return ErrHeaderCorrupt
			}
			if sawStreamInfo {
				return ErrHeaderCorrupt
			}
			sawStreamInfo = true
		}

		if blockType == 0 {
			data := make([]byte, length)
			for j := 0; j < length; j++ {
				c, ok := br.readByteNoCRC()
				if !ok {
					return ErrHeaderCorrupt
				}
				data[j] = c
			}
			si = parseStreamInfo(data)
			if si.channels < 1 || si.bps < 4 {
				return ErrHeaderCorrupt
			}
		} else {
			// 其余块（PADDING / VORBIS_COMMENT / PICTURE / SEEKTABLE …）不影响走帧，
			// 直接跳过 —— PICTURE 可能好几 MB，不能整块读进内存。
			if n, err := br.r.Discard(length); err != nil || n != length {
				return ErrHeaderCorrupt
			}
		}
		if last {
			break
		}
	}
	if !sawStreamInfo {
		return ErrHeaderCorrupt
	}

	// ── 音频帧 ──
	frames := 0
	for {
		ok, err := nextFrame(br, si)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		frames++
	}
	if frames == 0 {
		// 只有元数据没有音频帧 = 只下了一小截（或根本是提示音频）
		return fmt.Errorf("%w：没有任何音频帧", ErrFrameCorrupt)
	}
	return nil
}

// parseStreamInfo 从 34 字节的 STREAMINFO 数据里取出走帧要用的三项。
// 布局：min/max 块长 16+16、min/max 帧长 24+24、采样率 20、声道数-1 3、
// 采样位宽-1 5、总采样数 36、MD5 16 字节。
func parseStreamInfo(data []byte) streamInfo {
	// 前 10 字节 = 块长与帧长字段，跳过；接下来 8 字节是采样率/声道/位宽/总采样数
	v := uint64(data[10])<<56 | uint64(data[11])<<48 | uint64(data[12])<<40 |
		uint64(data[13])<<32 | uint64(data[14])<<24 | uint64(data[15])<<16 |
		uint64(data[16])<<8 | uint64(data[17])
	sampleRate := int(v >> 44 & 0xFFFFF)
	channels := int(v>>41&0x7) + 1
	bps := int(v>>36&0x1F) + 1
	return streamInfo{sampleRate: sampleRate, channels: channels, bps: bps}
}

// skipID3 跳过文件最前面可能有的 ID3v2 标签（10 字节头 + 同步安全长度）。
func skipID3(br *bitReader) {
	p, err := br.r.Peek(3)
	if err != nil || string(p) != "ID3" {
		return
	}
	hdr := make([]byte, 10)
	for i := range hdr {
		c, ok := br.readByteNoCRC()
		if !ok {
			return
		}
		hdr[i] = c
	}
	// 同步安全整数：4 字节，每字节只用低 7 位
	size := int(hdr[6]&0x7F)<<21 | int(hdr[7]&0x7F)<<14 | int(hdr[8]&0x7F)<<7 | int(hdr[9]&0x7F)
	if hdr[5]&0x10 != 0 {
		size += 10 // 有 footer
	}
	for i := 0; i < size; i++ {
		if _, ok := br.readByteNoCRC(); !ok {
			return
		}
	}
}

// nextFrame 走一帧。返回 (false, nil) 表示流已经干净地结束（遇到尾部标签或 EOF
// 且此前已有帧）；返回 (false, err) 表示这一帧坏了。
func nextFrame(br *bitReader, si streamInfo) (bool, error) {
	// 尾部标签：有些工具会在 FLAC 后面追加 APEv2 / ID3 / Lyrics3 标签。
	// 它们不是音频帧，看到就当正常结束，否则会把好文件判坏。
	if br.peekTag() {
		return false, nil
	}

	br.startFrame()

	// 同步码 14 位 = 0b11111111111110
	sync, err := br.readBits(14)
	if err != nil {
		return false, nil // 流在帧边界处结束（正常）
	}
	if sync != 0x3FFE {
		return false, fmt.Errorf("%w：同步码丢失（0x%04X）", ErrFrameCorrupt, sync)
	}
	if err := br.wantZeroBit("帧头保留位"); err != nil {
		return false, fmt.Errorf("%w：帧头被截断或保留位非 0", ErrFrameCorrupt)
	}
	blockingStrategy, err := br.readBits(1)
	if err != nil {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	_ = blockingStrategy // 只影响帧号语义，不影响走帧长度

	blockSizeCode, err := br.readBits(4)
	if err != nil {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	sampleRateCode, err := br.readBits(4)
	if err != nil {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	channelAssignment, err := br.readBits(4)
	if err != nil {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	sampleSizeCode, err := br.readBits(3)
	if err != nil {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	if err := br.wantZeroBit("帧头保留位"); err != nil {
		return false, fmt.Errorf("%w：帧头被截断或保留位非 0", ErrFrameCorrupt)
	}

	channels, sideChannel, err := channelCount(channelAssignment)
	if err != nil {
		return false, err
	}
	bitsPerSample, err := sampleSize(sampleSizeCode, si)
	if err != nil {
		return false, err
	}

	// UTF-8 编码的帧号（定长块）或采样号（变长块），最多 7 字节
	if _, err := br.readUTF8Number(); err != nil {
		if errors.Is(err, errBadUTF8) {
			return false, fmt.Errorf("%w：帧号编码非法", ErrFrameCorrupt)
		}
		return false, fmt.Errorf("%w：帧号被截断", ErrFrameCorrupt)
	}

	blockSize, err := readBlockSize(br, blockSizeCode)
	if err != nil {
		return false, err
	}
	if err := skipSampleRate(br, sampleRateCode, si); err != nil {
		return false, err
	}

	// 帧头 CRC-8（覆盖到 CRC-8 字节之前的所有帧头字节）。
	// CRC-8 字节自身仍算进整帧 CRC-16，所以用 readFrameByte 读。
	wantCRC8 := br.crc8
	gotCRC8, ok := br.readFrameByte()
	br.inHeader = false
	if !ok {
		return false, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
	}
	if gotCRC8 != wantCRC8 {
		return false, fmt.Errorf("%w：帧头 CRC-8 不符（%02X != %02X）", ErrFrameCorrupt, gotCRC8, wantCRC8)
	}

	// ── 子帧 ──
	for ch := 0; ch < channels; ch++ {
		width := bitsPerSample
		if ch == sideChannel {
			width++ // 边声道比采样位宽多一位
		}
		if err := skipSubframe(br, blockSize, width); err != nil {
			return false, err
		}
	}

	// 字节对齐（补 0 到字节边界）
	br.alignByte()

	// 整帧 CRC-16（覆盖同步码到对齐填充字节为止，不含 CRC-16 自身）
	wantCRC16 := br.crc16
	hi, ok := br.readByteNoCRC()
	if !ok {
		return false, fmt.Errorf("%w：帧尾被截断", ErrFrameCorrupt)
	}
	lo, ok := br.readByteNoCRC()
	if !ok {
		return false, fmt.Errorf("%w：帧尾被截断", ErrFrameCorrupt)
	}
	gotCRC16 := uint16(hi)<<8 | uint16(lo)
	if gotCRC16 != wantCRC16 {
		return false, fmt.Errorf("%w：整帧 CRC-16 不符（%04X != %04X）", ErrFrameCorrupt, gotCRC16, wantCRC16)
	}
	return true, nil
}

// channelCount 把 4 位声道赋值码拆成「声道数」与「哪一路是差值声道」。
func channelCount(code uint64) (channels int, sideChannel int, err error) {
	switch {
	case code <= 7:
		return int(code) + 1, -1, nil
	case code == 8: // left/side：0=左，1=边
		return 2, 1, nil
	case code == 9: // right/side：0=边，1=右
		return 2, 0, nil
	case code == 10: // mid/side：0=中，1=边
		return 2, 1, nil
	default:
		return 0, -1, fmt.Errorf("%w：声道赋值码非法（%d）", ErrFrameCorrupt, code)
	}
}

// sampleSize 把 3 位采样位数码拆成位数；码 0 = 「从 STREAMINFO 取」。
func sampleSize(code uint64, si streamInfo) (int, error) {
	switch code {
	case 0:
		if si.bps < 4 {
			return 0, fmt.Errorf("%w：STREAMINFO 里的采样位宽非法", ErrHeaderCorrupt)
		}
		return si.bps, nil
	case 1:
		return 8, nil
	case 2:
		return 12, nil
	case 4:
		return 16, nil
	case 5:
		return 20, nil
	case 6:
		return 24, nil
	case 7:
		return 32, nil
	default:
		return 0, fmt.Errorf("%w：采样位数码保留（%d）", ErrFrameCorrupt, code)
	}
}

// readBlockSize 解析块长码；6/7 表示块长写在帧头末尾。
func readBlockSize(br *bitReader, code uint64) (int, error) {
	switch {
	case code == 0:
		return 0, fmt.Errorf("%w：块长码保留", ErrFrameCorrupt)
	case code == 1:
		return 192, nil
	case code >= 2 && code <= 5:
		return 576 << (code - 2), nil
	case code == 6:
		v, err := br.readBits(8)
		if err != nil {
			return 0, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
		}
		return int(v) + 1, nil
	case code == 7:
		v, err := br.readBits(16)
		if err != nil {
			return 0, fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
		}
		return int(v) + 1, nil
	default:
		return 256 << (code - 8), nil
	}
}

// skipSampleRate 把采样率字段读掉（不参与走帧长度，但必须消耗掉，否则错位）。
// 码 0 = 取自 STREAMINFO，此时没有附加字节。
func skipSampleRate(br *bitReader, code uint64, si streamInfo) error {
	switch code {
	case 0:
		if si.sampleRate <= 0 {
			return fmt.Errorf("%w：STREAMINFO 里的采样率非法", ErrHeaderCorrupt)
		}
		return nil
	case 12:
		_, err := br.readBits(8)
		if err != nil {
			return fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
		}
		return nil
	case 13, 14:
		_, err := br.readBits(16)
		if err != nil {
			return fmt.Errorf("%w：帧头被截断", ErrFrameCorrupt)
		}
		return nil
	case 15:
		return fmt.Errorf("%w：采样率码非法", ErrFrameCorrupt)
	default:
		return nil
	}
}

// skipSubframe 走掉一个子帧的全部位（只数位，不还原采样）。
func skipSubframe(br *bitReader, blockSize, bitsPerSample int) error {
	if err := br.wantZeroBit("子帧起始保留位"); err != nil {
		return fmt.Errorf("%w：子帧被截断或保留位非 0", ErrFrameCorrupt)
	}
	typeBits, err := br.readBits(6)
	if err != nil {
		return fmt.Errorf("%w：子帧被截断", ErrFrameCorrupt)
	}
	wastedFlag, err := br.readBits(1)
	if err != nil {
		return fmt.Errorf("%w：子帧被截断", ErrFrameCorrupt)
	}
	wasted := 0
	if wastedFlag == 1 {
		// 一元编码，与 Rice 商同一个约定：n 个 0 再一个 1，值 = n。
		// 字段里存的是「浪费位数 - 1」，所以 wasted = n+1。
		n := 0
		for {
			v, err := br.readBits(1)
			if err != nil {
				return fmt.Errorf("%w：子帧被截断", ErrFrameCorrupt)
			}
			if v == 1 {
				break
			}
			n++
			if n > 32 {
				return fmt.Errorf("%w：浪费位数异常", ErrFrameCorrupt)
			}
		}
		wasted = n + 1
	}
	width := bitsPerSample - wasted
	if width <= 0 {
		return fmt.Errorf("%w：子帧位宽非法（%d）", ErrFrameCorrupt, width)
	}

	switch {
	case typeBits == 0: // CONSTANT：一个值
		return readExactly(br, width)
	case typeBits == 1: // VERBATIM：每个采样原样存放
		return readExactly(br, width*blockSize)
	case typeBits >= 8 && typeBits <= 12: // FIXED，order = typeBits-8
		// FIXED 子帧也要先读 order 个「预热采样」，然后才是残差。
		// 漏掉这一步会让整帧从残差开始就错位（以前的实现就漏了）。
		order := int(typeBits - 8)
		if err := readExactly(br, width*order); err != nil {
			return err
		}
		return skipResidual(br, blockSize, order)
	case typeBits >= 32: // LPC，order = typeBits-31
		order := int(typeBits - 31)
		if err := readExactly(br, width*order); err != nil {
			return err
		}
		// QLP 系数精度：**4 位**，存的是「精度 - 1」，0b1111 非法。
		//
		// 这里原来读成 5 位 + 后面 3 位移位 —— 与规范（4 位精度 + 5 位移位）
		// 错开 1 位，导致所有 LPC 子帧的比特流从残差开始就错位，合法文件被
		// 判成「残差编码方式非法」或「CRC-16 不符」。绝大多数真实无损文件都用
		// LPC 编码，所以这个 bug 的实际后果是**无损文件几乎全部被拒**、下载侧
		// 一路降档成 mp3。fixtures: testdata/lpc16.flac、testdata/lpc24.flac。
		precMinus1, err := br.readBits(4)
		if err != nil {
			return fmt.Errorf("%w：LPC 系数被截断", ErrFrameCorrupt)
		}
		if precMinus1 == 15 {
			return fmt.Errorf("%w：LPC 精度非法", ErrFrameCorrupt)
		}
		precision := int(precMinus1) + 1
		// 量化移位：有符号 5 位（只影响数值，不影响长度，跳过即可）
		if _, err := br.readBits(5); err != nil {
			return fmt.Errorf("%w：LPC 系数被截断", ErrFrameCorrupt)
		}
		if err := readExactly(br, precision*order); err != nil {
			return err
		}
		return skipResidual(br, blockSize, order)
	default:
		return fmt.Errorf("%w：子帧类型非法（%d）", ErrFrameCorrupt, typeBits)
	}
}

// skipResidual 走掉分区 Rice 编码的残差。
func skipResidual(br *bitReader, blockSize, predictorOrder int) error {
	method, err := br.readBits(2)
	if err != nil {
		return fmt.Errorf("%w：残差被截断", ErrFrameCorrupt)
	}
	var paramBits int
	switch method {
	case 0:
		paramBits = 4
	case 1:
		paramBits = 5
	default:
		return fmt.Errorf("%w：残差编码方式非法（%d）", ErrFrameCorrupt, method)
	}
	partitionOrder, err := br.readBits(4)
	if err != nil {
		return fmt.Errorf("%w：残差被截断", ErrFrameCorrupt)
	}
	partitions := 1 << partitionOrder
	if partitions == 0 || blockSize%partitions != 0 {
		return fmt.Errorf("%w：分区数与块长不符", ErrFrameCorrupt)
	}
	perPartition := blockSize / partitions
	for i := 0; i < partitions; i++ {
		n := perPartition
		if i == 0 {
			n -= predictorOrder
		}
		if n < 0 {
			return fmt.Errorf("%w：残差采样数为负", ErrFrameCorrupt)
		}
		param, err := br.readBits(paramBits)
		if err != nil {
			return fmt.Errorf("%w：残差被截断", ErrFrameCorrupt)
		}
		if param == uint64(1<<paramBits)-1 {
			// 逃逸：后面 5 位是「每个残差占几位」，0 表示全 0（不占位）
			raw, err := br.readBits(5)
			if err != nil {
				return fmt.Errorf("%w：残差被截断", ErrFrameCorrupt)
			}
			if raw == 0 {
				continue
			}
			if err := readExactly(br, n*int(raw)); err != nil {
				return err
			}
			continue
		}
		for j := 0; j < n; j++ {
			// 一元商 + param 位余数
			for {
				v, err := br.readBits(1)
				if err != nil {
					return fmt.Errorf("%w：残差被截断", ErrFrameCorrupt)
				}
				if v == 1 {
					break
				}
			}
			if err := readExactly(br, int(param)); err != nil {
				return err
			}
		}
	}
	return nil
}

func readExactly(br *bitReader, bits int) error {
	if bits < 0 {
		return fmt.Errorf("%w：位数为负", ErrFrameCorrupt)
	}
	for bits > 0 {
		n := bits
		if n > 32 {
			n = 32
		}
		if _, err := br.readBits(n); err != nil {
			return fmt.Errorf("%w：数据被截断", ErrFrameCorrupt)
		}
		bits -= n
	}
	return nil
}
