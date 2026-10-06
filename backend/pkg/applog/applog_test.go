package applog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBufferKeepsRecent(t *testing.T) {
	b := New(5)
	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		b.Add(s)
	}
	got := b.Recent(0)
	if len(got) != 5 {
		t.Fatalf("期望保留 5 条，实际 %d", len(got))
	}
	if got[len(got)-1].Message != "f" {
		t.Errorf("最后一条应为 f，实际 %q", got[len(got)-1].Message)
	}
	for _, e := range got {
		if e.Message == "a" {
			t.Error("最旧的一条应已被丢弃")
		}
	}
}

func TestBufferRecentLimit(t *testing.T) {
	b := New(100)
	for i := 0; i < 10; i++ {
		b.Add("x")
	}
	if got := b.Recent(3); len(got) != 3 {
		t.Errorf("Recent(3) 应返回 3 条，实际 %d", len(got))
	}
	if got := b.Recent(999); len(got) != 10 {
		t.Errorf("limit 超过总数时应返回全部，实际 %d", len(got))
	}
}

// 标准库 log 一次 Write 可能带多行，要拆开存，不能挤成一条
func TestBufferWriteSplitsLines(t *testing.T) {
	b := New(10)
	if _, err := b.Write([]byte("line1\nline2\n")); err != nil {
		t.Fatal(err)
	}
	if got := b.Recent(0); len(got) != 2 {
		t.Fatalf("一次写多行应拆成 2 条，实际 %d", len(got))
	}
}

func TestLevelOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[ERROR] something broke", "error"},
		{"[FATAL] cannot start", "error"},
		{"download failed", "error"},
		{"[WARN] slow", "warn"},
		{"[INIT] starting", "info"},
	}
	for _, c := range cases {
		if got := levelOf(c.in); got != c.want {
			t.Errorf("levelOf(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestClear(t *testing.T) {
	b := New(10)
	b.Add("x")
	b.Clear()
	if b.Len() != 0 {
		t.Error("Clear 后应为空")
	}
}

// 日志会被多个 goroutine 同时写（HTTP handler + 调度器），必须并发安全
func TestConcurrentAccess(t *testing.T) {
	b := New(50)
	done := make(chan bool)
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				b.Add("concurrent")
				_ = b.Recent(10)
			}
			done <- true
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if b.Len() > 50 {
		t.Errorf("并发写入后不应超过上限，实际 %d", b.Len())
	}
}

// 服务端自己的日志必须标记成 backend，前端才能按来源筛选
func TestAddMarksBackendSource(t *testing.T) {
	b := New(10)
	b.Add("服务端日志")
	got := b.Recent(0)
	if len(got) != 1 || got[0].Source != SourceBackend {
		t.Fatalf("Add 应标记 source=backend，实际 %+v", got)
	}
}

// 前端上报：级别由调用方显式给出，不能靠关键词去猜
func TestAddFromKeepsSourceAndLevel(t *testing.T) {
	b := New(10)
	b.AddFrom(SourceFrontend, "warn", "歌单链接无效")
	got := b.Recent(0)
	if len(got) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(got))
	}
	if got[0].Level != "warn" || got[0].Source != SourceFrontend {
		t.Errorf("level/source 不符：%+v", got[0])
	}
	// 文案里没有 "failed"/"error" 之类关键词，靠 levelOf 猜只会得到 info —— 这正是 AddFrom 存在的理由
	if levelOf("歌单链接无效") != "info" {
		t.Skip("levelOf 行为变了，本测试的前提需重新确认")
	}
}

func TestAddFromNormalizesBadLevel(t *testing.T) {
	b := New(10)
	for _, lv := range []string{"", "debug", "ERROR", "fatal"} {
		b.AddFrom(SourceFrontend, lv, "x")
	}
	for _, e := range b.Recent(0) {
		if e.Level != "info" {
			t.Errorf("非 info/warn/error 的级别应归一为 info，实际 %q", e.Level)
		}
	}
}

func TestAddFromDefaultsSource(t *testing.T) {
	b := New(10)
	b.AddFrom("", "error", "x")
	if got := b.Recent(0); got[0].Source != SourceFrontend {
		t.Errorf("source 为空时应默认 frontend，实际 %q", got[0].Source)
	}
}

// 空消息不该占用缓冲区（前端有时会带着 undefined 上报）
func TestAddFromRejectsEmptyMessage(t *testing.T) {
	b := New(10)
	b.AddFrom(SourceFrontend, "error", "   ")
	if b.Len() != 0 {
		t.Errorf("空白消息应被丢弃，实际 %d 条", b.Len())
	}
}

// 前端上报的是原始错误，可能带整段堆栈 —— 不设上限会让缓冲区被一条撑爆
func TestAddFromTruncatesLongMessage(t *testing.T) {
	b := New(10)
	long := strings.Repeat("中", maxMessageRunes+500) // 多字节字符
	b.AddFrom(SourceFrontend, "error", long)

	got := b.Recent(0)
	if len(got) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(got))
	}
	r := []rune(got[0].Message)
	if len(r) != maxMessageRunes+1 { // 2000 个字符 + 省略号
		t.Errorf("应截断到 %d 字符（含省略号），实际 %d", maxMessageRunes+1, len(r))
	}
	if r[len(r)-1] != '…' {
		t.Errorf("截断后应以省略号结尾，实际 %q", r[len(r)-1])
	}
	if !utf8.ValidString(got[0].Message) {
		t.Error("按字符截断后必须仍是合法 UTF-8（不能把多字节字符切一半）")
	}
}
