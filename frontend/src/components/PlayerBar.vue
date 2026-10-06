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
      空态 = 安静占位（不重复发现页的搜索）｜ 播放态 = 控制 + 封面信息进度 + 功能 ｜ 折叠态 = 细条
    ⚠️ 响应式靠分级隐藏，不用 transform: scale()。
  -->
  <!--
    浮动播放栏（2026-09-28 按用户要求改）：整条**不再占一行布局**，改成浮在内容之上。
    以前它在 flex 流里独占一行（1280×84），两侧露出的是页面底色 #f5f7f9 —— 看着就是一条灰带，
    而飞牛那边内容是从胶囊底下穿过去的。现在整条 `pointer-events-none`（点两侧等于点到内容），
    只有中间那颗胶囊收指针事件 → 「只有中间的播放栏」。
  -->
  <div class="pb-safe pointer-events-none fixed inset-x-0 bottom-0 z-40 select-none">

    <!-- ── 折叠态 ── -->
    <div v-if="collapsed && currentSong" class="px-3 pb-2">
      <button
        @click="$emit('toggle-collapse')"
        class="ys-glass ys-player-glass pointer-events-auto mx-auto flex h-8 w-full max-w-[576px] items-center justify-center gap-2 rounded-full text-[11px] text-white/60 transition-colors hover:text-white"
        title="展开播放条"
      >
        <ChevronUp class="h-3.5 w-3.5" />
        <span class="max-w-[50vw] truncate">{{ currentSong.name || '正在播放' }}</span>
      </button>
    </div>

    <!-- ── 空态：搜索卡片（**搜索的家就在这里** —— 用户 2026-09-28 明确：
         「保留底部播放栏搜索，去掉发现音乐页面的搜索」）—— 高度与播放态一致 ── -->
    <div v-else-if="!currentSong" class="px-3 pb-2 sm:pb-3">
      <form
        @submit.prevent="submitSearch"
        class="ys-glass ys-player-glass ys-player pointer-events-auto mx-auto flex h-[64px] items-center gap-3 px-4 sm:h-[72px]"
      >
        <span class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-white/10 text-emerald-400/70">
          <Disc3 class="h-4 w-4" />
        </span>
        <span class="min-w-0 flex-1">
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            type="search"
            placeholder="搜索歌曲、歌手、专辑…"
            title="搜索歌曲、歌手、专辑"
            class="w-full bg-transparent text-[13px] font-semibold text-white/90 placeholder:text-white/45 focus:outline-none"
          />
          <small class="block truncate text-[11px] text-white/35">也可以直接粘贴歌单链接</small>
        </span>
        <button
          type="submit"
          :disabled="!searchQuery.trim()"
          title="搜索"
          class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-emerald-500 text-white transition-colors hover:bg-emerald-400 disabled:opacity-40"
        >
          <Search class="h-4 w-4" />
        </button>
      </form>
    </div>

    <!-- ── 播放态 ── -->
    <div v-else class="px-3 pb-2 sm:pb-3">
      <div class="ys-glass ys-player-glass ys-player pointer-events-auto relative mx-auto flex h-[64px] items-center gap-2 overflow-hidden px-3 sm:h-[72px] sm:px-4">

        <!-- 底色层：封面放大 + 模糊 + 压暗（对应它的 `background: CV`） -->
        <img
          v-if="currentSong.cover"
          :src="currentSong.cover"
          alt=""
          aria-hidden="true"
          class="pointer-events-none absolute inset-0 h-full w-full scale-150 object-cover opacity-30 blur-2xl"
        />
        <span class="pointer-events-none absolute inset-0 bg-black/30" aria-hidden="true"></span>

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
            class="hidden h-9 w-9 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white sm:grid"
          >
            <SkipBack class="h-4 w-4 fill-current" />
          </button>
          <button
            @click="$emit('toggle-play')"
            :disabled="isResolving"
            :title="isPlaying ? '暂停' : '播放'"
            class="grid h-10 w-10 shrink-0 place-items-center rounded-full bg-emerald-500 text-white transition-all hover:bg-emerald-400 active:scale-95 disabled:opacity-60"
          >
            <Loader2 v-if="isResolving" class="h-4 w-4 animate-spin" />
            <Pause v-else-if="isPlaying" class="h-4 w-4 fill-current" />
            <Play v-else class="h-4 w-4 translate-x-0.5 fill-current" />
          </button>
          <button
            @click="$emit('next')"
            title="下一首"
            class="hidden h-9 w-9 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white sm:grid"
          >
            <SkipForward class="h-4 w-4 fill-current" />
          </button>
        </div>

        <!-- 中：搜索输入（点了右侧放大镜就换过来） -->
        <div v-if="searchMode" class="relative flex min-w-0 flex-1 items-center gap-2">
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            type="search"
            placeholder="搜索歌曲、歌手、专辑…"
            @keyup.enter="submitSearch"
            class="min-w-0 flex-1 rounded-full bg-white/10 px-3 py-2 text-[13px] text-white/90 placeholder:text-white/45 focus:outline-none"
          />
          <button
            @click="cancelSearch"
            title="取消搜索"
            class="grid h-8 w-8 shrink-0 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white"
          ><X class="h-4 w-4" /></button>
        </div>

        <!-- 中：封面 + 歌曲信息 + 进度（弹性区） -->
        <div v-else class="relative flex min-w-0 flex-1 items-center gap-2.5">
          <button
            @click="$emit('toggle-fullscreen')"
            class="grid h-[38px] w-[38px] shrink-0 place-items-center overflow-hidden rounded-md bg-white/10 text-white/50 transition-transform hover:scale-[1.03]"
            title="打开播放页"
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
          </button>

          <div class="min-w-0 flex-1">
            <b class="block max-w-[234px] truncate text-[13px] font-semibold text-white/90 sm:text-sm">
              {{ currentSong.name || currentSong.title || '曲率' }}
            </b>
            <small class="hidden max-w-[234px] truncate text-[11px] text-white/45 sm:block">
              {{ currentSong.singer || currentSong.artist || '—' }}
            </small>
            <!-- 进度：细线；≥sm 时两端带时间。手机（<sm）它就在歌名下面那一行 -->
            <div class="mt-1 flex items-center gap-2">
              <span class="hidden w-9 shrink-0 text-right font-mono text-[10px] text-white/40 sm:block">{{ formatTime(currentTime) }}</span>
              <!-- ⚠️ 视觉只有 4px，手指点不准：before 伪元素把命中区上下各撑 12px（视觉粗细不变） -->
              <span
                @click="handleSeek"
                class="relative h-1 flex-1 cursor-pointer rounded-full bg-white/15 before:absolute before:inset-x-0 before:-top-3 before:-bottom-3 before:content-['']"
              >
                <span class="block h-1 rounded-full bg-emerald-400" :style="{ width: progressPercent + '%' }"></span>
              </span>
              <span class="hidden w-9 shrink-0 font-mono text-[10px] text-white/40 sm:block">{{ formatTime(duration) }}</span>
            </div>
          </div>
        </div>

        <!-- 右：功能按钮（搜索 / 歌词 / 音量 / 播放页 / 收缩） -->
        <div class="relative flex shrink-0 items-center gap-0.5 sm:gap-1.5">
          <button
            @click="searchMode = true"
            title="搜索"
            class="grid h-9 w-9 place-items-center rounded-full text-white/55 transition-colors hover:bg-white/10 hover:text-white"
          ><Search class="h-4 w-4" /></button>
          <button
            @click="$emit('toggle-lyrics')"
            :class="['hidden h-9 rounded-full px-2 text-[11px] font-semibold transition-colors lg:block', showLyrics ? 'bg-emerald-500/20 text-emerald-300' : 'text-white/55 hover:bg-white/10 hover:text-white']"
            title="歌词"
          >词</button>
          <div class="hidden items-center gap-1.5 xl:flex">
            <button @click="toggleMute" title="静音" class="grid h-9 w-9 place-items-center rounded-full text-white/55 transition-colors hover:bg-white/10 hover:text-white">
              <VolumeX v-if="isMuted" class="h-4 w-4 text-rose-400" />
              <Volume1 v-else-if="volume < 0.5" class="h-4 w-4" />
              <Volume2 v-else class="h-4 w-4" />
            </button>
            <input
              type="range" min="0" max="1" step="0.01" v-model="volume"
              title="音量"
              class="h-1 w-16 cursor-pointer appearance-none rounded-full bg-white/20 accent-emerald-500"
            />
          </div>
          <button
            @click="$emit('toggle-fullscreen')"
            title="播放页"
            class="hidden h-9 w-9 place-items-center rounded-full text-white/55 transition-colors hover:bg-white/10 hover:text-white lg:grid"
          >
            <Maximize class="h-4 w-4" />
          </button>
          <button
            @click="$emit('toggle-collapse')"
            title="收起播放条"
            class="grid h-9 w-9 place-items-center rounded-full text-white/60 transition-colors hover:bg-white/10 hover:text-white"
          >
            <ChevronDown class="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, watch } from 'vue'
