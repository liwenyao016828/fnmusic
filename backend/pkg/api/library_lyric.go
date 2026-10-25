package api

// 缺歌词的文件：批量找词 + 写入（用户 2026-10-25 需求 N）。
//
//	POST /api/library/lyric/gaps    {dir, offset?, limit?, refresh?} → 缺歌词清单（分页）
//	POST /api/library/lyric/fetch   {dir, paths[], embed?}          → 只取词，预览用
//	POST /api/library/lyric/write   {dir, paths[], embed?}          → 同一批真的写盘
//
// # 为什么和「补全整理」分开
//
// 补全整理那一套围绕「缺标签 / 缺封面 / 可播性」；歌词有自己的命中率问题
// （网易按 id 直取 → 网易 cloudsearch → 酷狗严格校验，三级都可能落空），
// 用户要的是「先看清哪些歌真的没词、再决定补哪几首」，所以单开一组接口，
// 写法与歌名修复（`/api/library/title-fix/*`）保持一致。
//
// # 网络尽力而为
//
// 取词走 pkg/search.FetchLyric，它**不带 context**、单次最长几秒。所以这里：
//   - 并发上限 4（别把第三方接口打挂，也别把本机拖慢）
//   - 整批有墙钟预算，超时的条目报 timed_out 并如实上报（用户点名要写清「网络尽力而为」）
//   - 取到空也算结果：歌词本来就可能没有，界面按 reason 区分

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/tidy"
)

const (
	lyricGapDefaultLimit = 200
	lyricGapMaxLimit     = 1000
	lyricFetchWorkers    = 4
	// 一批取词的墙钟预算。超出的条目直接报 timed_out，不假装成功。
	lyricFetchBudget = 45 * time.Second
	lyricWriteMax    = 200
)

// lyricGapItem 是一首缺歌词的歌（清单用）。
type lyricGapItem struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Format string `json:"format,omitempty"`
}

// lyricHit 是一条取词结果。
type lyricHit struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Lyric  string `json:"lyric,omitempty"`
	OK     bool   `json:"ok"`
	// Reason：found / not_found / timed_out / no_meta
	Reason string `json:"reason"`
	Note   string `json:"note,omitempty"`
	// 写盘结果（只有 write 接口会填）
	Written bool   `json:"written,omitempty"`
	Err     string `json:"err,omitempty"`
}

type lyricGapRequest struct {
	Dir     string   `json:"dir"`
	Offset  int      `json:"offset"`
	Limit   int      `json:"limit"`
	Refresh bool     `json:"refresh"`
	Paths   []string `json:"paths"`
	// Embed：写盘时是否内嵌进音频标签（缺省 true）。外挂 .lrc 总是写 ——
	// 飞牛与本应用都读它，少写一个等于换播放器就没词。
	Embed *bool `json:"embed"`
}

// HandleLibraryLyricGaps 列出**真的没有歌词**的文件。
//
// 判据用 tidy.NeedsTidy（内嵌 + 同名外挂 .lrc 都算有词），与曲库管家、
// 整理页的「缺歌词」口径完全一致 —— 两边各写一套判断迟早互相矛盾。
func (s *Server) HandleLibraryLyricGaps(w http.ResponseWriter, r *http.Request) {
	var req lyricGapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求格式有误: "+err.Error())
		return
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}
	index := s.libraryIndex()
	all := make([]lyricGapItem, 0, 64)
	for _, e := range index.List() {
		if !strings.HasPrefix(e.Path, resolved+"/") && e.Path != resolved {
			continue
		}
		if e.HasLyric {
			continue
		}
		// 索引里 has_lyric 可能还没读过（TagReadAt=0）：这时不能凭标志判断，
		// 必须真读一次（NeedsTidy 既看内嵌也看外挂文件），否则会把有词的歌列进来。
		if e.TagReadAt == 0 {
			needLyric, _, terr := tidy.NeedsTidy(e.Path)
			if terr != nil || !needLyric {
				continue
			}
		}
		all = append(all, lyricGapItem{
			Path:   e.Path,
			Name:   filepath.Base(e.Path),
			Title:  e.Title,
			Artist: e.Artist,
			Format: e.Format,
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })

	limit := req.Limit
	if limit <= 0 {
		limit = lyricGapDefaultLimit
	}
	if limit > lyricGapMaxLimit {
		limit = lyricGapMaxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir":       resolved,
			"total":     len(all),
			"offset":    offset,
			"limit":     limit,
			"items":     all[offset:end],
			"truncated": end < len(all),
		},
	})
}

