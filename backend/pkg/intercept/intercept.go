// Package intercept 实现「接管官方音乐接口」的拦截层。
//
// # 它在整条链路里的位置
//
//	pkg/takeover  负责抢到 /var/run/trim_music.socket 并把官方 daemon 让到
//	              /var/run/trim_music_upstream.socket。它只管「谁在 listen」。
//	pkg/intercept 负责「收到请求之后做什么」：认识的请求自己处理（并混入在线
//	              曲目），不认识的原样转发给官方。
//
// 两者通过 takeover.Interceptor 接口解耦：takeover 拿到一个 `Handle(w, r) bool`，
// 返回 false 就自己透传。这样接管层不需要知道任何业务概念。
//
// # 拦截而不是替换
//
// 官方前端是现成的、用户已经在用的界面。本包**不改它**，只在它调用的接口上
// 做三件事：
//
//  1. **合并**：官方搜索/收藏/歌单/历史的响应里追加在线曲目，官方数据永远在前
//     （或按各端点的既有顺序），`total` 相应相加。
//  2. **接管**：在线曲目的取流、歌词、封面、元数据由我们直接应答 —— 官方后端
//     不认识这些 id。
//  3. **写入**：官方接口只认官方曲目，所以「收藏一首在线歌」「把在线歌加进歌单」
//     这些写操作由我们落盘；涉及官方曲目的部分仍然照常转发给官方。
//
// # 透传优先
//
// 每个 handler 的第一件事都是判断「这条请求是不是关于在线曲目的」。不是就
// 立刻原样转发 —— 包括官方返回 401、非 JSON、`code != 0` 的情况，全部原样透传，
// 不解析、不注入。这样即使官方接口改版，本层的失效模式也只是「在线功能消失」，
// 而不是「官方功能被我们搞坏」。
package intercept

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// apiPrefix 是官方音乐接口的公共前缀。
//
// ⚠️ **必须带 `/v1`**。少了它全部请求都会落到官方前端的 SPA fallback（返回
// HTML），表现为「接口没反应」而不是「404」—— 这个坑在探测阶段踩过一次。
const apiPrefix = "/music/api/v1"

