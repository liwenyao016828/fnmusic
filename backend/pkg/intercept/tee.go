package intercept

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/online"
)

// 「边听边下」（tee）：播放在线曲目时，把**同一条流**顺手写进磁盘，之后这首歌的
// 取流自动走本地。
//
// # 为什么值得做
//
// 这是唯一一种「不多花一分钱带宽」的下载：字节本来就从这儿过。参考实现里对应
// `_full_fetch_download` + `_promote_cached_to_library`。
//
// # 两条路径，由开关决定走哪条（互不污染）
//
//   - **开关开着** → 落进曲库（`<下载目录>/<歌手> - <标题>.<ext>`）+ 登记表登记，
//     重播走「收藏自动绑定本地」那条既有通路。这条路径已经真机验收过，行为不要改。
//   - **开关关着** → 只留**滚动试听缓存**（`<数据目录>/rolling-cache/<虚拟id>.<ext>`）：
//     最近 `teeRollingKeep` 首留在本地，重播直接喂文件、不出网；但它**不进曲库、
//     不进登记表**，被淘汰就是没了 —— 开关关着的语义仍然是「不留永久曲库副本」，
//     滚动缓存只是临时试听缓存。参考实现对应 `_tee_finalize` 的 else 分支
//     （`proxy/app.py:3866-3882`）与 `purge_rolling`（`proxy/cache_gc.py:63`）。
//
// 两条路径的共同点（也是本文件存在的理由）：都搭的是客户端正在听的那条流。
//
// # 被切歌打断的那半截（两条路径在这里**分叉**）
//
// 客户端中途走了（切歌 / 关页面 / 手机切网）时：
//
//   - **开关开着**（曲库路径）→ 服务端自己接着把这首歌拉完（`startTeeHandoff`
//     → `teeHandoffResume`），然后走**同一条**提升→登记→补标签的路。目标是
//     「用户切歌走了，那首歌照样补完进曲库」。参考实现同款（`proxy/app.py:4091`）。
//   - **开关关着**（滚动缓存）→ 半截**直接丢掉**。那只是最近 5 首的临时试听缓存，
//     为它起后台下载等于把「不留永久曲库副本」偷换成「偷偷帮你下整曲」。
//
// 续传失败（源不给 Range、报错、长度对不上、超时）**就是失败**：半截丢掉，
// 行为与没有这个功能时完全一样 —— 见 `resumeInto` 与 `teeHandoffResume`。
//
// # 两条硬规矩
//
//  1. **播放优先**：写盘失败绝不能影响听歌 —— 先写给客户端，再写盘；盘出错就把
//     文件句柄丢掉继续播（见 `teeWriter.Write`）。tee 是搭便车的，不是主角。
//     后台续传同样受这条管：它在**另一个** goroutine 里跑，不碰播放那条流，
//     也不让写客户端这件事多等一步（见 `teePool`）。
//  2. **只提升完整文件**：半截文件进了曲库比没有更糟 —— 它会一直播到一半停，
//     而用户以为已经下好了。所以必须「拿到的字节数 == 上游声明的总长」才提升。
//     这条对滚动缓存同样成立：缓存一个半截文件会让「重播不出网」变成
//     「重播播到一半停」。⚠️ 后台续传**没有**放宽这条：续传回来的字节最后仍然
//     是拿**磁盘上的真实大小**去跟「上游声明的总长」比（见 `finishTee`），
//     少一个字节都还是丢掉。
const teeSubdir = ".qulv-tee"

// teeRollingSubdir 是滚动试听缓存的目录名，落在**数据目录**下（与 favorites /
// play_history / downloaded.json 那几张表并列）。
//
// ⚠️ **刻意不放在下载目录里**，这是「开关关着 = 不留永久曲库副本」那条红线的
// 结构性保证：下载目录就是飞牛的曲库目录，放进去（哪怕藏在 `.` 子目录里）就有
// 被曲库扫描当成真曲目的风险 —— 那正是这条红线要防的事。参考实现同样把滚动缓存
// 放在自己的 cache_dir，而不是曲库目录。
const teeRollingSubdir = "rolling-cache"

// teeRollingKeep 是滚动试听缓存保留的曲目数上限（按**最近使用**淘汰，不是按写入）。
//
// 定 5 的理由：
//   - 要覆盖的用法是「刚才那首再听一遍 / 前后几首来回听 / 小歌单循环」，
//     只留 1 首（当前曲目）或 2 首（加个上一首）都嫌紧；
//   - 它必须是有限值：这是**临时**缓存，不能长成第二个曲库。5 首 320k 约 50MB，
//     就算整轨无损也在 150MB 量级 —— 对应用数据盘是可控的常数。
//
// 0 的语义是「一条不留」（见 `purgeRolling`），所以把它改成 0 就等于关掉滚动缓存。
const teeRollingKeep = 5

// teeRollingExts 是滚动缓存可能出现的扩展名（查缓存时要逐个试）。
//
// 唯一写入方是 `finishRolling`（用 `extForFormat`），两边必须同源；
// `TestRollingExtsMatchExtForFormat` 钉住这件事。
var teeRollingExts = []string{".mp3", ".flac", ".m4a", ".wav", ".ape"}

// teeEnrichTimeout 是「补标签」的预算：它要抓歌词与封面（各一次外部请求），
// 比下载短得多，但也不能让它挂住 —— 失败只是少几个标签，不影响已下好的歌。
const teeEnrichTimeout = 45 * time.Second

