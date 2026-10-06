package intercept

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 「收藏 / 加入歌单 → 自动下载并绑定本地」（v2.1.92）的用例。
//
// 这一层的价值全在**不挡路**与**不重复**上：
//   - 收藏是即时反馈的操作，下载必须后台跑（用户点红心不能等一首无损下完）；
//   - 收藏与「加入歌单」可能几乎同时触发，同一首歌只能下一次；
//   - 下完之后那个虚拟 guid 的取流要改走本地文件（这就是「绑定」）；
//   - 本地文件被删了要**回落**到在线流，而不是一直 404。

// fakeDownloadBackend 假的本机下载接口：真的写一个文件出来并回报它的路径。
//
// 为什么真写文件：`Downloaded.Put` 只登记**文件真的存在**的记录（登记一个不存在的
// 路径 = 收藏里那首歌直接 404），所以这里必须造一个真实存在的文件，否则用例测的是
// 一条永远走不通的路径。
func fakeDownloadBackend(t *testing.T) (*httptest.Server, *[]map[string]any, string) {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "甲 - 乙.mp3")
	if err := os.WriteFile(dst, []byte("ID3\x03\x00\x00\x00FAKEAUDIO"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := &[]map[string]any{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download/song" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		*seen = append(*seen, m)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"status": "success", "path": dst, "song_name": "甲"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, seen, dst
}

// downloadedItemFor 造一条登记记录。
func downloadedItemFor(guid, path, title, artist string) online.DownloadedItem {
	return online.DownloadedItem{
		GUID: guid, Path: path, Title: title, Artist: artist, At: time.Now().Unix(),
	}
}

// newDownloadedFor 开一张登记表（薄壳，只为让用例读起来短）。
func newDownloadedFor(dir string) (*online.Downloaded, error) { return online.NewDownloaded(dir) }

// waitDownloaded 等登记表出现某条记录（自动下载是后台 goroutine）。
func waitDownloaded(t *testing.T, h *harness, guid string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.it.downloaded.Get(guid); ok {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestFavoriteCreateTriggersBackgroundDownload(t *testing.T) {
	h := newHarness(t)
	srv, seen, dst := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }

	fake := registerOnline(h, "w900", "甲", "乙")

	w, _ := h.do(http.MethodPost, apiPrefix+"/favorite-track/create",
		`{"guid":"`+fake+`"}`, nil)
	if env := decode(t, w); asInt(env["code"]) != 0 {
		t.Fatalf("收藏本身必须成功（下载是附加动作）：%+v", env)
	}

	if !waitDownloaded(t, h, fake) {
		t.Fatal("开关打开时，收藏该触发后台下载并登记")
	}
	item, _ := h.it.downloaded.Get(fake)
	if item.Path != dst {
		t.Fatalf("登记的路径该是下载接口回报的那个：%q", item.Path)
	}
	if item.Title != "甲" || item.Artist != "乙" {
		t.Fatalf("登记该带上元数据（排障要看）：%+v", item)
	}
	// 落盘字节数：以前这里一直是空的（下载接口不返回大小，得自己 stat）。
	if item.Size <= 0 {
		t.Fatalf("登记该带上落盘字节数：%+v", item)
	}

	// 发给本机下载接口的载荷必须是完整的一首（少了哪个字段都会落成残缺文件）
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	got := (*seen)[0]
	for _, k := range []string{"name", "singer", "album", "source", "songmid", "url", "duration"} {
		if _, ok := got[k]; !ok {
			t.Errorf("下载载荷缺字段 %q：%+v", k, got)
		}
	}
	if got["name"] != "甲" || got["source"] != "wy" || got["songmid"] != "w900" {
		t.Fatalf("载荷内容不对：%+v", got)
	}
}

func TestFavoriteCreateSkipsDownloadWhenSwitchOff(t *testing.T) {
	// 默认关：不能因为「能力更好」就替用户决定占他的磁盘和带宽
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return false }

	fake := registerOnline(h, "w901", "甲", "乙")
	h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)

	time.Sleep(300 * time.Millisecond) // 给后台 goroutine 足够时间去犯错
	if len(*seen) != 0 {
		t.Fatalf("开关关着时不该调下载接口：%+v", *seen)
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("开关关着时不该登记")
	}
}

func TestAutoDownloadDedupesConcurrentTriggers(t *testing.T) {
	// 用户手快：收藏之后立刻又加入歌单 → 两次触发只能下一次
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }

	fake := registerOnline(h, "w902", "甲", "乙")
	h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)
	h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)

	if !waitDownloaded(t, h, fake) {
		t.Fatal("该登记")
	}
	time.Sleep(200 * time.Millisecond)
	if len(*seen) != 1 {
		t.Fatalf("同一首歌只该下一次，实际 %d 次", len(*seen))
	}
}

