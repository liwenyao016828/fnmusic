package fnos

// 曲库重扫的合并调度。
//
// 背景（见 docs/飞牛刮削适配调研.md）：
// 下载/整理把文件写进音乐目录后，飞牛音乐**不会自己发现** ——
// 它的曲库索引靠 POST /shared-library/scan-all 刷新。
// 我们早就实现了 ScanLibrary()，但一直没人调用（死代码），
// 结果是「下载完飞牛里看不到新歌」。
//
// 但也不能每下载一首就调一次：scan-all 是**全量扫描**，
// 一批 200 首会触发 200 次全量扫描，把 NAS 拖垮。
//
// 所以这里做两层合并：
//   1. **debounce**：最后一次请求之后再等 debounce 才执行，
//      期间的新请求只是把定时器往后推 —— 一批下载只触发一次
//   2. **最小间隔**：两次实际执行之间至少隔 minGap，
//      避免「连续几批小下载」把全量扫描打成连续跑

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

const (
	// rescanDebounce 一批任务结束后的静默期：这段时间内没有新请求才真正扫描
	rescanDebounce = 30 * time.Second
	// rescanMinGap 两次实际扫描之间的最小间隔（大曲库下全量扫描很贵）
	//
	// ⚠️ 2026-09-22 由 **3 分钟放宽到 10 分钟**。用户反馈飞牛那边的「音乐任务」
	// 看起来**一直在扫描**，问「这个项目在一直触发吗」。排查结论：
	// 我们确实在触发，但已被节流到「每 3 分钟最多一次」——
	// 问题在于**我们从不查询飞牛那边扫完没有**，单次全量若超过 3 分钟，
	// 就成了上一轮还没结束、下一轮已经到达，从 NAS 上看就是永不停歇。
	// 10 分钟给全量扫描留出余量（用户明确选了「调得更安静」；
	// 代价是刚下完的歌进飞牛曲库最晚可能多等几分钟）。
	rescanMinGap = 10 * time.Minute
)

// RescanScheduler 合并并节流「曲库重扫」请求。
type RescanScheduler struct {
	mu        sync.Mutex
	timer     *time.Timer
	debounce  time.Duration
	minGap    time.Duration
	lastRun   time.Time
	forceNext bool         // 下一次执行跳过 minGap（用户手动触发时用，见 ScheduleNow）
	statePath string       // 非空则把 lastRun 落盘（见 SetStatePath）
	scan      func() error // 便于测试注入
}

// rescanState lastRun 的落盘格式
type rescanState struct {
	LastRunUnix int64 `json:"last_run_unix"`
}

// NewRescanScheduler 创建调度器；debounce / minGap 传 0 用默认值。
func NewRescanScheduler(debounce, minGap time.Duration) *RescanScheduler {
	if debounce <= 0 {
		debounce = rescanDebounce
	}
	if minGap <= 0 {
		minGap = rescanMinGap
	}
	return &RescanScheduler{
		debounce: debounce,
		minGap:   minGap,
		scan: func() error {
			m := NewMusic("")
			if !m.Available() {
				return nil // 非飞牛环境：静默跳过
			}
			return m.ScanLibrary()
		},
	}
}

// SetStatePath 指定 lastRun 的落盘位置（通常是配置数据目录）。
//
// 不设也能跑，只是 `lastRun` 只活在内存里 —— **进程重启即归零**，
// 重启后 30 秒就能再扫一次，跟重启前那次毫无关系。
// 这是「一直在扫」的成因之一，所以由 server 把数据目录接进来。
func (s *RescanScheduler) SetStatePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statePath = path
	// 只在落盘的记录**更新**时采纳：内存里可能已经有一次更近的扫描
	if last, ok := s.readStateLocked(); ok && last.After(s.lastRun) {
		s.lastRun = last
	}
}

func (s *RescanScheduler) readStateLocked() (time.Time, bool) {
	if s.statePath == "" {
		return time.Time{}, false
	}
	raw, err := os.ReadFile(s.statePath)
	if err != nil {
		return time.Time{}, false
	}
	var st rescanState
	if err := json.Unmarshal(raw, &st); err != nil || st.LastRunUnix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(st.LastRunUnix, 0), true
}

