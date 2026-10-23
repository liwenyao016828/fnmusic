// Package backenddl 是「后端托管的下载队列」：把下载任务交给 NAS 上的后端去跑，
// 浏览器关掉也照样下完。
//
// **为什么需要它**：在此之前，下载的每一步都跑在浏览器里 —— 取直链靠页面里的
// 洛雪音源运行时，落盘是前端队列一条条调 `/api/download/song`。于是「下一批歌」
// 的实际含义是「这个标签页必须一直开着」。2026-10-23 用户明确要求：
// 「只能开网页挂着下载这个能帮我搞定吗？不必开着网页」。
//
// **它是唯一的队列执行者**（2026-10-23 架构定稿）：前端只负责把任务提交进来、
// 把状态展示出去；取链、落盘、重试、暂停/停止全部在这里。浏览器不再保留
// 第二条执行路径 —— 两套执行器同时跑，同一首歌会被下两遍，暂停也停不干净。
//
// **对账键是 client_id**：前端提交时带上自己的任务 id，后端原样存下并回传。
// 前端靠它把结果对回去，**不**靠歌名+歌手 —— 同名同歌手的不同录音（live /
// remix）会让「按名字对账」把结果写到错误的行上。歌名+歌手只剩一个弱兜底
// （前端没给 client_id 时，挡住「正在排队/正在下」的重复提交）。
package backenddl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 任务状态。
//
// paused / stopped 是**单条任务**的状态（用户对某一条点了暂停/停止）；
// 整个队列的暂停是 Queue.paused 这个布尔位（"暂停全部"），不是把每条改成 paused ——
// 否则「继续全部」无法区分「本来就被单独暂停的」和「被整队暂停的」。
const (
	StatusPending     = "pending"
	StatusDownloading = "downloading"
	StatusPaused      = "paused"
	StatusSuccess     = "success"
	StatusFailed      = "failed"
	StatusStopped     = "stopped"
)

// 跳过原因。前端要把它们分别显示成人话，所以必须是稳定的枚举值而不是错误串。
const (
	SkipDuplicate      = "duplicate"       // 同一个 client_id 已在队列里（回原 id 确认）
	SkipDuplicateTrack = "duplicate_track" // 同名同歌手（弱兜底，只在没给 client_id 时生效）
	SkipInvalid        = "invalid"         // 缺歌名（连搜都没法搜）/ 迁移状态不认识
	SkipNoChannel      = "no_channel"      // 没有任何可用渠道（由 API 层判定）
)

// Action 枚举（POST /api/download/backend/actions）。
//
// pause / resume 在**不带 ids** 时是整队暂停/继续；带 ids 时是单条。
const (
	ActionPause       = "pause"
	ActionResume      = "resume"
	ActionStop        = "stop"
	ActionCancel      = "cancel" // stop 的别名（前端历史上叫取消）
	ActionRetry       = "retry"
	ActionRetryFailed = "retry_failed"
	ActionRemove      = "remove"
	ActionClearDone   = "clear_done"
	ActionClearAll    = "clear_all"
)

// ErrUnknownAction 让 API 层回 400（而不是 500）：参数错不是服务器故障。
var ErrUnknownAction = errors.New("未知的队列动作")

