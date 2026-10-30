import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  LEGACY_TABS,
  TAB_IDS,
  normalizeLibraryTab,
  lastScanText,
  gapsAtText,
  unsupportedExtsText,
  gapsPendingCount,
  sidebarStatusItems,
  completeTitleText,
  completePercent,
  aiMatchedCount,
  completeDoneText,
  COMPLETE_STEPS,
  completeStep,
  completeHint,
  relWhen,
  fmtWhen,
} from './libraryView.js'

const NOW = 1758888000000 // 2025-09-26T12:00:00Z 附近，测试里当「现在」

test('normalizeLibraryTab：旧 id 落到新位置，认不出来的落到推送', () => {
  // ⚠️ 2026-10-28：整理页的「补全」分段下线后，'complete' 这个 id 有两种身份：
  //   · 老浏览器偏好里的 library_tab（侧栏项）—— 现在没有这一项了，必须落到默认项；
  //   · app 内跳转的 view id（navViews 的 ALIASES 里 complete → 整理页）。
  // 因此 LEGACY_TABS 不能再写 tidy → 'complete'：那会把一个已删除的侧栏项还回来。
  // 这里钉「tidy 不再映射到已删除的项」+「'complete' 作为侧栏项落空后仍兜到 push」。
  assert.equal(LEGACY_TABS.tidy, undefined)
  assert.equal(normalizeLibraryTab('complete'), 'push')
  assert.equal(normalizeLibraryTab('tidy'), 'push')
  assert.equal(normalizeLibraryTab('playlist'), 'download')
  assert.equal(normalizeLibraryTab('ai'), 'push')
  for (const id of ['nas', 'push', 'download', 'autodl']) assert.equal(normalizeLibraryTab(id), id)
  // 空 / 垃圾 / 已删除的页：都不能落空（模板里没有 v-else 兜底，落空就只剩标题栏）。
  // 'charts' 是**被搬走**的侧栏项（v2.1.120 升为顶层导航），老偏好里留着它必须落到默认项。
  for (const bad of ['', null, undefined, 'nope', 'setting', 'charts']) assert.equal(normalizeLibraryTab(bad), 'push')
  assert.deepEqual(Object.keys(LEGACY_TABS).sort(), ['ai', 'playlist'])
})

test('TAB_IDS：本地音乐（nas）与自动下载记录（autodl）是侧栏项，榜单管理已搬出去', () => {
  // nas 必须是合法侧栏项 —— 否则点侧栏「本地音乐」会被 normalize 悄悄落回「推送」，
  // 用户看到的就是「点了没反应」。
  assert.ok(TAB_IDS.has('nas'), 'nas 应在 TAB_IDS 里')
  assert.ok(TAB_IDS.has('autodl'), 'autodl 应在 TAB_IDS 里')
  // 2026-10-28：'complete'（补全）分段下线，不再是合法侧栏项
  assert.deepEqual([...TAB_IDS].sort(), ['autodl', 'download', 'nas', 'push'])
  // 它是 v2.1.120 从顶层导航搬进来的，没有旧身份：LEGACY_TABS 里**不许**出现 nas ——
  // 出现意味着有人给新页编了个「旧 id」，会掩盖真正的跳转 bug。
  assert.ok(!('nas' in LEGACY_TABS))
  // charts 同一轮搬**出去**了（升为顶层导航），不再属于这本侧栏。
  assert.ok(!TAB_IDS.has('charts'), 'charts 不该再在 TAB_IDS 里')
  // 旧偏好里可能还留着 discover_tab='manage'（发现页旧胶囊的键）。
  // 那个键从来不归管家管 —— 两个键不许再串。
  assert.equal(normalizeLibraryTab('manage'), 'push', 'manage 不是管家的侧栏项，别把发现页的旧 tab 键混进来')
})

// 自动下载（飞牛页面里收藏/加歌单/点下载触发的那批）只报**失败**：
// 成功的那些是流水，摆在侧栏当常态数字只会让人习惯性忽略这一行 ——
// 而这一行的用处恰恰是「有失败时让人注意到」。
test('sidebarStatusItems：自动下载只在有失败时出现，且指向 autodl 侧栏项', () => {
  const none = sidebarStatusItems({ autoDownload: { failed: 0 } })
  assert.equal(none.filter(s => s.id === 'autodl-failed').length, 0, '没有失败就不该出现')

  const some = sidebarStatusItems({ autoDownload: { failed: 2 } })
  const item = some.find(s => s.id === 'autodl-failed')
  assert.ok(item, '有失败时必须出现')
  assert.equal(item.count, 2)
  assert.equal(item.tone, 'error')
  // tab 决定点它跳到哪儿 —— 指错了用户会落到一个没有这条记录的页面上。
  assert.equal(item.tab, 'autodl')
})

