package online

import "strings"

// 本文件构造**下发给官方客户端的 VO**（value object）。
//
// 每个构造器都对齐 fnmusic-ext 里同名函数的输出（`build_online_track`、
// `build_favorite_track_obj`、`build_lyric_list_payload`、`build_metadata_payload`），
// 因为那些形状是**从官方前端的行为反推出来的**，不是猜的：
//
//   - `track.metadata` 的响应里 `data.track.genres` 必须是**数组**、
//     `data.track.album` 必须是**对象**。官方前端的 `_h()` 无防护地读
//     `data.track.genres.join(...)` 与 `data.track.album.*`，缺一个就抛错、
//     播放器直接跳过、**连 stream 请求都不会发**。
//   - 收藏/歌单/历史的条目里 `artists` 必须非空、`album.name` 必须非空，
//     否则 App 端会把整个列表丢掉。
//   - `duration` 系列统一是**毫秒**（官方内部就是毫秒）。
//   - `coverId` 是**裸的 32 位 hex**，不带 `track_` 前缀。fnmusic-ext 的
//     `disguise_client_json` 只在值本身以 `online:` 开头时才加前缀 —— 而
//     这里构造的 VO 从一开始就只含虚拟 id，永远不触发那条规则。
//
// ⚠️ **Go 的零值陷阱**：`var s []string` 序列化成 `null` 而不是 `[]`。
// 所有数组字段一律显式给 `[]any{}` / 非空切片，绝不能留 nil。

// Playback 描述「这条曲目实际以什么形式播放」。
//
// 搜索阶段还没解析直链，所以是零值 —— 那时 size=0、format 取默认 mp3。
// 解析出直链后可以带上真实的大小与格式，让客户端的进度条与规格显示正确。
type Playback struct {
	Format  string // mp3 / flac / m4a / aac / wav / ogg
	Size    int64  // 字节；未知为 0
	Bitrate int    // bps；0 表示按格式给默认值
}

// DurationMS 返回毫秒时长（官方 VO 里所有 duration 字段都是毫秒）。
func (t Track) DurationMS() int { return t.Duration * 1000 }

// Format 返回播放格式，未知时按 mp3 处理。
func (p Playback) format() string {
	f := strings.ToLower(strings.TrimSpace(p.Format))
	f = strings.TrimPrefix(f, ".")
	if f == "" {
		return "mp3"
	}
	return f
}

// bitDepth 返回位深。官方只在无损容器上给这个字段，有损格式上**必须省略**
// （fnmusic-ext 用 `if v is not None` 过滤掉）。
func (p Playback) bitDepth() (int, bool) {
	switch p.format() {
	case "wav", "flac", "aiff":
		return 16, true
	}
	return 0, false
}

// bitrate 返回码率：无损容器给 1411000，其余给 320000。
func (p Playback) bitrate() int {
	if p.Bitrate > 0 {
		return p.Bitrate
	}
	switch p.format() {
	case "flac", "wav", "ape", "wv":
		return 1411000
	}
	return 320000
}

// audioSpec 构造官方前端的 audioSpec 对象。
func (t Track) audioSpec(pb Playback) map[string]any {
	format := pb.format()
	spec := map[string]any{
		"path":       "online/" + t.Platform + "/" + t.FakeID() + "." + format,
		"format":     format,
		"codec":      format,
		"container":  format,
		"duration":   t.DurationMS(),
		"size":       pb.Size,
		"channel":    2,
		"sampleRate": 44100,
		"bitrate":    pb.bitrate(),
	}
	if depth, ok := pb.bitDepth(); ok {
		spec["bitDepth"] = depth
	}
	return spec
}

// artistsArray 构造搜索 VO 的 artists 数组。
//
// 官方习惯：搜索 VO 里 artist 的 guid 是「曲目 guid + :artist」，**没有**
// coverId 字段（那是收藏 VO 的形态，见 FavoriteVO）。
func (t Track) artistsArray(fake string) []any {
	return []any{map[string]any{
		"name": t.Artist(),
		"guid": fake + ":" + SubKindArtist,
	}}
}

