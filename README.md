<div align="center">

# 🎵 曲率 · yinshu-ai

**飞牛私有云 OS (fnOS) 上的 AI 音乐中枢**
在线播放 · 订阅追更 · 无损下载 · 曲库整理 · 官方 App 深度接管

[![Platform](https://img.shields.io/badge/Platform-fnOS%20%28x86__64%29-3b82f6?logo=linux)](https://developer.fnnas.com/)
[![Vue 3](https://img.shields.io/badge/Frontend-Vue%203%20%2B%20Vite-42b883?logo=vuedotjs)](https://vuejs.org/)
[![Go](https://img.shields.io/badge/Backend-Go%201.22+-00add8?logo=go)](https://go.dev/)
[![Version](https://img.shields.io/badge/Version-2.17.8-0b8a66)](RELEASE_NOTES.md)
[![License](https://img.shields.io/badge/License-MIT-amber.svg)](LICENSE)

[📦 下载最新版](https://github.com/liwenyao016828/fnmusic/releases/latest) · [✨ 功能](#-功能) · [📥 安装](#-安装) · [🧭 架构](#-架构一页话) · [🛠️ 开发](#️-开发) · [🔌 开放接口](#-开放接口供-ai-与外部程序调用)

</div>

---

> **v2.0.0 起更名**：原「极光音乐」(`fn-lx-player`) → **曲率** (`yinshu-ai`)。
> `appname` 不同，飞牛会把它当成**另一个应用**，**不覆盖**旧版，两者可并存（新 8898 / 旧 8899）。
> 数据目录独立，历史数据不会自动迁移。
>
> 本 README 里所有数字都是现场数出来的（复现命令见 [开发](#️-开发) 一节）；逐版变更见
> [`RELEASE_NOTES.md`](RELEASE_NOTES.md)，架构与踩坑见 [`HANDOVER.md`](HANDOVER.md)。

## ✨ 功能

导航是三层：**听**（发现音乐 / 本地曲库）· **管**（补全整理 / 订阅更新 / 下载）· **配置**（音源管理 / 账号连接 / 系统）。

### 🔍 发现音乐
- **全网聚合搜索**：多平台搜索去噪（`GET /api/search`），按音源启用池挑源取链。
- **榜单 / 精选歌单**：官方协议直连（后端内置，免脚本）浏览网易云与 QQ 音乐的榜单与分类歌单；
  点开即看曲目、直接播、可「订阅更新」。
- **榜单注入**：勾选的榜单以**只读在线歌单**出现在飞牛音乐 App 里，卡片右上角「+ 注入」就地切换。
- **双平台推荐**：已登录账号的每日推荐与账号歌单；可选的大模型推荐层（会发收听历史给模型，独立开关）。
- **正在播放卡**：发现页右侧一张深色卡，直接接管播放控制，不用等播放条。

### 📁 本地曲库
深扫各存储卷（FLAC / APE / WAV / DSD / MP3 / AAC / OGG），HTTP 206 分片串流、
内嵌封面与标签解析、歌词自动关联；目录树随选随切。

### 🧹 补全整理
四个分段，常驻顶部，不再塞进一个「⋯」菜单：
- **补全**：扫描缺口（歌词 / 封面 / 风格 / 年份 / 曲序 / 光盘各缺多少）→ 勾字段 → 预览 → 确认写入 → 可撤销。
  补全的值**一律来自已匹配的搜索结果，不让 AI 猜**。
- **体检 / 索引 / 查重**：增量索引、重复曲目双通道检测（进 `.tidy_trash` 回收站，不直删）。
- **替换音质**：扫曲库（默认只扫有损）→ 逐首列出**各渠道实际能拿到什么档位** → 勾选后用最高音质原地替换。
  档位按**实际字节率**判（mp3 读第一帧标称码率，洛雪按实测字节数 ÷ 时长推算），信不过脚本自报的「无损」。
  每次替换落一条**可查记录**（文件 / 歌名 / `from → to` / 渠道 / 成败），进页就能看见。
- **补全选项**：目录、单次上限、覆盖策略、主力平台，收进右侧抽屉。

### 🔔 订阅更新
歌单 / 榜单 / 账号歌单监控，每轮自动发现新歌；**首轮只建基线不下载**；
支持干跑预览、版本过滤（Live / Remix / 伴奏剔除）、缺口补下载、飞牛同步历史。

### ⬇️ 下载
- **取音渠道可配**：洛雪 → musicdl → 原生，顺序与开关都能调。
  **洛雪多选 = 轮询**：按你在音源页排的顺序挨个问，取**实际档位最高**的那条（不是第一个能下的就赢）。
- **收藏自动下载**：飞牛里收藏 / 加歌单即后台整轨下载并绑定本地。
- **音质补强**：这次拿不到无损就先落一份能拿到的，之后后台每 20 分钟一轮、失败冷却 24 小时、累计 5 回收手 ——
  不再「只试一次就永久放弃」，也不会「每次收藏白下一遍」。
- 持久化队列（刷新不丢）、暂停 / 继续 / 重试 / 换源；边听边下可选。

### 🎛️ 音源管理
- **两种互补协议**：`official`（后端内置，免脚本，搜索 / 榜单 / 歌单 / 歌词）+ `lx`（脚本，取直链）。
  没导入任何脚本时，搜索 / 榜单 / 歌单 / 歌词照常用，只有播放与下载需要一份 LX 脚本。
- 脚本 URL 订阅 / 本地上传 / 批量导入，自动去重；**多选启用池**（有序优先）。
- 服务型音源：按服务地址接入，配了它关掉浏览器页面也能下载。
- 熔断与健康度（连续失败 60 秒冷却）、真实探针测速、平台健康度统计。

### 👤 账号连接
网易云 / QQ 音乐扫码登录；AI 大模型（OpenAI 兼容）配置也在这页。
账号数据用于每日推荐、账号歌单推送、在线收藏与歌单接管。

### 🎧 沉浸播放器
黑胶唱机大屏：氛围背景、双语歌词对照、点词跳播、顺序 / 随机 / 单曲循环。
手机端左右滑动切页（跟手位移 + 阻尼弹簧），底部一颗搜索图标点开才滑出搜索框。

### 🖥️ 系统
日志（环形缓冲，重启即清）与设置（取音渠道、曲库文件命名、存储目录、下载行为、AI 开关）收进分段。

## 🔌 开放接口（供 AI 与外部程序调用）

本应用的设计目标之一就是**让别的 AI / 程序能直接调**：

- 后端 **136 个** HTTP 接口，全部标准 JSON（`{code,message,data}`），16 个分类。
- **接口自描述**：`GET /api/catalog` 返回全部接口 + 工作流指南 + 坑位提醒；
  前端点左上角软件图标滑出「开放接口」抽屉，渲染的是同一份数据。
- **AI 解析桥**：取直链任务可交给开着的网页端完成（`POST /api/bridge/resolve` → 轮询 jobs）。
- 长任务一律「立即返回 + 轮询」；写文件的动作都有干跑 / 预览 / 回收站 / 备份。

```bash
curl http://NAS:8898/api/app/version
curl http://NAS:8898/api/catalog                 # 全部接口 + 用法
curl -X POST http://NAS:8898/api/search -H 'Content-Type: application/json' -d '{"keyword":"晴天"}'
```

> 数数口径：`grep -cE "Method: +(http\.)?Method[A-Z]" backend/pkg/api/catalog.go` = 136；
> internal 名单 37 条；`go list ./... | wc -l` = 36 包。

## 🧭 架构一页话

**Go 后端**（`backend/`，Go 1.22，36 个包，3 个直接依赖 `coder/websocket` /
`golang.org/x/sys` / `modernc.org/sqlite`）单个静态二进制，跑在飞牛原生 FPK 应用里（非 Docker），
常驻内存约 15–25MB。负责 HTTP API 与接口目录、鉴权与 SSRF 防护、搜索 / 榜单 / 歌词聚合、
下载落盘、曲库索引与标签读写（ID3v2 / FLAC Vorbis 自实现）、监控与推送调度、
飞牛集成与音乐入口接管。前端产物 `go:embed` 进二进制，一个文件就是整个应用。

**Vue 3 前端**（`frontend/`，Vite + Tailwind，40 个组件）纯 JS 服务层，无组件测试框架 ——
逻辑拆成纯函数用 `node --test` 直测（**281 条用例 / 23 个文件**）。
界面是「安静玻璃」风格：中性色打底，品牌青玉只留给**正在发生的事**，控件 10px / 卡片 18px 圆角，
动效三档（160 / 260 / 360ms，`cubic-bezier(.32,.72,0,1)`），尊重「减弱动效」。

**Python sidecar**（`sidecar/musicdl_service/`）可选外挂音源：把 musicdl 的注册源接进搜索与解析，
补齐咪咕等 Go 侧没有的平台。缺省关，打开后自建 venv（约一两百 MB）。

**洛雪宿主**（`sidecar/lx_host/server.mjs`，Node）⚠️ **它会执行第三方 JS**：
把导入的 LX 脚本放进 `vm` 沙箱在**服务端**跑，从而不需要开着浏览器也能取直链。
这**故意打破**了「后端不执行第三方 JS」的原约束 —— 所以缺省**关**（`lx_server_enabled`，端口 8920），
要用必须显式打开并自行评估脚本可信度。宿主会对每条直链做 **Range 活性探测**：
真死链（404 / 错误页 / 非音频）判死换下一个脚本，「探测没结论」（超时）则保留但标记 unproven。

**飞牛接管层**（`pkg/takeover/` + `pkg/intercept/`）可选（`QULV_TAKEOVER=1`，**默认关**）：
接管官方音乐的 Unix socket 并全量透传，只认领自己登记过的在线曲目，其余逐字节交回官方；
关开关或进程退出时自动还原。

## 📥 安装

1. 到 **[Releases](https://github.com/liwenyao016828/fnmusic/releases/latest)** 下载最新 `yinshu-ai-*.fpk`；
2. 飞牛 NAS 管理后台 → **应用中心** → 左下角 **手动安装** → 上传 fpk → 确认；
3. 桌面出现 **「曲率」** 图标即完成；升级同样手动安装覆盖，配置与数据保留。

**从源码打包**（不需要 `fnpack`）：

```bash
bash scripts/build.sh --version 2.17.8      # 前端 → Go 交叉编译 → 组装 .fpk（含自检）
```

应用依赖 **飞牛音乐**（trim.music）存在（manifest 已声明）；页面注入 / 榜单注入 / 在线曲库
等「接管系」能力另需 `.env` 里 `QULV_TAKEOVER=1`（见下）。

> ⚠️ **升级必须 stop → start**，不能用 restart：`restart` 不会重启洛雪 node 子进程，
> 旧的 `server.mjs` 会继续跑，新逻辑（比如直链活性校验）不生效。

## 🔑 关键配置

**环境变量**（`.env`，改完重启应用生效）

| 变量 | 作用 | 缺省 |
|---|---|---|
| `QULV_TAKEOVER` | 音乐入口接管总开关（页面注入 / 榜单注入 / 在线曲库的前提） | 关 |
| `QULV_TAKEOVER_TARGET` / `QULV_TAKEOVER_UPSTREAM` / `QULV_TAKEOVER_TRACE` | 目标 socket / 让路后上游 / 调试追踪 | 自动推导 / 关 |
| `FN_AUTH_MODE` | 鉴权：`none` / `lan` / `token` | 自动（有 Token 用 `token`，否则 `lan`） |
| `FN_API_TOKEN` | API 令牌；设置后除探活 / 目录 / 版本外都要带 `X-API-Token` | 不设 |
| `FN_TRUSTED_CIDRS` | `lan` 模式追加免校验网段（逗号分隔 CIDR） | 不设 |
| `FN_CORS_ORIGINS` | 允许跨域来源；不设 = 仅同源 | 不设 |
| `FN_ALLOWED_ROOTS` | 允许读写的曲库根（冒号分隔），设置后**完全覆盖**内置列表 | `/vol1..16`、`/media`、`/mnt`、`/home` |
| `FN_ALLOW_PRIVATE_NET` | 允许出站访问内网 / 回环（SSRF 防护的例外） | 关 |
| `FNOS_TOKEN` / `TRIM_API_TOKEN` | 飞牛音乐令牌 / 飞牛开放 API 令牌（一般自动读取） | 自动 |

**应用配置**（`GET /api/config` 读全量，`POST /api/config` 选择性合并写回；
逐键对照 `backend/pkg/config/config.go` 的 `AppConfig`，无编造键）

| 键 | 作用 | 缺省 |
|---|---|---|
| `port` / `default_nas_dir` / `download_dir` | 服务端口（fpk 里 8898）/ 默认曲库目录 / 下载目录 | — |
| `active_source_ids` | 音源启用池（有序多选；`[]` = 用户显式全关，**不许**自动复活） | 首装自动启用第一个 |
| `auto_download_channels` | 取音渠道（`lx` / `dl` / `native`，列表本身就是允许集 + 顺序） | `[lx, dl, native]` |
| `preferred_platforms` | 主力平台（有序；其余只兜底） | `[wy, tx]` |
| `fav_auto_download` | 飞牛里收藏 / 加歌单时自动下载并绑定本地（会真占磁盘带宽） | 关 |
| `auto_download_quality` | 自动下载目标档位（`highest` = 尽力无损） | `highest` |
| `lx_server_enabled` / `lx_server_port` | ⚠️ 服务端洛雪宿主（执行第三方 JS） | 关 / 8920 |
| `musicdl_enabled` / `musicdl_sources` / `musicdl_limit` / `musicdl_port` | musicdl sidecar 开关 / 平台短码 / 单源条数 / 回环端口 | 关 / `["mg"]` / 5 / 8901 |
| `lyric_auto_download` / `cover_embed` | 下载时附带歌词 / 封面内嵌 | 开 / 开 |
| `tee_enabled` | 边听边下 | 关 |
| `daily_llm_enabled` | 每日推荐的大模型层（会把歌名 / 歌手 / 专辑发给模型，独立开关） | 关 |
| `chart_playlists` / `chart_playlists_enabled` / `ui_inject_enabled` | 注入飞牛的榜单名单 / 总开关 / 页面注入总开关 | 空 / 开 / 开 |

AI 模型本身（base_url / api_key / model / timeout / enabled）在「账号连接」页
（`/api/ai/config`，读回时密钥脱敏）。

## 🔒 安全

- **端口**：主服务 8898（LAN）；musicdl 8901 与洛雪宿主 8920 **只绑回环**，外部访问不到。
- **鉴权三层**（`FN_AUTH_MODE`）：`none` 不校验；`lan`（默认）本机 + 内网 + 链路本地 + CGNAT 免校验，
  其余须带 Token，只认 TCP 对端地址（`X-Forwarded-For` 一律不采信）；`token` 一律校验。
  **把端口映射 / 反代到公网前，务必设 `FN_API_TOKEN`。**
- 出站默认禁止访问内网 / 回环（SSRF 防护）；写文件全部限制在 `FN_ALLOWED_ROOTS` 内。
- 平台账号凭据只存服务端（0600），任何接口不回传 Cookie；运行日志只留内存环形缓冲。

## 🛠️ 开发

```bash
# 热加载：前端 :3000 + 后端 :8900，独立 .dev/ 数据目录，不碰正式实例
bash scripts/dev.sh

# 构建与打包
bash scripts/build.sh                       # 前端 → Go → .fpk（含自检）
bash scripts/build.sh --skip-frontend       # 复用 backend/dist
bash scripts/build.sh --version 2.17.8      # 指定版本号

# 测试
cd frontend && node --test 'src/services/*.test.mjs'   # 281 条
cd backend  && go build ./... && go test ./...         # 36 包；联网用例默认 skip（LIVE_NET_TEST=1 才跑）
node --test scripts/lx-utils-check.mjs                 # 洛雪宿主单测（147 条）
```

> `node --test` 在本机 v22 上**不接受目录**，要传具体文件或带引号的 glob。

**真机验收**：`scripts/nas-acceptance.mjs`；数字对账：`bash scripts/audit-numbers.sh`。
**本仓库不需要** `fnpack`，打包由 `scripts/build_fpk.py`（纯标准库）完成。

## 📚 版本与发版

- 当前版本 **2.17.9**，四处手工同步：`backend/pkg/api/server.go` 的 `CurrentVersion`、
  `fpk-package/manifest` 的 `version`、`RELEASE_NOTES.md` 顶部、`HANDOVER.md` §0。
- 升哪一位：**x.0.0** 架构级 / 破坏性变更；**2.x.0** 新增能力（新功能 / 新页面 / 新链路）；
  **2.x.y** 修 bug、UI 微调、文档。
- **自动发布**：功能完成并验证后，由 DSH 提交并推送 `main`。GitHub Actions 自动校验四处版本、运行测试、构建并自检 FPK，再创建 [Release](https://github.com/liwenyao016828/fnmusic/releases)（安装包 + SHA256 + 本版说明）。已有版本不覆盖；同版本文档提交只检查。PR 和其他分支只检查，不发布，也不自动部署 NAS。
- **失败重跑**：在 Actions 页面重跑，或执行 `bash scripts/release.sh`（需要已登录的 GitHub CLI）。不需要手动打 tag 或另配个人 Token。
- 仓库里**只保留最新一个** `.fpk`（历史包在 Releases，不在 git 历史里 —— 否则仓库没法克隆）。
  本仓库根目录的 `yinshu-ai-2.17.8.fpk` 就是最新一版（sha256
  `5d840c4b7700c93ed92cff92453c272e60481adde59115bd58c6e42f861ab072`）。
  > 仓库里放的是**发版留档**；对外分发请走 **Release 资产**（含校验和、可追溯、不进 git 历史）。
  > GitHub 的 raw 直链对超过 100 MB 的单个文件会拒绝，`.fpk` 目前没到线，但别拿它当下载渠道。

## ⚖️ 免责声明

本项目为开源的**个人本地私有云音频播放器与文件管理器**，不提供、不聚合、不分发、
不存储任何受版权保护的音频、歌词或图片资源；全部网络曲库能力依赖使用者**自主导入**的
第三方音源脚本或自行配置的外部服务。用户须自行确保遵守所在地区法律与第三方服务条款，
严禁用于商业营利；第三方脚本的合法性由脚本编写者与导入者自行承担。
完整政策见应用内首次启动的免责声明弹窗与 [LICENSE](LICENSE)（MIT）。
