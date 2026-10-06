package sources

// 音源批量导入。
//
// 支持两种来源，可在同一次请求里混用：
//   - urls：在线脚本地址（走 SSRF 防护，仅公网 http/https）
//   - scripts：本地脚本内容（前端多选文件后读取文本上传）
//
// 与单条导入（HandleAddCustom / HandleImportURL）相比，本文件解决了三个问题：
//  1. ID 用内容哈希而非时间戳 —— 批量导入不会因同秒而冲突；
//  2. 内容重复自动跳过 —— 重复导入同一脚本不会产生多个副本；
//  3. 从脚本头部注释提取 @name/@version/@author/@description —— 无需手工填写元数据。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/security"
)

// maxBatchItems 单次批量导入的条目上限，防止一次请求过大。
const maxBatchItems = 50

// metaPatterns 从脚本头部注释提取元数据（洛雪音源脚本约定）。
var metaPatterns = map[string]*regexp.Regexp{
	"name":        regexp.MustCompile(`(?m)@name\s+([^\r\n*]+)`),
	"description": regexp.MustCompile(`(?m)@description\s+([^\r\n*]+)`),
	"version":     regexp.MustCompile(`(?m)@version\s+([^\r\n*]+)`),
	"author":      regexp.MustCompile(`(?m)@author\s+([^\r\n*]+)`),
}

// scriptMeta 音源脚本的元数据。
type scriptMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      string `json:"author"`
}

