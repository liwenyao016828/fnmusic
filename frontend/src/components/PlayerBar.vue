<template>
  <!--
    底部播放栏 —— 按 trim.music 的真实实现对齐（2026-09-28 从它 fpk 的 JS 里挖到）：
      · 容器 `flex h-[72px] items-center gap-2 px-4` + `width: 576 / marginInline: auto / borderRadius: 44`
        ⇒ 这里是 .ys-player（宽 min(576px, 100vw-24px) / 高 64~72 / 胶囊圆角）
      · 玻璃 = `.music-player-glass`：`backdrop-filter: blur(12px)` + 轻微阴影 + 细边框
      · 底色**跟着封面走**（它的 `background: CV` 是拿封面算出来的）——
        这里用「封面自己放大 + 模糊 + 压暗」当底色层，视觉上同一件事，不用 canvas 取色
      · 封面 38×38 `rounded-md`（不是圆形）、歌曲信息块最宽 234px

    三个形态，高度一致（手机 64 / 桌面 72）：
      默认 = 右下角一颗搜索图标（v2.1.119 起播放条默认隐藏）｜
      搜索态 = 搜索框从图标位置滑出（覆盖/代替播放条）｜
      播放态 = 控制 + 封面信息进度 + 功能 ｜ 折叠态 = 细条
    ⚠️ 响应式靠分级隐藏，不用 transform: scale()（滑出动画的 scale 是搜索框的入场动效，与布局无关）。
  -->
  <!--
    浮动播放栏（2026-09-28 按用户要求改）：整条**不再占一行布局**，改成浮在内容之上。
    以前它在 flex 流里独占一行（1280×84），两侧露出的是页面底色 #f5f7f9 —— 看着就是一条灰带，
    而飞牛那边内容是从胶囊底下穿过去的。现在整条 `pointer-events-none`（点两侧等于点到内容），
    只有中间那颗胶囊收指针事件 → 「只有中间的播放栏」。
  -->
  <div class="pb-safe ys-playerbar-root ys-dock-lift pointer-events-none fixed inset-x-0 bottom-0 z-40 select-none">

    <!-- ── 折叠态 ── -->
    <div v-if="collapsed && currentSong" class="px-3 pb-2">
      <button
        @click="$emit('toggle-collapse')"
        class="ys-glass ys-player-glass pointer-events-auto mx-auto flex h-8 w-full max-w-[576px] items-center gap-3 overflow-hidden rounded-full px-3 text-[11px] text-white/70 transition-colors hover:text-white"
        title="展开播放条"
      >
        <ChevronUp class="h-3.5 w-3.5 shrink-0" />
        <!-- 2026-10-26 用户口径：最小播放框左边显示歌名、右边显示歌词，两边保持距离；
             显示不下整段就自动向右滚动（LyricMarquee direction="right"）。 -->
        <span class="max-w-[45vw] shrink-0 truncate font-medium text-white/85">{{ currentSong.name || '正在播放' }}</span>
        <span class="min-w-0 flex-1">
          <LyricMarquee :text="currentLyric" direction="right" />
        </span>
      </button>
    </div>

    <!--
      ── 搜索框（两种形态共用；v2.1.119 起整条播放栏默认隐藏，这里是滑出的那一框）──
      · hidden 态：底部右下角只有一颗搜索图标（固定位置，两种形态都在同一个位置）；
      · 点图标 → 本框从图标位置滑动放大出来（transform-origin 右下角，scale + 位移，
        见 style.css 的 .ys-barsearch；reduced-motion 下直接显隐）；
      · 聚焦绝不收、有词不算闲置；点别处 / 超时 8s / 提交 / 开始播放 / Esc → 滑回图标位置。
      状态机在 services/playerBarSearch.js（纯函数，有单测），这里只翻译 DOM 事件。
      ⚠️ 本框 z-50 高于播放态胶囊：播放条可见时展开搜索，框叠在条上方 —— 两者都完整可见。
      ⚠️ 2026-10-18：手机上这一套（框 + 右下角图标）**不出现** —— 底部「灵动岛」的
      搜索圆就把这件事做了，两者同时在场会叠在同一个角落（都是右下角、都是 z-50），
      点谁都不对。桌面端没有岛，这里照旧。
    -->
    <div
      v-if="searchOpen"
      class="pointer-events-auto ys-dock-lift fixed inset-x-0 bottom-0 z-50 px-3 pb-2 max-sm:hidden sm:pb-3"
      @focusout="onSearchFocusOut"
    >
      <!--
        2026-10-28 用户口径：「不需要右边的搜索确认按钮，就只用输入法里的搜索就可以了。」
        → 右侧那颗白色圆形提交按钮撤掉；提交路径改为三条（都在 form 上，不依赖按钮存在）：
          ① 输入法 / 键盘的搜索键（input 的 type=search + enterkeyhint=search → submit）；
          ② 回车（form 的 @submit.prevent）；
          ③ Esc 收起。
        无障碍：按钮不是「删掉」，而是 `.sr-only` —— form 里必须有一个提交控件，
        部分读屏/UA 才会把回车当成提交（而不是当作新行）。它不可 Tab 聚焦（tabindex=-1），
        也不会被鼠标点到，纯粹是给 AT 与 UA 用的语义占位。
      -->
      <form
        ref="searchBoxRef"
        @submit.prevent="submitSearch"
        class="ys-glass ys-player-glass ys-player ys-barsearch ys-barsearch--focusable mx-auto flex h-[64px] items-center gap-2.5 pl-3 pr-3.5 sm:h-[72px] sm:gap-3 sm:pl-4 sm:pr-4"
        :class="[reducedMotion ? 'ys-barsearch--now' : '', searchFocused ? 'ys-barsearch--focused' : '']"
      >
        <span class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-white/10 text-white/60">
          <Disc3 class="h-4 w-4" />
        </span>
        <span class="min-w-0 flex-1">
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            type="search"
            placeholder="搜索歌曲、歌手、专辑…"
            title="搜索歌曲、歌手、专辑（Enter 或输入法搜索键执行，Esc 收起）"
            aria-label="搜索歌曲、歌手、专辑"
            enterkeyhint="search"
            @focus="onSearchFocus"
            @keydown.esc.prevent="collapseSearch({ escape: true })"
            class="ys-barsearch__input w-full bg-transparent text-[13px] font-semibold text-white/90 placeholder:text-white/45 focus:outline-none"
          />
          <small class="block truncate text-[11px] text-white/35">也可以直接粘贴歌单链接 · Enter 搜索</small>
        </span>
        <button
          type="submit"
          :disabled="!searchQuery.trim()"
          :title="searchQuery.trim() ? '搜「' + searchQuery.trim() + '」' : '输入关键词后按 Enter 搜索'"
          class="sr-only"
          tabindex="-1"
          aria-hidden="true"
        >
          <Search class="h-4 w-4" />
        </button>
      </form>
    </div>

    <!--
      ── 搜索图标（hidden 态唯一可见物；两种形态同一位置 —— 右下角）──
      键盘可达：真正的 button；aria-expanded 恒为 false 是因为展开后本按钮卸载、
      搜索框内的输入框接住焦点（那个框自带 aria-label 与 Esc 说明）。
    -->
    <!--
      搜索图标：只有「没有播放条」时才单独固定在右下角（两种形态同一位置）。
      有播放条时，它会作为副岛紧贴在胶囊右侧 —— 见下面播放态里的 `.ys-sideisland-row`。
      手机端这一套不出现（底部灵动岛承担搜索），所以 max-sm:hidden。
    -->
    <button
      v-if="!searchOpen && !hasPlayerBar"
      ref="searchIconRef"
      type="button"
      @click="openSearch"
      title="搜索"
      aria-label="搜索歌曲、歌手、专辑"
      aria-expanded="false"
      class="ys-glass ys-player-glass ys-barsearch-icon ys-dock-lift [--dock-base:8px] sm:[--dock-base:12px] pointer-events-auto fixed right-3 z-50 grid h-11 w-11 place-items-center rounded-full text-white/85 transition-colors hover:text-white max-sm:hidden"
    >
      <Search class="h-4 w-4" />
    </button>

    <!-- ── 播放态（常驻控制；搜索作为副岛贴在它右侧）── -->
    <div v-if="hasPlayerBar && !searchOpen" class="px-3 pb-2 sm:pb-3" :class="contentInsetClass">
      <!-- 2026-10-29 用户：「当播放条出现的时候,右下角的搜索按钮,移动到播放条右侧.类似灵动岛副岛.」
           做法：胶囊和副岛放在同一个「岛行」里，行的总宽 = 胶囊宽 + 间距 + 副岛宽，
           用 max-width 兜住。副岛与胶囊等高、正圆、同一套玻璃质感（见下方按钮 class）。 -->
      <div class="ys-sideisland-row pointer-events-none mx-auto flex w-full max-w-[calc(576px+84px)] items-center justify-center gap-2">
      <div class="ys-glass ys-player-glass ys-player pointer-events-auto relative flex h-[64px] min-w-0 flex-1 items-center gap-2 px-3 sm:h-[72px] sm:px-4">
        <!--
          2026-10-29 用户：「底部音量条显示不完,被外面的播放组件裁切了」
          根因：胶囊容器原来是 `overflow-hidden`（为了把封面模糊层裁进圆角），
          而音量弹层是它的**子节点**、向上溢出 → 被一起裁掉。
          改法：胶囊不再裁切；改成把裁切职责交给 `.ys-player-clip` 这层绝对定位背景，
          由它自己 `overflow-hidden rounded-full` 兜住封面层。
        -->
        <div class="ys-player-clip pointer-events-none absolute inset-0 overflow-hidden rounded-full" aria-hidden="true">
          <!-- 磨砂半透明（2026-10-29 参考图）：封面染色 30%→20%、压暗 45%→12%。
               2026-10-29 用户：「整体玻璃效果不强啊,磨砂过了.再透一点」→ 压暗再降一档，
               让 .ys-player-glass 的薄玻璃透出背后内容，而不是被这层盖成一块实心板。 -->
          <img
            v-if="currentSong.cover"
            :src="currentSong.cover"
            alt=""
            class="absolute inset-0 h-full w-full scale-150 object-cover opacity-20 blur-2xl"
          />
          <span class="absolute inset-0 bg-black/[0.12]"></span>
        </div>

        <!-- 左：播放控制（trim.music 最左侧就是控制组，`h-12 shrink-0 items-center`） -->
        <div class="relative flex h-12 shrink-0 items-center gap-0.5">
          <button
            @click="cyclePlayMode"
            :title="playModeTitle"
            class="hidden h-9 w-9 place-items-center rounded-full text-white/55 transition-colors hover:bg-white/10 hover:text-white md:grid"
          >
            <Shuffle v-if="playMode === 'random'" class="h-3.5 w-3.5 text-emerald-400" />
            <Repeat1 v-else-if="playMode === 'loop'" class="h-3.5 w-3.5 text-emerald-400" />
            <Repeat v-else class="h-3.5 w-3.5" />
          </button>
          <button
            @click="$emit('prev')"
            title="上一首"
            class="grid h-8 w-8 shrink-0 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white sm:h-9 sm:w-9"
          >
            <SkipBack class="h-[15px] w-[15px] fill-current sm:h-4 sm:w-4" />
          </button>
          <button
            @click="$emit('toggle-play')"
            :disabled="isResolving"
            :title="isPlaying ? '暂停' : '播放'"
            class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-white text-black shadow-[0_1px_2px_rgba(0,0,0,0.25)] transition-transform hover:scale-[1.04] active:scale-95 disabled:opacity-50 sm:h-10 sm:w-10"
          >
            <Loader2 v-if="isResolving" class="h-4 w-4 animate-spin" />
            <Pause v-else-if="isPlaying" class="h-4 w-4 fill-current" />
            <Play v-else class="h-4 w-4 translate-x-0.5 fill-current" />
          </button>
          <button
            @click="$emit('next')"
            title="下一首"
            class="grid h-8 w-8 shrink-0 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white sm:h-9 sm:w-9"
          >
            <SkipForward class="h-[15px] w-[15px] fill-current sm:h-4 sm:w-4" />
          </button>
        </div>

        <!-- 中：封面 + 歌曲信息（弹性区）。
             搜索（v2.1.119）改由右下角图标滑出的独立搜索框承担 —— 这里不再内嵌输入框。
             2026-10-26 用户口径：进度条改为「封面外圈一圈绿色线条」，原线条位置换成一行歌词。 -->
        <div class="relative flex min-w-0 flex-1 items-center gap-2.5">
          <!-- 封面 + 绿色环形进度：conic-gradient 一圈，进度随播放推进。
               2026-10-28 用户口径：正方形封面不能把圆环四个角撑出去、中间不能留缝。
               做法：外层圆只负责画环（不给子元素裁切），内层圆负责裁封面 —— 内圆
               比外环小 6px，环宽 3px，一圈都露得出来；封面本身用 rounded-full 裁成圆。 -->
          <div class="relative grid h-[34px] w-[34px] shrink-0 place-items-center sm:h-[38px] sm:w-[38px]">
            <span
              class="absolute inset-0 rounded-full"
              :style="{ background: `conic-gradient(#22c55e ${progressPercent * 3.6}deg, rgba(255,255,255,0.22) 0deg)` }"
              aria-hidden="true"
            ></span>
            <!-- 2026-10-29 用户：「点击封面可以打开播放器.鼠标移动上去也有个放大的蒙版图标」
                 → 封面本身就是入口；悬停时压一层暗蒙版 + 一颗「放大」图标（Maximize2）。 -->
            <button
              @click="$emit('toggle-fullscreen')"
              class="ys-cover-btn group/cover relative grid h-[28px] w-[28px] shrink-0 place-items-center overflow-hidden rounded-full bg-black/45 text-white transition-transform hover:scale-[1.06] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white/80 sm:h-[32px] sm:w-[32px]"
              title="打开播放页"
              aria-label="打开播放页（沉浸播放器）"
            >
              <img
                v-if="currentSong.cover"
                :src="currentSong.cover"
                alt=""
                referrerpolicy="no-referrer"
                class="h-full w-full object-cover"
                @error="currentSong.cover = ''"
              />
              <Music v-else class="h-4 w-4" />
              <span
                class="ys-cover-mask absolute inset-0 grid place-items-center rounded-full bg-black/55 opacity-0 transition-opacity duration-150 group-hover/cover:opacity-100 group-focus-visible/cover:opacity-100 group-active/cover:opacity-100"
                aria-hidden="true"
              >
                <Maximize2 class="h-3.5 w-3.5 text-white" :stroke-width="2.2" />
              </span>
            </button>
          </div>

          <div class="min-w-0 flex-1">
            <!-- 2026-10-29 用户：「歌手移动到歌名后面」
                 → 一行「歌名 · 歌手」，歌名可截断、歌手跟在后面不换行。 -->
            <div class="flex min-w-0 items-baseline gap-1.5">
              <b class="min-w-0 truncate text-[13px] font-semibold tracking-[-0.01em] text-white sm:text-sm">
                {{ currentSong.name || currentSong.title || '曲率' }}
              </b>
              <small
                v-if="currentSong.singer || currentSong.artist"
                class="min-w-0 shrink-0 max-w-[45%] truncate text-[11px] font-normal text-white/50"
              >
                {{ currentSong.singer || currentSong.artist }}
              </small>
            </div>
            <!-- 歌词行（原进度条位置）：只显示一行，长度超出自动滚动 -->
            <div class="mt-0.5 min-w-0 text-white">
              <LyricMarquee :text="currentLyric" direction="left" />
            </div>
          </div>
        </div>

        <!-- 右：功能按钮（停止 / 音量 / 收缩） -->
        <div class="relative flex shrink-0 items-center gap-0.5 sm:gap-1.5">
          <!-- 2026-10-29 用户：「删除停止按钮」→ 这里已移除，播放/暂停用主按钮即可。 -->
          <!--
            音量（2026-10-29 用户：「音量条改为按钮，点击出现垂直音量条，从下往上」）。
            以前这里是一颗静音键 + 一条横着的 range（且只在 xl 才有）。现在合成一颗按钮：
            点开 → 在按钮上方弹出竖直滑块；再点一次 / 点别处 / Esc 收起。
            滑块本身可拖可点，方向键（↑↓ ←→、PageUp/PageDown、Home/End）都能调。
          -->
          <div ref="volumeWrapRef" class="relative">
            <button
              ref="volumeBtnRef"
              type="button"
              @click="toggleVolumePopover"
              :title="volumeTitle"
              :aria-label="volumeTitle"
              :aria-expanded="volumeOpen ? 'true' : 'false'"
              aria-haspopup="dialog"
              class="grid h-9 w-9 place-items-center rounded-full text-white/55 transition-colors hover:bg-white/10 hover:text-white"
              :class="volumeOpen ? 'bg-white/10 text-white' : ''"
            >
              <VolumeX v-if="isMuted || volume <= 0" class="h-4 w-4 text-rose-400" />
              <Volume1 v-else-if="volume < 0.5" class="h-4 w-4" />
              <Volume2 v-else class="h-4 w-4" />
            </button>

            <!-- 垂直音量条：从下往上弹出（底部对齐按钮，向上生长） -->
            <Transition name="ys-volpop">
              <div
                v-if="volumeOpen"
                ref="volumePopRef"
                role="dialog"
                aria-label="音量调节"
                class="ys-volpop absolute left-1/2 z-50 -translate-x-1/2 rounded-2xl border border-white/15 bg-black/80 px-1.5 py-2.5 shadow-[0_8px_28px_rgba(0,0,0,0.45)] backdrop-blur-md"
              >
                <input
                  ref="volumeSliderRef"
                  type="range"
                  min="0"
                  max="1"
                  step="0.01"
                  :value="volume"
                  aria-label="音量"
                  :aria-valuetext="Math.round(volume * 100) + '%'"
                  orient="vertical"
                  @input="onVolumeInput"
                  @change="onVolumeInput"
                  class="ys-volrange h-28 w-4 cursor-pointer appearance-none bg-transparent"
                />
              </div>
            </Transition>
          </div>
          <button
            @click="$emit('toggle-collapse')"
            title="收起播放条"
            class="grid h-9 w-9 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white"
          >
            <ChevronDown class="h-4 w-4" />
          </button>
        </div>
      </div>
      <!-- 副岛：播放条右侧那颗搜索按钮（2026-10-29 用户：「搜索按钮高度和ui要和播放条一样.并且是正圆」
           → 与胶囊等高（64/72）、正圆、同款玻璃底/边框/阴影；与胶囊只隔 4px，看起来是一条边而不是两条。 -->
      <button
        type="button"
        @click="openSearch"
        title="搜索"
        aria-label="搜索歌曲、歌手、专辑"
        aria-expanded="false"
        class="ys-glass ys-player-glass ys-sideisland ys-press pointer-events-auto relative hidden h-[64px] w-[64px] shrink-0 place-items-center overflow-hidden rounded-full text-white/75 transition-colors hover:text-white sm:grid sm:h-[72px] sm:w-[72px]"
      >
        <!-- 2026-10-29 用户：「播放条辅岛和主岛的底色不一样呢?副岛感觉要浅灰一些了.修复,让副岛和主岛一样」
             根因：两颗岛的 `.ys-glass + .ys-player-glass` 底完全一致，但**主岛里多一层**
             `.ys-player-clip` 的 `bg-black/[0.28]` 压暗层（为了压住封面染色、保证白字可读），
             副岛没有这层 → 副岛只剩 0.48 的近黑底，透出后面浅色页面，看起来发灰发浅。
             改法：给副岛补一层同样的 0.28 压暗（没有封面，所以只补压暗这一层），
             两颗岛的实际叠色就一致了。（压暗值与主岛那层同步，改一处必须改两处） -->
        <span class="pointer-events-none absolute inset-0 bg-black/[0.12]" aria-hidden="true"></span>
        <Search class="relative h-5 w-5" />
      </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import {
  Play, Pause, SkipBack, SkipForward, Shuffle, Repeat, Repeat1,
  Volume2, Volume1, VolumeX, Music, Loader2, Maximize2,
  Disc3, ChevronDown, ChevronUp, Search,
} from 'lucide-vue-next'
import LyricMarquee from './LyricMarquee.vue'
import { currentLyricLine } from '../services/lyricLine'
import {
  SEARCH_ANIM_MS,
  SEARCH_OPEN_TIMEOUT_MS,
  blurOutcome,
  iconClickNext,
  shouldCollapseSearch,
  timeoutNext,
} from '../services/playerBarSearch'

