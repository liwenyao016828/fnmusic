package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fn-lx-player/pkg/security"
)

type RequestPayload struct {
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Form     map[string]string `json:"form"`
	Timeout  int               `json:"timeout"`
	IsBinary bool              `json:"is_binary"`
}

type ResponsePayload struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	IsBase64   bool              `json:"is_base64"`
	Error      string            `json:"error,omitempty"`
}

// 出站安全策略（禁止访问内网 / 回环 / 元数据地址，响应体限 8MB）
var proxyOpts = security.DefaultOptions()

// 单一共享 client：复用连接池，避免"每请求新建 Transport"导致的 socket 耗尽
var safeClient = security.NewSafeClient(proxyOpts, 60*time.Second)

// 允许透传的 HTTP 方法白名单
var allowedMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodPost: true,
	http.MethodHead: true,
}

// 禁止由调用方指定的逐跳 / 敏感请求头，避免请求走私与 Host 头注入
var blockedHeaders = map[string]bool{
	"host":                true,
	"connection":          true,
	"content-length":      true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"keep-alive":          true,
}

func writeJSON(w http.ResponseWriter, status int, payload ResponsePayload) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func HandleProxyHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ResponsePayload{Error: "Method not allowed"})
		return
	}

	var payload RequestPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, ResponsePayload{Error: "Invalid JSON request: " + err.Error()})
		return
	}

	// 1. URL 校验：协议白名单 + 内网/元数据地址拦截
	target, err := security.ValidateURL(payload.URL, proxyOpts)
	if err != nil {
		writeJSON(w, http.StatusForbidden, ResponsePayload{Error: err.Error()})
		return
	}

	// 2. 方法白名单
	method := strings.ToUpper(strings.TrimSpace(payload.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !allowedMethods[method] {
		writeJSON(w, http.StatusForbidden, ResponsePayload{
			Error: "仅允许 GET / POST / HEAD 方法，收到 " + method,
		})
		return
	}

	var reqBody io.Reader
	if len(payload.Form) > 0 {
		values := url.Values{}
		for k, v := range payload.Form {
			values.Set(k, v)
		}
		reqBody = strings.NewReader(values.Encode())
	} else if payload.Body != "" {
		reqBody = bytes.NewReader([]byte(payload.Body))
	}

	timeout := 15 * time.Second
	if payload.Timeout > 0 && payload.Timeout <= 120000 {
		timeout = time.Duration(payload.Timeout) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reqBody)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ResponsePayload{Error: "Failed to construct request: " + err.Error()})
		return
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	if len(payload.Form) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	for k, v := range payload.Headers {
		if blockedHeaders[strings.ToLower(strings.TrimSpace(k))] {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := safeClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ResponsePayload{
			StatusCode: 502,
			Error:      err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	respHeaders := make(map[string]string)
	for k, vv := range resp.Header {
		respHeaders[k] = strings.Join(vv, "; ")
	}

	// 3. 响应体限额，超限返回错误而不是静默截断
	respData, err := security.LimitedRead(resp.Body, proxyOpts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ResponsePayload{
			StatusCode: resp.StatusCode,
			Headers:    respHeaders,
			Error:      "Failed to read response body: " + err.Error(),
		})
		return
	}

	isBinary := payload.IsBinary || isBinaryContentType(resp.Header.Get("Content-Type"))
	bodyStr := ""
	if isBinary {
		bodyStr = base64.StdEncoding.EncodeToString(respData)
	} else {
		bodyStr = string(respData)
	}

	writeJSON(w, 0, ResponsePayload{
		StatusCode: resp.StatusCode,
		Headers:    respHeaders,
		Body:       bodyStr,
		IsBase64:   isBinary,
	})
}

func isBinaryContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "video/") ||
		strings.Contains(ct, "octet-stream") ||
		strings.Contains(ct, "zip") ||
		strings.Contains(ct, "gzip")
}
