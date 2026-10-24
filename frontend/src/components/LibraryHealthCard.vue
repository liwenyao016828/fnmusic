<template>
  <!--
    文件名体检（用户 2026-10-24）：「飞牛文件管理和官方音乐 App 打开音乐文件夹显示未知错误」。

    根因是盘上有非法文件名的条目（歌曲标签里的 GBK 脏字节会被原样写成文件名，
    fnOS 的文件/媒体接口按 UTF-8 读名字，遇到这类字节整层目录直接报错）。
    下载侧已经在写入前清洗（backend/pkg/filename），这张卡负责把**已有的**查出来修掉。

    放在「体检」分段而不是新开一段：它和曲库体检是同一件事的两种口径
    （一个是元数据缺口，一个是磁盘条目本身不合法），分开放用户要点两次才找得到。
  -->
  <SectionCard
    :icon="ShieldAlert"
    title="文件名体检"
    sub="修「文件夹显示未知错误」：非法文件名、控制字符、0 字节音频、下载残留"
  >
    <template #actions>
      <button
        class="ys-press rounded-full px-3 py-1.5 text-[11.5px] font-medium transition-colors"
        :class="scanning ? 'bg-[var(--ys-border)] text-[var(--ys-text-faint)]' : 'bg-[var(--ys-accent)] text-white hover:brightness-105'"
        :disabled="scanning || fixing"
        @click="runScan"
      >{{ scanning ? '扫描中…' : (report ? '重新扫描' : '开始扫描') }}</button>
    </template>

    <div v-if="!report" class="rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
      <template v-if="scanning">正在扫描 {{ dirHint }} …<br />文件多的时候要几十秒</template>
      <template v-else>
        还没有扫描结果 —— 点右上「开始扫描」<br />
        <span class="text-[10.5px]">只读扫描，不会改动任何文件；发现问题后你确认了才会修</span>
      </template>
    </div>

    <template v-else>
      <!-- 统计行：把「会让飞牛报错的」和「只是垃圾」分开说，避免用户以为删残留就能修好未知错误 -->
      <div class="grid grid-cols-3 gap-2">
        <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
          <div class="text-[10.5px] text-[var(--ys-text-faint)]">扫描条目</div>
          <div class="mt-0.5 font-mono text-[17px] font-semibold text-[var(--ys-text)]">{{ report.scanned }}</div>
        </div>
        <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
          <div class="text-[10.5px] text-rose-600">会让文件夹报错</div>
          <div class="mt-0.5 font-mono text-[17px] font-semibold" :class="report.summary.fatal ? 'text-rose-600' : 'text-[var(--ys-text)]'">{{ report.summary.fatal }}</div>
        </div>
        <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
          <div class="text-[10.5px] text-amber-600">其他问题</div>
          <div class="mt-0.5 font-mono text-[17px] font-semibold text-[var(--ys-text)]">{{ report.summary.total - report.summary.fatal }}</div>
        </div>
      </div>

      <p class="mt-2 text-[10.5px] text-[var(--ys-text-faint)]">
        扫描目录 <span class="font-mono">{{ report.dir || dirHint }}</span> · 耗时 {{ (report.elapsedMs / 1000).toFixed(1) }}s
        <template v-if="report.truncated"> · 条目太多只看了前面一部分</template>
      </p>

      <div v-if="!report.issues.length" class="mt-2 rounded-[12px] border border-emerald-200 bg-emerald-50 px-3 py-2.5 text-[11px] text-emerald-800">
        这个目录没有发现非法条目 —— 如果飞牛里还是报未知错误，那就是共享权限或索引的问题，
        到飞牛「存储管理 / 共享文件夹」里重新授权一次再试。
      </div>

      <template v-else>
        <!-- 列表：只展前 40 条，够判断性质；修完可以再扫 -->
        <div class="mt-2 max-h-[240px] overflow-y-auto rounded-[12px] border border-[var(--ys-border)]">
          <div
            v-for="(it, i) in visibleIssues"
            :key="it.path + i"
            class="flex items-center gap-2 border-b border-[var(--ys-border)] px-3 py-1.5 last:border-0"
          >
            <span
              class="shrink-0 rounded px-1.5 py-0.5 text-[9.5px] font-semibold"
              :class="causesFolderError(it.kind) ? 'bg-rose-100 text-rose-700' : 'bg-amber-100 text-amber-700'"
            >{{ issueLabel(it.kind) }}</span>
            <span class="min-w-0 flex-1 truncate font-mono text-[10.5px] text-[var(--ys-text-muted)]" :title="it.path">{{ it.name || it.path }}</span>
            <span class="shrink-0 text-[10px] text-[var(--ys-text-faint)]">{{ fixHint(it) }}</span>
          </div>
        </div>
        <p v-if="report.issues.length > visibleIssues.length" class="mt-1 text-[10.5px] text-[var(--ys-text-faint)]">
          还有 {{ report.issues.length - visibleIssues.length }} 条未列出（修复会一起处理）
        </p>

        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button
            v-if="renameCount"
            class="ys-press rounded-full bg-[var(--ys-accent)] px-3 py-1.5 text-[11.5px] font-medium text-white hover:brightness-105 disabled:opacity-60"
            :disabled="fixing"
            @click="runFix('names')"
          >{{ fixing ? '修复中…' : `改名修好 ${renameCount} 条` }}</button>
          <button
            v-if="junkCount"
            class="ys-press rounded-full border border-[var(--ys-border)] px-3 py-1.5 text-[11.5px] font-medium text-[var(--ys-text-muted)] hover:border-[var(--ys-border-strong)] disabled:opacity-60"
            :disabled="fixing"
            @click="runFix('all')"
          >{{ fixing ? '修复中…' : `顺便清掉 ${junkCount} 个残留` }}</button>
        </div>
      </template>

      <InlineNotice class="mt-2" :text="fixMsg" :tone="fixTone" :log-link="false" />
    </template>
  </SectionCard>
