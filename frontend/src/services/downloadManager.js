import { ref, computed } from 'vue'
import { DownloadAPI, DownloadQueueAPI, NasAPI, SearchAPI } from '../api/client'
import { lxRuntime } from '../engine/lx-runtime'
import { sourceHealth } from './sourceHealth'
import { qualityLabel, isHighestQuality, isLosslessTier, isLossyContainer, urlLooksLossy } from '../engine/quality'
import { logError, logWarn } from './appLog'
import { fail, apiError } from './userMsg'
import { decideFailover, isSourceLevelFailure, untriedSources } from './downloadFailover'

// ── 跨平台回退 ──
//
// 歌曲是「在某个平台搜到的」，但那个平台未必取得到直链（版权下架、试听片段、
// 音源脚本对该平台支持不好）。此前会直接判失败，而明明别的平台有同一首歌。
//
// 所以取链失败时按下面的顺序去别的平台**重新搜一次**再取链。
// 顺序固定，保证行为可预期（不依赖搜索返回的偶然顺序）。
const PLATFORM_FALLBACK_ORDER = ['wy', 'tx', 'kg', 'kw']

/** 归一化用于匹配的曲名：小写 → 剥掉成对括号及内容 → 去掉空白与常见分隔符 */
function normalizeForMatch(s) {
  let t = String(s || '').toLowerCase()
  for (const [open, close] of [['(', ')'], ['（', '）'], ['[', ']'], ['【', '】']]) {
    for (;;) {
      const i = t.indexOf(open)
      if (i < 0) break
      const j = t.indexOf(close, i + 1)
      if (j < 0) break
      t = t.slice(0, i) + t.slice(j + 1)
    }
  }
  return t.replace(/[\s\-_·.．，,、&/]/g, '')
}

/**
 * 在指定平台搜同一首歌。
 *
 * **必须严格匹配**：曲名归一化后要完全相等，歌手还要有交集。
 * 放宽的话会下到翻唱/同名曲 —— 比下载失败更糟（用户拿到的是错的文件）。
 */
async function findSongOnPlatform(song, platform) {
  const keyword = [song.name, song.singer].filter(Boolean).join(' ').trim()
  if (!keyword) return null

  const res = await SearchAPI.search(keyword, platform, 1)
  const list = res?.data?.list || res?.list || []

  const wantName = normalizeForMatch(song.name)
  const wantSinger = normalizeForMatch(song.singer)

  for (const cand of list) {
    if (cand.source !== platform) continue
    if (normalizeForMatch(cand.name) !== wantName) continue
    if (wantSinger) {
      const got = normalizeForMatch(cand.singer)
      if (!got || (!got.includes(wantSinger) && !wantSinger.includes(got))) continue
    }
    return cand
  }
  return null
}

/**
 * 取直链：先用自己的平台，失败再跨平台重搜。
 *
 * 请求无损（flac / flac24bit）时额外做一层**偏好**：直链明确看得出有损（`.mp3`/`.php`）
 * 就先不返回，继续找真正无损的；实在找不到才退回它（有损也比下载失败强），
 * 并带上 `qualityMismatch: true` 让上层提示用户。
 *
 * @returns {Promise<{url, headers, quality, sourceId, sourceName, platform, song, qualityMismatch?}>}
 */
async function resolveWithPlatformFallback(sourceId, song, quality, isStoppedFn) {
  const own = String(song.source || '').toLowerCase()
  const order = [own, ...PLATFORM_FALLBACK_ORDER.filter(p => p !== own)].filter(Boolean)
  const wantLossless = isLosslessTier(quality)

  const errors = []
  let lossyFallback = null // 无损要求下，第一个「看得出有损」的结果，仅作兜底

  for (const platform of order) {
    if (isStoppedFn?.()) return null
    try {
      // 自己的平台直接用原 song；换平台要先搜到那一侧的曲目 ID
      const target = platform === own ? song : await findSongOnPlatform(song, platform)
      if (!target) {
        errors.push(`${platform}: 没搜到同一首歌`)
        continue
      }
      const res = await lxRuntime.getMusicUrl(sourceId, platform, target, quality)
      if (!res?.url) {
        errors.push(`${platform}: 音源返回空直链`)
        continue
      }

      const out = { ...res, platform, song: target }
      if (!wantLossless || !urlLooksLossy(res.url)) {
        return out
      }

      // 要的是无损，这条直链明确是有损 → 留作兜底，继续找
      if (!lossyFallback) lossyFallback = out
      errors.push(`${platform}: 请求无损但直链疑似有损`)
    } catch (e) {
      errors.push(`${platform}: ${e?.message || e}`)
    }
  }

  if (lossyFallback) {
    return { ...lossyFallback, qualityMismatch: true }
  }
  throw new Error(errors.slice(0, 4).join(' | ') || '所有平台都没能取到直链')
}

