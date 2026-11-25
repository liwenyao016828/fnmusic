<template>
  <!--
    「补全整理」各子页里的修复进度卡片（v2.37.0，用户 2026-11-25 原话）：

    第一版挂在段落导航下方共用一条，用户看过之后改成：「元数据的卡片就显示在补全页面
    就可以，坏文件和低音质也是在对应页面就可以。元数据需要显示多个卡片：总数，歌词，
    封面…等补全页面的功能，显示数字和对应类别就可以。另外两个页面也是同理。」

    所以现在按 group 拆开，各自挂在自己那一页：
      · group="meta"    元数据补全页：总数 / 还缺 / 歌词 / 封面 / 年份 / 曲序 / 光盘 / 风格
      · group="audio"   修复清单页：  总数 / 还缺 / 格式不对 / 时长过短 / 假无损
      · group="quality" 换音质页：    总数 / 已扫描 / 待替换 / 已修复

    数字全部来自落盘快照（library_index.json / repair_scan.json / quality_scan.json），
    刷新这张卡片不会触发任何全库重扫。
  -->
  <div class="mb-3 flex flex-wrap items-stretch gap-2">
    <section
      v-for="card in cards"
      :key="card.key"
      class="min-w-[92px] flex-1 rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-[var(--ys-surface)] px-2.5 py-1.5"
      :title="card.title || ''"
    >
      <div class="truncate text-[11px] leading-4 text-[var(--ys-text-muted)]">{{ card.label }}</div>
      <div class="flex items-baseline gap-0.5 tabular-nums">
        <span
          class="text-[15px] font-semibold leading-5"
          :class="card.accent ? 'text-[var(--ys-accent)]' : 'text-[var(--ys-text)]'"
        >{{ card.value }}</span>
        <span class="text-[10px] text-[var(--ys-text-muted)]">首</span>
      </div>
    </section>

    <div
      v-if="!known"
      class="flex min-w-[160px] flex-1 items-center rounded-[var(--ys-radius-card)] border border-dashed border-[var(--ys-border)] px-2.5 py-1.5 text-[11px] text-[var(--ys-text-muted)]"
    >这一类还没有扫描结论 · 先点上面的「扫描」</div>

    <button
      type="button"
      class="ys-press grid w-8 shrink-0 place-items-center self-stretch rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-[var(--ys-surface)] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
      :disabled="loading"
      :title="error || '重新读取（扫描完记得回来看一眼）'"
      @click="load"
    >
      <RefreshCw class="h-3.5 w-3.5" :class="loading ? 'animate-spin' : ''" />
    </button>
  </div>
</template>

<script setup>
/**
 * 修复进度卡片的取数与刷新。
 *
 * 数据全是**落盘快照**，所以这个组件随便刷新都不会触发全库重扫；真正让它变化的动作是：
 *   1. 跑扫描（修复清单 / 音质 / 补全）→ 后端扫描结束时自己写快照；
 *   2. 换版本成功 → 后端就地更新快照（见 intercept/repair_store.go）；
 * 两者都会发 `ys:tidy-progress-changed`，这里收到就重新读一次。
 *
 * 另加 60 秒轮询：宿主里可能同时开着别的窗口（比如官方音乐页），
 * 那些动作不会经过本窗口的事件总线，轮询是兜底。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { RepairAPI } from '../api/client'
import { loadPref } from '../services/prefs'
import { fail } from '../services/userMsg'

const props = defineProps({
  /** 这三类里的一类：meta（元数据补全页）/ audio（修复清单页）/ quality（换音质页） */
  group: { type: String, default: 'meta' },
})

/** 曲库目录键：与 TidyDirChip / TidyView 同一份（prefs 前缀 fn_，见 services/prefs.js） */
const PREF_KEY = 'nas_dir'
const POLL_MS = 60000

const dir = ref(loadPref(PREF_KEY, '') || '')
const loading = ref(false)
const error = ref('')
const total = ref(0)
const items = ref([])
let timer = null

const item = computed(() => items.value.find((it) => it.id === props.group) || null)
const known = computed(() => Boolean(item.value && item.value.known))

/**
 * 卡片由「这一类自己的数字」+「后端给的明细」拼出来：
 *   - meta / audio：总数、还缺，后面跟各细分类别；
 *   - quality：总数用全库（它一次只探两百首，必须让人看见还有多少没扫）。
 */
const cards = computed(() => {
  const it = item.value
  if (!it || !it.known) return []
  const out = []
  if (props.group === 'quality') {
    out.push({ key: 'total', label: '总数', value: total.value })
  } else {
    out.push({ key: 'total', label: '总数', value: it.covered })
  }
  for (const sub of it.sub || []) {
    out.push({
      key: sub.key,
      label: sub.label,
      value: sub.value,
      accent: sub.key === 'done' ? true : undefined,
    })
  }
  return out
})

async function load() {
  if (loading.value) return
  loading.value = true
  try {
    const res = await RepairAPI.progress(dir.value)
    const data = (res && res.data) || res || {}
    total.value = Number(data.total) || 0
    items.value = Array.isArray(data.items) ? data.items : []
    error.value = ''
  } catch (e) {
    // 卡片读不出来不是主流程故障：留一行提示，别弹窗打断用户。
    error.value = fail('读取修复进度', e, '进度读不出来')
  } finally {
    loading.value = false
  }
}

function onDirChanged(e) {
  const next = (e && e.detail && e.detail.dir) || loadPref(PREF_KEY, '') || ''
  if (next === dir.value) return
  dir.value = next
  total.value = 0
  items.value = []
  load()
}

/** 扫描/换版本之后由各页面发出来（跨标签页的库变更由 60 秒轮询兜底）。 */
function onProgressChanged() {
  load()
}

onMounted(() => {
  load()
  window.addEventListener('ys:dir-changed', onDirChanged)
  window.addEventListener('ys:tidy-progress-changed', onProgressChanged)
  window.addEventListener('storage', onDirChanged)
  timer = setInterval(load, POLL_MS)
})

onBeforeUnmount(() => {
  window.removeEventListener('ys:dir-changed', onDirChanged)
  window.removeEventListener('ys:tidy-progress-changed', onProgressChanged)
  window.removeEventListener('storage', onDirChanged)
  if (timer) clearInterval(timer)
  timer = null
})
</script>