const props = defineProps({
  currentSong: Object,
  isPlaying: Boolean,
  currentTime: Number,
  duration: Number,
  showLyrics: Boolean,
  isResolving: Boolean,
  playMode: { type: String, default: 'sequence' },
  /** 播放条是否被收起（由 App.vue 持有，本组件只负责渲染细条与派发切换） */
  collapsed: { type: Boolean, default: false },
  /** 原始 LRC 歌词（App.vue 的 lyricData）：底栏在原进度条位置显示「正在唱的那一行」 */
  lyric: { type: String, default: '' },
  /** 左侧栏是否收缩（56px 竖条）：收缩态下播放条整行再往右让一点，见 contentInsetClass */
  sidebarCollapsed: { type: Boolean, default: false },
})
const emit = defineEmits([
  'toggle-play', 'prev', 'next', 'seek', 'toggle-fullscreen',
  'toggle-view', 'volume-change', 'update:playMode', 'toggle-collapse', 'search', 'stop',
])

/** 播放条是否在展示（非折叠、有歌）——决定搜索按钮是「右下角固定」还是「胶囊右侧副岛」。 */
const hasPlayerBar = computed(() => !props.collapsed && !!props.currentSong)
/* 2026-10-29 用户：「收缩和播放条再增加点间距」（收缩 = 左侧栏折叠后的竖条）
   → 播放条整行在左侧栏收缩态再往右让一点，避免贴到旁边的图标列。
   用 margin-left 而不是 padding：行本身已经 mx-auto 居中，加 margin 会同步把整行往右推。
   ml-12 = 48px 让位；展开态 false → 空字符串，零影响。 */
