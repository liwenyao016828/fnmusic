<template>
  <section class="discover-player" aria-label="正在播放与滚动歌词">
    <div class="discover-player__backdrop" aria-hidden="true" :style="backdropStyle"></div>
    <div class="discover-player__veil" aria-hidden="true"></div>

    <div class="discover-player__body">
      <!-- ── 顶部：歌名 · 歌手 同一行（封面已挪去当背景，不再单独占位） ── -->
      <header v-if="song" class="discover-player__meta">
        <h3 class="discover-player__title">{{ title }}</h3>
        <p class="discover-player__artist">{{ artist }}</p>
      </header>

      <!-- ── 中部：iOS 计时器式的立体滚动歌词 ──
           上下拖动 / 滚轮可翻页，松手后跳到对应歌词时间。 -->
      <div
        ref="lyricBox"
        class="discover-player__lyrics"
        :class="dragging && 'is-dragging'"
        tabindex="0"
        role="region"
        aria-label="滚动歌词，上下拖动可跳转到对应歌词"
        @wheel.prevent="onWheel"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointercancel="onPointerUp"
        @keydown="onKey"
      >
        <p v-if="!lines.length && song" class="discover-player__empty">{{ lyricData ? '歌词即将开始' : '这首歌暂无滚动歌词' }}</p>
        <!-- 没在放歌时，这里接住首页的问候语：两行、左对齐，整块垂直居中。 -->
        <div v-else-if="!lines.length" class="discover-player__idle">
          <div class="discover-player__idle-copy">
            <p v-for="(row, i) in emptyCopy" :key="i" class="discover-player__idle-line">{{ row }}</p>
          </div>
        </div>
        <div v-else class="discover-player__wheel">
          <p
            v-for="(line, i) in lines"
            :key="`${i}-${line.time}`"
            class="discover-player__line"
            :class="isCenter(i) && 'is-active'"
            :style="lineStyle(i)"
          >
            <span>{{ line.text }}</span><small v-if="line.trans">{{ line.trans }}</small>
          </p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { activeLyricIndex, parseDisplayLyrics } from '../services/displayPaging'

const props = defineProps({
  song: Object,
  playing: Boolean,
  /* 首页那句问候语（「下午好，来点音乐」），没在放歌时显示在卡片中间 */
  greeting: { type: String, default: '' },
  lyricData: { type: String, default: '' },
  transData: { type: String, default: '' },
  currentTime: { type: Number, default: 0 },
  duration: { type: Number, default: 0 },
})
/* 只保留 seek：播放控制在底部灵动播放条里，卡片不再重复。 */
const emit = defineEmits(['seek'])

const lyricBox = ref(null)
const parallax = ref(0)

const lines = computed(() => parseDisplayLyrics(props.lyricData, props.transData))
/* 没有歌时整个顶部不渲染（改用中间的问候语），所以这里不再有「还没有播放歌曲」这类兜底文案 */
const title = computed(() => props.song?.name || props.song?.title || '')
const artist = computed(() => props.song?.singer || props.song?.artist || '')

/* 问候语按逗号切两行，补上句读：「下午好，」/「来点音乐。」 */
const emptyCopy = computed(() => {
  const raw = (props.greeting || '来点音乐').trim().replace(/[。.]$/, '')
  const parts = raw.split(/[，,、]/).map(s => s.trim()).filter(Boolean)
  if (!parts.length) return ['来点音乐。']
  if (parts.length === 1) return [`${parts[0]}。`]
  return [`${parts[0]}，`, `${parts.slice(1).join('，')}。`]
})

/* 暂停时保留最后一句高亮，不要整段歌词瞬间「熄灭」。 */
const activeRaw = computed(() => activeLyricIndex(lines.value, props.currentTime))
const pausedActive = ref(-1)
watch(activeRaw, v => { if (v >= 0) pausedActive.value = v })
const active = computed(() => props.playing ? activeRaw.value : pausedActive.value)

/* ── 封面当背景：模糊 + 半透明 ── */
const backdropStyle = computed(() => ({
  backgroundImage: props.song?.cover ? `url("${String(props.song.cover).replace(/["\\]/g, '\\$&')}")` : 'none',
  transform: `translate3d(0, ${parallax.value * 18}px, 0) scale(1.1)`,
}))

