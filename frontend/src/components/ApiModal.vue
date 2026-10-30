<template>
  <!--
    「开放接口」弹窗（2026-10-28 从原左侧抽屉 ApiDrawer.vue 迁来）。

    用户口径：「原来的 api 接口页面改为弹窗放在设置，免责声明旁边」——
    左上角那颗品牌按钮改成 AI 助手入口之后，开放接口这个页面失去了它的老入口，
    改成「设置 → 关于」那张卡里的一个按钮，与「免责声明与合规」并排。

    为什么是弹窗而不是抽屉：抽屉是从屏幕左边缘滑出来的，视觉上「属于全局导航」；
    而它现在的入口在设置页里，从设置里点出来的东西应该是**盖在设置上面**的一块，
    关掉就回到设置 —— 层级关系才对得上。尺寸上桌面给到 960px 宽（接口文档是
    两栏 + 长列表，太窄会挤成一条），手机铺满（inset-3）。

    实现要点（与 DisclaimerModal 同一套写法，便于维护）：
      · Teleport 到 body + 极高层级 z-[99999]（飞牛宿主里 iframe 叠层层级不可控）；
      · Esc 关闭、点遮罩关闭（点内容不关，@click.stop）；
      · 打开时锁 body 滚动（文档很长，弹窗内自己滚，背景不要跟着跑）；
      · ApiConsole 原样复用，一行不改。
  -->
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-200 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="modelValue"
        class="fixed inset-0 z-[99999] flex items-center justify-center bg-black/60 p-3 backdrop-blur-sm sm:p-6"
        @click="close"
      >
        <Transition
          appear
          enter-active-class="transition-[transform,opacity] duration-200 ease-out"
          enter-from-class="translate-y-2 scale-[0.98] opacity-0"
          enter-to-class="translate-y-0 scale-100 opacity-100"
        >
          <div
            v-if="modelValue"
            @click.stop
            role="dialog"
            aria-modal="true"
            aria-label="开放接口"
            class="relative z-10 flex h-full max-h-[86vh] w-full max-w-[960px] flex-col overflow-hidden rounded-2xl border border-[var(--ys-border)] bg-white shadow-2xl my-auto"
          >
            <!-- 标题栏 -->
            <div class="flex shrink-0 items-center justify-between border-b border-[var(--ys-border)] bg-white px-5 py-3.5">
              <div class="flex items-center gap-2.5">
                <div class="flex h-8 w-8 items-center justify-center rounded-xl bg-emerald-50 text-emerald-600">
                  <Terminal class="h-4 w-4" />
                </div>
                <div>
                  <h3 class="text-sm font-bold text-gray-800">开放接口</h3>
                  <p class="text-[11px] text-gray-500">供 AI 与外部程序调用 · 标准 HTTP + JSON</p>
                </div>
              </div>
              <button
                @click="close"
                class="rounded-xl p-1.5 text-gray-500 transition-colors hover:bg-[var(--ys-surface-hover)] hover:text-gray-600"
                title="关闭（Esc）"
                aria-label="关闭"
              >
                <X class="h-4 w-4" />
              </button>
            </div>

            <!-- 接口文档主体（ApiConsole 自带内部滚动，这里只管给它一块够高的区域） -->
            <div class="min-h-0 flex-1 overflow-hidden">
              <ApiConsole />
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import { watch, onBeforeUnmount } from 'vue'
import ApiConsole from './ApiConsole.vue'
import { Terminal, X } from 'lucide-vue-next'

const props = defineProps({
  modelValue: Boolean,
})
const emit = defineEmits(['update:modelValue'])

function close() {
  emit('update:modelValue', false)
}

function onKeydown(e) {
  if (e.key === 'Escape') {
    // 只处理最上层：文档里有输入框（复制框），在里面按 Esc 不该把整个弹窗关掉
    const el = e.target
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) return
    close()
  }
}

// body 滚动锁：弹窗内自己滚，背后的设置页不要跟着动。
// 用 overflow hidden + 记下原值，关闭时还原（别直接写 ''，可能破坏别处设置的内联样式）。
let prevOverflow = ''
function lockScroll(on) {
  if (typeof document === 'undefined') return
  if (on) {
    prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  } else {
    document.body.style.overflow = prevOverflow || ''
  }
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      window.addEventListener('keydown', onKeydown)
      lockScroll(true)
    } else {
      window.removeEventListener('keydown', onKeydown)
      lockScroll(false)
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  lockScroll(false)
})
</script>
