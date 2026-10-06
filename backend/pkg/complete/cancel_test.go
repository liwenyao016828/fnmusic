package complete

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 停止 = 不再派发新曲目。
//
// 这些用例守的是一条很容易退化的语义：以前补全任务喂的是 context.Background()，
// 于是「停止」这件事在代码里根本不存在。加回来之后，最容易犯的错是
// 「把被停止的曲目算成 no_match / failed」—— 那会让用户在汇总里看到
// 「没搜到 180 首」，以为是自己曲库里的歌太冷门，而实际上是他自己点了停止。
func TestResolveStopsDispatchingWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就是停止状态

	targets := []Target{
		{Path: "/m/a.mp3", Title: "a"},
		{Path: "/m/b.mp3", Title: "b"},
		{Path: "/m/c.mp3", Title: "c"},
	}
	plan := Resolve(ctx, targets, Options{}, nil)

	if len(plan.Items) != 0 {
		t.Fatalf("停止后的解析不应该产出任何计划项，得到 %d 条", len(plan.Items))
	}
	if plan.Cancelled != len(targets) {
		t.Fatalf("没轮到的曲目数应记为 Cancelled=%d，得到 %d", len(targets), plan.Cancelled)
	}
}

func TestExecuteWithCancelledContextWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plan := Plan{Items: []PlanItem{
		{Path: "/m/a.mp3", Title: "a"},
		{Path: "/m/b.mp3", Title: "b"},
	}}
	sum := Execute(ctx, plan, Options{}, nil)

	if len(sum.Results) != 0 {
		t.Fatalf("已停止的任务不应该产生任何逐条结果，得到 %d 条", len(sum.Results))
	}
	if sum.Cancelled != 2 {
		t.Fatalf("两首都没处理，Cancelled 应为 2，得到 %d", sum.Cancelled)
	}
	// 「没处理」不能被混进其它桶里 —— 这是本用例真正盯的东西。
	if sum.NoMatch != 0 || sum.Failed != 0 || sum.Matched != 0 {
		t.Fatalf("被停止的曲目不该被算成没搜到/失败/匹配：no_match=%d failed=%d matched=%d",
			sum.NoMatch, sum.Failed, sum.Matched)
	}
	if sum.Total != 2 {
		t.Fatalf("总数应仍是计划里的 2 首，得到 %d", sum.Total)
	}
}

// 序列化字段名是对外契约：前端读的是 `summary.cancelled`、`cancelled`（任务级），
// 改了名字不会编译报错，只会让界面永远显示「上次补全结果」。
func TestCancelledFieldsHaveStableJSONNames(t *testing.T) {
	res, err := json.Marshal(ItemResult{Path: "/m/a.mp3", Cancelled: true})
	if err != nil {
		t.Fatalf("marshal ItemResult: %v", err)
	}
	if !strings.Contains(string(res), `"cancelled":true`) {
		t.Fatalf("ItemResult.Cancelled 的 JSON 名应为 cancelled，得到 %s", res)
	}

	sum, err := json.Marshal(Summary{Cancelled: 3})
	if err != nil {
		t.Fatalf("marshal Summary: %v", err)
	}
	// 不是 omitempty：0 是有意义的（跑完了，一首都没停）
	if !strings.Contains(string(sum), `"cancelled":3`) {
		t.Fatalf("Summary.Cancelled 应始终出现且为数字，得到 %s", sum)
	}

	// Plan 的 cancelled 是补充信息：正常跑完（全部轮到）时不该出现
	plan, err := json.Marshal(Plan{})
	if err != nil {
		t.Fatalf("marshal Plan: %v", err)
	}
	if strings.Contains(string(plan), `"cancelled"`) {
		t.Fatalf("Plan.Cancelled 为 0 时不该出现在 JSON 里，得到 %s", plan)
	}
}
