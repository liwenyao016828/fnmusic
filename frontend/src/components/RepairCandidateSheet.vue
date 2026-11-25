<template>
  <SideSheet v-model="open" :title="sheetTitle" :subtitle="item ? item.name || baseName(item.path) : ''">
    <div class="space-y-3">
      <!-- 当前文件：让用户先看清「为什么要换」 -->
      <div class="rounded-[var(--ys-radius-control)] bg-[var(--ys-surface-sunken)] px-3 py-2">
        <div class="text-[11px] text-[var(--ys-text-muted)]">当前文件</div>
        <div class="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-[12px] text-[var(--ys-text)]">
          <span class="ys-num">{{ currentMeta }}</span>
          <span v-if="issueText" class="text-[var(--ys-danger)]">{{ issueText }}</span>
        </div>
        <div v-if="item && (item.title || item.artist)" class="mt-1 truncate text-[11px] text-[var(--ys-text-muted)]">
          {{ [item.title, item.artist].filter(Boolean).join(' · ') }}
        </div>
      </div>

      <!-- 搜索行：关键词可以改，改完就地重搜 -->
      <div class="flex items-center gap-2">
        <input
          v-model="keyword"
          type="search"
          class="h-8 min-w-0 flex-1 rounded-lg border border-[var(--ys-line-control)] px-2.5 text-xs"
          placeholder="歌名（只填歌名，搜索更准）"
          @keyup.enter="search()"
        >
        <button class="ys-btn ys-btn-primary ys-btn-sm" :disabled="searching || !keyword.trim()" @click="search()">
          <Loader2 v-if="searching" class="h-3.5 w-3.5 animate-spin" />
          {{ searching ? '搜索中' : '搜索' }}
        </button>
      </div>

      <InlineNotice v-if="errorText" :text="errorText" />

      <!-- 换完的结果：前后一眼可比 -->
      <div
        v-if="lastResult"
        class="rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] px-3 py-2 text-[12px]"
      >
        <div class="font-medium text-[var(--ys-text)]">已替换</div>
        <div class="mt-1 text-[11px] text-[var(--ys-text-muted)]">{{ lastResult.note || '文件已覆盖' }}</div>
        <div class="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[11px] ys-num">
          <span class="text-[var(--ys-text-muted)]">替换前</span>
          <span>{{ audioMeta(lastResult.before) }}</span>
          <span class="text-[var(--ys-text-muted)]">替换后</span>
          <span class="text-[var(--ys-accent)]">{{ audioMeta(lastResult.after) }}</span>
        </div>
      </div>

      <!-- 候选列表 -->
      <div v-if="searching && !rows.length" class="ys-empty">正在搜各平台的版本…</div>
      <div v-else-if="!rows.length" class="ys-empty">
        没搜到别的版本。可以改改关键词再搜一次 —— 也可以在文件名里补上歌手名试试。
      </div>
      <ul v-else class="divide-y divide-[var(--ys-border)]">
        <li v-for="c in rows" :key="c.key">
          <button
            type="button"
            class="flex w-full items-center gap-2.5 py-2 text-left ys-press"
            :class="probeDead(c) ? 'opacity-50' : ''"
            :disabled="replacing || probeDead(c)"
            :title="probeDead(c) ? `这个版本用不了：${c.probe.reason}` : ''"
            @click="pick(c)"
          >
            <img
              v-if="c.cover"
              :src="c.cover"
              alt=""
              class="h-9 w-9 shrink-0 rounded-[6px] object-cover"
              loading="lazy"
              referrerpolicy="no-referrer"
              @error="$event.target.style.display = 'none'"
            >
            <span v-else class="h-9 w-9 shrink-0 rounded-[6px] bg-[var(--ys-surface-sunken)]" />
            <span class="min-w-0 flex-1">
              <span class="flex min-w-0 items-center gap-1.5">
                <span class="ys-cell min-w-0 text-[12px] font-medium text-[var(--ys-text)]">{{ c.name || '（无标题）' }}</span>
                <!-- 原唱 / 翻唱：跟随主题色（--ys-accent-* 那一族，换主题自动跟着变） -->
                <span
                  v-if="c.version"
                  class="shrink-0 rounded-full px-1.5 py-[1px] text-[10px] leading-[15px]"
                  :class="c.version === '原唱'
                    ? 'bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]'
                    : 'bg-[var(--ys-surface-sunken)] text-[var(--ys-text-muted)]'"
                >{{ c.version }}</span>
              </span>
              <span class="ys-cell block text-[11px] text-[var(--ys-text-muted)]">
                {{ [c.singer, c.album].filter(Boolean).join(' · ') || '—' }}
              </span>
              <!-- 预检说这条用不了：把原因摆出来，别让用户点了才知道 -->
              <span v-if="probeDead(c)" class="mt-0.5 block text-[11px] text-[var(--ys-danger)]">
                {{ c.probe.reason || '这个版本取不到直链' }}
              </span>
            </span>
            <span class="shrink-0 text-right text-[11px] leading-tight ys-num">
              <span class="block text-[var(--ys-text)]">{{ platformLabel(c.platform) }} · {{ qualityLabel(c.quality) }}</span>
              <span class="block text-[var(--ys-text-muted)]">
                {{ formatDuration(c.durationSec) }}<template v-if="c.sizeBytes"> · {{ formatSize(c.sizeBytes) }}</template>
              </span>
            </span>
          </button>
          <div
            v-if="armedKey === c.key"
            class="mb-2 rounded-[var(--ys-radius-control)] border border-[var(--ys-danger)] px-2.5 py-2 text-[11px]"
          >
            <span class="text-[var(--ys-text)]">用这个版本 <b>覆盖</b> 原文件？原文件会被直接替换，不保留旧的那份。</span>
            <span class="mt-1.5 flex gap-2">
              <button class="ys-btn ys-btn-danger ys-btn-sm" :disabled="replacing" @click="confirmReplace(c)">
                <Loader2 v-if="replacing" class="h-3.5 w-3.5 animate-spin" />
                {{ replacing ? '正在替换' : '确认替换' }}
              </button>
              <button class="ys-btn ys-btn-ghost ys-btn-sm" :disabled="replacing" @click="armedKey = ''">取消</button>
            </span>
          </div>
        </li>
      </ul>

      <!-- 预检进度：只占一行，别做成阻塞式 loading -->
      <div v-if="probing" class="flex items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
        <Loader2 class="h-3 w-3 animate-spin" />
        正在检测这些版本能不能取到直链…
      </div>
      <div v-else-if="probeNote" class="text-[11px] text-[var(--ys-text-muted)]">{{ probeNote }}</div>
    </div>

    <template #footer>
      <p class="text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
        换掉的是文件本身：扩展名、码率、时长都以新文件为准。原来的坏文件不另存 ——
        要保险的话，先在「快照」段拍一张。
      </p>
    </template>
  </SideSheet>