// Upstream 抽象「把请求发给官方后端」。
//
// 由 pkg/takeover 用 Unix socket 实现（官方 daemon 被让到了
// /var/run/trim_music_upstream.socket）。本包不关心它是 socket 还是 TCP，
// 这样单测可以直接塞一个 httptest.Server。
type Upstream interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config 是拦截层的构造参数。
type Config struct {
	// DataDir 是持久化根目录（收藏 / 歌单 / 历史）。
	DataDir string
	// Upstream 是官方后端。必填。
	Upstream Upstream
	// Logf 是日志出口，nil 时静默。
	Logf func(format string, args ...any)
	// Platforms 是参与在线搜索的平台顺序，默认 wy,tx。
	//
	// 只列**有解析器**的平台：搜索层能查到四家，但能解析出直链的目前只有
	// 网易与 QQ。查得到点不动比查不到更糟，所以默认只放这两家。
	Platforms []string
	// ResolverTTL 预留：解析结果缓存时长（0 = 用 online 包默认值）。
	ResolverTTL time.Duration
	// Searcher 是搜索实现的注入口，nil 时用 pkg/search 的真实实现。
	//
	// 存在的理由是**可测性**：搜索要打四家平台的外网接口，单测不能依赖网络，
	// 也不该为了测试去起一个假的第三方站点。注入点放在这里，测试就能精确
	// 控制「哪些平台返回什么」。
	Searcher func(keyword, platform string, page, size int) []search.UnifiedSong
	// Pool 是直链解析池的注入口，nil 时用内置的网易 + QQ 解析器。
	Pool *online.Pool
	// LyricFetcher 是歌词取回的注入口，nil 时用 pkg/search 的三级链路。
	//
	// 同样是为了可测性：真实实现要打第三方歌词接口，单测不该依赖它。
	LyricFetcher func(t online.Track) string
	// NeteasePlaylist 是「网易公开歌单」的取回注入口，nil 时用内置实现。
	//
	// 返回值是（歌单名、封面直链、曲目）。存在的理由和上面几个一样：真实实现
	// 要打 `music.163.com`，而推荐歌单会在**每次列歌单时**被算一次 ——
	// 单测绝不能因此依赖外网，否则一个网络抖动就会让整个套件变红。
	NeteasePlaylist func(ctx context.Context, id string, limit int) (string, string, []online.Track)
	// ChartDetail 是「榜单内容」的取回注入口，nil 时走本机回环（见 vchart.go）。
	//
	// 返回值是（榜单名、封面直链、曲目）。和 NeteasePlaylist 同一个理由：真实实现
	// 要打第三方榜单接口，而榜单歌单会在**每次列歌单时**被算一次。
	ChartDetail func(ctx context.Context, source, id string, limit int) (string, string, []online.Track, error)
	// ChartPlaylists 返回「用户勾选要注入的榜单」，元素形如 `wy_19723756`。
	//
	// 由配置层提供（`AppConfig.ChartPlaylists`），nil 或返回空 = 一个榜单都不注入。
	// 之所以是**函数**而不是一份切片：配置会在运行时被改（榜单管理页保存即生效），
	// 而拦截层是启动时构造的 —— 拿一份拷贝就等于「改完要重启」。
	ChartPlaylists func() []string
	// ChartPlaylistsEnabled 是榜单注入的总开关，nil = 一律视为开。
	//
	// 与 ChartPlaylists 同样是函数：用户随时可能关掉。关掉时名单**不会丢**，
	// 只是这次不注入 —— 所以它是独立的一个开关，而不是「把名单清空」。
	ChartPlaylistsEnabled func() bool
	// UIInjectEnabled 是「往飞牛官方页面注入脚本」的开关，nil = 一律视为开。
	//
	// 与上面两个同样是函数：用户在设置里关掉就该立刻生效。关掉的效果是
	// `handlePageShell` **根本不认领**文档请求，官方页面由接管层的反向代理
	// 逐字节原样透传 —— 这是「注入把官方页弄坏了」时的一键退路。
	UIInjectEnabled func() bool
	// PlatformsFunc 返回「参与在线搜索合并」的平台，**现取**；nil 时用 Platforms
	// （或内置默认：网易 + QQ）。
	//
	// # 为什么要有这个函数
	//
	// musicdl 外挂平台是**用户开关决定的**：打开之后咪咕 / 千千 / B站 也该出现在
	// 飞牛官方页面的搜索里。构造时定死的话，用户开完开关还得重启应用才看得到 ——
	// 而其余几个配置项（榜单、页面注入、UI 覆盖）都是现取的，这里没理由不一样。
	//
	// 保留静态的 `Platforms` 是因为它在测试里是「一句话指定平台」的方便入口。
	PlatformsFunc func() []string
	// LocalAPI 是本机曲率后端的基地址（如 `http://127.0.0.1:8898`）。
	//
	// 只有「同源代理」那几条路由会用到它（见 qulvbridge.go）：注入脚本从官方
	// 页面里发起的请求先到拦截层，再由这里转给本机后端。留空表示不提供同源通道。
	//
	// 注意它是**启动时定死的回环地址**，不接受请求里的任何 host ——
	// 这是这条通道不引入 SSRF 面的原因。
	LocalAPI string
	// Revision 是注入脚本标签上的版本串（应用版本号）。
	//
	// 只用来「对着页面源码能看出这行脚本是哪个版本发的」，以及参与缓存打破。
	// 留空也能工作（脚本内容的指纹会补上）。见 pageshell.go。
	Revision string
	// LxSearch 是「洛雪音源脚本」搜索的注入口，nil = 洛雪源不进池（**缺省**）。
	//
	// 为什么是函数而不是一个布尔开关：洛雪源能不能用，取决于宿主进程起没起来、
	// 以及用户装进去的脚本**谁在 inited 里声明了 musicSearch** —— 后者只有宿主知道。
	// 所以这里只接一条「问它要候选」的路，哪些源能搜由宿主自己过滤（见
	// sidecar/lx_host/server.mjs 的 /search）；宿主没起来时它返回 lxnode.ErrNotRunning，
	// 拦截层当「源没开」静默跳过 —— 于是这里**不需要**再判一次开关
	// （两处判空迟早不一致）。
	//
	// 返回的 Song.Source 是洛雪源标识（qsvip / wy / …），进池时会加 `lx:` 前缀，
	// 见 lxsource.go。
	LxSearch func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error)
	// DownloadSources 返回「下载源池」的平台列表（现取），nil = 不用池（直接用曲目自己平台）。
	//
	// 用户的分工（2026-10-06）：网易云 / QQ 是**发现**来源，musicdl 那批平台是
	// **取音**来源。下载时先去池里找同名曲目挑最高音质，找不到才回落 ——
	// 见 pkg/intercept/acquire.go。
	DownloadSources func() []string
	// LyricAutoDownload 返回「下载时自动带上歌词」的开关（现取），nil = 视为开。
	LyricAutoDownload func() bool
	// CoverEmbed 返回「下载时把封面内嵌进标签」的开关（现取），nil = 视为开。
	CoverEmbed func() bool
	// TeeEnabled 返回「边听边下」的开关（现取），nil = 一律视为关。
	TeeEnabled func() bool
	// DownloadDir 返回曲库下载目录（现取）。「边听边下」把暂存文件写在它下面的
	// 隐藏子目录里，这样「提升」是一次 rename，不用跨设备搬字节。空 = 不做 tee。
	DownloadDir func() string
	// FavAutoDownload 返回「收藏 / 加入歌单时自动下载并绑定本地」的开关，nil = 一律视为关。
	//
	// 与其它开关同样是**函数**：用户在界面上打开就该立刻生效。默认关 ——
	// 它会真的占磁盘和带宽（无损一首几十兆），该由用户明确同意。
	FavAutoDownload func() bool
	// UIDevFile 是注入脚本的「开发覆盖」文件路径；文件存在且非空时优先发它。
	//
	// 注入界面只能在官方页面里看效果，而脚本平时是 `go:embed` 进二进制的 ——
	// 改一行要重打包 + 重装应用（约两分钟）。有这个文件，改完刷新官方页面
	// 就能看到。文件不存在（默认）时走嵌入的那份，生产行为完全不变。
	//
	// 只在同源通道的 `/music/_qulv/ui.js` 这一条上生效，不会影响别的响应。
	UIDevFile string
}

