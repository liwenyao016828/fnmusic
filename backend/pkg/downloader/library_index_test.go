package downloader

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/namefmt"
)

// ---------- 造数据的小工具 ----------

// writeLibraryFixture 造一个体积达标（≥500KB）的假音频文件。
// 查重只关心「路径在不在、体积够不够」，不需要真能解码。
func writeLibraryFixture(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeLowBitrateMP3 造一个首帧标称 128kbps 的 mp3（audioext.TierOfFile 只看首帧）。
func writeLowBitrateMP3(t *testing.T, path string) string {
	t.Helper()
	data := append([]byte{0xff, 0xfb, 0x90, 0x00}, make([]byte, 600*1024)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFLACWithDuration 造一个 STREAMINFO 里写着真实总采样数的 flac。
// 时长探测只读前 42 字节，所以不需要能解码的音频帧，但结构必须对。
func writeFLACWithDuration(t *testing.T, path string, seconds float64, size int) string {
	t.Helper()
	const sampleRate = 44100
	si := make([]byte, 34)
	si[0], si[1] = 0x10, 0x00 // min blocksize 4096
	si[2], si[3] = 0x10, 0x00 // max blocksize 4096
	total := uint64(seconds * sampleRate)
	v := uint64(sampleRate)<<44 | uint64(1)<<41 | uint64(15)<<36 | total&0xFFFFFFFFF
	for i := 0; i < 8; i++ {
		si[10+i] = byte(v >> (56 - 8*i))
	}
	out := append([]byte("fLaC"), 0x00, 0x00, 0x00, 0x22)
	out = append(out, si...)
	if size > len(out) {
		out = append(out, make([]byte, size-len(out))...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeMP3WithXing 造一个带 Xing/Info 帧数表的 mp3（MPEG1 Layer III / 44.1kHz / 立体声）。
// frames = 0 表示写一个**没有** Xing 的普通帧（用于验证「解析不出来就不否决」）。
func writeMP3WithXing(t *testing.T, path string, frames uint32, size int) string {
	t.Helper()
	out := []byte{0xff, 0xfb, 0x90, 0x00} // MPEG1 Layer3 128k 44100 stereo
	out = append(out, make([]byte, 32)...)
	if frames > 0 {
		out = append(out, []byte("Xing")...)
		var f [4]byte
		binary.BigEndian.PutUint32(f[:], 1) // flags: 有帧数字段
		out = append(out, f[:]...)
		binary.BigEndian.PutUint32(f[:], frames)
		out = append(out, f[:]...)
	}
	if size > len(out) {
		out = append(out, make([]byte, size-len(out))...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// matchCatalog 只填歌名，用于版本判据的单元测试。
func matchCatalog(name string) match.Catalog { return match.Catalog{Name: name} }

func entry(path, title, artist, album, format string) library.Entry {
	return library.Entry{Path: path, Title: title, Artist: artist, Album: album, Format: format}
}

// checkExists 调 /download/check 并取回响应里的三个 map。
func checkExists(t *testing.T, d *Downloader, body string) (exists map[string]bool, paths map[string]string, up map[string]bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/download/check", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.HandleCheckDownloaded(w, req)

	var out struct {
		Exists     map[string]bool   `json:"exists"`
		Paths      map[string]string `json:"paths"`
		Upgradable map[string]bool   `json:"upgradable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%s", w.Body.String())
	}
	return out.Exists, out.Paths, out.Upgradable
}

// ---------- 下载路径 ----------

// 核心场景：下载目录里没有同名文件，但全库索引里已经有一份（另一个目录、
// 文件名完全不同，靠标签认出来）——必须直接复用，不能再下一遍。
func TestDownloadSkipsWhenLibraryHasSameSongInAnotherDir(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "01 随便什么文件名.flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "晴天", "周杰伦", "叶惠美", "flac")}
	})

	// URL 留空：只要没被判成 already_exists，就会因为「没有有效音频流地址」失败。
	res, err := d.downloadSingleSong(SongPayload{
		Name: "晴天", Singer: "周杰伦", Album: "叶惠美", Quality: "flac",
	})
	if err != nil {
		t.Fatalf("全库已有同一首，不该再走下载: %v", err)
	}
	if res.Status != "already_exists" {
		t.Fatalf("status = %q，期望 already_exists", res.Status)
	}
	if filepath.ToSlash(res.Path) != filepath.ToSlash(libPath) {
		t.Errorf("命中路径 = %q，期望 %q", res.Path, libPath)
	}
	if res.ActualFormat != "flac" {
		t.Errorf("ActualFormat = %q，期望 flac", res.ActualFormat)
	}
}

// 没有标签的文件靠文件名兜底，也要能跨目录命中（namefmt.Parse 与 Build 互逆）。
func TestDownloadLibraryMatchFallsBackToFilename(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	base := namefmt.Build("周杰伦", "晴天")
	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), base+".flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{{Path: libPath}} // 标签全空
	})

	res, err := d.downloadSingleSong(SongPayload{Name: "晴天", Singer: "周杰伦", Quality: "flac"})
	if err != nil || res.Status != "already_exists" {
		t.Fatalf("文件名能反解出歌手/歌名，应命中已有文件：res=%+v err=%v", res, err)
	}
}

// 不同版本不能当同一首：库里是 Live，本次要正式版，必须继续下载。
func TestDownloadDoesNotSkipAcrossLibraryVersions(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "晴天 (Live).flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "晴天 (Live)", "周杰伦", "", "")}
	})

	res, err := d.downloadSingleSong(SongPayload{Name: "晴天", Singer: "周杰伦"})
	if err == nil || res == nil || res.Status != "failed" {
		t.Fatalf("Live 版不能挡住正式版下载：res=%+v err=%v", res, err)
	}
}

// 索引里的失效路径（文件已被删）不能当成「已存在」，否则用户点下载只得到
// already_exists、磁盘上却什么都没有。
func TestDownloadIgnoresStaleLibraryPath(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	missing := filepath.Join(t.TempDir(), "已经不存在了.flac")
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(missing, "晴天", "周杰伦", "", "")}
	})

	res, err := d.downloadSingleSong(SongPayload{Name: "晴天", Singer: "周杰伦"})
	if err == nil || res == nil || res.Status != "failed" {
		t.Fatalf("失效路径必须继续走下载流程：res=%+v err=%v", res, err)
	}
}

// 库里有一份低音质（另一个目录），本次请求无损且开了升级开关 → 必须真的下载，
// 不能被 already_exists 挡掉（后台音质补强依赖这条语义）。
func TestDownloadStillUpgradesOverLibraryCopy(t *testing.T) {
	outDir := t.TempDir()
	libDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	oldPath := writeLowBitrateMP3(t, filepath.Join(libDir, "周杰伦 - 晴天.mp3"))
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(oldPath, "晴天", "周杰伦", "", "mp3")}
	})

	flac := downloadFixtureFLACSize(600 * 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/flac")
		_, _ = w.Write(flac)
	}))
	defer srv.Close()

	res, err := d.downloadSingleSong(SongPayload{
		Name: "晴天", Singer: "周杰伦",
		URL: srv.URL + "/a.flac", Quality: "flac", UpgradeIfBetter: true,
	})
	if err != nil {
		t.Fatalf("低音质 + 升级开关应继续下载: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q，期望 success", res.Status)
	}
	if n := countAudioFiles(t, outDir); n != 1 {
		t.Errorf("下载目录应落 1 个文件，实际 %d 个", n)
	}
	// 另一个目录里的旧文件不属于本次下载目标，不能替用户删。
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("不该删除下载目录之外的旧文件: %v", err)
	}
}

// 关键安全语义（lead 特别指出）：**没有**开 upgrade_if_better 的调用方，
// 即使索引命中一份低音质文件，也绝不能覆盖/重下。
func TestDownloadDoesNotUpgradeUnlessCallerAsks(t *testing.T) {
	outDir := t.TempDir()
	libDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	oldPath := writeLowBitrateMP3(t, filepath.Join(libDir, "周杰伦 - 晴天.mp3"))
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(oldPath, "晴天", "周杰伦", "", "mp3")}
	})

	// 没带 UpgradeIfBetter，也没带 URL：必须停在 already_exists。
	res, err := d.downloadSingleSong(SongPayload{Name: "晴天", Singer: "周杰伦", Quality: "flac"})
	if err != nil {
		t.Fatalf("未开升级开关时应判已有：%v", err)
	}
	if res.Status != "already_exists" {
		t.Fatalf("status = %q，期望 already_exists（Upgradable 只是信息，不能代替调用方的开关）", res.Status)
	}
	if filepath.ToSlash(res.Path) != filepath.ToSlash(oldPath) {
		t.Errorf("命中路径 = %q，期望 %q", res.Path, oldPath)
	}
}

// 库里已经是无损、本次只请求 320k：绝不降级重下。
func TestDownloadDoesNotDowngradeLibraryLossless(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "晴天.flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "晴天", "周杰伦", "", "flac")}
	})

	res, err := d.downloadSingleSong(SongPayload{
		Name: "晴天", Singer: "周杰伦", Quality: "320k", UpgradeIfBetter: true,
	})
	if err != nil || res.Status != "already_exists" {
		t.Fatalf("已有无损时不该降级重下：res=%+v err=%v", res, err)
	}
}

// 没注入快照时行为保持不变（旧部署/旧测试不受影响）。
func TestDownloadWithoutLibrarySnapshotKeepsOldBehavior(t *testing.T) {
	outDir := t.TempDir()
	d := newTestDownloader(t, outDir)

	res, err := d.downloadSingleSong(SongPayload{Name: "晴天", Singer: "周杰伦"})
	if err == nil || res == nil || res.Status != "failed" {
		t.Fatalf("没有快照且没有本地文件时应走原流程（无 URL → 失败）：res=%+v err=%v", res, err)
	}
}

// ---------- MatchLibrary 判据 ----------

// 索引里同一首歌有多份、分数一样时优先还能用的那份；失效的必须跳过。
func TestMatchLibraryPrefersUsableLossless(t *testing.T) {
	dir := t.TempDir()
	missingFlac := filepath.Join(dir, "missing.flac") // 分数更高但文件不在
	mp3 := writeLibraryFixture(t, filepath.Join(dir, "there.mp3"), 600*1024)

	hit, ok := MatchLibrary([]library.Entry{
		entry(missingFlac, "晴天", "周杰伦", "叶惠美", "flac"),
		entry(mp3, "晴天", "周杰伦", "叶惠美", "mp3"),
	}, SongPayload{Name: "晴天", Singer: "周杰伦", Album: "叶惠美"})
	if !ok {
		t.Fatal("应命中仍然存在的那一份")
	}
	if hit.Path != mp3 {
		t.Errorf("应跳过失效路径，命中 %q，实际 %q", mp3, hit.Path)
	}
}

// 同名不同歌手绝不能串（"晴天" 这种歌名撞车很常见）。
func TestMatchLibraryRejectsDifferentArtist(t *testing.T) {
	p := writeLibraryFixture(t, filepath.Join(t.TempDir(), "晴天.flac"), 600*1024)
	if _, ok := MatchLibrary([]library.Entry{
		entry(p, "晴天", "朴树", "", "flac"),
	}, SongPayload{Name: "晴天", Singer: "周杰伦"}); ok {
		t.Fatal("歌手不同不该判成已存在")
	}
}

// 半成品与回收站里的文件都还在盘上、体积也够，但绝不是「已经有了」。
func TestMatchLibraryRejectsPartAndTrashFiles(t *testing.T) {
	dir := t.TempDir()
	part := writeLibraryFixture(t, filepath.Join(dir, "x.part", "晴天.flac"), 600*1024)
	trash := writeLibraryFixture(t, filepath.Join(dir, ".tidy_trash", "晴天.flac"), 600*1024)

	for _, p := range []string{part, trash} {
		if _, ok := MatchLibrary([]library.Entry{
			entry(p, "晴天", "周杰伦", "", "flac"),
		}, SongPayload{Name: "晴天", Singer: "周杰伦"}); ok {
			t.Errorf("半成品/回收站里的文件不该判成已存在：%s", p)
		}
	}
}

// 时长只在**可靠解析**出来时才否决：同名不同录音靠它分辨。
func TestMatchLibraryDurationVeto(t *testing.T) {
	p := writeFLACWithDuration(t, filepath.Join(t.TempDir(), "晴天.flac"), 180, 600*1024)
	entries := []library.Entry{entry(p, "晴天", "周杰伦", "", "flac")}

	// 库里 180s、请求 269s：差 89s，远超 max(10s, 15%)，必须不命中。
	if _, ok := MatchLibrary(entries, SongPayload{Name: "晴天", Singer: "周杰伦", Duration: 269}); ok {
		t.Fatal("时长相差太多不该判成已存在")
	}
	// 差几秒（容差内）仍算同一首。
	if _, ok := MatchLibrary(entries, SongPayload{Name: "晴天", Singer: "周杰伦", Duration: 185}); !ok {
		t.Fatal("容差内的时长差异不该否决")
	}
}

// 解析不出时长（mp3 没 Xing、请求也没带时长）→ 不否决：未知不能当「不符」用。
func TestMatchLibraryDurationUnknownDoesNotVeto(t *testing.T) {
	noXing := writeMP3WithXing(t, filepath.Join(t.TempDir(), "晴天.mp3"), 0, 600*1024)
	entries := []library.Entry{entry(noXing, "晴天", "周杰伦", "", "mp3")}

	if _, ok := MatchLibrary(entries, SongPayload{Name: "晴天", Singer: "周杰伦", Duration: 269}); !ok {
		t.Fatal("本地解析不出时长时应放行（不做 CBR 估算）")
	}
	if _, ok := MatchLibrary(entries, SongPayload{Name: "晴天", Singer: "周杰伦"}); !ok {
		t.Fatal("请求没带时长时应放行")
	}
}

// 版本指纹：改编版之间要能分辨 (Live) / (Live at 台北) / (Remix)。
func TestVersionIncompatibility(t *testing.T) {
	cases := []struct {
		name     string
		req, lib string
		wantBad  bool
	}{
		{"正式版 vs 正式版（括号只是说明）", "晴天 (电影主题曲)", "晴天", false},
		{"正式版 vs Live", "晴天", "晴天 (Live)", true},
		{"Live vs Live at 台北（同一版）", "晴天 (Live)", "晴天 (Live at 台北)", false},
		{"Live vs Remix", "晴天 (Live)", "晴天 (Remix)", true},
		{"Remix vs 别的 Remix", "晴天 (Club Remix)", "晴天 (Piano Remix)", true},
	}
	for _, tc := range cases {
		bad := versionIncompatibility(
			matchCatalog(tc.req), matchCatalog(tc.lib),
		)
		if (bad != "") != tc.wantBad {
			t.Errorf("%s: versionIncompatibility(%q,%q) = %q", tc.name, tc.req, tc.lib, bad)
		}
	}
}

// 歌手兼容：包含关系算兼容（中文多歌手常见写法），不同人绝不兼容。
func TestArtistCompatible(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"周杰伦", "周杰伦", true},
		{"周杰伦", "", true},        // 信息不足不否决
		{"周杰伦/杨瑞代", "周杰伦", true}, // 包含
		{"周杰伦", "朴树", false},
		{"Beyond", "BEYOND", true},
	}
	for _, tc := range cases {
		if got := artistCompatible(tc.a, tc.b); got != tc.want {
			t.Errorf("artistCompatible(%q,%q) = %v，期望 %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ---------- /download/check ----------

// check 也要走全库索引：下载目录里没有同名文件，但别的目录有。
func TestCheckDownloadedUsesLibraryIndex(t *testing.T) {
	d := newTestDownloader(t, t.TempDir())
	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "别的目录/随便命名.flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "真的爱你", "Beyond", "", "flac")}
	})

	exists, paths, _ := checkExists(t, d, `{"songs":[{"id":"346075","name":"真的爱你","singer":"Beyond"}]}`)
	if !exists["Beyond - 真的爱你"] || !exists["346075"] {
		t.Fatalf("全库已存在就该判为已下载：%+v", exists)
	}
	if paths["346075"] != filepath.ToSlash(libPath) {
		t.Errorf("paths[346075] = %q，期望 %q", paths["346075"], filepath.ToSlash(libPath))
	}
}

// check：库里只有 Live 版，正式版仍应判「没有」。
func TestCheckDownloadedRejectsDifferentLibraryVersion(t *testing.T) {
	d := newTestDownloader(t, t.TempDir())
	libPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "真的爱你 (Live).flac"), 600*1024)
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "真的爱你 (Live)", "Beyond", "", "flac")}
	})

	exists, _, _ := checkExists(t, d, `{"songs":[{"id":"346075","name":"真的爱你","singer":"Beyond"}]}`)
	if exists["Beyond - 真的爱你"] || exists["346075"] {
		t.Fatalf("Live 版不能把正式版判成已下载：%+v", exists)
	}
}

// check：盘上是 128k，本次请求 flac → exists=false 且 upgradable=true（附加字段）。
func TestCheckDownloadedMarksUpgradable(t *testing.T) {
	d := newTestDownloader(t, t.TempDir())
	libPath := writeLowBitrateMP3(t, filepath.Join(t.TempDir(), "周杰伦 - 晴天.mp3"))
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "晴天", "周杰伦", "", "mp3")}
	})

	exists, paths, up := checkExists(t, d,
		`{"songs":[{"id":"1","name":"晴天","singer":"周杰伦","quality":"flac"}]}`)
	if exists["1"] {
		t.Fatal("盘上档位更低时不该判成已下载（否则永远不会升级）")
	}
	if !up["1"] {
		t.Fatal("应标出可升级")
	}
	if paths["1"] == "" {
		t.Fatal("可升级时也要给出会被替换的那份路径")
	}

	// 已有无损、本次只请求 320k：不算可升级，直接判已下载（不降级）。
	d2 := newTestDownloader(t, t.TempDir())
	flacPath := writeLibraryFixture(t, filepath.Join(t.TempDir(), "晴天.flac"), 600*1024)
	d2.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(flacPath, "晴天", "周杰伦", "", "flac")}
	})
	exists, _, up = checkExists(t, d2,
		`{"songs":[{"id":"2","name":"晴天","singer":"周杰伦","quality":"320k"}]}`)
	if !exists["2"] || up["2"] {
		t.Fatalf("已有无损时 320k 应判已下载且不可升级：exists=%v up=%v", exists, up)
	}
}

// check：不带 quality（当前前端的探测体只有 id/name/singer）时不得出现「可升级」噪音。
func TestCheckDownloadedWithoutQualityKeepsExists(t *testing.T) {
	d := newTestDownloader(t, t.TempDir())
	libPath := writeLowBitrateMP3(t, filepath.Join(t.TempDir(), "周杰伦 - 晴天.mp3"))
	d.SetLibraryIndex(func() []library.Entry {
		return []library.Entry{entry(libPath, "晴天", "周杰伦", "", "mp3")}
	})

	exists, _, up := checkExists(t, d, `{"songs":[{"id":"1","name":"晴天","singer":"周杰伦"}]}`)
	if !exists["1"] {
		t.Fatal("没带 quality 时应保持旧语义：已下载")
	}
	if up["1"] {
		t.Fatal("没带 quality 时不该说可升级")
	}
}

// ---------- 时长探测 ----------

func TestProbeDuration(t *testing.T) {
	dir := t.TempDir()
	flac := writeFLACWithDuration(t, filepath.Join(dir, "a.flac"), 269, 4096)
	if got := probeDurationSec(flac); got != 269 {
		t.Errorf("flac 时长 = %d，期望 269", got)
	}

	// 269s @44.1kHz / 1152 samples per frame
	frames := uint32(269 * 44100 / 1152)
	mp3 := writeMP3WithXing(t, filepath.Join(dir, "a.mp3"), frames, 4096)
	if got := probeDurationSec(mp3); got != 269 {
		t.Errorf("mp3(Xing) 时长 = %d，期望 269", got)
	}

	// 没有 Xing → 未知（0），绝不按体积估个近似值。
	plain := writeMP3WithXing(t, filepath.Join(dir, "b.mp3"), 0, 600*1024)
	if got := probeDurationSec(plain); got != 0 {
		t.Errorf("没有 Xing 时必须返回 0（未知），实际 %d", got)
	}

	// 不认识的容器 → 未知。
	other := writeLibraryFixture(t, filepath.Join(dir, "c.m4a"), 4096)
	if got := probeDurationSec(other); got != 0 {
		t.Errorf("m4a 应返回 0，实际 %d", got)
	}
}

// durationMismatch 的容差：max(10s, 15%)。
func TestDurationMismatch(t *testing.T) {
	cases := []struct {
		want, got int
		bad       bool
	}{
		{0, 200, false},   // 请求没带时长
		{200, 0, false},   // 本地未知
		{200, 205, false}, // 5s，远小于 30s
		{200, 190, false},
		{200, 240, true}, // 40s > 30s
		{20, 31, true},   // 11s > max(10, 3)
		{20, 28, false},
	}
	for _, tc := range cases {
		got := durationMismatch(tc.want, tc.got) != ""
		if got != tc.bad {
			t.Errorf("durationMismatch(%d,%d) bad=%v，期望 %v", tc.want, tc.got, got, tc.bad)
		}
	}
}
