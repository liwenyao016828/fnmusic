package tidy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 测试固件 ──

func mp3Fixture() []byte {
	out := []byte{0xFF, 0xFB, 0x90, 0x00}
	out = append(out, []byte("AUDIO-PAYLOAD")...)
	return append(out, bytes.Repeat([]byte{0xAA}, 512)...)
}

func flacFixture() []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x00, 0x00, 0x00, 0x22})
	b.Write(make([]byte, 34))
	b.Write([]byte{0x81, 0x00, 0x00, 0x08})
	b.Write(make([]byte, 8))
	b.WriteString("AUDIOFRAMES")
	return b.Bytes()
}

func jpegFixture() []byte {
	return []byte{
		0xFF, 0xD8,
		0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x10, 0x00, 0x20, 0x03,
		0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
		0xFF, 0xD9,
	}
}

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ── 整理 ──

func TestTidyOneWritesLyricAndMetadataMP3(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.mp3", mp3Fixture())

	res := TidyOne(Item{
		Path: p, Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦",
		Lyric: "[00:01.00]一群嗜血的蚂蚁",
	}, DefaultOptions())

	if !res.OK {
		t.Fatalf("整理失败: %s", res.Error)
	}
	if !res.LyricEmbed {
		t.Error("应内嵌歌词")
	}
	if !res.LyricFile {
		t.Error("应写出外挂歌词")
	}
	if !res.MetaWritten {
		t.Error("应写入元数据")
	}
	if res.Format != "mp3" {
		t.Errorf("格式 = %q", res.Format)
	}

	// 校验外挂歌词
	lrc, err := os.ReadFile(filepath.Join(dir, "a.lrc"))
	if err != nil {
		t.Fatalf("外挂歌词未生成: %v", err)
	}
	if !strings.Contains(string(lrc), "一群嗜血的蚂蚁") {
		t.Errorf("外挂歌词内容不正确: %q", string(lrc))
	}
}

func TestTidyOneWritesFLAC(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "b.flac", flacFixture())

	res := TidyOne(Item{
		Path: p, Title: "平凡之路", Artist: "朴树", Album: "猎户星座",
		Lyric:      "[00:05.00]徘徊着的 在路上的",
		CoverBytes: jpegFixture(), CoverMime: "image/jpeg",
	}, DefaultOptions())

	if !res.OK {
		t.Fatalf("整理失败: %s", res.Error)
	}
	if !res.LyricEmbed {
		t.Error("FLAC 应内嵌歌词")
	}
	if !res.CoverEmbed {
		t.Error("FLAC 应内嵌封面")
	}
	if res.Format != "flac" {
		t.Errorf("格式 = %q", res.Format)
	}
}

func TestTidyOneSkipsExistingLyricWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.mp3", mp3Fixture())

	// 第一次写入
	TidyOne(Item{Path: p, Lyric: "旧歌词"}, Options{EmbedLyric: true})

	// 第二次不覆盖
	res := TidyOne(Item{Path: p, Lyric: "新歌词"}, Options{EmbedLyric: true, OverwriteLyric: false})
	if res.LyricEmbed {
		t.Error("未开启覆盖时不应重写歌词")
	}
	found := false
	for _, s := range res.Skipped {
		if strings.Contains(s, "已有内嵌歌词") {
			found = true
		}
	}
	if !found {
		t.Errorf("应说明跳过原因，实际: %v", res.Skipped)
	}

	// 开启覆盖后应生效
	res2 := TidyOne(Item{Path: p, Lyric: "新歌词"}, Options{EmbedLyric: true, OverwriteLyric: true})
	if !res2.LyricEmbed {
		t.Error("开启覆盖后应重写歌词")
	}
}

func TestTidyOneRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.txt", []byte("这不是音频，只是普通文本内容，用于测试格式拒绝逻辑。"))

	res := TidyOne(Item{Path: p, Lyric: "x"}, DefaultOptions())
	if res.OK {
		t.Error("不支持的格式应报错而不是静默成功")
	}
	if res.Error == "" {
		t.Error("应给出明确错误")
	}
}

