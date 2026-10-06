<template>
  <!--
    musicdl 音源管理（v2.1.89）。

    # 为什么单独一页

    musicdl 真机上注册了 **56 个源** —— 挤在音源管理首页的一个卡片里放不下，
    而且这个页要做的事（逐个启用/停用 + 单源检测）与「导入 LX 脚本」完全是两件事。

    # 为什么要有「检测」

    **「注册了」不代表「能用」**：真机实测 56 个源里一大半在国内出不了结果
    （超时、被墙、站方改版）。没有检测，用户只能一个个启用再试着搜 —— 而
    启用一个坏源是有代价的：sidecar 每次搜索都会为它等满单源预算。
    所以这一页的核心是「让用户看得出哪些能用」，不是「把 56 个开关摆出来」。
  -->
  <div class="space-y-3">
    <InlineNotice :text="pageError" />

    <!-- ── 状态 + 总开关 ── -->
    <div class="rounded-xl border border-gray-200 bg-white px-4 py-3.5">
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold text-gray-800">musicdl 外挂音源</h3>
            <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono', badge.cls]">{{ badge.text }}</span>
          </div>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
            musicdl 覆盖 {{ total }} 个音源（酷狗、酷我、咪咕、B站、Apple Music、Spotify…）。
            打开后首次要建 Python 环境并联网装依赖（约 1-2 分钟），期间曲率照常可用。
          </p>
        </div>
        <button
          role="switch"
          :aria-checked="enabled ? 'true' : 'false'"
          :disabled="busy"
          @click="toggleMaster"
          :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                   enabled ? 'bg-emerald-500' : 'bg-gray-200']">
          <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                         enabled ? 'translate-x-5' : '']" />
        </button>
      </div>
      <div v-if="enabled && statusNote" class="mt-2 flex items-start justify-between gap-2">
        <p class="text-[11px]" :class="statusTone">{{ statusNote }}</p>
        <button
          v-if="canRestart"
          :disabled="restarting"
          class="shrink-0 rounded-lg border border-gray-200 px-2 py-1 text-[11px] text-gray-600 hover:border-emerald-300 hover:text-emerald-600 disabled:opacity-50"
          @click="restartSidecar">
          {{ restarting ? '正在重启…' : '重试启动' }}
        </button>
      </div>
    </div>

    <template v-if="enabled">
      <!-- ── 工具条 ── -->
      <div class="rounded-xl border border-gray-200 bg-white px-4 py-3">
        <div class="flex flex-wrap items-center gap-2">
          <input
            v-model="filter"
            type="text"
            placeholder="过滤源名 / 短码…"
            class="flex-1 min-w-[10rem] rounded-lg border border-gray-200 px-3 py-1.5 text-xs outline-none focus:border-emerald-300" />
          <span class="text-[11px] text-gray-500 font-mono">已启用 {{ selected.length }} / {{ total }}</span>
          <button @click="selectOnlyUsable" :disabled="busy || !probed.length"
            class="px-3 py-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-xs text-emerald-700 disabled:opacity-50"
            title="只留下检测到能出结果的源（先点「检测当前启用的」或「检测全部」）">
            仅留可用
          </button>
          <button @click="selectAll" :disabled="busy" class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">全部启用</button>
          <button @click="selectNone" :disabled="busy" class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">全部停用</button>
          <button @click="probeSelected" :disabled="probing || !selected.length"
            class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 disabled:opacity-50">
            <Gauge :class="['w-3.5 h-3.5 text-emerald-500', probing ? 'animate-spin' : '']" />
            {{ probing ? `检测中 ${probeDone}/${probeTotal}` : '检测已启用' }}
          </button>
          <button @click="probeAll" :disabled="probing"
            class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50"
            title="把 56 个源都试一遍 —— 会跑很久（每个源单源预算十几秒），建议只在你真想筛一遍时用">
            检测全部
          </button>
        </div>
        <p class="mt-2 text-[11px] text-gray-400">
          ⚠️ 启用一个**用不了**的源是有代价的：每次搜索都要为它等满单源预算。先检测再启用。
        </p>
      </div>

      <!-- ── 源列表 ── -->
      <div class="rounded-xl border border-gray-200 bg-white overflow-hidden">
        <div
          v-for="s in visibleSources"
          :key="s.id"
          class="flex items-center gap-3 px-4 py-2.5 border-b border-gray-100 last:border-b-0 hover:bg-gray-50">
          <button
            role="switch"
            :aria-checked="isOn(s.id) ? 'true' : 'false'"
            :disabled="busy"
            @click="toggle(s.id)"
            :class="['relative w-9 h-5 rounded-full transition-colors shrink-0 disabled:opacity-50',
                     isOn(s.id) ? 'bg-emerald-500' : 'bg-gray-200']">
            <span :class="['absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform',
                           isOn(s.id) ? 'translate-x-4' : '']" />
          </button>

          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="text-[13px] text-gray-800 truncate">{{ s.label }}</span>
              <span class="text-[10px] font-mono text-gray-400">{{ s.id }}</span>
              <span
                v-if="s.native"
                class="text-[10px] px-1.5 py-0.5 rounded-full bg-amber-50 text-amber-600 border border-amber-200"
                title="这个源与曲率原生平台重叠：启用它会把该平台的搜索与解析都换成 musicdl（一个平台只能有一个 id 空间）">
                会换掉原生
              </span>
            </div>
            <div class="text-[10px] font-mono text-gray-300 truncate">{{ s.client }}</div>
          </div>

          <span
            v-if="probeOf(s.id)"
            :class="['text-[10px] font-mono shrink-0 px-1.5 py-0.5 rounded',
                     probeOf(s.id).ok ? 'bg-emerald-50 text-emerald-600' : 'bg-rose-50 text-rose-500']"
            :title="probeOf(s.id).error ? (probeOf(s.id).error.kind + ': ' + probeOf(s.id).error.message) : ''">
            {{ probeOf(s.id).ok ? `${probeOf(s.id).ms}ms · ${probeOf(s.id).items} 首` : `失败 ${probeOf(s.id).ms}ms` }}
          </span>
          <span v-else-if="probing" class="text-[10px] text-gray-300 shrink-0 font-mono">待测</span>
        </div>
        <div v-if="!visibleSources.length" class="px-4 py-6 text-center text-xs text-gray-400">没有匹配的源</div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Gauge } from 'lucide-vue-next'