// Item 是队列里的一项。
//
// 字段刻意与前端队列任务的最小交集对齐（歌名/歌手/专辑/时长/封面/平台 id/音质），
// 因为提交动作发生在浏览器里，而这些是浏览器手上就有的信息。
// 迁移历史记录时还会带上 status/error/path/actual_quality/size/added_at/updated_at，
// 见 Submit 的 migrate 参数。
type Item struct {
	ID string `json:"id"`
	// ClientID 是前端那条任务的 id（对账键，见包注释）。可以为空（外部程序
	// 直接调接口时没这个概念），为空就只能靠歌名+歌手弱兜底。
	ClientID string `json:"client_id,omitempty"`

	Name     string `json:"name"`
	Singer   string `json:"singer"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Cover    string `json:"cover"`
	// Platform / PlatformID 是**平台内的**身份（wy / tx / kg / kw + 平台 id）。
	// 有它就能走平台直链（纯 Go，不依赖任何外部进程）；没有就只能靠搜索。
	Platform   string `json:"platform,omitempty"`
	PlatformID string `json:"platform_id,omitempty"`
	// Quality 是用户点的目标档位（highest / flac / 320k / 128k…）。
	Quality string `json:"quality,omitempty"`

	Status string `json:"status"`
	Error  string `json:"error,omitempty"`

	Path          string `json:"path,omitempty"`
	ActualQuality string `json:"actual_quality,omitempty"`
	Source        string `json:"source,omitempty"`
	Size          int64  `json:"size,omitempty"`

	// Tries 是「这条被真正尝试过几次」，用来给 UI 区分「刚失败」与「一直失败」。
	Tries int `json:"tries"`

	AddedAt   int64 `json:"added_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Result 是一次成功取音的结果。
type Result struct {
	Path    string
	Quality string
	Source  string
	Size    int64
}

// Fetcher 是「取音 + 落盘」的能力，由 main.go 注入。
//
// 做成函数而不是让本包自己 import 取音那一坨：取音要连平台直链、洛雪宿主、
// 本机下载接口，那些依赖都在 main 里组装；这里只关心「给我一首歌，还我一个文件」。
type Fetcher func(ctx context.Context, it Item) (Result, error)

// Submitted 是一条被接受的任务（前端用它把 client_id 映射到后端 id）。
//
// Existing=true 表示「这条本来就在队列里，这次只是确认，没有新建/重排」。
// 前端据此把本地任务标成「已由后端接管」，**不要**再在浏览器里下一遍。
type Submitted struct {
	ClientID string `json:"client_id"`
	ID       string `json:"id"`
	Existing bool   `json:"existing,omitempty"`
}

// Skipped 是一条被跳过的任务与原因。
type Skipped struct {
	ClientID string `json:"client_id"`
	Reason   string `json:"reason"`
}

// Stats 给界面显示队列概况。paused 是整队暂停位（见 Queue.Paused）。
type Stats struct {
	Pending     int  `json:"pending"`
	Downloading int  `json:"downloading"`
	Paused      int  `json:"paused"`
	Success     int  `json:"success"`
	Failed      int  `json:"failed"`
	Stopped     int  `json:"stopped"`
	Total       int  `json:"total"`
	QueuePaused bool `json:"queue_paused"`
}

func (s *Stats) add(status string) {
	switch status {
	case StatusPending:
		s.Pending++
	case StatusDownloading:
		s.Downloading++
	case StatusPaused:
		s.Paused++
	case StatusSuccess:
		s.Success++
	case StatusFailed:
		s.Failed++
	case StatusStopped:
		s.Stopped++
	}
	s.Total++
}

// Queue 是串行的下载队列。
//
// **为什么串行**：一次取音会同时向上游打搜索与解析请求，洛雪脚本也不是为并发
// 写的（同一个脚本内部状态互相踩）。串行下 300 首慢一点，但不会把源打成 429，
// 也不会出现「同一首歌被两条任务同时下」。
type Queue struct {
	path  string
	fetch Fetcher
	logf  func(string, ...any)

	mu    sync.Mutex
	items []Item
	// paused 是「暂停全部」：true 时不再领取新的 pending，在途那条会继续跑完
	// （单条暂停/停止用 inflight 取消）。它是持久化的 —— 重启后不能自己继续跑。
	paused bool
	// next 是自增序号，只用于造 ID（ID 必须在本次进程内单调，重启后不能撞上
	// 已经落盘的老 ID —— 所以带上时间前缀，见 newIDLocked）。
	next uint64
	// inflight 让「暂停/停止某一条正在下载的任务」能真的把连接收掉，
	// 而不是只改个状态、worker 稍后回来又把状态覆盖掉。
	inflight map[string]context.CancelFunc

	wake     chan struct{}
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	// runCtx / cancel 让 Close 能**取消在途的取音**，而不是只关一个标志位
	// 让下载继续跑到超时。下载器本身认 ctx（见 pkg/downloader），所以取消
	// 会真的把连接收掉。
	runCtx context.Context
	cancel context.CancelFunc

	// started 只用于 Close 时判断要不要等 worker 退出。
	started bool

	// now 可替换，单测里用来钉住时间。
	now func() int64
}

// limitTerminal 是「历史记录」的上限。
//
// 成功后留这么多条是为了让用户回头看「刚才那批下到哪儿了」；无限留着会让
// 这个 JSON 越滚越大，而它每次状态变化都要整体重写一遍。
const limitTerminal = 200

// maxAutoRetries 是「进程退出时正在下」的那条自动重排队的上限。
//
// 只给它一次自动机会：卡死的任务（源一直超时）重启后自动重跑一次仍是失败，
// 就该停下来等用户点重试 —— 否则每次重启都白跑 5 分钟，用户看到的只是
// 「一直在转圈」。超过上限的标 failed 并写明原因，让「重试」按钮有的放矢。
const maxAutoRetries = 2

// New 创建队列。dataDir 是配置目录（与 backend_download_queue.json 同级）。
func New(dataDir string, fetch Fetcher, logf func(string, ...any)) *Queue {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		path:     filepath.Join(dataDir, "backend_download_queue.json"),
		fetch:    fetch,
		logf:     logf,
		inflight: map[string]context.CancelFunc{},
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		runCtx:   ctx,
		cancel:   cancel,
		now:      func() int64 { return time.Now().Unix() },
	}
	q.load()
	return q
}

