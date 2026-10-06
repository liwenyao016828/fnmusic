package intercept

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/online"
)

// 榜单歌单：把 `pkg/charts` 里的多平台排行榜挂成飞牛音乐里的在线歌单。
//
// # 从哪来
//
// 产品形态移植自 zouclang/fnos_music_ext v2.8.0 的 `proxy/charts.py` ——
// 「一个总开关 + 每榜可勾选，勾中的榜变成官方歌单列表里的卡片」。它把那 38 个
// 榜单（20 酷狗 + 18 网易云）写死在代码里。
//
// # 换掉了两处实现
//
//  1. **榜单目录不抄常量表**，直接用曲率自己的 `pkg/charts`（四平台实时榜单，
//     15 分钟缓存）。写死的 id 会随平台改版慢慢腐烂，而实时目录永远和用户在
//     「发现音乐」里看到的一致；顺带也不用维护那张表。
//  2. **只放有直链解析器的平台**（网易云 wy / QQ tx）。酷狗、酷我的榜单曲目
//     在曲率里解析不出可播地址（`online.Pool` 只注册了这两个），挂上去只会
//     变成「点开能看、点了不出声」的空壳 —— 那比没有更糟。
//
// # 与「每日推荐 / 热门推荐」共用同一条只读通道
//
// 卡片插在官方歌单列表最前；详情与曲目列表本地应答；写接口（加曲 / 移曲 /
// 删歌单）一律透传给官方 —— 虚拟歌单在本地一个字节都不落盘。
const vChartPrefix = "online:playlist:chart:"

const (
	// vChartSize 是单个榜单最多注入多少首**可播**曲目。
	//
	// 官方歌单首屏是整页全量交付的，所以这个数直接决定一次响应多大。
	// 100 与 fnmusic-ext 的口径一致。
	vChartSize = 100

	// vChartFetchMax 是一次从榜单详情里取多少首**候选**。
	//
	// 这个数必须比 vChartSize 大，而且大得有意义 —— 因为候选里只有一部分
	// 能解析出直链（免费曲目），先按 vChartSize 截断再过滤会把榜单砍成个位数。
	// 2026-10-06 真机实测（飞牛 NAS）：网易云「热歌榜」100 首候选里 **36 首**能播
	// （榜单越热门，VIP 比例越高），QQ「巅峰榜·热歌」300 首候选里仍然只有个位数 ——
	// QQ 的解析器本身弱（付费曲目拿不到 purl，文档记过约 33%，热门榜更低）。
	//
	// 上限取 300 是因为 `pkg/charts` 的 QQ 详情一次给 300 首（网易自己封顶 100），
	// 再多也拿不到。代价是解析批量更大（`neteaseMaxBatch` / `qqMaxBatch` 各 50，
	// 300 首 = 6 个批量请求），所以这条路径**算完就进 vCache**（30 分钟）。
	vChartFetchMax = 300

	// vChartParallel 是「同时抓几个榜单」的上限。
	//
	// 勾了 5 个榜，串行抓就是 5 次上游往返叠在**一次歌单列表请求**里；
	// 并行之后总耗时约等于一次往返。真忙的榜单也就十几个，不需要更宽。
	vChartParallel = 4

	// vChartNegTTL 是「空榜单」的缓存时长。
	//
	// 拉失败（上游抖动）和「整榜都不可播」都会算出 0 首。这种结果**必须缓存**，
	// 否则官方前端每进一次歌单页都会重新抓一遍所有空榜 —— 一个恒空的榜会变成
	// 每次请求都打上游。但也不能照 vCacheTTL 留 30 分钟：一次网络抖动就让榜单
	// 消失半小时太长。5 分钟是「抖动自己会好」与「别打上游」之间的折中。
	vChartNegTTL = 5 * time.Minute
)

// vChartPlatforms 是允许挂成榜单歌单的平台白名单。
//
// 判据是「`online.Pool` 有没有这个平台的解析器」，不是「曲率能不能列它的榜」。
// `pkg/charts` 四个平台都列得出来，但酷狗 / 酷我没有解析器。
var vChartPlatforms = map[string]bool{"wy": true, "tx": true}

// vChartRef 是一条被勾选的榜单。
type vChartRef struct {
	Source string // wy / tx
	ID     string // 平台内的榜单 id（如网易 19723756）
}

// key 是配置里存的形态：`<source>_<id>`，例如 `wy_19723756`。
func (r vChartRef) key() string { return r.Source + "_" + r.ID }

// guid 是这条榜单在飞牛里的歌单 id。
func (r vChartRef) guid() string { return vChartPrefix + r.key() }

