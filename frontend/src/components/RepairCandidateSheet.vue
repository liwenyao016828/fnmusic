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
          placeholder="歌名 + 歌手"
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
            :disabled="replacing"
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
              <span class="ys-cell block text-[12px] font-medium text-[var(--ys-text)]">{{ c.name || '（无标题）' }}</span>
              <span class="ys-cell block text-[11px] text-[var(--ys-text-muted)]">
                {{ [c.singer, c.album].filter(Boolean).join(' · ') || '—' }}
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
import {
  baseName, formatDuration, formatSize, candidateRows, candidateKeyword,
} from '../services/repairList'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  /** `{path, name, title, artist, duration_sec, container, kbps, size, kind, detail}` */
  item: { type: Object, default: null },
})
const emit = defineEmits(['update:modelValue', 'replaced'])

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

async function search() {
  const kw = keyword.value.trim()
  if (!kw) return
  searching.value = true
  errorText.value = ''
  armedKey.value = ''
  try {
    // 12s 预算：聚合全部平台实测能到 50s 以上（被最慢的平台拖住），远超 axios
    // 默认的 30s —— 不点名预算的话抽屉就是「搜 60 秒然后超时报错」。到点后端把
    // 已经回来的平台结果先交出来（budget_hit=true），洛雪自己的预算是 8s，赶得上。
    const data = await SearchAPI.search(kw, 'all', 1, 30, 'lx', 12000)
    // /api/search 的信封把 list 放在顶层，同时也镜像一份在 data 里 —— 两种都认
    const payload = (data && (data.data || data)) || {}
    rows.value = candidateRows(payload.list, (props.item && props.item.duration_sec) || 0)
    const hit = payload.budget_hit ? '（已到等待上限，只含先返回的平台）' : ''
    logInfo(`修复替换：搜「${kw}」得到 ${rows.value.length} 个版本${hit}`)
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
    keyword.value = candidateKeyword(it && (it.title || baseName(it.path)), it && it.artist)
    if (keyword.value) search()
  },
)
</script>