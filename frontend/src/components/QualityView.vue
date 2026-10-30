<template>
  <!--
    音质替换 =「补全整理 → 音质」这一段的**实现**（2026-10-29）。

    它一度被拆成独立顶层页，用户当天又改口：「音质替换还是放回补全整理页面吧」——
    整理曲库是一次性的活儿，换音质也属于「把这一批歌弄好」，不该为了它再找一页。
    所以现在它由 App.vue 的 `#inner-tidy-quality` 槽挂在整理页的第四段；
    组件本身**不知道**自己在那儿（不读 segmentOf），谁挂它都一样能用。

    用户分类口径：整理页按心智分 —— 补全（缺东西）/ 修复（东西不对）/
    音质（换文件本身）/ 快照（怕出事）。

    页面内容与原「补全整理 → 替换音质」段一字不差，只是外壳换成独立页：
    接后端 /api/quality/scan|replace（pkg/intercept/quality.go，契约见 api/client.js 的 QualityAPI 注释）。
  -->
  <PageShell>
    <SectionCard
      :icon="Gauge"
      title="替换音质"
      sub="对比三条渠道能拿到的最高档位，勾选后替换本地文件"
    >
        <!-- 控制行：范围 / 开始（running 就地变进度形态） / 停止 / 数量上限 -->
        <div class="flex flex-wrap items-center gap-2">
          <!-- `swipe-no-drag`：这是页内筛选器，不是页面分段器。不标的话它自己也是
               `[role="tablist"]`，手指压在上面横划会被 SwipePager 判成「换分段」，
               而实际应该什么都不做（见 SwipePager.pickLevel 的第 4 条规则）。 -->
          <SegmentedControl
            class="swipe-no-drag q-scope basis-[13rem] shrink-0 grow-0"
            :model-value="qMode"
            :segments="qModeSegments"
            @update:model-value="setQMode"
          />
          <button
            type="button"
            class="ys-btn ys-btn-primary ys-press"
            :disabled="qRunning || qBusy"
            :title="qScanBtnTitle"
            @click="startQualityScan"
          >{{ qScanBtnText }}</button>
          <button
            v-if="qRunning"
            type="button"
            class="ys-btn ys-btn-sm ys-press shrink-0"
            :disabled="qStopBusy"
            title="停止扫描（会真的让后端停下，已探到的结果保留）"
            @click="stopQualityScan"
          >
            <Square class="h-3 w-3" :stroke-width="2" />
            {{ qStopBusy ? '停止中…' : '停止' }}
          </button>
          <label class="ml-auto flex items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
            上限
            <input
              v-model.number="qLimit"
              type="number"
              min="1"
              max="500"
              class="ys-num w-16 rounded-[8px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-2 py-1.5 text-center font-mono text-[12px] text-[var(--ys-text)] focus:border-[var(--ys-accent)] focus:bg-white focus:outline-none disabled:opacity-50"
              :disabled="qRunning || qBusy"
            />
            条
          </label>
        </div>

        <!--
          换范围（仅看有损 / 全部曲库）只改**下一次**扫描的参数：后端的扫描是独立任务，
          不会因为这里换了个范围就自己重扫 —— 不说明的话用户会以为「全部曲库」没生效
          （2026-10-29 反馈「全部曲库按钮有点问题」的另一半：按钮点得动，但看不出发生了什么）。
        -->
        <p v-if="qModeHint" class="mt-1.5 text-[10.5px] leading-relaxed text-[var(--ys-text-aux)]">{{ qModeHint }}</p>

        <!-- running：scanned/total + 细进度条（数字一律 .ys-num） -->
        <div v-if="qRunning" class="mt-3">
          <div class="flex items-center justify-between text-[11px] text-[var(--ys-text-muted)]">
            <span>已扫描 <b class="ys-num text-[var(--ys-text)]">{{ qScan?.scanned || 0 }}</b> / <b class="ys-num text-[var(--ys-text)]">{{ qScan?.total || 0 }}</b> 条</span>
            <span>已发现可替换 <b class="ys-num text-[var(--ys-text)]">{{ qScan?.upgradeable || 0 }}</b> 条</span>
          </div>
          <div class="ys-progress mt-1.5 w-full">
            <i :style="{ width: qPercent + '%' }"></i>
          </div>
        </div>

        <!-- 状态行：running / 已中断（诚实文案） / truncated -->
        <p v-if="qStatusText" class="mt-2.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">{{ qStatusText }}</p>

        <!-- 失败态：state=failed 或后端给了 error，或请求层失败（接口未上线时走这里，不白屏） -->
        <InlineNotice
          v-if="qScan && (qScan.state === 'failed' || qScan.error)"
          class="mt-2.5"
          :text="'扫描失败：' + (qScan.error || '后端没有给出原因，详情见日志')"
        />
        <InlineNotice v-if="qErrorText" class="mt-2.5" :text="qErrorText" />

        <!--
          筛选条（2026-10-27）：一库几万首时，扫描结果里绝大多数是「已经是最高音质」
          的行，用户真正要处理的那几十条被淹在里面。默认不过滤（保持原样），
          勾一下就只看能替换的。
        -->
        <div v-if="qRowsAll.length" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-[var(--ys-text-muted)]">
          <label class="flex min-h-8 cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              class="h-3.5 w-3.5 accent-[var(--ys-accent)]"
              :checked="qOnlyUpgradeable"
              @change="setQOnlyUpgradeable($event.target.checked)"
            />
            只看能替换的
          </label>
          <span v-if="qHiddenRowCount" class="text-[var(--ys-text-aux)]">已隐藏 {{ qHiddenRowCount }} 条无需替换</span>
          <span class="ml-auto text-[10.5px] text-[var(--ys-text-aux)]">本次共 {{ qRowsAll.length }} 条</span>
        </div>

        <!--
          行内「用这个音源替换」的口径说明（2026-10-29 加）。
          用户要的是「右边除了能看到来源，再给一个点击选择：这首歌用这个音源替换」，
          所以每行右侧三个渠道格现在是**按钮**（点一下＝选它，再点一下＝撤销）。
          但后端 /api/quality/replace 目前只收 paths（intercept.ReplaceQuality(ctx, paths)），
          每条一律按该首各渠道的最高档位取音，**不支持按渠道指定** —— 指定结果已经随请求
          一起发上去（body.picks，后端今天会忽略），暂时只用于本页的标记/排序。
          这句话必须留在界面上：不能让用户以为点了「原生」就真从原生取文件。
        -->
        <p
          v-if="qRowsAll.some((e) => e.upgradeable && !e.replaced)"
          class="mt-1.5 text-[10.5px] leading-relaxed text-[var(--ys-text-aux)]"
        >
          点行里的渠道格＝指定「这首歌用这个音源替换」（再点一下取消，同时计入底部「已选」）。
          注：后端目前仍按每首各渠道的最高档位取音，指定渠道已随请求发出、暂作标记 —— 按渠道取音待后端支持。
        </p>

        <!-- 结果表：列表 + 发丝线（不做卡片套卡片）。只有可替换且未替换的行才能勾选 -->
        <div v-if="qRows.length" class="mt-2 overflow-x-auto overscroll-x-contain rounded-[12px] border border-[var(--ys-border)] bg-white">
          <div class="overflow-y-auto" :style="{ maxHeight: '440px' }">
            <div
              v-for="e in qRows"
              :key="e.path"
              class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-[var(--ys-border)] px-3 py-2.5 last:border-b-0"
              :class="e.replaced ? 'bg-[var(--ys-surface-hover)]' : ''"
            >
              <!-- 8.5：原来是裸 16px 的 input —— 手指点不中。用 label 撑成 32×32 热区，视觉尺寸不变 -->
              <label
                class="flex h-8 w-8 shrink-0 items-center justify-center"
                :class="(!e.upgradeable || e.replaced) ? 'cursor-not-allowed' : 'cursor-pointer'"
                :title="e.replaced ? '已替换' : (e.upgradeable ? '勾选后可用最高音质替换' : '已是各渠道最高音质，无需替换')"
              >
                <input
                  type="checkbox"
                  class="h-4 w-4 accent-[var(--ys-accent)]"
                  :checked="qSelected.has(e.path)"
                  :disabled="!e.upgradeable || e.replaced"
                  @change="toggleQSelect(e)"
                />
              </label>
              <div class="min-w-0 flex-1 basis-[180px]">
                <div class="truncate text-[12.5px] font-medium text-[var(--ys-text)]">
                  {{ e.title || baseName(e.path) }}
                  <span v-if="e.ext" class="ml-1 text-[10px] uppercase text-[var(--ys-text-aux)]">.{{ String(e.ext).replace(/^\./, '') }}</span>
                </div>
                <div class="mt-0.5 truncate text-[11px] text-[var(--ys-text-muted)]">{{ e.artist || '未知歌手' }}</div>
                <div v-if="e.rowError" class="mt-0.5 text-[10.5px] leading-relaxed text-red-500">替换失败：{{ e.rowError }}</div>
              </div>
              <span
                class="shrink-0 rounded-full bg-[var(--ys-surface-sunken)] px-2 py-0.5 text-[10.5px] text-[var(--ys-text-muted)]"
                title="当前本地文件的档位"
              >{{ e.current_tier || '—' }}</span>
              <div class="flex shrink-0 gap-1.5">
                <button
                  v-for="cell in qChannelCells(e)"
                  :key="cell.ch"
                  type="button"
                  class="q-cell w-[56px] rounded-[8px] border px-1 py-0.5 text-center"
                  :class="cell.picked
                    ? 'q-cell-picked border-[var(--ys-accent)]'
                    : cell.pickable
                      ? 'cursor-pointer border-[var(--ys-border)] hover:border-[var(--ys-accent)]'
                      : 'cursor-not-allowed border-[var(--ys-border)]'"
                  :disabled="!cell.pickable"
                  :aria-pressed="cell.picked ? 'true' : 'false'"
                  :title="cell.hint"
                  @click="pickQChannel(e, cell.ch)"
                >
                  <div
                    class="text-[9px] leading-tight"
                    :class="cell.picked ? 'font-medium text-[var(--ys-accent)]' : 'text-[var(--ys-text-aux)]'"
                  >{{ cell.label }}</div>
                  <div
                    class="q-cell-tier truncate text-[10px] leading-tight"
                    :class="cell.tier ? 'ys-num text-[var(--ys-text-muted)]' : 'text-[var(--ys-text-aux)]'"
                  >{{ cell.tier || '拿不到' }}</div>
                </button>
              </div>
              <span v-if="qPickOf(e)" class="shrink-0 text-[10.5px] text-[var(--ys-accent)]">用{{ Q_CHANNEL_LABELS[qPickOf(e)] || qPickOf(e) }}替换</span>
              <span v-if="e.replaced" class="shrink-0 text-[10.5px] text-[var(--ys-text-muted)]">已替换为 {{ e.newTier || '更高档位' }}</span>
            </div>
          </div>
        </div>

        <!-- 空态（需求 m02848：一条都没有时不留白） -->
        <!-- 被筛选筛空的要说清是「筛选」而不是「没得换」，否则用户会以为白扫了 -->
        <div v-else-if="qRowsAll.length && qOnlyUpgradeable" class="ys-empty mt-2">
          当前筛选下没有可替换的曲目 —— 取消「只看能替换的」可以看到全部 {{ qRowsAll.length }} 条
        </div>
        <div v-else-if="qState === 'done'" class="ys-empty mt-3">
          曲库里的歌已经是各渠道能拿到的最高音质了
        </div>
        <div v-else-if="qState === 'idle' && !qRunning && !qErrorText" class="ys-empty mt-3">
          还没有扫描结果 —— 点「开始扫描」，让洛雪 / musicdl / 原生三条渠道逐条对比
        </div>

        <!--
          替换 / 下载进度条（2026-10-24 用户原话：「用户选择歌曲并替换后，在下方增加正在
          替换或者正在下载的进度条」）。
          · 只在真有批次在跑时出现，收尾立刻收起，没进行中时不占位；
          · 与「扫描中」那条同一个 Quiet Glass 形状（1.5px 细条 + 圆角），
            强调色只用在进度本身，文字统一 11px 次要灰；
          · 文案说清「正在下载并替换 i / total」，并带上这一批在换的那首歌。
        -->
        <div v-if="qReplacing" class="mt-2.5">
          <div class="flex items-center justify-between gap-2 text-[11px] text-[var(--ys-text-muted)]">
            <span class="min-w-0 truncate">
              正在下载并替换
              <b class="ys-num text-[var(--ys-text)]">{{ qReplaceStep }}</b> /
              <b class="ys-num text-[var(--ys-text)]">{{ qReplaceTotal }}</b> 条
              <span v-if="qReplaceCurrent" class="text-[var(--ys-text-aux)]"> · {{ qReplaceCurrent }}</span>
            </span>
            <span class="ys-num shrink-0 text-[var(--ys-text-aux)]">{{ qReplacePercent }}%</span>
          </div>
          <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-black/[0.06]">
            <div
              class="h-full rounded-full bg-[var(--ys-accent)] transition-all duration-300"
              :style="{ width: qReplacePercent + '%' }"
            ></div>
          </div>
        </div>

        <!--
          没换成的那些：给一个「重试」入口（2026-10-27）。
          以前失败行只是留在表格里带一行红字，想把它们再试一遍得自己一条条回勾；
          而且失败集合在收尾时会从勾选里移除（避免用户以为已经处理过），
          所以这里必须有个地方把它们接住，否则那批勾选就悄悄丢了。
        -->
        <div
          v-if="!qReplacing && qRetryPaths.length"
          class="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-bg-soft)] px-3 py-2"
        >
          <span class="min-w-0 flex-1 text-[11px] leading-snug text-[var(--ys-text-muted)]">
            有 <b class="ys-num text-[var(--ys-text)]">{{ qRetryPaths.length }}</b> 条这轮没换成（原因写在下表对应行）
          </span>
          <button
            type="button"
            class="shrink-0 rounded-full border border-[var(--ys-border)] bg-white px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition hover:text-[var(--ys-text)] disabled:opacity-50"
            :disabled="qBusy"
            @click="retryFailedReplace"
          >
            重试这些
          </button>
          <button
            type="button"
            class="shrink-0 rounded-full px-2 py-1 text-[11px] text-[var(--ys-text-aux)] transition hover:text-[var(--ys-text-muted)]"
            @click="qRetryPaths = []"
          >
            忽略
          </button>
        </div>

        <!-- 最近替换记录：原地覆盖看不出变化，它是唯一凭据（需求 m04229） -->
        <div v-if="qRecords.length" class="mt-3">
          <div class="flex items-center gap-2">
            <span class="text-[11px] font-medium text-[var(--ys-text-muted)]">最近替换</span>
            <span class="text-[10px] text-[var(--ys-text-aux)]">共 {{ qScan?.records?.length || 0 }} 条</span>
            <!--
              自动补强的记录也在这本账上（2026-10-23）：补强轮是「替换音质」的自动版，
              结果只写进自动下载记录的话，用户来这一页自查就永远看不到它。
              这里把两者的条数分开说清，免得看到没印象的条目。
            -->
            <span v-if="qAutoCount" class="text-[10px] text-[var(--ys-text-aux)]">
              （其中后台自动补强 {{ qAutoCount }} 条）
            </span>
          </div>
          <div class="mt-1.5 overflow-hidden rounded-[12px] border border-[var(--ys-border)] bg-white">
            <div
              v-for="(r, i) in qRecords"
              :key="i"
              class="flex items-center gap-2 border-b border-[var(--ys-border)] px-3 py-2 last:border-b-0"
            >
              <span
                class="h-1.5 w-1.5 shrink-0 rounded-full"
                :class="r.ok ? 'bg-emerald-600' : 'bg-[var(--ys-text-faint)]'"
              ></span>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-1.5">
                  <span class="truncate text-[12px] text-[var(--ys-text)]">{{ r.label }}</span>
                  <span
                    v-if="r.by === 'upgrade'"
                    class="shrink-0 rounded bg-[var(--ys-bg-soft)] px-1 py-px text-[9.5px] text-[var(--ys-text-muted)]"
                    title="后台音质补强轮自动试的（节奏在「设置 → 后台音质补强」里调，失败会隔天再试）—— 不是你手动点的替换"
                  >自动补强</span>
                </div>
                <div class="mt-0.5 truncate text-[10.5px] text-[var(--ys-text-muted)]">
                  {{ r.tierText }}<span v-if="r.channel"> · {{ Q_CHANNEL_LABELS[r.channel] || r.channel }}</span>
                </div>
              </div>
              <span class="ys-num shrink-0 text-[10px] text-[var(--ys-text-aux)]">{{ r.when }}</span>
            </div>
          </div>
        </div>

        <!--
          勾选浮出批量条（底部深色玻璃，项目已有组件）。

          ⚠️ 必须 Teleport 到 body（2026-10-29 用户：「歌曲选择后，底部的已选提示和替换按钮
          位置不对，我只能往下滑才能看到这个组件」）。

          根因不是「它排在内容后面」，而是**它其实一直是 position:fixed**：
          SelectionBar 内部就是 `fixed inset-x-0 bottom-[88px]`，但本页挂在 SwipePager 的
          某一页里，而 SwipePager 的滑轨是靠 `transform: translate3d(...)` 横移的
          （SwipePager.vue 的 applyTransform）——**带 transform 的祖先会成为 position:fixed
          的包含块**，于是这个 fixed 不再相对视口，而是相对那条好几页宽的滑轨：既被排到了
          滑轨底部/侧边，也不跟着视口走，用户只能滚到内容末尾才勉强看见它。
          （HANDOVER 里「滑动页里的弹窗必须 Teleport to body」记的是同一个坑。）

          Teleport 出去之后它才真的贴在视口底部：内容区照旧滚动，已选条始终可见；
          没有勾选时它自己 v-if 淡出，因为它是 fixed 的，不会引起任何布局跳动。
          底部安全区由组件自己的 `pb-safe`（padding-bottom: var(--safe-bottom)）负责，
          移动端不会被 iOS home indicator 挡住。
        -->
        <Teleport to="body">
          <SelectionBar
            :count="qSelectedCount"
            :actions="[{ key: 'replace', label: '替换所选', icon: Replace, disabled: qBusy }]"
            @action="onQAction"
            @clear="qClearSelection"
          />
        </Teleport>
    </SectionCard>
  </PageShell>
