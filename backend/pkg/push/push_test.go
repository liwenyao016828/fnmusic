package push

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fn-lx-player/pkg/fnos"
)

// fakeMusic 起一个假飞牛音乐服务，返回可直接用于 PushTracks 的客户端。
// handlers 按路径匹配；未命中返回 {"code":0,"data":{}}。
func fakeMusic(t *testing.T, handlers map[string]http.HandlerFunc) *fnos.Music {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "music.sock")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": map[string]any{}})
	})

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("无法创建 Unix socket，跳过: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return fnos.NewMusicAt("test-token", sock, "http://localhost/music/api/v1")
}

func okBody(data any) map[string]any {
	return map[string]any{"code": 0, "msg": "", "data": data}
}

func jsonHandler(payload map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}
}

// 基础链路：按名称找到歌单 → 匹配曲目 → 加曲
func TestPushTracksMatchesAndAdds(t *testing.T) {
	var added []string

	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "pl-1", "name": "我的歌单"},
		})),
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦", "duration": 269},
		})),
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				GUID       string   `json:"guid"`
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.GUID != "pl-1" {
				t.Errorf("guid = %q", body.GUID)
			}
			added = append(added, body.TrackGUIDs...)
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{
		{Name: "晴天", Artist: "周杰伦", Duration: 269},
	}, Options{Title: "我的歌单"})
	if err != nil {
		t.Fatalf("PushTracks 失败: %v", err)
	}

	if res.GUID != "pl-1" {
		t.Errorf("应复用同名歌单，实际 guid=%q", res.GUID)
	}
	if res.CreatedPlaylist {
		t.Error("歌单已存在，不应标记为新建")
	}
	if res.Total != 1 || res.Matched != 1 || res.Added != 1 {
		t.Fatalf("统计不对: %+v", res)
	}
	if len(added) != 1 || added[0] != "t-1" {
		t.Fatalf("加曲内容不对: %v", added)
	}
}

// 匹配不上的曲目应进 missing，且不写入
func TestPushTracksReportsMissing(t *testing.T) {
	addCalled := false
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{{"guid": "pl-1", "name": "目标"}})),
		// 曲库返回完全不相干的歌
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "x-1", "title": "完全不相干的歌名", "artist": "另一个人"},
		})),
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track": func(w http.ResponseWriter, _ *http.Request) {
			addCalled = true
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{{Name: "晴天", Artist: "周杰伦"}}, Options{Title: "目标"})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if res.Matched != 0 || res.Added != 0 {
		t.Fatalf("不应匹配到任何曲目: %+v", res)
	}
	if len(res.Missing) != 1 {
		t.Fatalf("应报告 1 条未命中，实际 %d", len(res.Missing))
	}
	if res.Missing[0].Reason == "" {
		t.Error("未命中应给出原因")
	}
	if addCalled {
		t.Error("无匹配时不应调用加曲接口")
	}
}

// 干跑不写入
func TestPushTracksDryRunDoesNotWrite(t *testing.T) {
	wrote := false
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{{"guid": "pl-1", "name": "目标"}})),
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦"},
		})),
		"/music/api/v1/track/playlist-detail/list": func(w http.ResponseWriter, _ *http.Request) {
			wrote = true
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{}))
		},
		"/music/api/v1/playlist/add-track": func(w http.ResponseWriter, _ *http.Request) {
			wrote = true
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{{Name: "晴天", Artist: "周杰伦"}}, Options{Title: "目标", DryRun: true})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if !res.DryRun {
		t.Error("应标记 DryRun")
	}
	if res.Matched != 1 {
		t.Errorf("干跑也应报告匹配数，实际 %d", res.Matched)
	}
	if wrote {
		t.Error("干跑不应触碰写入接口")
	}
}

// 歌单里已有的曲目应跳过，不重复添加
func TestPushTracksSkipsAlreadyPresent(t *testing.T) {
	var added []string
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{{"guid": "pl-1", "name": "目标"}})),
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦"},
		})),
		// 歌单里已经有 t-1
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{{"guid": "t-1"}})),
		"/music/api/v1/playlist/add-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			added = append(added, body.TrackGUIDs...)
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{{Name: "晴天", Artist: "周杰伦"}}, Options{Title: "目标"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if res.AlreadyPresent != 1 || res.Added != 0 {
		t.Fatalf("应识别为已存在: %+v", res)
	}
	if len(added) != 0 {
		t.Fatalf("不应重复加曲，实际 %v", added)
	}
}

