/* 后台下载队列（`backend/pkg/backenddl`）—— 前端唯一队列。
 *
 * 架构（2026-10-23 用户最终口径）：**只有这一套下载队列**。
 * 所有「下载」按钮都只是往 NAS 后端的持久队列里提交任务；取链、下载、落盘、
 * 重试全部由 Go worker 执行。页面关掉、浏览器退出都不会中断。
 * 前端**不保留**浏览器取链/落盘的备用执行路径，这里只负责：
 *   · 入队（submit）
 *   · 展示（queue）
 *   · 控制（pause / resume / stop / retry / remove / clear_*）
 *
 * # 对账
 *
 * 唯一对账键是 `client_id`（前端生成，或旧本地任务迁移时沿用原 id）。
 * 后端按它幂等：同一 client_id 重复提交返回原任务确认，不会重复下载。
 * **绝不能用 name+singer 对回** —— 同名 Live/翻唱/不同音质会串版本。
 *
 * # 旧后端
 *
 * 路由不存在时是 404。这里归一成 `available=false` + 原因，入口显示不可用，
 * 而不是假装就绪、点下去报一个看不懂的错。
 */
import { computed, ref } from 'vue'
import { BackendDownloadAPI } from '../api/client.js'

export const BACKEND_STATUS_META = {
  pending: { label: '排队中', tone: 'text-gray-500 bg-gray-100' },
  paused: { label: '已暂停', tone: 'text-amber-600 bg-amber-50' },
  downloading: { label: '下载中', tone: 'text-emerald-600 bg-emerald-50' },
  success: { label: '已完成', tone: 'text-emerald-600 bg-emerald-50' },
  failed: { label: '失败', tone: 'text-rose-600 bg-rose-50' },
  stopped: { label: '已停止', tone: 'text-gray-500 bg-gray-100' },
}

export function backendStatusMeta(status) {
  return BACKEND_STATUS_META[status] || { label: status || '未知', tone: 'text-gray-500 bg-gray-100' }
}

export const BACKEND_CAPABILITY_NOTE =
  '下载由 NAS 后台执行，关掉本页也会继续。后台从服务端渠道取音源；' +
  '只在浏览器里存在的私有音源（blob:、需要页面登录态或本地缓存才能取到的链接）无法在后台取到。'

// ── 纯函数（node --test 覆盖，见 backendDownload.test.mjs） ──

/** 拆掉本项目后端的 { code, message, data } 外壳；业务码非 200 当错误抛。 */
export function unwrap(res) {
  let body = res
  if (body && typeof body === 'object' && !Array.isArray(body) && 'code' in body) {
    const code = Number(body.code)
    if (Number.isFinite(code) && code !== 200) {
      const err = new Error(String(body.message || body.error || `后端返回 ${code}`))
      err.backendCode = code
      throw err
    }
    body = body.data !== undefined ? body.data : body
  }
  return body
}

/** 后台任务（snake_case）→ 界面结构。 */
export function normalizeBackendTask(it) {
  const o = it || {}
  return {
    id: String(o.id || ''),
    clientId: String(o.client_id || o.clientId || ''),
    name: o.name || '',
    singer: o.singer || '',
    album: o.album || '',
    duration: Number(o.duration || 0),
    cover: o.cover || '',
    platform: o.platform || '',
    platformId: o.platform_id || o.platformId || '',
    quality: o.quality || '',
    status: String(o.status || 'pending').toLowerCase(),
    error: o.error || '',
    path: o.path || '',
    actualQuality: o.actual_quality || o.actualQuality || '',
    source: o.source || '',
    size: Number(o.size || 0),
    tries: Number(o.tries || 0),
    addedAt: Number(o.added_at || o.addedAt || 0),
    updatedAt: Number(o.updated_at || o.updatedAt || 0),
  }
}

