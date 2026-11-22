package online

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// musicdl sidecar 的客户端。
//
// # 为什么是「外挂进程」而不是原生 Go 实现
//
// musicdl 覆盖十几家音源，靠的是**跟着各平台改**的一套爬取逻辑（还带 curl_cffi
// 做 TLS 指纹伪装）。在 Go 里复刻它等于长期维护十几个会随时失效的解析器 ——
// 而酷狗 / 酷我 我们已经试过：原生拿不到（`err_code 30020` / `The request is illegal!`），
// 不是写得不认真，是风控。
//
// 所以分工是：**原生的（wy / tx）继续原生**（快、稳、无外部依赖），
// 剩下的一律交给 sidecar。对 `Pool` 来说两者没有区别 —— 都是 `Resolver`。
//
// # 契约（对应 sidecar 的 app.py）
//
//	GET  /healthz → {"ok","version","musicdl","sources","cached"}
//	POST /search  {"keyword","sources","limit"} → {"ok","items","errors"}
//	POST /resolve {"items":[{id,source,title,artist}]} → {"ok","items","errors"}
//
// # 解析为什么必须带歌名/歌手
//
// musicdl 的直链是**搜索结果对象上的字段**，不是「拿 id 去问」能拿到的。所以
// `ResolveQueries`（带关键词）才是正路；只给 id 的 `Resolve` / `ResolveBatch`
// 只能命中 sidecar 的进程内缓存 —— 我们仍然实现它们（接口要求），但要清楚
// 那条路是尽力而为，不是主路径。
const (
	// musicdlSearchTimeout 是整次搜索的预算。
	//
	// 比单条解析长得多：sidecar 内部会对每个源并发跑（单源预算 25s），
	// 而搜索是「用户点一下在等」的操作，给到 40s 是为了让最慢的那个源也能
	// 把结果交出来 —— 超过这个数说明 sidecar 本身出问题了。
	// ⚠️ 2026-10-08：40 → 135s。必须**大于** sidecar 的单源预算（现为 120s），
	// 否则整次搜索会先于最慢那个源被掐掉（差一点点最亏）。
	//
	// 为什么敢给这么长：这条路只服务**下载**（用户口径：收藏下载不必最快），
	// 而试听那条腿被 intercept 的 onlineSearchWait(3s) / onlineSearchBudget(8s)
	// 独立掐着，不受这里影响。
	musicdlSearchTimeout = 135 * time.Second
	// musicdlResolveTimeout 是批量解析的预算（每条还要回搜一次）。
	musicdlResolveTimeout = 60 * time.Second
	// musicdlHealthTimeout 要短：健康检查是给「现在能不能用」下结论的，
	// 卡住不返回等于把调用方也卡住。
	musicdlHealthTimeout = 3 * time.Second
	// musicdlFailCooldownBase 是一个源失败一次后要冷却多久。
	//
	// 为什么要有熔断：musicdl 的源是十几个互不相干的第三方站，其中总有站点抽风
	// （改版、被墙、超时）。不熔断的话每次搜索都会把时间平均摊给所有源，
	// 一个坏源就能把整次搜索拖到它的超时上限（实测慢源 3~15s，坏源直接跑满预算）。
	musicdlFailCooldownBase = 2 * time.Minute
	// musicdlFailCooldownMax 是冷却上限：连续失败越多冷却越久，但有顶，
	// 免得某个源被永久拉黑（它的站可能十分钟后就修好了）。
	musicdlFailCooldownMax = 15 * time.Minute
)

// MusicDL 是 sidecar 的客户端。一个实例覆盖多个平台（sidecar 内部按源名区分）。
type MusicDL struct {
	base   string
	client *http.Client
	logf   func(format string, args ...any)

	// health 记每个源最近的表现，用来熔断。
	//
	// 熔断的判据只认**明确的失败**（sidecar 报不可用 / 参数错，或我们这次等到超时）；
	// 「搜到了 0 条」**不算**失败 —— 那个源可能只是没有这首歌（实测 16 个源里
	// 有一半搜「海阔天空 beyond」能出结果、搜别的歌就 0 条，那是正常的）。
	mu     sync.Mutex
	health map[string]*musicdlSourceHealth
}

