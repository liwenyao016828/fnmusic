<template>
  <!--
    AI 大模型设置（v2.1.31 从「曲库管家 → AI 设置」搬来）。
    放在「账号连接」是因为它本质就是**又一个外部服务凭证**（Base URL + API Key + 模型），
    和飞牛音乐、网易云/QQ 扫码是同一类东西；曲库管家那侧只留曲库操作。

    ⚠️ **默认折叠**：这块有四个小节（配置 / 用量 / 文件名试算 / 命名规则），全展开约 190 行，
    会把这页的主任务「扫码登录」挤出首屏（用户 2026-09-24 选定：扫码卡之后 + 默认折叠）。
    折叠条上必须自带状态徽标与一句摘要 —— 收起不等于看不出没没配好。
  -->
  <div class="mt-5 overflow-hidden rounded-[15px] border border-gray-200 bg-white">
    <button
      class="flex w-full items-center gap-2.5 px-4 py-3.5 text-left transition-colors hover:bg-gray-50"
      @click="toggleOpen"
    >
      <Sparkles class="h-4 w-4 shrink-0 text-emerald-500" />
      <div class="min-w-0 flex-1">
        <b class="block truncate text-[13px] font-semibold text-gray-800">AI 大模型（OpenAI 兼容接口）</b>
        <span class="mt-0.5 block truncate text-[11px] text-gray-400">{{ summaryLine }}</span>
      </div>
      <span :class="[
        'shrink-0 rounded-full border px-2 py-0.5 text-[10px]',
        aiConfig.available ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-gray-200 bg-gray-100 text-gray-400',
      ]">{{ aiConfig.available ? '已就绪' : '未启用' }}</span>
      <ChevronDown
        class="h-4 w-4 shrink-0 text-gray-400 transition-transform" :class="open ? 'rotate-180' : ''"
      />
    </button>

    <div v-if="open" class="space-y-3 border-t border-gray-100 p-4">
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <label class="text-[11px] text-gray-500 md:col-span-2">
          Base URL
          <input v-model="aiForm.base_url" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
            placeholder="https://api.deepseek.com 或 https://ark.cn-beijing.volces.com/api/v3" />
        </label>
        <label class="text-[11px] text-gray-500">
          API Key
          <input v-model="aiForm.api_key" type="password" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
            :placeholder="aiConfig.configured ? '已配置（留空表示不修改）' : 'sk-...'" />
        </label>
        <label class="text-[11px] text-gray-500">
          模型
          <input v-model="aiForm.model" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
            placeholder="deepseek-chat" />
        </label>
        <label class="text-[11px] text-gray-500">
          超时（秒）
          <input v-model.number="aiForm.timeout" type="number" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs" />
        </label>
        <div class="flex items-end gap-2">
          <label class="flex items-center gap-1.5 text-[11px] text-gray-600">
            <input type="checkbox" v-model="aiForm.enabled" class="accent-emerald-500" />启用
          </label>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <button @click="saveAI" :disabled="savingAI"
          class="px-4 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold disabled:opacity-50">
          {{ savingAI ? '保存中...' : '保存配置' }}
        </button>
        <button @click="testAI" :disabled="testingAI"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
          {{ testingAI ? '测试中...' : '连通性测试' }}
        </button>
        <span v-if="aiTestMsg" class="text-[11px]" :class="aiTestOk ? 'text-emerald-600' : 'text-rose-500'">{{ aiTestMsg }}</span>
      </div>

      <!-- 用量 -->
      <div class="pt-3 border-t border-gray-50">
        <div class="flex items-center justify-between mb-2">
          <span class="text-xs font-semibold text-gray-700">用量统计</span>
          <button @click="resetUsage" class="text-[11px] text-gray-400 hover:text-rose-500">清空</button>
        </div>
        <div class="grid grid-cols-2 md:grid-cols-4 gap-2">
          <div class="p-2.5 rounded-lg bg-gray-50 border border-gray-100">
            <div class="text-[10px] text-gray-400">调用次数</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.total_calls || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-gray-50 border border-gray-100">
            <div class="text-[10px] text-gray-400">总 tokens</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.total_tokens || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-gray-50 border border-gray-100">
            <div class="text-[10px] text-gray-400">输入 tokens</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.prompt_tokens || 0 }}</div>
          </div>
          <div class="p-2.5 rounded-lg bg-gray-50 border border-gray-100">
            <div class="text-[10px] text-gray-400">无明细调用</div>
            <div class="text-lg font-bold text-gray-700 font-mono">{{ aiUsage.missing_usage || 0 }}</div>
          </div>
        </div>
        <div v-if="aiUsage.by_kind && Object.keys(aiUsage.by_kind).length" class="mt-2 space-y-0.5">
          <div v-for="(v, k) in aiUsage.by_kind" :key="k" class="flex items-center justify-between text-[11px] text-gray-500">
            <span class="font-mono">{{ k }}</span>
            <span>{{ v.calls }} 次 · {{ v.total }} tokens</span>
          </div>
        </div>
        <p v-else class="mt-2 text-[11px] text-gray-400">暂无调用记录</p>
      </div>

      <!-- 文件名解析试算 -->
      <div class="pt-3 border-t border-gray-50">
        <span class="text-xs font-semibold text-gray-700">文件名语义解析试算</span>
        <div class="flex items-center gap-2 mt-2">
          <input v-model="aiFilename" class="flex-1 px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
            placeholder="【无损】周杰伦 - 晴天.flac" />
          <button @click="runParse" :disabled="parsing"
            class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
            {{ parsing ? '解析中...' : '解析' }}
          </button>
        </div>
        <div v-if="parseResult" class="mt-2 p-2.5 rounded-lg bg-gray-50 border border-gray-100 text-[11px] text-gray-600 font-mono whitespace-pre-wrap">
          {{ JSON.stringify(parseResult, null, 2) }}
        </div>
      </div>

      <!--
        命名解析规则：AI 生成正则 → 校验 → 可手改 → 保存。
        为什么让 AI 产出「规则」而不是「结果」：成本从「每首歌一次调用」降到
        「每种命名习惯一次」，而且正则用户看得懂、能自己改。
        ⚠️ 生成与保存分开 —— AI 产出的正则必须先给用户看。
      -->
      <div class="pt-3 border-t border-gray-50 space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-xs font-semibold text-gray-700">命名解析规则</span>
          <span class="text-[10px] text-gray-400">优先于内置规则</span>
        </div>
        <p class="text-[11px] leading-relaxed text-gray-400">
          内置规则只覆盖常见命名（歌手 - 歌名）。遇到你特有的命名习惯，给一个样例 + 一句解释，
          让 AI 出一条正则；确认无误后保存，之后整库按它解析、不再调用 AI。
        </p>

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
          <button
            :disabled="ruleBusy"
            class="rounded-lg bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
            @click="genNameRule"
          >{{ ruleBusy ? '生成中…' : 'AI 生成规则' }}</button>
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
        <p v-else class="text-[11px] text-gray-400">还没有自定义规则 —— 常见命名会由内置规则处理。</p>
      </div>

      <!-- 这个面板自己的一句话反馈（原来靠宿主页的 flash 槽） -->
      <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Sparkles, ChevronDown } from 'lucide-vue-next'
