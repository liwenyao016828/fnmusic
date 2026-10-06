package sources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/security"
)

// maxScriptBytes 单个音源脚本的体积上限（2MB）。
// 防止超大脚本撑爆配置存储，也限制前端沙箱的执行成本。
const maxScriptBytes int64 = 2 << 20

// SourceItem 音源条目
type SourceItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	URL         string `json:"url"`
	IsCustom    bool   `json:"is_custom"`
	IsPreset    bool   `json:"is_preset"`
	Status      string `json:"status"`
	Size        int64  `json:"size"`
}

type Manager struct {
	cfgMgr *config.ConfigManager
}

func NewManager(cfgMgr *config.ConfigManager, _ interface{}, _ string) *Manager {
	return &Manager{
		cfgMgr: cfgMgr,
	}
}

func (m *Manager) HandleListSources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	allSources := make([]SourceItem, 0)

	// Read custom sources from config (All sources are user-imported, 0 built-in)
	cfg := m.cfgMgr.Get()
	for _, cs := range cfg.CustomSources {
		allSources = append(allSources, SourceItem{
			ID:          cs.ID,
			Name:        cs.Name,
			Description: cs.Description,
			Version:     cs.Version,
			Author:      cs.Author,
			IsCustom:    true,
			IsPreset:    false,
			Status:      "ready",
			Size:        int64(len(cs.Script)),
		})
	}

	// ── 校验启用池（多选激活，2026-09-30） ──
	// 丢掉已经不存在的 id（删过源之后可能残留）；
	// 全新安装（ActiveSourceIDs 还是 nil、旧的单值也是空）默认启用第一个 —— 保持老用户的第一印象。
	known := map[string]bool{}
	for _, s := range allSources {
		known[s.ID] = true
	}
	pool := cfg.ActiveIDs()
	next := make([]string, 0, len(pool))
	for _, id := range pool {
		if known[id] {
			next = append(next, id)
		}
	}
	if cfg.ActiveSourceIDs == nil && cfg.ActiveSourceID == "" && len(allSources) > 0 {
		next = []string{allSources[0].ID}
	}
	if !sameIDList(next, pool) {
		cfg.SetActiveIDs(next) // 顺带把兼容字段 active_source_id 对齐成池里第一个
		_ = m.cfgMgr.Update(cfg)
	}

	activeIDs := cfg.ActiveIDs()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":              200,
		"message":           "ok",
		"active_source_id":  cfg.ActiveSourceID,
		"active_source_ids": activeIDs,
		"sources":           allSources,
		"data": map[string]interface{}{
			"sources":           allSources,
			"active_id":         cfg.ActiveSourceID,
			"active_source_id":  cfg.ActiveSourceID,
			"active_source_ids": activeIDs,
		},
	})
}

func (m *Manager) HandleGetScript(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(r.URL.Query().Get("id"))
	if sourceID == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 3 && parts[1] == "sources" {
			sourceID = parts[2]
			if sourceID == "script" && len(parts) >= 4 {
				sourceID = parts[3]
			}
		}
	}
	if sourceID == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}

	// Remove potential .js suffix
	sourceID = strings.TrimSuffix(sourceID, ".js")

	// 1. Check custom sources
	var scriptContent string
	cfg := m.cfgMgr.Get()
	for _, cs := range cfg.CustomSources {
		if cs.ID == sourceID {
			scriptContent = cs.Script
			break
		}
	}

	if scriptContent == "" {
		http.Error(w, "Source script not found", http.StatusNotFound)
		return
	}

	// If client expects JSON or format != raw
	format := r.URL.Query().Get("format")
	if format == "raw" || strings.Contains(r.Header.Get("Accept"), "text/javascript") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(scriptContent))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]string{
			"id":     sourceID,
			"script": scriptContent,
		},
		"script": scriptContent,
	})
}

// sameIDList 有序比较两个 id 列表（避免为一个比较引入 slices 依赖）
func sameIDList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// removeFromPool 从启用池里摘掉一个 id。摘空且还有别的源（fallback 非空）时兜底启用第一个 ——
// 与单值时代的「激活音源被删掉时自动改选」保持一致，不让用户/AI 处于悬空状态。
// 返回是否真的改动过。
func removeFromPool(cfg *config.AppConfig, id, fallback string) bool {
	pool := cfg.ActiveIDs()
	next := make([]string, 0, len(pool))
	changed := false
	for _, x := range pool {
		if x == id {
			changed = true
			continue
		}
		next = append(next, x)
	}
	if !changed {
		return false
	}
	if len(next) == 0 && fallback != "" {
		next = []string{fallback}
	}
	cfg.SetActiveIDs(next)
	return true
}

func (m *Manager) HandleSetActive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SourceID string `json:"source_id"`
		ID       string `json:"id"`
		// Active 缺省（nil）= 旧语义「池里只放它」；true = 加入启用池；false = 从池里摘掉。
		Active *bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid payload"})
		return
	}

	targetID := req.SourceID
	if targetID == "" {
		targetID = req.ID
	}
	if targetID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "source_id required"})
		return
	}

	cfg := m.cfgMgr.Get()
	switch {
	case req.Active == nil:
		// 旧调用方：单值语义 = 池里只有它（外部 AI 与老前端仍然这么用）
		cfg.SetActiveIDs([]string{targetID})
	case *req.Active:
		// 启用：追加到池**尾**——池是有序的，顺序就是取链的优先级
		cfg.SetActiveIDs(append(cfg.ActiveIDs(), targetID))
	default:
		// 停用：从池里摘掉（摘空也允许，用户有权一个都不启用）
		pool := cfg.ActiveIDs()
		next := make([]string, 0, len(pool))
		for _, id := range pool {
			if id != targetID {
				next = append(next, id)
			}
		}
		cfg.SetActiveIDs(next)
	}
	_ = m.cfgMgr.Update(cfg)

	activeIDs := cfg.ActiveIDs()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":              200,
		"status":            "ok",
		"active_source_id":  cfg.ActiveSourceID,
		"active_source_ids": activeIDs,
		"data": map[string]interface{}{
			"active_id":         cfg.ActiveSourceID,
			"active_source_ids": activeIDs,
		},
	})
}