// musicdlSourceHealth 是一个源的健康记录。
type musicdlSourceHealth struct {
	fails       int       // 连续失败次数
	cooldownEnd time.Time // 冷却到什么时候
	lastFail    time.Time
	lastOK      time.Time
}

// NewMusicDL 组装客户端。base 形如 `http://127.0.0.1:8901`。
func NewMusicDL(base string, logf func(string, ...any)) *MusicDL {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &MusicDL{
		base:   strings.TrimRight(strings.TrimSpace(base), "/"),
		client: &http.Client{Timeout: musicdlResolveTimeout},
		logf:   logf,
		health: map[string]*musicdlSourceHealth{},
	}
}

// ── 单源等待预算（走 ctx）───────────────────────────────────────────────────
//
// 为什么预算要由调用方给：同一份搜索代码有两条腿，口径正好相反。
//
//	下载腿：宁可慢也要完整候选 —— musicdl 会对**每条结果**解析一次直链
//	        （sidecar 注释里记着咪咕 3 条约 49s、网易 3 条约 162s），
//	        给 3s 等于把取音池掐死。
//	探测腿（替换音质扫描）：只要「有没有更高档位」这一个答案，慢源就是纯浪费。
//
// 两条腿用的是同一个 Search，所以「每个源最多等多久」必须随 ctx 走，不能写死在客户端里。
type sourceBudgetKey struct{}

// WithSourceBudget 告诉 musicdl 客户端「这次每个源最多等多久」。
func WithSourceBudget(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil || d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, sourceBudgetKey{}, d)
}

// SourceBudget 取出 ctx 里的单源等待预算（没有 = 不限速，走慢口径）。
func SourceBudget(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	d, ok := ctx.Value(sourceBudgetKey{}).(time.Duration)
	return d, ok && d > 0
}

// sourceAllowed 这个源现在能不能问（不在冷却期里）。
func (m *MusicDL) sourceAllowed(src string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health[src]
	if h == nil {
		return true
	}
	return !h.cooldownEnd.After(time.Now())
}

// markSourceFailed 记一次失败并进入冷却。
func (m *MusicDL) markSourceFailed(src, reason string) {
	if src == "" {
		return
	}
	m.mu.Lock()
	h := m.health[src]
	if h == nil {
		h = &musicdlSourceHealth{}
		m.health[src] = h
	}
	h.fails++
	h.lastFail = time.Now()
	backoff := musicdlFailCooldownBase
	for i := 1; i < h.fails; i++ {
		backoff *= 2
		if backoff >= musicdlFailCooldownMax {
			break
		}
	}
	if backoff > musicdlFailCooldownMax {
		backoff = musicdlFailCooldownMax
	}
	h.cooldownEnd = h.lastFail.Add(backoff)
	fails := h.fails
	m.mu.Unlock()
	m.logf("[MUSICDL] 源 %s 连续第 %d 次失败（%s），接下来 %s 内跳过它", src, fails, reason, backoff)
}

// markSourceOK 成功一次就清掉冷却与失败计数。
func (m *MusicDL) markSourceOK(src string) {
	if src == "" {
		return
	}
	m.mu.Lock()
	if h := m.health[src]; h != nil {
		h.fails = 0
		h.cooldownEnd = time.Time{}
		h.lastOK = time.Now()
	}
	m.mu.Unlock()
}

// SourceCooldownRemaining 这个源还要冷却多久（0 = 现在就能问）。
//
// 给「音源管理」页与测试用：界面上要能看出「这个源刚被熔断，还差多久恢复」。
func (m *MusicDL) SourceCooldownRemaining(src string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health[strings.ToLower(strings.TrimSpace(src))]
	if h == nil {
		return 0
	}
	if d := time.Until(h.cooldownEnd); d > 0 {
		return d
	}
	return 0
}

