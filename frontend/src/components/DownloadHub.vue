<template>
  <PageShell>
    <template #title-extra>
      <span class="rounded-full border border-emerald-200 bg-emerald-50 px-2 py-0.5 font-mono text-[10px] text-emerald-600">
        队列 · 歌单 · 自动记录
      </span>
    </template>

    <template #metrics>
      <!-- 指标随分段切换（口径与旧「曲库管家·下载页」一致）；自动记录段不摆大卡（表里都有） -->
      <div v-if="metrics.length" class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div v-for="m in metrics" :key="m.label" class="rounded-xl border border-gray-200 bg-white px-[17px] py-[15px]">
          <b class="block text-[24px] font-bold" :class="m.color">{{ m.value }}</b>
          <span class="text-[12px] text-gray-400">{{ m.label }}</span>
        </div>
      </div>
    </template>

    <!-- 队列 / 歌单：两个子视图组件本身没动（v2.1.26 起就是同一队列的两个入口） -->
    <DownloadQueueView v-if="sub === 'queue'" embedded :active-source="activeSource" />
    <PlaylistDownload v-else-if="sub === 'playlist'" embedded />

    <template v-else>
      <!-- ══ 自动下载记录 ══ -->
        <div class="space-y-3">
          <div class="rounded-xl border border-gray-200 bg-white p-4">
            <div class="flex flex-wrap items-start gap-x-3 gap-y-2">
              <div class="min-w-0">
                <h3
                  class="text-[13px] font-bold text-gray-800"
                  title="飞牛页面里收藏 / 加歌单 / 点下载触发的那批。成功与失败都记。"
                >自动下载记录</h3>
              </div>
              <div class="ml-auto flex flex-wrap items-center gap-1.5">
                <button
                  @click="loadAutoDownload"
                  :disabled="autoDownloadLoading"
                  class="rounded-lg border border-gray-200 bg-white px-2.5 py-1 text-[11px] font-bold text-gray-500 transition-colors hover:border-emerald-300 hover:text-emerald-600 disabled:opacity-50"
                >{{ autoDownloadLoading ? '读取中…' : '刷新' }}</button>
                <button
                  @click="clearAutoDownload(selectedAutoIds)"
                  :disabled="!autoDownloadSelected.size"
                  class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-rose-300 hover:bg-rose-50 hover:text-rose-600 disabled:opacity-40"
                >清除选中<template v-if="autoDownloadSelected.size">（{{ autoDownloadSelected.size }}）</template></button>
                <button
                  @click="clearAutoDownload([])"
                  :disabled="!autoDownloadRecords.length"
                  class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-bold text-gray-600 transition-colors hover:border-rose-300 hover:bg-rose-50 hover:text-rose-600 disabled:opacity-40"
                >一键清除</button>
              </div>
            </div>
            <!-- 「没有记录」与「功能没起来」必须分开说：界面上长得一样会让人以为功能坏了 -->
            <p v-if="autoDownloadAvailable === false" class="mt-2 text-[11px] text-amber-600">
              记录功能本次没起来（数据目录不可写）—— 下面是空的，但不代表你没下过歌。
            </p>
            <p v-else-if="!autoDownloadRecords.length" class="mt-2 text-[11px] text-gray-400">
              还没有自动下载记录。在飞牛音乐里收藏一首在线歌曲试试。
            </p>
          </div>

          <div v-if="autoDownloadRecords.length" class="overflow-hidden rounded-xl border border-gray-200 bg-white">
            <!--
              手机上 `table-fixed`：列宽由容器决定，表格**永远等于屏宽**（与「本地音乐」那张表
              同一个做法与理由，见 NasExplorer 里那段注释）。「触发」列窄屏直接不显示 ——
              它是最不重要的一列，而挤掉它的代价是别的列全部截断。
            -->
            <table class="w-full table-fixed text-[11.5px] sm:table-auto">
              <thead class="bg-gray-50 text-gray-400">
                <tr>
                  <th class="w-9 px-2 py-2">
                    <input type="checkbox" class="align-middle" :checked="allAutoSelected" @change="toggleAllAuto" />
                  </th>
                  <th class="px-2 py-2 text-left font-medium">时间</th>
                  <th class="px-2 py-2 text-left font-medium">歌曲</th>
                  <th class="px-2 py-2 text-left font-medium">实际取音</th>
                  <th class="hidden px-2 py-2 text-left font-medium sm:table-cell">触发</th>
                  <th class="px-2 py-2 text-left font-medium">结果</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in autoDownloadRecords" :key="r.id" class="border-t border-gray-50 align-top">
                  <td class="px-2 py-2">
                    <input type="checkbox" class="align-middle" :checked="autoDownloadSelected.has(r.id)"
                      @change="toggleAutoSelect(r.id)" />
                  </td>
                  <td class="whitespace-nowrap px-2 py-2 text-gray-400">{{ fmtAutoTime(r.at) }}</td>
                  <td class="px-2 py-2">
                    <div class="max-w-[240px] truncate font-medium text-gray-800" :title="r.title">{{ r.title || '（无标题）' }}</div>
                    <div class="max-w-[240px] truncate text-[10.5px] text-gray-400">{{ r.artist }}</div>
                  </td>
                  <td class="px-2 py-2 text-gray-500">
                    <template v-if="r.source">{{ platformName(r.source) }}</template>
                    <template v-else>—</template>
                    <span v-if="r.quality" class="ml-1 font-mono text-[10.5px] text-emerald-600">{{ r.quality }}</span>
                  </td>
                  <td class="hidden px-2 py-2 text-gray-500 sm:table-cell">{{ autoTriggerLabel(r.trigger) }}</td>
                  <td class="px-2 py-2">
                    <span v-if="r.ok" class="rounded-md bg-emerald-50 px-1.5 py-0.5 text-emerald-600">已下载</span>
                    <span v-else class="rounded-md bg-rose-50 px-1.5 py-0.5 text-rose-600" :title="r.error">失败</span>
                    <div v-if="!r.ok && r.error" class="mt-1 max-w-[300px] text-[10.5px] leading-snug text-rose-500">{{ r.error }}</div>
                    <div v-if="r.path" class="mt-0.5 max-w-[300px] truncate font-mono text-[10.5px] text-gray-400" :title="r.path">{{ r.path }}</div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
    </template>
  </PageShell>
