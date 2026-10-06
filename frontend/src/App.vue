<template>
  <!--
    ⚠️ 这里以前是 `h-screen w-screen`：移动端浏览器地址栏会把 100vh 的一部分占掉，
    底部播放条被顶到可视区之外（露一半或整条看不见）；`w-screen` 还会在 iOS 上横向溢出。
    改成 `.app-viewport`（style.css 里的 100dvh）—— 手机横竖屏、地址栏收起/展开都跟着走。
  -->
  <div class="app-viewport flex flex-col w-full bg-[#f5f7f9] text-gray-800 overflow-hidden select-none font-sans">

    <!-- ── Immersive Player (shown when playing) ── -->
    <ImmersivePlayer
      v-if="showImmersive && currentSong"
      :current-song="currentSong"
      :is-playing="isPlaying"
      :current-time="currentTime"
      :duration="duration"
      :lyric-data="lyricData"
      :trans-data="transData"
      :playlist-count="playlist.length"
      :play-mode="playMode"
      :playlist="playlist"
      :current-index="currentIndex"
      @update:play-mode="val => playMode = val"
      @collapse="showImmersive = false"
      @toggle-play="togglePlay"
      @prev="playPrev"
      @next="playNext"
      @seek="onSeek"
      @volume-change="onVolumeChange"
      @select-track="playFromQueue"
      @clear-playlist="clearPlaylist"
    />

    <!-- ── Normal View ── -->
    <!--
      Top Navigation Bar（桌面尺度对齐 fnmusic-flow 的 .topbar：高 68px / 内边距 30px / 间距 28px / 品牌 20px）

      **桌面端完全按原样**（`sm:` 以上）：品牌（`lg` 才显示文字）+ 图标&名称、`h-[68px]`、`gap-7`、`px-8`、
      `whitespace-nowrap`、`overflow-hidden` —— 一个字都没改。

      **只有手机改：单行 + 只留图标**（用户 2026-09-26 定的口径）。
      原因：品牌 + 5 个导航项 + 右侧 3 个入口本来有 460px+，而原实现是全宽度通用的
      `whitespace-nowrap + overflow-hidden` 单行 ⇒ 手机上「音源 / 开放接口 / 打开播放器」三个入口
      **直接被裁到屏幕外、点不到**。
      ❗**别用折行解决**（第一版折了两行，用户原话否掉：「顶部菜单显示不全的话，可以不显示文字啊，不要两行」）。
      做法：导航项手机只显示 14px 图标（`p-2.5` ⇒ 34px 一个，名称靠 `sm:inline` 才出现）、
      音源按钮文字截到 `max-w-[56px]`、开放接口 / 打开播放器本来就只在手机显示图标。
      导航容器挂 `min-w-0 overflow-x-auto no-scrollbar`：极端窄屏（≤360px）时它自己横向滚动，
      **任何入口都不许被裁掉**（滚动是最后一道保险）。
      手机上只剩图标的按钮，必须给 `:title` + `:aria-label`。

      ⚠️ 顺序固定为「品牌 → 导航 → 右侧入口（`ml-auto` 顶到最右）」，别把导航排到右侧入口后面 —— 位置会错。

      ⚠️ `pt-safe` 挂在 header 上、真实内边距挂在内层 div 上 —— 两者挂同一元素会互相覆盖
      （自定义工具类排在核心工具类之后，`pt-safe` 会把 `py-2` 顶掉）。
    -->
    <header class="pt-safe bg-white border-b border-gray-100 z-20 shrink-0">
      <div ref="headerRowRef" class="flex items-center gap-x-2 px-3 py-2 sm:h-[68px] sm:gap-7 sm:px-8 sm:py-0 sm:whitespace-nowrap sm:overflow-hidden">

      <!--
        品牌 + 开放接口入口。
        2026-09-27 用户要求：原来右上角那个带文字的「开放接口」按钮挪到**软件图标本身**
        ——点图标就滑出开放接口抽屉，图标保留。
        提示只用**动态图标**（外圈旋转光环 + 图标呼吸，见 style.css 的 .brand-halo），
        不写任何提示文字；抽屉开着时动效暂停。
        品牌文字保留；`shrink-0` 别动，否则窄屏会被导航挤扁。
      -->
      <button
        type="button"
        class="brand-halo group flex shrink-0 items-center gap-2.5 rounded-[13px] cursor-pointer"
        :class="{ 'brand-halo-open': showApiDrawer }"
        title="开放接口 · 供 AI 与外部程序调用"
        aria-label="开放接口"
        @click="showApiDrawer = true"
      >
        <span class="relative grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-emerald-500 text-white transition-colors group-hover:bg-emerald-600">
          <span class="brand-core-glow" aria-hidden="true"></span>
          <Disc class="brand-disc w-[18px] h-[18px]" />
          <!-- 中间圆点（静止，盖住 Disc 的内圈）+ 它外层那个**呼吸的圆圈** -->
          <span class="brand-core" aria-hidden="true"></span>
          <span class="brand-core-ring" aria-hidden="true"></span>
        </span>
        <span class="hidden lg:block leading-tight text-left">
          <b class="block text-[20px] font-extrabold tracking-[-0.5px] text-gray-800">曲率</b>
          <small class="block text-[11px] font-medium text-gray-400">AI Music Hub</small>
        </span>
      </button>

      <!-- 分段切换器：灰底容器 + 白色胶囊高亮当前项（flow 的 .switcher：padding 4px / radius 11px / gap 3px）
           ⚠️ 顺序必须是「品牌 → 导航 → 右侧入口」（右侧那组靠 `ml-auto` 顶到最右），
           **别把导航排到右侧入口后面** —— 那样胶囊会被顶到屏幕最右边，位置就错了。
           ⚠️ 手机上**只留图标**（名称靠 `sm:inline` 才出现），理由见本节开头。 -->
      <nav ref="navElRef" class="flex min-w-0 items-center gap-0.5 bg-gray-100 p-1 rounded-[11px] overflow-x-auto no-scrollbar sm:shrink-0">
        <button
          v-for="item in navItems"
          :key="item.id"
          @click="currentView = item.id"
          :title="item.name"
          :aria-label="item.name"
          :class="[
            'p-2.5 sm:px-[18px] sm:py-2 rounded-lg text-[13px] font-bold transition-all flex items-center gap-1.5 whitespace-nowrap shrink-0',
            currentView === item.id
              ? 'bg-white text-emerald-600 shadow-sm'
              : 'text-gray-500 hover:text-gray-700'
          ]"
        >
          <component :is="item.icon" class="w-3.5 h-3.5 shrink-0" />
          <!-- 名称按**实测放不放得下**决定（见 fitNavDensity）：full=全名 / short=精简名 / icon=只留图标。
               ⚠️ 这里不再用 `sm:` 断点判断宽窄 —— 用户 2026-09-28 反馈「上方的空了一大半就判定为最小的图标了」，
                  断点那套在「窗口宽但容器窄」「侧栏预览」这类情况下会白扔一大片空间。 -->
          <span v-if="navDensityEffective === 'full'">{{ item.name }}</span>
          <span v-else-if="navDensityEffective === 'short'">{{ item.short }}</span>
          <!-- 下载队列进行中数量角标（下载已并入曲库管家，角标挂在它上面） -->
          <span
            v-if="item.id === 'library' && downloadManager.activeCount.value > 0"
            class="px-1.5 rounded-full bg-emerald-500 text-white text-[10px] font-bold animate-pulse shrink-0"
          >
            {{ downloadManager.activeCount.value }}
          </span>
        </button>

        <!-- 显示密度切换：「大 / 中 / 小」三档循环，用图标表达当前档位（文字说明走 title） -->
        <button
          @click="cycleNavDensity"
          class="ml-0.5 hidden h-7 w-7 shrink-0 place-items-center rounded-lg text-gray-400 transition-colors hover:bg-white hover:text-emerald-600 sm:grid"
          :title="navDensityTitle"
          :aria-label="navDensityTitle"
        >
          <component :is="navDensityIcon" class="w-3.5 h-3.5" />
        </button>
      </nav>

      <div class="ml-auto flex items-center gap-1.5 sm:gap-2.5 shrink-0">
        <button
          @click="currentView = 'sources'"
          class="px-2 sm:px-3 py-2 sm:py-1.5 rounded-full bg-emerald-50 border border-emerald-200 text-emerald-700 text-xs font-medium flex min-w-0 items-center gap-1 sm:gap-1.5 hover:bg-emerald-100 transition-all max-w-[76px] sm:max-w-[170px] lg:max-w-none whitespace-nowrap shrink"
          :title="`音源管理 · 当前音源：${activeSourceMeta?.name || '未设置'}`"
        >
          <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="activeSourceMeta ? 'bg-emerald-500' : 'bg-amber-400'"></span>
          <!-- 窄屏只放假两字（长名会把胶囊顶出屏幕，用户 2026-09-28 反馈）；宽屏给全名。两种都不许溢出。 -->
          <span class="truncate text-[11px] sm:hidden">{{ shortSourceName }}</span>
          <span class="hidden truncate text-[11px] sm:inline sm:text-xs">{{ activeSourceMeta?.name || '音源' }}</span>
        </button>

        <!-- Open immersive player button -->
        <button
          v-if="currentSong"
          @click="showImmersive = true"
          class="p-2 sm:p-1.5 rounded-lg border border-emerald-200 text-emerald-600 bg-emerald-50 hover:bg-emerald-100 transition-all shrink-0"
          title="打开播放器"
        >
          <Disc class="w-4 h-4" />
        </button>
      </div>

      </div>
    </header>

    <!-- ── 全局下载队列抽屉（快捷查看；完整页面见导航「下载队列」） ── -->
    <DownloadDrawer :active-source="activeSourceMeta" @open-full="openQueuePage" />

    <!--
      下载目录弹窗**全局挂一次**：它由 `downloadManager.showDirModal` 驱动，
      「发现」页等多处按钮都会打开它，不能绑在下载抽屉的 `v-if` 里
      —— 抽屉关着时点了没反应（见组件内注释）。
    -->
    <DownloadDirModal />

    <!-- ── 开放接口抽屉（侧边滑出，与下载抽屉同款交互） ── -->
    <ApiDrawer v-model="showApiDrawer" />

    <!-- ── 全局免责声明与服务条款弹窗 ── -->
    <DisclaimerModal
      v-model="showDisclaimerModal"
      :is-force="isDisclaimerForce"
    />

    <!-- Main Content -->
    <!-- 主内容区：左右滑动切页（跟手位移 / 到头阻尼 / 速度判定，见 components/SwipePager.vue） -->
    <SwipePager :order="swipeOrderList" :current="currentView" @update:current="v => currentView = v">
      <template #view-search>
        <SearchView
          :active-source="activeSourceMeta"
          :active-source-ids="activeSourceIds"
          :play-mode="playMode"
          :current-song="currentSong"
          :is-playing="isPlaying"
          @update:play-mode="val => playMode = val"
          @play="playSong"
          @play-all="playAllSongs"
          @navigate="view => currentView = view"
          @prev="playPrev"
          @next="playNext"
          @toggle-play="togglePlay"
          :bar-query="barSearchQuery"
          :bar-search-nonce="barSearchNonce"
        />
      </template>
      <template #view-nas><NasExplorer @play="playSong" /></template>
      <template #view-sources>
        <SourceManager
          @source-changed="onSourceChanged"
          @pool-changed="onPoolChanged"
          @open-disclaimer="openDisclaimer(false)"
        />
      </template>
      <template #view-library>
        <LibraryManager
          :active-source="activeSourceMeta"
          :tab="libraryTab"
          :tab-nonce="libraryNavNonce"
        />
      </template>
      <template #view-accounts><AccountManager /></template>
      <template #view-logs><LogViewer /></template>
    </SwipePager>

    <!-- 帧率探针：只有地址带 ?fps=1 时出现（真机上用来判断"卡不卡"，正式界面看不到） -->
    <FpsMeter v-if="showFpsMeter" />

    <!-- 全局轻提示（替代 alert）：失败时可点击直达「日志」看详情 -->
    <button
      v-if="appToast"
      class="fixed bottom-24 left-1/2 z-40 max-w-[90vw] -translate-x-1/2 rounded-xl bg-gray-900/85 px-4 py-2.5 text-[12px] leading-relaxed text-white shadow-lg"
      :class="appToast.goLogs ? 'cursor-pointer hover:bg-gray-900' : 'cursor-default'"
      @click="onToastClick"
    >{{ appToast.text }}</button>

    <!-- Bottom Player Bar -->
    <PlayerBar
      :current-song="currentSong"
      :is-playing="isPlaying"
      :current-time="currentTime"
      :duration="duration"
      :show-lyrics="false"
      :is-resolving="isResolving"
      :play-mode="playMode"
      :collapsed="playerBarCollapsed"
      @update:play-mode="val => playMode = val"
      @toggle-play="togglePlay"
      @prev="playPrev"
      @next="playNext"
      @seek="onSeek"
      @toggle-lyrics="() => { showImmersive = true }"
      @toggle-fullscreen="() => { showImmersive = true }"
      @toggle-view="v => currentView = v"
      @volume-change="onVolumeChange"
      @toggle-collapse="playerBarCollapsed = !playerBarCollapsed"
      @search="searchFromPlayerBar"
    />

    <audio
      ref="audioRef"
      preload="auto"
      @timeupdate="onTimeUpdate"
      @durationchange="onDurationChange"
      @ended="onEnded"
      @play="isPlaying = true"
      @pause="isPlaying = false"
      @error="onAudioError"
    ></audio>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { lxRuntime } from './engine/lx-runtime'
