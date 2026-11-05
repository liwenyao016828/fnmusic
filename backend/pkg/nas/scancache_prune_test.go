package nas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组用例盯的是「扫描缓存里的幽灵行」：
// 缓存 TTL 72 小时，期间文件被删（下载审计清理、用户手动删歌）缓存不会知道，
// 2026-11-04 用户报「本地曲库出现不该有的歌」——列表里挂着磁盘上早已不存在的文件。
// 命中缓存必须逐行对账磁盘：丢掉不存在的行、total 跟着变；整份都是幽灵行时作废重扫。

func TestScanSongsCacheDropsDeletedFiles(t *testing.T) {
	root := realPath(t, t.TempDir())
	setAllowedRoots(t, root)

	mkAudio(t, filepath.Join(root, "keep.mp3"), 4)
	gone := mkAudio(t, filepath.Join(root, "gone.mp3"), 4)

	InitScanCache(t.TempDir())
	t.Cleanup(func() { WaitScanCacheFlush(); InitScanCache("") })

	// 首次：缓存未命中，走完整枚举并落缓存
	first := scanSongs(t, root, false)
	if first.Cached || len(first.Songs) != 2 {
		t.Fatalf("首次应未命中缓存且扫到 2 首，实际 cached=%v %d 首", first.Cached, len(first.Songs))
	}

	// 外部删掉一首（缓存不知情）
	if err := os.Remove(gone); err != nil {
		t.Fatalf("删除 %s 失败：%v", gone, err)
	}

	// 命中缓存时幽灵行必须消失，total 必须同步（以前 cached.Total 还是 2）
	hit := scanSongs(t, root, false)
	if !hit.Cached {
		t.Fatal("第二次应命中缓存")
	}
	if got := songPaths(hit.Songs); len(got) != 1 || !strings.HasSuffix(got[0], "keep.mp3") {
		t.Fatalf("缓存命中只应返回磁盘上还在的 keep.mp3，实际：%v", got)
	}
	if hit.Total != 1 {
		t.Errorf("total = %d，应为 1（幽灵行要从计数里扣掉）", hit.Total)
	}

	// 瘦身后的缓存已回写：再查一次仍然是干净的一行，不会每轮都漏出幽灵行
	again := scanSongs(t, root, false)
	if !again.Cached || len(again.Songs) != 1 || again.Total != 1 {
		t.Fatalf("回写后的缓存应稳定返回 1 首，实际 cached=%v %d 首 total=%d",
			again.Cached, len(again.Songs), again.Total)
	}
}

func TestScanSongsCacheAllGoneInvalidatesAndRescans(t *testing.T) {
	root := realPath(t, t.TempDir())
	setAllowedRoots(t, root)

	old := mkAudio(t, filepath.Join(root, "old.mp3"), 4)

	InitScanCache(t.TempDir())
	t.Cleanup(func() { WaitScanCacheFlush(); InitScanCache("") })

	if first := scanSongs(t, root, false); first.Cached || len(first.Songs) != 1 {
		t.Fatalf("首次应扫到 1 首 old.mp3，实际 cached=%v %d 首", first.Cached, len(first.Songs))
	}

	// 目录整个换了内容：老文件删掉、新文件放进来
	if err := os.Remove(old); err != nil {
		t.Fatalf("删除 %s 失败：%v", old, err)
	}
	mkAudio(t, filepath.Join(root, "new.mp3"), 4)

	// 缓存里的行全部是幽灵行时不能拿空列表糊弄，必须当场重扫
	resp := scanSongs(t, root, false)
	if resp.Cached {
		t.Error("缓存整份作废后应重扫而不是返回空缓存")
	}
	if got := songPaths(resp.Songs); len(got) != 1 || !strings.HasSuffix(got[0], "new.mp3") {
		t.Fatalf("重扫应返回磁盘上真实的 new.mp3，实际：%v", got)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d，应为 1", resp.Total)
	}
}
