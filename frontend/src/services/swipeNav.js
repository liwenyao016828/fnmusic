/**
 * 左右滑动切页 —— **纯判定逻辑**（页面跟手、到头阻尼、甩一下翻页）。
 *
 * 为什么单独一个文件：本项目前端测试是 `node --test`，**没有 DOM / jsdom**，
 * 所以「往上找祖先、读 computedStyle、写 transform」留在组件里，
 * 这里只放能被单测覆盖的数学与判断。
 *
 * 方向约定：dx 为负 = 手指往左 → 下一页(next)；dx 为正 = 往右 → 上一页(prev)。
 */

/**
 * 这套参数是**按手指**调的 —— 滑动切页只给触摸设备用（鼠标不参与，见 SwipePager 的说明）。
 * 手指的抖动比鼠标大、行程比鼠标短、还会"甩"，所以阈值比桌面拖拽小、松手动画跟速度走。
 */
/** 起始多少像素内先定方向（轴向锁定）：手指抖 8px 很常见，再小就分不清"想横滑"还是"想竖滚" */
export const AXIS_LOCK_PX = 8
/** 拖过视口宽度的这个比例就算翻页（手指够不到 1/3，25% 更跟手） */
export const COMMIT_RATIO = 0.25
/** 或者甩得够快（px/ms）也算翻页 —— 手机上"短促一甩"比"拖够距离"更常用 */
export const COMMIT_VELOCITY = 0.3
/** 到头阻尼系数（只能拉动 40%，像橡皮筋；再小会觉得"拽不动"） */
export const RUBBER_FACTOR = 0.4
/** 松手后最短/最长动画时长（ms）—— 稍微放长一点，落位别一下就停住 */
export const SETTLE_MIN_MS = 220
export const SETTLE_MAX_MS = 420
/** 兼容旧名：默认动画时长 */
export const SNAP_MS = 280

/**
 * 松手后的动画时长：跟手指的速度走 —— 甩得越快，收得越快（iOS 那种"顺着劲儿滑过去"）。
 * 慢速拖到一半才松手 → 给足时间（最多 SETTLE_MAX_MS）；甩一下 → 干脆利落（最少 SETTLE_MIN_MS）。
 * @param {number} remainingPx 还差多少像素到位
 * @param {number} velocity px/ms
 */
export function settleDuration(remainingPx, velocity = 0) {
  const speed = Math.max(Math.abs(velocity), 0.4) // 下限 0.4px/ms，避免除得过大
  const ms = Math.abs(remainingPx) / speed
  return Math.min(SETTLE_MAX_MS, Math.max(SETTLE_MIN_MS, ms))
}

/* ══════════════════════════════════════════════════════════════════════════
   松手后的「阻尼弹簧」（用户 2026-09-30：「加上滑动结束后的阻尼感」）

   原来松手是一条**固定缓动曲线**（cubic-bezier）：位移"按时长播完"，手感像"被拖到终点"——
   没有惯性、也感觉不到重量。改成**阻尼弹簧**后，松手时的速度会真的带进运动里：
   先顺着劲儿冲，再被阻尼拉住，最后贴到位。

   翻页与弹回用**不同阻尼比**是刻意的：
     · 翻页 ζ=1（临界阻尼）—— 不回弹。回弹会让"下一页"先露一条边再退回来，看着像 bug。
     · 弹回 ζ<1（欠阻尼）—— 回弹一下再停，用户要的那个"阻尼感"就在这里。

   纯数学、无 DOM，放这里是为了能被 `node --test` 覆盖（本仓库前端测试没有 jsdom）。
   ══════════════════════════════════════════════════════════════════════════ */

/** 落位时长 → 角频率的换算系数：ω = K / ms。ω 越大弹簧越硬、收得越快 */
export const SPRING_SETTLE_K = 8.9
/** 翻页的阻尼比：临界阻尼，不回弹 */
export const SPRING_ZETA_TURN = 1
/** 弹回的阻尼比：欠阻尼，回弹一下再停（"阻尼感"的来源） */
export const SPRING_ZETA_BACK = 0.68
/** 松手速度上限（px/ms）：再快也不让弹簧冲过头 */
export const SPRING_MAX_V = 2.5
/** 兜底时长：数值积分万一不收敛，最多跑这么久就强制收尾 */
export const SPRING_MAX_MS = 900

/**
 * 落位时长（ms）→ 弹簧角频率。
 * 之所以还走 `settleDuration()` 那套时长：它是**按手指调过的**（甩得越快收得越快），
 * 这里只把"时长"换成"弹簧的软硬"，形状交给阻尼弹簧。
 */
export function springOmegaFor(durationMs) {
  return SPRING_SETTLE_K / Math.max(60, durationMs)
}

/** 松手速度限幅（顺带挡掉 NaN） */
export function clampVelocity(v, max = SPRING_MAX_V) {
  if (!Number.isFinite(v)) return 0
  return Math.max(-max, Math.min(max, v))
}

/**
 * 阻尼弹簧在 t 毫秒时的位置与速度 —— **解析解**，不是数值积分。
 *
 * ⚠️ 为什么不用半隐式欧拉：ω·dt 一大会引入**数值阻尼**（实测 ω=0.04、dt=16ms 时
 * 连 ζ=0.68 的欠阻尼都被吃掉，回弹直接消失 → "阻尼感"就没了）。
 * 解析解还顺带解决了帧率依赖：60Hz / 120Hz / 掉帧，轨迹完全一样。
 *
 * ζ=1（临界阻尼，翻页用）不振荡；ζ<1（欠阻尼，弹回用）会越过目标再回来。
 * @param {{x:number,v:number,target:number,omega:number,zeta:number}} s 起始状态
 * @param {number} t 从松手算起的毫秒数
 */
