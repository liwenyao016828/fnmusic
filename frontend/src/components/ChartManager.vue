<script setup>
/**
 * 榜单管理：把「哪些榜单要出现在飞牛音乐里」收成**一页**。
 *
 * 为什么单独一页：原来这一组控件是挤在「发现音乐 → 排行榜单」网格上方的一条控制条里
 * —— 那里是**浏览**的地方，用户是来看榜单的，却被四个开关和一句免责说明挡在最上面。
 * 浏览与配置是两件事，分开更说得清（这也是参照 fnmusic-ext 的界面组织方式：
 * 一个关注点一页，总开关 + 计数 + 批量按钮 + 按平台分组的封面网格）。
 *
 * 三条口径：
 * 1. 这里只管**听**：勾中的榜单会在飞牛音乐里变成一张**在线歌单**（现造的虚拟歌单、
 *    只读、曲目不落盘）。想把榜单里的新歌**存**下来（下到本地曲库 / 同步进飞牛歌单），
 *    那是「订阅」的事 —— 去「曲库管家 → 歌单监控」，或在「排行榜单」里点开榜单右上角的
 *    「订阅更新」。两处选的是同一张榜单，但一个只影响「能不能听」、一个才动硬盘，
 *    所以不是重复功能；页面底部有一句导流，免得用户以为是两个并排的开关。
 * 2. 勾选**立刻生效**（不学「改完再点保存并生效」）：少一个保存按钮，就少一次
 *    「我改了怎么没生效」。
 * 3. 只列**有直链解析器**的平台：网易云与 QQ。酷狗/酷我挂上去等于「点开能看、
 *    点了不出声」，宁可不出现在这个页面（后端拦截层是同一份白名单）。
 */
import { computed, onMounted, ref } from 'vue'
import { Loader2, Check, Play, Music2, RotateCw } from 'lucide-vue-next'
import { ChartsAPI } from '../api/client'
import {
  chartInject, chartInjectKey, replaceIds, toggleItem, setEnabled, setUIInject, loadConfig,
} from '../services/chartInject'

const emit = defineEmits(['preview'])

// 与后端拦截层 `vChartPlatforms` 同一份名单
const PLATFORMS = [
  { id: 'wy', name: '网易云音乐', dot: 'bg-red-400', hint: '网易云官方榜单（云音乐 App 里同样的那几个）' },
  { id: 'tx', name: 'QQ 音乐', dot: 'bg-emerald-400', hint: 'QQ 音乐官方榜单（巅峰榜等）' },
]

const { ids, enabled, uiInject, busy, error, savedAt } = chartInject

const groups = ref(PLATFORMS.map(p => ({ ...p, loading: false, error: '', items: [] })))
const loadingConfig = ref(false)

/** 模块级列表缓存：切走再回来不重打（后端自己也有 15 分钟缓存，这里只是省一次往返） */
const toplistCache = new Map()

const total = computed(() => groups.value.reduce((n, g) => n + g.items.length, 0))
const selectedCount = computed(() => ids.value.length)
const savedText = computed(() => {
  if (!savedAt.value) return ''
  const t = new Date(savedAt.value)
  const p = (n) => String(n).padStart(2, '0')
  return `${p(t.getHours())}:${p(t.getMinutes())}:${p(t.getSeconds())}`
})

async function loadGroup(g, force = false) {
  if (!force && toplistCache.has(g.id)) {
    g.items = toplistCache.get(g.id)
    g.error = ''
    return
  }
  g.loading = true
  g.error = ''
  try {
    const res = await ChartsAPI.toplists(g.id)
    const items = res?.code === 200 ? (res.data || []) : []
    g.items = items
    if (items.length) toplistCache.set(g.id, items)
    else g.error = '这个平台没有返回榜单'
  } catch (e) {
    g.error = '载入失败：' + (e?.response?.data?.message || e?.message || '未知错误')
  } finally {
    g.loading = false
  }
}

function refreshAll() {
  loadGroup(groups.value[0], true)
  loadGroup(groups.value[1], true)
}

onMounted(async () => {
  loadingConfig.value = true
  try {
    // 强制重读：这一页显示的就是「服务端现在到底是什么」，哪怕别处已经读过一次
    // （配置也可能被外部改过 —— 比如直接用 POST /api/config 写的）
    await loadConfig(true)
  } finally {
    loadingConfig.value = false
  }
  groups.value.forEach(g => loadGroup(g))
})

/** 某张榜单在配置里的键 */
function keyOf(item, g) {
  return chartInjectKey(item, g.id)
}

