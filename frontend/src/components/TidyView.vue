<template>
  <!--
    补全整理页（2026-10-21 顶部分段导航重构）

    用户口径：「这个补全整理的页面，不要再用 ⋯ 按钮了，换成顶部的导航栏，
    一个功能页面可以常驻，归类放在一起。」

    重构前：一整页 + 一张「补全·索引·查重」结果卡，六个功能入口（扫描缺失 /
    增量扫描 / 体检 / 查重 / 撤销 / 选项）全收在右上角 ⋯ 后面。
    重构后：壳层在页面顶部给了分段导航（SegmentedControl，与发现 / 下载 /
    系统同一套机制），五个分段各是一个常驻视图，控件归类进自己那段 ——
      · 补全（默认段）：扫描缺失 / 增量扫描（仍在 ⋯）/ 字段 chips / 预览 / 写入进度 / 结果；
      · 查重：查找重复 + 重复组列表 + 干跑预览 / 移入回收站；
      · 体检：audit 四格统计（与补全的索引口径各算各的）；
      · 记录：上次整理快照 + 撤销上一次补全的入口；
      · 替换音质：占位段 —— 后端 /api/quality/* 开发中，这里不发任何接口调用。
    分段切换只是 v-show：所有状态都存在本组件的 ref 里，跑完的体检 /
    查重结果切走再切回来不丢。次要动作仍留在 ⋯（用户反对的是主功能
    藏在 ⋯ 后面，不是反对 ⋯ 本身）。
  -->
  <PageShell>
    <template #actions>
      <!-- 每个分段一行主操作，只显示当前段那一条；
           补全段第 3 步不给按钮（确认写入就在预览表里，紧挨着要确认的清单） -->
      <template v-if="sub === 'complete'">
        <button
          v-if="completeStep === 1"
          :disabled="gapsLoading"
          :title="completeHint"
          class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
          @click="scanGaps"
        >{{ gapsLoading ? '统计中…' : '扫描缺失' }}</button>
        <button
          v-else-if="completeStep === 2"
          :disabled="previewing || completing || !selectedFields.length"
          :title="completeHint + (selectedFields.length ? '' : '（先在字段里勾要补的项）')"
          class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
          @click="runComplete"
        >{{ previewing ? '匹配中…' : '预览要写的内容' }}</button>

        <!-- 次要动作保留在 ⋯：现在只剩「增量扫描」 -->
        <OverflowMenu :items="completeMenu" @select="onCompleteMenu" />
      </template>
      <button
        v-else-if="sub === 'dedupe'"
        :disabled="deduping"
        title="扫描曲库，找出内容相同 / 同名的重复曲目"
        class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
        @click="runDedupe"
      >{{ deduping ? '查重中…' : '查找重复' }}</button>
      <button
        v-else-if="sub === 'audit'"
        :disabled="auditing"
        title="直接扫一遍文件统计缺口（与「补全」段的索引口径各算各的）"
        class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
        @click="runAudit"
      >{{ auditing ? '体检中…' : '曲库体检' }}</button>
      <button
        v-else-if="sub === 'records'"
        :disabled="reloading"
        title="重新拉取快照与上一次补全的状态"
        class="ys-press rounded-full border border-[var(--ys-border)] bg-white px-4 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-[var(--ys-surface-2)] disabled:opacity-50"
        @click="onRefresh"
      >{{ reloading ? '刷新中…' : '刷新' }}</button>
    </template>

    <div class="space-y-3.5">
      <!-- ═════ 分段一：补全（默认段，元数据补全主流程） ═════ -->
      <SectionCard
        v-show="sub === 'complete'"
        :icon="Sparkles"
        tone="brand"
        title="元数据补全"
        sub="歌词 / 封面 / 年份，写回文件本身"
        sub-title="补全的歌曲信息一律来自搜索结果，不由 AI 猜填"
      >
        <template #actions>
          <button
            type="button"
            class="flex items-center gap-1.5 rounded-[9px] px-2 py-1 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
            title="曲库目录、补全字段、最多处理几首、主力平台"
            @click="optionsOpen = true"
          >
            <SlidersHorizontal class="h-3.5 w-3.5" :stroke-width="1.8" />
            选项
          </button>
        </template>

        <!-- 一行摘要：目录（可点开选项）、缺口数字 -->
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
          <button
            type="button"
            class="flex min-w-0 max-w-full items-center gap-1.5 rounded-[8px] border border-[var(--ys-border)] bg-black/[0.02] px-2 py-1 text-left transition-colors hover:bg-black/[0.04]"
            title="点开修改曲库目录"
            @click="optionsOpen = true"
          >
            <FolderOpen class="h-3.5 w-3.5 shrink-0 text-[var(--ys-accent)]" :stroke-width="1.8" />
            <span class="min-w-0 truncate font-mono text-[11.5px] text-[var(--ys-text-muted)]">{{ tidyDir || nasDir || '/vol1/Music' }}</span>
          </button>
          <StatLine
            v-if="gaps"
            :items="[
              { label: '首曲目', value: gaps.total },
              { label: '首读过标签', value: gaps.read },
              { label: '首待补', value: gaps.any_field, dot: 'bg-amber-500' },
            ]"
          />
          <span v-if="gapsAtText" class="text-[10.5px] text-[var(--ys-text-faint)]">{{ gapsAtText }}</span>
        </div>

        <!--
          字段选择压成一行 chip（原来 6 张卡片画满一屏）。
          数字 = 该字段缺的曲目数；点一下就勾 / 取消，不需要进抽屉。
        -->
        <div class="mt-3 flex flex-wrap items-center gap-1.5">
          <button
            v-for="f in fieldOptions"
            :key="f.key"
            type="button"
            :title="f.hint"
            class="flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11.5px] transition-colors"
            :class="selectedFields.includes(f.key)
              ? 'border-emerald-500/35 bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]'
              : 'border-[var(--ys-border)] text-[var(--ys-text-faint)] hover:bg-black/[0.03] hover:text-[var(--ys-text-muted)]'"
            @click="toggleField(f.key)"
          >
            <Check v-if="selectedFields.includes(f.key)" class="h-3 w-3" :stroke-width="2.4" />
            <span class="font-medium">{{ f.name }}</span>
            <span v-if="gaps" class="font-mono text-[10.5px]" :class="f.missing ? 'text-amber-600' : 'opacity-50'">{{ f.missing }}</span>
          </button>
          <span class="ml-0.5 text-[11px] text-[var(--ys-text-faint)]">点字段可勾选 / 取消</span>
        </div>

        <!-- 还没读过标签的曲目：缺口无从判断，必须单独说清楚 -->
        <p
          v-if="gaps && (gaps.unread || gaps.unsupported)"
          class="mt-2 border-t border-[var(--ys-border)] pt-2 text-[10.5px] leading-relaxed text-[var(--ys-text-faint)]"
        >
          <span v-if="gaps.unread" class="text-amber-600">另有 {{ gaps.unread }} 首还没读过标签（先「增量扫描」，只能算待查）</span>
          <span v-if="gaps.unread && gaps.unsupported" class="mx-1.5">·</span>
          <span v-if="gaps.unsupported">
            另有 {{ gaps.unsupported }} 首格式写不了标签<template v-if="unsupportedExtsText">（{{ unsupportedExtsText }}）</template>
          </span>
        </p>

        <!-- ── 增量扫描结果（「补全」段 ⋯ → 增量扫描）：计数是索引口径 ── -->
        <div v-if="indexResult" class="mt-3.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3.5 py-2.5 text-[11px] text-[var(--ys-text-muted)]">
          增量扫描：新增 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.added }}</b> ·
          变化 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.changed }}</b> ·
          未变 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.unchanged }}</b> ·
          索引总数 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.index_total }}</b>
          <div v-if="!indexResult.complete" class="mt-1 text-amber-700">
            本次枚举不完整，已跳过删除判定（保护索引不被误清）
          </div>
        </div>

        <!-- ── 预览表：还没写任何文件 ── -->
        <div v-if="completePlan" class="mt-3.5 space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">即将写入（还没有动过文件）</span>
            <span class="text-[11px] text-[var(--ys-text-faint)]">
              共 {{ completePlan.total }} 首 · 匹配到 {{ completePlan.matched }} 首
              <span v-if="completePlan.matched_by_ai" class="text-[var(--ys-text-muted)]">
                · 其中 {{ completePlan.matched_by_ai }} 首由 AI 判断
              </span>
            </span>
          </div>

          <div class="max-h-56 overflow-y-auto rounded-[10px] border border-[var(--ys-border)] bg-white">
            <table class="w-full text-[11px]">
              <thead class="sticky top-0 bg-white text-[var(--ys-text-faint)]">
                <tr>
                  <th class="px-2 py-1.5 text-left font-medium">文件</th>
                  <th class="px-2 py-1.5 text-left font-medium">匹配到</th>
                  <th class="px-2 py-1.5 text-left font-medium">会写</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in completePlan.items" :key="it.path" class="border-t border-[var(--ys-border)]">
                  <td class="max-w-[200px] truncate px-2 py-1 font-mono text-[var(--ys-text-muted)]" :title="it.path">
                    {{ baseName(it.path) }}
                  </td>
                  <td class="px-2 py-1">
                    <template v-if="it.matched">
                      <span class="text-[var(--ys-text)]">{{ it.matched_name }}</span>
                      <span class="text-[var(--ys-text-faint)]"> · {{ platformName(it.matched_by) }}</span>
                      <span v-if="it.matched_by_ai" class="text-[var(--ys-text-muted)]"> · AI 判断</span>
                    </template>
                    <span v-else class="text-amber-600">{{ it.note || '没匹配上' }}</span>
                  </td>
                  <td class="px-2 py-1 text-[var(--ys-text-muted)]">
                    <template v-if="it.matched">
                      <span v-for="c in planCells(it)" :key="c.k" class="mr-1.5 inline-block">
                        {{ c.k }}<b class="font-mono text-[var(--ys-text)]">{{ c.v }}</b>
                      </span>
                      <span v-if="!planCells(it).length">—</span>
                      <span v-if="it.metadata_from" class="block text-[10px] text-[var(--ys-text-faint)]">
                        元数据借自 {{ platformName(it.metadata_from) }}
                      </span>
                      <span v-if="it.note" class="block text-[10px] text-amber-600">{{ it.note }}</span>
                    </template>
                    <span v-else>—</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <button
              :disabled="completing || !completePlan.matched"
              class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
              @click="executeComplete"
            >确认写入 {{ completePlan.matched }} 首</button>
            <button
              :disabled="completing"
              class="rounded-full px-3 py-1.5 text-[12px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              @click="completePlan = null"
            >取消</button>
            <span class="text-[11px] text-[var(--ys-text-faint)]">写入前会自动备份，可随时撤销</span>
          </div>
        </div>

        <!-- ── 补全进度 / 上次结果 ── -->
        <div v-if="completeJob" class="mt-3.5 space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex items-center justify-between gap-2">
            <span class="text-[12px] font-semibold text-[var(--ys-text)]">{{ completeTitle }}</span>
            <span class="flex shrink-0 items-center gap-2">
              <span v-if="completeJob.running" class="font-mono text-[11px] text-[var(--ys-text-faint)]">
                {{ completeJob.done }}/{{ completeJob.total }}
              </span>
              <button
                v-if="completeJob.running && !completeJob.cancelled"
                :disabled="stopBusy"
                class="rounded-[8px] px-2.5 py-1 text-[11px] text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-50"
                title="不再开始新的曲目；已写入的文件保留，可以用「撤销上一次补全」回退"
                @click="stopComplete"
              >{{ stopBusy ? '停止中…' : '停止' }}</button>
            </span>
          </div>

          <div v-if="completeJob.running" class="h-1.5 w-full overflow-hidden rounded-full bg-black/[0.06]">
            <div class="h-full rounded-full bg-emerald-500 transition-all duration-300" :style="{ width: completePercent + '%' }"></div>
          </div>
          <p v-if="completeJob.running && completeJob.current" class="truncate text-[11px] text-[var(--ys-text-faint)]">
            正在处理：{{ completeJob.current }}
          </p>

          <InlineNotice :text="completeJob.error" />

          <p v-if="completeJob.summary" class="text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
            <span v-for="c in summaryCells" :key="c.k" class="mr-2 inline-block">
              {{ c.k }}<b class="font-mono" :class="c.color">{{ c.v }}</b>
            </span>
            <span v-if="completeJob.summary.lyric_rejected" class="text-amber-600">
              · {{ completeJob.summary.lyric_rejected }} 首歌词对不上，已跳过不写
            </span>
            <span v-if="completeJob.summary.cancelled" class="text-rose-600">
              · {{ completeJob.summary.cancelled }} 首没处理（任务被停止）
            </span>
          </p>

          <!-- 「撤销上一次补全」不在这里了：入口归「记录」段（v-show 常驻，状态还在本组件） -->
        </div>

        <!-- ── 补全结果卡片墙（读自文件，不是「打算写成什么」）── -->
        <div v-if="cardsLoading || completedCards.length" class="mt-3.5 space-y-2 rounded-[12px] border border-[var(--ys-border)] bg-white p-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">
              {{ cardsLoading ? '正在回读文件标签…' : '补全后的歌曲信息（读自文件）' }}
            </span>
            <span class="text-[11px] text-[var(--ys-text-faint)]">
              {{ completedCards.length }} 张卡片<span v-if="skippedCards.length"> · {{ skippedCards.length }} 首没补上</span>
            </span>
          </div>

          <div class="flex flex-wrap gap-1.5">
            <span v-for="b in completeBar" :key="b.key"
              :class="[
                'rounded-lg border px-2 py-1 text-[10px]',
                b.selected ? 'border-emerald-500/30 bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]' : 'border-[var(--ys-border)] bg-black/[0.02] text-[var(--ys-text-faint)]',
              ]">
              {{ b.name }} <b class="font-mono">{{ b.count }}</b>
              <i v-if="!b.selected" class="not-italic">· 未勾</i>
            </span>
          </div>

          <div class="grid grid-cols-2 gap-2">
            <div v-for="c in completedCards" :key="c.path" class="flex gap-2 rounded-[10px] border border-[var(--ys-border)] bg-[#FAFAFC] p-2">
              <img v-if="c.hasCover" :src="c.coverUrl" :alt="c.title" class="h-14 w-14 shrink-0 rounded-[6px] object-cover" loading="lazy" />
              <div v-else class="flex h-14 w-14 shrink-0 items-center justify-center rounded-[6px] bg-black/[0.05]">
                <ImageIcon class="h-4 w-4 text-[var(--ys-text-faint)]" />
              </div>
              <div class="min-w-0 flex-1">
                <p class="truncate text-[11px] font-medium text-[var(--ys-text)]" :title="c.path">
                  {{ c.title || baseName(c.path) }}
                </p>
                <p class="truncate text-[10px] text-[var(--ys-text-muted)]">
                  {{ c.artist || '未标注歌手' }}<span v-if="c.album"> · {{ c.album }}</span>
                </p>
                <p class="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-[10px] text-[var(--ys-text-muted)]">
                  <span v-for="fd in cardFields(c)" :key="fd.k">
                    {{ fd.k }}
                    <b :class="['font-mono', fd.wrote ? 'text-[var(--ys-accent)]' : 'text-[var(--ys-text)]']">{{ fd.v }}</b>
                    <i v-if="fd.wrote" class="not-italic text-[var(--ys-accent)]">✚</i>
                  </span>
                </p>
              </div>
            </div>
          </div>
        </div>

        <!-- 没补成的单独一组：绝不混进卡片墙装成「补好了」 -->
        <div v-if="skippedCards.length" class="mt-3 space-y-1 rounded-[12px] border border-amber-200/70 bg-amber-50/50 p-3">
          <p class="text-[11px] font-semibold text-amber-800">这些没补上（{{ skippedCards.length }} 首）</p>
          <p v-for="c in skippedCards" :key="c.path" class="truncate text-[11px] text-amber-900/80">
            {{ baseName(c.path) }} —— {{ c.error || '没匹配到曲目' }}
          </p>
        </div>
      </SectionCard>

      <!-- ═════ 分段二：查重 ═════ -->
      <SectionCard
        v-show="sub === 'dedupe'"
        :icon="Copy"
        title="查重"
        sub="内容相同 / 同名不同版的重复曲目：保留一份，其余移入回收站"
      >
        <div v-if="!dupResult" class="rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] text-[var(--ys-text-faint)]">
          {{ deduping ? '正在查重…' : '还没有查重结果 —— 点右上「查找重复」' }}
        </div>

        <div v-if="dupResult" class="space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[12px] font-semibold text-[var(--ys-text)]">
              发现 <b class="font-mono text-rose-600">{{ dupResult.group_count }}</b> 组重复
            </span>
            <div class="flex gap-1.5">
              <button @click="resolveDup(true)" class="ys-press rounded-full border border-[var(--ys-border)] bg-white px-3 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]">干跑预览</button>
              <button @click="resolveDup(false)" class="ys-press rounded-full bg-rose-500 px-3 py-1 text-[11px] font-medium text-white transition-colors hover:bg-rose-600">移入回收站</button>
            </div>
          </div>
          <div v-if="dupResult.groups.length" class="max-h-64 space-y-1.5 overflow-y-auto">
            <div v-for="(g, i) in dupResult.groups.slice(0, 30)" :key="i" class="rounded-[10px] border border-[var(--ys-border)] bg-white p-2.5 text-[11px]">
              <div class="flex items-center gap-2">
                <span :class="['rounded-full px-1.5 py-0.5 font-mono text-[10px]', g.type === 'content' ? 'bg-rose-50 text-rose-600' : 'bg-amber-50 text-amber-600']">
                  {{ g.type === 'content' ? '内容相同' : '同名不同版' }}
                </span>
                <span class="text-[var(--ys-text-faint)]">{{ g.count }} 个文件</span>
              </div>
              <div class="mt-1 text-[var(--ys-accent)]">保留：{{ baseName(g.keep) }}</div>
              <div class="text-rose-500">清理：{{ g.delete.map(baseName).join('、') }}</div>
            </div>
          </div>
          <div v-if="dupResolveResult" class="rounded-[10px] border border-[var(--ys-border)] bg-white px-3.5 py-2.5 text-[11px] text-[var(--ys-text-muted)]">
            {{ dupResolveResult.dry_run ? '干跑估算' : '执行完成' }}：
            移动 <b class="font-mono">{{ dupResolveResult.moved }}</b> ·
            删除 <b class="font-mono">{{ dupResolveResult.deleted }}</b> ·
            释放 <b class="font-mono">{{ fmtSize(dupResolveResult.freed_bytes) }}</b>
            <div v-if="dupResolveResult.errors && dupResolveResult.errors.length" class="mt-1 flex flex-wrap items-center gap-2 text-rose-500">
              <span>{{ dupResolveResult.errors.length }} 个文件处理失败</span>
              <button class="underline decoration-dotted underline-offset-2 hover:no-underline" @click="navigateTo('logs')">查看日志</button>
            </div>
          </div>
        </div>
      </SectionCard>

      <!-- ═════ 分段三：体检 ═════ -->
      <SectionCard
        v-show="sub === 'audit'"
        :icon="ScanSearch"
        title="曲库体检"
        sub="直接扫文件统计缺口 —— 与「补全」段的索引口径各算各的，数字可能对不上"
      >
        <template v-if="audit && auditAtText" #actions>
          <span class="text-[10.5px] text-[var(--ys-text-faint)]">体检于 {{ auditAtText }}</span>
        </template>

        <div v-if="!audit" class="rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] text-[var(--ys-text-faint)]">
          {{ auditing ? '正在体检…' : '还没有体检结果 —— 点右上「曲库体检」' }}
        </div>
        <div v-else class="grid grid-cols-2 gap-2 md:grid-cols-4">
          <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
            <div class="text-[10.5px] text-[var(--ys-text-faint)]">曲目总数</div>
            <div class="mt-0.5 font-mono text-[19px] font-semibold text-[var(--ys-text)]">{{ audit.total }}</div>
          </div>
          <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
            <div class="text-[10.5px] text-amber-600">缺歌词</div>
            <div class="mt-0.5 font-mono text-[19px] font-semibold text-[var(--ys-text)]">{{ audit.no_lyric }}</div>
          </div>
          <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
            <div class="text-[10.5px] text-amber-600">缺封面</div>
            <div class="mt-0.5 font-mono text-[19px] font-semibold text-[var(--ys-text)]">{{ audit.no_cover }}</div>
          </div>
          <div class="rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2.5">
            <div class="text-[10.5px] text-rose-600">两者都缺</div>
            <div class="mt-0.5 font-mono text-[19px] font-semibold text-[var(--ys-text)]">{{ audit.both_missing }}</div>
          </div>
        </div>
      </SectionCard>

      <!--
        文件名体检（用户 2026-10-24）：「飞牛文件和官方音乐 App 打开音乐文件夹显示未知错误」。
        和上面的曲库体检是同一件事的两种口径 —— 上面数的是「元数据缺口」，
        这张看的是「磁盘条目本身不合法」。放同一段，用户不用点两次才找得到。
      -->
      <LibraryHealthCard v-show="sub === 'audit'" :dir="healthDir || tidyDir || nasDir" class="mt-3" />

      <!-- ═════ 分段四：记录（整理快照 + 撤销入口） ═════ -->
      <SectionCard
        v-show="sub === 'records'"
        :icon="History"
        title="整理记录"
        sub="上次整理快照与撤销入口"
      >
        <p v-if="statsLoading" class="text-[11px] text-[var(--ys-text-faint)]">正在读取上次整理快照…</p>
        <p
          v-else-if="indexStats"
          class="text-[11px] leading-relaxed text-[var(--ys-text-faint)]"
          title="快照读自索引；若和「补全」段「扫描缺失」的数字对不上，说明索引过期了 —— 跑一次「补全」段 ⋯ 里的「增量扫描」即可更新"
        >{{ lastScanText || '索引还没整理过' }} —— 跑「补全」段的「增量扫描」会更新</p>
        <p v-else class="text-[11px] text-[var(--ys-text-faint)]">还没有索引快照 —— 点右上「刷新」再试</p>

        <!-- 撤销入口：原来既在 ⋯ 里、又贴在补全进度条下；按归类放进「记录」段 -->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3.5 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">撤销上一次补全</span>
            <button
              :disabled="undoBusy || !completeJob || !completeJob.undo_available || completeJob.running"
              class="rounded-[8px] bg-black/[0.04] px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.07] disabled:opacity-50"
              @click="undoComplete"
            >{{ undoBusy ? '还原中…' : '还原' }}</button>
          </div>
          <p class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            <template v-if="completeJob && completeJob.undo_available && !completeJob.running">只把上一次补全改过的文件还原成补全前的样子，别的不碰</template>
            <template v-else-if="completeJob && completeJob.running">补全任务还在跑 —— 结束后才能撤销</template>
            <template v-else>目前没有可撤销的补全 —— 补全写入前会自动备份，跑完一次这里就会出现「还原」</template>
          </p>
        </div>
      </SectionCard>

      <!-- ═════ 分段五：替换音质（真接口 /api/quality/scan|replace，后端 pkg/intercept/quality.go） ═════ -->
      <SectionCard
        v-show="sub === 'quality'"
        :icon="Gauge"
        title="替换音质"
        sub="对比三条渠道能拿到的最高档位，勾选后替换本地文件"
      >
        <!-- 控制行：范围 / 开始（running 就地变进度形态） / 停止 / 数量上限 -->
        <div class="flex flex-wrap items-center gap-2">
          <SegmentedControl
            :model-value="qMode"
            :segments="[{ id: 'lossy', name: '仅看有损' }, { id: 'all', name: '全部曲库' }]"
            @update:model-value="setQMode"
          />
          <button
            type="button"
            class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
            :disabled="qRunning || qBusy"
            :title="qRunning ? '扫描进行中 —— 用右边的停止按钮中断查看' : '让三条渠道逐条对比曲库能拿到的最高音质'"
            @click="startQualityScan"
          >{{ qRunning ? '扫描中…' : '开始扫描' }}</button>
          <button
            v-if="qRunning"
            type="button"
            class="ys-press grid h-8 w-8 shrink-0 place-items-center rounded-[10px] border border-[var(--ys-border)] bg-white text-[var(--ys-text-muted)] transition-colors hover:border-[var(--ys-border-strong)]"
            title="停止（只停止本页查看进度，后端扫描可能仍在继续）"
            @click="stopQualityScan"
          >
            <Square class="h-3.5 w-3.5" :stroke-width="2" />
          </button>
          <label class="ml-auto flex items-center gap-1.5 text-[11px] text-[var(--ys-text-muted)]">
            上限
            <input
              v-model.number="qLimit"
              type="number"
              min="1"
              max="500"
              class="ys-num w-16 rounded-[10px] border border-[var(--ys-border)] bg-[#FAFAFC] px-2 py-1.5 text-center font-mono text-[12px] text-[var(--ys-text)] focus:border-[var(--ys-accent)] focus:bg-white focus:outline-none disabled:opacity-50"
              :disabled="qRunning || qBusy"
            />
            条
          </label>
        </div>

        <!-- running：scanned/total + 细进度条（数字一律 .ys-num） -->
        <div v-if="qRunning" class="mt-3">
          <div class="flex items-center justify-between text-[11px] text-[var(--ys-text-muted)]">
            <span>已扫描 <b class="ys-num text-[var(--ys-text)]">{{ qScan?.scanned || 0 }}</b> / <b class="ys-num text-[var(--ys-text)]">{{ qScan?.total || 0 }}</b> 条</span>
            <span>已发现可替换 <b class="ys-num text-[var(--ys-text)]">{{ qScan?.upgradeable || 0 }}</b> 条</span>
          </div>
          <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-black/[0.06]">
            <div class="h-full rounded-full bg-emerald-500 transition-all duration-300" :style="{ width: qPercent + '%' }"></div>
          </div>
        </div>

        <!-- 状态行：running / 已中断（诚实文案） / truncated -->
        <p v-if="qStatusText" class="mt-2.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">{{ qStatusText }}</p>

        <!-- 失败态：state=failed 或后端给了 error，或请求层失败（接口未上线时走这里，不白屏） -->
        <InlineNotice
          v-if="qScan && (qScan.state === 'failed' || qScan.error)"
          class="mt-2.5"
          :text="'扫描失败：' + (qScan.error || '后端没有给出原因，详情见日志')"
        />
        <InlineNotice v-if="qErrorText" class="mt-2.5" :text="qErrorText" />

        <!-- 结果表：列表 + 发丝线（不做卡片套卡片）。只有可替换且未替换的行才能勾选 -->
        <div v-if="qRows.length" class="mt-3 overflow-hidden rounded-[12px] border border-[var(--ys-border)] bg-white">
          <div class="overflow-y-auto" :style="{ maxHeight: '440px' }">
            <div
              v-for="e in qRows"
              :key="e.path"
              class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-[var(--ys-border)] px-3 py-2.5 last:border-b-0"
              :class="e.replaced ? 'bg-[var(--ys-surface-2)]' : ''"
            >
              <input
                type="checkbox"
                class="h-4 w-4 shrink-0 accent-[var(--ys-accent)]"
                :checked="qSelected.has(e.path)"
                :disabled="!e.upgradeable || e.replaced"
                :title="e.replaced ? '已替换' : (e.upgradeable ? '勾选后可用最高音质替换' : '已是各渠道最高音质，无需替换')"
                @change="toggleQSelect(e)"
              />
              <div class="min-w-0 flex-1 basis-[180px]">
                <div class="truncate text-[12.5px] font-medium text-[var(--ys-text)]">
                  {{ e.title || baseName(e.path) }}
                  <span v-if="e.ext" class="ml-1 text-[10px] uppercase text-[var(--ys-text-faint)]">.{{ String(e.ext).replace(/^\./, '') }}</span>
                </div>
                <div class="mt-0.5 truncate text-[11px] text-[var(--ys-text-muted)]">{{ e.artist || '未知歌手' }}</div>
                <div v-if="e.rowError" class="mt-0.5 text-[10.5px] leading-relaxed text-red-500">替换失败：{{ e.rowError }}</div>
              </div>
              <span
                class="shrink-0 rounded-full bg-[var(--ys-surface-sunken)] px-2 py-0.5 text-[10.5px] text-[var(--ys-text-muted)]"
                title="当前本地文件的档位"
              >{{ e.current_tier || '—' }}</span>
              <div class="flex shrink-0 gap-1.5">
                <div
                  v-for="cell in qChannelCells(e)"
                  :key="cell.ch"
                  class="w-[52px] rounded-[6px] border border-[var(--ys-border)] px-1 py-0.5 text-center"
                  :title="cell.hint"
                >
                  <div class="text-[9px] leading-tight text-[var(--ys-text-faint)]">{{ cell.label }}</div>
                  <div
                    class="truncate text-[10px] leading-tight"
                    :class="cell.tier ? 'ys-num text-[var(--ys-text-muted)]' : 'text-[var(--ys-text-faint)]'"
                  >{{ cell.tier || '拿不到' }}</div>
                </div>
              </div>
              <span v-if="e.replaced" class="shrink-0 text-[10.5px] text-[var(--ys-text-muted)]">已替换为 {{ e.newTier || '更高档位' }}</span>
            </div>
          </div>
        </div>

        <!-- 空态（需求 m02848：一条都没有时不留白） -->
        <div v-else-if="qState === 'done'" class="mt-3 rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
          曲库里的歌已经是各渠道能拿到的最高音质了
        </div>
        <div v-else-if="qState === 'idle' && !qRunning && !qErrorText" class="mt-3 rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
          还没有扫描结果 —— 点「开始扫描」，让洛雪 / musicdl / 原生三条渠道逐条对比
        </div>

        <!-- 最近替换记录：原地覆盖看不出变化，它是唯一凭据（需求 m04229） -->
        <div v-if="qRecords.length" class="mt-3">
          <div class="flex items-center gap-2">
            <span class="text-[11px] font-medium text-[var(--ys-text-muted)]">最近替换</span>
            <span class="text-[10px] text-[var(--ys-text-faint)]">共 {{ qScan?.records?.length || 0 }} 条</span>
            <!--
              自动补强的记录也在这本账上（2026-10-23）：补强轮是「替换音质」的自动版，
              结果只写进自动下载记录的话，用户来这一页自查就永远看不到它。
              这里把两者的条数分开说清，免得看到没印象的条目。
            -->
            <span v-if="qAutoCount" class="text-[10px] text-[var(--ys-text-faint)]">
              （其中后台自动补强 {{ qAutoCount }} 条）
            </span>
          </div>
          <div class="mt-1.5 overflow-hidden rounded-[12px] border border-[var(--ys-border)] bg-white">
            <div
              v-for="(r, i) in qRecords"
              :key="i"
              class="flex items-center gap-2 border-b border-[var(--ys-border)] px-3 py-2 last:border-b-0"
            >
              <span
                class="h-1.5 w-1.5 shrink-0 rounded-full"
                :class="r.ok ? 'bg-emerald-500' : 'bg-[var(--ys-text-faint)]'"
              ></span>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-1.5">
                  <span class="truncate text-[12px] text-[var(--ys-text)]">{{ r.label }}</span>
                  <span
                    v-if="r.by === 'upgrade'"
                    class="shrink-0 rounded bg-[var(--ys-bg-soft)] px-1 py-px text-[9.5px] text-[var(--ys-text-muted)]"
                    title="后台音质补强轮自动试的（每 20 分钟一轮，失败会隔天再试）—— 不是你手动点的替换"
                  >自动补强</span>
                </div>
                <div class="mt-0.5 truncate text-[10.5px] text-[var(--ys-text-muted)]">
                  {{ r.tierText }}<span v-if="r.channel"> · {{ Q_CHANNEL_LABELS[r.channel] || r.channel }}</span>
                </div>
              </div>
              <span class="ys-num shrink-0 text-[10px] text-[var(--ys-text-faint)]">{{ r.when }}</span>
            </div>
          </div>
        </div>

        <!-- 勾选浮出批量条（底部深色玻璃，项目已有组件） -->
        <SelectionBar
          :count="qSelectedCount"
          :actions="[{ key: 'replace', label: '用最高音质替换', icon: Replace, disabled: qBusy }]"
          @action="onQAction"
          @clear="qClearSelection"
        />
      </SectionCard>
    </div>

    <!-- ── 补全选项抽屉：目录 / 最多几首 / 覆盖 / AI / 主力平台 ── -->
    <SideSheet v-model="optionsOpen" title="补全选项" subtitle="默认值就是推荐值；改完只影响下一次补全">
      <div class="space-y-5">
        <label class="block">
          <span class="text-[12px] font-medium text-[var(--ys-text)]">曲库目录</span>
          <input
            v-model="tidyDir"
            class="mt-1.5 w-full rounded-[10px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3 py-2 font-mono text-[12px] text-[var(--ys-text)] transition-colors focus:border-[var(--ys-accent)] focus:bg-white focus:outline-none"
            :placeholder="nasDir || '/vol1/Music'"
          />
          <span class="mt-1 block text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            补全整理、体检、增量扫描、查重都以这个目录为准；留空就用设置里的默认曲库
          </span>
        </label>

        <label class="block">
          <span class="text-[12px] font-medium text-[var(--ys-text)]">本次最多处理</span>
          <div class="mt-1.5 flex items-center gap-2">
            <input v-model.number="completeLimit" type="number" min="1" max="200"
              class="w-20 rounded-[10px] border border-[var(--ys-border)] bg-[#FAFAFC] px-2 py-2 text-center font-mono text-[12px]" />
            <span class="text-[11px] text-[var(--ys-text-muted)]">首（每首要走两次外网请求）</span>
          </div>
        </label>

        <div class="space-y-2.5">
          <label class="flex cursor-pointer items-start gap-2.5">
            <input v-model="overwriteExisting" type="checkbox" class="mt-0.5" />
            <span>
              <span class="block text-[12px] font-medium text-[var(--ys-text)]">覆盖已有的歌词 / 封面</span>
              <span class="mt-0.5 block text-[11px] text-[var(--ys-text-faint)]">不勾就只补空的字段</span>
            </span>
          </label>
          <label class="flex cursor-pointer items-start gap-2.5" :title="aiGenreHint">
            <input v-model="allowAIGenre" type="checkbox" class="mt-0.5" :disabled="!selectedFields.includes('genre')" />
            <span>
              <span class="block text-[12px] font-medium text-[var(--ys-text)]">允许 AI 推断风格</span>
              <span class="mt-0.5 block text-[11px] text-[var(--ys-text-faint)]">只在勾了「风格」时可用；结果会标出「AI 判断」</span>
            </span>
          </label>
        </div>

        <div>
          <span class="text-[12px] font-medium text-[var(--ys-text)]">主力平台</span>
          <p class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            决定搜索顺序，以及兜底命中缺字段时回哪家借元数据
          </p>
          <div class="mt-2 flex flex-wrap gap-1.5">
            <button
              v-for="p in platformChips" :key="p.id"
              type="button"
              @click="togglePreferred(p.id)"
              :title="p.preferred ? '点击取消主力（仍会在兜底时用到）' : '点击设为主力平台'"
              :class="[
                'rounded-full border px-2.5 py-1 text-[11.5px] transition-colors',
                p.preferred ? 'border-emerald-500/35 bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]' : 'border-[var(--ys-border)] bg-white text-[var(--ys-text-faint)] hover:text-[var(--ys-text-muted)]',
              ]"
            >{{ p.name }}</button>
          </div>
        </div>
      </div>
    </SideSheet>
  </PageShell>
</template>

<script setup>
// ── 补全整理（TidyView）─────────────────────────────────────────────
// 2026-10-15 框架重做：从 LibraryManager.vue 的 complete tab 原样搬出。
// 逻辑零改动，只有两处刻意变化：
//   1. 页面级 flash（message/error ref + 底部 InlineNotice）→ 全局 feedback 通知；
//   2. 补全卡片补上了 coverUrl（旧代码 cardCover 定义了却从未被调用，
//      模板里 `:src="c.coverUrl"` 一直是 undefined —— 卡片封面其实从来没显示过）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  Sparkles, Check, FolderOpen, SlidersHorizontal,
  ScanSearch, FolderSearch, Copy, History, Gauge, ImageIcon, Replace, Square,
} from 'lucide-vue-next'
import PageShell from './PageShell.vue'
import SectionCard from './SectionCard.vue'
import InlineNotice from './InlineNotice.vue'
import OverflowMenu from './OverflowMenu.vue'
import SideSheet from './SideSheet.vue'
import StatLine from './StatLine.vue'
import SegmentedControl from './SegmentedControl.vue'
import SelectionBar from './SelectionBar.vue'
import LibraryHealthCard from './LibraryHealthCard.vue'
import { TidyAPI, TagsAPI, NasAPI, ConfigAPI, QualityAPI, DEFAULT_AUTO_DOWNLOAD_CHANNELS } from '../api/client'
import { loadPref, savePref, loadBool, loadCache, saveCache } from '../services/prefs'
import {
  FIELD_DEFS, DEFAULT_FIELDS, parseFields, toggleFieldKey, platformName, planCells,
  summaryCells as _summaryCells, summaryBar, cardModel, cardFields, mapWithLimit,
} from '../services/libraryFields'
// 这一页「看法」上的规则（相对时间 / 缺口文案 / 补全四态 / 三步流程）都在服务层，
// 由 libraryView.test.mjs 钉住 —— 组件里只做 ref → 函数参数的桥接，不本地重写。
import {
  lastScanText as _lastScanText, gapsAtText as _gapsAtText,
  unsupportedExtsText as _unsupportedExtsText,
  completePercent as _completePercent, aiMatchedCount as _aiMatchedCount,
  completeDoneText, completeTitleText as _completeTitleText,
  COMPLETE_STEPS, completeStep as _completeStep, completeHint as _completeHint,
} from '../services/libraryView'
import { fail as failText, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'
import { ok as notifyOk, fail as notifyFail } from '../services/feedback'
import { currentView, navigateTo, onNavigate, segmentOf } from '../services/navBus'

/** 页面提示统一走全局 feedback（右下角浮层），不再占页面底部一条 InlineNotice */
function flash(msg, isError = false) {
  if (isError) notifyFail(msg)
  else notifyOk(msg)
}

