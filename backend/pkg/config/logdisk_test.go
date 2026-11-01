package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 注：strPtr / boolPtr 两个小助手本文件在同包的既有测试里已有定义
// （namefmt_dirs_test.go、uiinject_test.go），这里直接复用，别重复声明。

// 运行日志落盘（`log_dir` + `log_disk_enabled`）是 2026-11-01 补的一批改动
// （用户 m00938「出了问题必须能追查」）。这里钉三件事：
//
//  1. **重启后读得回来** —— log_dir 在 2026-10-26 上线时只声明、没登记进
//     `load`/`Update` 两处选择性合并，表现是「设置页保存成功、回读仍为空、
//     重启更是全没了」。这是真踩过的 bug，必须回归。
//  2. `Update` 的补丁语义：nil = 这次没提 → 不动；false 是**真值**，不能被
//     当成「没传」吞掉（布尔开关最常见的坑，和 CacheDir 的空串三态同理）。
//  3. `EffectiveLogDir` 是「到底写不写、写到哪」的唯一判据：没表态默认开、
//     默认落在数据目录下的 logs；显式关掉才返回空串。
func TestLogDiskSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 出厂态：没表过态 → 默认开，落到 <数据目录>/logs
	if !cm.Get().LogDiskOn() {
		t.Fatal("没设置过时应当默认开启落盘（用户要求出问题必须能追查）")
	}
	if got, want := cm.EffectiveLogDir(), filepath.Join(dir, "logs"); got != want {
		t.Fatalf("默认落盘目录 = %q，想要 %q", got, want)
	}

	// 换到自定义目录 + 显式关掉，重启后都要原样回来
	cfg := cm.Get()
	cfg.LogDir = strPtr("/vol2/qulv-logs")
	cfg.LogDiskEnabled = boolPtr(false)
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if got := cm.Get().LogDirPath(); got != "/vol2/qulv-logs" {
		t.Fatalf("内存里就该是新目录，得到 %q", got)
	}
	if cm.Get().LogDiskOn() {
		t.Fatal("显式传 false 应当是关闭 —— 布尔零值被当成「没传」是最常见的坑")
	}
	if got := cm.EffectiveLogDir(); got != "" {
		t.Fatalf("关掉后生效目录应当是空串，得到 %q", got)
	}

	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	if got := cm2.Get().LogDirPath(); got != "/vol2/qulv-logs" {
		t.Fatalf("重启后目录丢了：得到 %q（load 没登记这个字段的典型症状）", got)
	}
	if cm2.Get().LogDiskOn() {
		t.Fatal("重启后开关状态丢了：关掉的落盘又自己开了")
	}
	if got := cm2.EffectiveLogDir(); got != "" {
		t.Fatalf("重启后生效目录应当还是空串，得到 %q", got)
	}
}

// 补丁语义：只传 enabled 时不动目录，只传目录时不动开关。
// 设置页的两个动作（切换开关 / 保存目录）分别走这两条路，串了就会互相踩。
func TestLogDiskPatchSemantics(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	// 先设一个目录
	if err := cm.Update(AppConfig{LogDir: strPtr("/vol1/Logs")}); err != nil {
		t.Fatalf("Update 目录失败: %v", err)
	}
	if got := cm.Get().LogDirPath(); got != "/vol1/Logs" {
		t.Fatalf("目录没写进去，得到 %q", got)
	}

	// 只关开关：目录必须原地不动（关掉不等于忘掉位置，下次开还在那儿）
	if err := cm.Update(AppConfig{LogDiskEnabled: boolPtr(false)}); err != nil {
		t.Fatalf("Update 开关失败: %v", err)
	}
	if got := cm.Get().LogDirPath(); got != "/vol1/Logs" {
		t.Fatalf("只改开关却把目录弄丢了，得到 %q", got)
	}
	if cm.Get().LogDiskOn() {
		t.Fatal("开关没生效")
	}

	// 只改目录：开关状态不能被顺手打开
	if err := cm.Update(AppConfig{LogDir: strPtr("/vol2/Logs")}); err != nil {
		t.Fatalf("Update 目录失败: %v", err)
	}
	if cm.Get().LogDiskOn() {
		t.Fatal("只改目录却又把落盘打开了 —— 用户明确关过的东西不该被别的动作带回来")
	}
	if got := cm.EffectiveLogDir(); got != "" {
		t.Fatalf("仍然是关闭态，生效目录应当为空，得到 %q", got)
	}
}

// 目录传空串 = 回到默认位置（**不是**关闭）。这是设置页「恢复默认」按钮的语义，
// 和关开关是两件事 —— 以前这两件事共用一个字段，清空输入框就把日志整个关了。
func TestLogDirEmptyMeansDefaultNotOff(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	if err := cm.Update(AppConfig{LogDir: strPtr("/vol1/Logs")}); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if err := cm.Update(AppConfig{LogDir: strPtr("")}); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !cm.Get().LogDiskOn() {
		t.Fatal("清空目录不等于关闭落盘")
	}
	if got, want := cm.EffectiveLogDir(), filepath.Join(dir, "logs"); got != want {
		t.Fatalf("空串应当回到默认目录 %q，得到 %q", want, got)
	}

	// 落盘文件确实会写到那儿（顺带钉住 EnableFile 的目录约定）
	if err := os.MkdirAll(cm.EffectiveLogDir(), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
}

// 落盘目录要能在盘上真的建出来 —— `EffectiveLogDir` 只算路径，
// 「能不能写」是 applog.EnableFile 的事，这里只验证路径合法且在建得出来的位置。
func TestEffectiveLogDirUnderDataDirIsWritable(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	target := cm.EffectiveLogDir()
	if !filepath.IsAbs(target) {
		t.Fatalf("生效目录必须是绝对路径，得到 %q", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("默认落盘目录建不出来: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("目录不可用: %v", err)
	}
}