test('lastScanText：scanned_at 是「秒」，不是毫秒', () => {
  assert.equal(lastScanText(null, NOW), '')
  assert.equal(lastScanText({}, NOW), '')
  assert.equal(lastScanText({ scanned_at: 0 }, NOW), '')
  const sec = Math.floor(NOW / 1000)
  assert.equal(lastScanText({ scanned_at: sec }, NOW), '上次整理：刚刚')
  assert.equal(lastScanText({ scanned_at: sec - 30 }, NOW), '上次整理：刚刚')
  assert.equal(lastScanText({ scanned_at: sec - 5 * 60 }, NOW), '上次整理：5 分钟前')
  assert.equal(lastScanText({ scanned_at: sec - 3 * 3600 }, NOW), '上次整理：3 小时前')
  assert.equal(lastScanText({ scanned_at: sec - 2 * 86400 }, NOW), '上次整理：2 天前')
  // 单位钉死：传毫秒（错单位）会被算成「刚刚」，而正确读法应是 5 分钟前
  assert.equal(lastScanText({ scanned_at: NOW - 5 * 60000 }, NOW), '上次整理：刚刚')
})

test('gapsAtText：gaps.at 是「毫秒」（与 lastScanText 不是一个单位）', () => {
  assert.equal(gapsAtText(null, NOW), '')
  assert.equal(gapsAtText({}, NOW), '')
  assert.equal(gapsAtText({ at: 0 }, NOW), '')
  // P2-10（2026-10-27）：加了「上次统计：」前缀 —— 原来是裸的「5 分钟前」，
  // 和旁边的「上次整理：3 小时前」并排时分不清说的是哪件事。
  assert.equal(gapsAtText({ at: NOW - 500 }, NOW), '上次统计：刚刚')
  assert.equal(gapsAtText({ at: NOW - 5 * 60000 }, NOW), '上次统计：5 分钟前')
  assert.equal(gapsAtText({ at: NOW - 3 * 3600000 }, NOW), '上次统计：3 小时前')
  assert.equal(gapsAtText({ at: NOW - 2 * 86400000 }, NOW), '上次统计：2 天前')
  // 单位钉死：若被当成秒（错单位），会算成两万天 —— 真机上「1375 天前」就是这么来的
  assert.match(gapsAtText({ at: Math.floor(NOW / 1000) }, NOW), /^上次统计：\d+ (天|个月)前$/)
})

test('relWhen：超过 30 天改说「个月前」，不再报「1375 天前」', () => {
  assert.equal(relWhen(0, NOW), '')
  assert.equal(relWhen(null, NOW), '')
  assert.equal(relWhen(NOW - 1000, NOW), '刚刚')
  assert.equal(relWhen(NOW - 90 * 60000, NOW), '1 小时前')
  assert.equal(relWhen(NOW - 45 * 86400000, NOW), '1 个月前')
})

