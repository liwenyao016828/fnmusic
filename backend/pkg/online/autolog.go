package online

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// AutoLog 是「自动下载记录」：由**飞牛音乐页面里的操作**触发的那批下载的逐条流水。
//
// # 用户口径（2026-10-07 指令）
//
// 「将下载记录展示在任务状态，该任务可以单独增加页面（比如自动下载记录等标题）。」
//
// # 为什么必须由后端记，不能复用网页端的下载队列
//
// 网页端的下载队列（`frontend/src/services/downloadManager.js`）记的是「用户**在曲率
// 这个页面上**排队下的那些」。而收藏 / 加入歌单触发的下载发生在**飞牛官方页面**上，
// 曲率前端根本不知道它存在 —— 用户点完红心，歌在后台下来了，队列里却一行都没有。
// 这正是用户报的那个现象：「收藏后下载了，但看不见记录」。
//
// # 与 downloaded.json 的分工
//
//	downloaded.json  「哪些虚拟 guid 已经落到本地了」—— 一 guid 一行、覆盖写，
//	                 取流时据此改走本地文件。**失败不会进它**。
//	autodownload_log 「每一次动作」—— 追加写、有上限，成功与失败都记。
//
// 排障要看的恰恰是失败那几次，所以两张表不能合并成一张。
//
// # 落盘位置
//
// 与在线登记表同目录（`<dataDir>/autodownload_log.json`，`dataDir` 就是 online 那层），
// 跟 favorites/playlists/history/downloaded 并列 —— 备份时一个目录带走全部在线数据。
type AutoLog struct {
	mu   sync.Mutex
	path string
	// items 按时间**倒序**存（新的在前）：列表页要的就是最近几条，
	// 存成正序的话每次读都要整体反转一遍。
	items []AutoLogEntry
}

// 自动下载的触发来源。它们写进记录、也由界面翻译成人话，所以是**表 schema 的
// 一部分** —— 读写两侧必须用同一套字符串（在 pkg/intercept 里各写一份迟早会漂）。
const (
	// TriggerFavorite：在飞牛音乐里点了收藏。
	TriggerFavorite = "fav"
	// TriggerPlaylist：在飞牛音乐里加进了歌单。
	TriggerPlaylist = "playlist"
	// TriggerPageClick：在飞牛页面里点了「下载到曲率」。
	TriggerPageClick = "page"
	// TriggerUpgrade：后台「音质补强」轮重下（不是用户动作，见 pkg/intercept/upgrade_sweeper.go）。
	//
	// 为什么单独一个来源：补强失败与收藏失败在用户眼里是两件事 —— 前者不该让他以为
	// 「我收藏了却没下下来」，它只是「这首的无损版还没搞到」。记录里分得开，界面才说得出话。
	TriggerUpgrade = "upgrade"
)

// AutoLogEntry 是一条自动下载记录。
type AutoLogEntry struct {
	// ID 是「时间戳 + 序号 + 内容哈希」拼出来的短 id，供多选删除用。
	//
	// 为什么不复用 guid：同一首歌可能被触发多次（先收藏、后又加进歌单），
	// 那是两条记录；用 guid 当 id 会让「删掉一条」连带删掉另一条。
	ID string `json:"id"`
	// At 是这次动作**结束**的时间（Unix 秒）。用结束时间而不是开始时间：
	// 用户来查记录时问的是「刚才那次下了没」，看到的应该是它什么时候出结果的。
	At int64 `json:"at"`
	// Trigger 是触发来源：fav（收藏）/ playlist（加入歌单）/ page（飞牛页面里
	// 点「下载到曲率」）。用户要分辨的就是这三种 —— 「我没点下载啊」和
	// 「我点了但它没记」是完全不同的两件事。
	Trigger string `json:"trigger"`
	// GUID 是曲率发出去的虚拟 id（可能为空：解析阶段就失败时还没有 guid）。
	GUID   string `json:"guid,omitempty"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// Source / Quality 是**实际取音**的平台与档位。可能是下载源池换过的源、
	// 也可能是兜底档位（点名要无损但只有 320k）—— 界面据此如实显示，
	// 不能拿「原平台 / 想要的档位」糊弄。
	Source  string `json:"source,omitempty"`
	Quality string `json:"quality,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Path    string `json:"path,omitempty"`
	// OK 是这次动作的结论。失败的那几条才是用户真正要看的，
	// 所以 Error 必须带上人能读懂的原因（不是「失败」两个字）。
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// autoLogLimit 是这张表的上限。
//
// 500 条 ≈ 几十次收藏动作，足够回答「刚才那首下了没」；再多也不会有人翻。
// 超了丢最旧的（表是倒序的，所以是丢**尾部**）。
const autoLogLimit = 500

// NewAutoLog 打开（或新建）自动下载记录表。dir 不存在会创建。
func NewAutoLog(dir string) (*AutoLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &AutoLog{path: filepath.Join(dir, "autodownload_log.json")}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil // 第一次跑，空表
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &l.items); err != nil {
		// 坏文件不当致命错误：丢掉它重新开始，总比整个在线功能起不来强。
		// 内容只是「历史流水」，丢了最坏结果是记录页空着。
		l.items = nil
	}
	return l, nil
}

