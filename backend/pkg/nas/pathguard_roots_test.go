package nas

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// ── 授权路径并集（管理员共享授权 + 系统注入的环境变量） ────────────────────
//
// 2026-10-31：本应用只给管理员自己用，授权入口统一为管理员共享授权，
// 已移除「当前用户个人授权」（trim.file.getUserAccessibleFolders / pickUserFile）。
// 这组测试钉住剩下的两路，并确认系统注入路径没被漏读。

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

func TestAllowedRootsIncludesDataAccessiblePathsEnv(t *testing.T) {
	base := t.TempDir()
	granted := filepath.Join(base, "granted")
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string {
		return fnos.DataAccessiblePathsForTest(granted + "::" + base + "/missing")
	})

	got := AllowedRoots()
	if len(got) == 0 || got[0] != realPath(t, granted) {
		t.Fatalf("AllowedRoots() = %v，期望首项是已存在的 %q", got, realPath(t, granted))
	}
	for _, r := range got {
		if strings.HasSuffix(r, "/missing") {
			t.Fatalf("不存在的授权路径不该进列表：%v", got)
		}
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

	if got := AllowedRoots(); len(got) == 0 || got[0] != realPath(t, first) {
		t.Fatalf("首次 AllowedRoots() = %v", got)
	}
	current = []string{second}
	AllowedRoots()
	InvalidateFnOSRootsCache()
	got := AllowedRoots()
	if len(got) == 0 || got[0] != realPath(t, second) {
		t.Fatalf("失效缓存后应看到新授权目录，AllowedRoots() = %v", got)
	}
}

// ── 飞牛环境下「只认授权目录」（2026-10-31 按官方权限模型重排优先级） ────────
//
// 官方模型是「没授权 = 看不见」。以前 AllowedRoots() 总会把探测到的 /vol1..16
// 拼在后面，等于绕过用户在应用设置里的授权动作；更糟的是「只授权 /vol1/1000/Music」
// 会退化成「整个 /vol1 可读」。这组测试钉住三条分支。

// swapFnosDetected 钉住「是不是飞牛环境」这个分支。
//
// 注意这里替换的是 fnosDetectedSource（判据与 token 无关），**不是** fnos.Available()。
// 2026-10-31 的真机故障就是「在飞牛里但没拿到 TRIM_API_TOKEN」被判成非飞牛，
// 于是并入了探测卷；用 Detected 才能把这条分支钉对。
func swapFnosDetected(t *testing.T, v bool) {
	t.Helper()
	old := fnosDetectedSource
	fnosDetectedSource = func() bool { return v }
	InvalidateFnOSRootsCache()
	t.Cleanup(func() {
		fnosDetectedSource = old
		InvalidateFnOSRootsCache()
	})
}

// unsetAllowedRootsEnv 清掉 FN_ALLOWED_ROOTS 并在用例结束恢复原值。
// 不能用 t.Setenv(allowedRootsEnv, "")：空串在 AllowedRoots 里也算「已设置」，
// 会走进显式覆盖分支，与「没设置」是两件事。
func unsetAllowedRootsEnv(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv(allowedRootsEnv)
	if err := os.Unsetenv(allowedRootsEnv); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(allowedRootsEnv, old)
		} else {
			os.Unsetenv(allowedRootsEnv)
		}
	})
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
}

func TestAllowedRootsFnosEnvWithoutGrantIsEmpty(t *testing.T) {
	swapFnosDetected(t, true)
	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string { return nil })
	unsetAllowedRootsEnv(t)

	// 飞牛里一个授权都没有时，必须回空（fail closed），而不是拿 /vol1 顶上。
	if got := AllowedRoots(); len(got) != 0 {
		t.Fatalf("飞牛环境下未授权时 AllowedRoots() 应为空，实际 %v", got)
	}
}

