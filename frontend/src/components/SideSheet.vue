<template>
  <!--
    右侧抽屉（Sheet）：承载「次要但完整」的内容 —— 高级选项、导入流程、行详情。

    为什么不弹模态框：模态框会打断当前页面、还必须先关掉才能继续看；
    抽屉是从右边滑出的一层，背景页面还在，用户随时能点回去。
    （用户 2026-09-28 已经定过这个口径：「次要/工具类入口应做成从右侧滑出的抽屉」。）

    与既有 ApiDrawer（左滑）方向相反是故意的：那是品牌入口，这是工具入口。
  -->
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-200 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="modelValue"
        class="fixed inset-0 z-[60] bg-black/25 backdrop-blur-[2px]"
        @click="close"
      ></div>
    </Transition>

    <Transition
      enter-active-class="transition-transform duration-[320ms] ease-[cubic-bezier(0.32,0.72,0,1)]"
      enter-from-class="translate-x-full"
      enter-to-class="translate-x-0"
      leave-active-class="transition-transform duration-200 ease-[cubic-bezier(0.4,0,1,1)]"
      leave-from-class="translate-x-0"
      leave-to-class="translate-x-full"
    >
      <aside
        v-if="modelValue"
        class="fixed inset-y-0 right-0 z-[61] flex w-[min(420px,100vw)] flex-col border-l border-[var(--ys-border)] bg-[var(--ys-surface-glass)] shadow-[-24px_0_60px_rgba(0,0,0,0.16)] backdrop-blur-2xl backdrop-saturate-150"
        role="dialog"
        aria-modal="true"
      >
        <!-- 头部：标题 + 一句说明 + 关闭 -->
        <header class="flex shrink-0 items-start gap-3 border-b border-[var(--ys-border)] px-5 pb-4 pt-safe">
          <div class="min-w-0 flex-1 pt-4">
            <h2 class="truncate text-[15px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">{{ title }}</h2>
            <p v-if="subtitle" class="mt-0.5 truncate text-[11px] text-[var(--ys-text-muted)]">{{ subtitle }}</p>
          </div>
          <button
            type="button"
            class="mt-3 grid h-8 w-8 shrink-0 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.06] hover:text-[var(--ys-text)]"
            title="关闭"
            @click="close"
          >
            <X class="h-4 w-4" :stroke-width="2" />
          </button>
        </header>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <slot />
        </div>

        <footer v-if="$slots.footer" class="shrink-0 border-t border-[var(--ys-border)] px-5 py-3 pb-safe">
          <slot name="footer" />
        </footer>
      </aside>
    </Transition>
  </Teleport>
</template>

<script setup>
import { watch, onBeforeUnmount } from 'vue'
import { X } from 'lucide-vue-next'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '' },
  subtitle: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])

function close() {
  emit('update:modelValue', false)
}

function onKey(e) {
  if (e.key === 'Escape' && props.modelValue) close()
}
watch(
  () => props.modelValue,
  (v) => {
    if (v) document.addEventListener('keydown', onKey)
    else document.removeEventListener('keydown', onKey)
  },
)
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>