</template>

<script setup>
/**
 * 「歌曲修复(替换)」的候选版本抽屉。
 *
 * 为什么先搜索再让用户点：坏文件（扩展名与真实容器不符 / 时长过短 / 假无损）
 * 往往「同一首歌有好几个版本」，自动挑一个换掉风险太大 —— 用户明确要求
 * 「通过搜索歌名，返回各种版本让用户选择」，所以这里是**人来做决定**。
 *
 * 复用 `/api/search`（source=all）而不是新开搜索接口：它已经把原生四个平台
 * 和 musicdl 外挂源并起来了；`songmid` 才是平台内 id，传给后端才取得到流。
 */
import { computed, ref, watch } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import SideSheet from './SideSheet.vue'
import InlineNotice from './InlineNotice.vue'
import { RepairAPI, SearchAPI } from '../api/client'
import { platformName } from '../services/libraryFields'
import { qualityLabel as baseQualityLabel } from '../engine/quality'
import { fail as failText } from '../services/userMsg'
import { logInfo } from '../services/appLog'
import { notifyTidyProgress } from '../services/tidyProgress'
import {
  baseName, formatDuration, formatSize, candidateRows, candidateKeyword,
} from '../services/repairList'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  /** `{path, name, title, artist, duration_sec, container, kbps, size, kind, detail}` */
  item: { type: Object, default: null },
})
const emit = defineEmits(['update:modelValue', 'replaced'])

/** 只检前多少个候选：后端上限 40，排序已把最可能的放最前面，再多也没人看。 */
const PROBE_LIMIT = 40

