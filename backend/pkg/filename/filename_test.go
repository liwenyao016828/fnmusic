package filename

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"非法字符换下划线", `a/b:c*d?e"f<g>h|i`, "a_b_c_d_e_f_g_h_i"},
		{"去掉首尾点与空格", "  ..歌名.  ", "歌名"},
		{"空名兜底", "   ", "未知"},
		{"控制字符去掉", "歌\x01名\x7f", "歌名"},
		{"Windows 保留名", "CON", "CON_"},
		{"保留名大小写不敏感", "nul", "nul_"},
		{"正常名字不动", "周杰伦 - 稻香", "周杰伦 - 稻香"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Fatalf("Sanitize(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeInvalidUTF8 是本轮的核心回归：GBK 脏字节不能原样落盘。
func TestSanitizeInvalidUTF8(t *testing.T) {
	// "\xb8\xe8\xc3\xfb" 是 GBK 的「歌名」，按 UTF-8 解是非法字节
	in := "\xb8\xe8\xc3\xfb"
	got := Sanitize(in)
	if !utf8.ValidString(got) {
		t.Fatalf("清洗结果必须是合法 UTF-8，实际 %q", got)
	}
	if !strings.Contains(got, "_") {
		t.Fatalf("非法 UTF-8 应被替换成下划线，实际 %q", got)
	}
	if !IsClean(got) {
		t.Fatalf("清洗结果自己仍然不干净：%q", got)
	}
}

func TestSanitizeTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("啊", 120) // 360 字节，远超上限
	got := Sanitize(long)
	if len(got) > maxBytes {
		t.Fatalf("截断后仍超上限：%d 字节", len(got))
	}
	if !IsClean(got) {
		t.Fatalf("截断结果不合法：%q", got)
	}
	// 不能出现半个汉字
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("截断切出了半个字符：%q", got)
		}
	}
}

func TestIsClean(t *testing.T) {
	cases := map[string]bool{
		"正常歌名.mp3":     true,
		"a_b - c.flac": true,
		"歌\u0001名.mp3": false,
		"尾点.mp3.":      false,
		" 前空格.mp3":     false,
		"bad/name.mp3": false,
		"CON.mp3":      false,
	}
	for in, want := range cases {
		if got := IsClean(in); got != want {
			t.Fatalf("IsClean(%q) = %v，期望 %v", in, got, want)
		}
	}
}
