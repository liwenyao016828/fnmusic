// Package audioprobe 直接读盘上音频文件的**身体**，回答两个问题：
// 这个文件到底是什么容器、它有多长。
//
// # 为什么需要它
//
// 「替换音质」回答的是「能不能换成更高的档位」，而用户这次要的是另一个问题：
// **这份文件本身是不是坏的** —— 扩展名写着 flac 但里面其实是 mp3、时长只有 30 秒
// 像试听片段、无损文件小得离谱（截断或假无损）。判断这三件事都必须读文件本身，
// 不能信标签：标签是别人写进去的，正是被怀疑的对象。
//
// # 只读必要的那几段
//
// 曲库动辄上万首，一次体检不能把每个文件从头读到尾。这里的口径是：
//   - 容器靠开头几十字节的魔数判断（错不了，也不需要解码）；
//   - 时长靠容器头里**已经写着**的总采样数/总帧数算（FLAC 的 STREAMINFO、
//     MP3 的 Xing/Info 头、WAV 的 data 块长度），都不需要走帧；
//   - 拿不到就如实返回 0，由调用方决定「这一项不判」而不是猜一个。
//
// 这与 `pkg/audioext` 的分工：那边回答「该算哪一档音质」（决定要不要替换），
// 这边回答「这份文件是不是它自称的样子」（决定要不要修）。
package audioprobe

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

// 容器名。刻意与文件扩展名分开：容器是**内容事实**，扩展名是**别人起的名字**。
const (
	ContainerFLAC = "flac"
	ContainerMP3  = "mp3"
	ContainerWAV  = "wav"
	ContainerAPE  = "ape"
	ContainerOGG  = "ogg"
	ContainerM4A  = "m4a"
	ContainerDSF  = "dsf"
	ContainerDFF  = "dff"
)

// headBytes 是探测容器魔数要读的开头长度。
//
// 64KiB 而不是 4 字节：mp3 开头常有几十 KB 的 ID3（封面 + 歌词），
// 真机见过 46KB 的，只读 4 字节会把「跳 ID3 找帧同步」这一步直接废掉。
const headBytes = 64 << 10

// Info 是一次探测的结论。字段为 0 一律表示「没测出来」，不是「等于零」。
type Info struct {
	// Container 内容真实的容器（用自己的魔数判断）。
	Container string `json:"container,omitempty"`
	// DurationSec 时长（秒）。0 = 没测出来（容器里没写、或这种容器不解析）。
	DurationSec float64 `json:"duration_sec,omitempty"`
	SampleRate  int     `json:"sample_rate,omitempty"`
	Channels    int     `json:"channels,omitempty"`
	// NominalKbps 标称码率，只来自**文件头里写着**的值（目前只有 mp3 帧头）。
	NominalKbps int `json:"nominal_kbps,omitempty"`
	// EffectiveKbps 等效码率 = 体积 ÷ 时长。
	//
	// 这是判断「无损文件小得离谱（截断 / 假无损）」的唯一依据：FLAC 不写码率，
	// 只能自己折算。用 float64 而不是 int —— 一个 100 秒却只有 200 字节的文件，
	// 折算出来是 0.016kbps，取整就变成 0，等于把这个信号抹掉。
	EffectiveKbps float64 `json:"effective_kbps,omitempty"`
	// Size 文件字节数（调用方算等效码率时要用）。
	Size int64 `json:"size,omitempty"`
}

// ErrTooShort 文件连魔数都不够，判不出容器。
var ErrTooShort = errors.New("文件太小，判不出容器")