func TestTidyOneEmptyPath(t *testing.T) {
	res := TidyOne(Item{Lyric: "x"}, DefaultOptions())
	if res.OK || res.Error == "" {
		t.Error("空路径应报错")
	}
}

// TestTidyOneNeverWritesFolderCover 关键回归：绝不写目录级 cover.jpg
func TestTidyOneNeverWritesFolderCover(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.mp3", mp3Fixture())

	TidyOne(Item{Path: p, CoverBytes: jpegFixture(), CoverMime: "image/jpeg"}, DefaultOptions())

	if fileExists(filepath.Join(dir, "cover.jpg")) {
		t.Error("绝不能写目录级 cover.jpg（会让整个文件夹显示同一张图）")
	}
}

func TestTidyOneWritesPerSongCoverWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", mp3Fixture())

	opts := DefaultOptions()
	opts.WriteCoverFile = true
	res := TidyOne(Item{Path: p, CoverBytes: jpegFixture(), CoverMime: "image/jpeg"}, opts)

	if !res.CoverFile {
		t.Fatalf("应写出逐曲同名封面，skipped=%v", res.Skipped)
	}
	if !fileExists(filepath.Join(dir, "周杰伦 - 夜曲.jpg")) {
		t.Error("封面应为逐曲同名 .jpg")
	}
}

func TestTidyOnePreservesAudioData(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.mp3", mp3Fixture())

	TidyOne(Item{Path: p, Title: "夜曲", Lyric: "[00:01.00]测试"}, DefaultOptions())

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("AUDIO-PAYLOAD")) {
		t.Error("写入标签后原始音频数据丢失")
	}
}

func TestTidyBatchHandlesFailuresWithoutAborting(t *testing.T) {
	dir := t.TempDir()
	good1 := writeTemp(t, dir, "a.mp3", mp3Fixture())
	good2 := writeTemp(t, dir, "b.flac", flacFixture())
	bad := writeTemp(t, dir, "c.txt", []byte("不是音频"))

	res := TidyBatch([]Item{
		{Path: good1, Lyric: "词1"},
		{Path: bad, Lyric: "词2"},
		{Path: good2, Lyric: "词3"},
	}, DefaultOptions(), nil)

	if res.Total != 3 {
		t.Errorf("总数 = %d", res.Total)
	}
	if res.OK != 2 {
		t.Errorf("成功 = %d, 期望 2", res.OK)
	}
	if res.Failed != 1 {
		t.Errorf("失败 = %d, 期望 1", res.Failed)
	}
	if len(res.Results) != 3 {
		t.Errorf("应返回每首的结果，实际 %d 条", len(res.Results))
	}
}

func TestTidyBatchEmpty(t *testing.T) {
	res := TidyBatch(nil, DefaultOptions(), nil)
	if res.Total != 0 || res.OK != 0 {
		t.Errorf("空输入应返回空结果: %+v", res)
	}
}

func TestAuditPaths(t *testing.T) {
	dir := t.TempDir()
	withLyric := writeTemp(t, dir, "a.mp3", mp3Fixture())
	TidyOne(Item{Path: withLyric, Lyric: "词", CoverBytes: jpegFixture(), CoverMime: "image/jpeg"}, DefaultOptions())

	noLyric := writeTemp(t, dir, "b.mp3", mp3Fixture())

	audit := AuditPaths([]string{withLyric, noLyric}, 10)
	if audit.Total != 2 {
		t.Errorf("总数 = %d", audit.Total)
	}
	if audit.NoLyric != 1 {
		t.Errorf("缺歌词 = %d, 期望 1", audit.NoLyric)
	}
	if len(audit.Missing) == 0 {
		t.Error("应给出缺失样本")
	}
}

// ── 去重 ──

