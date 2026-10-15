<template>
  <PageShell title="设置" desc="下载音质、文件命名、存储目录与运行日志都在这里。改完即刻生效，不需要重启。">
    <div class="space-y-4">
      <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />

      <!-- ⑤ 下载音质 -->
      <section class="rounded-xl border border-gray-200 bg-white p-4 sm:p-5">
        <h2 class="text-[15px] font-semibold text-gray-800">下载音质</h2>
        <p class="mt-1 text-[12px] text-gray-500">
          这个开关**盖住**下面两个入口各自的音质设置，不是改掉它们：改回「跟随」就能复原。
          本应用不做转码（NAS 上没有 ffmpeg），所以是「点名要某一档」，拿不到会按原顺序兜底，不会变成下载失败。
        </p>
        <div class="mt-3 grid gap-2 sm:grid-cols-3">
          <button
            v-for="o in qualityOptions"
            :key="o.value"
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="qualityValue === o.value
              ? 'border-emerald-500 bg-emerald-50'
              : 'border-gray-200 bg-white hover:border-gray-300'"
            :title="o.desc"
            @click="saveQuality(o.value)"
          >
            <div class="text-[13px] font-medium text-gray-800">{{ o.label }}</div>
            <div class="mt-1 text-[11px] leading-relaxed text-gray-500">{{ o.desc }}</div>
          </button>
        </div>
      </section>

      <!-- ② 文件命名 -->
      <section class="rounded-xl border border-gray-200 bg-white p-4 sm:p-5">
        <h2 class="text-[15px] font-semibold text-gray-800">曲库文件命名</h2>
        <p class="mt-1 text-[12px] text-gray-500">
          影响**新下载**的文件名。已经躺在曲库里的文件不会被改名（改名会让飞牛当成新歌重新扫描一遍）。
        </p>
        <div class="mt-3 grid gap-2 sm:grid-cols-2">
          <button
            v-for="o in namingOptions"
            :key="o.value"
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="namingValue === o.value
              ? 'border-emerald-500 bg-emerald-50'
              : 'border-gray-200 bg-white hover:border-gray-300'"
            @click="saveNaming(o.value)"
          >
            <div class="text-[13px] font-medium text-gray-800">{{ o.label }}</div>
            <div class="mt-1 rounded-lg bg-gray-50 px-2 py-1.5 font-mono text-[11px] text-gray-600">{{ o.sample }}</div>
          </button>
        </div>
      </section>

      <!-- ⑧ 存储目录 -->
      <section class="rounded-xl border border-gray-200 bg-white p-4 sm:p-5">
        <h2 class="text-[15px] font-semibold text-gray-800">存储目录</h2>
        <p class="mt-1 text-[12px] text-gray-500">
          缓存目录放的是「边听边存」的暂存档与试听滚动缓存，可以单独放一块盘。
          两个目录**不得相同、也不得互为父子**（清理缓存会连成品一起删掉）。
        </p>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class="text-[12px] font-medium text-gray-700">下载目录（曲库）</span>
            <input
              v-model.trim="downloadDir"
              type="text"
              spellcheck="false"
              placeholder="/vol1/Music"
              class="mt-1 w-full rounded-xl border border-gray-200 px-3 py-2 text-[13px] focus:border-emerald-500 focus:outline-none"
            />
          </label>
          <label class="block">
            <span class="text-[12px] font-medium text-gray-700">缓存目录（留空 = 默认位置）</span>
            <div class="mt-1 flex gap-2">
              <input
                v-model.trim="cacheDir"
                type="text"
                spellcheck="false"
                placeholder="/vol2/MediaCache"
                class="w-full rounded-xl border border-gray-200 px-3 py-2 text-[13px] focus:border-emerald-500 focus:outline-none"
              />
              <button
                type="button"
                class="shrink-0 rounded-xl border border-gray-200 px-3 text-[12px] text-gray-600 hover:border-gray-300"
                title="回到默认位置（跟下载目录同卷，提升是 rename，不跨盘）"
                @click="cacheDir = ''"
              >恢复默认</button>
            </div>
          </label>
        </div>
        <div class="mt-3 flex flex-wrap items-center gap-3">
          <button
            type="button"
            class="rounded-xl bg-emerald-500 px-4 py-2 text-[13px] font-medium text-white hover:bg-emerald-600 disabled:opacity-50"
            :disabled="savingDirs"
            @click="saveDirs"
          >{{ savingDirs ? '保存中…' : '保存目录' }}</button>
          <span v-if="dirError" class="text-[12px] text-red-500">{{ dirError }}</span>
        </div>
      </section>

      <!-- ③ 关于 / 导出日志 -->
      <section class="rounded-xl border border-gray-200 bg-white p-4 sm:p-5">
        <h2 class="text-[15px] font-semibold text-gray-800">关于</h2>
        <dl class="mt-2 grid gap-x-6 gap-y-1 text-[12px] text-gray-600 sm:grid-cols-2">
          <div class="flex gap-2"><dt class="text-gray-400">版本</dt><dd>{{ version || '…' }}</dd></div>
          <div class="flex gap-2"><dt class="text-gray-400">用途</dt><dd>自用；没有商用的取舍</dd></div>
        </dl>
        <div class="mt-3 flex flex-wrap items-center gap-3">
          <button
            type="button"
            class="rounded-xl border border-gray-200 px-4 py-2 text-[13px] text-gray-700 hover:border-gray-300 disabled:opacity-50"
            :disabled="exporting"
            :title="exportTitle"
            @click="exportLogs"
          >{{ exporting ? '打包中…' : '导出运行日志' }}</button>
          <span v-if="exportNote" class="text-[12px] text-gray-500">{{ exportNote }}</span>
        </div>
      </section>

    </div>
  </PageShell>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import PageShell from './PageShell.vue'
