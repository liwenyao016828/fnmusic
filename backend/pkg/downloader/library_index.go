// 全库索引去重：下载/查重时也看**曲库里已有的文件**，而不只是下载目录。
//
// 为什么需要（用户报的原始问题）：查重只按 `下载目录 + 由歌名拼出的文件名` 精确比对，
// 于是同一首歌只要**位置或命名**有一点不同——用户手动整理进了 `歌手/专辑/`、文件名
// 里带了注释、或者文件是别的工具写进去的——下载器就看不见它，再下一份。曲库里于是
// 出现两条同名同歌的音频，播放列表里也跟着重复。
//
// 判据的核心矛盾：`match.NormTitle` 会**整段删掉括号**，所以「晴天」与「晴天 (Live)」
// 的相似度是 1.0 —— 只靠相似度判「已有」会把现场版当成录音室版，那比重复下载更糟
// （用户点「播放」听到的是另一个版本）。所以这里的规则是：
//
//  1. 相似度 ≥ 0.80（歌名 0.7 + 歌手 0.3），且**歌名本身**的相似度 ≥ 0.85；
//  2. **版本类型**必须一致，且改编版之间要比「版本指纹」——指纹不等就不算同版本；
//  3. 歌手信息双方都有时必须兼容（防同名不同歌手串歌）；
//  4. 时长只在**可靠解析出来**时才参与否决（mp3 必须有 Xing/Info，flac 读 STREAMINFO，
//     见 duration_probe.go），解析不出来一律当成「不知道」，绝不因此拒绝走「已存在」；
//  5. 索引里的路径失效（删了/换盘/半成品/回收站）→ 跳过这条候选，**绝不**因此判「已存在」。
//
// 所有「歧义」一律倒向**保守**：宁可不判已有（多下一次），也不误判同版本（串版本）。
package downloader

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/namefmt"
)

const (
	// libraryMatchMinScore 判定「曲库里已经有这首歌」的相似度门槛。
	//
	// 与 `pkg/push.matchLocal` 的 localMatchMinScore 取同值 0.80（比 match.MatchMinScore
	// 的 0.72 严）：那边是「找一条本地文件来播放」，错配尚且能忍；这边是
	// 「决定不要再下载」，错配的后果是把另一首歌当成已有、用户永远拿不到他要的那首。
	libraryMatchMinScore = 0.80

	// libraryMatchMinTitle 歌名本身的相似度下限（比总分更严）。
	//
	// 总分里歌手占 0.3，同歌手的两首歌（`Numb` / `Numb Encore`）靠歌手就能把总分抬过
	// 0.80。查重的结论是「别下载了」，误判的代价是用户想下的那首永远下不下来，
	// 所以歌名这一项单独再卡一道。
	libraryMatchMinTitle = 0.85

	// libraryMinAudioBytes 目录查重（findExistingAudio）与索引查重共用的体积下限。
	// 低于它的多半是损坏文件、占位文件或极短的试听片段。
	libraryMinAudioBytes = 500 * 1024

	// libraryMaxDurationProbes 一次查重最多探测几条候选的真实时长。
	//
	// 时长探测要开文件读帧头（见 duration_probe.go）。候选通常只有一两条，
	// 但曲库大、同名歌多（各种合集/翻唱）时排序后的前几名可能都很像；给个上限，
	// 避免一次批量查重（200 首）变成上万次 open。
	libraryMaxDurationProbes = 5
)

// libraryBadPathMarks 路径里出现这些片段就当它不是「用户曲库里的正式文件」。
//
// `.part` 是下载器自己的半成品，`.tidy_trash` 是整理功能的回收站——两者都还在盘上，
// 出现时体积也够大，但它们不是「已经有了」，绝不能让下载被跳过。
var libraryBadPathMarks = []string{".part", ".tidy_trash", ".ai_backup/"}

// staleLogged 已经记过日志的失效路径（进程内去重）。
//
// 索引快照里的失效路径会一直躺在那里（本包不改索引，Prune 属于 pkg/library），
// 而查重是高频调用（每批 200 首）。每首每轮都打一条 warn，日志会被这一件事刷满，
// 真正要看的问题被淹掉。按路径记一次就够。
var staleLogged sync.Map

