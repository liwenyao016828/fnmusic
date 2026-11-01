<template>
  <div class="relative flex min-h-0 flex-1 flex-col overflow-hidden">
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
      <div class="flex min-w-0 flex-col gap-y-1.5 sm:flex-row sm:flex-wrap sm:items-center sm:gap-x-2">
        <!--
          筛选分段控件（沉底轨道 + 白色药丸）。
          2026-11-01（用户）：「下载页面（全部，下载中，已暂停，已停止…）这几个在移动端要
          单独一行，并且在适配的时候可以精简文字以便在一行里显示完，不用拖动条」——
          所以：
            · 移动端这一条**独占一行**（父容器 flex-col，sm 起才回到同一行）；
            · 标签文字按断点收字（`<span class="sm:hidden">` 用 2 字短名，宽屏用全名）；
            · 计数在移动端藏起来（一屏只留最必要的字）；
            · 轨道用 `overflow-hidden` + 均分（`flex-1` + `min-w-0`），**不出现横向滚动条**。
        -->
        <div class="flex w-full min-w-0 items-center gap-0.5 overflow-hidden rounded-[8px] bg-[var(--ys-surface-sunken)] p-1 sm:w-auto sm:flex-1">
          <button
            v-for="tab in filterTabs" :key="tab.id"
            @click="currentFilter = tab.id"
            :class="[
              'ys-press min-w-0 flex-1 whitespace-nowrap rounded-[8px] px-1.5 py-1 text-[11px] font-medium transition-colors sm:flex-none sm:px-2.5 sm:text-[11.5px]',
              currentFilter === tab.id
                ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
                : 'text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]',
            ]"
          >
            <span class="sm:hidden">{{ tab.short }}</span>
            <span class="hidden sm:inline">{{ tab.name }}</span>
            <span class="ml-0.5 hidden text-[10px] opacity-60 sm:inline">{{ tab.count }}</span>
          </button>
        </div>

        <div class="flex shrink-0 items-center justify-end gap-0.5">
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
              ? 'text-emerald-600 hover:text-emerald-600'
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

        2026-10-30 用户：「下载页面中,全选后,出现的已选,继续重新下载,删除记录这些和右上角的
        恢复,失败,停止,记录重复了吧.我的建议是,去掉全选后出现的四个按钮.把全选按钮放到按序号选
        这里.取消全选也放这里.右边原来的去掉全选的位置,改为重新下载.」

        所以这一整行**删掉**了：
          · 「已选 N」不再单独占一行 —— 它已经在「按序号选」那一行的标签里
            （「按序号选（共 N 行可勾）」）+ 按钮文案「取消全选（M）」里；
          · 暂停选中 / 继续 / 停止选中：对应右上角的「暂停 / 继续」切换与「停止全部」；
          · 重新下载：移到右上角那一组（原来全选所在的位置）；
          · 删除记录：右上角本来就有「清除记录」。
        一句话：勾选只是「限定范围」，动作仍然只有一份、位置固定。
      -->
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
    <Info class="mt-[1px] h-3.5 w-3.5 shrink-0 text-[var(--ys-text-aux)]" :stroke-width="1.8" />
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
  <div class="min-h-0 flex-1 space-y-2 overflow-y-auto bg-[var(--ys-surface-sunken)] p-3 sm:p-4">
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
    >
      <!--
        「全选当前」放在这里（2026-10-29 用户：「下载页面,全选当前的功能,移到下方,
        替换按序号选的文字」）。以前它在顶部那行批量动作里，用户要往上找；
        现在跟「按序号选」并排，两种「一次选一片」的手法是同一个位置、同一种心智。
        没勾选时也常驻（2026-10-25 #4 的诉求：全选不能藏在勾选之后才出现），
        列表为空时禁用但占位，避免忽隐忽现。
      -->
      <template #extra>
        <button
          type="button"
          :disabled="filteredTasks.length === 0"
          class="ys-press flex items-center gap-1 rounded-lg border border-[var(--ys-border)] bg-white px-2.5 py-1 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.04] hover:text-[var(--ys-text)] disabled:cursor-not-allowed disabled:opacity-35"
          :title="allSelected ? '取消当前筛选下的全部勾选' : '把当前筛选下的所有行都勾上（含下载中 / 排队中）'"
          @click="onSelectionAction('all')"
        >
          <ListChecks class="h-3.5 w-3.5" :stroke-width="1.9" />
          {{ allSelected ? '取消全选' : `全选当前（${filteredTasks.length}）` }}
        </button>
      </template>
    </RangeSelectBar>

    <!-- 空状态 -->
    <div
      v-if="!filteredTasks.length"
      class="flex flex-col items-center justify-center py-24 text-center text-gray-500"
    >
      <div class="w-14 h-14 rounded-2xl bg-[var(--ys-surface-sunken)] text-gray-300 flex items-center justify-center mb-3">
        <Music2 class="w-7 h-7" />
      </div>
      <p class="text-xs font-medium text-gray-600 mb-1">
        {{ currentFilter === 'all' ? '暂无下载任务' : `暂无${currentFilterName}任务` }}
      </p>
      <p class="text-[11px] text-gray-500 max-w-xs">
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
        class="pointer-events-none grid h-4 w-4 shrink-0 place-items-center rounded-[6px] border transition-colors"
        :class="selected.has(task.id)
          ? 'border-emerald-500 bg-emerald-600 text-white'
          : 'border-[var(--ys-border-strong)] bg-white text-transparent'"
        aria-hidden="true"
      >
        <Check class="h-3 w-3" :stroke-width="3" />
      </span>

      <!-- 封面 -->
      <div class="w-10 h-10 rounded-lg overflow-hidden bg-[var(--ys-surface-sunken)] shrink-0 relative flex items-center justify-center">
        <img
          v-if="task.song.cover"
          :src="task.song.cover"
          alt="cover"
          referrerpolicy="no-referrer"
          class="w-full h-full object-cover"
          @error="task.song.cover = ''"
        />
        <Music2 v-else class="h-4 w-4 text-[var(--ys-text-aux)]" :stroke-width="1.8" />
      </div>

      <!-- 歌曲信息与状态 -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center justify-between gap-1 mb-0.5">
          <p class="text-xs font-semibold text-gray-800 truncate" :title="task.song.name">
            {{ task.song.name }}
          </p>
          <!-- 平台标签 -->
          <span class="text-[9px] px-1.5 py-0.2 rounded-full bg-[var(--ys-surface-sunken)] text-gray-500 font-mono shrink-0">
            {{ task.song.source || task.platform || 'kw' }}
          </span>
        </div>
        <p class="text-[11px] text-gray-500 truncate mb-1" :title="task.song.singer">
          {{ task.song.singer }}{{ task.song.album ? ' · ' + task.song.album : '' }}
        </p>

        <!-- 状态徽标与提示 -->
        <div class="flex items-center gap-1.5 text-[10px]">
          <!-- 后台排队中 -->
          <span
            v-if="task.status === 'pending'"
            class="px-2 py-0.5 rounded-md bg-[var(--ys-surface-sunken)] text-gray-500 flex items-center gap-1"
            title="已提交到 NAS 后台队列，前端关闭也会继续"
          >
            <Clock class="w-3 h-3 text-gray-500" />
            <span>后台排队中</span>
          </span>

          <!-- 后台下载中 -->
          <span
            v-else-if="task.status === 'downloading'"
            class="px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-600 flex items-center gap-1 font-medium"
            title="由 NAS 后端执行，关掉本页/浏览器也会继续"
          >
            <Loader2 class="w-3 h-3 animate-spin text-emerald-600" />
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
            <Check class="w-3 h-3 text-emerald-600" />
            <span>已保存至 NAS</span>
          </span>

          <!--
            下载失败：徽标给一句 4~6 字的结论（平台无此档 / 洛雪宿主未开 / 写盘失败…），
            具体原因链在下面单独一行摊开 —— 以前只把整串塞进 title 悬停，
            用户根本看不到（issue #10 原话「个别下载任务成功，歌单全部失败，这是废了吗」）。
          -->
          <span
            v-else-if="task.status === 'failed'"
            class="px-2 py-0.5 rounded-md bg-rose-50 text-rose-600 flex items-center gap-1"
            :title="task.error || '下载失败'"
          >
            <AlertCircle class="w-3 h-3 text-rose-500 shrink-0" />
            <span>{{ explainFailure(task.error).label }}</span>
          </span>

          <!--
            已停止：点过「停止全部」/ 订阅行「停止」后留下的任务。
            ⚠️ 这一档以前**连状态文字都没有**，右侧也没有任何按钮 ——
            用户看得到任务却动不了（2026-09-22 反馈：「下载-停止后，已停止增加恢复下载」）。
          -->
          <span
            v-else-if="task.status === 'stopped'"
            class="px-2 py-0.5 rounded-md bg-[var(--ys-surface-sunken)] text-gray-500 flex items-center gap-1"
            :title="task.error || '已被用户停止'"
          >
            <Square class="w-3 h-3 text-gray-500" />
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
            class="px-1.5 py-0.5 rounded bg-[var(--ys-surface-sunken)] text-gray-500 font-mono"
            :title="'后台实际拿到的音质/容器'"
          >
            实际 {{ task.resolvedQuality }}
          </span>
          <span
            v-if="task.resolvedSource"
            class="px-1.5 py-0.5 rounded bg-[var(--ys-surface-sunken)] text-gray-500 truncate max-w-[130px]"
            :title="'实际取链渠道：' + task.resolvedSource"
          >
            {{ task.resolvedSource }}
          </span>
          <span v-if="task.attempts > 0" class="text-gray-500">
            重试 {{ task.attempts }} 次
          </span>
          <!--
            去重标记：同一首歌在失败段里被收了 N 条（不同来源/不同 client_id 提交过
            同一首，后端按 client_id 去重所以它们本来就是多条，见 dedupeBySong 的说明）。
            只在这一行上标个数，不再列 N 行 —— 用户原话「失败里怎么出现多个同一首歌曲」。
          -->
          <span
            v-if="task.dupCount > 1"
            class="rounded bg-rose-50 px-1.5 py-0.5 font-mono text-rose-600"
            :title="`同一首歌有 ${task.dupCount} 条失败记录（不同来源/不同时间提交），这里只列最新一条`"
          >
            共 {{ task.dupCount }} 条
          </span>
          <span v-if="task.path" class="text-gray-500 truncate max-w-[220px]" :title="task.path">{{ task.path }}</span>
        </div>

        <!--
          失败原因链：把后端拼好的「原生：…；洛雪：…；外挂池：…」摊开成几行。
          只在失败任务上出现，且只在真有明细时出现 —— 成功/排队中的行不多占一像素。
        -->
        <ul
          v-if="task.status === 'failed' && explainFailure(task.error).detail.length"
          class="mt-1 space-y-0.5 text-[10px] leading-4 text-rose-500/90"
        >
          <li v-for="(line, li) in explainFailure(task.error).detail" :key="li" class="truncate" :title="line">
            · {{ line }}
          </li>
        </ul>
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
          class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-50 text-emerald-600 transition-colors"
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
          class="px-2 py-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-50 text-emerald-600 text-[10px] font-medium transition-colors flex items-center gap-1"
          title="恢复下载（重新排队并继续）"
        >
          <PlayCircle class="w-3 h-3" :stroke-width="1.9" />
          <span>恢复下载</span>
        </button>

        <!-- 排队中/下载中/已暂停：取消并停止按钮 -->
        <button
          v-if="task.status === 'pending' || task.status === 'downloading' || task.status === 'paused'"
          @click.stop="downloadManager.cancelTask(task.id)"
          class="p-1.5 rounded-lg text-gray-500 hover:text-rose-600 hover:bg-rose-50 transition-colors"
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
            class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-50 text-emerald-600 transition-colors"
            title="重新下载"
          >
            <RotateCw class="w-3.5 h-3.5" />
          </button>
        </template>

        <!-- 已完成：用当前默认音质再下一次 -->
        <button
          v-if="task.status === 'success'"
          @click.stop="downloadManager.redownloadTasksByIds([task.id])"
          class="p-1.5 rounded-lg bg-emerald-50 hover:bg-emerald-50 text-emerald-600 transition-colors"
          title="重新下载（用当前默认音质再入队一次）"
        >
          <RotateCw class="w-3.5 h-3.5" />
        </button>

        <!-- 移除记录按钮 (已完成 / 失败 / 已停止) -->
        <button
          v-if="task.status === 'success' || task.status === 'failed' || task.status === 'stopped'"
          @click.stop="downloadManager.removeTask(task.id)"
          class="p-1.5 rounded-lg text-gray-300 hover:text-gray-500 hover:bg-[var(--ys-surface-hover)] transition-colors"
          title="移除记录"
        >
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  </div>
  </div>

  <!--
    底部状态提示条（2026-11-01 用户）：「下载页增加底部状态提示，并跟随状态切换页面
    （有下载→下载页、有失败→失败页）」。

    放底部不放顶部：顶部已有一行 backendNotice（讲「后台能不能下」，是前置条件），
    再堆一行就两行提示抢位置；这条讲的是**队列此刻在干什么**，属于看一眼就走的状态，
    贴着列表下沿最顺手。

    移动端（同一条需求）：「下载状态单独一行、文字精简、不使用滚动条」——所以：
      · 单独一层、flex 不换行，窄屏只留最关键的几个数字；
      · 文字 truncate（父级 overflow-hidden），横向滚动条永不出现；
      · 不做两套 DOM，只靠断点收字。
  -->
  <!--
    ══ 底部任务状态提示：**弹出的小提示框**，不是常驻条 ══
    2026-11-01（用户）：「底部状态提示是底部任务状态弹的小提示框。不是常驻」——
    所以它不再是贴着列表下沿的一条常驻横条（原来那条会一直占着一行高度），
    而是「任务状态一变就浮出来一下、几秒后自己收起」的浮动小胶囊：
      · 绝对定位贴底居中，不占布局高度（列表高度不再被它吃掉）；
      · 每次状态文案变化才重新弹出并重新计时（见 script 里 watch statusToast）；
      · 带跳转按钮时把倒计时暂停——用户正要点的东西不能自己消失。
  -->
  <Transition
    enter-active-class="transition duration-200 ease-out"
    enter-from-class="translate-y-2 opacity-0"
    enter-to-class="translate-y-0 opacity-100"
    leave-active-class="transition duration-150 ease-in"
    leave-from-class="translate-y-0 opacity-100"
    leave-to-class="translate-y-2 opacity-0"
  >
    <div
      v-if="statusToast"
      class="pointer-events-none absolute inset-x-0 bottom-3 z-20 flex justify-center px-3"
    >
      <div
        class="pointer-events-auto flex max-w-full items-center gap-2 rounded-full bg-[var(--ys-text)] px-3 py-1.5 text-[11.5px] text-white shadow-[0_4px_16px_rgba(0,0,0,0.18)]"
        @mouseenter="holdStatusToast"
        @mouseleave="resumeStatusToast"
      >
        <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="statusToast.dotClass" />
        <span class="min-w-0 truncate">{{ statusToast.text }}</span>
        <!--
          跳转而不是自动抢筛选：自动切会把用户正看着的「已完成」列表抢走，
          所以给一个明说「点去哪个页」的入口。优先级同样是 下载中 > 失败。
        -->
        <button
          v-if="statusToast.jumpTo"
          type="button"
          class="ys-press shrink-0 rounded-full px-2 py-0.5 text-[11px] font-semibold text-white/90 transition-colors hover:bg-white/15"
          @click="jumpFromToast(statusToast.jumpTo)"
        >{{ statusToast.jumpLabel }}</button>
      </div>
    </div>
  </Transition>

  <!--
    勾选了任务才浮出的操作条：清除选中 / 全选当前筛选。

    ⚠️ 这一条必须留在任何弹窗外面。以前它被写在 `Teleport + v-if` 的弹窗里，
    于是只有在那个弹窗打开时才存在 —— 平时勾了行什么都不浮出来，
    看起来就像「多选失效了」（用户 2026-10-23：「多选框上方增加多选功能……这些都失效了」）。
  -->
  <!--
    2026-10-30 用户：「去掉全选后出现的四个按钮」——
    底部这条浮出条（已选 N + 继续 / 重新下载 / 删除记录 + X）同样是与右上角批量动作
    重复的第二份，一并移除。勾选范围与「取消全选（M）」现在只在「按序号选」那一行，
    动作只保留右上角那一份。
    <SelectionBar> 组件本身继续由 DownloadHub / QualityView 使用，不删。
  -->

