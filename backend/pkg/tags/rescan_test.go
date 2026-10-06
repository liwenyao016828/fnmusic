package tags

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 风格（genre）必须能写能读 —— 飞牛音乐「风格」页靠它归类，
// 不写的话那一页就是空的（参考实现轻乐集 v1.2.16 修过同一个坑）。
func TestGenreRoundTripMP3(t *testing.T) {
	path := writeTemp(t, "genre.mp3", mp3Fixture())

	if _, err := Write(path, Metadata{Title: "夜曲", Artist: "周杰伦", Genre: "Rock"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Genre != "Rock" {
		t.Errorf("MP3 genre = %q，期望 Rock", got.Genre)
	}
}

func TestGenreRoundTripFLAC(t *testing.T) {
	path := writeTemp(t, "genre.flac", flacFixture())

	if _, err := Write(path, Metadata{Title: "晴天", Artist: "周杰伦", Genre: "民谣"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Genre != "民谣" {
		t.Errorf("FLAC genre = %q，期望 民谣", got.Genre)
	}
}

// 只有 genre 也要算「非空」，否则会被 IsEmpty 拦掉、根本写不进去
func TestGenreAloneIsNotEmpty(t *testing.T) {
	if (Metadata{Genre: "Jazz"}).IsEmpty() {
		t.Error("只填 genre 时不应判为空")
	}
	if !(Metadata{}).IsEmpty() {
		t.Error("全空应判为空")
	}
}

// 重写标签时 genre 要跟着更新（不能只留旧的）
func TestGenreRewriteReplacesValue(t *testing.T) {
	path := writeTemp(t, "rewrite.flac", flacFixture())

	_, _ = Write(path, Metadata{Title: "x", Genre: "Rock"})
	if _, err := Write(path, Metadata{Title: "x", Genre: "Jazz"}); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	got, _ := Read(path)
	if got.Genre != "Jazz" {
		t.Errorf("重写后 genre = %q，期望 Jazz（应替换而非保留旧值）", got.Genre)
	}
}

// ── TouchForRescan ──

// 核心行为：把 mtime 推到当下。
// 飞牛按「路径 + mtime/size」判断要不要重读，不推就永远读不到改动。
func TestTouchForRescanUpdatesMtime(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(audio, mp3Fixture(), 0644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	// 把 mtime 设到很久以前，模拟「文件没动过」
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(audio, old, old); err != nil {
		t.Fatalf("设置旧时间失败: %v", err)
	}

	TouchForRescan(audio)

	info, err := os.Stat(audio)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("mtime 没有被推到当下，仍是 %v", info.ModTime())
	}
}

// 附属文件（.lrc / 封面）也要一起推 —— 它们同样靠时间戳被判断是否重读
func TestTouchForRescanTouchesSidecars(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	lrc := filepath.Join(dir, "song.lrc")
	cover := filepath.Join(dir, "song.jpg")

	for _, p := range []string{audio, lrc, cover} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("准备 %s 失败: %v", p, err)
		}
		old := time.Now().Add(-48 * time.Hour)
		_ = os.Chtimes(p, old, old)
	}

	TouchForRescan(audio)

	for _, p := range []string{audio, lrc, cover} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat %s: %v", p, err)
		}
		if time.Since(info.ModTime()) > time.Minute {
			t.Errorf("%s 的 mtime 没被更新", filepath.Base(p))
		}
	}
}

// 不存在的附属文件要被跳过，不能凭空造出来
func TestTouchForRescanDoesNotCreateFiles(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(audio, mp3Fixture(), 0644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	TouchForRescan(audio)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "song.mp3" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("不应创建任何附属文件，实际目录里有: %v", names)
	}
}

func TestTouchForRescanHandlesEmptyAndMissing(t *testing.T) {
	// 空路径不应 panic
	TouchForRescan("")
	// 不存在的音频文件不应 panic
	TouchForRescan(filepath.Join(t.TempDir(), "nope.mp3"))
}
