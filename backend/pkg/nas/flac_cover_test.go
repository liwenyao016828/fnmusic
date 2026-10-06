package nas

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ── FLAC 封面提取回归测试 ──
//
// 旧实现把「从 JPEG 魔数起直到文件末尾」的全部字节都当作封面返回，
// 会把 PICTURE 块之后的元数据与音频帧一并吐出（实测单次响应 300KB+）。
// 这里断言返回的字节恰好等于图片本身。

func be32(n int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(n))
	return b
}

func flacBlockHeader(last bool, blockType byte, length int) []byte {
	b0 := blockType & 0x7F
	if last {
		b0 |= 0x80
	}
	return []byte{b0, byte(length >> 16), byte(length >> 8), byte(length)}
}

func buildFLACWithPicture(t *testing.T, pic []byte, audio []byte) string {
	t.Helper()

	var picture bytes.Buffer
	picture.Write(be32(3))                 // picture type: front cover
	picture.Write(be32(len("image/jpeg"))) // MIME 长度
	picture.WriteString("image/jpeg")
	picture.Write(be32(0))   // 描述长度
	picture.Write(be32(400)) // width
	picture.Write(be32(400)) // height
	picture.Write(be32(24))  // depth
	picture.Write(be32(0))   // colors
	picture.Write(be32(len(pic)))
	picture.Write(pic)

	var out bytes.Buffer
	out.WriteString("fLaC")
	// STREAMINFO（非最后一块）
	out.Write(flacBlockHeader(false, 0, 34))
	out.Write(make([]byte, 34))
	// PICTURE（最后一块）
	out.Write(flacBlockHeader(true, 6, picture.Len()))
	out.Write(picture.Bytes())
	// 音频帧数据（不应被当作封面返回）
	out.Write(audio)

	path := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractFLACCoverReturnsOnlyPicture(t *testing.T) {
	pic := fakeCover(900)
	audio := bytes.Repeat([]byte{0xFF, 0xF8, 0x69, 0x18}, 4096) // 16KB 伪音频帧

	path := buildFLACWithPicture(t, pic, audio)

	data, mime := extractCoverBytes(path)
	if len(data) == 0 {
		t.Fatal("FLAC 内嵌封面应当能被提取出来")
	}
	if !bytes.Equal(data, pic) {
		t.Errorf("封面应恰好等于 PICTURE 数据: got %d bytes, want %d bytes", len(data), len(pic))
	}
	if len(data) > len(pic) {
		t.Errorf("封面响应混入了图片之后的数据（多出 %d 字节）", len(data)-len(pic))
	}
	if mime != "image/jpeg" {
		t.Errorf("MIME = %q, 期望 image/jpeg", mime)
	}
}

func TestExtractFLACCoverFallsBackToFolderImage(t *testing.T) {
	// 没有内嵌封面时，应回退到同目录 cover.jpg
	dir := t.TempDir()
	// 同目录候选封面现在也受路径守卫约束，需要把临时目录设为允许根
	t.Setenv("FN_ALLOWED_ROOTS", dir)

	audio := bytes.Repeat([]byte{0xFF, 0xF8}, 512)

	var out bytes.Buffer
	out.WriteString("fLaC")
	out.Write(flacBlockHeader(true, 0, 34)) // STREAMINFO 即最后一块
	out.Write(make([]byte, 34))
	out.Write(audio)

	songPath := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(songPath, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	folderCover := fakeCover(300)
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), folderCover, 0o644); err != nil {
		t.Fatal(err)
	}

	data, mime := extractCoverBytes(songPath)
	if !bytes.Equal(data, folderCover) {
		t.Errorf("应回退到同目录封面: got %d bytes, want %d", len(data), len(folderCover))
	}
	if mime != "image/jpeg" {
		t.Errorf("MIME = %q", mime)
	}
}
