import test from 'node:test'
import assert from 'node:assert/strict'
import { submitSubscriptionBatch } from './subscriptionBatch.js'

const row = (key) => ({ key, download: true, fnos: false })
function fixture(rows, runRow, overrides = {}) {
  const results = []
  return {
    results,
    options: {
      keys: rows.map(r => r.key),
      findRow: key => rows.find(r => r.key === key),
      runRow,
      shouldStop: () => false,
      onResult: (r, res) => results.push([r.key, res]),
      ...overrides,
    },
  }
}

test('批量提交严格串行，单条抛错或返回失败仍继续', async () => {
  const calls = []
  let active = 0
  const f = fixture(['a', 'b', 'c', 'd'].map(row), async r => {
    assert.equal(active++, 0)
    calls.push(r.key)
    await Promise.resolve()
    active--
    if (r.key === 'b') throw new Error('提交失败')
    return { ok: r.key !== 'c' }
  })
  await submitSubscriptionBatch(f.options)
  assert.deepEqual(calls, ['a', 'b', 'c', 'd'])
  assert.deepEqual(f.results.map(([, r]) => r.ok), [true, false, false, true])
  assert.equal(f.results[1][1].error, '提交失败')
})

test('停止等待当前请求结束，不再提交下一条', async () => {
  let stop = false
  let release
  let entered
  const started = new Promise(resolve => { entered = resolve })
  const current = new Promise(resolve => { release = resolve })
  const calls = []
  const f = fixture([row('a'), row('b')], async r => {
    calls.push(r.key)
    entered()
    await current
    return { ok: true }
  }, { shouldStop: () => stop })
  const running = submitSubscriptionBatch(f.options)
  await started
  stop = true
  assert.equal(f.results.length, 0)
  release()
  await running
  assert.deepEqual(calls, ['a'])
  assert.equal(f.results.length, 1)
})

test('每条提交前重取状态，跳过删除及关闭的订阅', async () => {
  const rows = [row('a'), row('b'), row('c')]
  const calls = []
  const f = fixture(rows, async r => {
    calls.push(r.key)
    rows.splice(1, 1)
    rows[1].download = false
    return { ok: true }
  })
  await submitSubscriptionBatch(f.options)
  assert.deepEqual(calls, ['a'])
})

test('没有返回确认不能计为成功', async () => {
  const f = fixture([row('a')], async () => undefined)
  await submitSubscriptionBatch(f.options)
  assert.deepEqual(f.results, [['a', { ok: false, error: '未收到执行确认' }]])
})
