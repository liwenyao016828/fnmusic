package intercept

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
