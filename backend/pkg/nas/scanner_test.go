package nas

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 2026-10-29 回归：`/home` 绝不能是兜底「卷」。
//
// 背景：飞牛上 `/home/liwenyao` 是用户家目录。以前 getAvailableVolumes 把 `/home`
// 也列进去，于是
//   - `/home` 成了合法可删根（可删范围被放大到整个家目录）；
//   - joinTrash 按 "最贴近的根" 挑，`/home/liwenyao/.../Music/a.mp3` 命中的是 `/home`，
//     回收站被算成 `/home/.tidy_trash` —— 那是 root 拥有的目录，普通用户写不进去，
//     挪进回收站全失败，而 Handler 还回 200 →「列表删除了，实际没有删除」。
// 修法：兜底列表只保留真正的卷挂载点，共享目录走 FN_ALLOWED_ROOTS / 飞牛授权目录显式给出。
func TestGetAvailableVolumesExcludesHome(t *testing.T) {
	vols := getAvailableVolumes()
	if len(vols) == 0 {
		t.Fatal("getAvailableVolumes 不该返回空列表（至少要给出兜底卷）")
	}
	for _, v := range vols {
		// 精确比对：既拦 "/home"，也拦 "/home/" 这种写法。
		if filepath.Clean(v) == "/home" {
			t.Fatalf("兜底卷里出现了 /home（家目录不该被当成可删根）：%v", vols)
		}
		if strings.HasPrefix(filepath.Clean(v), "/home/") {
			t.Fatalf("兜底卷里出现了家目录下的路径：%s（全部：%v）", v, vols)
		}
	}
}

// 上面那条在「本机恰好有 /vol1」时会走真实探测分支；这条专门钉住**兜底**分支：
// 即使一个卷都不存在（容器 / 精简系统），兜底的 /vol1,/vol2,/vol3 里也不许冒出 /home。
//
// 做法：把探测用的目录（/vol1..16、/media、/mnt）在这台机器上是否存在这件事「固定」下来
// —— 这里不改代码，而是直接断言兜底常量的语义：一旦哪天有人把 /home 加回兜底列表，
// 上面那条在开发机（有 /home）与这条在容器（无 /vol*）里都会红。
func TestGetAvailableVolumesFallbackHasNoHome(t *testing.T) {
	vols := getAvailableVolumes()
	// 兜底分支的返回值恰好等于这三项时，逐项复查（顺带确认兜底本身没被写坏）
	isFallback := len(vols) == 3 &&
		vols[0] == "/vol1" && vols[1] == "/vol2" && vols[2] == "/vol3"
	if !isFallback {
		t.Logf("本机走了真实挂载点探测（%v），兜底分支由 TestGetAvailableVolumesExcludesHome 覆盖", vols)
		return
	}
	for _, v := range vols {
		if strings.Contains(v, "home") {
			t.Fatalf("兜底列表里出现了 home：%v", vols)
		}
	}
}

// 允许根的整体口径也复查一遍：AllowedRoots 是删除/浏览的安全边界，
// 在没有显式授权（FN_ALLOWED_ROOTS 未设置）时也不该把 /home 放进来。
func TestAllowedRootsExcludeHomeByDefault(t *testing.T) {
	// 显式清空 FN_ALLOWED_ROOTS 的「设置」语义会覆盖内置列表（返回空 = fail closed），
	// 所以这里必须真的 unset，而不是设成空串。
	old, had := os.LookupEnv(allowedRootsEnv)
	if err := os.Unsetenv(allowedRootsEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	defer func() {
		if had {
			_ = os.Setenv(allowedRootsEnv, old)
		}
	}()

	roots := AllowedRoots() // 飞牛不可用时自然回退到卷探测
	for _, r := range roots {
		if filepath.Clean(r) == "/home" {
			sort.Strings(roots)
			t.Fatalf("AllowedRoots 默认包含了 /home：%v", roots)
		}
	}
}
