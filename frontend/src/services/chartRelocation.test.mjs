/* 导航搬家 + 榜单页融合（v2.1.119 → v2.1.120 → 2026-10-15 → 2026-10-18）的源码约定测试。
 *
 * # 这几轮搬了什么
 *
 *   v2.1.119：榜单管理 发现音乐 → 曲库管家（第四个侧栏项）
 *   v2.1.120：榜单管理 曲库管家 → **顶层导航**（与「发现音乐」并列）
 *             本地音乐 顶层导航 → **曲库管家**（第一个侧栏项）
 *   2026-10-15（v2.4.0）：榜单管理 顶层导航 → **「发现」页的第二段**（页内分段）
 *             导航 7 项并成 5 项，视图清单的唯一数据源改为 services/navViews.js
 *   2026-10-18（v2.6.0）：**合并「导航榜单」与「发现里的排行榜单」**
 *             用户口径：「发现音乐顶部导航榜单与发现排行榜单重复了吧。融合一下，保留发现
 *             排行榜单的内部卡片 ui，替换到导航榜单……逻辑不要重复」。
 *             做法：删掉 ChartManager.vue，把它那批开关收成**同一个网格上方的一个折叠面板**；
 *             「榜单」段与发现页「排行榜单」胶囊从此指向同一份 UI —— 结构上不可能再漂移。
 *
 * # 为什么用这种形态
 *
 * 搬家/融合这件事的「正确」不在某个纯函数里，而在**几个文件的接线**上：
 *   · 榜单网格只有一份实现（在 SearchView.vue 里），ChartManager.vue 必须已经不存在；
 *   · 但 `charts` 这个**入口词**必须仍然有效（resolveRoute 解析成 search + charts 段）；
 *   · NasExplorer 只被曲库管家挂载；
 *   · 「▶ 看一眼」从榜单卡片回到详情仍走导航总线。
 * 任何一处接错，界面上都不会报错 —— 只是「入口没了」或「点了没反应」。
 * 组件实例没法在 node 里起（项目没有 DOM 环境），所以照 pageUi.test.mjs 的做法：
 * 把真源码读进来，钉住这些**约定**。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { resolveRoute, VIEW_IDS } from './navViews.js'

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel) => readFileSync(join(here, rel), 'utf8')
const abs = (rel) => join(here, rel)

const searchViewVue = read('../components/SearchView.vue')
const libraryManagerVue = read('../components/LibraryManager.vue')
const nasExplorerVue = read('../components/NasExplorer.vue')
const appVue = read('../App.vue')
const libraryViewJs = read('./libraryView.js')
const navBusJs = read('./navBus.js')
const navViewsJs = read('./navViews.js')

test('榜单融合（2026-10-18）：独立的「榜单管理」组件已经不存在', () => {
  assert.ok(
    !existsSync(abs('../components/ChartManager.vue')),
    'ChartManager.vue 必须已删除 —— 榜单网格与那批开关都住在 SearchView.vue 里，留两个副本就是用户说的「逻辑重复」',
  )
  assert.ok(
    !appVue.includes('ChartManager'),
    'App.vue 不该再提到 ChartManager（import / 模板 / 槽位都不该有）',
  )
  assert.ok(
    !/import ChartManager/.test(searchViewVue),
    'SearchView 也不该 import ChartManager（它是宿主，不该反过来依赖被删掉的那一页）',
  )
})

test('榜单融合：榜单卡片网格在 SearchView 里，且只长在「榜单」段（2026-10-18 二次收口）', () => {
  // 网格本体（卡片 + 注入按钮）只此一份
  assert.ok(
    searchViewVue.includes('isChartInjected(chart)'),
    'SearchView 里应有榜单卡片的注入按钮（网格本体）',
  )
  assert.ok(
    searchViewVue.includes('openChartDetail(chart)'),
    'SearchView 里应能打开榜单详情',
  )
  // 2026-10-18 用户口径：「去掉排行榜单和精选歌单。发现页默认暂时歌单。」
  // —— 发现段只剩精选歌单，榜单网格只认 segment='charts'。
  assert.match(
    searchViewVue,
    /v-if="currentMode === 'discover' && isChartsSegment"/,
    '榜单网格的显示条件应只认「榜单段」（发现页不再重复渲染它）',
  )
  // 榜单段必须有那批开关（原 ChartManager 的能力，一个都不能丢）
  for (const needle of [
    'showInjectPanel',
    'selectAllLoaded',
    'clearChartInjects',
    'selectOnlyChartPlatform',
    'toggleChartInjectEnabled',
    'toggleUIInject',
    'setAutoDownloadQuality',
  ]) {
    assert.ok(searchViewVue.includes(needle), `榜单段应带 ${needle}（原榜单管理页的能力）`)
  }
})

test('发现段的「榜单 / 精选歌单」切换胶囊已撤（2026-10-18 用户口径）', () => {
  // 「去掉排行榜单和精选歌单。发现页默认暂时歌单。」
  assert.ok(
    !searchViewVue.includes('discoverTab'),
    'SearchView 里不该再有 discoverTab（发现段只剩歌单一处内容，没有第二个 tab）',
  )
  assert.ok(
    !searchViewVue.includes('switchDiscoverTab'),
    'switchDiscoverTab 应已删除（没有第二个 tab 可切）',
  )
  assert.ok(
    !searchViewVue.includes("loadPref('discover_tab'") && !searchViewVue.includes("savePref('discover_tab'"),
    "不该再读写旧偏好 discover_tab（留着就是死键；注释里提它是可以的历史说明）",
  )
  // 歌单网格是发现段的**唯一**内容：条件里不该再出现 tab 判断
  assert.match(
    searchViewVue,
    /v-else-if="currentMode === 'discover' && !isChartsSegment"/,
    '精选歌单网格应是发现段唯一内容（只按段判断，不按 tab）',
  )
})

test('榜单仍是一等入口：charts 这个词必须还能解析到「发现音乐 + 榜单段」', () => {
  const r = resolveRoute('charts')
  assert.equal(r.view, 'search')
  assert.equal(r.segment, 'charts')
  assert.ok(VIEW_IDS.includes(r.view), 'charts 该落在已登记的顶层页里')
  assert.ok(appVue.includes('#view-search'), '应有 view-search 插槽（SwipePager 按插槽名渲染视图）')
  assert.ok(appVue.includes("decoratedSegments('search')"), '发现音乐应有页内分段器（发现 / 榜单）')
  assert.ok(
    appVue.includes("segMounted('search', 'discover')"),
    '发现音乐应挂载 SearchView（它一个人负责两段）',
  )
  assert.ok(
    searchViewVue.includes('isChartsSegment'),
    'SearchView 应认得 segment prop（否则榜单段会渲染成发现页）',
  )
  assert.ok(
    searchViewVue.includes('segment: { type: String'),
    'SearchView 应声明 segment prop',
  )
})

test('本地音乐：就是「本地曲库」顶层视图本身（2026-10-15 框架重做）', () => {
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

test('榜单管理不再是另一个入口：指路按钮已撤，榜单段自己就是面板', () => {
  // 2026-10-18 二次收口：发现页不再渲染榜单网格，那条「去榜单管理勾选」的指路条
  // 也跟着删了（它指向的按钮就在自己旁边，纯冗余）。openChartManager 随之成为死代码。
  assert.ok(
    !searchViewVue.includes('openChartManager'),
    'openChartManager 应已删除（指路条撤了，没有人再调它）',
  )
  assert.ok(
    !searchViewVue.includes("navigateTo('charts')"),
    '不该再有 navigateTo(\'charts\') —— charts 段靠导航分段器进，不需要页内再跳一次',
  )
  // 榜单管理的能力必须还在（只是不再需要一个「跳过去」的按钮）
  assert.ok(
    searchViewVue.includes('toggleChartInject(chart)'),
    '榜单卡片的「+ 注入 / ★ 已注入」按钮必须还在（那才是真入口）',
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

test('「▶ 看一眼」回到发现页仍走导航总线，负载形如 { chartPreview }', () => {
  assert.ok(
    searchViewVue.includes('payload?.chartPreview'),
    '发现页应消费 chartPreview 负载（榜单卡片点进去）',
  )
  assert.ok(
    searchViewVue.includes('openChartPreview'),
    '发现页应有 openChartPreview 接住负载',
  )
  assert.match(
    navBusJs,
    /export function navigateTo\(view, payload\)/,
    'navigateTo 应带可选 payload 参数',
  )
  assert.ok(appVue.includes('onNavigate('), 'App.vue 的订阅应接住 payload 参数')
  assert.ok(
    navBusJs.includes('currentView.value = route.view'),
    'navBus.goView 应按解析结果写共享 currentView（唯一切视图入口）',
  )
  assert.ok(
    appVue.includes('goView as navBusGoView'),
    'App.vue 应把壳级 goView 委托给 navBus.goView（单一数据源）',
  )
})

test('手机底部标签栏：下载收进「更多」，整理顶上（2026-10-18 用户口径）', () => {
  // 「补全整理和下载替换一下。下载放到更多里面。」
  assert.match(
    navViewsJs,
    /MOBILE_TAB_IDS = \['search', 'library', 'push', 'tidy'\]/,
    '手机标签栏应是 搜索 / 本地曲库 / 订阅更新 / 补全整理 —— 下载不在其中',
  )
  assert.ok(
    !/'download'\s*\]/.test(navViewsJs.match(/MOBILE_TAB_IDS = \[[^\]]*\]/)?.[0] || ''),
    'download 不该留在手机标签栏里',
  )
  // 抽屉里必须真的能到下载（否则这个入口就凭空消失了）
  assert.ok(
    appVue.includes('moreItems'),
    'App.vue 应有 moreItems（= 不在标签栏里的视图，含下载）',
  )
  assert.ok(
    appVue.includes('showMoreDrawer'),
    'App.vue 应挂载「更多」抽屉',
  )
  // 下载现在住在抽屉里：进行中数量必须挂在标签栏的「更多」入口上，否则完全没有提示
  assert.match(
    appVue,
    /item\.id === 'download' && downloadManager\.activeCount\.value > 0/,
    '「更多」入口应带下载进行中的数量徽标',
  )
})

test('手机底部：搜索是「灵动岛」（导航椭圆 + 搜索**正圆**，点开融合成一框）', () => {
  // 用户口径：「以 2:1 的方式展示导航和搜索框（中间要间隙，左边是椭圆，右边是个搜索圆。
  // 点击搜索圆，融合两个圆并出现搜索框）」
  // 2026-10-18 追加：「底部导航右边的搜索椭圆大了，就正常正圆就可以了。」
  // → 右圆从 flex-1（被抻宽成椭圆）改成固定 w-14 的**正圆**；左椭圆吃满剩余宽度。
  assert.ok(appVue.includes('islandOpen'), '手机底部应有灵动岛开合状态')
  assert.match(appVue, /w-14 shrink-0/, '搜索圆应是固定宽的正圆（w-14 = 56px，高也是 56px）')
  assert.ok(!/flex-\[2\]/.test(appVue), '不该再有 flex-[2]（那会把右圆抻成椭圆）')
  assert.match(appVue, /gap-2/, '闭态两形状之间要有间隙')
  assert.match(appVue, /islandOpen \? 'gap-0' : 'gap-2'/, '点开后间隙收到 0（两个形状贴合）')
  assert.ok(appVue.includes('openIsland'), '点右圆应展开灵动岛')
  assert.ok(appVue.includes('submitIslandSearch'), '灵动岛内提交搜索应走同一套落地（searchFromPlayerBar）')
  assert.ok(appVue.includes('closeIsland'), '应能收回灵动岛（Esc / 点外面 / 提交后）')
  // 桌面端那颗搜索图标必须退场，否则两个搜索入口叠在右下角
  const playerBarVue = read('../components/PlayerBar.vue')
  assert.ok(
    playerBarVue.includes('max-sm:hidden'),
    'PlayerBar 的搜索框/图标应在手机上隐藏（手机由灵动岛接管，两者同在右下角会重叠）',
  )
  assert.ok(
    appVue.includes('island-live-dot'),
    '搜索圆上应有「正在播放」的实时小圆点（用户口径：第二个实时显示小圆）',
  )
  assert.ok(
    read('../../src/style.css').includes('.island-live-dot'),
    'style.css 应有 .island-live-dot 的呼吸动画',
  )
})
