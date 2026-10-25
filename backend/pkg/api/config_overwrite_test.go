package api

import (
	"testing"

	"fn-lx-player/pkg/config"
)

// 这一组测试锁住 /api/config 的「多把 overwrite 钥匙共存」语义。
//
// 背景（真踩过的 bug）：榜单显示首数与更新限速都要靠 `*_overwrite` 键表达
// 「设成 0 / 清空」，因为 ChartSize / UpdateRateKBPS 带 omitempty，0 与「没传」
// 解码后一样。最早两个分支各写各的、各自 `patch = next`（next 来自 Get() 的旧值），
// 同一次请求里后一个分支把前一个的修改整个盖掉 —— 实测表现是
// 「rate 生效、同请求里的 github token 丢失」和「清空首数不生效」。
//
// mergeConfigPatch 是修法：从当前配置出发，先叠本次 patch 的其它字段。
// 下面的用例直接测这个合并函数，不经过 HTTP，跑得快也不依赖配置目录。

func TestMergeConfigPatchKeepsPatchFields(t *testing.T) {
	next := config.AppConfig{
		Theme:             "light",
		ChartSize:         20,
		UpdateGitHubToken: "old-token",
	}
	patch := config.AppConfig{Theme: "dark"}

	got := mergeConfigPatch(next, patch)

	if got.Theme != "dark" {
		t.Fatalf("patch 里的 theme 应当覆盖，得到 %q", got.Theme)
	}
	// 这三项 patch 里没有，必须原样保留 —— 这正是之前丢失的那部分。
	if got.ChartSize != 20 {
		t.Fatalf("patch 没提 chart_size，应保留 20，得到 %d", got.ChartSize)
	}
	if got.UpdateGitHubToken != "old-token" {
		t.Fatalf("patch 没提 token，应保留旧值，得到 %q", got.UpdateGitHubToken)
	}
}

func TestMergeConfigPatchEmptyPatchIsNoop(t *testing.T) {
	next := config.AppConfig{ChartSize: 30}
	got := mergeConfigPatch(next, config.AppConfig{})
	if got.ChartSize != 30 {
		t.Fatalf("空 patch 不该改动任何字段，得到 %+v", got)
	}
}

// 内部标记（json:"-"）不参与合并，但调用方在合并之后要自己重新置位 ——
// 合并走的是 JSON 往返，`-` 的字段会被丢掉。这条用例把这个约定写下来，
// 免得以后有人以为 mergeConfigPatch 能保留它们。
func TestMergeConfigPatchDropsInternalFlags(t *testing.T) {
	next := config.AppConfig{ChartSize: 10}
	patch := config.AppConfig{ChartSizeOverride: true}
	got := mergeConfigPatch(next, patch)
	if got.ChartSizeOverride {
		t.Fatal("json:\"-\" 的内部标记不该被合并回来，调用方需在之后显式置位")
	}
}
