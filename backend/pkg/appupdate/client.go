package appupdate

import (
	"errors"
	"net/http"
	"time"

	"fn-lx-player/pkg/security"
)

// newUpdateClient 构造更新专用 HTTP 客户端。
//
// 为什么不用默认的 http.Client、也不用 security.NewSafeClient：
//   - 必须复用仓库的 SSRF 防护（重定向逐跳校验），这是全项目约定；
//   - 但 fpk 动辄几十 MB，NewSafeClient 给「经代理的尝试」加了 5 秒上限，
//     大文件流式读取会在 5 秒时被 cancel 掉（媒体中继因此才另开了
//     NewSafeStreamRoundTripper）。这里直接用 Stream 版本 + 手动逐跳校验。
//   - 总时长不设死：检查更新由调用方 ctx 控制，下载由 downloadTimeout 控制；
//     Client.Timeout 一旦生效会把 30 分钟的下载也砍掉。
func newUpdateClient() *http.Client {
	opts := security.DefaultOptions()
	opts.MaxBodyBytes = 512 << 20 // 只影响 LimitedRead；transport 不读它，留作语义完整
	return &http.Client{
		Transport: security.NewSafeStreamRoundTripper(opts),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			if _, err := security.ValidateURL(req.URL.String(), opts); err != nil {
				return err
			}
			return nil
		},
		Timeout: 0,
	}
}

// CheckTimeout 是检查更新请求的默认上限（不含下载）。
const CheckTimeout = 30 * time.Second
