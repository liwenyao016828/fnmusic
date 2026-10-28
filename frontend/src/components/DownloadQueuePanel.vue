<template>
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
    <!--
      工具条（2026-10-19 渐进式披露重构）

      改之前这一行里挤着：存储路径条（独立一整行灰底）、6 个筛选标签、
      全选、全部暂停、全部继续、全部恢复、重试失败、停止全部、清除选中、
      一键清除记录、下载设置 —— 11 个控件争一条 40px 的行。

      现在分三层：
        · 常驻：存储位置（一行小字，点击即改）+ 筛选分段 + 两个「当前真能做」的图标按钮
        · ⋯：全部恢复 / 重试失败 / 停止全部 / 一键清除记录（下载设置已搬成工具栏上的齿轮按钮）
        · 勾选了任务才浮出底部操作条（清除选中 / 全选当前筛选）
    -->
    <div class="flex shrink-0 flex-col gap-2 border-b border-[var(--ys-border)] bg-white px-3 py-2.5 sm:px-4">
      <!--
        2026-10-25 用户：「下载-队列-去掉目录选择。移除三小点下拉框，全部恢复、重试失败、
        停止全部、去除记录都移出来。并根据屏幕大小适配图标与文字精简效果。」

        所以这一行现在是两层（原来是「路径 + 筛选 + 图标 + ⋯」挤在一行）：
          第一层：筛选分段（可横向滚）+ 批量动作（按断点精简）
          第二层：勾选后的批量条（选中了才出现，单独一行，不再和上面的按钮抢位置）
        **队列页不再有目录入口**：落盘目录在「系统 → 设置 → 存储目录」唯一一处修改，
        这里连只读路径也不摆了 —— 用户点名「去掉目录选择」，留着只读路径仍会让人误以为能点。
      -->
      <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5">
        <!-- 筛选：分段控件（沉底轨道 + 白色药丸），窄屏可横向滚动，不再换行挤成两行 -->
        <div class="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto rounded-[10px] bg-[var(--ys-surface-sunken)] p-1 no-scrollbar">
          <button
            v-for="tab in filterTabs" :key="tab.id"
            @click="currentFilter = tab.id"
            :class="[
              'ys-press shrink-0 whitespace-nowrap rounded-[8px] px-2.5 py-1 text-[11.5px] font-medium transition-colors',
              currentFilter === tab.id
                ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
                : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]',
            ]"
          >
            {{ tab.name }}
            <span class="ml-0.5 text-[10px] opacity-60">{{ tab.count }}</span>
          </button>
        </div>

        <div class="flex shrink-0 items-center gap-0.5">
          <!--
            队列的「暂停 / 继续」合成**一个常显的切换按钮**：此刻能做什么就画什么。
            用户 2026-10-25：「下载页面我怎么看到的还是没有继续的三角形图标」——
            根因不是图标错了，而是原来两个按钮都带 `v-if`（队列暂停 / 有暂停任务），
            队列正常跑着、也没人暂停过时**一个都不渲染**，用户永远等不到那个三角形。
            结果态图标会让人以为「有任务在跑」＝暂停图标，其实是「点了会暂停」；
            所以 title 必须写清动作，不能只写状态。
          -->
          <button
            @click="onToggleQueue"
            class="ys-press grid h-8 w-8 place-items-center rounded-full transition-colors hover:bg-black/[0.05]"
            :class="queueNeedsResume
              ? 'text-emerald-600 hover:text-emerald-700'
              : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]'"
            :title="queueNeedsResume ? '继续下载（队列已暂停或有暂停的任务）' : '暂停下载（不会删任务）'"
            :aria-label="queueNeedsResume ? '继续下载' : '暂停下载'"
          >
            <PlayCircle v-if="queueNeedsResume" class="h-4 w-4" :stroke-width="1.9" />
            <Pause v-else class="h-4 w-4" :stroke-width="1.9" />
          </button>

          <span class="mx-0.5 h-4 w-px bg-[var(--ys-border)]" aria-hidden="true"></span>
          <!--
            原来藏在右侧「⋯」里的四件事，现在摆在明面上。
            自适应两级：≥lg 图标 + 文字（lg 以下空间紧，中屏也只剩图标，
            档位比上一版（sm 就出文字）后移一档，避免 640–900px 区间文字挤爆）。
          -->
          <button
            v-for="act in queueActions" :key="act.id"
            type="button"
            :disabled="act.disabled"
            :title="act.hint || act.label"
            :aria-label="act.label"
            :class="[
              'ys-press flex items-center gap-1 rounded-full px-1.5 py-1.5 text-[11.5px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-35 lg:px-2 lg:gap-1.5',
              act.highlight
                ? 'text-emerald-600 hover:bg-emerald-50'
                : act.danger
                  ? 'text-rose-600 hover:bg-rose-50'
                  : 'text-[var(--ys-text-muted)] hover:bg-black/[0.05] hover:text-[var(--ys-text)]',
            ]"
            @click="onQueueMenu(act.id)"
          >
            <component :is="act.icon" class="h-4 w-4 shrink-0" :stroke-width="1.9" />
            <!--
              ⚠️ Issue #8 真正的坑在这里（2026-10-25 复测才发现）。
              以前这一行是写死的 `hidden lg:inline`：小于 1024px 时**所有**批量按钮
              都只剩一个图标。用户在手机（420px）上看到的是四个无字圆形图标，
              自然一直在问「一键重试到底做了没有」—— 功能是好的，只是没有字。
              现在：`act.highlight`（失败数 > 0 的重试失败）**任何宽度都出文字**，
              因为它正是用户要找的那个按钮；其余按钮维持 lg 起出文字，窄屏靠 aria-label。
            -->
            <span :class="act.highlight ? 'inline' : 'hidden lg:inline'">{{ act.label }}</span>
          </button>
        </div>
      </div>

      <!--
        勾选后的批量动作（用户 2026-10-23：「多选歌曲后，没有出现相应的按钮」）。
        单独一行、左侧对齐：挤在第一层会和上面的批量动作抢宽度，窄屏直接被推出去。

        2026-10-25 #4：**没勾选时也常驻一条「全选当前（N）」**。以前全选只藏在这里，
        必须先勾上一行才浮出来 —— 用户进页面找不到全选按钮（Issue 原文：
        「下载里边儿增加全选功能方便操作省时间」）。勾选与否只影响后面的批量动作。
        `pt-0.5` + 负边距是为了让空状态这一行不把工具栏撑高太多。
      -->
      <div class="flex flex-wrap items-center gap-1">
        <span v-if="selectedIds.length" class="px-1 text-[11px] text-[var(--ys-text-muted)]">
          已选 <b class="text-[var(--ys-text)]">{{ selectedIds.length }}</b>
        </span>
        <button
          v-for="a in (selectedIds.length ? selectionActions : idleActions)" :key="a.key"
          type="button"
          :disabled="a.disabled"
          :class="[
            'ys-press flex items-center gap-1 rounded-full px-2.5 py-1 text-[11.5px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-35',
            a.danger
              ? 'text-rose-600 hover:bg-rose-50'
              : 'text-[var(--ys-text-muted)] hover:bg-black/[0.05] hover:text-[var(--ys-text)]',
          ]"
          :title="a.hint || a.label"
          @click="onSelectionAction(a.key)"
        >
          <component :is="a.icon" v-if="a.icon" class="h-3.5 w-3.5" :stroke-width="1.9" />
          {{ a.label }}
        </button>
        <button
          v-if="selectedIds.length"
          type="button"
          class="ys-press grid h-7 w-7 place-items-center rounded-full text-[var(--ys-text-faint)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="取消选择"
          @click="selected = new Set()"
        >
          <X class="h-3.5 w-3.5" :stroke-width="2" />
        </button>
      </div>
    </div>

  <!--
    后台能力提示（2026-10-26 用户：「后台下载组件是不是多余了」后合并进来）。

    原来这里是独立的 `<BackendDownloadPanel />` 状态条（239 行）：它把「排队 X / 下载中 Y /
    失败 Z」再数一遍（下面筛选标签已经有了）、暂停/继续按钮再摆一组（右上角已经有了）、
    展开还给一份只读任务对照（和下面列表一模一样）。三处重复，只为一个真需求：

      · 后台现在**能不能真的下**（ready）—— 洛雪宿主没开 / 没渠道时必须明说 + 原因，
        否则用户点下载，任务会一直 pending 到超时。

    那个需求一行提示就够，于是并进队列头。任务明细与全部动作只保留下面一份，沿用
    「一条调用链，不在两处各写一套按钮」的原意。
  -->
  <div
    v-if="backendNotice"
    class="flex shrink-0 items-start gap-1.5 border-b border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-4 py-2 text-[11px] leading-relaxed text-[var(--ys-text-muted)]"
  >
    <Info class="mt-[1px] h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
    <div class="min-w-0">
      <b class="text-[var(--ys-text)]">{{ backendNotice.title }}</b>
      <span class="whitespace-pre-wrap"> —— {{ backendNotice.body }}</span>
    </div>
  </div>

  <!-- 旧浏览器队列 → 后台队列的一次性迁移：逐条确认，没确认的下次再试，绝不静默丢 -->
  <div
    v-if="downloadManager.migration.value.active"
    class="flex shrink-0 items-start gap-2 border-b border-amber-100 bg-amber-50/70 px-4 py-2 text-[11.5px] leading-relaxed text-amber-700"
  >
    <RefreshCw class="mt-0.5 h-3.5 w-3.5 shrink-0 animate-spin" :stroke-width="1.9" />
    <span>
      正在把旧浏览器的下载记录迁移到 NAS 后台队列（还剩
      <b>{{ downloadManager.migration.value.remaining }}</b> / {{ downloadManager.migration.value.total }} 条）。
      已确认的才删除本地记录；提交失败的会留到下次启动继续，不会丢，也不会把已完成的历史重新下载。
    </span>
  </div>

  <!-- 4. 任务列表内容区 -->
  <div class="min-h-0 flex-1 space-y-2 overflow-y-auto bg-[#FAFAFC] p-3 sm:p-4">
    <!--
      按序号快速勾选（2026-10-23 用户：「多选框上方增加多选功能，比如输入 100 就是 0-100，
      也可以输入 11-200」）。行号 = 每行最左边那一列 #，**按当前筛选后的可见列表数**：
      筛到「失败」再输 100，选的就是失败列表里前 100 行里可勾选的那些。
    -->
    <RangeSelectBar
      v-if="filteredTasks.length"
      :total="filteredTasks.length"
      :has-selection="selectedIds.length > 0"
      :label="`按序号选（共 ${filteredTasks.length} 行可勾）`"
      @apply="onRangeApply"
      @clear="selected = new Set()"
    />

    <!-- 空状态 -->
    <div
      v-if="!filteredTasks.length"
      class="flex flex-col items-center justify-center py-24 text-center text-gray-400"
    >
      <div class="w-14 h-14 rounded-2xl bg-gray-100 text-gray-300 flex items-center justify-center mb-3">
        <Music2 class="w-7 h-7" />
      </div>
      <p class="text-xs font-medium text-gray-600 mb-1">
        {{ currentFilter === 'all' ? '暂无下载任务' : `暂无${currentFilterName}任务` }}
      </p>
      <p class="text-[11px] text-gray-400 max-w-xs">
        在「发现音乐」里点下载，任务会直接提交给 NAS 后台队列
      </p>
    </div>

    <!--
      任务卡片列表（2026-10-23 改：勾选框做成**卡片边框选中态**）

      用户原话：「多选框美化为卡片边框式的样式，要符合全局多选样式（音源页面的多选样式）」。
      音源页的选中态是 `ring-2 ring-emerald-500/40`（整张卡描一圈绿边），不是行首一个
      macOS 蓝方框 —— 那种方框既小又难瞄，一行 40px 高时点中它全靠运气。

      现在：**点整行就是勾选**，选中的行整张卡描绿边 + 极浅绿底，左边一个绿色勾。
      勾只是**状态显示**，不再是唯一的命中区（pointer-events-none），
      所以点它等于点卡片，不会出现「点了勾没反应」。
      ⚠️ 2026-10-25 #4：以前「在跑/排队的行不可选」，于是「全选」在下载中/排队时形同虚设，
      也没法批量暂停。现在**所有行都能勾**，能不能暂停/停止由选中后浮出的批量条按状态决定。
    -->
    <div
      v-for="(task, rowIndex) in filteredTasks"
      :key="task.id"
      :class="[
        'group flex items-center gap-3 rounded-[12px] border bg-white p-3 transition-all select-none cursor-pointer',
        selected.has(task.id)
          ? 'border-transparent ring-2 ring-emerald-500/40 bg-emerald-50/30'
          : 'border-[var(--ys-border)] hover:border-[var(--ys-border-strong)]',
      ]"
      :title="selected.has(task.id) ? '点击取消选中' : '点击选中这一行'"
      @click="toggleSelect(task.id)"
    >
      <!--
        行号：与上面「按序号选」的输入是同一套口径（当前可见列表里的第几行）。
        没有这一列数字，用户输 11-200 时无从对照。
      -->
      <span class="w-7 shrink-0 text-center font-mono text-[11px] text-gray-300 tabular-nums">
        {{ rowIndex + 1 }}
      </span>

      <!-- 选中状态指示：所有行都有（2026-10-25 #4 起在跑/排队的行也能勾）；绿勾 = 已选，灰圈 = 可点但未选 -->
      <span
        class="pointer-events-none grid h-4 w-4 shrink-0 place-items-center rounded-[5px] border transition-colors"
        :class="selected.has(task.id)
          ? 'border-emerald-500 bg-emerald-500 text-white'
          : 'border-[var(--ys-border-strong)] bg-white text-transparent'"
        aria-hidden="true"
      >
        <Check class="h-3 w-3" :stroke-width="3" />
      </span>

      <!-- 封面 -->
      <div class="w-10 h-10 rounded-lg overflow-hidden bg-gray-100 shrink-0 relative flex items-center justify-center shadow-2xs">
        <img
          v-if="task.song.cover"
          :src="task.song.cover"
          alt="cover"
          referrerpolicy="no-referrer"
          class="w-full h-full object-cover"
          @error="task.song.cover = ''"
        />
        <Music2 v-else class="h-4 w-4 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
      </div>

      <!-- 歌曲信息与状态 -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center justify-between gap-1 mb-0.5">
          <p class="text-xs font-semibold text-gray-800 truncate" :title="task.song.name">
            {{ task.song.name }}
          </p>
          <!-- 平台标签 -->
          <span class="text-[9px] px-1.5 py-0.2 rounded-full bg-gray-100 text-gray-500 font-mono shrink-0">
            {{ task.song.source || task.platform || 'kw' }}
          </span>
        </div>
        <p class="text-[11px] text-gray-400 truncate mb-1" :title="task.song.singer">
          {{ task.song.singer }}{{ task.song.album ? ' · ' + task.song.album : '' }}
        </p>

        <!-- 状态徽标与提示 -->
        <div class="flex items-center gap-1.5 text-[10px]">
          <!-- 后台排队中 -->
          <span
            v-if="task.status === 'pending'"
            class="px-2 py-0.5 rounded-md bg-gray-100 text-gray-500 flex items-center gap-1"
            title="已提交到 NAS 后台队列，前端关闭也会继续"
          >
            <Clock class="w-3 h-3 text-gray-400" />
            <span>后台排队中</span>
          </span>

          <!-- 后台下载中 -->
          <span
            v-else-if="task.status === 'downloading'"
            class="px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-600 flex items-center gap-1 font-medium"
            title="由 NAS 后端执行，关掉本页/浏览器也会继续"
          >
            <Loader2 class="w-3 h-3 animate-spin text-emerald-500" />
            <span>后台下载中</span>
          </span>

          <!-- 已暂停 -->
          <span
            v-else-if="task.status === 'paused'"
            class="px-2 py-0.5 rounded-md bg-amber-50 text-amber-600 flex items-center gap-1 font-medium"
          >
            <Pause class="w-3 h-3 text-amber-500" />
            <span>已暂停</span>
          </span>

          <!-- 下载成功 -->
          <span
            v-else-if="task.status === 'success'"
            class="px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-600 flex items-center gap-1 font-medium"
          >
            <Check class="w-3 h-3 text-emerald-500" />
            <span>已保存至 NAS</span>
          </span>

          <!--
            下载失败：列表里只给结论「下载失败」，具体原因放在 title 悬停可见，
            同时已由 downloadManager 写进日志（见 services/appLog.js 的约定）。
          -->
          <span
            v-else-if="task.status === 'failed'"
            class="px-2 py-0.5 rounded-md bg-rose-50 text-rose-600 flex items-center gap-1"
            :title="task.error || '下载失败'"
          >
            <AlertCircle class="w-3 h-3 text-rose-500 shrink-0" />
            <span>下载失败</span>
          </span>

          <!--
            已停止：点过「停止全部」/ 订阅行「停止」后留下的任务。
            ⚠️ 这一档以前**连状态文字都没有**，右侧也没有任何按钮 ——
            用户看得到任务却动不了（2026-09-22 反馈：「下载-停止后，已停止增加恢复下载」）。
          -->
          <span
            v-else-if="task.status === 'stopped'"
            class="px-2 py-0.5 rounded-md bg-gray-100 text-gray-500 flex items-center gap-1"
            :title="task.error || '已被用户停止'"
          >
            <Square class="w-3 h-3 text-gray-400" />
            <span>已停止</span>
          </span>
        </div>

        <!-- 音质 / 实际音源 / 重试次数 -->
        <div class="flex items-center gap-1.5 mt-1 text-[10px] flex-wrap">
          <!--
            音质标签（2026-10-23 修「下载队列显示音质与实际音质不符」）。

            以前这里只显示**请求的档位**：用户选了「最高音质」，这一格就写成「最高音质」，
            而盘上那份可能只有 128k —— 用户看到的就是「显示的音质和实际不符」。

            现在按事实分层显示：
              · 用户请求 `highest` 时，绝不把 `resolvedQuality` 顶上来冒充档位
                （`resolvedQuality` 是**渠道推出来的**档位，渠道常把它说高）；
              · 任务还没落地 → 「最高音质」+「待取」；
              · 已落地 → 「最高音质」+ **实际容器**（后端按魔数嗅探，是权威结论）；
              · 请求的是固定档位且与盘上一致 → 只显示该档位（不必啰嗦）。
          -->
          <span class="px-1.5 py-0.5 rounded bg-emerald-50 text-emerald-600 font-mono">
            {{ downloadManager.qualityLabel(task.quality) || '最高音质' }}
          </span>
          <span
            v-if="task.resolvedQuality && task.resolvedQuality !== task.quality"
            class="px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 font-mono"
            :title="'后台实际拿到的音质/容器'"
          >
            实际 {{ task.resolvedQuality }}
          </span>
          <span
            v-if="task.resolvedSource"
            class="px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 truncate max-w-[130px]"
            :title="'实际取链渠道：' + task.resolvedSource"
          >
            {{ task.resolvedSource }}
          </span>
          <span v-if="task.attempts > 0" class="text-gray-400">
            重试 {{ task.attempts }} 次
          </span>
          <span v-if="task.path" class="text-gray-400 truncate max-w-[220px]" :title="task.path">{{ task.path }}</span>
        </div>
      </div>

      <!-- 右侧操作按钮 -->
      <div class="shrink-0 flex items-center gap-1">
        <!-- 排队或下载中：暂停按钮 -->
        <button
          v-if="task.status === 'pending' || task.status === 'downloading'"
          @click.stop="downloadManager.pauseTask(task.id)"
          class="p-1.5 rounded-lg bg-amber-50 hover:bg-amber-100 text-amber-600 transition-colors"
          title="暂停下载"
        >
          <Pause class="w-3.5 h-3.5" />
        </button>

        <!-- 已暂停：继续按钮 -->
        <button
          v-if="task.status === 'paused'"
          @click.stop="downloadManager.resumeTask(task.id)"
          class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 transition-colors"
          title="继续下载"
        >
          <PlayCircle class="w-3.5 h-3.5" :stroke-width="1.9" />
        </button>

        <!--
          已停止：恢复下载。
          `resumeTask` 会复位 `_aborted`（否则 `executeTask` 一进去就 return），
          所以这里直接调它即可，service 层不需要改。
        -->
        <button
          v-if="task.status === 'stopped'"
          @click.stop="downloadManager.resumeTask(task.id)"
          class="px-2 py-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 text-[10px] font-medium transition-colors flex items-center gap-1"
          title="恢复下载（重新排队并继续）"
        >
          <PlayCircle class="w-3 h-3" :stroke-width="1.9" />
          <span>恢复下载</span>
        </button>

        <!-- 排队中/下载中/已暂停：取消并停止按钮 -->
        <button
          v-if="task.status === 'pending' || task.status === 'downloading' || task.status === 'paused'"
          @click.stop="downloadManager.cancelTask(task.id)"
          class="p-1.5 rounded-lg text-gray-400 hover:text-rose-600 hover:bg-rose-50 transition-colors"
          title="停止并取消此任务"
        >
          <Square class="w-3.5 h-3.5" />
        </button>

        <!-- 失败任务：选音质后重试；渠道由后台按顺序自动选，前端不提供换源 -->
        <template v-if="task.status === 'failed'">
          <SelectField
            compact
            class="w-[104px]"
            :model-value="task.quality"
            :options="downloadManager.QUALITY_OPTIONS"
            hint="为该任务单独指定音质后重试"
            @update:model-value="v => onTaskQualityPick(task, v)"
          />

          <button
            @click.stop="downloadManager.retryTask(task.id)"
            class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 transition-colors"
            title="重新下载"
          >
            <RotateCw class="w-3.5 h-3.5" />
          </button>
        </template>

        <!-- 已完成：用当前默认音质再下一次 -->
        <button
          v-if="task.status === 'success'"
          @click.stop="downloadManager.redownloadTasksByIds([task.id])"
          class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-100 text-emerald-600 transition-colors"
          title="重新下载（用当前默认音质再入队一次）"
        >
          <RotateCw class="w-3.5 h-3.5" />
        </button>

        <!-- 移除记录按钮 (已完成 / 失败 / 已停止) -->
        <button
          v-if="task.status === 'success' || task.status === 'failed' || task.status === 'stopped'"
          @click.stop="downloadManager.removeTask(task.id)"
          class="p-1.5 rounded-lg text-gray-300 hover:text-gray-500 hover:bg-gray-100 transition-colors"
          title="移除记录"
        >
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  </div>
  </div>

  <!--
    勾选了任务才浮出的操作条：清除选中 / 全选当前筛选。

    ⚠️ 这一条必须留在任何弹窗外面。以前它被写在 `Teleport + v-if` 的弹窗里，
    于是只有在那个弹窗打开时才存在 —— 平时勾了行什么都不浮出来，
    看起来就像「多选失效了」（用户 2026-10-23：「多选框上方增加多选功能……这些都失效了」）。
  -->
  <SelectionBar
    :count="selectedIds.length"
    :actions="selectionActions"
    @action="onSelectionAction"
    @clear="selected = new Set()"
  />

