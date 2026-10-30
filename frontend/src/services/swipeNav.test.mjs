import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  lockAxis, axisYields, rubberBand, decideCommit, velocityFrom, settleDuration,
  isSwipeBlocked, resolveSwipeTarget, segmentOnEnter,
  flattenRail, railIndexOf, splitRail, railNeighbors,
  springAt, springSettled, springOmegaFor, clampVelocity,
  SPRING_ZETA_TURN, SPRING_ZETA_BACK, SPRING_MAX_V, SPRING_MAX_MS,
  AXIS_LOCK_PX, AXIS_YIELD_RATIO, COMMIT_RATIO, COMMIT_VELOCITY, RUBBER_FACTOR,
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

test('锁定后复查：横向手势不因纵向抖动让步，真竖着滚才交还', () => {
  // 横着划、手指上下抖 12px（常见）→ 仍然是横滑，不能丢掉这一划
  assert.equal(axisYields('x', -60, 12), false)
  // 横向只挪了 10px 就开始竖着滚（纵向 40px）→ 交还给页面滚动
  assert.equal(axisYields('x', -10, 40), true)
  // 正好卡在比例边界：刚好 1.2 倍不放，超过才放
  assert.equal(axisYields('x', -10, 10 * AXIS_YIELD_RATIO), false)
  assert.equal(axisYields('x', -10, 10 * AXIS_YIELD_RATIO + 1), true)
  // 竖向手势反过来同理：竖着滚时横向摆动不该抢成翻页
  assert.equal(axisYields('y', 15, 200), false)
  assert.equal(axisYields('y', 200, 15), true)
  // 未锁定 / 非法值：不放手
  assert.equal(axisYields(null, 100, 100), false)
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

/* ── 落位时长收紧（2026-10-28 用户：「滑动切换也有点卡」） ──
   落位动画越长，合成器要持续出力的时间越长；手机上手感也发"黏"。
   iOS 自己的分页落位大约 180~320ms，这里对齐到 160~320ms 并锁住，免得以后被改回去。 */
test('落位时长收紧到 160~320ms：更快收住 = 更少掉帧、更跟手', () => {
  assert.equal(SETTLE_MIN_MS, 160)
  assert.equal(SETTLE_MAX_MS, 320)
  assert.ok(SETTLE_MIN_MS < SETTLE_MAX_MS)
  // 极端输入也守在区间内
  assert.equal(settleDuration(0, 0), SETTLE_MIN_MS)
  assert.equal(settleDuration(1e9, 0.0001), SETTLE_MAX_MS)
})

/* ── 滑动进入带子导航的页（2026-10-29 用户：「滑到其他页面、然后跳到子导航」） ──
   滑动是平推，不该像点按那样把分段重置成默认段；看过就沿用上次停留的那段。 */
const SEGS = ['discover', 'charts']

test('滑动进入没看过的页 → 落在默认段（与点按一致）', () => {
  assert.equal(segmentOnEnter(SEGS, { hasVisited: false, defaultSegment: 'discover' }), 'discover')
})

test('滑动进入看过的页 → 沿用上次停留的段，不跳回第一段', () => {
  assert.equal(
    segmentOnEnter(SEGS, { remembered: 'charts', hasVisited: true, defaultSegment: 'discover' }),
    'charts',
  )
})

test('记着的段已被删掉 → 退回默认段，而不是返回一个不存在的 id', () => {
  assert.equal(
    segmentOnEnter(SEGS, { remembered: 'gone', hasVisited: true, defaultSegment: 'discover' }),
    'discover',
  )
})

test('默认段本身失效 → 落第一段（宁可显示第一段，也不要空页）', () => {
  assert.equal(segmentOnEnter(SEGS, { defaultSegment: 'nope' }), 'discover')
  assert.equal(segmentOnEnter(SEGS, {}), 'discover')
})

test('无分段的页 / 空输入 → null（调用方保持现状，不做无谓重置）', () => {
  assert.equal(segmentOnEnter([], { hasVisited: true, remembered: 'x' }), null)
  assert.equal(segmentOnEnter(undefined, {}), null)
})

/* ── 统一滑轨（2026-10-29 用户：「导航滑动要包括页面中的小导航」） ──
   例：曲库 → 补全 → 修复 → 音质 → 快照 → 订阅。 */
const RAIL_ORDER = ['search', 'library', 'tidy', 'push', 'download', 'sources']
const RAIL_SEGS = {
  search: ['discover', 'charts'],
  tidy: ['complete', 'repair', 'quality', 'records'],
  download: ['queue', 'playlist', 'autodl'],
}

test('扁平链：分段页按分段展开，无分段页占一格', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  assert.deepEqual(rail.map((it) => it.key), [
    'search:discover', 'search:charts',
    'library:',
    'tidy:complete', 'tidy:repair', 'tidy:quality', 'tidy:records',
    'push:',
    'download:queue', 'download:playlist', 'download:autodl',
    'sources:',
  ])
  assert.deepEqual(flattenRail([], RAIL_SEGS), [])
  assert.deepEqual(flattenRail(null, null), [])
})

test('用户举例的那一条链：曲库 → 补全 → 修复 → 音质 → 快照 → 订阅，一路只往左滑', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  let i = railIndexOf(rail, 'library')
  const path = []
  for (let n = 0; n < 5; n++) {
    i = railIndexOf(rail, rail[i].view, rail[i].segment)
    const nx = railNeighbors(rail, i).next
    assert.ok(nx, '链还没到头')
    path.push(nx.view + '/' + nx.segment)
    i = railIndexOf(rail, nx.view, nx.segment)
  }
  assert.deepEqual(path, ['tidy/complete', 'tidy/repair', 'tidy/quality', 'tidy/records', 'push/'])
})

