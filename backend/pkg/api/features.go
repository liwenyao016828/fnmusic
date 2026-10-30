package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/push"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/tags"
	"fn-lx-player/pkg/tidy"
)

// ── 整理（刮削） ──

// TidyRequest 整理请求
type TidyRequest struct {
	Items []tidy.Item `json:"items"`
	// Paths 简化用法：只给路径，后端自行读取现有标签
	Paths   []string     `json:"paths"`
	Options tidy.Options `json:"options"`
}

// HandleTidy 批量整理：补歌词/封面/元数据。
//
// POST /api/tidy
// body: {"paths":["/vol1/Music/a.flac"], "items":[{"path":"...","lyric":"...","cover_url":"..."}], "options":{...}}
func (s *Server) HandleTidy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req TidyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	items := req.Items
	// 支持只给路径的简化用法
	for _, p := range req.Paths {
		items = append(items, tidy.Item{Path: p})
	}
	if len(items) == 0 {
		errJSON(w, http.StatusBadRequest, "请提供 items 或 paths")
		return
	}
	if len(items) > 500 {
		errJSON(w, http.StatusBadRequest, "单次最多整理 500 首")
		return
	}

	// 路径安全：所有路径必须位于允许的曲库根目录内
	for i := range items {
		resolved, err := resolveLibraryPath(items[i].Path)
		if err != nil {
			errJSON(w, http.StatusForbidden, "路径不被允许: "+items[i].Path+"（"+err.Error()+"）")
			return
		}
		items[i].Path = resolved
	}

	opts := req.Options
	// 全部为 false 时视为「未指定」，套用默认值
	if !opts.EmbedLyric && !opts.WriteLRC && !opts.EmbedCover &&
		!opts.WriteCoverFile && !opts.WriteMetadata && !opts.OverwriteLyric && !opts.OverwriteCover {
		opts = tidy.DefaultOptions()
	}

	// 封面：`tidy.TidyOne` **不下载封面**（要求调用方给字节），而 `Item.CoverBytes` 是
	// `json:"-"` —— HTTP 调用方根本传不进来。所以「谁下载」这件事只能在这里兜住，
	// 否则传了 cover_url 也只会得到一句「封面需由调用方下载后以字节传入」。
	// 限并发 4：单批上限 500 首，无上限并发会同时打几百个外网请求。
	{
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for i := range items {
			if len(items[i].CoverBytes) > 0 || strings.TrimSpace(items[i].CoverURL) == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				if data, mime := fetchCoverForTidy(items[i].CoverURL); len(data) > 0 {
					items[i].CoverBytes = data
					items[i].CoverMime = mime
				}
			}(i)
		}
		wg.Wait()
	}

	res := tidy.TidyBatch(items, opts, nil)

	// 整理会改动文件（标签/歌词/封面），通知飞牛音乐重扫 ——
	// 它不会自己发现改动，不通知的话「整理了但飞牛里没变」。
	// 调度器会合并+节流，这里是立即返回的。
	if res.OK > 0 {
		fnos.ScheduleLibraryRescan()
	}

	// 若所有条目都没有可写内容，给出明确提示，而不是静默「成功但什么都没做」
	if res.OK > 0 {
		noop := 0
		for _, r := range res.Results {
			if !r.LyricEmbed && !r.LyricFile && !r.CoverEmbed && !r.CoverFile && !r.MetaWritten {
				noop++
			}
		}
		if noop == res.OK {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "没有可写入的内容",
				"data": res,
				"hint": "只传 paths 时后端只能读取现有标签、没有新内容可写。" +
					"请用 items 提供 lyric / title / artist / album；" +
					"歌词可从 GET /api/search/lyric 获取，封面地址来自搜索结果的 cover 字段",
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": res})
}

// coverSearch 放包级变量，便于离线测试替换（与 monitor_dispatcher.go 的 apiSourceResolve 同款）
var coverSearch = search.Search

// HandleCoverCandidates 搜封面候选：按「歌曲名 + 歌手」去各平台搜，收集可用的封面图。
//
// GET /api/library/cover-candidates?name=夜曲&artist=周杰伦&limit=8
//
// 用途：用户**手动**给某首歌换/补封面（「本地」页每首歌的「封面」按钮）。
// 在此之前只有「曲库补全」会补封面，而它是「搜到哪首就用哪首的图」，用户没得挑
// （用户 2026-09-22：「手动获取音源的封面或者搜索歌曲封面」）。
//
// 只做搜索与去重，**不下载图片** —— 选哪张由用户决定；选定后由
// `POST /api/nas/tags` 带 `cover` 去抓取并内嵌（那条路已有 SSRF 校验与体积上限）。
func (s *Server) HandleCoverCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	// keyword 优先：用户在界面上改了搜索词时按他改的搜（name/artist 只是组词的便捷写法）
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	if keyword == "" {
		keyword = strings.TrimSpace(name + " " + artist)
	}
	if keyword == "" {
		errJSON(w, http.StatusBadRequest, "请提供 keyword（或 name）")
		return
	}
	limit := 8
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 20 {
		limit = n
	}

	type candidate struct {
		CoverURL string `json:"cover_url"`
		Name     string `json:"name"`
		Artist   string `json:"artist"`
		Album    string `json:"album"`
		Source   string `json:"source"`
	}
	type scored struct {
		candidate
		score int
	}

	// 打分用：把「想找的那首歌」归一化，好让原曲浮到最前面。
	// ⚠️ 首版实测（2026-09-22）只按平台顺序取，结果全是网易云的**翻唱/伴奏**——
	// 搜索接口对「夜曲 周杰伦」会返回一堆同名翻唱，用户要的是原专辑封面。
	nameKey := strings.ToLower(strings.TrimSpace(match.NormTitle(name)))
	artistKey := strings.ToLower(strings.TrimSpace(match.PrimaryArtist(artist)))

	var all []scored
	seen := make(map[string]bool)

	// 四个平台都搜（**不提前 break**），按封面地址去重后再排序取前 N 张。
	for _, platform := range []string{"wy", "tx", "kg", "kw"} {
		for _, hit := range coverSearch(keyword, platform, 1, 10) {
			u := strings.TrimSpace(hit.Cover)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			sc := 0
			if nameKey != "" && strings.ToLower(strings.TrimSpace(match.NormTitle(hit.Name))) == nameKey {
				sc += 2
			}
			if artistKey != "" {
				ha := strings.ToLower(strings.TrimSpace(match.NormArtist(hit.Singer)))
				if strings.Contains(ha, artistKey) || strings.Contains(artistKey, ha) {
					sc++
				}
			}
			all = append(all, scored{candidate: candidate{
				CoverURL: u, Name: hit.Name, Artist: hit.Singer,
				Album: hit.Album, Source: hit.Source,
			}, score: sc})
		}
	}

	// 稳定排序：同分保持「平台顺序」，但**原曲（名字+歌手都对上）必然排在最前**。
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	out := make([]candidate, 0, limit)
	for _, s := range all {
		if len(out) >= limit {
			break
		}
		out = append(out, s.candidate)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"keyword":    keyword,
			"candidates": out,
			"hint": "选定后用 POST /api/nas/tags 携带 cover（图片地址）写入文件。" +
				"注意：只有 MP3 / FLAC 能内嵌封面，其它格式会返回写入失败。",
		},
	})
}