import { SearchAPI, SourcesAPI, NasAPI } from './api/client'
import SearchView from './components/SearchView.vue'
import NasExplorer from './components/NasExplorer.vue'
import SourceManager from './components/SourceManager.vue'
import AudioVisualizer from './components/AudioVisualizer.vue'
import VinylTurntable from './components/VinylTurntable.vue'
import LyricView from './components/LyricView.vue'
import PlayerBar from './components/PlayerBar.vue'
import ImmersivePlayer from './components/ImmersivePlayer.vue'
import DownloadDrawer from './components/DownloadDrawer.vue'
import DownloadDirModal from './components/DownloadDirModal.vue'
import LibraryManager from './components/LibraryManager.vue'
import ApiDrawer from './components/ApiDrawer.vue'
import DisclaimerModal from './components/DisclaimerModal.vue'
import AccountManager from './components/AccountManager.vue'
import SwipePager from './components/SwipePager.vue'
import FpsMeter from './components/FpsMeter.vue'
import LogViewer from './components/LogViewer.vue'
import { downloadManager } from './services/downloadManager'
import { bridgeWorker } from './services/bridgeWorker'
import { loadPref, savePref, loadBool, initUiPrefs } from './services/prefs'
import { onNavigate } from './services/navBus'
import { invalidate } from './services/viewCache'
import { logError } from './services/appLog'
import { Compass, HardDrive, Cpu, Disc, Library, UserRound, ScrollText, LayoutList, AlignJustify, LayoutGrid } from 'lucide-vue-next'

