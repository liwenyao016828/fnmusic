package search

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// 「额外来源」注册表：给**没有固定平台身份**的取音渠道留的一个口子。
//
// # 为什么不能复用 Searcher 注册表（provider.go）
//
// `Searcher` 是按**单平台**索引的（wy / tx / kg / kw / mg …），注册进去就等于宣称
// 「这个平台的搜索结果由我来出，搜索与解析共用同一个 id 空间」。洛雪(LX)脚本不满足
// 这个前提：
//
//   - 它的源 key（wy / tx / kg / mg）与原生平台代号**撞名**，注册进去会把原生搜索顶掉；
//   - 一次搜索结果里可能同时出现多个源的条目，标识是 `lx:<src>` 而不是平台代号，
//     解析也要走专门的 lx 解析器（见 intercept/lxresolve.go）；
//   - 它的结果**缺档位、缺体积**（实测，见 intercept/lxsource.go 顶部），
//     并进主搜索会改变现有排序与去噪的结果。
//
// 所以另开一张表，并且**默认不参与** —— 调用方显式 `?include=lx` 才并进来。
// 目前唯一的用户是「歌曲修复 / 换版本」的候选抽屉（用户 2026-11-24 口径：
// 「替换的音乐来源是什么？加入 lx 音源还有 musicdl」）。
//
// # musicdl 为什么不需要走这里
//
// musicdl 是**按平台**覆盖的（咪咕 mg / 千千 bq / B站 bi，可选酷狗酷我），搜索与
// 解析都按平台短码对齐，正是 `Searcher` + `online.MusicDLResolver` 的形状 ——
// 它早就并进 `/api/search` 了（sidecar 开着、源启用着就在）。洛雪没有平台身份，
// 才需要这条旁路。
type ExtraSource func(ctx context.Context, keyword string) []UnifiedSong

var (
	extraMu      sync.RWMutex
	extraSources = map[string]ExtraSource{}
)

// RegisterExtraSource 注册（或替换）一个额外来源。name 是 `?include=` 里的取值。
//
// 同名后注册的覆盖先注册的 —— 与 `RegisterSearcher` 同一个口径。
func RegisterExtraSource(name string, fn ExtraSource) {
	name = normalizeExtraName(name)
	if name == "" || fn == nil {
		return
	}
	extraMu.Lock()
	extraSources[name] = fn
	extraMu.Unlock()
}

// UnregisterExtraSource 摘掉一个额外来源（宿主关掉时用）。
func UnregisterExtraSource(name string) {
	name = normalizeExtraName(name)
	if name == "" {
		return
	}
	extraMu.Lock()
	delete(extraSources, name)
	extraMu.Unlock()
}

// ExtraSourceNames 返回已注册的额外来源名（排障与文档用）。
func ExtraSourceNames() []string {
	extraMu.RLock()
	defer extraMu.RUnlock()
	out := make([]string, 0, len(extraSources))
	for n := range extraSources {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// parseInclude 解析 `?include=lx,foo`。
//
// 容忍空格、大小写与重复值；认不出的名字**静默忽略**（少一个来源而已，
// 不该因为拼错一个参数就把整次搜索变成 400）。
func parseInclude(raw string) []string {
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		n := normalizeExtraName(part)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// extraSourcesFor 按名字取出一批额外来源（保持 names 的顺序）。
//
// 调用方拿到的是**函数快照**：真正执行时不持锁，避免慢来源把注册表锁住。
func extraSourcesFor(names []string) []ExtraSource {
	if len(names) == 0 {
		return nil
	}
	extraMu.RLock()
	defer extraMu.RUnlock()
	out := make([]ExtraSource, 0, len(names))
	for _, n := range names {
		if fn := extraSources[n]; fn != nil {
			out = append(out, fn)
		}
	}
	return out
}

func normalizeExtraName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