type persisted struct {
	Items  []Item `json:"items"`
	Paused bool   `json:"paused,omitempty"`
}

// load 读盘。文件坏了就当空队列 —— 队列丢了只是列表空，不该让应用起不来。
//
// 恢复策略（必须与 HANDOVER 的口径一致）：
//   - pending：照常排队，worker 起来就会跑；
//   - downloading：上次进程死在半路，**没有人还在下它**。Tries 还没到上限的
//     重新排队（中断往往只是重启），到了上限的标 failed 并说明原因 ——
//     不能让一条坏任务每次开机都白跑一遍；
//   - paused / stopped / success / failed：原样保留，**绝不自动重启**
//     （用户停掉/暂停的任务，重启后又自己跑起来最让人恼火）；
//   - 空状态：按 pending 处理（手工编辑/老文件容错）。
func (q *Queue) load() {
	data, err := os.ReadFile(q.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			q.logf("[BACKEND-DL] 队列文件读不了，按空队列启动：%v", err)
		}
		return
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		// 这里必须用 Unmarshal 的 err：以前打的是 ReadFile 的 err（恒为 nil），
		// 于是「文件坏了」在日志里长得像「文件好好的」。
		q.logf("[BACKEND-DL] 队列文件解析失败，按空队列启动：%v", err)
		return
	}
	now := q.now()
	dirty := false
	for i := range p.Items {
		it := &p.Items[i]
		if it.ID == "" {
			q.next++
			it.ID = fmt.Sprintf("bd-%d-%d", now*int64(time.Second), q.next)
			dirty = true
		}
		switch it.Status {
		case StatusDownloading:
			dirty = true
			if it.Tries < maxAutoRetries {
				it.Status = StatusPending
				it.Error = "上次退出时还没下完，已重新排队"
			} else {
				it.Status = StatusFailed
				it.Error = "上次退出时还没下完（多次中断），点重试继续"
			}
			it.UpdatedAt = now
		case StatusPending, StatusPaused, StatusStopped, StatusSuccess, StatusFailed:
		default:
			it.Status = StatusPending
			it.UpdatedAt = now
			dirty = true
		}
	}
	q.mu.Lock()
	q.items = p.Items
	q.paused = p.Paused
	q.mu.Unlock()
	if dirty {
		q.mu.Lock()
		if err := q.saveLocked(); err != nil {
			q.logf("[BACKEND-DL] 恢复后的队列写盘失败（本次仍按内存状态跑）：%v", err)
		}
		q.mu.Unlock()
	}
	if p.Paused {
		q.logf("[BACKEND-DL] 队列处于「已暂停」状态，重启后不会自动开跑")
	}
}

// saveLocked 落盘。调用方必须已持锁。
//
// 整文件重写而不是追加：队列最多几百条，原子替换（写 tmp 再 rename）比维护
// 一份只增不减的日志简单得多，也不会出现「进程被杀留下半行 JSON」。
//
// **返回 error 是刻意的**：提交接口必须能区分「真的落盘了」与「只在内存里」。
// 以前是 void + 只打日志，于是磁盘满/目录只读时接口照样回成功，重启后任务
// 悄悄消失 —— 用户看到的是「我明明提交了，怎么没下」，且没有任何线索。
func (q *Queue) saveLocked() error {
	p := persisted{Items: q.items, Paused: q.paused}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("队列序列化失败：%w", err)
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("队列写盘失败：%w", err)
	}
	if err := os.Rename(tmp, q.path); err != nil {
		// 不留下半截 tmp：失败时清掉，免得下次排查时看到「有个 tmp 但没生效」。
		_ = os.Remove(tmp)
		return fmt.Errorf("队列替换失败：%w", err)
	}
	return nil
}