</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch, onBeforeUnmount } from 'vue'
import { downloadManager } from '../services/downloadManager'
import SelectField from './SelectField.vue'
import { backendDownload } from '../services/backendDownload.js'
import RangeSelectBar from './RangeSelectBar.vue'
import { pickByIndexes } from '../services/rangeSelect'
import { explainFailure } from '../services/failureBadge'
import { ok as notifyOk, warn as notifyWarn } from '../services/feedback'
import {
  X, RotateCw, Trash2, Check,
  AlertCircle, Loader2, Music2, Clock, Pause, PlayCircle, Square,
  ListChecks, Eraser, RotateCcw, RefreshCw, Info
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
  {
    id: 'redownload',
    // 2026-10-30 用户：「右边原来的去掉全选的位置,改为重新下载」。
    // 这里原来是「全选当前」（已移到列表上方的「按序号选」那一行），
    // 空出来的位置放「重新下载」：把已完成的任务用当前音质策略再要一遍，
    // 与「重试失败」语义不重叠（一个补失败，一个换更好的音质重下）。
    label: selectedDoneCount.value > 0 ? `重新下载已完成（${selectedDoneCount.value}）` : '重新下载',
    icon: RefreshCw,
    disabled: selectedDoneCount.value === 0,
    hint: selectedDoneCount.value > 0
      ? `把勾选的 ${selectedDoneCount.value} 首已完成任务用当前音质重新下载（盘上那份不会被更差的覆盖）`
      : '先在左侧勾选已完成的歌曲，再点这里用当前音质重新下载',
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
    } else if (key === 'redownload') {
      // 只有勾选了「已完成」的行才有意义：重下用的是当前音质策略，
      // 盘上已有的文件不会被更差的覆盖（后端 force 提交，走同一套判重）。
      const ids = selectedTasks.value.filter(t => t.status === 'success').map(t => t.id)
      if (!ids.length) {
        notifyWarn('先在列表左侧勾选已完成的歌曲，再点「重新下载」')
        return
      }
      const n = await downloadManager.redownloadTasksByIds(ids)
      notifyOk(`已把 ${n || ids.length} 首重新排进队列（原文件不会被删除）`)
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
  // ⚠️ 2026-10-29：这条从**这里**移到列表上方的 RangeSelectBar 里了
  // （用户：「全选当前的功能,移到下方,替换按序号选的文字」）。
  // 这里保留 'all' 这个 key 的分发分支（runSelectionAction 里仍然认它），
  // 只是不再在这个数组里生成按钮 —— 否则上方又会多出一颗重复的全选。
  acts.push({ key: 'clear', label: `删除记录（${selectedIds.value.length}）`, icon: Trash2, danger: true, hint: '只从队列里移除记录，不删 NAS 上的文件' })
  return acts
})