import { ConfigAPI, MusicDLAPI } from '../api/client'
import { describeError } from '../services/userMsg'
import { logInfo, logWarn } from '../services/appLog'
import InlineNotice from './InlineNotice.vue'

const emit = defineEmits(['changed'])

const enabled = ref(false)
const selected = ref([])          // 启用中的源短码
const allSources = ref([])        // 全部源（来自 sidecar）
const total = ref(0)
const status = ref(null)
const busy = ref(false)
const probing = ref(false)
const probeDone = ref(0)
const probeTotal = ref(0)
const probed = ref([])            // 检测结果
const filter = ref('')
const pageError = ref('')

const badge = computed(() => {
  if (!enabled.value) return { text: '已关闭', cls: 'border-gray-200 text-gray-400' }
  const st = status.value?.sidecar?.state || ''
  if (st === 'ready') return { text: '运行中', cls: 'border-emerald-200 bg-emerald-50 text-emerald-600' }
  if (st === 'preparing') return { text: '准备中', cls: 'border-amber-200 bg-amber-50 text-amber-600' }
  if (st === 'starting') return { text: '启动中', cls: 'border-amber-200 bg-amber-50 text-amber-600' }
  if (st === 'failed') return { text: '启动失败', cls: 'border-rose-200 bg-rose-50 text-rose-600' }
  return { text: '待启动', cls: 'border-gray-200 text-gray-400' }
})

/** 状态行：失败与准备都要说清 —— 「装依赖失败」和「平台被风控」长得一模一样 */
const statusNote = computed(() => {
  const sc = status.value?.sidecar || {}
  if (status.value?.available === false) return '这个构建里没有带 sidecar 源码（安装包不完整）。'
  if (sc.state === 'failed') return `启动失败：${sc.error || '原因见日志'}`
  if (sc.state === 'preparing') return sc.note || '正在准备 Python 环境…'
  if (sc.state === 'starting') return sc.note || '正在启动外挂进程…'
  if (sc.state === 'ready') return '已就绪。启用哪些源由下面的开关决定，改完立即生效（不用重启）。'
  return ''
})

const restarting = ref(false)

/**
 * 只有「进程已经没了」才给重试按钮。
 *
 * preparing / starting 时它正在干活（首次要建 venv 装依赖，一两分钟），
 * 这时候点重试只会把刚起来的进程再掐一遍 —— 越点越起不来。
 */
const canRestart = computed(() => {
  const st = status.value?.sidecar?.state
  return st === 'failed' || st === 'off' || st === 'stopped'
})

