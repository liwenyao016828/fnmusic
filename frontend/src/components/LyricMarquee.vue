<template>
  <!--
    歌词单行自动滚动条（2026-10-26 用户要求：显示一行，长度超出自动滚动）。
    · 只在文本真的超出容器宽度时才滚动，短行静止；
    · 两份文本首尾相接，translateX(-50%) 正好滚完一份，形成无缝循环；
    · direction="left"：向左滚动（播放条歌词行）；
    · direction="right"：向右滚动（折叠最小播放框里「歌名 + 歌词」那一段）。
    尺寸变化 / 文本变化都会重新测量。
  -->
  <span ref="root" class="lyric-marquee">
    <span
      class="lyric-marquee__inner"
      :class="{ 'is-scrolling': scrolling, 'is-right': direction === 'right' }"
    >
      <span class="lyric-marquee__text">{{ text }}</span>
      <span v-if="scrolling" class="lyric-marquee__text" aria-hidden="true">{{ text }}</span>
    </span>
  </span>
</template>

<script setup>
import { ref, watch, nextTick, onMounted, onUnmounted } from 'vue'

const props = defineProps({
  text: { type: String, default: '' },
  direction: { type: String, default: 'left' },
})

const root = ref(null)
const scrolling = ref(false)
let ro = null

function measure() {
  const el = root.value
  if (!el) return
  scrolling.value = el.scrollWidth > el.clientWidth + 1
}

onMounted(() => {
  if (typeof ResizeObserver === 'function' && root.value) {
    ro = new ResizeObserver(measure)
    ro.observe(root.value)
  }
  measure()
})
onUnmounted(() => { if (ro) ro.disconnect() })
watch(() => props.text, () => nextTick(measure))
</script>

<style scoped>
.lyric-marquee {
  display: block;
  width: 100%;
  overflow: hidden;
}
.lyric-marquee__inner {
  display: inline-flex;
  align-items: center;
  white-space: nowrap;
  will-change: transform;
}
.lyric-marquee__inner.is-scrolling {
  animation: lyric-marquee-left 18s linear infinite;
}
.lyric-marquee__inner.is-scrolling.is-right {
  animation-name: lyric-marquee-right;
}
@keyframes lyric-marquee-left {
  from { transform: translateX(0); }
  to { transform: translateX(-50%); }
}
@keyframes lyric-marquee-right {
  from { transform: translateX(-50%); }
  to { transform: translateX(0); }
}
@media (prefers-reduced-motion: reduce) {
  .lyric-marquee__inner.is-scrolling {
    animation: none;
  }
}
</style>
