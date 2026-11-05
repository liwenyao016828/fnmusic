<template>
  <PageShell>
    <template #actions>
      <button
        class="rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03] hover:text-[var(--ys-text)] disabled:opacity-50"
        :disabled="loading"
        @click="reload"
      >{{ loading ? '读取中…' : '刷新' }}</button>
      <!-- 导出打包：这一页自己的说明里一直写着「也可以一键导出打包」
           （services/navViews.js 的 system/logs desc），但按钮只长在「设置」段里，
           站在日志页的人根本找不到。同一件事不该让人先去猜它在哪一页。
           走的是同一个 /api/logs?format=zip（配置凭据后端已脱敏）。 -->
      <button
        class="rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03] hover:text-[var(--ys-text)] disabled:opacity-50"
        :disabled="exporting"
        title="打包成一个 zip：运行日志 + 环境信息 + 脱敏后的配置，发出去排查用"
        @click="exportLogs"
      >{{ exporting ? '打包中…' : '导出' }}</button>
      <!-- 清空：两步确认（全站现在都不用 confirm()）——第一次点先摆出确认条，
           第二次才真清。只清内存里的记录，不动文件与配置。 -->
      <button
        v-if="tab !== 'task' && !clearArmed"
        class="rounded-[8px] px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-50"
        @click="clearArmed = true"
      >清空</button>
      <button
        v-else-if="clearArmed"
        class="rounded-[8px] bg-rose-600 px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-rose-700"
        @click="clearLogs"
      >确认清空</button>
      <button
        v-if="clearArmed"
        class="rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03]"
        @click="clearArmed = false"
      >取消</button>
    </template>

    <!--
      标签 + 过滤（2026-10-30 飞牛化：页内导航改成**文字 tab**）──
      原来这一排是「深底反白的胶囊按钮 + 白卡包裹」，那是控件形态。
      飞牛的页内导航（系统设置 → 访问设置/账号安全/防护/防火墙/证书）是：
      白底无卡片、无槽无胶囊，选中 = 深色加粗文字 + 下方 2px 强调色短线，
      未选中 = 中灰常规字重，标签间距约 32px。右侧的过滤开关仍靠右对齐。
    -->
    <div class="mb-3 flex flex-wrap items-end gap-8">
      <button
        v-for="t in tabs" :key="t.id"
        class="ys-press relative flex shrink-0 items-baseline whitespace-nowrap pb-2 text-[15px] leading-[20px] transition-colors duration-[var(--ys-dur-1)]"
        :class="tab === t.id
          ? 'font-semibold text-[var(--ys-text)]'
          : 'font-normal text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]'"
        @click="switchTab(t.id)"
      >
        <span class="relative inline-flex items-baseline">
          {{ t.name }}<span v-if="t.count" class="ml-1.5 text-[13px] opacity-80">{{ t.count }}</span>
          <span
            v-if="tab === t.id"
            class="absolute -bottom-[5px] left-0 right-0 h-[2px] rounded-full bg-[var(--ys-accent)]"
          />
        </span>
      </button>

      <label v-if="tab === 'server'" class="ml-auto flex items-center gap-1.5 pb-2 text-[11px] text-[var(--ys-text-muted)]">
        <input v-model="autoRefresh" type="checkbox" class="" />
        自动刷新（5 秒）
      </label>
      <label v-if="tab === 'server'" class="flex items-center gap-1.5 pb-2 text-[11px] text-gray-500">
        <input v-model="onlyFailed" type="checkbox" class="" />
        只看失败
      </label>
      <label v-else-if="tab === 'task'" class="ml-auto flex items-center gap-1.5 pb-2 text-[11px] text-gray-500">
        <input v-model="onlyFailed" type="checkbox" class="" />
        只看失败
      </label>
      <label v-else class="ml-auto flex items-center gap-1.5 pb-2 text-[11px] text-gray-500">
        <input v-model="onlyFailed" type="checkbox" class="" />
        只看失败
      </label>
    </div>

    <!-- 运行日志（后端 + 前端上报，同一时间轴） -->
    <div v-if="tab === 'server'" class="overflow-hidden rounded-[var(--ys-radius-block)] border border-[var(--ys-border)] bg-white">
      <!-- 写盘/轮转失败：从 /api/logs 的 disk_error 透出，不能只在导出包里才看得见 -->
      <p v-if="serverDiskError" class="border-b border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
        日志写盘失败：{{ serverDiskError }}（内存里的记录不受影响；请检查设置里的日志存储目录）
      </p>
      <!-- 筛选条：级别 / 来源 / 关键字。2000 条缓冲里找一条失败记录，
           没有筛选等于没有日志。筛选只作用于已加载的最近 500 条（见页脚说明）。 -->
      <div class="flex flex-wrap items-center gap-2 border-b border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-3 py-2">
        <input
          v-model="logQuery"
          type="search"
          placeholder="搜索日志内容 / 来源 / 时间…"
          class="min-w-[180px] flex-1 rounded-[6px] border border-[var(--ys-border)] bg-white px-2 py-1 text-[12px] text-[var(--ys-text)] outline-none focus:border-[var(--ys-accent)]"
        />
        <select
          v-model="levelFilter"
          class="rounded-[6px] border border-[var(--ys-border)] bg-white px-2 py-1 text-[12px] text-[var(--ys-text)]"
        >
          <option v-for="o in levelOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <select
          v-model="sourceFilter"
          class="rounded-[6px] border border-[var(--ys-border)] bg-white px-2 py-1 text-[12px] text-[var(--ys-text)]"
        >
          <option v-for="o in sourceOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <button
          v-if="filterActive"
          class="rounded-[6px] px-2 py-1 text-[12px] text-[var(--ys-text-muted)] hover:bg-black/[0.03] hover:text-[var(--ys-text)]"
          @click="resetFilters"
        >清除筛选</button>
      </div>
      <p v-if="!serverEntries.length" class="m-3 rounded-xl border border-dashed border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] py-12 text-center text-[12px] text-gray-500">
        还没有日志 —— 后端启动信息、请求与报错都会出现在这里
      </p>
      <p v-else-if="!visibleServer.length" class="m-3 rounded-xl border border-dashed border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] py-12 text-center text-[12px] text-gray-500">
        当前筛选条件下没有匹配的日志 —— 试试放宽级别 / 来源，或清空搜索词
      </p>
      <template v-else>
        <div class="max-h-[620px] divide-y divide-[var(--ys-border)] overflow-y-auto">
          <div v-for="(e, i) in visibleServer" :key="i" class="flex items-start gap-3 px-4 py-2">
            <!-- 显示浏览器本地时间（含日期），不再直显 NAS 的 UTC 墙钟。
                 title 里保留后端原始 RFC3339，便于与导出包逐行对照。 -->
            <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-500" :title="e.time || e.at">{{ serverTime(e) }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="levelClass(e.level)"
            >{{ levelLabel(e.level) }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px]"
              :class="sourceClass(e.source)"
              :title="e.source === 'frontend' ? '由网页端上报（你刚才的操作）' : '服务端自身产生'"
            >{{ sourceText(e.source) }}</span>
            <span
              class="min-w-0 flex-1 break-all font-mono text-[12px] leading-relaxed"
              :class="e.level === 'error' ? 'text-rose-700' : (e.level === 'warn' ? 'text-amber-700' : 'text-gray-700')"
            >{{ e.message }}</span>
          </div>
        </div>
        <p class="border-t border-[var(--ys-border)] px-4 py-2 text-[11px] text-gray-500">
          显示 {{ visibleServer.length }} / 已加载 {{ serverEntries.length }} 条 · 缓冲共 {{ serverTotal }} 条 ·
          <span v-if="filterActive" class="text-amber-700">筛选只在已加载的最近 500 条内进行，更早的匹配请用「导出」查全量 · </span>
          <span v-if="serverLogFile" class="break-all text-gray-500">
            落盘 {{ serverLogFile }}（按 20MB 轮转，重启不清空；导出包内含当前与上一代文件）
          </span>
          <span v-else>未开启落盘：只保留在内存里，重启应用即清空（可在设置里打开）</span>
          · <span class="text-gray-500">「清空」只清内存视图，不删除落盘文件</span>
          · <span class="text-gray-500">时间为浏览器本地时区，悬停可看原始时间戳</span>
        </p>
      </template>
    </div>

    <!-- 任务日志 -->
    <div v-else-if="tab === 'task'" class="overflow-hidden rounded-[var(--ys-radius-block)] border border-[var(--ys-border)] bg-white">
      <p v-if="!visibleTaskRuns.length" class="m-3 rounded-xl border border-dashed border-[var(--ys-border)] bg-black/[0.015] py-12 text-center text-[12px] text-[var(--ys-text-aux)]">
        {{ onlyFailed ? '没有失败的任务记录' : '还没有任务执行记录 —— 下载、整理、推送每跑一次都会记在这里' }}
      </p>
      <div v-else class="max-h-[620px] divide-y divide-[var(--ys-border)] overflow-y-auto">
        <div v-for="(r, i) in visibleTaskRuns" :key="i" class="flex flex-wrap items-start gap-3 px-4 py-2.5">
          <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-500">{{ r.at }}</span>
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
    <div v-else class="overflow-hidden rounded-[var(--ys-radius-block)] border border-[var(--ys-border)] bg-white">
      <p v-if="!visibleLocal.length" class="m-3 rounded-xl border border-dashed border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] py-12 text-center text-[12px] text-gray-500">
        {{ onlyFailed ? '没有失败的操作记录' : '还没有操作记录 —— 播放、搜索、下载、保存设置等操作都会记在这里；出错时附带完整信息' }}
      </p>
      <template v-else>
        <div class="max-h-[620px] divide-y divide-[var(--ys-border)] overflow-y-auto">
          <div v-for="(e, i) in visibleLocal" :key="i" class="flex items-start gap-3 px-4 py-2">
            <span class="shrink-0 pt-0.5 font-mono text-[11px] text-gray-500" :title="e.time || e.at">{{ localTime(e) }}</span>
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="levelClass(e.level)"
            >{{ levelLabel(e.level) }}</span>
            <span v-if="e.scope" class="shrink-0 rounded-full bg-[var(--ys-surface-sunken)] px-2 py-0.5 text-[10px] text-gray-600">
              {{ e.scope }}
            </span>
            <span
              class="min-w-0 flex-1 break-all font-mono text-[12px] leading-relaxed"
              :class="e.level === 'error' ? 'text-rose-700' : (e.level === 'warn' ? 'text-amber-700' : 'text-gray-700')"
            >{{ e.message }}</span>
          </div>
        </div>
        <p class="border-t border-[var(--ys-border)] px-4 py-2 text-[11px] text-gray-500">
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
import { tsOfServer, fmtLocalDateTime, fmtServerDateTime, entryTimeText } from '../services/logTime'
import { LEVEL_OPTIONS, collectSources, matchesQuery, serverEntryMatches, sourceText } from '../services/logFilter'
import { fail as failText } from '../services/userMsg'
import { fail as notifyFail } from '../services/feedback'
import PageShell from './PageShell.vue'