/* ── iOS 计时器式：歌词挂在圆柱面上，当前句在正中间 ──
   pos 是「滚到中间的那一行」（可以是拖拽中的小数），auto 跟随播放进度。
   几何：每一行贴在圆柱表面（轴向为水平），离中心 d 行就转过 |d|·STEP 弧度，
   于是越远的行越扁、越暗、越靠后 —— 就是 iOS 计时器那种滚轮数字的观感。 */
const pos = ref(0)
const manual = ref(false)
const dragging = ref(false)
watch(active, v => { if (!manual.value) pos.value = Math.max(0, v) })
watch(() => [props.song?.id, props.lyricData], () => { manual.value = false; dragging.value = false; pos.value = Math.max(0, active.value) })

const RADIUS = 118   // 虚拟圆柱半径（px）
const STEP = 0.44    // 每行绕圆柱转过的角度（弧度，约 25°）
const ARC_PX = RADIUS * STEP   // 中心附近一行对应的弧长（px），拖拽换算用

/* 当前这句的字号按「这句有多长」反推：字少的撑到接近满宽，字多的自动收小，
   于是永远刚好占满卡片又不溢出——真正意义上的「适配整个播放窗口」。 */
const boxWidth = ref(0)
let ro = null
onMounted(() => {
  if (!lyricBox.value || typeof ResizeObserver === 'undefined') return
  ro = new ResizeObserver(entries => { boxWidth.value = entries[0]?.contentRect?.width || 0 })
  ro.observe(lyricBox.value)
  boxWidth.value = lyricBox.value.clientWidth
})
onBeforeUnmount(() => { ro?.disconnect() })

const ACTIVE_MIN = 20
const SIDE_PAD = 14
const activeFontSize = computed(() => {
  const text = lines.value[Math.round(pos.value)]?.text
  const avail = boxWidth.value - SIDE_PAD * 2
  if (!text || avail <= 0) return 0
  /* 估算这句要占多少 em：中日韩全角算 1，空格 0.34，其余（拉丁字母/数字）0.56 */
  let em = 0
  for (const ch of text) {
    const code = ch.codePointAt(0)
    if (code > 0x2e80) em += 1
    else if (ch === ' ') em += 0.34
    else em += 0.56
  }
  /* 短句最多放大到「9 个字宽」，同时给 40px 硬顶：卡片宽时行距跟不上，字再大就会压住相邻行 */
  const cap = Math.min(40, avail / 9)
  return Math.round(Math.max(ACTIVE_MIN, Math.min(cap, avail / Math.max(em, 1))))
})

function isCenter(i) { return Math.abs(i - pos.value) < 0.5 }
function lineStyle(i) {
  const d = i - pos.value
  const ad = Math.abs(d)
  const theta = d * STEP
  const y = RADIUS * Math.sin(theta)
  const z = RADIUS * (Math.cos(theta) - 1)
  const opacity = ad <= 0.5 ? 1 : ad <= 1.5 ? 0.82 : ad <= 2.5 ? 0.5 : ad <= 3.5 ? 0.22 : 0
  /* 1-2-3-2-1 的字号梯度：中间最大，往上往下逐级收紧 */
  const scale = 1 - Math.min(ad, 4) * 0.11
  const style = {
    transform: `translateY(-50%) translate3d(0, ${y.toFixed(2)}px, ${z.toFixed(2)}px) rotateX(${(theta * 180 / Math.PI).toFixed(2)}deg) scale(${scale.toFixed(3)})`,
    opacity: String(opacity),
    zIndex: String(60 - Math.round(ad * 10)),
  }
  if (ad < 0.5 && activeFontSize.value) style.fontSize = `${activeFontSize.value}px`
  return style
}