// ── 目录与状态 ──
const nasDir = computed(() => loadPref('nas_dir', ''))
const tidyDir = ref('')
/**
 * 体检卡片要扫哪个目录。
 *
 * 2026-10-24 用户报的坑：在「本地曲库」里点「体检文件名」，卡片却扫了"补全整理"自己那份
 * 曲库目录 —— 因为入口只写了偏好、卡片优先读 props.dir。入口点明摆着是「就查我现在看的
 * 这个文件夹」，所以改成一次性 payload 传进来；`watch(currentView)` 在离开本页时清掉，
 * 免得用户下次从导航栏自己走进来还停在上次的目录上（这就是当初塞 localStorage 的毛病）。
 */
const healthDir = ref('')
let stopHealthNav = null
// 2026-10-19 渐进式披露 → 2026-10-21 分段导航：「偶尔才改一次的设置」仍在右侧抽屉；
// 主功能入口升为顶部分段，⋯ 里只留真正次要的动作（增量扫描）。
const optionsOpen = ref(false)
const auditing = ref(false)

/**
 * 当前分段：壳层（App.vue）的顶部分段导航把选择写进 navBus，这里只读。
 * 五个段共用本组件的同一批 ref —— 切段只是 v-show 显隐，
 * 跑完的体检 / 查重 / 补全结果不会因为切走而被卸载丢掉。
 */
