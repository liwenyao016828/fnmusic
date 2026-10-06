<template>
  <!--
    开发者用的帧率探针：地址后面加 `?fps=1` 才出现（正式界面里看不到）。
    为什么要它：滑动的"卡不卡"在开发和测试机上测不准（我这台机器 5 页全挂也是 60fps），
    必须拿到真机数字。这里除了实时 FPS，还专门记录**最近一次滑动过程中最差的一帧**——
    平均帧率好看但"某几帧掉到 100ms"才是手感的杀手。
  -->
  <div class="pointer-events-none fixed right-2 top-2 z-[80] rounded-lg bg-black/75 px-2 py-1 font-mono text-[10px] leading-tight text-emerald-300 shadow-lg">
    <div>FPS {{ fps }}</div>
    <div class="text-white/70">滑动最差 {{ worstDuringSwipe }}ms</div>
    <div class="text-white/40">帧 {{ frameCount }}</div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'

const fps = ref(0)
const worstDuringSwipe = ref(0)
const frameCount = ref(0)

let raf = 0
let last = performance.now()
let winStart = last
let winFrames = 0
let swiping = false
let worst = 0
let observer = null

function tick(now) {
  const delta = now - last
  last = now
  winFrames++
  frameCount.value++
  if (swiping && delta > worst) worst = delta
  if (now - winStart >= 500) {
    fps.value = Math.round((winFrames * 1000) / (now - winStart))
    winStart = now
    winFrames = 0
    if (!swiping) worstDuringSwipe.value = Math.round(worst)
  }
  raf = requestAnimationFrame(tick)
}

onMounted(() => {
  raf = requestAnimationFrame(tick)
  // SwipePager 在滑动时会挂 html[data-swiping] —— 这里跟着它统计"滑动期间最差一帧"
  observer = new MutationObserver(() => {
    const on = document.documentElement.dataset.swiping === '1'
    if (on && !swiping) { swiping = true; worst = 0 }
    if (!on && swiping) { swiping = false; worstDuringSwipe.value = Math.round(worst); worst = 0 }
  })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-swiping'] })
})

onBeforeUnmount(() => {
  if (raf) cancelAnimationFrame(raf)
  if (observer) observer.disconnect()
})
</script>
