// 音频文件真实时长的**可靠**探测。
//
// 为什么要自己解析（而不是拿文件名/索引里的元数据）：索引条目里根本没有时长
// （pkg/library.Entry 没有该字段，pkg/tags 也不读 TLEN），而查重要用它来分辨
// 「同名但时长差很多」的两个录音（现场版 vs 录音室版、剪辑版 vs 完整版）。
//
// 判据的边界很关键：**解析不出来就返回 0（未知），绝不用近似值冒充结论**。
// 时长在查重里是「否决项」——用估算值去否决，就等于用猜测挡住一次正确的去重。
// 所以：
//   - FLAC：读 STREAMINFO 的 total samples ÷ sample rate，精确；
//   - MP3：只认 Xing/Info（或同位置的 VBR 帧数表）里的帧数，精确到一帧；
//     **没有 Xing/Info 就返回 0**，不做「文件大小 ÷ 码率」这类 CBR 估算
//     （VBR 文件那样算能差出几十秒，正好把该判「已有」的判成「不是」）。
//   - 其它容器（m4a/aac/ogg/ape/dsd/wav）：返回 0，视为未知。
//
// 只读文件开头若干字节：批量查重时这段代码会被调用几百次，不能整文件扫。
package downloader

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// probeDurationSec 返回盘上音频文件的时长（秒）；0 表示**未知**（不是 0 秒）。
func probeDurationSec(path string) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return probeFLACDuration(path)
	case ".mp3":
		return probeMP3Duration(path)
	}
	return 0
}

// flacStreamInfoLen STREAMINFO 块的固定长度（34 字节）。
const flacStreamInfoLen = 34

// probeFLACDuration 从 FLAC 的 STREAMINFO 元数据块读总采样数与采样率。
//
// STREAMINFO 规定必须是第一个元数据块，所以只需要读文件最前面 42 字节：
// "fLaC"(4) + 块头(4) + STREAMINFO(34)。
func probeFLACDuration(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var buf [4 + 4 + flacStreamInfoLen]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return 0
	}
	if string(buf[0:4]) != "fLaC" {
		return 0
	}
	// 块头：1 bit 末块标记 + 7 bit 类型，后跟 24 bit 长度。
	if buf[4]&0x7f != 0 { // 第一个块不是 STREAMINFO：不是我们能认的 FLAC
		return 0
	}
	blockLen := int(buf[5])<<16 | int(buf[6])<<8 | int(buf[7])
	if blockLen != flacStreamInfoLen {
		return 0
	}

	si := buf[8:]
	// STREAMINFO 前 10 字节：min/max block size(2+2) + min/max frame size(3+3)。
	// 之后 8 字节打包：sampleRate(20) channels-1(3) bitsPerSample-1(5) totalSamples(36)。
	p := si[10:18]
	sampleRate := uint64(p[0])<<12 | uint64(p[1])<<4 | uint64(p[2])>>4
	totalSamples := uint64(p[3]&0x0f)<<32 |
		uint64(p[4])<<24 | uint64(p[5])<<16 | uint64(p[6])<<8 | uint64(p[7])
	if sampleRate == 0 || totalSamples == 0 {
		return 0
	}
	return int(math.Round(float64(totalSamples) / float64(sampleRate)))
}

// id3v2 头长度与「未知大小」标记（流式写出的 ID3v2 会用 0xFF 全 1 表示未知）。
const (
	id3HeaderLen  = 10
	id3MaxSkip    = 4        // 最多连着跳几个 ID3v2（多段 ID3 极少见，但要能跳过）
	mp3ScanWindow = 64 << 10 // 找第一帧的扫描窗口
)