test('反过来（右滑）同样是链上的上一项，包括同页的分段', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  const i = railIndexOf(rail, 'tidy', 'records')
  assert.deepEqual(railNeighbors(rail, i).prev, { key: 'tidy:quality', view: 'tidy', segment: 'quality' })
  assert.deepEqual(railNeighbors(rail, 0).prev, null)
  assert.deepEqual(railNeighbors(rail, rail.length - 1).next, null)
  assert.deepEqual(railNeighbors([], 0), { prev: null, next: null })
})

test('认不出当前位置 → 退到这一页第一项（不能变成 -1 而跳到链首）', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  assert.equal(railIndexOf(rail, 'tidy', 'gone'), 3)
  assert.equal(railIndexOf(rail, 'tidy'), 3)
  assert.equal(railIndexOf(rail, 'nope'), -1)
  assert.equal(railIndexOf(null, 'tidy'), -1)
})

test('链上下标拆成内外两条轨道：换页动外层，同页换段只动内层', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  const a = splitRail(rail, RAIL_ORDER, RAIL_SEGS, railIndexOf(rail, 'tidy', 'complete'))
  assert.deepEqual(a, { view: 'tidy', segment: 'complete', outerIndex: 2, innerIndex: 0 })
  const b = splitRail(rail, RAIL_ORDER, RAIL_SEGS, railIndexOf(rail, 'tidy', 'repair'))
  assert.equal(b.view, 'tidy')
  assert.equal(b.outerIndex, 2)
  assert.equal(b.innerIndex, 1)
  const c = splitRail(rail, RAIL_ORDER, RAIL_SEGS, railIndexOf(rail, 'push'))
  assert.deepEqual(c, { view: 'push', segment: '', outerIndex: 3, innerIndex: 0 })
})

test('splitRail 越界/脏数据不炸，回落给调用方交代的视图', () => {
  const rail = flattenRail(RAIL_ORDER, RAIL_SEGS)
  const r = splitRail(rail, RAIL_ORDER, RAIL_SEGS, 999, 'library')
  assert.equal(r.view, 'library')
  assert.equal(r.outerIndex, 1)
  assert.equal(r.innerIndex, 0)
  const bad = splitRail(null, null, null, 3)
  assert.equal(bad.view, '')
  assert.equal(bad.outerIndex, 0)
  assert.equal(bad.innerIndex, 0)
})
