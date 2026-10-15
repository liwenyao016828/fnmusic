package lxnode

import (
	"os"
	"path/filepath"
	"testing"
)

// node 可执行文件的查找（见 resolveNodeBin）。
//
// ⚠️ 这条判据是 2026-10-08 真机上查出来的：**飞牛的 node 不在应用的 PATH 里**
// （装在 /vol1/@appcenter/nodejs_v24/bin/node），原来只做 exec.LookPath →
// 服务端洛雪宿主根本起不来，而且失败是静默的。

func TestResolveNodeBinPrefersAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "mynode")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	fake := filepath.Join(dir, "envnode")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QULV_NODE_BIN", fake)
	if got := resolveNodeBin("node"); got != fake {
		t.Fatalf("QULV_NODE_BIN 应能覆盖，得到 %q", got)
	}
}

func TestResolveNodeBinFallsBackToPath(t *testing.T) {
	// 开发机/通用 Linux 上 PATH 里有 node，走第 3 步
	dir := t.TempDir()
	fake := filepath.Join(dir, "node")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", dir)
	if got := resolveNodeBin(""); got != fake {
		t.Fatalf("PATH 里能找到就该用它，得到 %q", got)
	}
}

func TestResolveNodeBinReturnsEmptyWhenNothingFound(t *testing.T) {
	t.Setenv("QULV_NODE_BIN", "")
	t.Setenv("PATH", t.TempDir()) // 空 PATH
	// 已知位置在开发机上不存在，所以应该是空串（真机上会命中 nodejs_vNN）
	if got := resolveNodeBin("definitely-not-a-node"); got != "" {
		// 允许命中开发机上的 /usr/local/bin/node —— 那种情况下这个断言不成立
		if !isExecutableFile(got) {
			t.Fatalf("返回的路径必须真的可执行：%q", got)
		}
	}
}