</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { downloadManager } from '../services/downloadManager'
import SelectField from './SelectField.vue'
import { backendDownload } from '../services/backendDownload.js'
import SelectionBar from './SelectionBar.vue'
import RangeSelectBar from './RangeSelectBar.vue'
import { pickByIndexes } from '../services/rangeSelect'
import { ok as notifyOk, warn as notifyWarn } from '../services/feedback'
import {
  X, RotateCw, Trash2, Check,
  AlertCircle, Loader2, Music2, Clock, Pause, PlayCircle, Square,
  ListChecks, Eraser, RotateCcw, Info
} from 'lucide-vue-next'

const props = defineProps({
  activeSource: Object
})

/**
 * 后台能力提示：只在前端**得说话**的时候才显示（正常情况下整行不占位）。
 *
 * 优先级与原状态条一致：接口没有 > 能力不可用 > 请求报错。
 * 「就绪」这件事不再写进界面 —— 队列能显示出来、下载能入队，本身就是就绪的证据，
 * 单独挂一个绿色「就绪」胶囊属于自证清白式的噪音。
 *
 * 挂载即探一次能力、卸载时归还共享轮询的引用计数（原 BackendDownloadPanel 负责的
 * 两件事，组件删了不能丢，否则队列计数只在别处挂着时才准）。
 */
