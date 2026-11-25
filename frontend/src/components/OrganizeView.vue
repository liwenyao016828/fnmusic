<template>
  <!--
    文件夹整理 =「补全整理 → 归类」这一段的实现（2026-11-20）。

    用户口径（原话）：目录层级按「歌手 / 专辑 / 曲目」组织，同一曲目的音频（.flac）、
    图片（.jpg）、歌词（.lrc）保持相同文件名并放在同一专辑目录下，而**结构样式
    （目录层级、命名格式）由用户自己改**。

    所以这一页的顺序是「先说清楚要搬成什么样 → 再看搬成什么样 → 最后才动手」：
      · 样式（目录模板 / 文件名模板 / 是否补序号）在顶部，可改；
      · 「预演」只读不动盘，逐条列出每首歌会去哪、哪些撞车；
      · 「开始整理」是两步确认 —— 移动文件不可逆，一次误点就是整库换位置；
      · 搬完留撤销记录，「撤销上次整理」把文件逆序搬回原位。

    它不知道自己挂在整理页的第几段（不读 segmentOf），谁挂它都一样能用；
    曲库目录与顶部那颗目录胶囊同源（prefs 的 nas_dir，换目录会派发 ys:dir-changed）。
  -->
  <PageShell>
    <SectionCard
      :icon="FolderTree"
      tone="brand"
      title="文件夹整理"
      sub="按「歌手 / 专辑 / 曲目」把歌搬进文件夹；同一首的音频 / 图片 / 歌词同名同目录"
    >
      <template #actions>
        <!-- 撤销放在标题右侧：它是「出事时的退路」，永远可见，但不可用时是灰的 -->
        <button
          type="button"
          class="ys-btn ys-btn-sm ys-press"
          :disabled="busy || !journal.available"
          :title="journal.available ? undoTitle : '还没有可撤销的整理记录'"
          @click="armUndo"
        >{{ undoArmed ? '确认撤销' : '撤销上次整理' }}</button>
      </template>

      <!--
        ① 选样式。每张卡片直接摆一条**拼好的成品路径**（周杰伦/范特西/01 双截棍.flac）——
        用户 2026-11-20 原话「看不懂这些 {artist}/{album}」，所以模板串只留在下面的自定义区里。
      -->
      <div class="mt-1 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        <button
          v-for="p in presetCards"
          :key="p.id"
          type="button"
          class="ys-press flex flex-col items-start gap-1 rounded-[10px] border px-2.5 py-2 text-left"
          :class="cardClass(p.id)"
          @click="choosePreset(p.id)"
        >
          <span class="flex w-full items-center gap-1.5">
            <span class="truncate text-[12px] font-medium">{{ p.name }}</span>
            <span v-if="p.id === presetId" class="ml-auto shrink-0 text-[10px] text-[var(--ys-accent)]">当前</span>
          </span>
          <span class="w-full truncate font-mono text-[10.5px] text-[var(--ys-text-aux)]" :title="p.example">{{ p.example }}</span>
        </button>
      </div>

      <!--
        ② 效果预览。一棵真的目录树，切换样式时逐行浮入（同一条链上的目录 + 音频 + 同名封面/歌词），
        让人一眼看懂「同一首歌的三件套会落在哪儿」——这比任何文字解释都省事。
      -->
      <div class="mt-3 overflow-hidden rounded-[10px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)]">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-[var(--ys-border)] px-2.5 py-1.5">
          <FolderTree class="h-3.5 w-3.5 shrink-0 text-[var(--ys-accent)]" :stroke-width="1.9" />
          <span class="text-[11px] font-medium text-[var(--ys-text)]">搬完就是这样</span>
          <span class="truncate text-[10.5px] text-[var(--ys-text-aux)]">
            拿示例「{{ sampleLabel }}」算的；真歌按各自的歌手 / 专辑 / 曲序落位
          </span>
        </div>
        <TransitionGroup tag="ul" name="ys-tree" :key="treeKey" class="px-3 py-2 font-mono text-[11px] leading-[1.65]">
          <li v-for="(row, i) in treeRows" :key="row.key">
            <span
              class="ys-tree-row block whitespace-pre"
              :class="row.audio ? 'font-medium text-[var(--ys-accent)]' : 'text-[var(--ys-text)]'"
              :style="{ animationDelay: `${i * 28}ms` }"
              :title="row.title"
            >{{ row.line }}</span>
          </li>
        </TransitionGroup>
      </div>

      <!--
        ③ 自定义收进抽屉：不想看占位符的人一辈子不用展开它；
        但想看的人（想把年份、碟号也拼进路径）需要看到完整说明，所以说明文案仍然由接口下发。
      -->
      <details :open="customOpen" class="mt-3 rounded-[10px] border border-[var(--ys-border)] px-2.5 py-2" @toggle="onDetailsToggle">
        <summary class="cursor-pointer text-[11px] text-[var(--ys-text-muted)]">自己改文件夹层级和文件名</summary>

        <div class="mt-2 grid gap-2 sm:grid-cols-2">
          <label class="block">
            <span class="text-[11px] text-[var(--ys-text-muted)]">文件夹层级</span>
            <input
              v-model="dirTpl"
              type="text"
              class="mt-1 w-full rounded-[8px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] focus:border-[var(--ys-accent)] focus:bg-white focus:outline-none"
              placeholder="{artist}/{album}"
              spellcheck="false"
              @input="onTemplateEdit"
            />
          </label>
          <label class="block">
            <span class="text-[11px] text-[var(--ys-text-muted)]">文件名（不用写扩展名）</span>
            <input
              v-model="fileTpl"
              type="text"
              class="mt-1 w-full rounded-[8px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] focus:border-[var(--ys-accent)] focus:bg-white focus:outline-none"
              placeholder="{track:02d} {title}"
              spellcheck="false"
              @input="onTemplateEdit"
            />
          </label>
        </div>

        <!-- 占位符说明随接口下发（后端 pkg/organize 是唯一权威），带示例值，不再只有符号 -->
        <ul class="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[10.5px] text-[var(--ys-text-aux)]">
          <li v-for="p in placeholders" :key="p.key">
            <span class="font-mono text-[var(--ys-text-muted)]">{{ p.key }}</span>
            <span class="mx-0.5">→</span>
            <span>{{ p.desc }}</span>
            <span class="font-mono text-[var(--ys-text-muted)]">（{{ p.example }}）</span>
          </li>
        </ul>
        <p class="mt-1.5 text-[10.5px] text-[var(--ys-text-aux)]">
          文件夹层级留空 = 全部平铺在曲库根目录下。
        </p>
      </details>

      <label class="mt-2 flex items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
        <input v-model="numberUnknown" type="checkbox" class="h-3.5 w-3.5 accent-[var(--ys-accent)]" @change="onTemplateEdit" />
        给没有曲序的歌按同专辑顺序补序号（没曲序时不留空号）
      </label>

      <!-- 动作行 -->
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="ys-btn ys-btn-primary ys-press"
          :disabled="busy"
          title="只算不搬：算出每首歌会去哪，不动任何文件"
          @click="runPlan"
        >{{ planBusy ? '预演中…' : '预演' }}</button>
        <button
          type="button"
          class="ys-btn ys-btn-sm ys-press"
          :disabled="busy || planStats.move <= 0"
          :title="planStats.move > 0 ? `把 ${planStats.move} 首搬到新位置（两步确认）` : '先点「预演」看看要搬什么'"
          @click="armApply"
        >{{ applyArmed ? `确认搬 ${planStats.move} 首` : `开始整理${planStats.move > 0 ? `（${planStats.move} 首）` : ''}` }}</button>
        <span v-if="applyArmed" class="text-[11px] text-amber-700">再点一次就开始搬；搬完可撤销</span>

        <span class="ml-auto flex min-w-0 items-center gap-1.5 text-[11px] text-[var(--ys-text-aux)]">
          <span class="shrink-0">目录</span>
          <span class="truncate font-mono" :title="shownRoot">{{ shownRoot || '（未选择）' }}</span>
        </span>
      </div>

      <InlineNotice class="mt-2" :text="notice" :tone="noticeTone" />

      <!-- 预演结果：统计全量、明细最多 500 条（后端裁的） -->
      <div v-if="planStats.audio > 0" class="mt-3">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[11px] text-[var(--ys-text-muted)]">
          <span>音频 <b class="ys-num text-[var(--ys-text)]">{{ planStats.audio }}</b> 首</span>
          <span>要搬 <b class="ys-num text-[var(--ys-text)]">{{ planStats.move }}</b></span>
          <span v-if="planStats.same > 0">已就位 <b class="ys-num text-[var(--ys-text)]">{{ planStats.same }}</b></span>
          <span v-if="planStats.conflict > 0" class="text-amber-700">冲突 <b class="ys-num">{{ planStats.conflict }}</b></span>
          <span v-if="planStats.skip > 0" class="text-rose-700">跳过 <b class="ys-num">{{ planStats.skip }}</b></span>
          <span v-if="planStats.sidecars > 0">附带歌词 / 封面 <b class="ys-num text-[var(--ys-text)]">{{ planStats.sidecars }}</b></span>
          <button
            type="button"
            class="ml-auto shrink-0 underline decoration-dotted underline-offset-2"
            @click="showSame = !showSame"
          >{{ showSame ? '只看要搬的' : '显示已就位的' }}</button>
        </div>

        <ul
          v-if="visibleItems.length"
          class="mt-2 max-h-[420px] overflow-y-auto rounded-[8px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-2.5 py-1"
        >
          <li
            v-for="(it, i) in visibleItems"
            :key="it.source + '#' + i"
            class="flex items-start gap-2 border-b border-[var(--ys-border)] py-1.5 last:border-b-0"
          >
            <span
              class="mt-0.5 shrink-0 rounded-[4px] px-1 py-0.5 text-[10px] font-medium"
              :class="badgeClass(it.status)"
            >{{ badgeText(it.status) }}</span>
            <div class="min-w-0 flex-1">
              <div class="truncate font-mono text-[11px] text-[var(--ys-text)]" :title="it.target || it.source">
                {{ relPath(it.target) || baseName(it.source) }}
              </div>
              <div class="truncate text-[10.5px] text-[var(--ys-text-aux)]" :title="it.source">
                <template v-if="it.reason">{{ it.reason }}</template>
                <template v-else>← {{ baseName(it.source) }}</template>
              </div>
            </div>
          </li>
        </ul>
        <p v-if="!visibleItems.length" class="mt-2 text-[10.5px] text-[var(--ys-text-aux)]">
          <template v-if="planStats.same > 0">这个目录已经是这个结构了，没有要搬的；点右侧「显示已就位的」可以逐条核对位置。</template>
          <template v-else>没有需要显示的条目。</template>
        </p>
        <p v-if="planTruncated > 0" class="mt-1 text-[10.5px] text-[var(--ys-text-aux)]">
          另有 {{ planTruncated }} 条没在这里列出（统计是全部的数量）
        </p>
        <p v-for="(w, i) in planWarnings" :key="'w' + i" class="mt-1 text-[10.5px] text-amber-700">{{ w }}</p>
      </div>

      <!-- 还没预演过的空态：一句话说清这页在干什么，别放一堆占位骨架 -->
      <p v-else-if="!planBusy" class="mt-3 text-[11px] leading-relaxed text-[var(--ys-text-aux)]">
        选好样式后点「预演」，这里会逐条列出每首歌会搬到哪儿 —— 预演不会动任何文件。
      </p>

      <!-- 撤销记录：只在真搬过东西之后出现 -->
      <div
        v-if="journal.available"
        class="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[8px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-2.5 py-2 text-[11px] text-[var(--ys-text-muted)]"
      >
        <History class="h-3.5 w-3.5 shrink-0 text-[var(--ys-accent)]" :stroke-width="1.9" />
        <span>
          上次整理：<b class="ys-num text-[var(--ys-text)]">{{ journal.moved }}</b> 首，
          {{ journalWhen }}
        </span>
        <span
          v-if="journal.pending > 1"
          class="text-[var(--ys-text-muted)]"
          >· 还有 {{ journal.pending - 1 }} 轮可以往回撤</span
        >
        <span class="truncate font-mono text-[10.5px]" :title="journal.root">{{ journal.root }}</span>
      </div>
    </SectionCard>

    <SectionCard
      class="mt-3"
      :icon="Activity"
      tone="soft"
      title="音质检测"
      sub="读取文件头、体积与时长，筛出格式或码率明显对不上的文件；不会自动修改文件。"
    >
      <template #actions>
        <span class="text-[11px] text-[var(--ys-text-muted)]">已检查 {{ auditScanned }} / {{ auditTotal }} 首</span>
        <button type="button" class="ys-btn ys-btn-sm" :disabled="auditBusy" @click="scanAudioAudit">
          {{ auditBusy ? '检测中…' : auditScanned ? '重新检测' : '开始检测' }}
        </button>
      </template>
      <p v-if="auditError" class="mt-2 text-xs text-[var(--ys-danger)]">{{ auditError }}</p>
      <div v-else-if="auditBusy && !auditItems.length" class="mt-2 text-xs text-[var(--ys-text-muted)]">正在读取曲库音频信息…</div>
      <div v-else-if="!auditItems.length" class="mt-2 text-xs text-[var(--ys-text-muted)]">检测只读文件头、时长、体积；不代表听感鉴定。识别到可疑文件后可逐首播放、换版本或移入回收站。</div>
      <div v-else class="mt-2 divide-y divide-[var(--ys-border)]">
        <div v-for="song in auditItems" :key="song.path" class="flex items-center gap-2 py-2">
          <button type="button" class="min-w-0 flex-1 text-left" @click="playAuditSong(song)">
            <span class="block truncate text-xs font-medium">{{ song.title || song.name }}</span>
            <span class="block truncate text-[11px] text-[var(--ys-text-muted)]">{{ song.artist || '未知歌手' }} · {{ song.ext || '未知扩展名' }} → {{ song.container || '未知容器' }} · {{ song.sizeText }} · {{ song.durationText }} · 标称 {{ song.nominal_kbps || '未知' }} / 等效 {{ song.effective_kbps ? Math.round(song.effective_kbps) : '未知' }} kbps</span>
            <span class="block truncate text-[11px] text-[var(--ys-danger)]">{{ song.issues.join('；') }}</span>
          </button>
          <button type="button" class="ys-btn ys-btn-sm" :disabled="auditBusy" @click="replaceAuditSong(song)">替换</button>
          <button type="button" class="ys-btn ys-btn-sm" :disabled="auditBusy" @click="deleteAuditSong(song)">删除</button>
        </div>
      </div>
      <p v-if="auditScanned" class="mt-2 text-[11px] text-[var(--ys-text-muted)]">已检查 {{ auditScanned }} / {{ auditTotal }} 首，列出 {{ auditItems.length }} 个异常。码率和容器只能提示异常，不等于听感结论。</p>
      <RepairCandidateSheet v-model="candidateOpen" :item="candidateItem" @replaced="onAuditReplaced" />
    </SectionCard>
  </PageShell>