const contentInsetClass = computed(() => (props.sidebarCollapsed ? 'ml-12' : ''))

// ── 搜索滑出/收回（v2.1.119 状态机：逻辑在 services/playerBarSearch.js，有单测）──
//
// 状态：searchOpen=false（只剩右下角搜索图标）| true（搜索框已滑出）。
// 收回触发源五类：点别处 / 提交 / 开始播放 / Esc / 8 秒超时；
// 豁免两条件：输入框聚焦、有词未提交（trim 后判）。判据全在 shouldCollapseSearch。
const searchQuery = ref('')
const searchOpen = ref(false)
const searchInputRef = ref(null)
const searchBoxRef = ref(null)
const searchIconRef = ref(null)
let searchTimer = null

/** 系统「减弱动效」：开着就跳过过渡（CSS 里同步有 @media 兜底，这里再收一层） */
const reducedMotion = ref(false)
let mq = null
function onMqChange(e) { reducedMotion.value = !!e.matches }

onMounted(() => {
  if (typeof window !== 'undefined' && window.matchMedia) {
    mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    reducedMotion.value = !!mq.matches
    // addEventListener 是新式 API；老 Safari 只有 addListener —— 两者都挂，谁有挂谁
    if (mq.addEventListener) mq.addEventListener('change', onMqChange)
    else if (mq.addListener) mq.addListener(onMqChange)
  }
  // 点框外收回：capture 阶段监听，免得子组件 stopPropagation 把它绕过去
  document.addEventListener('pointerdown', onDocPointerDown, true)
  document.addEventListener('keydown', onDocKeydown, true)
  // 音量浮层同理：点别处 / Esc 收起
  document.addEventListener('pointerdown', onVolumeDocPointerDown, true)
  document.addEventListener('keydown', onVolumeKeydown, true)
})
onUnmounted(() => {
  disarmSearchTimer()
  if (mq) {
    if (mq.removeEventListener) mq.removeEventListener('change', onMqChange)
    else if (mq.removeListener) mq.removeListener(onMqChange)
  }
  document.removeEventListener('pointerdown', onDocPointerDown, true)
  document.removeEventListener('keydown', onDocKeydown, true)
  document.removeEventListener('pointerdown', onVolumeDocPointerDown, true)
  document.removeEventListener('keydown', onVolumeKeydown, true)
})

