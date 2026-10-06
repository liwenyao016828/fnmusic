<template>
  <!--
    平台扫码登录弹窗（网易云 / QQ 音乐）。
    2026-09-27：从「账号连接」页搬到「发现音乐 → 双平台推荐」时抽成组件 ——
    弹窗（Teleport + 遮罩 + 状态行 + 刷新按钮）是一整块纯展示，逻辑全在
    `services/qrLogin.js` 与调用方，组件本身只认 `session` 和两个事件。
  -->
  <Teleport to="body">
    <div
      v-if="session"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4"
      @click.self="$emit('close')"
    >
      <div class="w-full max-w-[360px] overflow-hidden rounded-2xl bg-white shadow-2xl">
        <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3">
          <h3 class="text-sm font-semibold text-gray-800">{{ session.platformName }} · 扫码登录</h3>
          <button class="text-gray-400 hover:text-gray-600" title="关闭" @click="$emit('close')">✕</button>
        </div>
        <div class="flex flex-col items-center px-5 py-6">
          <div class="grid h-[200px] w-[200px] place-items-center rounded-xl border border-gray-200 bg-white p-2">
            <img v-if="session.image" :src="session.image" class="h-full w-full object-contain" alt="登录二维码" />
            <span v-else class="text-xs text-gray-400">{{ QR_TEXT.rendering }}</span>
          </div>
          <p class="mt-4 text-center text-[13px] font-medium" :class="session.ok ? 'text-emerald-600' : 'text-gray-600'">
            {{ session.status }}
          </p>
          <p class="mt-1 text-center text-[11px] text-gray-400">
            用「{{ session.appName }}」App 扫描上方二维码
          </p>
          <button
            v-if="session.expired"
            class="mt-3.5 rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white hover:bg-emerald-600"
            @click="$emit('restart')"
          >
            刷新二维码
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { QR_TEXT } from '../services/qrLogin'

defineProps({
  /** `services/qrLogin.js` 的 qrSessionView(...)，null = 不显示弹窗 */
  session: { type: Object, default: null },
})
defineEmits(['close', 'restart'])
</script>
