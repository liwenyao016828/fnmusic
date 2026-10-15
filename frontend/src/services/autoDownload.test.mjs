/* 「自动下载记录 + 音质」这条链的源码约定测试（v2.1.120）。
 *
 * # 为什么用这种形态
 *
 * 这条链的「正确」不在某个纯函数里，而在**四个文件的接线**上：
 *   · 后端把记录写进 pkg/online 的 autolog（写入方是拦截层）；
 *   · 前端通过 AutoDownloadAPI 读它、能多选、能一键清；
 *   · 侧栏「任务状态」里有一行指过去；
 *   · 榜单管理里的「音质下载」真的写回配置。
 * 任何一处接错，界面上都不会报错 —— 只是「记录页永远是空的」或「改了音质没生效」。
 * 组件实例没法在 node 里起（项目没有 DOM 环境），所以照 chartRelocation.test.mjs 的做法：
 * 把真源码读进来，钉住这些**约定**。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel) => readFileSync(join(here, rel), 'utf8')

const clientJs = read('../api/client.js')
const libraryManagerVue = read('../components/LibraryManager.vue')
const chartManagerVue = read('../components/ChartManager.vue')
const queuePanelVue = read('../components/DownloadQueuePanel.vue')
const downloadManagerJs = read('./downloadManager.js')
const chartInjectJs = read('./chartInject.js')
const libraryViewJs = read('./libraryView.js')
const pushStoreGo = read('../../../backend/pkg/push/store.go')
const uiJs = read('../../../backend/pkg/intercept/assets/ui.js')
const qulvbridgeGo = read('../../../backend/pkg/intercept/qulvbridge.go')
const autodownloadGo = read('../../../backend/pkg/intercept/autodownload.go')

test('自动下载记录：API 客户端有读与清两条', () => {
  assert.match(clientJs, /export const AutoDownloadAPI/, '应导出 AutoDownloadAPI')
  assert.ok(clientJs.includes("api.get('/auto-download/records'"), '应有读接口')
  assert.ok(clientJs.includes("api.delete('/auto-download/records'"), '应有清接口')
})

test('自动下载记录：曲库管家有这一页、能多选、能一键清', () => {
  // import 是一条长语句（同一个 import 里还有一堆别的 API），所以只钉「它在这个包里」
  assert.match(libraryManagerVue, /import \{[^}]*AutoDownloadAPI[^}]*\} from '\.\.\/api\/client'/,
    '应 import AutoDownloadAPI')
  assert.match(libraryManagerVue, /\{ id: 'autodl', name: '自动下载记录'/, 'tabs 里应有 autodl 项')
  assert.ok(libraryManagerVue.includes("activeTab === 'autodl'"), '应有该页的渲染分支')
  // 多选：用 Set 存 id，两个动作分别对应「清选中」与「一键清」
  assert.ok(libraryManagerVue.includes('autoDownloadSelected'), '应有勾选集合')
  assert.ok(libraryManagerVue.includes('clearAutoDownload([])'), '「一键清除」应传空 ids')
  assert.ok(libraryManagerVue.includes('clearAutoDownload(selectedAutoIds)'), '「清除选中」应传勾选的 id')
  // 首屏就拉一次：侧栏那行「自动下载失败 N」不该等到用户点进这一页才出现
  assert.match(libraryManagerVue, /onMounted\(\(\) => \{\n {2}loadDownloadPrefs\(\)[\s\S]{0,200}loadAutoDownload\(\)/,
    'onMounted 里应拉一次记录（侧栏角标要用）')
})

test('自动下载记录：侧栏任务状态里有一行指过去', () => {
  assert.ok(libraryViewJs.includes("id: 'autodl-failed'"), 'sidebarStatusItems 应有这一项')
  assert.ok(libraryViewJs.includes("tab: 'autodl'"), '这一项应指向 autodl 侧栏项')
  assert.ok(libraryManagerVue.includes('autoDownload: { failed: autoDownloadFailed.value }'),
    '组件应把失败数喂给 sidebarStatusItems')
})

test('音质下载：榜单管理里的选项真的写回配置', () => {
  assert.ok(chartManagerVue.includes('音质下载'), '榜单管理应有「音质下载」')
  assert.ok(chartManagerVue.includes('@update:model-value="setAutoDownloadQuality"'),
    '选项应接到 setAutoDownloadQuality')
  assert.ok(chartInjectJs.includes('export function setAutoDownloadQuality'), 'chartInject 应导出它')
  assert.ok(chartInjectJs.includes('saveAutoDownloadQuality'), '写入队列应真的发这个键')
  assert.ok(clientJs.includes('auto_download_quality'), '请求体键名应是 auto_download_quality')
  // 后端要认这个键（配置选择性合并两处都登记过，否则「写进去读不回来」）
  assert.ok(autodownloadGo.includes('func (i *Interceptor) autoDownloadQuality()'),
    '后端应有读这个偏好的方法')
})

test('下载队列：多选与一键清除记录都在，且只清「已结束」的', () => {
  assert.ok(queuePanelVue.includes('clearSelected'), '应有「清除选中」')
  assert.ok(queuePanelVue.includes('clearAllRecords'), '应有「一键清除记录」')
  assert.ok(queuePanelVue.includes('function isTerminal('), '应有「已结束」判据')
  assert.ok(downloadManagerJs.includes('function removeTasks('), 'service 应支持批量移除')
  assert.ok(downloadManagerJs.includes('function clearRecords('), 'service 应支持清记录')
  // 「清记录」不能把正在下的歌停掉 —— 那是 clearAll 的语义，两者必须分开。
  assert.match(downloadManagerJs, /function clearRecords\(\)[\s\S]{0,400}'downloading'/,
    'clearRecords 必须保留正在下载的任务')
})

test('试听音质：注入面板的切换按钮接的是同源通道那两条接口', () => {
  assert.ok(uiJs.includes("BASE + '/api/quality'"), '面板应 POST 音质偏好')
  assert.ok(uiJs.includes("BASE + '/api/now?guid='"), '面板应查「这首歌实际用了哪条源、哪一档」')
  assert.ok(uiJs.includes('qualityLabel'), '应有档位标签（认不出的档位照原样显示）')
  // 后端白名单必须真的有这两条 —— 否则脚本发出去只会拿到 404。
  assert.ok(qulvbridgeGo.includes('func (i *Interceptor) handleQulvNow('), '后端应有 now 处理器')
  assert.ok(qulvbridgeGo.includes('func (i *Interceptor) handleQulvQuality('), '后端应有 quality 处理器')
})

test('推送同步历史：多选与一键清除都在，且后端真的支持删', () => {
  assert.ok(libraryManagerVue.includes('clearPushRuns'), '应有清除动作')
  assert.ok(libraryManagerVue.includes('pushRunSelected'), '应有勾选集合')
  assert.ok(libraryManagerVue.includes('clearPushRuns([])'), '「一键清除记录」应传空 ids')
  assert.ok(libraryManagerVue.includes('clearPushRuns(Array.from(pushRunSelected))'), '「清除选中」应传勾选的 id')
  assert.ok(clientJs.includes('clearRuns:'), 'PushAPI 应有 clearRuns')
  assert.ok(pushStoreGo.includes('func (s *Store) ClearRuns('), 'push.Store 应支持清历史')
  // 只删历史、不动任务本身：顺手清掉 LastStatus/LastError 会让一个失败任务
  // 看起来像从没跑过 —— 那正是用户最需要看见的信息。
  const fn = pushStoreGo.slice(pushStoreGo.indexOf('func (s *Store) ClearRuns('))
  const body = fn.slice(0, fn.indexOf('\n}\n'))
  for (const field of ['LastStatus', 'LastError', 'LastResult', 'NextRunAt']) {
    assert.ok(!body.includes(field), `ClearRuns 不该碰 ${field}（任务状态不是历史）`)
  }
})

// 用户 2026-10-07 点名要改的四处里，有两处在曲库管家（推送订阅行的两个能力位、
// 补全整理的四个动作按钮）。它们必须真的用上 ActionCard —— 这一条是补的：
// 第一遍实现时漏了这两处，而变更记录里却写着改完了。
test('曲库管家：推送能力位与补全整理动作都改成了卡片', () => {
  assert.ok(libraryManagerVue.includes("import ActionCard from './ActionCard.vue'"),
    'LibraryManager 应 import ActionCard')
  const uses = (libraryManagerVue.match(/<ActionCard/g) || []).length
  assert.ok(uses >= 4, `补全整理那四个动作应是卡片，实际只找到 ${uses} 个 ActionCard`)
  // 两个能力位并排：包在一层 lg:grid-cols-2 的网格里，且各自是带边框的白卡
  assert.ok(libraryManagerVue.includes('下载到本地曲库</p>'), '保留「下载到本地曲库」标题')
  assert.ok(libraryManagerVue.includes('同步到飞牛歌单</p>'), '保留「同步到飞牛歌单」标题')
  assert.match(libraryManagerVue, /grid-cols-1 gap-3 lg:grid-cols-2/,
    '两个能力位应并排成一行（窄屏再叠）')
  assert.ok(!libraryManagerVue.includes("row.hasDownload ? 'border-t border-gray-100 pt-3' : ''"),
    '并排之后不再需要 border-top 分隔（那是「上下两块」的做法）')
})
