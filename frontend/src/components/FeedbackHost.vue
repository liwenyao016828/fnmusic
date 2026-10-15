<template>
  <!--
    全站瞬时提示的**唯一**渲染点（App.vue 里挂一次）。

    分工：`services/feedback.js` 管队列 / 去重 / 超时 / 写日志，这里只管长什么样。
    任何组件（含深层子组件）都能 `import { ok, fail } from '../services/feedback'` 直接用，
    不再需要 emit 一路往上递，也不会各页自己造一套「提示条」。

    位置沿用老 appToast 的 bottom-24：悬浮在底部播放条上方，不挡内容也不挡手指。
    aria-live 让读屏软件念出来（原来的实现没有）。
  -->
  <div
    class="pointer-events-none fixed bottom-24 left-1/2 z-40 flex w-full max-w-[92vw] -translate-x-1/2 flex-col items-center gap-2"
    aria-live="polite"
    aria-atomic="false"
  >
    <TransitionGroup name="ys-feedback">
      <button
        v-for="item in visible"
        :key="item.id"
        class="pointer-events-auto flex max-w-full items-center gap-2 rounded-[var(--ys-radius-control)] px-4 py-2.5 text-left leading-relaxed text-white shadow-lg"
        :class="[TONE[item.level] || TONE.info, item.goLogs ? 'cursor-pointer hover:brightness-110' : 'cursor-default']"
        :style="{ fontSize: 'var(--ys-fs-toast)' }"
        @click="onTap(item)"
      >
        <component :is="ICONS[item.level] || ICONS.info" class="h-3.5 w-3.5 shrink-0" />
        <span class="min-w-0 break-words">{{ item.text }}</span>
        <!-- 「看日志」是可点提示唯一的附加动作：一句话说不清的细节都在日志里 -->
        <span v-if="item.goLogs" class="shrink-0 whitespace-nowrap opacity-75">查看日志 ›</span>
      </button>
    </TransitionGroup>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { feedbackItems, dismiss, FEEDBACK_MAX_VISIBLE } from '../services/feedback'
import { navigateTo } from '../services/navBus'
import { Info, CheckCircle2, AlertTriangle, XCircle } from 'lucide-vue-next'

const visible = computed(() => feedbackItems.value.slice(-FEEDBACK_MAX_VISIBLE))

const TONE = {
  info: 'bg-[var(--ys-toast-info)]',
  ok: 'bg-[var(--ys-toast-ok)]',
  warn: 'bg-[var(--ys-toast-warn)]',
  fail: 'bg-[var(--ys-toast-fail)]',
}
const ICONS = { info: Info, ok: CheckCircle2, warn: AlertTriangle, fail: XCircle }

/**
 * 点一下：需要看细节的跳到「日志」页（深链解析见 services/navViews.js ——
 * `logs` 现在落在「系统」页的日志段），然后这条提示就该走了。
 */
function onTap(item) {
  if (item.goLogs) navigateTo('logs')
  dismiss(item.id)
}
</script>

<style scoped>
.ys-feedback-enter-active,
.ys-feedback-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}
.ys-feedback-enter-from,
.ys-feedback-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
.ys-feedback-move {
  transition: transform 0.18s ease;
}
</style>
