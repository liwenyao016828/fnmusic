package api

import (
	"fmt"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/monitor"
)

// 用桩替换真正的取链调用，验证「音源选择 / 降级链 / 错误口径」而不碰网络
func withResolveStub(t *testing.T, fn func(protocol, baseURL, token, platform, songID, quality string) (string, error)) {
	t.Helper()
	orig := apiSourceResolve
	if fn != nil {
		apiSourceResolve = fn
	}
	t.Cleanup(func() { apiSourceResolve = orig })
}

func newResolveSrv(t *testing.T, sources ...config.APISource) *Server {
	t.Helper()
	cm, err := config.NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatalf("创建配置管理器失败：%v", err)
	}
	cfg := cm.Get()
	cfg.APISources = sources
	if err := cm.Update(cfg); err != nil {
		t.Fatalf("写入音源配置失败：%v", err)
	}
	return &Server{cfgMgr: cm}
}

func resolveMon() *monitor.Monitor {
	return &monitor.Monitor{ID: "m1", Quality: monitor.QualityLossless, Fallback: monitor.FallbackBestEffort}
}

// 档位映射与降级链
func TestQualityChainMapping(t *testing.T) {
	cases := []struct {
		q        monitor.Quality
		fb       monitor.Fallback
		expected []string
	}{
		{monitor.QualityStandard, monitor.FallbackBestEffort, []string{"128k"}},
		{monitor.QualityHigh, monitor.FallbackBestEffort, []string{"320k", "128k"}},
		{monitor.QualityLossless, monitor.FallbackBestEffort, []string{"flac", "320k", "128k"}},
		{monitor.QualityHiRes, monitor.FallbackBestEffort, []string{"flac", "320k", "128k"}},
		// skip：只试目标档，不降级
		{monitor.QualityLossless, monitor.FallbackSkip, []string{"flac"}},
		{monitor.QualityHigh, monitor.FallbackSkip, []string{"320k"}},
		// Fallback 空值按 best_effort 处理
		{monitor.QualityLossless, "", []string{"flac", "320k", "128k"}},
	}
	for _, c := range cases {
		got := qualityChain(c.q, c.fb)
		if strings.Join(got, ",") != strings.Join(c.expected, ",") {
			t.Errorf("qualityChain(%s,%s) = %v，期望 %v", c.q, c.fb, got, c.expected)
		}
	}
}

// CanResolve 必须反映「真的有已启用音源」——否则没配音源的用户会满屏取链失败，
// 而不是看到准确的「待网页端补下载」。
func TestCanResolveReflectsEnabledSources(t *testing.T) {
	if (&monitorDispatcher{srv: newResolveSrv(t)}).CanResolve() {
		t.Error("没配音源时应为 false")
	}
	if (&monitorDispatcher{srv: newResolveSrv(t, config.APISource{
		ID: "a", BaseURL: "http://x", Enabled: false,
	})}).CanResolve() {
		t.Error("只有停用音源时应为 false")
	}
	if (&monitorDispatcher{srv: newResolveSrv(t, config.APISource{
		ID: "a", BaseURL: "  ", Enabled: true,
	})}).CanResolve() {
		t.Error("地址为空时应为 false")
	}
	if !(&monitorDispatcher{srv: newResolveSrv(t, config.APISource{
		ID: "a", BaseURL: "http://x", Protocol: "lx_server", Enabled: true,
	})}).CanResolve() {
		t.Error("有启用音源时应为 true")
	}
}

// 未启用的音源必须被跳过，且按配置顺序取第一个成功的
func TestResolveURLSkipsDisabledAndUsesFirstSuccess(t *testing.T) {
	var called []string
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		called = append(called, baseURL)
		if baseURL == "http://first" {
			return "", fmt.Errorf("这台挂了")
		}
		return "https://cdn.example/x.flac", nil
	})

	s := newResolveSrv(t,
		config.APISource{ID: "a", Name: "停用的", BaseURL: "http://disabled", Protocol: "lx_server", Enabled: false},
		config.APISource{ID: "b", Name: "第一台", BaseURL: "http://first", Protocol: "lx_server", Enabled: true},
		config.APISource{ID: "c", Name: "第二台", BaseURL: "http://second", Protocol: "lx_server", Enabled: true},
	)

	url, err := (&monitorDispatcher{srv: s}).ResolveURL(resolveMon(), monitor.Song{Source: "wy", ID: "186016"})
	if err != nil {
		t.Fatalf("应取链成功：%v", err)
	}
	if url != "https://cdn.example/x.flac" {
		t.Fatalf("返回地址不符：%s", url)
	}
	for _, c := range called {
		if c == "http://disabled" {
			t.Fatal("未启用的音源不应被调用")
		}
	}
	if len(called) == 0 || called[0] != "http://first" {
		t.Fatalf("应按配置顺序尝试，实际 %v", called)
	}
}

