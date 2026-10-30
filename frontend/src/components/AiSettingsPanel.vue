<template>
  <!--
    AI 大模型设置（v2.1.31 从「曲库管家 → AI 设置」搬来）。
    放在「账号连接」是因为它本质就是**又一个外部服务凭证**（Base URL + API Key + 模型），
    和飞牛音乐、网易云/QQ 扫码是同一类东西；曲库管家那侧只留曲库操作。

    ⚠️ **默认折叠**：这块有两个小节（配置 / 用量），全展开约 100 行，会把这页的主任务
    「扫码登录」挤出首屏（用户 2026-09-24 选定：扫码卡之后 + 默认折叠）。
    折叠条上必须自带状态徽标与一句摘要 —— 收起不等于看不出没没配好。

    2026-10-26 起这里只剩「服务凭证 + 用量」两件事：文件名试算与规则生成是
    「改歌名」的工具，已搬到「补全整理 → 歌名修复」（NameRulesPanel.vue）。
  -->
  <div class="mt-5 overflow-hidden rounded-xl border border-[var(--ys-border)] bg-white">
    <button
      class="flex w-full items-center gap-2.5 px-4 py-3.5 text-left transition-colors hover:bg-[var(--ys-surface-hover)]"
      @click="toggleOpen"
    >
      <Sparkles class="h-4 w-4 shrink-0 text-emerald-600" />
      <div class="min-w-0 flex-1">
        <b class="block truncate text-[13px] font-semibold text-gray-800">AI 大模型（OpenAI 兼容接口）</b>
        <span class="mt-0.5 block truncate text-[11px] text-gray-500">{{ summaryLine }}</span>
      </div>
      <span :class="[
        'shrink-0 rounded-full border px-2 py-0.5 text-[10px]',
        aiConfig.available ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] text-gray-500',
      ]">{{ aiConfig.available ? '已就绪' : '未启用' }}</span>
      <ChevronDown
        class="h-4 w-4 shrink-0 text-gray-500 transition-transform" :class="open ? 'rotate-180' : ''"
      />
    </button>

    <div v-if="open" class="space-y-3 border-t border-[var(--ys-border)] p-4">
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label class="text-[11px] text-gray-500 md:col-span-2">
          Base URL
          <input v-model="aiForm.base_url" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-[var(--ys-line-control)] text-xs font-mono"
            placeholder="https://api.deepseek.com 或 https://ark.cn-beijing.volces.com/api/v3" />
        </label>
        <label class="text-[11px] text-gray-500">
          API Key
          <input v-model="aiForm.api_key" type="password" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-[var(--ys-line-control)] text-xs font-mono"
            :placeholder="aiConfig.configured ? '已配置（留空表示不修改）' : 'sk-...'" />
        </label>
        <label class="text-[11px] text-gray-500">
          模型
          <input v-model="aiForm.model" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-[var(--ys-line-control)] text-xs font-mono"
            placeholder="deepseek-chat" />
        </label>
        <label class="text-[11px] text-gray-500">
          超时（秒）
          <input v-model.number="aiForm.timeout" type="number" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-[var(--ys-line-control)] text-xs" />
        </label>
        <div class="flex items-end gap-2">
          <label class="flex items-center gap-1.5 text-[11px] text-gray-600">
            <input type="checkbox" v-model="aiForm.enabled" class="" />启用
          </label>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <button @click="saveAI" :disabled="savingAI"
          class="px-4 py-1.5 rounded-lg bg-emerald-600 hover:bg-emerald-700 text-white text-xs font-semibold disabled:opacity-50">
          {{ savingAI ? '保存中...' : '保存配置' }}
        </button>
        <button @click="testAI" :disabled="testingAI"
          class="px-3 py-1.5 rounded-lg bg-[var(--ys-surface-control)] hover:bg-[var(--ys-surface-hover)] text-xs text-gray-600 disabled:opacity-50">
          {{ testingAI ? '测试中...' : '连通性测试' }}
        </button>
        <span v-if="aiTestMsg" class="text-[11px]" :class="aiTestOk ? 'text-emerald-600' : 'text-rose-500'">{{ aiTestMsg }}</span>
      </div>

      <!-- 用量 -->
      <div class="pt-3 border-t border-gray-50">
        <div class="flex items-center justify-between mb-2">
          <span class="text-xs font-semibold text-gray-700">用量统计</span>
          <button @click="resetUsage" class="text-[11px] text-gray-500 hover:text-rose-500">清空</button>
        </div>
        <div class="grid grid-cols-2 md:grid-cols-4 gap-2">
          <div class="p-2.5 rounded-lg bg-[var(--ys-surface-sunken)] border border-[var(--ys-border)]">
            <div class="text-[10px] text-gray-500">调用次数</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.total_calls || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-[var(--ys-surface-sunken)] border border-[var(--ys-border)]">
            <div class="text-[10px] text-gray-500">总 tokens</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.total_tokens || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-[var(--ys-surface-sunken)] border border-[var(--ys-border)]">
            <div class="text-[10px] text-gray-500">输入 tokens</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.prompt_tokens || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-[var(--ys-surface-sunken)] border border-[var(--ys-border)]">
            <div class="text-[10px] text-gray-500">无明细调用</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.missing_usage || 0 }}</div>
          </div>
        </div>
        <div v-if="aiUsage.by_kind && Object.keys(aiUsage.by_kind).length" class="mt-2 space-y-0.5">
          <div v-for="(v, k) in aiUsage.by_kind" :key="k" class="flex items-center justify-between text-[11px] text-gray-500">
            <span class="font-mono">{{ k }}</span>
            <span>{{ v.calls }} 次 · {{ v.total }} tokens</span>
          </div>
        </div>
        <p v-else class="mt-2 text-[11px] text-gray-500">暂无调用记录</p>
      </div>

      <!-- 2026-10-26 用户要求：文件名语义解析与命名解析规则已迁移到「补全整理 → 歌名修复」，见 NameRulesPanel.vue -->
      <!-- 这个面板自己的一句话反馈（原来靠宿主页的 flash 槽） -->
      <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Sparkles, ChevronDown } from 'lucide-vue-next'
