package api

import (
	"testing"
)

// 「不传 fields 就只补歌词+封面」是**向后兼容的关键**：
// 已有调用方（含网页端旧流程）不该因为后端加了六个新字段就悄悄改写年份/风格。
func TestParseCompleteFieldsDefaults(t *testing.T) {
	f := parseCompleteFields(nil, "")
	if !f.Lyric || !f.Cover || f.Year || f.Track || f.Disc || f.Genre {
		t.Errorf("默认应为「只补歌词+封面」，实际 %+v", f)
	}

	f = parseCompleteFields(nil, "cover")
	if f.Lyric || !f.Cover {
		t.Errorf(`only:"cover" 应只勾封面，实际 %+v`, f)
	}
	f = parseCompleteFields(nil, "lyric")
	if !f.Lyric || f.Cover {
		t.Errorf(`only:"lyric" 应只勾歌词，实际 %+v`, f)
	}

	// fields 比 only 更具体，同时给时以 fields 为准
	f = parseCompleteFields([]string{"year"}, "cover")
	if !f.Year || f.Cover {
		t.Errorf("fields 应覆盖 only，实际 %+v", f)
	}
}

func TestParseCompleteFieldsSelection(t *testing.T) {
	f := parseCompleteFields([]string{" Genre ", "DISC", ""}, "")
	if !f.Genre || !f.Disc || f.Year || f.Track || f.Lyric || f.Cover {
		t.Errorf("选择解析不正确，实际 %+v", f)
	}

	// "all" 是一次性勾选六个字段（前端「全选」不必拼六个字符串）
	f = parseCompleteFields([]string{"all"}, "")
	if !f.Lyric || !f.Cover || !f.Genre || !f.Year || !f.Track || !f.Disc {
		t.Errorf(`"all" 应全选，实际 %+v`, f)
	}

	// 全是无法识别的值 ⇒ 退回默认口径，绝不变成「什么都不补」
	f = parseCompleteFields([]string{"publisher", "封面"}, "")
	if !f.Lyric || !f.Cover {
		t.Errorf("未知值应被忽略并退回默认，实际 %+v", f)
	}
}
