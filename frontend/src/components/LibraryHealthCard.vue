<template>
  <!--
    文件名体检（用户 2026-10-24）：「飞牛文件管理和官方音乐 App 打开音乐文件夹显示未知错误」。

    根因是盘上有非法文件名的条目（歌曲标签里的 GBK 脏字节会被原样写成文件名，
    fnOS 的文件/媒体接口按 UTF-8 读名字，遇到这类字节整层目录直接报错）。
    下载侧已经在写入前清洗（backend/pkg/filename），这张卡负责把**已有的**查出来修掉。

    放在「体检」分段而不是新开一段：它和曲库体检是同一件事的两种口径
    （一个是元数据缺口，一个是磁盘条目本身不合法），分开放用户要点两次才找得到。

    ── 两种外壳，一份正文（2026-11-04 审查后重构）──
    2026-10-26 用户：「下面的文件名体检可以与歌名修复合并，并去掉相同的功能」。
    merge=True 时不开自己的卡片，只渲染一段带分隔线的子块，由「歌名修复」那张卡片包住。

    原来正文只写在 SectionCard 的 #body 里，merge 分支只有一个空的 `<slot name="body" />`，
    于是并进「歌名修复」时**扫完什么都不显示**（按钮只从「开始扫描」变成「重新扫描」）。
    现在正文抽到 LibraryHealthBody.vue，两个外壳都渲染它，扫描/修复状态仍由本组件持有。
  -->
  <div v-if="merge" class="mt-4 border-t border-[var(--ys-border)] pt-3">
    <div class="flex items-center gap-2">
      <ShieldAlert class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-muted)]" />
      <div class="min-w-0 flex-1">
        <div class="text-[12px] font-semibold text-[var(--ys-text)]">文件名体检</div>
        <div class="mt-0.5 truncate text-[10.5px] text-[var(--ys-text-aux)]">修「文件夹显示未知错误」：非法文件名、控制字符、0 字节音频、下载残留</div>
      </div>
      <button
        class="ys-press shrink-0 rounded-full px-3 py-1.5 text-[11.5px] font-medium transition-colors"
        :class="scanning ? 'bg-[var(--ys-border)] text-[var(--ys-text-aux)]' : 'bg-[var(--ys-accent)] text-white hover:brightness-105'"
        :disabled="scanning || fixing"
        @click="runScan"
      >{{ scanning ? '扫描中…' : (report ? '重新扫描' : '开始扫描') }}</button>
    </div>
    <div class="mt-2.5">
      <LibraryHealthBody
        :report="report"
        :scanning="scanning"
        :fixing="fixing"
        :armed="armed"
        :dir-hint="dirHint"
        :fix-msg="fixMsg"
        :fix-tone="fixTone"
        @fix="runFix"
        @unarm="armed = ''"
      />
    </div>
  </div>
  <SectionCard
    v-else
    :icon="ShieldAlert"
    title="文件名体检"
    sub="修「文件夹显示未知错误」：非法文件名、控制字符、0 字节音频、下载残留"
  >
    <template #actions>
      <button
        class="ys-press rounded-full px-3 py-1.5 text-[11.5px] font-medium transition-colors"
        :class="scanning ? 'bg-[var(--ys-border)] text-[var(--ys-text-aux)]' : 'bg-[var(--ys-accent)] text-white hover:brightness-105'"
        :disabled="scanning || fixing"
        @click="runScan"
      >{{ scanning ? '扫描中…' : (report ? '重新扫描' : '开始扫描') }}</button>
    </template>

    <!-- 卡片正文与 merge 分支共用同一份（见 LibraryHealthBody.vue） -->
    <template #default>
      <LibraryHealthBody
        :report="report"
        :scanning="scanning"
        :fixing="fixing"
        :armed="armed"
        :dir-hint="dirHint"
        :fix-msg="fixMsg"
        :fix-tone="fixTone"
        @fix="runFix"
        @unarm="armed = ''"
      />
    </template>
  </SectionCard>
</template>

<script setup>
import { computed, ref } from 'vue'
import { logError, logInfo } from '../services/appLog'
import { ShieldAlert } from 'lucide-vue-next'
import SectionCard from './SectionCard.vue'
import LibraryHealthBody from './LibraryHealthBody.vue'
import { scanHealth, fixHealth, pickFixable } from '../services/libraryHealth.js'
import { fail } from '../services/userMsg.js'

const props = defineProps({
  /** 曲库目录（由父组件传，空则由后端用默认根） */
  dir: { type: String, default: '' },
  /**
   * 并进「歌名修复」那张卡片：不开自己的 SectionCard，只渲染一段子块。
   * 2026-10-26 用户：「下面的文件名体检可以与歌名修复合并」。
   */
  merge: { type: Boolean, default: false },
})

const scanning = ref(false)
const fixing = ref(false)
// 3.6：就地两步确认的武装态（'' | 'names' | 'all'）
const armed = ref('')
const report = ref(null)
const fixMsg = ref('')
const fixTone = ref('error')

// 目录优先级：页面传进来的 > 从「本地曲库」点体检时记下的那个 > 空（后端用默认根）。
// 用户是在某个具体文件夹里发现「未知错误」的，那一层才是他要扫的。
// 目录一律由父组件给（入口点用一次性 payload 传当前目录）。
// 以前这里回退读 localStorage 的 nas_health_dir，会让用户在别处改目录后仍扫旧路径。
const targetDir = computed(() => props.dir || '')
const dirHint = computed(() => targetDir.value || '曲库目录')

async function runScan() {
  if (scanning.value || fixing.value) return
  scanning.value = true
  fixMsg.value = ''
  armed.value = '' // 3.6：重扫后旧报告的「确认」状态必须失效，否则会拿着新报告执行旧意图
  try {
    report.value = await scanHealth(targetDir.value)
    // 扫描只读、可重跑，但用户要能从日志页看出「什么时候扫过、扫出多少问题」
    const r = report.value || {}
    logInfo(
      '曲库扫描',
      `扫描完成：${r.scanned ?? 0} 个文件，发现 ${r.issues?.length ?? r.issueCount ?? 0} 个问题（${r.elapsedMs ?? 0}ms）`,
      r.dir || targetDir.value || '',
    )
  } catch (e) {
    report.value = null
    fixTone.value = 'error'
    fixMsg.value = fail('扫描曲库文件名', e)
    logError('曲库扫描', fixMsg.value, targetDir.value || '')
  } finally {
    scanning.value = false
  }
}

async function runFix(mode) {
  if (fixing.value || !report.value) return
  // 3.6：先武装再执行 —— 第一次点只把按钮换成「确认…」，第二次才真动文件
  if (armed.value !== mode) {
    armed.value = mode
    return
  }
  armed.value = ''
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
      logInfo('曲库修复', parts.join('，'), `mode=${mode} dir=${targetDir.value || ''}`)
    } else if (res.missed && !res.failed.length) {
      // 报告是上一次扫描的，应用重启过 —— 这不是失败，是让用户再扫一次
      fixTone.value = 'error'
      fixMsg.value = '扫描结果已过期（应用可能重启过），再点一次「重新扫描」即可'
    } else {
      fixTone.value = 'error'
      fixMsg.value = res.failed.length ? `一条都没修成功：${res.failed[0].error || '原因未知'}` : '没有可修的东西'
      logError('曲库修复', fixMsg.value, `mode=${mode} failed=${res.failed?.length || 0} missed=${res.missed || 0}`)
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