// Base 返回 sidecar 的基地址（空串 = 没配）。
func (m *MusicDL) Base() string { return m.base }

// MusicDLHealth 是 /healthz 的结果。
type MusicDLHealth struct {
	OK      bool     `json:"ok"`
	Version string   `json:"version"`
	MusicDL bool     `json:"musicdl"` // musicdl 这个库本身装好没有
	Error   string   `json:"musicdl_error"`
	Sources []string `json:"sources"`
	Cached  int      `json:"cached"`
}

// Health 问一次 sidecar。返回错误 = 连不上 / 不是它 / 它自己说不行。
func (m *MusicDL) Health(ctx context.Context) (*MusicDLHealth, error) {
	if m.base == "" {
		return nil, errors.New("没有配置 musicdl sidecar 地址")
	}
	ctx, cancel := context.WithTimeout(ctx, musicdlHealthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar 健康检查返回 %d", resp.StatusCode)
	}
	var out MusicDLHealth
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("sidecar 健康检查响应不是 JSON：%w", err)
	}
	if !out.OK {
		return nil, errors.New("sidecar 自报不健康")
	}
	return &out, nil
}

// MusicDLSong 是 sidecar 返回的一条搜索结果。
type MusicDLSong struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	PlatformID  string `json:"platform_id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	DurationS   int    `json:"duration_s"`
	Ext         string `json:"ext"`
	FileSize    int64  `json:"file_size"`
	CoverURL    string `json:"cover_url"`
	DownloadURL string `json:"download_url"`
	AlbumMID    string `json:"album_mid"`
}

// MusicDLSideError 是 sidecar 对**单个源 / 单条曲目**的错误条目。
//
// 它存在的意义是把「这个平台当前不可用」与「我们参数写错了」分开：前者只是
// 少一个平台，后者是我们自己的 bug，日志里必须能一眼看出来。
type MusicDLSideError struct {
	Kind    string `json:"kind"` // unavailable | invalid
	Message string `json:"message"`
}

func (e MusicDLSideError) Error() string {
	if e.Message == "" {
		return e.Kind
	}
	return e.Kind + ": " + e.Message
}

// IsInvalid 报告这是「我们参数错了」而不是「平台不可用」。
func (e MusicDLSideError) IsInvalid() bool { return e.Kind == "invalid" }

type musicdlSearchResp struct {
	OK     bool                        `json:"ok"`
	Items  []MusicDLSong               `json:"items"`
	Errors map[string]MusicDLSideError `json:"errors"`
}

type musicdlResolveResp struct {
	OK     bool                        `json:"ok"`
	Items  []musicdlResolvedItem       `json:"items"`
	Errors map[string]MusicDLSideError `json:"errors"`
}

type musicdlResolvedItem struct {
	ID       string            `json:"id"`
	Source   string            `json:"source"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Ext      string            `json:"ext"`
	FileSize int64             `json:"file_size"`
	Duration int               `json:"duration_s"`
	CoverURL string            `json:"cover_url"`
	Lyric    string            `json:"lyric"`
}

// postJSON 发一个 POST 并把响应解到 out。
func (m *MusicDL) postJSON(ctx context.Context, path string, payload any, out any, timeout time.Duration) error {
	if m.base == "" {
		return errors.New("没有配置 musicdl sidecar 地址")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// sidecar 的参数错误是 400 + {"error": "..."} —— 原样带出来，别吞掉
		return fmt.Errorf("sidecar %s 返回 %d：%s", path, resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sidecar %s 响应不是 JSON：%w", path, err)
	}
	return nil
}

// getJSON 发一个 GET 并把响应解到 out。
func (m *MusicDL) getJSON(ctx context.Context, path string, out any, timeout time.Duration) error {
	if m.base == "" {
		return errors.New("没有配置 musicdl sidecar 地址")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sidecar %s 返回 %d：%s", path, resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sidecar %s 响应不是 JSON：%w", path, err)
	}
	return nil
}

