package config

import "testing"

// 「下载时带歌词 / 内嵌封面」两个开关的**缺省是开**。
//
// 老配置文件里没有这两个键 → 指针为 nil。把 nil 当成关，会让所有老用户升级后
// 下载静默掉一个档次（没歌词 / 没封面），而这正是这两个开关要防的退步。
func TestLyricAndCoverDefaultOn(t *testing.T) {
	var a AppConfig
	if !a.LyricAutoDownloadOn() {
		t.Fatal("歌词开关缺省必须是开")
	}
	if !a.CoverEmbedOn() {
		t.Fatal("封面开关缺省必须是开")
	}
	no := false
	a.LyricAutoDownload = &no
	a.CoverEmbed = &no
	if a.LyricAutoDownloadOn() || a.CoverEmbedOn() {
		t.Fatal("显式 false 才是关")
	}
	yes := true
	a.LyricAutoDownload = &yes
	a.CoverEmbed = &yes
	if !a.LyricAutoDownloadOn() || !a.CoverEmbedOn() {
		t.Fatal("显式 true 当然是开")
	}
}
