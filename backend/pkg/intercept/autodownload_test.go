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
func fakeDownloadBackend(t *testing.T) (*httptest.Server, *[]map[string]any, func() string) {
	t.Helper()
	dir := t.TempDir()
	seen := &[]map[string]any{}
	var (
		mu       sync.Mutex
		lastPath string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download/song" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		// ⚠️ 落盘文件的扩展名要跟着**直链**走：真实下载器是按魔数嗅探决定扩展名的
		// （pkg/downloader 的 detectAudioExtByMagic），而 `tierFromLandedFile` 会拿
		// 这个扩展名校正登记表里的档位。假后端恒写 .mp3 的话，「下了无损」的用例
		// 会被如实校正成 320k —— 假的是后端，不是被测逻辑。
		ext, head := ".mp3", "ID3\x03\x00\x00\x00FAKEAUDIO"
		if u, _ := m["url"].(string); strings.Contains(strings.ToLower(u), ".flac") {
			ext, head = ".flac", "fLaC\x00\x00\x00\x22FAKEFLAC"
		}
		dst := filepath.Join(dir, "甲 - 乙"+ext)
		if err := os.WriteFile(dst, []byte(head), 0o644); err != nil {
			t.Error(err)
		}
		mu.Lock()
		*seen = append(*seen, m)
		lastPath = dst
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"status": "success", "path": dst, "song_name": "甲"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, seen, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastPath
	}
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
	srv, seen, dstOf := fakeDownloadBackend(t)
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
	dst := dstOf()
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
	// ⚠️ 原来的 `time.Sleep(200ms)` 是一条**能被调度器偏心蒙混过去**的断言，所以它
	// 在本地多核机器上一直是绿的，到 2 核的 GitHub runner 上就红（同一首歌下了 2 次）。
	// 改成等「只下一次」这个状态本身：两次触发都已经同步返回，后台 goroutine 最多
	// 一个 —— 多出来的那次要么已经打到假后端，要么永远打不到（首个完成后
	// unmarkDownloading 才放行第二个），所以这里不用猜时间，等 5 秒为上限。
	deadline := time.Now().Add(5 * time.Second)
	for len(*seen) > 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(*seen) != 1 {
		t.Fatalf("同一首歌只该下一次，实际 %d 次", len(*seen))
	}
}

