/* 导航搬家（v2.1.119 → v2.1.120 → 2026-10-15 重构）的源码约定测试。
 *
 * # 这几轮搬了什么
 *
 *   v2.1.119：榜单管理 发现音乐 → 曲库管家（第四个侧栏项）
 *   v2.1.120：榜单管理 曲库管家 → **顶层导航**（与「发现音乐」并列）
 *             本地音乐 顶层导航 → **曲库管家**（第一个侧栏项）
 *   2026-10-15（v2.4.0）：榜单管理 顶层导航 → **「发现」页的第二段**（页内分段）
 *             日志 + 设置 顶层导航 → **「系统」页的两段**；音源管理补进导航
 *             导航 7 项并成 5 项，视图清单的唯一数据源改为 services/navViews.js
 *
 * # 为什么用这种形态
 *
 * 搬家这件事的「正确」不在某个纯函数里，而在**几个文件的接线**上：
 *   · ChartManager 只被 App.vue 挂载（曲库管家不再 import）；
 *   · NasExplorer 只被曲库管家挂载（App.vue 不再 import / 不再有 view-nas）；
 *   · 管家的侧栏项集合里多了 nas、少了 charts；
 *   · 本地音乐的播放意图从管家往上转一层（少了它 = 点了歌不出声，且不报错）；
 *   · 「▶ 看一眼」从榜单管理回到发现页仍走导航总线。
 * 任何一处接错，界面上都不会报错 —— 只是「入口没了」或「点了没反应」。
 * 组件实例没法在 node 里起（项目没有 DOM 环境），所以照 pageUi.test.mjs 的做法：
 * 把真源码读进来，钉住这些**约定**。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { resolveRoute, VIEW_IDS } from './navViews.js'

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel) => readFileSync(join(here, rel), 'utf8')

const chartManagerVue = read('../components/ChartManager.vue')
const searchViewVue = read('../components/SearchView.vue')
const libraryManagerVue = read('../components/LibraryManager.vue')
const nasExplorerVue = read('../components/NasExplorer.vue')
const appVue = read('../App.vue')
const libraryViewJs = read('./libraryView.js')
const navBusJs = read('./navBus.js')
const navViewsJs = read('./navViews.js')

test('榜单管理：只由 App.vue 挂载，曲库管家不再 import 它', () => {
  assert.ok(
    appVue.includes("import ChartManager from './components/ChartManager.vue'"),
    'App.vue 应 import ChartManager（它现在是顶层视图）',
  )
  assert.ok(
    appVue.includes('<ChartManager'),
    'App.vue 模板里应挂载 ChartManager',
  )
  assert.ok(
    !libraryManagerVue.includes('ChartManager'),
    'LibraryManager 不该再 import / 挂载 ChartManager —— 它已升为顶层导航',
  )
})

test('榜单管理：不再占导航位，而是「发现」页的第二段（2026-10-15 重构）', () => {
  // 不再是顶层导航项
  assert.ok(
    !/\{ id: 'charts', name:/.test(appVue),
    'navItems 里不该再有 charts 导航项 —— 它已降为「发现」页的页内分段',
  )
  assert.ok(
    !appVue.includes('#view-charts'),
    '不该再有 view-charts 插槽（SwipePager 的顶层页只剩注册表里的 5 个）',
  )
  // 但必须仍能从顶层一键到达：深链解析到「发现」页的 charts 段
  const r = resolveRoute('charts')
  assert.equal(r.view, 'search')
  assert.equal(r.segment, 'charts')
  assert.ok(VIEW_IDS.includes(r.view), 'charts 该落在已登记的顶层页里')
  // 发现页确实渲染了分段器，且两段组件都挂上
  assert.ok(appVue.includes("decoratedSegments('search')"), '发现页应有页内分段器')
  assert.ok(appVue.includes("segMounted('search', 'charts')"), '发现页应挂载榜单段组件')

  assert.ok(
    !/id: 'nas'/.test(appVue),
    "navItems 里不该再有 nas 项（本地音乐已搬进曲库管家）",
  )
  assert.ok(
    !appVue.includes('#view-nas'),
    '不该再有 view-nas 插槽 —— 那个顶层视图已经不存在了',
  )
  // 视图清单收敛到注册表：模板槽位由 navViews.js 决定，不再手写 navItems
  assert.ok(appVue.includes('const navItems = VIEWS.map'), 'navItems 应由注册表生成（单一数据源）')
  assert.ok(appVue.includes('#view-search'), '应有 view-search 插槽（SwipePager 按插槽名渲染视图）')
})

test('本地音乐：就是「本地曲库」顶层视图本身（2026-10-15 框架重做）', () => {
  // 重做后不再有「曲库管家五合一 + nas 侧栏项」的结构：LibraryManager 被掏空成
  // 「本地曲库」这一个顶层视图（navViews.js 登记），壳里只剩 NasExplorer。
  assert.match(
    navViewsJs,
    /\{ id: 'library', name: '本地曲库'/,
    'navViews 里应有 library 顶层视图（本地曲库）',
  )
  assert.ok(
    libraryManagerVue.includes("import NasExplorer from './NasExplorer.vue'"),
    'LibraryManager 应 import NasExplorer',
  )
  assert.ok(
    libraryManagerVue.includes('<NasExplorer'),
    'LibraryManager 模板应挂载 NasExplorer',
  )
  // 子页不能自带 PageShell：否则会出现两个标题栏、双份内边距与两个滚动容器。
  assert.ok(
    !nasExplorerVue.includes('<PageShell'),
    'NasExplorer 不该再自带 PageShell（外壳由 LibraryManager 提供）',
  )
})

test('本地音乐：播放意图必须从管家往上转一层', () => {
  assert.ok(
    libraryManagerVue.includes("defineEmits(['play'])"),
    'LibraryManager 应声明 play 事件',
  )
  assert.ok(
    libraryManagerVue.includes("emit('play'"),
    'LibraryManager 应把 NasExplorer 的 play 原样转出去',
  )
  assert.ok(
    appVue.includes('@play="playSong"'),
    'App.vue 应接住 LibraryManager 的 play（播放器状态在顶层）',
  )
})

test('侧栏项集合：有 nas、没有 charts', () => {
  assert.match(
    libraryViewJs,
    /TAB_IDS = new Set\(\['nas', 'push', 'download', 'complete', 'autodl'\]\)/,
    'TAB_IDS 应含 nas 与 autodl、且不含 charts',
  )
  assert.ok(
    !/TAB_IDS[^\n]*charts/.test(libraryViewJs),
    'TAB_IDS 不该再含 charts —— 偏好里留着它的浏览器应落到默认项',
  )
})

test('榜单管理：指路按钮走导航总线，落到「发现」页的榜单段', () => {
  assert.ok(
    searchViewVue.includes('openChartManager'),
    '发现页应有 openChartManager（指路入口）',
  )
  assert.ok(
    searchViewVue.includes("navigateTo('charts')"),
    "openChartManager 应请求 charts（2026-10-15 后由 resolveRoute 解析成「发现页 + 榜单段」）",
  )
  assert.ok(
    !searchViewVue.includes('chartManager: true'),
    '不该再发 chartManager 负载（那条路由已撤）',
  )
  assert.ok(
    !searchViewVue.includes("'manage'"),
    "SearchView 里不该出现 'manage' 字面量（死键）",
  )
})

test('榜单管理：「看一眼」回到发现页仍走导航总线，负载形如 { chartPreview }', () => {
  assert.ok(
    chartManagerVue.includes("emit('preview'"),
    'ChartManager 仍通过 preview 事件对外说话（不 import 发现页）',
  )
  assert.ok(
    appVue.includes("navigateTo('search', { chartPreview: item })"),
    'App.vue 应把 preview 转成 chartPreview 负载发给发现页',
  )
  assert.ok(
    searchViewVue.includes('payload?.chartPreview'),
    '发现页应消费 chartPreview 负载',
  )
  // 总线本身要支持第二个参数（v2.1.119 为它加的；老调用方不传，行为不变）
  assert.match(
    navBusJs,
    /export function navigateTo\(view, payload\)/,
    'navigateTo 应带可选 payload 参数',
  )
  assert.ok(appVue.includes('onNavigate('), 'App.vue 的订阅应接住 payload 参数')
  // 2026-10-15 框架重做：切视图的**唯一入口**是 navBus.goView —— 共享 currentView 的写入、
  // 「认不出的 id 回落发现页 + warn」都在它里面做；navigateTo 内部已调过它。App.vue 的
  // 壳级 goView 只是薄包装（委托 navBusGoView + 补懒挂载账），订阅回调也只补 mountedSegs，
  // 不再自己拼 resolveRoute/切视图（否则同一个 unknown id 会弹两条提示）。
  assert.ok(
    navBusJs.includes('currentView.value = route.view'),
    'navBus.goView 应按解析结果写共享 currentView（唯一切视图入口）',
  )
  assert.ok(
    appVue.includes('goView as navBusGoView'),
    'App.vue 应把壳级 goView 委托给 navBus.goView（单一数据源）',
  )
})

test('榜单管理：升为顶层视图后穿上 PageShell（不再自绘页头）', () => {
  assert.ok(
    chartManagerVue.includes("import PageShell from './PageShell.vue'"),
    'ChartManager 应 import PageShell',
  )
  assert.ok(
    chartManagerVue.includes('<PageShell'),
    'ChartManager 应被 PageShell 包住',
  )
  assert.ok(
    !chartManagerVue.includes('mx-auto max-w-[1500px]'),
    '不该再自绘页面容器（那是 PageShell 的职责，两套会双份内边距）',
  )
})
