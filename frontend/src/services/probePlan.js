/**
 * 后台巡检的**选源计划**：每 5 分钟那一轮到底该探哪几个音源。
 *
 * 为什么需要它（实测 2026-09-30，29 个音源的压测）：
 * 旧实现是「把 sources 全量串行探一遍」，于是
 *   · 一个「注册了 handler 但永不 send('inited')」的源，单次探测要吃满
 *     3 × (waitInited 5s + 退避 0.7s) ≈ 17s，全量轮询直接卡在「检测中 0/29」半分钟；
 *   · 一轮全量 = 29 次真实取链 + 1.8s 主线程阻塞，而且**每 5 分钟重来一次**；
 *   · 音源越多，后台越接近「永远在探」，叠加播放动画/解码就是用户说的「很卡」。
 *
 * 选源口径（只探**有用的**）：
 *   1. 启用池里的 —— 它们才真正影响播放，必须盯着；
 *   2. 从来没测速记录的 —— 新导入的（或导入时关掉自动测试的），探一次卡片上才有记录点。
 * 明确不探：**没启用、且已有测速记录**的。用户没在用它，5 分钟敲一次毫无收益 ——
 *   这条同时把「池外已熔断（最近 DEAD_STREAK 次全失败）」的那批也挡在外面了：
 *   上游都挂了，再敲只是白烧 CPU。想重试还有手动「全部检测」和卡片上的按钮，不会失联。
 *
 * 再加一个**每轮预算 + 游标轮转**：单轮最多探 `budget` 个，下一轮从上次停下的地方接着走。
 * 于是不管导入多少音源，单轮工作量恒定，不会出现「越用越卡」。
 */

/** 每轮后台巡检最多探几个源（启用池 + 待补记录，合起来不超过这个数） */
export const SWEEP_BUDGET = 6

/** 连续失败几次算熔断。**唯一出处**：「清理失效音源」的判据也读这里，别改单边。 */
export const DEAD_STREAK = 3

/**
 * 该音源是否已被测速记录判定为失效：最近 `streak` 次**全部失败**。
 * 不足 `streak` 条一律不算 —— 单次失败可能只是上游抖一下，按单次判死会把还能用的源清掉。
 */
export function isDeadByHistory(history = {}, id, streak = DEAD_STREAK) {
  const list = (history || {})[id] || []
  if (list.length < streak) return false
  return list.slice(-streak).every(r => !r.ok)
}

/** 该音源是否已有测速记录（有记录 = 卡片上已有记录点，不需要再"补一次"） */
function hasHistory(history, id) {
  const list = (history || {})[id]
  return Array.isArray(list) && list.length > 0
}

/**
 * 算出一轮巡检该探哪些源。
 *
 * @param {object}   o
 * @param {Array}    o.sources   音源列表（对象数组取 `.id`，也容忍直接给字符串 id）
 * @param {string[]} o.activeIds 启用池（有序，服务端返回的 active_source_ids）
 * @param {object}   o.history   测速记录 { [id]: [{ ok, ms, t }] }
 * @param {number}   o.cursor    上一轮停下的位置（本轮从这个下标继续，环形轮转）
 * @param {number}   o.budget    本轮最多探几个
 * @returns {{ ids: string[], nextCursor: number, total: number, skipped: number }}
 *          `total` = 候选总数，`skipped` = 被跳过的音源数（想解释「为什么没全探」时用）
 */
export function sweepPlan({
  sources = [],
  activeIds = [],
  history = {},
  cursor = 0,
  budget = SWEEP_BUDGET,
} = {}) {
  const pool = new Set(activeIds || [])
  const candidates = []

  for (const s of sources || []) {
    const id = typeof s === 'string' ? s : s && s.id
    if (!id) continue
    // 池外的只认「还没记录过」的；有记录的一律跳过（不管记录是好是坏，用户没在用）
    if (!pool.has(id) && hasHistory(history, id)) continue
    candidates.push(id)
  }

  const total = candidates.length
  const skipped = (sources || []).length - total
  if (!total) return { ids: [], nextCursor: 0, total: 0, skipped }

  const size = Math.max(1, Math.floor(budget) || 1)
  // cursor 可能是按旧的候选长度算出来的，取模保证永远落在合法下标上
  const start = ((Math.floor(cursor) || 0) % total + total) % total
  const take = Math.min(size, total)
  const ids = []
  for (let i = 0; i < take; i++) ids.push(candidates[(start + i) % total])

  return { ids, nextCursor: (start + take) % total, total, skipped }
}
