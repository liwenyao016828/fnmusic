package online

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ── 虚拟 id ──────────────────────────────────────────────────────────────

func TestFakeIDIsDeterministicHex32(t *testing.T) {
	real := RealID("wy", "2652820720")
	fake := FakeID(real)

	if fake != FakeID(real) {
		t.Fatal("同一个真实 id 两次哈希结果不同 —— 重启后就重建不出映射表了")
	}
	if !IsHex32(fake) {
		t.Fatalf("虚拟 id 必须是 32 位小写 hex，得到 %q", fake)
	}
	if len(fake) != 32 {
		t.Fatalf("长度应为 32，得到 %d", len(fake))
	}
	if other := FakeID(RealID("tx", "2652820720")); other == fake {
		t.Fatal("不同平台同号曲目不应撞到同一个虚拟 id")
	}
}

func TestRealIDRoundTrip(t *testing.T) {
	real := RealID("tx", "0039MnYb0qxYhV")
	if !IsRealID(real) {
		t.Fatalf("%q 应被认成真实 id", real)
	}
	platform, id, ok := ParseRealID(real)
	if !ok || platform != "tx" || id != "0039MnYb0qxYhV" {
		t.Fatalf("解析结果不对：%q %q %v", platform, id, ok)
	}

	// 平台内 id 自己带冒号时，只切第一个冒号。
	platform, id, ok = ParseRealID("online:kw:123:456")
	if !ok || platform != "kw" || id != "123:456" {
		t.Fatalf("带冒号的 id 解析结果不对：%q %q %v", platform, id, ok)
	}

	if IsRealID("online") || IsRealID("offline:wy:1") {
		t.Fatal("非 online: 前缀不该被认成真实 id")
	}
}

func TestSubIDsAreDistinct(t *testing.T) {
	real := RealID("wy", "1")
	base := FakeID(real)
	seen := map[string]string{}
	for _, kind := range []string{SubKindAlbum, SubKindArtist, SubKindLyric} {
		sub := FakeSubID(real, kind)
		if !IsHex32(sub) {
			t.Fatalf("%s 子 id 不是 32 位 hex：%q", kind, sub)
		}
		if sub == base {
			t.Fatalf("%s 子 id 与主 id 相同", kind)
		}
		if prev, dup := seen[sub]; dup {
			t.Fatalf("%s 子 id 与 %s 撞车", kind, prev)
		}
		seen[sub] = kind
	}
}

func TestIsHex32RejectsOtherShapes(t *testing.T) {
	for _, s := range []string{
		"", "online:wy:1", "ABCDEF0123456789ABCDEF0123456789", // 大写
		"0123456789abcdef0123456789abcde",   // 31 位
		"0123456789abcdef0123456789abcdef0", // 33 位
		"0123456789abcdef0123456789abcdeg",  // 非 hex 字符
	} {
		if IsHex32(s) {
			t.Fatalf("%q 不该被认成 32 位 hex", s)
		}
	}
}

// ── 登记表 ──────────────────────────────────────────────────────────────

func TestRegistryWarmRebuildsAfterRestart(t *testing.T) {
	track := Track{Platform: "wy", PlatformID: "42", Title: "晴天", Artists: []string{"周杰伦"}}

	r1 := NewRegistry(0)
	fake := r1.Put(track)
	if _, ok := r1.Lookup(fake); !ok {
		t.Fatal("刚登记的条目查不到")
	}

	// 模拟进程重启：新登记表 + 重放磁盘上的描述符。
	r2 := NewRegistry(0)
	if _, ok := r2.Lookup(fake); ok {
		t.Fatal("空登记表不该查到任何东西")
	}
	r2.Warm([]Track{track})
	got, ok := r2.Lookup(fake)
	if !ok {
		t.Fatal("Warm 之后仍查不到 —— 重启后在线曲目会全部失效")
	}
	if got.Title != "晴天" {
		t.Fatalf("Warm 出来的描述符不对：%+v", got)
	}
}

