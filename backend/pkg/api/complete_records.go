package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/tags"
	"fn-lx-player/pkg/tidy"
)

// 补全记录（用户 2026-10-24）。
//
// 「补全结果只有计数，看不到到底给哪首歌补了什么，也没法撤单首」—— 这条路补上。
//
//	GET  /api/library/complete/records            列出全部备份（每次补全 = 一条记录）
//	GET  /api/library/complete/records?id=xxx     看某次的逐条明细（前后对照）
//	POST /api/library/complete/records            提交修正，or["..."] 批量撤回或改数据
//	POST /api/library/complete/records/restore    只撤回勾选的几条
//	POST /api/library/complete/records/edit       修正单条的标题/歌手（同时改写文件）
//
// 数据源就是补全时的**整文件备份清单**（pkg/complete/backup.go）：它本来就为了
// 「撤销」而存在，这里只是把它从「一个整批撤销按钮」变成「逐条可查、可撤、可改」。
// 不另开一套数据库 —— 两套记录必然会对不上。

// backupID 校验记录 id：只允许目录名形态（时间戳），杜绝路径穿越。
func backupID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ""
	}
	return id
}

// backupPath 把记录 id 拼成绝对路径，并确认它确实在备份根目录下。
func (s *Server) backupPath(id string) (string, bool) {
	dir := backupID(id)
	if dir == "" {
		return "", false
	}
	full := filepath.Join(s.aiBackupRoot, dir)
	if _, err := os.Stat(filepath.Join(full, complete.ManifestName)); err != nil {
		return "", false
	}
	return full, true
}

// HandleCompleteRecords 补全记录：列表 / 明细 / 修正。
func (s *Server) HandleCompleteRecords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if id := r.URL.Query().Get("id"); strings.TrimSpace(id) != "" {
			s.writeBackupDetail(w, id)
			return
		}
		dirs := complete.ListBackups(s.aiBackupRoot)
		list := make([]complete.BackupInfo, 0, len(dirs))
		for _, d := range dirs {
			list = append(list, complete.Describe(d, false))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{"root": s.aiBackupRoot, "records": list},
		})

	case http.MethodPost:
		s.handleRecordsEdit(w, r)

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) writeBackupDetail(w http.ResponseWriter, id string) {
	full, ok := s.backupPath(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "没有这次补全记录（可能已经全部撤销或删掉了）")
		return
	}
	info := complete.Describe(full, true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": info})
}

// recordsEditRequest 修正请求。两种动作走同一个口子，因为界面上的「撤回」与
// 「改数据」都是对同一批勾选项的操作，分成两个接口只会让前端多一套状态。
type recordsEditRequest struct {
	// ID 记录 id（备份目录名）
	ID string `json:"id"`
	// Items 要处理的条目（原文件路径）
	Items []string `json:"items"`
	// Op 动作：restore（撤回）/ edit（改数据）。空 = restore
	Op string `json:"op"`
	// Title / Artist 仅 op=edit 时有意义；空表示这个字段不改
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

func (s *Server) handleRecordsEdit(w http.ResponseWriter, r *http.Request) {
	var req recordsEditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	full, ok := s.backupPath(req.ID)
	if !ok {
		errJSON(w, http.StatusNotFound, "没有这次补全记录（可能已经全部撤销或删掉了）")
		return
	}
	if len(req.Items) == 0 {
		errJSON(w, http.StatusBadRequest, "没有勾选要处理的文件")
		return
	}
	// 只接受清单里真实存在的路径 —— 不能让调用方拿这个口子去动任意文件
	known := map[string]bool{}
	for _, e := range complete.ListEntries(full) {
		known[e.Original] = true
	}
	targets := make([]string, 0, len(req.Items))
	for _, p := range req.Items {
		if !known[p] {
			continue
		}
		targets = append(targets, p)
	}
	if len(targets) == 0 {
		errJSON(w, http.StatusBadRequest, "勾选的文件都不在这条记录里（可能已经撤过了）")
		return
	}

	if strings.TrimSpace(req.Op) == "edit" {
		s.applyRecordEdit(w, full, targets, req.Title, req.Artist)
		return
	}

	res := complete.RestoreEntry(full, targets)
	s.refreshIndexAfterRestore(res.RestoredPaths)

	if res.Restored == 0 && res.Failed > 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"code": 500, "message": "撤回失败，记录已保留可重试", "data": res,
		})
		return
	}
	msg := fmt.Sprintf("已撤回 %d 首", res.Restored)
	if res.Failed > 0 {
		msg += fmt.Sprintf("，%d 首失败（已保留在记录里可重试）", res.Failed)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": msg, "data": res})
}

