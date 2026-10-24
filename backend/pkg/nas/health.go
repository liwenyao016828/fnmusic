// 库体检：找出会把飞牛文件管理／官方音乐 App 搞崩的磁盘条目。
//
// 起因（用户 2026-10-24）：「我在飞牛上访问音乐，还有飞牛官方音乐 app，怎么显示
// 文件夹显示未知错误，之前都没这个问题。」
//
// 已确认的机制：下载落盘时若歌曲标签里带 GBK 脏字节，Go 会把非法 UTF-8 原样写进
// 文件名；fnOS 的文件/媒体接口按 UTF-8 处理名字，遇到非法字节**整层目录**直接报错
// （所以是「文件夹未知错误」而不是「某首歌显示不出来」）。
// 下载侧已经在 pkg/filename 里堵住了新数据，但用户盘上已有的坏名字还得能查能修 ——
// 这个文件就是那一步：扫描 + 一键修复。
package nas

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fn-lx-player/pkg/filename"
)

// 分类常量：前端按这些值决定显示什么图标、给什么修复按钮。
const (
	IssueBadUTF8     = "bad_utf8"      // 文件名含非法 UTF-8 字节（最可能是「未知错误」元凶）
	IssueControlChar = "control_char"  // 含不可见控制字符
	IssueIllegalChar = "illegal_char"  // 含 \ / : * ? " < > |
	IssueTrailing    = "trailing_dot"  // 首尾有点或空格
	IssueTooLong     = "too_long"      // 名字超长（>255 字节）
	IssueZeroByte    = "zero_byte"     // 0 字节音频（坏掉的半成品）
	IssueLeftover    = "leftover_part" // .part / .tmp / .crdownload 残留
	IssueNoPerm      = "no_permission" // 谁都读不了（权限位 000）
	IssueNotRegular  = "not_regular"   // socket/fifo/设备文件
	IssueBrokenLink  = "broken_symlink"
	IssueUnreadable  = "unreadable_dir" // 目录自身读不了（Walk 报错）
)

// 修复动作。
const (
	FixRename = "rename"
	FixDelete = "delete"
	FixChmod  = "chmod"
	FixManual = "manual" // 只报告，不自动动
)

// healthMaxEntries 限制单次扫描的条目数：盘上几十万文件时不能让一次点击卡死整个应用。
const healthMaxEntries = 200000

// healthMaxIssues 限制返回的条目数（前端不需要看五万条，看到前 500 条就能判断性质）。
const healthMaxIssues = 500

// HealthIssue 是一条「体检阳性」。
type HealthIssue struct {
	// ID 是 Path 原始字节的 base64url。**修复接口只认它，不认 path**。
	//
	// 为什么：非法 UTF-8 的文件名往返一次 JSON 就会被编码成 U+FFFD（Go 的
	// encoding/json 对无效字节是替换而不是报错），客户端把 path 原样传回来时
	// 已经找不到那个文件了 —— 而这恰恰是本功能要修的那一类。ID 是 ASCII，
	// 传输无损，服务端再用它回查自己的扫描缓存，顺便也就杜绝了「客户端送路径
	// 让服务端删文件」的可能。
	ID      string `json:"id,omitempty"`
	Path    string `json:"path"` // 仅供显示；带无效字节时这里的字符是替换过的
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
	Fix     string `json:"fix"`
	Suggest string `json:"suggest,omitempty"` // 修复后的名字（Fix=rename 时有意义）
	Size    int64  `json:"size,omitempty"`
}

// HealthReport 是一次扫描的结果。
type HealthReport struct {
	Dir       string        `json:"dir"`
	Scanned   int           `json:"scanned"`
	Issues    []HealthIssue `json:"issues"`
	Truncated bool          `json:"truncated"`
	ElapsedMs int64         `json:"elapsed_ms"`
}

// leftoverSuffixes 是下载/浏览器在中断后留下的半成品后缀。
// 它们不是坏文件，但对媒体库扫描毫无意义，而且会让「这个目录里有奇怪东西」的错觉放大。
var leftoverSuffixes = []string{".part", ".crdownload", ".download", ".tmp"}