func (s *RescanScheduler) writeStateLocked(t time.Time) {
	if s.statePath == "" {
		return
	}
	raw, err := json.Marshal(rescanState{LastRunUnix: t.Unix()})
	if err != nil {
		return
	}
	// 写不上不影响下载与扫描本身，只记一行日志
	if err := os.WriteFile(s.statePath, raw, 0o600); err != nil {
		log.Printf("[FNOS] 记录重扫时间失败: %v", err)
	}
}

// Schedule 请求一次重扫。多次调用会被合并成一次。
func (s *RescanScheduler) Schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.debounce, s.run)
}

// ScheduleNow 请求一次「尽快执行、不等最小间隔」的重扫。
//
// 只给**用户手动触发**的批量操作（文件夹整理这类一次搬几百个文件的动作）用：
// 走普通 Schedule 的话，整理完最多要等 10 分钟 minGap + 30 秒 debounce 才能被
// 飞牛发现，用户在飞牛音乐里看到的是「整理完歌全不见了」。
// 节流本来是防「批量下载把全量扫描打成连续跑」，而人手点一次整理天然不可能高频，
// 所以这一次跳过 minGap 是安全的；debounce 仍然保留（合并连点）。
func (s *RescanScheduler) ScheduleNow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forceNext = true
	if s.timer != nil {
		s.timer.Stop()
	}
	// 手动操作给一个短静默期就够：让同一批的多次调用合并，又不至于让用户干等
	d := s.debounce
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	s.timer = time.AfterFunc(d, s.run)
}

// Stop 取消尚未执行的扫描（进程退出时调用）。
func (s *RescanScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *RescanScheduler) run() {
	s.mu.Lock()
	// 手动触发（ScheduleNow）允许这一次无视最小间隔；跑完就复位，
	// 下一次回到常规节流，避免把这个口子变成常态。
	forced := s.forceNext
	s.forceNext = false
	// 距上次实际执行太近：推迟到满足最小间隔再跑，而不是直接丢弃
	if wait := s.minGap - time.Since(s.lastRun); !forced && wait > 0 {
		s.timer = time.AfterFunc(wait, s.run)
		s.mu.Unlock()
		return
	}
	scan := s.scan
	s.timer = nil
	s.mu.Unlock()

	if err := scan(); err != nil {
		log.Printf("[FNOS] 触发曲库重扫失败: %v", err)
	}

	// ⚠️ 按**返回后**计时，不是发起前。
	// 我们从不查询飞牛那边扫完没有（`ScanLibrary` 只发一个 POST、返回体也没有任务号），
	// 只能拿「请求往返结束」当近似 —— 至少不会在扫描可能还在跑的时候就把间隔算完。
	s.mu.Lock()
	s.lastRun = time.Now()
	s.writeStateLocked(s.lastRun)
	s.mu.Unlock()
}

// defaultRescan 进程级调度器
var defaultRescan = NewRescanScheduler(0, 0)

// ScheduleLibraryRescan 请求一次飞牛曲库重扫（合并 + 节流）。
//
// 在**下载/整理把文件写进音乐目录之后**调用。调用本身是立即返回的，
// 真正的扫描在 debounce 之后执行，不会阻塞调用方。
func ScheduleLibraryRescan() {
	defaultRescan.Schedule()
}

// ScheduleLibraryRescanNow 请求一次尽快执行的重扫（跳过最小间隔）。
//
// 用在用户手动触发的批量搬动之后（文件夹整理 apply / undo）：
// 正常 Schedule 可能要等 10 分钟才扫，用户会以为整理把歌弄丢了。
func ScheduleLibraryRescanNow() {
	defaultRescan.ScheduleNow()
}

// StopLibraryRescan 停止尚未执行的扫描（进程退出时调用）。
func StopLibraryRescan() {
	defaultRescan.Stop()
}

// SetRescanStatePath 把节流状态（上次实际扫描时间）落到数据目录。
//
// 由 server 在启动时调用一次。不调用也能跑，只是每次重启都会忘记上次扫过。
func SetRescanStatePath(path string) {
	defaultRescan.SetStatePath(path)
}
