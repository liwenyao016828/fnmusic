/**
 * 导航总线：共享的「当前视图」状态 + 跳转请求。
 *
 * 2026-10-15 引入时它只是个 pub-sub（currentView 归 App.vue 持有）。
 * 2026-10-17 导航框架重做后，**当前视图本身也搬进来共享**：
 *   · 桌面侧栏、手机底部标签栏、SwipePager、各视图的「stale 就刷新」watch
 *     都要读同一个 currentView —— 放在 App.vue 里就得层层 props/emit；
 *   · 任何组件调 `goView('download', 'queue')` 就能切页，不必知道壳的结构。
 *
 * ⚠️ 只用于「跳转到某个顶层视图」这一件事，不要拿它当通用事件总线。
 */
import { ref, computed } from 'vue'
import { resolveRoute, VIEW_IDS, defaultSegment, segmentsOf } from './navViews.js'
import { warn as feedbackWarn } from './feedback.js'

/** 当前顶层视图 id（VIEW_IDS 之一） */
export const currentView = ref('search')

/** 各视图的当前分段：viewId → segmentId（无分段的视图不进这个表） */
export const segState = ref({})

const listeners = new Set()

/** 某视图当前应显示的分段（没登记过就用默认段；无分段视图返回空串） */
export function segmentOf(viewId) {
  const segs = segmentsOf(viewId)
  if (!segs.length) return ''
  const cur = segState.value[viewId]
  return segs.some((s) => s.id === cur) ? cur : defaultSegment(viewId)
}

/**
 * 切到某个顶层视图（可选指定分段）。
 *
 * 认不出的 id 回落到发现页并弹一条 warn —— 漏登记的深链要**看得见**，
 * 而不是静默跳走让用户以为按钮坏了。
 *
 * @param {string} id 视图 id（别名也行，见 navViews.js 的 ALIASES）
 * @param {string} [segment] 分段 id
 */
export function goView(id, segment) {
  const route = resolveRoute(id, segment)
  if (route.unknown) {
    feedbackWarn(`未知的导航目标「${id}」，已回到发现页`)
  }
  currentView.value = route.view
  if (route.segment) segState.value = { ...segState.value, [route.view]: route.segment }
  return route
}

/**
 * 订阅导航请求（payload 监听）。
 *
 * 少数跳转要带数据过去（榜单管理的「▶ 看一眼这张榜单」→ 发现页打开那张榜单），
 * 数据不放进共享状态（一次性、只给目标页），仍走订阅表。
 * ⚠️ 视图切换本身**不再**依赖订阅者 —— goView 直接改 currentView，
 * 订阅者只负责消费 payload。
 *
 * @param {(view: string, payload?: any) => void} fn
 * @returns {() => void} 取消订阅
 */
export function onNavigate(fn) {
  if (typeof fn !== 'function') return () => {}
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/**
 * 请求跳转到某个顶层视图，可携带一次性 payload。
 *
 * 等价于 goView(view) + 把 (view, payload) 广播给 onNavigate 订阅者。
 * payload 可以是分段 id 字符串（如 navigateTo('download', 'queue')），
 * 也可以是对象（如 { chartPreview }）—— 订阅者自己判断。
 */
export function navigateTo(view, payload) {
  if (!view) return
  // payload 直接给的是已知分段 id 时，当作分段跳转（老调用形态 navigateTo('download','queue')）
  const seg = typeof payload === 'string' && segmentsOf(view).some((s) => s.id === payload)
    ? payload
    : undefined
  goView(view, seg)
  for (const fn of listeners) {
    try {
      fn(view, payload)
    } catch (_) {
      // 单个订阅者出错不应影响其他订阅者
    }
  }
}

/** 当前视图是不是某 id（模板里高频用，省一层比较） */
export function isCurrentView(id) {
  return currentView.value === id
}

/** 供测试：把共享状态复位（生产代码不要用） */
export function __resetNav() {
  currentView.value = 'search'
  segState.value = {}
  listeners.clear()
}

export const NAV_VIEW_IDS = VIEW_IDS

/** 壳（App.vue）用的派生状态：当前分段 */
export const currentSegment = computed(() => segmentOf(currentView.value))
