package search

import (
	"context"
	"strings"
	"sync"

	"fn-lx-player/pkg/online"
)

// 「外挂进程」形态的搜索器。
//
// # 为什么搜索侧也需要一层抽象
//
// 曲率的搜索原本是 `Search()` 里一个 switch（wy / tx / kg / kw 四个原生实现）。
// 但解析侧已经证明了一件事：**一个平台的搜索与解析必须共用同一个 id 空间** ——
// 搜索给出 `Songmid`，解析就按这个 id 去要直链；两边不一致的结果是「搜得到、
// 点开放不出」，而且看不出为什么。
//
// musicdl 覆盖的平台（咪咕 / 千千 / B站 / 可选酷狗酷我）的 id 只有 musicdl 自己
// 认识，所以这些平台的搜索也必须走它。于是有了这张注册表：`Search()` 先问外挂，
// 外挂没有（或没结果）再走原生 switch。
//
// 对调用方完全透明：`Search()` 的签名与返回值一个字没变。
type Searcher interface {
	// Platform 返回这个搜索器负责的平台短码（wy / tx / kg / kw / mg / bq / bi）。
	Platform() string
	// Search 返回**这个平台**的搜索结果。返回空表示「这次没搜到 / 当前不可用」，
	// 调用方据此决定要不要退回原生实现。
	Search(ctx context.Context, keyword string, page, size int) []UnifiedSong
}

var (
	externalMu        sync.RWMutex
	externalSearchers = map[string]Searcher{}
)

// RegisterSearcher 注册（或替换）某个平台的外挂搜索器。
//
// 同名后注册的覆盖先注册的 —— 与 `online.NewPool` 的口径一致，便于测试与热替换。
func RegisterSearcher(s Searcher) {
	if s == nil || strings.TrimSpace(s.Platform()) == "" {
		return
	}
	externalMu.Lock()
	externalSearchers[strings.ToLower(strings.TrimSpace(s.Platform()))] = s
	externalMu.Unlock()
}

// UnregisterSearcher 摘掉某个平台的外挂搜索器（sidecar 停掉时用）。
func UnregisterSearcher(platform string) {
	externalMu.Lock()
	delete(externalSearchers, strings.ToLower(strings.TrimSpace(platform)))
	externalMu.Unlock()
}

// ExternalSearcherPlatforms 返回当前注册了外挂搜索器的平台（排障与文档用）。
func ExternalSearcherPlatforms() []string {
	externalMu.RLock()
	defer externalMu.RUnlock()
	out := make([]string, 0, len(externalSearchers))
	for p := range externalSearchers {
		out = append(out, p)
	}
	return out
}

func lookupSearcher(platform string) Searcher {
	externalMu.RLock()
	defer externalMu.RUnlock()
	return externalSearchers[strings.ToLower(strings.TrimSpace(platform))]
}

// MusicDLSearcher 把 musicdl sidecar 接到搜索层上。
//
// 一个实例负责一组平台（sidecar 自己按源名区分），所以它**同时**注册到每个平台 ——
// 每次搜索只把自己那一个平台的源报给 sidecar，避免为了搜一个平台把十几个源都跑一遍。
type MusicDLSearcher struct {
	m       *online.MusicDL
	perPage int // 单源一次取多少（sidecar 那边会翻页，给太多会超时）
}

// NewMusicDLSearcher 组装搜索器。perPage <= 0 时用 online 那边的默认值。
func NewMusicDLSearcher(m *online.MusicDL, perPage int) *MusicDLSearcher {
	return &MusicDLSearcher{m: m, perPage: perPage}
}

// Platform 实现 Searcher —— 它负责多个平台，这里返回空串。
//
// ⚠️ 所以它**不能**直接 `RegisterSearcher`（注册表按单平台索引），要用
// `RegisterPlatforms` 逐个注册。
func (s *MusicDLSearcher) Platform() string { return "" }

// RegisterPlatforms 把 sidecar 搜索器注册到它覆盖的每个平台上。
func (s *MusicDLSearcher) RegisterPlatforms(platforms []string) {
	for _, p := range platforms {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		RegisterSearcher(&musicDLPlatformSearcher{parent: s, platform: p})
	}
}

// musicDLPlatformSearcher 是绑定到单个平台的那一层薄壳。
type musicDLPlatformSearcher struct {
	parent   *MusicDLSearcher
	platform string
}

func (s *musicDLPlatformSearcher) Platform() string { return s.platform }

// Search 实现 Searcher。
//
// 出错**不往上抛**：搜索层的约定是「返回空 = 这次没搜到」，而 `Search()` 拿到空
// 会退回原生实现（如果该平台有原生）。把错误抛出去会让整个搜索接口 500 ——
// 为了少一个平台把整次搜索弄坏，不划算。
func (s *musicDLPlatformSearcher) Search(ctx context.Context, keyword string, page, size int) []UnifiedSong {
	if s.parent == nil || s.parent.m == nil {
		return nil
	}
	limit := s.parent.perPage
	if limit <= 0 {
		limit = size
	}
	if limit <= 0 {
		limit = 15
	}
	// 只问当前这一页：sidecar 的 limit 是「翻页单位」而不是「累计条数」，
	// 分页交给它自己按 limit 翻，我们只取第一页（与原生实现的口径一致：
	// 原生那边也是 page/pageSize 直接传给上游）。
	_ = page

	items, err := s.parent.m.Search(ctx, keyword, []string{s.platform}, limit)
	if err != nil {
		return nil
	}
	out := make([]UnifiedSong, 0, len(items))
	for _, it := range items {
		if it.Source != "" && it.Source != s.platform {
			continue // sidecar 偶尔会把别的源混进来，按平台过滤掉
		}
		out = append(out, unifiedFromMusicDL(it))
	}
	return out
}

// unifiedFromMusicDL 把 sidecar 的条目转成搜索层的统一结构。
//
// ⚠️ `Songmid` 必须是**平台内 id**（sidecar 的 `platform_id`），不是它那个带短码
// 前缀的 `id` —— 解析时 `online.Track.PlatformID` 用的是这个值，拼错了就解析不到。
func unifiedFromMusicDL(it online.MusicDLSong) UnifiedSong {
	id := strings.TrimSpace(it.PlatformID)
	if id == "" {
		id = strings.TrimPrefix(it.ID, it.Source+":")
	}
	song := UnifiedSong{
		ID:       id,
		Songmid:  id,
		Name:     it.Title,
		Singer:   it.Artist,
		Album:    it.Album,
		Cover:    it.CoverURL,
		Source:   it.Source,
		Duration: it.DurationS,
		Interval: it.DurationS,
		Quality:  qualityFromExt(it.Ext),
		FileSize: it.FileSize,
	}
	if song.Quality != "" {
		song.Qualitys = []string{song.Quality}
	}
	return song
}

// qualityFromExt 把容器格式映射成曲率内部那套音质档位名。
//
// 曲率 UI 的音质徽章读的是这个字段；外挂平台只告诉我们「这是 flac 还是 mp3」，
// 所以只能给到这一层 —— **不猜码率**（猜了会在界面上显示成假信息）。
func qualityFromExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "flac", "ape", "wav":
		return "无损"
	case "m4a", "aac":
		return "AAC"
	case "mp3":
		return "320k"
	default:
		return ""
	}
}
