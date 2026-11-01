# 下载链路审查报告（v2.24.18 → 本批次）

审查时间：2026-11-01　范围：远端仓库 `/home/liwenyao/projects/music-v2` 工作区（HEAD 0779561）

---

## 一、审查范围

### 1. 后端取音计划（后端队列 = 后台下载的唯一实现）

| 文件 | 关注点 |
| --- | --- |
| `backend/pkg/backenddl/fetch.go` | `planAttempts` 渠道探测、`sortAttempts` 打分排序、`downloadOne` 落盘、`fallbackLower`/`corruptFallback` 两条降级兜底 |
| `backend/pkg/backenddl/queue.go` | 任务状态机、串行执行、终态日志、自动重试次数 |
| `backend/pkg/online/tier.go` | 音质档位定义 `TierLossless/320k/192k/128k`、`TierRank`、`PlaybackLadder`、错误分类 `ErrTierUnavailable`/`ErrNotPlayable` |
| `backend/pkg/online/resolve.go` | `Pool.ResolveLadder`（按档阶梯）、`Pool.ResolveTier`、`ResolveBypassingLadderCache`（绕缓存兜底）、正负缓存 |
| `backend/pkg/online/musicdl.go` | 外挂池 musicdl 的搜索 / 解析 / 是否支持按档位解析 |
| `backend/pkg/downloader/downloader.go` | 真正落盘：直链下载、魔数嗅探、FLAC 逐帧 CRC 校验、打标签嵌封面 |
| `backend/pkg/intercept/autodownload.go`、`lxresolve.go` | 另一套并行的取音计划（收藏自动下载 / 注入页） |
| `backend/pkg/api/backend_download.go`、`monitor_dispatcher.go`、`monitor/http.go` | 入队校验、订阅下载调度 |

### 2. 前端下载与提示

| 文件 | 关注点 |
| --- | --- |
| `frontend/src/services/downloadManager.js` | 后端队列镜像、占位任务、终态记账、未确认提交的持久化 |
| `frontend/src/services/backendDownload.js` | 队列轮询、可用性探测 |
| `frontend/src/services/appLog.js` | 操作日志环形缓冲 + 上报后端 |
| `frontend/src/services/feedback.js` + `components/FeedbackHost.vue` | 全站瞬时提示 |
| `frontend/src/style.css` | 提示条配色变量 |

### 3. 与下载相关的周边

- `backend/pkg/nas/scanner.go`（本地曲库扫描、封面接口 `/api/nas/cover`）
- `frontend/src/services/nasView.js` + `components/NasExplorer.vue`（封面地址补前缀与渲染）
- `backend/pkg/api/middleware.go`（内网免 Token 提示）
- `frontend/src/components/SourceManager.vue`（音源管理 · 服务端洛雪宿主卡）
- `frontend/src/components/PushManager.vue`（订阅更新的下载 / 同步入口）

### 4. 实证材料

- 运行日志 `…/.dev/data/logs/qulv.log`（647 KB）按分钟聚簇统计
- 后端队列快照 `GET /api/download/backend/queue`：214 条（success 199 / failed 10 / stopped 3 / paused 2）
- 后端应用内日志 `GET /api/logs?limit=2000`：237 条

---

## 二、发现的问题

### P0-1　下载失败日志在每次打开页面时整批重放（用户说的「没在下载却一直报下载失败」）

**现象**：日志页里「[下载失败]《某某》：没有拿到任何可用直链……」反复出现，而队列里并没有任何 `downloading` 任务。

**证据**：`qulv.log` 中前端 `[下载失败]` 共 **140 行**，按分钟聚簇为 7:46→60 条、10:33→10、10:39→10、10:49→10、10:50→20、10:54→10、10:56→10、11:52→10 —— 每一簇正好等于队列里历史 failed 任务数，且时间点全是页面加载时刻。

**成因**：`downloadManager.js` 的终态记账 `noteFinalState()` 用一个**纯内存** `Set`（`finalizedLog`）做去重。它的语义是对的（同一个任务只记一次），但 Set 的生命周期 = 一个页面实例：刷新/重开页面后 Set 清空，随后 `syncTasks()` 把后端队列里**所有历史 failed 任务**重新走一遍 `mapBackendItem → noteFinalState`，于是每一条旧失败都被当成「刚刚发生」再记一次，并上报到后端日志（`POST /api/logs`），混在同一时间轴上，看起来就像「一直在下载、一直在失败」。

### P0-2　「最高音质失败自动切下一档」的覆盖面不全

**现状**（已是正确行为的：原生的「缺档兜底」和「坏流降 320k」）：