const backendNotice = computed(() => {
  const { available, ready, reason, errorText } = backendDownload
  if (available.value === false) {
    return {
      title: '当前后端版本没有后台下载接口',
      body: `${reason.value || '升级后端后就能用'}；现在点下载会失败，请先升级。`,
    }
  }
  if (ready.value === false) {
    return {
      title: '后台下载当前不可用',
      body: `${reason.value || '后端没有可用的取音渠道'}`,
    }
  }
  if (errorText.value) return { title: '后台状态没取到', body: errorText.value }
  return null
})

onMounted(() => backendDownload.start())
onUnmounted(() => backendDownload.stop())

/**
 * 2026-10-25 用户：「下载-队列-去掉目录选择。」
 * → 工具栏里那条只读路径条也一起撤了（模板已无引用），落盘目录只在
 *   「系统 → 设置 → 存储目录」修改。原来这里还有个 `const backendDir = backendDownload.backendDir`，
 *   现在模板不用了，留着就是未使用变量，一并删掉。
 */

/**
 * ⚠️ `<script setup>` 里，props 只有**在模板中**才能当裸变量用（编译器会把模板里的
 * `activeSource` 变成 `__props.activeSource`）。普通函数里必须写 `props.activeSource`。
 * 以前 `onQueueMenu` 直接写 `activeSource?.id`，点「全部恢复 / 重试失败」时抛
 * `ReferenceError: activeSource is not defined`，两个按钮就这么死了
 * （用户 2026-10-23：「全部恢复和重试失败等这个页面的。这些都失效了」）。
 * 统一走下面这个取值函数，别再在脚本里裸写。
 */
