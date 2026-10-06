# 设计文档：一键订阅 + 服务型音源后端自动下载（v2.1.11 已实施）

> 状态：**已实施并打包交付（v2.1.11，2026-09-21 晚；后续修复见下）**。
> 实现记录见 `HANDOVER.md` §4.1（v2.1.11 起补上第三段：后端取链）与 §11 ㉓。
>
> - ✅ **内网音源已支持**（v2.1.12）：`pkg/apisource` 自己放开内网/回环访问，**不需要任何环境变量**。
> - ✅ **「停止更新」不再弄丢自动下载**（v2.1.13）：`pkg/monitor` 的 PATCH 改成真局部更新；
>   且「恢复更新」会重新断言订阅该有的配置，老数据可自愈。见 `HANDOVER.md` §11 ㉕。
> - ✅ **榜单详情也能订阅**（v2.1.14）：原先榜单只有「一键监控」，而它不绑定来源、追不到新歌；
>   现在榜单与精选歌单走同一条订阅路径。搜索结果仍不提供（没有稳定身份）。见 §11 ㉖。
> - ✅ **裸 ID 订阅不再永久失败**（v2.1.15）：精选歌单/榜单只有 id、没有分享链接，
>   而后端「按 ID 抓」要求已登录账号 → 没连账号的用户订阅后永远等不到新歌。
>   现在只拿到 id 时会先由 `charts.BuildPlaylistLink` 拼成标准链接，走免登录公开通道。见 §11 ㉗。
> - ✅ **QQ 歌单接口失效已修**（v2.1.15）：QQ 换掉了公开歌单接口（旧接口对匿名请求返回
>   `check privacy error!`），导致歌单详情 500 + QQ 歌单链接订阅静默失效 + 按账号抓 QQ 歌单失效。
>   三处统一到 `charts.FetchQQPlaylistDetail`（新接口 `v8/fcg-bin/fcg_v8_playlist_cp.fcg`，
>   一次返回全部曲目，不截断）。见 §11 ㉘。
> - ⚠️ **仍未验证**：飞牛真机自动下载闭环（需用户提供可用的服务型音源地址）；
>   `pkg/account` 的 QQ **已登录**歌单路径（需真实 QQ 账号）。
> - 接手者：先读本文档，再读 `HANDOVER.md` §4.1（监控双通道）与 §11 ㉑㉒㉓㉔㉕㉖㉗㉘。
> - 项目背景：曲率（yinshu-ai），飞牛 NAS 上的 Go + Vue3 音乐应用，无 git，改动记录全在 HANDOVER.md。

---

## 一、用户需求（原话归纳，白话）

用户看到参考项目 fnmusic-flow 1.0.4（全自动服务端流水线）后，提出：

1. **自动补下载最好**：订阅的歌单有新歌，后台自动下载到 NAS，不要每次开网页手点。
2. **一键订阅**：在主页（发现音乐）看到想听的歌单，点一下就完成订阅，不要再填表单建任务。
3. **可以停止**：有「停止更新」按钮，随时不追了。
4. **同时推飞牛**：歌单也要自动同步到飞牛音乐 App 里能看能播。

已确认的范围：**只接监控链**（推送链靠「监控下载的文件进了曲库 → 飞牛扫库 → 下一轮推送自然匹配」间接受益，不给推送任务单独做后端下载）。

## 二、已经做了什么（截至 v2.1.10，全部真机/包内验证过）

### 2.1 监控的「发现」已后端化（v2.1.10，见 HANDOVER §4.1 / §11 ㉑）

- `pkg/api/monitor_backend_discover.go`：歌单/收藏类监控由后端用**已登录账号**自抓
  （`push.FetchSource`），公开链接可匿名抓（`push.FetchSourceByLink`）；榜单类仍走浏览器上报。
- `pkg/monitor/pipeline.go` BuildPlan 规则：**无直链的曲目登记为 pending（不占下载预算）**；
  `downloaded 且无 file_path` 信任跳过；全部来源抓失败必须返回 error（防止误完成基线）。
- 网页端兜底：任务卡「补下载」按钮 → 逐首搜源入 downloadManager → 成功后
  `POST /api/monitor/tracks/mark` 回写 downloaded。
- 交付：`yinshu-ai-2.1.10.fpk` 6,290,194 字节，md5 `695324f6c37a02a042fa82dfeb40c3ef`。
  **待用户真机验证**（升级后点原监控的「立即执行」+「补下载」）。

### 2.2 服务型音源接入**早已建成，只差最后接线**