function emptyStats() {
  return { pending: 0, downloading: 0, paused: 0, success: 0, failed: 0, stopped: 0, total: 0 }
}

/** 队列响应归一：接受 {items}、{tasks}、{list}、裸数组；统计缺了按列表现算。 */
export function normalizeQueue(res) {
  const body = unwrap(res)
  const raw = Array.isArray(body) ? body : (body?.items || body?.tasks || body?.list || [])
  const items = (Array.isArray(raw) ? raw : []).map(normalizeBackendTask)
  const s = body && !Array.isArray(body) ? (body.stats || body.summary || null) : null
  const stats = emptyStats()
  for (const key of Object.keys(stats)) {
    if (key !== 'total' && s && s[key] != null) stats[key] = Number(s[key]) || 0
  }
  if (!s) {
    for (const i of items) {
      if (stats[i.status] != null) stats[i.status]++
    }
  }
  stats.total = Number(s?.total != null ? s.total : items.length) || 0
  return { items, stats, paused: !!(body && !Array.isArray(body) && body.paused) }
}

/**
 * 能力探测归一（GET /download/backend/status）。
 *
 * 后端字段是 `ready`（**能不能真的下**），不是「接口通不通」：
 * 队列读得出来 ≠ 有渠道取音源。ready=false 时界面必须禁用提交并说明原因，
 * 否则任务排进去会一直 pending 到超时。
 */
export function normalizeStatus(res) {
  const body = unwrap(res) || {}
  const sub = (body.subscriptions && typeof body.subscriptions === 'object') ? body.subscriptions : {}
  // 订阅托管同时接受嵌套（后端 handler 的写法）与扁平（早先的草案字段）
  const managed = sub.managed !== undefined ? !!sub.managed : !!body.subscriptions_managed
  return {
    ready: body.ready === undefined ? (body.available === undefined ? true : !!body.available) : !!body.ready,
    reason: String(body.reason || ''),
    paused: !!body.paused,
    // optional=true 的是「有更好、没有也能下」的渠道（目前只有服务端洛雪宿主）。
    // 下载页只用必需渠道判断能力，可选渠道的状态归音源管理页展示。
    channels: Array.isArray(body.channels)
      ? body.channels.map(c => ({ ...c, optional: !!c?.optional }))
      : [],
    lxHost: String(body.lx_host ?? body.lxHost ?? ''),
    dir: String(body.dir ?? body.download_dir ?? ''),
    managed,
    subscriptionsReason: String(sub.reason ?? body.subscriptions_reason ?? ''),
  }
}

/**
 * 提交响应归一。契约是逐条回执：
 *   { added:[{client_id, id}], skipped:[{client_id, reason}], stats }
 * 只回计数、没有 client_id 的响应不能猜「全都收下」（那会删掉用户任务），
 * `unmapped=true` 时调用方必须全部保留。
 */
export function normalizeSubmit(res) {
  const body = unwrap(res)
  const o = Array.isArray(body) ? { added: body } : (body || {})
  const list = (v) => {
    if (!Array.isArray(v)) return []
    return v.map(x => (typeof x === 'string' ? { client_id: x } : (x || {})))
      .filter(x => x && x.client_id)
  }
  const added = list(o.added ?? o.accepted ?? o.items_accepted)
  const skipped = list(o.skipped ?? o.rejected ?? o.items_skipped).map(x => ({
    client_id: x.client_id,
    reason: String(x.reason || x.code || 'skipped'),
    message: x.message || '',
  }))
  const addedCount = Number(o.added_count ?? (Array.isArray(o.added) ? null : o.added) ?? added.length) || 0
  const skippedCount = Number(o.skipped_count ?? (Array.isArray(o.skipped) ? null : o.skipped) ?? skipped.length) || 0
  return { added, skipped, addedCount, skippedCount, unmapped: !added.length && addedCount > 0 }
}

