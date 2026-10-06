// 远程图片的安全下载。
//
// 为什么放在 security 包：这里唯一的技术难点就是 **SSRF 防护** ——
// 封面地址来自第三方搜索接口，属于「用户可控的外部输入」，不能直接拿去请求。
// 复用本包已有的 ValidateURL（逐跳校验重定向目标）/ NewSafeClient / LimitedRead。
//
// 为什么需要它：`tidy.TidyOne` **刻意不下载封面**（见 pkg/tidy/tidy.go 的注释），
// 要求调用方把字节传进来。但 `tidy.Item.CoverBytes` 是 `json:"-"`，
// HTTP 调用方**根本无法**通过 JSON 传字节 —— 所以「谁负责下载」这件事必须有个公共实现，
// 否则封面功能就是个死结（这个缺口 2026-09-18 端到端测试才暴露出来）。
package security

import (
	"net/http"
	"strings"
	"time"
)

// 封面体积上限。封面正常几十 KB～几百 KB，12MB 足够，同时防住「超大文件打爆内存」。
const defaultImageMaxBytes = 12 << 20

// FetchImage 下载一张远程图片，返回 (字节, MIME)。
//
// 只接受能按魔数识别为 JPEG / PNG / GIF 的内容 —— 不信任 Content-Type
// （上游可能返回一个 HTML 错误页却标着 image/jpeg）。
// 任何一步不满足都返回 (nil, "")，调用方据此跳过封面即可，不需要区分失败原因。
//
// referer 用于绕过部分 CDN 的防盗链；不需要时传空串。
func FetchImage(rawURL, referer string) ([]byte, string) {
	return FetchImageWithLimit(rawURL, referer, defaultImageMaxBytes)
}

// FetchImageWithLimit 同 FetchImage，但可指定体积上限。
func FetchImageWithLimit(rawURL, referer string, maxBytes int64) ([]byte, string) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "http") {
		return nil, ""
	}
	if maxBytes <= 0 {
		maxBytes = defaultImageMaxBytes
	}

	opts := Options{MaxBodyBytes: maxBytes}
	if _, err := ValidateURL(rawURL, opts); err != nil {
		return nil, ""
	}

	client := NewSafeClient(opts, 20*time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}

	data, err := LimitedRead(resp.Body, opts)
	if err != nil || len(data) < 100 {
		return nil, ""
	}
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return data, "image/jpeg"
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return data, "image/png"
	case len(data) >= 6 && strings.HasPrefix(string(data), "GIF8"):
		return data, "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return data, "image/webp"
	}
	return nil, ""
}
