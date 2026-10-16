<template>
  <!--
    平台品牌标（网易云 / QQ 音乐）—— 2026-10-18 用户口径：
    「把图标从 q 改为 qq，w 改为网易，或者你改为他们的官方图标。去掉 qq 音乐和网易云文字。」
    选的是后者：不再用「N / Q」这种自造字母，也不再在标旁边写平台名 ——
    红底单音符 = 网易云音乐、绿底双音符（♫）= QQ 音乐，两个标志自己就说明是谁，
    完整名字进 `title`（悬停可见），版面省掉一行文字。

    为什么是手绘 SVG 而不是官方图片资源：应用要能离线跑、打包体积也不再增加图片请求，
    而这两个标志的核心识别特征就是「底色 + 音符形状」，用 path 画得出来且永不失真。

    用法：<BrandMark platform="netease" class="h-7 w-7" />（尺寸由 class 决定）
    platform 接受 'netease'/'wy' 与 'qq'/'tx' 两种写法（P 音源标识与官方协议平台码）。
  -->
  <span
    class="grid shrink-0 place-items-center rounded-full text-white"
    :class="isQQ ? 'bg-[#31C27C]' : 'bg-[#C20C0C]'"
    role="img"
    :aria-label="isQQ ? 'QQ 音乐' : '网易云音乐'"
  >
    <svg viewBox="0 0 24 24" class="h-[68%] w-[68%]" fill="currentColor" aria-hidden="true">
      <!-- QQ 音乐：双音符 ♫（两根符干 + 斜杠符梁 + 两个符头） -->
      <template v-if="isQQ">
        <rect x="7.1" y="6.3" width="1.75" height="10.4" rx="0.87" />
        <rect x="13.2" y="4.5" width="1.75" height="10.4" rx="0.87" />
        <path d="M7.1 6.3 15 4.5v2.55L7.1 8.85z" />
        <ellipse cx="8" cy="16.95" rx="2.95" ry="2.25" transform="rotate(-18 8 16.95)" />
        <ellipse cx="14.1" cy="15.15" rx="2.95" ry="2.25" transform="rotate(-18 14.1 15.15)" />
      </template>
      <!-- 网易云音乐：单音符（一根符干 + 一个卷曲符尾 + 一个符头） -->
      <template v-else>
        <rect x="10.3" y="4.9" width="1.85" height="11.4" rx="0.92" />
        <path d="M12.15 4.9c2.7.55 4.6 2.05 5.3 4.35.3.95-.25 1.7-1.2 1.6-.95-.1-1.7-.75-2.2-1.5-.55-.8-1.15-1.4-1.9-1.75z" />
        <ellipse cx="9.5" cy="16.85" rx="3.05" ry="2.3" transform="rotate(-18 9.5 16.85)" />
      </template>
    </svg>
  </span>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  /** 'netease' | 'wy' | 'qq' | 'tx' */
  platform: { type: String, default: '' },
})

const isQQ = computed(() => {
  const p = (props.platform || '').toLowerCase()
  return p === 'qq' || p === 'tx'
})
</script>