</template>

<script setup>
/**
 * 文件夹整理（2026-11-20）。
 *
 * 调后端 /api/organize/*（契约见 api/client.js 的 OrganizeAPI 注释、pkg/api/organize.go）：
 *   presets → plan（只读） → apply（真搬 + 落撤销记录） → undo（逆序搬回）
 *
 * 三件事值得单独说明：
 *
 * ① **两步确认，不用 window.confirm**：项目里从 2026-10-27 起就不再用系统弹窗
 *    （iOS PWA / 微信里样式不可控），「开始整理」「撤销」都改成按钮就地变文案。
 *
 * ② **执行前把样式存下来**：用户手改的模板如果不存，刷新一次就回到默认 ——
 *    搬文件是不可逆动作，「按 A 样式预演、按 B 样式执行」是最难查的一类事故。
 *    保存走 /api/organize/template 而不是 POST /config，因为**空目录模板（平铺）
 *    是合法值**，而 /config 的合并规则把空串当「没传」。
 *
 * ③ **换目录要作废旧方案**：顶部目录胶囊换完会派发 ys:dir-changed，这里收到就清掉
 *    预演结果 —— 留着上一份方案的统计只会让人对着 A 目录的数字去按 B 目录的执行键。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import PageShell from './PageShell.vue'
import SectionCard from './SectionCard.vue'
import InlineNotice from './InlineNotice.vue'
import { Activity, FolderTree, History } from 'lucide-vue-next'
import { OrganizeAPI, RepairAPI, NasAPI } from '../api/client'
import RepairCandidateSheet from './RepairCandidateSheet.vue'
import { notifyTidyProgress } from '../services/tidyProgress'
import { fail as failText, apiError } from '../services/userMsg'
import { ok as notifyOk, fail as notifyFail } from '../services/feedback'
import { loadPref } from '../services/prefs'
import { fmtWhen } from '../services/libraryView'
import { deleteOutcome } from '../services/nasDelete'

const emit = defineEmits(['play'])
const busy = ref(false)
const planBusy = ref(false)
const auditBusy = ref(false)
const auditError = ref('')
const auditScanned = ref(0)
const auditTotal = ref(0)
const auditItems = ref([])
const candidateOpen = ref(false)
const candidateItem = ref(null)


/** 内置样式（后端下发）+ 一个「自定义」占位段 */
const presets = ref([])
const presetId = ref('')
const dirTpl = ref('')
const fileTpl = ref('')
const numberUnknown = ref(false)
const placeholders = ref([])
/** 示例元数据（后端 organize.SampleMeta）：预览树里那句「拿示例 XX 算的」用它 */
const sample = ref({ artist: '', title: '' })
const sidecarExts = ref(['.lrc', '.jpg'])
/** 示例渲染结果（后端 /api/organize/sample）：模板改动时即时更新，不在前端重算 */
const preview = ref({ dir: '', file: '', ext: 'flac' })
/** 自定义抽屉是否展开（选「自定义」卡片时自动张开） */
const customOpen = ref(false)
/** 预览树的重新入场动画键：样式变了就换 key，让整棵树重新浮入一次 */
const treeKey = ref(0)
let sampleTimer = null

