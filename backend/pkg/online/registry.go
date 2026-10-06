package online

import "sync"

// Registry 维护「虚拟 id → 在线曲目」的映射。
//
// # 为什么纯内存、不落盘
//
// 虚拟 id 是真实 id 的确定性哈希，所以进程重启后只要把持久化存储里的曲目
// 描述符重放一遍（Warm）就能完全重建映射。这比维护一张落盘的映射表更省事，
// 也不会出现「映射表与存储不一致」这种只能靠对账发现的状态。
//
// 代价是「从未被任何存储引用过的曲目」重启后反解不出。但那种曲目同样没有任何
// 东西引用它 —— 客户端手上那条搜索结果早就随页面刷新没了 —— 所以不影响任何行为。
//
// # 并发
//
// fnmusic-ext 的对应结构（`_FAKE_GUID_REVERSE`）是一个模块级 dict，靠 CPython
// 的 GIL 保证并发安全。Go 没有 GIL，必须显式加锁。
type Registry struct {
	mu     sync.RWMutex
	byFake map[string]Track
	order  []string // 登记顺序，用于容量淘汰
	limit  int
}

// DefaultRegistryLimit 是登记表容量上限。
//
// 一条曲目描述符约 300 字节，两万条不到 10MB —— 对常驻服务可以忽略，但足够
// 覆盖「一整天滚动浏览 + 收藏 + 歌单」的全部曲目。上限存在的唯一理由是防止
// 长期运行下无限增长。
const DefaultRegistryLimit = 20000

// NewRegistry 建一个登记表；limit <= 0 时用 DefaultRegistryLimit。
func NewRegistry(limit int) *Registry {
	if limit <= 0 {
		limit = DefaultRegistryLimit
	}
	return &Registry{byFake: make(map[string]Track, 256), limit: limit}
}

// Put 登记一条曲目并返回它的虚拟 id。
//
// 重复登记（同一个真实 id 又来一次）只刷新描述符，**不动淘汰顺序** ——
// 搜索结果每次刷新都会重新登记一遍，如果这也算「新」，热门曲目会把整个
// 登记表挤成只有它自己。
func (r *Registry) Put(t Track) string {
	t = t.Normalized()
	fake := t.FakeID()

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byFake[fake]; exists {
		r.byFake[fake] = t
		return fake
	}
	r.byFake[fake] = t
	r.order = append(r.order, fake)
	r.evictLocked()
	return fake
}

// evictLocked 按登记顺序淘汰到容量以内。调用方必须已持写锁。
func (r *Registry) evictLocked() {
	// 一次可能淘汰多条（Warm 批量灌入时），所以用 for 而不是 if。
	for len(r.order) > r.limit {
		oldest := r.order[0]
		r.order[0] = "" // 断开引用，避免底层数组拖住已淘汰的字符串
		r.order = r.order[1:]
		delete(r.byFake, oldest)
	}
}

// Lookup 按虚拟 id 取回曲目。
func (r *Registry) Lookup(fakeID string) (Track, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byFake[fakeID]
	return t, ok
}

// Warm 批量重放描述符，用于启动时从持久化存储重建映射。
func (r *Registry) Warm(tracks []Track) {
	for _, t := range tracks {
		r.Put(t)
	}
}

// Len 返回当前登记条数。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byFake)
}