// LocalIndexHit 曲库索引里命中的那条既有文件。
//
// 字段给 HTTP 层直接序列化（`/api/download/library/lookup`）用，所以尽量扁平。
// `Score` 用 float64 是为了让前端能显示「多像」并自己决定要不要提示。
type LocalIndexHit struct {
	Path   string `json:"path"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	// Format 是索引里记的容器（flac/mp3…），Tier 是按**实际帧头**判的档位。
	Format string `json:"format,omitempty"`
	Tier   string `json:"tier,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// Duration 是探测到的本地时长（秒），0 = 没探出来（不是 0 秒）。
	Duration int `json:"duration,omitempty"`
	// Score 是 match.Similarity 的原始分（0~1）。
	Score float64 `json:"score"`
	// Upgradable 表示「盘上这份档位低于本次请求」，可以原地升级（见 libraryUpgradable）。
	//
	// ⚠️ 它只是**信息**，绝不能用它决定跳过还是升级：真正的判定必须走调用方的
	// `SongPayload.shouldUpgradeOver`，因为只有那里知道调用方有没有开
	// `upgrade_if_better`。用 Upgradable 决定升级，等于替没开开关的调用方
	// （比如开放接口的外部调用）偷偷覆盖用户的文件。
	Upgradable bool `json:"upgradable,omitempty"`
	// Reason 是给人和日志看的一句话结论（不做机器判别用）。
	Reason string `json:"reason,omitempty"`
}

// SetLibraryIndex 注入「全库索引快照」的读取函数（`library.Index.List`）。
//
// 为什么注入函数而不是一份切片：索引是**可变**的（扫描会整份替换快照），
// 注入拷贝等于把下载器的视野钉死在启动那一刻——用户刚扫完的新文件永远看不见。
// 注入函数则每次查重都读最新快照，代价只是一次加锁拷贝。
//
// 不注入也能正常工作：此时只按下载目录判重（v2.18.0 之前的行为）。
func (d *Downloader) SetLibraryIndex(snapshot func() []library.Entry) {
	d.libMu.Lock()
	d.libIndex = snapshot
	d.libMu.Unlock()
}

// librarySnapshot 取一次索引快照。批量查重时调用方应当只取一次并复用，
// 否则 200 首就是 200 次全量拷贝。
func (d *Downloader) librarySnapshot() []library.Entry {
	d.libMu.RLock()
	fn := d.libIndex
	d.libMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// findInLibrary 在最新快照里找这首歌（不管音质，调用方自己用 shouldUpgradeOver 判）。
func (d *Downloader) findInLibrary(song SongPayload) (LocalIndexHit, bool) {
	return MatchLibrary(d.librarySnapshot(), song)
}

// MatchLibrary 在一份索引快照里找「这首歌盘上已经有了」的最佳命中。
//
// 导出是为了让 HTTP 层（`/api/download/library/lookup`）与下载器**共用同一套判据**：
// 两处各写一份，迟早会出现「查询页说没有、下载器说有」的互相打脸。
//
// 返回 false 时调用方应当照常下载：这里的 false 同时覆盖「真的没有」与「看着像但
// 不敢确认」两种情形，后者宁可重复下一次，也不能串版本。
func MatchLibrary(entries []library.Entry, song SongPayload) (LocalIndexHit, bool) {
	name := strings.TrimSpace(song.Name)
	if name == "" || len(entries) == 0 {
		return LocalIndexHit{}, false
	}
	req := match.Catalog{
		Name:     name,
		Artist:   strings.TrimSpace(song.Singer),
		Album:    strings.TrimSpace(song.Album),
		Duration: song.Duration,
	}

	cands := make([]libraryCandidate, 0, 4)
	for _, e := range entries {
		if !libraryEntryUsable(e) {
			continue
		}
		cat := catalogForEntry(e)
		if strings.TrimSpace(cat.Name) == "" {
			continue
		}
		// 试听片段（demo/试听/片段，或时长 ≤75s）不算「已有」：
		// 用户要的是整首，判已有等于让他永远拿不到整首。
		if match.IsPreview(cat) {
			continue
		}
		if titleSimilarity(req.Name, cat.Name) < libraryMatchMinTitle {
			continue
		}
		score := match.Similarity(req, cat)
		if score < libraryMatchMinScore {
			continue
		}
		if !artistCompatible(req.Artist, cat.Artist) {
			continue
		}
		if versionIncompatibility(req, cat) != "" {
			continue
		}
		cands = append(cands, libraryCandidate{entry: e, cat: cat, score: score})
	}
	if len(cands) == 0 {
		return LocalIndexHit{}, false
	}

	// 同分时优先「同专辑」（同一版录音的强信号），再按路径稳定排序，
	// 保证同一批数据在多次调用里给出同一个答案（幂等，便于前端去抖与测试）。
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		ai, aj := albumEqual(req.Album, cands[i].cat.Album), albumEqual(req.Album, cands[j].cat.Album)
		if ai != aj {
			return ai
		}
		return cands[i].entry.Path < cands[j].entry.Path
	})

	// 时长否决只对**排序后的前几名**做：它的作用是把「同名但不同录音」剔掉，
	// 不是给全库打分。
	for i := range cands {
		if i >= libraryMaxDurationProbes {
			break
		}
		c := cands[i]
		dur := probeDurationSec(c.entry.Path)
		if durationMismatch(req.Duration, dur) != "" {
			continue
		}
		return buildLibraryHit(c, song, dur), true
	}
	return LocalIndexHit{}, false
}

