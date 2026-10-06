package downloader

import "testing"

// 「封面内嵌」的缺省必须是**开**。
//
// SongPayload.EmbedCover 是 *bool：字段缺失（前端 `{...song}` 展开、老调用方、
// 曲率自己的自动下载）时它是 nil。把 nil 当成「不要」会让所有下载静默丢封面 ——
// 用户只看到「飞牛里没封面」，而这是明显的退步。
func TestCoverWantedDefaultsTrue(t *testing.T) {
	var s SongPayload
	if !s.CoverWanted() {
		t.Fatal("字段缺失（nil）必须当作「要封面」")
	}
	yes := true
	s.EmbedCover = &yes
	if !s.CoverWanted() {
		t.Fatal("显式 true 当然要")
	}
	no := false
	s.EmbedCover = &no
	if s.CoverWanted() {
		t.Fatal("显式 false 才是不要")
	}
}

// 关掉封面开关之后，抓封面这件事**根本不该发生**（而不是抓完再丢掉）。
func TestCoverForTagsRespectsSwitch(t *testing.T) {
	no := false
	if got := coverForTags(SongPayload{Cover: "https://cdn.example/a.jpg", EmbedCover: &no}); got != "" {
		t.Fatalf("关掉封面后不该返回任何 URL：%q", got)
	}
	if got := coverForTags(SongPayload{Cover: " https://cdn.example/a.jpg "}); got != "https://cdn.example/a.jpg" {
		t.Fatalf("开着时该返回去掉空白的 URL：%q", got)
	}
	if got := coverForTags(SongPayload{}); got != "" {
		t.Fatalf("没给封面链接时返回空：%q", got)
	}
}
