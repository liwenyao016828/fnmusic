package config

import (
	"os"
	"path/filepath"
	"testing"
)

func strPtr(s string) *string { return &s }

// 命名格式（`filename_format`）与缓存目录（`cache_dir`）是 2026-10-15 新增的两个
// 设置项。这里钉的和 chartplaylists_test.go 是同一件事：
//
//  1. Update（界面保存）之后**重启进程**还能读回来；
//  2. `load` 与 `Update` 是两处独立的选择性合并，**两边都要登记**；
//  3. 空值 = 「没设置过 / 不改」，不能用它把盘上已有的值抹掉。
//
// 第 2 条这次**真的又踩了一次**：第一版实现只把 CacheDir 登记进 Update，
// `load` 里漏了 —— 表现正是「界面上改了当时生效、重启后自己变回默认」。
// 所以下面 TestLoadKeepsFilenameFormatAndCacheDir 是针对性回归，不是形式主义。
func TestFilenameFormatAndCacheDirSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	cfg := cm.Get()
	cfg.FilenameFormat = "title-artist"
	cfg.CacheDir = strPtr("/vol2/cache")
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if got := cm.Get().FilenameFormat; got != "title-artist" {
		t.Fatalf("内存里就该是新值，得到 %q", got)
	}

	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	got := cm2.Get()
	if got.FilenameFormat != "title-artist" {
		t.Errorf("重启后 filename_format 应为 title-artist，得到 %q", got.FilenameFormat)
	}
	if got.CacheDirPath() != "/vol2/cache" {
		t.Errorf("重启后 cache_dir 应为 /vol2/cache，得到 %q", got.CacheDirPath())
	}
}

