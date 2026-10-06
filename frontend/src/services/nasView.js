// 「本地音乐」（`NasExplorer.vue`）的路径与扫描结果看法逻辑。
//
// 搬出来的理由同样是「错了也不报错」：
//   · 面包屑把 `/vol1/Music/专辑` 拆成可点的层级，拆错就是「点了没反应」；
//   · 目录枚举被上限/深度**截断**时界面必须说出来 —— 以前只显示「已加载 1000 首」，
//     看起来就是这个目录的全部，用户根本不知道该去子目录里找；
//   · 更隐蔽的一条：记忆缓存里以前只存 `songs`，于是「切回刚才那个目录」时
//     截断提示会消失（缓存把「不完整」洗成了「完整」）。这里把载荷定成
//     `{songs, truncated, warnings}` 一起进一起出，缓存命中也不再丢。
//
// 与后端 `pkg/nas/scanner.go` 的约定：响应里 `truncated` 是被上限截断，`warnings` 是人话原因。

/** 枚举被截断时界面上的那句话（模板与测试共用同一份文案） */
export const SCAN_TRUNCATED_TEXT = '· 还有没列出来的（结果被截断）'
/** 拿不到具体原因时的兜底提示 */
export const SCAN_TRUNCATED_TITLE = '本次枚举结果不完整'

/**
 * 路径 → 面包屑（每一段都能点）。
 *
 * `path` 为空时用 `fallback`；根目录（`/vol1`）也返回一段，
 * 否则标题行会空掉 —— 空数组在模板里就是「什么都不显示」。
 */
export function breadcrumbsFor(path, fallback = '/vol1/Music') {
  const p = path || fallback
  const parts = String(p).split('/').filter(Boolean)
  const list = []
  let acc = ''
  for (const part of parts) {
    acc += '/' + part
    list.push({ name: part, path: acc })
  }
  return list.length ? list : [{ name: p, path: p }]
}

/** 记忆缓存的键（按目录分开：换目录不该看到别的目录的列表） */
export function scanCacheKey(dir) {
  return `nas_songs:${dir}`
}

/**
 * `path` 是否落在 `volume` 这块盘上。
 *
 * **必须带斜杠比较** —— 直接 `startsWith('/vol1')` 会把 `/vol10/...` 也算成 `/vol1` 的，
 * 于是磁盘标签上会同时高亮两块盘。根目录自己（`path === volume`）也算命中。
 */
export function isUnderVolume(path, volume) {
  if (!path || !volume) return false
  const v = volume.endsWith('/') ? volume.slice(0, -1) : volume
  return path === v || path.startsWith(v + '/')
}

/**
 * `path` 当前落在哪块盘上（磁盘标签靠它决定高亮哪一个）。
 *
 * 取**最长匹配**：`volumes` 里可能有嵌套的根（如 `/vol1` 与 `/vol1/Music`），
 * 那时应该亮最贴近的那一块。都没有则返回空串（不高亮任何一块）。
 */
export function volumeOf(path, volumes = []) {
  let best = ''
  for (const v of volumes) {
    if (isUnderVolume(path, v) && v.length > best.length) best = v
  }
  return best
}

/**
 * 扫描响应 → 视图载荷。两套形状都认（后端同时给了顶层与 `data` 包装）。
 *
 * `warnings` 只收数组：后端可能给 null，直接 `join` 会炸。
 */
export function scanViewOf(res) {
  const top = res || {}
  const data = top.data || {}
  const songs = top.songs ?? data.songs ?? []
  const warns = top.warnings ?? data.warnings ?? []
  return {
    songs: Array.isArray(songs) ? songs : [],
    truncated: Boolean(top.truncated ?? data.truncated),
    warnings: Array.isArray(warns) ? warns : [],
  }
}

/**
 * 缓存载荷 → 视图载荷。
 *
 * 兼容老形状（以前只存了 `songs` 数组）：那种缓存里没有截断信息，
 * 只能当作「未知」，按不截断显示 —— 这就是当初切回目录提示消失的原因。
 */
export function scanViewOfCache(cached) {
  if (Array.isArray(cached)) return { songs: cached, truncated: false, warnings: [] }
  if (!cached || typeof cached !== 'object') return { songs: [], truncated: false, warnings: [] }
  return {
    songs: Array.isArray(cached.songs) ? cached.songs : [],
    truncated: Boolean(cached.truncated),
    warnings: Array.isArray(cached.warnings) ? cached.warnings : [],
  }
}

/** 视图载荷 → 缓存载荷（三个字段一起存，见文件头的说明） */
export function scanCachePayload(view) {
  return { songs: view?.songs || [], truncated: Boolean(view?.truncated), warnings: view?.warnings || [] }
}

/**
 * 视图载荷 → 那句提示（是否显示、文案、悬停原因）。
 *
 * 没被截断就不显示；被截断但后端没给原因时给兜底文案，
 * 不能让用户悬停出一个空白（他只会以为界面坏了）。
 */
export function scanHintOf(view) {
  const show = Boolean(view?.truncated)
  const warns = Array.isArray(view?.warnings) ? view.warnings.filter((w) => w) : []
  return {
    show,
    text: SCAN_TRUNCATED_TEXT,
    title: warns.length ? warns.join('；') : SCAN_TRUNCATED_TITLE,
  }
}