// albumObject 构造搜索 VO 的 album 对象。
func (t Track) albumObject(fake string) map[string]any {
	return map[string]any{
		"name":    t.AlbumName(),
		"guid":    fake + ":" + SubKindAlbum,
		"artists": t.artistsArray(fake),
		"coverId": fake,
	}
}

// SearchVO 构造搜索结果里的一条在线曲目。
//
// 对应 fnmusic-ext 的 `build_online_track`。
func SearchVO(t Track, pb Playback, isFavorite bool) map[string]any {
	t = t.Normalized()
	fake := t.FakeID()
	format := pb.format()
	artists := t.artistsArray(fake)

	return map[string]any{
		"guid":      fake,
		"id":        fake,
		"title":     t.Title,
		"name":      t.Title,
		"artist":    t.Artist(),
		"artists":   artists,
		"album":     t.albumObject(fake),
		"albumName": t.AlbumName(),
		"audioSpec": t.audioSpec(pb),

		"duration":    t.DurationMS(),
		"duration_ms": t.DurationMS(),
		"durationMs":  t.DurationMS(),
		"duration_s":  float64(t.Duration),

		"codec":     format,
		"codecName": format,
		"format":    format,
		"ext":       format,

		"size":      pb.Size,
		"file_size": pb.Size,

		"coverId":   fake,
		"cover_url": t.CoverURL,
		"coverUrl":  t.CoverURL,
		"coverURL":  t.CoverURL,

		"source":       t.Platform,
		"is_online":    true,
		"isFavorite":   isFavorite,
		"isCue":        false,
		"hasLyric":     false,
		"genres":       []any{}, // ⚠️ 必须是数组，不能是 nil
		"accessStatus": 0,
	}
}

// FavoriteVO 构造收藏 / 歌单 / 播放历史里的一条在线曲目。
//
// 对应 fnmusic-ext 的 `build_favorite_track_obj`。与 SearchVO 的差别是**结构性的**，
// 不是风格差异：官方这几个列表端点的条目形状与搜索端不同（artist 带 coverId /
// createdAt、album 带 releaseDate / barcode、顶层带 year / discNo / trackNo / isrc），
// 用搜索 VO 顶上去会让 App 端整列表解析失败。
//
// template 非空时以它为结构底版、再用我们的字段覆盖 —— 官方真实条目里有些
// 我们构造不出的字段（如 `barcode` 的真实值），保留模板能少踩一个坑。
//
// ⚠️ **isFavorite 必须由调用方算好传进来，不能写死 true。**
// 官方前端会从列表里反推收藏集合：`favoriteIds = list.filter(x => x.isFavorite).map(x => x.guid)`。
// 我们这三个列表（收藏 / 歌单附加曲目 / 播放历史）共用这一个构造器，写死 true 会让
// **歌单和历史里的每一首在线曲目都显示成已收藏** —— 真机上就是这么发现的。
// 收藏列表按定义全 true，歌单与历史要查 FavoriteGUIDs。
func FavoriteVO(t Track, pb Playback, createdAt int64, isFavorite bool, template map[string]any) map[string]any {
	t = t.Normalized()
	fake := t.FakeID()
	ts := createdAt

	artists := []any{map[string]any{
		"guid":      fake + ":" + SubKindArtist,
		"name":      t.Artist(),
		"coverId":   nil, // 官方习惯：artist 没有独立封面，**不能**填歌曲 guid
		"createdAt": ts,
		"updatedAt": ts,
	}}

	album := map[string]any{
		"guid":        fake + ":" + SubKindAlbum,
		"name":        t.AlbumName(),
		"coverId":     fake,
		"releaseDate": nil, // ⚠️ 官方 album 对象这里是 null，不是 0 也不是 ""
		"barcode":     nil,
		"createdAt":   ts,
		"updatedAt":   ts,
	}

	obj := map[string]any{
		"guid":         fake,
		"title":        t.Title,
		"duration":     t.DurationMS(),
		"source":       t.Platform,
		"is_online":    true, // 官方前端不读，但契约上在线曲目都带（对齐 fnmusic-ext）
		"isFavorite":   isFavorite,
		"isCue":        false,
		"genres":       []any{},
		"artists":      artists,
		"album":        album,
		"audioSpec":    t.audioSpec(pb),
		"accessStatus": 0,
		"coverId":      fake,
		"year":         nil,
		"discNo":       nil,
		"trackNo":      nil,
		"isrc":         nil,
		"createdAt":    ts,
		"updatedAt":    ts,
	}
	return mergeTemplate(template, obj)
}