// 平台与曲目 ID 必须原样传给音源服务（wy 而不是 netease 之外的口径由 apisource 负责映射）
func TestResolveURLPassesPlatformAndSongID(t *testing.T) {
	var gotPlatform, gotSongID, gotQuality string
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		gotPlatform, gotSongID, gotQuality = platform, songID, quality
		return "https://cdn.example/y.mp3", nil
	})

	s := newResolveSrv(t, config.APISource{
		ID: "a", Name: "自建", BaseURL: "http://api", Protocol: "lx_script", Token: "tk", Enabled: true,
	})
	mon := resolveMon()
	mon.Quality = monitor.QualityHigh
	mon.Fallback = monitor.FallbackSkip

	if _, err := (&monitorDispatcher{srv: s}).ResolveURL(mon, monitor.Song{Source: "tx", ID: "003OUl"}); err != nil {
		t.Fatalf("取链失败：%v", err)
	}
	if gotPlatform != "tx" || gotSongID != "003OUl" {
		t.Fatalf("平台/ID 传参不符：%s / %s", gotPlatform, gotSongID)
	}
	if gotQuality != "320k" {
		t.Fatalf("skip 策略下应只试目标档 320k，实际 %s", gotQuality)
	}
}

// best_effort：无损取不到时应降级到 320k，而不是直接失败
func TestResolveURLBestEffortDowngrades(t *testing.T) {
	var tried []string
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		tried = append(tried, quality)
		if quality == "flac" {
			return "", fmt.Errorf("该曲目没有无损")
		}
		return "https://cdn.example/z.mp3", nil
	})

	s := newResolveSrv(t, config.APISource{
		ID: "a", Name: "自建", BaseURL: "http://api", Protocol: "lx_server", Enabled: true,
	})
	url, err := (&monitorDispatcher{srv: s}).ResolveURL(resolveMon(), monitor.Song{Source: "wy", ID: "1"})
	if err != nil {
		t.Fatalf("best_effort 应降级成功：%v", err)
	}
	if url == "" {
		t.Fatal("降级后应返回地址")
	}
	if strings.Join(tried, ",") != "flac,320k" {
		t.Fatalf("应按 flac → 320k 顺序降级，实际 %v", tried)
	}
}

// 没有已启用的音源 → 给出可行动的错误（指向音源管理）
func TestResolveURLNoEnabledSource(t *testing.T) {
	withResolveStub(t, func(string, string, string, string, string, string) (string, error) {
		t.Fatal("没有可用音源时不应调用取链")
		return "", nil
	})
	s := newResolveSrv(t, config.APISource{
		ID: "a", Name: "停用的", BaseURL: "http://x", Protocol: "lx_server", Enabled: false,
	})

	_, err := (&monitorDispatcher{srv: s}).ResolveURL(resolveMon(), monitor.Song{Source: "wy", ID: "1"})
	if err == nil || !strings.Contains(err.Error(), "音源管理") {
		t.Fatalf("应提示去音源管理启用服务型音源，实际 %v", err)
	}
}

// 全部失败时错误里要带上已尝试的音源，便于用户定位
func TestResolveURLAllFailReportsAttempts(t *testing.T) {
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		return "", fmt.Errorf("connection refused")
	})
	s := newResolveSrv(t, config.APISource{
		ID: "a", Name: "自建A", BaseURL: "http://a", Protocol: "lx_server", Enabled: true,
	})

	_, err := (&monitorDispatcher{srv: s}).ResolveURL(resolveMon(), monitor.Song{Source: "wy", ID: "1"})
	if err == nil {
		t.Fatal("全部失败应返回错误")
	}
	if !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "自建A") {
		t.Fatalf("错误应含原因与已尝试音源，实际 %v", err)
	}
}

// 缺少平台或 ID 的曲目无法取链，应尽早报错
func TestResolveURLRejectsIncompleteSong(t *testing.T) {
	s := newResolveSrv(t)
	_, err := (&monitorDispatcher{srv: s}).ResolveURL(resolveMon(), monitor.Song{Source: "", ID: "1"})
	if err == nil || !strings.Contains(err.Error(), "无法取链") {
		t.Fatalf("应拒绝缺平台/ID 的曲目，实际 %v", err)
	}
}
