package search

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// ── 全局限速：所有出站请求共用一个进程级节流器 ──
//
// 为什么必须有这一层：补全/整理是**并发**跑的（`complete.Options.Concurrency`
// 默认 2、`tidy.TidyBatch` 也是 2），而每首歌会按平台顺序搜多次、还要取歌词 /
// 封面 / 专辑详情。没有任何节流时，上游看到的是「并发数 × 单个调用点的速率」，
// 几个批量任务叠在一起就会给平台造成明显压力（也更容易被风控拦下，反而拿不到结果）。
//
// 为什么必须是**全局单例**：如果每个调用点各自限速，总速率会被并发数放大。参考实现
// music-meta-web v1.4.8 的 `ratelimit.py` 正因为这点把节流器做成进程级单例，
// 并注明「per-worker 限速会把上游 QPS 乘 worker 数」。
//
// 抖动：每次间隔取 `interval × (0.5~1.5)`，避免固定周期被识别成脚本。
// 抖动用纳秒时钟取低位，不引入 math/rand —— 后端刻意保持零第三方依赖。

// defaultSearchMinInterval 出站请求的默认最小间隔；0 = 不限速。
//
// 可用环境变量 `FN_SEARCH_MIN_INTERVAL_MS`（毫秒，0 = 关闭）覆盖，与其它 `FN_*`
// 变量同风格：上游风控变严时用户不必改代码就能调慢，自动化刷库时也能临时关掉。
const defaultSearchMinInterval = 300 * time.Millisecond

var (
	throttleMu       sync.Mutex
	throttleNextSlot time.Time // 下一次允许发出请求的最早时刻
	throttleInterval = defaultSearchMinInterval
)

// MinInterval 当前的最小请求间隔（0 = 不限速）。
func MinInterval() time.Duration {
	throttleMu.Lock()
	defer throttleMu.Unlock()
	return throttleInterval
}

// SetMinInterval 调整最小请求间隔（0 = 关闭限速）。给配置层与测试用。
func SetMinInterval(d time.Duration) {
	throttleMu.Lock()
	throttleInterval = d
	throttleNextSlot = time.Time{}
	throttleMu.Unlock()
}

// throttle 发请求前调用：为自己的请求**预约一个时间片**，然后等到那一刻。
//
// 用「预约」而不是「持锁 sleep」：持锁 sleep 会把所有请求串成一条线（连 HTTP 往返
// 都排队），而预约只约束**发出时刻**，请求本身仍可并发往返。
func throttle() {
	throttleMu.Lock()
	interval := throttleInterval
	if interval <= 0 {
		throttleMu.Unlock()
		return
	}
	now := time.Now()
	slot := now
	if throttleNextSlot.After(now) {
		slot = throttleNextSlot
	}
	jitter := 0.5 + float64(time.Now().UnixNano()%1000)/1000.0
	throttleNextSlot = slot.Add(time.Duration(float64(interval) * jitter))
	throttleMu.Unlock()

	if wait := time.Until(slot); wait > 0 {
		time.Sleep(wait)
	}
}

// doHTTP 是包内**唯一**的出站请求出口：先过全局限速，再发请求。
// 平台搜索 / 歌词 / 封面 / 专辑详情都必须走这里 —— 分散限速等于没限速。
//
// 顺带承担第二件事：把这次的成败记进 diag（见 diag.go）。放在这一层，
// 是因为「十个调用点各自上报」漏一个就静默失真，而这个包以前正是
// 在失败路径上只有 `return nil` —— QQ 接口下线后界面上只剩「搜到 0 条」，
// 查起来无从下手。
func doHTTP(req *http.Request) (*http.Response, error) {
	return doHTTPFor("", req)
}

// doHTTPFor 同 doHTTP，但可以显式指定平台代号；platform 传空 = 按 URL 自动判断。
// 只有在 URL 认不出平台时才需要显式传（例如走代理的同域接口）。
func doHTTPFor(platform string, req *http.Request) (*http.Response, error) {
	if platform == "" {
		platform = platformOf(req.URL.String())
	}
	throttle()
	resp, err := httpClient.Do(req)
	if err != nil {
		recordFailure(platform, "transport", err.Error())
		return resp, err
	}
	if resp.StatusCode != http.StatusOK {
		// 调用方拿到非 200 只会 return nil，不读 body。但错误原因往往就写在
		// body 开头（QQ 那次的 500 是空的 nginx 页 —— 正是「什么都不说」暴露了它），
		// 所以这里读一小段留证，再把 body 装回去，调用方的语义一点不变。
		head, _ := io.ReadAll(io.LimitReader(resp.Body, diagBodyHead))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(head))
		recordFailure(platform, "http_status",
			fmt.Sprintf("HTTP %d %s", resp.StatusCode, bodySnippet(head)))
		return resp, nil
	}
	recordOK(platform)
	return resp, nil
}

// parseMinIntervalEnv 解析 FN_SEARCH_MIN_INTERVAL_MS；空串或非法值一律忽略（回默认），
// 负值也忽略 —— 「配错了」不能让整个搜索链路卡死或变无限流。
func parseMinIntervalEnv(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

func init() {
	if d, ok := parseMinIntervalEnv(os.Getenv("FN_SEARCH_MIN_INTERVAL_MS")); ok {
		throttleMu.Lock()
		throttleInterval = d
		throttleMu.Unlock()
	}
}
