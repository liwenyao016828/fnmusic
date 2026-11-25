package intercept

// 「修复进度」用的落盘快照（repair_scan.json）与两个只读口径。
//
// # 为什么要有这个文件
//
// 用户 2026-11-25 要求：「显示某类修复项的完成进度，格式为『已修复 xx / 总歌曲数』…
// 找个地方长期显示」。这个数字要长期显示，就必须在刷新页面、重启进程之后还在；
// 而 /api/library/repair/scan 是**现扫**（走一遍目录 + 读每个文件头），几百首几秒、
// 上万首几十秒 —— 不能让常驻条每次刷新都去跑一遍。
//
// 所以：扫描结束时顺手把结论落盘，进度接口只读这份快照。
// 「扫描」和「看进度」是两件事，各自有各自的节奏。
//
// # 为什么不塞进 quality_scan.json
//
// 两者失效时机完全不同：音频坏文件只有**换过文件**才会变（预检/换版/整理），
// 音质结论是「音源现在能给到多高」的时效性判断，7 天就过期。
// 混在一个文件里，任何一边的清理逻辑都会把另一边误伤。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// repairScanFileVersion 是 repair_scan.json 的格式版本（以后要迁移时靠它判断）。
const repairScanFileVersion = 1

// repairScanFile 是 repair_scan.json 的形状：一次扫描的结论 + 它是扫哪儿得到的。
//
// Dir 必须存：用户可能换过曲库目录，旧快照不能拿来算新目录的进度。
type repairScanFile struct {
	Version int           `json:"version"`
	SavedAt int64         `json:"saved_at"`
	Dir     string        `json:"dir"`
	Scanned int           `json:"scanned"`
	Issues  []RepairIssue `json:"issues"`
}

// repairScanPath 返回快照文件路径；没配数据目录时返回空串（= 不落盘，功能降级为「待扫描」）。
func (i *Interceptor) repairScanPath() string {
	dir := strings.TrimSpace(i.cfg.DataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "repair_scan.json")
}

// saveRepairScan 把一次扫描结论落盘。
//
// 失败只记日志：进度条少一份数据是可接受的降级，**不能**因为它让扫描本身报错。
func (i *Interceptor) saveRepairScan(res RepairScanResult) {
	i.writeRepairScanFile(repairScanFile{
		Version: repairScanFileVersion,
		SavedAt: time.Now().Unix(),
		Dir:     res.Dir,
		Scanned: res.Scanned,
		Issues:  res.Issues,
	})
}