/**
 * 没勾选时那一行只摆「删除记录…」这类**需要先勾选才有意义**的动作 —— 现在它是空的。
 *
 * ⚠️ 2026-10-29：原来这里只摆一个「全选当前（N）」，现已移到列表上方的 RangeSelectBar
 * 右侧（用户要求「移到下方」）。返回空数组而不是删掉这个 computed，
 * 是因为模板里 `idleActions` 仍被引用（`selectedIds.length ? selectionActions : idleActions`），
 * 删了要连锁改模板；留一个空数组语义更清楚：没勾选时顶部这一行**不该有任何按钮**。
 * vite build 会 tree-shake 掉未使用的变量，但这里它**是**被模板用的，不会误删。
 */
const idleActions = computed(() => [])

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

/**
 * 底部任务状态提示的内容（2026-11-01 用户要的「底部状态提示 + 跟随状态切页」）。
 *
 * 口径：
 *   · 有在下的 → 先说「下载中 N」（最重要，用户想看进度）；
 *   · 有失败的 → 再说「失败 M」，并给一个「去失败页」的入口（用户原话
 *     「有失败→失败页」）；下载中也失败时，**下载中优先**，失败只作为后半句；
 *   · 只有已完成 → 一行「已完成 K」。
 * 全空（没有任何任务）时不出提示。
 *
 * 2026-11-01（用户）：「底部状态提示是底部任务状态弹的小提示框。不是常驻」——
 * 所以这里只负责算出**内容**，由下面的 statusToast 状态机决定什么时候浮出来、
 * 几秒后收起（见 watch(statusSummary)）。**不自动改 currentFilter**：自动切会把
 * 用户正在看的列表抢走（看「已完成」时突然跳去「失败」，比没有提示更烦）。
 */
