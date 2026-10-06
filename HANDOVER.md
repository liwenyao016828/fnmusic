# 曲率（原极光音乐）· music-v2 交接文档

> **给接手的 AI / 开发者**：先读完这份文档再动代码。这里记录了架构决策、**踩过的坑**和**不能做什么**，
> 全部来自实际开发与验证，不是推测。跳过它你大概率会重复踩同样的坑。

> ⚠️ **应用已更名**（2026-09-17）：显示名 **极光音乐 → 曲率**，`appname` **`fn-lx-player` → `yinshu-ai`**，
> 版本 **1.4.0 → 2.0.0**，端口 **8899 → 8898**。
> 两者在飞牛上是**两个独立应用**，可以并存。改应用名时必须同步的三处见 §11「深夜 ④」。
> 注意：**Go module 名仍是 `fn-lx-player`**（import 路径，不参与应用标识，不要动）。

---

## 0. 30 秒速览

| 项 | 内容 |
|---|---|
| **这是什么** | 飞牛 NAS（fnOS）上的音乐播放器 + 曲库管家，`Go` 后端 + `Vue 3` 前端，打包成 fnOS 原生 `.fpk` |
| **主要用途** | **供其他 AI / 外部程序调用**（帮用户下载歌曲、看歌单），其次才是人机交互 |
| **代码规模** | 后端 **32 个包 / 236 个 `.go` / 70,066 行**（不含测试 42,711；测试 27,355）/ 后端测试函数 **985 个**（联网用例默认 skip，要 `LIVE_NET_TEST=1`）；前端 **263 个测试**（2026-10-06 实测，复测跑 `bash scripts/audit-numbers.sh`） |
| **文档怎么读** | 本文件只有 **§0–§10**（活文档：架构 / 坑 / 业务规则 / 接口）。**历史变更条目在 [`docs/变更记录/`](docs/变更记录/INDEX.md)** —— `INDEX.md` 是 82 个回合的目录，正文按日期段分文件（2026-09-26 从 §11 拆出：那一节积到 4,595 行 / 310KB，占全文三分之二）。别处写的「§11 ㊼ 块十」= 那里同名回合的小节 |
| **应用标识** | 显示名 **曲率**，`appname` **`yinshu-ai`**，端口 **8898**（旧应用 `fn-lx-player` / 8899 可并存） |
| **后端依赖** | **已引入第三方依赖**（`modernc.org/sqlite v1.36.1`，纯 Go）。旧版「零依赖」规则已作废，见 §2 规则二 |
| **前端依赖** | vue / vite / tailwindcss / axios / lucide-vue-next / crypto-js / buffer / qrcode |
| **打包** | `bash scripts/build.sh`，**不需要 fnpack** |
| **当前版本** | `2.1.118`（代码与 manifest 已同步，见 §7） |
| **名称** | 显示名 **曲率**（2026-09-28 由「音枢 AI」更名）；技术标识 **`yinshu-ai`**（`appname` / 启动项 / 包文件名 / API 身份串）**刻意不改** |
| **改名红线** | 显示名随便改（`display_name`、界面、文档）；**`appname` 是技术标识，动之前必须单独跟用户确认** —— 它决定飞牛应用中心认不认"同一个应用"，改了就是新装一个，旧配置和数据不继承 |
| **版本规则** | ⚠️ **只要改过代码就必须升版本号**（用户 2026-09-28 定），四处同步：`backend/pkg/api/server.go` 的 `CurrentVersion`、`fpk-package/manifest` 的 `version`、`RELEASE_NOTES.md` 顶部、本表。**不许用同一个版本号重打包** |
| **推荐开发环境** | **WSL2 Debian**（见 §1.0）——Windows 下也能跑，但需要额外环境变量 |
| **接口总数** | `backend/pkg/api/catalog.go` 共 **122 个**（core 87 / internal 35，见 §6.1）。目录**面向 AI**：顶层 `guide` 先说「是干嘛的、怎么做」，接口清单只是参考 ⚠️ `/api/catalog` 是**扁平结构** —— 数组在**顶层 `endpoints`**，不是 `data.endpoints` |
| **音乐入口接管** | 可选能力（`QULV_TAKEOVER=1`），默认关闭。接管官方 Unix socket 并全透传，见 §2 规则六 / §3.6 |
| **在线曲库** | 接管生效时可用（v2.1.70 新增；v2.1.71 修真机接管失败；v2.1.72 修 isFavorite 写死）：网易 + QQ 的在线搜索/取流/歌词/封面/收藏/歌单/历史，合并进官方界面。见 §3.7 |
| **音源协议** | **两种互补协议**：`official`（后端内置，免脚本，搜索/榜单/歌单/歌词）+ `lx`（浏览器脚本，取直链）。见 §3.4 |
| **文案与报错口径** | **界面只给「你该怎么做」，报错只留一句人话；技术细节一律进日志**。改任何提示文案前先读 §3.3.3 |
| **AI 怎么用** | **只做「消歧」和「补缺」，不做「生成」**。五个用法与硬约束见 **§3.5**；完整评估与「明确不做」清单见 `docs/AI 能力评估.md` |

---

## 1. 立刻能跑起来的三件事

### 1.0 开发环境：推荐 WSL2（重要）

**仓库主副本位于 WSL 原生文件系统**：`/home/liwenyao/projects/music-v2`
（Windows 侧经 `\\wsl$\Debian\home\liwenyao\projects\music-v2` 访问）

**为什么用 WSL**：
- 目标平台是 Linux，WSL 下 `pkg/nas` 的路径守卫测试才能全绿（Windows 上有 5 个必失败，属平台语义差异）
- `npm install` 在 WSL 原生 ext4 上 **6 秒**，在 `/mnt/d` 上要 **3 分 23 秒**
- 完整打包（前端构建 + 交叉编译 + FPK）**6~18 秒**

**统一入口**：`.dev/wsl-run.sh`
```bash
bash .dev/wsl-run.sh status     # 环境与仓库状态
bash .dev/wsl-run.sh test       # 跑测试（后端 519 + 前端 105，全绿）
bash .dev/wsl-run.sh build      # 仅编译
bash .dev/wsl-run.sh fpk        # 打包 FPK（含前端）
bash .dev/wsl-run.sh fpk-fast   # 打包（跳过前端，复用 dist）
bash .dev/wsl-run.sh dev        # 开发环境（:3000 热更新 + :8900）
```
从 Windows 调用：
```
wsl.exe -d Debian -- bash /home/liwenyao/projects/music-v2/.dev/wsl-run.sh test
```

**访问服务的坑**：浏览器必须用 `localhost`（WSL2 转发走 IPv6 `[::1]`，`127.0.0.1` 不通）；
curl 测本机服务要加 `--noproxy '*'`（环境里有代理会拦截 localhost 请求）。

### 1.1 前端热加载预览（改 UI 用这个，**不要打包**）

```bash
cd music-v2
bash scripts/dev.sh
# → 前端 http://<本机IP>:3000/   后端 http://127.0.0.1:8900
```

`scripts/dev.sh` 会自动：装前端依赖 → 构建后端 → 起 Vite 热加载 → Ctrl+C 一起停。
它使用独立的 `.dev/` 数据目录，**不会碰正式实例**。

> 用户明确要求过：想看前端效果时必须能直接热加载，不要每次打包再安装。

> ⚠️ **Windows 下必须设 `FN_ALLOWED_ROOTS`**，否则路径守卫 fail closed，曲库功能全被拒：
> ```bash
> export FN_ALLOWED_ROOTS="D:/.../music-v2/.dev/music"
> bash scripts/dev.sh
> ```

### 1.2 编译与测试

```bash
cd backend
gofmt -l .        # 必须干净
go vet ./...      # 必须通过
go build ./...    # 必须通过
go test ./...     # 460 个用例必须全绿
```

> **关于 Go 工具链**：WSL 内已装在 `/usr/local/go`（`go` 在 PATH）。
> Windows 侧装在 `.tools/go`，脚本会自动探测。
> 打包脚本 `scripts/build_fpk.py` 依次查找：`--go` 参数 → `.tools/go/bin/go[.exe]` → PATH → 常见路径。

### 1.3 打包 FPK

```bash
cd music-v2
bash scripts/build.sh                      # 完整：前端构建 + 交叉编译 + 打包 + 自检
bash scripts/build.sh --version 1.3.0      # 指定版本号
python3 scripts/build_fpk.py --inspect x.fpk   # 勘察任意 fpk 结构
```

产物：`yinshu-ai.fpk`（约 5.9 MB，6.18 MB）。脚本会**按 manifest 里的 `appname` 命名**，
改应用名不用动打包脚本（见 §0 改名三处）。

> **首次构建需要网络**：项目已引入第三方依赖，`build_fpk.py` 的 `go_env()` 会设置
> `GOPROXY=https://goproxy.cn,direct` 与 `GOTOOLCHAIN=local`。模块下载到 `GOPATH/pkg/mod` 后即可离线构建。

#### ⚠️ FPK 里的 `ui/` 目录**不含前端页面**

解包 `app.tgz` 后看到的是：

```
yinshu-ai          ← Go 二进制（12 MB，前端静态文件 embed 在里面）
ui/config          ← 声明 iframe + port 8898
ui/images/*.png    ← 图标
config/*           ← 权限与资源声明
```

`ui/config` 里写的是：

```json
{ ".url": { "yinshu-ai.main": { "type": "iframe", "port": 8898, "url": "/" } } }
```

**飞牛用 iframe 打开 8898 端口，页面由 Go 服务自己提供**（`go:embed` 了 `frontend/dist`）。
**「ui 目录里没有 index.html」是正常形态**，不是漏打，别照这个去改打包脚本。

验证前端确实进去了：

```bash
strings <解出的二进制> | grep -oE "assets/index-[A-Za-z0-9_-]+\.(js|css)" | sort -u
```

#### 交付前值得做的核验（别只看「打包成功」四个字）

```bash
# 1. manifest 三处标识
tar -xzf yinshu-ai.fpk -O manifest | grep -E "appname|version|service_port"
# 2. 二进制构建时间（确认不是复用了旧产物）
tar -xzf yinshu-ai.fpk app.tgz && mkdir -p /tmp/c && tar -xzf app.tgz -C /tmp/c
stat -c "%y %s" /tmp/c/yinshu-ai
# 3. 本轮新功能符号真的编进去了（挑几个新写的函数名）
strings /tmp/c/yinshu-ai | grep -cE "PickSongMatch|VerifyLyric|MatchedByAI"
# 4. 测试是绿的
cd backend && go test ./... ; cd ../frontend && npm test
```

> 前端测试是 **`node --test`**（`npm test`），**不是 vitest**。
> 直接 `npx vitest run` 会去下另一个版本然后报错，别被带偏。

### 1.4 把 fpk 送到飞牛（用户 2026-09-30 要的「放测试文件夹」）

**投放目录就是 `/vol3/1000/音乐/测试下载/`**（同时也是曲率的下载目录）。用户是从这个目录装包的，
里面按版本平铺着 `yinshu-ai-2.1.32.fpk … 2.1.72.fpk`。文件名必须带版本号。

**NAS 主机名 `wenyaoNAS`，局域网地址 `192.168.1.66`**（2026-10-05 补记）。NAS 上装的曲率版本
用 `curl http://192.168.1.66:8898/api/app/version` 查；`8899` 是并存的旧「极光音乐」（`fn-lx-player`）。

⚠️ **别再用「让应用自己下载」这条路**：那个目录虽然就是应用的下载目录，但 `POST /api/download/song`
有 SSRF 策略（`pkg/security`），**会拒绝内网地址**，实测返回
`拒绝下载该地址: 禁止访问内网/元数据地址 <开发机IP>`；`AllowPrivateNet` 只由环境变量
`FN_ALLOW_PRIVATE_NET` 决定，**没有任何接口能打开**；家里路由的 hairpin 也不通（实测超时）。

✅ **能用的路（2026-09-30 实测成功）**：`ego_script` 里的 Node 进程**跑在飞牛本机上**
（`os.hostname()` = `wenyaoNAS`，能直接看到 `/vol1` `/vol3`），而本会话的 `bash`/文件工具跑在
**开发机**（看不到 NAS 文件系统）。所以：

```js
// 1) 开发机先把包用 HTTP 放出来
//    python3 -m http.server 18081 --bind 0.0.0.0   （在仓库根目录）
// 2) ego_script（注意：该运行时的 fetch 不是全局函数，要用 node:http）
const http = await import('node:http');
const fs = await import('node:fs');
// ... http.get 取回 Buffer → fs.writeFileSync('/vol3/1000/音乐/测试下载/yinshu-ai-X.fpk', buf)
fs.chmodSync('/vol3/1000/音乐/测试下载/yinshu-ai-X.fpk', 0o644);   // ⚠️ 必须
```

⚠️ **必须 `chmod 644`**：那个进程的 umask 会把文件写成 `707`，和同目录其它包（`644`）不一致。

其他已知边界（别浪费时间再试）：飞牛 **没有 SSH**（22 不通）；SMB 445 开着但匿名只有 `IPC$`；
从开发机只能到 `8898`（曲率）/ `8899`（旧版 fn-lx-player）/ `20128`（AI 网关）。

---

## 2. 架构：六条必须理解的规则

### 规则一：音源脚本只能在浏览器里跑

第三方音源（洛雪 lx 格式的 `.js` 脚本）**必须在浏览器执行**，后端无法运行它们。

- 前端 `src/engine/lx-runtime.js` 用 `new Function` 加载脚本，暴露 `globalThis.lx`
- 取直链在**浏览器**完成，然后 `POST /api/download/song` 把直链交给 Go 落盘
- 后端**不内置任何在线音源**（合规要求，见 README 免责声明）

**推论**：任何"后端定时自动抓歌单"的需求，都要先解决这个问题。
当前方案见 §4.1（浏览器上报模式）。

### 规则二：依赖策略（**2026-09-16 修订，旧「零依赖」规则已作废**）

**用户明确指示**：「那个铁律是 ai 写的。我的要求就是最简单化处理。能少代码就少代码，要求精简，
不要转弯抹角的。直截了当达成目的。**用外部依赖简单的化就用好了**」

→ **不要为了「纯净」手写轮子**。能用成熟依赖就用。

**仍然有效的硬约束**（这些是技术事实，不是风格偏好）：

| 约束 | 原因 |
|---|---|
| **只能用纯 Go 依赖** | 交叉编译需 `CGO_ENABLED=0`。SQLite 要用 `modernc.org/sqlite`，**不能**用 `mattn/go-sqlite3` |
| **钉版本，禁止 `@latest`** | `go get @latest` 会自动顶高 `go.mod` 的 go 指令并下载新工具链，破坏构建一致性。当前基线 **`go 1.22`** |
| **不 vendor** | `go mod vendor` 后达 228MB（modernc 占 219MB）。改为在 `build_fpk.py` 设 `GOPROXY` |

**已自研的替代品**（保留，不必替换）：

| 需求 | 实现 | 位置 |
|---|---|---|
| MP3 ID3v2.3 读写（USLT 歌词帧、APIC 封面） | 手写 | `pkg/tags/id3v2.go` |
| FLAC Vorbis Comment + PICTURE 块读写 | 手写 | `pkg/tags/flac.go` |
| JPEG/PNG/GIF 尺寸与位深嗅探 | 手写 | `pkg/tags/image.go` |
| 音乐库增量索引（替代 SQLite） | JSON + 原子写 | `pkg/library/index.go` |
| 歌单监控存储（替代 SQLite） | JSON + 原子写 | `pkg/monitor/store.go` |
| SSRF 防护 + 代理分流 | 标准库 | `pkg/security/security.go` |
| OpenAI 兼容客户端（含 SSE 解析） | 标准库 | `pkg/ai/ai.go` |

**当前依赖清单**：`modernc.org/sqlite v1.36.1`（仅用于读飞牛音乐库的登录令牌，见 `pkg/fnos/token.go`）

### 规则三：所有文件访问必须过路径守卫

`pkg/nas/pathguard.go` 是唯一入口：

```go
AllowedRoots() []string                  // 允许访问的根目录
ResolveSafePath(raw string) (string, error)  // 校验并解析真实路径
```

- 所有 NAS 处理器（browse / scan / stream / cover / lyric / tags）**必须先调用它**
- `AllowedRoots()` 优先级：**飞牛授权目录** → `FN_ALLOWED_ROOTS` → 卷探测（见 §3.1 `pkg/fnos`）
- 环境变量 `FN_ALLOWED_ROOTS`（冒号分隔）一旦设置就完全覆盖内置根目录；设空串 = 拒绝一切
- ⚠️ `ResolveSafePath` **要求路径已存在**（内部用 `EvalSymlinks`）。
  校验「待创建的下载目录」时要先自底向上找到最近的**已存在祖先**再校验
- ⚠️ **`GET /api/nas/read`（读文本文件）有三道约束，别把任意一条删掉**（v2.1.56 加）：
  路径守卫只是"在允许的根目录内"，而根目录里同样躺着配置、密钥、数据库 ——
  所以还必须有**扩展名白名单**（`.js/.mjs/.cjs/.json/.txt`）和**2MB 体积上限**，
  非 UTF-8 也拒。这个接口的唯一用途是「从 NAS 选音源脚本」，不是通用文件读取。
- ⚠️ **`/api/nas/browse` 的 `volumes` 不能丢**（v2.1.57 修）：它 = `AllowedRoots()`，
  是前端「从 NAS 选脚本」弹窗里**唯一的切盘入口**。前端 `SourceManager.vue` 的 `loadNasDir()`
  一度只留 `current/parent/folders/files`，导致弹窗只能看到默认打开的那一个根（用户反馈「只能看到一个硬盘」）。
- `GET /api/nas/browse` 支持 `files=1&ext=js,mjs` 额外列普通文件（默认不列，避免放大 JSON）

### 规则四：探测音源要分清「handler 注册」和「初始化完成」

音源脚本是**两段式**启动的，这两件事之间有真实的时间差，而且**不等长**（脚本要拉远程配置）：

- `handler` 注册 —— `lx-runtime.js` 的 `hasHandler()` / `waitHandler()`（默认 2s）能等到；
- 初始化完成 —— 只有脚本自己 `send('inited')` 之后 `sourceStatus` 才有记录，`getPlatforms()` 之前返回 `[]`。

- ⚠️ **handler 注册 ≠ 初始化完成**（v2.1.58 修）：导入后立刻测试，会正好撞进初始化窗口，
  脚本自己报「服务初始化中，请稍后」，**好音源被判成不可用**。所以 `probeSource` 失败且平台列表为空时，
  先 `await lxRuntime.waitInited(id, 5000)` 再退避重试（`SourceManager.vue` 的 `PROBE_NOT_READY_RETRIES` / `PROBE_INIT_WAIT_MS`）。
- ⚠️ **`waitInited` 只能用在「探测已经明确回了没就绪」之后**：少数脚本压根不发 `inited`，
  当前置门用会白等到超时。`waitInited` 对服务型音源（`apiSources`）直接返回 true。
- 判据集中在 `frontend/src/engine/source-error.js` 的 `isNotReadyError(raw)` ——
  它**只决定要不要重试**，展示给用户的仍是原始报错（不翻译）。

### 规则五：`probeSource` 的 `error` 已经翻译过，别再翻译一遍

- `lx-runtime.js` 的 `probeSource()` 返回 `{ error, rawError }`：`error` 是 **`explainSourceError` 处理过的文案**，
  `rawError` 才是上游原话。
- ⚠️ **调用方要翻译就用 `rawError`**（v2.1.58 修）：`SourceManager.vue` 一度对 `res.error` 再调一次
  `explainSourceError`，结果规则命中已翻译文案里的「HTTP 502/503」→ 报错**套娃**
  （`API 网关错误（…（原始报错：…（原始报错：Request failed with status code 502）））`）。
- `explainSourceError` 现在是**幂等**的：文案里含「（原始报错：」这个我们自己的标记就原样返回。
  加新规则时别把这个短路删掉。

### 规则六：音乐入口接管是**可选**能力，默认关闭

`pkg/takeover` 会 listen 飞牛官方音乐的 Unix socket（`/var/run/trim_music.socket`），
把官方后端挪到 `trim_music_upstream.socket`，除自己的探活端点外**全部透传**。

- **默认关闭**：环境变量 `QULV_TAKEOVER=1` 才启用。开启后本应用成为官方音乐的必经之路 ——
  进程出问题音乐就打不开，这个代价必须由用户显式选择。
- 相关环境变量：`QULV_TAKEOVER`（开关）、`QULV_TAKEOVER_TARGET` / `QULV_TAKEOVER_UPSTREAM`（路径覆盖）、
  `QULV_TAKEOVER_TRACE`（未拦截端点的采样日志）。
- **这些变量写在哪**：`/vol1/@appdata/<appname>/.env`（即 `/var/apps/yinshu-ai/var/.env`，
  后者是前者的软链）。飞牛没有「给应用设自定义环境变量」的界面，`cmd/main` 在启动前
  `set -a; . "$VAR_DIR/.env"` 把它读进来 —— 所以**这个文件是应用唯一的环境变量入口**，
  加新的开关时不用改别的地方。见 §3.6「环境变量入口」。
- 别在没接管的情况下假设「官方接口的调用方只有官方界面」——接管之后所有官方客户端请求都会经过本进程。
- 接管层的完整约束（身份只信内核、只做原子替换、先 journal 再动 socket、拓扑不明就拒绝、
  与别的扩展共存时让路）见 §3.6，**改这个包之前必须读完那一节**。

**⚠️ 已决定但尚未实现的破例**：用户 2026-10-05 决定，洛雪（lx）音源脚本的**取直链**要改由
「服务内起 Node 子进程执行」完成，而不是现在的浏览器执行。这会与规则一正面冲突
（后端不内置、不执行任何第三方 JS），实现时必须同步改写规则一的措辞与 `pkg/protocol` 顶部注释，
并在 README 免责声明里说清「脚本由使用者提供、在服务端沙箱内执行」。**当前版本没有实现这一点。**

---

## 3. 代码地图

### 3.1 后端 `backend/pkg/`

**共 32 个包，70,066 行**（生产 42,711 + 测试 27,355，985 个测试函数；`main.go` 已含在生产里）。下表行数**含测试**，
按 **2026-09-26 实测**更新（**改完代码记得复测，这张表很容易腐化** ——
一次审计就查出 `account` / `charts` / `api` 三处与文档差了一大截；
2026-09-26 这轮又一次性同步了 `search`/`complete`/`nas`/`tags`/`downloader`/`match`/`config` 七处，
随后补上 `charts`/`library`；第八条落地时**新增 `dirwalk` 行**，并同步 `library`（遍历本体搬走后只剩 75 行的适配层）与 `nas`）。
复测命令：`bash scripts/audit-numbers.sh`；静默失败点复测：`python3 .dev/silent-audit.py`。

| 包 | 行数 | 职责 | 关键点 |
|---|---:|---|---|
| `api` | 8734 | 路由、**接口目录**、中间件、飞牛/账号/推送/协议 HTTP 层 | `catalog.go` 是接口文档**单一数据源**，且**面向 AI 按任务组织**（顶层 `guide` + `role` 分层，见 §6 / §6.1）；⚠️ 改接口要按 §6.0 三步走：**路由只挂一处（`routes.go` 的 `routeTable`）**，再登记目录；源码↔路由的对账由 `catalog_routes_test.go` 钉着，`.dev/catalog-audit.mjs` 只核**线上**那一版；服务型音源取链在 `monitor_dispatcher.go`（含**多候选换源**，见 §4.1.8）；补全的字段勾选、缺口统计与**可停止的后台任务**在 `library_complete.go` / `features.go`（见 §4.13、§11 ㊼ 块八）；**鉴权按来源地址分三层**（`none`/`lan`/`token`，`middleware.go` + `middleware_test.go`，见 §7 安全环境变量与 §11 ㊼ 块七） |
| `account` | 4672 | **第三方平台账号**：网易云 + QQ 音乐扫码登录、日推、歌单 | 凭据 0600 落盘；见 §3.2；QQ 歌单详情 **v8 优先 + 老接口兜底**（见 §11 ㉘） |
| `charts` | 3170 | 排行榜与歌单聚合（**官方协议**的一部分） | `qq_playlist.go` 是 QQ 公开歌单的**唯一**抓取实现（`push` 也复用它），见 §11 ㉘；⚠️ **拉不到 ≠ 空的**：榜单翻页失败会 502 或留 warn，绝不静默回空榜单（见 §11 ㊼ 块九） |
| `monitor` | 3010 | **歌单监控**（发现→过滤→增量→下载决策） | 有「首轮基线」机制，见 §4.1；PATCH 是**真局部更新**，加 bool 字段别用 `!=`（见 §11 ㉕）；**`HandleRun` 会拒绝已停止的监控、`RunOnce` 每轮复查 enabled**（见 §4.1.8） |
| `nas` | 3006 | 目录扫描、音频串流、标签读写、**路径守卫**、扫描结果缓存 | `pathguard.go` 是所有文件访问的闸门；`ScanSongs` 已改用 `dirwalk`（与索引同一套枚举规则），响应带 `truncated`/`warnings`，**缓存命中也照带**（见 §11 ㊼ 块十） |
| `push` | 2291 | **推送同步**：账号歌单/日推 → 飞牛音乐歌单，含任务存储与定时调度 | 见 §3.2；`source_link.go` 的 QQ 抓取已改为复用 `charts`（见 §11 ㉘） |
| `ai` | 2000 | **OpenAI 兼容客户端** + 4 个业务用法（判名/挑歌/核验歌词/生成正则） | **见 §3.5 与 §4.2** |
| `intercept` | 9333 | **飞牛官方页面的接管层**：socket 接管、`/music/api/v1/*` 拦截与合并、虚拟 id 与虚拟歌单、注入 UI 的同源通道 | 见 §11 相关条目与 `docs/变更记录/2026-10-06.md` turn 62–96；⚠️ **这个包在 2026-10-06 被误删过约 350 行**（补丁漏了 `s[m.end():]`），已重建 —— 重建靠 `intercept_test.go`（47 KB）当验收网，见 turn 94。核心文件：`intercept.go`（`Handle`/`buildRoutes`/`route`/`forward`/`upstreamJSON`/`userKey`/`credentialFingerprint`/`adoptLegacyUserKey`/JSON 小工具）、`stream.go`（取流 + tee）、`tee.go`（边听边下）、`autodownload.go`（收藏/歌单触发的后台下载 + `downloadCandidates` 逐候选重试）、`acquire.go`（下载源池：`rankedSources`/`betterCandidate`/`durationMatches`）、`qulvbridge.go`（`/music/_qulv/*` 同源通道）、`favorite.go`/`playlist.go`/`history.go`/`lyric.go`/`metadata.go`/`cover.go`/`search.go`/`vplaylist.go`/`vchart.go`/`pageshell.go`/`lookup.go`、`assets/ui.js`（注入脚本，含 `describeSource`） |
| `online` | 3641 | 在线音源抽象：解析池（`resolve.go`）、已下载登记表（`downloaded.go`）、musicdl sidecar 客户端（`musicdl.go`） | `DownloadedItem` 带 `Source`/`Quality`（**实际取音**，可能已被下载源池换源）与 `Size`；`Put` **只登记真的存在的文件**；`downloaded.json` 是「收藏自动绑定本地」的落点 |
| `search` | 4261 | 多平台搜索聚合与去噪、**真实音质解析**、**音质查询接口**、**QQ 专辑详情（风格/年份）** | 全文零引用音源 → 免脚本可用；§5.7 / §5.8；`album_qq.go` 是**唯一的真实 genre 来源**（四家的搜索响应都不含 genre，见 §4.13） |
| `fnos` | 1881 | **飞牛集成**：官方开放 API + 音乐应用接口 + 令牌读取 | 见 §3.2；重扫节流在 `rescan.go`（**debounce 30s / 最小间隔 10min**，`lastRun` 落盘，见 §4.11） |
| `sources` | 1868 | 音源脚本管理 + 批量导入/导出/清理/删除 | `batch.go` / `manage.go` 见 §3.2 |
| `tags` | 2123 | 音频标签读写（MP3 = ID3v2.3，FLAC = Vorbis + PICTURE） | ⚠️ **`Write` 是合并语义**：没传的字段先从文件读回来再写（`mergeTextFields`）。两种容器的底层策略都是「删掉受管字段重建」，不合并就会**静默抹掉**没传的字段（见 §4.13）。封面另有各自的保留逻辑 |
| **`complete`** | 2842 | **主动补全流水线**：挑活 → 判名 → 搜索匹配 → 歌词核验 → 写标签 + **备份/撤销** | **见 §3.5**；`Resolve`/`Execute` 两阶段；**六个字段逐个可勾**，值只取「已匹配上的那条命中」（见 §4.13）；`ctx` 取消 = **不再派发新曲目**，被停止的曲目单记 `Cancelled`（绝不混进 `no_match`/`failed`，见 §11 ㊼ 块八） |
| `downloader` | 1863 | 下载落盘（断点续传、错误页拦截、歌词内嵌、封面内嵌） | 见 §4.3 的两个已修 bug；**单曲内部不做重试**，换源由上层负责（见 §4.1.8）；封面抓取失败会**记日志**（见 §4.11） |
| `tidy` | 1571 | **刮削整理 + 双通道去重** | ⚠️ `TidyOne` **不下载封面**，见 §4.8；绝不写目录级 cover.jpg；`Item` 支持年份/曲序/光盘/风格（见 §4.13） |
| **`nameparse`** | 1048 | **文件名解析规则层**：内置启发式 + `Suspect` 标记 + 用户自定义正则 | **见 §3.5**；改之前必读 |
| `library` | 1313 | **增量索引 + 枚举完整性安全门 + 字段缺口统计**（遍历本体已搬到 `dirwalk`，这里只做映射） | 见 §4.4、§5.4、§11 ㊼ 块十；⚠️ 「标签读没读过」的判据是 `Entry.TagReadAt`，**不再是「标题为空」**（那样新增字段永远补不上，见 §4.13）；⚠️ 缺口统计把**写不了标签的格式**（非 MP3/FLAC）单列成 `unsupported`，既不算待补也不算待读（见 §4.13） |
| **`dirwalk`** | 625 | **目录遍历的唯一实现**：排除目录 / 深度上限 / 软链接环剪枝 / 协作式取消 + 审计计数 | 第八条新增（见 §11 ㊼ 块十）；曲库索引与 NAS 浏览**共用这一份规则**；「到上限」「读不了目录」「跳过几条」全进 `Stats`/`warnings`，不再静默 |
| `apisource` | 785 | 服务型音源（中转）的协议探测与调用 | **支持内网/回环地址**（v2.1.12 起，用户显式配置的目标，见 §8.2） |
| `bridge` | 645 | **AI 解析桥**（外部调用者 ↔ 浏览器） | 见 §4.1 |
| `match` | 721 | 版本识别（Live/Remix/DJ…）与匹配打分 | 见 §5.1；飞牛推送的曲目匹配也复用它 |
| `security` | 594 | SSRF 防护、代理分流、响应体限额、**安全的远程图片下载**（`image.go`） | 见 §4.2、§4.8 |
| `config` | 636 | 配置读写 + **窗口记忆持久化**（`ui_prefs.go`） | 见 §3.3 |
| `applog` | 348 | **后端运行日志**（环形缓冲，供 `/api/logs`） | `source` 字段区分 backend/frontend |
| `protocol` | 252 | **音源协议模型**：官方协议 / LX 协议的能力边界 | 见 §3.4；纯数据 + 单测守着边界 |
| `downloadqueue` | 252 | **下载队列落盘**（NAS 上的权威副本，浏览器 localStorage 只当缓存） | v2.1.17 新增；**不解释任务字段**（`[]json.RawMessage`），前端改字段不用动后端 |
| `proxy` | 188 | HTTP 代理（供 lx 脚本跨域取数） | |
| `audioext` | 148 | **音频扩展名唯一清单**：`Exts`（14 种，能扫/能播）/ `Writable`（mp3+flac，能写标签） | v2.1.30 新增（见 §11 ㊼-④）；⚠️ 两组**别混用**：能扫 ≠ 能写，混了「补全成功」就是空话（见 §4.13） |

### 3.2 四个新增能力模块（2026-09-16）

#### `pkg/fnos` —— 飞牛集成（1759 行）

**两套飞牛接口，认证方式不同，极易踩坑**：

| | 官方开放 API | 音乐应用接口 |
|---|---|---|
| 文件 | `openapi.go` | `music.go` |
| Socket | `/var/run/trim_open_gateway_apiscope.socket` | `/var/run/trim_music.socket` |
| 基地址 | `http://localhost/api/v1/trimapp` | `http://localhost/music/api/v1` |
| 认证 | **`Bearer <TRIM_API_TOKEN>`** | **裸令牌，无 Bearer 前缀** |
| token 来源 | 系统自动注入环境变量 | 读飞牛音乐库（`token.go`） |
| 响应 | `{reqId, code, msg, data}` | `{code, msg/message, data}` |

- `openapi.go`：`Available()` / `Call()` / `SharedFolders()` / `DelSharedFolder()` / `PlatformConfig()`
- `music.go`：`Music` 客户端 —— `Me` / `Playlists` / `SearchTracks` / `PlaylistTracks` / `CreatePlaylist` / `FindOrCreatePlaylist` / `AddTracks` / `SetPlaylistCover` / `ScanLibrary`
- `token.go`：`MusicToken()` —— 环境变量 `FNOS_TOKEN` 优先，否则**只读**打开 `/var/lib/fnos-music-db/music.db` 取 `user_token` 表最新非空令牌（用 `modernc.org/sqlite`）

**关键设计**：
- `FindOrCreatePlaylist` 遇同名多个歌单**直接报错**，不随便挑一个（防误推）
- `AddTracks` 自动分批，每批 50
- 所有读库/连接失败路径**优雅降级**，非飞牛环境下不影响任何其他功能

#### `pkg/account` —— 第三方平台账号（2526 行）

