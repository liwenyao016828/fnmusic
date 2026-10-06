package intercept

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 「每日推荐」的排除集与本地兜底用例。
//
// 这一层改动有两个容易写错的地方，用例重点盯它们：
//  1. **排除的是曲目身份，不是虚拟 id 字符串** —— 同一首歌换个平台就是另一个
//     虚拟 id，只比 id 会把刚听完的歌换个平台又推一遍。
//  2. **本地兜底只补空位** —— 它一抢位置，推荐就从「没听过的新歌」变成
//     「他硬盘上那几首」。

// ── 造数据的小工具 ──────────────────────────────────────────────────────

// dailySharedUser 返回未带凭据的请求对应的用户键（harness 里就是 "shared"）。
func dailySharedUser(t *testing.T, h *harness) string {
	t.Helper()
	return h.it.userKey(mustRequest(t, "/music/api/v1/playlist/detail", "{}"))
}

// recordPlay 往该用户的播放历史里塞一条。
func recordPlay(t *testing.T, h *harness, user string, tr online.Track, at int64) {
	t.Helper()
	if err := h.it.store.RecordPlay(user, online.HistoryItem{
		GUID: tr.FakeID(), PlayedAt: at, Track: tr,
	}); err != nil {
		t.Fatalf("记播放历史失败：%v", err)
	}
}

// localTrack 造一条「已落本地」的曲目：文件在磁盘上、登记表里有登记、
// 描述符在虚拟 id 表里能从 fake 反查回来（三样缺一，它就不是本地曲库里我们
// 认得出来的东西）。返回曲目与它的虚拟 id、以及落盘路径。
func localTrack(t *testing.T, h *harness, id, title, artist string) (online.Track, string, string) {
	t.Helper()
	tr := teeTrack(id, title, artist)
	fake := h.it.registry.Put(tr)
	p := filepath.Join(t.TempDir(), fmt.Sprintf("%s-%s.mp3", artist, title))
	body := "ID3localfile-" + id
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.it.downloaded.Put(online.DownloadedItem{
		GUID: fake, Path: p, Title: title, Artist: artist, Size: int64(len(body)), Source: "wy",
	}); err != nil {
		t.Fatalf("登记已下载失败：%v", err)
	}
	return tr, fake, p
}

// dailyList 取当日「每日推荐」的曲目列表（走真实的详情端点）。
func dailyList(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	w, handled := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=-1&playlistGUID="+dailyGuidNow(), "", nil)
	if !handled {
		t.Fatal("虚拟歌单的曲目列表该被拦截")
	}
	return playlistBody(t, w)
}

// playlistBody 把曲目列表响应里的 `data.list` 拆成一组 map。
func playlistBody(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	d := dataOf(t, w)
	list, _ := d["list"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		out = append(out, asMap(raw))
	}
	return out
}

// titlesOf 把曲目列表折成歌名切片，便于断言「谁在、谁不在」。
func titlesOf(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, asString(m["title"]))
	}
	return out
}

// fillers 造 n 条「比任何给定时点都新」的历史条目，用来把某些曲目挤出
// 「最近播放」窗口 —— 窗口之外的老播放不该再被排除。
func fillers(t *testing.T, h *harness, user string, n int, base int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		tr := online.Track{
			Platform: "wy", PlatformID: fmt.Sprintf("filler%d", i),
			Title: fmt.Sprintf("填充%d", i), Artists: []string{"F"},
		}
		recordPlay(t, h, user, tr, base-int64(i))
	}
}

// ── 排除最近播放 ────────────────────────────────────────────────────────