// ?fps=1 → 显示帧率探针（见 components/FpsMeter.vue）
const showFpsMeter = typeof location !== 'undefined' && location.search.includes('fps=1')

const currentView = ref('search')

// 组件里的「查看日志」等入口通过导航总线请求切视图 —— 免去给每个组件加 emit + props
// （见 services/navBus.js 的说明）。currentView 的归属不变，这里只是多接一个触发源。
const offNavigate = onNavigate((view) => { currentView.value = view })

// 滑动切页的视图顺序（computed 是惰性的，即使 navItems 声明在后面也不会踩 TDZ）
const swipeOrderList = computed(() => navItems.map((item) => item.id))

// 写操作 → 数据失效（见 services/viewCache.js 的「事件失效」）：
// 下载完成意味着磁盘上多了文件，「本地音乐」「曲库管家」两页的缓存就旧了。
// 不在这里直接发请求，只标过期 —— 用户切过去时（onActivated）才刷，避免无谓的后台流量。
watch(
  () => downloadManager.completedCount.value,
  (now, before) => { if (before !== undefined && now > before) invalidate('nas', 'library') },
)

onUnmounted(offNavigate)
onUnmounted(() => { if (navResizeObserver) { navResizeObserver.disconnect(); navResizeObserver = null } })

/**
 * 全局轻提示（替代 alert）。
 *
 * 约定：界面上只给一句结论，完整信息进日志（见 services/appLog.js）。
 * `goLogs` 为 true 时点一下直达「日志」页 —— 比让用户自己找入口友好得多。
 */
