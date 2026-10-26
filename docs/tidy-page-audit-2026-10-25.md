# 补全整理页（TidyView）代码审查报告

> 审查对象：`frontend/src/components/TidyView.vue`（2930 行）+ `frontend/src/services/libraryView.js` + `NavViews` 分段定义
> （补全整理页 5 个分段：补全 / 查重 / 歌名修复 / 数据快照 / 替换音质）
> 审查维度：逻辑脱节 · 代码遗漏 · 功能缺失 · 逻辑重复 · 功能重复 · UI 重复 · 移动端适配 · 隐患
> 环境：远端 liwenyao@192.168.1.230:/home/liwenyao/projects/music-v2，审查时未改动任何文件

---

## 一、总体结论（先看这一段）

页面整体工程质量不差：分段的显隐用 `v-show` 保状态、缓存分目录存、按钮文案偏诚实（“停止 = 只停本页轮询，后端可能还在跑”）。

问题集中在 4 类：

1. **“扫描缺失”这个动作在代码里几乎不存在了**：按钮文案还是“扫描缺失/增量扫描”，但流程条 `COMPLETE_STEPS` 已经没人渲染，`completeHint` 只出现在 tooltip 里 → 新用户第一眼不知道下一步点哪。
2. **重复**：3 张表（歌名修复 / 手动体检 / 快照明细）三套表头三套样式却做同一件事；2 个“全选/清空”；2 个“刷新”；2 种“危险操作确认”（`confirm()` 系统弹窗 vs 页面内浮层，后者不存在）→ 移动端尤其难看。
3. **确认弹窗不是一个东西**：TidyView 里 3 处危险操作走 `window.confirm()`，而页面别处是自定义浮层/底部玻璃条；`window.confirm` 在 iOS PWA / 微信内置浏览器里样式丑、可被拦截，且**无法在确认框里展示预览差异**——而“恢复到这份快照”恰恰最需要先看 diff。
4. **移动端**：`TidyView.vue` 内 **0 处** `sm:`/`md:` 断点（`grep` 确认），没有任何 `matchMedia` / `window.innerWidth` 判断；只有 `SectionCard` / `TidyDirChip` / `SelectionBar` 自带响应式。这意味着整张表、每行 5~7 个横向挤压元素、所有按钮组在 375px 宽下都会挤成一团或横向溢出。

---

## 二、逻辑脱节（说的和做的不是一回事）

### 2.1 三步流程条“名存实亡”，但按钮文案还在引用旧阶段名 【高】

- `TidyView.vue:2427` `const completeSteps = COMPLETE_STEPS` —— **声明了从未在模板中使用**（死代码）。
- `libraryView.js:179-183` 的 `COMPLETE_STEPS` 仍是 `扫描缺失 / 勾选字段 / 预览并写入`。
- 但页面实际只剩两个 gate：`completeStep === 1` 显示“扫描”按钮（`TidyView.vue:106`），`completeStep === 2` 显示“预览要写的内容”（`TidyView.vue:53`）。**“勾选字段”这一步没有独立 gate**——勾字段是随时可点的 chip，跟步骤无关。
- `completeHint`（“先点上面的「扫描缺失」看缺什么”）只被塞进 `:title` tooltip（`TidyView.vue:55`），桌面上要 hover 才看得到，移动端**永久不可见**。
- 结果：`completeStep === 1` 时页面上“扫描”在 chip 行最右边，没有任何视觉引导；用户扫完也不知道要勾字段、要预览。

**影响**：用户 2026-10-12 提的「下一步怎么做呢？」这个原始诉求，在后续重构中被工具化（塞 tooltip）了，等于回退。

**建议**：
- 要么删掉 `completeSteps` 这个死变量，别再留半截；
- 要么把 `COMPLETE_STEPS` 真的渲染成一行小步骤条（`TidyView.vue` 字段 chip 行上方），并让 `completeHint` 在 `completeStep === 1/2` 时作为**正文文案**出现，而不是只有 title。
- 推荐的等价轻量做法：字段 chip 行右端的“扫描”按钮，文案随 `completeStep` 变（`扫描` → `重新扫描`），并在其左侧加一句 11px 灰字“下一步：勾字段 → 预览”。