func TestStreamServesLocalFileAfterDownload(t *testing.T) {
	// 这条是「绑定」的核心断言：登记之后取流**不再走在线代理**，直接给本地文件。
	h := newHarness(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "甲.mp3")
	if err := os.WriteFile(local, []byte("ID3\x03\x00\x00\x00LOCALBYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := registerOnline(h, "w903", "甲", "乙")
	if err := h.it.downloaded.Put(downloadedItemFor(fake, local, "甲", "乙")); err != nil {
		t.Fatalf("登记失败：%v", err)
	}

	// ⚠️ 不设「上游返回什么」：harness 的解析池把可播曲目指向
	// `https://cdn.invalid/<id>.mp3`（一个不存在的域名），所以「在线代理」这条
	// 路本来就走不通。判据改成「拿到了本地内容」——那才说明绑定生效了。
	w, handled := h.do(http.MethodGet, apiPrefix+"/track/stream?guid="+fake, "", nil)
	if !handled {
		t.Fatal("该被拦截层处理")
	}
	body := w.Body.String()
	if !strings.Contains(body, "LOCALBYTES") {
		t.Fatalf("该服务本地文件：%q", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "audio/mpeg") && !strings.Contains(ct, "audio") {
		t.Fatalf("Content-Type 该按文件推断：%q", ct)
	}
}

func TestStreamFallsBackWhenLocalFileGone(t *testing.T) {
	// 本地文件被清理掉之后必须**回落**到在线流 —— 一直 404 的话用户只会看到
	// 「这首歌播不了」，而完全想不到是「登记表里那条指向的文件没了」。
	h := newHarness(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "gone.mp3")
	if err := os.WriteFile(local, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := registerOnline(h, "w904", "甲", "乙")
	if err := h.it.downloaded.Put(downloadedItemFor(fake, local, "甲", "乙")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(local) // 文件没了

	w, _ := h.do(http.MethodGet, apiPrefix+"/track/stream?guid="+fake, "", nil)
	if strings.Contains(w.Body.String(), "LOCALBYTES") {
		t.Fatalf("文件都不在了，不该再服务本地：%q", w.Body.String())
	}
	// 核心断言：顺手把那条走不通的登记摘掉 —— 不摘的话每次取流都白跑一遍 stat，
	// 而且用户永远看不到「为什么这首歌播不了」的真相。
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("发现文件不在时该摘掉登记")
	}
}

func TestDownloadedRegistrySurvivesRestart(t *testing.T) {
	// 进程重启后必须还记得 —— 否则收藏里的歌会「突然又变回在线流」，
	// 而文件明明已经在库里了。
	dir := t.TempDir()
	f := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d1, err := newDownloadedFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d1.Put(downloadedItemFor("guid1", f, "甲", "乙")); err != nil {
		t.Fatal(err)
	}

	d2, err := newDownloadedFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := d2.Get("guid1")
	if !ok || item.Path != f {
		t.Fatalf("重启后该还在：%+v ok=%v", item, ok)
	}
}

func TestDownloadedRefusesMissingFile(t *testing.T) {
	// 登记一个不存在的路径 = 收藏里那首歌直接 404，所以 Put 必须拒绝
	dir := t.TempDir()
	d, err := newDownloadedFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Put(downloadedItemFor("g", filepath.Join(dir, "nope.mp3"), "甲", "乙")); err == nil {
		t.Fatal("文件不存在时 Put 该报错，而不是登记一条走不通的记录")
	}
	if _, ok := d.Get("g"); ok {
		t.Fatal("不该留下半条记录")
	}
}

// 「下载时带不带歌词 / 内嵌封面」（v2.1.96）的用例。
//
// ⚠️ 这两个开关**缺省是开**（与收藏自动下载 / 边听边下相反）：老配置里没有这两个键，
// 而「下下来的歌没歌词 / 没封面」是明显的退步。用例主要守这个缺省 ——
// 把它改成「缺省关」会让所有老用户的下载静默掉一个档次。

func TestDownloadPrefsDefaultsOn(t *testing.T) {
	h := newHarness(t)
	// 完全没接钩子（老配置 / 老调用方）→ 两个都当开
	p := h.it.downloadPrefs()
	if p["embed_lyric"] != true || p["write_lrc"] != true {
		t.Fatalf("缺省该带歌词：%+v", p)
	}
	if _, ok := p["embed_cover"]; ok {
		t.Fatalf("封面缺省就是开，开着时**不该**往载荷里放字段：%+v", p)
	}
}

func TestDownloadPrefsRespectsSwitches(t *testing.T) {
	h := newHarness(t)
	h.it.cfg.LyricAutoDownload = func() bool { return false }
	h.it.cfg.CoverEmbed = func() bool { return false }
	p := h.it.downloadPrefs()
	if _, ok := p["embed_lyric"]; ok {
		t.Fatalf("关掉歌词后不该再传 embed_lyric：%+v", p)
	}
	if _, ok := p["write_lrc"]; ok {
		t.Fatalf("关掉歌词后不该再传 write_lrc：%+v", p)
	}
	if p["embed_cover"] != false {
		t.Fatalf("关掉封面必须显式传 false（下载接口缺省是开）：%+v", p)
	}
}

func TestAutoDownloadPayloadCarriesDownloadPrefs(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }
	// 歌词关、封面关 → 载荷里该体现出来
	h.it.cfg.LyricAutoDownload = func() bool { return false }
	h.it.cfg.CoverEmbed = func() bool { return false }

	fake := registerOnline(h, "w901", "丙", "丁")
	w, _ := h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)
	if env := decode(t, w); asInt(env["code"]) != 0 {
		t.Fatalf("收藏该成功：%+v", env)
	}
	if !waitDownloaded(t, h, fake) {
		t.Fatal("该触发下载")
	}
	got := (*seen)[0]
	if _, ok := got["embed_lyric"]; ok {
		t.Errorf("歌词关着，载荷不该带 embed_lyric：%+v", got)
	}
	if got["embed_cover"] != false {
		t.Errorf("封面关着，载荷该显式传 false：%+v", got)
	}
}

// ② 「如实标注实际来源」：换了源就要记下来，不能只留原平台。
//
// 界面（下载队列 / 注入面板）拿这个字段告诉用户「这首歌是从哪儿、什么音质下来的」——
// 只显示原平台是在骗人。
func TestAutoDownloadRecordsActualSourceAfterSwitch(t *testing.T) {
	h := newHarness(t)
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }
	// 下载源池里放一个同名的无损版本
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgid", Songmid: "mgid", Name: "甲", Source: "mg", Quality: "无损"}}
	}
	fake := registerOnline(h, "w902", "甲", "乙")
	// ⚠️ 必须在 registerOnline **之后**注册 —— 它会按可播表重建整个池，
	// 先注册会被冲掉（表现是「该平台没有可用的直链解析器」）。
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgid": {URL: "https://cdn.invalid/mgid.flac", Format: "flac", Size: 8192},
	}})
	w, _ := h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)
	if env := decode(t, w); asInt(env["code"]) != 0 {
		t.Fatalf("收藏该成功：%+v", env)
	}
	if !waitDownloaded(t, h, fake) {
		t.Fatal("该触发下载")
	}
	item, _ := h.it.downloaded.Get(fake)
	if item.Source != "mg" {
		t.Fatalf("⚠️ 换了源就必须如实记录实际来源，得到 %q（原平台是 wy）", item.Source)
	}
	if qualityRank(item.Quality) != qualityRank("无损") {
		t.Fatalf("实际音质该一起记下来：%q", item.Quality)
	}
}