function armSearchTimer() {
  disarmSearchTimer()
  searchTimer = setTimeout(() => {
    searchTimer = null
    // 到点再问一次快照（竞态兜底）：这 8 秒里用户可能已经聚焦/输入 —— 那就不收
    collapseSearch({ timeout: true })
  }, SEARCH_OPEN_TIMEOUT_MS)
}
function disarmSearchTimer() {
  if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
}

/** 统一收回入口：快照交给状态机裁决，裁决要收才真的收 */
function collapseSearch(snapshot) {
  if (!searchOpen.value) return
  if (!shouldCollapseSearch({ focus: searchFocused.value, query: searchQuery.value, ...snapshot })) {
    armSearchTimer() // 没收成 = 用户还在用，续上哨兵
    return
  }
  searchOpen.value = false
  disarmSearchTimer()
  searchQuery.value = ''
}

/** 展开搜索框：从图标位置滑出来（动画走 CSS transform，见 .ys-barsearch） */
function openSearch() {
  const next = iconClickNext(searchOpen.value ? 'open' : 'hidden')
  searchOpen.value = next.state === 'open'
  if (searchOpen.value) {
    armSearchTimer()
    nextTick(() => searchInputRef.value?.focus())
  }
}

/** 输入框聚焦：硬豁免 —— 收回哨兵立刻拆掉（聚焦期间谁都收不走） */
const searchFocused = ref(false)
function onSearchFocus() {
  searchFocused.value = true
  disarmSearchTimer()
}
/** 失焦：空词就收、有词留着（用户可能正要点搜索按钮） */
function onSearchFocusOut(e) {
  if (searchBoxRef.value?.contains(e.relatedTarget)) return // 焦点还在框内（点提交按钮）
  searchFocused.value = false
  if (blurOutcome(searchQuery.value) === 'collapse') collapseSearch({ outside: true })
  else armSearchTimer()
}
/** 点了框外：不抢聚焦收回的活，只按「点别处」判一次 */
function onDocPointerDown(e) {
  if (!searchOpen.value) return
  if (searchBoxRef.value?.contains(e.target)) return
  if (searchIconRef.value?.contains(e.target)) return // 点图标是展开/续命，不算「别处」
  collapseSearch({ outside: true })
}
/** Esc：全局逃生口（焦点不在输入框里时兜底；框内那份由 input 的 keydown 处理） */
function onDocKeydown(e) {
  if (!searchOpen.value || e.key !== 'Escape') return
  if (searchBoxRef.value?.contains(e.target)) return
  collapseSearch({ escape: true })
}

