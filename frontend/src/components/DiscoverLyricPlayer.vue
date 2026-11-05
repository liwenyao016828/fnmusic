<template>
  <section ref="root" class="discover-player" aria-label="正在播放与滚动歌词">
    <div class="discover-player__backdrop" aria-hidden="true" :style="backdropStyle"></div>
    <div class="discover-player__veil" aria-hidden="true"></div>
    <div class="discover-player__content" :style="contentStyle">
      <div class="discover-player__art-wrap">
        <img v-if="song?.cover" :src="song.cover" class="discover-player__art" alt="" />
        <div v-else class="discover-player__art grid place-items-center bg-white/10 text-white/50"><Disc3 class="h-12 w-12" /></div>
        <span class="discover-player__live"><span :class="['h-1.5 w-1.5 rounded-full bg-orange-300', playing && 'animate-pulse']"></span>{{ playing ? '正在播放' : '已暂停' }}</span>
      </div>
      <div class="discover-player__copy">
        <div class="discover-player__eyebrow">NOW PLAYING · 随音乐滚动</div>
        <h3 class="discover-player__title">{{ song?.name || '还没有播放歌曲' }}</h3>
        <p class="discover-player__artist">{{ song?.singer || '从下方歌单挑一首，让歌词在这里流动' }}</p>
        <div ref="lyricBox" class="discover-player__lyrics" tabindex="0" role="region" aria-label="滚动歌词，上下滑动后跳转到居中歌词" @wheel.passive="beginManualScroll" @pointerdown="onPointerDown" @pointermove="onPointerMove" @pointerup="pointerY = null" @pointercancel="pointerY = null" @pointerleave="pointerY = null" @keydown="onLyricKey" @scroll.passive="onScroll">
          <p v-if="!lines.length" class="discover-player__empty">{{ lyricData ? '歌词即将开始' : '这首歌暂无滚动歌词' }}</p>
          <p v-for="(line, i) in lines" :key="`${i}-${line.time}`" :ref="el => { lineEls[i] = el }" :class="['discover-player__line', i === active && 'is-active', i < active && 'is-past']">
            <span>{{ line.text }}</span><small v-if="line.trans">{{ line.trans }}</small>
          </p>
        </div>
        <div class="discover-player__foot"><span>{{ formatTime(currentTime) }}</span><span>{{ formatTime(duration) }}</span></div>
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Disc3 } from 'lucide-vue-next'
import { activeLyricIndex, parseDisplayLyrics } from '../services/displayPaging'

const props = defineProps({
  song: Object,
  playing: Boolean,
  lyricData: { type: String, default: '' },
  transData: { type: String, default: '' },
  currentTime: { type: Number, default: 0 },
  duration: { type: Number, default: 0 },
})
const emit = defineEmits(['seek'])
const root = ref(null)
const lyricBox = ref(null)
const lineEls = ref([])
const parallax = ref(0)
const lines = computed(() => parseDisplayLyrics(props.lyricData, props.transData))
const active = computed(() => props.playing ? activeLyricIndex(lines.value, props.currentTime) : -1)
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
const backdropStyle = computed(() => ({
  backgroundImage: props.song?.cover ? `url("${String(props.song.cover).replace(/["\\]/g, '\\$&')}")` : 'none',
  transform: `translate3d(0, ${parallax.value * 28}px, 0) scale(1.16)`,
}))
const contentStyle = computed(() => ({ transform: `translate3d(0, ${parallax.value * -5}px, 0)` }))
let manualScroll = false
let seekTimer
function beginManualScroll() {
  if (!lines.value.length) return
  manualScroll = true
  clearTimeout(seekTimer)
  // 取消仍在进行的自动平滑滚动，接下来由手势控制。
  const box = lyricBox.value
  if (box) box.scrollTo({ top: box.scrollTop, behavior: 'instant' })
  seekTimer = setTimeout(finishManualScroll, 220)
}
let pointerY = null
function onPointerDown(event) {
  if (event.button !== 0) return
  pointerY = event.clientY
}
function onPointerMove(event) {
  if (pointerY == null) return
  const delta = event.clientY - pointerY
  if (Math.abs(delta) > 3 || manualScroll) {
    beginManualScroll()
    if (event.pointerType === 'mouse' && lyricBox.value) lyricBox.value.scrollTop -= delta
  }
  pointerY = event.clientY
}
function onLyricKey(event) {
  if (['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End'].includes(event.key)) beginManualScroll()
}
function finishManualScroll() {
  const box = lyricBox.value
  if (!manualScroll || !box) return
  const center = box.scrollTop + box.clientHeight / 2
  let nearest = -1
  let distance = Infinity
  lineEls.value.forEach((el, i) => {
    if (!el || !lines.value[i]) return
    const d = Math.abs(el.offsetTop + el.offsetHeight / 2 - center)
    if (d < distance) { distance = d; nearest = i }
  })
  manualScroll = false
  if (nearest >= 0 && props.duration > 0) emit('seek', Math.max(0, Math.min(props.duration, lines.value[nearest].time)))
}
function onScroll() {
  if (!lyricBox.value) return
  if (!reduced()) parallax.value = Math.max(-1, Math.min(1, lyricBox.value.scrollTop / 220))
  if (manualScroll) {
    clearTimeout(seekTimer)
    seekTimer = setTimeout(finishManualScroll, 220)
  }
}
watch([active, () => props.lyricData], async () => {
  await nextTick()
  const box = lyricBox.value
  const line = lineEls.value[active.value]
  if (!box || !line || manualScroll) return
  box.scrollTo({ top: Math.max(0, line.offsetTop - box.clientHeight / 2 + line.clientHeight / 2), behavior: reduced() ? 'auto' : 'smooth' })
})
watch(() => [props.song?.id, props.song?.cover, props.lyricData], () => { parallax.value = 0; manualScroll = false; clearTimeout(seekTimer); lineEls.value = [] })
function formatTime(v) { const n = Math.max(0, Math.floor(Number(v) || 0)); return `${Math.floor(n / 60)}:${String(n % 60).padStart(2, '0')}` }
let observer
onMounted(() => { if (typeof ResizeObserver === 'function' && lyricBox.value) { observer = new ResizeObserver(() => { const line = lineEls.value[active.value]; if (line && lyricBox.value) lyricBox.value.scrollTop = Math.max(0, line.offsetTop - lyricBox.value.clientHeight / 2) }); observer.observe(lyricBox.value) } })
onBeforeUnmount(() => { observer?.disconnect(); clearTimeout(seekTimer) })
</script>

