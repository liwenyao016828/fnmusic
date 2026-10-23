package backenddl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// LxHost 是「服务端洛雪宿主」需要的那几个方法（`*lxnode.Manager` 直接满足）。
//
// 用窄接口而不是直接吃 `*lxnode.Manager`，是为了让单测能塞一个假宿主进来 ——
// 真宿主是个 Node 子进程，测试里起不来。
type LxHost interface {
	SearchAll(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error)
	ResolveCandidates(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error)
	// Status 只用来回答「宿主现在能不能用」（`{"state":"ready"}` 之类）。
	Status() map[string]any
}

// FetcherConfig 组装一个「取音 + 落盘」的能力。
type FetcherConfig struct {
	// Pool 是平台直链解析器（网易/QQ + musicdl 注册进来的那些）。纯 Go、
	// 不需要任何外部进程 —— 这是后台下载能「关掉网页也照跑」的底气。
	Pool *online.Pool
	// Lx 是服务端洛雪宿主（可 nil）。用户没开「服务端洛雪宿主」时它是 nil，
	// 这时只能走平台直链：能下，但冷门歌/无损档可能拿不到。
	Lx LxHost
	// Scripts 返回用户选中并排好序的洛雪音源脚本名（可 nil）。
	Scripts func() []string
	// Channels 返回生效的取音渠道顺序（nil = config.DefaultDownloadChannels）。
	Channels func() []string
	// DownloadSources 返回 musicdl 启用的平台短码（nil/空 = 外挂池不可用）。
	DownloadSources func() []string
	// Searcher 是「按平台搜同名歌」的注入口（nil = pkg/search 的真实实现）。
	Searcher func(keyword, platform string) []search.UnifiedSong
	// LocalAPI 是本机后端地址（`http://127.0.0.1:<port>`）。落盘复用现成的
	// `/api/download/song`，不在这里重复实现一遍打标签/写歌词/命名。
	LocalAPI string
	// Token 是本机后台的 API Token（FN_API_TOKEN）。**必须带上**：authMode=token
	// 时连 127.0.0.1 也要校验，否则后端队列自己调 /api/download/song 会被 401 --
	// 用户看到的是「下载全失败：下载接口 HTTP 401」，却和音乐本身毫无关系。
	Token string
	// Prefs 返回下载偏好（要不要内嵌封面/写同名 .lrc），与手动下载同一套开关。
	Prefs func() map[string]any
	// Client 可 nil（用带超时的默认值）。
	Client *http.Client
	Logf   func(string, ...any)
}

// ChannelStatus 是某条取音渠道的可用性（给 /api/download/backend/status 用）。
type ChannelStatus struct {
	ID      string `json:"id"` // native / dl / lx
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	Reason  string `json:"reason,omitempty"`
}

// Availability 是「后台下载现在到底能不能用」的自述。
//
// ⚠️ 这里的 ready **不等于**「什么都能下」：洛雪宿主默认关闭时原生渠道仍然
// 可用（ready=true），但没有平台 id 的冷门歌会拿不到直链。前端必须看
// channels 逐条说明，不能把总开关显示成「全部可用」。
type Availability struct {
	Ready    bool            `json:"ready"`
	Reason   string          `json:"reason,omitempty"`
	Channels []ChannelStatus `json:"channels"`
	// LxHost 是宿主的原始状态（off / starting / ready / failed）。
	LxHost string `json:"lx_host"`
}

// channelsOf 返回清洗后的渠道顺序（与自动下载共用同一套配置语义）。
func (cfg FetcherConfig) channelsOf() []string {
	if cfg.Channels == nil {
		return append([]string(nil), config.DefaultDownloadChannels...)
	}
	return config.NormalizeDownloadChannels(cfg.Channels())
}