// 下载设置面板默认折叠，避免一屏挤满控件

/**
 * 队列此刻该「继续」还是「暂停」。
 *
 * 只要队列被暂停、或者有任何一条任务处于暂停态，下一步该做的就是继续；
 * 否则就是暂停队列（有在跑的任务时）。两者都不成立（队列空且没暂停任务）
 * 时仍显示暂停图标并禁用语义更含糊，所以这里退化为「播放」——按下会调
 * resumeAll()，后端无事可做也不会报错。
 */
const queueNeedsResume = computed(
  () => downloadManager.queuePaused.value || downloadManager.pausedCount.value > 0,
)

function onToggleQueue() {
  if (queueNeedsResume.value) downloadManager.resumeAll()
  else downloadManager.pauseAll()
}

/**
 * 队列页工具栏的四个批量动作（2026-10-25 从「⋯」里移出来，见模板注释）。
 *
 * 判定标准不变：这些动作**不需要先勾选**、也不是此刻正在发生的事。
 * 数量为 0 时禁用而不是隐藏 —— 按钮位置固定，用户才不会觉得「少了一项」。
 */
const queueActions = computed(() => [
  { id: 'resumeStopped', label: '全部恢复', icon: RotateCw, disabled: stoppedCount.value === 0, hint: `继续 ${stoppedCount.value} 首已停止的任务（重新排队）` },
  {
    id: 'retryFailed',
    // Issue #8：用户要的是「一键重试」**能看见**。失败时标签直接带数字，
    // 不再只靠悬停提示；同时加 emerald 高亮，与旁边灰按钮区分开。
    label: downloadManager.failedCount.value > 0 ? `重试失败（${downloadManager.failedCount.value}）` : '重试失败',
    icon: RotateCcw,
    highlight: downloadManager.failedCount.value > 0,
    disabled: downloadManager.failedCount.value === 0,
    hint: downloadManager.failedCount.value > 0
      ? `${downloadManager.failedCount.value} 首失败，点一下全部重新排队`
      : '没有失败的任务',
  },
  { id: 'stopAll', label: '停止全部', icon: Square, danger: true, disabled: downloadManager.activeCount.value + downloadManager.pausedCount.value === 0, hint: '停掉正在下载与排队中的任务（已完成的不受影响）' },
  { id: 'clearRecords', label: '清除记录', icon: Eraser, danger: true, disabled: terminalCount.value === 0, hint: `清除已完成 / 失败 / 已停止的 ${terminalCount.value} 条记录` },
])

