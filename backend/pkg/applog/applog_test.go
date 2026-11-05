package applog

import (
	"strings"
	"testing"
	"time"
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
		// 中文正文（2026-11-01）：真失败要判红，统计行不能标红。
		{"[BACKEND-DL] 失败：一鱼 - 晨钟暮鼓（第 7 次尝试）：\"没有拿到任何可用直链\"", "error"},
		{"[MONITOR] 订阅 A（s1）本轮失败：上游 502", "error"},
		{"[DOWNLOAD] 失败：甲 - 乙（耗时 1s）：写入文件失败", "error"},
		{"[COMPLETE] 任务结束：共 10，匹配 9，未匹配 1，失败 0，耗时 3s", "info"},
		{"[MONITOR] 订阅 A 本轮结束：发现 5，新增 2，失败 0，耗时 3s", "info"},
		{"[NAS] 扫描告警：权限 000", "info"},
		// 动作名里带 failed 不算失败结论（2026-11-01 实测：这行曾判成 error）
		{"[BACKEND-DL] 操作 retry_failed（整队）：影响 10 条", "info"},
		{"[BACKEND-DL] 操作 retry_failed：影响 3 条", "info"},
		{"push failed: upstream 502", "error"},
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

// Entry.Time 必须是**带时区的绝对时刻**（RFC3339）。旧实现只有 at="15:04:05"，
// 前端直接显示 → NAS（UTC）上比用户本地慢 8 小时，且跨天完全不可读。
func TestAddSetsRFC3339Time(t *testing.T) {
	b := New(10)
	b.Add("服务端日志")
	e := b.Recent(0)[0]
	if e.Time == "" {
		t.Fatal("Entry.Time 不应为空")
	}
	parsed, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t.Fatalf("Entry.Time 应为 RFC3339，实际 %q：%v", e.Time, err)
	}
	if d := time.Since(parsed); d < -time.Minute || d > time.Minute {
		t.Errorf("Entry.Time 应接近当前时刻，偏差 %s", d)
	}
	// 同一时刻在 at 里只有时分秒，date 部分必须能从 Time 里还原出来。
	if !strings.HasPrefix(e.Time, parsed.Format("2006-01-02")) {
		t.Errorf("Time 应含日期，实际 %q", e.Time)
	}
}

func TestAddFromSetsRFC3339Time(t *testing.T) {
	b := New(10)
	b.AddFrom(SourceFrontend, "warn", "前端告警")
	e := b.Recent(0)[0]
	if _, err := time.Parse(time.RFC3339, e.Time); err != nil {
		t.Fatalf("AddFrom 的 Entry.Time 应为 RFC3339，实际 %q：%v", e.Time, err)
	}
}

// AddFromAt：浏览器带上的绝对时刻，偏差超过容差才被采用。
//
// 用户 2026-11-03 口径「本地 22 点日志显示 14 点」的另一半防线：
// 前端上报的 at 必须带 offset，且只有真正差得多（时钟/时区设错）才信它。
func TestAddFromAtUsesReportedTimeWhenSkewed(t *testing.T) {
	b := New(10)
	// 浏览器在 +08:00，NAS 是 UTC：同一个真实事件，浏览器算的绝对时刻与
	// 服务器「此刻」相差 8 小时（> 1 分钟容差）→ 采用浏览器时刻。
	at := time.Now().Add(8 * time.Hour).Format(time.RFC3339)
	b.AddFromAt(SourceFrontend, "warn", "时钟差 8 小时", at)
	e := b.Recent(0)[0]
	parsed, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t.Fatalf("Entry.Time 应为 RFC3339，实际 %q：%v", e.Time, err)
	}
	if d := parsed.Sub(time.Now().Add(8 * time.Hour)); d < -2*time.Minute || d > 2*time.Minute {
		t.Errorf("应采用浏览器上报的时刻（约 8 小时后），实际偏差 %s（entry=%s）", d, e.Time)
	}
}

func TestAddFromAtIgnoresSmallSkew(t *testing.T) {
	b := New(10)
	// 几秒的偏差（网络往返级别）不该换时钟 —— 时间轴要稳定
	at := time.Now().Add(5 * time.Second).Format(time.RFC3339)
	b.AddFromAt(SourceFrontend, "warn", "只差 5 秒", at)
	e := b.Recent(0)[0]
	parsed, _ := time.Parse(time.RFC3339, e.Time)
	if d := parsed.Sub(time.Now()); d < -2*time.Minute || d > 2*time.Minute {
		t.Errorf("小偏差应仍用服务器时钟，实际偏差 %s（entry=%s）", d, e.Time)
	}
}

func TestAddFromAtRejectsNaiveAndGarbage(t *testing.T) {
	// 不带 offset 的墙上钟点、垃圾串、空串 → 一律当没给（用服务器时钟，且不报错）
	for _, bad := range []string{"2026-11-03T22:00:00", "not-a-time", ""} {
		b := New(10)
		b.AddFromAt(SourceFrontend, "error", "坏的 at", bad)
		e := b.Recent(0)[0]
		parsed, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			t.Fatalf("at=%q：Entry.Time 仍应为 RFC3339，实际 %q：%v", bad, e.Time, err)
		}
		if d := parsed.Sub(time.Now()); d < -2*time.Minute || d > 2*time.Minute {
			t.Errorf("at=%q：应忽略坏值用服务器时钟，实际 %s", bad, e.Time)
		}
	}
}

// 老调用方（AddFrom 三参）必须照旧能用 —— api/nas/library 等十几处后端内部上报
// 不带浏览器时刻，走的都是这条兼容路径。
func TestAddFromStillWorks(t *testing.T) {
	b := New(10)
	b.AddFrom(SourceFrontend, "warn", "老路径")
	e := b.Recent(0)[0]
	if e.Source != SourceFrontend || e.Level != "warn" {
		t.Errorf("AddFrom 兼容路径字段不符：%+v", e)
	}
	if _, err := time.Parse(time.RFC3339, e.Time); err != nil {
		t.Errorf("AddFrom 也应产出 RFC3339 Time：%v", err)
	}
}
