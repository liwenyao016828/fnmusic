<template>
  <div>
    <!-- ── 右侧滑出式下载队列抽屉（快捷查看用；完整页面见「下载队列」标签） ── -->
    <Teleport to="body">
      <!-- 遮罩背景 -->
      <Transition
        enter-active-class="transition-opacity duration-300 ease-out"
        enter-from-class="opacity-0"
        enter-to-class="opacity-100"
        leave-active-class="transition-opacity duration-200 ease-in"
        leave-from-class="opacity-100"
        leave-to-class="opacity-0"
      >
        <div
          v-if="downloadManager.isOpen.value"
          @click="downloadManager.close()"
          class="fixed inset-0 z-50 bg-black/40 backdrop-blur-xs"
        ></div>
      </Transition>

      <!-- 抽屉主体 -->
      <Transition
        enter-active-class="transition-transform duration-300 ease-out"
        enter-from-class="translate-x-full"
        enter-to-class="translate-x-0"
        leave-active-class="transition-transform duration-250 ease-in"
        leave-from-class="translate-x-0"
        leave-to-class="translate-x-full"
      >
        <!-- `pt-safe pb-safe`：抽屉是 inset-y-0 满高，刘海机上下两端会压住标题栏和内容 -->
        <div
          v-if="downloadManager.isOpen.value"
          class="fixed inset-y-0 right-0 z-50 w-full sm:w-[480px] pt-safe pb-safe bg-white shadow-2xl flex flex-col overflow-hidden border-l border-gray-100"
        >
          <!-- 顶部标题栏 -->
          <div class="px-5 py-4 border-b border-gray-100 flex items-center justify-between bg-white shrink-0">
            <div class="flex items-center gap-2.5">
              <div class="w-8 h-8 rounded-xl bg-emerald-50 text-emerald-600 flex items-center justify-center shadow-2xs">
                <Download class="w-4 h-4" :class="{ 'animate-bounce': downloadManager.activeCount.value > 0 }" />
              </div>
              <div>
                <h3 class="text-sm font-bold text-gray-800 flex items-center gap-2">
                  <span>下载队列</span>
                  <span
                    v-if="downloadManager.activeCount.value > 0"
                    class="px-2 py-0.5 rounded-full bg-emerald-500 text-white text-[10px] font-semibold animate-pulse"
                  >
                    {{ downloadManager.activeCount.value }} 首下载中
                  </span>
                </h3>
                <p class="text-[11px] text-gray-400">后台持久下载，切换页面不中断</p>
              </div>
            </div>

            <div class="flex items-center gap-1">
              <button
                @click="emit('open-full')"
                class="p-1.5 rounded-xl text-gray-400 hover:text-emerald-600 hover:bg-emerald-50 transition-colors"
                title="在完整页面中打开"
              >
                <Maximize2 class="w-4 h-4" />
              </button>
              <button
                @click="downloadManager.close()"
                class="p-1.5 rounded-xl text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors"
                title="关闭下载面板"
              >
                <X class="w-4 h-4" />
              </button>
            </div>
          </div>

          <!-- 队列内容（与完整页面共用同一面板） -->
          <DownloadQueuePanel :active-source="activeSource" />
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { downloadManager } from '../services/downloadManager'
import DownloadQueuePanel from './DownloadQueuePanel.vue'
import { Download, X, Maximize2 } from 'lucide-vue-next'

const props = defineProps({
  activeSource: Object
})

const emit = defineEmits(['open-full'])
</script>