// MusicDLSource 是 sidecar 报上来的一个源（给「音源管理」页）。
type MusicDLSource struct {
	ID      string `json:"id"`      // 曲率短码（`mg` / `apple` / `spotify`）
	Label   string `json:"label"`   // 界面显示名
	Client  string `json:"client"`  // musicdl 的客户端全名
	Enabled bool   `json:"enabled"` // 当前是否启用
	Native  bool   `json:"native"`  // 是否与曲率原生平台重叠（wy/tx/kg/kw）
}

type musicdlSourcesResp struct {
	OK         bool            `json:"ok"`
	MusicDL    bool            `json:"musicdl"`
	Registered int             `json:"registered"`
	Enabled    []string        `json:"enabled"`
	Sources    []MusicDLSource `json:"sources"`
}

// MusicDLSourceList 是 `/sources` 的结果。
type MusicDLSourceList struct {
	// MusicDL 表示 sidecar 里 musicdl 这个库本身装好没有（没装就只能列出别名）。
	MusicDL bool `json:"musicdl"`
	// Registered 是 musicdl 报出来的源总数（真机上是 56）。
	Registered int             `json:"registered"`
	Enabled    []string        `json:"enabled"`
	Sources    []MusicDLSource `json:"sources"`
}

// Sources 问 sidecar 要源清单。
func (m *MusicDL) Sources(ctx context.Context) (*MusicDLSourceList, error) {
	var resp musicdlSourcesResp
	if err := m.getJSON(ctx, "/sources", &resp, musicdlHealthTimeout*2); err != nil {
		return nil, err
	}
	return &MusicDLSourceList{
		MusicDL: resp.MusicDL, Registered: resp.Registered,
		Enabled: resp.Enabled, Sources: resp.Sources,
	}, nil
}

// MusicDLProbeResult 是单个源的检测结果。
type MusicDLProbeResult struct {
	ID    string            `json:"id"`
	MS    int               `json:"ms"`
	Items int               `json:"items"`
	OK    bool              `json:"ok"`
	Error *MusicDLSideError `json:"error,omitempty"`
}

type musicdlProbeResp struct {
	OK      bool                 `json:"ok"`
	Keyword string               `json:"keyword"`
	Results []MusicDLProbeResult `json:"results"`
}

