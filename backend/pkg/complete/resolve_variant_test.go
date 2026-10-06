package complete

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

// ── 「别写错版本」护栏：试听片段与改编版 ──
//
// 起因：`MatchByNameSinger` 只看曲名 + 歌手，而归一化会把括号内容整段去掉，
// 于是 `晴天` 与 `晴天 (Live)` 被判成同一首 —— 平台把 Live 版排在前面时，
// 它的年份/曲序就被写进了原唱文件。下面这些用例守的就是这条。

func cand(name, mid string, dur, year, track int) search.UnifiedSong {
	return search.UnifiedSong{
		Name: name, Singer: "周杰伦", Source: "wy",
		Songmid: mid, ID: mid, Duration: dur, Album: "叶惠美",
		Year: year, Track: track,
	}
}

func needYearTarget(path string) Target {
	return Target{Path: path, Title: "晴天", Artist: "周杰伦", NeedYear: true}
}

// 正式版文件 + 平台把 `晴天 (Live)` 排在正式版前面 → 必须挑正式版那条。
func TestResolveSkipsVariantWhenFormalAvailable(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {
			cand("晴天 (Live)", "L1", 390, 2019, 1), // 平台排前面，归一化后曲名与「晴天」相同
			cand("晴天", "W1", 269, 2003, 3),
		},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天.mp3")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if !it.Matched {
		t.Fatalf("应命中正式版，实际未匹配（note=%s）", it.Note)
	}
	if it.FillYear != 2003 {
		t.Errorf("年份应来自正式版 2003，实际 %d（说明挑到了 Live 版）", it.FillYear)
	}
}

// 只有改编版可用时不写 —— 宁可空着，也别把 Live 版的年份/曲序写进原版。
func TestResolveSkipsVariantOnlyMatch(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天 (Live)", "L1", 390, 2019, 1)},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天.mp3")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if it.Matched {
		t.Fatal("只有改编版可用时不该判为已匹配")
	}
	if it.FillYear != 0 || it.FillTrack != 0 {
		t.Errorf("跳过了却带了要写的值：%+v", it)
	}
	if !strings.Contains(it.Note, "改编版") {
		t.Errorf("要说明为什么跳过（改编版），实际 note=%q", it.Note)
	}
}

// 文件本身就是 Live 版时，改编版候选才是对的那条。
func TestResolveAcceptsVariantWhenFileIsVariant(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天 (Live)", "L1", 390, 2019, 1)},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天 (Live).flac")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if !it.Matched || it.FillYear != 2019 {
		t.Fatalf("文件名带 (Live) 时应接受改编版，实际 matched=%v year=%d note=%s",
			it.Matched, it.FillYear, it.Note)
	}
}

// 反向：文件是 Live 版、平台只有正式版 → 同样跳过（正式版的曲序属于录音室专辑）。
func TestResolveSkipsFormalWhenFileIsVariant(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天", "W1", 269, 2003, 3)},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天 (Live).flac")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if it.Matched {
		t.Fatal("文件是 Live 版、只有正式版候选时不该判为已匹配")
	}
	if !strings.Contains(it.Note, "改编版") {
		t.Errorf("要说明跳过原因，实际 note=%q", it.Note)
	}
}

// 试听片段（≤75 秒）不是「同一首的另一版本」：它带的年份/专辑是另一张碟的。
func TestResolveSkipsPreviewSnippet(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天", "P1", 60, 2003, 3)},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天.mp3")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if it.Matched {
		t.Fatal("试听片段不该判为已匹配")
	}
	if !strings.Contains(it.Note, "试听") {
		t.Errorf("要说明跳过原因是试听片段，实际 note=%q", it.Note)
	}
}

// 跳过改编版后**不能**再请 AI 兜底 —— 否则 AI 会照样挑中那条 Live 版，
// 护栏等于没有。这里用「AI 服务一次都不该被调用」来守。
func TestResolveDoesNotAskAIAfterVariantSkip(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天 (Live)", "L1", 390, 2019, 1)},
	})

	aiCalled := 0
	srv := mockAIServer(t, func(string) any {
		aiCalled++
		return 1
	})
	defer srv.Close()

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天.mp3")},
		Options{
			Platforms:  []string{"wy"},
			AIClient:   newTestAIClient(t, srv.URL),
			UseAIMatch: true,
		}, nil)

	it := plan.Items[0]
	if it.Matched {
		t.Fatal("只有 Live 版候选时不该判为已匹配")
	}
	if aiCalled != 0 {
		t.Errorf("有意跳过改编版后不该请 AI 兜底，实际调用了 %d 次", aiCalled)
	}
}

// 平台给出 `晴天 (原唱 周杰伦)` 这种**非版本**后缀时不该被误杀
// （括号里不是版本标记，仍算正式版）。
func TestResolveKeepsNonVariantBracketSuffix(t *testing.T) {
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{
		"wy": {cand("晴天 (原唱 周杰伦)", "W1", 269, 2003, 3)},
	})

	plan := Resolve(context.Background(), []Target{needYearTarget("/m/周杰伦 - 晴天.mp3")},
		Options{Platforms: []string{"wy"}}, nil)

	it := plan.Items[0]
	if !it.Matched || it.FillYear != 2003 {
		t.Fatalf("括号内不是版本标记，应正常命中，实际 matched=%v year=%d note=%s",
			it.Matched, it.FillYear, it.Note)
	}
}
