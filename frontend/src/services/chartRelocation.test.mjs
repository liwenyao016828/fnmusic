/* 榜单管理搬家（v2.1.119）的源码约定测试。
 *
 * # 为什么用这种形态
 *
 * 搬家这件事的「正确」不在某个纯函数里，而在**四个文件的接线**上：
 *   · ChartManager 只被曲库管家挂载（发现页不再 import）；
 *   · 发现页不再有「manage」这个子标签（胶囊、挂载点、偏好键全撤）；
 *   · 管家的侧栏项集合里多了 charts；
 *   · 「▶ 看一眼」从管家回到发现页走导航总线（navigateTo('search', { chartPreview })）。
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
const appVue = read('../App.vue')
const libraryViewJs = read('./libraryView.js')
const navBusJs = read('./navBus.js')

test('榜单管理：发现页不再 import ChartManager（挂载点已搬走）', () => {
  assert.ok(
    !searchViewVue.includes("import ChartManager"),
    'SearchView 不该再 import ChartManager —— 独立入口已撤销',
  )
  assert.ok(
    !searchViewVue.includes('<ChartManager'),
    'SearchView 模板里不该再挂 ChartManager',
  )
})

test('榜单管理：发现页不再有 manage 子标签（胶囊 / 挂载 / switchDiscoverTab 分支）', () => {
  // 胶囊按钮与挂载点的 v-if 都用 discoverTab === 'manage' 判定 —— 撤干净才算搬家完成
  assert.ok(
    !searchViewVue.includes("switchDiscoverTab('manage')"),
    '不该再有指向 manage 的切换入口',
  )
  assert.ok(
    !searchViewVue.includes("discoverTab === 'manage'"),
    '不该再有 manage 的渲染分支',
  )
  // 持久化键的注释口径同步：discover_tab 的合法值只剩两个
  assert.match(
    searchViewVue,
    /discover_tab[^]*?'charts' \| 'playlists'/,
    "discoverTab 注释应说明合法值只剩 'charts' | 'playlists'",
  )
})

test('榜单管理：曲库管家挂载它，且侧栏项集合里有 charts', () => {
  assert.ok(
    libraryManagerVue.includes("import ChartManager from './ChartManager.vue'"),
    'LibraryManager 应 import ChartManager',
  )
  assert.ok(
    libraryManagerVue.includes("<ChartManager"),
    'LibraryManager 模板应挂载 ChartManager',
  )
  assert.match(
    libraryManagerVue,
    /\{ id: 'charts', name: '榜单管理'/,
    'tabs 列表里应有 charts 侧栏项',
  )
  // 组件内部逻辑不动（口径：只挪挂载位置）—— 文件本身不该被改出行为分叉的痕迹
  assert.ok(
    chartManagerVue.includes("emit('preview'"),
    'ChartManager 仍通过 preview 事件对外说话',
  )
})

test('榜单管理：管家 → 发现的「看一眼」走导航总线且负载形如 { chartPreview }', () => {
  assert.ok(
    libraryManagerVue.includes("navigateTo('search', { chartPreview: item })"),
    '管家应调 navigateTo 带 chartPreview 负载',
  )
  assert.ok(
    searchViewVue.includes('payload?.chartPreview'),
    '发现页应消费 chartPreview 负载',
  )
  // 总线本身要支持第二个参数（这次为它加的；老调用方不传，行为不变）
  assert.match(
    navBusJs,
    /export function navigateTo\(view, payload\)/,
    'navigateTo 应带可选 payload 参数',
  )
  // App.vue 是路由器：发现→管家的方向由它落侧栏项；管家→发现只切视图，
  // chartPreview 负载原样传给订阅者（SearchView 自己消费 —— 订阅回调拿到完整 payload）。
  assert.ok(appVue.includes('onNavigate((view, payload)'), 'App.vue 的订阅应接住 payload 参数')
  assert.ok(appVue.includes('currentView.value = view'), 'App.vue 应按请求切视图')
  assert.ok(appVue.includes("libraryTab.value = 'charts'"), 'App.vue 应把管家落到 charts 侧栏项（发现→管家）')
  assert.ok(appVue.includes('libraryNavNonce.value++'), '侧栏项跳转要带 nonce（已挂载实例靠它触发 watch）')
})

test('榜单管理：指路按钮跳管家而不是已删除的 manage 页', () => {
  assert.ok(
    searchViewVue.includes('openChartManager'),
    '发现页应有 openChartManager（指路入口）',
  )
  assert.ok(
    !searchViewVue.includes("'manage'"),
    "SearchView 里不该再出现 'manage' 字面量（死键）",
  )
})