/**
 * 重试启动 = 让后端拿当前配置再 Apply 一次（配置没变就不会重建，只把挂掉的进程拉起来）。
 *
 * 没有这个入口时，sidecar 因瞬时原因挂掉（被 OOM 杀掉、装依赖时断网、端口被占）
 * 用户就只能看着「启动失败」—— 要么去动一个音源开关，要么重启整个应用。
 */
async function restartSidecar() {
  restarting.value = true
  pageError.value = ''
  try {
    await MusicDLAPI.restart()
    await load()
  } catch (e) {
    pageError.value = describeError(e)
  } finally {
    restarting.value = false
  }
}

const statusTone = computed(() => {
  const st = status.value?.sidecar?.state
  if (st === 'failed') return 'text-rose-600'
  if (st === 'ready') return 'text-emerald-600'
  return 'text-gray-500'
})

const visibleSources = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return allSources.value
  return allSources.value.filter(
    (s) => s.id.includes(q) || (s.label || '').toLowerCase().includes(q) || (s.client || '').toLowerCase().includes(q),
  )
})

function isOn(id) { return selected.value.includes(id) }
function probeOf(id) { return probed.value.find((r) => r.id === id) || null }

async function load() {
  try {
    const cfg = await ConfigAPI.get()
    const c = cfg?.config || cfg?.data || cfg || {}
    enabled.value = !!c.musicdl_enabled
    selected.value = Array.isArray(c.musicdl_sources) && c.musicdl_sources.length ? c.musicdl_sources : ['mg']

    const st = await MusicDLAPI.status()
    status.value = st?.data || null

    if (enabled.value) {
      const src = await MusicDLAPI.sources()
      if (src?.code && src.code !== 200) {
        pageError.value = src.msg || '读不到源清单'
      } else {
        const d = src?.data || src || {}
        allSources.value = d.sources || []
        total.value = d.registered || allSources.value.length
        // 以服务端为准（配置文件里的短码可能是旧的/写错的）
        if (Array.isArray(d.enabled) && d.enabled.length) selected.value = d.enabled
      }
    }
  } catch (e) {
    pageError.value = describeError(e)
  }
}

/** 只提交动到的键（局部 patch），别的设置不受影响 */
async function save(patch) {
  busy.value = true
  pageError.value = ''
  try {
    await ConfigAPI.save(patch)
    await load()
    emit('changed')
  } catch (e) {
    pageError.value = describeError(e)
    await load() // 回滚显示：以服务端为准
  } finally {
    busy.value = false
  }
}

function toggleMaster() {
  const next = !enabled.value
  enabled.value = next
  save({ musicdl_enabled: next })
}

function toggle(id) {
  const set = new Set(selected.value)
  if (set.has(id)) set.delete(id)
  else set.add(id)
  selected.value = allSources.value.map((s) => s.id).filter((x) => set.has(x))
  save({ musicdl_sources: selected.value })
}

function selectAll() {
  selected.value = allSources.value.map((s) => s.id)
  save({ musicdl_sources: selected.value })
}

function selectNone() {
  // 后端语义：空名单 = 回默认。这里给一个「只留 mg」的明确结果，避免歧义。
  selected.value = ['mg']
  save({ musicdl_sources: selected.value })
}

/** 只留下「检测过且出过结果」的源（没测过的会被丢掉 —— 这一点在按钮 title 里写明了） */
function selectOnlyUsable() {
  const ok = probed.value.filter((r) => r.ok).map((r) => r.id)
  if (!ok.length) return
  selected.value = allSources.value.map((s) => s.id).filter((x) => ok.includes(x))
  save({ musicdl_sources: selected.value })
}

async function runProbe(ids) {
  probing.value = true
  probeDone.value = 0
  probeTotal.value = ids.length
  probed.value = []
  try {
    // 一次只发一批（后端每个源要真搜一次，几十个源一起发会把 sidecar 压满）
    const BATCH = 8
    for (let i = 0; i < ids.length; i += BATCH) {
      const batch = ids.slice(i, i + BATCH)
      const res = await MusicDLAPI.probe(batch)
      const d = res?.data || res || {}
      probed.value = probed.value.concat(d.results || [])
      probeDone.value = Math.min(i + BATCH, ids.length)
    }
    const usable = probed.value.filter((r) => r.ok).length
    logInfo(`musicdl 检测完成：${usable}/${ids.length} 个源能出结果`)
    if (usable === 0) logWarn('musicdl 检测：这批源一个都没出结果')
  } catch (e) {
    pageError.value = describeError(e)
  } finally {
    probing.value = false
  }
}

function probeSelected() { runProbe([...selected.value]) }
function probeAll() { runProbe(allSources.value.map((s) => s.id)) }

onMounted(load)
</script>
