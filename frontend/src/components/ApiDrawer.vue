<template>
  <div>
    <!--
      「开放接口」抽屉：**从左边缘滑入、向右展开**。

      2026-09-28 用户口径：「动画不好看，弹出方向不对，应该从左到右滑出。」
      触发入口是左上角的软件图标（App.vue 的 button.brand-halo），所以面板锚在左边、
      入场位移 -100% → 0，视觉上就是「从左往右滑出来」，和入口在同一侧。

      ⚠️ 与「下载抽屉」（右侧滑出）方向相反是**故意的** —— 用户明确要求这个方向，别改回去。
      ⚠️ 缓动也一起换了：原来 300ms ease-out 起步太硬、看着像「弹」出来；
         现在入场 420ms cubic-bezier(.22,.61,.36,1)（快出慢收），出场 300ms 收得住。
    -->
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
          v-if="modelValue"
          @click="close"
          class="fixed inset-0 z-50 bg-black/40 backdrop-blur-xs"
        ></div>
      </Transition>

      <!-- 抽屉主体 -->
      <Transition
        enter-active-class="transition-transform duration-[420ms] ease-[cubic-bezier(0.22,0.61,0.36,1)]"
        enter-from-class="-translate-x-full"
        enter-to-class="translate-x-0"
        leave-active-class="transition-transform duration-300 ease-[cubic-bezier(0.4,0,1,1)]"
        leave-from-class="translate-x-0"
        leave-to-class="-translate-x-full"
      >
        <!-- `pt-safe pb-safe`：抽屉是 inset-y-0 满高，刘海机上下两端会压住标题栏和内容 -->
        <div
          v-if="modelValue"
          class="fixed inset-y-0 left-0 z-50 w-full sm:w-[720px] pt-safe pb-safe bg-white shadow-2xl flex flex-col overflow-hidden border-r border-gray-100"
        >
          <!-- 顶部标题栏 -->
          <div class="px-5 py-4 border-b border-gray-100 flex items-center justify-between bg-white shrink-0">
            <div class="flex items-center gap-2.5">
              <div class="w-8 h-8 rounded-xl bg-emerald-50 text-emerald-600 flex items-center justify-center shadow-2xs">
                <Terminal class="w-4 h-4" />
              </div>
              <div>
                <h3 class="text-sm font-bold text-gray-800">开放接口</h3>
                <p class="text-[11px] text-gray-400">供 AI 与外部程序调用 · 标准 HTTP + JSON</p>
              </div>
            </div>

            <button
              @click="close"
              class="p-1.5 rounded-xl text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors"
              title="关闭"
            >
              <X class="w-4 h-4" />
            </button>
          </div>

          <!-- 接口文档主体 -->
          <div class="flex-1 min-h-0 overflow-hidden">
            <ApiConsole />
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { watch, onBeforeUnmount } from 'vue'
import ApiConsole from './ApiConsole.vue'
import { Terminal, X } from 'lucide-vue-next'

const props = defineProps({
  modelValue: Boolean
})

const emit = defineEmits(['update:modelValue'])

function close() {
  emit('update:modelValue', false)
}

// Esc 关闭：抽屉占满屏幕高度、遮罩只盖内容区，键盘用户没有别的退出方式（2026-09-28 补上）。
// 只在打开时挂监听、关掉立刻摘掉 —— 别让它一直挂在 window 上。
function onKeydown(e) {
  if (e.key === 'Escape') close()
}

watch(() => props.modelValue, (open) => {
  if (open) window.addEventListener('keydown', onKeydown)
  else window.removeEventListener('keydown', onKeydown)
}, { immediate: true })

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>