</template>

<script setup>
/**
 * 下载中心（DownloadHub）—— 新框架 v2.4.x 从「曲库管家」拆出来的独立视图。
 *
 * 三个分段（队列 / 歌单 / 自动记录）由壳上的 SegmentedControl 切换，
 * 当前分段读 navBus 的 segmentOf('download') —— 深链 navigateTo('download','autodl')
 * 或手机底部标签进来都能落在正确的段上。
 *
 * 「下载行为」四个开关（收藏自动下载 / 边听边下 / 歌词 / 封面内嵌）
 * 已移到「系统 → 设置」的下载行为区 —— 它们是**全局配置**而不是这一页的操作，
 * 放在下载页会让人以为只对当前队列生效。
 */
import { computed, onMounted, ref, watch } from 'vue'
import PageShell from './PageShell.vue'
import DownloadQueueView from './DownloadQueueView.vue'
import PlaylistDownload from './PlaylistDownload.vue'
import { segmentOf } from '../services/navBus'
import { downloadManager } from '../services/downloadManager'
import { AutoDownloadAPI } from '../api/client'
import { platformName } from '../services/libraryFields'
import { fail as failText } from '../services/userMsg'
import { ok as notifyOk, fail as notifyFail } from '../services/feedback'

const props = defineProps({
  /** 当前音源元信息（队列视图解析直链用），由壳传入 */
  activeSource: Object,
})

const sub = computed(() => segmentOf('download'))

// ── 指标行：队列 / 歌单各看各的口径（并成一页了，数字别混在一排）──
const metrics = computed(() => {
  if (sub.value === 'playlist') {
    const rate = downloadManager.recentSuccessRate.value
    return [
      { label: '下载中', value: downloadManager.downloadingCount.value, color: 'text-blue-600' },
      { label: '等待中', value: downloadManager.pendingCount.value, color: 'text-gray-800' },
      { label: '失败待处理', value: downloadManager.failedCount.value, color: 'text-rose-600' },
      // 没有样本时显示「—」而不是 0%，否则会让人以为全失败了
      { label: '近 24h 成功率', value: rate == null ? '—' : `${rate}%`, color: 'text-emerald-600' },
    ]
  }
  if (sub.value === 'queue') {
    return [
      { label: '全部任务', value: downloadManager.totalCount.value, color: 'text-gray-800' },
      { label: '进行中', value: downloadManager.activeCount.value, color: 'text-emerald-600' },
      { label: '已暂停', value: downloadManager.pausedCount.value, color: 'text-amber-600' },
      { label: '失败', value: downloadManager.failedCount.value, color: 'text-rose-600' },
    ]
  }
  return []
})