// Start 起后台 worker。重复调用是幂等的（第二次直接返回）。
func (q *Queue) Start() {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()
	go q.loop()
}

// Close 停 worker，并**取消在途取音**。
//
// 顺序：先 cancel（让下载器把连接收掉）再 close(stop)（让 loop 退出），
// 最后有限等待。等不到也不阻塞退出：状态已经落过盘，下次启动按
// load() 的恢复策略处理。
func (q *Queue) Close() {
	q.stopOnce.Do(func() {
		q.cancel()
		close(q.stop)
	})
	q.mu.Lock()
	started := q.started
	q.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-q.done:
	case <-time.After(10 * time.Second):
		q.logf("[BACKEND-DL] worker 未在 10s 内退出，先返回（状态已落盘）")
	}
}

func (q *Queue) wakeup() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) loop() {
	defer close(q.done)
	// 启动时先把上次没下完的 pending 捞起来（暂停位为真时 claimNext 会拒绝）。
	q.wakeup()
	for {
		select {
		case <-q.stop:
			return
		case <-q.wake:
		}
		for {
			it, ok := q.claimNext()
			if !ok {
				break
			}
			q.runOne(it)
			select {
			case <-q.stop:
				return
			default:
			}
		}
	}
}

// claimNext 取第一条 pending 并标成 downloading（原子地，避免两个 worker 抢同一条）。
// 整队暂停时直接返回 false（不领取新任务，在途的那条由自己的 ctx 跑完）。
func (q *Queue) claimNext() (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.paused {
		return Item{}, false
	}
	for i := range q.items {
		if q.items[i].Status != StatusPending {
			continue
		}
		q.items[i].Status = StatusDownloading
		q.items[i].Tries++
		q.items[i].Error = ""
		q.items[i].UpdatedAt = q.now()
		if err := q.saveLocked(); err != nil {
			// 落盘失败也继续下：文件本身是幂等的（同路径会走 upgrade_if_better），
			// 只是重启后可能重复跑一次。留日志，别让一条磁盘故障把整个队列停住。
			q.logf("[BACKEND-DL] 「开始下载」状态写盘失败（继续下）：%v", err)
		}
		// 返回的是副本：worker 拿着它去取音，不持有锁。
		it := q.items[i]
		return it, true
	}
	return Item{}, false
}

// runOne 跑一条：取音有总时长上限 —— 一条挂住不能把整条队列堵死。
func (q *Queue) runOne(it Item) {
	ctx, cancel := context.WithTimeout(q.runCtx, 5*time.Minute)
	defer cancel()

	// 登记取消句柄：暂停/停止某一条时靠它把连接收掉。登记在取音之前，
	// 保证「刚领取就被暂停」也能取消。
	q.mu.Lock()
	q.inflight[it.ID] = cancel
	q.mu.Unlock()

	res, err := q.fetch(ctx, it)

	q.mu.Lock()
	delete(q.inflight, it.ID)

	// 关进程时被取消：**不要**把它写成 failed —— 那不是这首歌的错，
	// 下次启动要按「上次退出时没下完」继续跑（见 load）。
	if q.runCtx.Err() != nil {
		q.mu.Unlock()
		q.logf("[BACKEND-DL] 关闭时中断：%s - %s", it.Singer, it.Name)
		return
	}

	idx := q.indexLocked(it.ID)
	if idx < 0 {
		q.mu.Unlock()
		// 下完之前被用户删了：文件留在盘上（删文件是另一件事，不能在用户
		// 只是「不想看到这条记录」时替他删歌）。
		return
	}
	// 单条取消（暂停/停止）：状态已经被 Apply 改掉了，这里**不能覆盖** ——
	// 否则「点了暂停，过一会儿又变成失败的」。
	if ctx.Err() != nil && (q.items[idx].Status == StatusPaused || q.items[idx].Status == StatusStopped) {
		q.mu.Unlock()
		q.logf("[BACKEND-DL] 已按用户操作取消：%s - %s（当前状态 %s）", it.Singer, it.Name, q.items[idx].Status)
		return
	}
	now := q.now()
	if err != nil {
		q.items[idx].Status = StatusFailed
		q.items[idx].Error = err.Error()
		q.items[idx].UpdatedAt = now
		q.logf("[BACKEND-DL] 失败：%s - %s：%v", it.Singer, it.Name, err)
	} else {
		q.items[idx].Status = StatusSuccess
		q.items[idx].Path = res.Path
		q.items[idx].ActualQuality = res.Quality
		q.items[idx].Source = res.Source
		q.items[idx].Size = res.Size
		q.items[idx].Error = ""
		q.items[idx].UpdatedAt = now
		q.logf("[BACKEND-DL] 完成：%s - %s → %s（%s）", it.Singer, it.Name, res.Path, res.Quality)
	}
	q.trimLocked()
	if err := q.saveLocked(); err != nil {
		// 文件已经下好了；这里丢了状态只会让重启后重下同一首（幂等），
		// 但要留下明确的日志，否则「下完了列表里没有」无从排查。
		q.logf("[BACKEND-DL] 结果写盘失败（文件已落盘，重启后可能重下）：%v", err)
	}
	q.mu.Unlock()
}

