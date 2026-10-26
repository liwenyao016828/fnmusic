package appupdate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
)

// ── 更新源（镜像）的逐个尝试 ──
//
// 背景（用户 2026-10-25）：「增加默认镜像站，可删减增加。」「可以填自己的」「回退」。
// 现状是 NAS 上直接请求 api.github.com 就是 `dial tcp …:443: i/o timeout`，
// 所以必须有「换一台机器去问」的能力，且要在两台都不通时能说清楚试过谁。
//
// 三条硬规则，改代码前请先读：
//
//  1. **官方源永远排最后当兜底**（用户拍板「回退」）。它不能被删除，只能停用；
//     停用后就真的只剩镜像 —— 那是用户自己的选择，界面要写清楚。
//  2. **逐个串行尝试**，不做并发：镜像多半共享同一个出口，并发只是同时超时，
//     还会让「谁命中的」变得难说。失败原因要逐条带回（见 SourceAttempt）。
//  3. 镜像命中的**资产地址也要走同一台镜像**。资产在
//     objects.githubusercontent.com，直连同样可能不通；只加速 JSON 那一步
//     等于没加速。

// officialName 是内置兜底源的显示名。
const officialName = "GitHub 官方"

// sourceEntry 是一个「候选更新源」，检查与下载共用。
type sourceEntry struct {
	name string
	// releaseURL 是「问版本」用的地址（官方源是 api.github.com 的 releases/latest，
	// 镜像展开后的同义地址）。
	releaseURL string
	// isOfficial 决定资产地址怎么拼：官方源用 release JSON 里的绝对地址，
	// 镜像则把镜像前缀拼在它前面（或由源型镜像自己决定，见 config.MirrorURL）。
	isOfficial bool
	// mirror 是这条源对应的镜像配置；官方源时为零值。
	mirror config.UpdateMirror
	// allowOfficial 是「测试用的逃生门」：为 false 时 sources() 不再追加内置
	// 官方源，**只剩** mirrorsFunc 返回的那些。
	//
	// 为什么需要：纯前缀型镜像的 releaseURL 在测试里是 `srv.URL + 官方地址`，
	// 万一用户（或我们自己）配了一条通配前缀，官方那条会被同一台假服务器
	// 顺带服务到，结果「镜像失败」变成「官方源成功」—— 用例就会在没人察觉的
	// 情况下永远测不到失败分支。
	allowOfficial bool
}

// sources 返回本次要尝试的全部更新源（有序：镜像在前、官方源兜底在最后）。
//
// 停用的镜像不进返回列表，但要记进 skipped —— 界面「为什么没走我填的那台」
// 的答案通常就是「它被停用了」。
func (m *Manager) sources() (list []sourceEntry, skipped []SourceAttempt) {
	official := sourceEntry{
		name:       officialName,
		releaseURL: m.officialReleaseURL(),
		isOfficial: true,
	}
	if m.mirrorsFunc == nil {
		if m.allowOfficial {
			return []sourceEntry{official}, nil
		}
		return nil, nil
	}
	mirrors := config.NormalizeUpdateMirrors(m.mirrorsFunc())
	for _, mi := range mirrors {
		if mi.Disabled {
			skipped = append(skipped, SourceAttempt{Name: mi.Name, URL: mi.URL, Skipped: true})
			continue
		}
		list = append(list, sourceEntry{
			name:       mi.Name,
			releaseURL: config.MirrorURL(mi, m.officialReleaseURL(), m.repo),
			mirror:     mi,
		})
	}
	if !officialDisabled(mirrors) && m.allowOfficial {
		list = append(list, official)
	}
	return list, skipped
}

// officialDisabled 判断用户是否把内置官方源也停用了。
//
// 判断依据是「清单里有没有一条 URL == official 标记」—— 前端在停用官方源时
// 会往清单里写一条 URL == OfficialMirrorURL 的停用项。不用独立字段是
// 为了少一个「必须记住同步」的配置项。
func officialDisabled(mirrors []config.UpdateMirror) bool {
	for _, mi := range mirrors {
		if strings.TrimSpace(mi.URL) == OfficialMirrorURL {
			return mi.Disabled
		}
	}
	return false
}

// officialReleaseURL 返回官方 releases/latest 地址（Options.APIURL 优先，测试/自建用）。
func (m *Manager) officialReleaseURL() string {
	if u := strings.TrimSpace(m.apiURL); u != "" {
		return u
	}
	return fmt.Sprintf(defaultGitHubAPI, m.repo)
}

// assetURLFor 把 release JSON 里的资产绝对地址换成「经这条源」的地址。
//
// 官方源：原样返回（JSON 里的 browser_download_url 已经是完整地址）。
// 前缀型镜像：镜像前缀 + 原地址（同一个反代通常两处都通）。
// 源型镜像：**不改写**。源型更新源自己托管 JSON，资产通常是它一起托管的
// 绝对地址或同域相对地址，硬套前缀只会拼出坏 URL。
func assetURLFor(src sourceEntry, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || src.isOfficial || config.IsSourceMirror(src.mirror) {
		return raw
	}
	// 相对地址（自建源里很常见）先按源地址补全，再套前缀；否则前缀拼相对路径没意义。
	if !strings.Contains(raw, "://") {
		return raw
	}
	return config.MirrorURL(src.mirror, raw, "")
}

