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
	neteaseRef    = "https://music.163.com/"
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
