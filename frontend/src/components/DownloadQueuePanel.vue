<template>
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
    <!--
      工具条（2026-10-19 渐进式披露重构）

      改之前这一行里挤着：存储路径条（独立一整行灰底）、6 个筛选标签、
      全选、全部暂停、全部继续、全部恢复、重试失败、停止全部、清除选中、
      一键清除记录、下载设置 —— 11 个控件争一条 40px 的行。

      现在分三层：
        · 常驻：存储位置（一行小字，点击即改）+ 筛选分段 + 两个「当前真能做」的图标按钮
        · ⋯：全部恢复 / 重试失败 / 停止全部 / 一键清除记录 / 下载设置
        · 勾选了任务才浮出底部操作条（清除选中 / 全选当前筛选）
    -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b border-[var(--ys-border)] bg-white px-4 py-2.5">
      <button
        type="button"
        class="flex min-w-0 max-w-[340px] items-center gap-1.5 rounded-[8px] px-1.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
        :title="`存储位置：${downloadManager.currentDownloadDir.value}（点击更改）`"
        @click="downloadManager.openDirModal()"
      >
        <Folder class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
        <span class="truncate font-mono text-[11px]">{{ downloadManager.currentDownloadDir.value }}</span>
      </button>

      <!-- 筛选：分段控件（沉底轨道 + 白色药丸），与全站其它分段同款 -->
      <div class="flex min-w-0 flex-wrap items-center gap-0.5 rounded-[10px] bg-[var(--ys-surface-sunken)] p-1">
        <button
          v-for="tab in filterTabs" :key="tab.id"
          @click="currentFilter = tab.id"
          :class="[
            'ys-press whitespace-nowrap rounded-[8px] px-2.5 py-1 text-[11.5px] font-medium transition-colors',
            currentFilter === tab.id
              ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
              : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]',
          ]"
        >
          {{ tab.name }}
          <span class="ml-0.5 text-[10px] opacity-60">{{ tab.count }}</span>
        </button>
      </div>

      <div class="ml-auto flex items-center gap-1">
        <!-- 这两个是「此刻真能做的事」：暂停/继续，图标化后不占文字空间 -->
        <button
          v-if="downloadManager.activeCount.value > 0"
          @click="downloadManager.pauseAll()"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="暂停所有下载中的任务"
        >
          <Pause class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <button
          v-if="downloadManager.pausedCount.value > 0"
          @click="downloadManager.resumeAll(activeSource?.id)"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="继续所有已暂停的任务"
        >
          <Play class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <OverflowMenu :items="queueMenu" @select="onQueueMenu" />
      </div>
    </div>

  <!-- 4. 任务列表内容区 -->
  <div class="min-h-0 flex-1 space-y-2 overflow-y-auto bg-[#FAFAFC] p-3 sm:p-4">
    <!-- 空状态 -->
    <div
      v-if="!filteredTasks.length"
      class="flex flex-col items-center justify-center py-24 text-center text-gray-400"
    >
      <div class="w-14 h-14 rounded-2xl bg-gray-100 text-gray-300 flex items-center justify-center mb-3">
        <Music2 class="w-7 h-7" />
      </div>
      <p class="text-xs font-medium text-gray-600 mb-1">
        {{ currentFilter === 'all' ? '暂无下载任务' : `暂无${currentFilterName}任务` }}
      </p>
      <p class="text-[11px] text-gray-400 max-w-xs">
        在「发现音乐」里点下载，任务会进这个队列并存到 NAS
      </p>
    </div>

    <!-- 任务卡片列表 -->
    <div
      v-for="task in filteredTasks"
      :key="task.id"
      class="group flex items-center gap-3 rounded-[12px] border border-[var(--ys-border)] bg-white p-3 transition-colors hover:border-[var(--ys-border-strong)]"
    >
      <!-- 勾选框：只在已结束的行上出现（见工具栏那段注释） -->
      <input
        v-if="isTerminal(task.status)"
        type="checkbox"
        class="align-middle accent-emerald-500 shrink-0"
        :checked="selected.has(task.id)"
        @change="toggleSelect(task.id)"
      />

      <!-- 封面 -->
      <div class="w-10 h-10 rounded-lg overflow-hidden bg-gray-100 shrink-0 relative flex items-center justify-center shadow-2xs">
        <img
          v-if="task.song.cover"
          :src="task.song.cover"
          alt="cover"
          referrerpolicy="no-referrer"
          class="w-full h-full object-cover"
          @error="task.song.cover = ''"
        />
        <Music2 v-else class="h-4 w-4 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
      </div>

      <!-- 歌曲信息与状态 -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center justify-between gap-1 mb-0.5">
          <p class="text-xs font-semibold text-gray-800 truncate" :title="task.song.name">
            {{ task.song.name }}
          </p>
          <!-- 平台标签 -->
          <span class="text-[9px] px-1.5 py-0.2 rounded-full bg-gray-100 text-gray-500 font-mono shrink-0">
            {{ task.song.source || task.platform || 'kw' }}
          </span>
        </div>
        <p class="text-[11px] text-gray-400 truncate mb-1" :title="task.song.singer">
          {{ task.song.singer }}{{ task.song.album ? ' · ' + task.song.album : '' }}
        </p>

        <!-- 状态徽标与提示 -->
        <div class="flex items-center gap-1.5 text-[10px]">
          <!-- 等待中 -->
          <span
            v-if="task.status === 'pending'"
            class="px-2 py-0.5 rounded-md bg-gray-100 text-gray-500 flex items-center gap-1"
          >
            <Clock class="w-3 h-3 text-gray-400" />
            <span>排队等待中...</span>
          </span>

          <!-- 解析直链中 -->
          <span
            v-else-if="task.status === 'resolving'"
            class="px-2 py-0.5 rounded-md bg-blue-50 text-blue-600 flex items-center gap-1"
          >
            <Loader2 class="w-3 h-3 animate-spin text-blue-500" />
            <span>正在解析音源直链...</span>
          </span>

          <!-- 下载写入 NAS 中 -->
          <span
            v-else-if="task.status === 'downloading'"
            class="px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-600 flex items-center gap-1 font-medium"
          >
            <Loader2 class="w-3 h-3 animate-spin text-emerald-500" />
            <span>正在下载写入 NAS...</span>
          </span>

          <!-- 已暂停 -->
          <span
            v-else-if="task.status === 'paused'"
            class="px-2 py-0.5 rounded-md bg-amber-50 text-amber-600 flex items-center gap-1 font-medium"
          >
            <Pause class="w-3 h-3 text-amber-500" />
            <span>已暂停下载</span>
          </span>

          <!-- 下载成功 -->
          <span
            v-else-if="task.status === 'success'"
            class="px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-600 flex items-center gap-1 font-medium"
          >
            <Check class="w-3 h-3 text-emerald-500" />
            <span>已保存至 NAS 本地</span>
          </span>

          <!--
            下载失败：列表里只给结论「下载失败」，具体原因放在 title 悬停可见，
            同时已由 downloadManager 写进日志（见 services/appLog.js 的约定）。
          -->
          <span
            v-else-if="task.status === 'failed'"
            class="px-2 py-0.5 rounded-md bg-rose-50 text-rose-600 flex items-center gap-1"
            :title="task.error || '下载失败'"
          >
            <AlertCircle class="w-3 h-3 text-rose-500 shrink-0" />
            <span>下载失败</span>
          </span>

          <!--
            已停止：点过「停止全部」/ 订阅行「停止」后留下的任务。
            ⚠️ 这一档以前**连状态文字都没有**，右侧也没有任何按钮 ——
            用户看得到任务却动不了（2026-09-22 反馈：「下载-停止后，已停止增加恢复下载」）。
          -->
          <span
            v-else-if="task.status === 'stopped'"
            class="px-2 py-0.5 rounded-md bg-gray-100 text-gray-500 flex items-center gap-1"
            :title="task.error || '已被用户停止'"
          >
            <Square class="w-3 h-3 text-gray-400" />
            <span>已停止</span>
          </span>
        </div>

        <!-- 音质 / 实际音源 / 重试次数 -->
        <div class="flex items-center gap-1.5 mt-1 text-[10px] flex-wrap">
          <span class="px-1.5 py-0.5 rounded bg-emerald-50 text-emerald-600 font-mono">
            {{ downloadManager.qualityLabel(task.resolvedQuality || task.quality) }}
          </span>
          <!-- 请求无损但真实容器是有损：后端按文件魔数判定，是权威结论。
               文件本身没问题（内容与扩展名一致），只是音源没给无损直链。 -->
          <span
            v-if="task.qualityMismatch"
            class="px-1.5 py-0.5 rounded bg-amber-50 text-amber-600 font-medium"
            :title="`请求的是无损音质，但文件实际是 ${(task.actualFormat || '未知').toUpperCase()} —— 音源没提供无损直链。文件内容与扩展名一致，可正常播放。`"
          >
            实际 {{ (task.actualFormat || '?').toUpperCase() }}
          </span>
          <span
            v-if="task.resolvedSource"
            class="px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 truncate max-w-[110px]"
            :title="'实际取链音源：' + task.resolvedSource"
          >
            {{ task.resolvedSource }}
          </span>
          <span v-if="task.attempts > 0" class="text-gray-400">
            第 {{ task.attempts }}/{{ downloadManager.maxAttempts.value }} 次
          </span>
          <span v-if="task.embedded" class="text-emerald-500">✓ 已内嵌歌词</span>
        </div>
      </div>

      <!-- 右侧操作按钮 -->
      <div class="shrink-0 flex items-center gap-1">
        <!-- 排队或下载中：暂停按钮 -->
        <button
          v-if="task.status === 'pending' || task.status === 'resolving' || task.status === 'downloading'"
          @click="downloadManager.pauseTask(task.id)"
          class="p-1.5 rounded-lg bg-amber-50 hover:bg-amber-100 text-amber-600 transition-colors"
          title="暂停下载"
        >
          <Pause class="w-3.5 h-3.5" />
        </button>

        <!-- 已暂停：继续按钮 -->
        <button
          v-if="task.status === 'paused'"
          @click="downloadManager.resumeTask(task.id, activeSource?.id)"
          class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 transition-colors"
          title="继续下载"
        >
          <Play class="w-3.5 h-3.5" />
        </button>

        <!--
          已停止：恢复下载。
          `resumeTask` 会复位 `_aborted`（否则 `executeTask` 一进去就 return），
          所以这里直接调它即可，service 层不需要改。
        -->
        <button
          v-if="task.status === 'stopped'"
          @click="downloadManager.resumeTask(task.id, activeSource?.id)"
          class="px-2 py-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 text-[10px] font-medium transition-colors flex items-center gap-1"
          title="恢复下载（重新排队并继续）"
        >
          <Play class="w-3 h-3" />
          <span>恢复下载</span>
        </button>

        <!-- 排队中/下载中/已暂停：取消并停止按钮 -->
        <button
          v-if="task.status === 'pending' || task.status === 'resolving' || task.status === 'downloading' || task.status === 'paused'"
          @click="downloadManager.cancelTask(task.id)"
          class="p-1.5 rounded-lg text-gray-400 hover:text-rose-600 hover:bg-rose-50 transition-colors"
          title="停止并取消此任务"
        >
          <Square class="w-3.5 h-3.5" />
        </button>

        <!-- 失败任务：切换音质 / 换源 / 重试 -->
        <template v-if="task.status === 'failed'">
          <SelectField
            compact
            class="w-[104px]"
            :model-value="task.quality"
            :options="downloadManager.QUALITY_OPTIONS"
            hint="为该任务单独指定音质后重试"
            @update:model-value="v => onTaskQualityPick(task, v)"
          />

          <button
            @click="downloadManager.switchSource(task.id)"
            class="px-1.5 py-1 rounded-lg bg-blue-50 hover:bg-blue-100 text-blue-600 text-[10px] font-medium transition-colors"
            :title="'换一个健康音源重试（上次：' + (task.resolvedSource || '自动') + '）'"
          >
            换源
          </button>

          <button
            @click="downloadManager.retryTask(task.id, activeSource?.id)"
            class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 transition-colors"
            title="重新下载"
          >
            <RotateCw class="w-3.5 h-3.5" />
          </button>
        </template>

        <!-- 移除记录按钮 (已完成 / 失败 / 已停止) -->
        <button
          v-if="task.status === 'success' || task.status === 'failed' || task.status === 'stopped'"
          @click="downloadManager.removeTask(task.id)"
          class="p-1.5 rounded-lg text-gray-300 hover:text-gray-500 hover:bg-gray-100 transition-colors"
          title="移除记录"
        >
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  </div>
  </div>

