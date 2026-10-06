package library

import (
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/dirwalk"
)

// AudioExtensions 参与音乐库索引的音频扩展名。
//
// 清单本体在 pkg/audioext，这里只是别名：pkg/nas 的浏览/计数必须认同一份，
// 否则会出现「索引里看得见、NAS 浏览器里看不见」这种自相矛盾。
var AudioExtensions = audioext.Exts

// ScanOptions 枚举参数。
//
// 遍历规则本体在 pkg/dirwalk（排除目录、深度上限、软链接环剪枝、数量上限、
// 协作式取消都在那儿），这里只是曲库索引视角的参数名 —— NAS 浏览接口调的是同一份，
// 所以两边对「同一棵目录树里有几首歌」永远给出同一个答案。
type ScanOptions struct {
	// MaxDepth 最大递归深度（0 表示使用默认值 12），超出即计入 DepthTruncated
	MaxDepth int
	// MaxAudio 音频数量上限；命中后停止枚举并置 audit.LimitHit（0 表示不限制）
	MaxAudio int
	// ExcludeDirs 额外跳过的目录名
	ExcludeDirs []string
	// ShouldStop 返回 true 时提前停止（用于协作式取消）
	ShouldStop func() bool
	// OnProgress 每遍历完一个子目录回调一次（用于上报进度，避免长扫描看起来像卡死）
	OnProgress func(dirs, files, audio int)
}

// Scan 枚举 roots 下的音频文件。
//
// 返回 (path -> Entry) 与对账信息。**不读取文件内容**（不做 ID3 解析），
// 因此可以快速遍历大曲库；标签只在确认文件是新增/变化后才需要读取。
//
// 枚举交给 pkg/dirwalk：以前这套规则和 pkg/nas 的浏览扫描是两份实现，
// 排除目录、深度上限、软链接环保护只在索引这一侧生效，浏览器那一侧全无。
func Scan(roots []string, opts ScanOptions) (map[string]Entry, Audit) {
	files, st := dirwalk.Walk(roots, dirwalk.Options{
		MaxDepth:    opts.MaxDepth,
		MaxAudio:    opts.MaxAudio,
		ExcludeDirs: opts.ExcludeDirs,
		ShouldStop:  opts.ShouldStop,
		OnProgress:  opts.OnProgress,
	})

	now := time.Now().Unix()
	seen := make(map[string]Entry, len(files))
	for _, f := range files {
		seen[f.Path] = Entry{
			Path:   f.Path,
			Size:   f.Size,
			MTime:  f.MTime,
			Format: f.Format,
			SeenAt: now,
		}
	}

	audit := Audit{
		Roots:          st.Roots,
		FilesTotal:     st.FilesTotal,
		AudioTotal:     st.AudioTotal,
		Inaccessible:   st.Inaccessible,
		DepthTruncated: st.DepthTruncated,
		LimitHit:       st.LimitHit,
		MissingRoots:   st.MissingRoots,
		Complete:       st.Complete(),
		Warnings:       st.Warnings,
		ElapsedMs:      st.ElapsedMs,
	}
	return seen, audit
}
