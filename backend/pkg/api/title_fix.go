package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/nameparse"
	"fn-lx-player/pkg/tidy"
)

// AI 修复歌名（用户 2026-10-24：「体检 → 歌名修复：增加 ai 修复歌名卡片」）。
//
// 解决的是「标签里根本没有歌名，或歌名里塞了文件名垃圾」这一类和「非法文件名」
// 完全不同的问题 —— 后者是磁盘条目本身不合规（见 pkg/nas/health.go），
// 前者是元数据是空的/脏的，文件能正常打开，只是显示成 `01 - 未知.part` 这种。
//
// 走 AI 是因为这类名字**规则拆不出来**：`【HQ】Artist_Name_2003`、
// `Track03_周杰伦-晴天_(live)`。规则层（pkg/nameparse）先给候选，
// AI 只在候选里挑，不许凭空创作 —— 与 ParseName/JudgeName 同一套防幻觉口径。
//
//	GET  /api/library/title-fix/scan   扫出「歌名可疑」的曲目（只读）
//	POST /api/library/title-fix/fix    对勾选的曲目跑 AI 修正并写回标签
//
// 写回走 tidy.TidyOne 的 WriteMetadata 分支，并且**同步写一条补全记录**：
// 改了用户的文件就得能撤回，不能因为「这不是补全任务」就没有回滚手段。

type titleFixItem struct {
	Path   string `json:"path"`
	Old    string `json:"old"`    // 现在的标题（空 = 没有标题）
	Artist string `json:"artist"` // 现在的歌手，给 AI 当上下文
	Reason string `json:"reason"` // 为什么被判为可疑
	// AI 的结论（fix 之后回填）
	Suggest       string `json:"suggest,omitempty"`
	SuggestArtist string `json:"suggest_artist,omitempty"`
	OK            bool   `json:"ok,omitempty"`
	Error         string `json:"error,omitempty"`
}

// suspiciousTitle 判断一个标题是否值得交给 AI 看。**宁可多报不可漏报**：
// 这里只是筛选候选，真正的判断在 AI 那一步（且写回前还有一次「新值是否像话」的校验）。
func suspiciousTitle(title, artist string, e library.Entry) string {
	t := strings.TrimSpace(title)
	ext := strings.ToLower(filepath.Ext(e.Path))
	stem := strings.TrimSuffix(filepath.Base(e.Path), filepath.Ext(e.Path))

	switch {
	case t == "":
		return "没有歌名标签"
	case t == "未知" || t == "unknown" || strings.EqualFold(t, "untitled"):
		return "歌名是占位词"
	case strings.ContainsAny(t, "/\\=_|[]{}") && strings.Contains(t, artist) == false:
		return "歌名里混着文件名符号"
	case strings.EqualFold(t, stem):
		// 标题跟文件名一模一样 —— 说明从来没正确填过，只是把文件名抄进了标签
		return "歌名就是文件名"
	case len([]rune(t)) > 60:
		return "歌名异常长（多半是整条文件名）"
	case strings.HasPrefix(t, "Track") && len(t) <= 8:
		return "歌名是曲序号"
	}
	_ = ext
	return ""
}

// tidyGuessTitle 规则层（pkg/nameparse）从文件名拆出的歌名。AI 没给结论时的兜底，
// 与「补全」段用的是同一套解析规则 —— 两处口径必须一致，否则同一首歌会两处结论不同。
func tidyGuessTitle(name string) string {
	r := nameparse.Parse(name)
	return strings.TrimSpace(r.Title)
}