<!-- ── 下载设置弹窗（入口在筛选栏右侧的绿色按钮） ── -->
<Teleport to="body">
  <div
    v-if="showSettings"
    class="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/50 backdrop-blur-xs"
    @click.self="showSettings = false"
  >
    <div class="max-h-[85vh] w-full max-w-[560px] overflow-y-auto rounded-2xl bg-white shadow-2xl">
      <div class="flex items-center justify-between border-b border-gray-100 px-5 py-3.5">
        <h3 class="text-sm font-bold text-gray-800 flex items-center gap-1.5">
          <Settings2 class="w-4 h-4 text-emerald-500" />
          <span>下载设置</span>
        </h3>
        <button class="text-gray-400 hover:text-gray-600" @click="showSettings = false">
          <X class="w-4 h-4" />
        </button>
      </div>

      <div class="max-h-[70vh] space-y-4 overflow-y-auto px-5 py-4">
        <div class="grid grid-cols-2 gap-3 sm:gap-4">
          <SelectField
            v-model="qualityModel"
            label="默认音质"
            :options="downloadManager.QUALITY_OPTIONS"
            hint="「最高音质」会在多音源之间轮询最高可用档位"
          />
          <SelectField
            v-model="attemptsModel"
            label="失败重试"
            numeric
            :options="[1, 2, 3, 4, 5].map(n => ({ value: n, label: n + ' 次' }))"
            hint="单个任务失败后的重试次数"
          />
          <SelectField
            v-model="concurrencyModel"
            label="同时下载"
            numeric
            :options="[1, 2, 3].map(n => ({ value: n, label: n + ' 首' }))"
            hint="保守设置可降低被音源风控的概率"
          />
          <SelectField
            v-model="startIntervalModel"
            label="任务间隔"
            numeric
            :options="[0, 3, 5, 8, 10, 15, 20, 30].map(n => ({ value: n, label: n === 0 ? '不等待' : n + ' 秒' }))"
            hint="每两首之间至少等待多久再开始下一首"
          />
          <SelectField
            v-model="doneIntervalModel"
            label="完成后间隔"
            numeric
            :options="[0, 2, 3, 5, 8, 10, 15].map(n => ({ value: n, label: n === 0 ? '不等待' : n + ' 秒' }))"
            hint="一首完成后至少间隔多久才开始下一首"
          />
        </div>

        <!-- 开关项：与上面的下拉分组隔开，避免和 SelectField 的 hint 挤在一起 -->
        <div class="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border border-gray-200 bg-gray-50/60 px-3.5 py-3">
          <label class="flex items-center gap-2 cursor-pointer text-[12px] text-gray-600" title="失败时自动轮换到其它健康音源重试">
            <input type="checkbox" v-model="failoverModel" class="accent-emerald-500 w-3.5 h-3.5" />
            <span>自动换源</span>
          </label>
          <label class="flex items-center gap-2 cursor-pointer text-[12px] text-gray-600" title="把歌词写入音频文件标签（MP3 USLT / FLAC LYRICS）">
            <input type="checkbox" v-model="embedModel" class="accent-emerald-500 w-3.5 h-3.5" />
            <span>内嵌歌词</span>
          </label>
          <label class="flex items-center gap-2 cursor-pointer text-[12px] text-gray-600" title="同时写出与音频同名的 .lrc 文件">
            <input type="checkbox" v-model="lrcModel" class="accent-emerald-500 w-3.5 h-3.5" />
            <span>外挂 .lrc</span>
          </label>
        </div>

        <p class="text-[11px] leading-relaxed text-gray-400">
          改动立即生效并记住；已经在队列里的任务不受影响。
        </p>
      </div>

      <div class="flex justify-end border-t border-gray-100 px-5 py-3.5">
        <button
          class="rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white transition-colors hover:bg-emerald-600"
          @click="showSettings = false"
        >完成</button>
      </div>
    </div>
      <!-- 勾选了任务才浮出的操作条：清除选中 / 全选当前筛选 -->
    <SelectionBar
      :count="selectedIds.length"
      :actions="[
        { key: 'clear', label: '清除选中', icon: Trash2, danger: true },
        { key: 'all', label: allSelected ? '取消全选' : '全选已结束', icon: ListChecks },
      ]"
      @action="(k) => (k === 'clear' ? clearSelected() : toggleSelectAll())"
      @clear="selected = new Set()"
    />
