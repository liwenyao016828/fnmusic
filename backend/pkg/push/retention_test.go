package push

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"fn-lx-player/pkg/fnos"
)

// recordingRemovals 起一个假服务，记录所有被要求移除的 guid。
func recordingRemovals(t *testing.T) (*fnos.Music, *[]string) {
	t.Helper()
	var got []string

	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/remove-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = append(got, body.TrackGUIDs...)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})
	return m, &got
}

// quietMusic 一个什么都不做的假服务（默认 handler 返回 code 0）。
func quietMusic(t *testing.T) *fnos.Music {
	t.Helper()
	return fakeMusic(t, nil)
}

func TestPruneExpiredNoopWhenDisabled(t *testing.T) {
	m, got := recordingRemovals(t)

	pushed := []PushedTrack{{GUID: "old", AddedAt: time.Now().AddDate(0, 0, -30).Unix()}}

	removed, kept, err := PruneExpired(m, "pl-1", pushed, 0)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("days=0 时不该移除任何曲目，实际 %v", removed)
	}
	if len(kept) != 1 {
		t.Errorf("记录应原样保留，实际 %d 条", len(kept))
	}
	if len(*got) != 0 {
		t.Errorf("不该调用 remove-track，实际收到 %v", *got)
	}
}

// ⚠️ 最关键的一条：清理**只**能删我们自己推送过的曲目。
//
// 歌单里可能有用户手动加的歌。PruneExpired 只接收 pushed 清单、
// 从不读取歌单内容，因此只要断言「发出去的 guid 全部来自 pushed」，
// 就证明了不会误伤用户手动加的曲目。
func TestPruneExpiredOnlyRemovesOwnTracks(t *testing.T) {
	m, got := recordingRemovals(t)

	old := time.Now().AddDate(0, 0, -30).Unix()
	fresh := time.Now().AddDate(0, 0, -1).Unix()

	// 我们推送过的：两条超期、一条还新鲜
	pushed := []PushedTrack{
		{GUID: "mine-old-1", AddedAt: old},
		{GUID: "mine-old-2", AddedAt: old},
		{GUID: "mine-fresh", AddedAt: fresh},
	}
	// 歌单里另有 "user-added-1" / "user-added-2"（用户手动加的），
	// 它们**不在** pushed 里，因此不可能出现在移除请求中。

	removed, kept, err := PruneExpired(m, "pl-1", pushed, 7)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}

	if len(removed) != 2 {
		t.Fatalf("应移除 2 首超期曲目，实际 %d 首: %v", len(removed), removed)
	}
	if len(*got) != 2 {
		t.Fatalf("应只发出 2 个 guid，实际 %d 个: %v", len(*got), *got)
	}

	own := map[string]bool{"mine-old-1": true, "mine-old-2": true, "mine-fresh": true}
	for _, g := range *got {
		if !own[g] {
			t.Errorf("移除了不属于本任务的曲目 %q —— 会误删用户手动加的歌", g)
		}
		if g == "mine-fresh" {
			t.Error("保留期内的曲目被误删")
		}
	}

	if len(kept) != 1 || kept[0].GUID != "mine-fresh" {
		t.Errorf("kept 应只剩 mine-fresh，实际 %+v", kept)
	}
}

func TestPruneExpiredKeepsRecordsOnFailure(t *testing.T) {
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/remove-track": jsonHandler(map[string]any{
			"code": 500, "msg": "INTERNAL", "data": nil,
		}),
	})

	old := time.Now().AddDate(0, 0, -30).Unix()
	pushed := []PushedTrack{
		{GUID: "a", AddedAt: old},
		{GUID: "b", AddedAt: old},
	}

	removed, kept, err := PruneExpired(m, "pl-1", pushed, 7)
	if err == nil {
		t.Fatal("接口失败时应报错")
	}
	if len(removed) != 0 {
		t.Errorf("失败时不该有成功移除，实际 %v", removed)
	}
	// 关键：失败不能把记录丢掉，否则下轮就再也清理不到了
	if len(kept) != 2 {
		t.Errorf("失败时记录应原样保留待重试，实际 %d 条", len(kept))
	}
}

// AddedAt 缺失（老数据）时按「不超期」处理 —— 宁可留着也不误删
func TestPruneExpiredSkipsRecordsWithoutTimestamp(t *testing.T) {
	m := quietMusic(t)

	removed, kept, err := PruneExpired(m, "pl-1", []PushedTrack{{GUID: "no-time", AddedAt: 0}}, 7)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("没有时间戳的记录不该被删，实际 %v", removed)
	}
	if len(kept) != 1 {
		t.Errorf("记录应保留，实际 %d 条", len(kept))
	}
}

func TestNormalizeRetentionMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"days", RetentionDays},
		{"keep", RetentionKeep},
		{"", RetentionKeep},
		{"bogus", RetentionKeep}, // 非法值退回安全默认
		{"DAYS", RetentionKeep},  // 大小写敏感，不认识的当 keep
	}
	for _, c := range cases {
		if got := NormalizeRetentionMode(c.in); got != c.want {
			t.Errorf("NormalizeRetentionMode(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestMergePushed(t *testing.T) {
	base := []PushedTrack{
		{GUID: "a", AddedAt: 100},
		{GUID: "b", AddedAt: 200},
	}

	// 新增 c；重复推送 a 时刷新时间而不是追加
	got := MergePushed(base, []PushedTrack{
		{GUID: "c", AddedAt: 300},
		{GUID: "a", AddedAt: 999},
	})

	if len(got) != 3 {
		t.Fatalf("应得 3 条（去重后），实际 %d 条: %+v", len(got), got)
	}
	if got[0].GUID != "a" || got[0].AddedAt != 999 {
		t.Errorf("重复推送应刷新时间戳，实际 %+v", got[0])
	}
	if got[2].GUID != "c" {
		t.Errorf("新曲目应追加在末尾，实际 %+v", got[2])
	}

	if same := MergePushed(base, nil); len(same) != 2 {
		t.Errorf("added 为空时不该改动，实际 %d 条", len(same))
	}
	if got := MergePushed(nil, []PushedTrack{{GUID: ""}}); len(got) != 0 {
		t.Errorf("空 guid 应被忽略，实际 %d 条", len(got))
	}
}

// 端到端：带保留期的推送应把超期曲目移除，并把结果反映在 Result 上
func TestPushTracksAppliesRetention(t *testing.T) {
	old := time.Now().AddDate(0, 0, -30).Unix()

	var removed []string
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "pl-1", "name": "日推"},
		})),
		"/music/api/v1/search/track": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{
				{"guid": "new-1", "title": r.URL.Query().Get("q"), "duration": 200},
			}))
		},
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track":         jsonHandler(okBody(nil)),
		"/music/api/v1/playlist/remove-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			removed = append(removed, body.TrackGUIDs...)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{{Name: "新歌"}}, Options{
		Title:         "日推",
		RetentionMode: RetentionDays,
		RetentionDays: 7,
		Pushed:        []PushedTrack{{GUID: "stale-1", AddedAt: old}},
	})
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}

	if res.Expired != 1 {
		t.Errorf("应清理 1 首超期曲目，实际 %d", res.Expired)
	}
	if len(removed) != 1 || removed[0] != "stale-1" {
		t.Errorf("应移除 stale-1，实际 %v", removed)
	}
	// 清单里应只剩本轮新推的那首（stale 已清掉）
	if len(res.Pushed) != 1 || res.Pushed[0].GUID != "new-1" {
		t.Errorf("推送清单应只剩 new-1，实际 %+v", res.Pushed)
	}
}

// keep 模式（默认）下不该有任何移除动作
func TestPushTracksRetentionKeepByDefault(t *testing.T) {
	old := time.Now().AddDate(0, 0, -30).Unix()

	var removed []string
	m := fakeMusic(t, map[string]http.HandlerFunc{
		"/music/api/v1/playlist/list": jsonHandler(okBody([]map[string]any{
			{"guid": "pl-1", "name": "日推"},
		})),
		"/music/api/v1/search/track": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(okBody([]map[string]any{
				{"guid": "new-1", "title": r.URL.Query().Get("q"), "duration": 200},
			}))
		},
		"/music/api/v1/track/playlist-detail/list": jsonHandler(okBody([]map[string]any{})),
		"/music/api/v1/playlist/add-track":         jsonHandler(okBody(nil)),
		"/music/api/v1/playlist/remove-track": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TrackGUIDs []string `json:"trackGUIDs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			removed = append(removed, body.TrackGUIDs...)
			_ = json.NewEncoder(w).Encode(okBody(nil))
		},
	})

	res, err := PushTracks(m, []Track{{Name: "新歌"}}, Options{
		Title:  "日推",
		Pushed: []PushedTrack{{GUID: "stale-1", AddedAt: old}},
	})
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	if res.Expired != 0 || len(removed) != 0 {
		t.Errorf("默认 keep 模式不该移除任何曲目，实际 expired=%d removed=%v", res.Expired, removed)
	}
	// 清单应保留旧的 + 新的
	if len(res.Pushed) != 2 {
		t.Errorf("清单应保留 2 条，实际 %+v", res.Pushed)
	}
}
