package complete

import (
	"context"
	"os"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

// 联网测试：验证「搜索 → 匹配 → 取歌词」这条**外部依赖链**真的通。
//
// 默认跳过（与 pkg/push 的联网测试同一约定）：
//
//	LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live -v
//
// 为什么要单独测：单元测试覆盖不到「平台接口改版 / 结果被降级导致搜不到」这类问题 ——
// 而那恰恰是补全功能最容易悄悄坏掉的地方（界面上只表现为「一首都没补上」）。
//
// ⚠️ 实测结论（2026-09-18）：**各平台质量差异极大**，所以「单平台搜不到」不等于功能坏了 ——
// 必须按**真实流程**（多平台轮询）测：
//
//	搜「周杰伦 晴天」第一条：
//	  网易云 → 晴天(原唱 周杰伦) | RyaVocal   ← 翻唱，匹配不上
//	  QQ     → 晴天 | 周杰伦                    ← 正确
//	  酷狗   → 晴天 | 周杰伦                    ← 正确
//	  酷我   → 晴天（周杰伦） | 青崖            ← 翻唱
//
// 网易云与酷我对**匿名请求**返回的结果明显被降级（原版不在候选里）。
// 补全靠多平台轮询绕过了这一点，所以下面的用例断言的是「**至少一个平台能命中**」。
func TestSearchMatchLyricLive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") != "1" {
		t.Skip("需要联网：LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live -v")
	}

	var hit search.UnifiedSong
	var hitPlatform string

	for _, p := range DefaultPlatforms {
		cands := search.Search("周杰伦 晴天", p, 1, 10)
		t.Logf("[%s] 返回 %d 条候选", p, len(cands))
		if len(cands) == 0 {
			continue
		}
		s, ok := search.MatchByNameSinger(cands, "晴天", "周杰伦")
		if ok {
			hit, hitPlatform = s, p
			t.Logf("[%s] ✓ 命中：%s - %s（songmid=%s cover=%v）", p, s.Name, s.Singer, s.Songmid, s.Cover != "")
			break
		}
		t.Logf("[%s] ✗ 没命中（首条：%s - %s）", p, cands[0].Name, cands[0].Singer)
	}

	if hitPlatform == "" {
		t.Fatalf("四个平台都没能匹配到「晴天 - 周杰伦」—— 补全会整体失效，需要排查")
	}
	if hit.Songmid == "" && hit.ID == "" {
		t.Fatalf("匹配到的条目没有 id/songmid，后续取歌词会失败：%+v", hit)
	}

	// 取歌词（内部会按歌名+歌手去网易云找，与命中的平台无关）
	lrc, _ := search.FetchLyric(hit.Source, hit.Songmid, hit.Name, hit.Singer, hit.Duration, hit.Hash)
	if lrc == "" {
		t.Fatal("歌词为空 —— 补全拿不到内容，写标签也就没意义")
	}
	if !strings.Contains(lrc, "[") {
		t.Errorf("歌词不像 LRC 格式（应含时间轴）：%.80q", lrc)
	}
	t.Logf("歌词长度 %d 字符，开头：%.60q", len([]rune(lrc)), lrc)
}

// 匹配要能拒绝明显不是同一首的候选（防止把别的歌的歌词写进去）
func TestSearchMatchRejectsWrongSongLive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") != "1" {
		t.Skip("需要联网：LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live -v")
	}

	for _, p := range DefaultPlatforms {
		cands := search.Search("周杰伦 晴天", p, 1, 10)
		if len(cands) == 0 {
			continue
		}
		// 拿一个完全不同的歌名去匹配，必须匹配不上 ——
		// 否则「搜到什么就写什么」，会把错误歌词写进用户文件
		if s, ok := search.MatchByNameSinger(cands, "完全不存在的歌名XYZ", "周杰伦"); ok {
			t.Errorf("[%s] 不该匹配到不存在的歌名，却命中了：%s", p, s.Name)
		}
	}
}

