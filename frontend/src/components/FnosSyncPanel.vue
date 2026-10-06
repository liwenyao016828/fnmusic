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
  -->
  <div class="rounded-[15px] border border-gray-200 bg-white">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3.5">
      <div class="min-w-0">
        <b class="text-[14px] text-gray-800">飞牛音乐连接</b>
        <p class="mt-1 text-[11px] text-gray-400">推送、曲库匹配与重扫都依赖这条连接</p>
      </div>
      <button
        @click="check"
        :disabled="checking"
        class="shrink-0 rounded-lg bg-blue-500 px-3.5 py-2 text-[12px] font-semibold text-white transition-all hover:bg-blue-600 disabled:opacity-50"
      >{{ checking ? '检查中…' : '检查连接' }}</button>
    </div>

    <div class="space-y-3.5 px-4 py-4">
      <!-- 状态横幅 -->
      <div class="rounded-xl border px-3.5 py-3 text-[12px] leading-relaxed"
           :class="ready ? 'border-emerald-200 bg-emerald-50 text-emerald-800' : 'border-amber-200 bg-amber-50 text-amber-800'">
        <p class="font-medium">
          飞牛连接：{{ ready ? '已连接，可以执行真实写入' : '未连接' }}
          <span v-if="accountName" class="ml-1 text-emerald-700">· {{ accountName }}</span>
        </p>
        <p v-if="statusHint" class="mt-1 opacity-80">{{ statusHint }}</p>
        <p v-if="checkMsg" class="mt-1">{{ checkMsg }}</p>
      </div>

      <!-- 令牌机制说明 + 手工覆盖 -->
      <div class="rounded-xl border border-gray-200 bg-gray-50/60 px-3.5 py-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="min-w-0">
            <b class="text-[12px] text-gray-700">令牌</b>
            <span class="ml-2 rounded-full px-2 py-0.5 text-[10px] font-medium" :class="tokenBadge.class">
              {{ tokenBadge.text }}
            </span>
          </div>
        </div>
        <p class="mt-1.5 text-[11px] leading-relaxed text-gray-500">
          正常情况下<b class="text-gray-600">不用管这里</b> —— 令牌会自动从飞牛音乐读取，飞牛刷新后我们会自动跟随。
          只有当上方显示「未连接」且提示取不到令牌时，才需要手工填一个临时令牌排障。
        </p>

        <div class="mt-2.5 flex flex-wrap items-center gap-2">
          <input
            v-model="tokenInput"
            type="password"
            placeholder="粘贴临时 token（留空并保存 = 清除）"
            class="min-w-[200px] flex-1 rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-[12px] placeholder:text-gray-400 focus:border-emerald-400 focus:outline-none"
          />
          <button
            @click="saveToken"
            :disabled="savingToken"
            class="shrink-0 rounded-lg bg-emerald-500 px-3.5 py-1.5 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
          >{{ savingToken ? '保存中…' : '验证并保存' }}</button>
          <button
            v-if="hasManualToken"
            @click="clearToken"
            :disabled="savingToken"
            class="shrink-0 rounded-lg bg-gray-100 px-3 py-1.5 text-[12px] font-medium text-gray-600 transition-colors hover:bg-gray-200 disabled:opacity-50"
          >清除手工令牌</button>
        </div>
        <p v-if="tokenMsg" class="mt-1.5 text-[11px] text-gray-600">{{ tokenMsg }}</p>
      </div>

      <!-- 曲库重扫 -->
      <div class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 bg-gray-50/60 px-3.5 py-3">
        <div class="min-w-0">
          <b class="text-[12px] text-gray-700">曲库重扫</b>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
            下载与整理完成后会<b class="text-gray-600">自动重扫</b>，一般不用手动点。
            只有你从别处拷歌进音乐目录时，才需要在这里点一下。
          </p>
        </div>
        <button
          @click="rescan"
          :disabled="rescanning || !ready"
          class="shrink-0 rounded-lg bg-gray-100 px-3.5 py-2 text-[12px] font-medium text-gray-600 transition-colors hover:bg-gray-200 disabled:opacity-50"
        >{{ rescanning ? '已排队…' : '触发重扫' }}</button>
      </div>
      <p v-if="rescanMsg" class="text-[11px] text-gray-500">{{ rescanMsg }}</p>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
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
const accountName = computed(() => {
  const a = status.value?.music_account
  if (!a) return ''
  return typeof a === 'string' ? a : (a.name || a.nickname || '')
})
const statusHint = computed(() => (ready.value ? '' : (status.value?.hint || '')))
const hasManualToken = computed(() => status.value?.token_source === 'manual')

/** 令牌来源徽章 —— 让用户一眼看出现在用的是哪一个 */
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
    checkMsg.value = res.data?.note || '连接正常'
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
    tokenMsg.value = res.data?.note || '已保存'
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
    rescanMsg.value = res.data?.note || '已排入重扫队列'
  } catch (e) {
    rescanMsg.value = fail('触发曲库重扫', e)
  } finally {
    rescanning.value = false
    setTimeout(() => { rescanning.value = false }, 1500)
  }
}

onMounted(loadStatus)
</script>
