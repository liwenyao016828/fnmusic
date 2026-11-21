package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/fslock"
	"fn-lx-player/pkg/organize"
)

// ── 文件夹整理（v2.32.0）─────────────────────────────────────────────────
//
// 用户 2026-11-20 的口径：目录层级按「歌手 / 专辑 / 曲目」组织，同一曲目的
// 音频（.flac）/ 图片（.jpg）/ 歌词（.lrc）保持相同文件名、放在同一专辑目录下；
// **结构样式（目录层级、命名格式）由用户自己改**。
//
// 五个动作分开，是因为「移动文件」是不可逆的重活：
//
//	GET  /api/organize/presets  内置结构样式 + 生效中的模板 + 上次整理能否撤销
//	POST /api/organize/plan     预演：算出每首歌会去哪、哪些会撞车（**只读，不动盘**）
//	POST /api/organize/apply    执行：按「重新算一遍」的方案搬文件，并留撤销记录
//	POST /api/organize/undo     撤销最近一次整理（逆序搬回原位）
//	GET  /api/organize/journal  有没有可撤销的记录（界面据此决定撤销按钮亮不亮）
//
// apply 不接 plan 的返回结果、而是自己重算一遍：预演到执行之间用户可能刚下了新歌，
// 拿旧方案执行会漏搬；重算本身是幂等的（已经在位的会算成 same，不会被再搬一次）。

// organizeRequest 是 plan / apply 共用的请求体。
//
// 三个模板/选项字段都是指针：nil = 「这次没意见，按配置走」，
// 非 nil = 「就按这个算」（用户在预览框里改了模板、还没点保存也想先看效果）。
type organizeRequest struct {
	// Dir 整理根目录；空 = 用曲库目录（设置里那个落盘/曲库目录）。
	Dir string `json:"dir"`
	// DirTemplate 目录模板（相对根目录，用 "/" 分层）；空串 = 平铺。
	DirTemplate *string `json:"dir_template"`
	// FileTemplate 文件名模板（不含扩展名）。
	FileTemplate *string `json:"file_template"`
	// NumberUnknown 给没有曲序的歌按同专辑出现顺序补序号。
	NumberUnknown *bool `json:"number_unknown"`
	// MaxDepth 扫描深度，<=0 用 dirwalk 默认（12 层）。
	MaxDepth int `json:"max_depth"`
}

// organizeTemplateOf 合并「请求里带的模板」与「配置里存的模板」。
//
// 起点是**默认样式**而不是空模板：配置里这两个字段为 nil 表示「没设置过」，
// 直接拿零值会把默认的「歌手 / 专辑 / 曲目」变成平铺 —— 用户什么都没改，
// 整理完却全铺在了根目录下。
func (s *Server) organizeTemplateOf(dirTpl, fileTpl *string) organize.Template {
	t := organize.DefaultTemplate()
	cfg := s.cfgMgr.Get()
	if cfg.OrganizeDirTemplate != nil {
		t.Dir = *cfg.OrganizeDirTemplate
	}
	if cfg.OrganizeFileTemplate != nil {
		t.File = *cfg.OrganizeFileTemplate
	}
	if dirTpl != nil {
		t.Dir = *dirTpl
	}
	if fileTpl != nil {
		t.File = *fileTpl
	}
	return organize.NormalizeTemplate(t)
}

// organizeLookup 把曲库索引接成「按路径问元数据」的兜底查询。
//
// 没有它，标签缺失的歌会整批落到「未知歌手 / 未知专辑」—— 而曲库索引里
// 通常已经有上一次扫描读到的歌手/专辑/曲序（见 pkg/library 的 Entry）。
// 索引没起来时返回 nil = 只靠标签与文件名（组织器本身接受 Lookup 为空）。
func (s *Server) organizeLookup() func(path string) (organize.Meta, bool) {
	index := s.libraryIndex()
	if index == nil {
		return nil
	}
	return func(path string) (organize.Meta, bool) {
		e, ok := index.Get(path)
		if !ok {
			return organize.Meta{}, false
		}
		return organize.Meta{
			Title:  e.Title,
			Artist: e.Artist,
			Album:  e.Album,
			Genre:  e.Genre,
			Track:  e.Track,
			Disc:   e.Disc,
			Year:   e.Year,
			Source: "library",
		}, true
	}
}