func (m *Manager) HandleAddCustom(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Author      string `json:"author"`
		Script      string `json:"script"`
		Filename    string `json:"filename"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScriptBytes+1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Script) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "script content required"})
		return
	}

	if int64(len(req.Script)) > maxScriptBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    413,
			"message": fmt.Sprintf("音源脚本体积超限（最大 %d MB）", maxScriptBytes>>20),
		})
		return
	}

	csID := fmt.Sprintf("custom_%d", time.Now().Unix())
	csName := req.Name
	if csName == "" {
		if req.Filename != "" {
			csName = strings.TrimSuffix(req.Filename, ".js")
		} else {
			csName = "自定义音源"
		}
	}

	cs := config.CustomSource{
		ID:          csID,
		Name:        csName,
		Description: req.Description,
		Version:     req.Version,
		Author:      req.Author,
		Script:      req.Script,
		CreatedAt:   time.Now().Unix(),
	}

	cfg := m.cfgMgr.Get()
	cfg.CustomSources = append(cfg.CustomSources, cs)
	_ = m.cfgMgr.Update(cfg)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"status":  "ok",
		"message": "Custom source added successfully",
		"data":    cs,
	})
}

func (m *Manager) HandleImportURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "URL required"})
		return
	}

	// 校验目标地址：仅允许公网 http/https，阻断内网 / 回环 / 云元数据地址
	opts := security.Options{MaxBodyBytes: maxScriptBytes}
	if _, err := security.ValidateURL(req.URL, opts); err != nil {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 403, "message": "拒绝导入该地址: " + err.Error()})
		return
	}

	client := security.NewSafeClient(opts, 15*time.Second)
	resp, err := client.Get(req.URL)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 502, "message": "Failed to fetch source script: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 502, "message": fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)})
		return
	}

	body, err := security.LimitedRead(resp.Body, opts)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "读取脚本失败: " + err.Error()})
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "Empty response from URL"})
		return
	}

	csID := fmt.Sprintf("custom_%d", time.Now().Unix())
	csName := req.Name
	if csName == "" {
		csName = "导入音源"
	}

	cs := config.CustomSource{
		ID:        csID,
		Name:      csName,
		Script:    string(body),
		CreatedAt: time.Now().Unix(),
	}

	cfg := m.cfgMgr.Get()
	cfg.CustomSources = append(cfg.CustomSources, cs)
	_ = m.cfgMgr.Update(cfg)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"status":  "ok",
		"message": "Source imported successfully",
		"data":    cs,
	})
}

func (m *Manager) HandleDeleteCustom(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var targetID string
	if r.Method == http.MethodDelete || r.Method == http.MethodPost {
		var req struct {
			ID       string `json:"id"`
			SourceID string `json:"source_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		targetID = req.ID
		if targetID == "" {
			targetID = req.SourceID
		}
	}
	if targetID == "" {
		targetID = r.URL.Query().Get("id")
	}

	if targetID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "id required"})
		return
	}

	cfg := m.cfgMgr.Get()
	filtered := make([]config.CustomSource, 0)
	found := false
	for _, cs := range cfg.CustomSources {
		if cs.ID == targetID {
			found = true
			continue
		}
		filtered = append(filtered, cs)
	}

	if !found {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 404, "message": "Custom source not found"})
		return
	}

	cfg.CustomSources = filtered
	// 被删的源如果在启用池里，要一起摘掉；摘空且还有别的源时兜底启用第一个，
	// 避免把用户/AI 留在「一个源都没启用」的悬空状态（老行为就是这么兜的）。
	fallback := ""
	if len(filtered) > 0 {
		fallback = filtered[0].ID
	}
	removeFromPool(&cfg, targetID, fallback)
	_ = m.cfgMgr.Update(cfg)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"status":  "ok",
		"message": "Custom source deleted",
	})
}

func (m *Manager) HandleSourcesRest(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[1] == "sources" {
		sourceID := parts[2]
		if r.Method == http.MethodDelete {
			// Delete custom source
			cfg := m.cfgMgr.Get()
			filtered := make([]config.CustomSource, 0)
			for _, cs := range cfg.CustomSources {
				if cs.ID == sourceID {
					continue
				}
				filtered = append(filtered, cs)
			}
			cfg.CustomSources = filtered
			fallback := ""
			if len(filtered) > 0 {
				fallback = filtered[0].ID
			}
			removeFromPool(&cfg, sourceID, fallback)
			_ = m.cfgMgr.Update(cfg)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "status": "ok"})
			return
		} else if strings.HasSuffix(r.URL.Path, "/script") || (len(parts) == 4 && parts[3] == "script") {
			m.HandleGetScript(w, r)
			return
		}
	}
	m.HandleGetScript(w, r)
}
