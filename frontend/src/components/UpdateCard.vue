<template>
  <!--
    `plain`：被 UpdateModal 装进弹窗时置 true。外壳（圆角/边框/背景/内边距）和
    「版本更新」这个标题都由弹窗自己出，卡片再画一遍就是同一件事说两遍。
  -->
  <section :class="plain ? '' : 'rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white p-4 shadow-[0_1px_2px_rgba(0,0,0,0.03)] sm:p-5'">
    <div v-if="!plain" class="flex items-start gap-3">
      <span class="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]">
        <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': checking || phase === 'installing' }" :stroke-width="1.8" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-[14px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">版本更新</h2>
          <span
            v-if="hasUpdate && !installed"
            class="rounded-full bg-[var(--ys-accent-soft)] px-2 py-0.5 text-[10.5px] font-medium text-[var(--ys-accent)]"
          >有新版本</span>
        </div>
        <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
          从 GitHub Release 检查、下载并安装 fpk。下载和安装由 NAS 后台执行，关掉本页也会继续。
        </p>
      </div>
    </div>

    <div class="mt-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="min-w-0">
        <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span class="text-[11px] text-[var(--ys-text-muted)]">当前</span>
          <span class="font-mono text-[13px] font-medium text-[var(--ys-text)]">{{ current || '…' }}</span>
          <template v-if="latest && hasUpdate">
            <ArrowRight class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
            <span class="text-[11px] text-[var(--ys-text-muted)]">最新</span>
            <span class="font-mono text-[13px] font-semibold text-[var(--ys-accent)]">{{ latest }}</span>
          </template>
        </div>
        <div v-if="sizeLabel && active" class="mt-1 text-[11px] text-[var(--ys-text-faint)]">
          安装包 {{ sizeLabel }}
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-2">
        <button
          v-if="installed"
          type="button"
          class="ys-press rounded-[9px] bg-[var(--ys-accent)] px-3.5 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-[var(--ys-accent-hover)]"
          @click="reloadPage"
        >刷新页面</button>
        <button
          v-else
          type="button"
          class="ys-press rounded-[9px] px-3.5 py-1.5 text-[12px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          :class="hasUpdate
            ? 'bg-[var(--ys-accent)] text-white hover:bg-[var(--ys-accent-hover)]'
            : 'border border-[var(--ys-border)] bg-white text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]'"
          :disabled="busy"
          :title="primaryHint"
          @click="onPrimary"
        >{{ primaryLabel }}</button>
      </div>
    </div>

    <!-- 状态一行：下载/安装/失败/已最新。失败时把后端 message 原样放出来。 -->
    <p
      v-if="statusText"
      class="mt-3 text-[11.5px] leading-relaxed"
      :class="phase === 'error' ? 'text-rose-600' : 'text-[var(--ys-text-muted)]'"
      aria-live="polite"
    >{{ statusText }}</p>

    <div v-if="active" class="mt-2">
      <div class="h-1.5 overflow-hidden rounded-full bg-[var(--ys-surface-sunken)]">
        <div
          class="h-full rounded-full bg-[var(--ys-accent)] transition-[width] duration-500"
          :class="{ 'animate-pulse': phase === 'ready' || phase === 'installing' }"
          :style="{ width: `${Math.max(3, progress)}%` }"
        />
      </div>
      <div class="mt-1 flex justify-between text-[10.5px] text-[var(--ys-text-faint)]">
        <span>{{ progress }}%</span>
        <span v-if="phase === 'downloading' && sizeLabel">{{ sizeLabel }}</span>
        <span v-else-if="phase === 'installing'">正在安装</span>
      </div>
    </div>

    <p v-if="installed" class="mt-3 rounded-[9px] bg-[var(--ys-accent-soft)] px-3 py-2 text-[11.5px] leading-relaxed text-[var(--ys-accent)]">
      更新已启动，应用会在几十秒内重启，稍后刷新页面。
    </p>

    <!-- 更新说明：纯文本，默认折叠。notes 来自 GitHub Release，可能是 Markdown 原文；
         这里只用 {{ }} 插值 + pre 保留换行，**不渲染 HTML**。 -->
    <div v-if="notes && hasUpdate" class="mt-3 border-t border-[var(--ys-border)] pt-2.5">
      <button
        type="button"
        class="flex items-center gap-1.5 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
        :aria-expanded="notesOpen"
        @click="notesOpen = !notesOpen"
      >
        <ChevronDown class="h-3.5 w-3.5 transition-transform" :class="{ 'rotate-180': notesOpen }" :stroke-width="1.8" />
        更新说明
      </button>
      <pre
        v-if="notesOpen"
        class="mt-2 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-[9px] bg-[#FAFAFC] p-3 font-sans text-[11.5px] leading-relaxed text-[var(--ys-text-muted)]"
      >{{ notes }}</pre>
    </div>

    <div v-if="checkedAt && !hasUpdate && !checkError" class="mt-1 text-[10.5px] text-[var(--ys-text-faint)]">
      上次检查：{{ checkedLabel }}
    </div>

    <!-- 下载设置（用户 2026-10-25：「版本更新新增限制速率，避免被限速，新增个人token输入」）。
         默认收起：绝大多数人不需要碰；但「一直 403 / 一直下载失败」的人需要它。
         这两个值存在后端配置里（POST /api/config），改了立刻对下一次检查/下载生效。 -->
    <div class="mt-3 border-t border-[var(--ys-border)] pt-2.5">
      <button
        type="button"
        class="flex items-center gap-1.5 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
        :aria-expanded="settingsOpen"
        @click="settingsOpen = !settingsOpen"
      >
        <SlidersHorizontal class="h-3.5 w-3.5" :stroke-width="1.8" />
        GitHub 令牌
        <span class="text-[10.5px] font-normal text-[var(--ys-text-faint)]">{{ tokenSet ? '已配置' : '未配置' }}</span>
      </button>

      <div v-if="settingsOpen" class="mt-2.5 space-y-3">
        <!-- 个人 token -->
        <div>
          <div class="text-[11.5px] font-medium text-[var(--ys-text)]">GitHub 个人令牌（可选）</div>
          <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
            不填也能用，但 GitHub 对未登录来源限 <b>60 次/小时/IP</b>，共享出口下经常直接 403。
            填一个只读 token（<code class="font-mono text-[10.5px]">public_repo</code> 或 fine-grained 的 Contents 只读）后是 5000 次/小时。
          </p>
          <div class="mt-1.5 flex flex-wrap items-center gap-2">
            <input
              v-model="tokenInput"
              type="password"
              autocomplete="off"
              class="min-w-0 flex-1 rounded-[9px] border border-[var(--ys-border)] bg-white px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] outline-none placeholder:text-[var(--ys-text-faint)] focus:border-[var(--ys-accent)]"
              :placeholder="tokenSet ? '已设置（留空则不改动）' : 'github_pat_… 或 ghp_…'"
            />
            <button
              type="button"
              class="ys-press rounded-[9px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              :disabled="tokenSaving || !tokenInput.trim()"
              @click="saveToken"
            >保存</button>
            <button
              v-if="tokenSet"
              type="button"
              class="ys-press rounded-[9px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-50"
              :disabled="tokenSaving"
              @click="clearToken"
            >清除</button>
          </div>
          <p v-if="tokenNote" class="mt-1 text-[11px]" :class="tokenError ? 'text-rose-600' : 'text-[var(--ys-text-faint)]'">{{ tokenNote }}</p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { appUpdate, formatBytes } from '../services/appUpdate'
