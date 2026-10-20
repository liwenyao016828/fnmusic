<template>
  <!--
    选中态操作条（Selection Bar）。

    目的：把「全选 / 全部恢复 / 重试失败 / 一键清除 / 删除」这类**批量动作**
    从常驻版面里彻底移走 —— 没选中任何东西时它们本来就没有意义。
    这是 Apple 相册 / Files / Gmail 的同一套模式：**选中了才浮出一条操作条**。

    位置：底部居中、在播放条之上（播放条 z-40 固定贴底，这里 z-45 + 底距 88/96px）。
    形状：深色玻璃胶囊（与 toast / 播放条同一材质），白字，危险项红了才红。
  -->
  <Transition
    enter-active-class="transition duration-[260ms] ease-[cubic-bezier(0.32,0.72,0,1)]"
    enter-from-class="opacity-0 translate-y-3 scale-[0.98]"
    enter-to-class="opacity-100 translate-y-0 scale-100"
    leave-active-class="transition duration-[160ms] ease-in"
    leave-from-class="opacity-100 translate-y-0 scale-100"
    leave-to-class="opacity-0 translate-y-2 scale-[0.98]"
  >
    <div
      v-if="count > 0"
      class="pointer-events-none fixed inset-x-0 bottom-[88px] z-[45] flex justify-center px-3 pb-safe sm:bottom-[96px]"
    >
      <div class="pointer-events-auto flex max-w-full items-center gap-1 rounded-full border border-white/10 bg-[var(--ys-toast-bg)] px-2 py-1.5 shadow-[0_14px_40px_rgba(0,0,0,0.28)] backdrop-blur-2xl">
        <span class="pl-2.5 pr-1 text-[12px] font-medium text-white/70">
          已选 <b class="text-white">{{ count }}</b>
        </span>
        <span class="mx-1 h-4 w-px bg-white/15"></span>
        <button
          v-for="a in actions"
          :key="a.key"
          type="button"
          class="ys-press flex items-center gap-1.5 rounded-full px-3 py-1.5 text-[12px] font-medium transition-colors"
          :class="a.danger
            ? 'text-[#ff8a80] hover:bg-white/10'
            : 'text-white/80 hover:bg-white/10 hover:text-white'"
          :disabled="a.disabled"
          @click="emit('action', a.key)"
        >
          <component :is="a.icon" v-if="a.icon" class="h-3.5 w-3.5" :stroke-width="1.9" />
          {{ a.label }}
        </button>
        <span class="mx-1 h-4 w-px bg-white/15"></span>
        <button
          type="button"
          class="ys-press mr-0.5 grid h-7 w-7 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white"
          title="取消选择"
          @click="emit('clear')"
        >
          <X class="h-4 w-4" :stroke-width="2" />
        </button>
      </div>
    </div>
  </Transition>
</template>

<script setup>
import { X } from 'lucide-vue-next'

defineProps({
  count: { type: Number, default: 0 },
  actions: { type: Array, default: () => [] },
})

const emit = defineEmits(['action', 'clear'])
</script>