func (q *Queue) indexLocked(id string) int {
	for i := range q.items {
		if q.items[i].ID == id {
			return i
		}
	}
	return -1
}

// trimLocked 把「已结束」的历史裁到 limitTerminal 条，最旧的先扔。
func (q *Queue) trimLocked() {
	var terminal []int
	for i := range q.items {
		switch q.items[i].Status {
		case StatusSuccess, StatusFailed, StatusStopped:
			terminal = append(terminal, i)
		}
	}
	excess := len(terminal) - limitTerminal
	if excess <= 0 {
		return
	}
	// 按 UpdatedAt 从旧到新删（同刻按 ID 兜底，保证结果稳定可测）。
	sort.SliceStable(terminal, func(a, b int) bool {
		ia, ib := terminal[a], terminal[b]
		if q.items[ia].UpdatedAt != q.items[ib].UpdatedAt {
			return q.items[ia].UpdatedAt < q.items[ib].UpdatedAt
		}
		return q.items[ia].ID < q.items[ib].ID
	})
	drop := make(map[int]bool, excess)
	for _, idx := range terminal[:excess] {
		drop[idx] = true
	}
	kept := make([]Item, 0, len(q.items)-excess)
	for i, it := range q.items {
		if drop[i] {
			continue
		}
		kept = append(kept, it)
	}
	q.items = kept
}

// newIDLocked 造任务 id。调用方必须已持锁。
//
// 前缀带时间，是为了让「重启后新提交的任务」不会和盘上的老 id 撞车 ——
// q.next 每次重启都从 0 开始。
func (q *Queue) newIDLocked() string {
	q.next++
	return fmt.Sprintf("bd-%d-%d", time.Now().UnixNano(), q.next)
}

