<template>
  <PageShell>
    <template #actions>
      <button
        class="rounded-[10px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03] hover:text-[var(--ys-text)] disabled:opacity-50"
        :disabled="loading"
        @click="reload"
      >{{ loading ? '读取中…' : '刷新' }}</button>
      <!-- 导出打包：这一页自己的说明里一直写着「也可以一键导出打包」
           （services/navViews.js 的 system/logs desc），但按钮只长在「设置」段里，
           站在日志页的人根本找不到。同一件事不该让人先去猜它在哪一页。
           走的是同一个 /api/logs?format=zip（配置凭据后端已脱敏）。 -->
      <button
        class="rounded-[10px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03] hover:text-[var(--ys-text)] disabled:opacity-50"
        :disabled="exporting"
        title="打包成一个 zip：运行日志 + 环境信息 + 脱敏后的配置，发出去排查用"
        @click="exportLogs"
      >{{ exporting ? '打包中…' : '导出' }}</button>
      <!-- 清空：两步确认（全站现在都不用 confirm()）——第一次点先摆出确认条，
           第二次才真清。只清内存里的记录，不动文件与配置。 -->
      <button
        v-if="tab !== 'task' && !clearArmed"
        class="rounded-[10px] px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-50"
        @click="clearArmed = true"
      >清空</button>
      <button
        v-else-if="clearArmed"
        class="rounded-[10px] bg-rose-600 px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-rose-700"
        @click="clearLogs"
      >确认清空</button>
      <button
        v-if="clearArmed"
        class="rounded-[10px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03]"
        @click="clearArmed = false"
      >取消</button>
    </template>

    <!-- 标签 + 过滤（2026-10-18 用户「全局卡片化」：这一排控件原来裸在页面底色上，
         现在按全站统一样式收进白底圆角卡，与下面的日志面板接成一张连贯的界面） -->
    <div class="mb-3 flex flex-wrap items-center gap-2 rounded-[18px] border border-[var(--ys-border)] bg-white px-2.5 py-2.5 shadow-[0_1px_2px_rgba(0,0,0,0.03)]">
      <button
        v-for="t in tabs" :key="t.id"
        class="ys-press rounded-[9px] px-3.5 py-2 text-[12px] font-medium transition-colors"
        :class="tab === t.id
          ? 'bg-[var(--ys-text)] text-white'
          : 'text-[var(--ys-text-muted)] hover:bg-black/[0.05] hover:text-[var(--ys-text)]'"
        @click="switchTab(t.id)"
      >
        {{ t.name }}<span v-if="t.count" class="ml-1.5 opacity-80">{{ t.count }}</span>
      </button>

      <label v-if="tab === 'server'" class="ml-auto flex items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
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
    <div v-if="tab === 'server'" class="overflow-hidden rounded-[18px] border border-gray-200 bg-white">
      <p v-if="!serverEntries.length" class="m-3 rounded-xl border border-dashed border-gray-200 bg-gray-50/50 py-12 text-center text-[12px] text-gray-400">
        还没有日志 —— 后端启动信息、请求与报错都会出现在这里
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
          共 {{ serverTotal }} 条 ·
          <span v-if="serverLogFile" class="text-gray-500">内存副本重启即清空，同时落盘到 {{ serverLogFile }}</span>
          <span v-else>只保留在内存里，重启应用即清空</span>
          · <span class="text-gray-500">「前端」= 网页端上报的操作报错</span>
        </p>
      </template>
    </div>

    <!-- 任务日志 -->
    <div v-else-if="tab === 'task'" class="overflow-hidden rounded-[18px] border border-gray-200 bg-white">
      <p v-if="!visibleTaskRuns.length" class="m-3 rounded-xl border border-dashed border-[var(--ys-border)] bg-black/[0.015] py-12 text-center text-[12px] text-[var(--ys-text-faint)]">
        {{ onlyFailed ? '没有失败的任务记录' : '还没有任务执行记录 —— 下载、整理、推送每跑一次都会记在这里' }}
      </p>
      <div v-else class="max-h-[620px] divide-y divide-gray-50 overflow-y-auto">
        <div v-for="(r, i) in visibleTaskRuns" :key="i" class="flex flex-wrap items-start gap-3 px-4 py-2.5">
          <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-400">{{ r.at }}</span>
          <span class="shrink-0 rounded-full bg-black/[0.05] px-2 py-0.5 text-[10px] text-[var(--ys-text-muted)]">{{ r.kind }}</span>
          <span
            class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
            :class="r.ok ? 'bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]' : 'bg-rose-50 text-rose-600'"
          >{{ r.ok ? '成功' : '失败' }}</span>
          <div class="min-w-0 flex-1">
            <b class="block truncate text-[12px] font-medium text-[var(--ys-text)]">{{ r.name }}</b>
            <span class="block text-[11px] leading-relaxed text-[var(--ys-text-muted)]">{{ r.detail }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 操作日志（本机留存，重启不丢） -->
    <div v-else class="overflow-hidden rounded-[18px] border border-[var(--ys-border)] bg-white shadow-[0_1px_2px_rgba(0,0,0,0.03)]">
      <p v-if="!visibleLocal.length" class="m-3 rounded-xl border border-dashed border-gray-200 bg-gray-50/50 py-12 text-center text-[12px] text-gray-400">
        {{ onlyFailed ? '没有失败的操作记录' : '还没有操作记录 —— 播放、搜索、下载、保存设置等操作都会记在这里；出错时附带完整信息' }}
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
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { markLoaded, isStale } from '../services/viewCache'
import { currentView, segmentOf } from '../services/navBus'
import { LogsAPI, MonitorAPI, PushAPI } from '../api/client'
import { logEntries, clearLocalLog } from '../services/appLog'
import { fail as failText } from '../services/userMsg'
import { fail as notifyFail } from '../services/feedback'
import PageShell from './PageShell.vue'

const tab = ref('server')
const loading = ref(false)
const autoRefresh = ref(true)
const onlyFailed = ref(false)
const exporting = ref(false)
// 清空的两步确认（见模板）：以前这里还有一个 window.confirm()，是全项目最后一处
// 原生弹窗——已经统一成「就在原地再点一次」。
const clearArmed = ref(false)

const serverEntries = ref([])
const serverTotal = ref(0)
// 落盘文件路径（设置里配置了「日志存储位置」时非空）——页脚提示日志同时写到了哪里
const serverLogFile = ref('')
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
    // 后端按时间正序返回（旧→新）。2026-10-26 用户口径：日志页「新的在左上方」
    // —— 倒过来显示，最新的永远在第一行，不用往下滚找最后一条。
    serverEntries.value = (res?.data?.entries || []).slice().reverse()
    serverTotal.value = res?.data?.total ?? serverEntries.value.length
    serverLogFile.value = res?.data?.log_file || ''
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
  clearArmed.value = false
  reload()
}