</template>

<script setup>
/**
 * 音质替换页（2026-10-29 独立）。
 *
 * 从 TidyView 的「替换音质」段整段搬出 —— 模板、状态机、批处理逻辑一字未改，
 * 只做了三件事：
 *   ① 外壳换成 PageShell + 单张 SectionCard（不再有 v-show 分段）；
 *   ② 去掉跨段依赖 pendingSelectionCount 里的另两段勾选（本页没有它们）；
 *   ③ 补齐本页自己的 flash / baseName 等小工具（原来借用 TidyView 的）。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import PageShell from './PageShell.vue'
import SectionCard from './SectionCard.vue'
import SegmentedControl from './SegmentedControl.vue'
import SelectionBar from './SelectionBar.vue'
import InlineNotice from './InlineNotice.vue'
import { FileAudio, Gauge, Library, Replace, Square } from 'lucide-vue-next'
import { QualityAPI, DEFAULT_AUTO_DOWNLOAD_CHANNELS } from '../api/client'
import { fail as failText, apiError } from '../services/userMsg'
import { ok as notifyOk, fail as notifyFail } from '../services/feedback'
import { loadPref, savePref } from '../services/prefs'
import { fmtWhen } from '../services/libraryView'

/** 页面提示统一走全局 feedback（右下角浮层），与原整理页同一口径 */
function flash(msg, isError = false) {
  if (isError) notifyFail(msg)
  else notifyOk(msg)
}

