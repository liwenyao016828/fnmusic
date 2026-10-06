// Package complete 主动补全曲库缺失的歌词与封面。
//
// ── 为什么需要它 ──
//
// `POST /api/tidy` 是**被动**接口：调用方得自己把 `lyric` / `cover_url` 传进来。
// 对网页端用户来说这等于没做 —— 他不知道去哪找歌词，更不知道要拼哪些接口
// （那个接口自己的 hint 甚至写着「歌词可从 GET /api/search/lyric 获取」）。
//
// 这里把「找缺失 → 搜索匹配 → 取歌词/封面 → 写入标签」串成一条流水线，
// 界面上一个按钮就能跑完，接口调用方也不用自己拼。
//
// ── 原则（用户 2026-09-18 选的「搜索补全 + AI 补缺」）──
//
//   - **搜索优先**：歌词与封面都来自平台搜索，有权威来源、可核对；
//   - **AI 只补没有权威来源的字段** —— 当前只用于 genre（各平台搜索接口都不返回风格）。
//     ⚠️ **绝不用 AI 生成歌词**：那是编造内容，宁可留空。
//
// ── 复用而非重造 ──
//
//   - 搜索：`search.Search()`（本包新增的导出包装）
//   - 匹配：`search.MatchByNameSinger()` —— 与 `/api/music/qualities` **同一套判据**，
//     避免「音质查询认得出、补全认不出」这种前后不一致；
//     外面再套一层 `pickMatch` 的两道护栏（剔试听片段、版本标记必须与文件一致），
//     因为曲名归一化后 `晴天` 与 `晴天 (Live)` 完全相同，光看曲名会写错版本
//   - 取歌词：`search.FetchLyric()`
//   - 写标签：`tidy.TidyOne()` —— 格式嗅探、MP3/FLAC 分支、外挂 .lrc 都在那边
package complete

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fn-lx-player/pkg/ai"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/nameparse"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/tidy"
)

// Target 一首待补全的曲目。
//
// Title / Artist 通常来自曲库索引（`library.Entry` 里就有），
// 拿不到时留空，本包会尝试从文件名猜。
type Target struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`

	// Need* 是「这首要补哪些字段」的勾选。
	//
	// 逐字段可选是这一版的核心改动：飞牛「歌曲信息」有年份/曲序/光盘/风格四栏，
	// 用户可能只想补年份而不想动已有的歌词。没勾的字段**不会**被写入，
	// 也（依赖 `tags.Write` 的合并语义）不会被抹掉。
	NeedLyric bool `json:"need_lyric"`
	NeedCover bool `json:"need_cover"`
	NeedGenre bool `json:"need_genre,omitempty"`
	NeedYear  bool `json:"need_year,omitempty"`
	NeedTrack bool `json:"need_track,omitempty"`
	NeedDisc  bool `json:"need_disc,omitempty"`
}

// Options 补全选项
type Options struct {
	// Platforms 按顺序尝试的平台；留空用 DefaultPlatforms
	Platforms []string
	// Tidy 写入选项；零值用 tidy.DefaultOptions()
	Tidy tidy.Options
	// Concurrency 并发度（<=0 用 2）。与 TidyBatch 保持一致，也降低被平台风控的概率。
	Concurrency int
	// AIClient 可选：用于文件名判名与 genre 推断
	AIClient *ai.Client
	// UseAINameJudge 文件名没把握时（字段为空或规则自认可疑）请 AI 判一次。
	// 只在 AIClient 可用时生效；关掉则完全走规则。
	UseAINameJudge bool
	// UseAIMatch 规则匹配不上时，请 AI 从搜索候选里挑一条。
	// 同样只在 AIClient 可用时生效。
	UseAIMatch bool
	// UseAIVerifyLyric 对**所有**取到歌词的曲目做核验。
	//
	// 默认关：只核验「AI 挑的匹配」（那类风险最高、数量最少）。
	// 打开后 AI 调用量大约翻倍 —— 慎用，除非你很在意「歌词张冠李戴」。
	UseAIVerifyLyric bool
	// UseAIGenre 是否允许 AI 推断 genre（搜索来源没有这个字段）
	UseAIGenre bool
	// PreferredPlatforms 主力平台（有序，默认网易 + QQ）。
	//
	// 用途只有一个：**兜底平台命中、但勾的字段它给不出时，回主力平台借元数据**。
	// 酷狗/酷我的搜索响应不带年份/曲序/专辑标识，又常常是唯一能匹配上的那条 ——
	// 没有这一步，「勾了年份」对这批歌就永远是空。留空 = 用默认（网易、QQ）。
	PreferredPlatforms []string
	// NameRules 用户自定义的命名解析规则。非 nil 时**优先于内置启发式** ——
	// 它是用户显式定义的意图，而启发式只是我们猜的。
	NameRules *nameparse.Store
	// Backup 非 nil 时，改写每个文件**之前**先备份，供「撤销上一次补全」还原。
	// 备份失败会跳过该文件 —— 不能「备份失败还继续写」，那就失去撤销能力了。
	Backup *Backup
}

// DefaultPlatforms 默认搜索顺序：网易优先（歌词覆盖最好），再 QQ、酷狗、酷我
var DefaultPlatforms = []string{"wy", "tx", "kg", "kw"}

// searchFn 是搜索的可注入入口（单测靠它摆各平台的候选，不碰网络）。
var searchFn = search.Search

// hitCovers 数一条命中能满足几个「只有部分平台才给」的字段。
//
// 风格算「能满足」的条件是带专辑标识 —— 风格不在搜索响应里，
// 要靠 `FetchQQAlbumMeta(AlbumMID)` 再取一次，没 mid 就是取不到。
func hitCovers(s search.UnifiedSong, t Target) int {
	n := 0
	if t.NeedYear && s.Year > 0 {
		n++
	}
	if t.NeedTrack && s.Track > 0 {
		n++
	}
	if t.NeedDisc && s.Disc > 0 {
		n++
	}
	if t.NeedGenre && strings.TrimSpace(s.AlbumMID) != "" {
		n++
	}
	return n
}

// pickMatch 在某个平台的候选里挑一条命中，并过两道「别写错版本」的护栏。
//
// 为什么不能直接调 `search.MatchByNameSinger`：它**只看曲名与歌手**
// （归一化后曲名相等 + 歌手有交集），而平台搜索结果里混着试听片段与各种改编版：
//   - 试听片段（≤ `match.PreviewDurationSec` 秒）被当正常条目返回，
//     它带的年份/曲序/专辑是另一张碟的；
//   - 改编版（`晴天 (Live)`）曲名归一化后与正式版**完全相同**，会抢先命中，
//     于是把 Live 版的年份/曲序写进原唱文件 —— 真实发生过的错法。
//
// 两道护栏：
//  1. 时长 ≤ 75 秒的候选直接剔除；
//  2. 候选的「括号内版本标记」必须与**文件自己的**版本标记一致
//     （文件名带 `(Live)` 就只认改编版，否则只认正式版）。
//
// 为什么回头看原始文件名：`nameparse` 的 `bracketRe` 会把 `(Live)` 整段丢掉，
// 到这一层已经看不出文件本身是不是 Live 版了。
// 为什么不用 `match.IsVariant` 判候选：它是裸子串匹配，`Alive`/`Demons`
// 会被误判成改编版（见 `match.BracketedVariant` 的说明），括号内标记更准。
//
// 返回的 note 非空表示「**有意跳过**」：调用方不能把这些候选再交给 AI 兜底 ——
// AI 会照样挑中那条 Live 版，护栏等于没有。
//
// 已知取舍：不比对**本地文件的时长**。`pkg/tags` 不解析时长、曲库索引里也没有，
// 解析阶段又刻意不碰文件，所以这里只能做候选侧判据。等索引能给出时长，
// 这里再加一道「候选时长 vs 文件时长」的硬校验（`match.RejectReason` 已就绪）。
func pickMatch(cands []search.UnifiedSong, title, artist, fileName string) (search.UnifiedSong, bool, string) {
	wantVariant := match.BracketedVariant(fileName)

	var same, mismatch []search.UnifiedSong
	previews := 0
	for _, s := range cands {
		// 逐条复用与 /api/music/qualities 完全相同的判据（不做第二套归一化）
		if _, ok := search.MatchByNameSinger([]search.UnifiedSong{s}, title, artist); !ok {
			continue
		}
		if s.Duration > 0 && s.Duration <= match.PreviewDurationSec {
			previews++
			continue
		}
		if match.BracketedVariant(s.Name) == wantVariant {
			same = append(same, s)
			continue
		}
		mismatch = append(mismatch, s)
	}

	if len(same) > 0 {
		// 平台自己的排序就是「最相关在前」，同池里保持原顺序取第一条
		return same[0], true, ""
	}
	if len(mismatch) > 0 {
		return search.UnifiedSong{}, false,
			"只找到改编版《" + mismatch[0].Name + "》，已跳过（避免把改编版的年份/曲序写进原版）"
	}
	if previews > 0 {
		return search.UnifiedSong{}, false, "只找到试听片段（不足 75 秒），已跳过"
	}
	return search.UnifiedSong{}, false, ""
}

// Progress 进度快照（每完成一首回调一次）
type Progress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"`
	// Phase 当前阶段。补全分两步跑（先解析、后写入），
	// 进度条要能区分「在搜」和「在写」—— 两者的耗时与失败原因完全不同。
	Phase string `json:"phase,omitempty"`
}