// TestDailyRecommendExcludesRecentlyPlayed 是任务 C-1 的主用例。
//
// 三种「同一首歌」都要被排除：
//   - 历史里那首是**另一个平台**的同名同歌手曲目（虚拟 id 不同、身份相同）——
//     只比虚拟 id 的实现会把这一种又推一遍；
//   - 历史里那首**就是**列表里那首（虚拟 id 相同、身份也相同）；
//   - 虚拟 id 相同但标题变了（同一首歌在两个端点上解析出的标题不一致，例如
//     带不带「(Live)」后缀）—— 身份对不上，只能靠虚拟 id 兜住。
func TestDailyRecommendExcludesRecentlyPlayed(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true, "w2": true, "w3": true, "w4": true},
		song("w1", "甲", "A"), song("w2", "乙", "B"),
		song("w3", "丙（Live）", "C"), song("w4", "丁（Live）", "D"))
	user := dailySharedUser(t, h)
	now := time.Now().Unix()

	// ① 身份相同、虚拟 id 不同：网易的《乙 / B》刚听过，候选里是 QQ 的那条。
	recordPlay(t, h, user, online.Track{Platform: "tx", PlatformID: "999", Title: "乙", Artists: []string{"B"}}, now-90)
	// ② 就是候选里那条（网易 w3）刚听过，标题一模一样。
	recordPlay(t, h, user, online.Track{Platform: "wy", PlatformID: "w3", Title: "丙（Live）", Artists: []string{"C"}}, now-60)
	// ③ 同一条曲目（网易 w4），但两次解析出的标题不一样（历史里那份不带后缀）——
	// 身份键对不上，只比虚拟 id 才能兜住。
	recordPlay(t, h, user, online.Track{Platform: "wy", PlatformID: "w4", Title: "丁", Artists: []string{"D"}}, now-30)

	got := titlesOf(dailyList(t, h))
	if !contains(got, "甲") {
		t.Fatalf("没听过的「甲」该留在推荐里，得到 %v", got)
	}
	for _, bad := range []string{"乙", "丙（Live）", "丁（Live）"} {
		if contains(got, bad) {
			t.Errorf("⚠️「%s」刚播放过，不该再推（虚拟 id 或身份相同都要排除），得到 %v", bad, got)
		}
	}
}

// TestDailyRecentPlayWindowOnlyCoversNewest 钉住窗口口径。
//
// 历史最多 500 条；「全部历史都排除」会先把候选池吃干净。只有最近
// `vDailyRecentPlayWindow` 次播放过的才算「最近播放」。
func TestDailyRecentPlayWindowOnlyCoversNewest(t *testing.T) {
	h := newHarness(t)
	newest := "历史0"                                        // 最近一次播放
	outside := fmt.Sprintf("历史%d", vDailyRecentPlayWindow) // 恰好落在窗口之外
	h.setOnline(map[string]bool{"w1": true, "w2": true},
		song("w1", newest, "H"), song("w2", outside, "H"))

	user := dailySharedUser(t, h)
	base := time.Now().Unix()
	for i := 0; i <= vDailyRecentPlayWindow; i++ {
		tr := online.Track{
			Platform: "wy", PlatformID: fmt.Sprintf("h%d", i),
			Title: fmt.Sprintf("历史%d", i), Artists: []string{"H"},
		}
		recordPlay(t, h, user, tr, base-int64(i)*60)
	}

	got := titlesOf(dailyList(t, h))
	if contains(got, newest) {
		t.Errorf("最近播放过的那首不该推，得到 %v", got)
	}
	if !contains(got, outside) {
		t.Errorf("窗口之外（第 %d 次播放之前）的不该被排除，得到 %v", vDailyRecentPlayWindow+1, got)
	}
}

// TestDailyRecommendStillExcludesFavorites 守住**原有**的排除规则。
//
// 排除集这次被重写过（收藏 + 最近播放合并成一套判断），这条用例保证
// 「已经收藏过的不再推」没有被顺手改坏。
func TestDailyRecommendStillExcludesFavorites(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{"w1": true, "w2": true},
		song("w1", "已收藏的", "A"), song("w2", "没收藏的", "B"))
	user := dailySharedUser(t, h)

	var fav online.Track
	for _, s := range h.online {
		if s.Name == "已收藏的" {
			fav = online.Track{Platform: "wy", PlatformID: s.ID, Title: s.Name, Artists: []string{s.Singer}}
		}
	}
	if fav.PlatformID == "" {
		t.Fatal("前置条件没造出来")
	}
	if err := h.it.store.AddFavorite(user, online.FavoriteItem{
		GUID: fav.FakeID(), CreatedAt: time.Now().Unix(), Track: fav,
	}); err != nil {
		t.Fatal(err)
	}

	got := titlesOf(dailyList(t, h))
	if contains(got, "已收藏的") {
		t.Errorf("收藏过的不该再推，得到 %v", got)
	}
	if !contains(got, "没收藏的") {
		t.Errorf("没收藏过的该在推荐里，得到 %v", got)
	}
}