import { AIAPI } from '../api/client'
import InlineNotice from './InlineNotice.vue'
import { fail, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'
import { loadBool, savePref } from '../services/prefs'

const aiConfig = ref({})
const aiUsage = ref({})
const aiForm = ref({ enabled: false, base_url: '', api_key: '', model: '', timeout: 20 })
const savingAI = ref(false)
const testingAI = ref(false)
const aiTestMsg = ref('')
const aiTestOk = ref(false)

// 展开状态记在窗口记忆里：进来一会儿就又被折叠会很难受（改配置要反复展开）。
const open = ref(loadBool('ai_panel_open', false))

function toggleOpen() {
  open.value = !open.value
  savePref('ai_panel_open', open.value ? '1' : '0')
}

/**
 * 折叠时那行摘要：得让人不展开也知道「配没配、给谁用了」。
 * 用量为 0 时说未启用，比写一堆 0 有用。
 */
const summaryLine = computed(() => {
  const calls = aiUsage.value.total_calls || 0
  if (!aiConfig.value.configured && !aiConfig.value.enabled) {
    return '填服务地址 + API Key 后，风格兜底与疑难命名解析才会用到它'
  }
  const model = aiForm.value.model || aiConfig.value.model || '默认模型'
  return calls > 0
    ? `模型 ${model} · 累计 ${calls} 次调用 · ${aiUsage.value.total_tokens || 0} tokens`
    : `模型 ${model} · 还没调用过`
})

const notice = ref('')
const noticeOk = ref(false)
let noticeTimer = null

function flash(msg, isError = false) {
  notice.value = msg || ''
  noticeOk.value = !isError
  if (noticeTimer) clearTimeout(noticeTimer)
  if (notice.value) noticeTimer = setTimeout(() => { notice.value = '' }, 6000)
}

onMounted(() => {
  loadAI()
})

async function loadAI() {
  try {
    const [cfg, usage] = await Promise.all([AIAPI.getConfig(), AIAPI.usage(10)])
    if (cfg.code === 200) {
      aiConfig.value = cfg.data
      aiForm.value = {
        enabled: cfg.data.enabled,
        base_url: cfg.data.base_url || '',
        api_key: '',   // 留空表示不修改
        model: cfg.data.model || '',
        timeout: cfg.data.timeout || 20,
      }
    }
    if (usage.code === 200) aiUsage.value = usage.data
  } catch (e) {
    console.warn('loadAI failed', e)
  }
}

async function saveAI() {
  savingAI.value = true
  try {
    const res = await AIAPI.saveConfig(aiForm.value)
    if (res.code === 200) {
      flash('AI 配置已保存')
      await loadAI()
    } else {
      flash(apiError(res, '保存失败'), true)
    }
  } catch (e) {
    flash(fail('保存 AI 配置', e), true)
  } finally {
    savingAI.value = false
  }
}

async function testAI() {
  testingAI.value = true
  aiTestMsg.value = ''
  try {
    const res = await AIAPI.test()
    aiTestOk.value = res.code === 200
    if (res.code === 200) {
      aiTestMsg.value = `正常：${res.data?.reply || 'ok'}`
    } else {
      aiTestMsg.value = apiError(res, '连接失败')
      logError('测试 AI 连接', aiTestMsg.value, `code=${res.code} message=${res.message}`)
    }
  } catch (e) {
    aiTestOk.value = false
    aiTestMsg.value = fail('测试 AI 连接', e, '连接失败')
  } finally {
    testingAI.value = false
  }
}

async function resetUsage() {
  if (!confirm('确定清空用量统计？')) return
  try {
    await AIAPI.resetUsage()
    await loadAI()
  } catch (e) {
    flash(fail('清空用量统计', e), true)
  }
}
</script>