// teeTarget 是一次 tee 的现场。
type teeTarget struct {
	// f 是暂存文件的写句柄。两种情况下会是 nil：
	//   - `teeWriter.Write` 写盘失败时把它丢掉（之后只转发，不再碰盘）；
	//   - 半截被交接给后台续传（见 `startTeeHandoff`：先关句柄再交接，这样
	//     「谁在写这个文件」任何时刻只有一个答案），续传收尾时会包一个
	//     `f == nil` 的现场交给 `finishTee`。
	// 所以 `finishTee` 关句柄前必须先判空。
	f    *os.File
	part string
	// rolling 为 true 表示这份字节的归宿是**滚动试听缓存**（开关关着那条路径），
	// 为 false 表示归宿是曲库（开关开着那条路径，见 finishTee）。
	rolling bool
	// wrote 是**已经成功写进这个文件**的字节数（不是「收到了多少」）。
	wrote int64
}

// teeOn 报告开关（现取，改配置立刻生效）。
func (i *Interceptor) teeOn() bool {
	return i.cfg.TeeEnabled != nil && i.cfg.TeeEnabled()
}

// teeDir 返回 tee 的暂存目录。
//
// 刻意放在**下载目录下面**的隐藏子目录里：与最终落点在同一个文件系统上，
// 「提升」就是一次 rename，不用跨设备搬几十兆字节。
func (i *Interceptor) teeDir() string {
	if i.cfg.DownloadDir == nil {
		return ""
	}
	dir := strings.TrimSpace(i.cfg.DownloadDir())
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, teeSubdir)
}

// teeRollingDir 返回滚动试听缓存的落点（`<数据目录>/rolling-cache`）。
//
// 数据目录拿不到（理论上不会）时返回空串 = 这条路径整体退化掉，
// 表现只是「重播还要出网」，与以前一致。
func (i *Interceptor) teeRollingDir() string {
	dir := strings.TrimSpace(i.cfg.DataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, teeRollingSubdir)
}

// beginTee 为一次完整转发开一条落盘的路。返回 nil = 这次不落盘。
//
// 不落盘的几种情形，每一种都有理由（两条路径**共用**这批判断）：
//   - 没有登记表 / 没有虚拟 id —— 无从下手；
//   - 已经在库里 —— 别再存一份（重播本来就走本地）；
//   - 不是 200（206 分片）或是 HEAD —— 拼不成完整文件；
//   - 客户端要的是**中段** Range（拖动/续传）—— 同理；
//   - 上游没给 Content-Length —— 就没法判断「完整」，宁可不存。
//
// 开关决定的是**归宿**：开着进曲库暂存目录，关着进滚动缓存目录。
// 开关开着但下载目录不可用时**不**退化成滚动缓存 —— 那会让「开着」这条已经
// 真机验收过的路径凭空多出新的写盘行为，两条路径必须保持互不污染。
func (i *Interceptor) beginTee(fake string, t online.Track, resp *http.Response, r *http.Request) *teeTarget {
	if i.downloaded == nil || fake == "" {
		return nil
	}
	if resp.StatusCode != http.StatusOK || r.Method == http.MethodHead {
		return nil
	}
	if rng := strings.TrimSpace(r.Header.Get("Range")); rng != "" && !strings.HasPrefix(rng, "bytes=0-") {
		return nil
	}
	if _, ok := i.downloaded.Get(fake); ok {
		return nil
	}
	if resp.ContentLength <= 0 {
		return nil
	}
	if i.teeOn() {
		return i.beginLibraryTee(fake)
	}
	return i.beginRollingTee(fake)
}

// beginLibraryTee 开一份「提升进曲库」的暂存（开关开着那条路径）。
//
// ⚠️ 暂存名带一个**唯一后缀**（`<虚拟id>.<随机>.part`），与滚动缓存那条路径
// 同一个办法、同一个理由：同一条曲目被并发取两次（两台设备 / 重试与首次重叠 /
// 用户狂点重播）时，两次写盘**绝不能共用一个文件** —— 共用了就是两股字节交错
// 写进同一个文件，两边各自都「写够了 expected」，于是**一个坏文件被当成完整
// 文件提升进曲库**，之后这首歌永远播到一半。
//
// 名字仍然以虚拟 id 开头、以 `.part` 结尾：前者让人对着目录能认出这是谁的残骸，
// 后者是启动清扫（`cleanTeeCache`）认残骸的依据。
func (i *Interceptor) beginLibraryTee(fake string) *teeTarget {
	dir := i.teeDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	// CreateTemp 的默认权限是 0600（临时文件语义）；曲库文件不该比它更严，
	// 但保持与包内其它落盘一致（0644），免得以后有人对着两种权限猜原因。
	f, err := os.CreateTemp(dir, fake+".*.part")
	if err != nil {
		return nil
	}
	_ = f.Chmod(0o644)
	return &teeTarget{f: f, part: f.Name()}
}

// beginRollingTee 开一份「滚动试听缓存」的暂存（开关关着那条路径）。
//
// 暂存直接建在**缓存目录里**：转正就是一次同目录 rename，不会跨设备搬字节
// （参考实现也是直接往 cache_dir 写 .part）。
//
// ⚠️ 暂存名带一个**唯一后缀**（`<虚拟id>.<随机>.part`）：同一条曲目被并发取两次
// （多设备 / 重试与首次重叠）时，两次写盘绝不能共用一个文件 —— 共用了就是两股字节
// 交错写进同一个文件，两边各自都「写够了 expected」，于是**一条坏缓存被当成完整的
// 缓存转正**，之后每次重播都放一份坏音频。带随机后缀之后，各写各的，最后一次
// rename 赢，赢的那份是完整且自洽的。
//
// 参考实现同样用 uuid 后缀（`proxy/app.py:4020`）。曲库那条路径（`beginLibraryTee`）
// 用的是同一个办法 —— 它原来共用 `<虚拟id>.part`，被并发取流写坏过。
func (i *Interceptor) beginRollingTee(fake string) *teeTarget {
	dir := i.teeRollingDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	// CreateTemp 的默认权限是 0600（临时文件语义）；缓存不需要比曲库文件更严，
	// 但保持与包内其它落盘一致（0644），免得以后有人对着两种权限猜原因。
	f, err := os.CreateTemp(dir, fake+".*.part")
	if err != nil {
		return nil
	}
	_ = f.Chmod(0o644)
	return &teeTarget{f: f, part: f.Name(), rolling: true}
}

