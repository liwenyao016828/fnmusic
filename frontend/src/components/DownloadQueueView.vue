<template>
  <!--
    embedded=true 时不自带页面外壳，供「曲库管家 → 下载」内嵌使用，
    避免嵌套两个滚动容器与双份内边距。
  -->
  <component :is="embedded ? 'div' : PageShell">
    <template v-if="!embedded" #title-extra>
      <span
        v-if="downloadManager.activeCount.value > 0"
        class="rounded-full bg-[var(--ys-accent-soft)] px-2 py-0.5 text-[10px] font-medium text-[var(--ys-accent)]"
      >
        {{ downloadManager.activeCount.value }} 首进行中
      </span>
    </template>

    <!-- 指标行 -->
    <template v-if="!embedded" #metrics>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div
          v-for="m in metrics"
          :key="m.label"
          class="rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white px-[18px] py-4"
        >
          <!-- 指标卡：标签在上（带一颗状态点表示倾向），数字在下且一律墨色 ——
               彩色大数字一排下来像仪表盘，Apple 的做法是「数字说事实，颜色只提示」。 -->
          <span class="flex items-center gap-1.5 text-[11px] font-medium text-[var(--ys-text-muted)]">
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="m.dot"></span>
            {{ m.label }}
          </span>
          <b class="mt-1.5 block text-[26px] font-semibold tracking-[-0.02em] tabular-nums text-[var(--ys-text)]">{{ m.value }}</b>
        </div>
      </div>
    </template>

    <div>
      <!--
        内嵌模式不在这里渲染指标行 —— 外层「曲库管家」已经按当前侧栏项渲染了同一份指标，
        两边都渲染会出现两行一模一样的数字。
      -->

      <!-- 队列内容（与抽屉共用同一面板） -->
      <div
        class="flex flex-col overflow-hidden rounded-xl border border-[var(--ys-border)] bg-white"
        :class="embedded ? 'min-h-[420px]' : 'min-h-[460px]'"
      >
        <DownloadQueuePanel :active-source="activeSource" />
      </div>
    </div>
  </component>
</template>

<script setup>
import { computed } from 'vue'
import { downloadManager } from '../services/downloadManager'
import DownloadQueuePanel from './DownloadQueuePanel.vue'
import PageShell from './PageShell.vue'

const props = defineProps({
  activeSource: Object,
  /** true = 供「下载」页内嵌，不自带页面外壳 */
  embedded: { type: Boolean, default: false },
})

// 2026-10-18：原来这里有一份 shellProps（title「下载队列」/ desc「后台持久下载…」），
// 现在 PageShell 不收 title / desc 了 —— 页面名与简介的唯一出处是 services/navViews.js，
// 由 App.vue 顶栏渲染。所以这里不再传任何参数。

const metrics = computed(() => [
  { label: '全部任务', value: downloadManager.totalCount.value, dot: 'bg-[var(--ys-text-faint)]' },
  { label: '进行中', value: downloadManager.activeCount.value, dot: 'bg-emerald-600' },
  { label: '已暂停', value: downloadManager.pausedCount.value, dot: 'bg-amber-500' },
  { label: '失败', value: downloadManager.failedCount.value, dot: 'bg-rose-500' },
])
</script>