// TestBeginDownloadIsAtomic 直接压 beginDownload：「判断该不该下」和「占位」必须是
// 一次原子操作。这不是重复上面那条端到端用例 —— 那条走 HTTP，两次触发之间隔着
// 一大段处理链，撞上的概率取决于核数与调度；这条把 64 个并发**直接**怼在同一首歌
// 上，谁把两步拆开谁立刻红。
func TestBeginDownloadIsAtomic(t *testing.T) {
	h := newHarness(t)
	// 前提（与 autoDownload 开头那两道闸一致）：下载目标接口在、登记表在。
	// 缺这两个时 beginDownload 一律不放行 —— 那是「不下」而不是「撞车」。
	srv, _, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.favAutoDownload = func() bool { return true }
	fake := registerOnline(h, "w910", "甲", "乙")

	const n = 64
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
	)
	wg.Add(n)
	for k := 0; k < n; k++ {
		go func() {
			defer wg.Done()
			if ok, _ := h.it.beginDownload(fake); ok {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claimed != 1 {
		t.Fatalf("同一首歌 64 个并发触发只该有一个占到名额，实际 %d 个", claimed)
	}
	// 没释放之前再来一个也不能占（占位要在整个下载期间有效）。
	if ok, _ := h.it.beginDownload(fake); ok {
		t.Fatal("下载没结束时不该再放行第二个")
	}
	h.it.unmarkDownloading(fake)
	if ok, _ := h.it.beginDownload(fake); !ok {
		t.Fatal("上一个下完之后应该能被再次触发（换源重下就是靠这条）")
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
	srv, seen, dstOf := fakeDownloadBackend(t)
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
	onDisk, err := os.ReadFile(dstOf())
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

// ── 下载音质：按档位阶梯取音（v2.13.0）─────────────────────────────────
//
// 用户 2026-10-20 报：「收藏自动下载怎么下的是 mp3，不是各途径里最高的音质」。
// 根因是**下载这条路走的是 Pool.Resolve**，档位由平台自己定 —— 网易云那条
// 免登录接口固定 br=320000，于是同一首歌「听是无损、下是 mp3」。
// 下面这组用例守的就是「下载必须按 flac → 320k → 128k 去问」。

// tierStubResolver 是带档位能力的假解析器：记下被问的档位，按档位给不同结果。
type tierStubResolver struct {
	platform string
	mu       sync.Mutex
	calls    []string
	answers  map[string]map[string]*online.Resolved // platformID → tier → 结果
}

func (s *tierStubResolver) Platform() string { return s.platform }

func (s *tierStubResolver) Resolve(context.Context, string) (*online.Resolved, error) {
	return nil, online.ErrNotPlayable
}

func (s *tierStubResolver) ResolveBatch(context.Context, []string) (map[string]*online.Resolved, error) {
	return nil, online.ErrNotPlayable
}

func (s *tierStubResolver) ResolveAtTier(_ context.Context, id, tier string) (*online.Resolved, error) {
	s.mu.Lock()
	s.calls = append(s.calls, tier)
	s.mu.Unlock()
	if byTier, ok := s.answers[id]; ok {
		if r, ok := byTier[tier]; ok {
			return r, nil
		}
	}
	return nil, online.ErrTierUnavailable
}

func (s *tierStubResolver) requestedTiers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// downloadTiers 的阶梯必须与试听同源：highest 走完整阶梯，
// 点名档位时只往下退，**绝不往上问**。
func TestDownloadTiers(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		setting string
		want    []string
	}{
		{"highest", []string{online.TierLossless, online.Tier320k, online.Tier128k}},
		{"", []string{online.TierLossless, online.Tier320k, online.Tier128k}},
		{online.TierLossless, []string{online.TierLossless, online.Tier320k, online.Tier128k}},
		{online.Tier320k, []string{online.Tier320k, online.Tier128k}},
		{online.Tier192k, []string{online.Tier192k, online.Tier128k}},
		{online.Tier128k, []string{online.Tier128k}},
	}
	for _, c := range cases {
		setting := c.setting
		h.it.cfg.AutoDownloadQuality = func() string { return setting }
		got := h.it.downloadTiers()
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("档位偏好 %q 的阶梯该是 %v，得到 %v", setting, c.want, got)
		}
	}
}

// 「音质下载 = 最高」时，下载必须**先问无损档**，并如实登记实际拿到的档位。
func TestDownloadToLibraryAsksHighestTierFirst(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL

	res := &tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w920": {
			online.TierLossless: {URL: "https://cdn.invalid/w920.flac", Format: "flac", Size: 30 << 20, Tier: online.TierLossless},
			online.Tier320k:     {URL: "https://cdn.invalid/w920.mp3", Format: "mp3", Size: 9 << 20, Tier: online.Tier320k},
		},
	}}
	fake := registerOnline(h, "w920", "甲", "乙")
	h.it.pool = online.NewPool(res) // ⚠️ 必须在 registerOnline 之后 —— 它会重建池

	item, err := h.it.downloadToLibrary(context.Background(), fake, onlineTrackForTest("w920", "甲", "乙"))
	if err != nil {
		t.Fatalf("该下载成功：%v", err)
	}
	calls := res.requestedTiers()
	if len(calls) == 0 || calls[0] != online.TierLossless {
		t.Fatalf("⚠️ 下载必须先从无损档开始问，实际问的是 %v", calls)
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	if u, _ := (*seen)[0]["url"].(string); !strings.Contains(u, "w920.flac") {
		t.Fatalf("该下无损那条直链，实际是 %q", u)
	}
	// 登记表要写**实际**档位，而不是「搜索时声明的那个」
	if item.Quality != online.TierLossless {
		t.Fatalf("登记的音质该是实际解析到的 flac，得到 %q", item.Quality)
	}
}

// 反过来：池里的候选**只声明 320k** 时，不能把曲目自己实际解析出来的无损顶掉。
//
// 收藏场景里曲目自己常常不报档位（Quality 为空），这时只看声明值就会让
// 池里那条 320k 抢先 —— 正是用户看到「下到 mp3」的另一条路径。
func TestDownloadToLibraryNeverDowngradesToDeclaredPoolCandidate(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgid", Songmid: "mgid", Name: "甲", Source: "mg", Quality: "320k"}}
	}

	res := &tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w921": {online.TierLossless: {URL: "https://cdn.invalid/w921.flac", Format: "flac", Size: 30 << 20, Tier: online.TierLossless}},
	}}
	fake := registerOnline(h, "w921", "甲", "乙")
	h.it.pool = online.NewPool(res)
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgid": {URL: "https://cdn.invalid/mgid.mp3", Format: "mp3", Size: 8 << 20},
	}})

	// 曲目自己**不报档位**（收藏进来的在线歌常常如此）
	if _, err := h.it.downloadToLibrary(context.Background(), fake, onlineTrackForTest("w921", "甲", "乙")); err != nil {
		t.Fatalf("该下载成功：%v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	if u, _ := (*seen)[0]["url"].(string); !strings.Contains(u, "w921.flac") {
		t.Fatalf("⚠️ 曲目自己实际能拿到无损，不该换成池里那条 320k：%q", u)
	}
}