// Append 追加一条记录并落盘。
//
// 落盘失败**只吞掉不报错**：这条记录是「收藏」的附加产物，写不进去不该让
// 收藏本身看起来失败（与 autoDownload 的失败处理同一个口径）。
// 但会在返回值里说出来，让调用方记进日志。
func (l *AutoLog) Append(e AutoLogEntry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.At <= 0 {
		e.At = time.Now().Unix()
	}
	if e.ID == "" {
		e.ID = autoLogID(e)
	}
	// 新的在前。同一秒内的多条靠 append 顺序区分，不排序 —— 排序会把
	// 「同一秒里先成功后失败」的顺序打乱，而那个顺序正是排障要看的。
	l.items = append([]AutoLogEntry{e}, l.items...)
	if len(l.items) > autoLogLimit {
		l.items = l.items[:autoLogLimit]
	}
	return l.flushLocked()
}

// List 返回最多 limit 条记录（新的在前）。limit<=0 用默认 200。
func (l *AutoLog) List(limit int) []AutoLogEntry {
	if l == nil {
		return nil
	}
	if limit <= 0 {
		limit = 200
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.items)
	if n > limit {
		n = limit
	}
	out := make([]AutoLogEntry, n)
	copy(out, l.items[:n])
	return out
}

// Stats 汇总「需要用户处理」的那几项：失败条数与总数。
//
// 只给两个数：界面上那句「自动下载 · 失败 2」要的就是这两个，
// 多给一个用不上的数字只会让人猜它是什么意思。
func (l *AutoLog) Stats() (total int, failed int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, it := range l.items {
		if !it.OK {
			failed++
		}
	}
	return len(l.items), failed
}

// Remove 按 id 删除若干条；ids 为空时**全部清空**。
//
// 返回实际删掉的条数：界面据此说「已清除 N 条」—— 说「已清除」却一条没动
// 比不提示更让人困惑。
func (l *AutoLog) Remove(ids []string) (int, error) {
	if l == nil {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(ids) == 0 {
		n := len(l.items)
		l.items = nil
		return n, l.flushLocked()
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = trimSpaceASCII(id); id != "" {
			drop[id] = true
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	kept := make([]AutoLogEntry, 0, len(l.items))
	removed := 0
	for _, it := range l.items {
		if drop[it.ID] {
			removed++
			continue
		}
		kept = append(kept, it)
	}
	if removed == 0 {
		return 0, nil
	}
	l.items = kept
	return removed, l.flushLocked()
}

// flushLocked 写盘。调用方必须已持锁。
func (l *AutoLog) flushLocked() error {
	raw, err := json.MarshalIndent(l.items, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名：中途崩了不会留下半截 JSON（下一轮读进来就是空表，
	// 而空表看起来像「记录全没了」—— 原子替换把这种可能直接去掉）。
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// autoLogID 生成记录 id：`<时间戳>-<序号>-<内容哈希前 8 位>`。
//
// 为什么要内容哈希：同一秒里连着下三首同名歌是完全可能的（歌单里同一首出现两次），
// 只用时间戳会撞号，而撞号的表现是「删一条删掉了两条」。
func autoLogID(e AutoLogEntry) string {
	h := sha1.Sum([]byte(e.Trigger + "\x00" + e.GUID + "\x00" + e.Title + "\x00" + e.Artist))
	seq := atomicSeq()
	return strconv.FormatInt(e.At, 10) + "-" + strconv.FormatInt(seq, 10) + "-" + hex.EncodeToString(h[:4])
}

// 记录 id 里的序号。进程内自增即可 —— 它只用来区分同一秒内的多条。
var (
	seqMu  sync.Mutex
	seqVal int64
)

func atomicSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqVal++
	return seqVal
}

// SortByAtDesc 按时间倒序排（给调用方在合并多个来源时用）。
func SortByAtDesc(items []AutoLogEntry) {
	sort.SliceStable(items, func(a, b int) bool { return items[a].At > items[b].At })
}

// trimSpaceASCII 去掉首尾的空白字符（含全角空格）。
//
// 单独写一个而不是用 strings.TrimSpace：id 是十六进制串，只可能夹着普通空白；
// 而这里刻意**不**接受全角空格 —— 它出现在 id 里一定是复制粘贴出的错，
// 当成有效 id 只会让人以为「点了删除没反应」。
func trimSpaceASCII(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// ErrAutoLogDisabled 表示这次运行没有开自动下载记录（构造失败时降级）。
//
// 单独一个错误而不是让调用方判 nil：接口层要据此区分「没有记录」（空列表）
// 与「这个功能没起来」（明说一句），两者在界面上长得一样会让人以为功能坏了。
var ErrAutoLogDisabled = fmt.Errorf("自动下载记录未启用")
