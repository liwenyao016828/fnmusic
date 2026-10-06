package library

import (
	"testing"
)

// 写不了标签的格式必须单列：它们既不是「待补」也不是「待读」。
//
// 守的是「数字说不清」这个坑：以前 wav/opus 会被算进 any_field（补全会把它们排进队），
// 但写入器只有 MP3/FLAC，补全每轮都会对它们失败一次 —— any_field 永远减不到 0，
// 用户点多少次都看不到收敛，只能以为功能坏了。
func TestFieldGapsSeparatesUnwritableFormats(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/a.mp3", Size: 1, MTime: 1, Format: "mp3"})   // 没读过
	idx.Upsert(Entry{Path: "/m/b.flac", Size: 2, MTime: 2, Format: "flac"}) // 已读且齐全
	idx.Upsert(Entry{Path: "/m/c.wav", Size: 3, MTime: 3, Format: "wav"})
	idx.Upsert(Entry{Path: "/m/d.opus", Size: 4, MTime: 4, Format: "opus"})
	idx.MarkEnriched("/m/b.flac", Enrich{
		Title: "晴天", Artist: "周杰伦", Album: "叶惠美", Genre: "Pop 流行",
		Year: 2003, Track: 2, Disc: 1, HasLyric: true, HasCover: true,
	})

	g := idx.FieldGaps("/m")
	if g.Total != 4 {
		t.Fatalf("Total = %d, 期望 4", g.Total)
	}
	if g.Unsupported != 2 {
		t.Errorf("Unsupported = %d, 期望 2（wav + opus）", g.Unsupported)
	}
	if g.UnsupportedExts["wav"] != 1 || g.UnsupportedExts["opus"] != 1 || len(g.UnsupportedExts) != 2 {
		t.Errorf("UnsupportedExts = %v, 期望 {wav:1, opus:1}", g.UnsupportedExts)
	}
	// 可写的两条照旧：一条已读齐全，一条没读过（算待读、算缺口）
	if g.Read != 1 {
		t.Errorf("Read = %d, 期望 1（只有 b.flac 读成功过）", g.Read)
	}
	if g.AnyField != 1 {
		t.Errorf("AnyField = %d, 期望 1（wav/opus 不算缺口，只有没读过的 a.mp3 算）", g.AnyField)
	}
	if g.Total-g.Read-g.Unsupported != 1 {
		t.Errorf("unread 口径 = %d, 期望 1（接口用它，别把 wav/opus 算成待读）",
			g.Total-g.Read-g.Unsupported)
	}

	// 挑活也挑不出 wav/opus：排进队只会每轮失败一次
	got := idx.NeedingFields("/m", AllFields(), 0)
	if len(got) != 1 || got[0].Path != "/m/a.mp3" {
		t.Errorf("挑出的记录 = %v, 期望只有 /m/a.mp3", pathsOf(got))
	}
}

// 老索引（Format 为空）要按路径扩展名判断，不能因为缺这个字段就整库算「写不了标签」。
func TestFieldGapsUnwritableFallsBackToExtension(t *testing.T) {
	idx := NewIndex(t.TempDir())
	idx.Upsert(Entry{Path: "/m/old.mp3"}) // 没记 Format，可写
	idx.Upsert(Entry{Path: "/m/old.ape"}) // 没记 Format，写不了
	idx.Upsert(Entry{Path: "/m/noext"})   // 连扩展名都没有 → 保守算写不了

	g := idx.FieldGaps("/m")
	if g.Unsupported != 2 {
		t.Errorf("Unsupported = %d, 期望 2（ape + 无扩展名）", g.Unsupported)
	}
	if g.UnsupportedExts["ape"] != 1 || g.UnsupportedExts["未知"] != 1 {
		t.Errorf("UnsupportedExts = %v, 期望 {ape:1, 未知:1}", g.UnsupportedExts)
	}
	if g.AnyField != 1 {
		t.Errorf("AnyField = %d, 期望 1（只有 old.mp3 是能写又没读过的）", g.AnyField)
	}
}
