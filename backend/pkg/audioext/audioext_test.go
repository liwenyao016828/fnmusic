package audioext

import (
	"reflect"
	"testing"
)

// 两组清单的边界：能被扫进库 ≠ 能被写标签。混用会让「补全成功」变成空话。
func TestExtsVsWritable(t *testing.T) {
	if Has("/m/a.wav") != true {
		t.Error("wav 应该能被扫描/索引（在 Exts 里）")
	}
	if CanWriteTags("/m/a.wav") {
		t.Error("wav 不在写入器范围内，CanWriteTags 应该是 false")
	}
	if !CanWriteTags("/m/a.MP3") || !CanWriteTags("/m/a.flac") {
		t.Error("mp3/flac 应该可写（大小写不敏感）")
	}
	// Writable 必须是 Exts 的子集：能写但不能扫的格式是配置错误
	for ext := range Writable {
		if !Exts[ext] {
			t.Errorf("Writable 里有 %s，但 Exts 里没有 —— 能写却扫不到，说明清单漂移了", ext)
		}
	}
}

// CanWriteFormat 是「手上只有格式名（曲库索引里的 Entry.Format）」时的入口。
func TestCanWriteFormat(t *testing.T) {
	cases := []struct {
		format string
		want   bool
	}{
		{"flac", true},
		{"mp3", true},
		{"MP3", true},
		{".flac", true}, // 带点的写法也认
		{" wav ", false},
		{"opus", false},
		{"aac", false},
		{"", false}, // 老索引没记格式：保守当写不了，别排进队里白跑
	}
	for _, c := range cases {
		if got := CanWriteFormat(c.format); got != c.want {
			t.Errorf("CanWriteFormat(%q) = %v, 期望 %v", c.format, got, c.want)
		}
	}
}

func TestWritableFormats(t *testing.T) {
	if got := WritableFormats(); !reflect.DeepEqual(got, []string{"flac", "mp3"}) {
		t.Errorf("WritableFormats() = %v, 期望 [flac mp3]（升序、不带点）", got)
	}
}