// 同一首被多次命中时只加一次
func TestPushTracksDeduplicates(t *testing.T) {
	var added []string
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{{"guid": "pl-1", "name": "目标"}})),
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦"},
		})),
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			added = append(added, body.TrackGUIDs...)
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	// 三条不同写法但都指向同一首
	res, err := PushTracks(m, []Track{
		{Name: "晴天", Artist: "周杰伦"},
		{Name: "晴天", Artist: "周杰伦 "},
		{Name: "晴天", Artist: "周杰伦"},
	}, Options{Title: "目标"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if res.Matched != 1 {
		t.Errorf("去重后 matched 应为 1，实际 %d", res.Matched)
	}
	if len(added) != 1 {
		t.Fatalf("只应加一次，实际 %v", added)
	}
}

// 用 fnos_guid 时不再按名称查找（稳定身份）
func TestPushTracksUsesProvidedGUID(t *testing.T) {
	listCalled := false
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": func(w http.ResponseWriter, _ *http.Request) {
			listCalled = true
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{}))
		},
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦"},
		})),
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
	})

	res, err := PushTracks(m, []Track{{Name: "晴天", Artist: "周杰伦"}},
		Options{Title: "随便什么名字", FnosGUID: "fixed-guid"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if res.GUID != "fixed-guid" {
		t.Errorf("应使用传入的 guid，实际 %q", res.GUID)
	}
	if listCalled {
		t.Error("传了 guid 就不该再按名称查找歌单")
	}
}

// 没有同名歌单时应新建
func TestPushTracksCreatesPlaylistWhenAbsent(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list":   jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/create": jsonHandler(okBody(map[string]any{"guid": "new-pl"})),
		"/music/api/v1/search/track": jsonHandler(okBody([]map[string]any{
			{"guid": "t-1", "title": "晴天", "artist": "周杰伦"},
		})),
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
	})

	res, err := PushTracks(m, []Track{{Name: "晴天", Artist: "周杰伦"}}, Options{Title: "新歌单"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if res.GUID != "new-pl" || !res.CreatedPlaylist {
		t.Fatalf("应新建歌单: %+v", res)
	}
}

// 同名多个歌单时应报错，不随便挑一个
func TestPushTracksRejectsAmbiguousName(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "d1", "name": "重名"}, {"guid": "d2", "name": "重名"},
		})),
	})
	_, err := PushTracks(m, []Track{{Name: "晴天"}}, Options{Title: "重名"})
	if err == nil {
		t.Fatal("同名多个应报错")
	}
	if !strings.Contains(err.Error(), "重名") {
		t.Errorf("错误信息应说明重名，实际 %v", err)
	}
}

func TestPushTracksValidation(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{})

	if _, err := PushTracks(m, []Track{{Name: "x"}}, Options{}); err == nil {
		t.Error("缺 title 应报错")
	}
	if _, err := PushTracks(m, nil, Options{Title: "t"}); err == nil {
		t.Error("缺曲目应报错")
	}
}

// 上游返回非 0 码时应把服务端消息带出来
func TestPushTracksPropagatesUpstreamError(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(map[string]any{
			"code": 401, "msg": "INVALID TOKEN", "data": nil,
		}),
	})
	_, err := PushTracks(m, []Track{{Name: "x"}}, Options{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "INVALID TOKEN") {
		t.Fatalf("应带出服务端消息，实际 %v", err)
	}
}

// 匹配阶段应并发执行且并发有界 —— 大歌单不该逐首串行打 NAS。
//
// 用一个会记录「同时在飞的请求数」的假服务来验证：
// 既要有重叠（证明真的并发了），又不能超过 matchConcurrency（证明有界）。
func TestPushTracksMatchesConcurrently(t *testing.T) {
	const n = 30

	var inFlight, maxInFlight int32

	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "pl-1", "name": "并发测试"},
		})),
		"/music/api/v1/search/track": func(w http.ResponseWriter, r *http.Request) {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			// 记录峰值
			for {
				peak := atomic.LoadInt32(&maxInFlight)
				if cur <= peak || atomic.CompareAndSwapInt32(&maxInFlight, peak, cur) {
					break
				}
			}
			// 停留一下，让并发窗口真实存在
			time.Sleep(15 * time.Millisecond)

			q := r.URL.Query().Get("q")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{
				{"guid": "g-" + q, "title": q, "duration": 200},
			}))
		},
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track":         jsonHandler(okBody(nil)),
	})

	tracks := make([]Track, n)
	for i := range tracks {
		tracks[i] = Track{Name: fmt.Sprintf("song-%02d", i)}
	}

	res, err := PushTracks(m, tracks, Options{Title: "并发测试"})
	if err != nil {
		t.Fatalf("PushTracks 失败: %v", err)
	}

	peak := atomic.LoadInt32(&maxInFlight)
	if peak < 2 {
		t.Errorf("匹配阶段没有并发（峰值 %d），大歌单会退化成串行", peak)
	}
	if peak > matchConcurrency {
		t.Errorf("并发超出上限：峰值 %d > matchConcurrency %d", peak, matchConcurrency)
	}

	// 并发不该破坏「按输入顺序输出」
	if res.Total != n || res.Matched != n {
		t.Fatalf("统计不对: total=%d matched=%d（期望 %d/%d）", res.Total, res.Matched, n, n)
	}
	if len(res.Items) != n {
		t.Fatalf("Items 数量 = %d，期望 %d", len(res.Items), n)
	}
	for i, it := range res.Items {
		want := fmt.Sprintf("g-song-%02d", i)
		if it.FnosTrackGUID != want {
			t.Fatalf("第 %d 项顺序错乱：guid=%q，期望 %q", i, it.FnosTrackGUID, want)
		}
	}
}

// 并发下同一首重复出现时仍应只加一次（去重语义不能被并发破坏）
func TestPushTracksDeduplicatesConcurrently(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "pl-1", "name": "去重"},
		})),
		// 不同关键词都能匹配上（title 回显查询词保证相似度够），但都命中同一个 guid
		"/music/api/v1/search/track": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{
				{"guid": "same-guid", "title": r.URL.Query().Get("q"), "duration": 200},
			}))
		},
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track":         jsonHandler(okBody(nil)),
	})

	tracks := make([]Track, 12)
	for i := range tracks {
		tracks[i] = Track{Name: fmt.Sprintf("重复-%02d", i)}
	}

	res, err := PushTracks(m, tracks, Options{Title: "去重"})
	if err != nil {
		t.Fatalf("PushTracks 失败: %v", err)
	}
	if res.Matched != 1 {
		t.Errorf("同一 guid 应只计 1 次，实际 Matched=%d", res.Matched)
	}
	if res.Added != 1 {
		t.Errorf("同一 guid 应只加 1 次，实际 Added=%d", res.Added)
	}
}
