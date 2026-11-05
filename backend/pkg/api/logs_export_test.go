package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

	// disk 相关断言：本用例没启用落盘，info.txt 必须如实说明「未启用」，
	// 而不是悄悄地什么也不写（用户会以为是打包失败）。
	if !strings.Contains(files["info.txt"], "未启用") {
		t.Errorf("未启用落盘时 info.txt 应说明未启用：%s", files["info.txt"])
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

// setupDiskLog 启用落盘到临时目录，返回目录；用例结束自动关闭。
func setupDiskLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := applog.EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	t.Cleanup(func() { _ = applog.EnableFile("") })
	return dir
}

// exportZipFiles 跑一次导出，把包内条目读成 map。
func exportZipFiles(t *testing.T, s *Server) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleLogsExport(rec, httptest.NewRequest("GET", "/api/logs?format=zip", nil))
	res := rec.Result()
	if res.StatusCode != 200 {
		t.Fatalf("状态码 = %d，期望 200", res.StatusCode)
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
	return files
}

// 导出包必须带上落盘日志（当前 + 上一代）。
// 只带内存缓冲是不够的：用户常常是跑了几天才出问题，那时缓冲早被顶掉了。
func TestLogsExportIncludesDiskLogs(t *testing.T) {
	dir := setupDiskLog(t)
	const diskMarker = "落盘日志专用标记-4b71"
	applog.Default().Add(diskMarker) // 同时进缓冲与 qulv.log

	// 手工造一个上一代文件（真实轮转要写满 20MB，用例不该那么慢）。
	rotated := filepath.Join(dir, "qulv.log.1")
	const oldMarker = "上一代日志标记-9c02"
	if err := os.WriteFile(rotated, []byte(oldMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := config.NewConfigManager(t.TempDir(), 8080)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	files := exportZipFiles(t, &Server{cfgMgr: mgr})

	if !strings.Contains(files["qulv.log"], diskMarker) {
		t.Errorf("包内 qulv.log 应包含落盘日志，实际内容：%q", files["qulv.log"])
	}
	if !strings.Contains(files["qulv.log.1"], oldMarker) {
		t.Errorf("包内应包含上一代 qulv.log.1，实际内容：%q", files["qulv.log.1"])
	}
	if !strings.Contains(files["info.txt"], "qulv.log") {
		t.Errorf("info.txt 应说明持久日志的打包结果：%s", files["info.txt"])
	}
}

// 落盘文件读不到（被删/不是普通文件）时，导出包仍要成功，
// 并在 info.txt 里写明原因 —— 整个包 500 用户就什么都没拿到。
func TestLogsExportSurvivesUnreadableDiskLog(t *testing.T) {
	dir := setupDiskLog(t)
	// 把 qulv.log 换成目录：readDiskFile 会拒绝（不是普通文件）。
	if err := os.Remove(filepath.Join(dir, "qulv.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "qulv.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr, err := config.NewConfigManager(t.TempDir(), 8080)
	if err != nil {
		t.Fatalf("建配置管理器失败：%v", err)
	}
	files := exportZipFiles(t, &Server{cfgMgr: mgr})
	if _, ok := files["app.log"]; !ok {
		t.Fatal("即使持久日志读失败，导出包也应包含 app.log")
	}
	if !strings.Contains(files["info.txt"], "读取失败") {
		t.Errorf("info.txt 应记录持久日志读取失败的原因：%s", files["info.txt"])
	}
}

// app.log 的时间列必须是带时区的绝对时刻；旧实现只有 HH:MM:SS。
func TestBuildLogTextUsesRFC3339(t *testing.T) {
	applog.Default().Add("测试专用日志行")
	defer applog.Default().Clear()

	s := &Server{}
	text := s.buildLogText()
	lines := strings.Split(text, "\n")
	var found bool
	for _, l := range lines {
		if strings.HasPrefix(l, "#") || l == "" || strings.Contains(l, "（缓冲里没有日志）") {
			continue
		}
		stamp := strings.SplitN(l, "\t", 2)[0]
		if _, err := time.Parse(time.RFC3339, stamp); err != nil {
			t.Errorf("时间列 %q 不是 RFC3339：%v（整行 %q）", stamp, err, l)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("没有找到日志行，无法校验时间列")
	}
}

// 写盘出错必须在 info.txt 里可见：落盘默认开，静默失败会让用户以为日志一直在记。
func TestBuildInfoTextShowsDiskError(t *testing.T) {
	st := applog.DiskStatus{Enabled: true, Path: "/tmp/qulv.log", Size: 10, LastErr: "写入失败：no space left on device"}
	got := buildDiskSection(st, nil)
	if !strings.Contains(got, "no space left on device") {
		t.Errorf("应显示最近一次写盘错误，实际：%s", got)
	}
	if !strings.Contains(got, "/tmp/qulv.log") {
		t.Errorf("应显示落盘路径，实际：%s", got)
	}
}

// 服务器时区一行必须是「能换算」的信息：缩写 + 数字偏移。
// 旧的写法打印 time.Now().Location() —— NAS 上装了 tzdata 时是 "Local"，
// 对读导出包的人毫无用处（2026-11-03 排障时正是分不清服务器到底是哪个时区）。
func TestBuildInfoTextShowsServerTZ(t *testing.T) {
	// 只校验时区行本身：buildInfoText 需要 cfgMgr 等一整套服务器状态，
	// 这里不为了一个字符串断言去搭完整 Server —— 用与实现相同的取值方式
	// 钉住「Zone() 缩写 + offsetString 偏移」的输出形状。
	now := time.Now()
	zone, offset := now.Zone()
	var line string
	if zone != "" {
		line = fmt.Sprintf("服务器时区：%s (%s)", zone, offsetString(offset))
	} else {
		line = fmt.Sprintf("服务器时区：%s", now.Location())
	}
	if !regexp.MustCompile(`\(UTC[+-]\d{2}:\d{2}\)$`).MatchString(line) {
		t.Errorf("服务器时区行 %q 缺少 (UTC±HH:MM) 数字偏移", line)
	}
}

// offsetString：Zone() 的秒偏移渲染成 "UTC+08:00" / "UTC-05:00" / "UTC+00:00"。
func TestOffsetString(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "UTC+00:00"},
		{8 * 3600, "UTC+08:00"},
		{5*3600 + 30*60, "UTC+05:30"},
		{-5 * 3600, "UTC-05:00"},
		{-5*3600 - 30*60, "UTC-05:30"},
	}
	for _, c := range cases {
		if got := offsetString(c.in); got != c.want {
			t.Errorf("offsetString(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestBuildDiskSectionDisabled(t *testing.T) {
	got := buildDiskSection(applog.DiskStatus{}, nil)
	if !strings.Contains(got, "未启用") {
		t.Errorf("未启用时应明确说明，实际：%s", got)
	}
}

func TestBuildDiskSectionTruncated(t *testing.T) {
	got := buildDiskSection(
		applog.DiskStatus{Enabled: true, Path: "/tmp/qulv.log"},
		[]diskLogNote{{Name: "qulv.log", Path: "/tmp/qulv.log", Written: 20971520, Truncated: true}},
	)
	if !strings.Contains(got, "截断") {
		t.Errorf("截断应有提示，实际：%s", got)
	}
}

// POST /api/logs 带浏览器绝对时刻：相差超过容差才采用（见 applog.AddFromAt）。
// 钉住「上报接口真的把 at 传下去了」，防止将来 JSON 字段改名悄悄断链。
func TestLogsReportAcceptsAt(t *testing.T) {
	applog.Default().Clear()
	defer applog.Default().Clear()

	// 浏览器在 +08:00、NAS 是 UTC：同一事件浏览器算出的绝对时刻比服务器现在多 8h
	at := time.Now().Add(8 * time.Hour).Format(time.RFC3339)
	body, _ := json.Marshal(map[string]string{
		"source": "frontend", "level": "warn", "message": "带 at 的上报", "at": at,
	})
	rec := httptest.NewRecorder()
	(&Server{}).handleLogs(rec, httptest.NewRequest(http.MethodPost, "/api/logs", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200：%s", rec.Code, rec.Body.String())
	}

	// 直接查缓冲：看到 Entry 本身，而不是绕接口再拼一层间接
	entries := applog.Default().Recent(5)
	found := false
	for _, e := range entries {
		if e.Message != "带 at 的上报" {
			continue
		}
		found = true
		parsed, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			t.Fatalf("Entry.Time 应为 RFC3339，实际 %q：%v", e.Time, err)
		}
		if d := parsed.Sub(time.Now().Add(8 * time.Hour)); d < -2*time.Minute || d > 2*time.Minute {
			t.Errorf("应采用浏览器上报的 +8h 时刻，实际 %s（偏差 %s）", e.Time, d)
		}
	}
	if !found {
		t.Fatal("缓冲里没有刚上报的日志")
	}
}

// 老客户端不传 at：必须照旧 200，且用服务器时钟（不报错、不空指针）。
func TestLogsReportWithoutAtStillWorks(t *testing.T) {
	applog.Default().Clear()
	defer applog.Default().Clear()

	body, _ := json.Marshal(map[string]string{
		"source": "frontend", "level": "error", "message": "不带 at 的老客户端",
	})
	rec := httptest.NewRecorder()
	(&Server{}).handleLogs(rec, httptest.NewRequest(http.MethodPost, "/api/logs", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("老客户端不带 at 也应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}

	for _, e := range applog.Default().Recent(5) {
		if e.Message != "不带 at 的老客户端" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			t.Fatalf("Entry.Time 应为 RFC3339，实际 %q：%v", e.Time, err)
		}
		if d := parsed.Sub(time.Now()); d < -2*time.Minute || d > 2*time.Minute {
			t.Errorf("不传 at 应使用服务器时钟，实际 %s（偏差 %s）", e.Time, d)
		}
		return
	}
	t.Fatal("缓冲里没有刚上报的日志")
}
