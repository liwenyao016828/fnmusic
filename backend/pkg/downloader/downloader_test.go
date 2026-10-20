package downloader

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/tags"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"周杰伦", "周杰伦"},
		{`a/b\c:d*e?f"g<h>i|j`, "a_b_c_d_e_f_g_h_i_j"},
		{"  空格  ", "空格"},
		{"", "未知"},
		{"...", "未知"},
	}
	for _, tc := range cases {
		if got := sanitizeFilename(tc.in); got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, 期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeFilenameTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("歌", 200) // 600 字节
	got := sanitizeFilename(long)

	if len(got) > maxFilenameBytes {
		t.Errorf("截断后长度 = %d 字节, 期望 <= %d", len(got), maxFilenameBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("截断产生了非法 UTF-8 序列")
	}
	if len(got) == 0 {
		t.Error("截断后不应为空")
	}
	if !strings.HasPrefix(long, got) {
		t.Error("截断结果应为原字符串前缀")
	}
}

func TestAllocateUniquePath(t *testing.T) {
	dir := t.TempDir()
	base := "周杰伦 - 夜曲"

	first := allocateUniquePath(dir, base, "mp3")
	if filepath.Base(first) != base+".mp3" {
		t.Fatalf("首个路径 = %q", filepath.Base(first))
	}
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := allocateUniquePath(dir, base, "mp3")
	if filepath.Base(second) != base+" (2).mp3" {
		t.Errorf("第二个路径 = %q, 期望 %q", filepath.Base(second), base+" (2).mp3")
	}
	if err := os.WriteFile(second, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	third := allocateUniquePath(dir, base, "mp3")
	if filepath.Base(third) != base+" (3).mp3" {
		t.Errorf("第三个路径 = %q, 期望 %q", filepath.Base(third), base+" (3).mp3")
	}
}

func TestAllocateUniquePathDifferentExtNotConflict(t *testing.T) {
	dir := t.TempDir()
	base := "歌手 - 歌曲"

	if err := os.WriteFile(filepath.Join(dir, base+".mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := allocateUniquePath(dir, base, "flac")
	if filepath.Base(got) != base+".flac" {
		t.Errorf("不同扩展名不该被判为冲突: %q", filepath.Base(got))
	}
}

func TestFindExistingAudio(t *testing.T) {
	dir := t.TempDir()
	base := "歌手 - 歌曲"

	// 体积不足，不应命中
	small := filepath.Join(dir, base+".mp3")
	if err := os.WriteFile(small, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := findExistingAudio(dir, base); ok {
		t.Error("过小的文件不应被视为已下载")
	}

	// 体积达标（>500KB），应命中，且支持非 mp3 扩展名
	big := filepath.Join(dir, base+".flac")
	if err := os.WriteFile(big, make([]byte, 600*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := findExistingAudio(dir, base)
	if !ok {
		t.Fatal("达标的 flac 文件应被识别为已下载")
	}
	if got != big {
		t.Errorf("命中路径 = %q, 期望 %q", got, big)
	}
}

func TestDetectAudioExtByMagic(t *testing.T) {
	wav := []byte("RIFF")
	wav = append(wav, 0x24, 0x00, 0x00, 0x00)
	wav = append(wav, []byte("WAVE")...)

	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"flac", append([]byte("fLaC"), make([]byte, 8)...), "flac"},
		{"mp3-id3", append([]byte("ID3"), make([]byte, 8)...), "mp3"},
		{"mp3-sync", []byte{0xFF, 0xFB, 0x90, 0x00}, "mp3"},
		{"ogg", append([]byte("OggS"), make([]byte, 8)...), "ogg"},
		{"wav", wav, "wav"},
		{"m4a", []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p'}, "m4a"},
		{"ape", append([]byte("MAC "), make([]byte, 8)...), "ape"},
		{"dsf", append([]byte("DSD "), make([]byte, 8)...), "dsf"},
		// 关键：非音频内容必须返回空串，绝不能兜底成 mp3
		{"html", []byte("<html><body>403 Forbidden"), ""},
		{"json", []byte(`{"code":403,"msg":"login required"}`), ""},
		{"text", []byte("not-audio-at-all"), ""},
		{"too-short", []byte("ab"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectAudioExtByMagic(tc.head); got != tc.want {
				t.Errorf("detectAudioExtByMagic(%q) = %q, 期望 %q", string(tc.head), got, tc.want)
			}
		})
	}
}

func TestIsErrorPayload(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		ct   string
		want bool
	}{
		{"html-content-type", []byte("anything"), "text/html; charset=utf-8", true},
		{"json-content-type", []byte("anything"), "application/json", true},
		{"plain-content-type", []byte("anything"), "text/plain", true},
		{"html-body", []byte("<html><body>err"), "", true},
		{"doctype", []byte("<!DOCTYPE html>"), "", true},
		{"xml-body", []byte(`<?xml version="1.0"?>`), "", true},
		{"json-body", []byte(`{"code":403}`), "", true},
		{"array-body", []byte(`[{"err":1}]`), "", true},
		{"bom-then-html", append([]byte{0xEF, 0xBB, 0xBF}, []byte("<html>")...), "", true},
		{"leading-whitespace-html", []byte("\r\n\t  <html>"), "", true},
		// 合法音频不应被误杀
		{"real-mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x0a"), "audio/mpeg", false},
		{"real-flac", append([]byte("fLaC"), make([]byte, 16)...), "audio/flac", false},
		{"octet-stream", []byte("fLaC"), "application/octet-stream", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isErrorPayload(tc.head, tc.ct); got != tc.want {
				t.Errorf("isErrorPayload(%q, %q) = %v, 期望 %v", string(tc.head), tc.ct, got, tc.want)
			}
		})
	}
}

// newTestDownloader 构造下载目录指向临时目录、且允许访问回环地址的下载器
func newTestDownloader(t *testing.T, outDir string) *Downloader {
	t.Helper()
	t.Setenv("FN_ALLOW_PRIVATE_NET", "1")

	cfgMgr, err := config.NewConfigManager(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgMgr.Get()
	cfg.DownloadDir = outDir
	if err := cfgMgr.Update(cfg); err != nil {
		t.Fatal(err)
	}
	return NewDownloader(cfgMgr)
}

func countAudioFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, a := range audioExtensions {
			if ext == "."+a {
				n++
			}
		}
	}
	return n
}

// TestDownloadRejectsErrorPage 回归测试：CDN 返回大体积 HTML 错误页时，
// 必须判失败，绝不能存成 .mp3 并计为「下载成功」。
func TestDownloadRejectsErrorPage(t *testing.T) {
	html := append([]byte("<html><body>403 Forbidden 需要登录</body>"),
		bytes.Repeat([]byte(" "), 600*1024)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	res, err := d.downloadSingleSong(SongPayload{
		Name: "测试歌曲", Singer: "测试歌手",
		URL: srv.URL + "/stream.mp3", Quality: "320k",
	})
	if err == nil {
		t.Fatalf("HTML 错误页必须被判为失败，但返回了成功: %+v", res)
	}
	if n := countAudioFiles(t, outDir); n != 0 {
		t.Errorf("错误页不应留下任何音频文件，实际留下 %d 个", n)
	}
}

// TestDownloadRejectsErrorPageWithoutBadContentType 即使 Content-Type 被伪装也要能识别
func TestDownloadRejectsErrorPageWithoutBadContentType(t *testing.T) {
	body := append([]byte(`{"code":403,"message":"login required"}`),
		bytes.Repeat([]byte(" "), 400*1024)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	if _, err := d.downloadSingleSong(SongPayload{
		Name: "x", Singer: "y", URL: srv.URL + "/a.mp3",
	}); err == nil {
		t.Fatal("伪装成 octet-stream 的 JSON 错误页也应被拒绝")
	}
	if n := countAudioFiles(t, outDir); n != 0 {
		t.Errorf("不应留下音频文件，实际 %d 个", n)
	}
}

// TestDownloadRejectsNonAudioPayload 小体积非音频也要拒绝（不能只看体积）
func TestDownloadRejectsNonAudioPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("this is definitely not an audio stream"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	if _, err := d.downloadSingleSong(SongPayload{
		Name: "x", Singer: "y", URL: srv.URL + "/a.flac", Quality: "flac",
	}); err == nil {
		t.Fatal("非音频内容应被拒绝（不能因为 URL 是 .flac 就放行）")
	}
	if n := countAudioFiles(t, outDir); n != 0 {
		t.Errorf("不应留下音频文件，实际 %d 个", n)
	}
}

// TestDownloadAcceptsRealAudio 正向对照：合法音频必须正常落盘
func TestDownloadAcceptsRealAudio(t *testing.T) {
	flac := downloadFixtureFLAC()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(flac)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	res, err := d.downloadSingleSong(SongPayload{
		Name: "平凡之路", Singer: "朴树",
		URL: srv.URL + "/a.flac", Quality: "flac",
	})
	if err != nil {
		t.Fatalf("合法 FLAC 应当下载成功: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("status = %q, 期望 success", res.Status)
	}
	if !strings.HasSuffix(res.Path, ".flac") {
		t.Errorf("应按真实容器落盘为 .flac，实际 %q", res.Path)
	}
	if n := countAudioFiles(t, outDir); n != 1 {
		t.Errorf("应留下 1 个音频文件，实际 %d 个", n)
	}
}

// TestActualFormatReportsRealContainer 核心场景：**请求无损，音源给了有损直链**。
//
// 文件本身不算错（扩展名按魔数给，内容与扩展名一致），但用户以为拿到的是 FLAC。
// 所以 result 必须把真实容器单独回报出来，调用方才能发现「档位与容器不符」。
func TestActualFormatReportsRealContainer(t *testing.T) {
	// 内容其实是 MP3（ID3 头），但 URL 与请求音质都声称 flac
	mp3 := append([]byte("ID3"), make([]byte, 400*1024)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac") // 对端也在撒谎
		_, _ = w.Write(mp3)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	res, err := d.downloadSingleSong(SongPayload{
		Name: "某首假无损", Singer: "某人",
		URL: srv.URL + "/fake.flac", Quality: "flac",
	})
	if err != nil {
		t.Fatalf("应当下载成功（内容合法，只是不是无损）: %v", err)
	}

	if res.Quality != "flac" {
		t.Errorf("Quality 应原样回报请求档位 flac，实际 %q", res.Quality)
	}
	if res.ActualFormat != "mp3" {
		t.Errorf("ActualFormat 应为真实容器 mp3，实际 %q", res.ActualFormat)
	}
	if !strings.HasSuffix(res.Path, ".mp3") {
		t.Errorf("落盘扩展名应跟随真实容器 .mp3，实际 %q", res.Path)
	}
}

// TestActualFormatOnAlreadyExists 命中已有文件时也要给出真实容器
func TestActualFormatOnAlreadyExists(t *testing.T) {
	// 注意：findExistingAudio 要求体积 > 500KB 才算「已存在」，所以这里给 600KB
	flac := downloadFixtureFLACSize(600 * 1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(flac)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	payload := SongPayload{
		Name: "平凡之路", Singer: "朴树",
		URL: srv.URL + "/a.flac", Quality: "flac",
	}

	if _, err := d.downloadSingleSong(payload); err != nil {
		t.Fatalf("首次下载应成功: %v", err)
	}

	res, err := d.downloadSingleSong(payload)
	if err != nil {
		t.Fatalf("二次下载应命中已有文件而非报错: %v", err)
	}
	if res.Status != "already_exists" {
		t.Fatalf("status = %q, 期望 already_exists", res.Status)
	}
	if res.ActualFormat != "flac" {
		t.Errorf("已有文件的 ActualFormat 应为 flac，实际 %q", res.ActualFormat)
	}
}

func TestDetectImageMime(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}
	if got := detectImageMime(jpeg); got != "image/jpeg" {
		t.Errorf("JPEG = %q", got)
	}
	png := append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 8)...)
	if got := detectImageMime(png); got != "image/png" {
		t.Errorf("PNG = %q", got)
	}
	if got := detectImageMime([]byte("<html>not an image</html>")); got != "" {
		t.Errorf("非图片 = %q, 期望空串", got)
	}
}

// webp 必须认：`security/image.go` 的 FetchImage 认 webp（补全那条链在用），
// 下载这条链以前只认 jpeg/png/gif —— 同一张封面「补全能写、下载写不进去」，
// 用户看到的就是「封面时有时无」（2026-09-22 反馈的成因之一）。
func TestDetectImageMimeAcceptsWebP(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 8)...)
	if got := detectImageMime(webp); got != "image/webp" {
		t.Errorf("WEBP = %q, 期望 image/webp", got)
	}
	// 只有 RIFF 头但不是 WEBP（例如 wav）不该被当成图片
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 8)...)
	if got := detectImageMime(wav); got != "" {
		t.Errorf("WAVE = %q, 期望空串", got)
	}
}