// HandleLibraryAudit 曲库体检：统计缺歌词/缺封面的曲目（只读）
//
// GET /api/library/audit?dir=/vol1/Music
func (s *Server) HandleLibraryAudit(w http.ResponseWriter, r *http.Request) {
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir（当前没有配置曲库目录）")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}

	seen, audit := library.Scan([]string{resolved}, library.ScanOptions{})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}

	sampleLimit, _ := strconv.Atoi(r.URL.Query().Get("sample"))
	if sampleLimit <= 0 {
		sampleLimit = 50
	}

	res := tidy.AuditPaths(paths, sampleLimit)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir":            resolved,
			"scan":           audit,
			"total":          res.Total,
			"no_lyric":       res.NoLyric,
			"no_cover":       res.NoCover,
			"both_missing":   res.Both,
			"unreadable":     res.Unreadable,
			"missing_sample": res.Missing,
			"hint": "把 missing_sample 或扫描结果传给 POST /api/tidy 即可批量补齐；" +
				"歌词与封面需由调用方提供（可从 /api/search/lyric 与搜索结果里的封面地址获取）",
		},
	})
}

// HandleLibraryIndex 曲库增量索引：扫描并返回相对上次的增删变化。
//
// GET /api/library/index?dir=/vol1/Music&refresh=1
//
// 关键安全门：只有枚举完整（audit.complete）时才允许把未枚举到的记录判定为已删除；
// 否则只累计 removed_skipped 并给出告警，避免挂载抖动把索引清空。
func (s *Server) HandleLibraryIndex(w http.ResponseWriter, r *http.Request) {
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir（当前没有配置曲库目录）")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}

	seen, audit := library.Scan([]string{resolved}, library.ScanOptions{
		MaxAudio: 20000,
	})

	index := s.libraryIndex()
	delta := index.Prune(seen, audit)

	// 对新增/变化的文件落索引（跳过昂贵的标签读取）
	for _, e := range delta.Added {
		index.Upsert(e)
	}
	for _, e := range delta.Changed {
		index.Upsert(e)
	}
	for _, p := range delta.Removed {
		index.Remove(p)
	}
	// 未变化的仅刷新观察时间
	for p := range seen {
		index.Touch(p)
	}

	// 增量富化：只对本目录下尚未读取标签的记录读一次标签（昂贵的 IO，仅限增量）
	enriched := enrichIndexTags(index, resolved, 500)

	if err := index.Save(); err != nil {
		errJSON(w, http.StatusInternalServerError, "保存索引失败: "+err.Error())
		return
	}

	resp := map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir":             resolved,
			"audit":           audit,
			"added":           len(delta.Added),
			"changed":         len(delta.Changed),
			"unchanged":       delta.Unchanged,
			"removed":         len(delta.Removed),
			"removed_skipped": delta.RemovedSkipped,
			"index_total":     index.Len(),
			"enriched":        enriched,
			"complete":        audit.Complete,
		},
	}
	if !audit.Complete {
		resp["warning"] = "本次枚举不完整（存在不可读目录 / 深度截断 / 数量上限），" +
			"已跳过删除判定，索引记录未被移除"
	}
	writeJSON(w, http.StatusOK, resp)
}