// TestHotPlaylistDoesNotExcludePlayed 钉住「不动热门推荐」。
//
// 榜单是「原味」：用户点进去就是要看榜单，不该因为我们插了一层过滤而少几首
// （参考实现同样只给每日推荐上排除集）。
func TestHotPlaylistDoesNotExcludePlayed(t *testing.T) {
	h := newHarness(t)
	nm := neteaseTrack("9001", "丙", "C")
	h.setNetease(nm)
	user := dailySharedUser(t, h)
	recordPlay(t, h, user, nm, time.Now().Unix()-10)

	w, _ := h.do(http.MethodGet,
		"/music/api/v1/track/playlist-detail/list?page=1&size=-1&playlistGUID="+hotGuidNow(), "", nil)
	if got := titlesOf(playlistBody(t, w)); !contains(got, "丙") {
		t.Fatalf("热门推荐不该排除最近播放（它是榜单原味），得到 %v", got)
	}
}

// ── 本地曲库兜底 ────────────────────────────────────────────────────────

// TestDailyLocalFallbackOnlyFillsGaps 是任务 C-2 的主用例：
// 在线够就一首本地都不进；在线不够才补空位，而且**排在在线之后**。
func TestDailyLocalFallbackOnlyFillsGaps(t *testing.T) {
	t.Run("在线够时一首本地都不进", func(t *testing.T) {
		h := newHarness(t)
		playable := map[string]bool{}
		songs := make([]search.UnifiedSong, 0, vDailySize)
		for i := 0; i < vDailySize; i++ {
			id := fmt.Sprintf("w%d", i)
			playable[id] = true
			songs = append(songs, song(id, fmt.Sprintf("在线%d", i), "A"))
		}
		h.setOnline(playable, songs...)
		localTrack(t, h, "l1", "本地甲", "L")

		got := titlesOf(dailyList(t, h))
		if len(got) != vDailySize {
			t.Fatalf("在线给满时该正好 %d 首，得到 %d：%v", vDailySize, len(got), got)
		}
		if contains(got, "本地甲") {
			t.Fatalf("⚠️ 在线候选够时本地兜底一首都不该进（不能抢在线推荐的位置）：%v", got)
		}
	})

	t.Run("在线不够时补空位且排在后面", func(t *testing.T) {
		h := newHarness(t)
		h.setOnline(map[string]bool{"w1": true}, song("w1", "唯一的在线", "A"))
		localTrack(t, h, "l1", "本地甲", "L")
		localTrack(t, h, "l2", "本地乙", "L")

		got := titlesOf(dailyList(t, h))
		if len(got) != 3 {
			t.Fatalf("该是 1 在线 + 2 本地，得到 %v", got)
		}
		if got[0] != "唯一的在线" {
			t.Fatalf("在线推荐必须排在本地兜底之前，得到 %v", got)
		}
		if !contains(got[1:], "本地甲") || !contains(got[1:], "本地乙") {
			t.Fatalf("缺空位该由本地补齐，得到 %v", got)
		}
	})
}

// TestDailyLocalFallbackOrderAndWindow 钉住本地兜底的顺位：
// 「从未听过」在前、「最久未听」补齐；而且只有被挤出最近播放窗口的老播放，
// 才算「最久未听」那一档（窗口内的已被排除，不会出现在这里）。
func TestDailyLocalFallbackOrderAndWindow(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{}) // 在线一首都没有 → 全靠本地兜底
	user := dailySharedUser(t, h)
	base := time.Now().Unix()

	_, _, _ = localTrack(t, h, "l3", "从没听过", "L") // 历史里没有它
	localTrack(t, h, "l1", "一小时前听过", "L")
	localTrack(t, h, "l2", "半小时前听过", "L")

	// 20 条更新的播放把这两条挤出窗口：它们在窗口之外，属于「最久未听」档。
	fillers(t, h, user, vDailyRecentPlayWindow, base)
	recordPlay(t, h, user, online.Track{Platform: "wy", PlatformID: "l1", Title: "一小时前听过", Artists: []string{"L"}}, base-3600)
	recordPlay(t, h, user, online.Track{Platform: "wy", PlatformID: "l2", Title: "半小时前听过", Artists: []string{"L"}}, base-1800)

	got := titlesOf(dailyList(t, h))
	want := []string{"从没听过", "一小时前听过", "半小时前听过"}
	if len(got) != len(want) {
		t.Fatalf("该推 %v，得到 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺位该是「从未听过 → 最久未听」：期望 %v，得到 %v", want, got)
		}
	}
}

