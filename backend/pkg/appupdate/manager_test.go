package appupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub 起一个很小的假 GitHub：/release 返回 release JSON，/fpk 返回安装包字节。
func fakeGitHub(t *testing.T, payload []byte) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var releaseHits, fpkHits int32
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&releaseHits, 1)
		body := fmt.Sprintf(`{
			"tag_name":"v2.19.0","body":"notes","published_at":"2026-10-24T01:02:03Z",
			"html_url":"https://example.com/release",
			"assets":[{"name":"yinshu-ai-2.19.0.fpk","browser_download_url":"%s/fpk","size":%d}]
		}`, srv.URL, len(payload))
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/fpk", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fpkHits, 1)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/fpk-truncated", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fpkHits, 1)
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(payload[:10])
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &releaseHits, &fpkHits
}

func newTestManager(t *testing.T, srv *httptest.Server, opts ...func(*Options)) *Manager {
	t.Helper()
	o := Options{
		DataDir:        t.TempDir(),
		AppName:        "yinshu-ai",
		CurrentVersion: "2.18.1",
		Repo:           "o/r",
		APIURL:         srv.URL + "/release",
		Client:         srv.Client(),
		ScriptDir:      t.TempDir(),
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func waitStatus(t *testing.T, m *Manager, want string) DownloadState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := m.State()
		if st.Status == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := m.State()
	t.Fatalf("等状态 %q 超时，当前 %+v", want, st)
	return DownloadState{}
}

func TestCheckUsesCacheAndStripsTagPrefix(t *testing.T) {
	srv, hits, _ := fakeGitHub(t, []byte("fpk"))
	m := newTestManager(t, srv)

	res := m.Check(context.Background(), false)
	if res.Error != "" {
		t.Fatalf("检查失败：%s", res.Error)
	}
	if res.Current != "2.18.1" || res.Latest != "2.19.0" || !res.HasUpdate {
		t.Fatalf("结果不对：%+v", res)
	}
	if res.AssetName != "yinshu-ai-2.19.0.fpk" || res.Size != 3 || res.AssetURL == "" {
		t.Fatalf("安装包信息不对：%+v", res)
	}
	// 缓存：第二次不再打 API
	m.Check(context.Background(), false)
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("10 分钟缓存内不应重复请求，实际 %d 次", got)
	}
	m.Check(context.Background(), true)
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("refresh 应绕过缓存，实际 %d 次", got)
	}
}

func TestCheckReportsGitHubFailureButKeepsCurrent(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte("fpk"))
	m := newTestManager(t, srv)
	srv.Close() // 立刻断网

	res := m.Check(context.Background(), false)
	if res.Error == "" {
		t.Fatal("网络失败应有 error")
	}
	if res.Current != "2.18.1" || res.HasUpdate {
		t.Fatalf("失败时仍应给出当前版本且 has_update=false：%+v", res)
	}
}

func TestCheckWithoutFPKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v2.19.0","html_url":"u","assets":[]}`))
	}))
	defer srv.Close()
	m := newTestManager(t, srv)
	res := m.Check(context.Background(), false)
	if res.Error == "" || !strings.Contains(res.Error, "没有 .fpk") {
		t.Fatalf("有版本无 fpk 应明确报错：%+v", res)
	}
}

func TestDownloadWritesFileAndIsIdempotent(t *testing.T) {
	payload := []byte(strings.Repeat("F", 300<<10))
	srv, _, fpkHits := fakeGitHub(t, payload)
	m := newTestManager(t, srv)
	if res := m.Check(context.Background(), false); res.Error != "" {
		t.Fatal(res.Error)
	}

	st, err := m.StartDownload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusDownloading {
		t.Fatalf("应立即返回 downloading，实际 %s", st.Status)
	}
	done := waitStatus(t, m, StatusReady)
	if done.Progress != 100 || done.Size != int64(len(payload)) {
		t.Fatalf("完成后进度/大小不对：%+v", done)
	}
	got, err := os.ReadFile(done.Path)
	if err != nil || len(got) != len(payload) {
		t.Fatalf("落盘文件不对：len=%d err=%v", len(got), err)
	}
	if filepath.Dir(done.Path) != filepath.Join(m.dataDir, "updates") {
		t.Fatalf("应放在 <dataDir>/updates 下，实际 %s", done.Path)
	}

	// 幂等：再点一次不应再下载
	st2, err := m.StartDownload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st2.Status != StatusReady || st2.Path != done.Path {
		t.Fatalf("已就绪应直接返回 ready：%+v", st2)
	}
	if got := atomic.LoadInt32(fpkHits); got != 1 {
		t.Fatalf("重复调用不应重复下载，实际 %d 次", got)
	}
}

