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
    class="pointer-events-none fixed z-40 flex w-full max-w-[92vw] left-1/2 -translate-x-1/2 flex-col items-center gap-2 ys-dock-lift [--dock-base:6rem]"
    aria-live="polite"
    aria-atomic="false"
  >
    <TransitionGroup name="ys-feedback">
      <button
        v-for="item in visible"
        :key="item.id"
        class="pointer-events-auto flex max-w-full items-center gap-2.5 rounded-full border border-white/10 bg-[var(--ys-toast-bg)] px-4 py-2.5 text-left leading-relaxed text-white shadow-[0_10px_30px_rgba(0,0,0,0.22)] backdrop-blur-xl"
        :class="item.goLogs
          ? 'cursor-pointer transition-transform duration-200 [transition-timing-function:var(--ys-ease)] hover:scale-[1.02] active:scale-[0.99]'
          : 'cursor-default'"
        :style="{ fontSize: 'var(--ys-fs-toast)' }"
        @click="onTap(item)"
      >
        <!--
          提示条改成「深色玻璃一条圆角药丸 + 一个彩色图标」（v2.11.0）。
          原来四色实心底（info/ok/warn/fail 直接铺满）在同一屏里并排出现时太吵，
          而这东西是**瞬时**反馈：主信息是那句话，颜色只该做「轻重缓急」的提示。
        -->
        <component
          :is="ICONS[item.level] || ICONS.info"
          class="h-4 w-4 shrink-0"
          :class="TINT[item.level] || TINT.info"
          :stroke-width="2"
        />
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

/** 颜色只上图标：底统一是深色玻璃（--ys-toast-bg），不再四色铺满整条。 */
const TINT = {
  info: 'text-[var(--ys-toast-info)]',
  ok: 'text-[var(--ys-toast-ok)]',
  warn: 'text-[var(--ys-toast-warn)]',
  fail: 'text-[var(--ys-toast-fail)]',
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
  transition: opacity 0.22s cubic-bezier(0.32, 0.72, 0, 1), transform 0.22s cubic-bezier(0.32, 0.72, 0, 1);
}
.ys-feedback-enter-from,
.ys-feedback-leave-to {
  opacity: 0;
  transform: translateY(10px) scale(0.97);
}
.ys-feedback-move {
  transition: transform 0.22s cubic-bezier(0.32, 0.72, 0, 1);
}
@media (prefers-reduced-motion: reduce) {
  .ys-feedback-enter-active,
  .ys-feedback-leave-active,
  .ys-feedback-move {
    transition-duration: 0.001s;
  }
}
</style>