// 配置文件里已经有这两个键时，`load` 必须把它们读进来（只登记 Update 的回归）。
func TestLoadKeepsFilenameFormatAndCacheDir(t *testing.T) {
	dir := t.TempDir()
	raw := `{
	  "port": 8899,
	  "filename_format": "title-artist",
	  "cache_dir": "/vol2/cache"
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := cm.Get()
	if got.FilenameFormat != "title-artist" {
		t.Errorf("load 漏登记 filename_format（得到 %q）—— 表现是「改了能用、重启就没了」", got.FilenameFormat)
	}
	if got.CacheDirPath() != "/vol2/cache" {
		t.Errorf("load 漏登记 cache_dir（得到 %q）—— 同上", got.CacheDirPath())
	}
}

// 空值 = 不改。界面上只提交了下载目录时，不能顺手把命名格式和缓存目录清空。
func TestEmptyFilenameFormatAndCacheDirMeanDoNotTouch(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	cfg := cm.Get()
	cfg.FilenameFormat = "title-artist"
	cfg.CacheDir = strPtr("/vol2/cache")
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("首次 Update 失败: %v", err)
	}

	// 只带一个字段的「补丁式」提交（POST /api/config 就是这种形状）
	patch := cm.Get()
	patch.FilenameFormat = ""
	patch.CacheDir = nil
	patch.DownloadDir = "/vol1/Music"
	if err := cm.Update(patch); err != nil {
		t.Fatalf("补丁 Update 失败: %v", err)
	}
	got := cm.Get()
	if got.FilenameFormat != "title-artist" {
		t.Errorf("空 filename_format 不该覆盖旧值，得到 %q", got.FilenameFormat)
	}
	if got.CacheDirPath() != "/vol2/cache" {
		t.Errorf("nil cache_dir 不该覆盖旧值，得到 %q", got.CacheDirPath())
	}
	if got.DownloadDir != "/vol1/Music" {
		t.Errorf("同一批里的下载目录应该生效，得到 %q", got.DownloadDir)
	}
}

// ValidateDirs 是「缓存目录与下载目录不得相同、不得互为父子」的唯一实现点
// （上游 zouclang/fnos_music_ext v2.8.0 的目录校验，理由见函数注释）。
//
// 注意末尾两组用例：**空值必须放行** —— 缓存目录留空表示「用程序推导的默认值」，
// 而那个默认值本身就是下载目录下的隐藏子目录，属于「互为父子」；如果空值也拦，
// 默认配置自己就非法了。
func TestValidateDirs(t *testing.T) {
	cases := []struct {
		name        string
		download    string
		cache       string
		wantProblem bool
	}{
		{"两个都空（新装）", "", "", false},
		{"只有下载目录", "/vol1/Music", "", false},
		{"只有缓存目录", "", "/vol2/cache", false},
		{"不同目录", "/vol1/Music", "/vol2/cache", false},
		{"同级兄弟目录", "/vol1/Music", "/vol1/MusicCache", false},
		{"同名不同写法（尾斜杠）", "/vol1/Music/", "/vol1/Music", true},
		{"相同目录", "/vol1/Music", "/vol1/Music", true},
		{"缓存是下载的子目录", "/vol1/Music", "/vol1/Music/.cache", true},
		{"下载是缓存的子目录", "/vol1/Music", "/vol1", true},
		{"路径里有 .. 也要认出来", "/vol1/Music", "/vol1/Other/../Music", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := AppConfig{DownloadDir: c.download}
			if c.cache != "" {
				cfg.CacheDir = strPtr(c.cache)
			}
			got := cfg.ValidateDirs()
			if c.wantProblem && got == "" {
				t.Errorf("下载=%q 缓存=%q 应判为非法，却放行了", c.download, c.cache)
			}
			if !c.wantProblem && got != "" {
				t.Errorf("下载=%q 缓存=%q 应放行，却报了：%s", c.download, c.cache, got)
			}
		})
	}
}

// 显式空串 = 回到默认。这是 CacheDir 做成**指针**的唯一理由（见 AppConfig 注释）：
// 只有两态（空 / 非空）的话，用户一旦设过自定义缓存目录，就再也回不到
// 「用程序推导的默认值」——界面上那个「恢复默认」按钮会变成假按钮。
func TestExplicitEmptyCacheDirReturnsToDefault(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	cfg := cm.Get()
	cfg.CacheDir = strPtr("/vol2/cache")
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("首次 Update 失败: %v", err)
	}
	if cm.Get().CacheDirPath() != "/vol2/cache" {
		t.Fatal("前置条件不成立：自定义缓存目录没写进去")
	}

	clear := cm.Get()
	clear.CacheDir = strPtr("") // 显式清空，而不是「不带这个字段」
	if err := cm.Update(clear); err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if got := cm.Get().CacheDirPath(); got != "" {
		t.Errorf("显式空串应回到默认（CacheDirPath 为空串），得到 %q", got)
	}

	// 重启后必须仍然是「默认」：JSON 里那个 cache_dir:"" 不能被 omitempty 吃掉
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	if got := cm2.Get().CacheDirPath(); got != "" {
		t.Errorf("重启后应仍是默认，得到 %q —— 检查 json tag 是否漏了", got)
	}
}

// 「下载音质」开关：三态（跟随 / 无损 / 320k）与「认不出的值当跟随」。
func TestDownloadQualityOverrideTriState(t *testing.T) {
	mgr, err := NewConfigManager(t.TempDir(), 8080)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}

	// 老配置里没有这个键 = 跟随
	if got := mgr.Get().DownloadQualitySetting(); got != DownloadQualityFollow {
		t.Fatalf("缺省应为跟随，得到 %q", got)
	}

	// 写成「无损」
	on := DownloadQualityLossless
	if err := mgr.Update(AppConfig{DownloadQualityOverride: &on}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if got := mgr.Get().DownloadQualitySetting(); got != DownloadQualityLossless {
		t.Fatalf("应为 lossless，得到 %q", got)
	}

	// 关键：能回到「跟随」—— 用 string + 非空才写的合并规则就永远回不去。
	off := DownloadQualityFollow
	if err := mgr.Update(AppConfig{DownloadQualityOverride: &off}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if got := mgr.Get().DownloadQualitySetting(); got != DownloadQualityFollow {
		t.Fatalf("应回到跟随，得到 %q", got)
	}

	// 不传 = 不动（选择性合并的另一半语义）
	keep := DownloadQualityMP3
	if err := mgr.Update(AppConfig{DownloadQualityOverride: &keep}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if err := mgr.Update(AppConfig{Port: 8081}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if got := mgr.Get().DownloadQualitySetting(); got != DownloadQualityMP3 {
		t.Fatalf("没传这个字段时应当保持 mp3_320，得到 %q", got)
	}

	// 重启（重读盘）后仍在
	mgr2, err := NewConfigManager(filepath.Dir(mgr.filePath), 8080)
	if err != nil {
		t.Fatalf("重建配置管理器失败：%v", err)
	}
	if got := mgr2.Get().DownloadQualitySetting(); got != DownloadQualityMP3 {
		t.Fatalf("重启后应保持 mp3_320，得到 %q", got)
	}
}

// QualityOverride 是「开关套到某个入口自己的档位上」，覆盖优先、认不出当跟随。
func TestQualityOverrideMapping(t *testing.T) {
	cases := []struct {
		base, override, want string
	}{
		{"highest", "", "highest"},         // 跟随：不动
		{"320k", "", "320k"},               // 跟随：不动
		{"128k", "lossless", "flac"},       // 覆盖：一律无损
		{"flac24bit", "lossless", "flac"},  // 覆盖：一律无损
		{"highest", "mp3_320", "320k"},     // 覆盖：一律 320k
		{"flac", "mp3_320", "320k"},        // 覆盖：一律 320k
		{"128k", "乱写的", "128k"},            // 认不出 = 跟随（退化成各用各的，不钉死）
		{"highest", "  ", "highest"},       // 空白 = 跟随
		{"highest", "LOSSLESS", "highest"}, // 大小写不敏感？不 —— 认不出当跟随
	}
	for _, c := range cases {
		if got := QualityOverride(c.base, c.override); got != c.want {
			t.Errorf("QualityOverride(%q, %q) = %q，期望 %q", c.base, c.override, got, c.want)
		}
	}
}
