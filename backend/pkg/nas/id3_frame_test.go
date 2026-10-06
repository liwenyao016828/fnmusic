package nas

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ── ID3v2 帧长解析回归测试 ──
//
// 背景：ffmpeg 等工具默认写出 ID3v2.4，其帧长度使用 syncsafe 编码。
// 早期实现统一按 v2.3 的普通大端解析，导致长度 >= 128 的帧（APIC 封面、
// USLT 歌词等）被算成 2 倍以上，越界判断直接中断，封面与歌词都读不出来。

func ss4(n int) []byte {
	return []byte{byte((n >> 21) & 0x7F), byte((n >> 14) & 0x7F), byte((n >> 7) & 0x7F), byte(n & 0x7F)}
}

type testFrame struct {
	id   string
	body []byte
}

// buildID3File 构造一个带 ID3v2 标签的假 MP3
func buildID3File(t *testing.T, major byte, frames []testFrame) string {
	t.Helper()

	var body bytes.Buffer
	for _, fr := range frames {
		body.WriteString(fr.id)
		if major >= 4 {
			body.Write(ss4(len(fr.body)))
		} else {
			sz := make([]byte, 4)
			binary.BigEndian.PutUint32(sz, uint32(len(fr.body)))
			body.Write(sz)
		}
		body.Write([]byte{0, 0}) // flags
		body.Write(fr.body)
	}

	var out bytes.Buffer
	out.WriteString("ID3")
	out.Write([]byte{major, 0, 0})
	out.Write(ss4(body.Len()))
	out.Write(body.Bytes())
	// 少量伪音频数据
	out.Write(bytes.Repeat([]byte{0xFF, 0xFB}, 64))

	path := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// buildAPICBody 构造 APIC 帧体（编码 + MIME + 类型 + 描述 + 图片数据）
func buildAPICBody(mime string, pic []byte) []byte {
	b := []byte{0x00}
	b = append(b, []byte(mime)...)
	b = append(b, 0x00, 0x03, 0x00)
	return append(b, pic...)
}

// fakeCover 生成一段带 JPEG 魔数的假封面（长度刻意 > 128 以暴露 syncsafe 差异）
func fakeCover(n int) []byte {
	pic := make([]byte, n)
	copy(pic, []byte{0xFF, 0xD8, 0xFF, 0xE0})
	for i := 4; i < n; i++ {
		pic[i] = byte(i % 251)
	}
	return pic
}

func TestExtractCoverID3v24SyncsafeFrameSize(t *testing.T) {
	pic := fakeCover(400) // 400 字节：syncsafe 与普通大端结果不同

	path := buildID3File(t, 4, []testFrame{
		{id: "TIT2", body: append([]byte{0x03}, []byte("夜曲")...)},
		{id: "APIC", body: buildAPICBody("image/jpeg", pic)},
	})

	data, mime := extractCoverBytes(path)
	if len(data) == 0 {
		t.Fatal("ID3v2.4 的内嵌封面应当能被提取出来（syncsafe 帧长解析回归）")
	}
	if !bytes.Equal(data, pic) {
		t.Errorf("封面数据不一致: got %d bytes, want %d", len(data), len(pic))
	}
	if mime != "image/jpeg" {
		t.Errorf("MIME = %q, 期望 image/jpeg", mime)
	}
}

func TestExtractCoverID3v23PlainFrameSize(t *testing.T) {
	pic := fakeCover(400)

	path := buildID3File(t, 3, []testFrame{
		{id: "TIT2", body: append([]byte{0x03}, []byte("晴天")...)},
		{id: "APIC", body: buildAPICBody("image/jpeg", pic)},
	})

	data, mime := extractCoverBytes(path)
	if len(data) == 0 {
		t.Fatal("ID3v2.3 的内嵌封面应当能被提取出来")
	}
	if !bytes.Equal(data, pic) {
		t.Errorf("封面数据不一致: got %d bytes, want %d", len(data), len(pic))
	}
	if mime != "image/jpeg" {
		t.Errorf("MIME = %q", mime)
	}
}

func TestReadID3v2TagsID3v24WithLargeFrame(t *testing.T) {
	// 让 APIC 排在大文本帧之前，模拟真实文件中「大帧导致解析中断」的场景
	path := buildID3File(t, 4, []testFrame{
		{id: "APIC", body: buildAPICBody("image/jpeg", fakeCover(500))},
		{id: "TIT2", body: append([]byte{0x03}, []byte("富士山下")...)},
		{id: "TPE1", body: append([]byte{0x03}, []byte("陈奕迅")...)},
		{id: "TALB", body: append([]byte{0x03}, []byte("What's Going On")...)},
	})

	title, artist, album, ok := readID3v2Tags(path)
	if !ok {
		t.Fatal("readID3v2Tags 应当成功")
	}
	if title != "富士山下" {
		t.Errorf("title = %q（大帧之后的文本帧应仍可读到）", title)
	}
	if artist != "陈奕迅" {
		t.Errorf("artist = %q", artist)
	}
	if album != "What's Going On" {
		t.Errorf("album = %q", album)
	}
}

func TestReadID3v2TagsID3v23(t *testing.T) {
	path := buildID3File(t, 3, []testFrame{
		{id: "TIT2", body: append([]byte{0x03}, []byte("光年之外")...)},
		{id: "TPE1", body: append([]byte{0x03}, []byte("邓紫棋")...)},
	})

	title, artist, _, ok := readID3v2Tags(path)
	if !ok {
		t.Fatal("readID3v2Tags 应当成功")
	}
	if title != "光年之外" || artist != "邓紫棋" {
		t.Errorf("解析结果 = %q / %q", title, artist)
	}
}

func TestId3FrameSizeVersions(t *testing.T) {
	// 大端 0x00 0x00 0x01 0x00 = 256；同一字节按 syncsafe 则为 128
	raw := []byte{0x00, 0x00, 0x01, 0x00}

	if got := id3FrameSize(raw, 3); got != 256 {
		t.Errorf("ID3v2.3 帧长 = %d, 期望 256", got)
	}
	if got := id3FrameSize(raw, 4); got != 128 {
		t.Errorf("ID3v2.4 帧长 = %d, 期望 128 (syncsafe)", got)
	}
}
