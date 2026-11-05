/**
 * 日志时间戳的解析与显示（纯函数，无 Vue / 浏览器 API 依赖，便于 node --test 直跑）。
 *
 * # 为什么需要这个模块
 *
 * NAS 的系统时区通常是 UTC（本机 /etc/timezone = Etc/UTC），后端 `time.Now()`
 * 拿到的就是 UTC 墙钟。早期 applog.Entry.At 直接 `Format("15:04:05")`，前端原样
 * 渲染 → 用户看到的时间比本地慢 8 小时。
 *
 * 正确的做法：**后端传绝对时刻，显示交给浏览器本地时区**。后端新增
 * `Entry.Time`（RFC3339，带 UTC offset）后，这里负责解析它，并兼容几类历史数据：
 *
 *   · RFC3339 / ISO 带 offset  → 直接解析，绝对时刻明确；
 *   · Unix 秒 / 毫秒（push 运行记录用 `StartedAt int64`）→ 转毫秒；
 *   · 不带 offset 的本地 ISO → 分两类：
 *     - **后端产生的旧墙钟**（monitor 旧运行记录 `"2006-01-02T15:04:05"`，
 *       NAS UTC）→ 用 tsOfServer 按 **UTC** 解释。这些字符串记录的是服务器
 *       墙钟，服务器的时区就是 UTC —— 「本地 22 点显示 14 点」的直接根因
 *       就在它：naive ISO 被浏览器按本地解释，等于把 UTC 墙钟当成了本地墙钟。
 *       新记录已带 offset（后端 nowStamp()），不走这条分支。
 *     - **浏览器产生的本地留存**（appLog 的 `"MM-DD HH:MM:SS"`）→ 按
 *       **浏览器本地**解释（tsOf 默认行为）。它本来就记在用户机器上，字符串
 *       里的墙钟就是本地墙钟。
 *     前端 appLog 新条目已带 time（stampISO，RFC3339 带本地 offset），
 *     entryTimeText 优先走它，不依赖这里的两难推断。
 *
 * ⚠️ 调用方新增字段时必须带 offset，否则又会退回"服务器时区决定显示"的老问题。
 */

/**
 * 把时间戳（秒 / 毫秒）或时间字符串解析成毫秒数；无法解析返回 0。
 *
 * @param {number|string|Date|null|undefined} v
 * @param {{utcIfNaive?: boolean}} [opts] utcIfNaive=true 时，不带 offset 的
 *        ISO 字符串按 UTC 解释（用于后端旧墙钟数据，见 tsOfServer）
 * @returns {number}
 */
export function tsOf(v, opts = {}) {
  const { utcIfNaive = false } = opts
  if (v === null || v === undefined || v === '') return 0
  if (v instanceof Date) return Number.isNaN(v.getTime()) ? 0 : v.getTime()
  if (typeof v === 'number') {
    if (!Number.isFinite(v) || v <= 0) return 0
    // 1e12 ≈ 2001-09-09 的毫秒数；小于它的按秒级 Unix 时间处理。
    return v > 1e12 ? v : v * 1000
  }
  if (typeof v !== 'string') return 0
  const s = v.trim()
  if (!s) return 0

  // appLog.js 的本地格式 "MM-DD HH:MM:SS"（没有年份）—— 补上当前年份与 T。
  // 注意顺序：必须在通用 ISO 归一化之前匹配，否则 "-11-03" 会被当成月份。
  let m = /^(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/.exec(s)
  if (m) {
    const d = new Date()
    d.setFullYear(d.getFullYear(), Number(m[1]) - 1, Number(m[2]))
    d.setHours(Number(m[3]), Number(m[4]), Number(m[5] || 0), Number((m[6] || '0').padEnd(3, '0')))
    const ms = d.getTime()
    return Number.isNaN(ms) ? 0 : ms
  }

  // 归一化：空格分隔符补成 T；只有 "HH:MM" 的补 ":00"。
  let iso = s
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(iso)) iso = iso.replace(' ', 'T')
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(iso)) iso += ':00'

  // 后端旧墙钟数据（monitor 旧运行记录 started_at/finished_at）没有 offset，
  // 但它记的是 NAS 的 UTC 墙钟 —— 按 UTC 解释才能还原绝对时刻。
  // 显式补 Z 而不是转 Date 再调 setUTC*：Date.parse 的 ISO 分支按 UTC 解析
  // 补了 Z 的字符串，行为是规范的；而 new Date(...) 构造器对 naive ISO
  // **仍按本地解释**，绕不过去。
  if (utcIfNaive && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?$/.test(iso)) iso += 'Z'

  const t = Date.parse(iso)
  return Number.isNaN(t) ? 0 : t
}

/**
 * 后端旧墙钟数据的时间解析：不带 offset 的 ISO 按 UTC 解释。
 * 带 offset / Unix 秒毫秒的数据不受影响 —— 它们的绝对时刻本来就明确。
 * @param {number|string|Date|null|undefined} v
 * @returns {number}
 */
export function tsOfServer(v) {
  return tsOf(v, { utcIfNaive: true })
}

/**
 * 后端旧墙钟时间的显示格式化（与 tsOfServer 配套）。
 * @param {number|string|Date} v
 * @param {{date?: boolean}} [opts]
 * @returns {string} 无法解析时回退为原文
 */
export function fmtServerDateTime(v, opts = {}) {
  const ms = tsOfServer(v)
  if (!ms) return v === null || v === undefined ? '' : String(v)
  return fmtLocalDateTime(ms, opts)
}

/**
 * 格式化成浏览器本地的「日期 + 时间」（zh-CN，24 小时制）。
 * 跨天的日志必须能看出日期，只有 HH:MM:SS 会让跨午夜的记录完全不可读。
 * @param {number|string|Date} v
 * @param {{date?: boolean}} [opts] date:false 时只显示时分秒
 * @returns {string} 无法解析时回退为原文
 */
export function fmtLocalDateTime(v, opts = {}) {
  const { date = true } = opts
  const ms = tsOf(v)
  if (!ms) return v === null || v === undefined ? '' : String(v)
  const d = new Date(ms)
  const p = (n) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  if (!date) return hm
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
}

/**
 * 运行日志条目的显示时间：优先用带时区的 RFC3339 `time` 字段，
 * 只有旧客户端 / 旧数据的 `at`（HH:MM:SS）兜底原样显示。
 * @param {{time?: string, at?: string}|null|undefined} e
 * @returns {string}
 */
export function entryTimeText(e) {
  if (!e) return ''
  if (e.time) {
    const ms = tsOf(e.time)
    if (ms) return fmtLocalDateTime(ms)
  }
  return String(e.at || '')
}
