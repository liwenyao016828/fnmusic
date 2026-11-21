package downloader

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 目标名已被占用时必须失败，而不是静默覆盖别人写在那儿的文件。
func TestInstallFileExclusiveRefusesOccupiedTarget(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, ".song.part")
	target := filepath.Join(dir, "song.txt")

	if err := os.WriteFile(tmp, []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("someone else"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := installFileExclusive(tmp, target)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("占用时应返回 os.ErrExist，得到 %v", err)
	}
	// 被占用的目标内容不能被破坏
	got, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "someone else" {
		t.Fatalf("目标被覆盖了: %q", got)
	}
	// 临时文件还在，调用方才能换名字重试
	if _, serr := os.Stat(tmp); serr != nil {
		t.Fatalf("临时文件不该被删: %v", serr)
	}
}

func TestInstallFileExclusiveSucceeds(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, ".song.part")
	target := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(tmp, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installFileExclusive(tmp, target); err != nil {
		t.Fatalf("空目标应安装成功: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "audio" {
		t.Fatalf("内容不对: %q %v", got, err)
	}
	// link 成功后临时文件由调用方删除；这里确认名字不再是硬链接的第二个引用
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("临时文件应可删除: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("删临时文件不该影响目标: %v", err)
	}
}

// 同一个 .part 的并发写入必须串行化：并行写同一份字节流会交错，
// 坏 MP3 会被当成好文件入库（审计 M3）。
func TestLockPartSerializesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".same.part")

	var inside atomic.Int32
	var overlap atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockPart(path)
			defer unlock()
			if inside.Add(1) > 1 {
				overlap.Add(1)
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()

	if overlap.Load() != 0 {
		t.Fatalf("%d 个写入者同时在锁内", overlap.Load())
	}
	// 用完的锁条目要摘掉，否则连续下几万首会把 map 撑大
	partLocks.mu.Lock()
	n := len(partLocks.m)
	partLocks.mu.Unlock()
	if n != 0 {
		t.Fatalf("锁表没清干净，剩 %d 条", n)
	}
}

func TestLockPartSeparatesDifferentPaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.part")
	b := filepath.Join(dir, "b.part")

	unlockA := lockPart(a)
	defer unlockA()

	// 不同的 .part 不能被同一把锁挡住
	done := make(chan struct{})
	go func() {
		unlockB := lockPart(b)
		unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("不同路径互相阻塞了")
	}
}