// applyRecordEdit 修正单条的标题/歌手，并把新值真正写进音频文件。
//
// 「改数据」只对**标签文本**开放：歌词/封面是整块二进制，让用户手改没有意义，
// 要改就重新补一次（那会生成新的记录）。
func (s *Server) applyRecordEdit(w http.ResponseWriter, full string, targets []string, title, artist string) {
	title = strings.TrimSpace(title)
	artist = strings.TrimSpace(artist)
	if title == "" && artist == "" {
		errJSON(w, http.StatusBadRequest, "没有要改的字段（标题和歌手都是空的）")
		return
	}

	type editItem struct {
		Path string `json:"path"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
	}
	out := make([]editItem, 0, len(targets))
	changed := make([]string, 0, len(targets))
	var firstErr string
	for _, p := range targets {
		item := editItem{Path: p}
		opt := tidy.DefaultOptions()
		opt.EmbedLyric = false
		opt.WriteLRC = false
		opt.EmbedCover = false
		opt.WriteCoverFile = false
		opt.OverwriteLyric = false
		opt.OverwriteCover = false
		opt.WriteMetadata = true
		res := tidy.TidyOne(tidy.Item{Path: p, Title: title, Artist: artist}, opt)
		if !res.OK {
			item.Err = res.Error
			if firstErr == "" {
				firstErr = res.Error
			}
			out = append(out, item)
			continue
		}
		item.OK = true
		out = append(out, item)
		changed = append(changed, p)
		// 顺手把索引里的标题/歌手改成新值，免得下一轮补全又按旧值去搜
		old, _ := s.libraryIndex().Get(p)
		s.libraryIndex().MarkEnriched(p, library.Enrich{
			Title:    firstNonEmptyStr(title, old.Title),
			Artist:   firstNonEmptyStr(artist, old.Artist),
			Album:    old.Album,
			Genre:    old.Genre,
			Year:     old.Year,
			Track:    old.Track,
			Disc:     old.Disc,
			HasLyric: old.HasLyric,
			HasCover: old.HasCover,
		})
	}
	if len(changed) > 0 {
		_ = s.libraryIndex().Save()
		fnos.ScheduleLibraryRescan()
	}
	if len(changed) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"code": 500, "message": "改写失败：" + firstErr, "data": out,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": fmt.Sprintf("已改写 %d 首的标签", len(changed)), "data": out,
	})
}

// refreshIndexAfterRestore 撤回之后用**实际文件**重读一遍标签，刷新曲库索引。
//
// 不刷的话下一轮补全会因为「索引说有歌词」而跳过这些文件，
// 用户就会看到「撤销了但再也补不上」。
func (s *Server) refreshIndexAfterRestore(paths []string) {
	if len(paths) == 0 {
		return
	}
	for _, p := range paths {
		meta, err := tags.Read(p)
		if err != nil {
			// 读不回来就清掉「已读」标记，交给下一轮增量扫描重读
			s.libraryIndex().InvalidateTagRead(p)
			continue
		}
		needLyric, needCover, terr := tidy.NeedsTidy(p)
		if terr != nil {
			needLyric = strings.TrimSpace(meta.Lyric) == ""
			needCover = len(meta.Cover) == 0
		}
		s.libraryIndex().MarkEnriched(p, library.Enrich{
			Title:    meta.Title,
			Artist:   meta.Artist,
			Album:    meta.Album,
			Genre:    meta.Genre,
			Year:     meta.Year,
			Track:    meta.Track,
			Disc:     meta.Disc,
			HasLyric: !needLyric,
			HasCover: !needCover,
		})
	}
	_ = s.libraryIndex().Save()
}