/** 预演结果 */
const planRoot = ref('')
const planStats = ref({ audio: 0, move: 0, same: 0, conflict: 0, skip: 0, sidecars: 0 })
const planItems = ref([])
const planTruncated = ref(0)
const planWarnings = ref([])
const showSame = ref(false)

/** 撤销记录（后端 organize_journal.json 的摘要） */
const journal = ref({ available: false })

const notice = ref('')
const noticeTone = ref('error')

/** 两步确认的「已上膛」状态 */
const applyArmed = ref(false)
const undoArmed = ref(false)

/** 曲库目录（与顶部目录胶囊同源：prefs 的 nas_dir） */
const nasDir = ref(loadPref('nas_dir', '') || '')
const shownRoot = computed(() => planRoot.value || nasDir.value || '')

/**
 * 样式卡片 = 内置样式（后端已经配好成品示例路径）+ 一张「自定义」。
 * 卡片上显示的是**拼好的路径**而不是 {artist}/{album}：用户对模板串没有概念。
 */
const presetCards = computed(() => {
  const list = presets.value.map(p => ({
    id: p.id,
    name: p.name,
    example: p.example || '',
    dirExample: p.dir_example || '',
    fileExample: p.file_example || '',
    dir: p.dir || '',
    file: p.file || '',
  }))
  list.push({
    id: 'custom',
    name: '自定义',
    example: '自己写层级和文件名（可以拼年份、碟号）',
    dirExample: '',
    fileExample: '',
    dir: '',
    file: '',
  })
  return list
})