// enrichIndexTags 对索引中「尚未读过标签」的记录批量读取标签，
// 把 标题/歌手/专辑/风格/年份/曲序/光盘/有无歌词/有无封面 写回索引。返回成功富化的条数。
//
// 歌词/封面口径与 pkg/tidy.NeedsTidy 完全一致（内嵌 + 同名外挂文件），
// 保证「NAS 页」与「曲库管家」对缺失的统计不再矛盾。
// 读不动的记录只打时间戳、不动字段（下次不再重读，也不会误清空已有信息）。
func enrichIndexTags(index *library.Index, dir string, limit int) int {
	candidates := index.Unenriched(dir, limit)
	if len(candidates) == 0 {
		return 0
	}
	count := 0
	for _, e := range candidates {
		meta, err := tags.Read(e.Path)
		if err != nil {
			index.MarkTagRead(e.Path)
			continue
		}
		needLyric, needCover, terr := tidy.NeedsTidy(e.Path)
		if terr != nil {
			// NeedsTidy 失败时退回仅用内嵌结果
			needLyric = strings.TrimSpace(meta.Lyric) == ""
			needCover = len(meta.Cover) == 0
		}
		index.MarkEnriched(e.Path, library.Enrich{
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
		count++
	}
	if count > 0 {
		index.Save()
	}
	return count
}

// HandleLibraryIndexStats 读取某目录的索引概况（用于曲库管家首屏展示上次整理结果）。
//
// GET /api/library/index/stats?dir=/vol1/Music
func (s *Server) HandleLibraryIndexStats(w http.ResponseWriter, r *http.Request) {
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir（当前没有配置曲库目录）")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}
	index := s.libraryIndex()
	st := index.StatsForDir(resolved)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": st,
	})
}

