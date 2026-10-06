// Package fnos 提供飞牛 fnOS 开放 API 调用能力。
//
// 官方文档：https://developer.fnnas.com/api/calling/
//
// 调用方式：经 Unix socket POST /api/v1/trimapp，请求体由 req 决定调用哪个能力。
// token 由系统在启动 cmd/main 时注入环境变量 TRIM_API_TOKEN，官方明确要求
// 每次调用都从环境变量读取、不得持久化到文件或配置。
package fnos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	apiURL = "http://localhost/api/v1/trimapp"
	// appName 必须与 fpk-package/manifest 的 appname 一致。
	// 飞牛按这个标识去查「管理员授权给本应用的目录」，写错了会拿不到授权目录。
	appName = "yinshu-ai"
)

// socketPath 为飞牛开放 API 的 Unix socket 路径；声明为变量以便测试注入。
var socketPath = "/var/run/trim_open_gateway_apiscope.socket"

var client = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	},
}

// Available 报告开放 API 是否可用：socket 存在，且系统已注入 token。
// 非飞牛环境（如本地开发）返回 false，调用方据此走回退逻辑。
func Available() bool {
	if os.Getenv("TRIM_API_TOKEN") == "" {
		return false
	}
	_, err := os.Stat(socketPath)
	return err == nil
}

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// Call 调用一个开放 API 能力。成功（code==0）时把 data 反序列化到 out；out 可为 nil。
func Call(req string, data any, out any) error {
	token := os.Getenv("TRIM_API_TOKEN")
	if token == "" {
		return fmt.Errorf("缺少 TRIM_API_TOKEN：非飞牛环境，或应用未声明对应 api-scope")
	}
	if data == nil {
		data = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"reqId":   fmt.Sprintf("%d", time.Now().UnixNano()),
		"req":     req,
		"appName": appName,
		"data":    data,
	})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用 %s 失败: %w", req, err)
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("调用 %s 返回非 JSON (HTTP %d): %w", req, resp.StatusCode, err)
	}
	if result.Code != 0 {
		return fmt.Errorf("调用 %s 失败 (code=%d): %s", req, result.Code, result.Msg)
	}
	if out != nil {
		return json.Unmarshal(result.Data, out)
	}
	return nil
}

// SharedFolders 返回管理员授权给本应用的目录，需声明 trim.file.sharedAccess。
func SharedFolders() ([]string, error) {
	var data struct {
		Paths []string `json:"paths"`
	}
	if err := Call("trim.file.getSharedAccessibleFolders", nil, &data); err != nil {
		return nil, err
	}
	return data.Paths, nil
}

// DelSharedFolder 删除一个应用共享授权目录。
func DelSharedFolder(path string) error {
	return Call("trim.file.delSharedAccessibleFolder", map[string]any{"path": path}, nil)
}

// PlatformConfig 读取系统语言与版本，需声明 trim.system.getPlatformConfig。
func PlatformConfig() (map[string]any, error) {
	var data map[string]any
	err := Call("trim.system.getPlatformConfig", nil, &data)
	return data, err
}