func TestIsRetryableClassificationHelpers(t *testing.T) {
	// GetStreamReferer 依据平台域名补 Referer
	cases := map[string]string{
		"https://dl.stream.qqmusic.qq.com/x.mp3": "https://y.qq.com/",
		"http://m10.music.126.net/x.mp3":         "https://music.163.com/",
		"http://x.kuwo.cn/y.mp3":                 "http://www.kuwo.cn/",
		"https://unknown.example/z.mp3":          "",
	}
	for in, want := range cases {
		if got := GetStreamReferer(in, ""); got != want {
			t.Errorf("GetStreamReferer(%q) = %q, 期望 %q", in, got, want)
		}
	}
	if got := GetStreamReferer("https://unknown.example/z.mp3", "https://custom/"); got != "https://custom/" {
		t.Errorf("自定义 Referer 应优先, 得到 %q", got)
	}
}

// ── 断点续传 ──

func TestPartialPathIsStable(t *testing.T) {
	song := SongPayload{Name: "夜曲", Singer: "周杰伦", Source: "wy", Songmid: "186016"}
	a := partialPath("/music", "周杰伦 - 夜曲", song)
	b := partialPath("/music", "周杰伦 - 夜曲", song)

	if a != b {
		t.Errorf("同一任务两次调用应得到相同路径，避免断点丢失: %q vs %q", a, b)
	}
	if !strings.HasSuffix(a, ".part") {
		t.Errorf("应是 .part 文件: %q", a)
	}
	if !strings.HasPrefix(filepath.Base(a), ".") {
		t.Errorf("应是隐藏文件，避免被扫描进曲库: %q", filepath.Base(a))
	}
	// 不同歌曲应使用不同断点文件
	other := SongPayload{Name: "晴天", Singer: "周杰伦", Source: "wy", Songmid: "999"}
	if partialPath("/music", "周杰伦 - 夜曲", other) == a {
		t.Error("不同曲目应使用不同断点文件")
	}
}