const sub = computed(() => segmentOf('tidy'))

/**
 * 补全段的 ⋯：原六项里，扫描缺失 = 本段主按钮，体检 / 查重 = 各自的分段，
 * 撤销 = 「记录」段，补全选项 = 卡头的「选项」按钮 —— 这里只剩「增量扫描」。
 */
const completeMenu = computed(() => [
  { key: 'index', label: indexing.value ? '扫描中…' : '增量扫描', icon: FolderSearch, disabled: indexing.value },
])

function onCompleteMenu(key) {
  if (key === 'index') runIndexScan()
}

/** 替换音质（占位段）：后端 /api/quality/* 开发中，这里不发任何接口调用 */
// ── 分段五：替换音质（需求 m02757/m02848）──
//
// 接后端 /api/quality/scan|replace（pkg/intercept/quality.go，契约见 api/client.js 的 QualityAPI 注释）。
// 几个刻意的口径，改之前先读：
//  · scan 是**异步任务**：start 只是启动，进度靠轮询 status（2s 一次，对齐补全轮询）。
//    视图 v-show 常驻，切走再切回状态不丢；
//  · 「停止」只停本页轮询 —— 后端并不知道，状态文案必须诚实「已中断」，不许假装任务停了；
//  · 只有 upgradeable 的行可勾选；替换一次最多 50 条，超出前端分批顺序发；
//  · 成功只把对应行标成已替换（**不整表刷新**，用户的滚动位置/勾选视角保持连续），
//    失败把后端 error 写回该行。
const Q_CHANNEL_LABELS = { lx: '洛雪', dl: 'musicdl', native: '原生' }
const qMode = ref('lossy')
const qLimit = ref(clampQLimit(Number(loadPref('quality_limit', 200))))
const qScan = ref(null)      // 最近一次 scan 返回的 data（state/total/scanned/entries…）
const qStopped = ref(false)  // true = 本页已停止查看进度（不等于后端停了）
const qErrorText = ref('')   // 请求层失败（接口没上线/断网）的横幅，别白屏也别抛错
const qSelected = ref(new Set())
const qBusy = ref(false)     // 替换批次进行中
let qTimer = null

