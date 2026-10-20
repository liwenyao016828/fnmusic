<template>
  <!--
    一行式统计（Stat Line）：替换掉「四张指标大卡」。

    四张大卡占了首屏 1/5 的高度，只为说四个数字；而这些数字是**背景信息**，
    不是这一页要做的事。压成一行后，页面第一眼看到的是内容，不是仪表盘。

    用法：
      <StatLine :items="[{ label: '全部', value: 266 }, { label: '失败', value: 1, dot: 'bg-rose-500' }]" />
  -->
  <div class="flex flex-wrap items-center gap-x-5 gap-y-1.5">
    <span
      v-for="it in items"
      :key="it.label"
      class="inline-flex items-baseline gap-1.5"
      :class="it.muted ? 'opacity-50' : ''"
    >
      <span v-if="it.dot" class="mb-[1px] h-1.5 w-1.5 shrink-0 self-center rounded-full" :class="it.dot"></span>
      <b class="text-[13px] font-semibold tabular-nums tracking-[-0.01em] text-[var(--ys-text)]">{{ it.value }}</b>
      <span class="text-[11px] text-[var(--ys-text-muted)]">{{ it.label }}</span>
    </span>
  </div>
</template>

<script setup>
defineProps({
  items: { type: Array, default: () => [] },
})
</script>
