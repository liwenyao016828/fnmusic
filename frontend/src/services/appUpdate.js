/**
 * 应用内更新：检查 GitHub Release → 下载 fpk → 安装 → 等待飞牛重启。
 *
 * 为什么单独一个 service：
 *   · 检查/下载/安装是三段异步，下载完还要**自动**接安装，状态机放在组件里会被
 *     路由切换打断一半（组件卸载时定时器必须清掉，但进度不能丢）；
 *   · 这里不碰 DOM，fetch 可注入，node:test 能直接钉住状态迁移；
 *   · 后端拿不到安装权限时返回的是中文操作提示，界面必须原样显示，不能自己编。
 *
 * 契约（backend/pkg/api/app_update.go）：
 *   GET  /api/app/update/check    → {current,latest,has_update,notes,published_at,...}
 *   POST /api/app/update/download → {status:'idle'|'downloading'|'ready'|'installing'|'error',progress,path,size,error}
 *   GET  /api/app/update/state    → 同上
 *   POST /api/app/update/install  → {started,log}；失败时 code!=200 + 中文 message
 *   GET  /api/app/update/notice   → {show,version,from_version,direction,notes}
 *   POST /api/app/update/notice/ack → {ok}
 *
 * 「更新完成后第一次进入应用，提示更新完成和版本号」（用户 2026-10-25）：
 * 判据由后端定（版本号变了就提示一次，见 backend/pkg/appupdate/notice.go），
 * 前端只负责「取一次 → 弹窗 → 用户看到后 ack」。**不能**一取到就 ack：
 * 那样提示还没渲染就被标记已读，用户永远看不到。
 */

import { ref } from 'vue'

export const UPDATE_POLL_MS = 1000

/**
 * 自动检查更新的间隔：15 分钟。
 *
 * 用户 2026-10-25：「自动检查更新改为15分钟一次」。后端缓存 TTL 也是 15 分钟
 * （backend/pkg/appupdate/manager.go 的 defaultCacheTTL），两边必须一起改：
 * 前端比后端短 = 每次都打空（后端直接返缓存，没问题但浪费请求）；
 * 前端比后端长 = 用户看到的信息最旧能旧到 15 分钟以上。
 */
export const AUTO_CHECK_MS = 15 * 60 * 1000

/** 有语义的更新错误：界面只显示 message，不暴露 HTTP/JSON 细节。 */
export class UpdateError extends Error {
  constructor(message, { status = 0, code = '' } = {}) {
    super(message)
    this.name = 'UpdateError'
    this.status = status
    this.code = code
  }
}

function cleanVersion(v) {
  return String(v || '').replace(/^v/i, '').trim()
}

export function normalizeCheck(data) {
  const d = data || {}
  const current = cleanVersion(d.current)
  const latest = cleanVersion(d.latest)
  const notes = typeof d.notes === 'string' ? d.notes : ''
  return {
    current,
    latest,
    // 后端说没有更新就以它为准；但老接口没带 has_update 时，用版本号兜底。
    hasUpdate: d.has_update === undefined ? !!(latest && latest !== current) : !!d.has_update,
    notes,
    publishedAt: d.published_at || '',
    assetName: d.asset_name || '',
    assetUrl: d.asset_url || '',
    size: Number(d.size || 0) || 0,
    htmlUrl: d.html_url || '',
    checkedAt: Number(d.checked_at || 0) || 0,
    error: typeof d.error === 'string' ? d.error : '',
    source: typeof d.source === 'string' ? d.source : '',
    tried: Array.isArray(d.tried)
      ? d.tried.map((t) => ({
        name: String(t?.name || ''),
        url: String(t?.url || ''),
        ok: !!t?.ok,
        skipped: !!t?.skipped,
        reason: String(t?.reason || ''),
        error: String(t?.error || ''),
      }))
      : [],
  }
}

export function normalizeNotice(data) {
  const d = data || {}
  const direction = String(d.direction || '').toLowerCase()
  return {
    show: !!d.show,
    version: cleanVersion(d.version),
    fromVersion: cleanVersion(d.from_version),
    // 老后端不带 direction（或值不认识）时按 up 处理：这是最常见的情形。
    direction: ['up', 'down', 'unknown'].includes(direction) ? direction : 'up',
    notes: typeof d.notes === 'string' ? d.notes : '',
  }
}

