package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"fn-lx-player/pkg/nameparse"
)

// HandleNameRules 用户自定义的命名解析规则。
//
//	GET    /api/library/name-rules        列出全部规则
//	POST   /api/library/name-rules        生成 / 校验 / 保存（见下）
//	DELETE /api/library/name-rules?id=xx  删除一条
//
// POST 有三种用法，靠请求体里有没有 `regex` 区分：
//
//	① 生成：{sample, desc}        → 让 AI 出一条正则，**校验后返回建议，不保存**
//	② 校验：{regex, sample, dry_run:true} → 只回测，不保存
//	③ 保存：{regex, sample, ...}  → 校验通过才落盘
//
// ①③ 分开是刻意的：AI 产出的正则**必须先给用户看**。
// 它可能语法错误、可能匹配不上样例、可能提取不出字段 —— 用户看一眼就知道对不对，
// 直接存下去再让他在别处发现问题就晚了。
//
// 用户规则优先于内置启发式（`nameparse.Store.Parse`）—— 它是用户显式定义的意图。
func (s *Server) HandleNameRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"rules": s.nameRules.List(),
				// 允许的组名透出去，前端做提示用（别让用户自己猜）
				"allowed_fields": nameparse.AllowedFields,
			},
		})

	case http.MethodPost:
		var req struct {
			Sample   string   `json:"sample"`
			Desc     string   `json:"desc"`
			ID       string   `json:"id"`
			Regex    string   `json:"regex"`
			Fields   []string `json:"fields"`
			Name     string   `json:"name"`
			Note     string   `json:"note"`
			Disabled *bool    `json:"disabled"`
			DryRun   bool     `json:"dry_run"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}

		// ① 生成模式：给了样例但没给正则 → 请 AI 出，校验后返回建议（不保存）
		if strings.TrimSpace(req.Regex) == "" {
			sample := strings.TrimSpace(req.Sample)
			if sample == "" {
				errJSON(w, http.StatusBadRequest, "请提供 sample（文件名样例）")
				return
			}
			if s.aiClient == nil || !s.aiClient.RawConfig().Available() {
				errJSON(w, http.StatusBadRequest, "还没配置 AI，无法生成规则。可以自己写正则后用「校验」测一下。")
				return
			}
			sug, ok := s.aiClient.SuggestNameRegex(r.Context(), sample, req.Desc)
			if !ok {
				errJSON(w, http.StatusBadGateway, "AI 没能生成规则，换个样例或把命名方式说得更具体一点")
				return
			}
			// ⚠️ AI 产出的正则必须回测，否则不给用户看
			probe := nameparse.Rule{Regex: sug.Regex, Sample: sample}
			if err := nameparse.ValidateRule(probe); err != nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"code": 200, "message": "AI 生成的规则没通过校验，请手动修正",
					"data": map[string]interface{}{
						"regex": sug.Regex, "fields": sug.Fields, "note": sug.Note,
						"valid": false, "error": err.Error(),
					},
				})
				return
			}
			parsed, _ := nameparse.TestRule(sug.Regex, sample)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "规则已生成，确认后再保存",
				"data": map[string]interface{}{
					"regex": sug.Regex, "fields": sug.Fields, "note": sug.Note,
					"valid": true, "preview": parsed,
				},
			})
			return
		}

		// ②③ 校验 / 保存
		rule := nameparse.Rule{
			ID:     strings.TrimSpace(req.ID),
			Regex:  strings.TrimSpace(req.Regex),
			Fields: req.Fields,
			Name:   strings.TrimSpace(req.Name),
			Note:   strings.TrimSpace(req.Note),
			Sample: strings.TrimSpace(req.Sample),
			Source: "manual",
		}
		if req.Disabled != nil {
			rule.Disabled = *req.Disabled
		}
		// 更新已有规则时保持原有来源（AI 生成的别被改成 manual）
		if rule.ID != "" {
			for _, old := range s.nameRules.List() {
				if old.ID == rule.ID {
					if old.Source != "" {
						rule.Source = old.Source
					}
					if rule.Sample == "" {
						rule.Sample = old.Sample
					}
					break
				}
			}
		}

		if err := nameparse.ValidateRule(rule); err != nil {
			errJSON(w, http.StatusBadRequest, "规则没通过校验："+err.Error())
			return
		}
		if req.DryRun {
			parsed, _ := nameparse.TestRule(rule.Regex, rule.Sample)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "校验通过", "data": map[string]interface{}{"preview": parsed},
			})
			return
		}

		saved, err := s.nameRules.Upsert(rule)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, "保存失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "已保存", "data": saved,
		})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			errJSON(w, http.StatusBadRequest, "请提供 id")
			return
		}
		if err := s.nameRules.Remove(id); err != nil {
			errJSON(w, http.StatusInternalServerError, "删除失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已删除"})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