// organizeOptions 把请求 + 配置拼成组织器参数。
func (s *Server) organizeOptions(req organizeRequest) (organize.Options, string, error) {
	dir := s.libraryDirOrDefault(req.Dir)
	if strings.TrimSpace(dir) == "" {
		return organize.Options{}, "", errOrganizeNoDir
	}
	root, err := resolveLibraryPath(dir)
	if err != nil {
		return organize.Options{}, "", err
	}
	numberUnknown := false
	if req.NumberUnknown != nil {
		numberUnknown = *req.NumberUnknown
	} else if cfg := s.cfgMgr.Get(); cfg.OrganizeNumberUnknown != nil {
		numberUnknown = *cfg.OrganizeNumberUnknown
	}
	return organize.Options{
		Root:          root,
		Template:      s.organizeTemplateOf(req.DirTemplate, req.FileTemplate),
		Lookup:        s.organizeLookup(),
		MaxDepth:      req.MaxDepth,
		NumberUnknown: numberUnknown,
	}, root, nil
}

var errOrganizeNoDir = &organizeError{"还没有被授权访问任何目录，请先在「应用设置 → 授权目录」里授权一个音乐目录"}

// organizeError 让上面那个错误能带上一句给人看的话，又不引入 fmt 依赖。
type organizeError struct{ msg string }

func (e *organizeError) Error() string { return e.msg }

// decodeOrganizeRequest 读请求体（plan / apply / undo 共用）。
func decodeOrganizeRequest(w http.ResponseWriter, r *http.Request) (organizeRequest, bool) {
	var req organizeRequest
	// 请求体很小（几个模板串），1MB 足够，别给解析器留大内存的口子。
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return req, false
	}
	return req, true
}

// organizePresetView 是「内置样式 + 一条成品示例路径」。
//
// 用户 2026-11-20 原话：「用户看不懂这些 {artist}/{album} 等内容」——
// 所以每个样式旁边必须摆一条**拼好的**路径（周杰伦/范特西/01 双截棍.flac），
// 成品路径由 pkg/organize 的渲染器算，前端不自己拼。
type organizePresetView struct {
	organize.Preset
	// Example 相对整理根目录的成品路径（含扩展名），如「周杰伦/范特西/01 双截棍.flac」
	Example string `json:"example"`
	// DirExample 只有目录那一段（平铺时为空串）
	DirExample string `json:"dir_example"`
	// FileExample 只有文件名（不含扩展名）
	FileExample string `json:"file_example"`
}

// organizeExample 把模板套到示例元数据上，返回 (相对目录, 文件名, 完整相对路径)。
func organizeExample(t organize.Template) (string, string, string) {
	dirRel, base := organize.Render(t, organize.SampleMeta(), "flac")
	rel := base + ".flac"
	if dirRel != "" {
		rel = dirRel + "/" + rel
	}
	return dirRel, base, rel
}

