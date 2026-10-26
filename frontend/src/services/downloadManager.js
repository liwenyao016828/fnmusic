/**
 * 下载队列 —— 前端只做「提交 / 展示 / 控制」，真正的下载在 NAS 后端。
 *
 * 2026-10-23 架构收敛（用户口径）：只保留**一套**后台持久队列。
 * 所有 addSong/addBatch 都只是 POST 到 `backend/pkg/backenddl`；取链、下载、
 * 落盘、重试都由 Go worker 执行，关掉本页也继续。这里不再有：
 *   · 浏览器取链（lxRuntime / 平台直链）
 *   · 浏览器落盘（DownloadAPI.downloadSong）
 *   · processQueue 执行循环 / 换源 / 音质降级 / 本地自动重试
 *
 * `tasks` 是后端队列的界面映射，字段形状尽量保持旧组件认识的样子。
 * 唯一对账键是 client_id；旧本地任务做一次幂等迁移（见 downloadMigration.js），
 * 成功记录只导入历史，失败/停止/暂停保留原意，绝不因为迁移自动开跑。
 */
import { computed, ref, watch } from 'vue'
import { ConfigAPI, DownloadAPI, NasAPI } from '../api/client.js'
import { qualityOverride } from './qualityOverride.js'
import { qualityLabel, isHighestQuality } from '../engine/quality.js'
import { logError } from './appLog.js'
import { fail, apiError } from './userMsg.js'
import { loadBool, savePref } from './prefs.js'
import { backendDownload } from './backendDownload.js'
import {
  MIGRATION_FLAG,
  MIGRATION_BATCH_SIZE,
  songToBackendItem,
  platformSongId,
  legacyTaskToBackendItem,
  splitMigrationReceipt,
  legacyTasksToMigrate,
} from './downloadMigration.js'

export const QUALITY_OPTIONS = [
  { value: 'highest', label: '最高音质（自动轮询）' },
  { value: 'flac24bit', label: 'FLAC 24bit' },
  { value: 'flac', label: 'FLAC 无损' },
  { value: '320k', label: '320K' },
  { value: '128k', label: '128K' },
]

const TASKS_STORAGE_KEY = 'fn_download_tasks_v1'
const SETTINGS_STORAGE_KEY = 'fn_download_settings_v1'
const ROW_KEY_STORAGE_KEY = 'fn_download_row_keys_v1'
// 提交中断/失败但后端未确认的占位。只存这些，不存后端已确认的任务。
const UNSENT_STORAGE_KEY = 'fn_download_unsent_v1'

// ── 状态 ──

const tasks = ref([])
const downloadedMap = ref({})
const isOpen = ref(false)
const currentDownloadDir = ref('/vol1/Music')
const lastActionError = ref('')
const migration = ref({ active: false, total: 0, remaining: 0, done: false })

// 设置：只保留「音质」是真正逐任务生效的；其余保留字段是为了旧组件不炸。
const defaultQuality = ref('highest')
const qualityOverrideSetting = ref('')
const maxAttempts = ref(3)
const autoFailover = ref(true)
const concurrency = ref(1)
const embedLyric = ref(false)
const writeLrc = ref(true)
const startIntervalSec = ref(5)
const doneIntervalSec = ref(3)

// 目录弹窗
const showDirModal = ref(false)
const isSavingDir = ref(false)
const dirError = ref('')
const browseFolders = ref([])
const browseParentPath = ref('')
const editDownloadDir = ref('/vol1/Music')

/**
 * 目录浏览里是否连 `.`/`$`/`@` 开头的隐藏目录一起列出来。
 *
 * 用户 2026-10-25 报障「曲库在 music-v2/.dev/music，中间的 .dev 在目录选择器里看不到」——
 * 这个弹窗（下载页/发现页的「设置目录」）以前根本不传 hidden，于是那条路永远选不到点目录。
 * 与 `NasDirPicker.vue` 共用同一个偏好键，两边开关状态一致。
 */
const browseShowHidden = ref(loadBool('nas_show_hidden', false))
const browseHiddenCount = ref(0)
const browseHiddenDirs = ref([])

// ── 后端任务 → 界面任务 ──

// client_id 稳定，所以同一任务在多次轮询里复用同一个对象：
// pushEngine 的 watchQueueDrain / watchFillCompletion 靠对象引用观察状态变化。
const taskCache = new Map()
const provisional = ref([])
const rowKeys = ref(loadRowKeys())

function loadRowKeys() {
  try {
    const raw = localStorage.getItem(ROW_KEY_STORAGE_KEY)
    return raw ? JSON.parse(raw) : {}
  } catch (_) { return {} }
}

function persistRowKeys() {
  try { localStorage.setItem(ROW_KEY_STORAGE_KEY, JSON.stringify(rowKeys.value)) } catch (_) {}
}

function setTaskRowKey(task, rowKey) {
  if (!task || !rowKey) return
  task.autoRowKey = rowKey
  const key = task.clientId || task.id
  if (key) {
    rowKeys.value = { ...rowKeys.value, [key]: rowKey }
    persistRowKeys()
  }
}