// HandleTitleFixScan GET/POST /api/library/title-fix/scan
func (s *Server) HandleTitleFixScan(w http.ResponseWriter, r *http.Request) {
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir == "" && r.Method == http.MethodPost {
		var body struct {
			Dir string `json:"dir"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
		dir = strings.TrimSpace(body.Dir)
	}
	limit := 200

	entries := s.libraryIndex().List()
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	out := make([]titleFixItem, 0, limit)
	total := 0
	for _, e := range entries {
		if dirClean != "" && e.Path != dirClean && !strings.HasPrefix(filepath.ToSlash(e.Path), dirClean+"/") {
			continue
		}
		if reason := suspiciousTitle(e.Title, e.Artist, e); reason != "" {
			total++
			if len(out) < limit {
				out = append(out, titleFixItem{
					Path: e.Path, Old: e.Title, Artist: e.Artist, Reason: reason,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir": dir, "total": total, "items": out, "truncated": total > len(out),
			// AI 没配置时界面要能说清「不是我扫不到，是你没配 AI」
			"ai_ready": s.aiReady(),
		},
	})
}

// aiReady AI 是否配置好了（未配置时扫描照常工作，只是修不了）
func (s *Server) aiReady() bool {
	return s.aiClient != nil && s.aiClient.Config() != nil
}

// HandleTitleFixFix POST /api/library/title-fix/fix  body: {"dir":"","items":[{"path":".."}]}
func (s *Server) HandleTitleFixFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var req struct {
		Dir   string   `json:"dir"`
		Paths []string `json:"paths"`
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	paths := append([]string{}, req.Paths...)
	for _, it := range req.Items {
		paths = append(paths, it.Path)
	}
	if len(paths) == 0 {
		errJSON(w, http.StatusBadRequest, "没有勾选要修的歌")
		return
	}
	if len(paths) > 50 {
		errJSON(w, http.StatusBadRequest, "一次最多修 50 首（每首要过一次大模型）")
		return
	}
	if !s.aiReady() {
		errJSON(w, http.StatusBadRequest, "还没配置 AI 大模型 —— 到「系统 → 设置 → AI」里配好再来")
		return
	}

	// 只为**记录页可撤回**而建备份：写回标签是不可逆操作，没有备份就不该动用户文件。
	backup := complete.NewBackup(complete.NewBackupDir(s.aiBackupRoot))

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	out := make([]titleFixItem, 0, len(paths))
	fixed := 0
	for _, p := range paths {
		entry, ok := s.libraryIndex().Get(p)
		item := titleFixItem{Path: p, Old: entry.Title, Artist: entry.Artist}
		if !ok {
			item.Error = "曲库索引里没有这条记录（先跑一次「扫描」）"
			out = append(out, item)
			continue
		}
		// 嫌疑判定在写回时再算一次：索引可能刚被别的操作刷过
		item.Reason = suspiciousTitle(entry.Title, entry.Artist, entry)
		if item.Reason == "" {
			item.Error = "这首歌的歌名看起来没问题，跳过"
			out = append(out, item)
			continue
		}

		newTitle, newArtist := s.suggestTitle(ctx, p, entry)
		item.Suggest, item.SuggestArtist = newTitle, newArtist
		if newTitle == "" {
			item.Error = "AI 没给出可用的歌名（未启用或没认出来）"
			out = append(out, item)
			continue
		}
		if req.DryRun {
			item.OK = true
			out = append(out, item)
			continue
		}

		opt := tidy.DefaultOptions()
		opt.EmbedLyric, opt.WriteLRC = false, false
		opt.EmbedCover, opt.WriteCoverFile = false, false
		opt.OverwriteLyric, opt.OverwriteCover = false, false
		opt.WriteMetadata = true
		if backup != nil {
			if err := backup.BeforeWrite(p); err != nil {
				item.Error = "备份原文件失败，已跳过：" + err.Error()
				out = append(out, item)
				continue
			}
		}
		res := tidy.TidyOne(tidy.Item{Path: p, Title: newTitle, Artist: newArtist}, opt)
		if backup != nil {
			backup.AfterWrite(p)
		}
		if !res.OK {
			item.Error = res.Error
			out = append(out, item)
			continue
		}
		if backup != nil {
			// 记下改前改后：记录页要显示「《未知》→《晴天》」，只有新值看不出改了什么
			fields := []string{"title"}
			if strings.TrimSpace(newArtist) != "" {
				fields = append(fields, "artist")
			}
			backup.NoteWithOld(p, entry.Title, entry.Artist,
				newTitle, firstNonEmptyStr(newArtist, entry.Artist), fields)
		}
		item.OK = true
		fixed++
		out = append(out, item)

		s.libraryIndex().MarkEnriched(p, library.Enrich{
			Title:    newTitle,
			Artist:   firstNonEmptyStr(newArtist, entry.Artist),
			Album:    entry.Album,
			Genre:    entry.Genre,
			Year:     entry.Year,
			Track:    entry.Track,
			Disc:     entry.Disc,
			HasLyric: entry.HasLyric,
			HasCover: entry.HasCover,
		})
	}

	if backup != nil {
		if err := backup.Commit(); err != nil {
			// 备份清单写不进去 = 这批改动撤不了，必须如实说
			log.Printf("[TITLE-FIX] 补全记录清单写入失败（这批歌名修改可能无法撤回）：%v", err)
		}
	}
	if fixed > 0 {
		_ = s.libraryIndex().Save()
		fnos.ScheduleLibraryRescan()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": fmt.Sprintf("修好 %d 首", fixed),
		"data": map[string]interface{}{"items": out, "fixed": fixed, "dry_run": req.DryRun},
	})
}

// suggestTitle 让 AI 在「文件名 + 现有标题 + 歌手」之间给出歌名与歌手。
//
// 防幻觉与 ParseName 同口径：**新歌名必须能在文件名里找到**（忽略空格与标点），
// 否则不采用 —— 宁可留着旧值让用户手改，也不能把模型编的名字写进文件。
func (s *Server) suggestTitle(ctx context.Context, path string, e library.Entry) (string, string) {
	name := filepath.Base(path)
	res := s.aiClient.ParseName(ctx, name)
	title := strings.TrimSpace(res.Title)
	artist := strings.TrimSpace(res.Artist)

	// 现有标签里的信息也要用上：文件名烂、但 Artist 标签是对的，别把它丢掉
	if artist == "" {
		artist = strings.TrimSpace(e.Artist)
	}
	if title == "" || !ai.NameAppearsIn(name, title) {
		// 退一步：用规则层拆出来的歌名（有些名字规则就够用）
		if fromRule := tidyGuessTitle(name); fromRule != "" {
			return fromRule, artist
		}
		return "", artist
	}
	return title, artist
}

// HandleTitleFixApply POST /api/library/title-fix/apply
//
// body: {"items":[{"path":"..","title":"..","artist":".."}]}
//
// 用户手写的歌名（2026-10-26）：「修复记录要能改回手写标题」。
// 与 /title-fix/fix 的区别：那个是 AI 猜，这个是**用户自己填的**——
// 不需要 AI 可用，也不做「必须在文件名里找得到」的校验（用户说了算），
// 但同样要先备份、同样进补全记录（所以同样能撤回）。
func (s *Server) HandleTitleFixApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var req struct {
		Items []struct {
			Path   string `json:"path"`
			Title  string `json:"title"`
			Artist string `json:"artist"`
		} `json:"items"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(req.Items) == 0 {
		errJSON(w, http.StatusBadRequest, "没有要改的曲目")
		return
	}
	if len(req.Items) > 50 {
		errJSON(w, http.StatusBadRequest, "一次最多改 50 首")
		return
	}

	backup := complete.NewBackup(complete.NewBackupDir(s.aiBackupRoot))
	out := make([]titleFixItem, 0, len(req.Items))
	fixed := 0
	for _, it := range req.Items {
		p := strings.TrimSpace(it.Path)
		title := strings.TrimSpace(it.Title)
		artist := strings.TrimSpace(it.Artist)
		item := titleFixItem{Path: p, Suggest: title, SuggestArtist: artist}
		if p == "" || title == "" {
			item.Error = "路径或歌名是空的"
			out = append(out, item)
			continue
		}
		entry, ok := s.libraryIndex().Get(p)
		if !ok {
			item.Error = "曲库索引里没有这条记录（先跑一次「扫描」）"
			out = append(out, item)
			continue
		}
		item.Old, item.Artist = entry.Title, entry.Artist
		if title == entry.Title && (artist == "" || artist == entry.Artist) {
			item.Error = "和现在一样，不用改"
			out = append(out, item)
			continue
		}
		opt := tidy.DefaultOptions()
		opt.EmbedLyric, opt.WriteLRC = false, false
		opt.EmbedCover, opt.WriteCoverFile = false, false
		opt.OverwriteLyric, opt.OverwriteCover = false, false
		opt.WriteMetadata = true
		if backup != nil {
			if err := backup.BeforeWrite(p); err != nil {
				item.Error = "备份原文件失败，已跳过：" + err.Error()
				out = append(out, item)
				continue
			}
		}
		res := tidy.TidyOne(tidy.Item{Path: p, Title: title, Artist: firstNonEmptyStr(artist, entry.Artist)}, opt)
		if backup != nil {
			backup.AfterWrite(p)
		}
		if !res.OK {
			item.Error = res.Error
			out = append(out, item)
			continue
		}
		if backup != nil {
			fields := []string{"title"}
			if artist != "" && artist != entry.Artist {
				fields = append(fields, "artist")
			}
			backup.NoteWithOld(p, entry.Title, entry.Artist, title, firstNonEmptyStr(artist, entry.Artist), fields)
		}
		s.libraryIndex().MarkEnriched(p, library.Enrich{
			Title: title, Artist: firstNonEmptyStr(artist, entry.Artist),
			Album: entry.Album, Genre: entry.Genre, Year: entry.Year,
			Track: entry.Track, Disc: entry.Disc,
			HasLyric: entry.HasLyric, HasCover: entry.HasCover,
		})
		item.OK = true
		item.Suggest = title
		fixed++
		out = append(out, item)
	}
	if backup != nil {
		if err := backup.Commit(); err != nil {
			log.Printf("[TITLE-FIX] 补全记录清单写入失败（这批修改可能无法撤回）：%v", err)
		}
	}
	if fixed > 0 {
		_ = s.libraryIndex().Save()
		fnos.ScheduleLibraryRescan()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": fmt.Sprintf("改好 %d 首", fixed),
		"data": map[string]interface{}{"items": out, "fixed": fixed},
	})
}
