package account

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 这一组测的是 **v8 优先 + 老接口兜底** 的调度逻辑。
//
// 背景（2026-09-21）：老接口 `qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg`
// 对匿名请求只返回 `{"code":0,"subcode":4000,"msg":"check privacy error!"}`，
// 歌单详情直接 500。v8 接口匿名可读，所以改成先走 v8。

const v8Path = "/v8/fcg-bin/fcg_v8_playlist_cp.fcg"
const legacyPath = "/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg"

// v8 成功时**不应该**再去打老接口 —— 否则每次请求都白多一次出站。
func TestQQPlaylistDetailPrefersV8(t *testing.T) {
	legacyCalled := false
	fakeQQ(t, map[string]http.HandlerFunc{
		v8Path: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("id") != "7707261125" {
				t.Errorf("v8 应带上 id 参数，实际 %q", r.URL.Query().Get("id"))
			}
			// ⚠️ v8 的 cdlist 在 `data` 下，且曲目字段是 name / mid（不是 songname / songmid）
			payload := map[string]any{
				"code": 0,
				"data": map[string]any{
					"cdlist": []any{map[string]any{
						"disstid":  "7707261125",
						"dissname": "新接口歌单",
						"desc":     "新接口描述",
						"logo":     "https://a/cover.jpg",
						"nickname": "新接口创建者",
						"songlist": []any{
							map[string]any{
								"name": "歌一", "mid": "s1", "interval": float64(180),
								"singer": []any{map[string]any{"name": "甲"}},
								"album":  map[string]any{"name": "专一", "mid": "a1"},
							},
							map[string]any{"name": "缺 mid 应被丢弃", "mid": ""},
						},
					}},
				},
			}
			raw, _ := json.Marshal(payload)
			_, _ = w.Write(raw)
		},
		legacyPath: func(w http.ResponseWriter, _ *http.Request) {
			legacyCalled = true
			_, _ = w.Write([]byte(`jsonCallback({"cdlist":[]});`))
		},
	})

	detail, err := QQPlaylistDetail(loggedInCookies(), "7707261125")
	if err != nil {
		t.Fatalf("v8 成功时不该失败: %v", err)
	}
	if legacyCalled {
		t.Error("v8 已经拿到数据，不该再去打老接口")
	}
	if detail["title"] != "新接口歌单" {
		t.Errorf("title = %v，应为「新接口歌单」", detail["title"])
	}
	if detail["owner"] != "新接口创建者" {
		t.Errorf("owner = %v，应为「新接口创建者」", detail["owner"])
	}
	if detail["cover_url"] != "https://a/cover.jpg" {
		t.Errorf("cover_url = %v", detail["cover_url"])
	}
	items, _ := detail["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("缺 mid 的曲目应被丢弃，实际剩 %d 条", len(items))
	}
	if items[0]["song_id"] != "s1" || items[0]["duration_ms"] != 180000 {
		t.Errorf("曲目解析不对: %v", items[0])
	}
}

// v8 挂了（老版本上游 / 网络问题）必须还能用老接口 —— 这是保留老接口的意义。
func TestQQPlaylistDetailFallsBackToLegacy(t *testing.T) {
	// 故意不给 v8 桩：fakeQQ 会对未注册路径返回 404
	fakeQQ(t, map[string]http.HandlerFunc{
		legacyPath: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`jsonCallback({"cdlist":[{"dissname":"老接口歌单",` +
				`"songlist":[{"songname":"歌一","songmid":"s1","interval":180}]}]});`))
		},
	})

	detail, err := QQPlaylistDetail(loggedInCookies(), "999")
	if err != nil {
		t.Fatalf("v8 失败时应回落到老接口，实际报错: %v", err)
	}
	if detail["title"] != "老接口歌单" {
		t.Errorf("title = %v，应为「老接口歌单」", detail["title"])
	}
	items, _ := detail["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("老接口曲目没解析出来，实际 %d 条", len(items))
	}
}

// 两个都失败时，错误信息要能看出「两条路都试过了」，否则排查时不知道是谁的问题。
func TestQQPlaylistDetailBothPathsFailMentionsBoth(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{})

	_, err := QQPlaylistDetail(loggedInCookies(), "999")
	if err == nil {
		t.Fatal("两条路都失败时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "备用接口") {
		t.Errorf("错误信息应说明备用接口也失败了，实际：%q", msg)
	}
}
