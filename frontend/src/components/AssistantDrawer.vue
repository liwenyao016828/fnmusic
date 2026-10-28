<template>
  <div>
    <!--
      AI 助手抽屉：**从右边缘滑入、向左展开**（与下载抽屉同侧）。

      2026-10-26 新增；2026-10-27 大修：
        - 修透明背景（--ys-bg 变量不存在 → 用 --ys-surface）
        - 会话记录：会话落盘、列表可切、可删、可新建（新的在最上面）
        - 顶部对话模型下拉：从网关 /v1/models 拉真实列表，切了立即保存
        - 修「设置点了没反应」：信封解析收敛到 client.js，这里不再 res.data||res 猜结构
        - 输入区提示区分「没配 Key」与「配了没启用」，并指路到 账号连接 → AI 大模型

      安全模型（重要）：
        - 只读能力（Auto）：模型可直接调用，不弹确认
        - 写入能力（Confirm）：模型发起后先存为待确认操作，界面必须点「确认执行」
        - 全手动（ReadOnly）：模型只能查，不能调任何写入能力
      后端策略裁决（policy.Decide）是权威，前端只是把它翻译成 UI。
    -->
    <Teleport to="body">
      <!-- 遮罩 -->
      <Transition
        enter-active-class="transition-opacity duration-300 ease-out"
        enter-from-class="opacity-0"
        enter-to-class="opacity-100"
        leave-active-class="transition-opacity duration-200 ease-in"
        leave-from-class="opacity-100"
        leave-to-class="opacity-0"
      >
        <div
          v-if="modelValue"
          @click="close"
          class="fixed inset-0 z-[60] bg-black/40 backdrop-blur-xs"
        ></div>
      </Transition>

      <!-- 抽屉主体（之前用的 --ys-bg 变量根本不存在，导致整层透明） -->
      <Transition
        enter-active-class="transition-transform duration-300 ease-out"
        enter-from-class="translate-x-full"
        enter-to-class="translate-x-0"
        leave-active-class="transition-transform duration-250 ease-in"
        leave-from-class="translate-x-0"
        leave-to-class="translate-x-full"
      >
        <div
          v-if="modelValue"
          class="fixed inset-y-0 right-0 z-[60] w-full sm:w-[560px] pt-safe pb-safe bg-[var(--ys-surface)] shadow-2xl flex flex-col overflow-hidden border-l border-gray-100"
        >
          <!-- 顶部标题栏 -->
          <div class="px-5 py-4 border-b border-gray-100 flex items-center justify-between bg-white shrink-0">
            <div class="flex items-center gap-2.5 min-w-0">
              <div class="w-8 h-8 rounded-xl bg-emerald-50 text-emerald-600 flex items-center justify-center shadow-2xs shrink-0">
                <Sparkles class="w-4 h-4" />
              </div>
              <div class="min-w-0">
                <h3 class="text-sm font-bold text-gray-800">AI 助手</h3>
                <p class="text-[11px] text-gray-400 truncate">
                  {{ settings.enabled ? `${modeLabel} · 模型 ${currentModel || '未设置'}` : '已关闭（右上设置里开启）' }}
                </p>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <button
                @click="toggleSessions"
                :class="[
                  'p-1.5 rounded-xl transition-colors',
                  showSessions ? 'text-emerald-600 bg-emerald-50' : 'text-gray-400 hover:text-emerald-600 hover:bg-emerald-50'
                ]"
                title="会话记录"
              >
                <History class="w-4 h-4" />
              </button>
              <button
                @click="createSession"
                class="p-1.5 rounded-xl text-gray-400 hover:text-emerald-600 hover:bg-emerald-50 transition-colors"
                title="新建会话"
              >
                <Plus class="w-4 h-4" />
              </button>
              <button
                v-if="!showSettings"
                @click="showSettings = true"
                class="p-1.5 rounded-xl text-gray-400 hover:text-emerald-600 hover:bg-emerald-50 transition-colors"
                title="助手设置"
              >
                <Settings2 class="w-4 h-4" />
              </button>
              <button
                v-else
                @click="showSettings = false"
                class="p-1.5 rounded-xl text-emerald-600 bg-emerald-50 transition-colors"
                title="收起设置"
              >
                <ChevronDown class="w-4 h-4" />
              </button>
              <button
                @click="close"
                class="p-1.5 rounded-xl text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors"
                title="关闭"
              >
                <X class="w-4 h-4" />
              </button>
            </div>
          </div>

          <!-- 会话记录面板（折叠） -->
          <Transition
            enter-active-class="transition-all duration-200 ease-out"
            enter-from-class="max-h-0 opacity-0"
            enter-to-class="max-h-[50vh] opacity-100"
            leave-active-class="transition-all duration-150 ease-in"
            leave-from-class="max-h-[50vh] opacity-100"
            leave-to-class="max-h-0 opacity-0"
          >
            <div v-if="showSessions" class="border-b border-gray-100 bg-gray-50/50 shrink-0 overflow-hidden">
              <div class="max-h-[45vh] overflow-y-auto px-3 py-2 space-y-1">
                <p v-if="!sessions.length" class="text-center text-[11px] text-gray-400 py-4">
                  还没有历史会话，发一条消息就有了
                </p>
                <div
                  v-for="s in sessions"
                  :key="s.id"
                  @click="switchSession(s.id)"
                  :class="[
                    'flex items-center gap-2 px-3 py-2 rounded-xl bg-white border cursor-pointer transition-colors',
                    s.id === currentSessionId
                      ? 'border-emerald-400 ring-1 ring-emerald-100'
                      : 'border-gray-100 hover:border-emerald-300'
                  ]"
                >
                  <MessageSquare class="w-3.5 h-3.5 text-gray-300 shrink-0" />
                  <div class="min-w-0 flex-1">
                    <div class="text-xs font-medium text-gray-700 truncate">{{ s.title || '新会话' }}</div>
                    <div class="text-[10px] text-gray-400">{{ s.updated_at }} · {{ s.messages }} 条</div>
                  </div>
                  <button
                    @click.stop="deleteSession(s.id)"
                    class="p-1 rounded-lg text-gray-300 hover:text-red-500 hover:bg-red-50 transition-colors shrink-0"
                    title="删除这个会话"
                  >
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            </div>
          </Transition>

          <!-- 设置面板（折叠） -->
          <Transition
            enter-active-class="transition-all duration-200 ease-out"
            enter-from-class="max-h-0 opacity-0"
            enter-to-class="max-h-[60vh] opacity-100"
            leave-active-class="transition-all duration-150 ease-in"
            leave-from-class="max-h-[60vh] opacity-100"
            leave-to-class="max-h-0 opacity-0"
          >
            <div v-if="showSettings" class="border-b border-gray-100 bg-gray-50/50 shrink-0 overflow-hidden">
              <div class="max-h-[55vh] overflow-y-auto">
                <!-- 对话模型 -->
                <div class="px-5 py-3 border-b border-gray-100/60">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">对话模型</label>
                  <div class="mt-1.5 flex items-center gap-2">
                    <select
                      :value="currentModel"
                      @change="saveModel($event.target.value)"
                      :disabled="aiState !== 'ready'"
                      class="flex-1 min-w-0 px-2.5 py-1.5 rounded-xl border border-gray-200 bg-white text-xs text-gray-700 focus:outline-none focus:border-emerald-300 focus:ring-2 focus:ring-emerald-100 transition-colors disabled:opacity-50"
                    >
                      <option v-if="!modelOptions.length" value="">（暂无可用模型）</option>
                      <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
                    </select>
                  </div>
                  <p v-if="modelSaveMsg" class="text-[10px] text-emerald-600 mt-1">{{ modelSaveMsg }}</p>
                  <p v-else-if="modelError" class="text-[10px] text-amber-500 mt-1">拉取模型列表失败：{{ modelError }}</p>
                  <p v-else-if="aiState === 'unconfigured'" class="text-[10px] text-amber-500 mt-1 leading-relaxed">
                    还没配置 AI：到「账号连接 → AI 大模型」填 Base URL 与 API Key 并启用
                  </p>
                  <p v-else-if="aiState === 'disabled'" class="text-[10px] text-amber-500 mt-1 leading-relaxed">
                    AI 已配置但没启用：到「账号连接 → AI 大模型」勾选「启用」并保存
                  </p>
                  <p v-else class="text-[10px] text-gray-400 mt-1 leading-relaxed">
                    列表来自网关 /v1/models；切换立即保存。改 Base URL / Key 去「账号连接 → AI 大模型」。
                  </p>
                </div>

                <!-- 执行模式 -->
                <div class="px-5 py-3">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">执行模式</label>
                  <div class="mt-1.5 flex gap-2">
                    <button
                      v-for="m in modeOptions"
                      :key="m.value"
                      @click="updateSettings({ mode: m.value })"
                      :class="[
                        'flex-1 px-3 py-1.5 rounded-xl text-xs font-medium transition-colors',
                        settings.mode === m.value
                          ? 'bg-emerald-500 text-white shadow-sm'
                          : 'bg-white text-gray-500 hover:bg-gray-100 border border-gray-200'
                      ]"
                    >
                      {{ m.label }}
                    </button>
                  </div>
                  <p class="text-[10px] text-gray-400 mt-1 leading-relaxed">{{ modeDesc }}</p>
                </div>

                <!-- 总开关 -->
                <div class="px-5 py-2 flex items-center justify-between">
                  <div>
                    <span class="text-xs font-medium text-gray-600">启用助手</span>
                    <p class="text-[10px] text-gray-400">关闭后模型不会调用任何能力</p>
                  </div>
                  <button
                    @click="updateSettings({ enabled: !settings.enabled })"
                    :class="[
                      'relative w-11 h-6 rounded-full transition-colors',
                      settings.enabled ? 'bg-emerald-500' : 'bg-gray-300'
                    ]"
                  >
                    <span
                      :class="[
                        'absolute top-0.5 w-5 h-5 rounded-full bg-white shadow-sm transition-transform',
                        settings.enabled ? 'translate-x-[1.4rem]' : 'translate-x-0.5'
                      ]"
                    ></span>
                  </button>
                </div>

                <!-- 能力开关 -->
                <div class="px-5 py-3">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">能力开关</label>
                  <p class="text-[10px] text-gray-400 mt-0.5 mb-2">关闭的能力模型不会调用（写入类操作仍需确认）</p>
                  <div class="space-y-1.5">
                    <label
                      v-for="cap in capabilities"
                      :key="cap.name"
                      class="flex items-center justify-between px-3 py-2 rounded-xl bg-white border border-gray-100 cursor-pointer hover:bg-gray-50 transition-colors"
                    >
                      <div class="min-w-0">
                        <div class="text-xs font-medium text-gray-700 truncate">{{ cap.summary || cap.name }}</div>
                        <div class="text-[10px] text-gray-400 truncate font-mono">{{ cap.name }}</div>
                      </div>
                      <!-- 注意：读的是 settings.disabled（之前这里读了个不存在的 disabled，整个子树直接渲染报错） -->
                      <button
                        @click.prevent="toggleCapability(cap.name)"
                        :class="[
                          'relative w-9 h-5 rounded-full transition-colors shrink-0',
                          settings.disabled[cap.name] ? 'bg-gray-300' : 'bg-emerald-500'
                        ]"
                      >
                        <span
                          :class="[
                            'absolute top-0.5 w-4 h-4 rounded-full bg-white shadow-sm transition-transform',
                            settings.disabled[cap.name] ? 'translate-x-[1rem]' : 'translate-x-0.5'
                          ]"
                        ></span>
                      </button>
                    </label>
                  </div>
                </div>

                <!-- 保存失败提示（之前静默吞掉，点了像没反应） -->
                <p v-if="saveError" class="px-5 pb-3 text-[10px] text-red-500">{{ saveError }}</p>
              </div>
            </div>
          </Transition>

          <!-- 消息流 -->
          <div ref="msgContainer" class="flex-1 min-h-0 overflow-y-auto px-5 py-4 space-y-4">
            <!-- 空状态 -->
            <div v-if="messages.length === 0" class="flex flex-col items-center justify-center h-full text-center gap-4">
              <div class="w-16 h-16 rounded-2xl bg-gradient-to-br from-emerald-50 to-emerald-100 flex items-center justify-center shadow-sm">
                <Sparkles class="w-8 h-8 text-emerald-600" />
              </div>
              <div>
                <h4 class="text-base font-bold text-gray-700">曲率 AI 助手</h4>
                <p class="text-xs text-gray-400 mt-1 leading-relaxed">
                  可以帮你管理曲库、查看状态、开关功能。<br>
                  写操作会先确认再执行。
                </p>
              </div>
              <div class="flex flex-wrap gap-2 justify-center max-w-full">
                <button
                  v-for="q in suggestions"
                  :key="q"
                  @click="sendMessage(q)"
                  class="px-3 py-1.5 rounded-full text-xs font-medium bg-white border border-gray-200 text-gray-600 hover:border-emerald-300 hover:text-emerald-600 transition-colors"
                >
                  {{ q }}
                </button>
              </div>
            </div>

            <!-- 消息列表 -->
            <template v-for="(msg, i) in messages" :key="i">
              <!-- 用户消息 -->
              <div v-if="msg.role === 'user'" class="flex justify-end">
                <div class="max-w-[80%] px-3.5 py-2 rounded-2xl rounded-br-md bg-emerald-500 text-white text-sm leading-relaxed whitespace-pre-wrap shadow-sm">
                  {{ msg.content }}
                </div>
              </div>

              <!-- 助手消息（含工具调用） -->
              <div v-else-if="msg.role === 'assistant'" class="flex flex-col gap-2">
                <!-- 工具调用卡片 -->
                <div
                  v-for="(tc, ti) in msg.toolCalls || []"
                  :key="ti"
                  class="rounded-xl border border-gray-100 bg-gray-50/80 px-3 py-2.5"
                >
                  <div class="flex items-center gap-1.5 mb-1">
                    <Wrench class="w-3.5 h-3.5 text-gray-400 shrink-0" />
                    <span class="text-[11px] font-semibold text-gray-500 font-mono">{{ tc.name }}</span>
                    <span
                      v-if="tc.status === 'confirming'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-amber-100 text-amber-700 text-[10px] font-semibold"
                    >待确认</span>
                    <span
                      v-else-if="tc.status === 'pending'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-sky-100 text-sky-700 text-[10px] font-semibold"
                    >执行中</span>
                    <span
                      v-else-if="tc.status === 'done'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-emerald-100 text-emerald-700 text-[10px] font-semibold"
                    >完成</span>
                    <span
                      v-else-if="tc.status === 'rejected'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-red-100 text-red-700 text-[10px] font-semibold"
                    >已拒绝</span>
                    <span
                      v-else-if="tc.status === 'failed'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-red-100 text-red-700 text-[10px] font-semibold"
                    >失败</span>
                  </div>
                  <!-- 工具参数 -->
                  <pre v-if="tc.args" class="text-[10px] text-gray-500 font-mono whitespace-pre-wrap break-all bg-white/60 rounded-lg p-2 mb-1.5">{{ formatArgs(tc.args) }}</pre>
                  <!-- 工具结果 -->
                  <pre v-if="tc.result" class="text-[10px] text-gray-600 font-mono whitespace-pre-wrap break-all bg-white rounded-lg p-2 border border-gray-100">{{ truncateResult(tc.result) }}</pre>
                  <!-- 待确认：告诉用户「到底要做什么」，而不是只显示能力名 -->
                  <div
                    v-if="tc.status === 'confirming' && tc.actionId"
                    class="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2"
                  >
                    <div class="text-[11px] font-semibold text-amber-800 mb-0.5">需要你确认</div>
                    <div class="text-xs text-amber-900 leading-relaxed">{{ tc.detail || tc.summary || tc.name }}</div>
                  </div>
                  <!-- 待确认操作按钮 -->
                  <div v-if="tc.status === 'confirming' && tc.actionId" class="flex gap-2 mt-2">
                    <button
                      @click="confirmAction(tc.actionId, true)"
                      :disabled="streaming"
                      class="flex-1 px-3 py-1.5 rounded-lg text-xs font-semibold bg-emerald-500 text-white hover:bg-emerald-600 disabled:opacity-50 transition-colors"
                    >
                      确认执行
                    </button>
                    <button
                      @click="confirmAction(tc.actionId, false)"
                      :disabled="streaming"
                      class="flex-1 px-3 py-1.5 rounded-lg text-xs font-medium bg-white text-gray-500 border border-gray-200 hover:bg-gray-50 disabled:opacity-50 transition-colors"
                    >
                      拒绝
                    </button>
                  </div>
                </div>

                <!-- 助手正文 -->
                <div v-if="msg.content" class="max-w-[85%] px-3.5 py-2 rounded-2xl rounded-bl-md bg-white border border-gray-100 text-sm leading-relaxed text-gray-700 whitespace-pre-wrap shadow-sm">
                  {{ msg.content }}
                </div>
              </div>

              <!-- 错误消息 -->
              <div v-else-if="msg.role === 'error'" class="rounded-xl border border-red-100 bg-red-50 px-3.5 py-2.5 text-sm text-red-600">
                {{ msg.content }}
              </div>
            </template>

            <!-- 正在生成指示器 -->
            <div v-if="streaming" class="flex items-center gap-2 text-gray-400">
              <span class="inline-flex gap-0.5">
                <span class="w-1.5 h-1.5 rounded-full bg-gray-300 animate-bounce" style="animation-delay: 0ms"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-gray-300 animate-bounce" style="animation-delay: 100ms"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-gray-300 animate-bounce" style="animation-delay: 200ms"></span>
              </span>
              <span class="text-xs">助手正在思考…</span>
            </div>
          </div>

          <!-- 输入区 -->
          <div class="px-4 py-3 border-t border-gray-100 bg-white shrink-0">
            <div class="flex gap-2 items-end">
              <textarea
                ref="inputRef"
                v-model="inputText"
                @keydown.enter.exact.prevent="sendMessage()"
                placeholder="输入消息…（Enter 发送）"
                rows="1"
                class="flex-1 resize-none px-3 py-2 rounded-xl border border-gray-200 text-sm focus:outline-none focus:border-emerald-300 focus:ring-2 focus:ring-emerald-100 transition-colors max-h-32"
              ></textarea>
              <button
                @click="sendMessage()"
                :disabled="!inputText.trim() || streaming || !settings.enabled"
                class="px-4 py-2 rounded-xl bg-emerald-500 text-white text-sm font-medium disabled:opacity-40 disabled:cursor-not-allowed hover:bg-emerald-600 transition-colors shrink-0"
              >
                发送
              </button>
            </div>
            <p v-if="!settings.enabled" class="text-[10px] text-amber-500 mt-1.5 text-center">
              助手已关闭，点右上齿轮在设置里开启
            </p>
            <p v-else-if="aiState === 'unconfigured'" class="text-[10px] text-amber-500 mt-1.5 text-center">
              未配置 AI 大模型：到「账号连接 → AI 大模型」填 Base URL / API Key 并启用
            </p>
            <p v-else-if="aiState === 'disabled'" class="text-[10px] text-amber-500 mt-1.5 text-center">
              AI 已配置但未启用：到「账号连接 → AI 大模型」勾选「启用」并保存
            </p>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, reactive, computed, watch, nextTick, onMounted } from 'vue'
