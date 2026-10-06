<template>
  <div class="h-full flex flex-col overflow-hidden select-none">
    <!-- Header -->
    <div class="px-4 md:px-6 py-4 bg-white border-b border-gray-100 flex flex-col sm:flex-row sm:items-center justify-between gap-3 shrink-0">
      <div>
        <h2 class="text-sm font-bold text-gray-800 flex items-center gap-2">
          开放接口 · 供 AI 与外部程序调用
          <span class="text-[10px] px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-600 border border-emerald-200 font-mono">
            {{ visibleEndpoints.length }} 个接口
          </span>
        </h2>
        <p class="text-xs text-gray-400 mt-0.5">
          标准 HTTP + JSON，curl 或任意语言都能调。默认只列 AI 会用的那些（界面内部的可展开）；
          要做什么照着下面的「常见任务怎么做」按顺序调即可。
        </p>
      </div>
      <div class="flex items-center gap-2 self-end sm:self-auto">
        <button
          v-if="hiddenCount > 0"
          @click="toggleShowAll"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all"
          :title="showAll ? '收回纯界面自用的接口' : `另有 ${hiddenCount} 个界面内部接口（窗口记忆、日志、批量维护音源等），展开看`"
        >
          <Eye v-if="!showAll" class="w-3.5 h-3.5" />
          <EyeOff v-else class="w-3.5 h-3.5" />
          {{ showAll ? '只看 AI 常用的' : `显示内部接口（+${hiddenCount}）` }}
        </button>
        <button
          @click="copyText(catalogUrl)"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all"
          title="复制接口目录地址，交给 AI 自行发现全部接口"
        >
          <Copy class="w-3.5 h-3.5" />复制目录地址
        </button>
        <button
          @click="reload"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all disabled:opacity-50"
        >
          <RotateCw :class="['w-3.5 h-3.5 text-emerald-500', loading ? 'animate-spin' : '']" />
          刷新
        </button>
      </div>
    </div>

    <div class="flex-1 overflow-y-auto bg-gray-50 p-4 md:p-5 space-y-4">
      <!-- 运行状态 -->
      <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
        <div class="bg-white rounded-xl border border-gray-100 p-4 shadow-2xs">
          <div class="flex items-center gap-2 mb-2">
            <Activity class="w-4 h-4 text-emerald-500" />
            <span class="text-xs font-semibold text-gray-700">服务状态</span>
          </div>
          <div class="space-y-1 text-[11px] text-gray-500">
            <div class="flex justify-between"><span>版本</span><span class="font-mono text-gray-700">{{ health.version || '-' }}</span></div>
            <div class="flex justify-between"><span>运行时长</span><span class="font-mono text-gray-700">{{ uptimeText }}</span></div>
            <div class="flex justify-between">
              <span>鉴权模式</span>
              <span :class="['font-mono', authClass]">
                {{ authText }}
              </span>
            </div>
            <div v-if="authNote" class="text-[10px] text-gray-400 leading-snug pt-0.5">{{ authNote }}</div>
            <div class="flex justify-between"><span>跨域来源</span><span class="font-mono text-gray-700">{{ corsText }}</span></div>
          </div>
        </div>

        <div class="bg-white rounded-xl border border-gray-100 p-4 shadow-2xs md:col-span-2">
          <div class="flex items-center justify-between mb-2">
            <div class="flex items-center gap-2">
              <Cpu class="w-4 h-4 text-blue-500" />
              <span class="text-xs font-semibold text-gray-700">音源解析桥（网页端工作器）</span>
            </div>
            <button
              @click="bridgeWorker.toggle()"
              :class="[
                'px-2.5 py-1 rounded-lg text-[11px] font-semibold transition-all',
                bridgeWorker.isRunning.value ? 'bg-emerald-50 text-emerald-600 border border-emerald-200' : 'bg-gray-100 text-gray-500'
              ]"
            >
              {{ bridgeWorker.isRunning.value ? '运行中 · 点击停止' : '已停止 · 点击启动' }}
            </button>
          </div>
          <p class="text-[11px] text-gray-400 leading-relaxed">
            第三方音源脚本只能在浏览器执行，因此外部 AI 提交的
            <code class="px-1 rounded bg-gray-100 text-gray-600">/api/bridge/resolve</code>
            任务由本页面领取并完成取链。<strong class="text-gray-500">关闭本页面后，AI 将无法解析新歌曲的直链</strong>（搜索、榜单、本地曲库不受影响）。
          </p>
          <div class="flex items-center gap-4 mt-2 text-[11px] text-gray-500">
            <span>已处理 <b class="text-emerald-600 font-mono">{{ bridgeWorker.processedCount.value }}</b></span>
            <span>失败 <b class="text-rose-500 font-mono">{{ bridgeWorker.failedCount.value }}</b></span>
            <span v-if="bridgeWorker.lastError.value" class="text-rose-400 truncate" :title="bridgeWorker.lastError.value">
              最近错误：{{ bridgeWorker.lastError.value }}
            </span>
          </div>
        </div>
      </div>

      <!--
        给 AI 的操作说明：先说「这套接口是干嘛的」，再给「常见任务怎么做」。
        主要读者是替用户操作本应用的外部 AI，所以按任务组织而不是按分类铺接口 ——
        光给 100 多条平铺列表，它得自己猜该串哪几条，而最容易猜错的恰好是最要紧的那步
        （在线取直链必须有开着的应用页面）。
      -->
      <div v-if="guide" class="space-y-3">
        <div class="p-3 bg-white rounded-xl border border-gray-100 shadow-2xs">
          <div class="flex items-center gap-2 mb-1.5">
            <Lightbulb class="w-4 h-4 text-emerald-600" />
            <span class="text-xs font-semibold text-gray-700">这套接口是给谁用的</span>
          </div>
          <ul class="text-[11px] leading-relaxed text-gray-500 space-y-1">
            <li v-for="(line, i) in guide.what" :key="i">{{ line }}</li>
          </ul>
        </div>

        <div class="p-3 bg-amber-50/70 rounded-xl border border-amber-200/70">
          <div class="text-xs font-semibold text-amber-800 mb-1.5">调用前必须知道</div>
          <ul class="text-[11px] leading-relaxed text-amber-900/90 space-y-1">
            <li v-for="(line, i) in guide.prereq" :key="i">{{ line }}</li>
          </ul>
        </div>

        <div class="p-3 bg-white rounded-xl border border-gray-100 shadow-2xs">
          <div class="text-xs font-semibold text-gray-700 mb-2">常见任务怎么做</div>
          <div class="space-y-2">
            <div v-for="wf in guide.workflows" :key="wf.task" class="rounded-lg border border-gray-100 bg-gray-50/60 p-2.5">
              <div class="flex items-baseline gap-2 flex-wrap">
                <b class="text-[11px] text-gray-700">{{ wf.task }}</b>
                <span v-if="wf.summary" class="text-[10px] text-gray-400">{{ wf.summary }}</span>
              </div>
              <div class="mt-1 text-[11px] text-gray-500 leading-relaxed space-y-0.5">
                <p v-for="(s, i) in wf.steps" :key="i">{{ s }}</p>
              </div>
              <p v-if="wf.watch_out" class="mt-1.5 text-[10px] leading-relaxed text-amber-600">⚠️ {{ wf.watch_out }}</p>
            </div>
          </div>
        </div>

        <div class="p-3 bg-gray-100/60 rounded-xl">
          <div class="text-xs font-semibold text-gray-600 mb-1.5">统一约定</div>
          <ul class="text-[11px] leading-relaxed text-gray-500 space-y-1">
            <li v-for="(line, i) in guide.rules" :key="i">{{ line }}</li>
          </ul>
        </div>
      </div>

      <!-- 老后端没有 guide 时的兜底：目录顶层的扁平 usage -->
      <div v-else-if="usage.length" class="p-3 bg-emerald-50/70 rounded-xl border border-emerald-200/70">
        <div class="flex items-center gap-2 mb-1.5">
          <Lightbulb class="w-4 h-4 text-emerald-600" />
          <span class="text-xs font-semibold text-emerald-800">给 AI 的调用建议</span>
        </div>
        <ul class="text-[11px] text-emerald-900/90 space-y-0.5 leading-relaxed">
          <li v-for="(line, i) in usage" :key="i">{{ line }}</li>
        </ul>
      </div>

      <!-- 分类筛选 -->
      <div class="flex items-center gap-1.5 flex-wrap">
        <button
          v-for="cat in categories" :key="cat"
          @click="activeCategory = cat"
          :class="[
            'px-2.5 py-1 rounded-lg text-[11px] font-medium transition-all border',
            activeCategory === cat ? 'bg-emerald-500 text-white border-emerald-500' : 'bg-white text-gray-600 border-gray-200 hover:border-emerald-300'
          ]"
        >{{ cat }}</button>
      </div>

      <!-- 接口列表 -->
      <div class="space-y-2.5">
        <div
          v-for="ep in filteredEndpoints" :key="ep.method + ep.path"
          class="bg-white rounded-xl border border-gray-100 p-4 shadow-2xs hover:border-gray-200 transition-all"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <span :class="['text-[10px] font-bold px-1.5 py-0.5 rounded font-mono shrink-0', methodClass(ep.method)]">{{ ep.method }}</span>
                <code class="text-xs font-mono text-gray-800 font-semibold">{{ ep.path }}</code>
                <span v-if="ep.requires === 'browser'" class="text-[10px] px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 border border-amber-200/80">
                  需网页端在线
                </span>
                <span v-else-if="ep.requires === 'none' && ep.category !== '系统'" class="text-[10px] px-1.5 py-0.5 rounded bg-emerald-50 text-emerald-600 border border-emerald-200/80">
                  纯服务端
                </span>
                <!-- 展开内部接口时要看得出来：它们是界面自用 / 不该被外部调的，不是「漏标了」 -->
                <span v-if="ep.role === 'internal'" class="text-[10px] px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 border border-gray-200">
                  界面内部
                </span>
              </div>
              <p class="text-xs text-gray-600 mt-1.5">{{ ep.summary }}</p>

              <div v-if="ep.params && ep.params.length" class="mt-2 space-y-0.5">
                <div v-for="p in ep.params" :key="p.name" class="text-[11px] text-gray-500 flex items-start gap-1.5">
                  <span class="font-mono text-gray-700 shrink-0">{{ p.name }}</span>
                  <span v-if="p.required" class="text-rose-500 shrink-0">必填</span>
                  <span class="text-gray-400 shrink-0">[{{ p.in }}]</span>
                  <span class="truncate">{{ p.desc }}</span>
                </div>
              </div>

              <p v-if="ep.notes" class="text-[11px] text-amber-700/90 mt-2 leading-relaxed">💡 {{ ep.notes }}</p>

              <div v-if="ep.example" class="mt-2 flex items-center gap-1.5">
                <pre class="flex-1 min-w-0 text-[10.5px] font-mono bg-gray-900 text-emerald-300 rounded-lg px-2.5 py-1.5 overflow-x-auto">{{ ep.example }}</pre>
                <button
                  @click="copyText(ep.example)"
                  class="p-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-gray-500 shrink-0 transition-colors"
                  title="复制示例命令"
                >
                  <Copy class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>

            <button
              v-if="canTry(ep)"
              @click="tryEndpoint(ep)"
              :disabled="trying === ep.path"
              class="shrink-0 px-2.5 py-1 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 text-[11px] font-medium transition-colors disabled:opacity-50 flex items-center gap-1"
            >
              <Loader2 v-if="trying === ep.path" class="w-3 h-3 animate-spin" />
              <Play v-else class="w-3 h-3" />
              试一试
            </button>
          </div>

          <!-- 试运行结果 -->
          <div v-if="tryResult && tryResult.path === ep.path" class="mt-2">
            <pre class="text-[10.5px] font-mono bg-gray-50 border border-gray-200 rounded-lg p-2.5 max-h-56 overflow-auto text-gray-700">{{ tryResult.text }}</pre>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { AppAPI } from '../api/client'
