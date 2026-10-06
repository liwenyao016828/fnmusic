// Package library 提供音乐库的增量扫描索引与对账能力。
//
// 设计参考自 music-tidy 的三阶段扫描（枚举 → 比对 → 增量入库）与其
// 「枚举不完整就禁止删库」安全门，以及 music-monitor 的曲目去重键思路。
//
// 核心约定：
//   - 索引持久化在数据目录的 JSON 文件中（零第三方依赖，不引入 SQLite）；
//   - 变化判定基于 (size, mtime)，只有新增/变化的文件才需要读标签（昂贵操作）；
//   - 枚举不完整时（权限失败 / 深度截断 / 命中上限），**禁止**把未枚举到的
//     记录当作已删除，避免挂载抖动或临时权限问题导致索引被清空。
package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"

	"fn-lx-player/pkg/audioext"
)

// Entry 索引中的一条音频记录
type Entry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"` // Unix 秒
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Genre    string `json:"genre,omitempty"` // 风格：飞牛「风格」页读的内嵌字段
	Year     int    `json:"year,omitempty"`
	Track    int    `json:"track,omitempty"`
	Disc     int    `json:"disc,omitempty"`
	Format   string `json:"format,omitempty"`
	HasLyric bool   `json:"has_lyric,omitempty"`
	HasCover bool   `json:"has_cover,omitempty"`
	SeenAt   int64  `json:"seen_at"`

	// TagReadAt 上次成功读取该文件标签的时间（Unix 秒）。
	//
	// 它是「记录是否已富化」的**权威标记**。以前只看 Title/Artist 是否为空，
	// 于是加了年份/曲序/风格这些新字段之后，老库里「有标题但从未读过新字段」的记录
	// 永远不会被再读一遍，新字段就永远是 0 —— 看着像「全库都缺年份」，其实是没读。
	// 老数据这个字段是 0，会自动被当成待读，扫几轮就补齐了，不需要清索引重建。
	TagReadAt int64 `json:"tag_read_at,omitempty"`
}

// Audit 一次枚举的对账信息。
//
// Complete 是安全门：只有它为 true 时，调用方才允许把「索引里有、磁盘上没有」
// 的记录判定为已删除。
type Audit struct {
	Roots          []string `json:"roots"`
	FilesTotal     int      `json:"files_total"`     // 遍历到的文件总数（含非音频）
	AudioTotal     int      `json:"audio_total"`     // 音频文件数
	Inaccessible   int      `json:"inaccessible"`    // 读不了的子目录数
	DepthTruncated int      `json:"depth_truncated"` // 因深度上限被截断的目录数
	LimitHit       bool     `json:"limit_hit"`       // 是否命中数量上限而提前停止
	MissingRoots   []string `json:"missing_roots"`   // 不存在或不可访问的根目录
	Complete       bool     `json:"complete"`        // 以上都为干净状态时才为 true
	Warnings       []string `json:"warnings,omitempty"`
	ElapsedMs      int64    `json:"elapsed_ms"`
}

// Delta 一次扫描相对索引的变化
type Delta struct {
	Added     []Entry `json:"added"`
	Changed   []Entry `json:"changed"`
	Unchanged int     `json:"unchanged"`
	// Removed 仅在 Audit.Complete 为 true 时才会被填充
	Removed []string `json:"removed"`
	// RemovedSkipped 记录因枚举不完整而**跳过**的删除判定数量
	RemovedSkipped int `json:"removed_skipped"`
}

// Stats 索引概况
type Stats struct {
	Total     int      `json:"total"`
	Roots     []string `json:"roots"`
	UpdatedAt int64    `json:"updated_at"`
}

// Index 音乐库索引
type Index struct {
	mu        sync.RWMutex
	filePath  string
	entries   map[string]Entry
	updatedAt int64
}

// indexFile 持久化结构
type indexFile struct {
	Version   int              `json:"version"`
	UpdatedAt int64            `json:"updated_at"`
	Entries   map[string]Entry `json:"entries"`
}

const indexVersion = 1

// NewIndex 加载（或创建）索引。dataDir 为应用数据目录。
func NewIndex(dataDir string) *Index {
	idx := &Index{
		filePath: filepath.Join(dataDir, "library_index.json"),
		entries:  make(map[string]Entry),
	}
	idx.load()
	return idx
}

