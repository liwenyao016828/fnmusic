<template>
  <PageShell
    title="账号连接"
    desc="飞牛音乐与 AI 大模型的连接凭据都放在这里，只保存在本机（权限 0600），不会上传到任何第三方。"
  >
    <!--
      平台账号（网易云 / QQ 音乐）的扫码登录**已合并到「发现音乐 → 双平台推荐」**。
      2026-09-27 用户反馈：这一页的「两个平台扫码登录」和发现页的扫码登录是同一个功能，
      重复了，要求去掉这里的、换到发现页（连登录后的登出/注销按钮一起）。
      这里留一句指引，免得用户以为功能没了。
    -->
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-[15px] border border-emerald-100 bg-emerald-50/60 px-4 py-3">
      <div class="min-w-0">
        <b class="block text-[13px] text-emerald-800">平台账号扫码登录已移到「发现音乐」</b>
        <span class="mt-0.5 block text-[12px] leading-relaxed text-emerald-900/70">
          网易云音乐、QQ 音乐的扫码登录、登出与个人推荐现在都在「发现音乐 → 双平台推荐」里，登录完直接就能听。
        </span>
      </div>
      <button
        class="shrink-0 rounded-lg bg-emerald-500 px-3.5 py-1.5 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600"
        @click="goDiscover"
      >去扫码登录</button>
    </div>

    <!-- ── 飞牛音乐连接（与第三方账号并列，都是"外部连接"） ── -->
    <div class="mb-4">
      <FnosSyncPanel />
    </div>

    <!-- AI 大模型 = 又一个外部服务凭证，从「曲库管家 → AI 设置」搬来（默认折叠，见该组件注释） -->
    <AiSettingsPanel />

    <!-- ── 说明 ── -->
    <div class="mt-5 rounded-xl border border-blue-100 bg-blue-50/60 px-4 py-3 text-[12px] leading-relaxed text-blue-900/80">
      <b class="text-blue-700">接下来做什么</b>
      ：飞牛音乐连上后，到「曲库管家 → 推送」就能把发现页里拉到的每日推荐或私人歌单同步到飞牛音乐。
    </div>
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