// parseVChartRef 解析配置里的榜单键。
//
// 平台不认识 / id 为空都算无效 —— 配置层只做规范化，合法性判断留在这里，
// 这样以后加平台只改 `vChartPlatforms` 一处。
func parseVChartRef(key string) (vChartRef, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	src, id, found := strings.Cut(key, "_")
	if !found {
		return vChartRef{}, false
	}
	id = strings.TrimSpace(id)
	if !vChartPlatforms[src] || id == "" {
		return vChartRef{}, false
	}
	return vChartRef{Source: src, ID: id}, true
}

// enabledChartRefs 读出当前勾选的榜单（已去重、已剔除无效项）。
//
// `i.chartList` 是**函数**而不是值：榜单管理页保存后要立刻生效，拿启动时的
// 拷贝就等于「改完必须重启」。函数为 nil（未接线）时返回空 —— 也就是「没勾
// 任何榜」，这是最安全的行为。
//
// 总开关（`i.chartEnabled`）在最前面判：关掉时**连读都不读名单**就返回空。
// 名单本身照旧留在配置里，用户下次打开总开关时勾选原样还在。
func (i *Interceptor) enabledChartRefs() []vChartRef {
	if i.chartEnabled != nil && !i.chartEnabled() {
		return nil
	}
	if i.chartList == nil {
		return nil
	}
	keys := i.chartList()
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	out := make([]vChartRef, 0, len(keys))
	for _, k := range keys {
		ref, ok := parseVChartRef(k)
		if !ok || seen[ref.key()] {
			continue
		}
		seen[ref.key()] = true
		out = append(out, ref)
	}
	return out
}

// chartPlaylist 生成一条榜单歌单。
//
// 曲目经 `playableOnly` 过滤 —— 与搜索、每日推荐同一条口径：**进列表的每一首
// 都必须能播**。代价是榜单长度不再等于真实榜单长度（QQ 侧尤其明显，它直链能
// 解析出来的只有一部分），换来的是「点开一首、放不出声」不会发生。
func (i *Interceptor) chartPlaylist(ctx context.Context, ref vChartRef) vPlaylist {
	name, _, tracks, err := i.chartFetch(ctx, ref.Source, ref.ID, vChartFetchMax)
	if err != nil {
		i.logf("[INTERCEPT] 榜单 %s 拉取失败：%v", ref.key(), err)
		return vPlaylist{}
	}
	// 上游一次给多少不由我们决定，先按 vChartFetchMax 兜一层。
	if len(tracks) > vChartFetchMax {
		tracks = tracks[:vChartFetchMax]
	}
	for n := range tracks {
		tracks[n] = tracks[n].Normalized()
	}
	// ⚠️ 顺序是「先过滤、后截断」，不能反 —— 候选里能播的只占一部分
	// （真机实测：网易热门榜 100 首里 36 首，QQ 更低），先截到 100 再过滤
	// 等于把榜单砍成个位数。截断放在过滤之后，才有「凑满 100 首可播」的机会。
	tracks = i.playableOnly(ctx, ref.Source, tracks)
	if len(tracks) == 0 {
		return vPlaylist{}
	}
	if len(tracks) > vChartSize {
		tracks = tracks[:vChartSize]
	}

	name = strings.TrimSpace(name)
	if name == "" {
		// 上游没给名字时用榜 id 兜底，总比卡片上空白强。
		name = ref.key()
	}

	// 时间戳取「今天零点」而不是「现在」：榜单是每天刷新的，卡片上的
	// updatedAt 不该每缓存过期一次就跳一下。
	ts := vChartDayStart()

	return vPlaylist{
		Name:      name,
		CreatedAt: ts,
		UpdatedAt: ts,
		Tracks:    tracks,
	}
}

