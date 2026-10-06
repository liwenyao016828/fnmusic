<template>
  <!--
    左右滑动切页 —— **只给触摸设备用**（用户 2026-09-28 明确：这是手机端交互，电脑端不需要）。
    实现上只接 touch 事件、不接鼠标，所以桌面端无论怎么拖都不会触发；手机上跟手位移 + 到头阻尼 + 速度判定。

    结构是「三页滑轨」：轨道宽 300%，每页占 1/3（= 视口宽），默认用 -33.333% 显示中间那一页。
    拖动时给轨道加一个 ±dx 的像素位移，于是**手指下面就是页面在动，旁边那页看得见**；
    松手后按「拖过 25% 宽度 或 甩得够快（0.3px/ms）」决定翻页，否则弹回；
    **落位由阻尼弹簧驱动**（不是固定缓动曲线）：松手速度会带进运动里，先顺着劲儿冲、再被阻尼拉住；
    弹回用欠阻尼（会轻轻回弹一下），翻页用临界阻尼（不回弹，免得邻页露一条边像 bug）。

    ⚠️ 为什么不引第三方库：Swiper/Embla/use-gesture 都要求页面常驻 DOM，
       而这里 5 个页面都是重组件（各自挂载会发请求、扫目录），只能自己写 3 页滑轨。
       实现方式照它们的做法来：轴向锁定、passive:false 的 touchmove（锁定横向后才 preventDefault）、
       rAF 写 transform（不触发 Vue 重渲染）、松手后按速度判定。
  -->
  <div
    ref="viewportRef"
    data-swipe-viewport
    class="relative min-w-0 flex-1 overflow-hidden"
    :class="dragging ? 'select-none' : ''"
    @touchstart="onTouchStart"
  >
    <div ref="trackRef" class="flex h-full" :style="trackStyle">
      <!--
        每一页都是**常驻**的：只要访问过就留在 DOM 里（key 是视图 id），切页只是平移整行，
        不再卸载/重建组件 —— 这就是"切回来会重新请求一遍数据、卡一下"的根治办法。
        没访问过的页面不会被挂载（启动仍然只挂当前页），滑动开始时再把左右邻居预挂上。
      -->
      <section
        v-for="id in row"
        :key="id"
        :data-swipe-page="id"
        class="h-full shrink-0 overflow-hidden"
        :class="isIdlePage(id) ? 'swipe-page-idle' : ''"
        :style="{ width: 100 / row.length + '%' }"
      >
        <slot :name="'view-' + id" />
      </section>
    </div>
  </div>
</template>


<script setup>
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import {
  lockAxis, rubberBand, decideCommit, velocityFrom, settleDuration,
  isSwipeBlocked, resolveSwipeTarget,
  springAt, springSettled, springOmegaFor, clampVelocity,
  SPRING_ZETA_TURN, SPRING_ZETA_BACK, SPRING_MAX_MS,
} from '../services/swipeNav'

const props = defineProps({
  /** 视图顺序（App.vue 用 navItems 的 id 生成） */
  order: { type: Array, default: () => [] },
  current: { type: String, required: true },
})
const emit = defineEmits(['update:current'])

const viewportRef = ref(null)
const trackRef = ref(null)
const dragging = ref(false)
/**
 * ⚠️ 拖动位移**故意不是 ref**：每一帧改 ref 会让 SwipePager 重新渲染，
 * 而插槽内容是"每次渲染都重新生成的 vnode"——于是 5 个页面的整棵树每帧都要 diff 一遍，
 * 手机上就是明显的掉帧。改成**直接写 DOM 的 style**（Swiper / Embla 也是这么干的）：
 * 拖拽期间 Vue 完全不参与，只剩一次 style 写入。
 */
let dx = 0
let springRaf = 0
/** 视口宽（px）：用 px 平移，别用 calc(%) —— 轨道宽 500%，百分比每帧都要重算 */
const pageWidth = ref(0)
const pageHeight = ref(0)

/**
 * 行 = 已经挂载过的页面，按导航顺序排列。
 * 用它而不是固定三页：key 稳定 → 组件实例不重建 → 不重新请求、不卡。
 */
const row = ref([])
const rowIndex = computed(() => Math.max(0, row.value.indexOf(props.current)))

/** 左右邻居（到头就没有），也决定"还能不能往那个方向拖" */
const neighbors = computed(() => ({
  prev: resolveSwipeTarget(props.order, props.current, 'prev'),
  next: resolveSwipeTarget(props.order, props.current, 'next'),
}))

// 轨道只用响应式管宽度；transform 全部走下面的 applyTransform() 直接写 DOM
const trackStyle = computed(() => ({ width: `${(row.value.length || 1) * 100}%`, willChange: 'transform' }))

/**
 * 唯一的 transform 写入点：x = -(当前页下标 × 页宽) + 位移。
 *
 * ⚠️ **永远不加 CSS transition**：松手后由阻尼弹簧**每帧**驱动这个 transform，
 * 再叠一条 CSS 缓动就会两套时间轴打架（浏览器会拿 transition 去追每帧的目标值）。
 */
