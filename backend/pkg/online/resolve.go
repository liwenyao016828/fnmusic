package online

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Resolved 是一条解析出来的可播放直链。
type Resolved struct {
	URL     string
	Format  string // mp3 / flac / m4a / aac …
	Size    int64  // 字节；0 = 平台没给
	Bitrate int    // bps；0 = 平台没给
	Expires time.Time
}

// ErrNotPlayable 表示平台**明确**说这条曲目不可播（收费 / 无版权 / 地区限制）。
//
// 与「网络出错了」必须分开：前者是稳定结论、可以进负缓存；后者是临时故障、
// 不该被当成「这首歌不能听」。fnmusic-ext 在熔断器里也做了同样的区分
// （「解析干净但媒体不可用不算源故障」）。
var ErrNotPlayable = errors.New("曲目在该平台不可播放")

// ErrNoResolver 表示该平台没有注册解析器。
var ErrNoResolver = errors.New("该平台没有可用的直链解析器")

// Resolver 解析某个平台的一条曲目。
//
// 平台内 id 一律取曲率既有搜索层的 `UnifiedSong.Songmid` —— 那一层已经把
// 四家的原生 id 都归一到了这个字段（网易 song id、QQ songmid、酷狗 file hash、
// 酷我 rid）。
type Resolver interface {
	// Platform 返回平台代号（wy / tx / kg / kw）。
	Platform() string
	// Resolve 解析单条；平台明确不可播时返回 ErrNotPlayable。
	Resolve(ctx context.Context, platformID string) (*Resolved, error)
	// ResolveBatch 批量解析，只返回**能解析出**的那些。
	//
	// 批量是硬需求不是优化：搜索结果一页 20 条，逐条解析就是 20 次往返；
	// 而网易与 QQ 的接口都原生支持一次问多条。搜索时用它做「可播性过滤」，
	// 保证「搜得到就点得动」。
	ResolveBatch(ctx context.Context, platformIDs []string) (map[string]*Resolved, error)
}

// Query 是解析一条曲目所需的**最小上下文**。
//
// ID 是平台内 id（`UnifiedSong.Songmid`）；Title / Artist 只有需要「回搜」的
// 解析器才用得上，见 QueryResolver。
type Query struct {
	ID     string
	Title  string
	Artist string
}

// QueryResolver 是 Resolver 的**可选扩展**：解析时还需要歌名 / 歌手。
//
// # 为什么要有它
//
// musicdl 这类外挂解析器拿到的是**搜索结果对象上的直链** —— 只给一个平台内 id
// 它无法反查，只能(a)长期缓存搜索结果，或(b)拿关键词回搜一次。(a) 的代价是
// 进程一重启，所有已入库曲目全部解析失败；所以我们让 sidecar 走 (b)，而这需要
// 把歌名/歌手一起传下去。
//
// 原生解析器（wy / tx）按 id 就能直查，**不需要**实现这个接口 —— 于是「要不要
// 关键词」变成每个解析器自己的选择，而不是把关键词硬塞进所有人的签名里
// （那会改动两个既有实现与它们的全部用例）。
type QueryResolver interface {
	ResolveQueries(ctx context.Context, queries []Query) (map[string]*Resolved, error)
}

// 解析结果的缓存时长。
//
// 正面结果 5 分钟：直链本身有时效（网易的 URL 路径里带时间戳，约 20 分钟失效；
// QQ 的 vkey 更长），5 分钟是个安全水位 —— 缓存过期后重新解析一次即可。
//
// 负面结果也缓存：QQ 未登录时约三分之二的曲目返回受限，不缓存的话每次搜索
// 都要为这些曲目重新往返一轮。
const (
	resolveCacheTTL = 5 * time.Minute
	resolveNegTTL   = 5 * time.Minute
)

// Pool 是按平台分派的解析器集合，带结果缓存。
type Pool struct {
	mu        sync.RWMutex
	resolvers map[string]Resolver
	cache     map[string]cacheEntry
}

type cacheEntry struct {
	res    *Resolved // nil 表示「已确认不可播」
	expire time.Time
}

