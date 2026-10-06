import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  LEGACY_TABS,
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
} from './libraryView.js'

const NOW = 1758888000000 // 2025-09-26T12:00:00Z 附近，测试里当「现在」

test('normalizeLibraryTab：旧 id 落到新位置，认不出来的落到推送', () => {
  assert.equal(normalizeLibraryTab('tidy'), 'complete')
  assert.equal(normalizeLibraryTab('playlist'), 'download')
  assert.equal(normalizeLibraryTab('ai'), 'push')
  for (const id of ['push', 'download', 'complete']) assert.equal(normalizeLibraryTab(id), id)
  // 空 / 垃圾 / 已删除的页：都不能落空（模板里没有 v-else 兜底，落空就只剩标题栏）
  for (const bad of ['', null, undefined, 'nope', 'setting']) assert.equal(normalizeLibraryTab(bad), 'push')
  assert.deepEqual(Object.keys(LEGACY_TABS).sort(), ['ai', 'playlist', 'tidy'])
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
  assert.equal(gapsAtText({ at: NOW - 500 }, NOW), '刚刚')
  assert.equal(gapsAtText({ at: NOW - 5 * 60000 }, NOW), '5 分钟前')
  assert.equal(gapsAtText({ at: NOW - 3 * 3600000 }, NOW), '3 小时前')
  assert.equal(gapsAtText({ at: NOW - 2 * 86400000 }, NOW), '2 天前')
  // 单位钉死：若被当成秒（错单位），会算成两万天 —— 真机上「1375 天前」就是这么来的
  assert.match(gapsAtText({ at: Math.floor(NOW / 1000) }, NOW), /^\d{4,} 天前$/)
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
    'push-running', 'baseline', 'push-failed', 'push-pending', 'push-missing',
  ])
  // 每一项都要能点进去（tab 就是跳转目标），否则它只是摆设
  assert.deepEqual(full.map((s) => s.tab), [
    'complete', 'download', 'download', 'download', 'push', 'push', 'push', 'push', 'push',
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
