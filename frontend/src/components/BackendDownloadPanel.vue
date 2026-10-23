<!--
  后台下载（NAS 端执行）的**能力状态条**。

  架构口径（2026-10-23）：全站只有一套持久队列，下载由 Go worker 执行。
  所以这里不再有「交给后台」——下载本来就是后台做的；这条的状态意义变成：

    · 后台现在**能不能真的下**（status.ready）：洛雪宿主没开 / 没有平台渠道时
      必须如实说不可用 + 原因，而不是让用户点「下载」后任务一直 pending；
    · 现在**在跑什么**（排队 / 下载中 / 暂停 / 失败 / 已完成）；
    · 哪些渠道可用（channels、lx_host），以及后台队列本身的暂停状态；
    · 后端版本有没有这些接口（老版本 404 → 明确说「没有后台下载接口」）。

  任务明细本身在下面的队列列表里（可操作）；这里展开只做只读对照，
  避免两个地方各写一套「重试/删除」按钮、语义慢慢跑偏。
-->
<template>
  <section class="shrink-0 border-b border-[var(--ys-border)] bg-white">
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5 px-4 py-2">
      <button
        type="button"
        class="ys-press flex min-w-0 items-center gap-1.5 rounded-[8px] px-1 py-0.5 text-left transition-colors hover:bg-black/[0.04]"
        :title="expanded ? '收起后台状态明细' : '展开后台状态明细（渠道 / 任务对照）'"
        @click="expanded = !expanded"
      >
        <CloudDownload
          class="h-4 w-4 shrink-0"
          :class="tone.icon"
          :stroke-width="1.8"
        />
        <span class="whitespace-nowrap text-[12px] font-medium text-[var(--ys-text)]">后台下载</span>
        <span :class="['whitespace-nowrap rounded-full px-1.5 py-0.5 text-[10px] font-medium', tone.chip]">
          {{ tone.label }}
        </span>
        <ChevronDown
          class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)] transition-transform"
          :class="expanded ? 'rotate-180' : ''"
          :stroke-width="1.8"
        />
      </button>

      <!-- 计数：空队列不摆一排 0 -->
      <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px]">
        <span v-if="!totalCount && ready === true" class="text-[var(--ys-text-faint)]">队列为空，下载会直接排到这里</span>
        <span v-if="activeCount" class="whitespace-nowrap text-[var(--ys-text-muted)]">
          <b class="text-[var(--ys-text)]">{{ pendingCount }}</b> 排队
          <template v-if="downloadingCount">· <b class="text-emerald-600">{{ downloadingCount }}</b> 下载中</template>
        </span>
        <span v-if="paused" class="whitespace-nowrap rounded-full bg-amber-50 px-1.5 py-0.5 font-medium text-amber-600">
          整队已暂停
        </span>
        <span v-if="stats.paused" class="whitespace-nowrap rounded-full bg-amber-50 px-1.5 py-0.5 font-medium text-amber-600">
          {{ stats.paused }} 条单条暂停
        </span>
        <span v-if="stoppedCount" class="whitespace-nowrap text-[var(--ys-text-muted)]">{{ stoppedCount }} 已停止</span>
        <span v-if="failedCount" class="whitespace-nowrap font-medium text-rose-600">{{ failedCount }} 失败</span>
        <span v-if="completedCount" class="whitespace-nowrap text-[var(--ys-text-muted)]">{{ completedCount }} 已完成</span>
      </div>

      <div class="ml-auto flex items-center gap-1">
        <button
          v-if="ready === true && (activeCount || paused)"
          type="button"
          class="ys-press flex items-center gap-1 rounded-full px-2 py-1 text-[11px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
          :disabled="busy"
          :title="paused ? '让后台继续处理队列' : '让后台暂停全部任务（可在明细或队列里继续）'"
          @click="togglePause"
        >
          <Play v-if="paused" class="h-3.5 w-3.5" :stroke-width="1.8" />
          <Pause v-else class="h-3.5 w-3.5" :stroke-width="1.8" />
          {{ paused ? '继续' : '暂停' }}
        </button>
        <button
          type="button"
          class="ys-press flex h-6 w-6 items-center justify-center rounded-full text-[var(--ys-text-faint)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
          :class="loading ? 'animate-spin' : ''"
          title="刷新后台状态"
          @click="refresh"
        >
          <RefreshCw class="h-3.5 w-3.5" :stroke-width="1.8" />
        </button>
      </div>
    </div>

    <!-- 不可用 / 报错 / 弱提示：说清原因，别让用户对着按钮猜 -->
    <div v-if="notice" class="px-4 pb-2">
      <div class="flex items-start gap-1.5 rounded-[8px] bg-[var(--ys-surface-sunken)] px-2.5 py-1.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
        <Info class="mt-[1px] h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
        <div class="min-w-0">
          <b class="text-[var(--ys-text)]">{{ notice.title }}</b>
          <span class="whitespace-pre-wrap"> —— {{ notice.body }}</span>
        </div>
      </div>
    </div>

    <!-- 展开：渠道状态 + 只读任务对照 -->
    <div v-if="expanded" class="max-h-[260px] overflow-y-auto border-t border-black/[0.04] px-4 py-2">
      <div class="mb-1.5 flex flex-wrap items-center gap-1.5 text-[10.5px] text-[var(--ys-text-muted)]">
        <span class="text-[var(--ys-text-faint)]">取音渠道：</span>
        <span
          v-for="c in channels" :key="c.id || c.name"
          :class="['rounded-full px-1.5 py-0.5', c.ready ? 'bg-emerald-50 text-emerald-600' : 'bg-gray-100 text-gray-500']"
          :title="c.reason || (c.ready ? '可用' : '不可用')"
        >
          {{ c.label || c.name || c.id }}{{ c.ready ? '' : '（不可用）' }}
        </span>
        <span v-if="!channels.length" class="text-[var(--ys-text-faint)]">后端没有报告渠道信息</span>
        <span v-if="lxHost" class="rounded-full bg-gray-100 px-1.5 py-0.5 text-gray-500">洛雪宿主：{{ lxHostLabel }}</span>
      </div>

      <div v-if="!sortedItems.length" class="py-2 text-center text-[11px] text-[var(--ys-text-faint)]">
        后台队列为空
      </div>
      <div
        v-for="t in sortedItems"
        :key="t.id || `${t.clientId}:${t.name}`"
        class="flex items-center gap-2 border-b border-black/[0.04] py-1.5 last:border-0"
      >
        <span :class="['shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium', meta(t).tone]">{{ meta(t).label }}</span>
        <div class="min-w-0 flex-1">
          <div class="flex items-baseline gap-1.5">
            <span class="truncate text-[12px] text-[var(--ys-text)]" :title="t.name">{{ t.name || '（无名）' }}</span>
            <span class="shrink-0 truncate text-[11px] text-[var(--ys-text-muted)]">{{ t.singer }}</span>
          </div>
          <div v-if="t.error" class="truncate text-[10.5px] text-rose-500" :title="t.error">{{ t.error }}</div>
          <div v-else-if="t.status === 'success'" class="truncate text-[10.5px] text-[var(--ys-text-faint)]" :title="t.path">
            {{ t.path || '已完成' }}
          </div>
        </div>
        <span v-if="t.actualQuality || t.quality" class="hidden shrink-0 text-[10.5px] text-[var(--ys-text-faint)] sm:inline">
          {{ t.actualQuality || t.quality }}
        </span>
        <span v-if="t.tries > 1" class="shrink-0 text-[10px] text-[var(--ys-text-faint)]" title="后台尝试次数">第{{ t.tries }}次</span>
      </div>

      <div class="pt-1.5 text-[10.5px] leading-relaxed text-[var(--ys-text-faint)]">
        {{ capabilityNote }} 重试 / 删除 / 清除记录都在下面的队列里操作。
      </div>
    </div>
  </section>
