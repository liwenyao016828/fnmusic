/**
 * 顶层视图注册表（单一数据源）。
 *
 * 2026-10-15 重构引入。在此之前，「有哪些顶层页」这件事散在三处：
 *   1. App.vue 的 `navItems`（导航按钮）
 *   2. App.vue 模板里的 `<template #view-xxx>`（滑动分页的槽位）
 *   3. 各处组件里的 `navigateTo('logs')` / `navigateTo('charts')`（深链）
 * 三处各写一遍，于是出现了「音源管理是个整页，却不在导航里，只能靠顶栏胶囊进」
 * 这种结构性问题 —— 加页时漏登记一处就成了孤儿页。
 *
 * 现在这些 id 只在这里定义一次；导航、滑动顺序、页内分段、深链解析全部读它。
 *
 * 页内分段（segments）：把「二级但不该单独占导航位」的页并进顶层视图。
 * 例如「榜单管理」与「发现」都是浏览类内容，合并成「发现」页的两段；
 * 「日志」与「设置」都是工具页，合并成「系统」页的两段。
 * 分段只影响呈现，**深链仍可直接命中某一段**（resolveRoute('logs')）。
 */

/** 顶层视图。wizard 顺序 = 导航从左到右 = 手指左右滑动的顺序。 */
export const VIEWS = [
  {
    id: 'search',
    name: '发现音乐',
    short: '发现',
    segments: [
      { id: 'discover', name: '发现' },
      { id: 'charts', name: '榜单' },
    ],
  },
  { id: 'library', name: '曲库管家', short: '曲库' },
  { id: 'sources', name: '音源管理', short: '音源' },
  { id: 'accounts', name: '账号连接', short: '账号' },
  {
    id: 'system',
    name: '系统',
    short: '系统',
    segments: [
      { id: 'logs', name: '日志' },
      { id: 'settings', name: '设置' },
    ],
  },
]

export const VIEW_IDS = VIEWS.map((v) => v.id)

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
 *   · 收藏/外链里手工敲过的地址
 * 改版把「榜单管理」并进发现、「日志」「设置」并进系统，这里就是那一刀。
 */
const ALIASES = {
  charts: { view: 'search', segment: 'charts' },
  discover: { view: 'search', segment: 'discover' },
  logs: { view: 'system', segment: 'logs' },
  settings: { view: 'system', segment: 'settings' },
  // 历史写法：顶栏胶囊曾是唯一的音源入口，个别地方写成 netsources
  netsources: { view: 'sources', segment: '' },
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