function songKeyOf(song) {
  const s = song || {}
  return s.id || s.songmid || s.song_id || `${s.singer || s.artist || ''} - ${s.name || s.song_name || ''}`
}

function mapBackendItem(item) {
  const key = item.clientId || item.id
  // 占位任务已经挂在 tasks 里（PushManager 可能刚给它打了 autoRowKey），
  // 后端队列第一次出现时继续用同一个对象，watcher 的引用观察才不会断。
  let t = taskCache.get(key) || provisional.value.find(p => (p.clientId || p.id) === key)
  if (!t) {
    t = {}
  }
  taskCache.set(key, t)
  const song = {
    id: item.platformId || item.clientId || item.id,
    songmid: item.platformId || '',
    source: item.platform || '',
    name: item.name,
    singer: item.singer,
    album: item.album,
    duration: item.duration,
    cover: item.cover,
  }
  Object.assign(t, {
    id: key,
    clientId: item.clientId,
    _backendId: item.id,
    songKey: item.clientId || `${item.singer} - ${item.name}`,
    song,
    status: item.status,
    error: item.error || '',
    errorCode: item.error ? 'BACKEND' : '',
    quality: item.quality || 'highest',
    attempts: item.tries || 0,
    platform: item.platform || '',
    resolvedQuality: item.actualQuality || '',
    resolvedSource: item.source || '',
    path: item.path || '',
    size: item.size || 0,
    addedAt: item.addedAt || 0,
    completedAt: item.status === 'success' ? (item.updatedAt || item.addedAt || 0) : 0,
    autoRowKey: rowKeys.value[key] || t.autoRowKey || '',
    actualFormat: '',
    embedded: false,
    _submitItem: null,
  })
  if (t.status === 'success') markDownloaded(song, t.path)
  return t
}

function syncTasks() {
  const list = []
  const seen = new Set()
  for (const item of backendDownload.items.value) {
    const t = mapBackendItem(item)
    list.push(t)
    seen.add(t.clientId || t.id)
  }
  // 已提交但后端队列还没刷新出来的：保留占位，避免任务「闪一下消失」。
  for (const p of provisional.value) {
    if (seen.has(p.clientId || p.id)) continue
    list.push(p)
  }
  tasks.value = list
  // 后台队列里已经出现的占位可以退休了（对象本身被 mapBackendItem 复用了）。
  if (provisional.value.some(p => seen.has(p.clientId || p.id))) {
    provisional.value = provisional.value.filter(p => !seen.has(p.clientId || p.id))
  }
  persistUnsent()
}

// 必须早于下面这个 immediate watcher 声明：watcher 在模块求值阶段就会立刻跑
// syncTasks → persistUnsent，那时若 unsentReady 还在 TDZ，抛的是 ReferenceError
// 「Cannot access 'unsentReady' before initialization」——整页白屏（v2.18.0 首版踩过）。
let unsentReady = false

watch(backendDownload.items, syncTasks, { immediate: true })

// ── 计数 ──

const countBy = (...statuses) => computed(() => tasks.value.filter(t => statuses.includes(t.status)).length)
// 后台明确说了「现在不能下」时，界面别再假报「已加入队列」。
// null = 还没探测完，不拦；false = 后端报告不可用，入队必失败。
const canEnqueue = computed(() => backendDownload.ready.value !== false)
const enqueueBlockReason = computed(() => (backendDownload.ready.value === false
  ? (backendDownload.unavailableReason.value || backendDownload.errorText.value || '后台下载当前不可用')
  : ''))

const activeCount = computed(() => tasks.value.filter(t => t.status === 'pending' || t.status === 'downloading').length)
const queuePaused = computed(() => !!backendDownload.paused.value)
const pendingCount = countBy('pending')
const downloadingCount = countBy('downloading')
const pausedCount = countBy('paused')
const stoppedCount = countBy('stopped')
const failedCount = countBy('failed')
const completedCount = countBy('success')
const totalCount = computed(() => tasks.value.length)
const recentSuccessRate = computed(() => {
  const terminal = tasks.value.filter(t => ['success', 'failed', 'stopped'].includes(t.status)).slice(-50)
  if (!terminal.length) return 0
  return Math.round(terminal.filter(t => t.status === 'success').length * 100 / terminal.length)
})

function effectiveDefaultQuality() {
  return qualityOverride(defaultQuality.value, qualityOverrideSetting.value)
}

// ── 本地已下载标记 ──

function markDownloaded(song, path = '') {
  if (!song) return
  const name = song.singer || song.artist
    ? `${song.singer || song.artist} - ${song.name || song.song_name || ''}`
    : (song.name || '')
  const patch = {}
  if (song.id) patch[song.id] = path || true
  if (song.songmid) patch[song.songmid] = path || true
  if (name) patch[name] = path || true
  downloadedMap.value = { ...downloadedMap.value, ...patch }
}

