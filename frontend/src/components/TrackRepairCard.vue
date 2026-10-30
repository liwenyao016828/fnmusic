<script setup>
/**
 * 歌曲修复（2026-10-28 用户需求，替换原「文件名体检」卡片的位置）。
 *
 * 用户原话：「文件名体检改为歌曲修复，拿来给用户单独修复歌词、封面、调整歌词时间段，
 * 还有一些可以设置的等」—— 所以这里不是体检，是**逐首动手修**：
 *
 *   · 歌词：读当前歌词（外挂 .lrc 优先，其次内嵌）→ 可改时间轴 / 可在线找词 / 写回文件
 *   · 封面：看当前封面 → 按歌名/歌手在线找候选 → 选定写入
 *   · 标签：歌名 / 歌手 / 专辑就地改，写回文件（空字段不会被清空，后端 mergeTextFields 兜底）
 *
 * 与「文件名体检」的分工：体检负责**找出**有问题的文件并批量修文件名；这里是用户
 * 已经知道哪首歌有问题，进来单独修内容。列表也能按「缺歌词 / 缺封面」筛出来。
 *
 * 列表数据用 /api/library/index/list（曲库索引，翻页 + 关键词），不走目录遍历 ——
 * 目录里上万首时遍历一遍要几十秒，索引是现成的。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { Search, Wand2, RefreshCw, Image as ImageIcon, Loader2, ChevronLeft, ChevronRight, ChevronDown, ShieldAlert } from 'lucide-vue-next'
import SectionCard from './SectionCard.vue'
import SideSheet from './SideSheet.vue'
import LibraryHealthCard from './LibraryHealthCard.vue'
import { TidyAPI, TagsAPI, NasAPI } from '../api/client'
import { apiError, fail as failText, describeError } from '../services/userMsg'
import { loadPref, savePref } from '../services/prefs'
import { parseLyric, offsetLyric } from '../services/lyricLine'

const props = defineProps({
  dir: { type: String, default: '' },
  flash: { type: Function, default: () => {} },
})

const PAGE_SIZE = 30

// ── 列表 ──
const keyword = ref(loadPref('repair_kw', ''))
const filter = ref(loadPref('repair_filter', 'all')) // all | no_lyric | no_cover
const rows = ref([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const loadErr = ref('')

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

const FILTERS = [
  { id: 'all', name: '全部' },
  { id: 'no_lyric', name: '缺歌词' },
  { id: 'no_cover', name: '缺封面' },
]

async function loadList(keepPage = false) {
  if (!props.dir) return
  if (!keepPage) page.value = 1
  loading.value = true
  loadErr.value = ''
  try {
    // 关键词与筛选都交给服务端（/library/index/list?q=&filter=）：
    // 前端「本页内过滤」会让 total 与页数对不上，翻到后面整页可能是空的。
    const res = await TidyAPI.searchIndex({
      dir: props.dir,
      offset: (page.value - 1) * PAGE_SIZE,
      limit: PAGE_SIZE,
      q: keyword.value.trim() || undefined,
      filter: filter.value === 'all' ? undefined : filter.value,
    })
    const data = res?.data || {}
    rows.value = Array.isArray(data.items) ? data.items : []
    total.value = Number(data.total) || 0
    // 搜完总数变少导致当前页越界时，自动退到最后一页（不要停在空白页）
    const last = Math.max(1, Math.ceil(total.value / PAGE_SIZE))
    if (page.value > last) {
      page.value = last
      await loadList(true)
      return
    }
  } catch (e) {
    loadErr.value = describeError(e) || '读取曲库索引失败'
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// 服务端已按 q/filter 筛过，这里不再重复过滤
const shown = computed(() => rows.value)

function onKeyword() {
  savePref('repair_kw', keyword.value)
  // 输入防抖：每敲一个字打一次接口没必要，300ms 后的那次才是用户要的
  if (kwTimer) clearTimeout(kwTimer)
  kwTimer = setTimeout(() => loadList(false), 300)
}
let kwTimer = null
function setFilter(id) {
  filter.value = id
  savePref('repair_filter', id)
  loadList(false)
}
function goPage(delta) {
  const next = page.value + delta
  if (next < 1 || next > pageCount.value) return
  page.value = next
  loadList(true)
}

watch(() => props.dir, () => loadList(false))
onMounted(() => loadList(false))

// ── 单曲修复面板 ──
const open = ref(false)
const song = ref(null)
const tab = ref('lyric') // lyric | cover | tag
const busy = ref(false)
const healthOpen = ref(false)

const lyricText = ref('')
const lyricSource = ref('')
const lyricErr = ref('')
const lyricOffset = ref(0) // 秒，正数=整体延后，负数=提前
const embedLyric = ref(true)
const writeLrc = ref(true)

const tags = ref({ title: '', artist: '', album: '' })
const coverUrl = ref('')
const coverCandidates = ref([])
const coverBusy = ref(false)

const lyricPreview = computed(() => {
  const lrc = offsetLyric(lyricText.value, lyricOffset.value)
  return parseLyric(lrc).slice(0, 12)
})

function openSong(row) {
  song.value = row
  tab.value = 'lyric'
  lyricText.value = ''
  lyricSource.value = ''
  lyricErr.value = ''
  lyricOffset.value = 0
  // 文件里已经内嵌的封面：直接用 /api/nas/cover 读出来展示，用户能先看清现状再决定换不换
  coverUrl.value = row.has_cover ? NasAPI.coverUrl(row.path) : ''
  coverCandidates.value = []
  tags.value = { title: row.title || '', artist: row.artist || '', album: row.album || '' }
  open.value = true
  loadLyric()
}

async function loadLyric() {
  if (!song.value) return
  busy.value = true
  lyricErr.value = ''
  try {
    const res = await TagsAPI.read(song.value.path)
    const d = res?.data || {}
    tags.value = { title: d.title || song.value.title || '', artist: d.artist || song.value.artist || '', album: d.album || song.value.album || '' }
    lyricText.value = ''
    lyricSource.value = ''
    // /api/nas/tags 只回「有没有词」，不回歌词本身 —— 正文走 /api/nas/lyric
    // 那一套（外挂 .lrc/.txt 优先，其次内嵌），与播放器读词同一条路，
    // 用户在这里看到的就是将来播放时用的那份。
    if (d.has_lyric) {
      const lrc = await NasAPI.lyric(song.value.path)
      if (lrc?.code === 200 && lrc.data?.lyric) {
        lyricText.value = lrc.data.lyric
        lyricSource.value = lrc.data.source === 'file' ? '外挂歌词文件' : '文件内嵌歌词'
      }
    }
  } catch (e) {
    lyricErr.value = describeError(e) || '读取文件信息失败'
  } finally {
    busy.value = false
  }
}

async function fetchLyricOnline() {
  if (!song.value) return
  busy.value = true
  lyricErr.value = ''
  try {
    const res = await TidyAPI.lyricFetch({ dir: props.dir, paths: [song.value.path] })
    const hit = (res?.data?.items || [])[0]
    if (hit?.lyric) {
      lyricText.value = hit.lyric
      lyricSource.value = '在线找词'
      props.flash('已找到歌词，确认后点「写入文件」')
    } else {
      lyricErr.value = hit?.note || '没找到匹配的歌词'
    }
  } catch (e) {
    lyricErr.value = describeError(e) || '在线找词失败'
  } finally {
    busy.value = false
  }
}

async function saveLyric() {
  if (!song.value) return
  busy.value = true
  try {
    const res = await TagsAPI.write({
      path: song.value.path,
      lyric: offsetLyric(lyricText.value, lyricOffset.value),
      write_lrc: writeLrc.value,
      embed_lyric: embedLyric.value,
    })
    const d = res?.data || {}
    if (d.cover_error) props.flash(`歌词已写入（封面失败：${d.cover_error}）`, true)
    else props.flash('歌词已写入文件')
    savePref('repair_embed_lyric', embedLyric.value ? '1' : '0')
    savePref('repair_write_lrc', writeLrc.value ? '1' : '0')
    loadList(true)
    if (open.value) open.value = false
  } catch (e) {
    props.flash(failText('写入歌词', e, '写入失败'), true)
  } finally {
    busy.value = false
  }
}

async function findCovers() {
  if (!song.value) return
  coverBusy.value = true
  try {
    const res = await TagsAPI.coverCandidates({
      keyword: [tags.value.artist, tags.value.title].filter(Boolean).join(' '),
      name: tags.value.title,
      artist: tags.value.artist,
      limit: 8,
    })
    coverCandidates.value = res?.data?.candidates || []
    if (!coverCandidates.value.length) props.flash('没搜到封面候选', true)
  } catch (e) {
    props.flash(failText('找封面', e, '找封面失败'), true)
  } finally {
    coverBusy.value = false
  }
}

async function applyCover(url) {
  if (!song.value) return
  busy.value = true
  try {
    const res = await TagsAPI.write({ path: song.value.path, cover: url })
    const d = res?.data || {}
    if (d.cover_error) {
      props.flash(`封面写入失败：${d.cover_error}`, true)
      return
    }
    coverUrl.value = url
    props.flash('封面已写入文件')
    loadList(true)
  } catch (e) {
    props.flash(failText('写入封面', e, '写入失败'), true)
  } finally {
    busy.value = false
  }
}

async function saveTags() {
  if (!song.value) return
  busy.value = true
  try {
    await TagsAPI.write({
      path: song.value.path,
      title: tags.value.title,
      artist: tags.value.artist,
      album: tags.value.album,
    })
    props.flash('标签已写入文件')
    loadList(true)
  } catch (e) {
    props.flash(failText('写入标签', e, '写入失败'), true)
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  embedLyric.value = loadPref('repair_embed_lyric', '1') !== '0'
  writeLrc.value = loadPref('repair_write_lrc', '1') !== '0'
})
</script>

<template>
  <SectionCard title="歌曲修复" sub="逐首修歌词、换封面、改歌词时间轴" :icon="Wand2">
    <template #actions>
      <div class="flex items-center gap-1.5">
        <div class="flex items-center gap-1 rounded-full border border-[var(--ys-border)] bg-[var(--ys-bg)] px-2 py-1">
          <Search class="h-3.5 w-3.5 text-[var(--ys-text2)]" />
          <input
            v-model="keyword"
            @input="onKeyword"
            placeholder="搜歌名 / 歌手 / 专辑"
            class="w-32 bg-transparent text-[12px] outline-none placeholder:text-[var(--ys-text2)] sm:w-44"
          />
        </div>
        <button
          @click="loadList(false)"
          :disabled="loading"
          class="grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text2)] hover:bg-[var(--ys-bg)] hover:text-[var(--ys-text)] disabled:opacity-40"
          title="重新读取"
        >
          <Loader2 v-if="loading" class="h-4 w-4 animate-spin" />
          <RefreshCw v-else class="h-4 w-4" />
        </button>
      </div>
    </template>

    <template #body>
      <div class="flex flex-wrap items-center gap-1.5">
        <button
          v-for="f in FILTERS"
          :key="f.id"
          @click="setFilter(f.id)"
          :class="[
            'rounded-full px-2.5 py-1 text-[11px] font-medium transition-colors',
            filter === f.id
              ? 'bg-[var(--ys-brand)] text-white'
              : 'border border-[var(--ys-border)] text-[var(--ys-text2)] hover:text-[var(--ys-text)]',
          ]"
        >{{ f.name }}</button>
        <span class="ml-auto text-[11px] text-[var(--ys-text2)]">共 {{ total }} 首 · 第 {{ page }} / {{ pageCount }} 页</span>
      </div>

      <p v-if="loadErr" class="mt-2 text-[12px] text-rose-400">{{ loadErr }}</p>
      <p v-else-if="!rows.length && !loading && (keyword.trim() || filter !== 'all')" class="mt-3 text-[12px] text-[var(--ys-text2)]">
        没有符合条件的歌 —— 换个关键词，或把筛选切回「全部」。
      </p>
      <p v-else-if="!rows.length && !loading" class="mt-3 text-[12px] text-[var(--ys-text2)]">
        曲库索引里还没有文件 —— 先去「曲库」页扫描一次。
      </p>

      <ul v-else class="mt-2 divide-y divide-[var(--ys-border)]">
        <li v-for="r in shown" :key="r.path" class="flex items-center gap-2 py-2">
          <div class="min-w-0 flex-1">
            <b class="block truncate text-[13px]">{{ r.title || r.path.split('/').pop() }}</b>
            <small class="block truncate text-[11px] text-[var(--ys-text2)]">
              {{ r.artist || '未知歌手' }}{{ r.album ? ' · ' + r.album : '' }}
            </small>
          </div>
          <span
            :class="[
              'shrink-0 rounded-full px-1.5 py-0.5 text-[10px]',
              r.has_lyric ? 'bg-emerald-600/10 text-emerald-600' : 'bg-amber-500/10 text-amber-500',
            ]"
          >{{ r.has_lyric ? '有词' : '缺词' }}</span>
          <span
            :class="[
              'shrink-0 rounded-full px-1.5 py-0.5 text-[10px]',
              r.has_cover ? 'bg-emerald-600/10 text-emerald-600' : 'bg-amber-500/10 text-amber-500',
            ]"
          >{{ r.has_cover ? '有封' : '缺封' }}</span>
          <button
            @click="openSong(r)"
            class="shrink-0 rounded-full bg-[var(--ys-brand)] px-2.5 py-1 text-[11px] font-medium text-white"
          >修复</button>
        </li>
        <li v-if="!shown.length" class="py-3 text-center text-[12px] text-[var(--ys-text2)]">
          本页没有符合条件的歌曲
        </li>
      </ul>

      <div class="mt-2 flex items-center justify-center gap-3">
        <button
          @click="goPage(-1)"
          :disabled="page <= 1 || loading"
          class="grid h-7 w-7 place-items-center rounded-full border border-[var(--ys-border)] text-[var(--ys-text2)] disabled:opacity-40"
        ><ChevronLeft class="h-3.5 w-3.5" /></button>
        <span class="text-[11px] text-[var(--ys-text2)]">{{ page }} / {{ pageCount }}</span>
        <button
          @click="goPage(1)"
          :disabled="page >= pageCount || loading"
          class="grid h-7 w-7 place-items-center rounded-full border border-[var(--ys-border)] text-[var(--ys-text2)] disabled:opacity-40"
        ><ChevronRight class="h-3.5 w-3.5" /></button>
      </div>

      <!-- 磁盘条目本身不合法（非法文件名 / 0 字节音频 / 下载残留）：原来独立的
           「文件名体检」卡片。用户 2026-10-28 说「文件名体检改为歌曲修复」——
           功能没删，收成这张卡片底部的一颗入口按钮，点开才是完整扫描。 -->
      <div class="mt-3 border-t border-[var(--ys-border)] pt-2">
        <button
          @click="healthOpen = !healthOpen"
          class="flex w-full items-center gap-1.5 text-[11px] text-[var(--ys-text2)] hover:text-[var(--ys-text)]"
        >
          <ShieldAlert class="h-3.5 w-3.5" />
          <span>文件名体检（非法文件名 / 0 字节音频 / 下载残留）</span>
          <ChevronDown :class="['ml-auto h-3.5 w-3.5 transition-transform', healthOpen && 'rotate-180']" />
        </button>
        <LibraryHealthCard v-if="healthOpen" merge :dir="dir" />
      </div>
    </template>
  </SectionCard>

  <SideSheet v-model="open" :title="song?.title || '修复歌曲'" :subtitle="song?.path || ''">
    <div class="flex gap-1 pb-3">
      <button
        v-for="t in [{ id: 'lyric', name: '歌词' }, { id: 'cover', name: '封面' }, { id: 'tag', name: '标签' }]"
        :key="t.id"
        @click="tab = t.id"
        :class="[
          'flex-1 rounded-full py-1.5 text-[12px] font-medium',
          tab === t.id ? 'bg-[var(--ys-brand)] text-white' : 'border border-[var(--ys-border)] text-[var(--ys-text2)]',
        ]"
      >{{ t.name }}</button>
    </div>

    <!-- 歌词 -->
    <div v-if="tab === 'lyric'" class="space-y-3">
      <div class="flex items-center gap-2">
        <button
          @click="fetchLyricOnline"
          :disabled="busy"
          class="flex items-center gap-1 rounded-full bg-[var(--ys-brand)] px-2.5 py-1 text-[11px] font-medium text-white disabled:opacity-50"
        >
          <Wand2 class="h-3 w-3" />在线找词
        </button>
        <span class="text-[11px] text-[var(--ys-text2)]">{{ lyricSource || '还没有歌词' }}</span>
      </div>
      <p v-if="lyricErr" class="text-[11px] text-amber-500">{{ lyricErr }}</p>

      <label class="flex items-center gap-2 text-[11px] text-[var(--ys-text2)]">
        整体时间偏移
        <input v-model.number="lyricOffset" type="number" step="0.1" class="w-16 rounded border border-[var(--ys-border)] bg-transparent px-1.5 py-0.5 text-center text-[11px] outline-none" />秒
        <span>(正数=歌词延后)</span>
      </label>

      <div v-if="lyricPreview.length" class="max-h-32 overflow-auto rounded border border-[var(--ys-border)] p-2">
        <div v-for="(l, i) in lyricPreview" :key="i" class="truncate text-[11px] text-[var(--ys-text2)]">
          <span class="mr-1 tabular-nums opacity-60">{{ l.time.toFixed(1) }}s</span>{{ l.text }}
        </div>
      </div>

      <textarea
        v-model="lyricText"
        rows="10"
        spellcheck="false"
        placeholder="粘贴或直接编辑 LRC 歌词"
        class="w-full resize-y rounded border border-[var(--ys-border)] bg-transparent p-2 font-mono text-[11px] leading-5 outline-none"
      ></textarea>

      <div class="flex items-center gap-3 text-[11px] text-[var(--ys-text2)]">
        <label class="flex items-center gap-1"><input v-model="writeLrc" type="checkbox" />写 .lrc</label>
        <label class="flex items-center gap-1"><input v-model="embedLyric" type="checkbox" />内嵌进标签</label>
      </div>

      <button
        @click="saveLyric"
        :disabled="busy || !lyricText.trim()"
        class="w-full rounded-full bg-[var(--ys-brand)] py-2 text-[12px] font-medium text-white disabled:opacity-50"
      >{{ busy ? '写入中…' : '写入文件' }}</button>
    </div>

    <!-- 封面 -->
    <div v-else-if="tab === 'cover'" class="space-y-3">
      <div class="flex items-center gap-3">
        <div class="grid h-16 w-16 place-items-center overflow-hidden rounded bg-[var(--ys-bg)]">
          <img v-if="coverUrl" :src="coverUrl" alt="" class="h-full w-full object-cover" />
          <ImageIcon v-else class="h-5 w-5 text-[var(--ys-text2)]" />
        </div>
        <button
          @click="findCovers"
          :disabled="coverBusy"
          class="flex items-center gap-1 rounded-full bg-[var(--ys-brand)] px-2.5 py-1 text-[11px] font-medium text-white disabled:opacity-50"
        >
          <Loader2 v-if="coverBusy" class="h-3 w-3 animate-spin" />
          <Wand2 v-else class="h-3 w-3" />在线找封面
        </button>
      </div>
      <div v-if="coverCandidates.length" class="grid grid-cols-4 gap-2">
        <button
          v-for="(c, i) in coverCandidates"
          :key="i"
          @click="applyCover(c.cover_url)"
          :disabled="busy"
          class="overflow-hidden rounded border border-[var(--ys-border)] hover:border-[var(--ys-brand)]"
          :title="c.name"
        >
          <img :src="c.cover_url" alt="" class="aspect-square w-full object-cover" />
        </button>
      </div>
      <p v-else class="text-[11px] text-[var(--ys-text2)]">点「在线找封面」按歌名/歌手搜候选，点一张即写入文件。</p>
    </div>

    <!-- 标签 -->
    <div v-else class="space-y-2">
      <label class="block text-[11px] text-[var(--ys-text2)]">歌名
        <input v-model="tags.title" class="mt-1 w-full rounded border border-[var(--ys-border)] bg-transparent px-2 py-1 text-[12px] outline-none" />
      </label>
      <label class="block text-[11px] text-[var(--ys-text2)]">歌手
        <input v-model="tags.artist" class="mt-1 w-full rounded border border-[var(--ys-border)] bg-transparent px-2 py-1 text-[12px] outline-none" />
      </label>
      <label class="block text-[11px] text-[var(--ys-text2)]">专辑
        <input v-model="tags.album" class="mt-1 w-full rounded border border-[var(--ys-border)] bg-transparent px-2 py-1 text-[12px] outline-none" />
      </label>
      <button
        @click="saveTags"
        :disabled="busy"
        class="w-full rounded-full bg-[var(--ys-brand)] py-2 text-[12px] font-medium text-white disabled:opacity-50"
      >写入文件</button>
      <p class="text-[11px] text-[var(--ys-text2)]">留空的字段不会清掉文件里的原值。</p>
    </div>
  </SideSheet>
</template>