const appToast = ref(null) // { text, goLogs }
let appToastTimer = null

function showAppToast(text, goLogs = false) {
  appToast.value = { text, goLogs }
  if (appToastTimer) clearTimeout(appToastTimer)
  appToastTimer = setTimeout(() => { appToast.value = null }, 3600)
}

function onToastClick() {
  if (!appToast.value?.goLogs) return
  currentView.value = 'logs'
  appToast.value = null
}
const showRightPanel = ref(false)
const showImmersive = ref(false)
const stageTab = ref('turntable')

// 免责声明与条款弹窗状态
const showDisclaimerModal = ref(false)
const isDisclaimerForce = ref(false)

// 开放接口抽屉
const showApiDrawer = ref(false)

// 播放条收起状态（用户 2026-09-28：「播放时，在右边增加一个收缩图标」）。
// 只影响这一条栏，不打断音频播放；展开入口在细条上。
const playerBarCollapsed = ref(false)

// 播放栏发起的搜索（用户 2026-09-28 定：搜索的入口在播放栏，发现页不再放搜索框）。
// nonce 是给 SearchView 的触发信号 —— 同一个词连搜两次也要能跑起来。
const barSearchQuery = ref('')
const barSearchNonce = ref(0)
function searchFromPlayerBar(q) {
  barSearchQuery.value = q
  currentView.value = 'search'
  barSearchNonce.value += 1
}


function openDisclaimer(force = false) {
  isDisclaimerForce.value = force
  showDisclaimerModal.value = true
}

// 主导航（顶栏分段切换器）。
// 顺序参考 fnmusic-flow 的工作台布局：先内容、后管理、最后账号。
// 「音源管理」不在此列 —— 它是配置项，放在顶栏右侧的当前音源按钮里。
// 「推送同步」「下载」都不在此列 —— 已并入「曲库管家」，作为其侧栏项（见 LibraryManager）。
// 每个导航项两个名字：`name` 是完整名，`short` 是顶栏「精简档」用的短名
// （用户 2026-09-28 要求的三档密度见 navDensity）。
const navItems = [
  { id: 'search', name: '发现音乐', short: '发现', icon: Compass },
  { id: 'nas', name: '本地音乐', short: '本地', icon: HardDrive },
  { id: 'library', name: '曲库管家', short: '管家', icon: Library },
  { id: 'accounts', name: '账号连接', short: '账号', icon: UserRound },
  // 日志：后端运行日志 + 任务执行记录。放在最后 —— 它是排查用的工具页，
  // 不是日常主功能，但仍要一眼能找到（原先只能 SSH 上 NAS 看 backend.log）
  { id: 'logs', name: '日志', short: '日志', icon: ScrollText },
]

// ── 顶栏导航的显示密度：三档（用户 2026-09-28 要求）──
// 大 `full` = 图标 + 完整名称；中 `short` = 图标 + 精简名称（发现/本地/管家/账号/日志）；
// 小 `icon` = 只有图标（title / aria-label 里仍带全名，信息不丢）。
// 选择存进服务端偏好 nav_density，刷新与换端都保留；默认保持原来的桌面观感（完整名称）。
// 手机上（<640px）**无论哪一档都只显示图标** —— 窄屏要塞下 5 个导航 + 右侧入口，
// 多一个字的宽度就会把入口挤出去（见模板里那段注释）。
const navDensities = ['full', 'short', 'icon']
/** 用户选的档位 —— 它是**上限**，不是最终结果（放不下会自动降档，见 fitNavDensity） */
const navDensity = ref(loadPref('nav_density', 'full') || 'full')
/** 实际渲染用的档位 */
const navDensityEffective = ref(navDensity.value)
const navElRef = ref(null)
const headerRowRef = ref(null)
let navResizeObserver = null
let navFitting = false

/**
 * 按**实测宽度**决定导航显示档位：从用户选的档位开始，放不下就降一档再量，直到放下。
 *
 * 为什么不用 `sm:` 断点（原来的做法）：断点只看视口宽，不看这一行里其它东西占了多少 ——
 * 用户 2026-09-28 反馈「上方的空了一大半就判定为最小的图标了」，就是断点 + 侧栏/容器宽度
 * 对不上导致的。实测的是两个溢出信号：顶栏那一行溢出，或导航条内部溢出。
 */
