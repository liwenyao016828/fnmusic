package fnos

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGateway 起一个监听 Unix socket 的假飞牛网关，返回预设响应。
// 返回 (socket 路径, 收到的请求体列表指针)。
func fakeGateway(t *testing.T, reply map[string]any) (string, *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gw.sock")

	received := &[]map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/trimapp", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_auth"] = r.Header.Get("Authorization")
		*received = append(*received, body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("无法创建 Unix socket，跳过: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path, received
}

func TestAvailableRequiresTokenAndSocket(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })

	t.Setenv("TRIM_API_TOKEN", "")
	socketPath = "/nonexistent/gw.sock"
	if Available() {
		t.Fatal("无 token 时 Available 应为 false")
	}

	t.Setenv("TRIM_API_TOKEN", "tok")
	if Available() {
		t.Fatal("socket 不存在时 Available 应为 false")
	}

	path, _ := fakeGateway(t, map[string]any{"code": 0})
	socketPath = path
	if !Available() {
		t.Fatal("token + socket 均就绪时 Available 应为 true")
	}
}

func TestCallRequestShapeAndAuth(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })

	path, received := fakeGateway(t, map[string]any{"code": 0, "msg": "", "data": map[string]any{}})
	socketPath = path
	t.Setenv("TRIM_API_TOKEN", "secret-token")

	if err := Call("trim.file.getSharedAccessibleFolders", nil, nil); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}

	reqs := *received
	if len(reqs) != 1 {
		t.Fatalf("应收到 1 次请求，实际 %d", len(reqs))
	}
	req := reqs[0]

	// 官方要求的四个顶层字段
	for _, k := range []string{"reqId", "req", "appName", "data"} {
		if _, ok := req[k]; !ok {
			t.Errorf("请求体缺少字段 %q", k)
		}
	}
	if req["req"] != "trim.file.getSharedAccessibleFolders" {
		t.Errorf("req = %v", req["req"])
	}
	if req["appName"] != appName {
		t.Errorf("appName = %v, 应与 manifest 的 appname 一致 (%s)", req["appName"], appName)
	}
	if req["_auth"] != "Bearer secret-token" {
		t.Errorf("Authorization = %v", req["_auth"])
	}
}

func TestCallWithoutToken(t *testing.T) {
	t.Setenv("TRIM_API_TOKEN", "")
	err := Call("trim.system.getPlatformConfig", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "TRIM_API_TOKEN") {
		t.Fatalf("无 token 时应报明确错误，实际: %v", err)
	}
}

func TestCallPropagatesNonZeroCode(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })

	path, _ := fakeGateway(t, map[string]any{"code": 1, "msg": "仅管理员可进行此操作", "data": []string{}})
	socketPath = path
	t.Setenv("TRIM_API_TOKEN", "tok")

	err := Call("trim.file.getSharedAccessibleFolders", nil, nil)
	if err == nil {
		t.Fatal("code != 0 时应返回错误")
	}
	if !strings.Contains(err.Error(), "仅管理员可进行此操作") {
		t.Errorf("错误信息应包含服务端 msg，实际: %v", err)
	}
}

func TestSharedFoldersParsesPaths(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })

	path, _ := fakeGateway(t, map[string]any{
		"code": 0, "msg": "",
		"data": map[string]any{"paths": []string{"/vol1/1000/data", "/vol2/music"}},
	})
	socketPath = path
	t.Setenv("TRIM_API_TOKEN", "tok")

	got, err := SharedFolders()
	if err != nil {
		t.Fatalf("SharedFolders 失败: %v", err)
	}
	want := []string{"/vol1/1000/data", "/vol2/music"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDelSharedFolderSendsPath(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })

	path, received := fakeGateway(t, map[string]any{"code": 0, "data": map[string]any{"suc": true}})
	socketPath = path
	t.Setenv("TRIM_API_TOKEN", "tok")

	if err := DelSharedFolder("/vol1/1000/data"); err != nil {
		t.Fatalf("DelSharedFolder 失败: %v", err)
	}
	data, _ := (*received)[0]["data"].(map[string]any)
	if data["path"] != "/vol1/1000/data" {
		t.Errorf("data.path = %v", data["path"])
	}
}

// 确保 client 真的走 Unix socket 而非 TCP：指向不存在的 socket 必须失败。
func TestCallUsesUnixSocket(t *testing.T) {
	origPath := socketPath
	t.Cleanup(func() { socketPath = origPath })
	socketPath = "/nonexistent/definitely-not-here.sock"
	t.Setenv("TRIM_API_TOKEN", "tok")

	err := Call("trim.system.getPlatformConfig", nil, nil)
	if err == nil {
		t.Fatal("socket 不存在时应失败")
	}
	if !strings.Contains(err.Error(), "调用 trim.system.getPlatformConfig 失败") {
		t.Errorf("错误应说明是调用失败，实际: %v", err)
	}
}
