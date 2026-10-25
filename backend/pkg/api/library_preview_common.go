package api

// 预览类接口的公共入参处理。
//
// 抽出来的原因：同步预览（POST /api/library/complete·dry_run）与流式预览
// （POST /api/library/complete/preview/stream）必须**挑出同一批曲目、用同一套匹配参数**，
// 否则用户切换两种入口会看到不一样的清单。两处各写一遍迟早会走样。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/library"
)

// previewRequestBody 预览要用的那几个字段（两种入口共用）。
type previewRequestBody struct {
	Dir        string   `json:"dir"`
	Limit      int      `json:"limit"`
	Platforms  []string `json:"platforms"`
	Fields     []string `json:"fields"`
	Only       string   `json:"only"`
	UseAIGenre bool     `json:"use_ai_genre"`
	Overwrite  bool     `json:"overwrite"`
}

// previewParams 解析好的预览参数（挑完曲目之后，执行匹配时还要用到后三个）。
type previewParams struct {
	Req     previewRequestBody
	Targets []complete.Target
	Gaps    library.GapCounts
	Dir     string
}

// parsePreviewParams 读请求体、解析目录、挑出要预览的曲目。
//
// 返回的 errMsg 是 4xx 级别的错误文案（空串表示没问题），emptyHint 是
// 「一条都不用跑」时的提示（空串表示有活可干）。两者互斥。
func (s *Server) parsePreviewParams(r *http.Request) (p previewParams, errMsg, emptyHint string) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return p, "读取请求体失败", ""
	}
	// 只解析一次，把原始字节留着 —— 另一个入口还要从这里取同样的参数
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := json.Unmarshal(body, &p.Req); err != nil {
		return p, "请求体不是合法 JSON", ""
	}

	d := strings.TrimSpace(p.Req.Dir)
	if d == "" {
		return p, "请提供 dir", ""
	}
	resolved, err := resolveLibraryPath(d)
	if err != nil {
		return p, err.Error(), ""
	}
	p.Dir = resolved

	limit := p.Req.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		return p, "单次最多预览 200 首（每首要走两次外网请求，再多会跑很久）", ""
	}

	fields := parseCompleteFields(p.Req.Fields, p.Req.Only)
	p.Gaps = s.libraryIndex().FieldGaps(resolved)
	entries := s.libraryIndex().NeedingFields(resolved, fields, limit)
	if len(entries) == 0 {
		hint := "索引里没有勾选字段仍缺失的记录。若刚加了新歌，先跑一次「扫描」再试。"
		if note := unsupportedNote(p.Gaps, "它们不算在缺口里"); note != "" {
			hint += note
		}
		return p, "", hint
	}

	for _, e := range entries {
		t := complete.Target{Path: e.Path, Title: e.Title, Artist: e.Artist, Album: e.Album}
		if fields.Lyric && !e.HasLyric {
			t.NeedLyric = true
		}
		if fields.Cover && !e.HasCover {
			t.NeedCover = true
		}
		if fields.Genre && e.Genre == "" {
			t.NeedGenre = true
		}
		if fields.Year && e.Year <= 0 {
			t.NeedYear = true
		}
		if fields.Track && e.Track <= 0 {
			t.NeedTrack = true
		}
		if fields.Disc && e.Disc <= 0 {
			t.NeedDisc = true
		}
		if !t.WantsAnything() {
			continue
		}
		p.Targets = append(p.Targets, t)
	}
	if len(p.Targets) == 0 {
		return p, "", "勾选的字段这些记录都已具备（索引可能滞后，先跑一次「扫描」）。"
	}
	return p, "", ""
}

// options 用同一套参数造匹配选项（与 HandleLibraryComplete 里那条路完全一致）。
func (s *Server) options(p previewParams) complete.Options {
	return s.buildCompleteOptions(p.Req.Platforms, p.Req.UseAIGenre, p.Req.Overwrite)
}