/** 从绝对路径取文件名（音质记录/表格里没有歌名时用它兜底） */
function baseName(p) {
  return String(p || '').split('/').pop()
}

let onBeforeUnload = null

// ── 音质替换（需求 m02757/m02848；2026-10-29 从整理页独立成页）──
//
// 接后端 /api/quality/scan|replace（pkg/intercept/quality.go，契约见 api/client.js 的 QualityAPI 注释）。
// 几个刻意的口径，改之前先读：
//  · scan 是**异步任务**：start 只是启动，进度靠轮询 status（2s 一次，对齐补全轮询）。
//    本页只在打开时挂载，切走再切回会重新读一次 status（历史记录不丢）；
//  · 「停止」只停本页轮询 —— 后端并不知道，状态文案必须诚实「已中断」，不许假装任务停了；
//  · 只有 upgradeable 的行可勾选；替换一次最多 50 条，超出前端分批顺序发（这里一批只发 5 条，
//    见 Q_REPLACE_BATCH —— 50 条一批要等好几分钟，进度条根本看不出在动）；
//  · 替换中在结果表下方给一条细进度条；收尾**只**增量刷新「最近替换」记录、把成功的行移出
//    扫描队列（不整表重扫 —— 用户的滚动位置/勾选视角保持连续），失败把后端 error 写回该行。
const Q_CHANNEL_LABELS = { lx: '洛雪', dl: 'musicdl', native: '原生' }
/**
 * 「范围」两个选项（仅看有损 / 全部曲库）。
 *
 * ⚠️ 三个字段都不能省，2026-10-29 的「全部曲库只有一个灰色组件」就是这么来的：
 *  · `icon` —— SegmentedControl 的自适应降级到最后一档是「只留图标」，
 *    本页这两个选项原来**没有图标**，一旦降级，渲染出来的就是两个**空按钮**
 *    （实测各 16×12px、`<!--v-if-->`、`px-2` 无文字），也就是用户看到的"一坨灰色"；
 *  · `short` —— 中等宽度时用短名，降级也不会变成空白；
 *  · `desc`  —— 变成按钮的 title 气泡。
 * 另外组件是按 `boxEl.clientWidth` 判「放不放得下」的，而它在本页控制行里是个
 * **宽度由内容撑开**的 flex 子项：一旦渲染成纯图标就收缩成图标宽，下次继续判
 * 「放不下」→ 自锁在图标档（组件注释里警告过的那个坑）。所以调用处给了一个固定
 * 基准宽度（模板上的 `basis-[11rem]`），让它拿到真实可用宽度、稳定停在「图标+全名」。
 */