// Probe 探测一个音频文件。返回的 error 只在读文件本身失败时出现；
// 「读到了但判不出容器」是 Container="" + nil error（这是正常结论，不是故障）。
func Probe(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	info := Info{Size: fi.Size()}
	if fi.Size() <= 0 {
		return info, nil
	}

	head := make([]byte, headBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return info, err
	}
	head = head[:n]
	if len(head) < 12 {
		return info, ErrTooShort
	}

	info.Container = sniffContainer(head)
	switch info.Container {
	case ContainerFLAC:
		if rate, ch, samples, ok := flacStreamInfo(head); ok {
			info.SampleRate, info.Channels = rate, ch
			if rate > 0 && samples > 0 {
				info.DurationSec = float64(samples) / float64(rate)
			}
		}
	case ContainerMP3:
		dur, kbps, rate, ch := mp3Info(head, fi.Size())
		info.DurationSec, info.NominalKbps, info.SampleRate, info.Channels = dur, kbps, rate, ch
	case ContainerWAV:
		if dur, rate, ch, ok := wavInfo(head); ok {
			info.DurationSec, info.SampleRate, info.Channels = dur, rate, ch
		}
	case ContainerDSF:
		if dur, rate, ch, ok := dsfInfo(head); ok {
			info.DurationSec, info.SampleRate, info.Channels = dur, rate, ch
		}
	}
	// 无损容器的「标称码率」不是标签里写的，而是体积除以时长 —— 这正是
	// 判断「假无损/截断」的依据，所以在这里一次性算好，别让调用方各算一遍。
	if info.DurationSec > 0.5 && info.Size > 0 {
		info.EffectiveKbps = float64(info.Size) * 8 / info.DurationSec / 1000
	}
	return info, nil
}

// sniffContainer 靠魔数认容器。认不出返回空串。
func sniffContainer(head []byte) string {
	switch {
	case len(head) >= 4 && string(head[:4]) == "fLaC":
		return ContainerFLAC
	case len(head) >= 4 && string(head[:4]) == "RIFF" && len(head) >= 12 && string(head[8:12]) == "WAVE":
		return ContainerWAV
	case len(head) >= 4 && string(head[:4]) == "MAC ":
		return ContainerAPE
	case len(head) >= 4 && string(head[:4]) == "OggS":
		return ContainerOGG
	case len(head) >= 4 && string(head[:4]) == "DSD ":
		return ContainerDSF
	case len(head) >= 4 && string(head[:4]) == "FRM8":
		return ContainerDFF
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return ContainerM4A
	}
	// mp3 没有固定文件头：要么以 ID3 标签开头，要么直接是帧同步字。
	// 交给 mp3Info 判断（它会跳过 ID3 找同步字），这里先做个粗筛。
	if len(head) >= 3 && string(head[:3]) == "ID3" {
		return ContainerMP3
	}
	if _, ok := findMP3Frame(head, 0); ok {
		return ContainerMP3
	}
	return ""
}

// ExtContainer 把扩展名映射成「它声称的容器」。
// 认不出的扩展名返回空串（这种文件不参与容器核对）。
func ExtContainer(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".flac":
		return ContainerFLAC
	case ".mp3":
		return ContainerMP3
	case ".wav", ".wave":
		return ContainerWAV
	case ".ape":
		return ContainerAPE
	case ".ogg", ".oga":
		return ContainerOGG
	case ".m4a", ".mp4", ".aac":
		return ContainerM4A
	case ".dsf":
		return ContainerDSF
	case ".dff":
		return ContainerDFF
	}
	return ""
}

// flacStreamInfo 从第一个元数据块（STREAMINFO，固定 34 字节）里取采样率/声道/总采样数。
//
// 布局（RFC 9639 §8.2）：4 字节 magic 之后是 4 字节块头（1 位「最后一块」+ 7 位类型 +
// 24 位长度），再 34 字节内容：min/max blocksize 各 16 位、min/max framesize 各 24 位，
// 然后是 20 位采样率 + 3 位（声道-1）+ 5 位（位深-1）+ 36 位总采样数。
func flacStreamInfo(head []byte) (rate, channels int, samples uint64, ok bool) {
	if len(head) < 4+4+34 || string(head[:4]) != "fLaC" {
		return 0, 0, 0, false
	}
	if typ := head[4] & 0x7f; typ != 0 {
		return 0, 0, 0, false // 第一个块必须是 STREAMINFO，否则不猜
	}
	body := head[8 : 8+34]
	// 第 10 字节起是采样率/声道/位深/总采样数这 64 位（8 字节）。
	packed := binary.BigEndian.Uint64(body[10:18])
	rate = int(packed >> 44)
	channels = int((packed>>41)&0x7) + 1
	samples = packed & 0xfffffffff // 36 位
	return rate, channels, samples, rate > 0
}