func TestAllowedRootsFnosEnvIgnoresProbeVolumes(t *testing.T) {
	base := t.TempDir()
	granted := filepath.Join(base, "Music")
	mustMkdir(t, granted)

	swapFnosDetected(t, true)
	swapRootsSource(t, func() ([]string, error) { return []string{granted}, nil })
	swapEnvRootsSource(t, func() []string { return nil })
	unsetAllowedRootsEnv(t)

	got := AllowedRoots()
	want := []string{realPath(t, granted)}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("飞牛环境下 AllowedRoots() 应只含授权目录 %v，实际 %v", want, got)
	}
	// 探测卷（/vol1、/media…）一律不许出现：授权一个子目录不能变成整卷可读。
	probes := getAvailableVolumes()
	for _, r := range got {
		for _, p := range probes {
			if r == p {
				t.Fatalf("飞牛环境下不该混入探测卷 %q：%v", p, got)
			}
		}
	}
}

func TestAllowedRootsNonFnosEnvFallsBackToProbes(t *testing.T) {
	swapFnosDetected(t, false)
	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string { return nil })
	unsetAllowedRootsEnv(t)

	// 开发机 / 非飞牛 Linux 要还能选目录：退回探测卷。
	got := AllowedRoots()
	probes := getAvailableVolumes()
	if len(probes) == 0 {
		t.Skip("这台机器探不到 /vol*、/media、/mnt，跳过")
	}
	if len(got) == 0 {
		t.Fatalf("非飞牛环境应退回探测卷 %v，实际空", probes)
	}
}

