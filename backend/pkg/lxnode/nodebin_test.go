package lxnode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// node 可执行文件的查找（见 resolveNodeBin）。
//
// ⚠️ 这条判据是 2026-10-08 真机上查出来的：**飞牛的 node 不在应用的 PATH 里**
// （装在 /vol1/@appcenter/nodejs_v24/bin/node），原来只做 exec.LookPath →
// 服务端洛雪宿主根本起不来，而且失败是静默的。
//
// ⚠️⚠️ 2026-11-21 真机又踩出第二层：飞牛 PATH 里的 `/usr/bin/node` 是 v18，
// **不认 `--permission`**，能「找到 node」却起不来宿主。所以这里的判据从
// 「可执行就行」升级成「真跑一次 --permission 能过」。用例用可替换的探针
// （stubProbe）来钉顺序，不依赖跑用例那台机器的 node 版本。

// goodNode 写一个「接受 --permission」的假 node。
func goodNode(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubProbe 把探针换成白名单：只有 usable 里的路径才算可用。
//
// 必须 stub，否则判定结果取决于跑用例的机器（开发机 /usr/local/bin/node 是 v22，
// 真机可能根本没有 node）。
func stubProbe(t *testing.T, usable ...string) {
	t.Helper()
	allow := map[string]bool{}
	for _, p := range usable {
		allow[p] = true
	}
	old := nodePermissionProbe
	nodePermissionProbe = func(bin string) bool { return allow[bin] }
	resetProbeCache()
	t.Cleanup(func() {
		nodePermissionProbe = old
		resetProbeCache()
	})
}

func resetProbeCache() {
	nodeProbeCache.Range(func(k, _ any) bool {
		nodeProbeCache.Delete(k)
		return true
	})
}

func TestResolveNodeBinPrefersAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	fake := goodNode(t, dir, "mynode")
	stubProbe(t, fake)
	if got := resolveNodeBin(fake); got != fake {
		t.Fatalf("配置里给了绝对路径就该用它，得到 %q", got)
	}
	// 绝对路径写错时**继续往下找**（用户的意图是「用 node」，不是「用这个路径」）
	if got := resolveNodeBin(filepath.Join(dir, "nope")); got == filepath.Join(dir, "nope") {
		t.Fatalf("不存在的绝对路径不该被当成答案")
	}
}

func TestResolveNodeBinUsesEnvOverride(t *testing.T) {
	dir := t.TempDir()
	fake := goodNode(t, dir, "envnode")
	stubProbe(t, fake)
	t.Setenv("QULV_NODE_BIN", fake)
	if got := resolveNodeBin("node"); got != fake {
		t.Fatalf("QULV_NODE_BIN 应能覆盖，得到 %q", got)
	}
}

func TestResolveNodeBinFallsBackToPath(t *testing.T) {
	// PATH 是**最后**一步：前面的已知位置都不存在（stub 里只放行 PATH 那个）才轮到它
	dir := t.TempDir()
	fake := goodNode(t, dir, "node")
	stubProbe(t, fake)
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", dir)
	if got := resolveNodeBin(""); got != fake {
		t.Fatalf("前面都没命中时 PATH 里能找到就该用它，得到 %q", got)
	}
}

func TestResolveNodeBinReturnsEmptyWhenNothingFound(t *testing.T) {
	stubProbe(t) // 一个都不放行
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", t.TempDir())
	if got := resolveNodeBin("definitely-not-a-node"); got != "" {
		t.Fatalf("没有可用 node 时应返回空串，得到 %q", got)
	}
}

// TestResolveNodeBinSkipsNodeWithoutPermissionSupport 钉的正是 2026-11-21 真机故障：
// 一个**可执行但不认 --permission** 的 node 不能被选中（飞牛 /usr/bin/node v18 就是这样）。
// 这条用**真**探针，不 stub —— 它验的就是「跑一次才知道」这件事本身。
func TestResolveNodeBinSkipsNodeWithoutPermissionSupport(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "node")
	script := "#!/bin/sh\necho 'bad option: --permission' >&2\nexit 9\n"
	if err := os.WriteFile(bad, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	resetProbeCache()
	t.Cleanup(resetProbeCache)
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", dir)
	if got := resolveNodeBin("node"); got == bad {
		t.Fatalf("不认 --permission 的 node 不该被选中（真机故障就是这样来的）")
	}
	if nodeUsable(bad) {
		t.Fatalf("探针应判 bad node 不可用")
	}
}

// TestNodeCandidateListPutsAppcenterBeforePath 钉「已知安装位置优先于 PATH」。
func TestNodeCandidateListPutsAppcenterBeforePath(t *testing.T) {
	dir := t.TempDir()
	fake := goodNode(t, dir, "node")
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", dir)
	list := nodeCandidateList("node")
	idxApp, idxPath := -1, -1
	for i, p := range list {
		if p == "/vol1/@appcenter/nodejs_v24/bin/node" && idxApp < 0 {
			idxApp = i
		}
		if p == fake {
			idxPath = i
		}
	}
	if idxApp < 0 || idxPath < 0 {
		t.Fatalf("候选列表里应有 appcenter 与 PATH 两条，得到 %v", list)
	}
	if idxApp > idxPath {
		t.Fatalf("appcenter 的 node 必须排在 PATH 之前（否则飞牛 /usr/bin/node v18 会先被选中）")
	}
	// 去重：同一个路径不该出现两次
	seen := map[string]int{}
	for _, p := range list {
		seen[p]++
		if seen[p] > 1 {
			t.Fatalf("候选列表有重复项：%s", p)
		}
	}
}

// TestTailBufferKeepsLastBytes 钉「失败时能说出真正原因」的基础设施。
func TestTailBufferKeepsLastBytes(t *testing.T) {
	tb := newTailBuffer(16)
	if _, err := tb.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Write([]byte("XYZ")); err != nil {
		t.Fatal(err)
	}
	if got := tb.String(); !strings.HasSuffix(got, "XYZ") || len(got) > 16 {
		t.Fatalf("应只留最后 16 字节且带最新内容，得到 %q", got)
	}
	if got := firstLines("a\nb\nc\nd\n", 2); got != "a | b" {
		t.Fatalf("firstLines(2) 得到 %q", got)
	}
}
