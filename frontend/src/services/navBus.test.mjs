/**
 * navBus 的跳转语义测试。
 *
 * 2026-10-24 加「对象 payload 带 segment」这条路径时补的：以前只有字符串形态能切分段，
 * 于是「带一次性数据跳分段」只能写两次调用、或者把数据塞进 localStorage 传 —— 后者是
 * 持久状态，用户下次从导航栏自己走进同一个页面就会读到过期值（真出现过：体检卡片扫错目录）。
 * 这里把两种形态、以及「订阅者拿到 payload」都钉住。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { currentView, segState, navigateTo, onNavigate, segmentOf, __resetNav } from './navBus.js'

test('字符串 payload 是分段 id：切视图同时也切分段', () => {
  __resetNav()
  navigateTo('download', 'queue')
  assert.equal(currentView.value, 'download')
  assert.equal(segmentOf('download'), 'queue')
})

test('对象 payload 的 segment 字段同样能切分段（新调用形态）', () => {
  __resetNav()
  navigateTo('tidy', { segment: 'audit', healthDir: '/vol1/Music' })
  assert.equal(currentView.value, 'tidy')
  assert.equal(segmentOf('tidy'), 'audit')
})

test('订阅者能拿到完整 payload（含一次性数据）', () => {
  __resetNav()
  const seen = []
  const off = onNavigate((view, payload) => seen.push([view, payload]))
  navigateTo('tidy', { segment: 'audit', healthDir: '/vol1/Music/A' })
  off()
  navigateTo('tidy', { segment: 'audit', healthDir: '/vol1/Music/B' })
  assert.deepEqual(seen, [['tidy', { segment: 'audit', healthDir: '/vol1/Music/A' }]])
})

test('认不出的 segment 不切分段，但仍切视图', () => {
  __resetNav()
  navigateTo('tidy', { segment: '不存在的段' })
  assert.equal(currentView.value, 'tidy')
  assert.notEqual(segmentOf('tidy'), '不存在的段')
})

test('单个订阅者抛错不影响其他订阅者与跳转本身', () => {
  __resetNav()
  const seen = []
  const off1 = onNavigate(() => { throw new Error('boom') })
  const off2 = onNavigate((view) => seen.push(view))
  navigateTo('sources')
  off1(); off2()
  assert.equal(currentView.value, 'sources')
  assert.deepEqual(seen, ['sources'])
})

test('空 view 直接被忽略', () => {
  __resetNav()
  navigateTo('', 'queue')
  assert.equal(currentView.value, 'search')
})
