/* 导航搬家（v2.1.119 → v2.1.120）的源码约定测试。
 *
 * # 这两轮搬了什么
 *
 *   v2.1.119：榜单管理 发现音乐 → 曲库管家（第四个侧栏项）
 *   v2.1.120：榜单管理 曲库管家 → **顶层导航**（与「发现音乐」并列）
 *             本地音乐 顶层导航 → **曲库管家**（第一个侧栏项）
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

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel) => readFileSync(join(here, rel), 'utf8')

const chartManagerVue = read('../components/ChartManager.vue')
const searchViewVue = read('../components/SearchView.vue')
const libraryManagerVue = read('../components/LibraryManager.vue')
const nasExplorerVue = read('../components/NasExplorer.vue')
const appVue = read('../App.vue')
const libraryViewJs = read('./libraryView.js')
const navBusJs = read('./navBus.js')

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

test('榜单管理：顶层导航里有 charts 项，且没有 nas 项', () => {
  assert.match(
    appVue,
    /\{ id: 'charts', name: '榜单管理'/,
    'navItems 里应有 charts 导航项',
  )
  assert.ok(
    !/id: 'nas'/.test(appVue),
    "navItems 里不该再有 nas 项（本地音乐已搬进曲库管家）",
  )
  assert.ok(
    !appVue.includes('#view-nas'),
    '不该再有 view-nas 插槽 —— 那个顶层视图已经不存在了',
  )
  assert.ok(
    appVue.includes('#view-charts'),
    '应有 view-charts 插槽（SwipePager 按插槽名渲染视图）',
  )
})

test('本地音乐：搬进曲库管家，挂载点与侧栏项都在', () => {
  assert.ok(
    libraryManagerVue.includes("import NasExplorer from './NasExplorer.vue'"),
    'LibraryManager 应 import NasExplorer',
  )
  assert.ok(
    libraryManagerVue.includes('<NasExplorer'),
    'LibraryManager 模板应挂载 NasExplorer',
  )
  assert.match(
    libraryManagerVue,
    /\{ id: 'nas', name: '本地音乐'/,
    'tabs 列表里应有 nas 侧栏项',
  )
  // 子页不能自带 PageShell：否则会出现两个标题栏、双份内边距与两个滚动容器。
  assert.ok(
    !nasExplorerVue.includes('<PageShell'),
    'NasExplorer 不该再自带 PageShell（外壳由曲库管家提供）',
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

test('榜单管理：指路按钮切顶层视图，不再借 payload 让管家落侧栏项', () => {
  assert.ok(
    searchViewVue.includes('openChartManager'),
    '发现页应有 openChartManager（指路入口）',
  )
  assert.ok(
    searchViewVue.includes("navigateTo('charts')"),
    "openChartManager 应直接切到 charts 顶层视图",
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
  assert.ok(appVue.includes('onNavigate((view, payload)'), 'App.vue 的订阅应接住 payload 参数')
  assert.ok(appVue.includes('currentView.value = view'), 'App.vue 应按请求切视图')
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
