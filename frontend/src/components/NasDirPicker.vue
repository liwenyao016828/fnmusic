<template>
  <!--
    通用 NAS 目录选择弹窗（用户 2026-10-25 要求）：
    「选择目录的按键不圆润，点击后不是弹窗选择，而是必须自己手动输入，要选择弹出的目录结构。」

    设计要点：
    - **只选目录**，不选文件；按钮圆角与全站一致（rounded-xl）。
    - 顶部是磁盘切换（后端 `/api/nas/browse` 每次返回 volumes = AllowedRoots()），
      中间是层级面包屑（可以点任意一级跳回去），底部是「用当前目录」。
    - 每行同时支持**单击进入 / 双击选中并确认** —— 目录多的时候双击少一次点击，
      单击进目录则是大多数人第一反应，两种都留着。
    - 只依赖 `NasAPI.browse`，不新增后端接口。
  -->
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      @click.self="close()"
    >
      <div class="flex max-h-[85vh] w-full max-w-xl flex-col rounded-2xl border border-[var(--ys-border)] bg-white shadow-2xl">
        <!-- 头部 -->
        <div class="flex items-center justify-between border-b border-[var(--ys-border)] px-4 py-3">
          <h3 class="flex items-center gap-1.5 text-[13px] font-semibold text-[var(--ys-text)]">
            <FolderOpen class="h-4 w-4 text-amber-500" />
            <span>{{ title }}</span>
          </h3>
          <button
            type="button"
            class="rounded-full p-1 text-[var(--ys-text-faint)] hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
            @click="close()"
          >
            <X class="h-4 w-4" />
          </button>
        </div>

        <!-- 磁盘切换 -->
        <div v-if="volumes.length" class="flex flex-wrap gap-1.5 border-b border-[var(--ys-border)] px-4 py-2">
          <button
            v-for="v in volumes"
            :key="v"
            type="button"
            :class="[
              'rounded-full border px-2.5 py-1 text-[11px] transition-colors',
              currentVolume === v
                ? 'border-emerald-300 bg-emerald-50 text-emerald-700'
                : 'border-[var(--ys-border)] text-[var(--ys-text-muted)] hover:border-emerald-200 hover:text-[var(--ys-text)]',
            ]"
            @click="load(v)"
          >{{ v }}</button>
        </div>

        <!-- 飞牛宿主目录选择器（仅在宿主环境里出现；普通浏览器/开发机热更新时自动隐藏） -->
        <div
          v-if="hostAvailable"
          class="flex flex-wrap items-center gap-2 border-b border-[var(--ys-border)] bg-emerald-50/50 px-4 py-2"
        >
          <button
            type="button"
            class="flex items-center gap-1.5 rounded-xl border border-emerald-200 bg-white px-3 py-1.5 text-[12px] font-medium text-emerald-700 transition-colors hover:border-emerald-300 hover:bg-emerald-50 disabled:opacity-50"
            :disabled="hostPicking"
            @click="pickViaHost()"
          >
            <Sparkles class="h-3.5 w-3.5" />
            <span v-if="hostPicking">等待飞牛选择…</span>
            <template v-else>
              <span class="sm:hidden">飞牛选择器</span>
              <span class="hidden sm:inline">用飞牛的文件选择器</span>
            </template>
          </button>
          <span class="min-w-0 flex-1 text-[10px] leading-tight text-[var(--ys-text-faint)]">
            <span
              v-if="trimForced"
              class="rounded bg-amber-100 px-1 py-0.5 font-medium text-amber-700"
            >排版预览</span>
            <!-- 窄屏只留按钮和角标：这两句说明在手机上会把这一行撑成三行 -->
            <span class="hidden sm:inline">
              {{ trimForced ? '飞牛桌面的应用里才会真的弹选择器' : '选中的目录会自动授权给曲率，比一层层点更快' }}
            </span>
          </span>
        </div>

        <!-- 宿主选择器自己的报错：它跟「目录读不出来」不是一回事，
             所以单独一条、不挂「重试」按钮（重试会变成重新浏览目录，牛头不对马嘴） -->
        <InlineNotice v-if="hostError" class="mx-4 mt-2" :text="hostError" />

        <!-- 读取失败提示：以前 error 只赋值不渲染，请求挂了却显示「没有子文件夹」，会误导用户 -->
        <div v-if="error" class="mx-4 mt-2 flex items-center gap-2">
          <InlineNotice class="min-w-0 flex-1" :text="error" />
          <button
            type="button"
            class="shrink-0 rounded-lg border border-rose-200 bg-white px-2.5 py-1 text-[11px] font-medium text-rose-700 hover:bg-rose-50"
            @click="retry()"
          >重试</button>
        </div>

        <!-- 面包屑 -->
        <div class="flex items-center gap-1 overflow-x-auto border-b border-[var(--ys-border)] px-4 py-2 text-[11px]">
          <button
            type="button"
            class="shrink-0 rounded-md px-1.5 py-0.5 font-mono text-[var(--ys-text-muted)] hover:bg-black/[0.04]"
            title="上一级"
            :disabled="!parent"
            :class="!parent ? 'opacity-40' : ''"
            @click="parent && load(parent)"
          ><ArrowUp class="h-3.5 w-3.5" /></button>
          <template v-for="(c, i) in crumbs" :key="c.path">
            <span v-if="i" class="text-[var(--ys-text-faint)]">/</span>
            <button
              type="button"
              :class="[
                'shrink-0 rounded-md px-1.5 py-0.5 font-mono hover:bg-black/[0.04]',
                i === crumbs.length - 1 ? 'text-[var(--ys-text)]' : 'text-[var(--ys-text-muted)]',
              ]"
              @click="load(c.path)"
            >{{ c.name }}</button>
          </template>
        </div>

        <!--
          隐藏目录开关（2026-10-25 用户报障：曲库在 `music-v2/.dev/music`，
          `.dev` 在目录树里根本看不见）—— 后端默认把 `.`/`$`/`@` 开头的条目藏起来
          （`.tidy_trash`、`@eaDir`、`$RECYCLE.BIN` 不该被选成曲库），
          这里给一个显式开关，选了就记进 localStorage，下次打开还是这个偏好。
        -->
        <div class="flex items-center justify-between gap-2 border-b border-[var(--ys-border)] px-4 py-1.5">
          <span class="font-mono text-[10px] text-[var(--ys-text-faint)]">
            {{ folders.length ? folders.length + ' 个子目录' : '' }}
          </span>
          <label class="flex cursor-pointer items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
            <input
              type="checkbox"
              class="h-3.5 w-3.5 accent-emerald-500"
              :checked="showHidden"
              @change="setShowHidden($event.target.checked)"
            />
            <span>显示隐藏目录</span>
            <span class="hidden text-[10px] text-[var(--ys-text-faint)] sm:inline">（. 开头的，如 .dev）</span>
          </label>
        </div>

        <!-- 「开关就在眼前但看不见」兜底：用户 2026-10-25 报了两次「.dev 看不到」，
             所以后端把被折叠的数量和名字一起带回来，直接在列表上方点名。 -->
        <div v-if="!showHidden && hiddenCount > 0" class="border-b border-[var(--ys-border)] px-4 py-1.5">
          <button
            type="button"
            class="w-full rounded-lg bg-amber-50/70 px-2 py-1.5 text-left text-[11px] leading-tight text-amber-800 transition-colors hover:bg-amber-100"
            @click="setShowHidden(true)"
          >
            这个目录里还有 {{ hiddenCount }} 个隐藏文件夹没显示<template v-if="hiddenDirs.length">（{{ hiddenDirs.join('、') }}）</template> · 点这里显示
          </button>
        </div>

        <!-- 目录列表 -->
        <div class="min-h-[180px] flex-1 overflow-y-auto px-2 py-2">
          <div v-if="loading" class="py-8 text-center text-[11px] text-[var(--ys-text-faint)]">读取目录…</div>
          <template v-else>
            <button
              v-for="f in folders"
              :key="f.path"
              type="button"
              class="flex w-full items-center justify-between gap-2 rounded-lg px-2 py-1.5 text-left text-[12px] text-[var(--ys-text)] transition-colors hover:bg-emerald-50/60"
              @click="load(f.path)"
              @dblclick="choose(f.path)"
            >
              <span class="flex min-w-0 items-center gap-2">
                <Folder class="h-3.5 w-3.5 shrink-0 text-amber-400" />
                <span class="truncate">{{ f.name }}</span>
              </span>
              <span v-if="f.audio_count" class="shrink-0 font-mono text-[10px] text-[var(--ys-text-faint)]">{{ f.audio_count }} 首</span>
            </button>
            <p v-if="!folders.length && !error" class="py-8 text-center text-[11px] text-[var(--ys-text-faint)]">
              这个目录下没有子文件夹 —— 可以点下面「用这个目录」直接选它
            </p>
            <p v-else-if="!folders.length && error" class="py-8 text-center text-[11px] text-[var(--ys-text-faint)]">
              目录没读出来，上面可以重试，或换一个位置
            </p>
          </template>
        </div>

        <!-- 底部：当前路径 + 确认 -->
        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--ys-border)] px-4 py-2.5">
          <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-[var(--ys-text-muted)]" :title="current">{{ current || '（未选择）' }}</span>
          <div class="flex shrink-0 gap-2">
            <button
              type="button"
              class="rounded-xl px-3 py-1.5 text-[12px] text-[var(--ys-text-muted)] hover:bg-black/[0.04]"
              @click="close()"
            >取消</button>
            <button
              type="button"
              class="rounded-xl bg-emerald-500 px-4 py-1.5 text-[12px] font-medium text-white hover:bg-emerald-600 disabled:opacity-50"
              :disabled="!current"
              @click="choose(current)"
            >用这个目录</button>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { NasAPI } from '../api/client'