// mp3Info 解 mp3 的时长与标称码率。
//
// 优先用 Xing/Info 头里写着的总帧数（VBR 文件唯一可靠的口径）；没有这个头就按
// CBR 折算（体积 ÷ 码率），对 CBR 文件是准确的，对没有 Xing 头的 VBR 文件会偏。
func mp3Info(head []byte, size int64) (duration float64, kbps, rate, channels int) {
	start, ok := findMP3Frame(head, 0)
	if !ok {
		return 0, 0, 0, 0
	}
	h := head[start:]
	if len(h) < 4 {
		return 0, 0, 0, 0
	}
	frame, ok := parseMP3Header(h)
	if !ok {
		return 0, 0, 0, 0
	}
	kbps, rate, channels = frame.kbps, frame.rate, frame.channels

	// Xing/Info 头紧跟在第一帧头之后（CRC 存在时再 +2）。
	off := start + 4
	if frame.hasCRC {
		off += 2
	}
	off += frame.sideInfoLen()
	if frames, ok := xingFrames(head, off); ok && frames > 0 {
		return float64(frames) * float64(frame.samplesPerFrame) / float64(rate), kbps, rate, channels
	}
	// 没有 Xing 头 → 按 CBR 折算。减掉 ID3v2 的体积再算，否则带大封面的
	// 128k 文件会被算成「时长偏长」。
	if kbps > 0 && size > int64(start) {
		audioBytes := size - int64(start)
		return float64(audioBytes) * 8 / (float64(kbps) * 1000), kbps, rate, channels
	}
	return 0, kbps, rate, channels
}

// xingFrames 读 Xing/Info（或 VBRI）头里的总帧数。
//
// 字段位置按「侧信息之后」算：VBR 用的 Xing 在 MPEG1 是 32 字节侧信息 / MPEG2 是 17 字节，
// 而 `sideInfoLen` 已经按版本给好了。找不到标记就返回 ok=false，不猜。
func xingFrames(head []byte, off int) (int, bool) {
	if off < 0 || off+8 > len(head) {
		return 0, false
	}
	tag := string(head[off : off+4])
	if tag != "Xing" && tag != "Info" {
		return 0, false
	}
	flags := binary.BigEndian.Uint32(head[off+4 : off+8])
	if flags&0x1 == 0 { // bit0 = 有帧数字段
		return 0, false
	}
	if off+12 > len(head) {
		return 0, false
	}
	return int(binary.BigEndian.Uint32(head[off+8 : off+12])), true
}

// mp3FrameHeader 一帧头的关键字段。
type mp3FrameHeader struct {
	kbps            int
	rate            int
	channels        int
	version         int // 1 或 2（MPEG1 / MPEG2+2.5）
	mono            bool
	samplesPerFrame int
	hasCRC          bool
}

// sideInfoLen 该帧在 Xing 头之前还有多少字节侧信息。
func (h mp3FrameHeader) sideInfoLen() int {
	if h.version == 1 {
		if h.mono {
			return 17
		}
		return 32
	}
	if h.mono {
		return 9
	}
	return 17
}