// HandleLibraryGaps 统计某目录下各字段的缺口（曲库管家「AI 补全」子页的第一步）。
//
// GET /api/library/gaps?dir=/vol1/Music
//
// 只读索引，不开文件，所以秒回。代价是**索引可能滞后**：
// 因此把「读过标签的条数 / 还没读过的条数」一起返回，界面才能说清楚
// 「这些缺口是真缺，还是只是没扫描到」。
func (s *Server) HandleLibraryGaps(w http.ResponseWriter, r *http.Request) {
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir（当前没有配置曲库目录）")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}
	gaps := s.libraryIndex().FieldGaps(resolved)
	// unread 只算「能写标签但还没读过」的条数：写不了标签的格式单列在 unsupported 里，
	// 否则界面会劝用户去「增量扫描」那些扫多少遍也读不出字段的文件。
	unread := gaps.Total - gaps.Read - gaps.Unsupported
	if unread < 0 {
		unread = 0
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir":    resolved,
			"unread": unread,
			"gaps":   gaps,
			// 补全到底能写哪些格式：AI 调用方据此回答「这些文件为什么补不上」
			"writable_formats": audioext.WritableFormats(),
		},
	})
}

// HandleLibraryIndexList 查看索引内容（支持按目录过滤 + 分页）。
//
// GET /api/library/index/list?dir=/vol1/Music&offset=0&limit=100
// 不传 dir 时返回全部索引（兼容旧调用）。
func (s *Server) HandleLibraryIndexList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	index := s.libraryIndex()

	dirParam := strings.TrimSpace(r.URL.Query().Get("dir"))
	// 关键词 / 字段筛选（2026-10-28，「歌曲修复」列表用）：
	//
	//	q=夜空           标题/歌手/专辑/路径 任一包含即命中（不区分大小写）
	//	filter=no_lyric  只要没有歌词的
	//	filter=no_cover  只要没有封面的
	//
	// 为什么放服务端：前端「本页内过滤」会让 total 与页数对不上 —— 第 3 页可能
	// 整页都筛空，用户以为曲库没了。这里筛完再分页，total 就是筛完的总数。
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	var items []library.Entry
	var total int
	if dirParam != "" {
		// 按目录过滤时不再要求路径已存在（曲库可能正被外部改动）；
		// 若路径不可解析为安全根目录内，退回空结果而非 403，避免首屏报错。
		if resolved, err := resolveLibraryPath(dirParam); err == nil {
			items, total = index.ListDir(resolved, offset, limit)
		} else {
			items, total = []library.Entry{}, 0
		}
	} else {
		all := index.List()
		total = len(all)
		if offset > total {
			items = []library.Entry{}
		} else {
			end := offset + limit
			if end > total {
				end = total
			}
			items = all[offset:end]
		}
	}

	// 有筛选就从「整个目录」重新取一遍再过滤 —— 单页过滤没有意义。
	if q != "" || filter != "" {
		var pool []library.Entry
		if dirParam != "" {
			if resolved, err := resolveLibraryPath(dirParam); err == nil {
				pool, _ = index.ListDir(resolved, 0, 0) // limit=0 由 ListDir 当「全部」
			}
		} else {
			pool = index.List()
		}
		filtered := make([]library.Entry, 0, len(pool))
		for _, e := range pool {
			switch filter {
			case "no_lyric":
				if e.HasLyric {
					continue
				}
			case "no_cover":
				if e.HasCover {
					continue
				}
			}
			if q != "" && !entryMatches(e, q) {
				continue
			}
			filtered = append(filtered, e)
		}
		total = len(filtered)
		if offset >= total {
			items = []library.Entry{}
		} else {
			end := offset + limit
			if end > total {
				end = total
			}
			items = filtered[offset:end]
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"items": items, "total": total, "limit": limit, "offset": offset,
		},
	})
}