export function normalizeUpdateState(data) {
  const d = data || {}
  const status = String(d.status || 'idle').toLowerCase()
  const known = ['idle', 'downloading', 'ready', 'installing', 'error']
  const raw = Number(d.progress || 0)
  // 后端 progress 是 0–100 的百分比；越界值只做防御，不擅自把 0.5 当 50%。
  const progress = Number.isFinite(raw) ? Math.max(0, Math.min(100, Math.round(raw))) : 0
  return {
    status: known.includes(status) ? status : 'idle',
    progress,
    path: d.path || '',
    size: Number(d.size || 0) || 0,
    error: typeof d.error === 'string' ? d.error : '',
  }
}

export function formatBytes(n) {
  const size = Number(n || 0)
  if (!size) return ''
  if (size < 1024 * 1024) return `${Math.max(1, Math.round(size / 1024))} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function defaultFetch(...args) {
  if (typeof globalThis.fetch !== 'function') {
    throw new UpdateError('当前环境不支持网络请求')
  }
  return globalThis.fetch(...args)
}

/**
 * 创建一个更新控制器。默认导出单例；测试里 createAppUpdate({ fetchImpl }) 隔离。
 */
export function createAppUpdate({
  fetchImpl = defaultFetch,
  pollMs = UPDATE_POLL_MS,
  autoCheckMs = AUTO_CHECK_MS,
  now = () => Date.now(),
} = {}) {
  const current = ref('')
  const latest = ref('')
  const hasUpdate = ref(false)
  const notes = ref('')
  const publishedAt = ref('')
  const assetName = ref('')
  const assetUrl = ref('')
  const size = ref(0)
  const htmlUrl = ref('')
  const checkedAt = ref(0)
  const checkError = ref('')
  // 本次检查依次试过的更新源（[{name,url,ok,reason,error,skipped}]）。
  // 字段名 tried 与后端一致；界面用它显示「哪台成功、哪台为什么失败」。
  const checkTried = ref([])
  const checkSource = ref('')

  // 「更新完成」提示（见文件头）：notice 为 null 表示没提示或已处理完。
  const notice = ref(null)
  let notifyChecked = false
  let notifyTimer = null

  const checking = ref(false)
  const downloading = ref(false)
  const installing = ref(false)
  const installed = ref(false)
  const progress = ref(0)
  const downloadPath = ref('')
  const error = ref('')
  const phase = ref('idle') // idle | checking | available | downloading | ready | installing | installed | error

  let timer = null
  let polling = false
  let installRequested = false
  let autoChecked = false
  let stopped = false
  // 卸载/停止会 ++pollEpoch；异步请求回来时只要 epoch 变了，就说明组件已离开，
  // 不能再把定时器续上（否则切页后还在偷偷轮询）。
  let pollEpoch = 0

  function stopPolling() {
    stopped = true
    pollEpoch += 1
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  async function request(path, { method = 'GET', body = null, timeout = 15000 } = {}) {
    const controller = typeof AbortController === 'function' ? new AbortController() : null
    const timeoutId = controller ? setTimeout(() => controller.abort(), timeout) : null
    try {
      let res
      try {
        res = await fetchImpl(path, {
          method,
          headers: body ? { 'Content-Type': 'application/json' } : undefined,
          body: body ? JSON.stringify(body) : undefined,
          signal: controller ? controller.signal : undefined,
        })
      } catch (e) {
        if (e?.name === 'AbortError') throw new UpdateError('请求超时，请稍后重试')
        throw new UpdateError('无法连接 NAS，请检查网络后重试')
      }
      let payload = null
      try {
        payload = await res.json()
      } catch (_) {
        // 非 JSON（反代 502 的 HTML 等）按状态码给一句人话
      }
      if (!res.ok) {
        // 404 说明这个后端根本没有更新路由；此时后端可能返回反代/框架的通用
        // "not found"，不能把它直接摆给用户，固定成「先手动升级一次」的动作提示。
        const missing = res.status === 404
        const fallback = missing
          ? '当前后端版本还不支持应用内更新，请先手动升级一次'
          : `更新服务返回 HTTP ${res.status}`
        throw new UpdateError(missing ? fallback : (payload?.message || fallback), { status: res.status })
      }
      if (payload && payload.code !== undefined && Number(payload.code) !== 200) {
        // 安装失败的 message 是后端写好的中文操作提示，原样抛出，绝不改写。
        throw new UpdateError(payload.message || '更新服务返回失败', { code: String(payload.code) })
      }
      return payload?.data ?? payload
    } finally {
      if (timeoutId) clearTimeout(timeoutId)
    }
  }

  function applyCheck(data) {
    const d = normalizeCheck(data)
    current.value = d.current || current.value
    latest.value = d.latest
    hasUpdate.value = d.hasUpdate
    notes.value = d.notes
    publishedAt.value = d.publishedAt
    assetName.value = d.assetName
    assetUrl.value = d.assetUrl
    size.value = d.size
    htmlUrl.value = d.htmlUrl
    checkedAt.value = now()
    checkError.value = d.error
    checkSource.value = d.source || ''
    checkTried.value = Array.isArray(d.tried) ? d.tried : []
    if (installed.value) {
      phase.value = 'installed'
    } else if (d.hasUpdate) {
      if (!['downloading', 'ready', 'installing'].includes(phase.value)) phase.value = 'available'
    } else {
      phase.value = 'idle'
    }
  }

  async function check({ silent = false } = {}) {
    if (checking.value) return null
    checking.value = true
    if (!silent) checkError.value = ''
    try {
      const data = await request('/api/app/update/check', { timeout: 20000 })
      applyCheck(data)
      return normalizeCheck(data)
    } catch (e) {
      // 静默检查失败只在卡片里留一行浅灰提示，绝不弹错/打断设置页。
      checkError.value = e?.message || '检查更新失败'
      if (!hasUpdate.value && !installed.value) phase.value = 'idle'
      return null
    } finally {
      checking.value = false
    }
  }

  function failFrom(e) {
    error.value = e?.message || '更新失败'
    downloading.value = false
    installing.value = false
    phase.value = 'error'
    stopPolling()
  }

  function schedulePoll() {
    if (timer) return
    stopped = false
    timer = setTimeout(async () => {
      timer = null
      if (stopped) return
      const keepGoing = await pollOnce()
      // 轮询期间组件可能已经卸载（stopPolling 把 stopped 置 true），
      // 不能再把定时器续上，否则切页后还在后台打接口。
      if (keepGoing && !installed.value && !stopped) schedulePoll()
    }, pollMs)
  }

  function startPolling() {
    // 重新开始轮询意味着用户还在更新流程里，清掉上一次卸载留下的 stopped。
    stopped = false
    if (!timer) schedulePoll()
  }

  function applyState(data) {
    const st = normalizeUpdateState(data)
    downloadPath.value = st.path || downloadPath.value
    if (st.size) size.value = st.size

    if (st.status === 'downloading') {
      downloading.value = true
      installing.value = false
      phase.value = 'downloading'
      progress.value = st.progress
      return true
    }
    if (st.status === 'ready') {
      downloading.value = false
      progress.value = 100
      phase.value = 'ready'
      if (!installRequested && !installed.value) {
        // 下载完成即自动安装；installUpdate 自己会 stopPolling。
        void installUpdate()
      }
      return false
    }
    if (st.status === 'installing') {
      downloading.value = false
      installing.value = true
      phase.value = 'installing'
      progress.value = 100
      installRequested = true
      return true
    }
    if (st.status === 'error') {
      downloading.value = false
      installing.value = false
      error.value = st.error || '下载更新失败'
      phase.value = 'error'
      return false
    }
    // idle：下载还没开始 / 后端被重启清空了任务。保留「有更新」入口让用户重试。
    downloading.value = false
    installing.value = false
    if (['downloading', 'ready', 'installing'].includes(phase.value)) {
      phase.value = hasUpdate.value ? 'available' : 'idle'
    }
    return false
  }

  async function pollOnce() {
    if (polling) return true
    polling = true
    try {
      const data = await request('/api/app/update/state', { timeout: 15000 })
      return applyState(data)
    } catch (e) {
      failFrom(e)
      return false
    } finally {
      polling = false
    }
  }

  async function installUpdate() {
    if (installRequested || installed.value) return
    installRequested = true
    installing.value = true
    downloading.value = false
    phase.value = 'installing'
    progress.value = 100
    stopPolling()
    try {
      await request('/api/app/update/install', { method: 'POST', timeout: 60000 })
      installed.value = true
      installing.value = false
      phase.value = 'installed'
      error.value = ''
    } catch (e) {
      installing.value = false
      installRequested = false
      error.value = e?.message || '安装更新失败'
      phase.value = 'error'
    }
  }

  async function startUpdate() {
    if (!hasUpdate.value) return check()
    if (downloading.value || installing.value || installed.value) return null
    error.value = ''
    checkError.value = ''
    installed.value = false
    installRequested = false
    progress.value = 0
    downloading.value = true
    phase.value = 'downloading'
    const epoch = pollEpoch
    try {
      const data = await request('/api/app/update/download', { method: 'POST', timeout: 30000 })
      const keepGoing = applyState(data)
      if (keepGoing && !installed.value && epoch === pollEpoch) startPolling()
      return data
    } catch (e) {
      failFrom(e)
      return null
    }
  }

  /** 路由切回来时：更新还在跑就恢复轮询，不要因为组件卸载把进度停在半路。 */
  async function resumeIfActive() {
    const active = downloading.value || installing.value || phase.value === 'ready'
      || phase.value === 'downloading' || phase.value === 'installing'
    if (!active || installed.value) return
    const epoch = pollEpoch
    const keepGoing = await pollOnce()
    if (keepGoing && !installed.value && epoch === pollEpoch) startPolling()
  }

  /**
   * 取一次「更新完成」提示。每个应用会话只取一次（notifyChecked 去重），
   * 避免用户在多个页面之间切换时反复弹。
   *
   * 失败一律静默：提示是锦上添花，绝不能因为它让应用启动时报错。
   */
  async function ensureNotice() {
    if (notifyChecked) return null
    notifyChecked = true
    try {
      const data = await request('/api/app/update/notice', { timeout: 8000 })
      const n = normalizeNotice(data)
      notice.value = n.show ? n : null
      return n.show ? n : null
    } catch (_) {
      return null
    }
  }

  /** 用户看到提示后调用：后端记「已提示过」，下次启动不再弹。 */
  async function ackNotice() {
    const v = notice.value?.version || current.value
    notice.value = null
    if (!v) return false
    try {
      const data = await request('/api/app/update/notice/ack', {
        method: 'POST', body: { version: v }, timeout: 8000,
      })
      return !!data?.ok
    } catch (_) {
      // ack 失败最坏结果就是下次启动再提示一次，不需要打扰用户。
      return false
    }
  }

  /**
   * 启动静默自动检查：先查一次，之后每 AUTO_CHECK_MS 再查一次。
   *
   * 为什么是「定时轮询」而不是只看启动一次：应用是常驻的，NAS 上一开就是几个月，
   * 只在启动时查等于永远查不到新版本。轮询期间任何失败都只在设置页留一行浅灰字
   * （check 的 silent 分支），不弹错。
   */
  function startAutoCheck() {
    ensureChecked()
    if (notifyTimer) return
    notifyTimer = setInterval(() => {
      // 正在下载/安装时不要插一脚：check 会改 phase，把进度条状态搅乱。
      if (downloading.value || installing.value) return
      check({ silent: true })
    }, autoCheckMs)
  }

  function stopAutoCheck() {
    if (notifyTimer) {
      clearInterval(notifyTimer)
      notifyTimer = null
    }
  }

  /** 每个应用会话只自动检查一次；之后进设置页不再打扰。 */
  async function ensureChecked() {
    if (autoChecked) return null
    autoChecked = true
    return check({ silent: true })
  }

  // 仅供排查用的内部状态快照（浏览器控制台 appUpdate.debugState()）。
  // 生产代码不读它，但「弹窗为什么不出现」这类问题只看 DOM 是查不出来的：
  // notice/phase/notifyChecked 三者谁先被改，只有它们自己知道。
  function debugState() {
    return {
      notifyChecked,
      hasTimer: !!notifyTimer,
      autoChecked,
      notice: notice.value,
      phase: phase.value,
      hasUpdate: hasUpdate.value,
      latest: latest.value,
      current: current.value,
    }
  }

  function resetForTests() {
    stopPolling()
    current.value = ''
    latest.value = ''
    hasUpdate.value = false
    notes.value = ''
    publishedAt.value = ''
    assetName.value = ''
    assetUrl.value = ''
    size.value = 0
    htmlUrl.value = ''
    checkedAt.value = 0
    checkError.value = ''
    checkTried.value = []
    checkSource.value = ''
    checking.value = false
    downloading.value = false
    installing.value = false
    installed.value = false
    progress.value = 0
    downloadPath.value = ''
    error.value = ''
    phase.value = 'idle'
    polling = false
    installRequested = false
    autoChecked = false
    stopped = false
    notice.value = null
    notifyChecked = false
    stopAutoCheck()
  }

  return {
    current, latest, hasUpdate, notes, publishedAt, assetName, assetUrl, size, htmlUrl,
    checkedAt, checkError, checkTried, checkSource,
    checking, downloading, installing, installed, progress,
    downloadPath, error, phase,
    notice,
    check, startUpdate, pollOnce, installUpdate, stopPolling, startPolling,
    resumeIfActive, ensureChecked, ensureNotice, ackNotice,
    startAutoCheck, stopAutoCheck, resetForTests, debugState,
  }
}

export const appUpdate = createAppUpdate()