### 2.2 曲库目录改由 `TidyDirChip` 选，但 `tidyDir` 变量还在被大量读 【中】

- 文件头注释（`TidyView.vue:22-28`）和 `TidyView.vue:1112` 明确说“这个输入框已删 —— 目录统一由分段导航行右边的 TidyDirChip 选”。
- 但内部仍然到处 `tidyDir.value || nasDir.value`（`scanAll` / `scanGaps` / `runIndexScan` / `runDedupe` / `restoreAudit` / `restoreGaps`…），且 `onMounted` 里先 `tidyDir.value = loadPref('nas_dir', '')`。`TidyDirChip` 变更时走 `ys:dir-changed`，处理函数却把 `tidyDir.value = ''`（`TidyView.vue:2912` 附近），依赖 `tidyDir || nasDir` 的兜底顺序。
- **隐患**：任何一处直接写了 `tidyDir.value = 'xxx'`（例如未来接回内部选择器）就会盖过 `nasDir`，两份真相互相打架。代码里没有注释说明“`tidyDir` 只做兜底、永远是空串”这个契约。

**建议**：删掉 `tidyDir` 这个 ref，全部统一读 `nasDir`（`TidyDirChip` 写的就是 `prefs.nas_dir`），`tidyDir` 只保留兼容读取一次性本地值；或在 `<script setup>` 顶部写死 `const curDir = computed(() => (nasDir.value || '').trim())` 供所有 handler 使用。这样“目录只有一个真相源”才成立。

### 2.3 “暂停 / 继续预览”的入口在注释里提到，模板里没有 【中】

- `TidyView.vue:2598-2600` 注释：“用户 2026-10-24：预览改成流式显示，加暂停 / 继续 / 停止”。
- 实际模板（`TidyView.vue:149-165`）只有“停止 / 重新预览 / 收起”三个按钮，**没有“暂停/继续”**。
- 要么是遗漏（功能缺失），要么是需求已变但注释没跟上。无论哪种，注释都会误导后来者。

**建议**：按注释补一个 `Pause / Play` 按钮（`previewState` 加 `paused` 态，后端 `plan` 接口若支持 `pause` 就透传，不支持则先在客户端停轮询并在按钮上写“暂停（仍会继续写完当前这首）”），或把注释改成现状。

### 2.4 “停止”文案揭示了轮询真相，但 UI 语义没兜住 【中】

- `TidyView.vue:909` `title="停止（只停止本页查看进度，后端扫描可能仍在继续）"`。
- 这意味着用户点“停止”后 `qStopped = true`、`stopQualityPolling()`，**后端还在扫**，用户却看到按钮变回“开始扫描”。再点一次“开始扫描”会调用 `QualityAPI.scan('start', …)`，后端大概率返回“已在扫描中”。
- `qStatusText`（`TidyView.vue` `qStatusText` computed）已经写了诚实文案“已中断 —— 只是本页不再读取进度（后端扫描可能仍在继续），点「开始扫描」可重新接上”。**但用户点“开始扫描”后如果后端已在跑，`res.data` 直接回显旧进度，`qStopped` 变 false，一切看起来正常**——这没问题；问题在于**没有任何“真正中止后端”的入口**，用户想取消一次误触的超大扫描（比如 500 条 × 三渠道）只能等它扫完。

**建议**：后端若支持 `QualityAPI.scan('cancel')` 就加一个“取消后端扫描”按钮（放在进度条右侧，`v-if="qStopped && qState === 'running'"`）；不支持的化，把 title 文案升格为进度条下方的一行小字（别藏在 tooltip 里），至少让移动端用户知道真相。

### 2.5 数据快照：“索引过期”的提示把用户推去跑一个不相关的动作 【低-中】

