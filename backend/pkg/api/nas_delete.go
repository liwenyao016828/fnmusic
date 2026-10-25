package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/tidy"
)

// 删除单个/多个本地文件（用户 2026-10-24：「列表中的操作，去掉每行的歌词单独补全，
// 替换为删除按钮」）。
//
// 为什么默认是「移进回收站」而不是直接删：这是曲库管理页里的一次点击，
// 误触的代价是用户的歌没了。默认挪到曲库根目录的 `.tidy_trash`（与查重同一个回收站），
// 想真删必须显式传 `mode=delete` —— 危险动作不能是默认值。
//
// 删除会写一条**补全记录**（`<dataDir>/ai_backup/<时间戳>`）：它本来是给「补全撤销」
// 用的整文件备份设施，这里复用成「删掉的歌能一键恢复」。
// 不这么做的话，`mode=delete` 就是不可逆的 —— 界面上不该存在没有回滚手段的删除。
//
//	POST /api/nas/delete  {"paths":["/vol1/Music/a.flac"],"mode":"trash|delete","dry_run":false}
func (s *Server) HandleNASDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var req struct {
		Paths  []string `json:"paths"`
		Path   string   `json:"path"`
		Mode   string   `json:"mode"`
		DryRun bool     `json:"dry_run"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	paths := append([]string{}, req.Paths...)
	if p := strings.TrimSpace(req.Path); p != "" {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		errJSON(w, http.StatusBadRequest, "请提供 paths")
		return
	}
	if len(paths) > 200 {
		errJSON(w, http.StatusBadRequest, "一次最多删 200 个文件")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "trash"
	}
	if mode != "trash" && mode != "delete" {
		errJSON(w, http.StatusBadRequest, "mode 只能是 trash 或 delete")
		return
	}

	// ① 路径安全：每一个都必须过解析守卫（越权直接整批拒绝，不做一半）
	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		real, err := nas.ResolveSafePath(p)
		if err != nil {
			errJSON(w, http.StatusForbidden, "拒绝处理："+p+" —— "+err.Error())
			return
		}
		if fi, err := os.Stat(real); err != nil || fi.IsDir() {
			errJSON(w, http.StatusBadRequest, "不是可删除的文件："+p)
			return
		}
		resolved = append(resolved, real)
	}

	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "预览：没有改动任何文件",
			"data": map[string]interface{}{"mode": mode, "would_delete": resolved, "count": len(resolved)},
		})
		return
	}

	// ② 先备份（mode=delete 时这就是唯一的回滚手段）
	backup := complete.NewBackup(complete.NewBackupDir(s.aiBackupRoot))
	backedUp := 0
	for _, p := range resolved {
		if err := backup.BeforeWrite(p); err != nil {
			// 备份失败就不删这个文件 —— 没有回滚手段的删除不做
			errJSON(w, http.StatusInternalServerError, "备份失败，已中止（没有删除任何文件）："+err.Error())
			_ = os.RemoveAll(backup.Dir())
			return
		}
		backup.AfterWrite(p)
		backedUp++
	}
	if err := backup.Commit(); err != nil {
		errJSON(w, http.StatusInternalServerError, "写备份清单失败，已中止（没有删除任何文件）："+err.Error())
		_ = os.RemoveAll(backup.Dir())
		return
	}

	// ③ 删除 / 挪进回收站
	trashDir := joinTrash(resolved[0])
	res := tidy.ResolveDuplicates(
		[]tidy.DupGroup{{Type: "manual", Delete: resolved}},
		tidy.ResolveOptions{Mode: mode, TrashDir: trashDir},
	)

	// ④ 从索引里摘掉（否则曲库列表里还会显示一条点不开的记录）
	for _, p := range resolved {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			continue // 没删掉（失败）的留着
		}
		s.libraryIndex().Remove(p)
	}
	_ = s.libraryIndex().Save()
	fnos.ScheduleLibraryRescan()

	msg := "已挪进回收站"
	if mode == "delete" {
		msg = "已删除"
	}
	if res.Failed > 0 {
		msg += "，失败 " + itoa(res.Failed) + " 个"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": msg + "（可在「补全整理 → 数据快照」里恢复）",
		"data": map[string]interface{}{
			"mode": mode, "trash_dir": trashDir, "backed_up": backedUp,
			"moved": res.Moved, "deleted": res.Deleted, "failed": res.Failed,
			"errors": res.Errors, "backup_dir": backup.Dir(),
			"paths": resolved,
		},
	})
}

// itoa 小的整数转字符串（避免为了一个 %d 引入 fmt 在这里的噪音）
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
