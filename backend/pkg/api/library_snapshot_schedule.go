package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 定时快照（用户 2026-10-24：「增加定时快照和手动快照」）。
//
// 手动快照走 HandleMetaSnapshots 的 `{"op":"create"}`；这里是**按间隔自动拍**的那一份。
//
// ── 几个刻意的选择 ──
//
//  1. **默认关闭**。整库读标签是重活（几万首要几分钟、几百 MB），不打招呼就按天跑
//     会一点点吃掉磁盘，而且用户根本不知道为什么磁盘满了。开关在设置页。
//  2. **只拍索引里有的文件**。索引没扫过就是空的 → 直接跳过，不拍一份空快照。
//     （拍空快照更糟：用户会以为自己有备份。）
//  3. **失败不重试、不报警打扰**，只写一行日志等下一个间隔。快照是兜底手段，
//     它自己不应该是新的故障源。
//  4. **轮询而不是长定时器**。间隔是用户随时可改的配置，用 time.NewTicker 就得在
//     每次改配置时重建定时器；改成每 tick 读一次配置，改完下一分钟就生效。
const (
	// snapshotPollInterval 多久检查一次「到点没」。
	//
	// 不用一秒一次的粒度：定时快照的间隔是小时级，晚拍几分钟无所谓，
	// 但每分钟空转一次的成本可以忽略。
	snapshotPollInterval = time.Minute
	// snapshotSchedPrefix 定时快照的目录前缀 —— snapshotKind 靠它认出「定时」。
	snapshotSchedPrefix = "sched-"
)

// snapshotScheduler 是定时快照的调度器。
//
// 状态**全部落盘**（lastRun.json），进程重启不会「重启就立刻再拍一份」——
// 这一点与 fnos 重扫节流踩过的坑一样（见 pkg/fnos/rescan.go 的 SetStatePath）。
type snapshotScheduler struct {
	s     *Server
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	state string // 状态文件路径
}

func newSnapshotScheduler(s *Server) *snapshotScheduler {
	return &snapshotScheduler{
		s:     s,
		state: filepath.Join(s.aiBackupRoot, "last-scheduled.json"),
	}
}

// Start 启动调度（由 New 调用一次）。
func (d *snapshotScheduler) Start() {
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		// 启动时先等一会儿：进程刚起来时曲库索引可能还没加载完，
		// 这时候拍快照会以为是空库。
		if !d.sleep(2 * time.Minute) {
			return
		}
		for {
			d.tick()
			if !d.sleep(snapshotPollInterval) {
				return
			}
		}
	}()
}

// Stop 停止调度。
func (d *snapshotScheduler) Stop() {
	d.once.Do(func() {
		if d.stop != nil {
			close(d.stop)
			<-d.done
		}
	})
}

// sleep 睡一段时间，返回 false 表示期间被 Stop 打断。
func (d *snapshotScheduler) sleep(dur time.Duration) bool {
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-d.stop:
		return false
	case <-t.C:
		return true
	}
}

// tick 检查一次：开着吗？到点了吗？是就拍一份。
func (d *snapshotScheduler) tick() {
	cfg := d.s.cfgMgr.Get()
	if !cfg.SnapshotsScheduled() {
		return
	}
	interval := cfg.SnapshotInterval()
	if time.Since(d.lastRun()) < interval {
		return
	}
	// 先记时间再拍：拍的过程中进程被杀，也不会变成「每分钟都重拍一次」。
	// 代价是这一次失败要等下一个完整间隔 —— 比无限重试把磁盘写满强。
	d.markRun(time.Now())
	// 2026-10-27：定时快照改拍**元数据**（见 meta_snapshot.go）。
	// 旧实现把每个音频文件整份复制一遍，一库几十 GB，用户根本不敢开定时；
	// 而误操作改的只是标签，元数据快照几 MB 就够，还能随便留几十份。
	snap, saved, err := d.s.scheduledMetaSnapshot()
	if err != nil {
		log.Printf("[SNAPSHOT] 定时快照失败：%v", err)
		return
	}
	if !saved {
		log.Printf("[SNAPSHOT] 曲库元数据自上次快照以来没变（%d 首），本次跳过", snap.Count)
		return
	}
	log.Printf("[SNAPSHOT] 定时快照完成：%d 首的元数据", snap.Count)
	d.prune(cfg.SnapshotRetention())
}

// lastRun 读上一次成功（或尝试过）的拍摄时间；没有记录返回零值。
func (d *snapshotScheduler) lastRun() time.Time {
	raw, err := os.ReadFile(d.state)
	if err != nil {
		return time.Time{}
	}
	var st struct {
		LastRun int64 `json:"last_run"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.LastRun <= 0 {
		return time.Time{}
	}
	return time.Unix(st.LastRun, 0)
}

func (d *snapshotScheduler) markRun(t time.Time) {
	_ = os.MkdirAll(filepath.Dir(d.state), 0o755)
	raw := fmt.Sprintf(`{"last_run":%d}`, t.Unix())
	if err := os.WriteFile(d.state, []byte(raw), 0o600); err != nil {
		log.Printf("[SNAPSHOT] 记录拍摄时间失败：%v", err)
	}
}

// prune 只删**定时**快照里超出的最旧几份（保留份数由设置页决定）。
//
// 手动快照和「写文件前自动留的」一律不动：前者是用户亲手拍的决定要留着，
// 后者是补全/歌名修复的撤销依据 —— 删掉就等于把「撤回」按钮删了。
func (d *snapshotScheduler) prune(keep int) {
	if keep <= 0 {
		return
	}
	list := d.s.listMetaSnapshots()
	var mine []metaSnapshot
	for _, it := range list {
		if it.Kind == "scheduled" {
			mine = append(mine, it)
		}
	}
	// listMetaSnapshots 已经按时间倒序，超出 keep 的从尾部删
	if len(mine) <= keep {
		return
	}
	for _, it := range mine[keep:] {
		dir := filepath.Join(d.s.metaSnapshotRoot(), it.ID)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[SNAPSHOT] 清理旧定时快照 %s 失败：%v", it.ID, err)
		}
	}
}

// SnapshotScheduleStatus 给设置页用：当前开没开、间隔、保留份数、上次拍的时间。
func (s *Server) snapshotScheduleStatus() map[string]interface{} {
	cfg := s.cfgMgr.Get()
	last := ""
	if s.snapSched != nil {
		if t := s.snapSched.lastRun(); !t.IsZero() {
			last = t.Format(time.RFC3339)
		}
	}
	interval := cfg.SnapshotInterval()
	return map[string]interface{}{
		"enabled":        cfg.SnapshotsScheduled(),
		"interval_hours": int(interval / time.Hour),
		"keep":           cfg.SnapshotRetention(),
		"last_run":       last,
	}
}