func (cfg FetcherConfig) dlSources() []string {
	if cfg.DownloadSources == nil {
		return nil
	}
	out := make([]string, 0, 6)
	seen := map[string]bool{}
	for _, p := range cfg.DownloadSources() {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func (cfg FetcherConfig) searcher() func(keyword, platform string) []search.UnifiedSong {
	if cfg.Searcher != nil {
		return cfg.Searcher
	}
	return func(keyword, platform string) []search.UnifiedSong {
		// 每个源只取前几首：要回答的是「有没有同名的更好版本」，不是全捞回来。
		return search.Search(keyword, platform, 1, 3)
	}
}

// lxState 把宿主状态压成 off / starting / ready / failed 四个词。
func (cfg FetcherConfig) lxState() string {
	if cfg.Lx == nil {
		return "off"
	}
	st := ""
	if s := cfg.Lx.Status(); s != nil {
		if v, ok := s["state"].(string); ok {
			st = strings.ToLower(strings.TrimSpace(v))
		}
	}
	switch st {
	case lxnode.StateReady:
		return "ready"
	case lxnode.StateStarting:
		return "starting"
	case lxnode.StateFailed:
		return "failed"
	case "", lxnode.StateStopped:
		return "off"
	default:
		return st
	}
}

// Availability 报告各渠道现状（ready 的口径见类型注释）。
func (cfg FetcherConfig) Availability() Availability {
	channels := cfg.channelsOf()
	lx := cfg.lxState()
	dl := cfg.dlSources()
	out := Availability{LxHost: lx}
	anyReady := false
	for _, id := range channels {
		cs := ChannelStatus{ID: id, Enabled: true}
		switch id {
		case config.DLChannelNative:
			if cfg.Pool != nil {
				cs.Ready = true
			} else {
				cs.Reason = "平台直链解析器未初始化"
			}
		case config.DLChannelMusicDL:
			switch {
			case cfg.Pool == nil:
				cs.Reason = "平台直链解析器未初始化"
			case len(dl) == 0:
				cs.Reason = "musicdl 未启用（没有启用的外挂平台）"
			default:
				cs.Ready = true
			}
		case config.DLChannelLx:
			if cfg.Lx != nil && lx == "ready" {
				cs.Ready = true
			} else {
				cs.Reason = lxReason(lx)
			}
		}
		if cs.Ready {
			anyReady = true
		}
		out.Channels = append(out.Channels, cs)
	}
	out.Ready = anyReady
	if !anyReady {
		out.Reason = "没有任何可用的取音渠道"
	}
	return out
}

func lxReason(state string) string {
	switch state {
	case "off":
		return "服务端洛雪宿主未开启（默认关闭）"
	case "starting":
		return "服务端洛雪宿主正在启动"
	case "failed":
		return "服务端洛雪宿主启动失败"
	default:
		return "服务端洛雪宿主不可用（" + state + "）"
	}
}

// CanDownload 判断这条任务现在有没有可能被取到（接口层用它在提交时给出
// 明确的 no_channel，而不是让任务排队后再失败）。
func (cfg FetcherConfig) CanDownload(it Item) bool {
	channels := cfg.channelsOf()
	if strings.TrimSpace(it.Platform) != "" && strings.TrimSpace(it.PlatformID) != "" &&
		config.ChannelEnabled(channels, config.DLChannelNative) && cfg.Pool != nil {
		return true
	}
	if config.ChannelEnabled(channels, config.DLChannelLx) && cfg.lxState() == "ready" {
		return true
	}
	if config.ChannelEnabled(channels, config.DLChannelMusicDL) && cfg.Pool != nil && len(cfg.dlSources()) > 0 {
		// 外挂池只能靠搜索匹配，同名歌有没有得搜了才知道；这里给「有可能」。
		return true
	}
	return false
}

// NewFetcher 返回可直接交给 Queue 的取音函数。
func NewFetcher(cfg FetcherConfig) Fetcher {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// ⚠️ 必须回写：resolveURL/planAttempts 读的是 cfg.Logf。只在局部留一个
	// fallback 变量的话，那些函数拿到的是 nil，一打日志就 panic。
	cfg.Logf = logf

	client := cfg.Client
	if client == nil {
		// 单首无损几十兆，超时要够；但也不能无限等 —— 一条卡住会堵住整条队列。
		client = &http.Client{Timeout: 4 * time.Minute}
	}
	return func(ctx context.Context, it Item) (Result, error) {
		if strings.TrimSpace(it.Name) == "" {
			return Result{}, fmt.Errorf("缺少歌名")
		}
		attempts, err := planAttempts(ctx, cfg, it)
		if err != nil {
			return Result{}, err
		}
		// 按「实际档位从高到低」挨个试：解析成功不等于能下（CDN 403、试听片段、
		// 直链过期都是常见的），换下一条比让整首歌失败划算。
		var failures []string
		for _, a := range attempts {
			res, derr := downloadOne(ctx, client, cfg, it, a)
			if derr == nil {
				return res, nil
			}
			failures = append(failures, fmt.Sprintf("%s/%s：%v", a.channel, firstNonEmpty(a.tier, "?"), derr))
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
		}
		return Result{}, fmt.Errorf("所有取音尝试都失败：%s", strings.Join(failures, "；"))
	}
}

// ── 取音计划 ────────────────────────────────────────────────────────────

// attempt 是一次「已探测到直链、还没落盘」的尝试。
//
// 与 pkg/intercept 的 dlAttempt 同构：分渠道收集，最后按**实际档位**排序 ——
// 为什么不能「先平台直链拿到 320k 就直接下」：洛雪可能能给无损，先返回就等于
// 永远拿不到（见 HANDOVER「下载音质」那条的铁律）。
type attempt struct {
	channel string
	url     string
	tier    string // 实际档位（能回读就用回读值）
	source  string // 平台/音源标识，落盘登记要用
	label   string // 排障用：「lx/全豆要」这种
	size    int64
	score   int
	pos     int // 同渠道内的候选顺序（洛雪脚本顺序 / 外挂源顺序）
	// unproven = 宿主探测没结论（超时/DNS）。既没证明能下也没证明死了，
	// 排在所有 proven 之后 —— 拿它当主力是在赌网络。
	unproven bool
}

// planAttempts 按用户排的渠道逐条探测，返回排序后的计划（最好的在前）。
func planAttempts(ctx context.Context, cfg FetcherConfig, it Item) ([]attempt, error) {
	tiers := tiersFor(it.Quality)
	ceil := ceilingRank(tiers)
	channels := cfg.channelsOf()
	if len(channels) == 0 {
		return nil, fmt.Errorf("没有启用任何取音渠道")
	}

	var attempts []attempt
	var notes []string

	for _, ch := range channels {
		pos := 0
		add := func(a attempt) {
			a.pos = pos
			pos++
			if a.score < 0 {
				a.score = 0
			}
			attempts = append(attempts, a)
		}
		switch ch {
		case config.DLChannelNative:
			if cfg.Pool == nil || strings.TrimSpace(it.Platform) == "" || strings.TrimSpace(it.PlatformID) == "" {
				if cfg.Pool == nil {
					notes = append(notes, "原生：解析器未初始化")
				} else {
					notes = append(notes, "原生：这条任务没有平台 id")
				}
				break
			}
			plat := strings.ToLower(strings.TrimSpace(it.Platform))
			if strings.HasPrefix(plat, "lx:") {
				notes = append(notes, "原生：平台是洛雪伪源")
				break
			}
			res, err := cfg.Pool.ResolveLadder(ctx, plat, it.PlatformID, tiers)
			// ⚠️ 只用 ResolveLadder，绝不用 Pool.Resolve：后者档位由平台决定，
			// 网易那条免登录接口固定 br=320000 → 永远只能拿 320k mp3。
			if err != nil || res == nil || strings.TrimSpace(res.URL) == "" {
				notes = append(notes, fmt.Sprintf("原生：%v", firstErr(err)))
				break
			}
			tier := res.Tier
			if strings.TrimSpace(tier) == "" {
				tier = tierFromFormat(res.Format, res.Bitrate)
			}
			add(attempt{
				channel: config.DLChannelNative,
				url:     res.URL,
				tier:    tier,
				source:  plat,
				label:   "native/" + plat,
				size:    res.Size,
				score:   scoreOf(tier, ""),
			})

		case config.DLChannelLx:
			if cfg.Lx == nil || cfg.lxState() != "ready" {
				notes = append(notes, "洛雪："+lxReason(cfg.lxState()))
				break
			}
			scripts := cfg.scripts()
			want := tiers[0]
			found := false
			// 路径 A：用任务自己的平台 id 让脚本取链（脚本只要一个 id）。
			if strings.TrimSpace(it.Platform) != "" && strings.TrimSpace(it.PlatformID) != "" {
				plat := strings.ToLower(strings.TrimSpace(it.Platform))
				if !strings.HasPrefix(plat, "lx:") {
					cands, err := cfg.Lx.ResolveCandidates(ctx, plat, lxInfoFromItem(it), want, scripts)
					if err != nil {
						notes = append(notes, fmt.Sprintf("洛雪(按 id)：%v", err))
					} else {
						for _, a := range lxAttempts(cands, want, it.Duration, plat, cfg) {
							if len(attempts) >= maxAttempts {
								break
							}
							add(a)
							found = true
						}
					}
				}
			}
			// 路径 B：按歌名搜（洛雪自己搜得出候选时才能用这条）。
			if !found || len(attempts) < maxAttempts {
				songs, err := cfg.Lx.SearchAll(ctx, strings.TrimSpace(it.Name+" "+it.Singer), 12)
				if err != nil {
					notes = append(notes, fmt.Sprintf("洛雪搜索：%v", err))
				}
				best := pickLxSong(songs, it)
				if best == nil {
					if len(songs) > 0 {
						notes = append(notes, "洛雪搜索：没有同名同版录音的候选")
					}
				} else {
					info := best.Raw
					if len(info) == 0 {
						info = lxInfoFromSong(*best)
					}
					if info == nil {
						notes = append(notes, "洛雪搜索：候选缺少平台 id")
					} else {
						cands, cerr := cfg.Lx.ResolveCandidates(ctx, best.Source, info, want, scripts)
						if cerr != nil {
							notes = append(notes, fmt.Sprintf("洛雪(搜索取链)：%v", cerr))
						} else {
							for _, a := range lxAttempts(cands, want, it.Duration, best.Source, cfg) {
								if len(attempts) >= maxAttempts {
									break
								}
								add(a)
							}
						}
					}
				}
			}

		case config.DLChannelMusicDL:
			srcs := cfg.dlSources()
			if cfg.Pool == nil || len(srcs) == 0 {
				if len(srcs) == 0 {
					notes = append(notes, "外挂池：musicdl 未启用")
				}
				break
			}
			searchFn := cfg.searcher()
			for _, p := range srcs {
				if len(attempts) >= maxAttempts {
					break
				}
				songs := searchFn(strings.TrimSpace(it.Name+" "+it.Singer), p)
				best := pickUnifiedSong(songs, it, p)
				if best == nil {
					notes = append(notes, fmt.Sprintf("外挂池(%s)：没有同名同版录音", p))
					continue
				}
				id := unifiedPlatformID(*best)
				if id == "" {
					notes = append(notes, fmt.Sprintf("外挂池(%s)：候选缺少平台 id", p))
					continue
				}
				res, err := cfg.Pool.ResolveLadder(ctx, p, id, tiers)
				if err != nil || res == nil || strings.TrimSpace(res.URL) == "" {
					notes = append(notes, fmt.Sprintf("外挂池(%s)：%v", p, firstErr(err)))
					continue
				}
				tier := res.Tier
				if strings.TrimSpace(tier) == "" {
					tier = tierFromFormat(res.Format, res.Bitrate)
				}
				add(attempt{
					channel: config.DLChannelMusicDL,
					url:     res.URL,
					tier:    tier,
					source:  p,
					label:   "dl/" + p,
					size:    res.Size,
					score:   scoreOf(tier, best.Quality),
				})
			}
		}

		// 已经拿到「用户允许的顶」就不必再问别的渠道（再问也不更好了）。
		if ceil >= 0 && bestScore(attempts) >= ceil {
			break
		}
	}

	if len(attempts) == 0 {
		msg := "没有拿到任何可用直链"
		if len(notes) > 0 {
			msg += "：" + strings.Join(notes, "；")
		}
		if cfg.Lx == nil {
			msg += "（提示：可在设置里开启「服务端洛雪宿主」以支持更多音源）"
		}
		return nil, fmt.Errorf("%s", msg)
	}

	sortAttempts(attempts)
	if len(attempts) > maxAttempts {
		attempts = attempts[:maxAttempts]
	}
	// 同一个 URL 只试一次（不同渠道可能指向同一条 CDN 链接）。
	seen := map[string]bool{}
	out := attempts[:0]
	for _, a := range attempts {
		if seen[a.url] {
			continue
		}
		seen[a.url] = true
		out = append(out, a)
	}
	return out, nil
}

// maxAttempts 是「一次任务最多试几条直链」。
//
// 一条任务最多 5 分钟（见 Queue.runOne），而单首无损可能要几十秒；再多的
// 候选留着也没时间试完，反而会把后面的歌推到超时。按档位排过序后前 8 条
// 已经覆盖「高音质优先 + 换源兜底」。
const maxAttempts = 8

// lxInfoFromItem 拼洛雪脚本要的 `musicInfo`（id 三种写法都填）。
func lxInfoFromItem(it Item) map[string]any {
	id := strings.TrimSpace(it.PlatformID)
	return map[string]any{
		"id":        id,
		"songmid":   id,
		"hash":      id,
		"name":      it.Name,
		"singer":    it.Singer,
		"albumName": it.Album,
		"album":     it.Album,
		"duration":  it.Duration,
	}
}

// lxInfoFromSong 用洛雪搜索结果拼 `musicInfo`（脚本只认它自己的 raw 时用不上）。
func lxInfoFromSong(s lxnode.Song) map[string]any {
	id := strings.TrimSpace(s.ID)
	if id == "" {
		return nil
	}
	return map[string]any{
		"id":        id,
		"songmid":   id,
		"hash":      id,
		"name":      s.Name,
		"singer":    s.Singer,
		"albumName": s.Album,
		"album":     s.Album,
		"duration":  s.Duration,
	}
}

// lxAttempts 把宿主交回来的候选变成尝试列表。
//
// 排序语义与 pkg/intercept/lxresolve.go 的 pickLxCandidate 一致（我们是先收集
// 再统一排序，所以把「proven 优先 / 档位高优先 / 用户排前的优先」交给
// sortAttempts + pos 表达）。档位用**实测字节数 + 时长**校正，只降不升。
func lxAttempts(cands []lxnode.Candidate, want string, durationS int, source string, cfg FetcherConfig) []attempt {
	out := make([]attempt, 0, len(cands))
	for _, c := range cands {
		if strings.TrimSpace(c.URL) == "" || !strings.HasPrefix(c.URL, "http") {
			continue
		}
		unproven := strings.TrimSpace(c.Unproven) != ""
		tier := tierFromSize(c.Bytes, durationS, want)
		out = append(out, attempt{
			channel:  config.DLChannelLx,
			url:      c.URL,
			tier:     tier,
			source:   source,
			label:    "lx/" + firstNonEmpty(c.Script, source),
			size:     c.Bytes,
			score:    scoreOf(tier, want),
			unproven: unproven,
		})
	}
	return out
}

// sortAttempts 按「实际档位 → 渠道先后 → 同渠道候选顺序」排序。
//
// 渠道平手时 native(0) < dl(1) < lx(2)：同档位时官方平台那条更可信
// （封面/标签齐全、不容易死链），这与 v2.13.0 的「同档不换」口径一致。
func sortAttempts(list []attempt) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.unproven != b.unproven {
			return !a.unproven
		}
		if a.score != b.score {
			return a.score > b.score
		}
		ta, tb := channelTiebreak(a.channel), channelTiebreak(b.channel)
		if ta != tb {
			return ta < tb
		}
		return a.pos < b.pos
	})
}

