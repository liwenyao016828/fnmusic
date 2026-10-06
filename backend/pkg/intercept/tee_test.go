package intercept

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fn-lx-player/pkg/online"
)

// 「边听边下」（tee）的用例。
//
// 这一层最容易出的错不是「没存下来」，而是**把半截文件当成品登记进曲库** ——
// 那首歌之后会一直播到一半停，而用户以为已经下好了。所以用例重点在「不完整必须丢弃」。

func teeTrack(id, title, artist string) online.Track {
	return online.Track{Platform: "wy", PlatformID: id, Title: title, Artists: []string{artist}, Quality: "320k"}
}

// teeHarness 造一个开了 tee 的 harness，返回下载目录。
func teeHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	dir := t.TempDir()
	h.it.cfg.TeeEnabled = func() bool { return true }
	h.it.cfg.DownloadDir = func() string { return dir }
	return h, dir
}

func TestTeePromotesCompleteFile(t *testing.T) {
	h, dir := teeHarness(t)
	body := "ID3\x03\x00\x00\x00fake-audio-payload"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g1", nil)
	tr := teeTrack("1", "晴天", "周杰伦")

	tt := h.it.beginTee("g1", tr, resp, req)
	if tt == nil {
		t.Fatal("开关开着、完整响应、没在库里 → 该开 tee")
	}
	var sink bytes.Buffer
	if _, err := io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	h.it.finishTee(tt, "g1", tr, resp.ContentLength, "mp3")

	if sink.String() != body {
		t.Fatalf("客户端该收到完整字节，拿到 %q", sink.String())
	}
	it, ok := h.it.downloaded.Get("g1")
	if !ok {
		t.Fatal("完整拿到就该登记进曲库")
	}
	if !strings.HasSuffix(it.Path, ".mp3") {
		t.Fatalf("扩展名该跟着格式走：%s", it.Path)
	}
	if !strings.HasPrefix(it.Path, dir) {
		t.Fatalf("该落在下载目录里：%s", it.Path)
	}
	if b, err := os.ReadFile(it.Path); err != nil || string(b) != body {
		t.Fatalf("落盘内容不对：%v", err)
	}
	// tee 存的就是客户端正在听的那条流 → 取音来源必然是曲目自己平台
	if it.Source != "wy" {
		t.Fatalf("该如实记录取音来源：%q", it.Source)
	}
	// 暂存目录不该留下 .part
	left, _ := filepath.Glob(filepath.Join(dir, teeSubdir, "*.part"))
	if len(left) != 0 {
		t.Fatalf("提升之后不该留下暂存文件：%v", left)
	}
}

func TestTeeDiscardsIncompleteFile(t *testing.T) {
	h, dir := teeHarness(t)
	// 上游声明 100 字节，实际只转发了 7 字节（用户中途切歌 / 断流）
	resp := &http.Response{StatusCode: 200, ContentLength: 100, Body: io.NopCloser(strings.NewReader(""))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g2", nil)
	tr := teeTrack("2", "半截歌", "某人")

	tt := h.it.beginTee("g2", tr, resp, req)
	if tt == nil {
		t.Fatal("该开 tee")
	}
	var sink bytes.Buffer
	_, _ = (&teeWriter{w: &sink, t: tt}).Write([]byte("ID3fake"))
	h.it.finishTee(tt, "g2", tr, resp.ContentLength, "mp3")

	if _, ok := h.it.downloaded.Get("g2"); ok {
		t.Fatal("⚠️ 半截文件绝不能登记 —— 那首歌会一直播到一半停")
	}
	left, _ := filepath.Glob(filepath.Join(dir, teeSubdir, "*"))
	if len(left) != 0 {
		t.Fatalf("未完成的暂存该被删掉：%v", left)
	}
}

func TestTeeSkipsWhenItShould(t *testing.T) {
	h, dir := teeHarness(t)
	tr := teeTrack("3", "甲", "乙")
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g3", nil)
	full := func() *http.Response {
		return &http.Response{StatusCode: 200, ContentLength: 10, Body: io.NopCloser(strings.NewReader(""))}
	}

	if tt := h.it.beginTee("g3", tr, full(), req); tt == nil {
		t.Fatal("基线：该开 tee")
	}
	// ① 开关关着 → **不进曲库**：曲库暂存目录里不该多出文件。
	// （它会改走滚动试听缓存 —— 那是另一条路径，见 tee_rolling_test.go。）
	before, _ := filepath.Glob(filepath.Join(dir, teeSubdir, "*"))
	h.it.cfg.TeeEnabled = func() bool { return false }
	if tt := h.it.beginTee("g3", tr, full(), req); tt != nil && !tt.rolling {
		t.Fatal("开关关着绝不能走「提升进曲库」那条路")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, teeSubdir, "*")); len(left) != len(before) {
		t.Fatalf("开关关着时不该往曲库暂存目录里写：%v → %v", before, left)
	}
	h.it.cfg.TeeEnabled = func() bool { return true }
	// ② 206 分片
	r206 := full()
	r206.StatusCode = http.StatusPartialContent
	if tt := h.it.beginTee("g3", tr, r206, req); tt != nil {
		t.Fatal("206 分片拼不成完整文件，不该 tee")
	}
	// ③ 中段 Range（拖动/续传）
	rmid := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g3", nil)
	rmid.Header.Set("Range", "bytes=5000-")
	if tt := h.it.beginTee("g3", tr, full(), rmid); tt != nil {
		t.Fatal("中段 Range 不该 tee")
	}
	// ④ 上游没给总长 → 没法判断完整
	rnolen := full()
	rnolen.ContentLength = -1
	if tt := h.it.beginTee("g3", tr, rnolen, req); tt != nil {
		t.Fatal("不知道总长就不该 tee（判断不了完整）")
	}
	// ⑤ 已经在库里
	h.it.downloaded.Put(online.DownloadedItem{GUID: "g4", Path: mustWrite(t, "x.mp3", "abc")})
	rq := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g4", nil)
	if tt := h.it.beginTee("g4", tr, full(), rq); tt != nil {
		t.Fatal("已经在库里了，别再存一份")
	}
}