// 补全的两个阶段
const (
	// PhaseResolve 解析：定名 + 搜索匹配（不碰文件）
	PhaseResolve = "resolve"
	// PhaseWrite 写入：取歌词/封面 + 写标签
	PhaseWrite = "write"
)

// ItemResult 单曲结果
type ItemResult struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Matched     bool   `json:"matched"`
	MatchedBy   string `json:"matched_by,omitempty"`   // 命中的平台
	MatchedName string `json:"matched_name,omitempty"` // 匹配到的曲名，便于人工核对
	// MatchedByAI 这次匹配是规则配不上、由 AI 从候选里挑的。
	// 透出去是为了**可分辨** —— 用户有权知道哪些结果是模型判断的。
	MatchedByAI bool `json:"matched_by_ai,omitempty"`
	LyricFilled bool `json:"lyric_filled"`
	CoverFilled bool `json:"cover_filled"`
	GenreFilled bool `json:"genre_filled"`
	YearFilled  bool `json:"year_filled,omitempty"`
	TrackFilled bool `json:"track_filled,omitempty"`
	DiscFilled  bool `json:"disc_filled,omitempty"`

	// Album 以及下面三个值字段是**实际写进文件的内容**，供调用方回写曲库索引。
	// 不回写的话索引里年份还是 0，下一轮又会把同一批歌挑出来重补。
	Album string `json:"album,omitempty"`
	Genre string `json:"genre,omitempty"`
	Year  int    `json:"year,omitempty"`
	Track int    `json:"track,omitempty"`
	Disc  int    `json:"disc,omitempty"`

	// LyricRejected 歌词**取到了但核验不通过**，按「宁缺毋滥」没写入。
	// 与「本来就没取到」是两件事，要分开记 —— 否则用户看不出为什么没补上。
	LyricRejected     bool   `json:"lyric_rejected,omitempty"`
	LyricRejectReason string `json:"lyric_reject_reason,omitempty"`

	// Cancelled 这一首**是因为任务被停止而没跑完**。
	// 必须与「没搜到」（NoMatch）和「写入失败」（Failed）分开：
	// 那两个是关于这首歌的结论，这个只是关于这次任务的结论 ——
	// 混在一起用户会以为这些歌本身有问题。
	Cancelled bool `json:"cancelled,omitempty"`
}