// 码率表（kbps）：行 = MPEG1 / MPEG2&2.5，列 = layer I / II / III。
var mp3Bitrates = [2][3][15]int{
	{ // MPEG1
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	},
	{ // MPEG2 / 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	},
}

var mp3SampleRates = [3][3]int{
	{44100, 48000, 32000}, // MPEG1
	{22050, 24000, 16000}, // MPEG2
	{11025, 12000, 8000},  // MPEG2.5
}

// findMP3Frame 在 head 里从 from 起找第一个合法的帧同步字。
//
// 只认「头字节合法 + 码率/采样率查表不为 0 + layer != 保留值」三者同时成立的位置，
// 否则一串随机字节里 0xFF 太常见，会误判成 mp3。
func findMP3Frame(head []byte, from int) (int, bool) {
	for i := from; i+4 <= len(head); i++ {
		if head[i] != 0xFF || head[i+1]&0xE0 != 0xE0 {
			continue
		}
		if _, ok := parseMP3Header(head[i:]); ok {
			return i, true
		}
	}
	return 0, false
}

// parseMP3Header 解 4 字节帧头。
func parseMP3Header(b []byte) (mp3FrameHeader, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3FrameHeader{}, false
	}
	verBits := (b[1] >> 3) & 0x3
	layerBits := (b[1] >> 1) & 0x3
	if verBits == 1 || layerBits == 0 { // 保留值
		return mp3FrameHeader{}, false
	}
	var version, versionRow int
	switch verBits {
	case 3:
		version, versionRow = 1, 0
	case 2:
		version, versionRow = 2, 1
	default:
		version, versionRow = 2, 2
	}
	layer := 4 - int(layerBits) // bits 3→Layer I, 2→II, 1→III
	brIdx := int((b[2] >> 4) & 0xF)
	srIdx := int((b[2] >> 2) & 0x3)
	if brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return mp3FrameHeader{}, false
	}
	if layer < 1 || layer > 3 {
		return mp3FrameHeader{}, false
	}
	kbps := mp3Bitrates[versionRow][layer-1][brIdx]
	if kbps == 0 {
		return mp3FrameHeader{}, false
	}
	rate := mp3SampleRates[versionRow][srIdx]
	if rate == 0 {
		return mp3FrameHeader{}, false
	}
	chMode := (b[3] >> 6) & 0x3
	mono := chMode == 3
	spf := 1152
	if layer == 1 {
		spf = 384
	} else if layer == 3 && version != 1 {
		spf = 576
	}
	return mp3FrameHeader{
		kbps: kbps, rate: rate, version: version,
		channels: func() int {
			if mono {
				return 1
			}
			return 2
		}(),
		mono: mono, samplesPerFrame: spf, hasCRC: b[1]&0x1 == 0,
	}, true
}

// wavInfo 从 RIFF 头里读 fmt + data 块算时长。
func wavInfo(head []byte) (duration float64, rate, channels int, ok bool) {
	if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, 0, 0, false
	}
	byteRate := 0
	var dataSize uint32
	pos := 12
	for pos+8 <= len(head) {
		id := string(head[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(head[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+16 <= len(head) {
				channels = int(binary.LittleEndian.Uint16(head[body+2 : body+4]))
				rate = int(binary.LittleEndian.Uint32(head[body+4 : body+8]))
				byteRate = int(binary.LittleEndian.Uint32(head[body+8 : body+12]))
			}
		case "data":
			dataSize = binary.LittleEndian.Uint32(head[pos+4 : pos+8])
		}
		if size < 0 || body+size <= pos { // 块长度异常：别死循环
			break
		}
		pos = body + size + (size & 1)
		if id == "data" && byteRate > 0 {
			break
		}
	}
	if byteRate <= 0 || dataSize == 0 {
		return 0, rate, channels, false
	}
	return float64(dataSize) / float64(byteRate), rate, channels, true
}

// dsfInfo 读 DSF 头的采样率/声道/总采样数。
//
// 布局：DSD (4) + 块大小 (8) + 文件大小 (8) + 元数据指针 (8) + fmt 块：
// "fmt "(4) + 块大小(8) + 版本(4) + 格式 id(4) + 声道类型(4) + 声道数(4) +
// 采样率(4) + 位深(4) + 采样数(8)。
func dsfInfo(head []byte) (duration float64, rate, channels int, ok bool) {
	if len(head) < 80 || string(head[:4]) != "DSD " {
		return 0, 0, 0, false
	}
	if string(head[28:32]) != "fmt " {
		return 0, 0, 0, false
	}
	channels = int(binary.LittleEndian.Uint32(head[44:48]))
	rate = int(binary.LittleEndian.Uint32(head[48:52]))
	samples := binary.LittleEndian.Uint64(head[56:64])
	if rate <= 0 || samples == 0 {
		return 0, rate, channels, false
	}
	return float64(samples) / float64(rate), rate, channels, true
}
