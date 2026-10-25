package api

// 元数据快照（用户 2026-10-27：「数据快照是不是做错了，我要快照的是歌曲的元数据。
// 目的是为了，保留我的曲库中数据被误操作。」）
//
// # 为什么重做
//
// 上一版「数据快照」复制的是**整个音频文件**（见 library_snapshots.go 的旧实现），
// 目的是能整份回滚。但用户要的不是「文件级回滚」，而是「我的标签被误改了，
// 我要把元数据改回去」—— 那件事用不着动音频：
//
//   - 一个曲库动辄几十上百 GB，整份复制既慢又占盘，用户根本不敢开定时；
//   - 元数据只占几百字节，一库几万首也才几 MB，可以随便定时、随便留几十份；
//   - 真正的误操作就是「补全 / 歌名修复 / 整理」写错了标签，
//     要撤的是**标签**，音频本体从来没被改过。
//
// 所以快照 = 每首曲子的标签字段 + 文件大小/修改时间，存成一份 JSON。
// 恢复时只把标签写回去，并在写之前再留一份「恢复前快照」，恢复本身也能撤回。
//
// 文件级回滚仍然存在，但它是另一件事（下载/替换记录），不要混进来。
//
//	GET  /api/library/meta-snapshots              列表（?id=xxx 取明细）
//	POST /api/library/meta-snapshots              动作：{op:create|restore|delete}
//
// 落盘：<aiBackupRoot>/meta-snapshots/<id>/snapshot.json + meta.json
// 老的整文件快照（<aiBackupRoot>/<ts>/manifest.json）用 Kind 区分展示，互不影响。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/tidy"
)

const (
	metaSnapshotDir    = "meta-snapshots"
	metaSnapshotFile   = "snapshot.json"
	metaSnapshotMeta   = "meta.json"
	metaSnapshotMark   = "manual.json" // 手动快照标记（与老版一致，便于界面区分）
	metaSnapshotMaxAge = 90 * 24 * time.Hour
)

