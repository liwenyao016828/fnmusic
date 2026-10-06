package sources

// 音源管理的批量操作：批量删除 / 导出 / 清理重复。
//
// 说明：本项目的音源脚本只能在浏览器沙箱执行，因此「检测音源是否可用」由前端
// 完成（见 frontend/src/engine/lx-runtime.js 的 probeSource），后端不重复实现。
// 本文件提供的是不依赖执行脚本的管理能力。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
)

// HandleBatchDelete 批量删除音源
//
// POST /api/sources/batch_delete
//
// body: {"ids": ["custom_xxx", "custom_yyy"]}
//
// 删除激活中的音源时会自动改选剩余的第一个（或清空）。
func (m *Manager) HandleBatchDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "invalid payload: " + err.Error()})
		return
	}
	if len(req.IDs) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "请提供 ids"})
		return
	}

	target := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" {
			target[id] = true
		}
	}

	cfg := m.cfgMgr.Get()
	kept := make([]config.CustomSource, 0, len(cfg.CustomSources))
	deleted := make([]string, 0, len(target))
	for _, cs := range cfg.CustomSources {
		if target[cs.ID] {
			deleted = append(deleted, cs.ID)
			continue
		}
		kept = append(kept, cs)
	}
	cfg.CustomSources = kept

	// 被删的源如果在启用池里，一起摘掉（摘空且还有别的源时兜底启用第一个）——
	// 与单值时代「激活音源被删掉时自动改选」保持一致，不留悬空引用。
	fallback := ""
	if len(kept) > 0 {
		fallback = kept[0].ID
	}
	activeReset := false
	for id := range target {
		if removeFromPool(&cfg, id, fallback) {
			activeReset = true
		}
	}
	if err := m.cfgMgr.Update(cfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 500, "message": "保存失败: " + err.Error()})
		return
	}

	notFound := make([]string, 0)
	for id := range target {
		found := false
		for _, d := range deleted {
			if d == id {
				found = true
				break
			}
		}
		if !found {
			notFound = append(notFound, id)
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"deleted":          deleted,
			"deleted_count":    len(deleted),
			"not_found":        notFound,
			"remaining":        len(kept),
			"active_reset":     activeReset,
			"active_source_id": cfg.ActiveSourceID,
		},
	})
}

// HandleExportSources 导出全部音源，用于备份或迁移到其他设备
//
// GET /api/sources/export
//
// 返回结构可直接喂给 POST /api/sources/import_batch 的 scripts 字段实现还原。
func (m *Manager) HandleExportSources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	cfg := m.cfgMgr.Get()
	items := make([]map[string]interface{}, 0, len(cfg.CustomSources))
	scripts := make([]map[string]string, 0, len(cfg.CustomSources))

	for _, cs := range cfg.CustomSources {
		items = append(items, map[string]interface{}{
			"id": cs.ID, "name": cs.Name, "version": cs.Version,
			"author": cs.Author, "description": cs.Description,
			"created_at": cs.CreatedAt, "size": len(cs.Script),
		})
		scripts = append(scripts, map[string]string{
			"filename": cs.Name + ".js",
			"content":  cs.Script,
			"name":     cs.Name,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"version":          1,
			"exported_at":      time.Now().Unix(),
			"count":            len(items),
			"active_source_id": cfg.ActiveSourceID,
			"sources":          items,
			// scripts 可直接作为 import_batch 的 body 还原
			"scripts": scripts,
			"hint":    "把 data.scripts 原样作为 POST /api/sources/import_batch 的 body 即可在新设备还原。",
		},
	})
}

// HandleCleanupSources 清理重复音源（脚本内容相同者只保留最早的一条）
//
// POST /api/sources/cleanup
//
// body: {"dry_run": true}   默认 true，只报告不删除
//
// 兼容历史数据：早期导入使用时间戳 ID，可能与后来导入的同内容脚本并存。
func (m *Manager) HandleCleanupSources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DryRun *bool `json:"dry_run"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	}
	// 默认干跑：清理是破坏性操作，必须显式传 false 才真删
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	cfg := m.cfgMgr.Get()

	// 按内容哈希分组，组内按创建时间升序 → 保留最早的一条
	groups := make(map[string][]config.CustomSource)
	for _, cs := range cfg.CustomSources {
		sum := sha256.Sum256([]byte(cs.Script))
		key := hex.EncodeToString(sum[:])
		groups[key] = append(groups[key], cs)
	}

	duplicateGroups := make([]map[string]interface{}, 0)
	removeIDs := make(map[string]bool)
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].CreatedAt < group[j].CreatedAt })
		keep := group[0]
		dupes := make([]map[string]interface{}, 0, len(group)-1)
		for _, cs := range group[1:] {
			removeIDs[cs.ID] = true
			dupes = append(dupes, map[string]interface{}{
				"id": cs.ID, "name": cs.Name, "created_at": cs.CreatedAt,
			})
		}
		duplicateGroups = append(duplicateGroups, map[string]interface{}{
			"keep":   map[string]interface{}{"id": keep.ID, "name": keep.Name, "created_at": keep.CreatedAt},
			"remove": dupes,
		})
	}

	removed := make([]string, 0, len(removeIDs))
	if !dryRun && len(removeIDs) > 0 {
		kept := make([]config.CustomSource, 0, len(cfg.CustomSources))
		for _, cs := range cfg.CustomSources {
			if removeIDs[cs.ID] {
				removed = append(removed, cs.ID)
				continue
			}
			kept = append(kept, cs)
		}
		cfg.CustomSources = kept
		if removeIDs[cfg.ActiveSourceID] && len(kept) > 0 {
			cfg.ActiveSourceID = kept[0].ID
		}
		if err := m.cfgMgr.Update(cfg); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 500, "message": "保存失败: " + err.Error()})
			return
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dry_run":          dryRun,
			"duplicate_groups": duplicateGroups,
			"duplicate_count":  len(removeIDs),
			"removed":          removed,
			"remaining":        len(cfg.CustomSources),
			"hint": fmt.Sprintf("默认 dry_run=true 只报告不删除；确认无误后传 {\"dry_run\": false} 执行。"+
				"当前共 %d 个音源，其中 %d 个重复。", len(cfg.CustomSources), len(removeIDs)),
		},
	})
}
