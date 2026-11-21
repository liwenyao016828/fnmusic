package online

import (
	"strings"
	"sync"
)

// 本文件是「在线歌手」这一层的最小实现：歌手描述符、歌手登记表、以及
// 「曲目里的歌手名 → 可点进歌手页的虚拟 id」那条钩子。
//
// # 为什么要单独一张表
//
// 曲目的虚拟 id 派生自「平台 + 曲目 id」（见 track.go），而歌手是**另一个实体**：
// 一位歌手有自己的一套 id、自己的头像、自己的曲目与专辑列表。用曲目虚拟 id
// 加 `:artist`（今天的行为）只能表达「这首歌的歌手是谁」，点进去时我们无从知道
// 用户想看的是**哪一位**歌手 —— 所以那时只能原样透传给官方，而官方库里没有
// 在线歌手，结果就是「点了没反应」。
//
// 歌手表就是补这一段：拿到平台内歌手 id 时登记一条，下发一个**裸的 32 位 hex**
// 虚拟 id，客户端点回来我们能反解出「平台 + 歌手 id」，再去问平台要详情 / 曲目 /
// 专辑（见 pkg/intercept/vartist.go）。
//
// # 为什么不落盘
//
// 歌手虚拟 id 是**确定性**的（md5(salt + 真实 id)），同一张专辑、同一首歌每次算出来
// 都一样；而一张表里要存的只有「名字 / 头像 / 作品数」这类**展示用**元数据，
// 且它们每次都能从平台重新拿到。所以这张表是纯内存的、重启即空：
// 客户端手里那个 guid 反解不出来时，拦截层就把请求原样透传给官方（= 今天的行为），
// 不会出现「用一个过期身份去猜另一个歌手」。
const (
	// ArtistRegistryLimit 是歌手登记表的条数上限（FIFO 淘汰）。
	//
	// 比曲目表小得多（曲目表是 20000）：一位歌手会带出几十首曲目、几十张专辑，
	// 而一首歌只带出几位歌手 —— 2000 位歌手足够覆盖「一个人反复搜歌手」的量级，
	// 且每条只有几十字节。
	ArtistRegistryLimit = 2000
)

// Artist 是一位在线歌手的描述符。
//
// 字段全部来自平台的**原生歌手接口**（见 pkg/search/artist.go），不是从曲目里
// 推断的 —— 名字相同不代表同一位歌手，这条在音乐库里错得最离谱的就是它。
type Artist struct {
	Platform   string `json:"platform"`  // wy / tx / kg / kw
	ArtistID   string `json:"artist_id"` // 平台内 id（网易 artist id、QQ singermid）
	Name       string `json:"name"`      //
	CoverURL   string `json:"cover_url,omitempty"`
	AlbumCount int    `json:"album_count,omitempty"`
	TrackCount int    `json:"track_count,omitempty"`
}

// RealArtistID 返回歌手的真实 id，形态 `online:<平台>:artist:<平台内 id>`。
//
// ⚠️ 中间那一段 `artist` 是**必需的**：曲目的真实 id 是 `online:<平台>:<曲目 id>`，
// 少了这一段就可能与某首歌撞成同一个串（网易的 artist id 与 song id 都是纯数字，
// 撞不撞全看运气 —— 而这类 bug 只在特定歌手身上出现，极难复现）。
func RealArtistID(platform, artistID string) string {
	return "online:" + platform + ":artist:" + artistID
}

// ParseRealArtistID 解析歌手真实 id；不是歌手 id 时 ok=false。
func ParseRealArtistID(s string) (platform, artistID string, ok bool) {
	rest, found := strings.CutPrefix(s, "online:")
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] != SubKindArtist || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// RealID 返回本歌手的真实 id。
func (a Artist) RealID() string { return RealArtistID(a.Platform, a.ArtistID) }

// FakeID 返回本歌手的虚拟 id（裸 32 位 hex，与曲目同一套哈希）。
func (a Artist) FakeID() string { return FakeID(a.RealID()) }

// Normalized 返回补齐并校验后的副本；平台或平台内 id 缺失时 ok=false
// （那样的歌手**不能**登记：反解出来也问不到任何东西）。
func (a Artist) Normalized() (Artist, bool) {
	out := a
	out.Platform = strings.ToLower(strings.TrimSpace(out.Platform))
	out.ArtistID = strings.TrimSpace(out.ArtistID)
	out.Name = strings.TrimSpace(out.Name)
	out.CoverURL = strings.TrimSpace(out.CoverURL)
	if out.Platform == "" || out.ArtistID == "" {
		return Artist{}, false
	}
	if out.Name == "" {
		out.Name = UnknownArtist
	}
	if out.CoverURL != "" && strings.HasPrefix(out.CoverURL, "http://") {
		out.CoverURL = "https://" + strings.TrimPrefix(out.CoverURL, "http://")
	}
	return out, true
}