// Submit 提交一批任务。
//
// 语义（**幂等提交**，接口可以安全重试）：
//
//   - **client_id 是主键**。同一个 client_id：
//     还在 pending/downloading/paused → added 里回它原来的 id（Existing=true），
//     **不新建也不重排** —— 前端看到这个 id 就该停掉本地那条，否则浏览器和
//     后端会各下一遍（这正是「交接后本地又开始跑」的来源）；
//     已经 success/failed/stopped → 同样回原 id（Existing=true），状态**不动**。
//     网络超时导致的重试因此不会把已经下好的歌再下一遍；失败的重试走
//     `retry` 动作（显式意图），不靠重复提交。
//   - **force=true** 才允许把已结束（success/failed/stopped）的同 client_id 记录
//     **原地重排**（id 不变）：这是用户点「重新下载」的显式意图。
//   - 没给 client_id 时才用「歌名+歌手」做**弱兜底**：命中还在排队/在下的
//     那条就回它的 id（Existing=true）；force=true 则跳过这条兜底、新建一条。
//   - 缺歌名 → skipped(invalid)，整批照常处理其余条目。
//
// **migrate=true 是前端旧本地队列的一次性迁移**：此时 Item 里带的 status/
// error/path/actual_quality/size/added_at/updated_at 会被**原样保留**，
// 用来「不丢用户记录、也不让停掉的任务自己跑起来」：
//   - pending → pending（继续排队）；
//   - downloading → pending（浏览器关掉时它其实没在下，重排一次；与 load 同口径）；
//   - paused / stopped / failed / success → 原状态保留，**绝不自动开跑**；
//     其中 success 只是只读历史记录，不触发任何取音。
//   - 其它未知状态 → skipped(invalid)。
//
// 迁移对同一 client_id 也是幂等的：已经导入过的就回原 id 确认，不改状态。
//
// 持久化：内存状态先做成副本，**落盘成功才算数**；失败整体回滚并返回 err。
func (q *Queue) Submit(items []Item, force, migrate bool) (added []Submitted, skipped []Skipped, err error) {
	if len(items) == 0 {
		return nil, nil, nil
	}

	q.mu.Lock()
	// 失败时回滚到这份快照：调用方拿到 err 就该认为「一条都没进去」。
	snapshot := append([]Item(nil), q.items...)
	snapshotPaused := q.paused

	now := q.now()
	// client_id → 队列下标（同一 client_id 有多条历史时，优先拿「还在跑」的那条，
	// 否则拿最新的那条）。
	byClient := map[string]int{}
	for i := range q.items {
		cid := q.items[i].ClientID
		if cid == "" {
			continue
		}
		if old, ok := byClient[cid]; ok {
			if rankStatus(q.items[old].Status) > rankStatus(q.items[i].Status) {
				continue
			}
		}
		byClient[cid] = i
	}
	// 弱兜底：歌名+歌手 → 只登记「还在排队/在下」的那条的 id。
	byTrack := map[string]string{}
	for i := range q.items {
		if isActive(q.items[i].Status) {
			byTrack[trackKey(q.items[i])] = q.items[i].ID
		}
	}

	changed := false
	hasPending := false
	for _, it := range items {
		it.Name = strings.TrimSpace(it.Name)
		it.Singer = strings.TrimSpace(it.Singer)
		if it.Name == "" {
			skipped = append(skipped, Skipped{ClientID: it.ClientID, Reason: SkipInvalid})
			continue
		}

		// 迁移时先校验状态：不认识的状态不能被当成 pending 悄悄下起来
		// （例如老版本前端写过的 "error"、"cancelled"）。
		if migrate {
			if _, ok := migrateStatus(it.Status); !ok {
				skipped = append(skipped, Skipped{ClientID: it.ClientID, Reason: SkipInvalid})
				continue
			}
		}

		// ① client_id 主键：已在队列里就回原 id（确认），除非显式 force 重排终态。
		if it.ClientID != "" {
			if idx, ok := byClient[it.ClientID]; ok {
				old := &q.items[idx]
				if migrate || isActive(old.Status) || !force {
					added = append(added, Submitted{ClientID: it.ClientID, ID: old.ID, Existing: true})
					continue
				}
				// force && 已结束 → 原地重排（id 不变，前端手上的映射继续有效）。
				old.Status = StatusPending
				old.Error = ""
				old.Path, old.ActualQuality, old.Source, old.Size = "", "", "", 0
				old.Tries = 0
				old.UpdatedAt = now
				// 重新提交的应该是「最新的意图」：平台 id / 音质可能变了。
				old.Name, old.Singer, old.Album = it.Name, it.Singer, it.Album
				old.Duration, old.Cover = it.Duration, it.Cover
				old.Platform, old.PlatformID, old.Quality = it.Platform, it.PlatformID, it.Quality
				old.AddedAt = now
				byTrack[trackKey(*old)] = old.ID
				added = append(added, Submitted{ClientID: it.ClientID, ID: old.ID})
				hasPending = true
				changed = true
				continue
			}
		}

		// ② 弱兜底（只在没有 client_id 时，且不是迁移：迁移是「把旧记录搬过来」，
		// 有它自己的 id 语义，按歌名合并会丢记录）。
		if it.ClientID == "" && !force && !migrate {
			if id, ok := byTrack[trackKey(it)]; ok && id != "" {
				added = append(added, Submitted{ID: id, Existing: true})
				continue
			}
		}

		// ③ 新建。
		it.ID = q.newIDLocked()
		if migrate {
			status, _ := migrateStatus(it.Status)
			it.Status = status
			if status == StatusPending {
				if strings.TrimSpace(it.Error) != "" {
					// 迁移时 pending 上带的原因（例如「等页面打开时补下载」）多半
					// 已经过时，清掉；真正需要保留的是失败/停止的原因。
					it.Error = ""
				}
				hasPending = true
			}
			if it.Tries < 0 {
				it.Tries = 0
			}
		} else {
			it.Status = StatusPending
			it.Error = ""
			it.Path, it.ActualQuality, it.Source, it.Size = "", "", "", 0
			it.Tries = 0
			hasPending = true
		}
		if it.AddedAt == 0 {
			it.AddedAt = now
		}
		if it.UpdatedAt == 0 {
			it.UpdatedAt = it.AddedAt
		}
		byTrack[trackKey(it)] = it.ID
		q.items = append(q.items, it)
		added = append(added, Submitted{ClientID: it.ClientID, ID: it.ID})
		changed = true
	}

	if changed {
		q.trimLocked()
		if err := q.saveLocked(); err != nil {
			q.items = snapshot
			q.paused = snapshotPaused
			q.mu.Unlock()
			return nil, nil, err
		}
	}
	paused := q.paused
	q.mu.Unlock()
	if hasPending && !paused {
		q.wakeup()
	}
	return added, skipped, nil
}