async function fitNavDensity() {
  if (navFitting) return
  const row = headerRowRef.value
  const nav = navElRef.value
  if (!row || !nav) return
  navFitting = true
  try {
    const start = Math.max(0, navDensities.indexOf(navDensity.value))
    for (let i = start; i < navDensities.length; i++) {
      navDensityEffective.value = navDensities[i]
      await nextTick()
      const rowFits = row.scrollWidth <= row.clientWidth + 1
      const navFits = nav.scrollWidth <= nav.clientWidth + 1
      if (rowFits && navFits) return
    }
  } finally {
    navFitting = false
  }
}
const navDensityIcon = computed(() => ({ full: LayoutList, short: AlignJustify, icon: LayoutGrid }[navDensity.value] || LayoutList))
const navDensityTitle = computed(() => ({
  full: '菜单显示：图标 + 完整文字（点击切到精简）',
  short: '菜单显示：图标 + 精简文字（点击切到仅图标）',
  icon: '菜单显示：仅图标（点击切回完整文字）',
}[navDensity.value] || '切换菜单显示方式'))
function cycleNavDensity() {
  const idx = navDensities.indexOf(navDensity.value)
  navDensity.value = navDensities[(idx + 1) % navDensities.length]
  savePref('nav_density', navDensity.value)
  fitNavDensity()
}

// 音源名可能很长（用户 2026-09-28：「音源名长了后卡片会超出屏幕，可以优化为缩小后只显示前两个字」）：
// 窄屏胶囊里只放前两个字，完整名在宽屏显示 + title 里始终能看全。
const shortSourceName = computed(() => {
  const full = (activeSourceMeta.value?.name || '').trim()
  if (!full) return '音源'
  return full.length > 2 ? full.slice(0, 2) : full
})

/**
 * 从下载抽屉跳到「曲库管家 → 下载」。
 *
 * LibraryManager 的侧栏项有自己的持久化记忆，光设 pref 对已挂载的实例无效，
 * 所以额外用一个自增 nonce 触发它的 watch。
 */
const libraryTab = ref('')
const libraryNavNonce = ref(0)
function openQueuePage() {
  downloadManager.close()
  libraryTab.value = 'download'
  libraryNavNonce.value++
  currentView.value = 'library'
}

const audioRef = ref(null)
const audioEl = ref(null)
const isPlaying = ref(false)
const isResolving = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const currentSong = ref(null)
const playlist = ref([])
const currentIndex = ref(0)
const lyricData = ref('')
const transData = ref('')
const savedSource = loadPref('active_source_meta', '')
let initialSource = null
try {
  if (savedSource) initialSource = JSON.parse(savedSource)
} catch (_) {}

const activeSourceMeta = ref(initialSource)

/**
 * 启用池（多选激活，2026-09-30 用户定）：有序数组，前面的优先。
 * `activeSourceMeta` 保持 = **池首** —— 下游（播放栏、下载、曲库管家、AI 桥）全都不用改，
 * 而"池里第一个能干的"这件事由 SearchView 用 `activeSources` 现算。
 */
const activeSourceIds = ref([])
const allSourceMetas = ref([])
const activeSourceMetas = computed(() =>
  activeSourceIds.value.map(id => allSourceMetas.value.find(s => s.id === id)).filter(Boolean),
)
/** 池内挑源：给定平台 → 第一个支持它的源（都不支持则回退池首） */
function sourceIdForPlatform(platform) {
  return lxRuntime.pickSourceForPlatform(activeSourceIds.value, platform)
}

watch(activeSourceMeta, (meta) => {
  if (meta) {
    savePref('active_source_meta', JSON.stringify(meta))
  } else {
    savePref('active_source_meta', '')
  }
}, { deep: true })

const playMode = ref(loadPref('play_mode', 'sequence') || 'sequence')

watch(playMode, (val) => {
  savePref('play_mode', val)
})

// 注意：这里以前有个 watch(isPlaying) 会在「开始播放」时自动展开沉浸式播放器，
// 于是点列表里的歌、切上一首/下一首都会强制弹全屏（2026-09-27 用户反馈）。
// 现在只有用户主动操作才展开：顶栏「打开播放器」、播放条上的歌词/全屏按钮。

