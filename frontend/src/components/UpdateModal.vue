<template>
  <!--
    版本更新弹窗。

    为什么要弹窗（用户 2026-10-24）：「版本更新在导航栏底部增加一个检测到更新后显示
    有更新的窗口，点击后弹窗更新页面」—— 更新状态以前只有进「系统」那一页才看得到；
    检测到新版本时人正在别的页面，界面上没有任何地方告诉他「可以更新了」。现在侧栏
    底部（手机是「更多」抽屉底部）常驻一个入口，点这里就把同一套更新流程拉到手边。

    内容直接复用 UpdateCard —— 检查/下载/安装的状态机与文案都在它和 services/appUpdate
    里，这里只负责外壳：遮罩、点背景关闭、Esc 关闭。传 `plain` 让卡片在弹窗里不再自己
    套一层圆角边框（弹窗本身就是白卡片，卡片里再画一个框就是同一件事说两遍）。
  -->
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-60 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      role="dialog"
      aria-modal="true"
      aria-label="版本更新"
      @click.self="emit('close')"
    >
      <div class="w-full max-w-md rounded-2xl border border-gray-100 bg-white p-5 shadow-2xl">
        <div class="mb-3 flex items-center justify-between">
          <h3 class="flex items-center gap-1.5 text-sm font-bold text-gray-800">
            <ArrowUpCircle class="h-4 w-4 text-emerald-500" />
            <span>版本更新</span>
          </h3>
          <button
            class="text-gray-400 transition-colors hover:text-gray-600"
            title="关闭"
            aria-label="关闭"
            @click="emit('close')"
          >
            <X class="h-4 w-4" />
          </button>
        </div>
        <UpdateCard plain />
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { onBeforeUnmount, watch } from 'vue'
import { ArrowUpCircle, X } from 'lucide-vue-next'
import UpdateCard from './UpdateCard.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
})
const emit = defineEmits(['close'])

// Esc 关闭：只在打开时挂监听，关掉/卸载必须摘掉（与 SideSheet 同一套写法）。
function onKey(e) {
  if (e.key === 'Escape' && props.open) emit('close')
}
watch(
  () => props.open,
  (v) => {
    if (v) document.addEventListener('keydown', onKey)
    else document.removeEventListener('keydown', onKey)
  },
)
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>