// migrateStatus 把前端旧队列里的状态映射成本包的稳定枚举。
//
// downloading 归一到 pending：迁移发生在「浏览器那条执行路径已经关掉」的时刻，
// 旧队列里停在 downloading 的那条其实**没人在下**，重排一次才是对的。
// 空状态也按 pending（老数据结构没有 status 字段）。
func migrateStatus(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", StatusPending, StatusDownloading:
		return StatusPending, true
	case StatusPaused:
		return StatusPaused, true
	case StatusStopped:
		return StatusStopped, true
	case StatusFailed:
		return StatusFailed, true
	case StatusSuccess:
		return StatusSuccess, true
	}
	return "", false
}

// Apply 执行一个控制动作。返回受影响（真的改变）的条数。
//
// pause/resume 不带 ids 时作用于整队（暂停位）；其余动作必须给 ids。
// 单条暂停/停止会取消在途取音（ctx），并保留状态 —— worker 稍后回来看到
// 状态已是 paused/stopped 就不会覆盖。正在下载的歌**不可能**被标成 success：
// 取消后的半文件是下载器的 .part（可续传），不是成品。
func (q *Queue) Apply(action string, ids []string) (int, error) {
	q.mu.Lock()
	snapshot := append([]Item(nil), q.items...)
	snapshotPaused := q.paused
	now := q.now()

	// 需要收掉的连接：在锁里决定，出锁后再 cancel（cancel 本身不会阻塞，
	// 但先记账再统一调用，避免持锁时触发 fetch 的收尾回调）。
	var toCancel []context.CancelFunc
	byIDs := func(fn func(idx int) (bool, string)) int {
		hit := 0
		for _, id := range ids {
			idx := q.indexLocked(id)
			if idx < 0 {
				continue
			}
			ok, status := fn(idx)
			if !ok {
				continue
			}
			q.items[idx].Status = status
			q.items[idx].UpdatedAt = now
			hit++
		}
		return hit
	}

	var hit int
	switch action {
	case ActionPause:
		if len(ids) == 0 {
			if q.paused {
				q.mu.Unlock()
				return 0, nil
			}
			q.paused = true
			hit = 1
		} else {
			hit = byIDs(func(idx int) (bool, string) {
				switch q.items[idx].Status {
				case StatusPending:
					q.items[idx].Error = "已暂停（点继续恢复）"
					return true, StatusPaused
				case StatusDownloading:
					if c, ok := q.inflight[q.items[idx].ID]; ok {
						toCancel = append(toCancel, c)
					}
					q.items[idx].Error = "已暂停（点继续恢复；已下载的部分会保留为可续传的临时文件）"
					return true, StatusPaused
				}
				return false, ""
			})
		}

	case ActionResume:
		if len(ids) == 0 {
			if !q.paused {
				q.mu.Unlock()
				return 0, nil
			}
			q.paused = false
			hit = 1
		} else {
			hit = byIDs(func(idx int) (bool, string) {
				switch q.items[idx].Status {
				case StatusPaused, StatusStopped, StatusFailed:
					q.items[idx].Error = ""
					return true, StatusPending
				}
				return false, ""
			})
		}

	case ActionStop, ActionCancel:
		hit = byIDs(func(idx int) (bool, string) {
			switch q.items[idx].Status {
			case StatusPending, StatusPaused, StatusDownloading:
				if c, ok := q.inflight[q.items[idx].ID]; ok {
					toCancel = append(toCancel, c)
				}
				q.items[idx].Error = "已停止（点重试继续）"
				return true, StatusStopped
			}
			return false, ""
		})

	case ActionRetry:
		hit = byIDs(func(idx int) (bool, string) {
			switch q.items[idx].Status {
			case StatusFailed, StatusStopped, StatusPaused:
				q.items[idx].Error = ""
				return true, StatusPending
			}
			return false, ""
		})

	case ActionRetryFailed:
		hit = byIDs(func(idx int) (bool, string) {
			if q.items[idx].Status == StatusFailed {
				q.items[idx].Error = ""
				return true, StatusPending
			}
			return false, ""
		})

	case ActionRemove:
		remove := map[string]bool{}
		for _, id := range ids {
			idx := q.indexLocked(id)
			if idx < 0 || q.items[idx].Status == StatusDownloading {
				// 正在下的不能删：先 stop（会取消在途），再删。
				continue
			}
			remove[id] = true
		}
		if len(remove) > 0 {
			kept := q.items[:0]
			for _, it := range q.items {
				if remove[it.ID] {
					hit++
					continue
				}
				kept = append(kept, it)
			}
			q.items = kept
		}

	case ActionClearDone:
		kept := q.items[:0]
		for _, it := range q.items {
			if it.Status == StatusSuccess {
				hit++
				continue
			}
			kept = append(kept, it)
		}
		q.items = kept

	case ActionClearAll:
		// 只清终态：success / failed / stopped。paused、pending、downloading 不动 ——
		// 「清空完成记录」不该把用户还在排队的任务一起吞掉。
		kept := q.items[:0]
		for _, it := range q.items {
			switch it.Status {
			case StatusSuccess, StatusFailed, StatusStopped:
				hit++
				continue
			}
			kept = append(kept, it)
		}
		q.items = kept

	default:
		q.mu.Unlock()
		return 0, ErrUnknownAction
	}

	if hit == 0 {
		q.mu.Unlock()
		return 0, nil
	}
	if err := q.saveLocked(); err != nil {
		q.items = snapshot
		q.paused = snapshotPaused
		q.mu.Unlock()
		return 0, err
	}
	paused := q.paused
	q.mu.Unlock()

	for _, c := range toCancel {
		c()
	}
	// 解除暂停/重试后要有机会立刻开跑（否则要等下一次提交）。
	if !paused {
		q.wakeup()
	}
	return hit, nil
}

