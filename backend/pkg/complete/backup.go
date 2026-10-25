// 一次 AI 补全任务的「可撤销」记录。
//
// ── 为什么需要 ──
//
// 补全会**真正改写用户的音频文件**（内嵌歌词/封面/标签）。规则再稳、AI 再准，
// 也可能出现「整批都补错了」的情况（比如匹配到了另一个歌手的同名专辑）。
// 没有回滚手段的批量写文件功能，用户是不敢用的。
//
// ── 为什么是「整文件备份」而不是「记录改了什么再反向操作」 ──
//
// 标签写入是**破坏性重写**（原子替换整个文件）。要反向操作就得精确还原
// 原有的 ID3 帧顺序、文本编码、填充长度 —— 那比重拷一份文件脆弱得多，
// 而且一旦还原得不完美，文件可能就坏了。
// 音频文件不算大，宁可多占点磁盘，也要保证「撤销」真的能还原。
//
// ── 备份里都记了什么 ──
//
//   - 原文件本身（改写前拷一份）
//   - 本次**新建**的同名 sidecar（`.lrc` / `.jpg`）—— 撤销时删掉它们。
//     注意只删「本来不存在、这次才出现的」，用户原有的文件不能碰。
package complete

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// BackupEntry 一个文件的可撤销记录
type BackupEntry struct {
	Original string   `json:"original"` // 原文件路径
	Copy     string   `json:"copy"`     // 备份文件路径
	Size     int64    `json:"size"`
	Created  []string `json:"created,omitempty"` // 本次新建的 sidecar，撤销时删掉

	// 下面三项是「这次补全对这首歌写了什么」，供记录页直接展示前后对照。
	// 只记录**写成功的字段**；没写的字段留空（界面上就不显示那一行）。
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// OldTitle 改写**之前**的标题/歌手 —— 记录页要显示「旧 → 新」的前后对照，
	// 光有新值看不出到底改了什么（用户 2026-10-24：歌名修复记录要能看改前改后）。
	OldTitle  string   `json:"old_title,omitempty"`
	OldArtist string   `json:"old_artist,omitempty"`
	Fields    []string `json:"fields,omitempty"`     // 写进了哪些字段：lyric/cover/genre/year/track/disc/title/artist
	WrittenAt int64    `json:"written_at,omitempty"` // 写入时间（unix 秒）
}

// Backup 一次补全任务的备份。
//
// 并发安全：补全是并发跑的（Concurrency 默认 2），Save 会被多个 goroutine 调。
type Backup struct {
	mu      sync.Mutex
	dir     string
	entries []BackupEntry
	// pending 记录「写入前该文件有哪些 sidecar 存在」，用于事后判断哪些是新建的
	pending map[string][]string
}

// ManifestName 备份清单文件名
const ManifestName = "manifest.json"

// NewBackup 创建一次备份。dir 为空表示不备份（调用方自己判断）。
func NewBackup(dir string) *Backup {
	return &Backup{dir: dir, pending: map[string][]string{}}
}

// Dir 备份目录（空表示未启用）
func (b *Backup) Dir() string {
	if b == nil {
		return ""
	}
	return b.dir
}

// sidecarPaths 与音频同名的伴生文件（外挂歌词 / 逐曲封面）
func sidecarPaths(audioPath string) []string {
	stem := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	return []string{stem + ".lrc", stem + ".jpg", stem + ".png"}
}

// BeforeWrite 在改写某个文件**之前**调用：备份原文件，并记下当前有哪些 sidecar。
//
// 返回错误时调用方应当**放弃改这个文件** —— 备份失败还继续写，就失去了撤销能力。
func (b *Backup) BeforeWrite(path string) error {
	if b == nil || b.dir == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// 记下写入前存在的 sidecar（写入后新出现的才算「我们建的」）
	var existed []string
	for _, p := range sidecarPaths(path) {
		if _, err := os.Stat(p); err == nil {
			existed = append(existed, p)
		}
	}
	b.pending[path] = existed

	filesDir := filepath.Join(b.dir, "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return fmt.Errorf("创建备份目录失败: %w", err)
	}

	// 备份文件名带序号前缀，避免不同目录下的同名文件互相覆盖
	copyPath := filepath.Join(filesDir, fmt.Sprintf("%04d_%s", len(b.entries)+1, filepath.Base(path)))
	if err := copyFile(path, copyPath); err != nil {
		return fmt.Errorf("备份原文件失败: %w", err)
	}

	var size int64
	if fi, err := os.Stat(copyPath); err == nil {
		size = fi.Size()
	}
	b.entries = append(b.entries, BackupEntry{Original: path, Copy: copyPath, Size: size})
	return nil
}