// NewPool 组装一个解析器池。同名平台后注册的覆盖先注册的。
func NewPool(resolvers ...Resolver) *Pool {
	p := &Pool{
		resolvers: make(map[string]Resolver, len(resolvers)),
		cache:     make(map[string]cacheEntry, 256),
	}
	for _, r := range resolvers {
		if r != nil {
			p.resolvers[r.Platform()] = r
		}
	}
	return p
}

// Register 追加（或替换）一个平台的解析器。
//
// 为什么需要它：外挂解析器（musicdl sidecar）覆盖哪些平台是**配置**决定的，
// 而配置能在运行时改 —— 只能在构造时一次性传进去的话，改平台就得重启进程。
func (p *Pool) Register(r Resolver) {
	if r == nil || r.Platform() == "" {
		return
	}
	p.mu.Lock()
	p.resolvers[r.Platform()] = r
	p.mu.Unlock()
}

// Unregister 摘掉某个平台的解析器（sidecar 关掉时用）。
//
// 只摘解析器，**不动缓存** —— 缓存里那几条结果仍然有效（直链 5 分钟就过期），
// 清掉反而会让「刚关掉 sidecar」的那一下把所有已缓存的解析结果丢掉。
func (p *Pool) Unregister(platform string) {
	p.mu.Lock()
	delete(p.resolvers, platform)
	p.mu.Unlock()
}

// Platforms 返回已注册的平台代号。
func (p *Pool) Platforms() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.resolvers))
	for k := range p.resolvers {
		out = append(out, k)
	}
	return out
}

// Supports 报告该平台是否有解析器。
func (p *Pool) Supports(platform string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.resolvers[platform]
	return ok
}

// Resolve 解析一条曲目，带缓存。
func (p *Pool) Resolve(ctx context.Context, platform, platformID string) (*Resolved, error) {
	if platformID == "" {
		return nil, fmt.Errorf("空的平台内 id")
	}
	key := platform + "\x00" + platformID
	if res, hit, miss := p.fromCache(key); hit {
		return res, miss
	}

	p.mu.RLock()
	r, ok := p.resolvers[platform]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrNoResolver
	}

	res, err := r.Resolve(ctx, platformID)
	p.store(key, res, err)
	return res, err
}

// ResolveMany 批量解析同一平台的一批曲目，只返回能解析出的那些。
//
// 命中缓存的直接拿；未命中的才打一次批量请求。这样重复搜索同一个关键词时
// 上游压力接近零。
func (p *Pool) ResolveMany(ctx context.Context, platform string, platformIDs []string) map[string]*Resolved {
	return p.resolveIDs(ctx, platform, platformIDs, nil)
}