<style scoped>
.discover-player { position: relative; min-height: 310px; overflow: hidden; isolation: isolate; border-radius: 18px; background: #121110; color: #fff; box-shadow: 0 18px 50px rgba(24, 18, 12, .18); }
.discover-player__backdrop { position: absolute; inset: -36px; z-index: -2; background-position: center; background-size: cover; filter: blur(30px) saturate(1.3); opacity: .82; will-change: transform; transition: background-image .35s; }
.discover-player__veil { position: absolute; inset: 0; z-index: -1; background: linear-gradient(115deg, rgba(12, 10, 9, .45), rgba(12, 10, 9, .68)); backdrop-filter: blur(4px); }
.discover-player__content { display: grid; grid-template-columns: 170px minmax(0, 1fr); align-items: center; gap: 24px; min-height: 310px; padding: 26px; will-change: transform; }
.discover-player__art-wrap { position: relative; align-self: center; }
.discover-player__art { display: block; width: 170px; aspect-ratio: 1; object-fit: cover; border-radius: 16px; border: 1px solid rgba(255,255,255,.3); box-shadow: 0 14px 35px rgba(0,0,0,.3); }
.discover-player__live { display: inline-flex; align-items: center; gap: 6px; margin-top: 14px; color: rgba(255,255,255,.75); font-size: 11px; letter-spacing: .04em; }
.discover-player__copy { min-width: 0; }
.discover-player__eyebrow { font-size: 10px; letter-spacing: .12em; color: #fdba74; }
.discover-player__title { margin: 9px 0 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 22px; font-weight: 650; }
.discover-player__artist { margin: 6px 0 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: rgba(255,255,255,.64); }
.discover-player__lyrics { position: relative; touch-action: pan-y; user-select: none; cursor: grab; height: 148px; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: none; mask-image: linear-gradient(transparent, #000 24%, #000 76%, transparent); padding: 42px 0; }
.discover-player__lyrics::-webkit-scrollbar { display: none; }
.discover-player__line { margin: 0; padding: 5px 0; color: rgba(255,255,255,.55); font-size: 13px; line-height: 1.55; transition: color .25s, opacity .25s, transform .25s; }
.discover-player__line.is-past { opacity: .48; }
.discover-player__line.is-active { color: #fff; font-size: 17px; font-weight: 550; transform: translateX(3px); }
.discover-player__line small { display: block; margin-top: 2px; color: inherit; opacity: .65; font-size: 11px; font-weight: 400; }
.discover-player__empty { color: rgba(255,255,255,.55); text-align: center; font-size: 12px; }
.discover-player__foot { display: flex; justify-content: space-between; margin-top: 9px; color: rgba(255,255,255,.5); font-size: 10px; font-variant-numeric: tabular-nums; }
@media (max-width: 1050px) { .discover-player__content { grid-template-columns: 108px minmax(0, 1fr); gap: 18px; padding: 22px; } .discover-player__art { width: 108px; } .discover-player__title { font-size: 18px; } }
@media (max-width: 640px) { .discover-player__content { grid-template-columns: 76px minmax(0, 1fr); gap: 16px; min-height: 280px; padding: 20px; } .discover-player__art { width: 76px; border-radius: 12px; } .discover-player__lyrics { height: 134px; } .discover-player__title { font-size: 16px; } .discover-player__line.is-active { font-size: 15px; } }
@media (prefers-reduced-motion: reduce) { .discover-player__backdrop, .discover-player__content { transform: none !important; } .discover-player__line { transition: none; } }
</style>
