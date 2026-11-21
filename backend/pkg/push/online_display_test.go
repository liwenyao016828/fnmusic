package push

import (
	"testing"

	"fn-lx-player/pkg/online"
)

func TestVersionDiffReason(t *testing.T) {
	cases := []struct {
		src, other int
		diff       bool
	}{
		{269, 270, false}, // 1 秒差，同一版
		{269, 258, false}, // 11 秒但在 15%（40 秒）容差内
		{269, 180, true},  // 差 89 秒，明显是不同版
		{200, 170, false}, // 差 30 秒正好等于 15% 容差（30 秒），同一版
		{200, 168, true},  // 差 32 秒 > 30 秒容差，不同版
		{0, 180, false},   // 时长未知，不猜
		{269, 0, false},
	}
	for _, c := range cases {
		got := versionDiffReason(c.src, c.other)
		if (got != "") != c.diff {
			t.Errorf("versionDiffReason(%d,%d) = %q; diff=%v", c.src, c.other, got, c.diff)
		}
	}
}

func TestOnlineTrackFor(t *testing.T) {
	opts := Options{Platform: "NETEASE"} // 平台大小写/别名应在 Normalized 前处理
	// 这里只测 opts.Platform 直接小写；别名映射不在这个函数里。
	tr := Track{Name: "晴天", Artist: "周杰伦, 林俊杰", Album: "叶惠美", Duration: 269, SongID: "123", Cover: "http://x/c.jpg"}
	ot := onlineTrackFor(Options{Platform: "wy"}, tr)
	if ot == nil {
		t.Fatal("onlineTrackFor returned nil")
	}
	if ot.Platform != "wy" || ot.PlatformID != "123" || ot.Title != "晴天" || ot.Duration != 269 {
		t.Fatalf("unexpected online track: %+v", ot)
	}
	if len(ot.Artists) != 2 || ot.Artists[0] != "周杰伦" || ot.Artists[1] != "林俊杰" {
		t.Fatalf("artists = %+v", ot.Artists)
	}
	// 平台或 id 缺失都不能作为在线条目
	if onlineTrackFor(opts, Track{Name: "x"}) != nil {
		t.Fatal("missing SongID should return nil")
	}
	if onlineTrackFor(Options{}, Track{Name: "x", SongID: "1"}) != nil {
		t.Fatal("missing platform should return nil")
	}
}

func TestMarkOnline(t *testing.T) {
	it := &ItemResult{Name: "x"}
	ot := &online.Track{Platform: "wy", PlatformID: "1"}
	markOnline(it, ot, "reason")
	if !it.Online || it.OnlineReason != "reason" {
		t.Fatalf("markOnline: %+v", it)
	}
	// nil 不能 panic
	markOnline(nil, ot, "x")
	markOnline(it, nil, "x")
}