/**
 * ⋯ 菜单里的批量动作。
 *
 * 每个动作都要**当场回话**：返回的 hit 是后端真动了几条。以前这里只有异常分支
 * 才提示，正常但「一条都没动」时界面毫无反应 —— 用户就会觉得按钮又失效了
 * （2026-10-24：「下载里的重试失败又失效了吗」）。
 */
async function onQueueMenu(key) {
  try {
    if (key === 'resumeStopped') {
      const n = await downloadManager.resumeStopped()
      if (n > 0) notifyOk(`已恢复 ${n} 首重新排队`)
      else notifyWarn('没有可恢复的任务（已结束的请用「重新下载」）')
    } else if (key === 'retryFailed') {
      // Issue #8：用户在问「一键重试好了吗」—— 所以这里必须把结果说清楚，
      // 包括「几首里重试了几首、剩下的为什么没动」，不能只丢一句「已重试 N 首」。
      const before = downloadManager.failedCount.value
      const n = await downloadManager.retryAllFailed()
      if (n > 0) notifyOk(`已把 ${n} 首重新排进队列（原失败 ${before} 首）`)
      else notifyWarn(before > 0 ? `有 ${before} 首失败，但后端一条都没接（稍后再试或看日志）` : '没有失败任务可重试')
    } else if (key === 'stopAll') {
      const n = await downloadManager.stopAll()
      if (n > 0) notifyOk(`已停止 ${n} 首`)
      else notifyWarn('当前没有在跑或排队的任务')
    } else if (key === 'clearRecords') {
      await clearAllRecords()
    }
  } catch (e) {
    notifyWarn(`操作失败：${e?.userMessage || e?.message || '后台没有响应'}`)
  }
}

