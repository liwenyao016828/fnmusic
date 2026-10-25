package config

import "testing"

// 榜单显示首数（用户 2026-10-24：首数可设，全局 + 按榜）。
// 守两件事：非法输入被夹到区间（前端下拉之外还有手改配置文件的人），
// 以及三档优先级（单榜 > 全局 > 默认）不能倒过来。

func TestChartSizeNormalizeAndPriority(t *testing.T) {
	cases := []struct{ in, want int }{{0, 0}, {-5, 0}, {1, 10}, {10, 10}, {55, 55}, {999, 300}}
	for _, c := range cases {
		if got := NormalizeChartSize(c.in); got != c.want {
			t.Fatalf("NormalizeChartSize(%d)=%d want %d", c.in, got, c.want)
		}
	}
	cfg := AppConfig{}
	if got := cfg.ChartSizeFor("wy_1"); got != DefaultChartSize {
		t.Fatalf("默认应为 %d，得到 %d", DefaultChartSize, got)
	}
	cfg.ChartSize = 40
	if got := cfg.ChartSizeFor("wy_1"); got != 40 {
		t.Fatalf("全局生效失败: %d", got)
	}
	cfg.ChartSizes = map[string]int{"wy_1": 20}
	if got := cfg.ChartSizeFor("wy_1"); got != 20 {
		t.Fatalf("单榜覆盖失败: %d", got)
	}
	if got := cfg.ChartSizeFor("wy_2"); got != 40 {
		t.Fatalf("其它榜应跟全局: %d", got)
	}
	// 清洗：非法值被丢弃，键大小写统一
	norm := NormalizeChartSizes(map[string]int{" WY_9 ": 500, "tx_2": 0, "": 30})
	if norm["wy_9"] != ChartSizeMax || len(norm) != 1 {
		t.Fatalf("清洗结果不对: %#v", norm)
	}
}