/** 错误 → 人话 + 机器可判的 code。 */
export function describeBackendError(err) {
  const status = Number(err?.response?.status || 0)
  if (status === 404 || status === 405 || status === 501) {
    return { code: 'not_ready', message: '当前后端版本没有后台下载接口' }
  }
  if (status === 401 || status === 403) {
    return { code: 'denied', message: '后端拒绝了这个请求（权限或登录态问题）' }
  }
  if (status >= 500) {
    const m = err?.response?.data?.message || err?.response?.data?.error
    return { code: 'server', message: m ? `后端出错：${m}` : '后端处理失败（服务端错误）' }
  }
  if (err?.response) {
    const d = err.response.data || {}
    return { code: 'server', message: String(d.message || d.error || err.message || '后台请求失败') }
  }
  if (err?.request || err?.code === 'ECONNABORTED' || err?.code === 'ETIMEDOUT') {
    return { code: 'network', message: '连不上后端（NAS 可能正在重启或网络中断）' }
  }
  return { code: 'unknown', message: String(err?.message || '后台请求失败') }
}

/** 交接/入队结果 → 一句给用户的话。 */
export function describeHandoffResult(out) {
  const o = out || {}
  const parts = []
  if (o.added) parts.push(`已加入后台 ${o.added} 首`)
  if (o.duplicate) parts.push(`${o.duplicate} 首后台已有（未重复下载）`)
  if (o.skipped) parts.push(`${o.skipped} 首后台没有接收${o.skippedReason ? `：${o.skippedReason}` : ''}`)
  if (!parts.length && o.eligible) parts.push('后台没有确认接收')
  return parts.join('；') || '没有可提交的任务'
}

// ── 状态 ──
//
// 两个「能不能用」要分开：
//   reachable  接口在不在（老后端 404 → false）
//   ready      有没有能力真的下载（宿主/渠道；后端 status.ready）
// 只有两者都成立才允许提交 —— 队列读得出来但没渠道时，排进去只会 pending 到超时。
const reachable = ref(null)        // 接口在不在（旧后端 404 → false）；导出别名 available
const ready = ref(null)            // 能不能真的下（status.ready）
const unavailableReason = ref('')
const channels = ref([])           // [{id,enabled,ready,reason}] 给用户看「哪条渠道能用」
const lxHost = ref('')             // 'off' | 'starting' | 'ready' | ''
// 后台队列**实际**落盘目录（后端 status.dir）。只读：队列页不再提供改目录入口，
// 入口统一在「系统 -> 设置 -> 存储目录」。空串 = 老后端没这个字段，界面回退本地记忆值。
const backendDir = ref('')
const subscriptionsManaged = ref(false)
const subscriptionsReason = ref('')
const paused = ref(false)          // 整队暂停（后端 pause/resume 不带 ids）
const items = ref([])
const stats = ref({ pending: 0, downloading: 0, paused: 0, stopped: 0, success: 0, failed: 0, total: 0 })
const loading = ref(false)
const busy = ref(false)
const errorText = ref('')
const errorCode = ref('')
const lastSyncAt = ref(0)

const totalCount = computed(() => stats.value.total)
const pendingCount = computed(() => stats.value.pending)
const downloadingCount = computed(() => stats.value.downloading)
const completedCount = computed(() => stats.value.success)
const failedCount = computed(() => stats.value.failed)
const stoppedCount = computed(() => stats.value.stopped)
const activeCount = computed(() => stats.value.pending + stats.value.downloading)
const usable = computed(() => reachable.value === true && ready.value === true)
// 洛雪宿主没开、只剩平台直链时，音质/音源覆盖会明显变窄：界面要给一句提示，
// 别让用户以为「后台能下」就等于「和浏览器一样能下」。
// 2026-10-25：原本这里有 lxWeakHint（「就绪但宿主没开」→ 下载页提示「洛雪宿主未开启」）。
// 用户反馈「下载界面显示落雪宿主未开启，检查有问题」—— 后台下载并不需要宿主，
// 拿它当提示等于把可选音源能力说成下载故障。宿主状态改在音源管理页展示，
// 这里不再产生任何提示。

