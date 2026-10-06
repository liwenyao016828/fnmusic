package library

import (
	"testing"
)

// 新增字段必须能被「读进来 → 统计出来 → 挑得出活」走通一遍。
func TestFieldGapsAndNeedingFields(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/full.mp3", Size: 100, MTime: 1})
	idx.MarkEnriched("/m/full.mp3", Enrich{
		Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦", Genre: "Pop 流行",
		Year: 2005, Track: 3, Disc: 1, HasLyric: true, HasCover: true,
	})

	idx.Upsert(Entry{Path: "/m/noyear.flac", Size: 200, MTime: 2})
	idx.MarkEnriched("/m/noyear.flac", Enrich{
		Title: "晴天", Artist: "周杰伦", HasLyric: true, HasCover: true,
	})

	idx.Upsert(Entry{Path: "/m/never-read.mp3", Size: 300, MTime: 3}) // 从没读过标签

	dir := "/m"

	gaps := idx.FieldGaps(dir)
	if gaps.Total != 3 {
		t.Fatalf("Total = %d, 期望 3", gaps.Total)
	}
	// 从未读过标签的两条只算「待读」，不计进具体字段缺口
	if gaps.Read != 2 {
		t.Errorf("Read = %d, 期望 2（第三条从未读标签）", gaps.Read)
	}
	if gaps.Year != 1 || gaps.Genre != 1 || gaps.Track != 1 || gaps.Disc != 1 {
		t.Errorf("新字段缺口 = year:%d genre:%d track:%d disc:%d, 期望全 1",
			gaps.Year, gaps.Genre, gaps.Track, gaps.Disc)
	}
	if gaps.Lyric != 0 || gaps.Cover != 0 {
		t.Errorf("歌词/封面缺口 = %d/%d, 期望 0（两条已读记录都有）", gaps.Lyric, gaps.Cover)
	}
	if gaps.AnyField != 2 {
		t.Errorf("AnyField = %d, 期望 2（noyear 缺新字段 + never-read 一切未知）", gaps.AnyField)
	}

	// 只勾年份：never-read 也算缺（不确定就算缺，宁可多排一次活）
	yearOnly := idx.NeedingFields(dir, Fields{Year: true}, 0)
	if len(yearOnly) != 2 {
		t.Fatalf("只勾年份挑出 %d 条, 期望 2", len(yearOnly))
	}
	if yearOnly[0].Path != "/m/never-read.mp3" || yearOnly[1].Path != "/m/noyear.flac" {
		t.Errorf("挑出的记录 = %v", pathsOf(yearOnly))
	}

	// 只勾歌词：两条已读的都有歌词，只剩「没读过」的那条
	lyricOnly := idx.NeedingFields(dir, Fields{Lyric: true}, 0)
	if len(lyricOnly) != 1 || lyricOnly[0].Path != "/m/never-read.mp3" {
		t.Errorf("只勾歌词挑出 %v, 期望只有 never-read", pathsOf(lyricOnly))
	}

	// 一个都不勾 ⇒ 不挑活（否则会整库空跑）
	if got := idx.NeedingFields(dir, Fields{}, 0); len(got) != 0 {
		t.Errorf("未勾选任何字段却挑出 %d 条", len(got))
	}
}

// TagReadAt 是「读没读过」的权威标记：MarkEnriched 打上、InvalidateTagRead 清掉。
//
// 守的是两件事：
// 1. 老索引（字段为 0）会被当成「没读过」自动回读一遍，新增字段才有值；
// 2. 补全撤销后必须能清掉标记，否则索引里的年份/歌词标记会永久偏离文件真实内容。
func TestTagReadMarkers(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 1, MTime: 1, Title: "旧标题", Artist: "旧歌手"})

	un := idx.Unenriched("/m", 10)
	if len(un) != 1 {
		t.Fatal("有标题但从未读过年份/风格的老记录应进入待读队列（否则新字段永远补不上）")
	}

	idx.MarkEnriched("/m/a.mp3", Enrich{Title: "旧标题", Artist: "旧歌手", Year: 2005})
	if len(idx.Unenriched("/m", 10)) != 0 {
		t.Error("读过之后不该再进待读队列（每轮扫描都白读一遍）")
	}
	e, _ := idx.Get("/m/a.mp3")
	if e.TagReadAt == 0 {
		t.Fatal("MarkEnriched 没打 TagReadAt")
	}
	if e.Year != 2005 {
		t.Errorf("Year = %d, 期望 2005", e.Year)
	}

	// 读失败也要打点：否则坏文件每轮都被重读
	idx.MarkTagRead("/m/b.mp3")

	idx.InvalidateTagRead("/m/a.mp3")
	e, _ = idx.Get("/m/a.mp3")
	if e.TagReadAt != 0 {
		t.Error("InvalidateTagRead 应清掉已读标记")
	}
	if e.Year != 2005 || e.Title != "旧标题" {
		t.Errorf("清标记不该动字段值，实际 %+v", e)
	}
}

// 持久化往返：新字段与 TagReadAt 必须存得下、读得回（索引是补全的唯一挑活依据）
func TestEntryNewFieldsPersist(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex(dir)
	idx.Upsert(Entry{Path: "/m/a.flac", Size: 10, MTime: 1})
	idx.MarkEnriched("/m/a.flac", Enrich{
		Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦", Genre: "Pop 流行",
		Year: 2005, Track: 3, Disc: 2, HasLyric: true,
	})
	if err := idx.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded := NewIndex(dir)
	e, ok := reloaded.Get("/m/a.flac")
	if !ok {
		t.Fatal("重新加载后查不到记录")
	}
	if e.Genre != "Pop 流行" || e.Year != 2005 || e.Track != 3 || e.Disc != 2 {
		t.Errorf("新字段未持久化: %+v", e)
	}
	if e.TagReadAt == 0 {
		t.Error("TagReadAt 未持久化：重启后会把全库当成没读过，白白重扫一遍")
	}
	if !e.HasLyric || e.HasCover {
		t.Errorf("HasLyric/HasCover = %v/%v, 期望 true/false", e.HasLyric, e.HasCover)
	}
}

// NeedingTidy 是老的「歌词+封面」口径，改造成 NeedingFields 的语法糖后行为不能变。
func TestNeedingTidyKeepsOldSemantics(t *testing.T) {
	idx := NewIndex(t.TempDir())
	for _, p := range []string{"/m/a.mp3", "/m/b.mp3", "/m/c.mp3"} {
		idx.Upsert(Entry{Path: p, Size: 1, MTime: 1})
	}
	idx.MarkEnriched("/m/a.mp3", Enrich{Title: "a", HasLyric: true, HasCover: true, Genre: "x", Year: 2001})
	idx.MarkEnriched("/m/b.mp3", Enrich{Title: "b", HasLyric: true})
	idx.MarkEnriched("/m/c.mp3", Enrich{Title: "c", HasCover: true})

	got := pathsOf(idx.NeedingTidy("/m", 0))
	// a 词+封齐全 ⇒ 不挑（它缺年份/风格，但老口径不该管）；b 缺封面；c 缺歌词
	if len(got) != 2 || got[0] != "/m/b.mp3" || got[1] != "/m/c.mp3" {
		t.Errorf("NeedingTidy = %v, 期望 [/m/b.mp3 /m/c.mp3]（a 已有词+封面，只缺年份/风格不该被挑出来）", got)
	}
}

func pathsOf(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}
