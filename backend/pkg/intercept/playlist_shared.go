package intercept

import (
	"time"

	"fn-lx-player/pkg/online"
)

// AddSharedPlaylistTracks 往共享桶里写订阅歌单的在线条目，返回写入条数。
//
// **谁在用它**：订阅 / 注入歌单的后台任务（pkg/push）。任务没有请求上下文，
// 拿不到凭据指纹，也就不知道该写进哪个用户桶 —— 所以走「共享桶」
// （见 pkg/online/store_shared.go 的文件头注释）。
//
// **写的是什么**：源歌单里那些「飞牛曲库里没有」的曲目，以及「曲库里有同一首歌
// 但不是同一版」的曲目 —— 用户要的是**源歌单那版**，所以那一版走在线条目，
// 而不是让曲库里那版顶替。
func (i *Interceptor) AddSharedPlaylistTracks(playlistGUID string, tracks []online.Track) (int, error) {
	if playlistGUID == "" || len(tracks) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	items := make([]online.PlaylistItem, 0, len(tracks))
	for _, t := range tracks {
		t = t.Normalized()
		fake := i.registry.Put(t)
		items = append(items, online.PlaylistItem{GUID: fake, AddedAt: now, Track: t})
	}
	return i.store.AddSharedPlaylistTracks(playlistGUID, items)
}