function clampQLimit(n) {
  const v = Math.floor(Number(n))
  if (!Number.isFinite(v) || v < 1) return 200
  return Math.min(500, v)
}

const qState = computed(() => qScan.value?.state || 'idle')
const qRunning = computed(() => qState.value === 'running')
const qPercent = computed(() => {
  const t = Number(qScan.value?.total) || 0
  if (!t) return 0
  return Math.min(100, Math.round(((Number(qScan.value?.scanned) || 0) * 100) / t))
})
const qSelectedCount = computed(() => qSelected.value.size)
const qRows = computed(() => {
  const es = qScan.value?.entries || []
  // 扫完且一条都不可替换（也还没替换过任何东西）→ 让位给空态文案，不刷一屏灰勾
  if (qScan.value?.state === 'done' && !es.some((e) => e.upgradeable || e.replaced)) return []
  return es
})
const qStatusText = computed(() => {
  const parts = []
  if (qStopped.value && qState.value !== 'running') {
    parts.push('已中断 —— 只是本页不再读取进度（后端扫描可能仍在继续），点「开始扫描」可重新接上')
  } else if (qState.value === 'running') {
    parts.push('扫描中…')
  } else if (qState.value === 'done') {
    parts.push(`扫描完成 —— 共扫 ${qScan.value?.scanned ?? 0} 条，可替换 ${qScan.value?.upgradeable ?? 0} 条`)
  } else if (qState.value === 'failed') {
    parts.push('扫描失败')
  }
  if (qScan.value?.truncated) parts.push('已达数量上限，曲库后面部分没扫')
  return parts.join(' · ')
})