import { Sparkles, X, Settings2, ChevronDown, Wrench, History, Plus, Trash2, MessageSquare } from 'lucide-vue-next'
import { AssistantAPI, AIAPI } from '../api/client'

const props = defineProps({
  modelValue: Boolean,
})
const emit = defineEmits(['update:modelValue'])

const close = () => emit('update:modelValue', false)

// ── 状态 ──
const messages = ref([])
const inputText = ref('')
const streaming = ref(false)
const showSettings = ref(false)
const showSessions = ref(false)
const msgContainer = ref(null)
const inputRef = ref(null)

// AI 配置（aiCfg.available / configured / enabled 都来自后端，别再自己猜）
const aiCfg = ref(null)
const models = ref([])
const modelError = ref('')
const modelSaveMsg = ref('')
const saveError = ref('')

// 会话（后端已落盘，重启不丢）
const sessions = ref([])
const currentSessionId = ref('')

// 助手设置（从后端加载）
const settings = reactive({
  enabled: true,
  mode: 'confirm_writes',
  disabled: {},
})
const capabilities = ref([])

const modeOptions = [
  { value: 'read_only', label: '只读' },
  { value: 'confirm_writes', label: '确认写入' },
  { value: 'full_auto', label: '全自动' },
]

const modeLabel = computed(() => {
  const m = modeOptions.find(o => o.value === settings.mode)
  return m ? m.label : settings.mode
})