const qModeSegments = [
  { id: 'lossy', name: '仅看有损', short: '有损', icon: FileAudio, desc: '只扫有损文件 —— 无损的没什么可换的，扫它纯属浪费外呼' },
  { id: 'all', name: '全部曲库', short: '全部', icon: Library, desc: '整库都扫（含无损）：慢一些，但不会漏掉无损目录里的低码率文件' },
]
const qMode = ref('lossy')
const qLimit = ref(clampQLimit(Number(loadPref('quality_limit', 200))))
const qScan = ref(null)      // 最近一次 scan 返回的 data（state/total/scanned/entries…）
const qStopped = ref(false)  // true = 已点过「停止」；2026-10-27 起这个动作会真的停后端
const qStopBusy = ref(false) // 「停止」请求在途（防连点 + 按钮禁用）
const qErrorText = ref('')   // 请求层失败（接口没上线/断网）的横幅，别白屏也别抛错
const qSelected = ref(new Set())
// 行内「用这个音源替换」的选择：path → 渠道 id（'lx' | 'dl' | 'native'）。
// 2026-10-29 用户原话：「扫描出来的歌曲，右边除了可以看到来源再增加一个点击选择的功能，
// 意思是这首歌用这个音源替换。」——渠道格从只读展示改成按钮，点了就记在这里。
// 只有**显式指定过**的 path 才有条目：没指定 = 用后端的老口径（该首各渠道最高档位）。
const qPick = ref({})
const qBusy = ref(false)     // 替换批次进行中
let qTimer = null
// 替换 / 下载进度（2026-10-24 用户原话：「用户选择歌曲并替换后，在下方增加正在替换或者
// 正在下载的进度条」）：
//  - qReplacing 只在真有批次在跑时为 true，收尾立刻收起，没进行中时不占位；
//  - qReplaceDone 是**已经回来**的条数，qReplaceInFlight 是这一批刚发出去、还没回来的条数；
//    进度条的分子是两者之和（见 qReplaceStep），否则一批没回来之前条子一动不动。
const qReplacing = ref(false)
const qReplaceTotal = ref(0)
const qReplaceDone = ref(0)
const qReplaceInFlight = ref(0)
const qReplaceCurrent = ref('')  // 这一批在换的那首歌（取该批第一条的歌名 / 文件名）
// 上一轮没换成的 path（2026-10-27）：收尾时摆成「重试这些」的入口。
// 以前失败的行只是留在表格里带 rowError，用户想再试一次得自己一条条把勾找回来。
const qRetryPaths = ref([])

// 一批只发 5 条：契约上限是 50（见 api/client.js 的 QualityAPI.replace 注释），
// 但 50 条一批要等好几分钟才回来一次 —— 用户要的是「看得见在动」的进度，
// 所以这里小批串行：每十几秒推进一次，也不会把后端的外网请求打满。
const Q_REPLACE_BATCH = 5

function clampQLimit(n) {
  const v = Math.floor(Number(n))
  if (!Number.isFinite(v) || v < 1) return 200
  return Math.min(500, v)
}