// 字段只来自「匹配上的那条命中」，缺了必须有说明；有更全的命中时不许退回缺的。
//
// 跑法：LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live -v
//
// ⚠️ 这条用例**不断言一定补到年份/风格** —— 那是错的写法（我第一版就那么写，
// 结果平台一降级就红）：匿名搜索下各平台返回的内容会变，网易/酷我常被降级成翻唱，
// QQ 有时根本匹配不上。真实契约只有两条，与哪个平台今天肯给脸无关：
//
//	① 值只在匹配上的那条里取，取不到就跳过并写明原因（宁缺毋滥）；
//	② 若某个平台确实给出了能匹配且带字段的候选，就不许 settled 在信息更少的命中上。
//
// 另外守住实测发现的老坑：翻唱版的年份（2025）绝不能当原版写进去。
func TestResolveFillsMetadataFromHitLive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") != "1" {
		t.Skip("需要联网：LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live -v")
	}
	const title, artist = "晴天", "周杰伦"

	plan := Resolve(context.Background(), []Target{{
		Path: "/vol1/Music/周杰伦 - 晴天.mp3", Title: title, Artist: artist,
		NeedYear: true, NeedTrack: true, NeedGenre: true,
	}}, Options{}, nil)

	if len(plan.Items) != 1 {
		t.Fatalf("计划条数 = %d", len(plan.Items))
	}
	it := plan.Items[0]
	t.Logf("命中平台=%s 曲名=%q 年份=%d 曲序=%d 风格=%q 备注=%q",
		it.MatchedBy, it.MatchedName, it.FillYear, it.FillTrack, it.FillGenre, it.Note)

	if !it.Matched {
		t.Fatalf("四个平台一条都没匹配上 —— 补全会整体失效：%s", it.Note)
	}

	// 契约 ①：取不到的字段必须逐个说明，不能默默空着
	if it.FillYear <= 0 && !strings.Contains(it.Note, "年份") {
		t.Errorf("没补到年份却没说明原因：%q", it.Note)
	}
	if it.FillGenre == "" && !strings.Contains(it.Note, "风格") {
		t.Errorf("没补到风格却没说明原因：%q", it.Note)
	}
	// 翻唱版年份（实测 2025）不能当原版写进去
	if it.FillYear > 0 && (it.FillYear < 2000 || it.FillYear > 2010) {
		t.Errorf("年份 = %d，不在 2000-2010 区间 —— 很可能取到了翻唱版", it.FillYear)
	}

	// 契约 ②：自己再扫一遍四家，看有没有「能匹配且字段更全」的候选
	bestCovers, bestPlatform := 0, ""
	for _, p := range DefaultPlatforms {
		cands := search.Search(title+" "+artist, p, 1, 10)
		s, ok := search.MatchByNameSinger(cands, title, artist)
		if !ok {
			continue
		}
		n := 0
		if s.Year > 0 {
			n++
		}
		if s.Track > 0 {
			n++
		}
		if s.AlbumMID != "" {
			n++
		}
		t.Logf("[%s] 可匹配，能提供 %d 个字段（年份=%d 曲序=%d 专辑mid=%v）", p, n, s.Year, s.Track, s.AlbumMID != "")
		if n > bestCovers {
			bestCovers, bestPlatform = n, p
		}
	}

	got := 0
	if it.FillYear > 0 {
		got++
	}
	if it.FillTrack > 0 {
		got++
	}
	if it.FillGenre != "" {
		got++
	}
	// 有平台能匹配却带齐字段（例如 QQ），结果却选了个字段更少的命中 → 偏好没生效
	if bestCovers > got {
		t.Errorf("「%s」能匹配且可给 %d 个字段，实际只从 %s 拿到 %d 个 —— 该换到信息更全的命中",
			bestPlatform, bestCovers, it.MatchedBy, got)
	}
}
