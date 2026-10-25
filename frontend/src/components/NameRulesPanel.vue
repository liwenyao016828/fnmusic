<template>
  <!--
    文件名语义解析试算 + 命名解析规则（2026-10-26 用户要求：从「账号连接 → AI 大模型」搬进
    「补全整理 → 歌名修复」卡片。这两块本来就是「改歌名」的工具，摆在账号凭证页面里
    既找不到、也不该跟着 AI 配置一起折叠）。

    # 自包含

    组件自己拉规则列表、自己试算、自己保存/删除，父组件不持有任何状态 —— 这样插到
    哪个页面都只是「多一个工具」，不需要父页面替它记 useState。

    # 为什么让 AI 产出「规则」而不是「结果」

    成本从「每首歌一次调用」降到「每种命名习惯一次」，而且正则用户看得懂、能自己改。
    ⚠️ 生成与保存分成两步 —— AI 产出的正则可能语法错、匹配不上样例、提取不出字段，
    必须先给用户看，直接存下去再让他在别处发现问题就晚了。
  -->
  <div class="space-y-3">
    <!-- 文件名解析试算 -->
    <div>
      <span class="text-xs font-semibold text-gray-700">文件名语义解析试算</span>
      <div class="flex items-center gap-2 mt-2">
        <input v-model="aiFilename" class="flex-1 px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
          placeholder="【无损】周杰伦 - 晴天.flac" />
        <button @click="runParse" :disabled="parsing"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
          {{ parsing ? '解析中...' : '解析' }}
        </button>
      </div>
      <!-- 有曲库目录时给一句可抄的样例来源，省得用户自己猜格式（dir 为空则整行不渲染，
           行为和搬过来之前完全一样） -->
      <p v-if="dir" class="mt-1.5 text-[11px] text-gray-400">
        试算用的文件名可以照抄曲库里的，例如 <span class="font-mono">{{ dir }}</span> 下的某一首
      </p>
      <div v-if="parseResult" class="mt-2 p-2.5 rounded-lg bg-gray-50 border border-gray-100 text-[11px] text-gray-600 font-mono whitespace-pre-wrap">
        {{ JSON.stringify(parseResult, null, 2) }}
      </div>
    </div>

    <!-- 命名解析规则 -->
    <div class="pt-3 border-t border-gray-50 space-y-2">
      <div class="flex items-center justify-between">
        <span class="text-xs font-semibold text-gray-700">命名解析规则</span>
        <span class="text-[10px] text-gray-400">优先于内置规则</span>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <input
          v-model="ruleForm.sample"
          class="min-w-[180px] flex-1 rounded-lg border border-gray-200 px-2.5 py-1.5 font-mono text-xs"
          placeholder="【无损】Jay_Chou_晴天_2003.mp3"
        />
        <input
          v-model="ruleForm.desc"
          class="min-w-[150px] flex-1 rounded-lg border border-gray-200 px-2.5 py-1.5 text-xs"
          placeholder="一句话解释（可选）"
        />
      </div>

      <div class="grid grid-cols-2 gap-2">
        <ActionCard
          :title="ruleBusy ? '生成中…' : 'AI 生成规则'"
          hint="样例 + 一句解释 → 一条正则；确认无误再保存"
          :icon="Sparkles"
          :disabled="ruleBusy"
          @click="genNameRule"
        />
        <!-- 保存之后的行为与代价：整库按这条规则解析、不再调用 AI。
             用户点「保存规则」之前要知道这件事（它决定「值不值得花一次 AI」）。 -->
        <p class="text-[11px] leading-snug text-gray-400 sm:col-span-2 lg:col-span-3">
          保存后整库按这条规则解析，<b class="text-gray-600">不再调用 AI</b>。
        </p>
        <!-- 空态卡：这句说明没有可点的动作，所以不用 ActionCard（它会渲染成一个按钮，
             点了没反应反而是个坑），只借同一套卡片排版跟动作卡并排 -->
        <div v-if="!nameRules.length" class="rounded-xl border border-dashed border-gray-200 px-3 py-2.5">
          <b class="block text-[12.5px] font-bold text-gray-600">还没有自定义规则</b>
          <span class="mt-0.5 block text-[11px] leading-snug text-gray-400">常见命名会由内置规则处理</span>
        </div>
      </div>

      <!-- 生成结果：正则可编辑，校验通过才让保存 -->
      <div v-if="ruleForm.regex" class="space-y-2 rounded-lg border border-gray-200 bg-gray-50/60 p-2.5">
        <div v-if="ruleForm.error" class="text-[11px] text-rose-600">
          没通过校验：{{ ruleForm.error }}
        </div>
        <input
          v-model="ruleForm.regex"
          class="w-full rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 font-mono text-xs"
        />
        <div v-if="ruleForm.note" class="text-[11px] text-gray-500">{{ ruleForm.note }}</div>
        <div v-if="ruleForm.preview" class="font-mono text-[11px] text-emerald-700">
          试算：歌手={{ ruleForm.preview.artist || '(空)' }} · 歌名={{ ruleForm.preview.title || '(空)' }}
        </div>
        <div class="flex items-center gap-2">
          <button
            :disabled="ruleBusy"
            class="rounded-lg bg-gray-100 px-2.5 py-1 text-[11px] text-gray-600 hover:bg-gray-200 disabled:opacity-50"
            @click="testNameRule"
          >校验</button>
          <button
            :disabled="ruleBusy || !ruleForm.regex"
            class="rounded-lg bg-emerald-500 px-2.5 py-1 text-[11px] font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
            @click="saveNameRule"
          >保存规则</button>
          <button class="px-2.5 py-1 text-[11px] text-gray-400 hover:text-gray-600" @click="resetNameRule">取消</button>
        </div>
      </div>

      <!-- 已有规则 -->
      <div v-if="nameRules.length" class="space-y-1.5">
        <div v-for="r in nameRules" :key="r.id" class="rounded-lg border border-gray-100 bg-white p-2.5 text-[11px]">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 flex-1">
              <div class="break-all font-mono text-gray-700">{{ r.regex }}</div>
              <div class="mt-0.5 text-gray-400">
                <span v-if="r.note">{{ r.note }}</span>
                <span v-if="r.sample"> · 样例：{{ r.sample }}</span>
              </div>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <span :class="['rounded px-1.5 py-0.5 text-[10px]', r.disabled ? 'bg-gray-100 text-gray-400' : 'bg-emerald-50 text-emerald-600']">
                {{ r.disabled ? '已停用' : '生效中' }}
              </span>
              <button class="text-gray-400 hover:text-gray-600" @click="toggleNameRule(r)">{{ r.disabled ? '启用' : '停用' }}</button>
              <button class="text-gray-400 hover:text-rose-500" @click="deleteNameRule(r)">删除</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 这个面板自己的一句话反馈（原来靠宿主页的 flash 槽） -->
    <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Sparkles } from 'lucide-vue-next'