// entryMatches 关键词命中判断：标题 / 歌手 / 专辑 / 路径任一包含即算。
// 路径也参与是有用的 —— 用户常按「专辑文件夹名」找歌。
func entryMatches(e library.Entry, lowerQ string) bool {
	for _, v := range []string{e.Title, e.Artist, e.Album, e.Path} {
		if v != "" && strings.Contains(strings.ToLower(v), lowerQ) {
			return true
		}
	}
	return false
}

// ── 去重 ──

// HandleDuplicates 查找重复曲目
//
// GET /api/duplicates?dir=/vol1/Music&name=1
func (s *Server) HandleDuplicates(w http.ResponseWriter, r *http.Request) {
	dir := s.libraryDirOrDefault(r.URL.Query().Get("dir"))
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "请提供 dir（当前没有配置曲库目录）")
		return
	}
	resolved, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}

	seen, audit := library.Scan([]string{resolved}, library.ScanOptions{MaxAudio: 20000})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}

	opts := tidy.DefaultDedupeOptions()
	opts.IncludeNameDuplicate = r.URL.Query().Get("name") != "0"

	groups, warnings := tidy.FindDuplicates(paths, opts)

	var reclaimable int64
	for _, g := range groups {
		for _, victim := range g.Delete {
			for _, p := range paths {
				if p == victim {
					if fi, err := statSize(p); err == nil {
						reclaimable += fi
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"dir":         resolved,
			"groups":      groups,
			"group_count": len(groups),
			"scan":        audit,
			"warnings":    warnings,
			"hint": "执行去重请 POST /api/duplicates/resolve，" +
				"默认 mode=trash 移入回收站（可恢复），mode=delete 才真正删除",
		},
	})
}

// HandleDuplicatesResolve 执行去重
//
// POST /api/duplicates/resolve
// body: {"dir":"/vol1/Music","mode":"trash","dry_run":true}
func (s *Server) HandleDuplicatesResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Dir    string `json:"dir"`
		Mode   string `json:"mode"`
		DryRun bool   `json:"dry_run"`
		// Groups 可选：直接传入待处理的组（供 AI 精确控制）
		Groups []tidy.DupGroup `json:"groups"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	groups := req.Groups
	if len(groups) == 0 {
		if req.Dir == "" {
			errJSON(w, http.StatusBadRequest, "请提供 dir 或 groups")
			return
		}
		resolved, err := resolveLibraryPath(req.Dir)
		if err != nil {
			errJSON(w, http.StatusForbidden, err.Error())
			return
		}
		seen, _ := library.Scan([]string{resolved}, library.ScanOptions{MaxAudio: 20000})
		paths := make([]string, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
		groups, _ = tidy.FindDuplicates(paths, tidy.DefaultDedupeOptions())
		req.Dir = resolved
	}

	// 路径安全：所有待删除文件都必须在允许根目录内
	trashDir := ""
	for _, g := range groups {
		for _, victim := range g.Delete {
			resolved, err := resolveLibraryPath(victim)
			if err != nil {
				errJSON(w, http.StatusForbidden, "拒绝处理越权路径: "+victim)
				return
			}
			if trashDir == "" {
				trashDir = joinTrash(resolved)
			}
		}
	}

	res := tidy.ResolveDuplicates(groups, tidy.ResolveOptions{
		Mode:     req.Mode,
		TrashDir: trashDir,
		DryRun:   req.DryRun,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": res})
}

// ── AI ──

// RecommendCandidates 是拦截层「每日推荐的大模型层」的注入口
// （见 pkg/intercept/vdaily_llm.go）。
//
// 为什么要有这个薄薄的方法，而不是让拦截层直接拿 `*ai.Client`：AI 客户端与它的
// 配置、用量记账都归这个 Server 管，拦截层不该知道它们长什么样。它只要
// 「给我一组种子、还我一组候选」。
//
// **未配置 AI 时返回 nil**（`ai.Client.Recommend` 的静默降级），调用方据此把这一层
// 整个跳过 —— 每日推荐照常交在线 + 本地两层的结果，不会变成错误、空白或少歌。
//
// ⚠️ 它是**并发安全**的：拦截至在后台 goroutine 里调它（见 startLLMCandidates），
// 而 `ai.Client` 自己的配置读写有锁。
func (s *Server) RecommendCandidates(ctx context.Context, req ai.RecommendRequest) []ai.RecommendCandidate {
	if s == nil || s.aiClient == nil {
		return nil
	}
	return s.aiClient.Recommend(ctx, req)
}

// HandleAIConfig AI 配置读写
//
// GET  /api/ai/config  -> 密钥已脱敏
// POST /api/ai/config  -> 更新（密钥传空或含 **** 表示沿用旧值）
func (s *Server) HandleAIConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok", "data": s.aiClient.Config(),
		})

	case http.MethodPost:
		var patch ai.Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
			return
		}
		s.aiClient.UpdateConfig(patch)
		s.persistAIConfig()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok", "data": s.aiClient.Config(),
		})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// HandleAIModels GET /api/ai/models —— 列出网关可用模型（AI 助手的模型下拉）
//
// 失败也回 200 + 空列表 + error 原因：这是「下拉列表的填充接口」，
// 前端要能区分「没启用 / 没配 Key / 网关连不上」并给出对应提示，
// 拿一个裸 HTTP 错误反而没法在界面上说话。
func (s *Server) HandleAIModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.aiClient.Models(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200,
			"data": map[string]interface{}{"models": []string{}, "error": err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"models": models},
	})
}

// HandleAIUsage AI 用量统计
//
// GET /api/ai/usage?recent=20
func (s *Server) HandleAIUsage(w http.ResponseWriter, r *http.Request) {
	recent, _ := strconv.Atoi(r.URL.Query().Get("recent"))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": s.aiClient.Usage(recent),
	})
}

// HandleAIResetUsage 清空用量统计
func (s *Server) HandleAIResetUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	s.aiClient.ResetUsage()
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "已清空用量统计"})
}

// HandleAITest 连通性测试
func (s *Server) HandleAITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	reply, err := s.aiClient.Test(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"code": 502, "message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"reply": reply, "ok": true},
	})
}

// HandleAIComplete 让 AI 对给定内容做处理（用途：乱文件名解析、歌曲信息补全等）
//
// POST /api/ai/complete
// body: {"kind":"name_parse","filename":"...","prompt":"...","content":"..."}
func (s *Server) HandleAIComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Kind     string `json:"kind"`
		Filename string `json:"filename"`
		Prompt   string `json:"prompt"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// 文件名语义解析（结构化输出）
	if req.Kind == "name_parse" || (req.Kind == "" && req.Filename != "") {
		if strings.TrimSpace(req.Filename) == "" {
			errJSON(w, http.StatusBadRequest, "请提供 filename")
			return
		}
		// 2026-10-28：把「用户规则会怎么拆」一并发给界面，和 AI 的结论并排显示。
		// 用户问的「文件名解析和命名解析是不是重复了」本质就在这儿 —— 两个卡片各算各的，
		// 结论不同时用户根本不知道该信谁。现在同一个文件名，规则答案和 AI 答案直接摆一起。
		ruleRes := s.nameRules.Parse(filepath.Base(strings.TrimSpace(req.Filename)))
		ruleInfo := map[string]interface{}{
			"title":  strings.TrimSpace(ruleRes.Title),
			"artist": strings.TrimSpace(ruleRes.Artist),
			"suspect": ruleRes.Suspect,
			"from_rule": s.nameRules.HasMatch(filepath.Base(strings.TrimSpace(req.Filename))),
		}

		res := s.aiClient.ParseName(ctx, req.Filename)
		out := map[string]interface{}{
			"artist":    res.Artist,
			"title":     res.Title,
			"album":    res.Album,
			"style":    res.Style,
			"by_rules": ruleInfo,
		}
		if res.Artist == "" && res.Title == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"code": 200, "message": "未启用 AI 或解析无结果（未配置时属正常降级）",
				"data": out,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": out})
		return
	}

	// 自由文本生成（用户提示词可覆盖内容，但保留输出契约）
	system := ai.BuildSystemPrompt("你是音乐库整理助手。", "")
	user := ai.WithUserPrompt(req.Prompt, req.Content)
	if strings.TrimSpace(user) == "" {
		errJSON(w, http.StatusBadRequest, "请提供 content 或 prompt")
		return
	}

	out, usage, err := s.aiClient.Chat(ctx, "complete", system, user)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"code": 502, "message": err.Error()})
		return
	}
	if out == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "未启用 AI（未配置时属正常降级）", "data": map[string]interface{}{"text": ""},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"text": out, "usage": usage},
	})
}