function cardClass(id) {
  return id === presetId.value
    ? 'border-[var(--ys-accent)] bg-[var(--ys-surface-sunken)]'
    : 'border-[var(--ys-border)] bg-[var(--ys-surface)] hover:border-[var(--ys-text-aux)]'
}

const sampleLabel = computed(() => {
  const a = String(sample.value?.artist || '')
  const t = String(sample.value?.title || '')
  if (!a && !t) return '一首歌'
  return a ? `${a} - ${t}` : t
})

/**
 * 预览树的每一行：根目录 → 逐层子目录 → 音频 → 同名封面/歌词。
 *
 * 目录是**单链**（每层只有一个子目录），所以每层都用 └── 收尾；
 * 文件那一层最后一个用 └──、其余用 ├── —— 与用户给的那种 tree 输出一致。
 */
const treeRows = computed(() => {
  const rootName = baseName(shownRoot.value) || 'Music'
  const dirRel = String(preview.value?.dir || '')
  const base = String(preview.value?.file || '')
  const ext = String(preview.value?.ext || 'flac')
  const segs = dirRel.split('/').filter(Boolean)
  // 示例里只摆最典型的两个附属文件（歌词、封面），6 种全列会把树拉得很长
  const sides = (sidecarExts.value || []).filter(e => e === '.lrc' || e === '.jpg')

  const rows = []
  rows.push({ key: 'root', line: `${rootName}/`, audio: false, title: shownRoot.value || '' })
  segs.forEach((seg, i) => {
    rows.push({ key: `d${i}`, line: `${'    '.repeat(i)}└── ${seg}/`, audio: false, title: seg })
  })
  const depth = segs.length
  const files = []
  if (base) files.push({ text: `${base}.${ext}`, audio: true })
  sides.forEach(e => files.push({ text: `${base}${e}`, audio: false }))
  files.forEach((f, i) => {
    rows.push({
      key: `f${i}`,
      line: `${'    '.repeat(depth)}${i === files.length - 1 ? '└── ' : '├── '}${f.text}`,
      audio: f.audio,
      title: f.text,
    })
  })
  return rows
})