import { Folder, FolderOpen, X, ArrowUp, Sparkles } from 'lucide-vue-next'
import InlineNotice from './InlineNotice.vue'
import { fail } from '../services/userMsg'
import { loadBool, savePref } from '../services/prefs'
import { detectTrimHost, hasTrimPicker, trimProbed, trimForced, pickDirectory } from '../services/trimHost'

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '选择目录' },
  /** 初始打开的位置（一般是输入框里现在的值），空则从第一块盘开始 */
  start: { type: String, default: '' },
})
const emit = defineEmits(['update:open', 'pick'])

const loading = ref(false)
const current = ref('')
const parent = ref('')
const volumes = ref([])
const folders = ref([])
const error = ref('')
const hostError = ref('')
const hostPicking = ref(false)

/**
 * 是否列出 `.` 开头的隐藏目录（用户 2026-10-25 报障：曲库放在 `.dev/music`，
 * 目录树里翻不到）。默认关闭 —— 隐藏目录里塞着 `.tidy_trash` 这类不该被选成
 * 曲库的东西；偏好走 prefs.js（多端共享，与「本地曲库」弹窗同一个键）。
 */
const showHidden = ref(loadBool('nas_show_hidden', false))

/** 当前目录里被折叠掉的隐藏文件夹（数量 + 前几个名字），后端 hidden_count/hidden_dirs 带回 */
const hiddenCount = ref(0)
const hiddenDirs = ref([])

