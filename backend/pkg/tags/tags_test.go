package tags

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 测试固件 ──

// jpegFixture 一个最小可用 JPEG 头（含 SOF0，声明 width=32 height=16 3 通道）
func jpegFixture() []byte {
	return []byte{
		0xFF, 0xD8,
		0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x10, 0x00, 0x20, 0x03,
		0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
		0xFF, 0xD9,
	}
}

const mp3AudioMarker = "MP3-AUDIO-PAYLOAD-START"

func mp3Fixture() []byte {
	out := []byte{0xFF, 0xFB, 0x90, 0x00}
	out = append(out, []byte(mp3AudioMarker)...)
	out = append(out, bytes.Repeat([]byte{0xAA}, 512)...)
	return out
}

const flacAudioMarker = "AUDIO-FRAMES-PAYLOAD"

func flacFixture() []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	// STREAMINFO（last=false, type=0, len=34）
	b.Write([]byte{0x00, 0x00, 0x00, 0x22})
	b.Write(make([]byte, 34))
	// PADDING（last=true, type=1, len=8）
	b.Write([]byte{0x81, 0x00, 0x00, 0x08})
	b.Write(make([]byte, 8))
	b.WriteString(flacAudioMarker)
	return b.Bytes()
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	return path
}

// ── MP3 / ID3v2 ──

