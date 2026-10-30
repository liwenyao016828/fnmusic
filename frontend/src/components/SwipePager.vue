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
    @touchmove="onTouchMove"
    @touchend="onTouchEnd"
    @touchcancel="onTouchEnd"
  >
    <div ref="trackRef" class="flex h-full" data-swipe-track :style="trackStyle">
      <!--
        每一页都是**常驻**的：只要访问过就留在 DOM 里（key 是视图 id），切页只是平移整行，
        不再卸载/重建组件 —— 这就是"切回来会重新请求一遍数据、卡一下"的根治办法。
        没访问过的页面不会被挂载（启动仍然只挂当前页），滑动开始时再把左右邻居预挂上。
      -->
      <section
        v-for="id in row"
        :key="id"
        :data-swipe-page="id"
        class="h-full shrink-0 overflow-hidden swipe-page"
        :class="isIdlePage(id) ? 'swipe-page-idle' : ''"
        :style="{ width: 100 / row.length + '%' }"
      >
        <!--
          页内分段 = 第二层滑轨，**分段器在这一格外面**（2026-10-29 第五次改版）。

          用户口径：「页内小导航组件应该固定吧，不跟随滑动，只有导航栏下面的页面滑动。
          除了大导航页之间切换，小导航组件跟随大页面滑动」。

          于是两条规则分得很清楚：
            · 小导航（分段器）钉在 `page-head-<页>` 里 —— 不在滑轨上，页内换段时一动不动；
            · 内容站在内层滑轨上 —— 页内换段时只有它滑；
            · 大导航之间换页时，整页（含它的分段器）一起被外层轨道推走 ——
              因为分段器在 `<section>` 里面，自然跟着大页走。
        -->
        <div v-if="innerHasRail(id)" class="flex h-full min-h-0 flex-col">
          <div v-if="$slots['page-head-' + id]" class="shrink-0">
            <slot :name="'page-head-' + id" />
          </div>
          <div
            ref="innerRailRef"
            class="min-h-0 flex-1 overflow-hidden"
            data-rail-level="inner"
          >
            <div class="flex h-full" data-swipe-track :style="innerTrackStyle">
              <div
                v-for="seg in innerRowFor(id)"
                :key="id + ':' + seg"
                class="h-full shrink-0 overflow-hidden"
                :class="innerIdleFor(id, seg) ? 'swipe-page-idle' : ''"
                :style="{ width: 100 / (innerRowFor(id).length || 1) + '%' }"
              >
                <slot :name="'inner-' + id + '-' + seg" />
              </div>
            </div>
          </div>
        </div>
        <slot v-else :name="'view-' + id" />
      </section>
    </div>
  </div>
</template>


<script setup>
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import {
  lockAxis, axisYields, rubberBand, decideCommit, velocityFrom, settleDuration,
  isSwipeBlocked, resolveSwipeTarget,
  flattenRail, railIndexOf, railNeighbors,
  springAt, springSettled, springOmegaFor, clampVelocity,
  SPRING_ZETA_TURN, SPRING_ZETA_BACK, SPRING_MAX_MS,
} from '../services/swipeNav'