// ScanHealth 扫描一个目录，返回体检报告。dir 必须能通过 ResolveSafePath。
func ScanHealth(dir string) (HealthReport, error) {
	start := time.Now()
	rep := HealthReport{Dir: dir}

	real, err := ResolveSafePath(dir)
	if err != nil {
		return rep, err
	}
	rep.Dir = filepath.ToSlash(real)

	walkErr := filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 目录自身读不了：本身就是一条「未知错误」的候选，
			// 因为我们能看见它、却进不去，飞牛的表现就是点开报错。
			if path != real {
				// 能看见、进不去：绝大多数是权限位坏了（比如 000），给个 chmod 的选项；
				// 如果是属主不对，修复会失败并如实回报，不会假装成功。
				rep.Issues = append(rep.Issues, HealthIssue{
					Path: filepath.ToSlash(path), Name: d.Name(), Kind: IssueUnreadable,
					Detail: "目录无法读取：" + err.Error(), Fix: FixChmod, Suggest: "755",
				})
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rep.Scanned++
		if rep.Scanned > healthMaxEntries {
			rep.Truncated = true
			return fs.SkipAll
		}
		if path == real {
			return nil
		}
		if issue, ok := inspectEntry(path, d); ok {
			if len(rep.Issues) < healthMaxIssues {
				rep.Issues = append(rep.Issues, issue)
			} else {
				rep.Truncated = true
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return rep, walkErr
	}

	// 排序：先按分类的严重度（名字坏的优先），再按路径，
	// 让用户第一眼看到的就是「最可能导致未知错误」的那些。
	sort.SliceStable(rep.Issues, func(i, j int) bool {
		if healthRank(rep.Issues[i].Kind) != healthRank(rep.Issues[j].Kind) {
			return healthRank(rep.Issues[i].Kind) < healthRank(rep.Issues[j].Kind)
		}
		return rep.Issues[i].Path < rep.Issues[j].Path
	})
	for i := range rep.Issues {
		rep.Issues[i].ID = encodeIssueID(rep.Issues[i].Path)
	}
	rep.ElapsedMs = time.Since(start).Milliseconds()
	rememberHealthReport(rep)
	return rep, nil
}

// ── 扫描缓存 ─────────────────────────────────────────────────────────
//
// 修复接口按 ID 取回真实路径，靠的就是这份「最近几次扫描」。
// 它只活在内存里：进程重启后 ID 失效，界面会提示重新扫描 ——
// 让用户多扫一次，比让服务端接受客户端声称的路径安全得多。

var (
	healthCacheMu sync.Mutex
	healthCache   = map[string]HealthReport{}
)

const healthCacheDirs = 8

func encodeIssueID(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func decodeIssueID(id string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func rememberHealthReport(rep HealthReport) {
	healthCacheMu.Lock()
	defer healthCacheMu.Unlock()
	// 目录数量上限：每个报告最多 500 条，8 个目录的内存占用可忽略。
	// 满了就丢掉最早插入的一个（map 无序，但我们只关心「最近扫过的几个」）。
	if len(healthCache) >= healthCacheDirs {
		if _, ok := healthCache[rep.Dir]; !ok {
			for k := range healthCache {
				delete(healthCache, k)
				break
			}
		}
	}
	healthCache[rep.Dir] = rep
}

// LookupCachedIssues 按扫描缓存把 ID 还原成条目（含原始字节路径）。
// 第二个返回值是没找到的 ID 个数 —— 它们通常是「中间重启过」，调用方要提示重扫。
func LookupCachedIssues(dir string, ids []string) ([]HealthIssue, int) {
	healthCacheMu.Lock()
	rep, ok := healthCache[filepath.ToSlash(dir)]
	healthCacheMu.Unlock()
	if !ok {
		return nil, len(ids)
	}
	byID := make(map[string]HealthIssue, len(rep.Issues))
	for _, it := range rep.Issues {
		byID[it.ID] = it
	}
	out := make([]HealthIssue, 0, len(ids))
	miss := 0
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		} else {
			miss++
		}
	}
	return out, miss
}

// healthRank 是分类的展示顺序。名字非法的排前面：它们才是「文件夹未知错误」的成因；
// 0 字节/残留文件只是垃圾，不影响别的功能。
func healthRank(kind string) int {
	switch kind {
	case IssueBadUTF8:
		return 0
	case IssueControlChar:
		return 1
	case IssueIllegalChar:
		return 2
	case IssueTrailing:
		return 3
	case IssueTooLong:
		return 4
	case IssueUnreadable:
		return 5
	case IssueNoPerm:
		return 6
	case IssueNotRegular, IssueBrokenLink:
		return 7
	case IssueZeroByte:
		return 8
	case IssueLeftover:
		return 9
	}
	return 10
}

// inspectEntry 判断一个条目是否有问题。返回 false 表示干净。
func inspectEntry(path string, d fs.DirEntry) (HealthIssue, bool) {
	name := d.Name()
	issue := HealthIssue{Path: filepath.ToSlash(path), Name: name}

	// 1) 名字本身的问题 —— 先查，因为这是「整层目录报错」的根因。
	switch {
	case !utf8.ValidString(name):
		issue.Kind = IssueBadUTF8
		issue.Detail = "文件名含非法 UTF-8 字节（来源多半是歌曲标签里的 GBK 脏字节），飞牛的文件接口读到这里会整层报错"
	case hasControlChar(name):
		issue.Kind = IssueControlChar
		issue.Detail = "文件名含不可见控制字符"
	case strings.ContainsAny(name, `\/:*?"<>|`):
		issue.Kind = IssueIllegalChar
		issue.Detail = "文件名含跨平台非法字符"
	case name != strings.Trim(name, ". "):
		issue.Kind = IssueTrailing
		issue.Detail = "文件名首尾有点或空格（Windows/SMB 上打不开）"
	case len(name) > 255:
		issue.Kind = IssueTooLong
		issue.Detail = "文件名超过 255 字节"
	}
	if issue.Kind != "" {
		issue.Fix = FixRename
		issue.Suggest = suggestName(name, d.IsDir())
		if issue.Suggest == name {
			// 兜底：算不出更好的名字就别让用户点了没反应
			issue.Fix = FixManual
		}
		return issue, true
	}

	if d.IsDir() {
		return HealthIssue{}, false
	}

	info, statErr := d.Info()
	if statErr != nil {
		issue.Kind = IssueBrokenLink
		issue.Detail = "无法读取文件信息：" + statErr.Error()
		issue.Fix = FixManual
		return issue, true
	}
	issue.Size = info.Size()

	// 2) 权限位 000：能看见、打不开，飞牛同样会报错。
	if info.Mode().Perm()&0o444 == 0 {
		issue.Kind = IssueNoPerm
		issue.Detail = "文件权限为 000，任何用户都读不了"
		issue.Fix = FixChmod
		return issue, true
	}

	// 3) 非普通文件（socket/fifo/设备）：媒体扫描一碰就会出事，且绝不可能是用户想听的歌。
	if !info.Mode().IsRegular() {
		issue.Kind = IssueNotRegular
		issue.Detail = "不是普通文件（socket/fifo/设备节点）"
		issue.Fix = FixManual
		return issue, true
	}

	// 4) 残留半成品
	lower := strings.ToLower(name)
	for _, suf := range leftoverSuffixes {
		if strings.HasSuffix(lower, suf) {
			issue.Kind = IssueLeftover
			issue.Detail = "上次中断留下的半成品，占空间且会干扰媒体库"
			issue.Fix = FixDelete
			return issue, true
		}
	}

	// 5) 0 字节音频：标签写坏了或者下载中断，播放器点开就是错误。
	if info.Size() == 0 && isAudioExt(strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")) {
		issue.Kind = IssueZeroByte
		issue.Detail = "0 字节音频文件，播放必然失败"
		issue.Fix = FixDelete
		return issue, true
	}

	return HealthIssue{}, false
}

// hasControlChar 判断是否含 C0 控制字符或 DEL（\r\n\t 会被 trim 规则覆盖，这里一并算坏）。
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// isAudioExt 判断扩展名是不是音频；0 字节判定只对音频生效，
// 免得把用户放在同目录的 0 字节 txt/封面说明当成垃圾删掉。
func isAudioExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "m4a", "aac", "ogg", "wav", "ape", "dsf", "dff", "wma", "opus":
		return true
	}
	return false
}