// defaultPlatforms 是默认参与在线搜索的平台。
var defaultPlatforms = []string{"wy", "tx"}

// DefaultPlatforms 返回内置默认平台的一份**拷贝**。
//
// 给 main.go 用：它要在默认之上追加「当前启用的外挂平台」，而不是把默认那串
// 在别处再抄一遍（两处抄 = 迟早不一致）。
func DefaultPlatforms() []string {
	return append([]string(nil), defaultPlatforms...)
}

// platforms 返回当前要参与搜索合并的平台（**现取**，见 Config.Platforms）。
func (i *Interceptor) platforms() []string {
	if i.cfg.PlatformsFunc != nil {
		if out := i.cfg.PlatformsFunc(); len(out) > 0 {
			return out
		}
	}
	if len(i.platformsStatic) > 0 {
		return i.platformsStatic
	}
	return append([]string(nil), defaultPlatforms...)
}

// Interceptor 是拦截层主体。
type Interceptor struct {
	cfg      Config
	logf     func(format string, args ...any)
	registry *online.Registry
	pool     *online.Pool
	store    *online.Store
	routes   []route

	userMu    sync.Mutex
	userCache map[string]userEntry

	// legacyUserKey 是「按当前指纹算法算出的键在磁盘上没有对应文件」时沿用的既有键。
	// 见 adoptLegacyUserKey —— 它保证凭据指纹算法重建不会让老数据失联。
	legacyUserKey string
	// fpOnce 让「当前算法算出的指纹」只记一次日志（第一次拿到真请求时）。
	fpOnce sync.Once

	onlineMu    sync.Mutex
	onlineCache map[string]onlineCacheEntry

	// suggestShapeOnce 让「官方联想的 data 不是字符串数组」这件事只记一次日志
	// （见 suggest.go 的 noteSuggestShape）。
	suggestShapeOnce sync.Once

	lyricMu    sync.Mutex
	lyricCache map[string]lyricEntry

	vMu    sync.Mutex
	vCache map[string]vEntry

	// platformsStatic 是构造时定下的平台列表（Config.Platforms 为 nil 时用它）。
	platformsStatic []string

	// downloaded 记「哪些在线曲目已经落到本地了」—— 取流/元数据见到已登记的
	// 虚拟 guid 就直接服务本地文件，这就是「收藏自动绑定本地」。
	downloaded *online.Downloaded
	// favAutoDownload 是收藏自动下载的开关（现取，见 Config.FavAutoDownload）。
	favAutoDownload func() bool
	// dlMu/dlSet 保证「同一首歌只下一次」：收藏与加入歌单可能几乎同时触发。
	dlMu  sync.Mutex
	dlSet map[string]bool

	// tee 是「边听边下」被客户端打断之后的后台续传任务簿（见 tee.go 的 teePool）。
	// 零值可用 —— 只有真的起过一次续传才会建父 ctx 与登记表。
	tee teePool

	searcher     func(keyword, platform string, page, size int) []search.UnifiedSong
	lxSearch     func(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error)
	lyricFetch   func(t online.Track) string
	nmFetch      func(ctx context.Context, id string, limit int) (string, string, []online.Track)
	chartFetch   func(ctx context.Context, source, id string, limit int) (string, string, []online.Track, error)
	chartList    func() []string
	chartEnabled func() bool
	uiInject     func() bool
	localAPI     string

	statsMu sync.Mutex
	stats   map[string]int
}

