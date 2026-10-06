package complete

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/tidy"
)

// ── resolveName：索引 > AI > 规则 的三层优先级 ──
//
// 这里只测**不需要 AI** 的部分（规则层与索引层）。
// AI 那一层由 `pkg/ai` 的 JudgeName 用例覆盖（含防幻觉），
// 以及 pkg/nameparse 的 NeedsAI 判据覆盖「什么时候才叫 AI」。

func TestResolveNameRuleOnly(t *testing.T) {
	// 不带 AIClient → 只走规则
	opts := Options{}

	cases := []struct {
		path                  string
		idxTitle, idxArtist   string
		wantTitle, wantArtist string
	}{
		// 索引齐了 → 直接用，不碰文件名
		{"/m/whatever.mp3", "晴天", "周杰伦", "晴天", "周杰伦"},
		// 索引缺歌手 → 规则补
		{"/m/周杰伦 - 晴天.mp3", "晴天", "", "晴天", "周杰伦"},
		// 索引什么都没有 → 规则全给
		{"/m/周杰伦 - 晴天.mp3", "", "", "晴天", "周杰伦"},
		// 带序号
		{"/m/01. 周杰伦 - 晴天.flac", "", "", "晴天", "周杰伦"},
		// 只有歌名
		{"/m/晴天.mp3", "", "", "晴天", ""},
		// 空文件名
		{"/m/.mp3", "", "", "", ""},
	}
	for _, c := range cases {
		gotTitle, gotArtist := resolveName(context.Background(), opts, c.path, c.idxTitle, c.idxArtist)
		if gotTitle != c.wantTitle || gotArtist != c.wantArtist {
			t.Errorf("resolveName(%q, idx=(%q,%q)) = (%q, %q)，期望 (%q, %q)",
				c.path, c.idxTitle, c.idxArtist, gotTitle, gotArtist, c.wantTitle, c.wantArtist)
		}
	}
}

// 索引里的值必须优先于规则 —— 它来自文件标签/上次整理，比从文件名猜的准
func TestResolveNameIndexWinsOverRule(t *testing.T) {
	// 文件名说「A - B」，索引说「正确歌手 / 正确歌名」→ 用索引
	gotTitle, gotArtist := resolveName(context.Background(), Options{}, "/m/A - B.mp3", "正确歌名", "正确歌手")
	if gotTitle != "正确歌名" || gotArtist != "正确歌手" {
		t.Errorf("索引应优先于规则，实际 (%q, %q)", gotTitle, gotArtist)
	}
}

// 关掉 UseAINameJudge 时不该依赖 AI（这里没配 AIClient，等价于「AI 不可用」）
func TestResolveNameWithoutAIStillWorks(t *testing.T) {
	gotTitle, gotArtist := resolveName(context.Background(), Options{UseAINameJudge: true}, "/m/周杰伦 - 晴天.mp3", "", "")
	if gotTitle != "晴天" || gotArtist != "周杰伦" {
		t.Errorf("没配 AI 时规则必须仍然可用，实际 (%q, %q)", gotTitle, gotArtist)
	}
}

// ── 默认值：零值 Options 必须能直接用 ──

func TestWithDefaults(t *testing.T) {
	o := withDefaults(Options{})
	if len(o.Platforms) != len(DefaultPlatforms) {
		t.Errorf("平台应回落到默认，实际 %v", o.Platforms)
	}
	if o.Concurrency != 2 {
		t.Errorf("并发度应默认 2，实际 %d", o.Concurrency)
	}
	// 全 false 的 Tidy 视为未指定 → 套默认（补歌词与封面，但不覆盖已有内容）
	def := tidy.DefaultOptions()
	if o.Tidy != def {
		t.Errorf("Tidy 应回落到 DefaultOptions，实际 %+v", o.Tidy)
	}

	// 显式指定时不能被覆盖
	custom := withDefaults(Options{
		Platforms:   []string{"tx"},
		Concurrency: 5,
		Tidy:        tidy.Options{EmbedLyric: true},
	})
	if len(custom.Platforms) != 1 || custom.Platforms[0] != "tx" {
		t.Errorf("显式平台被覆盖：%v", custom.Platforms)
	}
	if custom.Concurrency != 5 {
		t.Errorf("显式并发度被覆盖：%d", custom.Concurrency)
	}
	if custom.Tidy != (tidy.Options{EmbedLyric: true}) {
		t.Errorf("显式 Tidy 被覆盖：%+v", custom.Tidy)
	}
}

// 空输入不应发起任何网络请求，直接返回空汇总
func TestRunEmpty(t *testing.T) {
	s := Run(context.Background(), nil, Options{}, nil)
	if s.Total != 0 || len(s.Results) != 0 {
		t.Errorf("空输入应返回空汇总，实际 %+v", s)
	}
}

// 认不出歌名时**必须提前返回**，不能带着空关键词去搜（否则会搜出一堆无关结果写错标签）
func TestRunUnrecognizableName(t *testing.T) {
	var progresses int
	s := Run(context.Background(), []Target{{Path: "/music/.mp3", NeedLyric: true}}, Options{}, func(Progress) {
		progresses++
	})

	if s.Total != 1 || len(s.Results) != 1 {
		t.Fatalf("应返回 1 条结果，实际 %+v", s)
	}
	r := s.Results[0]
	if r.Matched {
		t.Error("认不出歌名时不应判定为匹配成功")
	}
	if r.OK {
		t.Error("认不出歌名时不应判定为成功")
	}
	if !strings.Contains(r.Error, "认不出歌名") {
		t.Errorf("错误信息应说明认不出歌名，实际 %q", r.Error)
	}
	if s.NoMatch != 1 {
		t.Errorf("应计入 NoMatch，实际 %d", s.NoMatch)
	}
	if progresses != 1 {
		t.Errorf("每首完成都应回调一次进度，实际 %d 次", progresses)
	}
}

func TestDisplayName(t *testing.T) {
	if got := displayName(Target{Path: "/a/b/c.mp3", Title: "晴天"}); got != "晴天" {
		t.Errorf("有标题时用标题，实际 %q", got)
	}
	if got := displayName(Target{Path: "/a/b/c.mp3"}); got != "c.mp3" {
		t.Errorf("无标题时用文件名，实际 %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Errorf("应返回第一个非空并去空格，实际 %q", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Errorf("全空时返回空串，实际 %q", got)
	}
}
