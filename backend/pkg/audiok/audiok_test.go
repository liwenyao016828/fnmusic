package audiok

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ── 造文件的辅助 ────────────────────────────────────────────────────────

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	return p
}

// buildWAV 造一个结构自洽的 WAV；fmtSize/dataSize 可以故意写大来造截断。
func buildWAV(riffLenOverride int, fmtSize, dataSize int, includeData bool) []byte {
	buf := []byte("RIFF")
	buf = binary.LittleEndian.AppendUint32(buf, 0)
	buf = append(buf, "WAVE"...)

	fmtChunk := []byte("fmt ")
	fmtChunk = binary.LittleEndian.AppendUint32(fmtChunk, 16)
	fmtChunk = append(fmtChunk, make([]byte, 16)...)
	buf = append(buf, fmtChunk...)

	if includeData {
		dataChunk := []byte("data")
		dataChunk = binary.LittleEndian.AppendUint32(dataChunk, uint32(dataSize))
		dataChunk = append(dataChunk, make([]byte, 16)...)
		buf = append(buf, dataChunk...)
	}

	if riffLenOverride > 0 {
		binary.LittleEndian.PutUint32(buf[4:8], uint32(riffLenOverride))
	} else {
		binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf)-8))
	}
	return buf
}

// buildMP4 造一个结构自洽的 MP4；skipMoov 用于造「缺索引」的坏文件。
func buildMP4(skipMoov bool, mdatTooLong bool) []byte {
	// atom 的布局是「长度(4) + 类型(4)」，顺序不能反
	var buf []byte
	ftyp := make([]byte, 0, 16)
	ftyp = binary.BigEndian.AppendUint32(ftyp, 16)
	ftyp = append(ftyp, "ftyp"...)
	ftyp = append(ftyp, "M4A "...)
	ftyp = append(ftyp, make([]byte, 4)...)
	buf = append(buf, ftyp...)

	if !skipMoov {
		moov := make([]byte, 0, 8)
		moov = binary.BigEndian.AppendUint32(moov, 8)
		moov = append(moov, "moov"...)
		buf = append(buf, moov...)
	}

	mdatLen := 8
	if mdatTooLong {
		mdatLen = 4096 // 声明了但文件里没有这么多
	}
	mdat := make([]byte, 0, 8)
	mdat = binary.BigEndian.AppendUint32(mdat, uint32(mdatLen))
	mdat = append(mdat, "mdat"...)
	buf = append(buf, mdat...)
	return buf
}

// buildWavPack 造一个 wvpk 块；blockLen 可故意写大造截断。
func buildWavPack(blockLen int) []byte {
	buf := []byte("wvpk")
	buf = binary.LittleEndian.AppendUint32(buf, uint32(blockLen))
	buf = append(buf, make([]byte, 16)...)
	return buf
}

// buildAPE 造一个 MAC 头；descriptor/header 长度可故意写大造截断。
func buildAPE(descriptorBytes, headerBytes int, total int) []byte {
	head := make([]byte, 32)
	copy(head[0:4], "MAC ")
	binary.LittleEndian.PutUint16(head[4:6], 3990)
	binary.LittleEndian.PutUint32(head[8:12], uint32(descriptorBytes))
	binary.LittleEndian.PutUint32(head[12:16], uint32(headerBytes))
	out := make([]byte, total)
	copy(out, head)
	return out
}

// ── WAV ─────────────────────────────────────────────────────────────────

func TestValidateWAVAcceptsWellFormed(t *testing.T) {
	p := writeTemp(t, "ok.wav", buildWAV(0, 16, 16, true))
	if err := Validate(p, "wav"); err != nil {
		t.Fatalf("正常的 WAV 不该报错：%v", err)
	}
}

func TestValidateWAVRejectsTruncatedData(t *testing.T) {
	// data 块声明 4096 字节，文件里只有 16 字节 —— 典型的截断流
	p := writeTemp(t, "trunc.wav", buildWAV(0, 16, 4096, true))
	err := Validate(p, "wav")
	if err == nil {
		t.Fatal("data 块越界的 WAV 必须判死")
	}
}