// tryCheck 按顺序尝试每个源，返回第一个拿到合法 release 的结果。
//
// 返回的 res 已经填好 Source / Tried；全部失败时 res.Error 里带上「试过哪些」。
// ctx 是整个检查的预算（CheckTimeout），单个源的失败不应吞掉剩下的预算：
// 前缀型镜像遇到不可达主机时，transport 自己会在连接阶段快速失败。
func (m *Manager) tryCheck(ctx context.Context) (CheckResult, Release, sourceEntry) {
	res := CheckResult{Current: m.current, CheckedAt: m.now().Format(time.RFC3339)}
	list, skipped := m.sources()
	res.Tried = append(res.Tried, skipped...)

	var failures []string
	for _, src := range list {
		if err := ctx.Err(); err != nil {
			res.Tried = append(res.Tried, SourceAttempt{Name: src.name, URL: src.releaseURL, Error: "等待超时", Reason: "超时"})
			failures = append(failures, src.name+"：等待超时")
			break
		}
		// 每台各自限时（sourceAttemptWindow），剩余总预算更短就取更短的那个。
		sctx, cancel := context.WithTimeout(ctx, sourceAttemptWindow)
		rel, err := FetchLatestRelease(sctx, m.httpClient(), src.releaseURL, m.repo, m.tokenNow(), m.ua)
		cancel()
		if err != nil {
			reason := briefErr(err)
			res.Tried = append(res.Tried, SourceAttempt{Name: src.name, URL: src.releaseURL, Error: reason, Reason: reason})
			failures = append(failures, src.name+"："+reason)
			m.logf("[UPDATE] 更新源 %s 失败：%v", src.name, err)
			continue
		}
		// 空 tag 视为失败：公共镜像偶尔回 200 + 一段 HTML/空 JSON，如果不校验，
		// 用户会看到「更新源返回的版本号为空」这种和镜像毫无关联的报错。
		if strings.TrimSpace(rel.TagName) == "" {
			reason := "返回内容里没有版本号（可能不是 release 接口）"
			res.Tried = append(res.Tried, SourceAttempt{Name: src.name, URL: src.releaseURL, Error: reason, Reason: "不是 release 接口"})
			failures = append(failures, src.name+"："+reason)
			m.logf("[UPDATE] 更新源 %s 响应缺少 tag_name", src.name)
			continue
		}
		res.Tried = append(res.Tried, SourceAttempt{Name: src.name, URL: src.releaseURL, OK: true})
		res.Source = src.name
		if len(failures) > 0 {
			// 有失败才保留 Tried（前端据此显示「换了 N 台才通」）；全成功时列表很短，
			// 一并保留也无妨 —— 但这里只在有前序失败时提示，避免正常情况满屏。
			m.logf("[UPDATE] 更新源命中 %s（此前失败 %d 台）", src.name, len(failures))
		}
		// 只保留「命中之后」的记录会丢掉「为什么先失败了」，两者都有用。
		_ = failures
		return res, rel, src
	}

	if len(failures) == 0 {
		res.Error = "没有可用的更新源（镜像清单为空且官方源被停用）"
	} else {
		res.Error = fmt.Sprintf("所有更新源均失败：%s", strings.Join(failures, "；"))
	}
	// 失败也记住「这条消息从哪个版本说起」：本进程的里 Current 是对的，但如果
	// 进程自上次检查后重启过，缓存里的这条失败信息可能属于一个更旧的版本。
	res.Current = m.current
	return res, Release{}, sourceEntry{}
}

// briefErr 把网络错误压成一句人话（原文太长会撑爆界面和日志）。
//
// 保留最关键的片段：超时 / 拒绝 / DNS / TLS / HTTP 状态码。全部保留原文也
// 可以，但界面上是红字一行，读不下去的信息等于没有。
func briefErr(err error) string {
	s := strings.TrimSpace(err.Error())
	for _, mark := range []string{"i/o timeout", "timeout awaiting response headers", "connection refused",
		"no such host", "network is unreachable", "TLS handshake timeout", "EOF", "重置", "certificate"} {
		if strings.Contains(s, mark) {
			if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
				return mark + "（" + s[i+2:] + "）"
			}
			return mark
		}
	}
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// assetCandidates 返回「下载这个资产时依次要试的地址」。
//
// 规则：**先当前源，再其余源**（含官方源兜底）。当前源排第一是因为它刚刚
// 成功给出过 release JSON，通常下载也通，不该为了「统一顺序」白等一轮超时。
// 其余源的资产地址由 assetURLFor 现算：它们各自的 JSON 可能还没取过，但资产
// 地址只是「同一个 GitHub 地址前面挂不同前缀」，不需要再问一次 JSON。
//
// 源型镜像（{repo} 占位、自己托管 JSON）**不参与**换源兜底：它给出的资产地址
// 可能是内网地址，套别人家的前缀会拼出坏 URL；它失败就让它失败。
func (m *Manager) assetCandidates(rawURL string, current sourceEntry) []sourceEntry {
	out := []sourceEntry{current}
	list, _ := m.sources()
	for _, src := range list {
		if src.isOfficial == current.isOfficial && src.releaseURL == current.releaseURL {
			continue
		}
		if !src.isOfficial && config.IsSourceMirror(src.mirror) {
			continue
		}
		// 资产走前缀镜像时只看前缀；源型镜像排除在外，见上。
		if config.IsSourceMirror(current.mirror) && !current.isOfficial {
			// 当前源就是源型：只试它自己，别把它的 URL 套到别人身上。
			break
		}
		out = append(out, src)
	}
	return out
}