const tab = ref('server')
const loading = ref(false)
const autoRefresh = ref(true)
const onlyFailed = ref(false)
const logQuery = ref('')
const levelFilter = ref('all')
const sourceFilter = ref('all')
const exporting = ref(false)
// 清空的两步确认（见模板）：以前这里还有一个 window.confirm()，是全项目最后一处
// 原生弹窗——已经统一成「就在原地再点一次」。
const clearArmed = ref(false)

const serverEntries = ref([])
const serverTotal = ref(0)
// 落盘文件路径（设置里配置了「日志存储位置」时非空）——页脚提示日志同时写到了哪里
const serverLogFile = ref('')
// 最近一次写盘/轮转失败原因（后端 GET /api/logs 的 disk_error），空串 = 正常
const serverDiskError = ref('')
const taskRuns = ref([])
const localEntries = ref([])

const tabs = computed(() => [
  { id: 'server', name: '运行日志', count: serverTotal.value || '' },
  { id: 'task', name: '任务日志', count: taskRuns.value.length || '' },
  { id: 'local', name: '操作日志', count: localEntries.value.length || '' },
])

const visibleTaskRuns = computed(() => taskRuns.value.filter((r) =>
  (!onlyFailed.value || !r.ok) && matchesQuery(logQuery.value, r.name, r.kind, r.detail, r.at),
))