const statusSummary = computed(() => {
  const tasks = downloadManager.tasks.value
  const active = tasks.filter(t => t.status === 'pending' || t.status === 'downloading').length
  const failed = tasks.filter(t => t.status === 'failed').length
  const done = tasks.filter(t => t.status === 'success').length
  const parts = []
  if (active) parts.push(`下载中 ${active}`)
  if (failed) parts.push(`失败 ${failed}`)
  if (!active && done) parts.push(`已完成 ${done}`)

  let jumpTo = ''
  let jumpLabel = ''
  if (active) {
    jumpTo = 'downloading'
    jumpLabel = '去看下载'
  } else if (failed) {
    jumpTo = 'failed'
    jumpLabel = '去处理失败'
  }

  return {
    hasAnything: parts.length > 0,
    text: parts.join(' · ') || '',
    dotClass: active
      ? 'bg-[var(--ys-accent)]'
      : (failed ? 'bg-rose-500' : 'bg-[#22c55e]'),
    jumpTo,
    jumpLabel,
  }
})

/**
 * 底部小提示框的状态机（2026-11-01 用户：「底部任务状态弹的小提示框。不是常驻」）。
 *
 * 规则：
 *   · 文案（`statusSummary.text`）**变了**才弹出，并重新计时 —— 同一个数字不重复弹，
 *     不然队列一直在跑时会每隔几秒闪一下，很吵；
 *   · `STATUS_TOAST_MS` 后自动收起；鼠标停在上面时暂停倒计时（`holdStatusToast`），
 *     移开继续（`resumeStatusToast`）—— 用户正要点跳转时它不能自己消失；
 *   · 没有任何任务（`hasAnything` false）时立刻收起。
 */