func channelTiebreak(ch string) int {
	switch ch {
	case config.DLChannelNative:
		return 0
	case config.DLChannelMusicDL:
		return 1
	default:
		return 2
	}
}

func bestScore(list []attempt) int {
	best := -1
	for _, a := range list {
		if a.unproven {
			continue
		}
		if a.score > best {
			best = a.score
		}
	}
	return best
}

// tiersFor 把目标音质翻成「按顺序问的档位表」（高 → 低）。
//
// 与 pkg/intercept 的 downloadTiers 同口径：点名某一档时先问那一档，再退到
// 它**下面**的档，但绝不往上问（用户选 128k 是为了省空间）。
func tiersFor(q string) []string {
	want := normalizeTier(q)
	if want == "" {
		return append([]string(nil), online.PlaybackLadder...)
	}
	rank := online.TierRank(want)
	out := []string{want}
	for _, t := range online.PlaybackLadder {
		if online.TierRank(t) < rank {
			out = append(out, t)
		}
	}
	return out
}

func ceilingRank(tiers []string) int {
	if len(tiers) == 0 {
		return -1
	}
	return online.TierRank(tiers[0])
}

// scoreOf 给一次尝试打分：**优先实际档位**，读不出来才退回声明值。
//
// 两个都要：洛雪/musicdl 的旧解析器不回读档位，而搜索结果常常只有一句
// 「无损」当声明。只看声明会让「声明无损、实际 128k」的源插队。
func scoreOf(actual, declared string) int {
	if r := online.TierRank(actual); r >= 0 {
		return r
	}
	return declaredRank(declared)
}

