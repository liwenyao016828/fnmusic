<template>
  <div
    v-if="text"
    class="flex items-center gap-2 rounded-lg border px-3 py-2 text-[11px]"
    :class="tone === 'ok'
      ? 'border-emerald-200 bg-emerald-50 text-emerald-800'
      : 'border-rose-200 bg-rose-50 text-rose-700'"
  >
    <span class="min-w-0 flex-1 leading-relaxed">{{ text }}</span>
    <!--
      失败时给一个直达「日志」的入口：界面上只留一句人话，完整信息在日志里，
      没有入口的话用户根本不知道该去哪查（约定见 services/appLog.js）。
    -->
    <button
      v-if="tone !== 'ok' && logLink"
      class="shrink-0 underline decoration-dotted underline-offset-2 hover:no-underline"
      @click="goLogs"
    >查看日志</button>
  </div>
</template>

<script setup>
import { navigateTo } from '../services/navBus'

defineProps({
  /** 一句人话（由 services/userMsg.js 产出）；为空时不渲染 */
  text: { type: String, default: '' },
  /** 'error' 红色（默认）/ 'ok' 绿色 */
  tone: { type: String, default: 'error' },
  /** 是否显示「查看日志」入口 */
  logLink: { type: Boolean, default: true },
})

function goLogs() {
  navigateTo('logs')
}
</script>
