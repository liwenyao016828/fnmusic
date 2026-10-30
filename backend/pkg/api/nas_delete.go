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
	trashDir := trashDirFunc(resolved[0])
	if mode == "trash" {
		// 回收站目录不可写时（权限、只读盘）提前说清楚并中止 ——
		// 否则会走到逐文件失败，用户只看到「失败 N 个」却不知道是回收站在哪。
		if err := os.MkdirAll(trashDir, 0o755); err != nil {
			_ = os.RemoveAll(backup.Dir())
			errJSON(w, http.StatusInternalServerError,
				"回收站目录不可写，已中止（没有删除任何文件）："+trashDir+" —— "+err.Error()+
					"；可在设置里切到「直接删除」，或给这个目录写权限")
			return
		}
	}
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
	// ⚠️ 2026-10-29：以前这里无论删成没删成都是一句 OK —— 文件一个都没动，前端却看到 code 200，
	// 于是把行从列表里摘掉（用户报的「列表删除了，实际没有删除」）。
	// 现在如实说：部分失败带原因是 207 语义（仍给 code 200 + failed，前端按 failed 判定），
	// 全部失败则直接回 500，让调用方没法把它当成功。
	if res.Failed > 0 && res.Moved == 0 && res.Deleted == 0 {
		errJSON(w, http.StatusInternalServerError,
			"删除失败，没有移除任何文件"+errTail(res.Errors))
		return
	}
	if res.Failed > 0 {
		msg += "，失败 " + itoa(res.Failed) + " 个" + errTail(res.Errors)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": msg + recoveryNote(mode, res.Failed),
		"data": map[string]interface{}{
			"mode": mode, "trash_dir": trashDir, "backed_up": backedUp,
			"moved": res.Moved, "deleted": res.Deleted, "failed": res.Failed,
			"errors": res.Errors, "backup_dir": backup.Dir(),
			"paths": resolved,
		},
	})
}

// trashDirFunc 选「回收站放哪」。默认就是 joinTrash；做成变量只为让测试能模拟
// 「回收站目录建不出来（权限/只读盘）」而不必依赖真实文件系统权限。
var trashDirFunc = joinTrash

// errTail 把前几条失败原因串成一句话（最多 3 条，避免响应体变成日志）
func errTail(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	n := len(errs)
	if n > 3 {
		n = 3
	}
	return "：" + strings.Join(errs[:n], "；")
}

// recoveryNote 部分失败时不能说「全部可在快照里恢复」—— 没删掉的文件本来就还在
func recoveryNote(mode string, failed int) string {
	if failed > 0 {
		return "（失败的文件没有任何改动）"
	}
	if mode == "delete" {
		return "（备份在数据快照里）"
	}
	return "（可在「补全整理 → 数据快照」里恢复）"
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
