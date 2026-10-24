// 库体检前端规则的单测。
//
// 这些函数都不碰 DOM，但每一条都对应一个「界面上会误导用户」的分歧：
//   · 把 0 字节音频算成「会让文件夹报错」→ 用户以为清完残留就能修好未知错误；
//   · 「一键修复全部」把 fix=manual 的条目也塞给后端 → socket/软链接被乱动；
//   · 只修名字的模式把 .part 一起删了 → 用户本来只想让文件夹能打开，却顺手丢了东西。
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  ISSUE_LABELS, issueLabel, causesFolderError, summarizeIssues, pickFixable,
} from './libraryHealth.js'

test('issueLabel 覆盖后端所有分类，未知分类原样返回', () => {
  for (const kind of ['bad_utf8', 'control_char', 'illegal_char', 'trailing_dot', 'too_long',
    'zero_byte', 'leftover_part', 'no_permission', 'not_regular', 'broken_symlink', 'unreadable_dir']) {
    assert.ok(ISSUE_LABELS[kind], `${kind} 缺少中文名`)
    assert.notEqual(issueLabel(kind), kind)
  }
  assert.equal(issueLabel('whatever'), 'whatever')
  assert.equal(issueLabel(''), '未知问题')
})

test('只有名字/权限类问题才算「会让文件夹报错」', () => {
  assert.equal(causesFolderError('bad_utf8'), true)
  assert.equal(causesFolderError('control_char'), true)
  assert.equal(causesFolderError('illegal_char'), true)
  assert.equal(causesFolderError('unreadable_dir'), true)
  // 垃圾文件不影响目录能否打开
  assert.equal(causesFolderError('zero_byte'), false)
  assert.equal(causesFolderError('leftover_part'), false)
})

test('summarizeIssues 分别统计致命、可修与总数', () => {
  const issues = [
    { kind: 'bad_utf8', fix: 'rename' },
    { kind: 'illegal_char', fix: 'rename' },
    { kind: 'leftover_part', fix: 'delete' },
    { kind: 'not_regular', fix: 'manual' },
  ]
  const s = summarizeIssues(issues)
  assert.deepEqual(s, { total: 4, fatal: 2, fixable: 3 })
  assert.deepEqual(summarizeIssues(null), { total: 0, fatal: 0, fixable: 0 })
})

test('pickFixable 永远不会挑 manual，且 names 模式不删垃圾', () => {
  const issues = [
    { path: '/a', kind: 'bad_utf8', fix: 'rename' },
    { path: '/b', kind: 'no_permission', fix: 'chmod' },
    { path: '/c', kind: 'leftover_part', fix: 'delete' },
    { path: '/d', kind: 'not_regular', fix: 'manual' },
  ]

  const names = pickFixable(issues, 'names')
  assert.deepEqual(names.map(i => i.path), ['/a', '/b'])

  const all = pickFixable(issues, 'all')
  assert.deepEqual(all.map(i => i.path), ['/a', '/b', '/c'])

  // 默认模式是 names：误删用户文件是不可逆事故
  assert.deepEqual(pickFixable(issues).map(i => i.path), ['/a', '/b'])
})

test('fixHealth 送的是 id 而不是 path（非法 UTF-8 名字过不了 JSON 往返）', async () => {
  const calls = []
  const origFetch = globalThis.fetch
  globalThis.fetch = async (url, init) => {
    calls.push({ url, body: JSON.parse(init.body) })
    return {
      ok: true,
      status: 200,
      json: async () => ({ code: 200, fixed: 1, would_fix: 0, missed: 0, failed: [], results: [] }),
    }
  }
  try {
    const { fixHealth } = await import('./libraryHealth.js')
    const out = await fixHealth('/vol1/Music', [
      { id: 'AAA', path: '/vol1/Music/bad\ufffdsong.mp3', fix: 'rename' },
      { id: 'BBB', path: '/vol1/Music/ctrl.mp3', fix: 'rename' },
      { path: '/vol1/Music/no-id.mp3', fix: 'delete' }, // 没有 id 的条目不该混进去
    ])
    assert.equal(calls.length, 1)
    assert.deepEqual(calls[0].body.ids, ['AAA', 'BBB'])
    assert.equal(calls[0].body.dir, '/vol1/Music')
    // 关键：body 里不能出现 issues/path，否则后端又会去 stat 一个被替换过的名字
    assert.equal('issues' in calls[0].body, false)
    assert.equal(JSON.stringify(calls[0].body).includes('/vol1/Music/bad'), false)
    assert.equal(out.fixed, 1)
  } finally {
    globalThis.fetch = origFetch
  }
})

test('fixHealth 保留后端的 would_fix 与 missed', async () => {
  const origFetch = globalThis.fetch
  globalThis.fetch = async () => ({
    ok: true, status: 200,
    json: async () => ({ code: 200, fixed: 0, would_fix: 3, missed: 2, failed: [], results: [] }),
  })
  try {
    const { fixHealth } = await import('./libraryHealth.js')
    const out = await fixHealth('/x', [{ id: 'A', fix: 'rename' }], { dryRun: true })
    assert.equal(out.wouldFix, 3)
    assert.equal(out.missed, 2)
    assert.equal(out.fixed, 0)
  } finally {
    globalThis.fetch = origFetch
  }
})
