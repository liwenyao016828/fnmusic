package api

import (
	"testing"

	"fn-lx-player/pkg/library"
)

// TestTopGapFields 是给「元数据 已修复 0 / 全部」配的解释文本：
// 必须挑出缺口最大的字段、按缺口降序、只取前 n 个，且不把 0 缺口的字段说成缺口。
func TestTopGapFields(t *testing.T) {
	gaps := library.GapCounts{
		Total: 130, Read: 129, AnyField: 129,
		Lyric: 44, Cover: 12, Genre: 128, Year: 105, Track: 127, Disc: 129,
	}
	if got := topGapFields(gaps, 2); got != "光盘 129 首、风格 128 首" {
		t.Fatalf("top2 = %q，期望「光盘 129 首、风格 128 首」", got)
	}
	if got := topGapFields(gaps, 1); got != "光盘 129 首" {
		t.Fatalf("top1 = %q，期望「光盘 129 首」", got)
	}
	if got := topGapFields(library.GapCounts{}, 2); got != "" {
		t.Fatalf("没有缺口时应返回空串，实际 %q", got)
	}
	if got := topGapFields(gaps, 0); got != "" {
		t.Fatalf("n<=0 时应返回空串，实际 %q", got)
	}
}

// TestTidyProgressItemClamps 进度口径的钳位：
// done = covered - pending，且不可能为负（分母是扫过的歌，分子却可能因为
// 换了目录/统计口径变化而更大；宁可显示 0，也不要显示一个负数）。
func TestTidyProgressItemClamps(t *testing.T) {
	ok := tidyProgressItemOf("meta", "元数据", 10, 3, 0, true, "")
	if ok.Done != 7 || ok.Pending != 3 || ok.Covered != 10 {
		t.Fatalf("done/pending/covered = %d/%d/%d，期望 7/3/10", ok.Done, ok.Pending, ok.Covered)
	}

	over := tidyProgressItemOf("meta", "元数据", 2, 5, 0, true, "")
	if over.Done != 0 {
		t.Fatalf("待修多于评估数时 done 应为 0，实际 %d", over.Done)
	}

	neg := tidyProgressItemOf("quality", "低音质替换", -1, -1, 0, false, "还没扫过音质")
	if neg.Covered != 0 || neg.Pending != 0 || neg.Done != 0 {
		t.Fatalf("负数入参应被压成 0，实际 covered=%d pending=%d done=%d", neg.Covered, neg.Pending, neg.Done)
	}
	if neg.Known {
		t.Fatal("known 应原样传递 false")
	}
}