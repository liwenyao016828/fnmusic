package online

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Downloaded 记录「哪些在线曲目已经落到本地了」。
//
// # 它解决什么问题
//
// 在线曲目在官方那边是一条**虚拟 guid** 的记录（曲率自己造的 id），收藏、歌单、
// 播放历史里存的就是这个 id。用户收藏之后我们后台把整轨下到 NAS —— 但那条收藏
// 记录仍然指向虚拟 guid，取流还是走在线代理。
//
// 有了这张表，取流/元数据见到「已登记的虚拟 guid」时**直接服务本地文件**：
// 收藏条目继续可用，但已经是在听硬盘上的那份。这就是「收藏自动绑定本地」在曲率
// 这边的含义 —— **不需要回写官方库，也不依赖重扫**（官方库那边根本没这条记录，
// 想回写也回写不了）。
//
// # 为什么不放内存
//
// 进程重启后必须还记得 —— 否则收藏里的歌会「突然又变回在线流」，而文件明明已经
// 在库里的。落盘位置与在线登记表同目录（`<dataDir>/online/downloaded.json`），
// 与 favorites/playlists/history 那几张表并列。
type Downloaded struct {
	mu    sync.RWMutex
	path  string
	items map[string]DownloadedItem
}

// DownloadedItem 是一条「已落本地」记录。
type DownloadedItem struct {
	// GUID 是曲率发出去的虚拟 id（收藏/歌单/历史里存的那个）。
	GUID string `json:"guid"`
	// Path 是本地文件的**绝对路径**。
	Path string `json:"path"`
	// Title / Artist 只用于排障与界面显示（取流不靠它们）。
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// Source / Quality 是**实际取音**的平台与音质档位。
	//
	// ⚠️ 它们**不一定**等于曲目自己平台的：下载会先去「下载源池」里找同名的高音质
	// 版本（见 pkg/intercept/acquire.go），找到就换源。界面要如实告诉用户
	// 「这首歌是从哪儿、以什么音质下来的」—— 只显示原平台是在骗人。
	Source  string `json:"source,omitempty"`
	Quality string `json:"quality,omitempty"`
	// Size 是落盘字节数；0 = 当时没拿到。
	Size int64 `json:"size,omitempty"`
	// At 是登记时间（Unix 秒）。
	At int64 `json:"at"`
}

// NewDownloaded 打开（或新建）一张已下载登记表。dir 不存在会创建。
func NewDownloaded(dir string) (*Downloaded, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	d := &Downloaded{path: filepath.Join(dir, "downloaded.json"), items: map[string]DownloadedItem{}}
	raw, err := os.ReadFile(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil // 第一次跑，空表
		}
		return nil, err
	}
	var list []DownloadedItem
	if err := json.Unmarshal(raw, &list); err != nil {
		// 坏文件不当致命错误：丢掉它重新开始，总比整个在线功能起不来强。
		// （内容只是「哪些已下载」，丢了最坏结果是那几首又走在线流。）
		return d, nil
	}
	for _, it := range list {
		if it.GUID != "" {
			d.items[it.GUID] = it
		}
	}
	return d, nil
}

// Path 返回登记表的落盘路径（排障用）。
func (d *Downloaded) Path() string { return d.path }

// Get 查一条。
func (d *Downloaded) Get(guid string) (DownloadedItem, bool) {
	if d == nil {
		return DownloadedItem{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	it, ok := d.items[guid]
	return it, ok
}

// All 返回全部记录（按登记时间倒序）。
func (d *Downloaded) All() []DownloadedItem {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	out := make([]DownloadedItem, 0, len(d.items))
	for _, it := range d.items {
		out = append(out, it)
	}
	d.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Put 登记一条并落盘。
//
// ⚠️ 只登记**文件真的存在**的记录：写进去的每一条都意味着「取流会去读这个路径」，
// 登记一个不存在的路径 = 收藏里那首歌直接 404。
func (d *Downloaded) Put(it DownloadedItem) error {
	if d == nil || it.GUID == "" {
		return nil
	}
	if it.Path == "" {
		return nil
	}
	if _, err := os.Stat(it.Path); err != nil {
		return err
	}
	if it.At == 0 {
		it.At = time.Now().Unix()
	}
	d.mu.Lock()
	d.items[it.GUID] = it
	err := d.flushLocked()
	d.mu.Unlock()
	return err
}

// Remove 摘掉一条（文件被删/用户取消时用）。
func (d *Downloaded) Remove(guid string) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.items[guid]; !ok {
		return nil
	}
	delete(d.items, guid)
	return d.flushLocked()
}

// flushLocked 原子落盘（先写 .tmp 再 rename）。
//
// 原子写是必须的：这个文件在**下载完成后**被写，而那时进程可能正被重启
// （升级）—— 半截 JSON 会让下次启动读到一张坏表。
func (d *Downloaded) flushLocked() error {
	list := make([]DownloadedItem, 0, len(d.items))
	for _, it := range d.items {
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].At < list[j].At })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}