const qState = computed(() => qScan.value?.state || 'idle')
const qRunning = computed(() => qState.value === 'running')
const qPercent = computed(() => {
  const t = Number(qScan.value?.total) || 0
  if (!t) return 0
  return Math.min(100, Math.round(((Number(qScan.value?.scanned) || 0) * 100) / t))
})
// 替换进度条的分子 = 已回来的条数 + 这一批刚发出去还在飞的条数。
// 只算 done 的话，一批（5 条，走一次外网）没回来之前条子就是死的 —— 用户会觉得卡住了；
// 把在飞的一批算进来，批一发出去进度就前进一格（2026-10-24：「进度条要真的动」）。
const qReplaceStep = computed(() => Math.min(qReplaceTotal.value, qReplaceDone.value + qReplaceInFlight.value))
const qReplacePercent = computed(() => {
  const total = qReplaceTotal.value
  if (!total) return 0
  const pct = Math.round((qReplaceStep.value * 100) / total)
  // 替换中最多画到 99%：最后一批是「发出去就算进进度」的，若直接给到 100%，
  // 用户会看到「100% 但还在等」—— 收尾一旦完成这条就整体收起，不需要 100% 这个态。
  return qReplacing.value ? Math.min(99, pct) : Math.min(100, pct)
})
const qSelectedCount = computed(() => qSelected.value.size)
// 3.10：还没落盘的勾选数，>0 时离开页面会拦一下（原来统计的是整理页三段之和，
// 独立成页后本页只有音质这一种勾选，所以就是 qSelectedCount）。
const pendingSelectionCount = computed(() => qSelectedCount.value)
// 「只看能替换的」筛选（2026-10-27）：一库几万首时表格里绝大多数是
// 「已经是最高音质、没事可做」的行，用户真正要处理的那几十条被淹在里面。
const qOnlyUpgradeable = ref(false)
function setQOnlyUpgradeable(v) {
  if (v === qOnlyUpgradeable.value) return
  qOnlyUpgradeable.value = v
}
const qRowsAll = computed(() => {
  const es = qScan.value?.entries || []
  // 扫完且一条都不可替换（也还没替换过任何东西）→ 让位给空态文案，不刷一屏灰勾
  if (qScan.value?.state === 'done' && !es.some((e) => e.upgradeable || e.replaced)) return []
  return es
})
const qRows = computed(() => {
  if (!qOnlyUpgradeable.value) return qRowsAll.value
  // 失败的行（rowError）一定留着 —— 被筛掉的话用户根本找不到失败原因
  return qRowsAll.value.filter((e) => e.upgradeable || e.rowError)
})
/** 被筛选隐藏掉的行数（提示用，免得用户以为扫描结果少了） */
const qHiddenRowCount = computed(() => (qOnlyUpgradeable.value ? qRowsAll.value.length - qRows.value.length : 0))

function qModeLabel(m) {
  return m === 'all' ? '全部曲库' : '仅看有损'
}
/**
 * 「范围换了但结果还是老的」提示（2026-10-29）。
 *
 * 后端的 scan 是独立任务：换 qMode 只影响**下一次** start，已经在表里的 entries
 * 还是上一个范围扫出来的。一句提示能挡掉「我点了全部曲库，怎么还是只有有损的那些」。
 */
const qModeHint = computed(() => {
  if (qRunning.value) return ''
  const es = qScan.value?.entries || []
  if (!es.length) return ''
  const scanned = qScan.value?.mode || ''
  if (!scanned || scanned === qMode.value) return ''
  return `下面这批结果是按「${qModeLabel(scanned)}」扫的 —— 换了范围后要再按一次「重新扫描」，才会按「${qModeLabel(qMode.value)}」重扫`
})
/**
 * 音质扫描按钮的文案（六-2）。
 *
 * 原来只有「开始扫描 / 扫描中…」两态，但后端 `StartQualityScan`
 * （backend/pkg/intercept/quality.go:266）**每次都从头扫**，不清空旧结果就直接覆盖：
 * 已经扫出 30 条可替换、用户想再扫一遍，点下去会把那 30 条连同用户勾好的选择一起冲掉。
 * 所以按「有没有旧结果」分成两种文案，并在 title 里写清会丢什么。
 */
const qScanBtnText = computed(() => {
  if (qRunning.value) return '扫描中…'
  if (qBusy.value) return '提交中…'
  const es = qScan.value?.entries || []
  return es.length ? '重新扫描' : '开始扫描'
})
const qScanBtnTitle = computed(() => {
  if (qRunning.value) return '扫描进行中 —— 用右边的停止按钮可以真的让后端停下'
  const es = qScan.value?.entries || []
  if (!es.length) return '让三条渠道逐条对比曲库能拿到的最高音质'
  return `会从头上重新扫一遍，现在这 ${es.length} 条结果和已勾的选择都会被清掉；只想保留现状就不要再点`
})

const qStatusText = computed(() => {
  const parts = []
  if (qStopped.value && qState.value !== 'running') {
    // 2026-10-27：停止现在会真的通知后端（见 stopQualityScan），文案跟着改诚实
    parts.push('已停止 —— 后端也停了，已探到的结果保留着，可以先把这批换掉，之后再重新扫')
  } else if (qState.value === 'running') {
    parts.push('扫描中…')
  } else if (qState.value === 'done') {
    parts.push(`扫描完成 —— 共扫 ${qScan.value?.scanned ?? 0} 条，可替换 ${qScan.value?.upgradeable ?? 0} 条`)
  } else if (qState.value === 'failed') {
    parts.push('扫描失败')
  }
  if (qScan.value?.truncated) parts.push('已达数量上限，曲库后面部分没扫')
  return parts.join(' · ')
})

// 渠道三格：洛雪 / musicdl / 原生，拿到显示档位、拿不到显示灰的「拿不到」。
//
// 2026-10-29 起这三格是**可点选的按钮**（原来是只读 div）：
//   · pickable = 该渠道真的拿得到（tier 非空）**且**这行本来就值得换（upgradeable、未替换）
//     ——「已是最优」的行点它没有任何意义，不如禁用掉，别让用户以为能选；
//   · picked  = 用户已经指定「这首歌用这个渠道替换」（见 pickQChannel）。
function qChannelCells(e) {
  const picked = qPickOf(e)
  const canPick = !!e.upgradeable && !e.replaced
  return DEFAULT_AUTO_DOWNLOAD_CHANNELS.map((ch) => {
    const hit = (e.channels || []).find((c) => c.channel === ch)
    const label = Q_CHANNEL_LABELS[ch] || ch
    const tier = hit && hit.tier ? hit.tier : ''
    const from = hit && hit.platform ? `（来自${hit.platform}）` : ''
    const isPicked = picked === ch
    let hint
    if (!tier) hint = `${label}：这条在该渠道拿不到`
    else if (!canPick) hint = `${label}：${tier}${from} —— 这条不需要替换或已经替换过了`
    else hint = `${label}：${tier}${from} · 点一下＝这首歌用${label}替换${isPicked ? '（再点一下取消）' : ''}`
    return { ch, label, tier, picked: isPicked, pickable: !!tier && canPick, hint }
  })
}

/** 这一行当前指定的渠道（没指定返回空串） */
function qPickOf(e) {
  return (e && qPick.value[e.path]) || ''
}

