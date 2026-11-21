package intercept

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 在线歌手四条端点（见 vartist.go）的用例。
//
// # 这些用例在钉什么
//
// 一、**「反解不出来就透传」**：歌手虚拟 id 只在自己的表里认得，官方 id 一律
// 反解失败 → 逐字节原样透传。这条是整条链路的安全底线：任何时候最多是
// 「在线歌手没出现」，绝不可能是「官方歌手页被我们改坏」。
//
// 二、**歌手页的曲目与搜索页的曲目同形**：歌手页的每一首都要能取流、取歌词、
// 取封面、被收藏 —— 也就是必须走同一套 trackFromSong → registry → albumTrackVO。
//
// 三、**专辑卡片是「纯专辑锚点」**：歌手页专辑列表没有曲目，点进去要能靠
// albumRefs 反解出专辑身份、再去问原生专辑接口。这条链一旦断，症状就是
// 「歌手页能看到专辑，点进去是空的」。

// artistHitFixture 造一条原生歌手搜索命中。
func artistHitFixture(id, name string) search.ArtistHit {
	return search.ArtistHit{ID: id, Name: name, Cover: "https://cdn.invalid/" + id + ".jpg", AlbumCount: 3, TrackCount: 40}
}

// officialArtistList 造一个「官方歌手搜索」的响应体。
func officialArtistList(rows string, total int) string {
	return `{"code":0,"msg":"","data":{"list":[` + rows + `],"total":` + itoa(total) + `}}`
}

// ── /search/artist ─────────────────────────────────────────────────────

func TestArtistSearchAppendsOnlineAfterOfficial(t *testing.T) {
	h := newHarness(t)
	h.artistHits = []search.ArtistHit{
		artistHitFixture("101", "周杰伦"),
		artistHitFixture("102", "另一位"),
	}
	h.setOfficial("GET /music/api/v1/search/artist", http.StatusOK,
		officialArtistList(`{"guid":"`+officialGuid("a")+`","name":"本地歌手"}`, 1))

	w, handled := h.do(http.MethodGet, "/music/api/v1/search/artist?q=周杰伦", "", nil)
	if !handled {
		t.Fatal("search/artist 该被拦截层认领")
	}
	got := albumNames(albumList(t, w))
	if len(got) != 3 || got[0] != "本地歌手" || got[1] != "周杰伦" || got[2] != "另一位" {
		t.Fatalf("官方歌手必须在前、在线补在后：%v", got)
	}
	if dataOf(t, w)["total"] != float64(3) {
		t.Fatalf("total 该是 1 + 2 = 3，得到 %v", dataOf(t, w)["total"])
	}
}

func TestArtistSearchCardShape(t *testing.T) {
	h := newHarness(t)
	h.artistHits = []search.ArtistHit{artistHitFixture("101", "周杰伦")}
	h.setOfficial("GET /music/api/v1/search/artist", http.StatusOK, officialArtistList("", 0))

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/artist?q=x", "", nil)
	list := albumList(t, w)
	if len(list) != 1 {
		t.Fatalf("该注入 1 位在线歌手，得到 %d", len(list))
	}
	card := list[0]
	guid := asString(card["guid"])
	if !online.IsHex32(guid) {
		t.Fatalf("歌手 guid 该是裸 32hex（点回来靠它反解），得到 %q", guid)
	}
	// coverId 就是歌手虚拟 id：客户端拿它去 /static/cover 取头像。
	if asString(card["coverId"]) != guid {
		t.Fatalf("coverId 该等于歌手 guid，得到 %v", card["coverId"])
	}
	if card["albumCount"] != float64(3) || card["trackCount"] != float64(40) {
		t.Fatalf("专辑数 / 曲目数该透出来：%v / %v", card["albumCount"], card["trackCount"])
	}
	// 登记的必须是「能反解回平台内歌手 id」的那一条。
	a, ok := h.it.artists.Lookup(guid)
	if !ok || a.ArtistID != "101" || a.Platform != "wy" {
		t.Fatalf("歌手表里该能按 guid 反解出 (wy, 101)：%#v %v", a, ok)
	}
}

