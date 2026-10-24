// 「本地曲库」列表排序的纯逻辑（`NasExplorer.vue` 只管画，判据都在这里）。
//
// 用户原话（2026-10-24）：「曲库里面已下载的歌曲，按照最新排序，或者帮我增加列排序选项」。
// 于是两件事：
//   1) **默认排序 = 最新**：用户一进页面看到的就该是刚下载的那几首，而不是后端
//      枚举文件系统的顺序（那个顺序取决于目录遍历，跟下载时间毫无关系）；
//   2) 标题 / 专辑 / 格式 / 大小四列可点，点一次换方向。
//
// 为什么单独抽成纯函数：排序里全是「差一点就错」的细节 ——
//   · `updated_at` 是秒级 mtime，缺字段/为 0 的老数据（服务端缓存、旧响应）不能
//     被当成 1970 年排到最后，而应**退回后端原顺序**；
//   · 中文标题按码位比较会出乱序（「周杰伦」永远排在「A-Lin」前面），必须
//     `localeCompare(..., 'zh-Hans-CN')`；
//   · 同值必须稳定（同名不同目录的两首、同专辑一堆歌），否则每次重排都会跳。
// 这些都能在 node:test 里直接回归，不必开浏览器点。

/** 默认排序：最新（`updated_at` 倒序）。 */
export const DEFAULT_SORT = Object.freeze({ key: 'newest', dir: 'desc' })

/** 可排序列的键（顺序即表头顺序，供模板/测试共用）。 */
export const SORT_KEYS = Object.freeze(['newest', 'title', 'album', 'format', 'size'])

/**
 * 每列的默认方向：最新/大小是「大的在前」更有用，文本列按升序（A→Z）符合直觉。
 * 点另一列的第一次点击就用这个方向，再点同一列才反向。
 */
const DEFAULT_DIRS = Object.freeze({
  newest: 'desc',
  title: 'asc',
  album: 'asc',
  format: 'asc',
  size: 'desc',
})

function isDir(d) {
  return d === 'asc' || d === 'desc'
}

/** 这个键能不能排（非法键一律回退默认，见 parseSortPref）。 */
export function isSortKey(key) {
  return typeof key === 'string' && SORT_KEYS.includes(key)
}

/** 某一列的默认方向。 */
export function defaultDirOf(key) {
  return isSortKey(key) ? DEFAULT_DIRS[key] : DEFAULT_SORT.dir
}

/** 点一次表头之后的方向：同列取反，跨列回到该列默认方向。 */
export function nextSortDir(key, dir) {
  return isDir(dir) ? (dir === 'asc' ? 'desc' : 'asc') : defaultDirOf(key)
}

/** `updated_at` 归一成数字；缺失/非法/非正数一律当「没有时间」。 */
function tsOf(song) {
  const n = Number(song && song.updated_at)
  return Number.isFinite(n) && n > 0 ? n : 0
}

/** 文本列取排序用的值。 */
function textOf(song, key) {
  if (!song) return ''
  if (key === 'title') return String(song.title || song.filename || '')
  if (key === 'album') return String(song.album || '')
  if (key === 'format') return String(song.format || '')
  return ''
}

/** 字节数归一：非数字/负数都当 0（排到最后，但不影响其它条目相对顺序）。 */
function sizeOf(song) {
  const n = Number(song && song.size)
  return Number.isFinite(n) && n > 0 ? n : 0
}

// 中文友好比较：`zh-Hans-CN` 让「阿」「周」按拼音而不是码位排，
// 数字也按数值序（"第10首" 排在 "第9首" 之后）。个别环境不认这个 locale 时
// 会抛 RangeError，退化成默认比较即可 —— 排序永远不能因为语言环境崩掉页面。
function compareText(a, b) {
  const x = a == null ? '' : String(a)
  const y = b == null ? '' : String(b)
  if (x === y) return 0
  try {
    return x.localeCompare(y, 'zh-Hans-CN', { numeric: true, sensitivity: 'base' })
  } catch (_) {
    return x < y ? -1 : 1
  }
}

/**
 * 排序入口（**纯函数**：不读 localStorage、不改入参、不改 `songs` 里的对象）。
 *
 * @param {Array<object>} songs 后端返回的本地曲目（含 `updated_at` / `size` / `title`…）
 * @param {'newest'|'title'|'album'|'format'|'size'} key 排序列；非法值 → 最新
 * @param {'asc'|'desc'} dir 方向；非法值 → 该列默认方向
 * @returns {Array<object>} 新数组（元素是原对象引用，视图仍需自己 map）
 */
export function sortSongs(songs, key, dir) {
  if (!Array.isArray(songs) || songs.length === 0) return []
  const known = isSortKey(key)
  const k = known ? key : DEFAULT_SORT.key
  // 键非法时方向也一并回退默认：否则「bogus 键 + asc」会变成「按最新升序」，
  // 列表最上面是 1970 年的老文件 —— 这不是任何人有过的预期。
  const d = known ? (isDir(dir) ? dir : defaultDirOf(k)) : DEFAULT_SORT.dir
  const sign = d === 'asc' ? 1 : -1

  // 先带上原始下标：所有同值比较都用它兜底，保证**稳定**（同分保持后端原顺序）。
  const rows = songs.map((song, i) => ({ song, i }))

  if (k === 'newest') {
    // 没有 updated_at 的条目**不参与时间比较**：它们留在后端原顺序里、整体排在
    // 有时间的条目之后。既不会把老数据聚成乱序的一坨，也不会把它们甩到最前面
    // 冒充「最新」。全都没有时间时，结果恰好就是后端原顺序。
    const timed = []
    const untimed = []
    for (const row of rows) {
      const ts = tsOf(row.song)
      if (ts > 0) timed.push({ ...row, ts })
      else untimed.push(row)
    }
    timed.sort((a, b) => (a.ts === b.ts ? a.i - b.i : sign * (a.ts - b.ts)))
    return [...timed, ...untimed].map(r => r.song)
  }

  const valueOf = k === 'size'
    ? (song) => sizeOf(song)
    : (song) => textOf(song, k)

  const cmp = k === 'size'
    ? (a, b) => sign * (valueOf(a) - valueOf(b))
    : (a, b) => sign * compareText(valueOf(a), valueOf(b))

  rows.sort((a, b) => {
    const r = cmp(a.song, b.song)
    return r !== 0 ? r : a.i - b.i // 同值稳定
  })
  return rows.map(r => r.song)
}

/**
 * 解析持久化的排序偏好（`localStorage['fn_nas_sort']`，形如 `title:asc`）。
 *
 * 任何不认识的键/方向都**静默回退**默认（默认最新）：这份偏好会被用户在
 * 不同版本间来回带，旧版本写下的 `filemtime` 之类不该让列表变成随机顺序，
 * 更不该在控制台里刷错误。
 */
export function parseSortPref(raw) {
  const text = typeof raw === 'string' ? raw.trim() : ''
  if (!text) return { ...DEFAULT_SORT }
  const [key, dir] = text.split(':')
  if (!isSortKey(key)) return { ...DEFAULT_SORT }
  return { key, dir: isDir(dir) ? dir : defaultDirOf(key) }
}

/** 排序偏好 → 持久化字符串（与 parseSortPref 严格互逆）。 */
export function sortPrefOf(state) {
  const key = state && isSortKey(state.key) ? state.key : DEFAULT_SORT.key
  const dir = state && isDir(state.dir) ? state.dir : defaultDirOf(key)
  return `${key}:${dir}`
}
