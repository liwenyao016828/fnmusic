<template>
  <!--
    页内分段器：把一个顶层页分成几段（如「发现 / 榜单」、「日志 / 设置」）。

    存在的理由（2026-10-15 重构）：次级页面以前只有两条路 ——
      要么塞进导航栏当第 7、第 8 个平级项（导航越长越难扫，用户已经反馈过「挤」），
      要么整个页面替换掉主内容区（用户明确不喜欢：「不是位置换回来」，他要的是
      「次要入口做成抽屉/分段这种不打断主流程的呈现方式」）。
    分段器走第二条路的轻量版：位置固定、切换零跳转、深链仍可直达某一段。

    外观刻意与顶栏导航同一套（白底 + 强调色文字 + shadow-sm），
    这样「切页」和「切段」在视觉上是一家的，用户不需要学两套。

    ── 自适应（2026-10-26 用户：「补全整理内的导航栏根据屏幕大小适配图标与文字精简效果」）──
    段位一多（补全整理有 5 段）+ 右边还要挂目录胶囊时，窄屏上光靠横向滚动等于
    「大部分入口看不见」。所以这里按容器宽度分三档降级，宽到窄依次：

      1. 图标 + 全名         —— 默认，最清楚；
      2. 图标 + 短名         —— 全名放不下时，用 segment.short（「补全」「查重」…）；
      3. 只留图标            —— 连短名都放不下时，图标 + title 气泡兜底。

    判定用 ResizeObserver 实测**容器本身**的宽度，而不是 window 断点：
    这个分段器同时出现在整页（宽）和抽屉/窄栏（窄）里，媒体查询看的是视口，
    不知道自己是多宽，会判错。放下就不用缩，是「按可用空间降级」不是「一律缩小」。
  -->
  <div ref="boxEl" class="flex min-w-0 items-center gap-2">
    <div
      class="inline-flex min-w-0 max-w-full items-center gap-0.5 overflow-x-auto rounded-[var(--ys-radius-control)] bg-[var(--ys-surface-sunken)] p-1 no-scrollbar"
      role="tablist"
    >
      <button
        v-for="seg in segments"
        :key="seg.id"
        role="tab"
        :aria-selected="seg.id === modelValue"
        :title="seg.desc || seg.name"
        class="ys-press flex shrink-0 items-center gap-1.5 rounded-[8px] text-[12px] font-medium"
        :class="labelOf(seg) ? 'px-3 py-1.5' : 'px-2 py-1.5'"
        @click="emit('update:modelValue', seg.id)"
      >
        <component
          :is="seg.icon"
          v-if="seg.icon"
          class="h-3.5 w-3.5 shrink-0"
          :class="seg.id === modelValue ? 'text-[var(--ys-accent)]' : ''"
          :stroke-width="1.8"
        />
        <span v-if="labelOf(seg)" class="whitespace-nowrap">{{ labelOf(seg) }}</span>
      </button>
    </div>
    <!--
      2026-10-25 用户：「元数据补全页面内的路径展示，迁移到五个分段导航行的右边。可以点击进行目录选择。」
      分段器自己是「内联 flex」的，别处的调用方想在同一行右侧挂点东西（这里是曲库目录），
      就走这个槽位 —— 内容右对齐，窄屏时整行换行、不说错位。
    -->
    <div v-if="$slots.trail" ref="trailEl" class="ml-auto flex min-w-0 shrink items-center gap-2 pl-1">
      <slot name="trail" />
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({
  /** [{ id, name, short?, desc?, icon? }] —— 通常直接来自 services/navViews.js 的 segmentsOf() */
  segments: { type: Array, default: () => [] },
  modelValue: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])