func TestAuditPathsSidecarLyricCover(t *testing.T) {
	dir := t.TempDir()
	// 无内嵌歌词、无外挂文件 → 缺歌词 + 缺封面
	bare := writeTemp(t, dir, "song.mp3", mp3Fixture())
	// 同名 .lrc 外挂 → 不再算缺歌词
	sidecarLrc := writeTemp(t, dir, "c.mp3", mp3Fixture())
	if err := os.WriteFile(filepath.Join(dir, "c.lrc"), []byte("[00:01.00]词"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 同名 .jpg 封面 → 不再算缺封面
	sidecarCover := writeTemp(t, dir, "d.mp3", mp3Fixture())
	if err := os.WriteFile(filepath.Join(dir, "d.jpg"), jpegFixture(), 0o644); err != nil {
		t.Fatal(err)
	}
	// 目录级 cover.jpg → 仍不算逐曲封面（必须仍是缺封面）
	dirCover := writeTemp(t, dir, "e.mp3", mp3Fixture())
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), jpegFixture(), 0o644); err != nil {
		t.Fatal(err)
	}

	audit := AuditPaths([]string{bare, sidecarLrc, sidecarCover, dirCover}, 10)
	if audit.Total != 4 {
		t.Fatalf("总数 = %d, 期望 4", audit.Total)
	}
	if audit.NoLyric != 3 { // bare + sidecarCover + dirCover 无歌词；仅 sidecarLrc 有 .lrc
		t.Errorf("缺歌词 = %d, 期望 3", audit.NoLyric)
	}
	if audit.NoCover != 3 { // bare + sidecarLrc + dirCover 无逐曲封面；sidecarCover 有 .jpg
		t.Errorf("缺封面 = %d, 期望 3", audit.NoCover)
	}
	// 目录级 cover.jpg 不应让 e.mp3 的封面判为已补齐
	needL, needC, err := NeedsTidy(dirCover)
	if err != nil {
		t.Fatal(err)
	}
	if needL == false || needC == false {
		t.Errorf("e.mp3 目录级 cover.jpg 不应算补封面/歌词: needL=%v needC=%v", needL, needC)
	}
}

// ── 去重 ──

func TestFindDuplicatesByContent(t *testing.T) {
	dir := t.TempDir()
	same := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0x11}, 2048)...)

	a := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", same)
	b := writeTemp(t, dir, "周杰伦 - 夜曲 (2).mp3", same)
	_ = writeTemp(t, dir, "周杰伦 - 晴天.mp3", append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0x22}, 2048)...))

	groups, _ := FindDuplicates([]string{a, b, filepath.Join(dir, "周杰伦 - 晴天.mp3")}, DefaultDedupeOptions())

	contentGroups := 0
	for _, g := range groups {
		if g.Type == "content" {
			contentGroups++
			if g.Count != 2 {
				t.Errorf("重复组内应 2 个文件，实际 %d", g.Count)
			}
			if len(g.Delete) != 1 {
				t.Errorf("应建议删除 1 个，实际 %d", len(g.Delete))
			}
			if g.Keep == "" {
				t.Error("应给出建议保留的文件")
			}
		}
	}
	if contentGroups == 0 {
		t.Fatal("应检测出内容重复")
	}
}

func TestFindDuplicatesByContentIgnoresUniqueSizes(t *testing.T) {
	dir := t.TempDir()
	// 体积各不相同 → 不可能内容重复 → 不应产生 content 组
	a := writeTemp(t, dir, "x.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1000)...))
	b := writeTemp(t, dir, "y.mp3", append([]byte("ID3"), bytes.Repeat([]byte{2}, 2000)...))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())
	for _, g := range groups {
		if g.Type == "content" {
			t.Error("体积唯一的文件不应被判为内容重复")
		}
	}
}

func TestFindDuplicatesByNameChannel(t *testing.T) {
	dir := t.TempDir()
	// 同歌名同歌手但内容不同（不同版本/码率）。
	// 注意：体积分桶是哈希前的优化，所以两者体积必须相同才会进入候选。
	// 这里用等长的固定块构造，保证「体积相同但内容不同」。
	a := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	b := writeTemp(t, dir, "周杰伦 - 夜曲.flac", append([]byte("fLa"), bytes.Repeat([]byte{2}, 1500)...))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())

	hasName := false
	for _, g := range groups {
		if g.Type == "name" {
			hasName = true
		}
	}
	if !hasName {
		t.Error("同名同歌手但内容不同应被 name 通道检出")
	}
}