function isOn(item, g) {
  return ids.value.includes(keyOf(item, g))
}

/** 平台内可用的键（列表没取到时返回空 —— 空数组会让下面的动作跳过这个平台） */
function keysOf(g) {
  return g.items.map(it => chartInjectKey(it, g.id))
}

/**
 * 把某个平台的勾选设成 `want`（true = 全选，false = 全清），**其他平台原样不动**。
 *
 * ⚠️ 之所以按平台切片而不是一把 replace：某个平台列表没取到（网络抖/接口挂）时，
 * 「全部启用」不能顺手把那个平台原来勾好的榜单抹掉 —— 它只该动自己有把握的部分。
 * 这也是「全选」与「全部取消」的区别：前者只补自己知道的，后者是用户明确要清空。
 */
function applyPlatform(list, g, want) {
  if (!g.items.length) return list
  const others = list.filter(k => !k.startsWith(g.id + '_'))
  return want ? [...others, ...keysOf(g)] : others
}

function selectPlatform(g, want) {
  replaceIds(applyPlatform(ids.value.slice(), g, want))
}

function selectAllLoaded() {
  let next = ids.value.slice()
  for (const g of groups.value) next = applyPlatform(next, g, true)
  replaceIds(next)
}

function selectOnly(platformId) {
  let next = ids.value.slice()
  for (const g of groups.value) next = applyPlatform(next, g, g.id === platformId)
  replaceIds(next)
}

/** 播放量：卡片上不铺长数字（6507860992 → 65 亿） */
function fmtPlay(n) {
  const v = Number(n) || 0
  if (v >= 1e8) return (v / 1e8).toFixed(v >= 1e9 ? 0 : 1).replace(/\.0$/, '') + ' 亿'
  if (v >= 1e4) return (v / 1e4).toFixed(1).replace(/\.0$/, '') + ' 万'
  return v ? String(v) : ''
}

function subLine(item) {
  return [item.update_frequency, fmtPlay(item.play_count)].filter(Boolean).join(' · ')
}

function hideCover(e) {
  e.target.style.visibility = 'hidden'
}
</script>