// 渠道三格：洛雪 / musicdl / 原生，拿到显示档位、拿不到显示灰的「拿不到」
function qChannelCells(e) {
  return DEFAULT_AUTO_DOWNLOAD_CHANNELS.map((ch) => {
    const hit = (e.channels || []).find((c) => c.channel === ch)
    const label = Q_CHANNEL_LABELS[ch] || ch
    return {
      ch,
      label,
      tier: hit && hit.tier ? hit.tier : '',
      hint: hit && hit.tier
        ? `${label}：${hit.tier}${hit.platform ? `（来自${hit.platform}）` : ''}`
        : `${label}：这条在该渠道拿不到`,
    }
  })
}

// 最近的替换记录（最新在前）。后端已按时间倒序并截断到 100 条，
// 这里只取前 8 条摆出来 —— 它是「刚发生了什么」的凭据，不是存档页。
const qRecords = computed(() => {
  const list = Array.isArray(qScan.value?.records) ? qScan.value.records : []
  return list.slice(0, 8).map((r) => ({
    ...r,
    when: r?.at ? formatRecordTime(r.at) : '',
    label: (r?.title || baseName(r?.path || '')) + (r?.artist ? ` · ${r.artist}` : ''),
    tierText: r?.ok
      ? `${r.from || '?'} → ${r.to || '?'}`
      : (r?.note || '没换成'),
  }))
})

/** 记录里有几条是后台自动补强做的（`by === 'upgrade'`，见 pkg/intercept/quality.go 的 QualityRecord.By）。 */
const qAutoCount = computed(() => {
  const list = Array.isArray(qScan.value?.records) ? qScan.value.records : []
  return list.filter((r) => r && r.by === 'upgrade').length
})

