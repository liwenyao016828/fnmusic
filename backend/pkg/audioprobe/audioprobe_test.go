package audioprobe

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// craftFile 把构造好的字节写成真文件 —— Probe 只接受路径（它要 Stat 拿体积）。
func craftFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}
	return p
}

// flacBytes 造一个只有 STREAMINFO 的最小 FLAC 头。
//
// 只需要魔数 + 块头 + 34 字节 STREAMINFO：Probe 不解帧，验的是「会不会从容器头里
// 把采样率和总采样数解出来」。
func flacBytes(rate int, channels int, totalSamples uint64) []byte {
	buf := make([]byte, 4+4+34+64) // 尾巴留点字节，模拟真实文件不是刚好只有头
	copy(buf[0:4], "fLaC")
	buf[4] = 0x80 // 最后一块 + 类型 0（STREAMINFO）
	buf[5], buf[6], buf[7] = 0, 0, 34
	body := buf[8 : 8+34]
	// min/max blocksize + min/max framesize 全 0 即可，Probe 不读这些
	packed := uint64(rate)<<44 | uint64(channels-1)<<41 | (totalSamples & 0xfffffffff)
	binary.BigEndian.PutUint64(body[10:18], packed)
	return buf
}

// mp3Bytes 造「ID3 标签 + 若干 CBR 帧头 + 填充」的假 mp3。
//
// 没有 Xing 头 → 走 CBR 折算路径；帧头相同重复即可，Probe 只解第一帧。
func mp3Bytes(id3Size int, kbpsIdx int, total int) []byte {
	buf := make([]byte, 0, id3Size+total+16)
	if id3Size > 0 {
		hdr := make([]byte, 10)
		copy(hdr[0:3], "ID3")
		hdr[3], hdr[4] = 4, 0
		// synchsafe 长度
		n := id3Size - 10
		hdr[6] = byte((n >> 21) & 0x7f)
		hdr[7] = byte((n >> 14) & 0x7f)
		hdr[8] = byte((n >> 7) & 0x7f)
		hdr[9] = byte(n & 0x7f)
		buf = append(buf, hdr...)
		pad := make([]byte, n)
		buf = append(buf, pad...)
	}
	// MPEG1 Layer III：0xFF 0xFB（无 CRC） + 码率索引 + 采样率索引(44100) + 声道
	frame := []byte{0xFF, 0xFB, 0x00, 0x00}
	frame[2] = byte(kbpsIdx << 4)
	frame[3] = 0x00 // 立体声
	for i := 0; i < total; i++ {
		f := make([]byte, 4)
		copy(f, frame)
		buf = append(buf, f...)
		buf = append(buf, make([]byte, 16)...) // 帧体（内容无所谓）
	}
	return buf
}

// wavBytes 造一个 fmt + data 的 WAV。
func wavBytes(rate, channels, seconds int) []byte {
	byteRate := rate * channels * 2
	dataSize := byteRate * seconds
	buf := make([]byte, 44+32)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf)-8))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*2))
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))
	return buf
}

func TestProbeFLACDuration(t *testing.T) {
	dir := t.TempDir()
	// 44100Hz 立体声，总采样数正好 100 秒
	p := craftFile(t, dir, "a.flac", flacBytes(44100, 2, 44100*100))
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 失败：%v", err)
	}
	if info.Container != ContainerFLAC {
		t.Errorf("容器该是 flac，得到 %q", info.Container)
	}
	if info.SampleRate != 44100 || info.Channels != 2 {
		t.Errorf("采样率/声道该是 44100/2，得到 %d/%d", info.SampleRate, info.Channels)
	}
	if d := info.DurationSec; d < 99.9 || d > 100.1 {
		t.Errorf("时长该是 100 秒，得到 %.2f", d)
	}
}

func TestProbeMP3CBRDurationWithBigID3(t *testing.T) {
	dir := t.TempDir()
	// 46KB 的 ID3（真机上见过这么大的封面），128kbps（MPEG1 Layer III 码率表索引 9）
	body := mp3Bytes(46<<10, 9, 200)
	p := craftFile(t, dir, "a.mp3", body)
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 失败：%v", err)
	}
	if info.Container != ContainerMP3 {
		t.Fatalf("容器该是 mp3（要能跳过 ID3），得到 %q", info.Container)
	}
	if info.NominalKbps != 128 {
		t.Errorf("标称码率该是 128，得到 %d", info.NominalKbps)
	}
	if info.DurationSec <= 0 {
		t.Errorf("时长该能算出来（CBR 折算），得到 0")
	}
}

