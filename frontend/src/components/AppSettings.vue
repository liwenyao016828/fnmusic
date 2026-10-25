<template>
  <PageShell>
    <div class="space-y-4">
      <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />

      <!-- ⑤ 下载音质
           小节简介统一走 SectionCard 的 subTitle（2026-10-18 用户口径：「去掉简介」，改成悬浮可见）。
           原来那句里还写着 Markdown 的 ** 星号，页面会照原样显示 —— 顺手去掉。 -->
      <DisclosureSection
        :open="openKey === 'quality'"
        @toggle="toggleSection('quality')"
        :summary="qualitySummary"
        :icon="AudioLines"
        title="下载音质"
        hint="这个开关盖住下面两个入口各自的音质设置，不是改掉它们：改回「跟随」就能复原。本应用不做转码（NAS 上没有 ffmpeg），所以是「点名要某一档」，拿不到会按原顺序兜底，不会变成下载失败。"
      >
        <div class="grid gap-2 sm:grid-cols-3">
          <button
            v-for="o in qualityOptions"
            :key="o.value"
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="qualityValue === o.value
              ? 'border-emerald-500/40 bg-[var(--ys-accent-soft)]'
              : 'border-[var(--ys-border)] bg-white hover:border-[var(--ys-border-strong)]'"
            :title="o.desc"
            @click="saveQuality(o.value)"
          >
            <div class="text-[13px] font-medium text-[var(--ys-text)]">{{ o.label }}</div>
            <div class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">{{ o.desc }}</div>
          </button>
        </div>
      </DisclosureSection>

      <!-- ⑥ 取音渠道（需求 m02588）：数组顺序=优先级，改动即存 -->
      <DisclosureSection
        :open="openKey === 'channels'"
        @toggle="toggleSection('channels')"
        :summary="channelsSummary"
        :icon="Route"
        title="取音渠道"
        hint="同档位不换源：三条渠道拿到的实际音质相同时，用原生那条（更稳、封面标签更全）。这里的顺序决定的是『谁先去拿到更高音质』，不是『一定要用第三方链接』。洛雪一次探测可能要几十秒，慢是正常的。"
      >
        <div class="overflow-hidden rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] bg-white">
          <TransitionGroup tag="div" name="chan">
            <div
              v-for="c in channelRows"
              :key="c.id"
              class="flex items-center gap-2.5 border-b border-[var(--ys-border)] px-3 py-2.5 last:border-b-0"
            >
              <div class="min-w-0 flex-1" :class="c.checked ? '' : 'opacity-55'">
                <div class="flex items-center gap-1.5">
                  <span class="text-[13px] font-medium text-[var(--ys-text)]">{{ c.name }}</span>
                  <span
                    v-if="c.checked"
                    class="ys-num rounded-[6px] bg-[var(--ys-surface-sunken)] px-1.5 py-0.5 text-[10px] text-[var(--ys-text-muted)]"
                  >{{ c.rank }}</span>
                  <!-- 当前选中的洛雪音源：多选=轮询 / 单选=简称 / 未选=未选择（需求 m04229） -->
                  <span
                    v-if="c.id === 'lx'"
                    class="shrink-0 rounded-[6px] bg-[var(--ys-surface-sunken)] px-1.5 py-0.5 text-[10px] text-[var(--ys-text-muted)]"
                    :class="{ 'text-[var(--ys-accent)]': lxScripts.length > 0 }"
                    :title="lxSourceTitle"
                  >{{ lxSourceLabel }}</span>
                </div>
                <div class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">{{ c.desc }}</div>
              </div>
              <div class="flex shrink-0 items-center gap-1">
                <button
                  type="button"
                  class="ys-press grid h-6 w-6 place-items-center rounded-[7px] border border-[var(--ys-border)] text-[var(--ys-text-muted)] transition-colors hover:border-[var(--ys-border-strong)] disabled:pointer-events-none disabled:opacity-30"
                  :disabled="!c.checked || c.rank <= 1"
                  title="上移（提高优先级）"
                  @click="moveChannel(c.id, -1)"
                >
                  <ChevronUp class="h-3.5 w-3.5" :stroke-width="2" />
                </button>
                <button
                  type="button"
                  class="ys-press grid h-6 w-6 place-items-center rounded-[7px] border border-[var(--ys-border)] text-[var(--ys-text-muted)] transition-colors hover:border-[var(--ys-border-strong)] disabled:pointer-events-none disabled:opacity-30"
                  :disabled="!c.checked || c.rank >= channels.length"
                  title="下移（降低优先级）"
                  @click="moveChannel(c.id, 1)"
                >
                  <ChevronDown class="h-3.5 w-3.5" :stroke-width="2" />
                </button>
                <input
                  type="checkbox"
                  class="ml-1 h-4 w-4 shrink-0 accent-[var(--ys-accent)]"
                  :checked="c.checked"
                  title="是否参与取音"
                  @change="toggleChannel(c.id)"
                />
              </div>
            </div>
          </TransitionGroup>
        </div>
      </DisclosureSection>

      <!-- ② 文件命名 -->
      <DisclosureSection
        :open="openKey === 'naming'"
        @toggle="toggleSection('naming')"
        :summary="namingSummary"
        :icon="FileAudio"
        title="曲库文件命名"
        hint="影响新下载的文件名。已经躺在曲库里的文件不会被改名（改名会让飞牛当成新歌重新扫描一遍）。"
      >
        <div class="grid gap-2 sm:grid-cols-2">
          <button
            v-for="o in namingOptions"
            :key="o.value"
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="namingValue === o.value
              ? 'border-emerald-500/40 bg-[var(--ys-accent-soft)]'
              : 'border-[var(--ys-border)] bg-white hover:border-[var(--ys-border-strong)]'"
            @click="saveNaming(o.value)"
          >
            <div class="text-[13px] font-medium text-[var(--ys-text)]">{{ o.label }}</div>
            <div class="mt-1 rounded-lg bg-[#FAFAFC] px-2 py-1.5 font-mono text-[11px] text-[var(--ys-text-muted)]">{{ o.sample }}</div>
          </button>
        </div>
      </DisclosureSection>

      <!-- ⑧ 存储目录 -->
      <DisclosureSection
        :open="openKey === 'dirs'"
        @toggle="toggleSection('dirs')"
        :summary="dirsSummary"
        :icon="FolderCog"
        title="存储目录"
        hint="缓存目录放的是「边听边存」的暂存档与试听滚动缓存，可以单独放一块盘。两个目录不得相同、也不得互为父子（清理缓存会连成品一起删掉）。"
      >
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class="text-[12px] font-medium text-gray-700">下载目录（曲库）</span>
            <div class="mt-1 flex gap-2">
              <input
                v-model.trim="downloadDir"
                type="text"
                spellcheck="false"
                placeholder="/vol1/Music"
                class="w-full rounded-xl border border-gray-200 px-3 py-2 text-[13px] focus:border-emerald-500 focus:outline-none"
              />
              <!-- 用户 2026-10-25：不要「必须手输」，点一下就从目录树里选 -->
              <button
                type="button"
                class="shrink-0 rounded-xl border border-emerald-200 bg-emerald-50 px-3 text-[12px] font-medium text-emerald-700 hover:bg-emerald-100"
                @click="dirPicker = { open: true, field: 'download', start: downloadDir }"
              >选择</button>
            </div>
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
                class="shrink-0 rounded-xl border border-emerald-200 bg-emerald-50 px-3 text-[12px] font-medium text-emerald-700 hover:bg-emerald-100"
                @click="dirPicker = { open: true, field: 'cache', start: cacheDir || downloadDir }"
              >选择</button>
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
        <NasDirPicker
          v-model:open="dirPicker.open"
          :title="dirPicker.field === 'cache' ? '选择缓存目录' : '选择下载目录（曲库）'"
          :start="dirPicker.start"
          @pick="onPickDir"
        />
      </DisclosureSection>

      <!-- 下载行为（v2.5.0 从旧「曲库管家 → 下载」标签搬来；四个开关行为原样未动） -->
      <DisclosureSection
        :open="openKey === 'behavior'"
        @toggle="toggleSection('behavior')"
        :summary="behaviorSummary"
        :icon="Download"
        title="下载行为"
        hint="这几个开关都真的控制行为（不放「点了没反应」的开关）：打开后对应的自动动作开始占磁盘和带宽。"
      >
        <div class="space-y-2">
          <div class="flex items-start justify-between gap-3 rounded-xl border border-gray-100 bg-gray-50/60 px-3 py-2.5">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="text-[13px] font-medium text-[var(--ys-text)]">收藏自动下载并绑定本地</span>
                <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono',
                               favAutoDownload ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-gray-200 text-gray-400']">
                  {{ favAutoDownload ? '已开启' : '已关闭' }}
                </span>
              </div>
              <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
                收藏/加歌单的在线曲目会后台下到 NAS，之后听硬盘那份。
                <span class="text-amber-600">占磁盘和带宽（无损一首几十兆）。</span>
              </p>
            </div>
            <button
              role="switch"
              :aria-checked="favAutoDownload ? 'true' : 'false'"
              :disabled="favAutoBusy"
              @click="toggleFavAutoDownload"
              :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                       favAutoDownload ? 'bg-emerald-500' : 'bg-black/[0.12]']">
              <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                             favAutoDownload ? 'translate-x-5' : '']" />
            </button>
          </div>

          <div class="flex items-start justify-between gap-3 rounded-xl border border-gray-100 bg-gray-50/60 px-3 py-2.5">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="text-[13px] font-medium text-[var(--ys-text)]">边听边下</span>
                <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono',
                               teeEnabled ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-gray-200 text-gray-400']">
                  {{ teeEnabled ? '已开启' : '已关闭' }}
                </span>
              </div>
              <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
                边听边写进磁盘，整首播完提升进曲库。
                <span class="text-gray-400">半截文件会丢弃。</span>
              </p>
            </div>
            <button
              role="switch"
              :aria-checked="teeEnabled ? 'true' : 'false'"
              :disabled="teeBusy"
              @click="toggleTee"
              :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                       teeEnabled ? 'bg-emerald-500' : 'bg-black/[0.12]']">
              <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                             teeEnabled ? 'translate-x-5' : '']" />
            </button>
          </div>

          <div class="rounded-xl border border-gray-100 bg-gray-50/60 px-3 py-2.5">
            <div class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <span class="text-[13px] font-medium text-[var(--ys-text)]">下载时附带</span>
                <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
                  只影响曲率触发的下载。<span class="text-amber-600">缺省都是开</span>。
                </p>
              </div>
            </div>
            <div class="mt-2 space-y-2 border-t border-gray-100 pt-2">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="text-xs font-medium text-gray-700">歌词</div>
                  <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
                    内嵌进标签，另写同名 <code class="font-mono">.lrc</code>。
                  </p>
                </div>
                <button
                  role="switch"
                  :aria-checked="lyricAutoDownload ? 'true' : 'false'"
                  :disabled="lyricBusy"
                  @click="toggleLyric"
                  :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                           lyricAutoDownload ? 'bg-emerald-500' : 'bg-black/[0.12]']">
                  <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                                 lyricAutoDownload ? 'translate-x-5' : '']" />
                </button>
              </div>
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="text-xs font-medium text-gray-700">封面内嵌</div>
                  <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
                    写进文件标签，否则飞牛里没封面。
                  </p>
                </div>
                <button
                  role="switch"
                  :aria-checked="coverEmbed ? 'true' : 'false'"
                  :disabled="coverBusy"
                  @click="toggleCover"
                  :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                           coverEmbed ? 'bg-emerald-500' : 'bg-black/[0.12]']">
                  <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                                 coverEmbed ? 'translate-x-5' : '']" />
                </button>
              </div>
            </div>
          </div>
        </div>
      </DisclosureSection>

      <!-- 版本更新：检查 GitHub Release、下载 fpk、自动安装。后端执行，前端只展示状态。 -->
      <UpdateCard />

      <!-- ③ 关于 / 导出日志：不再是一张「带图标方块」的卡（那套视觉语言属于内容分区），
           收尾信息用一个安静的信息块：版本一行 + 两个轻按钮。 -->
      <section class="mt-1 rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-[#FBFBFD] px-4 py-3.5 sm:px-5">
        <div class="flex flex-wrap items-center gap-x-5 gap-y-1 text-[11.5px] text-[var(--ys-text-muted)]">
          <span class="flex items-center gap-1.5">
            <Info class="h-3.5 w-3.5 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
            曲率 <span class="font-mono">{{ version || '…' }}</span>
          </span>
          <span>自用；没有商用的取舍</span>
        </div>
        <div class="mt-2.5 flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="ys-press rounded-[9px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
            :disabled="exporting"
            :title="exportTitle"
            @click="exportLogs"
          >{{ exporting ? '打包中…' : '导出运行日志' }}</button>
          <button
            type="button"
            class="ys-press rounded-[9px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
            title="查看软件功能、第三方音源与内容授权的使用说明"
            @click="emit('open-disclaimer')"
          >免责声明与合规</button>
          <span v-if="exportNote" class="text-[12px] text-[var(--ys-text-faint)]">{{ exportNote }}</span>
        </div>
      </section>

    </div>
  </PageShell>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import PageShell from './PageShell.vue'
import DisclosureSection from './DisclosureSection.vue'
import { AudioLines, ChevronDown, ChevronUp, Download, FileAudio, FolderCog, Info, Route } from 'lucide-vue-next'
import { ConfigAPI, DownloadAPI, LogsAPI, AppAPI, ChartInjectAPI, SourcesAPI, DEFAULT_AUTO_DOWNLOAD_CHANNELS } from '../api/client'
import { QUALITY_OVERRIDE_OPTIONS } from '../services/qualityOverride'
import { fail, apiError } from '../services/userMsg'
import InlineNotice from './InlineNotice.vue'
import NasDirPicker from './NasDirPicker.vue'
import UpdateCard from './UpdateCard.vue'

/** 条款弹窗由 App.vue 持有（openDisclaimer），这里只负责把「想看条款」这件事报上去。 */
const emit = defineEmits(['open-disclaimer'])

// 成功/失败提示：沿用全局的 InlineNotice（AiSettingsPanel 同款），
// 不是弹 toast —— 本项目提示已经够多了，能就地显示就别再弹一层。
// 2026-10-19 渐进式披露：设置页默认全部收起，一次只展开一组 ——
// 版面只留「5 行摘要」，用户点哪行才长出哪块表单。
const openKey = ref('')
function toggleSection(k) {
  openKey.value = openKey.value === k ? '' : k
}

const qualitySummary = computed(() => {
  const o = qualityOptions.find((x) => x.value === qualityValue.value)
  return o ? o.label : '跟随各处设置'
})
const namingSummary = computed(() => {
  const o = namingOptions.find((x) => x.value === namingValue.value)
  return o ? o.sample || o.label : '默认命名'
})
const dirsSummary = computed(() => downloadDir.value || '默认位置')
const behaviorSummary = computed(() => {
  const on = []
  if (favAutoDownload.value) on.push('收藏自动下载')
  if (teeEnabled.value) on.push('边听边下')
  if (lyricAutoDownload.value) on.push('歌词')
  if (coverEmbed.value) on.push('封面')
  return on.length ? on.join(' · ') : '全部关闭'
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
/** 目录选择弹窗：{ open, field: 'download'|'cache', start } */
const dirPicker = ref({ open: false, field: 'download', start: '' })

function onPickDir(p) {
  if (!p) return
  if (dirPicker.value.field === 'cache') cacheDir.value = p
  else downloadDir.value = p
}
const cacheDir = ref('')
const savingDirs = ref(false)
const dirError = ref('')

// ── 下载行为四开关（v2.5.0 从旧 LibraryManager 搬来，逻辑原样）──
// 歌词/封面缺省「开」（后端 pkg/config LyricAutoDownloadOn 同语义：没这个键就是开），
// 收藏自动下载/边听边下缺省「关」（占磁盘和带宽，宁可不自动生效）。
const favAutoDownload = ref(false)
const favAutoBusy = ref(false)
const teeEnabled = ref(false)
const teeBusy = ref(false)
const lyricAutoDownload = ref(true)
const lyricBusy = ref(false)
const coverEmbed = ref(true)
const coverBusy = ref(false)

function asDefaultOn(v) {
  return v === undefined || v === null ? true : !!v
}

// toggleDownloadSwitch 抽出来是因为这四个开关是同一套动作：
// 乐观更新 → 写服务端 → 失败回滚（界面不能骗人）。整份带回其它字段（选择性合并）。
async function toggleDownloadSwitch(onRef, busyRef, key) {
  const next = !onRef.value
  onRef.value = next
  busyRef.value = true
  try {
    const res = await ConfigAPI.save({ ...(cfgRaw.value || {}), [key]: next })
    const bad = apiError(res)
    if (bad) {
      onRef.value = !next
      flash(fail('保存下载行为', new Error(bad)), true)
      return
    }
    cfgRaw.value = res?.data || cfgRaw.value
  } catch (e) {
    onRef.value = !next
    flash(fail('保存下载行为', e), true)
  } finally {
    busyRef.value = false
  }
}

function toggleFavAutoDownload() { return toggleDownloadSwitch(favAutoDownload, favAutoBusy, 'fav_auto_download') }
function toggleTee() { return toggleDownloadSwitch(teeEnabled, teeBusy, 'tee_enabled') }
function toggleLyric() { return toggleDownloadSwitch(lyricAutoDownload, lyricBusy, 'lyric_auto_download') }
function toggleCover() { return toggleDownloadSwitch(coverEmbed, coverBusy, 'cover_embed') }

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
      // 下载行为四开关：歌词/封面缺省开，另两个缺省关
      favAutoDownload.value = !!cfg.fav_auto_download
      teeEnabled.value = !!cfg.tee_enabled
      lyricAutoDownload.value = asDefaultOn(cfg.lyric_auto_download)
      coverEmbed.value = asDefaultOn(cfg.cover_embed)
      // 取音渠道：没配/空数组都按后端默认显示（client.js DEFAULT_AUTO_DOWNLOAD_CHANNELS 同口径）
      channels.value = Array.isArray(cfg.auto_download_channels) && cfg.auto_download_channels.length
        ? [...cfg.auto_download_channels]
        : [...DEFAULT_AUTO_DOWNLOAD_CHANNELS]
    }
  } catch (e) {
    flash(fail('读取设置', e), true)
  }
  try {
    // 洛雪音源列表：只要「启用中的」与它们的顺序（active_source_ids 就是轮询顺序）
    const res = await SourcesAPI.list()
    const data = res?.data || res || {}
    const list = Array.isArray(data.sources) ? data.sources : []
    const ids = Array.isArray(data.active_source_ids) ? data.active_source_ids : []
    const byId = new Map(list.map((x) => [x.id, x]))
    // 按名字去重：两个源同名会落成同一个脚本文件（真机上就有两个都叫
    // 「全豆要[聚合音源]」），实际只轮询一份 —— 数出来 6 个会是假的多。
    const seen = new Set()
    const names = []
    for (const id of ids) {
      const src = byId.get(id)
      if (!src || typeof src.name !== 'string') continue
      const name = src.name.trim()
      if (!name || seen.has(name)) continue
      seen.add(name)
      names.push(shortLxName(name))
    }
    lxScripts.value = names
  } catch (_) {
    // 音源列表读不到不该挡住整页设置：显示「未选择」比整页报错有用
    lxScripts.value = []
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

// ── 取音渠道（需求 m02588）──
//
// `channels` 是**有序数组**，顺序=收藏自动下载的取音优先级（与后端
// pkg/config/channels.go 同一口径）。勾选/排序都**立刻保存**（交互对齐 saveQuality）。
// 关键约束：空数组后端会回落默认 lx→dl→native，用户「以为留了一条」是错觉 ——
// 所以剩最后一条被取消时拦住并说明，而不是发一个空数组出去。
// shortLxName 把脚本名压成一个能塞进标签里的简称。
//
// 用户导入的脚本名五花八门（`全豆要_聚合音源_.js` / `念心音源.js` / `长青SVIP音源.js`），
// 原样显示又长又闹；但**不能乱改**——它得和用户在音源页看到的名字对得上。
// 所以只做「去 .js / 去尾部括注 / 去音源这类通用后缀 / 去首尾分隔符」。
function shortLxName(name) {
  let s = String(name || '').replace(/\.js$/i, '').trim()
  s = s.replace(/[（(][^（()）]*[）)]$/, '').trim()
  s = s.replace(/[_·\-\s]*(音源脚本|音乐源|音源)[_·\-\s]*$/,'').trim()
  s = s.replace(/^[_·\-\s]+|[_·\-\s]+$/g, '')
  return s || String(name || '').replace(/\.js$/i, '')
}

const CHANNEL_META = {
  lx: { short: '洛雪', name: '洛雪音源脚本', desc: '第三方聚合，常是唯一能真给无损的一条' },
  dl: { short: 'musicdl', name: 'musicdl 外挂池', desc: '咪咕/千千/B站等，配置里勾的那几家' },
  native: { short: '原生', name: '曲目原生解析', desc: '曲目自己平台的原生解析（网易云/QQ 等；免登录对收费曲目会静默降档）' },
}
const channels = ref([...DEFAULT_AUTO_DOWNLOAD_CHANNELS])
// 当前选中的洛雪音源脚本（简称，按用户排的轮询顺序）。
//
// 为什么设置页要自己读一次：这是**另一个维度**的选择 —— `channels` 决定「先问哪条渠道」，
// 而这里决定「洛雪这条渠道内部问哪几个脚本、按什么顺序」。用户改完音源页的勾选，
// 回到这里应当立刻看到「轮询」，而不是等重启（后端每次取链都现取名单，同一个口径）。
const lxScripts = ref([])
const lxSourceLabel = computed(() => {
  const n = lxScripts.value.length
  if (n === 0) return '未选择'
  if (n === 1) return lxScripts.value[1 - 1]
  return `轮询 ${n} 个`
})
const lxSourceTitle = computed(() => (lxScripts.value.length
  ? `洛雪按这个顺序挨个问、取实际档位最高的一条：${lxScripts.value.join(' → ')}`
  : '还没选洛雪音源：先去「音源」页勾选'))
const channelsSummary = computed(() =>
  channels.value.map((id) => {
    const short = CHANNEL_META[id]?.short || id
    // 洛雪那条把「内部怎么走」也带上：用户一眼能看到是轮询还是只问一个。
    return id === 'lx' ? `${short}·${lxSourceLabel.value}` : short
  }).join(' → '))
// 行列表 = 已勾选的按优先级在前，未勾选的按默认顺序排在后面（灰显、不可排序）
const channelRows = computed(() => {
  const meta = (id) => CHANNEL_META[id] || { short: id, name: id, desc: '' }
  const rows = channels.value.map((id, i) => ({ id, ...meta(id), checked: true, rank: i + 1 }))
  for (const id of DEFAULT_AUTO_DOWNLOAD_CHANNELS) {
    if (!channels.value.includes(id)) rows.push({ id, ...meta(id), checked: false, rank: 0 })
  }
  return rows
})

function toggleChannel(id) {
  const cur = channels.value
  if (cur.includes(id)) {
    if (cur.length === 1) {
      // 不发送空数组：清空=回落默认，得让用户知道
      return flash('至少留一条，否则回到默认 lx→dl→native', true)
    }
    return saveChannels(cur.filter((x) => x !== id))
  }
  saveChannels([...cur, id]) // 新勾选的排最后（优先级最低，别悄悄抢首位）
}

function moveChannel(id, dir) {
  const cur = channels.value
  const i = cur.indexOf(id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= cur.length) return
  const next = [...cur]
  next.splice(i, 1)
  next.splice(j, 0, id)
  saveChannels(next)
}

async function saveChannels(next) {
  const prev = channels.value
  channels.value = next // 乐观更新：顺序/勾选先落在界面上，失败再回滚
  try {
    const res = await ChartInjectAPI.saveAutoDownloadChannels(next)
    const bad = apiError(res)
    if (bad) {
      channels.value = prev
      return flash(fail('保存取音渠道', new Error(bad)), true)
    }
    cfgRaw.value = res?.data || cfgRaw.value
    flash('取音渠道已更新')
  } catch (e) {
    channels.value = prev
    flash(fail('保存取音渠道', e), true)
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

<style scoped>
/* 渠道行「上移/下移」的换位过渡：transform 一次，走全局 --ys-ease/--ys-dur-2；
   列表恒为三条（只有换位没有增删），所以只需要 move 过渡，无 enter/leave。 */
.chan-move {
  transition: transform var(--ys-dur-2) var(--ys-ease);
}
</style>