// userEntry 缓存「凭据指纹 → 官方 user guid」的探测结果。
//
// 为什么要缓存：探测要打一次 `/user/me`，而搜索、元数据、收藏列表各自都会问
// 一次「当前用户是谁」。不缓存的话一次页面加载就能放大出好几次上游请求
// （fnmusic-ext 就有这个问题，见其 `_online_favorite_set`）。
type userEntry struct {
	guid   string
	expire time.Time
}

// New 组装拦截层。Upstream 为 nil 时返回 nil（调用方应视为「拦截未启用」）。
func New(cfg Config) *Interceptor {
	if cfg.Upstream == nil || strings.TrimSpace(cfg.DataDir) == "" {
		return nil
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	store, err := online.NewStore(cfg.DataDir)
	if err != nil {
		logf("[INTERCEPT] 存储目录不可用，在线功能停用：%v", err)
		return nil
	}
	// 已下载登记表：与 favorites/playlists/history 同目录。
	// ⚠️ 它读不出来**不算**在线功能不可用 —— 丢的只是「哪些已下载」，最坏结果是
	// 那几首又走在线流，不该让整个拦截层起不来。
	downloaded, dlErr := online.NewDownloaded(cfg.DataDir)
	if dlErr != nil {
		logf("[INTERCEPT] 已下载登记表不可用（收藏自动绑定会退化成在线流）：%v", dlErr)
		downloaded, _ = online.NewDownloaded(filepath.Join(os.TempDir(), "qulv-downloaded"))
	}
	platforms := cfg.Platforms
	if len(platforms) == 0 {
		platforms = defaultPlatforms
	}

	pool := cfg.Pool
	if pool == nil {
		pool = online.NewPool(online.NetEaseResolver{}, online.QQResolver{})
	}
	searcher := cfg.Searcher
	if searcher == nil {
		searcher = search.Search
	}
	lyricFetcher := cfg.LyricFetcher
	if lyricFetcher == nil {
		lyricFetcher = defaultLyricFetcher
	}

	i := &Interceptor{
		cfg:             cfg,
		logf:            logf,
		registry:        online.NewRegistry(0),
		pool:            pool,
		downloaded:      downloaded,
		dlSet:           map[string]bool{},
		favAutoDownload: cfg.FavAutoDownload,
		searcher:        searcher,
		lxSearch:        cfg.LxSearch,
		lyricFetch:      lyricFetcher,
		store:           store,
		userCache:       make(map[string]userEntry, 16),
		onlineCache:     make(map[string]onlineCacheEntry, 32),
		lyricCache:      make(map[string]lyricEntry, 64),
		vCache:          make(map[string]vEntry, 8),
		stats:           make(map[string]int, 32),
	}
	i.platformsStatic = platforms

	// 网易歌单取回：默认用内置实现，测试可注入替身（见 Config.NeteasePlaylist）。
	i.nmFetch = cfg.NeteasePlaylist
	if i.nmFetch == nil {
		i.nmFetch = i.neteasePlaylist
	}
	// 本机后端基地址：去掉可能的尾斜杠，免得拼出 `//api/...`。
	i.localAPI = strings.TrimRight(strings.TrimSpace(cfg.LocalAPI), "/")
	// 榜单内容：默认走本机回环（`/api/charts/detail`），测试可注入替身。
	i.chartFetch = cfg.ChartDetail
	if i.chartFetch == nil {
		i.chartFetch = i.chartDetailViaLocalAPI
	}
	// 勾选了哪些榜单由配置层现取 —— 榜单管理页保存后立刻生效，不需要重启。
	i.chartList = cfg.ChartPlaylists
	// 总开关同样是现取。nil（没接线）保持 nil —— 读取处按「一律视为开」处理。
	i.chartEnabled = cfg.ChartPlaylistsEnabled
	// 页面注入开关：同样是现取（设置里关掉立刻生效，官方页面立刻回到原样）。
	i.uiInject = cfg.UIInjectEnabled

	// 启动时重建虚拟 id 登记表：虚拟 id 是确定性哈希，所以把磁盘上出现过的
	// 曲目描述符重放一遍就恢复了全部映射。
	warm := store.AllTracks()
	i.registry.Warm(warm)
	logf("[INTERCEPT] 在线曲目登记表已重建：%d 条；参与的平台 %s；解析器 %s",
		len(warm), strings.Join(platforms, "/"), strings.Join(i.pool.Platforms(), "/"))

	i.routes = i.buildRoutes()
	i.adoptLegacyUserKey()
	// 清掉上次运行留下的半截暂存文件（见 tee.go 的 cleanTeeCache）。
	i.cleanTeeCache()
	return i
}

// ── 路由 ──────────────────────────────────────────────────────────────

// route 是一条拦截规则。methods 是允许的方法集合（空 = 不限）。
type route struct {
	methods map[string]bool
	prefix  string
	handler func(http.ResponseWriter, *http.Request) bool
}

// matches 判断请求是否命中这条规则。
//
// 前缀匹配允许带子路径（`/track/stream/<id>`），也允许正好等于前缀。
func (rt route) matches(r *http.Request) bool {
	if !rt.methods[r.Method] {
		return false
	}
	return r.URL.Path == rt.prefix || strings.HasPrefix(r.URL.Path, rt.prefix+"/")
}

// Handle 认领并处理请求。返回 true 表示响应已经写完，调用方不要再处理。
//
// 返回 false 只有两种情形：路径不在拦截清单里，或者 handler 主动决定透传。
func (i *Interceptor) Handle(w http.ResponseWriter, r *http.Request) bool {
	for _, rt := range i.routes {
		if rt.matches(r) {
			i.bump(r.Method + " " + rt.prefix)
			return rt.handler(w, r)
		}
	}
	return false
}

// bump 记一次命中计数（诊断用，见 Interceptor.stats）。
func (i *Interceptor) bump(key string) {
	i.statsMu.Lock()
	if i.stats == nil {
		i.stats = make(map[string]int)
	}
	i.stats[key]++
	i.statsMu.Unlock()
}

// buildRoutes 组装路由表。
//
// 顺序即优先级：长前缀在前（`/track/playlist-detail/list` 必须排在 `/track` 之前，
// 否则会被更短的规则吃掉）。这里显式按前缀长度降序排一次，免得以后加路由时靠人工维护。
func (i *Interceptor) buildRoutes() []route {
	all := map[string]bool{"GET": true, "POST": true, "DELETE": true, "HEAD": true}
	get := map[string]bool{"GET": true, "HEAD": true}
	post := map[string]bool{"POST": true}
	postDelete := map[string]bool{"POST": true, "DELETE": true}

	routes := []route{
		// ── 同源通道（见 qulvbridge.go）：注入到官方页面里的脚本走这几条。
		// 白名单之外的 `_qulv/` 路径由兜底路由吃成 404，**不转发给官方**
		// （官方会把未知路径当 SPA 路由返回 HTML，200 + HTML 比 404 难查）。
		{get, qulvPrefix + "/ui.js", i.handleQulvUIJS},
		{get, qulvPrefix + "/api/info", i.handleQulvInfo},
		{post, qulvPrefix + "/api/download/online", i.handleQulvDownloadOnline},
		{all, qulvPrefix, i.handleQulvNotFound},

		// ── 页面外壳：注入脚本的落点。
		{get, "/music", i.handlePageShell},

		// ── 曲目
		{get, apiPrefix + "/track/playlist-detail/list", i.handlePlaylistTrackList},
		{get, apiPrefix + "/track/stream", i.handleStream},
		{get, apiPrefix + "/track/metadata", i.handleMetadata},
		{get, apiPrefix + "/track/lyrics", i.handleLyricText},
		{get, apiPrefix + "/detail/lyrics", i.handleLyricText},
		{get, apiPrefix + "/search/track", i.handleSearch},
		// 搜索联想：官方库的联想词 + 在线源的前几条歌名（见 suggest.go）。
		// 前缀挂载 —— 官方也用 `/search/suggest/<keyword>` 这种带子路径的写法
		// （`route.matches` 允许「正好等于」与「前缀 + /」两种形态）。
		{get, apiPrefix + "/search/suggest", i.handleSuggest},
		{get, apiPrefix + "/static/cover", i.handleCover},

		// ── 歌词
		{get, apiPrefix + "/lyric/list", i.handleLyricList},

		// ── 收藏
		{post, apiPrefix + "/favorite-track/create", i.handleFavoriteCreate},
		{post, apiPrefix + "/favorite-track/delete", i.handleFavoriteDelete},
		{get, apiPrefix + "/favorite-track/list", i.handleFavoriteList},

		// ── 歌单
		{get, apiPrefix + "/playlist/batch-detail", i.handlePlaylistBatchDetail},
		{get, apiPrefix + "/playlist/detail", i.handlePlaylistDetail},
		{postDelete, apiPrefix + "/playlist/delete", i.handlePlaylistDelete},
		{post, apiPrefix + "/playlist/add-track", i.handlePlaylistAddTrack},
		{post, apiPrefix + "/playlist/remove-track", i.handlePlaylistRemoveTrack},
		{all, apiPrefix + "/playlist/list", i.handlePlaylistList},

		// ── 播放历史
		{get, apiPrefix + "/play-history/list", i.handleHistoryList},
		{postDelete, apiPrefix + "/play-history/delete", i.handleHistoryDelete},

		// ── 埋点：官方页面会发，我们只记不转。
		{post, apiPrefix + "/event/report", i.handleEventReport},

		// ── 兜底：官方端点比我们拦的多得多，`/music/api/v1` 下没被专门拦的一律
		// **认领并原样透传**（必须认领：放行会让上层再走一遍它自己的转发逻辑，
		// 两处转发迟早不一致）。
		{all, apiPrefix, i.handleApiPassThrough},
	}

	sort.SliceStable(routes, func(a, b int) bool {
		return len(routes[a].prefix) > len(routes[b].prefix)
	})
	return routes
}

// ── 凭据指纹 ──────────────────────────────────────────────────────────

// userKey 返回当前请求的**凭据指纹**，用作本地收藏 / 歌单 / 历史的分区键。
//
// ⚠️ 这个键决定「用户已有的在线收藏与歌单存在哪个文件里」。改它的算法 = 让老数据
// 全部看不见（文件还在，只是找不着了）。真机上的既有文件是 `fp-485d391520ecf02c.json`，
// 所以格式必须保持 `fp-` + 16 位小写十六进制。
func (i *Interceptor) userKey(r *http.Request) string {
	// ⚠️ 指纹要先算再判 legacy：如果放在 legacy 短路之后，真机（对账成功）就**永远
	// 算不到真实指纹**，那条诊断日志也就永远不打 —— 而它正是把算法收敛回正确值
	// 唯一的依据。第一次的代价可以忽略（sync.Once + 一次哈希）。
	fp := credentialFingerprint(r)
	i.fpOnce.Do(func() {
		i.logf("[INTERCEPT] 凭据指纹（当前算法）：%q；磁盘上沿用的是：%q", fp, i.legacyUserKey)
		// 把几种候选形态一起打出来，用来**收敛算法**（见 fpCandidates 的说明）。
		// 只打哈希，不打凭据本身。
		var b strings.Builder
		for _, c := range fpCandidates(r) {
			if c[1] == "" {
				continue
			}
			b.WriteString(" ")
			b.WriteString(c[0])
			b.WriteString("=")
			b.WriteString(strings.TrimPrefix(c[1], "fp-"))
		}
		if b.Len() > 0 {
			i.logf("[INTERCEPT] 指纹候选（哪一个等于磁盘上的 %s 就是原算法）：%s",
				strings.TrimPrefix(i.legacyUserKey, "fp-"), b.String())
		}
	})
	if i.legacyUserKey != "" {
		return i.legacyUserKey
	}
	if fp == "" {
		// 没有可区分的凭据（本机直连、测试、官方页面还没登录）→ 用共享桶。
		// 这与真机的 `fp-<16hex>` 是两种情形，不能混：拿一个「空凭据哈希」当键
		// 会让所有匿名请求各占一个分区。
		return "shared"
	}
	return fp
}

// credentialFingerprint 把请求里的凭据折成一个稳定指纹；没有凭据时返回空串。
//
// ⚠️ 真机既有数据文件名是 `fp-485d391520ecf02c.json`，格式必须保持
// `fp-` + 16 位小写十六进制。**改算法 = 老收藏/歌单全部看不见**（文件还在，只是找不着）。
func credentialFingerprint(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := sha256.New()
	found := false
	for _, name := range []string{"Authorization", "Cookie", "X-Token", "X-Auth-Token"} {
		if v := r.Header.Get(name); v != "" {
			_, _ = io.WriteString(h, name)
			_, _ = io.WriteString(h, ":")
			_, _ = io.WriteString(h, v)
			_, _ = io.WriteString(h, "\n")
			found = true
		}
	}
	if !found {
		return ""
	}
	return "fp-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// ── 透传 / 上游 ────────────────────────────────────────────────────────

// hopByHop 是逐跳头：不该在转发时原样带过去。
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func isHopByHop(name string) bool { return hopByHop[http.CanonicalHeaderKey(name)] }

// forward 把请求原样转给官方上游（保留方法 / 头 / body）。
func (i *Interceptor) forward(r *http.Request, body []byte) (*http.Response, error) {
	if i.cfg.Upstream == nil {
		return nil, errors.New("上游不可用")
	}
	raw := body
	if raw == nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		raw = b
	}
	// 相对 URL 不行 —— http.NewRequest 要绝对地址。占位 host 用 `unix`：
	// 真正的官方后端地址由 Upstream 实现（main 里那个）决定，这里只负责把
	// 方法 / 路径 / 头 / body 原样递过去。
	req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://unix"+r.URL.RequestURI(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// ⚠️ 必须保留原始 Host —— 官方可能按 Host 分租户，占位 host `unix` 不能泄漏上去。
	req.Host = r.Host
	return i.cfg.Upstream.Do(req)
}

// copyResponse 把上游响应原样回给客户端（跳过逐跳头）。
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	_ = resp.Body.Close()
}

