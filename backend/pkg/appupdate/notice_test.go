package appupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noticeManager 构造一个只关心 notice 的 Manager（不联网）。
func noticeManager(t *testing.T, dir, ver string) *Manager {
	t.Helper()
	m := New(Options{
		DataDir:        dir,
		ScriptDir:      dir,
		CurrentVersion: ver,
		Logf:           func(string, ...any) {},
		Now:            time.Now,
	})
	return m
}

// TestNoticeSilentOnFirstRun：首次运行不是「更新」，不能弹提示，但要落记录。
func TestNoticeSilentOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	m := noticeManager(t, dir, "2.21.0")
	if n := m.Notice(); n.Show {
		t.Fatalf("首次运行不该提示，实得 %+v", n)
	}
	if _, err := os.Stat(filepath.Join(dir, lastRunName)); err != nil {
		t.Fatalf("首次运行要落 last_run.json：%v", err)
	}
	// 同一版本第二次启动也不该提示。
	if n := m.Notice(); n.Show {
		t.Fatalf("同版本重启不该提示，实得 %+v", n)
	}
}

// TestNoticeAfterVersionChange：用户拍板「变化就提示」—— 版本变了必须弹，且带旧版本号。
func TestNoticeAfterVersionChange(t *testing.T) {
	dir := t.TempDir()
	noticeManager(t, dir, "2.21.0").Notice() // 记录 2.21.0

	m := noticeManager(t, dir, "2.21.1")
	n := m.Notice()
	if !n.Show {
		t.Fatal("版本从 2.21.0 变到 2.21.1 必须提示")
	}
	if n.Version != "2.21.1" || n.FromVersion != "2.21.0" {
		t.Fatalf("提示要带新旧版本号，实得 %+v", n)
	}
	if n.Direction != "up" {
		t.Fatalf("应判定为升级，实得 %q", n.Direction)
	}

	// 未 ack → 再读还是要提示（用户可能在提示渲染前就关了页面）。
	if n2 := m.Notice(); !n2.Show {
		t.Fatal("没确认之前必须继续提示")
	}
	if !m.AckNotice("2.21.1") {
		t.Fatal("ack 应成功")
	}
	if n3 := m.Notice(); n3.Show {
		t.Fatalf("ack 之后不该再提示，实得 %+v", n3)
	}
}

// TestNoticeDowngradeIsFlagged：降级也要提示，但方向要标成 down（不能写「更新成功」）。
func TestNoticeDowngradeIsFlagged(t *testing.T) {
	dir := t.TempDir()
	noticeManager(t, dir, "2.22.0").Notice()
	m := noticeManager(t, dir, "2.21.0")
	n := m.Notice()
	if !n.Show || n.Direction != "down" {
		t.Fatalf("降级应提示且方向为 down，实得 %+v", n)
	}
}

// TestAckIgnoresStaleVersion：旧页面（拿着旧版本号）点确认，不能把新版本的提示作废。
func TestAckIgnoresStaleVersion(t *testing.T) {
	dir := t.TempDir()
	noticeManager(t, dir, "2.21.0").Notice()
	m := noticeManager(t, dir, "2.21.1")
	if m.AckNotice("2.21.0") {
		t.Fatal("旧版本号的 ack 应被忽略")
	}
	if n := m.Notice(); !n.Show {
		t.Fatalf("旧 ack 之后仍应提示，实得 %+v", n)
	}
}

// TestNoticeSurvivesCorruptFile：last_run.json 写坏了不能影响启动，也不能反复弹。
func TestNoticeSurvivesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lastRunName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := noticeManager(t, dir, "2.21.0")
	if n := m.Notice(); n.Show {
		t.Fatalf("坏文件当首次运行处理（不提示），实得 %+v", n)
	}
}