func TestArtistSearchSkipsNamesAlreadyInOfficial(t *testing.T) {
	h := newHarness(t)
	h.artistHits = []search.ArtistHit{
		artistHitFixture("101", "周杰伦"),
		artistHitFixture("102", "另一位"),
	}
	h.setOfficial("GET /music/api/v1/search/artist", http.StatusOK,
		officialArtistList(`{"guid":"`+officialGuid("a")+`","name":" 周杰伦 "}`, 1))

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/artist?q=x", "", nil)
	got := albumNames(albumList(t, w))
	if len(got) != 2 || got[1] != "另一位" {
		t.Fatalf("官方已有的歌手名不该再注入一遍（空白差异也算同名）：%v", got)
	}
}

func TestArtistSearchPassesThroughUnknownShape(t *testing.T) {
	h := newHarness(t)
	h.artistHits = []search.ArtistHit{artistHitFixture("101", "在线歌手")}

	for _, body := range []string{
		`<html>not json</html>`,
		`{"code":1,"msg":"bad"}`,
		`{"code":0,"data":{"list":"nope"}}`,
		`{"code":0,"data":{"list":[1,2]}}`,
	} {
		h.setOfficialFunc(func(_ *http.Request, _ []byte) *http.Response {
			return jsonResp(http.StatusOK, body)
		})
		w, _ := h.do(http.MethodGet, "/music/api/v1/search/artist?q=x", "", nil)
		if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
			t.Fatalf("形状不认识时必须逐字节透传，输入 %q 得到 %q", body, w.Body.String())
		}
	}
}

func TestArtistSearchNoKeywordPassesThrough(t *testing.T) {
	h := newHarness(t)
	h.artistHits = []search.ArtistHit{artistHitFixture("101", "在线歌手")}
	body := officialArtistList(`{"guid":"`+officialGuid("a")+`","name":"本地"}`, 1)
	h.setOfficial("GET /music/api/v1/search/artist", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/search/artist", "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("没关键词时不该注入（在线那一路是空搜索）：%q", w.Body.String())
	}
}

// ── /artist [/detail] ──────────────────────────────────────────────────

// registerArtist 往歌手表里塞一位歌手，返回它的虚拟 id。
func registerArtist(h *harness, platform, id, name string) string {
	return h.it.artists.Put(online.Artist{Platform: platform, ArtistID: id, Name: name})
}

func TestArtistDetailReturnsNativeInfo(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "周杰伦")
	h.artistInfo = search.ArtistInfo{
		ID: "101", Name: "周杰伦", Cover: "https://cdn.invalid/a.jpg",
		BriefDesc: "简介", AlbumCount: 7, TrackCount: 88,
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/artist?guid="+fake, "", nil)
	d := dataOf(t, w)
	if asString(d["guid"]) != fake || asString(d["id"]) != "101" || asString(d["name"]) != "周杰伦" {
		t.Fatalf("歌手详情身份不对：%#v", d)
	}
	if asString(d["cover"]) != "https://cdn.invalid/a.jpg" || asString(d["coverId"]) != fake {
		t.Fatalf("封面字段该填（coverId 是歌手虚拟 id）：%v / %v", d["cover"], d["coverId"])
	}
	if d["albumCount"] != float64(7) || d["trackCount"] != float64(88) || asString(d["briefDesc"]) != "简介" {
		t.Fatalf("专辑数 / 曲目数 / 简介该透出来：%#v", d)
	}
}

func TestArtistDetailAcceptsSuffixedGUID(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	h.artistInfo = search.ArtistInfo{ID: "101", Name: "甲"}

	// 客户端有可能把 `<32hex>:artist` 原样带回来（与其它虚拟 id 的习惯一致）。
	w, _ := h.do(http.MethodGet, "/music/api/v1/artist?guid="+fake+":artist", "", nil)
	if asString(dataOf(t, w)["name"]) != "甲" {
		t.Fatalf("带 :artist 后缀也该反解出来：%s", w.Body.String())
	}
}