// probeMP3Duration 只从 Xing/Info 帧头取总帧数算时长（没有就返回 0 = 未知）。
func probeMP3Duration(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	// 跳过 ID3v2（它常有一两百 KB 的封面，里面全是看起来像帧同步的字节）。
	pos := int64(0)
	for i := 0; i < id3MaxSkip; i++ {
		var h [id3HeaderLen]byte
		if _, err := f.ReadAt(h[:], pos); err != nil {
			return 0
		}
		if string(h[0:3]) != "ID3" {
			break
		}
		size := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
		if size < 0 {
			return 0
		}
		pos += id3HeaderLen + size
		if h[5]&0x10 != 0 { // 有 footer
			pos += id3HeaderLen
		}
	}

	window := make([]byte, mp3ScanWindow)
	n, err := f.ReadAt(window, pos)
	if n < 4 {
		return 0
	}
	_ = err // io.EOF 是正常的：窗口就是文件尾
	window = window[:n]

	// 帧头可能在窗口内任意偏移（前面可能有残留标签/填充字节），
	// 逐个同步点试到第一个能解析出 Xing/Info 的帧为止。
	for off := 0; off+4 <= len(window); off++ {
		if window[off] != 0xFF || window[off+1]&0xE0 != 0xE0 {
			continue
		}
		if dur, ok := parseXingDuration(window[off:]); ok {
			return dur
		}
	}
	return 0
}

// mp3SampleRates 按 MPEG 版本（3=MPEG1, 2=MPEG2, 0=MPEG2.5）与采样率索引取采样率。
var mp3SampleRates = [4][3]int{
	{11025, 12000, 8000},  // MPEG2.5
	{0, 0, 0},             // 保留
	{22050, 24000, 16000}, // MPEG2
	{44100, 48000, 32000}, // MPEG1
}

// parseXingDuration 从一帧的帧头出发，找 Xing/Info 里的总帧数并换算秒数。
//
// 位置规则：帧头(4) + side info（MPEG1 单声道 17 / 双声道 32；MPEG2/2.5 单声道 9 /
// 双声道 17）之后紧跟 "Xing"/"Info"，然后是 flags(4) 与各可选字段，
// 帧数是 flags 最低位为 1 时的第一个字段。
func parseXingDuration(b []byte) (int, bool) {
	if len(b) < 4 {
		return 0, false
	}
	ver := (b[1] >> 3) & 0x03
	if ver == 1 { // 保留值
		return 0, false
	}
	layer := (b[1] >> 1) & 0x03 // 0=保留, 1=Layer III, 2=Layer II, 3=Layer I
	if layer == 0 {
		return 0, false
	}
	bitrateIdx := b[2] >> 4
	if bitrateIdx == 0 || bitrateIdx == 0x0f { // free / 非法
		return 0, false
	}
	srIdx := (b[2] >> 2) & 0x03
	if srIdx == 3 {
		return 0, false
	}
	channelMode := (b[3] >> 6) & 0x03 // 3 = 单声道

	sideInfo := 32
	if ver == 3 { // MPEG1
		if channelMode == 3 {
			sideInfo = 17
		}
	} else { // MPEG2 / MPEG2.5
		sideInfo = 17
		if channelMode == 3 {
			sideInfo = 9
		}
	}

	off := 4 + sideInfo
	if len(b) < off+12 {
		return 0, false
	}
	tag := string(b[off : off+4])
	if tag != "Xing" && tag != "Info" {
		return 0, false
	}
	flags := binary.BigEndian.Uint32(b[off+4 : off+8])
	if flags&0x01 == 0 { // 没写帧数字段
		return 0, false
	}
	frames := binary.BigEndian.Uint32(b[off+8 : off+12])
	if frames == 0 {
		return 0, false
	}
	sampleRate := mp3SampleRates[ver][srIdx]
	if sampleRate == 0 {
		return 0, false
	}

	samplesPerFrame := 1152
	switch {
	case layer == 3: // Layer I
		samplesPerFrame = 384
	case layer == 1 && ver != 3: // Layer III 的 MPEG2/MPEG2.5
		samplesPerFrame = 576
	}
	return int(math.Round(float64(frames) * float64(samplesPerFrame) / float64(sampleRate))), true
}