function formatRecordTime(at) {
  const d = new Date(Number(at) * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function normalizeScan(j) {
  const d = j && typeof j === 'object' ? j : {}
  return {
    state: d.state || 'running',
    mode: d.mode || qMode.value,
    total: Number(d.total) || 0,
    scanned: Number(d.scanned) || 0,
    truncated: !!d.truncated,
    upgradeable: Number(d.upgradeable) || 0,
    error: d.error || '',
    entries: Array.isArray(d.entries) ? d.entries : [],
    // 替换记录（后端落盘的最近 100 条，重启还在）——
    // 替换是原地覆盖，不看这份记录用户根本无从知道「昨天到底换过什么」。
    records: Array.isArray(d.records) ? d.records : [],
  }
}

async function startQualityScan() {
  if (qRunning.value || qBusy.value) return
  const limit = clampQLimit(qLimit.value)
  qLimit.value = limit
  savePref('quality_limit', limit)
  qErrorText.value = ''
  try {
    const res = await QualityAPI.scan('start', qMode.value, limit)
    if (res.code === 200) {
      qStopped.value = false
      qScan.value = normalizeScan(res.data)
      if (qRunning.value) startQualityPolling()
      else stopQualityPolling()
      pruneQSelection()
    } else {
      qErrorText.value = apiError(res, '发起音质扫描')
    }
  } catch (e) {
    // 后端接口还没上线 / 网络失败：横幅说清即可，不白屏、不抛未捕获错误
    qErrorText.value = failText('发起音质扫描', e)
  }
}

// 进页先读一次状态：替换记录是**历史**，不该只有「刚扫过」才看得见
// （用户 m04229 的原话就是「替换音质，怎么看不到记录」）。
async function loadQualityStatus() {
  try {
    const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
    if (res.code === 200 && res.data) qScan.value = normalizeScan(res.data)
  } catch (_) {
    // 读不到就当没有历史：不该因为一条记录挡住整页
  }
}

function startQualityPolling() {
  stopQualityPolling()
  qTimer = setInterval(async () => {
    try {
      const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
      if (res.code !== 200 || !res.data) return // 单次查询失败不打断轮询（对齐补全轮询口径）
      qScan.value = normalizeScan(res.data)
      if (qState.value === 'done' || qState.value === 'failed') {
        stopQualityPolling()
        pruneQSelection()
      }
    } catch (_) {
      // 轮询失败静默：后端重启/网络抖一下不该刷屏
    }
  }, 2000)
}

function stopQualityPolling() {
  if (qTimer) {
    clearInterval(qTimer)
    qTimer = null
  }
}

function stopQualityScan() {
  stopQualityPolling()
  qStopped.value = true
}

function setQMode(v) {
  if (v === qMode.value) return
  if (qRunning.value) return flash('扫描进行中 —— 先按停止再换范围', true)
  qMode.value = v
}

function toggleQSelect(e) {
  if (!e.upgradeable || e.replaced) return
  const sel = new Set(qSelected.value)
  if (sel.has(e.path)) sel.delete(e.path)
  else sel.add(e.path)
  qSelected.value = sel
}

function qClearSelection() {
  qSelected.value = new Set()
}

// 新一轮扫描/替换之后，把已不存在或已不可替换的勾选剔掉
function pruneQSelection() {
  const valid = new Set((qScan.value?.entries || []).filter((e) => e.upgradeable && !e.replaced).map((e) => e.path))
  let changed = false
  const sel = new Set(qSelected.value)
  for (const path of sel) {
    if (!valid.has(path)) {
      sel.delete(path)
      changed = true
    }
  }
  if (changed) qSelected.value = sel
}

function onQAction(key) {
  if (key === 'replace') replaceWithBest()
}

async function replaceWithBest() {
  if (qBusy.value || !qSelected.value.size) return
  const paths = [...qSelected.value]
  qBusy.value = true
  qErrorText.value = ''
  let doneCount = 0
  let failCount = 0
  try {
    // 契约：一次最多 50 条 —— 超出由前端分批，顺序发（一批一落，失败行当场标注）
    for (let i = 0; i < paths.length; i += 50) {
      const batch = paths.slice(i, i + 50)
      const res = await QualityAPI.replace(batch)
      if (res.code !== 200) {
        failCount += batch.length
        qErrorText.value = apiError(res, '替换音质')
        continue
      }
      const d = res.data || {}
      const doneMap = new Map()
      const failMap = new Map()
      for (const it of d.done || []) {
        // ok:false 配的是 `note`（例如「拿到的是同档位文件，没有覆盖」）——
        // 与 failed[] 那种真失败不同，但要照实说，不能显示成成功。
        if (it && it.ok === false) failMap.set(it.path, (it && (it.note || it.error)) || '这条没有替换成功')
        else if (it) doneMap.set(it.path, it)
      }
      for (const it of d.failed || []) failMap.set(it.path, it.error || '替换失败')
      doneCount += doneMap.size
      failCount += failMap.size
      applyQReplace(doneMap, failMap)
    }
    if (doneCount) flash(`已用最高音质替换 ${doneCount} 首`)
    if (failCount) flash(`${failCount} 条替换失败，原因写在对应行`, true)
  } catch (e) {
    // 某批直接抛错：后面的批次不再发（不假装成功），已勾选未处理的留在条上可重试
    flash(failText('替换音质', e), true)
  } finally {
    qBusy.value = false
  }
}

// 只改对应行的数据（done → replaced + 新档位；failed → rowError），
// 行按 path 做 key，Vue 就地更新，不整表重绘 —— 用户视角连续。
function applyQReplace(doneMap, failMap) {
  const j = qScan.value
  if (!j) return
  j.entries = (j.entries || []).map((x) => {
    const d = doneMap.get(x.path)
    if (d) return { ...x, replaced: true, upgradeable: false, newTier: d.tier || '', newChannel: d.channel || '', rowError: '' }
    const err = failMap.get(x.path)
    if (err !== undefined) return { ...x, rowError: err }
    return x
  })
  const sel = new Set(qSelected.value)
  for (const path of [...sel]) {
    if (doneMap.has(path) || failMap.has(path)) sel.delete(path)
  }
  qSelected.value = sel
}
const indexing = ref(false)
const deduping = ref(false)
const audit = ref(null)
// 体检结果的落盘时间（0 = 当前这次是刚跑的，非 0 = 从缓存恢复的上次结果）
const auditAt = ref(0)
const indexResult = ref(null)
const dupResult = ref(null)
const dupResolveResult = ref(null)
// 上次整理快照（来自持久化索引 library_index.json）
const indexStats = ref(null)
const statsLoading = ref(false)
const reloading = ref(false)

// ── 曲库体检结果的持久化 ──
// 用 `prefs` 的 saveCache/loadCache：本地 localStorage + 后端双写，跟着设备走。
// **按目录分开存** —— 换了曲库目录不该看到别的目录的体检结果。
function auditCacheKey(dir) {
  return `last_audit:${dir || ''}`
}

function restoreAudit() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) {
    audit.value = null
    auditAt.value = 0
    return
  }
  // ttl 传 0 = 永不过期：用户要的就是「一直看得到上次结果」（2026-09-22 反馈）
  const cached = loadCache(auditCacheKey(dir), 0)
  const ok = cached && typeof cached === 'object'
  audit.value = ok ? cached : null
  auditAt.value = ok ? Number(cached.at) || 0 : 0
}

// 目录一改就换成本目录的上次体检结果（各目录分开存，见 auditCacheKey）
watch(tidyDir, restoreAudit)

/** 体检结果的时间戳文案（只用于「上次体检结果」这行提示） */
const auditAtText = computed(() => {
  if (!auditAt.value) return ''
  try {
    return new Date(auditAt.value).toLocaleString('zh-CN', { hour12: false })
  } catch (_) {
    return ''
  }
})

async function runAudit() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return flash('请填写曲库目录', true)
  savePref('nas_dir', dir)
  auditing.value = true
  try {
    const res = await TidyAPI.audit(dir)
    if (res.code === 200) {
      audit.value = res.data
      // 刚跑出来的结果不显示「上次体检」提示（auditAt 为 0 = 本次）
      auditAt.value = 0
      saveCache(auditCacheKey(dir), { ...res.data, at: Date.now() })
    } else {
      flash(apiError(res, '体检失败'), true)
    }
  } catch (e) {
    flash(failText('曲库体检', e), true)
  } finally {
    auditing.value = false
  }
}

// 读取上次整理快照：挂载时自动加载持久化索引的统计，首屏即可看到上次结果
async function loadIndexStats() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return
  statsLoading.value = true
  try {
    const res = await TidyAPI.indexStats(dir)
    if (res.code === 200 && res.data && res.data.total) indexStats.value = res.data
  } catch (_) {
    // 后端不可用/无索引：保持空，不打扰
  } finally {
    statsLoading.value = false
  }
}

async function runIndexScan() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return flash('请填写曲库目录', true)
  savePref('nas_dir', dir)
  indexing.value = true
  try {
    const res = await TidyAPI.index(dir)
    if (res.code === 200) {
      indexResult.value = res.data
      if (res.warning) flash(res.warning, true)
      // 扫描刚把新字段读进索引 —— 顺手重算缺口，否则页面还显示旧的待补数量
      loadIndexStats()
      scanGaps().catch(() => {})
    } else {
      flash(apiError(res, '扫描失败'), true)
    }
  } catch (e) {
    flash(failText('增量扫描', e), true)
  } finally {
    indexing.value = false
  }
}