// 任务数据结构：
// {
//   id, songKey, song, status, error, errorCode,
//   quality, attempts, maxAttempts,
//   activeSourceId, platform, resolvedQuality, resolvedSource, path, embedded,
//   addedAt, completedAt, _triedSources, _aborted, _retryAfter
// }
//
// 状态机：pending -> resolving -> downloading -> success
//                                └-> failed / paused

// ── 可调参数（对齐觅音 miyin 的默认值） ──

// 用户在下载面板可选的固定音质；'highest' 表示自动轮询最高音质
export const QUALITY_OPTIONS = [
  { value: 'highest', label: '最高音质（自动轮询）' },
  { value: 'flac24bit', label: 'FLAC 24bit' },
  { value: 'flac', label: 'FLAC 无损' },
  { value: '320k', label: '320K' },
  { value: '128k', label: '128K' },
]

// 重试前的基础延迟（miyin 为固定 500ms）
const RETRY_DELAY_MS = 500
// 队列持久化 key
const TASKS_STORAGE_KEY = 'fn_download_tasks_v1'
const SETTINGS_STORAGE_KEY = 'fn_download_settings_v1'

// 不可重试的错误码（权限/磁盘只读/试听片段）
const FATAL_CODES = new Set(['EACCES', 'EPERM', 'EROFS', 'PREVIEW_CLIP', 'HTTP_FATAL'])
// 可重试的错误码
const RETRYABLE_CODES = new Set([
  'ENOSPC', 'ECONNRESET', 'ECONNREFUSED', 'ETIMEDOUT', 'ENETUNREACH',
  'EAI_AGAIN', 'EPIPE', 'ECONNABORTED', 'NETWORK', 'HTTP_RETRY', 'GET_URL_FAILED',
])

const tasks = ref([])
const downloadedMap = ref({})
const isOpen = ref(false)
const currentDownloadDir = ref('/vol1/Music')

// ── 下载设置（持久化） ──
const defaultQuality = ref('highest')
const maxAttempts = ref(3)
const autoFailover = ref(true)
const concurrency = ref(1)
const embedLyric = ref(false)   // 内嵌歌词进音频标签
const writeLrc = ref(true)      // 同时写同名 .lrc 外挂歌词

// 目录选择状态
const showDirModal = ref(false)
const editDownloadDir = ref('/vol1/Music')
const isSavingDir = ref(false)
/** 保存下载目录失败时的一句原因（service 层没有 UI，交给调用方渲染） */
const dirError = ref('')
const browseFolders = ref([])
const browseParentPath = ref('')

let runningCount = 0
const isPaused = ref(false)

// 防风控限速：两次「任务启动」之间、以及「上一个任务完成后」到下次启动的间隔（秒）
const startIntervalSec = ref(5)
const doneIntervalSec = ref(3)
let lastStartedAt = 0
let lastFinishedAt = 0
let queueWakeTimer = null
let persistTimer = null

// ── 计算属性 ──
const activeCount = computed(() =>
  tasks.value.filter(t => t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading').length
)
const pausedCount = computed(() => tasks.value.filter(t => t.status === 'paused').length)
const completedCount = computed(() => tasks.value.filter(t => t.status === 'success').length)
const failedCount = computed(() => tasks.value.filter(t => t.status === 'failed').length)
const totalCount = computed(() => tasks.value.length)

// 下面两个是给「歌单下载」页的指标用的。
// 注意与 activeCount 的区别：activeCount 把「等待中」也算进去了（顶栏角标要的是"总共在跑"），
// 而这两个要把「正在下载」和「排队等待」分开显示。
/** 正在取链 / 下载中的数量（不含等待） */
const downloadingCount = computed(() =>
  tasks.value.filter(t => t.status === 'resolving' || t.status === 'downloading').length
)
/** 已入队、还没开始的数量 */
const pendingCount = computed(() => tasks.value.filter(t => t.status === 'pending').length)

/**
 * 近 24 小时成功率（整数百分比）。
 *
 * 没有样本时返回 **null** 而不是 0 —— 界面上要显示「—」。
 * 显示 0% 会让人以为「全都失败了」，而实际上只是还没跑过。
 */
const recentSuccessRate = computed(() => {
  const since = Date.now() - 24 * 60 * 60 * 1000
  const done = tasks.value.filter(
    t => (t.status === 'success' || t.status === 'failed') && (t.completedAt || 0) >= since,
  )
  if (!done.length) return null
  const ok = done.filter(t => t.status === 'success').length
  return Math.round((ok / done.length) * 100)
})

// ── 持久化 ──

function hasStorage() {
  try {
    return typeof localStorage !== 'undefined' && localStorage !== null
  } catch (_) {
    return false
  }
}

function loadSettings() {
  if (!hasStorage()) return
  try {
    const raw = localStorage.getItem(SETTINGS_STORAGE_KEY)
    if (!raw) return
    const s = JSON.parse(raw)
    if (s.defaultQuality) defaultQuality.value = s.defaultQuality
    if (typeof s.maxAttempts === 'number') maxAttempts.value = Math.min(8, Math.max(1, s.maxAttempts))
    if (typeof s.autoFailover === 'boolean') autoFailover.value = s.autoFailover
    if (typeof s.concurrency === 'number') concurrency.value = Math.min(3, Math.max(1, s.concurrency))
    if (typeof s.startIntervalSec === 'number') startIntervalSec.value = Math.min(120, Math.max(0, s.startIntervalSec))
    if (typeof s.doneIntervalSec === 'number') doneIntervalSec.value = Math.min(120, Math.max(0, s.doneIntervalSec))
    if (typeof s.embedLyric === 'boolean') embedLyric.value = s.embedLyric
    if (typeof s.writeLrc === 'boolean') writeLrc.value = s.writeLrc
  } catch (_) {}
}

function persistSettings() {
  if (!hasStorage()) return
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({
      defaultQuality: defaultQuality.value,
      maxAttempts: maxAttempts.value,
      autoFailover: autoFailover.value,
      concurrency: concurrency.value,
      startIntervalSec: startIntervalSec.value,
      doneIntervalSec: doneIntervalSec.value,
      embedLyric: embedLyric.value,
      writeLrc: writeLrc.value,
    }))
  } catch (_) {}
}