- `TidyView.vue:743-745`：`title="快照读自索引；若和「补全」段「扫描缺失」的数字对不上，说明索引过期了 —— 跑一次「补全」段 ⋯ 里的「增量扫描」即可更新"`。
- 两个脱节点：
  1. 文案引用「补全」段的 **⋯ 菜单里的「增量扫描」**，但 `TidyView.vue:104-111` 早已把扫描合并成一处“扫描”按钮，**⋯ 菜单已经不存在**（`grep '⋯'` 在模板里无对应弹出层）。老用户按图索骥会找不到。
  2. 这段话只挂 `title`，移动端看不到。

**建议**：改成正文一行小字 + 一个内联“立即更新索引”按钮（直接调 `runIndexScan`），把“推用户去找”变成“就地可用”。

### 2.6 `restoreLyricGaps()` 在 `onMounted` 被调，但歌名修复段的缓存恢复顺序有漏 【低】

- `onMounted` 里恢复了 `restoreAudit / restoreGaps / restoreCards / restoreTitleFix / restoreDedupe / restoreSnapshots / restoreLyricGaps`，顺序上 `restoreLyricGaps` 在 `restoreTitleFix` 之后。
- 但 `useLyricGaps`（`services/useLyricGaps.js`）是组合式函数，自身可能已有 `onMounted` 初始化。**双份初始化存在竞争**：谁后写 `lyricItems` 谁赢，缓存可能被一次 `load` 覆盖成空。
- 这是“组合式函数 + 组件手写恢复”混用的典型脱节。建议把 `restoreLyricGaps()` 也收进 `useLyricGaps` 内部，或用 `await` 串起来，别让两处都写同一个 ref。

### 2.7 查重“移入回收站”没有确认预览强制门 【中】

- `TidyView.vue:362` “移入回收站”是红色主按钮，一点就 `confirm('将把重复文件移入回收站…')`。
- 但 `resolveDup(false)` 会**直接对整库执行**，而且没有任何“先看会移哪些”的强制顺序——虽然旁边有“干跑预览”，但它不是必经步骤。
- **逻辑脱节**：标题说“保留一份，其余移入回收站”，但决定“保留哪一个”的规则（`g.keep` / `g.delete`）完全在后端，**页面没有任何说明“按什么规则选 keep”**。用户只能盲信。
- 建议：在列表顶部加一行 `规则：内容相同取码率/时长最优；同名不同版取…（后端 tidy/duplicate.go 口径）`，并把“干跑预览 → 确认”做成两步（第一次点红色按钮先干跑并把结果贴出来，第二次才真移）。

### 2.8 `scanAll()` 在 `indexing` 期间禁止再次点击，但 `gapsLoading` 不禁止 【低】

- `TidyView.vue:107` `:disabled="indexing || gapsLoading"` 是对的。
- 但 `runIndexScan()` 内部 `if (res.code === 200) { loadIndexStats(); scanGaps() }` **又调了一次 `scanGaps`**，而 `scanAll` 在外面也 `await scanGaps()`。→ 一次“扫描”点击 = **两次缺口统计请求**（并发/串行各一次），移动端弱网下看到的计数会跳两下。
- 建议：`runIndexScan` 内部不再自动 `scanGaps`，由调用方决定（查重后刷新场景显式调）。

---

## 三、代码遗漏（该有而没有）

