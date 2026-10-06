package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fn-lx-player/pkg/online"
)

// 这一组盯的是「外挂搜索器」这一层：注册表的分派、条目转换、以及**它绝不能把
// 原生搜索弄坏**（没注册时 `Search()` 的行为必须与以前逐字节一样）。

// fakeSearcher 是一个只回固定结果的假搜索器。
type fakeSearcher struct {
	platform string
	items    []UnifiedSong
	calls    int
}

func (f *fakeSearcher) Platform() string { return f.platform }

func (f *fakeSearcher) Search(_ context.Context, _ string, _, _ int) []UnifiedSong {
	f.calls++
	return f.items
}

func TestSearchUsesRegisteredSearcher(t *testing.T) {
	f := &fakeSearcher{platform: "mg", items: []UnifiedSong{
		{ID: "1", Songmid: "1", Name: "海屿你", Singer: "马也_Crabbit", Source: "mg"},
	}}
	RegisterSearcher(f)
	t.Cleanup(func() { UnregisterSearcher("mg") })

	got := Search("海屿你", "mg", 1, 20)
	if f.calls != 1 {
		t.Fatalf("注册了搜索器的平台应走外挂，实际调用 %d 次", f.calls)
	}
	if len(got) != 1 || got[0].Songmid != "1" || got[0].Source != "mg" {
		t.Fatalf("应返回外挂的结果：%+v", got)
	}
}

func TestSearchPlatformIsCaseInsensitive(t *testing.T) {
	f := &fakeSearcher{platform: "mg", items: []UnifiedSong{{ID: "1", Songmid: "1", Name: "甲", Source: "mg"}}}
	RegisterSearcher(f)
	t.Cleanup(func() { UnregisterSearcher("mg") })

	if got := Search("甲", " MG ", 1, 20); len(got) != 1 {
		t.Fatalf("平台名该大小写/空白无关：%+v", got)
	}
}

func TestUnregisterRestoresNativeBehaviour(t *testing.T) {
	f := &fakeSearcher{platform: "mg", items: []UnifiedSong{{ID: "1", Songmid: "1", Name: "甲", Source: "mg"}}}
	RegisterSearcher(f)
	UnregisterSearcher("mg")

	// 摘掉之后这个平台回到「原生 switch 里没有 → 返回 nil」的既有行为。
	// 这条同时证明了「注册外挂**不会**改变没注册时的任何行为」。
	if got := Search("甲", "mg", 1, 20); len(got) != 0 {
		t.Fatalf("摘掉外挂后该返回空：%+v", got)
	}
	if f.calls != 0 {
		t.Fatal("摘掉之后不该再调它")
	}
}

func TestRegisterIgnoresEmptyPlatform(t *testing.T) {
	// Platform() 返回空串的搜索器（比如没绑定平台的壳）不能被注册进表里 ——
	// 否则它会覆盖掉「空平台」这个键，把分派搞乱。
	RegisterSearcher(&fakeSearcher{platform: ""})
	RegisterSearcher(nil)
	if got := ExternalSearcherPlatforms(); len(got) != 0 {
		t.Fatalf("空平台不该进注册表：%+v", got)
	}
}

func TestUnifiedFromMusicDLKeepsPlatformIDAsSongmid(t *testing.T) {
	// ⚠️ 这条是跨进程契约的守卫：解析时 `Track.PlatformID` 用的是 `Songmid`，
	// 而 sidecar 认识的 id 是**不带短码前缀**的平台内 id。这里要是把带前缀的
	// `id`（`mg:42`）填进 Songmid，解析就永远对不上。
	got := unifiedFromMusicDL(online.MusicDLSong{
		ID: "mg:42", Source: "mg", PlatformID: "42",
		Title: "海屿你", Artist: "马也_Crabbit", Album: "专辑",
		DurationS: 295, Ext: "flac", CoverURL: "https://c/1.jpg",
	})
	if got.Songmid != "42" || got.ID != "42" {
		t.Fatalf("Songmid 必须是平台内 id（不带短码前缀），得到 %q", got.Songmid)
	}
	if got.Source != "mg" || got.Name != "海屿你" || got.Singer != "马也_Crabbit" {
		t.Fatalf("字段映射不对：%+v", got)
	}
	if got.Duration != 295 || got.Interval != 295 {
		t.Fatalf("时长要同时填 Duration 与 Interval：%+v", got)
	}
	if got.Quality != "无损" || len(got.Qualitys) != 1 || got.Qualitys[0] != "无损" {
		t.Fatalf("flac 该映射成无损档：%+v", got)
	}
}