func TestRegistryEvictsOldest(t *testing.T) {
	r := NewRegistry(3)
	var fakes []string
	for i := 0; i < 3; i++ {
		fakes = append(fakes, r.Put(Track{Platform: "wy", PlatformID: string(rune('a' + i))}))
	}
	// 再塞一条，最旧的应被淘汰。
	newest := r.Put(Track{Platform: "wy", PlatformID: "z"})
	if r.Len() != 3 {
		t.Fatalf("上限失效，Len=%d", r.Len())
	}
	if _, ok := r.Lookup(fakes[0]); ok {
		t.Fatal("最旧的条目没有被淘汰")
	}
	if _, ok := r.Lookup(newest); !ok {
		t.Fatal("新条目查不到")
	}
}

func TestRegistryPutDoesNotReorder(t *testing.T) {
	r := NewRegistry(2)
	a := Track{Platform: "wy", PlatformID: "a"}
	fa := r.Put(a)
	r.Put(Track{Platform: "wy", PlatformID: "b"})
	// 重复登记 a 不应把它变成「最新」。
	r.Put(a)
	r.Put(Track{Platform: "wy", PlatformID: "c"})
	if _, ok := r.Lookup(fa); ok {
		t.Fatal("重复登记刷新了淘汰顺序 —— 反复搜索会把常用曲目挤出去")
	}
}

// ── VO 形状 ─────────────────────────────────────────────────────────────
//
// 这些断言存在的理由很具体：官方前端对形状的要求是**硬性**的。
// `genres` 是 null 会让 `_h()` 抛错，`album` 不是对象会让播放器直接跳过，
// duration 单位错了会让进度条显示成 0:00。

func marshalJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal 失败：%v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal 失败：%v", err)
	}
	return out
}

func sampleTrack() Track {
	return Track{
		Platform:   "wy",
		PlatformID: "2652820720",
		Title:      "晴天",
		Artists:    []string{"周杰伦"},
		Album:      "叶惠美",
		Duration:   269,
		CoverURL:   "https://example.invalid/c.jpg",
	}
}