// ── 辅助 ──

// resolveLibraryPath 把路径限制在允许的曲库根目录内
func resolveLibraryPath(p string) (string, error) {
	return resolveAllowedPath(p)
}

func statSize(p string) (int64, error) {
	fi, err := statFile(p)
	if err != nil {
		return 0, err
	}
	return fi, nil
}

// fetchCoverForTidy 下载封面（供整理流程使用）。
//
// 实现已下沉到 security.FetchImage —— 那里带 SSRF 防护与体积/魔数校验，
// 而且 pkg/complete 也要用同一套（两边各写一份迟早会漂）。
func fetchCoverForTidy(rawURL string) ([]byte, string) {
	return security.FetchImage(rawURL, "https://music.163.com/")
}

var _ = io.Discard

// resolveAllowedPath 复用 NAS 路径守卫，把路径限制在允许的曲库根目录内
func resolveAllowedPath(p string) (string, error) {
	return nas.ResolveSafePath(p)
}

// statFile 返回文件大小
func statFile(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// joinTrash 计算回收站目录（放在曲库根目录下的 .tidy_trash）
//
// ⚠️ 2026-10-29 修的一个真实 bug（用户报「本地曲库删除歌曲…实际没有删除」）：
// 以前用 `strings.HasPrefix(samplePath, root)` 判归属 —— 前缀匹配会把
// `/home/liwenyao/Music/a.mp3` 判成落在根 `/home` 下，于是回收站被建到
// `/home/.tidy_trash`（普通用户没权限写），挪动全部失败、文件一个都没删掉。
// 现在按**路径分段**比较（与 nas 包的 isWithinRoot 同一规则），并取最长匹配的根，
// 保证「最贴近的那个盘/共享目录」说了算；根自己就是路径时取它的父目录做回收站。
func joinTrash(samplePath string) string {
	roots := nas.AllowedRoots()
	best := ""
	for _, root := range roots {
		if withinPath(root, samplePath) && len(root) > len(best) {
			best = root
		}
	}
	if best == "" || filepath.Clean(best) == filepath.Clean(samplePath) {
		return filepath.Join(filepath.Dir(samplePath), ".tidy_trash")
	}
	return filepath.Join(best, ".tidy_trash")
}

// withinPath 按路径分段判断 target 是否等于 root 或位于其下。
// 直接 HasPrefix 会让 /vol10 命中 /vol1、/home/x 命中 /home —— 那是删歌落到错误回收站的根因。
func withinPath(root, target string) bool {
	root, target = filepath.Clean(root), filepath.Clean(target)
	if root == target {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(target, root)
}

// ── AI 配置持久化 ──

// aiConfigPath AI 配置文件路径（存 API Key，权限 0600）
func (s *Server) aiConfigPath() string {
	return filepath.Join(s.cfgMgr.DataDir(), "ai_config.json")
}

// persistAIConfig 保存 AI 配置（含密钥，因此限制文件权限）
func (s *Server) persistAIConfig() {
	s.aiCfgMu.Lock()
	defer s.aiCfgMu.Unlock()

	s.aiClient.Config() // 确保配置已就绪
	cfg := s.aiClient.RawConfig()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	tmp := s.aiConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.aiConfigPath())
}

// loadAIConfig 启动时加载 AI 配置
func (s *Server) loadAIConfig() {
	data, err := os.ReadFile(s.aiConfigPath())
	if err != nil {
		return
	}
	var cfg ai.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}
	s.aiClient.UpdateConfig(cfg)
}