// 池里那条**真的更好**时，下载要换源 —— 并且如实登记「从哪个源、什么档位」下来的。
//
// 这是「各途径里最高音质」的正向路径：曲目自己只有 320k，池里（咪咕/洛雪）有无损。
func TestDownloadToLibrarySwitchesToPoolWhenPoolIsActuallyBetter(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.cfg.AutoDownloadQuality = func() string { return qualityHighest }
	h.it.cfg.DownloadSources = func() []string { return []string{"mg"} }
	h.it.searcher = func(_ string, _ string, _, _ int) []search.UnifiedSong {
		return []search.UnifiedSong{{ID: "mgflac", Songmid: "mgflac", Name: "甲", Source: "mg", Quality: "flac", FileSize: 30 << 20}}
	}

	// 曲目自己（网易云）只有 320k
	own := &tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w922": {online.Tier320k: {URL: "https://cdn.invalid/w922.mp3", Format: "mp3", Size: 9 << 20, Tier: online.Tier320k}},
	}}
	fake := registerOnline(h, "w922", "甲", "乙")
	h.it.pool = online.NewPool(own)
	h.it.pool.Register(&stubResolver{platform: "mg", answers: map[string]*online.Resolved{
		"mgflac": {URL: "https://cdn.invalid/mgflac.flac", Format: "flac", Size: 30 << 20},
	}})

	item, err := h.it.downloadToLibrary(context.Background(), fake, onlineTrackForTest("w922", "甲", "乙"))
	if err != nil {
		t.Fatalf("该下载成功：%v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	if u, _ := (*seen)[0]["url"].(string); !strings.Contains(u, "mgflac.flac") {
		t.Fatalf("⚠️ 池里有真无损就该换源取它，实际是 %q", u)
	}
	if item.Source != "mg" || item.Quality != online.TierLossless {
		t.Fatalf("登记该如实写换源后的来源与实际档位：%+v", item)
	}
}

// 酷狗/酷我：曲率原生搜得到、但没有自己的解析器 —— 候选必须改道 `lx:<平台>`。
//
// 用户 2026-10-20 的原话：「把落雪和 musicdl 利用起来，收藏自动下载可以走它们的链接」。
// 这条用例守的是「改道」这一步：候选身份不换成 lx:kg，池只会回 ErrNoResolver，
// 于是搜到的无损候选永远参与不了下载。
func TestRankedSourcesBridgesKugouToLx(t *testing.T) {
	h := newHarness(t)
	// 原生搜索：酷狗能给出无损（曲率自己的 search 覆盖 kg/kw）
	h.it.searcher = func(_ string, platform string, _, _ int) []search.UnifiedSong {
		if platform != "kg" {
			return nil
		}
		return []search.UnifiedSong{{
			ID: "HASH1", Songmid: "HASH1", Name: "甲", Singer: "乙", Album: "专辑",
			Duration: 245000, Quality: "flac24bit", Source: "kg",
		}}
	}
	// 洛雪取链那一半（宿主）
	h.it.lxResolve = func(_ context.Context, source string, info map[string]any, quality string) (string, int64, error) {
		if source != "kg" || info["id"] != "HASH1" {
			return "", 0, online.ErrNotPlayable
		}
		return "https://cdn.example/HASH1.flac", 0, nil
	}

	list := h.it.rankedSources(context.Background(), "甲", "乙", 245)
	if len(list) == 0 {
		t.Fatal("酷狗那条无损候选该进池")
	}
	got := list[0]
	if got.Platform != "lx:kg" {
		t.Fatalf("酷狗候选该改道洛雪取链（lx:kg），得到 %q", got.Platform)
	}
	if got.PlatformID != "HASH1" {
		t.Fatalf("改道后 id 必须还是酷狗那条（脚本按它取链），得到 %q", got.PlatformID)
	}
	if online.TierRank(got.Quality) < online.TierRank(online.TierLossless) {
		t.Fatalf("声明档位该跟着候选走（否则它会被『严格更好』那道筛挡掉），得到 %q", got.Quality)
	}

	// 暂存里必须有脚本取链要的那份 info（歌名/歌手/时长），否则脚本拒绝
	info := h.it.lxItem("kg", "HASH1")
	if info == nil {
		t.Fatal("改道时必须把这条曲目的 info 交给洛雪")
	}
	if info["name"] != "甲" || info["singer"] != "乙" {
		t.Fatalf("info 该带歌名与歌手（脚本要按它匹配），得到 %v", info)
	}
	if d, _ := info["duration"].(int); d != 245 {
		t.Fatalf("info 里的时长该是秒（245），得到 %v", info["duration"])
	}
}