// ── 请求 ──

function markUnreachable(err) {
  const d = describeBackendError(err)
  if (d.code === 'not_ready') {
    reachable.value = false
    ready.value = false
    channels.value = []
    lxHost.value = ''
  }
  if (!ready.value) {
    subscriptionsManaged.value = false
    subscriptionsReason.value = unavailableReason.value || d.message
  }
  unavailableReason.value = d.message
  errorCode.value = d.code
  errorText.value = d.message
}

/** 探测能力。返回「现在能不能真的下」。 */
async function checkStatus() {
  try {
    const st = normalizeStatus(await BackendDownloadAPI.status())
    reachable.value = true
    ready.value = st.ready
    paused.value = st.paused
    channels.value = st.channels
    lxHost.value = st.lxHost
    backendDir.value = st.dir || ''
    subscriptionsManaged.value = st.managed
    subscriptionsReason.value = st.subscriptionsReason || (st.managed ? '' : st.reason)
    unavailableReason.value = st.ready ? '' : (st.reason || '后台下载不可用（后端没有可用取音渠道）')
    errorText.value = ''
    errorCode.value = ''
    return st.ready
  } catch (e) {
    markUnreachable(e)
    return false
  }
}

/** 读队列（后台的唯一真相）。失败不把已有列表清空，免得界面空一下又跳回来。 */
async function refreshQueue() {
  loading.value = true
  try {
    const res = normalizeQueue(await BackendDownloadAPI.queue())
    items.value = res.items
    stats.value = res.stats
    paused.value = res.paused
    reachable.value = true
    errorText.value = ''
    errorCode.value = ''
    lastSyncAt.value = Date.now()
    return res
  } catch (e) {
    markUnreachable(e)
    return null
  } finally {
    loading.value = false
  }
}

function throwUser(e) {
  const err = describeBackendError(e)
  const out = new Error(err.message)
  out.userMessage = err.message
  out.code = err.code
  if (err.code === 'not_ready') {
    reachable.value = false
    ready.value = false
  }
  unavailableReason.value = err.message
  throw out
}

/**
 * 把浏览器任务提交给后台（**只发请求，不改前端队列**）。
 *
 * 返回逐条回执 `{added:[{client_id}], skipped:[{client_id,reason}]}`；
 * 状态改写由 downloadManager 统一负责 —— 「提交失败不丢原任务」是结构保证：
 * 这里抛错时，调用方还没有删任何本地占位。
 */
async function submitItems(payload, opts = {}) {
  const list = Array.isArray(payload) ? payload : []
  if (!list.length) return { added: [], skipped: [], addedCount: 0, skippedCount: 0, unmapped: false }
  busy.value = true
  try {
    return normalizeSubmit(await BackendDownloadAPI.submit(list, opts))
  } catch (e) {
    throwUser(e)
  } finally {
    busy.value = false
  }
}

/**
 * 对后台任务做动作：pause / resume / stop / cancel / retry / retry_failed /
 * remove / clear_done / clear_all。后端自己决定「不传 ids」时的默认范围，
 * 前端不猜（整队 pause 与单条 pause 是同一个 action）。
 */
async function act(action, ids = []) {
  busy.value = true
  try {
    const body = unwrap(await BackendDownloadAPI.action(action, ids))
    if (body && typeof body === 'object') {
      if (body.stats) stats.value = { ...stats.value, ...body.stats }
      if (typeof body.paused === 'boolean') paused.value = body.paused
    }
    await refreshQueue()
    // hit 是后端实际动的条数；没有 hit 的旧后端返回 1/0 让调用方仍能给出反馈。
    return Number.isFinite(Number(body?.hit)) ? Number(body.hit) : (body ? 1 : 0)
  } catch (e) {
    throwUser(e)
  } finally {
    busy.value = false
  }
}

