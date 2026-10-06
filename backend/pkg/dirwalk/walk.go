// Package dirwalk 是全项目**唯一**的目录枚举实现。
//
// 以前有两套，规则各不相同：
//
//	pkg/library/scanner.go —— filepath.WalkDir，带审计（文件数/不可读/深度截断/
//	                          数量上限）、排除目录表、深度上限、软链接环剪枝、协作式取消；
//	pkg/nas/scanner.go     —— ScanSongs 里的 filepath.Walk，只跳隐藏目录，
//	                          固定 1000 首上限，没有环保护，没有审计，
//	                          而且「到上限」和「读不了目录」都不告诉调用方。
//
// 差别不是风格问题：同一棵目录树，曲库索引和 NAS 浏览器会给出互相矛盾的结果
// （「索引里没有、浏览里有」/「浏览里莫名其妙少了」），而符号链接环会让浏览接口
// 永远转不完。所以两边的遍历规则收在这一份里，pkg/library 与 pkg/nas 都调它。
package dirwalk

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/audioext"
)

// DefaultMaxDepth 递归深度上限（根目录的子目录算第 1 层）。
// 音乐库的目录树极少超过 6 层，12 层足以覆盖「歌手/专辑/碟」这类结构，
// 又能在有人误把 /vol1 当根目录时把代价钉住。
const DefaultMaxDepth = 12

// DefaultExcludeDirs 默认不进入的目录名。
//
// 回收站/缩略图/开发目录：里面不会有该入库的音乐，却会被逐层遍历
// （群晖的 @eaDir 甚至每个目录都带一份）。
var DefaultExcludeDirs = []string{
	".tidy_trash",
	".trash",
	"@eaDir",
	"#recycle",
	".recycle",
	"$RECYCLE.BIN",
	"System Volume Information",
	".git",
	"node_modules",
}

// Options 枚举参数。零值可用：深度走 DefaultMaxDepth、不额外排除目录、
// 不限数量、不取消、不上报进度。
type Options struct {
	// MaxDepth 最大递归深度（<=0 表示 DefaultMaxDepth）。顶到上限的目录记进
	// Stats.DepthTruncated 并写一条 warning —— 结果不完整必须让人看见。
	MaxDepth int

	// MaxAudio 音频数量上限（0 表示不限）。命中即停止枚举（SkipAll），
	// 置 Stats.LimitHit 并写 warning。
	MaxAudio int

	// ExcludeDirs 额外跳过的目录名（叠加在 DefaultExcludeDirs 之上）。
	ExcludeDirs []string

	// IncludeHidden 为 true 时不跳「.」开头的目录。
	// 默认跳过，但根目录本身是隐藏目录时照常遍历。
	IncludeHidden bool

	// AcceptFile 返回 false 的文件被跳过：不进 FilesTotal/AudioTotal，
	// 只在 Stats.Skipped 里计数（例：软链接文件指向允许根目录之外，不该暴露）。
	AcceptFile func(path string, mode fs.FileMode) bool

	// ShouldStop 协作式取消：返回 true 时尽快停止，Stats.Stopped 置位。
	ShouldStop func() bool

	// OnProgress 每遍历 20 个目录回调一次（长扫描不该看起来像卡死）。
	OnProgress func(dirsVisited, files, audio int)
}

// File 一个被枚举到的音频文件。不读文件内容（ID3/封面/歌词由调用方按需读）。
type File struct {
	Path    string // 统一斜杠形式
	Size    int64
	MTime   int64  // Unix 秒
	Format  string // 扩展名，小写，不带点
	Symlink bool
}

// Stats 本次枚举的对账信息。字段名与 JSON 键沿用 pkg/library.Audit 的口径，
// 这样两边的响应里 files_total / audio_total / inaccessible 含义完全一致。
type Stats struct {
	Roots          []string `json:"roots"`
	FilesTotal     int      `json:"files_total"`     // 遍历到的文件总数（含非音频）
	AudioTotal     int      `json:"audio_total"`     // 音频文件数
	DirsVisited    int      `json:"dirs_visited"`    // 进入过的目录数
	Inaccessible   int      `json:"inaccessible"`    // 读不了的目录/文件数
	DepthTruncated int      `json:"depth_truncated"` // 因深度上限被截断的目录数
	Skipped        int      `json:"skipped,omitempty"`
	LimitHit       bool     `json:"limit_hit"` // 命中数量上限而提前停止
	Stopped        bool     `json:"stopped,omitempty"`
	MissingRoots   []string `json:"missing_roots"` // 不存在或不是目录的根
	Warnings       []string `json:"warnings,omitempty"`
	ElapsedMs      int64    `json:"elapsed_ms"`
}