// 本地留存是时间正序，界面按「新的在上」显示
const visibleLocal = computed(() => {
  const list = localEntries.value.slice().reverse()
  return list.filter((e) => (!onlyFailed.value || e.level === 'error')
    && matchesQuery(logQuery.value, e.message, e.scope, e.level))
})

const levelOptions = LEVEL_OPTIONS
// 来源选项跟着实际数据走（后端还有 api / nas / library / downloader 等来源）
const sourceOptions = computed(() => collectSources(serverEntries.value))
const filterActive = computed(() =>
  logQuery.value.trim() !== '' || levelFilter.value !== 'all' || sourceFilter.value !== 'all' || onlyFailed.value,
)
function resetFilters() {
  logQuery.value = ''
  levelFilter.value = 'all'
  sourceFilter.value = 'all'
  onlyFailed.value = false
}

function levelLabel(l) {
  if (l === 'error') return '错误'
  if (l === 'warn') return '警告'
  return '信息'
}

function levelClass(l) {
  if (l === 'error') return 'bg-rose-50 text-rose-600'
  if (l === 'warn') return 'bg-amber-50 text-amber-700'
  return 'bg-[var(--ys-surface-sunken)] text-gray-500'
}

/** 日志来源显示名（backend / frontend 之外还有 api / nas / library 等） */
function sourceClass(s) {
  return s === 'frontend' ? 'bg-blue-50 text-blue-600' : 'bg-[var(--ys-surface-sunken)] text-gray-500'
}

