package tidy

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"fn-lx-player/pkg/namefmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"fn-lx-player/pkg/match"
)

// DupGroup 一组重复曲目
type DupGroup struct {
	// Type: content（内容完全相同）/ name（同名同歌手但内容不同）
	Type      string    `json:"type"`
	Key       string    `json:"key"`
	Count     int       `json:"count"`
	TotalSize int64     `json:"total_size"`
	Keep      string    `json:"keep"`   // 建议保留的文件
	Delete    []string  `json:"delete"` // 建议清理的文件
	Items     []DupItem `json:"items"`
}

// DupItem 组内单个文件
type DupItem struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Format   string `json:"format"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	HasLyric bool   `json:"has_lyric"`
	HasCover bool   `json:"has_cover"`
	// 保留优先级（越大越该保留）
	Priority int `json:"priority"`
}

// DedupeOptions 去重选项
type DedupeOptions struct {
	// IncludeNameDuplicate 是否也检测「同名同歌手但内容不同」的重复
	IncludeNameDuplicate bool `json:"include_name_duplicate"`
	// MaxFiles 最多扫描的文件数（防止超大曲库一次性读太多）
	MaxFiles int `json:"max_files"`
	// HashConcurrency 计算 SHA-1 的并发度
	HashConcurrency int `json:"hash_concurrency"`
}

// DefaultDedupeOptions 默认去重选项
func DefaultDedupeOptions() DedupeOptions {
	return DedupeOptions{
		IncludeNameDuplicate: true,
		MaxFiles:             5000,
		HashConcurrency:      4,
	}
}

// 无损容器：去重时优先保留
var losslessFormats = map[string]bool{
	"flac": true, "ape": true, "wav": true, "aiff": true, "aif": true,
	"alac": true, "dsf": true, "dff": true, "wv": true,
}

// priority 计算保留优先级：无损 > 已整理（有歌词/封面） > 体积更大
//
// 这个排序口径来自 music-tidy 的 _quality_key，但补上了「已整理」维度，
// 避免把已经补好歌词封面的文件删掉、留下没标签的那个。
func priorityOf(it DupItem) int {
	p := 0
	if losslessFormats[strings.ToLower(it.Format)] {
		p += 1000
	}
	if it.HasLyric {
		p += 100
	}
	if it.HasCover {
		p += 50
	}
	return p
}

// FindDuplicates 扫描曲库找出重复。
//
// 双通道（与 music-tidy 一致）：
//   - content：SHA-1 相同 → 确定重复
//   - name：归一化「歌手|歌名」相同但 SHA-1 不同 → 疑似同曲不同版本
//
// 做 SHA-1 前先按 (size) 分桶：只有体积相同的文件才可能内容相同，
// 这样能把哈希计算量降到极低（大曲库的关键优化）。
func FindDuplicates(paths []string, opts DedupeOptions) ([]DupGroup, []string) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 5000
	}
	if opts.HashConcurrency <= 0 {
		opts.HashConcurrency = 4
	}

	warnings := make([]string, 0)
	if len(paths) > opts.MaxFiles {
		warnings = append(warnings,
			fmt.Sprintf("曲库文件数 %d 超过本次上限 %d，仅检测前 %d 个",
				len(paths), opts.MaxFiles, opts.MaxFiles))
		paths = paths[:opts.MaxFiles]
	}

	// 1. 收集文件信息
	items := make([]DupItem, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		it := DupItem{
			Path:   p,
			Size:   fi.Size(),
			Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), "."),
		}
		// 从文件名解析歌手/歌名，用于 name 通道。
		// 方向按用户配的命名格式走（pkg/namefmt）—— 这里写死「歌手在前」的话，
		// 换格式之后去重会把「歌名」当成歌手参与匹配，同一首歌的两份反而对不上。
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		it.Artist, it.Title = namefmt.Parse(base)
		items = append(items, it)
	}

	// 2. 按体积分桶，只对可能重复的候选算 SHA-1
	bySize := make(map[int64][]int)
	for i, it := range items {
		bySize[it.Size] = append(bySize[it.Size], i)
	}

	hashOf := make(map[string]string) // path -> sha1
	hashed := 0
	for size, idxs := range bySize {
		if len(idxs) < 2 || size == 0 {
			continue // 体积唯一的文件不可能有内容重复
		}
		for _, i := range idxs {
			h, err := fileSHA1(items[i].Path)
			if err != nil {
				continue
			}
			hashOf[items[i].Path] = h
			hashed++
		}
	}
	_ = hashed

	groups := make([]DupGroup, 0)

	// 3. content 通道：SHA-1 分组
	byHash := make(map[string][]DupItem)
	for _, it := range items {
		h, ok := hashOf[it.Path]
		if !ok {
			continue
		}
		byHash[h] = append(byHash[h], it)
	}
	for h, group := range byHash {
		if len(group) < 2 {
			continue
		}
		groups = append(groups, buildGroup("content", h, group))
	}

	// 4. name 通道：归一化「歌手|歌名」相同但 SHA-1 不同
	if opts.IncludeNameDuplicate {
		byName := make(map[string][]DupItem)
		for _, it := range items {
			key := match.NormArtist(it.Artist) + "|" + match.NormTitle(it.Title)
			if key == "|" || strings.Trim(key, "|") == "" {
				continue
			}
			byName[key] = append(byName[key], it)
		}
		for key, group := range byName {
			if len(group) < 2 {
				continue
			}
			// 若组内 SHA-1 只有一种，说明已被 content 通道覆盖，跳过
			seenHash := make(map[string]bool)
			for _, it := range group {
				if h, ok := hashOf[it.Path]; ok {
					seenHash[h] = true
				}
			}
			if len(seenHash) <= 1 {
				continue
			}
			groups = append(groups, buildGroup("name", key, group))
		}
	}

	// 按可回收空间降序，最该处理的排前面
	sort.Slice(groups, func(a, b int) bool {
		sa := groups[a].TotalSize - sizeOfKeep(groups[a])
		sb := groups[b].TotalSize - sizeOfKeep(groups[b])
		if sa != sb {
			return sa > sb
		}
		return groups[a].Key < groups[b].Key
	})

	return groups, warnings
}

func sizeOfKeep(g DupGroup) int64 {
	for _, it := range g.Items {
		if it.Path == g.Keep {
			return it.Size
		}
	}
	return 0
}

func buildGroup(kind, key string, items []DupItem) DupGroup {
	g := DupGroup{Type: kind, Key: key, Items: make([]DupItem, 0, len(items))}

	var total int64
	for _, it := range items {
		total += it.Size
		// 读取是否有歌词/封面（用于保留优先级），失败不影响去重
		if needLyric, needCover, err := NeedsTidy(it.Path); err == nil {
			it.HasLyric = !needLyric
			it.HasCover = !needCover
		}
		it.Priority = priorityOf(it)
		g.Items = append(g.Items, it)
	}

	// 排序：优先级高的在前；同优先级保留体积更大的（信息更全）。
	// 最后用文件名做确定性兜底：同分时保留名字更短的那个，
	// 即「原始文件名」而不是「xxx (2).mp3」这类副本。
	// 没有这个兜底时，同分文件保留哪个取决于 map 遍历顺序，结果不可复现。
	sort.SliceStable(g.Items, func(a, b int) bool {
		if g.Items[a].Priority != g.Items[b].Priority {
			return g.Items[a].Priority > g.Items[b].Priority
		}
		if g.Items[a].Size != g.Items[b].Size {
			return g.Items[a].Size > g.Items[b].Size
		}
		la, lb := len(filepath.Base(g.Items[a].Path)), len(filepath.Base(g.Items[b].Path))
		if la != lb {
			return la < lb
		}
		return g.Items[a].Path < g.Items[b].Path
	})

	g.Count = len(g.Items)
	g.TotalSize = total
	g.Keep = g.Items[0].Path
	for _, it := range g.Items[1:] {
		g.Delete = append(g.Delete, it.Path)
	}
	return g
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ResolveOptions 执行去重的选项
type ResolveOptions struct {
	// Mode: trash（默认，移入回收站）/ delete（直接删除）
	Mode string `json:"mode"`
	// TrashDir 回收站目录名（相对曲库根），默认 .tidy_trash
	TrashDir string `json:"trash_dir"`
	// DryRun 只计算不执行
	DryRun bool `json:"dry_run"`
}

// ResolveResult 去重执行结果
type ResolveResult struct {
	DryRun    bool     `json:"dry_run"`
	Mode      string   `json:"mode"`
	Moved     int      `json:"moved"`
	Deleted   int      `json:"deleted"`
	Failed    int      `json:"failed"`
	Freed     int64    `json:"freed_bytes"`
	Processed []string `json:"processed,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