async function runDedupe() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return flash('请填写曲库目录', true)
  savePref('nas_dir', dir)
  deduping.value = true
  dupResolveResult.value = null
  try {
    const res = await TidyAPI.duplicates(dir)
    if (res.code === 200) dupResult.value = res.data
    else flash(apiError(res, '查重失败'), true)
  } catch (e) {
    flash(failText('查找重复', e), true)
  } finally {
    deduping.value = false
  }
}

async function resolveDup(dryRun) {
  const dir = tidyDir.value || nasDir.value
  if (!dryRun && !confirm('将把重复文件移入回收站（.tidy_trash），可从回收站恢复。继续？')) return
  try {
    const res = await TidyAPI.resolveDuplicates({ dir, mode: 'trash', dry_run: dryRun })
    if (res.code === 200) {
      dupResolveResult.value = res.data
      // 逐文件失败原因太长，界面只给数量，明细进日志
      const errs = res.data?.errors || []
      if (errs.length) logError('清理重复文件', `${errs.length} 个文件处理失败`, errs.slice(0, 20).join('；'))
      if (!dryRun) { await runDedupe(); await runAudit() }
    } else {
      flash(apiError(res, '执行失败'), true)
    }
  } catch (e) {
    flash(failText('清理重复文件', e), true)
  }
}

const lastScanText = computed(() => _lastScanText(indexStats.value))

function fmtSize(bytes) {
  const b = Number(bytes || 0)
  if (b > 1024 * 1024 * 1024) return (b / 1024 / 1024 / 1024).toFixed(2) + ' GB'
  if (b > 1024 * 1024) return (b / 1024 / 1024).toFixed(1) + ' MB'
  if (b > 1024) return (b / 1024).toFixed(1) + ' KB'
  return b + ' B'
}

function baseName(p) {
  return String(p || '').split('/').pop()
}

// ── 补全歌词/封面（后端是异步任务，前端靠轮询看进度）──
// 为什么是轮询而不是等一次请求：一首歌要走「搜索 + 取歌词」两次外网请求，
// 几十首要几分钟 —— 同步请求必然超时，而且用户看不到任何进展，会以为卡死了。
const completeJob = ref(null)
const completing = ref(false)
const previewing = ref(false)
const undoBusy = ref(false)
// 停止请求在途 —— 防连点（后端 cancelRun 是幂等的，但连点会让界面文案乱跳）
const stopBusy = ref(false)
// 预览出来的计划（还没写文件）。用户确认后才执行。
const completePlan = ref(null)
let completeTimer = null

// ── 按字段补全 ──
// * **能补的都按字段单独勾** —— 用户可能只想要年份，不该顺带改写他的歌词；
// * **值只来自搜索命中**（年份/曲序/光盘/风格都取自「匹配上的那条」），
//   风格额外只有 QQ 专辑详情给真实值，AI 推断要单独勾（默认关）。
const gaps = ref(null)
const gapsLoading = ref(false)
const selectedFields = ref(parseFields(loadPref('complete_fields', DEFAULT_FIELDS.join(','))))
const completeLimit = ref(Number(loadPref('complete_limit', '30')) || 30)
const overwriteExisting = ref(loadBool('complete_overwrite', false))
const allowAIGenre = ref(loadBool('complete_ai_genre', false))

const aiGenreHint = '默认不让 AI 猜风格：勾上后，非 QQ 命中或专辑详情没给 genre 时，' +
  '才会让大模型按歌名+歌手推断一个'

// ── 主力平台（写回 /api/config，后端拿它决定搜索顺序与「回哪家借元数据」）──
const PLATFORM_CHOICES = [
  { id: 'wy', name: '网易云' },
  { id: 'tx', name: 'QQ 音乐' },
  { id: 'kg', name: '酷狗' },
  { id: 'kw', name: '酷我' },
]
const preferredPlatforms = ref(['wy', 'tx'])
const preferredRaw = ref(null)   // 保存时要原样带回的完整配置，避免局部写覆盖别的设置

const platformChips = computed(() => PLATFORM_CHOICES.map((p) => ({
  ...p, preferred: preferredPlatforms.value.includes(p.id),
})))

async function loadPreferred() {
  try {
    const res = await ConfigAPI.get()
    const cfg = res?.data || res?.config
    if (!cfg) return
    preferredRaw.value = cfg
    preferredPlatforms.value = Array.isArray(cfg.preferred_platforms) && cfg.preferred_platforms.length
      ? cfg.preferred_platforms : ['wy', 'tx']
  } catch (_) {
    // 读不到就用默认，别挡住这一页的主流程
  }
}

async function togglePreferred(id) {
  const cur = preferredPlatforms.value
  // 全清光是不行的：那样后端会退回默认（网易+QQ），界面却显示"没主力"，两边不一致。
  if (cur.includes(id)) {
    if (cur.length === 1) return flash('至少得留一个主力平台', true)
    preferredPlatforms.value = cur.filter((p) => p !== id)
  } else {
    preferredPlatforms.value = [...cur, id]
  }
  await savePreferred()
}

async function savePreferred() {
  const base = preferredRaw.value || {}
  const payload = { ...base, preferred_platforms: preferredPlatforms.value }
  try {
    const res = await ConfigAPI.save(payload)
    if (res?.code && res.code !== 200) return flash(apiError(res, '保存失败'), true)
    preferredRaw.value = res?.data || payload
    flash(`主力平台已设为：${preferredPlatforms.value.map((p) => platformName(p)).join('、')}`)
  } catch (e) {
    flash(failText('保存主力平台', e), true)
  }
}

const fieldOptions = computed(() => FIELD_DEFS.map((f) => ({
  ...f,
  missing: gaps.value ? (gaps.value[f.key] ?? 0) : '—',
})))

/**
 * 三步流程条（2026-10-12 用户反馈「点完扫描缺失，下一步做什么？」）。
 * 只由**已有状态**推出，不引入新的步骤状态机。
 * 步骤/文案全部来自 services/libraryView.js，由 libraryView.test.mjs 钉住。
 */
const completeSteps = COMPLETE_STEPS
const completeStep = computed(() => _completeStep(gaps.value, completePlan.value))
const completeHint = computed(() => _completeHint(gaps.value, completePlan.value))

/**
 * 「写不了标签的格式」按扩展名的明细，如 `wav×12、opus×3`。
 * 后端把这类文件从缺口里摘了出去（`gaps.unsupported`），但必须让用户看见。
 */
const unsupportedExtsText = computed(() => _unsupportedExtsText(gaps.value?.unsupported_exts))

/**
 * 「上次统计」的相对时间。
 * ⚠️ `gaps.at` 是 `Date.now()`（**毫秒**），和 `indexStats.scanned_at`（秒）不是一个单位。
 */
const gapsAtText = computed(() => _gapsAtText(gaps.value))

function toggleField(key) {
  selectedFields.value = toggleFieldKey(selectedFields.value, key)
  savePref('complete_fields', selectedFields.value.join(','))
}

const summaryCells = computed(() => _summaryCells(completeJob.value?.summary, aiMatchedCount.value))

function gapsCacheKey(dir) {
  return `last_gaps:${dir}`
}

/** 刷新后仍显示上次统计（同「体检」的处理，2026-09-22 反馈「数据每次都会消失」） */
function restoreGaps() {
  const dir = (tidyDir.value || '').trim()
  if (!dir) {
    gaps.value = null
    return
  }
  const cached = loadCache(gapsCacheKey(dir), 0)
  gaps.value = cached && typeof cached === 'object' ? cached : null
}

async function scanGaps() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return flash('请填写曲库目录', true)
  gapsLoading.value = true
  try {
    const res = await TidyAPI.gaps(dir)
    const bad = apiError(res, '统计失败')
    if (bad) return flash(bad, true)
    const d = res.data || {}
    gaps.value = { ...(d.gaps || {}), unread: d.unread || 0, dir: d.dir, at: Date.now() }
    saveCache(gapsCacheKey(dir), gaps.value)
    flash(`已统计 ${gaps.value.total || 0} 首：${gaps.value.any_field || 0} 首还有待补字段`)
  } catch (e) {
    flash(failText('统计字段缺口', e), true)
  } finally {
    gapsLoading.value = false
  }
}

// 换目录就换成这个目录的上次统计与上次卡片，别把上个库的数字挂在这里
watch(tidyDir, () => {
  restoreGaps()
  restoreCards()
})

// ── 补全结果卡片：逐首回读文件里真实的标签 ──
// 为什么不直接用补全结果里的值：结果说的是「我们打算写什么」，
// 卡片要说的是「文件里现在是什么」。界面上报"补好了"而飞牛里没有，最伤信任。
const completedCards = ref([])
const skippedCards = ref([])
const cardsLoading = ref(false)
const completeBar = ref([])
const cardsAt = ref(0)

function cardsCacheKey(dir) {
  return `last_complete_cards:${dir}`
}

function cardCover(path) {
  // 带时间戳：同一批刚重写过封面，不加参数浏览器会继续用旧图
  return NasAPI.coverUrl(path) + '&v=' + cardsAt.value
}

/**
 * 卡片补上 coverUrl。
 * ⚠️ 旧 LibraryManager 里 cardCover 定义了却从未被调用，模板 `:src="c.coverUrl"`
 * 一直是 undefined —— 封面其实从来没显示出来过。这次搬组件时接上。
 */