import { ConfigAPI, DownloadAPI, LogsAPI, AppAPI } from '../api/client'
import { QUALITY_OVERRIDE_OPTIONS } from '../services/qualityOverride'
import { fail, apiError } from '../services/userMsg'
import InlineNotice from './InlineNotice.vue'

// 成功/失败提示：沿用全局的 InlineNotice（AiSettingsPanel 同款），
// 不是弹 toast —— 本项目提示已经够多了，能就地显示就别再弹一层。
const notice = ref('')
const noticeOk = ref(false)
let noticeTimer = null
function flash(msg, isError = false) {
  notice.value = msg || ''
  noticeOk.value = !isError
  if (noticeTimer) clearTimeout(noticeTimer)
  if (notice.value) noticeTimer = setTimeout(() => { notice.value = '' }, 6000)
}

// 保存时要**整份带回**的原始配置 —— POST /api/config 是选择性合并，
// 但只发一个键就等于把「其它字段」交给后端的合并规则（见 LibraryManager 同款做法）。
const cfgRaw = ref(null)

const qualityOptions = QUALITY_OVERRIDE_OPTIONS
const qualityValue = ref('')

const namingOptions = [
  {
    value: 'artist-title',
    label: '歌手 - 歌名',
    sample: '周杰伦 - 晴天.flac',
  },
  {
    value: 'title-artist',
    label: '歌名 - 歌手',
    sample: '晴天 - 周杰伦.flac',
  },
]
const namingValue = ref('artist-title')

const downloadDir = ref('')
const cacheDir = ref('')
const savingDirs = ref(false)
const dirError = ref('')

const version = ref('')
const exporting = ref(false)
const exportNote = ref('')
const exportTitle = '打包日志、环境信息与配置（凭据已抹除），可以直接发给别人看'

onMounted(() => {
  load()
})

async function load() {
  try {
    const res = await ConfigAPI.get()
    const cfg = res?.data || res?.config
    if (cfg) {
      cfgRaw.value = cfg
      // download_quality_override 是三态：缺省（跟随）就是空串
      qualityValue.value = String(cfg.download_quality_override || '')
      namingValue.value = cfg.filename_format || 'artist-title'
      downloadDir.value = cfg.download_dir || ''
      // 空串 = 默认位置，与「没配」在界面上是同一件事
      cacheDir.value = cfg.cache_dir || ''
    }
  } catch (e) {
    flash(fail('读取设置', e), true)
  }
  try {
    const res = await AppAPI.getVersion()
    version.value = res?.data?.version || ''
  } catch (_) {
    // 版本号读不到不该挡住整页设置
  }
}

async function saveQuality(v) {
  const prev = qualityValue.value
  qualityValue.value = v
  try {
    const res = await ConfigAPI.save({ ...(cfgRaw.value || {}), download_quality_override: v })
    const bad = apiError(res)
    if (bad) {
      qualityValue.value = prev
      return flash(fail('保存下载音质', new Error(bad)), true)
    }
    cfgRaw.value = res?.data || cfgRaw.value
    // 让下载队列立刻按新档位走（不用等下次刷新）
    try {
      const { downloadManager } = await import('../services/downloadManager')
      downloadManager.setQualityOverride(v)
    } catch (_) {}
    flash('下载音质已更新')
  } catch (e) {
    qualityValue.value = prev
    flash(fail('保存下载音质', e), true)
  }
}

async function saveNaming(v) {
  const prev = namingValue.value
  namingValue.value = v
  try {
    const res = await ConfigAPI.save({ ...(cfgRaw.value || {}), filename_format: v })
    const bad = apiError(res)
    if (bad) {
      namingValue.value = prev
      return flash(fail('保存命名格式', new Error(bad)), true)
    }
    cfgRaw.value = res?.data || cfgRaw.value
    flash('命名格式已更新，只影响之后新下载的文件')
  } catch (e) {
    namingValue.value = prev
    flash(fail('保存命名格式', e), true)
  }
}

async function saveDirs() {
  savingDirs.value = true
  dirError.value = ''
  try {
    const res = await DownloadAPI.saveDirs({ download_dir: downloadDir.value, cache_dir: cacheDir.value })
    const bad = apiError(res)
    if (bad) {
      dirError.value = bad
      return
    }
    const d = res?.data || {}
    if (d.download_dir) downloadDir.value = d.download_dir
    cacheDir.value = d.cache_dir || ''
    dirError.value = ''
    flash('目录已保存')
  } catch (e) {
    dirError.value = fail('保存存储目录', e)
  } finally {
    savingDirs.value = false
  }
}

async function exportLogs() {
  exporting.value = true
  exportNote.value = ''
  try {
    const blob = await LogsAPI.exportZip()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `qulv-logs-${fmtStamp()}.zip`
    document.body.appendChild(a)
    a.click()
    a.remove()
    // 立刻回收：留着的话这个 blob 会一直占内存到刷新
    setTimeout(() => URL.revokeObjectURL(url), 10000)
    exportNote.value = '已下载。里面的配置凭据字段已抹除，可以放心发出去。'
  } catch (e) {
    exportNote.value = ''
    flash(fail('导出运行日志', e), true)
  } finally {
    exporting.value = false
  }
}

function fmtStamp() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}
</script>
