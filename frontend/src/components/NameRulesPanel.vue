<template>
  <!--
    「文件名解析」+「命名解析」两块内容（2026-10-28 用户：
    「文件名解析和命名解析用卡片装起来（这两个是不是重复了？）」）。

    # 它们不重复，但确实是同一条流水线的两段

    · **文件名解析** = 看结果：给一个文件名，立刻告诉你「这首歌的歌手 / 歌名是什么」。
      一次性试算，不落任何配置 —— 用户拿它验证「我这条命名规则到底对不对」。
    · **命名解析** = 定规则：把「这类文件名该怎么拆」写成一条正则存下来，
      以后每首歌都按它拆，不再调用 AI（省钱、可预期、可手改）。

    所以顺序是「先有规则 → 才有解析结果」。为了把关系说清，「文件名解析」同时显示
    两种结果：**按你的规则**算出来的、**AI 猜**出来的，并标出命中的是不是自定义规则。

    # v2.24.5 视觉统一（2026-10-30 用户：「命名解析规则的下拉框里的 UI 优化一下，符合全局样式」）

    它现在是「系统 → 设置」里**折叠区的内容**（DisclosureSection 自己就是一张白底卡片）。
    原来这里还套了两张 SectionCard 白卡 —— 白底嵌白底，看不出哪层是边界（SectionCard
    的注释里就写着「卡内不要再嵌白底卡片」）；按钮与字号也是各写一套：主按钮是
    `bg-emerald-600` 绿实心（全站主按钮是 .ys-btn-primary 的强调色钴蓝），输入框 12px、
    解释文字 11/11.5/12.5px 混用，选项卡还用了一个 `grid-cols-2` 却塞进
    `sm:col-span-2 lg:col-span-3` 的说明段（列不存在的空位）。
    现在统一成设置页其余小节的语汇：
      · 不再内嵌白卡，改用「小标题 + 灰字说明」+ 浅底分区（--ys-surface-sunken）；
      · 输入框走 `--ys-radius-control` + 13px（与「存储目录」一致），焦点样式交给全局
        `input:focus-visible`（描边变强调色），不在组件里另画；
      · 按钮一律 .ys-btn / .ys-btn-sm / .ys-btn-primary / .ys-btn-ghost(-danger)；
      · 列表用「一框多行 + 行间 1px 结构线」，跟「取音渠道」那条列表同款；
      · 空态用 .ys-empty（虚线框 + 11px），不再手写虚线卡片。
  -->
  <div class="space-y-4">
    <!-- ═════ 一、文件名解析（试算，看结果）═════ -->
    <section>
      <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <h3 class="text-[12.5px] font-semibold text-[var(--ys-text)]">文件名解析</h3>
        <p class="text-[11px] leading-snug text-[var(--ys-text-aux)]">
          只是试算，不改任何配置 —— 要改拆分行为请看下面的「规则」
        </p>
      </div>

      <div class="mt-2 flex items-center gap-2">
        <input
          v-model="aiFilename"
          class="min-w-0 flex-1 rounded-[var(--ys-radius-control)] border border-[var(--ys-line-control)] bg-white px-3 py-2 font-mono text-[13px] text-[var(--ys-text)] placeholder:text-[var(--ys-text-placeholder)]"
          placeholder="【无损】周杰伦 - 晴天.flac"
          @keydown.enter="runParse"
        />
        <button
          type="button"
          class="ys-btn shrink-0"
          :class="parsing ? 'bg-black/[0.04] text-[var(--ys-text-aux)]' : 'ys-btn-primary'"
          :disabled="parsing"
          @click="runParse"
        >{{ parsing ? '解析中…' : '解析' }}</button>
      </div>

      <p v-if="dir" class="mt-1.5 text-[11px] leading-snug text-[var(--ys-text-aux)]">
        试算用的文件名可以照抄曲库里的，例如 <span class="font-mono">{{ dir }}</span> 下的某一首
      </p>

      <div v-if="parseResult" class="mt-2 grid gap-2 sm:grid-cols-2">
        <div class="rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] p-2.5">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">按你的规则</span>
            <span
              v-if="parseRules.from_rule"
              class="rounded-full bg-emerald-50 px-1.5 py-0.5 text-[10px] text-emerald-600"
            >命中自定义规则</span>
            <span
              v-else
              class="rounded-full bg-[var(--ys-surface-control)] px-1.5 py-0.5 text-[10px] text-[var(--ys-text-muted)]"
            >内置规则</span>
            <span
              v-if="parseRules.suspect"
              class="rounded-full bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-700"
            >可能拆错</span>
          </div>
          <div class="mt-1 space-y-0.5 text-[11.5px] text-[var(--ys-text-secondary)]">
            <div>歌手：<span class="font-medium text-[var(--ys-text)]">{{ parseRules.artist || '(空)' }}</span></div>
            <div>歌名：<span class="font-medium text-[var(--ys-text)]">{{ parseRules.title || '(空)' }}</span></div>
          </div>
        </div>

        <div class="rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] p-2.5">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">AI 猜的</span>
            <span
              v-if="parseAiDisabled"
              class="rounded-full bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-700"
            >AI 未配置</span>
          </div>
          <div class="mt-1 space-y-0.5 text-[11.5px] text-[var(--ys-text-secondary)]">
            <div>歌手：<span class="font-medium text-[var(--ys-text)]">{{ parseAi.artist || '(空)' }}</span></div>
            <div>歌名：<span class="font-medium text-[var(--ys-text)]">{{ parseAi.title || '(空)' }}</span></div>
            <div v-if="parseAi.album" class="text-[var(--ys-text-aux)]">专辑：{{ parseAi.album }}</div>
          </div>
        </div>
      </div>

      <p v-if="parseResult && differs" class="mt-1.5 text-[11px] leading-snug text-amber-700">
        两边结论不一致 —— 以「按你的规则」为准（AI 只看文件名，看不到你的规则）。
        想让它按你的规矩拆，就到下面把规则配好。
      </p>
    </section>

    <!-- ═════ 二、规则（配规则，改行为）═════ -->
    <section class="border-t border-[var(--ys-border)] pt-4">
      <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <h3 class="text-[12.5px] font-semibold text-[var(--ys-text)]">规则</h3>
        <p class="text-[11px] leading-snug text-[var(--ys-text-aux)]">
          自定义规则优先于内置规则；保存前可以先校验
        </p>
      </div>

      <!-- 样例 + 一句解释：AI 靠这两样写正则 -->
      <div class="mt-2 grid gap-2 sm:grid-cols-2">
        <label class="block min-w-0">
          <span class="text-[11px] font-medium text-[var(--ys-text-muted)]">文件名样例</span>
          <input
            v-model="ruleForm.sample"
            class="mt-1 w-full rounded-[var(--ys-radius-control)] border border-[var(--ys-line-control)] bg-white px-3 py-2 font-mono text-[13px] text-[var(--ys-text)] placeholder:text-[var(--ys-text-placeholder)]"
            placeholder="【无损】Jay_Chou_晴天_2003.mp3"
          />
        </label>
        <label class="block min-w-0">
          <span class="text-[11px] font-medium text-[var(--ys-text-muted)]">一句话解释（可选）</span>
          <input
            v-model="ruleForm.desc"
            class="mt-1 w-full rounded-[var(--ys-radius-control)] border border-[var(--ys-line-control)] bg-white px-3 py-2 text-[13px] text-[var(--ys-text)] placeholder:text-[var(--ys-text-placeholder)]"
            placeholder="例如：歌手_歌名_年份，歌手用下划线连写"
          />
        </label>
      </div>

      <div class="mt-2 flex flex-wrap items-center gap-2">
        <button type="button" class="ys-btn ys-btn-primary" :disabled="ruleBusy" @click="genNameRule">
          <Sparkles class="h-3.5 w-3.5" :stroke-width="1.8" />
          {{ ruleBusy ? '生成中…' : 'AI 生成规则' }}
        </button>
        <!-- 保存之后的行为与代价：整库按这条规则解析、不再调用 AI。
             用户点「保存规则」之前要知道这件事（它决定「值不值得花一次 AI」）。 -->
        <span class="text-[11px] leading-snug text-[var(--ys-text-aux)]">
          保存后整库按这条规则解析，<b class="font-semibold text-[var(--ys-text-muted)]">不再调用 AI</b>
        </span>
      </div>

      <!-- 生成结果：正则可编辑，校验通过才让保存 -->
      <div
        v-if="ruleForm.regex"
        class="mt-2 space-y-2 rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] p-3"
      >
        <div
          v-if="ruleForm.error"
          class="rounded-[6px] bg-[#fff1f0] px-2 py-1.5 text-[11px] leading-snug text-[var(--ys-danger)]"
        >没通过校验：{{ ruleForm.error }}</div>
        <input
          v-model="ruleForm.regex"
          class="w-full rounded-[var(--ys-radius-control)] border border-[var(--ys-line-control)] bg-white px-3 py-2 font-mono text-[13px] text-[var(--ys-text)]"
        />
        <div v-if="ruleForm.note" class="text-[11px] leading-snug text-[var(--ys-text-muted)]">{{ ruleForm.note }}</div>
        <div v-if="ruleForm.preview" class="font-mono text-[11px] leading-snug text-emerald-600">
          试算：歌手={{ ruleForm.preview.artist || '(空)' }} · 歌名={{ ruleForm.preview.title || '(空)' }}
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="ys-btn ys-btn-sm" :disabled="ruleBusy" @click="testNameRule">校验</button>
          <button
            type="button"
            class="ys-btn ys-btn-sm ys-btn-primary"
            :disabled="ruleBusy || !ruleForm.regex"
            @click="saveNameRule"
          >保存规则</button>
          <button type="button" class="ys-btn ys-btn-sm ys-btn-ghost" @click="resetNameRule">取消</button>
        </div>
      </div>

      <!-- 已有规则 -->
      <div
        v-if="nameRules.length"
        class="mt-2 overflow-hidden rounded-[var(--ys-radius-control)] border border-[var(--ys-border)] bg-white"
      >
        <div
          v-for="r in nameRules"
          :key="r.id"
          class="flex items-start gap-2 border-b border-[var(--ys-border)] px-3 py-2.5 last:border-b-0"
        >
          <div class="min-w-0 flex-1">
            <div class="break-all font-mono text-[11.5px] leading-snug text-[var(--ys-text-secondary)]">{{ r.regex }}</div>
            <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-[var(--ys-text-aux)]">
              <span v-if="r.note">{{ r.note }}</span>
              <span v-if="r.sample" class="truncate">样例：<span class="font-mono">{{ r.sample }}</span></span>
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-1.5">
            <span
              class="rounded-full px-1.5 py-0.5 text-[10px]"
              :class="r.disabled
                ? 'bg-[var(--ys-surface-control)] text-[var(--ys-text-muted)]'
                : 'bg-emerald-50 text-emerald-600'"
            >{{ r.disabled ? '已停用' : '生效中' }}</span>
            <button type="button" class="ys-btn ys-btn-sm ys-btn-ghost" @click="toggleNameRule(r)">
              {{ r.disabled ? '启用' : '停用' }}
            </button>
            <button type="button" class="ys-btn ys-btn-sm ys-btn-ghost-danger" @click="deleteNameRule(r)">删除</button>
          </div>
        </div>
      </div>

      <!-- 空态：这句说明没有可点的动作，所以只是提示，不做成按钮卡 -->
      <p v-else class="ys-empty ys-empty-sm mt-2">
        还没有自定义规则 —— 常见命名会由内置规则处理
      </p>
    </section>

    <!-- 这个面板自己的一句话反馈（原来靠宿主页的 flash 槽） -->
    <InlineNotice v-if="notice" :text="notice" :tone="noticeOk ? 'ok' : 'error'" :log-link="!noticeOk" />
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Sparkles } from 'lucide-vue-next'
import { AIAPI, TidyAPI } from '../api/client'
import InlineNotice from './InlineNotice.vue'
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

// 试算结果拆成两栏：规则算的（后端 by_rules）+ AI 猜的（顶层字段）
const parseRules = computed(() => {
  const d = parseResult.value || {}
  return d.by_rules || { title: '', artist: '', suspect: false, from_rule: false }
})
const parseAi = computed(() => parseResult.value || {})
// 「AI 未配置」：后端降级时两个字段都是空且没配模型
const parseAiDisabled = computed(() => {
  const d = parseResult.value
  return !!d && !d.title && !d.artist && d.by_rules !== undefined
})
const differs = computed(() => {
  const r = parseRules.value
  const a = parseAi.value
  if (!a.title && !a.artist) return false
  return (r.title || '') !== (a.title || '') || (r.artist || '') !== (a.artist || '')
})

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
    parseResult.value = (res && res.data) || {}
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