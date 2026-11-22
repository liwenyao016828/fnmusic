package intercept

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// 下面几个造文件的 helper 与 pkg/audioprobe 测试同构：这里测的是「判据」，
// 即探测结果出来后 classifyRepairFile 会不会给出正确的 kind，不是探测本身。

func repairFlac(rate int, samples uint64) []byte {
	buf := make([]byte, 4+4+34+64)
	copy(buf[0:4], "fLaC")
	buf[4] = 0x80
	buf[5], buf[6], buf[7] = 0, 0, 34
	body := buf[8 : 8+34]
	packed := uint64(rate)<<44 | uint64(1)<<41 | (samples & 0xfffffffff)
	binary.BigEndian.PutUint64(body[10:18], packed)
	return buf
}

func repairMP3(seconds int) []byte {
	// MPEG1 Layer III 128kbps 44.1kHz 立体声，CBR 无 Xing → 按体积折算时长。
	// 一帧 417 字节 ≈ 26ms，要凑够指定秒数。
	kbps := 128
	frameBytes := 144000 / 1152 // ≈125
	total := seconds * (44100 / 1152)
	buf := make([]byte, 0, total*(frameBytes+4))
	frame := []byte{0xFF, 0xFB, byte(9 << 4), 0x00} // 码率索引 9 = 128kbps
	for n := 0; n < total; n++ {
		buf = append(buf, frame...)
		buf = append(buf, make([]byte, frameBytes)...)
	}
	_ = kbps
	return buf
}

func writeRepairFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClassifyExtMismatch(t *testing.T) {
	dir := t.TempDir()
	// 扩展名 .flac，内容其实是 mp3
	p := writeRepairFile(t, dir, "假无损.flac", repairMP3(200))
	issue := classifyRepairFile(p)
	if issue == nil {
		t.Fatal("该报 ext_mismatch，却没报")
	}
	if issue.Kind != "ext_mismatch" {
		t.Errorf("kind 该是 ext_mismatch，得到 %q（detail=%s）", issue.Kind, issue.Detail)
	}
	if issue.Container != "mp3" {
		t.Errorf("container 该是 mp3，得到 %q", issue.Container)
	}
}

func TestClassifyTooShort(t *testing.T) {
	dir := t.TempDir()
	// 10 秒的 mp3 —— 时长过短
	p := writeRepairFile(t, dir, "试听片段.mp3", repairMP3(10))
	issue := classifyRepairFile(p)
	if issue == nil {
		t.Fatal("该报 too_short，却没报")
	}
	if issue.Kind != "too_short" {
		t.Errorf("kind 该是 too_short，得到 %q（detail=%s）", issue.Kind, issue.Detail)
	}
}

func TestClassifyFakeLossless(t *testing.T) {
	dir := t.TempDir()
	// 声明 100 秒的 flac 头，但文件只有一百多字节 → 等效码率极低
	p := writeRepairFile(t, dir, "假无损2.flac", repairFlac(44100, 44100*100))
	issue := classifyRepairFile(p)
	if issue == nil {
		t.Fatal("该报 fake_lossless，却没报")
	}
	if issue.Kind != "fake_lossless" {
		t.Errorf("kind 该是 fake_lossless，得到 %q（detail=%s）", issue.Kind, issue.Detail)
	}
}

func TestClassifyHealthyFilePasses(t *testing.T) {
	dir := t.TempDir()
	// 一个扩展名对得上、时长足够、体积也正常的 mp3 → 不该报
	p := writeRepairFile(t, dir, "正常歌曲.mp3", repairMP3(200))
	if issue := classifyRepairFile(p); issue != nil {
		t.Errorf("正常文件不该报体检阳性：%+v", issue)
	}
}

func TestClassifyUnknownContainerSkipped(t *testing.T) {
	dir := t.TempDir()
	p := writeRepairFile(t, dir, "未知.flac", []byte("这不是音频，只是一段很普通的长文本，用来探测魔数识别会不会误报。"))
	if issue := classifyRepairFile(p); issue != nil {
		t.Errorf("认不出的容器不该报：%+v", issue)
	}
}
