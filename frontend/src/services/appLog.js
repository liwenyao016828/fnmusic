/**
 * 前端操作日志。
 *
 * 约定（2026-09-18 定）：**界面上的报错只留一句人话**（见 services/userMsg.js），
 * 完整信息（接口路径、原始 message、堆栈）落到日志里，用户到「日志」页自己查 ——
 * 既不用开浏览器控制台，也不用截图问人。
 *
 * 这个模块把前端事件送到两个地方：
 *
 *   1. **本地留存** —— localStorage 环形缓冲，刷新/重启不丢，离线也能查
 *   2. **上报后端** —— POST /api/logs，进后端运行日志，与后端事件落在同一条时间轴
 *
 * ⚠️ 本地留存**刻意不走 services/prefs.js**：prefs 会同步到服务端的 ui_prefs.json，
 * 而那个文件有 **64KB 总上限**（见 backend/pkg/config/uiprefs.go 的 MaxUIPrefsBytes）。
 * 日志是几十 KB 的量级，塞进去会挤占其他窗口记忆的预算，甚至让整体写入失败。
 * 而且日志本就是**本机诊断记录**，不是跨端共享的偏好 —— 直接用 localStorage 更合适。
 * 键名仍用 `fn_` 前缀，与其余前端存储保持一致，便于一眼认出是本应用的数据。
 *
 * ⚠️ 上报通道由外部注入（`setLogReporter`），本模块**不 import api/client** ——
 * 日志是最底层的能力，不该依赖网络层：否则 client.js 一旦想记日志就成循环依赖，
 * 而且单元测试也会被迫把 axios 拖进来。
 */

const STORAGE_KEY = 'fn_app_log'
const MAX = 200 // 本地留存条数
const MAX_MSG = 500 // 单条消息字符上限（本地留存用，避免配额被一条撑爆）

let entries = [] // 时间正序（新的在末尾）
let loaded = false
let reporter = null // (level, message) => void | Promise，由 main.js 注入

function hasStorage() {
  try {
    return typeof localStorage !== 'undefined' && localStorage !== null
  } catch (_) {
    return false
  }
}

function load() {
  if (loaded) return
  loaded = true
  if (!hasStorage()) return
  try {
    const arr = JSON.parse(localStorage.getItem(STORAGE_KEY) || '[]')
    if (Array.isArray(arr)) entries = arr.slice(-MAX)
  } catch (_) {
    entries = []
  }
}

function persist() {
  if (!hasStorage()) return
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(entries.slice(-MAX)))
  } catch (_) {
    // 配额满 / 隐私模式：砍掉一半再试一次，还不行就只留内存（不影响使用）
    try {
      entries = entries.slice(-Math.floor(MAX / 2))
      localStorage.setItem(STORAGE_KEY, JSON.stringify(entries))
    } catch (_) {}
  }
}

function clip(s, n) {
  const t = String(s ?? '')
  return t.length > n ? t.slice(0, n) + '…' : t
}

/** "09-18 14:39:02" —— 本地日志跨天也要能看出日期 */
function stamp(d = new Date()) {
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/**
 * 带时区的绝对时刻（RFC3339，本地 offset），与后端 applog.Entry.Time 同一个口径。
 *
 * 为什么本地留存也要多存一个：`at` 只有「MM-DD HH:MM:SS」，**既没有年份也没有时区**。
 * 用户换一台机器、换一个时区打开，或者跨年之后再看，`at` 就成了无法还原的墙钟数字。
 * 保留 `at` 不动（旧数据、旧的显示逻辑、搜索都还依赖它），只新增字段 —— 与后端
 * 「At 保留给旧客户端、Time 是新客户端唯一正确的来源」是同一套兼容策略。
 */
function stampISO(d = new Date()) {
  const pad = (n, w = 2) => String(n).padStart(w, '0')
  const off = -d.getTimezoneOffset() // 分钟，东为正
  const sign = off >= 0 ? '+' : '-'
  const abs = Math.abs(off)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}` +
    `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
}

function normLevel(l) {
  return l === 'warn' || l === 'error' ? l : 'info'
}

function describe(d) {
  if (d === undefined || d === null || d === '') return ''
  if (d instanceof Error) return d.message || String(d)
  if (typeof d === 'string') return d
  try {
    return JSON.stringify(d)
  } catch (_) {
    return String(d)
  }
}

/**
 * 注入上报通道（POST /api/logs）。在 main.js 里接一次即可。
 * 不注入时只做本地留存 —— 日志模块自身永远不该因为「上报失败」而报错。
 */
export function setLogReporter(fn) {
  reporter = typeof fn === 'function' ? fn : null
}

/**
 * 记一条前端日志。
 *
 * @param {'info'|'warn'|'error'} level
 * @param {string} scope 出错的动作/模块，如「歌单解析」「推送任务保存」
 * @param {string} message 完整信息 —— **这里可以写技术细节**，它只进日志不进界面
 * @param {*} [detail] 附加信息（Error 对象、响应体等），会转成文本拼在后面
 * @returns {{at:string, level:string, scope:string, message:string}}
 */
export function logEvent(level, scope, message, detail) {
  load()
  const extra = describe(detail)
  const text = extra ? `${message} | ${extra}` : String(message ?? '')
  const now = new Date()
  const e = {
    at: stamp(now),
    // 绝对时刻（带本地 offset）。at 没有年份和时区，跨年/换机器就还原不回来；
    // 新增而不是替换 at，是为了不改变既有显示与搜索行为。
    time: stampISO(now),
    level: normLevel(level),
    scope: String(scope || ''),
    message: clip(text, MAX_MSG),
  }
  entries.push(e)
  if (entries.length > MAX) entries = entries.slice(-MAX)
  persist()

  // 上报后端：fire-and-forget。后端不可用时本地已经留了一份，不打扰用户、也不再制造报错。
  // 顺带把绝对时刻送过去 —— 否则浏览器时钟与 NAS 时钟不一致时（一个快 8 分钟、
  // 一个慢 3 分钟很常见），两条来源的日志在同一个时间轴上根本对不齐。
  if (reporter) {
    const wire = e.scope ? `[${e.scope}] ${e.message}` : e.message
    try {
      Promise.resolve(reporter(e.level, wire, e.time)).catch(() => {})
    } catch (_) {}
  }
  return e
}

export const logError = (scope, message, detail) => logEvent('error', scope, message, detail)
export const logWarn = (scope, message, detail) => logEvent('warn', scope, message, detail)
export const logInfo = (scope, message, detail) => logEvent('info', scope, message, detail)

/** 读全部本地留存条目（时间正序的副本） */
export function logEntries() {
  load()
  return entries.slice()
}

/** 清空本地留存 */
export function clearLocalLog() {
  load()
  entries = []
  persist()
}

/** 当前本地留存条数（不触发加载的轻量读法） */
export function localLogCount() {
  load()
  return entries.length
}