func TestValidateWAVRejectsMissingData(t *testing.T) {
	p := writeTemp(t, "nodata.wav", buildWAV(0, 16, 0, false))
	if err := Validate(p, "wav"); err == nil {
		t.Fatal("没有 data 块的 WAV 必须判死")
	}
}

func TestValidateWAVRejectsNonRIFF(t *testing.T) {
	p := writeTemp(t, "bad.wav", []byte("NOTAWAVEFILE........."))
	if err := Validate(p, "wav"); err == nil {
		t.Fatal("不是 RIFF 的文件必须判死")
	}
}

func TestValidateWAVRejectsLengthMismatch(t *testing.T) {
	p := writeTemp(t, "len.wav", buildWAV(999999, 16, 16, true))
	if err := Validate(p, "wav"); err == nil {
		t.Fatal("RIFF 声明长度与文件大小对不上必须判死")
	}
}

// ── MP4 / M4A ──────────────────────────────────────────────────────────

func TestValidateMP4AcceptsWellFormed(t *testing.T) {
	p := writeTemp(t, "ok.m4a", buildMP4(false, false))
	if err := Validate(p, "m4a"); err != nil {
		t.Fatalf("正常的 M4A 不该报错：%v", err)
	}
}

func TestValidateMP4RejectsTruncatedAtom(t *testing.T) {
	p := writeTemp(t, "trunc.m4a", buildMP4(false, true))
	if err := Validate(p, "m4a"); err == nil {
		t.Fatal("atom 越界的 M4A 必须判死")
	}
}

func TestValidateMP4RejectsMissingMoov(t *testing.T) {
	p := writeTemp(t, "nomoov.m4a", buildMP4(true, false))
	if err := Validate(p, "m4a"); err == nil {
		t.Fatal("缺 moov 的 M4A 必须判死")
	}
}

// ── WavPack ────────────────────────────────────────────────────────────

func TestValidateWavPackAcceptsWellFormed(t *testing.T) {
	p := writeTemp(t, "ok.wv", buildWavPack(24))
	if err := Validate(p, "wv"); err != nil {
		t.Fatalf("正常的 WavPack 不该报错：%v", err)
	}
}

func TestValidateWavPackRejectsTruncatedBlock(t *testing.T) {
	p := writeTemp(t, "trunc.wv", buildWavPack(4096))
	if err := Validate(p, "wv"); err == nil {
		t.Fatal("块长越界的 WavPack 必须判死")
	}
}

// ── APE ────────────────────────────────────────────────────────────────

func TestValidateAPEAceptsWellFormed(t *testing.T) {
	p := writeTemp(t, "ok.ape", buildAPE(52, 24, 200))
	if err := Validate(p, "ape"); err != nil {
		t.Fatalf("正常的 APE 不该报错：%v", err)
	}
}

func TestValidateAPERejectsTruncated(t *testing.T) {
	// 头部声明要 32+52+24=108 字节，文件只有 64 字节
	p := writeTemp(t, "trunc.ape", buildAPE(52, 24, 64))
	if err := Validate(p, "ape"); err == nil {
		t.Fatal("头部放不下的 APE 必须判死")
	}
}

func TestValidateAPERejectsNonMAC(t *testing.T) {
	p := writeTemp(t, "bad.ape", make([]byte, 200))
	if err := Validate(p, "ape"); err == nil {
		t.Fatal("不是 MAC 头的文件必须判死")
	}
}

// ── 分派 ───────────────────────────────────────────────────────────────

func TestValidateIgnoresUnsupportedContainers(t *testing.T) {
	p := writeTemp(t, "x.mp3", []byte("not really an mp3"))
	if err := Validate(p, "mp3"); err != nil {
		t.Fatalf("不校验的容器不该报错：%v", err)
	}
	if err := Validate(p, "flac"); err != nil {
		t.Fatalf("FLAC 交给 flacok 管，audiok 不该管：%v", err)
	}
}