// metaTag 一首曲子的标签快照（只记**我们可能改动**的字段）
type metaTag struct {
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	MTime  int64  `json:"mtime,omitempty"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	Genre  string `json:"genre,omitempty"`
	Year   int    `json:"year,omitempty"`
	Track  int    `json:"track,omitempty"`
	Disc   int    `json:"disc,omitempty"`
	Format string `json:"format,omitempty"`
}

// metaSnapshot 一份元数据快照
type metaSnapshot struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Kind    string    `json:"kind"` // manual | scheduled | auto | pre-restore
	Dir     string    `json:"dir,omitempty"`
	Tracks  []metaTag `json:"tracks"`
	Count   int       `json:"count"`
	// Writable 其中本应用能写标签的曲目数（mp3/flac），恢复时只有这些能被写回
	Writable int    `json:"writable"`
	Note     string `json:"note,omitempty"`
}

// metaSnapshotRoot 快照根目录
func (s *Server) metaSnapshotRoot() string {
	return filepath.Join(s.aiBackupRoot, metaSnapshotDir)
}

// newMetaSnapshotID 时间戳 ID，天然按时间排序
func newMetaSnapshotID() string {
	return time.Now().Format("20060102-150405")
}

// snapshotOfLibrary 把曲库索引的当前状态转成快照内容。
//
// 用**索引**而不是重新读盘：索引里就是「本应用认为的元数据」，
// 而误操作恰恰来自本应用按这份认知去写文件。两者用同一份来源才对得上账。
func (s *Server) snapshotOfLibrary(dir string) metaSnapshot {
	entries := s.libraryIndex().List()
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	out := metaSnapshot{Tracks: make([]metaTag, 0, len(entries))}
	for _, e := range entries {
		if dirClean != "" && e.Path != dirClean && !strings.HasPrefix(filepath.ToSlash(e.Path), dirClean+"/") {
			continue
		}
		out.Tracks = append(out.Tracks, metaTag{
			Path: e.Path, Size: e.Size, MTime: e.MTime,
			Title: e.Title, Artist: e.Artist, Album: e.Album, Genre: e.Genre,
			Year: e.Year, Track: e.Track, Disc: e.Disc, Format: e.Format,
		})
		if audioext.CanWriteTags(e.Path) {
			out.Writable++
		}
	}
	out.Count = len(out.Tracks)
	out.Dir = dir
	return out
}

// createMetaSnapshot 落一份快照到磁盘
func (s *Server) createMetaSnapshot(kind, dir, note string) (metaSnapshot, error) {
	snap := s.snapshotOfLibrary(dir)
	if snap.Count == 0 {
		return snap, fmt.Errorf("曲库里还没有曲目 —— 先到「补全整理 → 补全」里点一次「扫描」")
	}
	snap.ID = newMetaSnapshotID()
	snap.Kind = kind
	snap.Note = note
	snap.Created = time.Now()
	base := filepath.Join(s.metaSnapshotRoot(), snap.ID)
	// 同一秒内连点两次：加后缀，别覆盖上一份
	for i := 1; ; i++ {
		if _, err := os.Stat(base); os.IsNotExist(err) {
			break
		}
		base = filepath.Join(s.metaSnapshotRoot(), fmt.Sprintf("%s-%d", snap.ID, i))
		snap.ID = fmt.Sprintf("%s-%d", snap.ID, i)
		if i > 20 {
			break
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return snap, err
	}
	if err := writeJSONFile(filepath.Join(base, metaSnapshotFile), snap); err != nil {
		return snap, err
	}
	if kind == "manual" {
		_ = os.WriteFile(filepath.Join(base, metaSnapshotMark), []byte("{}"), 0o644)
	}
	if err := writeJSONFile(filepath.Join(base, metaSnapshotMeta), map[string]interface{}{
		"id": snap.ID, "created": snap.Created, "kind": kind, "count": snap.Count,
		"writable": snap.Writable, "dir": dir, "note": note,
	}); err != nil {
		return snap, err
	}
	s.pruneMetaSnapshots()
	return snap, nil
}

// loadMetaSnapshot 读一份快照（含曲目明细）
func (s *Server) loadMetaSnapshot(id string) (metaSnapshot, error) {
	var snap metaSnapshot
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return snap, fmt.Errorf("快照 id 不合法")
	}
	b, err := os.ReadFile(filepath.Join(s.metaSnapshotRoot(), id, metaSnapshotFile))
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// listMetaSnapshots 列快照（不含曲目明细，只给界面用的概览）
func (s *Server) listMetaSnapshots() []metaSnapshot {
	root := s.metaSnapshotRoot()
	des, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]metaSnapshot, 0, len(des))
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		var snap metaSnapshot
		b, err := os.ReadFile(filepath.Join(root, de.Name(), metaSnapshotFile))
		if err != nil {
			continue
		}
		if json.Unmarshal(b, &snap) != nil {
			continue
		}
		snap.Tracks = nil // 列表不带明细，几万首会很沉
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// pruneMetaSnapshots 按保留策略清理老快照（默认留 90 天）
func (s *Server) pruneMetaSnapshots() {
	keepDays := s.metaSnapshotKeepDays()
	cutoff := time.Now().Add(-time.Duration(keepDays) * 24 * time.Hour)
	root := s.metaSnapshotRoot()
	des, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, de.Name()))
	}
}

// HandleMetaSnapshots GET/POST /api/library/meta-snapshots
func (s *Server) HandleMetaSnapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			snap, err := s.loadMetaSnapshot(id)
			if err != nil {
				errJSON(w, http.StatusNotFound, "找不到这份快照："+err.Error())
				return
			}
			// 明细里标出「和现在不一样」的曲目：用户点开一份快照最想知道的就是这个
			cur := make(map[string]metaTag, 1024)
			for _, t := range s.snapshotOfLibrary(snap.Dir).Tracks {
				cur[t.Path] = t
			}
			diff := make([]map[string]interface{}, 0, 32)
			for _, t := range snap.Tracks {
				c, ok := cur[t.Path]
				if !ok {
					diff = append(diff, map[string]interface{}{"path": t.Path, "change": "missing", "snap_title": t.Title})
					continue
				}
				fields := diffFields(t, c)
				if len(fields) > 0 {
					diff = append(diff, map[string]interface{}{
						"path": t.Path, "change": "changed", "fields": fields,
						"snap_title": t.Title, "now_title": c.Title,
					})
				}
				if len(diff) >= 500 {
					break
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "ok",
				"data": map[string]interface{}{
					"id": snap.ID, "created": snap.Created, "kind": snap.Kind, "dir": snap.Dir,
					"count": snap.Count, "writable": snap.Writable, "note": snap.Note,
					"tracks": snap.Tracks, "diff": diff,
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"items":     s.listMetaSnapshots(),
				"keep_days": s.metaSnapshotKeepDays(),
				"scheduled": s.metaSnapshotScheduleEnabled(),
			},
		})
	case http.MethodPost:
		var body struct {
			Op  string `json:"op"`
			ID  string `json:"id"`
			Dir string `json:"dir"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		switch strings.TrimSpace(body.Op) {
		case "create":
			snap, err := s.createMetaSnapshot("manual", strings.TrimSpace(body.Dir), "")
			if err != nil {
				errJSON(w, http.StatusInternalServerError, "拍快照失败："+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": fmt.Sprintf("已快照 %d 首歌的元数据", snap.Count),
				"data": map[string]interface{}{"id": snap.ID, "count": snap.Count, "writable": snap.Writable},
			})
		case "restore":
			res, err := s.restoreMetaSnapshot(body.ID)
			if err != nil {
				errJSON(w, http.StatusBadRequest, "恢复失败："+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": fmt.Sprintf("已恢复 %d 首的元数据", res.Restored),
				"data": res,
			})
		case "delete":
			id := strings.TrimSpace(body.ID)
			if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
				errJSON(w, http.StatusBadRequest, "快照 id 不合法")
				return
			}
			if err := os.RemoveAll(filepath.Join(s.metaSnapshotRoot(), id)); err != nil {
				errJSON(w, http.StatusInternalServerError, "删除失败："+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已删除"})
		default:
			errJSON(w, http.StatusBadRequest, "op 只能是 create / restore / delete")
		}
	default:
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
	}
}