import { bridgeWorker } from '../services/bridgeWorker'
import {
  Copy, RotateCw, Activity, Cpu, Lightbulb, Play, Loader2, Eye, EyeOff
} from 'lucide-vue-next'

const endpoints = ref([])
const usage = ref([])
// guide 是目录顶层的「这是干嘛的 + 怎么做」。老后端没这个字段时为 null，界面退回 usage 兜底。
const guide = ref(null)
const health = ref({})
const loading = ref(false)
const activeCategory = ref('全部')
const trying = ref('')
const tryResult = ref(null)
const serverBase = ref('')
// 抽屉默认只展示面向人的接口；带 hidden 标记的（偏机器调用）折叠起来，
// 接口本身依然完全可用，可用「显示全部」展开。
const showAll = ref(false)

const catalogUrl = computed(() => `${window.location.origin}/api/catalog`)

const hiddenCount = computed(() => endpoints.value.filter(e => e.hidden).length)

const visibleEndpoints = computed(() =>
  showAll.value ? endpoints.value : endpoints.value.filter(e => !e.hidden)
)

const categories = computed(() => {
  const set = new Set(visibleEndpoints.value.map(e => e.category))
  return ['全部', ...Array.from(set)]
})

const filteredEndpoints = computed(() => {
  if (activeCategory.value === '全部') return visibleEndpoints.value
  return visibleEndpoints.value.filter(e => e.category === activeCategory.value)
})