// Get 返回一条任务的快照。
func (q *Queue) Get(id string) (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	idx := q.indexLocked(id)
	if idx < 0 {
		return Item{}, false
	}
	return q.items[idx], true
}

// Wait 等某条任务进入终态（success/failed/stopped），供订阅（monitor）用：
// 订阅只有在后端真的落盘后才把它标成「已下载」，失败就留在可重试状态。
//
// 返回当前快照；bool=false 表示没等到（ctx 超时/队列里已经没有这条）。
func (q *Queue) Wait(ctx context.Context, id string) (Item, bool) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		q.mu.Lock()
		idx := q.indexLocked(id)
		if idx >= 0 {
			it := q.items[idx]
			if isTerminal(it.Status) {
				q.mu.Unlock()
				return it, true
			}
		} else {
			q.mu.Unlock()
			return Item{}, false
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return Item{}, false
		case <-q.stop:
			return Item{}, false
		case <-t.C:
		}
	}
}

// List 返回队列快照（**最新加入的在前**）：界面第一眼看到的是刚提交的那批。
func (q *Queue) List() []Item {
	q.mu.Lock()
	out := make([]Item, len(q.items))
	copy(out, q.items)
	q.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AddedAt != out[j].AddedAt {
			return out[i].AddedAt > out[j].AddedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Stats 返回队列概况。
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	var s Stats
	for i := range q.items {
		s.add(q.items[i].Status)
	}
	s.QueuePaused = q.paused
	return s
}

// Paused 报告整队是否处于「暂停全部」。
func (q *Queue) Paused() bool { return q.isPaused() }

func (q *Queue) isPaused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.paused
}

func isActive(status string) bool {
	switch status {
	case StatusPending, StatusDownloading, StatusPaused:
		return true
	}
	return false
}

func isTerminal(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusStopped:
		return true
	}
	return false
}

// rankStatus 用于「同一 client_id 有多条历史时该认哪条」：在跑的优先，
// 其次失败/停止（用户可能想重试），最后才是成功的历史记录。
func rankStatus(status string) int {
	switch status {
	case StatusDownloading:
		return 5
	case StatusPending:
		return 4
	case StatusPaused:
		return 3
	case StatusFailed, StatusStopped:
		return 2
	default:
		return 1
	}
}

func trackKey(it Item) string {
	return strings.TrimSpace(it.Name) + "\x00" + strings.TrimSpace(it.Singer)
}
