/**
 * 把技术错误翻译成「一句人话」。
 *
 * 约定（2026-09-18 定）：界面上**不再直接显示**技术信息（HTTP 状态、接口路径、
 * 原始 message），只留一句用户看得懂的原因；完整信息通过 services/appLog.js 进日志，
 * 用户到「日志」页查。
 *
 * 用法：
 *
 *   // 组件里 catch 到错误
 *   message.value = fail('歌单解析', e)          // 写日志 + 拿到一句人话
 *
 *   // 后端返回 code !== 200
 *   const bad = apiError(res); if (bad) message.value = bad
 *
 * 设计取舍：**后端 message 常常本身就是人话**（如「没能找到歌单 id」），
 * 直接丢掉太浪费；但也有些是技术串。所以用 usableMessage() 判一下 ——
 * 短、含中文、没有接口路径和堆栈的，才拿来用。
 */

import { logError } from './appLog.js'
// 日志里的接口路径要带上网关前缀，否则微应用形态下排查时分不清打到了哪儿。
import { API_BASE } from './basePath.js'

const MAX_UI_LEN = 40

function clip(s, n) {
  const t = String(s ?? '')
  return t.length > n ? t.slice(0, n) + '…' : t
}

function hasCJK(s) {
  return /[\u4e00-\u9fa5]/.test(s)
}

/**
 * 后端的 message 能不能直接给用户看。
 * 判据：够短 + 含中文 + 不含接口路径 / 异常类名 / 换行堆栈。
 */
export function usableMessage(msg) {
  const s = String(msg ?? '').trim()
  if (!s || s.length > MAX_UI_LEN) return false
  if (!hasCJK(s)) return false
  if (s.includes('/api/')) return false
  if (s.includes('\n')) return false
  if (/^[A-Za-z]+Error\b/.test(s)) return false
  if (/\bat .+:\d+/.test(s)) return false // 堆栈行
  return true
}

/**
 * 从后端响应体里取「那句话」。
 *
 * 本项目后端回错误的字段名**并不统一**：统一响应壳用 `message`（api 的 respond），
 * 但补强/音质这些接口回的是 `{code, msg}`（`quality_autoupgrade.go`、`quality.go`），
 * 另有少数地方用 `error`。只认 `message` 的后果很具体（2026-11-01，issue #10）：
 * 用户点「手动音质补强」永久弹一句「操作失败」，而后端其实明说了原因
 * （比如「补强轮次还没到时间」「登记表未就绪」）—— 全被 fallback 吞掉，
 * 用户只能来问。
 *
 * 顺序按「本项目最常用 → 最不常用」排，先命中先用。
 */
export function serverMessage(body) {
  if (!body || typeof body !== 'object') return ''
  for (const k of ['message', 'msg', 'error', 'detail']) {
    const v = body[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return ''
}

/** 把 axios 错误 / Error / 字符串整理成写进日志的完整描述 */
export function describeError(err) {
  if (err === undefined || err === null || err === '') return ''
  if (typeof err === 'string') return clip(err, 400)

  const res = err.response
  if (res) {
    const status = res.status
    const method = String(res.config?.method || 'get').toUpperCase()
    const url = API_BASE + (res.config?.url || '')
    let body = ''
    try {
      body = typeof res.data === 'string' ? res.data : JSON.stringify(res.data)
    } catch (_) {}
    return clip(`HTTP ${status} ${method} ${url}${body ? ' ← ' + body : ''}`, 400)
  }

  if (err.code === 'ECONNABORTED') return clip(`请求超时 ${API_BASE + (err.config?.url || '')}`, 400)
  if (err.code === 'ERR_NETWORK' || err.message === 'Network Error') {
    return clip(`网络不可达 ${API_BASE + (err.config?.url || '')}`, 400)
  }
  if (err instanceof Error) return clip(`${err.name}: ${err.message}`, 400)
  return clip(err, 400)
}

/**
 * 错误 → 一句人话。
 * @param {*} err
 * @param {string} [fallback] 兜底文案（也用于完全无法判断时）
 */
export function shortReason(err, fallback = '操作失败') {
  if (typeof err === 'string' && err.trim()) return clip(err.trim(), MAX_UI_LEN)

  const res = err?.response
  if (res) {
    // 后端自己给的人话优先（如「没能找到歌单 id」），比按状态码猜更准。
    // 走 serverMessage 而不是只读 `.message`：补强/音质那批接口回的是 `msg`。
    const serverMsg = serverMessage(res.data)
    if (usableMessage(serverMsg)) return String(serverMsg).trim()

    const s = res.status
    if (s === 401 || s === 403) return '没有权限，请先在「账号连接」登录'
    if (s === 404) return '接口不存在，可能是版本不匹配'
    if (s === 429) return '请求太频繁，稍后再试'
    if (s >= 500) return '服务端出错了，详情见日志'
    if (s === 400 || s === 422) return '参数不对，请检查填写内容'
    return fallback
  }

  if (err?.code === 'ECONNABORTED') return '请求超时，详情见日志'
  if (err?.code === 'ERR_NETWORK' || err?.message === 'Network Error') {
    return '连不上服务，请确认后端在运行'
  }
  if (err instanceof Error && usableMessage(err.message)) return err.message.trim()
  return fallback
}

/**
 * 失败处理：写日志（完整信息）+ 返回界面要显示的那句人话。
 *
 * @param {string} scope 出错的动作/模块，如「歌单解析」
 * @param {*} err
 * @param {string} [fallback]
 * @returns {string} 一句人话
 */
export function fail(scope, err, fallback = '操作失败') {
  const reason = shortReason(err, fallback)
  logError(scope, reason, describeError(err))
  return reason
}

/**
 * 后端统一响应 `{code, message, data}` → 失败时返回那句人话，成功返回空串。
 * 供 `const bad = apiError(res); if (bad) ...` 这种写法用。
 *
 * 注意 `res` 为空也算失败：拿不到响应体本身就是异常，静默放过会让调用方
 * 以为操作成功了。
 *
 * 字段名走 serverMessage（`message` / `msg` / `error` 都认）—— 只认 `message`
 * 时，回 `{code:400, msg:'…'}` 的那批接口（补强 / 音质）一律退化成 fallback
 * 「操作失败」，真实原因被吞掉（issue #10 的「手动音质补强提示操作失败」）。
 */
export function apiError(res, fallback = '操作失败') {
  if (res && res.code === 200) return ''
  if (!res) return fallback
  const msg = serverMessage(res)
  return usableMessage(msg) ? msg : fallback
}