/** 开始播放 = 意图信号，收回（判据在状态机里：有词也收，因为词没在提交路径上） */
watch(() => props.isPlaying, (playing) => {
  if (playing) collapseSearch({ playing: true })
})

/** 提交搜索：把词交给 App.vue（它负责切到发现页并把词转给 SearchView 去执行） */
function submitSearch() {
  const q = searchQuery.value.trim()
  if (!q) return
  emit('search', q)
  collapseSearch({ submitting: true })
}

const volume = ref(0.8)
const isMuted = ref(false)
const prevVolume = ref(0.8)

// ── 垂直音量条（2026-10-29）──
// 一颗按钮开合；点外面 / Esc 收起；音量实时生效（走下面那个 watch 的 volume-change）。
// 键盘：滑块聚焦时 ↑↓←→ 每次 ±5%，PageUp/PageDown ±10%，Home/End 到 0/100%。
const volumeOpen = ref(false)
const volumeBtnRef = ref(null)
const volumeWrapRef = ref(null)
const volumePopRef = ref(null)
const volumeSliderRef = ref(null)

const volumeTitle = computed(() => {
  const pct = Math.round(volume.value * 100)
  if (isMuted.value || volume.value <= 0) return '静音（点击调节音量）'
  return `音量 ${pct}%（点击调节）`
})