// HandleLibraryLyricFetch 批量取词，只预览（不写文件）。
func (s *Server) HandleLibraryLyricFetch(w http.ResponseWriter, r *http.Request) {
	s.handleLyricBatch(w, r, false)
}

// HandleLibraryLyricWrite 批量取词并写盘（内嵌 + 同名 .lrc）。
func (s *Server) HandleLibraryLyricWrite(w http.ResponseWriter, r *http.Request) {
	s.handleLyricBatch(w, r, true)
}

func (s *Server) handleLyricBatch(w http.ResponseWriter, r *http.Request, write bool) {
	var req lyricGapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求格式有误: "+err.Error())
		return
	}
	if len(req.Paths) == 0 {
		errJSON(w, http.StatusBadRequest, "请提供 paths")
		return
	}
	if write && len(req.Paths) > lyricWriteMax {
		errJSON(w, http.StatusBadRequest, "一次最多写 "+strconv.Itoa(lyricWriteMax)+" 首")
		return
	}
	resolved := ""
	if dir := strings.TrimSpace(req.Dir); dir != "" {
		var err error
		if resolved, err = resolveLibraryPath(dir); err != nil {
			errJSON(w, http.StatusForbidden, err.Error())
			return
		}
	}
	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := resolveLibraryPath(p)
		if err != nil {
			continue // 越界/不存在的直接跳过，不让它进结果
		}
		if resolved != "" && !strings.HasPrefix(abs, resolved+"/") && abs != resolved {
			continue
		}
		paths = append(paths, abs)
	}
	if len(paths) == 0 {
		errJSON(w, http.StatusBadRequest, "没有可处理的文件")
		return
	}

	hits := make([]lyricHit, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, lyricFetchWorkers)
	deadline := time.Now().Add(lyricFetchBudget)
	for idx, p := range paths {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if time.Now().After(deadline) {
				hits[idx] = lyricHit{Path: p, Name: filepath.Base(p), Reason: "timed_out", Note: "本批已超时，这一首没来得及尝试"}
				return
			}
			hits[idx] = s.fetchLyricForFile(p)
		}(idx, p)
	}
	wg.Wait()

	if write {
		embed := true
		if req.Embed != nil {
			embed = *req.Embed
		}
		for idx := range hits {
			h := &hits[idx]
			if !h.OK || strings.TrimSpace(h.Lyric) == "" {
				continue
			}
			res := tidy.TidyOne(tidy.Item{Path: h.Path, Lyric: h.Lyric}, tidy.Options{
				WriteLRC:       true,
				EmbedLyric:     embed,
				OverwriteLyric: false, // 已经有词的文件不覆盖（清单里本来也不该有）
			})
			if res.Error != "" {
				h.OK = false
				h.Reason = "write_failed"
				h.Err = res.Error
				continue
			}
			h.Written = res.LyricFile || res.LyricEmbed
			if !h.Written {
				h.Reason = "write_skipped"
				h.Note = strings.Join(res.Skipped, "；")
			}
		}
		// 写完让索引的 has_lyric 立刻正确（否则清单里还会留着这些歌）
		index := s.libraryIndex()
		for _, h := range hits {
			if h.Written {
				index.InvalidateTagRead(h.Path) // 下次增量富化会重读标签，has_lyric 变 true
			}
		}
		_ = index.Save()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"items": hits,
			"write": write,
		},
	})
}

