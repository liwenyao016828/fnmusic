package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/config"
)

// 导出包里**绝不能**出现凭据 —— 这是这个功能的红线：一旦漏了，用户为了报个问题
// 就把自己的飞牛令牌和音源 cookie 发进了聊天窗口，收不回来。
func TestSensitiveKeyDetection(t *testing.T) {
	sensitive := []string{
		"fnos_token", "Token", "api_key", "apikey",
		"password", "passwd", "secret", "cookie", "credentials",
		"Authorization", "script",
	}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("键 %q 应当被判为敏感（漏抹 = 凭据外泄）", k)
		}
	}
	// 这些是排障真正需要的字段，抹掉了导出包就没用了。
	// api_sources / custom_sources 是**容器键**：它们本身不抹（抹掉整段就丢了
	// 「用户接了哪几个源」这个排障信息），靠递归进去抹掉 token / script 这些叶子。
	// 这条边界由 TestRedactValueKeepsShapeAndMasksLeaves 钉住。
	plain := []string{
		"filename_format", "cache_dir", "download_dir", "prefer_quality",
		"auto_download_quality", "preferred_platforms", "port", "theme",
		"api_sources", "custom_sources",
	}
	for _, k := range plain {
		if isSensitiveKey(k) {
			t.Errorf("键 %q 不应被判为敏感（抹掉了排障就没信息了）", k)
		}
	}
}

func TestRedactValueKeepsShapeAndMasksLeaves(t *testing.T) {
	in := map[string]any{
		"filename_format": "title-artist",
		"fnos_token":      "REAL-TOKEN",
		"api_sources": []any{
			map[string]any{"name": "自建源", "token": "REAL-TOKEN-2", "base_url": "http://x"},
		},
		"empty_token": "",
	}
	out, _ := redactValue(in).(map[string]any)
	if out["filename_format"] != "title-artist" {
		t.Errorf("非敏感字段被改动了：%v", out["filename_format"])
	}
	if out["fnos_token"] == "REAL-TOKEN" {
		t.Error("顶层令牌没有被抹除")
	}
	if out["empty_token"] != "" {
		t.Errorf("空字符串应当保持空（不是「已抹除」，否则看不出它是空）: %v", out["empty_token"])
	}
	arr, _ := out["api_sources"].([]any)
	if len(arr) != 1 {
		t.Fatalf("数组形状被破坏：%v", out["api_sources"])
	}
	item, _ := arr[0].(map[string]any)
	if item["token"] == "REAL-TOKEN-2" {
		t.Error("嵌套数组里的令牌没有被抹除")
	}
	if item["base_url"] != "http://x" {
		t.Errorf("嵌套数组里的非敏感字段被改动了：%v", item["base_url"])
	}
}

func TestExportFileNameShape(t *testing.T) {
	got := exportFileName(mustParseTime(t, "2026-10-15T13:45:01+08:00"))
	if got != "qulv-logs-20261015-134501.zip" {
		t.Errorf("导出文件名 = %q，期望 qulv-logs-20261015-134501.zip", got)
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("解析时间失败：%v", err)
	}
	return tm
}

// 端到端：真跑一次打包，逐个条目校验内容与脱敏。
func TestLogsExportZipEndToEnd(t *testing.T) {
	mgr, err := config.NewConfigManager(t.TempDir(), 8080)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	cfg := mgr.Get()
	cfg.FnosToken = "REAL-FNOS-TOKEN"
	cfg.FilenameFormat = "title-artist"
	cfg.DownloadDir = "/vol1/Music"
	cache := "/vol2/MediaCache"
	cfg.CacheDir = &cache
	if err := mgr.Update(cfg); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	const marker = "导出测试专用日志行-8f3a"
	applog.Default().Add(marker)

	s := &Server{cfgMgr: mgr}
	rec := httptest.NewRecorder()
	s.handleLogsExport(rec, httptest.NewRequest("GET", "/api/logs?format=zip", nil))

	res := rec.Result()
	if res.StatusCode != 200 {
		t.Fatalf("状态码 = %d，期望 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q，期望 application/zip", ct)
	}
	cd := res.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "qulv-logs-") || !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition 不完整：%q", cd)
	}

	body, _ := io.ReadAll(res.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("结果不是合法 zip：%v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开 %s 失败：%v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(b)
	}
	for _, want := range []string{"app.log", "info.txt", "config.json"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("包里缺少 %s（现有：%v）", want, keysOf(files))
		}
	}

	if !strings.Contains(files["app.log"], marker) {
		t.Error("app.log 里没有刚写进去的那条日志")
	}
	if !strings.Contains(files["info.txt"], CurrentVersion) {
		t.Errorf("info.txt 里没有版本号 %s：%s", CurrentVersion, files["info.txt"])
	}
	if !strings.Contains(files["info.txt"], cache) {
		t.Error("info.txt 里没有自定义缓存目录")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripJSONComments(files["config.json"])), &parsed); err != nil {
		t.Fatalf("config.json 不是合法 JSON（剥掉注释后）：%v\n%s", err, files["config.json"])
	}
	if parsed["filename_format"] != "title-artist" {
		t.Errorf("config.json 里的命名格式 = %v，期望 title-artist", parsed["filename_format"])
	}
	if strings.Contains(files["config.json"], "REAL-FNOS-TOKEN") {
		t.Error("config.json 里泄露了飞牛令牌 —— 这是这个功能的红线")
	}
	if !strings.Contains(files["config.json"], "已抹除") {
		t.Error("config.json 里应当能看到「已抹除」的占位（否则看不出是抹了还是本来就没有）")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// stripJSONComments 去掉导出时加的 `//` 头注释，好让测试能解析它。
func stripJSONComments(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}