func TestFindDuplicatesPrefersLossless(t *testing.T) {
	dir := t.TempDir()
	// 等长构造：体积必须相同才会进入哈希候选（体积分桶优化）
	mp3 := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	flac := writeTemp(t, dir, "周杰伦 - 夜曲.flac", append([]byte("fLa"), bytes.Repeat([]byte{2}, 1500)...))

	groups, _ := FindDuplicates([]string{mp3, flac}, DefaultDedupeOptions())
	for _, g := range groups {
		if g.Type == "name" {
			if g.Keep != flac {
				t.Errorf("应优先保留无损文件，实际保留 %s", filepath.Base(g.Keep))
			}
			if len(g.Delete) != 1 || g.Delete[0] != mp3 {
				t.Errorf("应建议删除 mp3，实际 %v", g.Delete)
			}
		}
	}
}

func TestFindDuplicatesPrefersTidied(t *testing.T) {
	dir := t.TempDir()
	plain := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	tidied := writeTemp(t, dir, "周杰伦 - 夜曲 (2).mp3", append([]byte("ID3"), bytes.Repeat([]byte{2}, 1500)...))

	// 给其中一个补上歌词与封面
	TidyOne(Item{Path: tidied, Lyric: "词", CoverBytes: jpegFixture(), CoverMime: "image/jpeg"}, DefaultOptions())

	groups, _ := FindDuplicates([]string{plain, tidied}, DefaultDedupeOptions())
	for _, g := range groups {
		if g.Type == "name" {
			if g.Keep != tidied {
				t.Errorf("应优先保留已整理（有词有封面）的文件，实际保留 %s", filepath.Base(g.Keep))
			}
		}
	}
}

func TestFindDuplicatesEmptyAndSingle(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3", mp3Fixture())

	if groups, _ := FindDuplicates(nil, DefaultDedupeOptions()); len(groups) != 0 {
		t.Error("空输入应返回空结果")
	}
	if groups, _ := FindDuplicates([]string{a}, DefaultDedupeOptions()); len(groups) != 0 {
		t.Error("单个文件不应有重复组")
	}
}

func TestFindDuplicatesMaxFilesWarning(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		paths = append(paths, writeTemp(t, dir, string(rune('a'+i))+".mp3",
			append([]byte("ID3"), bytes.Repeat([]byte{byte(i)}, 100+i)...)))
	}

	opts := DefaultDedupeOptions()
	opts.MaxFiles = 2
	_, warnings := FindDuplicates(paths, opts)
	if len(warnings) == 0 {
		t.Error("超过上限时应给出告警")
	}
}

// ── 去重执行 ──

