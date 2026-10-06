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
	flac := append([]byte("fLaC"), make([]byte, 400*1024)...)

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
	flac := append([]byte("fLaC"), make([]byte, 600*1024)...)

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
	full := append([]byte("fLaC"), make([]byte, 400*1024)...)

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
	if !bytes.Equal(data, full) {
		t.Errorf("续传后文件内容不完整: got %d bytes, want %d", len(data), len(full))
	}
	// 成功后 .part 应被消费掉
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("成功后 .part 应被移除")
	}
}

func TestDownloadFallsBackWhenServerIgnoresRange(t *testing.T) {
	full := append([]byte("fLaC"), bytes.Repeat([]byte{0xAB}, 400*1024)...)

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
	if !bytes.Equal(data, full) {
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
func downloadFixtureFLAC() []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x00, 0x00, 0x00, 0x22}) // STREAMINFO, last=false, len=34
	b.Write(make([]byte, 34))
	b.Write([]byte{0x81, 0x00, 0x00, 0x08}) // PADDING, last=true, len=8
	b.Write(make([]byte, 8))
	b.Write(bytes.Repeat([]byte{0xAA}, 400*1024)) // 假音频帧，只为过体积校验
	return b.Bytes()
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
