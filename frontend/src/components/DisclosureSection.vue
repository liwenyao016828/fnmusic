<template>
  <!--
    折叠分区（Disclosure Section）：设置类页面用它把「一堆常驻控件」压成一行摘要。

    规则（2026-10-19 用户口径：不要一窝蜂挤在页面上）：
      · 默认**全部收起**，只露标题 + 一句当前值摘要（`summary`）；
      · 一次通常只展开一组（由父级管 `openKey`），版面永远只有一块是展开的；
      · 展开动画用 grid-template-rows 0fr→1fr，不写 max-height 魔法数字。

    用法：
      <DisclosureSection title="下载音质" summary="FLAC 24bit · 优先无损" :open="openKey === 'quality'" @toggle="toggle('quality')">
        ...表单...
      </DisclosureSection>
  -->
  <section class="overflow-hidden rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white">
    <button
      type="button"
      class="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-black/[0.015] sm:px-5"
      :aria-expanded="open"
      :title="hint"
      @click="emit('toggle')"
    >
      <span v-if="icon" class="grid h-8 w-8 shrink-0 place-items-center rounded-[8px] bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]">
        <component :is="icon" class="h-4 w-4" :stroke-width="1.8" />
      </span>
      <span class="min-w-0 flex-1">
        <span class="block truncate text-[14px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">{{ title }}</span>
        <span v-if="summary" class="mt-0.5 block truncate text-[11px] text-[var(--ys-text-muted)]">{{ summary }}</span>
      </span>
      <slot name="actions" />
      <ChevronDown
        class="h-4 w-4 shrink-0 text-[var(--ys-text-aux)] transition-transform duration-[260ms] ease-[cubic-bezier(0.32,0.72,0,1)]"
        :class="open ? 'rotate-180' : ''"
        :stroke-width="1.8"
      />
    </button>

    <div
      class="grid transition-[grid-template-rows] duration-[260ms] ease-[cubic-bezier(0.32,0.72,0,1)]"
      :class="open ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
    >
      <div class="overflow-hidden">
        <div class="border-t border-[var(--ys-border)] px-4 py-4 sm:px-5">
          <slot />
        </div>
      </div>
    </div>
  </section>
</template>

<script setup>
import { ChevronDown } from 'lucide-vue-next'

defineProps({
  title: { type: String, required: true },
  summary: { type: String, default: '' },
  icon: { type: [Object, Function], default: null },
  /** 悬停说明：为什么这一组要这么设计（不占版面） */
  hint: { type: String, default: '' },
  open: { type: Boolean, default: false },
})
const emit = defineEmits(['toggle'])
</script>