/**
 * 把一份队列数据装回内存。
 *
 * 刷新页面后无法恢复进行中的请求（后端下载是阻塞式请求），
 * 与 miyin 启动时把 running 重置为 queued 的做法一致，这里重置为 pending。
 * 本地缓存和服务端两份数据都走这里，避免两处写法分叉。
 */
function restoreTasks(list) {
  if (!Array.isArray(list)) return
  tasks.value = list.map(t => {
    const task = { ...t, _aborted: false, _retryAfter: 0 }
    if (task.status === 'resolving' || task.status === 'downloading') {
      task.status = 'pending'
      task.error = '页面刷新后重新排队'
    }
    return task
  })
  if (tasks.value.some(t => t.status === 'pending')) {
    setTimeout(() => processQueue(), 0)
  }
}

function loadTasks() {
  if (!hasStorage()) return
  try {
    const raw = localStorage.getItem(TASKS_STORAGE_KEY)
    if (!raw) return
    restoreTasks(JSON.parse(raw))
  } catch (_) {
    tasks.value = []
  }
}

/**
 * 跟 NAS 上的队列对齐。
 *
 * ⚠️ 本地 localStorage 只是**缓存**（首屏不空白、断网也能看），
 * 服务端那份才是「跟着设备走」的权威副本 —— 换浏览器/清缓存后靠它恢复
 * （2026-09-22 用户反馈：「歌曲下载记录没有了，是只保留在浏览器吗」）。
 *
 * 只有服务端**有内容**时才覆盖本地；服务端为空说明从没存过
 * （老版本升上来、或换了数据目录），这时把本地这份推上去更合理。
 */
async function syncTasksFromServer() {
  try {
    const res = await DownloadQueueAPI.load()
    if (res?.code !== 200) return
    const remote = res.data?.tasks
    if (!Array.isArray(remote) || !remote.length) {
      if (tasks.value.length) persistTasks()
      return
    }
    restoreTasks(remote)
  } catch (_) {
    // 服务端不可用就继续用本地缓存，不打扰用户
  }
}

function slimTasks() {
  // 只持久化必要字段，避免体积膨胀
  return tasks.value.map(t => ({
    id: t.id,
    songKey: t.songKey,
    song: t.song,
    status: t.status,
    error: t.error,
    errorCode: t.errorCode,
    quality: t.quality,
    attempts: t.attempts,
    activeSourceId: t.activeSourceId,
    platform: t.platform,
    resolvedQuality: t.resolvedQuality,
    resolvedSource: t.resolvedSource,
    path: t.path,
    embedded: t.embedded,
    addedAt: t.addedAt,
    completedAt: t.completedAt,
    _triedSources: t._triedSources,
    // 订阅行「停止」靠它认领自己的任务（见 stopByRow）——不持久化的话
    // 刷新页面后停止就管不到刷新前排队的那些歌
    autoRowKey: t.autoRowKey,
  }))
}

function persistTasks() {
  if (persistTimer) return
  persistTimer = setTimeout(() => {
    persistTimer = null
    const slim = slimTasks()
    // ① 本地缓存：首屏立即有内容，断网也能看
    if (hasStorage()) {
      try {
        localStorage.setItem(TASKS_STORAGE_KEY, JSON.stringify(slim))
      } catch (_) {}
    }
    // ② 落盘到 NAS：换设备/清缓存后还能看到
    DownloadQueueAPI.save(slim).catch((e) => {
      // 存不上不该影响下载本身，只记日志
      logError('下载队列落盘', '保存到 NAS 失败', e?.message || String(e))
    })
  }, 400)
}

// ── 工具函数 ──

function getSongKey(song) {
  return song.id || `${song.singer || '未知'} - ${song.name || '未知'}`
}

