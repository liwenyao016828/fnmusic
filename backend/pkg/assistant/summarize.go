package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
)

// apiRow 是 /api/catalog 里一条接口条目（其它接口清单类响应也能套用）。
type apiRow struct {
	Method  string
	Path    string
	Summary string
}

func apiRowOf(item json.RawMessage) (apiRow, bool) {
	var e apiRow
	if json.Unmarshal(item, &e) != nil || e.Path == "" {
		return apiRow{}, false
	}
	return e, true
}

func (r apiRow) line() string {
	line := strings.TrimSpace(r.Method + " " + r.Path)
	if r.Summary != "" {
		line += " — " + r.Summary
	}
	return line
}

// match 判断条目是否命中任一关键词（方法/路径/说明里出现即可）。
func (r apiRow) match(terms []string) bool {
	hay := strings.ToLower(r.Method + " " + r.Path + " " + r.Summary)
	for _, t := range terms {
		if strings.Contains(hay, t) {
			return true
		}
	}
	return false
}

// maxCatalogRows 不带过滤词时最多展开多少条条目行。
const maxCatalogRows = 80

// maxPlainItems 非「接口清单」数组（usage 提示语、字符串列表…）最多列几条。
const maxPlainItems = 10

// appendArrayRows 把一个数组渲染成「键: 共 N 条」+ 条目行。
//
// 两类数组要分开对待：
//   - 接口清单（条目带 path）：过滤词非空时只展开命中的。181 条里
//     /api/library/* 排在中后段，按位置截断会让它们彻底看不到，
//     于是模型只能回答「被截断了，可能还有」。按关键词筛选才是对的取舍。
//   - 普通列表（usage 提示语、字符串数组…）：没有 path 可筛，
//     照原样列前几条即可 —— 绝不能对它报「没有匹配关键词」，
//     那会让模型以为这块内容不存在。
func appendArrayRows(lines []string, key string, arr []json.RawMessage, filter string) []string {
	rows := make([]apiRow, 0, len(arr))
	for _, item := range arr {
		if row, ok := apiRowOf(item); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return appendPlainItems(lines, key, arr)
	}

	lines = append(lines, fmt.Sprintf("%s: 共 %d 条", key, len(arr)))
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(filter)))
	if len(terms) > 0 {
		hit := 0
		for _, row := range rows {
			if row.match(terms) {
				lines = append(lines, "  "+row.line())
				hit++
			}
		}
		if hit == 0 {
			lines = append(lines, fmt.Sprintf("  …（没有匹配「%s」的条目）", filter))
		}
		if miss := len(arr) - hit; miss > 0 {
			lines = append(lines, fmt.Sprintf("  …（另有 %d 条与「%s」无关，已省略）", miss, filter))
		}
		return lines
	}
	for i, row := range rows {
		if i >= maxCatalogRows {
			lines = append(lines, fmt.Sprintf("  …（另有 %d 条未列出，可用 filter 参数按关键词筛选）", len(arr)-maxCatalogRows))
			break
		}
		lines = append(lines, "  "+row.line())
	}
	return lines
}

// appendPlainItems 列普通数组（非接口清单）的前若干条。
func appendPlainItems(lines []string, key string, arr []json.RawMessage) []string {
	lines = append(lines, fmt.Sprintf("%s: 共 %d 条", key, len(arr)))
	for i, item := range arr {
		if i >= maxPlainItems {
			lines = append(lines, fmt.Sprintf("  …（另有 %d 条未列出）", len(arr)-maxPlainItems))
			break
		}
		var s string
		if json.Unmarshal(item, &s) == nil && s != "" {
			lines = append(lines, "  "+s)
			continue
		}
		// 对象数组：给出名称类字段，而不是整条丢掉
		var e struct {
			Label   string `json:"label"`
			Title   string `json:"title"`
			Name    string `json:"name"`
			KeyName string `json:"key"`
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		}
		if json.Unmarshal(item, &e) == nil {
			if label := firstNonEmpty(e.Label, e.Title, e.Name, e.KeyName, e.ID); label != "" {
				if e.Enabled != nil {
					state := "关"
					if *e.Enabled {
						state = "开"
					}
					label += "（" + state + "）"
				}
				lines = append(lines, "  "+label)
			}
		}
	}
	return lines
}

// firstNonEmpty 取第一个非空值（数组条目命名不统一时用）。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