/**
 * 点行里的渠道格＝「这首歌用这个音源替换」（2026-10-29 用户需求）。
 *
 * 交互口径：
 *  · 点一个渠道 → 记下选择 + 把这行计入「已选」（用户说的「选中后计入已选」）；
 *  · 再点同一个 → 撤销：清掉指定并把它移出已选（点错了必须有回头路）；
 *  · 点另一个渠道 → 换指定，仍留在已选里；
 *  · 行里的复选框仍然是「选不选这首」的总开关：勾掉时一并清掉指定（见 toggleQSelect），
 *    免得出现「没勾选却还留着指定渠道」这种看不出所以然的状态。
 */
function pickQChannel(e, ch) {
  if (!e || !e.upgradeable || e.replaced) return
  const hit = (e.channels || []).find((c) => c.channel === ch)
  if (!hit || !hit.tier) return
  const picks = { ...qPick.value }
  const sel = new Set(qSelected.value)
  if (picks[e.path] === ch) {
    delete picks[e.path]
    sel.delete(e.path)
  } else {
    picks[e.path] = ch
    sel.add(e.path)
  }
  qPick.value = picks
  qSelected.value = sel
}

// 最近的替换记录（最新在前）。后端已按时间倒序并截断到 100 条，
// 这里只取前 8 条摆出来 —— 它是「刚发生了什么」的凭据，不是存档页。
const qRecords = computed(() => {
  const list = Array.isArray(qScan.value?.records) ? qScan.value.records : []
  return list.slice(0, 8).map((r) => ({
    ...r,
    when: r?.at ? formatRecordTime(r.at) : '',
    label: (r?.title || baseName(r?.path || '')) + (r?.artist ? ` · ${r.artist}` : ''),
    tierText: r?.ok
      ? `${r.from || '?'} → ${r.to || '?'}`
      : (r?.note || '没换成'),
  }))
})

/** 记录里有几条是后台自动补强做的（`by === 'upgrade'`，见 pkg/intercept/quality.go 的 QualityRecord.By）。 */
const qAutoCount = computed(() => {
  const list = Array.isArray(qScan.value?.records) ? qScan.value.records : []
  return list.filter((r) => r && r.by === 'upgrade').length
})

// P2-10：原来这里手写 pad 出「10-27 18:39」（没有年份，跨年就歧义），改用统一的 fmtWhen
const formatRecordTime = fmtWhen

function normalizeScan(j) {
  const d = j && typeof j === 'object' ? j : {}
  return {
    state: d.state || 'running',
    mode: d.mode || qMode.value,
    total: Number(d.total) || 0,
    scanned: Number(d.scanned) || 0,
    truncated: !!d.truncated,
    upgradeable: Number(d.upgradeable) || 0,
    error: d.error || '',
    entries: Array.isArray(d.entries) ? d.entries : [],
    // 替换记录（后端落盘的最近 100 条，重启还在）——
    // 替换是原地覆盖，不看这份记录用户根本无从知道「昨天到底换过什么」。
    records: Array.isArray(d.records) ? d.records : [],
  }
}

async function startQualityScan() {
  if (qRunning.value || qBusy.value) return
  const limit = clampQLimit(qLimit.value)
  qLimit.value = limit
  savePref('quality_limit', limit)
  qErrorText.value = ''
  try {
    const res = await QualityAPI.scan('start', qMode.value, limit)
    if (res.code === 200) {
      qStopped.value = false
      qScan.value = normalizeScan(res.data)
      if (qRunning.value) startQualityPolling()
      else stopQualityPolling()
      pruneQSelection()
    } else {
      qErrorText.value = apiError(res, '发起音质扫描')
    }
  } catch (e) {
    // 后端接口还没上线 / 网络失败：横幅说清即可，不白屏、不抛未捕获错误
    qErrorText.value = failText('发起音质扫描', e)
  }
}

// 进页先读一次状态：替换记录是**历史**，不该只有「刚扫过」才看得见
// （用户 m04229 的原话就是「替换音质，怎么看不到记录」）。
async function loadQualityStatus() {
  try {
    const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
    if (res.code === 200 && res.data) qScan.value = normalizeScan(res.data)
  } catch (_) {
    // 读不到就当没有历史：不该因为一条记录挡住整页
  }
}

function startQualityPolling() {
  stopQualityPolling()
  qTimer = setInterval(async () => {
    try {
      const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
      if (res.code !== 200 || !res.data) return // 单次查询失败不打断轮询（对齐补全轮询口径）
      // 替换进行中不拿 status 覆盖表格：旧扫描快照会把刚移出队列的行放回来，
      // 用户会看到「已经换完的歌又出现在待替换列表里」（2026-10-24 改）
      if (qBusy.value) return
      qScan.value = normalizeScan(res.data)
      if (qState.value === 'done' || qState.value === 'failed') {
        stopQualityPolling()
        pruneQSelection()
      }
    } catch (_) {
      // 轮询失败静默：后端重启/网络抖一下不该刷屏
    }
  }, 2000)
}

function stopQualityPolling() {
  if (qTimer) {
    clearInterval(qTimer)
    qTimer = null
  }
}

/**
 * 停止扫描（2.4 / 六-2，2026-10-27 修）。
 *
 * 原来这里**只停本地轮询**，标题还诚实地写着「后端扫描可能仍在继续」——
 * 于是用户点了停止、以为停了，后端其实还在逐首打三条渠道（一次几百条能跑好几分钟），
 * 想取消就只能等它扫完。后端 /api/quality/scan 早就支持 `action:"stop"`
 * （intercept/quality.go:328 StopQualityScan，会 close(stop) 取消在途探测，
 * 已探到的结果保留），所以这里补上真正的取消。
 */
async function stopQualityScan() {
  stopQualityPolling()
  qStopped.value = true
  qStopBusy.value = true
  try {
    const res = await QualityAPI.scan('stop')
    // 后端回的是停止后的状态（已探到的保留在 entries 里）——直接拿来刷新界面
    if (res?.data) qScan.value = res.data
    flash('已停止扫描，已探到的结果保留着')
  } catch (e) {
    // 停不下来也要说清楚，别让用户以为停了
    flash(failText('停止扫描', e) + ' —— 后端可能还在扫，等一会儿再看进度', true)
  } finally {
    qStopBusy.value = false
  }
}

function setQMode(v) {
  if (v === qMode.value) return
  if (qRunning.value) return flash('扫描进行中 —— 先按停止再换范围', true)
  qMode.value = v
}

function toggleQSelect(e) {
  if (!e.upgradeable || e.replaced) return
  const sel = new Set(qSelected.value)
  const picks = { ...qPick.value }
  if (sel.has(e.path)) {
    sel.delete(e.path)
    // 勾掉这行 = 这首不换了，行内指定的渠道一起清掉（否则会留下「没勾选却指定了渠道」的悬空状态）
    delete picks[e.path]
  } else {
    sel.add(e.path)
  }
  qSelected.value = sel
  qPick.value = picks
}

