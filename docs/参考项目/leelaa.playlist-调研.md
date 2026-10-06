# 参考调研：`leelaa.playlist` v1.1.1（「AI 歌单」）

> 来源：用户提供的 `leelaa.playlist-1.1.1.fpk`（2026-09-18 解包）
> 目的：评估「它的 AI 歌单能不能直接拿来用 / 代码好不好抄」
> 结论先说：**代码抄不了（没有源码），但设计值得借鉴，而且已经从中发现并修掉了我们的一个真缺口。**

---

## 0. 结论

| 问题 | 回答 |
|---|---|
| **能直接拿来用吗** | ❌ **不能**。后端是 9.8MB 的 **Rust 编译产物**（stripped ELF，无源码），前端是 **minified JS + 无 sourcemap**。而且技术栈完全不同（它 Rust、我们 Go；它前端直连 LLM、我们后端调） |
| **代码好抄吗** | ❌ **不好抄**。`HomeView` 176KB 只有 131 行，标识符全是 `$` / `Ae` / `cn` 这种 mangled 名。读不出源码结构 |
| **设计值得借鉴吗** | ✅ **值得**。它的 prompt 是**完整可读**的（字符串不压缩），架构从二进制里也能还原出来 |
| **对我们有实际价值吗** | ✅ **有，而且立刻兑现了一条**：它的「推理模型坑」防御直接暴露了我们的一个**静默失败**（见 §4） |

---

## 1. 它是什么

飞牛（FNOS）上的本地 **AI 歌单助手**：用自然语言从**自己的曲库**里挑歌组成歌单。
支持双曲库源（Navidrome / 飞牛音乐），自带播放器。

> ⚠️ **注意定位差异**：它是「从**我的曲库**里挑歌」；我们是「按歌名歌手**去平台搜**」。
> 功能不重叠 —— 所以不存在「直接拿来用」的语境，顶多借设计。

---

## 2. 架构（从二进制与打包产物还原）

```
┌─ Rust 后端（axum 0.7.9 + tokio 1.51.1 + rusqlite 0.39.0 + reqwest 0.13.2）
│  路由：/health /avatar /playlists /song-features /navidrome /hf-proxy /analyze-audio /crypto
│  · 调 ffmpeg 解码音频 → f32 PCM（AUDIO_ANALYSIS_SAMPLE_RATE、-f f32le）
│  · ONNX 本地推理 → 生成 audio_embedding / lyrics_embedding
│  · 存进 SQLite 的 song_embeddings 表（两列 TEXT）
│  · 模型从 https://hf-mirror.com/ 下载（/hf-proxy 转发，国内可用）
└─ Vue 3 前端（打包产物）
    · 自然语言 → 用预计算的 embedding 做**特征检索**筛出候选
    · 候选交给**远程 LLM** 挑（多 provider）
```

**它支持的 LLM provider**（前端硬编码的 base_url）：
`api.deepseek.com` / `api.moonshot.cn` / `api.openai.com` / `api.siliconflow.cn` /
`dashscope.aliyuncs.com/compatible-mode` / `open.bigmodel.cn/api/paas/v4`

> ⚠️ **它在前端直连 LLM API** —— API Key 存在浏览器里。我们不这么做（后端调、Key 不出后端）。

---

## 3. 它的 AI 设计（prompt 完整可读，这是最有价值的部分）

### 3.1 两段式：先解析意图，再推荐

```
你是一个音乐推荐系统的意图解析器。你的任务不是推荐歌曲，而是把用户输入解析为结构化 JSON。
只返回一个 JSON 对象，不要输出任何解释、Markdown 或代码块。
```

### 3.2 「特征检索 → 候选 → LLM 挑」

```
## 候选歌曲（必须仅从中挑选）
已从全库 {N} 首歌曲中，通过特征检索为你筛选出以下 {M} 首最相关的候选歌曲。
警告：你【必须】且【只能】从以下给出的候选歌曲列表中挑选，绝对禁止伪造歌曲 ID，
也禁止挑选不在列表中的歌曲，否则将导致系统崩溃！
提示：为了提供"渐进式"的无缝播放体验，请尽量返回丰富的曲目
（比如挑选 20-30 首符合主题的歌曲），以便系统直接将其加入播放队列。
```

- 候选上限 **500**（超出部分显示「还有 N 首未列出」）
- **强约束措辞**：「必须」「只能」「绝对禁止」「否则将导致系统崩溃」

### 3.3 「分组渐进筛选」——候选太多时分批过滤

```
你现在处于"分组渐进筛选"阶段，不生成最终歌单，只做本组候选过滤。
必须遵守:
1. 只能从给定候选列表中选择歌曲
2. songs 必须是歌曲 ID 的字符串数组
3. 本组最多选择 {N} 首，至少选择 1 首
```

### 3.4 迭代调整（**我们没有的交互**）

```
请保留满意的歌曲，剔除不满意的歌曲，并补充新的歌曲；
如果是完全重新生成，则可忽略当前歌单。
```
以及「换一批」：
```
请换一批风格相近但不同的歌曲。
```