// 曲目自己平台只能拿到 320k 时，洛雪能用**同一个平台 id** 拿到无损 —— 就该用它。
//
// 这是「免登录网易云只给 128k/320k」的正解：第三方聚合站不吃登录态。
func TestDownloadToLibraryUsesLxWhenNativeIsLossy(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL

	// 原生解析（网易）：按阶梯只能到 320k
	h.it.pool = online.NewPool(&tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w930": {online.Tier320k: {URL: "https://cdn.invalid/w930.mp3", Format: "mp3", Size: 9 << 20, Tier: online.Tier320k}},
	}})
	// 洛雪：同一个网易 id → 无损
	var lxSource string
	h.it.lxResolve = func(_ context.Context, source string, info map[string]any, quality string) (string, int64, error) {
		lxSource = source
		if info["id"] != "w930" {
			return "", 0, online.ErrNotPlayable
		}
		return "https://cdn.example/w930.flac", 0, nil
	}

	fake := registerOnline(h, "w930", "甲", "乙")
	track := online.Track{
		Platform: "wy", PlatformID: "w930", Title: "甲", Artists: []string{"乙"},
		Album: "专辑", Duration: 245,
	}
	item, err := h.it.downloadToLibrary(context.Background(), fake, track)
	if err != nil {
		t.Fatalf("洛雪补强该成功：%v", err)
	}
	if lxSource != "wy" {
		t.Fatalf("补强该用曲目自己平台的 id 去问洛雪（source=wy），得到 %q", lxSource)
	}
	if len(*seen) != 1 {
		t.Fatalf("该只调一次下载接口，实际 %d 次", len(*seen))
	}
	url, _ := (*seen)[0]["url"].(string)
	if !strings.Contains(url, "w930.flac") {
		t.Fatalf("该下洛雪给的无损直链，实际下了 %q", url)
	}
	if item.Source != "lx:wy" {
		t.Fatalf("登记的实际来源该是洛雪，得到 %q", item.Source)
	}
	if online.TierRank(item.Quality) < online.TierRank(online.TierLossless) {
		t.Fatalf("登记的实际档位该是无损，得到 %q", item.Quality)
	}
}

// 用户点名 320k/128k 时**不许**去问洛雪要无损：那是省流量，不是没本事。
func TestDownloadToLibrarySkipsLxWhenUserAsksLowerTier(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.cfg.AutoDownloadQuality = func() string { return online.Tier320k }

	asked := false
	h.it.lxResolve = func(context.Context, string, map[string]any, string) (string, int64, error) {
		asked = true
		return "https://cdn.example/x.flac", 0, nil
	}
	h.it.pool = online.NewPool(&tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w931": {online.Tier320k: {URL: "https://cdn.invalid/w931.mp3", Format: "mp3", Size: 9 << 20, Tier: online.Tier320k}},
	}})

	fake := registerOnline(h, "w931", "甲", "乙")
	track := online.Track{
		Platform: "wy", PlatformID: "w931", Title: "甲", Artists: []string{"乙"},
		Album: "专辑", Duration: 245,
	}
	if _, err := h.it.downloadToLibrary(context.Background(), fake, track); err != nil {
		t.Fatalf("该照常下 320k：%v", err)
	}
	if asked {
		t.Fatal("点名 320k 时不该去问洛雪要无损（违背用户选择、还白等几十秒）")
	}
	if len(*seen) != 1 {
		t.Fatalf("该只下 320k 那一份，实际 %d 次", len(*seen))
	}
}

