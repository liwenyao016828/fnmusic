package flacok

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ── CRC 实现的独立锚定 ────────────────────────────────────────────────
//
// 下面的 crc8Bitwise / crc16Bitwise 是**逐位实现**（不用包里的查表），
// 用来交叉验证查表实现。两边对得上，才说明查表没有写错。

func crc8Bitwise(p []byte) byte {
	crc := byte(0)
	for _, b := range p {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func crc16Bitwise(p []byte) uint16 {
	crc := uint16(0)
	for _, b := range p {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x8005
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// TestCRCStandardCheckVectors 用标准校验值把实现钉在标准上：
// CRC-8（poly 0x07, init 0, MSB-first）对 "123456789" 的校验值是 0xF4；
// CRC-16（poly 0x8005, init 0, MSB-first，即 CRC-16/UMTS）是 0xFEE8。
// 这两个数不对，整个校验器的判据就不可信。
func TestCRCStandardCheckVectors(t *testing.T) {
	const check = "123456789"
	if got := crc8Of([]byte(check)); got != 0xF4 {
		t.Fatalf("CRC-8 校验值错：got 0x%02X want 0xF4", got)
	}
	if got := crc16Of([]byte(check)); got != 0xFEE8 {
		t.Fatalf("CRC-16 校验值错：got 0x%04X want 0xFEE8", got)
	}
	if got := crc8Bitwise([]byte(check)); got != 0xF4 {
		t.Fatalf("逐位 CRC-8 校验值错：got 0x%02X want 0xF4", got)
	}
	if got := crc16Bitwise([]byte(check)); got != 0xFEE8 {
		t.Fatalf("逐位 CRC-16 校验值错：got 0x%04X want 0xFEE8", got)
	}
}

// TestCRCTableMatchesBitwise 查表实现与逐位实现必须处处一致。
func TestCRCTableMatchesBitwise(t *testing.T) {
	inputs := [][]byte{
		nil,
		{0x00},
		{0xFF},
		{0x00, 0x01, 0x02, 0x03},
		bytes.Repeat([]byte{0xA5}, 300),
		[]byte("fLaC"),
		[]byte("这是一段用来跑 CRC 的中文与 ASCII 混排文本 1234567890"),
	}
	for i, p := range inputs {
		if got, want := crc8Of(p), crc8Bitwise(p); got != want {
			t.Fatalf("输入 %d：CRC-8 查表 0x%02X != 逐位 0x%02X", i, got, want)
		}
		if got, want := crc16Of(p), crc16Bitwise(p); got != want {
			t.Fatalf("输入 %d：CRC-16 查表 0x%04X != 逐位 0x%04X", i, got, want)
		}
	}
}

// ── 夹具构造 ─────────────────────────────────────────────────────────
//
// 手工合成一个最小 FLAC：fLaC + STREAMINFO（34 字节）+ 若干 CONSTANT 帧。
// 帧头的 CRC-8 与整帧 CRC-16 都用上面的**逐位**实现算，
// 所以合成出来的文件对查表实现来说是真正的外部输入。

type fixtureStreamInfo struct {
	sampleRate int
	channels   int
	bps        int
}

func (s fixtureStreamInfo) bytes() []byte {
	data := make([]byte, 34)
	// min/max blocksize = 16
	data[0], data[1] = 0x00, 0x10
	data[2], data[3] = 0x00, 0x10
	// bytes 10..17：采样率 20 位 | 声道数-1 3 位 | 位宽-1 5 位 | 总采样数 36 位
	v := uint64(s.sampleRate)<<44 | uint64(s.channels-1)<<41 | uint64(s.bps-1)<<36 | 16
	for i := 0; i < 8; i++ {
		data[10+i] = byte(v >> (56 - 8*i))
	}
	return data
}

// fixtureFrame 合成一个 CONSTANT 子帧的完整帧。
// blockSizeCode/sampleRateCode/sampleSizeCode 传 0xFF 表示用默认值
// （6 = 8 位显式块长、9 = 44.1kHz 表项、4 = 16 位）。
func fixtureFrame(s fixtureStreamInfo, blockSizeCode, sampleRateCode, sampleSizeCode byte) []byte {
	if blockSizeCode == 0xFF {
		blockSizeCode = 6
	}
	if sampleRateCode == 0xFF {
		sampleRateCode = 9
	}
	if sampleSizeCode == 0xFF {
		sampleSizeCode = 4
	}
	hdr := []byte{
		0xFF, 0xF8, // 同步码 + 保留位 + 定长块
		blockSizeCode<<4 | sampleRateCode,
		0<<4 | sampleSizeCode<<1, // 声道赋值 0（单声道）+ 位宽码 + 保留位
		0x00,                   // 帧号 0（UTF-8 风格：小于 0x80 的值就是一个字节）
		byte(16 - 1),           // 显式块长 16
	}
	hdr = append(hdr, crc8Bitwise(hdr))

	// 子帧：保留位 0 + 类型 0（CONSTANT）+ 浪费位标志 0 = 0x00，
	// 然后 16 位常量采样 0x0001。
	frame := append(hdr, 0x00, 0x00, 0x01)
	// 已经字节对齐，直接补整帧 CRC-16
	crc := crc16Bitwise(frame)
	return append(frame, byte(crc>>8), byte(crc))
}

func fixtureFile(frames ...[]byte) []byte {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	block := si.bytes()
	out := []byte{'f', 'L', 'a', 'C'}
	out = append(out, 0x80, 0x00, 0x00, byte(len(block)))
	out = append(out, block...)
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

func defaultFrames() [][]byte {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	return [][]byte{fixtureFrame(si, 0xFF, 0xFF, 0xFF)}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.flac")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── 正常文件 ─────────────────────────────────────────────────────────

func TestValidSingleFrame(t *testing.T) {
	if err := ValidateFile(writeTemp(t, fixtureFile(defaultFrames()...))); err != nil {
		t.Fatalf("完好的单帧文件被判坏：%v", err)
	}
}

func TestValidMultiFrame(t *testing.T) {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	f := fixtureFrame(si, 0xFF, 0xFF, 0xFF)
	if err := ValidateFile(writeTemp(t, fixtureFile(f, f, f, f, f))); err != nil {
		t.Fatalf("完好的五帧文件被判坏：%v", err)
	}
}

// TestFrameCodesFallbackToStreamInfo 帧头码值 0 = 「取自 STREAMINFO」，
// 这条分支必须能走通（真文件里两种写法都常见）。
func TestFrameCodesFallbackToStreamInfo(t *testing.T) {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	f := fixtureFrame(si, 0xFF, 0, 0) // 采样率与位宽都从 STREAMINFO 取
	if err := ValidateFile(writeTemp(t, fixtureFile(f))); err != nil {
		t.Fatalf("码值回退 STREAMINFO 的帧被判坏：%v", err)
	}
}

// TestTrailingTagIsCleanEnd 文件尾部挂 APEv2/ID3/Lyrics3 标签是合法的，
// 不能因此把好文件判坏。
func TestTrailingTagIsCleanEnd(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	data = append(data, []byte("APETAGEX\x20\x00\x00\x00 跟在后面的标签不算音频帧")...)
	if err := ValidateFile(writeTemp(t, data)); err != nil {
		t.Fatalf("带尾部标签的好文件被判坏：%v", err)
	}
}

// TestID3PrefixIsSkipped 文件头挂 ID3v2 也允许。
func TestID3PrefixIsSkipped(t *testing.T) {
	// 10 字节头："ID3" + 版本 2 字节 + 标志 1 字节 + 同步安全长度 4 字节
	id3 := []byte{'I', 'D', '3', 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05}
	data := append(id3, []byte("HELLO")...)
	data = append(data, fixtureFile(defaultFrames()...)...)
	if err := ValidateFile(writeTemp(t, data)); err != nil {
		t.Fatalf("带 ID3 前缀的好文件被判坏：%v", err)
	}
}

// ── 坏文件 ───────────────────────────────────────────────────────────

// TestCorruptSubframeDataIsCaught 核心用例：正文一个字节坏了必须被抓住。
// 以前的校验只看魔数，这种文件会被照常入库。
func TestCorruptSubframeDataIsCaught(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	// 帧里常量采样的高字节（倒数第 4 个字节：… 0x00 0x01 CRC16）
	data[len(data)-3] ^= 0x01
	err := ValidateFile(writeTemp(t, data))
	if !errors.Is(err, ErrFrameCorrupt) {
		t.Fatalf("坏帧没被抓住：err = %v", err)
	}
}

func TestCorruptFrameCRCIsCaught(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	// 整帧 CRC-16 的低字节
	data[len(data)-1] ^= 0xFF
	if err := ValidateFile(writeTemp(t, data)); !errors.Is(err, ErrFrameCorrupt) {
		t.Fatalf("CRC 被篡改却没报错：%v", err)
	}
}

func TestCorruptFrameHeaderCRCIsCaught(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	// 帧头 CRC-8 字节（帧从魔数+元数据之后开始；这里改帧里的 CRC-8）
	frameStart := 4 + 4 + 34
	data[frameStart+6] ^= 0x01
	if err := ValidateFile(writeTemp(t, data)); !errors.Is(err, ErrFrameCorrupt) {
		t.Fatalf("帧头 CRC-8 被篡改却没报错：%v", err)
	}
}

func TestTruncatedFile(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	cases := map[string]int{
		"截断在帧中间": len(data) - 3,
		"只剩魔数":   6,
		"元数据没读完": 20,
	}
	for name, n := range cases {
		err := ValidateFile(writeTemp(t, data[:n]))
		if err == nil {
			t.Fatalf("%s：截断文件却通过了校验", name)
		}
	}
}

func TestBadMagic(t *testing.T) {
	if err := ValidateFile(writeTemp(t, []byte("这不是 FLAC 只是同名的普通文件"))); !errors.Is(err, ErrNotFLAC) {
		t.Fatalf("魔数不对却没报 ErrNotFLAC：%v", err)
	}
}

func TestEmptyStream(t *testing.T) {
	if err := Validate(bytes.NewReader(nil)); !errors.Is(err, ErrNotFLAC) {
		t.Fatalf("空流没报 ErrNotFLAC：%v", err)
	}
}

func TestStreamInfoLengthMustBe34(t *testing.T) {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	block := si.bytes()
	data := []byte{'f', 'L', 'a', 'C'}
	data = append(data, 0x80, 0x00, 0x00, byte(len(block)-1)) // 声明 33 字节
	data = append(data, block...)                            // 实际给 34
	data = append(data, fixtureFrame(si, 0xFF, 0xFF, 0xFF)...)
	if err := ValidateFile(writeTemp(t, data)); !errors.Is(err, ErrHeaderCorrupt) {
		t.Fatalf("STREAMINFO 长度不对却没报 ErrHeaderCorrupt：%v", err)
	}
}

func TestNoFramesIsCorrupt(t *testing.T) {
	si := fixtureStreamInfo{sampleRate: 44100, channels: 1, bps: 16}
	block := si.bytes()
	data := []byte{'f', 'L', 'a', 'C'}
	data = append(data, 0x80, 0x00, 0x00, byte(len(block)))
	data = append(data, block...)
	err := ValidateFile(writeTemp(t, data))
	if !errors.Is(err, ErrFrameCorrupt) {
		t.Fatalf("只有元数据没有音频帧却没报 ErrFrameCorrupt：%v", err)
	}
}

// TestCorruptFrameStructureIsCaught 同步码丢了（坏流最典型的样子）。
func TestCorruptFrameStructureIsCaught(t *testing.T) {
	data := fixtureFile(defaultFrames()...)
	frameStart := 4 + 4 + 34
	data[frameStart] = 0x00 // 同步码第一个字节
	if err := ValidateFile(writeTemp(t, data)); !errors.Is(err, ErrFrameCorrupt) {
		t.Fatalf("同步码丢失却没报 ErrFrameCorrupt：%v", err)
	}
}

// TestValidateFileMissingPath 路径打不开要原样返回 os 的错误（不是误判成坏文件）。
func TestValidateFileMissingPath(t *testing.T) {
	if err := ValidateFile(filepath.Join(t.TempDir(), "nope.flac")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件不存在却没返回 os.ErrNotExist：%v", err)
	}
}