const modeDesc = computed(() => {
  if (settings.mode === 'read_only') return '助手只能查询信息，不会调用任何写入操作'
  if (settings.mode === 'confirm_writes') return '助手可查询和发起写入，但每次写入前都需要你确认'
  return '助手可自由调用所有能力（高风险操作仍需确认）'
})

// ready：能用；unconfigured：连 Key 都没配；disabled：配了但没启用
const aiState = computed(() => {
  if (!aiCfg.value) return 'unknown'
  if (!aiCfg.value.configured) return 'unconfigured'
  if (!aiCfg.value.enabled) return 'disabled'
  return 'ready'
})

const currentModel = computed(() => (aiCfg.value && aiCfg.value.model) || '')

// 模型下拉选项：网关列表 + 当前模型兜底（当前模型不在列表里也要能显示）
const modelOptions = computed(() => {
  const list = [...models.value]
  const cur = currentModel.value
  if (cur && !list.includes(cur)) list.unshift(cur)
  return list
})

const suggestions = [
  '曲库有多少首歌？',
  '下载队列里有什么？',
  '开启榜单注入',
  '帮我整理曲库',
]

// ── 数据加载 ──
// client.js 已把 {code, xxx} 信封拆好，这里拿到什么用什么，不再 res.data || res。
async function loadSettings() {
  try {
    const s = await AssistantAPI.getSettings()
    settings.enabled = s.enabled ?? true
    settings.mode = s.mode || 'confirm_writes'
    settings.disabled = s.disabled || {}
  } catch (e) {
    // 静默：加载失败用默认值
  }
}