/* ── 拖拽翻页：松手后跳到最近一句 ── */
let startY = 0
let startPos = 0
function onPointerDown(event) {
  if (event.button !== 0 || !lines.value.length) return
  dragging.value = true
  manual.value = true
  startY = event.clientY
  startPos = pos.value
  try { lyricBox.value?.setPointerCapture?.(event.pointerId) } catch {}
}
function onPointerMove(event) {
  if (!dragging.value) return
  pos.value = startPos + (startY - event.clientY) / ARC_PX
  parallax.value = Math.max(-1, Math.min(1, (pos.value - startPos) / 6))
}
function onPointerUp(event) {
  if (!dragging.value) return
  dragging.value = false
  try { lyricBox.value?.releasePointerCapture?.(event.pointerId) } catch {}
  parallax.value = 0
  settle()
}
/* 滚轮翻页 */
let wheelTimer
function onWheel(event) {
  if (!lines.value.length) return
  manual.value = true
  const step = event.deltaY > 0 ? 0.6 : -0.6
  pos.value = Math.max(0, Math.min(lines.value.length - 1, pos.value + step))
  clearTimeout(wheelTimer)
  wheelTimer = setTimeout(settle, 260)
}
function settle() {
  const nearest = Math.max(0, Math.min(lines.value.length - 1, Math.round(pos.value)))
  pos.value = nearest
  manual.value = false
  if (lines.value[nearest]) emit('seek', Math.max(0, lines.value[nearest].time))
}
function onKey(event) {
  if (!lines.value.length) return
  const last = lines.value.length - 1
  let target = null
  if (event.key === 'ArrowDown') target = Math.min(last, Math.round(pos.value) + 1)
  else if (event.key === 'ArrowUp') target = Math.max(0, Math.round(pos.value) - 1)
  else if (event.key === 'Home') target = 0
  else if (event.key === 'End') target = last
  if (target == null) return
  event.preventDefault()
  pos.value = target
  manual.value = true
  clearTimeout(wheelTimer)
  wheelTimer = setTimeout(settle, 260)
}

onBeforeUnmount(() => clearTimeout(wheelTimer))
</script>

<style scoped>
/* 卡片宽度由 hero 栅格决定，所以内部一律用 container query 自适应，
   不再跟视口宽度走。
   注意：根节点**不设 display**。SearchView 用 `hidden md:block` 控制移动端隐藏，
   而 scoped 样式会给选择器加上 [data-v-xxx]，特异性(0,2,0)会压过 Tailwind 的
   单类(0,1,0)，一设 display 移动端就藏不住了。纵向排布放在 __body 上。 */
.discover-player {
  position: relative;
  min-height: 330px;
  padding: 22px 22px 18px;
  overflow: hidden;
  isolation: isolate;
  border-radius: 18px;
  background: #121110;
  color: #fff;
  box-shadow: 0 18px 50px rgba(24, 18, 12, .18), inset 0 0 0 1px rgba(255, 255, 255, .06);
  container-type: inline-size;
}

/* 封面 → 背景：让封面**真的看得见**（放大裁切 + 中等模糊 + 高不透明度），
   上面再压一层由深到浅的暗色渐隐，保证歌名与歌词始终有对比度。 */
.discover-player__backdrop {
  position: absolute;
  inset: -40px;
  z-index: -2;
  background-position: center;
  background-size: cover;
  filter: blur(16px) saturate(1.75) brightness(1.06);
  opacity: .95;
  will-change: transform;
  transition: background-image .35s, transform .6s cubic-bezier(.22, .9, .26, 1);
}
.discover-player__veil {
  position: absolute;
  inset: 0;
  z-index: -1;
  background:
    /* 顶部一点高光，像封面透过来的光 */
    radial-gradient(120% 74% at 50% -6%, rgba(255, 255, 255, .1), transparent 62%),
    /* 中间给封面留出最大的透出面积，顶部和底部各压一道暗，文字才立得住 */
    linear-gradient(180deg, rgba(9, 8, 7, .46) 0%, rgba(9, 8, 7, .2) 32%, rgba(9, 8, 7, .28) 60%, rgba(9, 8, 7, .62) 100%);
}

/* 上中下：顶部歌名/歌手，中部（+下部）歌词。 */
.discover-player__body { display: flex; flex-direction: column; height: 100%; min-height: 0; }