import { ConfigAPI } from '../api/client'
import { apiError, fail } from '../services/userMsg'

defineProps({
  /** 见模板注释：弹窗里用 true，去掉自带的卡片外壳与标题。 */
  plain: { type: Boolean, default: false },
})

const {
  current, latest, hasUpdate, notes, publishedAt, size, checkedAt, checkError,
  checking, installed, progress, error, phase,
} = appUpdate

const notesOpen = ref(false)

const active = computed(() => ['downloading', 'ready', 'installing'].includes(phase.value))
const busy = computed(() => checking.value || active.value || installed.value)
const sizeLabel = computed(() => formatBytes(size.value))

const primaryLabel = computed(() => {
  if (checking.value && !hasUpdate.value) return '检查中…'
  if (phase.value === 'downloading') return `更新中 ${progress.value}%`
  if (phase.value === 'ready' || phase.value === 'installing') return '正在安装…'
  if (hasUpdate.value) return phase.value === 'error' ? '重试更新' : '立即更新'
  return checkError.value ? '重新检查' : '检查更新'
})

const primaryHint = computed(() => {
  if (phase.value === 'downloading') return '正在从 GitHub 下载 fpk，关掉本页也会继续'
  if (phase.value === 'installing') return '正在调用飞牛安装器，请勿关闭应用中心'
  if (hasUpdate.value) return '下载完成后会自动安装，不需要再点一次'
  return '检查 GitHub 上是否有新版本'
})