// AfterWrite 在改写完成**之后**调用：记下这次新建了哪些 sidecar。
func (b *Backup) AfterWrite(path string) {
	if b == nil || b.dir == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	before := map[string]bool{}
	for _, p := range b.pending[path] {
		before[p] = true
	}
	delete(b.pending, path)

	for _, p := range sidecarPaths(path) {
		if before[p] {
			continue // 本来就有，不是我们建的
		}
		if _, err := os.Stat(p); err == nil {
			// 找到对应条目追加
			for i := range b.entries {
				if b.entries[i].Original == path {
					b.entries[i].Created = append(b.entries[i].Created, p)
					break
				}
			}
		}
	}
}

// Note 记下「这首歌这次写了什么」，供记录页展示前后对照。
//
// 必须由调用方在**写完标签之后**调用（写入成功才有意义）。path 不在清单里就
// 忽略 —— 没备份的文件本来也撤不了，记了只会让界面上的「可撤销」骗人。
func (b *Backup) Note(path, title, artist string, fields []string) {
	b.NoteWithOld(path, "", "", title, artist, fields)
}

// NoteWithOld 与 Note 相同，但额外记下改写**之前**的标题/歌手。
//
// 歌名修复要靠它显示「《未知》→《晴天》」；补全路径不需要旧值，继续用 Note。
func (b *Backup) NoteWithOld(path, oldTitle, oldArtist, title, artist string, fields []string) {
	if b == nil || b.dir == "" || len(fields) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.entries {
		if b.entries[i].Original != path {
			continue
		}
		b.entries[i].OldTitle = oldTitle
		b.entries[i].OldArtist = oldArtist
		b.entries[i].Title = title
		b.entries[i].Artist = artist
		b.entries[i].WrittenAt = time.Now().Unix()
		// 同一字段被写两次（理论上不会）也只记一条
		seen := map[string]bool{}
		for _, f := range b.entries[i].Fields {
			seen[f] = true
		}
		for _, f := range fields {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			b.entries[i].Fields = append(b.entries[i].Fields, f)
		}
		return
	}
}

// Commit 落盘清单。没备份任何文件时什么都不做（不留空目录）。
func (b *Backup) Commit() error {
	if b == nil || b.dir == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) == 0 {
		_ = os.RemoveAll(b.dir)
		return nil
	}
	return writeManifest(b.dir, b.entries)
}

// Entries 当前清单的副本（提交前也能读，供调用方事后统计本次改了哪些文件）
func (b *Backup) Entries() []BackupEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BackupEntry, len(b.entries))
	copy(out, b.entries)
	return out
}

// Count 备份了几个文件
func (b *Backup) Count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// TotalSize 备份占用的总字节数
func (b *Backup) TotalSize() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int64
	for _, e := range b.entries {
		n += e.Size
	}
	return n
}

// ── 撤销 ──

// RestoreResult 撤销结果
type RestoreResult struct {
	Dir      string   `json:"dir"`
	Restored int      `json:"restored"` // 还原的音频文件数
	Removed  int      `json:"removed"`  // 删掉的 sidecar 数
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
	// RestoredPaths 成功还原的文件路径 —— 调用方要拿它刷新索引
	// （索引里的 HasLyric/HasCover 在还原后就过期了）
	RestoredPaths []string `json:"-"`
}

// FindLatest 找最近一次备份目录（没有则返回空串）
func FindLatest(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	// 目录名是时间戳（见 NewBackupDir），字典序即时间序
	sort.Strings(names)
	latest := filepath.Join(root, names[len(names)-1])
	if _, err := os.Stat(filepath.Join(latest, ManifestName)); err != nil {
		return ""
	}
	return latest
}

// NewBackupDir 在 root 下开一个以时间戳命名的新备份目录
func NewBackupDir(root string) string {
	return filepath.Join(root, time.Now().Format("20060102-150405"))
}

// Restore 从指定备份目录还原，成功后**删掉该备份目录**（撤销只做一次）。
func Restore(dir string) RestoreResult {
	res := RestoreResult{Dir: dir}
	if dir == "" {
		res.Errors = append(res.Errors, "没有可撤销的补全记录")
		return res
	}

	entries, err := readManifest(dir)
	if err != nil {
		res.Errors = append(res.Errors, "读取备份清单失败: "+err.Error())
		return res
	}
	if len(entries) == 0 {
		res.Errors = append(res.Errors, "备份清单是空的")
		return res
	}

	for _, e := range entries {
		// 先删新建的 sidecar，再还原原文件 ——
		// 顺序反过来的话，还原后的文件又会多出一份不属于它的 .lrc
		for _, p := range e.Created {
			if err := os.Remove(p); err == nil {
				res.Removed++
			}
		}
		if err := copyFile(e.Copy, e.Original); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", filepath.Base(e.Original), err))
			continue
		}
		res.Restored++
		res.RestoredPaths = append(res.RestoredPaths, e.Original)
	}

	// 全部成功才删备份；有失败就留着让用户能重试
	if res.Failed == 0 {
		_ = os.RemoveAll(dir)
	}
	return res
}

