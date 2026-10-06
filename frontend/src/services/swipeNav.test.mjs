import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  lockAxis, rubberBand, decideCommit, velocityFrom, settleDuration,
  isSwipeBlocked, resolveSwipeTarget,
  springAt, springSettled, springOmegaFor, clampVelocity,
  SPRING_ZETA_TURN, SPRING_ZETA_BACK, SPRING_MAX_V, SPRING_MAX_MS,
  AXIS_LOCK_PX, COMMIT_RATIO, COMMIT_VELOCITY, RUBBER_FACTOR,
  SETTLE_MIN_MS, SETTLE_MAX_MS,
} from './swipeNav.js'

/** 跑一遍弹簧直到停住，返回 { settleMs, overshootPx }（overshoot = 越过目标的最大距离） */
function runSpring({ x, v = 0, target = 0, ms = 300, zeta }) {
  const s = { x, v, target, omega: springOmegaFor(ms), zeta }
  let settleMs = null, overshoot = 0
  const dir = Math.sign(x - target) || 1
  for (let t = 0; t <= SPRING_MAX_MS; t += 8) {
    const st = springAt(s, t)
    if (Math.sign(st.x - target) === -dir && Math.abs(st.x - target) > overshoot) overshoot = Math.abs(st.x - target)
    if (settleMs === null && springSettled(st)) { settleMs = t; break }
  }
  return { settleMs, overshoot: +overshoot.toFixed(2) }
}

const ORDER = ['search', 'nas', 'library', 'accounts', 'logs']

test('轴向锁定：还没动够 → null；横向大 → x；竖向大 → y', () => {
  assert.equal(lockAxis(3, 2), null)
  assert.equal(lockAxis(AXIS_LOCK_PX + 2, 1), 'x')
  assert.equal(lockAxis(1, AXIS_LOCK_PX + 2), 'y')
  assert.equal(lockAxis(-40, 30), 'x')
})

test('到头阻尼：能去的方向不阻尼，到头方向只拉动 35%', () => {
  assert.equal(rubberBand(-100, { canPrev: true, canNext: true }), -100)
  assert.equal(rubberBand(-100, { canPrev: true, canNext: false }), -100 * RUBBER_FACTOR)
  assert.equal(rubberBand(100, { canPrev: false, canNext: true }), 100 * RUBBER_FACTOR)
})

test('拖够比例就翻页', () => {
  assert.equal(decideCommit({ dx: -400, width: 1000, velocity: 0 }), 'next')
  assert.equal(decideCommit({ dx: 400, width: 1000, velocity: 0 }), 'prev')
})

test('没拖够但甩得快也翻（手感关键）', () => {
  assert.equal(decideCommit({ dx: -120, width: 1000, velocity: -0.6 }), 'next')
  assert.equal(decideCommit({ dx: 120, width: 1000, velocity: 0.6 }), 'prev')
})

test('没拖够也没甩 → 弹回', () => {
  assert.equal(decideCommit({ dx: -COMMIT_RATIO * 1000 + 1, width: 1000, velocity: -0.1 }), null)
  assert.equal(decideCommit({ dx: -200, width: 1000, velocity: COMMIT_VELOCITY - 0.01 }), null)
})

test('到头方向即便甩得再快也不翻', () => {
  assert.equal(decideCommit({ dx: -900, width: 1000, velocity: -2, canNext: false }), null)
  assert.equal(decideCommit({ dx: 900, width: 1000, velocity: 2, canPrev: false }), null)
})

test('宽度为 0（还没量到）直接不翻', () => {
  assert.equal(decideCommit({ dx: -400, width: 0, velocity: -1 }), null)
})

test('速度：取最近一个 ≥40ms 的采样点算；松手前停顿过就作废', () => {
  const samples = [{ x: 0, t: 100 }, { x: -60, t: 160 }, { x: -140, t: 220 }]
  // 最后一个点是 t=220，往回找第一个「至少早 40ms」的点 = t=160 → dt=60，(‑140‑(‑60))/60
  assert.ok(Math.abs(velocityFrom(samples, 220) - (-80 / 60)) < 1e-6)
  assert.equal(velocityFrom(samples, 900), 0) // 停顿太久，不算"甩"
  assert.equal(velocityFrom([{ x: 0, t: 1 }], 2), 0)
  assert.equal(velocityFrom(null, 2), 0)
})

test('isSwipeBlocked：输入类、range、横向滚动、播放器、显式 no-drag 都阻止', () => {
  for (const d of [
    { tag: 'input' }, { tag: 'textarea' }, { tag: 'select' },
    { tag: 'input', type: 'range' }, { tag: 'div', editable: true },
    { tag: 'div', canScrollX: true }, { tag: 'div', inPlayer: true },
    { tag: 'div', inPlayer: true }, { tag: 'div', noDrag: true },
  ]) assert.equal(isSwipeBlocked(d), true, JSON.stringify(d))
})

test('isSwipeBlocked：普通容器不阻止', () => {
  for (const d of [{ tag: 'div', overflowX: 'visible' }, { tag: 'div', overflowX: 'hidden' },
    // ⚠️ 曾经的 bug：竖向滚动容器的 overflowX 计算值也是 auto，不能因此拦手势
    { tag: 'div', overflowX: 'auto' }, { tag: 'div', overflowX: 'scroll', canScrollX: false }, {}, null]) {
    assert.equal(isSwipeBlocked(d), false, JSON.stringify(d))
  }
})