function isSongDownloading(song) {
  const key = getSongKey(song)
  return tasks.value.some(t =>
    t.songKey === key && (t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading')
  )
}

function isSongDownloaded(song) {
  const key1 = `${song.singer} - ${song.name}`
  const key2 = song.id
  return !!(downloadedMap.value[key1] || (key2 && downloadedMap.value[key2]))
}

function markDownloaded(song) {
  downloadedMap.value = {
    ...downloadedMap.value,
    [song.id || '']: true,
    [`${song.singer} - ${song.name}`]: true,
    ...(song.id ? { [song.id]: true } : {}),
  }
}

function isStopped(task) {
  return task._aborted || task.status === 'paused'
}

/** 已就绪音源（按健康度排序） */
function availableSourceIds() {
  try {
    return lxRuntime.getReadySourceIds()
  } catch (_) {
    return []
  }
}

/** 轮换到下一个未尝试过的音源 */
function rotateSource(task) {
  const tried = new Set(task._triedSources || [])
  if (task.activeSourceId) tried.add(task.activeSourceId)
  const all = availableSourceIds()
  const next = all.find(id => !tried.has(id)) || all[0]
  task._triedSources = [...tried]
  if (next) task.activeSourceId = next
}

/** 判断失败是否值得重试（对齐 miyin 的错误分类） */
function isRetryableFailure(code, message) {
  if (code && FATAL_CODES.has(code)) return false
  if (code && RETRYABLE_CODES.has(code)) return true
  const m = String(message || '').toLowerCase()
  if (/权限|permission denied|read-only|只读/.test(m)) return false
  return /network|fetch failed|timeout|超时|断网|socket|econn|http 5|http 429|too many|temporarily unavailable|请求失败|解析/.test(m)
}

/** 从后端返回体推断错误码 */
function classifyBackendResult(res) {
  const msg = String(res?.message || res?.data?.error || '')
  const statusMatch = msg.match(/HTTP\s*错误[:：]?\s*(\d{3})/)
  if (statusMatch) {
    const code = Number(statusMatch[1])
    if (code >= 500 || code === 429) return { code: 'HTTP_RETRY', message: msg }
    return { code: 'HTTP_FATAL', message: msg }
  }
  if (/空间不足|磁盘满|no space|disk full/i.test(msg)) return { code: 'ENOSPC', message: msg }
  if (/权限|permission|read-only|只读/i.test(msg)) return { code: 'EACCES', message: msg }
  if (/过小|无效流/.test(msg)) return { code: 'PREVIEW_CLIP', message: msg }
  return { code: 'DOWNLOAD_FAILED', message: msg || '后端下载失败' }
}

// ── 任务增删 ──

function buildTask(song, activeSourceId, status, quality = '') {
  return {
    id: `task_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`,
    songKey: getSongKey(song),
    song: { ...song },
    status,
    error: '',
    errorCode: '',
    // 未显式指定音质时用全局默认（'highest' = 自动轮询最高）
    quality: quality || defaultQuality.value,
    attempts: 0,
    activeSourceId: activeSourceId || '',
    platform: song.source || 'kw',
    resolvedQuality: '',
    resolvedSource: '',
    path: '',
    embedded: false,
    addedAt: Date.now(),
    completedAt: null,
    _triedSources: [],
    _aborted: false,
    _retryAfter: 0,
  }
}

// 添加单曲下载任务
// quality 可选：不传则用全局默认音质（DownloadQualityModal 选择后会传入）
function addSong(song, activeSourceId, autoOpen = true, quality = '') {
  const key = getSongKey(song)
  const existing = tasks.value.find(t =>
    t.songKey === key && (t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading')
  )
  if (existing) {
    if (autoOpen) isOpen.value = true
    return existing
  }

  const task = buildTask(song, activeSourceId, 'pending', quality)
  tasks.value.unshift(task)
  persistTasks()
  if (autoOpen) isOpen.value = true

  processQueue()
  return task
}

// 批量添加下载任务（默认静默后台加入队列）
//
// `pickSourceFor`（可选）：多选激活后每首歌可能要挑不同的源 ——
// 池是有序的，`song.source`（平台）决定池里谁先接。传函数而不是每首歌挂字段，
// 免得把 `_pickSourceId` 这种临时键写进任务对象、再被持久化到后端队列里。
function addBatch(songs, activeSourceId, autoOpen = false, pickSourceFor = null) {
  if (!songs || !songs.length) return

  for (const song of songs) {
    if (isSongDownloaded(song)) continue
    if (isSongDownloading(song)) continue
    const sid = (typeof pickSourceFor === 'function' && pickSourceFor(song)) || activeSourceId
    tasks.value.push(buildTask(song, sid, isPaused.value ? 'paused' : 'pending'))
  }

  persistTasks()
  if (autoOpen) isOpen.value = true
  if (!isPaused.value) processQueue()
}

function pauseTask(taskId) {
  const task = tasks.value.find(t => t.id === taskId)
  if (task) {
    task.status = 'paused'
    task._aborted = true
    persistTasks()
  }
}

function resumeTask(taskId, fallbackSourceId) {
  const task = tasks.value.find(t => t.id === taskId)
  if (task) {
    task.status = 'pending'
    task.error = ''
    task.errorCode = ''
    task._aborted = false
    task._retryAfter = 0
    if (fallbackSourceId) task.activeSourceId = fallbackSourceId
    isPaused.value = false
    persistTasks()
    processQueue()
  }
}

function cancelTask(taskId) {
  const task = tasks.value.find(t => t.id === taskId)
  if (task) task._aborted = true
  tasks.value = tasks.value.filter(t => t.id !== taskId)
  persistTasks()
}

function pauseAll() {
  isPaused.value = true
  tasks.value.forEach(t => {
    if (t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading') {
      t.status = 'paused'
      t._aborted = true
    }
  })
  persistTasks()
}

function resumeAll(fallbackSourceId) {
  isPaused.value = false
  tasks.value.forEach(t => {
    if (t.status === 'paused') {
      t.status = 'pending'
      t.error = ''
      t.errorCode = ''
      t._aborted = false
      t._retryAfter = 0
      if (fallbackSourceId) t.activeSourceId = fallbackSourceId
    }
  })
  persistTasks()
  processQueue()
}

// 停止所有进行中与等待中的任务。
//
// 关键：被停止的任务标记为 stopped 并**保留在列表里**，而不是直接删除。
// 旧实现用 filter 把非成功任务全部移除，用户点「停止全部」后面板立刻清空，
// 既看不出刚下过什么，也不知道哪些被中断了。
function stopAll() {
  const now = Date.now()
  tasks.value.forEach(t => {
    if (t.status === 'success') return
    t._aborted = true
    if (t.status === 'downloading' || t.status === 'resolving' || t.status === 'pending' || t.status === 'paused') {
      t.status = 'stopped'
      t.error = t.error || '已被用户停止'
      t.completedAt = t.completedAt || now
    }
  })
  isPaused.value = false
  persistTasks()
}

/**
 * 停止**某一个订阅**（某一行）的下载任务 —— 订阅行点「停止」时联动调用。
 *
 * 只停这一行补下载进来的任务，不动别的订阅：用户点的是「这个订阅别跑了」，
 * 不是「把整个下载队列清空」。所以不用 `stopAll()`。
 *
 * 任务靠 `autoRowKey` 认领（由 `LibraryManager.fillRow` 在入队时打上，见那里注释）。
 * 与「停止全部」一致：被停止的任务保留在列表里，不是删掉。
 */
function stopByRow(rowKey) {
  if (!rowKey) return 0
  const now = Date.now()
  let hit = 0
  tasks.value.forEach(t => {
    if (t.autoRowKey !== rowKey) return
    if (t.status === 'success') return
    t._aborted = true
    if (t.status === 'downloading' || t.status === 'resolving' || t.status === 'pending' || t.status === 'paused') {
      t.status = 'stopped'
      t.error = t.error || '订阅已停止'
      t.completedAt = t.completedAt || now
      hit++
    }
  })
  if (hit) {
    persistTasks()
    logWarn('下载', `订阅已停止，${hit} 首已入队的下载被停止`)
  }
  return hit
}

function retryTask(taskId, fallbackSourceId) {
  const task = tasks.value.find(t => t.id === taskId)
  if (task) {
    task.status = 'pending'
    task.error = ''
    task.errorCode = ''
    task._aborted = false
    task._retryAfter = 0
    if (fallbackSourceId) task.activeSourceId = fallbackSourceId
    isPaused.value = false
    persistTasks()
    processQueue()
  }
}

/**
 * 恢复所有「已停止」的任务。
 *
 * 与 `resumeAll` 分开：那个只管 `paused`（用户主动暂停），这个只管 `stopped`
 * （用户点过停止 / 订阅行停止联动停掉的）。混在一起会让「全部继续」把
 * 用户明确停掉的任务也一并恢复。
 *
 * `resumeTask` 单个恢复时也会复位 `_aborted`，这里保持一致 —— 不复位的话
 * `executeTask` 一进去就 `isStopped()` 返回，看起来像「点了没反应」。
 */
function resumeStopped(fallbackSourceId) {
  isPaused.value = false
  let hit = 0
  tasks.value.forEach(t => {
    if (t.status !== 'stopped') return
    t.status = 'pending'
    t.error = ''
    t.errorCode = ''
    t._aborted = false
    t._retryAfter = 0
    if (fallbackSourceId) t.activeSourceId = fallbackSourceId
    hit++
  })
  if (!hit) return 0
  persistTasks()
  processQueue()
  return hit
}

function retryAllFailed(fallbackSourceId) {
  isPaused.value = false
  tasks.value.forEach(t => {
    if (t.status === 'failed') {
      t.status = 'pending'
      t.error = ''
      t.errorCode = ''
      t._aborted = false
      t._retryAfter = 0
      if (fallbackSourceId) t.activeSourceId = fallbackSourceId
    }
  })
  persistTasks()
  processQueue()
}

/** 为单个任务切换音质并重新排队 */
function setTaskQuality(taskId, quality) {
  const task = tasks.value.find(t => t.id === taskId)
  if (!task) return
  task.quality = quality
  task.attempts = 0
  task._triedSources = []
  task.status = 'pending'
  task.error = ''
  task.errorCode = ''
  task._aborted = false
  task._retryAfter = 0
  isPaused.value = false
  persistTasks()
  processQueue()
}

/** 手动为任务换源并重试 */
function switchSource(taskId, sourceId) {
  const task = tasks.value.find(t => t.id === taskId)
  if (!task) return
  if (sourceId) {
    task.activeSourceId = sourceId
    task._triedSources = [...new Set([...(task._triedSources || []), sourceId])]
  } else {
    rotateSource(task)
  }
  task.attempts = 0
  task.status = 'pending'
  task.error = ''
  task.errorCode = ''
  task._aborted = false
  task._retryAfter = 0
  isPaused.value = false
  persistTasks()
  processQueue()
}

/** 批量切换音质 */
function setAllQuality(quality) {
  defaultQuality.value = quality
  persistSettings()
}

function removeTask(taskId) {
  cancelTask(taskId)
}

function clearCompleted() {
  tasks.value = tasks.value.filter(t => t.status !== 'success')
  persistTasks()
}

function clearAll() {
  tasks.value = tasks.value.filter(t => t.status === 'resolving' || t.status === 'downloading')
  persistTasks()
}

// ── 队列调度器（页面切换不会打断） ──

function scheduleQueueWake(delayMs) {
  if (queueWakeTimer) return
  queueWakeTimer = setTimeout(() => {
    queueWakeTimer = null
    processQueue()
  }, Math.max(50, delayMs))
}

function processQueue() {
  if (isPaused.value) return
  if (runningCount >= concurrency.value) return

  // 防风控：两次任务启动之间留出间隔，避免连续快速请求触发音源限流
  const wait = msUntilCanStart()
  if (wait > 0) {
    scheduleQueueWake(wait)
    return
  }

  const now = Date.now()
  const nextTask = tasks.value.find(t =>
    t.status === 'pending' && (!t._retryAfter || t._retryAfter <= now)
  )

  if (!nextTask) {
    const waiting = tasks.value.filter(t => t.status === 'pending' && t._retryAfter > now)
    if (waiting.length) {
      scheduleQueueWake(Math.min(...waiting.map(t => t._retryAfter - now)))
    }
    return
  }

  runningCount++
  lastStartedAt = Date.now()
  executeTask(nextTask).finally(() => {
    runningCount--
    lastFinishedAt = Date.now()
    if (!isPaused.value) processQueue()
  })

  if (!isPaused.value && runningCount < concurrency.value) processQueue()
}

/** 距离下一个任务允许启动还需等待多久（毫秒）。
 *
 * 这是防风控的核心：短时间内连续请求同一音源很容易被限流甚至封禁。
 * 通过「任务间隔」与「下载完成后间隔」两个维度限速，
 * 让节奏接近人工操作而不是爬虫。
 */
function msUntilCanStart() {
  const now = Date.now()
  let wait = 0

  const startGap = Math.max(0, startIntervalSec.value) * 1000
  const doneGap = Math.max(0, doneIntervalSec.value) * 1000

  if (startGap > 0 && lastStartedAt) {
    wait = Math.max(wait, lastStartedAt + startGap - now)
  }
  if (doneGap > 0 && lastFinishedAt) {
    wait = Math.max(wait, lastFinishedAt + doneGap - now)
  }
  return Math.max(0, Math.ceil(wait))
}

/** 失败处理：按错误分类决定重试 / 换源重试 / 终止 */
function handleTaskFailure(task, message, code) {
  if (isStopped(task)) return

  const attempts = (task.attempts || 0) + 1
  task.attempts = attempts
  task.errorCode = code

  const all = availableSourceIds()
  const tried = [...(task._triedSources || []), task.activeSourceId]
  const decision = decideFailover({
    code,
    message,
    attempts,
    tried,
    all,
    maxAttempts: maxAttempts.value,
    autoFailover: autoFailover.value,
    // 改造前那套「可重试」判定原样喂进去：瞬时错误与取链失败仍然可重试
    transientRetry: isRetryableFailure(code, message),
  })

  if (decision.retry) {
    if (decision.switchSource) rotateSource(task)
    task.status = 'pending'
    task.error = `失败重试(${attempts}/${maxAttempts.value})${decision.switchSource ? '·换源' : ''}: ${message}`
    task._retryAfter = Date.now() + RETRY_DELAY_MS
    persistTasks()
    scheduleQueueWake(RETRY_DELAY_MS)
    // 换源重试过程用户看不到，但排查「为什么这首一直失败」时需要 —— 记进日志
    logWarn(
      '下载',
      `${songLabel(task)} 失败，${decision.switchSource ? '换源重试' : '重试'}（${attempts}/${maxAttempts.value}）`,
      message,
    )
    return
  }

  task.status = 'failed'
  // 用户问过「下载失败怎么不换源」—— 如果原因其实是**已经没有别的源可换**，
  // 得让他看得见，否则会以为是没做换源
  const exhausted = isSourceLevelFailure(code, message) && untriedSources(tried, all).length === 0
  task.error = exhausted && all.length ? `${message}（已试过全部 ${all.length} 个音源）` : message
  task.completedAt = Date.now()
  persistTasks()
  // 队列里只显示「下载失败」四个字，真正原因（音源报错 / HTTP 状态）在这里留下
  logError('下载', `${songLabel(task)} 下载失败`, `${message}${code ? ` (code=${code})` : ''}`)
}

/** 日志里标识一首歌：歌名 + 歌手，够定位就行 */
function songLabel(task) {
  const s = task?.song || {}
  const name = s.name || s.title || s.filename || '未知曲目'
  const artist = s.singer || s.artist || ''
  return artist ? `${name} - ${artist}` : name
}

// 单个任务执行流程
async function executeTask(task) {
  if (isStopped(task)) return

  const song = task.song
  const quality = task.quality || defaultQuality.value
  const platform = task.platform || song.source || 'kw'

  task.status = 'resolving'
  task.error = ''
  task.errorCode = ''
  task.qualityMismatch = false
  task.actualFormat = ''

  let audioUrl = song.url || song.streamUrl || ''
  let resolvedQuality = ''
  let resolvedSourceName = ''
  let resolvedSourceId = task.activeSourceId || ''

  // ── 1. 解析直链阶段：音质阶梯 × 多音源轮询 × 跨平台回退 ──
  if (!audioUrl && song.source !== 'nas') {
    try {
      // 自己的平台取不到就去别的平台重搜（见 resolveWithPlatformFallback）
      const urlRes = await resolveWithPlatformFallback(
        resolvedSourceId, song, quality, () => isStopped(task),
      )
      if (isStopped(task)) return
      if (!urlRes?.url) throw new Error('音源返回空下载直链')

      audioUrl = urlRes.url
      resolvedQuality = urlRes.quality || ''
      resolvedSourceId = urlRes.sourceId || resolvedSourceId
      resolvedSourceName = urlRes.sourceName || ''
      task.activeSourceId = resolvedSourceId
      task._triedSources = [...new Set([...(task._triedSources || []), resolvedSourceId].filter(Boolean))]
      task.resolvedSource = resolvedSourceName
      task.actualReferer = urlRes.headers?.Referer || urlRes.headers?.referer || ''

      // 跨平台取到的曲目：记下实际平台与曲目信息。
      // 用途有二：① 任务列表显示真实来源 ② 失败重试时 task.song 已指向回退平台的曲目，
      // 重试会直接在该平台再取一次，不会再从原平台绕一圈。
      // 注意：**落盘用的 payload 仍以原曲为准**（见下方 payload），因为
      //   - 曲名/歌手是严格匹配过的，两者一致
      //   - 后端只读 name/singer/album/cover/genre，不读 source/songmid/hash
      //   - 保留原曲的 album/cover，用户看到的就是他在原平台搜到的那张封面
      //   - markDownloaded 也按原曲的 id 记账，换掉会导致列表里的「已下载」勾丢失
      if (urlRes.platform && urlRes.platform !== String(song.source || '').toLowerCase()) {
        task.resolvedPlatform = urlRes.platform
        task.song = { ...song, ...urlRes.song }
        task.platform = urlRes.platform
      }
    } catch (resolveErr) {
      if (isStopped(task)) return
      const msg = resolveErr?.message || '音源解析直链失败'
      // 取链失败在 highest 模式下可重试换源（对齐 miyin）
      return handleTaskFailure(task, msg, 'GET_URL_FAILED')
    }
  }

  if (isStopped(task)) return

  // ── 2. 落盘阶段 ──
  task.status = 'downloading'
  task.resolvedQuality = resolvedQuality

  const payload = {
    ...song,
    url: audioUrl,
    quality: resolvedQuality || (isHighestQuality(quality) ? '' : quality),
    referer: task.actualReferer || song.referer || '',
    embed_lyric: embedLyric.value,
    write_lrc: writeLrc.value,
  }

  try {
    const res = await DownloadAPI.downloadSong(payload)
    if (isStopped(task)) return

    const resultStatus = res?.data?.status || res?.result?.status
    if (res?.code === 200 && resultStatus !== 'failed') {
      task.status = 'success'
      task.completedAt = Date.now()
      task.resolvedQuality = res?.data?.quality || resolvedQuality
      task.actualFormat = res?.data?.actual_format || ''
      task.path = res?.data?.path || ''
      task.embedded = !!res?.data?.embedded
      // 请求无损、真实容器却有损 → 记下来给用户看（后端按魔数判定，是权威结论）
      task.qualityMismatch = isLosslessTier(quality) && isLossyContainer(task.actualFormat)
      markDownloaded(song)
      persistTasks()
    } else {
      const cls = classifyBackendResult(res)
      handleTaskFailure(task, cls.message, cls.code)
    }
  } catch (dlErr) {
    if (isStopped(task)) return
    handleTaskFailure(task, dlErr.message || '网络连接异常', 'NETWORK')
  }
}

// ── 目录浏览与保存 ──

async function openDirModal() {
  editDownloadDir.value = currentDownloadDir.value
  showDirModal.value = true
  browseDir(currentDownloadDir.value)
}

async function browseDir(path) {
  try {
    const res = await NasAPI.browse(path)
    if (res.code === 200 && res.data) {
      browseFolders.value = res.data.folders || []
      browseParentPath.value = res.data.parent || ''
      if (res.data.current) editDownloadDir.value = res.data.current
    }
  } catch (e) {
    console.warn('Failed to browse dir:', e)
  }
}

async function saveDownloadDir() {
  const dir = editDownloadDir.value.trim()
  if (!dir) return
  isSavingDir.value = true
  dirError.value = ''
  try {
    const res = await DownloadAPI.saveConfig(dir)
    const bad = apiError(res, '保存失败')
    if (bad) {
      dirError.value = bad
      logError('保存下载目录', bad, `code=${res?.code} message=${res?.message} dir=${dir}`)
      return
    }
    currentDownloadDir.value = dir
    showDirModal.value = false
  } catch (e) {
    // 这里以前是 alert（service 层没有 UI）—— 现在把一句话原因交给调用方渲染
    dirError.value = fail('保存下载目录', e)
  } finally {
    isSavingDir.value = false
  }
}

// 初始化加载配置
async function loadConfig() {
  try {
    const res = await DownloadAPI.getConfig()
    if (res.code === 200) {
      currentDownloadDir.value = res.download_dir || res.default_nas_dir || '/vol1/Music'
      editDownloadDir.value = currentDownloadDir.value
    }
  } catch (e) {
    console.warn('Failed to load download config:', e)
  }
}

// 批量查询已下载状态
async function checkSongsDownloaded(songs) {
  if (!songs || !songs.length) return
  try {
    const payload = songs.map(s => ({
      id: s.id || '',
      name: s.name || '',
      singer: s.singer || '',
    }))
    const res = await DownloadAPI.checkDownloaded(payload)
    if (res.code === 200 && res.exists) {
      downloadedMap.value = { ...downloadedMap.value, ...res.exists }
    }
  } catch (e) {
    console.warn('Failed to check download status:', e)
  }
}

// ── 设置写入辅助 ──

function updateSettings(patch) {
  if ('defaultQuality' in patch) defaultQuality.value = patch.defaultQuality
  if ('maxAttempts' in patch) maxAttempts.value = Math.min(8, Math.max(1, patch.maxAttempts))
  if ('autoFailover' in patch) autoFailover.value = !!patch.autoFailover
  if ('concurrency' in patch) concurrency.value = Math.min(3, Math.max(1, patch.concurrency))
  if ('startIntervalSec' in patch) startIntervalSec.value = Math.min(120, Math.max(0, Number(patch.startIntervalSec)))
  if ('doneIntervalSec' in patch) doneIntervalSec.value = Math.min(120, Math.max(0, Number(patch.doneIntervalSec)))
  if ('embedLyric' in patch) embedLyric.value = !!patch.embedLyric
  if ('writeLrc' in patch) writeLrc.value = !!patch.writeLrc
  persistSettings()
  processQueue()
}

loadSettings()
// 先用本地缓存把首屏撑起来（同步、不闪烁），再异步跟 NAS 上的队列对齐。
// 服务端那份才是「跟着设备走」的权威副本 —— 换浏览器/清缓存后靠它恢复。
loadTasks()
syncTasksFromServer()

export const downloadManager = {
  tasks,
  downloadedMap,
  isOpen,
  isPaused,
  currentDownloadDir,
  showDirModal,
  editDownloadDir,
  isSavingDir,
  dirError,
  browseFolders,
  browseParentPath,

  // 下载设置
  defaultQuality,
  maxAttempts,
  autoFailover,
  concurrency,
  startIntervalSec,
  doneIntervalSec,
  embedLyric,
  writeLrc,
  updateSettings,
  QUALITY_OPTIONS,
  qualityLabel,

  activeCount,
  pausedCount,
  completedCount,
  failedCount,
  totalCount,
  downloadingCount,
  pendingCount,
  recentSuccessRate,

  open: () => { isOpen.value = true },
  close: () => { isOpen.value = false },
  toggle: () => { isOpen.value = !isOpen.value },

  addSong,
  addBatch,
  pauseTask,
  resumeTask,
  cancelTask,
  pauseAll,
  resumeAll,
  resumeStopped,
  stopAll,
  stopByRow,
  retryTask,
  retryAllFailed,
  removeTask,
  clearCompleted,
  clearAll,

  // 新增：音质 / 换源
  setTaskQuality,
  switchSource,
  setAllQuality,
  availableSourceIds,
  sourceHealth,
  isRetryableFailure,

  isSongDownloading,
  isSongDownloaded,
  checkSongsDownloaded,
  loadConfig,

  openDirModal,
  browseDir,
  saveDownloadDir,
}