// onlineTrackForTest 造一条与 registerOnline 注册进去的那条一致的在线曲目
// （duration 必须对得上 —— 池里的候选要过时长判定）。
func onlineTrackForTest(id, title, artist string) online.Track {
	return online.Track{
		Platform: "wy", PlatformID: id, Title: title, Artists: []string{artist},
		Album: "专辑", Duration: 245, CoverURL: "https://cdn.invalid/c.jpg",
	}.Normalized()
}

// 「坏档换源重试」：某一个源坏了（解析不出直链 / 下下来不是音频 / CDN 挂了）
// 不该让整首歌失败 —— 换下一个候选再试，最后兜底曲目自己平台。
//
// 参考实现里对应「坏档拉黑并换 mp3 重试一次」那一步。
func TestDownloadToLibraryRetriesNextCandidate(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	var mu sync.Mutex
	urls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		u, _ := p["url"].(string)
		mu.Lock()
		urls = append(urls, u)
		n := len(urls)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// 第一个候选（池里的 mg）—— 假装「下下来的不是音频」
			_, _ = w.Write([]byte(`{"code":500,"message":"bad","data":{"error":"不是音频文件"}}`))
			return
		}
		dst := filepath.Join(dir, "甲 - 乙.mp3")
		_ = os.WriteFile(dst, []byte("ID3\x03\x00\x00\x00FAKE"), 0o644)
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"path":"` + dst + `"}}`))
	}))
	defer srv.Close()
	h.it.localAPI = srv.URL

	// 池里有一个同名的无损版本（比曲目自己的空档位更好）
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgid", Songmid: "mgid", Name: "甲", Source: "mg", Quality: "无损"}}
	}
	fake := registerOnline(h, "w910", "甲", "乙")
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgid": {URL: "https://cdn.invalid/mgid.flac", Format: "flac", Size: 8192},
	}})

	item, err := h.it.downloadToLibrary(context.Background(), fake, onlineTrackForTest("w910", "甲", "乙"))
	if err != nil {
		t.Fatalf("第一个源坏了也该换下一个源成功：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 2 {
		t.Fatalf("该试两个候选（池里的 mg → 曲目自己 wy），实际 %d 次：%v", len(urls), urls)
	}
	if !strings.Contains(urls[0], "mgid") {
		t.Fatalf("第一个该试池里的高音质候选：%v", urls)
	}
	if !strings.Contains(urls[1], "w910") {
		t.Fatalf("第二个该兜底曲目自己平台：%v", urls)
	}
	// 登记的是**实际成功那条**的来源
	if item.Source != "wy" {
		t.Fatalf("最终成功的是 wy，登记该记 wy：%q", item.Source)
	}
}

// 候选全失败 → 报错（不能静默返回一个空 item 让上层以为下好了）
func TestDownloadToLibraryFailsWhenAllCandidatesFail(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":500,"message":"bad","data":{"error":"坏档"}}`))
	}))
	defer srv.Close()
	h.it.localAPI = srv.URL
	fake := registerOnline(h, "w911", "甲", "乙")

	if _, err := h.it.downloadToLibrary(context.Background(), fake, onlineTrackForTest("w911", "甲", "乙")); err == nil {
		t.Fatal("⚠️ 全失败必须报错，不能返回空 item")
	}
}