// chartCards 并行算出所有已勾选榜单的卡片，返回顺序与勾选顺序一致。
//
// 顺序稳定是有意的：官方歌单列表里卡片的位置不该每次刷新都换一换。
func (i *Interceptor) chartCards(ctx context.Context, r *http.Request) []vPlaylist {
	refs := i.enabledChartRefs()
	if len(refs) == 0 {
		return nil
	}
	pls := make([]vPlaylist, len(refs))
	filled := make([]bool, len(refs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, vChartParallel)
	for n, ref := range refs {
		wg.Add(1)
		go func(n int, ref vChartRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pl, ok := i.vPlaylistFor(ref.guid(), r)
			if ok && len(pl.Tracks) > 0 {
				pls[n] = pl
				filled[n] = true
			}
		}(n, ref)
	}
	wg.Wait()

	out := make([]vPlaylist, 0, len(refs))
	for n := range pls {
		if filled[n] {
			out = append(out, pls[n])
		}
	}
	return out
}

// vChartDayStart 返回本地时区今天 00:00 的 Unix 时间戳。
//
// 不能用 `time.Now().Truncate(24*time.Hour)` —— `Truncate` 是相对**绝对时间**
// （UTC 零时）截断的，在东八区会得到当天 08:00。
func vChartDayStart() int64 {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

// ── 榜单内容：走本机回环 ────────────────────────────────────────────────

// chartDetailViaLocalAPI 从本机曲率后端的 `/api/charts/detail` 取一条榜单。
//
// # 为什么走 HTTP 而不是直接调 `pkg/charts`
//
// 那边四个平台的详情抓取都是「解析完直接写 http.ResponseWriter」的形态，要拿到
// 数据得把四个函数都拆成「取数 + 写响应」两半 —— 那是重写几百行解析代码的风险，
// 而这条路的成本只是一次本机回环请求。解析代码只留一份，行为天然一致。
//
// 地址用的是启动时定死的 `LocalAPI`（回环），**不接受请求里的任何 host** ——
// 这条通道不引入 SSRF 面。
func (i *Interceptor) chartDetailViaLocalAPI(ctx context.Context, source, id string, limit int) (string, string, []online.Track, error) {
	if i.localAPI == "" {
		return "", "", nil, errors.New("本机曲率后端地址未知（未接线 LocalAPI）")
	}
	endpoint := i.localAPI + "/api/charts/detail?source=" + urlQueryEscape(source) + "&id=" + urlQueryEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", nil, err
	}
	resp, err := online.SharedClient().Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("榜单接口返回 HTTP %d", resp.StatusCode)
	}

	var payload chartDetailPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return "", "", nil, err
	}
	if payload.Code != 200 {
		return "", "", nil, fmt.Errorf("榜单接口返回 code=%d", payload.Code)
	}

	songs := payload.Data.Songs
	if limit > 0 && len(songs) > limit {
		songs = songs[:limit]
	}
	tracks := make([]online.Track, 0, len(songs))
	for _, s := range songs {
		if t, ok := s.track(source); ok {
			tracks = append(tracks, t)
		}
	}
	return payload.Data.Name, payload.Data.Cover, tracks, nil
}

// chartDetailPayload 是 `/api/charts/detail` 的响应形状（只取我们用的字段）。
type chartDetailPayload struct {
	Code int `json:"code"`
	Data struct {
		Name  string          `json:"name"`
		Cover string          `json:"cover"`
		Songs []chartSongItem `json:"songs"`
	} `json:"data"`
}

// chartSongItem 是 `pkg/charts` 的 `ChartSongItem`（字段名与那边一致）。
type chartSongItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Singer   string `json:"singer"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Cover    string `json:"cover"`
	Source   string `json:"source"`
	Songmid  string `json:"songmid"`
}

// track 把榜单曲目转成在线曲目。
//
// 平台内 id 优先用 `songmid`（QQ 侧它是真主键），没有才从 `id` 上剥平台前缀：
// `pkg/charts` 的 `id` 是 `<平台>_<平台内 id>` 的复合形态（如 `wy_1234567`），
// 直接当平台内 id 用会让解析器拿着 `wy_1234567` 去问网易要 `1234567`，必然失败。
func (s chartSongItem) track(fallbackSource string) (online.Track, bool) {
	src := strings.ToLower(strings.TrimSpace(s.Source))
	if src == "" {
		src = fallbackSource
	}
	if !vChartPlatforms[src] {
		return online.Track{}, false
	}

	pid := strings.TrimSpace(s.Songmid)
	if pid == "" {
		pid = strings.TrimPrefix(strings.TrimSpace(s.ID), src+"_")
	}
	title := strings.TrimSpace(s.Name)
	if pid == "" || title == "" {
		return online.Track{}, false
	}

	return online.Track{
		Platform:   src,
		PlatformID: pid,
		Title:      title,
		// 复用搜索那条路的拆分规则（search.go 的 splitArtists）：榜单曲目的
		// `singer` 在 `pkg/charts` 里就是用 `, ` 拼出来的，与搜索结果同一来源。
		// 一个包内两套拆分规则比「AC/DC 被拆开」更让人困惑。
		Artists:  splitArtists(s.Singer),
		Album:    strings.TrimSpace(s.Album),
		Duration: s.Duration,
		CoverURL: strings.TrimSpace(s.Cover),
	}.Normalized(), true
}