func TestArtistDetailPassesThroughUnknownGUID(t *testing.T) {
	h := newHarness(t)
	body := `{"code":0,"msg":"","data":{"guid":"` + officialGuid("b") + `","name":"官方歌手"}}`
	h.setOfficial("GET /music/api/v1/artist", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/artist?guid="+officialGuid("b"), "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("认不出的歌手 id 必须原样透传官方：%q", w.Body.String())
	}
}

func TestArtistDetailPassesThroughWhenPlatformFails(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	h.artistErr = errors.New("boom")
	body := `{"code":0,"data":{"name":"官方"}}`
	h.setOfficial("GET /music/api/v1/artist", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/artist?guid="+fake, "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("平台拿不到详情时该透传（而不是回空壳）：%q", w.Body.String())
	}
}

func TestArtistDetailPassesThroughEmptyName(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	h.artistInfo = search.ArtistInfo{ID: "101"} // 名字空 = 视为没拿到
	body := `{"code":0,"data":{"name":"官方"}}`
	h.setOfficial("GET /music/api/v1/artist", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/artist?guid="+fake, "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("名字为空时该透传：%q", w.Body.String())
	}
}

// ── /track/artist-detail/list ──────────────────────────────────────────

func TestArtistTrackListReturnsPlayableTracks(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "周杰伦")
	h.artistSongs = []search.UnifiedSong{
		{ID: "1", Songmid: "1", Name: "第一首", Singer: "周杰伦", Album: "专辑一", Duration: 200, AlbumID: "900"},
		{ID: "2", Songmid: "2", Name: "第二首", Singer: "周杰伦", Album: "专辑二", Duration: 210},
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/artist-detail/list?artistGUID="+fake, "", nil)
	d := dataOf(t, w)
	if d["total"] != float64(2) {
		t.Fatalf("total 该是 2，得到 %v", d["total"])
	}
	list := albumList(t, w)
	if len(list) != 2 || asString(list[0]["name"]) != "第一首" {
		t.Fatalf("曲目列表不对：%v", albumNames(list))
	}
	// 歌手页的曲目必须**能点**：guid 是 32hex，且能反解回原曲目（取流 / 取歌词都靠它）。
	guid := asString(list[0]["guid"])
	if !online.IsHex32(guid) {
		t.Fatalf("曲目 guid 该是 32hex，得到 %q", guid)
	}
	if tv, ok := h.it.registry.Lookup(guid); !ok || tv.PlatformID != "1" {
		t.Fatalf("歌手页的曲目必须登记进曲目表（否则点了没反应）：%#v %v", tv, ok)
	}
	// 原生专辑 id 要跟着进来 —— 专辑页靠它要完整曲目。
	if tv, _ := h.it.registry.Lookup(guid); tv.AlbumID != "900" {
		t.Fatalf("曲目该带上平台专辑 id，得到 %q", tv.AlbumID)
	}
}

func TestArtistTrackListSortMapping(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	var seen []string
	h.it.cfg.ArtistTracks = func(_ context.Context, _ string, offset, limit int, order string) ([]search.UnifiedSong, int, error) {
		seen = append(seen, order)
		if offset != 20 || limit != 20 {
			t.Fatalf("分页该是 page=2,size=20 → offset=20,limit=20，得到 %d/%d", offset, limit)
		}
		return nil, 0, nil
	}
	for _, tc := range []struct{ param, want string }{
		{"", "hot"},
		{"hot", "hot"},
		{"createdAt", "time"},
		{"time", "time"},
		{"new", "time"},
	} {
		seen = nil
		h.do(http.MethodGet, "/music/api/v1/track/artist-detail/list?artistGUID="+fake+"&page=2&size=20&sort="+tc.param, "", nil)
		if len(seen) != 1 || seen[0] != tc.want {
			t.Fatalf("sort=%q 该映射成 %q，得到 %v", tc.param, tc.want, seen)
		}
	}
}

