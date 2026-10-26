<template>
  <!--
    补全整理页（2026-10-21 顶部分段导航重构）

    用户口径：「这个补全整理的页面，不要再用 ⋯ 按钮了，换成顶部的导航栏，
    一个功能页面可以常驻，归类放在一起。」

    重构前：一整页 + 一张「补全·索引·查重」结果卡，六个功能入口（扫描缺失 /
    增量扫描 / 体检 / 查重 / 撤销 / 选项）全收在右上角 ⋯ 后面。
    重构后：壳层在页面顶部给了分段导航（SegmentedControl，与发现 / 下载 /
    系统同一套机制），五个分段各是一个常驻视图，控件归类进自己那段 ——
      · 补全（默认段）：字段 chips + 合并后的「扫描」（＝增量扫描 + 重算缺口）/
        预览 / 写入进度 / 结果；
      · 查重：查找重复 + 重复组列表 + 干跑预览 / 移入回收站；
      · 歌名修复（原「体检」）：AI 改歌名 + 命名规则 + 文件名体检 + 缺歌词；
      · 数据快照（原「记录」）：快照列表 + 撤销上一次补全 + 手动 / 定时快照；
      · 替换音质：/api/quality/scan|replace（后端 pkg/intercept/quality.go）。
    分段切换只是 v-show：所有状态都存在本组件的 ref 里，跑完的体检 /
    查重结果切走再切回来不丢。

    ⚠️ 2026-10-27 清理：原来的「⋯ 菜单」已经彻底不存在了 —— 「增量扫描」并进了
    卡片内的「扫描」按钮，「选项」是卡片头那颗按钮。所以 `completeMenu` /
    `onCompleteMenu` / `OverflowMenu` import 都是死代码，已删；
    相关注释里出现的「⋯」也已改掉（曾把用户指到一个不存在的控件上）。
  -->
  <PageShell>
    <!--
      2026-10-25 用户两条要求合并的落点：
        ①「统一补全整理里五个导航内的样式，右上角的按钮迁移到下方卡片内。」
        ②「元数据补全页面内的路径展示，迁移到五个分段导航行的右边。可以点击进行目录选择。」
      所以这里不再有页首动作区：每个分段的按钮回到各自卡片头（SectionCard 的 #actions），
      曲库目录改成导航行右边的 TidyDirChip（见 App.vue 里 SegmentedControl 的 #trail）。
    -->

    <!--
      分段内容的**公共容器**（PageShell 的默认 slot 只有这一层）。
      五个分段的 SectionCard 都是它的兄弟节点，靠 v-show 切换显隐。
      ⚠️ 别让 SectionCard 意外嵌套：一旦上面多了一层没闭合的标签，
      所有分段会被吸进第一个 SectionCard 的 slot，整页就只剩一块「导航栏」。
    -->
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
          <!--
            2026-10-25 用户：「右上角的按钮迁移到下方卡片内。」
            「选项」原本挂在页首（现在页首只剩曲库目录），搬进卡片头；
            「预览要写的内容」原来也在页首，按同一口径搬到这里 —— 卡片头就是这一段的所有入口。
          -->
          <button
            v-if="completeStep === 2"
            :disabled="previewing || completing || !selectedFields.length"
            :title="completeHint + (selectedFields.length ? '' : '（先在字段里勾要补的项）')"
            class="ys-btn ys-btn-primary ys-press"
            @click="startPreview"
          >{{ previewing ? '停止预览' : '预览要写的内容' }}</button>
          <button
            type="button"
            class="flex items-center gap-1.5 rounded-[9px] px-2 py-1 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
            title="补全字段、最多处理几首、覆盖口径、主力平台"
            @click="optionsOpen = true"
          >
            <SlidersHorizontal class="h-3.5 w-3.5" :stroke-width="1.8" />
            选项
          </button>
        </template>

        <!--
          一行摘要：缺口数字（曲库目录已挪到分段导航行右边，见页面顶部 #actions-trail）
        -->
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
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
          「下一步点哪」常驻正文（2026-10-27 修）。
          这句话原来只挂在预览按钮的 `title` 上 —— 手机上根本没有 hover，
          等于没写；而它是这一页唯一告诉用户「现在该做什么」的文案（服务层 completeHint）。
        -->
        <p class="mt-2 flex items-start gap-1.5 rounded-[10px] bg-[var(--ys-accent-soft)] px-2.5 py-1.5 text-[11px] leading-relaxed text-[var(--ys-accent)]">
          <Info class="mt-[1px] h-3.5 w-3.5 shrink-0" :stroke-width="2" />
          <span>
            {{ completeHint }}<template v-if="!selectedFields.length">（先在下面的字段里勾要补的项）</template>
          </span>
        </p>

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
          <!-- 合并后的唯一扫描按钮（原「扫描缺失」）：先收新文件，再重算缺口 -->
          <button
            v-if="completeStep === 1"
            :disabled="indexing || gapsLoading"
            :title="indexing || gapsLoading ? '正在扫描磁盘并重算缺口…' : '扫描磁盘上新增 / 变化的文件，并重新统计缺哪些字段'"
            class="ys-btn ys-btn-primary ys-press ml-auto"
            @click="scanAll"
          >{{ indexing || gapsLoading ? '扫描中…' : '扫描' }}</button>
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

        <!-- ── 预览（流式）：匹配完一首就出一行，还没有动过文件 ── -->
        <div v-if="previewOpen" class="mt-3.5 space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="flex items-center gap-2 text-[11px] font-semibold text-[var(--ys-text)]">
              {{ previewState === 'running' ? '正在逐条匹配…' : previewHeading }}
              <span class="font-mono font-normal text-[var(--ys-text-faint)]">
                {{ previewMeta.done }} / {{ previewMeta.total || '?' }}
              </span>
              <span v-if="previewMatchedCount" class="font-normal text-[var(--ys-text-muted)]">
                · 匹配到 {{ previewMatchedCount }} 首
              </span>
            </span>
            <span class="flex shrink-0 items-center gap-2">
              <!--
                2.3（2026-10-27）：注释里写的是「暂停 / 继续 / 停止」，模板只有「停止 / 重新预览」——
                现在把话术对齐成三个各自说清的动作：
                  · 暂停 = 断开流（服务端立刻取消，不再打外网），已出的行**保留**，但这份清单不能用
                  · 继续 = 从第一首重新匹配（后端不支持断点续传，别假装能接上）
                  · 收起 = 只把这块收起来，不影响任何状态
                原来叫「停止」容易被理解成「就停在这儿、之后能接着跑」，而那做不到。
              -->
              <button
                v-if="previewState === 'running'"
                class="rounded-full border border-[var(--ys-border)] bg-white px-3 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
                title="断开后服务端立刻停止匹配，已出的行会留着，但这份清单不完整、不能写入"
                @click="stopPreview"
              >暂停</button>
              <button
                v-else
                class="ys-btn ys-btn-sm ys-btn-primary ys-press"
                :title="previewRows.length ? '从第一首重新匹配；现在这份列表会被清空' : '开始逐条匹配'"
                @click="startPreview"
              >{{ previewRows.length ? '重新预览' : '预览' }}</button>
              <button
                class="rounded-full px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
                title="收起预览（不影响已写入的结果）"
                @click="closePreview"
              >收起</button>
            </span>
          </div>

          <!-- 进度条：只在跑的时候占位，跑完不留一条 100% 的装饰 -->
          <!-- 进度条统一走 .ys-progress（七）：原来这里 h-1（4px），另外两条 h-1.5（6px） -->
          <div v-if="previewState === 'running'" class="ys-progress">
            <i :style="{ width: previewPercent + '%' }" />
          </div>

          <div class="max-h-64 overflow-auto rounded-[10px] border border-[var(--ys-border)] bg-white">
            <table class="w-full min-w-[520px] text-[11px]">
              <thead class="sticky top-0 z-10 bg-[var(--ys-bg-soft)] text-[var(--ys-text-faint)]">
                <tr>
                  <th class="px-2 py-1.5 text-left font-medium">文件</th>
                  <th class="px-2 py-1.5 text-left font-medium">匹配到</th>
                  <th class="px-2 py-1.5 text-left font-medium">会写</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in previewRows" :key="it.path" class="border-t border-[var(--ys-border)]">
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
                <tr v-if="previewState === 'running' && !previewRows.length">
                  <td colspan="3" class="px-2 py-3 text-center text-[var(--ys-text-faint)]">
                    正在搜索匹配，第一首出来就会显示…
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <button
              v-if="previewPlanId"
              :disabled="completing || !previewMatchedCount"
              class="ys-btn ys-btn-primary ys-press"
              @click="executeComplete"
            >确认写入 {{ previewMatchedCount }} 首</button>
            <span v-if="previewPlanId" class="text-[11px] text-[var(--ys-text-faint)]">写入前会自动备份，可随时撤销</span>
            <span v-else-if="previewState === 'done'" class="text-[11px] text-amber-600">
              这份清单不完整，不能执行 —— 重新预览一次
            </span>
            <span v-else-if="previewState === 'idle'" class="text-[11px] text-[var(--ys-text-faint)]">
              已暂停，结果不完整；要继续请点「重新预览」（会从第一首重来）
            </span>
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
                class="ys-btn ys-btn-sm ys-btn-ghost-danger ys-press"
                title="不再开始新的曲目；已写入的文件保留，可以用「撤销上一次补全」回退"
                @click="stopComplete"
              >{{ stopBusy ? '停止中…' : '停止' }}</button>
            </span>
          </div>

          <div v-if="completeJob.running" class="ys-progress mt-0 w-full">
            <i :style="{ width: completePercent + '%' }"></i>
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

          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
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
        <template #actions>
          <button
            :disabled="deduping"
            title="扫描曲库，找出内容相同 / 同名的重复曲目"
            class="ys-btn ys-btn-primary ys-press"
            @click="runDedupe"
          >{{ deduping ? '查重中…' : '查找重复' }}</button>
        </template>
        <div v-if="!dupResult" class="ys-empty">
          {{ deduping ? '正在查重…' : '还没有查重结果 —— 点右上「查找重复」' }}
        </div>

        <div v-if="dupResult" class="space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">
              <template v-if="dupResult.group_count">
                发现 <b class="font-mono text-rose-600">{{ dupResult.group_count }}</b> 组重复
              </template>
              <template v-else>
                <span class="text-[var(--ys-accent)]">没有发现重复</span>
                <span class="ml-1 font-normal text-[var(--ys-text-faint)]">（同一首歌的不同版本按标签区分，不算重复）</span>
              </template>
            </span>
            <!-- 0 组时不给操作按钮：没有可移的文件，留着红按钮只会让人误点 -->
            <div v-if="dupResult.group_count" class="flex flex-wrap gap-1.5">
              <button
                :disabled="dupBusy"
                class="ys-press rounded-full border border-[var(--ys-border)] bg-white px-3 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
                @click="resolveDup(true)"
              >{{ dupBusy ? '…' : '干跑预览' }}</button>
              <!--
                P0-4（2026-10-27）：红按钮不再一点就动手。第一次点只跑干跑并把清单摊开，
                按钮变成「确认移入回收站」；第二次点才真移。原来走 window.confirm()，
                iOS PWA 下样式系统级、而且确认框里塞不下「将移走 N 个」的清单 ——
                用户只能盲确认，而这是整页唯一不可逆的批量操作。
              -->
              <button
                :disabled="dupBusy"
                class="ys-btn ys-btn-sm ys-btn-danger ys-press"
                :class="dupConfirmArmed ? 'ring-2 ring-rose-300' : ''"
                @click="resolveDup(false)"
              >{{ dupConfirmArmed ? '确认移入回收站' : '移入回收站' }}</button>
              <button
                v-if="dupConfirmArmed"
                :disabled="dupBusy"
                class="rounded-full border border-[var(--ys-border)] bg-white px-3 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
                @click="cancelResolveDup"
              >取消</button>
            </div>
          </div>

          <!-- 保留规则：谁被保留完全由后端决定，页面以前一个字都没说（2.7） -->
          <!--
            9.2 / 七（2026-10-27）：原文写的是 Markdown 的 `**…**`，在 HTML 里就是四个
            字面星号（真机上直接印出来）。顺便把回收站的**绝对路径**写出来并给个复制按钮 ——
            以前只说「曲库里的 .tidy_trash」，用户误删后不知道去哪儿找。
          -->
          <p class="text-[10.5px] leading-snug text-[var(--ys-text-muted)]">
            保留规则：内容相同的取码率/时长更优的那份，同名不同版的保留标签更完整的；
            被清理的文件<b>移到回收站，不会直接删除</b>，需要时可以自己去里面捞回来。
            <span v-if="dupResult.group_count > (dupResult.groups || []).length" class="text-amber-700">
              下方清单只列了前 {{ (dupResult.groups || []).length }} 组（共 {{ dupResult.group_count }} 组），
              点「干跑预览」会给出全部影响的文件数。
            </span>
          </p>
          <div v-if="trashPath" class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px] text-[var(--ys-text-faint)]">
            <span>回收站就在：</span>
            <code class="min-w-0 truncate font-mono text-[var(--ys-text-muted)]" :title="trashPath">{{ trashPath }}</code>
            <button
              class="shrink-0 underline decoration-dotted underline-offset-2 hover:no-underline"
              @click="copyTrashPath"
            >复制路径</button>
          </div>

          <div v-if="dupResult.groups.length" class="max-h-64 space-y-1.5 overflow-y-auto">
            <div v-for="(g, i) in visibleDupGroups" :key="i" class="rounded-[10px] border border-[var(--ys-border)] bg-white p-2.5 text-[11px]">
              <div class="flex items-center gap-2">
                <span :class="['rounded-full px-1.5 py-0.5 font-mono text-[10px]', g.type === 'content' ? 'bg-rose-50 text-rose-600' : 'bg-amber-50 text-amber-600']">
                  {{ g.type === 'content' ? '内容相同' : '同名不同版' }}
                </span>
                <span class="text-[var(--ys-text-faint)]">{{ g.count }} 个文件</span>
              </div>
              <div class="mt-1 truncate text-[var(--ys-accent)]" :title="g.keep">保留：{{ baseName(g.keep) }}</div>
              <div class="truncate text-rose-500" :title="g.delete.join('、')">清理：{{ g.delete.map(baseName).join('、') }}</div>
            </div>
            <button
              v-if="dupResult.groups.length > 30"
              class="w-full rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
              @click="dupShowAll = !dupShowAll"
            >{{ dupShowAll ? '只看前 30 组' : `展开全部 ${dupResult.groups.length} 组` }}</button>
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
      <!--
        ═════ 分段三：歌名修复（原「体检」）═════
        2026-10-24 用户：「去除体检中的曲库体检，更名为歌名修复，页面加入歌名修复功能。」
        原来的「曲库体检」只统计缺歌词/缺封面（audit 四格），用户明确不要了；
        这一段的主动作换成歌名修复：扫出没有歌名 / 歌名就是文件名的曲目，
        交给 AI 从候选里挑或直接按文件名解析，**只改标签里的标题字段**。
        文件名体检（LibraryHealthCard）留着 —— 它是「磁盘条目本身不合法」，
        和改歌名是两件事，但它就在这一段里，用户修完名字可以顺手扫一遍。
      -->
      <SectionCard
        v-show="sub === 'audit'"
        :icon="ScanSearch"
        title="歌名修复"
        sub="没有歌名 / 歌名就是文件名的曲目：让 AI 从搜索结果里挑，或按文件名解析 —— 只改标题标签，可撤回"
      >
        <template #actions>
          <!-- 2026-10-25 用户：「右上角的按钮迁移到下方卡片内」—— 扫描歌名 + 修复记录时间都进卡片头 -->
          <span v-if="titleFixRecordsAtText" class="text-[10.5px] text-[var(--ys-text-faint)]">{{ titleFixRecordsAtText }}</span>
          <button
            :disabled="titleFixScanning"
            :title="titleFixScanning ? '正在从曲库索引里挑出歌名可疑的曲目…' : '扫出没有歌名 / 歌名就是文件名的曲目，交给 AI 修'"
            class="ys-btn ys-btn-primary ys-press"
            @click="scanTitleFix"
          >{{ titleFixScanning ? '扫描中…' : '扫描歌名' }}</button>
        </template>

        <!-- 空态 -->
        <div v-if="!titleFixItems.length && !titleFixScanning" class="ys-empty">
          <p>还没有扫到可疑歌名 —— 点右上「扫描歌名」</p>
          <p class="mt-1 text-[10.5px]">判定口径：标题为空、标题就是文件名（有没有扩展名都算）、标题里带「未知」「unknown」「未命名」</p>
        </div>

        <div v-if="titleFixScanning" class="ys-empty">
          正在从曲库索引里挑歌名可疑的曲目…
        </div>

        <!-- AI 修复卡：批量猜 + 逐条改 -->
        <div v-if="titleFixItems.length" class="mt-1 space-y-2.5 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] p-3.5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">
              歌名可疑 {{ titleFixItems.length }} 首
              <span v-if="titleFixSelectedCount" class="text-[var(--ys-accent)]">· 已勾 {{ titleFixSelectedCount }}</span>
            </span>
            <span class="flex items-center gap-2">
              <button
                :disabled="titleFixBusy || !titleFixSelectedCount"
                class="ys-btn ys-btn-sm ys-btn-primary ys-press"
                @click="runTitleFix"
              >{{ titleFixBusy ? '修复中…' : `AI 修复 ${titleFixSelectedCount || ''}` }}</button>
              <button
                :disabled="titleFixBusy"
                class="rounded-full px-2.5 py-1.5 text-[11px] text-[var(--ys-text-muted)] hover:text-[var(--ys-text)] disabled:opacity-50"
                @click="toggleAllTitleFix"
              >{{ titleFixAllSelected ? '全不选' : '全选' }}</button>
              <!-- 9.6（2026-10-27）：清空整份待修名单原来一下就点没了（虽然能重扫，
                   但几百首的勾选要重来一遍）—— 和查重一样点两次。 -->
              <button
                :disabled="titleFixBusy"
                class="ys-btn ys-btn-sm"
                :class="titleFixClearArmed ? 'ys-btn-ghost-danger font-semibold' : ''"
                @click="clearTitleFixList"
              >{{ titleFixClearArmed ? '确认清空' : '清空' }}</button>
            </span>
          </div>

          <!--
            3.8（2026-10-27）：几百首时「我刚勾了哪几首」在表里找不着 ——
            加「只看勾选的」+「按歌名排序」。默认不过滤、不排序（保持扫描顺序）。
          -->
          <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-[var(--ys-text-muted)]">
            <label class="flex cursor-pointer items-center gap-1.5">
              <input
                type="checkbox"
                class="h-3.5 w-3.5 accent-[var(--ys-accent)]"
                :checked="titleFixOnlySelected"
                @change="setTitleFixOnlySelected($event.target.checked)"
              />
              只看勾选的
            </label>
            <label class="flex cursor-pointer items-center gap-1.5">
              <input
                type="checkbox"
                class="h-3.5 w-3.5 accent-[var(--ys-accent)]"
                :checked="titleFixSort === 'title'"
                @change="setTitleFixSort($event.target.checked ? 'title' : 'file')"
              />
              按歌名排序（空的排前面）
            </label>
            <span v-if="titleFixHiddenCount" class="text-[var(--ys-text-faint)]">已隐藏 {{ titleFixHiddenCount }} 条未勾选</span>
            <span class="ml-auto text-[10.5px] text-[var(--ys-text-faint)]">共 {{ titleFixItems.length }} 条</span>
          </div>

          <div v-if="!titleFixView.length" class="ys-empty ys-empty-sm">
            勾选被「只看勾选的」藏住了 —— 取消勾选这句就能看到全部 {{ titleFixItems.length }} 条
          </div>
          <div v-else class="max-h-80 overflow-auto rounded-[10px] border border-[var(--ys-border)] bg-white">
            <table class="w-full min-w-[620px] text-[11px]">
              <thead class="sticky top-0 z-10 bg-[var(--ys-bg-soft)] text-[var(--ys-text-faint)]">
                <tr>
                  <th class="w-8 px-2 py-1.5"></th>
                  <th class="px-2 py-1.5 text-left font-medium">文件</th>
                  <th class="px-2 py-1.5 text-left font-medium">当前的歌名</th>
                  <th class="px-2 py-1.5 text-left font-medium">
                    <!--
                      2026-10-26 用户：「把修复后的列改成手动歌名体检，
                      点击后随机抽取 10 首歌曲交给AI修复，并展示修复结果（包括是否可以修复、带原歌名、新歌名等）」。
                      所以这一列的表头不再是"结果"两个字，而是那个动作的入口。
                    -->
                    <span class="flex items-center gap-1.5">
                      <span>修复后</span>
                      <button
                        :disabled="manualAuditBusy"
                        class="ys-press rounded-full border border-[var(--ys-border)] bg-white px-2 py-0.5 text-[10px] font-normal text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
                        title="随便抽 10 首（含歌名正常的）让 AI 判断能不能修复、会改成什么 —— 只出结果，不改文件"
                        @click="runManualTitleAudit"
                      >{{ manualAuditBusy ? '抽查中…' : '手动歌名体检' }}</button>
                    </span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in titleFixView" :key="it.path" class="border-t border-[var(--ys-border)]">
                  <!-- 8.5：勾选框热区原来是 16px（只有 input 本身），手指点不中 ——
                       用 label 把整格撑成 32×32 的可点区域，视觉尺寸不变。 -->
                  <td class="w-10 px-1.5 py-1">
                    <label class="flex h-8 w-8 cursor-pointer items-center justify-center">
                      <input type="checkbox" class="h-4 w-4" :checked="it.selected" :disabled="titleFixBusy" @change="toggleTitleFix(it)" />
                    </label>
                  </td>
                  <td class="max-w-[220px] truncate px-2 py-1 font-mono text-[var(--ys-text-muted)]" :title="it.path">
                    {{ baseName(it.path) }}
                  </td>
                  <td class="ys-cell max-w-[150px] px-2 py-1 text-rose-600" :title="it.title">
                    {{ it.title || '（空）' }}
                  </td>
                  <td class="ys-cell max-w-[220px] px-2 py-1 text-[var(--ys-text)]" :title="it.suggested_title">
                    <template v-if="it.done">
                      <span class="text-[var(--ys-accent)]">✓ {{ it.written_title }}</span>
                    </template>
                    <template v-else-if="it.suggested_title">{{ it.suggested_title }}</template>
                    <template v-else-if="it.error"><span class="text-amber-600">{{ it.error }}</span></template>
                    <template v-else>—</template>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!--
            六-3：页面上有两个「AI 按钮」，区别原来只写在 tooltip 里（手机上等于没有）。
            正文里点明分工：扫描歌名 = 按规则挑可疑的、会写文件；手动体检 = 随机抽 10 首、
            只出结论不写文件，专门用来验证 AI 靠不靠谱。
          -->
          <p class="text-[10.5px] leading-relaxed text-[var(--ys-text-faint)]">
            只改文件里的标题标签，不动文件名；每次修复都进「数据快照」，可以随时撤回。
            这里的「扫描歌名」按规则挑出可疑的，会写文件；
            「手动歌名体检」是随机抽 10 首（含歌名看着正常的）让 AI 判断，
            <b>只出结论、不写文件</b> —— 想先看看 AI 靠不靠谱就用它。
          </p>
        </div>

        <!--
          手动歌名体检（用户 2026-10-26）：「点击后随机抽取 10 首歌曲交给 AI 修复，
          并展示修复结果（包括是否可以修复、带原歌名、新歌名等）」。
          入口在上方表格「修复后」列的表头。**纯只读**：不写标签、不留快照 ——
          目的就是回答「AI 到底靠不靠谱」，所以样本是随机抽的（也包含歌名看着正常的），
          只挑可疑样本会得出「AI 全都修对了」的假象。真改请勾回去走 AI 修复。
        -->
        <div v-if="manualAuditItems.length || manualAuditBusy" class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-white p-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">
              手动歌名体检
              <span v-if="manualAuditBusy" class="ml-1 font-normal text-[var(--ys-text-faint)]">正在逐首问 AI…</span>
              <span v-else-if="manualAuditSummary" class="ml-1 font-normal text-[var(--ys-text-muted)]">{{ manualAuditSummary }}</span>
            </span>
            <span class="flex items-center gap-2">
              <button
                :disabled="manualAuditBusy"
                class="rounded-full px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] hover:text-[var(--ys-text)] disabled:opacity-50"
                @click="runManualTitleAudit"
              >再抽 10 首</button>
              <button
                v-if="manualAuditItems.length"
                class="rounded-full px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]"
                @click="manualAuditItems = []; manualAuditSummary = ''"
              >清空</button>
            </span>
          </div>
          <div v-if="manualAuditItems.length" class="mt-2 max-h-72 overflow-auto rounded-[10px] border border-[var(--ys-border)]">
            <table class="w-full min-w-[780px] text-[11px]">
              <thead class="sticky top-0 z-10 bg-[var(--ys-bg-soft)] text-[11px] text-[var(--ys-text-faint)]">
                <tr>
                  <th class="px-2 py-1.5 text-left font-normal">文件</th>
                  <th class="px-2 py-1.5 text-left font-normal">原歌名</th>
                  <th class="px-2 py-1.5 text-left font-normal">能不能修</th>
                  <th class="px-2 py-1.5 text-left font-normal">AI 给的新歌名</th>
                  <th class="px-2 py-1.5 text-left font-normal">说明</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in manualAuditItems" :key="it.path" class="border-t border-[var(--ys-border)] align-top">
                  <td class="max-w-[180px] truncate px-2 py-1 font-mono text-[var(--ys-text-muted)]" :title="it.path">{{ it.file }}</td>
                  <td class="max-w-[150px] truncate px-2 py-1" :title="it.title">
                    <span :class="it.need ? 'text-rose-600' : 'text-[var(--ys-text)]'">{{ it.title || '（空）' }}</span>
                    <span v-if="it.artist" class="ml-1 text-[10px] text-[var(--ys-text-faint)]">/ {{ it.artist }}</span>
                  </td>
                  <td class="whitespace-nowrap px-2 py-1">
                    <span :class="['rounded-full px-1.5 py-0.5 text-[10px]', it.fixable ? 'bg-emerald-50 text-emerald-700' : 'bg-amber-50 text-amber-700']">
                      {{ it.fixable ? '能修' : '修不了' }}
                    </span>
                    <span class="ml-1 text-[10px] text-[var(--ys-text-faint)]">{{ it.source === 'ai' ? 'AI' : it.source === 'rule' ? '规则' : '—' }}</span>
                  </td>
                  <td class="max-w-[180px] truncate px-2 py-1 text-[var(--ys-accent)]" :title="it.new_title">{{ it.new_title || '—' }}</td>
                  <td class="max-w-[200px] px-2 py-1 text-[10px] leading-relaxed text-[var(--ys-text-faint)]">{{ it.note || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="mt-1.5 text-[10px] leading-relaxed text-[var(--ys-text-faint)]">
            只出结果，不改任何文件。判定口径与「AI 修复」完全一致，所以这里说能修、那边真修就一定修得出来。
          </p>
        </div>

        <!-- 修复记录：可撤回 / 批量撤回 / 改回手写标题 -->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-white p-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">
              修复记录 <span class="font-mono">{{ titleFixRecords.length }}</span>
            </span>
            <span class="flex items-center gap-2">
              <button
                v-if="titleFixSelectedRecords.length"
                :disabled="titleFixUndoBusy"
                class="ys-btn ys-btn-sm ys-btn-ghost-danger ys-press"
                @click="undoTitleFixRecords(titleFixSelectedRecords)"
              >撤回已勾 {{ titleFixSelectedRecords.length }}</button>
              <button
                class="rounded-full px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]"
                @click="loadTitleFixRecords"
              >刷新</button>
            </span>
          </div>
          <p v-if="!titleFixRecords.length" class="mt-2 text-[11px] text-[var(--ys-text-faint)]">
            还没有修复记录 —— 修过之后这里会列出每一首改了什么，可以逐条撤回。
          </p>
          <div v-else class="mt-2 max-h-64 space-y-1 overflow-y-auto">
            <div v-for="r in titleFixRecords" :key="r.id" class="flex items-center gap-2 rounded-[8px] border border-[var(--ys-border)] bg-[#FAFAFC] px-2 py-1.5 text-[11px]">
              <label class="flex h-7 w-7 shrink-0 cursor-pointer items-center justify-center">
                <input type="checkbox" :checked="titleFixSelectedRecords.includes(r.id)" @change="toggleTitleFixRecord(r.id)" />
              </label>
              <span class="min-w-0 flex-1 truncate" :title="r.path">
                <s class="text-[var(--ys-text-faint)]">{{ r.old_title || '（空）' }}</s>
                <span class="mx-1 text-[var(--ys-text-faint)]">→</span>
                <span class="text-[var(--ys-text)]">{{ r.new_title }}</span>
              </span>
              <span class="shrink-0 text-[10px] text-[var(--ys-text-faint)]">{{ r.at_text }}</span>
              <span v-if="r.undone" class="shrink-0 text-[10px] text-[var(--ys-text-faint)]">已撤回</span>
              <!-- 改回手写标题：撤回之后再改一个，记录会记住最终值 -->
              <button
                v-else
                class="shrink-0 rounded-full px-2 py-0.5 text-[10px] text-[var(--ys-text-muted)] hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
                @click="editTitleFixRecord(r)"
              >修改</button>
              <button
                v-if="!r.undone"
                class="ys-btn ys-btn-sm ys-btn-ghost-danger ys-press"
                @click="undoTitleFixRecords([r.id])"
              >撤回</button>
            </div>
          </div>
        </div>
      </SectionCard>

      <!--
        命名解析规则 + 文件名语义解析试算（用户 2026-10-26）：
        「账号连接里的 AI 大模型，下面的文件名语义解析、命名解析规则 / AI 生成规则 /
        自定义规则，都迁移到补全整理 → 歌名修复卡片内。」
        理由：这两件事只服务「把文件名拆成歌名+歌手」这一个用途，
        放在 AI 配置页里用户永远找不到；歌名修复修不好时最需要它的地方就是这里。
        组件自包含（自己拉规则 / 试算 / 生成 / 保存），见 NameRulesPanel.vue。
      -->
      <NameRulesPanel v-show="sub === 'audit'" class="mt-3" :dir="curDir" />

      <!--
        文件名体检（用户 2026-10-24）：「飞牛文件和官方音乐 App 打开音乐文件夹显示未知错误」。
        和上面的曲库体检是同一件事的两种口径 —— 上面数的是「元数据缺口」，
        这张看的是「磁盘条目本身不合法」。放同一段，用户不用点两次才找得到。
        2026-10-26 用户：「下面的文件名体检可以与歌名修复合并，并去掉相同的功能」——
        它现在排在「修复记录」之下、同一张卡片里，由 LibraryHealthCard 自渲染（无外框）。
      -->
      <LibraryHealthCard v-show="sub === 'audit'" merge :dir="healthDir || curDir" />

      <!--
        ═════ 缺歌词（用户 2026-10-25 需求 N）═════
        「加一个缺歌词的代码」—— 扫曲库里真的没词的文件，先「查找歌词」预览命中情况，
        确认无误再「写入文件」（内嵌标签 + 同名 .lrc 都写）。
        判据用后端 tidy.NeedsTidy（内嵌与 .lrc 都算有词），和曲库管家同一口径。
        放在歌名修复段里而不单开一段：两者都是「文件里缺东西」，用户找的时候在一个地方。
      -->
      <SectionCard
        v-show="sub === 'audit'"
        :icon="FileAudio"
        title="缺歌词"
        :desc="'曲库里没有歌词的歌。先「查找歌词」看命中情况，确认后再写入 —— 每条都会同时写内嵌标签和同名 .lrc。' + (lyricAtText ? '（' + lyricAtText + '）' : '')"
      >
        <template #actions>
          <button class="ys-btn ys-btn-sm" :disabled="lyricScanning" @click="scanLyricGaps">
            {{ lyricScanning ? '扫描中…' : '扫描缺歌词' }}
          </button>
          <button
            v-if="lyricItems.length"
            class="ys-btn ys-btn-sm"
            :disabled="lyricFetching || !lyricSelectedCount"
            @click="fetchLyricPreview"
          >{{ lyricFetching ? '查找中…' : `查找歌词（${lyricSelectedCount}）` }}</button>
          <button
            v-if="lyricFoundCount"
            class="ys-btn ys-btn-sm ys-btn-primary"
            :disabled="lyricWriting"
            @click="writeLyric"
          >{{ lyricWriting ? '写入中…' : `写入文件（${lyricFoundCount}）` }}</button>
        </template>

        <div v-if="!lyricItems.length" class="ys-empty">
          点右上角「扫描缺歌词」，看看哪些歌还没有词。
        </div>

        <template v-else>
          <div class="mb-2 flex flex-wrap items-center gap-3 text-[11px] text-[var(--ys-text-muted)]">
            <label class="flex cursor-pointer items-center gap-1.5">
              <input type="checkbox" :checked="lyricAllSelected" @change="toggleAllLyric" />
              全选
            </label>
            <span>共 {{ lyricTotal }} 首缺词，本页 {{ lyricItems.length }} 首</span>
            <span v-if="lyricSelectedCount">已勾 {{ lyricSelectedCount }} 首</span>
            <button class="ml-auto text-[11px] text-[var(--ys-text-faint)] hover:text-[var(--ys-text)]" @click="clearLyricGaps">清空</button>
          </div>

          <div class="max-h-[420px] overflow-auto overscroll-contain rounded-xl border border-[var(--ys-border)]">
            <table class="w-full min-w-[520px] text-[12px]">
              <thead class="sticky top-0 z-10 bg-[var(--ys-bg-soft)] text-[11px] text-[var(--ys-text-faint)]">
                <tr>
                  <th class="w-8 px-2 py-1.5"></th>
                  <th class="px-2 py-1.5 text-left font-normal">文件</th>
                  <th class="px-2 py-1.5 text-left font-normal">标题 / 歌手</th>
                  <th class="px-2 py-1.5 text-left font-normal">结果</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in lyricItems" :key="it.path" class="border-t border-[var(--ys-border)]">
                  <td class="w-10 px-1.5 py-1.5 align-top">
                    <label v-if="!it.written" class="flex h-8 w-8 cursor-pointer items-center justify-center">
                      <input type="checkbox" :checked="it.selected" @change="toggleLyric(it)" />
                    </label>
                  </td>
                  <td class="max-w-[280px] truncate px-2 py-1.5 align-top" :title="it.path">{{ it.name }}</td>
                  <td class="px-2 py-1.5 align-top">
                    <div class="ys-cell" :title="it.title || '—'">{{ it.title || '—' }}</div>
                    <div class="ys-cell text-[10px] text-[var(--ys-text-muted)]" :title="it.artist || '—'">{{ it.artist || '—' }}</div>
                  </td>
                  <td class="px-2 py-1.5 align-top">
                    <span v-if="it.written" class="text-emerald-600">已写入</span>
                    <template v-else-if="it.ok">
                      <span class="text-emerald-600">找到{{ it.note ? '（' + it.note + '）' : '' }}</span>
                      <!--
                        P3-15（2026-10-27）：原来「找到」只是个绿色文字，用户点「写入文件」
                        之前看不到会被写进去的到底是什么词 —— 匹配到 live 版 / 纯音乐版 /
                        错版歌词都只能写完再发现。歌词文本其实早就在 `it.lyric` 里了
                        （useLyricGaps 的 `lyric: h.lyric || it.lyric`），只是没人渲染。
                      -->
                      <button
                        v-if="it.lyric"
                        class="ys-btn ys-btn-sm ys-btn-ghost ml-1 px-1.5 py-0.5 text-[10px]"
                        :aria-expanded="lyricPreviewPath === it.path ? 'true' : 'false'"
                        @click="toggleLyricPreview(it)"
                      >{{ lyricPreviewPath === it.path ? '收起' : '看词' }}</button>
                    </template>
                    <span v-else-if="it.reason === 'timed_out'" class="text-amber-600">{{ it.note || '网络超时，没来得及试' }}</span>
                    <span v-else-if="it.reason === 'not_found'" class="text-[var(--ys-text-muted)]">没找到</span>
                    <span v-else-if="it.reason === 'no_meta'" class="text-[var(--ys-text-muted)]">缺标题/歌手，无法搜词</span>
                    <span v-else-if="it.err" class="text-rose-600">{{ it.err }}</span>
                    <span v-else class="text-[var(--ys-text-faint)]">未查找</span>
                  </td>
                </tr>
                <!-- 歌词预览行（P3-15）：只读、不写盘，看完再决定要不要勾选写入 -->
                <tr v-if="lyricPreviewPath === it.path" class="border-t border-[var(--ys-border)] bg-[var(--ys-surface-2)]">
                  <td colspan="4" class="px-3 py-2">
                    <div class="mb-1 flex flex-wrap items-center gap-2 text-[10.5px] text-[var(--ys-text-faint)]">
                      <span>歌词预览 · {{ (lyricPreviewLines(it).length) }} 行</span>
                      <span>写入时会同时写内嵌标签和同名 .lrc</span>
                      <button
                        class="ml-auto text-[10.5px] text-[var(--ys-text-faint)] hover:text-[var(--ys-text)]"
                        @click="lyricPreviewPath = ''"
                      >关闭预览</button>
                    </div>
                    <pre class="max-h-56 overflow-auto overscroll-contain whitespace-pre-wrap break-all rounded-[8px] border border-[var(--ys-border)] bg-white p-2 font-mono text-[10.5px] leading-relaxed text-[var(--ys-text-muted)]">{{ lyricPreviewText(it) }}</pre>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </SectionCard>


      <!--
        ═════ 分段四：数据快照（原「记录」）═════
        2026-10-24 用户：「记录更名为数据快照，增加定时快照和手动快照的功能和数据恢复。」
        语义变了：以前这里只有「撤销上一次补全」这一个入口，现在它是**一条快照列表** ——
        每次写文件之前都留了一份快照（补全 / 歌名修复 / 手动），任何时候都能回到那一刻。
        快照的数据源仍是备份清单（pkg/complete/backup.go），不另开数据库。
      -->
      <SectionCard
        v-show="sub === 'records'"
        :icon="History"
        title="数据快照"
        sub="曲库元数据的快照：哪天标签被改坏了，可以按快照改回来（只存标签，不复制音频）"
      >
        <template #actions>
          <button
            :disabled="reloading"
            title="重新拉取快照与上一次补全的状态"
            class="ys-press rounded-full border border-[var(--ys-border)] bg-white px-3.5 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-[var(--ys-surface-2)] disabled:opacity-50"
            @click="onRefresh"
          >{{ reloading ? '刷新中…' : '刷新' }}</button>
        </template>
        <p v-if="statsLoading" class="text-[11px] text-[var(--ys-text-faint)]">正在读取上次整理快照…</p>
        <p v-else-if="statsError" class="text-[11px] leading-relaxed text-amber-700">
          {{ statsError }} —— 下面这些数字读不到，不代表库里没有歌
        </p>
        <!--
          2026-10-27 修 2.5：原来这段说明只挂在 title 上，而且指路指向「补全」段的 ⋯ 菜单 ——
          那个溢出菜单早就删了（增量扫描并进了「扫描」按钮）。移动端用户既看不到 tooltip，
          又按图索骥找不到入口。现在：正文一行说明 + 一个就地可点的「立即更新索引」。
        -->
        <div v-else-if="indexStats" class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <p class="min-w-0 text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            {{ lastScanText || '索引还没整理过' }} · 这一段的数字读自索引；
            若和「补全」段的待补数量对不上，说明索引过期了
          </p>
          <button
            :disabled="indexing"
            class="shrink-0 rounded-full border border-[var(--ys-border)] bg-white px-2 py-0.5 text-[10px] text-[var(--ys-text-muted)] transition-colors hover:bg-[var(--ys-surface-2)] disabled:opacity-50"
            title="就地把磁盘上新增/变化的文件收进索引（等于「补全」段的「扫描」）"
            @click="refreshIndexHere"
          >{{ indexing ? '更新中…' : '立即更新索引' }}</button>
        </div>
        <p v-else class="text-[11px] text-[var(--ys-text-faint)]">还没有索引快照 —— 点右上「刷新」再试</p>

        <!-- 撤销入口：原来既在 ⋯ 里、又贴在补全进度条下；按归类放进「记录」段 -->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-[#FAFAFC] px-3.5 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">撤销上一次补全</span>
            <button
              :disabled="undoBusy || !completeJob || !completeJob.undo_available || completeJob.running"
              class="rounded-[8px] bg-black/[0.04] px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.07] disabled:opacity-50"
              :class="undoConfirmArmed ? 'ring-2 ring-rose-300' : ''"
              @click="undoComplete"
            >{{ undoBusy ? '还原中…' : (undoConfirmArmed ? '确认还原' : '还原') }}</button>
          </div>
          <!--
            9.1（2026-10-27）：最后一处 window.confirm 换成就地两步。
            iOS PWA 与微信内置浏览器里 confirm 是系统弹窗，样式不可控、换行难看，
            而且这个操作是「只做一次、没有重做」的 —— 值得多要一次明确点击。
          -->
          <p v-if="undoConfirmArmed" class="mt-1.5 rounded-[8px] bg-rose-50 px-2.5 py-1.5 text-[11px] leading-relaxed text-rose-700">
            再点一次「确认还原」就会把
            <b v-if="completeJob && completeJob.undo_count">上次补全改过的 {{ completeJob.undo_count }} 个文件</b>
            <b v-else>上次补全改过的文件</b>
            改回补全前的样子。<b>这一步只能做一次</b>，没有「重做」——
            想再补上得重新跑一次补全。
            <button
              :disabled="undoBusy"
              class="ml-1 rounded-full border border-rose-300 bg-white px-2.5 py-0.5 text-[11px] text-rose-700 transition-colors hover:bg-rose-100 disabled:opacity-50"
              @click="cancelUndoComplete"
            >取消</button>
          </p>
          <p v-if="!undoConfirmArmed" class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            <template v-if="completeJob && completeJob.undo_available && !completeJob.running">只把上一次补全改过的文件还原成补全前的样子，别的不碰</template>
            <template v-else-if="completeJob && completeJob.running">补全任务还在跑 —— 结束后才能撤销</template>
            <template v-else>目前没有可撤销的补全 —— 补全写入前会自动备份，跑完一次这里就会出现「还原」</template>
          </p>
        </div>

        <!-- 手动快照 -->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-white px-3.5 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">手动快照</span>
            <span class="flex items-center gap-2">
              <button
                :disabled="snapBusy"
                class="ys-btn ys-btn-sm ys-btn-primary ys-press"
                @click="takeSnapshot"
              >{{ snapBusy ? '拍快照中…' : '立即快照' }}</button>
              <!-- 六-1：和卡片头那个「刷新」区分开 —— 这里只重拉快照列表（轻），头上的全量重读五段（重） -->
              <button
                :disabled="snapBusy"
                title="只重新拉快照列表；头上的「刷新」会连缺口、结果卡片等一起重读"
                class="rounded-full px-2.5 py-1.5 text-[11px] text-[var(--ys-text-muted)] hover:text-[var(--ys-text)] disabled:opacity-50"
                @click="loadSnapshots"
              >刷新列表</button>
            </span>
          </div>
          <p class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-faint)]">
            手动快照把<b>每首歌的元数据（歌名/歌手/专辑/风格/年份/曲序）</b>存一份 ——
            只存标签，一库几万首也只有几 MB，音频文件本身不复制。之后哪天标签被改坏了，
            点列表里的「恢复到这份」就能改回来；恢复前还会自动再留一份，点错也不怕。
          </p>
        </div>

        <!-- 定时快照（用户 2026-10-24：「增加定时快照和手动快照」）-->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-white p-3"
             title="按固定间隔自动拍一份快照，保留最近几份，更早的自动删掉">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[11px] font-semibold text-[var(--ys-text)]">定时快照</span>
            <button
              type="button"
              class="ys-btn ys-btn-sm ys-press"
              :class="schedule.enabled
                ? 'ys-btn-primary'
                : 'bg-black/[0.06] text-[var(--ys-text-muted)] hover:bg-black/[0.1]'"
              :disabled="scheduleBusy || !!scheduleReadError"
              @click="toggleSchedule"
            >{{ schedule.enabled ? '已开启' : '已关闭' }}</button>
            <span class="text-[11px] text-[var(--ys-text-muted)]">每</span>
            <SelectField
              class="w-28"
              :model-value="String(schedule.interval_hours || 24)"
              :options="SNAPSHOT_INTERVAL_OPTIONS"
              @update:model-value="setScheduleInterval"
            />
            <span class="text-[11px] text-[var(--ys-text-muted)]">保留最近</span>
            <SelectField
              class="w-24"
              :model-value="String(schedule.keep || 7)"
              :options="SNAPSHOT_KEEP_OPTIONS"
              @update:model-value="setScheduleKeep"
            />
            <span class="text-[11px] text-[var(--ys-text-muted)]">份</span>
            <span class="ml-auto text-[10px] text-[var(--ys-text-faint)]">{{ scheduleLastText }}</span>
          </div>
          <p v-if="scheduleReadError" class="mt-1.5 text-[10px] leading-snug text-amber-700">
            {{ scheduleReadError }} —— 上面这个开关显示的不一定是真实状态，别在这里判断定时有没有开
          </p>
          <p class="mt-1.5 text-[10px] text-[var(--ys-text-faint)]">
            打开时立刻拍一份；之后按间隔自动拍。只清理「定时快照」，手动快照与写入前快照永远保留。
          </p>
        </div>

        <!-- 快照列表 -->
        <div class="mt-3 rounded-[12px] border border-[var(--ys-border)] bg-white p-3">
          <p v-if="snapLoading" class="text-[11px] text-[var(--ys-text-faint)]">正在读取快照列表…</p>
          <p v-else-if="!snapshots.length" class="text-[11px] text-[var(--ys-text-faint)]">还没有快照 —— 补全或歌名修复跑过一次后，这里会出现。</p>
          <div v-else class="space-y-1">
            <!--
              每份快照一行，明细**紧跟在被点的那一行下面**（2026-10-27 修）。
              以前明细是列表末尾的一个独立块：快照攒到十几份时，点第 2 行的「看明细」，
              内容出现在第 15 行下面 —— 用户以为没反应，而且没有任何关闭入口。
            -->
            <template v-for="sn in snapshots" :key="sn.id">
              <div
                class="flex flex-wrap items-center gap-2 rounded-[8px] border px-2.5 py-2 text-[11px] transition-colors"
                :class="isSnapOpen(sn)
                  ? 'border-[#0B8A66]/35 bg-[#F3FAF7]'
                  : 'border-[var(--ys-border)] bg-[#FAFAFC]'"
              >
                <span class="min-w-0 flex-1 truncate">
                  <b class="font-mono text-[var(--ys-text)]">{{ sn.at_text || sn.id }}</b>
                  <span class="ml-2 text-[var(--ys-text-muted)]">{{ sn.kind_text }}</span>
                  <span class="ml-2 text-[var(--ys-text-faint)]">{{ sn.count }} 首的元数据</span>
                  <span v-if="sn.note" class="ml-2 text-[var(--ys-text-faint)]">· {{ sn.note }}</span>
                </span>
                <button
                  class="shrink-0 rounded-full px-2 py-0.5 text-[10px] text-[var(--ys-text-muted)] hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
                  :aria-expanded="isSnapOpen(sn) ? 'true' : 'false'"
                  @click="viewSnapshot(sn)"
                >{{ isSnapOpen(sn) ? '收起明细' : '看明细' }}</button>
                <button
                  :disabled="snapBusy"
                  :class="[
                    'ys-btn ys-btn-sm',
                    isRestoreArmed(sn) ? 'ys-btn-danger' : 'ys-btn-ghost-danger',
                  ]"
                  :aria-pressed="isRestoreArmed(sn) ? 'true' : 'false'"
                  @click="armRestoreSnapshot(sn)"
                >{{ isRestoreArmed(sn) ? '已选这份' : '恢复到这份' }}</button>
              </div>

              <!-- 明细：只列「和现在不一样」的曲目 —— 几万首全列出来没人看得下去 -->
              <div
                v-if="isSnapOpen(sn)"
                class="rounded-[8px] border border-[var(--ys-border)] bg-white p-2"
              >
                <div class="flex flex-wrap items-center gap-2">
                  <p class="min-w-0 flex-1 text-[11px] leading-snug text-[var(--ys-text-muted)]">
                    快照 {{ fmtTimeText(snapDetail.created) }} · {{ snapDetail.count || 0 }} 首 ·
                    可写标签 {{ snapDetail.writable || 0 }} 首
                    <span class="text-[var(--ys-text-faint)]">（其余格式本应用写不了，恢复时会跳过）</span>
                  </p>
                  <!--
                    P3-13（2026-10-27）：快照以前只能在页面里看，没法带走。
                    这份 diff 是「哪几首会被改回什么」的完整依据，用户要拿它去
                    别的工具里比对、或者出事时当证据，都得先能导出。
                  -->
                  <button
                    v-if="snapDetail.id"
                    class="ys-btn ys-btn-sm ys-btn-ghost"
                    :title="'把这份快照的差异导出成 JSON 文件'"
                    @click="exportSnapDetail(sn)"
                  >导出 JSON</button>
                  <button
                    class="ys-btn ys-btn-sm ys-btn-ghost"
                    @click="closeSnapDetail"
                  >关闭</button>
                </div>
                <p v-if="snapDetailLoading" class="mt-1 text-[11px] text-[var(--ys-text-faint)]">正在读取差异…</p>
                <p v-else-if="snapErrorText" class="mt-1 text-[11px] text-rose-600">{{ snapErrorText }}</p>
                <p v-else-if="!(snapDetail.diff || []).length" class="mt-1 text-[11px] text-emerald-700">
                  这份快照和现在的曲库一致 —— 没有需要恢复的东西
                </p>
                <template v-else>
                  <p v-if="snapDetail.diff_truncated" class="mt-1 text-[11px] leading-snug text-amber-700">
                    差异共 {{ snapDetail.diff_total }} 条，下表只列前 {{ snapDetail.diff_cap || snapDetail.diff.length }} 条
                    —— 底下还有 {{ snapDetail.diff_total - snapDetail.diff.length }} 条没显示。
                  </p>
                  <div class="mt-1.5 max-h-64 overflow-auto rounded-[8px] border border-[var(--ys-border)]">
                    <table class="w-full min-w-[560px] text-[11px]">
                      <thead class="sticky top-0 z-10 bg-[var(--ys-bg-soft)] text-[11px] text-[var(--ys-text-faint)]">
                        <tr>
                          <th class="px-2 py-1.5 text-left font-medium">文件</th>
                          <th class="px-2 py-1.5 text-left font-medium">快照里的歌名</th>
                          <th class="px-2 py-1.5 text-left font-medium">现在的歌名</th>
                          <th class="px-2 py-1.5 text-left font-medium">变了哪些字段</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-for="e in snapDetail.diff" :key="e.path" class="border-t border-[var(--ys-border)]">
                          <td class="max-w-[220px] truncate px-2 py-1 font-mono text-[var(--ys-text-muted)]" :title="e.path">{{ baseName(e.path) }}</td>
                          <td class="ys-cell max-w-[150px] px-2 py-1 text-[var(--ys-text)]" :title="e.snap_title || '—'">{{ e.snap_title || '—' }}</td>
                          <td class="ys-cell max-w-[150px] px-2 py-1" :class="e.change === 'missing' ? 'text-amber-600' : 'text-rose-600'">
                            {{ e.change === 'missing' ? '文件已不在' : (e.now_title || '—') }}
                          </td>
                          <td class="px-2 py-1 text-[10.5px] text-[var(--ys-text-faint)]">{{ (e.fields || []).join(' / ') || '—' }}</td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                </template>
              </div>

              <!--
                恢复确认条（2026-10-27 修 3.5）：原来是 window.confirm 一句「把 N 首恢复回去？」，
                既看不见差异、在 iOS PWA 里也丑。现在就地摊开数字，第二次点才真写。
              -->
              <div
                v-if="isRestoreArmed(sn)"
                class="rounded-[8px] border border-rose-200 bg-rose-50/60 p-2.5"
              >
                <p class="text-[11px] font-semibold text-rose-800">
                  确认把元数据恢复成「{{ sn.at_text || sn.id }}」这一刻的样子？
                </p>
                <p class="mt-1 text-[11px] leading-snug text-rose-900/80">
                  <template v-if="snapDetailLoading || !snapRestoreSummary(sn)">
                    正在读取这份快照和现在的差异…
                  </template>
                  <template v-else-if="snapRestoreSummary(sn).none">
                    这份快照和现在的曲库一致 —— 恢复不会改动任何文件。
                  </template>
                  <template v-else>
                    会改 <b class="font-mono">{{ snapRestoreSummary(sn).total }}</b> 首<template
                      v-if="snapRestoreSummary(sn).truncated">（差异超过 {{ snapRestoreSummary(sn).listed }} 条，上面只列了前几条）</template>；
                    <template v-if="snapRestoreSummary(sn).missing">其中 <b class="font-mono">{{ snapRestoreSummary(sn).missing }}</b> 首文件已经不在，会跳过；</template>
                    <template v-if="snapRestoreSummary(sn).skipped">共 <b class="font-mono">{{ snapRestoreSummary(sn).skipped }}</b> 首写不了标签（格式不支持），也会跳过；</template>
                    其余按快照里的标签写回。恢复前会自动再拍一份当前状态的快照，点错了还能撤回来。
                  </template>
                </p>
                <div class="mt-2 flex flex-wrap items-center gap-1.5">
                  <button
                    :disabled="snapBusy"
                    class="ys-btn ys-btn-sm ys-btn-danger ys-press"
                    @click="restoreSnapshot(sn)"
                  >{{ snapBusy ? '恢复中…' : '确认恢复' }}</button>
                  <button
                    :disabled="snapBusy"
                    class="rounded-full border border-[var(--ys-border)] bg-white px-3 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
                    @click="cancelRestoreSnapshot"
                  >取消</button>
                  <button
                    v-if="!isSnapOpen(sn)"
                    class="rounded-full px-2 py-1 text-[11px] text-[var(--ys-text-muted)] underline decoration-dotted underline-offset-2 hover:text-[var(--ys-text)]"
                    @click="viewSnapshot(sn)"
                  >先看差异明细</button>
                </div>
              </div>
            </template>
          </div>
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
            class="ys-btn ys-btn-primary ys-press"
            :disabled="qRunning || qBusy"
            :title="qScanBtnTitle"
            @click="startQualityScan"
          >{{ qScanBtnText }}</button>
          <button
            v-if="qRunning"
            type="button"
            class="ys-btn ys-btn-sm ys-press shrink-0"
            :disabled="qStopBusy"
            title="停止扫描（会真的让后端停下，已探到的结果保留）"
            @click="stopQualityScan"
          >
            <Square class="h-3 w-3" :stroke-width="2" />
            {{ qStopBusy ? '停止中…' : '停止' }}
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
          <div class="ys-progress mt-1.5 w-full">
            <i :style="{ width: qPercent + '%' }"></i>
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

        <!--
          筛选条（2026-10-27）：一库几万首时，扫描结果里绝大多数是「已经是最高音质」
          的行，用户真正要处理的那几十条被淹在里面。默认不过滤（保持原样），
          勾一下就只看能替换的。
        -->
        <div v-if="qRowsAll.length" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-[var(--ys-text-muted)]">
          <label class="flex min-h-8 cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              class="h-3.5 w-3.5 accent-[var(--ys-accent)]"
              :checked="qOnlyUpgradeable"
              @change="setQOnlyUpgradeable($event.target.checked)"
            />
            只看能替换的
          </label>
          <span v-if="qHiddenRowCount" class="text-[var(--ys-text-faint)]">已隐藏 {{ qHiddenRowCount }} 条无需替换</span>
          <span class="ml-auto text-[10.5px] text-[var(--ys-text-faint)]">本次共 {{ qRowsAll.length }} 条</span>
        </div>

        <!-- 结果表：列表 + 发丝线（不做卡片套卡片）。只有可替换且未替换的行才能勾选 -->
        <div v-if="qRows.length" class="mt-2 overflow-x-auto overscroll-x-contain rounded-[12px] border border-[var(--ys-border)] bg-white">
          <div class="overflow-y-auto" :style="{ maxHeight: '440px' }">
            <div
              v-for="e in qRows"
              :key="e.path"
              class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-[var(--ys-border)] px-3 py-2.5 last:border-b-0"
              :class="e.replaced ? 'bg-[var(--ys-surface-2)]' : ''"
            >
              <!-- 8.5：原来是裸 16px 的 input —— 手指点不中。用 label 撑成 32×32 热区，视觉尺寸不变 -->
              <label
                class="flex h-8 w-8 shrink-0 items-center justify-center"
                :class="(!e.upgradeable || e.replaced) ? 'cursor-not-allowed' : 'cursor-pointer'"
                :title="e.replaced ? '已替换' : (e.upgradeable ? '勾选后可用最高音质替换' : '已是各渠道最高音质，无需替换')"
              >
                <input
                  type="checkbox"
                  class="h-4 w-4 accent-[var(--ys-accent)]"
                  :checked="qSelected.has(e.path)"
                  :disabled="!e.upgradeable || e.replaced"
                  @change="toggleQSelect(e)"
                />
              </label>
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
        <!-- 被筛选筛空的要说清是「筛选」而不是「没得换」，否则用户会以为白扫了 -->
        <div v-else-if="qRowsAll.length && qOnlyUpgradeable" class="ys-empty mt-2">
          当前筛选下没有可替换的曲目 —— 取消「只看能替换的」可以看到全部 {{ qRowsAll.length }} 条
        </div>
        <div v-else-if="qState === 'done'" class="ys-empty mt-3">
          曲库里的歌已经是各渠道能拿到的最高音质了
        </div>
        <div v-else-if="qState === 'idle' && !qRunning && !qErrorText" class="ys-empty mt-3">
          还没有扫描结果 —— 点「开始扫描」，让洛雪 / musicdl / 原生三条渠道逐条对比
        </div>

        <!--
          替换 / 下载进度条（2026-10-24 用户原话：「用户选择歌曲并替换后，在下方增加正在
          替换或者正在下载的进度条」）。
          · 只在真有批次在跑时出现，收尾立刻收起，没进行中时不占位；
          · 与「扫描中」那条同一个 Quiet Glass 形状（1.5px 细条 + 圆角），
            青玉色 #0B8A66 只用在进度本身，文字统一 11px 次要灰；
          · 文案说清「正在下载并替换 i / total」，并带上这一批在换的那首歌。
        -->
        <div v-if="qReplacing" class="mt-2.5">
          <div class="flex items-center justify-between gap-2 text-[11px] text-[var(--ys-text-muted)]">
            <span class="min-w-0 truncate">
              正在下载并替换
              <b class="ys-num text-[var(--ys-text)]">{{ qReplaceStep }}</b> /
              <b class="ys-num text-[var(--ys-text)]">{{ qReplaceTotal }}</b> 条
              <span v-if="qReplaceCurrent" class="text-[var(--ys-text-faint)]"> · {{ qReplaceCurrent }}</span>
            </span>
            <span class="ys-num shrink-0 text-[var(--ys-text-faint)]">{{ qReplacePercent }}%</span>
          </div>
          <div class="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-black/[0.06]">
            <div
              class="h-full rounded-full bg-[#0B8A66] transition-all duration-300"
              :style="{ width: qReplacePercent + '%' }"
            ></div>
          </div>
        </div>

        <!--
          没换成的那些：给一个「重试」入口（2026-10-27）。
          以前失败行只是留在表格里带一行红字，想把它们再试一遍得自己一条条回勾；
          而且失败集合在收尾时会从勾选里移除（避免用户以为已经处理过），
          所以这里必须有个地方把它们接住，否则那批勾选就悄悄丢了。
        -->
        <div
          v-if="!qReplacing && qRetryPaths.length"
          class="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-[12px] border border-[var(--ys-border)] bg-[var(--ys-bg-soft)] px-3 py-2"
        >
          <span class="min-w-0 flex-1 text-[11px] leading-snug text-[var(--ys-text-muted)]">
            有 <b class="ys-num text-[var(--ys-text)]">{{ qRetryPaths.length }}</b> 条这轮没换成（原因写在下表对应行）
          </span>
          <button
            type="button"
            class="shrink-0 rounded-full border border-[var(--ys-border)] bg-white px-2.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition hover:text-[var(--ys-text)] disabled:opacity-50"
            :disabled="qBusy"
            @click="retryFailedReplace"
          >
            重试这些
          </button>
          <button
            type="button"
            class="shrink-0 rounded-full px-2 py-1 text-[11px] text-[var(--ys-text-faint)] transition hover:text-[var(--ys-text-muted)]"
            @click="qRetryPaths = []"
          >
            忽略
          </button>
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
                    title="后台音质补强轮自动试的（节奏在「设置 → 后台音质补强」里调，失败会隔天再试）—— 不是你手动点的替换"
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

    <!--
      ── 补全选项弹窗：目录 / 最多几首 / 覆盖 / AI / 主力平台 ──
      2026-10-25 用户：「选项按钮抽屉修改为弹窗。」
      原来这里是右侧抽屉（SideSheet）；改成居中弹窗后，改完直接点「完成」关掉，
      不用先滑回去再看一眼页面。
    -->
    <Teleport to="body">
      <div
        v-if="optionsOpen"
        class="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px]"
        @click.self="optionsOpen = false"
      >
        <div class="max-h-[85vh] w-full max-w-md overflow-y-auto overscroll-contain rounded-2xl border border-[var(--ys-border)] bg-white p-5 pb-[max(1.25rem,env(safe-area-inset-bottom))] shadow-2xl">
          <div class="mb-4 flex items-start justify-between gap-3">
            <div>
              <h3 class="text-[13px] font-semibold text-[var(--ys-text)]">补全选项</h3>
              <p class="mt-0.5 text-[11px] text-[var(--ys-text-faint)]">默认值就是推荐值；改完只影响下一次补全</p>
            </div>
            <button
              type="button"
              class="shrink-0 rounded-full p-1 text-[var(--ys-text-faint)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
              title="关闭"
              @click="optionsOpen = false"
            >✕</button>
          </div>
          <div class="space-y-5">
        <!--
          2026-10-25 用户：「移除补全选项里的曲库目录组件。」
          这个输入框已删 —— 目录统一由分段导航行右边的 TidyDirChip 选（点开是目录树），
          同一个值写在 prefs.nas_dir，不再允许两个地方各改一份。
        -->

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
        </div>
      </div>
    </Teleport>
  </PageShell>
