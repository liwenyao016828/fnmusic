package intercept

// 「替换音质」扫描结果的**落盘**存储。
//
// # 为什么需要（用户 2026-11-21 的原话）
//
// 「当前不会保留扫描记录，每次都需要重新扫描；请优化并保留已扫描的歌曲的记录，
// 避免重复扫描同一首歌。」
//
// 原来的实现里扫描结果只活在内存（qualityScanState.status.Entries）：换个页面回来、
// 重启应用、甚至只是重扫一次，结论全没。而扫一首歌要问 2–3 条渠道（洛雪一条失败
// 要 8 秒 × 脚本数），一库几百首就是十几分钟外呼 —— 绝大多数歌上次的结论根本没变，
// 这些外呼是纯浪费。
//
// # 存什么、以什么为准
//
// 每条 = 一个曲库文件上一次探测的**结论**（各渠道档位、能不能换、盘上当时的档位）。
// 判据是「文件还在这儿 + 盘上档位没变 + 距今不超过 qualityScanFreshTTL」，
// 三条都满足才敢跳过它；任何一条不满足就重新探 —— 宁可多问一次，也不要拿旧结论
// 骗用户「这首换不了」。
//
// 文件名故意与 quality_records.json（替换**动作**的历史）分开：一个是「探过什么」，
// 一个是「换过什么」，混在一起两边都会被对方的清理逻辑影响。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// qualityScanFreshTTL 是「这条结论还能用多久」。7 天是个折中：
	// 音源站的曲库不是每天翻新，但也不能当永久真值（VIP 到期、下架都会变）。
	qualityScanFreshTTL = 7 * 24 * time.Hour
	// 落盘条数上限。扫描上限是 2000，留一倍余量；超出按 ScannedAt 淘汰最旧。
	qualityStoreMaxEntries = 5000
	// 每探到这么多条就落一次盘：既不必每首写一次文件，也不会因为进程被秒杀
	// 而把整轮结果全丢掉（真机上应用中心卸载/升级就是直接 SIGKILL）。
	qualityStoreFlushEvery = 20
)

// qualityScanFile 是 quality_scan.json 的形状（带版本号，方便以后迁移）。
type qualityScanFile struct {
	Version int            `json:"version"`
	SavedAt int64          `json:"saved_at"`
	Entries []QualityEntry `json:"entries"`
}

const qualityScanFileVersion = 1

// qualityScanStore 是扫描结论的落盘缓存。
type qualityScanStore struct {
	mu      sync.Mutex
	dir     string
	entries map[string]QualityEntry // path → 结论
	fresh   int                     // 距上次落盘新增的条数
	loaded  bool
}

func newQualityScanStore(dir string) *qualityScanStore {
	return &qualityScanStore{dir: strings.TrimSpace(dir), entries: map[string]QualityEntry{}}
}

func qualityScanPath(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "quality_scan.json")
}

// load 惰性读盘（失败当作「没有历史」，绝不阻塞扫描）。
func (s *qualityScanStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	p := qualityScanPath(s.dir)
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var f qualityScanFile
	if err := json.Unmarshal(b, &f); err != nil {
		return
	}
	for _, e := range f.Entries {
		if strings.TrimSpace(e.Path) == "" {
			continue
		}
		s.entries[e.Path] = e
	}
}

// snapshot 返回全部结论（可替换优先 + 路径排序，与界面上扫完的顺序一致）。
func (s *qualityScanStore) snapshot() []QualityEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	out := make([]QualityEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sortQualityEntries(out)
	return out
}

// lookup 读单条结论。
func (s *qualityScanStore) lookup(path string) (QualityEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	e, ok := s.entries[path]
	return e, ok
}

// newest 返回最近一次扫描时间（Unix 秒，0 = 从来没扫过）。
func (s *qualityScanStore) newest() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var max int64
	for _, e := range s.entries {
		if e.ScannedAt > max {
			max = e.ScannedAt
		}
	}
	return max
}

// put 记一条结论（并可能触发落盘）。
func (s *qualityScanStore) put(e QualityEntry) {
	if strings.TrimSpace(e.Path) == "" {
		return
	}
	if e.ScannedAt == 0 {
		e.ScannedAt = time.Now().Unix()
	}
	s.mu.Lock()
	s.load()
	// 超时的结果不覆盖已有的结论 —— 「没探出」不是「探出没有」，覆盖掉等于
	// 把一条有效结论抹成空白。保留旧的那条，它照样能免掉一次重扫。
	if old, ok := s.entries[e.Path]; ok && e.TimedOut && !old.TimedOut {
		s.mu.Unlock()
		return
	}
	s.entries[e.Path] = e
	s.trimLocked()
	s.fresh++
	flush := s.fresh >= qualityStoreFlushEvery
	if flush {
		s.fresh = 0
	}
	list := s.entriesLocked()
	s.mu.Unlock()
	if flush {
		_ = s.write(list)
	}
}