// ③ 「加入歌单」也要自动下载并绑定本地。
//
// 用户指令写的是「收藏 / **加入歌单** → 自动下载并绑定本地」—— 两条路径必须是同一个
// 行为。以前只有收藏那条挂了钩子，往歌单里加一首在线歌什么都不会发生。
// （参考实现 fnmusic-ext 的 `_register_fav_autobind` 正是在收藏与加入歌单**两处**都调用。）
func TestPlaylistAddTrackTriggersAutoDownload(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }

	// 用 registerOnline：它同时把「这条能解析出直链」登记进去 ——
	// 只 registry.Put 的话取音会以「该平台不可播放」失败，测的就不是触发时机了。
	fake := registerOnline(h, "1", "在线歌", "甲")
	h.setOfficialFunc(func(_ *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":null}`)
	})

	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"playlist-1","trackGUIDs":["`+fake+`"]}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("加入歌单本身必须成功（下载是附加动作）：%+v", env)
	}
	if !waitDownloaded(t, h, fake) {
		t.Fatal("⚠️ 加入歌单也该触发自动下载 —— 用户指令写的是「收藏 / 加入歌单」")
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	if (*seen)[0]["name"] != "在线歌" {
		t.Fatalf("载荷该是这首歌：%+v", (*seen)[0])
	}
}