// upstreamJSON 转发请求并把上游的 JSON 解析出来。
//
// 返回 (解析后的对象, 原始字节, 状态码, Content-Type, 是否拿到可解析的 JSON 对象)。
// 上游返回非 JSON（比如 HTML 错误页）时 ok = false，调用方据此决定怎么回。
func (i *Interceptor) upstreamJSON(r *http.Request, body []byte) (map[string]any, []byte, int, string, bool) {
	resp, err := i.forward(r, body)
	if err != nil {
		return nil, nil, http.StatusBadGateway, "", false
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, resp.StatusCode, ct, false
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, raw, resp.StatusCode, ct, false
	}
	// ⚠️ 官方 API 的业务错误是 **HTTP 200 + 非 0 的 code**（比如 160002「歌单已达上限」）。
	// 只看状态码会把这类失败当成成功，接着往本地桶里写 —— 于是出现「界面报错、歌单里却
	// 多了一首只有本机看得见的歌」这种最难排查的脏数据。所以 ok 必须同时要求 code == 0。
	if code, ok := obj["code"]; ok && asInt(code) != 0 {
		return obj, raw, resp.StatusCode, ct, false
	}
	return obj, raw, resp.StatusCode, ct, true
}

// passThrough 原样把请求转给官方上游。
func (i *Interceptor) passThrough(w http.ResponseWriter, r *http.Request) {
	resp, err := i.forward(r, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": 502, "msg": "上游不可用", "data": nil})
		return
	}
	copyResponse(w, resp)
}