// Summary 整体结果
type Summary struct {
	Total       int `json:"total"`
	Matched     int `json:"matched"`
	NoMatch     int `json:"no_match"`
	LyricFilled int `json:"lyric_filled"`
	CoverFilled int `json:"cover_filled"`
	GenreFilled int `json:"genre_filled"`
	YearFilled  int `json:"year_filled"`
	TrackFilled int `json:"track_filled"`
	DiscFilled  int `json:"disc_filled"`
	// LyricRejected 歌词取到了但核验不通过、按「宁缺毋滥」没写入的数量
	LyricRejected int          `json:"lyric_rejected"`
	Failed        int          `json:"failed"`
	Results       []ItemResult `json:"results"`
	// Cancelled 因为任务被停止而**没跑**的曲目数（含已派发但中途放弃的）。
	// 它不是失败，但也不代表「这批歌没问题」—— 用户需要知道还剩多少没做。
	Cancelled int `json:"cancelled"`
	// BackupDir 本次的备份目录（空 = 没备份）。撤销时用它。
	BackupDir string `json:"backup_dir,omitempty"`
	// BackupSize 备份占用的字节数
	BackupSize int64 `json:"backup_size,omitempty"`
	// BackupError 备份清单写入失败时的说明（非空表示本次可能无法撤销）
	BackupError string `json:"backup_error,omitempty"`
}

