<template>
  <PageShell
    title="日志"
    desc="界面上的报错只给一句结论，完整信息都在这里 —— 排查时不用登 NAS 翻文件，也不用开浏览器控制台"
  >
    <template #actions>
      <button
        class="rounded-lg bg-gray-100 px-3 py-1.5 text-[12px] font-medium text-gray-600 transition-colors hover:bg-gray-200 disabled:opacity-50"
        :disabled="loading"
        @click="reload"
      >{{ loading ? '读取中…' : '刷新' }}</button>
      <button
        v-if="tab !== 'task'"
        class="rounded-lg bg-rose-50 px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-100"
        @click="clearLogs"
      >清空</button>
    </template>

    <!-- 标签 + 过滤 -->
    <div class="mb-3 flex flex-wrap items-center gap-2">
      <button
        v-for="t in tabs" :key="t.id"
        class="rounded-lg px-3.5 py-2 text-[12px] font-medium transition-all"
        :class="tab === t.id ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
        @click="switchTab(t.id)"
      >
        {{ t.name }}<span v-if="t.count" class="ml-1.5 opacity-80">{{ t.count }}</span>
      </button>

      <label v-if="tab === 'server'" class="ml-auto flex items-center gap-1.5 text-[11px] text-gray-500">
        <input v-model="autoRefresh" type="checkbox" class="accent-emerald-500" />
        自动刷新（5 秒）
      </label>
      <label v-else-if="tab === 'task'" class="ml-auto flex items-center gap-1.5 text-[11px] text-gray-500">
        <input v-model="onlyFailed" type="checkbox" class="accent-emerald-500" />
        只看失败
      </label>
      <label v-else class="ml-auto flex items-center gap-1.5 text-[11px] text-gray-500">
        <input v-model="onlyFailed" type="checkbox" class="accent-emerald-500" />
        只看失败
      </label>
    </div>

    <!-- 运行日志（后端 + 前端上报，同一时间轴） -->
    <div v-if="tab === 'server'" class="overflow-hidden rounded-[15px] border border-gray-200 bg-white">
      <p v-if="!serverEntries.length" class="py-14 text-center text-[12px] text-gray-400">
        还没有日志。后端启动信息、请求与报错都会出现在这里。
      </p>
      <template v-else>
        <div class="max-h-[620px] divide-y divide-gray-50 overflow-y-auto">
          <div v-for="(e, i) in serverEntries" :key="i" class="flex items-start gap-3 px-4 py-2">
            <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-400">{{ e.at }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="levelClass(e.level)"
            >{{ levelLabel(e.level) }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px]"
              :class="sourceClass(e.source)"
              :title="e.source === 'frontend' ? '由网页端上报（你刚才的操作）' : '服务端自身产生'"
            >{{ sourceLabel(e.source) }}</span>
            <span
              class="min-w-0 flex-1 break-all font-mono text-[12px] leading-relaxed"
              :class="e.level === 'error' ? 'text-rose-700' : (e.level === 'warn' ? 'text-amber-700' : 'text-gray-700')"
            >{{ e.message }}</span>
          </div>
        </div>
        <p class="border-t border-gray-100 px-4 py-2 text-[11px] text-gray-400">
          共 {{ serverTotal }} 条 · 只保留在内存里，重启应用即清空 ·
          <span class="text-gray-500">「前端」= 网页端上报的操作报错</span>
        </p>
      </template>
    </div>

    <!-- 任务日志 -->
    <div v-else-if="tab === 'task'" class="overflow-hidden rounded-[15px] border border-gray-200 bg-white">
      <p v-if="!visibleTaskRuns.length" class="py-14 text-center text-[12px] text-gray-400">
        {{ onlyFailed ? '没有失败的任务记录。' : '还没有任务执行记录。下载、整理、推送每跑一次都会记在这里。' }}
      </p>
      <div v-else class="max-h-[620px] divide-y divide-gray-50 overflow-y-auto">
        <div v-for="(r, i) in visibleTaskRuns" :key="i" class="flex flex-wrap items-start gap-3 px-4 py-2.5">
          <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-400">{{ r.at }}</span>
          <span class="shrink-0 rounded-full bg-gray-100 px-2 py-0.5 text-[10px] text-gray-600">{{ r.kind }}</span>
          <span
            class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
            :class="r.ok ? 'bg-emerald-50 text-emerald-600' : 'bg-rose-50 text-rose-600'"
          >{{ r.ok ? '成功' : '失败' }}</span>
          <div class="min-w-0 flex-1">
            <b class="block truncate text-[12px] text-gray-800">{{ r.name }}</b>
            <span class="block text-[11px] leading-relaxed text-gray-500">{{ r.detail }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 操作日志（本机留存，重启不丢） -->
    <div v-else class="overflow-hidden rounded-[15px] border border-gray-200 bg-white">
      <p v-if="!visibleLocal.length" class="py-14 text-center text-[12px] text-gray-400">
        {{ onlyFailed ? '没有失败的操作记录。' : '还没有操作记录。你在界面上的操作一旦出错，完整信息会记在这里。' }}
      </p>
      <template v-else>
        <div class="max-h-[620px] divide-y divide-gray-50 overflow-y-auto">
          <div v-for="(e, i) in visibleLocal" :key="i" class="flex items-start gap-3 px-4 py-2">
            <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-400">{{ e.at }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="levelClass(e.level)"
            >{{ levelLabel(e.level) }}</span>
            <span v-if="e.scope" class="shrink-0 rounded-full bg-gray-100 px-2 py-0.5 text-[10px] text-gray-600">
              {{ e.scope }}
            </span>
            <span
              class="min-w-0 flex-1 break-all font-mono text-[12px] leading-relaxed"
              :class="e.level === 'error' ? 'text-rose-700' : (e.level === 'warn' ? 'text-amber-700' : 'text-gray-700')"
            >{{ e.message }}</span>
          </div>
        </div>
        <p class="border-t border-gray-100 px-4 py-2 text-[11px] text-gray-400">
          共 {{ localEntries.length }} 条 · 保存在本机浏览器，刷新与重启都不丢（最多保留 200 条）
        </p>
      </template>
    </div>
  </PageShell>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { markLoaded, isStale } from '../services/viewCache'
import { LogsAPI, MonitorAPI, PushAPI } from '../api/client'
import { logEntries, clearLocalLog } from '../services/appLog'
import PageShell from './PageShell.vue'

const tab = ref('server')
const loading = ref(false)
const autoRefresh = ref(true)
const onlyFailed = ref(false)

const serverEntries = ref([])
const serverTotal = ref(0)
const taskRuns = ref([])
const localEntries = ref([])

const tabs = computed(() => [
  { id: 'server', name: '运行日志', count: serverTotal.value || '' },
  { id: 'task', name: '任务日志', count: taskRuns.value.length || '' },
  { id: 'local', name: '操作日志', count: localEntries.value.length || '' },
])

const visibleTaskRuns = computed(() =>
  onlyFailed.value ? taskRuns.value.filter((r) => !r.ok) : taskRuns.value,
)

// 本地留存是时间正序，界面按「新的在上」显示
const visibleLocal = computed(() => {
  const list = localEntries.value.slice().reverse()
  return onlyFailed.value ? list.filter((e) => e.level === 'error') : list
})

function levelLabel(l) {
  if (l === 'error') return '错误'
  if (l === 'warn') return '警告'
  return '信息'
}

function levelClass(l) {
  if (l === 'error') return 'bg-rose-50 text-rose-600'
  if (l === 'warn') return 'bg-amber-50 text-amber-700'
  return 'bg-gray-100 text-gray-500'
}

/** 日志来源：后端自身 / 网页端上报 */
function sourceLabel(s) {
  return s === 'frontend' ? '前端' : '后端'
}

function sourceClass(s) {
  return s === 'frontend' ? 'bg-blue-50 text-blue-600' : 'bg-gray-100 text-gray-400'
}

/** 把秒级 / 毫秒级时间戳或 ISO 字符串统一成毫秒数，便于排序 */
function tsOf(v) {
  if (!v) return 0
  if (typeof v === 'number') return v > 1e12 ? v : v * 1000
  const t = Date.parse(v)
  return Number.isNaN(t) ? 0 : t
}

function fmtTime(v) {
  const ms = tsOf(v)
  if (!ms) return String(v || '')
  return new Date(ms).toLocaleTimeString('zh-CN', { hour12: false })
}

async function loadServer() {
  try {
    const res = await LogsAPI.list(500)
    serverEntries.value = res?.data?.entries || []
    serverTotal.value = res?.data?.total ?? serverEntries.value.length
  } catch (_) {
    // 后端不可用时静默：日志页本身不该再制造报错
  }
}

/** 操作日志：读本机留存（同步，不请求后端） */
function loadLocal() {
  localEntries.value = logEntries()
}

/**
 * 任务日志 = 聚合「推送到曲库」（监控运行记录）与「推送到飞牛歌单」（同步任务运行记录）。
 * 两者各有自己的存储，这里只做合并展示，不新增存储。
 *
 * ⚠️ 监控的运行记录里**只存了 `monitor_id`，没有名字**（实测字段就是没有 monitor_name），
 * 所以额外拉一次监控列表做 id → 名字 的映射 —— 否则界面上会显示
 * `mon_1789710169610041532` 这种内部 id，用户看不懂。
 * 走映射还有个好处：**历史记录也能显示名字**（后端补字段只能修新记录）。
 */
async function loadTasks() {
  const out = []
  const [mon, push, monitors] = await Promise.allSettled([
    MonitorAPI.runs({ limit: 100 }),
    PushAPI.runs('', 100),
    MonitorAPI.list(),
  ])

  const nameById = new Map()
  if (monitors.status === 'fulfilled') {
    for (const m of monitors.value?.data?.monitors || []) {
      if (m?.id) nameById.set(m.id, m.name || '')
    }
  }

  if (mon.status === 'fulfilled') {
    for (const r of mon.value?.data?.runs || []) {
      const counts = []
      if (r.found) counts.push(`发现 ${r.found}`)
      if (r.new_items) counts.push(`新增 ${r.new_items}`)
      if (r.downloaded) counts.push(`下载 ${r.downloaded}`)
      if (r.failed) counts.push(`失败 ${r.failed}`)
      out.push({
        ts: tsOf(r.started_at),
        at: fmtTime(r.started_at),
        kind: '推送到曲库',
        name: r.monitor_name || nameById.get(r.monitor_id) || r.monitor_id || '监控任务',
        ok: r.status === 'ok' || r.status === 'success',
        detail:
          (r.baseline ? '【首次检查】' : '') +
          (counts.join(' · ') || String(r.log || '').split('\n')[0] || ''),
      })
    }
  }

  if (push.status === 'fulfilled') {
    for (const r of push.value?.data?.runs || []) {
      const res = r.result || {}
      const counts = []
      if (res.total) counts.push(`来源 ${res.total} 首`)
      if (res.matched) counts.push(`匹配 ${res.matched} 首`)
      if (res.added) counts.push(`入歌单 ${res.added} 首`)
      if (res.missing) counts.push(`缺 ${res.missing} 首`)
      out.push({
        ts: tsOf(r.started_at),
        at: fmtTime(r.started_at),
        kind: '推送到飞牛歌单',
        name: r.task_name || r.task_id || '同步任务',
        ok: r.status === 'success',
        detail: r.error || counts.join(' · ') || '',
      })
    }
  }

  out.sort((a, b) => b.ts - a.ts)
  taskRuns.value = out.slice(0, 200)
}

async function reload() {
  loading.value = true
  try {
    if (tab.value === 'server') await loadServer()
    else if (tab.value === 'task') await loadTasks()
    else loadLocal()
  } finally {
    loading.value = false
  }
}

function switchTab(id) {
  tab.value = id
  reload()
}

async function clearLogs() {
  if (tab.value === 'local') {
    if (!confirm('清空本机的操作日志？只清这台浏览器里的记录。')) return
    clearLocalLog()
    loadLocal()
    return
  }
  if (!confirm('清空后端运行日志？只清内存里的记录，不影响文件与配置。')) return
  try {
    await LogsAPI.clear()
    await loadServer()
  } catch (_) {}
}

let timer = null
function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

function startTimer() {
  if (timer) return
  timer = setInterval(() => {
    if (tab.value === 'server' && autoRefresh.value) loadServer()
  }, 5000)
}

onMounted(() => {
  reload()
  markLoaded('logs')
  // 运行日志每 5 秒拉一次（可关）—— 排查时能边操作边看
  startTimer()
})

onUnmounted(stopTimer)

// 常驻页面：切走就停轮询（否则日志页在后台每 5 秒打一次后端），切回来按 TTL 刷一次
onDeactivated(stopTimer)
onActivated(() => {
  startTimer()
  if (isStale('logs')) reload()
})
</script>
