<template>
  <!--
    按序号快速多选（Range Select Bar）。

    来历（用户 2026-10-23）：「下载页面中，多选框上方增加多选功能，目的为快速选择歌曲，
    比如用户输入 100，就是 0-100，用户也可以输入开始歌曲到最终歌曲比如 11-200。」

    一格一格点 200 行是点不完的，所以这里给一个「输入行号 → 一次选择一片」的入口。
    行号 = 界面上那一列 # 显示的数字（1 基，所见即所填），解析规则在 services/rangeSelect.js。
  -->
  <div class="flex flex-wrap items-center gap-2">
    <span class="text-[11px] font-medium text-[var(--ys-text-muted)]">{{ label }}</span>

    <div class="flex items-center gap-1.5">
      <input
        v-model="text"
        type="text"
        inputmode="numeric"
        autocomplete="off"
        spellcheck="false"
        :placeholder="placeholder"
        class="w-[132px] rounded-lg border border-[var(--ys-border)] bg-white px-2.5 py-1 text-[11.5px] tabular-nums text-[var(--ys-text)] outline-none transition-colors placeholder:text-[var(--ys-text-faint)] focus:border-emerald-400"
        @keyup.enter="apply"
      />
      <button
        type="button"
        class="ys-press rounded-lg bg-emerald-500 px-2.5 py-1 text-[11.5px] font-semibold text-white transition-colors hover:bg-emerald-600"
        :title="`当前共 ${total} 行，输入 100 = 前 100 首，11-200 = 第 11 到 200 首`"
        @click="apply"
      >选择</button>
      <button
        v-if="hasSelection"
        type="button"
        class="ys-press rounded-lg border border-[var(--ys-border)] bg-white px-2.5 py-1 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
        title="清空已勾选的行"
        @click="emit('clear')"
      >清空选择</button>
    </div>

    <span class="text-[10.5px] text-[var(--ys-text-faint)]">
      共 {{ total }} 行 · 例 100 / 11-200 / 1-10,80
    </span>
  </div>

  <InlineNotice v-if="error" :text="error" :log-link="false" />
</template>

<script setup>
import { ref } from 'vue'
import InlineNotice from './InlineNotice.vue'
import { parseRangeSpec } from '../services/rangeSelect'

const props = defineProps({
  /** 当前**可见**列表的行数（行号以此为准，越界会被夹到这一行） */
  total: { type: Number, required: true },
  label: { type: String, default: '按序号选' },
  placeholder: { type: String, default: '如 100 或 11-200' },
  /** 是否已有勾选 —— 决定「清空选择」按钮出不出现 */
  hasSelection: { type: Boolean, default: false },
})

const emit = defineEmits(['apply', 'clear'])

const text = ref('')
const error = ref('')

function apply() {
  error.value = ''
  const res = parseRangeSpec(text.value, props.total)
  if (!res.ok) {
    error.value = res.error
    return
  }
  // 输入保留：用户接着填下一段时看得见自己刚选了哪些
  emit('apply', res.indexes, res)
}
</script>