onMounted(async () => {
  audioEl.value = audioRef.value
  downloadManager.loadConfig()

  // 导航密度按实测宽度自适应（见 fitNavDensity）：首次挂载量一次，之后行宽变了重量
  fitNavDensity()
  if (headerRowRef.value && typeof ResizeObserver !== 'undefined') {
    navResizeObserver = new ResizeObserver(() => { fitNavDensity() })
    navResizeObserver.observe(headerRowRef.value)
  }

  // 启动 AI 解析桥工作器：让外部 AI 提交的取链任务能在本页面完成
  bridgeWorker.start()

  // 0. 免责声明与服务条款：**只需同意一次**。
  //
  // 本地没有记录时，再向服务端确认一次（窗口记忆存在应用数据目录，多端共享）。
  // 这一步是必要的：用 IP 和用域名访问属于不同 origin，localStorage 不互通，
  // 只看本地会出现「明明同意过又弹」。
  // 加 1.5s 上限 —— 后端慢或不可用时也不能卡住启动，最多多问这一次。
  if (!loadBool('disclaimer_accepted')) {
    await Promise.race([initUiPrefs(), new Promise((r) => setTimeout(r, 1500))])
  }
  if (!loadBool('disclaimer_accepted')) {
    setTimeout(() => {
      openDisclaimer(true)
    }, 150)
  }

  // 1. 加载音源
  try {
    const res = await SourcesAPI.list()
    if (res.code === 200) {
      const srcs = res.data.sources || []
      allSourceMetas.value = srcs
      // 启用池：新字段优先；老后端只给单值 active_id 时降级成单元素池
      const poolIds = Array.isArray(res.data.active_source_ids) && res.data.active_source_ids.length
        ? res.data.active_source_ids
        : (res.data.active_id ? [res.data.active_id] : [])
      activeSourceIds.value = poolIds
      const meta = srcs.find(s => s.id === poolIds[0]) || null
      activeSourceMeta.value = meta
      // 预先加载所有已导入的音源脚本，确保主音源失效时能够秒级无缝自动容灾降级！
      for (const s of srcs) {
        try {
          const scriptRes = await SourcesAPI.getScript(s.id)
          if (scriptRes.code === 200) {
            await lxRuntime.loadScript(s, scriptRes.data.script)
          }
        } catch (e) {
          console.warn('Failed to load source:', s.id, e)
        }
      }

      // 服务型音源（按服务地址接入）没有脚本，单独登记进运行时，
      // 这样它们才能和脚本型音源一起参与取链轮询与熔断。
      try {
        const apiRes = await SourcesAPI.apiList()
        if (apiRes?.code === 200) {
          lxRuntime.setApiSources(apiRes.data?.sources || [])
        }
      } catch (e) {
        console.warn('Failed to load API sources:', e)
      }
    }
  } catch (e) {
    console.warn('Failed to load active source:', e)
  }
})

function onSourceChanged(meta) {
  activeSourceMeta.value = meta
}

/**
 * 音源管理页改动了启用池 → 同步到全局。
 * `list` 是池内各源的 meta（有序）；池首同时写进 activeSourceMeta（下游只认它）。
 */
function onPoolChanged({ ids, metas }) {
  activeSourceIds.value = Array.isArray(ids) ? ids : []
  if (Array.isArray(metas)) allSourceMetas.value = metas
  activeSourceMeta.value = (metas || []).find(m => m.id === activeSourceIds.value[0]) || null
}

function isFakeOrNoticeAudio(url) {
  if (!url || typeof url !== 'string') return true
  const lower = url.toLowerCase()
  return lower.includes('panspace') || lower.includes('notice') || lower.includes('audio_forbidden') || lower.includes('error.mp3')
}

let isSwitchingDirect = false
let currentSessionId = 0
let lastFailedSongId = null

