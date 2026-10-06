# scripts/lx-fixtures —— 校验用的「音源脚本」

这些 `.js` **不是**音源脚本，**不要**丢进 `preset_sources/` 或任何音源目录。
它们是 `scripts/lx-utils-check.mjs` 的夹具：长得像洛雪音源（用 `globalThis.lx`、
`lx.send(EVENT_NAMES.inited, …)`、`lx.on(EVENT_NAMES.request, …)`），因为只有这样
才能被**真**探针 / **真**宿主按音源脚本的路径加载 —— 直接 import 测不到 vm 沙箱那一层。

| 文件 | 谁加载它 | 验什么 |
|---|---|---|
| `utils-check.js` | 探针（`lx-probe.mjs`） | 把 `lx.utils` 每个成员真调一遍，结果送进 `inited`，供外部 oracle 逐字段比对 |
| `trap-check.js` | 探针 | miss 陷阱：一级/二级/三级缺失都要被报出，真实成员零误报 |
| `require-whitelist-check.js` | 探针 | 沙箱 `require` 白名单：放行的真能用、拒绝的真抛错且信息可读 |
| `require-denied-fs.js` | 探针 + 真宿主 | 顶层 `require('node:fs')` → 加载期就必须**明确失败** |
| `require-denied-childprocess.js` | 探针 + 真宿主 | 顶层 `require('node:child_process')` → 同上 |
| `utils-guard.js` | **真宿主**（`sidecar/lx_host/server.mjs`） | 端到端：宿主给的 utils 五项能力全对才返回直链，`POST /resolve` 才算过 |

跑法只有一条命令：

```bash
node scripts/lx-utils-check.mjs
```