async function clearLogs() {
  clearArmed.value = false
  if (tab.value === 'local') {
    clearLocalLog()
    loadLocal()
    return
  }
  try {
    await LogsAPI.clear()
    await loadServer()
  } catch (_) {}
}

/** 「导出」：与「设置」段里那个按钮同一个接口（zip 里的凭据字段后端已抹除）。 */
function stamp() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`
}

async function exportLogs() {
  if (exporting.value) return
  exporting.value = true
  try {
    const blob = await LogsAPI.exportZip()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `qulv-logs-${stamp()}.zip`
    document.body.appendChild(a)
    a.click()
    a.remove()
    // 立刻回收：留着的话这个 blob 会一直占内存到刷新
    setTimeout(() => URL.revokeObjectURL(url), 10000)
  } catch (e) {
    notifyFail(failText('导出运行日志', e))
  } finally {
    exporting.value = false
  }
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
  // 操作日志顺手读一次：它只是读 localStorage，零成本。
  // 不读的话「操作日志」那个标签上没有条数，用户根本不知道里面有东西 ——
  // 而「点了查看日志却发现没日志」的另一半原因就在这里。
  loadLocal()
  reload()
  markLoaded('logs')
  // 运行日志每 5 秒拉一次（可关）—— 排查时能边操作边看；
  // 挂载时不在这个页（system 视图的 logs 段）就别起轮询，切过来时上面的 watch 会起。
  if (currentView.value === 'system' && segmentOf('system') === 'logs') startTimer()
})

onUnmounted(stopTimer)

// 常驻页面：切走就停轮询（否则日志页在后台每 5 秒打一次后端），切回来按 TTL 刷一次。
// ⚠️ 2026-10-17：原实现挂在 onActivated/onDeactivated 上 —— SwipePager 没有 KeepAlive，
// 那两个钩子从来不会触发（死代码）。改成 watch 导航总线的共享 currentView：
// 日志是 system 视图的默认段；在「设置」段停留时也把轮询停掉（看不到还每 5 秒打后端）。
watch(
  [currentView, () => (currentView.value === 'system' ? segmentOf('system') : '')],
  ([v, seg]) => {
    if (v !== 'system' || seg !== 'logs') { stopTimer(); return }
    startTimer()
    if (isStale('logs')) reload()
  },
)
</script>