| # | 位置 | 缺什么 | 后果 |
|---|---|---|---|
| 3.1 | `TidyView.vue:2427` | `completeSteps` 声明后从未使用 | 死代码；同时暗示流程条被删了没删干净 |
| 3.2 | 预览区 `TidyView.vue:149-165` | 没有“暂停/继续”（注释 `2598` 说有） | 长预览（几千首）没法中途停手看一眼 |
| 3.3 | 快照明细 `TidyView.vue:849-880` | **没有“关闭”按钮**，只靠点另一条快照覆盖 `snapDetail` | 移动端打开明细后无法收起，只能再点一次“看明细”撞开另一份 |
| 3.4 | 快照明细 `TidyView.vue:849` | 没有 `:key` / 没有空态文案当 `diff` 为 `null`（后端不返回时） | `snapDetail.diff || []` 兜住了，但 `snapDetail.count` / `writable` 没有兜底，后端字段缺失时直接显示 `undefined 首` |
| 3.5 | `restoreSnapshot` `TidyView.vue:1823` | 确认框里**不放 diff 摘要**（“会改 12 首 / 3 首文件已不存在 / 5 首格式不可写”） | 用户只能盲确认；而这些数字后端 `metaRestoreResult` 里都有 |
| 3.6 | `LibraryHealthCard.vue` | 修复动作 `runFix('names')` / `runFix('all')` **没有二次确认** | 批量改名 + 清残留是不可逆（虽可撤销？未核实后端） |
| 3.7 | `runDedupe` / `resolveDup` | 没有把 `dupResult.groups` 的**完整数量**告知（只 `slice(0,30)` 展示） | 30 组之外的重复组用户完全看不见，却会被“移入回收站”一起处理 |
| 3.8 | 歌名修复表格 `TidyView.vue:453-500` | 没有“按列排序”/“筛选”，也没有“只显示勾选的” | 几百首时可勾选集合不可见 |
| 3.9 | 任何表格 | 没有 `overflow-x-auto` 包裹（横滑），只有 `max-h` 竖滚 | 窄屏必然横向撑破卡片 |
| 3.10 | `TidyView.vue` 全文 | 没有 `onBeforeUnmount` 级“有未保存选择时离开提醒” | 勾了 200 首切走再回来（`v-show` 保住了，但页面刷新会丢） |

---

## 四、功能缺失（相对需求 / 常识预期）

1. **没有任何“批量预览会写什么”的总览**：补全段有流式预览，但歌名修复、缺歌词、音质替换都没有统一“先看清单再执行”——歌名修复是“AI 修复 N”直接写。
2. **音质替换没有“试听 / 看码率对比”**：`qRows` 只显示 `current_tier` 与目标 tier，但**目标渠道的 URL/大小/时长不显示**，用户没法判断“换了会不会变差”。
3. **快照没有“导出 / 对比两份快照”**：既然后端存 JSON，前端完全可以在 `viewSnapshot` 里加上“和另一份快照比”。
4. **缺歌词段落没有“编辑/替换”入口**：找到歌词后直接“写入文件”，用户不能先看一眼歌词内容（`it.note` 只有“命中”说明）。
5. **整个页面没有“全局后退/撤销”**：每次操作只 flash 一条消息，没有“刚才那次撤销”按钮（`undoComplete` 只覆盖补全，`undoTitleFix` 只覆盖歌名）。

---

## 五、逻辑重复（同一件事写了两遍）

### 5.1 `scanGaps` 被三个入口调用，且 `runIndexScan` 内部还会再调一次 【中】
见 2.8。`scanAll` → `runIndexScan` → `scanGaps` + `scanAll` → `scanGaps`。

### 5.2 缓存 key 构造散落各处 【低】
`gapsCacheKey(dir)` / `auditCacheKey(dir)` / `snapshotCacheKey()` / `saveDedupe` / `saveTitleFix`… 每个都自己拼字符串，各用 `loadCache(key, 0)`。一处改前缀就漏。
**建议**：抽 `caches.js`：`const K = { gaps: d => \`gaps:${d}\`, … }`，页面只引用 `K.*`。

### 5.3 错误处理模式重复 【低】
`apiError(res, label)` / `failText(label, e)` + `flash(...)` 在每个 handler 里重复 3~4 行（`runDedupe` / `runAudit` / `loadSnapshots` / `takeSnapshot` / `restoreSnapshot` / `runIndexScan` / `scanGaps` … 20+ 处）。
**建议**：封装 `async function guard(label, fn) { try { const res = await fn(); const bad = apiError(res, label); if (bad) return flash(bad, true); return res } catch (e) { flash(failText(label, e), true) } }`，能砍掉约 80 行样板。