const visibleItems = computed(() => {
  const items = planItems.value || []
  return showSame.value ? items : items.filter(it => it.status !== 'same')
})

const journalWhen = computed(() => {
  const t = journal.value?.created_at
  if (!t) return ''
  try {
    return fmtWhen(t) || ''
  } catch (e) {
    return ''
  }
})
const undoTitle = computed(() => {
  const n = Number(journal.value?.moved || 0)
  return `把上次整理的 ${n} 首搬回原位`
})

function baseName(p) {
  const s = String(p || '')
  const i = s.lastIndexOf('/')
  return i >= 0 ? s.slice(i + 1) : s
}

/** 目标路径显示成「相对于整理根目录」的样子 —— 全路径太长，看不出结构 */
function relPath(p) {
  const s = String(p || '')
  const root = String(planRoot.value || '')
  if (!s) return ''
  if (root && s.startsWith(root + '/')) return s.slice(root.length + 1)
  return s
}

function badgeText(status) {
  if (status === 'move') return '搬'
  if (status === 'same') return '已就位'
  if (status === 'conflict') return '冲突'
  return '跳过'
}

function badgeClass(status) {
  if (status === 'move') return 'bg-emerald-50 text-emerald-700'
  if (status === 'same') return 'bg-[var(--ys-surface-control)] text-[var(--ys-text-muted)]'
  if (status === 'conflict') return 'bg-amber-50 text-amber-700'
  return 'bg-rose-50 text-rose-700'
}