func withDefaults(o Options) Options {
	if len(o.Platforms) == 0 {
		o.Platforms = DefaultPlatforms
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	// 全 false 视为「未指定」，套默认（与 /api/tidy 的判定一致）
	if !o.Tidy.EmbedLyric && !o.Tidy.WriteLRC && !o.Tidy.EmbedCover &&
		!o.Tidy.WriteCoverFile && !o.Tidy.WriteMetadata &&
		!o.Tidy.OverwriteLyric && !o.Tidy.OverwriteCover {
		o.Tidy = tidy.DefaultOptions()
	}
	return o
}

// Run 逐首补全（解析 + 写入一步到位）。onProgress 可为 nil。
//
// 需要「先预览后写」时用 `Resolve` + `Execute` 两步，见它们的注释。
// 单首失败不会拖垮整批：结果里逐条给出原因。
func Run(ctx context.Context, targets []Target, opts Options, onProgress func(Progress)) Summary {
	plan := Resolve(ctx, targets, opts, nil)
	return Execute(ctx, plan, opts, onProgress)
}

// ── 阶段一：解析（**不动文件**）──

// PlanItem 计划里的一条：名字解析完、也匹配到了平台曲目，但**还没动文件**。
type PlanItem struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`

	Matched     bool   `json:"matched"`
	MatchedBy   string `json:"matched_by,omitempty"`
	MatchedName string `json:"matched_name,omitempty"`
	MatchedByAI bool   `json:"matched_by_ai,omitempty"`

	NeedLyric bool `json:"need_lyric"`
	NeedCover bool `json:"need_cover"`
	NeedGenre bool `json:"need_genre,omitempty"`
	NeedYear  bool `json:"need_year,omitempty"`
	NeedTrack bool `json:"need_track,omitempty"`
	NeedDisc  bool `json:"need_disc,omitempty"`

	// Fill* 是「确认执行后**将要写入**的值」，来自已匹配到的那条平台曲目。
	// 预览页靠它们显示「年份：（空）→ 2003（QQ 音乐）」。
	// 没匹配上、或平台压根没给这个字段时保持零值 —— 宁缺毋滥，绝不拿 AI 猜的值凑数。
	FillYear  int    `json:"fill_year,omitempty"`
	FillTrack int    `json:"fill_track,omitempty"`
	FillDisc  int    `json:"fill_disc,omitempty"`
	FillGenre string `json:"fill_genre,omitempty"`
	// MetadataFrom 元数据是**从哪个主力平台借来的**（兜底命中自己给不出时）。
	// 界面要显示它 —— 用户有权知道「年份是 QQ 给的，因为酷狗那条没有」。
	MetadataFrom string `json:"metadata_from,omitempty"`
	// FillLyric 歌词能否取到要到执行阶段才知道，这里只给「要不要取」

	// Note 预览阶段就能确定的问题（例如「没搜到匹配的曲目」）
	Note string `json:"note,omitempty"`

	// ── 以下不对外暴露：执行阶段靠它们取歌词/封面 ──
	//
	// ⚠️ 记下**匹配到的曲目标识**，而不是让执行阶段重新搜一次 ——
	// 否则搜索结果变了、AI 挑得不一样，用户就会「批准了 A、结果写了 B」。
	source   string
	songmid  string
	hash     string
	duration int
	coverURL string
	albumMID string
}

// WantsAnything 是否勾选了至少一个字段
func (t Target) WantsAnything() bool {
	return t.NeedLyric || t.NeedCover || t.NeedGenre || t.NeedYear || t.NeedTrack || t.NeedDisc
}

// Plan 一次补全的计划
type Plan struct {
	Dir   string     `json:"dir"`
	Items []PlanItem `json:"items"`
	// Cancelled 解析阶段被停止时、**还没轮到**的曲目数（这些歌连搜索都没发起）。
	// 计划会被执行方（`Execute`）看到，所以这里也必须如实标出来 ——
	// 否则「计划里只有 20 条」看起来像用户只挑了 20 首。
	Cancelled int `json:"cancelled,omitempty"`
}

// Resolve 解析一批曲目：定名 → 搜索匹配 → 得到计划。**不写任何文件。**
//
// 为什么要分两步：补全会真正改写用户的音频文件。
// 先让用户看一眼「文件 → 匹配到的歌 → 会写什么」，确认后才落盘 ——
// 这比事后撤销体验好得多（**撤销是兜底，不是主防线**）。
//
// ctx 被取消时**停止派发新曲目**，已经在跑的让它跑完（半途强杀只会留下没人要的半成品）；
// 少做的部分记在 `Plan.Cancelled` 里。
func Resolve(ctx context.Context, targets []Target, opts Options, onProgress func(Progress)) Plan {
	opts = withDefaults(opts)
	plan := Plan{Items: make([]PlanItem, 0, len(targets))}
	if len(targets) == 0 {
		return plan
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.Concurrency)
	done := 0
	dispatched := 0

	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		dispatched++
		go func(t Target) {
			defer wg.Done()
			defer func() { <-sem }()

			item := resolveOne(ctx, t, opts)

			mu.Lock()
			plan.Items = append(plan.Items, item)
			done++
			snap := Progress{Done: done, Total: len(targets), Current: displayName(t), Phase: PhaseResolve}
			mu.Unlock()

			if onProgress != nil {
				onProgress(snap)
			}
		}(t)
	}
	wg.Wait()

	plan.Cancelled = len(targets) - dispatched
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Path < plan.Items[j].Path })
	return plan
}

// resolveOne 解析一首：定名 → 搜索 → 匹配。不碰文件。
func resolveOne(ctx context.Context, t Target, opts Options) PlanItem {
	it := PlanItem{
		Path:      t.Path,
		NeedLyric: t.NeedLyric,
		NeedCover: t.NeedCover,
		NeedGenre: t.NeedGenre,
		NeedYear:  t.NeedYear,
		NeedTrack: t.NeedTrack,
		NeedDisc:  t.NeedDisc,
	}

	title, artist := resolveName(ctx, opts, t.Path, t.Title, t.Artist)
	if title == "" {
		it.Note = "文件名里认不出歌名"
		return it
	}
	it.Title, it.Artist = title, artist

	keyword := title
	if artist != "" {
		keyword = title + " " + artist
	}

	// 1) 按平台顺序搜索：规则命中即停；全都没命中就把候选攒起来交给 AI 挑
	//
	// 2026-09-23 真机撞到一件事：默认顺序里先命中的是酷狗，而酷狗/酷我的搜索响应
	// 不带年份、曲序、专辑标识 —— 于是「勾了年份/风格」的那几栏必然是空，
	// 明明网易或 QQ 也能匹配上却被跳过了。所以这里改成：
	// **只要勾的字段还没被这条命中满足，就继续往后找一个信息更全的命中。**
	// 没勾那些字段时（只补歌词/封面）want=0，第一个命中立刻 break，与旧行为完全一致。
	//
	// 换过去的代价只是多一两次搜索请求，而且候选全都过了同一道 `MatchByNameSinger`
	// 外加 `pickMatch` 的版本/试听护栏，不存在「为了字段挑到别的歌」——
	// 宁缺毋滥的底线不变。
	want := 0
	if t.NeedYear {
		want++
	}
	if t.NeedTrack {
		want++
	}
	if t.NeedDisc {
		want++
	}
	if t.NeedGenre {
		want++
	}

	var hit search.UnifiedSong
	var pool []search.UnifiedSong
	var guardNote string
	bestCovers := -1
	fileName := filepath.Base(t.Path)
	for _, p := range opts.Platforms {
		if ctx.Err() != nil {
			break
		}
		cands := searchFn(keyword, p, 1, 10)
		if len(cands) == 0 {
			continue
		}
		s, ok, note := pickMatch(cands, title, artist, fileName)
		if ok {
			if n := hitCovers(s, t); n > bestCovers {
				hit, bestCovers = s, n
			}
			// 已经补齐（或本来就不要求字段）→ 不必再搜后面的平台
			if want == 0 || bestCovers >= want {
				break
			}
			continue
		}
		// 有意跳过（只有试听片段 / 版本对不上）→ 记下原因，且**不交给 AI 兜底**：
		// 交过去 AI 会照样挑中那条 Live 版，护栏等于没有。
		if note != "" {
			if guardNote == "" {
				guardNote = note
			}
			continue
		}
		// 规则没命中 → 攒候选（按平台顺序，前面平台的排前面）
		pool = append(pool, cands...)
	}

	// 2) 规则全平台都没命中 → 请 AI 从候选里挑一条
	//
	// 这一步解决的是「搜到了但配不上」：`MatchByNameSinger` 要求曲名归一化后完全相等，
	// 而平台标题常带后缀（`晴天 (Live)`、`晴天 (原唱 周杰伦)`、`晴天（R&B版）`）。
	// AI 只从候选里挑（输出序号或 null），不能引入候选外的内容。
	if hit.ID == "" && hit.Songmid == "" && guardNote != "" {
		// 有「有意跳过」的候选时不再请 AI —— 见 pickMatch 的说明
		it.Note = guardNote
		return it
	}
	if hit.ID == "" && hit.Songmid == "" && opts.UseAIMatch && opts.AIClient != nil && len(pool) > 0 {
		if s, ok := pickByAI(ctx, opts.AIClient, title, artist, t.Album, pool); ok {
			hit = s
			it.MatchedByAI = true
		}
	}

	if hit.ID == "" && hit.Songmid == "" {
		it.Note = "没有搜到匹配的曲目"
		return it
	}

	it.Matched = true
	it.MatchedBy = hit.Source
	it.MatchedName = hit.Name
	it.Album = firstNonEmpty(t.Album, hit.Album)
	// 记下标识，执行阶段按它取歌词封面（保证「预览什么就写什么」）
	it.source, it.songmid, it.hash = hit.Source, hit.Songmid, hit.Hash
	it.duration, it.coverURL = hit.Duration, hit.Cover
	it.albumMID = hit.AlbumMID

	// 年份/曲序/光盘：值**只能取自已匹配到的那条命中**（hit），不能取搜索列表里的
	// 任意一条 —— 实测「晴天 周杰伦」网易前三条都是翻唱（year=2025, track=1），
	// 直接拿第一条会把 DJ 版的序号写进周杰伦的原唱文件。
	if it.NeedYear && hit.Year > 0 {
		it.FillYear = hit.Year
	}
	if it.NeedTrack && hit.Track > 0 {
		it.FillTrack = hit.Track
	}
	if it.NeedDisc && hit.Disc > 0 {
		it.FillDisc = hit.Disc
	}

	// 风格：只有 QQ 的专辑详情给真实 genre（其它平台的搜索结果里没这个字段）。
	// 拿不到就留空 —— 除非用户另外勾了「AI 推断风格」，那是 writeOne 里的事。
	if it.NeedGenre {
		if m, ok := qqAlbumMeta(ctx, it.albumMID); ok {
			it.FillGenre = m.Genre
			if it.FillYear <= 0 && m.Year > 0 {
				it.FillYear = m.Year
			}
		}
	}

	// 兜底平台命中（酷狗/酷我的响应不带年份/曲序/专辑标识）时，回主力平台借元数据。
	// 借不到就保持原样 —— 下面的 note 会逐条说明哪个字段为什么空着。
	if from := borrowMetadata(ctx, &it, hit, preferredOrDefault(opts.PreferredPlatforms)); from != "" {
		it.MetadataFrom = from
	}

	// 哪个字段取不到要**单独说明**：否则用户在预览页只看到「匹配成功了」，
	// 却不知道年份其实没补上。
	if it.NeedYear && it.FillYear <= 0 {
		addNote(&it, "匹配到的曲目没有年份")
	}
	if it.NeedTrack && it.FillTrack <= 0 {
		addNote(&it, "匹配到的曲目没有曲序")
	}
	if it.NeedGenre && it.FillGenre == "" {
		addNote(&it, "风格要 QQ 音乐专辑详情才有（本次不是 QQ 命中，或缺专辑标识）")
	}

	if it.NeedCover && strings.TrimSpace(it.coverURL) == "" {
		it.NeedCover = false
		addNote(&it, "匹配到的曲目没有封面")
	}
	return it
}

// preferredOrDefault 主力平台顺序；没配置时用默认（网易、QQ）。
func preferredOrDefault(list []string) []string {
	if len(list) == 0 {
		return []string{"wy", "tx"}
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"wy", "tx"}
	}
	return out
}

// sameRecording 判断两条不同平台的命中是不是同一个录音版本。
//
// 借元数据前必须过这一道：曲名歌手都对得上、但一个是 4 分 26 秒的原版、
// 一个是 6 分半的现场版，那借过来的年份就是错的。**宁可空着也不能借错**。
// 两边时长都已知才判；有一方为 0 时不做判据（很多平台搜索响应不带时长）。
func sameRecording(a, b search.UnifiedSong) bool {
	if a.Duration <= 0 || b.Duration <= 0 {
		return true
	}
	diff := a.Duration - b.Duration
	if diff < 0 {
		diff = -diff
	}
	tol := a.Duration * 15 / 100
	if tol < 10 {
		tol = 10
	}
	return diff <= tol
}

// borrowMetadata 兜底平台命中、但勾了的字段它给不出时，回主力平台借元数据。
//
// 只借年份/曲序/光盘/风格这四样**元数据** —— 歌词、封面、直链一律还用原来那条命中，
// 否则就变成「标签是 QQ 的、音频是酷狗的」，那是比空着更糟的错法。
//
// 门槛三道，任一不过就不借：① 过同一道 `MatchByNameSinger`（曲名归一化相等 + 歌手有交集）；
// ② `sameRecording` 的时长容差；③ 只填「这次勾了、而原命中给不出」的字段。
// 返回真正贡献了值的平台（一个都没借到时返回空串）。
func borrowMetadata(ctx context.Context, it *PlanItem, from search.UnifiedSong, preferred []string) string {
	needYear := it.NeedYear && it.FillYear <= 0
	needTrack := it.NeedTrack && it.FillTrack <= 0
	needDisc := it.NeedDisc && it.FillDisc <= 0
	needGenre := it.NeedGenre && it.FillGenre == ""
	if !needYear && !needTrack && !needDisc && !needGenre {
		return ""
	}

	name := firstNonEmpty(from.Name, it.Title)
	singer := firstNonEmpty(from.Singer, it.Artist)
	keyword := name
	if singer != "" {
		keyword = name + " " + singer
	}

	got := ""
	for _, p := range preferred {
		if ctx.Err() != nil || p == from.Source {
			continue
		}
		cands := searchFn(keyword, p, 1, 10)
		if len(cands) == 0 {
			continue
		}
		s, ok := search.MatchByNameSinger(cands, name, singer)
		if !ok || !sameRecording(from, s) {
			continue
		}
		if needYear && s.Year > 0 {
			it.FillYear, needYear, got = s.Year, false, p
		}
		if needTrack && s.Track > 0 {
			it.FillTrack, needTrack, got = s.Track, false, p
		}
		if needDisc && s.Disc > 0 {
			it.FillDisc, needDisc, got = s.Disc, false, p
		}
		if needGenre && strings.TrimSpace(s.AlbumMID) != "" {
			if m, ok := fetchAlbumMeta(s.AlbumMID); ok && strings.TrimSpace(m.Genre) != "" {
				it.FillGenre, needGenre, got = m.Genre, false, p
			}
		}
		if !needYear && !needTrack && !needDisc && !needGenre {
			break
		}
	}
	return got
}

// addNote 追加一条说明（已有的用「；」串起来，别把前一条覆盖掉）
func addNote(it *PlanItem, msg string) {
	if strings.TrimSpace(it.Note) == "" {
		it.Note = msg
		return
	}
	it.Note += "；" + msg
}

// fetchAlbumMeta 是 QQ 专辑详情的可注入入口（单测靠它避免真打外网）。
var fetchAlbumMeta = search.FetchQQAlbumMeta

// qqAlbumMeta 取 QQ 专辑详情里的风格/年份；没有专辑标识就直接返回 false（不白跑网络）。
func qqAlbumMeta(ctx context.Context, albumMID string) (search.QQAlbumMeta, bool) {
	if strings.TrimSpace(albumMID) == "" || ctx.Err() != nil {
		return search.QQAlbumMeta{}, false
	}
	return fetchAlbumMeta(albumMID)
}

// ── 阶段二：执行（取素材 + 写文件）──

// Execute 按计划取歌词/封面并写入文件。
//
// ctx 被取消时**停止派发新曲目**：已经写完的文件保留（撤消靠备份清单），
// 正在写的那一首让它写完（写到一半中断会留下半个标签块，那比不写更糟）。
// 少做的部分记在 `Summary.Cancelled` 与逐条的 `ItemResult.Cancelled` 里。
func Execute(ctx context.Context, plan Plan, opts Options, onProgress func(Progress)) Summary {
	opts = withDefaults(opts)
	out := Summary{Total: len(plan.Items), Results: make([]ItemResult, 0, len(plan.Items))}
	if len(plan.Items) == 0 {
		return out
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.Concurrency)
	done := 0
	dispatched := 0

	for _, it := range plan.Items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		dispatched++
		go func(it PlanItem) {
			defer wg.Done()
			defer func() { <-sem }()

			r := writeOne(ctx, it, opts)

			mu.Lock()
			out.Results = append(out.Results, r)
			done++
			if r.Cancelled {
				// 被停止的任务：这一首没跑完，既不是「没搜到」也不是「写失败」。
				out.Cancelled++
			} else {
				if r.Matched {
					out.Matched++
				} else {
					out.NoMatch++
				}
				if r.LyricFilled {
					out.LyricFilled++
				}
				if r.CoverFilled {
					out.CoverFilled++
				}
				if r.GenreFilled {
					out.GenreFilled++
				}
				if r.YearFilled {
					out.YearFilled++
				}
				if r.TrackFilled {
					out.TrackFilled++
				}
				if r.DiscFilled {
					out.DiscFilled++
				}
				if r.LyricRejected {
					out.LyricRejected++
				}
				// 「没搜到」不是「写入失败」：前者是正常结果（同名翻唱、纯音乐、小语种），
				// 后者是真出问题。以前两者都计进 Failed，同一首会在汇总里出现两次。
				if !r.OK && r.Matched {
					out.Failed++
				}
			}
			snap := Progress{Done: done, Total: len(plan.Items), Current: displayName(Target{Path: it.Path, Title: it.Title}), Phase: PhaseWrite}
			mu.Unlock()

			if onProgress != nil {
				onProgress(snap)
			}
		}(it)
	}
	wg.Wait()

	// 连派发都没轮到的（含计划本身就没解析出来的）也要算进去，
	// 否则「停了一次，汇总里只少了 1 首」，用户会以为剩下的都处理过了。
	out.Cancelled += len(plan.Items) - dispatched

	// 落盘备份清单 —— 「撤销」要用它。没备份任何文件时不会留下空目录。
	if opts.Backup != nil {
		if err := opts.Backup.Commit(); err != nil {
			// 清单写不出来不阻断任务（文件已经改了），但要让调用方知道撤销会不可用
			out.BackupError = "备份清单写入失败，本次可能无法撤销：" + err.Error()
		}
		if opts.Backup.Count() > 0 {
			out.BackupDir = opts.Backup.Dir()
			out.BackupSize = opts.Backup.TotalSize()
		}
	}

	sort.Slice(out.Results, func(i, j int) bool { return out.Results[i].Path < out.Results[j].Path })
	return out
}

// writeOne 取歌词/封面并写入一个文件。
func writeOne(ctx context.Context, it PlanItem, opts Options) ItemResult {
	r := ItemResult{Path: it.Path, Title: it.Title, Artist: it.Artist}
	// 已经停止的任务：**一个字节都不碰**。
	// 这里必须挡住 —— 外面还可能排着几首已派发的（并发池里的那几首），
	// 它们在 stop 之后才轮到执行，不挡的话「停止」之后文件还在继续被改写。
	if ctx.Err() != nil {
		r.Cancelled = true
		r.Error = "任务已停止，这首没处理"
		return r
	}
	if !it.Matched {
		r.Error = firstNonEmpty(it.Note, "没有搜到匹配的曲目")
		return r
	}
	r.Matched = true
	r.MatchedBy = it.MatchedBy
	r.MatchedName = it.MatchedName
	r.MatchedByAI = it.MatchedByAI

	item := tidy.Item{
		Path:   it.Path,
		Title:  it.Title,
		Artist: it.Artist,
		Album:  it.Album,
	}

	if it.NeedLyric {
		lrc, _ := search.FetchLyric(it.source, it.songmid, it.MatchedName, it.Artist, it.duration, it.hash)

		// 写入前核验：平台返回的歌词未必对得上（数据错标 / 同名前缀串味）。
		// **写错歌词比不写更糟** —— 用户会以为是真歌词，还失去了「缺歌词」这个可行动的信号。
		//
		// 核验对象是**本地曲目自称的身份**（item.Title/Artist）而不是命中候选的身份：
		// 这样既挡得住平台错标，也挡得住「AI 挑错了歌」——后者正是风险最高的那类。
		//
		// 默认只核验 AI 挑的匹配（数量少、风险高、成本可控）；
		// UseAIVerifyLyric 打开则对所有曲目核验（AI 调用量翻倍，慎用）。
		if lrc != "" && opts.AIClient != nil && (opts.UseAIVerifyLyric || it.MatchedByAI) {
			if v, ok := opts.AIClient.VerifyLyric(ctx, item.Artist, item.Title, lrc); ok && !v.Match {
				r.LyricRejected = true
				r.LyricRejectReason = v.Reason
				lrc = ""
			}
		}
		item.Lyric = lrc
	}

	if it.NeedCover && strings.TrimSpace(it.coverURL) != "" {
		// ⚠️ `tidy.TidyOne` **不会**去下载封面 —— 它明确要求调用方把字节传进来
		// （见 pkg/tidy/tidy.go 里那句「下载交给调用方」）。只填 CoverURL 是**没用的**，
		// 结果只会得到一条 Skipped。所以这里必须自己下。
		// 用 security.FetchImage：带 SSRF 防护、体积上限与魔数校验（不信任 Content-Type）。
		if data, mime := security.FetchImage(it.coverURL, coverReferer(it.source)); len(data) > 0 {
			item.CoverBytes = data
			item.CoverMime = mime
		}
	}

	// 年份/曲序/光盘：值在**预览阶段**就从命中里取好了，执行阶段不再重取 ——
	// 「预览什么就写什么」，不靠二次搜索（搜索结果会飘）。
	item.Year, item.Track, item.Disc = it.FillYear, it.FillTrack, it.FillDisc

	// 风格：**搜索拿到的真实风格优先，AI 推断只是兜底**，而且必须用户显式勾选。
	// 用户的要求是「一定要用搜索的，而不是 AI 瞎猜」—— 这个顺序不能反。
	if it.NeedGenre {
		item.Genre = it.FillGenre
		if strings.TrimSpace(item.Genre) == "" && opts.UseAIGenre && opts.AIClient != nil {
			item.Genre = inferGenre(ctx, opts.AIClient, item.Title, item.Artist)
		}
	}

	// 写入前备份 —— 「撤销上一次补全」靠它。
	// ⚠️ 备份失败就**跳过这个文件**：不能「备份失败还继续写」，
	// 那等于悄悄剥夺了用户的回滚能力。
	if opts.Backup != nil {
		if err := opts.Backup.BeforeWrite(it.Path); err != nil {
			r.Error = "备份原文件失败，已跳过：" + err.Error()
			return r
		}
	}

	// 写标签（格式嗅探 / MP3·FLAC 分支 / 外挂 .lrc 都在 tidy 里）
	res := tidy.TidyOne(item, opts.Tidy)

	// 记下这次新建了哪些 sidecar（.lrc / .jpg），撤销时要把它们删掉
	if opts.Backup != nil {
		opts.Backup.AfterWrite(it.Path)
	}
	r.OK = res.OK
	r.Error = res.Error
	r.LyricFilled = res.LyricEmbed || res.LyricFile
	r.CoverFilled = res.CoverEmbed || res.CoverFile
	r.Album = item.Album
	r.Genre = item.Genre
	r.Year, r.Track, r.Disc = item.Year, item.Track, item.Disc
	// res.MetaWritten 为真 ⇒ tidy 确实把某个文本字段（含年份/曲序/风格）写进了文件
	// —— WriteMetadata 关掉时这些字段根本不会进 meta，所以不会误报。
	r.GenreFilled = item.Genre != "" && res.MetaWritten
	r.YearFilled = item.Year > 0 && res.MetaWritten
	r.TrackFilled = item.Track > 0 && res.MetaWritten
	r.DiscFilled = item.Disc > 0 && res.MetaWritten
	return r
}

// ── AI 兜底：只用于 genre ──

const genreSystem = `你是音乐风格分类助手。用户给你歌名与歌手，你只回一个 JSON 对象：{"genre":"..."}。
genre 用**中文**，尽量落在常见大类里（流行 / 摇滚 / 民谣 / 电子 / 古典 / 说唱 / 爵士 / 金属 / 乡村 / R&B / 原声配乐）。
拿不准就返回空字符串 —— 不要瞎猜、不要编造细分流派。`

// inferGenre 用 AI 推断风格。
//
// 只在这里用 AI，因为**各平台搜索接口都不返回 genre**，没有权威来源可查；
// 而歌词/封面一律走搜索 —— AI 生成歌词是编造内容，绝不用。
// 推断结果做长度与标点校验，防止模型不听话时把一句话塞进标签。
func inferGenre(ctx context.Context, c *ai.Client, title, artist string) string {
	if c == nil || strings.TrimSpace(title) == "" {
		return ""
	}
	user := ai.WithUserPrompt(
		fmt.Sprintf("歌名：%s\n歌手：%s", title, firstNonEmpty(artist, "未知")),
		`{"genre":"..."}`,
	)
	raw, _, err := c.Chat(ctx, "genre", genreSystem, user)
	if err != nil {
		return ""
	}
	var out struct {
		Genre string `json:"genre"`
	}
	if err := ai.ParseJSONObject(raw, &out); err != nil {
		return ""
	}
	g := strings.TrimSpace(out.Genre)
	if g == "" || len([]rune(g)) > 12 || strings.ContainsAny(g, "，。,.；;：:！!？?") {
		return ""
	}
	return g
}

// resolveName 定出「歌手 + 歌名」。优先级：**索引 > AI > 规则**。
//
// ── 分层理由（对照参考实现 music-tidy v1.14.0 的 _parse_name / _llm_should_rescue）──
//
//  1. **索引里的值最可信** —— 它来自文件标签或上次整理，比从文件名猜的准。
//  2. **规则优先于 AI**：`01. 周杰伦 - 晴天.flac` 这种用正则就能拆，不该花 AI 的钱。
//  3. **只有规则「没把握」时才请 AI**。注意不是「规则失败」——
//     `nameparse.Result.NeedsAI()` 的判据是「字段为空 **或** 自认可疑」。
//     后者更重要：把「好听的歌曲推荐」当成歌手，字段非空、看着正常，
//     却会悄悄写进用户的标签。
//  4. **AI 只能从候选里挑**，且结果必须出自文件名 —— 这条硬约束在 `ai.JudgeName` 内部做。
//
// filenameExts 判断「这标题其实是文件名」时用到的扩展名集合。
var filenameExts = map[string]bool{
	".mp3": true, ".flac": true, ".wav": true, ".m4a": true, ".ape": true,
	".ogg": true, ".opus": true, ".aac": true, ".wma": true, ".aiff": true, ".dsf": true,
}

// looksLikeFilename 判断索引/标签里的「标题」其实就是文件名。
//
// 为什么要专门判它：这类标题是早年的扫描或转码脚本直接把文件名塞进去的
// （实测样本 `title="02 晴天.mp3"`、artist 倒是对的）。补全信了这个标题，
// 就等于拿一个带扩展名的字符串去搜索 —— **永远搜不到**，
// 而界面上只表现为「这首没匹配上」，完全看不出根因是标签脏。
//
// 判据保守：带音频扩展名，或去掉扩展名后与文件名归一化相同（下划线/空格差异算相同）。
// 真歌名恰好等于文件名的情况判别不出来，但那时退回的结果本来也一样，不亏。
func looksLikeFilename(title, base string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	if filenameExts[strings.ToLower(filepath.Ext(t))] {
		return true
	}
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, "_", " ")
		return strings.Join(strings.Fields(s), " ")
	}
	return norm(t) == norm(strings.TrimSuffix(base, filepath.Ext(base)))
}

func resolveName(ctx context.Context, opts Options, path, idxTitle, idxArtist string) (string, string) {
	title := strings.TrimSpace(idxTitle)
	artist := strings.TrimSpace(idxArtist)

	// 脏标题当「没有标题」处理，让下面的规则解析 / AI 判名从文件名重新认一遍。
	// 歌手不动 —— 它往往是好的，扔掉反而更难搜。
	if looksLikeFilename(title, filepath.Base(path)) {
		title = ""
	}

	// 先试用户自定义规则（有的话），没命中回落到内置启发式
	var r nameparse.Result
	if opts.NameRules != nil {
		r = opts.NameRules.Parse(filepath.Base(path))
	} else {
		r = nameparse.Parse(filepath.Base(path))
	}

	// 索引缺的部分，先用规则结果占位
	ruleTitle, ruleArtist := r.Title, r.Artist

	// 索引齐了就不用折腾了
	if title != "" && artist != "" {
		return title, artist
	}
	if !opts.UseAINameJudge || opts.AIClient == nil || !r.NeedsAI() {
		return firstNonEmpty(title, ruleTitle), firstNonEmpty(artist, ruleArtist)
	}

	// 候选：规则给的（含左右两种解释）+ 索引给的
	cands := make([]ai.NameCandidate, 0, len(r.Candidates)+1)
	for _, c := range r.Candidates {
		cands = append(cands, ai.NameCandidate{Artist: c.Artist, Title: c.Title})
	}
	if title != "" || artist != "" {
		cands = append(cands, ai.NameCandidate{Artist: artist, Title: title})
	}

	if picked, ok := opts.AIClient.JudgeName(ctx, filepath.Base(path), cands); ok {
		// AI 只补索引缺的那部分，不覆盖索引已有的值
		if title == "" {
			title = picked.Title
		}
		if artist == "" {
			artist = picked.Artist
		}
	}
	return firstNonEmpty(title, ruleTitle), firstNonEmpty(artist, ruleArtist)
}

// maxMatchCandidates 交给 AI 挑的候选上限。
//
// 四个平台各 10 条就是 40 条，prompt 会明显变长、也更贵；20 条足够覆盖常见情况
// （按平台顺序截断，前面的平台优先 —— 那正是我们更信任的平台）。
const maxMatchCandidates = 20

// pickByAI 把候选池交给 AI 挑一条。
func pickByAI(ctx context.Context, c *ai.Client, title, artist, album string, pool []search.UnifiedSong) (search.UnifiedSong, bool) {
	if len(pool) > maxMatchCandidates {
		pool = pool[:maxMatchCandidates]
	}
	cands := make([]ai.SongCandidate, 0, len(pool))
	for _, s := range pool {
		cands = append(cands, ai.SongCandidate{
			Name: s.Name, Singer: s.Singer, Album: s.Album,
			Duration: s.Duration, Source: s.Source,
		})
	}
	idx, ok := c.PickSongMatch(ctx, ai.SongQuery{Title: title, Artist: artist, Album: album}, cands)
	if !ok || idx < 0 || idx >= len(pool) {
		return search.UnifiedSong{}, false
	}
	return pool[idx], true
}

// coverReferer 部分平台 CDN 有防盗链，带上对应站点的 Referer 更稳。
// 实测 QQ 的 y.gtimg.cn 不带也能下，但带上不亏。
func coverReferer(source string) string {
	switch source {
	case "tx":
		return "https://y.qq.com/"
	case "wy":
		return "https://music.163.com/"
	case "kg":
		return "https://www.kugou.com/"
	case "kw":
		return "https://www.kuwo.cn/"
	}
	return ""
}

func displayName(t Target) string {
	if s := strings.TrimSpace(t.Title); s != "" {
		return s
	}
	return filepath.Base(t.Path)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
