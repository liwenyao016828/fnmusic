<template>
  <!--
    2026-10-25 用户：「元数据补全页面内的路径展示，迁移到五个分段导航行的右边。
    可以点击进行目录选择。移除补全选项里的曲库目录组件。」

    曲库目录是「整理页各分段共用的一件事」：补全、修复、快照（2026-10-29 从五段压到三段）
    扫的都是同一个目录，所以它不属于任何一张卡片，也不该藏在补全段的选项弹窗里。
    放在分段导航行右边 —— 看得见当前扫哪里，点一下就换。

    状态存在 services/snapState.js 里（和五个分段各自的缓存同一份口径），
    这样 TidyView 和自己这个 chip 读的是同一个值，不需要把 App 和页面组件
    用 props/emit 串起来。
  -->
  <button
    type="button"
    class="ys-press flex min-w-0 max-w-[38vw] items-center gap-1.5 rounded-full border border-[var(--ys-border)] bg-[var(--ys-surface)] px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:border-emerald-500/40 hover:text-[var(--ys-text)] disabled:opacity-60 sm:max-w-[260px]"
    :title="`曲库目录：${shown}（点击选择）`"
    :disabled="busy"
    @click="pick"
  >
    <FolderOpen class="h-3.5 w-3.5 shrink-0 text-[var(--ys-accent)]" :stroke-width="1.9" />
    <!--
      8.7（2026-10-27）：`max-w-[38vw]` 在 320px 设备上只有 ~121px 宽，
      而 `truncate` 会把 `/vol1/Music` 截成 `/vol1/M…` —— 用户看不出选的是哪个目录。
      窄屏改成只显示最后一段（目录名），宽屏才显示全路径。
    -->
    <span class="hidden truncate font-mono sm:inline">{{ shown || '选择曲库目录' }}</span>
    <span class="truncate font-mono sm:hidden">{{ shown ? shownTail : '选择目录' }}</span>
  </button>
  <!-- 选择失败的说明：只在出错时出现一行，成功后清空（成功态静默）。 -->
  <span v-if="chipMsg" class="ml-1.5 truncate text-[11px] text-amber-700" :title="chipMsg">{{ chipMsg }}</span>
  <NasDirPicker v-model:open="open" title="选择曲库目录" :start="shown" @pick="onPick" />
</template>

<script setup>
import { computed, ref } from 'vue'
import { FolderOpen } from 'lucide-vue-next'
import NasDirPicker from './NasDirPicker.vue'
import { loadPref, savePref } from '../services/prefs'
import { chooseDirectory } from '../services/trimHost'
import { lastPick, refreshAuthStatus } from '../services/fnosAuth'
import { fail } from '../services/userMsg'

defineProps({})

const DEFAULT_DIR = ''
const prefDir = ref(loadPref('nas_dir', '') || '')
/** 用户没设过目录时显示占位文案，不再凭空写死 '/vol1/Music'（飞牛里那个位置多半没授权） */
/** 选完目录会写 prefs 的 nas_dir；TidyView 从同一处读，改完两处一起变 */
const open = ref(false)
const shown = computed(() => prefDir.value || DEFAULT_DIR)
/**
 * 窄屏显示用：只要最后一段目录名（`/vol1/Music` → `Music`）。
 * 全路径在手机上必然被截断，截断后的片段（`/vol1/M…`）反而认不出是哪个目录。
 */
const shownTail = computed(() => {
  const parts = String(shown.value).split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : shown.value
})

/**
 * 点 chip：**先弹飞牛的文件选择器**，宿主不在才退回收自绘目录树。
 *
 * 为什么改（2026-10-31 用户原话）：「我觉得用和点击选择后可以不需要出我自己的
 * 目录选择器了，直接用飞牛的文件选择器。」整理页换曲库目录原来是直接开自绘弹窗，
 * 用户得在里面再找一次「用飞牛的文件选择器」—— 多一层，而且未授权时那层树是空的。
 */
const busy = ref(false)
/** 失败时的一句人话（显示在 chip 下面那条小字里；成功静默，不打扰）。 */
const chipMsg = ref('')

async function pick() {
  if (busy.value) return
  busy.value = true
  chipMsg.value = ''
  try {
    const res = await chooseDirectory({ title: '选择曲库目录', okText: '用这个目录' })
    if (res.ok) {
      // 选完即授权：让后端刷新授权缓存，再按「刚选的目录读不读得到」给结论。
      const st = await refreshAuthStatus(res.path).then(() => lastPick.value).catch(() => null)
      if (!st || st.readable) {
        onPick(res.path)
      } else {
        // 还没生效别急着把曲库目录改成一个读不了的路径（改了下次扫描会直接 403）：
        // 走统一的「一句人话 + 进日志」，让用户稍等再点一次。
        chipMsg.value = fail('选择曲库目录', st.hint || '目录还没进入授权范围', '目录还没进入授权范围')
        // 已经在用自绘树选好目录的情况下才真的写入 —— 这里不改，等用户重试。
      }
      return
    }
    if (res.cancelled) return
    // 宿主不在 / 宿主拒绝：退回自绘目录树（浏览器里也能选目录）。
    open.value = true
  } finally {
    busy.value = false
  }
}

function onPick(path) {
  if (!path) return
  prefDir.value = path
  savePref('nas_dir', path)
  // TidyView 需要重新加载：它监听的是窗口的 storage 事件（同标签页不会触发，所以另发一个自定义事件）
  window.dispatchEvent(new CustomEvent('ys:dir-changed', { detail: { dir: path } }))
}
</script>