const props = defineProps({
  /** 视图顺序（App.vue 用 navItems 的 id 生成） */
  order: { type: Array, default: () => [] },
  current: { type: String, required: true },
  /**
   * 当前页的**页内分段**（2026-10-28）：{ [viewId]: [segId, ...] }。
   * 传了之后，这一页里标了 data-rail-level="inner" 的那块内容会多一层滑轨：
   * 手指在分段器/内容上左右划 → 整页（含分段器）一起动，同时内层再横移一页宽。
   */
  segment: { type: String, default: '' },
  innerSegs: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['update:current', 'update:segment'])

const viewportRef = ref(null)
const trackRef = ref(null)
/**
 * 内层滑轨容器的 ref。
 * ⚠️ 它写在 `<section v-for>` 里，Vue 会按**数组 ref** 收集（`innerRailEls.value` 是数组）。
 *    所以访问时必须取 [0]，不能直接 `.style` —— 见 innerRailEl()。
 */
const innerRailRef = ref(null)
const dragging = ref(false)
/**
 * ⚠️ 拖动位移**故意不是 ref**：每一帧改 ref 会让 SwipePager 重新渲染，
 * 而插槽内容是"每次渲染都重新生成的 vnode"——于是 5 个页面的整棵树每帧都要 diff 一遍，
 * 手机上就是明显的掉帧。改成**直接写 DOM 的 style**（Swiper / Embla 也是这么干的）：
 * 拖拽期间 Vue 完全不参与，只剩一次 style 写入。
 */
let dx = 0
let springRaf = 0

/**
 * ── 内嵌一层（页内分段）的滑轨状态（2026-10-28 用户：「页面中的内导航也加入整体滑动」）──
 *
 * 手势归属：`level` = 'outer' | 'inner' | null。
 *   · 起点落在 `[data-rail-level="inner"]` 里、且当前页有 innerSurface 槽 → 这一划归内层，
 *     叫父级（App.vue）一起把**整块页面（含它自己的分段器）**横移，并且内层再额外横移一页宽，
 *     于是分段切换时「分段器本身」也跟手滑走，而不是只有下面内容换掉。
 *   · 否则归外层，就是原来的整页横滑。
 * 归一化后内外用的是同一套阻尼/速度/落位逻辑，只是终点 x 不同。
 */
let level = null
let dxInner = 0
let innerSpringRaf = 0
let innerRowIndex = ref(0)
const innerRow = ref([])
let innerSegs = []
let innerView = ''
let innerCurrent = ''

/**
 * ⚠️⚠️ 2026-10-29 关键修复：内层分段「只能往后滑、回不到上一段」。
 *
 * 根因：`innerSegs` / `innerCurrent` 是普通的 `let` 变量（不是 ref），而下面这个
 * `innerNeighbors` 却是 `computed`。computed 只在自己的**响应式依赖**变化时重算，
 * 而普通变量根本不是依赖 —— 于是这个 computed 会在**第一次被求值后永久缓存**。
 * 第一次求值时 `innerSegs` 还是空数组（watch immediate 还没把分段填进来），
 * 算出来就是 `{ prev: null, next: 'charts' }`，之后再也不会变：
 *   · `canPrev` 永远 false → `rubberBand` 把左滑（右滑带动画）按"到头了"压缩成橡皮筋，
 *     跟手量只有约 1/3 页宽，永远够不到 COMMIT_RATIO → 每次都弹回原位；
 *   · `canNext` 永远 true → 往左一路"能滑"，于是表现为**只能前进、不能后退**。
 *
 * 现在把 `innerNeighbors` 改成**普通函数**（每次调用现算，读的就是最新的变量），
 * 并把调用处从 `innerNeighbors.value` 改成 `innerNeighbors()`。
 * 不用 ref 改重写是有意的：这两个值只在手势处理里读，没有任何模板依赖它们，
 * 变成 ref 只会多一层订阅开销，还要小心 drag 期间触发重渲染。
 */
function innerNeighbors() {
  return {
    prev: resolveSwipeTarget(innerSegs, innerCurrent, 'prev'),
    next: resolveSwipeTarget(innerSegs, innerCurrent, 'next'),
  }
}

/* 轨道宽度必须随行数变；will-change 交给 CSS 的 html[data-swiping] 开关，
   不再常驻（常驻会让轨道与每一页长期各占一个合成层）。 */
const innerTrackStyle = computed(() => ({ width: `${(innerRow.value.length || 1) * 100}%` }))

/**
 * 内层拖动/落位的统一绘制（2026-10-28）。
 *
 * 效果：整页（含它的分段器）跟着手指走 + 内层再多横移一页宽，于是「分段器本身也在滑」。
 * 实现：只写一个 CSS 变量 `--rail-dx` 到视口上，由 CSS 分别作用到
 *   ① 视口内当前 section 的整体位移（`transform: translate3d(var(--rail-dx),0,0)`）
 *   ② 内层轨道的位移（在 applyInnerTransform 里本来就有）
 * 不在 JS 里同时写两处 transform：两处都要写同一份每帧变化的值时，帧序一错就抖。
 */
function paintInner() {
  const el = viewportRef.value
  if (!el) return
  /**
   * ⚠️ 2026-10-29：`--rail-dx` 不再用于「整页跟随位移」（见 style.css 里那条被停用的规则）。
   *    分段器现在固定在 `page-head-<页>` 插槽里，只有内容进滑轨；整页再跟着位移，
   *    就等于把内容平移两遍（用户看到「分段器整块飞出视口」）。
   *    保留这个变量的写入只是为了调试时能在 DOM 上看到当前 drag 量。
   */
  el.style.setProperty('--rail-dx', `${dxInner}px`)
  el.style.setProperty('--rail-fix', el.dataset.railFixed === '1'
    ? `${-(innerRowIndex.value * (pageWidth.value || 0))}px`
    : '0px')
  el.dataset.railShift = dxInner ? '1' : ''
}

function applyInnerTransform() {
  const el = innerTrackEl()
  if (!el) return
  el.style.transition = 'none'
  el.style.transform = `translate3d(${-(innerRowIndex.value * (pageWidth.value || 0)) + dxInner}px, 0, 0)`
}

/**
 * 内层轨道元素。
 *
 * ⚠️ 2026-10-29 关键修复：这个 `ref="innerTrackRef"` 写在 `<section v-for>` 里面，
 *    Vue 于是把它当成**数组 ref** 收集（`innerTrackRef.value` 是一个 `[el]` 数组），
 *    对它取 `.style` 直接抛 TypeError —— 于是：
 *      · 内层轨道永远不动（只有外层整页在动），
 *      · 抛异常把 `finish()` 从中间打断，`clearSwiping()` 执行不到，
 *        `html[data-swiping="1"]` 永久残留（玻璃模糊再也不恢复、页面一直当"在滑"），
 *        `--rail-dx` 也留在视口上，下一次滑动整页歪着。
 *    改成从视口直接查 DOM：只有**当前页**才有内层轨道（见 innerHasRail），
 *    所以这个选择器不会有歧义，也不需要再维护一个 ref。
 */
function innerTrackEl() {
  // ⚠️ 2026-10-29 修「子导航不跟手」：以前用
  //   `vp.querySelector('[data-rail-level="inner"] [data-swipe-track]')`
  //   找内层轨道。外层 `<div class="flex h-full" data-swipe-track>`（整行轨道）
  //   **本身也带 `data-swipe-track`**，选择器又会匹配「后代」——于是它命中的
  //   很可能不是内层轨道那条，而是别的东西，每帧把位移写错元素：
  //   内层轨道因此不动，只有整页在动（＝用户看到的「子导航没加入切换」）。
  //   现在直接拿 ref 指向的那个容器，再用 :scope 限定到它自己的直接子轨道。
  //   取不到（还没渲染）就返回 null，交给调用方跳过这一帧。
  const holders = innerRailRef.value
  const holder = Array.isArray(holders) ? holders[0] : holders
  if (!holder) return null
  return holder.querySelector(':scope > [data-swipe-track]')
}

function stopInnerSpring() {
  if (innerSpringRaf) { cancelAnimationFrame(innerSpringRaf); innerSpringRaf = 0 }
}

function innerIsIdlePage(id) {
  if (id === innerCurrent) return false
  const nb = innerNeighbors()
  if (id === nb.prev || id === nb.next) return false
  return true
}

/**
 * 内层滑轨装哪些分段？
 *
 * ⚠️⚠️ 2026-10-29 改：以前是「只装当前段 + 左右邻居」（懒加），本意是省渲染。
 *    但用户要的是「分段器依次左右滑」（补全→修复→音质→快照，一个一个过去），
 *    而懒加让滑轨里只有 2 个格子（50%/50%）：滑到第 3 段时滑轨里还没有它，
 *    `innerRowIndex` 只能取到 0/1，`translate3d` 就永远是最多一页宽 ——
 *    表现就是「滑两下之后卡住不动 / 分段器整块飞出视口」。
 *
 *    现在：**当前页的所有分段一次性全部装进滑轨**（整理页 4 个 = 4 格 25%）。
 *    代价是同一页里多渲染几个分段的内容 —— 但它们本来就都在同一个 TidyView 里
 *    （用 v-show 切换），代价可忽略；换来的是「4 段都能按顺序滑」这个正确行为。
 *    非当前段仍带 `swipe-page-idle`（content-visibility:auto），不参与光栅化。
 */
function mountInnerSeg(id) {
  if (!id || innerRow.value.includes(id)) return
  const next = [...innerRow.value, id].sort((a, b) => innerSegs.indexOf(a) - innerSegs.indexOf(b))
  innerRow.value = next
}

/** 把当前页的**全部分段**装进内层滑轨（幂等：已装的不重复装） */
function mountAllInnerSegs() {
  if (!innerSegs.length) return
  if (innerRow.value.length === innerSegs.length && innerSegs.every((s) => innerRow.value.includes(s))) return
  innerRow.value = [...innerSegs]
}

function padInnerNeighbors() {
  // 全装策略下"预挂邻居"已无意义（本来全都在），保留函数名是为了不动调用点。
  // 2026-10-29：调用点现在在**方向锁定之后**（那时 props.current 已经确定），
  // 早先写在 touchstart 里时，第一次进入某页会在"上一页的分段表"上铺轨道。
  mountAllInnerSegs()
}


/** 当前页有哪些分段（由 App 传入；没有 = 这一页不分段） */
function innerSegsOf(view) {
  const map = props.innerSegs || {}
  return map[view] || []
}

/**
 * ⚠️ 只有**当前页**挂内层滑轨：一个 SwipePager 只维护一份内层状态（一页宽、一个下标），
 * 多个页面同时有滑轨的话会互相踩。用户的诉求是「当前这页的内导航也能滑」，
 * 别的页面在滑到位之前看不见，等它变成当前页时再装滑轨也不晚。
 */
function innerHasRail(view) {
  return view === props.current && innerSegsOf(view).length > 1
}

function innerRowFor(view) {
  return innerHasRail(view) ? innerRow.value : []
}

function innerIdleFor(view, seg) {
  return innerHasRail(view) ? innerIsIdlePage(seg) : false
}

/**
 * 内层滑轨的状态同步。
 *
 * ⚠️⚠️ 2026-10-29 关键修复：「切页后分段器整块错位一页宽」（用户：「补全整理的 4 个也要加进滑动」）。
 *
 * 根因：`innerRow` 是**一份全局数组**（一个 SwipePager 只维护一份内层状态），
 * 而旧写法在 `props.current` 变化时**只追加、不重置** —— 于是从「发现」（分段 discover/charts）
 * 滑到「整理」（分段 complete/repair/quality/records）时，数组里留着上一页的分段 id：
 *   innerRow = ['discover', 'complete', 'repair']   ← discover 是**上一页**的分段
 * 由此连锁出错：
 *   · 内层轨道宽度按 innerRow.length 算（300%），可实际只渲染本页的分段（3 个 33%）；
 *   · innerRowIndex 取 'repair' 的下标 = 2 → 轨道 translate3d(-2×页宽) = -2128px；
 *   · 但 DOM 里 'discover' 那个格子渲染的是**空插槽**（`#inner-tidy-discover` 不存在），
 *     于是整排内容凭空右移一页宽 —— 视觉上就是分段器飞出了视口。
 *
 * 现在：只要 `props.current` 变了（换页），先清空 innerRow 再按新页重新装，
 * 保证这份数组里永远只有**当前页**的分段 id。
 */
watch(
  () => props.current,
  () => {
    innerRow.value = []
    innerRowIndex.value = 0
    dxInner = 0
  },
)

watch(
  () => [props.current, props.segment, props.innerSegs],
  () => {
    innerView = props.current
    innerSegs = innerSegsOf(props.current)
    innerCurrent = props.segment || innerSegs[0] || ''
    if (!innerSegs.length) { innerRow.value = []; innerRowIndex.value = 0; dxInner = 0; return }
    // 防御：万一还有不属于本页的残留 id（例如上面那个重置 watch 与本 watch 的执行顺序差异），一并剔掉
    if (innerRow.value.some((s) => !innerSegs.includes(s))) {
      innerRow.value = innerRow.value.filter((s) => innerSegs.includes(s))
    }
    mountInnerSeg(innerCurrent)
    // 当前页的 4 个分段一次性全装（见 mountAllInnerSegs 的说明），这样才滑得过去
    mountAllInnerSegs()
    innerRowIndex.value = Math.max(0, innerRow.value.indexOf(innerCurrent))
    nextTick(() => applyInnerTransform())
  },
  { immediate: true, deep: true },
)

function innerPageWidth() { return viewportWidth() }
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
const trackStyle = computed(() => ({ width: `${(row.value.length || 1) * 100}%` }))

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

/**
 * 内层（页内分段）轨道的落位弹簧 —— 与 runSpring 同一套解析解，只是作用在 dxInner 上。
 *
 * ⚠️ 2026-10-28 补齐：这个函数原来**只有调用没有定义**（finish() 里两处调用），
 *    内层手势一松手就抛 `runInnerSpring is not defined`，于是分段器永远停在半路、
 *    `--rail-dx` 残留、下次滑动整页歪着 —— 这就是「页内导航滑不动」的另一半原因。
 */
function runInnerSpring(toX, v0, zeta, durationMs, onDone) {
  stopInnerSpring()
  const from = { x: dxInner, v: clampVelocity(v0), target: toX, omega: springOmegaFor(durationMs), zeta }
  const t0 = performance.now()
  const tick = (now) => {
    const elapsed = now - t0
    const st = springAt(from, elapsed)
    dxInner = st.x
    applyInnerTransform()
    paintInner()
    if (springSettled(st) || elapsed > SPRING_MAX_MS) {
      dxInner = toX
      applyInnerTransform()
      paintInner()
      innerSpringRaf = 0
      onDone()
      return
    }
    innerSpringRaf = requestAnimationFrame(tick)
  }
  innerSpringRaf = requestAnimationFrame(tick)
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
}

/**
 * 这一划动谁？—— **统一滑轨（2026-10-29 第四次按用户口径重做）**
 *
 * 用户原话：「我想要的导航滑动切换页面，是包括了页面中的小导航。我举例，曲库换到
 * 下一个页面应该是补全，继续滑是修复……最后快照继续滑就是订阅。其他几个导航页面里
 * 有这种都统一为这样包含小页面」
 *
 * 所以「谁动」**不再由手指压在哪个元素上决定**，而由链上的下一项决定：
 *   · 下一项是别的页面     → 整页横滑（outer）；
 *   · 下一项是同一页的另一段 → 只动这一页的内容（inner），页头的小导航钉住不动。
 * 三端一致、没有随机性：手指按在页面任意位置、往左甩，看到的都是「链上的下一项」。
 *
 * ⚠️ 前几版踩过的坑（都因为"归属"是靠元素判的）：
 *   · 起点落在 `[data-rail-level="inner"]`（包着整页内容）就归内层 → 想翻页结果换了子导航；
 *   · 改成"只有压在分段器上才归内层" → 手指按在内容上划不动分段
 *     （用户又报「子导航没加入切换」）。
 * 现在判定只看链，元素只剩一个例外：真能横向滚的条（分类胶囊等）由 describeTarget 挡掉。
 *
 * @param {number} moveX 锁定轴向时的累计位移（负=往左=下一项）
 * @param {HTMLElement} target 起点元素（只为兜底：还没链时退化成按分段器判）
 */
function pickLevel(moveX, target) {
  const dir = moveX < 0 ? 'next' : 'prev'
  const it = railStep(dir)
  if (it) return it.view === props.current ? 'inner' : 'outer'
  // 链还没算出来（首帧 / innerSegs 还没传）：退回旧规则，至少别让滑动整个失效
  if (!innerSegs.length) return 'outer'
  const hit = target && target.closest && target.closest('[role="tablist"]')
  return hit && !hit.closest('.swipe-no-drag') ? 'inner' : 'outer'
}

/** 扁平链（视图 × 分段，见 swipeNav.flattenRail）—— 每次现算，避免 computed 缓存住旧分段表 */
function railList() {
  return flattenRail(props.order || [], props.innerSegs || {})
}

/** 当前在链上的位置 */
function railPos() {
  return railIndexOf(railList(), props.current, props.segment || (innerSegs.length ? innerSegs[0] : ''))
}

/** 链上的下一项/上一项（到头为 null） */
function railStep(dir) {
  return railNeighbors(railList(), railPos())[dir]
}

function onTouchMove(e) {
  const t = e.touches[0] || (e.changedTouches && e.changedTouches[0])
  if (!t) return
  // ⚠️ touchmove 必须非 passive 才能 preventDefault（锁横向前不拦，竖滑照常滚）。
  //    上一版是「手势开始时往 window 上挂非 passive 监听」—— 那要晚一帧才生效，
  //    第一段位移会被浏览器按默认行为吃掉（表现就是「起手有点黏、发顿」）。
  //    现在监听直接挂在视口上（模板里 @touchmove 不带 .passive 就是非 passive），
  //    从第一帧就拦得住，滑动更跟手。
  step(t.clientX, t.clientY, e)
}

function onTouchEnd() {
  finish()
}

function detach() {
  // 监听已改为模板绑定（挂在视口元素上），这里不再有 window 监听需要摘。
  // 保留这个函数名是为了让 finish()/卸载路径的语义清楚：一次手势结束 = 状态清空。
}

function begin(x, y, target) {
  if (describeTarget(target)) return
  // 上一层手势没走完（例如上一次落位弹簧还在跑、但 touchcancel 没来的极端情况）：
  // 先把位移归零，否则新一划会接着上一划的残余位移，看起来像"自己漂"。
  // ⚠️ 2026-10-29：以前只在 `dx || dxInner` 为真时才清；可 DOM 上的 transform 是**直接写**的，
  //    内存里的 dx 已经是 0 而 DOM 还留着上次的位移时（落位被打断 / 翻页收尾没跑完），
  //    这个条件就漏掉 → 整页歪着、下一划从错位起算。现在无条件重画一次，代价是一次 style 写入。
  if (dx || dxInner) {
    dx = 0; dxInner = 0
  }
  applyTransform(); applyInnerTransform(); paintInner()
  // ⚠️ 触点上**不判归属**：真的"谁动"要等方向锁定后看链上的下一项（见 step）。
  //    这里只做"上一划的弹簧停掉"这类与方向无关的收尾。
  level = null
  stopSpring()
  stopInnerSpring()
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
    // 方向已经确定才谈"归属"：链上的下一项是别的页就整页滑，是同一页的另一段就只动内容。
    // 只在这里判一次：拖动中途不再复查，否则手指一抖就会整页/内容来回切（会看见跳动）。
    level = pickLevel(moveX, e && e.target)
    // 内外两种邻居都预挂：整页滑时需要旁边那页有内容，分段滑时需要内层轨道已铺好
    padNeighbors()
    padInnerNeighbors()
  } else if (axis === 'x' && axisYields(axis, moveX, moveY)) {
    // 已锁定横向，但手指其实是在竖着滚（纵向位移明显压过横向）→ 还回去，页面照常滚。
    // 注意这里**不能 stopPropagation / 不 preventDefault**：这一划要交回浏览器去滚页面。
    dx = 0
    dxInner = 0
    applyTransform()
    paintInner()
    applyInnerTransform()
    start = { x, y, t: performance.now() } // 归位后重新起算，避免下一帧又立刻判回"竖"
    axis = null
    return
  }
  // 锁定横向后才阻断浏览器默认行为 —— 这时这划已经归我们了，也避免浏览器把触摸序列取消掉
  if (e && e.cancelable) e.preventDefault()

  const now = performance.now()
  samples.push({ x, t: now })
  if (samples.length > 8) samples.shift()

  if (level === 'inner') {
    // 到头判定读**扁平链**：整理页最后一个分段（快照）再往左，链上还有「订阅」，
    // 按内层自己的邻居算会被判成"到头"然后压成橡皮筋 —— 那就是用户说的"滑不动了"。
    const nb = { prev: railStep('prev'), next: railStep('next') }
    dxInner = rubberBand(moveX, { canPrev: !!nb.prev, canNext: !!nb.next })
    // 整页跟着一起动（父级接管），内层再多横移一页宽 —— 于是分段器本身也在滑
    paintInner()
    if (!rafId) {
      rafId = requestAnimationFrame(() => { rafId = 0; applyInnerTransform() })
    }
    return
  }

  // 外层的"到头"同样读扁平链：最后一页之前还有它的分段可走，不该一上手就压成橡皮筋
  const nbOuter = { prev: railStep('prev'), next: railStep('next') }
  dx = rubberBand(moveX, { canPrev: !!nbOuter.prev, canNext: !!nbOuter.next })
  if (!rafId) {
    rafId = requestAnimationFrame(() => { rafId = 0; applyTransform() })
  }
}

