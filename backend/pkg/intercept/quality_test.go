package intercept

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/online"
)

// 曲库扫描与替换的用例。
//
// 这里刻意把「判据」与「外呼」分开测：档位怎么算、名字怎么归一、哪些文件该进扫描、
// 什么样的结果才算「可替换」—— 这些是纯函数，值得钉死；而真去问渠道、真去落盘那条
// 路已经有自动下载那套用例在盯（两处共用同一份 collectDownloadAttempts）。

func TestQualityCurrentTierByContainer(t *testing.T) {
	cases := []struct{ ext, want string }{
		{".flac", online.TierLossless}, {".FLAC", online.TierLossless},
		{".ape", online.TierLossless}, {".wav", online.TierLossless},
		{".dsf", online.TierLossless}, {".dff", online.TierLossless},
		{".mp3", online.Tier320k}, {".m4a", online.Tier320k},
		{".ogg", online.Tier320k}, {"", online.Tier320k},
	}
	for _, c := range cases {
		if got := qualityCurrentTier(c.ext); got != c.want {
			t.Errorf("qualityCurrentTier(%q) = %q，想要 %q", c.ext, got, c.want)
		}
	}
}

// 「按容器判」这条口径的由来：真机见过名字里写着「无损」其实是 mp3 的文件。
func TestQualityCurrentTierIgnoresNameThatLies(t *testing.T) {
	p := "/x/Beyond - 真的爱你 无损.flac.mp3"
	if got := qualityCurrentTier(strings.ToLower(filepath.Ext(p))); got != online.Tier320k {
		t.Fatalf("名字里带「无损」的 mp3 该算 320k，得到 %q", got)
	}
}

func TestQualityNameKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"真的爱你", "真的爱你"},
		{"  真的爱你  ", "真的爱你"},
		{"真的爱你 (Live)", "真的爱你"},
		{"真的爱你（现场版）", "真的爱你"},
		{"Beyond - 真的爱你", "beyond真的爱你"},
		{"Hello, World!", "helloworld"},
	}
	for _, c := range cases {
		if got := qualityNameKey(c.in); got != c.want {
			t.Errorf("qualityNameKey(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

func TestQualityListLibraryFilesFiltersAndLimits(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.mp3")
	write("b.flac")
	write("c.txt")
	write("sub/d.m4a")
	write(".trash/e.mp3") // 隐藏目录不进扫描

	// 默认 mode=lossy：只列有损，且隐藏目录被跳过。
	got, truncated, err := h.it.qualityListLibraryFiles("lossy", 100)
	if err != nil {
		t.Fatalf("列文件失败：%v", err)
	}
	if truncated {
		t.Fatal("还没到 limit，不该 truncated")
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "a.mp3") || !strings.HasSuffix(got[1], "d.m4a") {
		t.Fatalf("lossy 该只列 a.mp3 与 sub/d.m4a（且按路径排序），得到 %v", got)
	}

	// mode=all：无损也进来，但隐藏目录里的仍然不算（3 个：a.mp3 / b.flac / sub/d.m4a）。
	all, _, err := h.it.qualityListLibraryFiles("all", 100)
	if err != nil {
		t.Fatalf("列文件失败：%v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all 该列 3 个音频文件，得到 %d：%v", len(all), all)
	}

	// 到上限：只给一份、并置 truncated（不是错误）。
	one, truncated, err := h.it.qualityListLibraryFiles("all", 1)
	if err != nil {
		t.Fatalf("列文件失败：%v", err)
	}
	if len(one) != 1 || !truncated {
		t.Fatalf("limit=1 该只给 1 条并 truncated=true，得到 %d 条 truncated=%v", len(one), truncated)
	}
}

// 没配曲库目录时 fail closed（不是「什么都没扫到」——那是两种完全不同的结论）。
func TestQualityListLibraryFilesFailClosedWithoutRoots(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.it.qualityListLibraryFiles("lossy", 10); err == nil {
		t.Fatal("没有曲库目录时该报错，而不是回一份空列表")
	}
}

// 扫描根目录必须是**曲库目录**，不是「允许访问的根」（真机上是 /vol1 /vol2 /vol3 整卷）。
// 2026-10-21 真机踩过：拿安全边界当扫描根，整卷遍历把应用自己的数据目录扫了进来，
// 40 条结果全是 tts_*/uuid 中间产物，一首真歌都没进结果。
func TestQualityLibraryRootsUseDownloadDirNotAllowedRoots(t *testing.T) {
	lib := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", "/")
	h := newHarness(t)
	if got := h.it.qualityLibraryRoots(); len(got) != 0 {
		t.Fatalf("没配 download_dir 时该没有扫描根，得到 %v", got)
	}
	h.it.cfg.DownloadDir = func() string { return lib }
	if got := h.it.qualityLibraryRoots(); len(got) != 1 || got[0] != lib {
		t.Fatalf("扫描根该是曲库目录 %q，得到 %v", lib, got)
	}
}

func TestQualityReplaceRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", root)
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	flac := filepath.Join(root, "x.flac")
	if err := os.WriteFile(flac, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.it.replaceOneQuality(context.Background(), flac); err == nil ||
		!strings.Contains(err.Error(), "已经是无损") {
		t.Fatalf("无损文件该被拒并说明原因，得到 %v", err)
	}

	if _, err := h.it.replaceOneQuality(context.Background(), filepath.Join(root, "nope.mp3")); err == nil {
		t.Fatal("文件不存在该报错")
	}
	if _, err := h.it.replaceOneQuality(context.Background(), "/etc/passwd"); err == nil {
		t.Fatal("曲库外的路径该被守卫拦下")
	}
}

// 探不到渠道时如实留空：`channels` 空、`best` 为空、`upgradeable` 假 ——
// 界面据此显示「没探到」，而不是编一个档位出来。
func TestProbeQualityReportsNothingWhenNoChannelAnswers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FN_ALLOWED_ROOTS", root)
	p := filepath.Join(root, "Beyond - 不再犹豫.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	entry := h.it.probeQuality(context.Background(), p)
	if entry.Title != "不再犹豫" || entry.Artist != "Beyond" {
		t.Fatalf("名字该按「歌手 - 歌名」拆开，得到 artist=%q title=%q", entry.Artist, entry.Title)
	}
	// 渠道那一栏仍然按用户配的顺序把三行都列出来（界面要显示「这三条各是什么结果」），
	// 但档位一律是空串 —— 「没探到」与「探到了 320k」必须能分开。
	if len(entry.Channels) != 3 {
		t.Fatalf("该按配置列三条渠道，得到 %d 条：%+v", len(entry.Channels), entry.Channels)
	}
	for _, ch := range entry.Channels {
		if ch.Tier != "" || ch.Score != 0 {
			t.Fatalf("没人答话时档位该留空，得到 %+v", ch)
		}
	}
	if entry.Best != nil || entry.Upgradeable {
		t.Fatalf("没有任何渠道答话时 best 该为空、不可替换，得到 %+v", entry)
	}
}

// 扫描任务：空曲库也要走完并落到 done（不是一个永远 running 的僵尸状态）。
func TestQualityScanEmptyLibraryGoesDone(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	st, err := h.it.StartQualityScan("lossy", 50)
	if err != nil {
		t.Fatalf("起扫描失败：%v", err)
	}
	if st.State != QualityStateRunning || st.Total != 0 {
		t.Fatalf("起手该是 running/0 首，得到 %+v", st)
	}
	for n := 0; n < 200; n++ {
		time.Sleep(10 * time.Millisecond)
		if got := h.it.QualityScanStatus(); got.State == QualityStateDone {
			if got.Scanned != 0 || got.Upgradeable != 0 {
				t.Fatalf("空曲库该是 0/0，得到 %+v", got)
			}
			return
		}
	}
	t.Fatal("空曲库的扫描没能在合理时间内结束")
}

// 已有任务在跑时再点一次「开始」不该并起第二个（扫描是外呼重活，两份并行只是互相拖慢）。
func TestQualityScanDoesNotStartTwice(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t)
	h.it.cfg.DownloadDir = func() string { return root }

	h.it.quality.mu.Lock()
	h.it.quality.running = true
	h.it.quality.status = QualityStatus{State: QualityStateRunning, Total: 7, Scanned: 3}
	h.it.quality.mu.Unlock()

	st, err := h.it.StartQualityScan("all", 10)
	if err != nil {
		t.Fatalf("重复 start 不该报错：%v", err)
	}
	if st.Total != 7 || st.Scanned != 3 {
		t.Fatalf("重复 start 该回当前状态，得到 %+v", st)
	}
}

// 读盘上真实码率：早年存的低码率 mp3 必须能被判成「可替换」。
//
// 2026-10-21 真机踩到：按容器一律算 320k，于是 ffmpeg 转出来的 128k mp3 被判成
// 「已是各渠道最高音质」，用户真正需要升的那批反而换不了。
func TestQualityCurrentTierOfFileReadsMP3Bitrate(t *testing.T) {
	dir := t.TempDir()
	// 一个小工具：拼一个合法的 MPEG1 Layer3 帧头 + 静音数据。
	mkMP3 := func(name string, brIdx byte, withID3 bool) string {
		p := filepath.Join(dir, name)
		var b []byte
		if withID3 {
			// ID3v2.3，大小 100（同步安全整数字节：0,0,0,100）
			b = append(b, 'I', 'D', '3', 3, 0, 0, 0, 0, 0, 100)
			b = append(b, make([]byte, 100)...)
		}
		// 0xFF 0xFB = MPEG1 Layer3 无 CRC；第二字节高 4 位是码率索引
		b = append(b, 0xff, 0xfb|(brIdx>>4), (brIdx&0x0f)<<4|0x00, 0x00)
		b = append(b, make([]byte, 2000)...)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// MPEG1 Layer3 码率表：索引 5 = 64kbps，11 = 192kbps，14 = 320kbps
	cases := []struct {
		name string
		br   byte
		id3  bool
		want string
	}{
		{"low.mp3", 5, false, online.Tier128k},
		{"mid.mp3", 11, false, online.Tier192k},
		{"high.mp3", 14, false, online.Tier320k},
		{"tagged-low.mp3", 5, true, online.Tier128k},
	}
	for _, c := range cases {
		p := mkMP3(c.name, c.br, c.id3)
		if got := qualityCurrentTierOfFile(p, ".mp3"); got != c.want {
			t.Errorf("%s：该判 %s，得到 %s", c.name, c.want, got)
		}
	}
	// 无损容器不受码率探测影响
	p := filepath.Join(dir, "x.flac")
	if err := os.WriteFile(p, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := qualityCurrentTierOfFile(p, ".flac"); got != online.TierLossless {
		t.Errorf("flac 该判无损，得到 %s", got)
	}
	// 解不出来（不是合法 mp3）回落成 320k：宁可不升，也不拿猜的档位去覆盖
	bad := filepath.Join(dir, "bad.mp3")
	if err := os.WriteFile(bad, []byte("not an mp3 at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := qualityCurrentTierOfFile(bad, ".mp3"); got != online.Tier320k {
		t.Errorf("解不出来时该回落 320k，得到 %s", got)
	}
}

// 交叉校验：audioext（文件侧判据）与 online（平台侧判据）对档位的认识必须一致。
// 两边各写一份常量是有意的（audioext 要当叶子包，不 import 平台包），但一旦漂移，
// 「界面说能升、下载器说已存在」那类互相打脸就会回来 —— 用这个用例钉住。
func TestAudioextTierAgreesWithOnline(t *testing.T) {
	pairs := []struct{ ext, online string }{
		{audioext.TierLossless, online.TierLossless},
		{audioext.Tier320k, online.Tier320k},
		{audioext.Tier192k, online.Tier192k},
		{audioext.Tier128k, online.Tier128k},
	}
	for _, p := range pairs {
		if p.ext != p.online {
			t.Errorf("取值不一致：audioext %q vs online %q", p.ext, p.online)
		}
		if audioext.TierRank(p.ext) != online.TierRank(p.online) {
			t.Errorf("排名不一致：%q → audioext %d / online %d",
				p.ext, audioext.TierRank(p.ext), online.TierRank(p.online))
		}
	}
	// 平台侧认识的写法两边必须一致。
	for _, q := range []string{"flac24bit", "hires", "ape", "wav", "dsd", "320k", "128k", ""} {
		if audioext.TierRank(q) != online.TierRank(q) {
			t.Errorf("TierRank(%q)：audioext %d / online %d", q, audioext.TierRank(q), online.TierRank(q))
		}
	}
	// 文件侧还多认几种人写出来的写法（`无损`/`SQ`/`standard` 等）—— 那是下载接口
	// 收到的档位字符串可能来自前端或用户，必须比平台侧宽；但**不许**比平台侧更松地
	// 把某个已知档位判低，否则「要不要覆盖」的判断就会走偏。
	for _, q := range []string{"无损", "SQ", "lossless", "hi-res", "exhigh", "standard", "aac", "m4a"} {
		if got, ok := audioext.TierRank(q), online.TierRank(q); ok >= 0 && got < ok {
			t.Errorf("TierRank(%q)：文件侧 %d 比平台侧 %d 更保守", q, got, ok)
		}
	}
}

// ── 替换记录（需求 m04229：「替换音质，怎么看不到记录」）────────────────────

// 记录必须**落盘**：应用每次装机/升级都会重启，内存版本等于「一升级记录就没了」。
func TestQualityRecordsPersistAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	i := New(Config{
		DataDir:  dir,
		Upstream: &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
		Logf:     func(string, ...any) {},
	})
	if i == nil {
		t.Fatalf("New 返回 nil")
	}
	i.appendQualityRecord(QualityRecord{Path: "/x/a.mp3", Title: "a", From: "128k", To: "320k", Channel: "dl", OK: true})
	i.appendQualityRecord(QualityRecord{Path: "/x/b.mp3", Title: "b", From: "320k", OK: false, Note: "各渠道都没有更高的档位"})

	// 模拟重启：同一目录重新构造一个拦截层
	j := New(Config{
		DataDir:  dir,
		Upstream: &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
		Logf:     func(string, ...any) {},
	})
	got := j.QualityRecords()
	if len(got) != 2 {
		t.Fatalf("重启后应读到 2 条记录，得到 %d", len(got))
	}
	// 最新在前
	if got[0].Title != "b" || got[1].Title != "a" {
		t.Fatalf("记录顺序应最新在前，得到 %q, %q", got[0].Title, got[1].Title)
	}
	if got[0].OK || got[0].Note == "" {
		t.Fatalf("失败记录应保留原因：%+v", got[0])
	}
}

// 上限截断：记录是「最近做了什么」，不能无限长成存档。
func TestQualityRecordsCap(t *testing.T) {
	i := New(Config{
		DataDir:  t.TempDir(),
		Upstream: &fakeUpstream{fn: func(*http.Request, []byte) *http.Response { return nil }},
		Logf:     func(string, ...any) {},
	})
	for n := 0; n < qualityRecordLimit+20; n++ {
		i.appendQualityRecord(QualityRecord{Path: "/x/a.mp3", OK: true})
	}
	if got := len(i.QualityRecords()); got != qualityRecordLimit {
		t.Fatalf("应截断到 %d 条，得到 %d", qualityRecordLimit, got)
	}
}
