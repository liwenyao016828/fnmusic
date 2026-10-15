/**
 * 统一反馈通道（瞬时提示）。
 *
 * 2026-10-15 重构引入。在此之前，界面上的反馈有**四套并存、说法不一**的通道：
 *   1. header 上的销售文案 / 页脚说明（永远可见，占竖向空间）
 *   2. 各页自己的 `InlineNotice`（一次性结果，却留在原地不消失）
 *   3. App.vue 里的单槽位 `appToast`（只有 App.vue 自己能用，页面组件够不着）
 *   4. `alert()`（少数几个老地方）
 * 同一件事（「下载目录保存失败」）在三处各写一遍不同措辞，用户看到的是
 * 「这个应用提示太多、说法还不一致」。
 *
 * 本模块只负责一件事：**「说过就走」的瞬时反馈**。
 * 需要用户看完再操作的（凭据怎么填、为什么要扫码）不用这里，用常驻说明；
 * 需要留在页面上作为状态的（「当前音源：X」）用状态条，不用提示。
 *
 * 用法：
 *   import { ok, fail } from '../services/feedback'
 *   ok('已保存')
 *   fail('下载目录保存失败', { scope: 'download', detail: err })
 *
 * 全部提示由 `components/FeedbackHost.vue` 统一渲染（App.vue 里挂一次）。
 * `fail`/`warn` 带 `scope` 时会顺手写一条应用日志，保持
 * 「界面只给一句结论，完整信息进日志」的既有约定（见 services/appLog.js）。
 */

import { ref } from 'vue'
// 带 .js 后缀：本模块要能在 `node --test` 下直接跑（Node 的 ESM 不做无后缀解析，
// 与 services/userMsg.js:21 的写法一致）
import { logWarn, logError } from './appLog.js'

/** 同时最多显示几条（再多就看不清了，先来先走） */
export const FEEDBACK_MAX_VISIBLE = 3

/** 同一条提示在这个窗口内重复出现时合并，只重置计时（避免按钮连点刷屏） */
export const FEEDBACK_DEDUPE_MS = 2000

/** 列表最多保留多少条（防止无人渲染时无界增长） */
const KEEP_MAX = 12

/** 各等级的默认停留时间（fail 留久一点，用户可能要读完再决定去不去日志） */
const AUTO_DISMISS_MS = { info: 3600, ok: 3600, warn: 5000, fail: 6000 }

/** 当前提示列表（按先后顺序，最新在最后）。模板里用 slice(-FEEDBACK_MAX_VISIBLE) 渲染。 */
export const feedbackItems = ref([])

let seq = 0
/** id → 计时器（0 号表示不自动消失） */
const timers = new Map()

function clearTimer(id) {
  const t = timers.get(id)
  if (t) clearTimeout(t)
  timers.delete(id)
}

function scheduleDismiss(id, ms) {
  clearTimer(id)
  if (!ms || ms <= 0) return
  timers.set(
    id,
    setTimeout(() => dismiss(id), ms),
  )
}

/**
 * 推送一条瞬时提示。
 *
 * @param {string} text 一句人话（不要塞堆栈）
 * @param {object} [opts]
 * @param {'info'|'ok'|'warn'|'fail'} [opts.level='info']
 * @param {boolean} [opts.goLogs] 点击直达「日志」页（fail 默认 true）
 * @param {number} [opts.timeout] 停留毫秒；0 = 不自动消失
 * @param {string} [opts.scope] 日志 scope；warn/fail 给了就顺手写日志
 * @param {any} [opts.detail] 日志详情
 * @returns {number} 提示 id
 */
export function notify(text, opts = {}) {
  const msg = String(text == null ? '' : text).trim()
  if (!msg) return 0

  const level = opts.level || 'info'
  const goLogs = opts.goLogs === undefined ? level === 'fail' : !!opts.goLogs
  const now = Date.now()

  // 合并：同文案同等级且还新鲜 → 只把它的计时器往后推
  const last = feedbackItems.value[feedbackItems.value.length - 1]
  if (last && last.text === msg && last.level === level && now - last.at < FEEDBACK_DEDUPE_MS) {
    last.at = now
    scheduleDismiss(last.id, opts.timeout === undefined ? AUTO_DISMISS_MS[level] : opts.timeout)
    return last.id
  }

  if (opts.scope && (level === 'warn' || level === 'fail')) {
    const log = level === 'fail' ? logError : logWarn
    log(opts.scope, msg, opts.detail)
  }

  const item = { id: ++seq, text: msg, level, goLogs, at: now }
  feedbackItems.value = [...feedbackItems.value, item].slice(-KEEP_MAX)
  scheduleDismiss(item.id, opts.timeout === undefined ? AUTO_DISMISS_MS[level] : opts.timeout)
  return item.id
}

export const info = (text, opts) => notify(text, { ...opts, level: 'info' })
export const ok = (text, opts) => notify(text, { ...opts, level: 'ok' })
export const warn = (text, opts) => notify(text, { ...opts, level: 'warn' })
/** 失败：默认可点进「日志」看详情（老 appToast 的行为，现在人人可用） */
export const fail = (text, opts) => notify(text, { ...opts, level: 'fail' })

export function dismiss(id) {
  clearTimer(id)
  feedbackItems.value = feedbackItems.value.filter((it) => it.id !== id)
}

export function dismissAll() {
  for (const id of [...timers.keys()]) clearTimer(id)
  feedbackItems.value = []
}

/** 便捷对象形态（`feedback.ok(...)`），与具名导出等价 */
export const feedback = { notify, info, ok, warn, fail, dismiss, dismissAll }