func TestTeeSurvivesDiskWriteFailure(t *testing.T) {
	// 盘写挂了**绝不能**影响播放 —— tee 是搭便车的，不是主角
	f, err := os.CreateTemp(t.TempDir(), "closed*")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // 之后任何写都会失败
	tt := &teeTarget{f: f}
	var sink bytes.Buffer
	n, err := (&teeWriter{w: &sink, t: tt}).Write([]byte("audio"))
	if err != nil || n != 5 || sink.String() != "audio" {
		t.Fatalf("写盘失败后仍该把字节交给客户端：n=%d err=%v body=%q", n, err, sink.String())
	}
	if tt.f != nil {
		t.Fatal("写盘失败后该把句柄丢掉，不再碰盘")
	}
}

func TestSanitizeFileNameAndExt(t *testing.T) {
	if got := sanitizeFileName("AC/DC - 晴天"); strings.ContainsAny(got, `/\`) {
		t.Fatalf("路径分隔符必须被换掉：%q", got)
	}
	if got := sanitizeFileName("a\x01b"); strings.ContainsRune(got, 1) {
		t.Fatalf("控制字符必须被换掉：%q", got)
	}
	if got := sanitizeFileName(strings.Repeat("长", 300)); len(got) > 120 {
		t.Fatalf("超长歌名该截断：%d", len(got))
	}
	for in, want := range map[string]string{"flac": ".flac", "MP3": ".mp3", "m4a": ".m4a", "": ".mp3", "谁知道": ".mp3"} {
		if got := extForFormat(in); got != want {
			t.Fatalf("extForFormat(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func mustWrite(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// tee 提升出来的文件**必须补标签**。
//
// tee 是直接复制流字节、没走下载接口，所以落盘的文件没有任何标签 —— 歌能听、能绑定，
// 但在飞牛里标题/歌手/专辑/封面/歌词全是空的。这个用例守的就是那条补标签的调用。
func TestTeeEnrichesTagsAfterPromotion(t *testing.T) {
	h, _ := teeHarness(t)
	var mu sync.Mutex
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/download/enrich" {
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			// 真的往文件里追加几个字节，模拟「写标签让文件变大」——
			// 否则「补完标签要更新登记大小」这条根本测不到。
			if p, _ := m["path"].(string); p != "" {
				if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
					_, _ = f.Write([]byte("TAGPAD"))
					_ = f.Close()
				}
			}
			mu.Lock()
			got = m
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"embedded":true}}`))
	}))
	defer srv.Close()
	h.it.localAPI = srv.URL

	body := "ID3\x03\x00\x00\x00fake-audio"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=g9", nil)
	tr := teeTrack("9", "晴天", "周杰伦")

	tt := h.it.beginTee("g9", tr, resp, req)
	if tt == nil {
		t.Fatal("该开 tee")
	}
	var sink bytes.Buffer
	_, _ = io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body)
	h.it.finishTee(tt, "g9", tr, resp.ContentLength, "mp3")

	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("⚠️ tee 提升之后必须调 /api/download/enrich，否则飞牛里元数据是空的")
	}
	if got["name"] != "晴天" || got["singer"] != "周杰伦" {
		t.Fatalf("补标签的载荷该带元数据：%+v", got)
	}
	if p, _ := got["path"].(string); p == "" {
		t.Fatalf("补标签的载荷必须带 path：%+v", got)
	}
	if got["embed_lyric"] != true || got["write_lrc"] != true {
		t.Fatalf("缺省该带上歌词：%+v", got)
	}
	// 补标签之后文件变大了，登记表里那份大小要跟上 ——
	// 否则「登记说 A、磁盘上 B」这种对不上会让人以为是别的问题。
	item, ok := h.it.downloaded.Get("g9")
	if !ok {
		t.Fatal("该已登记")
	}
	if item.Size != int64(len(body)+len("TAGPAD")) {
		t.Fatalf("补标签后登记大小该更新成落盘真实大小，得到 %d（期望 %d）",
			item.Size, len(body)+len("TAGPAD"))
	}
}

// 没接本机 API（老配置 / 单测环境）时不能崩，也不能调。
func TestTeeEnrichWithoutLocalAPI(t *testing.T) {
	h, _ := teeHarness(t)
	h.it.localAPI = ""
	body := "ID3fake"
	resp := &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	req := httptest.NewRequest(http.MethodGet, "/music/api/v1/track/stream?guid=gA", nil)
	tr := teeTrack("A", "甲", "乙")
	tt := h.it.beginTee("gA", tr, resp, req)
	var sink bytes.Buffer
	_, _ = io.Copy(&teeWriter{w: &sink, t: tt}, resp.Body)
	h.it.finishTee(tt, "gA", tr, resp.ContentLength, "mp3")
	if _, ok := h.it.downloaded.Get("gA"); !ok {
		t.Fatal("没有本机 API 也该完成提升（补标签只是锦上添花）")
	}
}