function applyTransform() {
  const el = trackRef.value
  if (!el) return
  el.style.transition = 'none'
  el.style.transform = `translate3d(${-(rowIndex.value * (pageWidth.value || 0)) + dx}px, 0, 0)`
}

/** 停掉正在跑的落位弹簧（新手势开始 / 组件卸载时） */
function stopSpring() {
  if (springRaf) { cancelAnimationFrame(springRaf); springRaf = 0 }
}

/**
 * 松手后的落位：**阻尼弹簧**（解析解）驱动 dx。
 * 松手速度真的带进运动里 —— 先顺着劲儿冲、再被阻尼拉住；弹回时还会轻轻回弹一下。
 * 解析解而不是数值积分：帧率无关，且不会像欧拉积分那样自带"数值阻尼"把回弹吃掉。
 */
function runSpring(toX, v0, zeta, durationMs, onDone) {
  stopSpring()
  const from = { x: dx, v: clampVelocity(v0), target: toX, omega: springOmegaFor(durationMs), zeta }
  const t0 = performance.now()
  const tick = (now) => {
    const elapsed = now - t0
    const st = springAt(from, elapsed)
    dx = st.x
    applyTransform()
    if (springSettled(st) || elapsed > SPRING_MAX_MS) {
      dx = toX
      applyTransform()
      springRaf = 0
      onDone()
      return
    }
    springRaf = requestAnimationFrame(tick)
  }
  springRaf = requestAnimationFrame(tick)
}

// 行的组成 / 当前页 / 尺寸变化 → 重新摆正（无动画）
watch([row, rowIndex, pageWidth], () => nextTick(() => applyTransform()))

function measure() {
  const vp = viewportRef.value
  if (!vp) return
  pageWidth.value = vp.clientWidth
  pageHeight.value = vp.clientHeight
  applyTransform()
}

/**
 * 这个页面要不要跳过渲染（content-visibility: auto）？
 *
 * ⚠️ 只跳过**隔了两页及以上**的页面。第一版写的是"只要不是当前页就跳过"，结果**即将滑入的那一页**
 * 也在跳过名单里 —— 它要等滑到位才被浏览器一次性渲染出来，用户看到的就是
 * "翻过去以后下一个页面突然出现在屏幕上"。现在左右邻居始终正常渲染，滑进来时它一直在场；
 * 省下来的是"离得远的两页"的光栅化，那才是真正看不见的部分。
 */
function isIdlePage(id) {
  if (id === props.current) return false
  if (id === neighbors.value.prev || id === neighbors.value.next) return false
  return true
}

/** 把某个视图挂进行里（按导航顺序插到正确位置，保证左右邻居在几何上就是相邻页） */
function mountView(id) {
  if (!id || row.value.includes(id)) return
  const order = props.order || []
  const next = [...row.value, id].sort((a, b) => {
    const ia = order.indexOf(a), ib = order.indexOf(b)
    return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
  })
  row.value = next
}

/** 预挂当前页的左右邻居：滑动开始时调一次，这样拖到一半旁边那页已经有内容 */
function padNeighbors() {
  mountView(neighbors.value.prev)
  mountView(neighbors.value.next)
}

watch(
  () => props.current,
  (id) => mountView(id),   // 只挂当前页；邻居等"真的开始拖动"时再预挂（见 begin）
  { immediate: true },
)

const viewportWidth = () => viewportRef.value?.clientWidth || 0

let start = null        // { x, y, t, type }
let axis = null         // 'x' | 'y' | null
let samples = []
let rafId = 0
let swallowTimer = 0

/** 起点是不是落在"横滑归它自己管"的地方（分类条、进度条、输入框…）—— 是就整个手势不接 */
function describeTarget(el) {
  let node = el
  for (let depth = 0; node && node.nodeType === 1 && depth < 6; depth++, node = node.parentElement) {
    const cs = getComputedStyle(node)
    if (isSwipeBlocked({
      tag: node.tagName,
      type: node.getAttribute && node.getAttribute('type'),
      editable: node.isContentEditable,
      // ⚠️ 不要只看 overflowX：竖向滚动容器的 overflowX 计算值也是 auto（见 swipeNav.js 的说明）。
      //    只有"内容真的比容器宽"才算横滑区，才让路。
      canScrollX: (cs.overflowX === 'auto' || cs.overflowX === 'scroll') && node.scrollWidth > node.clientWidth + 1,
      inPlayer: !!(node.closest && node.closest('.ys-player, .ys-ambient')),
      noDrag: !!(node.classList && node.classList.contains('swipe-no-drag')),
    })) return true
  }
  return false
}

function onTouchStart(e) {
  if (e.touches.length !== 1) return
  begin(e.touches[0].clientX, e.touches[0].clientY, e.target)
  if (!start) return
  // 只在手势真的开始时挂非 passive 的 touchmove：锁定横向前不拦，竖向滚动照常
  window.addEventListener('touchmove', onTouchMove, { passive: false })
  window.addEventListener('touchend', onTouchEnd)
  window.addEventListener('touchcancel', onTouchEnd)
}

