package online

import (
	"strings"
	"sync"
)

// 本文件是「在线专辑」的**纯专辑锚点**：只有平台内专辑 id，没有曲目。
//
// # 它补的是哪一段
//
// 曲目侧的专辑虚拟 id 是「某条曲目虚拟 id + `:album`」（见 vo.go 的 albumObject），
// 也就是说**专辑必须借一首歌才能被反解**。这在专辑搜索里成立（聚合出来的每张
// 专辑本来就有歌），但在两个地方不成立：
//
//  1. **歌手页的专辑列表**：平台的原生歌手专辑接口只给专辑（id / 名字 / 封面 /
//     曲目数），一首歌都不给 —— 没有歌就没有「曲目虚拟 id」可借；
//  2. **原生专辑接口拿到的专辑**：我们在搜索时只拿到了专辑 id，曲目还没取。
//
// 所以这里加一层**只由专辑 id 派生**的锚点（确定性哈希，和曲目同一套），
// 让「一张没有歌的专辑」也能下发 guid、也能被反解回「平台 + 专辑 id」。
//
// # 它与曲目锚点不冲突
//
// 两者的虚拟 id 由**不同的真实 id** 派生（`online:<p>:album:<id>` vs
// `online:<p>:<track id>`），哈希撞车只有理论上的可能；而反解时**先查专辑表、
// 再查曲目表**（见 valbum.go 的 albumAnchor），所以即便撞了也先按专辑语义应答。
// 曲目锚点的老 guid 依旧有效 —— 已经下发出去的 id 不能失效。
const (
	// AlbumRefLimit 是专辑锚点表的条数上限（FIFO 淘汰）。
	AlbumRefLimit = 2000
)

// AlbumRef 是一张「只有专辑身份」的在线专辑。
type AlbumRef struct {
	Platform string `json:"platform"`
	AlbumID  string `json:"album_id"` // 平台内专辑 id（网易 album id、QQ albummid）
	Name     string `json:"name"`
	Artist   string `json:"artist,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
	// TrackCount 是平台声明的曲目数（0 = 平台没给）。
	TrackCount int `json:"track_count,omitempty"`
	// Year 是发行年份（0 = 平台没给）。
	Year int `json:"year,omitempty"`
}

// RealAlbumID 返回专辑的真实 id，形态 `online:<平台>:album:<平台内 id>`。
//
// 中间那一段 `album` 是必需的：专辑 id 与曲目 id 在同一平台上可能是同一串数字，
// 少了它就会把一张专辑和一首歌算成同一个虚拟 id（见 RealArtistID 的同类说明）。
func RealAlbumID(platform, albumID string) string {
	return "online:" + platform + ":album:" + albumID
}

// ParseRealAlbumID 解析专辑真实 id；不是专辑 id 时 ok=false。
func ParseRealAlbumID(s string) (platform, albumID string, ok bool) {
	rest, found := strings.CutPrefix(s, "online:")
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] != SubKindAlbum || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// RealID 返回本专辑的真实 id。
func (a AlbumRef) RealID() string { return RealAlbumID(a.Platform, a.AlbumID) }

// FakeID 返回本专辑的虚拟 id。
func (a AlbumRef) FakeID() string { return FakeID(a.RealID()) }

// Normalized 返回补齐并校验后的副本；缺平台或专辑 id 时 ok=false。
func (a AlbumRef) Normalized() (AlbumRef, bool) {
	out := a
	out.Platform = strings.ToLower(strings.TrimSpace(out.Platform))
	out.AlbumID = strings.TrimSpace(out.AlbumID)
	out.Name = strings.TrimSpace(out.Name)
	out.Artist = strings.TrimSpace(out.Artist)
	out.CoverURL = strings.TrimSpace(out.CoverURL)
	if out.Platform == "" || out.AlbumID == "" {
		return AlbumRef{}, false
	}
	if out.Name == "" {
		out.Name = UnknownAlbum
	}
	if out.CoverURL != "" && strings.HasPrefix(out.CoverURL, "http://") {
		out.CoverURL = "https://" + strings.TrimPrefix(out.CoverURL, "http://")
	}
	return out, true
}

// AlbumRefRegistry 是专辑虚拟 id → 专辑锚点的内存表（并发安全，FIFO 淘汰）。
type AlbumRefRegistry struct {
	mu     sync.Mutex
	byFake map[string]AlbumRef
	order  []string
	limit  int
}

// NewAlbumRefRegistry 构造一张专辑锚点表；limit <= 0 时用 AlbumRefLimit。
func NewAlbumRefRegistry(limit int) *AlbumRefRegistry {
	if limit <= 0 {
		limit = AlbumRefLimit
	}
	return &AlbumRefRegistry{
		byFake: make(map[string]AlbumRef, 64),
		order:  make([]string, 0, 64),
		limit:  limit,
	}
}

// Put 登记一张专辑并返回虚拟 id；不合法时返回空串。
func (r *AlbumRefRegistry) Put(a AlbumRef) string {
	a, ok := a.Normalized()
	if !ok {
		return ""
	}
	fake := a.FakeID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byFake[fake]; exists {
		r.byFake[fake] = a
		return fake
	}
	for len(r.byFake) >= r.limit && len(r.order) > 0 {
		old := r.order[0]
		r.order = r.order[1:]
		delete(r.byFake, old)
	}
	r.byFake[fake] = a
	r.order = append(r.order, fake)
	return fake
}

// Lookup 按虚拟 id 反查专辑锚点。
func (r *AlbumRefRegistry) Lookup(fake string) (AlbumRef, bool) {
	fake = strings.TrimSpace(fake)
	if fake == "" {
		return AlbumRef{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byFake[fake]
	return a, ok
}

// Len 返回表里现有的条数（只用于日志与自检）。
func (r *AlbumRefRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byFake)
}
