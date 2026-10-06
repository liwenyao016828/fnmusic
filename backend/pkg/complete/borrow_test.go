package complete

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

// stubBorrow 按「平台 + 关键词」给候选，并把每次调用的 `平台|关键词` 记下来。
//
// 关键词也要记：借的时候必须用**兜底命中自己写的曲名歌手**去主力平台搜
// （本地解析出来的名字可能就是搜不到的那个），这点只能从搜索词上验证。
func stubBorrow(t *testing.T, byPlatform map[string][]search.UnifiedSong) *[]string {
	t.Helper()
	var calls []string
	origSearch := searchFn
	searchFn = func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		calls = append(calls, platform+"|"+keyword)
		return byPlatform[platform]
	}
	t.Cleanup(func() { searchFn = origSearch })

	origMeta := fetchAlbumMeta
	fetchAlbumMeta = func(albumMID string) (search.QQAlbumMeta, bool) {
		if albumMID == "" {
			return search.QQAlbumMeta{}, false
		}
		return search.QQAlbumMeta{Genre: "Pop 流行", Year: 2003}, true
	}
	t.Cleanup(func() { fetchAlbumMeta = origMeta })
	return &calls
}

// 兜底平台命中、字段给不出 → 回主力平台借元数据。
//
// 要守住两条：借来的只有元数据；音频侧的标识一个都不换
// （换了就变成「标签是 QQ 的、歌词封面是酷狗的」，比空着更糟）。
func TestBorrowMetadataFromPreferredPlatform(t *testing.T) {
	calls := stubBorrow(t, map[string][]search.UnifiedSong{
		// 酷狗：能对上，但响应里没有年份/曲序/专辑标识（歌手写法比本地多了一段）
		"kg": {{Name: "夜曲", Singer: "周杰伦 feat. 杨瑞代", Source: "kg", Songmid: "KG1", ID: "KG1",
			Duration: 226, Cover: "https://kg/1.jpg"}},
		// QQ：只有按酷狗那串的写法才搜得到（本地写法搜不到同名同歌手的那条）
		"tx": {{Name: "夜曲", Singer: "周杰伦 feat. 杨瑞代", Source: "tx", Songmid: "TX1", ID: "TX1",
			Duration: 226, AlbumMID: "AM1", Year: 2005, Track: 1, Disc: 1}},
	})

	plan := Resolve(context.Background(), []Target{{
		Path: "/m/夜曲.mp3", Title: "夜曲", Artist: "周杰伦",
		NeedLyric: true, NeedCover: true, NeedYear: true, NeedTrack: true, NeedDisc: true, NeedGenre: true,
	}}, Options{Platforms: []string{"kg"}, PreferredPlatforms: []string{"tx"}}, nil)

	it := plan.Items[0]
	if !it.Matched || it.MatchedBy != "kg" {
		t.Fatalf("应先命中酷狗，实际 %+v（calls=%v）", it, *calls)
	}
	if it.MetadataFrom != "tx" {
		t.Errorf("应记下元数据借自 tx，实际 %q", it.MetadataFrom)
	}
	if it.FillYear != 2005 || it.FillTrack != 1 || it.FillDisc != 1 || it.FillGenre != "Pop 流行" {
		t.Errorf("四个字段没借到：%+v", it)
	}
	if it.MatchedName != "夜曲" {
		t.Errorf("曲名不该被借动：%q", it.MatchedName)
	}
	var sawBorrowCall bool
	for _, c := range *calls {
		if strings.HasPrefix(c, "tx|") {
			sawBorrowCall = true
			// 用的是**兜底命中自己写的**曲名歌手（带 feat.），不是本地解析出来那串
			if !strings.Contains(c, "夜曲 周杰伦 feat. 杨瑞代") {
				t.Errorf("借的时候关键词不对，实际 %q", c)
			}
		}
	}
	if !sawBorrowCall {
		t.Errorf("根本没去主力平台找，calls=%v", *calls)
	}
}