// passThroughBody 与 passThrough 同理，但 body 已经被读过了，得用存下来的那份重放。
func (i *Interceptor) passThroughBody(w http.ResponseWriter, r *http.Request, raw []byte) {
	resp, err := i.forward(r, raw)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": 502, "msg": "上游不可用", "data": nil})
		return
	}
	copyResponse(w, resp)
}

// ── JSON / 响应小工具 ─────────────────────────────────────────────────

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// encodeJSON 序列化。关闭 HTML 转义（官方数据里可能有 `<`、`&`，转义后客户端读到的
// 字符串会变），并去掉行尾换行（Encode 会加一个）。
func encodeJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("{}")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// asString 取字符串。
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	}
	return ""
}

// asInt 取整数。
//
// 刻意**不做** float64 → int 的隐式转换判断：json.Number 是整数形态时才认。
func asInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// atoiDefault 解析整数，失败或为空时用默认值。
func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// queryOrBody 依次在查询串与请求体里找第一个非空取值。
func queryOrBody(r *http.Request, body map[string]any, keys ...string) string {
	if r != nil {
		q := r.URL.Query()
		for _, k := range keys {
			if v := strings.TrimSpace(q.Get(k)); v != "" {
				return v
			}
		}
	}
	return firstString(body, keys...)
}