function toggleVolumePopover() {
  volumeOpen.value = !volumeOpen.value
  if (volumeOpen.value) nextTick(() => volumeSliderRef.value?.focus())
  else volumeBtnRef.value?.focus()
}

function closeVolumePopover(refocus = false) {
  if (!volumeOpen.value) return
  volumeOpen.value = false
  if (refocus) nextTick(() => volumeBtnRef.value?.focus())
}

function onVolumeInput(e) {
  const raw = e && e.target ? e.target.value : e
  const v = Number(raw)
  if (!Number.isFinite(v)) return
  volume.value = Math.min(1, Math.max(0, v))
  // 拖到 0 = 静音；从 0 拖起来要解除静音（watch 里已处理，这里显式一次更直观）
  if (volume.value <= 0) { if (!isMuted.value) prevVolume.value = volume.value || prevVolume.value; isMuted.value = true }
  else isMuted.value = false
}

/** Esc 收起（全局兜底：焦点在滑块外也能关） */
function onVolumeKeydown(e) {
  if (!volumeOpen.value) return
  if (e.key === 'Escape') { e.preventDefault(); closeVolumePopover(true) }
}

/** 点外面收起：capture 阶段，免得被组件内的 stopPropagation 绕过 */
function onVolumeDocPointerDown(e) {
  if (!volumeOpen.value) return
  if (volumeWrapRef.value?.contains(e.target)) return
  const el = e.target
  if (el && el.nodeType === 1 && (el.closest?.('.ys-volpop-enter-active') || el.closest?.('[role="dialog"][aria-label="音量调节"]'))) return
  closeVolumePopover()
}