import { AIAPI, TidyAPI } from '../api/client'
import InlineNotice from './InlineNotice.vue'
import { fail, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'
import { loadBool, savePref } from '../services/prefs'

const aiConfig = ref({})
const aiUsage = ref({})
const aiForm = ref({ enabled: false, base_url: '', api_key: '', model: '', timeout: 20 })
const savingAI = ref(false)
const testingAI = ref(false)
const aiTestMsg = ref('')
const aiTestOk = ref(false)
const aiFilename = ref('')
const parsing = ref(false)
const parseResult = ref(null)

// 展开状态记在窗口记忆里：进来一会儿就又被折叠会很难受（改配置要反复展开）。
const open = ref(loadBool('ai_panel_open', false))

function toggleOpen() {
  open.value = !open.value
  savePref('ai_panel_open', open.value ? '1' : '0')
}

/**
 * 折叠时那行摘要：得让人不展开也知道「配没配、给谁用了」。
 * 用量为 0 时说未启用，比写一堆 0 有用。
 */
const summaryLine = computed(() => {
  const calls = aiUsage.value.total_calls || 0
  if (!aiConfig.value.configured && !aiConfig.value.enabled) {
    return '填服务地址 + API Key 后，风格兜底与疑难命名解析才会用到它'
  }
  const model = aiForm.value.model || aiConfig.value.model || '默认模型'
  return calls > 0
    ? `模型 ${model} · 累计 ${calls} 次调用 · ${aiUsage.value.total_tokens || 0} tokens`
    : `模型 ${model} · 还没调用过`
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

onMounted(() => {
  loadAI()
  loadNameRules()
})

async function loadAI() {
  try {
    const [cfg, usage] = await Promise.all([AIAPI.getConfig(), AIAPI.usage(10)])
    if (cfg.code === 200) {
      aiConfig.value = cfg.data
      aiForm.value = {
        enabled: cfg.data.enabled,
        base_url: cfg.data.base_url || '',
        api_key: '',   // 留空表示不修改
        model: cfg.data.model || '',
        timeout: cfg.data.timeout || 20,
      }
    }
    if (usage.code === 200) aiUsage.value = usage.data
  } catch (e) {
    console.warn('loadAI failed', e)
  }
}

async function saveAI() {
  savingAI.value = true
  try {
    const res = await AIAPI.saveConfig(aiForm.value)
    if (res.code === 200) {
      flash('AI 配置已保存')
      await loadAI()
    } else {
      flash(apiError(res, '保存失败'), true)
    }
  } catch (e) {
    flash(fail('保存 AI 配置', e), true)
  } finally {
    savingAI.value = false
  }
}

async function testAI() {
  testingAI.value = true
  aiTestMsg.value = ''
  try {
    const res = await AIAPI.test()
    aiTestOk.value = res.code === 200
    if (res.code === 200) {
      aiTestMsg.value = `正常：${res.data?.reply || 'ok'}`
    } else {
      aiTestMsg.value = apiError(res, '连接失败')
      logError('测试 AI 连接', aiTestMsg.value, `code=${res.code} message=${res.message}`)
    }
  } catch (e) {
    aiTestOk.value = false
    aiTestMsg.value = fail('测试 AI 连接', e, '连接失败')
  } finally {
    testingAI.value = false
  }
}

async function resetUsage() {
  if (!confirm('确定清空用量统计？')) return
  try {
    await AIAPI.resetUsage()
    await loadAI()
  } catch (e) {
    flash(fail('清空用量统计', e), true)
  }
}

async function runParse() {
  if (!aiFilename.value.trim()) return flash('请输入文件名', true)
  parsing.value = true
  parseResult.value = null
  try {
    const res = await AIAPI.complete({ kind: 'name_parse', filename: aiFilename.value })
    parseResult.value = res.data || {}
    if (res.message && res.message !== 'ok') flash(res.message)
    await loadAI()
  } catch (e) {
    flash(fail('文件名语义解析', e), true)
  } finally {
    parsing.value = false
  }
}

// ── 命名解析规则：AI 生成正则 → 校验 → 可手改 → 保存 ──
//
// ⚠️ **生成与保存分成两步**：AI 产出的正则可能语法错、匹配不上样例、提取不出字段，
// 必须先给用户看。直接存下去再让他在别处发现问题就晚了。
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
    await loadAI()
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