function isSongDownloaded(song) {
  if (!song) return false
  const id = song.id || song.songmid || song.song_id
  if (id && downloadedMap.value[id]) return true
  const name = `${song.singer || song.artist || ''} - ${song.name || song.song_name || ''}`
  if (downloadedMap.value[name]) return true
  return tasks.value.some(t => t.status === 'success' && (
    (id && (t.song?.id === id || t.song?.songmid === id)) ||
    (name !== ' - ' && `${t.song?.singer || ''} - ${t.song?.name || ''}` === name)
  ))
}

function isSongDownloading(song) {
  if (!song) return false
  const id = song.id || song.songmid || song.song_id
  const name = `${song.singer || song.artist || ''} - ${song.name || song.song_name || ''}`
  return tasks.value.some(t => {
    if (!['pending', 'downloading', 'paused'].includes(t.status)) return false
    if (id && (t.song?.id === id || t.song?.songmid === id || t.songKey === id)) return true
    return name !== ' - ' && `${t.song?.singer || ''} - ${t.song?.name || ''}` === name
  })
}

async function checkSongsDownloaded(songs) {
  if (!Array.isArray(songs) || !songs.length) return
  try {
    const payload = songs.map(s => ({
      id: s.id || s.songmid || '',
      name: s.name || s.song_name || '',
      singer: s.singer || s.artist || '',
      album: s.album || '',
      duration: Number(s.duration || s.interval || 0) || 0,
      // 不要固定 highest：用户可能把默认档位/覆盖开关调低了。
      quality: s.quality || effectiveDefaultQuality(),
    }))
    const res = await DownloadAPI.checkDownloaded(payload)
    if (res?.code === 200 && res.exists) {
      downloadedMap.value = { ...downloadedMap.value, ...res.exists }
    }
  } catch (e) {
    console.warn('Failed to check download status:', e)
  }
}

// ── 入队 ──

let provisionalSeq = 0

function makeClientId() {
  const seq = (++provisionalSeq).toString(36)
  return `ui-${Date.now().toString(36)}-${seq}-${Math.random().toString(36).slice(2, 7)}`
}

function makeProvisional(song, sourceId, quality) {
  const clientId = makeClientId()
  const item = songToBackendItem(song, { clientId, sourceId, quality: quality || effectiveDefaultQuality() })
  const task = {
    id: clientId,
    clientId,
    _backendId: '',
    songKey: songKeyOf(song),
    song: {
      ...song,
      id: song.id || song.songmid || '',
      songmid: song.songmid || '',
      source: song.source || '',
      name: song.name || song.song_name || '',
      singer: song.singer || song.artist || '',
      album: song.album || '',
    },
    status: 'pending',
    error: '',
    errorCode: '',
    quality: item.quality,
    attempts: 0,
    platform: item.platform,
    resolvedQuality: '',
    resolvedSource: '',
    path: '',
    addedAt: Date.now(),
    completedAt: 0,
    autoRowKey: '',
    _submitItem: item,
  }
  return task
}

function persistUnsent() {
  // 模块加载时 items watcher 会先 immediate 跑一次；那时还没读旧占位，
  // 绝不能把磁盘上的「未确认提交」当成空队列擦掉。
  if (!unsentReady) return
  try {
    const rows = provisional.value
      .filter(p => p._submitItem && !p._backendId && p.status !== 'success' && p.status !== 'paused' && p.status !== 'stopped')
      .map(p => ({
        clientId: p.clientId,
        song: p.song,
        item: p._submitItem,
        quality: p.quality,
        autoRowKey: p.autoRowKey || '',
        addedAt: p.addedAt || 0,
      }))
    if (rows.length) localStorage.setItem(UNSENT_STORAGE_KEY, JSON.stringify(rows))
    else localStorage.removeItem(UNSENT_STORAGE_KEY)
  } catch (_) {}
}

function loadUnsent() {
  try {
    const raw = localStorage.getItem(UNSENT_STORAGE_KEY)
    if (!raw) return
    const rows = JSON.parse(raw)
    if (!Array.isArray(rows)) return
    const loaded = []
    for (const row of rows) {
      if (!row?.clientId || !row?.item) continue
      loaded.push({
        id: row.clientId,
        clientId: row.clientId,
        _backendId: '',
        songKey: row.item.platform_id || row.clientId,
        song: row.song || {},
        // 上次页面在请求途中关掉/网络失败：不算后台失败，提示用户重试；
        // 重试复用同一 client_id，后端会幂等返回同一条，不会重复下载。
        status: 'failed',
        error: '上次提交到后台未确认，点重试重新提交（同一条任务不会重复下载）',
        errorCode: 'SUBMIT_UNKNOWN',
        quality: row.quality || 'highest',
        attempts: 0,
        platform: row.item.platform || '',
        resolvedQuality: '',
        resolvedSource: '',
        path: '',
        addedAt: row.addedAt || Date.now(),
        completedAt: 0,
        autoRowKey: row.autoRowKey || '',
        _submitItem: row.item,
      })
    }
    if (loaded.length) provisional.value = [...provisional.value, ...loaded]
  } catch (_) {
    // 存储坏了也不能一直不落盘新任务。
  } finally {
    unsentReady = true
  }
}

