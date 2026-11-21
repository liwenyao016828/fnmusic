// Package fslock 提供「曲库文件操作」的进程级读写闸门。
//
// 为什么需要它：同一时刻可能有两类人在动同一批文件 ——
//   - 下载器：往曲库目录里写音频（含 .part 临时文件、最后 link 到目标名）；
//   - 整理（organize）：把已经落盘的文件挪到「歌手/专辑」目录里。
//
// 两者撞在一起会出真事故：整理刚确认「目标名是空的」，下载正好在这一刻把
// 同名文件 link 上去，整理接着把它当自己的方案搬走 —— 用户看到的是「下载完了
// 但歌不在，或者跑到别的歌手目录里」（2026-11-20 审计 M8，上游用一把
// `_LIBRARY_FILE_LOCK` 做同样的事）。
//
// 语义刻意不对称：
//   - 搬运（整理）是**排他**的：它要保证「预演时看到的盘面」在执行期间不变；
//   - 写入（下载）是**共享**的：多首并行下载互不影响，只有整理会让路。
//
// 整理侧用 TryExclusive + 超时而不是死等：死等会让界面看起来卡死，
// 报「有下载在进行，稍后再整理」更好懂。
package fslock

import (
	"errors"
	"sync"
	"time"
)

// ErrBusy 表示闸门正被占用（有下载在进行），调用方应当稍后重试。
var ErrBusy = errors.New("曲库正在被下载任务占用")

var (
	mu          sync.RWMutex
	exclusiveOn bool
)

// Shared 取得共享访问权（下载用）。返回的 release 必须调用。
//
// 会**等待**排他的整理结束：等几百毫秒/几秒，比让两方同时动盘好得多。
func Shared() (release func()) {
	mu.RLock()
	return mu.RUnlock
}

// TryExclusive 尝试取得排他访问权（整理用），最多等 wait。
//
// 拿到返回 release；等不到返回 ErrBusy —— 调用方应当告诉用户「有下载在跑」，
// 而不是把界面卡在那里。
func TryExclusive(wait time.Duration) (release func(), err error) {
	deadline := time.Now().Add(wait)
	for {
		if mu.TryLock() {
			exclusiveOn = true
			return func() {
				exclusiveOn = false
				mu.Unlock()
			}, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}
