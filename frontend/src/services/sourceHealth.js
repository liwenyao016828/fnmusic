/**
 * 音源健康度与熔断器（移植自觅音 miyin 的 failCircuit 机制）
 *
 * 规则（对齐 miyin/server/services/sourceRuntime.ts）：
 *  - 连续失败 MAX_FAILS_BEFORE_BREAK 次 → 该音源熔断 BREAK_MS 毫秒
 *  - 熔断期间该音源被跳过，不再发起请求（避免反复拖慢播放/下载）
 *  - 任意一次成功立即清零失败计数（recordSuccess）
 *  - 冷却时间到期后自动恢复可用（半开：下一次调用即探测）
 *
 * 状态持久化到 localStorage，刷新页面后冷却期依然有效。
 */
import { ref, computed } from 'vue'

const STORAGE_KEY = 'fn_source_health_v1'

// 熔断阈值（与 miyin 保持一致）
export const MAX_FAILS_BEFORE_BREAK = 3
export const BREAK_MS = 60_000

/**
 * @typedef {Object} HealthRecord
 * @property {number} fails     当前连续失败次数
 * @property {number} openUntil 熔断解禁时间戳（0 表示未熔断）
 * @property {number} ok        累计成功次数
 * @property {number} fail      累计失败次数
 * @property {number} lastLatencyMs 最近一次成功耗时
 * @property {string} lastError 最近一次错误信息
 * @property {number} lastOkAt  最近成功时间戳
 * @property {number} lastFailAt 最近失败时间戳
 */

const state = ref({})
let saveTimer = null

function now() {
  return Date.now()
}

function emptyRecord() {
  return {
    fails: 0,
    openUntil: 0,
    ok: 0,
    fail: 0,
    lastLatencyMs: 0,
    lastError: '',
    lastOkAt: 0,
    lastFailAt: 0,
  }
}

function hasStorage() {
  try {
    return typeof localStorage !== 'undefined' && localStorage !== null
  } catch (_) {
    return false
  }
}

function load() {
  if (!hasStorage()) return
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return
    const parsed = JSON.parse(raw)
    const out = {}
    for (const [id, rec] of Object.entries(parsed || {})) {
      if (!rec || typeof rec !== 'object') continue
      out[id] = { ...emptyRecord(), ...rec }
      // 冷却期已过的熔断在启动时顺手关闭
      if (out[id].openUntil && out[id].openUntil <= now()) {
        out[id].openUntil = 0
        out[id].fails = 0
      }
    }
    state.value = out
  } catch (_) {
    state.value = {}
  }
}

function save() {
  if (!hasStorage() || saveTimer) return
  saveTimer = setTimeout(() => {
    saveTimer = null
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state.value))
    } catch (_) {}
  }, 300)
}

function patch(sourceId, mutate) {
  if (!sourceId) return
  const cur = state.value[sourceId] || emptyRecord()
  const next = { ...cur }
  mutate(next)
  // 整体替换以触发 Vue 响应式更新
  state.value = { ...state.value, [sourceId]: next }
  save()
}

function remainingBreakMs(sourceId) {
  const rec = state.value[sourceId]
  if (!rec || !rec.openUntil) return 0
  return Math.max(0, rec.openUntil - now())
}

function isAvailable(sourceId) {
  return remainingBreakMs(sourceId) <= 0
}

function recordSuccess(sourceId, latencyMs = 0) {
  patch(sourceId, rec => {
    rec.fails = 0
    rec.openUntil = 0
    rec.ok += 1
    rec.lastOkAt = now()
    rec.lastError = ''
    if (latencyMs > 0) {
      // 平滑一下，避免单次抖动主导排序
      rec.lastLatencyMs = rec.lastLatencyMs > 0
        ? Math.round(rec.lastLatencyMs * 0.6 + latencyMs * 0.4)
        : Math.round(latencyMs)
    }
  })
}

function recordFailure(sourceId, error) {
  patch(sourceId, rec => {
    rec.fails += 1
    rec.fail += 1
    rec.lastFailAt = now()
    rec.lastError = String(error || '未知错误').slice(0, 200)
    if (rec.fails >= MAX_FAILS_BEFORE_BREAK) {
      rec.openUntil = now() + BREAK_MS
      rec.fails = 0
    }
  })
}

function get(sourceId) {
  return state.value[sourceId] || null
}

function list() {
  return state.value
}

function reset(sourceId) {
  if (!state.value[sourceId]) return
  const next = { ...state.value }
  delete next[sourceId]
  state.value = next
  save()
}

function resetAll() {
  state.value = {}
  save()
}

/** 成功率（无数据时返回 null） */
function successRate(sourceId) {
  const rec = state.value[sourceId]
  if (!rec) return null
  const total = rec.ok + rec.fail
  if (total <= 0) return null
  return rec.ok / total
}

/**
 * 健康分：越大越优先使用。熔断中的音源返回 -1（排最后）。
 */
function score(sourceId) {
  if (!isAvailable(sourceId)) return -1
  const rec = state.value[sourceId]
  if (!rec) return 50 // 未测过的音源给中性分
  const rate = successRate(sourceId)
  const rateScore = (rate == null ? 0.5 : rate) * 100
  const latencyPenalty = rec.lastLatencyMs > 0
    ? Math.min(rec.lastLatencyMs / 100, 30)
    : 15
  const freshness = rec.lastOkAt > rec.lastFailAt ? 10 : 0
  return rateScore - latencyPenalty + freshness
}

/** 按健康度排序（健康优先、熔断置底），stable 保留原始相对顺序 */
function sortIds(ids) {
  return [...(ids || [])]
    .map((id, index) => ({ id, index, s: score(id) }))
    .sort((a, b) => (b.s - a.s) || (a.index - b.index))
    .map(x => x.id)
}

/** 'cooldown' | 'warning' | 'healthy' | 'unknown' */
function statusOf(sourceId) {
  if (!isAvailable(sourceId)) return 'cooldown'
  const rec = state.value[sourceId]
  if (!rec || (rec.ok === 0 && rec.fail === 0)) return 'unknown'
  if (rec.fail > 0 && rec.lastFailAt >= rec.lastOkAt) return 'warning'
  if (rec.ok > 0) return 'healthy'
  return 'unknown'
}

const cooldownIds = computed(() =>
  Object.keys(state.value).filter(id => !isAvailable(id))
)

const cooldownCount = computed(() => cooldownIds.value.length)

/** 供 UI 显示的中文状态文案 */
function statusLabel(sourceId) {
  const st = statusOf(sourceId)
  if (st === 'cooldown') {
    const sec = Math.ceil(remainingBreakMs(sourceId) / 1000)
    return `熔断中 ${sec}s`
  }
  if (st === 'warning') return '近期异常'
  if (st === 'healthy') return '正常'
  return '未检测'
}

load()

export const sourceHealth = {
  state,
  cooldownIds,
  cooldownCount,

  MAX_FAILS_BEFORE_BREAK,
  BREAK_MS,

  isAvailable,
  remainingBreakMs,
  recordSuccess,
  recordFailure,
  get,
  list,
  reset,
  resetAll,
  successRate,
  score,
  sortIds,
  statusOf,
  statusLabel,
}