async function playSong(songOrPayload) {
  // 只切歌、不展开大屏：列表点歌与切歌时界面不该被全屏盖住（2026-09-27 用户反馈）

  let song = songOrPayload
  if (songOrPayload && songOrPayload.song) {
    song = songOrPayload.song
    if (songOrPayload.playlist && Array.isArray(songOrPayload.playlist) && songOrPayload.playlist.length > 0) {
      playlist.value = [...songOrPayload.playlist]
      const foundIdx = songOrPayload.index !== undefined ? songOrPayload.index : playlist.value.findIndex(s => s.id === song.id)
      currentIndex.value = foundIdx >= 0 ? foundIdx : 0
    }
  } else if (song) {
    const idx = playlist.value.findIndex(s => s.id === song.id)
    if (idx === -1) {
      playlist.value.push(song)
      currentIndex.value = playlist.value.length - 1
    } else {
      playlist.value[idx] = song
      currentIndex.value = idx
    }
  }

  if (!song) return

  const thisSession = ++currentSessionId
  lastFailedSongId = null

  // 1. 立即停止上一首音频并完全卸载（使用 removeAttribute('src') 避免触发浏览器虚假 error 事件）
  if (audioRef.value) {
    try {
      audioRef.value.pause()
      audioRef.value.removeAttribute('src')
      audioRef.value.load()
    } catch (_) {}
  }
  isPlaying.value = false
  currentTime.value = 0
  duration.value = 0
  lyricData.value = ''
  transData.value = ''
  isResolving.value = true
  currentSong.value = { ...song }

  // NAS 本地音频直接播放并同步加载歌词
  if (song.source === 'nas' && song.streamUrl) {
    fetchNasLyric(song, thisSession)
    startAudioPlay(song.streamUrl, thisSession)
    isResolving.value = false
    return
  }

  const sourceId = activeSourceMeta.value?.id || ''
  if (!sourceId) {
    isResolving.value = false
    // 官方协议负责搜索 / 榜单 / 歌单 / 歌词（后端内置，免脚本）；
    // 只有「把歌曲解析成可播放直链」这一步需要 LX 协议。所以这里只拦播放，不拦浏览。
    //
    // ⚠️ 确认框只问「要不要去导入」，**不解释机制** —— 用户在这一步需要的是选择，
    // 不是「LX 协议 vs 官方协议」的科普（那是日志/文档的事，见 HANDOVER §3.3.3）。
    const go = confirm('还没有可用的音源，无法播放。\n\n现在去「音源管理」导入一个吗？')
    if (go) {
      currentView.value = 'sources'
    }
    return
  }

  const platform = song.source || 'kw'

  // 3. 立即并行拉取新渠道的对应歌词，无需等待音频直链解析完成
  fetchLyrics(sourceId, platform, song, thisSession)

  let resolvedUrl = ''
  let resolvedHeaders = {}

  try {
    try {
      const result = await lxRuntime.getMusicUrl(sourceId, platform, song, song.quality || '320k')
      if (thisSession !== currentSessionId) return
      if (result?.url && /^https?:/.test(result.url) && !isFakeOrNoticeAudio(result.url)) {
        resolvedUrl = result.url
        resolvedHeaders = result.headers || {}
        console.log('[PLAY] Custom source resolved:', resolvedUrl)
      }
    } catch (err) {
      console.warn('[PLAY] Custom source resolution failed:', err.message)
    }

    // 智能跨源换源匹配 (Cross-platform song match fallback, 类似洛雪换源播放机制)
    // 当原平台无法解析（例如 WY 接口受限或 VIP 付费独家），自动跨平台匹配 KW、TX 的同名音轨
    if (!resolvedUrl && thisSession === currentSessionId && song?.name) {
      console.log(`[PLAY] 原平台 [${platform}] 解析失败，启动智能跨平台换源匹配: "${song.name}"...`)
      const altPlatforms = ['kw', 'tx', 'kg'].filter(p => p !== platform)
      
      for (const altP of altPlatforms) {
        if (resolvedUrl || thisSession !== currentSessionId) break
        try {
          const cleanName = song.name.replace(/\(.*?\)|（.*?）|\[.*?\]|【.*?】/g, '').trim() || song.name
          const query = `${cleanName} ${song.singer || ''}`.trim()
          const searchRes = await SearchAPI.search(query, altP, 1)
          const candidates = searchRes?.data?.list || searchRes?.list || []
          
          for (let i = 0; i < Math.min(candidates.length, 3); i++) {
            if (resolvedUrl || thisSession !== currentSessionId) break
            const cand = candidates[i]
            try {
              const matchResult = await lxRuntime.getMusicUrl(sourceId, altP, cand, '128k')
              if (matchResult?.url && /^https?:/.test(matchResult.url) && !isFakeOrNoticeAudio(matchResult.url)) {
                resolvedUrl = matchResult.url
                resolvedHeaders = matchResult.headers || {}
                console.log(`[PLAY] 智能跨源匹配成功 [${altP.toUpperCase()}]:`, cand.name, cand.singer, resolvedUrl)
                fetchLyrics(sourceId, altP, cand, thisSession)
                break
              }
            } catch (_) {}
          }
        } catch (e) {
          console.warn(`[PLAY] 跨源检索 [${altP}] 异常:`, e)
        }
      }
    }

    if (thisSession !== currentSessionId) return

    if (resolvedUrl && !isFakeOrNoticeAudio(resolvedUrl)) {
      startAudioPlay(resolvedUrl, thisSession, resolvedHeaders)
    } else {
      const srcName = activeSourceMeta.value?.name || '当前音源'
      // 播放失败：界面上只给一句结论 + 该怎么做；完整原因（音源名、平台、歌曲 id）进日志。
      // 原来这里是一个带「可能原因 1/2」的长 alert，既打断操作又把技术细节推给用户。
      showAppToast(`《${song.name}》解析失败 —— 换一个音源试试，详情见「日志」`, true)
      logError(
        '播放取链',
        `${srcName} 未能解析《${song.name}》`,
        `platform=${platform} source=${song.source || ''} songmid=${song.songmid || song.id || ''}`,
      )
    }
  } finally {
    if (thisSession === currentSessionId) {
      isResolving.value = false
    }
  }
}

function playAllSongs({ songs, startIndex = 0 }) {
  if (!songs || !songs.length) return
  playlist.value = [...songs]
  if (playMode.value === 'random') {
    currentIndex.value = Math.floor(Math.random() * songs.length)
  } else {
    currentIndex.value = startIndex
  }
  playSong(playlist.value[currentIndex.value])
}

async function fetchNasLyric(song, sessionId) {
  if (!song?.path) return
  try {
    const res = await NasAPI.lyric(song.path)
    if (sessionId && sessionId !== currentSessionId) return
    if (res?.code === 200 && res.data?.lyric) {
      lyricData.value = res.data.lyric
      transData.value = ''
      console.log('[NAS LYRIC] Loaded successfully from:', res.data.source)
    }
  } catch (e) {
    console.warn('[NAS LYRIC] fetch error:', e)
  }
}

async function fetchLyrics(sourceId, platform, song, sessionId) {
  // 1. Try LX custom source lyric if source available
  if (sourceId) {
    try {
      const lrc = await lxRuntime.getLyric(sourceId, platform, song)
      if (sessionId && sessionId !== currentSessionId) return
      if (lrc?.lyric) {
        lyricData.value = lrc.lyric
        transData.value = lrc.tlyric || ''
        return
      }
    } catch (e) {
      console.warn('[LYRIC] Custom source lyric failed:', e)
    }
  }

  // 2. High-precision Universal Lyric Engine (covers 99.9% songs of all platforms)
  try {
    const duration = song.interval || song.duration || 0
    const hash = song.hash || ''
    const res = await SearchAPI.lyric(platform, song.songmid || song.id, song.name, song.singer, duration, hash)
    if (sessionId && sessionId !== currentSessionId) return
    if (res.code === 200 && res.data?.lyric) {
      lyricData.value = res.data.lyric
      transData.value = res.data.tlyric || ''
      console.log('[LYRIC] Universal lyric loaded, length:', res.data.lyric.length)
    }
  } catch (e) {
    console.warn('[LYRIC] Universal lyric failed:', e)
  }
}

let currentRawUrl = ''
let currentPlayingHeaders = {}

