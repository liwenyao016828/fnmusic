package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"fn-lx-player/pkg/library"
)

// 缺口接口必须把「写不了标签的格式」单列出来，而不是混进缺口里。
//
// 守的是 m01698 那个坑的前半段：wav/opus 既算「待读」又算「待补」，
// 于是 any_field 永远减不到 0 —— 界面上的数字永远显示「还有 N 首要补」，
// 用户点多少次都看不到收敛。后半段（补全不再排这些活）在 pkg/library 的单测里。
func TestHandleLibraryGapsReportsUnwritableFormats(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", dir)

	s := &Server{}
	s.libIndex = library.NewIndex(t.TempDir())
	s.libIndex.Upsert(library.Entry{Path: filepath.Join(dir, "a.mp3"), Size: 1, MTime: 1, Format: "mp3"})
	s.libIndex.Upsert(library.Entry{Path: filepath.Join(dir, "b.wav"), Size: 2, MTime: 2, Format: "wav"})
	s.libIndex.Upsert(library.Entry{Path: filepath.Join(dir, "c.opus"), Size: 3, MTime: 3, Format: "opus"})

	rec := httptest.NewRecorder()
	s.HandleLibraryGaps(rec, httptest.NewRequest(
		http.MethodGet, "/api/library/gaps?dir="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d, 期望 200；body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data struct {
			Unread int `json:"unread"`
			Gaps   struct {
				Total           int            `json:"total"`
				Read            int            `json:"read"`
				AnyField        int            `json:"any_field"`
				Unsupported     int            `json:"unsupported"`
				UnsupportedExts map[string]int `json:"unsupported_exts"`
			} `json:"gaps"`
			WritableFormats []string `json:"writable_formats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v；body=%s", err, rec.Body.String())
	}

	d := resp.Data
	if d.Gaps.Total != 3 {
		t.Errorf("total = %d, 期望 3", d.Gaps.Total)
	}
	if d.Gaps.Unsupported != 2 {
		t.Errorf("unsupported = %d, 期望 2（wav + opus）", d.Gaps.Unsupported)
	}
	if d.Gaps.UnsupportedExts["wav"] != 1 || d.Gaps.UnsupportedExts["opus"] != 1 {
		t.Errorf("unsupported_exts = %v, 期望 {wav:1, opus:1}", d.Gaps.UnsupportedExts)
	}
	// unread 只能算「能写但没读过」的那条 mp3：写不了的格式读多少遍也没字段
	if d.Unread != 1 {
		t.Errorf("unread = %d, 期望 1（wav/opus 不该被劝去「增量扫描」）", d.Unread)
	}
	if d.Gaps.AnyField != 1 {
		t.Errorf("any_field = %d, 期望 1（只有 mp3 算缺口）", d.Gaps.AnyField)
	}
	if len(d.WritableFormats) != 2 || d.WritableFormats[0] != "flac" || d.WritableFormats[1] != "mp3" {
		t.Errorf("writable_formats = %v, 期望 [flac mp3]（AI 调用方据此解释补不上的原因）", d.WritableFormats)
	}
}
