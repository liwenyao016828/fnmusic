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
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b border-[var(--ys-border)] bg-white px-4 py-2.5">
      <button
        type="button"
        class="flex min-w-0 max-w-[340px] items-center gap-1.5 rounded-[8px] px-1.5 py-1 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
        :title="`存储位置：${downloadManager.currentDownloadDir.value}（点击更改）`"
        @click="downloadManager.openDirModal()"
      >
        <Folder class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-faint)]" :stroke-width="1.8" />
        <span class="truncate font-mono text-[11px]">{{ downloadManager.currentDownloadDir.value }}</span>
      </button>

      <!-- 筛选：分段控件（沉底轨道 + 白色药丸），与全站其它分段同款 -->
      <div class="flex min-w-0 flex-wrap items-center gap-0.5 rounded-[10px] bg-[var(--ys-surface-sunken)] p-1">
        <button
          v-for="tab in filterTabs" :key="tab.id"
          @click="currentFilter = tab.id"
          :class="[
            'ys-press whitespace-nowrap rounded-[8px] px-2.5 py-1 text-[11.5px] font-medium transition-colors',
            currentFilter === tab.id
              ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
              : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]',
          ]"
        >
          {{ tab.name }}
          <span class="ml-0.5 text-[10px] opacity-60">{{ tab.count }}</span>
        </button>
      </div>

      <!--
        勾选后的批量动作（用户 2026-10-23：「多选歌曲后，没有出现相应的按钮，比如继续、
        重新下载、删除记录等等，可以放在右上角」）。

        放在工具栏右上角而不是只靠底部浮条：浮条是「选中了才浮出来」的全局约定，
        但它离行远，批量动作挤在一个胶囊里也放不下四五个。这里用**文字按钮**，
        而且只在选中的任务里确实存在那种状态时才出现（没选失败就不摆「重新下载」）——
        点了得到「0 首」的反馈，和按钮坏了没区别。
      -->
      <div v-if="selectedIds.length" class="ml-auto flex flex-wrap items-center gap-1">
        <span class="px-1 text-[11px] text-[var(--ys-text-muted)]">
          已选 <b class="text-[var(--ys-text)]">{{ selectedIds.length }}</b>
        </span>
        <button
          v-for="a in selectionActions" :key="a.key"
          type="button"
          :class="[
            'ys-press flex items-center gap-1 rounded-full px-2.5 py-1 text-[11.5px] font-medium transition-colors',
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
          type="button"
          class="ys-press grid h-7 w-7 place-items-center rounded-full text-[var(--ys-text-faint)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="取消选择"
          @click="selected = new Set()"
        >
          <X class="h-3.5 w-3.5" :stroke-width="2" />
        </button>
      </div>

      <div class="ml-auto flex items-center gap-1" :class="selectedIds.length ? 'ml-0' : ''">
        <!-- 这两个是「此刻真能做的事」：暂停/继续，图标化后不占文字空间 -->
        <button
          v-if="downloadManager.queuePaused.value"
          @click="downloadManager.resumeAll()"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="后台队列已暂停，点此继续"
        >
          <Play class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <button
          v-else-if="downloadManager.activeCount.value > 0"
          @click="downloadManager.pauseAll()"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="暂停后台队列（不会删任务）"
        >
          <Pause class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <button
          v-if="downloadManager.pausedCount.value > 0"
          @click="downloadManager.resumeAll()"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="继续所有已暂停的任务（并恢复后台队列）"
        >
          <Play class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <!--
          下载设置从 ⋯ 里搬了出来（用户 2026-10-23：「下载列表的下载设置移出来」）。
          ⋯ 里放的是「偶尔用一次的批量动作」，而这一项是**这一页的策略旋钮**
          （新任务的默认音质）—— 藏在 ⋯ 里，用户会以为默认策略改不了。
          摆成图标而不是文字，是为了不把这条 40px 的行重新塞满。
        -->
        <button
          type="button"
          class="ys-press grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
          title="下载设置（新任务的默认音质）"
          @click="showSettings = true"
        >
          <Settings2 class="h-4 w-4" :stroke-width="1.9" />
        </button>
        <OverflowMenu :items="queueMenu" @select="onQueueMenu" />
      </div>
    </div>

  <!--
    后台下载状态条（NAS 端执行）。下载是后台做的，这里只如实报告「能不能下 / 在跑什么」，
    任务明细与全部动作在下面的列表里（一条调用链，不在两处各写一套按钮）。
  -->
  <BackendDownloadPanel />

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
      :label="`按序号选（共 ${terminalTasks.length} 行可勾）`"
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
      在跑/排队的行不可选，它们的卡片保持原样（没有勾、也不会被点选中）。
    -->
    <div
      v-for="(task, rowIndex) in filteredTasks"
      :key="task.id"
      :class="[
        'group flex items-center gap-3 rounded-[12px] border bg-white p-3 transition-all',
        isTerminal(task.status) ? 'cursor-pointer select-none' : '',
        selected.has(task.id)
          ? 'border-transparent ring-2 ring-emerald-500/40 bg-emerald-50/30'
          : 'border-[var(--ys-border)] hover:border-[var(--ys-border-strong)]',
      ]"
      :title="isTerminal(task.status) ? (selected.has(task.id) ? '点击取消选中' : '点击选中这一行') : ''"
      @click="isTerminal(task.status) && toggleSelect(task.id)"
    >
      <!--
        行号：与上面「按序号选」的输入是同一套口径（当前可见列表里的第几行）。
        没有这一列数字，用户输 11-200 时无从对照。
      -->
      <span class="w-7 shrink-0 text-center font-mono text-[11px] text-gray-300 tabular-nums">
        {{ rowIndex + 1 }}
      </span>

      <!-- 选中状态指示：已结束的行才有；绿勾 = 已选，灰圈 = 可点但未选 -->
      <span
        v-if="isTerminal(task.status)"
        class="pointer-events-none grid h-4 w-4 shrink-0 place-items-center rounded-[5px] border transition-colors"
        :class="selected.has(task.id)
          ? 'border-emerald-500 bg-emerald-500 text-white'
          : 'border-[var(--ys-border-strong)] bg-white text-transparent'"
        aria-hidden="true"
      >
        <Check class="h-3 w-3" :stroke-width="3" />
      </span>
      <!-- 不可勾的行也要占住这一格，否则整列会左右错开 -->
      <span v-else class="w-4 shrink-0" aria-hidden="true"></span>

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
          <Play class="w-3.5 h-3.5" />
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
          <Play class="w-3 h-3" />
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

