package nas

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/fnos"
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

// ── 授权路径并集（管理员共享 + 系统注入的环境变量） ────────────────────────
//
// 用户最常见的抱怨是「我在设置里授权了目录，曲率还是看不到」。
// 这条路径以前有三个坑，本组测试逐个钉住：
//   1. TRIM_DATA_ACCESSIBLE_PATHS 完全没被读；
//   2. 共享授权取失败时把整份 roots 清掉；
//   3. 缓存不失效，用户授权完目录后曲率一直看不到。
//
// 2026-10-31 起「当前用户个人授权」这一路已从实现里去掉（本应用只给管理员自己用，
// 授权入口统一为管理员共享授权，见 pathguard.go 里 fnosAuthorizedRoots 的注释）。
// 原来钉「个人授权并入并集 / 个人授权失败不影响共享」的两条用例随之失去意义，
// 这里按新契约改写：共享授权照常生效，个人授权不再参与。

// rootsContains 断言 roots 里**包含** want，且每一项都按相对次序出现。
//
// 不写成「完全相等」是刻意的：AllowedRoots() 还会并入本机的卷挂载点
// （getAvailableVolumes），远端 NAS 上真实存在 /media、/mnt，而开发机上没有。
// 这组用例要钉的是「授权目录确实进来了、顺序不被打乱」，不是机器上有几个挂载点。
// 写成完全相等只会让测试换个机器就红，且红得与授权逻辑毫无关系。
func rootsContains(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("AllowedRoots() = %v，缺少授权目录或次序被打乱，期望依次包含 %v", got, want)
	}
}

// swapEnvRootsSource 替换「系统注入的授权路径环境变量」数据源。
func swapEnvRootsSource(t *testing.T, fn func() []string) {
	t.Helper()
	old := fnosEnvRootsSource
	fnosEnvRootsSource = fn
	InvalidateFnOSRootsCache()
	t.Cleanup(func() {
		fnosEnvRootsSource = old
		InvalidateFnOSRootsCache()
	})
}

// 共享授权与系统注入的授权路径要能同时生效：以前只要共享授权非空就提前 return，
// 环境变量那一路等于从来没被读到，用户在设置里授权的目录也就「看不到」。
func TestAllowedRootsMergesSharedAndEnvRoots(t *testing.T) {
	base := t.TempDir()
	shared := filepath.Join(base, "shared")
	fromEnv := filepath.Join(base, "fromEnv")
	for _, d := range []string{shared, fromEnv} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	swapRootsSource(t, func() ([]string, error) { return []string{shared}, nil })
	swapEnvRootsSource(t, func() []string { return []string{fromEnv} })

	got := AllowedRoots()
	rootsContains(t, got, []string{realPath(t, shared), realPath(t, fromEnv)})
}

func TestAllowedRootsIncludesDataAccessiblePathsEnv(t *testing.T) {
	base := t.TempDir()
	granted := filepath.Join(base, "granted")
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string {
		// 真实实现读的是 TRIM_DATA_ACCESSIBLE_PATHS；这里直接替换数据源，
		// 同时断言真实解析函数能处理冒号分隔与空项。
		return fnos.DataAccessiblePathsForTest(granted + "::" + base + "/missing")
	})

	got := AllowedRoots()
	rootsContains(t, got, []string{realPath(t, granted)})
	for _, g := range got {
		if strings.HasSuffix(g, "/missing") {
			t.Fatalf("不存在的授权项必须被忽略，实际 AllowedRoots() = %v", got)
		}
	}
}

// 任何一路授权来源失败都不能把整份 roots 清掉 —— 否则用户看到的是「越权」，
// 而真因（接口不可用）一个字都没留下。这里让共享授权那一路失败，
// 验证系统注入的授权路径仍然可用，且失败留了带原因的日志。
func TestAllowedRootsSurvivesSharedRootsFailure(t *testing.T) {
	applog.Default().Clear()
	base := t.TempDir()
	granted := filepath.Join(base, "granted")
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	swapRootsSource(t, func() ([]string, error) {
		return nil, errors.New("令牌失效")
	})
	swapEnvRootsSource(t, func() []string { return []string{granted} })

	got := AllowedRoots()
	rootsContains(t, got, []string{realPath(t, granted)})
	found := false
	for _, e := range applog.Default().Recent(50) {
		if strings.Contains(e.Message, "读取飞牛共享目录失败") && strings.Contains(e.Message, "令牌失效") {
			found = true
		}
	}
	if !found {
		t.Fatalf("授权目录读取失败必须留带原因的日志，实际：%+v", applog.Default().Recent(5))
	}
}

func TestInvalidateFnOSRootsCacheForcesRefetch(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	current := []string{first}
	swapRootsSource(t, func() ([]string, error) { return current, nil })

	rootsContains(t, AllowedRoots(), []string{realPath(t, first)})
	current = []string{second}
	AllowedRoots()
	InvalidateFnOSRootsCache()
	// 只断言「失效缓存后能看到新目录」，不断言「失效前一定看不到」：
	// 共享授权还有一层 30 秒 TTL + 后台过期刷新，缓存可能已自然过期，
	// 把「必须还是旧值」写进断言会制造与本次行为无关的偶发失败。
	rootsContains(t, AllowedRoots(), []string{realPath(t, second)})
}