/** 为单个失败任务指定音质并立即重试（SelectField 直接给出值，不再是 DOM event） */
function onTaskQualityPick(task, quality) {
  if (!quality) return
  downloadManager.setTaskQuality(task.id, quality)
}

// ── 多选与清除记录 ──
//
// 「已结束」= 记录可以清掉的那些。它现在只剩「一键清除记录」这一个用途。
//
// ⚠️ 2026-10-25 #4 修正：它**不再**决定「哪些行能勾」。以前能勾的前提是 isTerminal，
// 于是「下载中 / 后台排队中」的行根本没有勾选框 —— 用户想批量暂停一批正在下的歌，
// 只能一首一首点右侧小按钮，「全选」在那些筛选下永远是灰的（Issue #4）。现在所有行都能勾，
// 批量条按选中行的实际状态给出「暂停选中 / 继续 / 停止选中 / 删除记录」。
function isTerminal(status) {
  return status === 'success' || status === 'failed' || status === 'stopped'
}

/** 还没结束的行（排队 / 下载中 / 已暂停）—— 它们能批量暂停、停止。 */
function isActive(status) {
  return status === 'pending' || status === 'downloading' || status === 'paused'
}

/** 勾选的 id。用 Set 而不是数组：勾/取消是 O(1)，且天然去重。 */
const selected = ref(new Set())
const selectedIds = computed(() => Array.from(selected.value))
/** 当前筛选下所有行 —— 「全选」圈的就是这一批（Issue #4：以前只圈已结束的，等于没用）。 */
const allSelected = computed(() =>
  filteredTasks.value.length > 0 && filteredTasks.value.every(t => selected.value.has(t.id)))
