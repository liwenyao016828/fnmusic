package appupdate

import "testing"

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.18.1", "2.18.0", 1},
		{"2.18.0", "2.18.1", -1},
		{"2.18.0", "2.18.0", 0},
		{"2.18", "2.18.0", 0},    // 缺位补 0
		{"2.19", "2.18.9", 1},    // 段数不同也要比对数
		{"v2.18.1", "2.18.1", 0}, // tag 前缀
		{"V2.19.0", "2.18.1", 1},
		{"2.19.0-beta.1", "2.18.1", 1}, // 预发布后缀忽略，但仍比主版本
		{"2.18.1-rc.1", "2.18.1", 0},
		{"", "0.0.0", 0},
		{"3", "2.9.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersion(c.a, c.b); got != c.want {
			t.Errorf("CompareVersion(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("2.19.0", "2.18.1") {
		t.Fatal("2.19.0 应比 2.18.1 新")
	}
	if IsNewer("2.18.1", "2.18.1") {
		t.Fatal("同版本不应判为更新")
	}
	if IsNewer("2.18.0", "2.18.1") {
		t.Fatal("旧版本不应判为更新")
	}
}
