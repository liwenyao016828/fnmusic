/**
 * 榜单注入 / 页面注入 的共享状态（模块级单例）。
 *
 * 为什么要单独成模块：这两个开关不止一处要用 —— 「发现音乐 → 排行榜单」的卡片上要显示
 * 「已注入」、新加的「榜单管理」页要勾选、以后的双向切换按钮也要问同一个问题。
 * 各组件各存一份副本，就会出现「这边改了、那边还显示旧的」。
 *
 * 写回策略：**乐观更新 + latest-wins 排队**
 * - 勾选是高频小动作，等一个来回才变色会显得卡，所以先改本地、再发请求。
 * - 写的时候再点不丢：记下「最终想写成的样子」，这一轮回来再补一次。
 *   （旧实现在 busy 期间直接 return，快速连点会静默丢掉后面几下 —— 批量按钮上很致命。）
 * - 失败**不猜**：重新拉一次服务端配置以它为准。乐观更新已经改过本地了，
 *   想「回滚到刚才的样子」需要一个真相，而本地那份已经不是真相。
 */
import { ref } from 'vue'
import { ChartInjectAPI } from '../api/client'
import { createWriteQueue } from './writeQueue'

const ids = ref([])         // 已勾选的榜单，元素形如 `wy_3778678`
const enabled = ref(true)   // 榜单注入总开关（关掉**不丢**勾选）
const uiInject = ref(true)  // 页面注入（飞牛自带音乐页里曲率补的入口）
const busy = ref(false)     // 有写请求在飞（按钮禁用 + 转圈）
const error = ref('')
const savedAt = ref(0)      // 最近一次写成功的时刻（ms）
const loading = ref(false)

let attempted = false       // 读过一次就够了；读失败时也不该每次切页都重打（重试走 force）
let reading = null          // 读请求去重
let writeEpoch = 0          // 每次写入成功 +1，用来作废「写之前发出的」读响应

function describe(e) {
  return e?.response?.data?.message || e?.message || '未知错误'
}

/**
 * 榜单 → 配置里的键：`<平台>_<榜单 id>`。
 * 与后端虚拟歌单 guid 的后缀同形（`online:playlist:chart:wy_3778678`），
 * 所以配置里存的就是 guid 的后半段，排查时可以直接对上。
 * 榜单详情对象里可能没有 `source`，用当前平台兜底。
 */
export function chartInjectKey(item, fallbackSource = '') {
  const source = String(item?.source || fallbackSource || '').trim().toLowerCase()
  return `${source}_${item?.id ?? ''}`.toLowerCase()
}

/** 清洗：去空白、转小写、去重 —— 与后端 `NormalizeChartPlaylists` 同一套口径 */
export function normalizeIds(list) {
  const out = []
  for (const raw of Array.isArray(list) ? list : []) {
    const key = String(raw ?? '').trim().toLowerCase()
    if (key && !out.includes(key)) out.push(key)
  }
  return out
}

/** 读服务端配置。默认只读一次；`force` 用于「失败后重试」和「写失败后拉回真相」。 */
export async function loadConfig(force = false) {
  if (reading) return reading
  if (attempted && !force) return undefined
  // 正在写的时候不读：读回来的是**写之前**的服务端状态，会把用户刚点的那一下顶回去
  if (busy.value) return undefined
  const startEpoch = writeEpoch
  loading.value = true
  reading = (async () => {
    try {
      const cfg = await ChartInjectAPI.read()
      // 读的这段时间里有写入落地 → 这份响应已经过时，丢掉（否则界面会停在旧勾选上，
      // 而且不会自愈：配置只在切页时读一次）
      if (startEpoch !== writeEpoch) return
      ids.value = normalizeIds(cfg.ids)
      enabled.value = cfg.enabled
      uiInject.value = cfg.uiInject
      error.value = ''
    } catch (e) {
      error.value = '读取配置失败：' + describe(e)
    } finally {
      attempted = true
      loading.value = false
      reading = null
    }
  })()
  return reading
}

/** 真正发请求的动作（队列只管「什么时候写、写什么」，不关心怎么发） */
const writes = createWriteQueue({
  send: async ({ ids: wIds, enabled: wEnabled, ui: wUI }) => {
    // 三个键分开 patch：后端是选择性合并，没出现的键一律不动
    if (wIds !== undefined) await ChartInjectAPI.saveIds(wIds)
    if (wEnabled !== undefined) await ChartInjectAPI.saveEnabled(wEnabled)
    if (wUI !== undefined) await ChartInjectAPI.saveUIInject(wUI)
    writeEpoch++
    savedAt.value = Date.now()
    error.value = ''
  },
  onBusy: (b) => {
    busy.value = b
  },
  onError: async (e) => {
    error.value = '保存失败：' + describe(e)
    await loadConfig(true)   // 以服务端为准
  },
})

export function isInjected(key) {
  return ids.value.includes(key)
}

/** 整体替换勾选名单（批量动作走这里：一次请求写完，不是逐个发） */
export function replaceIds(list) {
  const next = normalizeIds(list)
  ids.value = next
  return writes.ids(next)
}

export function toggleItem(item, fallbackSource = '') {
  const key = chartInjectKey(item, fallbackSource)
  return replaceIds(ids.value.includes(key) ? ids.value.filter(k => k !== key) : [...ids.value, key])
}

export function setEnabled(on) {
  enabled.value = !!on
  return writes.enabled(on)
}

export function setUIInject(on) {
  uiInject.value = !!on
  return writes.ui(on)
}

export const chartInject = {
  ids, enabled, uiInject, busy, error, savedAt, loading,
  load: loadConfig,
  isInjected,
  replaceIds,
  toggleItem,
  setEnabled,
  setUIInject,
}