const playModes = ['sequence', 'random', 'loop']
const playModeTitle = computed(() => ({ sequence: '列表循环', random: '随机播放', loop: '单曲循环' }[props.playMode]))
function cyclePlayMode() {
  const nextIdx = (playModes.indexOf(props.playMode) + 1) % playModes.length
  emit('update:playMode', playModes[nextIdx])
}

const progressPercent = computed(() => (!props.duration ? 0 : Math.min(100, (props.currentTime / props.duration) * 100)))

/** 当前正在唱的那一行歌词（原进度条位置 / 折叠最小播放框右段都显示它） */
const currentLyric = computed(() =>
  currentLyricLine(props.lyric, props.currentTime)
  || (props.currentSong && (props.currentSong.singer || props.currentSong.artist))
  || ''
)

function toggleMute() {
  if (isMuted.value) { isMuted.value = false; volume.value = prevVolume.value || 0.8 }
  else { prevVolume.value = volume.value; volume.value = 0; isMuted.value = true }
}

watch(volume, (val) => { emit('volume-change', parseFloat(val)); if (val > 0) isMuted.value = false })
</script>

<style scoped>
/* ── 垂直音量条（2026-10-29）────────────────────────────────────────────
   range 想竖起来只能靠 writing-mode: vertical-lr + direction: rtl：
   轨道从下往上长，值往上调 —— 这正是用户要的「从下往上」。 */