func TestArtistTrackListClampsSize(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	var got int
	h.it.cfg.ArtistTracks = func(_ context.Context, _ string, _, limit int, _ string) ([]search.UnifiedSong, int, error) {
		got = limit
		return nil, 0, nil
	}
	for _, tc := range []struct {
		size string
		want int
	}{{"", 50}, {"0", 50}, {"1000", 50}, {"100", 100}, {"7", 7}} {
		got = 0
		h.do(http.MethodGet, "/music/api/v1/track/artist-detail/list?artistGUID="+fake+"&size="+tc.size, "", nil)
		if got != tc.want {
			t.Fatalf("size=%q 该被夹到 %d，得到 %d", tc.size, tc.want, got)
		}
	}
}

func TestArtistTrackListPlatformErrorReturnsEmptyList(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "甲")
	h.artistErr = errors.New("boom")

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/artist-detail/list?artistGUID="+fake, "", nil)
	d := dataOf(t, w)
	if d["total"] != float64(0) {
		t.Fatalf("平台出错该回空列表 + total 0，得到 %v", d["total"])
	}
	if list, ok := d["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("list 该是空数组（不是 null）：%#v", d["list"])
	}
}

func TestArtistTrackListPassesThroughUnknownArtist(t *testing.T) {
	h := newHarness(t)
	body := `{"code":0,"data":{"list":[],"total":0}}`
	h.setOfficial("GET /music/api/v1/track/artist-detail/list", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/track/artist-detail/list?artistGUID="+officialGuid("c"), "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("认不出的歌手该透传：%q", w.Body.String())
	}
}

// ── /album/artist-detail/list ──────────────────────────────────────────

func TestArtistAlbumListRegistersAlbumRefs(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "周杰伦")
	h.artistAlbums = []search.AlbumHit{
		{ID: "900", Name: "专辑一", Cover: "https://cdn.invalid/al.jpg", Artist: "周杰伦", Size: 12, Year: 2004},
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/album/artist-detail/list?artistGUID="+fake, "", nil)
	list := albumList(t, w)
	if len(list) != 1 {
		t.Fatalf("该有 1 张专辑，得到 %d", len(list))
	}
	card := list[0]
	guid := asString(card["guid"])
	if !online.IsHex32(guid) {
		t.Fatalf("专辑 guid 该是 32hex，得到 %q", guid)
	}
	if card["trackCount"] != float64(12) || card["releaseDate"] != nil || card["barcode"] != nil {
		t.Fatalf("专辑卡片形状不对：%#v", card)
	}
	arts, _ := card["artists"].([]any)
	if len(arts) != 1 || asString(arts[0].(map[string]any)["guid"]) != fake {
		t.Fatalf("卡片上的歌手 guid 该是歌手虚拟 id（可点）：%#v", card["artists"])
	}
	ref, ok := h.it.albumRefs.Lookup(guid)
	if !ok || ref.AlbumID != "900" || ref.Platform != "wy" || ref.TrackCount != 12 {
		t.Fatalf("专辑身份必须登记（点进去要靠它问原生接口）：%#v %v", ref, ok)
	}
}

func TestArtistAlbumListPassesThroughUnknownArtist(t *testing.T) {
	h := newHarness(t)
	body := `{"code":0,"data":{"list":[],"total":0}}`
	h.setOfficial("GET /music/api/v1/album/artist-detail/list", http.StatusOK, body)

	w, _ := h.do(http.MethodGet, "/music/api/v1/album/artist-detail/list?artistGUID="+officialGuid("d"), "", nil)
	if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(body) {
		t.Fatalf("认不出的歌手该透传：%q", w.Body.String())
	}
}