| 部件 | 位置 | 状态 |
|---|---|---|
| 三协议取直链 | `pkg/apisource/apisource.go` → `Resolve(protocol, baseURL, token, platform, songID, quality) (url, error)` | 已上线。lx_script（GET /url, X-API-Key）/ lx_server（GET /api/music/url, x-user-token）/ lx_api（POST /music/url）。含 SSRF 守卫（pkg/security，内网需 FN_ALLOW_PRIVATE_NET=1） |
| 协议自动探测 | `apisource.Detect(baseURL, token)` | 已有 |
| 配置存储 | `config.ConfigManager.Get().APISources`（字段：ID/Name/BaseURL/Token/Protocol/Enabled） | 已有，token 不经接口回显 |
| HTTP 层 | `pkg/api/apisource.go`：GET/POST/DELETE `/api/sources/api`、`/api/sources/api/detect`、`/api/sources/api/resolve` | 已有 |
| 前端 UI | `SourceManager.vue`「按服务地址接入」区块；`lx-runtime.js` 也把服务型音源注册给浏览器端搜索 | 已有 |
| 后端下载落盘 | `pkg/downloader` `DownloadOne(SongPayload{URL,...})`：断点续传/魔数校验/歌词内嵌 | 已有，monitor 的 Download 已走它 |
| 飞牛扫库 | `fnos.ScheduleLibraryRescan()`（合并+节流），**downloader 落盘成功后自动触发**（downloader.go:526） | 已有，不用额外接线 |
| 推送任务支持链接订阅 | `pkg/push/store.go` Task.SourceKind='link'（source_id 存链接，不依赖账号登录）；Runner 定时执行 | 已有 |

**结论：B 方案的缺口只有一段**——监控决策层不知道「后端能取链」，拿到无直链曲目一律登记 pending。

### 2.3 用户手里有可用的音源服务地址（已确认），真机可验证。

## 三、已批准的设计

### 3.1 核心接线：监控决策层新增「取链」动作（方案一）

**原则：monitor 包保持零第三方依赖，能力用函数注入。**

1. `pkg/monitor` 新增可选能力接口（http.go Dispatcher 旁）：

```go
// URLResolver 可选能力：声明「我能替无直链曲目解析下载地址」
type URLResolver interface { ResolveURL(song Song) (string, error) }
```

2. `BuildPlan(...)` 增加参数 `canResolve bool`：
   - 有直链 → download（不变）
   - 无直链 + AutoDownload + canResolve → **新 action "resolve"（占下载预算）**
   - 无直链 + 不可 resolve → register pending（v2.1.10 行为，不变）
   - 基线轮依旧只 register（**不得**在基线轮对几百首歌打音源服务）

3. `RunOnce`（http.go）新增 case "resolve"：
   - 断言 `h.disp.(URLResolver)` 成功 → `url, err := ResolveURL(item.Song)`
   - 成功：`item.Song.URL = url` → 走既有 download 分支（downloader 落盘，自动触发飞牛扫库）
   - 失败：`UpsertTrack(StatusPending, Error="后端取链失败：…（待网页端补下载）")`，
     **不计入 run.Failed**（音源偶发失败不应把整轮标红），run.Message 里记一笔。
   - 单轮不重试，下一轮监控自动再试（与监控节奏匹配，不引入退避状态机）。

4. `pkg/api/monitor_dispatcher.go` 实现 `ResolveURL`：
   - 数据源：`s.cfgMgr.Get().APISources`，过滤 Enabled，按顺序尝试，**第一个成功即用**（简单故障切换）。
   - 调 `apisource.Resolve(src.Protocol, src.BaseURL, src.Token, song.Source, song.ID, quality)`。
     song.Source 已是 wy/tx/kg/kw，apisource.mapPlatform 全支持。
   - **音质映射**（monitor.Quality → 服务端 quality 字符串）：
     standard→128k，high→320k，lossless→flac，hires→flac（服务若确认支持 hires 再放开）。
     `mon.Fallback == best_effort` 时降级链 目标 → 320k → 128k 逐档试；skip 时只试目标档。
     注意：各服务对 hires 的拼写未实测，属真机验证点。
   - **测试桩**：仿照同仓库 `pkg/api/monitor_backend_discover.go` 的 bd* 包级变量模式，把
     「枚举 APISources + 调 Resolve」收进一个包级函数变量，离线测试可替换。

5. RunOnce 里 BuildPlan 的调用点传入 canResolve：由 dispatcher 断言 URLResolver 得出。

### 3.2 一键订阅（前端为主，无新后端系统）

**一个「订阅」= 同时创建 1 个监控任务 + 1 个推送任务（facade，不合并后端）。**