function say(msg, isError = false) {
  if (!msg) {
    notice.value = ''
    return
  }
  noticeTone.value = isError ? 'error' : 'ok'
  notice.value = msg
  if (isError) notifyFail(msg)
  else notifyOk(msg)
}

/** 请求体：模板字段一律显式传（用户在页面上看到的那一套就是要执行的那一套） */
function requestBody() {
  return {
    dir: nasDir.value || '',
    dir_template: dirTpl.value,
    file_template: fileTpl.value,
    number_unknown: !!numberUnknown.value,
  }
}

async function scanAudioAudit() {
  if (auditBusy.value) return
  auditBusy.value = true; auditError.value = ''
  try {
    const res = await OrganizeAPI.audioAudit({ dir: nasDir.value || '' })
    const data = res?.data || {}
    auditScanned.value = Number(data.scanned) || 0
    auditTotal.value = Number(data.total) || 0
    auditItems.value = (Array.isArray(data.issues) ? data.issues : []).map(x => ({ ...x, sizeText: fmtAuditSize(x.size), durationText: x.duration_sec ? `${Math.round(x.duration_sec)} 秒` : '时长未知' }))
  } catch (e) { auditError.value = failText('检测音质', e, '检测失败') }
  finally { auditBusy.value = false }
}
function fmtAuditSize(n) { const v=Number(n)||0; if(v<1024)return `${v} B`; if(v<1048576)return `${(v/1024).toFixed(1)} KB`; return `${(v/1048576).toFixed(1)} MB` }
function playAuditSong(song) {
 const playlist=auditItems.value.map(x=>({path:x.path,name:x.title||x.name,title:x.title||x.name,artist:x.artist,streamUrl:NasAPI.streamUrl(x.path),source:'nas',format:x.ext}))
 emit('play',{song:playlist.find(x=>x.path===song.path),playlist,index:playlist.findIndex(x=>x.path===song.path)})
}
function replaceAuditSong(song) { candidateItem.value={path:song.path,name:song.name,title:song.title||song.name,artist:song.artist,duration_sec:song.duration_sec,container:song.container,size:song.size,kind:'',detail:''}; candidateOpen.value=true }
function onAuditReplaced({item}) { auditItems.value=auditItems.value.filter(x=>x.path!==item.path); notifyTidyProgress() }
async function deleteAuditSong(song) {
 try { const res=await NasAPI.deleteFiles({paths:[song.path]}); const out=deleteOutcome(res); if(!out.ok)throw new Error(out.message); auditItems.value=auditItems.value.filter(x=>x.path!==song.path); notifyTidyProgress() }
 catch(e){ auditError.value=failText('移入回收站',e,'操作失败') }
}


function onTemplateEdit() {
  presetId.value = 'custom'
  applyArmed.value = false
  void refreshSampleDebounced()
}

function onDetailsToggle(e) {
  customOpen.value = !!(e && e.target && e.target.open)
}

/** 模板改了要立刻看到「这样改完长什么样」：防抖 250ms 问后端渲染一条示例 */
async function refreshSampleDebounced() {
  if (sampleTimer) clearTimeout(sampleTimer)
  sampleTimer = setTimeout(() => { void refreshSample() }, 250)
}