// libraryCandidate 一条通过了全部判据的候选（还没做时长探测）。
type libraryCandidate struct {
	entry library.Entry
	cat   match.Catalog
	score float64
}

// buildLibraryHit 把候选折成可序列化的命中结论。
func buildLibraryHit(c libraryCandidate, song SongPayload, dur int) LocalIndexHit {
	hit := LocalIndexHit{
		Path:     filepath.ToSlash(c.entry.Path),
		Title:    strings.TrimSpace(c.entry.Title),
		Artist:   strings.TrimSpace(c.entry.Artist),
		Album:    strings.TrimSpace(c.entry.Album),
		Format:   audioFormatOf(c.entry.Path, c.entry.Format),
		Tier:     audioext.TierOfFile(c.entry.Path),
		Size:     c.entry.Size,
		Duration: dur,
		Score:    math.Round(c.score*1000) / 1000,
		Reason:   "曲库里已存在",
	}
	if fi, err := os.Stat(c.entry.Path); err == nil {
		hit.Size = fi.Size()
	}
	if up, reason := libraryUpgradable(song, c.entry.Path); up {
		hit.Upgradable = true
		hit.Reason = reason
	}
	return hit
}

// libraryUpgradable 报告盘上这份文件能不能被本次请求升档（**只作信息展示**）。
//
// 判据**完全复用** `SongPayload.shouldUpgradeOver`（v2.17.2 起的那套安全升级逻辑：
// 档位严格更高，或同档位但容器不同、且旧的是有损），不在这里另写一份。
// 另写一份的后果就是「查询页说能升、下载器说不升」。
//
// 这里临时把 UpgradeIfBetter 置真，是因为要回答的是「有没有更好的档位**可用**」；
// 而「要不要真的覆盖」**必须**由调用方拿原始 song 去问 shouldUpgradeOver。
func libraryUpgradable(song SongPayload, path string) (bool, string) {
	probe := song
	probe.UpgradeIfBetter = true
	if !probe.shouldUpgradeOver(path) {
		return false, ""
	}
	cur := audioext.TierOfFile(path)
	return true, fmt.Sprintf("已存在 %s（%s），本次 %s 可原地升级", filepath.Base(path), cur, song.Quality)
}