func TestDownloadResumesFromPartialFile(t *testing.T) {
	full := downloadFixtureFLAC()

	// 服务端支持 Range：从 offset 开始返回剩余部分
	var sawRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		http.ServeContent(w, r, "a.flac", time.Time{}, bytes.NewReader(full))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	// 预置一个已下载一半的 .part
	song := SongPayload{Name: "平凡之路", Singer: "朴树", Source: "wy", Songmid: "1",
		URL: srv.URL + "/a.flac", Quality: "flac"}
	part := partialPath(outDir, "朴树 - 平凡之路", song)
	half := len(full) / 2
	if err := os.WriteFile(part, full[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := d.downloadSingleSong(song)
	if err != nil {
		t.Fatalf("应从断点续传成功: %v", err)
	}
	if sawRange == "" {
		t.Error("存在 .part 时应发送 Range 头进行续传")
	}
	if !strings.HasPrefix(sawRange, "bytes=") {
		t.Errorf("Range 头格式不正确: %q", sawRange)
	}
	if res.Status != "success" {
		t.Errorf("status = %q", res.Status)
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(audioFrames(data), audioFrames(full)) {
		t.Errorf("续传后文件内容不完整: got %d bytes, want %d", len(data), len(full))
	}
	// 成功后 .part 应被消费掉
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("成功后 .part 应被移除")
	}
}

func TestDownloadFallsBackWhenServerIgnoresRange(t *testing.T) {
	full := downloadFixtureFLAC()

	// 服务端忽略 Range，永远返回 200 全量
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(full)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	song := SongPayload{Name: "平凡之路", Singer: "朴树", Source: "wy", Songmid: "1",
		URL: srv.URL + "/a.flac", Quality: "flac"}
	part := partialPath(outDir, "朴树 - 平凡之路", song)
	if err := os.WriteFile(part, []byte("garbage-old-partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := d.downloadSingleSong(song)
	if err != nil {
		t.Fatalf("服务端不支持 Range 时应从头重下并成功: %v", err)
	}
	data, _ := os.ReadFile(res.Path)
	if !bytes.Equal(audioFrames(data), audioFrames(full)) {
		t.Errorf("应从零重下得到完整文件: got %d, want %d", len(data), len(full))
	}
}

func TestDownloadKeepsPartialOnIncomplete(t *testing.T) {
	// 声明完整长度但提前断流
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		w.Header().Set("Content-Length", "800000")
		_, _ = w.Write(append([]byte("fLaC"), make([]byte, 400*1024)...))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	song := SongPayload{Name: "平凡之路", Singer: "朴树", Source: "wy", Songmid: "1",
		URL: srv.URL + "/a.flac", Quality: "flac"}

	_, err := d.downloadSingleSong(song)
	if err == nil {
		t.Fatal("对端提前断流应判为失败")
	}
	if !strings.Contains(err.Error(), "断点") {
		t.Errorf("错误信息应说明保留了断点，实际: %v", err)
	}

	// .part 应被保留，供下次续传
	part := partialPath(outDir, "朴树 - 平凡之路", song)
	if _, statErr := os.Stat(part); statErr != nil {
		t.Error("下载不完整时应保留 .part 供下次续传")
	}
}

// ── 下载链路写入发行信息（2026-09-26）──
//
// 回归点：SongPayload 以前没有 year/track/disc 字段，applyTags 也只填
// Title/Artist/Album/Genre，所以**下载落盘的歌在飞牛里年份/曲序/光盘永远是空的**
// （实测库里 41 首测试曲目 41 首都缺），只能事后跑 /api/library/complete 补。
//
// 夹具要点：既要有合法 FLAC 头（前 12 字节与 pkg/tags 自己的 flacFixture 同构：
// STREAMINFO + 末尾 PADDING，写入器才认），又要撑过 300KB 的体积校验。
// 下面两个 CRC 是**逐位**实现（与 pkg/flacok 的查表实现独立），只用来给测试夹具
// 算校验位。两边对得上，夹具才算是「外部输入」而不是自说自话。
func fixtureCRC8(p []byte) byte {
	crc := byte(0)
	for _, b := range p {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func fixtureCRC16(p []byte) uint16 {
	crc := uint16(0)
	for _, b := range p {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x8005
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// fixtureFrame 合成一个 VERBATIM 子帧的完整帧（单声道 16 位、4096 采样）。
// 帧头 CRC-8 与整帧 CRC-16 都按规范算对，所以 pkg/flacok 的逐帧校验能过。
//
// 采样数据真的躺在帧里，所以调帧数就能把文件撑到多大 —— 关键是这种体积是
// **帧**给的，不是 PADDING 给的：applyTags → pkg/tags 写标签时会重建元数据、
// 丢弃 PADDING（pkg/tags/flac.go 的「丢弃 padding，由重建后的布局决定空间」），
// 用 PADDING 凑体积的话，写完标签文件会缩水，体积阈值与内容比对都会失效。
func fixtureFrame(seq byte) []byte {
	const blockSize = 4096 // 采样数
	hdr := []byte{
		0xFF, 0xF8, // 同步码 + 保留位 + 定长块
		0x79,                                                     // 块长码 7（16 位显式）| 采样率码 9（44.1kHz）
		0x08,                                                     // 声道赋值 0（单声道）| 位宽码 4（16 位）| 保留位 0
		seq,                                                      // 帧号（裸字节，必须 < 0x80）
		byte((blockSize - 1) >> 8), byte((blockSize - 1) & 0xFF), // 显式块长
	}
	hdr = append(hdr, fixtureCRC8(hdr))

	frame := append(hdr, 0x02) // 子帧头：保留位 0 + VERBATIM(1) + 浪费位 0
	samples := make([]byte, blockSize*2)
	for i := range samples {
		samples[i] = seq ^ byte(i) // 确定性图案
	}
	frame = append(frame, samples...)
	crc := fixtureCRC16(frame)
	return append(frame, byte(crc>>8), byte(crc))
}

// audioFrames 剥掉元数据块，返回音频帧字节。
//
// 用来做内容比对：applyTags 会重建元数据（丢 PADDING、加 VORBIS_COMMENT），
// 整文件字节比对会误报，要比的是音频部分。
func audioFrames(data []byte) []byte {
	if len(data) < 8 || string(data[:4]) != "fLaC" {
		return data
	}
	i := 4
	for i+4 <= len(data) {
		last := data[i]&0x80 != 0
		n := int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
		i += 4 + n
		if last {
			break
		}
	}
	if i > len(data) {
		return nil
	}
	return data[i:]
}

// downloadFixtureFLACSize 合成一个**结构完好**的 FLAC 文件到指定体积（约数）。
//
// 必须是真结构：入库前有 pkg/flacok 的逐帧 CRC 校验（见 downloadSingleSong），
// 假的「fLaC + 一堆 0xAA」会被判成坏流拒收。元数据块与 pkg/tags 的 flacFixture
// 同构（STREAMINFO + 末尾 PADDING，写入器才认），元数据必须排在音频帧前面。
func downloadFixtureFLACSize(size int) []byte {
	si := make([]byte, 34)
	si[0], si[1] = 0x00, 0x10 // min blocksize
	si[2], si[3] = 0x00, 0x10 // max blocksize
	// 采样率 44100 | 声道数-1 = 0 | 位宽-1 = 15 | 总采样数这里不填（走帧不看它）
	v := uint64(44100)<<44 | uint64(15)<<36
	for i := 0; i < 8; i++ {
		si[10+i] = byte(v >> (56 - 8*i))
	}

	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x00, 0x00, 0x00, 0x22}) // STREAMINFO, last=false, len=34
	b.Write(si)
	b.Write([]byte{0x81, 0x00, 0x00, 0x00}) // PADDING(1), last=true, len=0

	for seq := 0; b.Len() < size; seq++ {
		b.Write(fixtureFrame(byte(seq % 0x70)))
	}
	return b.Bytes()
}

func downloadFixtureFLAC() []byte {
	return downloadFixtureFLACSize(400 * 1024)
}

func TestDownloadWritesYearTrackDisc(t *testing.T) {
	fixture := downloadFixtureFLAC()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	d := newTestDownloader(t, t.TempDir())

	res, err := d.downloadSingleSong(SongPayload{
		Name: "救赎", Singer: "2eight 小茶", Album: "救赎",
		URL: srv.URL + "/a.flac", Quality: "flac",
		Year: 2024, Track: 3, Disc: 1,
	})
	if err != nil {
		t.Fatalf("下载应成功: %v", err)
	}

	got, err := tags.Read(res.Path)
	if err != nil {
		t.Fatalf("读回标签失败: %v", err)
	}
	if got.Year != 2024 || got.Track != 3 || got.Disc != 1 {
		t.Fatalf("年份/曲序/光盘应写进文件，实际 year=%d track=%d disc=%d",
			got.Year, got.Track, got.Disc)
	}
}

// 平台没给年份/曲序时（0）不许往文件里塞假值：tags 的写入器对 <=0 一律跳过。
func TestDownloadWithoutYearLeavesFieldsUnset(t *testing.T) {
	fixture := downloadFixtureFLAC()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	d := newTestDownloader(t, t.TempDir())

	res, err := d.downloadSingleSong(SongPayload{
		Name: "小巷", Singer: "3sumr",
		URL: srv.URL + "/b.flac", Quality: "flac",
	})
	if err != nil {
		t.Fatalf("下载应成功: %v", err)
	}

	got, err := tags.Read(res.Path)
	if err != nil {
		t.Fatalf("读回标签失败: %v", err)
	}
	if got.Year != 0 || got.Track != 0 || got.Disc != 0 {
		t.Fatalf("平台没给年份/曲序时不应写入，实际 year=%d track=%d disc=%d",
			got.Year, got.Track, got.Disc)
	}
}

// 前端是把搜索结果对象整个展开着传的（downloadManager.js 的 {...song}），
// 所以 JSON 字段名必须与 search.UnifiedSong 的 year/track/disc 对齐 ——
// 一旦改名，值会在下载落盘时被静默丢掉，且不报任何错。
func TestSongPayloadYearJSONNamesMatchSearch(t *testing.T) {
	raw := []byte(`{"name":"晴天","singer":"周杰伦","album":"叶惠美","year":2003,"track":3,"disc":1}`)

	var p SongPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if p.Year != 2003 || p.Track != 3 || p.Disc != 1 {
		t.Fatalf("year/track/disc 必须按同名 JSON 字段解出，实际 year=%d track=%d disc=%d",
			p.Year, p.Track, p.Disc)
	}
}

// 查重必须认「已下好的无损」—— 原先这里硬编码 `.mp3`，于是下过 .flac 的歌查不到，
// 界面还显示「未下载」，再点一次就是重下一遍。
func TestCheckDownloadedFindsNonMP3Audio(t *testing.T) {
	dir := t.TempDir()
	d := newTestDownloader(t, dir)

	base := namefmt.Build("Beyond", "真的爱你")
	flacPath := filepath.Join(dir, base+".flac")
	if err := os.WriteFile(flacPath, make([]byte, 600*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"songs":[{"id":"346075","name":"真的爱你","singer":"Beyond"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/download/check", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.HandleCheckDownloaded(w, req)

	var out struct {
		Exists map[string]bool `json:"exists"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%s", w.Body.String())
	}
	if !out.Exists["Beyond - 真的爱你"] || !out.Exists["346075"] {
		t.Fatalf("无损已存在就该判为已下载：%+v", out.Exists)
	}
}