function toggleShowAll() {
  showAll.value = !showAll.value
  activeCategory.value = '全部'
}

const uptimeText = computed(() => {
  const s = Number(health.value.uptime_seconds || 0)
  if (!s) return '-'
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return `${h}小时${m}分`
  if (m > 0) return `${m}分${sec}秒`
  return `${sec}秒`
})

const corsText = computed(() => {
  const list = health.value.cors_origins || []
  return list.length ? list.join(', ') : '仅同源'
})

// 鉴权模式三态：none 完全放开 / lan 内网免校验、其它来源要 Token / token 一律校验
const authText = computed(() => {
  switch (health.value.auth_mode) {
    case 'token': return 'Token 已启用'
    case 'lan': return '内网免校验 · 公网需 Token'
    default: return '完全开放（无 Token）'
  }
})

const authClass = computed(() => {
  switch (health.value.auth_mode) {
    case 'token': return 'text-emerald-600'
    case 'lan': return 'text-sky-600'
    default: return 'text-rose-600'
  }
})

// 后端 auth_note 是权威解释，前端不自己编；额外信任网段一并回显
const authNote = computed(() => {
  const note = health.value.auth_note || ''
  const extra = health.value.trusted_cidrs || []
  const tail = extra.length ? `；额外信任：${extra.join(', ')}` : ''
  return note ? note + tail : tail.replace(/^；/, '')
})