const open = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const keyword = ref('')
const searching = ref(false)
const replacing = ref(false)
const rows = ref([])
const errorText = ref('')
const armedKey = ref('')
const lastResult = ref(null)
/** 可用性预检：正在检测 / 一句结论（检测完给个汇总，别让人以为没发生） */
const probing = ref(false)
const probeNote = ref('')
/** 预检代数：用户改关键词重搜时，上一轮预检的迟到结果必须丢掉 */
let probeGen = 0

const sheetTitle = computed(() => {
  const it = props.item
  if (!it) return '换一个版本'
  const kindText = { ext_mismatch: '格式不对', too_short: '时长过短', fake_lossless: '假无损' }[it.kind] || ''
  return kindText ? `换版本 · ${kindText}` : '换一个版本'
})

const currentMeta = computed(() => {
  const it = props.item
  if (!it) return '—'
  return [
    it.container ? String(it.container).toUpperCase() : (it.ext ? String(it.ext).toUpperCase() : ''),
    it.duration_sec ? formatDuration(it.duration_sec) : '',
    it.size ? formatSize(it.size) : '',
    it.kbps ? `${it.kbps} kbps` : '',
  ].filter(Boolean).join(' · ') || '—'
})

const issueText = computed(() => {
  const it = props.item
  if (!it) return ''
  return it.detail || ''
})

function platformLabel(id) {
  const value = String(id || '')
  const musicdlNames = { mg: '咪咕', bq: '千千', bi: 'B站' }
  const lxPlatformNames = { wy: '网易云', tx: 'QQ 音乐', kg: '酷狗', kw: '酷我', ...musicdlNames }
  if (value.toLowerCase().startsWith('lx:')) {
    const source = value.slice(3).toLowerCase()
    return `洛雪 · ${lxPlatformNames[source] || platformName(source) || source.toUpperCase()}`
  }
  return musicdlNames[value.toLowerCase()] || platformName(value) || value.toUpperCase()
}

function qualityLabel(value) {
  return value ? baseQualityLabel(value) : '音质未知'
}

function audioMeta(info) {
  if (!info) return '—'
  return [
    info.container ? String(info.container).toUpperCase() : '',
    formatDuration(info.duration_sec),
    formatSize(info.size),
    info.kbps ? `${Math.round(info.kbps)} kbps` : '',
  ].filter(Boolean).join(' · ')
}

/**
 * 这条候选是不是「稳定用不了」。
 *
 * 只有 ok=false 且 transient=false 才算判死：transient 只是这次没拿到直链
 * （宿主没起 / 上游抽风），禁掉它会让用户白等一轮重试。
 */
function probeDead(c) {
  return !!(c.probe && !c.probe.ok && !c.probe.transient)
}

/**
 * 对候选做可用性预检（用户要求「提前检测列表中的歌曲是否可用」）。
 *
 * 为什么不在 search() 里同步等：预检最坏要几秒（后端 8 并发、每条约 5 秒），
 * 把搜索结果卡在它后面等于搜完再转圈 —— 先出列表，再在后台把「用不了」的标红，
 * 用户想点的多半是排在前面那几条，等标注到达时他还没点到。
 *
 * 只检前 N 条（后端上限 40），因为排序已经把最可能的放在最前面了。
 */
async function probeRows(gen) {
  const items = rows.value
    .slice(0, PROBE_LIMIT)
    .map((c) => ({ platform: c.platform, platform_id: c.platformId }))
  if (!items.length) return
  probing.value = true
  probeNote.value = ''
  try {
    const res = await RepairAPI.probe(items)
    if (gen !== probeGen) return // 已经重搜过一轮，这批结果作废
    const payload = (res && (res.data || res)) || {}
    const list = Array.isArray(payload.items) ? payload.items : []
    const byKey = new Map()
    list.forEach((it) => byKey.set(`${it.platform}:${it.platform_id}`, it))
    rows.value = rows.value.map((c) => ({ ...c, probe: byKey.get(c.key) || c.probe || null }))
    const dead = list.filter((it) => !it.ok && !it.transient).length
    const soft = list.filter((it) => !it.ok && it.transient).length
    const ok = list.filter((it) => it.ok).length
    const parts = []
    if (ok) parts.push(`${ok} 个可用`)
    if (dead) parts.push(`${dead} 个用不了（已置灰）`)
    if (soft) parts.push(`${soft} 个暂时取不到`)
    probeNote.value = parts.length ? `已检测前 ${list.length} 个版本：${parts.join('，')}` : ''
    logInfo(`修复替换：可用性检测 ${list.length} 条 → ${parts.join('，') || '无结论'}`)
  } catch (e) {
    // 预检失败不该毁掉搜索：只在界面留一句，详情进日志
    if (gen === probeGen) probeNote.value = '可用性检测没跑成，仍可直接点选（取不到直链时会报原因）'
    logInfo(`修复替换：可用性检测失败 ${e && e.message ? e.message : ''}`)
  } finally {
    if (gen === probeGen) probing.value = false
  }
}