// firstString 在 map 里按给定键序取第一个非空字符串。
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(asString(m[k])); v != "" {
			return v
		}
	}
	return ""
}

// writeJSON 写一个 JSON 应答。
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(encodeJSON(body))
}

// writeRaw 原样回传上游的字节与状态码。
func writeRaw(w http.ResponseWriter, raw []byte, status int, ct string) {
	if ct == "" {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// writeEnvelope 写一个官方形状的信封：传进来的 map 是 **data**，不是顶层字段。
func writeEnvelope(w http.ResponseWriter, data map[string]any) {
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "", "data": data})
}

// asBool 取布尔。只认真正的 bool —— 数字 0/1 与字符串 "true"/"false" 一概不认
// （官方不同端点对这些字段的类型并不一致，猜错会让「开关」类的判断静默反向）。
func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// handleApiPassThrough 是 `/music/api/v1` 下的兜底：没被专门拦的一律原样转给官方。
func (i *Interceptor) handleApiPassThrough(w http.ResponseWriter, r *http.Request) bool {
	i.passThrough(w, r)
	return true
}

// adoptLegacyUserKey 处理「凭据指纹算法重建后与磁盘上既有文件对不上」这件事。
//
// 背景：拦截层的凭据指纹算法在一次事故后是**重建**的（原实现被误删），而真机上已经
// 有 `favorites/fp-485d391520ecf02c.json` 这样的数据文件。指纹算得不一样，用户就会
// 看到「我的在线收藏全没了」—— 文件其实还在，只是程序找不着。
//
// 所以构造时做一次对账：目录里**恰好只有一个** `fp-*.json` 时就沿用那个键。
// 只在「唯一候选」时采纳 —— 多用户环境下宁可不猜（宁可让新算法生效，也不要让两个
// 用户共用一个桶）。
func (i *Interceptor) adoptLegacyUserKey() {
	// ⚠️ `cfg.DataDir` **本身就是 online 目录**（`main.go` 传的是
	// `filepath.Join(*dataDir, "online")`），store 的布局是它下面的
	// `favorites/<user>.json`。多拼一层 `online` 会指到一个不存在的目录 →
	// 对账永远找不到既有文件 → 老收藏照样看不见。
	dir := filepath.Join(i.cfg.DataDir, "favorites")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var found []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "fp-") || !strings.HasSuffix(n, ".json") {
			continue
		}
		found = append(found, strings.TrimSuffix(n, ".json"))
	}
	if len(found) != 1 {
		return
	}
	i.legacyUserKey = found[0]
	// 这里不打「当前算法算出什么」—— 构造时还没有请求，算出来必然是空串，
	// 打了反而像「算法算出了空」。真实值由 userKey 的第一次调用记录（见 fpOnce）。
	i.logf("[INTERCEPT] 沿用磁盘上已有的用户键 %s（凭据指纹算法重建后对账）", found[0])
}