// ResolveDuplicates 执行去重。
//
// 默认 mode=trash：**移入回收站而不是直接删除**，误判时可恢复。
// 这是刻意与「直接删除」区分开的安全默认值。
func ResolveDuplicates(groups []DupGroup, opts ResolveOptions) ResolveResult {
	if opts.Mode == "" {
		opts.Mode = "trash"
	}
	if opts.TrashDir == "" {
		opts.TrashDir = ".tidy_trash"
	}

	res := ResolveResult{DryRun: opts.DryRun, Mode: opts.Mode}

	for _, g := range groups {
		for _, victim := range g.Delete {
			fi, err := os.Stat(victim)
			if err != nil {
				res.Failed++
				if len(res.Errors) < 50 {
					res.Errors = append(res.Errors, victim+": "+err.Error())
				}
				continue
			}
			size := fi.Size()

			if opts.DryRun {
				res.Freed += size
				if opts.Mode == "delete" {
					res.Deleted++
				} else {
					res.Moved++
				}
				continue
			}

			if opts.Mode == "delete" {
				if err := os.Remove(victim); err != nil {
					res.Failed++
					if len(res.Errors) < 50 {
						res.Errors = append(res.Errors, victim+": "+err.Error())
					}
					continue
				}
				res.Deleted++
			} else {
				dst, err := moveToTrash(victim, opts.TrashDir)
				if err != nil {
					res.Failed++
					if len(res.Errors) < 50 {
						res.Errors = append(res.Errors, victim+": "+err.Error())
					}
					continue
				}
				// 同时把同名 .lrc / 封面一起搬走，避免留下孤立文件
				moveSidecars(victim, dst)
				res.Moved++
			}
			res.Freed += size
			if len(res.Processed) < 200 {
				res.Processed = append(res.Processed, victim)
			}
		}
	}
	return res
}

