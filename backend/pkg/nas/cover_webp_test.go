package nas

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// ── APIC 帧体里切图片：必须认出 WebP / GIF（2026-11-01 回归）──
//
// 用户报「本地曲库的封面怎么看不到了」：下载链路里很多封面内嵌成 WebP，
// 旧实现只扫 JPEG/PNG 魔数 → 判定「没有封面」→ /api/nas/cover 返回 404 → 整列 💿。

// webpBytes 造一段最小的 WebP 头（RIFF + size + WEBP），后面跟一段假数据。
func webpBytes() []byte {
	b := []byte("RIFF")
	b = append(b, 0x24, 0x01, 0x00, 0x00) // 文件长度（占位）
	b = append(b, []byte("WEBP")...)
	b = append(b, []byte("VP8 ")...)
	return append(b, bytes.Repeat([]byte{0xAB}, 32)...)
}

func TestSliceImageWebP(t *testing.T) {
	pic := webpBytes()
	// APIC 帧体的真实前缀：mime + \x00 + 描述长度（v2.3）或 \x00 + 图片类型
	frame := append([]byte("image/webp\x00\x03\x00"), pic...)
	got, mime := sliceImage(frame)
	if mime != "image/webp" {
		t.Fatalf("mime = %q, want image/webp", mime)
	}
	if !bytes.Equal(got, pic) {
		t.Fatalf("切出来的字节不等于原图（got %d bytes, want %d）", len(got), len(pic))
	}
}

func TestSliceImageGIFAndJPEG(t *testing.T) {
	gif := append([]byte("GIF89a"), bytes.Repeat([]byte{0x11}, 16)...)
	if got, mime := sliceImage(append([]byte("image/gif\x00\x03\x00"), gif...)); mime != "image/gif" || !bytes.Equal(got, gif) {
		t.Fatalf("GIF: mime=%q ok=%v", mime, bytes.Equal(got, gif))
	}

	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x22}, 16)...)
	if got, mime := sliceImage(append([]byte("image/jpeg\x00\x03\x00"), jpg...)); mime != "image/jpeg" || !bytes.Equal(got, jpg) {
		t.Fatalf("JPEG: mime=%q ok=%v", mime, bytes.Equal(got, jpg))
	}
}

// RIFF 容器里不是 WEBP（例如 wav）时不能当成封面，否则会把音频当图片吐出去。
func TestSliceImageIgnoresNonWebpRIFF(t *testing.T) {
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0}, 8)...)
	if got, mime := sliceImage(wav); got != nil || mime != "" {
		t.Fatalf("不该把 WAV 当封面：mime=%q len=%d", mime, len(got))
	}
}

// 端到端：内嵌 WebP 的 MP3 要能从 extractCoverBytes 里拿出封面。
func TestExtractCoverWebPFromMP3(t *testing.T) {
	pic := webpBytes()
	frameBody := append([]byte("image/webp\x00\x03\x00"), pic...)

	var tag bytes.Buffer
	tag.WriteString("ID3")
	tag.Write([]byte{3, 0, 0}) // v2.3

	var frames bytes.Buffer
	frames.WriteString("APIC")
	frames.Write(be32(len(frameBody)))
	frames.Write([]byte{0, 0}) // flags
	frames.Write(frameBody)

	size := frames.Len()
	tag.Write([]byte{byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F), byte(size >> 7 & 0x7F), byte(size & 0x7F)})
	tag.Write(frames.Bytes())

	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(p, append(tag.Bytes(), bytes.Repeat([]byte{0x55}, 64)...), 0o644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	data, mime := extractCoverBytes(p)
	if mime != "image/webp" {
		t.Fatalf("mime = %q, want image/webp", mime)
	}
	if !bytes.Equal(data, pic) {
		t.Fatalf("封面字节不对（got %d, want %d）", len(data), len(pic))
	}
}
