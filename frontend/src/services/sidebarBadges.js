// 侧栏导航项上的「任务进行中」徽标文案（2026-10-29 用户要求）
//
// 用户原话：「订阅更新中,处理任务的时候,增加停止功能,吧左下角的任务状态中移除.
// 当用户有任务时,将订阅更新替换为出现的问题(精简文字),当有多个时,采用:任务进行ing.」
//
// 也就是说：左下角「任务状态」里不再列订阅相关的条目（见 libraryView.js 的
// sidebarStatusItems），订阅的状态改为**就地显示在左侧导航「订阅更新」那一项上**。
//
// 三档口径：
//   · 没有任务        → 导航项照常写「订阅更新」，不出现徽标
//   · 只有一个任务    → 直接把「出现的问题」精简成几个字挂上去（比如「2 首待推送」）
//   · 有多个任务      → 收敛成一句「任务进行ing」，不逐个罗列（窄侧栏塞不下）
//
// ⚠️ 这里是纯函数，不读 store、不发请求：调用方（App.vue）自己把数传进来。
// 纯函数化是为了能在 node --test 里直接钉住三档文案，不用起浏览器。

/**
 * 把订阅任务的原始计数归纳成一个徽标描述。
 *
 * @param {object} input
 * @param {number} [input.pushRunning]   正在推送的曲目数（0 = 没在推）
 * @param {number} [input.pending]       待推送曲目数
 * @param {number} [input.failed]        推送下载失败数
 * @param {number} [input.missing]       曲库里还没有的曲目数
 * @param {number} [input.pendingBaseline] 还没开始追新的订阅数
 * @returns {{ show: boolean, label: string, count: number, tone: 'progress'|'warn'|'error'|'muted', title: string }}
 *   - show=false 表示导航项保持原名「订阅更新」
 *   - label 是要覆盖在导航项上的短文案
 *   - tone 决定徽标配色（progress 绿 / warn 琥珀 / error 红 / muted 灰）
 *   - title 是鼠标悬停的完整说明（侧栏窄，详细内容靠它）
 */
export function pushNavBadge(input = {}) {
  const running = Math.max(0, Number(input.pushRunning) || 0)
  const pending = Math.max(0, Number(input.pending) || 0)
  const failed = Math.max(0, Number(input.failed) || 0)
  const missing = Math.max(0, Number(input.missing) || 0)
  const baseline = Math.max(0, Number(input.pendingBaseline) || 0)

  // 按「用户现在最该先知道哪件事」排序，不是按数量大小。
  // 失败最优先：它需要人动手，摆在最前面才不会沉在下面。
  const problems = []
  if (failed > 0) problems.push({ kind: 'failed', count: failed, label: `${failed} 首推送失败`, tone: 'error' })
  if (running > 0) problems.push({ kind: 'running', count: running, label: `${running} 首推送中`, tone: 'progress' })
  if (pending > 0) problems.push({ kind: 'pending', count: pending, label: `${pending} 首待推送`, tone: 'warn' })
  if (missing > 0) problems.push({ kind: 'missing', count: missing, label: `${missing} 首缺源`, tone: 'muted' })
  if (baseline > 0) problems.push({ kind: 'baseline', count: baseline, label: `${baseline} 个未追新`, tone: 'warn' })

  if (problems.length === 0) {
    return { show: false, label: '订阅更新', count: 0, tone: 'muted', title: '订阅更新' }
  }

  const total = problems.reduce((s, p) => s + p.count, 0)
  const detail = problems.map((p) => p.label).join(' · ')

  // 多个任务 → 收成一句「任务进行ing」（用户原话里就是这个写法）
  if (problems.length > 1) {
    // 配色跟着「最严重的那个」走：有失败就是红，否则绿
    const tone = problems.some((p) => p.kind === 'failed') ? 'error' : 'progress'
    return { show: true, label: '任务进行ing', count: total, tone, title: detail }
  }

  // 只有一个任务 → 直接显示这个问题的精简文案（用户原话：「将订阅更新替换为出现的问题(精简文字)」）
  const only = problems[0]
  return { show: true, label: only.label, count: only.count, tone: only.tone, title: detail }
}

/**
 * 侧栏导航项最终显示的名字。
 *
 * 没有任务时就是原名的「订阅更新」；有任务时按 pushNavBadge 换成问题文案。
 * 单独抽一个函数是因为模板里有两处渲染导航项（桌面侧栏 / 手机底部标签栏），
 * 两边都要同一套口径，不能各写一份 if。
 *
 * @param {string} itemId 导航项 id
 * @param {string} itemName 导航项原名
 * @param {object} badge pushNavBadge(...) 的返回值
 * @returns {string}
 */
export function navLabelWithBadge(itemId, itemName, badge) {
  if (itemId !== 'push') return itemName
  if (!badge || !badge.show) return itemName
  return badge.label
}
