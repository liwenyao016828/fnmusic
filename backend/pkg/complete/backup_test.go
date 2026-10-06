package complete

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 造一个假的音频文件（内容随便，备份/还原不关心格式）
func writeAudio(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ── 核心：备份 → 改写 → 还原 ──

func TestBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "music", "周杰伦 - 晴天.mp3")
	writeAudio(t, audio, "原始内容")

	bk := NewBackup(NewBackupDir(filepath.Join(dir, "ai_backup")))
	if err := bk.BeforeWrite(audio); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 模拟 tidy 改写了文件，并新建了 .lrc
	writeAudio(t, audio, "被改写后的内容（多了标签）")
	writeAudio(t, filepath.Join(dir, "music", "周杰伦 - 晴天.lrc"), "[00:01.00]歌词")
	bk.AfterWrite(audio)

	if err := bk.Commit(); err != nil {
		t.Fatalf("落盘清单失败: %v", err)
	}
	if bk.Count() != 1 {
		t.Fatalf("应备份 1 个文件，实际 %d", bk.Count())
	}

	// 还原
	res := Restore(bk.Dir())
	if res.Restored != 1 {
		t.Fatalf("应还原 1 个文件，实际 %d（errors=%v）", res.Restored, res.Errors)
	}
	if res.Removed != 1 {
		t.Errorf("应删掉 1 个新建的 .lrc，实际 %d", res.Removed)
	}
	if got := readFile(t, audio); got != "原始内容" {
		t.Errorf("音频没还原：%q", got)
	}
	if exists(filepath.Join(dir, "music", "周杰伦 - 晴天.lrc")) {
		t.Error("新建的 .lrc 应该被删掉")
	}
	if exists(bk.Dir()) {
		t.Error("还原成功后备份目录应该被删掉")
	}
}

// ⚠️ 最重要的一条：**用户原有的 sidecar 不能被删**
//
// 补全只在「缺失时」写 .lrc，所以本来就存在的 .lrc 不是我们建的。
// 撤销时把它删掉 = 毁用户数据。
func TestRestoreKeepsPreexistingSidecar(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "晴天.mp3")
	lrc := filepath.Join(dir, "晴天.lrc")
	writeAudio(t, audio, "原始音频")
	writeAudio(t, lrc, "用户自己放进去的歌词")

	bk := NewBackup(NewBackupDir(filepath.Join(dir, "ai_backup")))
	if err := bk.BeforeWrite(audio); err != nil {
		t.Fatal(err)
	}
	// 改写音频（但 .lrc 本来就存在，不该被记为「新建」）
	writeAudio(t, audio, "改写后")
	bk.AfterWrite(audio)
	if err := bk.Commit(); err != nil {
		t.Fatal(err)
	}

	res := Restore(bk.Dir())
	if res.Removed != 0 {
		t.Errorf("不该删任何 sidecar（.lrc 是用户原有的），实际删了 %d 个", res.Removed)
	}
	if !exists(lrc) {
		t.Fatal("用户原有的 .lrc 被删了 —— 这是毁数据")
	}
	if got := readFile(t, lrc); got != "用户自己放进去的歌词" {
		t.Errorf(".lrc 内容被改了：%q", got)
	}
}

// 没备份任何文件时不该留下空目录
func TestBackupCommitWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	backupDir := NewBackupDir(filepath.Join(dir, "ai_backup"))
	bk := NewBackup(backupDir)
	if err := bk.Commit(); err != nil {
		t.Fatalf("空备份提交不该报错: %v", err)
	}
	if exists(backupDir) {
		t.Error("没备份文件时不该留下空目录")
	}
	if bk.Count() != 0 {
		t.Errorf("Count 应为 0，实际 %d", bk.Count())
	}
}

// FindLatest 取最近一次
func TestFindLatestPicksNewest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ai_backup")
	for _, name := range []string{"20260101-120000", "20260102-120000", "20260103-120000"} {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = writeManifest(d, []BackupEntry{{Original: "/x.mp3", Copy: "/y.mp3", Size: 1}})
	}
	got := FindLatest(root)
	if filepath.Base(got) != "20260103-120000" {
		t.Errorf("应取最新的，实际 %s", filepath.Base(got))
	}
}

func TestFindLatestNoBackup(t *testing.T) {
	if got := FindLatest(filepath.Join(t.TempDir(), "不存在")); got != "" {
		t.Errorf("没有备份时应返回空串，实际 %q", got)
	}
	// 目录存在但没有清单文件 → 也算没有可撤销的
	root := filepath.Join(t.TempDir(), "ai_backup")
	_ = os.MkdirAll(filepath.Join(root, "20260101-120000"), 0o755)
	if got := FindLatest(root); got != "" {
		t.Errorf("没有清单文件时应返回空串，实际 %q", got)
	}
}

// 还原失败时备份要保留（让用户能重试）
func TestRestoreKeepsBackupOnFailure(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "晴天.mp3")
	writeAudio(t, audio, "原始")

	bk := NewBackup(NewBackupDir(filepath.Join(dir, "ai_backup")))
	if err := bk.BeforeWrite(audio); err != nil {
		t.Fatal(err)
	}
	writeAudio(t, audio, "改写后")
	bk.AfterWrite(audio)
	if err := bk.Commit(); err != nil {
		t.Fatal(err)
	}

	// 把备份文件删掉，制造还原失败
	entries, _ := readManifest(bk.Dir())
	if err := os.Remove(entries[0].Copy); err != nil {
		t.Fatal(err)
	}

	res := Restore(bk.Dir())
	if res.Failed != 1 {
		t.Fatalf("应有 1 个失败，实际 %d", res.Failed)
	}
	if !exists(bk.Dir()) {
		t.Error("还原失败时备份应保留，供用户重试")
	}
}