// 时长差太多说明不是同一个录音版本（同名同歌手的现场版/串烧版），宁可空着也不借。
func TestBorrowSkippedWhenDurationsDiffer(t *testing.T) {
	calls := stubBorrow(t, map[string][]search.UnifiedSong{
		"kg": {{Name: "夜曲", Singer: "周杰伦", Source: "kg", Songmid: "K", ID: "K", Duration: 226}},
		// 390 秒 vs 226 秒：差 73%，明显另一个版本
		"tx": {{Name: "夜曲", Singer: "周杰伦", Source: "tx", Songmid: "T", ID: "T",
			Duration: 390, AlbumMID: "AM1", Year: 2006, Track: 9}},
	})

	plan := Resolve(context.Background(), []Target{{
		Path: "/m/夜曲.mp3", Title: "夜曲", Artist: "周杰伦", NeedYear: true, NeedTrack: true,
	}}, Options{Platforms: []string{"kg"}, PreferredPlatforms: []string{"tx"}}, nil)

	it := plan.Items[0]
	if it.FillYear != 0 || it.FillTrack != 0 {
		t.Errorf("时长差这么多还借了，sameRecording 失效：%+v", it)
	}
	if it.MetadataFrom != "" {
		t.Errorf("不该记下借自 %q", it.MetadataFrom)
	}
	if !strings.Contains(it.Note, "年份") {
		t.Errorf("没借到要在 note 里说明，实际 %q", it.Note)
	}
	if len(*calls) < 2 {
		t.Errorf("确实搜过主力平台才对，calls=%v", *calls)
	}
}

// 没勾「只有部分平台才给」的字段时，一条都不许多搜 —— 那是白花外网请求。
func TestNoBorrowWhenNothingRequested(t *testing.T) {
	calls := stubBorrow(t, map[string][]search.UnifiedSong{
		"kg": {{Name: "夜曲", Singer: "周杰伦", Source: "kg", Songmid: "K", ID: "K", Duration: 226, Year: 2005}},
	})

	plan := Resolve(context.Background(), []Target{{
		Path: "/m/夜曲.mp3", Title: "夜曲", Artist: "周杰伦", NeedLyric: true, NeedCover: true,
	}}, Options{Platforms: []string{"kg"}, PreferredPlatforms: []string{"tx"}}, nil)

	it := plan.Items[0]
	if !it.Matched {
		t.Fatalf("该命中，实际 %q", it.Note)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "tx|") {
			t.Errorf("没勾元数据字段却去搜了主力平台：%v", *calls)
		}
	}
	if it.MetadataFrom != "" || it.FillYear != 0 || it.FillGenre != "" {
		t.Errorf("未勾选的字段不该被写入：%+v", it)
	}
}

// 主力平台自己也搜不到时，静默不借，但不能影响歌词/封面照常补。
func TestBorrowNoopWhenPreferredPlatformHasNoMatch(t *testing.T) {
	calls := stubBorrow(t, map[string][]search.UnifiedSong{
		"kg": {{Name: "夜曲", Singer: "周杰伦", Source: "kg", Songmid: "K", ID: "K", Duration: 226}},
		"tx": nil,
	})

	plan := Resolve(context.Background(), []Target{{
		Path: "/m/夜曲.mp3", Title: "夜曲", Artist: "周杰伦", NeedYear: true,
	}}, Options{Platforms: []string{"kg"}, PreferredPlatforms: []string{"tx"}}, nil)

	it := plan.Items[0]
	if !it.Matched {
		t.Fatalf("兜底命中仍然算匹配成功（歌词封面照补），实际 %q", it.Note)
	}
	if it.FillYear != 0 || it.MetadataFrom != "" {
		t.Errorf("没借到却记了值：%+v", it)
	}
	if !strings.Contains(it.Note, "年份") {
		t.Errorf("要说明年份没补上，实际 %q", it.Note)
	}
	if len(*calls) < 2 {
		t.Errorf("该真的去主力平台找过一次，calls=%v", *calls)
	}
}