function qClearSelection() {
  qSelected.value = new Set()
  qPick.value = {}
}

// 新一轮扫描/替换之后，把已不存在或已不可替换的勾选剔掉
function pruneQSelection() {
  const valid = new Set((qScan.value?.entries || []).filter((e) => e.upgradeable && !e.replaced).map((e) => e.path))
  let changed = false
  const sel = new Set(qSelected.value)
  for (const path of sel) {
    if (!valid.has(path)) {
      sel.delete(path)
      changed = true
    }
  }
  if (changed) qSelected.value = sel
  // 行内指定的渠道同步剔掉：对已经不在表里 / 已经不可替换的行留着指定毫无意义，
  // 而且会在下次替换时把渠道发给后端（见 replaceWithBest 的 picks 快照）。
  const picks = { ...qPick.value }
  let pickChanged = false
  for (const path of Object.keys(picks)) {
    if (!valid.has(path)) {
      delete picks[path]
      pickChanged = true
    }
  }
  if (pickChanged) qPick.value = picks
}

function onQAction(key) {
  if (key === 'replace') replaceWithBest()
}

/**
 * 替换已选（2026-10-29 起带行内渠道指定）。
 *
 * picks = { path: 'lx'|'dl'|'native' }，来自行内渠道格的点击（见 pickQChannel）。
 * ⚠️ 后端 /api/quality/replace 今天只解 `paths`（intercept.ReplaceQuality(ctx, paths)，
 *    每条一律用 entry.best），`picks` 会被静默忽略 —— 这里照样发上去，理由：
 *    ① 后端补上按渠道取音时，前端不用再改一次；
 *    ② 本页立刻就能用它做标记（行尾「用洛雪替换」）。
 *    但**不许**在界面上让用户以为点了某个渠道就一定会从那个渠道取文件 —— 所以表格上方
 *    有一句明说「后端目前仍按最高档位取音」，等后端支持后再把那句删掉。
 */
async function replaceWithBest(pathsArg, picksArg) {
  if (qBusy.value || qReplacing.value) return
  const paths = Array.isArray(pathsArg) && pathsArg.length ? [...pathsArg] : [...qSelected.value]
  if (!paths.length) return
  // 快照一次：提交过程中用户还可能继续点别的行，不能让中途变化的 qPick 漏进这次请求
  const picks = picksArg && typeof picksArg === 'object' ? { ...picksArg } : { ...qPick.value }
  qBusy.value = true
  qErrorText.value = ''
  let doneCount = 0
  let failCount = 0
  // 成功的 path 收在这里：收尾时一次性从扫描队列里移除
  // （用户 2026-10-24 原话：「移除已完成歌曲的扫描队列」）。
  const donePaths = new Set()
  // 失败的 path 收在这里：收尾时摆成「重试这些」的入口（2026-10-27 加）。
  const failedPaths = new Set()
  const batchErrors = []
  qReplaceTotal.value = paths.length
  qReplaceDone.value = 0
  qReplaceInFlight.value = 0
  qReplaceCurrent.value = ''
  qReplacing.value = true
  qRetryPaths.value = []
  try {
    // 契约：一次最多 50 条 —— 这里刻意只发 5 条一批（见 Q_REPLACE_BATCH），顺序发、一批一落。
    for (let i = 0; i < paths.length; i += Q_REPLACE_BATCH) {
      const batch = paths.slice(i, i + Q_REPLACE_BATCH)
      // 批一发出去就把「在等哪首」摆到进度条上：5 条也要各走一次外网，用户得知道进度在哪
      qReplaceCurrent.value = qBatchLabel(batch[0])
      qReplaceInFlight.value = batch.length
      // ⚠️ try 必须在**循环内**（2026-10-27 修）。
      // 原来 catch 在循环外：第 3 批发一次抛错（断网 / 超时），第 4 批到第 N 批根本不再发，
      // 用户看到进度条停在原地、后面几十首一动不动，而那些勾选还留在条上以为在排队。
      try {
        // 行内指定的渠道：只把这一批里显式指定过的放进 picks（后端今天会忽略，见函数头注释）
        const batchPicks = {}
        for (const p of batch) if (picks[p]) batchPicks[p] = picks[p]
        const res = await QualityAPI.replace(batch, batchPicks)
        if (res.code !== 200) {
          // 整批没被受理（不是单条失败）：这一批全算失败，但**继续发后面的批次**
          const msg = apiError(res, '替换音质')
          batchErrors.push(msg)
          qErrorText.value = msg
          for (const p of batch) failedPaths.add(p)
          failCount += batch.length
          qReplaceDone.value += batch.length
          qReplaceInFlight.value = 0
          continue
        }
        const d = res.data || {}
        const doneMap = new Map()
        const failMap = new Map()
        for (const it of d.done || []) {
          // ok:false 配的是 `note`（例如「拿到的是同档位文件，没有覆盖」）——
          // 与 failed[] 那种真失败不同，但要照实说，不能显示成成功。
          if (it && it.ok === false) failMap.set(it.path, (it && (it.note || it.error)) || '这条没有替换成功')
          else if (it) doneMap.set(it.path, it)
        }
        for (const it of d.failed || []) failMap.set(it.path, it.error || '替换失败')
        // 后端偶尔会少回几条（既不在 done 也不在 failed）—— 那些不能默默消失，
        // 否则「勾了 5 条，回来只有 3 条的结果」，用户无从判断另外 2 条怎么了。
        for (const p of batch) {
          if (!doneMap.has(p) && !failMap.has(p)) failMap.set(p, '后端没有返回这条的结果，可能没换成')
        }
        doneCount += doneMap.size
        failCount += failMap.size
        for (const path of doneMap.keys()) donePaths.add(path)
        for (const path of failMap.keys()) failedPaths.add(path)
        applyQReplace(doneMap, failMap)
        // 这批回来了：把在飞数换成真实完成数（进度条分子不变，但自此算「已落盘」）
        qReplaceDone.value += batch.length
        qReplaceInFlight.value = 0
      } catch (e) {
        // 单批抛错：只记这一批失败，后面的批次继续发（这才是用户要的「别卡死」）
        const msg = failText('替换音质', e)
        batchErrors.push(msg)
        for (const p of batch) failedPaths.add(p)
        failCount += batch.length
        qReplaceDone.value += batch.length
        qReplaceInFlight.value = 0
      }
    }
    if (doneCount) flash(`已替换 ${doneCount} 首`)
    if (failCount) flash(`${failCount} 条没换成${batchErrors.length ? `：${batchErrors[0]}` : '，原因写在对应行'}`, true)
  } finally {
    // 收尾（2026-10-24 用户原话：「替换完成后，自动更新当前页面的最近记录，
    // 移除已完成歌曲的扫描队列」）：
    // 1) 只取一次 status 把 records 换掉 —— 不整表重扫，表格/滚动位置/其他字段都保持原样，
    //    这样刚换完的那几首立刻出现在「最近替换」里；
    // 2) 成功的行从扫描队列里移除（不再标 replaced:true 留在表里），失败行留着带 rowError；
    // 3) 失败的行不静默留在勾选里 —— 它们进 qRetryPaths，用户点一下就能重试
    //    （原地不动的勾选会让人以为「已经处理过了」）。
    await refreshQualityRecordsOnly()
    dropReplacedFromQueue(donePaths)
    qRetryPaths.value = [...failedPaths]
    if (failedPaths.size) {
      const sel = new Set(qSelected.value)
      for (const p of failedPaths) sel.delete(p)
      qSelected.value = sel
    }
    qReplaceInFlight.value = 0
    qReplacing.value = false
    qBusy.value = false
  }
}