// declaredRank 把搜索结果里的音质声明翻译成 online.TierRank 的分。
func declaredRank(q string) int {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "无损", "flac", "flac24bit", "hires", "ape", "wav", "dsd":
		return online.TierRank(online.TierLossless)
	case "aac", "m4a", "320k", "320":
		return online.TierRank(online.Tier320k)
	case "192k", "192":
		return online.TierRank(online.Tier192k)
	case "128k", "128":
		return online.TierRank(online.Tier128k)
	}
	return 0
}

// tierFromFormat 在解析器不回读档位时，按容器/码率推一个 —— 只用于**排序**，
// 最终落盘档位仍由扩展名校正（audioext.TierOfFile）。
func tierFromFormat(format string, bitrate int) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "flac", "ape", "wav", "dsf", "dff":
		return online.TierLossless
	}
	switch {
	case bitrate <= 0:
		return ""
	case bitrate >= 900000:
		return online.TierLossless
	case bitrate >= 256000:
		return online.Tier320k
	case bitrate >= 160000:
		return online.Tier192k
	default:
		return online.Tier128k
	}
}

// tierFromSize 用**实测字节数 + 时长**反推真实档位，只降不升。
//
// 口径与 pkg/intercept/lxresolve.go 的 lxTierFromSize 一致（那里是权威实现，
// 这里是后台下载的同一口径备份）。洛雪背后那些第三方站最常见的偏差不是报错，
// 而是「你点无损，它给一条 128k 的 mp3，然后按无损的名义回话」——「295 秒只给
// 4.1MB」骗不了人（≈113kbps）。
//
// size/duration 缺任一 → 原样返回 claimed：没有证据就不能假装知道；
// 落盘后还有扩展名那道校正。
func tierFromSize(size int64, durationS int, claimed string) string {
	if size <= 0 || durationS <= 0 || strings.TrimSpace(claimed) == "" {
		return claimed
	}
	kbps := float64(size) * 8 / float64(durationS) / 1000
	observed := online.Tier128k
	switch {
	case kbps >= 500:
		observed = online.TierLossless
	case kbps >= 260:
		observed = online.Tier320k
	case kbps >= 160:
		observed = online.Tier192k
	}
	// 只降不升：字节数说不清无损内部的分层，别拿它吹高。
	if online.TierRank(observed) >= online.TierRank(claimed) {
		return claimed
	}
	return observed
}