<template>
  <div class="mx-auto max-w-[1500px] px-5 py-6 pb-28 sm:px-8 sm:py-7 sm:pb-24">
    <!-- 页头 -->
    <div class="mb-4 flex flex-wrap items-start gap-x-4 gap-y-2">
      <div class="min-w-0">
        <h2 class="text-[15px] font-bold text-gray-900">榜单管理</h2>
        <p class="mt-1 max-w-[900px] text-[12px] leading-relaxed text-gray-500">
          勾选想要的榜单，它们会以<strong class="text-gray-700">在线歌单</strong>的形式出现在
          <strong class="text-gray-700">飞牛音乐</strong>的歌单列表里。
          <span class="text-amber-600/90">
            歌单里只保留能播放的曲目，所以会比平台榜单短 —— 榜单越热门、付费曲目越多，能播的越少。
          </span>
        </p>
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2">
        <span v-if="busy" class="flex items-center gap-1 text-[11px] font-bold text-emerald-600">
          <Loader2 class="h-3.5 w-3.5 animate-spin" />保存中…
        </span>
        <span v-else-if="savedText" class="text-[11px] text-gray-400">已生效 {{ savedText }}</span>
        <button
          type="button"
          @click="refreshAll"
          class="flex items-center gap-1 rounded-lg border border-gray-200 bg-white px-2.5 py-1 text-[11px] font-bold text-gray-500 transition-colors hover:border-emerald-200 hover:text-emerald-600"
        >
          <RotateCw class="h-3.5 w-3.5" />刷新榜单
        </button>
      </div>
    </div>

    <!-- 卡片 1：总控制 -->
    <div class="rounded-xl border border-gray-100 bg-white p-4">
      <div class="flex flex-wrap items-center gap-x-4 gap-y-3">
        <!--
          总开关：关掉只是「先不注入」，勾选**不丢** —— 所以它与「全部取消」是两个动作。
        -->
        <label
          class="flex cursor-pointer select-none items-center gap-2 text-[12.5px] font-bold transition-colors"
          :class="enabled ? 'text-emerald-600' : 'text-gray-400'"
          :title="enabled ? '关掉后勾选会保留，只是暂时不注入' : '打开后按勾选注入'"
        >
          <button
            type="button"
            role="switch"
            :aria-checked="enabled ? 'true' : 'false'"
            :disabled="busy"
            @click.prevent="setEnabled(!enabled)"
            class="relative shrink-0 rounded-full transition-colors disabled:opacity-50"
            :class="enabled ? 'bg-emerald-500' : 'bg-gray-300'"
            style="height: 18px; width: 32px;"
          >
            <span
              class="absolute top-0.5 h-3.5 w-3.5 rounded-full bg-white shadow transition-all"
              :class="enabled ? 'left-4' : 'left-0.5'"
            ></span>
          </button>
          <span>榜单注入</span>
        </label>
        <span class="text-[11px] leading-snug text-gray-400">
          {{ enabled ? '已打开：勾中的榜单会在飞牛音乐里出现（只读，在飞牛里加曲/删曲不会生效）' : '已关闭：飞牛音乐里不会出现任何榜单歌单，勾选已保留' }}
        </span>

        <span
          class="rounded-full px-2 py-0.5 text-[10px] font-bold whitespace-nowrap"
          :class="selectedCount ? 'bg-emerald-50 text-emerald-600' : 'bg-gray-100 text-gray-400'"
        >已选 {{ selectedCount }} / {{ total }}</span>

        <div class="ml-auto flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            :disabled="busy || !total"
            @click="selectAllLoaded"
            class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-emerald-300 hover:bg-emerald-50 hover:text-emerald-600 disabled:opacity-40"
          >全部启用</button>
          <button
            type="button"
            :disabled="busy || !selectedCount"
            @click="replaceIds([])"
            class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-gray-300 hover:bg-gray-50 disabled:opacity-40"
          >全部关闭</button>
          <span class="mx-0.5 h-4 w-px bg-gray-200"></span>
          <button
            type="button"
            :disabled="busy"
            @click="selectOnly('wy')"
            class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-red-200 hover:bg-red-50 hover:text-red-500 disabled:opacity-40"
          >仅网易云</button>
          <button
            type="button"
            :disabled="busy"
            @click="selectOnly('tx')"
            class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-emerald-200 hover:bg-emerald-50 hover:text-emerald-600 disabled:opacity-40"
          >仅 QQ 音乐</button>
        </div>
      </div>

      <!--
        页面注入：比榜单注入更「外面」的一层。关掉后转发层**根本不认领**飞牛自带的页面文档，
        官方页面逐字节原样 —— 出问题时的退路。改完要**刷新飞牛页面**才生效。
      -->
      <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-gray-100 pt-3">
        <label
          class="flex cursor-pointer select-none items-center gap-2 text-[12.5px] font-bold transition-colors"
          :class="uiInject ? 'text-emerald-600' : 'text-gray-400'"
          :title="uiInject
            ? '飞牛自带的音乐页里会多出「下载到曲率」等曲率补的入口。关掉它就完全没有 —— 官方页面逐字节原样'
            : '已关闭：飞牛自带的音乐页是官方原样，曲率补的入口都不会出现'"
        >
          <button
            type="button"
            role="switch"
            :aria-checked="uiInject ? 'true' : 'false'"
            :disabled="busy"
            @click.prevent="setUIInject(!uiInject)"
            class="relative shrink-0 rounded-full transition-colors disabled:opacity-50"
            :class="uiInject ? 'bg-emerald-500' : 'bg-gray-300'"
            style="height: 18px; width: 32px;"
          >
            <span
              class="absolute top-0.5 h-3.5 w-3.5 rounded-full bg-white shadow transition-all"
              :class="uiInject ? 'left-4' : 'left-0.5'"
            ></span>
          </button>
          <span>页面注入</span>
        </label>
        <span class="min-w-0 flex-1 text-[11px] leading-snug text-gray-400">
          飞牛自带音乐页里曲率补的入口（如「下载到曲率」）。
          {{ uiInject ? '关掉后官方页面逐字节原样' : '当前已关闭，官方页面原样' }} —— 改完刷新飞牛页面才看得到。
        </span>
      </div>

      <p v-if="error" class="mt-2 text-[11px] text-red-500">{{ error }}</p>
      <p v-else-if="loadingConfig" class="mt-2 text-[11px] text-gray-400">正在读取当前勾选…</p>
    </div>

    <!-- 卡片 2/3：按平台分组 -->
    <div
      v-for="g in groups"
      :key="g.id"
      class="mt-4 rounded-xl border border-gray-100 bg-white p-4"
    >
      <div class="flex flex-wrap items-center gap-2">
        <span class="h-2 w-2 shrink-0 rounded-full" :class="g.dot"></span>
        <div class="text-[13px] font-bold text-gray-800">
          {{ g.name }}榜单
          <span class="font-normal text-gray-400">（{{ g.items.length }} 个）</span>
        </div>
        <Loader2 v-if="g.loading" class="h-3.5 w-3.5 animate-spin text-emerald-500" />
        <span
          v-if="g.items.length"
          class="rounded-full bg-gray-50 px-2 py-0.5 text-[10px] font-bold text-gray-400"
        >本平台已选 {{ keysOf(g).filter(k => ids.includes(k)).length }} / {{ g.items.length }}</span>
        <div class="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            :disabled="busy || !g.items.length"
            @click="selectPlatform(g, true)"
            class="rounded-lg px-2.5 py-1 text-[11px] font-bold text-gray-500 transition-colors hover:bg-emerald-50 hover:text-emerald-600 disabled:opacity-40"
          >全选</button>
          <button
            type="button"
            :disabled="busy || !g.items.length"
            @click="selectPlatform(g, false)"
            class="rounded-lg px-2.5 py-1 text-[11px] font-bold text-gray-500 transition-colors hover:bg-gray-100 disabled:opacity-40"
          >全清</button>
        </div>
      </div>
      <p class="mt-1 text-[11px] text-gray-400">{{ g.hint }}</p>

      <p v-if="g.error" class="mt-2 text-[11px] text-red-500">
        {{ g.error }}
        <button type="button" class="ml-1 font-bold underline" @click="loadGroup(g, true)">重试</button>
      </p>

      <div
        v-if="g.items.length"
        class="mt-3 grid gap-2.5"
        style="grid-template-columns: repeat(auto-fill, minmax(196px, 1fr));"
      >
        <div
          v-for="item in g.items"
          :key="item.id"
          @click="toggleItem(item, g.id)"
          class="group relative flex cursor-pointer select-none items-center gap-2.5 rounded-xl border p-2.5 transition-all"
          :class="isOn(item, g)
            ? 'border-emerald-400 bg-emerald-50/60'
            : 'border-gray-100 bg-white hover:border-emerald-200 hover:bg-emerald-50/30'"
        >
          <!-- 封面：加载失败只藏图，占位图标还在（不塌陷） -->
          <div class="relative h-11 w-11 shrink-0 overflow-hidden rounded-lg bg-gray-100">
            <Music2 class="absolute left-1/2 top-1/2 h-4 w-4 -translate-x-1/2 -translate-y-1/2 text-gray-300" />
            <img
              v-if="item.cover"
              :src="item.cover"
              alt=""
              loading="lazy"
              referrerpolicy="no-referrer"
              @error="hideCover"
              class="absolute inset-0 h-full w-full object-cover"
            />
          </div>
          <div class="min-w-0 flex-1">
            <div
              class="truncate text-[12.5px] font-bold"
              :class="isOn(item, g) ? 'text-emerald-700' : 'text-gray-800'"
              :title="item.name"
            >{{ item.name }}</div>
            <div class="mt-0.5 truncate text-[10.5px] text-gray-400">{{ subLine(item) }}</div>
          </div>
          <!-- 看一眼：跳到「排行榜单」打开这张榜单的曲目（管理页只负责选） -->
          <button
            type="button"
            title="去排行榜单看它的曲目"
            @click.stop="emit('preview', { id: item.id, source: g.id, name: item.name, cover: item.cover, update_frequency: item.update_frequency })"
            class="shrink-0 rounded-full p-1 text-gray-300 opacity-0 transition-all hover:bg-white hover:text-emerald-600 group-hover:opacity-100"
          >
            <Play class="h-3.5 w-3.5 fill-current" />
          </button>
          <Check
            v-if="isOn(item, g)"
            class="h-4 w-4 shrink-0 text-emerald-500"
          />
        </div>
      </div>
    </div>

    <!-- 导流：听 vs 存 -->
    <div class="mt-4 rounded-xl border border-dashed border-gray-200 bg-white/60 p-3.5">
      <p class="text-[11.5px] leading-relaxed text-gray-500">
        这里管的是<strong class="text-gray-700">听</strong>：榜单在线播放，不占硬盘。
        想把榜单里的新歌<strong class="text-gray-700">存</strong>下来（下到本地曲库 / 同步进飞牛歌单），
        那是<strong class="text-gray-700">订阅</strong> —— 去「曲库管家 → 歌单监控」订阅同一张榜单，
        或者在「排行榜单」里点开某张榜单、用右上角的「订阅更新」。
        两件事互不冲突：订阅不会让榜单出现在飞牛的歌单列表里，注入也不会帮你下载。
      </p>
    </div>
  </div>
</template>
