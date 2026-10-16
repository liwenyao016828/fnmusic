<template>
  <PageShell>
    <!--
      平台账号（网易云 / QQ 音乐）的扫码登录**已合并到「发现音乐 → 双平台推荐」**。
      2026-09-27 用户反馈：这一页的「两个平台扫码登录」和发现页的扫码登录是同一个功能，
      重复了，要求去掉这里的、换到发现页（连登录后的登出/注销按钮一起）。
      这里留一句指引，免得用户以为功能没了。
    -->
    <!--
      平台账号入口改到发现页后，这里只留一行指路（2026-10-14 用户：「提示太多，看起来很挤」——
      原来是两张卡：绿横幅 + 蓝说明。本页的主任务是下面两个连接面板，指引压到一行灰字）。
    -->
    <p class="mb-3 text-[11.5px] text-gray-400">
      网易云 / QQ 音乐的扫码登录在「发现音乐 → 双平台推荐」
      <button class="ml-1 font-semibold text-emerald-600 underline decoration-dotted underline-offset-2 hover:text-emerald-700" @click="goDiscover">前往</button>
    </p>

    <!-- ── 飞牛音乐连接（与第三方账号并列，都是"外部连接"） ── -->
    <div class="mb-4">
      <FnosSyncPanel />
    </div>

    <!-- AI 大模型 = 又一个外部服务凭证，从「曲库管家 → AI 设置」搬来（默认折叠，见该组件注释） -->
    <AiSettingsPanel />

    <!--
      （2026-10-14）删掉了原来底部的蓝色「接下来做什么」卡：连接成功后 FnosSyncPanel
      的状态横幅自己会变绿，推送页也有自己的引导 —— 三处说同一句话只剩噪音。
    -->
  </PageShell>
</template>

<script setup>
import { onMounted } from 'vue'
import { markLoaded } from '../services/viewCache'

// 这一页现在只放「外部服务凭据」：飞牛音乐连接 + AI 大模型。
//
// 曾经这里还有网易云 / QQ 音乐的扫码登录卡片、二维码弹窗、日推与歌单拉取结果面板
// ——2026-09-27 全部搬到「发现音乐 → 双平台推荐」（用户要求：两处重复，只留一处）。
// 因此本组件不再需要 AccountAPI / QRCode / 轮询定时器那些东西。
import PageShell from './PageShell.vue'
import FnosSyncPanel from './FnosSyncPanel.vue'
import AiSettingsPanel from './AiSettingsPanel.vue'
import { navigateTo } from '../services/navBus'

/** 跳到「发现音乐」——扫码登录现在在那边（见模板里的说明块） */
function goDiscover() {
  navigateTo('search')
}
// 常驻页面：进过一次就登记新鲜度（账号信息基本不变，TTL 5 分钟）
onMounted(() => markLoaded('accounts'))

</script>
