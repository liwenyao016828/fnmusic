package nas

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/applog"
)

// fnosSharedRoots 的失败以前是静默的：roots 变 nil，之后所有飞牛目录都被判成
// 「不在允许访问的目录范围内」，用户看到的是越权提示，真因（接口不可用 / 令牌失效）
// 一个字都没有。见 HANDOVER §11 ㊼ 块九。

func resetFnosRootsCache() {
	fnosRootsCache.Lock()
	fnosRootsCache.at = time.Time{}
	fnosRootsCache.roots = nil
	fnosRootsCache.Unlock()
}

func swapRootsSource(t *testing.T, fn func() ([]string, error)) {
	t.Helper()
	old := fnosRootsSource
	fnosRootsSource = fn
	resetFnosRootsCache()
	t.Cleanup(func() {
		fnosRootsSource = old
		resetFnosRootsCache()
	})
}

func TestSharedRootsFailureIsLogged(t *testing.T) {
	applog.Default().Clear()
	swapRootsSource(t, func() ([]string, error) {
		return nil, errors.New("令牌失效")
	})

	if got := fnosSharedRoots(); got != nil {
		t.Fatalf("取失败应当当作没有共享目录（回 nil），实际 %v", got)
	}
	found := false
	for _, e := range applog.Default().Recent(50) {
		if strings.Contains(e.Message, "读取飞牛共享目录失败") && strings.Contains(e.Message, "令牌失效") {
			found = true
		}
	}
	if !found {
		t.Fatalf("失败必须留一条带原因的日志，实际：%+v", applog.Default().Recent(5))
	}
}

func TestSharedRootsSuccessIsReturnedAndCached(t *testing.T) {
	applog.Default().Clear()
	calls := 0
	swapRootsSource(t, func() ([]string, error) {
		calls++
		return []string{"/vol3/1000/音乐"}, nil
	})

	first := fnosSharedRoots()
	if len(first) != 1 || first[0] != "/vol3/1000/音乐" {
		t.Fatalf("应返回共享目录，实际 %v", first)
	}
	second := fnosSharedRoots()
	if len(second) != 1 {
		t.Fatalf("第二次应命中缓存，实际 %v", second)
	}
	if calls != 1 {
		t.Fatalf("30 秒内应只拉一次（缓存生效），实际拉了 %d 次", calls)
	}
	for _, e := range applog.Default().Recent(50) {
		if strings.Contains(e.Message, "读取飞牛共享目录失败") {
			t.Fatalf("成功路径不该记失败日志：%s", e.Message)
		}
	}
}

// 飞牛不可用（没令牌 / 没 socket）是正常情况：既不记日志，也不进缓存以外的分支。
func TestSharedRootsUnavailableIsQuiet(t *testing.T) {
	applog.Default().Clear()
	swapRootsSource(t, func() ([]string, error) { return nil, nil })

	if got := fnosSharedRoots(); got != nil {
		t.Fatalf("不可用时应回 nil，实际 %v", got)
	}
	if entries := applog.Default().Recent(50); len(entries) != 0 {
		t.Fatalf("不可用是正常情况，不该有日志：%+v", entries)
	}
}