</template>

<script setup>
import { computed, ref } from 'vue'
import { ShieldAlert } from 'lucide-vue-next'
import SectionCard from './SectionCard.vue'
import InlineNotice from './InlineNotice.vue'
import {
  scanHealth, fixHealth, issueLabel, causesFolderError, pickFixable,
} from '../services/libraryHealth.js'
import { fail } from '../services/userMsg.js'

const props = defineProps({
  /** 曲库目录（由父组件传，空则由后端用默认根） */
  dir: { type: String, default: '' },
})

const scanning = ref(false)
const fixing = ref(false)
const report = ref(null)
const fixMsg = ref('')
const fixTone = ref('error')

// 目录优先级：页面传进来的 > 从「本地曲库」点体检时记下的那个 > 空（后端用默认根）。
// 用户是在某个具体文件夹里发现「未知错误」的，那一层才是他要扫的。
// 目录一律由父组件给（入口点用一次性 payload 传当前目录）。
// 以前这里回退读 localStorage 的 nas_health_dir，会让用户在别处改目录后仍扫旧路径。
const targetDir = computed(() => props.dir || '')
const dirHint = computed(() => targetDir.value || '曲库目录')
const visibleIssues = computed(() => (report.value?.issues || []).slice(0, 40))
const renameCount = computed(() => pickFixable(report.value?.issues, 'names').length)
const junkCount = computed(() => pickFixable(report.value?.issues, 'all').length - renameCount.value)

function fixHint(it) {
  if (it.fix === 'rename') return it.suggest ? `→ ${it.suggest}` : '可改名'
  if (it.fix === 'delete') return '可删除'
  if (it.fix === 'chmod') return '可改权限'
  return '需手动'
}

async function runScan() {
  if (scanning.value || fixing.value) return
  scanning.value = true
  fixMsg.value = ''
  try {
    report.value = await scanHealth(targetDir.value)
  } catch (e) {
    report.value = null
    fixTone.value = 'error'
    fixMsg.value = fail('扫描曲库文件名', e)
  } finally {
    scanning.value = false
  }
}

async function runFix(mode) {
  if (fixing.value || !report.value) return
  const targets = pickFixable(report.value.issues, mode)
  if (!targets.length) return
  fixing.value = true
  fixMsg.value = ''
  try {
    const res = await fixHealth(targetDir.value, targets, {})
    if (res.fixed) {
      fixTone.value = 'ok'
      const parts = [`已修好 ${res.fixed} 条`]
      if (res.failed.length) parts.push(`${res.failed.length} 条没成功`)
      if (res.missed) parts.push(`${res.missed} 条已过期（重新扫描后再试）`)
      fixMsg.value = parts.join('，') + '；刷新一次飞牛页面就能看到效果'
    } else if (res.missed && !res.failed.length) {
      // 报告是上一次扫描的，应用重启过 —— 这不是失败，是让用户再扫一次
      fixTone.value = 'error'
      fixMsg.value = '扫描结果已过期（应用可能重启过），再点一次「重新扫描」即可'
    } else {
      fixTone.value = 'error'
      fixMsg.value = res.failed.length ? `一条都没修成功：${res.failed[0].error || '原因未知'}` : '没有可修的东西'
    }
    // 修完立刻重扫：让用户看到数字变化，而不是自己去点第二次
    report.value = await scanHealth(targetDir.value)
  } catch (e) {
    fixTone.value = 'error'
    fixMsg.value = fail('修复文件名', e)
  } finally {
    fixing.value = false
  }
}
</script>
