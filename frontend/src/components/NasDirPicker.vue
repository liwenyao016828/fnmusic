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
            <p v-if="!folders.length" class="py-8 text-center text-[11px] text-[var(--ys-text-faint)]">
              这个目录下没有子文件夹 —— 可以点下面「用这个目录」直接选它
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
import { Folder, FolderOpen, X, ArrowUp } from 'lucide-vue-next'

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
    const res = await NasAPI.browse(path)
    const d = res?.data || res || {}
    current.value = d.current || path
    parent.value = d.parent || ''
    volumes.value = d.volumes || volumes.value || []
    folders.value = d.folders || []
  } catch (e) {
    error.value = '读取目录失败：' + (e?.message || e)
  } finally {
    loading.value = false
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
watch(() => props.open, (v) => {
  if (v) {
    volumes.value = []
    folders.value = []
    load(props.start || '')
  }
})
</script>