function markProvisionalFailed(task, reason) {
  task.status = 'failed'
  task.error = reason || '提交到后台失败'
  task.errorCode = 'SUBMIT_FAILED'
  task.completedAt = 0
}

async function submitProvisional(list, { force = false } = {}) {
  if (!list.length) return { added: 0, duplicate: 0, skipped: 0 }
  const items = list.map(t => t._submitItem).filter(Boolean)
  try {
    const receipt = await backendDownload.submitItems(items, { force })
    const confirmed = new Set([
      ...(receipt.added || []).map(x => x.client_id),
      ...(receipt.skipped || []).filter(x => String(x.reason).toLowerCase() === 'duplicate').map(x => x.client_id),
    ])
    const skipped = new Map(
      (receipt.skipped || []).filter(x => String(x.reason).toLowerCase() !== 'duplicate')
        .map(x => [x.client_id, x.reason]),
    )
    for (const t of list) {
      if (confirmed.has(t.clientId)) continue
      const reason = skipped.get(t.clientId)
      markProvisionalFailed(t, reason ? skipReasonText(reason) : '后台没有确认接收，任务已保留，可重试')
    }
    // 刷新队列后，已确认的占位会被真正的后台任务替换（同一个 client_id）。
    await backendDownload.refreshQueue().catch(() => {})
    // 只把仍未被后台确认的占位留下。
    const backendIds = new Set(backendDownload.items.value.map(i => i.clientId).filter(Boolean))
    provisional.value = provisional.value.filter(p => !backendIds.has(p.clientId))
    syncTasks()
    return {
      added: confirmed.size,
      duplicate: (receipt.skipped || []).filter(x => String(x.reason).toLowerCase() === 'duplicate').length,
      skipped: skipped.size,
    }
  } catch (e) {
    const msg = e?.userMessage || e?.message || '提交到后台失败'
    // 最危险的边界：请求超时/断线，但后端其实已经入队。
    // 这时「把任务标记失败」会让用户重试时又提交一遍。先对账一次队列：
    // 只要同一 client_id 已经在后台，就认领它；没对上的才标失败并保留原记录。
    await backendDownload.refreshQueue().catch(() => {})
    const backendIds = new Set(backendDownload.items.value.map(i => i.clientId).filter(Boolean))
    const recovered = list.filter(t => backendIds.has(t.clientId))
    if (recovered.length) {
      const recoveredIds = new Set(recovered.map(t => t.clientId))
      provisional.value = provisional.value.filter(p => !recoveredIds.has(p.clientId))
      for (const t of list) if (!recoveredIds.has(t.clientId)) markProvisionalFailed(t, msg)
      syncTasks()
      return { added: recovered.length, duplicate: 0, skipped: list.length - recovered.length, recovered: recovered.length }
    }
    for (const t of list) markProvisionalFailed(t, msg)
    syncTasks()
    throw e
  }
}

function skipReasonText(reason) {
  return ({
    no_channel: '后台没有可用渠道取到这首（缺平台 id，或取音渠道未就绪）',
    invalid: '后台认为任务信息不完整',
    duplicate_track: '后台队列里已有同名同歌手的任务',
    duplicate: '后台队列里已有这条任务',
  })[reason] || `后台未接收（${reason || '未知原因'}）`
}

/**
 * 加一首到后台队列。同步返回占位任务（旧调用方要用它观察完成/打 autoRowKey），
 * 提交在后台异步完成；失败会把占位标成 failed 并保留，绝不静默丢。
 */
function addSong(song, activeSourceId, autoOpen = true, quality = '') {
  const name = song?.name || song?.song_name
  if (!name) return null
  if (isSongDownloading(song)) return null
  const task = makeProvisional(song, activeSourceId, quality)
  provisional.value = [...provisional.value, task]
  if (!canEnqueue.value) {
    markProvisionalFailed(task, enqueueBlockReason.value)
    syncTasks()
  } else {
    syncTasks()
    submitProvisional([task]).catch(() => {})
  }
  if (autoOpen) isOpen.value = true
  return task
}

function addBatch(songs, activeSourceId, autoOpen = false, pickSourceFor = null) {
  const list = (Array.isArray(songs) ? songs : []).filter(s => s && (s.name || s.song_name))
  const created = []
  for (const song of list) {
    if (isSongDownloading(song)) continue
    const sid = (typeof pickSourceFor === 'function' ? pickSourceFor(song) : '') || activeSourceId || ''
    created.push(makeProvisional(song, sid, ''))
  }
  if (!created.length) return []
  provisional.value = [...provisional.value, ...created]
  if (!canEnqueue.value) {
    for (const t of created) markProvisionalFailed(t, enqueueBlockReason.value)
    syncTasks()
    if (autoOpen) isOpen.value = true
    return created
  }
  syncTasks()
  if (autoOpen) isOpen.value = true
  // 分块提交，避免一次 POST 太大；每块独立成败，失败块保留原占位。
  ;(async () => {
    for (let i = 0; i < created.length; i += MIGRATION_BATCH_SIZE) {
      await submitProvisional(created.slice(i, i + MIGRATION_BATCH_SIZE)).catch(() => {})
    }
  })()
  return created
}