async function refreshSample() {
  try {
    const res = await OrganizeAPI.sample({ dir_template: dirTpl.value, file_template: fileTpl.value })
    const data = res?.data
    if (!data) return
    preview.value = { dir: data.dir || '', file: data.file || '', ext: data.ext || 'flac' }
    if (Array.isArray(data.sidecars) && data.sidecars.length) sidecarExts.value = data.sidecars
    treeKey.value += 1
  } catch (_) {
    /* 示例渲染失败不影响预演与执行；保持上一次的树即可 */
  }
}

function choosePreset(id) {
  presetId.value = id
  const p = presetCards.value.find(x => x.id === id)
  if (id === 'custom') {
    // 自定义：直接把抽屉拉下来（否则用户点了「自定义」看不到可改的地方）
    customOpen.value = true
  } else if (p) {
    dirTpl.value = p.dir || ''
    fileTpl.value = p.file || ''
    // 卡片自带的示例是后端算好的，不用再问一次
    preview.value = { dir: p.dirExample || '', file: p.fileExample || '', ext: 'flac' }
    treeKey.value += 1
  }
  applyArmed.value = false
  undoArmed.value = false
}

/** 用生效中的模板反推「当前是哪个内置样式」，反推不出来就落到自定义 */
function syncPresetFromTemplate(tpl) {
  const hit = presets.value.find(p => (p.dir || '') === (tpl.dir || '') && (p.file || '') === (tpl.file || ''))
  presetId.value = hit ? hit.id : 'custom'
}

async function refresh(opts = {}) {
  try {
    const data = await OrganizeAPI.presets()
    presets.value = Array.isArray(data.presets) ? data.presets : []
    placeholders.value = Array.isArray(data.placeholders) ? data.placeholders : []
    journal.value = data.journal || { available: false }
    if (data.sample) sample.value = data.sample
    if (Array.isArray(data.sidecar_exts) && data.sidecar_exts.length) sidecarExts.value = data.sidecar_exts
    // 只在首次加载时用配置里的模板覆盖输入框：之后用户手改的内容不能被一次刷新冲掉。
    if (!opts.keepTemplate) {
      const tpl = data.template || {}
      dirTpl.value = tpl.dir || ''
      fileTpl.value = tpl.file || ''
      numberUnknown.value = !!data.number_unknown
      syncPresetFromTemplate(tpl)
    }
    // 预览树跟着「生效中的模板」走（keepTemplate 时按输入框里的来，别把用户手改的盖掉）
    const view = data.template_view || {}
    if (opts.keepTemplate) await refreshSample()
    else {
      preview.value = { dir: view.dir || '', file: view.file || '', ext: 'flac' }
      treeKey.value += 1
    }
    if (!nasDir.value && data.dir) nasDir.value = data.dir
  } catch (e) {
    say(failText('读取整理样式', e), true)
  }
}

async function runPlan(opts = {}) {
  if (planBusy.value) return
  planBusy.value = true
  if (!opts.silent) {
    applyArmed.value = false
    undoArmed.value = false
    say('')
  }
  try {
    const res = await OrganizeAPI.plan(requestBody())
    const bad = apiError(res)
    if (bad) {
      if (!opts.silent) say(bad, true)
      return
    }
    const data = res?.data || {}
    planRoot.value = data.root || ''
    planStats.value = data.stats || { audio: 0, move: 0, same: 0, conflict: 0, skip: 0, sidecars: 0 }
    planItems.value = Array.isArray(data.items) ? data.items : []
    planTruncated.value = Number(data.truncated) || 0
    planWarnings.value = Array.isArray(data.warnings) ? data.warnings : []
    if (opts.silent) return
    const st = planStats.value
    if (!st.audio) say('这个目录里没扫到音频文件', true)
    else if (!st.move) say('已经全是这个结构了，没有要搬的')
    else say(`要搬 ${st.move} 首${st.conflict ? `，${st.conflict} 条撞车需要先处理` : ''}`)
  } catch (e) {
    if (!opts.silent) say(failText('预演整理方案', e), true)
  } finally {
    planBusy.value = false
  }
}

/** 「开始整理」第一下只上膛，第二下才真搬 */
function armApply() {
  if (busy.value) return
  if (!applyArmed.value) {
    applyArmed.value = true
    undoArmed.value = false
    return
  }
  applyArmed.value = false
  void runApply()
}

