package tags

import (
	"bytes"
	"testing"
)

// 飞牛「歌曲信息」里的年份 / 曲目序号 / 光盘序号要能写进去、也能读回来。
// 用户 2026-09-22 要求「按字段补全」——这三个字段以前**根本没有存储位**。
func TestMP3YearTrackDiscRoundTrip(t *testing.T) {
	path := writeTemp(t, "meta.mp3", mp3Fixture())
	if _, err := Write(path, Metadata{Title: "晴天", Year: 2003, Track: 3, Disc: 1}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := readID3v2(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if got.Year != 2003 || got.Track != 3 || got.Disc != 1 {
		t.Fatalf("年份/曲序/光盘应回读一致，实际 year=%d track=%d disc=%d", got.Year, got.Track, got.Disc)
	}
}

// 0 = **不写入**（不是写 0）。平台没给年份就别往文件里塞一个假值。
func TestMP3ZeroMetaFieldsNotWritten(t *testing.T) {
	path := writeTemp(t, "zero.mp3", mp3Fixture())
	if _, err := Write(path, Metadata{Title: "x"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	data := mustRead(t, path)
	for _, frame := range []string{"TYER", "TDRC", "TRCK", "TPOS"} {
		if bytes.Contains(data, []byte(frame)) {
			t.Errorf("没有年份/曲序/光盘时不该写 %s 帧", frame)
		}
	}
}

// 年份**两个帧都写**：TYER 是 v2.3 的规范帧，TDRC 是 v2.4 的，
// 不少现代播放器只认后者。飞牛读哪个本地验证不了，两个都写最稳。
func TestMP3WritesBothYearFrames(t *testing.T) {
	path := writeTemp(t, "both.mp3", mp3Fixture())
	if _, err := Write(path, Metadata{Title: "x", Year: 2003}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	data := mustRead(t, path)
	if !bytes.Contains(data, []byte("TYER")) || !bytes.Contains(data, []byte("TDRC")) {
		t.Error("年份应同时写 TYER 与 TDRC（飞牛读哪个未验证，缺一个就可能不显示）")
	}
}

// FLAC 侧同样要能写回（DATE / TRACKNUMBER / DISCNUMBER）
func TestFLACYearTrackDiscRoundTrip(t *testing.T) {
	path := writeTemp(t, "meta.flac", flacFixture())
	if _, err := Write(path, Metadata{Title: "晴天", Year: 2003, Track: 3, Disc: 2}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := readFLAC(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if got.Year != 2003 || got.Track != 3 || got.Disc != 2 {
		t.Fatalf("年份/曲序/光盘应回读一致，实际 year=%d track=%d disc=%d", got.Year, got.Track, got.Disc)
	}
}

// FLAC 的年份**两个键都写**：DATE（ISO 8601，其它工具认）+ YEAR（纯数字 4 位）。
//
// 参考实现 music-meta-web v1.4.8（writer.py:11-15 / 127-143）在真机上对照过：
// 飞牛服务端读 FLAC/OGG 年份只认 YEAR，只写 date 的文件服务端返回 year=null。
// 与 MP3 侧同时写 TYER + TDRC 是同一个思路 —— 缺一个就可能不显示。
func TestFLACWritesBothYearKeys(t *testing.T) {
	path := writeTemp(t, "both.flac", flacFixture())
	if _, err := Write(path, Metadata{Title: "x", Year: 1999}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	data := mustRead(t, path)
	if !bytes.Contains(data, []byte("DATE=1999")) {
		t.Error("应写 DATE=1999")
	}
	if !bytes.Contains(data, []byte("YEAR=1999")) {
		t.Error("应写 YEAR=1999（飞牛年份栏只认这个键，缺了会显示为空）")
	}
	if got, _ := readFLAC(path); got.Year != 1999 {
		t.Errorf("年份应能读回，实际 %d", got.Year)
	}
}

// 年份为 0 = **不写入**（不是写 0）：平台没给年份就别往文件里塞假值。
func TestFLACZeroYearNotWritten(t *testing.T) {
	path := writeTemp(t, "zeroyear.flac", flacFixture())
	if _, err := Write(path, Metadata{Title: "x"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	data := mustRead(t, path)
	if bytes.Contains(data, []byte("DATE=")) || bytes.Contains(data, []byte("YEAR=")) {
		t.Error("没有年份时不该写 DATE / YEAR")
	}
}

// atoiPrefix：标签里的数字字段常见带后缀/日期形态
func TestAtoiPrefix(t *testing.T) {
	cases := map[string]int{
		"3": 3, "3/12": 3, "2003-07-31": 2003, "  7  ": 7, "": 0, "abc": 0,
	}
	for in, want := range cases {
		if got := atoiPrefix(in); got != want {
			t.Errorf("atoiPrefix(%q) = %d，期望 %d", in, got, want)
		}
	}
}