/** 全部任务里已结束的条数（决定「一键清除记录」显不显示 —— 没有可清的就不摆按钮） */
const terminalCount = computed(() => downloadManager.tasks.value.filter(t => isTerminal(t.status)).length)

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleSelectAll() {
  selected.value = allSelected.value
    ? new Set()
    : new Set(filteredTasks.value.map(t => t.id))
}

/**
 * 按序号区间勾选（2026-10-23 新增，解析规则见 services/rangeSelect.js）。
 *
 * 语义是**替换**：一次输入 = 一次把「我要的这些行」说清楚（`1-10,80` 一次就能填完），
 * 而不是往已有选择上叠加 —— 叠加的话用户填完 100 再填 11-200，会得到 1-200，
 * 而他想要的其实是「只有 11-200」。想清空重来用右侧「清空选择」。
 *
 * 2026-10-25 #4：**所有行都能勾了**（以前只圈已结束的，正在下的圈不上）。
 * 所以这里不再需要「摘掉不可勾的那些」，也不再需要那句「另有 N 行不能勾」的提示。
 */
function onRangeApply(indexes) {
  const rows = pickByIndexes(filteredTasks.value, indexes)
  if (!rows.length) {
    notifyWarn('没有命中任何行（序号从 1 开始，数的是当前筛选下可见的行）')
    return
  }
  selected.value = new Set(rows.map(t => t.id))
  notifyOk(`已选中 ${rows.length} 行`)
}

async function clearSelected() {
  const n = selectedIds.value.length
  await downloadManager.removeTasks(selectedIds.value)
  selected.value = new Set()
  notifyOk(`已从队列移除 ${n} 条记录（NAS 上的文件未删除）`)
}

/**
 * 勾选后浮出的操作条：「按序号选」框住的这一段，用户接下来想干什么。
 *
 * 只有真的存在可做的事才摆按钮（失败数为 0 就不给「重试选中」），
 * 否则用户点下去得到一个「0 首」的提示，会以为功能又坏了 ——
 * 这个页面刚因为按钮点了没反应被投诉过一次。
 */
const selectedTasks = computed(() => downloadManager.tasks.value.filter(t => selected.value.has(t.id)))
const selectedFailedCount = computed(() => selectedTasks.value.filter(t => t.status === 'failed').length)
const selectedResumableCount = computed(() =>
  selectedTasks.value.filter(t => t.status === 'stopped' || t.status === 'paused').length)

const selectedDoneCount = computed(() => selectedTasks.value.filter(t => t.status === 'success').length)
const selectedRetryableCount = computed(() => selectedFailedCount.value + selectedDoneCount.value)
/** 选中里「还在队列中、能暂停/停止」的条数（Issue #4：以前这些行勾不上，也就没有批量暂停）。 */
const selectedActiveCount = computed(() => selectedTasks.value.filter(t => isActive(t.status)).length)

const selectionActions = computed(() => {
  const acts = []
  // 批量暂停放在最前：Issue #4 里用户真正的需求就是「一次暂停一批正在下的」。
  // 后端 pause 带 ids 时只动这几条，不影响整队开关。
  if (selectedActiveCount.value > 0) {
    acts.push({
      key: 'pause',
      label: `暂停选中（${selectedActiveCount.value}）`,
      icon: Pause,
      hint: '把选中的排队中 / 下载中 / 已暂停任务暂停（已暂停的保持不动）',
    })
  }
  // 「继续」覆盖在跑之外的一切（失败要重试、停止要恢复），所以它是第一条
  if (selectedRetryableCount.value > 0 || selectedResumableCount.value > 0) {
    acts.push({
      key: 'continue',
      label: '继续',
      icon: PlayCircle,
      hint: '选中的任务重新排队：失败的会重试、停止/暂停的会接着下',
    })
  }
  if (selectedDoneCount.value > 0) {
    acts.push({
      key: 'redownload',
      label: `重新下载（${selectedDoneCount.value}）`,
      icon: RotateCw,
      // 这条的用处是把「当初只拿到 128k」的那批用当前策略再要一遍
      hint: '把选中的已完成任务重新下一次（盘上那份不会被更差的覆盖）',
    })
  }
  if (selectedActiveCount.value > 0) {
    acts.push({
      key: 'stop',
      label: `停止选中（${selectedActiveCount.value}）`,
      icon: Square,
      danger: true,
      hint: '停掉选中的排队中 / 下载中 / 已暂停任务（已完成的不受影响）',
    })
  }
  // 全选口径 2026-10-25 改：圈「当前筛选下的全部行」，不只是已结束的。
  acts.push({
    key: 'all',
    label: allSelected.value ? '取消全选' : `全选当前（${filteredTasks.value.length}）`,
    icon: ListChecks,
    hint: '把当前筛选下的所有行都勾上（含下载中 / 排队中），再点一次取消',
  })
  acts.push({ key: 'clear', label: `删除记录（${selectedIds.value.length}）`, icon: Trash2, danger: true, hint: '只从队列里移除记录，不删 NAS 上的文件' })
  return acts
})

