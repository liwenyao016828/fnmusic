<template>
  <!--
    歌单下载：粘贴歌单分享链接 → 解析 → 展示真实歌曲/封面/去重结果 → 一键下载。
    与「发现音乐」里粘链接的区别：这里是**专门的下载工作台**，
    解析结果直接给出「已在库 / 待下载」的判定，省得自己比对。
  -->
  <component :is="embedded ? 'div' : PageShell" v-bind="shellProps">
    <template v-if="!embedded" #metrics>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div v-for="m in metrics" :key="m.label" class="rounded-xl border border-gray-200 bg-white px-[17px] py-[15px]">
          <b class="block text-[24px] font-bold" :class="m.color">{{ m.value }}</b>
          <span class="text-[12px] text-gray-400">{{ m.label }}</span>
        </div>
      </div>
    </template>

    <div class="space-y-4">

      <!-- ── 解析入口 ── -->
      <div class="rounded-[15px] border border-gray-200 bg-white p-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="min-w-0">
            <b class="text-[14px] text-gray-800">歌单下载</b>
            <p class="mt-1 text-[11px] leading-relaxed text-gray-400">
              把歌单分享链接粘到下面，点「解析」—— 网易云 / QQ 都行，App 里复制的短链也能直接用。
            </p>
          </div>
          <button
            @click="focusInput()"
            class="shrink-0 rounded-lg bg-blue-500 px-4 py-2 text-[12px] font-semibold text-white transition-all hover:bg-blue-600"
          >解析歌单链接</button>
        </div>

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <input
            ref="linkInput"
            v-model="link"
            type="text"
            placeholder="粘贴歌单分享链接，例如 https://music.163.com/#/playlist?id=3778678"
            class="min-w-[240px] flex-1 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs font-mono placeholder:text-gray-400 focus:border-emerald-400 focus:outline-none"
            @keyup.enter="parse"
          />
          <button
            @click="parse"
            :disabled="parsing || !link.trim()"
            class="shrink-0 rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
          >{{ parsing ? '解析中…' : '解析' }}</button>
        </div>

        <InlineNotice :text="error" class="mt-2" />
      </div>

      <!-- ── 解析结果 ── -->
      <template v-if="parsed">
        <div class="rounded-[15px] border border-gray-200 bg-white p-4">
          <div class="flex flex-wrap items-center gap-4">
            <div class="h-[76px] w-[76px] shrink-0 overflow-hidden rounded-xl bg-gray-100">
              <img v-if="parsed.cover" :src="parsed.cover" alt="" referrerpolicy="no-referrer" class="h-full w-full object-cover" />
              <div v-else class="grid h-full w-full place-items-center text-2xl text-gray-300">♫</div>
            </div>
            <div class="min-w-0 flex-1">
              <b class="block truncate text-[15px] text-gray-800">{{ parsed.title }}</b>
              <p class="mt-1 text-[12px] text-gray-400">
                共 {{ parsed.songs.length }} 首
                <span class="mx-1.5 text-gray-300">|</span>
                <span class="text-emerald-600">已在库 {{ inLibraryCount }} 首</span>
                <span class="mx-1.5 text-gray-300">|</span>
                <span class="text-amber-600">待下载 {{ toDownload.length }} 首</span>
              </p>
            </div>
            <button
              @click="downloadAll"
              :disabled="!toDownload.length"
              class="shrink-0 rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
            >{{ toDownload.length ? `一键下载全部（${toDownload.length}）` : '没有需要下载的' }}</button>
            <button
              v-if="!subscription"
              @click="subscribe"
              :disabled="subscribing"
              class="shrink-0 rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-2 text-[12px] font-semibold text-emerald-700 transition-all hover:bg-emerald-100 disabled:opacity-50"
            >{{ subscribing ? '订阅中…' : '订阅更新' }}</button>

            <template v-else>
              <span
                class="shrink-0 rounded-lg border px-3 py-2 text-[12px] font-semibold"
                :class="subscription.active
                  ? 'border-emerald-200 bg-emerald-50 text-emerald-700'
                  : 'border-gray-200 bg-gray-50 text-gray-500'"
              >{{ subscription.active ? '已订阅' : '已暂停' }}</span>
              <button
                @click="toggleSubscription"
                :disabled="togglingSub"
                class="shrink-0 rounded-lg border border-gray-200 bg-white px-4 py-2 text-[12px] font-semibold text-gray-600 transition-all hover:bg-gray-50 disabled:opacity-50"
              >{{ togglingSub ? '处理中…' : (subscription.active ? '停止更新' : '恢复更新') }}</button>
            </template>
          </div>
          <InlineNotice :text="notice" tone="ok" class="mt-3" />
        </div>

        <!-- 歌曲列表 -->
        <div class="overflow-hidden rounded-[15px] border border-gray-200 bg-white">
          <div class="flex items-center justify-between border-b border-gray-100 px-4 py-2.5">
            <span class="text-[12px] font-semibold text-gray-700">曲目列表</span>
            <label class="flex items-center gap-1.5 text-[11px] text-gray-500">
              <input type="checkbox" v-model="hideInLibrary" class="accent-emerald-500" />
              隐藏已在库的
            </label>
          </div>
          <div class="max-h-[520px] divide-y divide-gray-50 overflow-y-auto">
            <div
              v-for="(s, i) in visibleSongs" :key="s.id || i"
              class="flex items-center gap-3 px-4 py-2.5"
            >
              <span class="w-6 shrink-0 text-center font-mono text-[11px] text-gray-300">{{ i + 1 }}</span>
              <div class="h-9 w-9 shrink-0 overflow-hidden rounded-lg bg-gray-100">
                <img v-if="s.cover" :src="s.cover" alt="" referrerpolicy="no-referrer" class="h-full w-full object-cover" />
              </div>
              <div class="min-w-0 flex-1">
                <b class="block truncate text-[13px] text-gray-800">{{ s.name }}</b>
                <span class="block truncate text-[11px] text-gray-400">
                  {{ s.singer || '未知歌手' }}<span v-if="s.album"> · {{ s.album }}</span>
                </span>
              </div>
              <span
                v-if="downloadManager.isSongDownloaded(s)"
                class="shrink-0 rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-medium text-emerald-600"
              >已在库</span>
              <span
                v-else-if="downloadManager.isSongDownloading(s)"
                class="shrink-0 rounded-full bg-blue-50 px-2 py-0.5 text-[10px] font-medium text-blue-600"
              >下载中</span>
            </div>
            <p v-if="!visibleSongs.length" class="py-10 text-center text-[12px] text-gray-400">
              没有可显示的曲目
            </p>
          </div>
        </div>
      </template>

      <!-- 未解析时的空态 -->
      <div v-else-if="!parsing" class="rounded-[15px] border border-dashed border-gray-200 bg-white py-16 text-center">
        <p class="text-[13px] font-semibold text-gray-500">还没有解析任何歌单</p>
        <p class="mt-1 text-[11px] text-gray-400">把歌单分享链接粘到上面，点「解析」开始</p>
      </div>
    </div>
  </component>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ChartsAPI } from '../api/client'