// libraryIndex 返回曲库索引实例
func (s *Server) libraryIndex() *library.Index {
	return s.libIndex
}

// libraryDirOrDefault 把「可选 dir」解析成**实际要用的曲库目录**。
//
// 为什么需要它：曲库类接口（stats / gaps / audit / index / duplicates / lyric gaps…）
// 原来一律要求调用方显式传 dir，没传就 400「请提供 dir」。但应用自己**已经**知道
// 曲库在哪（配置里的 download_dir / default_nas_dir，见 config.LibraryDir）——
// 于是每个调用方都得先猜一次目录，猜错就是 403、不猜就是 400，连内置 AI 助手
// 的 library_stats 能力都被卡成「执行失败（HTTP 400）：请提供 dir」。
//
// 现在的口径：
//   - 传了 dir → 原样返回（调用方明确指定优先，行为不变）；
//   - 没传 → 回退到配置里生效的曲库目录；
//   - 只有配置也空（理论上不会，LibraryDir 有 /vol1/Music 兜底）才回 ""，
//     由调用方决定报什么错。
//
// 注意：这里只**补默认值**，不加宽权限 —— resolveLibraryPath 的路径守卫照旧。
func (s *Server) libraryDirOrDefault(raw string) string {
	if dir := strings.TrimSpace(raw); dir != "" {
		return dir
	}
	if s.cfgMgr == nil {
		return ""
	}
	cfg := s.cfgMgr.Get()
	return strings.TrimSpace(cfg.LibraryDir())
}