async function runApply() {
  busy.value = true
  say('')
  try {
    // 先存样式：搬文件不可逆，不能出现「按 A 预演、用 B 执行」之后连 B 是什么都说不清。
    try {
      await OrganizeAPI.saveTemplate(dirTpl.value, fileTpl.value, numberUnknown.value)
    } catch (e) {
      // 存样式失败不该拦住执行，但要说一声（否则下次打开样式变了会莫名其妙）
      notifyFail(failText('记住整理样式', e))
    }
    const res = await OrganizeAPI.apply(requestBody())
    const bad = apiError(res)
    if (bad) {
      say(bad, true)
      return
    }
    const data = res?.data || {}
    const result = data.result
    if (!result) {
      say(res?.message || '没有需要移动的文件')
      await runPlan()
      return
    }
    planRoot.value = data.plan?.root || planRoot.value
    const moved = Number(result.moved) || 0
    const side = Number(result.sidecars) || 0
    const failed = Number(result.failed) || 0
    const parts = [`搬好 ${moved} 首`]
    if (side) parts.push(`附带歌词 / 封面 ${side} 个`)
    if (failed) parts.push(`${failed} 个没搬成功`)
    if (data.journal_saved) parts.push('可撤销')
    else if (moved || side) parts.push('撤销记录没写成功，这次无法撤销')
    say(parts.join('，'), failed > 0)
    // 执行时算的那份方案已经过期了（那些「搬」现在都是「已就位」），
    // 悄悄重算一遍让列表反映磁盘真实状态；提示语保留上面这句结果。
    await runPlan({ silent: true })
    // 搬完刷新一次撤销记录（标题右侧的按钮据此从灰变亮）
    await refresh({ keepTemplate: true })
  } catch (e) {
    say(failText('执行文件夹整理', e), true)
  } finally {
    busy.value = false
  }
}

function armUndo() {
  if (busy.value || !journal.value.available) return
  if (!undoArmed.value) {
    undoArmed.value = true
    applyArmed.value = false
    say('')
    return
  }
  undoArmed.value = false
  void runUndo()
}

async function runUndo() {
  busy.value = true
  say('')
  try {
    const res = await OrganizeAPI.undo()
    const bad = apiError(res)
    if (bad) {
      say(bad, true)
      return
    }
    const result = res?.data?.result || {}
    const failed = Number(result.failed) || 0
    const parts = [`搬回 ${Number(result.moved) || 0} 首`]
    if (Number(result.sidecars)) parts.push(`附带歌词 / 封面 ${Number(result.sidecars)} 个`)
    if (failed) parts.push(`${failed} 个没搬回去`)
    say(parts.join('，'), failed > 0)
    // 撤销记录用一次就清掉（后端口径），这里直接按「不可用」渲染
    journal.value = { available: false }
    // 歌都搬回原位了，磁盘状态又变了 —— 重算一遍，别让统计停在 0 首。
    await runPlan({ silent: true })
  } catch (e) {
    say(failText('撤销上次整理', e), true)
  } finally {
    busy.value = false
  }
}

/** 顶部目录胶囊换了目录：作废旧方案，重新读一次目录 */
let onDirChanged = null

onMounted(() => {
  void refresh()
  onDirChanged = (e) => {
    nasDir.value = (e && e.detail && e.detail.dir) || loadPref('nas_dir', '') || ''
    auditItems.value = []; auditScanned.value = 0; auditTotal.value = 0
    planRoot.value = ''
    planItems.value = []
    planStats.value = { audio: 0, move: 0, same: 0, conflict: 0, skip: 0, sidecars: 0 }
    planTruncated.value = 0
    planWarnings.value = []
    applyArmed.value = false
    undoArmed.value = false
    say('')
  }
  window.addEventListener('ys:dir-changed', onDirChanged)
})

onUnmounted(() => {
  if (onDirChanged) window.removeEventListener('ys:dir-changed', onDirChanged)
})
</script>

<style scoped>
/*
  预览树的入场动画：切样式时整棵树重来一遍，逐行错开 28ms 浮入。
  用途是让「结构变了」这件事有可见的反馈 —— 换样式后如果只是文字瞬间替换，
  人眼会以为没生效。动画只做一次，不循环、不呼吸，避免干扰阅读。
*/
.ys-tree-row {
  animation: ys-tree-in 260ms cubic-bezier(0.22, 1, 0.36, 1) both;
}

@keyframes ys-tree-in {
  from {
    opacity: 0;
    transform: translateY(-3px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* 系统开了「减少动态效果」就不动，直接显示终态 */
@media (prefers-reduced-motion: reduce) {
  .ys-tree-row {
    animation: none;
  }
}
</style>
