package online

import (
	"path/filepath"
	"testing"
)

// ── 自动下载记录（见 autolog.go） ──────────────────────────────────────────
//
// 这张表的价值全在「用户能不能查到刚才那次下了没」上，所以用例钉三件事：
// 新的在前、失败也记、删了就是删了（且能只删指定的几条）。

func TestAutoLogKeepsNewestFirstAndPersists(t *testing.T) {
	dir := t.TempDir()
	l, err := NewAutoLog(dir)
	if err != nil {
		t.Fatalf("建表失败：%v", err)
	}
	for i, title := range []string{"第一首", "第二首", "第三首"} {
		if err := l.Append(AutoLogEntry{At: int64(100 + i), Trigger: TriggerFavorite, Title: title, OK: true}); err != nil {
			t.Fatalf("追加失败：%v", err)
		}
	}

	got := l.List(10)
	if len(got) != 3 {
		t.Fatalf("应有 3 条，得到 %d", len(got))
	}
	// 新的在前：用户来查记录问的是「刚才那次下了没」。
	if got[0].Title != "第三首" {
		t.Fatalf("最新的一条应排最前，得到 %q", got[0].Title)
	}

	// 落盘之后重开必须还在（进程重启就丢的话，这张表就没意义了）。
	l2, err := NewAutoLog(dir)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if n := len(l2.List(10)); n != 3 {
		t.Fatalf("重开后应有 3 条，得到 %d", n)
	}
}

func TestAutoLogRecordsFailures(t *testing.T) {
	l, _ := NewAutoLog(t.TempDir())
	_ = l.Append(AutoLogEntry{At: 1, Trigger: TriggerFavorite, Title: "好的", OK: true})
	_ = l.Append(AutoLogEntry{At: 2, Trigger: TriggerPlaylist, Title: "坏的", OK: false, Error: "解析直链失败"})

	total, failed := l.Stats()
	if total != 2 || failed != 1 {
		t.Fatalf("应有 2 条、其中 1 条失败，得到 total=%d failed=%d", total, failed)
	}
	// 失败那条的原因要留下 —— 界面上「下载失败」四个字对排查毫无用处。
	if l.List(1)[0].Error == "" {
		t.Fatalf("失败记录必须带上人能读懂的原因")
	}
}

func TestAutoLogIDsAreUniqueWithinSameSecond(t *testing.T) {
	l, _ := NewAutoLog(t.TempDir())
	// 同一秒里连着下三首同名歌（歌单里同一首出现两次是常事）。
	for i := 0; i < 3; i++ {
		_ = l.Append(AutoLogEntry{At: 100, Trigger: TriggerPlaylist, GUID: "same", Title: "同名", OK: true})
	}
	seen := map[string]bool{}
	for _, it := range l.List(10) {
		if seen[it.ID] {
			t.Fatalf("同一秒内的记录 id 撞号了：%s（撞号的表现是「删一条删掉两条」）", it.ID)
		}
		seen[it.ID] = true
	}
}

func TestAutoLogRemoveByIDsAndClearAll(t *testing.T) {
	l, _ := NewAutoLog(t.TempDir())
	for i := 0; i < 4; i++ {
		_ = l.Append(AutoLogEntry{At: int64(10 + i), Trigger: TriggerFavorite, Title: string(rune('a' + i)), OK: true})
	}
	items := l.List(10)

	// 只删指定的一条：其余三条必须原样还在。
	removed, err := l.Remove([]string{items[1].ID})
	if err != nil || removed != 1 {
		t.Fatalf("应删掉 1 条，得到 removed=%d err=%v", removed, err)
	}
	if n := len(l.List(10)); n != 3 {
		t.Fatalf("应剩 3 条，得到 %d", n)
	}

	// 空 ids = 全清（界面上那个「一键清除」）。
	removed, err = l.Remove(nil)
	if err != nil || removed != 3 {
		t.Fatalf("全清应删掉 3 条，得到 removed=%d err=%v", removed, err)
	}
	if n := len(l.List(10)); n != 0 {
		t.Fatalf("全清后应为空，得到 %d", n)
	}

	// 重开之后仍然是空的 —— 删除必须落盘，否则「清了又回来」。
	dir := filepath.Dir(l.path)
	l2, err := NewAutoLog(dir)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if n := len(l2.List(10)); n != 0 {
		t.Fatalf("重开后仍应为空，得到 %d 条", n)
	}
}

func TestAutoLogCapsLength(t *testing.T) {
	l, _ := NewAutoLog(t.TempDir())
	for i := 0; i < autoLogLimit+20; i++ {
		_ = l.Append(AutoLogEntry{At: int64(i), Trigger: TriggerFavorite, OK: true})
	}
	if n := len(l.List(autoLogLimit + 10)); n != autoLogLimit {
		t.Fatalf("应封顶在 %d 条，得到 %d", autoLogLimit, n)
	}
	// 丢的是**最旧的**（表是倒序，所以丢尾部）—— 丢最新的等于「刚下的看不见」。
	if got := l.List(1)[0].At; got != int64(autoLogLimit+19) {
		t.Fatalf("最新的那条必须还在，得到 At=%d", got)
	}
}

// nil 表（构造失败时降级）上的每个方法都必须安全 —— 接口层会照常调它们。
func TestAutoLogNilIsSafe(t *testing.T) {
	var l *AutoLog
	if err := l.Append(AutoLogEntry{}); err != nil {
		t.Fatalf("nil 表 Append 应无错：%v", err)
	}
	if n := len(l.List(10)); n != 0 {
		t.Fatalf("nil 表 List 应为空，得到 %d", n)
	}
	if total, failed := l.Stats(); total != 0 || failed != 0 {
		t.Fatalf("nil 表 Stats 应为零值，得到 %d/%d", total, failed)
	}
	if n, err := l.Remove(nil); n != 0 || err != nil {
		t.Fatalf("nil 表 Remove 应无错且删 0 条，得到 %d/%v", n, err)
	}
}
