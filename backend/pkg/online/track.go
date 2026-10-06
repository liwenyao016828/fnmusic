// Package online 定义「在线曲目」这一领域概念：曲目描述符、虚拟 id 方案、
// 内存登记表、官方客户端 VO 构造，以及各平台的直链解析器。
//
// # 与 fnmusic-ext 的取舍差异
//
// fnmusic-ext 把在线曲目的真实 id 伪装成 32 位 hex 的「官方 guid」，然后靠一个
// 全局递归字符串重写（`disguise_client_json`）把响应里所有 `online:…` 换掉，
// 用正则 `online:[A-Za-z0-9_:\-]+` 猜哪些字符串是 id。那种做法的脆弱点是结构性的：
// 官方只要加一个带 id 的新字段，或改一下 id 的字符集，重写就会漏 —— 而且漏了
// 不报错，只是客户端拿到一个认不出的 id。
//
// 本包反过来做：**先给在线曲目分配虚拟 id，然后自己构造要下发的 JSON**。每个
// 字段的值由我们写进去，不扫描、不重写、不猜。代价是每个端点都要显式写一份
// VO 构造（见 vo.go），好处是官方改字段时我们只会「少显示一个字段」，不会
// 「把 id 漏出去」。
//
// # 虚拟 id 是确定性的
//
// 虚拟 id = `md5(salt + 真实 id)`，同一个真实 id 永远算出同一个虚拟 id。所以
// 进程重启后不需要恢复一张映射表：把持久化存储里的曲目描述符重放一遍
// （Registry.Warm），映射就完整重建了。
package online

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// Salt 是虚拟 id 的盐前缀。
//
// ⚠️ **只有这一处**。fnmusic-ext 把同一个公式抄了两份（`proxy/app.py:2101` 与
// `nmplaylists.py:83`），漏改一处就会对同一首歌算出两个不同的 id，表现为
// 「收藏了但封面显示不出来」这类难以定位的问题。
//
// 用 `qulv::` 而不是 `fnmusic-ext::` 是为了与那个扩展的虚拟 id 空间完全隔离：
// 两者理论上不会同时接管同一个 socket，但万一用户手工切换过，隔离能保证
// 我们不会把别人的虚拟 id 当成自己的。
const Salt = "qulv::"

// RealID 返回在线曲目的真实 id，形态 `online:<平台>:<平台内 id>`。
//
// 例如 `online:wy:2652820720`、`online:tx:0039MnYb0qxYhV`。
//
// **真实 id 永远不下发到客户端** —— 它只在本进程内用于登记表与直链解析。
// 下发的一律是 FakeID。
func RealID(platform, platformTrackID string) string {
	return "online:" + platform + ":" + platformTrackID
}

// ParseRealID 解析真实 id；不是在线 id 时 ok=false。
//
// 只切第一个冒号：平台内 id 理论上可能含冒号，剩下的整体当平台内 id。
func ParseRealID(s string) (platform, platformTrackID string, ok bool) {
	rest, found := strings.CutPrefix(s, "online:")
	if !found {
		return "", "", false
	}
	platform, platformTrackID, found = strings.Cut(rest, ":")
	if !found || platform == "" || platformTrackID == "" {
		return "", "", false
	}
	return platform, platformTrackID, true
}

// IsRealID 报告 s 是否是在线曲目的真实 id。
func IsRealID(s string) bool {
	_, _, ok := ParseRealID(s)
	return ok
}

// FakeID 把真实 id 映射成给官方客户端看的虚拟 id。
//
// 输出是 **32 位小写 hex**，这不是随便选的：官方 guid 就是这个形态
// （`/music/api/v1/sys/config` 返回的 `serverGUID` = `d88039ff564c4de9aef5ab2d46d3de25`），
// 客户端会拿它做长度与字符集假设。换成别的编码（base32、带前缀、UUID 带横线）
// 都会被客户端在某个环节拒掉或显示异常。
func FakeID(realID string) string { return hash(Salt + realID) }

// FakeSubID 为真实 id 的附属对象（专辑 / 艺术家 / 歌词）生成虚拟 id。
//
// 为什么用「真实 id + 类型后缀」再哈希，而不是「对附属对象自己的 id 再哈希」：
// 附属对象没有平台内 id，只能从曲目 id 派生。带类型后缀让不同附属类型不会
// 撞车（同一首歌的专辑 id 与歌词 id 不同），也让日志里一眼看出归属。
func FakeSubID(realID, kind string) string { return hash(Salt + realID + ":" + kind) }

// 附属对象的类型名。集中在这里，避免各处写字面量写错。
const (
	SubKindAlbum  = "album"
	SubKindArtist = "artist"
	SubKindLyric  = "lyric"
)