func (i *Index) load() {
	data, err := os.ReadFile(i.filePath)
	if err != nil {
		// 读不了 ≠ 空库。以前这里静默 return，用户只会看到「曲库一首歌都没有」，
		// 完全不知道是索引文件坏了或没权限（见 HANDOVER §11 ㊼ 块九）。
		// 文件不存在是正常首启，不记。
		if !os.IsNotExist(err) {
			applog.Default().AddFrom("library", "warn",
				fmt.Sprintf("[索引] 读取索引文件失败，本次从空库开始：%s（%v）", i.filePath, err))
		}
		return
	}
	var f indexFile
	if err := json.Unmarshal(data, &f); err != nil {
		// 索引损坏时从空开始重建，不影响主流程（下次扫描会重新填充）。
		// 但要把坏文件留档：否则下一次 Save 直接覆盖它，事后无法判断到底丢了什么。
		bad := i.filePath + ".bad-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(i.filePath, bad); rerr == nil {
			applog.Default().AddFrom("library", "warn",
				fmt.Sprintf("[索引] 索引文件损坏，已留档为 %s，本次从空库重建（下次扫描会重新填充）：%v", filepath.Base(bad), err))
		} else {
			applog.Default().AddFrom("library", "warn",
				fmt.Sprintf("[索引] 索引文件损坏，本次从空库重建（下次扫描会重新填充；留档失败：%v）：%v", rerr, err))
		}
		return
	}
	if f.Entries != nil {
		i.entries = f.Entries
	}
	i.updatedAt = f.UpdatedAt
}

// Save 原子写盘（临时文件 + rename），避免写入中断导致索引损坏
func (i *Index) Save() error {
	i.mu.RLock()
	snapshot := make(map[string]Entry, len(i.entries))
	for k, v := range i.entries {
		snapshot[k] = v
	}
	updatedAt := i.updatedAt
	i.mu.RUnlock()

	payload, err := json.Marshal(indexFile{
		Version:   indexVersion,
		UpdatedAt: updatedAt,
		Entries:   snapshot,
	})
	if err != nil {
		return err
	}

	dir := filepath.Dir(i.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".libindex-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(payload); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, i.filePath)
}

// Get 查一条记录的既有状态与 (size, mtime)
func (i *Index) Get(path string) (Entry, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.entries[path]
	return e, ok
}

// Len 索引条数
func (i *Index) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.entries)
}

// Stats 概况
func (i *Index) Stats(roots []string) Stats {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return Stats{Total: len(i.entries), Roots: roots, UpdatedAt: i.updatedAt}
}

// List 返回索引中的全部记录（按路径排序，便于稳定输出与测试）
func (i *Index) List() []Entry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]Entry, 0, len(i.entries))
	for _, e := range i.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// DirStats 某目录（或其子树）下的索引概况，供曲库管家展示上次整理结果。
type DirStats struct {
	Dir       string `json:"dir"`
	Total     int    `json:"total"`
	WithLyric int    `json:"with_lyric"`
	NoLyric   int    `json:"no_lyric"`
	WithCover int    `json:"with_cover"`
	NoCover   int    `json:"no_cover"`
	Richened  int    `json:"richened"`   // 已读取过标签（有标题信息）的条数
	ScannedAt int64  `json:"scanned_at"` // 该目录下最新的观察时间
}

// StatsForDir 统计 dir 子树内的索引记录。
//
// 注意：歌词/封面口径与 pkg/tidy.NeedsTidy 一致（内嵌 + 同名外挂文件）。
// 未富化过的记录（HasLyric/HasCover 均为 false 且无标签）计入 missing 统计，
// 需要触发一次「扫描并补全标签」才会落数据。
func (i *Index) StatsForDir(dir string) DirStats {
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	i.mu.RLock()
	defer i.mu.RUnlock()

	var st DirStats
	st.Dir = dir
	for p, e := range i.entries {
		if dirClean != "" && p != dirClean && !strings.HasPrefix(p, dirClean+"/") {
			continue
		}
		st.Total++
		if e.HasLyric {
			st.WithLyric++
		} else {
			st.NoLyric++
		}
		if e.HasCover {
			st.WithCover++
		} else {
			st.NoCover++
		}
		if e.Title != "" || e.Artist != "" {
			st.Richened++
		}
		if e.SeenAt > st.ScannedAt {
			st.ScannedAt = e.SeenAt
		}
	}
	return st
}

