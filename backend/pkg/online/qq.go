package online

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// QQResolver 解析 QQ 音乐的直链。
//
// 用的是**免登录**的 vkey 接口：
//
//	POST https://u.y.qq.com/cgi-bin/musicu.fcg
//	{"req_0":{"module":"vkey.GetVkeyServer","method":"CgiGetVkey","param":{
//	   "guid":"10000","songmid":[…],"songtype":[0,…],"uin":"0",
//	   "loginflag":1,"platform":"20"}},
//	 "comm":{"uin":0,"format":"json","ct":24,"cv":0}}
//
// 直链 = `sip[0] + midurlinfo[i].purl`，`songmid` 原生支持批量。
//
// # 命中率
//
// **未登录时大约只有三分之一的曲目能解析出 purl**（实测「晴天」搜索结果
// 4/12）。其余返回 `result: 104003` —— 这是「该曲目需要付费/会员」的稳定
// 结论，不是故障，所以判为 ErrNotPlayable 并进负缓存。
//
// 正因为命中率不高，搜索合并时**必须**做可播性过滤：否则用户会看到一堆
// 点不动的 QQ 结果。这个过滤是搜索路径的一部分，不是可选优化。
type QQResolver struct{}

// Platform 实现 Resolver。
func (QQResolver) Platform() string { return "tx" }

const (
	qqVkeyAPI = "https://u.y.qq.com/cgi-bin/musicu.fcg"
	qqReferer = "https://y.qq.com/"
	// qqMaxBatch 是单次批量请求的曲目数上限。接口能收更多，取 50 覆盖一页搜索。
	qqMaxBatch = 50
	// qqTTL 是 vkey 的估计有效期。接口不返回过期时间；实测几小时内有效，
	// 但解析结果本身还有 5 分钟的池内缓存兜底，取 2 小时足够保守。
	qqTTL = 2 * time.Hour
	// qqGUID 是请求里固定的设备标识。官方 web 端也用固定值，不参与鉴权。
	qqGUID = "10000"
)

type qqVkeyParam struct {
	GUID      string   `json:"guid"`
	SongMid   []string `json:"songmid"`
	SongType  []int    `json:"songtype"`
	UIN       string   `json:"uin"`
	LoginFlag int      `json:"loginflag"`
	Platform  string   `json:"platform"`
	// Filename 是**档位选择器**：给了它，服务端就按你点的那一档给链
	// （见 qqTierFilename）。不传时服务端自己挑，实测给的是 128k m4a。
	//
	// ⚠️ 一条 songmid 只能配**一个** filename。塞多个会把整条请求毒掉：
	// 单独问 M500 是 result=0，和 F000 一起问就变成 101404（实测 2026-10-07）。
	// 所以阶梯必须逐档调用，不能一次问完。
	Filename []string `json:"filename,omitempty"`
}

type qqVkeyRequest struct {
	Req0 struct {
		Module string      `json:"module"`
		Method string      `json:"method"`
		Param  qqVkeyParam `json:"param"`
	} `json:"req_0"`
	Comm struct {
		UIN    int    `json:"uin"`
		Format string `json:"format"`
		CT     int    `json:"ct"`
		CV     int    `json:"cv"`
	} `json:"comm"`
}

type qqVkeyResponse struct {
	Code int `json:"code"`
	Req0 struct {
		Code int `json:"code"`
		Data struct {
			SIP        []string `json:"sip"`
			MidURLInfo []struct {
				SongMid  string `json:"songmid"`
				Purl     string `json:"purl"`
				Result   int    `json:"result"`
				FileName string `json:"filename"`
			} `json:"midurlinfo"`
		} `json:"data"`
	} `json:"req_0"`
}

// Resolve 实现 Resolver。
func (r QQResolver) Resolve(ctx context.Context, platformID string) (*Resolved, error) {
	got, err := r.ResolveBatch(ctx, []string{platformID})
	if err != nil {
		return nil, err
	}
	if res, ok := got[platformID]; ok {
		return res, nil
	}
	return nil, ErrNotPlayable
}

// ResolveBatch 实现 Resolver。按 songmid 匹配回填，不依赖响应顺序。
func (r QQResolver) ResolveBatch(ctx context.Context, platformIDs []string) (map[string]*Resolved, error) {
	out := make(map[string]*Resolved, len(platformIDs))
	if len(platformIDs) == 0 {
		return out, nil
	}

	valid := make([]string, 0, len(platformIDs))
	for _, id := range platformIDs {
		if s := strings.TrimSpace(id); s != "" {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}

	var lastErr error
	for start := 0; start < len(valid); start += qqMaxBatch {
		end := start + qqMaxBatch
		if end > len(valid) {
			end = len(valid)
		}
		batch := valid[start:end]

		var payload qqVkeyRequest
		payload.Req0.Module = "vkey.GetVkeyServer"
		payload.Req0.Method = "CgiGetVkey"
		payload.Req0.Param = qqVkeyParam{
			GUID:      qqGUID,
			SongMid:   batch,
			SongType:  make([]int, len(batch)), // 全 0 = 普通歌曲
			UIN:       "0",
			LoginFlag: 1,
			Platform:  "20",
		}
		payload.Comm.UIN = 0
		payload.Comm.Format = "json"
		payload.Comm.CT = 24
		payload.Comm.CV = 0

		body, err := json.Marshal(payload)
		if err != nil {
			lastErr = err
			continue
		}
		req, err := newRequest(ctx, http.MethodPost, qqVkeyAPI, qqReferer)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))

		respBody, err := doJSON(sharedClient, req, 1<<20)
		if err != nil {
			lastErr = err
			continue
		}
		var parsed qqVkeyResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			lastErr = fmt.Errorf("解析 QQ 响应失败: %w", err)
			continue
		}
		if parsed.Code != 0 || parsed.Req0.Code != 0 {
			lastErr = fmt.Errorf("QQ vkey 返回 code=%d req_0.code=%d", parsed.Code, parsed.Req0.Code)
			continue
		}

		// sip 是直链的前缀（形如 http://aqqmusic.tc.qq.com/）。多数 purl 是相对
		// 路径，但也见过已经带 http 的，所以两种都处理。
		prefix := ""
		if len(parsed.Req0.Data.SIP) > 0 {
			prefix = parsed.Req0.Data.SIP[0]
		}
		for _, item := range parsed.Req0.Data.MidURLInfo {
			if item.Purl == "" || item.SongMid == "" {
				continue
			}
			full := item.Purl
			if !strings.HasPrefix(full, "http") {
				full = prefix + full
			}
			out[item.SongMid] = &Resolved{
				URL:     full,
				Format:  formatFromFilename(item.FileName),
				Expires: time.Now().Add(qqTTL),
			}
		}
	}
	return out, lastErr
}

