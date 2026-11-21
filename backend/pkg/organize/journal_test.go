package organize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestJournalChainKeepsMultipleRuns 撤销链要留得下多批整理。
//
// 老做法只留最后一次，用户连着整理两轮又改主意时就没得撤了。
func TestJournalChainKeepsMultipleRuns(t *testing.T) {
	dir := t.TempDir()
	mk := func(n int) Journal {
		return Journal{
			Root:  "/music",
			Moves: []Move{{From: "/music/b" + string(rune('0'+n)) + ".flac", To: "/music/a.flac"}},
			Moved: 1,
		}
	}
	if err := SaveJournal(dir, mk(1)); err != nil {
		t.Fatal(err)
	}
	if err := SaveJournal(dir, mk(2)); err != nil {
		t.Fatal(err)
	}

	all := LoadJournals(dir)
	if len(all) != 2 {
		t.Fatalf("撤销链应有 2 批，实际 %d", len(all))
	}
	// 最后一批是最近那次
	last, ok := LoadJournal(dir)
	if !ok || last.Moves[0].From != "/music/b2.flac" {
		t.Fatalf("最近一批取错: %+v", last)
	}
	// 撤掉一批之后，另一批还在
	popped, ok := PopJournal(dir)
	if !ok || popped.Moves[0].From != "/music/b2.flac" {
		t.Fatalf("PopJournal 取错: %+v", popped)
	}
	rest := LoadJournals(dir)
	if len(rest) != 1 || rest[0].Moves[0].From != "/music/b1.flac" {
		t.Fatalf("撤销一批后剩下的不对: %+v", rest)
	}
}

// TestReplaceLastJournalOverwritesPrepared 执行前写的「计划版」要被真实结果覆盖，
// 而不是变成两批一模一样的记录。
func TestReplaceLastJournalOverwritesPrepared(t *testing.T) {
	dir := t.TempDir()
	prepared := Journal{Root: "/music", Moves: []Move{{From: "/x", To: "/y"}}, Moved: 1}
	if err := SaveJournal(dir, prepared); err != nil {
		t.Fatal(err)
	}
	real := Journal{Root: "/music", Moves: []Move{{From: "/y", To: "/x"}}, Moved: 3}
	if err := ReplaceLastJournal(dir, real); err != nil {
		t.Fatal(err)
	}
	all := LoadJournals(dir)
	if len(all) != 1 {
		t.Fatalf("应该还是 1 批，实际 %d", len(all))
	}
	if all[0].Moved != 3 || all[0].Moves[0].From != "/y" {
		t.Fatalf("没有被覆盖: %+v", all[0])
	}
}

// TestDropLastJournal 一批都没搬成时，计划版要能被丢掉。
func TestDropLastJournal(t *testing.T) {
	dir := t.TempDir()
	if err := SaveJournal(dir, Journal{Root: "/music", Moves: []Move{{From: "/x", To: "/y"}}}); err != nil {
		t.Fatal(err)
	}
	if err := DropLastJournal(dir); err != nil {
		t.Fatal(err)
	}
	if all := LoadJournals(dir); len(all) != 0 {
		t.Fatalf("应该被丢掉，实际还剩 %d 批", len(all))
	}
	if _, err := os.Stat(filepath.Join(dir, journalFileName)); !os.IsNotExist(err) {
		t.Fatalf("文件应该被删掉，实际状态: %v", err)
	}
}

// TestJournalReadsOldSingleRunFormat 老版本写的是单条记录（顶层就有 moves），
// 升级后要照样能读出来，否则用户升级应用就丢了一次撤销机会。
func TestJournalReadsOldSingleRunFormat(t *testing.T) {
	dir := t.TempDir()
	old := Journal{
		Root:  "/music",
		Moves: []Move{{From: "/b.flac", To: "/a.flac"}},
		Moved: 1,
	}
	raw, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, journalFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadJournal(dir)
	if !ok {
		t.Fatal("老格式的撤销记录应该能读出来")
	}
	if got.Moves[0].From != "/b.flac" {
		t.Fatalf("内容不对: %+v", got)
	}
	all := LoadJournals(dir)
	if len(all) != 1 {
		t.Fatalf("老格式应被当作 1 批，实际 %d", len(all))
	}
}