// localTrackSnapshot 把曲库索引转成 push 包认得的一串 LocalTrack（给「飞牛歌单
// 同步」做本地兜底匹配用）。
//
// 为什么要在这里转而不是让 push 包直接吃 library.Index：push 包是被 monitor /
// runner / api 三层共用的底层，让它依赖 library 会把「推歌单」和「扫曲库」两个
// 东西绑死（library 那边还在演进）。中间隔一层纯数据，两边都能独立测。
//
// 返回 nil 表示索引不可用 —— 调用方据此退回「只看飞牛曲库」的老行为，
// 绝不能因为拿不到本地索引就把整次推送判失败。
func (s *Server) localTrackSnapshot() func() []push.LocalTrack {
	if s.libIndex == nil {
		return nil
	}
	idx := s.libIndex
	return func() []push.LocalTrack {
		entries := idx.List()
		out := make([]push.LocalTrack, 0, len(entries))
		for _, e := range entries {
			name := strings.TrimSpace(e.Title)
			if name == "" {
				// 没读到标签的条目用文件名兜底：曲库里相当一部分文件靠文件名认，
				// 少了这一步它们永远匹配不上（用户会说「我明明有这首歌」）。
				base := filepath.Base(e.Path)
				name = strings.TrimSuffix(base, filepath.Ext(base))
			}
			out = append(out, push.LocalTrack{
				Path:   e.Path,
				Name:   name,
				Artist: e.Artist,
				Album:  e.Album,
				Format: e.Format,
			})
		}
		return out
	}
}
