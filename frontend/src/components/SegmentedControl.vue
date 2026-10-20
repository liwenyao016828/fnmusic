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
  -->
  <div
    class="inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-[var(--ys-radius-control)] bg-[var(--ys-surface-sunken)] p-1 no-scrollbar"
    role="tablist"
  >
    <button
      v-for="seg in segments"
      :key="seg.id"
      role="tab"
      :aria-selected="seg.id === modelValue"
      class="ys-press flex shrink-0 items-center gap-1.5 rounded-[8px] px-3 py-1.5 text-[12px] font-medium"
      :class="
        seg.id === modelValue
          ? 'bg-[var(--ys-surface)] text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
          : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]'
      "
      @click="emit('update:modelValue', seg.id)"
    >
      <component
        :is="seg.icon"
        v-if="seg.icon"
        class="h-3.5 w-3.5 shrink-0"
        :class="seg.id === modelValue ? 'text-[var(--ys-accent)]' : ''"
        :stroke-width="1.8"
      />
      <span class="whitespace-nowrap">{{ seg.name }}</span>
    </button>
  </div>
</template>

<script setup>
defineProps({
  /** [{ id, name, icon? }] —— 通常直接来自 services/navViews.js 的 segmentsOf() */
  segments: { type: Array, default: () => [] },
  modelValue: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])
</script>