func TestDownloadDetectsTruncatedFile(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte(strings.Repeat("F", 100)))
	m := newTestManager(t, srv)
	if res := m.Check(context.Background(), false); res.Error != "" {
		t.Fatal(res.Error)
	}
	// 手动把 asset URL 指向截断端点：模拟 CDN 少发了字节。
	m.mu.Lock()
	m.assetURL = srv.URL + "/fpk-truncated"
	m.mu.Unlock()

	if _, err := m.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := waitStatus(t, m, StatusError)
	if bad.Error == "" {
		t.Fatalf("截断下载应报错：%+v", bad)
	}
}

func TestInstallRequiresReadyFile(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte("x"))
	m := newTestManager(t, srv)
	if _, err := m.Install(); err == nil || !strings.Contains(err.Error(), "还没有下载好") {
		t.Fatalf("未下载就安装应报错，实际 %v", err)
	}
}

func TestInstallNoPermissionGivesActionableMessage(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte("x"))
	m := newTestManager(t, srv, func(o *Options) {
		o.IsRoot = func() bool { return false }
		o.SudoAvailable = func() bool { return false }
	})
	// 造一个已就绪的安装包
	path := filepath.Join(t.TempDir(), "y.fpk")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.dl = DownloadState{Status: StatusReady, Path: path, Progress: 100, Size: 1}
	m.mu.Unlock()

	_, err := m.Install()
	if err == nil || !strings.Contains(err.Error(), "应用中心") {
		t.Fatalf("无权限时应提示去应用中心手动安装，实际 %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(m.dir, scriptName)); statErr == nil {
		t.Fatal("没有权限时不应留下安装脚本")
	}
}

func TestInstallWritesScriptAndLaunchesDetached(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte("x"))
	launched := make(chan string, 1)
	m := newTestManager(t, srv, func(o *Options) {
		o.IsRoot = func() bool { return true }
		o.Launch = func(script, logPath string) error {
			launched <- script + "|" + logPath
			return nil
		}
	})
	fpk := filepath.Join(m.dataDir, "updates", "y.fpk")
	if err := os.MkdirAll(filepath.Dir(fpk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fpk, []byte("fpk"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.dl = DownloadState{Status: StatusReady, Path: fpk, Progress: 100, Size: 3}
	m.mu.Unlock()

	res, err := m.Install()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || !strings.HasSuffix(res.Log, logName) {
		t.Fatalf("返回结果不对：%+v", res)
	}
	if got := m.State().Status; got != StatusInstalling {
		t.Fatalf("状态应为 installing，实际 %s", got)
	}

	scriptPath := filepath.Join(m.dir, scriptName)
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("脚本没写出来：%v", err)
	}
	body := string(data)
	for _, want := range []string{
		"set -eu",
		"exec sudo -n /bin/sh",
		"appcenter-cli stop",
		"appcenter-cli uninstall",
		"appcenter-cli install-fpk",
		"--env",
		"--volume",
		"tar -xOzf",
		"sleep 2",
		"yinshu-ai",
		fpk,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("脚本缺少 %q", want)
		}
	}
	if info, err := os.Stat(scriptPath); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("脚本应可执行：%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, envName)); err != nil {
		t.Fatalf("env 文件没写出来：%v", err)
	}

	select {
	case got := <-launched:
		if !strings.HasPrefix(got, scriptPath+"|") {
			t.Fatalf("启动参数不对：%s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("安装脚本没有被启动")
	}
}

func TestStateReadyTurnsIdleWhenFileDisappears(t *testing.T) {
	srv, _, _ := fakeGitHub(t, []byte("x"))
	m := newTestManager(t, srv)
	path := filepath.Join(t.TempDir(), "gone.fpk")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.dl = DownloadState{Status: StatusReady, Path: path, Progress: 100}
	m.mu.Unlock()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st := m.State(); st.Status != StatusIdle || st.Progress != 0 {
		t.Fatalf("文件没了状态应回到 idle：%+v", st)
	}
}
