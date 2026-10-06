/**
 * 极简导航总线。
 *
 * 背景：界面上的报错只留一句人话，用户需要能**一键跳到「日志」页**看完整信息。
 * 但错误可能出现在 14 个组件里的任何一个 —— 给每个组件都加
 * `emit('navigate', ...)` + 在 App.vue 里逐个接，样板代码太多（用户明确要求「能少代码就少代码」）。
 *
 * 所以这里用一个模块级的订阅表：App.vue 订阅一次，任何组件直接调 `navigateTo('logs')`。
 * 不改动 `currentView` 的归属（仍由 App.vue 持有），只是给它多一个触发入口。
 *
 * ⚠️ 只用于「跳转到某个顶层视图」这一件事，不要拿它当通用事件总线。
 */

const listeners = new Set()

/**
 * 订阅导航请求。
 * @param {(view: string) => void} fn
 * @returns {() => void} 取消订阅
 */
export function onNavigate(fn) {
  if (typeof fn !== 'function') return () => {}
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** 请求跳转到某个顶层视图（如 'logs' / 'sources' / 'accounts'） */
export function navigateTo(view) {
  if (!view) return
  for (const fn of listeners) {
    try {
      fn(view)
    } catch (_) {
      // 单个订阅者出错不应影响其他订阅者
    }
  }
}
