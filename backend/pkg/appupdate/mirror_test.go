package appupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fn-lx-player/pkg/config"
)

// fakeMirror 起一台「只会转发 release JSON」的镜像，行为最接近 gh-proxy.com：
// 路径 = `/` + 原始地址。dead=true 时直接断连（模拟国内不通的主机）。
func fakeMirror(t *testing.T, payload []byte, tag string, dead bool) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if dead {
			hj, _ := w.(http.Hijacker)
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		// 真实镜像的路径形如 `/https://api.github.com/repos/.../releases/latest`。
		if !strings.HasPrefix(r.URL.Path, "/https://") {
			http.NotFound(w, r)
			return
		}
		// 路径里带 ?case= 时给不同的 tag，让同一台假服务器能扮演多个镜像。
		if c := r.URL.Query().Get("case"); c != "" {
			tag = c
		}
		_, _ = w.Write([]byte(`{"tag_name":"v` + tag + `","body":"notes","assets":[` +
			`{"name":"app.fpk","browser_download_url":"` + srvURLOf(t, r) + `/assets/app.fpk","size":7}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// srvURLOf 从请求里推回测试服务器自己的地址（fakeMirror 里拿不到闭包外的 srv）。
func srvURLOf(t *testing.T, r *http.Request) string {
	t.Helper()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// testManager 构造一个「能连 httptest 假服务器」的 Manager。
//
// 为什么不能直接 New(opts)：New 见到 Client==nil 会塞上安全 transport，
// 而那套 SSRF 防护**故意拦 127.0.0.1**（这是全项目约定，不能为测试放宽），
// 所以这里在构造之后把 client 换成默认 client、并把 proxyFunc 清掉
// （非 nil 的 proxyFunc 会让 httpClient() 走代理分支重建安全 client）。
// 顺序必须与 New 内部一致：先赋 client，再清 proxyFunc。
func testManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m := New(mirrorOpts(dir))
	m.client = http.DefaultClient
	m.proxyFunc = nil
	// 测试里默认**不许**追加内置官方源：纯前缀镜像的地址是 `假服务器 + 官方地址`，
	// 通配的假服务器会把官方那条也服务掉，于是「镜像失败」被官方成功掩盖。
	// 要测「回退到官方」的用例自己把它打开。
	m.allowOfficial = false
	return m
}

func mirrorOpts(dir string) Options {
	return Options{
		DataDir:        dir,
		ScriptDir:      dir,
		CurrentVersion: "2.0.0",
		Logf:           func(string, ...any) {},
		Now:            time.Now,
	}
}

// TestCheckFallsThroughToFirstWorkingMirror：第一台不通、第二台能用 →
// 必须命中第二台，且 Tried 里能看出第一台失败的原因。
func TestCheckFallsThroughToFirstWorkingMirror(t *testing.T) {
	dir := t.TempDir()
	dead, _ := fakeMirror(t, nil, "", true)
	good, goodHits := fakeMirror(t, nil, "2.1.0", false)

	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{
			{Name: "坏的", URL: dead.URL},
			{Name: "好的", URL: good.URL},
		}
	}
	m.apiURL = testManager(t, t.TempDir()).officialReleaseURL()

	res := m.Check(context.Background(), true)
	if res.Error != "" {
		t.Fatalf("第二台应当成功，实得 error=%q tried=%+v", res.Error, res.Tried)
	}
	if res.Latest != "2.1.0" {
		t.Fatalf("应取到 2.1.0，实得 %q", res.Latest)
	}
	if res.Source != "好的" {
		t.Fatalf("Source 应为「好的」，实得 %q", res.Source)
	}
	if n := atomic.LoadInt32(goodHits); n != 1 {
		t.Fatalf("好镜像应被请求 1 次，实得 %d", n)
	}
	var deadSeen bool
	for _, a := range res.Tried {
		if a.Name == "坏的" && !a.OK && a.Error != "" {
			deadSeen = true
		}
	}
	if !deadSeen {
		t.Fatalf("Tried 里应记录第一台的失败原因，实得 %+v", res.Tried)
	}
}

// TestCheckFallsBackToOfficialLast：镜像全挂 + 官方源可用 → 必须回退到官方源。
// 这是用户 2026-10-25 明确要求的「回退」。
func TestCheckFallsBackToOfficialLast(t *testing.T) {
	dir := t.TempDir()
	dead, _ := fakeMirror(t, nil, "", true)
	official, _ := fakeMirror(t, nil, "2.2.0", false)

	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{{Name: "坏的", URL: dead.URL}}
	}
	m.apiURL = official.URL + "/https://api.github.com/repos/x/y/releases/latest"
	m.allowOfficial = true // 这条用例正是要测「回退官方源」

	res := m.Check(context.Background(), true)
	if res.Error != "" {
		t.Fatalf("应回退到官方源成功，实得 error=%q", res.Error)
	}
	if res.Source != officialName {
		t.Fatalf("Source 应为官方源，实得 %q", res.Source)
	}
	if res.Latest != "2.2.0" {
		t.Fatalf("应取到 2.2.0，实得 %q", res.Latest)
	}
}

// TestCheckAllSourcesFailReportsEveryOne：全部失败时错误里必须列出每一台，
// 否则用户只知道「失败了」，不知道该换哪一台。
func TestCheckAllSourcesFailReportsEveryOne(t *testing.T) {
	dir := t.TempDir()
	a, _ := fakeMirror(t, nil, "", true)
	b, _ := fakeMirror(t, nil, "", true)

	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{{Name: "甲", URL: a.URL}, {Name: "乙", URL: b.URL}}
	}
	m.apiURL = "http://127.0.0.1:1/repos/x/y/releases/latest" // 一定连不上

	res := m.Check(context.Background(), true)
	if res.Error == "" {
		t.Fatal("全失败时必须有 error")
	}
	for _, name := range []string{"甲", "乙"} {
		if !strings.Contains(res.Error, name) {
			t.Fatalf("错误文案要含「%s」，实得 %q", name, res.Error)
		}
	}
}

// TestDisabledMirrorIsSkippedButReported：停用的镜像不参与尝试，但要出现在
// Tried 里（skipped=true）——「为什么没走我填的那台」的答案就在这里。
func TestDisabledMirrorIsSkippedButReported(t *testing.T) {
	dir := t.TempDir()
	off, offHits := fakeMirror(t, nil, "9.9.9", false)

	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{{Name: "停用的", URL: off.URL, Disabled: true}}
	}
	official, _ := fakeMirror(t, nil, "2.0.0", false)
	m.apiURL = official.URL + "/https://api.github.com/repos/x/y/releases/latest"
	m.allowOfficial = true

	res := m.Check(context.Background(), true)
	if n := atomic.LoadInt32(offHits); n != 0 {
		t.Fatalf("停用的镜像不该被请求，实得 %d 次", n)
	}
	if res.Latest != "2.0.0" {
		t.Fatalf("应走官方源拿到 2.0.0，实得 %q", res.Latest)
	}
	var skipped bool
	for _, a := range res.Tried {
		if a.Name == "停用的" && a.Skipped {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("Tried 里应有 skipped 记录，实得 %+v", res.Tried)
	}
}

// TestOfficialCanBeDisabled：用户把官方源也停用时，就不再回退到 GitHub。
func TestOfficialCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	dead, _ := fakeMirror(t, nil, "", true)

	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{
			{Name: "坏的", URL: dead.URL},
			{Name: "GitHub 官方", URL: OfficialMirrorURL, Disabled: true},
		}
	}
	m.apiURL = "http://127.0.0.1:1/releases/latest"

	res := m.Check(context.Background(), true)
	if res.Error == "" {
		t.Fatal("官方源被停用 + 镜像不可用 → 必须报错")
	}
}

// TestAssetURLForKeepsOfficialAndPrefixesMirror 校验资产地址的拼法。
func TestAssetURLForKeepsOfficialAndPrefixesMirror(t *testing.T) {
	raw := "https://github.com/o/r/releases/download/v1/a.fpk"
	off := sourceEntry{isOfficial: true}
	if got := assetURLFor(off, raw); got != raw {
		t.Fatalf("官方源应原样返回，实得 %q", got)
	}
	pre := sourceEntry{mirror: config.UpdateMirror{URL: "https://gh-proxy.com"}}
	if got, want := assetURLFor(pre, raw), "https://gh-proxy.com/"+raw; got != want {
		t.Fatalf("前缀镜像应拼前缀：want %q got %q", want, got)
	}
	// 源型镜像自己托管资产，套前缀会拼出坏 URL。
	src := sourceEntry{mirror: config.UpdateMirror{URL: "https://my.host/{repo}/latest.json"}}
	if got := assetURLFor(src, raw); got != raw {
		t.Fatalf("源型镜像不该改写资产地址，实得 %q", got)
	}
}

// TestSourceMirrorExpandsRepoPlaceholder：源型镜像把 {repo} 换成 owner/repo。
func TestSourceMirrorExpandsRepoPlaceholder(t *testing.T) {
	dir := t.TempDir()
	m := testManager(t, dir)
	m.mirrorsFunc = func() []config.UpdateMirror {
		return []config.UpdateMirror{{Name: "自建", URL: "https://my.host/rel/{repo}/latest.json"}}
	}
	list, _ := m.sources()
	t.Logf("mirrorsFunc nil? %v; sources=%+v", m.mirrorsFunc == nil, list)
	if len(list) == 0 {
		t.Fatal("应至少有一条源")
	}
	if got, want := list[0].releaseURL, "https://my.host/rel/liwenyao016828/fnmusic/latest.json"; got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}
