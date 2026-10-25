package api

// 手动歌名体检（用户 2026-10-26）：
// 「把修复后的列改成手动歌名体检，点击后随机抽取 10 首歌曲交给 AI 修复，
//   并展示修复结果（包括是否可以修复、带原歌名、新歌名等）。」
//
// 与 /title-fix/scan 的区别，用户在乎的正是这一点：
//   - scan 只挑「歌名可疑」的，用来批量修；
//   - 体检是**随机抽样**，包括歌名看起来正常的 —— 用来回答「AI 到底靠不靠谱」。
//     只挑可疑样本会得出「AI 全都修对了」的假象。
//
// 纯只读：不写标签、不留快照，结果只是给用户看的一张表。
// 想真改，勾回去走 /title-fix/fix 或记录页的「修改」。
//
//	POST /api/library/title-fix/audit  body: {"dir":"","count":10}

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/library"
)

// titleAuditItem 体检表里的一行
type titleAuditItem struct {
	Path   string `json:"path"`
	File   string `json:"file"` // 文件名（表里显示这个，路径太长）
	Title  string `json:"title"`
	Artist string `json:"artist"`
	// Need 判定是否需要修（空 = 看起来正常）
	Need string `json:"need,omitempty"`
	// Fixable 能否修好：修好=有可用新歌名；修不了=AI 与规则都没把握
	Fixable   bool   `json:"fixable"`
	NewTitle  string `json:"new_title,omitempty"`
	NewArtist string `json:"new_artist,omitempty"`
	// Source 结果来源：ai（大模型）/ rule（文件名规则）/ none
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

// HandleTitleFixAudit POST /api/library/title-fix/audit
func (s *Server) HandleTitleFixAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	var req struct {
		Dir   string `json:"dir"`
		Count int    `json:"count"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	n := req.Count
	if n <= 0 || n > 20 {
		n = 10
	}
	if !s.aiReady() {
		errJSON(w, http.StatusBadRequest, "还没配置 AI 大模型 —— 到「系统 → 设置 → AI 大模型」里配好再来体检")
		return
	}

	dir := strings.TrimSpace(req.Dir)
	dirClean := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	pool := make([]library.Entry, 0, 256)
	for _, e := range s.libraryIndex().List() {
		if dirClean != "" && e.Path != dirClean && !strings.HasPrefix(filepath.ToSlash(e.Path), dirClean+"/") {
			continue
		}
		pool = append(pool, e)
	}
	if len(pool) == 0 {
		errJSON(w, http.StatusBadRequest, "曲库里还没有曲目 —— 先到「补全」里点一次「扫描」")
		return
	}
	// 随机抽（不是取前 N 个）：用户要的是「抽样看看」，前 N 个永远是同一批同目录的曲子
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if n > len(pool) {
		n = len(pool)
	}

	// 每首一次大模型调用，10 首要几十秒；给够时间但别无限等
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	out := make([]titleAuditItem, 0, n)
	fixable, needFix := 0, 0
	for _, e := range pool[:n] {
		it := titleAuditItem{
			Path: e.Path, File: filepath.Base(e.Path), Title: e.Title, Artist: e.Artist,
			Need: suspiciousTitle(e.Title, e.Artist, e),
		}
		if it.Need != "" {
			needFix++
		}
		title, artist, src := s.auditOne(ctx, e)
		it.NewTitle, it.NewArtist, it.Source = title, artist, src
		it.Fixable = strings.TrimSpace(title) != ""
		if it.Fixable {
			fixable++
		}
		switch {
		case it.Need == "" && src == "ai":
			it.Note = "歌名看着正常，AI 也没提出更合适的写法"
		case it.Need == "" && src == "none":
			it.Note = "歌名看着正常（AI 没给结论，可能这一首请求失败）"
		case it.Fixable && src == "rule":
			it.Note = "AI 没能给出可在文件名里核对的结论，退回按文件名规则"
		case !it.Fixable:
			it.Note = "AI 与规则都没把握 —— 建议手动改"
		}
		out = append(out, it)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"items": out, "sampled": len(out), "pool": len(pool),
			"need_fix": needFix, "fixable": fixable,
			"ai_ready": true,
		},
	})
}

// auditOne 对一首曲子算「AI 会给什么歌名」。口径与 suggestTitle 完全一致
// （**必须**一致：体检说能修、真修的时候却修不出来，这个功能就是在骗人），
// 差别只有一点：体检不打标签、也不做假值兜底 —— 拿不准就如实说拿不准。
func (s *Server) auditOne(ctx context.Context, e library.Entry) (title, artist, source string) {
	name := filepath.Base(e.Path)
	res := s.aiClient.ParseName(ctx, name)
	t := strings.TrimSpace(res.Title)
	a := strings.TrimSpace(res.Artist)
	if a == "" {
		a = strings.TrimSpace(e.Artist)
	}
	if t != "" && ai.NameAppearsIn(name, t) {
		return t, a, "ai"
	}
	if fromRule := tidyGuessTitle(name); fromRule != "" {
		return fromRule, a, "rule"
	}
	return "", a, "none"
}
