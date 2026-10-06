package complete

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

func TestLooksLikeFilename(t *testing.T) {
	cases := []struct {
		title, base string
		want        bool
		why         string
	}{
		{"02 晴天.mp3", "02 晴天.mp3", true, "整个就是文件名"},
		{"周杰伦 - 夜曲.flac", "周杰伦 - 夜曲.flac", true, "带音频扩展名"},
		{"01_周杰伦_夜曲", "01_周杰伦_夜曲.mp3", true, "下划线写法归一化后与文件名相同"},
		{"夜曲", "01 周杰伦 - 夜曲.mp3", false, "正常歌名不该被误判"},
		{"晴天", "02 晴天.mp3", false, "去掉序号后的正常歌名"},
		{"", "02 晴天.mp3", false, "空的交给上游处理"},
		{"Chrome", "Chrome.mp3", true, "与文件主体同名时判不出来 —— 但退回规则解析拿到的也是同一个词，不亏"},
	}
	for _, c := range cases {
		if got := looksLikeFilename(c.title, c.base); got != c.want {
			t.Errorf("looksLikeFilename(%q, %q) = %v, 期望 %v（%s）", c.title, c.base, got, c.want, c.why)
		}
	}
}

// 脏标题要退回按文件名判名，但歌手要留着 —— 它通常是好的，扔掉反而更难搜。
func TestResolveNameDiscardsFilenameAsTitle(t *testing.T) {
	title, artist := resolveName(context.Background(), Options{},
		"/m/01 周杰伦 - 夜曲.mp3", "01 周杰伦 - 夜曲.mp3", "周杰伦")

	if strings.Contains(title, ".mp3") {
		t.Errorf("标题仍然带着扩展名：%q", title)
	}
	if !strings.Contains(title, "夜曲") {
		t.Errorf("该从文件名认出「夜曲」，实际 %q", title)
	}
	if artist != "周杰伦" {
		t.Errorf("歌手应保持 %q，实际 %q", "周杰伦", artist)
	}
}

// 索引里标题歌手都正常时，走的是快路径，不许被这次改动多绕一步。
func TestResolveNameKeepsGoodIndexTitle(t *testing.T) {
	title, artist := resolveName(context.Background(), Options{},
		"/m/01 周杰伦 - 夜曲.mp3", "夜曲", "周杰伦")
	if title != "夜曲" || artist != "周杰伦" {
		t.Errorf("正常标签应保持原值，实际 %q / %q", title, artist)
	}
}

// 端到端：脏标题修复后，搜索词里不该再有扩展名，并能正常匹配上。
func TestResolveSearchesCleanKeyword(t *testing.T) {
	var calls []string
	var keywords []string
	orig := searchFn
	searchFn = func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		keywords = append(keywords, keyword)
		calls = append(calls, platform)
		return []search.UnifiedSong{{
			Name: "夜曲", Singer: "周杰伦", Source: platform,
			Songmid: "S1", ID: "S1", Duration: 226, Album: "十一月的萧邦", Year: 2005, Track: 1,
		}}
	}
	t.Cleanup(func() { searchFn = orig })

	plan := Resolve(context.Background(), []Target{{
		Path: "/m/01 周杰伦 - 夜曲.mp3", Title: "01 周杰伦 - 夜曲.mp3", Artist: "周杰伦",
		NeedLyric: true, NeedYear: true,
	}}, Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if !it.Matched {
		t.Fatalf("修好标题后应该能匹配上，实际 note=%q", it.Note)
	}
	for _, k := range keywords {
		if strings.Contains(k, ".mp3") {
			t.Errorf("搜索词里还有扩展名：%q", k)
		}
	}
	if it.FillYear != 2005 {
		t.Errorf("年份没补上：%+v", it)
	}
}