// ── 自动下载记录（见 api/client.js 的 AutoDownloadAPI）──
//
// 这些下载发生在飞牛官方页面上（收藏 / 加歌单 / 点「下载到曲率」），
// 本前端看不见它们 —— 只有拦截层知道。所以记录由后端记、这里只读与清
// （见 pkg/online/autolog.go）。
const autoDownloadRecords = ref([])
const autoDownloadAvailable = ref(true)
const autoDownloadLoading = ref(false)
/** 多选用的 id 集合。用 Set 而不是数组：勾选/取消是 O(1)，且天然去重。 */
const autoDownloadSelected = ref(new Set())

const selectedAutoIds = computed(() => Array.from(autoDownloadSelected.value))
const allAutoSelected = computed(() =>
  autoDownloadRecords.value.length > 0 &&
  autoDownloadRecords.value.every((r) => autoDownloadSelected.value.has(r.id)))

/** 触发来源翻人话。与 pkg/online 的 Trigger* 常量一一对应。 */
function autoTriggerLabel(t) {
  return { fav: '收藏', playlist: '加歌单', page: '页面点下载' }[t] || t || '自动'
}

/** 记录时间：今天的只给时分秒，更早的带上月日 —— 一屏里最常看的是「刚刚那几次」。 */
function fmtAutoTime(sec) {
  if (!sec) return ''
  const d = new Date(sec * 1000)
  const p = (n) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  const today = new Date()
  const sameDay = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate()
  return sameDay ? hm : `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
}

async function loadAutoDownload() {
  autoDownloadLoading.value = true
  try {
    const res = await AutoDownloadAPI.records(200)
    const d = res?.data || {}
    autoDownloadRecords.value = d.list || []
    autoDownloadAvailable.value = d.available !== false
    // 选中的 id 里已经不存在的要摘掉（刚清过、或被上限挤掉了），
    // 否则「清除选中（3）」会数到三条幽灵记录，点了也删不掉任何东西。
    const live = new Set(autoDownloadRecords.value.map((r) => r.id))
    autoDownloadSelected.value = new Set(selectedAutoIds.value.filter((id) => live.has(id)))
  } catch (e) {
    autoDownloadAvailable.value = false
    notifyFail(failText('读取自动下载记录', e))
  } finally {
    autoDownloadLoading.value = false
  }
}

function toggleAutoSelect(id) {
  const next = new Set(autoDownloadSelected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  autoDownloadSelected.value = next
}

function toggleAllAuto() {
  autoDownloadSelected.value = allAutoSelected.value
    ? new Set()
    : new Set(autoDownloadRecords.value.map((r) => r.id))
}

/**
 * 清除记录。`ids` 为空 = 全清（那一键）。
 *
 * 这里问一句 `confirm`：删记录不可撤销，而两个按钮挨着放，误点一下就没了。
 * 后端不做二次确认（它只是接口），确认是**界面**的责任。
 */
async function clearAutoDownload(ids) {
  const n = ids.length
  if (!confirm(n ? `确定清除选中的 ${n} 条记录？` : '确定清除全部自动下载记录？')) return
  try {
    const res = await AutoDownloadAPI.clear(ids)
    const removed = res?.data?.removed ?? 0
    notifyOk(`已清除 ${removed} 条记录`)
    autoDownloadSelected.value = new Set()
    await loadAutoDownload()
  } catch (e) {
    notifyFail(failText('清除自动下载记录', e))
  }
}

// 进「自动记录」段才拉（懒加载）；深链直接落在这一段时首屏就要拉一次
watch(sub, (s) => { if (s === 'autodl') loadAutoDownload() })
onMounted(() => { if (sub.value === 'autodl') loadAutoDownload() })
</script>
