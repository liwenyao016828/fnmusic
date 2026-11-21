package online

// 共享歌单条目：订阅 / 注入歌单里「曲库里没有」的那些曲目。
//
// # 为什么要有「共享」这一层
//
// 按用户分桶的在线条目（`playlist_tracks/<user>.json`）是**用户在界面上手动加进去**的，
// 天然属于某个用户。但「订阅歌单」是**服务端任务**写进去的：任务没有请求上下文，
// 拿不到凭据指纹，也就不知道该写进哪个用户桶 —— 硬写进某个用户的桶，多用户 NAS 上
// 就会串味（别人的歌单里冒出你的订阅歌）。
//
// 所以订阅歌单的在线条目走**共享桶**：只按歌单 guid 分区，不按用户分区。
// 歌单本身是飞牛音乐里的对象（全局唯一），所以共享桶不会把 A 用户的歌单内容
// 泄到 B 用户的另一个歌单里 —— 只有「同一个飞牛歌单」才会看到它，而那个歌单
// 本来就是大家共用的。
//
// # 文件名为什么带下划线
//
// `playlist_tracks/` 下的其它文件名是 `UserKey(user)`，可能是 `shared`（匿名桶）。
// 共享桶用 `_shared.json`：带下划线前缀，与 `shared.json` 不会撞名；
// `UserKey` 会把非字母数字字符换成 `_`，所以真实用户键不可能以 `_` 开头。

import (
	"path/filepath"
	"sort"
)

// sharedPlaylistFileName 是共享桶的文件名。
const sharedPlaylistFileName = "_shared.json"

// sharedPlaylistFile 是共享桶的磁盘格式。
type sharedPlaylistFile struct {
	// Items 按歌单 guid 分区的在线条目。
	Items map[string][]PlaylistItem `json:"items"`
	// Removed 记「用户手动从这个歌单里删掉过」的在线条目虚拟 id。
	//
	// 用途与官方曲目的「手动删除不再加回」一致（见 pkg/push 的 ManualRemovals）：
	// 用户在飞牛页面把一首在线歌删了，下一轮订阅不该又把它塞回来。
	// 按歌单 guid 分区 —— 删的是「这个歌单里的这一首」，不是「这首歌」。
	Removed map[string][]string `json:"removed,omitempty"`
}

// sharedPath 返回共享桶的文件路径。
func (s *Store) sharedPath() string {
	return filepath.Join(s.dir, "playlist_tracks", sharedPlaylistFileName)
}

// readShared 读共享桶。调用方必须已持锁。
func (s *Store) readShared() sharedPlaylistFile {
	var f sharedPlaylistFile
	_ = readJSON(s.sharedPath(), &f)
	if f.Items == nil {
		f.Items = make(map[string][]PlaylistItem)
	}
	return f
}

// AddSharedPlaylistTracks 往共享桶里追加在线条目（同 guid 幂等），返回**新增**条数。
//
// 已经在「用户手动删过」名单里的条目会被跳过：用户明确不要它了，推送不该再塞回来。
func (s *Store) AddSharedPlaylistTracks(playlistGUID string, items []PlaylistItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sharedPath()
	f := s.readShared()
	removed := removedSet(f.Removed[playlistGUID])

	bucket := f.Items[playlistGUID]
	existing := make(map[string]int, len(bucket))
	for i, it := range bucket {
		existing[it.GUID] = i
	}
	added := 0
	for _, it := range items {
		it.Track = it.Track.Normalized()
		if it.GUID == "" {
			continue
		}
		if removed[it.GUID] {
			continue
		}
		if i, ok := existing[it.GUID]; ok {
			bucket[i].Track = it.Track
			continue
		}
		existing[it.GUID] = len(bucket)
		bucket = append(bucket, it)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	f.Items[playlistGUID] = bucket
	return added, writeJSONAtomic(path, f)
}

// SharedPlaylistTracks 返回某个歌单下的共享在线条目（按加入时间正序）。
func (s *Store) SharedPlaylistTracks(playlistGUID string) []PlaylistItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.readShared()
	items := f.Items[playlistGUID]
	sort.SliceStable(items, func(i, j int) bool { return items[i].AddedAt < items[j].AddedAt })
	return items
}

