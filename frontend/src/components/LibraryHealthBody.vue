<template>
  <!--
    文件名体检的**正文**（两种外壳共用一份）：
      · merge=true  → 并进「歌名修复」卡片，只渲染这段子块（外壳画分隔线）；
      · merge=false → 自己开一张 SectionCard（外壳画卡片头）。

    2026-11-04 审查：merge 分支原来只写了 `<slot name="body" />`，而正文全在
    SectionCard 的 #body 里 —— 于是「开始扫描」跑完、report 拿到了，界面上
    什么都不显示（按钮只从「开始扫描」变成「重新扫描」）。正文只写一份，
    两个外壳各自只负责外壳，避免再次分叉。
  -->
  <div
    v-if="!report"
    class="rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] leading-relaxed text-[var(--ys-text-aux)]"
  >
    <template v-if="scanning">正在扫描 {{ dirHint }} …<br />文件多的时候要几十秒</template>
    <template v-else>
      还没有扫描结果 —— 点「开始扫描」<br />
      <span class="text-[10.5px]">只读扫描，不会改动任何文件；发现问题后你确认了才会修</span>
    </template>
  </div>

  <template v-else>
    <!-- 统计行：把「会让飞牛报错的」和「只是垃圾」分开说，避免用户以为删残留就能修好未知错误 -->
    <div class="grid grid-cols-3 gap-2">
      <div class="rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-3 py-2.5">
        <div class="text-[10.5px] text-[var(--ys-text-aux)]">扫描条目</div>
        <div class="mt-0.5 font-mono text-[17px] font-semibold text-[var(--ys-text)]">{{ report.scanned }}</div>
      </div>
      <div class="rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-3 py-2.5">
        <div class="text-[10.5px] text-rose-600">会让文件夹报错</div>
        <div class="mt-0.5 font-mono text-[17px] font-semibold" :class="report.summary.fatal ? 'text-rose-600' : 'text-[var(--ys-text)]'">{{ report.summary.fatal }}</div>
      </div>
      <div class="rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-3 py-2.5">
        <div class="text-[10.5px] text-amber-600">其他问题</div>
        <div class="mt-0.5 font-mono text-[17px] font-semibold text-[var(--ys-text)]">{{ report.summary.total - report.summary.fatal }}</div>
      </div>
    </div>

    <p class="mt-2 text-[10.5px] text-[var(--ys-text-aux)]">
      扫描目录 <span class="font-mono">{{ report.dir || dirHint }}</span> · 耗时 {{ (report.elapsedMs / 1000).toFixed(1) }}s
      <template v-if="report.truncated"> · 条目太多只看了前面一部分</template>
    </p>

    <div v-if="!report.issues.length" class="mt-2 rounded-[12px] border border-emerald-200 bg-emerald-50 px-3 py-2.5 text-[11px] text-emerald-700">
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
          <span class="shrink-0 text-[10px] text-[var(--ys-text-aux)]">{{ fixHint(it) }}</span>
        </div>
      </div>
      <p v-if="report.issues.length > visibleIssues.length" class="mt-1 text-[10.5px] text-[var(--ys-text-aux)]">
        还有 {{ report.issues.length - visibleIssues.length }} 条未列出（修复会一起处理）
      </p>

      <!--
        3.6（2026-10-27）：批量改名 / 清残留原来点了就执行，没有任何确认。
        改名尚可再改回来，**清残留是真删**，删错了只能去回收站翻（而且这里删的不一定进回收站）。
        所以两种动作都走就地两步：第一次点变成「确认…」，第二次才真执行，切走/重扫自动解除武装。
      -->
      <div class="mt-2 flex flex-wrap items-center gap-2">
        <button
          v-if="renameCount"
          class="ys-press rounded-full px-3 py-1.5 text-[11.5px] font-medium text-white hover:brightness-105 disabled:opacity-60"
          :class="armed === 'names' ? 'bg-rose-600' : 'bg-[var(--ys-accent)]'"
          :disabled="fixing"
          @click="$emit('fix', 'names')"
        >{{ fixing ? '修复中…' : (armed === 'names' ? `确认改名 ${renameCount} 条` : `改名修好 ${renameCount} 条`) }}</button>
        <button
          v-if="junkCount"
          class="ys-press rounded-full border px-3 py-1.5 text-[11.5px] font-medium disabled:opacity-60"
          :class="armed === 'all'
            ? 'border-rose-300 bg-rose-50 text-rose-700'
            : 'border-[var(--ys-border)] text-[var(--ys-text-muted)] hover:border-[var(--ys-border-strong)]'"
          :disabled="fixing"
          @click="$emit('fix', 'all')"
        >{{ fixing ? '修复中…' : (armed === 'all' ? `确认删掉 ${junkCount} 个残留` : `顺便清掉 ${junkCount} 个残留`) }}</button>
        <button
          v-if="armed"
          class="px-2 py-1 text-[11px] text-[var(--ys-text-aux)] hover:text-[var(--ys-text)]"
          @click="$emit('unarm')"
        >取消</button>
      </div>
      <p v-if="armed" class="mt-1 text-[10.5px] leading-relaxed text-rose-700">
        <template v-if="armed === 'all'">
          再点一次就会真的删掉这 {{ junkCount }} 个残留文件 —— 删了这里没有还原入口，只能去回收站找。
        </template>
        <template v-else>
          再点一次就会把 {{ renameCount }} 个文件名改成上面建议的样子；改坏了需要手动改回来。
        </template>
      </p>
    </template>

    <InlineNotice class="mt-2" :text="fixMsg" :tone="fixTone" :log-link="false" />
  </template>
</template>

<script setup>
import { computed } from 'vue'
import InlineNotice from './InlineNotice.vue'
import { issueLabel, causesFolderError, pickFixable } from '../services/libraryHealth.js'

const props = defineProps({
  /** scanHealth 的报告（null = 还没扫过） */
  report: { type: Object, default: null },
  scanning: { type: Boolean, default: false },
  fixing: { type: Boolean, default: false },
  /** 就地两步确认的武装态（'' | 'names' | 'all'） */
  armed: { type: String, default: '' },
  dirHint: { type: String, default: '' },
  fixMsg: { type: String, default: '' },
  fixTone: { type: String, default: 'error' },
})

defineEmits(['fix', 'unarm'])

const visibleIssues = computed(() => (props.report?.issues || []).slice(0, 40))
const renameCount = computed(() => pickFixable(props.report?.issues, 'names').length)
const junkCount = computed(() => pickFixable(props.report?.issues, 'all').length - renameCount.value)

function fixHint(it) {
  if (it.fix === 'rename') return it.suggest ? `→ ${it.suggest}` : '可改名'
  if (it.fix === 'delete') return '可删除'
  if (it.fix === 'chmod') return '可改权限'
  return '需手动'
}
</script>
