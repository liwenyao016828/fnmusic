package tags

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// 读回文件头里的 ID3 标签体（测试用）。
func readTagBody(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开文件失败: %v", err)
	}
	defer f.Close()
	head := make([]byte, id3HeaderSize)
	if _, err := io.ReadFull(f, head); err != nil {
		t.Fatalf("读标签头失败: %v", err)
	}
	if !bytes.Equal(head[:3], []byte("ID3")) {
		t.Fatalf("文件头不是 ID3 标签")
	}
	body := make([]byte, unsynchsafe(head[6:10]))
	if _, err := io.ReadFull(f, body); err != nil {
		t.Fatalf("读标签体失败: %v", err)
	}
	return body
}

// 旧标签里的**非受管帧**（TPE2 专辑艺术家 / TCOM 作曲 / COMM 备注）在写新标签时
// 必须原样保留 —— 原来的策略是丢弃整个旧 ID3、只重建受管帧，等于每次改歌名/
// 歌词都静默清掉作曲与专辑艺术家，事后无法恢复（审计 M9）。
func TestMP3KeepsUnmanagedID3Frames(t *testing.T) {
	oldFrames := bytes.Join([][]byte{
		id3Frame("TIT2", textFrameBody("旧歌名")),
		id3Frame("TPE2", textFrameBody("专辑艺术家")),
		id3Frame("TCOM", textFrameBody("作曲人")),
	}, nil)
	path := writeTemp(t, "keep.mp3", append(wrapID3v2Tag(oldFrames), mp3Fixture()...))

	if _, err := Write(path, Metadata{Title: "新歌名", Artist: "新歌手"}); err != nil {
		t.Fatalf("写标签失败: %v", err)
	}

	body := string(readTagBody(t, path))
	for _, id := range []string{"TPE2", "TCOM"} {
		if !strings.Contains(body, id) {
			t.Errorf("非受管帧 %s 被丢掉了", id)
		}
	}
	// 受管帧要按新值重建，不能留着旧的
	if strings.Contains(body, "旧歌名") {
		t.Errorf("受管帧 TIT2 没被更新（旧值还在）")
	}

	meta, err := Read(path)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if meta.Title != "新歌名" {
		t.Errorf("歌名 = %q", meta.Title)
	}

	// 音频数据不能被标签重写吃掉
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(mp3AudioMarker)) {
		t.Errorf("音频负载丢了")
	}
}

// 没有旧标签、或旧标签是 v2.2 / 带 unsynchronisation 时，也要能正常写入
// （不能因为「迁移不了」就把文件写坏）。
func TestMP3WriteStillWorksWithoutPreservableTag(t *testing.T) {
	path := writeTemp(t, "plain.mp3", mp3Fixture())
	if _, err := Write(path, Metadata{Title: "歌名", Artist: "歌手"}); err != nil {
		t.Fatalf("写标签失败: %v", err)
	}
	meta, err := Read(path)
	if err != nil || meta.Title != "歌名" {
		t.Fatalf("回读失败: %v %+v", err, meta)
	}
}
