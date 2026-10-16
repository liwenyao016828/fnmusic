<template>
  <PageShell>
    <template #title-extra>
      <span class="rounded-full border border-emerald-200 bg-emerald-50 px-2 py-0.5 font-mono text-[10px] text-emerald-600">
        本地 · 浏览 · 播放
      </span>
    </template>

    <!--
      2026-10-15 框架重做：原「曲库管家」（nas/push/download/complete/autodl 五合一，3480 行）
      按功能拆成了顶层视图 —— 订阅更新 PushManager、下载 DownloadHub、补全整理 TidyView，
      本组件只剩「本地曲库」这一件事：给 NasExplorer 一个 PageShell 外壳。
      NasExplorer 组件内部零改动（它自己那层 PageShell 早在 v2.1.120 就摘了），
      `play` 事件原样上转给 App.vue —— 播放器在顶层，本页不持有音频状态。
    -->
    <NasExplorer @play="song => emit('play', song)" />
  </PageShell>
</template>

<script setup>
import PageShell from './PageShell.vue'
import NasExplorer from './NasExplorer.vue'

const emit = defineEmits(['play'])
</script>