// suggestName 算出「干净的名字」。目录直接用文件名规则（目录也能改名，改完飞牛就能进）；
// 文件保留扩展名 —— 扩展名是播放器/媒体库识别格式的依据，绝不能一起洗掉。
func suggestName(name string, isDir bool) string {
	if isDir {
		return filename.Sanitize(name)
	}
	ext := filepath.Ext(name)
	if !validExt(ext) {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	return filename.Sanitize(stem) + ext
}

// validExt 只接受「点 + 1~8 位字母数字」的扩展名。
//
// 为什么不复用 filename.Sanitize：它会去掉首尾的点，".mp3" 会被洗成 "mp3"，
// 结果文件名反而丢了扩展名（2026-10-24 自测踩到）。扩展名本来就窄，
// 直接白名单更安全：带非法字节的假扩展名宁可丢掉，也别带进新名字里。
func validExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 9 || ext[0] != '.' {
		return false
	}
	for i := 1; i < len(ext); i++ {
		c := ext[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// ── 修复 ──────────────────────────────────────────────────────────────

// FixResult 是单条修复的结果。
type FixResult struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	NewPath string `json:"new_path,omitempty"`
	Note    string `json:"note,omitempty"`
	Error   string `json:"error,omitempty"`
}

// FixIssues 按体检报告执行修复。只接受 scan 报告里出现过的动作，路径必须落在允许根目录内，
// 避免「拿着一个体检响应就能删任意文件」。
func FixIssues(root string, issues []HealthIssue, dryRun bool) []FixResult {
	realRoot, err := ResolveSafePath(root)
	if err != nil {
		return []FixResult{{Path: root, Action: FixManual, Error: err.Error()}}
	}

	results := make([]FixResult, 0, len(issues))
	for _, it := range issues {
		res := FixResult{Path: it.Path, Kind: it.Kind, Action: it.Fix}
		// 两种来源：
		//   · 带 ID 的（正常路径）—— 来自本进程的扫描缓存，路径是原始字节，
		//     这时只能自己做包含性校验：ResolveSafePath 走 EvalSymlinks，
		//     碰到「首尾空格」这类名字会判为不可访问，而那正是要修的东西。
		//   · 不带 ID 的（老客户端/手工调用）—— 退回通用校验。
		var real string
		var err error
		if it.ID != "" {
			real, err = trustedIssuePath(realRoot, it.Path)
		} else {
			real, err = ResolveSafePath(it.Path)
			if err == nil && real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				err = errors.New("路径不在本次体检的目录内")
			}
		}
		if err != nil {
			res.Error = "拒绝处理：" + err.Error()
			results = append(results, res)
			continue
		}
		if dryRun {
			// dry-run 只报「将会做什么」：以前它把 OK 置 true，调用方按 OK 数
			// 报「已修好 N 条」，用户会以为已经改完了。
			res.Action = "would_" + it.Fix
			res.Note = "预览：没有改动任何文件"
			results = append(results, res)
			continue
		}
		switch it.Fix {
		case FixRename:
			newPath, err := renameToClean(real, it.Suggest)
			if err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
				res.NewPath = filepath.ToSlash(newPath)
			}
		case FixDelete:
			if err := os.Remove(real); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
		case FixChmod:
			// 目录要 0755 才能进去，文件 0644 就够。以前一律 0644，
			// 修完权限 000 的目录依然打不开。
			mode := fs.FileMode(0o644)
			if fi, statErr := os.Lstat(real); statErr == nil && fi.IsDir() {
				mode = 0o755
			}
			if err := os.Chmod(real, mode); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
		default:
			res.Error = "这条需要人工处理"
		}
		results = append(results, res)
	}
	return results
}