// HandleOrganizePresets 返回内置结构样式与当前生效的模板。
//
//	GET /api/organize/presets
//
// 为什么把「内置样式」放在后端而不是前端写死：结构样式的合法性由
// pkg/organize 的模板渲染器决定（占位符、零填充写法），前端抄一份迟早会漂。
func (s *Server) HandleOrganizePresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	cfg := s.cfgMgr.Get()
	numberUnknown := false
	if cfg.OrganizeNumberUnknown != nil {
		numberUnknown = *cfg.OrganizeNumberUnknown
	}
	journal, hasJournal := organize.LoadJournal(s.cfgMgr.DataDir())

	// 每个样式配一条成品示例：界面上直接给「周杰伦/范特西/01 双截棍.flac」，
	// 而不是让用户自己翻译 {artist}/{album}。
	raw := organize.Presets()
	views := make([]organizePresetView, 0, len(raw))
	for _, p := range raw {
		t := organize.Template{Dir: p.Dir, File: p.File}
		dirRel, base, rel := organizeExample(t)
		views = append(views, organizePresetView{Preset: p, Example: rel, DirExample: dirRel, FileExample: base})
	}
	curDir, curBase, curRel := organizeExample(s.organizeTemplateOf(nil, nil))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"presets":          views,
			"default_template": organize.DefaultTemplate(),
			// 生效中的模板：请求里不带模板时按这个算（界面把它填进输入框）
			"template":       s.organizeTemplateOf(nil, nil),
			"template_view":  map[string]interface{}{"dir": curDir, "file": curBase, "example": curRel},
			"sample":         organize.SampleMeta(),
			"number_unknown": numberUnknown,
			// 占位符说明随接口下发，界面不用自己维护一份提示文案
			"placeholders": organize.Placeholders(),
			"sidecar_exts": organize.SidecarExts(),
			"dir":          s.libraryDirOrDefault(""),
			"journal":      organizeJournalSummary(journal, hasJournal, len(organize.LoadJournals(s.cfgMgr.DataDir()))),
		},
	})
}

// HandleOrganizeSample 按给定模板渲染一条示例路径（不扫盘、不动盘）。
//
//	POST /api/organize/sample {"dir_template":"{artist}/{album}","file_template":"{track:02d} {title}"}
//
// 用户改模板时界面要立刻看到「这样改完长什么样」。渲染规则只有后端一份，
// 所以前端每敲一下就带个防抖来问这里，而不是自己实现一套占位符替换
// （那套实现一旦和后端不同步，界面就会预告一个执行时根本不发生的结果）。
func (s *Server) HandleOrganizeSample(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	req, ok := decodeOrganizeRequest(w, r)
	if !ok {
		return
	}
	t := s.organizeTemplateOf(req.DirTemplate, req.FileTemplate)
	dirRel, base, rel := organizeExample(t)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"template": t,
			"dir":      dirRel,
			"file":     base,
			"ext":      "flac",
			"example":  rel,
			"sample":   organize.SampleMeta(),
			"sidecars": organize.SidecarExts(),
		},
	})
}

// HandleOrganizePlan 预演整理方案（只读）。
//
//	POST /api/organize/plan {"dir":..., "dir_template":..., "file_template":...}
func (s *Server) HandleOrganizePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	req, ok := decodeOrganizeRequest(w, r)
	if !ok {
		return
	}
	opts, root, err := s.organizeOptions(req)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := organize.PlanOf(opts)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "扫描整理方案失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": organizePlanPayload(plan, root),
	})
}