// ListDir 按目录前缀过滤 + 偏移分页，返回排序后的记录。
// 第二返回值为该目录下的总条数（不受分页影响）。
func (i *Index) ListDir(dir string, offset, limit int) ([]Entry, int) {
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	all := i.List()
	filtered := make([]Entry, 0, len(all))
	for _, e := range all {
		if dirClean != "" && e.Path != dirClean && !strings.HasPrefix(e.Path, dirClean+"/") {
			continue
		}
		filtered = append(filtered, e)
	}
	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		return []Entry{}, total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return filtered[offset:end], total
}

// Enrich 一次「读标签」的结果。用结构体而不是八个位置参数 ——
// 字段变多之后，位置参数的调用点最容易出错（顺序对了、含义错了编译器也不报）。
type Enrich struct {
	Title    string
	Artist   string
	Album    string
	Genre    string
	Year     int
	Track    int
	Disc     int
	HasLyric bool
	HasCover bool
}

// MarkEnriched 把读取到的标签元数据写回指定路径的记录（不改变 size/mtime）。
// 路径不存在于索引时忽略（说明已被删除或尚未扫描）。
func (i *Index) MarkEnriched(path string, enriched Enrich) {
	p := filepath.ToSlash(path)
	i.mu.Lock()
	defer i.mu.Unlock()
	e, ok := i.entries[p]
	if !ok {
		return
	}
	e.Title, e.Artist, e.Album, e.Genre = enriched.Title, enriched.Artist, enriched.Album, enriched.Genre
	e.Year, e.Track, e.Disc = enriched.Year, enriched.Track, enriched.Disc
	e.HasLyric, e.HasCover = enriched.HasLyric, enriched.HasCover
	e.TagReadAt = time.Now().Unix()
	i.entries[p] = e
}

// MarkTagRead 只记录「这个文件的标签读过了」，不动任何字段值。
//
// 用途是**读失败时也要打点**：否则一个永久读不出来的文件（权限、坏数据）
// 会被每一轮扫描重新排队，白烧一遍 IO。字段保持原样，所以不会误清空已有信息。
func (i *Index) MarkTagRead(path string) {
	p := filepath.ToSlash(path)
	i.mu.Lock()
	defer i.mu.Unlock()
	e, ok := i.entries[p]
	if !ok {
		return
	}
	e.TagReadAt = time.Now().Unix()
	i.entries[p] = e
}

// InvalidateTagRead 撤销「这个文件的标签读过了」这条记录，让它重新进入增量读取队列。
//
// 用于文件被换回旧版本（补全撤销）之后又读不回内容的情形：
// 索引里那些值是补全时写的，留着就永久和文件对不上。
func (i *Index) InvalidateTagRead(path string) {
	p := filepath.ToSlash(path)
	i.mu.Lock()
	defer i.mu.Unlock()
	e, ok := i.entries[p]
	if !ok {
		return
	}
	e.TagReadAt = 0
	i.entries[p] = e
}

// Fields 勾选要补全的字段。零值 = 一个都没勾（调用方自己决定怎么兜底）。
type Fields struct {
	Lyric, Cover, Genre, Year, Track, Disc bool
}

// Any 是否至少勾了一个字段
func (f Fields) Any() bool {
	return f.Lyric || f.Cover || f.Genre || f.Year || f.Track || f.Disc
}

// AllFields 全选（「歌词和封面一起补」的老口径就是它的前两个）
func AllFields() Fields {
	return Fields{Lyric: true, Cover: true, Genre: true, Year: true, Track: true, Disc: true}
}

// Missing 判断一条记录在勾选范围内是否还有待补字段。
//
// 口径是「**不确定就算缺**」：从未成功读过标签（TagReadAt 为 0）的记录，
// 所有勾选字段都算缺。宁可多排一次活（补全时会先匹配、匹配不到就跳过），
// 也不要因为索引没记就告诉用户「什么都不缺」。
func (f Fields) Missing(e Entry) bool {
	if e.TagReadAt == 0 {
		return f.Any()
	}
	if f.Lyric && !e.HasLyric {
		return true
	}
	if f.Cover && !e.HasCover {
		return true
	}
	if f.Genre && e.Genre == "" {
		return true
	}
	if f.Year && e.Year <= 0 {
		return true
	}
	if f.Track && e.Track <= 0 {
		return true
	}
	if f.Disc && e.Disc <= 0 {
		return true
	}
	return false
}