// 本次不带封面时，**不能**把文件里已有的 APIC 抹掉。
//
// 守的原因：MP3 写入策略是「丢弃旧 ID3 + 只重建受管帧」，
// 而**飞牛只认内嵌封面**（`docs/飞牛刮削适配调研.md`；参考实现同结论）。
// 以前「只想改个歌词/歌手」会把封面一起删掉 —— FLAC 有保留逻辑，MP3 一直没有，
// 用户看到的就是「封面凭空消失」（2026-09-22 反馈「飞牛里不显示封面」的成因之一）。
func TestMP3KeepsExistingCoverWhenNotProvided(t *testing.T) {
	cover := jpegFixture()
	existing := buildID3v2Tag(Metadata{Title: "旧标题", Cover: cover, CoverMime: "image/jpeg"})
	path := writeTemp(t, "keep-cover.mp3", append(existing, mp3Fixture()...))

	// 只改歌手，不带封面
	if _, err := Write(path, Metadata{Artist: "新歌手"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	got, err := readID3v2(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if len(got.Cover) == 0 {
		t.Fatal("既有封面被抹掉了 —— 飞牛只认内嵌封面，用户会看到封面消失")
	}
	if !bytes.Equal(got.Cover, cover) {
		t.Error("保留下来的封面字节与原来不一致")
	}
	if got.Artist != "新歌手" {
		t.Errorf("歌手应更新为「新歌手」，实际 %q", got.Artist)
	}
}

// 反向保护：明确给了新封面时要覆盖旧的，别把「保留」写成「永远不换」。
func TestMP3ReplacesCoverWhenProvided(t *testing.T) {
	oldCover := jpegFixture()
	newCover := append(append([]byte{}, jpegFixture()...), 0x01, 0x02, 0x03)

	existing := buildID3v2Tag(Metadata{Title: "x", Cover: oldCover, CoverMime: "image/jpeg"})
	path := writeTemp(t, "replace-cover.mp3", append(existing, mp3Fixture()...))

	if _, err := Write(path, Metadata{Title: "x", Cover: newCover, CoverMime: "image/jpeg"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, _ := readID3v2(path)
	if !bytes.Equal(got.Cover, newCover) {
		t.Error("给了新封面时应覆盖旧的")
	}
}

func TestMP3WriteReadRoundTrip(t *testing.T) {
	path := writeTemp(t, "song.mp3", mp3Fixture())

	meta := Metadata{
		Title:     "夜曲",
		Artist:    "周杰伦",
		Album:     "十一月的萧邦",
		Lyric:     "[00:01.00]一群嗜血的蚂蚁",
		Cover:     jpegFixture(),
		CoverMime: "image/jpeg",
	}

	format, err := Write(path, meta)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if format != FormatMP3 {
		t.Fatalf("format = %q, 期望 %q", format, FormatMP3)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.Title != meta.Title {
		t.Errorf("Title = %q, 期望 %q", got.Title, meta.Title)
	}
	if got.Artist != meta.Artist {
		t.Errorf("Artist = %q, 期望 %q", got.Artist, meta.Artist)
	}
	if got.Album != meta.Album {
		t.Errorf("Album = %q, 期望 %q", got.Album, meta.Album)
	}
	if strings.TrimSpace(got.Lyric) != meta.Lyric {
		t.Errorf("Lyric = %q, 期望 %q", got.Lyric, meta.Lyric)
	}
	if !bytes.Equal(got.Cover, meta.Cover) {
		t.Errorf("Cover 长度 = %d, 期望 %d", len(got.Cover), len(meta.Cover))
	}
	if got.CoverMime != "image/jpeg" {
		t.Errorf("CoverMime = %q", got.CoverMime)
	}
}

func TestMP3PreservesAudioData(t *testing.T) {
	path := writeTemp(t, "song.mp3", mp3Fixture())

	if _, err := Write(path, Metadata{Lyric: "[00:01.00]测试歌词"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(data, []byte(mp3AudioMarker)) {
		t.Fatal("写入标签后原始音频数据丢失")
	}
	// 音频数据必须完好地位于标签之后
	idx := bytes.Index(data, []byte(mp3AudioMarker))
	if idx <= 0 {
		t.Fatal("音频数据位置异常")
	}
	rest := data[idx:]
	expectedTail := mp3Fixture()[4:]
	if !bytes.Equal(rest, expectedTail) {
		t.Fatal("标签之后的音频字节与原始数据不一致")
	}
}

func TestMP3RewriteReplacesTagInsteadOfDuplicating(t *testing.T) {
	path := writeTemp(t, "song.mp3", mp3Fixture())

	if _, err := Write(path, Metadata{Title: "第一次", Lyric: "[00:01.00]第一版歌词"}); err != nil {
		t.Fatalf("第一次 Write: %v", err)
	}
	sizeAfterFirst, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Write(path, Metadata{Title: "第二次", Lyric: "[00:02.00]第二版歌词"}); err != nil {
		t.Fatalf("第二次 Write: %v", err)
	}
	sizeAfterSecond, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Title != "第二次" {
		t.Errorf("Title = %q, 期望「第二次」（旧标签未被替换）", got.Title)
	}
	if !strings.Contains(got.Lyric, "第二版歌词") {
		t.Errorf("Lyric = %q, 期望包含「第二版歌词」", got.Lyric)
	}

	// 标签被替换而非累加：体积不应显著膨胀
	if sizeAfterSecond.Size() > sizeAfterFirst.Size()*2 {
		t.Errorf("重复写入后文件体积异常膨胀: %d -> %d", sizeAfterFirst.Size(), sizeAfterSecond.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 文件头部只应有一个 ID3v2 标签
	if !bytes.HasPrefix(data, []byte("ID3")) {
		t.Fatal("文件未以 ID3 标签开头")
	}
	if n := bytes.Count(data, []byte("ID3\x03\x00\x00")); n != 1 {
		t.Errorf("文件中出现 %d 个 ID3v2.3 标签头，期望 1 个", n)
	}
}

func TestMP3WritesExistingID3v2PrefixedFile(t *testing.T) {
	// 模拟真实 MP3：已有 ID3v2 标签 + 音频
	existing := buildID3v2Tag(Metadata{Title: "旧标题", Artist: "旧歌手"})
	raw := append(existing, mp3Fixture()[4:]...)
	path := writeTemp(t, "existing.mp3", raw)

	if _, err := Write(path, Metadata{Title: "新标题", Lyric: "[00:01.00]新歌词"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Title != "新标题" {
		t.Errorf("Title = %q, 期望「新标题」", got.Title)
	}
	if got.Artist != "旧歌手" {
		// 「只重建受管帧」不等于「把没传的字段抹掉」：未传的旧值必须合并保留，
		// 否则「只想改个标题」就会连歌手一起清掉（见 Write 里的 mergeTextFields）。
		t.Errorf("Artist = %q, 期望保留既有的「旧歌手」", got.Artist)
	}

	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte(mp3AudioMarker)) {
		t.Fatal("音频数据丢失")
	}
	if n := bytes.Count(data, []byte("ID3\x03\x00\x00")); n != 1 {
		t.Errorf("出现 %d 个 ID3v2.3 标签头，期望 1 个", n)
	}
}

// ── FLAC ──

func TestFLACWriteReadRoundTrip(t *testing.T) {
	path := writeTemp(t, "song.flac", flacFixture())

	meta := Metadata{
		Title:     "晴天",
		Artist:    "周杰伦",
		Album:     "叶惠美",
		Lyric:     "[00:03.00]故事的小黄花",
		Cover:     jpegFixture(),
		CoverMime: "image/jpeg",
	}

	format, err := Write(path, meta)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if format != FormatFLAC {
		t.Fatalf("format = %q, 期望 %q", format, FormatFLAC)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.Title != meta.Title {
		t.Errorf("Title = %q, 期望 %q", got.Title, meta.Title)
	}
	if got.Artist != meta.Artist {
		t.Errorf("Artist = %q, 期望 %q", got.Artist, meta.Artist)
	}
	if got.Album != meta.Album {
		t.Errorf("Album = %q, 期望 %q", got.Album, meta.Album)
	}
	if got.Lyric != meta.Lyric {
		t.Errorf("Lyric = %q, 期望 %q", got.Lyric, meta.Lyric)
	}
	if !bytes.Equal(got.Cover, meta.Cover) {
		t.Errorf("Cover 长度 = %d, 期望 %d", len(got.Cover), len(meta.Cover))
	}
}

func TestFLACPreservesAudioAndStreamInfo(t *testing.T) {
	path := writeTemp(t, "song.flac", flacFixture())

	if _, err := Write(path, Metadata{Lyric: "[00:03.00]故事的小黄花"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("fLaC")) {
		t.Fatal("FLAC magic 丢失")
	}
	if !bytes.HasSuffix(data, []byte(flacAudioMarker)) {
		t.Fatal("FLAC 音频帧数据丢失或位置改变")
	}

	// STREAMINFO 必须是第一个元数据块
	if data[4]&0x7F != flacBlockStreamInfo {
		t.Errorf("第一个元数据块类型 = %d, 期望 STREAMINFO(0)", data[4]&0x7F)
	}

	// 校验块结构自洽：逐个块遍历，能恰好走到音频起始处
	blocks, err := parseBlocksFromBytes(t, data)
	if err != nil {
		t.Fatalf("重建后的 FLAC 块结构非法: %v", err)
	}
	hasVC := false
	lastCount := 0
	for i, b := range blocks {
		if b.blockType == flacBlockVorbisComment {
			hasVC = true
		}
		if b.last {
			lastCount++
			if i != len(blocks)-1 {
				t.Error("last-block 标记出现在非末尾块上")
			}
		}
	}
	if !hasVC {
		t.Error("未找到 VORBIS_COMMENT 块")
	}
	if lastCount != 1 {
		t.Errorf("last-block 标记数量 = %d, 期望 1", lastCount)
	}
}

type parsedBlock struct {
	blockType byte
	last      bool
}

func parseBlocksFromBytes(t *testing.T, data []byte) ([]parsedBlock, error) {
	t.Helper()
	if len(data) < 4 || string(data[:4]) != "fLaC" {
		return nil, errors.New("bad magic")
	}
	pos := 4
	out := []parsedBlock{}
	for i := 0; i < 1024; i++ {
		if pos+4 > len(data) {
			return nil, errors.New("truncated header")
		}
		h := data[pos : pos+4]
		last := h[0]&0x80 != 0
		bt := h[0] & 0x7F
		length := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		pos += 4
		if pos+length > len(data) {
			return nil, errors.New("truncated block body")
		}
		pos += length
		out = append(out, parsedBlock{blockType: bt, last: last})
		if last {
			break
		}
	}
	return out, nil
}

func TestFLACRewriteReplacesFields(t *testing.T) {
	path := writeTemp(t, "song.flac", flacFixture())

	if _, err := Write(path, Metadata{Title: "旧标题", Lyric: "旧歌词", Cover: jpegFixture(), CoverMime: "image/jpeg"}); err != nil {
		t.Fatalf("第一次 Write: %v", err)
	}
	if _, err := Write(path, Metadata{Title: "新标题", Lyric: "新歌词"}); err != nil {
		t.Fatalf("第二次 Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Title != "新标题" {
		t.Errorf("Title = %q, 期望「新标题」", got.Title)
	}
	if got.Lyric != "新歌词" {
		t.Errorf("Lyric = %q, 期望「新歌词」", got.Lyric)
	}
	if !bytes.HasSuffix(mustRead(t, path), []byte(flacAudioMarker)) {
		t.Error("音频数据丢失")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ── 格式嗅探与边界 ──

func TestSniffFormat(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
	}{
		{"flac", flacFixture(), FormatFLAC},
		{"mp3-id3", append(buildID3v2Tag(Metadata{Title: "x"}), 0x00, 0x01), FormatMP3},
		{"mp3-sync", mp3Fixture(), FormatMP3},
		{"unknown-text", []byte("this is not audio at all"), FormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.name, tc.data)
			got, err := SniffFormat(path)
			if err != nil {
				t.Fatalf("SniffFormat: %v", err)
			}
			if got != tc.want {
				t.Errorf("SniffFormat = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

func TestWriteRejectsUnsupportedAndEmpty(t *testing.T) {
	path := writeTemp(t, "note.txt", []byte("这不是音频文件，只是普通文本内容。"))

	if _, err := Write(path, Metadata{Title: "x"}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("err = %v, 期望 ErrUnsupportedFormat", err)
	}

	mp3 := writeTemp(t, "a.mp3", mp3Fixture())
	if _, err := Write(mp3, Metadata{}); !errors.Is(err, ErrEmptyMetadata) {
		t.Errorf("err = %v, 期望 ErrEmptyMetadata", err)
	}
}

func TestHasLyric(t *testing.T) {
	mp3 := writeTemp(t, "a.mp3", mp3Fixture())
	if HasLyric(mp3) {
		t.Error("未写入歌词时 HasLyric 应为 false")
	}
	if _, err := Write(mp3, Metadata{Lyric: "[00:01.00]hello"}); err != nil {
		t.Fatal(err)
	}
	if !HasLyric(mp3) {
		t.Error("写入歌词后 HasLyric 应为 true")
	}

	flac := writeTemp(t, "b.flac", flacFixture())
	if _, err := Write(flac, Metadata{Lyric: "[00:01.00]hi"}); err != nil {
		t.Fatal(err)
	}
	if !HasLyric(flac) {
		t.Error("FLAC 写入歌词后 HasLyric 应为 true")
	}
}

func TestImageDimensions(t *testing.T) {
	w, h, d := imageDimensions(jpegFixture())
	if w != 32 || h != 16 || d != 24 {
		t.Errorf("JPEG 尺寸 = %dx%d@%d, 期望 32x16@24", w, h, d)
	}
}

func TestSynchsafeRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 255, 1000, 123456, 0x0FFFFFFF} {
		if got := unsynchsafe(synchsafe(n)); got != n {
			t.Errorf("synchsafe 往返失败: %d -> %d", n, got)
		}
	}
}