// finishTee 收尾：完整就转正（进曲库或进滚动缓存，看这次是哪种路径），不完整就删掉。
//
// ⚠️ 这里是**唯一**决定「提不提升」的地方，也是「字节数 == 上游声明的总长」那条
// 硬规矩唯一的落点。后台续传补完的那份字节同样从这里过（见 `teeHandoffResume`）。
func (i *Interceptor) finishTee(tt *teeTarget, fake string, t online.Track, expect int64, format string) {
	if tt == nil {
		return
	}
	// f 可能是 nil：写盘失败时被 teeWriter 丢掉了，或者这份现场是后台续传
	// 交回来的（那份已经写完、句柄早关了）。见 teeTarget.f。
	if tt.f != nil {
		_ = tt.f.Close()
		tt.f = nil
	}

	if expect <= 0 || tt.wrote != expect {
		// 半截文件留着只会占地方，而且下次播放会误以为有缓存
		_ = os.Remove(tt.part)
		i.logf("[INTERCEPT] 边听边下未完成，已丢弃（%d/%d 字节）：%s", tt.wrote, expect, t.Title)
		return
	}
	if tt.rolling {
		i.finishRolling(tt, fake, t, format)
		return
	}
	dst := i.teeDestPath(t, format)
	if dst == "" {
		_ = os.Remove(tt.part)
		return
	}
	if err := os.Rename(tt.part, dst); err != nil {
		// rename 失败（权限/意外跨设备）→ 退化成复制，但**别把半截留在暂存里**
		if cerr := copyLocalFile(tt.part, dst); cerr != nil {
			_ = os.Remove(tt.part)
			i.logf("[INTERCEPT] 边听边下提升失败：%v", cerr)
			return
		}
		_ = os.Remove(tt.part)
	}

	item := online.DownloadedItem{
		GUID: fake, Path: dst, Title: t.Title, Artist: t.Artist(), Size: tt.wrote,
		// tee 存的就是客户端正在听的那条流 —— 取音来源必然是曲目自己平台
		// （下载源池只管下载，管不到播放），所以这里如实填它。
		Source: t.Platform, Quality: t.Quality,
	}
	if err := i.downloaded.Put(item); err != nil {
		i.logf("[INTERCEPT] 边听边下登记失败：%v", err)
		return
	}
	i.logf("[INTERCEPT] 边听边下完成并已绑定本地 %q — %s → %s", t.Title, t.Artist(), dst)
	i.enrichAfterTee(fake, item, t)
	// 顺手把滚动缓存收敛到上限：开关在开/关之间切过之后，老缓存不该无限期占着地方。
	i.purgeRolling(teeRollingKeep)
}

// finishRolling 把一条完整的试听缓存转正到滚动目录，并按「最近使用」收敛到上限。
//
// ⚠️ **红线**：这里**不写登记表、不补标签、不落歌词**。滚动缓存不是「已进曲库」——
// 一旦登记进 downloaded，取流就会去读一个迟早被淘汰掉的临时文件：登记表里躺着
// 一条指向「明天可能没了」的路径，而「收藏里那首歌 404」正是这张表要避免的事。
// 参考实现的 `_tee_finalize` 里，开关关着那条分支同样只搬文件、不写任何元数据。
func (i *Interceptor) finishRolling(tt *teeTarget, fake string, t online.Track, format string) {
	dir := i.teeRollingDir()
	if dir == "" {
		_ = os.Remove(tt.part)
		return
	}
	dst := filepath.Join(dir, fake+extForFormat(format))
	if err := os.Rename(tt.part, dst); err != nil {
		if cerr := copyLocalFile(tt.part, dst); cerr != nil {
			_ = os.Remove(tt.part)
			i.logf("[INTERCEPT] 试听缓存落盘失败：%v", cerr)
			return
		}
		_ = os.Remove(tt.part)
	}
	// 刚写完 = 刚用过。mtime 就是「最近使用」那条时间线（命中时也会刷新，见
	// serveRollingIfAny），淘汰按它排序。显式设一次而不是靠文件系统给的时间，
	// 是为了让「写入」和「命中」两条时间线用同一个时钟。
	now := time.Now()
	_ = os.Chtimes(dst, now, now)

	i.logf("[INTERCEPT] 试听缓存已保留 %d 首（不出网重播）：%q — %s（%.1f MB）",
		teeRollingKeep, t.Title, t.Artist(), float64(tt.wrote)/(1<<20))
	i.purgeRolling(teeRollingKeep)
}