import {
  Play, Pause, SkipBack, SkipForward, Shuffle, Repeat, Repeat1,
  Volume2, Volume1, VolumeX, Maximize, Music, Loader2,
  Disc3, ChevronDown, ChevronUp, Search, X,
} from 'lucide-vue-next'

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
})
const emit = defineEmits([
  'toggle-play', 'prev', 'next', 'seek', 'toggle-lyrics', 'toggle-fullscreen',
  'toggle-view', 'volume-change', 'update:playMode', 'toggle-collapse', 'search',
])

// ── 搜索入口（搜索的家在播放栏：用户 2026-09-28 定的）──
const searchQuery = ref('')
const searchMode = ref(false)
const searchInputRef = ref(null)

/** 提交搜索：把词交给 App.vue（它负责切到发现页并把词转给 SearchView 去执行） */
function submitSearch() {
  const q = searchQuery.value.trim()
  if (!q) return
  emit('search', q)
  cancelSearch()
}
function cancelSearch() {
  searchMode.value = false
  searchQuery.value = ''
}
watch(searchMode, async (on) => {
  if (!on) return
  await nextTick()
  searchInputRef.value?.focus()
})

const volume = ref(0.8)
const isMuted = ref(false)
const prevVolume = ref(0.8)

const playModes = ['sequence', 'random', 'loop']
const playModeTitle = computed(() => ({ sequence: '列表循环', random: '随机播放', loop: '单曲循环' }[props.playMode]))
function cyclePlayMode() {
  const nextIdx = (playModes.indexOf(props.playMode) + 1) % playModes.length
  emit('update:playMode', playModes[nextIdx])
}

const progressPercent = computed(() => (!props.duration ? 0 : Math.min(100, (props.currentTime / props.duration) * 100)))

function formatTime(sec) {
  if (!sec || isNaN(sec)) return '00:00'
  return Math.floor(sec / 60).toString().padStart(2, '0') + ':' + Math.floor(sec % 60).toString().padStart(2, '0')
}

/**
 * 点哪儿跳哪儿。用 `currentTarget` 取坐标 —— 手机上那条细线在歌名下面、
 * 桌面上是中间那条带时间的，两处共用这一个处理函数，就不必各挂一个 ref。
 */
function handleSeek(e) {
  if (!props.duration) return
  const rect = e.currentTarget.getBoundingClientRect()
  if (!rect.width) return
  emit('seek', Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width)) * props.duration)
}

function toggleMute() {
  if (isMuted.value) { isMuted.value = false; volume.value = prevVolume.value || 0.8 }
  else { prevVolume.value = volume.value; volume.value = 0; isMuted.value = true }
}

watch(volume, (val) => { emit('volume-change', parseFloat(val)); if (val > 0) isMuted.value = false })
</script>