// 洛雪给的结果**不比原生更好**时不采用：白换一次下载没有意义（还可能是别家转码）。
//
// v2.16 之前这是 `downloadViaLx` 的一个 baseScore 入参；渠道化（v2.17）之后它变成
// 「所有渠道进同一张表、按实际档位排序 + 同档位不换源」的自然结果 —— 断言跟着挪到
// 这个结果上：原生如实回读 320k，洛雪的直链露馅是 mp3（`lxURLLooksLossy` 降回 320k）
// → 同档，赢家必须是原生，而且**只下一次**。
func TestDownloadToLibraryKeepsNativeWhenLxIsNotBetter(t *testing.T) {
	h := newHarness(t)
	srv, seen, _ := fakeDownloadBackend(t)
	h.it.localAPI = srv.URL
	h.it.lxResolve = func(context.Context, string, map[string]any, string) (string, int64, error) {
		return "https://cdn.example/x.mp3", 0, nil
	}
	fake := registerOnline(h, "w932", "甲", "乙")
	// ⚠️ 必须在 `registerOnline` **之后**换池：`registerOnline → setOnline` 会用
	// `poolFrom(playable)` 覆盖 `h.it.pool`，先设就被冲掉（那个替身只回 `Format: mp3`、
	// 不回读档位，于是原生算 -1 分，测试就变成「比的是假池而不是比的是判据」）。
	// 原生解析器这里**如实回读**档位（真网易云就是这么干的），两条才真的同档可比。
	h.it.pool = online.NewPool(&tierStubResolver{platform: "wy", answers: map[string]map[string]*online.Resolved{
		"w932": {online.Tier320k: {URL: "https://cdn.invalid/w932.mp3", Format: "mp3", Size: 9 << 20, Tier: online.Tier320k}},
	}})
	track := online.Track{
		Platform: "wy", PlatformID: "w932", Title: "甲", Artists: []string{"乙"}, Duration: 245,
	}
	item, err := h.it.downloadToLibrary(context.Background(), fake, track)
	if err != nil {
		t.Fatalf("该用原生那条下成功：%v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("该只下一次，实际 %d 次", len(*seen))
	}
	if url, _ := (*seen)[0]["url"].(string); strings.Contains(url, "cdn.example") {
		t.Fatalf("同档位不该换成洛雪的链接：%q", url)
	}
	if item.Source == "lx:wy" {
		t.Fatalf("登记的实际来源该还是原生 wy，得到 %q", item.Source)
	}
}

// 「已下载过」不等于「永远不再下载」：库里是有损、这次能拿无损时，允许重下一次。
//
// 边界必须钉死，否则会退化成「每次收藏都重下一遍」（平台就是不给无损时纯浪费流量）。
func TestShouldRetryUpgrade(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "甲 - 乙.mp3")
	if err := os.WriteFile(mp3, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	flac := filepath.Join(dir, "甲 - 乙.flac")
	if err := os.WriteFile(flac, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.it.cfg.AutoDownloadQuality = func() string { return "highest" }
	if !h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k"}) {
		t.Fatal("有损文件 + 想要无损 → 应该允许升级重下")
	}
	// v2.17 起「试过一回」不再等于「永久放弃」：真正拦住重复下载的是冷却位。
	// 只钉 UpgradeTried 而没有 UpgradeNext（= 老版本写下的记录）→ 可以再试，
	// 这批欠账正是这次改动要救回来的（第三方站今天 404 明天可能就好了）。
	if !h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k", UpgradeTried: true}) {
		t.Fatal("老记录只有 UpgradeTried → 按新口径应该允许再试一次")
	}
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k",
		UpgradeTried: true, UpgradeTries: 1, UpgradeNext: time.Now().Add(time.Hour).Unix()}) {
		t.Fatal("冷却没到就该拦住（否则每次收藏都白下一遍）")
	}
	if !h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k",
		UpgradeTried: true, UpgradeTries: 1, UpgradeNext: time.Now().Add(-time.Minute).Unix()}) {
		t.Fatal("冷却过了应该放行")
	}
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k",
		UpgradeTries: upgradeMaxTries, UpgradeNext: time.Now().Add(-time.Hour).Unix()}) {
		t.Fatalf("试满 %d 回就该收手，剩下的交给「替换音质」页", upgradeMaxTries)
	}
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: flac, Quality: "flac"}) {
		t.Fatal("已经是无损的不该重下")
	}
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "flac"}) {
		t.Fatal("记录说无损就不该按有损处理")
	}
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: filepath.Join(dir, "missing.mp3"), Quality: "320k"}) {
		t.Fatal("文件已不在了 → 不是升级场景")
	}

	h.it.cfg.AutoDownloadQuality = func() string { return "320k" }
	if h.it.shouldRetryUpgrade(online.DownloadedItem{Path: mp3, Quality: "320k"}) {
		t.Fatal("用户点名 320k 时不该去要无损")
	}
}