function setShowHidden(v) {
  showHidden.value = !!v
  savePref('nas_show_hidden', showHidden.value)
  load(current.value || props.start || '')
}

/** 宿主目录选择器是否可用（探测完成后才会有真值，未探完时按钮不出现） */
const hostAvailable = computed(() => trimProbed.value && hasTrimPicker())

/** 当前目录落在哪个盘上（用于高亮磁盘按钮；取最长前缀，避免 /vol10 也亮 /vol1） */
const currentVolume = computed(() => {
  let best = ''
  for (const v of volumes.value) {
    if (current.value === v || current.value.startsWith(v.endsWith('/') ? v : v + '/')) {
      if (v.length > best.length) best = v
    }
  }
  return best
})

/** 面包屑：把 /vol1/Music/华语 拆成可点的层级 */
const crumbs = computed(() => {
  const p = current.value
  if (!p) return []
  const parts = p.split('/').filter(Boolean)
  const out = [{ name: '/', path: '/' }]
  let acc = ''
  for (const seg of parts) {
    acc += '/' + seg
    out.push({ name: seg, path: acc })
  }
  return out
})

async function load(path = '') {
  loading.value = true
  error.value = ''
  try {
    const res = await NasAPI.browse(path, { hidden: showHidden.value })
    const d = res?.data || res || {}
    current.value = d.current || path
    parent.value = d.parent || ''
    volumes.value = d.volumes || volumes.value || []
    folders.value = d.folders || []
    hiddenCount.value = d.hidden_count || 0
    hiddenDirs.value = d.hidden_dirs || []
  } catch (e) {
    // 走统一的「一句人话 + 进日志」，不再把 HTTP 串直接怼到界面上
    error.value = fail('目录浏览', e, '读取目录失败')
    folders.value = []
    hiddenCount.value = 0
    hiddenDirs.value = []
  } finally {
    loading.value = false
  }
}

/** 出错后的重试：优先重读当前目录，其次回到打开时的起始目录 */
function retry() {
  load(current.value || props.start || '')
}

/**
 * 用飞牛宿主的目录选择器挑一个目录。
 *
 * 和上面自己画的目录树是**两条并存的路**：这里选中的路径会被宿主授权给曲率，
 * 适合「曲库在别的盘/别人的共享里，自己一层层点太慢」的场景。
 * 宿主不可用时按钮根本不会渲染，所以这里不用再判环境。
 */
async function pickViaHost() {
  if (hostPicking.value) return
  hostPicking.value = true
  hostError.value = ''
  try {
    const r = await pickDirectory({ title: props.title })
    if (r.ok) {
      choose(r.path)
    } else if (!r.cancelled) {
      hostError.value = fail('飞牛目录选择', r.error || '宿主没返回目录', '选择目录失败')
    }
  } finally {
    hostPicking.value = false
  }
}

function choose(path) {
  if (!path) return
  emit('pick', path)
  emit('update:open', false)
}

function close() {
  emit('update:open', false)
}

// 每次打开都重新定位到输入框里现在的值（用户可能在外面改过路径）
// `immediate` 是给「父组件一挂载就是打开状态」的用法兜底 —— 不加的话那种情况下
// watch 不触发，目录树会一直空着。
watch(() => props.open, (v) => {
  if (v) {
    volumes.value = []
    folders.value = []
    error.value = ''
    hostError.value = ''
    hiddenCount.value = 0
    hiddenDirs.value = []
    // 隐藏项开关与「本地曲库」弹窗共用同一个偏好键，开弹窗时重读一次保持同步
    showHidden.value = loadBool('nas_show_hidden', false)
    load(props.start || '')
    // 宿主探测是异步的（要跟父窗口握手，最长 4s），**不 await** —— 探到了
    // 「用飞牛的文件选择器」那行会自己冒出来，探不到就永远不出现。
    detectTrimHost()
  }
}, { immediate: true })
</script>