// ── 控制：全部走后端 actions ──

function taskById(id) {
  return tasks.value.find(t => t.id === id || t.clientId === id || t._backendId === id) || null
}

function backendIds(ids) {
  const out = []
  for (const id of Array.isArray(ids) ? ids : [ids]) {
    const t = taskById(id)
    if (t?._backendId) out.push(t._backendId)
  }
  return out
}

async function runAction(action, ids) {
  const bid = backendIds(ids)
  if (!bid.length) {
    lastActionError.value = '任务还没同步到后台，请稍后再试'
    return 0
  }
  lastActionError.value = ''
  try {
    return await backendDownload.act(action, bid)
  } catch (e) {
    lastActionError.value = e?.userMessage || e?.message || '操作失败'
    logError('后台队列操作', lastActionError.value, `action=${action}`)
    throw e
  }
}

const pauseTask = (id) => runAction('pause', [id])
const resumeTask = (id) => runAction('resume', [id])
const cancelTask = (id) => runAction('stop', [id])

/**
 * 按 id 批量暂停 / 停止（2026-10-25 #4 新增）。
 *
 * 以前只有整队操作（`pauseAll` 是后端不带 ids 的整队开关，`stopAll` 停的是**全部**
 * 在跑任务）和逐条操作两条路，中间缺了「只动我勾的这几条」—— 挑几首正在下的歌暂停
 * 只能一首一首点右侧小按钮。这两个函数就是这个缺口，走的仍是 `runAction`，
 * 所以「任务还没同步到后台」之类的判断与单条完全一致。
 */
async function pauseTasksByIds(ids) {
  const list = (Array.isArray(ids) ? ids : []).map(taskById).filter(Boolean)
  const targets = list.filter(t => ['pending', 'downloading'].includes(t.status))
  return targets.length ? runAction('pause', targets.map(t => t.id)) : 0
}

async function stopTasksByIds(ids) {
  const list = (Array.isArray(ids) ? ids : []).map(taskById).filter(Boolean)
  const targets = list.filter(t => ['pending', 'downloading', 'paused'].includes(t.status))
  return targets.length ? runAction('stop', targets.map(t => t.id)) : 0
}

const removeTask = (id) => removeTasks([id])
async function removeTasks(ids) {
  const list = (Array.isArray(ids) ? ids : [ids]).map(taskById).filter(Boolean)
  // 从没被后台确认的占位（提交失败/离线）在后台没有记录，只能本地删；
  // 直接发 remove 会 hit=0，任务永远留在列表里，看起来像「删不掉」。
  const unsynced = list.filter(t => !t._backendId)
  if (unsynced.length) {
    const drop = new Set(unsynced.map(t => t.clientId || t.id))
    provisional.value = provisional.value.filter(t => !drop.has(t.clientId || t.id))
    syncTasks()
  }
  const syncedIds = list.filter(t => t._backendId).map(t => t.id)
  const hit = await (syncedIds.length ? runAction('remove', syncedIds) : Promise.resolve(0))
  return (hit || 0) + unsynced.length
}
const retryTask = (id) => retryTasksByIds([id])

// 整队暂停/继续：后端 pause/resume 不带 ids 是 data-level 的整队开关，
// 比逐条发 ids 稳（新入队的任务也在开关覆盖范围内）。
async function pauseAll() {
  lastActionError.value = ''
  try {
    return await backendDownload.act('pause', [])
  } catch (e) {
    lastActionError.value = e?.userMessage || e?.message || '暂停队列失败'
    throw e
  }
}

async function resumeAll() {
  lastActionError.value = ''
  try {
    // 单条暂停的任务要带 ids 才会回到 pending；整队暂停标志再单独恢复。
    const pausedIds = tasks.value.filter(t => t.status === 'paused').map(t => t.id)
    if (pausedIds.length) await runAction('resume', pausedIds)
    if (backendDownload.paused.value) return await backendDownload.act('resume', [])
    return pausedIds.length
  } catch (e) {
    lastActionError.value = e?.userMessage || e?.message || '继续队列失败'
    throw e
  }
}

async function resumeStopped() {
  return runAction('resume', tasks.value.filter(t => t.status === 'stopped' || t.status === 'paused').map(t => t.id))
}

async function stopAll() {
  return runAction('stop', tasks.value.filter(t => ['pending', 'downloading', 'paused'].includes(t.status)).map(t => t.id))
}

async function stopByRow(rowKey) {
  const ids = tasks.value.filter(t => t.autoRowKey === rowKey && ['pending', 'downloading', 'paused'].includes(t.status)).map(t => t.id)
  return ids.length ? runAction('stop', ids) : 0
}