test('fmtWhen：秒 / 毫秒 / ISO 串 / Date 都能认，输出固定 YYYY-MM-DD HH:mm', () => {
  assert.equal(fmtWhen(''), '')
  assert.equal(fmtWhen(0), '')
  assert.equal(fmtWhen('0'), '')
  assert.equal(fmtWhen(null), '')
  // 秒（< 1e11）按秒解释
  assert.equal(fmtWhen(Date.UTC(2026, 9, 27, 10, 39) / 1000), fmtWhen(Date.UTC(2026, 9, 27, 10, 39)))
  // 毫秒
  assert.equal(fmtWhen(Date.UTC(2026, 9, 27, 10, 39)), fmtWhen(new Date(Date.UTC(2026, 9, 27, 10, 39))))
  // ISO 串
  assert.equal(fmtWhen('2026-10-27T10:39:00.000Z'), fmtWhen(new Date(Date.UTC(2026, 9, 27, 10, 39))))
  // 格式固定：不带秒、不带 AM/PM，跨语言环境不会变
  assert.match(fmtWhen(Date.UTC(2026, 9, 27, 10, 39)), /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  // 解析不了的串原样返回，不抛
  assert.equal(fmtWhen('不是时间'), '不是时间')
  // 「2026/10/27 18:39:52」这种带秒的旧文案不会再出现
  assert.ok(!fmtWhen(Date.UTC(2026, 9, 27, 10, 39)).includes(':' + '39' + ':00'))
})

test('unsupportedExtsText：按数量倒序，同数量按名字，缺字段不显示', () => {
  assert.equal(unsupportedExtsText(undefined), '')
  assert.equal(unsupportedExtsText(null), '')
  assert.equal(unsupportedExtsText({}), '')
  assert.equal(unsupportedExtsText('wav'), '')
  assert.equal(unsupportedExtsText({ wav: 1 }), 'wav×1')
  assert.equal(unsupportedExtsText({ wav: 3, opus: 12, ape: 3 }), 'opus×12、ape×3、wav×3')
})

test('gapsPendingCount：0 不显示角标，缺数据返回空串', () => {
  assert.equal(gapsPendingCount(null), '')
  assert.equal(gapsPendingCount({}), '')
  assert.equal(gapsPendingCount({ any_field: 0 }), '')
  assert.equal(gapsPendingCount({ any_field: 6 }), 6)
})

test('sidebarStatusItems：只列需要处理的事，且顺序稳定', () => {
  const full = sidebarStatusItems({
    completeJob: { running: true, total: 6 },
    download: { active: 2, failed: 1, paused: 3 },
    pushRunning: 1,
    pendingBaseline: 4,
    monitorStats: { failed: 5, pending: 6, missing: 7 },
  })
  assert.deepEqual(full.map((s) => s.id), [
    'complete-running', 'dl-active', 'dl-failed', 'dl-paused',
  ])
  // 每一项都要能点进去（tab 就是跳转目标），否则它只是摆设
  // 2026-10-28：补全任务的状态项改跳「推送」（补全段已下线，原先的 tab:'complete' 会落空）
  // 2026-10-29：订阅相关的 5 项（push-running / baseline / push-failed / push-pending /
  //   push-missing）已按用户要求移出左下角任务状态 —— 它们改在导航「订阅更新」上显示。
  assert.deepEqual(full.map((s) => s.tab), [
    'push', 'download', 'download', 'download',
  ])
  for (const s of full) {
    assert.ok(s.label, `${s.id} 缺文案`)
    assert.ok(s.count > 0, `${s.id} 计数应大于 0 —— 0 的不该出现在这个列表里`)
    assert.ok(['progress', 'warn', 'error', 'muted'].includes(s.tone), `${s.id} 档位未知：${s.tone}`)
  }
  // 恒为 0 的一律不出现
  assert.deepEqual(sidebarStatusItems({ download: { active: 0, failed: 0, paused: 0 } }), [])
  assert.deepEqual(sidebarStatusItems(), [])
  assert.deepEqual(sidebarStatusItems({ monitorStats: { failed: 0, pending: 0, missing: 0 } }), [])
})

test('sidebarStatusItems：跑着的补全压住「写入失败」，总数缺省为 0', () => {
  const items = sidebarStatusItems({ completeJob: { running: true, summary: { failed: 9 } } })
  assert.deepEqual(items.map((s) => s.id), ['complete-running'])
  assert.equal(items[0].count, 0)
  const failed = sidebarStatusItems({ completeJob: { running: false, summary: { failed: 2 } } })
  assert.deepEqual(failed.map((s) => s.id), ['complete-failed'])
  assert.equal(failed[0].tone, 'error')
  assert.equal(failed[0].count, 2)
})

test('completeTitleText：四态不混（被停止 ≠ 跑完）', () => {
  assert.equal(completeTitleText(null), '')
  assert.equal(completeTitleText({ running: true }), '正在补全…')
  assert.equal(completeTitleText({ running: true, cancelled: true }), '正在停止…')
  assert.equal(completeTitleText({ running: false }), '上次补全结果')
  assert.equal(completeTitleText({ running: false, cancelled: true }), '上次补全被停止')
})

test('completePercent：没有总数 0，向上取整，封顶 100', () => {
  assert.equal(completePercent(null), 0)
  assert.equal(completePercent({ done: 3, total: 0 }), 0)
  assert.equal(completePercent({ done: 1, total: 3 }), 33)
  assert.equal(completePercent({ done: 2, total: 3 }), 67)
  assert.equal(completePercent({ done: 3, total: 3 }), 100)
  assert.equal(completePercent({ done: 5, total: 3 }), 100)
})

test('aiMatchedCount：数出「AI 挑的」而不是「规则配上的」', () => {
  assert.equal(aiMatchedCount(null), 0)
  assert.equal(aiMatchedCount({}), 0)
  assert.equal(aiMatchedCount({ results: [{}, { matched_by_ai: false }] }), 0)
  assert.equal(aiMatchedCount({ results: [{ matched_by_ai: true }, {}, { matched_by_ai: true }] }), 2)
})

test('completeDoneText：停止单独说，跑完报六个字段，没搜到才追加', () => {
  const done = {
    cancelled: false,
    summary: {
      lyric_filled: 3, cover_filled: 3, year_filled: 2, track_filled: 1, disc_filled: 1,
      genre_filled: 0, no_match: 0, results: [{}],
    },
  }
  assert.equal(
    completeDoneText(done),
    '补全完成：歌词 3 · 封面 3 · 年份 2 · 曲序 1 · 光盘 1 · 风格 0',
  )
  assert.equal(
    completeDoneText({ ...done, summary: { ...done.summary, no_match: 2 } }),
    '补全完成：歌词 3 · 封面 3 · 年份 2 · 曲序 1 · 光盘 1 · 风格 0 · 没搜到 2 首',
  )
  const stopped = {
    cancelled: true,
    summary: { results: [{}, {}, {}], cancelled: 6, no_match: 0 },
  }
  // ⚠️ 被停止的那句必须带上「没处理」与「可以撤销回退」——
  // 否则用户以为「点了停止也全补好了」，或者不敢点停止
  assert.equal(completeDoneText(stopped), '补全已停止：已处理 3 首，6 首没处理（已写入的可以撤销回退）')
  assert.equal(completeDoneText({ cancelled: true }), '')
  // 后端漏字段时不能把 undefined 印在界面上
  const partial = completeDoneText({ cancelled: false, summary: { lyric_filled: 1 } })
  assert.ok(!/undefined|NaN/.test(partial), partial)
  assert.equal(partial, '补全完成：歌词 1 · 封面 0 · 年份 0 · 曲序 0 · 光盘 0 · 风格 0')
})

// ── 补全整理的三步流程 ──
// 用户反馈的原话：「我点击了扫描缺失，也正常出现了缺失的歌曲数量。但是，下一步怎么做呢？」
// 这三步必须和界面上的按钮一一对上，且只由 gaps / 预览是否拿到 plan_id 推出。
test('三步流程：没扫过缺口停在第 1 步，扫过停在第 2 步，出了预览计划停在第 3 步', () => {
  assert.equal(completeStep(null, null), 1)
  // 缺口是持久化恢复的（刷新页面后还在），所以刷新后仍应停在第 2 步
  assert.equal(completeStep({ total: 10 }, null), 2)
  // 预览出来了就一定要跳到第 3 步 —— 否则界面会一边让用户「勾字段」一边已经能写入了
  assert.equal(completeStep({ total: 10 }, true), 3)
  // 没有缺口统计也能直接预览（后端自己会扫），此时仍算第 3 步
  assert.equal(completeStep(null, true), 3)
  // 流式预览「停了」= 没拿到 plan_id，必须退回第 2 步 —— 那份清单不完整、不能写
  assert.equal(completeStep({ total: 10 }, false), 2)
  // 正在预览（还没出 plan_id）也不能退回第 1 步：否则跑到一半「扫描缺失」按钮又冒出来
  assert.equal(completeStep(null, false, true), 2)
})

test('三步流程：每一步都要有一句「下一步点哪」，且不能指错按钮', () => {
  // 第 1 步指向的就是卡片里那个真实存在的「扫描」按钮（2026-10-27：原来写的是「扫描缺失」，
  // 按钮早就并进字段 chip 行改名了，提示里那个名字在界面上根本找不到）
  assert.match(completeHint(null, null), /扫描/)
  assert.ok(!/扫描缺失/.test(completeHint(null, null)))
  assert.match(completeHint({ total: 10 }, null), /预览要写的内容/)
  // 预览按钮在卡片头右上角 —— 提示里要说清位置，用户才找得到
  assert.match(completeHint({ total: 10 }, null), /右上角/)
  assert.match(completeHint({ total: 10 }, true), /确认写入/)
  // 正在预览时提示「可以停止」，而不是叫人在跑的时候再点一次预览
  assert.match(completeHint(null, false, true), /停止预览/)
  // 第 1 步那句不能提前叫人点「预览」—— 字段还没勾，点了也白点
  assert.ok(!/预览/.test(completeHint(null, null)))
})