// 开关关着时，「加入歌单」也不该下载（与收藏那条同一个开关）。
func TestPlaylistAddTrackSkipsDownloadWhenSwitchOff(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return false }

	fake := registerOnline(h, "2", "甲", "乙")
	h.setOfficialFunc(func(_ *http.Request, _ []byte) *http.Response {
		return jsonResp(http.StatusOK, `{"code":0,"msg":"","data":null}`)
	})
	w, _ := h.do(http.MethodPost, "/music/api/v1/playlist/add-track",
		`{"guid":"playlist-1","trackGUIDs":["`+fake+`"]}`, nil)
	if env := decode(t, w); env["code"] != float64(0) {
		t.Fatalf("加入歌单该成功：%+v", env)
	}
	time.Sleep(300 * time.Millisecond)
	if len(*seen) != 0 {
		t.Fatal("开关关着时不该下载")
	}
	if _, ok := h.it.downloaded.Get(fake); ok {
		t.Fatal("开关关着时不该登记")
	}
}

// 端到端：**收藏 → 后台下载 → 登记 → 取流改走本地**。
//
// 这条链路的每一段都有单测（收藏触发下载 / 下载登记 / 取流优先本地），但
// 「**串起来还通**」没有自动化守住 —— 而它正是用户实际感受到的那条路：
// 点红心 → 文件落到 NAS → 以后听的是硬盘那份。
//
// **分段绿 ≠ 整条绿**：中间任何一处的契约变了（比如登记表的键、取流查登记表的时机、
// 下载接口的应答形状），分段用例都可能各自仍然绿，而整条路已经断了。
func TestFavoriteToLocalStreamEndToEnd(t *testing.T) {
	h := newHarness(t)
	srv, seen, dst := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }

	fake := registerOnline(h, "w950", "甲", "乙")

	// ① 用户在飞牛里点红心。
	w, _ := h.do(http.MethodPost, apiPrefix+"/favorite-track/create", `{"guid":"`+fake+`"}`, nil)
	if env := decode(t, w); asInt(env["code"]) != 0 {
		t.Fatalf("收藏该成功：%+v", env)
	}

	// ② 后台下载完成并登记（不占播放路径，所以这里是等出来的）。
	if !waitDownloaded(t, h, fake) {
		t.Fatal("该触发后台下载并登记")
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}

	// ③ 取流改走本地文件：要带 Range 语义，而且**内容必须就是磁盘上那份**。
	w2, _ := h.do(http.MethodGet, apiPrefix+"/track/stream?guid="+fake, "",
		map[string]string{"Range": "bytes=0-9"})
	if w2.Code != http.StatusPartialContent {
		t.Fatalf("该回 206（本地文件 + Range），得到 %d：%s", w2.Code, w2.Body.String())
	}
	onDisk, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) < 10 {
		t.Fatalf("假下载后端写的文件太小：%d", len(onDisk))
	}
	if got := w2.Body.String(); got != string(onDisk[:10]) {
		t.Fatalf("回的内容该是磁盘上那份：got %q want %q", got, string(onDisk[:10]))
	}
}