func hash(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// IsHex32 报告 s 是否是 32 位小写 hex。
//
// ⚠️ 这只是**形态**判断，**不能**用来判断「这是不是我们的虚拟 id」：官方 guid
// 也是 32 位小写 hex，两者在形态上不可区分。归属判断一律走 Registry.Lookup。
func IsHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

// Track 是一条在线曲目的完整描述符。
//
// 它同时承担两个职责，所以字段分成两组看：
//
//   - **解析直链的输入**：Platform + PlatformID + Quality
//   - **渲染 VO 的元数据**：Title / Artists / Album / Duration / CoverURL / Year
//
// ⚠️ **整条描述符会被持久化**（收藏、歌单附加条目、播放历史里各存一份），
// 所以 `json` 标签就是磁盘格式的一部分。改字段名等于改磁盘格式，需要迁移。
//
// 这也是「重启后能重建登记表」的前提：磁盘上有完整描述符，重放一遍就能
// 重新算出 FakeID。
type Track struct {
	Platform   string   `json:"platform"`    // wy / tx / kg / kw
	PlatformID string   `json:"platform_id"` // 平台内 id（网易 song id、QQ songmid…）
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album"`
	Duration   int      `json:"duration"` // 秒
	CoverURL   string   `json:"cover_url,omitempty"`
	Quality    string   `json:"quality,omitempty"`
	Year       int      `json:"year,omitempty"`
}

// RealID 返回本条曲目的真实 id。
func (t Track) RealID() string { return RealID(t.Platform, t.PlatformID) }

// FakeID 返回本条曲目的虚拟 id。
func (t Track) FakeID() string { return FakeID(t.RealID()) }

// UnknownAlbum / UnknownArtist 是描述符补齐时写进去的**占位名**（见 Normalized）。
//
// 导出它们是为了让调用方分得清「这个名字是真的」还是「兜底占位」：专辑聚合要按
// 专辑名归并（见 pkg/intercept/valbum.go），把「未知专辑」也当成一个专辑名去聚合
// 就成了一锅粥 —— 而它到底是不是占位，只有这里知道。
const (
	UnknownAlbum  = "未知专辑"
	UnknownArtist = "未知艺术家"
)

// Artist 返回艺术家拼接串。
//
// 官方 VO 的 `artist` 是单串（多艺术家用 `/` 连接），而 `artists` 是数组。
// 两者必须同时提供：前端有的地方读 `artist`，有的地方遍历 `artists`。
func (t Track) Artist() string {
	if len(t.Artists) == 0 {
		return UnknownArtist
	}
	return strings.Join(t.Artists, "/")
}

// AlbumName 返回专辑名，恒非空。
//
// 官方前端对空专辑名的处理不可靠（有分支直接读 `album.name.length`），所以
// 这里必须兜底 —— fnmusic-ext 也是这么做的（`build_favorite_track_obj` 里
// 兜底「未知专辑」）。
func (t Track) AlbumName() string {
	if strings.TrimSpace(t.Album) == "" {
		return UnknownAlbum
	}
	return t.Album
}

// Normalized 返回补齐空字段后的副本。
//
// 所有写入存储 / 登记表的路径都必须先过一遍它，保证磁盘上的描述符永远是
// 「可以直接渲染」的形态 —— 否则读回来时还得再判一次空。
func (t Track) Normalized() Track {
	out := t
	cleaned := make([]string, 0, len(out.Artists))
	for _, a := range out.Artists {
		if s := strings.TrimSpace(a); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		cleaned = []string{UnknownArtist}
	}
	out.Artists = cleaned

	if strings.TrimSpace(out.Album) == "" {
		out.Album = UnknownAlbum
	}
	if out.Duration < 0 {
		out.Duration = 0
	}
	out.Platform = strings.ToLower(strings.TrimSpace(out.Platform))
	out.PlatformID = strings.TrimSpace(out.PlatformID)
	out.Title = strings.TrimSpace(out.Title)
	if out.Title == "" {
		// 标题是客户端列表里唯一必显的字段。留空会出现一行什么都没有的条目，
		// 用户点不动也不知道点的是什么 —— 兜一个占位名比留空好。
		out.Title = "未知曲目"
	}
	return out
}

// PlatformName 返回平台的中文名，只用于日志与占位文案。
func PlatformName(platform string) string {
	if n, ok := platformNames[platform]; ok {
		return n
	}
	return platform
}

var platformNames = map[string]string{
	"wy": "网易云",
	"tx": "QQ音乐",
	"kg": "酷狗",
	"kw": "酷我",
}