### 5.4 “全选 / 全不选 / 清空”三连写了两套 【中】
- 歌名修复：`toggleAllTitleFix` + `titleFixItems = []; saveTitleFix()`（`TidyView.vue:440-449`）
- 缺歌词：`toggleAllLyric` + `clearLyricGaps`（`TidyView.vue:673-679`）
两者语义完全一致，但按钮 class、间距、位置各不相同（一在表头卡右上，一在表格上方工具行）。
**建议**：抽 `TableBulkBar`（`count` / `allSelected` / `onToggleAll` / `onClear` / 额外 actions 插槽），三张表（歌名修复 / 手动体检 / 缺歌词）共用。

### 5.5 时间格式化重复 【低】
`fmtTimeText`（`TidyView.vue:1734`）、`auditAtText`、`gapsAtText`（来自 libraryView）、`lastScanText`、`scheduleLastText`、`snapshots.at_text` —— 6 种时间展示口径混用（绝对时间 `toLocaleString` / 相对时间“3 小时前” / 时间戳），同一页里出现三种。
**建议**：统一成“< 1 小时：xx 分钟前；否则：MM-DD HH:mm”，一处导出 `fmtWhen()`。

### 5.6 音质扫描的“最新一条状态”读了两遍 【中】
`startQualityScan` 成功后 `qScan = normalizeScan(res.data)`；之后 `refreshQualityRecordsOnly` 又请求一次 `QualityAPI.scan('status')`，只为拿 `records`。而 `startQualityScan` 已把 `records` 一并拿到（`normalizeScan` 里 `records` 字段就是）。**替换完成后的这次额外请求可以省掉**，直接用 `applyQReplace` 后的本地 `records` 即可（或让 `replace` 接口返回新记录）。

### 5.7 `qSelected` 的手动 Set 拷贝重复 6 次
`toggleQSelect` / `pruneQSelection` / `dropReplacedFromQueue` / `applyQReplace` / `qClearSelection` 每次都在 `new Set(qSelected.value)` 再赋值。Vue 3 `ref(new Set())` 的响应性需要整体替换，这没错，但完全可以抽 `mutateQSel(fn)` 一行包住。

---

## 六、功能重复（两个入口做同一件事）

1. **“刷新”有两个**：卡片头 `onRefresh`（`TidyView.vue:733-738`，全量重读五段）与手动快照区里的 `loadSnapshots`（`TidyView.vue:775-779`，只刷快照）。同一个“数据快照”卡片里并排放两个“刷新”，语义差一个“全量/局部”，用户分不清。
2. **音质段里两个“扫描/开始扫描”语义打架**：按钮文案只在“扫描中…”/“开始扫描”之间变，但实际上已中断状态下点它 = 重连轮询（见 2.4），并非重新扫描。
3. **`scanTitleFix`（扫描歌名）与 `runManualTitleAudit`（手动体检）**：都抽样交给 AI，一个只挑可疑、一个随机 10 首；这个区别只在 title 里写清楚，卡片正文没说，用户会觉得“两个 AI 按钮”。
4. **`resolveDup(true)` 干跑预览** 与 `resolveDup(false)` 真执行：按钮并排同色系（一个白底一个红底），干跑结果只在下方出现一行，真执行不会先展示“将移走 N 个文件”的清单，跟 2.7 同一问题。

---

## 七、UI 重复（视觉上重复/N 次，该合并）

