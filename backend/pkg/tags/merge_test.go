package tags

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// 只写一部分字段时，**没写的字段必须保留**。
//
// 这是补全功能的前置安全条件：AI 补全会按字段分别勾选（这次只补年份、下次只换封面），
// 而两种容器的写入策略都是「删掉受管字段，只按传入值重建」。
// 不合并的话每一次局部写入都是一次数据清理 —— 尤其 v2.1.23 的手动换封面
// 就是「只传 path + cover」，会把标题/歌手/专辑/风格/歌词一起清空。
func TestWriteMergesIntoExistingMetadata(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		fixture  func() []byte
	}{
		{name: "MP3", fileName: "merge.mp3", fixture: mp3Fixture},
		{name: "FLAC", fileName: "merge.flac", fixture: flacFixture},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.fileName, tt.fixture())

			full := Metadata{
				Title:  "夜曲",
				Artist: "周杰伦",
				Album:  "十一月的萧邦",
				Genre:  "流行",
				Lyric:  "[00:01.00]词曲：周杰伦",
				Year:   2005,
				Track:  3,
				Disc:   1,
			}
			if _, err := Write(path, full); err != nil {
				t.Fatalf("建立初始标签: %v", err)
			}

			// 只传封面 —— 模拟「手动换封面」这条最危险的路径
			if _, err := Write(path, Metadata{Cover: jpegFixture(), CoverMime: "image/jpeg"}); err != nil {
				t.Fatalf("只写封面失败: %v", err)
			}

			got, err := Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got.Cover) == 0 {
				t.Error("封面没写进去")
			}
			for _, f := range []struct {
				key, want, got string
			}{
				{"Title", full.Title, got.Title},
				{"Artist", full.Artist, got.Artist},
				{"Album", full.Album, got.Album},
				{"Genre", full.Genre, got.Genre},
				{"Lyric", full.Lyric, got.Lyric},
			} {
				if strings.TrimSpace(f.got) == "" {
					t.Errorf("%s 被抹掉了：只写封面不该动文本字段", f.key)
					continue
				}
				if !strings.Contains(f.got, f.want) {
					t.Errorf("%s = %q, 期望保留 %q", f.key, f.got, f.want)
				}
			}
			if got.Year != 2005 || got.Track != 3 || got.Disc != 1 {
				t.Errorf("数字字段被抹掉了: year=%d track=%d disc=%d, 期望 2005/3/1", got.Year, got.Track, got.Disc)
			}
		})
	}
}

// 合并不能变成「传了也不生效」：给了值就要覆盖旧值。
func TestWriteProvidedFieldsWinOverExisting(t *testing.T) {
	path := writeTemp(t, "override.mp3", mp3Fixture())

	if _, err := Write(path, Metadata{Title: "旧标题", Artist: "旧歌手", Year: 1999}); err != nil {
		t.Fatalf("初始写入: %v", err)
	}
	if _, err := Write(path, Metadata{Title: "新标题"}); err != nil {
		t.Fatalf("第二次写入: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Title != "新标题" {
		t.Errorf("Title = %q, 期望传入值覆盖旧值", got.Title)
	}
	if got.Artist != "旧歌手" {
		t.Errorf("Artist = %q, 期望未传的保留旧值", got.Artist)
	}
	if got.Year != 1999 {
		t.Errorf("Year = %d, 期望未传的保留旧值", got.Year)
	}
}

// 只补年份时不该碰歌词；MP3 的封面另有保留逻辑，这里确认它没被合并流程干扰。
func TestWriteYearOnlyKeepsLyricAndCover(t *testing.T) {
	cover := jpegFixture()
	existing := buildID3v2Tag(Metadata{Title: "x", Lyric: "[00:02.00]保留我", Cover: cover, CoverMime: "image/jpeg"})
	path := writeTemp(t, "year-only.mp3", append(existing, mp3Fixture()...))

	if _, err := Write(path, Metadata{Year: 2003}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, cover) {
		t.Error("只写年份把封面弄丢了")
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(got.Lyric, "保留我") {
		t.Errorf("Lyric = %q, 期望保留既有歌词", got.Lyric)
	}
	if got.Year != 2003 {
		t.Errorf("Year = %d, 期望 2003", got.Year)
	}
}