export function springAt(s, t) {
  const d = s.x - s.target
  const w = s.omega
  const z = s.zeta
  if (z >= 1 - 1e-6) {
    // 临界阻尼：x - target = e^{-ωt}·(d + (v₀+ωd)·t)
    const e = Math.exp(-w * t)
    const b = s.v + w * d
    return { x: s.target + e * (d + b * t), v: e * (s.v - w * b * t), target: s.target }
  }
  // 欠阻尼：ω_d = ω√(1-ζ²)，包络 e^{-ζωt}
  const wd = w * Math.sqrt(1 - z * z)
  const e = Math.exp(-z * w * t)
  const c = Math.cos(wd * t)
  const sn = Math.sin(wd * t)
  const b = (s.v + z * w * d) / wd
  return {
    x: s.target + e * (d * c + b * sn),
    v: e * (s.v * c - (z * w * b + d * wd) * sn),
    target: s.target,
  }
}

/** 到位判定：位置与速度都小到看不出来 */
export function springSettled(s, xTol = 0.5, vTol = 0.02) {
  return Math.abs(s.x - s.target) <= xTol && Math.abs(s.v) <= vTol
}

/**
 * 轴向锁定：在头 `AXIS_LOCK_PX` 像素内谁大谁是主轴。
 * @returns {'x'|'y'|null} null = 还不够判定，继续等
 */
export function lockAxis(dx, dy) {
  if (Math.abs(dx) < AXIS_LOCK_PX && Math.abs(dy) < AXIS_LOCK_PX) return null
  return Math.abs(dx) > Math.abs(dy) ? 'x' : 'y'
}

/**
 * 到头阻尼：第一页再往右拉、最后一页再往左拉，都只能拉动一点点（跟手但不越界）。
 * @param {number} dx 原始位移
 * @param {{canPrev:boolean, canNext:boolean}} opts
 */
export function rubberBand(dx, { canPrev = true, canNext = true } = {}) {
  if (dx < 0 && !canNext) return dx * RUBBER_FACTOR
  if (dx > 0 && !canPrev) return dx * RUBBER_FACTOR
  return dx
}

/**
 * 松手时：翻页还是弹回？
 * 两个条件任一成立就翻：拖过 COMMIT_RATIO 宽度，或者速度超过 COMMIT_VELOCITY。
 * 到头方向即使满足也不翻（配合 rubberBand，视觉上不会越界）。
 * @returns {'prev'|'next'|null}
 */
export function decideCommit({ dx = 0, width = 1, velocity = 0, canPrev = true, canNext = true } = {}) {
  if (!width) return null
  const far = Math.abs(dx) >= width * COMMIT_RATIO
  const fast = Math.abs(velocity) >= COMMIT_VELOCITY
  if (!far && !fast) return null
  const dir = dx < 0 ? 'next' : 'prev'
  if (dir === 'next' && !canNext) return null
  if (dir === 'prev' && !canPrev) return null
  return dir
}

/**
 * 速度（px/ms）：取最近两个采样点，时间窗太旧就不算（松手前停顿过的手不该算成"甩"）。
 * @param {Array<{x:number,t:number}>} samples
 * @param {number} now
 */
export function velocityFrom(samples, now, windowMs = 120) {
  if (!Array.isArray(samples) || samples.length < 2) return 0
  const last = samples[samples.length - 1]
  if (now - last.t > windowMs) return 0
  let first = last
  for (let i = samples.length - 2; i >= 0; i--) {
    first = samples[i]
    if (last.t - samples[i].t >= 40) break // 至少跨 40ms，避免除以极小值
  }
  const dt = last.t - first.t
  if (dt <= 0) return 0
  return (last.x - first.x) / dt
}

/**
 * 起点是不是在"横滑归它自己管"的地方 —— 是的话整个手势都别接。
 *
 * ⚠️ **2026-09-28 踩过的坑**：一开始这里判的是 `overflow-x` 是不是 auto/scroll。但 CSS 的规矩是
 * 「只要一个轴不是 visible，另一个轴的计算值就变成 auto」—— 于是页面里所有
 * `overflow-y-auto` 的竖向滚动容器，`overflowX` 的计算值都是 `auto`，全被判成"横滑区"，
 * 结果**大部分地方怎么拖都不切页**（用户原话「我切换不了呢，就成功过一次」）。
 * 现在只看 `canScrollX` —— 由调用方按 `scrollWidth > clientWidth` 真实算出来。
 *
 * @param {{tag?:string,type?:string,editable?:boolean,canScrollX?:boolean,inPlayer?:boolean,noDrag?:boolean}} desc
 */
export function isSwipeBlocked(desc) {
  if (!desc || typeof desc !== 'object') return false
  if (desc.noDrag) return true // 显式标了 .swipe-no-drag
  if (desc.editable) return true
  const tag = String(desc.tag || '').toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true
  if (String(desc.type || '').toLowerCase() === 'range') return true
  if (desc.canScrollX) return true // 分类条 / 歌单横滑：真的能横向滚才让路
  if (desc.inPlayer) return true
  return false
}

/**
 * 这次该切到哪个视图。**到头就停住，不循环** —— 页面不是环形的，循环会让人失去位置感。
 * @param {string[]} order
 * @param {string} current
 * @param {'next'|'prev'} dir
 * @returns {string|null} 目标视图，null = 没有下一页/上一页
 */
export function resolveSwipeTarget(order, current, dir) {
  if (!Array.isArray(order) || order.length === 0) return null
  const i = order.indexOf(current)
  if (i === -1) return dir === 'next' ? order[0] : order[order.length - 1]
  const j = dir === 'next' ? i + 1 : i - 1
  return j >= 0 && j < order.length ? order[j] : null
}