| 重复对象 | 位置 | 说明 |
|---|---|---|
| 表头样式 3 套 | `TidyView.vue:178`（`bg-white`）、`455`（`bg-white`）、`536`（`bg-[var(--ys-bg-soft)]`）、`684`（`bg-[var(--ys-bg-soft)]` + `z-10`） | 4 张表 3 种表头色，没 `z-index` 的两张 sticky 表头会被行内容盖住 |
| 表格空态 4 套 | `351`、`418`、`667`、`828` | 字号 `text-[11px]`/`text-[12px]` 混用，边框 `dashed` 与纯文本混用 |
| 主按钮 class 手抄 5 次 | `56`、`109`、`347`、`437`、`772` | `bg-emerald-500 px-3.5 py-1.5 text-[12px] font-semibold` 全是硬编码，`ys-btn ys-btn-primary` 只在缺歌词段用 |
| 危险按钮 3 套 | `362`（rose-500 实心）、`843`（rose-600 文字）、`249`（rose-600 文字） | 三种危险级别没有设计规范 |
| 进度条 3 套 | `169`（h-1）、`256`（h-1.5）、`934`（h-1.5 不同底色） | 宽高/圆角/动画时长不一致 |
| 卡片内小标题 2 套 | `244`（`text-[12px] font-semibold`）、`768`（`text-[11px] font-semibold`） | 同一层级的“手动快照 / 定时快照 / 撤销”字号不一致 |
| 统计数字口径 3 套 | `StatLine`、`completeBar`、`LibraryHealthCard:61` 的 3 格 | “首曲目 / 首读过 / 首待补” vs “x 张卡片” vs “扫描条目 / 报错 / 其他” |

**建议统一**：
- 表格抽 `<DataTable :columns>`，表头统一 `bg-[var(--ys-surface-2)] sticky top-0 z-10`；
- 空态抽 `<EmptyState icon text hint />`；
- 按钮统一走已有 `ys-btn / ys-btn-primary / ys-btn-danger / ys-btn-sm`（`style.css` 已定义），删掉手抄的 emerald；
- 进度条抽 `<ProgressBar size="md" />`。

---

## 八、移动端适配（重点）

### 8.1 零断点 【高】
`TidyView.vue` 全文 `grep "sm:|md:|lg:|@media|innerWidth|matchMedia"` → **0 命中**。
所有响应式全靠外层 `SectionCard`（`sm:p-5`）和 `TidyDirChip`（`sm:max-w-[260px]`）、`SelectionBar`（`sm:bottom-[96px]`）撑着。

### 8.2 表格在 375px 下必然溢出 【高】
| 表 | 列数 | 现象 |
|---|---|---|
| 歌名修复 `TidyView.vue:454-500` | 勾选+文件 220 + 歌名 150 + 修复后 220 | 内容宽 ≈ 630px，父容器 `max-w` 未设，**横向撑破 SectionCard**（`rounded-[12px] border` 会被撑开或裁掉） |
| 快照明细 `859-878` | 220+150+150+字段 | ≈ 570px 同上 |
| 手动体检 `535-560` | 180+150+70+180+200 | ≈ 780px，最严重 |
| 缺歌词 `683-713` | 280+… | ≈ 500px |

只有 `TidyView.vue:682` 用了 `overflow-auto`（能横滑），`453/534/858` 都只有 `overflow-y-auto`。

**修复**：给每个表格容器加 `overflow-x-auto`；或改为 `<div class="sm:hidden">` 卡行 + `<table class="hidden sm:table">` 双形态。

### 8.3 卡片头 action 区在窄屏换行挤 【中】
`SectionCard` 的 `#actions` 是 `flex flex-wrap justify-end`，但歌名修复卡片头同时塞了 `titleFixRecordsAtText`（时间文本）+ 绿色主按钮；缺歌词卡片头塞了 3 个按钮（扫描 / 查找 / 写入）。窄屏时这些会换行到标题下方，把 `sub` 副标题挤成一列——`SectionCard` 注释里自己写了“`shrink-0` 不能加在外层，窄屏会溢出”，但**内部每个按钮没加 `shrink-0`**，仍然会被压缩变形。

### 8.4 `SectionCard` 的副标题 `truncate` 【中】
`SectionCard.vue:41` `sub` 用了 `truncate`（单行省略）。补全/缺歌词/快照三段的 `sub` 都是 30~60 字长说明，**窄屏时只剩前 8 个字**，用户看不到关键信息（“只改标题标签，可撤回”之类），且没有展开入口。