// Complete 枚举是否完整：没有缺失根目录、没有读不了的项、没有深度截断、
// 没命中上限、没被取消。只有完整时才允许据此判定「索引里的这条该删除」。
func (s Stats) Complete() bool {
	return len(s.MissingRoots) == 0 &&
		s.Inaccessible == 0 &&
		s.DepthTruncated == 0 &&
		!s.LimitHit &&
		!s.Stopped
}

// Walk 枚举 roots 下的音频文件。
//
// 返回枚举到的音频文件（按遍历顺序）与对账信息。任何「结果可能不完整」的情况
// 都会同时进 Stats 和 Warnings，绝不静默。
func Walk(roots []string, opts Options) ([]File, Stats) {
	start := time.Now()
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultMaxDepth
	}

	exclude := make(map[string]bool, len(DefaultExcludeDirs)+len(opts.ExcludeDirs))
	for _, d := range DefaultExcludeDirs {
		exclude[d] = true
	}
	for _, d := range opts.ExcludeDirs {
		if d = strings.TrimSpace(d); d != "" {
			exclude[d] = true
		}
	}

	stats := Stats{Roots: append([]string(nil), roots...)}
	files := make([]File, 0, 64)
	visited := make(map[string]bool) // realpath 去重，防软链接环

	for _, root := range roots {
		if opts.ShouldStop != nil && opts.ShouldStop() {
			break
		}

		fi, err := os.Stat(root)
		if err != nil || !fi.IsDir() {
			stats.MissingRoots = append(stats.MissingRoots, root)
			continue
		}

		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if opts.ShouldStop != nil && opts.ShouldStop() {
				return filepath.SkipAll
			}

			if err != nil {
				// 读不了的目录是「枚举不完整」的主要来源，单独计数 + 留下路径。
				stats.Inaccessible++
				if d != nil && d.IsDir() {
					stats.Warnings = append(stats.Warnings, "无法读取目录："+path+"（"+err.Error()+"）")
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				name := d.Name()
				if path != root {
					if exclude[name] {
						return filepath.SkipDir
					}
					if !opts.IncludeHidden && strings.HasPrefix(name, ".") {
						return filepath.SkipDir
					}
				}

				// 目录按 realpath 剪枝：软链接环和「同一目录被链接多次」都不会重复遍历。
				if real, rerr := filepath.EvalSymlinks(path); rerr == nil {
					if visited[real] {
						return filepath.SkipDir
					}
					visited[real] = true
				}

				depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
				if depth >= opts.MaxDepth {
					stats.DepthTruncated++
					stats.Warnings = append(stats.Warnings, "目录层级超过上限，已跳过更深层："+path)
					return filepath.SkipDir
				}

				stats.DirsVisited++
				if opts.OnProgress != nil && stats.DirsVisited%20 == 0 {
					opts.OnProgress(stats.DirsVisited, stats.FilesTotal, stats.AudioTotal)
				}
				return nil
			}

			// 先判扩展名，再决定要不要花系统调用取文件信息。
			ext := strings.ToLower(filepath.Ext(path))
			audio := audioext.Exts[ext]

			if opts.AcceptFile != nil && !opts.AcceptFile(path, d.Type()) {
				stats.Skipped++
				return nil
			}
			stats.FilesTotal++
			if !audio {
				return nil
			}

			symlink := d.Type()&os.ModeSymlink != 0
			if symlink {
				// 链接解析不了就跳过：算「读不了」，不算「这个库里没有这首歌」。
				if _, rerr := filepath.EvalSymlinks(path); rerr != nil {
					stats.Inaccessible++
					return nil
				}
			}

			info, ierr := d.Info()
			if ierr != nil {
				stats.Inaccessible++
				return nil
			}

			stats.AudioTotal++
			files = append(files, File{
				Path:    filepath.ToSlash(path),
				Size:    info.Size(),
				MTime:   info.ModTime().Unix(),
				Format:  strings.TrimPrefix(ext, "."),
				Symlink: symlink,
			})

			if opts.MaxAudio > 0 && stats.AudioTotal >= opts.MaxAudio {
				stats.LimitHit = true
				stats.Warnings = append(stats.Warnings,
					"音频数量达到本次上限，已提前停止枚举（结果不完整）")
				return filepath.SkipAll
			}
			return nil
		})

		if walkErr != nil && walkErr != filepath.SkipAll {
			stats.Inaccessible++
			stats.Warnings = append(stats.Warnings, "遍历目录出错："+root+"（"+walkErr.Error()+"）")
		}
		if stats.LimitHit {
			break // 命中上限是全局停止，不必再走下一个根目录
		}
	}

	if opts.ShouldStop != nil && opts.ShouldStop() {
		stats.Stopped = true
		stats.Warnings = append(stats.Warnings, "枚举被主动取消，结果不完整")
	}

	if opts.OnProgress != nil {
		opts.OnProgress(stats.DirsVisited, stats.FilesTotal, stats.AudioTotal)
	}

	stats.ElapsedMs = time.Since(start).Milliseconds()
	return files, stats
}