// mergeTemplate 以 template 为底、obj 覆盖之。
//
// 对应 fnmusic-ext 的 `merged = deepcopy(template); merged.update(obj)`：
// 只做**顶层**合并（Python 的 `dict.update` 就是顶层）。不做深合并是有意的 ——
// 深合并会把模板里的 `album.artists` 之类带回来，反而偏离我们要的形状。
func mergeTemplate(template, obj map[string]any) map[string]any {
	if len(template) == 0 {
		return obj
	}
	merged := make(map[string]any, len(template)+len(obj))
	for k, v := range template {
		merged[k] = v
	}
	for k, v := range obj {
		merged[k] = v
	}
	return merged
}

// MetadataVO 构造 `track/metadata` / `track/audio-info` 的完整响应体。
//
// 对应 fnmusic-ext 的 `build_metadata_payload`。**这是最容易踩坑的一个**：
// 官方前端 `resolveTrackPlayback._h()` 无防护地读
// `data.track.genres.join(...)`、`data.track.album`、`data.track.artists`，
// 三者任一形状不对就抛错 —— 表现是「点播放没反应，且网络面板里没有 stream 请求」。
func MetadataVO(t Track, pb Playback, isFavorite, hasLyric bool) map[string]any {
	t = t.Normalized()
	fake := t.FakeID()
	artists := t.artistsArray(fake)
	album := t.albumObject(fake)
	spec := t.audioSpec(pb)
	format := pb.format()

	// 完整 VO（与搜索 VO 同形，额外带上 isFavorite / hasLyric 的真实值）。
	full := SearchVO(t, pb, isFavorite)
	full["hasLyric"] = hasLyric

	track := map[string]any{
		"guid":         fake,
		"id":           fake,
		"title":        t.Title,
		"artists":      artists,
		"album":        album,
		"genres":       []any{}, // ⚠️ _h() 会 .join() 它
		"duration":     t.DurationMS(),
		"coverId":      fake,
		"coverUrl":     t.CoverURL,
		"format":       format,
		"hasLyric":     hasLyric,
		"isFavorite":   isFavorite,
		"isCue":        false,
		"accessStatus": 0,
		"audioSpec":    spec,
	}

	full["guid"] = fake
	full["id"] = fake
	full["album"] = album
	full["audioSpec"] = spec
	full["track"] = track

	return map[string]any{"code": 0, "msg": "ok", "data": full}
}

// LyricListVO 构造 `lyric/list` 的响应体。
//
// 对应 fnmusic-ext 的 `build_lyric_list_payload`。`source: 2` 表示
// EXTERNAL_LRC（外挂歌词文件，不强制 offset）；歌词为空时返回空列表而**不是**
// 一条空 content —— 空 content 会让客户端渲染出一个空的歌词面板。
func LyricListVO(fakeID, lyricText string, now int64) map[string]any {
	text := strings.TrimSpace(lyricText)
	if text == "" {
		return map[string]any{"code": 0, "msg": "ok", "data": map[string]any{
			"list": []any{}, "preferred": "",
		}}
	}
	lyricGUID := fakeID + ":" + SubKindLyric
	return map[string]any{"code": 0, "msg": "ok", "data": map[string]any{
		"list": []any{map[string]any{
			"guid":      lyricGUID,
			"content":   text,
			"source":    2,
			"isLRC":     true,
			"offset":    0,
			"createdAt": now,
			"updatedAt": now,
		}},
		"preferred": lyricGUID,
	}}
}

// LyricTextVO 构造 `track/lyrics` / `detail/lyrics/…` 的响应体。
func LyricTextVO(fakeID, lyricText string) map[string]any {
	return map[string]any{"code": 0, "msg": "ok", "data": map[string]any{
		"guid":  fakeID,
		"lyric": lyricText,
	}}
}

// EmptyOK 构造「成功但无内容」的响应体。
func EmptyOK() map[string]any {
	return map[string]any{"code": 0, "msg": "ok", "data": map[string]any{}}
}