</template>

<script setup>
//
// 只读状态条：不提供第二个「重试/删除」按钮组，动作统一由队列列表（downloadManager）发起，
// 这样后台动作只有一条调用链，行为不会两处跑偏。
//
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ChevronDown, CloudDownload, Info, Pause, Play, RefreshCw } from 'lucide-vue-next'
import {
  backendDownload, backendStatusMeta, BACKEND_CAPABILITY_NOTE,
} from '../services/backendDownload.js'
import { ok as notifyOk, warn as notifyWarn } from '../services/feedback'

const {
  available,
  ready,
  reason,
  usable,
  channels,
  lxHost,
  paused,
  lxWeakHint,
  items: backendItems,
  stats,
  loading,
  busy,
  errorText,
  totalCount,
  pendingCount,
  downloadingCount,
  stoppedCount,
  completedCount,
  failedCount,
  activeCount,
} = backendDownload

const capabilityNote = BACKEND_CAPABILITY_NOTE
const expanded = ref(false)

const tone = computed(() => {
  if (available.value === false) return { label: '接口不可用', chip: 'bg-gray-100 text-gray-500', icon: 'text-[var(--ys-text-faint)]' }
  if (ready.value === false) return { label: '当前不可下', chip: 'bg-amber-50 text-amber-600', icon: 'text-amber-500' }
  if (ready.value === true) return { label: '就绪', chip: 'bg-emerald-50 text-emerald-600', icon: 'text-emerald-500' }
  return { label: '检查中…', chip: 'bg-gray-100 text-gray-500', icon: 'text-[var(--ys-text-faint)]' }
})

const lxHostLabel = computed(() => ({ off: '未开启', starting: '启动中', ready: '已就绪' })[lxHost.value] || lxHost.value)

/** 一条条给原因，优先级：接口没有 > 能力不可用 > 弱提示 > 请求报错 */
const notice = computed(() => {
  if (available.value === false) {
    return { title: '当前后端版本没有后台下载接口', body: `${reason.value || '升级后端后就能用'}；现在点下载会失败，请先升级。` }
  }
  if (ready.value === false) {
    return { title: '后台下载当前不可用', body: `${reason.value || '后端没有可用的取音渠道'}` }
  }
  if (errorText.value) return { title: '后台状态没取到', body: errorText.value }
  if (lxWeakHint.value) {
    return { title: '洛雪宿主未开启', body: '后台只能走平台直链，部分歌曲会取不到音源；需要更全的音源请在设置里开启服务端洛雪宿主。' }
  }
  return null
})

const sortedItems = computed(() => {
  const rank = { downloading: 0, pending: 1, paused: 2, failed: 3, stopped: 4, success: 5 }
  return [...backendItems.value].sort((a, b) => (rank[a.status] ?? 9) - (rank[b.status] ?? 9) || (b.addedAt || 0) - (a.addedAt || 0))
})

function meta(t) {
  return backendStatusMeta(t.status)
}

async function refresh() {
  await backendDownload.refreshStatus()
  await backendDownload.refreshQueue()
  if (!usable.value && backendDownload.errorText.value) notifyWarn(backendDownload.errorText.value)
}

async function togglePause() {
  try {
    await backendDownload.togglePause()
    notifyOk(paused.value ? '后台队列已暂停' : '后台队列已继续')
  } catch (e) {
    notifyWarn(e?.userMessage || '切换后台暂停失败')
  }
}

// 计数/状态要在打开抽屉、刷新页面时立刻正确：挂载就探一次，之后交给共享轮询
onMounted(() => backendDownload.start())
onUnmounted(() => backendDownload.stop())
</script>