- 原生渠道：`ResolveLadder` 问完 `flac → 320k → 128k`；全灭且错误是「平台没有这一档」时，`fallbackLower` 再绕缓存裸问一次「平台自己挑一档」→ 拿到 ≤ 点名的档就用。
- 任何渠道下到一条**坏流无损**（FLAC 帧 CRC 不过）：`corruptFallback` 重解析 320k 再下一次。

**缺口**：

1. **洛雪宿主渠道完全没有降级兜底**。`planAttempts` 的 `lx` 分支只问一次 `tiers[0]`（点名的最高档），拿到的候选按 `tierFromSize` 反推档位后直接入列；一旦脚本对无损档答不出来，这个渠道当场放弃，不会用 320k 再问它一遍。用户「选了最高音质、洛雪能下 320k 但下不了无损」的曲子会直接失败。
2. **`fallbackLower` 对「曲目不可播」过于保守**。它只在 `errors.Is(ladderErr, online.ErrTierUnavailable)` 时继续；只要阶梯最后留给调用方的错误是 `ErrNotPlayable`（例如 QQ 对匿名问某一档回 `result=104003` 被归成不可播、musicdl 只有 id 没有歌名时统一回 `ErrNotPlayable`），兜底就被直接跳过 —— 而这正是「平台其实还有一档，我们却没再去问」的典型场景。
3. **musicdl 渠道不存在「按档位解析」**。`MusicDLResolver` 没实现 `TierResolver`，`Pool.ResolveTier` 会退化成裸 `Resolve`（sidecar 返回什么档就是什么档），于是 `tiers` 对它是无效的：选「最高音质」并不能让它去要无损。
4. **`corruptFallback` 一轮只降一次**（`downgraded` 标志），且降完 320k 若仍坏流就放弃，不会再试 128k。

### P1-3　下载失败原因链落盘但用户看不到可行动的信息

队列的 `Item.Error` 只存 `err.Error()`（`fetch.go` 的 `failures` 拼接，形如「native/lx/flac：下载失败：{…}」），而「**为什么**降不下去」的判断过程（走没走兜底、兜底为什么放弃）只写进 `[BACKEND-DL]` 日志行，界面上没有对应的说明。用户看到 `tries=6`、`quality=flac` 却无从判断是「平台没无损」还是「坏流」。

### P1-4　本地曲库封面不显示（开发机口径）

- 后端扫描给的封面地址 `fmt.Sprintf("/api/nas/cover?path=%s", f.Path)` **没有 URL 转义**：路径里带空格、`#`、`?`、`&` 时会截断或污染查询串 → 接口 404/403 → 界面掉回 💿。
- 封面接口对同一路径 24 小时强缓存（`Cache-Control: public, max-age=86400`），换了封面当天看不到变化。
- 开发机当下的真实原因：11:04 那次后端是**裸启动**（`./fn-dev-backend -port 8900 …`，进程环境里没有任何 `FN_*`），没带 `FN_ALLOWED_ROOTS` → `/api/nas/*` 全部 403「路径不在允许访问的目录范围内」→ 曲库空白、封面全没。这不是代码缺陷，是启动方式；必须走 `bash .dev/run-dev-backend.sh`。

### P1-5　订阅「下载到本地 / 同步」后的提示条白底白字

`style.css` 把 `--ys-toast-bg` 改成了 `#ffffff`（注释写明「白底 + 深色字」），但 `FeedbackHost.vue` 的按钮文字仍是 `text-white`、边框仍是 `border-white/10` → **白底白字**，任何 ok/warn/fail 提示都看不见文字。`SelectionBar.vue` 的浮条同样用这个底色 + `text-white/70`，一起坏。

### P2-6　内网免 Token 提示持续发送

`middleware.go` 在 `AuthModeLAN` 分支里对每个新出现的非本机 IP 记一条 info（由 `ipNotifier` 限每分钟一条）。用户明确说无用，要取消。

### P2-7　音源管理 · 洛雪宿主卡信息不完整

卡片右上角塞了开关按钮，与旁边官方协议卡（名称 + 徽标）排版不一致；也没有告诉用户「下方列表里已经开了几个源」。

---

## 三、结论

1. **下载主链路（渠道探测 → 按实际档位排序 → 逐条落盘 → 坏流降档）设计是对的**，但有三处实质缺陷让「最高音质失败后自动切下一档」在实际使用中几乎走不到：
   - **洛雪渠道没有逐档询问**：只把最高档交给脚本，拿不到就整条渠道作废；
   - **坏流降档只降一次、且只在原曲平台重解析**：musicdl 给的坏 flac 想降 320k 时，重解析却退回 QQ（匿名恒不可播）→ 必然全灭；
   - **探测串行且无单源超时**：20 个 musicdl 源逐个搜，实测提交到出计划要 **2 分 11 秒**，期间界面只有「开始下载」一行，用户观感就是「卡住/没按预期执行」。
   另外 musicdl 解析器没实现按档位解析（`TierResolver`），选「最高音质」对它只是「平台给什么拿什么」—— 这是「选了最高却拿到 128k」的结构性原因，本批次未改动解析协议（风险大），已在报告中标为待办。