const statusText = computed(() => {
  if (phase.value === 'installed') return ''
  if (phase.value === 'installing') return '正在安装，请勿关闭飞牛应用中心…'
  if (phase.value === 'ready') return '下载完成，正在准备安装…'
  if (phase.value === 'downloading') return `正在下载更新 ${progress.value}%`
  if (phase.value === 'error') return error.value || '更新失败'
  if (checkError.value && !hasUpdate.value) return `检查更新失败：${checkError.value}`
  if (checking.value) return '正在检查更新…'
  if (!checkedAt.value) return ''
  if (!hasUpdate.value) return '已是最新版本'
  const when = formatWhen(publishedAt.value)
  return when ? `发现新版本 · 发布于 ${when}` : '发现新版本'
})

function toDate(v) {
  if (!v) return null
  const text = String(v)
  if (/^\d+$/.test(text)) {
    const n = Number(text)
    // GitHub/后端可能给秒或毫秒；小于 1e12 的按秒处理。
    return new Date(n < 1e12 ? n * 1000 : n)
  }
  const d = new Date(text)
  return Number.isNaN(d.getTime()) ? null : d
}

function formatWhen(v) {
  const d = toDate(v)
  return d ? d.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' }) : ''
}

const checkedLabel = computed(() => {
  const d = toDate(checkedAt.value)
  return d ? d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : ''
})

function onPrimary() {
  if (hasUpdate.value) appUpdate.startUpdate()
  else appUpdate.check()
}

function reloadPage() {
  window.location.reload()
}

// ── GitHub 个人令牌（用户 2026-10-25）──
//
// 存在后端配置里，清空走 POST /api/config 的专用钥匙 update_token_clear：
// 为什么不直接发 update_github_token: ''：这个字段带 omitempty，「清空」和
// 「没传这个键」在后端解码后长得一样 —— 这是本项目「改回默认永远改不回来」
// 那类 bug 的老根因（榜单首数踩过一次）。
//
// （同一次需求里的「下载限速」被用户当场否掉了：「不需要下载限速」，别再加。）
const settingsOpen = ref(false)
const tokenInput = ref('')
const tokenSet = ref(false)
const tokenSaving = ref(false)
const tokenNote = ref('')
const tokenError = ref(false)

/** 读出当前配置（GET /api/config 与 ConfigAPI.get 同一份结构）。 */
async function loadSettings() {
  try {
    const res = await ConfigAPI.get()
    const cfg = res?.data || res?.config || {}
    // 后端永远不回传 token 的值，只回 update_token_set 这个布尔。
    tokenSet.value = !!cfg.update_token_set
  } catch {
    // 读失败就当默认：不要为了一个可选设置弹错误。
  }
}

async function saveToken() {
  const v = tokenInput.value.trim()
  if (!v || tokenSaving.value) return
  tokenSaving.value = true
  tokenError.value = false
  try {
    const res = await ConfigAPI.save({ update_github_token: v })
    const bad = apiError(res)
    if (bad) {
      tokenError.value = true
      tokenNote.value = fail('保存令牌', new Error(bad))
      return
    }
    // 后端永远不回传 token 的值，只回 update_token_set —— 所以这里只更新布尔。
    tokenSet.value = !!res?.data?.update_token_set
    tokenInput.value = ''
    tokenNote.value = '已保存。下一次「检查更新」就会用上'
    // 令牌影响的就是限速/权限这类事，立刻重查一次让用户看到结果。
    appUpdate.check()
  } catch (e) {
    tokenError.value = true
    tokenNote.value = fail('保存令牌', e)
  } finally {
    tokenSaving.value = false
  }
}

async function clearToken() {
  tokenSaving.value = true
  tokenError.value = false
  try {
    const res = await ConfigAPI.save({ update_token_clear: true })
    const bad = apiError(res)
    if (bad) {
      tokenError.value = true
      tokenNote.value = fail('清除令牌', new Error(bad))
      return
    }
    tokenSet.value = !!res?.data?.update_token_set
    tokenInput.value = ''
    tokenNote.value = '令牌已清除（回到匿名 60 次/小时）'
  } catch (e) {
    tokenError.value = true
    tokenNote.value = fail('清除令牌', e)
  } finally {
    tokenSaving.value = false
  }
}

onMounted(() => {
  // 首次进设置页静默检查一次；失败只在卡片里留一行浅灰提示，不弹 toast。
  appUpdate.ensureChecked()
  // 如果上一次离开时下载/安装还在跑，回来接着轮询，不把进度丢在半路。
  appUpdate.resumeIfActive()
  loadSettings()
})
onUnmounted(() => appUpdate.stopPolling())
</script>