**网易云**（`netease.go`）：全部走 legacy `/api/` 路径，**不需要 weapi 加密**
- `NeteaseQRKey()` / `NeteaseQRCheck(key)` —— 扫码（状态码 800过期/801待扫/802待确认/**803成功**）
- `NeteaseAccount(cookie)` / `NeteaseDailySongs(cookie)` / `NeteaseUserPlaylists(cookie, uid, limit)`

**QQ 音乐**（`qq.go`）：移植自 fnmusic-flow 内置的 pockettune-server
- 扫码 5 步：`ptqrshow` → `ptqrlogin`（`ptqrtoken = hash33(qrsig)`）→ 跳转取 `p_skey`
  → `graph.qq.com/oauth2.0/authorize`（`g_tk = hash33(p_skey, 5381)`）→ `musicu.fcg` 换凭据
- `QQDaily(cookies)` —— 日推（走 `req_0` 而非 `request`，comm 用网页版参数）
- `QQPlaylists(cookies)` —— 自建（`fcg_user_created_diss`）+ 收藏（`fcg_get_profile_order_asset`），单侧失败不影响另一侧
- `QQPlaylistDetail(cookies, id)` —— 详情（**返回 JSONP，需剥 `jsonCallback(...)` 包装**）

**存储**（`store.go`）：JSON 文件，**权限 0600**；`List()` 会抹掉 Cookie，避免凭据经接口外泄

#### `pkg/push` —— 推送（飞牛歌单方向）

**「推送」是一件事，不是两件。** 见下方「推送的统一模型」。

| 文件 | 职责 |
|---|---|
| `push.go` | `PushTracks(m, tracks, opts)` —— 在飞牛曲库匹配 → 写入歌单（**单次推送与定时任务共用这一份逻辑**） |
| `source.go` | `FetchSource(store, provider, kind, sourceID)` —— 从账号拉取源曲目，统一成 `Track` |
| `store.go` | 任务与执行历史的 JSON 持久化（`push_tasks.json`） |
| `runner.go` | `Execute(task, trigger)` + 定时调度（每分钟检查到点任务） |

**关键设计**：
- **`FnosGUID` 是稳定身份**：首次推送成功后记住飞牛歌单 guid，后续复用它。
  绝不退回按名称匹配 —— 否则歌单改名或出现重名时会建出第二个歌单
- **匹配复用 `pkg/match` 的 `Similarity` + `MatchMinScore`（0.72）**，**宁缺毋滥**
- **匹配阶段并发执行，并发上限 `matchConcurrency = 6`**（2026-09-17 改）。
  每首歌要打 1~2 次飞牛搜索接口（先按歌名，未命中再按「歌手 + 歌名」），
  原先**串行** —— 一个 200 首的日推就是 ~400 次串行往返。
  改并发后结果仍**按输入顺序汇总**、GUID 去重语义不变（有单测 + `-race` 守着）。
  > 为什么不学 flow 的「一次性内存索引」：flow 是容器里挂了音乐目录、**扫文件系统**建索引；
  > 而我们的匹配必须拿到**飞牛曲目的 GUID**（`AddTracks(guid, trackGUIDs)`），
  > 那只有飞牛 API 能给。飞牛**没有「列出全部曲目」的接口**（见 `docs/飞牛音乐API.md`），
  > 所以无法在本地建一份带 GUID 的索引。
- 同名多个飞牛歌单时**直接报错**，不随便挑
- 加曲自动分批（每批 50），已在歌单中的曲目跳过
- 无论成败都写执行历史（含失败原因），最多保留 200 条
- **存储层不设业务默认值**（`Save` 不会把 `Enabled` 改成 true）——
  因为调度器回写也走 `Save`，强制启用会把用户禁用的任务重新打开；「新建默认启用」由 API 层负责
- ⚠️ **`FetchSource` 被 `GET /api/accounts/tracks` 复用**（见 §5.9）——
  改它的字段映射会同时影响「推送」与「双平台推荐」，别只顾一头

#### 推送的统一模型（2026-09-17 对齐 flow，重要）

**曾经的错误设计**：把「推送到飞牛歌单」和「下载到本地曲库」当成**两条并列的路**，
界面上做成两个平级 tab。结果是用户会问「这两个到底有什么区别」——
因为从用户视角看，它们确实是两条互不相干的链路，一条不新增音乐、一条新增。

**flow 的正确答案**（已从 `F:\down\fnmusic-flow-extracted\...\index.html` 逐行核实）：
它们不是并列关系，而是**同一条流水线的两个出口**。flow 的推送请求体就是证据：

```js
POST /api/fnos/playlists/{id}/append
body: { download_missing: true }          // ← 关键
→ { result: { added: N }, queue: { tasks_created: M } }
→ 提示：「已补录到原歌单：新增 N 首，新增下载 M 首」
```

```
订阅来源（歌单 / 日推）
      ↓
推送到飞牛 → 在飞牛曲库匹配
      ├── 匹配上   → 写入飞牛音乐歌单          (added)
      └── 匹配不上 → 自动加入下载队列 → 落盘到音乐文件夹 (tasks_created)
                                          ↓
                              落盘后下一轮推送即可匹配 —— 闭环
```

**我们的落地方式**（与 flow 的差别只在下载队列的位置）：

- flow 的下载队列在**后端**，一次请求就能顺带入队（`queue.tasks_created`）
- 我们的下载队列在**前端**（`downloadManager`），所以由**前端**补这一步：
  推送结果面板里，未匹配曲目旁有「**一键补下载这 N 首**」按钮，
  点击后逐首用 `SearchAPI.search(name + artist, 平台)` 搜到最佳结果，加入下载队列

因此界面上：
- **推送到飞牛歌单**：匹配入库；曲库里还没有的**可一键补下载**，落盘后下轮就能匹配
- **推送到本地曲库**：只下载、不建歌单（给「我只要文件」的场景）

两者都归在「曲库管家 → 推送」下（顶栏不再单列「推送同步」），
`catalog.go` 的分类名也统一为「推送 · 下载到曲库」/「推送 · 飞牛歌单」。

> 后端**没有** `DownloadMissing` 参数 —— 因为 `Result.Missing` 本来就已经返回给前端了，
> 补下载由前端驱动，不需要后端再造一套队列。

#### `pkg/sources` —— 音源批量管理（1679 行）

- `batch.go`：`HandleImportBatch` —— 在线 URL + 本地脚本混用批量导入
  - **ID 用内容 SHA-256 前 16 位**（不是时间戳），因此批量导入不会 ID 冲突，且天然可去重
  - 元数据（`@name`/`@version`/`@author`/`@description`）自动从脚本头部注释提取
- `manage.go`：`HandleBatchDelete` / `HandleExportSources` / `HandleCleanupSources`
  - 导出结果可直接喂给 import_batch 还原
  - 清理重复**默认 dry_run**

### 3.3 前端 `frontend/src/`

| 目录 | 内容 |
|---|---|
| `components/` | **25 个** Vue 组件（播放器、下载队列、曲库管家、接口抽屉、**推送同步、账号连接、扫码登录弹窗 `QrLoginModal.vue`**…） |
| `engine/` | `lx-runtime.js`（音源沙箱）、`quality.js`（音质阶梯） |
| `services/` | `downloadManager.js`（下载队列）、`sourceHealth.js`（熔断）、`bridgeWorker.js`（AI 桥）、`prefs.js`（偏好与缓存）、`libraryView.js` / `nasView.js`（**从组件里抽出来的纯逻辑**：状态项顺序与配色档、缺口/时间单位文案、补全标题与百分比、面包屑、截断提示）、`qrLogin.js`（**扫码登录的状态映射**：网易云 `code` / QQ `state` → 文案与 `done/saved`，见 §4.16） |
| `api/client.js` | 所有后端接口的封装（含 `FnosAPI` / `AccountAPI` / `PushAPI`） |

> **窗口点击记忆 = 服务端文件**：各页面选中状态（发现页 Tab、榜单/搜索平台、NAS 目录、
> 播放模式、当前音源）由 `prefs.js` 统一管理，持久化到服务端 `ui_prefs.json`
> （`GET/POST /api/ui-prefs`），跨浏览器共享、不因清缓存丢失；localStorage 仅作离线兜底。
> 一次性标记（`fn_disclaimer_accepted`）、熔断状态（`sourceHealth`）、下载队列
> （`downloadManager`）仍保留在 localStorage，不纳入。

> **前端怎么测**：`npm test` 就是 `node --test`（**没有 DOM 环境**，未装 jsdom/@vue/test-utils）。
> 所以能回归测的只有**纯逻辑**：组件里成段的判断（状态项顺序、缺口文案与时间单位、补全标题/百分比、
> 面包屑、截断提示）一律放 `src/services/*.js`，配套 `src/services/*.test.mjs`；组件只做「取值 + 渲染」。
> 写新的判断逻辑时照这个来 —— 埋在 `.vue` 里的分支没人能测到。

#### 3.3.1 统一页面外壳 `PageShell.vue`（**改任何页面排版前先读这一节**）

**为什么存在**：原先各页面各写一套外壳（`h-full flex flex-col overflow-hidden` + 全宽无最大宽度约束 +
`px-4 md:px-6` 各页不同），导致页面之间标题层级、左右边距、最大宽度全不一致，视觉上很散。
2026-09-16 参考 fnmusic-flow 的 `workspace` 布局抽出统一外壳。

**唯一的容器规范（改排版只改这一处）**：

```
外层：h-full overflow-y-auto bg-[#f5f7f9]          ← 唯一滚动容器，页面内容不需要再自己滚
内层：mx-auto w-full max-w-[1500px]
      px-5 py-6 pb-28 sm:px-8 sm:py-7               ← pb-28 给底部播放条留位
  标题区：h1 26px font-bold tracking-tight text-gray-800
          + desc 13px text-gray-500（max-w-4xl）
          + 右侧 actions slot（flex-wrap）
  指标行：metrics slot（grid 2 列 → md:4 列，卡片 rounded-xl border-gray-200 bg-white
          px-[17px] py-[15px]，数值 24px font-bold + 标签 12px text-gray-400）
  内容：默认 slot
```

**尺度依据（对齐 fnmusic-flow 的实测值，别凭感觉改）**：

| 元素 | flow 实测 | 我们取值 |
|---|---|---|
| 内容区 padding | `.workspace { padding:28px 34px 100px }` | `py-6/28px`、`sm:px-8/32px`、`pb-28/112px` |
| 内容最大宽 | `max-width:1500px` | `max-w-[1500px]` |
| 页面主标题 | hero `h1 { font-size:29px }` | **26px**（我们页面更密，略收） |
| 区块标题 | `.section-head h2 { font-size:19px }` | 19px（页面内区块用） |
| 顶栏高度 | `.topbar { height:68px }` | **68px** |
| 顶栏内边距 / 间距 | `padding:0 30px; gap:28px` | `px-4 sm:px-8`、`gap-4 sm:gap-7` |
| 品牌字号 | `.brand { font-size:20px; font-weight:800 }` | 20px / 800 |
| 分段切换器 | `.switcher { padding:4px; radius:11px; gap:3px }`，按钮 `padding:8px 18px` | `p-1 rounded-[11px] gap-0.5`，按钮 `px-3 sm:px-[18px] py-2` |
| 指标卡 | `.metric { padding:15px 17px; radius:12px }`，`b{font-size:24px}` | `px-[17px] py-[15px]`、`text-[24px]` |
| 底部播放条 | `.player { height:68px; padding:0 30px }` | 68px（已一致） |
| 主色 | `--blue:#3578f6` | **emerald**（有意保留品牌色，未跟随） |

> ⚠️ **最容易踩的坑：整体「缩一号」**。原先我们 h1 只有 19px、品牌 15px、顶栏 60px、内边距 16~20px，
> 结构是对的但尺度全面小于 flow，观感就是「排版不好看」。
> 改版式时请对照上表，**别再把字号/留白改小**。

**用法**：

```vue
<PageShell title="曲库管家" desc="定时监控歌单自动下载新增曲目…">
  <template #title-extra><span>…小徽章…</span></template>
  <template #actions><nav>…分段切换器 / 主按钮…</nav></template>
  <template #metrics><div class="grid grid-cols-2 gap-3 md:grid-cols-4">…</div></template>
  <div class="space-y-3">…内容…</div>
</PageShell>
```

**当前采用情况（18 个组件）**：

| 状态 | 组件 |
|---|---|
| ✅ 用 `PageShell` | `SearchView`(见下注)、`NasExplorer`、`LibraryManager`、`SourceManager`、`DownloadQueueView`、`AccountManager` |
| ⬜ 不适用 | `ApiConsole`（在侧边抽屉 `ApiDrawer` 里，属面板语境不是页面）、`ImmersivePlayer`、`PlayerBar`、`DownloadQueuePanel`、`DownloadDrawer`、`LyricView`、`AudioVisualizer`、`VinylTurntable`、`DisclaimerModal` |

> **`SearchView` 是唯一例外**：它是「搜索优先」布局（顶部搜索条 + 结果区），套 `PageShell`
> 会让搜索条跟着滚走。因此**不套组件，但严格复制容器规范** —— 顶部条与结果区都改成
> `max-w-[1500px] mx-auto px-5 py-6 pb-28 sm:px-8 sm:py-7`，标题同样 26px，保证与其它页面像素级对齐。
> 改它的时候别用别的 max-width（历史上是 `max-w-4xl` 搜索条 + `max-w-5xl` 内容区，两边不齐）。
>
> 它内部还有一个 **hero 双列头部**（渐变欢迎卡 + 深色「正在播放」卡），对齐 flow 的 `.hero`，
> 仅在 `discover` 模式显示。相关 props：`currentSong` / `isPlaying`；emits：`prev` / `next` / `toggle-play`。

**分段切换器（flow 的导航特征，已统一）**：灰底容器 + 白色胶囊高亮

```html
<nav class="flex items-center gap-0.5 rounded-[11px] bg-gray-100 p-1">
  <button :class="active ? 'bg-white text-emerald-600 shadow-sm' : 'text-gray-500 hover:text-gray-700'">…</button>
</nav>
```

`LibraryManager` 的左侧栏三项（推送 / 下载 / 补全整理）用的就是这一套，**不要退回原来的
`bg-emerald-500 text-white` 实心按钮**（那是旧风格，与顶栏导航不一致）。

> ⚠️ **例外**：`LibraryManager` 已改为**左侧栏分组导航**（见下），不再用页内分段切换器；
> 但「下载」页内部确实是两个子视图切换（队列 / 歌单下载），见 §4.14。

**状态横幅 + 主按钮必须同一行**（`PushManager` 的飞牛状态条就是这条）：

```html
<div class="mb-4 flex items-stretch gap-3">
  <div class="min-w-0 flex-1 rounded-xl border px-4 py-3 text-[12px] leading-relaxed">…提示文案…</div>
  <button class="flex shrink-0 items-center justify-center rounded-lg bg-emerald-500 px-4 …">+ 新建同步任务</button>
</div>
```

要点：横幅 `flex-1 min-w-0`（占剩余宽度）、按钮 `shrink-0`（不被压扁）、容器 `items-stretch`
（横幅因文案换行变高时**按钮跟随拉伸**）。**不要**把按钮单独放一行 `justify-end` ——
会变成「按钮浮在提示条上方」的错列（2026-09-18 修过一次，见 §11 ⑥）。

#### 3.3.2 左侧栏分组导航（对齐 flow 的 `download-shell`）

**适用场景**：页面内有**多个功能区块**、且区块本身还有分组时，用左侧栏比页内 Tab 更清晰。

flow 实测：`.download-shell { grid-template-columns:220px 1fr; gap:20px }`，
侧栏 `.download-nav { background:#fff; border-radius:15px; padding:12px; position:sticky; top:88px }`，
分组标题 11px 灰、条目 `padding:10px`、选中 `bg:--blue2 + color:--blue + font-weight:700`、
角标 `margin-left:auto; border-radius:99px; padding:1px 7px; font-size:11px`。

我们的实现（`LibraryManager.vue`）：

```html
<div class="grid grid-cols-1 gap-5 lg:grid-cols-[220px_1fr]">
  <aside class="h-max rounded-[15px] border border-gray-200 bg-white p-3 lg:sticky lg:top-6">
    <h4 class="mx-2.5 mb-3 mt-2 text-[11px] font-medium text-gray-400">曲库管家</h4>
    <button v-for="t in tabs" :class="[
      'flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2.5 text-left text-[13px] transition-colors',
      activeTab === t.id ? 'bg-emerald-50 font-bold text-emerald-600' : 'text-gray-600 hover:bg-gray-50'
    ]">
      <component :is="t.icon" class="w-4 h-4 shrink-0" />
      <span class="min-w-0 flex-1 truncate">{{ t.name }}</span>
      <span v-if="t.count" class="shrink-0 rounded-full bg-gray-100 px-1.5 py-0.5 text-[11px] text-gray-500">{{ t.count }}</span>
    </button>
    <h4>任务状态</h4>
    …带角标的状态条目…
  </aside>
  <main class="min-w-0 space-y-3">
    <!-- 指标行放主区内（flow 的 .metric-row 也在 .download-main 内），与内容左对齐 -->
    <div class="grid grid-cols-2 gap-3 md:grid-cols-4">…</div>
    …各区块内容…
  </main>
</div>
```

**要点**：
- 侧栏 `lg:sticky lg:top-6`，`lg` 以下自动退化为上下堆叠
- **指标行要放在 `<main>` 里**，不要放 `PageShell` 的 `#metrics` slot —— 否则会横跨侧栏、与内容不对齐
- 主区必须 `min-w-0`，否则长内容会把网格撑破
- 侧栏条目用 `component :is="t.icon"` 渲染图标（`tabs` 是带 `icon`/`count` 的 computed）
- 选中态用 emerald（flow 用蓝），保持品牌色

> **不建议强行对齐的部分**：flow 的「下载与曲库」把 下载/搜索/歌单/曲库/推送 **合并成一页**，
> 我们是**拆成多页 + 顶栏导航**。这是产品结构差异，不是排版差异，别为了像 flow 而合并页面。

**导航结构**（2026-09-16 重排，参考 fnmusic-flow 的工作台布局）：

```
顶栏 68px：[品牌] [分段切换器] ……………………………………… [当前音源] [开放接口] [播放器]
分段切换器：发现音乐 · 本地音乐 · 曲库管家 · 账号连接 · 日志
```

- **分段切换器**（灰底容器 + 白色胶囊高亮）是 flow 的导航特征，已采用
- **「音源管理」不在切换器里** —— 它是配置项，收进右侧的「当前音源」按钮
- 主色保持项目的 **emerald**（未改成 flow 的蓝色，以保留品牌识别度）

**两个新页面**：
| 组件 | 对应功能 |
|---|---|
| ~~`PushManager.vue`~~ | **已于 v2.1.16 并入 `LibraryManager.vue` 并删除**（文件备份在 `.workbuddy-ai/tmp/removed/`）。原因见 §4.1.5 |
| `AccountManager.vue` | 账号连接：两个平台卡片（品牌色顶边）、扫码弹窗（网易需前端渲染二维码，用 `qrcode` 包）、每日推荐/歌单速览 |

> ⚠️ 前端引入新依赖 `qrcode`（把网易返回的二维码内容渲染成图；QQ 本身直接返回图片）。

---

#### 3.3.3 文案与报错口径（**改任何提示文案 / 错误处理前先读这一节**）

用户 2026-09-18 定的两条规矩，**全项目适用**。

**① 界面不写「工作原理」，只写「你该怎么做」。**

用户原话：「我觉得不应该直接显示工作原理等提示，而是用户应该怎么做」。

反面教材（改造前真实存在，`LibraryManager` 本地曲库侧）：

> 工作原理：后端不主动抓歌单（音源脚本只能在浏览器执行），因此需要网页端或外部 AI 先
> `POST /api/monitors/{id}/discover` 上报曲目…

—— 把接口路径和内部机制直接摆给了普通用户。改后：

> 怎么用：点右侧「新建推送任务」，粘上歌单链接即可。建好后到「发现音乐」打开同一个歌单
> （或在「歌单下载」里解析后点「订阅更新」），曲目才会登记进来。**第一次只登记不下载**，
> 从第二轮起自动下载新增曲目。

**② 报错只留一句人话，技术细节一律进日志。**

用户原话：「报错信息应该放在日志里给用户查」。三层实现：

| 层 | 文件 | 职责 |
|---|---|---|
| 日志服务 | `frontend/src/services/appLog.js` | 本地留存（localStorage 环形缓冲 200 条，刷新/重启不丢）+ 上报后端 `POST /api/logs` |
| 文案翻译 | `frontend/src/services/userMsg.js` | `fail(scope, err)` / `apiError(res)` → 一句人话；`describeError(err)` → 完整技术描述（**只进日志**） |
| 统一呈现 | `frontend/src/components/InlineNotice.vue` | 一句原因 + 「查看日志」入口（经 `services/navBus.js` 直达日志页，免去给 14 个组件加 emit） |

**后端**：`applog.Entry` 带 `source` 字段（`backend` / `frontend`），前端上报经
`POST /api/logs` 汇进同一个环形缓冲 —— 排查一次操作失败时，服务端与浏览器两侧的信息
落在同一条时间轴上。日志页「运行日志」标签按来源打标，「操作日志」标签读本机留存那份。

> ⚠️ **前端本地留存刻意不走 `services/prefs.js`**：prefs 会同步到服务端的
> `ui_prefs.json`，而那个文件有 **64KB 总上限**（`backend/pkg/config/uiprefs.go` 的
> `MaxUIPrefsBytes`）。几十 KB 的日志塞进去会挤占其他窗口记忆的预算。

**有意保留、不适用这条规矩的地方**（改动前先想清楚）：

- **合规 / 免责声明** —— `SourceManager` 的「技术中立免责声明」、`DisclaimerModal` 全文。
  法律文本，必须原样呈现。
- **协议能力清单**（`SourceManager` 协议卡片）—— 用户要据此做选择；已改成「你得到什么」
  而非「内部怎么实现」。
- **开放接口抽屉 `ApiConsole`** —— 面向外部 AI / 开发者的调试台，原始报错在那里是有效信息。
- **状态类提示**（飞牛未连接、未导入音源）—— **保留**，但统一改成「状态 + 该去哪」，
  例如「飞牛音乐未连接，暂时不能新建同步任务。请到「账号连接」登录飞牛音乐 [去登录]」。

**判断标准**：这句话用户看完**能不能采取行动**？不能 → 它属于日志，不属于界面。

> ⚠️ **后端也会犯这个错**。`fnos.UnavailableReason()` 曾经把「逐条库路径检查结果 +
> socket 路径 + 环境变量名 + 接口地址」拼成一大段塞进界面。现已拆成两层：
> `UnavailableReason()` 一句话给界面，`UnavailableDetail()` 逐项诊断给日志
> （见 `backend/pkg/api/fnos.go` 的 `log.Printf`）。

**③ 改一个用户可见术语时，前端改完必须 `grep` 整个仓库**（2026-09-21 踩到，只改前端 = 改了一半）。

后端有很多字符串**会出现在界面上**，但它们不在 `.vue` 里，很容易被漏掉：

| 后端产物 | 用户在哪看到 |
|---|---|
| 运行日志行（`run.Log`、`logLines`） | 日志页「任务日志」 |
| 干跑 / 预览的 `Warnings[]` | 新建订阅时的警告条 |
| `Track.Error`（每首歌「为什么没下载」的原因） | 曲目列表里逐条显示 |
| `push.Result.Missing[].Reason` | 「飞牛缺 N 首」的原因 |

**做法**：`grep -rn "<术语>" --include="*.go" --include="*.vue" --include="*.js"` →
**逐条判断是「注释」还是「用户能看到的字符串」**。注释不用改（进不了二进制，
`strings`/`grep` 在包里查不到）；其余全改。**否则界面上同时出现两套叫法，比不改更糟。**

实例：把「待建基线」改成「还没开始追新」时，只改了前端 —— 结果后端还在往
任务日志和 `Track.Error` 里写「首轮基线」，一个界面两种说法。四处后端字符串一并改掉才收口。

**④ ⚠️ 别写「网页端」「保持网页打开」这类词**（2026-09-22 教训）。

用户在飞牛里看到的是一个 **App 窗口**，不是「网页」—— 飞牛是用 iframe 打开我们的
（见 §4.1.7 的表）。他原话：「我这个是飞牛 app。网页开着什么意思。飞牛里应用启动算吗？」
**一句本来想帮忙的提示，反而变成了新的困惑。**

**统一说「曲率 页面」**。写涉及运行环境的文案前，先问一句「**用户眼里的它叫什么**」。

（`ApiConsole` 里讲**接口机制**的那些「网页端」保留 —— 那是给调用 API 的 AI / 开发者看的，
本节已把调试台列为有意保留技术表述的例外。但讲「要不要开着页面」的那两句已统一。）

---

#### 3.3.4 移动端约定（**改任何布局 / 触控交互前先读这一节**）

目标场景：用户在**飞牛 App 里用手机**（iOS / Android WebView）打开曲率。
桌面浏览器也仍在用 ⇒ **任何移动端改动都不许动桌面观感**（本节的写法都是增量：桌面命中的规则一个字没变）。

**① 判定断点一律用 `@media (hover: none) and (pointer: coarse)`，不用屏宽。**

「真的用手指点」这个条件同时覆盖手机竖屏 / 横屏 / 平板 / 触屏笔记本；
而桌面浏览器即使把窗口拖到 390px 也**不会**被误伤（窄桌面窗口仍然该有 hover、悬停提示、细滚动条）。
整份移动端基础层只有这一个媒体查询（`frontend/src/style.css` 末尾）。

**② 三层基础（全在 `frontend/src/style.css`）**

| 层 | 内容 |
|---|---|
| `:root` | `--safe-top / --safe-bottom / --safe-left / --safe-right` = `env(safe-area-inset-*, 0px)` |
| `@layer base` | `.app-viewport`：`height:100vh; height:100dvh`（先 vh 兜底老 WebView，再 dvh 覆盖） |
| `@layer utilities` | `.pt-safe/.pb-safe/.pl-safe/.pr-safe`、`.no-scrollbar`、`.tap-target`（44px 命中区伪元素） |

粗指针媒体块里另有三个显隐工具类：`.show-on-touch`（`opacity:1`）、`.show-on-touch-flex`、`.hide-on-touch`。

**③ ⚠️ 两条铁律（都是踩过的坑）**

1. **安全区挂外层、真实内边距挂内层。**
   `.pt-safe` 和 `pt-2` 是同一个 CSS 属性，而自定义工具类排在核心工具类**之后**
   ⇒ 挂同一元素时 `pt-safe` 会把 `pt-2` 顶掉，**内边距凭空消失**。
   顶栏（`App.vue`）、底部播放条（`PlayerBar.vue`）、沉浸页上下栏（`ImmersivePlayer.vue`）、
   两个抽屉（`ApiDrawer` / `DownloadDrawer`）都是「外层吃安全区 + 内层放内边距」的结构。
   固定高度同理：`h-14` / `h-20` 会被安全区撑破，用 `min-h-14` / 内边距撑。
2. **根高度不许用 `h-screen` / `100vh`。**
   手机浏览器地址栏会吃掉 100vh 的一部分 ⇒ 底部播放条被顶到可视区之外（露一半或整条看不见）。
   根容器用 `.app-viewport`（100dvh）；也不要 `w-screen`（iOS 上会横向溢出），用 `w-full`。

**④ 触控可用性下限：命中区 ≥ 30px（理想 44px）。**

- 图标按钮在手机上统一放大到 `p-2 sm:p-1`（桌面回到原样）。
- **视觉只有 6px 的进度条**（`PlayerBar.vue` / `ImmersivePlayer.vue`）用 `before:` 伪元素上下各撑 12px：
  伪元素属于本元素 ⇒ 点击仍然命中它、`clientX` 换算进度也不受影响（视觉粗细不变）。
- `.tap-target::after` 是给**孤立**按钮用的 44px 命中区。**相邻按钮不要用** ——
  命中区会互相重叠，边界上点「上一首」可能命中「下一首」。

**⑤ 触屏没有 hover：`group-hover:` 出现/切换的东西必须给出触屏答案。**

要么常显（`.show-on-touch` / `.show-on-touch-flex`），要么常隐（`.hide-on-touch`）。
实例：歌曲列表行（`SearchView.vue`）桌面是「悬停把序号换成播放图标」，触屏永远是序号 ⇒
用户看不出这一行能点。触屏改成常显播放图标（序号在手机上本来也没用），桌面行为不变。
📌 **反例（有意保留）**：发现页封面上的 `opacity-0 group-hover:opacity-100` 播放角标**没有**改成触屏常显 ——
卡片本身可点，而常显会在每个封面上压一个绿色圆点，那是纯视觉噪音（用户明确反感界面噪音）。

**⑥ 顶栏（`App.vue`）在手机上必须单行 —— 靠「去掉文字、只留图标」腾地方。**

品牌 + 5 个导航项 + 右侧 3 个入口本来就有 460px+，而原实现是 `whitespace-nowrap + overflow-hidden` 的单行
⇒ 手机上「音源 / 开放接口 / 打开播放器」三个入口**直接被裁到屏幕外、点不到**。

**❗ 不要用折行解决 —— 用户 2026-09-26 明确否掉了第一版的两行方案**，
原话：「顶部菜单显示不全的话，可以不显示文字啊，**不要两行**」。
所以手机上：导航项只留图标（名称靠 `sm:inline` 才出现，`p-2.5` + 14px 图标 = 34px 一个）、
开放接口 / 打开播放器本来就只在手机显示图标、音源按钮的文字截到 `max-w-[56px]`。
导航容器挂 `min-w-0 overflow-x-auto no-scrollbar`：极端窄屏（≤360px）时它自己横向滚动 ——
**滚动是最后一道保险，不许再回到「被裁掉」**。
凡是手机上只剩图标的按钮，必须给 `:title` + `:aria-label`（没有文字标签，图标得自己说清是什么）。

**❗ 桌面端一个字都不许改** —— 用户 2026-09-26 那条指令的第一句就是「**手机适配为图标，电脑端不变，就按之前**」。
`sm:` 以上保持原样：`h-[68px]` / `gap-7` / `px-8` / 图标 + 名称 / `whitespace-nowrap` / `overflow-hidden`。
新加的类一律只挂在 `sm:` **以下**才生效（`p-2.5 sm:px-[18px] sm:py-2`、`max-w-[56px] sm:max-w-none`）——
判断「桌面有没有被动过」的办法：把所有 `sm:` 及以上的候选值逐个读一遍，跟改动前逐字相同才算没动。

**⚠️ DOM 顺序固定为「品牌 → 导航 → 右侧入口」**（右侧那组靠 `ml-auto` 顶到最右）。
我在两行方案里把右侧入口挪到了导航**前面**，改回单行时忘了挪回来 —— 胶囊被顶到屏幕最右边缘，
用户一眼看出「位置不对」。**动这三个块之前先确认谁在前。**

**⑦ 高度取值**：顶栏桌面 68px / 手机单行自适应；底部播放条手机 60px、桌面 68px（**安全区另算，不含在内**）。

---

### 3.4 音源协议模型（**理解「为什么没音源也能搜」必读**）

#### 背景：一个长期被误解的能力边界

项目原先只有一种「协议」——用户在浏览器导入的 **LX 自定义源脚本**。
前端因此把**所有**功能都锁在了「必须先导入音源」之后：没导入音源，连搜索框和榜单页都不让用。

但事实上，**后端早就内置了官方平台适配**（`pkg/search` 搜索与歌词、`pkg/charts` 榜单与歌单），
这些能力**不依赖任何脚本**。实测：

```bash
# 无需任何音源，后端独立返回 37 条结果，且带真实音质档位
curl 'http://<NAS>:8899/api/search?q=周杰伦&platform=wy&limit=3'
```

`pkg/search` 全文**零引用** `sources` / `cfgMgr` / `CustomSource` —— 搜索与音源完全无关。

#### 两种协议，互补而非替代

| | `official` 官方协议 | `lx` LX 自定义源协议 |
|---|---|---|
| 位置 | **后端内置**（`pkg/search` + `pkg/charts`） | **浏览器沙箱**（`frontend/src/engine/lx-runtime.js`） |
| 需要导入音源 | ❌ 不需要 | ✅ 需要 |
| 能力 | `search` `charts` `playlist` `lyric` | `music_url`（取直链）`search` `lyric` |
| 平台 | `wy` `tx` `kg` `kw` | `wy` `tx` `kg` `kw` `mg` |

**结论**：没有音源时，搜索 / 榜单 / 歌单 / 歌词**全部可用**；只有「点击播放」与「下载」这两步需要 `lx` 协议把歌曲解析成直链。

#### 代码位置

| 文件 | 作用 |
|---|---|
| `backend/pkg/protocol/protocol.go` | 协议描述的**单一数据源**（纯数据 + `List()` / `Get()` / `HasCapability()`） |
| `backend/pkg/api/protocol.go` | HTTP 层，把静态描述 + 运行时状态（`ready` / `source_count`）一起返回 |
| `GET /api/protocols` | 对外接口，见 §6 |
| `frontend/src/components/SourceManager.vue` | 「音源协议」总览卡片，数据来自上述接口 |
| `frontend/src/components/SearchView.vue` | 未导入音源时只显示**提示条**（不遮挡内容） |

#### 改动的铁律

1. **不要把「未导入音源」重新变成搜索/浏览的硬门槛** —— 那是本次修掉的 bug。
   门槛只允许出现在**播放**与**下载**两处。
2. **官方协议不得宣称 `music_url`** —— 取直链是 `lx` 协议的职责，混淆会误导前端与外部 AI。
   有单测守着：`TestOfficialProtocolDoesNotClaimMusicURL`。
3. **`lx` 协议的 `Location` 恒为 `browser`** —— 后端不内置、不执行第三方 JS（§2 规则一）。
   有单测守着：`TestLxProtocolStaysInBrowser`。
4. 改官方协议覆盖的平台时，**三处要同步**：`pkg/protocol/protocol.go` 的 `OfficialPlatforms`、
   `pkg/search/aggregator.go` 的 `switch` 分支、`pkg/charts/charts.go` 的 `switch` 分支。
   （`SearchView.vue` 的 `defaultPlatforms` 也应一致）

### 3.5 AI 能力总览（**加/改 AI 功能前必读**）

> 完整评估、判据、以及「明确不做」的清单见 **`docs/AI 能力评估.md`**。
> 这里只讲「有哪些、怎么开、边界在哪」。

#### 定位（一句话）

**AI 是「消歧器」「补缺器」「规则生成器」，不是「内容生成器」。**

#### 配置

「账号连接 → AI 大模型」那块折叠卡（v2.1.32 起；以前在曲库管家的「AI 设置」侧栏项），或 `POST /api/ai/config`。OpenAI 兼容接口
（`base_url` / `api_key` / `model` / `timeout` / `enabled`）。
**未配置时全部静默降级**，所有功能回落到规则层，主流程零感知。
用量按 `kind` 分类记账，在同一个页面看（`GET /api/ai/usage`）。

#### 五个用法（全部已完成，全部默认生效除标注外）

| 用法 | 在哪 | 触发时机 | 防错 |
|---|---|---|---|
| **文件名判名** `JudgeName` | `pkg/complete` 的 `resolveName` | 字段为空 **或** 规则自认可疑（`Suspect`） | 结果必须出自文件名，否则丢弃 |
| **搜索匹配消歧** `PickSongMatch` | `pkg/complete` 的 `resolveOne` | **规则全平台都没命中**时 | 只输出候选序号；越界判失败；编号 0 起 |
| **歌词核验** `VerifyLyric` | `pkg/complete` 的 `writeOne` | 默认**只核验 AI 挑的匹配**；`UseAIVerifyLyric` 可开全量 | 核验不通过就不写；**没给判定 ≠ 不匹配** |
| **生成命名正则** `SuggestNameRegex` | `POST /api/library/name-rules` | 用户主动点「AI 生成规则」 | **五道校验**（语法/组名白名单/含 artist 或 title/匹配样例/结果出自样例） |
| **genre 推断** `inferGenre` | `pkg/complete` 的 `writeOne` | `UseAIGenre`，**默认关** | 拿不准返回空；长度 + 标点校验 |

#### 三条硬约束（改 AI 代码时不要破坏）

1. **AI 只能从给定输入里选/判，不能引入外部内容。**
   `JudgeName` 要求「歌手歌名只能是文件名里出现过的文字」，
   `PickSongMatch` 只输出候选序号 —— 输出范围被限制成有限集合，幻觉无处可逃。
   **光靠 prompt 说「不许编」不够，返回前必须自己校验**（见各自的实现）。

2. **布尔字段的零值语义要想清楚。** 这个坑踩过两次：
   - `nameparse.Rule` 第一版用 `Enabled bool` → 零值 false 导致「生成→保存」的规则**默认不生效**
   - `ai.VerifyLyric` 第一版用 `bool` 接 `match` → 模型回 `{}` 时被当成「不匹配」，**把好歌词全丢掉**
   → 用**反向字段**（`Disabled`）或**指针**（`*bool`）区分「没有值」和「值为假」。

3. **AI 是可选增强，每个场景都要有规则兜底。** 没配 Key 时不能有任何功能坏掉。

#### 成本

一次调用约 500 prompt + 100 completion tokens。粗算 1000 首约 **¥1**、约 **16 分钟**（并发 2）。
**成本可忽略，延迟才是约束** → 所以 AI 只处理「规则搞不定的那一小部分」。

#### ⚠️ 真实准确率**没有验证过**

所有 AI 相关的测试用的都是 **mock 服务**，只证明了「解析、防错、适配层」正确，
**没有证明「模型判得对 / 挑得对」**。开发环境从未配过 API Key。

真机验证时建议先配 Key 跑一批**已知答案**的歌（比如 50 首自己认得出来的），
统计命中率，再决定要不要默认开。方法见 §4.8。

### 3.6 音乐入口接管（`pkg/takeover`，v2.1.67 新增；v2.1.68 / v2.1.69 / v2.1.71 修真机 bug）

**为什么存在**：飞牛官方音乐后端只监听一个 Unix socket，谁能 listen 它，谁就决定官方界面能看到什么。
本应用此前一直是这个 socket 的**客户端**（`pkg/fnos/token.go` 读令牌、`pkg/fnos/music.go` 调歌单接口）。
接管补上了「服务端」那一半，为后续阶段（拦截搜索/取流/歌词/封面/收藏/歌单并做合并）铺路。
**v2.1.67 只有接管与全透传，不改任何行为。**

#### 文件

| 文件 | 作用 |
|---|---|
| `takeover.go` | `Manager`：锁、journal 恢复、发布/回滚、还原、反向代理、探活端点 |
| `journal.go` | 接管前落盘的现场记录（原子写、0600），用于崩溃后自愈 |
| `takeover_linux.go` | 平台能力：`listenUnixNoUnlink` / `moveNoReplace` / `flock` / `inspect` / `probeJSON` |
| `takeover_other.go` | 非 Linux 桩：一律返回 `ErrNotLinux`，**不静默退化** |
| `takeover_test.go` | 19 个离线用例（真 Unix socket + 真 `renameat2`，不需要 root，不需要官方 daemon） |

#### 生命周期

```
Enable()
  ├─ Enabled()？          否 → ErrDisabled
  ├─ acquireLock()        flock 非阻塞；被占 → ErrUnsafe（另一个实例已接管）
  ├─ recoverIfNeeded()    上次崩在半路？按 journal 先还原官方，再往下走
  ├─ Snapshot()           探 target / upstream 各是什么
  │    ├─ 两个都空/stale → 开机竞态：等官方出现（默认 40s），超时 → ErrUnsafe
  │    ├─ target 是我们   → 已在接管，直接成功
  │    ├─ upstream 是官方 且 target 不是官方
  │    │                  → **让路**（有人已接管：官方被挪到上游，他顶在入口）
  │    ├─ 任一为 other-proxy → **让路**（同上，只是这次认得出对手）
  │    └─ 任一为 unknown  → ErrUnsafe（拓扑不明，不猜）
  ├─ stageListener()      在 <DataDir>/stage/ 下 listen 私有 socket
  ├─ 起 httpSrv.Serve(staging)   ← **必须在 publish 之前**：publish 靠 livez 自证
  └─ publish()
       ├─ verifyOurselves(staging)
       ├─ writeJournal()   先落 journal
       ├─ move(target → upstream)
       ├─ move(staging → target)
       ├─ verifyOurselves(target)  失败 → rollbackPublished()
       └─ waitUpstream()           等上游确认为 official，失败 → rollbackPublished()

Close() / RestoreNow()   停自己的监听 → restore(true) → 释放锁
restore(preflight)
  ├─ 没有 journal → 无操作（**我们从没 publish 过，就没什么可撤销的**）
  ├─ 上游 absent → 清 journal，无操作
  ├─ preflight 且上游不是 official → **ErrUnsafe 拒绝**（绝不动别人的代理）
  ├─ target 上蹲着自称不是我们的代理 → 清掉作废的 journal，无操作（**别人接管了入口**）
  └─ target → <target>.qulv-restore；upstream → target；失败则挪回
```

#### 八条硬约束（**别改回去**）

1. **身份只信内核**：`SO_PEERCRED` 取 peer pid + `/proc/<pid>/exe` 判是不是 `trim-music`。
   应用自报的服务名只是补充（`probeLivez` 要求 `service` 与 `pid` 同时对上，只认服务名任何东西都能冒充）。
2. **只做原子替换**：`renameat2(RENAME_NOREPLACE)`。内核或文件系统不支持（`EINVAL`）→ 直接拒绝，
   **绝不退化成「先删再建」** —— 那一步会真删掉官方 socket。
3. **先 journal 再动 socket**：`publish` 的顺序不能换。还原**之前**先预检上游必须是官方。
4. **不抢位置**：官方没起来时在预算内等它（否则官方之后 bind 会 `EADDRINUSE`）；
   别人的扩展已经在代理时让路；拓扑不明时拒绝。
   **判「别人在代理」靠拓扑，不靠认识对手**：`上游是活的官方 daemon` + `入口不在官方手里`
   = 有人在代理，不管它是谁。不要退回「只认 `StateOtherProxy`」——别人的代理对不认识的路径
   通常直接透传给官方，官方对 `/_qulv/livez` 回 **200 + 前端 HTML**，我们的 JSON 探针拿不到东西，
   只能落到 `unknown`，于是误报「拓扑不明」并拒绝接管。这是 v2.1.67 在 NAS 上的真实故障
   （fnmusic-ext 正在运行，`/_ext/livez` 回它自己的 JSON）。让路的理由记在 `Status.Yielded`，
   由 `GET /api/takeover/status` 的 `yielded` 字段暴露。
5. **退出顺序**：`main.go` 里 `tk.Close()` 必须在 `httpServer.Shutdown()` **之前** ——
   反了的话中间那段时间 socket 被我们占着但服务已不响应，音乐打不开。
6. **只撤销自己记录过的接管**：`restore()` 的第一件事是读 journal，读不到就**什么都不做**。
   这是 v2.1.69 修的回归，也是本包最危险的一处 —— 让路时 `upstream` 恰好是官方
   （官方被那个扩展挪过去了）、`target` 是别人的 socket，只看上游判据会认为「该还原」，
   把别人的 socket 挪走再 `os.Remove` 掉。**我们自己什么都没接管过，却毁掉了别人的接管。**
   第二道闸是 `serviceAt(target)`：入口上活着的东西自称的服务名不是 `qulv-takeover` 就收手
   （覆盖「我们被 kill -9 留下 journal、机器重启后别人的扩展先接管」这个场景）。
   注意 `serviceAt` **不要求 pid 对上** —— 它问的可能是另一个进程里的我们自己。
7. **staging socket 必须与 target 同目录**：发布那一步是 `rename(staging, target)`，而
   `rename(2)` **不能跨文件系统**（`EXDEV`）。原先 staging 起在 `--data/stage` 下，开发机上
   `--data /tmp/...` 和 socket 都在 `/tmp`，同挂载点，所以一路全绿；真机上 data 在 `/vol1`、
   官方 socket 在 `/var/run`，第一次接管就报 `发布失败，已回滚: invalid cross-device link`
   （v2.1.71 修）。现在 staging 是 `filepath.Dir(target)` 下的 `.qulv-stage-<pid>.sock`。
   **别用 `mv` 验证跨文件系统**：GNU `mv` 会退化成「复制 + 删除」把问题藏起来，`renameat2` 不会。
   回归测试是 `TestEnableAcrossFilesystems`（target 放 `/dev/shm`、data 放临时目录）。
   同目录带来的两个副作用也一并处理了：失败路径 `dropStaging` 删文件（`listenUnixNoUnlink`
   故意不删），启动时 `sweepStaleStaging` 扫掉 `kill -9` 留下的残留。
8. **入口 socket 权限必须继承官方**：官方入口是 `srw-rw-rw-`（0666），而我们 bind 出来的
   文件受进程 umask 影响（NAS 上 022 → `srwxr-xr-x`）。差别在 root 眼里完全看不出来 ——
   **root 连得上、日志干净、`/api/takeover/status` 全绿** —— 但 nginx 或官方应用的非 root
   组件会直接 `connect` 失败，表现是「接管之后官方界面打不开音乐」。v2.1.71 在 NAS 上
   就是这么发现的：同一句 `curl --unix-socket`，root 拿到 `livez`，uid 895 报
   `Failed to connect`。现在 `publish()` 在动 socket 之前先 `os.Chmod(staging, socketMode(target))`，
   `socketMode` **读官方当前的权限位**（官方不在才退回 0666）—— 不写死 0666，官方哪天收紧成
   0600，跟着收紧才叫「权限不变」。回归测试 `TestPublishedSocketModeMatchesOfficial`
   （去掉那行 `os.Chmod` 实测会报「权限是 0755」）。
   **这类 bug 单测天然抓不到**：测试和被测进程是同一个用户跑的，开发机上永远全绿。

#### 测试为什么需要 `classifyOverride` 和 `Options.IsOurs`

进程内测试里假 daemon 与被测代码**同 pid**，`/proc/<pid>/exe` 也都指向测试二进制 ——
内核那两条判据在测试里天然失效。`classifyOverride`（测试专用全局钩子）与 `Options.IsOurs`
（按路径的归属回调）是给测试用的替身；**生产环境两者都是 nil / 空**。
同理 `Options.OnMoved` 让测试把「假 daemon 现在挂在哪个路径上」跟着 `rename` 搬。

#### 环境变量入口（v2.1.68 补）

飞牛启动应用只调 `cmd/main`，那个脚本原来直接把二进制 `nohup` 起来，**不读任何配置文件**；
后端也不加载 `.env`。所以用户把 `QULV_TAKEOVER=1` 写进数据目录后没有任何人读它。

现在 `cmd/main` 在启动前 `set -a; . "${VAR_DIR}/.env"; set +a`。
**这是本应用唯一的环境变量入口** —— 加新开关只要在代码里 `os.Getenv` 就行，不用再动脚本。

路径：`/vol1/@appdata/<appname>/.env`，而 `/var/apps/yinshu-ai/var/.env` 是它的软链，
两种写法都认（和 fnmusic-ext 放 `.env` 的位置一致，用户不用记两套）。
文件不存在静默跳过；解析失败只警告不拦启动。

#### 对外接口

- `GET /api/takeover/status` —— 报告 target/upstream 各自形态；让路时多一个 `yielded` 字段说明原因
- `POST /api/takeover/restore` —— 立刻交还官方直连（上游不是官方时 409）

两条都在 `internalEndpoints` 名单里：开启要改环境变量 + 重启，不是 AI 能自行完成的动作；
`restore` 会改变官方音乐可用性，不该由外部调用触发。

---

### 3.7 在线曲库拦截层（`pkg/intercept` + `pkg/online`，v2.1.70 新增）

接管生效后（§3.6），官方客户端的**每一个**请求都先经过这一层。它只认自己登记过的
在线曲目，其余一律交回透传 —— **所以它整个坏掉时，表现只是「在线曲库消失」，
官方音乐本身照常**。这条失效模式是这一层的设计前提，改它之前先想清楚。

#### 文件职责

| 文件 | 职责 |
|---|---|
| `pkg/online/track.go` | 虚拟 id：`RealID(platform, id) = "online:<平台>:<平台内 id>"`；`FakeID = md5("qulv::" + RealID)` → **32 位小写 hex**。盐只有这一处常量 |
| `pkg/online/registry.go` | 内存登记表（fake → Track 描述符）。**不落盘**：虚拟 id 是确定性哈希，重启时把磁盘上出现过的曲目重放一遍就恢复了 |
| `pkg/online/vo.go` | 下发给官方客户端的 JSON 形状（搜索 VO / 收藏 VO / 元数据 VO / 歌词 VO） |
| `pkg/online/resolve.go` | 直链解析池：`Resolver` 接口 + 5 分钟缓存（只缓存成功与「明确不可播」，网络错误不缓存） |
| `pkg/online/netease.go` / `qq.go` | 两个真实解析器 |
| `pkg/online/store.go` | 按用户隔离的磁盘存储（收藏 / 歌单附加条目 / 播放历史）。**每条都内嵌完整曲目描述符** —— 这是重启后重建登记表的前提 |
| `pkg/intercept/intercept.go` | 路由表、透传、用户隔离键、上游响应读取 |
| `pkg/intercept/search.go` | 搜索合并 |
| `pkg/intercept/stream.go` | 在线取流代理（转发 `Range`，流式） |
| `pkg/intercept/lookup.go` | 从请求里认 guid（query / 子路径 / body 多别名） |
| `pkg/intercept/{lyric,metadata,cover,favorite,playlist,history}.go` | 其余六个域 |
| `pkg/intercept/vplaylist.go` | **虚拟歌单**（v2.1.73 起）：每日推荐 / 热门推荐 / 榜单卡片，插入官方歌单列表最前，对外只读 |
| `pkg/intercept/vchart.go` | **榜单注入**（v2.1.76）：把「发现音乐 → 排行榜单」里勾选的榜单变成飞牛里的在线歌单 |
| `pkg/intercept/pageshell.go` | **页面注入**（v2.1.79）：只对 `GET` + `Accept: text/html` 的响应缓冲一份文档，在 `</body>` 前插一行 `<script defer src="/music/_qulv/ui.js?v=…">`；其余流量一律不认领 |
| `pkg/online/musicdl.go` | **musicdl sidecar 客户端**（v2.1.83）：`MusicDLResolver` 把外挂进程绑定成 `Resolver`；`ResolveQueries` 是带歌名/歌手的那条正路。`musicdlSongID` 与 sidecar 的 `core.song_id` **必须同拼法**（跨进程键） |
| `pkg/search/provider.go` | **外挂搜索器注册表**（v2.1.83）：`Searcher` 接口 + `MusicDLSearcher`。`Search()` 先问外挂、没结果退回原生 |
| `pkg/sidecar/sidecar.go` | **sidecar 进程管理**（v2.1.83）：venv 引导、spawn、健康检查、优雅停止、残留清理 |
| `musicdl_wiring.go` | **musicdl 接线**（v2.1.83，仓库根/backend）：配置 → 进程 → 注册，`Apply(cfg)` 幂等 |
| `sidecar/musicdl_service/` | **Python sidecar 源码**（v2.1.83）：`core.py`（纯逻辑、零依赖可测）+ `app.py`（FastAPI）+ `test_core.py` + `requirements.txt` |
| `pkg/intercept/assets/ui.js` | **注入进飞牛官方页面的脚本**（v2.1.79 起，v2.1.81 改版，`go:embed`）：顶栏「曲率」入口 + 面板（正在播放 / 一键下载 / 打开曲率 / 偏好 / 状态）+ 行菜单「下载到曲率」+ 顶部 toast。**皮肤全用官方的 `--ds-*` 变量**（见下文第 6 条）。零 `setInterval`、幂等、只打同源白名单接口 |

#### 五条硬约束

1. **虚拟 id 必须是 32 位小写 hex，且是确定性哈希。**
   官方客户端只认自己那种 32 位 hex 的 id 形状；确定性则是「重启后自愈重建」的
   全部依据 —— 一旦改成随机 id 或换盐，所有已存在的在线收藏/歌单条目全部变成孤儿。
2. **不抄 fnmusic-ext 的全局递归字符串重写。**
   它是用正则 `online:[A-Za-z0-9_:\-]+` 在整棵 JSON 上猜 id 形态，官方加一个带 id
   的字段就漏。这里是**按字段名结构化映射**：先分配虚拟 id，再自己构造要下发的
   JSON，每个字段显式写。代价是官方加字段时我们「少显示一个字段」，而不是「漏出
   内部 id」—— 失效方向是安全的那一边。
3. **只注入能解析出直链的在线结果。**
   QQ 实测只有约 33% 的曲目能拿到 `purl`。把不能播的也列出来，用户点下去只会得到
   一个 404，比不显示更糟。
4. **认不出来就透传，绝不猜。**
   官方非 200 / 非 JSON / `code != 0` / guid 没登记过 —— 一律原样透传。这条同样适用
   于「官方的业务错误」：官方 API 的业务失败是 **HTTP 200 + 非 0 的 code**，所以
   任何「先转官方、成功后再改本地状态」的地方都必须判 `code`，只看 `StatusCode`
   会把失败当成功（v2.1.70 修过这个：见下方）。
5. **`isFavorite` 必须照实算，绝不能写死。**
   官方前端**不查收藏接口**，而是从列表里反推收藏集合：
   `favoriteIds = list.filter(x => x.isFavorite).map(x => x.guid)`。
   而收藏列表、歌单附加曲目、播放历史**共用** `online.FavoriteVO`，早先它把
   `isFavorite` 写死成 `true` → 歌单和历史里**每一首**在线曲目都被算进
   `favoriteIds`，红心全亮（v2.1.72 修，对着官方前端 bundle 核出来的）。
   现在的语义是：收藏列表传 `true`（按定义都在里面），歌单/历史传
   `favorites[it.Track.FakeID()]`，其中 `favorites := i.store.FavoriteGUIDs(i.userKey(r))`
   —— 与 `search.go` / `metadata.go` 标注 `SearchVO` 用的是同一个模式。
   回归测试 `TestOnlineIsFavoriteReflectsStore` 覆盖歌单 + 历史两条路径。
   ⚠️ 顺带记住：**`is_online` 在官方前端里出现 0 次**（官方 UI 根本不读），
   别把它当功能字段；它只是契约一致性的点缀。

#### 合并顺序（三处不一致是刻意的）

| 场景 | 顺序 | 为什么 |
|---|---|---|
| 搜索 | 本地在前、在线在后 | 官方客户端把本地结果当作「我的歌」，放前面符合预期 |
| 收藏 / 歌单曲目 | 官方在前、在线在后 | 官方列表是用户既有资产，新加的挂后面 |
| 播放历史 | **在线在前**、官方在后 | 历史是「最近发生了什么」，在线播放通常就是刚刚那次 |

#### 用户隔离

隔离键 = 官方 `/user/me` 返回的 guid；探测不出来退化成凭据指纹
（`sha256(Cookie + Authorization + X-Trim-Music-Temp-Token)`）；**连凭据都没有才
退化成 `shared`**。

> ⚠️ 这个 `shared` 分支曾经是死代码：`credentialFingerprint` 无条件对三个头做 sha256，
> 而 sha256 对空输入也给得出合法摘要，于是「一个凭据都没有」看起来像「一个真实用户」。
> 现在三个头全空时显式返回空串。

探测失败**不阻断**请求 —— 只是把这条请求归到 shared 桶。宁可让某个用户暂时看不到
自己的在线收藏，也不要因为一次探测失败让整个列表 500。

#### 已验证的边界（改之前先读）

- **搜索分页**：本地段占全局下标 `[0, 本地总数)`，在线段紧随其后。官方越界页会被
  官方自己钳回第 1 页（此时必须丢掉官方返回的本地列表，否则第 3 页会重复显示第 1 页）；
  官方忽略 `size` 全量返回时按请求窗口原地切片。
- **`total` 用实际可播条数**，不是「查到的条数」。fnmusic-ext 的 `total_online` 是过滤
  前的计数，会出现「写着 20 条、翻页只有 6 条」。
- **歌单曲目列表的 `size == -1` 是官方「全量」约定**，必须在「小于 1 就兜底」之前
  保留下来（`atoiSigned`）。用通用 `atoiDefault` 会把全量悄悄变成第一页。
- **歌单加歌：官方批次先行**。官方批次失败（包括业务错误码）时**本地一条都不写**，
  并把官方的错误原样返回。反过来会做出「界面报错、歌单里却多了一首只有本机看得见的歌」。
- **歌单删歌单：官方业务成功之后才清本地**。宁可留下「官方没了、本地还有」的残留，
  也不要「本地删干净了、官方其实还在」—— 后者会让用户反复删同一首歌。
- **元数据的 `data.track.genres` 必须是数组、`data.track.album` 必须是对象**。
  官方前端 `_h()` 会直接抛错并跳过整个播放器（连 stream 都不请求）。
  Go 里 `nil` slice 会 marshal 成 `null`，所以恒用 `[]any{}`。
- **歌词取回是同步阻塞调用**，外面必须套一层带缓冲 channel + `time.After` 的超时；
  否则一个慢接口会把整个请求挂住。
- **封面有同源校验**：官方前端只接受同源、pathname 以 `/static/cover` 结尾、带
  `coverId` 参数的 URL。所以外站封面 URL 不能直接下发，必须经 `/static/cover` 代理。
  代理侧按**魔数**认图片类型，不信任 `Content-Type`。
- **转给上游的请求必须是绝对 URL。** 上游走 Unix socket，host 部分其实没用
  （transport 会无视它去拨 socket），但 `http.Client.Do` 会先检查 URL 是不是绝对的 ——
  用 `r.URL.RequestURI()`（只有 path+query）会直接失败在 `unsupported protocol scheme`，
  而且错在发出去之前：客户端只看到一个**空 body 的 502**，和「上游挂了」长得一模一样。
  这也是为什么 `upstreamJSON` 的转发失败分支一定要打日志。
  `TestForwardBuildsAbsoluteURL` 用真 `*http.Client` 钉死这一点 —— 测试替身
  `fakeUpstream` 根本不看 URL，抓不到它。

#### 榜单注入（`vchart.go`，v2.1.76）

产品形态移植自 `zouclang/fnos_music_ext` v2.8.0 的 `proxy/charts.py`，
但**换掉了它的两处实现**（那是它的约束，不是我们的）：

| | fnmusic-ext | 曲率 |
|---|---|---|
| 榜单目录 | 写死 20 酷狗 + 18 网易云常量表（自抓榜单才有必要） | 直接调 `pkg/charts` 的实时榜单（15 分钟缓存）→ 候选 **wy 63 + tx 25 = 88 个**，平台上下架自动跟上 |
| 曲目来源 | 自己抓酷狗/网易 | 本机回环 `GET /api/charts/detail` |

**为什么走回环而不是直接调 `pkg/charts`**：那边四个平台的详情抓取都是
「解析完直接写 `http.ResponseWriter`」，要拿数据得把四个函数拆成「取数 + 写响应」两半 ——
那是重写几百行解析代码的风险，而回环成本只是一次本机请求，解析代码只留一份。
（`pkg/api/routes.go` 的三个 charts 接口**没有鉴权中间件**，所以回环不用带凭据。）

四条约定的口径：

1. **只放有直链解析器的平台**（`vChartPlatforms = {wy, tx}`）。
   kg/kw 挂上去就是「点开能看、点了不出声」—— 不给比给一个点了没反应的歌单好。
2. **榜单曲目同样过 `playableOnly()`**（口径与虚拟歌单一致：**进列表的每一首都必须能播**）。
   ⚠️ **顺序是「先过滤、后截断」，不能反。** 真机实测（2026-10-06）：
   榜单热门曲里 VIP / 无版权比例很高，**网易云「热歌榜」100 首候选里只有 36 首能播，
   QQ「巅峰榜·热歌」300 首候选里只有个位数**。所以候选窗口 `vChartFetchMax = 300`
   （取满上游能给的全部），过滤完**再**截到 `vChartSize = 100`；
   v2.1.76 第一版写反了顺序（先截 100 再过滤）→ QQ 榜单只剩 3 首。
   顺序由 `TestChartFiltersBeforeTruncating` 钉住（变异验证过：交换顺序 → `应得 100，得到 50`）。
   这条口径的**直接后果**：飞牛里的歌单必然比平台榜单短，前端注入控制条上明写了这件事。
3. **空结果只负缓存 5 分钟**（`vChartNegTTL`），正常 30 分钟。
   一次上游抖动不该让榜单卡片消失半小时，也不该变成每次请求都重打上游。
   另外**拉不到曲目就不进缓存**（`vPlaylistFor` 的 chart 分支直接 `return false`）。
4. **两个键**：`chart_playlists`（名单，元素 `<source>_<id>` 如 `wy_19723756`）
   + `chart_playlists_enabled`（总开关，**三态指针**：nil=开 / true / false）。
   总开关关掉**不丢勾选** —— 「关掉整个功能」与「取消某几个榜」是两件事，
   把总开关做成「清空名单」会让用户丢勾选（有测试钉死：
   `TestChartMasterSwitchOffKeepsPicksButInjectsNothing`）。
   ⚠️ `vChartDayStart()` **不能用 `Truncate(24*time.Hour)`** —— 那是按 UTC 零时截断，
   东八区会得到当天早上 08:00。

⚠️ 这个功能是**只读**的：详情 / 曲目列表本地应答，写接口（加歌 / 改名 / 删除）透传官方，
飞牛里表现为「可以看、可以播、改不了」。

#### 页面注入（`pageshell.go` + `assets/ui.js`，v2.1.79 → v2.1.81）

往**飞牛官方页面**里插一行脚本：顶栏一个「曲率」入口，点开是一块面板（正在播放 +
一键下载 / 打开曲率 / 偏好开关 / 连接状态），曲目行的「更多操作」菜单里多一项
「下载到曲率」，下载结果用顶部 toast 报。看板卡 `b6fa812b`（回合 70 的同源通道是它的
后端半边）；同一套框架给 `1ceb1ccf`（曲率 ↔ 飞牛双向切换按钮）复用。**改这块之前先读下面八条。**

1. **只碰文档。** 认领条件是 `GET` + `Accept` 含 `text/html`：其余（静态资源、接口、
   下载、WebSocket 升级）一律 `return false`，原样交给接管层的反向代理。代价是没有 SPA
   路由感知；收益是**官方页面的可用性没有被押在字符串拼接上**。
2. **必须丢掉条件请求头，并在注入后重算 `Content-Length`、丢掉 `ETag`/`Last-Modified`。**
   转发前 `Del("If-None-Match")` / `Del("If-Modified-Since")`。少任一条，真机上就是
   「升级了但界面没变」（浏览器拿 304 用注入前的缓存，或按旧长度截断把脚本切掉）——
   外壳只有 10 KB，每次都完整传一份比排查 304 便宜得多。文档 > 1 MiB 或带
   `Content-Encoding` 时不注入、直接流式转发（宁可不注入，也不把大响应憋进内存）。
3. **通道必须挂在 `/music/` 下。** fnOS 的 nginx 只有 `location /music` 指向入口 socket，
   裸的 `/_qulv/` 那棵树归一管的 livez/healthz。v2.1.74 的同源通道就栽在这里
   （当时只用 curl 直打 socket 验证过，生产路径根本没通）→ `pageshell_test.go` 里
   有一条**设计守卫**把前缀钉死。
4. **一键退路是真的退路。** `ui_inject_enabled=false` 时 `handlePageShell` **不认领**
   （而不是「认领了但不插」）→ 请求落到反向代理，官方页面与注入前**逐字节一致**。
   开关现取（`Config.UIInjectEnabled` 是函数），改完立刻生效、不用重启；
   **界面上也点得到**（v2.1.80）：「发现音乐 → 排行榜单」控制条上的 **`页面注入`** 开关
   （与 `榜单注入` 是两个开关，共用 `chartInjectBusy` 串行化，写的是同一个 `/config` 局部 patch）。
5. **注入脚本不改官方逻辑、不读凭据。** 它只做三件事：旁听官方接口 JSON 建
   `guid → {在线?}` 索引；监听「更多操作」点击取出该行 guid；在菜单里克隆一条既有条目改字。
   **官方给在线曲目返回的 `coverId` 就是 `guid`**（抽样 8/8）—— 所以「这一行是谁」不需要
   另开接口问。**零 `setInterval`**、幂等（`window.__QULV_PAGE_UI__`）、toast 挂 shadow DOM
   里（不与 Tailwind/Semi 抢层叠）。认得出来「不是在线曲目」就不插；认不出来的也插，
   让**后端**当唯一权威。
   ⚠️ **旁听合并必须「只升不降」**（v2.1.82 真机上栽过）：同一个 guid 会在多个响应里出现，
   其中 `track/metadata` 里嵌着的 `track` 字段是**官方上游原样透传**的那份、**没有 `is_online`**
   —— 用「最后写入者赢」会把已认定在线的曲目覆盖成本地曲目，真机现象是「正在播放一首
   在线歌，下载按钮却是灰的」。**字段缺失 = 「这个响应没说」**，只有**显式**为假才降级；
   标题 / 歌手 / 来源同理（新值为空则保旧）。

6. **不发明颜色 —— 注入界面一律 `var(--ds-xxx, 回退值)`。**（v2.1.81 定的，**这是这一版
   改版的全部要点**）官方页面自带一整套设计令牌挂在 `:root` 上，量得到的常用几个：

   | 用途 | 变量 | 真机值 |
   |---|---|---|
   | 品牌色 | `--ds-action-primary-bg` | `#f62c55` |
   | 品牌色软底 / 描边 | `--ds-action-primary-soft` / `-border` | `color-mix(in srgb,#f62c55 16%,transparent)` / `…46%…` |
   | 弹层底 / 边 / 阴影 | `--ds-bg-dropdown` / `--ds-border-dropdown` / `--ds-shadow-dropdown` | `#000000e6` / `#fff3` / `0 8px 32px #00000080` |
   | 玻璃模糊 | `--ds-player-glass-filter` | `blur(12px)` |
   | 文字四级灰 | `--ds-text-primary/-secondary/-tertiary/-quaternary` | `#fff` / `#fffc` / `#fff9` / `#fff6` |
   | 控件底 / 悬停 | `--ds-bg-button-primary(-hover)` | `#ffffff14` / `#ffffff1f` |
   | 列表项底 / 悬停 | `--ds-bg-list-item(-hover)` | `#ffffff0f` / `#ffffff14` |
   | 开关 | `--semi-color-switch-bg-on` / `--ds-bg-toggle-knob` | `rgba(249,61,99,1)` / `#fff` |
   | 状态色 | `--ds-special-success/-warning/-danger` | `#6bab45` / `#f8bf28` / `#f62c55` |
   | 字体 | `--ds-font-family-base` | `Montserrat, …` |

   **为什么这条必须守住**：v2.1.79/2.1.80 的注入物自己硬编码颜色（`#3f3f46` /
   `#15803d` / `#b91c1c`），放在官方页面上像贴上去的 —— 用户的原话是「不咋好看」。
   换成变量之后，官方换主题、换品牌色、换字体，注入的界面**跟着变**，不需要重新量一次。
   回退值只是「变量读不到」时的兜底，**不是第二套设计**。

   **为什么可以这么做**：整个界面住在一个 **shadow root** 里（隔离 Tailwind / Semi 的
   全局 reset），而**自定义属性会继承穿过 shadow 边界** —— 「隔离样式」和「共用皮肤」
   两件事同时成立。改 CSS 时记住这个前提。

   学 `fnmusic-ext` 的是**组织与克制**（卡片分组、开关带一行 13px 灰说明、顶部居中 toast、
   选中态加 `✓`），**不是它的浅色皮肤**（它是 `#f4f6f8` 底 + `#2f6fed` 主色，跟官方深色
   页面放一起会打架）。

7. **顶栏入口按钮靠「克隆官方按钮」而不是照抄样式。** 取 `button[aria-label="设置"]`
   克隆后换图标与文案（`data-qulv-entry`）→ 高度、圆角、hover、焦点圈、与相邻元素的间距
   **就是官方的**，官方改版也跟着变。代价是 React 不认这颗节点、重渲染可能把它摘掉：
   所以 `ensureEntry()` **幂等**（先查存在性），并由 `MutationObserver` 在**每批变更末尾**
   `if (!entryNode()) ensureEntry()` 补回 —— 一并覆盖「首屏时官方还没挂载完」。
   ⚠️ 别把这段改成「只在 removedNodes 时补」：首屏那条路径根本不会有 removedNodes。

8. **`/music/_qulv/api/info` 的端口只从 `LocalAPI` 取，绝不从请求取。**（v2.1.81）
   面板要知道「曲率自己的地址」（注入脚本跑在官方页面的 origin 上，曲率在另一个端口，
   脚本无从得知），所以由后端拼：**主机名取请求的 `Host`**（用 IP / 域名 / 主机名访问
   官方页面都能到同一台 NAS），**端口取启动时定死的回环地址**。请求头是调用方可控的，
   拿它拼 URL 就是开放重定向 / SSRF 面 —— `TestQulvInfoPortNeverComesFromRequest`
   专门守着这条。拿不到就返回空串，面板把「打开曲率」置灰：**宁可少一个按钮，
   也不要一个骗人的按钮。**

⚠️ 官方曲目列表是 `react-window` **虚拟滚动**：行节点会被复用，所以 guid 不能绑在闭包里 ——
点击时现读菜单上的 `dataset.qulvGuid`。

#### 注入脚本的「开发覆盖」（v2.1.81）

`<数据目录>/ui.dev.js`（真机即 `/vol1/@appdata/yinshu-ai/ui.dev.js`）**存在且非空**时，
`handleQulvUIJS` 优先发它，并带上响应头 `X-Qulv-Dev: 1`（devtools 里一眼看出「发的不是
打进包里那份」）。

**为什么要有它**：注入界面只能在官方页面里看效果，而脚本平时是 `go:embed` 进二进制的 ——
改一行要重打包 + 重装应用（约两分钟）。有这个文件，改完刷新官方页面就能看到，视觉迭代从
「两分钟一轮」变成「刷新一轮」。**文件不存在（默认）= 走嵌入的那份，生产行为完全不变。**

⚠️ **读失败 / 空白一律静默退回嵌入的那份**（`TestQulvUIBlankDevFileFallsBack` 守着）：
空文件不能把注入脚本变成空白 —— 那会让官方页面上什么都不出现，而「脚本在、功能全无」
比 404 更难查。改完记得删掉这个文件（或者干脆别在生产机上留它）。

**实测基准（v2.1.82 真机，1280×813）**：

| 项 | 读数 |
|---|---|
| 官方外壳（未注入） | **10,674 B**，零 `_qulv` |
| 官方外壳（注入后） | **10,741 B**（+67 B，就是那一行）；标签 `?v=2.1.82-155d99f1` |
| `ui_inject_enabled=false` | 回到 **10,674 B** / 0 次 `_qulv`（**逐字节等于未注入**） |
| 顶栏入口 | `[data-qulv-entry]` 32×32 @ (22, **1133**)；官方「设置」32×32 @ (22, **1167**)；`nextElementSibling === 设置` 为真 |
| 面板 | 320×415；`background: rgba(18,18,20,.68)`、`border: rgba(255,255,255,.2)`、`border-radius: 12px`、`box-shadow: 0 8px 32px`、字体 `Montserrat`（**全是官方令牌**） |
| 行菜单 | 9 项 → **10 项**，`下载到曲率` 落在「添加到歌单」与「查看专辑」之间（第 6 项） |
| 偏好开关 | 关 → `localStorage['qulv.ui.menu']='0'` 且**已展开的菜单里那一条立刻消失**；开 → 立刻回来 |
| 下载全链路 | 点面板「下载到曲率」→ toast `已下载到曲率曲库` → 落盘 `马也_Crabbit - 海屿你.mp3`（**12,214,427 B**） |

面板与 toast 同住 `[data-qulv-ui]` 的 **shadow root** 里，`document.getElementById` 从页面查不到
（故意的隔离）—— 调试时用 `document.querySelector('[data-qulv-ui]').shadowRoot.textContent`。
`.panel` 隐藏态是 `pointer-events:none`（子元素继承），显示时才 `auto`。

⚠️ **量面板样式时别在点击的同一次求值里读**：`.btn` 有 `transition: background .15s`，
刚切换状态时 `getComputedStyle` 给的是**过渡中间值**（真机上就因此把「已启用」的品牌色
误读成软底色，白查了一轮）。

#### musicdl 外挂音源（`pkg/online/musicdl.go` + `pkg/sidecar` + `sidecar/`，v2.1.83）

曲率的在线能力原来只有网易云与 QQ（原生）。酷狗 / 酷我 原生**搜得到但拿不到直链**
（`err_code 30020` / `The request is illegal!` —— 风控，不是没写对），咪咕 / 千千 / B站
完全没有。这些交给一个 **Python 外挂进程**（musicdl），Go 侧通过回环 HTTP 与它说话。

**改这块之前先读下面八条。**

1. **平台短码写死映射**（不靠字符串推导 —— 「去掉 MusicClient 再小写」那套会在
   `HTQYY`/`FiveSing` 这类名字上出错）：

   | musicdl 源 | 短码 | 原生 |
   |---|---|---|
   | `NeteaseMusicClient` / `QQMusicClient` | `wy` / `tx` | 全支持（走 sidecar 纯重复 → 默认关） |
   | `KuGouMusicClient` / `KuwoMusicClient` | `kg` / `kw` | 搜索 ✓ **直链 ✗** |
   | `MiguMusicClient` / `QianqianMusicClient` / `BilibiliMusicClient` | `mg` / `bq` / `bi` | 都没有 |

   **默认只开 `mg,bq,bi`**（纯增量）。`kg`/`kw` 打开后**搜索与解析都改走 musicdl** ——
   ⚠️ **一个平台只能有一个 id 空间**：搜索给出的 id 必须能被解析认出来，一半原生一半
   外挂就是「搜得到、点开放不出」。所以这是「改变现有行为」，默认关。

2. **官方页搜索合并：等搜索有上限、解析要用自己的 context。**（v2.1.86–87 真机两连坑）
   合并是并发跑各平台 + `wg.Wait()`，而 `i.searcher` 是**同步无 ctx** 的 ——
   ⚠️ **总预算（`onlineSearchBudget`）对它是无效的**，所以另有一道
   `onlineSearchWait`（3 秒）**限时等待**：到点只用已经回来的平台。
   然后 ⚠️ **解析必须用新的 context**：等搜索会把总预算用光，拿那个**已取消**的 ctx 去
   `ResolveMany` → 解析全灭 → 现象是「搜索返回了，但一首在线歌都没有」。
   另外 `stubResolver` **必须看 ctx**（忽略 ctx 的假件让这类 bug 在单测里永远测不出来）。

3. **默认值不是随手定的，是实测出来的。**（v2.1.88）真机单源实测（同一关键词）：

   | 平台 | 耗时 / 条数 |
   |---|---|
   | 咪咕 `mg` | 2.1 秒 / 5 条 · 3.2 秒 / 8 条 · **7.0 秒 / 12 条** |
   | 千千 `bq` | **>12 秒 / 0 条**（一次都没成功过） |
   | B站 `bi` | **>12 秒 / 0 条**（同上） |

   → 默认源只留 **`mg`**，默认单源条数 **5**。musicdl 在**搜索阶段**就为每条解析直链，
   条数直接决定耗时；15 条赶不上 3 秒窗口，现象是「开了外挂音源却搜不到咪咕的歌」。
   `TestMusicDLDefaultLimitStaysInTheOfficialSearchWindow` 钉住这条约束（>8 就报错）。

4. **解析是无状态的：请求必须带 `title` + `artist`。** musicdl 的直链是**搜索结果对象上
   的字段**（`download_url`），不是「拿 id 去问」能拿到的。参考实现（fnmusic-ext）靠
   sidecar 自己长期缓存搜索结果、之后按 id 反查 —— 代价是**进程一重启，所有已入库曲目
   全部解析失败**。这里改成**带歌名+歌手回搜**再按 `id → 歌名+歌手 → 歌名` 三级匹配
   （宁失败不错歌）。所以 `Pool.ResolveTracks`（不是 `ResolveMany`）才是外挂平台的正路；
   `ResolveMany` 手里只有 id，对外挂平台只能命中 sidecar 进程内缓存。

5. **网络故障绝不写负缓存。** 写了等于把那首歌锁死五分钟不可播，sidecar 恢复了也没用。
   只有「sidecar **明确**说这条没有直链」才负缓存。三条用例守着
   （`TestSidecarDownDegradesWithoutPoisoningCache` / `...ErrorStatusDoesNotPoisonCache` /
   `...NotPlayableIsNegativeCached`）。

6. **sidecar 生命周期由 Go 掌管**（不是写进 fpk 的 start/stop 脚本）：venv 在
   `<数据目录>/sidecar/venv`，依赖标记 `.deps-ok` 存 `requirements.txt` 的**内容哈希**
   —— 内容没变就不重装（pip 那一趟要一两分钟，每次启动都跑是不可接受的）。
   **健康检查是唯一的就绪判据**（进程活着 ≠ uvicorn 起来了 ≠ musicdl 装好了）。
   进程放进**自己的进程组**（`Setpgid`），停止时对整组发信号；启动前 `killStale` 按
   **端口**找残留进程（按进程名猜会误杀别人的 python）。

7. **失败必须说得清。** `GET /api/musicdl/status` 报
   `off / preparing（正在装依赖）/ starting / ready / failed（附 error）/ stopped` ——
   因为「装依赖失败」和「平台被风控」在界面上长得一模一样，没有这个接口就无从下手。
   sidecar 的 `errors` 里 `kind` 区分 `unavailable`（少一个平台）与 `invalid`（我们参数写错了）。

8. **打包必须显式列文件名。** `scripts/build_fpk.py` 把
   `sidecar/musicdl_service/{app.py,core.py,requirements.txt}` 打进 `app.tgz`
   —— **扫目录会把 `__pycache__` 和 `test_*.py` 一起打进去**；少一个文件是**静默失败**
   （真机上表现为「找不到 sidecar 源码目录」），所以那里有存在性断言。
   ⚠️ 同一个脚本的 `run_go_build` 必须编译**包**（`.`）而不是 `main.go` —— 写死文件名时
   `main` 包里的其它文件会被静默漏编，现象是「本地 `go build ./...` 正常、打包才 undefined」。

9. **源清单是「运行时发现」的，不是写死的表。**（v2.1.89）musicdl 真机注册 **57 个源**
   （`MusicClientBuilder.REGISTERED_MODULES`）。`core.ALIASES` 只列与曲率原生重叠、
   **必须**保持两字母短码的 7 个；其余由 `short_code()` 按 musicdl 的命名约定派生
   （剥 `MusicClient` 后缀 → **品牌名**：`AppleMusicClient` → `apple`）。
   ⚠️ `register_known()` 把注册表报的全名并进 `_KNOWN_SHORTS`，而
   `normalize_sources` / `parse_song_id` 的判据是**「注册表说过没有」而不是「看着像个词」**
   —— 少了这道闸，用户写错的 `nope` 会被当成一个源、`zz:123` 这种外来 id 会被切开。
   `client_name()` 只信别名表与注册表，**不靠规则还原大小写**。
   ⚠️ 界面上要标 `native=true` 的源（wy/tx/kg/kw）为「会换掉原生」：启用它会把那个
   平台的搜索与解析都换成 musicdl（一个平台只能有一个 id 空间）。
   `GET /api/musicdl/sources` + `POST /api/musicdl/probe`（单源**真实搜索**检测）——
   检测是这一页的核心价值：「注册了」≠「能用」，而**启用一个用不了的源是有代价的**
   （每次搜索都要为它等满单源预算）。

⚠️ **两次真机 bug 的教训（都是「本机跑不到那条路径」）**：
   ① **模块级语句顺序** —— 读注册表放在了 `MUSICDL_OK` 赋值**之前** → 导入 `NameError`
   → uvicorn 加载不了 app → 进程退出，日志里只有一长串 traceback。
   ② **改名漏调用点** —— `core.SOURCES` 改名 `ALIASES` 后 `app.py` 仍写 `core.SOURCES[short]`
   → 每次搜索 `AttributeError`。
   现在这两条路径都有用例：**用假 `fastapi`/`musicdl` 真把 `app.py` 导一遍**、以及
   **真的调一次 `search_source_sync`**。改 sidecar 之前先跑它们。

**实测基准（v2.1.88 真机）**：开关打开 → `preparing` → **64 秒 `ready`**（首次装依赖）；
升级重装后**秒级 ready**（venv 与标记跨升级保留，没重装）；`/healthz` →
`{"ok":true,"version":"1.0.0","musicdl":true,"sources":["mg"],"limit":5}`。

**全链路闭环**（v2.1.88，在飞牛音乐里）：搜「海屿你」→ **2.2 秒**、`total: 52` =
1 本地 + **49 在线（wy 31 / tx 16 / mg 2）**；拿搜索给的虚拟 guid 请求
`/music/api/v1/track/stream` → **`206`** + `content-type: audio/mpeg` +
`content-range: bytes 0-4095/11839949` + 魔数 `49 44 33` = **ID3 (mp3)**。
（直连 sidecar 那条路单独验过：解析 → `ext:"mp3"`、`file_size:17536733`、
主机 `freetyst.nf.migu.cn`、拉 4KB → `HTTP/2 206` + 同样的 ID3 魔数。）

⚠️ **还没做的**：外挂平台只有 API 能搜到，**没接进任何界面** —— `knownPlatforms`
仍是 `{wy,tx,kg,kw}`（`PlatformPriority()` 会滤掉 mg/bq/bi）、`main.go` 没给
`intercept.Config.Platforms` 传值（`pkg/intercept` 用 `{"wy","tx"}`，官方页搜索合并不带
外挂平台）、曲率搜索 UI 只暴露 wy/tx。三件事是一件事，下一轮做。

#### 测试与可测性

`pkg/intercept` 与 `pkg/online` 全部离线可跑（54 个用例），真实外网访问被三个注入口
挡住：`Config.Searcher`（在线搜索）、`Config.Pool`（直链解析）、`Config.LyricFetcher`
（歌词）。生产环境三者都留 nil 走真实实现。

---

## 4. 必须知道的"坑"

> 这些都是实际踩过并修好的，别改回去。4.1~4.4 是业务/架构坑，4.5 是 Windows 环境坑，4.6~4.9 是集成/账号连接的坑。

### 4.1 歌单监控的「发现」通道：浏览器上报 → 后端自抓（v2.1.10）

**问题**：用户希望定时监控歌单自动下载新歌。但音源脚本只能在浏览器跑（规则一），
后端拿不到**直链**；v2.1.9 之前连**曲目列表**也只靠浏览器上报。

**v2.1.10 起的双通道方案**（`pkg/api/monitor_backend_discover.go`）：

```
kind=playlist/favorites → 后端用已登录账号（push.FetchSource）自抓；
                          公开链接可匿名抓（FetchSourceByLink）
kind=chart（或酷狗/酷我等 lx 来源） → 仍是 POST /api/monitors/{id}/discover 浏览器上报
两条通道并存时：浏览器上报优先（takeDiscovered），取不到才走后端自抓。
```

- 首轮 `BaselineDone=false` 时**只登记曲目、不下载**，避免首次监控几百首歌单瞬间灌满队列
- **文件被删必须重下**：已登记但 `os.Stat` 失败时重新进入下载流程，
  否则用户手动删文件后监控会**永久僵死**
- ⚠️ **干跑预览必须用 `peekDiscovered` 而非 `takeDiscovered`**。
  用后者会把上报的曲目"吃掉"，导致紧接着的正式运行发现不了任何东西（这个 bug 出现过）
- **后端自抓的曲目没有直链**：`BuildPlan` 规定 download 动作必须有 `song.URL`，
  无直链的新歌登记为 pending（原因「待网页端一键补下载」），**不占单轮下载预算**；
  下载由网页端任务卡上的「补下载」按钮完成（SearchAPI 搜源 → downloadManager 入队）
- **补下载成功后回写** `POST /api/monitor/tracks/mark`（status=downloaded）。
  `BuildPlan` 对「downloaded 且 file_path 为空」的记录**信任并跳过** —— 否则下一轮
  又会把同一批歌登记回 pending
- **全部来源抓取失败必须返回 error**（而不是空列表）：否则失败轮会被当成
  「歌单清空」把首轮基线误标记完成。部分失败只进 warnings。

**沿革**：v2.1.9 之前只有「浏览器上报」一条通道，而实际上报只在「订阅歌单」那一刻
发生一次（`PlaylistDownload.vue` 创建监控时调 `MonitorAPI.discover`），加上
`takeDiscovered` 是消费式的 —— 第二轮起必然报「尚未收到该监控的曲目上报」。
2026-09-19 真机确认，2.1.10 按上面第 2 方案（后端抓取通道）修复。

#### v2.1.11 起补上第三段：后端取链（`monitor.URLResolver`）

v2.1.10 解决了「后端能自己发现曲目」，但拿到的曲目**没有直链**，只能登记 pending
等网页端点「补下载」—— 用户不开网页就永远不下载。配上服务型音源后，这段也能自动化：

```
发现（后端自抓 / 浏览器上报）
  └ 有直链        → download（原有）
  └ 无直链 + 后端有可用音源 → action "resolve"（占单轮预算）→ 取链成功即落盘
  └ 无直链 + 没有音源      → register pending（等网页端「补下载」，行为同 2.1.10）
```

- **接口**：`pkg/monitor` 定义可选能力 `URLResolver { ResolveURL(mon, song); CanResolve() }`。
  RunOnce 对注入的 Dispatcher 做类型断言，**两个方法都要满足**才产出 resolve。
  ⚠️ `CanResolve()` 不是多余的 —— 只看「有没有实现接口」的话，**装了应用但没启用任何音源**
  的用户会看到满屏「取链失败」，而不是准确的「待网页端补下载」。
- **实现**：`pkg/api/monitor_dispatcher.go` 的 `ResolveURL` 遍历 `cfgMgr.Get().APISources`
  （只看 `Enabled` 且 `BaseURL` 非空，按配置顺序 failover，第一个成功即用），
  调 `apisource.Resolve(protocol, baseURL, token, song.Source, song.ID, quality)`。
- **音质映射**（`qualityChain`）：standard→`128k` / high→`320k` / lossless|hires→`flac`；
  `best_effort` 逐档降级（目标→320k→128k），`skip` 只试目标档。
  ⚠️ **hires 各服务的拼写未实测**，先按 flac 请求（同属无损容器）。
- **取链失败不标 failed**：登记 pending + `run.ResolveFailed++`，整轮仍是 `ok`。
  下一轮监控自动重试（不引入退避状态机 —— 监控本来就有节奏）。
- ⚠️ **基线轮绝不取链**：几百首一次性打爆音源服务。BuildPlan 里 resolve 只发生在非基线轮，
  且**与 download 共用单轮预算**（`MaxDownloads`，默认 30）。
- ✅ **内网音源已支持**（v2.1.12）：`pkg/apisource` 自己放开内网/回环访问，**不需要任何环境变量**。
  原先走 `security.DefaultOptions()` 被 `FN_ALLOW_PRIVATE_NET` 卡住，内网音源连「填地址」都过不去。
  判断依据与 `pkg/ai` 的 AI 服务地址一致（用户显式配置的目标，无 SSRF 面）——
  **完整理由、以及「下载器为何有意不放开」的对照表见 §8.2**。
- **离线测试手法**：真实调用放包级变量 `apiSourceResolve`（与 `bd*` 同款），
  测试替换它即可验证「音源选择 / 降级链 / 错误口径」而不碰网络。

#### 4.1.5 界面上「一个订阅 = 一行 + 两个能力位」（v2.1.16）

**背景**：后端有**两套**独立任务 —— `monitor`（管发现与下载）和 `push task`（管飞牛歌单）。
但两者**共享了整整一半逻辑**：来源订阅 + 定时增量发现（抓取实现都是 `push.FetchSource`），
差别只在「对新增曲目做什么」。所以界面上它们不该是两个并列功能页 ——
用户会为同一个歌单管两份检查间隔、点两次启停。

**现在**：曲库管家 → 推送是**一个列表**，一行 = 一个来源，两个能力位：

```
download —— 有新歌就下载到本地曲库        （底层 monitor）
fnos     —— 把曲库里匹配上的同步到飞牛歌单（底层 push task）
```

**关键约束（改这块前必读）**：

1. **只合并前端，不合并后端**（YAGNI）。归并逻辑在 `frontend/src/services/subscribe.js` 的
   `buildSubscriptions(monitors, tasks)` —— **纯函数、有单测**（`subscribe.test.mjs`）。
   ⚠️ 别在组件里另写一套归并：界面一套口径、测试另一套口径是这类合并最容易出的问题。
2. ⚠️ **认不出 key 的记录也必须出现在列表里**。榜单「当前列表」模式的监控没有
   `target.playlists`，`push task` 里的 `daily`/`playlist` 来源也不是链接 ——
   它们退化成 `monitor:<id>` / `task:<id>` 行。过滤掉的话用户会以为「任务不见了」。
3. ⚠️ **一个监控可能绑多个歌单，只认第一个 key** 作为「它的行」，
   否则同一个监控出现在多行，在任一行点开关看起来同时动了两行。
4. ⚠️ **两个能力位取「任一记录 enabled」**（与 `pickSubscription` 的 `active` 同口径）：
   同一来源可能有多条历史记录，只看第一条会把「有一条还在跑」误判成「已停」。
5. **勾选语义**：有记录 → 只改 `enabled`（停用，保留配置与已下载文件）；
   没记录 → 真的建一条（走 `subscribePlaylist`，**只建这一边**）。
   「删除」才删记录。所以 `row.hasDownload` / `row.hasFnos` 决定勾选框是「切换」还是「新建」。
6. ⚠️ **间隔是订阅级的**，保存时两侧一起改 —— 否则会出现「下载 6 小时、同步 1 天」这种错位。
   注意两侧接口语义不同：监控侧是真局部更新（只传改动的键），推送侧是整体替换（必须回传全字段）。
7. **账号来源（歌单 / 每日推荐）只能同步飞牛**：它没有可粘贴的公开链接，
   监控侧订阅不了；「每日推荐」更是每天都变。所以新建弹窗里选「从账号选」时，
   「下载到本地曲库」置灰并说明原因。
8. **补下载有两个来源**，都走「逐首搜索 → 入队」：
   监控侧 `pending`（已发现待下载）与推送侧 `missing`（曲库里没有、推不上飞牛）。
   前者下载成功后要 `POST /api/monitor/tracks/mark` 回写；后者不用 ——
   下一轮推送任务重新匹配后自然更新。

**删掉的东西**：`PushManager.vue` 已并入本页并删除（备份在 `.workbuddy-ai/tmp/removed/`）。
其中「预览」（不实际推送、只看会推什么）**有意没搬** —— 「飞牛缺 N 首」徽章 +
执行历史已覆盖同一信息，少一个入口少一分困惑。

**文案**：界面上一律说「**还没开始追新**」，**不要写「基线」**（内部术语，用户看不懂）。
后端字段仍叫 `baseline_done`（接口契约，别改）。

⚠️ **但后端有四处字符串会写到界面上**，改术语时必须一起改（第一轮只改了前端，是改了一半）：

| 位置 | 用户在哪看到 |
|---|---|
| `monitor/pipeline.go` `SummarizePlan()` 的 `【首次检查】` | 任务日志摘要（`run.Log`） |
| `monitor/http.go` `HandleRun` 的 `【首次检查】本轮只登记曲目，不下载` | 任务日志正文 |
| `monitor/pipeline.go` `Preview()` 的警告「这个订阅还没开始追新：…」 | 干跑结果警告 |
| `monitor/pipeline.go` `PlanItem.Reason`「首次检查，只登记不下载」 | **落库进 `Track.Error`** → 曲目列表逐条显示 |

**判据**：`grep -rn 基线 --include="*.go"` 后**逐条看是注释还是字符串**。
注释不用管（进不了二进制）；字符串全改。详见 §3.3.3 的第 ③ 条。

#### 4.1.6 「停止」的语义、下载完自动同步、下载队列落盘（v2.1.17）

三处都是**用户实测反馈**驱动的。改之前先看这里的判据。

**① 「停止」= 两侧一起停，「开启」才各开各的。**

用户原话：「在推送的时候点了停止怎么还在推到下载」。

根因：两个能力位各自独立启停 —— 点「同步到飞牛歌单」的停止只停了推送任务，
监控侧照旧一轮一轮发现新歌、继续往下载队列里塞，**界面上完全看不出另一侧还在跑**。

`services/subscribe.js` 的 `setCapability(row, which, enabled)` 现在是：

| 操作 | 行为 |
|---|---|
| `enabled === false` | **两侧一起停**（monitors + tasks 都传） |
| `enabled === true` | 只开 `which` 那一侧（「只下载、不同步飞牛」是合理诉求，别替用户做主） |

配套：`LibraryManager` 的 `capTitle()` 把「点一下会发生什么」写进药丸的悬停说明
（开着的时候会说「下载与同步飞牛会一起停」），`toggleCapability` 的 flash 也写明是「整个订阅」。

**② 下载队列一空就自动推一次，不用再点「立即执行」。**

用户原话：「需要我手动点击一次立即执行才可以把歌曲显示在歌单里」。

⚠️ **这里的等待是关键，别当成多余代码删掉**：推送只认**飞牛曲库**里搜得到的歌，
而刚下载的文件要等飞牛重扫（`pkg/fnos/rescan.go`，30 秒 debounce）才进曲库。
所以 `LibraryManager` 的 `autoPushRow()` 是「先排一次重扫 → 等 `AUTO_PUSH_SETTLE_MS`（90 秒）→ 再推」。
**立刻推会一首都匹配不上** —— 用户看到的现象就是「歌单里没有歌」。

⚠️ 触发条件是**「队列里不再有活跃任务」而不是「全部成功」**（`watchQueueDrain`）：
用户原话「不管是全部下载完还是中途停止了，都能在飞牛音乐看到歌曲」——
中途暂停时已下完的那部分也该推上去，不能干等一个永远不会来的「全部完成」。

⚠️ **已知缺口**：这条链路走的是**前端补下载**（`fillRow`）。后端自己有服务型音源时
（`monitor` 的 `resolve` 分支）会直接下载落盘，那条路**没有触发点**，只能等下一轮定时推送。
要补的话在后端下载完成处加钩子（别在前端再想办法，前端不知道后端什么时候下完）。

**③ 下载队列落盘到 NAS。**

用户原话：「歌曲下载记录没有了，是只保留在浏览器吗？可以保存到设备的」。

以前只写 `localStorage`（`downloadManager.js` 的 `fn_download_tasks_v1`），换浏览器/清缓存就没了。现在：

- 后端 `pkg/downloadqueue` + `GET/PUT /api/download/queue`，落 `<dataDir>/download_queue.json`
- ⚠️ **后端不解释任务字段**（`[]json.RawMessage` 原样存取）—— 队列的状态机归前端
  `downloadManager`，前端加字段不用改后端。接口是**整体替换**，不是按条增删
  （想改成增量的话先想清楚「前端删了一条、后端还留着」怎么收场）
- ⚠️ **不能复用 `prefs.js`**：它走 `ui_prefs.json`，有 **64KB 总上限**
  （`backend/pkg/config/uiprefs.go` 的 `MaxUIPrefsBytes`），下载队列会把它撑爆
- 前端：`localStorage` 降级成**缓存**（首屏不空白、断网可用），服务端才是权威副本。
  启动时 `loadTasks()` 先撑首屏 → `syncTasksFromServer()` 再对齐；
  **只有服务端有内容时才覆盖本地**（服务端为空说明从没存过 —— 老版本升上来、
  或换了数据目录，这时把本地那份推上去更合理）

#### 4.1.7 勾了「下载到本地曲库」就自动下（v2.1.18）

**背景**：订阅上勾了「下载到本地曲库」，点「立即执行」却一首都不下 —— 后端那一轮只把新歌
登记成 `pending`，得回列表手点「补下载」才开始。用户原话：

> 「我订阅的歌单，下面选择了下载到本地曲库，那就应该在点击立即执行时，就开始下载了吧」
> 「包括补下载，也应该是作为用户在未勾选时手动下载」

**根因是架构限制，不是 bug**：音源脚本只能在浏览器里执行取直链，后端拿不到链
（`pkg/monitor/pipeline.go` 的 `canResolve=false` 分支只能 `register`）。
所以「勾了自动下载」这件事**只有前端能做** —— 别指望在后端补这一步。

**做法**：`LibraryManager` 的 `autoFillPending()`，挂在轮询上（8 秒一次）。

⚠️ **四道闸门，少一道就会出问题**：

| 闸门 | 为什么 |
|---|---|
| **`autoFilledRun`（只接新轮次）** | v2.1.23 新增。**只服务刚跑完的那一轮**（轮次 = `row.lastRunAt`）。没有它，上一轮留下的旧账（没搜到、被停掉的）会在每次打开页面时被自动抓一遍 —— 用户看到的就是「我没点它自己在列队」（2026-09-22 反馈）。处理完一轮（入队不下了 / 没试过的曲目为空）就把轮次记下，不再重复抓 |
| `row.baselineDone` | 第一次检查登记的是歌单**现有的全部曲目**，一次性几百首会打爆音源服务；而且「第一次检查不下载」本身是既有机制 |
| 该行在下载队列里还有活跃任务 → 让开 | 这就是「分批」：一批 N 首，下完自动接下一批。队列任务上打 `autoRowKey` 标记来认领 |
| `autoTried`（本次会话已自动试过的曲目） | 失败的不回写、仍是 `pending`，不拦会每轮重试同一批、无限循环。失败的留给用户手点「补下载」重来 |

⚠️ **轮询本身删不掉**（用户 2026-09-22 问「8 秒轮询发现待下载就入队是不是多余」）：
音源脚本只能在浏览器里跑，后端那一轮**只能**把新歌登记成 `pending`；
把这个登记变成真正下载的只有前端这一步。所以能做的是**改判据**（上面的闸门 0），不是删轮询。

其它约束：

- **一次只处理一行**（处理完就 `return`），别把所有订阅一起下、把 NAS 和音源一起打满
- **不能只挂在「推送」子页上** —— 用户切到「下载」也得继续下，所以 `pollTimer` 里先跑它
- 「立即执行」里额外安排了一次 `autoFillPending()`（延迟 5 秒，等后端异步 run 登记完），
  否则用户点完要干等一个 8 秒轮询周期才有反应
- 自动入队是**后台行为**，不弹「没有需要补下载的曲目」这类提示（每 8 秒弹一次会烦死人），
  只在真的入队了才 flash 一条

**「每次下载数量」**：复用 monitor 的 `max_downloads`（行内设置里叫「每次下载数量」，
预设 10/30/50/100 + 自由输入）。后端 `MaxDownloadsPerRun` 从 50 提到 **300** ——
⚠️ 它**不是节流阀，只是防呆**（拦「手输 999999」把一轮 run 拖成几小时）。
真正的节流靠音源服务自身 + 下载队列的并发/间隔。

**判重**（用户：「同一首，同歌手，不需要，只下载不一样的版本」）：
`enqueueSongs` 入队前先调 `POST /api/download/check`（后端按 `歌手 - 歌名.mp3` 查文件、
要求 >300KB），命中就**跳过入队并回写成「已下载」** ——
不回写的话它会一直挂在 `pending` 里，数字永远不降、自动补下载每轮白跑。
判定抽成纯函数 `frontend/src/services/downloadDedup.js` 的 `pickAlreadyLocal()`，有 8 个单测
（含「歌名和 id 都缺时别拿空拼接查表」这个坑）。

⚠️ **已知限制（也是 v2.1.19 补界面提示的原因）**：这条链路要求**浏览器里那个页面活着**。

飞牛是用 **iframe** 打开我们的（`fpk-package/app/ui/config` 里 `"type": "iframe"` + 端口 8898），
所以「页面」就是飞牛桌面里那个曲率 窗口：

| 状态 | 能不能下 |
|---|---|
| 飞牛里应用「已启动」（后端 Go 进程活着） | ❌ 后端拿不到音源直链 |
| 曲率 那个窗口开着 | ✅ |
| 关掉那个窗口 | ❌ iframe 卸载，前端停了 |
| 切到别的应用 / 最小化 | ⚠️ **未实测** —— 取决于飞牛是销毁 iframe 还是只隐藏 |

用户 2026-09-22 直接问过：「我这个是飞牛 app。网页开着什么意思。飞牛里应用启动算吗？」
—— **以前的文案说的是「网页端」，这个词本身就是问题**。现在「推送」页加了一条提示
（`LibraryManager` 的 `apiSourceCount`，只在**没配服务型音源**时显示），
并把唯一的真后台方案（服务型音源）写在同一条里。

⚠️ **别再写「网页端」这种词**：用户在飞牛里看到的是一个 App 窗口，不是「网页」。

**「服务型音源」是什么、不是什么**（2026-09-22 查证，避免后人重复走弯路）：

支持三种协议（规格见 §11 ㉒）：

| 协议 | 请求 | 鉴权头 |
|---|---|---|
| `lx_server` | `GET {base}/api/music/url?source=&id=&quality=` | `x-user-token` |
| `lx_script` | `GET {base}/url?source=&songId=&quality=` | `X-API-Key` |
| `lx_api` | `POST {base}/music/url` body `{source,musicId,quality}` | `X-Api-Key` |

⚠️ **官方的 LX Music「开放 API 服务」不是这个** —— 我查了 `lxmusic.toside.cn/desktop/open-api`，
它只暴露**播放器状态 / 歌词 / 播放控制**，**没有取直链的接口**。
所以「装个洛雪就有服务型音源了」是错的，别往那条路找。

配好之后：
- `POST /api/sources/api/resolve` → `{code:200,data:{url,...}}`，**后端直连取链、不需要浏览器**
  （接口说明原文：「前端播放/下载时调用它取直链，因此无需在浏览器里执行任何第三方代码」）
- `POST /api/monitors/{id}/run` → 一整轮「发现 → 取链 → 下载」全在后端跑完

⚠️ **另一条路（让后端直接执行 LX `.js`）**：技术上可行，但要在 Go 里嵌 JS 引擎
（`goja` 之类）并把 `lx.*` 沙箱 API 在服务端重建一遍 —— 浏览器那套踩了一轮坑才跑通，
搬过去等于重来。而且 `pkg/api/monitor_backend_discover.go` 的注释明说了
「下载仍由网页端「补下载」完成 —— **合规边界不变**」，是**有意**留在浏览器的。
要动这条得先明确这个取舍。

#### 4.1.8 「停止」要立即生效；下载失败要换源（v2.1.20）

这一节固化两件用户 2026-09-22 真机反馈的事。**改订阅启停 / 下载失败处理前先读它。**

**① 「停止」以前只改开关，拦不住「已经在跑的东西」**

用户原话：「立即执行后，再推送到下载的过程中，我点了全部停止怎么还在增加下载，
应该同时停止推送和下载」「我停止后，一会儿又开始推送」。

根因是**四条在途路径都没复查开关**，各自缺一道检查：

| 在途的东西 | 为什么没停 | 现在靠什么停 |
|---|---|---|
| 正在入队的批次（`enqueueSongs` 逐首串行搜索，几十首要 1~2 分钟） | 循环里根本没有停止检查 | `stoppedKeys`（会话级 Set）+ 每次迭代 `isRowStopped()` 复查；`fillRow` 的两个阶段之间也复查 |
| 已经排队的下载任务 | 「停止」只改后端 enabled，不碰下载队列 | `downloadManager.stopByRow(row.key)` —— 只停**这一行**入队的任务（靠 `autoRowKey` 认领），别的订阅不受影响 |
| 90 秒后自动推一次飞牛的定时器（`autoPushRow`） | 开关检查在 `await 90 秒`**之前**，等完无条件推 | 等待结束后**再查一次** `stoppedKeys` 与 `fresh2.fnos` |
| 后端已开跑的那一轮 `RunOnce` | 调度侧只拦「下一轮」，拦不住正在跑的这一轮 | 计划循环**每个 item 前**复查 `store.GetMonitor().Enabled`，停了就 `break` 并把 `run.Message` 置为「订阅已停止，本轮中止」 |

⚠️ **`autoRowKey` 必须给「手动补下载」也打上**（v2.1.20 前只有 auto 才打），
否则用户手点「补下载」入队的那批歌，点停止后管不到。该字段**会随队列落盘**
（`slimTasks` 里带 `autoRowKey`），刷新页面后停止依然有效。

**② 后端两个「立即执行」入口以前不看 enabled（防线缺失）**

- `pkg/monitor/http.go` 的 `HandleRun`：拿到监控就 `go RunOnce(mon)`，**不判 `mon.Enabled`**
- `pkg/api/push.go` 的 `HandlePushRun`：同样不判 `task.Enabled`

定时调度那侧**本来就有**判定（`monitor.DueMonitors` / `push.DueTasks` 都有 `if !Enabled { continue }`），
所以「停掉之后还能跑起来的」全是手动/自动触发的路径。现在两个入口都返回 **409**
（「该订阅已停止，请先恢复更新再执行」）。

⚠️ 测试坑：`monitor` 包的测试夹具必须显式写 `Enabled: true` ——
`RunOnce` 每轮都会复查 enabled，零值 false 会被当成「已停止」而立刻中止
（生产里订阅创建时前端会传 `enabled:true`）。见 `resolve_test.go` 的 `newRunFixture`。

**③ 「仅停推送」独立入口（v2.1.20 新增）**

能力位药丸点关闭是**两侧一起停**（v2.1.17 用户明确要求），但「下载继续、先别往飞牛推」
也是合理诉求。所以那一行在 `row.fnos` 为真时多一个「仅停推送」按钮 →
`subscribe.setPushEnabled(row, false)` → **只**改推送任务 enabled，监控侧一个字节不碰。
恢复：点一下「同步到飞牛歌单」药丸（开启只开点的那一侧）。

**④ 下载失败要换源（取链成功 ≠ 下载成功）**

用户原话：「歌曲下载失败怎么没有自动换源下载呢」。

关键区分：**取链阶段**早就会跨源/跨平台轮询（`lx-runtime.getMusicUrl`），
而**落盘阶段**失败以前直接 `failed`，不回头换源。两条路分别修：

| 路径 | 以前的毛病 | 现在 |
|---|---|---|
| 后端（服务型音源 apisource） | `monitor_dispatcher.go` 的 `ResolveURL` **拿到第一条非空链就返回**，下载失败即 `failed`，下一轮又按固定顺序取到同一个坏源 | 新增可选接口 `monitor.URLResolverCandidates`（`ResolveURLCandidates(mon, song, max)`）+ `downloadWithFailover`：取候选（去重）后逐个落盘，最多 `maxSourceAttempts = 3` 个；**一条链都取不到**才登记 pending，**候选全部下不动**才标 failed |
| 浏览器（音源脚本） | `downloadManager.handleTaskFailure` 里 CDN 403 / 坏链 / 文件过小被判成 FATAL → 直接 failed | 判定抽到纯函数 `frontend/src/services/downloadFailover.js` 的 `decideFailover()`：把失败分「本地问题（权限/只读，换源无用）」与「链路问题（403/坏链/试听片段，**换源能救**）」；后者且还有没试过的音源 → `rotateSource` 后重试 |

⚠️ **别把「不可重试」当成一桶**：这个坑就是当初把「链坏了」（换源能救）和
「目录写不进去」（换源救不了）混在一起，导致前者也被判死。
`ENOSPC`（空间不足）**刻意保持原样**（旧实现当可重试），不在本次调整范围内。

所有音源都试过仍失败时，前端任务错误里会带「已试过全部 N 个音源」——
免得用户以为程序没做换源。

### 4.2 AI 网关的三个坑

**坑 A：代理死锁**

本机环境变量设了全局代理 `http_proxy=http://192.168.1.3:7890`，
`no_proxy` 只含 localhost/127.0.0.1。Go 的 `http.ProxyFromEnvironment` 会把
**内网请求也发给代理**，而 SSRF 防护又拦内网 → 死锁：

```
proxyconnect tcp: dial tcp 192.168.1.3:7890: 禁止访问内网/元数据地址
```

**已修**：`pkg/security` 的 `proxyFunc` 按目标分流 —— 内网/回环直连，公网才走代理。

**坑 B：AI 网关返回 SSE**

用户的 AI 网关（本机 `0.0.0.0:20128`）**忽略 `stream:false`，始终按 SSE 返回**：

```
data: {"choices":[{"delta":{"content":"正"}}]}

data: {"choices":[{"delta":{"content":"常"}}]}

data: [DONE]
```

直接按 JSON 解析会报 `invalid character 'd' after top-level value`。
**已修**：`pkg/ai` 的 `parseSSE` 自动识别两种格式并拼接增量片段。

**其他要点**：
- 该网关**每次随机路由到不同免费模型**，偶尔返回**空 content**（不是 bug，重试即可）
- 访问必须用 `http://127.0.0.1:20128/v1`；用局域网 IP `192.168.1.66:20128` 会 hairpin 不通
- 用户明确要求过：**不要再加"多地址自动回退"之类的复杂逻辑**

**坑 C：推理模型返回空 content（2026-09-18 修，从参考实现学到的）**

推理模型（DeepSeek-R1 / QwQ 等）把思维链放在 **`reasoning_content`**，
**`content` 可能是空的**。我们原来：

- `FinishReason` 解析了但**从没用过**
- `reasoning_content` / `reasoning` **完全没接**

**后果是静默失败**：配了推理模型 → content 为空 → `ParseJSONObject("")` 失败 →
**所有 AI 功能（判名 / 挑歌 / 核验 / 生成正则）都表现为「没结果」，错误信息还是空的** ——
用户完全不知道是自己配了推理模型。

**已修**（`pkg/ai`）：
- 非流式与流式都接 `reasoning_content` / `reasoning` 两个字段名
- content 空 → **回退到 reasoning**（个别网关把正文塞在那里）
- 两者都空且 `finish_reason=length` → 报「输出被 token 上限截断……请调大 max_tokens
  或换非推理模型」；否则报带 `finish_reason` 的明确错误
- 顺手把匿名的 `Choices` 嵌套结构抽成具名类型（`chatMessageOut` / `chatDelta` / `chatChoice`）——
  匿名嵌套结构改字段时极易漏改一处

> 💡 这个坑是看参考实现 `leelaa.playlist` 的错误处理学到的，见
> `docs/参考项目/leelaa.playlist-调研.md` §4。
> **教训：AI 相关代码不能只处理「成功路径」** —— 模型的输出形态比 HTTP 接口多得多
> （空 content / 只有思维链 / 被 token 截断 / 只回 JSON 不回正文……），
> 每一种都要么能兜住、要么给**可行动**的错误。

### 4.3 下载器的两个已修 bug（别再改回去）

**bug 1：把错误页当成功**
CDN 防盗链/限流时返回 200 + HTML/JSON，旧实现按 URL 扩展名兜底成 `.mp3` 存下来并计为成功。
（实测复现：614KB 错误页存成 `.mp3`）

**现在**：`isErrorPayload` 拦截 text/html、json、`<html`、`{`、`[` 等特征；
`detectAudioExtByMagic` 认不出魔数就**直接拒绝**，不做 URL 猜测。

> ⚠️ 刻意**不用**"可打印字符占比"这类启发式——会误杀带大 ID3 文本标签的合法 MP3。

**bug 2：断点续传的两个缺陷**
- 续传时把 512 字节的探测缓冲当成数据写进文件（文件比原文件大 512 字节）
- 对端提前断流（`unexpected EOF`）被当成"写入失败"→ 删掉断点 → 无法续传

**现在**：`head` 只在 `have == 0`（从零下载）时用于魔数校验；
`isTruncatedBody` 区分"对端断流"与"本地写入失败"，前者**保留 .part** 供下次续传。

### 4.4 曲库扫描的"安全门"（最重要的一条）

**问题**：如果枚举过程中出现权限失败 / 深度截断 / 数量上限，
把"没枚举到的文件"当成已删除 → **索引会被误清空**。

**现在**：`pkg/library` 的 `Audit.Complete` 只有四项全干净才为 `true`：

```go
audit.Complete = len(audit.MissingRoots) == 0 &&
    audit.Inaccessible == 0 &&
    audit.DepthTruncated == 0 &&
    !audit.LimitHit
```

为 `false` 时 **`Prune` 拒绝删除任何记录**，只累计 `RemovedSkipped` 并告警。

**性能设计**：先按 `(size, mtime)` 对比找出新增/变化，**只对它们读标签**。
变化判定容差 1 秒（SMB/NFS 的 mtime 精度常见 1~2 秒，严格比较会导致每次都重读）。

### 4.5 Windows / MSYS 踩坑（若在 Windows 而非 WSL 开发）

| 坑 | 现象 | 解法 |
|---|---|---|
| **MSYS 只转换命令行参数，不转换环境变量值** | `GOROOT`/`GOCACHE`/`GOPATH` 塞入 `/d/...` 会报 `cannot find GOROOT directory` | 用 `cygpath -w` 转换（`dev.sh` 已内置 `to_native()`） |
| **Python 的 `Path.is_file()` 不补 `.exe`** | 探测 `go` 失败（shell 的 `[ -x ]` 会自动补） | 显式同时尝试 `go.exe` 与 `go`（`build_fpk.py` 已处理） |
| **`.ps1` 含中文必须带 UTF-8 BOM** | PowerShell 5.1 按 GBK 解析，多字节序列吞掉引号 → **语法错误、脚本完全跑不起来** | 补 BOM（`build_and_pack.ps1` 已修） |
| **`dev.sh` 的 GOROOT 推导会被符号链接打破** | `package context is not in std (/usr/local/src/context)` | 先用 `readlink -f` 解析真实路径（已修） |
| **`go build -o foo` 在 Windows 上不追加 `.exe`** | — | Git Bash 仍可执行该无扩展名 PE 文件 |
| **不要用 PowerShell `Expand-Archive -Force` 解压** | 触发 safe-delete 批量删除守卫，解压中断（表现为 `package xxx is not in std`） | 用 Python `zipfile.extractall` |
| **`grep` 要覆盖全文件** | 曾只 `head -30` 而误判 `build_fpk.py` 缺 `from __future__ import annotations`（实际在第 44 行） | 别想当然去"修"不存在的 bug |

### 4.6 飞牛集成与账号连接的坑（2026-09-16 新增）

| 坑 | 说明 |
|---|---|
| **两套飞牛接口认证方式不同** | 官方开放 API 用 `Bearer <TRIM_API_TOKEN>`；音乐应用接口用**裸令牌，无 Bearer**。已在测试里固化 |
| **QQ 音乐有两处 `hash33`，seed 不同** | `ptqrtoken = hash33(qrsig, 0)`；`g_tk = hash33(p_skey, 5381)` |
| **QQ 换凭据必须禁用重定向跟随** | 否则读不到 `Location` 头里的 code |
| **QQ 日推用 `req_0` 而非 `request`** | 且 comm 用网页版参数（`ct:19`、`platform:yqq.json`） |
| **QQ 歌单详情是 JSONP** | 需先剥掉 `jsonCallback(...)` 包装再解析 |
| **网易 `privilege.maxbr` 无区分度** | 实测恒为 999000，判断无损/Hi-Res 必须看 `sq`/`hr` 对象是否存在 |
| **酷我 `zp` 档 bitrate=20000 是哨兵值** | 必须先按 format/level 判定，再回退码率映射 |
| **`go get @latest` 会顶高 go 指令** | 还会自动下载新工具链。**必须钉版本**，并在打包脚本设 `GOTOOLCHAIN=local` |
| **WSL2 的 localhost 走 IPv6** | 浏览器要用 `localhost`（`127.0.0.1` 不通）；curl 测本机服务要加 `--noproxy '*'` |
| **别用 `pkill -f <关键字>` 结束 WSL 后台进程** | 会误杀承载命令的 shell 自身。用 `TaskStop` 或按端口结束 |
| **音源脚本的 handler 可能是异步注册的** | 两段式脚本先拉远程配置再 `lx.on('request')`，实测滞后约 250ms。取链前必须 `waitHandler()`，否则好音源会被判成「尚未就绪」并计入熔断。见 §4.7 |
| **handler 注册 ≠ 初始化完成** | 注册 handler 之后脚本还要拉远程配置、发 `inited`，这之前 `getPlatforms()` 是 `[]`、探测必报「服务初始化中，请稍后」。导入后立刻测试就会踩到（好音源被判不可用）。修法 `waitInited()`，见 §2 规则四 |
| **`probeSource` 的 `error` 已翻译过** | 要人话就用 `rawError` 再 `explainSourceError`；对 `error` 再翻译一次会套娃。`explainSourceError` 现已幂等，见 §2 规则五 |
| **飞牛「检查连接」在本地开发环境必然是 503** | WSL 上没有 `/var/run/trim_music.socket` 与飞牛音乐库，属设计内行为。别当 bug 查，看报错原文即可 |
| **飞牛音乐库路径别用旧文档里的值** | 真实路径是 `/var/apps/trim.music/var/db/music.db`（取自官方 FPK 二进制）。旧文档的 `/var/lib/fnos-music-db/...` 是错的，照抄会 503 |
| **判断「是不是音源脚本」千万别扫源码文本** | 混淆脚本的标识符是 `\u006c\u0078` 这类转义拼的，明文 `lx.on(` 根本不出现。实测一个能用的脚本七个标记全不命中，被误杀在导入之外。**要执行后再判定**（有没有 `inited` / 有没有注册 handler）—— 觅音就是这么做的，见 §4.7 |
| **探针曲目必须按平台给** | 曲目 ID 是平台私有的，拿酷我的 ID 去问 QQ 必然取不到，会把好音源误判成坏的 |
| **拖拽高亮别用 dragenter/dragleave 计数** | 鼠标划过子元素时 `dragleave` 会冒泡出来，各浏览器 enter/leave 的顺序与次数还不一致 —— 计数法要么闪一下、要么漏一次 leave 就永久亮着。用 **dragover 心跳**：指针停留期间 `dragover` 约每 50ms 触发一次，它一停就说明走了，150ms 无 `dragover` 自动熄灭（`SourceManager.vue` 的 `onDragOver` / `stopDragHighlight`） |
| **弹窗里的拖拽必须在 document 上挡一层** | 只给落点绑 `preventDefault` 的话，拖到弹窗**外面**松手浏览器会直接打开那个 `.js`、页面跳走、已填输入全丢。打开弹窗时在 `document` 上挂 `dragover`/`drop` 的 preventDefault，关闭与 `onUnmounted` 时摘掉（`guardPageDrop`） |
| **后台巡检千万别全量重探** | 音源一多就卡（用户 2026-09-30：「音源多了是不是会卡顿，我感觉很卡」）。旧实现每 5 分钟把 `sources` **全量串行**探一遍（含未启用、含已失效），单源最坏 ≈17s（`waitInited` 5s × 3 次重试），29 源一轮 60~90s、主线程阻塞 1.8s、前 30 秒卡在「检测中 0/29」——音源越多越接近「永远在探」。修法见 `services/probePlan.js`：只探**启用池 + 从没测过记录的**，单轮预算 `SWEEP_BUDGET=6` + 游标轮转，探测走 `probeOne(id, { light: true })` |
| **`probeOne` 的两种模式别搞混** | 后台巡检用 `{ light: true }`（只试 1 次、不等 init、「还没准备好」不记失败）；**导入流程与手动「全部检测」必须用默认全量重试** —— 用户在等结果，值得多试几次等脚本 init 完。轻量模式一旦记失败，正常音源会被误判红点，再被「清理失效音源」连坐删掉 |

---

### 4.7 怎么查「同一个音源，别处能用、我们跑不了」

> 音源脚本普遍经过 **VM 混淆**（标识符是 Unicode 乱码、字符串编码、控制流扁平化），
> **静态阅读基本无效**，必须靠运行时观测。以下是实测有效的一套流程。

**第一步：先分清「加载失败」还是「取链失败」。** 不要一上来就怀疑兼容层。

```js
// 浏览器控制台
JSON.stringify({
  loaded: await window.lxRuntime.loadScript({id:'t',name:'t',version:'1'}, scriptText),
  hasHandler: window.lxRuntime.requestHandlers.has('t'),
  platforms: window.lxRuntime.getPlatforms('t'),
})
```

| 现象 | 含义 |
|---|---|
| `loaded: false` | 脚本初始化抛错（`loadScript` 是 try/catch 后返回 false，**不抛异常**，要看 console） |
| `loaded: true` 但 `hasHandler: false` | 两段式脚本还没注册完（**等一下再看**），或它根本不注册 |
| `hasHandler: true` | 初始化 OK，问题在取链 |

**第二步：绕开多音源轮询，直接调它自己的 handler。** 多音源轮询会**掩盖**单音源故障 ——
某个源坏了，`getMusicUrl()` 会静默用别的源成功返回，你会以为「没问题」。

```js
const h = window.lxRuntime.requestHandlers.get('t')
h({source:'wy', action:'musicUrl', info:{type:'320k', musicInfo:{source:'wy', id:'2668397359', songmid:'2668397359', name:'晴天', singer:'周杰伦'}}})
```

**第三步：抓出站请求 —— 但⚠️ 千万别改脚本源码。**
我们实测过：**把打桩前置到源码字符串会改变脚本行为**（脚本对自身源码做完整性校验，
服务端直接回 `403 脚本完整性验证失败`）—— 典型观测者效应。
**正确做法是在 XHR 层抓包**（`XMLHttpRequest.prototype.open/send` 打桩），脚本完全无感。

**第四步：用「脚本自己的日志 + 服务端原话」定归属。**

| 观察 | 结论 |
|---|---|
| 请求发出去了、服务端回业务错误码 | **不是兼容性问题**，看服务端说什么 |
| 服务端说「版本过低 / 请升级」 | 脚本被作者废弃了，换新版即可（实测 `[独家音源] v4` 就是这种） |
| 请求 URL 里带着 `sign=fail` 之类 | 脚本自己签不出来 —— 多为配套签名服务已停，同样换新版 |
| 上游回 502 / 404 / nginx 错误页 | 上游故障，不怪我们 |
| 抛 `Invalid count value` / `X is not a function` | 才是我们的沙箱问题，见 `lx-compat.js` 顶部注释 |

> **判据**：脚本能加载、能注册 handler、能发出请求、能收到响应 → 沙箱兼容性没问题，差异只可能在上游。

**第五步（定论手段）：把它丢进参考实现的沙箱跑一遍做对照。**

只要同一脚本在**两边逐平台结果一致**，就可以拍板「不是运行时兼容性问题」。
工具已备好：`scripts/miyin-sim.mjs`（忠实复刻觅音的 Node `vm` 沙箱：
`globalThis`/`global` 指向 sandbox、`md5` 返回 hex 字符串、`rsaEncrypt` 走 Node 原生
`RSA_NO_PADDING`、`zlib` 用 `node:zlib`，并且**给 `currentScriptInfo` 带上 `rawScript`**）。

```bash
node scripts/miyin-sim.mjs <脚本路径> wy,tx,kg,kw
```

> 实测样例：`lx-music-source-v6 (修复).js` 在两边都是
> wy/kw → 上游 nginx **502**、tx → 上游 `{"code":2,"msg":"Object trans failure: 140017."}`、
> kg → 成功拿到直链。**三次重复一致** → 上游问题，不是我们。

**两个必踩的坑**

1. **轮询会骗你**。`getMusicUrl()` 会跨音源轮询，别的音源成功就会静默顶替，
   你会误以为被测脚本「能用」。**必须用 `probeSource()` 绕开轮询**。
   辨认方法：看日志里的接口域名（各音源的上游不同）。
2. `音源管理` 里那些几十字节的「音源甲/乙」是占位脚本，**永远不会注册 handler**，
   不要拿它们当故障样本。
3. **刚导入的源要等初始化**。`probeSource()` 只等 handler（2s），脚本还没发 `inited` 时会回
   「服务初始化中，请稍后」——这是**新源正常现象**，不是坏了。要等就 `await waitInited(id, 5000)`
   （见 §2 规则四）；测试完记得把临时导入的源 DELETE 掉，别污染 `/api/sources`。

### 4.8 怎么验证「写文件」类的功能（**可复用手法，强烈建议照做**）

这类功能（打标签、补歌词封面、整理去重）的开发环境通常**没有真实音频文件**，
也装不了 ffmpeg —— 于是很容易只跑个编译就说「做好了」。**这套手法能在没有真实曲库的
情况下跑完整条链路**，本项目 2026-09-18 就是靠它挖出了两个真 bug（见下）。

#### 三个关键技巧

**① 造一个「只满足魔数检查」的最小文件**

`tags.SniffFormat` 只看文件头：首 3 字节是 `ID3` 就判为 MP3（`fLaC` 判 FLAC）。
而 ID3v2 的写入是「**前置新标签 + 保留原有内容**」，不解析音频帧 ——
所以一个 210 字节的文件就能走完整条写入路径：

```python
header = b"ID3" + bytes([0x03, 0x00, 0x00]) + bytes(4)   # ID3v2.3 空标签头
open(path, "wb").write(header + b"\x00" * 200)            # 200 字节填充
```

**② 往 `library_index.json` 注入记录**

补全的挑活靠索引（`Index.NeedingTidy`）。索引文件在 `<dataDir>/library_index.json`，
格式是 `{"version":1,"updated_at":0,"entries":{"<绝对路径>": {...}}}`。
把 `title/artist` 留空可以**逼它走文件名解析那条路**（否则索引里的值会直接生效，测不到解析）。

> ⚠️ **后端只在启动时 load 一次索引** —— 注入后**必须重启**才生效。
> 改完记得从 `.bak` 恢复再重启一次。

**③ 假 AI 服务（验证 AI 相关逻辑时）**

用 `httptest.NewServer` 起一个假的 OpenAI 兼容端点，按请求内容返回预设 JSON。
见 `pkg/complete/match_test.go` 的 `mockAIServer` 与 `pkg/ai/ai_test.go` 的 `chatServer`。
**注意**：断言时只取 `role != "system"` 的消息 —— system prompt 里也含「编号」等字样，
混在一起会让计数断言出错（这个坑踩过）。

#### 完整流程

```bash
# 1. 造文件 + 注入索引（脚本见历史提交；思路如上）
# 2. 重启后端（索引只在启动时 load）
# 3. 跑：POST /api/library/complete {"dir": "...", "dry_run": true}   ← 先预览
#        确认文件没被动过（ls -la 看大小、看有没有备份目录）
# 4. 执行：POST /api/library/complete {"plan_id": "..."}
# 5. 验结果：ls -la 看体积、strings 看 ID3 帧、grep 看 .lrc 内容
# 6. 验撤销：POST /api/library/complete/undo  → 文件应回到原始字节数
# 7. 清理：删测试文件、从 .bak 恢复索引、删 ai_backup、重启
```

#### 靠这套手法挖出的两个真 bug

1. **`tidy.TidyOne` 不下载封面** —— 它遇到 `CoverURL` 只记一条 `Skipped`
   （「封面需由调用方下载后以字节传入」），而 `Item.CoverBytes` 是 `json:"-"`，
   **HTTP 调用方根本传不进来**。结果：封面功能一直是个死结，传 `cover_url` 完全无效。
   `pkg/api/features.go` 里的 `fetchCoverForTidy()` **定义了却从没被调用过**。
   修法：下沉到 `security.FetchImage`，`HandleTidy` 与 `pkg/complete` 都用它。
   > **编译和单测都发现不了** —— 两边各自看都「没错」，接起来才是断的。
   > **跨模块的职责边界只有端到端跑一次才会暴露。**

2. **撤销后索引没刷新** —— 索引里的 `HasLyric/HasCover` 在还原后过期，
   不刷新的话下一轮补全会因为「索引说有歌词」而跳过，表现为
   **「撤销了但再也补不上」**。修法：用 `tidy.NeedsTidy(path)` 读实际文件重判。

---

### 附：参考实现觅音（miyin）的关键做法

拆包路径：`miyin-v0.5.1.fpk` → `app.tgz` → `server/.output/server/chunks/nitro/nitro.mjs`
（**未混淆，可直接读**；搜 `loadLxSource` / `classifySourceError` / `PROBE_TRACKS`）。

| 做法 | 觅音的实现 | 对我们的意义 |
|---|---|---|
| **判定「是不是音源」** | 执行后看 `didInit` + `handlers.length` | **不扫源码文本** —— 我们曾因此误杀混淆脚本 |
| **等初始化** | `initPromise` vs `INIT_WAIT_MS` | 脚本异步 `send('inited')` 不会被误判 |
| **平台兜底** | `platforms` 为空 → `["wy","kw","kg","tx","mg"]` | 宁可给默认值，不要直接判失败 |
| **报错分类** | `classifySourceError()` | 把 `unknow error` / `block ip` / 404 / 429 翻成人话 |
| **探针曲目** | `PROBE_TRACKS` 按平台 | 每平台一首能取到链的曲子，否则好音源被误判成坏的 |
| **沙箱加固** | `MAX_TIMERS`、`LOAD_TIMEOUT_MS` + `breakOnSigint`、rejection guard、按脚本路径熔断 | 我们对应的是 `CALL_TIMEOUT_MS` + `sourceHealth` |
| **脚本体积上限** | 2 MB | 与我们一致 |

> 它有而我们还没有的：**沙箱定时器数量上限**（防脚本开无限定时器拖死页面）、
> **按脚本路径的熔断**（我们按音源 id，效果相近）、
> **`breakOnSigint`**（Node 侧才有意义）。
> 后续要加固沙箱时，从这张表里挑。

---

### 4.9 扫码登录的两个坑（2026-09-19 真机发现并修复）

网易云与 QQ 的扫码登录都出过问题，**根因不同，但有个共同点：服务端都返回 200**，
所以「接口能通」不能证明「流程正确」。**唯一有效的验证是真机扫一次。**

#### 坑 A：网易云扫码被 App 拒 —— 根因是**请求形态**，不是 type（2.1.1 的结论已被推翻）

**现象**：二维码能显示，手机扫码后 App 弹「请切换其他登录方式或升级新版本再试用」，
**卡在扫码前，轮询一直是 801**。

**2.1.1 的判断（错了）**：以为 `type=1` 是废弃端类型，改成 `type=3` + 伪造 `chainId`。
真机实测（2026-09-19 下午，NAS 已升 2.1.1）**仍然被 App 拒扫**。

**现在的证据**（当天从官网登录框 JS `pt_frame_index_*.js` 逐参数核实）：
官方 web 扫码**至今用的就是 `type=1`**，但请求形态和我们的裸 GET 完全不同——

```
POST /api/login/qrcode/unikey          body: type=1        （不是 GET query）
POST /api/login/qrcode/client/login    body: key=…&type=1  （1 秒轮询）
二维码内容 = http://music.163.com/login?codekey=<unikey>   （无 chainId）
803 后登录 Cookie（MUSIC_U）走 Set-Cookie 响应头下发，随后直接调 /api/w/nuser/account/get
```

社区库 `@neteasecloudmusicapienhanced/api` 的 `login_qr_check.js` 与 `qrlogin.html`
demo 印证同一点：**803 的 Cookie 取自响应头**（`result.cookie.join(';')`），不是 body 字段。

**推论**：服务端可能按请求形态（POST 表单 + 站内会话 Cookie）给 unikey 打渠道标记，
裸 GET 申请出来的码会被 App 判为异常渠道。`type=1~10` 服务端一律放行这点不变
——「服务端返回 200」依然证明不了任何事，**判别只有真机扫码**。

**当前实现**（`pkg/account/netease.go`，已照官方形态重写，⚠️ **待真机验证**）：

- 引导 GET `/` 收 NMTID 等初始 Cookie（失败不拦）
- POST 表单申请 unikey；会话 Cookie 存入 `neteaseQRSessions`（key → jar，TTL 10 分钟），
  轮询请求原样带上
- 二维码 URL = `https://music.163.com/login?codekey=<unikey>`，**无 chainId**
- 803 时 Cookie 优先取**本轮响应的 Set-Cookie**（须含 MUSIC_U），body.cookie 兜底
- 活测：`LIVE_NET_TEST=1 go test ./pkg/account/ -run TestLive` —— 已在线验证
  POST+type=1 能拿到真 unikey、会话轮询回 801。**扫码后是否被 App 接受仍未验证。**

⚠️ **别再翻 type 的值**。三次翻转（1→3→1）证明它不是变量；要动先抓官方 JS 对照。

**🔨 当晚第三锤（v2.1.3，真机日志实锤）**：2.1.2 装到 NAS 后，「日志」页里
**轮询响应本身**就带着「请切换其他登录方式」的 message —— 拒绝发生在**服务端渠道判定**，
连 App 都还没参与。结合社区库源码：`@neteasecloudmusicapienhanced/api` 的
`APP_CONF.encrypt=true` → **默认全走 eapi 加密通道**（`interfacepc.music.163.com/eapi/...`，
AES-ECB `e82ckenh8dichen8` + `nobody{uri}use{text}md5forencrypt` 摘要，
header 参数内嵌 params、Cookie 头由 header 拼、**会话靠 deviceId 串**）。
明文 `/api/` 扫码通道已死，flow 的网易云能用正是因为它依赖这个库。

**v2.1.3 实现**（`netease_eapi.go` + `netease.go` 扫码函数）：
- `eapiPost(uri, data, deviceID, jar)`：加密、POST、Set-Cookie 收集，一套封装
- 游客身份：`/api/register/anonimous`（xor+md5+base64 的 username）进程级懒取 MUSIC_A；
  NAS 上实测**拿不到 MUSIC_A 也不拦**（2.1.3 活测时游客注册未回 token，
  unikey/轮询仍正常 —— 说明该步目前非必要，留着贴近参考实现）
- 扫码会话：`neteaseQRSessions[key] = {deviceID, cookies}`（TTL 10min），
  申请与轮询共用同一 deviceId
- 轮询 message 含「切换/升级」时**专门记一条日志**（渠道被拒的判别信号）
- 活测（LIVE_NET_TEST）：eapi 拿到真 unikey、轮询回 801 等待扫码，
  **不再出现渠道拒绝文案** —— 但 App 放行与否仍待真人扫（v2.1.3 就是拿去扫的）

**✅ 2.1.3 真机定案（当晚 NAS 日志）**：`[网易云扫码] 803 授权成功，Cookie 来源=响应头 长度=1810`
—— **eapi 通道真机走通，网易云扫码登录正式可用**。游客注册未回 MUSIC_A 也不影响。
本坑到此闭环；再动扫码前先读这段。

#### 坑 B：QQ 换 `p_skey` 少了一步 `check_sig`

**现象**：二维码正常，手机扫码后前端显示「已扫码，请在手机上确认」，
然后**卡死不动**，日志报 `QQ 授权没有返回 p_skey`。

**根因**：老流程是「拿 `ptqrlogin` 返回的跳转地址去请求，响应头里就会带 `p_skey`」。
**现在不会了** —— 必须多走一步：

```
ptqrlogin 返回跳转地址（里面带 uin 和 ptsigx）
  ↓  ⚠️ 从跳转地址里用正则抠出 uin / ptsigx
GET ssl.ptlogin2.graph.qq.com/check_sig?uin=&ptsigx=&...   ← 这一步才换出 p_skey
  ↓
POST graph.qq.com/oauth2.0/authorize                        ← 必须带上面那份完整 Cookie
  ↓
code 换 musickey
```

注意两个易错点：

- **`check_sig` 的域名是 `ssl.ptlogin2.graph.qq.com`**，与 `ptqrlogin` 用的
  `ssl.ptlogin2.qq.com` **不是同一台**，抄代码时极易写混。
- **`authorize` 要带 `check_sig` 返回的整份 Cookie（含 p_skey 本身）**，
  只带 `qrsig` 会被拒。所以 `qqExchangePsKey` 返回的是 Cookie map 而不是单个 skey 字符串。

参数表（`uin/pttype/service/nodirect/ptsigx/s_url/ptlang/ptredirect/aid/daid/
pt_login_type/pt_3rd_aid`）**逐项抄自**社区维护的 QQMusicApi（见 §9），**不要省项**。

**⚠️ 2026-09-19 晚补记**：NAS 升到 2.1.1 后用户实测 QQ **仍然**停在「已扫码」或报
`没有 p_skey`。该链路已对照 QQMusicApi 逐项核实，且申请+轮询在线活测通过
（`TestLiveQQProbe`），失败点在**真人确认之后**的 check_sig/authorize 环节，
服务端侧无法复现。判别手法：`node scripts/qq-checksig-probe.mjs` 真人扫一次 ——
轮询不回 0 = 上游状态机/会话问题；回 0 但 check_sig 不下发 p_skey = 参数或风控问题。

**🔨 2.1.3 真机日志定界（v2.1.4 修复）**：check_sig 实际回了 **HTTP 302 + `p_skey_forbid` +
空 `p_skey`** —— 腾讯**风控拒发**，不是漏步骤。与参考实现唯一差异：
QQMusicApi 的 check_sig **不带任何 Cookie**，我们带了整个会话 jar（superkey/ETK/RK…）。
v2.1.4 起：`qqExchangePsKey` 裸请求（无 Cookie 头），`authorize` 只带 check_sig
**新下发**的那份（不与 qrsig/ptqrlogin 的合并）；`p_skey_forbid` 单独识别并给出
「QQ 风控拒绝」的人话报错。⚠️ **待真机**：若裸请求仍 forbid，下一跳是 IP 出口风控，
不是代码问题（别再改参数表——那张表已逐项对过）。

**🚫 2.1.4 真机回报：裸请求仍 forbid（同一出口 IP）** —— 代码侧差异已全部排除。
**无手机判别实验（2026-09-19 深夜，已做）**：拿假 ptsigx 直接打 check_sig → 回 **403**；
真实扫码流 → **302 + p_skey_forbid**。两者不同 ⇒ **不是 IP 级无差别封锁**，
forbid 跟着**会话/账号**走 → 头号嫌疑 = 该 QQ 账号开了「登录保护」或被腾讯标记。
按此顺序判别（别再动代码）：
① 手机 QQ「设置→账号安全→登录保护」关掉再扫（`p_skey_forbid` 的头号成因就是它，专拦第三方登录）；
② 仍 forbid → **换另一个 QQ 号**扫一次（区分账号被标记 vs 客户端环境被标记）；
③ 仍 forbid → 电脑连**手机热点**跑 `node scripts/qq-checksig-probe.mjs` 扫码：
热点成功 = NAS 宽带出口 IP 被精细标记；热点也 forbid = Node 与 Go 的 **TLS 指纹差异**，
届时才考虑换 HTTP 客户端库。

**⚠️ 升级后界面是旧的 ≠ 包没打好**：前后端同一个二进制，页面是升级前加载的旧 JS，
**刷新浏览器（F5）** 即可；`index.html` 已是 no-cache，不存在缓存钉死问题。
（2026-09-19 用户实测 2.1.4 时误判过一次，排查前先确认对方刷新没有。）

**✅ 2026-09-20 最终解法（v2.1.5）**：上面 ①②③ 都不用查了 —— 直接换通道。
**QQ 登录改走「QQ音乐 App 扫码」**（官方网页版同款，凭据经 MQTT 推送，不碰 check_sig），
见 §11 ⑱ 与 `qq_mobile_qr.go`/`qq_mqtt5.go`。旧互联通道保留为回退。

#### 怎么查这类问题（可复用手法）

```bash
# 1. 先确认服务端「有没有响应」——这一步通常都是通的，但先排除
bash scripts/probe-wy-qr-check.sh      # 网易云：申请 key → 轮询 → 二维码 URL 可达性
bash scripts/wy-type-probe.sh          # 探测 type 参数各家取值（会发现 1~10 全放行）

# 2. 关键：拿参考实现做对照，逐项比对，而不是自己猜参数
#    对照对象见 §9，两者都是当前可用且维护中的实现
```

> **方法论（§4.7 那条的又一次应用）**：`lx.utils.crypto.md5` 那个坑是靠「同一个脚本
> 放进参考实现跑」定位的；扫码登录这两个坑同样 ——
> **先把参考实现的原始代码挖出来，逐参数比对**，比自己对着文档猜快得多。
> 本项目原有的 QQ 实现是照 flow 移植的，而 **flow 自己也漏了 `check_sig`** ——
> 说明「有参考实现」不等于「参考实现是对的」，**要找活跃维护的**。

---

### 4.10 界面挂载点与「刷新就没」的三个坑（v2.1.21）

三个都是**用户真机点出来的**，共同点是「代码看着没错，用起来是坏的」。

#### ① 弹窗挂在条件渲染的容器里 → 按钮「点不动」

**症状**：「发现音乐」页点「设置目录」没反应（用户 2026-09-22 反馈；订阅歌单后尤其明显）。

**根因**：`SearchView.vue` 的按钮只调 `downloadManager.openDirModal()`，它只做一件事 ——
把 `showDirModal` 置 `true`。而**真正渲染这个弹窗的是 `DownloadQueuePanel.vue`**，
那个面板藏在 `DownloadDrawer` 的 `v-if="downloadManager.isOpen"` 里：

| 场景 | 抽屉 | 弹窗有没有宿主 | 点「设置目录」 |
|---|---|---|---|
| 抽屉开着 | ✅ | ✅ | 正常弹出 |
| 抽屉关着（含订阅后自动下载） | ❌ | ❌ | **标志位改了，没任何组件渲染 → 看起来点不动** |

订阅后的自动补下载走 `addSong(autoOpen=false)`，**不会打开抽屉**，所以订阅场景必现。

**修法**：把弹窗抽成 `frontend/src/components/DownloadDirModal.vue`，在 `App.vue` **全局挂一次**。
顺带解决第二个问题：`DownloadQueuePanel` 同时挂在抽屉 / 完整队列页 / 曲库管家里，
弹窗留在里面会**重复渲染多份**。

⚠️ **通用规矩**：**条件渲染容器内的全局弹窗 = 定时炸弹**。
凡是靠一个「服务层标志位」驱动的弹窗/抽屉，宿主必须挂在**无条件渲染**的位置
（App.vue 顶层 + `Teleport to="body"`），不要塞在某个 `v-if` 面板里。

#### ② 状态机多了一档，UI 没跟上 → 任务「看得到动不了」

**症状**：下载面板点过「停止全部」后，那些任务既没有状态文字、也没有任何按钮
（用户原话：「下载-停止后，已停止增加恢复下载」）。

**根因**：`stopAll()` / `stopByRow()` 会把任务置为 `stopped`，但 `DownloadQueuePanel.vue` 的两处
`v-if` 链都**没有 `stopped` 分支** —— 状态徽标链到 `failed` 就结束，右侧按钮组同理，
连「移除记录」都只认 `success || failed`。所以那一档只能筛出来看，**动不了**。

**修法**：
- 状态徽标补 `stopped` →「已停止」（`Square` 图标 + `task.error` 进 title）
- 右侧补「恢复下载」按钮 → `downloadManager.resumeTask(task.id, activeSource?.id)`
- 「移除记录」放开给 `stopped`
- 顶部补「全部恢复」→ 新增 `downloadManager.resumeStopped()`

⚠️ **service 层本来就能救**：`resumeTask()` 会复位 `_aborted`（不复位的话 `executeTask`
一进去就 `isStopped()` 返回，表现为「点了没反应」）。缺的只是 UI 入口 ——
**加功能前先确认 service 层有没有现成能力，别重复实现**。

⚠️ 「全部恢复」**刻意与「全部继续」分开**：`resumeAll` 只管 `paused`（用户主动暂停），
混进 `stopped`（用户明确停掉）会让「全部继续」越权恢复用户不想跑的任务。

#### ③ 同一页两种持久化口径 → 数据「刷新就没」

**症状**：曲库体检的 4 个数字刷新页面就消失（用户要求「修改为保持显示」）；
而同页「上次整理快照」是留着的。

**根因**：`audit` 只是组件内 `ref`（`LibraryManager.vue`），而 `indexStats` 走后端持久化
（`library_index.json`）。**同一页两种口径**，用户一眼就能看出割裂。

**修法**：用 `services/prefs.js` 现成的 `saveCache/loadCache`（**本地 localStorage + 后端双写**，
跟着设备走）：
- 键**按目录分开**：`last_audit:<dir>` —— 换目录不该看到别的目录的数字
- `ttl` 传 **0 = 永不过期**（用户要的就是「一直看得到」）
- `auditAt` 为 0 表示「本次刚跑」，非 0 表示「从缓存恢复」→ 只有后者显示「上次体检结果（时间）」
- `watch(tidyDir, restoreAudit)`：目录一改就换成那本目录的结果，没有就**清空**

⚠️ 写「上次 X」这类提示时，**必须区分「本次结果」与「缓存结果」**，
否则刚点完体检就显示「上次体检结果」是错的。

---

### 4.11 飞牛曲库重扫的频率，与「飞牛里没有封面」（v2.1.22）

#### ① 重扫频率：3 分钟 → 10 分钟（`pkg/fnos/rescan.go`）

用户原话：「音乐任务为什么一直扫描，这个项目在一直触发吗？」

**先回答「是不是我们在触发」：是。** 全仓库真正落到飞牛接口的只有一处
（`fnos.Music.ScanLibrary()` → `POST /shared-library/scan-all`），但有 6 个入口：
下载落盘（`downloader.go`，**每成功一首各调一次**）、整理批次收尾、补全任务收尾、
手动 `POST /api/fnos/rescan`、前端「下完自动推」、前端手动按钮。
它们全部进同一个**进程级**调度器（`defaultRescan`），所以是被合并 + 节流后的触发，
**上限 = 每 3 分钟最多 1 次**（debounce 30s 只是「最后一批之后再等 30 秒」）。

**那为什么看起来「一直在扫」？** 三个破口：

| 破口 | 说明 |
|---|---|
| **从不查询扫描是否结束** | `ScanLibrary()` 只发一个 POST，返回体里没有任务号/进度；我们只能知道自己「发起了」。**单次全量若超过 3 分钟**，就是上一轮还没结束、下一轮已经到达，从 NAS 上看首尾相接 |
| **最小间隔从「发起」计时** | 原来 `lastRun` 在执行**前**写入 → 间隔是「距上次开始」而不是「距上次结束」 |
| **`lastRun` 只在内存** | 进程重启即归零，重启后 30 秒又能扫一次，跟重启前那次毫无关系 |

**改法**（v2.1.22）：

- `rescanMinGap` **3min → 10min**（用户明确选「调得更安静」；代价是刚下完的歌进飞牛曲库最晚多等几分钟）
- `lastRun` **落盘**到 `<数据目录>/fnos_rescan.json`（新增 `SetRescanStatePath`，由 `server.go` 在启动时接上）；重启后沿用
- 间隔改为**按扫描返回后计时**（`scan()` 返回再写 `lastRun`）
- 顺手把 `StopLibraryRescan()` 接进 `StopScheduler()` —— 它以前是**没人调用的死代码**

⚠️ 这三条只解决「我们这侧的节奏」；**飞牛单次全量扫描本身要多久，本仓库测不到**
（`docs/飞牛刮削适配调研.md` 与 §8 都把这个列为未实测项）。真机上若还嫌频繁，
下一个可调的是 `rescanMinGap` 这个常量（有测试钉住数值，改时会提醒你想清楚）。

#### ② 飞牛不显示封面的三条成因

用户原话：「我在这个项目里看到有封面，在飞牛音乐里，歌曲就不显示封面。」

**第一件事是别被「我们能看到」骗了** —— 我们界面上那两张封面**都不来自文件**：

| 界面 | 封面从哪来 |
|---|---|
| 下载队列 / 播放器 / 搜索页 | 平台 CDN 的 `song.cover`（一条 URL，随队列持久化） |
| 「本地」页（NAS 曲库） | `GET /api/nas/cover`，它有**目录级兜底**：同名 `.jpg/.png`、`cover.jpg`、`folder.jpg`、`front.jpg`（`pkg/nas/scanner.go`） |

而飞牛**只读音频内嵌封面**（`docs/飞牛刮削适配调研.md`；为飞牛做的参考实现
`docs/参考项目/music-tidy/.../tidy_engine.py:1563` 也明确写「逐曲同名 .jpg 它并不识别」）。
所以「目录里有张图 → 我们显示、飞牛不显示」是完全正常的组合，**不是同一个东西**。

v2.1.22 修的三个**真缺陷**（都是「我们以为自己写了封面，其实没写进去」）：

| # | 缺陷 | 修法 |
|---|---|---|
| 1 | **MP3 重写标签会把已有封面删掉**。MP3 的策略是「丢弃旧 ID3 + 只重建受管帧」，`meta.Cover` 为空时旧 APIC 就没了；**FLAC 有保留逻辑，MP3 没有** —— 「只想改个歌词，封面却没了」 | `writeID3v2` 里镜像 FLAC：本次没带封面就先读回旧 APIC 再写。有正反两条测试（保留 / 给了新封面要覆盖） |
| 2 | **webp 封面被静默丢弃**。`downloader.detectImageMime` 只认 jpeg/png/gif，而 `security/image.go` 的 `FetchImage` 认 webp —— 同一张封面「补全能写、下载写不进去」，表现为封面时有时无 | `detectImageMime` 补 webp 魔数（`RIFF....WEBP`），并加测试确认 wav 不会被误判 |
| 3 | **抓封面失败完全静默**。`fetchCoverBytes` 以前失败一律 `return nil, ""`，用户只看到「飞牛里没封面」，分不清是「本来没图」还是「抓失败了」 | 改为返回失败原因，由 `applyTags` 写进**「日志」页**；**只记域名**（CDN 签名参数是敏感信息，不整条打链接） |

⚠️ **仍未解决 / 未验证的**：

- **非 MP3/FLAC 容器（ogg/ape/wav/m4a/dsf…）根本不写内嵌标签**（`tags.Write` 返回
  `ErrUnsupportedFormat`），下载路径也**不写任何 sidecar 图片** —— 这类文件在飞牛里注定没封面。
  这条本轮**没动**（要么加容器支持，要么写 sidecar，都是新工作量）。
- **已存在的文件不会被补封面**：`downloader` 对同名且 >500KB 的文件直接
  `already_exists` 早退，不会再写标签。历史文件要用「曲库补全」那条链去补
  （`complete` 的 `EmbedCover: true`）。
- **飞牛是否真能解析我们写的这套标签**（ID3v2.3 + 文本帧 UTF-16 BOM + APIC picture type=3；
  FLAC PICTURE type=3）**没有真机证据**。参考实现写的是 UTF-8 帧 —— 若真机上仍不显示，
  下一个怀疑对象就是帧编码/图片类型，而不是「没写进去」。

#### ③ 「本地音乐」把艺术家挪到标题第二行

用户要求：「把本地音乐里的艺术家加到歌曲标题/封面的第二行」。

`NasExplorer.vue` 的表头原有独立的「艺术家」列，窄屏被挤到 90px 并截断（要横向滑动才看得见）。
现在：艺术家放到**标题正下方第二行**（工程师视角：标题单元格 = 封面 + 标题 + 艺术家），
原本那行文件名只在「确实多带了信息」时以 `·` 追加（沿用 v2.1.17 的 `titleIsJustFilename` 判据）；
**独立的「艺术家」列随之删掉** —— 同一行出现两次同一信息正是用户 2026-09-22 反馈过的问题，
顺带也让表格更窄、少一次横向滚动。

---

### 4.12 手动搜索 / 更换封面（v2.1.23）

用户原话：「手动获取音源的封面或者搜索歌曲封面。」

在这之前，补封面只有「曲库管家 → 补全」一条路：它按歌名+歌手搜到曲目后直接取那首的封面，
**用户没得挑**。现在「本地」页每首歌多了一个「封面」按钮。

**两条腿分工**（复用已有能力，不另造写入口）：

| 环节 | 谁做 |
|---|---|
| 找候选图 | **新增** `GET /api/library/cover-candidates?keyword=&name=&artist=&limit=` —— 多平台搜、按**封面地址去重**、只返回候选，**不下载图片** |
| 写进文件 | **复用** `POST /api/nas/tags` 的 `cover`（图片地址）—— 那条路已有 SSRF 校验、12MB 上限、魔数识别 |

⚠️ **候选必须排序，不能只按平台顺序取**（首版实测踩到）：
搜「夜曲 周杰伦」时，第一个平台返回的**全是翻唱/伴奏**（`Xai小爱`、`Piano Echoes`…），
原专辑封面根本不会出现。现在的做法：

1. 四个平台**都搜**（不提前 break），每平台 10 条
2. 用 `pkg/match` 的 `NormTitle` / `NormArtist` / `PrimaryArtist` 给每条打分
   —— 歌名归一化后相等 +2、歌手对得上 +1
3. `sort.SliceStable` 按分排序（同分保持平台顺序），取前 `limit` 张

实测结果：第一张就是《夜曲》/ 十一月的萧邦（原专辑），翻唱掉到后面仍可选。

**前端**（`NasExplorer.vue`）：每首歌右侧「封面」按钮（`ImageIcon`）→ 弹窗

- 输入框默认填「歌名 歌手」，可改词重搜，**也可以直接粘一张图片地址**
  （输入 `http(s)://` 开头时按钮变成「用这个地址」，走同一个写入接口）
- 候选以网格预览（`referrerpolicy="no-referrer"`，部分 CDN 有防盗链）
- 写入后：后端**清 NAS 扫描缓存**（`InvalidateScanCacheFile`），前端给 `cover_url` 加时间戳
  参数强制重新取图，并排一次飞牛重扫

⚠️ **只有 MP3 / FLAC 能内嵌封面**（`pkg/nas/tags.go` 的 `taggableExts`），
其它格式会在 `cover_error` 里说明原因 —— 这个字段是 v2.1.23 新加的，
以前封面失败是**静默**的（`fetchTagCover` 现在与 `downloader.fetchCoverBytes` 同口径：认 webp + 报原因）。
⚠️ 但那条路在 v2.1.24 之前**只传 path + cover 会把标题/歌手/专辑/风格/歌词一起抹掉**
（`tags.Write` 不是合并语义）—— 见 §4.13。

---

### 4.13 按字段补全「歌曲信息」=「AI 补全」子页（v2.1.24）

用户原话（附飞牛「歌曲信息」截图）：「让 ai 帮我补全这些歌曲，**一定要用搜索的，而不是 ai 瞎猜**。
我建议增加一个 ai 补全页面，分别可以补全哪些信息（取决于飞牛音乐可以使用哪些信息）」

飞牛那页有七栏，其中六栏有权威来源可补：

| 字段 | 来源 | 拿不到时 |
|---|---|---|
| 歌词 | 平台歌词接口（`search.FetchLyric`），写入前 AI 核验 | 跳过（宁缺毋滥） |
| 封面 | 命中曲目的封面地址 → `security.FetchImage` | 跳过 |
| 年份 | **命中曲目自带的发行时间**（网易 `publishTime` 毫秒 / QQ `pubtime` 秒） | 跳过并记 note |
| 曲序 | 命中曲目的专辑内序号（QQ 的 `cdidx`、网易的 `no`） | 同上 |
| 光盘 | 网易 `cd`、QQ `belongcd`/`cdidx`（`discOfQQ`） | 同上（单碟本来就没有） |
| 风格 | **只有 QQ 专辑详情接口**给真实值：`fcg_v8_album_info_cp.fcg?albummid=` → `data.genre` | 只有勾了「允许 AI 推断」才用 AI 兜底 |

⚠️ 四家平台的**搜索**响应都没有 genre，所以「风格」必须多一次专辑详情请求
（`pkg/search/album_qq.go`，带 1024 条内存缓存）。

**铁律：值只能来自「已经匹配上的那条命中」**。这条不是洁癖，是实测出来的：
搜「晴天 周杰伦」时**网易前三条都是翻唱**（`year=2025`、`track=1`），
QQ 才是原版（叶惠美 / `2003` / `track=3`）。取错来源就把翻唱版的序号写进了用户的原唱文件 ——
**写错比不写更糟**。所以 `resolveOne` 里 `Fill*` 全部取自 `hit`，
匹配不上（`MatchByNameSinger` 与 AI 都没定下来）就一个字段都不写。
联网回归用例 `TestResolveFillsMetadataFromHitLive` 钉的是**契约**而不是「今天哪个平台肯给」
（跑法 `LIVE_NET_TEST=1 go test ./pkg/complete/ -run Live`）：匿名搜索下各平台返回的内容会变，
实测过同一条用例一天命中 QQ（`年份=2003 曲序=3 风格="Pop 流行"`）、
另一天只有酷狗能匹配且三栏全空 —— 所以它只断言
「取不到必须逐个说明」+「有更全的命中不许 settle 在缺的上面」（见 §11 ㊵）。

**命中平台偏好（v2.1.27）**：勾了年份/曲序/光盘/风格时，`resolveOne` 不会在第一个规则命中就停，
而是继续扫后面的平台、换成能给出更多所勾字段的那条命中（候选全都过了同一道 `MatchByNameSinger`，
不存在"为了字段挑到别的歌"）；没勾这些字段时行为与以前一致，一次命中即停。

**兜底命中回主力平台借元数据（v2.1.29）**：主力平台一条都匹配不上、只有酷狗/酷我命中时，
`borrowMetadata` 拿**那条命中自己写的曲名+歌手**去主力平台再认一次，
**只借年份/曲序/光盘/风格** —— 歌词、封面、直链一律还用原命中，
否则就变成「标签是 QQ 的、音频是酷狗的」，那是比空着更糟的错法。
三道门槛：过同一道 `MatchByNameSinger`；`sameRecording` 的时长容差（两边都已知才判，
差 >15% 且 >10 秒算不同版本）；只填「这次勾了而原命中给不出」的字段。
借到了记在 `PlanItem.MetadataFrom`，预览显示「元数据借自 QQ 音乐」。

主力平台存在 `AppConfig.preferred_platforms`（默认 `["wy","tx"]`，`PlatformPriority()` 归一化），
消费两处：上面的借取顺序；`buildCompleteOptions` 在调用方没传 `platforms` 时按
`OrderedPlatforms(主力, 默认四家)` 排序 —— **兜底平台不会被丢掉，只是排后面**。
界面入口在「补全整理」的选项行（主力平台 chips，至少留一个，否则后端会悄悄退回默认，两边不一致）。

⚠️ **`POST /api/config` 是局部 patch，但 `Update` 对 `FnosToken` 是无条件覆盖**
（空串 = 清除手工令牌）—— 只发一个键的 patch 会把令牌悄悄清掉。
`handleConfig` 现在先看 body 里有没有 `fnos_token` 这个键，没有就沿用当前值。
**以后加"允许清空"语义的配置字段，记得同步这一步**，否则同一个坑再来一遍。

#### 必须先修的数据丢失缺陷：`tags.Write` 不是合并语义

做这个功能前发现（**不是本轮引入的，v2.1.23 的换封面就是受害者**）：
两种容器的写入策略都是「**删掉受管字段，只按传入值重建**」
（MP3 = `skipExistingID3` 丢掉整个旧 ID3；FLAC = `mergeVorbisFields` 先删受管 key）——
**没传的字段会被静默抹掉**。后果：

1. `POST /api/nas/tags` 只带 `path + cover`（v2.1.23 的手动换封面）⇒
   标题 / 歌手 / 专辑 / 风格 / 歌词**全部清空**；
2. 补全重跑时 `item.Lyric` 只在 `NeedLyric` 时填 ⇒ 已有歌词会被抹掉；
3. 只补年份 ⇒ 同样全清。

修法：`Write` 里加 `mergeTextFields`（先 `Read` 一次，把没传的字段补回来再分发）。
封面**不走这条**：FLAC 原样复用 PICTURE 块（字节级精确），MP3 在 `writeID3v2` 里读回 ——
合并会把精确块换成重编码，所以那里保持原逻辑。

真机验证（`.dev/music/01 夜曲.mp3`，走完整「补全 → 只换封面」序列）：

```
补全后：  title="夜曲" artist="周杰伦" album="十一月的萧邦" genre="Pop 流行" year=2005 track=1 disc=1
          lyric=1761字符 封面=21315字节
只传 cover 换封面后：全部字段原样保留，封面 21315 → 25015 字节
撤销补全后：  genre="" year=0 track=0 disc=0 lyric=0 封面=0（文件回到补全前），缺口统计同步回退
```

回归测试：`pkg/tags/merge_test.go`（3 个，含「只写封面不该抹掉文本字段」的 MP3+FLAC 双跑）
+ `TestMP3WritesExistingID3v2PrefixedFile` 的断言**从「未传的 Artist 应为空」改成「应保留」**
—— 那条断言锁的正是这个 bug。

#### 索引：「读没读过」改用 `Entry.TagReadAt`

`Unenriched` 原来是「Title 与 Artist 都为空」= 没读过。加了新字段之后这个判据会**永久跳过**
「有标题但从没读过年份/风格」的老记录 —— 界面显示「全库缺年份」，其实是没读。
现在改为看 `TagReadAt`（`MarkEnriched` 打点、`InvalidateTagRead` 清除）：

- 老索引该字段是 0 ⇒ 自动被当成待读，**升级后回读一遍**（每轮 `limit 500`，无需清索引重建）。
  ⚠️ 这意味着升级后的前几轮扫描会比以前慢（要重读全库标签），属预期。
- 读失败也要 `MarkTagRead` 打点（不动字段值），否则坏文件每轮被重读、白烧 IO。
- 补全撤销后**重新读实际文件**再回写索引（以前只刷 `HasLyric/HasCover`，
  新字段会留在补全后的值上 —— 索引就永久偏离文件了）。

缺口判定口径统一为「**不确定就算缺**」（`Fields.Missing`）：没读过的记录所有勾选字段都算缺，
宁可多排一次活（补全会先匹配，匹配不到就跳过），也不报「什么都不缺」。

#### 接口

- **新增** `GET /api/library/gaps?dir=` → `{dir, unread, gaps:{total,read,lyric,cover,genre,year,track,disc,any_field,unsupported,unsupported_exts}, writable_formats}`。
  只读索引、不开文件，所以 `unread` 必须一起返回：那些记录的缺口是「还没扫描」而不是「真缺」。
  ⚠️ `unread = total - read - unsupported`：**写不了标签的格式不能算「还没读」**（见下条）。
- **写不了标签的格式（非 MP3/FLAC）单独计数**，不混进缺口、也不排进补全队：
  `gaps.unsupported` + `gaps.unsupported_exts`（`{wav:12}`），`writable_formats:["flac","mp3"]`（来自 `pkg/audioext`）。
  以前 `.wav/.opus` 记录既被算进 `unread`（界面劝你点「增量扫描」，可它扫多少遍都读不出字段），
  又被 `AllFields().Missing()` 一律算「缺」⇒ 排进补全队每轮失败一次（`tidy.TidyOne` 明确回
  「暂不支持写入 wav 格式（目前支持 MP3 / FLAC）」），**`any_field` 永远减不到 0**。
  现在：`FieldGaps`/`NeedingFields` 都先摘出去；`POST /api/library/complete` 的提示/预览/直接跑三处都带
  「另有 N 首是补全写不了的格式（wav×1；本应用只能写 MP3/FLAC），已排除」，预览 `data` 也带 `unsupported`。
- `POST /api/library/complete` 多一个 `fields` 数组（`lyric/cover/genre/year/track/disc` 或 `all`）。
  ⚠️ **不传就还是只补「歌词 + 封面」**（老口径）—— 已有调用方不能因为后端加了六个字段就悄悄改写年份。
  `only` 仍在，但只在 `fields` 缺失时生效。
- 计划项（`PlanItem`）新增 `need_*` 与 `fill_year/fill_track/fill_disc/fill_genre`（**将要写入的值**，
  预览页显示「年份：2003（QQ 音乐）」）；结果新增 `year_filled/track_filled/disc_filled` 与
  `album/genre/year/track/disc` 实际值（调用方靠它们回写索引）。
- 顺手修了两处：`tidy` 的 `MetaWritten` 以前不含风格/数字字段（只补风格时 `GenreFilled` 误报 false）；
  `Summary.Failed` 以前把「没搜到」也算成「写入失败」（同一首计两次）。
- `POST /api/nas/tags` 的 `EmbedTagsRequest` 以前**没有 genre 字段** ⇒ 手动写标签永远改不了风格，已补。

#### 前端

「曲库管家」当时新增了独立子页 **「AI 补全」**（`activeTab === 'complete'`，图标 `Wand2`），
把原来散在「整理去重」里的补全预览/进度/撤销**整体搬了过来**
（两处渲染同一个 plan 会出现「一边确认过、一边还是旧的」）。四步：

1. **扫描缺失** —— 六个字段各缺多少 + 「N 首还没读过标签，先点增量扫描」
2. **勾字段** —— 每栏带来源说明（风格那栏明写「只有 QQ 命中才有」）；勾选存 `complete_fields` 偏好
3. **预览要写的内容** —— 「文件 → 匹配到（含平台名）→ 会写：年份 2003 / 曲序 3 / 风格 Pop 流行」，
   取不到的字段有 note 单列，不显示猜测值
4. **确认写入 → 进度 → 汇总（六个字段各补了几首）→ 撤销**

这一页只有一个目录输入框，体检/增量扫描/查重都吃它（两处各填一个目录最容易补错库）；
缺口统计按目录缓存（`last_gaps:<dir>`），刷新不丢（同 §4.10 的「刷新就没」处理）；
扫描 / 补全完成 / 撤销后都会重算缺口。
**v2.1.26 起「整理去重」已经并进这一页，页名改成「补全整理」**，见 §4.14。

字段表与展示逻辑抽到 `frontend/src/services/libraryFields.js`，
`libraryFields.test.mjs` 钉住「字段 key 必须与后端 `parseCompleteFields` 一致」——
拼错后端不报错，只会静默不补。

⚠️ **界面**：早期几轮测不了（浏览器自动化在本机被权限分类器拦，见 §4.10 末尾的替代验证配方），
但 2026-09-26 起能用 NAS 上的 ego 浏览器真机走了：`http://192.168.1.230:3000/` → 曲库管家 → 补全整理 → 点「扫描缺失」，
缺口行读回来是「上次统计（刚刚）· 共 7 首，其中 6 首读过标签 · 待补 6 首（…）·
另有 1 首格式写不了标签（wav×1），补全不会动它们（要补得先转成 MP3/FLAC）」，
六个字段格子（歌词 4 / 封面 4 / 年份 6 / 曲序 6 / 光盘 6 / 风格 6）与 `/api/library/gaps` 逐项一致。
**仍然没测**：真实观感（配色、密度、中文换行）与「预览 → 确认写入 → 卡片墙」那条动线。

#### 还没确认的（需要真机 / 样本）

- 飞牛读年份到底是 `TYER` 还是 `TDRC`、FLAC 是 `DATE` 还是 `YEAR`：
  **两种都写了**（MP3 同写 TYER+TDRC；FLAC 只写 DATE，读时两者都认），等真机反馈再收敛。
- 飞牛「风格」页能否接受 `Pop 流行` 这种「英文 + 中文」的平台原始串。
- 我们的 ID3v2.3 文本帧用 UTF-16，参考实现用 UTF-8 —— 目前实测能读出中文标题，先不动。

### 4.14 曲库管家精简成四个标签 + 补全结果卡片墙（v2.1.26）

用户原话：「曲库管家，很多页面感觉分类不够精简。整理去重感觉可以加入到 ai 补全，
ai 补全也可以更名补全整理，补全后显示补全歌曲信息展示：以卡片方式加下方已选择补全的信息内容。」
追问「精简幅度」时选了**顺手把下载也合了**。

**左侧栏 6 → 4：推送 / 下载 / 补全整理 / AI 设置**（v2.1.32 再 **4 → 3**：AI 设置整块挪去「账号连接」，见 §11 ㊺）

| 变化 | 为什么 |
|---|---|
| 「整理去重」并进「AI 补全」，页名 **补全整理** | 体检 → 看缺什么 → 补 → 查重，本来就是一条流水线；分两页最容易出的事故是**两个目录输入框填的不是同一个库** |
| 「歌单下载」并进「下载」，页内两个子视图（`downloadSub`，记忆键 `download_sub`） | 底下是同一个下载队列，只是提交入口不同；分两页会让人先找「我那条下到哪了」 |
| `LegacyTabs = { tidy: 'complete', playlist: 'download' }` + `applyTab()` / `normalizeTab()` | ⚠️ **必须留着**：旧值存在浏览器偏好（`library_tab`）和 `App.vue` 的跳转参数里，不映射就是点进来落空白页 |
| 指标行随子视图 / 页面切换（下载看队列或歌单两套数字；**补全整理不返回指标行**，见 §11 ㊹） | 原来「一页一套数字」是对的，合并后若不跟着切，会出现「标题写下载、数字是整理」；但补全整理页里指标行与缺口面板、体检卡片三处数字重复且口径不一，2026-09-23 用户指出后砍掉指标行 |

**一页的排布**：`目录 + 扫描缺失 / 增量扫描 / 体检 / 查找重复` 一排四个动作 →
缺口统计 → 勾字段 → 预览 → 确认写入 → 进度与撤销 → **补全结果卡片墙** →
下面一张独立卡片放「体检 · 索引 · 查重」的结果（**动作不在这里，只在上面那一排**，
两处都能点会让人搞不清哪份是当前的）。

**卡片墙的数据是回读文件得到的，不是补全结果里的值**：

- 补全跑完，逐首 `GET /api/nas/tags`（`mapWithLimit` 并发 4），显示
  封面缩略图（`NasAPI.coverUrl(path) + &v=<批次时间戳>` 防旧图）+ 歌名 / 歌手 / 专辑 +
  年份 · 曲序 · 光盘 · 风格 · 歌词 · 封面，**本次真写上的字段带 ✚ 角标**。
- 为什么坚持回读：结果说的是「我们打算写什么」，文件里是什么是另一回事
  （某首写失败、或文件被别的程序改过）。**界面上说"补好了"而飞牛里没有，是最伤信任的错法。**
- 没匹配上 / 写失败的一律进**另一组 amber 列表**并写明原因，绝不混进卡片墙。
- 单首回读失败也要出现在卡片里（退回用补全结果的值、不带封面）——
  「卡片凭空少了几首」比「某首显示不全」更让人怀疑整个功能。
- 为此后端补了 `GET /api/nas/tags` 的 `genre/year/track/disc`（以前只回标题歌手专辑，
  卡片就只能显示"打算写的"）。有测试锁：`pkg/nas/tags_read_test.go`。
- 卡片按目录缓存（`last_complete_cards:<dir>`），刷新不丢；撤销时**一起清空**
  （留着就是在展示一批已经不存在的标签值）。

纯逻辑（`summaryBar` / `cardModel` / `cardFields` / `splitCards` / `mapWithLimit`）
都抽在 `frontend/src/services/libraryFields.js` 并有单测，组件里只留渲染与取数。

⚠️ **界面未实测**（老问题：浏览器自动化被权限分类器拦，见 §4.10 末尾）。
本轮做到的：`vite build` 通过 + 前端 133 测试全绿 + 后端全绿 +
用 `curl` 验过 `GET /api/nas/tags` 新字段与 `version=2.1.26`。
真机要看：左侧栏四项是否都在、旧入口（下载抽屉、任务状态）跳转是否落到正确子视图、卡片墙排版。

---

### 4.15 播放链路：点歌不响的三层出站根因 + 切歌语义（2026-09-27）

用户报「点击列表歌不自动播放 / 下一首无效 / 切歌总弹大屏」。真机复现时 LX 取链**全 502**，
剥开是三层（都在**后端出站链路**上，与前端无关）：

| 层 | 现象 | 根因 | 修法（`backend/pkg/security/security.go`） |
|---|---|---|---|
| 1 | `proxyconnect tcp: dial tcp 192.168.1.3:7890: 禁止访问内网/元数据地址` | 我们自己的 SSRF `Dialer.Control` 拦下了**用户自己配置的代理出口**（代理是内网地址） | `proxyExemptAddrs()` 把环境里配的代理加进豁免白名单；`dialControl(opts, exempt)` 先查豁免再判内网。**豁免是固定白名单**，没配在环境里的私网地址照旧拦 |
| 2 | `Post …: EOF`（明文 HTTP 经代理通、HTTPS/CONNECT 不通） | 后端对公网**无条件走代理**，代理的 CONNECT 隧道坏了就全挂 | `proxyFallbackTransport`：**只在「没拿到响应 + 连接层错误 + 请求体可重放」时直连重试一次**；拿到任何响应（哪怕 502）绝不重试；单次经代理上限 5s，失败后 60s 冷却（冷却期内 `proxyFunc` 直接返回 nil）。**流式中继用 `NewSafeStreamRoundTripper`（无上限）**，否则 5s 会掐断长音频响应体 |
| 3 | `http2: timeout awaiting response headers` 卡满 20s | `newSafeTransport` 里 `ForceAttemptHTTP2: true`，而音乐站多挂在 CDN 边缘（openresty / EdgeOne）上：宣告支持 h2 却把 Go 的 h2 客户端挂到 `ResponseHeaderTimeout` | 改为 `ForceAttemptHTTP2: false`（同一地址 HTTP/1.1 只要 0.2s）。回归测试 `TestOutboundTransportDoesNotForceHTTP2` 钉住 |

**切歌语义（前端 `frontend/src/App.vue`，用户口径）**

- 手动「上一首 / 下一首」**一律按当前列表顺序**（`(i±1+n)%n`）。随机模式**只**作用于自动续播（`onEnded`）与「播放全部」的起始曲 ——
  以前 `playPrev/playNext` 在 random 模式下走随机，用户点「下一首」听到的是随机另一首，已被否掉。
- **点歌/切歌不再自动展开沉浸式大屏**。以前 `playSong()` 第一行就是 `showImmersive.value = true`，另有一处 `watch(isPlaying)` 也在自动展开；
  两处都删。现在只有顶栏「打开播放器」与播放条的歌词/全屏按钮才开。

**诊断手法（下次别在 HTTP 层猜）**

- 写个只调 `security.NewSafeClient` 的小 `main.go` 计时连打，比在接口层猜快得多；
  对比直连用 `env -u HTTP_PROXY -u http_proxy -u HTTPS_PROXY -u https_proxy -u ALL_PROXY -u all_proxy curl -4 …`。
- 浏览器侧 LX 运行时的请求走 **axios(XHR)**，`window.fetch` 埋点抓不到 —— 要抓得 hook `XMLHttpRequest.prototype.open/send`。
- **别在同一条命令里既 `pkill -f 'fn-dev-backend …'` 又 `go build -o ../.dev/fn-dev-backend`**：包装 shell 的命令行含同样字符串，会自杀（本轮复现过一次）。停 dev 后端用 `job_kill`。

⚠️ **上游数据质量（未改，待用户定）**：现用音源脚本对 `kg` / `tx` 源解析出的直链都落在 `cdn-img.gitcode.com`、
是**固定 171,072 B ≈ 7.1 秒**的占位片段（不同 songmid 字节数完全相同）；同一接口的 `kw` 源正常（`car-bj.kuwo.cn`，3.4 MB 完整曲）。
**我们的中继没有截断**（`/api/player/stream` 与源站字节数一致）。所以「点 kg/tx 行的歌听 7 秒就跳」是上游给的片段。

---

### 4.16 入口的信息架构：扫码登录只在「发现音乐」，开放接口在左上角图标（2026-09-28）

**改之前是「一份功能、两个入口、其中一个是假的」**：平台扫码登录的完整实现（卡片 → 二维码 → 轮询 → 登出 → 日推/歌单预览）
全在「账号连接」，「发现音乐 → 双平台推荐」只有一句「去扫码登录」的跳转；而「开放接口」是顶栏右侧一行文字按钮，
紧挨着一个 9×9 的软件图标，看起来像两个并列入口。用户提出合并后：

| 想干什么 | 现在的唯一入口 |
|---|---|
| 扫码登录 / 登出 / 换账号 | 「**发现音乐**」→ 双平台推荐的两张卡片（`SearchView.vue`） |
| 打开开放接口抽屉 | **左上角软件图标**（曲率 方块，`button.brand-halo`），提示是**图形不是文字** |
| 飞牛连接凭据 / AI 设置 | 「账号连接」（这一页只剩这两块 + 一句指路） |

**改的时候别踩**：

- 扫码状态判断抽在 `frontend/src/services/qrLogin.js`，**不要**在组件里再写一遍：
  网易云 `code` 803 成功 / 800 过期 / 801·802 用平台文案，QQ 看 `state`；**认不出的码不许当成功**
  （写错的代价是「显示登录成功但没登进去」或「永远停在等待扫码…」这种假状态）。
- **存盘失败不自动关弹窗**（`shouldCloseAfterLogin` 要求 `saved`）——那是用户唯一能看到失败原因的位置。
- 离开发现页必须停轮询（`SearchView.vue` 的 `onUnmounted` → `stopQrPolling()`），否则二维码过期后仍每 2 秒空打一次接口。
- 图标入口的动效写在 `frontend/src/style.css` 末段（`brand-halo-ring` 旋转 + `brand-disc` 呼吸）；
  顶栏那一行是 `overflow: hidden`，**光环只能溢出图标 4px**（36+4+4=44 < 顶栏 68px），别把 `inset` 调大；
  抽屉打开时 `animation-play-state: paused`，并且 `prefers-reduced-motion` 下全部关掉。
- 上游一致性：`AccountManager.vue` 已不含任何平台 API 调用，全仓只剩 `App.vue` 渲染它、`navItems` 里那一项、
  以及 `navBus` 注释提到 `accounts` —— 再想加回平台登录入口时先看这一节。

### 4.17 顶栏信息密度 · 抽屉方向 · 播放栏三形态 · 深色玻璃皮肤（2026-09-28）

**顶栏导航密度（三档，按实测宽度自适应）**：`navItems` 每项有 `name`（全名）与 `short`（发现/本地/管家/账号/日志）。
档位 `navDensity`（`full` / `short` / `icon`）存服务端偏好 `nav_density`，顶栏末尾一个图标按钮循环切换（说明写在 `title`）。
⚠️ **渲染用哪个档位由 `fitNavDensity()` 实测决定**（`App.vue`）：从用户选的档位开始量两个溢出信号 ——
顶栏那一行 `scrollWidth > clientWidth`、或导航条内部溢出 —— 放不下才降一档；`ResizeObserver` 盯着顶栏行，窗口/侧栏变宽变窄都重量。
**别退回 `sm:` 断点那套**：断点只看视口宽、不看这一行里其它东西占多少，用户 2026-09-28 的原话是
「上方的空了一大半就判定为最小的图标了」。导航项角标（下载中数量）仍挂在图标后面。

**开放接口抽屉默认「从左到右滑出」**：面板锚 `left-0`、入场 `-translate-x-full → 0`、`420ms cubic-bezier(.22,.61,.36,1)`。
⚠️ 这与「下载抽屉从右侧滑出」**方向相反是故意的**（用户明确要求）。抽屉补了 Esc 关闭（只在打开时挂 `keydown`）。

**logo 的提示动效**：只用「**内圈实心圆呼吸** + 柔光」，**外圈旋转方框已按用户要求删除**（`.brand-halo-ring` 与它的 keyframes 都没了，
别再加回来）。内圈呼吸靠 `circle:last-of-type` + `transform-box: fill-box`（不加 `fill-box` 会锚在 SVG 左上角、看着像平移）；
抽屉开着时 `animation-play-state: paused`；`prefers-reduced-motion` 下全停。

**全屏播放页（`ImmersivePlayer.vue`）：一套 DOM、一套纵向结构、三档尺度（v2.1.47 起）**：
- **三档都是纵向居中**：顶部导航 → 大封面 → 歌曲信息 → 沉浸式歌词 → 细进度条 → 播放控制。
  手机 `<768` / 平板 `768~1023` / 桌面 `≥1024`，差异只在**留白、内容容器宽、封面尺寸**，结构不变。
- ⚠️ **桌面端不做左右分栏**。用户 2026-09-29 的新 spec 原话：「不要为了'响应式'强行在桌面端增加左右两栏」，
  第七节也要求歌词是「轻量级浮动歌词层」而不是右侧黑色歌词栏。
- ⚠️ **口径变更史（改之前先问）**：v2.1.42–43 = 40/60 分栏 → v2.1.45 = 全端纵向 → v2.1.46 = ≥1100px 宽屏分栏
  → **v2.1.47 = 回到全端纵向**（用户给了完整新 spec）。**别再按「分栏更好」自作主张改回去**。
- **桌面垂直居中**靠一对 auto 外边距：`.ys-imm__body { margin-top: auto }` + `.ys-imm__bottom { margin-bottom: auto }`，
  把「封面+信息+歌词+传输条」整组居中、顶部导航仍钉在最上面 —— **不要用 absolute 定位**（spec 十一 明确反对）。
- **封面尺寸**：手机 `min(78vw, 380px, 42dvh)`；桌面 `min(clamp(280px, 30vw, 420px), calc(100vh - 500px))`。
  ⚠️ 那道 `calc` 高度预算是**必须的**：顶栏 56 + 信息区 107（含词曲行）+ 歌词间距 28 + 歌词窗 16vh + 传输条 125 + 外壳留白 40。
  曾经只留 480 且用 `40vh/30vh`，结果 1600×700 上封面被压到 210px（屏宽 13%），用户直接看出「主角没了」。
  **矮视口两档（`max-height: 700px` / `560px`）不要加 `max-width` 限定** —— 桌面纵向版同样会被矮窗口压到。
- **歌词窗在桌面档是有上限的**（`clamp(96px, 16vh, 200px)`）：大屏多出来的必须是留白，不是更高的歌词窗（spec 六）。
- **顶部只有两个圆钮**：左「收起」、右「更多」。收藏 / 播放列表 / 复制歌词 / 双语 / 字号全部收在「更多」浮层里
  （绝对定位锚在内容列右上角，**不进布局流**，实测打开时封面位移 = 0px）。
- **背景链路**：封面全屏铺满 → `blur(40px)` → `scale(1.1)` → `opacity .35` → 主色染色层 → 主色光晕 → 黑色三段渐变
  （`rgba(0,0,0,.15)/.45/.75`）。**背景只给颜色氛围，看不清封面细节**。
- **动态环境色**：`sampleAmbient()` 用一个**独立的 `Image` + 离屏 canvas 24×24** 采主色（不动页面上那张封面图，
  所以采样失败绝不影响封面显示）；跨域污染 canvas → `try/catch` 静默退回中性深色 `rgb(22,21,28)`。
  主色经 HSL 压成「深底色（L=.17）+ 光晕（L=.44）」。**换色必须平滑**：底色走 `background-color` 1.4s 过渡，
  光晕用 A/B 两层交叉淡入淡出（渐变本身不能 transition）。
- **毛玻璃**：`.ys-imm__icon-btn` / `.ys-imm__play` / `.ys-imm__sheet` / `.ys-imm__toast` 统一
  `rgba(255,255,255,.06)` + `backdrop-filter: blur(20px)` + `1px rgba(255,255,255,.08)`。**不许出现 `#111` 这类深色面板。**
- **歌词**：纵向嵌在封面与进度条之间，居中；上下 `mask-image` 渐变让两端消失（不是一块黑色歌词窗）。
  距离衰减由 `lineStyle(idx)` 出内联样式：当前 `0.9` / 相邻 `0.45`·`0.3` / 更远 `0.2`·`0.12`，
  非当前行叠 `blur(0.4~1.1px)`。⚠️ **`lineStyle()` 里必须带 `fontSize`** —— A−/A+ 靠它生效（漏了就是按钮失效，实测踩过）。
  滚动容器用 `::before/::after` 两个 42% 高的 flex 占位撑出上下留白（**不能用百分比 padding，那按宽度算**）。
- **矮视口分档**：`max-height:700px` / `560px` 两档收紧封面（`30vh` / 再收），并在 560 档把歌词 `min-height` 归零 + 内容区
  `overflow:hidden` —— 保证**任何尺寸下歌词区都不会和底部控制区重叠**（实测 844×390 横屏曾重叠 138px）。
- ⚠️ 别在同一个节点上既写 `pb-safe/pt-safe` 又写 `pb-8/pt-2` —— 安全区类会把它盖掉（踩过，底部留白变 0）。永远是外层安全区 + 内层 padding。
  （v2.1.45 起沉浸页直接读 `--safe-top/--safe-bottom` 变量，不再用那两个工具类。）
- 收藏（心形）目前是**本机收藏**（前端 prefs，key = 歌名 - 歌手），后端没有单曲收藏接口。

**播放栏三形态（高度恒定：手机 64 / 桌面 72）**：
⚠️ **播放栏是浮动层，不占布局行**（v2.1.41 用户要求："只要中间那条，两侧不要灰带"）：
外层 `fixed inset-x-0 bottom-0 pointer-events-none`，只有胶囊本身 `pointer-events-auto`（三个形态都要给），
这样内容从胶囊底下穿过、点两侧等于点到内容。**别再把它放回 flex 流里** —— 那样会在底部留出一条空白带（露出页面底色，看着就是灰条）。
配套要求：**页面底部要留 ≥96px 留白**（`PageShell.vue` 已带 `sm:pb-24`；新页面照抄，或直接用 `PageShell`），
否则滚到底最后一条会被胶囊压住。注意 Tailwind 的 `sm:py-7` 会覆盖前面的 `pb-28`，桌面端要单独写 `sm:pb-*`。

空态 = **搜索卡片（真输入框）**；播放态 = 左控制组 + 中封面信息进度 + 右功能按钮（右侧还带一个搜索按钮，
点了把中间那块就地换成输入框）；折叠态 = 32px 细条（歌名 + 展开入口）。收缩不影响播放。

⚠️ **搜索的家在底部播放栏，不在发现页**（用户 2026-09-28 定，我第一版做反了、被当场纠正
「你弄反了，是保留底部播放栏搜索，去掉发现音乐页面的搜索」）：
- 链路：播放栏 `emit('search', q)` → `App.searchFromPlayerBar(q)`（切到 `currentView='search'` + `barSearchNonce++`）→
  `SearchView` 监听 `barSearchNonce` 跑 `handleSearch()`。**用 nonce 不用 `watch(barQuery)`** —— 同一个词连搜两次也得触发。
- `SearchView` 顶部**不许再加搜索框**（加了就等于把入口搬回页面，用户否过）；搜索结果的「← 返回发现」按钮必须留着，
  否则进了结果页回不去。

**深色玻璃皮肤（播放栏 + 沉浸页共用）**，尺寸与模糊量**照 trim.music fpk 里的真实实现**（不是猜的）：
它的播放条是 `music-player-glass flex h-[72px] items-center gap-2 px-4` + `style={{ width: 576, marginInline: 'auto', borderRadius: 44 }}`，
玻璃 `.music-player-glass { backdrop-filter: blur(12px) }`，封面 `h-[38px] w-[38px] rounded-md`，信息块 `max-w-[234px]`，
**底色取封面**（`background: CV`，运行时算的）。我们对应为：
- `.ys-player` = **宽 `min(576px, 100vw-24px)` / 高 72（手机 64）/ 胶囊圆角**；
- `.ys-player-glass` = `blur(12px) saturate(120%)`（**别再写 20px**）；
- 封面 **38×38 `rounded-md`**；
- 底色层 = 封面自己「放大 + `blur(2xl)` + 压暗」当背景（等价于它的 `background: CV`，不采样、零成本；沉浸页用 `.ys-ambient__cover` 同一招）。

**（历史，v2.1.42–v2.1.43）沉浸式播放页曾照飞牛音乐的正在播放页做**，参数直接取自它源码：
```
--music-player-now-playing-cover-size : clamp(240px, min(30vw, 50vh), 480px)   （手机固定 240×240）
--music-player-now-playing-content-gap : clamp(24px, 5vw, 64px)
grid-template-columns : minmax(240px, var(--cover-size)) minmax(0, 1fr)
容器                    : max-w-[calc(cover + gap + 579px)]，md:pt/pb-[72px]
顶栏                    : h-[68px]；右侧竖工具条 w-20（80px），top-1/2 -translate-y-1/2
歌词                    : .music-player-karaoke-lyrics —— **隐藏滚动条**，逐行 opacity/filter/transform 过渡
封面装饰                : bg-black/35 压暗 + rotate-[-28deg] 斜高光 + rounded-tl-full 右下四分之一圆
玻璃面板                : rounded-[24px] + backdrop-blur
```
⚠️ **v2.1.45 已弃用上面这套**（网格分栏 + 固定封面尺寸 + 飞牛式顶栏）—— 保留在此只作参考，
**不要再照它改回去**。当前实现见本节上面「v2.1.45 起只有一套纵向沉浸式布局」。
（唯一仍然通用的经验：歌词滚动容器必须自己有确定高度，否则 `h-full` 解析不出、歌词会把整页撑高、
点歌词跳转后的自动定位会失准 —— 当年实测：修之前歌词框 2088px、修之后 568px 内部滚动。）

**logo 呼吸动效（2026-09-28 定稿）**：
- 呼吸的实体是**圆点外层的圆圈 `.brand-core-ring`**（绝对定位的 HTML 元素），**中间那个圆点 `.brand-core` 固定不动** ——
  用户口径：「圆点不变，是圆点外层的圆圈呼吸效果」。别让圆点自己缩放，也别用 SVG 里的 `circle` 做呼吸
  （**SVG 的 transform 不参与合成**，每帧重绘，慢速下肉眼可见地顿）。
- 柔光 `.brand-core-glow` **只动 `opacity`**，别再加 `scale`（缩放 radial-gradient 每帧重新栅格化）。
- 关键帧 `0% → 45% 顶点 → 55% 顶点 → 100%`（顶点留停顿），缓动 `cubic-bezier(.37,0,.63,1)`，周期 3.2s。
  两端硬碰硬的 `0/50/100 + ease-in-out` 会看出「一顿一顿」。
- 加新动效时记住这条通用经验：**HTML 元素动 transform/opacity 才在合成层；SVG 子元素动 transform、渐变动 scale 都不是。**

**没照搬的只有强调色**：trim.music 用红 `#f62c55`，曲率保留翡翠绿 —— 牌子色，别动。
环境光 `data-playing` 控制：未播放静止、播放才亮、`prefers-reduced-motion` 全停。

**滑动性能（v2.1.39，四条别踩回去）**：
1. **拖动位移绝不要放进 `ref`** —— 每帧改 ref 会让 SwipePager 重渲染，`<slot>` 会重新生成 vnode，
   于是**所有常驻页面的整棵树每帧 diff 一遍**。位移直接写 `track.style.transform`（rAF 里），Vue 完全不参与。
   ⚠️ 做这种"去响应式"重构时**必须实测**：本轮漏改一处 `animating.value`（布尔值上写属性）→ `begin()` 抛异常 →
   touchmove 监听没挂上 → 拖拽彻底失效，而构建和单测都是绿的。
2. **`content-visibility: auto` 只给「隔了两页及以上」的页面**（见 `isIdlePage()`）：
   - 当前页**绝不能加** —— 它隐含 `contain: layout paint`，会把当前页里的 `fixed` 弹窗（二维码登录、目录选择）变成相对页面定位。
   - **左右邻居也绝不能加** —— 加了之后那一页整段滑动都不渲染，等滑到位才被一次性渲染出来，
     用户看到的就是"下一页啪地出现"（59 轮踩过：为了省光栅化把翻页手感弄坏了）。
   - 只有隔两页以上、真正看不见的页面才跳过。省的是它们的渲染，不是"正在移动的那两页"。
3. **滑动期间关掉 `backdrop-filter`**：`html[data-swiping='1']` 时 `.ys-glass/.ys-player-glass` 的模糊置 `none`，手势结束恢复。
   背景内容在动时模糊每帧重算，这是手机上最贵的一项。
4. **transform 用 px**，别用 `calc(% + px)`（轨道 500% 宽，百分比每帧重算）。

**真机看帧数**：地址后加 `?fps=1` → 右上角实时 FPS + 最近一次滑动的最差一帧（`components/FpsMeter.vue`）。
以后用户报"卡"，先让他开这个拿数字，别猜。

**滑动页里的弹窗必须 `Teleport to="body"`（v2.1.55 踩过）**：
- 症状：手机上弹窗只露出一部分、还偏到左边。
- 根因：滑轨 `.flex.h-full` 带 `transform: translate3d(...)`，而**带 transform 的祖先会成为
  `position: fixed` 的包含块** —— fixed 不再相对视口，而是相对那条好几页宽的滑轨
  （实测 overlay 宽 780px、面板 `left:-256px`）。
- 规矩：**任何放在 SwipePager 页面里的 `fixed inset-0` 弹窗，都必须 `<Teleport to="body">`**。
  本项目其它弹窗（ApiDrawer / DownloadDrawer / QrLoginModal / LibraryManager / SearchView …）本来就是这么写的，
  别漏。`content-visibility: auto` 也有同样的包含块效果，见下面那条。

**页面常驻 + 数据新鲜度（v2.1.37，必读）**：
- `SwipePager` 的行里放的是**已访问过的页面**（`:key="id"`，按导航顺序），切页只改 `transform`。
  ⚠️ **别再把页面放在会随 `current` 变化的插槽位置** —— 那样每切一次就卸载重建一次，
  各页 `onMounted` 重新拉数据，就是用户说的「每次滑之后都会触发当前页面的刷新、卡顿」。
- 常驻之后**没有 unmount 了**，所以"离开页面"的清理必须用 **`onDeactivated`**：
  日志页的 5 秒轮询、发现页的二维码轮询都在那里停。新加轮询/定时器时记得一起处理，否则会一直在后台打后端。
- 数据新鲜度统一走 `services/viewCache.js`：`markLoaded(view)` 标记加载完成；
  `onActivated` 里 `isStale(view)` 决定要不要刷；有写操作就 `invalidate('nas','library')`
  （已经接了下载完成事件）。新页面接入照抄这三步即可，别各自发明一套。
- 邻居页只在**开始拖动**时预挂（`padNeighbors`），切页不预挂 —— 免得每切一次都白白挂一页、发一轮请求。

**主内容区左右滑动切页（2026-09-28，v2.1.36 重做）**：实现在 `components/SwipePager.vue`（三页滑轨），
判定在 `services/swipeNav.js`（纯函数 + 单测）。要点：
- **跟手**：拖动给轨道加 ±dx（rAF 直写 transform）；锁定横向后才 `preventDefault()`，所以竖向滚动不受影响
  （`touchmove` 是**现挂**的非 passive 监听，别在模板上写 `@touchmove.passive` —— 那样没法 preventDefault，
  浏览器会把触摸序列判成滚动并 `pointercancel`，手势就废了，这是 v2.1.35 版最大的坑）。
- **只给触摸设备用**（用户 2026-09-28 明确："这是手机端交互，电脑端不需要"）：只接 `touchstart/touchmove/touchend`，
  **不要加鼠标处理** —— 加了桌面端就会被误触发。要测试请用 DevTools 设备模拟或真机。
- **参数按手指调**：轴向锁定 **8px**、翻页阈值 **25%** 宽、甩动判定 **0.3px/ms**、到头阻尼 **0.4**；
  松手动画时长由 `settleDuration(剩余距离, 速度)` 算（**180~380ms**），不是固定值 —— 甩得越快收得越快。
- ⚠️ **判断"横滑区"只能看 `scrollWidth > clientWidth`**，不能看 `overflow-x` 的计算值 ——
  CSS 里只要一个轴不是 visible，另一个轴的计算值就是 auto，于是所有 `overflow-y-auto` 的竖向滚动容器
  都会被误判成横滑区，**结果整页都拖不动**（v2.1.35 的第二个坑，用户原话「我切换不了呢，就成功过一次」）。
- 起点在真横滚容器 / `input` / `select` / `range` / 可编辑元素 / 播放器 / `.swipe-no-drag` → 整个手势不接。
- 翻页动画结束那一下的 click 要吞掉，否则会误触发页面里的按钮或歌曲行。

⚠️ **别踩**：
- 响应式**不许**用 `transform: scale()`（用户明确要求）：靠 `width` 流式 + 实测降档 + 分级隐藏 + 间距/字号自适应。
  隐藏优先级：封面/歌名/播放 > 上一首·下一首 > 歌词/播放页 > 播放模式 > 音量。
- 安全区（`pb-safe`/`pt-safe`）**只挂外层容器**，和真实内边距挂同一元素会互相覆盖。
- 深浅两套皮肤同时存在：抽屉/ApiConsole 是浅色，播放器是深色 —— `ys-glass` 别用到浅色面板上（那用 `ApiDrawer` 自己的浅色写法）。
- 真机验证视口时注意：CDP 视口覆盖只在**同一个 ego 脚本会话内**有效（脚本结束就恢复），跨调用量不到同一宽度。

---

**列表行的「标签溢出压住按钮」（v2.1.49 修，别再犯）**：
- 症状：`SourceManager.vue` 音源卡片上平台标签一多，就横向溢出、盖住右边的「测速 / 删除」；
  按钮变宽（「启用」→「当前激活」）时更明显。
- 根因：那一行是 `flex` 但**没有 `flex-wrap`**，父列写了 `min-w-0` 却**没有裁剪**。
  flex 子项的 `min-width: auto` 不允许收缩到内容宽度以下 → 标签多了就整行溢出到列外；
  右侧按钮组又是 `shrink-0` + 右对齐，只会被盖、不会把标签挤回去。
- 修法（**两处缺一不可**，谁把它们删掉谁复现）：
  1. 标签行 `flex-wrap` + `min-w-0`；
  2. 左列 `overflow-hidden` —— 这是"绝不画到按钮区上"的硬保证。
- 同类排查：任何"左边是可变长内容、右边是 `shrink-0` 按钮组"的 flex 行，都要按这两条检查一遍。

---

**多音源「启用池」（v2.1.50 起，取链的核心规则）**：
- 用户 2026-09-30 定的语义：**优先级池 + 失败自动切换**。`AppConfig.ActiveSourceIDs`（有序）是唯一数据源，
  `ActiveSourceID` 只是**兼容字段 = 池首**（给只认单值的旧调用方 / 外部 AI 读）。
- ⚠️ **`ActiveSourceIDs` 不能加 `omitempty`**：`[]` = 用户把全部音源都关掉（合法状态，必须尊重），
  「字段缺失」= 从没设置过（这时才允许自动启用第一个）。加了 omitempty 两者会被抹成一样，
  用户关掉的音源会自己活过来。`config` 包有专门用例钉住这条。
- 前端取链：`lxRuntime.pickSourceForPlatform(池, 平台)` → 池里第一个支持该平台的源；
  搜索页平台按钮用 `lxRuntime.unionPlatforms(池)`（池内并集）。**别再用单个 activeSource 去判断能力**。
- `POST /api/sources/active`：`active:true` 加入池尾 / `active:false` 摘掉 / **不传** = 旧语义（池里只留它）。
- 删源时用 `removeFromPool()`：摘掉该 id；**摘空且还有别的源**时兜底启用第一个（与单值时代的自动改选一致，
  不让用户/AI 悬空）；一个源都不剩才允许空池。

---

**沉浸式播放页的桌面档（v2.1.61 起，档位口径 v2.1.62 更新，改之前先读）**：
- **移动端是红线**（用户 2026-09-30 原话「移动端不要变！！只修改桌面端」）：所有桌面改动只允许写在
  `frontend/src/components/ImmersivePlayer.vue` 的**桌面档媒体查询**里；**手机档（基础样式）不许动**。
- 两个「手机零副作用」的手法（要按 spec 调桌面尺度时用它们，别直接改基础规则）：
  1. **字号走 CSS 变量基准**：`lineStyle()` 只输出 `--line-size`，最终 px = `calc(基准 * var(--imm-lyric-scale))`；
     桌面把 `--imm-lyric-scale` 设成 1.16，手机档为 1 → 手机逐像素不变。双语行同法（`--line-trans-size`）。
     ⚠️ 字号**必须**在 `lineStyle()` 里给（曾经漏过一次，A− / A+ 直接失效）。
  2. **主色走 `--imm-halo`**：根元素 `:style="{ '--imm-halo': tintColor }"`，只在桌面被 `.ys-imm__cover-wrap::before` 引用。
- ⚠️ **`.ys-imm__shell` 在桌面档必须 `position: static`**：它默认 `relative`，是 560px 内容列；
  顶栏（`.ys-imm__top`）与「更多」浮层改成绝对定位后会以它为参照，**被死死困在 560px 列内**
  （实测：只挪到列内缩进 56px，而窗口宽 1440）。设为 `static` 后它们改以 `.ys-imm`（fixed = 窗口）为参照。
  外壳内其余绝对定位件不受影响：`__toast` 用 `left: 50%` 居中、`__nudge` 以 `__lyrics` 为参照。
- ⚠️ **高度预算已吃满，别再加高**：1440×900 上「顶栏 58 + 封面 400 + 信息 ~92 + 歌词窗 16vh(144) + 传输条 127 + 外壳留白」≈ 900px。
  封面靠 `min(65vw, 420px, calc(100vh - 500px))` 里那道 `100vh - 500px` 收缩；歌词窗是 `flex: 0 1 auto`，
  加高了它会被压到 clamp 下限（110px），表现为「歌词忽然只剩两行」。
- 断点口径（**两档**，v2.1.62 用户定；口径史见该文件头注释）：
  · **手机档** = 基础样式，列 100%、封面 `min(78vw,380px,42dvh)`、播放键 56、无音量条；
  · **桌面档** = 其余一切，列 560、封面 `min(65vw,420px,calc(100vh-500px))`、播放键 52、普通键 28、歌词 ×1.16、顶栏贴窗口两侧；
  · 桌面档条件必须是**否定式** `@media not all and (max-width: 767px) and (hover: none) and (pointer: coarse)`
    —— 语义「不是（窄 + 触摸）」，用户原话「除了手机窗口，都是桌面」（原先写 `min-width: 1024px`，
    结果电脑上拉窄窗口就退回手机版式）。**不要改回肯定式**（`(hover: hover) and (pointer: fine)` 之类）：
    无指针设备（headless / 部分 WebView / 无鼠标的 kiosk）`hover:none` + `pointer:none`，两个特性都不匹配 →
    肯定式静默失效；否定式在特性不支持时整条为假、`not` 为真，兜到桌面档。
  · 原 `768~1023` 平板档已删（规则并入桌面档）；`max-height: 700px` / `560px` 两档保留且只收封面与歌词窗高度
    （**不要给它们加 max-width 限定**，桌面纵向版同样会被矮窗口压到）。
- 验证手法：ego 在**同一个脚本会话内**切 `Emulation.setDeviceMetricsOverride`，分别量 390×889 与 1440×900 的 computed style
  （跨会话量不到，视口覆盖会复原），移动端那组必须逐项与改前一致。

### 4.18 第三方音源脚本的三道闸（v2.1.75）—— 别删

`frontend/src/engine/lx-runtime.js` 用 `new Function` 求值**用户导入的第三方 lx 音源脚本**。
这些脚本是任意代码，实测有两条真实危害（都出自同一条「洛雪音乐源」v1.0.0 v2-fix）：

1. **它注册了一个 `setInterval(…, 2000)` 的反调试回调**（obfuscator.io 的自我保护：
   `while(!![]){}` + `else debugger;` + 自递归 + 每次重跑字符串数组解码）。单次 **600~990ms**，
   于是主线程**每 2 秒被占死一次**。用户侧的表现是「在飞牛 iframe 里拖窗口，隔 3~4 秒卡一下拖不动，循环」
   —— 注意它是**周期性**的，跟「动画太多」那种持续开销不是一回事，别往 CSS 上找。
2. **它把 `console.log` / `info` / `warn` / `error` / `trace` 全换成了自己的函数**，
   于是它之后的**所有**日志静默消失（含我们自己的 `Loaded OK` / `Load error`）。
   排查时表现为「日志停在某条音源上、再也不动」，看起来像同步死循环卡死 ——
   其实是日志通道被劫持了。**「日志停了」不等于「卡住了」，先验日志通道还在不在。**

对应的三道闸（`scriptTimersAllowed()` / `createScriptTimerShim()` / `swapGlobalTimers()` / `reclaimConsole()`）：

- 脚本作用域里的 `setInterval`、`requestAnimationFrame` **默认不调度**（返回数字句柄，`clearInterval(0)` 无害）；
  `setTimeout` **照常放行** —— 一次性定时器有界，且「两段式音源」（先拉远程配置再注册 handler）
  和它无关，但**我们自己的调用超时走它**，一刀切会掐掉正常功能。
- 求值那一小段窗口里，`globalThis.setInterval` / `requestAnimationFrame` 也换成闸门
  （兜住写死 `window.setInterval(...)` 的混淆器），`finally` 还原。
  ⚠️ **`const realSetInterval = globalThis.setInterval` 必须先抓在手里**，否则自我调用爆栈。
- 每次加载完抢回被换掉的 `console` 方法并 `console.warn` 指出是哪个音源干的。
  抢回在求值**之后** —— 脚本求值期自己打的日志会丢，这是刻意的：
  若在求值前把 `console` 冻成只读，带 `'use strict'` 的脚本会直接抛错、音源加载失败，比丢日志严重。
- 逃生开关：`window.__LX_ALLOW_SCRIPT_TIMERS__ = true` 或 `localStorage['lx.allowScriptTimers'] = '1'`
  （用户明确要某条音源的定时器时才开）。
- 测试在 `frontend/src/engine/lx-runtime.test.mjs`（16 条，`node --test` 风格、零依赖）。
  ⚠️ 写这类测试有个坑：**别把还原动作放在断言之前的 `finally` 里** —— 断言会恒真，
  拿空实现做变异它照样通过。先快照、后还原。

### 4.y ⚠️ 用户隔离在这台机器上实际是**关闭**的（2026-10-07 确认，用户已知情并选择维持现状）

**事实**：拦截层的 `userKey(r)` 一旦采纳了 legacy 键，就**无条件返回它、与请求凭据无关**：

```
intercept.go:543    if i.legacyUserKey != "" {
intercept.go:544        return i.legacyUserKey
```

而 `legacyUserKey` 是在构造时按「`favorites/` 下**恰好只有一个** `fp-*.json`」决定的（`intercept.go:838-863`，`New` 里调用，`TestAdoptLegacyUserKey` 钉住）。真机上 `favorites/` 里就是只有一个文件，所以**所有凭据都映射到同一个用户键**。

**后果**：这台机器上**收藏 / 在线歌单 / 播放历史 / 各类按用户分区的缓存全部共用一个桶**。也就是说：谁都能看到谁的收藏。任何新增的「按用户隔离」的缓存或存储，在这里都分不开 —— 别以为加了 `userKey` 维度就真的隔离了。

**为什么会这样（这是补救动作的副作用，不是设计）**：`adoptLegacyUserKey` 是 `intercept.go` 被误删约 350 行、重建之后加的，目的是**让用户既有的收藏不消失**（重建后的 `credentialFingerprint` 算出来的值与磁盘上的对不上，不采纳就显示 0 条收藏）。当时只验证了「收藏还在」，**没有评估它对多用户隔离的影响**。

**根因**：`credentialFingerprint` 的哈希输入始终是**猜的**（试过 6 种输入形态 × sha256/md5/sha1 共 16 个候选，无一命中磁盘上的 `fp-485d391520ecf02c`）。收敛它需要真机（当前 NAS 浏览器未修、装不了机）。

**用户 2026-10-07 的决定：维持现状（单人使用，不值得为它花一轮装机）。**

**依赖用户隔离之前先确认**：`favorites/` 下是不是只有一个 `fp-*.json`。如果哪天要多用户/家庭共享，这是**第一个**要处理的地方（要么收敛指纹算法，要么收窄采纳规则 —— 例如只对第一个出现的指纹采纳 legacy 键）。

### 4.x 工作方法上的坑（改代码 / 写验收时踩出来的）

> 这一节不是业务规则，是**做事方式**上的坑。都来自 2026-10-06 那几十轮（`docs/变更记录/2026-10-06.md` turn 62–106），每一条都真的踩过、并且**代价不小**。写在这里是因为它们散在各轮记录里，下次换人（或换我）还得重新踩一遍。

**改文件**

1. **改之前先 `cp` 一份。** 这个项目没有 git，机器上没有备份。有一次给 `backend/pkg/intercept/intercept.go` 打补丁，脚本写成 `s[:start] + insert + m.group(0)[1:]` —— **漏了 `+ s[m.end():]`**，匹配点之后约 350 行全没了。恢复花了整整一轮，靠的是「从出厂二进制里 `strings` 抠路由路径 + 用那份 47 KB 的 `intercept_test.go` 当验收网」。
2. **补丁要能报告「我没打上」。** 替换前对锚点断言 `count == 1`，对不上就打印 `SKIP` —— **不要静默跳过**。静默跳过会让人以为改了、其实没改，后面所有判断都建立在错的前提上。
3. **测试夹具要按生产约定摆，不能按实现的想象摆。** 有个 bug 是「对账目录多拼了一层 `online`」，而用例**也按同样错的方式布置了临时目录** → 测试和实现一起错、一起过。真机上表现是「用户的收藏显示 0 条」。**夹具要复刻生产的形状**（比如 `Config{DataDir: filepath.Join(dir, "online")}`），不是复刻你的假设。
4. **别把「平台没给这个字段」当成「值是 0 / 空」。** 体积、时长、音质档位都可能缺失。把 0 当「很小」会让「不报这个字段的源」永远选不上；把空当「没有」会让「本来就在库里的东西」被当成不存在。缺失就是缺失，单独处理。

**写验收**

5. **每一段验「开关前后」必须换一个没搜过的输入。** `pkg/search` 有结果缓存 —— 用同一个关键词测「关掉之后还出不出结果」，测到的是**缓存**，不是开关。第一次就是这么被误导的。
6. **断言要针对被改动的那个集合，不要拿一个更大的集合判空。** 「关掉全部外挂源之后 `pool.Platforms()` 该为空」是错的 —— 解析池里本来就有原生 `wy`/`tx`。这类断言会把「本来就有的东西」当成回归。
7. **分段绿 ≠ 整条绿。** 一条链路每段都有单测，不代表串起来还通。要有一条端到端用例走完整条路（这个项目里是「收藏 → 后台下载 → 登记 → 取流走本地」）。
8. **取流这类「应该走本地」的断言，要比内容，不能只比状态码。** 只断言 `206` 会假绿 —— 代理在线流也回 `206`。要比「回的字节就是磁盘上那份」。
9. **别拿被自己拦截过的端点当对照组的取样来源。** 想取一条「官方曲目」来对照，却用了 `/music/api/v1/track/list` —— 那个端点**正是我们自己接管并合并在线结果的**，取出来的还是虚拟 id，于是「官方也失败」根本不能证明请求形状有问题。
10. **两个看起来一样的东西，先确认它们真的是同一个。** `i.cfg.LocalAPI` 与 `i.localAPI`（构造时解析、去过尾斜杠）不是一回事 —— 判错那一个，表现是「静默地什么都没做」，最难查。

**在 NAS 上跑东西**

11. **别用 `pkill -f <名字>`。** shell 自己的命令行也含那个串，会把发起命令的 shell 一起杀掉 —— 表现是 `EXIT null`，看着像「命令失败了」，其实目标进程**没死**。要按 `/api/musicdl/status` 报出来的 pid 精确 `kill`。
12. **`page.evaluate(fn, a, b)` 只传第一个参数。** 多传的会静默变 `undefined`，表现是「服务端收到了请求但字段是空的」—— 看起来极像服务端 bug。要传多个值就自己拼成一个对象/字符串。

**可重跑的验收**：`scripts/nas-acceptance.mjs`（跑法与上面这些纪律的落地写法见 §7.x）。

## 5. 业务规则（改之前请先理解）

### 5.1 版本过滤与匹配打分（`pkg/match`）

**版本标记只作用于歌名与专辑，不看歌手** —— 否则艺名含 "DJ" 的正常歌曲会被误伤。

翻唱/改编词表包含：`live 现场 演唱会 跨年 音乐会 演出 acoustic unplugged demo 试听 片段 snippet
remix 混音 dj 加长版 伴奏 instrumental karaoke 纯音乐 广播剧 radio edit ost 变速 sped up slowed
cover 翻唱 重制 remaster 重混 mashup 8d 环绕 铃声 ringtone`

**正式版永远优先，改编版仅在正式版完全不可用时兜底。**

时长校验：试听片段 ≤75 秒；与原曲容差 `max(10秒, 参考时长×15%)`。**时长未知不构成拒绝理由**。

匹配打分：歌名 0.7 + 歌手 0.3；歌手只要有一方包含另一方（"周杰伦" vs "周杰伦/杨瑞代"）就给 0.9。
阈值：搜索候选 0.72，换源候选 0.85。

> 参考来源：`music-monitor` 项目的做法。注意它有两个缺陷我们没有复制：
> ①它没有首轮基线；②它的 `allow_variant` 硬编码 True 导致改编版绕过时长校验。

### 5.2 去重的保留优先级（`pkg/tidy`）

```
无损格式 > 已整理(有歌词/封面) > 体积更大 > 文件名更短（确定性兜底）
```

- **默认 `mode=trash` 移入 `.tidy_trash` 回收站，不直接删除**，误判可恢复
- 同分时用文件名长度做兜底：没有它，保留哪个取决于 map 遍历顺序，**结果不可复现**
- 做 SHA-1 前先按 size 分桶 —— 体积唯一的文件不可能内容重复，大曲库靠这个把哈希量降到极低
- 双通道：`content`（SHA-1 相同）+ `name`（归一化"歌手|歌名"相同但 SHA-1 不同）

### 5.3 刮削规则（`pkg/tidy`）

- **绝不写目录级 `cover.jpg`** —— 会让整个文件夹显示同一张图（有专门测试锁定）
- 只写逐曲同名封面图（`.jpg`/`.png`）
- 默认**不覆盖**已有歌词/封面，需显式开启
- 仅支持 MP3 / FLAC（`pkg/tags` 的能力范围），其他格式**明确报错**而不是静默失败

### 5.4 扫描/整理的持久化与判定口径（本次修复）

**NAS 目录扫描结果已落文件缓存**（`pkg/nas/scancache.go` → `scan_cache.json`）：
- 非 `refresh=1` 时先读缓存，命中即返回，不再每次打开页面全盘重扫
- 下载成功后 `InvalidateScanCacheFile` 使缓存失效，保证能看到新文件

**曲库管家首屏自动加载上次整理快照**：
- `library` 索引新增 `StatsForDir/ListDir/MarkEnriched/Unenriched`
- 新增 `GET /api/library/index/stats` 读持久化 `library_index.json` 概况
- 前端 `LibraryManager` 挂载即 `loadIndexStats()`，无需等用户点扫描

**歌词/封面缺失的判定口径已统一**（三处一致，不再"NAS 页有歌词、曲库管家却报缺"）：
- `tidy.NeedsTidy` 认同名外挂 `.lrc`/`.txt` 与逐曲同名图（新增 `hasSidecarLyric`/`hasSidecarCover`）
- 与 NAS 扫描的 `checkHasLyric` 同一套候选规则
- **仍不认目录级 cover.jpg**（避免整文件夹同图，见 §5.3 锁定）

### 5.5 音质阶梯（`frontend/src/engine/quality.js`）

`flac24bit > flac > 320k > 192k > 128k`

**档位优先、音源次之**：同一档位轮询全部音源，全部失败才降一档。与 `miyin` 项目一致。

⚠️ **音质字符串必须小写化** —— 搜索接口返回的是 `"320K"`/`"FLAC"` 大写，
而音源脚本约定小写，不转换会导致脚本无法识别。

### 5.6 音源熔断（`frontend/src/services/sourceHealth.js`）

连续 3 次失败 → 熔断 60 秒；任意一次成功清零；状态持久化在 localStorage。
取链时按「成功率 + 延迟」排序，熔断中的置底。

### 5.7 真实音质解析（`pkg/search/quality.go`）

**旧实现是硬编码的**：四个平台无论什么歌都返回 `["128k","320k","flac","flac24bit"]`，是虚假信息。
现已改为**解析各平台返回的真实码率字段**动态构造。

**档位常量必须与前端 `QUALITY_LADDER` 严格对齐**：
`flac24bit > flac > 320k > 192k > 128k`

| 平台 | 字段 | 判据 |
|---|---|---|
| 网易 | `l`/`m`/`h`/`sq`/`hr`（对象，含 `br`） | 对象非 null 且 `br>0` → 128k/192k/320k/flac/flac24bit |
| QQ | `size128`/`size320`/`sizeflac`/`sizehires`/`sizeape` | >0 → 对应档位（实测 `sizehires` 不返回，仍保留解析） |
| 酷狗 | `FileHash`/`HQFileHash`/`SQFileHash`/`ResFileHash`/`SuperFileHash` | 非空 → 128k/320k/flac/flac24bit |
| 酷我 | `N_MINFO`/`MINFO`/`FORMATS` | 解析 `level,bitrate,format`；`format:flac`→flac；`zp`→flac24bit；否则按码率映射 |

**四个实测得来的关键事实**（别再猜）：
1. 网易 `privilege.maxbr` **恒为 999000**，无法区分无损与 Hi-Res → 必须用 `sq`/`hr` 对象是否存在
2. 酷我 `zp` 档 `bitrate=20000` 是**哨兵值**而非真实码率 → 必须先按 format/level 判定再回退码率映射
3. 酷狗 `SQBitrate=937`、`ResBitrate=1642` → 无损阈值定为 **500kbps**（有损上限 320k）
4. 酷我 `zp` 映射为 `flac24bit` 而**不是** `hires` —— `hires` 不在 `QUALITY_LADDER` 内，会被前端排到最后兜底（排序错误）

**解析失败时**（API 改版等）回退为 `["320k"]`，不虚报无损。

---

### 5.8 音质查询接口的实现方式（`pkg/search/qualities.go`，2026-09-17 新增）

**`GET /api/music/qualities` 为什么不调平台详情接口**

上游是直接调各平台详情接口拿音质的，但**实测那四个接口普遍不可用**：

| 平台 | 详情接口实测结果 |
|---|---|
| 网易 | 不返回音质字段 |
| QQ | 缺无损档 |
| 酷狗 | 返回空壳 |
| 酷我 | 需要额外 token |

**我们的做法：搜索 + 按 ID 匹配。** 搜索结果里的 `Qualitys` 本来就是真实值
（由 `quality.go` 从各平台码率/文件尺寸字段推导），所以：

```
GET /api/music/qualities?source=wy&id=<songmid>&name=<曲名>&singer=<歌手>
  ↓
用 "曲名 + 歌手" 发起一次平台搜索（**不调用 filterAndRankSongs**，召回优先于排序）
  ↓
① 按 ID 精确匹配  → matched_by = "id"
② 退一步按 曲名+歌手 匹配 → matched_by = "name_singer"
③ 都没命中 → matched_by = "none"，qualitys = []（**绝不臆造档位**）
```

**匹配字段（各平台填的字段不同，见 `aggregator.go`）**：

| 平台 | 匹配依据 |
|---|---|
| 网易 | `Songmid` = 数字 ID |
| QQ | `ID` = `"tx_"+songmid`，`Songmid` = songmid |
| 酷狗 | `ID` = `"kg_"+FileHash`，`Songmid` = `Hash` = FileHash |
| 酷我 | `Songmid` = rid |

`matchByID` 依次比对 `Songmid`、完整 `ID`、以及 `"前缀_ID"` 的后半段。

**曲名归一化**（`normalizeForMatch`）：转小写 → 剥掉成对括号及其内容
（`(Live)` / `【无损】` 都算噪声）→ 去掉空白与 `- _ · . , 、 & /`。
未闭合的括号**不吞后续内容**（有单测守着）。

**响应结构**：`{qualitys, best, matched_by, candidates_checked, source, id}`，
其中 `qualitys` 按档位从高到低排列，`best` 为最高可用档位。
同时给出扁平字段（`qualitys` / `best` / `matched_by` 也在顶层），方便外部程序少解一层。

> ⚠️ 参数校验：`source` 必须是 `wy/tx/kg/kw`（`mg` 不在官方协议范围内）；
> `id`（或 `songmid`/`hash`）必填。缺 `name` 时无法搜索 → 直接返回空档位（HTTP 200，不报错）。

**前端**：`DownloadQualityModal.vue` 在单曲下载前弹出，档位来自本接口。
「最高音质（自动）」永远可选（兜底），固定档位仅在真实存在时展示。
勾选「记住为默认音质」会写入 `downloadManager.setAllQuality()`。

### 5.9 账号曲目拉取（`GET /api/accounts/tracks`，2026-09-17 新增）

给「双平台推荐」用的归一化接口。**三个接口的分工别搞混**：

| 接口 | 返回什么 | 用途 |
|---|---|---|
| `GET /api/accounts/{provider}/daily` | 平台**原始**对象（网易 `ar[]`/`al{}`、QQ 各自结构） | 调试 / 外部程序自行解析 |
| `GET /api/push/sources` | **轻量列表**（只有 kind/id/name/cover/count，**不含曲目**） | 给推送任务选来源、给推荐卡列歌单 |
| `GET /api/accounts/tracks` | **归一化后的可播放曲目**（带 `source`/`songmid`） | 前端直接入列表播放 / 下载 |

**归一化复用 `pkg/push` 的 `FetchSource`** —— 与「推送」用的是同一套拉取与字段映射，
所以不会出现「推送看到的曲目」和「推荐页看到的曲目」两处口径不一致。

```
GET /api/accounts/tracks?provider=netease&kind=daily
GET /api/accounts/tracks?provider=netease&kind=playlist&id=<歌单ID>&limit=200

→ data: { provider, kind, id, title, cover, owner, platform, songs[], count }
   songs[]: { id, songmid, name, singer, album, cover, duration, interval, source }
```

- `platform` / `source` 取 `wy` / `tx`，与 `pkg/search` 的平台代号对齐，
  前端据此选音源脚本的对应接口
- `id` 加平台前缀（`wy_186016`），避免不同平台同 ID 在列表里 key 冲突
- **不查音质档位**：那需要对每首歌再搜一次，代价过高。下载时由
  `/api/music/qualities` 按需查询（见 §5.8）
- 未登录 / 登录态过期 / 来源为空 → HTTP 502 + 原样带出中文原因

**前端消费方**：
- `SearchView.vue` 的「双平台推荐」区块 —— 点「每日推荐」或某个歌单即入列表
- `LibraryManager.vue` 的「补下载」—— 用 `SearchAPI.search` 反查后入下载队列（`fillRow()`，
  监控侧 pending 与推送侧 missing 共用 `enqueueSongs()`）

---

## 6. 接口契约（对外最重要）

**`backend/pkg/api/catalog.go` 是接口文档的单一数据源**，`GET /api/catalog` 返回全部 **122 个**
接口的结构化描述，前端「开放接口」抽屉渲染同一份数据。

> **目录的主要读者是「替用户操作本应用」的外部 AI**，不是翻手册的人。
> 所以它按**任务**组织（先说这是什么、能替你做什么、常见任务怎么串），接口清单只是参考。
> 用户 2026-09-23 的原话：「我让 ai 看这个目的是为了帮我下载歌曲，帮我操作这个应用…
> 所以先告诉 ai 是干嘛的，怎么做」。

**顶层结构**（⚠️ **扁平**：`endpoints` / `guide` 都在顶层，不在 `data` 里，`data` 为 `null`）：

```
{ code, message, app, version, base_url, auth_mode,
  guide: { what[], prereq[], workflows[], rules[] },
  usage[],            // 由 guide 生成，别手写第二份
  endpoints[] }
```

`guide.workflows[]` 每条 = `{task, summary, steps[], endpoints[], watch_out}`，
其中 `endpoints` 写的是 `"METHOD /path"`，**必须能在目录里找到**（有测试钉着）。
当前 9 条食谱：下载一首 / 批量与歌单 / 订阅自动下 / 停掉任务 / 同步飞牛歌单 /
补全曲库信息 / 只换封面 / 查重清理 / 连平台账号。

分类与数量（共 122，2026-10-05 实测）：整理与去重 17 · 音源管理 16 · 系统 12 · 推送·下载到曲库 12 ·
账号连接 12 · 下载 10 · 本地曲库 8 · 飞牛集成 7 · 推送·飞牛歌单 7 ·
AI 大模型 6 · 榜单与歌单 5 · AI 解析桥 4 · 搜索与发现 3 · 音乐入口接管 2 · 网络代理 1

**响应约定**：统一 `{"code": 200, "message": "ok", "data": {...}}`；
错误用 HTTP 状态码 + `{"code": N, "message": "中文说明"}`。

### 6.0 改接口的三步（缺一不可）

1. **挂路由 → 加进 `backend/pkg/api/routes.go` 的 `routeTable`**（全项目唯一注册处，104 条），
   再登记到 `catalog.go`（漏了 AI 就发现不了）；
2. 决定 `role`：AI 会用来干活的进 core，纯界面自用/外部不该碰的加进 `internalEndpoints` 名单；
3. 如果它属于某个任务的必经一步，**写进对应的 `workflows.steps`**。

**路由 ↔ 目录双向钉死**：漏登记、或者目录里写了不存在的路由，`catalog_routes_test.go` 都会红。
只有两类例外能跳过它，且**都要写理由**：`routeCatalogAliases`（前缀挂载/别名 → 它对应的目录条目集，
如 `/api/monitors/` → 三条 `{id}` 模板）、`undocumentedRoutes`（历史别名、网页端音源脚本内部协议、恒 403 的历史桩）。
测试还会校验这两张表本身不是后门：键必须是真路由、别名目标必须真在目录里、理由不能空、不能两边都占。

自检（**源码级与线上级是两件事，都要跑**）：

```bash
cd backend && go test ./pkg/api/ -run 'Catalog|Route' -count=1  # 源码：路由↔目录双向 + 目录自洽(role/workflow/篇幅)
node .dev/catalog-audit.mjs http://127.0.0.1:8900               # 线上：跑着的那个进程 == 源码这一版吗（默认就是这个 URL）
```

> `.dev/catalog-audit.mjs` **只做线上核对**（它独有价值：源码对得上不代表线上就是这一版，旧进程没重启是排查时最常见的假象）。
> 它以前也做源码级检查，但用「前缀包含」猜匹配（`/api/sources` 被当成覆盖了整个 `/api/sources/*` 家族），
> 真漏登记的接口被算成「已登记」—— **假清白比没有检查更糟**，所以源码级只留 Go 测试一处。

### 6.1 `role` 分层：core / internal（取代旧的按分类折叠）

`internalEndpoints`（键 = `"METHOD /path"`）决定谁被折起来；**当前 87 条 core / 33 条 internal**（2026-09-26 实测）。
`Endpoint.Hidden` 由 `Role` 推导（internal ⇒ hidden），所以「抽屉看到的」和
「AI 按 role 分流的」永远是同一份判断 —— `catalog_test.go` 断言两者一致。

**判定口径只有一条**：AI 调它是「帮用户干活」还是「根本不该调」。进 internal 的是这几类，
不是「我不常用」：

- 界面自己的状态：`ui-prefs`、`logs`（读/写/清）、`POST /api/config`（整体写配置）
- 会被批量误伤的管理动作：音源脚本批量导入 / 导出 / 清理 / 批删
- 前端闭环的写入口：`PUT /api/download/queue`（队列副本由网页端整体写回）、
  `GET /api/bridge/pending`、`POST /api/monitor/tracks/mark`
- 界面表格专用：`library/index/list`、`library/index/stats`（AI 要缺口看 `library/gaps`）
- 本应用自身的大模型设置与 AI 生成按钮：`ai/*` 共 5 条
- 排障与内部机制：`fnos/check`、`fnos/token`、脚本跨域用的 `proxy/http`、播放用的 `player/stream`

> ⚠️ 旧的 `hiddenCategories`（按分类整块折叠：AI 解析桥 / AI 大模型 / 网络代理 / 飞牛集成）**已删除**。
> 它粒度太粗必然折错：「整理与去重」里补全和命名规则混在一起，「飞牛集成」里 `rescan`
> 是补全之后要做的、而 `token` 手工写入绝不该被外部调。
>
> **接口一条都没撤**：`/api/catalog` 仍返回全部 113 条（带 `role` / `hidden`）。
> 抽屉右上角开关文案是「显示内部接口（+N）」，展开后 internal 条目带「界面内部」灰标。

> 改 `internalEndpoints` 后用 `/api/catalog` 复核 core / internal 两个数字
> （2026-09-23 实测 `total=113 / core=84 / internal=29`）。
> ⚠️ 别把 `/api/catalog` 返回的 `version` 抄进文档当「当前版本」—— 它随每个补丁版本变，必腐化。

**安全环境变量**：

| 变量 | 作用 |
|---|---|
| `FN_API_TOKEN` | API 凭据。设置后，除 `/api/health`、`/api/catalog`、`/api/app/version` 外都需带 `X-API-Token` |
| `FN_AUTH_MODE` | 鉴权模式：`lan`（本机/内网/容器地址免 Token，其它来源必须带 Token）/ `token`（一律校验）/ `none`（完全不校验，等同 v2.1.30 之前的旧行为）。**不设置 = auto**：设了 `FN_API_TOKEN` 按 `token`，否则按 `lan` |
| `FN_TRUSTED_CIDRS` | 追加信任来源（逗号分隔的 IP/CIDR），`lan` 模式下免 Token。只认 TCP 对端地址，`X-Forwarded-For` / `X-Real-IP` 一律不采信 |
| `FN_CORS_ORIGINS` | 允许跨域的来源（逗号分隔）。**不设置 = 仅同源**（不再无脑下发 `*`） |
| `FN_ALLOWED_ROOTS` | 允许访问的曲库根目录（冒号分隔），**设置后完全覆盖**内置值 |
| `FN_ALLOW_PRIVATE_NET` | 设为 `1` 才允许出站请求访问内网/回环（默认禁止，SSRF 防护）。⚠️ **`pkg/apisource` 不受它约束**（v2.1.12 起自己放开，理由见 §8.2）；它管的是代理、下载器等其余路径 |
| `FN_PROXY_PRIVATE_NET` | 设为 `1` 时，内网目标**也走代理**（默认内网直连，避免代理自身在内网被自己拦下的死锁）。仅在「NAS 用局域网 IP 访问自己、必须经代理才通」时需要 |
| `FNOS_TOKEN` | 手工指定飞牛音乐令牌，覆盖自动读取 |
| `TRIM_API_TOKEN` | **飞牛系统自动注入**，应用不需要也不应设置它 |

**鉴权模式（2.1.30 树内新增，尚未打包）**：默认 `lan` —— 本机 / RFC1918 / `169.254/16` / `100.64/10`（含 Tailscale）/ `fc00::/7` / `fe80::/10` 免校验；**其它来源（真实公网地址）直接 401**，并在响应里写清「你是谁、怎么放行」。启动日志会打一行 `[AUTH] <模式>（<说明>）`；`/api/health` 回 `auth_mode` / `auth_note` / `trusted_cidrs`，`/api/catalog` 的 `auth_mode` 与它同源（`middleware_test.go` 钉着两处口径）。内网来源**首次**访问记一条 info（谁在直连这台机器，以前完全静默），拒绝记 warn（同一来源 60s 内只记一次，防止被扫描时刷爆内存日志）。

> 为什么不是「非 localhost 一律要 Token」：那会同时打断浏览器、局域网里的 AI 程序、Docker 桥接的调用方 —— 而这些正是本应用的主要使用者。收紧的落点是「公网来源」，它本来就不该有权限。

---

### 6.2 同源通道 `/music/_qulv/*`（注入脚本用，v2.1.74 起；v2.1.79 修正前缀）

注入到**飞牛音乐官方页面**里的脚本不能直连曲率后端 —— 官方页面 origin 是 NAS 本身、后端在 `:8898`，
直连是跨域；放开 CORS 等于把「能下载、能整理、能读整机文件」的一整套暴露给任意站点。
所以拦截层（`pkg/intercept/qulvbridge.go`）开了一条**同源**通道，注入脚本用相对路径即可，且无预检请求。

| 路径 | 作用 |
|---|---|
| `POST /music/_qulv/api/download/online` | 入参只有 `{guid}`（32 位 hex 的**虚拟**在线曲目 id）。直链由**后端**解析，落盘复用既有的 `/api/download/song`（标签、封面、去重都在那儿） |
| `GET /music/_qulv/ui.js` | 注入脚本本体（v2.1.79 起，`go:embed` 进二进制、`Cache-Control: no-cache`） |
| `/music/_qulv/` 下**其它一切** | 一律 **404，且不转发官方** |

三条硬约束：

- **绝不猜 guid**：只认本应用登记表里的 id（`lookupTrackAny`）；不在表里 → **404**。
  猜错会把不相干的歌写进用户曲库。
- **不可播放要区分原因**：502，且 `ErrNotPlayable`（收费 / 无版权 / 地区限制）与
  `ErrNoResolver`（该平台没有可用的直链解析器）分开报，不是笼统失败。
- **兜底 404 不转发官方**：官方对未知路径走 SPA fallback 返回 `200 + HTML`，比 404 难查得多。

⚠️ 前缀**不在** `/music/api/v1` 之下，所以不影响既有「拦不认得就打上游」的透传语义
（路由按前缀长度倒序匹配）。回环调用 `/api/download/song` **不复制客户端凭据**（本机免鉴权）。

⚠️ **前缀必须在 `/music/` 下**（v2.1.79 修正）：fnOS 的 nginx 只有 `location /music` 指向
入口 socket，裸的 `/_qulv/` 那棵树归一管自己的 livez/healthz —— v2.1.74 交付时用的是裸前缀，
`curl` 直打 socket 能通，**从浏览器里其实永远到不了**。`pageshell_test.go` 里有设计守卫。

## 7. 发布前必须做的

1. **同步版本号（两处，手工）**
   - `backend/pkg/api/server.go` 的 `CurrentVersion`
   - `fpk-package/manifest` 的 `version`
   - 改完顺手核对 `HANDOVER.md` §0 与 `RELEASE_NOTES.md` 顶部的版本号，**四处要一致**

   > ⚠️ **`frontend/package.json` 的 `version` 不在这份清单里** —— 它是前端包自身版本
   > （一直是 `1.0.0`），**与发布版本无关，不要去改**。

   **版本号怎么定**（语义化版本）：

   | 情况 | 走哪一位 | 例 |
   |---|---|---|
   | 新增功能、接口只增不改（向后兼容） | **minor** | `2.0.2` → **`2.1.0`** |
   | 只修 bug，接口不变 | **patch** | `2.0.1` → `2.0.2` |
   | 破坏性变更（改 appname / 端口 / 接口不兼容） | **major** | `1.4.0` → `2.0.0` |

   > 别习惯性地只递增末位。「加了一堆功能却只升 patch」会让用户以为是小修，
   > 也让飞牛侧难以判断该不该升级。

2. 跑完整验证：`gofmt -l pkg/`（须为空）/ `go vet` / `go build` / `go test`（**504 用例**）/ 前端 `npm test`（**81 项**）+ `npm run build`
3. `bash scripts/build.sh --version X.Y.Z`
   （`--version` 会覆盖写进 manifest；也可直接改 `fpk-package/manifest`，两者等效）
4. **解包自检**：确认内嵌二进制是 ELF x86-64 且 > 1MB

   ```bash
   rm -rf /tmp/c && mkdir -p /tmp/c/app
   tar -xzf yinshu-ai.fpk -C /tmp/c app.tgz && tar -xzf /tmp/c/app.tgz -C /tmp/c/app
   stat -c "%y  %s bytes  %n" /tmp/c/app/*          # 二进制，时间应为刚刚
   file /tmp/c/app/yinshu-ai                        # ELF 64-bit LSB executable, x86-64
   ```

5. **确认版本号真的换掉了**（别只看 manifest）
   ```bash
   VER=$(grep -m1 '^version=' fpk-package/manifest | cut -d= -f2)   # 从 manifest 取，别手写
   # ① manifest 本身（注意：是 `-O`，不要写成 `-xzOf`，那条在本环境取空）
   tar -xzf yinshu-ai.fpk -O manifest | grep -E '^(appname|version|service_port)='
   # ② 二进制里的版本常量
   strings -n 3 /tmp/c/app/yinshu-ai | grep -F  "$VER"    # 应命中
   # ③ 上一个版本号是否有残留（把 2.1.10 换成你上一版）
   strings -n 3 /tmp/c/app/yinshu-ai | grep -cF 2.1.10    # 期望 0
   ```
   > ⚠️ **③ 命中不为 0 时先别慌**：大概率是源码里的**历史引用**（例如 `pkg/api/catalog.go`
   > 的接口说明文案「（v2.1.10 起…）」被编进了字符串表），**属正常，别去改**。
   > 判据：只要不是版本常量本身（`CurrentVersion` / manifest 的 `version=`）就没问题。
   > 想确认就 `grep -rn "2\.1\.10" --include="*.go" backend | head` 看它出现在哪儿。
   > 注：`strings | grep '^2\.1\.0$'` **捞不到** —— Go 的 `const string` 不单独进字符串表，
   > 短字符串会和相邻数据拼在一起。要用 `grep -F`（不加行首行尾锚点）。
   > ⚠️ **核验中文文案必须用 `grep -o -a -F`**：`strings` 默认**跳过非 ASCII**，
   > 拿它去数中文会全为 0，看起来像「文案没打进去」，其实是工具的问题。

> ⚠️ **改后端后要重建 dev 二进制，别只跑 `wsl-run.sh build`。**
> `cmd_build` 是 `go build ./...` —— 只做编译检查，**不产出任何文件**；
> dev 服务器跑的 `.dev/fn-dev-backend` 是 `cmd_dev` 用 `go build -o` 生成的。
> 只跑 `build` 会一直跑旧二进制，白排查半天。
> **判断依据**：`ls -la --time-style=full-iso .dev/fn-dev-backend` 的时间戳要新于源码。
> 正确做法：跑 `bash .dev/wsl-run.sh dev`，或显式 `go build -o .dev/fn-dev-backend ./backend`。

> ⚠️ **历史教训**：`CurrentVersion` 曾在代码里停在 `1.2.7` 很久，而 `RELEASE_NOTES.md`
> 已经写到 `v1.3.0`/`v1.4.0` —— 文档与代码长期不一致，靠人记是记不住的。
> 发布前把这一节当 checklist 走一遍。

---

### 7.x NAS 真机验收（可重跑）

`scripts/nas-acceptance.mjs` 一次跑完 goal 的验收标准：① 音源页逐个启停生效 ② 收藏一首网易歌 → 自动下载并绑定本地 ③ 设置开关真的控制行为 ④ 收尾还原配置 + 收藏总数。

跑法（dev box 把脚本挂到 `:8893`，在 NAS 上取正文求值）：

```js
const src = await (取 http://192.168.1.230:8893/nas-acceptance.mjs 的正文);
const fn = new Function('page','cp','http', 'return (async () => {' + src + '})()');
console.log(JSON.stringify(await fn(page, cp, http), null, 1));
```

⚠️ **这个脚本里写着几条踩出来的纪律，改它之前先读注释**：

- **每一段验「开关前后」必须换一个没搜过的关键词** —— `pkg/search` 有结果缓存，用同一个词测到的是缓存不是开关。
- **`page.evaluate(fn, a, b)` 只传第一个参数** —— 多传的会静默变 `undefined`，表现是「服务端收到了请求但字段是空的」，看起来极像服务端 bug。
- **别用 `pkill -f musicdl_service`** —— shell 自己的命令行也含这串，会把自己杀掉（表现 `EXIT null`，看着像命令失败，其实目标进程没死）。要按 `/api/musicdl/status` 报的 pid 精确 kill。
- **取流要断言「内容就是磁盘那份」**，不能只断言 206 —— 代理在线流也回 206。

## 8. 已知未完成 / 可改进

### 8.0 音乐入口接管：四阶段路线（用户 2026-10-05 拍板，**每阶段单独出版本**）

参考 `fnmusic-ext` v2.6.2 的能力，按「先能接管、再能改行为、再补功能、最后补安装体验」分四阶段。
**阶段边界是刻意的**：接管层是唯一能让官方音乐打不开的组件，所以它必须单独一个版本上线、单独被验证。

| 阶段 | 内容 | 状态 |
|---|---|---|
| **① 接管 + 全透传 + 回滚** | `pkg/takeover`：socket 接管、透传、journal、崩溃自愈、还原、状态/还原接口 | ✅ **v2.1.67 完成**（本版） |
| **② 拦截 + 合并** | 拦官方搜索 / 取流 / 歌词 / 封面 / 收藏 / 歌单 / 播放历史，把本地曲库与在线结果合并；虚拟 id 伪装（官方客户端只认自己的 id 形状，要造确定性映射并在重启后自愈重建） | ✅ **v2.1.70 完成**（v2.1.71 / v2.1.72 修真机 bug，见 §3.7） |
| **③ 补足链** | 边听边存（tee save，客户端边播边落盘 + 切歌后 `Range` 续传）、收藏/加歌单自动绑定本地、落库即补歌词与内嵌封面、落库后通知官方重扫 | ⬜ 未开始 |
| **④ 设置与安装** | 设置热生效（改配置不重启）、fpk 安装向导（`wizard/`）、`install_dep_apps=trim.music`、`upgrade_init` 升级前备份、`manifest` 的 changelog 字段 | ⬜ 未开始 |

**移植时的取舍（已定，别推翻）**：

- **三音源互斥不抄**。它是 `.env` 里三选一，导致「网易 + 酷我」这种自然组合用不了；
  本项目已有的多音源并存模型更好，不要在接管层引入互斥开关。
- **LLM 推荐层不抄**（现阶段）。它的「按账号隔离」前提是家庭多用户；本项目是单用户模型，
  前提不成立。等接管真的接进了官方界面、有了多账号场景再评估。
- **lx 取链改服务内 Node 子进程**：用户已拍板（破规则一），但要等阶段 ②/③ 落地时再做，
  届时必须同步改 §2 规则一与 `pkg/protocol` 顶部注释，并在 README 免责声明里说清。
- **不抄它的 WebUI 认证**（`x-trim-isadmin: true` 单头 + 只拦 `/api/` 前缀）和
  **`/api/host-file` 式任意路径代读**（root 进程代读任意绝对路径 `.js`）—— 那是安全洞，不是特性。
- **`disguise_client_json` 式的全局递归字符串重写不抄**：官方加一个带 id 的字段就漏。
  阶段 ② 要做的是**结构化映射**（按字段名认，不按 id 正则猜）。

#### 8.0.1 第二批：虚拟歌单 / 页面注入 / musicdl（用户 2026-10-05 拍板）

阶段 ③④ 之外，用户又点了三件事，同时拍了两个方向性决定：

| 事项 | 拍板结果 | 看板卡 |
|---|---|---|
| **飞牛顶部歌单显示推荐** | 做。拦截层造**虚拟歌单**，插在官方歌单列表最前，对外**只读** | `af0ee245` |
| **在飞牛页面里放我们的入口按钮** | ✅ **批准注入**。做**真正双向**的切换（曲率 ↔ 飞牛），不是单向 | `1ceb1ccf` |
| **两个页面都能下载 / 收藏** | 收藏**不用做**（本来就是同一份官方数据）；只补**下载**，走同源代理 `/music/_qulv/api/*`。同源通道 ✅ v2.1.74，页面注入 + 「下载到曲率」菜单项 ✅ **v2.1.79**、界面里的 `页面注入` 一键退路开关 ✅ **v2.1.80**（见 §3.7「页面注入」） | `b6fa812b` |
| **接入 musicdl** | 做。走 **Python sidecar**（FastAPI 包一层），不在 Go 里硬啃各家风控 | `6b471228` |
| **多平台榜单管理**（`zouclang/fnos_music_ext` 的榜单管理） | 做。**不抄它的常量表**（直接用 `pkg/charts` 的实时榜单，候选 88 个）；只放 **wy/tx**（有直链解析器的平台）；复用虚拟歌单机制插入官方歌单列表，对外只读。✅ **v2.1.76 完成**，见 §3.7 | `d6561b06` |
| **定位：是否融合成「飞牛增强层」** | ❌ **不融合**。用户选**保持两个对等的独立入口** —— 曲率前端**必须保留完整播放能力**，两个入口对等，不为给飞牛让位而砍功能 | — |

**注入的边界（已批准；v2.1.79 按此落地，边界仍要守）**：只往官方 SPA 的 HTML 里插一行
`<script defer src="/music/_qulv/ui.js?v=…">`（前缀必须挂在 `/music/` 下，见 §6.2）；
**绝不改官方 bundle**，注入失败一律原样放行原始 HTML（最坏情况只是菜单里少一项）；
`ui_inject_enabled` 可一键关掉（v2.1.80 起在「发现音乐 → 排行榜单」控制条上就能点，也能 `POST /api/config`）—— 关掉后拦截层**不再认领**文档请求，
官方页面与注入前**逐字节一致**；toast 挂在 **shadow DOM** 里（不跟 Tailwind/Semi 抢层叠）；
注入脚本**不得读取或上报任何凭据 / token**。

### 8.1 下游待办：上游 v1.2.7 → v1.2.9 的 4 项（**3 项已完成**）

上游 `webspider7/fnmusic` 在 v1.2.8/v1.2.9 做了音质与下载相关的增强：

| 项 | 上游做法 | 我们现状 |
|---|---|---|
| **搜索返回真实可用音质** | 解析各平台真实码率字段动态构造 `Qualitys` | ✅ **已完成**（2026-09-16），见 `pkg/search/quality.go` 与 §5.7 |
| **`GET /api/music/qualities`** | 按 `source + id/songmid/hash` 实时查询该曲真实可用音质 | ✅ **已完成**（2026-09-17），见 `pkg/search/qualities.go` 与 §5.8。<br>**未按上游做法**：上游直接调平台详情接口，我们实测那四个接口普遍不可用（网易不返回音质、QQ 缺无损、酷狗返回空壳、酷我要 token），改用「搜索 + 按 ID 匹配」 |
| **下载质量选择弹窗** | `DownloadQualityModal.vue`，单曲下载前弹出真实可选档位 | ✅ **已完成**（2026-09-17），见 `frontend/src/components/DownloadQualityModal.vue`；`downloadManager.addSong` 增加第 4 个参数 `quality` |
| **FLAC 跨平台严格回退** | 请求无损却拿到 MP3 时跨平台重搜并**严格校验**直链含 `.flac`/`format=flac`/`rate=flac`/`fLaC` 且不含 `.mp3`/`.php` | ✅ **已完成**（2026-09-17 深夜 ③），但**刻意不照搬上游的严格校验**，见下方说明 |

> 注：上游的**下载器魔数识别、错误页拦截、纳秒临时文件、歌词 sidecar** 我们 fork 时已自带且更完善，无需再并入。

#### 关于「请求无损却拿到有损」——为什么没照搬上游的严格校验

上游要求直链**必须含** `.flac`/`format=flac` 等正向证据。我们没有照搬，理由：

大量 LX 音源的直链是 **CDN 不透明路径**（`https://cdn.x/api/v1/stream?token=...`），
根本看不出格式。要求正向证据会把这类**合法无损全部误杀**——比漏判更糟。

所以拆成两层，各司其职：

| 层 | 位置 | 职责 |
|---|---|---|
| **偏好** | `engine/quality.js` 的 `urlLooksLossy()` + `downloadManager.resolveWithPlatformFallback` | 只排除**显式有损**证据（`.mp3`/`.php`/`format=mp3`），让跨平台回退**优先**找到真无损；全找不到则退回有损结果（**有损也比下载失败强**），并带 `qualityMismatch: true` |
| **权威判定** | 后端 `SongResult.actual_format`（魔数嗅探）+ `isLossyContainer()` | 文件落盘后按**真实容器**判定。这是唯一可靠的结论，前端据此在任务上显示「实际 MP3」徽标 |

> ⚠️ **`Quality` 与 `ActualFormat` 是两个轴，别混**：
> `Quality` 是**档位**（flac24bit/flac/320k/128k，前端请求什么就回报什么）；
> `ActualFormat` 是**容器**（flac/mp3/ogg/ape/dsf/dff/wav/m4a，按魔数嗅探）。
> 320k 与 128k 的容器都是 mp3，所以只有 `ActualFormat` 能揭穿「假无损」。
> 注意 `wav`/`ape`/`dsf`/`dff` 也算无损，`LOSSY_CONTAINERS` 里**没有**它们。

### 8.2 用户点名的需求：完成情况（2026-09-16）

用户在 2026-09-16 提出的一批需求，**完成情况如下**（不要误以为全做完了）：

| 用户原话 | 状态 | 说明 |
|---|---|---|
| 「flow 的**飞牛连接**可以加入我的项目」 | ✅ 后端完成 | `pkg/fnos`：官方开放 API 目录授权 + 音乐接口 + 令牌读取 + `POST /api/fnos/push` |
| 「flow 的**账号连接**可以加入我的项目」 | ✅ 后端完成 | `pkg/account`：网易云与 QQ 扫码登录、日推、歌单 |
| 「**音源增加批量导入**，不管在线还是本地」 | ✅ 完成 | `POST /api/sources/import_batch`（另附导出/清理/批量删除） |
| 「里面的 **ui 我很喜欢，可以替换大部分到现有项目**」 | ✅ **已完成** | 顶栏重排为 flow 工作台布局 + 抽出统一外壳 `PageShell` + **全部页面**统一容器规范（1500px）。见 §3.3.1 |
| 「**flow 推送**也很有逻辑，逻辑比目前的监控感觉好些」 | ✅ **完成** | `pkg/push` + 7 个接口：订阅模型（源歌单/日推 → 飞牛歌单）、一键串接、定时同步、执行历史。见 §3.2 |
| 「**增加协议**（官方和其他除 lx 的协议），通过觅音可以看其他协议」 | ✅ **已完成**（官方协议部分） | `pkg/protocol` + `GET /api/protocols`：把「官方协议（后端内置，免脚本，搜索/榜单/歌单/歌词）」与「LX 协议（浏览器脚本，取直链）」显式建模，并**解除前端对音源的硬依赖**。见 §3.4 |
| 「**自动补下载最好 / 主页歌单一键订阅 / 可以停止 / 同时推飞牛**」（2026-09-21） | ✅ **完成**（v2.1.11） | 监控决策层新增**后端取链**（服务型音源）+ 前端**订阅状态机** facade（监控与推送一起建、一起停）。见 §4.1 与 §11 ㉓。⚠️ 飞牛真机闭环仍待验证 |

#### ✅ 已解决：内网音源取不了链（v2.1.12）

**问题**（2026-09-21 实测暴露）：`apisource` 的出站请求走 `pkg/security` 守卫，**默认拒绝内网/回环地址**
（实测报错「禁止访问回环地址 127.0.0.1」）。内网自建音源需要 `FN_ALLOW_PRIVATE_NET=1`，
但 `fpk-package/cmd/main` 启动脚本不设置它，飞牛也没有给用户配环境变量的界面 ——
**音源服务跑在局域网内的用户，连「填地址 → 探测协议」都过不去**，后端自动下载整条链是死的。

**结论：不改环境变量，改 `pkg/apisource` 自己放开内网访问**（`apisource.go` 的 `opts()`）。
理由是 `pkg/ai` 里已经有一模一样的先例和判断：

> AI 服务地址是**用户显式配置**的可信目标，且常见于局域网自建（Ollama / one-api / LM Studio）。
> 因此这里允许访问内网地址 —— **目标来自用户配置而非请求参数，不存在 SSRF 面**。

服务型音源地址是完全同一回事（用户在「音源管理」里自己填的 `lx_server` / `lx_api` 地址）。
所以**不需要新配置项、不需要新开关、不需要界面** —— 默认就是对的。
连带修掉两处面向用户的过期文案：`catalog.go` 的 `/api/sources/api` 接口说明、
`SourceManager.vue` 里教用户配 `FN_ALLOW_PRIVATE_NET=1` 的那行（那条提示在飞牛上根本做不到）。

**⚠️ 但有一处有意不放开 —— 下载器（`pkg/downloader`）仍保持默认拒绝内网。**
这不是漏改，是个真实的差别：

| 出站目标来自 | 放开内网是否安全 |
|---|---|
| `apisource` 的 base URL = **用户手输的地址** | ✅ 安全。放开的是用户自己指定的目标，无提权 |
| `downloader` 的音频直链 = **音源服务返回的地址** | ❌ 不安全。放开等于让那个服务能把 NAS 指向任意内网主机（云元数据 `169.254.169.254`、路由器后台…），是真实的 **SSRF 放大** |

后果：**若音源服务返回的直链本身是内网地址，下载那一步仍会失败**（报「禁止访问内网/元数据地址」）。
多数自建音源返回的是上游 CDN 公网直链，不受影响。
若真机上撞到这个，再单独讨论（`FN_ALLOW_PRIVATE_NET=1` 仍是全局逃生口，但它同时放开下载器）。


**参考实现位置**（要动手时先读这些）：
- flow 的完整源码：`F:\down\fnmusic-flow-extracted\app\docker\app\`（Python，含 `static/index.html` 单文件 UI）
- API 型音源协议的实现范例：同目录 `lx_source.py`（正则提取 `API_URL`/`API_KEY`，调 `{api}/url` 与 `{api}/search`）
- 推送调度模型：同目录 `scheduler.py`（订阅 → 拉取 → 推送 → 入队）
- 音源批量导入参考：`https://github.com/qwex888/miyin` 的 `server/services/source*.ts`

> **关于「其他除 lx 的协议」**：已核实觅音（miyin）本身**也只跑 LX 脚本**（在 Node 里跑，
> 见其 `server/services/sourceRuntime.ts`），它的「服务端平台适配」只覆盖**搜索**（`platformSearch.ts`），
> 与我们已有的 `pkg/search` 是同一层能力。因此「其他协议」目前**没有已验证的参考实现**。
> 若后续要做，候选方向：MusicFree 插件格式、AList/WebDAV 直连、Subsonic/Navidrome API。
> 动手前请先与用户确认具体要哪一种，不要自行发挥。

### 8.3 飞牛集成待办

> 📄 **「飞牛本地刮削需要什么数据」已调研完毕，见 `docs/飞牛刮削适配调研.md`**。
> 结论：**三条工程缺口**（不依赖 AI），按影响排序：
> 1. **从不触发飞牛重扫** —— `fnos.Music.ScanLibrary()` 是**死代码**（定义了但全仓库无人调用），
>    导致「下载完飞牛里看不到新歌」
> 2. **写完标签/歌词不 touch mtime** —— 飞牛按「路径 + mtime/size」做增量指纹，
>    只写 `.lrc` 时音频没变 → 飞牛不重读 →「改了歌词飞牛不更新」
> 3. **不写 genre** —— `tags.Metadata` 连 `Genre` 字段都没有，
>    两个容器也都没写 `TCON`/`GENRE` → 飞牛「风格」页空
>
> AI 只该负责「genre 值缺失时推断」「生成缺失封面」这类需要判断的部分 ——
> 前三项纯工程，且收益更大。**先补工程缺口再接 AI**，否则会出现
> 「AI 写了 genre 但飞牛不重扫所以看不到」这种白干。

| 项 | 说明 |
|---|---|
| **真机验证** | **所有飞牛/账号/推送代码都只经单元测试与假服务器验证**，未在真实 fnOS 上跑过。需装 FPK 后访问 `GET /api/fnos/status` 看两个 available 是否为 true |
| ~~前端扫码界面~~ | ✅ 已完成（`AccountManager.vue`）——但仍**未在真机走过完整扫码链路** |
| **飞牛前端 JS SDK（目录选择器）** | 已声明 `micro_app=true` 与 `api-scope`，但前端还没接 `@trimjs/web-app` 的 `pickSharedFile` |
| **QQ 音乐 WeChat 扫码** | 已实现 QQ 扫码；flow 还支持微信扫码（`open.weixin.qq.com/connect/qrconnect`），未移植 |
| **推送的保留期策略** | ✅ 已实现：`retention_mode` / `retention_days`（如日推只保留 7 天，到期自动清理本任务推送过的曲目）。入口在订阅行的「设置」里（v2.1.16 起） |
| ~~前端推送管理界面~~ | ✅ 已完成，v2.1.16 起并入 `LibraryManager.vue` 的「推送」页（见 §4.1.5） |

### 8.4 其他待办

| 项 | 说明 |
|---|---|
| ~~**监控全自动化**~~ | ✅ 已完成（v2.1.10）：歌单/收藏类后端自抓 + 网页端「补下载」，见 §4.1 与 §11 ㉑ |
| ~~**一键订阅 + 服务型音源后端自动下载**~~ | ✅ **已完成（v2.1.11，v2.1.12 修掉内网音源）**：监控决策层加「取链」动作（用已配置的服务型音源 `pkg/apisource` 自动取链下载）+ 主页歌单「一键订阅」（同时建监控+推送任务）+「停止更新」按钮。见 §4.1 与 §11 ㉓㉔。⚠️ 飞牛真机闭环仍待验证 |
| **`.ncm` / `.qmc` 解密** | 用户提过想要（让老资源能播）。算法已调研清楚（NCM = AES-128-ECB + 自定义 keybox；QMC 三套掩码），**Go 有 `crypto/aes` 比 Python 手写容易**，但 QMC 的掩码/RC4/QTag 需自己写，且无密钥的文件（STag）**永远解不开** |
| **CUE 整轨拆分** | 用户提过。需 ffmpeg；注意要带超时、要继承封面（参考项目在这两点上都有缺陷） |
| **AI 生成封面** | 参考实现 `music-tidy v1.14.0` 已有完整做法（`cover_prompt` + `generate_image`，需 `enable_ai_cover` 开关 + 生图模型）。**可搬，但必须标注「这不是专辑原封面」**。见 §8.5 |
| **`charts.go` 拆分** | 单文件 1665 行，4 音源×4 类接口样板重复，可抽泛型 helper |
| **前端虚拟滚动** | 大列表未做虚拟化 |
| **音源检测/禁用失效** | miyin 有「检测可用性 / 禁用失效音源」，我们**刻意未做** —— 音源脚本只能在浏览器执行，检测属前端职责（`lx-runtime.js` 已有 `probeSource`） |

### 8.5 AI 路线图：已完成与待办
> 完整评估（含判据与「明确不做」清单）见 **`docs/AI 能力评估.md`**；用法与约束见 §3.5。

**已完成**（2026-09-18，全部在没配 API Key 的情况下开发 + mock 测试）：

| 优先级 | 项 | 落地 |
|---|---|---|
| P0 | 文件名判名（`Suspect` 标记 + 候选消歧 + 防幻觉校验） | `pkg/nameparse` + `ai.JudgeName` + `complete.resolveName` |
| P0 | AI 生成命名正则（五道校验 + 可手改 + 用户规则优先） | `pkg/nameparse/rules.go` + `ai.SuggestNameRegex` + `/api/library/name-rules` |
| P1 | 搜索匹配消歧 | `ai.PickSongMatch` + `complete.pickByAI` |
| P1 | 歌词核验（写入前判断张冠李戴） | `ai.VerifyLyric` + `complete.writeOne` |
| P1 | 批量补全「先预览后写」 | `complete.Resolve` / `Execute` + `plan_id` |
| — | 撤销上一次补全（前置能力） | `pkg/complete/backup.go` + `/api/library/complete/undo` |

**待办**：

| 优先级 | 项 | 说明 |
|---|---|---|
| P2 | **genre 推断开放** | 代码已写（`inferGenre`），`UseAIGenre` **默认关**。开之前先确认**飞牛「风格」页是否真读内嵌 genre**（既有未验证项，见 §8.3） |
| P2 | **AI 生成封面** | 照抄参考实现；**默认关 + 必须标注非原封面** |
| P3 | **查重「规则决定、AI 解释」** | 现在 `pkg/tidy/dedupe.go` 是纯规则（`priorityOf` + SHA1）。**决策留给规则**（可复现可测试），只让 AI 把选择翻译成一句人话 —— 参考实现的 `reason` 字段确实有用 |
| — | **AI 真实准确率验证** | ⚠️ **一次都没验证过**（开发环境没配 Key，测试全是 mock）。真机验证方法见 §4.8 |

---

### 8.6 全项目扫描发现的问题（2026-09-18，**尚未处理**）

用户要求「检查这个项目还有什么没优化的」。下面是**用命令扫出来的**（不是凭印象），按严重度排。
**都还没改** —— 留着当待办清单。

#### 🔴 一、12 条路由没进 `catalog`（文档漂移，**最该修**）

`catalog.go` 号称「接口的单一数据源」，实测**注册了但 catalog 里完全没有**的有：

| 路由 | 分类 |
|---|---|
| `/api/player/check` `/api/player/resolve` `/api/player/stream` | **播放核心** |
| `/api/nas/scan` `/api/nas/directories` | NAS |
| `/api/sources/upload` `/api/sources/script` `/api/sources/custom` | 音源 |
| `/api/bridge/claim` `/api/bridge/result` `/api/bridge/jobs` | AI 解析桥 |
| `/api/ai/usage/reset` | AI |

**为什么算问题**：本项目的定位是「**主要供其他 AI / 外部程序调用**」，而 catalog 是它们
发现接口的**唯一入口** —— 这 12 条外部程序根本看不到，包括**播放相关的三条**。

**修法**：补条目 + **加一个一致性测试**（断言「`server.go` 注册的路由」⊆「catalog 的 Path」；
动态路由用前缀匹配，`/api/monitors/{id}` 对应注册的 `/api/monitors/`）。
有了测试，以后加接口忘了写文档会直接挂 CI。

> 复现命令：
> ```bash
> grep -oE 'mux\.HandleFunc\("[^"]+"' pkg/api/server.go | sed 's/.*"\(.*\)"/\1/' | sort -u > /tmp/r.txt
> grep -oE 'Path: "[^"]+"'        pkg/api/catalog.go  | sed 's/Path: "\(.*\)"/\1/' | sort -u > /tmp/c.txt
> comm -23 /tmp/r.txt /tmp/c.txt   # 注册了但没文档
> ```

#### 🔴 二、`pkg/api` 零测试（4,920 行 / 106 个接口）

全项目最大的测试缺口。**不建议**一上来就写全套 handler 测试（要搭 mux/config，
成本高收益低）；建议**先补两个高价值、低成本的**：

1. **上面那条 catalog 一致性测试** —— 一次投入，永久防漂
2. **纯函数的测试** —— 路径解析（`resolveLibraryPath` / `resolveAllowedPath`）、
   参数解析、`catalog` 本身的字段完整性。这些不需要起服务

> 参考：项目里已有 `docs/真机验证清单.md`，但**手写的接口路径会腐化**
> （§11 记过：一次查出 4 处路径写错）。测试才是不会腐化的那份。

#### 🟡 三、567 行前端死代码

`App.vue` 里 **import 了但模板从没用过**：

| 文件 | 行数 | 说明 |
|---|---:|---|
| `components/AudioVisualizer.vue` | 71 | 频谱可视化 |
| `components/VinylTurntable.vue` | 85 | 黑胶唱盘 |
| `components/LyricView.vue` | 136 | 歌词页 |
| `engine/audio-visualizer.js` | 275 | 上面第一个的引擎，**只被它引用** |

`ImmersivePlayer.vue` **已经内置了黑胶与歌词页**（搜 `Vinyl Disc` / `mobileTab === 'lyrics'`）
→ 这三个是重构后遗留的。

**注意**：Rollup **已经把它们 tree-shake 掉了**（在 `backend/dist/assets/*.js` 里搜
`能量柱`（可视化器的模式名）、`黑胶`（唱盘组件里的文案）等特征串，**搜不到**），
所以**不影响体积**，只是会误导读代码的人。
**要么删，要么加注释说明「暂未接线」** —— 别让下一个人以为它们在被用。

#### 🟡 四、`audio-visualizer.js` 的 resize 监听无法移除

```js
window.addEventListener('resize', () => this.resize())   // 匿名函数，移除不了
```
`stop()` 只 `cancelAnimationFrame`，**不移除这个监听**。组件卸载后监听还在，
每次 resize 都会调用已废弃引擎的 `resize()`。

> 不过它属于上面那条死代码 —— **删掉就没有这个问题**；要启用的话得先把监听存成字段。

#### 🟢 五、检查过但**没问题**的（别重复扫）

| 项 | 结果 |
|---|---|
| TODO / FIXME / HACK | **零**（命中的全在参考项目的 vendor 里） |
| 前端定时器/监听器配对 | `setInterval`/`clearInterval` 各 6 处、`addEventListener` 5 / `removeEventListener` 7 —— **基本都成对**，只有上面那 1 处例外 |
| 前端依赖冗余 | 6 个 runtime 依赖**全都在用**（`grep -rl` 逐个验证过） |
| 图标库导入 | lucide **按图标导入**，无 `import *`，已正确 tree-shake |
| `panic()` | `pkg` 下**零处** |
| gofmt / vet / 测试 | 全绿（460 个测试） |

#### 🔴 六、交接风险：`.dev/` 被 gitignore，但「统一入口」在里面

`.gitignore` 第 26 行是 `.dev/` —— **整个目录不纳入版本控制**。但 §1 告诉接手的人
「统一入口是 `bash .dev/wsl-run.sh`」，**这个脚本就在被忽略的目录里**。

**后果**：
- 如果**打包目录**交付 → 没问题（`.dev/` 跟着走）
- 如果**用 git 交付**（`git init` + push）→ 接手的人**没有 `wsl-run.sh`**，
  §1 的第一条自检就做不了。同样会丢的还有 `count-lines.sh` / `count-tests.sh` /
  几个 `probe-wy-*.sh`（这些是排查工具，丢了不影响主流程）

**修法（二选一，改 `.gitignore` 时要小心别把 `.dev/data` 放出去）**：
```
.dev/*
!.dev/wsl-run.sh
```
或者把 `wsl-run.sh` 挪到 `scripts/`（和 `dev.sh` / `build.sh` 放一起，语义也更对）。

> 💡 本轮已经按同样的道理，把数字自查脚本放在 **`scripts/audit-numbers.sh`**（会被跟踪），
> 而不是 `.dev/`。

#### ⬜ 七、这次**没查完**的（下次可以接着看）
- `charts.go` **1,699 行**（§8.4 已列「可拆分」，4 音源 × 4 类接口样板重复）
- `LibraryManager.vue` 1,639 行 / `SearchView.vue` 1,510 行 —— 前端最大的两个组件
- `pkg/proxy` 188 行无测试
- **bundle 583KB / gzip 187KB** —— 构成正常（Vue + crypto-js + buffer + axios + qrcode + 应用代码），
  唯一能显著改善的是**按需加载视图**（现在 `App.vue` 用 `v-if` 切视图，全在主 bundle 里）。
  属**结构改造**，收益（首屏快一点）与风险不成正比 —— **建议先不动**


## 9. 参考过的项目

| 项目 | 借鉴内容 | 注意 |
|---|---|---|
| [miyin](https://github.com/qwex888/miyin) | 音源熔断（3 次/60s）、音质阶梯、下载队列、歌词内嵌 | 它的 mp3 内嵌歌词实际写成 `TXXX:USLT` 而非标准 `USLT`（ffmpeg 的缺陷），我们写的是**标准帧** |
| [music-tidy](https://github.com/qwex888) | 三阶段扫描、枚举完整性安全门、sidecar 规则、去重优先级；**AI 用法**（见下） | 它**没有**年份/音轨刮削、**没有** tlyric 翻译、converter 无原子写 |
| [music-monitor](https://github.com/Baey666/music-monitor) | 版本过滤词表、匹配打分、时长校验、干跑预览 | 它**没有首轮基线**；改编版会绕过时长校验（死代码 bug） |
| **`leelaa.playlist`** v1.1.1（「AI 歌单」） | **推理模型的错误处理**（`content \|\| reasoning` 回退 + `finish_reason=length` 识别）—— 见 §4.2 坑 C | 飞牛上的第三方应用。**无源码**（Rust 二进制 + minified JS），只能学设计。调研见 `docs/参考项目/leelaa.playlist-调研.md` |
| **`fnmusic-ext`** v2.6.2（[javycoder/fnos_music_ext](https://github.com/javycoder/fnos_music_ext)） | **音乐入口接管**（`pkg/takeover` 的参考实现）：身份只信内核（`SO_PEERCRED` + `/proc/<pid>/exe`）、`renameat2(RENAME_NOREPLACE)` 原子替换、先 journal 后动 socket、staging socket 先起再发布、还原前预检上游。另有若干**尚未移植**的能力清单（边听边存 tee、收藏自动绑定本地、落库即补词图、推荐三层兜底、WebUI 热生效、fpk 安装向导）见 §8 | 它是 Python + 三容器架构，**只能学纪律不能照抄结构**。安全气味：容器内 `node:vm` 跑未受信 JS 而文档不说清、WebUI 只靠 `x-trim-isadmin: true` 单头认证且只拦 `/api/` 前缀。它**没有**本项目的路径安全（`pathguard` fail-closed）、SSRF 防护、曲库管家（增量索引安全门 / 去重 / `.tidy_trash` / dry_run+plan_id+undo） |
| **`fnmusic-flow`** v1.0.4 | QQ 扫码登录的**原始移植来源**（`qqmusic/qq-auth.mjs`）；网易云直接依赖社区库而非自研 | ⚠️ **它的 QQ 流程缺了 `check_sig` 这一步，照抄会卡死**（见 §4.9 坑 B）。**有参考实现 ≠ 参考实现是对的** |
| [**QQMusicApi**](https://github.com/L-1124/QQMusicApi)（L-1124） | **QQ 扫码登录的正确流程**（`qqmusic_api/modules/login.py` 的 `_authorize_qq_qr`）—— `check_sig` 换 p_skey 的做法与全部参数表 | **活跃维护**（Python）。QQ 登录出问题先看它，别只看 flow |
| [**@neteasecloudmusicapienhanced/api**](https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced) | **网易云登录的正确参数**（`module/login_qr_key.js` 的 `type: 3` + `login_qr_create.js` 的 chainId）—— 见 §4.9 坑 A | 就是 flow 用来做网易云的那个库（flow 没自研）。**npm 上活跃发版**，最新 4.40.x |

> **给扫码登录排错的一句话结论**：网易云看
> `@neteasecloudmusicapienhanced/api` 的 `module/login_qr_*.js`；
> QQ 看 `QQMusicApi` 的 `modules/login.py`。**别只信 flow** —— 它自己漏步骤。

**`music-tidy` 的 AI 用法**（源码已放进项目，见下）—— 本项目 2026-09-18 的 AI 功能大量参考它：

- **「结果必须来自输入」的防幻觉约束**（`judge_name` / `_llm_rescue`）——
  这是最有价值的一条，`ai.JudgeName` 直接照搬
- **「让 AI 生成规则」而不是「让 AI 做判断」**（`analyze_name_rule`）——
  `pkg/nameparse/rules.go` 的思路来源
- **触发条件用「规则自认可疑」而不是「字段为空」**（`_llm_should_rescue` 的 `suspect` 分支）——
  `nameparse.Result.Suspect` 直接照搬
- **`lyric_source` 字段级来源标注** —— 我们用它标记 `MatchedByAI` / `LyricRejected`

> ⚠️ **它做了但我们刻意不做**：**AI 生成歌词**。它的 prompt 明写
> 「若不确定完整歌词，请……创作合理、连贯……的中文歌词」，且时间码是估算值不会同步。
> 理由见 `docs/AI 能力评估.md` §5。

**参考源码位置**：

| 目录 | 版本 | 说明 |
|---|---|---|
| `docs/参考项目/music-tidy/` | **v1.12.0** | 早期提取，留着做版本对比 |
| `docs/参考项目/music-tidy-1.14.0/` | **v1.14.0** | **看 AI 部分用这个**（`server/ai_llm.py` 675 行 / 8 个方法）；附 `README.md` 指路 |

> 解包 FPK 的方法：`tar -xzf x.fpk && mkdir app && tar -xzf app.tgz -C app`
> （外层是 gzip 包，里面还有一层 `app.tgz`）

| [fnmusic-flow](https://github.com/) 1.0.4 | 推送的统一模型（`download_missing`，见 §3.2）、音源健康度、歌单分类 | 它用 Node 在**服务端**跑 lx 脚本（我们因合规约束不这么做）；1.0.4 的「内存索引匹配」针对的是它自己挂载的文件系统，**不适用于我们**（见 §3.2 说明） |
| [go-music-dl](https://github.com/guohuiyuan/go-music-dl) | 多平台能力矩阵（12 平台）、歌曲/歌单/专辑**链接解析**、WebDAV 同步 | 它自身 go 1.25 用不了；**底层库 `guohuiyuan/music-lib` 才是关键** —— go 1.18、纯 Go、仅依赖 `x/text`，**实测可在我们的 go 1.22 下编译**，但许可证是 **AGPL-3.0**，与我们的 **MIT** 冲突，引入需用户决策 |
| [轻乐集 music-tidy](https://github.com/) 1.14.0 | 歌单前缀命名解析（只有**两侧夹空白**的连字符才算歌手/歌名分界） | **核查过我们已符合**：`pkg/nas/scanner.go` 的 `parseBasicMeta` 用的就是 `strings.Split(base, " - ")`。1.14.0 本身只是改名+换图标，无功能可借鉴 |

### fnmusic-ext 的「推荐歌单」是拦截层现造的，官方库里没有（2026-10-05 查证）

做虚拟歌单之前必须先回答「那条『每日推荐』是谁造的」，否则会和官方库里的真歌单打架。
**结论：完全是 `fnmusic-ext` 在拦截时现造的，与我们无关，也不会和官方库冲突。**

**三条独立证据**：

1. **我们的仓库里 `online:playlist` 出现 0 次**（`grep -rn "online:playlist"` → 无命中）。
   `backend/pkg/push` 只推**曲目**：`PushTracks` → `Music.FindOrCreatePlaylist` → `AddTracks`
   → `SetPlaylistCover`（`backend/pkg/push/push.go:112,194,202`），**不造日推命名**。
2. **fnmusic-ext 侧全部只读**：`/tmp/fnme2/proxy/recommend.py` 里 4 处 `sqlite3.connect` 都是
   `file:...?mode=ro`（`:305`、`:340`、`:388`、`:453`）；库路径 `music_db_path()` 默认
   `/usr/local/apps/@appdata/trim.music/db/music.db`（`:149-151`，可被 `FNMUSIC_MUSIC_DB` 覆盖）。
   注入点是它自己的路由 `proxy/app.py:5936` `@app.get("/music/api/v1/playlist/list")`。
3. **真机上官方库确实是空的**（NAS 只读查 `music.db`）：`playlist` 表**存在但 0 行**、
   `playlist_track` **0 行**、`favorite_track` **0 行**、`track` **71**、`shared_library` **1**。
   `playlist` 的列是 `id, guid, name, cover_guid, user_id, created_at, updated_at`。

**它的命名（我们要沿用，切换时用户看到的名字才不会变）**：

| 常量 | 值 | 位置 |
|---|---|---|
| `DAILY_GUID_PREFIX` | `online:playlist:daily:` | `proxy/recommend.py:34` |
| `HOT_GUID_PREFIX` | `online:playlist:hot:` | `proxy/recommend.py:35` |
| `NM_GUID_PREFIX` | `online:playlist:nm:<id>` | `proxy/nmplaylists.py:37` |

- **每日推荐**（开关 `FNMUSIC_RECOMMEND_DAILY`）：`netease-daily`（网易真·每日推荐）→ `llm`
  → `local-random`（只读官方库，按账户播放历史「从未听过优先、最久未听补齐」）三级补齐到
  `PLAYLIST_SIZE = 20`，**按账户隔离**。
  ⚠️ 已知约束：网易的每日推荐**每天只有一份内容**，当天**仅第一个构建的用户**能用（占用「音源名额」）。
- **热门推荐**（开关 `FNMUSIC_RECOMMEND_HOT`）：`netease-charts`（网易热歌榜 toplist，免登录）
  + `lx-charts`（`kg TOP500` / `kw 飙升榜` / `wy 新歌速递`）。
- 歌单封面取「第一个带可用封面直链的在线曲目」，无在线封面则回落第一首带官方 `coverId` 的本地曲目；
  必须跳过酷我 `artistpicserver.kuwo.cn` —— 那个「封面 URL」其实是含图片链接的**文本页**，不是直链。

> **对我们做虚拟歌单的意义**：`guid` 里带 `:` 是安全的（官方歌单 guid 与曲目 guid 走不同校验路径），
> 所以能沿用同一套命名。但**当天的网易日推会被先到者占掉** —— 若哪天两者同时活着
> （socket 层已互斥，正常不会发生），会出现一边有内容一边空。

### 飞牛音乐 API 文档（本地资料，重要）

**`docs/飞牛音乐API.md`** —— 从飞牛音乐应用二进制解析 + 实测整理的完整接口文档。
**改 `pkg/fnos` 之前必读**，它把 socket 路径、token 来源、各端点与响应结构都写清楚了。

已核对：我们的 `pkg/fnos` 与文档**完全一致**（socket `/var/run/trim_music.socket`、
基地址 `http://localhost/music/api/v1`、token 从 `/var/lib/fnos-music-db/music.db`
的 `user_token` 表只读读取）。

**文档里有、但我们还没实现的端点**：

| 端点 | 用途 |
|---|---|
| `POST /playlist/remove-track` | 从歌单移除曲目 —— 可用来做「推送保留期策略」（见 §8.3） |
| `GET /search/album` | 搜索专辑 |
| `GET /search/artist` | 搜索歌手 |
| `GET /search/playlist` | 搜索公共歌单 |

> ⚠️ **文档确认：飞牛没有「列出全部曲目」的接口** —— 这是 §3.2 里
> 「无法在本地建带 GUID 的曲库索引」这一结论的依据。

---

## 10. 快速自检清单

接手后建议按顺序确认（**下面的数字是 2026-09-26 实测**，对不上说明环境有问题，不是文档错了）：

- [ ] `bash .dev/wsl-run.sh status` 环境正常（WSL 下）；或 Windows 下 `bash scripts/dev.sh` 能起来，浏览器能看到界面
- [ ] `go test ./...` 全绿（**698 个用例**，其中 8 个联网用例默认 skip）
- [ ] `cd frontend && npm run test` 全绿（**138 个用例**）与 `npm run build` 通过
- [ ] `GET /api/catalog` 返回 **122 个**接口（core 87 / internal 35），顶层带 `guide`（9 条任务食谱）
- [ ] `cd backend && go test ./pkg/api/ -run 'Catalog|Route'` 绿（源码：路由↔目录双向、目录自洽）
      `node .dev/catalog-audit.mjs http://127.0.0.1:8900` 输出「线上这一版与源码一致。」且 role/hidden 无不一致
- [ ] `GET /api/fnos/status` 可访问（非飞牛环境两个 available 为 false 属正常）
- [ ] `GET /api/accounts` 可用（未连接时 accounts 为空）
- [ ] `GET /api/push/tasks` 可用（初始为空列表）
- [ ] `GET /api/monitors` 可用（歌单监控）
- [ ] `GET /api/library/complete` 可用（AI 补全任务状态；没跑过时 `running=false`）
- [ ] `GET /api/library/name-rules` 可用（命名解析规则，初始为空数组）
- [ ] `bash scripts/build.sh` 能打出 FPK
- [ ] **在真实 fnOS 上验证飞牛与账号功能**（见 §8.2，尚未做过）
- [ ] **配一次真实 AI Key 验证 AI 功能**（见 §3.5 末尾与 §8.5 —— **准确率至今没验证过**）

> ⚠️ **数字容易腐化**，改完代码顺手复测。一条命令：
> ```bash
> bash scripts/audit-numbers.sh    # 后端包数/行数/测试数 + 前端行数/测试数 + 文档体积（HANDOVER 超 3000 行会 WARN）
> curl -s --noproxy '*' localhost:8900/api/catalog | python3 -c \
>   "import sys,json;d=json.load(sys.stdin);print(len(d['endpoints']))"   # 接口数
> ```
> 2026-09-18 一次审计就查出**六处数字过期**（§0 / §1.2 / §3.1 / §3.2 / §6 / §10），
> 其中 §10 自检清单写的是「304 用例 / 15 用例 / 84 接口」，实际是 **460 / 71 / 106** ——
> **接手的人照着一跑会以为项目坏了**。
> 2026-09-21 又一次审计，§0 与 §10 的六项数字同样全过期（已更新为 504 / 81 / 107）——
> **这节是重灾区，改完代码务必顺手复测。**

---

## 11. 变更记录（本工作区）

> **2026-09-26 拆分**：这一节积到了 4,595 行 / 310KB（占整个交接文档三分之二），已**按日期段拆到 `docs/变更记录/`**。
> 本文件从此只留常读的 §0–§10（约 2,711 行 / 183KB）。历史正文一字未改 —— 回合编号（㊽ …①）与「第几块」等标记都在，
> 所以别处写的「见 §11 ㊼ 块十」指的是 `docs/变更记录/` 里同名回合的小节。

- **目录**：[`docs/变更记录/INDEX.md`](docs/变更记录/INDEX.md)（82 个回合，倒序，每行带文件链接）
- 最近的回合：[`52 六项 UI 改造（导航密度 / 抽屉方向 / 播放栏三形态 / 深色玻璃皮肤）`](docs/变更记录/2026-09-28.md)、[`51 扫码登录搬进「发现音乐」+ 开放接口入口换成左上角软件图标`](docs/变更记录/2026-09-28.md)、[`㊿ 点歌不响的三层出站根因 + 切歌语义回到列表顺序 + 不再自动弹大屏`](docs/变更记录/2026-09-27.md)、[`㊾ 交接文档按日期段拆出 + 前端巨组件可测逻辑下沉`、`㊽ 移动端一层：应用外壳` 与 `㊼ 元数据闭环 + 平台故障可观测 + 接口目录双向钉死 + 写不了的格式单列 + 鉴权按来源分三层 + 补全任务可停止 + 静默失败收口 + 两套目录遍历合并成一份](docs/变更记录/2026-09-26.md)
- **每轮改完仍要写**（用户定的铁规，见 `.workbuddy-ai/memory/agent/README.md`）：正文改本文件 §0–§10，变更条目追加到 `docs/变更记录/` **当月文件的顶部**，并在 `INDEX.md` 加一行。

### 11.1 为什么拆、去哪找

| 想找什么 | 去哪儿 |
|---|---|
| 某个功能「怎么用、怎么改」 | 本文件 §3–§6（活文档，改代码前先读） |
| 某个功能「哪一轮改的、当时怎么验的」 | [`docs/变更记录/INDEX.md`](docs/变更记录/INDEX.md) → 对应日期段文件 |
| 最近这轮做了什么 | `docs/变更记录/` 里**最新的那个文件**，最上面就是 |

理由只有一条：接手者（人或 AI）第一个读的就是这份文档，490KB ≈ 16 万 token，读完就没有上下文预算干别的了；
而真正需要逐字读的规范早已在 §3（代码地图 / 前端约定）、§4（坑）、§5（业务规则）里沉淀过一遍，历史条目属于按需查阅。
体积有护栏：`scripts/audit-numbers.sh` 打印本文件与 `docs/变更记录/` 的行数/字节，本文件超过 3,000 行会提醒。