async function retryAllFailed() {
  lastActionError.value = ''
  try {
    return await backendDownload.retryFailed()
  } catch (e) {
    lastActionError.value = e?.userMessage || e?.message || '重试失败'
    throw e
  }
}

async function retryTasksByIds(ids) {
  const list = (Array.isArray(ids) ? ids : []).map(taskById).filter(Boolean)
  const retry = list.filter(t => ['failed', 'stopped'].includes(t.status))
  const success = list.filter(t => t.status === 'success')
  let hit = 0
  // 从没被后台确认过的占位（提交失败 / 超时）没有 backend id，发 action 只会得到
  // 「任务还没同步」；用同一个 client_id 重新提交才是幂等的重试路径。
  const unsynced = retry.filter(t => !t._backendId && t._submitItem)
  const synced = retry.filter(t => t._backendId)
  if (unsynced.length) {
    const receipt = await submitProvisional(unsynced)
    hit += receipt?.added || 0
  }
  if (synced.length) hit += await runAction('retry', synced.map(t => t.id))
  if (success.length) {
    const receipt = await resubmitTasks(success)
    hit += receipt?.added?.length || 0
  }
  return hit
}

async function resumeTasksByIds(ids) {
  const list = (Array.isArray(ids) ? ids : []).map(taskById).filter(t => t && ['paused', 'stopped'].includes(t.status))
  return list.length ? runAction('resume', list.map(t => t.id)) : 0
}

async function resubmitTasks(list, quality = '') {
  const items = list.map(t => ({
    client_id: t.clientId || t.id,
    name: t.song?.name || '',
    singer: t.song?.singer || '',
    album: t.song?.album || '',
    duration: Number(t.song?.duration || 0) || 0,
    cover: t.song?.cover || '',
    platform: t.platform || t.song?.source || '',
    platform_id: platformSongId(t.song, t.platform || t.song?.source),
    quality: quality || t.quality || effectiveDefaultQuality(),
  }))
  // 后端单批上限 500 条（超过直接 400）。一批失败不能让整次操作白干，
  // 所以按批提交、累积回执；某批失败时把后面的也标成未确认，交给调用方
  // 决定重试 —— 已成功的批次不能被回滚掉（同 client_id 重提是幂等的）。
  const added = []
  const skipped = []
  for (let i = 0; i < items.length; i += MIGRATION_BATCH_SIZE) {
    const chunk = items.slice(i, i + MIGRATION_BATCH_SIZE)
    const receipt = await backendDownload.submitItems(chunk, { force: true })
    if (receipt?.added) added.push(...receipt.added)
    if (receipt?.skipped) skipped.push(...receipt.skipped)
  }
  await backendDownload.refreshQueue().catch(() => {})
  return { added, skipped }
}

async function redownloadTasksByIds(ids) {
  const list = (Array.isArray(ids) ? ids : []).map(taskById).filter(Boolean)
  if (!list.length) return 0
  const receipt = await resubmitTasks(list)
  // 后端对同 client_id 的终态任务会回 existing:true，added 里仍算一条，
  // 因此这里直接数 added 就是「真正重新排队了几首」。
  return receipt?.added?.length || 0
}

async function setTaskQuality(id, quality) {
  const t = taskById(id)
  if (!t) return null
  if (['pending', 'downloading', 'paused'].includes(t.status)) {
    lastActionError.value = '任务正在后台队列里，先停止再改音质'
    return null
  }
  return resubmitTasks([t], quality)
}

function switchSource() {
  // 单一后台队列由服务端按渠道顺序取源，前端不再逐任务切源。
  lastActionError.value = '后台下载由服务端自动选择可用渠道，无需手动换源'
  return null
}

async function clearCompleted() {
  return backendDownload.clearDone()
}

async function clearRecords() {
  // 只清已结束的记录；'downloading' / pending / paused 一概不碰，
  // 那是 clearAll 之外的「停止全部」语义，两者必须分开。
  const ids = tasks.value.filter(t => ['success', 'failed', 'stopped'].includes(t.status)).map(t => t.id)
  return ids.length ? removeTasks(ids) : 0
}

async function clearAll() {
  return backendDownload.clearAll()
}

function dropTasks(predicate) {
  const list = typeof predicate === 'function' ? tasks.value.filter(predicate) : []
  provisional.value = provisional.value.filter(t => !list.includes(t))
  syncTasks()
  return list
}

// ── 设置 / 目录 / 迁移 ──

function persistSettings() {
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({
      defaultQuality: defaultQuality.value,
      maxAttempts: maxAttempts.value,
      autoFailover: autoFailover.value,
      concurrency: concurrency.value,
      embedLyric: embedLyric.value,
      writeLrc: writeLrc.value,
      startIntervalSec: startIntervalSec.value,
      doneIntervalSec: doneIntervalSec.value,
    }))
  } catch (_) {}
}

