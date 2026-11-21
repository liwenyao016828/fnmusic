package namefmt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildParseRoundTrip(t *testing.T) {
	Set(ArtistTitle)
	defer Set(ArtistTitle)

	base := Build("周杰伦", "晴天")
	if base != "周杰伦 - 晴天" {
		t.Fatalf("Build = %q", base)
	}
	artist, title := Parse(base)
	if artist != "周杰伦" || title != "晴天" {
		t.Fatalf("Parse = %q, %q", artist, title)
	}

	Set(TitleArtist)
	base = Build("周杰伦", "晴天")
	if base != "晴天 - 周杰伦" {
		t.Fatalf("TitleArtist Build = %q", base)
	}
	artist, title = Parse(base)
	if artist != "周杰伦" || title != "晴天" {
		t.Fatalf("TitleArtist Parse = %q, %q", artist, title)
	}
}

// 文件名主干必须有总长上限：逐段各截 180 字节时，两段拼起来会越过
// 文件系统的 255 字节上限（见 MaxBaseBytes 的说明）。
func TestBuildClampsTotalLength(t *testing.T) {
	Set(ArtistTitle)
	defer Set(ArtistTitle)

	longArtist := strings.Repeat("周", 200) // 600 字节
	longTitle := strings.Repeat("杰", 200)
	base := Build(longArtist, longTitle)

	if len(base) > MaxBaseBytes {
		t.Fatalf("主干 %d 字节，超过上限 %d", len(base), MaxBaseBytes)
	}
	if !utf8.ValidString(base) {
		t.Fatalf("截断切坏了 UTF-8: %q", base)
	}
	if strings.HasSuffix(base, " -") || strings.HasPrefix(base, "- ") || strings.HasSuffix(base, " ") {
		t.Fatalf("截断留下了悬空分隔符: %q", base)
	}
}

func TestClampKeepsShortNames(t *testing.T) {
	if got := Clamp("周杰伦 - 晴天", MaxBaseBytes); got != "周杰伦 - 晴天" {
		t.Fatalf("短名字不该被改: %q", got)
	}
	// 截在半个汉字上也要回退到 rune 边界
	got := Clamp(strings.Repeat("周", 100), 8)
	if got != "周周" { // 每个汉字 3 字节，8 字节只能放 2 个
		t.Fatalf("Clamp = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("Clamp 切坏了 UTF-8")
	}
}

// 没有歌手时不要拼出 "- 歌名" 这种带前导破折号的名字。
func TestBuildSkipsSeparatorWhenSideEmpty(t *testing.T) {
	Set(ArtistTitle)
	defer Set(ArtistTitle)

	if got := Build("", "晴天"); got != "晴天" {
		t.Fatalf("空歌手时应只留歌名，实际 %q", got)
	}
	if got := Build("周杰伦", ""); got != "周杰伦" {
		t.Fatalf("空歌名时应只留歌手，实际 %q", got)
	}
	Set(TitleArtist)
	if got := Build("", "晴天"); got != "晴天" {
		t.Fatalf("TitleArtist 下空歌手同样只留歌名，实际 %q", got)
	}
}