// HandleOrganizeApply 执行整理（真实移动文件）。
//
//	POST /api/organize/apply {"dir":..., "dir_template":..., "file_template":...}
//
// 执行完写一份撤销记录（<数据目录>/organize_journal.json），并触发一次曲库重扫 ——
// 文件位置变了，索引不重扫就会一直指着旧路径（下载/删歌之后也是这么做的，
// 见 nas_delete.go 的 fnos.ScheduleLibraryRescan）。
//
// 这里用的是 ScheduleLibraryRescanNow 而不是普通的 Schedule：整理是一次搬几百个
// 文件的手动操作，普通调度要等 10 分钟最小间隔，用户在飞牛音乐里会先看到
// 「整理完歌全没了」。手动操作不可能高频，这一次跳过间隔是安全的。
func (s *Server) HandleOrganizeApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	req, ok := decodeOrganizeRequest(w, r)
	if !ok {
		return
	}
	opts, root, err := s.organizeOptions(req)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	// 排他闸门：整理期间不许下载往曲库里写文件（下载器拿这把闸门的共享端）。
	// 用「等 3 秒就报忙」而不是死等：界面卡住比明确说一句「有下载在跑」更糟。
	releaseGate, gateErr := fslock.TryExclusive(3 * time.Second)
	if gateErr != nil {
		errJSON(w, http.StatusConflict, "有下载任务正在进行，等它下完再整理（也可以先把下载暂停）")
		return
	}
	defer releaseGate()

	// 重新算一遍：客户端看到的方案可能已经过期（期间又下了新歌 / 手工挪过文件）。
	plan, err := organize.PlanOf(opts)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "扫描整理方案失败: "+err.Error())
		return
	}
	if plan.Stats.Move == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "没有需要移动的文件",
			"data": map[string]interface{}{
				"plan":   organizePlanPayload(plan, root),
				"result": nil,
			},
		})
		return
	}
	// 执行前先落一份「整批计划」的撤销记录：Apply 是逐文件搬的，中途崩溃
	// （断电 / 进程被杀 / 某首歌复制校验失败后用户强退）时，正式记录还没写，
	// 用户就失去了撤回依据。先写计划版，成功后再用真实结果覆盖。
	if prepared := organize.PlannedJournal(plan); len(prepared.Moves) > 0 {
		if err := organize.SaveJournal(s.cfgMgr.DataDir(), prepared); err != nil {
			errJSON(w, http.StatusInternalServerError, "写入撤销记录失败，为避免无法撤回已中止: "+err.Error())
			return
		}
	}

	res, err := organize.Apply(plan)
	if err != nil && res.Moved == 0 && res.Sidecars == 0 {
		errJSON(w, http.StatusInternalServerError, "整理失败: "+err.Error())
		return
	}

	// 撤销记录：只在真的搬动了东西时写（空记录会把上一次的撤销点冲掉）。
	journalSaved := false
	if len(res.Journal) > 0 {
		j := organize.Journal{
			CreatedAt: time.Now(),
			Root:      plan.Root,
			Template:  plan.Template,
			Moves:     res.Journal,
			Moved:     res.Moved,
		}
		// 覆盖执行前写的那份「计划版」（同一批，不追加）。
		if err := organize.ReplaceLastJournal(s.cfgMgr.DataDir(), j); err != nil {
			res.Errors = append(res.Errors, "撤销记录没写成功（本次整理无法撤销）: "+err.Error())
		} else {
			journalSaved = true
		}
	} else {
		// 一个文件都没搬成：把执行前写的计划版丢掉，别留一条撤不动的空记录。
		if err := organize.DropLastJournal(s.cfgMgr.DataDir()); err != nil {
			res.Errors = append(res.Errors, "撤销记录没清掉: "+err.Error())
		}
	}

	// 索引跟着搬家：先把旧路径从索引里摘掉，再让飞牛重扫补新路径。
	// 不摘的话，旧路径要等一次「枚举完整」的 prune 才会消失，中间这段时间
	// 曲库里会同时出现新旧两份记录。
	s.migrateOrganizeMoved(plan)
	// 再顺手把「盘上已不存在」的条目清掉：整理/撤销之外，用户还会在文件管理器里
	// 手工改名、删歌，这些路径靠 Prune 收不干净（扫描有 2 万首上限，超了
	// audit.Complete 就是 false，Prune 会安全地什么都不删）。
	pruned := s.reconcileOrganizeRoot(plan.Root)

	if res.Moved > 0 || res.Sidecars > 0 {
		fnos.ScheduleLibraryRescanNow()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"plan":          organizePlanPayload(plan, root),
			"result":        res,
			"journal_saved": journalSaved,
			"index_pruned":  pruned,
		},
	})
}

