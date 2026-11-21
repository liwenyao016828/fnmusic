package intercept

import (
	"net/http"
	"strings"
	"testing"

	"fn-lx-player/pkg/online"
)

// 订阅推送写进共享桶的在线条目，要能在歌单曲目列表里看到
// （飞牛页面显示完整歌单，用户 2026-11-21 的要求）。
func TestSharedPlaylistTracksMergedIntoTrackList(t *testing.T) {
	h := newHarness(t)
	// 官方歌单 1 首
	h.setOfficialFunc(func(r *http.Request, _ []byte) *http.Response {
		if r.URL.Path != "/music/api/v1/track/playlist-detail/list" {
			return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[],"total":0}}`)
		}
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":{"list":[{"guid":"off-1","title":"曲库里的","artist":"A"}],"total":1}}`)
	})

	// 后台推送写共享桶（没有 HTTP 上下文）
	tr := online.Track{Platform: "wy", PlatformID: "s1", Title: "在线的那首", Artists: []string{"B"}}
	n, err := h.it.AddSharedPlaylistTracks("pl", []online.Track{tr})
	if err != nil || n != 1 {
		t.Fatalf("AddShared = %d, %v; want 1, nil", n, err)
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/playlist-detail/list?playlistGUID=pl&page=1&size=-1", "", nil)
	d := dataOf(t, w)
	list := d["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("全量应返回 2 条（官方 1 + 共享 1），得到 %d: %v", len(list), list)
	}
	if d["total"] != float64(2) {
		t.Fatalf("total = %v; want 2", d["total"])
	}
	if list[0].(map[string]any)["title"] != "曲库里的" {
		t.Fatalf("官方曲目应在前: %v", list[0])
	}
	if !strings.Contains(list[1].(map[string]any)["title"].(string), "在线的那首") {
		t.Fatalf("共享在线曲目应在后: %v", list[1])
	}
}

// 用户桶与共享桶都有的曲目只显示一次（按 guid 去重，用户段优先）。
func TestMergeOnlinePlaylistItemsDedupsByGUID(t *testing.T) {
	tr := online.Track{Platform: "wy", PlatformID: "s1", Title: "在线", Artists: []string{"A"}}
	guid := tr.FakeID()
	user := []online.PlaylistItem{{GUID: guid, Track: tr}}
	shared := []online.PlaylistItem{{GUID: guid, Track: tr}, {GUID: "other", Track: online.Track{Platform: "wy", PlatformID: "s2", Title: "另一首"}}}
	got := mergeOnlinePlaylistItems(user, shared)
	if len(got) != 2 {
		t.Fatalf("merge = %d; want 2", len(got))
	}
	if got[0].GUID != guid || got[1].GUID != "other" {
		t.Fatalf("merge order = %+v", got)
	}
}

// 删除歌单时共享桶要级联清掉，不然下次建同名歌单会冒出旧曲目。
func TestPlaylistDeleteDropsSharedBucket(t *testing.T) {
	h := newHarness(t)
	tr := online.Track{Platform: "wy", PlatformID: "s1", Title: "在线", Artists: []string{"A"}}
	if _, err := h.it.AddSharedPlaylistTracks("pl", []online.Track{tr}); err != nil {
		t.Fatal(err)
	}
	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/delete", `{"guid":"pl"}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("delete 失败: %v", env)
	}
	if got := h.it.store.SharedPlaylistTracks("pl"); len(got) != 0 {
		t.Fatalf("共享桶未级联清理: %d 条", len(got))
	}
}