// SharedPlaylistTrackCounts 返回每个歌单的共享在线条目数。
//
// 与 PlaylistTrackCounts 同用途（官方 trackCount 只算官方曲目，界面要显示总数），
// 但不按用户分桶 —— 共享桶本来就是全局的。
func (s *Store) SharedPlaylistTrackCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.readShared()
	out := make(map[string]int, len(f.Items))
	for guid, items := range f.Items {
		if len(items) > 0 {
			out[guid] = len(items)
		}
	}
	return out
}

// RemoveSharedPlaylistTracks 从共享桶里移除若干在线条目，返回移除条数。
//
// 移除的同时会记进「手动删过」名单：用户在飞牛页面删掉一首在线歌，下一轮订阅
// 不该又把它加回来（与官方曲目的删除语义一致）。
func (s *Store) RemoveSharedPlaylistTracks(playlistGUID string, guids []string) (int, error) {
	if len(guids) == 0 {
		return 0, nil
	}
	drop := make(map[string]bool, len(guids))
	for _, g := range guids {
		drop[g] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sharedPath()
	f := s.readShared()
	bucket := f.Items[playlistGUID]
	if len(bucket) == 0 {
		return 0, nil
	}
	out := make([]PlaylistItem, 0, len(bucket))
	removed := 0
	for _, it := range bucket {
		if drop[it.GUID] {
			removed++
			continue
		}
		out = append(out, it)
	}
	if removed == 0 {
		return 0, nil
	}
	if len(out) == 0 {
		delete(f.Items, playlistGUID)
	} else {
		f.Items[playlistGUID] = out
	}
	if f.Removed == nil {
		f.Removed = make(map[string][]string)
	}
	for g := range drop {
		f.Removed[playlistGUID] = append(f.Removed[playlistGUID], g)
	}
	return removed, writeJSONAtomic(path, f)
}

// MarkSharedPlaylistRemoved 记下「用户手动删过」的在线条目（不删条目本身）。
func (s *Store) MarkSharedPlaylistRemoved(playlistGUID string, guids []string) error {
	if len(guids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sharedPath()
	f := s.readShared()
	if f.Removed == nil {
		f.Removed = make(map[string][]string)
	}
	seen := removedSet(f.Removed[playlistGUID])
	for _, g := range guids {
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		f.Removed[playlistGUID] = append(f.Removed[playlistGUID], g)
	}
	return writeJSONAtomic(path, f)
}

// SharedPlaylistRemoved 返回某歌单里「用户手动删过」的在线条目集合。
func (s *Store) SharedPlaylistRemoved(playlistGUID string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.readShared()
	return removedSet(f.Removed[playlistGUID])
}

// DropSharedPlaylist 删除歌单时级联清掉它的共享桶。
func (s *Store) DropSharedPlaylist(playlistGUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sharedPath()
	f := s.readShared()
	if _, ok := f.Items[playlistGUID]; !ok {
		return nil
	}
	delete(f.Items, playlistGUID)
	delete(f.Removed, playlistGUID)
	return writeJSONAtomic(path, f)
}

// removedSet 把列表转成集合。
func removedSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		if s != "" {
			out[s] = true
		}
	}
	return out
}

// ── 全量重放 ──────────────────────────────────────────────────────────────

// sharedTracks 返回共享桶里的全部曲目描述符（启动时重放登记表用）。
//
// 调用方必须已持锁 —— 这里**不**加锁，避免 AllTracks 重入死锁。
func (s *Store) sharedTracks() []Track {
	f := s.readShared()
	var out []Track
	for _, bucket := range f.Items {
		for _, it := range bucket {
			out = append(out, it.Track)
		}
	}
	return out
}
