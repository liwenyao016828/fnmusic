<template>
  <!--
    统一样式的下拉选择框（**自绘面板**，不用原生 <select>）。

    为什么要自绘：原生 <select> 的**触发框**可以用 appearance-none 改样式，
    但**点开后的弹出面板**完全由浏览器/系统绘制 —— 方块直角、系统字体、蓝色高亮，
    放在这套圆角 emerald 界面里非常突兀（用户反馈：「下载设置，下拉感觉有点突兀」）。
    所以这里改成 button + 自己画的列表。

    用法不变：
      <SelectField v-model="quality" label="默认音质" :options="[{value:'highest',label:'最高音质'}]" hint="…" />

    实现要点：
      · 面板 <Teleport to="body"> + fixed 定位 —— 表格行、滚动容器里都不会被裁切；
      · 空间不够时自动向上弹；滚动/缩放时重新定位；
      · 支持键盘：↑↓ 移动、Enter/Space 选中、Esc 关闭；
      · 点击外部关闭（用捕获阶段 pointerdown，比 click 更早、更可靠）。
  -->
  <div class="block min-w-0">
    <span v-if="label" class="mb-1 block text-[11px] text-gray-400">{{ label }}</span>

    <button
      ref="triggerRef"
      type="button"
      :disabled="disabled"
      :title="hint || undefined"
      :aria-expanded="open"
      aria-haspopup="listbox"
      :class="[
        'flex w-full items-center justify-between gap-1 rounded-lg border border-gray-200 bg-white text-left text-gray-700 transition-colors',
        'hover:border-emerald-300 focus:border-emerald-400 focus:outline-none focus:ring-2 focus:ring-emerald-400/20',
        'disabled:cursor-not-allowed disabled:opacity-50',
        compact ? 'py-1 pl-2 pr-1.5 text-[11px]' : 'py-1.5 pl-2.5 pr-2 text-[12px]',
      ]"
      @click="toggle"
      @keydown="onKeydown"
    >
      <span class="min-w-0 flex-1 truncate">{{ currentLabel }}</span>
      <ChevronDown
        :class="[
          'shrink-0 text-gray-400 transition-transform duration-150',
          open && 'rotate-180',
          compact ? 'w-3 h-3' : 'w-3.5 h-3.5',
        ]"
      />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="panelRef"
        role="listbox"
        class="fixed z-[70] overflow-y-auto rounded-xl border border-gray-200 bg-white p-1 shadow-xl ring-1 ring-black/5"
        :style="panelStyle"
      >
        <button
          v-for="(o, i) in options"
          :key="String(o.value)"
          type="button"
          role="option"
          :aria-selected="String(o.value) === String(modelValue)"
          :class="[
            'flex w-full items-center justify-between gap-2 rounded-lg px-2.5 py-1.5 text-left text-[12px] transition-colors',
            String(o.value) === String(modelValue)
              ? 'bg-emerald-50 font-semibold text-emerald-700'
              : 'text-gray-700 hover:bg-gray-50',
            i === activeIndex && 'ring-1 ring-inset ring-emerald-200',
          ]"
          @click="pick(o.value)"
          @mouseenter="activeIndex = i"
        >
          <span class="min-w-0 flex-1 truncate">{{ o.label }}</span>
          <Check v-if="String(o.value) === String(modelValue)" class="h-3.5 w-3.5 shrink-0 text-emerald-500" />
        </button>
      </div>
    </Teleport>

    <span v-if="hint && showHint && !compact" class="mt-1 block text-[10px] leading-snug text-gray-400">{{ hint }}</span>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Check, ChevronDown } from 'lucide-vue-next'

const props = defineProps({
  modelValue: { type: [String, Number], default: '' },
  options: { type: Array, default: () => [] },
  label: { type: String, default: '' },
  hint: { type: String, default: '' },
  /** 是否在控件下方显示 hint（表格里空间紧张时可关掉，只留 title 提示） */
  showHint: { type: Boolean, default: true },
  /** 数值型绑定（如重试次数、间隔秒数） */
  numeric: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  /** 紧凑模式：更小的内边距与字号，用于表格行内（不渲染 label / hint） */
  compact: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue'])

const open = ref(false)
const activeIndex = ref(0)
const triggerRef = ref(null)
const panelRef = ref(null)
const panelStyle = ref({})

/** 面板最大高度；实际高度取「可用空间」与它的较小值 */
const PANEL_MAX = 260
/** 触发框与面板之间的间隙 */
const GAP = 4

const currentLabel = computed(() => {
  const hit = props.options.find((o) => String(o.value) === String(props.modelValue))
  return hit ? hit.label : (props.options[0]?.label ?? '')
})

function sameValue(a, b) {
  return String(a) === String(b)
}

/** 按触发框位置摆放面板（fixed 坐标），空间不够就向上弹 */
function placePanel() {
  const el = triggerRef.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const below = window.innerHeight - r.bottom - GAP
  const above = r.top - GAP
  // 下方连一行都放不下、且上方更宽裕 → 向上弹
  const openUp = below < 120 && above > below
  panelStyle.value = {
    left: `${Math.round(r.left)}px`,
    width: `${Math.round(r.width)}px`,
    ...(openUp
      ? { bottom: `${Math.round(window.innerHeight - r.top + GAP)}px`, maxHeight: `${Math.max(80, Math.min(PANEL_MAX, above))}px` }
      : { top: `${Math.round(r.bottom + GAP)}px`, maxHeight: `${Math.max(80, Math.min(PANEL_MAX, below))}px` }),
  }
}

function show() {
  if (props.disabled) return
  const idx = props.options.findIndex((o) => sameValue(o.value, props.modelValue))
  activeIndex.value = idx >= 0 ? idx : 0
  open.value = true
  nextTick(placePanel)
}

function close() {
  open.value = false
}

function toggle() {
  open.value ? close() : show()
}

function pick(v) {
  emit('update:modelValue', props.numeric ? Number(v) : v)
  close()
}

function onKeydown(e) {
  if (e.key === 'Escape') {
    close()
    return
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    if (!open.value) {
      show()
      return
    }
    const n = props.options.length
    if (!n) return
    activeIndex.value = (activeIndex.value + (e.key === 'ArrowDown' ? 1 : -1) + n) % n
    return
  }
  if ((e.key === 'Enter' || e.key === ' ') && open.value) {
    e.preventDefault()
    const o = props.options[activeIndex.value]
    if (o) pick(o.value)
  }
}

/** 点击面板与触发框之外 → 关闭。用捕获阶段，避免被内层 stopPropagation 挡掉 */
function onDocPointerDown(e) {
  if (!open.value) return
  const t = e.target
  if (triggerRef.value?.contains(t)) return
  if (panelRef.value?.contains(t)) return
  close()
}

function onViewportChange() {
  if (open.value) placePanel()
}

watch(open, (v) => {
  if (v) {
    document.addEventListener('pointerdown', onDocPointerDown, true)
    window.addEventListener('scroll', onViewportChange, true)
    window.addEventListener('resize', onViewportChange)
  } else {
    document.removeEventListener('pointerdown', onDocPointerDown, true)
    window.removeEventListener('scroll', onViewportChange, true)
    window.removeEventListener('resize', onViewportChange)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocPointerDown, true)
  window.removeEventListener('scroll', onViewportChange, true)
  window.removeEventListener('resize', onViewportChange)
})
</script>
