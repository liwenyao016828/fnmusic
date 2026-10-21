package flacok

import (
	"bufio"
	"encoding/binary"
	"io"
)

// bitReader 按位读取，并**顺手**维护两份 CRC：
//
//   - crc8：只盖帧头（到 CRC-8 字节之前），校验帧头
//   - crc16：盖整帧（从同步码到字节对齐的填充字节），不含 CRC-16 自身
//
// 这样「边走帧边算 CRC」，不需要把整帧读进内存再算一遍 —— 300MB 的无损文件
// 也只用常数内存。
type bitReader struct {
	r *bufio.Reader

	acc  uint64 // 位累加器（高位在前）
	n    int    // acc 里还有几位
	fail error  // 第一次读失败（EOF 等），此后一直返回它

	inFrame  bool // 当前处在一帧之内（crc16 才有意义）
	inHeader bool // 当前处在帧头之内（crc8 才有意义）

	crc8  uint8
	crc16 uint16

	// mode 决定下一个从底层读进来的字节参与哪份 CRC
	mode byte
}

const (
	modeBoth  byte = iota // 帧头字节：crc8 + crc16
	modeFrame             // 帧体字节：只算 crc16（含 CRC-8 字节自身）
	modeNone              // 校验和字节本身：不算任何 CRC
)

func newBitReader(r io.Reader) *bitReader {
	br := &bitReader{mode: modeNone}
	if _, ok := r.(*bufio.Reader); ok {
		br.r = r.(*bufio.Reader)
	} else {
		br.r = bufio.NewReaderSize(r, 64*1024)
	}
	return br
}

// startFrame 开始一帧：复位两份 CRC，后面读进来的字节都算进去。
func (b *bitReader) startFrame() {
	b.inFrame = true
	b.inHeader = true
	b.crc8 = 0
	b.crc16 = 0
	b.mode = modeBoth
}

// nextByte 取一个字节（含 CRC 计入）。EOF 会记进 b.fail。
func (b *bitReader) nextByte() (byte, bool) {
	c, err := b.r.ReadByte()
	if err != nil {
		if b.fail == nil {
			b.fail = err
		}
		return 0, false
	}
	switch b.mode {
	case modeBoth:
		if b.inHeader {
			b.crc8 = crc8Update(b.crc8, c)
		}
		b.crc16 = crc16Update(b.crc16, c)
	case modeFrame:
		b.crc16 = crc16Update(b.crc16, c)
	}
	return c, true
}

// readByte 读一个字节并计入 CRC（帧内字节）。
func (b *bitReader) readByte() (byte, bool) {
	b.mode = modeBoth
	if b.inFrame && !b.inHeader {
		b.mode = modeFrame
	}
	return b.nextByte()
}

// readByteNoCRC 读一个字节但不计入任何 CRC —— 用于读校验和字节本身
// 与帧外的元数据区。
func (b *bitReader) readByteNoCRC() (byte, bool) {
	b.mode = modeNone
	return b.nextByte()
}

// readFrameByte 读一个只算 crc16 的字节（帧头的 CRC-8 字节自身属于这一类）。
func (b *bitReader) readFrameByte() (byte, bool) {
	b.mode = modeFrame
	return b.nextByte()
}

// readBits 读 n 位（n <= 32），高位在前。
func (b *bitReader) readBits(n int) (uint64, error) {
	for b.n < n {
		c, ok := b.readByte()
		if !ok {
			return 0, b.fail
		}
		b.acc = b.acc<<8 | uint64(c)
		b.n += 8
	}
	b.n -= n
	v := b.acc >> b.n
	b.acc &= (1 << b.n) - 1
	return v, nil
}

// wantZeroBit 读 1 位并要求它是 0（规范里的保留位）。
func (b *bitReader) wantZeroBit(what string) error {
	v, err := b.readBits(1)
	if err != nil {
		return err
	}
	if v != 0 {
		return errReservedBit
	}
	_ = what
	return nil
}

// alignByte 丢掉不足一字节的位（帧内补 0 到字节边界）。
func (b *bitReader) alignByte() {
	b.acc = 0
	b.n = 0
}

// readUTF8Number 读 UTF-8 编码的帧号/采样号（1~7 字节），返回其值。
//
// 只用来数位（帧长不依赖它），但仍要读全，否则后面全部错位。
func (b *bitReader) readUTF8Number() (uint64, error) {
	first, ok := b.readByte()
	if !ok {
		return 0, b.fail
	}
	var extra int
	switch {
	case first&0x80 == 0:
		extra = 0
	case first&0xE0 == 0xC0:
		extra = 1
	case first&0xF0 == 0xE0:
		extra = 2
	case first&0xF8 == 0xF0:
		extra = 3
	case first&0xFC == 0xF8:
		extra = 4
	case first&0xFE == 0xFC:
		extra = 5
	case first&0xFF == 0xFE:
		extra = 6
	default:
		return 0, errBadUTF8
	}
	v := uint64(first & (0x7F >> extra))
	for i := 0; i < extra; i++ {
		c, ok := b.readByte()
		if !ok {
			return 0, b.fail
		}
		if c&0xC0 != 0x80 {
			return 0, errBadUTF8
		}
		v = v<<6 | uint64(c&0x3F)
	}
	return v, nil
}

// peekTag 往前看几个字节，判断是不是尾部标签（不消耗）。
// 只在帧边界（位累加器为空）调用。
func (b *bitReader) peekTag() bool {
	if b.n != 0 {
		return false
	}
	p, err := b.r.Peek(12)
	if err != nil && len(p) < 3 {
		return false
	}
	s := string(p)
	for _, magic := range []string{"APETAGEX", "ID3", "LYRICSBEGIN", "TAG+"} {
		if len(s) >= len(magic) && s[:len(magic)] == magic {
			return true
		}
	}
	return false
}

// ── CRC ──
//
// FLAC 用两种 CRC：
//
//	CRC-8：x^8 + x^2 + x + 1（0x07），初值 0，逐字节、高位在前
//	CRC-16：x^16 + x^15 + x^2 + 1（0x8005），初值 0，逐字节、高位在前
//
// 与 libFLAC 的 crc8_table_/crc16_table_ 同一套；测试里用标准校验值
// （"123456789" → CRC-8 = 0xF4、CRC-16 = 0xFEE8）锚定，避免实现自说自话。

var crc8Table [256]byte
var crc16Table [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		c := byte(i)
		for j := 0; j < 8; j++ {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x07
			} else {
				c <<= 1
			}
		}
		crc8Table[i] = c

		w := uint16(i) << 8
		for j := 0; j < 8; j++ {
			if w&0x8000 != 0 {
				w = w<<1 ^ 0x8005
			} else {
				w <<= 1
			}
		}
		crc16Table[i] = w
	}
}

func crc8Update(crc uint8, b byte) uint8 {
	return crc8Table[crc^b]
}

func crc16Update(crc uint16, b byte) uint16 {
	return crc16Table[byte(crc>>8)^b] ^ (crc << 8)
}

// 独立入口：供测试与调用方对已知字节串算 CRC（不经过位读取器）。
func crc8Of(p []byte) byte {
	var c byte
	for _, b := range p {
		c = crc8Update(c, b)
	}
	return c
}

func crc16Of(p []byte) uint16 {
	var c uint16
	for _, b := range p {
		c = crc16Update(c, b)
	}
	return c
}

var _ = binary.BigEndian // 保留：帧头多字节字段都是大端，将来要用