</div>
</Teleport>

</template>

<script setup>
import { ref, computed } from 'vue'
import { downloadManager } from '../services/downloadManager'
import SelectField from './SelectField.vue'
import OverflowMenu from './OverflowMenu.vue'
import SelectionBar from './SelectionBar.vue'
import {
  X, Folder, RotateCw, Trash2, Check,
  AlertCircle, Loader2, Music2, Clock, Pause, Play, Square,
  Settings2, Settings, ListChecks, Eraser, RotateCcw
} from 'lucide-vue-next'

const props = defineProps({
  activeSource: Object
})

// 下载设置面板默认折叠，避免一屏挤满控件
const showSettings = ref(false)

/**
 * 队列页右上角 ⋯。
 * 判定标准：这个动作**不需要先勾选**、也不是此刻正在发生的事 → 进菜单。
 * 「全部恢复 / 重试失败 / 停止全部」三个批量动作只在对应状态存在时才出现。
 */
const queueMenu = computed(() => [
  { key: 'resumeStopped', label: '全部恢复', icon: Play, disabled: stoppedCount.value === 0, hint: `${stoppedCount.value} 首已停止` },
  { key: 'retryFailed', label: '重试失败', icon: RotateCcw, disabled: downloadManager.failedCount.value === 0, hint: `${downloadManager.failedCount.value} 首失败` },
  { key: 'stopAll', label: '停止全部', icon: Square, danger: true, disabled: downloadManager.activeCount.value + downloadManager.pausedCount.value === 0 },
  { key: 'clearRecords', label: '清除所有记录', icon: Eraser, danger: true, disabled: terminalCount.value === 0, hint: '已完成 / 失败 / 已停止' },
  { key: 'settings', label: '下载设置…', icon: Settings },
])