// writeRepairScanFile 落盘一份快照（先写临时文件再改名）。
//
// 进程被杀在半路时，宁可留旧快照也不要留半个 JSON —— 半个文件读回来会被
// 当成「没扫过」，进度条凭空退回到「待扫描」。
func (i *Interceptor) writeRepairScanFile(f repairScanFile) {
	p := i.repairScanPath()
	if p == "" {
		return
	}
	b, err := json.Marshal(f)
	if err != nil {
		i.logf("修复进度快照编码失败：%v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		i.logf("修复进度快照写入失败：%v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		i.logf("修复进度快照替换失败：%v", err)
	}
}

// updateRepairScanAfterReplace 把「换版本」的结果同步进进度快照。
//
// 不做这一步的话，用户修好十首、进度条还停在「还剩十二首」，只能靠自己再扫一遍 ——
// 那就是把「数字不准」写进了功能里。这里重判新文件：真修好了就划掉，
// 换来的还是坏的就按新结论留一条（换版本不保证候选本身没问题）。
//
// 旧路径与新路径都要处理：mp3 → flac 时文件名都变了。
func (i *Interceptor) updateRepairScanAfterReplace(oldPath, newPath string) {
	f, ok := i.loadRepairScan()
	if !ok {
		return
	}
	kept := make([]RepairIssue, 0, len(f.Issues))
	hit := false
	for _, it := range f.Issues {
		if it.Path == oldPath || (newPath != "" && it.Path == newPath) {
			hit = true
			continue
		}
		kept = append(kept, it)
	}
	if !hit {
		return // 这个文件本来就不在待修清单里，快照不用动
	}
	if issue := classifyRepairFile(newPath); issue != nil {
		kept = append(kept, *issue)
	}
	f.Issues = kept
	f.SavedAt = time.Now().Unix()
	i.writeRepairScanFile(f)
}

// loadRepairScan 读回上一次扫描结论；没有或读不动都当作「没扫过」。
func (i *Interceptor) loadRepairScan() (repairScanFile, bool) {
	p := i.repairScanPath()
	if p == "" {
		return repairScanFile{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return repairScanFile{}, false
	}
	var f repairScanFile
	if err := json.Unmarshal(b, &f); err != nil {
		return repairScanFile{}, false
	}
	if f.Version != repairScanFileVersion {
		return repairScanFile{}, false
	}
	return f, true
}

// underDir 判断 path 是否在 dir 子树内（两边都按 slash 归一，末尾斜杠不算差异）。
func underDir(path, dir string) bool {
	p := strings.TrimSuffix(filepath.ToSlash(path), "/")
	d := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	if d == "" {
		return true
	}
	return p == d || strings.HasPrefix(p, d+"/")
}

// AudioRepairFacts 返回「音频坏文件」这一类在 dir 下的评估口径。
//
// scanned = 上次扫描走过多少个音频文件（= 这一类**已评估**的歌曲数）；
// pending = 其中仍被判为有问题的条数（一个文件最多一条，见 classifyRepairFile）；
// at = 那份快照的时间；known = 有没有可用的快照。
//
// 只有快照目录与查询目录**一致**时才敢给 scanned：换过目录的话，文件数没法从
// 问题清单里反推（清单里只有坏文件），拿它当分母会算出一个虚高的「已修复」。
func (i *Interceptor) AudioRepairFacts(dir string) (scanned, pending int, at int64, known bool) {
	f, ok := i.loadRepairScan()
	if !ok {
		return 0, 0, 0, false
	}
	if strings.TrimSuffix(filepath.ToSlash(f.Dir), "/") != strings.TrimSuffix(filepath.ToSlash(dir), "/") {
		return 0, 0, 0, false
	}
	return f.Scanned, len(f.Issues), f.SavedAt, true
}

// AudioRepairBreakdown 在 AudioRepairFacts 的基础上再给出「按问题类型分」的条数。
//
// 用户 2026-11-25 要的是坏文件页里按类别显示卡片（格式不对 / 时长过短 / 假无损），
// 所以这里把快照里的 Issues 按 Kind 归类。Kind 是 classifyRepairFile 判出来的三个值，
// 认不出的类型不丢，挂在返回的 map 里由调用方决定怎么显示。
func (i *Interceptor) AudioRepairBreakdown(dir string) (scanned int, byKind map[string]int, at int64, known bool) {
	f, ok := i.loadRepairScan()
	if !ok {
		return 0, nil, 0, false
	}
	if strings.TrimSuffix(filepath.ToSlash(f.Dir), "/") != strings.TrimSuffix(filepath.ToSlash(dir), "/") {
		return 0, nil, 0, false
	}
	byKind = make(map[string]int, 4)
	for _, issue := range f.Issues {
		byKind[issue.Kind]++
	}
	return f.Scanned, byKind, f.SavedAt, true
}

// QualityFacts 返回「低音质替换」这一类在 dir 下的评估口径。
//
// covered = 音质扫描里已经探过的歌（一次扫描有条数上限，通常远小于全库）；
// pending = 其中「还能换到更高档位」的条数（换完就地更新，所以这个数会自己往下走）。
//
// 用扫描结论而不是全库数当分母，理由见 api/tidy_progress.go 的口径说明。
func (i *Interceptor) QualityFacts(dir string) (covered, pending int, at int64, known bool) {
	if i.qualityStore == nil {
		return 0, 0, 0, false
	}
	entries := i.qualityStore.snapshot()
	for _, e := range entries {
		if !underDir(e.Path, dir) {
			continue
		}
		covered++
		if e.Upgradeable {
			pending++
		}
		if e.ScannedAt > at {
			at = e.ScannedAt
		}
	}
	if covered == 0 {
		return 0, 0, 0, false
	}
	return covered, pending, at, true
}