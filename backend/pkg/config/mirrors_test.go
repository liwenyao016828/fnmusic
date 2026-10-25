package config

import "testing"

// TestNormalizeUpdateMirrorAcceptsHTTP 守住一个曾经写反的分支：
// 早先 `case "http","https": return false`，把**所有合法镜像**全丢掉，
// 表现为「界面填了也存了，但一个都没用上」。这个用例就是它的回归测试。
func TestNormalizeUpdateMirrorAcceptsHTTP(t *testing.T) {
	for _, raw := range []string{
		"https://gh-proxy.com",
		"http://192.168.1.3:8080/gh",
		"gh-proxy.com",           // 不带 scheme，要补 https
		"https://my.host/{repo}/latest.json",
	} {
		if _, ok := NormalizeUpdateMirror(UpdateMirror{URL: raw}); !ok {
			t.Fatalf("%q 应被接受", raw)
		}
	}
	for _, raw := range []string{"ftp://x.com/a", "javascript:alert(1)", ""} {
		if _, ok := NormalizeUpdateMirror(UpdateMirror{URL: raw}); ok {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
}

// TestNormalizeUpdateMirrorTrimsSlash：前缀型末尾的 `/` 要归一掉，拼接只有一个分隔符。
func TestNormalizeUpdateMirrorTrimsSlash(t *testing.T) {
	m, ok := NormalizeUpdateMirror(UpdateMirror{URL: "https://gh-proxy.com/"})
	if !ok {
		t.Fatal("应被接受")
	}
	if m.URL != "https://gh-proxy.com" {
		t.Fatalf("末尾斜杠应去掉，实得 %q", m.URL)
	}
	// 不带名字时用 host 当显示名。
	if m.Name != "gh-proxy.com" {
		t.Fatalf("默认名应为 host，实得 %q", m.Name)
	}
}

// TestOfficialMirrorMarkerKeepsDisabledFlag：官方源占位项必须能存下来，
// 且 Disabled 跟着用户填的走（写成强制 true 会让「停用」变成「启用」）。
func TestOfficialMirrorMarkerKeepsDisabledFlag(t *testing.T) {
	m, ok := NormalizeUpdateMirror(UpdateMirror{URL: OfficialMirrorURL, Disabled: false})
	if !ok || m.Disabled {
		t.Fatalf("占位项应被保留且 Disabled=false，实得 %+v ok=%v", m, ok)
	}
}

// TestNormalizeUpdateMirrorsDedupes：同一台镜像的两种写法只留一条，
// 否则会对同一台白等两轮超时。
func TestNormalizeUpdateMirrorsDedupes(t *testing.T) {
	got := NormalizeUpdateMirrors([]UpdateMirror{
		{URL: "https://gh-proxy.com"},
		{URL: "https://gh-proxy.com/"},
		{URL: "https://ghfast.top"},
		{URL: "ftp://bad.example"}, // 非 http(s)，必须被丢掉
	})
	if len(got) != 2 {
		t.Fatalf("应剩 2 条，实得 %d：%+v", len(got), got)
	}
	if got[0].URL != "https://gh-proxy.com" || got[1].URL != "https://ghfast.top" {
		t.Fatalf("去重后应保序，实得 %+v", got)
	}
}

// TestMirrorURL：前缀型拼在原地址前；源型展开 {repo} 且忽略原地址。
func TestMirrorURL(t *testing.T) {
	prefix := UpdateMirror{URL: "https://gh-proxy.com"}
	official := "https://api.github.com/repos/o/r/releases/latest"
	if got, want := MirrorURL(prefix, official, "o/r"), "https://gh-proxy.com/"+official; got != want {
		t.Fatalf("want %q got %q", want, got)
	}
	src := UpdateMirror{URL: "https://my.host/{repo}/latest.json"}
	if got, want := MirrorURL(src, official, "o/r"), "https://my.host/o/r/latest.json"; got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

// TestDefaultUpdateMirrorsIsCopy：默认清单必须是副本，改它不能污染内置值
// （用户 2026-10-25 拍板「只放 gh-proxy」）。
func TestDefaultUpdateMirrorsIsCopy(t *testing.T) {
	a := DefaultUpdateMirrors()
	if len(a) != 1 || a[0].URL != "https://gh-proxy.com" {
		t.Fatalf("默认清单应只有 gh-proxy.com，实得 %+v", a)
	}
	a[0].URL = "changed"
	if b := DefaultUpdateMirrors(); b[0].URL != "https://gh-proxy.com" {
		t.Fatalf("默认清单被污染了：%+v", b)
	}
}
