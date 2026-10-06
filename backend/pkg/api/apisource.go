package api

// 「按服务地址」接入音源的 HTTP 层。
//
// 与既有的 /api/sources/*（LX 脚本，浏览器执行）并列，但走的是另一条路：
// 后端直接请求用户填的服务地址拿直链。协议规格见 pkg/apisource。
//
//	GET    /api/sources/api             列出已接入的 API 音源
//	POST   /api/sources/api             新增 / 更新（body 见下）
//	DELETE /api/sources/api?id=xxx      删除
//	POST   /api/sources/api/detect      探测服务地址属于哪种协议（**不保存**，供导入向导用）
//	POST   /api/sources/api/resolve     用某个 API 音源取一条可播直链

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/apisource"
	"fn-lx-player/pkg/config"
)

// apiSourceView 对外返回的结构：**不回显 token**
// （与账号接口同样的原则：凭据不经接口外泄）
type apiSourceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	HasToken  bool   `json:"has_token"`
	Protocol  string `json:"protocol"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	Note      string `json:"note,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func toAPISourceView(s config.APISource) apiSourceView {
	return apiSourceView{
		ID:        s.ID,
		Name:      s.Name,
		BaseURL:   s.BaseURL,
		HasToken:  strings.TrimSpace(s.Token) != "",
		Protocol:  s.Protocol,
		Label:     apisource.ProtocolLabel(s.Protocol),
		Enabled:   s.Enabled,
		Note:      s.Note,
		CreatedAt: s.CreatedAt,
	}
}

// HandleAPISources 列出 / 新增 / 删除 API 音源
func (s *Server) HandleAPISources(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Get()
		list := make([]apiSourceView, 0, len(cfg.APISources))
		for _, item := range cfg.APISources {
			list = append(list, toAPISourceView(item))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "ok",
			"data": map[string]any{"sources": list, "count": len(list)},
		})

	case http.MethodPost:
		var req struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			BaseURL  string `json:"base_url"`
			Token    string `json:"token"`
			Protocol string `json:"protocol"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}

		base := strings.TrimSpace(req.BaseURL)
		if base == "" {
			errJSON(w, http.StatusBadRequest, "服务地址不能为空")
			return
		}

		// 没给协议就现探一次 —— 用户只填地址也能用
		protocol := apisource.NormalizeProtocol(req.Protocol)
		note := ""
		if protocol == "" {
			res, err := apisource.Detect(base, strings.TrimSpace(req.Token))
			if err != nil {
				errJSON(w, http.StatusBadGateway, "自动探测失败："+err.Error())
				return
			}
			protocol = res.Protocol
			note = res.Detail
		}

		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = base
		}

		cfg := s.cfgMgr.Get()
		id := strings.TrimSpace(req.ID)
		entry := config.APISource{
			ID:        id,
			Name:      name,
			BaseURL:   base,
			Token:     strings.TrimSpace(req.Token),
			Protocol:  protocol,
			Enabled:   true,
			Note:      note,
			CreatedAt: time.Now().Unix(),
		}

		found := false
		for i := range cfg.APISources {
			if id != "" && cfg.APISources[i].ID == id {
				// 更新：token 留空表示「不改」，避免界面脱敏后误清空凭据
				if entry.Token == "" {
					entry.Token = cfg.APISources[i].Token
				}
				entry.CreatedAt = cfg.APISources[i].CreatedAt
				if req.Enabled != nil {
					entry.Enabled = *req.Enabled
				} else {
					entry.Enabled = cfg.APISources[i].Enabled
				}
				cfg.APISources[i] = entry
				found = true
				break
			}
		}
		if !found {
			// 同地址重复添加视为更新，避免堆一堆一样的东西
			for i := range cfg.APISources {
				if cfg.APISources[i].BaseURL == base {
					entry.ID = cfg.APISources[i].ID
					entry.CreatedAt = cfg.APISources[i].CreatedAt
					if entry.Token == "" {
						entry.Token = cfg.APISources[i].Token
					}
					cfg.APISources[i] = entry
					found = true
					break
				}
			}
		}
		if !found {
			if entry.ID == "" {
				entry.ID = "api_" + randomSuffix()
			}
			cfg.APISources = append(cfg.APISources, entry)
		}

		if err := s.cfgMgr.Update(cfg); err != nil {
			errJSON(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200, "message": "ok",
			"data": toAPISourceView(entry),
		})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			errJSON(w, http.StatusBadRequest, "缺少 id")
			return
		}
		cfg := s.cfgMgr.Get()
		out := make([]config.APISource, 0, len(cfg.APISources))
		removed := false
		for _, item := range cfg.APISources {
			if item.ID == id {
				removed = true
				continue
			}
			out = append(out, item)
		}
		if !removed {
			errJSON(w, http.StatusNotFound, "没有找到该音源")
			return
		}
		cfg.APISources = out
		if err := s.cfgMgr.Update(cfg); err != nil {
			errJSON(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "message": "ok"})

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		errJSON(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// HandleAPISourceDetect 探测服务地址属于哪种协议（不保存）
func (s *Server) HandleAPISourceDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	res, err := apisource.Detect(strings.TrimSpace(req.BaseURL), strings.TrimSpace(req.Token))
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"protocol": res.Protocol,
			"label":    res.Label,
			"detail":   res.Detail,
		},
	})
}

// HandleAPISourceResolve 用指定的 API 音源取一条可播直链
//
// 前端（以及外部程序）在需要取直链时调用它，从而无需在浏览器里执行任何第三方代码。
func (s *Server) HandleAPISourceResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var req struct {
		ID       string `json:"id"`
		Platform string `json:"platform"`
		SongID   string `json:"song_id"`
		Quality  string `json:"quality"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	cfg := s.cfgMgr.Get()
	var found *config.APISource
	for i := range cfg.APISources {
		if cfg.APISources[i].ID == strings.TrimSpace(req.ID) {
			found = &cfg.APISources[i]
			break
		}
	}
	if found == nil {
		errJSON(w, http.StatusNotFound, "没有找到该 API 音源")
		return
	}
	if !found.Enabled {
		errJSON(w, http.StatusServiceUnavailable, "该音源已停用")
		return
	}

	link, err := apisource.Resolve(
		found.Protocol, found.BaseURL, found.Token,
		req.Platform, req.SongID, req.Quality,
	)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"url":      link,
			"sourceId": found.ID,
			"name":     found.Name,
			"quality":  req.Quality,
		},
	})
}

// randomSuffix 生成一个短随机串做 id 后缀（不引入额外依赖）
func randomSuffix() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	n := time.Now().UnixNano()
	for i := range b {
		b[i] = chars[int(n>>uint(i*4))%len(chars)]
		n = n/7 + 31
	}
	return string(b)
}