function onQueueMenu(key) {
  if (key === 'resumeStopped') downloadManager.resumeStopped(activeSource?.id)
  else if (key === 'retryFailed') downloadManager.retryAllFailed(activeSource?.id)
  else if (key === 'stopAll') downloadManager.stopAll()
  else if (key === 'clearRecords') clearAllRecords()
  else if (key === 'settings') showSettings.value = true
}

// ── 下载策略设置的读写模型 ──
const qualityModel = computed({
  get: () => downloadManager.defaultQuality.value,
  set: v => downloadManager.updateSettings({ defaultQuality: v }),
})
const attemptsModel = computed({
  get: () => downloadManager.maxAttempts.value,
  set: v => downloadManager.updateSettings({ maxAttempts: Number(v) }),
})
const concurrencyModel = computed({
  get: () => downloadManager.concurrency.value,
  set: v => downloadManager.updateSettings({ concurrency: Number(v) }),
})
const startIntervalModel = computed({
  get: () => downloadManager.startIntervalSec.value,
  set: v => downloadManager.updateSettings({ startIntervalSec: Number(v) }),
})
const doneIntervalModel = computed({
  get: () => downloadManager.doneIntervalSec.value,
  set: v => downloadManager.updateSettings({ doneIntervalSec: Number(v) }),
})
const failoverModel = computed({
  get: () => downloadManager.autoFailover.value,
  set: v => downloadManager.updateSettings({ autoFailover: !!v }),
})
const embedModel = computed({
  get: () => downloadManager.embedLyric.value,
  set: v => downloadManager.updateSettings({ embedLyric: !!v }),
})
const lrcModel = computed({
  get: () => downloadManager.writeLrc.value,
  set: v => downloadManager.updateSettings({ writeLrc: !!v }),
})