// Probe 让 sidecar 对指定源各做一次**真实搜索**，回报耗时与条数。
//
// 为什么要真搜：「注册了」不代表「能用」—— 真机上 56 个源里有一大半在国内
// 出不了结果（超时、被墙、站方改版）。这个接口是「音源管理」页最有用的东西。
func (m *MusicDL) Probe(ctx context.Context, sources []string, keyword string, limit int) ([]MusicDLProbeResult, error) {
	payload := map[string]any{}
	if len(sources) > 0 {
		payload["sources"] = sources
	}
	if strings.TrimSpace(keyword) != "" {
		payload["keyword"] = keyword
	}
	if limit > 0 {
		payload["limit"] = limit
	}
	var resp musicdlProbeResp
	// 检测是「每个源各跑一次」：给足时间（单源预算由 sidecar 定），但不能无限等。
	if err := m.postJSON(ctx, "/probe", payload, &resp, musicdlResolveTimeout); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// Search 在 sidecar 覆盖的平台上搜一批。
//
// 单个源出错**不算整次失败**：把出错的源记进日志，把其余源的结果返回 ——
// 用户要的是「少一个平台」，不是「搜索坏了」。全部源都失败才返回错误。
//
// 走的是**慢口径**（不限单源等待）：这条路主要服务下载，宁可慢也要完整候选。
func (m *MusicDL) Search(ctx context.Context, keyword string, sources []string, limit int) ([]MusicDLSong, error) {
	return m.search(ctx, keyword, sources, limit, 0)
}

// SearchWithBudget 同 Search，但给每个源一个**等待上限**（探测腿用）。
//
// 预算只掐「我们等多久」，不掐 sidecar 内部的工作量 —— 它那边的线程会继续跑到
// 自己的单源预算为止，我们只是不等它了。
func (m *MusicDL) SearchWithBudget(ctx context.Context, keyword string, sources []string, limit int, budget time.Duration) ([]MusicDLSong, error) {
	return m.search(ctx, keyword, sources, limit, budget)
}

func (m *MusicDL) search(ctx context.Context, keyword string, sources []string, limit int, budget time.Duration) ([]MusicDLSong, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, nil
	}

	// 熔断：冷却期内的源**这一轮不问**（问它就是白等它超时）。
	ask := make([]string, 0, len(sources))
	skipped := make([]string, 0, len(sources))
	for _, src := range sources {
		src = strings.ToLower(strings.TrimSpace(src))
		if src == "" {
			continue
		}
		if !m.sourceAllowed(src) {
			skipped = append(skipped, src)
			continue
		}
		ask = append(ask, src)
	}
	if len(ask) == 0 {
		if len(skipped) > 0 {
			m.logf("[MUSICDL] %d 个源都在冷却期，本次不再问：%s", len(skipped), strings.Join(skipped, "/"))
		}
		return nil, nil
	}

	payload := map[string]any{"keyword": keyword}
	if len(ask) > 0 {
		payload["sources"] = ask
	}
	if limit > 0 {
		payload["limit"] = limit
	}

	timeout := musicdlSearchTimeout
	if budget > 0 && budget < timeout {
		timeout = budget
	}

	var resp musicdlSearchResp
	if err := m.postJSON(ctx, "/search", payload, &resp, timeout); err != nil {
		// 整次超时/失败：这次问过的源全部记一次失败（分不清是哪个慢，就都算上）。
		for _, src := range ask {
			m.markSourceFailed(src, err.Error())
		}
		return nil, err
	}

	// 有结果的源 = 这次是好的；报错的源 = 这次坏了。
	ok := map[string]bool{}
	for _, it := range resp.Items {
		if src := strings.ToLower(strings.TrimSpace(it.Source)); src != "" {
			ok[src] = true
		}
	}
	errs := make(map[string]MusicDLSideError, len(resp.Errors))
	for k, v := range resp.Errors {
		errs[strings.ToLower(strings.TrimSpace(k))] = v
	}
	for src, e := range errs {
		if e.IsInvalid() {
			// 参数错是我们的问题，喊出来
			m.logf("[MUSICDL] 搜索源 %s 参数错误：%v", src, e)
			m.markSourceFailed(src, e.Message)
			continue
		}
		m.logf("[MUSICDL] 搜索源 %s 当前不可用：%v", src, e)
		m.markSourceFailed(src, e.Message)
	}
	for src := range ok {
		if _, bad := errs[src]; !bad {
			m.markSourceOK(src)
		}
	}
	if len(resp.Items) == 0 && len(resp.Errors) > 0 {
		return nil, fmt.Errorf("musicdl 的 %d 个源全部不可用", len(resp.Errors))
	}
	return resp.Items, nil
}

