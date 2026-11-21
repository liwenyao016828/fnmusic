package organize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组用例盯的是「专辑封面跟着专辑目录一起搬」（associateDirCovers）。
//
// 上游 organize.py 的做法：目录里的 cover.jpg / folder.jpg 属于**整个目录**
// 而不是某一首歌；只有当这个目录里的歌全部被搬走、而且它们属于同一个专辑时，
// 才知道这张封面归谁 —— 这时候把它复制到目标专辑目录，**保持原文件名**
// （cover.jpg 还是 cover.jpg，一个专辑一张封面）。

// dirCoverAlbum 造一个「专辑目录里有一张 cover.jpg」的目录。
func dirCoverAlbum(t *testing.T, root, album string, songs ...string) (audioPaths []string, coverPath string) {
	t.Helper()
	dir := filepath.Join(root, album)
	coverPath = filepath.Join(dir, "cover.jpg")
	writeTestFile(t, coverPath, "fake-cover")
	for _, s := range songs {
		p := filepath.Join(dir, s+".flac")
		writeTestFile(t, p, "fake-flac")
		audioPaths = append(audioPaths, p)
	}
	return audioPaths, coverPath
}

func TestDirCoverMovesWithAlbum(t *testing.T) {
	root := t.TempDir()
	audio, cover := dirCoverAlbum(t, root, "散放", "甲", "乙")

	lookup := func(p string) (Meta, bool) {
		switch p {
		case audio[0]:
			return Meta{Title: "甲", Artist: "歌手", Album: "专辑", Track: 1}, true
		case audio[1]:
			return Meta{Title: "乙", Artist: "歌手", Album: "专辑", Track: 2}, true
		}
		return Meta{}, false
	}

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Move != 2 {
		t.Fatalf("两首都该搬，得到 %+v", plan.Stats)
	}

	// 封面要出现在某一条的 Moves 里（跟着第一首走）。
	wantCover := filepath.Join(root, "歌手", "专辑", "cover.jpg")
	found := false
	for _, item := range plan.Items {
		for _, mv := range item.Moves {
			if mv.Kind == "albumcover" {
				found = true
				if mv.From != cover {
					t.Errorf("封面来源不对：%s", mv.From)
				}
				if mv.To != wantCover {
					t.Errorf("封面目标不对：%s（期望 %s）", mv.To, wantCover)
				}
			}
		}
	}
	if !found {
		t.Fatal("专辑封面应当跟着搬")
	}

	res, err := Apply(plan)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Failed != 0 {
		t.Fatalf("执行失败：%+v", res.Errors)
	}
	if !pathExists(wantCover) {
		t.Errorf("目标专辑目录里应当有 cover.jpg：%s", wantCover)
	}
	if pathExists(cover) {
		t.Error("原位置的封面应当被搬走")
	}
}

func TestDirCoverKeepsWhenAlbumUnclear(t *testing.T) {
	root := t.TempDir()
	// 同一目录里两首歌分属不同专辑 → 封面归属不明，保留原样。
	dir := filepath.Join(root, "散放")
	a1 := filepath.Join(dir, "甲.flac")
	a2 := filepath.Join(dir, "乙.flac")
	writeTestFile(t, a1, "f")
	writeTestFile(t, a2, "f")
	cover := filepath.Join(dir, "cover.jpg")
	writeTestFile(t, cover, "fake-cover")

	lookup := func(p string) (Meta, bool) {
		switch p {
		case a1:
			return Meta{Title: "甲", Artist: "歌手", Album: "专辑一", Track: 1}, true
		case a2:
			return Meta{Title: "乙", Artist: "歌手", Album: "专辑二", Track: 1}, true
		}
		return Meta{}, false
	}

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	for _, item := range plan.Items {
		for _, mv := range item.Moves {
			if mv.Kind == "albumcover" {
				t.Fatalf("归属不明时不该搬封面：%+v", mv)
			}
		}
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "封面归属不明") {
		t.Errorf("方案里应当说明封面被跳过：%v", plan.Warnings)
	}
	if !pathExists(cover) {
		t.Error("原位置的封面应当保留")
	}
}

func TestDirCoverKeepsWhenTargetHasDifferentImage(t *testing.T) {
	root := t.TempDir()
	audio, _ := dirCoverAlbum(t, root, "散放", "甲")

	// 目标专辑目录里已经有一张**内容不同**的 cover.jpg。
	destDir := filepath.Join(root, "歌手", "专辑")
	writeTestFile(t, filepath.Join(destDir, "cover.jpg"), "another-image")

	lookup := func(p string) (Meta, bool) {
		if p == audio[0] {
			return Meta{Title: "甲", Artist: "歌手", Album: "专辑", Track: 1}, true
		}
		return Meta{}, false
	}

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	for _, item := range plan.Items {
		for _, mv := range item.Moves {
			if mv.Kind == "albumcover" {
				t.Fatalf("目标已有不同封面时不该覆盖：%+v", mv)
			}
		}
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "保留原图") {
		t.Errorf("方案里应当说明封面被保留：%v", plan.Warnings)
	}
}

func TestDirCoverAndTrackCoverBothMove(t *testing.T) {
	root := t.TempDir()
	// 目录里有 cover.jpg（专辑封面），同时这一首还自带 甲.jpg（它自己的封面）。
	// 两者不是一回事：前者进专辑目录当 cover.jpg，后者跟着歌名走。
	dir := filepath.Join(root, "散放")
	a1 := filepath.Join(dir, "甲.flac")
	writeTestFile(t, a1, "f")
	ownCover := filepath.Join(dir, "甲.jpg")
	writeTestFile(t, ownCover, "own-cover")
	dirCover := filepath.Join(dir, "cover.jpg")
	writeTestFile(t, dirCover, "dir-cover")

	lookup := func(p string) (Meta, bool) {
		if p == a1 {
			return Meta{Title: "甲", Artist: "歌手", Album: "专辑", Track: 1}, true
		}
		return Meta{}, false
	}

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	var sawOwn, sawAlbum bool
	for _, item := range plan.Items {
		for _, mv := range item.Moves {
			switch mv.Kind {
			case "cover":
				sawOwn = true
				if filepath.Base(mv.To) != "01 甲.jpg" {
					t.Errorf("曲目自带的封面应当跟着歌名走：%s", mv.To)
				}
			case "albumcover":
				sawAlbum = true
				if filepath.Base(mv.To) != "cover.jpg" {
					t.Errorf("专辑封面应当保持原名：%s", mv.To)
				}
			}
		}
	}
	if !sawOwn {
		t.Error("曲目自带的封面应当一起搬")
	}
	if !sawAlbum {
		t.Error("目录里的专辑封面也应当搬")
	}
}

func TestSameFileBytes(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.jpg")
	b := filepath.Join(root, "b.jpg")
	c := filepath.Join(root, "c.jpg")
	writeTestFile(t, a, "same")
	writeTestFile(t, b, "same")
	writeTestFile(t, c, "different")
	if !sameFileBytes(a, b) {
		t.Error("内容相同的两个文件应判为一致")
	}
	if sameFileBytes(a, c) {
		t.Error("内容不同的两个文件不该判为一致")
	}
	if sameFileBytes(a, filepath.Join(root, "nope.jpg")) {
		t.Error("不存在的文件不该判为一致")
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("测试文件应当存在：%v", err)
	}
}