### 3.5 上下文注入

用户画像（性别 / 年龄 / 偏好）+ **当前天气** + 时间 + 情绪，都拼进 prompt。

### 3.6 JSON 契约反复强调（说明踩过坑）

```
请严格只返回一个 JSON 对象。
不要输出 Markdown，不要输出代码块，不要输出解释文字。
songs 里只能保留真实存在且不重复的歌曲 ID。
songs 必须是字符串数组，例如 ["abc123", "def456"]，不要返回对象数组，
不要返回歌名，不要返回占位符。
```

---

## 4. ⭐ 最有价值的发现：推理模型的静默失败（**已修**）

它的错误处理里有这么一段：

```js
v = qe(R), H = v.content || Ve(v.reasoning);
if (H) return i == null || i(H, H), H;
throw d === "length"
  ? new Error("AI 推理耗尽了输出 token，未生成最终正文；已自动扩大额度重试但仍无结果")
  : p.trim()
    ? new Error("AI 只返回了推理过程，没有返回最终正文；非流式重试仍无结果")
    : new Error("AI 返回成功，但流式和非流式响应都没有正文内容");
```

**它在处理三件事**：
1. `content || reasoning` —— 推理模型（DeepSeek-R1 / QwQ 等）把思维链放 `reasoning_content`，
   **content 可能是空的**；空时回退到 reasoning
2. 识别 `finish_reason === "length"` —— 推理把 token 额度吃光了
3. 三种失败给**不同的、可行动**的错误信息

### 我们当时的状态：完全没处理

查了我们的 `pkg/ai`：

- `FinishReason` **解析了但从没用过**
- `reasoning_content` / `reasoning` **完全没接**

**后果是静默失败**：用户配了推理模型 → `content` 为空 → `ParseJSONObject("")` 失败 →
所有 AI 功能（判名 / 挑歌 / 核验 / 生成正则）都表现为「没结果」，
而错误信息是空的 —— **用户完全不知道是自己配了推理模型**。

### 已修（2026-09-18）

- `chatMessageOut` / `chatDelta` 增加 `reasoning_content` 与 `reasoning` 两个字段名
- **非流式**：content 空 → 回退到 reasoning；两者都空且 `finish_reason=length` →
  报「大模型输出被 token 上限截断（finish_reason=length）—— 推理模型常把额度耗在思维链上，
  请调大 max_tokens 或换非推理模型」
- **流式**：同样攒 reasoning，正文一直没来就回退；否则给带 `finish_reason` 的明确错误
- 顺手把匿名的 `Choices` 嵌套结构抽成具名类型（`chatMessageOut` / `chatDelta` / `chatChoice`）——
  匿名嵌套结构在改字段时极易漏改一处

**测试 7 个子例**：正常模型 / reasoning_content 回退 / reasoning 字段名 / length 截断 /
无 finish_reason / SSE 回退 / SSE 空响应报错。

---

## 5. 值得借鉴但**我们暂时不做**的

| 设计 | 为什么不做 |
|---|---|
| **ONNX 本地推理 + embedding 特征检索** | 它的场景是「从**自己曲库几万首**里挑」，必须先缩小候选。我们的场景是「按歌名歌手**去平台搜**」—— **搜索本身就是检索**，不需要 embedding。真要做「AI 从我的曲库里挑歌单」时，这才是标准答案 |
| **分组渐进筛选** | 我们 `maxMatchCandidates = 20` 直接截断就够。候选池真要变大时再用这个思路 |
| **前端直连 LLM API** | API Key 会暴露在前端。我们后端调 |
| **迭代调整（保留满意的 / 换一批）** | 这是**很好的交互设计**，但我们的补全是「批量补缺」不是「交互式挑歌」，场景不匹配 |

**唯一可以直接借鉴措辞的**：它那套「必须 / 只能 / 绝对禁止 / 否则将导致系统崩溃」的强约束写法。
我们的 `PickSongMatch` / `JudgeName` 已经说了「只能从候选里挑」，但语气可以更重。

---

## 6. 顺带记下的手法

- **FPK 解包**：`tar -xzf x.fpk && mkdir app && tar -xzf app.tgz -C app`（外层 gzip，内含 `app.tgz`）
- **从 stripped Rust 二进制里挖架构**：
  ```bash
  strings -n 8 server-rs | grep -oE "^/api/[a-z0-9/_-]+" | sort -u     # 路由
  strings -n 8 server-rs | grep -oE "src/[a-z_/]+\.rs" | sort -u        # 模块结构（含依赖的）
  strings -n 8 server-rs | grep -oE "[a-z0-9-]+-[0-9]+\.[0-9]+\.[0-9]+" # 依赖版本
  ```
  ⚠️ `src/*.rs` 大多来自 cargo registry 里的**依赖**，要用 `grep -v cargo/registry` 过滤
- **从 minified JS 里挖 prompt**：字符串字面量**不会被压缩**。
  用正则抓含关键词的长字符串即可（见 `docs/AI 能力评估.md` 提到的手法）。
  但要**过滤掉代码**：含大量 `{` 或 `function` 的丢弃。