// libraryEntryUsable 判断索引里的这一条能不能作为「已有文件」的候选。
//
// 失效路径在这里被**静默跳过**：索引是异步扫描出来的，用户删文件、拔移动硬盘、
// 整理功能挪目录之后，索引里会留一批指不到东西的路径。若把「stat 失败」当成
// 「已存在」，下载会被永久挡住——而用户看到的是一个空目录。反过来，这里跳过
// 也**不**动索引：清理失效条目是 pkg/library Prune 的活，下载器只读。
func libraryEntryUsable(e library.Entry) bool {
	p := strings.TrimSpace(e.Path)
	if p == "" || !filepath.IsAbs(p) || !audioext.Has(p) {
		return false
	}
	lower := strings.ToLower(p)
	for _, bad := range libraryBadPathMarks {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		logStaleLibraryPath(p)
		return false
	}
	// 索引里的体积可能与盘上不一致（文件被截断/替换过）：以盘上为准再判一次。
	return fi.Size() >= libraryMinAudioBytes
}

// logStaleLibraryPath 同一条失效路径每个进程只记一次（见 staleLogged 的说明）。
func logStaleLibraryPath(path string) {
	if _, loaded := staleLogged.LoadOrStore(path, struct{}{}); loaded {
		return
	}
	applog.Default().AddFrom("downloader", "warn",
		fmt.Sprintf("[查重] 索引里的文件已经不在了，跳过：%s", path))
}

// catalogForEntry 把索引条目转成匹配用的 Catalog。
//
// 索引条目可能**没读过标签**（只扫了文件名，Title 为空）：这时用 namefmt.Parse
// 从文件名主干反解歌手/歌名，与写入侧（namefmt.Build）严格互逆。
// 注意方向是全局配置决定的，所以只能借 namefmt，不能自己切 " - "。
func catalogForEntry(e library.Entry) match.Catalog {
	title := strings.TrimSpace(e.Title)
	artist := strings.TrimSpace(e.Artist)
	if title == "" {
		base := strings.TrimSuffix(filepath.Base(e.Path), filepath.Ext(e.Path))
		a, t := namefmt.Parse(base)
		if artist == "" {
			artist = strings.TrimSpace(a)
		}
		title = strings.TrimSpace(t)
	}
	return match.Catalog{Name: title, Artist: artist, Album: strings.TrimSpace(e.Album)}
}

// artistCompatible 判断两个歌手标注是否兼容。
//
// 双方都有值时才判：一方为空说明信息不足，不否决（否决等于放弃一次正确的去重）。
// 兼容 = 归一后相等或互相包含（"周杰伦" vs "周杰伦/杨瑞代" 是中文多歌手的常见形态），
// 或者主歌手一致。**不做模糊相似**：0.8 的总分已经容不下歌手误判，
// 而「晴天 / 周杰伦」与「晴天 / 某翻唱」正是要挡住的那类串歌。
func artistCompatible(a, b string) bool {
	na, nb := match.NormArtist(a), match.NormArtist(b)
	if na == "" || nb == "" {
		return true
	}
	if na == nb || strings.Contains(na, nb) || strings.Contains(nb, na) {
		return true
	}
	pa := match.NormArtist(match.PrimaryArtist(a))
	pb := match.NormArtist(match.PrimaryArtist(b))
	return pa != "" && pb != "" && (pa == pb || strings.Contains(pa, pb) || strings.Contains(pb, pa))
}

// versionIncompatibility 判断两个 Catalog 是不是**不同的版本**，返回非空表示不同。
//
// 为什么要单独判：`match.NormTitle` 删掉整段括号，于是「晴天」与「晴天 (Live)」
// 歌名分是满分。只靠相似度，用户点下载录音室版时会被判「已存在」，实际拿到的是现场版。
//
// 规则（歧义一律保守，见包注释）：
//   - 正式版 vs 改编版（match.Kind 不同）→ 不同版本；
//   - 改编版 vs 改编版 → 比「版本指纹」：归一化后保留括号内文字，相等或一方是
//     另一方的子串才算同版本。`(Live)` 与 `(Live at 台北)` 是同一版，`(Live)` 与
//     `(Remix)` 不是；
//   - 正式版 vs 正式版 → 同版本（括号里塞的是「电影主题曲」这类说明，不是版本）。
func versionIncompatibility(req, cand match.Catalog) string {
	rk, ck := match.Kind(req), match.Kind(cand)
	if rk != ck {
		return "版本类型不同（正式版 / 改编版）"
	}
	if rk != match.KindVariant {
		return ""
	}
	a, b := versionKey(req.Name, req.Album), versionKey(cand.Name, cand.Album)
	if a == "" || b == "" {
		// 指纹为空说明两边都没有可用文字，无法判断 —— 保守判「不同版本」。
		return "版本指纹为空，无法确认同版本"
	}
	if a == b || strings.Contains(a, b) || strings.Contains(b, a) {
		return ""
	}
	return "改编版本不同"
}

