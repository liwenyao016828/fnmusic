/* 「停止全部」要停住生产者（订阅自动补下载 / 补下载 / 排空后自动推送）的回归钉子。
 *
 * # 用户报障（2026-11-03）
 *
 * 「点了停止全部，当前这首停下来了，但下一首还是继续推送下载。」
 *
 * 根因不在后端队列（后端空 ids 的整队 stop 早已原子停掉 pending/downloading/paused，
 * 见 backend/pkg/backenddl/queue.go 的 Apply 与 queue_stopall_test.go），
 * 而在**谁在往队列里放歌**：
 *   · `enqueueSongs` 逐首串行搜索入队，几十首一两分钟 —— 以前只认「订阅行被停」；
 *   · `autoFillPending` 每 8 秒一批（批与批之间队列恰好是空的）—— 以前完全不看停止；
 *   · `watchQueueDrain` / `watchFillCompletion` 的 3 秒定时器与 `autoPushRow` 的
 *     90 秒等待 —— 以前点了停止也照样跑完。
 *
 * 这里钉两件事：
 *   1. 闸门的语义（纯函数，能真跑）；
 *   2. 接线：`downloadManager.stopAll()` 必须关闸，`pushEngine` 的生产者路径
 *      必须**全部**走闸门判定（源码约定测试，项目没有 DOM 环境，见
 *      downloadManager.init.test.mjs 的先例）。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createProducerGate, createFillBatch, producerMustStop, waitUnlessStopped } from './producerGate.js'

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel) => readFileSync(join(here, rel), 'utf8')

const downloadManagerJs = read('./downloadManager.js')
const pushEngineJs = read('./pushEngine.js')

// ── 1. 闸门语义 ──

test('闸门：默认开，关闭后带原因，再开即恢复', () => {
  const gate = createProducerGate()
  assert.equal(gate.isClosed(), false)
  assert.equal(gate.reason(), '')
  gate.close('stop_all')
  assert.equal(gate.isClosed(), true)
  assert.equal(gate.reason(), 'stop_all')
  gate.open()
  assert.equal(gate.isClosed(), false)
  assert.equal(gate.reason(), '')
})

test('producerMustStop：队列停止 / 行停止 → 必须停；整队暂停只拦自动批次', () => {
  assert.equal(producerMustStop({}), false)
  assert.equal(producerMustStop({ queueStopped: true }), true, '停止全部必须先拦下来')
  assert.equal(producerMustStop({ rowStopped: true }), true, '订阅行停止仍然生效（不回归）')
  assert.equal(producerMustStop({ queueStopped: true, manual: true }), true, '手点补下载也不能绕过停止全部')
  // 暂停不是停止：自动批次不再往队列里塞，但用户手点「补下载」应该照常
  assert.equal(producerMustStop({ queuePaused: true }), true)
  assert.equal(producerMustStop({ queuePaused: true, manual: true }), false)
})

test('批次令牌：逐首搜索每一轮都重新问停止（停止能插进串行循环中间）', async () => {
  let stopped = false
  const batch = createFillBatch({ stopped: () => stopped })
  const enqueued = []
  for (let i = 0; i < 5; i++) {
    if (batch.stopped()) break
    enqueued.push(i)
    if (i === 1) stopped = true // 用户在第 2 首之后点了停止全部
  }
  assert.deepEqual(enqueued, [0, 1], '停止之后不能再入队第 3、4、5 首')
})

test('waitUnlessStopped：等待可被停止打断，未被停则等满', async () => {
  // 假定时器：把排队中的回调按「推进一轮」逐个执行（不依赖真实时钟）
  const makeClock = () => {
    const queue = []
    return {
      setTimeoutFn: (fn) => {
        queue.push(fn)
        return queue.length
      },
      async advance() {
        const fn = queue.shift()
        if (!fn) return false
        fn()
        await new Promise((r) => setImmediate(r))
        return true
      },
      pending: () => queue.length,
    }
  }

  // ① 第 3 跳之后用户点了停止全部 → 必须提前结束（false），且不再继续排定时器
  {
    const clock = makeClock()
    let stopped = false
    let hops = 0
    const p = waitUnlessStopped(90000, { stopped: () => stopped, stepMs: 1000, setTimeoutFn: clock.setTimeoutFn })
    while (clock.pending()) {
      hops++
      if (hops === 3) stopped = true
      await clock.advance()
    }
    assert.equal(await p, false, '90 秒等待中点了停止 → 必须提前结束')
    assert.ok(hops <= 4, `停止后不应继续跳：实际 ${hops} 跳`)
  }

  // ② 没人点停止 → 等满（true），跳数 = ms/stepMs
  {
    const clock = makeClock()
    const p = waitUnlessStopped(3000, { stopped: () => false, stepMs: 1000, setTimeoutFn: clock.setTimeoutFn })
    let hops = 0
    while (clock.pending()) {
      hops++
      await clock.advance()
    }
    assert.equal(await p, true, '没点停止 → 等满')
    assert.equal(hops, 3, '3000ms / 1000ms 应恰好 3 跳')
  }

  // ③ 进入等待时已经被停 → 一次都不跳
  {
    const clock = makeClock()
    assert.equal(await waitUnlessStopped(90000, { stopped: () => true, setTimeoutFn: clock.setTimeoutFn }), false)
    assert.equal(clock.pending(), 0, '已经停止就不该再排定时器')
  }
})

// ── 2. 接线约定（源码） ──

test('停止全部：downloadManager.stopAll 关生产者闸门并收掉定时器，再发后端空 ids 整队停止', () => {
  const m = downloadManagerJs.match(/async function stopAll\(\) \{[\s\S]*?\n\}/)
  assert.ok(m, '找不到 stopAll 实现')
  const body = m[0]
  assert.match(body, /producerGate\.close\(/, 'stopAll 必须关生产者闸门（否则下一批继续入队）')
  assert.match(body, /cancelProducers\(\)/, 'stopAll 必须收掉生产者侧的排空/回写定时器')
  assert.match(body, /backendDownload\.act\('stop', \[\]\)/, '后端仍要收到空 ids 的整队停止（上一轮修复不能回退）')
  assert.ok(
    body.indexOf('producerGate.close(') < body.indexOf("backendDownload.act('stop', [])"),
    '先关闸再发请求：请求在途时不能还有生产者往队列里塞',
  )
})

test('用户主动继续/重试/重下会开闸，否则停止全部会把订阅永久锁死', () => {
  for (const [name, re] of [
    ['resumeStopped', /async function resumeStopped\(\) \{[\s\S]*?reopenProducers\(\)/],
    ['retryAllFailed', /async function retryAllFailed\(\) \{[\s\S]*?reopenProducers\(\)/],
    ['redownloadTasksByIds', /async function redownloadTasksByIds\(ids\) \{[\s\S]*?reopenProducers\(\)/],
  ]) {
    assert.match(downloadManagerJs, re, `${name} 必须调用 reopenProducers()`)
  }
})

test('pushEngine 的生产者路径全部走 producerStopped / producerMustStop', () => {
  // 逐首搜索的两处检查（每首一次、每个平台一次）
  const enqueue = pushEngineJs.match(/async function enqueueSongs\([\s\S]*?\n\}\n/)
  assert.ok(enqueue, '找不到 enqueueSongs')
  const guards = enqueue[0].match(/if \(producerStopped\(rowKey\)\) break/g) || []
  assert.equal(guards.length, 2, `enqueueSongs 里应有两处 producerStopped 检查，实际 ${guards.length}`)
  assert.ok(!/isRowStopped\(rowKey\)/.test(enqueue[0]), 'enqueueSongs 不能只认行停止（会漏掉停止全部）')

  // 批次 ticker 与排空后推送
  assert.match(pushEngineJs, /async function autoFillPending\(\)[\s\S]*?producerMustStop\(\{[\s\S]*?queueStopped: downloadManager\.producerStopped\(\)/,
    'autoFillPending（每 8 秒的批次 ticker）必须先看队列停止与整队暂停')
  assert.match(pushEngineJs, /async function autoPushRow\(row\)[\s\S]*?producerStopped\(row\.key\)/,
    'autoPushRow 必须看闸门')
  assert.match(pushEngineJs, /waitUnlessStopped\(AUTO_PUSH_SETTLE_MS/,
    'autoPushRow 的 90 秒等待必须可被停止打断')

  // 定时器要能被收掉
  assert.match(pushEngineJs, /stopWatchers\.add\(timer\)/g, 'watchFillCompletion / watchQueueDrain 的定时器要登记')
  assert.match(pushEngineJs, /function clearFillTimers\(\)/, '要有 clearFillTimers 供停止全部调用')
  assert.match(pushEngineJs, /clearFillTimers,/, 'clearFillTimers 要从 pushEngine 导出')
})

test('producerStopped 一处实现两处来源（行停止 + 队列停止）', () => {
  const m = pushEngineJs.match(/function producerStopped\(rowKey\) \{\n\s+return (.*)\n\}/)
  assert.ok(m, '找不到 producerStopped')
  assert.match(m[1], /isRowStopped\(rowKey\)/)
  assert.match(m[1], /downloadManager\.producerStopped\(\)/)
})
