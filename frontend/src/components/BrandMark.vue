<template>
  <!--
    平台品牌标（网易云 / QQ 音乐）—— 2026-10-18 用户口径：
    「把图标从 q 改为 qq，w 改为网易，或者你改为他们的官方图标。去掉 qq 音乐和网易云文字。」

    v2.9.0 起改用**官方客户端图标位图**（此前的自绘音符只做到「形似」）：
    两张图分别是两家在 App Store 上架客户端的图标资源（网易云音乐 appIconNew、
    QQ 音乐 AppIcon），落盘在 `src/assets/platform/`，由 Vite 打包进产物 ——
    不引外链，NAS 离线也能显示；图省事不在运行时去请求第三方 CDN。

    造型：官方图标本身就是圆角方形，所以容器也用圆角方 + overflow-hidden 裁切
    （此前是 rounded-full，会把方图四角切掉、变成「红圈里一个音符」，反而不像官方图标）。
    圆角取 22%，与 iOS 连续圆角的观感接近。

    用法：<BrandMark platform="netease" class="h-7 w-7" />（尺寸由 class 决定）
    platform 接受 'netease'/'wy' 与 'qq'/'tx' 两种写法（官方协议平台码 / 音源标识）。
  -->
  <span
    class="grid shrink-0 place-items-center overflow-hidden rounded-[22%] ring-1 ring-black/5"
    role="img"
    :aria-label="isQQ ? 'QQ 音乐' : '网易云音乐'"
  >
    <img
      :src="isQQ ? qqIcon : neteaseIcon"
      class="h-full w-full object-cover"
      alt=""
      draggable="false"
    />
  </span>
</template>

<script setup>
import { computed } from 'vue'
import neteaseIcon from '../assets/platform/netease.png'
import qqIcon from '../assets/platform/qq.png'

const props = defineProps({
  /** 'netease' | 'wy' | 'qq' | 'tx' */
  platform: { type: String, default: '' },
})

const isQQ = computed(() => {
  const p = (props.platform || '').toLowerCase()
  return p === 'qq' || p === 'tx'
})
</script>