function loadSettings() {
  try {
    const raw = localStorage.getItem(SETTINGS_STORAGE_KEY)
    if (!raw) return
    const s = JSON.parse(raw)
    if (s.defaultQuality) defaultQuality.value = s.defaultQuality
    if (s.maxAttempts) maxAttempts.value = s.maxAttempts
    if ('autoFailover' in s) autoFailover.value = !!s.autoFailover
    if (s.concurrency) concurrency.value = s.concurrency
    if ('embedLyric' in s) embedLyric.value = !!s.embedLyric
    if ('writeLrc' in s) writeLrc.value = !!s.writeLrc
    if (s.startIntervalSec != null) startIntervalSec.value = s.startIntervalSec
    if (s.doneIntervalSec != null) doneIntervalSec.value = s.doneIntervalSec
  } catch (_) {}
}

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
}

function setQualityOverride(v) {
  qualityOverrideSetting.value = v || ''
}

function setAllQuality(quality) {
  updateSettings({ defaultQuality: quality })
}

async function openDirModal() {
  editDownloadDir.value = currentDownloadDir.value
  // 每次打开都重读偏好：用户可能在「本地曲库」弹窗里刚切过开关
  browseShowHidden.value = loadBool('nas_show_hidden', false)
  showDirModal.value = true
  browseDir(currentDownloadDir.value)
}

/** 切「显示隐藏目录」：记住偏好并就地重读当前目录（不跳回上一层） */
function setBrowseShowHidden(v) {
  browseShowHidden.value = !!v
  savePref('nas_show_hidden', browseShowHidden.value)
  browseDir(editDownloadDir.value)
}

async function browseDir(path) {
  try {
    const res = await NasAPI.browse(path, { hidden: browseShowHidden.value })
    if (res.code === 200 && res.data) {
      browseFolders.value = res.data.folders || []
      browseParentPath.value = res.data.parent || ''
      browseHiddenCount.value = res.data.hidden_count || 0
      browseHiddenDirs.value = res.data.hidden_dirs || []
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
    dirError.value = fail('保存下载目录', e)
  } finally {
    isSavingDir.value = false
  }
}

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
  try {
    const cfg = await ConfigAPI.get()
    if (cfg) qualityOverrideSetting.value = String(cfg.download_quality_override || '')
  } catch (_) {}
}

// ── 旧本地队列迁移 ──

function legacyToTask(t, index) {
  const song = t.song || {}
  const id = t.id || `legacy-${index}`
  const task = {
    id,
    clientId: id,
    _backendId: '',
    songKey: songKeyOf(song),
    song,
    status: t.status,
    error: t.error || '',
    errorCode: 'MIGRATED',
    quality: t.quality || effectiveDefaultQuality(),
    attempts: t.attempts || 0,
    platform: t.platform || song.source || '',
    resolvedQuality: t.resolvedQuality || '',
    resolvedSource: t.resolvedSource || '',
    path: t.path || '',
    addedAt: t.addedAt || 0,
    completedAt: t.completedAt || 0,
    autoRowKey: t.autoRowKey || '',
    _submitItem: null,
  }
  return task
}

function readLegacyTasks() {
  try {
    const raw = localStorage.getItem(TASKS_STORAGE_KEY)
    if (!raw) return []
    const list = JSON.parse(raw)
    return Array.isArray(list) ? legacyTasksToMigrate(list) : []
  } catch (_) { return [] }
}

function writeLegacyTasks(list) {
  try {
    if (list.length) localStorage.setItem(TASKS_STORAGE_KEY, JSON.stringify(list))
    else localStorage.removeItem(TASKS_STORAGE_KEY)
  } catch (_) {}
}

