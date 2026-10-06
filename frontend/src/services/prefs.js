/**
 * 本地偏好与轻量缓存工具。
 *
 * 持久化策略：**服务端文件优先，localStorage 兜底**。
 *
 * 背景：各页面/窗口的点击状态（发现页 Tab、榜单/搜索平台、NAS 目录、播放模式、
 * 当前音源）原只存在浏览器 localStorage——换浏览器/清缓存就丢，多端也不共享。
 * 现在统一改存到服务端文件（GET/POST /api/ui-prefs → ui_prefs.json），多端共享，
 * 且不因清缓存而丢失。
 *
 * 读：内存 map 优先（毫秒级、同步），模块加载时以 localStorage 种子化；
 *     应用启动后异步 init() 从后端拉取并合并，后端可达时始终以后端为最终状态。
 * 写：同步写内存 + localStorage，再防抖异步 POST 后端批量合并。
 *     后端不可用时静默降级到 localStorage（不阻塞交互）。
 *
 * 统一前缀 fn_，并在 localStorage 不可用（隐私模式等）时安全降级。
 */

import { UiPrefsAPI } from '../api/client'

const PREFIX = 'fn_'

// 内存态：所有读写都先落到这里，保证跨挂载、跨导航瞬时生效。
const store = new Map()

function hasStorage() {
  try {
    return typeof localStorage !== 'undefined' && localStorage !== null
  } catch (_) {
    return false
  }
}

// 用 localStorage 现有值种子化内存（后端尚未返回时的瞬时快照）。
function seedFromLocal() {
  if (!hasStorage()) return
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && k.startsWith(PREFIX)) {
        store.set(k, localStorage.getItem(k))
      }
    }
  } catch (_) {}
}

// ── 后端同步 ──

let initialized = false
let initPromise = null

/**
 * 启动时调用：从后端拉取全部窗口记忆，合并进内存与 localStorage。
 * 后端不可用（未启动 / 鉴权失败 / 网络异常）时静默降级，不阻塞应用。
 */
export function initUiPrefs() {
  if (initPromise) return initPromise
  initPromise = (async () => {
    try {
      const res = await UiPrefsAPI.getAll()
      const data = (res && res.data) || {}
      applyServerData(data)
      initialized = true
    } catch (_) {
      // 后端不可用：保持 localStorage 快照即可
    }
  })()
  return initPromise
}

function applyServerData(data) {
  const seen = new Set()
  for (const [k, v] of Object.entries(data)) {
    if (v === null || v === undefined) continue
    const key = PREFIX + k
    store.set(key, typeof v === 'string' ? v : JSON.stringify(v))
    if (hasStorage()) {
      try { localStorage.setItem(key, store.get(key)) } catch (_) {}
    }
    seen.add(key)
  }
}

// 防抖批量推送后端
let pushTimer = null
const pushQueue = new Map()

function schedulePush() {
  if (pushTimer) clearTimeout(pushTimer)
  pushTimer = setTimeout(flushPush, 300)
}

async function flushPush() {
  pushTimer = null
  if (pushQueue.size === 0) return
  const patch = {}
  for (const [key, raw] of pushQueue) {
    patch[key.slice(PREFIX.length)] = safeParse(raw)
  }
  pushQueue.clear()
  if (Object.keys(patch).length === 0) return
  try {
    await UiPrefsAPI.merge(patch)
  } catch (_) {
    // 后端不可用：本地已保存，下次启动 init() 会重新拉取
  }
}

function safeParse(raw) {
  if (raw === null || raw === undefined) return raw
  // 已是字符串且不以引号包裹时直接当字符串用
  const s = String(raw)
  try {
    return JSON.parse(s)
  } catch (_) {
    return s
  }
}

function noteLocalChange(key) {
  pushQueue.set(key, store.get(key))
  schedulePush()
}

// ── 字符串偏好 ──

/** 读取字符串偏好 */
export function loadPref(key, fallback = '') {
  const k = PREFIX + key
  if (store.has(k)) {
    const v = store.get(k)
    return v === null || v === '' ? fallback : v
  }
  if (!hasStorage()) return fallback
  try {
    const v = localStorage.getItem(k)
    return v === null || v === '' ? fallback : v
  } catch (_) {
    return fallback
  }
}

/** 写入字符串偏好（同步写内存 + localStorage，防抖异步推后端） */
export function savePref(key, value) {
  const k = PREFIX + key
  const s = String(value)
  store.set(k, s)
  if (hasStorage()) {
    try { localStorage.setItem(k, s) } catch (_) {}
  }
  noteLocalChange(k)
}

/** 读取布尔偏好 */
export function loadBool(key, fallback = false) {
  const v = loadPref(key, null)
  if (v === null || v === '') return fallback
  return v === '1' || v === 'true'
}

// ── 带过期时间的 JSON 缓存 ──
// 与偏好同样后端持久化，但带时间戳，过期即回落 fallback。

/** 读取带过期时间的 JSON 缓存 */
export function loadCache(key, ttlMs, fallback = null) {
  const k = PREFIX + key
  const raw = store.has(k) ? store.get(k) : (() => {
    if (!hasStorage()) return null
    try { return localStorage.getItem(k) } catch (_) { return null }
  })()
  if (!raw) return fallback
  try {
    const box = JSON.parse(raw)
    if (!box || typeof box !== 'object' || typeof box.at !== 'number') return fallback
    if (ttlMs > 0 && Date.now() - box.at > ttlMs) return fallback
    return box.data
  } catch (_) {
    return fallback
  }
}

/** 写入 JSON 缓存（带时间戳；防抖异步推后端） */
export function saveCache(key, data) {
  const k = PREFIX + key
  const s = JSON.stringify({ at: Date.now(), data })
  store.set(k, s)
  if (hasStorage()) {
    try { localStorage.setItem(k, s) } catch (_) {}
  }
  noteLocalChange(k)
}

// ── 进程内（模块级）缓存 ──
// 组件切换导航时会被卸载重建，模块级缓存可跨挂载存活，用于避免重复扫描/请求。
// 这部分是纯前端的运行期缓存，不属于「窗口点击记忆」，不需要持久化。

const memory = new Map()

export function memGet(key) {
  return memory.has(key) ? memory.get(key) : undefined
}

export function memSet(key, value) {
  memory.set(key, value)
}

export function memClear(keyPrefix = '') {
  if (!keyPrefix) {
    memory.clear()
    return
  }
  for (const k of [...memory.keys()]) {
    if (k.startsWith(keyPrefix)) memory.delete(k)
  }
}

// 模块加载即从 localStorage 种子化内存
seedFromLocal()