function withCover(cards) {
  return (cards || []).map((c) => (c && c.path ? { ...c, coverUrl: cardCover(c.path) } : c))
}

function saveCards(dir) {
  if (!dir) return
  saveCache(cardsCacheKey(dir), {
    at: cardsAt.value, bar: completeBar.value,
    cards: completedCards.value, skipped: skippedCards.value,
  })
}

/** 刷新页面后卡片还在（同「体检」的处理，见 restoreAudit） */
function restoreCards() {
  const dir = (tidyDir.value || '').trim()
  if (!dir) return
  const cached = loadCache(cardsCacheKey(dir), 0)
  if (!cached || typeof cached !== 'object') return
  cardsAt.value = Number(cached.at) || 0
  completeBar.value = cached.bar || []
  completedCards.value = withCover(cached.cards || [])
  skippedCards.value = withCover(cached.skipped || [])
}

async function loadCompletedCards(summary) {
  const dir = tidyDir.value || nasDir.value
  const results = (summary && summary.results) || []
  completeBar.value = summaryBar(selectedFields.value, summary)
  cardsAt.value = Date.now()

  const written = results.filter((r) => r.ok)
  skippedCards.value = withCover(results.filter((r) => !r.ok).map((r) => cardModel(null, r)))
  if (!written.length) {
    completedCards.value = []
    saveCards(dir)
    return
  }

  cardsLoading.value = true
  try {
    const settled = await mapWithLimit(written, 4, async (r) => {
      const res = await TagsAPI.read(r.path)
      return cardModel(res && res.code === 200 ? res.data : {}, r)
    })
    // 单首回读失败也要出现在卡片里（退回用补全结果的值 + 不带封面），
    // "卡片凭空少了几首"比"某首显示不全"更让人怀疑整个功能
    completedCards.value = withCover(
      settled.map((s, i) => (s.ok ? s.value : cardModel({}, written[i]))),
    )
  } finally {
    cardsLoading.value = false
    saveCards(dir)
  }
}

watch(tidyDir, restoreCards)

const completePercent = computed(() => _completePercent(completeJob.value))

/** 补全卡片的标题四态（跑 / 正在停 / 上次被停 / 上次结果）——「正在停止」不能显示成「正在补全」 */
const completeTitle = computed(() => _completeTitleText(completeJob.value) || '补全整理')

/**
 * 其中有多少首是「规则配不上、由 AI 从候选里挑的」。
 * 透出来是为了**可分辨** —— 用户有权知道哪些结果是模型判断的。
 */
const aiMatchedCount = computed(() => _aiMatchedCount(completeJob.value?.summary))

async function runComplete() {
  const dir = tidyDir.value || nasDir.value
  if (!dir) return flash('请填写曲库目录', true)
  if (!selectedFields.value.length) return flash('先勾选要补的字段', true)
  savePref('nas_dir', dir)
  savePref('complete_limit', String(completeLimit.value || 30))

  completeJob.value = null
  completePlan.value = null
  previewing.value = true
  try {
    // 先预览：只解析匹配，**不写任何文件**
    const res = await TidyAPI.previewComplete({
      dir,
      limit: completeLimit.value || 30,
      fields: selectedFields.value,
      overwrite: !!overwriteExisting.value,
      use_ai_genre: !!allowAIGenre.value,
    })
    if (res?.hint) {
      flash(res.message || '没有需要补全的曲目', true)
      return
    }
    const bad = apiError(res, '预览失败')
    if (bad) {
      flash(bad, true)
      return
    }
    completePlan.value = res.data
    flash(res.message || '预览完成，确认后才会写入')
  } catch (e) {
    flash(failText('预览补全', e), true)
  } finally {
    previewing.value = false
  }
}

/**
 * 执行已确认的计划。
 * ⚠️ 传回 `plan_id` 而不是重新提交目录 —— 执行的是**用户刚看到的那份计划**，
 * 否则搜索结果变了就会「批准了 A、结果写了 B」。
 */
async function executeComplete() {
  const pid = completePlan.value?.plan_id
  if (!pid) return flash('预览已失效，请重新预览', true)

  completePlan.value = null
  try {
    const res = await TidyAPI.executeComplete(pid)
    completeJob.value = res?.data
    flash(res?.message || '已开始写入')
    startCompletePolling()
  } catch (e) {
    flash(failText('写入补全结果', e), true)
  }
}

function startCompletePolling() {
  stopCompletePolling()
  completing.value = true
  completeTimer = setInterval(async () => {
    try {
      const res = await TidyAPI.completeStatus()
      const j = res?.data
      if (!j) return
      completeJob.value = j
      if (!j.running) {
        stopCompletePolling()
        const s = j.summary
        if (s) {
          // 被停止 ≠ 跑完（话术在服务层，有单测）
          flash(completeDoneText(j))
          loadIndexStats()
          // 缺口数字要跟着重算，否则页面还挂着「缺 12 首」而实际已经补好了
          scanGaps().catch(() => {})
          // 卡片墙：逐首回读文件真实标签
          loadCompletedCards(s).catch(() => {})
        }
      }
    } catch (_) {
      // 单次轮询失败不打断；下一轮再试
    }
  }, 2000)
}

/**
 * 停止正在跑的补全任务。
 * 不弹确认框：停止不是破坏性操作 —— 已写入的保留、还能用「撤销」回退，
 * 弹窗反而会让人以为「停了会丢东西」而不敢停。
 */
async function stopComplete() {
  if (stopBusy.value) return
  stopBusy.value = true
  try {
    const res = await TidyAPI.cancelComplete()
    flash(res?.message || '已请求停止')
    // 不一定停下来了：可能刚好跑完了。拉一次真实状态。
    await loadCompleteStatus()
  } catch (e) {
    flash(failText('停止补全', e), true)
  } finally {
    stopBusy.value = false
  }
}

function stopCompletePolling() {
  if (completeTimer) {
    clearInterval(completeTimer)
    completeTimer = null
  }
  completing.value = false
}

/** 挂载时接上「上次/正在跑」的任务 —— 刷新页面不该让进度凭空消失 */
async function loadCompleteStatus() {
  try {
    const res = await TidyAPI.completeStatus()
    const j = res?.data
    if (!j) return
    if (j.running) {
      completeJob.value = j
      startCompletePolling()
    } else if (j.summary || j.undo_available) {
      // 有可撤销的备份时也要显示面板 —— 服务重启后 summary 没了，但备份还在
      completeJob.value = j
    }
  } catch (_) {
    // 后端不可用就静默
  }
}

/**
 * 撤销上一次补全：把改过的文件从备份还原。
 * 撤销只做一次（没有「重做」）—— 确认弹窗里要说清楚。
 */
async function undoComplete() {
  if (!confirm('把上一次补全改过的文件还原成补全前的样子？\n\n还原后需要重新跑一次补全才能再补上。')) return
  undoBusy.value = true
  try {
    const res = await TidyAPI.undoComplete()
    flash(res?.message || '已撤销')
    await loadCompleteStatus() // 撤销后 undo_available 会变 false
    await loadIndexStats()
    scanGaps().catch(() => {})
    // 卡片墙必须一起清 —— 留着就是在展示一批已经不存在的标签值
    completedCards.value = []
    skippedCards.value = []
    completeBar.value = []
    saveCards(tidyDir.value || nasDir.value)
  } catch (e) {
    flash(failText('撤销补全', e), true)
  } finally {
    undoBusy.value = false
  }
}

/** 顶栏「刷新」：重新拉快照与任务状态（缓存恢复的部分也重读一遍） */
async function onRefresh() {
  if (reloading.value) return
  reloading.value = true
  try {
    restoreAudit()
    restoreGaps()
    restoreCards()
    await Promise.all([loadIndexStats(), loadCompleteStatus()])
  } finally {
    reloading.value = false
  }
}

onMounted(() => {
  tidyDir.value = loadPref('nas_dir', '')
  // 「本地曲库 → 体检文件名」带过来的目录（一次性）：消费后仍留到离开本页为止，
  // 这样用户切到别的分段看一眼再回来，卡片不会突然换成另一个目录
  stopHealthNav = onNavigate((view, payload) => {
    if (view === 'tidy' && payload && typeof payload === 'object' && payload.healthDir) {
      healthDir.value = String(payload.healthDir)
    }
  })
  // 首屏自动读取上次整理快照（持久化索引），无需等用户点扫描
  loadIndexStats()
  // 上次体检 / 缺口统计 / 结果卡片都从缓存恢复（2026-09-22「数据每次都会消失」）
  restoreAudit()
  restoreGaps()
  restoreCards()
  // 主力平台从应用配置读
  loadPreferred()
  // 补全任务是后端在跑的，刷新页面不该让进度凭空消失
  loadCompleteStatus()
  // 替换记录同理：上次替换过什么，进页就该看得到
  loadQualityStatus()
})

onUnmounted(() => {
  stopCompletePolling()
  stopQualityPolling()
  if (stopHealthNav) stopHealthNav()
})

// 离开「补全整理」就丢掉入口带来的一次性目录，避免导航栏自己走进来时停在过期目录
watch(currentView, (v) => {
  if (v !== 'tidy') healthDir.value = ''
})
</script>