function onTouchMove(e) {
  const t = e.touches[0] || (e.changedTouches && e.changedTouches[0])
  if (!t) return
  step(t.clientX, t.clientY, e)
}

function onTouchEnd() {
  finish()
}

function detach() {
  window.removeEventListener('touchmove', onTouchMove)
  window.removeEventListener('touchend', onTouchEnd)
  window.removeEventListener('touchcancel', onTouchEnd)
}

function begin(x, y, target) {
  if (describeTarget(target)) return
  padNeighbors() // 预挂邻居：拖到一半旁边那页已经有内容，不会先白一下
  stopSpring()   // 上一次落位没跑完就再上手：就地接管，不跳变
  start = { x, y, t: performance.now() }
  axis = null
  samples = [{ x, t: start.t }]
}

function step(x, y, e) {
  if (!start) return
  const moveX = x - start.x
  const moveY = y - start.y
  if (!axis) {
    axis = lockAxis(moveX, moveY)
    if (!axis) return
    if (axis === 'y') { detach(); start = null; return } // 竖向 → 放手，页面该滚就滚
    dragging.value = true
    // 滑动期间关掉玻璃模糊：背景内容在动的时候，backdrop-filter 每帧都要重算，手机上最贵
    document.documentElement.dataset.swiping = '1'
    padNeighbors() // 这时候才预挂邻居（拖到一半旁边那页已经有内容）
  }
  // 锁定横向后才阻断浏览器默认行为 —— 这时这划已经归我们了，也避免浏览器把触摸序列取消掉
  if (e && e.cancelable) e.preventDefault()

  const now = performance.now()
  samples.push({ x, t: now })
  if (samples.length > 8) samples.shift()

  dx = rubberBand(moveX, { canPrev: !!neighbors.value.prev, canNext: !!neighbors.value.next })
  if (!rafId) {
    rafId = requestAnimationFrame(() => { rafId = 0; applyTransform(false) })
  }
}

function clearSwiping() {
  delete document.documentElement.dataset.swiping
}

function finish() {
  if (!start) { dragging.value = false; detach(); clearSwiping(); return }
  detach()
  const wasAxisX = axis === 'x'
  const stopped = start
  start = null
  axis = null
  dragging.value = false
  if (!wasAxisX) { dx = 0; applyTransform(); clearSwiping(); return }

  const velocity = velocityFrom(samples, performance.now())
  const width = viewportWidth()
  const dir = decideCommit({
    dx, width, velocity: Math.abs(velocity) < 0.05 ? 0 : velocity,
    canPrev: !!neighbors.value.prev, canNext: !!neighbors.value.next,
  })

  if (!dir) {
    // 弹回：**欠阻尼**弹簧 —— 越过原位一点点再回来，就是用户要的"阻尼感"
    const backMs = settleDuration(Math.abs(dx), velocity)
    runSpring(0, velocity, SPRING_ZETA_BACK, backMs, () => {
      dx = 0
      applyTransform()
      clearSwiping()
    })
    return
  }

  const target = dir === 'next' ? neighbors.value.next : neighbors.value.prev
  const remaining = width - Math.abs(dx)
  const toX = dir === 'next' ? -width : width
  // 翻页：**临界阻尼** —— 不回弹。回弹会让"下一页"先露一条边再退回来，看着像 bug
  runSpring(toX, velocity, SPRING_ZETA_TURN, settleDuration(remaining, velocity), async () => {
    // ⚠️ **先归零再挂页**：mountView 会改 row/rowIndex，触发那个 watch 立刻重算一次 transform。
    //    如果此时 dx 还停在 ±页宽，就会用"新下标 + 旧位移"算出一个错位值（实测会跳到 -1170px）。
    //    归零后：-(新下标×页宽) 与弹簧终点正好重合，切页是**无缝**的。
    dx = 0
    mountView(target)
    emit('update:current', target)
    await nextTick()
    requestAnimationFrame(() => {
      applyTransform()
      clearSwiping()
      swallowNextClick()
    })
  })
}

/** 翻页动画结束那一下的 click 不该再落到页面元素上（否则会误触发按钮/歌曲行） */
function swallowNextClick() {
  const stop = (ev) => { ev.stopPropagation(); ev.preventDefault() }
  window.addEventListener('click', stop, { capture: true, once: true })
  if (swallowTimer) clearTimeout(swallowTimer)
  swallowTimer = setTimeout(() => window.removeEventListener('click', stop, { capture: true }), 400)
}

let sizeObserver = null
onMounted(() => {
  measure()
  if (typeof ResizeObserver !== 'undefined' && viewportRef.value) {
    sizeObserver = new ResizeObserver(() => measure())
    sizeObserver.observe(viewportRef.value)
  }
})

onBeforeUnmount(() => {
  detach()
  clearSwiping()
  if (sizeObserver) sizeObserver.disconnect()
  if (rafId) cancelAnimationFrame(rafId)
  stopSpring()
  if (swallowTimer) clearTimeout(swallowTimer)
})
</script>