// reconcileOrganizeRoot 把索引里「在这个根目录之下、但盘上已经没有」的条目摘掉。
//
// 为什么不能只靠 library.Index.Prune：Prune 要求一次「枚举完整」的扫描
// （audit.Complete），而扫描有 2 万首上限，超过就是 false —— 于是被手工挪走 /
// 删掉的歌会一直留在索引里，界面上是打不开的条目（审计 M10）。
// 整理完顺手 stat 一遍这个根目录下的条目：几千次 stat 很便宜，换来索引与盘面一致。
func (s *Server) reconcileOrganizeRoot(root string) int {
	idx := s.libraryIndex()
	if idx == nil || strings.TrimSpace(root) == "" {
		return 0
	}
	prefix := filepath.ToSlash(filepath.Clean(root)) + "/"
	removed := 0
	for _, e := range idx.List() {
		if !strings.HasPrefix(filepath.ToSlash(e.Path), prefix) {
			continue
		}
		if _, err := os.Stat(e.Path); err != nil {
			idx.Remove(e.Path)
			removed++
		}
	}
	if removed > 0 {
		_ = idx.Save()
	}
	return removed
}

// HandleOrganizeUndo 撤销最近一次整理。
//
//	POST /api/organize/undo
//
// 逆序搬回：后搬的先回，避免两个文件互换位置时路径被占住。
func (s *Server) HandleOrganizeUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	// 取出最近一批记录（并把它从撤销链上摘掉）：撤销是「退回整理之前」，
	// 不是可以反复按的回放键；链上更早的批次留着，用户可以一轮一轮往回撤。
	j, ok := organize.PopJournal(s.cfgMgr.DataDir())
	if !ok || len(j.Moves) == 0 {
		errJSON(w, http.StatusBadRequest, "没有可撤销的整理记录")
		return
	}
	releaseGate, gateErr := fslock.TryExclusive(3 * time.Second)
	if gateErr != nil {
		errJSON(w, http.StatusConflict, "有下载任务正在进行，等它下完再撤销")
		return
	}
	defer releaseGate()

	res, err := organize.Undo(j.Moves, j.Root)
	if err != nil && res.Moved == 0 {
		errJSON(w, http.StatusInternalServerError, "撤销失败: "+err.Error())
		return
	}
	// 路径又变回来了，索引跟着回迁 —— 否则撤销之后曲库索引里挂的还是新路径，
	// 界面上的「已整理」条目全变成失效路径。
	s.migrateOrganizeMoves(j.Moves)
	pruned := s.reconcileOrganizeRoot(j.Root)
	if res.Moved > 0 || res.Sidecars > 0 {
		fnos.ScheduleLibraryRescanNow()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"result":       res,
			"root":         j.Root,
			"index_pruned": pruned,
		},
	})
}

// HandleOrganizeTemplate 记住这次用的样式（下次打开还是它）。
//
//	POST /api/organize/template {"dir_template":..., "file_template":..., "number_unknown":...}
//
// 为什么不走 POST /api/config：那边的合并规则是「空串 = 没传」（mergeConfigPatch），
// 而**空目录模板是合法取值**（平铺样式就是把歌收成一层）。走那边保存「平铺」会静默
// 存不进去，用户下次打开又变回「歌手 / 专辑 / 曲目」—— 这里直接把指针写进配置，
// 空串与非空串一视同仁。
func (s *Server) HandleOrganizeTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	req, ok := decodeOrganizeRequest(w, r)
	if !ok {
		return
	}
	if req.DirTemplate == nil && req.FileTemplate == nil && req.NumberUnknown == nil {
		errJSON(w, http.StatusBadRequest, "没有需要保存的样式")
		return
	}
	next := config.AppConfig{
		OrganizeDirTemplate:   req.DirTemplate,
		OrganizeFileTemplate:  req.FileTemplate,
		OrganizeNumberUnknown: req.NumberUnknown,
	}
	// 先规整成组织器认可的样子再存：存进去的就是算方案时用的那一份。
	tpl := organize.NormalizeTemplate(s.organizeTemplateOf(req.DirTemplate, req.FileTemplate))
	if req.DirTemplate != nil {
		next.OrganizeDirTemplate = &tpl.Dir
	}
	if req.FileTemplate != nil {
		next.OrganizeFileTemplate = &tpl.File
	}
	if err := s.cfgMgr.Update(next); err != nil {
		errJSON(w, http.StatusInternalServerError, "保存样式失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"template": tpl},
	})
}