/** 整队暂停/继续（后端 pause/resume 不带 ids 即整队） */
async function togglePause() {
  return act(paused.value ? 'resume' : 'pause')
}

// ── 轮询：有任务在跑就快一点，闲着就慢一点；页面隐藏时不打空转请求 ──
//
// 引用计数：DownloadQueuePanel 同时挂在抽屉、完整队列页、曲库管家里，
// 多个实例共用一个定时器，谁最后卸载谁停。
let timer = null
let subscribers = 0
let tickCount = 0
let unbind = null

function isHidden() {
  try { return typeof document !== 'undefined' && document.visibilityState === 'hidden' } catch (_) { return false }
}

function clearTimer() {
  if (timer) { clearTimeout(timer); timer = null }
}

function schedule(delay) {
  clearTimer()
  timer = setTimeout(tick, delay)
}

async function tick() {
  timer = null
  if (!subscribers || isHidden()) return
  tickCount++
  // 能力探测不必每轮都问（本地 4s 一次也会刷日志）：不可用或每 5 轮问一次。
  if (reachable.value === false || ready.value === false || tickCount % 5 === 1) {
    await checkStatus()
  }
  if (reachable.value !== false) await refreshQueue()
  if (subscribers) schedule(activeCount.value > 0 ? 4000 : 15000)
}

function onVisible() {
  if (!subscribers || isHidden()) return
  tick()
}

/** 组件挂载时调用（可多处挂载，引用计数只跑一个定时器） */
export function startBackendPolling() {
  subscribers++
  if (subscribers > 1) return
  try {
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisible)
      unbind = () => document.removeEventListener('visibilitychange', onVisible)
    }
  } catch (_) {}
  tick()
}

/** 组件卸载时调用 */
export function stopBackendPolling() {
  subscribers = Math.max(0, subscribers - 1)
  if (subscribers > 0) return
  clearTimer()
  if (unbind) { unbind(); unbind = null }
}

/**
 * 单测用：把状态与轮询计数复位（测试之间互不影响）。
 * 生产代码没有别的调用点，别拿它当「刷新」用。
 */
export function resetBackendDownloadForTests() {
  subscribers = 0
  tickCount = 0
  clearTimer()
  if (unbind) { unbind(); unbind = null }
  reachable.value = null
  ready.value = null
  unavailableReason.value = ''
  channels.value = []
  lxHost.value = ''
  subscriptionsManaged.value = false
  subscriptionsReason.value = ''
  paused.value = false
  items.value = []
  stats.value = { pending: 0, downloading: 0, paused: 0, stopped: 0, success: 0, failed: 0, total: 0 }
  loading.value = false
  busy.value = false
  errorText.value = ''
  errorCode.value = ''
  lastSyncAt.value = 0
}

export const backendDownload = {
  // 能力
  available: reachable,
  reachable,
  ready,
  usable,
  reason: unavailableReason,
  unavailableReason,
  channels,
  lxHost,
  backendDir,
  subscriptionsManaged,
  subscriptionsReason,
  unavailableReason,
  // 队列
  items,
  stats,
  paused,
  loading,
  busy,
  errorText,
  errorCode,
  lastSyncAt,

  totalCount,
  pendingCount,
  downloadingCount,
  completedCount,
  failedCount,
  stoppedCount,
  activeCount,

  start: startBackendPolling,
  stop: stopBackendPolling,
  refreshStatus: checkStatus,
  checkStatus,
  refreshQueue,
  submitItems,
  act,
  togglePause,

  retry: (id) => act('retry', [id]),
  stopTask: (id) => act('stop', [id]),
  remove: (ids) => act('remove', ids),
  retryFailed: () => act('retry_failed'),
  clearDone: () => act('clear_done'),
  clearAll: () => act('clear_all'),
}
