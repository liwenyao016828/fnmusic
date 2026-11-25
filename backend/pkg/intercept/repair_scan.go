package intercept

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/audioprobe"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/nas"
)

// 歌曲修复（1）：扫描「坏文件」。
//
// 判据全部来自**文件身体**（audioprobe），不看标签 —— 标签正是被怀疑的对象：
//   - 容器与扩展名不符：.flac 里装的是 mp3（假无损 / 转错名）；
//   - 时长过短：默认 < 30 秒，像试听片段 / 截断；
//   - 无损文件小得离谱：体积折算的等效码率 < 200kbps（100 秒才 2MB 的「flac」）。
//
// 这些只是**可疑**，不是判决 —— 效果器前奏、纯人声短样本都可能很短。界面把它叫
// 「文件体检」，让用户勾选哪些要修，绝不自动动手。

// repairScanWorkers 并发探文件。读几个字节的头部，磁盘 IO 很轻，8 路足够。
const repairScanWorkers = 8

// 时长阈值：30 秒以下的音频基本只有试听片段/截断/音效，正经歌曲极少这么短。
const repairShortSec = 30.0

// 无损等效码率下限：低于它的「无损」几乎肯定是假货或截断。
const repairLosslessMinKbps = 200.0

// RepairIssue 一条「文件不对」的体检结果。
type RepairIssue struct {
	Path      string  `json:"path"`
	Title     string  `json:"title,omitempty"`
	Artist    string  `json:"artist,omitempty"`
	Ext       string  `json:"ext,omitempty"`
	Container string  `json:"container,omitempty"`
	Duration  float64 `json:"duration_sec,omitempty"`
	Size      int64   `json:"size,omitempty"`
	Kbps      int     `json:"kbps,omitempty"`
	Kind      string  `json:"kind"`   // ext_mismatch / too_short / fake_lossless
	Detail    string  `json:"detail"` // 一句话说明为什么可疑
}

// RepairScanResult 一次体检的完整结果。
type RepairScanResult struct {
	Scanned int           `json:"scanned"`
	Issues  []RepairIssue `json:"issues"`
	Elapsed int64         `json:"elapsed_ms"`
	Dir     string        `json:"dir"`
}

// ScanRepairIssues 扫描曲库根目录，找出「坏文件」。
//
// 只走曲库根守卫（nas.ResolveSafePath），且只探测 Tunatic 认可的那些扩展名；
// 每个文件只读开头 64KiB + Stat，一万首也在秒级，不需要像替换音质那样摸外网。
func (i *Interceptor) ScanRepairIssues(ctx context.Context, dir string) (RepairScanResult, error) {
	start := time.Now()
	root, err := nas.ResolveSafePath(strings.TrimSpace(dir))
	if err != nil {
		return RepairScanResult{}, err
	}
	// 枚举：复用下载器的曲库根（cfg.DownloadDir），dir 只是缩小范围。
	paths := make([]string, 0, 256)
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 读不了的目录跳过（体检本来就允许部分不可达）
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			base := d.Name()
			if strings.HasPrefix(base, ".") || base == "@eaDir" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == "" || !audioext.Has(p) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return RepairScanResult{}, err
	}
	sort.Strings(paths)

	type probeResult struct {
		issue *RepairIssue
	}
	results := make([]probeResult, len(paths))
	jobs := make(chan int, len(paths))
	for idx := range paths {
		jobs <- idx
	}
	close(jobs)

	var wg sync.WaitGroup
	for n := 0; n < repairScanWorkers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				results[idx].issue = classifyRepairFile(paths[idx])
			}
		}()
	}
	wg.Wait()

	out := RepairScanResult{Dir: root, Scanned: len(paths), Issues: []RepairIssue{}}
	for _, r := range results {
		if r.issue != nil {
			out.Issues = append(out.Issues, *r.issue)
		}
	}
	out.Elapsed = time.Since(start).Milliseconds()
	// 顺手落盘：进度条（见 repair_store.go）读的就是这一份，不能让页面刷新
	// 触发一次全库重扫。失败只记日志，不影响本次扫描的返回。
	i.saveRepairScan(out)
	return out, nil
}

// classifyRepairFile 探测单个文件，有问题返回一条 RepairIssue，没问题返回 nil。
func classifyRepairFile(path string) *RepairIssue {
	info, err := audioprobe.Probe(path)
	if err != nil || info.Size <= 0 {
		return nil // 读不出来不算体检阳性（后面「文件名体检」会单独报）
	}
	ext := strings.ToLower(filepath.Ext(path))
	want := audioprobe.ExtContainer(ext)
	got := info.Container
	if want == "" || got == "" {
		return nil // 容器不认识 → 不判，避免误报
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	artist, title := namefmt.Parse(base)
	if title == "" {
		title = base
	}

	issue := &RepairIssue{
		Path: path, Title: title, Artist: artist, Ext: ext,
		Container: got, Duration: info.DurationSec, Size: info.Size,
		Kbps: info.NominalKbps,
	}
	if want != got {
		issue.Kind = "ext_mismatch"
		issue.Detail = "扩展名是 " + strings.TrimPrefix(ext, ".") + "，文件内容其实是 " + got
		return issue
	}
	if info.DurationSec > 0 && info.DurationSec < repairShortSec {
		issue.Kind = "too_short"
		issue.Detail = "时长只有 " + strings.TrimSpace(fmtDuration(info.DurationSec)) + "，像试听片段或截断"
		return issue
	}
	if audioext.LosslessContainer(ext) && info.DurationSec > 1 && info.EffectiveKbps > 0 && info.EffectiveKbps < repairLosslessMinKbps {
		issue.Kind = "fake_lossless"
		issue.Detail = "无损文件只有 " + formatKBps(info.EffectiveKbps) + "，像假无损或被截断"
		return issue
	}
	return nil
}

// fmtDuration 把秒数写成人类可读的短字符串。
func fmtDuration(sec float64) string {
	if sec < 60 {
		return fmt.Sprintf("%.0f 秒", sec)
	}
	m := int(sec) / 60
	rem := int(sec) % 60
	if rem == 0 {
		return fmt.Sprintf("%d 分", m)
	}
	return fmt.Sprintf("%d 分 %d 秒", m, rem)
}

// formatKBps 把等效码率写成人类可读的字符串。
func formatKBps(kbps float64) string {
	if kbps < 10 {
		return fmt.Sprintf("%.1f kbps", kbps)
	}
	return fmt.Sprintf("%.0f kbps", kbps)
}