// metaRestoreResult 恢复结果
type metaRestoreResult struct {
	Restored    int                      `json:"restored"`     // 真的写回去的曲目数
	Unchanged   int                      `json:"unchanged"`    // 本来就一致、不用写的
	Skipped     int                      `json:"skipped"`      // 写不了标签（wav/ape 等）
	Missing     int                      `json:"missing"`      // 文件已经不在了
	Errors      int                      `json:"errors"`       //
	PreSnapshot string                   `json:"pre_snapshot"` // 恢复前的快照（恢复本身也能撤）
	Items       []map[string]interface{} `json:"items"`        //
}

// restoreMetaSnapshot 把一份快照里的标签写回文件。
//
// 三条安全规则：
//  1. 先给**当前状态**拍一份 pre-restore 快照，恢复本身也能被撤；
//  2. 只写本应用能写标签的格式（mp3/flac），其余如实计入 skipped；
//  3. 文件大小/修改时间和快照里差太多的，先跳过 —— 那多半是换过文件，
//     把旧元数据盖上去只会制造新的脏数据。
func (s *Server) restoreMetaSnapshot(id string) (metaRestoreResult, error) {
	res := metaRestoreResult{Items: make([]map[string]interface{}, 0, 32)}
	snap, err := s.loadMetaSnapshot(id)
	if err != nil {
		return res, fmt.Errorf("找不到这份快照：%w", err)
	}
	pre, err := s.createMetaSnapshot("pre-restore", snap.Dir, "恢复「"+id+"」之前的自动快照")
	if err == nil {
		res.PreSnapshot = pre.ID
	}

	idx := s.libraryIndex()
	for _, t := range snap.Tracks {
		if !audioext.CanWriteTags(t.Path) {
			res.Skipped++
			continue
		}
		if _, statErr := os.Stat(t.Path); statErr != nil {
			res.Missing++
			continue
		}
		cur, ok := idx.Get(t.Path)
		if !ok {
			// 索引没有这条：按磁盘重读一次，避免把「索引没扫到」当成「没变化」
			cur = library.Entry{Path: t.Path}
		}
		if t.Size > 0 && cur.Size > 0 && cur.Size != t.Size {
			res.Errors++
			res.Items = append(res.Items, map[string]interface{}{
				"path": t.Path, "error": "文件大小和快照不一致（可能已被替换），跳过",
			})
			continue
		}
		if len(diffFields(t, metaTag{
			Title: cur.Title, Artist: cur.Artist, Album: cur.Album, Genre: cur.Genre,
			Year: cur.Year, Track: cur.Track, Disc: cur.Disc,
		})) == 0 {
			res.Unchanged++
			continue
		}
		opt := tidy.DefaultOptions()
		opt.EmbedLyric, opt.WriteLRC = false, false
		opt.EmbedCover, opt.WriteCoverFile = false, false
		opt.OverwriteLyric, opt.OverwriteCover = false, false
		opt.WriteMetadata = true
		r := tidy.TidyOne(tidy.Item{
			Path: t.Path, Title: t.Title, Artist: t.Artist, Album: t.Album,
			Genre: t.Genre, Year: t.Year, Track: t.Track, Disc: t.Disc,
		}, opt)
		if !r.OK {
			res.Errors++
			res.Items = append(res.Items, map[string]interface{}{"path": t.Path, "error": r.Error})
			continue
		}
		res.Restored++
		idx.MarkEnriched(t.Path, library.Enrich{
			Title: t.Title, Artist: t.Artist, Album: t.Album, Genre: t.Genre,
			Year: t.Year, Track: t.Track, Disc: t.Disc,
			HasLyric: cur.HasLyric, HasCover: cur.HasCover,
		})
		if len(res.Items) < 200 {
			res.Items = append(res.Items, map[string]interface{}{
				"path": t.Path, "title": t.Title, "from": cur.Title,
			})
		}
	}
	if res.Restored > 0 {
		_ = idx.Save()
		fnos.ScheduleLibraryRescan()
	}
	return res, nil
}