// ── 被切歌打断的下载：后台续传 ──────────────────────────────────────────
//
// 参考实现（zouclang 2.8.0）怎么做的，逐条对着看（`proxy/app.py`）：
//
//   - `:4091` 客户端把流丢下之后，**不删**那条 `.part`，而是判断够不够格交接：
//     `tee_enabled and full_resource and not upstream_aborted and written >= 1024
//     and _tee_handoff_slot_available()`。不够格才走老的删除分支。
//   - `:4713` `_register_tee_handoff` 是交接点：已经在缓存里 / 已经有一个在途
//     整轨下载 / 在失败冷却期内 → 直接删掉半截；否则建一个后台任务，并把它记在
//     `_full_fetch_tasks[guid]` 与 `_tee_handoff_active` 里。
//   - `:4742` `_tee_handoff_download` 是任务本体：先试续传（`_resume_part_download`），
//     不行就删半截、整轨重下（`_full_fetch_download`）。
//   - `:4762` 续传用 `Range: bytes=<已写>-` 重新开一次这条流的取流；拿到
//     `Content-Range: bytes <起点>-<终点>/<总长>`，要求**起点等于已写字节数、
//     总长等于一开始声明的总长**，两者对不上就放弃；补完再走它原有的收尾
//     （落库 + 补标签 + 官方绑定），长度不符则**抛错丢掉**（`final != total`）。
//   - `:4688` 并发上限来自配置 `tee_handoff_max`（`:121`，默认 3，0 = 关掉续传），
//     满了就丢半截，**不排队**。
//   - `:109-121` 那个开关区不需要用户**显式开启**这个能力：它跟着「边听边存」
//     总开关（`tee_save_enabled`，默认开）走，`tee_handoff_max` 只是额外给的
//     一个「关掉/调小」的口子。
//
// 曲率这边的对应关系（名字一一对上，结构按曲率的形状来）：
//
//	截流点/交接判断   startTeeHandoff      （代理实现里散在 finally 里的那几个条件）
//	后台任务本体      teeHandoffResume
//	续传              resumeInto
//	并发/收尾         teePool
//	收尾（提升/登记/补标签） 复用既有的 finishTee —— 那条已真机验收的路一行没分叉
//
// 与参考实现**有意**不同的两点：
//
//  1. **不做「续传不行就整轨重下」那个回落**。整轨重下是一条全新的下载路径
//     （要重新挑源、写盘、再走一遍收尾），而任务的要求就是「续传失败就是失败，
//     半截丢掉，行为跟现在一样」。少一条回落，就少一处能把不完整文件送进曲库
//     的地方。
//  2. **不做「失败冷却」**（参考实现的 `_full_fetch_failed` + 1800 秒）。那个
//     冷却存在的理由是它会在失败后立刻整轨重下；这里失败只剩一次 Range 请求的
//     代价，不值得为它多维护一张会过期的状态表。

// teeHandoffMax 是**同时**进行的后台续传任务数上限。
//
// 定 2 的理由：
//   - 要覆盖的用法是「切歌走了」：同一时刻真正被打断的歌通常只有 1 首，第二个
//     名额留给「两台设备同时听 / 连着切两首」这种重叠 —— 而不是留给「狂点重播」，
//     那种情况本来就有 beginTee 的「已在库里就不 tee」和滚动缓存在挡。
//   - 它必须小：这是**额外**的外网流量，与「下一首歌的取流」共用同一条上行。
//     2 条后台流对一条家庭宽带已经不算少，再多就该让用户自己决定（见下条）。
//   - 上限不是队列：满了就把半截丢掉（= 今天的行为），**不排队** —— 排队等于在
//     内存里再建一个无界队列，而它要解决的问题（用户快速跳歌）恰恰是最容易把
//     队列灌满的场景。
//
// 为什么不与参考实现一样做成配置项：曲率的 Config 里清一色是布尔开关（每个都
// 对应设置页上的一个勾），没有数值型配置的先例，加一个就要连带前端控件、持久化
// 与用例；而参考实现那个 env 也**不是**「用户显式开启才生效」，它跟着边听边存
// 总开关走。所以这里先按常量定死（跟着 `TeeEnabled` 走），口子留在后面：真有人
// 嫌后台流量大，再加一个 `TeeHandoffMax func() int` 的函数字段即可（与其它开关
// 同一个形状），不必现在就长出一套数值配置。
const teeHandoffMax = 2

// teeHandoffTimeout 是单个后台续传任务的时限（从开工到收尾之前）。
//
// 定 10 分钟的理由：320k 的一首约 10MB、整轨无损 30–40MB；就算源站只给 1Mbps，
// 40MB 也只要 5 分半。10 分钟既容得下慢源，又能在源站「连上了但一直不给字节」时
// 把任务掐掉 —— 没有这个时限，任务会一直占着名额与文件句柄（`mediaClient`
// 刻意不设整体 Timeout，那是给「一首歌本该能放几分钟」让的路）。
const teeHandoffTimeout = 10 * time.Minute

// teeHandoffStopWait 是收尾（见 CloseTee）时等后台任务自己退出的上限。
//
// 取消之后任务会立刻在「读上游」那一步失败并删掉半截，所以正常是毫秒级；
// 这个上限只是不让一个卡住的源把进程关闭流程拖住 —— 进程本来就要退出了。
const teeHandoffStopWait = 3 * time.Second

// teeHandoffMinBytes 是「值得续传」的最少已写字节数。
//
// 1KiB 取自参考实现（`proxy/app.py:4091` 的 `written >= 1024`）。它挡的是「客户端
// 刚一上来就走了」（写了 0 字节）：那种情况续传等于整轨重下，不是这次要做的功能。
const teeHandoffMinBytes = 1024

// teeInterrupt 记「这次转发为什么提前结束」，用来判断要不要交接后台续传。
//
// 只交接「客户端先走」的那种中断，判据是 `clientGone`（请求上下文被取消）：
// net/http 在客户端断开时（切歌、关页面、手机切网）会替我们取消它。⚠️ 所以
// 「用户主动切歌」与「客户端网络真断了」在这里**是同一件事** —— 我们不去区分，
// 也**无法**可靠区分（对服务端来说都只是「对面不读了」），更没有理由区别对待：
// 两种情况都已经写下的字节都同样可信，续传也同样是「把这首歌补完」。
//
// 真正要区分的是另一件事：**上游**自己断了。那时源站不可信，追着它续传只是把
// 同一段网络故障再打一遍、白占一个名额，所以不交接。这条线对着参考实现的
// `not upstream_aborted`（`proxy/app.py:4091`）。
type teeInterrupt struct {
	// readErr 是从上游读到的错误；nil 或 io.EOF = 上游把该给的字节给完了。
	readErr error
	// clientGone 是「客户端的请求上下文已经取消」—— net/http 在连接断开时
	// 就会取消它，是「客户端走了」最直接的证据。
	clientGone bool
}