// versionKey 版本指纹：小写后只留字母、数字与中日韩文字（**保留括号内的文字**）。
//
// 与 match.NormTitle 的关键区别就是「保留括号内文字」——那正是版本后缀所在的位置。
// 歌名里没有可用字符时退回专辑（"晴天" + 专辑"演唱会" 也是改编版，只是标记在专辑上）。
func versionKey(name, album string) string {
	if k := keepAlnumCJKLower(name); k != "" {
		return k
	}
	return keepAlnumCJKLower(album)
}

// keepAlnumCJKLower 小写后只保留字母/数字/中日韩文字，其余（含括号、空格、标点）全丢。
//
// 与 match 包内部的 keepAlnumCJK 同口径，但那边不导出；这里需要的只是「去掉标点、
// 保住文字」，保持一份本地实现比把内部函数导出去更省事。
func keepAlnumCJKLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4E00 && r <= 0x9FFF, // CJK
			r >= 0x3040 && r <= 0x30FF, // 日文假名
			r >= 0xAC00 && r <= 0xD7AF: // 韩文
			b.WriteRune(r)
		}
	}
	return b.String()
}

// titleSimilarity 只算歌名相似度。
//
// match.Similarity 的权重是歌名 0.7 + 歌手 0.3；两边歌手都留空时歌手项固定 0.5，
// 于是 total = 歌名相似度*0.7 + 0.15，可以直接反推。
func titleSimilarity(a, b string) float64 {
	s := match.Similarity(match.Catalog{Name: a}, match.Catalog{Name: b})
	return math.Max(0, math.Min(1, (s-0.15)/0.7))
}

// durationMismatch 判断本地文件的真实时长与请求时长是否差得太多，返回非空表示不符。
//
// 只在**两边都有值**时判：请求没带时长（前端的 check 探测体只有 id/name/singer）、
// 或本地解析不出来（mp3 没有 Xing/Info），都当成「不知道」——「不知道」不能当
// 「不符」用，否则一条误判就把本该去重的下载又做一遍。
// 容差与 pkg/match 一致：max(10s, 15%)。
func durationMismatch(want, got int) string {
	if want <= 0 || got <= 0 {
		return ""
	}
	tol := int(math.Round(float64(want) * match.DurationToleranceRatio))
	if tol < match.DurationToleranceAbs {
		tol = match.DurationToleranceAbs
	}
	diff := want - got
	if diff < 0 {
		diff = -diff
	}
	if diff > tol {
		return fmt.Sprintf("时长不符（请求 %ds，本地 %ds）", want, got)
	}
	return ""
}

// albumEqual 专辑名是否可视为相同（归一后相等）。
func albumEqual(a, b string) bool {
	na, nb := keepAlnumCJKLower(a), keepAlnumCJKLower(b)
	return na != "" && na == nb
}

// audioFormatOf 命中的容器格式：优先索引记的值，没有就从扩展名取。
func audioFormatOf(path, fromIndex string) string {
	if f := strings.ToLower(strings.TrimSpace(fromIndex)); f != "" {
		return f
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// sameDirAs 判断 path 是否就在 dir 目录下（只比目录，不解析软链接）。
func sameDirAs(path, dir string) bool {
	return filepath.Clean(filepath.Dir(path)) == filepath.Clean(dir)
}