// ArtistRegistry 是歌手虚拟 id → 歌手描述符的内存表。
//
// 并发约定与 Registry 一致：Put 与 Lookup 都会被多个请求 goroutine 调用，
// 内部加锁；淘汰是 FIFO（不是 LRU）—— 这里不追求命中率，只要求**有界**。
type ArtistRegistry struct {
	mu     sync.Mutex
	byFake map[string]Artist
	order  []string
	limit  int
}

// NewArtistRegistry 构造一张歌手表；limit <= 0 时用 ArtistRegistryLimit。
func NewArtistRegistry(limit int) *ArtistRegistry {
	if limit <= 0 {
		limit = ArtistRegistryLimit
	}
	return &ArtistRegistry{
		byFake: make(map[string]Artist, 64),
		order:  make([]string, 0, 64),
		limit:  limit,
	}
}

// Put 登记一位歌手并返回它的虚拟 id；不合法（缺平台 / 缺平台内 id）时返回空串。
//
// 重复登记不动淘汰顺序（与 Registry.Put 同一套：反复给同一批曲目构造 VO 不该
// 把它们互相挤出去）。
func (r *ArtistRegistry) Put(a Artist) string {
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

// Lookup 按虚拟 id 反查歌手。虚拟 id 必须与 Put 时算出的一致（确定性哈希）。
func (r *ArtistRegistry) Lookup(fake string) (Artist, bool) {
	fake = strings.TrimSpace(fake)
	if fake == "" {
		return Artist{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byFake[fake]
	return a, ok
}

// Len 返回表里现有的条数（只用于日志与自检）。
func (r *ArtistRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byFake)
}

// ArtistNamer 把一位歌手登记进（进程级的）歌手表并返回客户端可见的虚拟 id。
//
// 由 pkg/intercept 在构造时注入（见 intercept.New 里的 SetArtistNamer），因为
// 曲目 VO 的构造在 online 包里、而歌手表的实例在拦截层里。为 nil 时 VO 回落成
// 「曲目虚拟 id + :artist」的**旧形态**（今天的线上行为，也是所有单测的默认）。
//
// ⚠️ 只允许在进程启动时写一次：它不是全局可变配置，而是「本进程的歌手表在哪」
// 这一点绑定。运行期改它会让同一个请求里的两个字段算出不同的 id。
var ArtistNamer func(Artist) string

// SetArtistNamer 注入歌手登记钩子（见 ArtistNamer）。
func SetArtistNamer(fn func(Artist) string) { ArtistNamer = fn }

// artistEntries 构造曲目 VO 里的 artists 数组。
//
// 有原生歌手 id 时**逐位**下发（网易的合作专辑、QQ 的合唱都是这种形态），每位
// 带自己的虚拟 id；否则回落成「曲目虚拟 id + :artist」一位（旧形态）。
func (t Track) artistEntries(fake string) []any {
	if fn := ArtistNamer; fn != nil && len(t.ArtistIDs) == len(t.Artists) && len(t.Artists) > 0 {
		out := make([]any, 0, len(t.Artists))
		for n, name := range t.Artists {
			guid := strings.TrimSpace(fn(Artist{Platform: t.Platform, ArtistID: t.ArtistIDs[n], Name: name}))
			if guid == "" {
				// 登记失败（缺 id）= 这一位不可点，回落旧形态。
				out = append(out, map[string]any{
					"name": name,
					"guid": fake + ":" + SubKindArtist,
				})
				continue
			}
			out = append(out, map[string]any{"name": name, "guid": guid})
		}
		return out
	}
	return []any{map[string]any{
		"name": t.Artist(),
		"guid": fake + ":" + SubKindArtist,
	}}
}

// favoriteArtists 构造收藏 / 歌单 / 历史 VO 里的 artists 数组。
//
// 与 artistEntries 的区别只有形状（列表条目多 coverId / createdAt / updatedAt
// 三个字段，见 FavoriteVO 的说明）；**身份**那部分完全共用，避免两条路各写一份
// 后漂成不同的虚拟 id。
//
// coverId 保持 nil：官方习惯是 artist 没有独立封面，填歌曲 guid 会让客户端
// 拿歌曲封面当头像用（真机上表现为「所有歌手长得都一样」）。
func (t Track) favoriteArtists(fake string, ts int64) []any {
	base := t.artistEntries(fake)
	out := make([]any, 0, len(base))
	for _, e := range base {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"guid":      m["guid"],
			"name":      m["name"],
			"coverId":   nil,
			"createdAt": ts,
			"updatedAt": ts,
		})
	}
	return out
}