/* ── 顶部信息：歌名和歌手并排一行，歌名优先占宽、歌手跟在后面对齐基线 ── */
.discover-player__meta { display: flex; align-items: baseline; gap: 12px; flex: 0 0 auto; min-width: 0; }
.discover-player__title {
  flex: 0 1 auto;
  min-width: 0;
  margin: 0;
  font-size: 20px;
  font-weight: 650;
  line-height: 1.3;
  letter-spacing: -.012em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  text-shadow: 0 2px 14px rgba(0, 0, 0, .45);
}
.discover-player__artist {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 46%;
  margin: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-size: 13px;
  line-height: 1.3;
  color: rgba(255, 255, 255, .64);
  text-shadow: 0 1px 10px rgba(0, 0, 0, .4);
}

/* ── 立体滚动歌词（iOS 计时器那种圆柱面） ── */
.discover-player__lyrics {
  position: relative;
  flex: 1 1 0;
  min-height: 150px;
  margin-top: 10px;
  overflow: hidden;
  touch-action: pan-y;
  user-select: none;
  cursor: grab;
  outline: none;
  mask-image: linear-gradient(transparent, #000 16%, #000 84%, transparent);
  -webkit-mask-image: linear-gradient(transparent, #000 16%, #000 84%, transparent);
}
.discover-player__lyrics.is-dragging { cursor: grabbing; }
.discover-player__lyrics:focus-visible { box-shadow: inset 0 0 0 1px rgba(255, 255, 255, .22); border-radius: 12px; }
.discover-player__wheel { position: absolute; inset: 0; perspective: 620px; perspective-origin: 50% 50%; }
.discover-player__line {
  position: absolute;
  left: 0;
  right: 0;
  top: 50%;
  margin: 0;
  padding: 3px 0;
  text-align: center;
  color: rgba(255, 255, 255, .6);
  font-size: clamp(15px, 4.4cqw, 26px);
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-wrap: balance;
  text-shadow: 0 2px 12px rgba(0, 0, 0, .55);
  transform-origin: 50% 50%;
  transition: transform .52s cubic-bezier(.22, .9, .26, 1), opacity .4s ease;
  backface-visibility: hidden;
}
.discover-player__lyrics.is-dragging .discover-player__line { transition: none; }
.discover-player__line.is-active { color: #fff; font-size: clamp(22px, 7.2cqw, 40px); font-weight: 650; letter-spacing: -.015em; }
.discover-player__line small { display: block; margin-top: 3px; color: inherit; opacity: .62; font-size: 12.5px; font-weight: 400; }
.discover-player__empty { display: grid; place-items: center; height: 100%; margin: 0; color: rgba(255, 255, 255, .5); text-align: center; font-size: 14px; }

/* ── 闲置态：首页那句问候语搬到这里，两行、左对齐，整块在卡片里居中 ── */
.discover-player__idle { display: flex; align-items: center; justify-content: center; height: 100%; }
.discover-player__idle-copy { text-align: left; }
.discover-player__idle-line {
  margin: 0;
  font-size: clamp(24px, 7.4cqw, 34px);
  font-weight: 650;
  letter-spacing: -.02em;
  line-height: 1.42;
  color: #fff;
}
.discover-player__idle-line + .discover-player__idle-line { color: rgba(255, 255, 255, .82); }

/* ── 按卡片自身宽度分档（歌词字号用 cqw 自动跟手，这里只管标题与留白） ── */
@container (min-width: 320px) {
  .discover-player__title { font-size: 21px; }
}
@container (min-width: 420px) {
  .discover-player__title { font-size: 23px; }
  .discover-player__artist { font-size: 14px; }
}
@container (min-width: 560px) {
  .discover-player { padding: 26px 28px 22px; }
  .discover-player__title { font-size: 25px; }
  .discover-player__artist { font-size: 15px; }
}
@container (min-width: 720px) {
  .discover-player__title { font-size: 27px; }
  /* 卡片变宽（平板单列 / 大屏）后歌词不再铺满整行，收成居中的窄列，
     阅读距离更近、也让放大的当前句有个稳定的视觉支点。 */
  .discover-player__lyrics { width: 100%; max-width: 620px; margin-inline: auto; }
}

@media (prefers-reduced-motion: reduce) {
  .discover-player__backdrop { transform: none !important; }
  .discover-player__line { transition: none; }
}
</style>
