import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DEAD_STREAK, SWEEP_BUDGET, isDeadByHistory, sweepPlan } from './probePlan.js'

const fail = n => Array.from({ length: n }, (_, i) => ({ ok: false, ms: 0, t: i }))
const ok = n => Array.from({ length: n }, (_, i) => ({ ok: true, ms: 120, t: i }))
const src = (...ids) => ids.map(id => ({ id, name: id }))

test('isDeadByHistory：不足 DEAD_STREAK 条一律不算失效', () => {
  assert.equal(DEAD_STREAK, 3)
  assert.equal(isDeadByHistory({ a: [] }, 'a'), false)
  assert.equal(isDeadByHistory({ a: fail(1) }, 'a'), false)
  assert.equal(isDeadByHistory({ a: fail(2) }, 'a'), false)
  assert.equal(isDeadByHistory({}, 'a'), false)
  assert.equal(isDeadByHistory({}, ''), false)
})

test('isDeadByHistory：最近 DEAD_STREAK 次全失败才算', () => {
  assert.equal(isDeadByHistory({ a: fail(3) }, 'a'), true)
  assert.equal(isDeadByHistory({ a: fail(10) }, 'a'), true)
  // 最近 3 条里有一次成功 = 上游还活着，不能判死
  assert.equal(isDeadByHistory({ a: [...fail(2), { ok: true, ms: 90, t: 9 }] }, 'a'), false)
  // 只看最近 3 条：更早的失败不该拖累它
  assert.equal(isDeadByHistory({ a: [...fail(5), ...ok(1)] }, 'a'), false)
  // 最近的 3 条是失败，但之前是成功 —— 那是刚挂的，仍算熔断
  assert.equal(isDeadByHistory({ a: [...ok(2), ...fail(3)] }, 'a'), true)
})

test('sweepPlan：启用池内全部入选，池外只补「从没测过」的', () => {
  const plan = sweepPlan({
    sources: src('pool1', 'pool2', 'old1', 'new1'),
    activeIds: ['pool1', 'pool2'],
    history: { old1: ok(4) }, // 池外、有记录 → 跳过
    budget: 10,
  })
  assert.deepEqual(plan.ids, ['pool1', 'pool2', 'new1'])
  assert.equal(plan.total, 3)
  assert.equal(plan.skipped, 1)
})

test('sweepPlan：池外的已熔断源不再被后台敲（但池内的必须盯着）', () => {
  const plan = sweepPlan({
    sources: src('dead1', 'deadInPool'),
    activeIds: ['deadInPool'],
    history: { dead1: fail(3), deadInPool: fail(3) },
    budget: 10,
  })
  assert.deepEqual(plan.ids, ['deadInPool'])
})

test('sweepPlan：单轮受预算约束，游标轮转让下一轮接着走', () => {
  const all = Array.from({ length: 10 }, (_, i) => `s${i}`)
  const sources = src(...all)
  const first = sweepPlan({ sources, budget: 6 })
  assert.equal(first.ids.length, 6)
  assert.deepEqual(first.ids, all.slice(0, 6))
  assert.equal(first.nextCursor, 6)

  const second = sweepPlan({ sources, budget: 6, cursor: first.nextCursor })
  assert.deepEqual(second.ids, ['s6', 's7', 's8', 's9', 's0', 's1'])
  assert.equal(second.nextCursor, 2)

  const third = sweepPlan({ sources, budget: 6, cursor: second.nextCursor })
  assert.deepEqual(third.ids, ['s2', 's3', 's4', 's5', 's6', 's7'])
})

test('sweepPlan：连转多轮能覆盖全部候选，不重不漏', () => {
  const all = Array.from({ length: 7 }, (_, i) => `s${i}`)
  const seen = new Set()
  let cursor = 0
  for (let round = 0; round < 7; round++) {
    const p = sweepPlan({ sources: src(...all), budget: 1, cursor })
    p.ids.forEach(id => seen.add(id))
    cursor = p.nextCursor
  }
  assert.equal(seen.size, all.length)
})

test('sweepPlan：cursor 是按旧候选长度算的也不炸，取模后仍合法', () => {
  const p = sweepPlan({ sources: src('a', 'b'), budget: 1, cursor: 99 })
  assert.equal(p.ids.length, 1)
  assert.ok(['a', 'b'].includes(p.ids[0]))
  assert.ok(p.nextCursor >= 0 && p.nextCursor < 2)
  // 负数 / NaN / 小数都不该把它带出界
  for (const cursor of [-5, NaN, 2.7, undefined]) {
    const q = sweepPlan({ sources: src('a', 'b', 'c'), budget: 2, cursor })
    assert.equal(q.ids.length, 2)
    assert.ok(q.nextCursor >= 0 && q.nextCursor < 3)
  }
})

test('sweepPlan：没有候选时返回空计划', () => {
  const p = sweepPlan({ sources: src('a'), activeIds: [], history: { a: ok(1) } })
  assert.deepEqual(p.ids, [])
  assert.equal(p.nextCursor, 0)
  assert.equal(p.total, 0)
  assert.equal(p.skipped, 1)
  assert.deepEqual(sweepPlan({}).ids, [])
})

test('sweepPlan：脏数据（字符串 id、缺 id、空列表）不抛异常', () => {
  const p = sweepPlan({ sources: ['s1', { name: '无 id' }, null, 's1'], activeIds: null, history: null, budget: 5 })
  assert.deepEqual(p.ids, ['s1', 's1'])
  assert.deepEqual(sweepPlan({ sources: null, activeIds: undefined, history: undefined }).ids, [])
})

test('sweepPlan：预算至少为 1（0 / 负数也不该让后台整轮空转）', () => {
  assert.equal(sweepPlan({ sources: src('a', 'b'), budget: 0 }).ids.length, 1)
  assert.equal(sweepPlan({ sources: src('a', 'b'), budget: -3 }).ids.length, 1)
  assert.equal(SWEEP_BUDGET, 6)
})
