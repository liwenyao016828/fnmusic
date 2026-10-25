<template>
  <!--
    包一层 div 再接 v-if：`<Teleport>` 是渲染出口不是元素，v-if 直接写在它上面时，
    Vue 会把这个分支当成「未挂载的注释位」，后续 prop 变化不保证重渲染（实测 SSR/HMR
    下整块不出现）。v-if 放进 Teleport 的内容里就只是普通的条件渲染，最稳。

    「更新完成」提示弹窗（用户 2026-10-25：「更新完成后第一次进入应用，提示更新完成和版本号」）。

    为什么不复用 UpdateModal：那个是「有没有新版本 / 去更新」的操作面板。
    这里要说的是一件**已经发生的事**——「你已经从 X 升到 Y 了」。用同一张卡片会让用户
    在一个能点「立即更新」的界面里被告知「已经更新好了」，最容易误操作。

    判据在服务端（backend/pkg/appupdate/notice.go）：**版本号变了就提示一次**，
    所以手动装 fpk、应用中心升级、甚至降级都会走到这里；首次运行不会。
    文案必须能区分升级/降级——降级说「更新成功」是错的（用户会以为出 bug 了）。

    关闭即确认：关掉就调 ackNotice()，后端记下「提示过了」，下次启动不再弹。
    -->
  <Teleport to="body">
    <!--
      z-[70]：必须**高过** SideSheet（z-[60]/z-[61]）与各弹窗（z-[60]）。
      这里以前写的是 `z-60`——Tailwind 3 里没有 `z-60` 这个内置类（只有 z-0/10/20/30/40/50），
      所以整条规则被丢弃，计算值是 `z-index: auto`：侧栏底部点开「版本更新」抽屉后弹这个，
      会被抽屉的 z-[61] 盖住（2026-10-25 实测：弹窗在 DOM 里、元素中心能命中，但人眼看不到，
      一度以为是「渲染没出来」）。同文件里 UpdateModal / DownloadDirModal / NasDirPicker
      也是 `z-60`，同样是坏的，只是恰好没人叠在抽屉上打开过。
    -->
    <div
      v-if="notice"
      class="fixed inset-0 z-[70] flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      role="dialog"
      aria-modal="true"
      aria-label="更新完成"
      @click.self="onClose"
    >
      <div class="w-full max-w-md rounded-2xl border border-gray-100 bg-white p-5 shadow-2xl">
        <div class="mb-3 flex items-center justify-between">
          <h3 class="flex items-center gap-1.5 text-sm font-bold text-gray-800">
            <component :is="headIcon" class="h-4 w-4" :class="headColor" />
            <span>{{ headTitle }}</span>
          </h3>
          <button
            class="text-gray-400 transition-colors hover:text-gray-600"
            title="关闭"
            aria-label="关闭"
            @click="onClose"
          >
            <X class="h-4 w-4" />
          </button>
        </div>

        <div class="space-y-2 text-sm text-gray-600">
          <p class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            <span class="text-xs text-gray-400">当前版本</span>
            <span class="font-mono text-base font-semibold text-gray-800">v{{ notice.version }}</span>
            <template v-if="notice.fromVersion">
              <span class="text-xs text-gray-400">原版本</span>
              <span class="font-mono text-xs text-gray-400 line-through">v{{ notice.fromVersion }}</span>
            </template>
          </p>
          <p v-if="notice.direction === 'down'" class="rounded-lg bg-amber-50 px-2.5 py-2 text-xs leading-relaxed text-amber-700">
            这次是从较新的 v{{ notice.fromVersion }} 回到了 v{{ notice.version }}。
            如果是误操作，可以到「系统 → 版本更新」里重新检查。
          </p>
          <p v-else class="rounded-lg bg-emerald-50 px-2.5 py-2 text-xs leading-relaxed text-emerald-700">
            已经用上新版本。下面这些是新版本带来的变化。
          </p>
        </div>

        <!-- release 说明：只在服务端缓存里正好是这一版时才有，可能为空 -->
        <div v-if="notice.notes" class="mt-3 max-h-64 overflow-y-auto rounded-lg border border-gray-100 bg-gray-50/60 p-3">
          <pre class="whitespace-pre-wrap break-words font-sans text-xs leading-relaxed text-gray-600">{{ notice.notes }}</pre>
        </div>

        <div class="mt-4 flex items-center justify-end gap-2">
          <button
            class="rounded-lg bg-gray-900 px-3.5 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-gray-800"
            @click="onClose"
          >
            知道了
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, onBeforeUnmount, watch } from 'vue'
import { ArrowUpCircle, ArrowDownCircle, CheckCircle2, X } from 'lucide-vue-next'

const props = defineProps({
  // {show, version, fromVersion, direction, notes}；null 表示不显示。
  notice: { type: Object, default: null },
})
const emit = defineEmits(['close'])

const headIcon = computed(() => {
  if (props.notice?.direction === 'down') return ArrowDownCircle
  if (props.notice?.direction === 'unknown') return CheckCircle2
  return ArrowUpCircle
})
const headColor = computed(() => (props.notice?.direction === 'down' ? 'text-amber-500' : 'text-emerald-500'))
const headTitle = computed(() => {
  const d = props.notice?.direction
  if (d === 'down') return '版本已回退'
  if (d === 'unknown') return '版本已变化'
  return '更新完成'
})

function onClose() {
  // 只要弹出来过就算「用户看到了」，关掉即确认（下次启动不再弹）。
  emit('close')
}

// Esc 关闭：与 UpdateModal / SideSheet 同一套写法，卸载必须摘监听。
function onKey(e) {
  if (e.key === 'Escape' && props.notice) onClose()
}
watch(
  () => !!props.notice,
  (v) => {
    if (v) document.addEventListener('keydown', onKey)
    else document.removeEventListener('keydown', onKey)
  },
)
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>