async function migrateLegacyQueue() {
  try {
    if (localStorage.getItem(MIGRATION_FLAG) === '1') {
      migration.value = { active: false, total: 0, remaining: 0, done: true }
      writeLegacyTasks([])
      return
    }
  } catch (_) {}
  let legacy = readLegacyTasks()
  if (!legacy.length) {
    try { localStorage.setItem(MIGRATION_FLAG, '1') } catch (_) {}
    migration.value = { active: false, total: 0, remaining: 0, done: true }
    return
  }
  migration.value = { active: true, total: legacy.length, remaining: legacy.length, done: false }
  const display = legacy.map(legacyToTask)
  provisional.value = [...provisional.value, ...display]
  syncTasks()

  try {
    // 用队列而不是固定下标：部分失败时数组会缩短，`i += 50` 会跳过还没试过的记录。
    let pending = [...legacy]
    while (pending.length) {
      const chunk = pending.slice(0, MIGRATION_BATCH_SIZE)
      const items = chunk.map((t, j) => legacyTaskToBackendItem(t, { qualityFallback: effectiveDefaultQuality(), index: j }))
      // migrate:true 才允许带 status/path 等迁移字段，且 success 只落历史、不取音。
      const receipt = await backendDownload.submitItems(items, { migrate: true })
      const { confirmedIds, duplicateIds, skippedReasons } = splitMigrationReceipt(receipt)
      const confirmed = new Set([...confirmedIds, ...duplicateIds])
      const remain = []
      for (let j = 0; j < chunk.length; j++) {
        const t = chunk[j]
        const cid = items[j].client_id
        if (confirmed.has(cid)) continue
        const reason = skippedReasons.get(cid)
        if (reason) t.error = skipReasonText(reason)
        remain.push(t)
      }
      pending = [...remain, ...pending.slice(chunk.length)]
      // 已确认的从本地删除；未确认的写回，下次启动继续（同 client_id 幂等）。
      writeLegacyTasks(pending)
      provisional.value = provisional.value.filter(p => !confirmed.has(p.clientId || p.id))
      migration.value = { active: true, total: migration.value.total, remaining: pending.length, done: false }
      syncTasks()
      if (!remain.length && !pending.length) break
      // 整批都没被确认（例如后端暂时不可用）就别空转重试同一批。
      if (remain.length === chunk.length) break
    }
    legacy = pending
    await backendDownload.refreshQueue().catch(() => {})
    if (!legacy.length) {
      try { localStorage.setItem(MIGRATION_FLAG, '1') } catch (_) {}
      migration.value = { active: false, total: migration.value.total, remaining: 0, done: true }
    } else {
      migration.value = { active: true, total: migration.value.total, remaining: legacy.length, done: false }
    }
  } catch (e) {
    // 后端不可用/网络失败：一条不删，保留到下次启动；界面显示为原状态。
    logError('迁移下载队列', e?.userMessage || e?.message || '提交失败', `remaining=${legacy.length}`)
    migration.value = { active: true, total: migration.value.total, remaining: legacy.length, done: false }
    syncTasks()
  }
}

// ── 初始化 ──

let initialized = false
async function init() {
  if (initialized) return
  initialized = true
  loadSettings()
  await loadConfig()
  loadUnsent()
  // 先探一次可用性：搜索页在 addSong 之前就能拦住「后端不可用」，
  // 不会先让用户看到「已加入」再变成失败。
  await backendDownload.refreshStatus().catch(() => {})
  await backendDownload.refreshQueue().catch(() => {})
  syncTasks()
  await migrateLegacyQueue()
}

init()

function open() { isOpen.value = true }
function close() { isOpen.value = false }
function toggle() { isOpen.value = !isOpen.value }

function slimTasks() {
  return tasks.value.map(t => ({
    id: t.clientId || t.id,
    status: t.status,
    quality: t.quality,
    addedAt: t.addedAt,
    path: t.path,
    autoRowKey: t.autoRowKey,
  }))
}

export const downloadManager = {
  // 状态
  tasks,
  downloadedMap,
  isOpen,
  currentDownloadDir,
  lastActionError,
  migration,

  // 计数
  activeCount,
  queuePaused,
  canEnqueue,
  enqueueBlockReason,
  pendingCount,
  downloadingCount,
  pausedCount,
  stoppedCount,
  failedCount,
  completedCount,
  totalCount,
  recentSuccessRate,

  // 设置
  defaultQuality,
  qualityOverrideSetting,
  maxAttempts,
  autoFailover,
  concurrency,
  embedLyric,
  writeLrc,
  startIntervalSec,
  doneIntervalSec,
  updateSettings,
  setQualityOverride,
  setAllQuality,
  effectiveDefaultQuality,

  // 目录
  showDirModal,
  isSavingDir,
  dirError,
  browseFolders,
  browseParentPath,
  editDownloadDir,
  browseShowHidden,
  browseHiddenCount,
  browseHiddenDirs,
  openDirModal,
  browseDir,
  setBrowseShowHidden,
  saveDownloadDir,
  loadConfig,

  // 查询
  isSongDownloading,
  isSongDownloaded,
  checkSongsDownloaded,

  // 入队
  addSong,
  addBatch,

  // 控制
  pauseTask,
  resumeTask,
  cancelTask,
  pauseTasksByIds,
  stopTasksByIds,
  removeTask,
  removeTasks,
  retryTask,
  retryAllFailed,
  retryTasksByIds,
  resumeTasksByIds,
  resumeStopped,
  redownloadTasksByIds,
  setTaskQuality,
  switchSource,
  pauseAll,
  resumeAll,
  stopAll,
  stopByRow,
  clearCompleted,
  clearRecords,
  clearAll,
  dropTasks,

  // 兼容
  slimTasks,
  saveTasks: () => backendDownload.refreshQueue().catch(() => {}),
  syncTasksFromServer: () => backendDownload.refreshQueue().catch(() => {}),

  // UI
  open,
  close,
  toggle,

  // 质量
  QUALITY_OPTIONS,
  qualityLabel,
  isHighestQuality,

  // 路由/DOM 之外的辅助
  setTaskRowKey,

  // 旧组件仍读这个（后台队列没有「浏览器音源」概念，返回空）
  availableSourceIds: ref([]),
  sourceHealth: ref({}),
}