function methodClass(method) {
  switch ((method || '').toUpperCase()) {
    case 'GET': return 'bg-emerald-50 text-emerald-600 border border-emerald-200/70'
    case 'POST': return 'bg-blue-50 text-blue-600 border border-blue-200/70'
    case 'DELETE': return 'bg-rose-50 text-rose-600 border border-rose-200/70'
    default: return 'bg-gray-100 text-gray-600'
  }
}

/** 把示例里的服务端 base_url 替换成当前浏览器访问的地址，便于直接复制使用 */
function rewriteExample(example) {
  if (!example) return example
  if (serverBase.value && serverBase.value !== window.location.origin) {
    return example.split(serverBase.value).join(window.location.origin)
  }
  return example
}

async function reload() {
  loading.value = true
  try {
    const [cat, hl] = await Promise.all([
      AppAPI.catalog().catch(() => null),
      AppAPI.health().catch(() => null),
    ])
    if (cat) {
      serverBase.value = cat.base_url || ''
      endpoints.value = (cat.endpoints || []).map(e => ({ ...e, example: rewriteExample(e.example) }))
      usage.value = cat.usage || []
      guide.value = cat.guide && (cat.guide.workflows || []).length ? cat.guide : null
    }
    if (hl?.data) health.value = hl.data
  } finally {
    loading.value = false
  }
}

/** 只有无必填参数的 GET 接口才提供「试一试」，避免误触发写操作 */
function canTry(ep) {
  if ((ep.method || '').toUpperCase() !== 'GET') return false
  if (ep.path.includes('{')) return false
  return !(ep.params || []).some(p => p.required && p.in === 'query')
}

async function tryEndpoint(ep) {
  trying.value = ep.path
  tryResult.value = null
  try {
    const res = await fetch(ep.path, { headers: { Accept: 'application/json' } })
    const text = await res.text()
    let pretty = text
    try { pretty = JSON.stringify(JSON.parse(text), null, 2) } catch (_) {}
    if (pretty.length > 4000) pretty = pretty.slice(0, 4000) + '\n... (已截断)'
    tryResult.value = { path: ep.path, text: `HTTP ${res.status}\n\n${pretty}` }
  } catch (e) {
    tryResult.value = { path: ep.path, text: '请求失败: ' + (e?.message || e) }
  } finally {
    trying.value = ''
  }
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
  } catch (_) {
    // 剪贴板不可用时退化为手动选择
    window.prompt('复制以下内容：', text)
  }
}

onMounted(() => {
  reload()
  // 打开接口页即确保解析桥在运行
  if (!bridgeWorker.isRunning.value) bridgeWorker.start()
})
</script>
