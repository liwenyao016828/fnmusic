/**
 * 详情数据「进程内」缓存 —— 歌单/榜单曲目、以及发现页几个首屏小请求。
 *
 * 背景（用户口径）：**每次退出再进来，本地/在线数据都重新拉一遍**。
 * 具体是三件事叠在一起：
 *   ① 三个详情入口（精选歌单 / 榜单 / 分享链接）每次都 `activeSongList = []`
 *      → 在线拉一次曲目，列表先闪一下空白；
 *   ② 拿到曲目后再 POST 一次 `/api/download/check` 查「哪些已经下载到 NAS」
 *      （这就是「本地」那半数据），68 首的歌单就是一次大 payload；
 *   ③ 还要再查一次「这个歌单订阅了没有」。
 *
 * `viewCache.js` 只管**视图级**的「多久该刷新」，回答不了「这份歌单数据还在吗」，
 * 而且它不认识分页位置、已下载态这些字段。所以这里补一层按 key 的数据缓存：
 *
 *   · **TTL + SWR**：TTL 内直接命中不发请求；过期了先出旧数据，再后台刷新。
 *   · **LRU**：容量上限 `MAX_ENTRIES`，超出淘汰最久未用的（进程内 Map，不落盘）。
 *   · **可注入 now**：所有判定吃 `now` 参数，纯逻辑、不碰 DOM/网络，
 *     单测无需 jsdom（本项目前端测试没有 jsdom，见 detailCache.test.mjs）。
 *
 * 与 `prefs.js` 的 `memGet/memSet` 的关系：那对函数是**裸 Map**，没有 TTL、
 * 没有容量上限，也只适合「拉过就一直用」的静态字典。需要新鲜度的一律走这里。
 */

/** 各类数据的 TTL（毫秒）：变化越快的越短 */
const TTL = {
  songs: 5 * 60_000,      // 歌单/榜单曲目：平台侧一天才动几次
  downloads: 60_000,      // 「已下载」查询：downloadManager 自己会实时改，这里只兜网络查询
  accounts: 5 * 60_000,   // 平台账号连接状态
  sources: 5 * 60_000,    // 「我的歌单」列表
  categories: 30 * 60_000, // 歌单分类：几乎不变
}

const DEFAULT_TTL = 60_000
const MAX_ENTRIES = 40

/** key → { at, data }，Map 的插入顺序就是 LRU 顺序（命中时先删再插） */
const store = new Map()

/** 该类数据的 TTL；未知类型给默认值 */
export function ttlOf(kind) {
  return TTL[kind] ?? DEFAULT_TTL
}

/**
 * 拼 key：`<kind>:<platform>:<id>`。
 * 任何一段为空（尤其是拿不到 id）时返回 ''，调用方据此退化成「每次都拉」。
 */
export function cacheKey(kind, platform, id) {
  const k = String(kind || '').trim()
  const i = String(id || '').trim()
  if (!k || !i) return ''
  return `${k}:${String(platform || '').trim()}:${i}`
}

/** 读一条（顺手把它挪到 LRU 队尾）；没有就返回 undefined */
export function read(key) {
  if (!key || !store.has(key)) return undefined
  const entry = store.get(key)
  store.delete(key)
  store.set(key, entry)
  return entry.data
}

/** 这条数据多久前写的（毫秒）；没有则 Infinity */
export function age(key, now = Date.now()) {
  const entry = store.get(key)
  return entry ? now - entry.at : Infinity
}

/**
 * 还在 TTL 内吗？（= 不需要发请求）
 * 注意：`write` 会刷新 `at`，所以「刚写完」一定是新鲜的。
 */
export function isFresh(key, kind = 'songs', now = Date.now()) {
  const entry = store.get(key)
  return !!entry && now - entry.at <= ttlOf(kind)
}

/** 写一条（合并语义由调用方用 read + 展开决定）；同时刷新 `at` 并把容量收进上限 */
export function write(key, data, now = Date.now()) {
  if (!key) return
  store.delete(key)
  store.set(key, { at: now, data })
  trim()
}

/** 精确删一条（写操作后调） */
export function drop(key) {
  if (key) store.delete(key)
}

/** 按前缀批量删：`dropPrefix('playlist:wy:')` = 清掉网易云所有歌单详情 */
export function dropPrefix(prefix) {
  if (!prefix) return
  for (const k of [...store.keys()]) {
    if (k.startsWith(prefix)) store.delete(k)
  }
}

/** 全部清空（例如「强制刷新」按钮） */
export function clear() {
  store.clear()
}

/** 当前条目数（测试与诊断用） */
export function size() {
  return store.size
}

/**
 * 「已下载」状态是不是刚查过？
 * 单独记账而不是重写整条：查已下载**不该**把曲目数据的 `at` 刷新成新的，
 * 否则一份 5 分钟前拉的曲目会因为「刚查过已下载」而永远不过期。
 */
export function downloadFresh(key, now = Date.now()) {
  const data = read(key)
  if (!data || !data.downloadAt) return false
  return now - data.downloadAt <= ttlOf('downloads')
}

/** 记一次「已下载」查询时间（不动 `at`） */
export function markDownloadChecked(key, now = Date.now()) {
  const entry = store.get(key)
  if (!entry || !entry.data) return
  entry.data.downloadAt = now
}

/**
 * 把最新的订阅状态写回缓存条目（不动 `at`）。
 *
 * 订阅/取消订阅后 `refreshSubscription()` 已经拿到真值，但下次进同一个歌单
 * 若只认缓存里的旧值就会显示错的「已订阅」。这里只改这一个字段，
 * 不刷新曲目的新鲜度 —— 订阅状态变了不代表曲目列表也该当新数据。
 */
export function markSubscription(key, subscription) {
  const entry = store.get(key)
  if (!entry || !entry.data) return
  entry.data.subscription = subscription || null
}

function trim() {
  while (store.size > MAX_ENTRIES) {
    const oldest = store.keys().next().value
    if (oldest === undefined) return
    store.delete(oldest)
  }
}

// ── 同一份数据的「在途请求」合并 ─────────────────────────────────────────────
//
// 发现页**同时挂两份** SearchView（`inner-search-discover` / `inner-search-charts`
// 各一个，见 App.vue 的 SwipePager），两边 onMounted 会背靠背地请求同一批数据。
// `memGet` 只能挡住「已经回来了」的那一次，挡不住两个并发请求 —— 实测一次进页面
// 就发出两遍 `/api/charts/toplists` + 两遍 `/api/charts/playlists`。
// 这里按 key 把在途 Promise 复用掉：后到的那一方直接等前一个的结果。

/** key → Promise */
const inflight = new Map()

/**
 * 同一个 key 的并发请求只发一次。
 * @param {string}   key 用来识别「同一份数据」（通常就是缓存 key）
 * @param {Function} fn  () => Promise<any>
 * @returns {Promise<any>}
 */
export function dedupe(key, fn) {
  if (!key) return Promise.resolve().then(fn)
  const running = inflight.get(key)
  if (running) return running
  const p = Promise.resolve()
    .then(fn)
    .finally(() => {
      // 只清自己这一条：慢请求回来时 key 可能已被后来者替换
      if (inflight.get(key) === p) inflight.delete(key)
    })
  inflight.set(key, p)
  return p
}

/** 当前在途请求数（测试与诊断用） */
export function inflightSize() {
  return inflight.size
}
