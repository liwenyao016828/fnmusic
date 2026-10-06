package search

import (
	"testing"
	"time"
)

// TestTTLCacheExpiry 过期条目按未命中处理。
func TestTTLCacheExpiry(t *testing.T) {
	c := newTTLCache[string](30*time.Millisecond, 8)

	c.Put("k", "v")
	if got, ok := c.Get("k"); !ok || got != "v" {
		t.Fatalf("未过期就取不到：got=%q ok=%v", got, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("条目已过期却仍命中")
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("过期条目没被顺手删掉，Len=%d", n)
	}
}

// TestTTLCacheNeverExpiresZeroTTL TTL=0 表示永不过期。
func TestTTLCacheNeverExpiresZeroTTL(t *testing.T) {
	c := newTTLCache[int](0, 4)
	c.Put("k", 7)
	time.Sleep(20 * time.Millisecond)
	if got, ok := c.Get("k"); !ok || got != 7 {
		t.Fatalf("TTL=0 应永不过期，got=%d ok=%v", got, ok)
	}
}

// TestTTLCacheEvictsLeastRecentlyUsed 超上限时淘汰最久未使用的一条。
func TestTTLCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newTTLCache[string](0, 2)

	c.Put("a", "1")
	c.Put("b", "2")
	// 碰一次 a，让 b 成为最久未使用的。
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a 应命中")
	}

	c.Put("c", "3") // 触发淘汰，应踢掉 b

	if _, ok := c.Get("b"); ok {
		t.Fatal("b 是最久未使用的，应被淘汰")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a 刚用过，不该被淘汰")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c 刚写入，不该被淘汰")
	}
	if n := c.Len(); n != 2 {
		t.Fatalf("淘汰后条数应回到上限 2，实际 %d", n)
	}
}

// TestTTLCacheStatsAndPurge 计数与清空。
func TestTTLCacheStatsAndPurge(t *testing.T) {
	c := newTTLCache[string](0, 4)
	c.Put("a", "1")

	c.Get("a")    // hit
	c.Get("nope") // miss
	hits, misses, evicted := c.Stats()
	if hits != 1 || misses != 1 {
		t.Fatalf("计数不对：hits=%d misses=%d", hits, misses)
	}
	if evicted != 0 {
		t.Fatalf("没触发淘汰，evicted 应为 0，实际 %d", evicted)
	}

	c.Purge()
	if n := c.Len(); n != 0 {
		t.Fatalf("Purge 后应为空，实际 %d", n)
	}
}

// TestCachedSearchOnlyCachesNonEmpty 空结果不进缓存 —— 一次网络抖动不能变成
// 十几小时的「搜不到」。
func TestCachedSearchOnlyCachesNonEmpty(t *testing.T) {
	calls := 0

	empty := func() []UnifiedSong {
		calls++
		return nil
	}
	cachedSearch("t-empty", "晴天", 1, 10, empty)
	cachedSearch("t-empty", "晴天", 1, 10, empty)
	if calls != 2 {
		t.Fatalf("空结果被缓存了：只调了 %d 次，期望 2 次", calls)
	}

	hitCalls := 0
	full := func() []UnifiedSong {
		hitCalls++
		return []UnifiedSong{{Name: "晴天"}}
	}
	if got := cachedSearch("t-full", "晴天", 1, 10, full); len(got) != 1 {
		t.Fatalf("首次结果不对：%d 条", len(got))
	}
	if got := cachedSearch("t-full", "晴天", 1, 10, full); len(got) != 1 {
		t.Fatalf("命中缓存的结果不对：%d 条", len(got))
	}
	if hitCalls != 1 {
		t.Fatalf("非空结果没被缓存：调了 %d 次，期望 1 次", hitCalls)
	}
}

// TestCachedSearchKeyIncludesPagingAndLimit 页/条数不同不能串味。
func TestCachedSearchKeyIncludesPagingAndLimit(t *testing.T) {
	calls := 0
	fetch := func() []UnifiedSong {
		calls++
		return []UnifiedSong{{Name: "晴天"}}
	}
	cachedSearch("t-key", "晴天", 1, 10, fetch)
	cachedSearch("t-key", "晴天", 2, 10, fetch) // 换页 → 应真发
	cachedSearch("t-key", "晴天", 2, 20, fetch) // 换条数 → 应真发
	cachedSearch("t-key", "晴天", 2, 20, fetch) // 完全命中 → 不再发
	if calls != 3 {
		t.Fatalf("缓存键没区分页/条数：调了 %d 次，期望 3 次", calls)
	}
}

