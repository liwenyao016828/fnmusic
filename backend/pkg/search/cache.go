package search

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── 元数据缓存：同一个查询不重复打上游 ──
//
// 改之前：`search` 包每次调用都真发请求 —— 同一首歌在「预览 → 执行」、
// 「第一轮没补齐 → 再跑一轮」、以及多个批量任务之间会反复搜同一个词；
// 只有 QQ 专辑详情有一层无 TTL 的进程内缓存（见 album_qq.go）。
//
// 参考实现 music-meta-web v1.4.8 的 `cache.py` 给了三条值得照搬的结论：
//  1. 缓存键必须是**真正发给上游的那个查询词**（否则「晴天」与「晴天 (Live)」串味）；
//  2. TTL 按数据稳定性分级（搜索 7 天 / 专辑详情 30 天 / 封面 1 天 …）；
//  3. 缓存**原始结果**，命中后按当前请求重新排序 —— 本实现里天然成立，
//     因为排序用的关键词本身就在缓存键里。
//
// 用标准库实现（map + 自增序号），不引入任何依赖。缓存放**进程内存**而非落盘：
// 进程重启即失效，代价只是重搜一次，换来的是没有「陈旧文件 / 并发写盘 /
// 清理策略」这些额外故障面。

// 缓存分级（TTL / 条数上限）
const (
	searchCacheTTL = 12 * time.Hour
	searchCacheMax = 512 // 一个 (关键词, 页, 条数) 一条，够覆盖几轮批量补全
	albumCacheTTL  = 7 * 24 * time.Hour
	albumCacheMax  = 512
	lyricCacheTTL  = 30 * 24 * time.Hour
	lyricCacheMax  = 512
)

// ttlCache 带 TTL 与 LRU 淘汰的进程内缓存（并发安全）。
//
// 不用 container/list：本包只用最基础的标准库，用「自增序号 + 满了扫一遍找最旧」
// 实现淘汰即可（条目上限只有几百，且只在超出时才扫）。
type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	items   map[string]*cacheEntry[V]
	seq     uint64
	hits    uint64
	misses  uint64
	evicted uint64
}

type cacheEntry[V any] struct {
	val     V
	expires time.Time // 零值 = 永不过期
	usedAt  uint64
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	if max <= 0 {
		max = 1
	}
	return &ttlCache[V]{ttl: ttl, max: max, items: make(map[string]*cacheEntry[V], max)}
}

// Get 取缓存；过期条目按未命中处理并顺手删掉。
func (c *ttlCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V

	e, ok := c.items[key]
	if !ok {
		c.misses++
		return zero, false
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		delete(c.items, key)
		c.misses++
		return zero, false
	}
	c.seq++
	e.usedAt = c.seq
	c.hits++
	return e.val, true
}

// Put 写缓存；超出上限时淘汰最久未使用的一条。
func (c *ttlCache[V]) Put(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expires := time.Time{}
	if c.ttl > 0 {
		expires = time.Now().Add(c.ttl)
	}

	c.seq++
	if e, ok := c.items[key]; ok {
		e.val, e.expires, e.usedAt = val, expires, c.seq
		return
	}
	if len(c.items) >= c.max {
		c.evictLocked()
	}
	c.items[key] = &cacheEntry[V]{val: val, expires: expires, usedAt: c.seq}
}

// evictLocked 满了才调用：先清掉已过期的，仍满则淘汰最久未使用的。
func (c *ttlCache[V]) evictLocked() {
	now := time.Now()
	oldestKey := ""
	var oldest uint64
	for k, e := range c.items {
		if !e.expires.IsZero() && now.After(e.expires) {
			delete(c.items, k)
			c.evicted++
			continue
		}
		if oldestKey == "" || e.usedAt < oldest {
			oldestKey, oldest = k, e.usedAt
		}
	}
	if oldestKey != "" && len(c.items) >= c.max {
		delete(c.items, oldestKey)
		c.evicted++
	}
}