// ── 内部 ──

func writeManifest(dir string, entries []BackupEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), b, 0o600)
}

func readManifest(dir string) ([]BackupEntry, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	var out []BackupEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// copyFile 复制文件（保留权限位）。
//
// 用「临时文件 + rename」保证原子性：还原到一半被打断也不会留下半个文件。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// 保留原文件权限（音频一般是 0644）
	if fi, err := os.Stat(src); err == nil {
		_ = os.Chmod(tmp, fi.Mode().Perm())
	}
	return os.Rename(tmp, dst)
}

// ── 单条撤销 / 只读查看（2026-10-24 起） ──
//
// 上面那套 Restore 是「整批撤销」：一次补全的所有文件一起还原，然后备份目录整个删掉。
// 但用户真正想要的是「这一首补错了，就撤这一首」—— 于是记录里要能下钻到单条：
//
//   ListEntries  读某次备份的清单（界面上「补全结果 / 快照」列表的数据源）
//   RestoreEntry 只还原清单里的某几条，其余的不动
//
// RestoreEntry 还原成功的条目会从清单里**移除**；清单空了才删整个备份目录。
// 这样「撤了一首」之后剩下的还能再撤，而不会因为「撤过一次」就整批失效。

// ListEntries 读某次备份的清单。dir 为空或读不到时返回空列表（不是错误 ——
// 界面上「这次补全没备份任何文件」是正常状态，不该让调用方当成失败）。
func ListEntries(dir string) []BackupEntry {
	entries, err := readManifest(dir)
	if err != nil {
		return nil
	}
	return entries
}

// ListBackups 列出 root 下全部备份目录（按时间倒序，最新的在前），供「记录」页选。
func ListBackups(root string) []string {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range dirs {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(p, ManifestName)); err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// BackupInfo 一次备份的概览（记录页列表用）
type BackupInfo struct {
	Dir     string        `json:"dir"`
	ID      string        `json:"id"` // 目录名（时间戳），接口里用它指代这次备份
	At      int64         `json:"at"` // 备份时间（unix 秒）
	Count   int           `json:"count"`
	Size    int64         `json:"size"`
	Entries []BackupEntry `json:"entries,omitempty"`
}

// Describe 读某次备份的概览；withEntries 为真时把逐条清单也带上。
func Describe(dir string, withEntries bool) BackupInfo {
	info := BackupInfo{Dir: dir, ID: filepath.Base(dir)}
	if ts, err := time.Parse("20060102-150405", info.ID); err == nil {
		info.At = ts.Unix()
	}
	entries := ListEntries(dir)
	info.Count = len(entries)
	for _, e := range entries {
		info.Size += e.Size
	}
	if withEntries {
		info.Entries = entries
	}
	return info
}

// RestoreEntry 只还原清单里 original 命中 keep 的那些条目。
//
// keep 用**原文件路径**匹配（调用方从 ListEntries 拿到路径原样传回来即可）。
// 返回的 RestoredPaths 同样给调用方刷索引；清单里没命中的条目原样保留。
func RestoreEntry(dir string, keep []string) RestoreResult {
	res := RestoreResult{Dir: dir}
	if dir == "" {
		res.Errors = append(res.Errors, "没有可撤销的备份")
		return res
	}
	entries, err := readManifest(dir)
	if err != nil {
		res.Errors = append(res.Errors, "读取备份清单失败: "+err.Error())
		return res
	}
	want := make(map[string]bool, len(keep))
	for _, p := range keep {
		want[p] = true
	}

	kept := make([]BackupEntry, 0, len(entries))
	hit := 0
	for _, e := range entries {
		if !want[e.Original] {
			kept = append(kept, e)
			continue
		}
		hit++
		for _, p := range e.Created {
			if err := os.Remove(p); err == nil {
				res.Removed++
			}
		}
		if err := copyFile(e.Copy, e.Original); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", filepath.Base(e.Original), err))
			kept = append(kept, e) // 没还原成功就留在清单里，允许重试
			continue
		}
		res.Restored++
		res.RestoredPaths = append(res.RestoredPaths, e.Original)
	}
	if hit == 0 {
		res.Errors = append(res.Errors, "清单里没有匹配的条目（可能已经撤过了）")
		return res
	}

	if len(kept) == 0 {
		_ = os.RemoveAll(dir)
		return res
	}
	// 只把「已还原」的条目从清单里去掉，剩下的重写清单
	if err := writeManifest(dir, kept); err != nil {
		res.Errors = append(res.Errors, "更新备份清单失败: "+err.Error())
	}
	return res
}
