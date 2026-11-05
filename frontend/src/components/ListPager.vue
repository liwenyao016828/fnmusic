<template>
  <nav v-if="total" class="flex w-full min-w-0 flex-wrap items-center justify-between gap-3 border-t border-[var(--ys-border)] px-1 pt-4 text-xs text-gray-500" :aria-label="label">
    <span class="tabular-nums">{{ text }}</span>
    <div class="flex max-w-full min-w-0 flex-wrap items-center gap-2">
      <button type="button" class="ys-page-button" :disabled="page <= 1 || loading" aria-label="上一页" @click="changePage(page - 1)"><ChevronLeft class="h-4 w-4" /></button>
      <template v-for="p in pageItems" :key="p">
        <span v-if="p === '…'" class="px-1">…</span>
        <button v-else type="button" class="ys-page-button" :class="p === page && 'border-orange-500 bg-orange-500 text-white'" :aria-current="p === page ? 'page' : undefined" :disabled="loading" @click="changePage(p)">{{ p }}</button>
      </template>
      <button type="button" class="ys-page-button" :disabled="page >= pages || loading" aria-label="下一页" @click="changePage(page + 1)"><ChevronRight class="h-4 w-4" /></button>
      <div class="w-[88px]">
        <SelectField compact numeric :model-value="pageSize" :options="sizes.map(n => ({ value: n, label: `${n} 条/页` }))" @update:model-value="changeSize" />
      </div>
    </div>
  </nav>
</template>

<script setup>
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'
import SelectField from './SelectField.vue'
import { clampPage, DISPLAY_PAGE_SIZES, normalizePageSize, pageLabel } from '../services/displayPaging'

const props = defineProps({
  page: { type: Number, default: 1 },
  pageSize: { type: Number, default: 24 },
  total: { type: Number, default: 0 },
  loading: Boolean,
  sizes: { type: Array, default: () => DISPLAY_PAGE_SIZES },
  label: { type: String, default: '列表分页' },
})
const emit = defineEmits(['change-page', 'change-size'])
const sizes = computed(() => props.sizes)
const pageSize = computed(() => normalizePageSize(props.pageSize))
const pages = computed(() => Math.max(1, Math.ceil(props.total / pageSize.value)))
const page = computed(() => clampPage(props.page, props.total, pageSize.value))
const text = computed(() => pageLabel(page.value, props.total, pageSize.value))
const pageItems = computed(() => {
  const n = pages.value
  if (n <= 7) return Array.from({ length: n }, (_, i) => i + 1)
  const start = Math.max(2, Math.min(page.value - 2, n - 5))
  return [1, ...(start > 2 ? ['…'] : []), ...Array.from({ length: 5 }, (_, i) => start + i), ...(start + 4 < n - 1 ? ['…'] : []), n]
})
function changePage(p) { if (p !== page.value) emit('change-page', clampPage(p, props.total, pageSize.value)) }
function changeSize(n) { emit('change-size', normalizePageSize(n)) }
</script>

<style scoped>
.ys-page-button { display: grid; place-items: center; min-width: 2rem; height: 2rem; padding: 0 .35rem; border: 1px solid var(--ys-border); border-radius: .5rem; background: #fff; color: var(--ys-text-secondary); font-variant-numeric: tabular-nums; transition: color .18s, border-color .18s, background .18s; }
.ys-page-button:hover:not(:disabled) { border-color: #fdba74; color: #c2410c; }
.ys-page-button:disabled { opacity: .4; cursor: default; }
</style>