async function loadCapabilities() {
  try {
    capabilities.value = await AssistantAPI.capabilities()
  } catch (e) {
    capabilities.value = []
  }
}

async function loadAIConfig() {
  try {
    const env = await AIAPI.getConfig()
    // getConfig 返回的是 {code,message,data:{...}} 信封本体
    aiCfg.value = (env && env.data) || env || null
  } catch (e) {
    aiCfg.value = null
  }
}

async function loadModels() {
  modelError.value = ''
  try {
    const res = await AIAPI.models()
    models.value = (res && res.models) || []
    if (res && res.error) modelError.value = res.error
  } catch (e) {
    modelError.value = e.message || String(e)
  }
}

// ── 助手设置 ──
async function updateSettings(patch) {
  // 先本地生效（乐观更新），失败再回滚并亮出错误——之前静默吞异常，
  // 用户点三下设置「一点反应都没有」就是这么来的
  const before = { enabled: settings.enabled, mode: settings.mode, disabled: { ...settings.disabled } }
  if ('mode' in patch) settings.mode = patch.mode
  if ('enabled' in patch) settings.enabled = !!patch.enabled
  if ('disabled' in patch) settings.disabled = { ...patch.disabled }
  saveError.value = ''
  try {
    const s = await AssistantAPI.saveSettings(patch)
    if (s) {
      settings.enabled = s.enabled ?? settings.enabled
      settings.mode = s.mode || settings.mode
      settings.disabled = s.disabled || settings.disabled
    }
  } catch (e) {
    settings.enabled = before.enabled
    settings.mode = before.mode
    settings.disabled = before.disabled
    saveError.value = `保存失败：${e.message || e}`
  }
}

