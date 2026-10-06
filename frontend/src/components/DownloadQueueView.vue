<template>
  <!--
    embedded=true 时不自带页面外壳，供「曲库管家 → 下载」内嵌使用，
    避免嵌套两个滚动容器与双份内边距。
  -->
  <component :is="embedded ? 'div' : PageShell" v-bind="shellProps">
    <template v-if="!embedded" #title-extra>
      <span
        v-if="downloadManager.activeCount.value > 0"
        class="rounded-full bg-emerald-500 px-2 py-0.5 text-[10px] font-bold text-white animate-pulse"
      >
        {{ downloadManager.activeCount.value }} 首进行中
      </span>
    </template>

    <!-- 指标行 -->
    <template v-if="!embedded" #metrics>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div v-for="m in metrics" :key="m.label" class="rounded-xl border border-gray-200 bg-white px-[17px] py-[15px]">
          <b class="block text-[24px] font-bold" :class="m.color">{{ m.value }}</b>
          <span class="text-[12px] text-gray-400">{{ m.label }}</span>
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
        class="flex flex-col overflow-hidden rounded-[15px] border border-gray-200 bg-white"
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
  /** true = 供「曲库管家 → 下载」内嵌，不自带页面外壳 */
  embedded: { type: Boolean, default: false },
})

/** 自带外壳时的 PageShell 参数（内嵌模式下不会被使用） */
const shellProps = computed(() =>
  props.embedded
    ? {}
    : {
        title: '下载队列',
        desc: '后台持久下载，切换页面不中断 · 支持音质选择、失败自动换源、歌词内嵌',
      },
)

const metrics = computed(() => [
  { label: '全部任务', value: downloadManager.totalCount.value, color: 'text-gray-800' },
  { label: '进行中', value: downloadManager.activeCount.value, color: 'text-emerald-600' },
  { label: '已暂停', value: downloadManager.pausedCount.value, color: 'text-amber-600' },
  { label: '失败', value: downloadManager.failedCount.value, color: 'text-rose-600' },
])
</script>