/** 折叠状态下的一行摘要，让用户不展开也知道当前策略 */
const settingsSummary = computed(() => {
  const q = downloadManager.qualityLabel(downloadManager.defaultQuality.value)
  const parts = [q, `同时 ${downloadManager.concurrency.value} 首`]
  if (downloadManager.autoFailover.value) parts.push('自动换源')
  if (downloadManager.embedLyric.value) parts.push('内嵌歌词')
  return parts.join(' · ')
})

/** 为单个失败任务指定音质并立即重试（SelectField 直接给出值，不再是 DOM event） */
function onTaskQualityPick(task, quality) {
  if (!quality) return
  downloadManager.setTaskQuality(task.id, quality)
}

// ── 多选与清除记录 ──
//
// 「已结束」= 记录可以清掉的那些。它同时决定三件事：勾选框出现在哪些行上、
// 「全选」圈哪些、「一键清除记录」清哪些 —— 一处定义，三处共用。
function isTerminal(status) {
  return status === 'success' || status === 'failed' || status === 'stopped'
}

/** 勾选的 id。用 Set 而不是数组：勾/取消是 O(1)，且天然去重。 */
const selected = ref(new Set())
const selectedIds = computed(() => Array.from(selected.value))
const terminalTasks = computed(() => filteredTasks.value.filter(t => isTerminal(t.status)))
const allSelected = computed(() =>
  terminalTasks.value.length > 0 && terminalTasks.value.every(t => selected.value.has(t.id)))