func TestSearchVOShape(t *testing.T) {
	track := sampleTrack()
	vo := marshalJSON(t, SearchVO(track, Playback{}, true))
	fake := track.FakeID()

	if vo["guid"] != fake {
		t.Fatalf("guid 应为虚拟 id %q，得到 %v", fake, vo["guid"])
	}
	if vo["coverId"] != fake {
		t.Fatalf("coverId 应等于 guid，得到 %v", vo["coverId"])
	}
	if vo["duration"] != float64(269000) {
		t.Fatalf("duration 必须是毫秒，得到 %v", vo["duration"])
	}
	if vo["duration_s"] != float64(269) {
		t.Fatalf("duration_s 应为秒，得到 %v", vo["duration_s"])
	}

	// genres 必须是数组 —— 这里用「重新 marshal 一次看是不是 null」来验，
	// 因为 nil slice 会静默变成 null，而在 Go 里两种写法看起来一样。
	raw, err := json.Marshal(SearchVO(track, Playback{}, false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"genres":[]`) {
		t.Fatalf("genres 必须是空数组而不是 null：%s", raw)
	}

	album, ok := vo["album"].(map[string]any)
	if !ok {
		t.Fatalf("album 必须是对象，得到 %T", vo["album"])
	}
	if album["guid"] != fake+":"+SubKindAlbum {
		t.Fatalf("album.guid 不对：%v", album["guid"])
	}
	if album["coverId"] != fake {
		t.Fatalf("album.coverId 不对：%v", album["coverId"])
	}

	artists, ok := vo["artists"].([]any)
	if !ok || len(artists) == 0 {
		t.Fatalf("artists 必须是非空数组，得到 %T", vo["artists"])
	}
	first, _ := artists[0].(map[string]any)
	if first["guid"] != fake+":"+SubKindArtist {
		t.Fatalf("artist.guid 不对：%v", first["guid"])
	}

	spec, ok := vo["audioSpec"].(map[string]any)
	if !ok {
		t.Fatalf("audioSpec 必须是对象，得到 %T", vo["audioSpec"])
	}
	if !strings.HasPrefix(spec["path"].(string), "online/wy/") {
		t.Fatalf("audioSpec.path 前缀不对：%v", spec["path"])
	}
	if spec["duration"] != float64(269000) {
		t.Fatalf("audioSpec.duration 必须是毫秒，得到 %v", spec["duration"])
	}
	if _, has := spec["bitDepth"]; has {
		t.Fatal("有损格式不该给 bitDepth")
	}
	if vo["is_online"] != true {
		t.Fatal("is_online 必须为 true")
	}
}

func TestSearchVOFlacHasBitDepth(t *testing.T) {
	track := sampleTrack()
	vo := marshalJSON(t, SearchVO(track, Playback{Format: "flac", Size: 30 << 20}, false))
	spec := vo["audioSpec"].(map[string]any)
	if spec["bitDepth"] != float64(16) {
		t.Fatalf("flac 应带 bitDepth=16，得到 %v", spec["bitDepth"])
	}
	if spec["bitrate"] != float64(1411000) {
		t.Fatalf("无损码率应为 1411000，得到 %v", spec["bitrate"])
	}
	if spec["size"] != float64(30<<20) {
		t.Fatalf("size 应透传，得到 %v", spec["size"])
	}
}

func TestMetadataVOHasTrackObject(t *testing.T) {
	track := sampleTrack()
	env := marshalJSON(t, MetadataVO(track, Playback{}, true, true))
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("MetadataVO 应返回官方信封，data 是对象，得到 %T", env["data"])
	}

	inner, ok := data["track"].(map[string]any)
	if !ok {
		t.Fatalf("data.track 必须是对象，得到 %T", data["track"])
	}
	if inner["title"] != "晴天" {
		t.Fatalf("track.title 不对：%v", inner["title"])
	}
	if _, ok := inner["album"].(map[string]any); !ok {
		t.Fatalf("track.album 必须是对象，得到 %T", inner["album"])
	}
	if _, ok := inner["genres"].([]any); !ok {
		t.Fatalf("track.genres 必须是数组，得到 %T", inner["genres"])
	}
	if inner["hasLyric"] != true || data["hasLyric"] != true {
		t.Fatal("hasLyric 应同时出现在 data 顶层与 data.track 里")
	}
	if inner["isFavorite"] != true {
		t.Fatal("isFavorite 应透传")
	}
	if env["code"] != float64(0) {
		t.Fatalf("code 应为 0，得到 %v", env["code"])
	}
}

func TestLyricVOs(t *testing.T) {
	fake := FakeID(RealID("wy", "1"))

	// 空歌词 → 空列表 + preferred 为空串（不是一条空 content）。
	empty := marshalJSON(t, LyricListVO(fake, "   ", 123))
	data := empty["data"].(map[string]any)
	if list := data["list"].([]any); len(list) != 0 {
		t.Fatalf("空歌词应给空列表，得到 %v", list)
	}
	if data["preferred"] != "" {
		t.Fatalf("空歌词的 preferred 应为空串，得到 %v", data["preferred"])
	}

	// 有歌词 → 一条记录，guid 带 :lyric 后缀，source=2。
	full := marshalJSON(t, LyricListVO(fake, "[00:01.00]晴天", 123))
	data = full["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("应有 1 条歌词，得到 %d", len(list))
	}
	entry := list[0].(map[string]any)
	if entry["guid"] != fake+":"+SubKindLyric {
		t.Fatalf("歌词 guid 不对：%v", entry["guid"])
	}
	if entry["source"] != float64(2) {
		t.Fatalf("source 应为 2（外挂 LRC），得到 %v", entry["source"])
	}
	if entry["isLRC"] != true {
		t.Fatal("isLRC 应为 true")
	}
	if data["preferred"] != fake+":"+SubKindLyric {
		t.Fatalf("preferred 不对：%v", data["preferred"])
	}

	text := marshalJSON(t, LyricTextVO(fake, "abc"))
	td := text["data"].(map[string]any)
	if td["guid"] != fake || td["lyric"] != "abc" {
		t.Fatalf("LyricTextVO 形状不对：%v", td)
	}

	// EmptyOK 的 data 是空对象而不是 null。
	emptyOK := marshalJSON(t, EmptyOK())
	if _, ok := emptyOK["data"].(map[string]any); !ok {
		t.Fatalf("EmptyOK 的 data 应为对象，得到 %T", emptyOK["data"])
	}
}

// TestFavoriteVOHonorsIsFavoriteAndMarksOnline 钉住 FavoriteVO 的两个取值语义。
//
// 真机 bug 背景：官方前端从列表反推收藏集合 ——
//
//	favoriteIds = list.filter(x => x.isFavorite).map(x => x.guid)
//
// 而我们三个列表（收藏 / 歌单附加曲目 / 播放历史）共用这个构造器，早先把
// isFavorite 写死成 true，于是歌单和历史里的**每一首**在线曲目都被标成已收藏。
// 所以它必须由调用方算好传进来，且 false 要能原样带出。
func TestFavoriteVOHonorsIsFavoriteAndMarksOnline(t *testing.T) {
	track := sampleTrack()

	fav := marshalJSON(t, FavoriteVO(track, Playback{}, 1700000000, true, nil))
	if fav["isFavorite"] != true {
		t.Fatalf("isFavorite=true 应原样带出，得到 %v", fav["isFavorite"])
	}
	if fav["is_online"] != true {
		t.Fatalf("在线曲目应带 is_online=true（契约一致性），得到 %v", fav["is_online"])
	}

	plain := marshalJSON(t, FavoriteVO(track, Playback{}, 1700000000, false, nil))
	if plain["isFavorite"] != false {
		t.Fatalf("isFavorite=false 必须原样带出，得到 %v —— 写死 true 会让歌单/历史里未收藏的在线曲目显示成已收藏", plain["isFavorite"])
	}
}

func TestFavoriteVOMergesTemplate(t *testing.T) {
	track := sampleTrack()
	template := map[string]any{"barcode": "REAL-BARCODE", "title": "旧标题"}
	vo := marshalJSON(t, FavoriteVO(track, Playback{}, 1700000000, true, template))

	if vo["barcode"] != "REAL-BARCODE" {
		t.Fatalf("模板里的字段应被保留，得到 %v", vo["barcode"])
	}
	if vo["title"] != "晴天" {
		t.Fatalf("我们的字段应覆盖模板，得到 %v", vo["title"])
	}
	if vo["createdAt"] != float64(1700000000) {
		t.Fatalf("createdAt 不对：%v", vo["createdAt"])
	}
	album := vo["album"].(map[string]any)
	if _, has := album["artists"]; has {
		t.Fatal("收藏 VO 的 album 不该带 artists（官方形状如此）")
	}
}

// ── 存储 ────────────────────────────────────────────────────────────────

func TestStoreFavoritesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	track := sampleTrack()
	fake := track.FakeID()

	if err := s.AddFavorite("u1", FavoriteItem{GUID: fake, CreatedAt: 1, Track: track}); err != nil {
		t.Fatal(err)
	}
	// 重复加不应产生两条。
	if err := s.AddFavorite("u1", FavoriteItem{GUID: fake, CreatedAt: 2, Track: track}); err != nil {
		t.Fatal(err)
	}
	items := s.Favorites("u1")
	if len(items) != 1 {
		t.Fatalf("重复收藏应只留一条，得到 %d", len(items))
	}
	if !s.FavoriteGUIDs("u1")[fake] {
		t.Fatal("FavoriteGUIDs 没返回刚加的曲目")
	}

	// 多用户隔离。
	if got := s.Favorites("u2"); len(got) != 0 {
		t.Fatalf("u2 不该看到 u1 的收藏，得到 %d 条", len(got))
	}

	if _, err := s.RemoveFavorite("u1", fake); err != nil {
		t.Fatal(err)
	}
	if got := s.Favorites("u1"); len(got) != 0 {
		t.Fatalf("删除后仍有 %d 条", len(got))
	}

	// 重新打开（模拟重启）后仍是空的。
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Favorites("u1"); len(got) != 0 {
		t.Fatalf("重启后删除的收藏又回来了：%d 条", len(got))
	}
}

func TestStorePlaylistAndCascade(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	track := sampleTrack()
	fake := track.FakeID()
	pl := "playlist-guid-1"

	if err := s.AddPlaylistTracks("u", pl, []PlaylistItem{{GUID: fake, AddedAt: 1, Track: track}}); err != nil {
		t.Fatal(err)
	}
	if got := s.PlaylistTracks("u", pl); len(got) != 1 {
		t.Fatalf("歌单应有 1 条，得到 %d", len(got))
	}
	if got := s.PlaylistTrackCounts("u")[pl]; got != 1 {
		t.Fatalf("计数应为 1，得到 %d", got)
	}

	// 删除歌单要级联清掉条目。
	if err := s.DropPlaylist("u", pl); err != nil {
		t.Fatal(err)
	}
	if got := s.PlaylistTracks("u", pl); len(got) != 0 {
		t.Fatalf("删歌单后仍留了 %d 条孤儿数据", len(got))
	}
	if got := s.PlaylistTrackCounts("u")[pl]; got != 0 {
		t.Fatalf("删歌单后计数仍为 %d", got)
	}
}

func TestStoreHistoryLimitAndOrder(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 塞满 + 超出上限。
	for i := 0; i < historyLimit+10; i++ {
		track := Track{Platform: "wy", PlatformID: itoaTest(i), Title: "t"}
		if err := s.RecordPlay("u", HistoryItem{GUID: track.FakeID(), PlayedAt: int64(i), Track: track}); err != nil {
			t.Fatal(err)
		}
	}
	items := s.History("u")
	if len(items) != historyLimit {
		t.Fatalf("历史上限应为 %d，得到 %d", historyLimit, len(items))
	}
	// 最新在前。
	if items[0].PlayedAt < items[len(items)-1].PlayedAt {
		t.Fatal("播放历史应按时间倒序（新的在前）")
	}

	// 同一首歌重复播放应只留一条（时间刷新）。
	track := Track{Platform: "wy", PlatformID: "dup", Title: "d"}
	_ = s.RecordPlay("u", HistoryItem{GUID: track.FakeID(), PlayedAt: 1, Track: track})
	_ = s.RecordPlay("u", HistoryItem{GUID: track.FakeID(), PlayedAt: 999, Track: track})
	count := 0
	for _, it := range s.History("u") {
		if it.Track.PlatformID == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重复播放应只留一条，得到 %d", count)
	}
}

func TestAllTracksCoversEveryBucket(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fa := Track{Platform: "wy", PlatformID: "1"}
	fb := Track{Platform: "tx", PlatformID: "2"}
	fc := Track{Platform: "wy", PlatformID: "3"}

	_ = s.AddFavorite("u", FavoriteItem{GUID: fa.FakeID(), Track: fa})
	_ = s.AddPlaylistTracks("u", "pl", []PlaylistItem{{GUID: fb.FakeID(), Track: fb}})
	_ = s.RecordPlay("u", HistoryItem{GUID: fc.FakeID(), Track: fc})

	got := map[string]bool{}
	for _, t2 := range s.AllTracks() {
		got[t2.FakeID()] = true
	}
	for name, track := range map[string]Track{"收藏": fa, "歌单": fb, "历史": fc} {
		if !got[track.FakeID()] {
			t.Fatalf("%s 里的曲目没被 AllTracks 覆盖 —— 重启后它的虚拟 id 反解不出来", name)
		}
	}
}

func TestUserKeySanitizes(t *testing.T) {
	cases := map[string]string{
		"":            "shared",
		"   ":         "shared",
		"___":         "shared",
		"../etc/pass": "___etc_pass",
		"abc-123":     "abc-123",
		"../../x":     "______x",
	}
	for in, want := range cases {
		if got := UserKey(in); got != want {
			t.Fatalf("UserKey(%q) = %q，期望 %q", in, got, want)
		}
	}
	if strings.ContainsAny(UserKey("a/b"), `/\`) {
		t.Fatal("UserKey 结果不该含路径分隔符")
	}
	long := strings.Repeat("x", 200)
	if len(UserKey(long)) > 64 {
		t.Fatalf("UserKey 应截断到 64 字符，得到 %d", len(UserKey(long)))
	}
}

// ── 解析池 ──────────────────────────────────────────────────────────────

type fakeResolver struct {
	platform string
	answers  map[string]*Resolved
	fail     map[string]error
	calls    int
}

func (f *fakeResolver) Platform() string { return f.platform }

func (f *fakeResolver) Resolve(ctx context.Context, id string) (*Resolved, error) {
	f.calls++
	if err, ok := f.fail[id]; ok {
		return nil, err
	}
	if r, ok := f.answers[id]; ok {
		return r, nil
	}
	return nil, ErrNotPlayable
}

func (f *fakeResolver) ResolveBatch(ctx context.Context, ids []string) (map[string]*Resolved, error) {
	out := map[string]*Resolved{}
	for _, id := range ids {
		if r, err := f.Resolve(ctx, id); err == nil {
			out[id] = r
		}
	}
	return out, nil
}

func TestPoolCachesSuccessAndNotPlayable(t *testing.T) {
	r := &fakeResolver{
		platform: "wy",
		answers:  map[string]*Resolved{"1": {URL: "https://cdn.invalid/1.mp3", Format: "mp3"}},
		fail:     map[string]error{"2": ErrNotPlayable},
	}
	p := NewPool(r)

	if _, err := p.Resolve(context.Background(), "wy", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background(), "wy", "1"); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatalf("命中缓存后不该再解析，解析器被调了 %d 次", r.calls)
	}

	// 明确不可播可以负缓存。
	if _, err := p.Resolve(context.Background(), "wy", "2"); !errors.Is(err, ErrNotPlayable) {
		t.Fatalf("应返回 ErrNotPlayable，得到 %v", err)
	}
	if _, err := p.Resolve(context.Background(), "wy", "2"); !errors.Is(err, ErrNotPlayable) {
		t.Fatalf("负缓存失效，得到 %v", err)
	}
	if r.calls != 2 {
		t.Fatalf("负缓存未生效，解析器被调了 %d 次", r.calls)
	}
}

func TestPoolDoesNotCacheNetworkErrors(t *testing.T) {
	netErr := errors.New("connection reset")
	r := &fakeResolver{platform: "wy", fail: map[string]error{"1": netErr}}
	p := NewPool(r)

	if _, err := p.Resolve(context.Background(), "wy", "1"); err == nil {
		t.Fatal("应报错")
	}
	if _, err := p.Resolve(context.Background(), "wy", "1"); err == nil {
		t.Fatal("应报错")
	}
	// 网络错误**不能**缓存：一次抖动不该让这首歌在 5 分钟内都播不了。
	if r.calls != 2 {
		t.Fatalf("网络错误被缓存了，解析器只被调了 %d 次", r.calls)
	}
}

func TestPoolUnknownPlatform(t *testing.T) {
	p := NewPool(&fakeResolver{platform: "wy"})
	if _, err := p.Resolve(context.Background(), "kg", "1"); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("没有解析器的平台应返回 ErrNoResolver，得到 %v", err)
	}
	if p.Supports("kg") {
		t.Fatal("Supports 不该说支持 kg")
	}
	if !p.Supports("wy") {
		t.Fatal("Supports 应说支持 wy")
	}
}

func TestResolveManySkipsFailures(t *testing.T) {
	r := &fakeResolver{
		platform: "wy",
		answers: map[string]*Resolved{
			"1": {URL: "https://cdn.invalid/1.mp3"},
			"3": {URL: "https://cdn.invalid/3.mp3"},
		},
		fail: map[string]error{"2": ErrNotPlayable},
	}
	p := NewPool(r)
	got := p.ResolveMany(context.Background(), "wy", []string{"1", "2", "3"})
	if len(got) != 2 {
		t.Fatalf("应只返回能播的 2 条，得到 %d", len(got))
	}
	if _, ok := got["2"]; ok {
		t.Fatal("不可播的条目不该出现在结果里")
	}
}

func TestTrackNormalizedFillsDefaults(t *testing.T) {
	empty := Track{Platform: "wy", PlatformID: "1"}.Normalized()
	if empty.Title == "" {
		t.Fatal("标题为空时应兜底")
	}
	if empty.Artist() == "" {
		t.Fatal("艺术家为空时应兜底 —— 官方 VO 的 artists 不能是空数组")
	}
	if empty.AlbumName() == "" {
		t.Fatal("专辑名为空时应兜底")
	}
	// 时长缺失时**不**编一个值：0 表示「未知」，编一个假时长会让进度条撒谎、
	// 也让拖动定位落在错误的位置。这里只要求别出现负数。
	if empty.Duration < 0 {
		t.Fatalf("时长不该是负数，得到 %d", empty.Duration)
	}
}

func TestPlatformName(t *testing.T) {
	for _, p := range []string{"wy", "tx", "kg", "kw"} {
		if PlatformName(p) == "" || PlatformName(p) == p {
			t.Fatalf("%s 应有人话名称，得到 %q", p, PlatformName(p))
		}
	}
	if PlatformName("zz") != "zz" {
		t.Fatalf("未知平台应原样返回，得到 %q", PlatformName("zz"))
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

var _ = time.Now