// HandleOrganizeJournal 查有没有可撤销的整理记录。
//
//	GET /api/organize/journal
func (s *Server) HandleOrganizeJournal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	j, ok := organize.LoadJournal(s.cfgMgr.DataDir())
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": organizeJournalSummary(j, ok && len(j.Moves) > 0, len(organize.LoadJournals(s.cfgMgr.DataDir()))),
	})
}

// migrateOrganizeMoved 把搬走的音频在曲库索引里的路径改到新位置。
//
// 为什么不是「删掉算了」：删掉之后这条记录里已经读过的标签（歌手/专辑/曲序）
// 就全丢了，紧接着再预演一次只能靠文件名猜 —— 而整理后的文件名已经不包含
// 歌手，猜出来的歌手是空的，于是这些歌又会被判成该搬进「未知歌手 / 未知专辑」。
// 改路径是零信息损失的做法。
//
// 附属文件（歌词/封面）本来就不单独进索引，它们体现在音频的 HasLyric /
// HasCover 上；三个文件是一起搬的，所以这两个标记不用动。
func (s *Server) migrateOrganizeMoved(plan organize.Plan) {
	moves := make([]organize.Move, 0, len(plan.Items))
	for _, item := range plan.Items {
		if item.Status != "move" {
			continue
		}
		moves = append(moves, item.Moves...)
	}
	s.migrateOrganizeMoves(moves)
}

// migrateOrganizeMoves 按「From 的索引记录改挂到 To」批量迁移路径。
//
// 只迁移「目标确实已经存在」的那些：整理/撤销都可能部分失败，失败的那条
// 文件还在原处，把索引改到不存在的路径上等于把这行记录弄丢。
func (s *Server) migrateOrganizeMoves(moves []organize.Move) {
	index := s.libraryIndex()
	if index == nil {
		return
	}
	changed := false
	for _, mv := range moves {
		if mv.Kind != "audio" {
			continue
		}
		if _, err := os.Stat(mv.To); err != nil {
			continue
		}
		entry, ok := index.Get(mv.From)
		index.Remove(mv.From)
		if ok {
			entry.Path = mv.To
			index.Upsert(entry)
		}
		changed = true
	}
	if changed {
		_ = index.Save()
	}
}

// organizePlanPayload 把方案裁成「界面看得动」的形态。
//
// 整库预演可能有上万条，一次性回给界面既慢又没必要：统计数字是全量的，
// 明细只回前 500 条（界面本来就只需要让人抽查几条确认方向对不对）。
func organizePlanPayload(plan organize.Plan, root string) map[string]interface{} {
	const maxItems = 500
	items := plan.Items
	truncated := 0
	if len(items) > maxItems {
		truncated = len(items) - maxItems
		items = items[:maxItems]
	}
	if items == nil {
		items = []organize.Item{}
	}
	out := map[string]interface{}{
		"root":      root,
		"template":  plan.Template,
		"stats":     plan.Stats,
		"items":     items,
		"truncated": truncated,
		"warnings":  plan.Warnings,
	}
	return out
}

// organizeJournalSummary 是「能否撤销」的摘要（不含逐条移动明细 —— 那可能几千条）。
//
// pending 是还排着队的批次数：撤销链现在留一串（用户可以一轮一轮往回撤），
// 界面上顺手告诉用户「还剩几轮可撤」。
func organizeJournalSummary(j organize.Journal, ok bool, pending int) map[string]interface{} {
	if !ok {
		return map[string]interface{}{"available": false, "pending": 0}
	}
	return map[string]interface{}{
		"available":  true,
		"created_at": j.CreatedAt,
		"root":       j.Root,
		"template":   j.Template,
		"moved":      j.Moved,
		"moves":      len(j.Moves),
		"pending":    pending,
	}
}
