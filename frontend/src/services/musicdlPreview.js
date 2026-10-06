/**
 * musicdl「试运行」（预览一个未启用的源）在前端侧的那点纯逻辑。
 *
 * 为什么要单独抽出来：音源管理页里这一段最容易写错的不是按钮，而是**倒计时**。
 * 服务端给的是一次快照（`seconds_left`），界面要每秒刷新显示 —— 直接拿快照值每秒
 * 减 1，会在「轮询又打了一次接口 / 页面刚回来 / 时钟被改过」时跳来跳去（甚至减成负数）。
 * 所以口径统一成一处：**把服务端剩余秒数换成一个本地截止时刻**，之后只从那个时刻算
 * 剩余，永远不会为负、也不会因为多次轮询而重复计时。
 *
 * 另一处是**相位判断**：进程还没就绪（首次要建 venv，一两分钟）时去探源，会把
 * 「还没起来」记成「这个源不能用」—— 那正是这个页面存在的意义所在，不能搞错。
 */

/** 服务端默认 TTL（秒），与后端 `previewTTL` 同值；只在拿不到 ttl 时兜底。 */
export const PREVIEW_DEFAULT_TTL = 300

/** 试运行的阶段。idle = 没有在试运行。 */
export const PREVIEW_PHASES = ['idle', 'starting', 'ready', 'failed']

/**
 * 剩余秒数的人话：'4:32' / '45 秒'。
 * 负数与非数字一律当 0（倒计时永远不该出现 '-1 秒'）。
 */
export function formatSecondsLeft(sec) {
  const n = Math.max(0, Math.floor(Number(sec) || 0))
  if (n < 60) return `${n} 秒`
  const m = Math.floor(n / 60)
  const s = n % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

/** 取状态对象（接口可能是 {code,data} 包一层，也可能直接是 data）。 */
export function previewData(resp) {
  if (!resp || typeof resp !== 'object') return null
  const d = resp.data && typeof resp.data === 'object' ? resp.data : resp
  return d && typeof d === 'object' ? d : null
}

/**
 * 试运行的阶段。
 *
 * state 来自后端（sidecar 的状态机）：off / preparing / starting / ready / failed / stopped。
 * 只有 `ready` 才算「起来了」，其余一律当「还在起」——**宁可让用户多等一秒，
 * 也不要把没起来的进程当成源不可用**。
 */
export function previewPhase(pv) {
  if (!pv || pv.active !== true) return 'idle'
  const st = String(pv.state || (pv.sidecar && pv.sidecar.state) || '')
  if (st === 'failed') return 'failed'
  if (st === 'ready') return 'ready'
  return 'starting'
}

/**
 * 试运行状态那一行文案。idle（没在试）时返回空串。
 *
 * `now` 让调用方传一个**响应式**的当前时刻：组件里倒计时每秒重绘靠的就是它
 * （组件传 `nowMs.value`，纯函数这边默认取 `Date.now()`）。
 */
export function previewStatusText(pv, label, now = Date.now()) {
  const phase = previewPhase(pv)
  if (phase === 'idle') return ''
  const name = label || pv.source || '该源'
  const left = formatSecondsLeft(secondsUntil(previewDeadline(pv, now), now))
  if (phase === 'failed') {
    const err = (pv.sidecar && pv.sidecar.error) || '原因见日志'
    return `试运行 ${name} 启动失败：${err}`
  }
  if (phase === 'starting') {
    return `正在拉起 ${name}…（${left} 后自动回收；首次可能要建 Python 环境，一两分钟）`
  }
  return `试运行 ${name} · 剩余 ${left} · 到点自动回收（不写配置、不影响搜索）`
}

/**
 * 把服务端快照换成**本地截止时刻**（毫秒时间戳，0 = 没有在试运行）。
 *
 * 之后所有倒计时都从它算 —— 于是「每秒重绘」不需要每秒打接口，
 * 多次轮询之间也不会重复计时。
 */
export function previewDeadline(pv, now = Date.now()) {
  if (!pv || pv.active !== true) return 0
  const left = Math.max(0, Math.floor(Number(pv.seconds_left) || 0))
  return now + left * 1000
}

/** 距离截止时刻还有几秒（向上取整、不为负）。 */
export function secondsUntil(deadline, now = Date.now()) {
  const d = Number(deadline) || 0
  if (!d) return 0
  return Math.max(0, Math.ceil((d - now) / 1000))
}

/**
 * 试运行这一点是不是**已经该探一次源**了。
 *
 * 判据只有一条：进程已经 ready。没就绪时探源会把「还没起来」记成「源不能用」，
 * 而那正是这个页面最不该给错的结论。
 */
export function shouldProbePreview(pv) {
  return previewPhase(pv) === 'ready'
}

/**
 * 还要不要继续问状态：只要服务端还报「在试运行」就接着问。
 *
 * 为什么不能写成「本地倒计时走完就停」：回收发生在**服务端**（定时器 + 停进程），
 * 界面自己数到 0 只说明「大概到点了」—— 而那一刻恰恰最该问一次，好把状态行与
 * 进程状态如实收回去。服务端报 active:false 才算真的结束。
 */
export function previewNeedsPolling(pv) {
  return previewPhase(pv) !== 'idle'
}