// moveToTrash 把文件移入回收站，保持相对目录结构避免同名覆盖
func moveToTrash(src, trashDir string) (string, error) {
	base := filepath.Base(filepath.Dir(src))
	name := filepath.Base(src)
	dstDir := filepath.Join(trashDir, base)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, name)

	// 同名冲突时加序号，绝不覆盖回收站里已有的文件
	if fileExists(dst) {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		for i := 2; i < 1000; i++ {
			cand := filepath.Join(dstDir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
			if !fileExists(cand) {
				dst = cand
				break
			}
		}
	}

	if err := renameOrCopy(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// renameOrCopy 优先用 rename（快、原子），失败则退化为「复制 + 删除」。
//
// 为什么要退化：飞牛 NAS 上一个卷里可能挂多个子卷（例如 /vol3 本身是卷根，
// 而 /vol3/1000 是独立子卷）。回收站目录由 joinTrash 挑「最深的允许根」，
// 常见结果是 <卷根>/.tidy_trash，而源文件在 <卷根>/<共享目录>/… 子卷里 ——
// 跨子卷的 os.Rename 会直接返回 EXDEV（invalid cross-device link）。
// 2026-10-30 用户在 NAS 上删 NAS 文件时命中这个 500。
//
// 退化路径：复制到 dst 的临时文件 → rename 到 dst（同目录内必然同设备）→
// 再删源文件。任何一步失败都回滚（删掉临时文件），不会留下半个文件。
func renameOrCopy(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) && !isCrossDeviceErr(err) {
		return err
	}

	srcInfo, statErr := os.Stat(src)
	if statErr != nil {
		return statErr
	}
	in, openErr := os.Open(src)
	if openErr != nil {
		return openErr
	}
	defer in.Close()

	tmp := dst + ".xdev.tmp"
	out, createErr := os.Create(tmp)
	if createErr != nil {
		return createErr
	}
	if _, cpErr := io.Copy(out, in); cpErr != nil {
		out.Close()
		_ = os.Remove(tmp)
		return cpErr
	}
	if closeErr := out.Close(); closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if srcInfo.Mode().Perm() != 0 {
		_ = os.Chmod(tmp, srcInfo.Mode().Perm())
	}
	if renameErr := os.Rename(tmp, dst); renameErr != nil {
		_ = os.Remove(tmp)
		return renameErr
	}
	if rmErr := os.Remove(src); rmErr != nil {
		// 源删不掉：回收站里多了一份，但宁可留下副本也不丢数据
		return fmt.Errorf("跨卷移动：已复制到 %s，但源文件删除失败：%w", dst, rmErr)
	}
	return nil
}

// isCrossDeviceErr 兜底判断跨设备错误（部分平台 syscall.EXDEV 不可直接用时）。
func isCrossDeviceErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cross-device")
}

// moveSidecars 把与音频同名的附属文件一起移入回收站
func moveSidecars(original, movedTo string) {
	stem := strings.TrimSuffix(original, filepath.Ext(original))
	movedStem := strings.TrimSuffix(movedTo, filepath.Ext(movedTo))
	for _, ext := range []string{".lrc", ".jpg", ".jpeg", ".png"} {
		if !fileExists(stem + ext) {
			continue
		}
		_ = os.Rename(stem+ext, movedStem+ext)
	}
}