// ResolveTracks 批量解析一批**完整曲目**（带歌名/歌手）。
//
// 与 ResolveMany 的唯一差别：它能把关键词一路传到实现了 QueryResolver 的解析器
// 手里（musicdl 那类需要回搜的）。没有 QueryResolver 时行为与 ResolveMany 完全一致。
//
// 为什么单独开一个方法而不是改 ResolveMany 的签名：既有调用点（搜索合并、歌单与
// 榜单的可播性过滤）手里大多只有 id，改签名会把它们全部卷进来 —— 而它们**不需要**
// 这个能力。
func (p *Pool) ResolveTracks(ctx context.Context, platform string, tracks []Track) map[string]*Resolved {
	ids := make([]string, 0, len(tracks))
	queries := make(map[string]Query, len(tracks))
	for _, t := range tracks {
		id := strings.TrimSpace(t.PlatformID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if _, seen := queries[id]; !seen {
			queries[id] = Query{ID: id, Title: t.Title, Artist: t.Artist()}
		}
	}
	return p.resolveIDs(ctx, platform, ids, queries)
}

// resolveIDs 是 ResolveMany / ResolveTracks 的公共实现。
//
// queries 非空且该平台的解析器实现了 QueryResolver 时走带关键词的那条路。
func (p *Pool) resolveIDs(ctx context.Context, platform string, platformIDs []string, queries map[string]Query) map[string]*Resolved {
	out := make(map[string]*Resolved, len(platformIDs))
	if len(platformIDs) == 0 {
		return out
	}

	pending := make([]string, 0, len(platformIDs))
	seen := make(map[string]bool, len(platformIDs))
	for _, id := range platformIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		key := platform + "\x00" + id
		if res, hit, _ := p.fromCache(key); hit {
			if res != nil {
				out[id] = res
			}
			continue
		}
		pending = append(pending, id)
	}
	if len(pending) == 0 {
		return out
	}

	p.mu.RLock()
	r, ok := p.resolvers[platform]
	p.mu.RUnlock()
	if !ok {
		return out
	}

	var got map[string]*Resolved
	var err error
	if qr, ok := r.(QueryResolver); ok && len(queries) > 0 {
		qs := make([]Query, 0, len(pending))
		for _, id := range pending {
			if q, ok := queries[id]; ok {
				qs = append(qs, q)
				continue
			}
			qs = append(qs, Query{ID: id})
		}
		got, err = qr.ResolveQueries(ctx, qs)
	} else {
		got, err = r.ResolveBatch(ctx, pending)
	}
	if err != nil && len(got) == 0 {
		// 整批失败：不写负缓存 —— 这可能只是网络抖动，不该让这批曲目在
		// 接下来的五分钟里都「不可播」。
		return out
	}
	for _, id := range pending {
		if res, ok := got[id]; ok {
			p.store(platform+"\x00"+id, res, nil)
			out[id] = res
			continue
		}
		// 批量接口没给这条 → 平台明确不可播（网易与 QQ 都是「能解析就一定有
		// 条目」，缺条目等价于不可播）。
		p.store(platform+"\x00"+id, nil, ErrNotPlayable)
	}
	return out
}

// fromCache 查缓存。hit=true 表示命中；miss 是当时记下的错误（可能是 nil）。
func (p *Pool) fromCache(key string) (res *Resolved, hit bool, miss error) {
	p.mu.RLock()
	e, ok := p.cache[key]
	p.mu.RUnlock()
	if !ok || time.Now().After(e.expire) {
		return nil, false, nil
	}
	if e.res == nil {
		return nil, true, ErrNotPlayable
	}
	return e.res, true, nil
}

// store 写缓存。只有「确定」的结论才进缓存：成功，或平台明确说不可播。
// 其它错误（网络、超时）不缓存，让下一次能重试。
func (p *Pool) store(key string, res *Resolved, err error) {
	switch {
	case err == nil && res != nil:
		p.mu.Lock()
		p.cache[key] = cacheEntry{res: res, expire: time.Now().Add(resolveCacheTTL)}
		p.mu.Unlock()
	case errors.Is(err, ErrNotPlayable):
		p.mu.Lock()
		p.cache[key] = cacheEntry{res: nil, expire: time.Now().Add(resolveNegTTL)}
		p.mu.Unlock()
	}
}

// ── 出站 HTTP ────────────────────────────────────────────────────────────

// DesktopUA 是出站请求用的 User-Agent。
//
// 四家平台对非浏览器 UA 的响应差别很大（有的直接拒绝，有的返回降质结果），
// 所以固定用一个桌面 Chrome 的 UA。
const DesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// sharedClient 是所有解析器共用的出站客户端。
//
// 走 http.ProxyFromEnvironment：部署环境如果需要经代理访问外网（比如家里
// 走旁路由），设好 http_proxy 就能生效，不需要额外配置。
var sharedClient = &http.Client{
	Timeout: 12 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   6 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// newRequest 构造一个带通用头的出站请求。
func newRequest(ctx context.Context, method, url string, referer string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DesktopUA)
	req.Header.Set("Accept", "*/*")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return req, nil
}

// SharedClient 暴露共用的出站客户端。
//
// 封面抓取之类的「非解析」出站请求复用它 —— 同一个连接池、同一套超时与
// 代理配置，不必再造一个。
func SharedClient() *http.Client { return sharedClient }

// NewRequest 是 newRequest 的导出形式（跨包复用同一套请求头）。
func NewRequest(ctx context.Context, method, url, referer string) (*http.Request, error) {
	return newRequest(ctx, method, url, referer)
}