<!-- ── 下载设置弹窗（入口：工具栏右侧的齿轮按钮） ── -->
<Teleport to="body">
  <div
    v-if="showSettings"
    class="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/50 backdrop-blur-xs"
    @click.self="showSettings = false"
  >
    <div class="max-h-[85vh] w-full max-w-[560px] overflow-y-auto rounded-2xl bg-white shadow-2xl">
      <div class="flex items-center justify-between border-b border-gray-100 px-5 py-3.5">
        <h3 class="text-sm font-bold text-gray-800 flex items-center gap-1.5">
          <Settings2 class="w-4 h-4 text-emerald-500" />
          <span>下载设置</span>
        </h3>
        <button class="text-gray-400 hover:text-gray-600" @click="showSettings = false">
          <X class="w-4 h-4" />
        </button>
      </div>

      <div class="space-y-4 px-5 py-4">
        <SelectField
          v-model="qualityModel"
          label="默认音质"
          :options="downloadManager.QUALITY_OPTIONS"
          hint="只影响新提交的任务；已在后台队列里的任务按当初的档位走"
        />
        <p class="text-[11px] leading-relaxed text-gray-400">
          下载由 NAS 后台队列统一调度：并发、重试和取音渠道都由后端策略决定，
          不再由浏览器逐首执行。这里只保留「新任务要什么音质」。
        </p>
      </div>

    </div>
  </div>