// TestDailyLocalFallbackHonoursExclude 钉住本地池也走同一套排除集。
//
// 本地那批曲目同样是「他已经有的歌」：收藏过的不推（推荐里再出现一次没有意义），
// 最近刚听过的不推（刚点过一遍的又排在最前面，看起来就像推荐坏了）。
func TestDailyLocalFallbackHonoursExclude(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{})
	user := dailySharedUser(t, h)
	now := time.Now().Unix()

	_, favFake, _ := localTrack(t, h, "l1", "本地收藏的", "L")
	localTrack(t, h, "l2", "本地刚听过的", "L")
	localTrack(t, h, "l9", "本地好的", "L")

	if err := h.it.store.AddFavorite(user, online.FavoriteItem{
		GUID: favFake, CreatedAt: now, Track: online.Track{
			Platform: "wy", PlatformID: "l1", Title: "本地收藏的", Artists: []string{"L"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	recordPlay(t, h, user, online.Track{
		Platform: "wy", PlatformID: "l2", Title: "本地刚听过的", Artists: []string{"L"},
	}, now-30)

	got := titlesOf(dailyList(t, h))
	if len(got) != 1 || got[0] != "本地好的" {
		t.Fatalf("本地池只该推「本地好的」（另两条在排除集里），得到 %v", got)
	}
}

// TestDailyLocalFallbackSkipsDeadEntries 钉住「点得动的才推」。
//
// 一条推出来点不动的条目比不推更糟：用户会以为功能坏了。
func TestDailyLocalFallbackSkipsDeadEntries(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{})

	// ① 文件被删了（用户自己清理过）→ 不推。
	_, _, gone := localTrack(t, h, "l1", "文件没了的", "L")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	// ② 登记表里有、但描述符不在虚拟 id 表里 → 说不清是哪首歌，不推。
	orphan := filepath.Join(t.TempDir(), "orphan.mp3")
	if err := os.WriteFile(orphan, []byte("ID3orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.it.downloaded.Put(online.DownloadedItem{GUID: fakeID(4242), Path: orphan}); err != nil {
		t.Fatal(err)
	}
	// ③ 正常的本地曲目 —— 它必须在，否则「全都跳过」也能让上面两条断言通过。
	localTrack(t, h, "l9", "本地好的", "L")

	got := titlesOf(dailyList(t, h))
	if !contains(got, "本地好的") {
		t.Fatalf("正常的本地曲目该被推出来，得到 %v", got)
	}
	for _, bad := range []string{"文件没了的", "未知曲目"} {
		if contains(got, bad) {
			t.Errorf("⚠️ 不该推点不动的条目（%q），得到 %v", bad, got)
		}
	}
	if len(got) != 1 {
		t.Errorf("只该有 1 首（那首好的），得到 %v", got)
	}
}

// TestDailyLocalFallbackTracksArePlayable 是这条链路的端到端：
// 本地兜底推出来的曲目，取流必须直接读磁盘那份 —— 不出网、字节一模一样。
func TestDailyLocalFallbackTracksArePlayable(t *testing.T) {
	h := newHarness(t)
	h.setOnline(map[string]bool{})
	tr, fake, path := localTrack(t, h, "l1", "本地甲", "L")
	_ = tr

	list := dailyList(t, h)
	if len(list) != 1 {
		t.Fatalf("该推出 1 首本地曲目，得到 %d", len(list))
	}
	guid := asString(list[0]["guid"])
	if guid != fake {
		t.Fatalf("列表里的 guid 该是这条曲目的虚拟 id：得到 %q，期望 %q", guid, fake)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	w, handled := h.do(http.MethodGet, "/music/api/v1/track/stream?guid="+guid, "", nil)
	if !handled {
		t.Fatal("取流该被拦截层处理")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("本地兜底推出来的曲目必须能播（读磁盘那份），得到 %d：%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(want) {
		t.Fatalf("该把磁盘上那份原样喂给客户端：得到 %q，期望 %q", got, want)
	}
	// 一次网络动作都不该发生（既没打媒体 CDN，也没打官方上游）。
	for _, c := range h.up.calls() {
		if strings.Contains(c, "track/stream") {
			t.Errorf("本地曲目的取流不该透传给官方：%s", c)
		}
	}
}
