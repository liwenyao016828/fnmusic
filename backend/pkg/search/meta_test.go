package search

import "testing"

// 平台搜索响应里的时间戳是两种单位：网易 publishTime 用毫秒、QQ pubtime 用秒。
// 换算错了会写出 1970 年这种脏数据。
func TestYearFromTimestamps(t *testing.T) {
	// 实测值：网易 publishTime=1059580800000(ms)、QQ pubtime=1059580800(s) → 都是 2003 年
	if got := msToYear(1059580800000); got != 2003 {
		t.Errorf("毫秒时间戳应换算成 2003，实际 %d", got)
	}
	if got := secToYear(1059580800); got != 2003 {
		t.Errorf("秒时间戳应换算成 2003，实际 %d", got)
	}
	if msToYear(0) != 0 || secToYear(0) != 0 || secToYear(-1) != 0 {
		t.Error("时间戳缺失（0/负）应返回 0，表示「平台没给」")
	}
}

// "01" 这类带前导零的字符串（网易 cd 字段）要能解析
func TestAtoiLoose(t *testing.T) {
	cases := map[string]int{"01": 1, " 3 ": 3, "": 0, "x": 0, "12": 12}
	for in, want := range cases {
		if got := atoiLoose(in); got != want {
			t.Errorf("atoiLoose(%q) = %d，期望 %d", in, got, want)
		}
	}
}

// QQ 的 belongCD 实测单碟专辑也返回 0 —— 0 一律当「平台没给」，
// 而 cdIdx 有值说明在专辑里 → 补 1。宁可不写，也别写个错的。
func TestDiscOfQQ(t *testing.T) {
	cases := []struct{ belongCD, cdIdx, want int }{
		{2, 5, 2}, // 明确是第 2 碟
		{0, 3, 1}, // belongCD 没给但曲序有 → 单碟第 1 张
		{0, 0, 0}, // 都没给 → 不写
		{1, 0, 1}, // 明确第 1 碟
	}
	for _, c := range cases {
		if got := discOfQQ(c.belongCD, c.cdIdx); got != c.want {
			t.Errorf("discOfQQ(%d,%d) = %d，期望 %d", c.belongCD, c.cdIdx, got, c.want)
		}
	}
}

// QQ 专辑详情的 aDate 形如 "2003-07-31"
func TestYearFromDate(t *testing.T) {
	cases := map[string]int{
		"2003-07-31": 2003, "2003": 2003, "2003-07": 2003,
		"": 0, "abc": 0, "1800-01-01": 0,
	}
	for in, want := range cases {
		if got := yearFromDate(in); got != want {
			t.Errorf("yearFromDate(%q) = %d，期望 %d", in, got, want)
		}
	}
}
