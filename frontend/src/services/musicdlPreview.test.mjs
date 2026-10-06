import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  PREVIEW_DEFAULT_TTL,
  formatSecondsLeft,
  previewData,
  previewDeadline,
  previewNeedsPolling,
  previewPhase,
  previewStatusText,
  secondsUntil,
  shouldProbePreview,
} from './musicdlPreview.js'

const T0 = 1_700_000_000_000

test('PREVIEW_DEFAULT_TTL 与后端 5 分钟一致', () => {
  assert.equal(PREVIEW_DEFAULT_TTL, 300)
})

test('formatSecondsLeft：不足一分钟说秒，超过说 m:ss，负数当 0', () => {
  assert.equal(formatSecondsLeft(45), '45 秒')
  assert.equal(formatSecondsLeft(0), '0 秒')
  assert.equal(formatSecondsLeft(60), '1:00')
  assert.equal(formatSecondsLeft(272), '4:32')
  assert.equal(formatSecondsLeft(300), '5:00')
  // 服务端快照 + 本地时钟偏差可能算出负数 —— 绝不能显示「-3 秒」
  assert.equal(formatSecondsLeft(-3), '0 秒')
  assert.equal(formatSecondsLeft(null), '0 秒')
  assert.equal(formatSecondsLeft('abc'), '0 秒')
})

test('previewData：兼容包一层与不包一层', () => {
  assert.deepEqual(previewData({ code: 200, data: { active: true } }), { active: true })
  assert.deepEqual(previewData({ active: false }), { active: false })
  assert.equal(previewData(null), null)
  assert.equal(previewData(undefined), null)
})

test('previewPhase：没起来的一律不算 ready', () => {
  assert.equal(previewPhase(null), 'idle')
  assert.equal(previewPhase({}), 'idle')
  assert.equal(previewPhase({ active: false, state: 'ready' }), 'idle')
  assert.equal(previewPhase({ active: true, state: 'ready' }), 'ready')
  assert.equal(previewPhase({ active: true, state: 'preparing' }), 'starting')
  assert.equal(previewPhase({ active: true, state: 'starting' }), 'starting')
  assert.equal(previewPhase({ active: true, state: 'off' }), 'starting')
  // 只有 failed 是失败；状态缺失也只当「还在起」，不当成坏源
  assert.equal(previewPhase({ active: true, state: 'failed' }), 'failed')
  assert.equal(previewPhase({ active: true }), 'starting')
  assert.equal(previewPhase({ active: true, sidecar: { state: 'ready' } }), 'ready')
})

test('previewDeadline + secondsUntil：轮询之间不会重复计时、也不会为负', () => {
  const pv = { active: true, seconds_left: 300 }
  const deadline = previewDeadline(pv, T0)
  assert.equal(deadline, T0 + 300_000)
  // 同一份快照算两次，结果一样（不能被「又打了一次接口」重新计时）
  assert.equal(previewDeadline(pv, T0), deadline)
  // 10 秒后
  assert.equal(secondsUntil(deadline, T0 + 10_000), 290)
  // 到期与过期都不为负
  assert.equal(secondsUntil(deadline, deadline), 0)
  assert.equal(secondsUntil(deadline, deadline + 60_000), 0)
  // 没在试运行 → 没有截止时刻
  assert.equal(previewDeadline({ active: false, seconds_left: 300 }, T0), 0)
  assert.equal(secondsUntil(0, T0), 0)
})

test('shouldProbePreview：只有进程就绪才该探源', () => {
  // 「还没起来」探出来的是「源不能用」—— 正是这个页面最不该给错的结论
  assert.equal(shouldProbePreview({ active: true, state: 'preparing' }), false)
  assert.equal(shouldProbePreview({ active: true, state: 'failed' }), false)
  assert.equal(shouldProbePreview({ active: false }), false)
  assert.equal(shouldProbePreview({ active: true, state: 'ready' }), true)
})

test('previewNeedsPolling：服务端说在试就一直问，说回收了就停', () => {
  // 本地倒计时数到 0 也还要问 —— 回收发生在服务端，那一下正是该问的时刻
  assert.equal(previewNeedsPolling({ active: true, state: 'ready', seconds_left: 0 }), true)
  assert.equal(previewNeedsPolling({ active: true, state: 'preparing', seconds_left: 300 }), true)
  assert.equal(previewNeedsPolling({ active: true, state: 'failed' }), true)
  // 服务端已经报回收 → 不用再问了
  assert.equal(previewNeedsPolling({ active: false }), false)
  assert.equal(previewNeedsPolling(null), false)
})

test('previewStatusText：三种阶段的文案都把「会自动回收」说清楚', () => {
  const starting = previewStatusText({ active: true, state: 'preparing', source: 'bq', seconds_left: 250 }, '千千音乐')
  assert.match(starting, /正在拉起 千千音乐/)
  assert.match(starting, /自动回收/)

  const ready = previewStatusText({ active: true, state: 'ready', source: 'bq', seconds_left: 250 }, '千千音乐')
  assert.match(ready, /试运行 千千音乐/)
  assert.match(ready, /4:10/)
  // 这句是用户最需要知道的：试运行不生效
  assert.match(ready, /不写配置、不影响搜索/)

  const failed = previewStatusText(
    { active: true, state: 'failed', source: 'bq', sidecar: { error: '没找到 sidecar 源码目录' } },
    '千千音乐',
  )
  assert.match(failed, /启动失败：没找到 sidecar 源码目录/)

  // 没在试运行 → 不占一行
  assert.equal(previewStatusText(null, '千千音乐'), '')
  assert.equal(previewStatusText({ active: false }, '千千音乐'), '')
})