/**
 * 没勾选时那一行只摆一个「全选当前（N）」—— 全选不该藏在勾选之后才出现。
 * 行数为 0 时禁用（列表空着，全选没有意义），但**仍占位**，避免它忽隐忽现。
 */
const idleActions = computed(() => [
  {
    key: 'all',
    label: allSelected.value
      ? '取消全选'
      : `全选当前（${filteredTasks.value.length}）`,
    icon: ListChecks,
    disabled: filteredTasks.value.length === 0,
    hint: '把当前筛选下的所有行都勾上（含下载中 / 排队中），再点一次取消',
  },
])

async function onSelectionAction(key) {
  try {
    await runSelectionAction(key)
  } catch (e) {
    notifyWarn(`操作失败：${e?.userMessage || e?.message || '后台没有响应'}`)
  }
}

async function runSelectionAction(key) {
  if (key === 'clear') return clearSelected()
  if (key === 'all') return toggleSelectAll()
  if (key === 'pause') {
    const ids = selectedTasks.value.filter(t => t.status === 'pending' || t.status === 'downloading').map(t => t.id)
    if (!ids.length) {
      notifyWarn('选中的任务里没有正在下载或排队中的（已暂停的保持不动）')
      return
    }
    const n = await downloadManager.pauseTasksByIds(ids)
    notifyOk(n > 0 ? `已暂停 ${n} 首` : '没有可暂停的任务')
    return
  }
  if (key === 'stop') {
    const ids = selectedTasks.value.filter(t => isActive(t.status)).map(t => t.id)
    if (!ids.length) {
      notifyWarn('选中的任务里没有可停止的（已完成 / 失败 / 已停止的不受影响）')
      return
    }
    const n = await downloadManager.stopTasksByIds(ids)
    selected.value = new Set()
    notifyOk(n > 0 ? `已停止 ${n} 首` : '没有可停止的任务')
    return
  }
  if (key === 'continue') {
    // 一个「继续」把两档都管了：失败的要重试、停止/暂停的要接着下。
    // 用户勾了一段按「继续」，不该还要先分辨哪几行当初是被停掉的。
    const retried = await downloadManager.retryTasksByIds(selectedIds.value)
    const resumed = await downloadManager.resumeTasksByIds(selectedIds.value)
    selected.value = new Set()
    const n = (retried || 0) + (resumed || 0)
    if (n > 0) notifyOk(`已重新排队 ${n} 首${retried && resumed ? `（重试 ${retried}、恢复 ${resumed}）` : ''}`)
    else notifyWarn('选中的行里没有可继续的任务（已完成的请用「重新下载」）')
    return
  }
  if (key === 'redownload') {
    const n = await downloadManager.redownloadTasksByIds(selectedIds.value)
    selected.value = new Set()
    if (n > 0) notifyOk(`已把 ${n} 首重新排进队列`)
    else notifyWarn('选中的行里没有已完成的任务')
  }
}

/**
 * 一键清除记录。问一句 `confirm`：清记录不可撤销，而它与「清除选中」挨着放，
 * 误点一下就全没了 —— 确认是**界面**的责任（service 只管做）。
 */
async function clearAllRecords() {
  const n = terminalCount.value
  if (!n) return
  if (!confirm(`确定清除 ${n} 条已结束的记录？（正在下载与排队中的不受影响）`)) return
  await downloadManager.clearRecords()
  selected.value = new Set()
  notifyOk(`已清除 ${n} 条记录`)
}

const currentFilter = ref('all') // 'all' | 'downloading' | 'paused' | 'completed' | 'failed'

const filterTabs = computed(() => [
  { id: 'all', name: '全部', count: downloadManager.totalCount.value },
  { id: 'downloading', name: '下载中', count: downloadManager.activeCount.value },
  { id: 'paused', name: '已暂停', count: downloadManager.pausedCount.value },
  { id: 'stopped', name: '已停止', count: stoppedCount.value },
  { id: 'completed', name: '已完成', count: downloadManager.completedCount.value },
  { id: 'failed', name: '失败', count: downloadManager.failedCount.value },
])

const stoppedCount = computed(() =>
  downloadManager.tasks.value.filter(t => t.status === 'stopped').length
)

const currentFilterName = computed(() => {
  const match = filterTabs.value.find(t => t.id === currentFilter.value)
  return match ? match.name : ''
})

const filteredTasks = computed(() => {
  const list = downloadManager.tasks.value
  // 2026-10-26 用户口径：下载队列「新的在最上方」。tasks 仍保持后端到达顺序
  // （那才是实际执行顺序，别动），这里只改**显示**排序：按加入时间倒序，
  // 同批加入（addedAt 相同）的保持原相对顺序 —— 整单批量下载不会乱跳。
  const order = list.map((t, i) => ({ t, i }))
    .sort((a, b) => ((b.t.addedAt || 0) - (a.t.addedAt || 0)) || (a.i - b.i))
    .map((x) => x.t)
  if (currentFilter.value === 'downloading') {
    return order.filter(t => t.status === 'pending' || t.status === 'downloading')
  }
  if (currentFilter.value === 'paused') {
    return order.filter(t => t.status === 'paused')
  }
  if (currentFilter.value === 'stopped') {
    return order.filter(t => t.status === 'stopped')
  }
  if (currentFilter.value === 'completed') {
    return order.filter(t => t.status === 'success')
  }
  if (currentFilter.value === 'failed') {
    return order.filter(t => t.status === 'failed')
  }
  return order
})
</script>