// fpOf 把一段字符串算成 `fp-` + 16 位小写十六进制（与磁盘上的键同格式）。
func fpOf(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "fp-" + hex.EncodeToString(sum[:])[:16]
}

// fpOfMD5 / fpOfSHA1 是同一件事的另外两种算法。
//
// 为什么要试：16 位十六进制 = 8 字节，sha256 取前 8 字节是一种可能，**md5 取前 8 字节
// 同样是**（md5 是 16 字节），sha1 取前 8 字节也是。原实现被误删，不知道用的是哪个 ——
// 只能一起算出来跟磁盘上的键对照。
func fpOfMD5(s string) string {
	if s == "" {
		return ""
	}
	sum := md5.Sum([]byte(s))
	return "fp-" + hex.EncodeToString(sum[:])[:16]
}

func fpOfSHA1(s string) string {
	if s == "" {
		return ""
	}
	sum := sha1.Sum([]byte(s))
	return "fp-" + hex.EncodeToString(sum[:])[:16]
}

// fpCandidates 列出「凭据指纹可能哈希了什么」的几种形态。
//
// 只为**收敛算法**服务：原实现被误删，现在的算法是猜的。真机上当前算法算出
// `fp-3f2df32fa3c5c1a1`，而磁盘上的既有键是 `fp-485d391520ecf02c` —— 哪个候选等于
// 后者，原实现用的就是那种形态（头名 + 分隔符 + 顺序的组合）。
//
// 直接反推不行：知道输出、不知道输入。所以只能把候选一起算出来对照。
// **只打哈希，不打凭据本身** —— 日志不该出现 token/cookie 明文。
func fpCandidates(r *http.Request) [][2]string {
	auth := r.Header.Get("Authorization")
	cookie := r.Header.Get("Cookie")
	token := r.Header.Get("X-Token")
	authToken := r.Header.Get("X-Auth-Token")
	names := []string{"Authorization", "Cookie", "X-Token", "X-Auth-Token"}
	vals := []string{auth, cookie, token, authToken}

	var namedNL, namedAmp, bare strings.Builder
	for i, n := range names {
		if vals[i] == "" {
			continue
		}
		namedNL.WriteString(n)
		namedNL.WriteString(":")
		namedNL.WriteString(vals[i])
		namedNL.WriteString("\n")
		namedAmp.WriteString(n)
		namedAmp.WriteString("=")
		namedAmp.WriteString(vals[i])
		namedAmp.WriteString("&")
		bare.WriteString(vals[i])
	}

	inputs := [][2]string{
		{"named+NL", namedNL.String()},
		{"cookieRaw", cookie},
		{"bare", bare.String()},
		{"authRaw", auth},
		{"authNL", "Authorization:" + auth + "\n"},
		{"cookieNL", "Cookie:" + cookie + "\n"},
	}
	out := make([][2]string, 0, len(inputs)*3)
	for _, in := range inputs {
		out = append(out,
			[2]string{in[0] + ":sha256", fpOf(in[1])},
			[2]string{in[0] + ":md5", fpOfMD5(in[1])},
			[2]string{in[0] + ":sha1", fpOfSHA1(in[1])},
		)
	}
	return append(out, [][2]string{
		{"named+&", fpOf(namedAmp.String())},
		{"cookieAuthRaw", fpOf(cookie + auth)},
	}...)
}