</template>

<script setup>
// ── 补全整理（TidyView）─────────────────────────────────────────────
// 2026-10-15 框架重做：从 LibraryManager.vue 的 complete tab 原样搬出。
// 逻辑零改动，只有两处刻意变化：
//   1. 页面级 flash（message/error ref + 底部 InlineNotice）→ 全局 feedback 通知；
//   2. 补全卡片补上了 coverUrl（旧代码 cardCover 定义了却从未被调用，
//      模板里 `:src="c.coverUrl"` 一直是 undefined —— 卡片封面其实从来没显示过）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  Sparkles, Check, SlidersHorizontal, Info, Loader2, AlertTriangle, X,
  ScanSearch, Copy, History, Gauge, ImageIcon, Replace, Square, FileAudio,
} from 'lucide-vue-next'
import PageShell from './PageShell.vue'
import SectionCard from './SectionCard.vue'
import InlineNotice from './InlineNotice.vue'
// SideSheet 已不再使用（2026-10-25 补全选项改成弹窗）；OverflowMenu 也不再使用
// （2026-10-27：「⋯」菜单早已拆掉，入口分别并进卡片内按钮与各分段）—— 两个 import 都已删，
// 避免「改这里的菜单却不生效」这种死引用。
import StatLine from './StatLine.vue'
import SegmentedControl from './SegmentedControl.vue'
import SelectionBar from './SelectionBar.vue'
import LibraryHealthCard from './LibraryHealthCard.vue'
import NameRulesPanel from './NameRulesPanel.vue'
import { useLyricGaps } from '../services/useLyricGaps'
import SelectField from './SelectField.vue'
import { TidyAPI, TagsAPI, NasAPI, ConfigAPI, QualityAPI, DEFAULT_AUTO_DOWNLOAD_CHANNELS } from '../api/client'
import { loadPref, savePref, loadBool, loadCache, saveCache } from '../services/prefs'
import {
  FIELD_DEFS, DEFAULT_FIELDS, parseFields, toggleFieldKey, platformName, planCells,
  summaryCells as _summaryCells, summaryBar, cardModel, cardFields, mapWithLimit,
} from '../services/libraryFields'
// 这一页「看法」上的规则（相对时间 / 缺口文案 / 补全四态 / 下一步提示）都在服务层，
// 由 libraryView.test.mjs 钉住 —— 组件里只做 ref → 函数参数的桥接，不本地重写。
// 注：COMPLETE_STEPS 已不再 import —— 它对应的流程条从来没被渲染过（见下面 completeSteps 的删除说明）。
import {
  lastScanText as _lastScanText, gapsAtText as _gapsAtText,
  unsupportedExtsText as _unsupportedExtsText,
  completePercent as _completePercent, aiMatchedCount as _aiMatchedCount,
  completeDoneText, completeTitleText as _completeTitleText,
  completeStep as _completeStep, completeHint as _completeHint,
  fmtWhen, relWhen,
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
/**
 * 2026-10-25 用户：「移除补全选项里的曲库目录组件，路径展示迁移到分段导航行右边。」
 * 曲库目录现在由 TidyDirChip（挂在分段导航行右侧）统一选，值仍落在 prefs 的 nas_dir。
 * tidyDir 保留给「本地曲库页传进来的目录」这条老链路，平时为空 ⇒ 一律用 nas_dir。
 */
const nasDir = ref(loadPref('nas_dir', ''))
const tidyDir = ref('')
/**
 * ⚠️ 目录的唯一真相源（2026-10-27 修 2.2「双真相源」）。
 *
 * 起因：TidyDirChip 把目录写进 prefs.nas_dir，而组件里到处写 `tidyDir.value || nasDir.value`，
 * 两个 ref 谁赢取决于兜底顺序 —— 任何一处直接写 tidyDir 就会盖过 nasDir，两份真相互相打架，
 * 而且代码里没有任何地方声明「tidyDir 只做兜底、永远是空串」这个契约。
 *
 * 现在：**所有 handler 一律读 curDir**，tidyDir 只剩「本地曲库页传进来的目录」这条老链路的兼容
 * 入口（onMounted 里读一次 prefs 兼容旧值、离开本页时清空），不再参与业务判断。
 */
const curDir = computed(() => (tidyDir.value || nasDir.value || '').trim())
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
let onDirChanged = null
// 3.10：刷新 guard 的句柄
let onBeforeUnload = null
// 2026-10-19 渐进式披露 → 2026-10-21 分段导航 → 2026-10-27 收起「⋯」：
// 主功能入口升为顶部分段，次要动作也各归其位（增量扫描并进卡内「扫描」按钮、
// 选项 = 卡片头那颗按钮），页面上已经**没有**溢出菜单了。
const optionsOpen = ref(false)
const auditing = ref(false)

/**
 * 当前分段：壳层（App.vue）的顶部分段导航把选择写进 navBus，这里只读。
 * 五个段共用本组件的同一批 ref —— 切段只是 v-show 显隐，
 * 跑完的体检 / 查重 / 补全结果不会因为切走而被卸载丢掉。
 */
const sub = computed(() => segmentOf('tidy'))

// ⚠️ 2026-10-27 删除：`completeMenu`（计算属性，只有一项「增量扫描」）+
// `onCompleteMenu`（分发这个 key）+ `OverflowMenu` import。三者全是死代码 ——
// 模板里从来没有渲染过 OverflowMenu，「增量扫描」早已并进补全段的「扫描」按钮
// （见 scanAll）。留着会让人以为页面上真有一个「⋯ 菜单」可以改。

// ── 分段五：替换音质（需求 m02757/m02848）──
//
// 接后端 /api/quality/scan|replace（pkg/intercept/quality.go，契约见 api/client.js 的 QualityAPI 注释）。
// 几个刻意的口径，改之前先读：
//  · scan 是**异步任务**：start 只是启动，进度靠轮询 status（2s 一次，对齐补全轮询）。
//    视图 v-show 常驻，切走再切回状态不丢；
//  · 「停止」只停本页轮询 —— 后端并不知道，状态文案必须诚实「已中断」，不许假装任务停了；
//  · 只有 upgradeable 的行可勾选；替换一次最多 50 条，超出前端分批顺序发（这里一批只发 5 条，
//    见 Q_REPLACE_BATCH —— 50 条一批要等好几分钟，进度条根本看不出在动）；
//  · 替换中在结果表下方给一条细进度条；收尾**只**增量刷新「最近替换」记录、把成功的行移出
//    扫描队列（不整表重扫 —— 用户的滚动位置/勾选视角保持连续），失败把后端 error 写回该行。
const Q_CHANNEL_LABELS = { lx: '洛雪', dl: 'musicdl', native: '原生' }
const qMode = ref('lossy')
const qLimit = ref(clampQLimit(Number(loadPref('quality_limit', 200))))
const qScan = ref(null)      // 最近一次 scan 返回的 data（state/total/scanned/entries…）
const qStopped = ref(false)  // true = 已点过「停止」；2026-10-27 起这个动作会真的停后端
const qStopBusy = ref(false) // 「停止」请求在途（防连点 + 按钮禁用）
const qErrorText = ref('')   // 请求层失败（接口没上线/断网）的横幅，别白屏也别抛错
const qSelected = ref(new Set())
const qBusy = ref(false)     // 替换批次进行中
let qTimer = null
// 替换 / 下载进度（2026-10-24 用户原话：「用户选择歌曲并替换后，在下方增加正在替换或者
// 正在下载的进度条」）：
//  - qReplacing 只在真有批次在跑时为 true，收尾立刻收起，没进行中时不占位；
//  - qReplaceDone 是**已经回来**的条数，qReplaceInFlight 是这一批刚发出去、还没回来的条数；
//    进度条的分子是两者之和（见 qReplaceStep），否则一批没回来之前条子一动不动。
const qReplacing = ref(false)
const qReplaceTotal = ref(0)
const qReplaceDone = ref(0)
const qReplaceInFlight = ref(0)
const qReplaceCurrent = ref('')  // 这一批在换的那首歌（取该批第一条的歌名 / 文件名）
// 上一轮没换成的 path（2026-10-27）：收尾时摆成「重试这些」的入口。
// 以前失败的行只是留在表格里带 rowError，用户想再试一次得自己一条条把勾找回来。
const qRetryPaths = ref([])

// 一批只发 5 条：契约上限是 50（见 api/client.js 的 QualityAPI.replace 注释），
// 但 50 条一批要等好几分钟才回来一次 —— 用户要的是「看得见在动」的进度，
// 所以这里小批串行：每十几秒推进一次，也不会把后端的外网请求打满。
const Q_REPLACE_BATCH = 5

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
// 替换进度条的分子 = 已回来的条数 + 这一批刚发出去还在飞的条数。
// 只算 done 的话，一批（5 条，走一次外网）没回来之前条子就是死的 —— 用户会觉得卡住了；
// 把在飞的一批算进来，批一发出去进度就前进一格（2026-10-24：「进度条要真的动」）。
const qReplaceStep = computed(() => Math.min(qReplaceTotal.value, qReplaceDone.value + qReplaceInFlight.value))
const qReplacePercent = computed(() => {
  const total = qReplaceTotal.value
  if (!total) return 0
  const pct = Math.round((qReplaceStep.value * 100) / total)
  // 替换中最多画到 99%：最后一批是「发出去就算进进度」的，若直接给到 100%，
  // 用户会看到「100% 但还在等」—— 收尾一旦完成这条就整体收起，不需要 100% 这个态。
  return qReplacing.value ? Math.min(99, pct) : Math.min(100, pct)
})
const qSelectedCount = computed(() => qSelected.value.size)
// 3.10：还没落盘的勾选总数（歌名修复 / 缺歌词 / 替换音质三段），>0 时离开页面会拦一下。
// 只统计「选中且还没写」的，已写完的不算未保存。
const pendingSelectionCount = computed(() =>
  titleFixSelectedCount.value + lyricSelectedCount.value + qSelectedCount.value)
// 「只看能替换的」筛选（2026-10-27）：一库几万首时表格里绝大多数是
// 「已经是最高音质、没事可做」的行，用户真正要处理的那几十条被淹在里面。
const qOnlyUpgradeable = ref(false)
function setQOnlyUpgradeable(v) {
  if (v === qOnlyUpgradeable.value) return
  qOnlyUpgradeable.value = v
}
const qRowsAll = computed(() => {
  const es = qScan.value?.entries || []
  // 扫完且一条都不可替换（也还没替换过任何东西）→ 让位给空态文案，不刷一屏灰勾
  if (qScan.value?.state === 'done' && !es.some((e) => e.upgradeable || e.replaced)) return []
  return es
})
const qRows = computed(() => {
  if (!qOnlyUpgradeable.value) return qRowsAll.value
  // 失败的行（rowError）一定留着 —— 被筛掉的话用户根本找不到失败原因
  return qRowsAll.value.filter((e) => e.upgradeable || e.rowError)
})
/** 被筛选隐藏掉的行数（提示用，免得用户以为扫描结果少了） */
const qHiddenRowCount = computed(() => (qOnlyUpgradeable.value ? qRowsAll.value.length - qRows.value.length : 0))
/**
 * 音质扫描按钮的文案（六-2）。
 *
 * 原来只有「开始扫描 / 扫描中…」两态，但后端 `StartQualityScan`
 * （backend/pkg/intercept/quality.go:266）**每次都从头扫**，不清空旧结果就直接覆盖：
 * 已经扫出 30 条可替换、用户想再扫一遍，点下去会把那 30 条连同用户勾好的选择一起冲掉。
 * 所以按「有没有旧结果」分成两种文案，并在 title 里写清会丢什么。
 */
const qScanBtnText = computed(() => {
  if (qRunning.value) return '扫描中…'
  if (qBusy.value) return '提交中…'
  const es = qScan.value?.entries || []
  return es.length ? '重新扫描' : '开始扫描'
})
const qScanBtnTitle = computed(() => {
  if (qRunning.value) return '扫描进行中 —— 用右边的停止按钮可以真的让后端停下'
  const es = qScan.value?.entries || []
  if (!es.length) return '让三条渠道逐条对比曲库能拿到的最高音质'
  return `会从头上重新扫一遍，现在这 ${es.length} 条结果和已勾的选择都会被清掉；只想保留现状就不要再点`
})

const qStatusText = computed(() => {
  const parts = []
  if (qStopped.value && qState.value !== 'running') {
    // 2026-10-27：停止现在会真的通知后端（见 stopQualityScan），文案跟着改诚实
    parts.push('已停止 —— 后端也停了，已探到的结果保留着，可以先把这批换掉，之后再重新扫')
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

// P2-10：原来这里手写 pad 出「10-27 18:39」（没有年份，跨年就歧义），改用统一的 fmtWhen
const formatRecordTime = fmtWhen

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
      // 替换进行中不拿 status 覆盖表格：旧扫描快照会把刚移出队列的行放回来，
      // 用户会看到「已经换完的歌又出现在待替换列表里」（2026-10-24 改）
      if (qBusy.value) return
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

/**
 * 停止扫描（2.4 / 六-2，2026-10-27 修）。
 *
 * 原来这里**只停本地轮询**，标题还诚实地写着「后端扫描可能仍在继续」——
 * 于是用户点了停止、以为停了，后端其实还在逐首打三条渠道（一次几百条能跑好几分钟），
 * 想取消就只能等它扫完。后端 /api/quality/scan 早就支持 `action:"stop"`
 * （intercept/quality.go:328 StopQualityScan，会 close(stop) 取消在途探测，
 * 已探到的结果保留），所以这里补上真正的取消。
 */
async function stopQualityScan() {
  stopQualityPolling()
  qStopped.value = true
  qStopBusy.value = true
  try {
    const res = await QualityAPI.scan('stop')
    // 后端回的是停止后的状态（已探到的保留在 entries 里）——直接拿来刷新界面
    if (res?.data) qScan.value = res.data
    flash('已停止扫描，已探到的结果保留着')
  } catch (e) {
    // 停不下来也要说清楚，别让用户以为停了
    flash(failText('停止扫描', e) + ' —— 后端可能还在扫，等一会儿再看进度', true)
  } finally {
    qStopBusy.value = false
  }
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

async function replaceWithBest(pathsArg) {
  if (qBusy.value || qReplacing.value) return
  const paths = Array.isArray(pathsArg) && pathsArg.length ? [...pathsArg] : [...qSelected.value]
  if (!paths.length) return
  qBusy.value = true
  qErrorText.value = ''
  let doneCount = 0
  let failCount = 0
  // 成功的 path 收在这里：收尾时一次性从扫描队列里移除
  // （用户 2026-10-24 原话：「移除已完成歌曲的扫描队列」）。
  const donePaths = new Set()
  // 失败的 path 收在这里：收尾时摆成「重试这些」的入口（2026-10-27 加）。
  const failedPaths = new Set()
  const batchErrors = []
  qReplaceTotal.value = paths.length
  qReplaceDone.value = 0
  qReplaceInFlight.value = 0
  qReplaceCurrent.value = ''
  qReplacing.value = true
  qRetryPaths.value = []
  try {
    // 契约：一次最多 50 条 —— 这里刻意只发 5 条一批（见 Q_REPLACE_BATCH），顺序发、一批一落。
    for (let i = 0; i < paths.length; i += Q_REPLACE_BATCH) {
      const batch = paths.slice(i, i + Q_REPLACE_BATCH)
      // 批一发出去就把「在等哪首」摆到进度条上：5 条也要各走一次外网，用户得知道进度在哪
      qReplaceCurrent.value = qBatchLabel(batch[0])
      qReplaceInFlight.value = batch.length
      // ⚠️ try 必须在**循环内**（2026-10-27 修）。
      // 原来 catch 在循环外：第 3 批发一次抛错（断网 / 超时），第 4 批到第 N 批根本不再发，
      // 用户看到进度条停在原地、后面几十首一动不动，而那些勾选还留在条上以为在排队。
      try {
        const res = await QualityAPI.replace(batch)
        if (res.code !== 200) {
          // 整批没被受理（不是单条失败）：这一批全算失败，但**继续发后面的批次**
          const msg = apiError(res, '替换音质')
          batchErrors.push(msg)
          qErrorText.value = msg
          for (const p of batch) failedPaths.add(p)
          failCount += batch.length
          qReplaceDone.value += batch.length
          qReplaceInFlight.value = 0
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
        // 后端偶尔会少回几条（既不在 done 也不在 failed）—— 那些不能默默消失，
        // 否则「勾了 5 条，回来只有 3 条的结果」，用户无从判断另外 2 条怎么了。
        for (const p of batch) {
          if (!doneMap.has(p) && !failMap.has(p)) failMap.set(p, '后端没有返回这条的结果，可能没换成')
        }
        doneCount += doneMap.size
        failCount += failMap.size
        for (const path of doneMap.keys()) donePaths.add(path)
        for (const path of failMap.keys()) failedPaths.add(path)
        applyQReplace(doneMap, failMap)
        // 这批回来了：把在飞数换成真实完成数（进度条分子不变，但自此算「已落盘」）
        qReplaceDone.value += batch.length
        qReplaceInFlight.value = 0
      } catch (e) {
        // 单批抛错：只记这一批失败，后面的批次继续发（这才是用户要的「别卡死」）
        const msg = failText('替换音质', e)
        batchErrors.push(msg)
        for (const p of batch) failedPaths.add(p)
        failCount += batch.length
        qReplaceDone.value += batch.length
        qReplaceInFlight.value = 0
      }
    }
    if (doneCount) flash(`已用最高音质替换 ${doneCount} 首`)
    if (failCount) flash(`${failCount} 条没换成${batchErrors.length ? `：${batchErrors[0]}` : '，原因写在对应行'}`, true)
  } finally {
    // 收尾（2026-10-24 用户原话：「替换完成后，自动更新当前页面的最近记录，
    // 移除已完成歌曲的扫描队列」）：
    // 1) 只取一次 status 把 records 换掉 —— 不整表重扫，表格/滚动位置/其他字段都保持原样，
    //    这样刚换完的那几首立刻出现在「最近替换」里；
    // 2) 成功的行从扫描队列里移除（不再标 replaced:true 留在表里），失败行留着带 rowError；
    // 3) 失败的行不静默留在勾选里 —— 它们进 qRetryPaths，用户点一下就能重试
    //    （原地不动的勾选会让人以为「已经处理过了」）。
    await refreshQualityRecordsOnly()
    dropReplacedFromQueue(donePaths)
    qRetryPaths.value = [...failedPaths]
    if (failedPaths.size) {
      const sel = new Set(qSelected.value)
      for (const p of failedPaths) sel.delete(p)
      qSelected.value = sel
    }
    qReplaceInFlight.value = 0
    qReplacing.value = false
    qBusy.value = false
  }
}

/** 重试上一轮没换成的那些（2026-10-27）：把失败集合塞回勾选再跑一遍。 */
function retryFailedReplace() {
  const paths = qRetryPaths.value
  if (!paths.length || qBusy.value || qReplacing.value) return
  replaceWithBest(paths)
}

// 进度条上「当前这首」的可读名：表里能查到时用歌名，查不到就退回文件名。
function qBatchLabel(path) {
  const hit = (qScan.value?.entries || []).find((e) => e.path === path)
  return (hit && hit.title) || baseName(path) || ''
}

/**
 * 只刷新「最近替换」记录，其余字段一概不动（2026-10-24 需求）。
 *
 * 为什么不是直接再调一次 loadQualityStatus()：那会把 entries / state / scanned /
 * upgradeable 全量覆盖 —— 用户刚替换完，表格正被他自己看着，整表重扫一次等于把
 * 滚动位置和失败行都洗掉。这里只把 `records` 写回去，刚替换完的记录就会出现在
 * 「最近替换」里，其余字段保持调用前的样子。
 */
async function refreshQualityRecordsOnly() {
  try {
    const res = await QualityAPI.scan('status', qMode.value, clampQLimit(qLimit.value))
    if (res.code !== 200 || !res.data) return
    const records = Array.isArray(res.data.records) ? res.data.records : null
    if (!records) return
    if (qScan.value) qScan.value.records = records
    else qScan.value = normalizeScan(res.data) // 极端情况（表都没了）才整份采纳
  } catch (_) {
    // 记录刷不出来不影响「替换成功」这个结论：flash 已经报过条数了，别叠第二块错误
  }
}

/**
 * 把成功的行移出扫描队列（2026-10-24 用户原话：「移除已完成歌曲的扫描队列」）。
 *
 * 换过的歌已经没有「可替换」可言 —— 留着只会让用户以为还没换。
 * 同步把 upgradeable 减掉真正移除的条数（不能减成负数），勾选集合也剔除掉这些 path。
 */
function dropReplacedFromQueue(donePaths) {
  const j = qScan.value
  if (!j || !donePaths.size) return
  const before = (j.entries || []).length
  j.entries = (j.entries || []).filter((x) => !donePaths.has(x.path))
  const removed = before - j.entries.length
  if (removed > 0) j.upgradeable = Math.max(0, (Number(j.upgradeable) || 0) - removed)
  if (qSelected.value.size) {
    const sel = new Set(qSelected.value)
    for (const path of donePaths) sel.delete(path)
    qSelected.value = sel
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
// 查重：真执行前的「已看过干跑清单」闸门 + 正在跑 + 是否展开全部组（2026-10-27 P0-4/3.7）
const dupBusy = ref(false)
const dupConfirmArmed = ref(false)
const dupShowAll = ref(false)
// 9.2：回收站的绝对路径（后端 joinTrash：曲库根下的 .tidy_trash）。
// 以前页面只说「曲库里的 .tidy_trash」，用户误删后不知道去哪儿捞。
const trashPath = computed(() => {
  const d = (curDir.value || '').replace(/\/+$/, '')
  return d ? `${d}/.tidy_trash` : ''
})
/**
 * 复制到剪贴板（9.2）。
 * ⚠️ 本应用是 http + 局域网 IP 部署 → 不是安全上下文，`navigator.clipboard`
 * 直接是 undefined（和 ImmersivePlayer.vue:1156 同一个坑）。所以要判存在 + 退回 execCommand。
 */
async function copyText(text, okMsg) {
  const done = () => flash(okMsg || '已复制')
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      done()
      return
    } catch (_) { /* 落到下面的兜底 */ }
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, ta.value.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) done()
    else flash('复制失败 —— 浏览器没给剪贴板权限，手动选中复制吧', true)
  } catch (e) {
    flash(failText('复制', e), true)
  }
}
function copyTrashPath() {
  if (!trashPath.value) return
  copyText(trashPath.value, '回收站路径已复制')
}
const visibleDupGroups = computed(() => {
  const gs = (dupResult.value && dupResult.value.groups) || []
  return dupShowAll.value ? gs : gs.slice(0, 30)
})
// 查重 / 索引扫描结果的落盘时间（0 = 本次刚跑的）；2026-10-25 加，见 restoreDedupe
const dedupeAt = ref(0)
const indexScanAt = ref(0)
// 上次整理快照（来自持久化索引 library_index.json）
const indexStats = ref(null)
const statsLoading = ref(false)
// 读索引统计失败的原因（2026-10-27 修 9.3：原来静默吞错，页面会说成「还没整理过」）
const statsError = ref('')
const reloading = ref(false)

// ── 曲库体检结果的持久化 ──
// 用 `prefs` 的 saveCache/loadCache：本地 localStorage + 后端双写，跟着设备走。
// **按目录分开存** —— 换了曲库目录不该看到别的目录的体检结果。
function auditCacheKey(dir) {
  return `last_audit:${dir || ''}`
}

function restoreAudit() {
  const dir = curDir.value
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
watch(curDir, restoreAudit)

/** 体检结果的时间戳文案（只用于「上次体检结果」这行提示） */
const auditAtText = computed(() => (auditAt.value ? fmtWhen(auditAt.value) : ''))

async function runAudit() {
  const dir = curDir.value
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

// ── 数据快照（原「记录」段）──
const snapLoading = ref(false)
const snapBusy = ref(false)
const snapshots = ref([])
// 待确认恢复的那一份（2026-10-27 修 3.5：恢复是两步确认，不再走 window.confirm）
const snapRestorePending = ref(null)
const snapDetail = ref(null)
const snapDetailLoading = ref(false)
const snapErrorText = ref('')

function fmtBytes(n) {
  const v = Number(n) || 0
  if (v < 1024) return `${v} B`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/** 快照来源的显示名（后端 kind：manual / scheduled / pre-restore / auto） */
const SNAP_KIND_TEXT = {
  manual: '手动快照',
  scheduled: '定时快照',
  'pre-restore': '恢复前留底',
  auto: '写入前留底',
}

/**
 * ISO 时间 → 「2026-10-27 09:12」。
 * P2-10（2026-10-27）：原来这里自己 new Date + toLocaleString，和 services 里的
 * `fmtWhen` 是两份实现，输出还不一样（这个只到分钟，那个到秒）。现在只留一个。
 */
const fmtTimeText = fmtWhen

/**
 * 快照列表的缓存键。快照是**曲库级**的（不分目录），所以键不带目录 ——
 * 与体检/缺口/卡片那三份「按目录分开存」不同，别混。
 */
function snapshotCacheKey() {
  return 'last_snapshots'
}

/** 上次读到的快照列表与定时设置（刷新页面 / 切段回来都还在） */
function restoreSnapshots() {
  const cached = loadCache(snapshotCacheKey(), 0)
  if (!cached || typeof cached !== 'object') return
  if (Array.isArray(cached.snapshots)) snapshots.value = cached.snapshots
  if (cached.schedule && typeof cached.schedule === 'object') {
    schedule.value = { ...schedule.value, ...cached.schedule }
  }
}

async function loadSnapshots() {
  snapLoading.value = true
  try {
    // 2026-10-27 快照改成「曲库元数据快照」：只存标签，不复制音频（见后端 meta_snapshot.go）
    const [res, sc] = await Promise.all([
      TidyAPI.metaSnapshots(),
      // 2026-10-27 修 9.4：原来定时快照配置读失败被 `.catch(() => null)` 吃掉，
      // 页面把 enabled 显示成「关闭」—— 用户会以为自己的定时任务被关掉了。
      // 现在记下失败原因，开关区显示「读不到配置」，而不是假装是关闭状态。
      TidyAPI.snapshotSchedule().catch((e) => {
        scheduleReadError.value = failText('读取定时快照配置', e)
        return null
      }),
    ])
    const bad = apiError(res, '读取快照失败')
    if (bad) return flash(bad, true)
    snapshots.value = (res.data?.items || []).map(sn => ({
      ...sn,
      count: Number(sn.count) || 0,
      kind_text: SNAP_KIND_TEXT[sn.kind] || '自动快照',
      at_text: fmtTimeText(sn.created),
    }))
    const scData = sc && sc.data ? sc.data : (sc || {})
    if (!sc) {
      // 上面的 catch 已经写了原因（网络层直接抛了）
      if (!scheduleReadError.value) scheduleReadError.value = '读取定时快照配置失败'
    } else {
      scheduleReadError.value = apiError(sc, '读取定时快照配置失败')
    }
    if (scData && !scheduleReadError.value) {
      schedule.value = {
        enabled: !!scData.enabled,
        interval_hours: Number(scData.interval_hours) || 24,
        keep: Number(scData.keep) || 7,
        last_run: scData.last_run || '',
      }
    }
    // 快照列表也是「上次缓存的数据」：切走再回来、刷新页面都还在（2026-10-25）
    saveCache(snapshotCacheKey(), {
      snapshots: snapshots.value,
      schedule: schedule.value,
    })
  } catch (e) {
    flash(failText('读取快照', e), true)
  } finally {
    snapLoading.value = false
  }
}

async function takeSnapshot() {
  snapBusy.value = true
  try {
    const res = await TidyAPI.takeMetaSnapshot(curDir.value)
    const bad = apiError(res, '创建快照失败')
    if (bad) return flash(bad, true)
    flash(res.message || '快照已保存')
    await loadSnapshots()
  } catch (e) {
    flash(failText('创建快照', e), true)
  } finally {
    snapBusy.value = false
  }
}

// 「看明细」的就地展开（2026-10-27）：
// · snapDetail 存在窗台上，但**只有点开的那一行**渲染它（见模板里的 isSnapOpen）；
// · 再点一次收起，另有「关闭」按钮；
// · 读失败时错误就地显示在这一行下面，而不是只 flash 一下（flash 过去就没了，
//   用户盯着空白的明细区不知道发生了什么）。
function isSnapOpen(sn) {
  return !!snapDetail.value && snapDetail.value.id === sn.id
}
function closeSnapDetail() {
  snapDetail.value = null
  snapErrorText.value = ''
}

/**
 * 把当前展开的这份快照导出成 JSON 文件（P3-13，2026-10-27）。
 *
 * 导的是**已经读进来的那份 diff**（用户看到什么就导什么），不重新请求 ——
 * 重新请求会拿到一个可能已经变了的版本（恢复操作会改曲库），
 * 那用户手里的文件就和屏幕上的对不上了。
 * 文件名带上快照时间和 ID，一份目录里存多份不会互相覆盖。
 */
function exportSnapDetail(sn) {
  const d = snapDetail.value
  if (!d || !d.id) return
  const payload = {
    exported_at: new Date().toISOString(),
    snapshot: {
      id: d.id,
      created: d.created || '',
      count: d.count || 0,
      writable: d.writable || 0,
      note: sn?.note || d.note || '',
    },
    diff_total: d.diff_total ?? (d.diff || []).length,
    diff_truncated: !!d.diff_truncated,
    diff: d.diff || [],
  }
  try {
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    // 时间戳里的 ':' 在 Windows 上不能进文件名，统一换成 '-'
    const stamp = String(d.created || '').replace(/[:\s]/g, '-').replace(/-+$/, '') || 'snapshot'
    a.download = `qulv-snapshot-${stamp}-${d.id}.json`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    // 立刻回收：慢一步在 iOS Safari 上会攒着 blob 不放（导出多次后页面会变卡）
    setTimeout(() => URL.revokeObjectURL(url), 0)
    flash(`已导出 ${(d.diff || []).length} 条差异`)
  } catch (e) {
    // 浏览器禁止下载（iOS PWA 的某些版本）时别装作成功了
    logError('exportSnapDetail', e)
    flash('这个浏览器不允许直接下载文件，可以换桌面端试', true)
  }
}
async function viewSnapshot(sn) {
  if (isSnapOpen(sn)) return closeSnapDetail()
  snapErrorText.value = ''
  snapDetail.value = null // 先收起上一份：明细头里的时间/数量不能是上一份的
  snapDetailLoading.value = true
  try {
    const res = await TidyAPI.metaSnapshotDetail(sn.id)
    const bad = apiError(res, '读取快照明细失败')
    if (bad) {
      // 这一份读不出来，保留展开态让错误有地方落脚（isSnapOpen 需要 id）
      snapDetail.value = { id: sn.id, created: sn.created, count: 0, writable: 0, diff: [] }
      snapErrorText.value = bad
      return
    }
    snapDetail.value = res.data
  } catch (e) {
    snapDetail.value = { id: sn.id, created: sn.created, count: 0, writable: 0, diff: [] }
    snapErrorText.value = failText('读取快照明细', e)
  } finally {
    snapDetailLoading.value = false
  }
}

/**
 * 恢复快照 —— 两步确认（2026-10-27 修 3.5 / 9.1）。
 *
 * 原来走 `window.confirm()`：iOS PWA 下是系统级弹窗，而且**确认框里塞不进差异摘要** ——
 * 用户只能盲确认「把 N 首恢复回去」。这里改成先掏开销掉的动作上「恢复到这份」，
 * 就地展开一份确认条（会改多少首 / 多少首已不在 / 多少首格式写不了），
 * 第二次点「确认恢复」才真写。
 */
function armRestoreSnapshot(sn) {
  if (snapRestorePending.value && snapRestorePending.value.id === sn.id) {
    snapRestorePending.value = null
    return
  }
  snapRestorePending.value = sn
  // 没读过这份明细就先读一遍 —— 确认条上的数字得是真的
  if (!(snapDetail.value && snapDetail.value.id === sn.id)) viewSnapshot(sn)
}

function cancelRestoreSnapshot() {
  snapRestorePending.value = null
}

function isRestoreArmed(sn) {
  return !!snapRestorePending.value && snapRestorePending.value.id === sn.id
}

/** 确认条上的差异摘要（数字缺失时给 null，模板里就只说「恢复前会自动留底」） */
function snapRestoreSummary(sn) {
  const d = snapDetail.value && snapDetail.value.id === sn.id ? snapDetail.value : null
  if (!d) return null
  const diff = d.diff || []
  if (!diff.length) return { none: true, total: 0, missing: 0, writable: 0, skipped: 0 }
  const missing = diff.filter(e => e.change === 'missing').length
  const total = Number(d.diff_total) || diff.length
  return {
    none: false,
    total,
    listed: diff.length,
    missing,
    writable: Number(d.writable) || 0,
    skipped: Math.max(0, total - (Number(d.writable) || 0)),
    truncated: !!d.diff_truncated,
  }
}

async function restoreSnapshot(sn) {
  if (!snapRestorePending.value || snapRestorePending.value.id !== sn.id) {
    return armRestoreSnapshot(sn)
  }
  snapRestorePending.value = null
  snapBusy.value = true
  try {
    const res = await TidyAPI.restoreMetaSnapshot(sn.id)
    const bad = apiError(res, '恢复快照失败')
    if (bad) return flash(bad, true)
    flash(res.message || '已恢复')
    await loadSnapshots()
    closeSnapDetail() // 恢复后旧 diff 已经过时，留着会让用户以为「怎么还没变」（9.7）
    loadIndexStats().catch(() => {})
    // 9.8：恢复改的是标签，歌名修复的名单也该跟着重算，
    // 否则快照段的数字变了、歌名段还是旧名单。只在本来就有名单时才重扫（别在空页上突然发起扫描）。
    if (titleFixItems.value.length) scanTitleFix()
  } catch (e) {
    flash(failText('恢复快照', e), true)
  } finally {
    snapBusy.value = false
  }
}

// ── 定时快照（用户 2026-10-24：「增加定时快照和手动快照」）──
// 与手动快照共用一个 GET /api/library/snapshots 响应里的 schedule 键，少一次请求。
const schedule = ref({ enabled: false, interval_hours: 24, keep: 7, last_run: '' })
// 读定时快照配置失败的原因（2026-10-27 修 9.4：不能把「读不到」显示成「已关闭」）
const scheduleReadError = ref('')
const scheduleBusy = ref(false)
const SNAPSHOT_INTERVAL_OPTIONS = [
  { value: '6', label: '6 小时' },
  { value: '12', label: '12 小时' },
  { value: '24', label: '1 天' },
  { value: '72', label: '3 天' },
  { value: '168', label: '7 天' },
]
const SNAPSHOT_KEEP_OPTIONS = [
  { value: '3', label: '3' },
  { value: '5', label: '5' },
  { value: '7', label: '7' },
  { value: '14', label: '14' },
  { value: '30', label: '30' },
]
/** 「上次拍的」：留空要说清楚是还没拍过，而不是没开 */
const scheduleLastText = computed(() => {
  if (!schedule.value.last_run) return schedule.value.enabled ? '还没拍过' : ''
  return `上次 ${fmtWhen(schedule.value.last_run)}`
})

async function saveSchedule(patch, okText) {
  scheduleBusy.value = true
  try {
    const res = await TidyAPI.saveSnapshotSchedule(patch)
    const bad = apiError(res, '保存定时快照设置失败')
    if (bad) return flash(bad, true)
    await loadSnapshots()
    if (okText) flash(okText)
  } catch (e) {
    flash(failText('保存定时快照设置', e), true)
  } finally {
    scheduleBusy.value = false
  }
}

async function toggleSchedule() {
  const next = !schedule.value.enabled
  await saveSchedule({ enabled: next }, next
    ? '定时快照已开启：正在拍第一份，之后按间隔自动拍'
    : '定时快照已关闭')
}

async function setScheduleInterval(v) {
  const n = Number(v) || 24
  if (n === schedule.value.interval_hours) return
  await saveSchedule({ interval_hours: n }, `已改为每 ${n} 小时`)
}

async function setScheduleKeep(v) {
  const n = Number(v) || 7
  if (n === schedule.value.keep) return
  await saveSchedule({ keep: n }, `已改为保留最近 ${n} 份`)
}

// ═════ 歌名修复（2026-10-24 用户：「体检 → 歌名修复，页面加入歌名修复功能」）═════
//
// 两件事合在一段：①扫出歌名可疑的曲目（后端 GET /api/library/title-fix/scan，只读索引）
// ②交给 AI 修（POST /fix，写标签 + 落一条补全记录）。
// 「修复记录」直接读补全记录（GET /library/complete/records 里的 title/artist 条目），
// 不另存一份本地日志 —— 记录的真相在备份清单里，本地再存一份必然会对不上。
const titleFixScanning = ref(false)
const titleFixBusy = ref(false)
const titleFixUndoBusy = ref(false)
const titleFixItems = ref([])
const titleFixAiReady = ref(true)
const titleFixRecords = ref([])
const titleFixSelectedRecords = ref([])

const titleFixSelectedCount = computed(() => titleFixItems.value.filter(it => it.selected && !it.done).length)
const titleFixAllSelected = computed(() => titleFixItems.value.length > 0 && titleFixItems.value.every(it => it.selected || it.done))

// ── 3.8（2026-10-27）：几百首时「勾了哪几首」在表里根本找不着 ──
// 加了「只看勾选的」过滤 + 「按歌名排序」，都只是**视图**，不改 titleFixItems 本体
// （本体的顺序参与缓存与勾选，动它会把用户的勾选顺序搞乱）。
const titleFixOnlySelected = ref(false)
function setTitleFixOnlySelected(v) {
  if (v === titleFixOnlySelected.value) return
  titleFixOnlySelected.value = v
}
const titleFixSort = ref('file') // file | title
function setTitleFixSort(v) {
  const next = v === 'title' ? 'title' : 'file'
  if (next === titleFixSort.value) return
  titleFixSort.value = next
}
const titleFixView = computed(() => {
  let rows = titleFixItems.value
  if (titleFixOnlySelected.value) rows = rows.filter(it => it.selected && !it.done)
  if (titleFixSort.value === 'title') {
    // 「（空）」和以「未知 / unknown」开头的排在最前面：这些才是最该修的
    rows = [...rows].sort((a, b) => String(a.title || '').localeCompare(String(b.title || ''), 'zh-CN'))
  }
  return rows
})
/** 被过滤掉的数量（提示用，免得用户以为名单变少了） */
const titleFixHiddenCount = computed(() =>
  titleFixOnlySelected.value ? titleFixItems.value.length - titleFixView.value.length : 0)

// 9.6：清空两步确认的武装态
const titleFixClearArmed = ref(false)
let titleFixClearTimer = 0
function clearTitleFixList() {
  if (!titleFixClearArmed.value) {
    titleFixClearArmed.value = true
    flash('再点一次「确认清空」就清掉这份名单（不会动文件，只是丢掉这次的扫描结果）')
    // 卡片头里塞不下「取消」按钮，改用 6 秒自动失效 —— 免得武装态一直挂着
    clearTimeout(titleFixClearTimer)
    titleFixClearTimer = setTimeout(() => { titleFixClearArmed.value = false }, 6000)
    return
  }
  clearTimeout(titleFixClearTimer)
  titleFixClearArmed.value = false
  titleFixItems.value = []
  saveTitleFix()
  flash('名单已清空，需要时重新「扫描歌名」')
}
/** 卡片头右侧那句「最近修复时间」：取记录里最新一条，没有记录就不显示这一行 */
const titleFixRecordsAtText = computed(() => {
  const at = titleFixRecords.value.reduce((m, r) => Math.max(m, Number(r.at) || 0), 0)
  if (!at) return ''
  return `最近修复 ${fmtWhen(at)}`
})

function toggleTitleFix(it) {
  it.selected = !it.selected
  saveTitleFix()
}
function toggleAllTitleFix() {
  const next = !titleFixAllSelected.value
  for (const it of titleFixItems.value) it.selected = next
  saveTitleFix()
}
function toggleTitleFixRecord(id) {
  const i = titleFixSelectedRecords.value.indexOf(id)
  if (i >= 0) titleFixSelectedRecords.value.splice(i, 1)
  else titleFixSelectedRecords.value.push(id)
  saveTitleFix()
}

// ── 歌名修复结果缓存（2026-10-25 用户：「保留五个分段的上次缓存数据和操作」）──
// 和体检 / 缺口 / 卡片同一套（prefs.saveCache：localStorage + 后端双写，按目录分开）。
// **勾选状态也要存**：用户勾了一半切去看别的段，回来勾选不能全丢 ——
// 「操作」也是要保留的状态，不只是结果表格。
// ttl=0 = 永不过期，与 restoreAudit 一致（用户要的就是「一直看得到上次的」）。
const titleFixAt = ref(0)

function titleFixCacheKey(dir) {
  return `last_titlefix:${dir}`
}

function saveTitleFix() {
  const dir = curDir.value
  if (!dir) return
  saveCache(titleFixCacheKey(dir), {
    at: titleFixAt.value,
    ai_ready: titleFixAiReady.value,
    records_at: titleFixRecordsAtText.value,
    items: titleFixItems.value,
    selected_records: titleFixSelectedRecords.value,
  })
}

function restoreTitleFix() {
  const dir = curDir.value
  if (!dir) return
  const cached = loadCache(titleFixCacheKey(dir), 0)
  if (!cached || typeof cached !== 'object') return
  titleFixAt.value = Number(cached.at) || 0
  titleFixAiReady.value = cached.ai_ready !== false
  // 老缓存可能没有 selected 字段：一律按「未勾」还原，不给它编造勾选
  titleFixItems.value = (cached.items || []).map(it => ({ ...it, selected: !!it.selected }))
  titleFixSelectedRecords.value = Array.isArray(cached.selected_records) ? cached.selected_records : []
}

/** 歌名修复上次扫描的时间（非 0 = 从缓存恢复的） */
const titleFixAtText = computed(() => (titleFixAt.value ? fmtWhen(titleFixAt.value) : ''))

// ── 缺歌词（用户 2026-10-25 需求 N）──
// 逻辑全在 services/useLyricGaps.js，这里只把三个依赖喂进去：
// 目录、提示条、写入成功后刷新索引统计（歌词写进标签后 has_lyric 就变了）。
const lyric = useLyricGaps({
  dir: () => curDir.value,
  flash: (msg, isErr) => flash(msg, isErr),
  afterWrite: () => { loadIndexStats().catch(() => {}) },
})
const {
  items: lyricItems,
  total: lyricTotal,
  scanning: lyricScanning,
  fetching: lyricFetching,
  writing: lyricWriting,
  atText: lyricAtText,
  selectedCount: lyricSelectedCount,
  foundCount: lyricFoundCount,
  allSelected: lyricAllSelected,
  restore: restoreLyricGaps,
  scan: scanLyricGaps,
  toggle: toggleLyric,
  toggleAll: toggleAllLyric,
  fetchPreview: fetchLyricPreview,
  write: writeLyric,
  clear: clearLyricGaps,
} = lyric

/**
 * 歌词预览（P3-15，2026-10-27）。
 *
 * 只在本地展开，**不发请求也不写盘** —— 歌词文本在「查找歌词」那一步就已经
 * 跟着结果回来了（`items[i].lyric`）。一次只展开一条：这行会撑到 224px 高，
 * 同时展开多条会把表格顶出去，用户反而找不到刚点的那行。
 */
const lyricPreviewPath = ref('')
function toggleLyricPreview(it) {
  lyricPreviewPath.value = lyricPreviewPath.value === it.path ? '' : it.path
}
/** 预览行数（`
` 切分，丢掉结尾空行） */
function lyricPreviewLines(it) {
  const t = String(it?.lyric || '').replace(/\s+$/, '')
  return t ? t.split(/\r?\n/) : []
}
/**
 * 预览正文：最多 60 行。
 * 整首词一般 30-60 行，超过这个数说明匹配到的可能是长电台版/合辑版 ——
 * 更该让用户把注意力放在「开头对不对」上，而不是把整页刷满。
 */
function lyricPreviewText(it) {
  const lines = lyricPreviewLines(it)
  if (!lines.length) return '（这首歌的结果里没有歌词正文）'
  if (lines.length <= 60) return lines.join('\n')
  return lines.slice(0, 60).join('\n') + `\n\n…… 还有 ${lines.length - 60} 行没显示`
}

// tab 切到后台再回来时 localStorage 可能被别的标签页写过，这里不主动刷新 ——
// 这一页的真相在曲库索引里，切回来点「扫描歌名」就会重算。

// ── 手动歌名体检（用户 2026-10-26：随机抽 10 首问 AI，只出结果不改文件）──
const manualAuditBusy = ref(false)
const manualAuditItems = ref([])
const manualAuditSummary = ref('')
async function runManualTitleAudit() {
  if (manualAuditBusy.value) return
  manualAuditBusy.value = true
  manualAuditSummary.value = ''
  manualAuditItems.value = []
  try {
    // 10 首要逐首问大模型，后端自己留 120s；这里不设 axios 超时（默认值可能更短）
    const res = await TidyAPI.titleFixAudit(curDir.value, 10)
    const bad = apiError(res, '手动歌名体检失败')
    if (bad) return flash(bad, true)
    const d = res.data || {}
    manualAuditItems.value = (d.items || []).map(it => ({
      path: it.path,
      file: it.file || baseName(it.path),
      title: it.title || '',
      artist: it.artist || '',
      need: it.need || '',
      fixable: !!it.fixable,
      new_title: it.new_title || '',
      new_artist: it.new_artist || '',
      source: it.source || 'none',
      note: it.note || '',
    }))
    const sampled = Number(d.sampled) || manualAuditItems.value.length
    manualAuditSummary.value = `抽 ${sampled} 首（曲库共 ${d.pool || 0} 首）· 该修 ${d.need_fix || 0} · 能修 ${d.fixable || 0}`
    flash(`体检完成：${manualAuditSummary.value}`)
  } catch (e) {
    flash(failText('手动歌名体检', e), true)
  } finally {
    manualAuditBusy.value = false
  }
}

async function scanTitleFix() {
  const dir = curDir.value
  titleFixScanning.value = true
  try {
    const res = await TidyAPI.titleFixScan(dir)
    const bad = apiError(res, '扫描歌名失败')
    if (bad) return flash(bad, true)
    const d = res.data || {}
    titleFixAiReady.value = d.ai_ready !== false
    titleFixAt.value = 0 // 本次刚扫的，不是缓存
    titleFixItems.value = (d.items || []).map(it => ({
      path: it.path,
      title: it.old || '',
      artist: it.artist || '',
      reason: it.reason || '',
      suggested_title: it.suggest || '',
      selected: true,
      done: false,
      error: '',
      source: '',
    }))
    saveTitleFix()
    if (!titleFixItems.value.length) flash('没扫到歌名可疑的曲目')
    else if (!titleFixAiReady.value) flash(`扫到 ${titleFixItems.value.length} 首，但还没配置 AI 大模型 —— 修不了`, true)
    loadTitleFixRecords().catch(() => {})
  } catch (e) {
    flash(failText('扫描歌名', e), true)
  } finally {
    titleFixScanning.value = false
  }
}

async function runTitleFix() {
  const targets = titleFixItems.value.filter(it => it.selected && !it.done)
  if (!targets.length) return flash('先勾选要修的曲目', true)
  titleFixBusy.value = true
  try {
    const res = await TidyAPI.titleFixFix({ paths: targets.map(it => it.path) })
    const bad = apiError(res, '修复歌名失败')
    if (bad) return flash(bad, true)
    const byPath = new Map((res.data?.items || []).map(r => [r.path, r]))
    for (const it of targets) {
      const r = byPath.get(it.path)
      if (!r) continue
      if (r.ok) {
        it.done = true
        it.written_title = r.suggest || ''
        it.source = 'ai'
        it.error = ''
      } else {
        it.error = r.error || '没修成'
        it.selected = false
      }
    }
    flash(res.message || `修好 ${res.data?.fixed ?? 0} 首`)
    saveTitleFix()
    loadTitleFixRecords().catch(() => {})
    scanGaps().catch(() => {})
    loadIndexStats().catch(() => {})
  } catch (e) {
    flash(failText('修复歌名', e), true)
  } finally {
    titleFixBusy.value = false
  }
}

/**
 * 修复记录：从补全记录里挑出「改了标题」的那些条目，摊平成一列。
 * 每条记录 = 一次操作（AI 修复或手动改），带 id + 原文件路径，撤回就是回一个备份。
 */
async function loadTitleFixRecords() {
  const res = await TidyAPI.completeRecords()
  const bad = apiError(res, '读取修复记录失败')
  if (bad) return flash(bad, true)
  const out = []
  for (const rec of res.data?.records || []) {
    for (const e of rec.entries || []) {
      const fields = e.fields || []
      if (!fields.includes('title') && !fields.includes('artist')) continue
      out.push({
        id: rec.id,
        path: e.original,
        old_title: e.old_title || '',
        new_title: e.title || '',
        at: rec.at,
        at_text: fmtWhen(rec.at),
        undone: false,
      })
    }
  }
  titleFixRecords.value = out
  titleFixSelectedRecords.value = titleFixSelectedRecords.value.filter(id =>
    out.some(r => r.id === id))
}

async function undoTitleFixRecords(ids) {
  if (!ids.length) return
  const byRecord = new Map()
  for (const id of ids) byRecord.set(id, true)
  const itemsByRecord = new Map()
  for (const r of titleFixRecords.value) {
    if (!byRecord.has(r.id)) continue
    if (!itemsByRecord.has(r.id)) itemsByRecord.set(r.id, [])
    itemsByRecord.get(r.id).push(r.path)
  }
  titleFixUndoBusy.value = true
  let okCount = 0
  try {
    for (const [id, paths] of itemsByRecord) {
      const res = await TidyAPI.editCompleteRecords({ id, items: paths, op: 'restore' })
      if (apiError(res, '撤回失败')) continue
      okCount += res.data?.restored || 0
    }
    flash(`已撤回 ${okCount} 首`)
    titleFixSelectedRecords.value = []
    await loadTitleFixRecords()
    // 撤回后列表里那些条目变成「已撤回」，勾选也清了 —— 存一份新状态，
    // 否则刷新回来还是撤回前的样子
    saveTitleFix()
    loadIndexStats().catch(() => {})
    scanGaps().catch(() => {})
  } catch (e) {
    flash(failText('撤回歌名修复', e), true)
  } finally {
    titleFixUndoBusy.value = false
  }
}

/** 改回手写标题：撤回之后／或者干脆手改一个更好的 —— 直接改文件，同样进记录 */
async function editTitleFixRecord(r) {
  const val = window.prompt('改成什么歌名？（会直接写进文件标签，可再撤回）', r.new_title || '')
  if (val === null) return
  const title = val.trim()
  if (!title) return flash('歌名不能为空', true)
  try {
    const res = await TidyAPI.editCompleteRecords({ id: r.id, items: [r.path], op: 'edit', title })
    const bad = apiError(res, '改写歌名失败')
    if (bad) return flash(bad, true)
    flash(res.message || '已改写')
    await loadTitleFixRecords()
  } catch (e) {
    flash(failText('改写歌名', e), true)
  }
}

// 读取上次整理快照：挂载时自动加载持久化索引的统计，首屏即可看到上次结果
/**
 * 索引统计（快照段那行「上次整理…」）。
 *
 * 2026-10-27 修 9.3：原来 catch 是空的注释「后端不可用/无索引：保持空，不打扰」——
 * 后果是后端挂掉时页面显示「还没有索引快照 —— 点右上「刷新」再试」，
 * 用户会以为**库里没歌**，而不是「服务读不到」。现在把失败原因单独记下来并显示。
 */
async function loadIndexStats() {
  const dir = curDir.value
  if (!dir) return
  statsLoading.value = true
  try {
    const res = await TidyAPI.indexStats(dir)
    const bad = apiError(res, '读取索引统计失败')
    if (bad) {
      statsError.value = bad
      return
    }
    statsError.value = ''
    indexStats.value = res.data && res.data.total ? res.data : null
  } catch (e) {
    statsError.value = failText('读取索引统计', e)
  } finally {
    statsLoading.value = false
  }
}

async function runIndexScan() {
  const dir = curDir.value
  if (!dir) return flash('请填写曲库目录', true)
  savePref('nas_dir', dir)
  indexing.value = true
  try {
    const res = await TidyAPI.index(dir)
    if (res.code === 200) {
      indexResult.value = res.data
      indexScanAt.value = 0 // 本次刚扫的
      if (res.warning) flash(res.warning, true)
      // 索引变了 ⇒ 索引统计跟着变，顺手重读一次（O(1) 的一行数字）
      loadIndexStats()
      // ⚠️ 这里**不再**顺手 scanGaps()（2026-10-27 修 2.8 / 5.1）：
      // 原来 scanAll 外面 await 一次、这里又 auto 一次 ⇒ 一次「扫描」点击发两次缺口统计，
      // 弱网下页面上的待补数字会跳两下。缺口重算由调用方决定：
      // scanAll 会 await scanGaps()，其它入口（快照段的「立即更新索引」）自己显式调。
      saveDedupe() // 索引扫描结果一起落盘（用户切段回来还能看到这次扫了什么）
    } else {
      flash(apiError(res, '扫描失败'), true)
    }
  } catch (e) {
    flash(failText('增量扫描', e), true)
  } finally {
    indexing.value = false
  }
}

/**
 * 快照段那行「立即更新索引」（2026-10-27 修 2.5）。
 *
 * 与补全段的「扫描」等价（增量扫描 + 重算缺口），只是从快照段就地触发 ——
 * 原来这里只有一句 tooltip 让用户「去补全段找增量扫描」，而那个入口不存在了。
 */
async function refreshIndexHere() {
  if (!curDir.value) return flash('请先选曲库目录', true)
  await runIndexScan()
  await scanGaps()
}

// ── 查重 / 索引扫描结果的持久化（2026-10-25 用户：「保留五个分段的上次缓存数据和操作」）──
// 与体检同一套：按目录分开存、ttl=0 永不过期。
// 为什么不复用 runDedupe：查重是重活（要扫全库算哈希），每次进页面都跑一遍没必要，
// 用户切到歌名修复看一眼再回来，应该看到和刚才一模一样的表格。
function dedupeCacheKey(dir) {
  return `last_dedupe:${dir || ''}`
}
function indexScanCacheKey(dir) {
  return `last_index_scan:${dir || ''}`
}

function saveDedupe() {
  const dir = curDir.value
  if (!dir) return
  saveCache(dedupeCacheKey(dir), {
    at: dedupeAt.value,
    result: dupResult.value,
    resolved: dupResolveResult.value,
  })
  saveCache(indexScanCacheKey(dir), { at: indexScanAt.value, result: indexResult.value })
}

function restoreDedupe() {
  const dir = curDir.value
  if (!dir) return
  const c = loadCache(dedupeCacheKey(dir), 0)
  if (c && typeof c === 'object') {
    dedupeAt.value = Number(c.at) || 0
    dupResult.value = c.result || null
    dupResolveResult.value = c.resolved || null
  }
  const i = loadCache(indexScanCacheKey(dir), 0)
  if (i && typeof i === 'object') {
    indexScanAt.value = Number(i.at) || 0
    indexResult.value = i.result || null
  }
}

/** 查重结果的落盘时间文案 */
const dedupeAtText = computed(() => (dedupeAt.value ? `上次查重 ${fmtWhen(dedupeAt.value)}` : ''))

async function runDedupe() {
  const dir = curDir.value
  if (!dir) return flash('请填写曲库目录', true)
  savePref('nas_dir', dir)
  deduping.value = true
  dupResolveResult.value = null
  // 新的一次查重 ⇒ 上一轮的「已看过清单」作废，红按钮退回「移入回收站」
  dupConfirmArmed.value = false
  dupShowAll.value = false
  try {
    const res = await TidyAPI.duplicates(dir)
    if (res.code === 200) {
      dupResult.value = res.data
      dedupeAt.value = 0 // 本次刚跑的
      saveDedupe()
    } else flash(apiError(res, '查重失败'), true)
  } catch (e) {
    flash(failText('查找重复', e), true)
  } finally {
    deduping.value = false
  }
}

/**
 * 清理重复文件。
 *
 * 2026-10-27 改（审查报告 P0-4 / 2.7 / 3.7）：
 * · 去掉了 `window.confirm()` —— iOS PWA / 微信里是系统级弹窗，样式不可控，
 *   而且确认框里塞不下「将移走 N 个文件、释放 X」的清单，用户只能盲确认；
 * · 第一次点红按钮**只干跑**并把清单摊开（按钮改成「确认移入回收站」），
 *   第二次点才真动手 —— 这是整页唯一不可逆的批量操作，值得多一次点击；
 * · 执行完不再被随后的 `runDedupe()` 把结果清掉（原来「执行完成」面板一闪就没）。
 */
/** P0-4：查重确认条里的「取消」—— 武装后必须能退回去 */
function cancelResolveDup() {
  dupConfirmArmed.value = false
}

async function resolveDup(dryRun) {
  const dir = curDir.value
  if (!dir) return flash('请填写曲库目录', true)
  if (!dryRun && !dupConfirmArmed.value) {
    flash('先看一眼清单 —— 再点一次「确认移入回收站」才会真的移')
    await resolveDup(true)
    return
  }
  dupBusy.value = true
  try {
    const res = await TidyAPI.resolveDuplicates({ dir, mode: 'trash', dry_run: dryRun })
    if (res.code === 200) {
      dupResolveResult.value = res.data
      dupConfirmArmed.value = true
      saveDedupe()
      // 逐文件失败原因太长，界面只给数量，明细进日志
      const errs = res.data?.errors || []
      if (errs.length) logError('清理重复文件', `${errs.length} 个文件处理失败`, errs.slice(0, 20).join('；'))
      if (!dryRun) {
        const done = dupResolveResult.value
        dupConfirmArmed.value = false
        await runDedupe()
        await runAudit()
        // runDedupe 会在开头清掉 dupResolveResult —— 把「执行完成：移动 N」放回去，
        // 否则用户刚做完操作，唯一的结果面板就消失了。
        dupResolveResult.value = done
        saveDedupe()
      }
    } else {
      flash(apiError(res, '执行失败'), true)
    }
  } catch (e) {
    flash(failText('清理重复文件', e), true)
  } finally {
    dupBusy.value = false
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
/** 9.1：撤销补全的两步确认（第一次点只摆出确认条，第二次才真还原） */
const undoConfirmArmed = ref(false)
// 停止请求在途 —— 防连点（后端 cancelRun 是幂等的，但连点会让界面文案乱跳）
const stopBusy = ref(false)
// 预览出来的计划（还没写文件）。用户确认后才执行。
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
 * ⚠️ 2026-10-27 删除：`completeSteps = COMPLETE_STEPS`。
 *
 * 它从 2026-10-12 加进来（用户「点完扫描缺失，下一步做什么？」）就**从来没有**
 * 被模板渲染过 —— 三步流程条只活在注释和死变量里，用户一直看不到。
 * 现在由常驻正文的 `completeHint`（下面的那一行）承担「下一步点哪」，
 * 它每一步都指的是界面上真实存在的按钮，所以不再需要这组步骤定义。
 *
 * 步骤/文案的真相在 services/libraryView.js（completeStep / completeHint），
 * 由 libraryView.test.mjs 钉住。
 */
const completeStep = computed(() => _completeStep(gaps.value, previewState.value === 'done', previewing.value))
const completeHint = computed(() => _completeHint(gaps.value, previewState.value === 'done', previewing.value))

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
  const dir = curDir.value
  if (!dir) {
    gaps.value = null
    return
  }
  const cached = loadCache(gapsCacheKey(dir), 0)
  gaps.value = cached && typeof cached === 'object' ? cached : null
}

/**
 * 补全段的「扫描」——用户 2026-10-25：「扫描缺失和扫描是同一功能吧」。
 *
 * 两件事合并成一次点击：先把磁盘上新增/变化的文件收进索引，再重算缺哪些字段。
 * 顺序不能反：缺口统计只读索引，索引没更新时统计出来的是旧世界。
 * 单独保留 scanGaps / runIndexScan 两个函数是因为别处（扫描完补全、查重后刷新）还在用。
 */
async function scanAll() {
  const dir = curDir.value
  if (!dir) return flash('请填写曲库目录', true)
  await runIndexScan()
  await scanGaps()
}

async function scanGaps() {
  const dir = curDir.value
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
watch(curDir, () => {
  restoreGaps()
  restoreCards()
  // 换目录就把歌名修复 / 查重 / 索引扫描也换成该目录的上次结果（各目录分开存）
  restoreTitleFix()
  restoreDedupe()
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
  const dir = curDir.value
  if (!dir) return
  const cached = loadCache(cardsCacheKey(dir), 0)
  if (!cached || typeof cached !== 'object') return
  cardsAt.value = Number(cached.at) || 0
  completeBar.value = cached.bar || []
  completedCards.value = withCover(cached.cards || [])
  skippedCards.value = withCover(cached.skipped || [])
}

async function loadCompletedCards(summary) {
  const dir = curDir.value
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

watch(curDir, restoreCards)

const completePercent = computed(() => _completePercent(completeJob.value))

/** 补全卡片的标题四态（跑 / 正在停 / 上次被停 / 上次结果）——「正在停止」不能显示成「正在补全」 */
const completeTitle = computed(() => _completeTitleText(completeJob.value) || '补全整理')

/**
 * 其中有多少首是「规则配不上、由 AI 从候选里挑的」。
 * 透出来是为了**可分辨** —— 用户有权知道哪些结果是模型判断的。
 */
const aiMatchedCount = computed(() => _aiMatchedCount(completeJob.value?.summary))

// ── 流式预览的状态机 ──
//
// 用户 2026-10-24：「预览改成流式显示，加暂停 / 继续 / 停止」。
//
// 三个状态要分清，界面话术不能混：
//   running —— 还在跑，条目继续往列表里加
//   done    —— 跑完并拿到了 plan_id，可以「确认写入」
//   idle    —— 用户中途暂停了（或流断了）：**没有 plan_id**，这份清单不完整
// 所以「能不能写」只看 planId，不看列表里有几行。
const previewState = ref('idle') // idle | running | done
const previewRows = ref([])
const previewMeta = ref({ done: 0, total: 0, matched: 0 })
const previewPlanId = ref('')
const previewDir = ref('')

// 「暂停」= 断开流（服务端立刻取消，不再打外网），已出的行留着但**不能用**；
// 「继续」就是重新发一次流（后端没有断点续传，所以是从第一首重来，按钮文案照实写）。
// 刻意不做「挂起连接」的假暂停，理由见 client.js 里 streamNDJSON 的注释。
let previewAbort = null

const previewOpen = computed(() => previewing.value || previewState.value === 'running' || previewState.value === 'done')
const previewHeading = computed(() => (previewState.value === 'done' ? '预览完成（还没有动过文件）' : '预览已暂停'))
const previewMatchedCount = computed(() => previewRows.value.filter((r) => r.matched).length)
const previewPercent = computed(() => {
  const { done, total } = previewMeta.value
  if (!total) return 0
  return Math.min(100, Math.round((done / total) * 100))
})

function previewUpsert(item, done) {
  if (!item || !item.path) return
  const rows = previewRows.value
  const key = item.path
  // 同一首理论上只来一次；重复推送就取最新（后端也按 Index 去重，这里是第二道）
  const i = rows.findIndex((r) => r.path === key)
  const row = { ...item }
  if (i >= 0) rows.splice(i, 1, row)
  else rows.push(row)
  if (done > previewMeta.value.done) previewMeta.value = { ...previewMeta.value, done }
}

function resetPreviewState() {
  previewState.value = 'idle'
  previewRows.value = []
  previewMeta.value = { done: 0, total: 0, matched: 0 }
  previewPlanId.value = ''
}

/** 停止预览（也算「暂停」）：断开连接，服务端立刻不再派发新曲目 */
function stopPreview() {
  if (previewAbort) {
    try {
      previewAbort.abort()
    } catch (_) { /* 已断开就无所谓 */ }
  }
}

function closePreview() {
  stopPreview()
  resetPreviewState()
  previewing.value = false
}

/**
 * 开始（或重新开始）预览。
 * 走的是流式接口 /library/complete/preview/stream —— 匹配完一首就出一行，
 * 与旧的同步预览是同一套匹配逻辑，只是不再等全部跑完才返回。
 */
async function startPreview() {
  const dir = curDir.value
  if (!dir) return flash('请填写曲库目录', true)
  if (!selectedFields.value.length) return flash('先勾选要补的字段', true)
  savePref('nas_dir', dir)
  savePref('complete_limit', String(completeLimit.value || 30))

  stopPreview() // 上一轮可能还挂着
  resetPreviewState()
  completeJob.value = null
  previewDir.value = dir
  previewing.value = true
  previewState.value = 'running'

  const ctrl = new AbortController()
  previewAbort = ctrl

  let failure = ''
  await TidyAPI.previewStream(
    {
      dir,
      limit: completeLimit.value || 30,
      fields: selectedFields.value,
      overwrite: !!overwriteExisting.value,
      use_ai_genre: !!allowAIGenre.value,
    },
    {
      signal: ctrl.signal,
      onEvent: (ev) => {
        if (ev.type === 'start') {
          previewMeta.value = { done: 0, total: ev.total || 0, matched: 0 }
        } else if (ev.type === 'item') {
          previewUpsert(ev.item, ev.done || 0)
        } else if (ev.type === 'progress') {
          previewMeta.value = { ...previewMeta.value, done: ev.done || 0, total: ev.total || previewMeta.value.total }
        } else if (ev.type === 'done') {
          previewPlanId.value = ev.plan_id || ''
          previewMeta.value = { done: ev.done || 0, total: ev.total || 0, matched: ev.matched || 0 }
          previewState.value = 'done'
        } else if (ev.type === 'cancelled') {
          if (typeof ev.matched === 'number') {
            previewMeta.value = { ...previewMeta.value, matched: ev.matched }
          }
          if (ev.message) flash(ev.message)
        } else if (ev.type === 'error') {
          failure = ev.message || ev.error || '预览失败'
        }
      },
    },
  )

  if (previewAbort === ctrl) previewAbort = null
  previewing.value = false

  // 用户自己停的：保留已出的行，明确告诉它这份不完整
  if (ctrl.signal.aborted) {
    if (previewState.value === 'running') previewState.value = 'idle'
    return
  }
  if (failure) {
    previewState.value = 'idle'
    flash(failure, true)
    return
  }
  if (previewState.value === 'done' && !previewPlanId.value) {
    previewState.value = 'idle'
  }
  if (previewState.value === 'done') {
    flash('预览完成，确认后才会写入')
  }
}

/**
 * 执行已确认的计划。
 * ⚠️ 传回 `plan_id` 而不是重新提交目录 —— 执行的是**用户刚看到的那份计划**，
 * 否则搜索结果变了就会「批准了 A、结果写了 B」。
 */
async function executeComplete() {
  const pid = previewPlanId.value
  if (!pid) return flash('预览已失效，请重新预览', true)

  previewPlanId.value = ''
  try {
    const res = await TidyAPI.executeComplete(pid)
    completeJob.value = res?.data
    flash(res?.message || '已开始写入')
    startCompletePolling()
    // 计划已消费，面板收起（进度接棒显示在下面的补全卡片里）
    resetPreviewState()
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
 * 撤销只做一次（没有「重做」）—— 两步确认的第二行会写清楚（9.1）。
 */
function cancelUndoComplete() {
  // 9.1：确认条里的「取消」—— 原来武装之后没有退路，只能真按下去或离开页面
  undoConfirmArmed.value = false
}

async function undoComplete() {
  // 9.1：从 window.confirm 改成就地两步（见模板里的确认条）
  if (!undoConfirmArmed.value) {
    undoConfirmArmed.value = true
    return
  }
  undoConfirmArmed.value = false
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
    saveCards(curDir.value)
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
    // 「刷新」= 把五段的上次结果全部重读一遍（不只是补全段的卡片）。
    // 用户点刷新时想看的是「我现在还剩什么没做」，只刷一段会让人疑惑另外四段为什么不动。
    restoreAudit()
    restoreGaps()
    restoreCards()
    restoreTitleFix()
    restoreDedupe()
    restoreSnapshots()
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
  // 2026-10-25 用户：「保留五个分段的上次缓存数据和操作」——
  // 歌名修复（含勾选）、查重 + 索引扫描、数据快照三段的缓存也一起恢复，
  // 不然切段回来或刷新后那三段就只剩空态。
  restoreTitleFix()
  restoreDedupe()
  restoreSnapshots()
  restoreLyricGaps()
  // 2026-10-25：曲库目录改由分段导航行右边的 TidyDirChip 选（写 prefs.nas_dir），
  // 这里监听它选完之后发的事件 —— 同标签页不会触发 storage 事件，必须走自定义事件。
  onDirChanged = (e) => {
    nasDir.value = (e && e.detail && e.detail.dir) || loadPref('nas_dir', '')
    tidyDir.value = ''
    loadIndexStats()
    restoreGaps()
  }
  window.addEventListener('ys:dir-changed', onDirChanged)
  // 3.10：勾选状态只活在内存里（缓存里恢复的是上一次的列表，不是本次勾选），
  // 刷新/关标签页会悄无声息地丢掉「已勾了 N 首、正要去写」。有未落盘的勾选时拦一下。
  onBeforeUnload = (e) => {
    if (pendingSelectionCount.value <= 0) return
    e.preventDefault()
    e.returnValue = ''
  }
  window.addEventListener('beforeunload', onBeforeUnload)
  // 歌名修复的记录列表是**服务端真相**（补全记录），缓存只存它算出来的时间文案，
  // 这里补拉一次：本地缓存可能落后于另一次会话里做过的修复。
  loadTitleFixRecords().catch(() => {})
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
  if (onDirChanged) window.removeEventListener('ys:dir-changed', onDirChanged)
  if (onBeforeUnload) window.removeEventListener('beforeunload', onBeforeUnload)
})

// 离开「补全整理」就丢掉入口带来的一次性目录，避免导航栏自己走进来时停在过期目录
watch(currentView, (v) => {
  if (v !== 'tidy') healthDir.value = ''
})
</script>
