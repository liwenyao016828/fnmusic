<template>
  <!--
    飞牛音乐连接面板（嵌在「账号连接」页里）。

    与参考实现（flow 的「飞牛同步管理」）的对应关系：
      · 连接状态 / 检查连接        → 有
      · 手工 Token（故障排查）      → 有
      · 「自动续期」               → **我们不需要**：令牌每次调用都从飞牛数据库现读，
                                     飞牛自己刷新后自动跟随。界面上把这点讲清楚，
                                     而不是照抄一个我们用不到的「续期」开关。
      · 同步收藏夹转正             → 无对应概念（那是 flow 的临时推荐目录机制）

    2026-10-23 用户提的两件事，这一版都在这里：
      1. 「令牌」与「曲库重扫」合并成**一行两张卡**（原来是上下两块，占掉一整屏）；
      2. 「清理一下这个页面的文字提示，只有某个功能失败或者需要提醒用户时才显示下方提示」。

    第 2 条的落地方式：除「未连接」这条必须让人看见的横幅之外，**所有常驻说明文字
    都撤掉了** —— 令牌来源徽章已经说明「现在是哪个令牌」，成功/正常不需要再说一遍。
    成功态一律静默（按钮自己的「已排队…」「检查中…」就是反馈），只有失败或需要
    用户动手时才在卡片下方出现那一行。
  -->
  <div class="rounded-xl border border-gray-200 bg-white">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3.5">
      <div class="min-w-0">
        <b class="text-[14px] text-gray-800">飞牛音乐连接</b>
        <!-- 副标题只在「未连接」时出现：连上之后它就是噪音（用户要的是能干活的确认，不是介绍）。 -->
        <p v-if="!ready" class="mt-1 text-[11px] text-gray-400">推送、曲库匹配与重扫都依赖这条连接</p>
      </div>
      <button
        @click="check"
        :disabled="checking"
        class="shrink-0 rounded-lg bg-emerald-500 px-3.5 py-2 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
      >{{ checking ? '检查中…' : '检查连接' }}</button>
    </div>

    <div class="space-y-3 px-4 py-4">
      <!--
        状态横幅：只留「未连接 / 连不上」这一种。
        连上之后整个横幅消失 —— 头部的绿色「检查连接」按钮与账户名已经足够说明状态，
        再用一条绿底横幅复述「已连接」是纯噪音（2026-10-23 用户要求清理文字提示）。
      -->
      <div
        v-if="!ready || statusHint"
        class="rounded-xl border border-amber-200 bg-amber-50 px-3.5 py-3 text-[12px] leading-relaxed text-amber-800"
      >
        <p class="font-medium">飞牛连接：未连接</p>
        <p v-if="statusHint" class="mt-1 opacity-80">{{ statusHint }}</p>
      </div>

      <!-- 检查失败才出现（成功不说什么） -->
      <p v-if="checkMsg" class="text-[11px] text-rose-600">{{ checkMsg }}</p>

      <!-- 令牌 与 曲库重扫：并排两张卡（用户要求「合并到一行，不同卡片」） -->
      <div class="grid gap-2.5 sm:grid-cols-2">
        <!-- ── 令牌 ── -->
        <div class="rounded-xl border border-gray-200 bg-gray-50/60 px-3.5 py-3">
          <div class="flex flex-wrap items-center gap-2">
            <b class="text-[12px] text-gray-700">令牌</b>
            <span class="rounded-full px-2 py-0.5 text-[10px] font-medium" :class="tokenBadge.class">
              {{ tokenBadge.text }}
            </span>
          </div>
          <input
            v-model="tokenInput"
            type="password"
            placeholder="粘贴临时 token（留空并保存 = 清除）"
            class="mt-2 w-full rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-[12px] placeholder:text-gray-400 focus:border-emerald-400 focus:outline-none"
          />
          <div class="mt-2 flex flex-wrap gap-1.5">
            <button
              type="button"
              class="ys-press flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-[11.5px] font-medium text-gray-700 transition-colors hover:border-emerald-300 hover:text-emerald-700 disabled:opacity-50"
              :disabled="savingToken"
              title="正常不用管；取不到令牌时手工填一个排障"
              @click="saveToken"
            >
              <KeyRound class="h-3.5 w-3.5" :stroke-width="1.8" />
              {{ savingToken ? '保存中…' : '验证并保存' }}
            </button>
            <button
              v-if="hasManualToken"
              type="button"
              class="ys-press flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-[11.5px] font-medium text-gray-500 transition-colors hover:border-rose-300 hover:text-rose-600 disabled:opacity-50"
              :disabled="savingToken"
              title="清掉临时令牌，回到自动读取"
              @click="clearToken"
            >
              <Eraser class="h-3.5 w-3.5" :stroke-width="1.8" />
              清除
            </button>
          </div>
          <!-- 只在失败时显示；保存成功由徽章变绿体现 -->
          <p v-if="tokenMsg" class="mt-1.5 text-[11px] text-rose-600">{{ tokenMsg }}</p>
        </div>

        <!-- ── 曲库重扫 ── -->
        <div class="rounded-xl border border-gray-200 bg-gray-50/60 px-3.5 py-3">
          <b class="text-[12px] text-gray-700">曲库重扫</b>
          <div class="mt-2">
            <button
              type="button"
              class="ys-press flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-[11.5px] font-medium text-gray-700 transition-colors hover:border-emerald-300 hover:text-emerald-700 disabled:opacity-50"
              :disabled="rescanning || !ready"
              :title="ready ? '下载整理后会自动重扫；从别处拷歌进来才要手动点' : '先连上飞牛才能重扫'"
              @click="rescan"
            >
              <RefreshCw class="h-3.5 w-3.5" :stroke-width="1.8" :class="rescanning ? 'animate-spin' : ''" />
              {{ rescanning ? '已排队…' : '触发重扫' }}
            </button>
          </div>
          <!-- 同上：只在失败时出现 -->
          <p v-if="rescanMsg" class="mt-1.5 text-[11px] text-rose-600">{{ rescanMsg }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { KeyRound, Eraser, RefreshCw } from 'lucide-vue-next'
import { FnosAPI } from '../api/client'
import { fail, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'

const status = ref(null)
const checking = ref(false)
const checkMsg = ref('')
const tokenInput = ref('')
const tokenMsg = ref('')
const savingToken = ref(false)
const rescanning = ref(false)
const rescanMsg = ref('')

const ready = computed(() => !!status.value?.music_available)
const statusHint = computed(() => (ready.value ? '' : (status.value?.hint || '')))
const hasManualToken = computed(() => status.value?.token_source === 'manual')

/** 令牌来源徽章 —— 让用户一眼看出现在用的是哪一个（这也是「令牌正常」的唯一提示，不再另写一行） */
const tokenBadge = computed(() => {
  switch (status.value?.token_source) {
    case 'manual': return { text: '手工指定', class: 'bg-amber-100 text-amber-700' }
    case 'env': return { text: '环境变量', class: 'bg-blue-100 text-blue-700' }
    case 'db': return { text: '自动读取（飞牛数据库）', class: 'bg-emerald-100 text-emerald-700' }
    default: return { text: '未取到令牌', class: 'bg-gray-200 text-gray-600' }
  }
})

/**
 * 错误处理统一走 services/userMsg.js 的 fail() / apiError()：
 * 界面只显示一句人话，原始报错（含后端 message 与 HTTP 状态）写进日志。
 *
 * ⚠️ 历史坑：以前这里自己从 `e.message` 取文案，而 axios 的 message 恒为
 * 「Request failed with status code 503」—— 后端精心写的排查提示会被丢掉，
 * 用户只看到一个状态码。现在由 describeError() 负责把两者都完整记进日志。
 *
 * 另：下面的「成功」分支一律把消息置空 —— 成功态的反馈交给界面本身
 * （按钮文字变化、徽章变色、状态横幅消失），不再写一行灰字复述一遍。
 */

async function loadStatus() {
  try {
    const res = await FnosAPI.status()
    if (res?.code === 200) status.value = res.data || {}
  } catch (_) {
    // 拉不到状态不影响页面其它部分
  }
}

async function check() {
  checking.value = true
  checkMsg.value = ''
  try {
    const res = await FnosAPI.check()
    const bad = apiError(res, '检查失败')
    if (bad) {
      checkMsg.value = bad
      logError('检查飞牛连接', bad, `code=${res?.code} message=${res?.message}`)
      return
    }
    await loadStatus()
  } catch (e) {
    checkMsg.value = fail('检查飞牛连接', e)
  } finally {
    checking.value = false
  }
}

async function saveToken() {
  savingToken.value = true
  tokenMsg.value = ''
  try {
    const res = await FnosAPI.saveToken(tokenInput.value.trim())
    const bad = apiError(res, '保存失败')
    if (bad) {
      tokenMsg.value = bad
      logError('保存飞牛令牌', bad, `code=${res?.code} message=${res?.message}`)
      return
    }
    tokenInput.value = ''
    await loadStatus()
  } catch (e) {
    tokenMsg.value = fail('保存飞牛令牌', e)
  } finally {
    savingToken.value = false
  }
}

async function clearToken() {
  tokenInput.value = ''
  await saveToken()
}

async function rescan() {
  rescanning.value = true
  rescanMsg.value = ''
  try {
    const res = await FnosAPI.rescan()
    const bad = apiError(res, '触发失败')
    if (bad) {
      rescanMsg.value = bad
      logError('触发曲库重扫', bad, `code=${res?.code} message=${res?.message}`)
      return
    }
    // 成功不写字：按钮的「已排队…」+ 转圈图标就是反馈，1.5 秒后自己复位。
    setTimeout(() => { rescanning.value = false }, 1500)
  } catch (e) {
    rescanMsg.value = fail('触发曲库重扫', e)
    rescanning.value = false
  }
}

onMounted(loadStatus)
</script>
