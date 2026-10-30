// 钉住 services/sidebarBadges.js 的三档口径（2026-10-29 用户要求）
//
// 用户原话：「当用户有任务时,将订阅更新替换为出现的问题(精简文字),
//           当有多个时,采用:任务进行ing.」
// 这三档是本批需求的验收点，写得越死越好 —— 文案是用户直接看的，不能各自发挥。
import { test } from 'node:test'
import assert from 'node:assert/strict'

import { pushNavBadge, navLabelWithBadge } from './sidebarBadges.js'

test('pushNavBadge：没有任务时不显示，导航项保持原名「订阅更新」', () => {
  const badge = pushNavBadge({})
  assert.equal(badge.show, false)
  assert.equal(badge.label, '订阅更新')
  assert.equal(badge.count, 0)
  // 全 0 / 缺省 / 垃圾值都算「没任务」，不能因为 NaN 误报
  assert.equal(pushNavBadge({ pushRunning: 0, pending: 0, failed: 0, missing: 0, pendingBaseline: 0 }).show, false)
  assert.equal(pushNavBadge({ pushRunning: NaN, pending: null, failed: undefined }).show, false)
  // 负数（后端偶尔会给出 -1 表示未知）不能被当成任务
  assert.equal(pushNavBadge({ pending: -1, failed: -3 }).show, false)
})

test('pushNavBadge：只有一个任务时，显示「出现的问题」的精简文案', () => {
  // 单一问题 → 直接用它自己的文案，而不是笼统的「任务进行ing」
  const pendingOnly = pushNavBadge({ pending: 2 })
  assert.equal(pendingOnly.show, true)
  assert.equal(pendingOnly.label, '2 首待推送')
  assert.equal(pendingOnly.count, 2)
  assert.equal(pendingOnly.tone, 'warn')

  const failedOnly = pushNavBadge({ failed: 5 })
  assert.equal(failedOnly.show, true)
  assert.equal(failedOnly.label, '5 首推送失败')
  assert.equal(failedOnly.tone, 'error')

  const runningOnly = pushNavBadge({ pushRunning: 3 })
  assert.equal(runningOnly.label, '3 首推送中')
  assert.equal(runningOnly.tone, 'progress')

  const missingOnly = pushNavBadge({ missing: 7 })
  assert.equal(missingOnly.label, '7 首缺源')
  assert.equal(missingOnly.tone, 'muted')

  const baselineOnly = pushNavBadge({ pendingBaseline: 1 })
  assert.equal(baselineOnly.label, '1 个未追新')
  assert.equal(baselineOnly.tone, 'warn')
})

test('pushNavBadge：多个任务收敛成「任务进行ing」，count 是总数', () => {
  const badge = pushNavBadge({ pushRunning: 2, pending: 3 })
  assert.equal(badge.show, true)
  assert.equal(badge.label, '任务进行ing')
  assert.equal(badge.count, 5)          // 2 + 3
  assert.equal(badge.tone, 'progress')  // 没有失败 → 绿
  // 悬停说明里要能看到明细，否则用户不知道「进行的是什么事」
  assert.match(badge.title, /2 首推送中/)
  assert.match(badge.title, /3 首待推送/)
})

test('pushNavBadge：有失败时多个任务也染红（失败比「进行中」更该被看见）', () => {
  const badge = pushNavBadge({ pushRunning: 1, pending: 2, failed: 1 })
  assert.equal(badge.label, '任务进行ing')
  assert.equal(badge.tone, 'error')
  assert.equal(badge.count, 4) // 1 + 2 + 1
})

test('pushNavBadge：五种都能同时出现时仍只给一句话，不罗列', () => {
  const badge = pushNavBadge({ pushRunning: 1, pending: 1, failed: 1, missing: 1, pendingBaseline: 1 })
  assert.equal(badge.label, '任务进行ing')
  assert.equal(badge.count, 5)
  // 侧栏只有 216px（收缩后 56px），label 必须短；超过 8 个字符就把侧栏撑开了
  assert.ok(badge.label.length <= 8, `label 太长：${badge.label}`)
  // 五个问题在 title 里都要有
  assert.equal(badge.title.split(' · ').length, 5)
})

test('navLabelWithBadge：只有「订阅更新」换成问题文案，其他导航项不受影响', () => {
  const badge = pushNavBadge({ pending: 2 })
  assert.equal(navLabelWithBadge('push', '订阅更新', badge), '2 首待推送')
  // 其他项哪怕传了 badge 也不换 —— 徽标是订阅专属的
  assert.equal(navLabelWithBadge('download', '下载', badge), '下载')
  assert.equal(navLabelWithBadge('nas', '本地曲库', badge), '本地曲库')
  // 没任务（show=false）时保持原名
  assert.equal(navLabelWithBadge('push', '订阅更新', pushNavBadge({})), '订阅更新')
  // badge 缺省 / null 也要安全（模板首帧 badge 可能还没算出来）
  assert.equal(navLabelWithBadge('push', '订阅更新', null), '订阅更新')
  assert.equal(navLabelWithBadge('push', '订阅更新', undefined), '订阅更新')
})
