<template>
  <!--
    2026-10-25 用户：「元数据补全页面内的路径展示，迁移到五个分段导航行的右边。
    可以点击进行目录选择。移除补全选项里的曲库目录组件。」

    曲库目录是「五个分段共用的一件事」：补全、查重、歌名修复、快照、替换音质
    扫的都是同一个目录，所以它不属于任何一张卡片，也不该藏在补全段的选项弹窗里。
    放在分段导航行右边 —— 看得见当前扫哪里，点一下就换。

    状态存在 services/snapState.js 里（和五个分段各自的缓存同一份口径），
    这样 TidyView 和自己这个 chip 读的是同一个值，不需要把 App 和页面组件
    用 props/emit 串起来。
  -->
  <button
    type="button"
    class="ys-press flex min-w-0 max-w-[38vw] items-center gap-1.5 rounded-full border border-[var(--ys-border)] bg-[var(--ys-surface)] px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:border-[var(--ys-accent)]/40 hover:text-[var(--ys-text)] sm:max-w-[260px]"
    :title="`曲库目录：${shown}（点击选择）`"
    @click="open = true"
  >
    <FolderOpen class="h-3.5 w-3.5 shrink-0 text-[var(--ys-accent)]" :stroke-width="1.9" />
    <span class="truncate font-mono">{{ shown }}</span>
  </button>
  <NasDirPicker v-model:open="open" title="选择曲库目录" :start="shown" @pick="onPick" />
</template>

<script setup>
import { computed, ref } from 'vue'
import { FolderOpen } from 'lucide-vue-next'
import NasDirPicker from './NasDirPicker.vue'
import { loadPref, savePref } from '../services/prefs'

defineProps({})

const DEFAULT_DIR = '/vol1/Music'
const prefDir = ref(loadPref('nas_dir', '') || '')
/** 让 TidyView 的 NAS 目录 prop 的默认值也能落一条记录：用户没设过就用 '/vol1/Music' */
/** 选完目录会写 prefs 的 nas_dir；TidyView 从同一处读，改完两处一起变 */
const open = ref(false)
const shown = computed(() => prefDir.value || DEFAULT_DIR)

function onPick(path) {
  if (!path) return
  prefDir.value = path
  savePref('nas_dir', path)
  // TidyView 需要重新加载：它监听的是窗口的 storage 事件（同标签页不会触发，所以另发一个自定义事件）
  window.dispatchEvent(new CustomEvent('ys:dir-changed', { detail: { dir: path } }))
}
</script>
