package complete

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

// ── 模糊档（2026-10-29）──
//
// 用户原话：「一个个搜全名感觉很慢，搞模糊搜索」。
// 真机上另一类失败是：平台明明搜到了，但曲名不是「归一化后完全相等」——
// 文件名有个错字、简繁不同、或平台把「原唱」挂在标题后面。这类候选以前整批被
// `MatchByNameSinger` 判掉，用户看到的是「搜到了但配不上」。
//
// 下面的用例守三件事：
//  ① 差一个字的同一首歌要被配上（并且 Note 里要有「模糊匹配」的牌子）；
//  ② 版本护栏在模糊档同样生效（Live 版的元数据不能写进原版）；
//  ③ 差得远的（《晴天》配《雨天》）仍然不能配。

func fuzzyHit(t *testing.T, cands []search.UnifiedSong, title, artist, fileName string) (PlanItem, bool) {
	t.Helper()
	var calls []string
	stubPlatforms(t, &calls, map[string][]search.UnifiedSong{"wy": cands})
	plan := Resolve(context.Background(),
		[]Target{{Path: fileName, Title: title, Artist: artist, NeedYear: true}},
		Options{Platforms: []string{"wy"}}, nil)
	return plan.Items[0], plan.Items[0].Matched
}

// 文件名有个错字（青花磁 / 青花瓷）—— 严格档会整批拒掉，模糊档要接住。
func TestResolveFuzzyCatchesTypo(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("青花磁", "W1", 239, 2008, 5),
	}, "青花瓷", "周杰伦", "/m/周杰伦 - 青花瓷.mp3")
	if !ok {
		t.Fatalf("差一个字的同一首歌应被模糊档配上，实际未匹配（note=%q）", it.Note)
	}
	if it.FillYear != 2008 {
		t.Errorf("年份应来自命中曲目 2008，实际 %d", it.FillYear)
	}
	if !strings.Contains(it.Note, "模糊匹配") {
		t.Errorf("模糊命中必须在 Note 里挂牌（用户要能分辨「精确」和「猜的」），实际 note=%q", it.Note)
	}
	if !strings.Contains(it.Note, "核对") {
		t.Errorf("模糊命中的说明要请用户核对，实际 note=%q", it.Note)
	}
}

// 繁简差异（後來 / 后来）—— 折叠后完全相等，属于安全命中。
func TestResolveFuzzyCatchesTraditional(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("後來", "W2", 268, 2016, 1),
	}, "后来", "周杰伦", "/m/周杰伦 - 后来.mp3")
	if !ok {
		t.Fatalf("繁简不同应被模糊档配上，实际未匹配（note=%q）", it.Note)
	}
	if !strings.Contains(it.Note, "模糊匹配") {
		t.Errorf("繁简命中也是模糊命中，必须挂牌，实际 note=%q", it.Note)
	}
}

// 版本护栏在模糊档同样生效：只有 Live 版时不许配（否则会把 Live 的年份写进原版）。
func TestResolveFuzzyStillRejectsVariant(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("青花磁 (Live)", "L1", 300, 2019, 1),
	}, "青花瓷", "周杰伦", "/m/周杰伦 - 青花瓷.mp3")
	if ok {
		t.Errorf("文件名是原版时不能配 Live 版（即使歌名只差一个字），实际配上了：%+v", it)
	}
}

// 差得远的绝不能配：两字歌名只差一个字就是另一首歌。
func TestResolveFuzzyRejectsDifferentSong(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("雨天", "W3", 250, 2005, 1),
	}, "晴天", "周杰伦", "/m/周杰伦 - 晴天.mp3")
	if ok {
		t.Errorf("《晴天》不该被配成《雨天》，实际配上了：note=%q", it.Note)
	}
	if !strings.Contains(it.Note, "没有搜到匹配的曲目") {
		t.Errorf("拒配时应给出明确结论，实际 note=%q", it.Note)
	}
}

// 试听片段在模糊档同样不要（和严格档同一条护栏）。
func TestResolveFuzzyRejectsPreview(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("青花磁", "P1", 30, 2008, 5),
	}, "青花瓷", "周杰伦", "/m/周杰伦 - 青花瓷.mp3")
	if ok {
		t.Errorf("30 秒的试听片段不该被配成整首，实际配上了：%+v", it)
	}
}

// 精确命中不该被模糊档「顺手挂牌」—— 严格档走通时 Note 里不能出现模糊字样。
func TestResolveExactHitHasNoFuzzyNote(t *testing.T) {
	it, ok := fuzzyHit(t, []search.UnifiedSong{
		cand("青花瓷", "W4", 239, 2008, 5),
	}, "青花瓷", "周杰伦", "/m/周杰伦 - 青花瓷.mp3")
	if !ok {
		t.Fatalf("完全相等应直接命中，实际未匹配（note=%q）", it.Note)
	}
	if strings.Contains(it.Note, "模糊匹配") {
		t.Errorf("精确命中不该被挂上模糊牌子，实际 note=%q", it.Note)
	}
}