// GapCounts 一个目录里各字段的缺口统计（供「扫描缺失」展示）
type GapCounts struct {
	Total    int `json:"total"`
	Read     int `json:"read"` // 已成功读过标签的条数；Total-Read 的缺口只是「没读」不是「真缺」
	Lyric    int `json:"lyric"`
	Cover    int `json:"cover"`
	Genre    int `json:"genre"`
	Year     int `json:"year"`
	Track    int `json:"track"`
	Disc     int `json:"disc"`
	AnyField int `json:"any_field"` // 至少缺一个字段（补全会排进队的条数）

	// Unsupported = 格式写不了标签的条数（wav/opus/ape…），UnsupportedExts 按扩展名细分。
	//
	// 这些文件不是「缺字段」，是**根本写不进去**（写入器只有 MP3/FLAC，见 pkg/audioext）。
	// 必须单列：以前它们既算「待读」又算「待补」，于是 any_field 永远减不到 0，
	// 用户点多少次「补全」都看不到收敛，只会以为功能坏了 —— 数字说不清就别指望人信。
	Unsupported     int            `json:"unsupported"`
	UnsupportedExts map[string]int `json:"unsupported_exts,omitempty"`
}

// writableEntry 判断这条索引记录是不是「本应用能写标签」的格式。
//
// 优先用扫描时记下的 Format；老索引可能没有这个字段，退回按路径扩展名判断
// （不能反过来一刀切：Format 为空就当成不支持，会把老索引整库算成写不了）。
func writableEntry(e Entry) bool {
	if e.Format != "" {
		return audioext.CanWriteFormat(e.Format)
	}
	return audioext.CanWriteTags(e.Path)
}

// formatName 缺口统计里按格式归类用的显示名（"wav"/"opus"…），拿不到就写「未知」。
func formatName(e Entry) string {
	if f := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e.Format)), "."); f != "" {
		return f
	}
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(e.Path)), "."); ext != "" {
		return ext
	}
	return "未知"
}

// FieldGaps 统计该目录下各字段的缺失数量（只读索引，不开文件）
func (i *Index) FieldGaps(dir string) GapCounts {
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	var g GapCounts
	i.mu.RLock()
	for p, e := range i.entries {
		if dirClean != "" && p != dirClean && !strings.HasPrefix(p, dirClean+"/") {
			continue
		}
		g.Total++
		// 写不了标签的格式先摘出去：它们既不该算「待补」（排了也写不进去），
		// 也不该算「待读」（读多少遍都不会有字段）。
		if !writableEntry(e) {
			g.Unsupported++
			if g.UnsupportedExts == nil {
				g.UnsupportedExts = map[string]int{}
			}
			g.UnsupportedExts[formatName(e)]++
			continue
		}
		// AnyField 用与 NeedingFields 完全相同的判据（「不确定就算缺」）：
		// 它是「点开始会跑出几条」的答案。若按「只数确定的」来算，界面会显示
		// 「什么都不缺」而任务却真的排出队了 —— 自相矛盾，用户不敢再点。
		if AllFields().Missing(e) {
			g.AnyField++
		}
		if e.TagReadAt == 0 {
			// 还没读过标签：具体字段缺口无从判定，只算「待读」，不计进各字段
			continue
		}
		g.Read++
		if !e.HasLyric {
			g.Lyric++
		}
		if !e.HasCover {
			g.Cover++
		}
		if e.Genre == "" {
			g.Genre++
		}
		if e.Year <= 0 {
			g.Year++
		}
		if e.Track <= 0 {
			g.Track++
		}
		if e.Disc <= 0 {
			g.Disc++
		}
	}
	i.mu.RUnlock()
	return g
}

