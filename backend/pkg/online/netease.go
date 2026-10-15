package online

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NetEaseResolver 解析网易云音乐的直链。
//
// 用的是**免登录**的公开接口：
//
//	GET https://music.163.com/api/song/enhance/player/url?ids=[<id>,…]&br=320000
//
// 只要带 Referer，不需要 cookie 或登录。`ids=[…]` 原生支持批量，所以搜索时
// 一次请求就能验活一整页结果。
//
// 实测（2026-10-05）：免费曲目（`fee=8`）能拿到完整 mp3，`Range: bytes=0-4095`
// 返回 206、`audio/mpeg`、4096 字节、魔数 `ID3` —— 是真实音频而不是错误页。
// 付费曲目的 `url` 为 null → 判为不可播。
type NetEaseResolver struct{}

// Platform 实现 Resolver。
func (NetEaseResolver) Platform() string { return "wy" }

const (
	neteaseURLAPI = "https://music.163.com/api/song/enhance/player/url"
	// neteaseURLAPIV1 是带 `level` 参数的**新版**接口，按档位取链只能走它。
	//
	// ⚠️ `encodeType` 必填（实测 2026-10-07）：不带它时接口返回 HTTP 200 +
	// 一个**重复了两遍**的 `{"msg":"参数错误","code":400}`（content-length 66），
	// 看起来像被限流，其实只是少了个参数。取值无所谓（flac / mp3 都行）。
	neteaseURLAPIV1 = "https://music.163.com/api/song/enhance/player/url/v1"
	neteaseRef      = "https://music.163.com/"
	// neteaseMaxBatch 是一次批量请求最多问多少条。接口本身能收更多，但这个
	// 数量已经覆盖一整页搜索结果，再大只会拉长单次超时窗口。
	neteaseMaxBatch = 50
	// neteaseDefaultTTL 是接口没给 expi 时的兜底有效期。实测 URL 路径里带
	// 生成时间戳，约 20 分钟失效，取 20 分钟。
	neteaseDefaultTTL = 20 * time.Minute
)

type neteaseURLResponse struct {
	Data []struct {
		ID        int64  `json:"id"`
		URL       string `json:"url"`
		Br        int    `json:"br"`
		Size      int64  `json:"size"`
		Code      int    `json:"code"`
		Type      string `json:"type"`
		Level     string `json:"level"`
		Fee       int    `json:"fee"`
		Expi      int    `json:"expi"`
		Encode    string `json:"encodeType"`
		FreeTrial struct {
			Privilege struct {
				FreeTrialPrivilege bool `json:"freeTrialPrivilege"`
			} `json:"freeTrialPrivilege"`
		} `json:"freeTrialInfo"`
	} `json:"data"`
}

// Resolve 实现 Resolver。
func (r NetEaseResolver) Resolve(ctx context.Context, platformID string) (*Resolved, error) {
	got, err := r.ResolveBatch(ctx, []string{platformID})
	if err != nil {
		return nil, err
	}
	if res, ok := got[platformID]; ok {
		return res, nil
	}
	return nil, ErrNotPlayable
}

// ResolveBatch 实现 Resolver。批量接口按 id 匹配回填，不依赖响应顺序。
func (r NetEaseResolver) ResolveBatch(ctx context.Context, platformIDs []string) (map[string]*Resolved, error) {
	out := make(map[string]*Resolved, len(platformIDs))
	if len(platformIDs) == 0 {
		return out, nil
	}

	// 接口对空/非法 id 的处理不可靠，先滤掉非数字的。
	valid := make([]string, 0, len(platformIDs))
	for _, id := range platformIDs {
		if isDigits(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}

	var lastErr error
	for start := 0; start < len(valid); start += neteaseMaxBatch {
		end := start + neteaseMaxBatch
		if end > len(valid) {
			end = len(valid)
		}
		batch := valid[start:end]

		// 手工拼 query 以保留字面方括号 —— 接口实测认 `ids=[a,b]` 这个形状，
		// 用 url.Values 会把 `[` 转义成 `%5B`，虽然多数情况下也能用，但没必要
		// 引入这个不确定性。id 已过滤为纯数字，没有转义需求。
		endpoint := fmt.Sprintf("%s?ids=[%s]&br=320000", neteaseURLAPI, strings.Join(batch, ","))
		req, err := newRequest(ctx, http.MethodGet, endpoint, neteaseRef)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := doJSON(sharedClient, req, 1<<20)
		if err != nil {
			lastErr = err
			continue
		}

		var parsed neteaseURLResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = fmt.Errorf("解析网易响应失败: %w", err)
			continue
		}
		for _, item := range parsed.Data {
			if item.URL == "" {
				continue
			}
			out[fmt.Sprintf("%d", item.ID)] = &Resolved{
				URL:     item.URL,
				Format:  neteaseFormat(item.Type, item.Encode, item.URL),
				Size:    item.Size,
				Bitrate: item.Br,
				Expires: time.Now().Add(neteaseTTL(item.Expi)),
			}
		}
	}
	return out, lastErr
}