// 多个文件（并发场景）
func TestBackupMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ai_backup")
	bk := NewBackup(NewBackupDir(root))

	var paths []string
	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		p := filepath.Join(dir, "music", n)
		writeAudio(t, p, "原始-"+n)
		paths = append(paths, p)
	}
	for _, p := range paths {
		if err := bk.BeforeWrite(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range paths {
		writeAudio(t, p, "改写-"+filepath.Base(p))
		bk.AfterWrite(p)
	}
	if err := bk.Commit(); err != nil {
		t.Fatal(err)
	}

	res := Restore(bk.Dir())
	if res.Restored != 3 {
		t.Fatalf("应还原 3 个，实际 %d（errors=%v）", res.Restored, res.Errors)
	}
	for _, p := range paths {
		want := "原始-" + filepath.Base(p)
		if got := readFile(t, p); got != want {
			t.Errorf("%s 内容 = %q，期望 %q", filepath.Base(p), got, want)
		}
	}
}

// 空目录 / nil 备份不能崩
func TestBackupNilAndEmpty(t *testing.T) {
	var bk *Backup
	if err := bk.BeforeWrite("/x.mp3"); err != nil {
		t.Errorf("nil 备份不该报错: %v", err)
	}
	bk.AfterWrite("/x.mp3")
	if err := bk.Commit(); err != nil {
		t.Errorf("nil 备份提交不该报错: %v", err)
	}
	if bk.Count() != 0 || bk.TotalSize() != 0 || bk.Dir() != "" {
		t.Error("nil 备份应返回零值")
	}

	// dir 为空的备份（不启用）
	empty := NewBackup("")
	if err := empty.BeforeWrite("/x.mp3"); err != nil {
		t.Errorf("未启用备份时不该报错: %v", err)
	}
	if empty.Count() != 0 {
		t.Error("未启用时不该记录任何文件")
	}

	if res := Restore(""); res.Restored != 0 || len(res.Errors) == 0 {
		t.Error("还原空目录应给出明确错误")
	}
}

// 原文件被删掉了，还原应该把它重建出来
func TestRestoreRecreatesDeletedFile(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "晴天.mp3")
	writeAudio(t, audio, "原始")

	bk := NewBackup(NewBackupDir(filepath.Join(dir, "ai_backup")))
	if err := bk.BeforeWrite(audio); err != nil {
		t.Fatal(err)
	}
	bk.AfterWrite(audio)
	if err := bk.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(audio)

	res := Restore(bk.Dir())
	if res.Restored != 1 {
		t.Fatalf("应还原 1 个，实际 %d（%v）", res.Restored, res.Errors)
	}
	if got := readFile(t, audio); got != "原始" {
		t.Errorf("文件没重建：%q", got)
	}
}

// ── 两阶段流程：没匹配上的项**绝不碰文件** ──
//
// 这是「先预览后写」的核心保证：预览里显示「没匹配上」的那些，
// 执行阶段必须原封不动 —— 既不改文件，也不该产生备份（没写就不需要备份）。

func TestExecuteSkipsUnmatchedWithoutTouchingFiles(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "认不出的歌.mp3")
	writeAudio(t, audio, "原始内容")

	backupDir := NewBackupDir(filepath.Join(dir, "ai_backup"))
	plan := Plan{Items: []PlanItem{
		{Path: audio, Title: "认不出的歌", Note: "没有搜到匹配的曲目"},
	}}

	sum := Execute(context.Background(), plan, Options{Backup: NewBackup(backupDir)}, nil)

	if sum.Total != 1 || sum.NoMatch != 1 {
		t.Errorf("应有 1 首「没搜到」，实际 total=%d no_match=%d", sum.Total, sum.NoMatch)
	}
	// 「没搜到」是**正常结果**，不是「写入失败」—— 同一首不该同时进两个计数，
	// 否则界面上会显示「没搜到 1 · 写入失败 1」，用户以为文件写坏了。
	if sum.Failed != 0 {
		t.Errorf("没搜到不该计为写入失败，实际 failed=%d", sum.Failed)
	}
	if sum.Matched != 0 || sum.LyricFilled != 0 || sum.CoverFilled != 0 {
		t.Errorf("没匹配上的项不该算作已补：%+v", sum)
	}
	if got := readFile(t, audio); got != "原始内容" {
		t.Errorf("没匹配上的项不该改文件，实际内容 %q", got)
	}
	if sum.BackupDir != "" || exists(backupDir) {
		t.Error("没写任何文件就不该产生备份目录")
	}
	if len(sum.Results) != 1 || sum.Results[0].Error == "" {
		t.Errorf("结果里应给出失败原因：%+v", sum.Results)
	}
}

// 空计划不该有任何副作用
func TestExecuteEmptyPlan(t *testing.T) {
	sum := Execute(context.Background(), Plan{}, Options{}, nil)
	if sum.Total != 0 || len(sum.Results) != 0 {
		t.Errorf("空计划应返回空汇总，实际 %+v", sum)
	}
}
