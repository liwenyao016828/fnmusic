package config

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CustomSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Script      string `json:"script"`
	CreatedAt   int64  `json:"created_at"`
}

// APISource 「按服务地址」接入的音源（自建 / 社区中转服务）。
//
// 与 CustomSource 的区别：CustomSource 是一段 LX 脚本，**在浏览器里执行**；
// APISource 是一个 HTTP 服务地址，由**后端**直接发请求拿直链。
// 二者互补 —— 脚本型需要用户有脚本，服务型只需要一个地址。
type APISource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Token    string `json:"token,omitempty"`
	Protocol string `json:"protocol"` // lx_script / lx_server / lx_api
	Enabled  bool   `json:"enabled"`
	// Note 探测时留下的说明（如「解析接口正常」），便于界面展示
	Note      string `json:"note,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type AppConfig struct {
	Port          int    `json:"port"`
	DefaultNasDir string `json:"default_nas_dir"`
	DownloadDir   string `json:"download_dir"`
	// LogDir 日志落盘目录（2026-10-26 用户口径：「设置增加日志存储位置」）。
	// 空串 = 默认不落盘（见 pkg/applog 包注释）；设置后每条运行日志同时写进 <LogDir>/qulv.log，
	// 启动时由 NewServer 恢复生效。校验走 downloader.ValidateNasDir（绝对路径 + NAS 存储卷内）。
	LogDir string `json:"log_dir"`
	// 注：读目录请用 LibraryDir()，不要直接读字段 —— 空值时它要回退 DefaultNasDir，
	// 两处各写一份回退逻辑迟早会漂（下载器就踩过：显示 /vol1/Music、实际写别处）。
	ActiveSourceID string `json:"active_source_id"`
	// ActiveSourceIDs 启用池（有序，前面的优先）。用户 2026-09-30 定的多选激活：
	// 可以同时启用多个音源，取链按池内顺序试、第一个失败自动换下一个。
	//
	// ⚠️ **不要加 `omitempty`**：`[]` 与「字段缺失」在这里是**两种不同状态** ——
	//    · 字段缺失（nil）= 从没设置过 → 迁移时用旧的 active_source_id 兜底、全新安装还能自动启用第一个；
	//    · `[]`（非 nil 空）= 用户**显式把全部音源都关掉**了 → 必须尊重，不能又给他自动开一个。
	//    加了 omitempty 之后这两种状态会被一起抹成"缺失"，用户关掉的全部音源会自己活过来。
	ActiveSourceIDs []string `json:"active_source_ids"`
	Theme           string   `json:"theme"`
	VisualizerMode  string   `json:"visualizer_mode"`
	PreferQuality   string   `json:"prefer_quality"`
	// PlaybackQuality 是**试听**（飞牛页面在线播放）的音质偏好，与 PreferQuality
	// （下载）分开：两者要的东西不一样 —— 下载可以慢慢等一份无损，试听要先出声。
	//
	//   auto（默认）= 本平台内阶梯 flac → 320k → 128k，能上无损就上无损，最快；
	//   lossless   = 无损优先：本平台拿不到无损时再去下载源池找同名无损版（慢一点，真无损）；
	//   320k / 128k = 固定档位，不降级（拿不到就是拿不到）。
	//
	// 空串视为 auto（老配置里没有这个键）。
	PlaybackQuality string `json:"playback_quality,omitempty"`
	// AutoDownloadQuality 是「飞牛音乐页面里触发的下载」（收藏 / 加入歌单 /
	// 点「下载到曲率」）要的音质档位。
	//
	//   highest（默认）= 能无损就无损（走下载源池原本的排序）；
	//   flac / 320k / 128k = 点名要这一档：正好是那一档的候选排最前，
	//   没有就按原顺序兜底（宁可下到别的档位，也不要让收藏变成下载失败），
	//   实际档位会如实写进自动下载记录。
	//
	// 空串视为 highest（老配置里没有这个键）。
	AutoDownloadQuality string `json:"auto_download_quality,omitempty"`
	// AutoDownloadChannels 是收藏 / 加歌单自动下载**允许哪些取音渠道、按什么顺序试**。
	//
	// 用户 2026-10-21 的要求（m02588）：「增加一个自动下载源（方式：lx，网易云，dl）」
	// —— 这三条路以前是写死的顺序（原生 → 洛雪 → 外挂池），用户既关不掉一条，
	// 也没法让洛雪排前面。而他收藏《不再犹豫》那次恰恰就是被这个顺序坑的：
	// 免登录网易云只给 320k，代码拿到「够用的基线」后就没再给别的渠道机会。
	//
	//	"native" = 曲目自己平台的原生解析（网易云 / QQ / 酷狗 …）
	//	"lx"     = 洛雪音源脚本（经 sidecar/lx_host 执行）
	//	"dl"     = musicdl 外挂池（咪咕 / 千千 / B 站 …，取 musicdl_sources）
	//
	// 约定（见 NormalizeDownloadChannels）：**这个列表本身就是允许集 + 顺序**，
	// 没列出的渠道 = 关掉；空 = 默认三档全开 `["lx","dl","native"]`。
	// 顺序只决定「先问谁」，**不承诺音质** —— 换源判据仍然是实际解析出的档位，
	// 不比基线好的候选一律不换（v2.13.0 定下的口径，不许用顺序去推翻它）。
	AutoDownloadChannels []string `json:"auto_download_channels,omitempty"`
	// DownloadQualityOverride 是**全局**「下载音质」开关（照参考实现的
	// FNMUSIC_DL_QUALITY）。它不改上面那两个字段，而是**盖住**它们 ——
	// 「跟着各自设置」和「这次我想都用某档」是两件事，混进一个字段就再也回不去了。
	//
	//   ""         = 跟随各入口自己的设置（默认）；
	//   "lossless" = 一律点名无损（下不到就按各入口自己的规则兜底，不会变成下载失败）；
	//   "mp3_320"  = 一律点名 320k。
	//
	// 只有这两个取值会被认；认不出就当作跟随。**没有转码能力**（NAS 上没有
	// ffmpeg），所以「MP3 320k」是「点名这一档」而不是「转成 320k」。
	//
	// 指针是必须的：空串本身就是一个合法取值（= 跟随）。用 string 的话
	// 「非空才写」的合并规则会让它永远回不到跟随 —— 与 CacheDir 同一个坑。
	DownloadQualityOverride *string        `json:"download_quality_override,omitempty"`
	CustomSources           []CustomSource `json:"custom_sources"`
	// APISources 按服务地址接入的音源，见 APISource
	APISources []APISource `json:"api_sources,omitempty"`
	// PreferredPlatforms 主力平台（有序）。默认网易 + QQ ——
	// 酷狗/酷我只用来兜底「只有它们上有」的歌。消费方：补全的平台顺序、
	// 以及兜底命中时「回主力平台借年份/曲序/风格」的查找顺序。空 = 用默认。
	PreferredPlatforms []string `json:"preferred_platforms,omitempty"`
	// ChartPlaylists 是「要注入到飞牛音乐歌单列表里的榜单」名单，元素形如
	// `wy_19723756`（`<平台>_<榜单 id>`，与虚拟歌单 guid 的后缀同形）。
	//
	// 这里**可以**用 omitempty：与 ActiveSourceIDs 不同，「从没设置过」与
	// 「显式清空」在语义上是同一件事 —— 两者都是「一个榜单都不注入」，
	// 不存在需要区分的状态。
	ChartPlaylists []string `json:"chart_playlists,omitempty"`
	// ChartPlaylistsEnabled 是榜单注入的**总开关**。用指针是为了表达三态：
	// 没设置过（nil，视为开）/ 显式开 / 显式关。关掉时**名单照旧留着** ——
	// 临时关一下再打开不用重勾一遍（fnmusic-ext 也是「总开关 + 名单」两个键）。
	// 读它请走 ChartsInjectEnabled()，别自己判 nil。
	ChartPlaylistsEnabled *bool `json:"chart_playlists_enabled,omitempty"`
	// ChartSize 是**每个榜单显示多少首**（用户 2026-10-24 要求「首数可设」）。
	//
	// 0（或不设）= 用内置默认（intercept 包的 vChartSize，100 首）。
	// 限制在 [ChartSizeMin, ChartSizeMax]：太少看不出榜、太多一次拉取会拖慢页面，
	// 区间校验在 NormalizeChartSize 里做，别在校验函数之外各自判。
	ChartSize int `json:"chart_size,omitempty"`
	// ChartSizes 按榜单单独设的显示首数：`wy_19723756` → 50。
	// 有这个键就用它，否则用 ChartSize，再否则用默认 —— 这样临时把一个榜调短、
	// 其它榜跟着全局走，不用逐个配。
	ChartSizes map[string]int `json:"chart_sizes,omitempty"`
	// ── 数据快照的定时设置（用户 2026-10-24：「记录改为数据快照，增加定时快照」）──
	//
	// SnapshotScheduleEnabled 定时快照总开关。**默认关**：整库读标签是重活，
	// 悄悄按天跑一份快照会一点点吃掉磁盘，得让用户自己打开。
	// 用指针是为了让「局部 patch 里没有这个键」和「显式关掉」分得开。
	SnapshotScheduleEnabled *bool `json:"snapshot_schedule_enabled,omitempty"`
	// SnapshotIntervalHours 多久拍一份（小时）。0 / 缺省 → 默认 24 小时。
	// 上限 30 天，下限 1 小时 —— 定时快照是兜底手段，不是备份盘。
	SnapshotIntervalHours int `json:"snapshot_interval_hours,omitempty"`
	// SnapshotKeep 保留几份定时快照（超出的从最旧的开始删）。0 → 默认 7。
	SnapshotKeep int `json:"snapshot_keep,omitempty"`
	// ── 后台「音质补强」的设置（用户 2026-10-25：「音质补强是不是有个自动补强？下载-自动
	// 记录里有个记录，但是我没法控制呢？增加相应的定时按钮，补强音质来源，音质。将这个自动
	// 下载功能设置增加到设置。」）──
	//
	// 背景：在那之前补强轮是**写死**的 —— 20 分钟一轮、每轮 3 首、只在「收藏自动下载」开着
	// 时才动（见 pkg/intercept/upgrade_sweeper.go）。它的结果只出现在「下载-自动记录」里
	// 一行「自动补强」，用户在界面上既看不到它在跑，也关不掉、调不了。
	//
	// UpgradeSweepEnabled 补强轮的总开关（**独立于收藏自动下载**）。
	//
	// 指针 = 三态，且**老配置（nil）沿用原来的语义**：以前能补强 ⟺ FavAutoDownload 开着，
	// 所以 nil 时读出来就是「跟收藏自动下载同步」（见 UpgradeSweepOn）。升级不应把用户
	// 正在跑的补强悄悄停掉，也不该给没开收藏下载的人突然打开一个后台重活。
	UpgradeSweepEnabled *bool `json:"upgrade_sweep_enabled,omitempty"`
	// UpgradeSweepIntervalMinutes 两轮之间隔多久（分钟）。0 / 缺省 → 默认 240（等于原来的
	// 20 分钟 × 12 轮、按「一天补完一小批」的节奏，但让用户能选）。区间见 NormalizeUpgradeSweep。
	UpgradeSweepIntervalMinutes int `json:"upgrade_sweep_interval_minutes,omitempty"`
	// UpgradeSweepBatch 一轮最多派几首去补。0 / 缺省 → 默认 3（原来的 upgradeSweepBatch）。
	// 上限 10：一次探测在洛雪渠道要几十秒，一轮派太多会把用户此刻的收藏下载挤到后面。
	UpgradeSweepBatch int `json:"upgrade_sweep_batch,omitempty"`
	// UpgradeSweepIgnoreCooldown 是否无视 24 小时冷却位。
	//
	// 原来失败一定记冷却，用户想「再试一次」只能等一天 —— 而第三方站是随时会好的。
	// 打开后每一轮都从「最该补的」开始重试（仍受 upgradeMaxTries 的总次数上限约束，
	// 不会无限重试）。**缺省关**：它会让外呼变密，该由用户明确打开。
	UpgradeSweepIgnoreCooldown *bool `json:"upgrade_sweep_ignore_cooldown,omitempty"`
	// ── 应用内更新：GitHub 个人 token（用户 2026-10-25）──
	//
	// 「新增个人token输入」。（同一次需求里的「限制速率」被用户当场否掉了：
	// 「不需要下载限速」—— 所以这里没有速率字段，别再补。）
	//
	// UpdateGitHubToken 是访问 GitHub API 用的个人访问令牌（PAT）。
	//
	// 未鉴权时 GitHub 限速 **60 次/小时/IP**，共享代理后面这一个出口是很多人在用，
	// 检查更新经常直接 403（本项目实测过）。填个只读 token 后是 5000 次/小时，
	// 而且能看私有仓库。空 = 不用 token。
	//
	// ⚠️ 这个值会落盘到配置文件里（和其它 AI key 同级），**永远不回传给前端**
	// ——读接口只回 update_token_set（布尔）。要清空用 UpdateClearToken。
	UpdateGitHubToken string `json:"update_github_token,omitempty"`
	// UpdateClearToken 与 ChartSizeOverride 同一套路：只在 /api/config 内部流转，
	// 因为 UpdateGitHubToken 带 omitempty，「传空串清空」与「没传」解码后一样。
	UpdateClearToken bool `json:"-"`
	// UpdateTokenSet 是**只读**的派生字段：表示「磁盘上存了 token」，
	// 供 GET /api/config 回给前端显示状态（值本身在 redactConfig 里被抹掉）。
	UpdateTokenSet bool `json:"update_token_set,omitempty"`
	// UpdateProxy 是应用内出站代理（http / https / socks5）。
	//
	// 为什么要有这个字段（用户 2026-10-25，NAS 上更新失败截图）：
	// 应用由 fnOS 拉起，**读不到 shell 里的 HTTP_PROXY**，而 GitHub 的 release
	// 资产（objects.githubusercontent.com）在部分网络下直连就是
	// `dial tcp 20.205.243.166:443: i/o timeout`。用户的原话是要能在应用里设置，
	// 而不是「SSH 上去给应用进程加环境变量」。
	//
	// 只作用于**应用内更新**（检查/下载 fpk）这一条链路，不影响音源与播放：
	// 那些走后端自己的 transport，代理由那里的环境变量决定。
	// 空 = 不额外配置（沿用环境变量 / 直连）。
	UpdateProxy string `json:"update_proxy,omitempty"`
	// UpdateClearProxy 与 UpdateClearToken 同一套路：omitempty 让空串与「没传」同形，
	// 所以「清空代理」要一个显式标记。
	UpdateClearProxy bool `json:"-"`
	// UpdateMirrors 是「去哪儿查新版本 / 下安装包」的镜像清单（用户 2026-10-25：
	// 「增加默认镜像站，可删减增加」「可以填自己的」「回退」）。
	//
	// 用**指针**是必须的：`[]` 和「没这个键」解码后都是 len==0，但前者表示
	// 「我只要官方源」，后者表示「没配过、用默认」。语义差别很大，靠指针区分。
	// nil → 读的时候用 DefaultUpdateMirrors()。
	// 详见 pkg/config/mirrors.go 的说明（为什么默认只有 gh-proxy.com）。
	UpdateMirrors *[]UpdateMirror `json:"update_mirrors,omitempty"`
	// UpdateResetMirrors 是内部的「恢复默认清单」标记：清空 UpdateMirrors 让人
	// 回不到默认（回到 nil 只能靠删配置文件），所以单给一个动作。不落盘。
	UpdateResetMirrors bool `json:"-"`
	// ChartSizeOverride 是**只在 /api/config 内部流转**的提示位：这次 Update 要把
	// ChartSize / ChartSizes 当成「整体覆盖」而不是「非空才改」。
	//
	// 为什么需要它：这两个字段带 omitempty，`0` 和「没传这个键」在解码后长得
	// 一模一样，而「改回默认」正是要写 0 / 写空表。接口层用 `*_overwrite` 键接住
	// 这种意图，再把这个位置成 true 交给 Update —— 不落盘（omitempty + 不是真配置）。
	ChartSizeOverride bool `json:"-"`
	// UIInjectEnabled 是「往飞牛官方音乐页面注入脚本」的开关（默认开）。
	//
	// 注入只往**页面文档**里加一行 `<script>`，不改官方代码；但这是改到别人家门口的
	// 功能，必须留一个一键退路：关掉之后 `handlePageShell` 直接不认领文档请求，
	// 官方页面回到**逐字节原样**（由接管层的反向代理透传）。
	// nil = 开。读它请走 InjectUIEnabled()，别自己判 nil。
	UIInjectEnabled *bool `json:"ui_inject_enabled,omitempty"`
	// MusicDLEnabled 是 musicdl sidecar 的开关（**默认关**）。
	//
	// 打开后曲率会：①在数据目录里建一个 Python venv 并装 musicdl 那套依赖
	// （首次约 1-2 分钟，需要能访问 PyPI）；②拉起一个 sidecar 进程；
	// ③把咪咕 / 千千 / B站 这几个原生没有的平台接进搜索与解析。
	//
	// 为什么默认关：它会占磁盘（venv 一两百 MB）、首次启动要联网装依赖。
	// 这两件事都该由用户明确同意，不能因为「能力更好」就替他决定。
	// nil = 关。读它请走 MusicDLOn()，别自己判 nil。
	MusicDLEnabled *bool `json:"musicdl_enabled,omitempty"`
	// MusicDLSources 是 sidecar 要启用的平台短码（`mg` / `bq` / `bi` / `kg` / `kw` …）。
	//
	// 空 = 用默认（咪咕 + 千千 + B站）。⚠️ 把 `kg` / `kw` 加进来会**改变现有行为**：
	// 这两个平台原生的搜索能用但拿不到直链，加进来之后搜索与解析都改走 musicdl
	// （一个平台只能有一个 id 空间，否则「搜得到、点开放不出」）。
	// ⚠️ **不要**加 omitempty：nil（从没配过）与 []（用户把源全关掉）是两种不同的
	// 状态，见 MusicDLActiveSources。加 omitempty 会把 [] 序列化成缺字段，
	// 重启后读回来变成 nil → 「全关」又悄悄变回默认源。
	MusicDLSources []string `json:"musicdl_sources"`
	// MusicDLLimit 是 sidecar 每个源返回的条数（默认 15）。
	//
	// ⚠️ 别调太大：musicdl 按这个数量**翻页**且每条都会先解析直链，
	// 30 会变成 6 页、整次搜索在超时前一条都交不出来。
	MusicDLLimit int `json:"musicdl_limit,omitempty"`
	// MusicDLPort 是 sidecar 监听的**回环**端口（默认 8901）。
	MusicDLPort int `json:"musicdl_port,omitempty"`
	// MusicDLPython 是建 venv 用的 Python 解释器路径，空 = 自动找
	// （优先 /usr/bin/python3）。只在自动找错了的时候才需要填。
	MusicDLPython string `json:"musicdl_python,omitempty"`
	// FavAutoDownload 是「在飞牛音乐里收藏 / 加入歌单时自动下载并绑定本地」的开关。
	//
	// 打开后：收藏（或加入歌单）一首**在线**曲目时，后台把整轨下到 NAS 曲库；
	// 下完之后那首歌的取流改走本地文件（「绑定」）—— 收藏条目继续可用，但已经在
	// 听硬盘上那份。这是 fnmusic-ext 的 `fav_auto_bind` 在曲率这边的等价物。
	//
	// **默认关**：它会真的占磁盘和带宽（无损一首几十兆），该由用户明确同意。
	// nil = 关。读它请走 FavAutoDownloadOn()，别自己判 nil。
	FavAutoDownload *bool `json:"fav_auto_download,omitempty"`
	// LyricAutoDownload 是「下载时自动带上歌词」的开关：自动下载（收藏 / 加入歌单 /
	// 边听边下提升）时同时内嵌歌词并写同名 .lrc。
	// **nil = 开**（老配置里没这个字段，而「下下来的歌没歌词」是明显的退步）。
	LyricAutoDownload *bool `json:"lyric_auto_download,omitempty"`
	// CoverEmbed 是「下载时把封面内嵌进标签」的开关。**nil = 开**，理由同上。
	CoverEmbed *bool `json:"cover_embed,omitempty"`
	// TeeEnabled 是「边听边下」的开关：播放在线曲目时顺手把同一条流写进磁盘，
	// 完整拿到就提升成曲库文件（与「收藏自动下载」共用同一张登记表）。
	// nil = 关。读它请走 TeeOn()，别自己判 nil。
	TeeEnabled *bool `json:"tee_enabled,omitempty"`
	// DailyLLMEnabled 是「每日推荐里的**大模型推荐层**」的开关。
	//
	// # 为什么要与「AI 大模型」的总开关分开
	//
	// AI 总开关（`ai.Config.Enabled`）管的是**别的用途**：文件名判名、搜索匹配消歧、
	// 歌词核验、生成命名正则 —— 那些发出去的是**文件名与曲目元数据**。
	// 这一层发出去的是**用户的收听历史与收藏**（歌名 + 歌手 + 专辑，字段级清单见
	// `pkg/ai.RecommendRequest`）—— 是另一类数据。
	//
	// 用户为了「清理文件名」而打开 AI，不该等于默认同意把自己的听歌记录发给模型。
	// 所以这两件事各有一个开关，**两个都开**这一层才动：本开关 = 用户对「把收听
	// 历史发出去」这件事的明确同意；AI 总开关 = 有没有模型可调（没配好时静默降级）。
	//
	// # 为什么缺省是关
	//
	// 它会**真的把收听历史发到外部**，还可能每天花掉几次 token，该由用户明确打开。
	// ⚠️ 参考实现 `/tmp/fnme-z` 没有这个开关 —— 它「配了 `FNMUSIC_LLM_*` 就启用」。
	// 这里刻意不照抄：那是它把 LLM 配置本身当开关用，而曲率的 AI 配置是**全局**的、
	// 已经被另外五个用途占着了（见 HANDOVER §3.5）。
	//
	// nil = 关。读它请走 DailyLLMOn()，别自己判 nil。
	DailyLLMEnabled *bool `json:"daily_llm_enabled,omitempty"`

	// ── 服务端洛雪宿主（goal-a5eb2a23 ⑤ 方案 b）────────────────────────
	// ⚠️ 打开后**第三方音源脚本会跑在服务端** —— 这故意破了原来「后端不执行第三方 JS」
	// 的约束，是用户 2026-10-06 明确选的 (b)。所以缺省**关**，界面文案要写明这一点。
	//
	// 只有两个键，是刻意的：路径类字段一旦用字符串，`Update` 收到「部分更新」时
	// 会被空值清掉（这个项目踩过同类坑）。所以脚本目录/宿主脚本路径**由程序推导**，
	// 只把「开不开」和「端口」交给用户。
	LXServerEnabled *bool `json:"lx_server_enabled,omitempty"`
	LXServerPort    int   `json:"lx_server_port,omitempty"` // 0 = 用缺省 8920
	// FnosToken 手工指定的飞牛音乐令牌（故障排查用）。
	// 非空时优先于环境变量与数据库自动读取；留空表示走自动读取。
	FnosToken string `json:"fnos_token,omitempty"`

	// ── 下载落盘相关（2026-10-15 加，来自上游 zouclang/fnos_music_ext v2.7.8）──

	// FilenameFormat 是曲库文件的命名格式：`歌曲名 - 歌手名` 还是 `歌手名 - 歌曲名`。
	//
	// 取值见 pkg/namefmt（空 / 认不出一律当 `artist-title`，也就是**改造前的行为**）：
	//   artist-title（默认）→ 「晴天 - 周杰伦」
	//   title-artist        → 「周杰伦 - 晴天」
	//
	// ⚠️ 换格式**不会**自动重命名已经落盘的歌，只会影响之后新下载的文件；
	// 但扫描/去重/找歌词这三条读取侧都会按同一个格式反解文件名（见 pkg/namefmt.Parse），
	// 所以中途切换会让**老文件**的歌手/歌名在列表里读反 —— 这是这个开关唯一的坑，
	// 界面上必须写清楚（用户可以先用「整理」把老文件批量改名）。
	FilenameFormat string `json:"filename_format,omitempty"`

	// CacheDir 是「边听边存 / 曲库暂存」的目录。
	//
	// 空 = 用程序推导的默认值（下载目录下的隐藏子目录），与改造前一致。
	// 之所以允许独立于下载目录：缓存是要被淘汰的临时数据，用户常想把它放
	// 在系统盘之外、或者干脆单独限容；而下载目录是**成品**，两者混在一起
	// 会让「清理缓存」一不小心删掉成品。
	//
	// ⚠️ 与 DownloadDir **不得相同、也不得互为父子** —— 校验在 pkg/config 的
	// ValidateDirs()，调用点在保存配置的接口和下载/接管启动处。
	// nil = 没设置过（用默认），指向空串 = **显式**用默认（界面上「恢复默认」走这条），
	// 指向路径 = 指定。三态的理由与 CoverEmbed 完全一样：只有两态的话，
	// 用户一旦设过自定义目录就再也回不到默认值了。
	CacheDir *string `json:"cache_dir,omitempty"`
}

// ActiveIDs 返回启用池（有序）。**旧配置**（只有单值 active_source_id）自动升成单元素池。
// 返回副本，避免调用方改到配置内部的切片。
func (a AppConfig) ActiveIDs() []string {
	if a.ActiveSourceIDs != nil {
		return append([]string(nil), a.ActiveSourceIDs...)
	}
	if a.ActiveSourceID != "" {
		return []string{a.ActiveSourceID}
	}
	return nil
}

// SetActiveIDs 写入启用池，并同步兼容字段 `active_source_id` = 池里第一个。
// 同步兼容字段是为了**旧的调用方 / 外部 AI**：它们只认单值，给它们"池里优先级最高的那个"最合理。
func (a *AppConfig) SetActiveIDs(ids []string) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	a.ActiveSourceIDs = clean
	if len(clean) > 0 {
		a.ActiveSourceID = clean[0]
	} else {
		a.ActiveSourceID = ""
	}
}

// NormalizeChartPlaylists 清洗榜单名单：去空白、转小写、去重、丢掉空元素。
//
// 形态（`<平台>_<榜单 id>`）的合法性**不在这里判** —— 「哪些平台有直链解析器」
// 是拦截层的事，配置层只负责把同一份名单稳定地存成同一种形状。
// 两边都归一化一次，将来加平台时不用改磁盘格式。
func NormalizeChartPlaylists(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return clean
}

// ChartsInjectEnabled 是「榜单注入」总开关的读取口径：**没设置过就是开**。
//
// 这样新装的用户不用先去界面上点一下总开关才能用；而已经显式关掉的人，
// 重启后仍然是关的（我们存的是 false，不是「没存」）。
func (a AppConfig) ChartsInjectEnabled() bool {
	return a.ChartPlaylistsEnabled == nil || *a.ChartPlaylistsEnabled
}

// 榜单显示首数的上下限。
// 下限给 10：再少就不像「榜」，只有个位数的话还不如直接看在线歌单。
// 上限给 300：与 intercept 包的 vChartFetchMax 对齐 —— 显示数超过一次能取回的
// 条数没有意义（多出来的全是空），而且首数越大拉取越慢。
const (
	ChartSizeMin     = 10
	ChartSizeMax     = 300
	DefaultChartSize = 100
)

// HasUpdateToken 判断是否配了 GitHub token（只回布尔，绝不回值）。
func (a AppConfig) HasUpdateToken() bool {
	return strings.TrimSpace(a.UpdateGitHubToken) != ""
}

// UpdateMirrorList 返回实际生效的镜像清单（未配置时是默认清单）。
//
// 只返回启用项？不 —— 停用的也要返回，界面得把它们显示出来才能「重新启用」。
// 调用方（appupdate）自己按 Disabled 过滤。
func (a AppConfig) UpdateMirrorList() []UpdateMirror {
	if a.UpdateMirrors == nil {
		return DefaultUpdateMirrors()
	}
	return NormalizeUpdateMirrors(*a.UpdateMirrors)
}

// UpdateProxyURL 返回归一后的应用内更新代理；没配 / 非法时返回空串。
//
// 归一（补 scheme、去空白）放在读写两端共用的一个函数里：界面上最容易漏的就是
// scheme，只填 `192.168.1.3:7890` 也得能用。
func (a AppConfig) UpdateProxyURL() string {
	return NormalizeUpdateProxy(a.UpdateProxy)
}

// NormalizeUpdateProxy 把用户填的代理地址归一成带 scheme 的 URL 字符串。
//
// 只认 http / https / socks5 / socks5h；不合法就返回空串（当没配），
// 与 security.parseProxyURL 的判定保持一致 —— 两端判据不同的结果就是
// 「界面显示已配置，实际不生效」这类查半天的问题。
func NormalizeUpdateProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return raw
	default:
		return ""
	}
}

// NormalizeChartSize 把任意输入夹到合法区间；<=0 表示「用默认」。
//
// 单独成函数是为了让读写两端用同一把尺：配置校验、前端下拉、intercept 取数
// 三处各写一遍迟早会漂。返回 0 表示「没设」，调用方自己换成 DefaultChartSize。
// NormalizeChartSize 把「用户填的数」夹进 [ChartSizeMin, ChartSizeMax]。
//
// ⚠️ 返回 0 **只代表「没填 / 没设置」**，绝不表示「清空」—— 清空走 Update 里
// 的 ChartSizeOverride 整体覆盖分支（那里直接赋 0，不经过本函数）。把这里的 0
// 当成「清空」会让「填 0 = 恢复默认」和「填 0 = 非法」搅成一团。
func NormalizeChartSize(n int) int {
	if n <= 0 {
		return 0
	}
	if n < ChartSizeMin {
		return ChartSizeMin
	}
	if n > ChartSizeMax {
		return ChartSizeMax
	}
	return n
}

// NormalizeChartSizes 是 normalizeChartSizes 的导出别名（接口层 POST /config
// 也要用它，两处必须同一把尺；内部调用保留小写名，改动面最小）。
func NormalizeChartSizes(in map[string]int) map[string]int { return normalizeChartSizes(in) }

// normalizeChartSizes 清洗按榜单的首数表：键转小写去空白，值夹到合法区间，
// 非法或 <=0 的项直接丢掉（丢弃 = 让该榜跟着全局走，而不是记成 0）。
func normalizeChartSizes(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		id := strings.ToLower(strings.TrimSpace(k))
		if id == "" {
			continue
		}
		if n := NormalizeChartSize(v); n > 0 {
			out[id] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ChartSizeFor 取某个榜单**实际显示多少首**。
// 优先级：该榜单独设的 > 全局 ChartSize > 默认。
func (a AppConfig) ChartSizeFor(chartID string) int {
	if n := NormalizeChartSize(a.ChartSizes[chartID]); n > 0 {
		return n
	}
	if n := NormalizeChartSize(a.ChartSize); n > 0 {
		return n
	}
	return DefaultChartSize
}

// ── 数据快照定时设置（见 AppConfig.SnapshotScheduleEnabled）──

const (
	// SnapshotDefaultIntervalHours 没设置时的快照间隔：一天一份。
	SnapshotDefaultIntervalHours = 24
	// SnapshotMinIntervalHours / SnapshotMaxIntervalHours 间隔的合理区间。
	// 下限 1 小时而不是 1 分钟：整库读标签本身就要几十秒到几分钟，
	// 配得更密只会互相叠着跑，磁盘也是白白发热。
	SnapshotMinIntervalHours = 1
	SnapshotMaxIntervalHours = 24 * 30
	// SnapshotDefaultKeep 默认保留几份定时快照。
	SnapshotDefaultKeep = 7
	// SnapshotMaxKeep 保留份数上限：再多就不像「快照」而像「备份盘」了。
	SnapshotMaxKeep = 60
)

// SnapshotInterval 返回定时快照的实际间隔，配错一律夹回合法区间。
func (a AppConfig) SnapshotInterval() time.Duration {
	n := a.SnapshotIntervalHours
	if n <= 0 {
		n = SnapshotDefaultIntervalHours
	}
	if n < SnapshotMinIntervalHours {
		n = SnapshotMinIntervalHours
	}
	if n > SnapshotMaxIntervalHours {
		n = SnapshotMaxIntervalHours
	}
	return time.Duration(n) * time.Hour
}

// SnapshotRetention 返回定时快照保留几份。
func (a AppConfig) SnapshotRetention() int {
	n := a.SnapshotKeep
	if n <= 0 {
		n = SnapshotDefaultKeep
	}
	if n > SnapshotMaxKeep {
		n = SnapshotMaxKeep
	}
	return n
}

// ── 后台「音质补强」的设置读取口径（见 AppConfig.UpgradeSweepEnabled）──

const (
	// UpgradeSweepDefaultIntervalMinutes 默认两轮间隔：4 小时。
	UpgradeSweepDefaultIntervalMinutes = 240
	// UpgradeSweepMinIntervalMinutes 间隔下限：一次探测在洛雪渠道要几十秒，
	// 再密就是连续外呼第三方站了。
	UpgradeSweepMinIntervalMinutes = 15
	// UpgradeSweepMaxIntervalMinutes 间隔上限：一天。
	UpgradeSweepMaxIntervalMinutes = 24 * 60
	// UpgradeSweepDefaultBatch 一轮默认派几首。
	UpgradeSweepDefaultBatch = 3
	// UpgradeSweepMaxBatch 一轮上限。
	UpgradeSweepMaxBatch = 10
)

// NormalizeUpgradeSweepInterval 把间隔（分钟）夹进合法区间，<=0 取默认。
func NormalizeUpgradeSweepInterval(n int) int {
	if n <= 0 {
		return UpgradeSweepDefaultIntervalMinutes
	}
	if n < UpgradeSweepMinIntervalMinutes {
		return UpgradeSweepMinIntervalMinutes
	}
	if n > UpgradeSweepMaxIntervalMinutes {
		return UpgradeSweepMaxIntervalMinutes
	}
	return n
}

// NormalizeUpgradeSweepBatch 把「一轮几首」夹进 [1, UpgradeSweepMaxBatch]，<=0 取默认。
func NormalizeUpgradeSweepBatch(n int) int {
	if n <= 0 {
		return UpgradeSweepDefaultBatch
	}
	if n < 1 {
		return 1
	}
	if n > UpgradeSweepMaxBatch {
		return UpgradeSweepMaxBatch
	}
	return n
}

// UpgradeSweepOn 报告补强轮开没开。
//
// **nil（老配置）= 跟收藏自动下载同步** —— 这正是升级前的行为（补强轮的 deps 里第一条就是
// FavAutoDownloadOn），所以升级既不会给没开收藏下载的人悄悄起一个后台重活，也不会把已经在
// 补强的人停下来。用户在设置里显式开关过之后，就完全按他选的走（哪怕收藏自动下载是关的，
// 老曲库里的有损也照样该补）。
func (a AppConfig) UpgradeSweepOn() bool {
	if a.UpgradeSweepEnabled != nil {
		return *a.UpgradeSweepEnabled
	}
	return a.FavAutoDownloadOn()
}

// UpgradeSweepIgnoreCooldownOn 报告「无视冷却位」。nil = 关。
func (a AppConfig) UpgradeSweepIgnoreCooldownOn() bool {
	return a.UpgradeSweepIgnoreCooldown != nil && *a.UpgradeSweepIgnoreCooldown
}

// UpgradeSweepInterval 返回补强轮实际间隔。
func (a AppConfig) UpgradeSweepInterval() time.Duration {
	return time.Duration(NormalizeUpgradeSweepInterval(a.UpgradeSweepIntervalMinutes)) * time.Minute
}

// UpgradeSweepPerRound 返回补强轮一轮实际派几首。
func (a AppConfig) UpgradeSweepPerRound() int {
	return NormalizeUpgradeSweepBatch(a.UpgradeSweepBatch)
}

// InjectUIEnabled 是「页面注入」开关的读取口径：**没设置过就是开**。
//
// 关掉之后官方页面回到完全原样（拦截层不认领文档请求，由接管层反向代理透传），
// 用于「注入把官方页弄坏了，先退回来」这种时刻。
// MusicDLDefaults 是 sidecar 的默认值（三处共用，避免「默认值散在各处」）。
const (
	// MusicDLDefaultPort 是 sidecar 的回环端口。
	//
	// 选 8901 而不是紧挨主端口的 8899：8899 是**旧的「极光音乐」应用**在用的，
	// 两个应用并存时抢端口会很难查。
	MusicDLDefaultPort = 8901
	// MusicDLDefaultLimit 是单源返回条数（见 MusicDLLimit 的说明）。
	//
	// ⚠️ **别调大**：musicdl 在搜索阶段就会为每一条解析直链，条数直接决定耗时。
	// 真机实测（咪咕，同一关键词）：5 条 → 2.1 秒；8 条 → 3.2 秒；12 条 → 7.0 秒。
	// 而官方页面搜索合并只等 3 秒（见 pkg/intercept/search.go 的 onlineSearchWait）
	// —— 给到 15 条时外挂平台**永远赶不上那个窗口**，表现为「开了外挂音源却搜不到
	// 咪咕的歌」。5 条够填搜索结果的第一屏，且能稳稳落在窗口内。
	MusicDLDefaultLimit = 5
)

// MusicDLDefaultSources 是默认启用的平台。
//
// # 为什么只剩咪咕一个
//
// v2.1.87 真机实测（sidecar `/search` 单源，同一关键词）：
//
//	mg（咪咕）  2.1 秒 / 5 条    6.6 秒 / 15 条   ← 可用
//	bq（千千）  >12 秒 / **0 条**（超预算，连续多次都是 0）← 不可用
//	bi（B站）   >12 秒 / **0 条**（同上）                ← 不可用
//
// 千千与 B站 在真机上一次都没成功过。把它们留在默认里，代价是**每次搜索**都要为
// 它们空等（sidecar 侧两个线程白跑十几秒），而收益是零 —— 所以默认只留咪咕。
// 它们仍在可选列表里（用户那边网络或许能通），打开方式见 `musicdl_sources`。
//
// # 其余平台的取舍
//
// 咪咕曲率原生没有 → 纯增量。酷狗 / 酷我 原生「搜得到、放不出」，交给 musicdl
// 才能播，但那会把搜索也换掉（一个平台只能有一个 id 空间），属于改变现有行为 →
// 默认关。网易 / QQ 原生就是全的，走 sidecar 纯属重复 → 默认关。
var MusicDLDefaultSources = []string{"mg"}

// NormalizeMusicDLSources 清洗平台名单：去空白、转小写、去重、丢掉空元素。
//
// **不校验是不是已知平台**：认不认得出交给 sidecar（它会丢掉不认识的并如实
// 报告生效了哪些），这样上游加平台时这里不用跟着改。
func NormalizeMusicDLSources(list []string) []string {
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// FavAutoDownloadOn 报告「收藏自动下载并绑定本地」开没开（nil = 关）。
func (a AppConfig) FavAutoDownloadOn() bool {
	return a.FavAutoDownload != nil && *a.FavAutoDownload
}

// TeeOn 报告「边听边下」开没开（nil = 关）。
func (a AppConfig) TeeOn() bool {
	return a.TeeEnabled != nil && *a.TeeEnabled
}

// DailyLLMOn 报告「每日推荐里的大模型推荐层」开没开（nil = 关）。
//
// ⚠️ 它只回答「用户同意把收听历史发出去吗」，**不**回答「模型配好了吗」——
// 后者是 `ai.Config.Available()` 的事，由 `pkg/ai` 自己静默降级。
// 两处都判等于把同一条判断写两遍，迟早不一致。
func (a AppConfig) DailyLLMOn() bool {
	return a.DailyLLMEnabled != nil && *a.DailyLLMEnabled
}

// LXServerOn 是「服务端洛雪宿主」的总开关。缺省关 —— 它是新能力，且会破掉
// 「后端不执行第三方 JS」这条边界，必须用户显式打开。
func (a AppConfig) LXServerOn() bool {
	return a.LXServerEnabled != nil && *a.LXServerEnabled
}

// LyricAutoDownloadOn 报告「下载时自动带上歌词」开没开。
//
// ⚠️ **缺省是开**：这两个开关与上面几个相反 —— 老配置里没有它们（nil），
// 而「下下来的歌没歌词 / 没封面」是明显的退步，所以 nil 必须当作开。
func (a AppConfig) LyricAutoDownloadOn() bool {
	return a.LyricAutoDownload == nil || *a.LyricAutoDownload
}

// CoverEmbedOn 报告「下载时把封面内嵌进标签」开没开（缺省开，理由同上）。
func (a AppConfig) CoverEmbedOn() bool {
	return a.CoverEmbed == nil || *a.CoverEmbed
}

// ValidateDirs 检查「下载目录 / 缓存目录」这对路径是否合法。
//
// 两条规则（来自上游 zouclang/fnos_music_ext v2.8.0 的目录校验，那条 release 写得很明确）：
//
//	① 两者不得**相同**：缓存会被淘汰清理，与成品放同一个目录等于随时可能删掉成品；
//	② 两者不得**互为父子**：`/vol1/Music` 与 `/vol1/Music/.cache` 这种，清理缓存时
//	   一个 rm -rf 就能带走整个曲库，而且「下载完成 → 从缓存转正」会在同目录里
//	   自己搬自己，rename 的语义变得不可预测。
//
// 只在**两个都非空**时校验：缓存留空表示「用程序推导的默认值」，那个值本身
// 就是下载目录下的隐藏子目录（属于规则 ②）—— 默认配置不能把自己判成非法。
// 返回空串表示通过，否则是给用户看的中文原因（接口直接回给前端）。
func (a AppConfig) ValidateDirs() string {
	d, c := strings.TrimSpace(a.DownloadDir), a.CacheDirPath()
	if d == "" || c == "" {
		return ""
	}
	clean := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return filepath.Clean(p)
	}
	dc, cc := clean(d), clean(c)
	if dc == cc {
		return "缓存目录不能和下载目录是同一个目录（清理缓存会连成品一起删掉）"
	}
	if strings.HasPrefix(cc, dc+string(filepath.Separator)) {
		return "缓存目录不能放在下载目录里面（清理缓存会连整个曲库一起删掉）"
	}
	if strings.HasPrefix(dc, cc+string(filepath.Separator)) {
		return "下载目录不能放在缓存目录里面（缓存清理会牵连整个曲库）"
	}
	return ""
}

// 「下载音质」开关的三个取值。
const (
	DownloadQualityFollow   = ""         // 跟随各入口自己的设置
	DownloadQualityLossless = "lossless" // 一律点名无损
	DownloadQualityMP3      = "mp3_320"  // 一律点名 320k
)

// DownloadQualitySetting 返回生效的「下载音质」开关；nil / 空白 = 跟随。
func (a AppConfig) DownloadQualitySetting() string {
	if a.DownloadQualityOverride == nil {
		return DownloadQualityFollow
	}
	return strings.TrimSpace(*a.DownloadQualityOverride)
}

// QualityOverride 把「下载音质」开关套到某个入口自己的档位上。
//
// 认不出的覆盖值一律当「跟随」：宁可退化成各用各的，也不要因为一个错值
// 把所有下载都钉死在某一档。这个映射在网页端 services/qualityOverride.js
// 有一份镜像（那份是给下载队列用的），改这里请一起改。
func QualityOverride(base, override string) string {
	switch strings.TrimSpace(override) {
	case DownloadQualityLossless:
		// 点名无损。两个入口的取值表里都有 "flac" 这一档。
		return "flac"
	case DownloadQualityMP3:
		return "320k"
	default:
		return strings.TrimSpace(base)
	}
}

// CacheDirPath 返回生效的缓存目录；空串 = 用程序推导的默认值。
func (a AppConfig) CacheDirPath() string {
	if a.CacheDir == nil {
		return ""
	}
	return strings.TrimSpace(*a.CacheDir)
}

// MusicDLOn 报告 sidecar 开没开（nil = 关）。
func (a AppConfig) MusicDLOn() bool {
	return a.MusicDLEnabled != nil && *a.MusicDLEnabled
}

// MusicDLActiveSources 返回实际生效的平台名单（空 = 默认）。
func (a AppConfig) MusicDLActiveSources() []string {
	// ⚠️ 判 nil 而**不是** len==0 —— 两者含义完全不同：
	//
	//	nil = 从没配过（老配置 / 新装）→ 用默认源（当前 mg）
	//	[]  = 用户在音源管理页把源**全关掉了** → 就该一个平台都不搜
	//
	// 用 len==0 判会把「全关」悄悄变回「默认开 mg」：用户明明关了，搜索里还有咪咕的
	// 结果。这正是 v2.1.99 真机验收「音源页逐个启停生效」时抓到的现象。
	if a.MusicDLSources == nil {
		return append([]string(nil), MusicDLDefaultSources...)
	}
	return NormalizeMusicDLSources(a.MusicDLSources)
}

// MusicDLEffectiveLimit 返回实际生效的单源条数。
func (a AppConfig) MusicDLEffectiveLimit() int {
	if a.MusicDLLimit <= 0 {
		return MusicDLDefaultLimit
	}
	if a.MusicDLLimit > 50 {
		return 50
	}
	return a.MusicDLLimit
}

// MusicDLEffectivePort 返回实际生效的 sidecar 端口。
func (a AppConfig) MusicDLEffectivePort() int {
	if a.MusicDLPort <= 0 || a.MusicDLPort > 65535 {
		return MusicDLDefaultPort
	}
	return a.MusicDLPort
}

func (a AppConfig) InjectUIEnabled() bool {
	return a.UIInjectEnabled == nil || *a.UIInjectEnabled
}

// DefaultPlatformPriority 是用户没设置时的主力平台顺序。
var DefaultPlatformPriority = []string{"wy", "tx"}

// knownPlatforms 是「主力平台」这个配置项认得的平台。
//
// `mg` / `bq` / `bi` 曾被排除在外（旧注释是「目前无搜索实现，别放进来」）——
// v2.1.83 起它们由 musicdl sidecar 提供搜索与解析，**那个理由不再成立**。
// 现在放行：sidecar 没开时搜它们是空结果（如实），开了就有结果。
//
// 认不认得一个平台 id 只能有一个答案，所以这里是唯一的白名单。
var knownPlatforms = map[string]bool{
	"wy": true, "tx": true, "kg": true, "kw": true,
	"mg": true, "bq": true, "bi": true,
}

// PlatformPriority 归一化后的主力平台顺序：去掉未知 id 与重复项，空则回默认。
//
// 归一化放在这里而不是每个调用方各写一遍 —— 界面能传什么是开放的，
// 而「认不认识这个平台 id」只能有一个答案。
func (a AppConfig) PlatformPriority() []string {
	out := make([]string, 0, len(a.PreferredPlatforms))
	seen := map[string]bool{}
	for _, raw := range a.PreferredPlatforms {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" || !knownPlatforms[p] || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultPlatformPriority...)
	}
	return out
}

// OrderedPlatforms 把「主力在前、其余殿后」拼成一次搜索要用的平台顺序。
//
// 没在 priority 里的平台**不会被丢掉** —— 用户要的就是「qq/网易都没有时才用 kg/kw」，
// 所以它们仍在队列里，只是排后面。
func OrderedPlatforms(priority, fallback []string) []string {
	out := make([]string, 0, len(priority)+len(fallback))
	seen := map[string]bool{}
	for _, p := range append(append([]string{}, priority...), fallback...) {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

type ConfigManager struct {
	mu       sync.RWMutex
	filePath string
	config   AppConfig
	uiPrefs  *UIPrefsManager
	// onUpdate 是「保存之后」的回调（在锁外调用，避免回调里再读配置时死锁）。
	//
	// 用途：有些能力不是「读一下配置就行」，而是要在配置变化时**动起来** ——
	// 比如 musicdl sidecar 要按开关拉起 / 停掉进程。没有这个钩子，那些能力就只能
	// 「改完重启」，而用户明确的偏好是「开关点了就该立刻生效」。
	onUpdate []func(AppConfig)
}

// OnUpdate 注册一个配置保存后的回调。**在保存成功之后**调用。
//
// 回调必须自己保证幂等：同一次保存会把它调一次，而它可能被多次保存连着调。
func (cm *ConfigManager) OnUpdate(fn func(AppConfig)) {
	if fn == nil {
		return
	}
	cm.mu.Lock()
	cm.onUpdate = append(cm.onUpdate, fn)
	cm.mu.Unlock()
}

func NewConfigManager(dataDir string, defaultPort int) (*ConfigManager, error) {
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		dataDir = "/tmp/fn-lx-player-data"
		_ = os.MkdirAll(dataDir, 0755)
	}

	defaultDir := "/vol1/Music"
	if _, err := os.Stat("/vol1"); err != nil {
		defaultDir = filepath.Join(dataDir, "music")
		_ = os.MkdirAll(defaultDir, 0755)
	}

	cm := &ConfigManager{
		filePath: filepath.Join(dataDir, "config.json"),
		config: AppConfig{
			Port:                defaultPort,
			DefaultNasDir:       defaultDir,
			DownloadDir:         defaultDir,
			ActiveSourceID:      "",
			Theme:               "fresh-mint",
			VisualizerMode:      "bars",
			PreferQuality:       "320k",
			PlaybackQuality:     "auto",
			AutoDownloadQuality: "highest",
			CustomSources:       make([]CustomSource, 0),
		},
	}
	cm.uiPrefs = NewUIPrefsManager(filepath.Dir(cm.filePath))

	cm.load()
	return cm, nil
}

func (cm *ConfigManager) load() {
	data, err := os.ReadFile(cm.filePath)
	if err != nil {
		return
	}
	var c AppConfig
	if err := json.Unmarshal(data, &c); err == nil {
		if c.DefaultNasDir != "" {
			cm.config.DefaultNasDir = c.DefaultNasDir
		}
		if c.DownloadDir != "" {
			cm.config.DownloadDir = c.DownloadDir
		} else if cm.config.DefaultNasDir != "" {
			cm.config.DownloadDir = cm.config.DefaultNasDir
		}
		// 启用池：nil = 老配置（或从没设置过）→ 用旧的单值迁移成单元素池；
		// 非 nil（含空数组）都是用户的真实选择，原样读回。
		if c.ActiveSourceIDs != nil {
			cm.config.SetActiveIDs(c.ActiveSourceIDs)
		} else if c.ActiveSourceID != "" {
			cm.config.SetActiveIDs([]string{c.ActiveSourceID})
		}
		if c.Theme != "" {
			cm.config.Theme = c.Theme
		}
		if c.VisualizerMode != "" {
			cm.config.VisualizerMode = c.VisualizerMode
		}
		if c.PreferQuality != "" {
			cm.config.PreferQuality = c.PreferQuality
		}
		if c.PlaybackQuality != "" {
			cm.config.PlaybackQuality = c.PlaybackQuality
		}
		if c.AutoDownloadQuality != "" {
			cm.config.AutoDownloadQuality = c.AutoDownloadQuality
		}
		if c.AutoDownloadChannels != nil {
			cm.config.AutoDownloadChannels = NormalizeDownloadChannels(c.AutoDownloadChannels)
		}
		if c.DownloadQualityOverride != nil {
			v := strings.TrimSpace(*c.DownloadQualityOverride)
			cm.config.DownloadQualityOverride = &v
		}
		if c.CustomSources != nil {
			cm.config.CustomSources = c.CustomSources
		}
		// ⚠️ load 与 Update 是**两处独立的选择性合并**，新增字段两边都要登记，
		// 否则会出现「写进去了但读不回来」或「读得回来但写不进去」。
		// APISources 曾同时漏在这两处，表现为「接入音源成功、列表却是空」。
		if c.APISources != nil {
			cm.config.APISources = c.APISources
		}
		// ⚠️ `PreferredPlatforms` 曾经漏在这一处（Update 里有、load 里没有），
		// 表现为「界面上改了主力平台、能用，重启后自己变回默认」——
		// 因为 Update 写进的是内存 + 文件，而重启只走 load，漏登记就等于没存。
		if c.PreferredPlatforms != nil {
			cm.config.PreferredPlatforms = c.PreferredPlatforms
		}
		if c.ChartPlaylists != nil {
			cm.config.ChartPlaylists = NormalizeChartPlaylists(c.ChartPlaylists)
		}
		if c.ChartSize > 0 {
			cm.config.ChartSize = NormalizeChartSize(c.ChartSize)
		}
		if c.ChartSizes != nil {
			cm.config.ChartSizes = normalizeChartSizes(c.ChartSizes)
		}
		// 总开关：nil = 文件里没这个键（保持原值），有值才覆盖。
		// 存一份副本而不是直接存调用方的指针 —— 那个结构体（POST /api/config
		// 的 patch）活着的时间比配置短，留着指针等于把配置挂在临时对象上。
		if c.ChartPlaylistsEnabled != nil {
			v := *c.ChartPlaylistsEnabled
			cm.config.ChartPlaylistsEnabled = &v
		}
		// 显示首数：只有显式给了才覆盖，没给的键保持原值（Update 的语义）。
		if c.ChartSize > 0 {
			cm.config.ChartSize = NormalizeChartSize(c.ChartSize)
		}
		if c.ChartSizes != nil {
			cm.config.ChartSizes = normalizeChartSizes(c.ChartSizes)
		}
		// GitHub token（用户 2026-10-25）。⚠️ 老规矩：load 与 Update 两处都要登记。
		if c.UpdateGitHubToken != "" {
			cm.config.UpdateGitHubToken = strings.TrimSpace(c.UpdateGitHubToken)
		}
		// 应用内更新代理（用户 2026-10-25）。同上：load 与 Update 两处都要登记。
		if c.UpdateProxy != "" {
			cm.config.UpdateProxy = NormalizeUpdateProxy(c.UpdateProxy)
		}
		// 更新镜像清单（用户 2026-10-25）。同上：load 与 Update 两处都要登记。
		if c.UpdateMirrors != nil {
			list := NormalizeUpdateMirrors(*c.UpdateMirrors)
			cm.config.UpdateMirrors = &list
		}
		if c.UIInjectEnabled != nil {
			v := *c.UIInjectEnabled
			cm.config.UIInjectEnabled = &v
		}
		// 收藏自动下载（v2.1.92 新增）。⚠️ 与下面那条同样的规矩：load 与 Update
		// **两处都要登记** —— 只写一处就是「改完能用、重启变回默认」或「改了没生效」。
		if c.FavAutoDownload != nil {
			v := *c.FavAutoDownload
			cm.config.FavAutoDownload = &v
		}
		if c.TeeEnabled != nil {
			v := *c.TeeEnabled
			cm.config.TeeEnabled = &v
		}
		// 每日推荐的大模型层（v2.1.118 新增）。⚠️ 同样 load 与 Update 两处都要登记。
		if c.DailyLLMEnabled != nil {
			v := *c.DailyLLMEnabled
			cm.config.DailyLLMEnabled = &v
		}
		if c.LXServerEnabled != nil {
			v := *c.LXServerEnabled
			cm.config.LXServerEnabled = &v
		}
		if c.LXServerPort != 0 {
			cm.config.LXServerPort = c.LXServerPort
		}
		if c.LyricAutoDownload != nil {
			v := *c.LyricAutoDownload
			cm.config.LyricAutoDownload = &v
		}
		if c.CoverEmbed != nil {
			v := *c.CoverEmbed
			cm.config.CoverEmbed = &v
		}
		// 命名格式：**空 = 不覆盖**（空表示「没设置过」，认不出的值交给 pkg/namefmt 归一）
		if c.FilenameFormat != "" {
			cm.config.FilenameFormat = c.FilenameFormat
		}
		// 缓存目录是三态（nil = 不覆盖，指向空串 = 显式回到默认），不能写成 `!= ""`
		if c.CacheDir != nil {
			v := *c.CacheDir
			cm.config.CacheDir = &v
		}
		// musicdl sidecar（v2.1.83 新增）。
		// ⚠️ 与上面那条同样的规矩：**load 与 Update 两处都要登记**。只写 Update
		// 就是「界面上改了能用、重启后自己变回默认」；只写 load 就是「改了没生效」。
		if c.MusicDLEnabled != nil {
			v := *c.MusicDLEnabled
			cm.config.MusicDLEnabled = &v
		}
		if c.MusicDLSources != nil {
			cm.config.MusicDLSources = NormalizeMusicDLSources(c.MusicDLSources)
		}
		if c.MusicDLLimit > 0 {
			cm.config.MusicDLLimit = c.MusicDLLimit
		}
		if c.MusicDLPort > 0 {
			cm.config.MusicDLPort = c.MusicDLPort
		}
		// 允许清空（空串 = 回到自动找解释器），所以用赋值而不是判空跳过
		cm.config.MusicDLPython = c.MusicDLPython
		// FnosToken 允许清空（空串就是「回到自动读取」），所以不能像其它字段那样
		// 用 `!= ""` 判空跳过 —— 那样用户就永远清不掉手工令牌了。
		cm.config.FnosToken = c.FnosToken
	}
}

func (cm *ConfigManager) Get() AppConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

// DataDir 返回配置所在的数据目录（供其它模块存放自己的持久化文件）
func (cm *ConfigManager) DataDir() string {
	return filepath.Dir(cm.filePath)
}

// UIPrefs 返回窗口记忆管理器（各页面点击状态持久化到 ui_prefs.json）。
func (cm *ConfigManager) UIPrefs() *UIPrefsManager {
	return cm.uiPrefs
}

func (cm *ConfigManager) Update(c AppConfig) error {
	cm.mu.Lock()

	// 落盘成功后要通知订阅者，但**必须在解锁之后** —— 回调里通常要再读一次配置
	// （`cm.Get()`），而 Go 的 RWMutex 不可重入，持锁回调就是自己跟自己死锁
	// （这个坑真踩过：加完钩子之后 `pkg/config` 整包 60 秒超时）。
	//
	// defer 是 LIFO，所以这里只注册一个闭包：快照在锁内取，解锁与通知按顺序做完。
	saved := false
	defer func() {
		snapshot := cm.config
		subs := make([]func(AppConfig), len(cm.onUpdate))
		copy(subs, cm.onUpdate)
		cm.mu.Unlock()

		if !saved {
			return // 没落盘成功（校验/序列化失败）→ 不通知
		}
		for _, fn := range subs {
			func() {
				// 回调里的 panic 不能影响保存结果 —— 配置已经写进文件了，
				// 那才是这次调用的承诺；而且此刻函数正在返回，panic 会直接崩进程。
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[CONFIG] 配置变更回调 panic：%v", r)
					}
				}()
				fn(snapshot)
			}()
		}
	}()

	if c.DefaultNasDir != "" {
		cm.config.DefaultNasDir = c.DefaultNasDir
	}
	if c.DownloadDir != "" {
		cm.config.DownloadDir = c.DownloadDir
	}
	if c.ActiveSourceIDs != nil {
		cm.config.SetActiveIDs(c.ActiveSourceIDs)
	} else if c.ActiveSourceID != "" {
		// 旧调用方只给单值 → 语义就是"池里只有它"
		cm.config.SetActiveIDs([]string{c.ActiveSourceID})
	}
	if c.Theme != "" {
		cm.config.Theme = c.Theme
	}
	if c.VisualizerMode != "" {
		cm.config.VisualizerMode = c.VisualizerMode
	}
	if c.PreferQuality != "" {
		cm.config.PreferQuality = c.PreferQuality
	}
	if c.PlaybackQuality != "" {
		cm.config.PlaybackQuality = c.PlaybackQuality
	}
	if c.AutoDownloadQuality != "" {
		cm.config.AutoDownloadQuality = c.AutoDownloadQuality
	}
	if c.AutoDownloadChannels != nil {
		cm.config.AutoDownloadChannels = NormalizeDownloadChannels(c.AutoDownloadChannels)
	}
	if c.DownloadQualityOverride != nil {
		v := strings.TrimSpace(*c.DownloadQualityOverride)
		cm.config.DownloadQualityOverride = &v
	}
	if c.CustomSources != nil {
		cm.config.CustomSources = c.CustomSources
	}
	// 注意：Update 是**选择性合并** —— 每个字段都要在这里显式处理，
	// 否则调用方的修改会被静默丢弃（APISources 就曾漏在这里，表现为「保存成功但列表为空」）。
	// 用 nil 表示「不动」，空切片表示「清空」。
	if c.APISources != nil {
		cm.config.APISources = c.APISources
	}
	// 主力平台同理：漏在这里就会「界面改了但没生效」。nil = 不动，空切片 = 清空（回默认）。
	if c.PreferredPlatforms != nil {
		cm.config.PreferredPlatforms = c.PreferredPlatforms
	}
	// 榜单歌单名单：nil = 不动，空切片 = 一个榜单都不注入。
	// 归一化放在这里，界面传来什么（大小写、空格、重复项）都不会污染磁盘。
	if c.ChartPlaylists != nil {
		cm.config.ChartPlaylists = NormalizeChartPlaylists(c.ChartPlaylists)
	}
	// 总开关：nil = 不动（patch 里没这个键），有值才覆盖。名单不受它影响 ——
	// 关掉只是「先不注入」，勾过的榜还在。
	if c.ChartPlaylistsEnabled != nil {
		v := *c.ChartPlaylistsEnabled
		cm.config.ChartPlaylistsEnabled = &v
	}
	if c.ChartSizeOverride {
		// 整体覆盖：配多少就是多少，0 = 回到默认。这是「改回默认」唯一能生效的路径。
		cm.config.ChartSize = NormalizeChartSize(c.ChartSize)
		cm.config.ChartSizes = normalizeChartSizes(c.ChartSizes)
	} else {
		if c.ChartSize > 0 {
			cm.config.ChartSize = NormalizeChartSize(c.ChartSize)
		}
		if c.ChartSizes != nil {
			cm.config.ChartSizes = normalizeChartSizes(c.ChartSizes)
		}
	}
	// GitHub 个人 token：空串 = 不动（带 omitempty，没法区分「没传」），清空走 UpdateClearToken。
	if c.UpdateGitHubToken != "" {
		cm.config.UpdateGitHubToken = strings.TrimSpace(c.UpdateGitHubToken)
	}
	if c.UpdateClearToken {
		cm.config.UpdateGitHubToken = ""
	}
	// 应用内更新代理：空串 = 不动，清空走 UpdateClearProxy。
	// 存归一后的值（补 scheme），避免界面填 `192.168.1.3:7890` 时读写两端不一致。
	if c.UpdateProxy != "" {
		cm.config.UpdateProxy = NormalizeUpdateProxy(c.UpdateProxy)
	}
	if c.UpdateClearProxy {
		cm.config.UpdateProxy = ""
	}
	// 更新镜像清单：给键 = 整体替换（含给空数组 = 只用官方源）；恢复默认走
	// UpdateResetMirrors。两者都做了时以「恢复默认」为准 —— 界面上不可能同时
	// 点两个按钮，真同时发了也是「恢复默认」更安全（清单不会被清空）。
	if c.UpdateMirrors != nil {
		list := NormalizeUpdateMirrors(*c.UpdateMirrors)
		cm.config.UpdateMirrors = &list
	}
	if c.UpdateResetMirrors {
		cm.config.UpdateMirrors = nil
	}
	// 配了更新的限速 / token 之后，管理器要在下一次下载时用新值。这里不直接推进去：
	// 保存流程末尾统一走 OnUpdate 回调（见 api 层注册处），重启也走 load ——
	// 「一处修改、两处生效」的坑本项目踩过好几次。
	// 定时快照：间隔与保留份数是普通数值，直接夹区间落库（0 = 回到各自默认）。
	// 开关用指针判 nil —— 局部 patch 里没有这个键时不能把它关掉。
	if c.SnapshotScheduleEnabled != nil {
		v := *c.SnapshotScheduleEnabled
		cm.config.SnapshotScheduleEnabled = &v
	}
	if c.SnapshotIntervalHours > 0 {
		v := c.SnapshotIntervalHours
		if v < SnapshotMinIntervalHours {
			v = SnapshotMinIntervalHours
		}
		if v > SnapshotMaxIntervalHours {
			v = SnapshotMaxIntervalHours
		}
		cm.config.SnapshotIntervalHours = v
	}
	if c.SnapshotKeep > 0 {
		v := c.SnapshotKeep
		if v > SnapshotMaxKeep {
			v = SnapshotMaxKeep
		}
		cm.config.SnapshotKeep = v
	}
	// 后台音质补强：三个键都是「给了才改」。开关与「无视冷却」是指针，nil = 保持原样；
	// 两个数值 0 = 保持原样（要回默认就在界面上选默认值，与定时快照同一口径）。
	if c.UpgradeSweepEnabled != nil {
		v := *c.UpgradeSweepEnabled
		cm.config.UpgradeSweepEnabled = &v
	}
	if c.UpgradeSweepIgnoreCooldown != nil {
		v := *c.UpgradeSweepIgnoreCooldown
		cm.config.UpgradeSweepIgnoreCooldown = &v
	}
	if c.UpgradeSweepIntervalMinutes > 0 {
		cm.config.UpgradeSweepIntervalMinutes = NormalizeUpgradeSweepInterval(c.UpgradeSweepIntervalMinutes)
	}
	if c.UpgradeSweepBatch > 0 {
		cm.config.UpgradeSweepBatch = NormalizeUpgradeSweepBatch(c.UpgradeSweepBatch)
	}
	// 页面注入开关：nil = 不动。关掉 = 官方页面完全原样（拦截层不认领文档请求）。
	if c.UIInjectEnabled != nil {
		v := *c.UIInjectEnabled
		cm.config.UIInjectEnabled = &v
	}
	// 收藏自动下载：nil = 不动（patch 里没这个键就不能动它）。
	if c.FavAutoDownload != nil {
		v := *c.FavAutoDownload
		cm.config.FavAutoDownload = &v
	}
	if c.TeeEnabled != nil {
		v := *c.TeeEnabled
		cm.config.TeeEnabled = &v
	}
	// 每日推荐的大模型层：nil = 不动（patch 里没这个键就不能动它）。
	if c.DailyLLMEnabled != nil {
		v := *c.DailyLLMEnabled
		cm.config.DailyLLMEnabled = &v
	}
	if c.LXServerEnabled != nil {
		v := *c.LXServerEnabled
		cm.config.LXServerEnabled = &v
	}
	if c.LXServerPort != 0 {
		cm.config.LXServerPort = c.LXServerPort
	}
	if c.LyricAutoDownload != nil {
		v := *c.LyricAutoDownload
		cm.config.LyricAutoDownload = &v
	}
	if c.CoverEmbed != nil {
		v := *c.CoverEmbed
		cm.config.CoverEmbed = &v
	}
	// 命名格式：空 = 不改 —— 空在这里有明确语义（没设置过 → 用默认方向）
	if c.FilenameFormat != "" {
		cm.config.FilenameFormat = c.FilenameFormat
	}
	// 缓存目录是三态：nil = 不改，指向空串 = 显式回到默认（界面的「恢复默认」）
	if c.CacheDir != nil {
		v := *c.CacheDir
		cm.config.CacheDir = &v
	}
	// musicdl sidecar（v2.1.83 新增）：nil = 不动，空切片 = 回到默认平台名单。
	// ⚠️ 开关是 nil 三态：patch 里没这个键就不能动它（否则「保存别的设置」会把
	// 用户刚打开的 sidecar 关掉）。
	if c.MusicDLEnabled != nil {
		v := *c.MusicDLEnabled
		cm.config.MusicDLEnabled = &v
	}
	if c.MusicDLSources != nil {
		cm.config.MusicDLSources = NormalizeMusicDLSources(c.MusicDLSources)
	}
	if c.MusicDLLimit > 0 {
		cm.config.MusicDLLimit = c.MusicDLLimit
	}
	if c.MusicDLPort > 0 {
		cm.config.MusicDLPort = c.MusicDLPort
	}
	cm.config.MusicDLPython = c.MusicDLPython
	// FnosToken 允许清空（空串就是「回到自动读取」），所以不能像其它字段那样
	// 用 `!= ""` 判空跳过 —— 那样用户就永远清不掉手工令牌了。
	cm.config.FnosToken = c.FnosToken

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cm.filePath, data, 0644); err != nil {
		return err
	}
	saved = true // 只有落盘成功才通知订阅者（见函数开头那段 defer）
	return nil
}

// SnapshotsScheduled 是定时快照开关的读取口径：**没设置过就是关**。
func (a AppConfig) SnapshotsScheduled() bool {
	return a.SnapshotScheduleEnabled != nil && *a.SnapshotScheduleEnabled
}

// LibraryDir 返回**生效**的下载落盘目录（曲库目录）。
//
// 与 pkg/downloader 的 getDownloadDir 同一口径：DownloadDir 为空时回退
// DefaultNasDir，再为空回退 /vol1/Music，最后 Clean —— 下载写哪里，这里就报哪里。
func (a *AppConfig) LibraryDir() string {
	if a == nil {
		return "/vol1/Music"
	}
	dir := a.DownloadDir
	if dir == "" {
		dir = a.DefaultNasDir
	}
	if dir == "" {
		dir = "/vol1/Music"
	}
	return filepath.Clean(dir)
}