import { AIAPI, TidyAPI } from '../api/client'
import InlineNotice from './InlineNotice.vue'
import ActionCard from './ActionCard.vue'
import { fail, apiError } from '../services/userMsg'

const props = defineProps({
  /**
   * 曲库目录（可选）。只用于试算区的样例提示 —— 用户从「歌名修复」进来时，
   * 手边就有这个目录里的真实文件名可抄。为空则完全不渲染提示。
   */
  dir: { type: String, default: '' },
})

/**
 * AI 调用（试算 / 生成规则）会消耗 token，用量统计在别处展示。
 * 这里只发通知，不持有父组件状态 —— 没监听方就等于没发。
 */
const emit = defineEmits(['usage-changed'])

const aiFilename = ref('')
const parsing = ref(false)
const parseResult = ref(null)

// ── 一句话反馈：本组件自己的 notice，宿主页不用给 flash 槽 ──
const notice = ref('')
const noticeOk = ref(false)
let noticeTimer = null

function flash(msg, isError = false) {
  notice.value = msg || ''
  noticeOk.value = !isError
  if (noticeTimer) clearTimeout(noticeTimer)
  if (notice.value) noticeTimer = setTimeout(() => { notice.value = '' }, 6000)
}

onMounted(() => {
  loadNameRules()
})