// upstreamIntact 报告上游那条流本身没坏。
func (in teeInterrupt) upstreamIntact() bool {
	if in.clientGone {
		// 客户端走了：读侧那个错误多半就是「请求 ctx 被连坐取消」的产物
		// （我们把 r.Context() 传给了取媒体的请求），不能算上游故障。
		// 已经写进文件的那段前缀仍然可信 —— 它是取消之前就已经读到的。
		return true
	}
	return in.readErr == nil || errors.Is(in.readErr, io.EOF)
}

// teeReadErr 记下「读上游时到底出没出错」。
//
// 为什么要单独包一层：io.Copy 返回的错误不告诉你它来自哪一侧。客户端断开时
// 错误来自写侧（我们写客户端失败），上游断流时来自读侧 —— 收尾时要区分这两者
// （见 teeInterrupt）。这里只负责把读侧那个错误留下来。
//
// 只有 io.Copy 那一个 goroutine 碰它（交接发生在 copy 结束之后），所以不上锁。
type teeReadErr struct {
	r   io.Reader
	err error
}

func (t *teeReadErr) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

// teePool 是后台续传的任务簿：谁在跑、跑几个、怎么收尾。
//
// 零值可用（第一次起任务时才建父 ctx 与登记表）。把这三件事收在一个结构体里、
// 而不是散在 Interceptor 上，是因为它们每一处都要在「请求 goroutine」与
// 「后台 goroutine」之间共享 —— 散着写迟早会漏掉一把锁。
type teePool struct {
	mu sync.Mutex
	// base 是所有任务共用的父 ctx：CloseTee 取消它 = 所有在途任务立刻收手。
	base   context.Context
	cancel context.CancelFunc
	// live 是「在途」的曲目集合（用虚拟 id）。它同时充当并发上限的计数、
	// 同曲去重的依据，以及收尾时「还有几个人没退」的判据。
	live   map[string]struct{}
	closed bool
	// capOverride 非 0 时覆盖 teeHandoffMax —— 只给用例用（验上限不必真起 3 首）。
	capOverride int
}

// limit 返回本次生效的并发上限。
func (p *teePool) limit() int {
	if p.capOverride > 0 {
		return p.capOverride
	}
	return teeHandoffMax
}

// start 占一个名额并在后台跑 fn（fn 拿到的 ctx 在 CloseTee 时被取消）。
//
// 返回 false = 这次不跑：已经在收尾了、同一条曲目已经在续传、或者名额满了。
// 三种情形调用方一律按旧语义处理（把半截丢掉），**不排队**。
func (p *teePool) start(fake string, fn func(ctx context.Context)) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	if p.base == nil {
		p.base, p.cancel = context.WithCancel(context.Background())
		p.live = make(map[string]struct{})
	}
	if _, dup := p.live[fake]; dup {
		p.mu.Unlock()
		return false
	}
	if len(p.live) >= p.limit() {
		p.mu.Unlock()
		return false
	}
	p.live[fake] = struct{}{}
	ctx := p.base
	p.mu.Unlock()

	go func() {
		// 摘登记发生在 fn **返回之后**（defer 的执行顺序）：所以 inflight()==0
		// 就等于「这个任务已经彻底跑完」，用例可以据此断言磁盘上的最终状态。
		defer p.release(fake)
		fn(ctx)
	}()
	return true
}

// release 摘掉一条在途登记。
func (p *teePool) release(fake string) {
	p.mu.Lock()
	delete(p.live, fake)
	p.mu.Unlock()
}

// inflight 返回在途任务数。
func (p *teePool) inflight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}