test('resolveSwipeTarget：正常前进/后退、到头停住、非导航视图退回首尾、空数组不炸', () => {
  assert.equal(resolveSwipeTarget(ORDER, 'nas', 'next'), 'library')
  assert.equal(resolveSwipeTarget(ORDER, 'nas', 'prev'), 'search')
  assert.equal(resolveSwipeTarget(ORDER, 'logs', 'next'), null)
  assert.equal(resolveSwipeTarget(ORDER, 'search', 'prev'), null)
  assert.equal(resolveSwipeTarget(ORDER, 'sources', 'next'), 'search')
  assert.equal(resolveSwipeTarget(ORDER, 'sources', 'prev'), 'logs')
  assert.equal(resolveSwipeTarget([], 'search', 'next'), null)
  assert.equal(resolveSwipeTarget(null, 'search', 'next'), null)
})

test('松手动画时长跟速度走：甩得越快收得越快', () => {
  const slow = settleDuration(600, 0.4)
  const fast = settleDuration(600, 2)
  assert.ok(slow > fast, `${slow} 应大于 ${fast}`)
})

test('松手动画时长有上下限，不会秒切也不会拖沓', () => {
  assert.equal(settleDuration(20, 5), SETTLE_MIN_MS)
  assert.equal(settleDuration(2000, 0.05), SETTLE_MAX_MS)
  assert.ok(settleDuration(200, 1) >= SETTLE_MIN_MS && settleDuration(200, 1) <= SETTLE_MAX_MS)
})

test('移动端参数就该是"手指友好"的一组（阈值比桌面拖拽小）', () => {
  assert.ok(AXIS_LOCK_PX <= 10, '轴向锁定要够小，手指抖 8~10px')
  assert.ok(COMMIT_RATIO <= 1 / 3, '手机够不到 1/3，比例要更小')
  assert.ok(COMMIT_VELOCITY <= 0.35, '手机上短促一甩就该翻页')
  assert.ok(RUBBER_FACTOR <= 0.5, '到头阻尼别太硬')
})

/* ── 松手后的阻尼弹簧（2026-09-30 加「滑动结束后的阻尼感」） ── */

test('阻尼弹簧：t=0 就是松手那一刻的位置与速度', () => {
  const s = { x: -120, v: -0.8, target: 0, omega: springOmegaFor(300), zeta: SPRING_ZETA_BACK }
  const st = springAt(s, 0)
  assert.equal(st.x, -120)
  assert.ok(Math.abs(st.v - (-0.8)) < 1e-9, `v(0) 应为 -0.8，实测 ${st.v}`)
})

test('阻尼弹簧：翻页用临界阻尼，**绝不越过目标**（回弹会让邻页露一条边，像 bug）', () => {
  const r = runSpring({ x: -390, ms: 350, zeta: SPRING_ZETA_TURN })
  assert.equal(r.overshoot, 0)
  assert.ok(r.settleMs !== null, '必须在兜底时长内停住')
})

test('阻尼弹簧：弹回用欠阻尼，会越过原位一点再回来 —— 这就是"阻尼感"', () => {
  const r = runSpring({ x: -100, ms: 220, zeta: SPRING_ZETA_BACK })
  assert.ok(r.overshoot > 1, `应该有可见的回弹，实测 ${r.overshoot}px`)
  assert.ok(r.overshoot < 15, `回弹要克制，实测 ${r.overshoot}px`)
  assert.ok(r.settleMs !== null, '回弹后必须停住')
})

test('阻尼弹簧：位移越大回弹越大（比例感），但都在兜底时长内收住', () => {
  const small = runSpring({ x: -40, ms: 220, zeta: SPRING_ZETA_BACK })
  const big = runSpring({ x: -200, ms: 220, zeta: SPRING_ZETA_BACK })
  assert.ok(big.overshoot > small.overshoot)
  for (const r of [small, big]) assert.ok(r.settleMs !== null && r.settleMs <= SPRING_MAX_MS)
})

test('阻尼弹簧：松手速度会带进运动里 —— 顺着劲儿甩比静止松手更早到位', () => {
  // 翻页的目标是 -width（往左翻），所以"顺着劲儿"的速度是负的
  const still = runSpring({ x: -100, v: 0, target: -390, ms: 350, zeta: SPRING_ZETA_TURN })
  const flick = runSpring({ x: -100, v: -1.5, target: -390, ms: 350, zeta: SPRING_ZETA_TURN })
  assert.ok(flick.settleMs < still.settleMs, `${flick.settleMs} 应小于 ${still.settleMs}`)
  assert.equal(still.overshoot, 0)
  assert.equal(flick.overshoot, 0, '带速度也不该冲过目标页')
})

test('阻尼弹簧：解析解是纯函数 —— 同一 t 的值与调用顺序/帧率无关', () => {
  const s = { x: -100, v: 0, target: 0, omega: springOmegaFor(220), zeta: SPRING_ZETA_BACK }
  const first = springAt(s, 96).x
  springAt(s, 500)   // 先乱调几个别的时刻
  springAt(s, 1000)
  assert.equal(springAt(s, 96).x, first, '同一个 t 必须给出同一个值（数值积分会挂在这条上）')
  assert.ok(first > 0, '此刻应当已经越过原位（欠阻尼回弹）')
  assert.ok(first < 15, '回弹幅度要克制')
})

test('落位时长 → 弹簧软硬：时长越短越硬，且不会除出 Infinity', () => {
  assert.ok(springOmegaFor(220) > springOmegaFor(420))
  assert.ok(Number.isFinite(springOmegaFor(0)) && springOmegaFor(0) > 0)
})

test('松手速度限幅：超范围夹住，NaN 当 0（别让弹簧冲过头）', () => {
  assert.equal(clampVelocity(-9), -SPRING_MAX_V)
  assert.equal(clampVelocity(9), SPRING_MAX_V)
  assert.equal(clampVelocity(0.7), 0.7)
  assert.equal(clampVelocity(NaN), 0)
  assert.equal(clampVelocity(undefined), 0)
})