// diffFields 比较两份标签，返回不一样的字段名
func diffFields(a, b metaTag) []string {
	out := make([]string, 0, 4)
	if a.Title != b.Title {
		out = append(out, "title")
	}
	if a.Artist != b.Artist {
		out = append(out, "artist")
	}
	if a.Album != b.Album {
		out = append(out, "album")
	}
	if a.Genre != b.Genre {
		out = append(out, "genre")
	}
	if a.Year != b.Year {
		out = append(out, "year")
	}
	if a.Track != b.Track {
		out = append(out, "track")
	}
	if a.Disc != b.Disc {
		out = append(out, "disc")
	}
	return out
}

// writeJSONFile 原子写一个 JSON 文件（先写临时文件再 rename）
func writeJSONFile(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// metaSnapshotDigest 一份快照的内容指纹（给定时快照做「内容没变就不重复存」判断）
func metaSnapshotDigest(tracks []metaTag) string {
	h := sha1.New()
	for _, t := range tracks {
		io.WriteString(h, t.Path)
		io.WriteString(h, "\x00"+t.Title+"\x00"+t.Artist+"\x00"+t.Album+"\x00"+t.Genre)
		io.WriteString(h, "\x00"+strconv.Itoa(t.Year)+"\x00"+strconv.Itoa(t.Track)+"\x00"+strconv.Itoa(t.Disc))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// latestMetaSnapshotDigest 最近一份定时快照的内容指纹（没有则空）
func (s *Server) latestMetaSnapshotDigest() string {
	list := s.listMetaSnapshots()
	for _, it := range list {
		if it.Kind != "scheduled" {
			continue
		}
		if snap, err := s.loadMetaSnapshot(it.ID); err == nil {
			return metaSnapshotDigest(snap.Tracks)
		}
		return ""
	}
	return ""
}

// scheduledMetaSnapshot 定时任务调它：内容没变就跳过，避免每小时堆一份完全相同的快照
func (s *Server) scheduledMetaSnapshot() (metaSnapshot, bool, error) {
	cur := s.snapshotOfLibrary("")
	if cur.Count == 0 {
		return cur, false, fmt.Errorf("曲库索引为空，跳过")
	}
	digest := metaSnapshotDigest(cur.Tracks)
	if digest == s.latestMetaSnapshotDigest() {
		return cur, false, nil
	}
	snap, err := s.createMetaSnapshot("scheduled", "", "定时快照")
	if err != nil {
		log.Printf("[META-SNAP] 定时快照失败：%v", err)
		return snap, false, err
	}
	return snap, true, nil
}