function toggleCapability(name) {
  const next = { ...settings.disabled, [name]: !settings.disabled[name] }
  updateSettings({ disabled: next })
}

// ── 对话模型 ──
async function saveModel(model) {
  if (!model || !aiCfg.value) return
  modelSaveMsg.value = ''
  try {
    // UpdateConfig 的 enabled 是「显式传参」语义：必须带上当前值，
    // 否则切个模型就把整个 AI 关了；api_key 传空 = 沿用旧 Key
    const env = await AIAPI.saveConfig({
      enabled: aiCfg.value.enabled,
      base_url: aiCfg.value.base_url,
      api_key: '',
      model,
      timeout: aiCfg.value.timeout,
    })
    const cfg = (env && env.data) || env
    if (cfg) aiCfg.value = cfg
    modelSaveMsg.value = `模型已切换为 ${cfg?.model || model}`
  } catch (e) {
    modelSaveMsg.value = `保存失败：${e.message || e}`
  }
  setTimeout(() => { modelSaveMsg.value = '' }, 3000)
}

// ── 会话 ──
async function loadSessions() {
  try {
    sessions.value = await AssistantAPI.sessions()
  } catch (e) {
    sessions.value = []
  }
}

function toggleSessions() {
  showSessions.value = !showSessions.value
  if (showSessions.value) {
    showSettings.value = false
    loadSessions()
  }
}