func neteaseTTL(expi int) time.Duration {
	if expi > 0 {
		return time.Duration(expi) * time.Second
	}
	return neteaseDefaultTTL
}

// neteaseFormat 推断播放格式，优先用接口给的 type，其次 encodeType，最后看 URL 后缀。
func neteaseFormat(typ, encode, rawURL string) string {
	for _, candidate := range []string{typ, encode} {
		c := strings.ToLower(strings.TrimSpace(candidate))
		if c != "" && c != "null" {
			return c
		}
	}
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.LastIndexByte(rawURL, '.'); i >= 0 && i+1 < len(rawURL) {
		if ext := strings.ToLower(rawURL[i+1:]); len(ext) <= 5 {
			return ext
		}
	}
	return "mp3"
}

// doJSON 发请求并读回 body（带上限，防止上游返回异常大响应把内存吃光）。
func doJSON(client *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	return body, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 按档位解析（TierResolver）
// ---------------------------------------------------------------------------

// neteaseTierLevel 把我们的档位翻成 v1 接口的 level 参数。
func neteaseTierLevel(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TierLossless:
		return "lossless"
	case Tier320k:
		return "exhigh"
	case Tier192k:
		return "higher"
	case Tier128k:
		return "standard"
	}
	return ""
}

// neteaseTierOf 把接口**回读**出来的 level / br / type 翻回我们的档位。
//
// ⚠️ 这是「请求无损却拿到有损」唯一的可靠判据。实测（2026-10-07，免登录）：
//
//	level=lossless → 回 level=exhigh, br=320000, type=mp3（**静默降级，不报错**）
//	level=hires    → 同上
//
// 也就是说：**不能拿请求的档位当结果**。不回读就会把 320k mp3 当无损报给用户。
func neteaseTierOf(level string, br int, typ string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "lossless", "hires":
		// 阶梯里不为 hires 单开一格：对用户来说「是不是无损」才是那个决策点。
		return TierLossless
	case "exhigh":
		return Tier320k
	case "higher":
		return Tier192k
	case "standard":
		return Tier128k
	}
	// level 认不出时按码率/容器兜 —— 接口版本变化时不至于把档位显示成空。
	if br > 0 {
		switch {
		case br >= 500_000:
			return TierLossless
		case br >= 256_000:
			return Tier320k
		case br >= 160_000:
			return Tier192k
		case br >= 112_000:
			return Tier128k
		}
	}
	if strings.EqualFold(strings.TrimSpace(typ), "flac") {
		return TierLossless
	}
	return ""
}

// ResolveAtTier 实现 TierResolver：按指定档位取一条直链。
//
// 档位认不出、或 id 不是纯数字时**退回默认解析**（`Resolve`）—— 不装作能按档位要，
// 因为 v1 接口对非法参数的反应是一个重复两遍的 400，看起来像故障。
func (r NetEaseResolver) ResolveAtTier(ctx context.Context, platformID, tier string) (*Resolved, error) {
	level := neteaseTierLevel(tier)
	id := strings.TrimSpace(platformID)
	if level == "" || !isDigits(id) {
		return r.Resolve(ctx, platformID)
	}

	endpoint := fmt.Sprintf("%s?ids=[%s]&level=%s&encodeType=flac", neteaseURLAPIV1, id, level)
	req, err := newRequest(ctx, http.MethodGet, endpoint, neteaseRef)
	if err != nil {
		return nil, err
	}
	body, err := doJSON(sharedClient, req, 1<<20)
	if err != nil {
		return nil, err
	}
	var parsed neteaseURLResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析网易响应失败: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, ErrTierUnavailable
	}

	item := parsed.Data[0]
	if item.URL == "" {
		// 曲目级不可播 与 「这一档没有」必须分开 —— 前者不该降档重试。
		// 实测 data[0].code：200 = 有 URL / 404 = 不可听 / -110 = 需购买。
		if item.Code == 404 || item.Code == -110 {
			return nil, ErrNotPlayable
		}
		return nil, ErrTierUnavailable
	}
	return &Resolved{
		URL:     item.URL,
		Format:  neteaseFormat(item.Type, item.Encode, item.URL),
		Size:    item.Size,
		Bitrate: item.Br,
		Expires: time.Now().Add(neteaseTTL(item.Expi)),
		Tier:    neteaseTierOf(item.Level, item.Br, item.Type),
	}, nil
}