async function search() {
  const kw = keyword.value.trim()
  if (!kw) return
  searching.value = true
  errorText.value = ''
  armedKey.value = ''
  probeNote.value = ''
  const gen = ++probeGen
  try {
    // 12s 预算：聚合全部平台实测能到 50s 以上（被最慢的平台拖住），远超 axios
    // 默认的 30s —— 不点名预算的话抽屉就是「搜 60 秒然后超时报错」。到点后端把
    // 已经回来的平台结果先交出来（budget_hit=true），洛雪自己的预算是 8s，赶得上。
    const data = await SearchAPI.search(kw, 'all', 1, 30, 'lx', 12000)
    // /api/search 的信封把 list 放在顶层，同时也镜像一份在 data 里 —— 两种都认
    const payload = (data && (data.data || data)) || {}
    const it = props.item || {}
    rows.value = candidateRows(payload.list, it.duration_sec || 0, {
      keyword: kw,
      // 原唱/翻唱按「原文件歌手」判，不是按搜索框里的词 —— 搜索框现在只有歌名
      artist: it.artist || '',
    })
    const hit = payload.budget_hit ? '（已到等待上限，只含先返回的平台）' : ''
    logInfo(`修复替换：搜「${kw}」得到 ${rows.value.length} 个版本${hit}`)
    if (rows.value.length) probeRows(gen)
  } catch (e) {
    // fail() 自己会写日志（完整信息进日志、一句人话给界面），不要再单独 logError
    errorText.value = failText('修复替换：搜索版本', e, '搜索失败，换个关键词再试')
    rows.value = []
  } finally {
    searching.value = false
  }
}

function pick(c) {
  if (replacing.value) return
  // 两次点击确认：第一次只武装，避免手一滑就把歌换掉
  armedKey.value = armedKey.value === c.key ? '' : c.key
}

async function confirmReplace(c) {
  const it = props.item
  if (!it || !it.path) return
  replacing.value = true
  errorText.value = ''
  try {
    const res = await RepairAPI.replace({
      path: it.path,
      platform: c.platform,
      platform_id: c.platformId,
      title: c.name,
      artist: c.singer,
      album: c.album,
      duration: Math.round(c.durationSec || 0),
      quality: c.quality,
      cover_url: c.cover,
    })
    // 后端统一信封（{code,message,data}），替换结果在 data 里
    const result = (res && res.data) || null
    lastResult.value = result
    armedKey.value = ''
    logInfo(`修复替换：${baseName(it.path)} ← ${platformLabel(c.platform)} ${c.name}`)
    // 进度条：修好一首，那一类的「还剩」就该减一（后端快照已就地更新）。
    notifyTidyProgress()
    emit('replaced', { item: it, candidate: c, result })
  } catch (e) {
    errorText.value = failText('修复替换：覆盖文件', e, '替换失败，原文件没动')
  } finally {
    replacing.value = false
  }
}

watch(
  () => props.modelValue,
  (v) => {
    if (!v) return
    const it = props.item
    lastResult.value = null
    errorText.value = ''
    armedKey.value = ''
    rows.value = []
    probing.value = false
    probeNote.value = ''
    probeGen += 1
    // 只填歌名（用户 2026-11-25 明确）：拼歌手只会把搜索结果带偏
    keyword.value = candidateKeyword(it && (it.title || baseName(it.path)), it && it.artist)
    if (keyword.value) search()
  },
)
</script>