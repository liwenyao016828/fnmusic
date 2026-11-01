package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fn-lx-player/pkg/nas"
)

// GET /api/fnos/auth-status 与 POST /api/fnos/auth-refresh。
//
// 这两条路径的意义：飞牛要求「用户显式授权目录，应用才看得到文件」。曲率以前
// 只靠「/vol1 起全都允许」，于是要么授权了也进不去（后端不认），要么什么都没
// 授权却能读整个卷（不符合权限模型）。这里钉住三件事：
//  1. 未授权时给出提示与可执行的动作（前端据此显示提示条）；
//  2. 非飞牛环境不给提示（本地开发不该看到红条）；
//  3. 路径只对回环来源返回（同网段设备不该拿到目录结构）。

func authStatusFrom(t *testing.T, remoteAddr string) map[string]any {
	t.Helper()
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/fnos/auth-status", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	s.HandleFnosAuthStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("该 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	return decodeData(t, rec)
}

func TestAuthStatusHintsWhenNotAuthorized(t *testing.T) {
	withFakeFnos(t)
	// 授权目录全部换成空：模拟「用户还没授权任何目录」。
	swapFakeSharedFolders(t, nil)

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["authorized"] == true {
		t.Fatalf("没有任何授权目录时 authorized 必须为 false：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint == "" {
		t.Fatalf("未授权时必须给出提示文案：%+v", data)
	}
	if data["hint_action"] != "grant" {
		t.Fatalf("未授权时 hint_action 该是 grant，实际 %v", data["hint_action"])
	}
}

func TestAuthStatusSilentOutsideFnos(t *testing.T) {
	withFakeFnos(t)
	swapFakeDetected(t, false)
	swapFakeSharedFolders(t, nil)

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["available"] == true {
		t.Fatalf("非飞牛环境 available 必须为 false：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint != "" {
		t.Fatalf("非飞牛环境不该显示授权提示，实际 %q", hint)
	}
}

// 真机实测的故障形态：**在飞牛里，但系统没注入 TRIM_API_TOKEN**。
// 这时 available（是不是飞牛）必须是 true，提示条必须出现 —— 否则用户既看不到
// 提示、也没有任何授权目录，界面只剩一片空白。
func TestAuthStatusHintsEvenWhenTokenMissing(t *testing.T) {
	withFakeFnos(t)
	swapFakeAvailable(t, false) // 开放 API 调不通
	swapFakeSharedFolders(t, nil)

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["available"] != true {
		t.Fatalf("在飞牛环境里 available 必须为 true（与 token 无关）：%+v", data)
	}
	if data["api_available"] != false {
		t.Fatalf("没 token 时 api_available 该是 false：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint == "" {
		t.Fatalf("没 token 且没授权时必须给出提示文案：%+v", data)
	}
}

func TestAuthStatusAuthorizedWhenSharedGranted(t *testing.T) {
	withFakeFnos(t) // 默认两目录

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["authorized"] != true {
		t.Fatalf("有共享授权时应视为已授权：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint != "" {
		t.Fatalf("已授权时不该再提示，实际 %q", hint)
	}
	if n, _ := data["shared_count"].(float64); int(n) != 2 {
		t.Fatalf("shared_count 该是 2，实际 %v", data["shared_count"])
	}
}

// 同网段来源：可以有布尔与计数，但**不能有路径**。
func TestAuthStatusHidesPathsFromLAN(t *testing.T) {
	withFakeFnos(t)
	data := authStatusFrom(t, "192.168.1.50:51234")
	for _, key := range []string{"roots", "fnos_roots"} {
		if _, ok := data[key]; ok {
			t.Fatalf("内网来源不该拿到 %q：%+v", key, data)
		}
	}
	if _, ok := data["shared_count"]; !ok {
		t.Fatalf("计数是允许的，shared_count 应该在：%+v", data)
	}
}

func TestAuthRefreshOnlyPost(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/fnos/auth-refresh", nil)
	rec := httptest.NewRecorder()
	s.HandleFnosAuthRefresh(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 该 405，得到 %d", rec.Code)
	}
}

func TestAuthRefreshReturnsCounts(t *testing.T) {
	withFakeFnos(t)
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/fnos/auth-refresh", nil)
	rec := httptest.NewRecorder()
	s.HandleFnosAuthRefresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 该 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	if _, ok := data["authorized"]; !ok {
		t.Fatalf("应答该带 authorized：%+v", data)
	}
}

// 真机事故复现：`config/resource` 的 `data-share: yinshu-ai/data` 让系统注入
// TRIM_DATA_SHARE_PATHS=/vol1/@appshare/yinshu-ai/data。那是曲率**自己**的数据目录，
// 旧逻辑把它算成「已授权」→ authorized 恒为 true → 前端提示条永远不出现，用户
// 一个音乐目录都没授权却看不到任何引导（2026-10-31 用户反馈「点了没反应」）。
//
// 现在 authorized 只看 FnOSGrantedRoots()（已剔除 data-share），所以这个场景必须
// 回到「未授权 + 有提示」。
func TestAuthStatusDataShareAloneIsNotAuthorized(t *testing.T) {
	withFakeFnos(t)
	swapFakeSharedFolders(t, nil)

	own := filepath.Join(t.TempDir(), "@appshare", "yinshu-ai", "data")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("TRIM_DATA_SHARE_PATHS", own)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", "")
	nas.InvalidateFnOSRootsCache()
	t.Cleanup(nas.InvalidateFnOSRootsCache)

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["authorized"] == true {
		t.Fatalf("只有 data-share 自用目录时必须算「未授权」：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint == "" {
		t.Fatalf("只有 data-share 自用目录时必须给出授权提示：%+v", data)
	}
	// 但「所有能访问的飞牛路径」里仍然要有它（路径守卫靠它写自己的数据目录）。
	if roots, _ := data["fnos_roots"].([]any); len(roots) == 0 {
		t.Fatalf("fnos_roots 该包含 data-share 目录：%+v", data)
	}
	if granted, _ := data["granted_roots"].([]any); len(granted) != 0 {
		t.Fatalf("granted_roots 必须是空的（那是用户授权，不是自用目录）：%+v", data)
	}
}

// 反向：真给了音乐目录，granted_roots 要出现，authorized 要变 true。
func TestAuthStatusRealGrantCountsEvenWithDataShare(t *testing.T) {
	withFakeFnos(t)
	base := t.TempDir()
	own := filepath.Join(base, "@appshare", "yinshu-ai", "data")
	music := filepath.Join(base, "vol3", "1000", "音乐")
	for _, d := range []string{own, music} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	swapFakeSharedFolders(t, nil)
	t.Setenv("TRIM_DATA_SHARE_PATHS", own)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", music)
	nas.InvalidateFnOSRootsCache()
	t.Cleanup(nas.InvalidateFnOSRootsCache)

	data := authStatusFrom(t, "127.0.0.1:5000")
	if data["authorized"] != true {
		t.Fatalf("有真实授权目录时应算已授权：%+v", data)
	}
	if hint, _ := data["hint"].(string); hint != "" {
		t.Fatalf("已授权时不该再提示，实际 %q", hint)
	}
	granted, _ := data["granted_roots"].([]any)
	if len(granted) != 1 {
		t.Fatalf("granted_roots 该只有音乐目录，实际 %+v", granted)
	}
}