async function createSession() {
  if (streaming.value) return
  try {
    const s = await AssistantAPI.newSession()
    currentSessionId.value = (s && s.id) || ''
  } catch (e) {
    // 新建失败时退回内存自增 id，聊天仍可用（后端 chat 空 session_id 会落 default）
    currentSessionId.value = ''
  }
  messages.value = []
  showSessions.value = false
  scrollBottom()
}

async function switchSession(id) {
  if (streaming.value || !id) return
  try {
    const sess = await AssistantAPI.history(id)
    if (!sess) return
    currentSessionId.value = sess.id || id
    messages.value = renderHistory(sess.messages || [])
  } catch (e) {
    // 历史拉不到：切过去但清屏，不打断用户
    currentSessionId.value = id
    messages.value = []
  }
  showSessions.value = false
  scrollBottom()
}

async function deleteSession(id) {
  try {
    await AssistantAPI.removeSession(id)
  } catch (e) {
    // 删除失败：刷新列表反映真实状态
  }
  await loadSessions()
  if (id === currentSessionId.value) {
    // 删的是当前会话：跳到最新的一个；没有就新建
    if (sessions.value.length > 0) {
      await switchSession(sessions.value[0].id)
    } else {
      await createSession()
    }
  }
}

// 把后端历史消息（含 tool_calls / tool 结果）映射成界面消息流：
//   user → 用户气泡；assistant → 气泡 + 工具卡片；tool → 结果回填到前一条 assistant 的卡片
// system 行直接丢弃（不该出现在历史里）。
function renderHistory(raw) {
  const out = []
  let lastAssistant = null
  for (const m of raw) {
    if (m.role === 'user') {
      out.push({ role: 'user', content: m.content })
      lastAssistant = null
    } else if (m.role === 'assistant') {
      const msg = { role: 'assistant', content: m.content || '', toolCalls: [] }
      for (const call of m.tool_calls || []) {
        const fn = call.function || {}
        const args = safeParseArgs(fn.arguments)
        msg.toolCalls.push({
          id: call.id || '',
          name: fn.name || '',
          args,
          summary: '',
          detail: '',
          result: '',
          // 历史里的调用已经有过结果，先记 done；下面由 tool 行回填真实结果与状态
          status: 'done',
          actionId: '',
        })
      }
      if (!msg.content && !msg.toolCalls.length) continue
      out.push(msg)
      lastAssistant = msg
    } else if (m.role === 'tool') {
      if (!lastAssistant) continue
      // 优先按 tool_call_id 匹配；没有 id 时按顺序回填第一个没结果的卡片
      let tc = null
      if (m.tool_call_id) tc = lastAssistant.toolCalls.find(t => t.id === m.tool_call_id)
      if (!tc) tc = lastAssistant.toolCalls.find(t => !t.result)
      if (tc) {
        tc.result = m.content || ''
        tc.status = 'done'
      }
    }
  }
  return out
}

