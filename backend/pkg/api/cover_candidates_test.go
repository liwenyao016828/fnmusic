package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/search"
)

// stubCoverSearch 替换多平台搜索，验证「去重 / 上限 / 组参」而不碰网络。
func stubCoverSearch(t *testing.T, fn func(keyword, platform string, page, pageSize int) []search.UnifiedSong) *[]string {
	t.Helper()
	orig := coverSearch
	called := &[]string{}
	coverSearch = func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		*called = append(*called, platform)
		return fn(keyword, platform, page, pageSize)
	}
	t.Cleanup(func() { coverSearch = orig })
	return called
}

func coverCandidatesRequest(t *testing.T, s *Server, query string) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	s.HandleCoverCandidates(rec, httptest.NewRequest(http.MethodGet, "/api/library/cover-candidates?"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Keyword    string `json:"keyword"`
			Candidates []struct {
				CoverURL string `json:"cover_url"`
				Artist   string `json:"artist"`
				Source   string `json:"source"`
			} `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	return map[string]interface{}{"keyword": resp.Data.Keyword, "candidates": resp.Data.Candidates}
}

// 一个搜索词都没有要拒绝（否则会把整个曲库搜一遍）；
// keyword / name / artist 三者有其一即可 —— 只给歌手也能搜。
func TestCoverCandidatesRequiresSomeKeyword(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).HandleCoverCandidates(rec, httptest.NewRequest(http.MethodGet, "/api/library/cover-candidates?", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("没有任何搜索词应返回 400，实际 %d", rec.Code)
	}

	stubCoverSearch(t, func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		return []search.UnifiedSong{{Cover: "https://img/x.jpg"}}
	})
	got := coverCandidatesRequest(t, &Server{}, "artist=%E5%91%A8%E6%9D%B0%E4%BC%A6")
	cands, _ := got["candidates"].([]struct {
		CoverURL string `json:"cover_url"`
		Artist   string `json:"artist"`
		Source   string `json:"source"`
	})
	// 4 个平台都返回同一张地址 → 去重后只剩 1 张（顺带证明「只给歌手也能搜」）
	if len(cands) != 1 {
		t.Fatalf("只给歌手也应搜到结果并去重成 1 张，实际 %d", len(cands))
	}
}

// 同一张封面在多平台会重复出现 —— 必须去重，用户看到的才是「不同的几张可选」
func TestCoverCandidatesDedupesAndCaps(t *testing.T) {
	stubCoverSearch(t, func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		switch platform {
		case "wy":
			return []search.UnifiedSong{
				{Cover: "https://img/a.jpg", Name: "夜曲", Singer: "周杰伦", Source: "wy"},
				{Cover: "https://img/b.jpg", Name: "夜曲", Singer: "周杰伦", Source: "wy"},
				{Cover: "", Name: "无图", Source: "wy"}, // 没封面 → 丢弃
			}
		case "tx":
			// 与 wy 的第一张同址 → 去重后不该出现两次
			return []search.UnifiedSong{
				{Cover: "https://img/a.jpg", Name: "夜曲", Singer: "周杰伦", Source: "tx"},
				{Cover: "https://img/c.jpg", Name: "夜曲", Singer: "周杰伦", Source: "tx"},
			}
		}
		return nil
	})
	s := &Server{}

	got := coverCandidatesRequest(t, s, "name=%E5%A4%9C%E6%9B%B2&artist=%E5%91%A8%E6%9D%B0%E4%BC%A6")
	cands, _ := got["candidates"].([]struct {
		CoverURL string `json:"cover_url"`
		Artist   string `json:"artist"`
		Source   string `json:"source"`
	})
	if len(cands) != 3 {
		t.Fatalf("应去重成 3 张（a/b/c），实际 %d", len(cands))
	}
	wantOrder := []string{"https://img/a.jpg", "https://img/b.jpg", "https://img/c.jpg"}
	for i, w := range wantOrder {
		if cands[i].CoverURL != w {
			t.Errorf("第 %d 张应为 %s，实际 %s", i+1, w, cands[i].CoverURL)
		}
	}
	if !strings.Contains(got["keyword"].(string), "夜曲") {
		t.Errorf("keyword 应含歌名，实际 %q", got["keyword"])
	}
}

// limit 要生效（前端一次只展示几张）
func TestCoverCandidatesRespectsLimit(t *testing.T) {
	stubCoverSearch(t, func(keyword, platform string, page, pageSize int) []search.UnifiedSong {
		return []search.UnifiedSong{
			{Cover: "https://img/" + platform + "1.jpg"},
			{Cover: "https://img/" + platform + "2.jpg"},
			{Cover: "https://img/" + platform + "3.jpg"},
		}
	})
	s := &Server{}

	got := coverCandidatesRequest(t, s, "name=x&limit=2")
	cands, _ := got["candidates"].([]struct {
		CoverURL string `json:"cover_url"`
		Artist   string `json:"artist"`
		Source   string `json:"source"`
	})
	if len(cands) != 2 {
		t.Fatalf("limit=2 应只返回 2 张，实际 %d", len(cands))
	}
}
