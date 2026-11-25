/**
 * 「常驻修复进度」的事件总线（v2.37.0）。
 *
 * 进度条（components/TidyProgress.vue）挂在页头，跑扫描的是各子页、换版本的是抽屉和
 * 本地曲库页 —— 三者没有父子关系，用事件比把状态吊到 App 再透传下去干净。
 *
 * 为什么组件不自己算：进度读的是后端落盘快照（repair_scan.json / quality_scan.json /
 * library_index.json），前端只负责「什么时候该重新读一次」。
 */
export const TIDY_PROGRESS_EVENT = 'ys:tidy-progress-changed'

/**
 * 通知进度条重新读一次。
 *
 * 何时调用：任何让它背后的快照发生变化的动作之后 —— 修复清单扫描结束、
 * 音质扫描结束、换版本成功、补全写完标签。多调无害（一次性 GET，秒回）。
 */
export function notifyTidyProgress() {
  try {
    window.dispatchEvent(new CustomEvent(TIDY_PROGRESS_EVENT))
  } catch (_) {
    // 非浏览器环境（单测）静默：进度条本来就只是展示。
  }
}