/**
 * 顶层视图注册表（单一数据源）。
 *
 * 2026-10-15 引入；2026-10-17 随「导航框架重做」扩成 8 视图 + 分组。
 * 「有哪些顶层页」只在这里定义一次：导航（桌面侧栏 / 手机底部标签栏）、
 * 滑动顺序、页内分段、深链解析、角标全部读它。
 *
 * 2026-10-17 重做的动机：原「曲库管家」一页塞了 5 个标签（本地音乐/推送/下载/
 * 补全整理/自动下载记录），一个 3480 行的巨型组件；顶层导航只有 5 项，
 * 但真正高频的功能（下载队列、订阅）都藏在二级标签里。现在按**用户功能**拆平：
 *
 * ```
 * listen 组：发现音乐（发现/榜单） · 本地曲库
 * manage 组：补全整理 · 订阅更新 · 下载（队列/歌单/记录）
 * config 组：音源管理 · 账号连接 · 系统（日志/设置）
 * ```
 *
 * 页内分段（segments）：把「二级但不该单独占导航位」的页并进顶层视图，
 * 深链仍可直接命中某一段（resolveRoute('autodl') → download/autodl）。
 *
 * 分组（group）只影响**呈现**（桌面侧栏的分组标题；手机底部栏取前 4 个
 * 高频视图 + 「更多」抽屉），不影响 id 与深链。
 */

/** 导航分组。桌面侧栏按此顺序分节；手机底部标签栏不用它。 */
export const NAV_GROUPS = [
  { id: 'listen', name: '听' },
  { id: 'manage', name: '管' },
  { id: 'config', name: '配置' },
]

/** 顶层视图。数组顺序 = 桌面侧栏顺序 = 手指左右滑动的顺序。 */
export const VIEWS = [
  {
    id: 'search',
    name: '发现音乐',
    short: '发现',
    group: 'listen',
    segments: [
      { id: 'discover', name: '发现' },
      { id: 'charts', name: '榜单' },
    ],
  },
  { id: 'library', name: '本地曲库', short: '曲库', group: 'listen' },
  { id: 'tidy', name: '补全整理', short: '整理', group: 'manage' },
  { id: 'push', name: '订阅更新', short: '订阅', group: 'manage' },
  {
    id: 'download',
    name: '下载',
    short: '下载',
    group: 'manage',
    segments: [
      { id: 'queue', name: '队列' },
      { id: 'playlist', name: '歌单' },
      { id: 'autodl', name: '自动记录' },
    ],
  },
  { id: 'sources', name: '音源管理', short: '音源', group: 'config' },
  { id: 'accounts', name: '账号连接', short: '账号', group: 'config' },
  {
    id: 'system',
    name: '系统',
    short: '系统',
    group: 'config',
    segments: [
      { id: 'logs', name: '日志' },
      { id: 'settings', name: '设置' },
    ],
  },
]

export const VIEW_IDS = VIEWS.map((v) => v.id)

/** 手机底部标签栏：前 4 个高频视图 + 「更多」抽屉（抽屉里是其余全部）。 */
export const MOBILE_TAB_IDS = ['search', 'library', 'push', 'download']

export function viewsOfGroup(groupId) {
  return VIEWS.filter((v) => v.group === groupId)
}

/** 默认落在哪一段（没有分段时返回空串） */
export function defaultSegment(viewId) {
  const v = VIEWS.find((x) => x.id === viewId)
  if (!v || !v.segments || !v.segments.length) return ''
  return v.segments[0].id
}

export function segmentsOf(viewId) {
  const v = VIEWS.find((x) => x.id === viewId)
  return (v && v.segments) || []
}

export function isView(viewId) {
  return VIEW_IDS.includes(viewId)
}

/**
 * 老 id / 别名 → 新位置。
 *
 * 必须保留这层映射，否则这些历史入口会静默失效：
 *   · 组件里的 `navigateTo('logs')` / `navigateTo('charts')` / `navigateTo('settings')`
 *     （十几处：报错提示里的「看日志」、榜单的「▶ 看一眼」、设置里跳日志等）
 *   · 浏览器偏好里存的旧 id（library_tab='nas'/'complete'/'autodl'…）
 *   · 收藏/外链里手工敲过的地址
 * 2026-10-17 把「曲库管家」的 5 个标签拆成 4 个顶层视图，这里就是那一刀。
 */
const ALIASES = {
  charts: { view: 'search', segment: 'charts' },
  discover: { view: 'search', segment: 'discover' },
  logs: { view: 'system', segment: 'logs' },
  settings: { view: 'system', segment: 'settings' },
  // 历史写法：顶栏胶囊曾是唯一的音源入口，个别地方写成 netsources
  netsources: { view: 'sources', segment: '' },
  // ── 原「曲库管家」侧栏项（library_tab 偏好 / statusItems 跳转 / 下载抽屉「查看队列」）──
  nas: { view: 'library', segment: '' },
  complete: { view: 'tidy', segment: '' },
  queue: { view: 'download', segment: 'queue' },
  playlist: { view: 'download', segment: 'playlist' },
  autodl: { view: 'download', segment: 'autodl' },
}

/**
 * 解析任意视图 id（含历史别名）成 { view, segment }。
 *
 * 认不出时**回落到发现页并标记 unknown**：宁可多跳一次首页，也不要让
 * 「点了一下什么反应都没有」发生（那种 bug 用户只会报「按钮坏了」）。
 * 调用方可以据此记一条 warn 日志，把漏登记的 id 暴露出来。
 *
 * @param {string} id 视图 id（可能是老 id / 别名）
 * @param {string} [segment] 想去的分段；不传或该视图没有这段时用默认段
 * @returns {{ view: string, segment: string, unknown: boolean }}
 */
export function resolveRoute(id, segment) {
  const alias = ALIASES[id]
  const view = alias ? alias.view : id
  const unknown = !alias && !isView(id)
  const target = unknown ? 'search' : view

  const segs = segmentsOf(target)
  let seg = segment || (alias ? alias.segment : '') || defaultSegment(target)
  // 没有分段的页一律空串（模板据此决定渲不渲分段器）；有分段但认不出指定值时回落第一段
  if (!segs.length) seg = ''
  else if (!segs.some((s) => s.id === seg)) seg = segs[0].id

  return { view: target, segment: seg, unknown }
}