// Len 当前条目数
func (c *ttlCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Purge 清空全部条目（计数保留）
func (c *ttlCache[V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*cacheEntry[V], c.max)
}

// Stats 返回 命中 / 未命中 / 淘汰 次数
func (c *ttlCache[V]) Stats() (hits, misses, evicted uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.evicted
}

// albumMetaCache QQ 专辑详情缓存（原实现在 album_qq.go，无 TTL、满了整体清空）
var albumMetaCache = newTTLCache[QQAlbumMeta](albumCacheTTL, albumCacheMax)

// searchCache 平台搜索结果缓存
var searchCache = newTTLCache[[]UnifiedSong](searchCacheTTL, searchCacheMax)

// cachedSearch 包一层缓存：同一个 (平台, 查询词, 页, 条数) 只真发一次请求。
//
// 只缓存**非空**结果：空结果既可能是「平台确实没有这首」，也可能是请求失败
// （QQ 那边把接口失败也返回空列表）。把一次网络抖动记成 12 小时的「搜不到」，
// 比多打一次上游糟得多。
func cachedSearch(platform, keyword string, page, limit int, fetch func() []UnifiedSong) []UnifiedSong {
	key := platform + "|" + keyword + "|" + strconv.Itoa(page) + "|" + strconv.Itoa(limit)
	if songs, ok := searchCache.Get(key); ok {
		return songs
	}
	// 失败计数先拍个快照：请求失败后调用方同样只看到空列表，但那不是
	// 「平台上没有这首歌」，别把两种信号写成同一条。见 failureCount 的注释。
	failuresBefore := failureCount(platform)
	songs := fetch()
	if len(songs) > 0 {
		searchCache.Put(key, songs)
	} else if failureCount(platform) == failuresBefore {
		// 空结果单独计数，不混进失败率：它可能是冷门关键词，也可能是平台
		// 开始「静默返回空」（QQ 那次的形态就是 200 + 空列表）。
		// 只记真实请求 —— 缓存命中的不算，否则重复搜同一个词会污染比例。
		noteEmpty(platform, keyword)
	}
	return songs
}

// ── 歌词缓存 ──

// lyricPair 歌词正文 + 翻译。两条必须一起缓存：翻译是跟着某一版正文走的，
// 单独命中正文却丢了翻译，等于把「中文歌词 + 无翻译」当成一个新结果。
type lyricPair struct {
	lrc  string
	tlrc string
}

// lyricCache 歌词缓存。
//
// TTL 给到 30 天：歌词是「存在了就不会变」的数据。取词链路有三层降级
// （网易云按 ID → 网易云按搜索 → 酷狗严格匹配），一首没词或没命中的歌
// 每轮补全都要把三层走一遍，是最容易打上游的那条路。
//
// 只缓存 lrc 非空的成对结果 —— 与搜索结果同一套道理：空结果既可能是
// 「这首歌确实没词」，也可能是某一层接口抖动。把一次抖动记成 30 天的
// 「无歌词」，比多打一次上游糟得多。
var lyricCache = newTTLCache[lyricPair](lyricCacheTTL, lyricCacheMax)

// cachedLyric 包一层歌词缓存：同一组入参只真取一次。
//
// 键包含**全部六个入参**。`fetchLyric` 有三层降级，输出由这六个共同决定：
// 网易云那条靠 (歌名, 歌手, 时长) 做候选校验，酷狗那条还要 hash。少放一个，
// 同一首歌的不同元数据版本就会串味（例如预览时用「晴天」，执行时用
// 「晴天 (Live)」—— 时长与 hash 都不同，本就该分开取）。
//
// 分隔符用 "\x00" 而不是 "|"：歌名与歌手是自由文本，含 "|" 的可能性不为零，
// 用可打印字符拼键会让 ("ab","c") 和 ("a","bc") 撞成同一个键。
//
// 这里不额外限速：出站请求统一走 doHTTP（见 throttle.go），缓存只是让
// 重复的取词在到达限速器之前就被挡掉。
func cachedLyric(source, songmid, title, singer string, durationSec int, hash string, fetch func() (string, string)) (string, string) {
	key := strings.Join([]string{source, songmid, title, singer, strconv.Itoa(durationSec), hash}, "\x00")
	if p, ok := lyricCache.Get(key); ok {
		return p.lrc, p.tlrc
	}
	lrc, tlrc := fetch()
	if lrc != "" {
		lyricCache.Put(key, lyricPair{lrc: lrc, tlrc: tlrc})
	}
	return lrc, tlrc
}

// ── 四个平台函数的缓存包装 ──
//
// 三条调用路径（`Search` / `HandleSearch` / `qualities.searchByPlatform`）都直接调
// `searchXxx`，所以缓存包在**这一层**，而不是包在 `Search` 上 —— 否则 HTTP 接口
// 那条路径完全不经过缓存。缓存键是传给平台的原始关键词与条数，与真正发出去的
// 请求一一对应。

func searchNetEase(keyword string, page, limit int) []UnifiedSong {
	return cachedSearch("wy", keyword, page, limit, func() []UnifiedSong {
		return searchNetEaseRaw(keyword, page, limit)
	})
}

func searchQQ(keyword string, page, limit int) []UnifiedSong {
	return cachedSearch("tx", keyword, page, limit, func() []UnifiedSong {
		return searchQQRaw(keyword, page, limit)
	})
}

func searchKuGou(keyword string, page, limit int) []UnifiedSong {
	return cachedSearch("kg", keyword, page, limit, func() []UnifiedSong {
		return searchKuGouRaw(keyword, page, limit)
	})
}

func searchKuWo(keyword string, page, limit int) []UnifiedSong {
	return cachedSearch("kw", keyword, page, limit, func() []UnifiedSong {
		return searchKuWoRaw(keyword, page, limit)
	})
}
