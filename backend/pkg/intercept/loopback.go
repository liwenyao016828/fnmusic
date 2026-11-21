package intercept

import (
	"context"
	"io"
	"net/http"
)

// newLocalRequest 构造一条发往**本机曲率后端**的请求，并带上它要求的凭据。
//
// # 为什么需要它（2026-11-20 修正）
//
// 拦截层里所有「转给本机后端」的请求（榜单详情 / 指令条 / 下载落盘）以前都
// 不带任何凭据，注释的理由是「后端的 AUTH 中间件对本机/内网地址免校验」——
// 这句话只在 `lan` / `none` 模式下成立。
//
// 用户一旦设了 `FN_API_TOKEN`（`FN_AUTH_MODE=token`），`/api/charts/detail`
// 对 127.0.0.1 同样要 `X-API-Token`：请求被 401 → 榜单取回空 → 而「空榜不注入」
// → 榜单卡片**静默消失**，日志里一个字都没有（这三个环节都不报错，只在自己的
// 作用域里返回零值）。这是同一类故障最容易重复出现的地方，所以统一收口在这里。
//
// 不带 Accept-Encoding / User-Agent 之类的自定义头：回环链路上没有代理，
// 越简单越不会被中间件改写。
func (i *Interceptor) newLocalRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if i.localToken != "" {
		req.Header.Set("X-API-Token", i.localToken)
	}
	return req, nil
}
