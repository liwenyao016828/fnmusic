<template>
  <!--
    溢出菜单（⋯）：把「低频率但必须存在」的动作从版面上收起来。

    为什么要有这个组件（2026-10-19 用户口径）：
      「一个页面里一样有好多功能，按钮，选项，卡片，都是一窝蜂挤在一起……一些不必要的
       按钮和交互可以通过现代化方式实现交互显示，而不是挤在一个页面。」
    结论不是「删功能」，而是**分层**：高频动作留在版面上（1 个实心主按钮），
    其余进 ⋯ 菜单。所以本组件的验收标准是：触发键只有一个 32px 的圆形图标按钮，
    菜单只在点开时存在（`v-if`，不占布局）。

    视觉口径与全站一致：12px 圆角、发丝线、毛玻璃、大扩散低不透明度阴影；
    菜单项 34px 高、13px 字、左侧 16px 线性图标；危险项只在 hover 时上红底。

    用法：
      <OverflowMenu
        :items="[{ key: 'rescan', label: '重新扫描', icon: RotateCw }, { key: 'clear', label: '清空', icon: Trash2, danger: true }]"
        @select="onMenu"
      />
    传 `label` 会把触发器从左图标按钮变成「文字 + 图标」的轻量按钮（用于「更多筛选」这类
    需要一点可发现性的入口）。
  -->
  <div ref="rootRef" class="relative shrink-0">
    <button
      type="button"
      class="ys-press flex items-center gap-1.5 rounded-[8px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
      :class="label ? 'h-8 px-2.5 text-[12px] font-medium' : 'h-8 w-8 justify-center'"
      :aria-expanded="open"
      aria-haspopup="menu"
      :title="title || (label || '更多操作')"
      @click.stop="toggle"
    >
      <component :is="iconComponent" v-if="label" class="h-3.5 w-3.5" :stroke-width="1.8" />
      <span v-if="label">{{ label }}</span>
      <MoreHorizontal v-else class="h-4 w-4" :stroke-width="1.8" />
    </button>

    <Transition
      enter-active-class="transition duration-[160ms] ease-[cubic-bezier(0.32,0.72,0,1)]"
      enter-from-class="opacity-0 scale-[0.97] -translate-y-1"
      enter-to-class="opacity-100 scale-100 translate-y-0"
      leave-active-class="transition duration-100 ease-in"
      leave-from-class="opacity-100 scale-100"
      leave-to-class="opacity-0 scale-[0.98]"
    >
      <div
        v-if="open"
        role="menu"
        class="absolute right-0 top-[calc(100%+6px)] z-50 w-[208px] origin-top-right overflow-hidden rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-surface)] p-1 shadow-[0_16px_40px_rgba(0,0,0,0.14)] ring-1 ring-black/[0.04]"
      >
        <button
          v-for="item in items"
          :key="item.key"
          type="button"
          role="menuitem"
          :disabled="item.disabled"
          class="flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-[7px] text-left text-[13px] transition-colors disabled:opacity-40"
          :class="item.danger
            ? 'text-rose-600 hover:bg-rose-50'
            : 'text-[var(--ys-text)] hover:bg-black/[0.05]'"
          :title="item.hint || ''"
          @click.stop="pick(item)"
        >
          <component :is="item.icon" v-if="item.icon" class="h-4 w-4 shrink-0 opacity-70" :stroke-width="1.8" />
          <span class="min-w-0 flex-1 truncate">{{ item.label }}</span>
          <kbd v-if="item.shortcut" class="shrink-0 text-[10px] text-[var(--ys-text-aux)]">{{ item.shortcut }}</kbd>
        </button>
        <div v-if="grouped" class="my-1 h-px bg-[var(--ys-border)]"></div>
      </div>
    </Transition>
  </div>
</template>

<script setup>
import { ref, computed, onBeforeUnmount, onMounted } from 'vue'
import { MoreHorizontal } from 'lucide-vue-next'

const props = defineProps({
  items: { type: Array, default: () => [] },
  label: { type: String, default: '' },
  icon: { type: [Object, Function], default: null },
  title: { type: String, default: '' },
})

const emit = defineEmits(['select'])

const open = ref(false)
const rootRef = ref(null)
const iconComponent = computed(() => props.icon || MoreHorizontal)
// 有一条分隔线就够：列表里出现 danger 项时，危险动作与常规动作分开
const grouped = computed(() => props.items.some((i) => i.danger))

function toggle() {
  open.value = !open.value
}

function pick(item) {
  if (item.disabled) return
  open.value = false
  emit('select', item.key, item)
}

function onDocPointer(e) {
  if (!open.value) return
  if (rootRef.value && !rootRef.value.contains(e.target)) open.value = false
}
function onKey(e) {
  if (e.key === 'Escape') open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointer, true)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocPointer, true)
  document.removeEventListener('keydown', onKey)
})
</script>