// ── 候选匹配（Live/Remix 保护靠 pkg/match） ──────────────────────────

// candidateOK 判断候选是不是**同一版录音**。
//
// 用 pkg/match 而不是自己写一遍归一化：它已经处理了
//   - 版本标记（Live/Remix/DJ/伴奏/翻唱…）—— 原曲是正式版时，改编版一律拒绝；
//     原曲自己就是 Live 时，只接受带 live 标记的；
//   - 试听片段（≤75 秒或带 demo/snippet 标记）；
//   - 时长偏差（max(10s, 15%)）。
//
// 只归一化歌名不做版本区分，会把「晴天」和「晴天 (Live)」判成同一首。
func candidateOK(want match.Catalog, cand match.Catalog) bool {
	if match.NormTitle(cand.Name) == "" || match.NormTitle(cand.Name) != match.NormTitle(want.Name) {
		return false
	}
	// 歌手必须沾边（多歌手标注很常见：「周杰伦/杨瑞代」）。
	wa, ca := match.NormArtist(want.Artist), match.NormArtist(cand.Artist)
	if wa != "" && ca != "" && !strings.Contains(ca, wa) && !strings.Contains(wa, ca) {
		return false
	}
	// 原曲是改编版时，才允许改编版候选；试听/时长偏差仍要过。
	allowVariant := match.IsVariant(want)
	if r := match.RejectReason(cand, want.Duration, allowVariant); r != "" {
		return false
	}
	// 两边都知道时长时，同一版录音不能差太多 —— 时长是「同一版」的最后一道闸。
	if want.Duration > 0 && cand.Duration > 0 {
		d := want.Duration - cand.Duration
		if d < 0 {
			d = -d
		}
		tol := int(float64(want.Duration) * match.DurationToleranceRatio)
		if tol < match.DurationToleranceAbs {
			tol = match.DurationToleranceAbs
		}
		if d > tol {
			return false
		}
	}
	return true
}

