<div align="center">

# 🎵 曲率 (AI Music Hub) · yinshu-ai

> 专为 **飞牛私有云 OS (fnOS)** 打造的 AI 音乐中枢：在线播放、订阅追更、批量下载、曲库整理、
> 官方 App 深度接管 —— 曲库的整条流水线，一个应用做完。

> **v2.0.0 起已更名**：原「极光音乐」(`fn-lx-player`) → **曲率** (`yinshu-ai`)。
> `appname` 不同，因此飞牛会把它当成**另一个应用**安装，**不会覆盖**旧版，两者可并存
> （新应用端口 8898，旧应用 8899）。数据目录独立，历史数据不会自动迁移。

[![Platform](https://img.shields.io/badge/Platform-fnOS%20%28x86__64%29-3b82f6?logo=linux)](https://developer.fnnas.com/)
[![Vue 3](https://img.shields.io/badge/Frontend-Vue%203%20%2B%20Vite-42b883?logo=vuedotjs)](https://vuejs.org/)
[![Go](https://img.shields.io/badge/Backend-Go%201.22+-00add8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-amber.svg)](LICENSE)

[📦 快速下载 FPK](https://github.com/webspider7/fnmusic/releases/latest) • [✨ 功能清单](#-功能清单) • [🧭 架构一页话](#-架构一页话) • [📥 安装](#-安装-fpk-手动安装) • [🛠️ 开发](#%EF%B8%8F-开发)

</div>

---

> **本 README 的数字都是现场数出来的**（数数口径见各节脚注；复核命令见 [开发](#%EF%B8%8F-开发) 一节）。
> 版本与逐版变更见 [`RELEASE_NOTES.md`](RELEASE_NOTES.md) 与 [`docs/变更记录/`](docs/变更记录/INDEX.md)。

## ✨ 功能清单

前端实际有的页面（顶栏导航：**发现音乐 / 本地音乐 / 曲库管家 / 账号连接 / 日志**，
右上角另有「音源管理」胶囊与「打开播放器」按钮）：

### 🔍 发现音乐（搜索 · 榜单 · 歌单）
- **全网聚合搜索**：多平台搜索去噪（`GET /api/search`）；按音源启用池挑源取链。
- **排行榜单 / 精选歌单**：官方协议（后端内置，免脚本）浏览网易云与 QQ 音乐的榜单与分类歌单；
  点开看曲目、直接播、可「订阅更新」。
- **榜单注入**：勾选的榜单会以**在线歌单**（虚拟歌单，只读）出现在飞牛音乐 App 里 ——
  卡片右上角「+ 注入」就地切换；批量勾选与总开关在 **曲库管家 → 榜单管理**。
- **双平台推荐卡**：已登录账号的每日推荐与歌单，点一下载入列表即可播。
- **页面注入入口**：飞牛官方音乐页里的「下载到曲率」等补的入口（总开关在曲库管家 → 榜单管理页）。

### 🗂️ 曲库管家（一个侧栏容器，四个子页）
- **推送**（歌单监控）：订阅榜单 / 歌单链接 / 账号歌单，每轮自动发现新歌；
  「下载到本地曲库」与「同步到飞牛歌单」两个能力位各勾各的。**首轮只建基线不下载**；
  支持干跑预览（`POST /api/monitor/preview`）、版本过滤（Live/Remix/伴奏等剔除）、
  缺口补下载、飞牛同步历史。
- **下载**（队列 + 歌单批量）：持久化下载队列（刷新不丢）、暂停/继续/重试/换源；
  下载行为开关（收藏自动下载并绑定本地 / 边听边下 / 歌词封面附带）也在这页。
- **补全整理**：扫描缺口（歌词/封面/风格/年份/曲序/光盘各缺多少）→ 勾字段 → 预览 → 确认写入 →
  可撤销；体检 / 增量扫描 / 查重（双通道，进 `.tidy_trash` 回收站不直删）同页完成。
  **补全的值一律来自已匹配的搜索结果，不让 AI 猜**。
- **榜单管理**（v2.1.119 从发现页搬入）：哪些榜单注入飞牛音乐、榜单注入总开关、
  页面注入总开关，全在这一个子页配置。

### 🎛️ 音源管理
- **两种互补协议**：`official`（后端内置，免脚本，搜索/榜单/歌单/歌词）+
  `lx`（浏览器脚本，取直链）。没导入任何音源时，搜索/榜单/歌单/歌词照常用，
  只有播放与下载需要一份 LX 脚本。
- **脚本导入**：URL 订阅 / 本地上传 / 批量导入，自动去重；**多选启用池**（有序，前面的优先）。
- **服务型音源**：按服务地址接入（探测协议、直连取链）—— 配了它，关掉浏览器页面也能下载。
- **熔断与健康度**：连续失败 60 秒冷却、健康度排序、真实探针测速、平台健康度统计（`/api/diagnostics/platforms`）。

### 👤 账号连接
- 网易云 / QQ 音乐扫码登录；AI 大模型（OpenAI 兼容）配置也在这页。
- 账号数据用于：每日推荐 / 账号歌单推送 / 在线收藏与歌单接管。

### 📁 本地音乐（NAS 曲库）
- 深扫各存储卷音频（FLAC/APE/WAV/DSD/MP3/AAC/OGG 等），HTTP 206 分片串流，
  内嵌封面与标签解析，歌词自动关联；目录树浏览器随选随切。

### 🎧 沉浸播放器
- 黑胶唱机大屏（`ImmersivePlayer`）：氛围背景、双语歌词对照、点击歌词跳播、
  顺序/随机/单曲循环；切歌语义固定「按当前列表顺序」。
- 底部播放条（v2.1.119 起**默认隐藏**）：右下角一颗搜索图标，点开搜索框从图标位置滑出，
  聚焦不收回，点别处/超时/提交/开始播放/Esc 自动收回；尊重系统「减弱动效」。
- 手机端左右滑动切页（跟手位移 + 阻尼弹簧，`SwipePager`）。

### 🔌 开放接口（供 AI 与外部程序调用）
- 后端 **131 个** HTTP 接口（`catalog.go` 现场数：96 个 core + 35 个 internal，16 个分组），
  全部标准 JSON（`{code,message,data}`）。
- **接口自描述**：`GET /api/catalog` 返回全部接口 + 「怎么做」的工作流指南（含坑位提醒）；
  前端点左上角软件图标滑出「开放接口」抽屉。
- **AI 解析桥**：取直链任务可交给开着的网页端完成（`POST /api/bridge/resolve` → 轮询 jobs）。
- 长任务全部「立即返回 + 轮询」，写文件的动作都有干跑/预览与回收站/备份。

> 数数口径：`grep -cE "Method: +(http\.)?Method[A-Z]" backend/pkg/api/catalog.go` = 131；
> internal 名单 35 条；`go list ./... | wc -l` = 33 包；前端用例数见 [开发](#%EF%B8%8F-开发)。

---

## 🧭 架构一页话

**Go 后端（`backend/`，Go 1.22，33 个包，唯一第三方依赖 `modernc.org/sqlite`）**
单个静态二进制，跑在飞牛原生 FPK 应用里（非 Docker），常驻内存约 15–25MB。
负责：HTTP API 与接口目录、鉴权与 SSRF 防护、搜索/榜单/歌词聚合、下载落盘、
曲库索引与标签读写（ID3v2 / FLAC Vorbis 自实现）、监控与推送调度、飞牛集成与**音乐入口接管**、
AI 用途编排。前端构建产物 `go:embed` 进二进制，一个文件就是整个应用。

**Vue 3 前端（`frontend/`，Vite + Tailwind）** 30 个组件、纯 JS 服务层（下载队列 / 滑动切页 /
导航总线 / 偏好），无组件测试框架 —— 逻辑拆成纯函数用 `node --test` 直测（287 条用例，20 个文件）。

**Python sidecar（`sidecar/musicdl_service/`）** 可选外挂音源进程：把 musicdl 的
**56 个注册源**（sidecar 实测口径）接进搜索与解析，补齐咪咕等 Go 侧没有的平台。
缺省关（`musicdl_enabled`），打开后会自建 Python venv 并装依赖（约占一两百 MB 磁盘）。
缺省只启用实测可用的咪咕一个源，其余在可选列表里。

**服务端洛雪宿主（`sidecar/lx_host/server.mjs`，Node）** ⚠️ **它会执行第三方 JS**：
把用户导入的洛雪(LX)音源脚本放进 Node 的 vm 沙箱在**服务端**跑 `lx.on('request')`，
从而不需要开着浏览器页面也能取直链。这**故意打破**了「后端不执行第三方 JS」的原约束，
是用户在知情后明确选择的方案 —— 所以缺省**关**（`lx_server_enabled`，端口缺省 8920），
要用必须显式打开，并自行评估导入脚本的可信度。

**飞牛官方页面接管层（`pkg/takeover/` + `pkg/intercept/`）** 可选能力（环境变量
`QULV_TAKEOVER=1`，**默认关**）：接管官方音乐的 Unix socket 并全量透传，
拦截层只认领自己登记过的在线曲目（搜索合并/在线取流/收藏歌单历史/在线专辑），
其余逐字节交回官方；关掉开关或进程退出时自动还原官方直连。

---

## 📥 安装（fpk 手动安装）

1. 从 [Releases](https://github.com/webspider7/fnmusic/releases/latest) 下载最新 `.fpk`；
2. 飞牛 NAS 管理后台 → **应用中心** → 左下角 **手动安装** → 上传 fpk → 确认；
3. 桌面出现 **「曲率」** 图标即完成。升级时同样手动安装覆盖，配置与数据保留。
4. 应用依赖 **飞牛音乐**（trim.music）存在（manifest 已声明依赖）；
   页面注入 / 榜单注入 / 在线曲库等「接管系」能力还需要 `.env` 里 `QULV_TAKEOVER=1`（见下）。

## 🔑 关键配置

**环境变量（`.env`，改完重启应用生效）**

| 变量 | 作用 | 缺省 |
|---|---|---|
| `QULV_TAKEOVER` | 音乐入口接管总开关（接管官方 socket、页面注入、在线曲库的前提） | 关（不设或非 `1`） |
| `QULV_TAKEOVER_TARGET` / `QULV_TAKEOVER_UPSTREAM` / `QULV_TAKEOVER_TRACE` | 接管的目标 socket / 让路后上游 / 调试追踪 | 自动推导 / 关 |
| `FN_AUTH_MODE` | 鉴权模式：`none` / `lan` / `token`（见安全一节） | 自动：有 Token 用 `token`，否则 `lan` |
| `FN_API_TOKEN` | API 令牌；设置后除探活/目录/版本三接口外都要带 `X-API-Token` | 不设 |
| `FN_TRUSTED_CIDRS` | `lan` 模式追加免校验网段（逗号分隔 CIDR） | 不设 |
| `FN_CORS_ORIGINS` | 允许跨域来源（逗号分隔）；不设 = 仅同源 | 不设 |
| `FN_ALLOWED_ROOTS` | 允许读写的曲库根目录（冒号分隔），设置后**完全覆盖**内置列表 | `/vol1..16`、`/media`、`/mnt`、`/home` |
| `FN_ALLOW_PRIVATE_NET` | 允许出站请求访问内网/回环（SSRF 防护的例外口子） | 关 |
| `FNOS_TOKEN` / `TRIM_API_TOKEN` | 飞牛音乐令牌 / 飞牛开放 API 令牌（一般自动读取，排查用） | 自动 |

**应用配置（`GET /api/config` 读全量，`POST /api/config` 选择性合并写回；
逐键对照 `backend/pkg/config/config.go` 的 `AppConfig`，无编造键）**

| 键 | 作用 | 缺省 |
|---|---|---|
| `port` / `default_nas_dir` / `download_dir` | 服务端口（实际由启动参数 `-port` 决定，fpk 里是 8898）/ 默认曲库目录 / 下载目录 | — |
| `active_source_ids` | 音源启用池（有序多选；`[]` = 用户显式全关，**不许**自动复活） | 首次安装自动启用第一个 |
| `preferred_platforms` | 主力平台（有序，默认 `[wy,tx]`；其余只兜底） | `[wy,tx]` |
| `chart_playlists` / `chart_playlists_enabled` | 注入飞牛的榜单名单（`wy_3778678` 形）与总开关；关掉**不丢**勾选 | 名单空 / 开 |
| `ui_inject_enabled` | 页面注入总开关：关掉后官方页面逐字节原样 | 开 |
| `musicdl_enabled` / `musicdl_sources` / `musicdl_limit` / `musicdl_port` / `musicdl_python` | musicdl sidecar 总开关 / 启用的平台短码（`mg` 等；空 = 默认）/ 单源条数（别调大，搜索会超时）/ 回环端口 / 建 venv 的解释器 | 关 / `["mg"]` / 5 / 8901 / 自动找 |
| `fav_auto_download` | 飞牛里收藏（或加歌单）时自动下载整轨并绑定本地。**会真的占磁盘带宽** | 关 |
| `lyric_auto_download` / `cover_embed` | 下载时附带歌词（内嵌 + 同名 .lrc）/ 封面内嵌 | 开 / 开 |
| `tee_enabled` | 边听边下：播放在线曲目时把同一条流顺手写进磁盘 | 关 |
| `daily_llm_enabled` | **每日推荐的大模型推荐层**。⚠️ 它会把**收听历史与收藏**（歌名/歌手/专辑，最多 20+20 条）发给模型；与 AI 总开关是两个开关，**两个都开**这层才动。预留 5 个名额、绝不阻塞推荐、按 (用户, 当天) 缓存 | 关 |
| `lx_server_enabled` / `lx_server_port` | ⚠️ **服务端洛雪宿主（执行第三方 JS）**，缺省关；端口缺省 8920 | 关 |
| `fnos_token` | 手工指定飞牛音乐令牌（排查用；留空走自动读取） | 空 |

AI 模型本身（base_url / api_key / model / timeout / enabled）走「账号连接」页
（`/api/ai/config`，读回时密钥脱敏）。AI 的五个用途与硬约束见 `docs/AI 能力评估.md` 与 HANDOVER §3.5。

---

## 🔒 安全注意

- **端口**：主服务 `8898`（TCP，LAN）。musicdl sidecar `8901` 与洛雪宿主 `8920` 只绑**回环**
  （127.0.0.1），外部访问不到；旧版应用端口 8899 与本应用可并存。
- **鉴权三层**（`FN_AUTH_MODE`）：
  - `none`：不校验（历史行为，需显式设置）；
  - `lan`（**默认**）：本机 + 内网 + 链路本地 + carrier-grade NAT 免校验，其它来源必须带 Token；
    只认 TCP 对端地址，`X-Forwarded-For` 一律不采信；
  - `token`：一律校验（`/api/health`、`/api/catalog`、`/api/app/version` 除外）。
  默认（未设 `FN_AUTH_MODE`）：设了 `FN_API_TOKEN` 即 `token` 模式，否则 `lan`。
  **把端口映射/反代到公网前，务必设 `FN_API_TOKEN`。**
- 出站请求默认禁止访问内网/回环（SSRF 防护）；写文件动作全部限制在 `FN_ALLOWED_ROOTS` 内。
- 平台账号凭据只存服务端（0600），任何接口不回传 Cookie；运行日志只留内存环形缓冲（重启即清）。

---

## 🛠️ 开发

**构建与打包**

```bash
bash scripts/build.sh                  # 完整构建：前端 vite build → Go 交叉编译 → 组装 .fpk（含自检）
bash scripts/build.sh --skip-frontend  # 跳过前端，复用 backend/dist
```

**测试**

```bash
cd frontend && npm test                          # node --test：全绿用例数以实测为准
cd backend  && go build ./... && go test ./...   # 33 个包全绿；联网用例默认 skip（LIVE_NET_TEST=1 才跑）
```

**真机验收**：`scripts/nas-acceptance.mjs` —— 在 NAS 的浏览器环境里逐条验
音源启停生效、搜索窗口、注入、推送等（脚本头部有用法说明）。
另有一个数字对账脚本 `bash scripts/audit-numbers.sh`（后端/前端/文档的规模数字现场重数）。

**开发热加载**：`bash scripts/dev.sh` → 前端 `:3000` 热更新 + 后端 `:8900`
（独立 `.dev/` 数据目录，不碰正式实例）。

**本仓库不需要** `fnpack`；打包由 `scripts/build_fpk.py`（纯标准库）完成。

## 📚 版本与变更记录

- 当前版本：**2.1.119**（四处同步：`backend/pkg/api/server.go` 的 `CurrentVersion`、
  `fpk-package/manifest` 的 `version`、`RELEASE_NOTES.md` 顶部、HANDOVER §0）。
- 逐版变更：[`RELEASE_NOTES.md`](RELEASE_NOTES.md)（每版一段，带设计理由）；
  更早的按日期整理在 [`docs/变更记录/`](docs/变更记录/INDEX.md)。
- 架构决策、踩坑记录与「不能做什么」：见 `HANDOVER.md`（给接手者读的活文档）。

---

## ⚖️ 免责声明

本项目为开源的**个人本地私有云音频播放器与文件管理器**，不提供、不聚合、不分发、
不存储任何受版权保护的音频、歌词或图片资源；软件内全部网络曲库能力依赖使用者
**自主导入**的第三方音源脚本或自行配置的外部服务，开发者未制作、未内置、未捆绑任何音源。
用户须自行确保遵守所在地区法律与第三方服务条款；严禁用于商业营利；
第三方脚本的合法性由脚本编写者与导入者自行承担。版权方异议通道：GitHub Issue。
完整政策见应用内首次启动的免责声明弹窗与 [LICENSE](LICENSE)（MIT）。
