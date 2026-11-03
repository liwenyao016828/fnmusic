package nas

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── 封面字节缓存（2026-11-03）──
//
// 用户报「本地曲库的封面有时能显示、有时不能」。列表一屏几百张缩略图，
// 而 extractCoverBytes 每次都把整个音频文件重新解析一遍 → 后端在同时解几十个大文件，
// 慢的那几张被前端当成失败。缓存之后只有第一张要解析。
//
// 这里守的是「缓存的判据必须跟着文件走」：路径 + size + mtime 变了就必须重算，
// 否则用户换了封面还要等重启才看得到（那是比「看不到」更难查的 bug）。

// mp3WithAPIC 造一个带 APIC 帧的最小 MP3（够 hasEmbeddedCover / extractCoverBytes 认得）。
func mp3WithAPIC(t *testing.T, img []byte, extra int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")

	// APIC 帧体：mime + \x00 + 图片类型 + 描述 + \x00 + 图片数据
	body := append([]byte("image/jpeg\x00\x03cover\x00"), img...)
	frame := []byte("APIC")
	size := len(body)
	// v2.3 帧头：id(4) + size(4, 大端) + flags(2)
	frame = append(frame,
		byte(size>>24), byte(size>>16), byte(size>>8), byte(size),
		0x00, 0x00)
	frame = append(frame, body...)

	// ID3v2.3 头：ID3 + ver + flags + size(4, syncsafe)
	tagSize := len(frame)
	hdr := []byte{'I', 'D', '3', 0x03, 0x00, 0x00,
		byte((tagSize >> 21) & 0x7f), byte((tagSize >> 14) & 0x7f),
		byte((tagSize >> 7) & 0x7f), byte(tagSize & 0x7f)}

	data := append(hdr, frame...)
	data = append(data, make([]byte, extra)...) // 撑大文件，验证 size 参与判据
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func jpegBytes(n int, fill byte) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, n)...)
}

func TestCoverBytesCachesByFile(t *testing.T) {
	img := jpegBytes(64, 0x01)
	p := mp3WithAPIC(t, img, 0)

	first, mime := coverBytes(p)
	if len(first) == 0 {
		t.Fatal("第一次应当解析出封面")
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}

	// 缓存命中：返回的必须是同一个底层数组（同一次解析的结果），不是重新解一遍
	second, _ := coverBytes(p)
	if &first[0] != &second[0] {
		t.Fatal("同一文件两次读取应当命中缓存（返回同一份字节）")
	}
}

func TestCoverCacheInvalidatesOnChange(t *testing.T) {
	old := jpegBytes(64, 0x01)
	p := mp3WithAPIC(t, old, 0)
	if got, _ := coverBytes(p); len(got) == 0 {
		t.Fatal("先要有封面")
	}

	// 换一张明显不同的封面（长度也不同 → size 变）
	fresh := jpegBytes(200, 0x02)
	mtime := time.Now().Add(2 * time.Second) // 显式推后，避免同秒写入导致 mtime 相同
	p2 := mp3WithAPIC(t, fresh, 0)
	if err := os.Chtimes(p2, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	// 同一个路径重写（等价于用户「换封面」）
	if err := copyFile(p2, p); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	got, _ := coverBytes(p)
	if len(got) == len(old) {
		t.Fatalf("文件换了封面之后仍拿到旧字节（len=%d）—— 缓存没有按 size/mtime 失效", len(got))
	}
}

func TestCoverCacheMissForMissingFile(t *testing.T) {
	if data, _ := coverBytes(filepath.Join(t.TempDir(), "nope.mp3")); len(data) != 0 {
		t.Fatal("不存在的文件不该有封面")
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
