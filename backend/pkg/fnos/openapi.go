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

// Available 报告开放 API 是否**可调用**：socket 存在，且系统已注入 token。
//
// ⚠️ 这个函数只回答「能不能现在就调开放 API」，**不能**用来判断「这是不是飞牛环境」。
// 真机实测（2026-10-31，fnOS 1.2.0903）：应用以 root 运行时系统可能没把
// TRIM_API_TOKEN 注入进来（app center 侧降级日志是
// `api signature verify failed, degraded: no api token granted`），此时
// Available() 为 false，但机器明明是飞牛。以前 AllowedRoots() 就是拿它当
// 「是否飞牛」的判据，于是把「没拿到 token」误判成「不在飞牛」，转而并入
// /vol1../vol3、/media、/mnt 的探测卷 —— 用户在应用设置里一个目录都没授权，
// 曲率却能列出整卷，正是用户反馈的那个问题。
//
// 判断「是不是飞牛环境」请用 Detected()。
func Available() bool {
	return availableSource()
}

// Detected 报告当前是否运行在飞牛 fnOS 环境里。
//
// 判据**只看部署特征，与 token 无关**（这是它与 Available 的关键区别）：
//
//	① 开放 API 的 Unix socket 存在（/var/run/trim_open_gateway_apiscope.socket）；
//	② 或者 /var/apps/<appname> 存在 —— 应用被飞牛应用中心安装过就一定有这个目录。
//
// 任一成立即认定为飞牛。这个判据故意宽松：**误判成飞牛的代价是「什么都不给看」**
// （fail closed，用户点一下授权就恢复），**误判成非飞牛的代价是「把整卷都给看」**
// （越权）。两害相权，宁可判成飞牛。
func Detected() bool {
	return detectedSource()
}

// detectedSource 供测试替换，见 SetDetectedForTest。
var detectedSource = func() bool {
	if _, err := os.Stat(socketPath); err == nil {
		return true
	}
	if _, err := os.Stat("/var/apps/" + appName); err == nil {
		return true
	}
	return false
}

// SetDetectedForTest 固定「是否飞牛环境」，返回原值供恢复。仅测试用。
func SetDetectedForTest(v bool) bool {
	old := detectedSource
	detectedSource = func() bool { return v }
	return old()
}

// availableSource 供测试替换，见 SetAvailableForTest。
var availableSource = func() bool {
	if os.Getenv("TRIM_API_TOKEN") == "" {
		return false
	}
	_, err := os.Stat(socketPath)
	return err == nil
}

// SetAvailableForTest 固定「开放 API 是否可用」，返回原值供恢复。仅测试用。
func SetAvailableForTest(v bool) bool {
	old := availableSource
	availableSource = func() bool { return v }
	return old()
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
// SharedFolders 返回管理员授权给本应用的共享目录（需声明 trim.file.sharedAccess）。
//
// sharedFoldersSource 供测试替换：真机之外的单元测试不该依赖飞牛 socket。
var sharedFoldersSource = func() ([]string, error) {
	var data struct {
		Paths []string `json:"paths"`
	}
	if err := Call("trim.file.getSharedAccessibleFolders", nil, &data); err != nil {
		return nil, err
	}
	return data.Paths, nil
}

// SetSharedFoldersSourceForTest 替换共享目录数据源，返回原来源供恢复。仅测试用。
func SetSharedFoldersSourceForTest(fn func() ([]string, error)) func() ([]string, error) {
	old := sharedFoldersSource
	if fn == nil {
		fn = func() ([]string, error) { return nil, nil }
	}
	sharedFoldersSource = fn
	return old
}

func SharedFolders() ([]string, error) {
	return sharedFoldersSource()
}

// DelSharedFolder 删除一个应用共享授权目录。
func DelSharedFolder(path string) error {
	return Call("trim.file.delSharedAccessibleFolder", map[string]any{"path": path}, nil)
}