function safeParseArgs(raw) {
  if (!raw) return {}
  if (typeof raw === 'object') return raw
  try {
    return JSON.parse(raw)
  } catch {
    return { raw: String(raw) }
  }
}

// ── 对话 ──
async function sendMessage(text) {
  const content = (text || inputText.value).trim()
  if (!content || streaming.value || !settings.enabled) return

  inputText.value = ''
  messages.value.push({ role: 'user', content })
  streaming.value = true
  scrollBottom()

  // 助手占位消息（后续增量填充）
  const assistantMsg = reactive({ role: 'assistant', content: '', toolCalls: [] })
  messages.value.push(assistantMsg)

  let streamText = ''

  const ensureTC = (name, args, actionId) => {
    // 同一个 actionId 视为同一张卡（后端「发起」和「完成」是两条事件）
    let tc = actionId
      ? assistantMsg.toolCalls.find(t => t.actionId === actionId)
      : null
    if (!tc) tc = assistantMsg.toolCalls.find(t => t.name === name && t.status !== 'done' && t.status !== 'failed')
    if (!tc) {
      tc = reactive({ name, summary: '', detail: '', args: args || {}, result: '', status: 'pending', actionId: actionId || '' })
      assistantMsg.toolCalls.push(tc)
    }
    if (args && Object.keys(args).length) tc.args = args
    if (actionId) tc.actionId = actionId
    return tc
  }

  try {
    await AssistantAPI.chat(content, currentSessionId.value, ({ event, data }) => {
      switch (event) {
        case 'meta':
          if (data.session_id) currentSessionId.value = data.session_id
          break
        case 'role':
          if (data.role === 'assistant') {
            streamText = ''
          }
          break
        case 'text':
          streamText += data.text || ''
          assistantMsg.content = streamText
          scrollBottom()
          break
        case 'tool':
          {
            // 后端把「发起」和「完成」都放在 tool 事件里（靠 status 区分），
            // 且等待确认时带 action_id —— 确认卡必须挂上 actionId，否则
            // 界面只显示一个「待确认」标签却没有按钮（用户就点不了）。
            const tc = ensureTC(data.name, data.args, data.action_id)
            if (data.summary && !tc.summary) tc.summary = data.summary
            if (data.action_id) tc.actionId = data.action_id
            if (data.status === 'awaiting_confirmation') {
              tc.status = 'confirming'
            } else if (data.status === 'executing') {
              tc.status = 'pending'
            } else if (data.status === 'done') {
              tc.status = 'done'
              if (data.result) tc.result = data.result
            } else if (data.status === 'error' || data.status === 'blocked') {
              tc.status = data.status === 'blocked' ? 'rejected' : 'failed'
              if (data.result) tc.result = data.result
              if (data.reason) tc.result = data.reason
            }
            scrollBottom()
          }
          break
        case 'tool_result':
          {
            const tc = assistantMsg.toolCalls.find(t => t.name === data.name)
            if (tc) {
              tc.result = data.result || ''
              tc.status = data.status || 'done'
            }
            scrollBottom()
          }
          break
        case 'action':
          {
            // action 事件本身带 detail（人话描述）和 args，挂到对应卡片上
            const tc = ensureTC(data.name || data.capability, data.args, data.action_id || data.id)
            tc.actionId = data.action_id || data.id || tc.actionId
            if (data.detail) tc.detail = data.detail
            if (data.summary) tc.summary = data.summary
            if (data.args && Object.keys(data.args).length) tc.args = data.args
            if (data.status === 'pending') tc.status = 'confirming'
            scrollBottom()
          }
          break
        case 'error':
          assistantMsg.content = `⚠️ ${data.message || data.error || '出错了'}`
          assistantMsg.role = 'error'
          break
        case 'done':
          break
      }
    })
  } catch (e) {
    assistantMsg.role = 'error'
    assistantMsg.content = `请求失败：${e.message || '未知错误'}`
  } finally {
    streaming.value = false
    scrollBottom()
    // 静默刷新会话列表：标题 / 条数 / 排序（当前会话会浮到最上面）
    loadSessions()
  }
}