</Teleport>

  <!--
    勾选了任务才浮出的操作条：清除选中 / 全选当前筛选。

    ⚠️ 这一条**必须留在「下载设置」弹窗外面**。以前它被写在
    `<Teleport><div v-if="showSettings">` 里面，于是只有在设置弹窗打开时
    才存在 —— 平时勾了行什么都不浮出来，看起来就像「多选失效了」
    （用户 2026-10-23：「多选框上方增加多选功能……这些都失效了」）。
  -->
  <SelectionBar
    :count="selectedIds.length"
    :actions="selectionActions"
    @action="onSelectionAction"
    @clear="selected = new Set()"
  />

</template>

<script setup>
import { ref, computed } from 'vue'
import { downloadManager } from '../services/downloadManager'
import SelectField from './SelectField.vue'
import OverflowMenu from './OverflowMenu.vue'
import SelectionBar from './SelectionBar.vue'
import RangeSelectBar from './RangeSelectBar.vue'
import BackendDownloadPanel from './BackendDownloadPanel.vue'
import { pickByIndexes } from '../services/rangeSelect'
import { ok as notifyOk, warn as notifyWarn } from '../services/feedback'
import {
  X, Folder, RotateCw, Trash2, Check,
  AlertCircle, Loader2, Music2, Clock, Pause, Play, Square,
  Settings2, ListChecks, Eraser, RotateCcw
} from 'lucide-vue-next'

const props = defineProps({
  activeSource: Object
})

/**
 * ⚠️ `<script setup>` 里，props 只有**在模板中**才能当裸变量用（编译器会把模板里的
 * `activeSource` 变成 `__props.activeSource`）。普通函数里必须写 `props.activeSource`。
 * 以前 `onQueueMenu` 直接写 `activeSource?.id`，点「全部恢复 / 重试失败」时抛
 * `ReferenceError: activeSource is not defined`，两个按钮就这么死了
 * （用户 2026-10-23：「全部恢复和重试失败等这个页面的。这些都失效了」）。
 * 统一走下面这个取值函数，别再在脚本里裸写。
 */
// 下载设置面板默认折叠，避免一屏挤满控件
const showSettings = ref(false)

/**
 * 队列页右上角 ⋯。
 * 判定标准：这个动作**不需要先勾选**、也不是此刻正在发生的事 → 进菜单。
 * 「全部恢复 / 重试失败 / 停止全部」三个批量动作只在对应状态存在时才出现。
 */
const queueMenu = computed(() => [
  { key: 'resumeStopped', label: '全部恢复', icon: Play, disabled: stoppedCount.value === 0, hint: `${stoppedCount.value} 首已停止` },
  { key: 'retryFailed', label: '重试失败', icon: RotateCcw, disabled: downloadManager.failedCount.value === 0, hint: `${downloadManager.failedCount.value} 首失败` },
  { key: 'stopAll', label: '停止全部', icon: Square, danger: true, disabled: downloadManager.activeCount.value + downloadManager.pausedCount.value === 0 },
  { key: 'clearRecords', label: '清除所有记录', icon: Eraser, danger: true, disabled: terminalCount.value === 0, hint: '已完成 / 失败 / 已停止' },
])

async function onQueueMenu(key) {
  try {
    if (key === 'resumeStopped') await downloadManager.resumeStopped()
    else if (key === 'retryFailed') await downloadManager.retryAllFailed()
    else if (key === 'stopAll') await downloadManager.stopAll()
    else if (key === 'clearRecords') await clearAllRecords()
  } catch (e) {
    notifyWarn(`操作失败：${e?.userMessage || e?.message || '后台没有响应'}`)
  }
}