/** 重试上一轮没换成的那些（2026-10-27）：把失败集合塞回勾选再跑一遍。 */
function retryFailedReplace() {
  const paths = qRetryPaths.value
  if (!paths.length || qBusy.value || qReplacing.value) return
  replaceWithBest(paths)
}

// 进度条上「当前这首」的可读名：表里能查到时用歌名，查不到就退回文件名。
function qBatchLabel(path) {
  const hit = (qScan.value?.entries || []).find((e) => e.path === path)
  return (hit && hit.title) || baseName(path) || ''
}

/**
 * 只刷新「最近替换」记录，其余字段一概不动（2026-10-24 需求）。
 *
 * 为什么不是直接再调一次 loadQualityStatus()：那会把 entries / state / scanned /
 * upgradeable 全量覆盖 —— 用户刚替换完，表格正被他自己看着，整表重扫一次等于把
 * 滚动位置和失败行都洗掉。这里只把 `records` 写回去，刚替换完的记录就会出现在
 * 「最近替换」里，其余字段保持调用前的样子。
 */
async function refreshQualityRecordsOnly() {
  try {
    const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
    if (res.code !== 200 || !res.data) return
    const records = Array.isArray(res.data.records) ? res.data.records : null
    if (!records) return
    if (qScan.value) qScan.value.records = records
    else qScan.value = normalizeScan(res.data) // 极端情况（表都没了）才整份采纳
  } catch (_) {
    // 记录刷不出来不影响「替换成功」这个结论：flash 已经报过条数了，别叠第二块错误
  }
}

/**
 * 把成功的行移出扫描队列（2026-10-24 用户原话：「移除已完成歌曲的扫描队列」）。
 *
 * 换过的歌已经没有「可替换」可言 —— 留着只会让用户以为还没换。
 * 同步把 upgradeable 减掉真正移除的条数（不能减成负数），勾选集合也剔除掉这些 path。
 */
function dropReplacedFromQueue(donePaths) {
  const j = qScan.value
  if (!j || !donePaths.size) return
  const before = (j.entries || []).length
  j.entries = (j.entries || []).filter((x) => !donePaths.has(x.path))
  const removed = before - j.entries.length
  if (removed > 0) j.upgradeable = Math.max(0, (Number(j.upgradeable) || 0) - removed)
  if (qSelected.value.size) {
    const sel = new Set(qSelected.value)
    for (const path of donePaths) sel.delete(path)
    qSelected.value = sel
  }
  // 换成功的行连同行内渠道指定一起清掉（失败的留着，重试时还要按用户指定的渠道发）
  const picks = { ...qPick.value }
  let pickChanged = false
  for (const path of donePaths) {
    if (picks[path]) {
      delete picks[path]
      pickChanged = true
    }
  }
  if (pickChanged) qPick.value = picks
}

// 只改对应行的数据（done → replaced + 新档位；failed → rowError），
// 行按 path 做 key，Vue 就地更新，不整表重绘 —— 用户视角连续。
function applyQReplace(doneMap, failMap) {
  const j = qScan.value
  if (!j) return
  j.entries = (j.entries || []).map((x) => {
    const d = doneMap.get(x.path)
    if (d) return { ...x, replaced: true, upgradeable: false, newTier: d.tier || '', newChannel: d.channel || '', rowError: '' }
    const err = failMap.get(x.path)
    if (err !== undefined) return { ...x, rowError: err }
    return x
  })
  const sel = new Set(qSelected.value)
  for (const path of [...sel]) {
    if (doneMap.has(path) || failMap.has(path)) sel.delete(path)
  }
  qSelected.value = sel
}


onMounted(() => {
  // 替换记录是**历史**，进页就该看得到，不该只有「刚扫过」才有
  loadQualityStatus()
  // 3.10：有未落盘的勾选时离开页面拦一下
  onBeforeUnload = (e) => {
    if (pendingSelectionCount.value <= 0) return
    e.preventDefault()
    e.returnValue = ''
  }
  window.addEventListener('beforeunload', onBeforeUnload)
})

onUnmounted(() => {
  stopQualityPolling()
  if (onBeforeUnload) window.removeEventListener('beforeunload', onBeforeUnload)
})
</script>

<style scoped>
/*
  ── 页内「范围」切换（仅看有损 / 全部曲库）的选中态 ──

  2026-10-29 修过一次：当时 SegmentedControl 的选中态只给图标上色，
  本页两段又没图标，于是「一坨灰色」，就用 :deep() 补了个白底胶囊。
  2026-10-30 SegmentedControl 整体改成飞牛的**文字 tab**（深色加粗 + 蓝色下划线），
  选中态本来就自带动效与区分，本页这层覆盖反而会盖掉蓝色短线 —— 整段删掉。
*/

/*
  ── 行内渠道格：可点选（见脚本里的 pickQChannel）──
  能点的时候给 cursor-pointer + hover 描边（Tailwind 在模板里），
  选中态（选中了「用这个音源替换」）用强调色描边 + 浅底 + 档位字加粗。
*/
.q-cell {
  background: transparent;
  transition: background-color 0.15s ease, border-color 0.15s ease;
}
.q-cell:disabled {
  opacity: 0.75;
}
.q-cell-picked {
  background: var(--ys-accent-soft);
}
.q-cell-picked .q-cell-tier {
  color: var(--ys-accent);
  font-weight: 600;
}
</style>
