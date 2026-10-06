package search

import (
	"testing"

	"fn-lx-player/pkg/online"
)

// 外挂音源声明的文件体积必须一路带到 UnifiedSong —— 「同档比体积」全靠它。
//
// 以前这个字段在 core.py 里就有（`file_size`），但 Go 侧的 UnifiedSong 没有对应
// 字段、映射时被静默丢掉，所以「同档比体积」根本无从谈起。
func TestUnifiedFromMusicDLCarriesFileSize(t *testing.T) {
	got := unifiedFromMusicDL(online.MusicDLSong{
		Source: "mg", PlatformID: "600902000006889366",
		Title: "晴天", Artist: "周杰伦", Ext: "flac", FileSize: 45 << 20,
	})
	if got.FileSize != 45<<20 {
		t.Fatalf("体积该带过来，得到 %d", got.FileSize)
	}
	if got.Quality != "无损" {
		t.Fatalf("档位该由扩展名推出：%q", got.Quality)
	}
	// 平台没给时是 0（= 没有信息），不是负数也不是瞎猜的值
	empty := unifiedFromMusicDL(online.MusicDLSong{Source: "mg", PlatformID: "1", Title: "甲", Ext: "mp3"})
	if empty.FileSize != 0 {
		t.Fatalf("没给体积该是 0：%d", empty.FileSize)
	}
}
