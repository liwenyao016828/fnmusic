/**
 * 「生产者」（订阅自动补下载 / 手动补下载 / 批量执行）的停止闸门 —— 纯函数，无依赖。
 *
 * # 为什么单独拆出来
 *
 * 2026-11-03 用户报障：「停止全部后下一首仍继续推送下载」。
 *
 * 下载队列的「停止全部」只停**已经进队列的东西**。可是队列里的歌是一批一批
 * 由生产者塞进去的，生产者有两条路径完全绕过「只停队列」这件事：
 *
 *  1. **正在入队的这一批**：`enqueueSongs` 是逐首串行搜索 → 入队的（几十首要
 *     一两分钟）。以前只有「订阅行被停」会打断它（`isRowStopped`），
 *     下载队列的「停止全部」对它毫无影响 —— 于是用户点完停止，队列还在涨。
 *  2. **已经排好的批次（ticker）**：`autoFillPending` 每 8 秒跑一轮，判据是
 *     「这一行还有没有活跃任务」。批与批之间队列必然是空的（上一批刚下完），
 *     所以下一轮一定会再入队一批 —— 这就是用户看到的「下一首继续」。
 *     `watchQueueDrain` / `watchFillCompletion` 里挂的定时器同理。
 *
 * 所以停止必须是一个**跨模块的闸门**：`downloadManager.stopAll()`
 * 关闸（并取消在途的那一批），生产者每次搜索/入队前都来问一句。
 * 「继续/重试」再开闸 —— 用户主动去下的动作不该被上一轮的停止拦住。
 *
 * # 与 stoppedKeys 的关系
 *
 * `stoppedKeys` 是**按订阅行**的停止（行内的停止按钮）：停的是整行，包括它的
 * 定时执行能力。这里的闸门是**整条下载队列**的停止：只停「往队列里塞东西」，
 * 不动订阅本身的 enabled。两者是并列的，任何一条命中都要停。
 */

/**
 * 全局停止闸门（跨模块单例用：`downloadManager` 关门，生产者每次入队前查）。
 *
 * 记住「是谁关的」是为了让日志和提示说清原因；`''` = 开闸。
 */
export function createProducerGate() {
  let closedBy = ''

  return {
    /** 关闸：此后任何生产者都不得再入队（`reason` 只用于日志）。 */
    close(reason = 'stop_all') {
      closedBy = String(reason || 'stop_all')
    },
    /** 开闸：用户主动继续/重试/重新下载时调用。 */
    open() {
      closedBy = ''
    },
    /** 是否处于关闭状态。 */
    isClosed() {
      return !!closedBy
    },
    /** 关闭原因（未关时为空串）。 */
    reason() {
      return closedBy
    },
  }
}

/**
 * 生产者此刻是否**必须**停止。
 *
 * @param {object} g
 * @param {boolean} g.queueStopped 下载队列「停止全部」是否按下了（跨模块闸门）
 * @param {boolean} g.rowStopped    这个生产者所属的订阅行是否被停（`stoppedKeys`）
 * @param {boolean} g.queuePaused   整队是否处于「暂停全部」
 *        —— 暂停不是停止，但同样不该继续往队列里塞：塞进去的只有排队状态，
 *        用户在暂停期间看到「队列一直在涨」也是一种「停不下来」。**只影响 ticker 批次**，
 *        不影响用户手点的「补下载」。
 * @param {boolean} g.manual        这一批是用户手点发起的
 * @returns {boolean}
 */
export function producerMustStop({ queueStopped = false, rowStopped = false, queuePaused = false, manual = false } = {}) {
  if (queueStopped || rowStopped) return true
  if (queuePaused && !manual) return true
  return false
}

/**
 * 一次搜索 → 入队的「批次令牌」。
 *
 * 生产者每开一批就取一个令牌，之后**每一次 await 之后**都问一次 `stopped()`：
 * 逐首串行搜索的中间点（每首都可能入队一首）、以及一批与下一批之间的空档，
 * 都是「停止」必须能插进去的地方。
 *
 * @param {() => boolean} stopped 停止判据（通常是 `() => gate.isClosed() || isRowStopped(key)`）
 */
export function createFillBatch({ stopped }) {
  const isStopped = typeof stopped === 'function' ? stopped : () => false
  return {
    /** 这一批此刻是否应立刻放弃（不再搜索、不再入队）。 */
    stopped() {
      return !!isStopped()
    },
  }
}

/**
 * 把「停止」接进一个可以被打断的等待：等待期间一旦停止，就立刻返回而不是等满。
 *
 * 用于「下载排空后等 90 秒再自动推送」这类定时器 —— 以前只在**等之前**和
 * **等之后**各查一次，停止不掉这段等待本身（用户看到的就是「停止后一会儿
 * 又动了」）。这里改成每 `stepMs` 复查一次。
 *
 * @param {number} ms
 * @param {object} o
 * @param {() => boolean} o.stopped
 * @param {number} [o.stepMs=1500]
 * @param {(fn: Function, ms: number) => any} [o.setTimeoutFn]
 * @returns {Promise<boolean>} true = 等满了；false = 中途被停止打断
 */
export function waitUnlessStopped(ms, { stopped, stepMs = 1500, setTimeoutFn } = {}) {
  const isStopped = typeof stopped === 'function' ? stopped : () => false
  const setT = setTimeoutFn || ((fn, wait) => setTimeout(fn, wait))
  if (isStopped()) return Promise.resolve(false)
  return new Promise((resolve) => {
    let left = Math.max(0, Number(ms) || 0)
    const tick = () => {
      if (isStopped()) {
        resolve(false)
        return
      }
      if (left <= 0) {
        resolve(true)
        return
      }
      const step = Math.min(Math.max(1, Number(stepMs) || 1500), left)
      left -= step
      setT(tick, step)
    }
    tick()
  })
}
