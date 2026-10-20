<template>
  <!--
    补全整理页（2026-10-19 渐进式披露重构）

    用户口径：「一个页面里一样有好多功能，按钮，选项，卡片，都是一窝蜂挤在一起……
    一些不必要的按钮和交互可以通过现代化方式实现交互显示，而不是挤在一个页面。」

    重构前的版面（一屏里同时存在的控件）：目录输入框 + 4 张动作卡（扫描缺失 / 增量扫描 /
    体检 / 查找重复）+ 三步流程条 + 6 张字段勾选卡 + 一行选项（最多N首 / 覆盖 / AI / 4 个平台 chip）
    + 预览表 + 结果卡 + 第二张「体检·索引·查重」空卡。

    重构后的分层：
      · 版面上常驻：曲库目录（一行可点）· 缺口数字（一行）· 已选字段（一行 chip）· 结果区；
      · 主按钮：只放**当前这一步**该点的那个（扫描缺失 / 预览要写的内容）；
      · 其余全部收进右上角 ⋯：增量扫描 / 体检 / 查重 / 撤销；
      · 目录、最多 N 首、覆盖、AI、主力平台 → 右侧抽屉「补全选项」。
    结果区（预览表 / 进度 / 卡片墙 / 失败清单）保持常驻 —— 它们只在有数据时出现。
  -->
  <PageShell>
    <template #actions>
      <!-- 当前步骤的主按钮：第 3 步不给按钮（确认写入就在预览表里，紧挨着要确认的清单） -->
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
        :title="completeHint + (selectedFields.length ? '' : '（先在「补全选项」里勾要补的字段）')"
        class="ys-press rounded-full bg-emerald-500 px-4 py-1.5 text-[12px] font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600 disabled:opacity-50"
        @click="runComplete"
      >{{ previewing ? '匹配中…' : '预览要写的内容' }}</button>

      <OverflowMenu :items="tidyMenu" @select="onTidyMenu" />
    </template>

    <div class="space-y-3.5">
      <SectionCard
        :icon="Sparkles"
        tone="brand"
        title="补全整理"
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

          <div
            v-if="completeJob.undo_available && !completeJob.running"
            class="flex flex-wrap items-center gap-2 border-t border-[var(--ys-border)] pt-2"
          >
            <button
              :disabled="undoBusy"
              class="rounded-[8px] bg-black/[0.04] px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.07] disabled:opacity-50"
              @click="undoComplete"
            >{{ undoBusy ? '还原中…' : '撤销上一次补全' }}</button>
            <span class="text-[11px] text-[var(--ys-text-faint)]">还原成补全前的样子</span>
          </div>
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

      <!-- ══ 体检 / 索引 / 查重结果 ══
           2026-10-19：**空的时候整张卡不渲染**。原来这块是一张写着「还没有体检结果」的卡，
           占掉首屏一整块，却什么也没说。现在从 ⋯ 菜单跑完才出现。 -->
      <SectionCard
        v-if="audit || indexResult || dupResult || auditing"
        :icon="FolderOpen"
        title="体检 · 索引 · 查重"
        sub="从右上角 ⋯ 里跑，结果落在这里"
      >
        <template v-if="audit && auditAtText" #actions>
          <span class="text-[10.5px] text-[var(--ys-text-faint)]">体检于 {{ auditAtText }}</span>
        </template>

        <p v-if="statsLoading" class="text-[11px] text-[var(--ys-text-faint)]">正在读取上次整理快照…</p>
        <p
          v-else-if="indexStats"
          class="text-[11px] leading-relaxed text-[var(--ys-text-faint)]"
          title="下面这组数字是直接扫文件得到的（跟「扫描缺失」的索引口径各算各的），两边对不上就说明索引过期了"
        >{{ lastScanText || '索引还没整理过' }} —— 点「增量扫描」会更新</p>

        <div v-if="!audit" class="mt-3 rounded-[12px] border border-dashed border-[var(--ys-border)] px-4 py-6 text-center text-[11px] text-[var(--ys-text-faint)]">
          {{ auditing ? '正在体检…' : '还没有体检结果' }}
        </div>
        <div v-else class="mt-3 grid grid-cols-2 gap-2 md:grid-cols-4">
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

        <div v-if="indexResult" class="mt-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3.5 py-2.5 text-[11px] text-[var(--ys-text-muted)]">
          增量扫描：新增 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.added }}</b> ·
          变化 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.changed }}</b> ·
          未变 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.unchanged }}</b> ·
          索引总数 <b class="font-mono text-[var(--ys-text)]">{{ indexResult.index_total }}</b>
          <div v-if="!indexResult.complete" class="mt-1 text-amber-700">
            本次枚举不完整，已跳过删除判定（保护索引不被误清）
          </div>
        </div>

        <div v-if="dupResult" class="mt-2.5 space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
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
  Wand2, Sparkles, AlertTriangle, HardDrive, Trash2, Check, FolderOpen,
  SlidersHorizontal, RotateCcw, ScanSearch, FolderSearch, Copy,
} from 'lucide-vue-next'
import PageShell from './PageShell.vue'
import SectionCard from './SectionCard.vue'
import InlineNotice from './InlineNotice.vue'
import OverflowMenu from './OverflowMenu.vue'
import SideSheet from './SideSheet.vue'
import StatLine from './StatLine.vue'
import { TidyAPI, TagsAPI, NasAPI, ConfigAPI } from '../api/client'
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
import { navigateTo } from '../services/navBus'

/** 页面提示统一走全局 feedback（右下角浮层），不再占页面底部一条 InlineNotice */
function flash(msg, isError = false) {
  if (isError) notifyFail(msg)
  else notifyOk(msg)
}

// ── 目录与状态 ──
const nasDir = computed(() => loadPref('nas_dir', ''))
const tidyDir = ref('')
// 2026-10-19 渐进式披露：所有「偶尔才改一次的设置」收进右侧抽屉，
// 所有「偶尔才跑一次的动作」收进右上角 ⋯ —— 版面上只留当前这一步。
const optionsOpen = ref(false)
const auditing = ref(false)

/**
 * 右上角 ⋯ 菜单。顺序 = 从「最常跑」到「最不常跑」。
 * 「扫描缺失」同时也可能出现在主按钮位上（第 1 步），这里保留一份是为了
 * 用户在别的步骤时也能重新统计 —— 不放到抽屉里，因为它仍然高频。
 */
const tidyMenu = computed(() => [
  { key: 'gaps', label: gapsLoading.value ? '统计中…' : '扫描缺失', icon: AlertTriangle, disabled: gapsLoading.value },
  { key: 'index', label: indexing.value ? '扫描中…' : '增量扫描', icon: FolderSearch, disabled: indexing.value },
  { key: 'audit', label: auditing.value ? '体检中…' : '曲库体检', icon: ScanSearch, disabled: auditing.value },
  { key: 'dedupe', label: deduping.value ? '查重中…' : '查找重复', icon: Copy, disabled: deduping.value },
  { key: 'undo', label: '撤销上一次补全', icon: RotateCcw, disabled: undoBusy.value || !completeJob.value?.undo_available },
  { key: 'options', label: '补全选项…', icon: SlidersHorizontal },
])

function onTidyMenu(key) {
  if (key === 'gaps') scanGaps()
  else if (key === 'index') runIndexScan()
  else if (key === 'audit') runAudit()
  else if (key === 'dedupe') runDedupe()
  else if (key === 'undo') undoComplete()
  else if (key === 'options') optionsOpen.value = true
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
})

onUnmounted(() => {
  stopCompletePolling()
})
</script>
