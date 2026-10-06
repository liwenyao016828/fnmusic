package online

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 本文件是「按用户隔离的在线数据」持久化层。
//
// 三个独立的存储，形状对齐 fnmusic-ext（那套形状已经在真机上验证过 App 能读）：
//
//	favorites/<user>.json         {"items":[{"guid","createdAt","track"}]}
//	playlist_tracks/<user>.json   {"items":{"<歌单虚拟id>":[{"guid","addedAt","track"}]}}
//	play_history/<user>.json      {"items":[{"guid","playedAt","track"}]}
//
// 每条记录里都**内嵌完整的 Track 描述符**，这是有意的：它让「进程重启后重建
// 虚拟 id 登记表」变成「把磁盘上的 track 重放一遍」，不需要额外维护一张映射表，
// 也不会出现「表与数据不一致」这种只能靠对账发现的状态。
type Store struct {
	mu  sync.Mutex
	dir string
}

// 播放历史上限，对齐 fnmusic-ext（超出后丢最旧的）。
const historyLimit = 500

// FavoriteItem 是一条在线收藏。
type FavoriteItem struct {
	GUID      string `json:"guid"`
	CreatedAt int64  `json:"createdAt"`
	Track     Track  `json:"track"`
}

// PlaylistItem 是某个歌单里的一条在线附加曲目。
type PlaylistItem struct {
	GUID    string `json:"guid"`
	AddedAt int64  `json:"addedAt"`
	Track   Track  `json:"track"`
}

// HistoryItem 是一条在线播放历史。
type HistoryItem struct {
	GUID     string `json:"guid"`
	PlayedAt int64  `json:"playedAt"`
	Track    Track  `json:"track"`
}

type favoritesFile struct {
	Items []FavoriteItem `json:"items"`
}

type playlistFile struct {
	Items map[string][]PlaylistItem `json:"items"`
}

type historyFile struct {
	Items []HistoryItem `json:"items"`
}

// NewStore 建（或打开）一个存储根目录。
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("存储目录不能为空")
	}
	for _, sub := range []string{"", "favorites", "playlist_tracks", "play_history"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir}, nil
}

// Dir 返回存储根目录。
func (s *Store) Dir() string { return s.dir }

// ── 收藏 ─────────────────────────────────────────────────────────────────

// Favorites 返回某个用户的在线收藏（按收藏时间倒序，新的在前）。
func (s *Store) Favorites(user string) []FavoriteItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f favoritesFile
	_ = readJSON(s.path("favorites", user), &f)
	sort.SliceStable(f.Items, func(i, j int) bool { return f.Items[i].CreatedAt > f.Items[j].CreatedAt })
	return f.Items
}

// FavoriteGUIDs 返回某个用户全部在线收藏的虚拟 id 集合，供搜索/列表标注 isFavorite。
func (s *Store) FavoriteGUIDs(user string) map[string]bool {
	items := s.Favorites(user)
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it.GUID] = true
	}
	return out
}

// AddFavorite 幂等新增（同 guid 重复收藏只刷新描述符，不动收藏时间）。
func (s *Store) AddFavorite(user string, item FavoriteItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("favorites", user)
	var f favoritesFile
	_ = readJSON(path, &f)
	item.Track = item.Track.Normalized()
	for i := range f.Items {
		if f.Items[i].GUID == item.GUID {
			f.Items[i].Track = item.Track
			return writeJSONAtomic(path, f)
		}
	}
	f.Items = append(f.Items, item)
	return writeJSONAtomic(path, f)
}

// RemoveFavorite 删除一条收藏，返回是否真的删掉了。
func (s *Store) RemoveFavorite(user, guid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("favorites", user)
	var f favoritesFile
	_ = readJSON(path, &f)
	out := f.Items[:0]
	removed := false
	for _, it := range f.Items {
		if it.GUID == guid {
			removed = true
			continue
		}
		out = append(out, it)
	}
	if !removed {
		return false, nil
	}
	f.Items = out
	return true, writeJSONAtomic(path, f)
}