// formatFromFilename 从 QQ 返回的 filename（形如 `C400003mAan70zUy5O.m4a`）取后缀。
func formatFromFilename(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i+1 < len(name) {
		if ext := strings.ToLower(strings.TrimSpace(name[i+1:])); ext != "" && len(ext) <= 5 {
			return ext
		}
	}
	return "m4a"
}

// ---------------------------------------------------------------------------
// 按档位解析（TierResolver）
// ---------------------------------------------------------------------------

// qqTierFilename 把档位翻成 vkey 请求里的 filename 前缀与扩展名。
//
// 实测（2026-10-07）：档位**完全由 filename 前缀决定** ——
// F000=flac、A000=ape、M500=320k mp3、C400=128k m4a。
// 未登录时 F000 / A000 / M800 一律 `result=104003` 且 purl 是**空串**（不是 null）。
//
// QQ 没有独立的 192k 档，192k 退到 128k 那一档 —— 不编一个不存在的档位去问，
// 那种请求换回来的是参数错误，看起来像源坏了。
func qqTierFilename(tier string) (prefix, ext string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TierLossless:
		return "F000", ".flac", true
	case Tier320k:
		return "M500", ".mp3", true
	case Tier192k, Tier128k:
		return "C400", ".m4a", true
	}
	return "", "", false
}

// ResolveAtTier 实现 TierResolver：按指定档位取一条直链。
//
// ⚠️ 两条必须知道的真机事实（2026-10-07 实测，都写在这里免得下次当成新 bug）：
//
//  1. **未登录问不到无损**：flac/ape 恒 104003、purl 空串。所以试听阶梯在 QQ 上
//     实际只会走到 320k / 128k 那两档 —— 这是平台能力，不是我们写错了。
//  2. **`result=0` + 有 purl 也不代表能听**：实测未登录拿到的多是**约 48 秒的
//     试听片段**（且常是 128k，即使点名要 M500），偶尔干脆 404。所以这里给出的
//     档位只能标到「点名要的那一档」，真实码率得靠取流时的魔数嗅探兜底
//     （见 pkg/intercept 的 qualityMismatch 一路）。
func (r QQResolver) ResolveAtTier(ctx context.Context, platformID, tier string) (*Resolved, error) {
	prefix, ext, ok := qqTierFilename(tier)
	id := strings.TrimSpace(platformID)
	if !ok || id == "" {
		// 认不出的档位：退回默认解析，不装作能按档位要。
		return r.Resolve(ctx, platformID)
	}

	var payload qqVkeyRequest
	payload.Req0.Module = "vkey.GetVkeyServer"
	payload.Req0.Method = "CgiGetVkey"
	payload.Req0.Param = qqVkeyParam{
		GUID:      qqGUID,
		SongMid:   []string{id},
		SongType:  []int{0},
		UIN:       "0",
		LoginFlag: 1,
		Platform:  "20",
		Filename:  []string{prefix + id + ext},
	}
	payload.Comm.UIN = 0
	payload.Comm.Format = "json"
	payload.Comm.CT = 24
	payload.Comm.CV = 0

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := newRequest(ctx, http.MethodPost, qqVkeyAPI, qqReferer)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	respBody, err := doJSON(sharedClient, req, 1<<20)
	if err != nil {
		return nil, err
	}
	var parsed qqVkeyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("解析 QQ 响应失败: %w", err)
	}
	if parsed.Code != 0 || parsed.Req0.Code != 0 {
		return nil, fmt.Errorf("QQ vkey 返回 code=%d req_0.code=%d", parsed.Code, parsed.Req0.Code)
	}

	prefixURL := ""
	if len(parsed.Req0.Data.SIP) > 0 {
		prefixURL = parsed.Req0.Data.SIP[0]
	}
	infos := parsed.Req0.Data.MidURLInfo
	for i := range infos {
		item := infos[i]
		// 按 songmid 匹配回填；服务端只回一条时直接认它（它可能把 mid 换成了
		// 自己规范化的那一串，见 qqVkeyParam.Filename 的说明）。
		if item.SongMid != id && len(infos) != 1 {
			continue
		}
		if item.Purl == "" || item.Result != 0 {
			// 104003 = 这一档对匿名调用者不开放。当成「没有这一档」，让阶梯降档。
			return nil, ErrTierUnavailable
		}
		full := item.Purl
		if !strings.HasPrefix(full, "http") {
			full = prefixURL + full
		}
		return &Resolved{
			URL:     full,
			Format:  formatFromFilename(item.FileName),
			Expires: time.Now().Add(qqTTL),
			Tier:    strings.ToLower(strings.TrimSpace(tier)),
		}, nil
	}
	return nil, ErrTierUnavailable
}
