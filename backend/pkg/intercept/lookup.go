package intercept

import (
	"io"
	"net/http"
	"strings"

	"fn-lx-player/pkg/online"
)

// 本文件集中处理「从请求里认出这是一条在线曲目」。
//
// 官方不同端点、不同版本对同一个 guid 用的参数名不一致，路径里也可能直接带
// guid。少认一个别名就会让某一类客户端失效，所以候选名一次列全。

// guidParamNames 是所有可能承载 guid 的查询参数名。
var guidParamNames = []string{
	"guid", "trackGUID", "trackGuid", "trackguid", "coverId", "cover_id", "id", "trackId",
}

// guidCandidates 收集请求里所有可能是 guid 的取值。
//
// subpath 是该端点路径之后多出来的部分（官方有一批 `/xxx/{subpath:path}` 形态的
// 路由），由各 handler 显式传入 —— 不在这里从 URL 猜，因为猜「哪一段是 guid」
// 只会把 `/track/stream` 里的 `stream` 也当成候选。
func guidCandidates(r *http.Request, subpath string) []string {
	q := r.URL.Query()
	out := make([]string, 0, len(guidParamNames)+2)
	for _, k := range guidParamNames {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			out = append(out, v)
		}
	}
	if subpath = strings.Trim(strings.TrimSpace(subpath), "/"); subpath != "" {
		out = append(out, subpath)
	}
	return out
}

// subPathAfter 返回 path 相对 prefix 之后的部分（去掉首尾斜杠）。
//
// 用于 `/music/api/v1/track/stream/{guid}` 这类「guid 在路径里」的形态。
func subPathAfter(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(path, prefix), "/")
}

// lookupTrack 把请求里的虚拟 id 解析成登记表里的在线曲目。
//
// 返回 ok=false 表示「这条请求与在线曲目无关」，调用方必须原样透传 ——
// 这是整个拦截层最重要的不变式：认不出来就交给官方，绝不猜。
func (i *Interceptor) lookupTrack(r *http.Request, subpath string) (online.Track, string, bool) {
	return i.lookupTrackAny(guidCandidates(r, subpath))
}

// lookupTrackAny 在一组候选 id 里找出第一条登记过的在线曲目。
func (i *Interceptor) lookupTrackAny(candidates []string) (online.Track, string, bool) {
	for _, candidate := range candidates {
		for _, form := range expandGUID(candidate) {
			if t, ok := i.registry.Lookup(form); ok {
				return t, form, true
			}
		}
	}
	return online.Track{}, "", false
}

// bodyGUIDCandidates 从请求体里收集可能是 guid 的取值。
//
// 官方不同端点对同一个字段用不同的名字（收藏是 `trackGUID`，歌单是
// `trackGUIDs` 数组，历史删除是 `guid` 或 `trackGUID`），一次列全。
func bodyGUIDCandidates(body map[string]any) []string {
	if body == nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, k := range guidParamNames {
		if v := strings.TrimSpace(asString(body[k])); v != "" {
			out = append(out, v)
		}
	}
	// `trackGUIDs` / `guids` 这类数组形态：只需要第一条能认出来的。
	for _, k := range []string{"trackGUIDs", "trackGuids", "guids", "tracks"} {
		for _, raw := range asSlice(body[k]) {
			if s := strings.TrimSpace(asString(raw)); s != "" {
				out = append(out, s)
				continue
			}
			// 也可能是对象数组：[{"guid": "…"}, …]
			if m := asMap(raw); m != nil {
				for _, nk := range guidParamNames {
					if s := strings.TrimSpace(asString(m[nk])); s != "" {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// bodyGUIDList 从请求体里取出全部 guid（用于歌单批量加/删曲）。
func bodyGUIDList(body map[string]any, keys ...string) []string {
	if body == nil {
		return nil
	}
	var out []string
	for _, k := range keys {
		for _, raw := range asSlice(body[k]) {
			if s := strings.TrimSpace(asString(raw)); s != "" {
				out = append(out, s)
				continue
			}
			if m := asMap(raw); m != nil {
				for _, nk := range guidParamNames {
					if s := strings.TrimSpace(asString(m[nk])); s != "" {
						out = append(out, s)
						break
					}
				}
			}
		}
	}
	return out
}

// expandGUID 展开一个候选 id 的等价形态。
//
// 虚拟 id 会以几种带后缀的形态出现：歌词是 `<guid>:lyric`，专辑是
// `<guid>:album`，艺术家是 `<guid>:artist`。三者都指向同一条曲目，所以先按
// 原样查，查不到再逐个剥掉后缀重试。
func expandGUID(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := []string{s}
	for _, suffix := range []string{
		":" + online.SubKindLyric,
		":" + online.SubKindAlbum,
		":" + online.SubKindArtist,
	} {
		if base, found := strings.CutSuffix(s, suffix); found && base != "" {
			out = append(out, base)
		}
	}
	return out
}

// readBody 读出请求体并解析成对象。
//
// 同时返回**原始字节**：handler 常常要先看 body 才能判断这条请求是不是关于
// 在线曲目的，而决定透传时原始 Body 已经被消费掉了，必须把字节喂回去。
// 上限 1MiB —— 官方这几个端点的 body 都是几十字节的小 JSON。
func readBody(r *http.Request) (raw []byte, obj map[string]any) {
	if r.Body == nil {
		return nil, nil
	}
	defer func() { _ = r.Body.Close() }()
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(buf) == 0 {
		return buf, nil
	}
	parsed, err := decodeObject(buf)
	if err != nil {
		return buf, nil
	}
	return buf, parsed
}