const boxEl = ref(null)
const trailEl = ref(null)
/**
 * 可用宽度（px）。
 *
 * ⚠️ 这份状态必须在 `<script setup>` 里声明 —— 它**每个组件实例一份**。
 * 曾经把它写成模块级 `const railWidth = ref(0)`（模块作用域，全应用共享一个），
 * 结果是所有页面共用「最后一次测到的那个宽度」：补全整理页量出来很窄，
 * 切到发现 / 下载页时它们也照样只用图标，用户以为「一个页面用图标，所有页面都是图标」。
 *
 * ⚠️ 2 必须量**外层盒子**（boxEl），不能量按钮条本身 —— 按钮条是 `inline-flex`，
 * 宽度由内容撑开（放得下就窄、放不下才顶到 max-w-full）。量它等于
 * 「按当前已渲染的文字宽度判断要不要显示文字」，永远自洽地错：
 * 某次渲染成纯图标 → 它收缩成图标宽度 → 下次继续判「放不下」→ 锁死在图标档。
 * 用户当场看出：「明明很宽啊」「和下载怎么顶部导航也是只有图标了」。
 * 外层 `flex` 盒子撑满父容器、且**不受按钮内容影响**，才是真正的可用空间。
 */
const railWidth = ref(0)

/** 按钮条与 trail 槽位之间的 gap-2 */
const SIBLING_GAP = 8

let ro = null

/** 量一次并写回。trail 槽位（如曲库目录胶囊）占掉的宽度要扣掉，否则会按整宽算 */
function measure() {
  const box = boxEl.value
  if (!box) return
  const total = box.clientWidth || 0
  if (!total) return
  let used = 0
  const trail = trailEl.value
  if (trail) {
    // ⚠️ 只扣 trail 的**实际宽度**，绝不能把 margin-left 也算进去。
    // trail 带 `ml-auto`，它的 margin-left 是 flex 把剩余空间全推给它的结果
    // （实测撑到 611px），那是**空白**，不是被谁占用。
    // 加上它 → 可用宽度从 1040 掉到 166 → 5 段永远判成「放不下」→ 只剩图标，
    // 用户看到的就是「明明很宽啊，怎么只有图标」（2026-10-26）。
    used = trail.getBoundingClientRect().width + SIBLING_GAP
  }
  railWidth.value = Math.max(0, total - used)
}

onMounted(() => {
  measure()
  // 首帧兜底：`v-show` 隐藏的分段器在挂载时父容器可能是 display:none，
  // clientWidth 读出来是 0；等它被显示时 ResizeObserver 未必会回调
  // （0 → 有宽度确实会回调，但如果第一次就读到了错误值则不会）。
  // 下一帧再量一次，把「挂了但还没布局好」的情况兜住。
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(measure)
  if (typeof ResizeObserver === 'undefined') return
  ro = new ResizeObserver(() => measure())
  if (boxEl.value) ro.observe(boxEl.value)
  // 目录胶囊是异步拿到的（要先请求曲库路径），它变宽时要重算
  if (trailEl.value) ro.observe(trailEl.value)
})
onBeforeUnmount(() => {
  if (ro) { ro.disconnect(); ro = null }
})

/**
 * 估算每个按钮的像素宽度 —— 不必精确到 1px，只需要「放不放得下」这个判断是对的。
 * 中文约 12px/字、西文约 6.5px/字（12px 字号 + 左右各 12px 内边距 + 图标与间距）。
 */
function textWidth(text) {
  let px = 0
  for (const ch of String(text || '')) px += /[\u3400-\u9fff]/.test(ch) ? 12 : 6.5
  return px
}
function btnWidth(seg, label) {
  const icon = seg.icon ? 14 + 6 : 0 // h-3.5 + gap-1.5
  return textWidth(label) + icon + (label ? 24 : 16)
}

/** 当前该显示哪种文字：'' = 只要图标 */
const labelMode = computed(() => {
  const w = railWidth.value
  if (!w) return 'full'
  const gap = (props.segments.length - 1) * 2 + 8 // gap-0.5 × 段间距 + p-1 内边距
  const sumOf = (key) => props.segments.reduce((n, s) => n + btnWidth(s, key(s)), 0) + gap
  if (sumOf((s) => s.name) <= w) return 'full'
  if (sumOf((s) => s.short || s.name) <= w) return 'short'
  return 'icon'
})

function labelOf(seg) {
  if (labelMode.value === 'icon') return ''
  if (labelMode.value === 'short') return seg.short || seg.name || ''
  return seg.name || ''
}
</script>