async function confirmAction(actionId, approved) {
  // 找到这张卡（可能在任何一条助手消息里）
  let tc = null
  let owner = null
  for (const msg of messages.value) {
    const found = (msg.toolCalls || []).find(t => t.actionId === actionId)
    if (found) { tc = found; owner = msg; break }
  }
  if (!tc) return

  // 拒绝：无需等后端回话，直接收尾
  if (!approved) {
    tc.status = 'rejected'
    tc.result = '已拒绝，未执行。'
    try {
      await AssistantAPI.confirm(actionId, false)
    } catch (e) {
      /* 拒绝失败也无妨，后端那条 pending 一小时后自动过期 */
    }
    return
  }

  tc.status = 'pending'
  tc.result = '正在执行…'
  streaming.value = true
  scrollBottom()

  // 执行结果和助手的后续回复从流里回来：新建一条助手消息承接
  const followMsg = reactive({ role: 'assistant', content: '', toolCalls: [] })
  messages.value.push(followMsg)
  let streamText = ''

  try {
    await AssistantAPI.confirm(actionId, true, ({ event, data }) => {
      switch (event) {
        case 'tool': {
          const name = data.name || tc.name
          const status = data.status === 'executing' ? 'pending' : (data.status === 'error' ? 'failed' : 'done')
          tc.status = status
          if (data.result) tc.result = data.result
          scrollBottom()
          break
        }
        case 'action':
          if (data.action_id === actionId || data.id === actionId) {
            tc.status = data.status === 'failed'
              ? 'failed'
              : data.status === 'executed' ? 'done' : 'confirming'
            if (data.result) tc.result = data.result
            if (data.message) tc.result = data.message
          }
          break
        case 'role':
          if (data.role === 'assistant') streamText = ''
          break
        case 'text':
          streamText += data.text || ''
          followMsg.content = streamText
          scrollBottom()
          break
        case 'error':
          followMsg.role = 'error'
          followMsg.content = `⚠️ ${data.message || data.error || '执行出错'}`
          // 后端在「已处理过 / 策略不允许」这类情况下只回错误事件，
          // 卡片不能一直停在「正在执行…」，否则看起来像卡死。
          if (tc.status === 'pending') {
            tc.status = 'failed'
            if (!tc.result || tc.result === '正在执行…') tc.result = data.message || '未能执行'
          }
          break
        default:
          break
      }
    })
  } catch (e) {
    if (tc.status === 'pending') tc.status = 'failed'
    followMsg.role = 'error'
    followMsg.content = `确认执行失败：${e.message || '未知错误'}`
  } finally {
    streaming.value = false
    // 拿走没有内容的空占位（例如后端只回执行结果、模型没有额外话说）
    if (!followMsg.content) {
      const i = messages.value.indexOf(followMsg)
      if (i >= 0) messages.value.splice(i, 1)
    }
    scrollBottom()
    loadSessions()
  }
}

function formatArgs(args) {
  if (!args) return ''
  try {
    return JSON.stringify(args, null, 2)
  } catch {
    return String(args)
  }
}

function truncateResult(result) {
  if (!result) return ''
  const str = typeof result === 'string' ? result : JSON.stringify(result, null, 2)
  return str.length > 500 ? str.slice(0, 500) + '\n…（已截断）' : str
}

function scrollBottom() {
  nextTick(() => {
    if (msgContainer.value) {
      msgContainer.value.scrollTop = msgContainer.value.scrollHeight
    }
  })
}

// 打开时：加载数据 + 会话引导（第一次打开自动接上最近一次会话，或新建）
watch(() => props.modelValue, async (open) => {
  if (open) {
    loadSettings()
    loadCapabilities()
    loadAIConfig()
    loadModels()
    if (!currentSessionId.value) {
      await loadSessions()
      if (sessions.value.length > 0) {
        await switchSession(sessions.value[0].id)
      } else {
        await createSession()
      }
    }
    scrollBottom()
  }
})

onMounted(() => {
  loadSettings()
  loadCapabilities()
  loadAIConfig()
})
</script>
