<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-200 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="modelValue"
        class="fixed inset-0 z-[99999] bg-black/75 backdrop-blur-sm flex items-center justify-center p-3 sm:p-6"
        style="z-index: 99999;"
        @click="onBackdropClick"
      >
        <div
          @click.stop
          class="w-full max-w-lg bg-white rounded-2xl shadow-2xl border border-[var(--ys-border)] overflow-hidden flex flex-col max-h-[86vh] my-auto animate-in fade-in zoom-in-95 duration-200 relative z-10"
        >
          <!-- 标题栏 (固定在顶部) -->
          <div class="px-5 py-3.5 border-b border-[var(--ys-border)] flex items-center justify-between bg-gradient-to-r from-amber-50/70 to-orange-50/70 shrink-0">
            <div class="flex items-center gap-2.5">
              <div class="w-8 h-8 rounded-xl bg-amber-500 text-white flex items-center justify-center">
                <ShieldAlert class="w-4 h-4" />
              </div>
              <div>
                <h3 class="text-sm font-bold text-gray-800">
                  免责声明与服务条款
                </h3>
                <p class="text-[11px] text-gray-500">
                  曲率 · 纯净开源容器合规声明
                </p>
              </div>
            </div>

            <!-- 如果非强制首次确认，允许直接右上角关闭 -->
            <button
              v-if="!isForce"
              @click="close"
              class="p-1.5 rounded-xl text-gray-500 hover:text-gray-600 hover:bg-[var(--ys-surface-hover)] transition-colors"
            >
              <X class="w-4 h-4" />
            </button>
          </div>

          <!-- 正文条款区域 (自适应滚动) -->
          <div class="p-5 flex-1 min-h-0 overflow-y-auto space-y-3.5 text-xs leading-relaxed text-gray-600">
            <div class="p-3 rounded-xl bg-amber-50/80 border border-amber-200/80 text-amber-900 text-[11px] flex items-start gap-2">
              <AlertTriangle class="w-4 h-4 text-amber-600 shrink-0 mt-0.5" />
              <div>
                <strong>重要提醒：</strong>本软件提供音频管理、检索、播放与下载功能，安装包不附带音乐内容或第三方音源脚本。使用前请确认相关内容和音源的授权，并阅读以下说明。
              </div>
            </div>

            <div class="space-y-3">
              <div>
                <h4 class="font-bold text-gray-800 text-xs flex items-center gap-1.5 mb-1">
                  <span>1. 技术中立与容器定位</span>
                </h4>
                <p class="text-[11px] text-gray-500 text-justify">
                  本软件运行于个人私有云环境，提供本地曲库管理、网络曲库检索、播放及下载。下载任务由后端持久化和执行，前端负责提交、显示状态和控制；运行时可按用户操作将音频、歌词和封面保存到用户设备。
                </p>
              </div>

              <div>
                <h4 class="font-bold text-gray-800 text-xs flex items-center gap-1.5 mb-1">
                  <span>2. 第三方自定义音源脚本责任</span>
                </h4>
                <p class="text-[11px] text-gray-500 text-justify">
                  脚本由用户自行导入；下载使用可用的服务端渠道，LX 脚本需要启用对应服务端宿主。用户应核实脚本来源、使用条款及内容授权。脚本由用户导入、代码免费开源或在服务端运行，均不当然排除相关主体依法应承担的责任。
                </p>
              </div>

              <div>
                <h4 class="font-bold text-gray-800 text-xs flex items-center gap-1.5 mb-1">
                  <span>3. 内容授权与使用范围</span>
                </h4>
                <p class="text-[11px] text-gray-500 text-justify">
                  请仅获取和使用已获许可的内容，遵守适用法律与平台服务条款，不得未经许可复制、传播或绕过访问限制。个人学习或家庭使用并不自动获得音乐内容授权；本项目代码的 MIT 许可证也不授予音乐、商标或第三方服务的使用权。
                </p>
              </div>

              <div>
                <h4 class="font-bold text-gray-800 text-xs flex items-center gap-1.5 mb-1">
                  <span>4. 知识产权保护与下架机制</span>
                </h4>
                <p class="text-[11px] text-gray-500 text-justify">
                  本软件高度尊重各大内容平台及版权方的知识产权。若任何权利方认为本开源技术项目的代码实现涉及侵权争议，请通过 GitHub 仓库提交合法有效的权利通知，开发者核实后将在第一时间积极响应并配合处理。
                </p>
              </div>

              <div>
                <h4 class="font-bold text-gray-800 text-xs flex items-center gap-1.5 mb-1">
                  <span>5. 免责范围</span>
                </h4>
                <p class="text-[11px] text-gray-500 text-justify">
                  软件按 MIT 许可证约定提供，不保证第三方音源持续可用；使用前请备份重要数据。本说明不排除或限制适用法律规定不能免除的责任，也不替代对具体内容授权和使用行为的判断。
                </p>
              </div>
            </div>
          </div>

          <!-- 底部确认区域 -->
          <div class="px-5 py-3.5 bg-[var(--ys-surface-sunken)] border-t border-[var(--ys-border)] shrink-0">
            <!-- 「暂不同意」提示：内联显示，替代原来的 alert（alert 会盖住条款、也打断阅读） -->
            <p
              v-if="declineMsg"
              class="mb-2.5 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-[11px] text-rose-700"
            >{{ declineMsg }}</p>
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
            <label v-if="isForce" class="flex items-center gap-2 cursor-pointer select-none text-[11px] text-gray-700">
              <input
                type="checkbox"
                v-model="hasAgreed"
                class="w-4 h-4 text-emerald-600 rounded border-[var(--ys-line-control)] focus:ring-emerald-500"
              />
              <span>我已充分阅读并同意以上免责声明</span>
            </label>
            <span v-else class="text-[11px] text-gray-500 font-mono">
              协议已生效
            </span>

            <div class="flex items-center gap-2 justify-end">
              <button
                v-if="isForce"
                @click="onDecline"
                class="px-3 py-1.5 rounded-xl border border-[var(--ys-border)] hover:bg-white text-gray-500 text-xs transition-all"
              >
                暂不同意
              </button>
              <button
                @click="onAccept"
                :disabled="isForce && !hasAgreed"
                class="px-5 py-1.5 rounded-xl bg-emerald-600 hover:bg-emerald-700 text-white text-xs font-semibold shadow-sm transition-all disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {{ isForce ? '同意并继续使用' : '我知道了' }}
              </button>
            </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import { ref } from 'vue'
import { ShieldAlert, AlertTriangle, X } from 'lucide-vue-next'
import { savePref } from '../services/prefs'

/** 「暂不同意」的内联提示 */
const declineMsg = ref('')

const props = defineProps({
  modelValue: Boolean,
  isForce: {
    type: Boolean,
    default: false,
  }
})

const emit = defineEmits(['update:modelValue', 'accepted'])

const hasAgreed = ref(false)

function close() {
  emit('update:modelValue', false)
}

function onBackdropClick() {
  if (!props.isForce) {
    close()
  }
}

function onAccept() {
  // 走「窗口记忆」而不是直接写 localStorage：它由 prefs 服务同步到应用数据目录，
  // 换端口 / 换浏览器 / 清缓存都还在。直接写 localStorage 时，
  // 用 IP 和用域名访问是两个不同的 origin，会出现「明明同意过又弹」。
  savePref('disclaimer_accepted', '1')
  declineMsg.value = ''
  emit('accepted')
  close()
}

function onDecline() {
  declineMsg.value = '需要同意本免责声明与服务条款后才能使用曲率 的各项功能。'
}
</script>