.ys-volrange {
  writing-mode: vertical-lr;
  direction: rtl;
  accent-color: #ffffff;
}
/* 轨道：白 22% 的胶囊，两侧留出拇指的余量（Chromium / WebKit）
   2026-10-29 用户：「播放条的音量条细一点,离播放条近一点」→ 轨道 4px → 2px。 */
.ys-volrange::-webkit-slider-runnable-track {
  width: 2px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.22);
}
.ys-volrange::-webkit-slider-thumb {
  -webkit-appearance: none;
  appearance: none;
  width: 10px;
  height: 10px;
  margin-left: -4px;
  border-radius: 999px;
  background: #fff;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.35);
}
.ys-volrange::-moz-range-track {
  width: 2px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.22);
}
.ys-volrange::-moz-range-thumb {
  width: 10px;
  height: 10px;
  border: 0;
  border-radius: 999px;
  background: #fff;
}
.ys-volrange:focus-visible {
  outline: 2px solid rgba(255, 255, 255, 0.85);
  outline-offset: 2px;
  border-radius: 999px;
}

/* 弹出动效：从按钮所在位置往上长出来（transform-origin 在底部） */
.ys-volpop-enter-active,
.ys-volpop-leave-active {
  transition: opacity 120ms ease, transform 160ms cubic-bezier(0.22, 1, 0.36, 1);
  transform-origin: bottom center;
}
.ys-volpop-enter-from,
.ys-volpop-leave-to {
  opacity: 0;
  transform: translate(-50%, 6px) scaleY(0.85);
}
/* ── 音量弹层定位（2026-10-29 用户：「底部音量条显示不完,被外面的播放组件裁切了」）
   定位基准是「按钮外层那个 36px 高的 wrapper」，不是胶囊。胶囊是它的父级、
   高度随意变（手机 64 / 桌面 72），用 100%+固定值去算会多出 22px 的空档。
   现在的算法完全相对 wrapper：
     弹层下沿 = wrapper 顶 − 24px。wrapper 高 36、其顶在胶囊顶下方 12px，
     ⇒ 弹层下沿落在胶囊上沿上方 12px（手机/桌面同值，因为两者内边距一致）。
   2026-10-29 用户：「播放条的音量条细一点,离播放条近一点」→ 由 28px 收到 6px。 */
.ys-volpop {
  bottom: calc(100% + 24px);
}
/* 系统减弱动效：只淡入淡出，不做缩放 */
@media (prefers-reduced-motion: reduce) {
  .ys-volpop { transition: none; }
}

/* ── 副岛（2026-10-29）：播放条右侧那颗搜索按钮 ──
   2026-10-29 用户：「搜索按钮高度和ui要和播放条一样.并且是正圆」
   → 与胶囊等高（64 / 72）、正圆（宽高相等 + rounded-full）、沿用 .ys-player-glass 的底与边框，
     阴影也换成与胶囊同一套，否则两套不同偏移的投影会在中间叠出一条暗缝。
   播放条 576、副岛 72、间距 8 → 整行 656，超过就让它自己收窄（flex-1 的胶囊先缩）。 */
.ys-sideisland {
  box-shadow: 0 8px 30px rgba(0, 0, 0, 0.28);
  /* 2026-10-29 用户：「播放条的有两条细一点,离播放条近一点」
     → 胶囊与副岛各自都有一圈 1px 描边，两者挨在一起时看着是「两条粗线」。
     Tailwind 的 border utilities 在任意数值下都会生成 1px 物理 px（0.5px 声明被
     rounded to device pixels 之后仍渲染成 1px），所以这里用 color-mix 调淡颜色
     （等效 0.5px 的视觉浓度），并把两者间距收到 2px。 */
  border-color: color-mix(in srgb, rgba(255, 255, 255, 0.1) 50%, transparent);
}

/* ── 封面悬停蒙版：压暗 + 放大图标（0 → 1 淡入，reduced-motion 下直接显示） ── */
@media (prefers-reduced-motion: reduce) {
  .ys-cover-mask { transition: none; }
}

/* 系统减弱动效：只淡入淡出，不做缩放 */
@media (prefers-reduced-motion: reduce) {
  .ys-volpop-enter-active,
  .ys-volpop-leave-active {
    transition: opacity 80ms linear;
  }
  .ys-volpop-enter-from,
  .ys-volpop-leave-to {
    transform: translate(-50%, 0);
  }
}
</style>