import { downloadManager } from '../services/downloadManager'
import { fail, apiError } from '../services/userMsg'
import { logError, logWarn } from '../services/appLog'
import {
  playlistKey, findSubscription, subscribePlaylist, setSubscriptionEnabled,
} from '../services/subscribe'
import InlineNotice from './InlineNotice.vue'
import PageShell from './PageShell.vue'

const props = defineProps({
  /** true = 供「曲库管家」内嵌，不自带页面外壳 */
  embedded: { type: Boolean, default: false },
})

const shellProps = computed(() =>
  props.embedded
    ? {}
    : {
        title: '歌单下载',
        desc: '粘贴歌单分享链接，解析出真实曲目后整单下载，或订阅它持续追新',
      },
)

const link = ref('')
const linkInput = ref(null)
const parsing = ref(false)
const error = ref('')
const parsed = ref(null) // { title, cover, songs }
const hideInLibrary = ref(false)
const subscribing = ref(false)
const togglingSub = ref(false)
const notice = ref('')
/** 当前歌单的订阅状态：null = 未订阅，否则 { monitor, task, active } */
const subscription = ref(null)

const metrics = computed(() => [
  { label: '下载中', value: downloadManager.downloadingCount.value, color: 'text-blue-600' },
  { label: '等待中', value: downloadManager.pendingCount.value, color: 'text-gray-800' },
  { label: '失败待处理', value: downloadManager.failedCount.value, color: 'text-rose-600' },
  {
    label: '近 24h 成功率',
    // 没有样本时显示「—」而不是 0%：0% 会让人以为全失败了
    value: downloadManager.recentSuccessRate.value == null ? '—' : `${downloadManager.recentSuccessRate.value}%`,
    color: 'text-emerald-600',
  },
])