// MusicDLQuery 是「要解析哪一条」。
type MusicDLQuery struct {
	ID     string `json:"id"`
	Source string `json:"source,omitempty"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
}

// Resolve 批量解析，返回 **按请求 id 索引** 的结果（只含成功的）。
//
// 出错分两类：单条失败（进 errors，不影响别人）与整次失败（返回 error）。
func (m *MusicDL) Resolve(ctx context.Context, queries []MusicDLQuery) (map[string]*Resolved, error) {
	out := make(map[string]*Resolved, len(queries))
	if len(queries) == 0 {
		return out, nil
	}

	var resp musicdlResolveResp
	if err := m.postJSON(ctx, "/resolve", map[string]any{"items": queries}, &resp, musicdlResolveTimeout); err != nil {
		return nil, err
	}
	for id, e := range resp.Errors {
		if e.IsInvalid() {
			m.logf("[MUSICDL] 解析 %s 参数错误：%v", id, e)
			continue
		}
		m.logf("[MUSICDL] 解析 %s 失败：%v", id, e)
	}
	for _, it := range resp.Items {
		if it.URL == "" {
			continue
		}
		out[it.ID] = &Resolved{
			URL:     it.URL,
			Format:  strings.ToLower(strings.TrimSpace(it.Ext)),
			Size:    it.FileSize,
			Expires: time.Time{}, // sidecar 不给过期时间：直链当场用，不缓存太久（见 Pool 的 TTL）
			Headers: MediaHeaders(it.Headers),
		}
	}
	return out, nil
}

// MusicDLResolver 把 sidecar 绑定到**某一个平台**上，于是它能当 Resolver 用。
//
// 为什么要按平台拆：`Pool` 的索引是「平台 → 解析器」，而一个 sidecar 覆盖多个
// 平台。拆成一层薄壳（共用同一个 `MusicDL`，也就是共用连接与配置）最省事，
// 也让「某个平台单独换实现」变成可能。
type MusicDLResolver struct {
	m        *MusicDL
	platform string
}

// Resolver 返回绑定到 platform 的解析器。
func (m *MusicDL) Resolver(platform string) MusicDLResolver {
	return MusicDLResolver{m: m, platform: strings.ToLower(strings.TrimSpace(platform))}
}

// Platform 实现 Resolver。
func (r MusicDLResolver) Platform() string { return r.platform }

// Resolve 实现 Resolver —— 只有 id，没有歌名/歌手。
//
// ⚠️ 这是**尽力而为**的一条路：musicdl 的直链只能从搜索拿到，所以这里只能命中
// sidecar 的进程内缓存。要可靠解析请用 ResolveQueries（Pool.ResolveTracks 会走它）。
func (r MusicDLResolver) Resolve(ctx context.Context, platformID string) (*Resolved, error) {
	got, err := r.ResolveBatch(ctx, []string{platformID})
	if err != nil {
		return nil, err
	}
	if res, ok := got[platformID]; ok {
		return res, nil
	}
	return nil, ErrNotPlayable
}

// ResolveBatch 实现 Resolver（同样是「只有 id」的那条路）。
func (r MusicDLResolver) ResolveBatch(ctx context.Context, platformIDs []string) (map[string]*Resolved, error) {
	queries := make([]MusicDLQuery, 0, len(platformIDs))
	for _, id := range platformIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		queries = append(queries, MusicDLQuery{
			ID:     musicdlSongID(r.platform, id),
			Source: r.platform,
		})
	}
	bySideID, err := r.m.Resolve(ctx, queries)
	if err != nil {
		return nil, err
	}
	// sidecar 的 id 是 `<短码>:<平台内 id>`，而 Pool 的键是平台内 id —— 换回来。
	out := make(map[string]*Resolved, len(bySideID))
	for _, id := range platformIDs {
		if res, ok := bySideID[musicdlSongID(r.platform, id)]; ok {
			out[id] = res
		}
	}
	return out, nil
}

// ResolveQueries 实现 QueryResolver —— **带歌名/歌手的正路**。
func (r MusicDLResolver) ResolveQueries(ctx context.Context, qs []Query) (map[string]*Resolved, error) {
	queries := make([]MusicDLQuery, 0, len(qs))
	for _, q := range qs {
		if strings.TrimSpace(q.ID) == "" {
			continue
		}
		queries = append(queries, MusicDLQuery{
			ID:     musicdlSongID(r.platform, q.ID),
			Source: r.platform,
			Title:  q.Title,
			Artist: q.Artist,
		})
	}
	bySideID, err := r.m.Resolve(ctx, queries)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Resolved, len(bySideID))
	for _, q := range qs {
		if res, ok := bySideID[musicdlSongID(r.platform, q.ID)]; ok {
			out[q.ID] = res
		}
	}
	return out, nil
}

// musicdlSongID 与 sidecar 的 `core.song_id` 保持同一拼法。
//
// ⚠️ 两边必须一起改：这个 id 是跨进程的键，对不上就全部解析失败。
func musicdlSongID(platform, platformID string) string {
	return platform + ":" + platformID
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