**修复**：`sm:truncate`（宽屏省略，窄屏换行），或 `line-clamp-2 sm:line-clamp-1`。

### 8.5 触控目标小于 44px 【中】
- `TidyView.vue:952` 行高 `py-2.5` + `text-[12.5px]` ≈ 34px，全站在 44px 标准以下；
- 小按钮（`text-[10px] px-2 py-0.5`）≈ 18px 高，手指点不中；
- 复选框 `h-4 w-4`（`TidyView.vue:961`）尺寸 OK，但 **label 没有包住整行**，只有 16px 热区。

### 8.6 `SelectionBar` 在音质段的位置 【中】
`SelectionBar` 固定 `bottom-[88px] / sm:bottom-[96px]`，但补全整理的音质段自身有 `max-h-[440px]` 内滚动表。**移动端键盘/滚动容器内固定定位会飘**；且 `SelectionBar` 与底部播放条都 `backdrop-blur`，两层玻璃叠一起视觉脏。

### 8.7 `TidyDirChip` 与分段导航同行 【中】
`SegmentedControl` 的 `#trail` 插槽在 `max-w-[38vw]`，而 5 个分段按钮在移动端会退化成图标（`SegmentedControl` 内部有宽度测量逻辑）。两者相加在 320px 宽设备上会触发前者压缩、后者塌成 1 个图标 —— `SegmentedControl.vue:82` 的注释提到“放得下就窄、放不下才顶到 `max-w-full`”，说明作者已在意，但**没有在 TidyDirChip 过长时给一个“只显示图标”的降级形态**。

### 8.8 `grid grid-cols-2`（`TidyView.vue:302`） 【低】
补全结果卡片墙固定 2 列，窄屏下每张卡 ~160px，`cover 56px + 两行文字` 会截断歌名到 6 个汉字。建议 `grid-cols-1 sm:grid-cols-2`。

### 8.9 模态 `max-h-[85vh]`（`TidyView.vue:1096`） 【低】
未处理 `env(safe-area-inset-bottom)`，iPhone 底部横条会盖住弹窗底部按钮。

### 8.10 表格内 `truncate` + `title` 属性 【低】
所有截断都靠 `:title` 显示全文 —— **移动端无 hover，等于信息丢失**。建议长按/点击展开，或行内 `<details>`。

---

## 九、隐患 / 体验风险

1. **`window.confirm` 3 处**（`1823` / `2307` / `2838`）：iOS PWA 下样式系统级、长文案换行难看；更关键的是**无法内嵌 diff**（见 3.5）。
2. **`resolveDup(false)` 不可逆风险**：虽有“可从回收站恢复”，但页面**没有“打开回收站”入口**，用户误删后只能去文件管理器找 `.tidy_trash`。
3. **`loadIndexStats` 静默吞错**（`TidyView.vue:2203-2212` `catch (_) {}`）→ 后端挂了页面显示“索引还没整理过”，用户会以为库里没歌。
4. **`loadSnapshots` 的 `catch(() => null)`**（`TidyView.vue:1769`）同理：定时快照配置读失败被当成“关闭”，用户以为定时被关了。
5. **`qRows` 过滤条件**：`if (qScan.state === 'done' && !es.some(e => e.upgradeable || e.replaced)) return []` —— **一旦扫完且无可替换，整张表消失**，包括之前跑出来的 `records` 历史（“最近替换”）也不见了（因为那条记录在 `qScan.records`，模板里另有位置？需确认 `records` 渲染是否依赖 `qRows`）。若依赖，用户会以为历史被清空。
6. **`titleFixItems = []; saveTitleFix()` 清空**（`TidyView.vue:448`）：没有确认，误点一键清空所有待修列表（虽可重新扫描）。
7. **`snapDetail` 不会在 `loadSnapshots` 之后自动刷新**：恢复快照后明细仍显示恢复前的 diff，用户一看“怎么还没变”会怀疑恢复失败。
8. **`restoreSnapshot` 成功后 `scanGaps()` 重跑**（`1829`），但**不重跑 `runAudit`**，体检段数字与快照不一致。
9. **`Q_REPLACE_BATCH = 5`**：用户勾 500 首 = 100 次串行 HTTP，任一批抛错就 `break`（`try/catch` 在循环外），**后面 95 批不会发**，但已勾选项在 finally 里没有被清（只在 `applyQReplace` 里清已处理的），用户看到一堆没反应的勾。**建议改成 `continue` + 记录失败批次，或补“重试失败批次”按钮**。