// pickLxSong 从洛雪搜索结果里挑一条（同名同版、音质最高、时长最接近）。
func pickLxSong(songs []lxnode.Song, it Item) *lxnode.Song {
	want := match.Catalog{Name: it.Name, Artist: it.Singer, Album: it.Album, Duration: it.Duration}
	type scored struct {
		s     *lxnode.Song
		rank  int
		durD  int
		size  int64
		order int
	}
	var cands []scored
	for i := range songs {
		s := &songs[i]
		c := match.Catalog{Name: s.Name, Artist: s.Singer, Album: s.Album, Duration: s.Duration}
		if !candidateOK(want, c) {
			continue
		}
		d := absInt(s.Duration - it.Duration)
		cands = append(cands, scored{s: s, rank: declaredRank(s.Quality), durD: d, size: s.Size, order: i})
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].rank != cands[b].rank {
			return cands[a].rank > cands[b].rank
		}
		if cands[a].size != cands[b].size {
			return cands[a].size > cands[b].size
		}
		if cands[a].durD != cands[b].durD {
			return cands[a].durD < cands[b].durD
		}
		return cands[a].order < cands[b].order
	})
	return cands[0].s
}

// pickUnifiedSong 从外挂池（musicdl）搜索结果里挑一条。
func pickUnifiedSong(songs []search.UnifiedSong, it Item, source string) *search.UnifiedSong {
	want := match.Catalog{Name: it.Name, Artist: it.Singer, Album: it.Album, Duration: it.Duration}
	best := -1
	var bestScore float64
	for i := range songs {
		s := &songs[i]
		c := match.Catalog{Name: s.Name, Artist: s.Singer, Album: s.Album, Duration: songDuration(s)}
		if !candidateOK(want, c) {
			continue
		}
		// pkg/match 的 Similarity：歌名 70% + 歌手 30%，同分时看音质声明与体积。
		score := match.Similarity(match.Catalog{Name: s.Name, Artist: s.Singer, Album: s.Album, Duration: songDuration(s)}, want)
		if score < match.MatchMinScore {
			continue
		}
		if best < 0 || score > bestScore || (score == bestScore && betterQuality(*s, songs[best])) {
			best = i
			bestScore = score
		}
	}
	if best < 0 {
		return nil
	}
	return &songs[best]
}