// drain 等在途任务全部收尾（**不**取消它们），最多等 d，返回是否都收完了。
//
// 生产路径不用它（后台任务本来就该在后台跑完）；用例用它把「后台」变成「可断言」：
// 不 drain 就去读登记表，读到的只能是「碰巧已经跑完」。
func (p *teePool) drain(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if p.inflight() == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stop 取消所有在途任务并等它们收尾（最多等 d，超时就放手）。
func (p *teePool) stop(d time.Duration) {
	p.mu.Lock()
	p.closed = true
	cancel := p.cancel
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	p.drain(d)
}

// CloseTee 取消所有在途的后台续传并等它们收尾（半截由任务自己删掉）。
//
// ⚠️ 调过之后这个拦截层**不再**起新的续传任务（`teePool.closed`）：收尾是一个
// 单向动作，回来之后旧行为（半截丢掉）才是对的。
//
// main.go 在 SIGTERM 的优雅关闭里调它。不调也不会漏 goroutine（进程退出会带走
// 所有 goroutine），但那样会留下一堆 `.part`，要等下次启动清扫而且只在超过
// `teeStaleAfter`（1 小时）之后才会被清 —— 所以还是调一下更干净。
func (i *Interceptor) CloseTee() {
	i.tee.stop(teeHandoffStopWait)
}

// finishTeeAfterStream 是取流路径的收尾入口：完整就转正；被客户端中途丢下的那份
// 交给后台续传（够格时）；其余情形与以前一模一样（半截丢掉）。
//
// 取流路径（stream.go）走这个，不要再直接调 finishTee —— 直接调就绕过了续传。
func (i *Interceptor) finishTeeAfterStream(tt *teeTarget, fake string, t online.Track, expect int64, format string, in teeInterrupt) {
	if tt == nil {
		return
	}
	if i.startTeeHandoff(tt, fake, t, expect, format, in) {
		return
	}
	i.finishTee(tt, fake, t, expect, format)
}

// startTeeHandoff 判断这次中断够不够格交接后台续传；够格就起一个任务并返回 true。
//
// ⚠️ 返回 true 时 `tt.part` 已经交给后台了（可能已经被删掉、被续传、被提升），
// 调用方**不能再碰它**（不能再删、更不能 finishTee）。
func (i *Interceptor) startTeeHandoff(tt *teeTarget, fake string, t online.Track, expect int64, format string, in teeInterrupt) bool {
	if tt.rolling {
		// 滚动试听缓存（开关关着那条路）**有意**不交接：它只是最近 5 首的临时
		// 试听缓存，随时会被淘汰，半截丢掉是对的。开关关着时冒出后台下载，
		// 等于把「不留永久曲库副本」这条红线偷换成「偷偷帮你下整曲」。
		return false
	}
	if !i.teeOn() {
		// 开关在这一刻是关的：不再起任何新的下载。正在续传的那些不管 ——
		// 它们属于「开着的时候开始的」，用户关开关的意图是「以后别下」。
		return false
	}
	if expect <= 0 || tt.wrote >= expect {
		return false // 没法判断完整；或者本来就写完了（正常提升路径会处理）
	}
	if tt.wrote < teeHandoffMinBytes {
		return false // 太短，见 teeHandoffMinBytes
	}
	if !in.upstreamIntact() {
		return false // 上游自己断的：不追着坏源续传（见 teeInterrupt）
	}
	if tt.f == nil {
		return false // 写盘那头已经坏了（teeWriter 写失败时丢掉了句柄）：没有可续的现场
	}
	if _, ok := i.downloaded.Get(fake); ok {
		return false // 已经在库里了（客户端那条路刚补完），不必再拉一遍
	}
	part, written := tt.part, tt.wrote
	// 句柄现在关掉：后台任务自己用 O_APPEND 重新打开（见 resumeInto）。
	// 先关再交接，是为了「谁在写这个文件」在任何时刻都只有一个答案。
	_ = tt.f.Close()
	tt.f = nil

	if !i.tee.start(fake, func(ctx context.Context) {
		i.teeHandoffResume(ctx, fake, t, part, written, expect, format)
	}) {
		// 同一条曲目已经在续传 / 名额满了 / 已经在收尾：按旧语义丢掉半截。
		// 这里**不删**文件：交回给调用方那条既有的 finishTee 走一遍（它会删掉
		// 不完整的半截），日志与行为与今天完全一致。
		i.logf("[INTERCEPT] 后台续传无空位（在途 %d/%d），半截按旧语义丢弃：%s",
			i.tee.inflight(), teeHandoffMax, t.Title)
		return false
	}
	i.logf("[INTERCEPT] 取流被客户端中断，已交接后台续传（%d/%d 字节）：%s — %s",
		written, expect, t.Title, t.Artist())
	return true
}

// teeHandoffResume 是后台任务本体：把被打断的那首歌补完，然后走**既有的**
// 提升→登记→补标签那条路。
//
// 它跑在一个独立的 goroutine 里（ctx 来自 teePool）：请求路径早就返回了，这个
// goroutine 不持有任何请求期资源 —— 没有 ResponseWriter、不碰播放那条流、不占
// `downloadQueue` 那类的名额。所以「下一个人」不会被它挡住；它与播放路径唯一的
// 交集是 `mediaClient` 的连接池（每主机 4 条空闲连接，够它俩各占一条）。
func (i *Interceptor) teeHandoffResume(ctx context.Context, fake string, t online.Track, part string, written, expect int64, format string) {
	ctx, cancel := context.WithTimeout(ctx, teeHandoffTimeout)
	defer cancel()

	size, ok := i.resumeInto(ctx, t, part, written, expect)
	if !ok {
		_ = os.Remove(part)
		i.logf("[INTERCEPT] 后台续传未成，已丢弃半截（%d/%d 字节）：%s — %s",
			written, expect, t.Title, t.Artist())
		return
	}
	// 只说「补齐了余下的字节」：提不提升由 finishTee 判（它自己会记日志），
	// 这里不该抢在它前面宣布成功。
	i.logf("[INTERCEPT] 后台续传补齐余下字节（%d → %d）：%s — %s",
		written, size, t.Title, t.Artist())
	// 包成一个「已经写完」的现场交给 finishTee：于是「只提升完整文件」那条硬规矩
	// （以及登记表、补标签）一个字都没改 —— size 是**磁盘上量到的真实大小**，
	// finishTee 里那句 `tt.wrote != expect` 照样会拦下来。f 故意留 nil：句柄
	// 早在交接时就关了，这里不需要再开一个只为关掉。
	i.finishTee(&teeTarget{part: part, wrote: size}, fake, t, expect, format)
}

// resumeInto 用 Range 把 part 从 `written` 续到上游声明的总长，返回**续完之后
// 磁盘上的真实大小**；false = 这次续传没成（调用方必须把 part 删掉）。
//
// 判定「续的是同一个文件」只认一件事：206 + `Content-Range` 的**起点等于我们的
// 现场、总长等于上游一开始声明的总长**。少一样就放弃 —— 宁可少一首，不可多一个
// 错文件（换了一档音质/换了一条直链的文件，长度几乎必然不同，这条就是拦它的）。
func (i *Interceptor) resumeInto(ctx context.Context, t online.Track, part string, written, expect int64) (int64, bool) {
	if i.pool == nil {
		return 0, false
	}
	// 现场必须自洽：磁盘上那段前缀的长度要**正好**等于我们打算在 Range 里声明的
	// 起点。对不上说明这段前缀不是我们以为的那一段（写盘失败时 wrote 与磁盘长度
	// 可能不一致，见 teeWriter），那就不猜，直接放弃。
	if fi, err := os.Stat(part); err != nil || fi.Size() != written {
		return 0, false
	}
	// 重新解析一次：直链有时效（CDN 的签名 URL 几分钟就失效），拿最初那条 URL 去
	// 续传等于赌它还活着；参考实现同样重新走一次在线取流（`_open_online_stream`）。
	// 解析池自带 5 分钟缓存，所以正常情况下拿到的还是同一条 URL。
	res, err := i.pool.Resolve(ctx, t.Platform, t.PlatformID)
	if err != nil || res == nil || strings.TrimSpace(res.URL) == "" {
		return 0, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, res.URL, nil)
	if err != nil {
		return 0, false
	}
	applyMediaHeaders(req, t, fmt.Sprintf("bytes=%d-", written))
	resp, err := mediaClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		// 源不认 Range（回 200 整轨）或直接报错（4xx/5xx）：拼不出来，放弃。
		return 0, false
	}
	start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || start != written || total != expect {
		return 0, false
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, false
	}
	// CopyN 而不是 Copy：上游要是违反 Content-Range 多给字节，一个都不写进盘
	// （写进去也只会被后面的长度检查丢掉，但没必要先落一盘垃圾）。
	_, cerr := io.CopyN(f, resp.Body, expect-written)
	closeErr := f.Close()
	if cerr != nil || closeErr != nil {
		return 0, false
	}
	// 以**磁盘上的真实大小**为准（不是 written+拷贝计数）：这个数接下来要交给
	// finishTee 去跟 expect 比，必须是独立量出来的，不能是本函数的自述。
	fi, err := os.Stat(part)
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

// parseContentRange 解析 `bytes <起点>-<终点>/<总长>`，返回起点与总长。
//
// 只认这一种形状（大小写不敏感、允许空白）：解析不出来就放弃续传，不猜。
func parseContentRange(v string) (start, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if len(v) < len("bytes") || !strings.EqualFold(v[:len("bytes")], "bytes") {
		return 0, 0, false
	}
	span, totalPart, found := strings.Cut(strings.TrimSpace(v[len("bytes"):]), "/")
	if !found {
		return 0, 0, false
	}
	startPart, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	total, err2 := strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
	if err1 != nil || err2 != nil || total <= 0 {
		return 0, 0, false
	}
	return start, total, true
}

// serveRollingIfAny 用滚动试听缓存应答取流：命中就完全不出网。
//
// 返回 true = 已经应答（调用方不要再往下走）。这是「开关关着时重播不出网」
// 的落地点 —— 与「已进曲库」那条路（serveDownloadedIfAny）刻意分开：
// 那条读登记表（永久），这条只认缓存目录里的文件（临时、随时可能被淘汰）。
func (i *Interceptor) serveRollingIfAny(w http.ResponseWriter, r *http.Request, fake string) bool {
	path := i.rollingPath(fake)
	if path == "" {
		return false
	}
	// 命中就是「刚用过」：刷新 mtime，让淘汰按使用而不是按写入排。
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	i.logf("[INTERCEPT] 试听缓存命中 %s → %s", fake, path)
	// http.ServeFile 自带 Range / If-Modified-Since / Content-Type 推断 ——
	// 官方客户端拖动进度条靠 Range，自己写一遍容易漏（与 serveDownloadedIfAny 同理）。
	http.ServeFile(w, r, path)
	return true
}

// rollingPath 找 `<rolling>/<fake>.<ext>`；没有返回空串。
//
// 按扩展名逐个试而不是记一张表：目录里最多 `teeRollingKeep` 个文件，
// 几次 stat 的成本可以忽略，而少维护一张「文件名 → 曲目」的映射就少一处会不一致的状态。
func (i *Interceptor) rollingPath(fake string) string {
	dir := i.teeRollingDir()
	if dir == "" || fake == "" {
		return ""
	}
	// fake 是登记表里的虚拟 id（32 位 hex），但它一路从请求里传进来 ——
	// 拼路径前挡一次路径分隔符，免得将来有人把别的来源接进来。
	if strings.ContainsAny(fake, `/\`) || strings.Contains(fake, "..") {
		return ""
	}
	for _, ext := range teeRollingExts {
		p := filepath.Join(dir, fake+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p
		}
	}
	return ""
}

// rollingFiles 列出滚动缓存里的**完整**音频（按最近使用新→旧，mtime 相同则按文件名）。
//
// `.part` 是**正在进行**的缓存，不算数：它既不该被淘汰，也不该占 keep 名额。
func (i *Interceptor) rollingFiles() []string {
	dir := i.teeRollingDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		path  string
		mtime int64
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !rollingAudioName(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	// 稳定排序：同一次 Chtimes 的秒级/纳秒级并列（以及某些文件系统的秒级 mtime）
	// 不应该让「淘汰哪一条」变成随机 —— 那会让行为在真机上不可复现。
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].mtime != items[b].mtime {
			return items[a].mtime > items[b].mtime
		}
		return items[a].path < items[b].path
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.path)
	}
	return out
}

// rollingAudioName 判断一个文件名是不是滚动缓存的完整音频（而不是半截的 `.part`）。
func rollingAudioName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, want := range teeRollingExts {
		if ext == want {
			return true
		}
	}
	return false
}

// purgeRolling 把滚动缓存收敛到 keep 条（保留最近使用的那些），返回删除条数。
//
// keep <= 0 = 一条不留（把 `teeRollingKeep` 改成 0 就等于关掉滚动缓存）。
//
// 只删**完整音频**，`.part` 一律不碰：那是正在下载/正在被下一段字节续写的现场，
// 删了它等于把一次进行中的播放写盘搞坏（这是它与启动清扫 `cleanTeeCache` 的分工
// —— 后者清的是上次运行留下的孤儿 `.part`）。
func (i *Interceptor) purgeRolling(keep int) int {
	files := i.rollingFiles()
	if keep < 0 {
		keep = 0
	}
	if len(files) <= keep {
		return 0
	}
	removed := 0
	for _, p := range files[keep:] {
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	if removed > 0 {
		i.logf("[INTERCEPT] 试听缓存淘汰 %d 条（上限 %d 首）", removed, keep)
	}
	return removed
}

// enrichAfterTee 给 tee 提升出来的文件补标签（**不重下音频**）。
//
// ⚠️ 为什么必须补：tee 是直接复制流字节、没走下载接口，所以落盘的文件**没有任何
// 标签** —— 歌能听、能绑定，但在飞牛里标题/歌手/专辑/封面/歌词全是空的。
// 重新下载一遍会把 tee 省下的带宽又花回去，所以只调「补标签」那条路径。
//
// 失败只记日志：文件已经在库里、已经能听，补标签是锦上添花 —— 不能因为抓不到
// 封面就把一首已经下好的歌算成失败。
func (i *Interceptor) enrichAfterTee(fake string, item online.DownloadedItem, t online.Track) {
	// ⚠️ 判 `i.localAPI` 而不是 `i.cfg.LocalAPI` —— 真正发请求的 postLocalAPI 用的是
	// 前者（构造时从 cfg 解析并去掉尾斜杠）。判错了会在测试环境里静默不补标签。
	if i.localAPI == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), teeEnrichTimeout)
	defer cancel()
	payload := map[string]any{
		"path":     item.Path,
		"name":     t.Title,
		"singer":   t.Artist(),
		"album":    t.Album,
		"cover":    t.CoverURL,
		"source":   t.Platform,
		"songmid":  t.PlatformID,
		"duration": t.Duration,
		"year":     t.Year,
	}
	for k, v := range i.downloadPrefs() {
		payload[k] = v
	}
	if _, status, err := i.postLocalAPI(ctx, "/api/download/enrich", payload); err != nil || status != 200 {
		i.logf("[INTERCEPT] 边听边下补标签失败（文件已在库里，不影响播放）：status=%d err=%v", status, err)
		return
	}
	// 补完标签文件**变大了**（封面/歌词进了标签）—— 登记表里那份大小要跟上。
	// 否则「登记说 10.4 MB、磁盘上 10.5 MB」这种对不上，排查时会让人以为是别的问题
	// （真机实测：流下来 10,878,476 → 落盘 11,029,926）。
	if fi, statErr := os.Stat(item.Path); statErr == nil && fi.Size() != item.Size {
		item.Size = fi.Size()
		if err := i.downloaded.Put(item); err != nil {
			i.logf("[INTERCEPT] 边听边下更新登记大小失败：%v", err)
		}
	}
}

// teeDestPath 决定提升后的落点：`<下载目录>/<歌手> - <标题>.<扩展名>`。
//
// 与下载链路同一个命名习惯 —— 同一个曲库里不该有两种命名。
func (i *Interceptor) teeDestPath(t online.Track, format string) string {
	if i.cfg.DownloadDir == nil {
		return ""
	}
	dir := strings.TrimSpace(i.cfg.DownloadDir())
	if dir == "" {
		return ""
	}
	name := sanitizeFileName(t.Artist() + " - " + t.Title)
	if name == "" {
		name = sanitizeFileName(t.Title)
	}
	if name == "" {
		return ""
	}
	return filepath.Join(dir, name+extForFormat(format))
}

// teeWriter 一边把字节交给客户端，一边写进暂存文件。
//
// ⚠️ **顺序不能反**：先写给客户端（播放优先），再写盘。盘写失败就把句柄丢掉，
// 绝不让磁盘问题变成一个「听不了」的错误。
type teeWriter struct {
	w io.Writer
	t *teeTarget
}

func (tw *teeWriter) Write(p []byte) (int, error) {
	n, err := tw.w.Write(p)
	if n > 0 && tw.t.f != nil {
		if _, werr := tw.t.f.Write(p[:n]); werr != nil {
			_ = tw.t.f.Close()
			tw.t.f = nil // 之后只转发，不再碰盘
		} else {
			tw.t.wrote += int64(n)
		}
	}
	return n, err
}

// sanitizeFileName 把文件名里的路径分隔符与控制字符换掉 —— 歌名是外部数据，
// 带 `/` 的话会把文件写到别的地方去。
func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}
	out := strings.Map(repl, s)
	out = strings.Trim(out, " .")
	if len(out) > 120 {
		out = strings.TrimSpace(out[:120])
	}
	return out
}

// extForFormat 把解析出来的格式映射成扩展名（认不出就当 mp3）。
func extForFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "flac":
		return ".flac"
	case "m4a", "aac":
		return ".m4a"
	case "wav":
		return ".wav"
	case "ape":
		return ".ape"
	}
	return ".mp3"
}

// copyLocalFile 是 rename 失败时的退路。
func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// teeStaleAfter 是暂存文件的「过期」门槛。
//
// 只在**启动时**清一次，且只清超过这个时长的 —— 启动时不可能有正在进行的 tee，
// 但门槛仍然留着，避免误删上一次运行刚写了一半、马上要被续上的文件。
const teeStaleAfter = time.Hour

// cleanTeeCache 清掉上次运行留下的半截暂存文件，并把滚动缓存收敛到上限。
//
// ⚠️ 为什么必须清：`.part` 只在 `finishTee` 里被删或被改名。进程如果**中途被杀**
// （升级、OOM、断电），那个 `.part` 就永远留在对应目录里 —— 每听一首歌没听完就升级，
// 就多一个几十兆的残骸，而且**没有任何地方会提到它**（界面上看不到、日志里没有）。
//
// 两个目录都要扫（曲库暂存 + 滚动缓存），因为两条路径都会留下 `.part`。
// 顺手 `purgeRolling`：上限被调小之后，老缓存要能在启动时就清掉。
//
// 只在启动时清：那时候不可能有正在进行的 tee，所以不用考虑并发。
func (i *Interceptor) cleanTeeCache() {
	cutoff := time.Now().Add(-teeStaleAfter)
	for _, dir := range []string{i.teeDir(), i.teeRollingDir()} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		removed, freed := 0, int64(0)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				removed++
				freed += info.Size()
			}
		}
		if removed > 0 {
			i.logf("[INTERCEPT] 清掉 %d 个上次运行留下的半截暂存文件（%.1f MB）：%s",
				removed, float64(freed)/(1<<20), dir)
		}
	}
	i.purgeRolling(teeRollingKeep)
}