// fetchLyricForFile 给一个文件取歌词：歌名/歌手优先用文件名反解（命名格式见
// pkg/namefmt），认不出就把整个文件名当歌名 —— 与「歌名修复」的判断口径一致。
//
// 取词顺序：先按平台搜到曲目 id 再直取（最准），再让 FetchLyric 自己走三级兜底。
func (s *Server) fetchLyricForFile(path string) lyricHit {
	h := lyricHit{Path: path, Name: filepath.Base(path)}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title, artist := namefmt.Parse(base)
	// 索引里有真标签就优先用它：文件名往往带「01 」这样的序号前缀，
	// 直接当歌名去搜十次有九次搜不到（.dev/music 里实测的第一条就是这样）。
	// 索引没读过标签（TagReadAt=0）或标题等于文件名时才退回文件名反解。
	if idx := s.libraryIndex(); idx != nil {
		if e, ok := idx.Get(path); ok && e.TagReadAt > 0 {
			if t := strings.TrimSpace(e.Title); t != "" && t != strings.TrimSpace(base) {
				title = t
			}
			if a := strings.TrimSpace(e.Artist); a != "" {
				artist = a
			}
		}
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSpace(base)
	}
	h.Title, h.Artist = title, artist
	if h.Title == "" {
		h.Reason = "no_meta"
		h.Note = "文件名里认不出歌名，先跑「歌名修复」"
		return h
	}

	keyword := h.Title
	if h.Artist != "" {
		keyword = h.Artist + " " + h.Title
	}
	for _, p := range []string{"wy", "tx", "kg"} {
		for _, cand := range search.Search(keyword, p, 1, 8) {
			if !lyricTitleMatch(cand.Name, h.Title) {
				continue
			}
			if h.Artist != "" && !lyricArtistMatch(cand.Singer, h.Artist) {
				continue
			}
			lrc, _ := search.FetchLyric(p, cand.Songmid, cand.Name, cand.Singer, cand.Interval, cand.Hash)
			if strings.TrimSpace(lrc) != "" {
				h.Title, h.Artist = cand.Name, cand.Singer
				h.Lyric, h.OK, h.Reason = lrc, true, "found"
				return h
			}
		}
	}
	// 平台都没直给：按歌名让 FetchLyric 自己走「网易 cloudsearch → 酷狗」兜底
	if lrc, _ := search.FetchLyric("", "", h.Title, h.Artist, 0, ""); strings.TrimSpace(lrc) != "" {
		h.Lyric, h.OK, h.Reason = lrc, true, "found"
		return h
	}
	h.Reason = "not_found"
	h.Note = "各平台都没找到歌词（网络尽力而为，可稍后重试）"
	return h
}

// lyricTitleMatch 歌名比对：去掉括号里的版本后缀、繁简不处理，只做严格归一等。
func lyricTitleMatch(a, b string) bool {
	na, nb := strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	return stripBracketSuffix(na) == stripBracketSuffix(nb)
}

// lyricArtistMatch 歌手比对：一方包含另一方即算（合唱列表顺序各异）。
func lyricArtistMatch(a, b string) bool {
	na := strings.ToLower(strings.TrimSpace(a))
	nb := strings.ToLower(strings.TrimSpace(b))
	if na == "" || nb == "" {
		return true
	}
	return strings.Contains(na, nb) || strings.Contains(nb, na)
}

// stripBracketSuffix 去掉「(Remastered)」「（Live）」这类尾巴。
func stripBracketSuffix(s string) string {
	for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}, {"[", "]"}, {"【", "】"}} {
		if i := strings.Index(s, pair[0]); i > 0 {
			if j := strings.LastIndex(s, pair[1]); j > i {
				s = s[:i] + s[j+len(pair[1]):]
			}
		}
	}
	return strings.TrimSpace(s)
}