/** 全部任务里已结束的条数（决定「一键清除记录」显不显示 —— 没有可清的就不摆按钮） */
const terminalCount = computed(() => downloadManager.tasks.value.filter(t => isTerminal(t.status)).length)

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleSelectAll() {
  selected.value = allSelected.value
    ? new Set()
    : new Set(terminalTasks.value.map(t => t.id))
}

function clearSelected() {
  downloadManager.removeTasks(selectedIds.value)
  selected.value = new Set()
}

/**
 * 一键清除记录。问一句 `confirm`：清记录不可撤销，而它与「清除选中」挨着放，
 * 误点一下就全没了 —— 确认是**界面**的责任（service 只管做）。
 */
function clearAllRecords() {
  const n = terminalCount.value
  if (!n) return
  if (!confirm(`确定清除 ${n} 条已结束的记录？（正在下载与排队中的不受影响）`)) return
  downloadManager.clearRecords()
  selected.value = new Set()
}

const currentFilter = ref('all') // 'all' | 'downloading' | 'paused' | 'completed' | 'failed'

const filterTabs = computed(() => [
  { id: 'all', name: '全部', count: downloadManager.totalCount.value },
  { id: 'downloading', name: '下载中', count: downloadManager.activeCount.value },
  { id: 'paused', name: '已暂停', count: downloadManager.pausedCount.value },
  { id: 'stopped', name: '已停止', count: stoppedCount.value },
  { id: 'completed', name: '已完成', count: downloadManager.completedCount.value },
  { id: 'failed', name: '失败', count: downloadManager.failedCount.value },
])

const stoppedCount = computed(() =>
  downloadManager.tasks.value.filter(t => t.status === 'stopped').length
)

const currentFilterName = computed(() => {
  const match = filterTabs.value.find(t => t.id === currentFilter.value)
  return match ? match.name : ''
})

const filteredTasks = computed(() => {
  const list = downloadManager.tasks.value
  if (currentFilter.value === 'downloading') {
    return list.filter(t => t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading')
  }
  if (currentFilter.value === 'paused') {
    return list.filter(t => t.status === 'paused')
  }
  if (currentFilter.value === 'stopped') {
    return list.filter(t => t.status === 'stopped')
  }
  if (currentFilter.value === 'completed') {
    return list.filter(t => t.status === 'success')
  }
  if (currentFilter.value === 'failed') {
    return list.filter(t => t.status === 'failed')
  }
  return list
})
</script>