// trustedIssuePath 校验一条「来自本进程扫描缓存」的路径。
//
// 名字本身不查合法性（目标是修它），但必须证明它确实落在本次体检的目录内，
// 而且**父目录**要经过 EvalSymlinks —— 父目录才是可能被换成软链接逃逸的那一层，
// 文件名保持原样才能命中带空格/无效字节的真实条目。
func trustedIssuePath(realRoot, raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", errors.New("路径不是绝对路径")
	}
	clean := filepath.Clean(raw)
	if clean != realRoot && !strings.HasPrefix(clean, realRoot+string(filepath.Separator)) {
		return "", errors.New("路径不在本次体检的目录内")
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", err
	}
	if realDir != realRoot && !strings.HasPrefix(realDir, realRoot+string(filepath.Separator)) {
		return "", errors.New("路径不在本次体检的目录内")
	}
	if _, err := os.Lstat(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// renameToClean 把文件改成干净名字；同名已存在时追加 " (2)"、" (3)"…
// 不覆盖任何已有文件：体检工具最忌讳「修着修着把歌删了」。
func renameToClean(oldPath, suggest string) (string, error) {
	dir := filepath.Dir(oldPath)
	base := filepath.Base(suggest)
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("算不出安全的名字")
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	target := filepath.Join(dir, base)
	for n := 2; n <= 50; n++ {
		if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
	}
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("同名文件太多，放弃改名")
	}
	if pointsToSame(oldPath, target) {
		return "", fmt.Errorf("改名前后是同一个文件")
	}
	if err := os.Rename(oldPath, target); err != nil {
		return "", err
	}
	return target, nil
}