function clearSwiping() {
  delete document.documentElement.dataset.swiping
}

function finish() {
  if (!start) { dragging.value = false; detach(); clearSwiping(); return }
  detach()
  const wasAxisX = axis === 'x'
  const wasLevel = level
  const wasDxInner = dxInner
  start = null
  axis = null
  level = null
  /**
   * ⚠️⚠️ 2026-10-29 关键修复：「滑过去但没到下一页」的真正原因。
   *
   * 老代码在弹簧**开始之前**就把 dx 归零了（runSpring 的起点取的是全局 dx）：
   *   ① `dx = 0` → runSpring 的 from.x 变成 0 → 弹簧从 0 弹到 ±一页宽，
   *      而不是从手指松开的位置接着走；`settleDuration(remaining)` 又按 remaining 算，
   *      时间对不上，于是中途就被 springSettled 判停 → **落不到位**。
   *   ② 这条 `dx = 0` 是**无条件**执行的：即使后面走到了弹回分支，位移也已经先被清掉，
   *      表现就是「滑完松手，页面自己跑回原位、没有任何跟手的收尾」。
   *   ③ `dx = 0` 却没有 applyTransform()，DOM 上还留着上次拖动写进去的角度/位移，
   *      下一次手势 begin() 看到 dx 为 0 又不清 DOM —— 错位被固化。
   *
   * 现在：起点保留（从松手位置继续），只在**落位完成**时归零。
   */
  if (wasLevel === 'inner') {
    dragging.value = false
    if (!wasAxisX) {
      dxInner = 0; dx = 0
      applyInnerTransform(); paintInner()
      clearSwiping()
      return
    }
    const velocity = velocityFrom(samples, performance.now())
    const width = viewportWidth()
    const nb = { prev: railStep('prev'), next: railStep('next') }
    const dirIn = decideCommit({
      dx: wasDxInner, width, velocity: Math.abs(velocity) < 0.05 ? 0 : velocity,
      canPrev: !!nb.prev, canNext: !!nb.next,
    })
    if (!dirIn) {
      const backMs = settleDuration(Math.abs(wasDxInner), velocity)
      runInnerSpring(0, velocity, SPRING_ZETA_BACK, backMs, () => {
        dxInner = 0; dx = 0
        applyInnerTransform(); paintInner()
        clearSwiping()
      })
      return
    }
    commitInner(dirIn, velocity, width, wasDxInner)
    return
  }
  if (!wasAxisX) {
    dx = 0; applyTransform(); dragging.value = false; clearSwiping(); return
  }

  dragging.value = false

  const velocity = velocityFrom(samples, performance.now())
  const width = viewportWidth()
  // 到头判定读扁平链（见 step 里的说明）
  const nbOuter = { prev: railStep('prev'), next: railStep('next') }
  const dir = decideCommit({
    dx, width, velocity: Math.abs(velocity) < 0.05 ? 0 : velocity,
    canPrev: !!nbOuter.prev, canNext: !!nbOuter.next,
  })

  if (!dir) {
    // 弹回：**欠阻尼**弹簧 —— 越过原位一点点再回来，就是用户要的"阻尼感"
    const backMs = settleDuration(Math.abs(dx), velocity)
    runSpring(0, velocity, SPRING_ZETA_BACK, backMs, () => {
      dx = 0
      applyTransform()
      paintInner()
      clearSwiping()
    })
    return
  }

  // 外层落位 = 链上的下一项。它在目标页上**可能是第一个分段**（曲库 → 整理/补全），
  // 见下面「换页那一划落在哪一段」的说明：不能沿用目标页上次停留的段，
  // 否则中间的段会被跳过（曲库 → 修复 → 快照）。
  const railTarget = railStep(dir)
  const target = railTarget ? railTarget.view : (dir === 'next' ? neighbors.value.next : neighbors.value.prev)
  const remaining = width - Math.abs(dx)
  const toX = dir === 'next' ? -width : width
  // 翻页：**临界阻尼** —— 不回弹。回弹会让"下一页"先露一条边再退回来，看着像 bug
  runSpring(toX, velocity, SPRING_ZETA_TURN, settleDuration(remaining, velocity), async () => {
    /**
     * 落位后的收尾（2026-10-29 修「滑过去没到下一页」）：
     *
     * 老写法是 `dx = 0` **然后** mountView(target) —— 看着对，其实有坑：
     *   · mountView 只是改 row（响应式），若 target 是**新挂载**的页，
     *     新下标要等 nextTick 后 DOM 才对齐，而 dx 已经被清成 0，
     *     于是那段时间轨道停在「旧下标 × 页宽」= **视觉上弹回原来那页**。
     *   · 更糟的是这里的 `await nextTick()` 里夹着 `requestAnimationFrame`，
     *     nextTick 本身不保证在下一帧之前完成；页面被别的更新插队时，
     *     transform 会有一帧停在旧位置 —— 用户看到的就是「滑了一下又回去了」。
     *
     * 现在：**先把目标页挂上、并把下标同步好，再一次写 transform**，
     * 中间不出现「新行 + 旧位移」的错位帧；弹簧终点的 -(targetIndex×页宽) 与
     * 落位后的 -(rowIndex×页宽) 完全重合，所以切换是连续的、不会有一下回弹。
     */
    dx = 0
    mountView(target)
    // 下标在 mountView 之后同步算一次：不等 computed 被 nextTick 刷新，避免上面那种错位帧
    rowIndex.value = Math.max(0, row.value.indexOf(target))
    /**
     * 换页那一划落在**目标页的链上第一项**，不是它上次停留的那一段。
     *
     * 用户 2026-10-29 的示例：曲库 → 补全 → 修复 → 音质 → 快照 → 订阅。
     * 若这里沿用目标页记忆的段（滑动进入看过的页保留上次停留的段），
     * 从曲库左滑一次就可能直接落到「整理/修复」，中间那几段被整段跳过 ——
     * 链就不是链了。所以：
     *   · 换页（跨页）→ 落该页第一段（曲库 → 整理/补全），后续几划再逐段往左；
     *   · 页内换段（不换页）→ 由 commitInner 负责，那才是"逐段"的步进。
     * 点按导航（goView）仍然按"看过就回上次那段"走 —— 那是点按语义，和滑动不同。
     */
    if (railTarget && railTarget.segment) emit('update:segment', railTarget.segment)
    emit('update:current', target)
    // 立刻按新下标重画一次（若 target 尚未进 DOM，宽度可能还是 0，下面 applyTransform 会兜住）
    applyTransform()
    await nextTick()
    requestAnimationFrame(() => {
      applyTransform()
      clearSwiping()
      swallowNextClick()
    })
  })
}