// drop 忘掉一条（文件被删/被移走时用）。
func (s *qualityScanStore) drop(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	delete(s.entries, path)
}

// flush 把内存里的结论落盘。
func (s *qualityScanStore) flush() {
	s.mu.Lock()
	s.load()
	s.fresh = 0
	list := s.entriesLocked()
	s.mu.Unlock()
	_ = s.write(list)
}

func (s *qualityScanStore) entriesLocked() []QualityEntry {
	out := make([]QualityEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out
}

// trimLocked 超上限时淘汰最旧的结论。
func (s *qualityScanStore) trimLocked() {
	if len(s.entries) <= qualityStoreMaxEntries {
		return
	}
	list := s.entriesLocked()
	sort.Slice(list, func(a, b int) bool { return list[a].ScannedAt < list[b].ScannedAt })
	drop := len(list) - qualityStoreMaxEntries
	for n := 0; n < drop; n++ {
		delete(s.entries, list[n].Path)
	}
}

// write 原子写盘（先写 .tmp 再 rename，与替换记录同一套范式）。
func (s *qualityScanStore) write(list []QualityEntry) error {
	p := qualityScanPath(s.dir)
	if p == "" {
		return nil
	}
	f := qualityScanFile{Version: qualityScanFileVersion, SavedAt: time.Now().Unix(), Entries: list}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// sortQualityEntries 统一排序口径：可替换优先，再按路径。
//
// 用户打开这一页最想看的是「能换的那批」，而不是文件系统遍历顺序（那在他眼里
// 就是随机的）。这里与 runQualityScan 收尾时的排序必须是同一套。
func sortQualityEntries(list []QualityEntry) {
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].Upgradeable != list[b].Upgradeable {
			return list[a].Upgradeable
		}
		return list[a].Path < list[b].Path
	})
}

// ── Interceptor 侧的口径 ──

// qualityEntryFileOk 判断「这条结论描述的那个文件还在、还是那个档位」。
//
// 这是**展示**的门槛：文件没了（被删/被移走）或者用户自己换过文件（档位变了），
// 旧的结论就不再描述现实，该丢。
func qualityEntryFileOk(e QualityEntry) bool {
	if strings.TrimSpace(e.Path) == "" {
		return false
	}
	if fi, err := os.Stat(e.Path); err != nil || fi.IsDir() {
		return false
	}
	return qualityCurrentTierOfFile(e.Path, e.Ext) == e.CurrentTier
}

// qualityEntryFresh 判断「这条历史结论还能不能用来跳过重扫」。
//
// 四条都满足才算新鲜：
//  1. 结论里带扫描时间，且距今 < qualityScanFreshTTL；
//  2. 文件还在（被删/被移走的不算）；
//  3. 盘上档位与结论里记的一致 —— 用户自己换过文件、或者上次替换成功过，
//     档位就变了，旧的「换不了/能换成 320k」都不再成立；
//  4. **不是超时的** —— 超时是「没探出」，不是「探出没有」，没有结论就
//     不能拿来免掉下次重探（否则一次网络抖动会让这首歌 7 天不再被查）。
func (i *Interceptor) qualityEntryFresh(e QualityEntry, now time.Time) bool {
	if e.ScannedAt <= 0 {
		return false
	}
	if e.TimedOut {
		return false
	}
	if now.Sub(time.Unix(e.ScannedAt, 0)) > qualityScanFreshTTL {
		return false
	}
	return qualityEntryFileOk(e)
}

// qualityStoreSnapshot 返回历史结论里**当前仍然有效**的那部分。
//
// 顺手把失效的丢掉：文件没了、档位变了、超过 7 天的，都不该继续占着界面。
func (i *Interceptor) qualityStoreSnapshot() []QualityEntry {
	if i.qualityStore == nil {
		return nil
	}
	all := i.qualityStore.snapshot()
	out := make([]QualityEntry, 0, len(all))
	for _, e := range all {
		// ⚠️ 这里**只**丢「文件没了/档位变了」的 —— 那些结论已经不描述现实了。
		//
		// 超时的、超过 7 天的都**留在列表里**：它们是「扫过但没结论」或「上次的
		// 结论」，界面上要看得见。用户抱怨过的正是「扫出来的歌莫名其妙全没了」
		// —— 少给结论可以（界面上会标明），把人扫过的歌藏起来不行。
		if !qualityEntryFileOk(e) {
			i.qualityStore.drop(e.Path)
			continue
		}
		out = append(out, e)
	}
	sortQualityEntries(out)
	return out
}
