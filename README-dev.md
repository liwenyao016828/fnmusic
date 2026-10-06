# music-v2 · 开发者入口

> **先读 [`HANDOVER.md`](HANDOVER.md)** —— 架构规则、踩过的坑、业务规则、接口契约、发布流程都在那里。
> 面向用户的功能介绍见 [`README.md`](README.md)。

---

## 这是什么

飞牛 NAS（fnOS）上的音乐播放器 **+ 曲库管家**，Go 后端 + Vue 3 前端，打包为 fnOS 原生 `.fpk`。

**主要供其他 AI / 外部程序调用**（下载歌曲、查歌单、整理曲库），其次才是人机交互。

> ⚠️ **接手须知**：项目的主要消费者是 AI，所以**改完代码请同步更新本文档与 `HANDOVER.md`**。
> 最易腐化的数字：测试用例数、接口总数、包数量与行数、依赖状态、版本号。

## 核心能力一览

| 模块 | 说明 |
|---|---|
| 🎵 **播放器** | 黑胶唱机沉浸式界面、歌词同步、多循环模式、响应式适配 |
| 📁 **本地曲库** | NAS 多存储卷扫描、无损串流（支持 Range）、内嵌封面/歌词提取 |
| 🧩 **音源管理** | **批量导入**（在线 URL + 本地脚本，内容哈希去重）、导出/清理/批量删除、熔断、健康度排序 |
| 📥 **下载队列** | 音质阶梯轮询、失败自动换源、断点续传、排队限速防风控 |
| 🗂️ **曲库管家** | 歌单监控（增量 + 版本过滤）、批量刮削、去重（默认进回收站）、AI 接入 |
| 🔍 **真实音质** | 搜索返回各平台**真实可用档位**（不再硬编码） |
| 🚀 **飞牛集成** | 官方开放 API 目录授权、飞牛音乐歌单推送（曲目自动匹配） |
| 👤 **账号连接** | 网易云 / QQ 音乐**扫码登录**，可取日推与私人歌单 |
| 🔁 **推送同步** | 把账号的歌单/日推**定时搬到飞牛音乐**（订阅模型 + 匹配 + 执行历史） |
| 🔌 **开放接口** | **84 个** HTTP + JSON 接口，`GET /api/catalog` 自描述供 AI 自动发现 |

## 快速开始

**推荐在 WSL2 下开发**（目标平台是 Linux；Windows 下路径守卫有 5 个测试必失败）。

```bash
# 0) 环境自检
bash .dev/wsl-run.sh status

# 1) 前端热加载预览（改 UI 用这个，不要打包）
bash .dev/wsl-run.sh dev
#    → http://localhost:3000/   （注意用 localhost，不要用 127.0.0.1）

# 2) 跑测试
bash .dev/wsl-run.sh test                      # 304 个用例
cd frontend && npm test && npm run build       # 15 个用例 + 构建

# 3) 打包成安装包
bash .dev/wsl-run.sh fpk
```

Windows 下也能跑（`bash scripts/dev.sh`），但**必须设 `FN_ALLOWED_ROOTS`**，否则路径守卫 fail closed。

> 详见 [`HANDOVER.md`](HANDOVER.md) §1。

## 目录结构

```
music-v2/
├── HANDOVER.md            ← ★ 先读这个
├── README.md              # 面向用户的功能与安装说明
├── README-dev.md          # 本文件
├── RELEASE_NOTES.md
├── backend/               # Go 后端
│   ├── pkg/
│   │   ├── api/           # 路由 + 接口目录（catalog.go 是单一数据源）+ 飞牛/账号 HTTP 层
│   │   ├── fnos/          # 飞牛集成：官方开放 API + 音乐接口 + 令牌读取
│   │   ├── account/       # 第三方账号：网易云 / QQ 扫码登录、日推、歌单
│   │   ├── nas/           # 目录扫描、串流、标签、路径守卫
│   │   ├── monitor/       # 歌单监控
│   │   ├── sources/       # 音源脚本管理（含批量导入/导出/清理）
│   │   ├── tidy/          # 刮削整理 + 去重
│   │   ├── search/        # 多平台搜索聚合 + 真实音质解析（quality.go）
│   │   ├── ai/            # OpenAI 兼容客户端（含 SSE 解析）
│   │   ├── library/       # 增量索引 + 枚举完整性安全门
│   │   ├── match/         # 版本识别与匹配打分（飞牛推送也复用它）
│   │   ├── security/      # SSRF 防护 + 代理分流
│   │   ├── tags/          # 音频标签读写（自研）
│   │   ├── downloader/    # 下载落盘
│   │   ├── bridge/        # AI 解析桥
│   │   ├── charts/        # 排行榜与歌单
│   │   └── ...
│   └── main.go
├── frontend/              # Vue 3 + Vite
│   └── src/
│       ├── components/    # 17 个组件（含推送同步 PushManager、账号连接 AccountManager）
│       ├── engine/        # 音源沙箱（lx-runtime）、音质阶梯（quality）
│       └── services/      # 下载队列、熔断、AI 桥、偏好缓存
├── fpk-package/           # fnOS 打包元数据（manifest / cmd / config / ui）
├── scripts/               # build.sh / dev.sh / build_fpk.py
├── .dev/                  # 本地开发辅助（已 gitignore）：wsl-run.sh 等
└── docs/                  # 调研资料与参考项目源码
    ├── ncm-qmc-cue-研究报告.md
    └── 参考项目/music-tidy/
```

## 三条必须先理解的架构规则

1. **音源脚本只能在浏览器执行** —— 后端不内置、也无法运行在线音源解析（合规要求）。
   取直链在前端完成，再交给后端落盘。任何"后端自动抓歌单"的需求都要先解决这个约束。
2. **依赖策略**（2026-09-16 修订，旧「零第三方依赖」已作废）——
   用户明确要求「最简单化处理，能用外部依赖简化就用」。
   但仍需遵守：**只用纯 Go 依赖**（交叉编译要 `CGO_ENABLED=0`）、**钉版本禁止 `@latest`**、**不 vendor**（改用 `GOPROXY`）。见 HANDOVER §2。
3. **所有文件访问必须过 `pkg/nas/pathguard.go`** —— 它是唯一的路径安全闸门。

## 相关文档

| 文档 | 内容 |
|---|---|
| [`HANDOVER.md`](HANDOVER.md) | **交接主文档**：架构、踩过的坑、业务规则、接口契约、发布流程、待办 |
| [`README.md`](README.md) | 面向用户：功能亮点、安装指南、免责声明 |
| [`RELEASE_NOTES.md`](RELEASE_NOTES.md) | 版本变更记录 |
| `docs/ncm-qmc-cue-研究报告.md` | NCM/QMC 解密与 CUE 拆分的算法调研（待办项资料） |
| `docs/参考项目/music-tidy/` | 参考项目源码 + 精读报告 |

> ⚠️ 参考项目的源码仅作学习对照，**其许可证与本项目不同**，不要直接复制代码进本项目。
