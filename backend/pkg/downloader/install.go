package downloader

import (
	"errors"
	"os"
	"sync"
)

// ── 同一目标文件的在途互斥 ──
//
// .part 的名字是「目标基名 + 平台曲目 ID」确定性生成的（见 partialPath），
// 这是断点续传的前提：重试时能找回上次的临时文件，不必从头再下。
//
// 代价是同一次任务被并发触发时（批量任务重复入队、用户连点、定时调度与手动
// 下载撞车），两条 goroutine 会**同时往同一个 .part 里写**，字节交错。
// FLAC 的 frame CRC / MD5 能把坏流挡下来，坏掉的 MP3 则会被当成好文件入库。
//
// 所以按 .part 路径加一把进程内互斥：后来者等前一个走完再自己写，
// 而不是并行写同一份字节流。
var partLocks = struct {
	mu sync.Mutex
	m  map[string]*partLock
}{m: make(map[string]*partLock)}

type partLock struct {
	mu   sync.Mutex
	refs int
}

// lockPart 返回解锁函数。引用计数保证用完就把条目摘掉，不让这个 map 无界增长
// （用户可以连续下几万首，钥匙不能只进不出）。
func lockPart(path string) func() {
	partLocks.mu.Lock()
	l := partLocks.m[path]
	if l == nil {
		l = &partLock{}
		partLocks.m[path] = l
	}
	l.refs++
	partLocks.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		partLocks.mu.Lock()
		l.refs--
		if l.refs <= 0 {
			delete(partLocks.m, path)
		}
		partLocks.mu.Unlock()
	}
}

// installFileExclusive 把临时文件排他地装到目标路径。
//
// 为什么不能直接用 os.Rename：allocateUniquePath 与实际落地之间有一段窗口，
// 期间别人（另一个下载任务、整理功能、用户自己拷进来的文件）可能占住这个
// 名字，而 rename 会**静默覆盖**它 —— 整理与下载撞在一起时就等于丢别人的歌。
// link(2) 在目标已存在时原子失败（EEXIST），正好是我们需要的语义。
//
// 返回 os.ErrExist 表示目标已被占用（调用方应该换个名字重试），
// 其余错误才是真失败。
func installFileExclusive(tmpPath, finalPath string) error {
	err := os.Link(tmpPath, finalPath)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return os.ErrExist
	}
	// 兜底：有些文件系统不支持硬链接（部分 FUSE / 网络盘 / 跨卷）。
	// 退化成「先探存在、再 rename」，窗口小得多，而且只在 link 不可用时才走。
	if _, statErr := os.Lstat(finalPath); statErr == nil {
		return os.ErrExist
	}
	return os.Rename(tmpPath, finalPath)
}