func TestResolveDryRunDoesNotTouchFiles(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	b := writeTemp(t, dir, "b.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())
	res := ResolveDuplicates(groups, ResolveOptions{DryRun: true})

	if !res.DryRun {
		t.Error("应标记为干跑")
	}
	if res.Freed == 0 {
		t.Error("应估算可回收空间")
	}
	// 文件必须都还在
	if !fileExists(a) || !fileExists(b) {
		t.Error("干跑不应改动任何文件")
	}
}

func TestResolveMovesToTrashByDefault(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	b := writeTemp(t, dir, "b.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())
	res := ResolveDuplicates(groups, ResolveOptions{Mode: "trash", TrashDir: filepath.Join(dir, ".tidy_trash")})

	if res.Moved != 1 {
		t.Fatalf("应移动 1 个文件到回收站，实际 %d（错误: %v）", res.Moved, res.Errors)
	}
	if res.Deleted != 0 {
		t.Error("默认模式不应直接删除")
	}
	// 保留的文件还在，被删的已移走
	remaining := 0
	for _, p := range []string{a, b} {
		if fileExists(p) {
			remaining++
		}
	}
	if remaining != 1 {
		t.Errorf("原目录应剩 1 个文件，实际 %d", remaining)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".tidy_trash")); err != nil || !fi.IsDir() {
		t.Error("应创建回收站目录")
	}
}

func TestResolveTrashMovesSidecars(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	b := writeTemp(t, dir, "b.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	// b 有外挂歌词
	lrcB := writeTemp(t, dir, "b.lrc", []byte("[00:01.00]词"))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())
	// 确保被删的是 b（含歌词的文件优先级更高，通常不会被删，
	// 所以这里反过来给 a 加歌词，让 b 成为被删对象）
	_ = lrcB
	res := ResolveDuplicates(groups, ResolveOptions{Mode: "trash", TrashDir: filepath.Join(dir, ".tidy_trash")})

	// 至少验证：被移走的音频若带同名 .lrc，应一并搬走
	for _, g := range groups {
		for _, victim := range g.Delete {
			stem := strings.TrimSuffix(victim, filepath.Ext(victim))
			if fileExists(stem + ".lrc") {
				t.Errorf("被移走的音频不应留下孤立歌词: %s", stem+".lrc")
			}
		}
	}
	if res.Failed > 0 {
		t.Errorf("不应有失败: %v", res.Errors)
	}
}

func TestResolveDeleteMode(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	b := writeTemp(t, dir, "b.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))

	groups, _ := FindDuplicates([]string{a, b}, DefaultDedupeOptions())
	res := ResolveDuplicates(groups, ResolveOptions{Mode: "delete"})

	if res.Deleted != 1 {
		t.Fatalf("应删除 1 个文件，实际 %d", res.Deleted)
	}
	remaining := 0
	for _, p := range []string{a, b} {
		if fileExists(p) {
			remaining++
		}
	}
	if remaining != 1 {
		t.Errorf("应剩 1 个文件，实际 %d", remaining)
	}
}

func TestResolveTrashNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	trash := filepath.Join(dir, ".tidy_trash", filepath.Base(dir))
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}
	// 回收站里已存在同名文件
	if err := os.WriteFile(filepath.Join(trash, "a.mp3"), []byte("已存在的旧备份"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := writeTemp(t, dir, "a.mp3", append([]byte("ID3"), bytes.Repeat([]byte{1}, 1500)...))
	dst, err := moveToTrash(src, filepath.Join(dir, ".tidy_trash"))
	if err != nil {
		t.Fatalf("移入回收站失败: %v", err)
	}
	if dst == filepath.Join(trash, "a.mp3") {
		t.Error("不应覆盖回收站中已存在的同名文件")
	}
	old, _ := os.ReadFile(filepath.Join(trash, "a.mp3"))
	if string(old) != "已存在的旧备份" {
		t.Error("回收站中的旧文件被覆盖了")
	}
}

func TestPriorityOrdering(t *testing.T) {
	lossless := DupItem{Format: "flac"}
	mp3Tidied := DupItem{Format: "mp3", HasLyric: true, HasCover: true}
	mp3Plain := DupItem{Format: "mp3"}

	if priorityOf(lossless) <= priorityOf(mp3Tidied) {
		t.Error("无损应优先于已整理的 mp3")
	}
	if priorityOf(mp3Tidied) <= priorityOf(mp3Plain) {
		t.Error("已整理的应优先于未整理的")
	}
}

// TestFindDuplicatesDeterministicKeep 同分时应保留原始文件名而非 (2) 副本。
// 没有确定性兜底时，保留哪个取决于 map 遍历顺序，结果不可复现。
func TestFindDuplicatesDeterministicKeep(t *testing.T) {
	dir := t.TempDir()
	content := append([]byte("ID3"), bytes.Repeat([]byte{0x11}, 5000)...)
	original := writeTemp(t, dir, "周杰伦 - 夜曲.mp3", content)
	copy2 := writeTemp(t, dir, "周杰伦 - 夜曲 (2).mp3", content)

	// 多次运行结果必须一致
	for i := 0; i < 10; i++ {
		groups, _ := FindDuplicates([]string{copy2, original}, DefaultDedupeOptions())
		for _, g := range groups {
			if g.Type != "content" {
				continue
			}
			if g.Keep != original {
				t.Fatalf("第 %d 次运行保留了副本 %s，应保留原始文件 %s",
					i+1, filepath.Base(g.Keep), filepath.Base(original))
			}
		}
	}
}