// TestCachedSearchKeyIncludesKeyword 关键词不同不能串味（「晴天」vs「晴天 (Live)」）。
func TestCachedSearchKeyIncludesKeyword(t *testing.T) {
	calls := 0
	fetch := func() []UnifiedSong {
		calls++
		return []UnifiedSong{{Name: "晴天"}}
	}
	cachedSearch("t-kw", "晴天", 1, 10, fetch)
	cachedSearch("t-kw", "晴天 (Live)", 1, 10, fetch)
	if calls != 2 {
		t.Fatalf("缓存键没区分关键词：调了 %d 次，期望 2 次", calls)
	}
}

// ── 歌词缓存 ──

// TestCachedLyricHitsOnIdenticalArgs 同一组入参只真取一次，正文与翻译一起命中。
func TestCachedLyricHitsOnIdenticalArgs(t *testing.T) {
	lyricCache.Purge()

	calls := 0
	fetch := func() (string, string) {
		calls++
		return "[00:01]晴天", "[00:01]Sunny Day"
	}

	lrc, tlrc := cachedLyric("tx", "song-1", "晴天", "周杰伦", 269, "hash-1", fetch)
	if lrc != "[00:01]晴天" || tlrc != "[00:01]Sunny Day" {
		t.Fatalf("首次取词不对：lrc=%q tlrc=%q", lrc, tlrc)
	}

	lrc2, tlrc2 := cachedLyric("tx", "song-1", "晴天", "周杰伦", 269, "hash-1", fetch)
	if lrc2 != lrc || tlrc2 != tlrc {
		t.Fatalf("命中缓存的结果与首次不一致：lrc=%q tlrc=%q", lrc2, tlrc2)
	}
	if calls != 1 {
		t.Fatalf("同一组入参应只取一次，实际 %d 次", calls)
	}
}

// TestCachedLyricDoesNotCacheEmpty 空结果不进缓存 —— 一次接口抖动不能变成
// 30 天的「这首歌没词」。
func TestCachedLyricDoesNotCacheEmpty(t *testing.T) {
	lyricCache.Purge()

	calls := 0
	empty := func() (string, string) {
		calls++
		return "", ""
	}
	cachedLyric("tx", "song-empty", "无词歌", "某某", 200, "", empty)
	cachedLyric("tx", "song-empty", "无词歌", "某某", 200, "", empty)
	if calls != 2 {
		t.Fatalf("空歌词被缓存了：只调了 %d 次，期望 2 次", calls)
	}
}

// TestCachedLyricKeySeparatesAllInputs 六个入参任一不同都要分开取 ——
// 取词链路有三层降级，输出由全部入参共同决定。
func TestCachedLyricKeySeparatesAllInputs(t *testing.T) {
	lyricCache.Purge()

	calls := 0
	fetch := func() (string, string) {
		calls++
		return "[00:01]x", ""
	}

	cachedLyric("tx", "m", "晴天", "周杰伦", 269, "h", fetch)        // 基准
	cachedLyric("kg", "m", "晴天", "周杰伦", 269, "h", fetch)        // 换平台
	cachedLyric("tx", "m2", "晴天", "周杰伦", 269, "h", fetch)       // 换 songmid
	cachedLyric("tx", "m", "晴天 (Live)", "周杰伦", 269, "h", fetch) // 换歌名
	cachedLyric("tx", "m", "晴天", "周杰伦/杨瑞代", 269, "h", fetch)    // 换歌手
	cachedLyric("tx", "m", "晴天", "周杰伦", 300, "h", fetch)        // 换时长
	cachedLyric("tx", "m", "晴天", "周杰伦", 269, "h2", fetch)       // 换 hash
	if calls != 7 {
		t.Fatalf("缓存键没覆盖全部入参：调了 %d 次，期望 7 次", calls)
	}

	// 自由文本字段相邻拼接不能撞键：("ab","c") 与 ("a","bc") 必须分开。
	cachedLyric("tx", "m", "ab", "c", 1, "", fetch)
	cachedLyric("tx", "m", "a", "bc", 1, "", fetch)
	if calls != 9 {
		t.Fatalf("自由文本字段拼接撞键：调了 %d 次，期望 9 次", calls)
	}
}