// pointsToSame 用 os.SameFile 判重，避免大小写不敏感的文件系统上把文件改名成自己。
func pointsToSame(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// ── HTTP 入口 ────────────────────────────────────────────────────────

// HandleHealthScan POST /api/nas/health/scan  body: {"dir":"/vol1/Music","limit":500}
func HandleHealthScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var req struct {
		Dir   string `json:"dir"`
		Limit int    `json:"limit"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)

	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = strings.TrimSpace(r.URL.Query().Get("dir"))
	}
	if dir == "" {
		roots := AllowedRoots()
		if len(roots) == 0 {
			writePathError(w, ErrNoAllowedRoots)
			return
		}
		dir = roots[0]
	}

	rep, err := ScanHealth(dir)
	if err != nil {
		writePathError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"dir": rep.Dir, "scanned": rep.Scanned, "issues": rep.Issues,
		"truncated": rep.Truncated, "elapsed_ms": rep.ElapsedMs,
		"data": rep,
	})
}

// HandleHealthFix POST /api/nas/health/fix  body: {"dir":"/vol1/Music","issues":[...],"dry_run":false}
func HandleHealthFix(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var req struct {
		Dir    string        `json:"dir"`
		IDs    []string      `json:"ids"`    // 首选：scan 返回的 issue.id
		Issues []HealthIssue `json:"issues"` // 兼容旧客户端（按 path 处理）
		DryRun bool          `json:"dry_run"`
	}
	missed := 0
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "请求体无法解析"})
		return
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		roots := AllowedRoots()
		if len(roots) == 0 {
			writePathError(w, ErrNoAllowedRoots)
			return
		}
		dir = roots[0]
	}
	issues := req.Issues
	if len(req.IDs) > 0 {
		// 目录必须用扫描时报告里的那个（EvalSymlinks 之后的形式）去查缓存，
		// 否则同一个目录写成 /vol1/Music/ 或带软链接就会查不到。
		lookupDir := strings.TrimSpace(req.Dir)
		if real, err := ResolveSafePath(lookupDir); err == nil {
			lookupDir = real
		}
		issues, missed = LookupCachedIssues(lookupDir, req.IDs)
		if len(issues) == 0 {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 409, "message": "扫描结果已过期（应用可能重启过），请重新扫描一次再修",
			})
			return
		}
		// 部分命中就照做；missed 让界面能说清「有 N 条已经对不上了」
	}
	if len(issues) == 0 {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "message": "没有要修的东西", "fixed": 0})
		return
	}

	results := FixIssues(dir, issues, req.DryRun)
	fixed, would := 0, 0
	var failed []FixResult
	for _, res := range results {
		switch {
		case res.OK:
			fixed++
		case req.DryRun && strings.HasPrefix(res.Action, "would_"):
			would++
		default:
			failed = append(failed, res)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "ok",
		"fixed": fixed, "would_fix": would, "missed": missed, "failed": failed, "results": results,
	})
}
