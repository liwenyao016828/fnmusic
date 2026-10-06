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