function startAudioPlay(url, sessionId, headers = {}) {
  if (sessionId && sessionId !== currentSessionId) return
  if (!audioRef.value) return
  currentRawUrl = url
  currentPlayingHeaders = headers || {}

  let playSrc = url
  // 通过后端流中继代理，彻底消除浏览器跨域CORS、防盗链403以及HTTPS Mixed Content阻断！
  if (playSrc.startsWith('http://') || playSrc.startsWith('https://')) {
    if (!playSrc.includes('/api/player/stream') && !playSrc.includes('/api/nas/stream')) {
      let proxyUrl = `/api/player/stream?url=${encodeURIComponent(playSrc)}`
      const ref = headers?.referer || headers?.Referer
      if (ref) {
        proxyUrl += `&referer=${encodeURIComponent(ref)}`
      }
      playSrc = proxyUrl
    }
  }
  audioRef.value.src = playSrc
  audioRef.value.load()
  const p = audioRef.value.play()
  if (p !== undefined) {
    p.then(() => {
      isPlaying.value = true
    }).catch(e => {
      console.warn('Play prevented or error:', e)
      isPlaying.value = false
    })
  }
}

function togglePlay() {
  if (!audioRef.value) return
  if (audioRef.value.paused) {
    audioRef.value.play().then(() => { isPlaying.value = true }).catch(console.warn)
  } else {
    audioRef.value.pause()
    isPlaying.value = false
  }
}

function getRandomIndex() {
  if (playlist.value.length <= 1) return 0
  let nextIdx = currentIndex.value
  while (nextIdx === currentIndex.value) {
    nextIdx = Math.floor(Math.random() * playlist.value.length)
  }
  return nextIdx
}

// 手动切歌一律走「当前列表顺序」，与播放模式无关。
// 用户口径（2026-09-27）：下一首/上一首就该是当前页面歌单的下一首/上一首。
// 随机只作用于「自动续播到下一首」和「播放全部」的起始曲，见 onEnded / playAllSongs。
function playPrev() {
  if (!playlist.value.length) return
  currentIndex.value = (currentIndex.value - 1 + playlist.value.length) % playlist.value.length
  playSong(playlist.value[currentIndex.value])
}

/* 播放列表抽屉里点某一行（v2.1.64）：切到那一首，队列本身不动。
   走的是同一条 playSong 路径 —— 上一首 / 下一首仍然按当前队列顺序走。 */
function playFromQueue(idx) {
  const song = playlist.value[idx]
  if (!song) return
  currentIndex.value = idx
  playSong(song)
}

/* 播放列表抽屉头部的垃圾桶：清空队列。正在播的那首**继续播**（用户是在清列表，不是停止播放），
   只是没有「下一首」可切了 —— prev/next 里本来就对空队列有 return 兜底。 */
function clearPlaylist() {
  if (!playlist.value.length) return
  const cur = currentSong.value
  playlist.value = []
  currentIndex.value = 0
  if (cur) playlist.value = [cur]
}

function playNext() {
  if (!playlist.value.length) return
  currentIndex.value = (currentIndex.value + 1) % playlist.value.length
  playSong(playlist.value[currentIndex.value])
}

function onSeek(time) { if (audioRef.value) audioRef.value.currentTime = time }
function onTimeUpdate() { if (audioRef.value) currentTime.value = audioRef.value.currentTime }

function onDurationChange() {
  if (!audioRef.value) return
  duration.value = audioRef.value.duration || 0
}

async function onAudioError(e) {
  if (isResolving.value) return
  if (!audioRef.value || !audioRef.value.getAttribute('src')) return
  const src = audioRef.value.src || ''
  if (!src || src === window.location.href || src.endsWith('/api/player/stream?url=')) return

  const song = currentSong.value
  if (!song || song.source === 'nas') return

  console.warn('[AUDIO] Audio playback error detected for:', song?.name, 'src:', src, e)

  // 如果后端代理流播放出错，尝试直接降级播放原始直链 URL
  if (src.includes('/api/player/stream') && currentRawUrl && currentRawUrl !== src) {
    console.log('[AUDIO] Proxy stream error, falling back directly to raw URL:', currentRawUrl)
    audioRef.value.src = currentRawUrl
    audioRef.value.load()
    audioRef.value.play().then(() => {
      isPlaying.value = true
    }).catch(err => {
      console.warn('[AUDIO] Direct playback also failed:', err)
      isPlaying.value = false
    })
    return
  }

  isPlaying.value = false
}
function onEnded() {
  if (playMode.value === 'loop') {
    if (audioRef.value) {
      audioRef.value.currentTime = 0
      audioRef.value.play().catch(console.warn)
    }
    return
  }
  if (playMode.value === 'random') {
    currentIndex.value = getRandomIndex()
    playSong(playlist.value[currentIndex.value])
    return
  }
  playNext()
}
function onVolumeChange(val) { if (audioRef.value) audioRef.value.volume = val }

function toggleLyrics() {
  if (!showRightPanel.value) { showRightPanel.value = true; stageTab.value = 'lyrics' }
  else stageTab.value = stageTab.value === 'lyrics' ? 'turntable' : 'lyrics'
}

function toggleFullscreen() {
  if (!document.fullscreenElement) document.documentElement.requestFullscreen().catch(console.warn)
  else document.exitFullscreen().catch(console.warn)
}
</script>
