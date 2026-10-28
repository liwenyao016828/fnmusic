package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/library"
)

// 曲库类接口的 dir 必须是**可选**的：不传就用配置里生效的曲库目录。
//
// 守的是用户实测报回来的坑：内置 AI 助手问「曲库有多大」时，library_stats
// 能力恒返回 400「请提供 dir」——因为该能力的 Build 从不带 dir，而接口又硬要求
// dir，两边一夹就成了「永远查不到曲库」。外部调用方（curl / 别的 AI）同样中招：
// 不带 dir 是 400，带错目录是 403，等于逼调用方先猜一次目录。
//
// 口径定在这里：**调用方显式给了 dir 就照用；没给就回退到配置**。
// 这样既不让「猜目录」成为前置条件，也没有放宽路径守卫（resolveLibraryPath 照旧）。
func TestLibraryEndpointsDefaultToConfiguredDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", root)

	mgr, err := config.NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	cfg := mgr.Get()
	cfg.DefaultNasDir = root
	cfg.DownloadDir = root
	if err := mgr.Update(cfg); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	s := &Server{cfgMgr: mgr}
	s.libIndex = library.NewIndex(t.TempDir())
	s.libIndex.Upsert(library.Entry{Path: filepath.Join(root, "a.mp3"), Size: 1, MTime: 1, Format: "mp3"})
	s.libIndex.Upsert(library.Entry{Path: filepath.Join(root, "b.mp3"), Size: 2, MTime: 2, Format: "mp3"})

	// 不带 dir 的四个只读曲库接口都必须 200，且真的查到这条索引。
	cases := []struct {
		name string
		path string
		h    http.HandlerFunc
	}{
		{"index/stats", "/api/library/index/stats", s.HandleLibraryIndexStats},
		{"library/gaps", "/api/library/gaps", s.HandleLibraryGaps},
		{"duplicates", "/api/duplicates", s.HandleDuplicates},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.h(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s 不带 dir 应回退到配置目录并返回 200，实际 HTTP %d；body=%s",
					c.path, rec.Code, rec.Body.String())
			}
		})
	}

	// stats 必须真的统计到配置目录下的 2 首歌 —— 只回 200 不算修好。
	rec := httptest.NewRecorder()
	s.HandleLibraryIndexStats(rec, httptest.NewRequest(http.MethodGet, "/api/library/index/stats", nil))
	var resp struct {
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v；body=%s", err, rec.Body.String())
	}
	if resp.Data.Total != 2 {
		t.Errorf("曲库统计 total = %d，期望 2（回退目录没生效？）", resp.Data.Total)
	}

	// 显式传 dir 仍然优先（行为不变），不能因为加默认值就把调用方的参数吃掉。
	other := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", root+":"+other)
	rec = httptest.NewRecorder()
	s.HandleLibraryIndexStats(rec,
		httptest.NewRequest(http.MethodGet, "/api/library/index/stats?dir="+other, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("显式 dir 应返回 200，实际 HTTP %d；body=%s", rec.Code, rec.Body.String())
	}
	resp.Data.Total = -1
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.Data.Total != 0 {
		t.Errorf("显式指定空目录时 total = %d，期望 0（说明默认值盖掉了调用方参数）", resp.Data.Total)
	}
}

// 歌词缺口接口（POST + body.dir）也要接受省略 dir。
func TestLyricGapsDefaultsToConfiguredDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", root)

	mgr, err := config.NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	cfg := mgr.Get()
	cfg.DefaultNasDir = root
	cfg.DownloadDir = root
	if err := mgr.Update(cfg); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	s := &Server{cfgMgr: mgr}
	s.libIndex = library.NewIndex(t.TempDir())

	rec := httptest.NewRecorder()
	s.HandleLibraryLyricGaps(rec, httptest.NewRequest(http.MethodPost, "/api/library/lyric/gaps",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("不带 dir 应回退配置目录并返回 200，实际 HTTP %d；body=%s", rec.Code, rec.Body.String())
	}
}