// ── 歌单附加条目 ──────────────────────────────────────────────────────────

// PlaylistTracks 返回某个歌单下的在线附加条目（按加入时间正序）。
func (s *Store) PlaylistTracks(user, playlistGUID string) []PlaylistItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f playlistFile
	_ = readJSON(s.path("playlist_tracks", user), &f)
	items := f.Items[playlistGUID]
	sort.SliceStable(items, func(i, j int) bool { return items[i].AddedAt < items[j].AddedAt })
	return items
}

// PlaylistTrackCounts 返回每个歌单的在线附加条目数。
//
// 用途：官方 `playlist/detail` 返回的 `trackCount` 只是官方曲目数，界面要显示
// 「官方 + 在线」的总数，所以得把在线条数加回去。
func (s *Store) PlaylistTrackCounts(user string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f playlistFile
	_ = readJSON(s.path("playlist_tracks", user), &f)
	out := make(map[string]int, len(f.Items))
	for guid, items := range f.Items {
		if len(items) > 0 {
			out[guid] = len(items)
		}
	}
	return out
}

// AddPlaylistTracks 往歌单追加在线条目（同 guid 幂等）。
func (s *Store) AddPlaylistTracks(user, playlistGUID string, items []PlaylistItem) error {
	if len(items) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("playlist_tracks", user)
	var f playlistFile
	_ = readJSON(path, &f)
	if f.Items == nil {
		f.Items = make(map[string][]PlaylistItem)
	}
	bucket := f.Items[playlistGUID]
	existing := make(map[string]int, len(bucket))
	for i, it := range bucket {
		existing[it.GUID] = i
	}
	for _, it := range items {
		it.Track = it.Track.Normalized()
		if i, ok := existing[it.GUID]; ok {
			bucket[i].Track = it.Track
			continue
		}
		existing[it.GUID] = len(bucket)
		bucket = append(bucket, it)
	}
	f.Items[playlistGUID] = bucket
	return writeJSONAtomic(path, f)
}

// RemovePlaylistTracks 从歌单移除若干在线条目，返回移除条数。
func (s *Store) RemovePlaylistTracks(user, playlistGUID string, guids []string) (int, error) {
	if len(guids) == 0 {
		return 0, nil
	}
	drop := make(map[string]bool, len(guids))
	for _, g := range guids {
		drop[g] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("playlist_tracks", user)
	var f playlistFile
	_ = readJSON(path, &f)
	bucket := f.Items[playlistGUID]
	if len(bucket) == 0 {
		return 0, nil
	}
	out := bucket[:0]
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
	return removed, writeJSONAtomic(path, f)
}

// DropPlaylist 删除歌单时级联清掉它的在线桶。
func (s *Store) DropPlaylist(user, playlistGUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("playlist_tracks", user)
	var f playlistFile
	_ = readJSON(path, &f)
	if _, ok := f.Items[playlistGUID]; !ok {
		return nil
	}
	delete(f.Items, playlistGUID)
	return writeJSONAtomic(path, f)
}

// ── 播放历史 ─────────────────────────────────────────────────────────────

// History 返回某个用户的在线播放历史（新的在前）。
func (s *Store) History(user string) []HistoryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f historyFile
	_ = readJSON(s.path("play_history", user), &f)
	sort.SliceStable(f.Items, func(i, j int) bool { return f.Items[i].PlayedAt > f.Items[j].PlayedAt })
	return f.Items
}

// RecordPlay 记一条播放历史。同一首重复播放只更新时间并提到最前，不重复占位。
func (s *Store) RecordPlay(user string, item HistoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("play_history", user)
	var f historyFile
	_ = readJSON(path, &f)
	item.Track = item.Track.Normalized()

	out := make([]HistoryItem, 0, len(f.Items)+1)
	out = append(out, item)
	for _, it := range f.Items {
		if it.GUID == item.GUID {
			continue
		}
		out = append(out, it)
	}
	if len(out) > historyLimit {
		out = out[:historyLimit]
	}
	f.Items = out
	return writeJSONAtomic(path, f)
}

