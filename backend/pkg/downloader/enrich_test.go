package downloader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 「只补标签」的路径守卫。
//
// 这个接口会**写盘**，而路径来自调用方 —— 不做检查它就是一个「往任意路径写标签」
// 的入口。用例守的就是这条边界。
func TestEnrichExistingRefusesPathOutsideDownloadDir(t *testing.T) {
	dir := t.TempDir()
	d := newTestDownloader(t, dir)

	outside := filepath.Join(t.TempDir(), "victim.mp3")
	if err := os.WriteFile(outside, []byte("ID3fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnrichExisting(SongPayload{Name: "甲", Singer: "乙"}, outside); err == nil {
		t.Fatal("⚠️ 下载目录之外的文件必须拒绝")
	}

	// 目录穿越也不行
	traverse := filepath.Join(dir, "..", filepath.Base(outside))
	if _, err := d.EnrichExisting(SongPayload{Name: "甲", Singer: "乙"}, traverse); err == nil {
		t.Fatal("⚠️ `..` 穿越必须拒绝")
	}
}

func TestEnrichExistingRejectsMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	d := newTestDownloader(t, dir)
	if _, err := d.EnrichExisting(SongPayload{}, ""); err == nil {
		t.Fatal("空路径必须拒绝")
	}
	if _, err := d.EnrichExisting(SongPayload{}, filepath.Join(dir, "不存在.mp3")); err == nil {
		t.Fatal("文件不存在必须拒绝")
	}
	empty := filepath.Join(dir, "empty.mp3")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnrichExisting(SongPayload{}, empty); err == nil {
		t.Fatal("空文件必须拒绝（它是失败下载留下的残骸）")
	}
}

// 目录内的真文件：守卫必须放行（否则 tee 补标签永远不生效）。
func TestEnrichExistingAllowsFileInsideDownloadDir(t *testing.T) {
	dir := t.TempDir()
	d := newTestDownloader(t, dir)
	p := filepath.Join(dir, "甲 - 乙.mp3")
	if err := os.WriteFile(p, []byte("ID3\x03\x00\x00\x00FAKEAUDIO"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 关掉歌词与封面 → applyTags 不抓外部资源，走纯本地路径
	no := false
	_, err := d.EnrichExisting(SongPayload{Name: "甲", Singer: "乙", EmbedCover: &no}, p)
	if err != nil && !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("目录内的真文件该放行：%v", err)
	}
}
