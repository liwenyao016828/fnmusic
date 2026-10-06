<template>
  <!--
    下载音质选择弹窗

    数据来自 GET /api/music/qualities（官方协议，免音源）——
    后端用「搜索 + 按 ID 匹配」拿到该曲在目标平台**真实可用**的档位，
    不再像以前那样对所有歌返回同一份硬编码档位。

    匹配不到时（matched_by === 'none'）不臆造档位：只提供「最高音质（自动）」，
    由音源脚本在取链时自行降级。
  -->
  <Teleport to="body">
    <Transition
      enter-active-class="transition-all duration-200 ease-out"
      enter-from-class="opacity-0 scale-95"
      enter-to-class="opacity-100 scale-100"
      leave-active-class="transition-all duration-150 ease-in"
      leave-from-class="opacity-100 scale-100"
      leave-to-class="opacity-0 scale-95"
    >
      <div
        v-if="modelValue"
        class="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4 backdrop-blur-xs"
        @click.self="close"
      >
        <div class="w-full max-w-[440px] overflow-hidden rounded-2xl border border-gray-100 bg-white shadow-2xl">

          <!-- 头部 -->
          <div class="flex items-start justify-between gap-3 border-b border-gray-100 px-5 py-4">
            <div class="min-w-0">
              <h3 class="text-[15px] font-bold text-gray-800">选择下载音质</h3>
              <p class="mt-1 truncate text-[11px] text-gray-400">
                {{ song?.name || '未知歌曲' }}<span v-if="song?.singer"> · {{ song.singer }}</span>
              </p>
            </div>
            <button
              @click="close"
              class="shrink-0 rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600"
            ><X class="w-4 h-4" /></button>
          </div>

          <!-- 主体 -->
          <div class="px-5 py-4">

            <!-- 加载中 -->
            <div v-if="loading" class="flex items-center justify-center gap-2 py-8 text-[12px] text-gray-400">
              <Loader2 class="w-4 h-4 animate-spin text-emerald-500" />
              正在查询该曲真实可用音质…
            </div>

            <template v-else>
              <!--
                查询失败：用统一提示条（一句人话 + 「查看日志」入口）。
                下面两个「未命中」块**保持蓝色/琥珀色** —— 它们不是错误，
                是「档位仅供参考」的结果说明，用红色反而吓人。
              -->
              <InlineNotice v-if="fetchError" :text="fetchError" class="mb-3" />
              <div
                v-else-if="matchedBy === 'name_singer'"
                class="mb-3 rounded-lg border border-blue-100 bg-blue-50/70 px-3 py-2 text-[11px] leading-relaxed text-blue-900/80"
              >
                没按 ID 精确命中，是按「曲名 + 歌手」匹配到的同名曲目 —— 档位仅供参考，拿不准就选「最高音质」。
              </div>
              <div
                v-else-if="matchedBy === 'none'"
                class="mb-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-[11px] leading-relaxed text-amber-800"
              >
                查不到这首歌的可用档位 —— 选「最高音质」即可，脚本会自动降级。
              </div>

              <!-- 档位列表 -->
              <div class="space-y-2">
                <!-- 最高音质（自动）—— 永远可选，作为兜底 -->
                <button
                  @click="picked = 'highest'"
                  :class="[
                    'flex w-full items-center gap-3 rounded-xl border px-3.5 py-3 text-left transition-all',
                    picked === 'highest'
                      ? 'border-emerald-400 bg-emerald-50/70 ring-1 ring-emerald-400/20'
                      : 'border-gray-200 hover:border-emerald-300'
                  ]"
                >
                  <div class="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-emerald-500 text-white">
                    <Sparkles class="w-4 h-4" />
                  </div>
                  <div class="min-w-0 flex-1">
                    <b class="block text-[13px] text-gray-800">最高音质（自动）</b>
                    <span class="block text-[11px] text-gray-400">从可用档位由高到低自动尝试，拿到哪个算哪个</span>
                  </div>
                  <Check v-if="picked === 'highest'" class="w-4 h-4 shrink-0 text-emerald-500" />
                </button>

                <!-- 该曲真实可用的固定档位 -->
                <button
                  v-for="q in qualitys" :key="q"
                  @click="picked = q"
                  :class="[
                    'flex w-full items-center gap-3 rounded-xl border px-3.5 py-3 text-left transition-all',
                    picked === q
                      ? 'border-emerald-400 bg-emerald-50/70 ring-1 ring-emerald-400/20'
                      : 'border-gray-200 hover:border-emerald-300'
                  ]"
                >
                  <div class="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-gray-100 text-gray-500">
                    <Music class="w-4 h-4" />
                  </div>
                  <div class="min-w-0 flex-1">
                    <b class="block text-[13px] text-gray-800">{{ qualityLabel(q) }}</b>
                    <span class="block font-mono text-[11px] text-gray-400">{{ q }}</span>
                  </div>
                  <span
                    v-if="q === best"
                    class="shrink-0 rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-bold text-emerald-600"
                  >该曲最高</span>
                  <Check v-if="picked === q" class="w-4 h-4 shrink-0 text-emerald-500" />
                </button>
              </div>

              <!-- 记住为默认 -->
              <label class="mt-3 flex items-center gap-2 text-[11px] text-gray-500">
                <input type="checkbox" v-model="remember" class="accent-emerald-500" />
                记住为默认音质（下次单曲下载不再询问）
              </label>
            </template>
          </div>

          <!-- 底部 -->
          <div class="flex items-center justify-end gap-2 border-t border-gray-100 px-5 py-3.5">
            <button
              @click="close"
              class="rounded-lg px-3.5 py-2 text-[12px] font-semibold text-gray-500 transition-colors hover:bg-gray-50"
            >取消</button>
            <button
              @click="confirm"
              :disabled="loading"
              class="rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
            >加入下载队列</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import { ref, watch } from 'vue'