// betterQuality 同匹配分时：声明档位高的优先，再看体积（同档比体积）。
func betterQuality(a, b search.UnifiedSong) bool {
	ra, rb := declaredRank(a.Quality), declaredRank(b.Quality)
	if ra != rb {
		return ra > rb
	}
	return a.FileSize > b.FileSize
}

// songDuration 统一时长单位（有的源用毫秒）。
func songDuration(s *search.UnifiedSong) int {
	d := s.Duration
	if d <= 0 {
		d = s.Interval
	}
	if d > 3600 {
		d /= 1000
	}
	return d
}

// unifiedPlatformID 取平台内 id（Songmid 优先，与 pkg/intercept 同口径）。
func unifiedPlatformID(s search.UnifiedSong) string {
	if id := strings.TrimSpace(s.Songmid); id != "" {
		return id
	}
	return strings.TrimSpace(s.ID)
}

func (cfg FetcherConfig) scripts() []string {
	if cfg.Scripts == nil {
		return nil
	}
	return cfg.Scripts()
}

// ── 落盘 ────────────────────────────────────────────────────────────────

// downloadOne 把一条直链交给本机下载接口落盘。
//
// 复用 `/api/download/song` 而不是在这里直接写文件：命名规则、打标签、内嵌
// 封面、写同名 .lrc、以及「盘上已有一份更好的就别覆盖」这些规矩都在那边。
func downloadOne(ctx context.Context, client *http.Client, cfg FetcherConfig, it Item, a attempt) (Result, error) {
	payload := map[string]any{
		"name":     it.Name,
		"singer":   it.Singer,
		"album":    it.Album,
		"cover":    it.Cover,
		"source":   firstNonEmpty(it.Platform, a.source),
		"songmid":  it.PlatformID,
		"url":      a.url,
		"duration": it.Duration,
		// 档位：传**实际**档位（平台会静默降级：要无损给 320k），下载器拿它
		// 跟盘上那份比，决定要不要覆盖。
		"quality": firstNonEmpty(a.tier, it.Quality),
		// 与自动下载一致：按最高音质入库 —— 库里已有一份 320k 时，这次拿到
		// 无损就该换掉，而不是回一句「已存在」把无损挡在门外。
		"upgrade_if_better": true,
	}
	if cfg.Prefs != nil {
		for k, v := range cfg.Prefs() {
			payload[k] = v
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.LocalAPI, "/")+"/api/download/song", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := strings.TrimSpace(cfg.Token); tok != "" {
		req.Header.Set("X-API-Token", tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	// 非 2xx 一律算失败：本机接口出错时回的是错误码，不能因为 body 里恰好
	// 有 path 字样就当成功（响应形状解析之前先把硬错误拦住）。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("下载接口 HTTP %d：%s", resp.StatusCode, snippet(raw))
	}

	// 响应形状（pkg/downloader.HandleDownloadSong）：`{code,message,data:{path,…}}`。
	// ⚠️ 只有 code == 200 且有 path 才算成功 —— `already_exists` 也算成功
	// （文件本来就在），所以不按 status 字符串判，只按「有没有 path」判。
	var out struct {
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
			Path   string `json:"path"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("下载接口返回的不是 JSON（HTTP %d）：%s", resp.StatusCode, snippet(raw))
	}
	if out.Code != 200 || strings.TrimSpace(out.Data.Path) == "" {
		msg := strings.TrimSpace(out.Data.Error)
		if msg == "" {
			msg = snippet(raw)
		}
		return Result{}, fmt.Errorf("下载失败：%s", msg)
	}

	var size int64
	if fi, serr := os.Stat(out.Data.Path); serr == nil {
		size = fi.Size()
	}
	// 档位用**落盘文件的扩展名**校正一次 —— 扩展名是魔数嗅探出来的，
	// 比任何声明都硬。
	return Result{
		Path:    out.Data.Path,
		Quality: firstNonEmpty(audioext.TierOfFile(out.Data.Path), a.tier, it.Quality),
		Source:  firstNonEmpty(a.source, it.Platform),
		Size:    size,
	}, nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// normalizeTier 把前端的音质档位翻成取链层认识的档位。
//
// 「highest」不是档位，是**意图**（要它能给的最好的）：返回空串，由
// tiersFor 换成完整的降级阶梯。监控的 standard/high/lossless 也在这里收口。
func normalizeTier(q string) string {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "", "highest", "auto", "best":
		return ""
	case "standard", "normal":
		return online.Tier128k
	case "high":
		return online.Tier320k
	case "lossless", "sq":
		return online.TierLossless
	case "flac24bit", "hires":
		return online.TierLossless
	default:
		return strings.ToLower(strings.TrimSpace(q))
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstErr(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("没有直链")
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