// FN_ALLOWED_ROOTS 写了就按写的来（含飞牛授权目录），不管是不是飞牛环境。
func TestAllowedRootsEnvWinsEvenInFnosEnv(t *testing.T) {
	base := t.TempDir()
	granted := filepath.Join(base, "granted")
	manual := filepath.Join(base, "manual")
	mustMkdir(t, granted, manual)

	swapFnosDetected(t, true)
	swapRootsSource(t, func() ([]string, error) { return []string{granted}, nil })
	swapEnvRootsSource(t, func() []string { return nil })
	t.Setenv(allowedRootsEnv, manual)

	got := AllowedRoots()
	want := []string{realPath(t, granted), realPath(t, manual)}
	if len(got) != len(want) {
		t.Fatalf("AllowedRoots() = %v，want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedRoots()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// FnOSAuthorizedRoots 是「授权状态」的判据：它不含探测卷，所以管理员把整个 /vol1
// 授权给你时，也会被认成「已授权」，不会被当年「剔掉内置卷前缀」的口径吃掉。
func TestFnOSAuthorizedRootsExcludesProbeVolumes(t *testing.T) {
	base := t.TempDir()
	whole := filepath.Join(base, "vol1") // 模拟「管理员授权了整个卷」
	mustMkdir(t, whole)

	swapRootsSource(t, func() ([]string, error) { return []string{whole}, nil })
	swapEnvRootsSource(t, func() []string { return nil })

	got := FnOSAuthorizedRoots()
	if len(got) != 1 || got[0] != realPath(t, whole) {
		t.Fatalf("FnOSAuthorizedRoots() = %v，期望 [%q]", got, realPath(t, whole))
	}
}

// 2026-10-31 起个人授权不再参与：哪怕环境里塞了 TRIM_UID、飞牛那边也有用户授权，
// 允许根目录也只来自管理员共享授权 + 系统注入路径。
// 这里用一个「被删掉的查询」无法伪造，所以改为断言 FnOSAuthorizedRoots 的内容
// 完全由共享授权与 env 两路决定（换 uid 不影响结果）。
func TestFnOSAuthorizedRootsIgnoresCurrentUserUID(t *testing.T) {
	base := t.TempDir()
	shared := filepath.Join(base, "shared")
	mustMkdir(t, shared)

	swapRootsSource(t, func() ([]string, error) { return []string{shared}, nil })
	swapEnvRootsSource(t, func() []string { return nil })

	t.Setenv("TRIM_UID", "1000")
	first := FnOSAuthorizedRoots()
	t.Setenv("TRIM_UID", "1001")
	second := FnOSAuthorizedRoots()

	if len(first) != 1 || first[0] != realPath(t, shared) {
		t.Fatalf("uid=1000 时 FnOSAuthorizedRoots() = %v，期望只有共享授权", first)
	}
	if len(second) != 1 || second[0] != realPath(t, shared) {
		t.Fatalf("uid=1001 时 FnOSAuthorizedRoots() = %v，期望只有共享授权", second)
	}
}

// ── 「用户到底授权了没有」只能用 FnOSGrantedRoots 判断 ────────────────────────
//
// 2026-10-31 真机事故：`config/resource` 的 `data-share: yinshu-ai/data` 让系统注入
// TRIM_DATA_SHARE_PATHS=/vol1/@appshare/yinshu-ai/data。那是曲率**自己**的数据目录，
// 却被 FnOSAuthorizedRoots 算成「已授权」→ auth-status 永远回 authorized=true →
// 前端引导条永远不出现，用户一个音乐目录都没授权却看不到任何提示，只能自己撞 403。

func swapDataShareSource(t *testing.T, fn func() []string) {
	t.Helper()
	old := fnosDataShareSource
	fnosDataShareSource = fn
	InvalidateFnOSRootsCache()
	t.Cleanup(func() {
		fnosDataShareSource = old
		InvalidateFnOSRootsCache()
	})
}

// 只有 data-share 自己的目录时，用户授权集合必须为空。
func TestFnOSGrantedRootsIgnoresDataShareOnly(t *testing.T) {
	base := t.TempDir()
	own := filepath.Join(base, "@appshare", "yinshu-ai", "data")
	mustMkdir(t, own)

	// 系统把 data-share 路径同时注入 TRIM_DATA_SHARE_PATHS（env 那一路会带出它）。
	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string { return []string{own} })
	swapDataShareSource(t, func() []string { return []string{own} })

	if got := FnOSGrantedRoots(); len(got) != 0 {
		t.Fatalf("只有 data-share 自用目录时 FnOSGrantedRoots() = %v，期望空（否则前端永不提示授权）", got)
	}
	// 但路径守卫仍然要放行自己的数据目录，否则曲率写不了配置。
	// （测试机不是飞牛环境，AllowedRoots 会额外并入探测卷 /media、/mnt，所以只断言「包含」。）
	if got := AllowedRoots(); !slices.Contains(got, realPath(t, own)) {
		t.Fatalf("AllowedRoots() = %v，期望仍然包含 data-share 目录 %q", got, realPath(t, own))
	}
	// FnOSAuthorizedRoots 保留旧语义（含 data-share），供「所有能访问的飞牛路径」展示用。
	if got := FnOSAuthorizedRoots(); len(got) != 1 || got[0] != realPath(t, own) {
		t.Fatalf("FnOSAuthorizedRoots() = %v，期望仍包含 data-share 目录 %q", got, realPath(t, own))
	}
}

// data-share + 一个真正授权过的音乐目录：只认后者。
func TestFnOSGrantedRootsKeepsRealGrantsBesideDataShare(t *testing.T) {
	base := t.TempDir()
	own := filepath.Join(base, "@appshare", "yinshu-ai", "data")
	music := filepath.Join(base, "vol3", "1000", "音乐")
	mustMkdir(t, own, music)

	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	swapEnvRootsSource(t, func() []string { return []string{own, music} })
	swapDataShareSource(t, func() []string { return []string{own} })

	got := FnOSGrantedRoots()
	if len(got) != 1 || got[0] != realPath(t, music) {
		t.Fatalf("FnOSGrantedRoots() = %v，期望只有音乐目录 %q", got, realPath(t, music))
	}
}

// 管理员共享授权不需要靠 env 区分：它从来不是 data-share，必须计入。
func TestFnOSGrantedRootsCountsSharedGrant(t *testing.T) {
	base := t.TempDir()
	own := filepath.Join(base, "@appshare", "yinshu-ai", "data")
	shared := filepath.Join(base, "vol1", "1000", "music")
	mustMkdir(t, own, shared)

	swapRootsSource(t, func() ([]string, error) { return []string{shared}, nil })
	swapEnvRootsSource(t, func() []string { return []string{own} })
	swapDataShareSource(t, func() []string { return []string{own} })

	got := FnOSGrantedRoots()
	if len(got) != 1 || got[0] != realPath(t, shared) {
		t.Fatalf("FnOSGrantedRoots() = %v，期望 [%q]", got, realPath(t, shared))
	}
}

// ── 授权后的「缓存不信任窗口」 ─────────────────────────────────────────────
//
// 用户口径（2026-10-31）：「选完目录直接可用，不要再来一步授权」。
// 光靠 InvalidateFnOSRootsCache 不够 —— 宿主侧授权可能异步落库，失效后立刻重取
// 拿到的还是旧列表，而且**又被缓存 30 秒**，用户只能再点一次刷新。窗口期内改成
// 每次都真问飞牛，就能在宿主落库后的下一次请求上自动生效。

func TestRootsStaleWindowBypassesCache(t *testing.T) {
	calls := 0
	swapRootsSource(t, func() ([]string, error) {
		calls++
		// 第一次还没有授权，第二次（宿主落库后）才有 —— 模拟真实的异步授权。
		if calls == 1 {
			return nil, nil
		}
		return []string{"/vol3/1000/音乐"}, nil
	})
	resetFnosStaleWindow(t)

	if got := fnosSharedRoots(); len(got) != 0 {
		t.Fatalf("前置条件：第一次应为空，实际 %v", got)
	}
	// 缓存命中：不打开窗口时第二次不该再拉。
	if got := fnosSharedRoots(); len(got) != 0 || calls != 1 {
		t.Fatalf("缓存应生效（calls=%d），实际 %v", calls, got)
	}

	// 用户刚授完权：前端打 auth-refresh → InvalidateFnOSRootsCache。
	InvalidateFnOSRootsCache()

	// 注意节流：窗口期内 1.5 秒只放行一次，所以这里必须等过节流间隔，
	// 否则测的是节流而不是窗口。用 fnosRootsStale.last 直接清零更稳。
	resetStaleThrottle()
	got := fnosSharedRoots()
	if calls != 2 {
		t.Fatalf("窗口期内应绕过缓存真去问飞牛，实际只拉了 %d 次", calls)
	}
	if len(got) != 1 || got[0] != "/vol3/1000/音乐" {
		t.Fatalf("窗口期内应读到新授权目录，实际 %v", got)
	}
}

func TestRootsStaleWindowExpires(t *testing.T) {
	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	resetFnosStaleWindow(t)

	NoteFnOSRootsStale()
	if !fnosRootsStaleNow() {
		t.Fatal("刚打开窗口时应判为 stale")
	}
	// 把窗口挪到过去：过期后必须回到「信缓存」的老行为，不许常态化打 socket。
	fnosRootsStale.Lock()
	fnosRootsStale.until = time.Now().Add(-time.Second)
	fnosRootsStale.Unlock()
	if fnosRootsStaleNow() {
		t.Fatal("窗口过期后不该再判为 stale")
	}
}

func TestRootsStaleThrottled(t *testing.T) {
	swapRootsSource(t, func() ([]string, error) { return nil, nil })
	resetFnosStaleWindow(t)

	NoteFnOSRootsStale()
	if !fnosRootsStaleNow() {
		t.Fatal("窗口期内第一次应放行")
	}
	if fnosRootsStaleNow() {
		t.Fatal("节流间隔内的第二次不该放行（目录树一次浏览会连打多个请求）")
	}
}

// resetFnosStaleWindow 清掉窗口与节流时间戳，并登记测试结束时复原
// （窗口是包级变量，残留会让后面的用例看见「永远 stale」）。
func resetFnosStaleWindow(t *testing.T) {
	t.Helper()
	resetFnosStaleWindowQuiet()
	t.Cleanup(resetFnosStaleWindowQuiet)
}

func resetFnosStaleWindowQuiet() {
	fnosRootsStale.Lock()
	fnosRootsStale.until = time.Time{}
	fnosRootsStale.last = time.Time{}
	fnosRootsStale.Unlock()
}

// resetStaleThrottle 只清节流时间戳，保留窗口。
func resetStaleThrottle() {
	fnosRootsStale.Lock()
	fnosRootsStale.last = time.Time{}
	fnosRootsStale.Unlock()
}
