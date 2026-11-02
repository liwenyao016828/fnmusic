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
      class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-3 backdrop-blur-xs sm:p-4"
      role="dialog"
      aria-modal="true"
      aria-label="版本更新"
      @click.self="emit('close')"
    >
      <!--
        手机端内容显示不全（用户 2026-11-02 第 3 项）：原来的卡片只有
        `w-full max-w-md` + `p-5`，高度不设上限 —— 展开「镜像、网络与令牌」
        （token + 镜像清单 + 代理）后整张卡片比手机视口还高，而弹窗外层是
        `items-center`，超出的部分**上下两头同时被裁掉**，中间那段谁也滚不到：
        关闭按钮和底部按钮都可能按不着。

        现在把卡片改成「最高不超过可视区，内部自己滚」：
          · `max-h-[calc(100dvh - 安全区 - 1.5rem)]`（dvh 而不是 vh：手机浏览器地址栏
            收起时 vh 不跟着变；再扣掉刘海与手势条，卡片不会顶到屏幕边）；
          · flex 纵向布局，标题行 `shrink-0` 钉在顶部，只有内容区 `overflow-y-auto`；
          · 内边距在手机上收一点（p-4），给正文让出宽度。
        这样无论哪一块展开、展开多久，标题与关闭按钮始终可见，内容永远能滚到底。
      -->
      <div class="flex max-h-[calc(100dvh_-_var(--safe-top)_-_var(--safe-bottom)_-_1.5rem)] w-full max-w-md flex-col rounded-2xl border border-[var(--ys-border)] bg-white p-4 shadow-2xl sm:p-5">
        <div class="mb-3 flex shrink-0 items-center justify-between">
          <h3 class="flex items-center gap-1.5 text-sm font-bold text-gray-800">
            <ArrowUpCircle class="h-4 w-4 text-emerald-600" />
            <span>版本更新</span>
          </h3>
          <button
            class="text-gray-500 transition-colors hover:text-gray-600"
            title="关闭"
            aria-label="关闭"
            @click="emit('close')"
          >
            <X class="h-4 w-4" />
          </button>
        </div>
        <!-- 内容区独立滚动：`-mx-1 px-1` 是给 focus 环留的余量，避免被 overflow 裁掉。 -->
        <div class="-mx-1 min-h-0 flex-1 overflow-y-auto overscroll-contain px-1">
          <UpdateCard plain />
        </div>
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