func TestUnifiedFromMusicDLFallsBackToStrippingPrefix(t *testing.T) {
	// sidecar 没给 platform_id（老版本 / 异常条目）时，从 `source:` 前缀里剥出来
	got := unifiedFromMusicDL(online.MusicDLSong{ID: "bq:abc", Source: "bq", Title: "甲"})
	if got.Songmid != "abc" {
		t.Fatalf("该从 id 里剥掉短码前缀，得到 %q", got.Songmid)
	}
}

func TestQualityFromExtNeverInventsBitrate(t *testing.T) {
	// 外挂平台只告诉我们容器格式，不知道码率 —— 不能猜（猜了界面上就是假信息）
	cases := map[string]string{
		"flac": "无损", "ape": "无损", "wav": "无损",
		"m4a": "AAC", "aac": "AAC",
		"mp3": "320k",
		"":    "",
		"ogg": "", // 认不出就不给，不硬套一个
	}
	for ext, want := range cases {
		if got := qualityFromExt(ext); got != want {
			t.Errorf("qualityFromExt(%q) = %q，期望 %q", ext, got, want)
		}
	}
}

// ── MusicDLSearcher（真发一次 HTTP 给假 sidecar）──────────────────────

func fakeSidecarSearch(t *testing.T, handler http.HandlerFunc) *online.MusicDL {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return online.NewMusicDL(srv.URL, t.Logf)
}

func TestMusicDLSearcherFiltersOtherSources(t *testing.T) {
	m := fakeSidecarSearch(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"items": []map[string]any{
				{"id": "mg:1", "source": "mg", "platform_id": "1", "title": "甲", "download_url": "http://d/1"},
				{"id": "bq:2", "source": "bq", "platform_id": "2", "title": "乙", "download_url": "http://d/2"},
			},
			"errors": map[string]any{},
		})
	})
	s := &MusicDLSearcher{m: m, perPage: 10}

	got := (&musicDLPlatformSearcher{parent: s, platform: "mg"}).Search(context.Background(), "甲", 1, 20)
	if len(got) != 1 || got[0].Source != "mg" {
		t.Fatalf("只该保留本平台的结果：%+v", got)
	}
}

func TestMusicDLSearcherReturnsNilOnSidecarError(t *testing.T) {
	// 搜索层的约定是「返回空 = 这次没搜到」，不能把错误抛出去 ——
	// 那会让整个搜索接口 500，为了少一个平台把整次搜索弄坏。
	m := online.NewMusicDL("http://127.0.0.1:1", t.Logf)
	s := &MusicDLSearcher{m: m, perPage: 10}
	if got := (&musicDLPlatformSearcher{parent: s, platform: "mg"}).Search(context.Background(), "甲", 1, 20); got != nil {
		t.Fatalf("sidecar 连不上时该返回 nil：%+v", got)
	}
}

func TestRegisterPlatformsRegistersEachPlatform(t *testing.T) {
	m := fakeSidecarSearch(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": []any{}, "errors": map[string]any{}})
	})
	s := NewMusicDLSearcher(m, 10)
	s.RegisterPlatforms([]string{"mg", "bq", "bi"})
	t.Cleanup(func() {
		for _, p := range []string{"mg", "bq", "bi"} {
			UnregisterSearcher(p)
		}
	})

	got := ExternalSearcherPlatforms()
	if len(got) != 3 {
		t.Fatalf("三个平台都该注册上，得到 %+v", got)
	}
	for _, p := range []string{"mg", "bq", "bi"} {
		if lookupSearcher(p) == nil {
			t.Fatalf("%s 没注册上", p)
		}
	}
}
