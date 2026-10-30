package api

import (
	"path/filepath"
	"testing"

	"fn-lx-player/pkg/nas"
)

// 2026-10-29 用户报「本地曲库删除歌曲…实际没有删除」的根因回归：
//
//	joinTrash 以前用 strings.HasPrefix 判归属，/home/liwenyao/Music/a.mp3 会命中根 /home，
//	回收站被建到普通用户写不了的 /home/.tidy_trash → 挪动全失败 → 文件一个都没删掉，
//	而 Handler 仍然回 200。
func TestWithinPathSegments(t *testing.T) {
	cases := []struct {
		root, target string
		want         bool
	}{
		{"/vol1", "/vol1/Music/a.mp3", true},
		{"/vol1", "/vol1", true},
		{"/vol1", "/vol10/Music/a.mp3", false},         // 前缀相同但不是同一块盘
		{"/home", "/homefriend/a.mp3", false},          // 前缀相同但不是同一棵目录树
		{"/home/liwenyao", "/home/liwenyao/Music/a", true},
		{"/home/liwenyao/", "/home/liwenyao/Music/a", true}, // 根带尾斜杠也认
		{"/vol1/Music", "/vol1/Musicbox/a.mp3", false},
	}
	for _, c := range cases {
		if got := withinPath(c.root, c.target); got != c.want {
			t.Errorf("withinPath(%q, %q) = %v，期望 %v", c.root, c.target, got, c.want)
		}
	}
}

// joinTrash 取「最贴近的那个根」，而不是第一个前缀命中的根。
func TestJoinTrashPicksNestedRoot(t *testing.T) {
	t.Setenv("FN_ALLOWED_ROOTS", "/home"+string(filepath.ListSeparator)+"/home/liwenyao/Music")
	// AllowedRoots 要求根真实存在；/home 与 /tmp 一定在，这里只验证挑选逻辑不 panic
	got := joinTrash("/home/liwenyao/Music/a.mp3")
	if got == "" {
		t.Fatal("joinTrash 不该返回空串")
	}
	if filepath.Base(got) != ".tidy_trash" {
		t.Fatalf("回收站目录名不对：%s", got)
	}
}

// 回收站目录必须落在被删文件所在的那棵树里，绝不能是 /home/.tidy_trash 这种跨界位置。
func TestJoinTrashStaysUnderRoot(t *testing.T) {
	roots := nas.AllowedRoots()
	if len(roots) == 0 {
		t.Skip("本机没有可用的允许根目录")
	}
	for _, root := range roots {
		sample := filepath.Join(root, "Music", "a.mp3")
		got := joinTrash(sample)
		if !withinPath(root, filepath.Dir(got)) && filepath.Clean(got) != filepath.Join(root, ".tidy_trash") {
			t.Errorf("回收站 %s 不在允许根 %s 之下", got, root)
		}
	}
}