// NeedingFields 返回该目录下、在勾选字段上仍有缺失的记录，供补全挑活。
//
// 写不了标签的格式（非 MP3/FLAC）会被跳过：排进队也只会每轮失败一次，
// 而这些条数在 GapCounts.Unsupported 里单独报给用户。
func (i *Index) NeedingFields(dir string, want Fields, limit int) []Entry {
	if !want.Any() {
		return nil
	}
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	i.mu.RLock()
	out := make([]Entry, 0, 256)
	for p, e := range i.entries {
		if limit > 0 && len(out) >= limit {
			break
		}
		if dirClean != "" && p != dirClean && !strings.HasPrefix(p, dirClean+"/") {
			continue
		}
		if !writableEntry(e) {
			continue
		}
		if want.Missing(e) {
			out = append(out, e)
		}
	}
	i.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// Unenriched 返回该目录下尚未读取过标签的记录（新增/变化的文件），供增量读标签。
//
// 判据是 TagReadAt 是否为 0，**不再**用「标题和歌手都为空」：
// 后者会把「有标题但从没读过年份/曲序/风格」的老记录永久跳掉，
// 新增字段就永远补不上（看着像全库都缺，其实是没读）。
// 老索引记录 TagReadAt 都是 0，因此升级后会自动回读一遍，每轮 limit 条，无需清索引。
func (i *Index) Unenriched(dir string, limit int) []Entry {
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	i.mu.RLock()
	out := make([]Entry, 0, 256)
	for p, e := range i.entries {
		if limit > 0 && len(out) >= limit {
			break
		}
		if dirClean != "" && p != dirClean && !strings.HasPrefix(p, dirClean+"/") {
			continue
		}
		if e.TagReadAt == 0 {
			out = append(out, e)
		}
	}
	i.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// NeedingTidy 返回该目录下**缺歌词或缺封面**的记录，供「整理补全」挑活。
//
// 与 Unenriched 的区别（别搞混）：Unenriched 找的是「标签还没读过」的记录
// （Title/Artist 都为空），用于增量读标签；这里找的是「标签读过了、但缺歌词/封面」，
// 用于补全。判据用的是索引里记的 HasLyric / HasCover —— 它们由上一次整理写入。
//
// 注意：索引是**快照**。若从未跑过整理，这两个标记都是 false，会返回全量 ——
// 调用方应先用 limit 限流，别一次把整库都排进去。
func (i *Index) NeedingTidy(dir string, limit int) []Entry {
	return i.NeedingFields(dir, Fields{Lyric: true, Cover: true}, limit)
}

// IsChanged 判断文件是否新增或已变化（变化判定口径：size 不同，或 mtime 相差超过 1 秒）
func (i *Index) IsChanged(path string, size, mtime int64) (added bool, changed bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	old, ok := i.entries[path]
	if !ok {
		return true, false
	}
	if old.Size != size || abs64(old.MTime-mtime) > 1 {
		return false, true
	}
	return false, false
}

// Upsert 写入/更新一条记录
func (i *Index) Upsert(e Entry) {
	if e.SeenAt == 0 {
		e.SeenAt = time.Now().Unix()
	}
	i.mu.Lock()
	i.entries[e.Path] = e
	i.updatedAt = e.SeenAt
	i.mu.Unlock()
}

// Touch 仅刷新已存在记录的观察时间，不改变其 size/mtime（避免掩盖真实变化）
func (i *Index) Touch(path string) {
	i.mu.Lock()
	if e, ok := i.entries[path]; ok {
		e.SeenAt = time.Now().Unix()
		i.entries[path] = e
	}
	i.mu.Unlock()
}

// Remove 删除一条记录
func (i *Index) Remove(path string) {
	i.mu.Lock()
	delete(i.entries, path)
	i.mu.Unlock()
}

// Prune 依据一次枚举结果计算增删。
//
// 安全门：只有 audit.Complete 为 true 时才真正删除记录；
// 否则跳过删除并累计 RemovedSkipped，供调用方告警。
func (i *Index) Prune(seen map[string]Entry, audit Audit) Delta {
	var delta Delta

	i.mu.RLock()
	old := make(map[string]Entry, len(i.entries))
	for k, v := range i.entries {
		old[k] = v
	}
	i.mu.RUnlock()

	// 新增 / 变化
	for path, cur := range seen {
		prev, ok := old[path]
		if !ok {
			delta.Added = append(delta.Added, cur)
			continue
		}
		if prev.Size != cur.Size || abs64(prev.MTime-cur.MTime) > 1 {
			delta.Changed = append(delta.Changed, cur)
		} else {
			delta.Unchanged++
		}
	}

	// 删除（受安全门保护）
	for path := range old {
		if _, ok := seen[path]; ok {
			continue
		}
		if !audit.Complete {
			delta.RemovedSkipped++
			continue
		}
		delta.Removed = append(delta.Removed, path)
	}

	sort.Slice(delta.Added, func(a, b int) bool { return delta.Added[a].Path < delta.Added[b].Path })
	sort.Slice(delta.Changed, func(a, b int) bool { return delta.Changed[a].Path < delta.Changed[b].Path })
	sort.Strings(delta.Removed)
	return delta
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