// parseScriptMeta 从脚本头部注释提取元数据；缺失字段留空。
func parseScriptMeta(script string) scriptMeta {
	var meta scriptMeta
	get := func(key string) string {
		m := metaPatterns[key].FindStringSubmatch(script)
		if len(m) < 2 {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	meta.Name = get("name")
	meta.Description = get("description")
	meta.Version = get("version")
	meta.Author = get("author")
	return meta
}

// scriptID 由脚本内容派生稳定 ID。
// 用内容哈希而非时间戳：批量导入不会同秒冲突，且天然可作为去重依据。
func scriptID(script string) string {
	sum := sha256.Sum256([]byte(script))
	return "custom_" + hex.EncodeToString(sum[:])[:16]
}

// batchResult 单个条目的导入结果。
type batchResult struct {
	Source  string `json:"source"` // URL 或文件名，便于调用方定位
	Status  string `json:"status"` // imported / skipped / failed
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Author  string `json:"author,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// HandleImportBatch 批量导入音源。
//
// POST /api/sources/import_batch
//
// body:
//
//	{
//	  "urls": ["https://example.com/a.js", "https://example.com/b.js"],
//	  "scripts": [{"filename": "mine.js", "content": "/*! @name 我的音源 */..."}]
//	}
//
// 两类可任选其一或同时提供；单次合计不超过 maxBatchItems 条。
// 重复导入同一脚本会被识别为 skipped（按内容哈希判断），不会产生副本。
func (m *Manager) HandleImportBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URLs    []string `json:"urls"`
		Scripts []struct {
			Filename string `json:"filename"`
			Content  string `json:"content"`
			Name     string `json:"name"`
		} `json:"scripts"`
	}
	// 上限放宽到 50 条 × 2MB + 请求开销
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchItems*maxScriptBytes+1<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "invalid payload: " + err.Error()})
		return
	}
	if len(req.URLs) == 0 && len(req.Scripts) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "请提供 urls 或 scripts"})
		return
	}
	if total := len(req.URLs) + len(req.Scripts); total > maxBatchItems {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 400, "message": fmt.Sprintf("单次最多导入 %d 条，本次 %d 条", maxBatchItems, total),
		})
		return
	}

	// 一次性载入配置，导入过程中只往内存里追加，最后统一落盘，
	// 避免批量导入时反复读写配置文件。
	cfg := m.cfgMgr.Get()
	known := make(map[string]bool, len(cfg.CustomSources))
	for _, cs := range cfg.CustomSources {
		known[cs.ID] = true
	}

	results := make([]batchResult, 0, len(req.URLs)+len(req.Scripts))
	imported := 0

	// 1. 在线 URL
	if len(req.URLs) > 0 {
		opts := security.Options{MaxBodyBytes: maxScriptBytes}
		client := security.NewSafeClient(opts, 20*time.Second)
		for _, rawURL := range req.URLs {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				continue
			}
			body, err := fetchScript(client, rawURL, opts)
			if err != nil {
				results = append(results, batchResult{Source: rawURL, Status: "failed", Reason: err.Error()})
				continue
			}
			res, added := addSource(&cfg, string(body), "")
			res.Source = rawURL
			if added {
				imported++
			}
			results = append(results, res)
		}
	}

	// 2. 本地脚本内容
	for _, item := range req.Scripts {
		content := item.Content
		if strings.TrimSpace(content) == "" {
			results = append(results, batchResult{
				Source: item.Filename, Status: "failed", Reason: "脚本内容为空",
			})
			continue
		}
		if int64(len(content)) > maxScriptBytes {
			results = append(results, batchResult{
				Source: item.Filename, Status: "failed",
				Reason: fmt.Sprintf("脚本体积超限（最大 %d MB）", maxScriptBytes>>20),
			})
			continue
		}
		fallback := item.Name
		if fallback == "" {
			fallback = strings.TrimSuffix(item.Filename, ".js")
		}
		res, added := addSource(&cfg, content, fallback)
		res.Source = item.Filename
		if added {
			imported++
		}
		results = append(results, res)
	}

	if imported > 0 {
		_ = m.cfgMgr.Update(cfg)
	}

	skipped, failed := 0, 0
	for _, res := range results {
		switch res.Status {
		case "skipped":
			skipped++
		case "failed":
			failed++
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"total":    len(results),
			"imported": imported,
			"skipped":  skipped,
			"failed":   failed,
			"results":  results,
			"hint": "重复导入同一脚本会被识别为 skipped（按内容哈希判断）；" +
				"导入后需到「音源管理」选中激活的音源。",
		},
	})
}

// addSource 把一个脚本加入配置。内容重复时返回 skipped。
// 返回 (结果, 是否真正新增)。
func addSource(cfg *config.AppConfig, script, fallbackName string) (batchResult, bool) {
	id := scriptID(script)
	for _, cs := range cfg.CustomSources {
		if cs.ID == id {
			return batchResult{
				Status: "skipped", ID: id, Name: cs.Name,
				Reason: "脚本内容与已有音源相同，已跳过",
			}, false
		}
	}

	// 形状校验：挡掉网页 / 任意文本（详见 validate.go 的说明）。
	// 放在去重之后、写入之前 —— 重复导入同一份坏内容时报「已存在」更贴切。
	if ok, reason := ValidateScriptContent(script); !ok {
		return batchResult{
			Status: "failed", ID: id, Name: fallbackName, Reason: reason,
		}, false
	}

	meta := parseScriptMeta(script)
	name := meta.Name
	if name == "" {
		name = fallbackName
	}
	if name == "" {
		name = "导入音源"
	}

	cs := config.CustomSource{
		ID:          id,
		Name:        name,
		Description: meta.Description,
		Version:     meta.Version,
		Author:      meta.Author,
		Script:      script,
		CreatedAt:   time.Now().Unix(),
	}
	cfg.CustomSources = append(cfg.CustomSources, cs)

	return batchResult{
		Status: "imported", ID: id, Name: name,
		Version: meta.Version, Author: meta.Author,
	}, true
}

// fetchScript 抓取在线脚本，含 SSRF 校验与体积限制。
func fetchScript(client *http.Client, rawURL string, opts security.Options) ([]byte, error) {
	if _, err := security.ValidateURL(rawURL, opts); err != nil {
		return nil, fmt.Errorf("拒绝导入该地址: %w", err)
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("抓取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := security.LimitedRead(resp.Body, opts)
	if err != nil {
		return nil, fmt.Errorf("读取脚本失败: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("上游返回空内容")
	}
	return body, nil
}