2. **「一直在报下载失败」不是下载在跑**，而是前端终态记账的去重只活在内存里，每次打开页面把历史失败整批重记一遍 —— 修复点是**把去重持久化到 localStorage**，并在首次见到「本页加载前就已终态」的任务时不记账。
3. 其余 5 项都是明确的界面/提示缺陷，根因单一、改动小：封面地址转义 + 缓存降级、提示条文字色、内网提示删除、洛雪卡排版、绿框标题删除。

---

## 四、本批次修复清单

| # | 问题 | 改动 |
| --- | --- | --- |
| 1 | 失败日志每次开页面整批重放 | `downloadManager.js`：`finalizedLog` 持久化到 `localStorage['fn_download_final_logged_v1']`（上限 800 条，先入先出）；以页面实例创建时刻为水位线，首轮同步时已终态且 `updatedAt` 早于水位线的历史任务不记账 |
| 2 | 洛雪渠道缺档不降级 | `fetch.go`：新增 `collectLx` 逐档向宿主询问（`lossless → 320k → 128k`），`looseTakeLx` 放宽到「不高于点名档」；`top_only` 模式仍只问最高档 |
| 3 | 坏流降档走死路 | `fetch.go`：新增 `reResolveForLower` —— 原曲平台无解时改问**这条候选自己的 musicdl 源**；新增 `bestLowerRemaining` 优先复用计划里**档位最高的**低档候选（避免 320k 被 128k 顶掉）；回读档位高于目标时按实际档位继续而不是放弃 |
| 4 | 坏流只降一次 | `fetch.go`：`corruptLowerTiers` 按 `320k → 128k` 逐级重试 |
| 5 | 探测串行、无单源预算 | `fetch.go`：musicdl 各源改为**并发**探测（`probeOneMusicDLSource` + `WaitGroup`），按用户源顺序收结果。实测提交→出计划 **2m11s → 20s**，整单耗时 2m11s → 33s |
| 6 | 降档后来源标错 | `fetch.go`：`reResolveForLower` 回传实际来源，队列不再把「mitu 给的 128k」记成「来源 tx」 |
| 7 | 渠道可用性不裁剪 | `config/channels.go` 新增 `ChannelAvailability` + `EffectiveDownloadChannels`；`fetch.go` 新增 `capItemTier`（用户点 320k 就不要无损）、`probeAllChannels`（计划为空时跨渠道兜底重探） |
| 8 | FLAC 校验误判边界 | `flacok`：新增「已进入帧内 → 读失败 = 截断（坏流）」「剩余不足一个最短帧头（6 字节）→ 补齐位、正常结束」两条判定；新增 `TestTailPaddingIsCleanEnd` / `TestTruncatedInsideLastFrameIsCaught` |
| 9 | 封面地址未转义 / 缓存过长 / 无封面也发请求 | `scanner.go`：`url.QueryEscape`；`ETag` + 缓存 24h→1h；新增 `hasEmbeddedCover` / `folderCoverExists` / `fileHasTrailingCover`，无封面直接给空地址（不再 404 刷屏） |
| 10 | 下载没带封面 | `fetch.go`：`coverFromSearch` —— 提交时没带 cover 就回搜一次补上，拿不到不阻塞下载 |
| 11 | 提示条白底白字 | `FeedbackHost.vue` / `SelectionBar.vue`：文字与边框改用主题变量 |
| 12 | 内网免 Token 提示持续刷 | `middleware.go`：删除该 info 日志（拒绝请求的 warn 保留） |
| 13 | 洛雪宿主卡 | `SourceManager.vue`：加「下方列表已开启 N 个」，开关移到卡片最下方与官方协议按钮同排同尺寸 |
| 14 | 绿框标题 | `PushManager.vue` / `SearchView.vue`：删除「这个订阅要做什么」 |
| 15 | `stopQualityPolling is not defined` | `TidyView.vue`：删掉对已迁走函数（现属 `QualityView.vue`）的调用 |

## 五、本批次未做（需要单独排期）

1. **musicdl 按档位解析**：`MusicDLResolver` 未实现 `TierResolver`，`ResolveLadder` 对它退化成裸解析 —— 选「最高音质」时它给什么拿什么。要做需要在 sidecar 侧支持按档请求，属协议变更。
2. **单源超时**：musicdl 搜索整体预算 135s（为兼容最慢的源），并发化以后整体受最慢源拖累的问题已缓解，但没有逐源硬超时。
3. **失败原因上界面**：队列 `Item.Error` 仍是技术原因串，未做「平台无无损 / 文件损坏 / 限流」的用户可读归类。