/** 把秒级 / 毫秒级时间戳或时间字符串统一成毫秒数，便于排序（见 services/logTime.js） */
function fmtTime(v) {
  // 任务日志两列并排，时间戳保持紧凑的「日期 + 时分秒」；无法解析时回退原文。
  // 保留这个包装是为了让「展示格式」在组件内只定义一处；任务日志的两组来源
  // 都走 fmtServerDateTime（旧 monitor 记录是 naive UTC 墙钟，push 是 Unix 秒）。
  return fmtServerDateTime(v)
}

// 服务端新条目带 RFC3339 的 time（含时区），必须显示浏览器本地时间，
// 不能直显 NAS 的 UTC 墙钟（差 8 小时）。at 仅是没有新字段的旧数据兜底。
function serverTime(e) {
  return entryTimeText(e)
}

// 操作日志同理：新条目带 time（见 services/appLog.js 的 stampISO）→ 完整本地
// 日期时间；旧留存只有 "MM-DD HH:MM:SS"（浏览器本地墙钟、无年份）→ 原样显示。
function localTime(e) {
  return entryTimeText(e)
}

const visibleServer = computed(() => serverEntries.value.filter((e) =>
  serverEntryMatches(e, {
    level: levelFilter.value,
    source: sourceFilter.value,
    onlyFailed: onlyFailed.value,
    query: logQuery.value,
  }),
))

async function loadServer() {
  try {
    const res = await LogsAPI.list(500)
    // 后端按时间正序返回（旧→新）。2026-10-26 用户口径：日志页「新的在左上方」
    // —— 倒过来显示，最新的永远在第一行，不用往下滚找最后一条。
    serverEntries.value = (res?.data?.entries || []).slice().reverse()
    serverTotal.value = res?.data?.total ?? serverEntries.value.length
    serverLogFile.value = res?.data?.log_file || ''
    serverDiskError.value = res?.data?.disk_error || ''
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
      // 旧运行记录是不带 offset 的 UTC 墙钟（见 services/logTime.js 的 tsOfServer），
      // 必须按 UTC 解析再换算本地，否则北京用户看到的时间比实际早 8 小时。
      out.push({
        ts: tsOfServer(r.started_at),
        at: fmtServerDateTime(r.started_at),
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
      // push 的 started_at 是 Unix 秒（绝对时刻明确），走通用的 fmtTime 即可；
      // 但旧数据万一混进来的是 naive ISO，fmtServerDateTime 按 UTC 解析也不会错。
      out.push({
        ts: tsOfServer(r.started_at),
        at: fmtServerDateTime(r.started_at),
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