async function runParse() {
  if (!aiFilename.value.trim()) return flash('请输入文件名', true)
  parsing.value = true
  parseResult.value = null
  try {
    const res = await AIAPI.complete({ kind: 'name_parse', filename: aiFilename.value })
    parseResult.value = res.data || {}
    if (res.message && res.message !== 'ok') flash(res.message)
    emit('usage-changed')
  } catch (e) {
    flash(fail('文件名语义解析', e), true)
  } finally {
    parsing.value = false
  }
}

// ── 命名解析规则：AI 生成正则 → 校验 → 可手改 → 保存 ──
const nameRules = ref([])
const ruleBusy = ref(false)
const ruleForm = ref({ sample: '', desc: '', regex: '', note: '', preview: null, error: '', id: '' })

async function loadNameRules() {
  try {
    const res = await TidyAPI.nameRules()
    nameRules.value = res?.data?.rules || []
  } catch (_) {
    // 拉不到不阻塞页面
  }
}

function resetNameRule() {
  ruleForm.value = {
    sample: ruleForm.value.sample,
    desc: ruleForm.value.desc,
    regex: '', note: '', preview: null, error: '', id: '',
  }
}

async function genNameRule() {
  const sample = ruleForm.value.sample.trim()
  if (!sample) return flash('先填一个文件名样例', true)

  ruleBusy.value = true
  ruleForm.value.error = ''
  try {
    const res = await TidyAPI.saveNameRule({ sample, desc: ruleForm.value.desc })
    const d = res?.data || {}
    ruleForm.value.regex = d.regex || ''
    ruleForm.value.note = d.note || ''
    ruleForm.value.preview = d.preview || null
    ruleForm.value.id = ''
    if (d.valid === false) {
      ruleForm.value.error = d.error || '没通过校验'
      flash('AI 生成的规则没通过校验，请手动修正', true)
    } else {
      flash("规则已生成，确认无误后点「保存规则」")
    }
    emit('usage-changed')
  } catch (e) {
    flash(fail('生成命名规则', e), true)
  } finally {
    ruleBusy.value = false
  }
}

async function testNameRule() {
  ruleBusy.value = true
  try {
    const res = await TidyAPI.saveNameRule({
      regex: ruleForm.value.regex, sample: ruleForm.value.sample, dry_run: true,
    })
    ruleForm.value.error = ''
    ruleForm.value.preview = res?.data?.preview || null
    flash("校验通过")
  } catch (e) {
    ruleForm.value.error = apiError(e, '规则没通过校验')
    ruleForm.value.preview = null
    flash(ruleForm.value.error, true)
  } finally {
    ruleBusy.value = false
  }
}

async function saveNameRule() {
  ruleBusy.value = true
  try {
    const res = await TidyAPI.saveNameRule({
      id: ruleForm.value.id,
      regex: ruleForm.value.regex,
      sample: ruleForm.value.sample,
      note: ruleForm.value.note,
      source: 'ai',
    })
    flash(res?.message || '已保存')
    resetNameRule()
    await loadNameRules()
  } catch (e) {
    ruleForm.value.error = apiError(e, '保存失败')
    flash(ruleForm.value.error, true)
  } finally {
    ruleBusy.value = false
  }
}

async function toggleNameRule(r) {
  try {
    await TidyAPI.saveNameRule({
      id: r.id, regex: r.regex, sample: r.sample, note: r.note, disabled: !r.disabled,
    })
    await loadNameRules()
  } catch (e) {
    flash(fail('切换规则状态', e), true)
  }
}

async function deleteNameRule(r) {
  if (!confirm('删除这条命名规则？删掉后这类文件名会回落到内置规则。')) return
  try {
    await TidyAPI.removeNameRule(r.id)
    flash("已删除")
    await loadNameRules()
  } catch (e) {
    flash(fail('删除规则', e), true)
  }
}
</script>