// 歌手页专辑卡片 → 点进专辑页 → 原生接口给完整曲目。这条是「歌手的专辑」这条
// 路能不能真的用起来的判据（卡片没有曲目，全靠 albumRefs 反解）。
func TestArtistAlbumCardOpensNativeAlbum(t *testing.T) {
	h := newHarness(t)
	fake := registerArtist(h, "wy", "101", "周杰伦")
	h.artistAlbums = []search.AlbumHit{
		{ID: "900", Name: "专辑一", Artist: "周杰伦", Size: 2},
	}
	h.albumDetail = search.AlbumDetail{
		ID: "900", Name: "专辑一", Artist: "周杰伦",
		Tracks: []search.UnifiedSong{
			{ID: "1", Songmid: "1", Name: "第一首", Singer: "周杰伦", Album: "专辑一", Duration: 200, AlbumID: "900"},
			{ID: "2", Songmid: "2", Name: "第二首", Singer: "周杰伦", Album: "专辑一", Duration: 210, AlbumID: "900"},
		},
	}

	w, _ := h.do(http.MethodGet, "/music/api/v1/album/artist-detail/list?artistGUID="+fake, "", nil)
	albumFake := asString(albumList(t, w)[0]["guid"])

	// 点进去：专辑详情必须认领（而不是透传给官方 → 官方没有这张在线专辑）。
	h.setOfficialFunc(func(_ *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusNotFound, `{"code":404,"msg":"not found"}`)
	})
	w, _ = h.do(http.MethodGet, "/music/api/v1/album?guid="+albumFake, "", nil)
	d := dataOf(t, w)
	if asString(d["name"]) != "专辑一" {
		t.Fatalf("专辑详情该认领并回这张在线专辑：%s", w.Body.String())
	}
	if d["trackCount"] != float64(2) {
		t.Fatalf("曲目数该来自原生专辑接口（2 首），得到 %v", d["trackCount"])
	}
	tracks, _ := d["tracks"].([]any)
	if len(tracks) != 2 || asString(tracks[1].(map[string]any)["name"]) != "第二首" {
		t.Fatalf("曲目列表该是原生接口的完整曲目：%#v", d["tracks"])
	}
}

// ── 封面兜底：歌手头像 / 专辑封面 ──────────────────────────────────────

func TestEntityCoverTargetResolvesArtistAndAlbumRef(t *testing.T) {
	h := newHarness(t)
	artistFake := h.it.artists.Put(online.Artist{Platform: "wy", ArtistID: "101", Name: "甲", CoverURL: "https://cdn.invalid/artist.jpg"})
	albumFake := h.it.albumRefs.Put(online.AlbumRef{Platform: "wy", AlbumID: "900", Name: "专辑一", CoverURL: "https://cdn.invalid/album.jpg"})

	for _, tc := range []struct{ form, want string }{
		{artistFake, "https://cdn.invalid/artist.jpg"},
		{artistFake + ":artist", "https://cdn.invalid/artist.jpg"},
		{albumFake, "https://cdn.invalid/album.jpg"},
		{albumFake + ":album", "https://cdn.invalid/album.jpg"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/music/api/v1/static/cover?coverId="+tc.form, nil)
		coverURL, cacheKey, logID, ok := h.it.entityCoverTarget(r, "")
		if !ok {
			t.Fatalf("%s 该能反解出封面地址", tc.form)
		}
		if coverURL != tc.want {
			t.Fatalf("%s 该解出 %q，得到 %q", tc.form, tc.want, coverURL)
		}
		if cacheKey == "" || logID == "" {
			t.Fatalf("缓存键与日志 id 都不该为空：%q / %q", cacheKey, logID)
		}
	}
}

// 「认不出就透传」：官方曲目 / 官方歌手 id 一律不受影响。
func TestEntityCoverTargetIgnoresUnknownIDs(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest(http.MethodGet, "/music/api/v1/static/cover?coverId="+officialGuid("e"), nil)
	if _, _, _, ok := h.it.entityCoverTarget(r, ""); ok {
		t.Fatal("认不出的 id 不该被封面兜底认领（该透传官方）")
	}
}
