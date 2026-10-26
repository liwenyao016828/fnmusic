package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"fn-lx-player/pkg/fnos"
)

// GET /api/fnos/status 的「按谁在问分档」逻辑（2026-10-25 安全整改）。
//
// 这个接口默认吃「内网免校验」（auth_mode=lan），所以它自己必须收着点：
// 同网段任何设备都能打到这里，应答里不该有**目录绝对路径**与**账号名**。
//
// 用假 gateway 数据源注入，不依赖真机飞牛环境。

// withFakeFnos 把 fnos 的两个数据源换成固定值，返回恢复函数。
func withFakeFnos(t *testing.T) {
	t.Helper()
	// 开放 API 的 Available() 认「环境变量 TRIM_API_TOKEN + socket 存在」。
	// 测试里把 socket 指到一个临时文件，就能让 Available() 为真而不碰真机。
	oldAvail := fnos.SetAvailableForTest(true)
	t.Cleanup(func() { fnos.SetAvailableForTest(oldAvail) })
	oldFolders := fnos.SetSharedFoldersSourceForTest(func() ([]string, error) {
		return []string{"/vol1/1000/music", "/vol1/1000/downloads"}, nil
	})
	oldMusic := fnos.SetMusicStatusSourceForTest(func() (bool, map[string]any, error) {
		return true, map[string]any{"guid": "g-1", "name": "liwenyao", "role": "admin"}, nil
	})
	// 开放 API 的 Available() 认环境变量 + socket 存在性；测试里用环境变量顶一下，
	// socket 那条走不到（Call 已被上面的假数据源绕过）。

	t.Cleanup(func() {
		fnos.SetSharedFoldersSourceForTest(oldFolders)
		fnos.SetMusicStatusSourceForTest(oldMusic)
	})
}

func fnosStatusFrom(t *testing.T, remoteAddr string) map[string]any {
	t.Helper()
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/fnos/status", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	s.HandleFnosStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("该 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	return decodeData(t, rec)
}

// 同网段来源：不能看到目录路径与账号名。
func TestFnosStatusHidesContentFromLAN(t *testing.T) {
	withFakeFnos(t)
	data := fnosStatusFrom(t, "192.168.1.50:51234")

	for _, key := range []string{"shared_folders", "music_account", "shared_folders_error", "music_error"} {
		if _, ok := data[key]; ok {
			t.Fatalf("内网来源不该拿到 %q，实际应答：%+v", key, data)
		}
	}
	// 但「有没有授权目录」这件事必须还能表达出来 —— 界面要用
	if got, _ := data["shared_folder_count"].(float64); int(got) != 2 {
		t.Fatalf("内网来源该拿到授权目录**数量**，得到 %+v", data["shared_folder_count"])
	}
	if data["music_available"] == nil || data["open_api_available"] == nil {
		t.Fatalf("布尔状态不能省，否则界面看不出连没连上：%+v", data)
	}
}

// 本机来源：保留完整内容（App 自己的界面要用）。
func TestFnosStatusKeepsContentForLoopback(t *testing.T) {
	withFakeFnos(t)
	data := fnosStatusFrom(t, "127.0.0.1:40000")

	folders, ok := data["shared_folders"].([]any)
	if !ok || len(folders) != 2 {
		t.Fatalf("本机来源该拿到目录清单，得到 %+v", data["shared_folders"])
	}
	account, ok := data["music_account"].(map[string]any)
	if !ok || account["name"] != "liwenyao" {
		t.Fatalf("本机来源该拿到账号信息，得到 %+v", data["music_account"])
	}
}

// IPv6 回环也算本机（飞牛桌面可能经 ::1 连过来）。
func TestFnosStatusIPv6Loopback(t *testing.T) {
	withFakeFnos(t)
	data := fnosStatusFrom(t, "[::1]:40000")
	if _, ok := data["shared_folders"]; !ok {
		t.Fatalf("::1 也该算本机，得到 %+v", data)
	}
}

// ⚠️ 这条守的是「别拿请求头当身份」：伪造 X-Forwarded-For: 127.0.0.1
// 也必须还是看得出是外网来源（本测试里 RemoteAddr 是内网）。
func TestFnosStatusIgnoresForwardedHeaders(t *testing.T) {
	withFakeFnos(t)
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/fnos/status", nil)
	req.RemoteAddr = "192.168.1.50:51234"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	rec := httptest.NewRecorder()
	s.HandleFnosStatus(rec, req)

	data := decodeData(t, rec)
	if _, ok := data["shared_folders"]; ok {
		t.Fatal("伪造 X-Forwarded-For 不该升级为本机来源")
	}
}

// 令牌来源是枚举（manual/env/db/none），两种来源都该能看到 —— 界面靠它显示徽章。
func TestFnosStatusTokenSourceAlwaysVisible(t *testing.T) {
	withFakeFnos(t)
	for _, addr := range []string{"127.0.0.1:1", "10.0.0.9:1"} {
		data := fnosStatusFrom(t, addr)
		if _, ok := data["token_source"]; !ok {
			t.Fatalf("来源 %s 也该看到 token_source（枚举，不含令牌）：%+v", addr, data)
		}
	}
}