const STATUS_TOAST_MS = 4000
const statusToast = ref(null)
let toastTimer = null
let toastRemain = STATUS_TOAST_MS
let toastStartedAt = 0

function clearToastTimer() {
  if (toastTimer) {
    clearTimeout(toastTimer)
    toastTimer = null
  }
}

function armToastTimer(ms) {
  clearToastTimer()
  toastRemain = ms
  toastStartedAt = Date.now()
  toastTimer = setTimeout(() => {
    toastTimer = null
    statusToast.value = null
  }, ms)
}

function holdStatusToast() {
  if (!statusToast.value || !toastTimer) return
  const elapsed = Date.now() - toastStartedAt
  toastRemain = Math.max(600, toastRemain - elapsed)
  clearToastTimer()
}

function resumeStatusToast() {
  if (!statusToast.value) return
  armToastTimer(toastRemain)
}

function jumpFromToast(filter) {
  if (!filter) return
  currentFilter.value = filter
  statusToast.value = null
  clearToastTimer()
}

watch(
  () => statusSummary.value,
  (s) => {
    if (!s.hasAnything) {
      statusToast.value = null
      clearToastTimer()
      return
    }
    if (statusToast.value && statusToast.value.text === s.text) return
    statusToast.value = { ...s }
    armToastTimer(STATUS_TOAST_MS)
  },
  { immediate: true, deep: false }
)