// ── 下载策略设置的读写模型 ──
const qualityModel = computed({
  get: () => downloadManager.defaultQuality.value,
  set: v => downloadManager.updateSettings({ defaultQuality: v }),
})
/** 为单个失败任务指定音质并立即重试（SelectField 直接给出值，不再是 DOM event） */
function onTaskQualityPick(task, quality) {
  if (!quality) return
  downloadManager.setTaskQuality(task.id, quality)
}

// ── 多选与清除记录 ──
//
// 「已结束」= 记录可以清掉的那些。它同时决定三件事：勾选框出现在哪些行上、
// 「全选」圈哪些、「一键清除记录」清哪些 —— 一处定义，三处共用。
function isTerminal(status) {
  return status === 'success' || status === 'failed' || status === 'stopped'
}

/** 勾选的 id。用 Set 而不是数组：勾/取消是 O(1)，且天然去重。 */
const selected = ref(new Set())
const selectedIds = computed(() => Array.from(selected.value))
const terminalTasks = computed(() => filteredTasks.value.filter(t => isTerminal(t.status)))
const allSelected = computed(() =>
  terminalTasks.value.length > 0 && terminalTasks.value.every(t => selected.value.has(t.id)))
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
    : new Set(terminalTasks.value.map(t => t.id))
}

/**
 * 按序号区间勾选（2026-10-23 新增，解析规则见 services/rangeSelect.js）。
 *
 * 语义是**替换**：一次输入 = 一次把「我要的这些行」说清楚（`1-10,80` 一次就能填完），
 * 而不是往已有选择上叠加 —— 叠加的话用户填完 100 再填 11-200，会得到 1-200，
 * 而他想要的其实是「只有 11-200」。想清空重来用右侧「清空选择」。
 *
 * 只有「已结束」的行能勾（在跑/排队的行没有勾选框，见 isTerminal 那段注释），
 * 所以命中里要摘掉不可勾的那些，并把摘掉多少首说给用户 —— 不然他会以为全选上了。
 */
function onRangeApply(indexes) {
  const rows = pickByIndexes(filteredTasks.value, indexes)
  const selectable = rows.filter(t => isTerminal(t.status))
  if (!selectable.length) {
    notifyWarn(`命中的 ${rows.length} 行都还在跑或排队，没有可勾选的记录（换个筛选试试）`)
    return
  }
  selected.value = new Set(selectable.map(t => t.id))
  const skipped = rows.length - selectable.length
  notifyOk(
    `已选中 ${selectable.length} 行` +
      (skipped > 0 ? `，另有 ${skipped} 行在跑/排队不能勾` : ''),
  )
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

const selectionActions = computed(() => {
  const acts = []
  // 「继续」覆盖在跑之外的一切（失败要重试、停止要恢复），所以它是第一条
  if (selectedRetryableCount.value > 0 || selectedResumableCount.value > 0) {
    acts.push({
      key: 'continue',
      label: '继续',
      icon: Play,
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
  acts.push({ key: 'all', label: allSelected.value ? '取消全选' : '全选已结束', icon: ListChecks })
  acts.push({ key: 'clear', label: `删除记录（${selectedIds.value.length}）`, icon: Trash2, danger: true, hint: '只从队列里移除记录，不删 NAS 上的文件' })
  return acts
})

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
  if (currentFilter.value === 'downloading') {
    return list.filter(t => t.status === 'pending' || t.status === 'downloading')
  }
  if (currentFilter.value === 'paused') {
    return list.filter(t => t.status === 'paused')
  }
  if (currentFilter.value === 'stopped') {
    return list.filter(t => t.status === 'stopped')
  }
  if (currentFilter.value === 'completed') {
    return list.filter(t => t.status === 'success')
  }
  if (currentFilter.value === 'failed') {
    return list.filter(t => t.status === 'failed')
  }
  return list
})
</script>