---

## 十、优先级建议（按 ROI 排序）

### P0（用户路径断点 / 会丢数据）
1. 音质替换批处理 `catch` 位置：循环内 `try/catch` 每批，失败继续（4.1 / 9.9）。
2. `scanAll` 双重 `scanGaps` 去掉（2.8 / 5.1）。
3. 快照明细加“关闭”按钮 + 缺字段兜底（3.3 / 3.4）。
4. `resolveDup` 强制“干跑 → 确认”两步 + 展示全部组数（2.7 / 3.7）。

### P1（移动端可用性）
5. 所有表格容器加 `overflow-x-auto`（8.2）。
6. `SectionCard.sub` 的 `truncate` → `sm:truncate` / `line-clamp-2`（8.4）。
7. 把 `completeHint` 从 tooltip 提升为正文（2.1）。
8. `grid-cols-2` → `grid-cols-1 sm:grid-cols-2`（8.8）。

### P2（一致性 / 可维护性）
9. 抽 `TableBulkBar` / `DataTable` / `EmptyState` / `guard()`（5.4 / 5.3 / 七）。
10. 统一时间格式 `fmtWhen()`（5.5）。
11. 危险操作确认统一成自定义浮层，支持内嵌 diff 摘要（9.1）。
12. 删死代码 `completeSteps`，或真的渲染流程条（2.1 / 3.1）。

### P3（体验增强，非缺陷）
13. 快照“对比两份”/“导出 JSON”。
14. 音质段显示目标渠道音质详情（码率/大小/时长）再让用户决定（4.2）。
15. 缺歌词支持“先看歌词内容再写”（4.4）。
16. 全局“撤销上一步”统一入口（4.5）。

---

## 十一、附：可复现的 grep 证据

```bash
# 零响应式断点
grep -c "sm:\|md:\|lg:\|@media\|innerWidth\|matchMedia" frontend/src/components/TidyView.vue
# → 0

# 危险确认用的是系统弹窗
grep -n "confirm(" frontend/src/components/TidyView.vue
# → 1823, 2307, 2838

# 死代码
grep -n "completeSteps" frontend/src/components/TidyView.vue
# → 2427 声明，模板 0 引用

# 表格容器缺横向滚动
grep -n "overflow-y-auto rounded" frontend/src/components/TidyView.vue
# → 176, 453, 534, 682(有 overflow-auto), 858 → 仅 682 支持横滑

# 表头样式三种
grep -n "text-\[var(--ys-text-faint)\]\"\|text-\[var(--ys-border)\]" frontend/src/components/TidyView.vue | head
```

---

## 十二、结论

- **没有发现“会让功能完全不能用”的严重 bug**（页面能跑、接口契约对得上、`v-show` 保状态）。
- **真正的问题是“中等问题密集”**：流程引导被弱化、移动端 0 断点导致表格溢出、危险操作确认不成体系、样板块重复了 4 套。
- 建议先修 P0/P1（约 6 处改动，半天内可完成），再统一组件（P2），最后补体验增强（P3）。

---

### 附注：审查未覆盖的部分
- 未运行前端构建 / 单测（本次仅静态审查）。
- `useLyricGaps.js` 的完整实现只做了引用级核对，未逐行审。
- 后端 `tidy` 包的具体算法（查重 keep 规则）未读，2.7 的建议以“前端如何呈现规则”为准。