onBeforeUnmount(() => clearToastTimer())

/**
 * 筛选分段。
 * `short` 是移动端用的 2 字名（2026-11-01 用户：「移动端……可以精简文字以便在一行里
 * 显示完，不用拖动条」）——6 个 2 字标签 + 无计数，390px 宽也能一行放下。
 */
const filterTabs = computed(() => [
  { id: 'all', name: '全部', short: '全部', count: downloadManager.totalCount.value },
  { id: 'downloading', name: '下载中', short: '下载', count: downloadManager.activeCount.value },
  { id: 'paused', name: '已暂停', short: '暂停', count: downloadManager.pausedCount.value },
  { id: 'stopped', name: '已停止', short: '停止', count: stoppedCount.value },
  { id: 'completed', name: '已完成', short: '完成', count: downloadManager.completedCount.value },
  { id: 'failed', name: '失败', short: '失败', count: downloadManager.failedCount.value },
])

const stoppedCount = computed(() =>
  downloadManager.tasks.value.filter(t => t.status === 'stopped').length
)

const currentFilterName = computed(() => {
  const match = filterTabs.value.find(t => t.id === currentFilter.value)
  return match ? match.name : ''
})

/**
 * 失败列表里去重（2026-11-01 用户：「失败里怎么出现多个同一首歌曲」）。
 *
 * 真话：后端队列按 `client_id` 去重，**同一首歌确实只会有一条**（见
 * backenddl.Queue.Submit）。界面上出现「好几首一样的」，是同一个平台同一首歌
 * 被**不同来源**分别提交过：订阅行自动补下载用 `mon:<订阅id>:...` 当 client_id，
 * 用户手动下载用 `dl-...`，重试又是另一次提交 —— client_id 各不相同，后端就不算
 * 重复，界面上自然排成多行同一个歌名 + 歌手。
 *
 * 失败段按「歌名+歌手」收成一行（保留**最新那条**，其余计数），这样一眼看得出
 * 是「同一首失败了好几次」而不是「这里有一堆不同的坏歌」。只在 failed 筛选下做，
 * 「全部」段仍按原样逐条列出（那里能看到每一条的真实身份和错误）。
 */
function dedupeBySong(list) {
  const seen = new Map()
  for (const t of list) {
    const s = t.song || {}
    const key = `${(s.singer || '').trim()}||${(s.name || t.name || '').trim()}` || t.id
    const hit = seen.get(key)
    if (!hit) {
      seen.set(key, { task: t, dup: 0 })
    } else {
      // list 已按 addedAt 倒序，第一条就是最新的一条
      hit.dup += 1
    }
  }
  return [...seen.values()].map(({ task, dup }) => (dup ? { ...task, dupCount: dup + 1 } : task))
}

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
    return dedupeBySong(order.filter(t => t.status === 'failed'))
  }
  return order
})
</script>