/**
 * 内层落位（统一滑轨版，2026-10-29）。
 *
 * 一划落定后「链上那一项」可能只差一段，也可能同时换页（整理页最后一个分段的下一项
 * 就是「订阅」）。为了不出现「滑到一半换页」的跳变，统一成：
 *   1. 先把内层滑到「这一划本该停的格子」并 emit 分段；
 *   2. 若目标还在同一页 → 到此为止（只有内容动了，页头的小导航没动）；
 *   3. 若目标已经是别的页 → 紧接着用外层轨道的那一段动画把整页送过去
 *      （内层先归位到 0，随后整页横滑，层次分明、不会两条轨道一起飞）。
 *
 * @param {'prev'|'next'} dirIn 方向
 * @param {number} velocity 松手速度
 * @param {number} width 页宽
 * @param {number} fromX 松手时的内层位移（弹簧从这里接着走）
 */
function commitInner(dirIn, velocity, width, fromX) {
  const item = railStep(dirIn)
  const toXIn = dirIn === 'next' ? -width : width
  runInnerSpring(toXIn, velocity, SPRING_ZETA_TURN, settleDuration(width - Math.abs(fromX), velocity), async () => {
    dxInner = 0; dx = 0
    if (!item) {
      applyInnerTransform(); paintInner(); clearSwiping()
      return
    }
    const sameView = item.view === props.current
    const nextSeg = sameView ? item.segment : (innerSegsOf(item.view)[0] || '')
    // 内层轨道里这一项可能还没装（目标页的分段表刚换过）——装上它，否则拖不到那一格
    mountInnerSeg(nextSeg)
    innerRowIndex.value = Math.max(0, innerRow.value.indexOf(nextSeg))
    viewportRef.value && (viewportRef.value.dataset.railFixed = '1')
    paintInner()
    if (sameView) {
      emit('update:segment', nextSeg)
      await nextTick()
      requestAnimationFrame(() => {
        innerRowIndex.value = Math.max(0, innerRow.value.indexOf(nextSeg))
        applyInnerTransform()
        requestAnimationFrame(() => {
          if (viewportRef.value) viewportRef.value.dataset.railFixed = ''
          paintInner()
          clearSwiping()
          swallowNextClick()
        })
      })
      return
    }
    // 同页已经滑到链尾，再往前就是下一页：先把本页内层归位，再把整页送过去
    emit('update:segment', nextSeg)
    innerRowIndex.value = 0
    applyInnerTransform(); paintInner()
    if (viewportRef.value) viewportRef.value.dataset.railFixed = ''
    mountView(item.view)
    rowIndex.value = Math.max(0, row.value.indexOf(item.view))
    emit('update:current', item.view)
    applyTransform()
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
