package tidy

import (
	"testing"

	"fn-lx-player/pkg/tags"
)

// 年份/曲序/光盘/风格要真能落到文件里（飞牛「歌曲信息」那四栏读的就是它们）。
func TestTidyOneWritesYearTrackDiscGenre(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.mp3", mp3Fixture())

	res := TidyOne(Item{
		Path: p, Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦",
		Genre: "Pop 流行", Year: 2005, Track: 3, Disc: 1,
	}, DefaultOptions())
	if !res.OK {
		t.Fatalf("TidyOne 失败: %s", res.Error)
	}
	if !res.MetaWritten {
		t.Error("MetaWritten = false，数字字段/风格写入未被记录")
	}

	got, err := tags.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year != 2005 || got.Track != 3 || got.Disc != 1 {
		t.Errorf("年份/曲序/光盘 = %d/%d/%d, 期望 2005/3/1", got.Year, got.Track, got.Disc)
	}
	if got.Genre != "Pop 流行" {
		t.Errorf("Genre = %q, 期望「Pop 流行」", got.Genre)
	}
}

// 只补风格（标题歌手专辑都没给、年份为 0）时 MetaWritten 必须为真 ——
// 以前的判据漏了风格与数字字段，complete 会把「GenreFilled」报成 false。
func TestTidyOneGenreOnlyCountsAsMetaWritten(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "g.mp3", mp3Fixture())

	res := TidyOne(Item{Path: p, Genre: "民谣"}, DefaultOptions())
	if !res.OK {
		t.Fatalf("TidyOne 失败: %s", res.Error)
	}
	if !res.MetaWritten {
		t.Error("只写风格时 MetaWritten 应为 true")
	}
	if res.LyricEmbed || res.CoverEmbed {
		t.Errorf("不该顺带写歌词/封面: %+v", res)
	}
}

// WriteMetadata 关掉时数字字段一个都不该写（选项闸门要覆盖新字段）。
func TestTidyOneSkipsNumbersWhenMetadataDisabled(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "off.mp3", mp3Fixture())

	opts := DefaultOptions()
	opts.WriteMetadata = false

	res := TidyOne(Item{
		Path: p, Title: "不该写", Genre: "不该写", Year: 2005, Track: 3, Disc: 2,
		Lyric: "[00:01.00]只写歌词",
	}, opts)
	if !res.OK {
		t.Fatalf("TidyOne 失败: %s", res.Error)
	}
	if !res.LyricEmbed {
		t.Error("歌词仍应写入")
	}
	if res.MetaWritten {
		t.Error("WriteMetadata=false 时不该标记为写了元数据")
	}

	got, err := tags.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year != 0 || got.Track != 0 || got.Disc != 0 || got.Genre != "" || got.Title != "" {
		t.Errorf("元数据字段应全部为空，实际 %+v", got)
	}
}

// 只补年份**不能**把已有的标题/歌手/歌词抹掉（tags.Write 的合并语义透传到 tidy）。
//
// 这条是「按字段补全」能上线的前提：用户勾了年份， tidy 只会传 Year，
// 其余字段全靠合并保留。
func TestTidyOneYearOnlyKeepsExistingFields(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "keep.mp3", mp3Fixture())

	full := Item{
		Path: p, Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦", Genre: "Pop 流行",
		Lyric: "[00:01.00]一群嗜血的蚂蚁",
	}
	if res := TidyOne(full, DefaultOptions()); !res.OK {
		t.Fatalf("建立初始标签失败: %s", res.Error)
	}

	// 模拟「这次只勾了年份」：tidy 只会传 Year/Track/Disc
	only := TidyOne(Item{Path: p, Year: 2005, Track: 3}, DefaultOptions())
	if !only.OK {
		t.Fatalf("只补年份失败: %s", only.Error)
	}

	got, err := tags.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "夜曲" || got.Artist != "周杰伦" || got.Album != "十一月的萧邦" {
		t.Errorf("标题/歌手/专辑被抹掉了: %+v", got)
	}
	if got.Genre != "Pop 流行" {
		t.Errorf("Genre 被抹掉了: %q", got.Genre)
	}
	if got.Lyric == "" {
		t.Error("内嵌歌词被抹掉了")
	}
	if got.Year != 2005 || got.Track != 3 {
		t.Errorf("年份/曲序 = %d/%d, 期望 2005/3", got.Year, got.Track)
	}
}