/** 已在库的数量 */
const inLibraryCount = computed(() =>
  (parsed.value?.songs || []).filter(s => downloadManager.isSongDownloaded(s)).length,
)
/** 还需要下载的曲目 */
const toDownload = computed(() =>
  (parsed.value?.songs || []).filter(
    s => !downloadManager.isSongDownloaded(s) && !downloadManager.isSongDownloading(s),
  ),
)
const visibleSongs = computed(() => {
  const list = parsed.value?.songs || []
  return hideInLibrary.value ? list.filter(s => !downloadManager.isSongDownloaded(s)) : list
})

function focusInput() {
  linkInput.value?.focus()
}

async function parse() {
  const url = link.value.trim()
  if (!url || parsing.value) return

  parsing.value = true
  error.value = ''
  notice.value = ''
  parsed.value = null
  subscription.value = null
  try {
    const res = await ChartsAPI.playlistDetailByUrl(url)
    const bad = apiError(res, '解析失败')
    if (bad) {
      error.value = bad
      logError('解析歌单链接', bad, `code=${res?.code} message=${res?.message} link=${url}`)
      return
    }
    const d = res.data || {}
    const songs = d.songs || []
    if (!songs.length) {
      // 「解析成功但一首都没有」多半不是出错 —— 说清两种可能，别让用户以为功能坏了
      error.value = '这个歌单没有解析到曲目 —— 可能是空歌单，或它需要登录才能查看。'
      logWarn('解析歌单链接', '解析到 0 首曲目', url)
      return
    }
    parsed.value = {
      title: d.name || d.title || '歌单',
      cover: d.cover || '',
      songs,
    }
    // 让「已在库」判定立刻生效
    downloadManager.checkSongsDownloaded(songs)
    // 这个歌单订阅过没有？解析完才知道是哪个歌单，所以在这里查
    await refreshSubscription()
  } catch (e) {
    error.value = fail('解析歌单链接', e)
  } finally {
    parsing.value = false
  }
}

function downloadAll() {
  const list = toDownload.value
  if (!list.length) return
  downloadManager.addBatch(list, '', false)
  downloadManager.open()
}

/**
 * 查这个歌单是否已订阅（按归一化歌单标识匹配监控与推送任务）。
 * 查不到就当未订阅 —— 订阅状态查询失败不该打扰用户。
 */
async function refreshSubscription() {
  subscription.value = null
  const key = playlistKey({ link: link.value })
  if (!key) return
  try {
    subscription.value = await findSubscription(key)
  } catch (_) { /* 忽略：按未订阅展示 */ }
}

/**
 * 订阅这个歌单：一键建「监控（自动下载）+ 推送（同步飞牛）」两条任务。
 *
 * 与「一键下载全部」的区别：那个是一次性的，这个是**持续追新** ——
 * 歌单以后新增的歌会自动下载，并自动补进飞牛音乐歌单。
 *
 * 建完立刻把当前解析出的曲目作为基线一起上报，省得等下一轮再抓一次。
 * 首轮只登记不下载（后端 BaselineDone 机制），避免几百首瞬间灌满队列。
 */
async function subscribe() {
  const d = parsed.value
  if (!d?.songs?.length || subscribing.value) return

  subscribing.value = true
  notice.value = ''
  error.value = ''
  try {
    // id 形如 wy_1973665667，上报要的是纯 id（songmid）
    const songs = d.songs
      .map((s) => ({
        source: s.source || 'wy',
        id: s.songmid || s.id || '',
        name: s.name || '',
        artist: s.singer || '',
        album: s.album || '',
        duration: s.interval || s.duration || 0,
        cover: s.cover || '',
      }))
      .filter((s) => s.id && s.name)

    const res = await subscribePlaylist({
      link: link.value.trim(),
      source: d.songs[0]?.source || '',
      name: d.title,
      songs,
    })

    if (!res.ok) {
      error.value = res.error
      logError('订阅歌单更新', res.error, `link=${link.value.trim()}`)
      return
    }
    notice.value = res.message
    await refreshSubscription()
  } catch (e) {
    error.value = fail('订阅歌单更新', e)
  } finally {
    subscribing.value = false
  }
}

/** 停止 / 恢复更新：两条任务一起改，已下载的文件与飞牛歌单都保留 */
async function toggleSubscription() {
  const sub = subscription.value
  if (!sub || togglingSub.value) return

  togglingSub.value = true
  error.value = ''
  try {
    const next = !sub.active
    await setSubscriptionEnabled(sub, next)
    await refreshSubscription()
    notice.value = next
      ? '已恢复更新，会继续自动下载新增曲目并同步飞牛歌单'
      : '已停止更新。已下载的文件和飞牛歌单都保留，随时可以恢复'
  } catch (e) {
    error.value = fail('更新订阅状态', e)
  } finally {
    togglingSub.value = false
  }
}
</script>