import { X, Check, Loader2, Sparkles, Music } from 'lucide-vue-next'
import { MusicAPI } from '../api/client'
import { fail, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'
import InlineNotice from './InlineNotice.vue'
import { qualityLabel } from '../engine/quality'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  song: { type: Object, default: null },
})

const emit = defineEmits(['update:modelValue', 'confirm'])

const loading = ref(false)
const qualitys = ref([])
const best = ref('')
const matchedBy = ref('')
const fetchError = ref('')
const picked = ref('highest')
const remember = ref(false)

function close() {
  emit('update:modelValue', false)
}

function confirm() {
  emit('confirm', { quality: picked.value, remember: remember.value })
  close()
}

/** 打开时按 song 查一次真实音质 */
watch(
  () => [props.modelValue, props.song],
  async ([open, song]) => {
    if (!open || !song) return

    loading.value = true
    qualitys.value = []
    best.value = ''
    matchedBy.value = ''
    fetchError.value = ''
    picked.value = 'highest'
    remember.value = false

    try {
      const res = await MusicAPI.qualities(
        song.source || '',
        song.songmid || song.id || '',
        song.name || '',
        song.singer || '',
        song.hash || '',
      )
      const bad = apiError(res, '查询失败')
      if (bad) {
        fetchError.value = bad + ' —— 直接选「最高音质」即可，脚本会自动降级。'
        logError('查询歌曲音质', bad, `code=${res?.code} message=${res?.message} id=${song.id || ''}`)
      } else {
        qualitys.value = res.data?.qualitys || []
        best.value = res.data?.best || ''
        matchedBy.value = res.data?.matched_by || ''
      }
    } catch (e) {
      // 查询失败不影响下载：仍可选「最高音质」由音源脚本自行降级
      fetchError.value = fail('查询歌曲音质', e) + ' —— 直接选「最高音质」即可，脚本会自动降级。'
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)
</script>
