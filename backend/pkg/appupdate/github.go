package appupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// defaultGitHubAPI 是 GitHub 的 releases/latest 模板。owner/repo 在运行时从
// Options.Repo 注入（默认取 git remote 指向的 liwenyao016828/fnmusic）。
//
// 为什么不把 repository 写死成 URL：本项目曾改过仓库名，写死的 URL 一旦过期，
// 用户看到的是「检查更新失败」，而不是「更新源配错了」。仓库名集中在
// DefaultRepo 一处，环境变量 FN_UPDATE_REPO 可覆盖（自建镜像/测试用）。
const defaultGitHubAPI = "https://api.github.com/repos/%s/releases/latest"

// DefaultRepo 是本项目的更新源仓库（owner/repo）。
// 取值来源：`git remote get-url origin`（当前为 git@github.com:liwenyao016828/fnmusic.git）。
const DefaultRepo = "liwenyao016828/fnmusic"

// OfficialMirrorURL 是「内置 GitHub 官方源」在镜像清单里的占位 URL。
//
// 它不指向任何真实地址：清单里出现一条 URL 等于本常量的条目，只表示
// 「官方源被停用了」（见 source.go 的 officialDisabled）。用占位值而不是加一个
// 布尔配置项，是为了让「清单」继续是唯一真源 —— 少一个必须手动同步的字段。
const OfficialMirrorURL = "github://official"

// ReleaseAsset GitHub release 里的一个产物。
type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Release 是 GitHub releases/latest 里我们关心的字段。
//
// 只解析需要的字段：release 的完整 JSON 有几十个键（author、reactions、
// upload_url…），全量建模只会让测试 fixture 变长、字段一改就编译不过。
// 未知字段 Go 会自动忽略，这是有意的。
type Release struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	PublishedAt string         `json:"published_at"`
	HTMLURL     string         `json:"html_url"`
	Prerelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
	Assets      []ReleaseAsset `json:"assets"`
}

// PickFPKAsset 从 assets 里挑出 `.fpk` 安装包。
//
// 选最大那个：同一个 release 可能同时挂 x86_64/arm64，或者带一个历史小包；
// 本应用目前只发一个 fpk，取大的在「多挂了一个 debug 包」时也更不容易选错。
// 完全找不到返回 nil —— 调用方要把「有新版但没有安装包」当成可读错误告诉用户，
// 而不是当成功。
func PickFPKAsset(assets []ReleaseAsset) *ReleaseAsset {
	var best *ReleaseAsset
	for i := range assets {
		a := &assets[i]
		if !strings.HasSuffix(strings.ToLower(a.Name), ".fpk") {
			continue
		}
		if strings.TrimSpace(a.DownloadURL) == "" {
			continue
		}
		if best == nil || a.Size > best.Size {
			best = a
		}
	}
	return best
}

// ParseRelease 解析 GitHub 的 release JSON。
func ParseRelease(data []byte) (Release, error) {
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return rel, fmt.Errorf("解析 GitHub 响应失败：%w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return rel, fmt.Errorf("GitHub 响应缺少 tag_name（可能不是 release 接口）")
	}
	return rel, nil
}

// FetchLatestRelease 请求 GitHub releases/latest。
//
// 为什么带 Accept: application/vnd.github+json 与 X-GitHub-Api-Version：
// GitHub 对不带 Accept 的请求仍然返回 JSON，但官方文档把「未指定版本」视为
// 未来可能变化；显式写版本号可以避免某天字段语义变了而我们静默错解析。
// User-Agent 是 GitHub API 的硬要求（缺了直接 403），这里用应用名 + 版本。
func FetchLatestRelease(ctx context.Context, client *http.Client, apiURL, repo, token, userAgent string) (Release, error) {
	if client == nil {
		return Release{}, fmt.Errorf("HTTP 客户端未初始化")
	}
	if strings.TrimSpace(repo) == "" {
		return Release{}, fmt.Errorf("更新源仓库未配置")
	}
	if apiURL == "" {
		apiURL = fmt.Sprintf(defaultGitHubAPI, repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	} else {
		req.Header.Set("User-Agent", "yinshu-ai-updater")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if strings.TrimSpace(token) != "" {
		// 可选：未鉴权时 GitHub 限速 60 次/小时/IP，配了 token 更稳（也支持私有仓库）。
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("请求 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()

	// release JSON 正常 <64KB；限 2MB 只为防止异常响应把内存吃满。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Release{}, fmt.Errorf("读取 GitHub 响应失败：%w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, fmt.Errorf("更新源仓库或 release 不存在（HTTP 404）：%s", repo)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("GitHub 限速或拒绝访问（HTTP %d）：可稍后重试，或配置 FN_GITHUB_TOKEN", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("GitHub 返回 HTTP %d：%s", resp.StatusCode, snippet(string(body), 200))
	}
	return ParseRelease(body)
}

func snippet(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