- **入口与状态机**（`SearchView.vue` 歌单视图，现「一键监控」按钮位置升级；`PlaylistDownload.vue`
  的「订阅更新」同步升级为同一逻辑）：
  - 未订阅 → 按钮「订阅更新」：点击后
    1. `MonitorAPI.create({ kind:'playlist', target:{playlists:[{link, name}]}, auto_download:true, quality:'lossless', interval_minutes:360, enabled:true, name:歌单名 })`
    2. `PushAPI.create({... source_kind:'link', source_id:链接, name:歌单名, enabled:true, interval_minutes:360 ...})`（字段形状以 `pkg/push/store.go` 的 Task 结构与 `PushManager.vue` 现有 create 为准）
    3. 若飞牛未配置（fnos 不可用）：只建监控，提示「未配置飞牛，本次只自动下载不推送」。
    4. 首轮基线只登记不下载（既有机制），推送任务首轮把已在曲库的歌推上飞牛。
  - 已订阅（监控与推送都 enabled）→ 按钮变「已订阅」，点开可「**停止更新**」：
    两个任务一起 enabled=false（**保留记录与历史**；已下载文件、已推的飞牛歌单都保留）。
    角标显示「已暂停」，再点「恢复更新」= 双双 enabled=true。
  - 「删除订阅」不放进这个按钮（曲库管家/推送页已有删除，避免误触级联删曲目记录）。
- **已订阅判定**：前端拿监控列表 + 推送列表，按**归一化链接**匹配（去 query 参数、去尾斜杠后
  比较；实施时抽一个 normalizePlaylistLink() 小函数，网易/QQ 各一条规则，别过度设计）。
- `LibraryManager.vue` 任务卡在 v2.1.10 的「补下载」按钮继续保留（兜底通道不拆）。

### 3.3 用户视角的完整闭环（验收即按此走）

```
点「订阅更新」→ 首轮登记基线 + 已在曲库的歌推上飞牛
每 6 小时 → 后端拉歌单 → 新歌 → 服务型音源取链 → 自动下载 → 飞牛自动扫库
         → 下一轮推送把新歌补进飞牛歌单
取链失败的歌 → pending → 下一轮重试；网页开着时可手动「补下载」
点「停止更新」→ 全部停，已有成果保留
```

## 四、实施清单（建议顺序，即实施计划的骨架）

1. `pkg/monitor`：URLResolver 接口 + BuildPlan 加 canResolve + "resolve" action + RunOnce 分支（TDD：`plan_backend_test.go` 追加用例——canResolve=false 行为回归、=true 产出 resolve、基线轮不打点）
2. `pkg/api/monitor_dispatcher.go`：ResolveURL 实现 + 音质映射 + 包级桩；测试（enabled 过滤、failover 顺序、取链失败不标 failed）
3. 手动触发链路联调：`/api/monitors/{id}/run` 一轮内出现 downloaded 记录且文件落盘
4. 前端：normalizePlaylistLink() + SearchView/PlaylistDownload 订阅按钮状态机 + 飞牛未配置时的降级提示
5. `SourceManager.vue` 服务型音源卡片加一行说明：「已启用的服务型音源会被监控用于自动下载」
6. 版本 2.1.11：CurrentVersion、fpk-package/manifest、RELEASE_NOTES、HANDOVER §4.1/§11
7. 全量 `go test ./...` + 打包（`bash .dev/wsl-run.sh fpk`）+ 包内符号校验 + 交付桌面

## 五、坑位提醒（接手前必读）

- **基线轮绝不能调音源服务**（几百首一次性打爆服务端）——BuildPlan 的 resolve 只发生在非基线轮。
- 下载目录必须在飞牛音乐曲库目录（或其子目录）内，否则推送永远匹配不上——监控下载走
  downloader 的既有落盘目录，别另设路径。
- `apisource.Resolve` 的内网访问需要 FN_ALLOW_PRIVATE_NET=1；用户音源服务多部署在本机/内网，
  fpk 环境变量在哪配置要先查（与飞牛集成的既有开关同源）。
- token 永不经接口回显（既有 apiSourceView 已处理，新增代码别破坏）。
- `wsl.exe -d Debian -- bash -c` 会吞 shell 变量/heredoc：写脚本文件或用 printf 管道。
- 无 git：改前备份到 `.dev/backup-*/`；GOCACHE/GOPATH 用 `.tools/`，GOPROXY=goproxy.cn。
- 用户偏好：白话沟通；计划批准后自主连续执行不逐步确认；交付 fpk 到 Windows 桌面（/mnt/c/Users/liwenyao/Desktop）。
- 每轮改完立刻更新 HANDOVER.md 正文 + §11（项目铁规）。

## 六、真机验收清单

- 音源服务地址填入「音源管理 → 按服务地址接入」，探测出协议、测试取链成功
- 主页歌单点「订阅更新」→ 曲库管家出现监控 + 推送两条任务
- 首轮基线完成，无下载风暴
- 第二轮（或手动「立即执行」）新增曲目自动出现 downloaded 记录、文件落在曲库目录
- 飞牛音乐 App 里歌单出现/更新
- 「停止更新」后不再执行，恢复后继续
- 断开音源服务跑一轮：该轮歌曲变 pending 而非 failed，恢复服务后下一轮自动补上

（文档完）