// openAsset 依次尝试各候选源，返回第一个拿到 200 的响应。
//
// 每台内部仍保留「重试一次」：GitHub 经代理回源偶发首字节超时，一次抖动不该
// 就换源。返回值 tried 是「已失败的源 + 原因」，用于最终报错（用户要能看出
// 试过哪些，2026-10-25 镜像需求）。
func (m *Manager) openAsset(ctx context.Context, rawURL string, current sourceEntry) (*http.Response, []string, error) {
	cands := m.assetCandidates(rawURL, current)
	var tried []string
	for i, src := range cands {
		// 当前源用 rawURL（它就是按当前源拼好的），其余源要换前缀。
		url := rawURL
		if i > 0 {
			url = assetURLFor(src, rawURL)
			if url == "" {
				continue
			}
		}
		if i > 0 {
			m.logf("[UPDATE] 下载改用更新源 %s：%s", src.name, url)
			m.mu.Lock()
			m.dl.Error = fmt.Sprintf("当前源下载失败，正在改用「%s」", src.name)
			m.mu.Unlock()
		}
		resp, err := m.fetchAsset(ctx, url)
		if err == nil {
			m.mu.Lock()
			m.dl.Error = "" // 清掉重试/换源期间写下的提示
			m.mu.Unlock()
			return resp, tried, nil
		}
		tried = append(tried, src.name+"："+briefErr(err))
		m.logf("[UPDATE] 更新源 %s 下载失败：%v", src.name, err)
		if ctx.Err() != nil {
			return nil, tried, ctx.Err()
		}
	}
	return nil, tried, fmt.Errorf("下载失败：所有更新源都不可用")
}

// fetchAsset 对单个地址做「最多 downloadRetries 次」的请求。
// 只重试传输层错误；HTTP 状态码不对立刻返回（404 重试一百次也不会出现）。
func (m *Manager) fetchAsset(ctx context.Context, url string) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("User-Agent", m.ua)
		req.Header.Set("Accept", "application/octet-stream")
		resp, derr := m.httpClient().Do(req)
		if derr == nil {
			if resp.StatusCode == http.StatusOK {
				return resp, nil
			}
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if attempt >= downloadRetries || ctx.Err() != nil {
			return nil, derr
		}
		m.logf("[UPDATE] 下载第 %d 次失败（%v），%s 后重试", attempt, derr, downloadRetryWait)
		m.mu.Lock()
		m.dl.Error = fmt.Sprintf("网络不稳定，正在重试（%v）", derr)
		m.mu.Unlock()
		select {
		case <-time.After(downloadRetryWait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// openAssetRetry 是「已经拿到过响应、但传输中途断了」时的换源入口。
//
// 与 openAsset 的区别只有起点：跳过刚失败的那台（它的地址就是 resp 请求过的），
// 从它的下一台开始试。返回的 tried 是这次新失败的源，用于最终报错。
func (m *Manager) openAssetRetry(ctx context.Context, rawURL string, current sourceEntry, _ *http.Response, cause error) (*http.Response, []string, error) {
	cands := m.assetCandidates(rawURL, current)
	var tried []string
	tried = append(tried, current.name+"："+briefErr(cause))
	for i, src := range cands {
		if i == 0 {
			continue // 当前源刚断，别在同一台上重来第二遍
		}
		url := assetURLFor(src, rawURL)
		if url == "" {
			continue
		}
		m.logf("[UPDATE] 下载中断，改用更新源 %s", src.name)
		m.mu.Lock()
		m.dl.Error = fmt.Sprintf("下载中断，正在改用「%s」重新下载", src.name)
		m.dl.Progress = 0
		m.dl.Size = 0
		m.mu.Unlock()
		resp, err := m.fetchAsset(ctx, url)
		if err == nil {
			m.mu.Lock()
			m.dl.Error = ""
			m.mu.Unlock()
			return resp, tried, nil
		}
		tried = append(tried, src.name+"："+briefErr(err))
		if ctx.Err() != nil {
			return nil, tried, ctx.Err()
		}
	}
	return nil, tried, fmt.Errorf("%w（所有更新源都试过了）", cause)
}