// RemoveHistory 按虚拟 id 删历史，返回删除条数。
func (s *Store) RemoveHistory(user string, guids []string) (int, error) {
	if len(guids) == 0 {
		return 0, nil
	}
	drop := make(map[string]bool, len(guids))
	for _, g := range guids {
		drop[g] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("play_history", user)
	var f historyFile
	_ = readJSON(path, &f)
	out := f.Items[:0]
	removed := 0
	for _, it := range f.Items {
		if drop[it.GUID] {
			removed++
			continue
		}
		out = append(out, it)
	}
	if removed == 0 {
		return 0, nil
	}
	f.Items = out
	return removed, writeJSONAtomic(path, f)
}

// ── 全量重放（重建登记表） ────────────────────────────────────────────────

// AllTracks 读遍所有用户的三个存储，返回出现过的全部曲目描述符。
//
// 启动时用它 Warm 登记表：虚拟 id 是确定性哈希，所以重放一遍就恢复了
// 「虚拟 id → 曲目」的全部映射。
func (s *Store) AllTracks() []Track {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Track
	seen := make(map[string]bool, 256)
	add := func(t Track) {
		if t.Platform == "" || t.PlatformID == "" {
			return
		}
		id := t.RealID()
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, t)
	}

	for _, name := range listJSON(s.sub("favorites")) {
		var f favoritesFile
		if readJSON(name, &f) == nil {
			for _, it := range f.Items {
				add(it.Track)
			}
		}
	}
	for _, name := range listJSON(s.sub("playlist_tracks")) {
		var f playlistFile
		if readJSON(name, &f) == nil {
			for _, bucket := range f.Items {
				for _, it := range bucket {
					add(it.Track)
				}
			}
		}
	}
	for _, name := range listJSON(s.sub("play_history")) {
		var f historyFile
		if readJSON(name, &f) == nil {
			for _, it := range f.Items {
				add(it.Track)
			}
		}
	}
	return out
}

// ── 文件机制 ─────────────────────────────────────────────────────────────

func (s *Store) sub(kind string) string { return filepath.Join(s.dir, kind) }

func (s *Store) path(kind, user string) string {
	return filepath.Join(s.dir, kind, UserKey(user)+".json")
}

// UserKey 把任意用户标识压成安全的文件名。
//
// 调用方传进来的通常是官方 user guid（32 位 hex），但兜底值可能是
// `sha256(cookie+authorization+…)` 或字面量 `shared`。统一过滤一遍，
// 既防路径穿越（`../`），也保证不同来源的标识不会撞到同一个文件。
func UserKey(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return "shared"
	}
	var b strings.Builder
	b.Grow(len(user))
	for i := 0; i < len(user); i++ {
		c := user[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	key := b.String()
	// 全是下划线等价于空；再加一道长度上限，避免超长文件名。
	if strings.Trim(key, "_") == "" {
		return "shared"
	}
	if len(key) > 64 {
		key = key[:64]
	}
	return key
}

func listJSON(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// readJSON 读 JSON。文件不存在时返回 nil 且不改动 v（调用方拿到的就是零值）——
// 「没有这个文件」和「文件是空的」在语义上等价，不该让调用方分两路处理。
//
// 解析失败会返回错误（调用方多数选择忽略并当空处理）：宁可丢一次在线收藏，
// 也不要因为一个损坏的文件让整个列表接口 500。
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// writeJSONAtomic 原子写 JSON：先写同目录的 .part 再 rename。
//
// 直接 `os.WriteFile` 在断电/进程被杀时会留下半截文件；rename 在同一文件系统
// 内是原子的，读到的要么是旧内容要么是新内容，不会有中间态。
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
