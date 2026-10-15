/* 下载队列的**合并规则**（纯函数，无依赖，便于直接跑测试）。
 *
 * # 为什么单独一个模块
 *
 * 「本地列表」和「NAS 上的那份副本」谁说了算，是这个队列最容易出诡异 bug 的地方，
 * 而 bug 的表现又特别像玄学：
 *   · 点了取消，过一会儿它自己又跑起来，最后显示下载成功；
 *   · 一键「清除记录」清干净了，刷新一下记录全回来；
 *   · 点「停止全部」，切个页面又变回排队中。
 * 三条都是同一类竞态：**移除/停止只改了本地，服务端那份要等一次 PUT 才更新，
 * 而中间任何一次同步都是拿服务端整份替换本地**，于是旧状态把新状态盖回去。
 *
 * 规则本身很短，但必须逐条钉住（downloadMerge.test.mjs），而且不能依赖 Vue 的
 * ref / localStorage 才能测 —— 所以规则放这里，downloadManager.js 只负责接线。
 */

/**
 * 「本地已经定下来」的状态 —— 本地是这些状态时，不被服务端的在途状态盖掉。
 *
 * 不只是终态（成功/失败/已停止），还包括**用户主动挂起的「已暂停」**：
 * 服务端那份要等一次 PUT 才更新，同步撞进这个窗口时若按服务端算，暂停中的任务会
 * 变回 pending 被真去下载 —— 用户以为停着，磁盘上却多出文件，比「取消后复活」更隐蔽。
 *
 * 反向（本地在途、服务端已定下来）不在此列：那种情况服务端才是对的。
 * 残留风险：用户刚点「继续」而服务端恰好还是旧的「已暂停」，任务会被拉回已暂停
 * （不下载、只多显示一次暂停）。这就是为什么所有用户动作都走 persistTasks(true)
 * 立刻落盘 —— 把窗口压到只有一次 PUT 往返。
 */
export const SETTLED = new Set(['success', 'failed', 'stopped', 'paused'])

export function isSettled(status) {
  return SETTLED.has(status)
}

/**
 * 把一份任务列表并进当前队列，返回合并后的新数组。
 *
 * @param {object}   o
 * @param {Array}    o.list       要并入的列表（本地缓存或服务端副本）
 * @param {Array}    o.locals     当前队列（判断「本地是不是已经是终态」）
 * @param {Iterable} o.cancelled  用户明确移除过的任务 id（墓碑）
 * @param {boolean}  o.fromServer 这份列表是不是服务端来的
 * @returns {Array} 合并后的任务数组
 */
export function mergeTaskList({ list, locals = [], cancelled = [], fromServer = false }) {
  if (!Array.isArray(list)) return []
  const cancelledSet = cancelled instanceof Set ? cancelled : new Set(cancelled || [])
  const localById = new Map((locals || []).filter(Boolean).map(t => [t.id, t]))
  const kept = []
  for (const t of list) {
    if (!t || !t.id) continue
    // ① 用户明确移除过（取消 / 清空记录）的任务：任何来源都不再复活它。
    //    按 id 而不是 songKey —— 「取消」针对这一次任务，同一首歌之后重新入队
    //    是新 id；按 songKey 记会把用户后来重新加的那次也误杀。
    if (cancelledSet.has(t.id)) continue
    const local = localById.get(t.id)
    // ② 服务端说还在跑/还在排队，本地已经落定 → 以本地为准。
    //    这是「点停止之后它又自己跑起来」的另一条路。
    //    反向（本地在跑、服务端已成功）不覆盖：那才是真的下完了，得认。
    if (fromServer && local && isSettled(local.status) && !isSettled(t.status)) {
      kept.push({
        ...t,
        ...local,
        // 停止/暂停都必须带住 _aborted，否则 executeTask 一进去就不返回，任务真会跑
        _aborted: !!local._aborted || local.status === 'stopped' || local.status === 'paused',
        _retryAfter: 0,
      })
      continue
    }
    const task = { ...t, _aborted: false, _retryAfter: 0 }
    // 刷新页面后无法恢复进行中的请求（后端下载是阻塞式请求），重置为 pending
    // 重新排队 —— 与 miyin 启动时把 running 重置为 queued 的做法一致。
    if (task.status === 'resolving' || task.status === 'downloading') {
      task.status = 'pending'
      task.error = '页面刷新后重新排队'
    }
    kept.push(task)
  }
  return kept
}