func TestProbeWAVDuration(t *testing.T) {
	dir := t.TempDir()
	p := craftFile(t, dir, "a.wav", wavBytes(44100, 2, 30))
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 失败：%v", err)
	}
	if info.Container != ContainerWAV {
		t.Fatalf("容器该是 wav，得到 %q", info.Container)
	}
	if d := info.DurationSec; d < 29.9 || d > 30.1 {
		t.Errorf("时长该是 30 秒，得到 %.2f", d)
	}
}

// 「扩展名写 flac、内容其实是 mp3」是这轮要抓的主要目标，魔数必须认出来。
func TestProbeDetectsContainerMismatch(t *testing.T) {
	dir := t.TempDir()
	p := craftFile(t, dir, "fake.flac", mp3Bytes(0, 9, 100))
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 失败：%v", err)
	}
	if info.Container != ContainerMP3 {
		t.Fatalf("内容该被认成 mp3，得到 %q", info.Container)
	}
	if got := ExtContainer(".flac"); got != ContainerFLAC {
		t.Fatalf("扩展名映射该给 flac，得到 %q", got)
	}
	if info.Container == ExtContainer(".flac") {
		t.Errorf("内容与扩展名不该一致 —— 这正是要报出来的问题")
	}
}

func TestProbeUnknownContainer(t *testing.T) {
	dir := t.TempDir()
	p := craftFile(t, dir, "a.flac", append([]byte("这不是音频文件，只是一段文本内容"), make([]byte, 64)...))
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 不该报错（判不出是正常结论）：%v", err)
	}
	if info.Container != "" {
		t.Errorf("容器该是空串（没测出来），得到 %q", info.Container)
	}
	if info.DurationSec != 0 {
		t.Errorf("时长该是 0，得到 %.2f", info.DurationSec)
	}
}

func TestProbeZeroByteAndMissing(t *testing.T) {
	dir := t.TempDir()
	p := craftFile(t, dir, "empty.mp3", nil)
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("0 字节文件不该报错：%v", err)
	}
	if info.Size != 0 || info.Container != "" {
		t.Errorf("0 字节该给 Size=0、Container 空，得到 %d/%q", info.Size, info.Container)
	}
	if _, err := Probe(filepath.Join(dir, "不存在.mp3")); err == nil {
		t.Errorf("文件不存在该报错")
	}
}

func TestExtContainer(t *testing.T) {
	cases := map[string]string{
		".flac": ContainerFLAC, ".FLAC": ContainerFLAC,
		".mp3": ContainerMP3, ".wav": ContainerWAV, ".wave": ContainerWAV,
		".ape": ContainerAPE, ".ogg": ContainerOGG, ".oga": ContainerOGG,
		".m4a": ContainerM4A, ".dsf": ContainerDSF, ".dff": ContainerDFF,
		".txt": "", "": "",
	}
	for ext, want := range cases {
		if got := ExtContainer(ext); got != want {
			t.Errorf("ExtContainer(%q) = %q，该是 %q", ext, got, want)
		}
	}
}

// 等效码率用于判断「无损文件小得离谱」：无损容器算的是体积/时长。
func TestProbeLosslessEffectiveBitrate(t *testing.T) {
	dir := t.TempDir()
	// 100 秒的 FLAC 头，但文件只有 ~150 字节 → 等效码率必然极低
	p := craftFile(t, dir, "suspicious.flac", flacBytes(44100, 2, 44100*100))
	info, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe 失败：%v", err)
	}
	if info.EffectiveKbps <= 0 {
		t.Fatalf("等效码率该被算出来，得到 %v", info.EffectiveKbps)
	}
	if info.EffectiveKbps > 100 {
		t.Errorf("这么小的文件等效码率该很低，得到 %.3f kbps", info.EffectiveKbps)
	}
}
