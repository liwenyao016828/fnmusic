<template>
  <PageShell>
    <template #title-extra>
      <span class="rounded-full border border-emerald-200 bg-emerald-50 px-2 py-0.5 font-mono text-[10px] text-emerald-600">
        订阅 · 追新 · 推送
      </span>
    </template>

    <template #actions>
      <button
        @click="onRefresh"
        :disabled="reloading"
        class="rounded-[10px] border border-[var(--ys-border)] bg-white px-3.5 py-2 text-xs font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.03] hover:text-[var(--ys-text)] disabled:opacity-50"
      >{{ reloading ? '刷新中…' : '刷新' }}</button>
    </template>

    <template #metrics>
      <div v-if="metrics.length" class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div
          v-for="m in metrics"
          :key="m.label"
          class="rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white px-[18px] py-4 shadow-[0_1px_2px_rgba(0,0,0,0.03)]"
        >
          <!-- 指标卡：标签在上（带一颗状态点表示倾向），数字在下且一律墨色 ——
               彩色大数字一排下来像仪表盘，Apple 的做法是「数字说事实，颜色只提示」。 -->
          <span class="flex items-center gap-1.5 text-[11px] font-medium text-[var(--ys-text-muted)]">
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="m.dot"></span>
            {{ m.label }}
          </span>
          <b class="mt-1.5 block text-[26px] font-semibold tracking-[-0.02em] tabular-nums text-[var(--ys-text)]">{{ m.value }}</b>
        </div>
      </div>
    </template>

        <!--
          一个来源 = 一行订阅，两个能力位（下载到本地曲库 / 同步到飞牛歌单）。

          底层仍是两套任务（monitor 管发现与下载、push 管飞牛歌单），但它们共享
          「来源订阅 + 定时增量发现」，差别只在「对新增曲目做什么」—— 所以界面上
          合成一个列表、用两个勾选框区分，而不是两个并列的功能页。
          归并逻辑见 services/subscribe.js 的 buildSubscriptions()。
        -->
        <!--
          使用引导：形态照 fnmusic-flow 的 onboarding —— 一块卡里「标题 + N/3 进度 + 收起」，
          下面一排三张步骤卡，完成打勾、文案换成真实数字、每张带跳转按钮。
          （用户 2026-09-23：「引导还是没看到。我需要的是顶部大卡片式的那种」+「你看他怎么做的」）
          收起**由用户点**（flow 也是如此，不自动消失），点了以后记窗口记忆，横幅上一眼能看到「展开」。
        -->
        <div v-if="!pushGuideCollapsed"
          class="rounded-[18px] border border-gray-200 bg-white p-4 sm:p-5 space-y-3">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <p class="text-[10px] font-bold uppercase tracking-wide text-emerald-600">首次使用</p>
              <h3
                class="mt-1 text-[15px] font-semibold text-gray-800"
                title="一条订阅 = 一个来源，两个开关各勾一个。"
              >
                {{ pushGuideAllDone ? '一切就绪，之后每轮自动追新' : '三步即可开始' }}
              </h3>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <span class="rounded-full border border-gray-200 bg-white px-2.5 py-1 font-mono text-[11px] font-bold text-emerald-600">
                {{ pushGuideDone }} / 3
              </span>
              <button
                class="rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] text-gray-500 transition-all hover:bg-gray-50"
                @click="hidePushGuide"
              >{{ pushGuideAllDone ? '收起' : '暂时隐藏' }}</button>
            </div>
          </div>

          <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
            <!--
              2026-10-18 用户：「订阅更新怎么还是那么多的文字，简化一下。」
              步骤卡原来是「标题 + 一整段 hint + 按钮」三行字，三张卡就是三段说明；
              现在整段 hint 收进卡片的 title（悬停可见），卡片只剩「序号 + 标题 + 动作按钮」。
              首次使用仍然看得懂：标题本身就是动作，按钮就是入口。
            -->
            <div v-for="s in pushGuideSteps" :key="s.id"
              class="flex flex-col rounded-xl border p-3.5"
              :class="s.done ? 'border-emerald-200 bg-emerald-50/40' : 'border-gray-200'"
              :title="s.done ? s.doneHint : s.hint">
              <div class="flex items-center gap-2.5">
                <span :class="[
                  'flex h-[23px] w-[23px] shrink-0 items-center justify-center rounded-full text-[12px] font-extrabold',
                  s.done ? 'bg-emerald-100 text-emerald-600' : 'bg-emerald-50 text-emerald-600',
                ]">
                  <Check v-if="s.done" class="h-3 w-3" />
                  <template v-else>{{ s.no }}</template>
                </span>
                <b class="min-w-0 flex-1 truncate text-[13px]" :class="s.done ? 'text-emerald-700' : 'text-gray-700'">{{ s.title }}</b>
                <Check v-if="s.done" class="h-3.5 w-3.5 shrink-0 text-emerald-500" />
              </div>
              <div class="mt-3 flex flex-wrap gap-1.5">
                <button
                  :class="[
                    'rounded-lg px-2.5 py-1 text-[11px] font-semibold transition-all',
                    s.done ? 'border border-gray-200 text-gray-600 hover:bg-gray-50' : 'bg-emerald-500 text-white hover:bg-emerald-600',
                  ]"
                  @click="runGuideAction(s)"
                >{{ s.action.label }}</button>
              </div>
            </div>
          </div>
        </div>
        <div v-else
          class="flex flex-wrap items-center gap-2 rounded-[18px] border border-gray-200 bg-white px-[17px] py-3 text-[11px] text-gray-500 shadow-2xs">
          <span class="flex h-5 w-5 items-center justify-center rounded-md bg-emerald-100 text-emerald-600">
            <Check class="h-3 w-3" />
          </span>
          <span>使用引导已收起</span>
          <button class="text-emerald-600 hover:underline" @click="openPushGuide">展开看看</button>
        </div>

        <div class="flex items-center justify-end gap-2">
          <button
            class="flex shrink-0 items-center justify-center rounded-lg bg-emerald-500 px-4 py-2 text-[13px] font-semibold text-white transition-all hover:bg-emerald-600"
            @click="openCreate"
          >
            + 新建订阅
          </button>
        </div>

        <!--
          「下载到本地曲库」这一步在**浏览器里**执行（音源脚本只能在浏览器跑），
          所以那个窗口关掉就停。这件事以前只在**报错文案**里提过，而且说的是「网页端」——
          用户直接问过「网页开着什么意思，飞牛里应用启动算吗」（2026-09-22）。
          配了服务型音源则由后端直连取链、关页面也能下，所以只在**没配**时提示。
          引导卡片的第①步已经把这件事说清了，所以**引导显示时不再重复**；
          等用户把引导收起（那时他可能压根没读过第①步的文案）才把这条常驻提醒放出来。

          2026-10-18 简化：原来这一段是三行正文，现在压成一行标题 +
          完整说明进 title；它是一条**提示**，不该在页面上占掉三行。
        -->
        <p
          v-if="pushGuideCollapsed && !apiSourceCount"
          class="rounded-[18px] border border-gray-200 bg-white px-4 py-2.5 text-[11px] text-gray-500"
          title="取链脚本只能在浏览器里跑；关掉窗口后已排队的会停，下次打开继续。想关掉页面也能下：到「音源管理 → 按服务地址接入」配一个服务型音源，那种音源由后端直接取链，跟浏览器无关。"
        >
          <b class="text-gray-700">「下载到本地曲库」需要让曲率 这个窗口一直开着</b>
          <span class="ml-1 text-gray-400">（关掉也能下：配一个服务型音源）</span>
        </p>

        <!--
          飞牛没连上不影响「下载」这一位，但「同步飞牛」建不出来 —— 提前说清，
          别等用户勾了才报错（勾选框会置灰并带上原因）。
        -->
        <p
          v-if="!fnosConnected"
          class="rounded-[18px] border border-amber-100 bg-amber-50/70 px-4 py-2.5 text-[11px] text-amber-900/80"
          title="到「账号连接」登录飞牛即可；只勾「下载到本地曲库」不受影响。"
        >
          <b class="text-amber-700">飞牛音乐未连接</b>
          <span class="ml-1">，暂时建不了「同步到飞牛歌单」</span>
        </p>

        <div v-if="!subscriptions.length" class="rounded-[18px] border border-dashed border-gray-200 bg-gray-50/50 py-12 text-center">
          <p class="text-[12px] text-gray-400">还没有订阅 —— 点右上角「新建订阅」开始</p>
        </div>

        <div v-else class="space-y-2.5">
          <div
            v-for="row in subscriptions" :key="row.key"
            class="rounded-[18px] border border-gray-200 bg-white p-4"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-semibold text-gray-800">{{ row.name }}</span>
                  <span class="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[10px] text-gray-500">{{ row.sourceLabel }}</span>
                  <span v-if="row.hasDownload && !row.baselineDone" class="rounded border border-amber-200 bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-700" title="第一次检查还没跑完。跑完就会开始记录「以后新增了哪些歌」">还没开始追新</span>
                  <span v-if="row.pendingCount" class="rounded border border-amber-200 bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-700">待下载 {{ row.pendingCount }}</span>
                  <span v-if="row.missingCount" class="rounded border border-gray-200 bg-gray-50 px-1.5 py-0.5 text-[10px] text-gray-500">飞牛缺 {{ row.missingCount }}</span>
                  <span v-if="rowBusy(row)" class="animate-pulse rounded bg-blue-50 px-1.5 py-0.5 text-[10px] text-blue-600">处理中</span>
                  <span v-else-if="!row.download && !row.fnos" class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-400">已停止</span>
                </div>
                <div class="mt-1.5 flex flex-wrap items-center gap-3 text-[11px] text-gray-400">
                  <span>间隔 {{ intervalText(row.intervalMinutes) }}</span>
                  <span v-if="row.targetTitle">飞牛歌单 {{ row.targetTitle }}</span>
                  <span v-if="row.lastRunAt">上次发现 {{ row.lastRunAt.replace('T', ' ') }}</span>
                  <span v-if="row.lastError" class="max-w-[260px] truncate text-rose-500" :title="row.lastError">
                    同步失败：{{ row.lastError }}
                  </span>
                </div>
              </div>
              <div class="flex shrink-0 items-center gap-1">
                <button
                  v-if="row.pendingCount || row.missingCount"
                  class="rounded-lg bg-amber-50 px-2.5 py-1.5 text-[11px] font-medium text-amber-700 hover:bg-amber-100 disabled:opacity-50"
                  :disabled="!!busyKey"
                  :title="fillHint(row)"
                  @click="fillRow(row)"
                >
                  {{ fillingKey === row.key && fillProgress ? `入队中 ${fillProgress}` : `补下载 ${row.pendingCount + row.missingCount} 首` }}
                </button>
                <button
                  class="rounded-lg bg-emerald-50 px-2.5 py-1.5 text-[11px] font-medium text-emerald-600 hover:bg-emerald-100 disabled:opacity-50"
                  :disabled="!!busyKey || (!row.download && !row.fnos)"
                  @click="runRow(row)"
                >
                  立即执行
                </button>
                <button
                  class="rounded-lg bg-gray-100 px-2.5 py-1.5 text-[11px] text-gray-600 hover:bg-gray-200"
                  @click="toggleExpand(row)"
                >
                  {{ expandedKey === row.key ? '收起' : '设置' }}
                </button>
                <button
                  class="rounded-lg p-1.5 text-gray-300 hover:bg-red-50 hover:text-red-400"
                  @click="removeRow(row)"
                >
                  <Trash2 class="h-3.5 w-3.5" />
                </button>
              </div>
            </div>

            <!--
              两个能力位：点一下即开、再点即停。
              沿用项目里筛选器的**药丸选中样式**（选中 = 实心 emerald + 打勾图标），
              不用裸 checkbox —— 一排小方框和整体视觉不搭。
              ⚠️ 「停」是**两侧一起停**、「开」只开点的那一侧（见 `capTitle` 的注释）；
              记录、已下载的文件、飞牛歌单都保留，要彻底清掉记录用右侧的删除。
            -->
            <div class="mt-3 flex flex-wrap items-center gap-2 border-t border-gray-50 pt-3">
              <span class="mr-0.5 text-[11px] text-gray-400">这个订阅做什么</span>
              <button
                class="flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all disabled:opacity-50"
                :class="row.download ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                :disabled="!!busyKey || capBlocked(row, 'download')"
                :title="capTitle(row, 'download')"
                @click="toggleCapability(row, 'download', !row.download)"
              >
                <Check v-if="row.download" class="h-3.5 w-3.5" />
                <Download v-else class="h-3.5 w-3.5" />
                下载到本地曲库
              </button>
              <button
                class="flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all disabled:opacity-50"
                :class="row.fnos ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                :disabled="!!busyKey || capBlocked(row, 'fnos')"
                :title="capTitle(row, 'fnos')"
                @click="toggleCapability(row, 'fnos', !row.fnos)"
              >
                <Check v-if="row.fnos" class="h-3.5 w-3.5" />
                <Send v-else class="h-3.5 w-3.5" />
                同步到飞牛歌单
              </button>
              <!--
                只停推送（用户 2026-09-22 提出）：
                药丸点关闭是两侧一起停，但「下载继续、先别往飞牛推」也说得通。
                只在推送开着的时候出现，免得平时多一个看不懂的按钮。
              -->
              <button
                v-if="row.fnos"
                class="rounded-lg px-2.5 py-1.5 text-[11px] text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 disabled:opacity-50"
                :disabled="!!busyKey"
                title="只停「同步到飞牛歌单」，本地下载继续跑。要恢复就再点一下左边的「同步到飞牛歌单」"
                @click="stopPushOnly(row)"
              >
                仅停推送
              </button>
            </div>

            <!-- 设置：只展开当前行（订阅多起来时整页会被撑得很长） -->
            <div v-if="expandedKey === row.key" class="mt-3 space-y-3 rounded-lg border border-gray-100 bg-gray-50/60 p-3">
              <!--
                两个能力位**并排成卡片**（用户 2026-10-07：「可以用一行多个，每个按照
                卡片形式展示按钮和文字标题，简介就可以」）。以前它们各自整行、第二个靠
                border-top 分隔 —— 一屏只看得到「下载」，得往下滚才知道还有「同步」，
                而这两个开关是这条订阅的全部意义。
              -->
              <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
              <div v-if="row.hasDownload" class="space-y-2 rounded-lg border border-gray-200 bg-white p-3">
                <p class="text-[12px] font-bold text-gray-700">下载到本地曲库</p>
                <div class="grid grid-cols-2 gap-2">
                  <label class="block">
                    <span class="mb-1 block text-[11px] text-gray-400">目标音质</span>
                    <select v-model="editForm.quality" class="w-full rounded-lg border border-gray-200 px-2 py-1.5 text-[12px] outline-none focus:border-emerald-400">
                      <option value="lossless">无损 (FLAC)</option>
                      <option value="hires">Hi-Res</option>
                      <option value="high">较高 (≈320k)</option>
                      <option value="standard">标准 (≈128k)</option>
                    </select>
                  </label>
                  <!--
                    用户 2026-09-22：「手动选择，或者手动输入」——
                    所以既有预设药丸（常用值一键选），也能直接填任意数（上限 300，后端 MaxDownloadsPerRun）。
                  -->
                  <div class="col-span-2 sm:col-span-1">
                    <span class="mb-1 block text-[11px] text-gray-400">每次下载数量</span>
                    <div class="flex flex-wrap items-center gap-1">
                      <button
                        v-for="opt in batchOptions" :key="opt"
                        class="rounded-lg px-2 py-1 text-[11px] font-medium transition-all"
                        :class="editForm.max_downloads === opt ? 'bg-emerald-500 text-white' : 'bg-white text-gray-600 hover:bg-gray-100'"
                        @click="editForm.max_downloads = opt"
                      >{{ opt }}</button>
                      <input
                        v-model.number="editForm.max_downloads"
                        type="number" min="1" max="300"
                        class="w-16 rounded-lg border border-gray-200 px-2 py-1 text-[11px] outline-none focus:border-emerald-400"
                        title="也可以直接填一个数（1~300）"
                      />
                    </div>
                  </div>
                  <label class="block">
                    <span class="mb-1 block text-[11px] text-gray-400">排除关键词</span>
                    <input v-model="editForm.exclude_kw" class="w-full rounded-lg border border-gray-200 px-2 py-1.5 text-[12px] outline-none focus:border-emerald-400" placeholder="伴奏,Live" />
                  </label>
                  <div class="flex items-end pb-1.5">
                    <button
                      class="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-[11px] font-medium transition-all"
                      :class="editForm.embed ? 'bg-emerald-500 text-white' : 'bg-white text-gray-600 hover:bg-gray-100'"
                      @click="editForm.embed = !editForm.embed"
                    >
                      <Check v-if="editForm.embed" class="h-3 w-3" />
                      内嵌歌词封面
                    </button>
                  </div>
                </div>
              </div>

              <div v-if="row.hasFnos" class="space-y-2 rounded-lg border border-gray-200 bg-white p-3">
                <p class="text-[12px] font-bold text-gray-700">同步到飞牛歌单</p>
                <div class="grid grid-cols-2 gap-2">
                  <label class="block">
                    <span class="mb-1 block text-[11px] text-gray-400">目标歌单名</span>
                    <input v-model="editForm.target_title" class="w-full rounded-lg border border-gray-200 px-2 py-1.5 text-[12px] outline-none focus:border-emerald-400" placeholder="留空用订阅名" />
                  </label>
                  <label class="block">
                    <span class="mb-1 block text-[11px] text-gray-400">保留期</span>
                    <select v-model="editForm.retention_mode" class="w-full rounded-lg border border-gray-200 px-2 py-1.5 text-[12px] outline-none focus:border-emerald-400">
                      <option value="keep">永久保留</option>
                      <option value="days">按天移除</option>
                    </select>
                  </label>
                  <label v-if="editForm.retention_mode === 'days'" class="block">
                    <span class="mb-1 block text-[11px] text-gray-400">保留天数</span>
                    <input v-model.number="editForm.retention_days" type="number" min="1" class="w-full rounded-lg border border-gray-200 px-2 py-1.5 text-[12px] outline-none focus:border-emerald-400" />
                  </label>
                  <div class="flex items-end pb-1.5">
                    <button
                      class="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-[11px] font-medium transition-all"
                      :class="editForm.sync_cover ? 'bg-emerald-500 text-white' : 'bg-white text-gray-600 hover:bg-gray-100'"
                      @click="editForm.sync_cover = !editForm.sync_cover"
                    >
                      <Check v-if="editForm.sync_cover" class="h-3 w-3" />
                      同步封面
                    </button>
                  </div>
                </div>
                <p class="text-[11px] leading-relaxed text-gray-400">
                  只清理本任务推送过的曲目。
                </p>
              </div>
              </div>

              <div class="flex flex-wrap items-center gap-2 border-t border-gray-100 pt-3">
                <span class="text-[11px] text-gray-400">检查间隔</span>
                <button
                  v-for="opt in intervalOptions" :key="opt.value"
                  class="rounded-lg px-2.5 py-1 text-[11px] font-medium transition-all"
                  :class="editForm.interval_minutes === opt.value ? 'bg-emerald-500 text-white' : 'bg-white text-gray-600 hover:bg-gray-100'"
                  @click="editForm.interval_minutes = opt.value"
                >{{ opt.label }}</button>
                <button
                  class="ml-auto rounded-lg bg-emerald-500 px-3 py-1.5 text-[11px] font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                  :disabled="!!busyKey"
                  @click="onSaveSettings(row)"
                >保存设置</button>
              </div>
            </div>
          </div>
        </div>

        <!--
          飞牛同步历史：低频查看，放列表下方（原来在「推送歌单」页里独占一块）。
          多选与一键清除（用户 2026-10-07 指令「任务状态的几个页面中增加多选或者
          一键清除记录按钮」）—— 它也是任务状态的一页。
        -->
        <div v-if="pushRuns.length" class="overflow-hidden rounded-xl border border-gray-200 bg-white">
          <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-gray-100 px-4 py-3">
            <h3 class="text-sm font-semibold text-gray-800">飞牛同步历史</h3>
            <span class="text-[11px] text-gray-400">最近 {{ pushRuns.length }} 条</span>
            <div class="ml-auto flex flex-wrap items-center gap-1.5">
              <label class="mr-1 flex cursor-pointer select-none items-center gap-1 text-[11px] text-gray-500">
                <input type="checkbox" class="align-middle accent-emerald-500" :checked="allPushRunsSelected" @change="toggleAllPushRuns" />
                <span>全选</span>
              </label>
              <button
                v-if="pushRunSelected.size"
                @click="clearPushRuns(Array.from(pushRunSelected))"
                class="rounded-lg bg-rose-50 px-2.5 py-1 text-[11px] font-bold text-rose-600 transition-colors hover:bg-rose-100"
              >清除选中（{{ pushRunSelected.size }}）</button>
              <button
                @click="clearPushRuns([])"
                class="rounded-lg px-2.5 py-1 text-[11px] font-bold text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600"
                title="清除全部同步历史（不动订阅任务本身）"
              >一键清除记录</button>
            </div>
          </div>
          <!--
            卡片行而不是表格（2026-10-08 实测：5 列 `<table>` 在 390px 手机视口下宽 **596px**，
            把「结果」那列整个推出屏幕 —— 横向滚动的表在手机上没人会去滑）。
            卡片行是**换行**而不是溢出：窄屏时时间与状态掉到第二行。
          -->
          <div class="max-h-[260px] overflow-y-auto">
            <div
              v-for="r in pushRuns" :key="r.id"
              class="flex items-start gap-2 border-b border-gray-50 px-3 py-2 last:border-0"
            >
              <input type="checkbox" class="mt-0.5 shrink-0 accent-emerald-500" :checked="pushRunSelected.has(r.id)"
                @change="togglePushRunSelect(r.id)" />
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span class="min-w-0 truncate text-[12px] font-medium text-gray-700">{{ r.task_name }}</span>
                  <span
                    class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px]"
                    :class="r.status === 'success' ? 'bg-emerald-50 text-emerald-600' : 'bg-rose-50 text-rose-600'"
                  >{{ r.status === 'success' ? '成功' : '失败' }}</span>
                  <span class="shrink-0 text-[10px] text-gray-400">{{ String(r.started_at || '').replace('T', ' ') }}</span>
                </div>
                <div v-if="r.error" class="mt-0.5 truncate text-[11px] text-rose-500" :title="r.error">{{ r.error }}</div>
                <div v-else-if="r.result" class="mt-0.5 text-[11px] text-gray-400">
                  匹配 {{ r.result.matched }}/{{ r.result.total }}，新增 {{ r.result.added }}
                </div>
              </div>
            </div>
          </div>
        </div>

    <Teleport to="body">
      <div v-if="showCreate" class="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4" @click.self="showCreate = false">
        <div class="max-h-[85vh] w-full max-w-[520px] overflow-y-auto rounded-2xl bg-white shadow-2xl">
          <div class="flex items-center justify-between border-b border-gray-100 px-5 py-3.5">
            <h3 class="text-sm font-semibold text-gray-800">新建订阅</h3>
            <button class="text-gray-400 hover:text-gray-600" @click="showCreate = false">✕</button>
          </div>

          <div class="max-h-[70vh] space-y-4 overflow-y-auto px-5 py-4">
            <!-- 名称 -->
            <label class="block">
              <span class="mb-1.5 block text-[12px] font-medium text-gray-600">订阅名称</span>
              <input v-model="form.name" class="w-full rounded-lg border border-gray-200 px-3 py-2 text-[13px] outline-none focus:border-emerald-400" placeholder="例如：每周新歌" />
            </label>

            <!--
              来源两种：粘贴公开歌单链接（可订阅可下载）/ 已登录账号里的歌单与每日推荐。
              账号来源没有可粘贴的链接，「每日推荐」监控侧也不支持 → 只能同步飞牛。
            -->
            <div>
              <span class="mb-1.5 block text-[12px] font-medium text-gray-600">订阅来源</span>
              <div class="mb-2 flex gap-2">
                <button
                  class="rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all"
                  :class="form.sourceMode === 'link' ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                  @click="setSourceMode('link')"
                >粘贴歌单链接</button>
                <button
                  class="rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all"
                  :class="form.sourceMode === 'account' ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                  @click="setSourceMode('account')"
                >从账号选</button>
              </div>

              <template v-if="form.sourceMode === 'link'">
                <textarea v-model="form.links" rows="3"
                          class="w-full rounded-lg border border-gray-200 px-3 py-2 text-[12px] font-mono outline-none focus:border-emerald-400"
                          placeholder="https://music.163.com/playlist?id=123&#10;https://y.qq.com/n/ryqq/playlist/456"></textarea>
                <span class="mt-1 block text-[11px] leading-relaxed text-gray-400">
                  网易云 / QQ 公开歌单，分享短链也行。每行一个。
                </span>
              </template>

              <template v-else>
                <div class="mb-2 flex gap-2">
                  <button
                    v-for="p in [{ id: 'netease', name: '网易云' }, { id: 'qq', name: 'QQ 音乐' }]" :key="p.id"
                    class="rounded-lg px-3 py-1 text-[12px] font-medium transition-all"
                    :class="form.provider === p.id ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                    @click="changeProvider(p.id)"
                  >{{ p.name }}</button>
                </div>
                <div v-if="sourcesLoading" class="py-1.5 text-[12px] text-gray-400">正在读取账号歌单…</div>
                <InlineNotice v-else-if="sourcesError" :text="sourcesError" />
                <select
                  v-else
                  v-model="form.sourceValue"
                  class="w-full rounded-lg border border-gray-200 px-3 py-2 text-[13px] outline-none focus:border-emerald-400"
                >
                  <option v-for="s in accountSources" :key="s.kind + ':' + s.id" :value="s.kind + ':' + s.id">
                    {{ s.name }}{{ s.count ? `（${s.count} 首）` : '' }}
                  </option>
                </select>
                <span class="mt-1 block text-[11px] leading-relaxed text-gray-400">
                  需先在「账号连接」登录；只能同步飞牛，不能自动下载。
                </span>
              </template>
            </div>

            <!--
              两个能力位：勾什么就建什么（沿用项目里的药丸选中样式）。
              以前一律两条都建，用户会莫名其妙多出一条用不到的推送任务，还得单独去关。
            -->
            <div class="rounded-lg border border-gray-200 px-3.5 py-3">
              <p class="mb-2.5 text-[12px] font-medium text-gray-700">这个订阅要做什么</p>
              <div class="space-y-2.5">
                <div class="flex items-start gap-2.5">
                  <button
                    class="flex shrink-0 items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all disabled:opacity-40"
                    :class="form.wantDownload ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                    :disabled="form.sourceMode === 'account'"
                    @click="form.wantDownload = !form.wantDownload"
                  >
                    <Check v-if="form.wantDownload" class="h-3.5 w-3.5" />
                    下载到本地曲库
                  </button>
                  <span class="pt-1.5 text-[11px] leading-relaxed text-gray-400">
                    {{ form.sourceMode === 'account'
                      ? '账号来源不能自动下载'
                      : '每轮新歌自动下到本地（音质与上限在设置里改）' }}
                  </span>
                </div>
                <div class="flex items-start gap-2.5">
                  <button
                    class="flex shrink-0 items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all disabled:opacity-50"
                    :class="form.wantFnos ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                    :disabled="!fnosConnected"
                    @click="form.wantFnos = !form.wantFnos"
                  >
                    <Check v-if="form.wantFnos" class="h-3.5 w-3.5" />
                    同步到飞牛歌单
                  </button>
                  <span class="pt-1.5 text-[11px] leading-relaxed text-gray-400">
                    {{ fnosConnected ? '把曲库里匹配上的曲目写进飞牛音乐歌单，缺的可以补下载' : '飞牛音乐未连接，先到「账号连接」登录' }}
                  </span>
                </div>
              </div>
            </div>

            <label class="block">
              <span class="mb-1.5 block text-[12px] font-medium text-gray-600">检查间隔（分钟）</span>
              <input v-model.number="form.interval" type="number" class="w-full rounded-lg border border-gray-200 px-3 py-2 text-[13px] outline-none focus:border-emerald-400" />
            </label>

            <InlineNotice :text="createError" />
          </div>

          <div class="flex justify-end gap-2 border-t border-gray-100 px-5 py-3.5">
            <button class="rounded-lg bg-gray-100 px-4 py-2 text-[12px] font-medium text-gray-600 hover:bg-gray-200" @click="showCreate = false">取消</button>
            <button class="rounded-lg bg-emerald-500 px-4 py-2 text-[12px] font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                    :disabled="creating || !canCreateSubscription" @click="createSubscription">
              {{ creating ? '创建中…' : '创建订阅' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </PageShell>
</template>

<script setup>
/**
 * 订阅更新（PushManager）—— 新框架 v2.4.x 从「曲库管家」拆出来的独立视图。
 *
 * 数据与动作全部来自 services/pushEngine.js（全局单例，App.vue 启动时常驻轮询），
 * 本组件只持有**纯 UI 状态**：引导卡折叠、行内设置表单、新建弹窗、同步历史多选。
 * 拆分的动机：补下载/自动同步以前绑在 LibraryManager 的组件生命周期上，
 * 页面一关引擎就停 —— 现在引擎独立，这个页面开不开都在追新。
 */
import { computed, onMounted, ref } from 'vue'
import { Check, Download, Send, Trash2 } from 'lucide-vue-next'
import PageShell from './PageShell.vue'
import InlineNotice from './InlineNotice.vue'
import { PushAPI } from '../api/client'
import { fail } from '../services/userMsg'
import { ok as flashOk } from '../services/feedback'
import { navigateTo } from '../services/navBus'
import { loadBool, savePref } from '../services/prefs'
import {
  SUBSCRIBE_INTERVAL_MINUTES,
  pushGuideSteps as pushGuideStepsOf,
  pushGuideDoneCount,
} from '../services/subscribe'
import { pushEngine } from '../services/pushEngine'

const {
  subscriptions, fnosConnected, apiSourceCount, pushRuns,
  fillingKey, fillProgress, busyKey,
  fillRow, fillHint,
  toggleCapability, stopPushOnly, runRow, removeRow, saveRowSettings,
  createLinkSubscriptions, createAccountTask,
  clearPushRuns: engineClearRuns,
  reloadAll, loadPushRuns, loadFnosStatus, loadApiSourceCount,
} = pushEngine

// ── 刷新 ──
const reloading = ref(false)
async function onRefresh() {
  reloading.value = true
  try {
    await Promise.all([reloadAll(), loadPushRuns(), loadFnosStatus(), loadApiSourceCount()])
  } finally {
    reloading.value = false
  }
}

// ── 指标行（口径与旧「曲库管家·推送页」一致，别再造第三份口径）──
const metrics = computed(() => {
  const rows = subscriptions.value
  return [
    { label: '订阅', value: rows.length, dot: 'bg-[var(--ys-text-faint)]' },
    { label: '自动下载', value: rows.filter((r) => r.download).length, dot: 'bg-emerald-500' },
    { label: '同步飞牛', value: rows.filter((r) => r.fnos).length, dot: 'bg-emerald-500' },
    { label: '待下载', value: rows.reduce((n, r) => n + r.pendingCount, 0), dot: 'bg-amber-500' },
  ]
})

// ── 三步引导（判据在 services/subscribe.js 的 pushGuideSteps，纯函数有单测）──
const pushGuideSteps = computed(() =>
  pushGuideStepsOf(subscriptions.value, {
    apiSourceCount: apiSourceCount.value,
    scanIntervalMinutes: subscriptions.value[0]?.intervalMinutes || SUBSCRIBE_INTERVAL_MINUTES,
  }),
)
const pushGuideDone = computed(() => pushGuideDoneCount(pushGuideSteps.value))
const pushGuideAllDone = computed(
  () => pushGuideSteps.value.length > 0 && pushGuideDone.value === pushGuideSteps.value.length,
)
const pushGuideCollapsed = ref(loadBool('push_guide_hidden', false))

function hidePushGuide() {
  pushGuideCollapsed.value = true
  savePref('push_guide_hidden', '1')
}
function openPushGuide() {
  pushGuideCollapsed.value = false
  savePref('push_guide_hidden', '0')
}

/** 步骤卡按钮：跳去做这件事的地方（新框架下 tab 跳转 = navigateTo 视图 + 分段） */
function runGuideAction(step) {
  const to = step?.action?.to
  if (!to) return
  if (to.kind === 'view') navigateTo(to.name)
  else if (to.kind === 'create') openCreate()
  else if (to.kind === 'tab') navigateTo(to.name, to.sub)
}

// ── 行状态 ──
function rowBusy(row) {
  return !!busyKey.value && busyKey.value.startsWith(`${row.key}:`)
}

function intervalText(minutes) {
  const m = Number(minutes || 0)
  if (!m) return '仅手动'
  if (m % 10080 === 0) return `${m / 10080} 周`
  if (m % 1440 === 0) return `${m / 1440} 天`
  if (m % 60 === 0) return `${m / 60} 小时`
  return `${m} 分钟`
}

/**
 * 能力位药丸能不能点 / 悬停文案。
 * ⚠️ 「停」是两侧一起停（见 services/subscribe.js 的 setCapability）——
 * 用户反馈过「点了停止怎么还在推」，capTitle 必须把这一点说出来。
 */
function capBlocked(row, which) {
  if (!row) return true
  if (which === 'download') {
    return row.hasDownload ? false : !row.canCreate
  }
  if (row.hasFnos) return false
  if (!row.canCreate) return true
  return !fnosConnected.value
}

function capReason(row, which) {
  if (!row) return ''
  const has = which === 'download' ? row.hasDownload : row.hasFnos
  if (!has && !row.canCreate) return row.blockReason || '这个来源建不了新的任务'
  if (which === 'fnos' && !has && !fnosConnected.value) return '飞牛音乐未连接，先到「账号连接」登录'
  return ''
}

function capTitle(row, which) {
  const blocked = capReason(row, which)
  if (blocked) return blocked
  const on = which === 'download' ? row.download : row.fnos
  if (on) {
    const both = '点一下停止 —— 下载与同步飞牛会一起停（记录、已下载的文件、飞牛歌单都保留）'
    return which === 'fnos' ? `${both}；只想停推送、留着下载，用旁边的「仅停推送」` : both
  }
  return which === 'download'
    ? '点一下开始：每轮发现的新歌自动下到本地'
    : '点一下开始：把曲库里匹配上的歌同步到飞牛歌单'
}

// ── 行内设置（一次只展开一行）──
const expandedKey = ref('')
const editForm = ref({
  quality: 'lossless', max_downloads: 30, exclude_kw: '', embed: true,
  target_title: '', sync_cover: true, retention_mode: 'keep', retention_days: 0,
  interval_minutes: SUBSCRIBE_INTERVAL_MINUTES,
})
const batchOptions = [10, 30, 50, 100]
const intervalOptions = [
  { label: '手动', value: 0 },
  { label: '1 小时', value: 60 },
  { label: '6 小时', value: SUBSCRIBE_INTERVAL_MINUTES },
  { label: '每天', value: 1440 },
  { label: '每周', value: 10080 },
]

function toggleExpand(row) {
  if (expandedKey.value === row.key) {
    expandedKey.value = ''
    return
  }
  expandedKey.value = row.key
  const mon = row.monitors[0] || {}
  const task = row.tasks[0] || {}
  editForm.value = {
    quality: mon.quality || 'lossless',
    max_downloads: mon.max_downloads ?? 30,
    exclude_kw: mon.exclude_kw || '',
    embed: mon.embed !== false,
    target_title: task.target_title || '',
    sync_cover: task.sync_cover !== false,
    retention_mode: task.retention_mode || 'keep',
    retention_days: task.retention_days || 0,
    interval_minutes: row.intervalMinutes || SUBSCRIBE_INTERVAL_MINUTES,
  }
}

/** 引擎的 saveRowSettings 要 (row, 表单值)；保存成功后收起这一行 */
async function onSaveSettings(row) {
  const ok = await saveRowSettings(row, editForm.value)
  if (ok) expandedKey.value = ''
}

// ── 新建订阅弹窗 ──
const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
const form = ref({
  name: '', links: '', interval: SUBSCRIBE_INTERVAL_MINUTES,
  wantDownload: true, wantFnos: true,
  sourceMode: 'link',
  provider: 'netease',
  sourceValue: '',
})

const canCreateSubscription = computed(() => {
  if (!form.value.name.trim()) return false
  if (form.value.sourceMode === 'account') return !!form.value.sourceValue
  return !!form.value.links.trim()
})

/** 打开弹窗（飞牛没连上时默认不勾「同步飞牛」，别让用户建一个建不成的） */
function openCreate() {
  form.value.name = ''
  form.value.links = ''
  form.value.interval = SUBSCRIBE_INTERVAL_MINUTES
  form.value.wantDownload = true
  form.value.wantFnos = fnosConnected.value
  form.value.sourceMode = 'link'
  form.value.provider = 'netease'
  form.value.sourceValue = ''
  createError.value = ''
  showCreate.value = true
}

// 账号来源（歌单 / 每日推荐）：没有可粘贴的公开链接，只能同步飞牛
const accountSources = ref([])
const sourcesLoading = ref(false)
const sourcesError = ref('')

async function loadAccountSources(provider) {
  sourcesLoading.value = true
  sourcesError.value = ''
  accountSources.value = []
  try {
    const res = await PushAPI.sources(provider)
    accountSources.value = res?.data?.sources || []
    if (accountSources.value.length) {
      form.value.sourceValue = accountSources.value[0].kind + ':' + accountSources.value[0].id
    }
  } catch (e) {
    sourcesError.value = fail('读取账号歌单', e)
  } finally {
    sourcesLoading.value = false
  }
}

function setSourceMode(m) {
  form.value.sourceMode = m
  if (m === 'account') {
    form.value.wantDownload = false
    if (!accountSources.value.length) loadAccountSources(form.value.provider)
  }
}

function changeProvider(p) {
  form.value.provider = p
  loadAccountSources(p)
}

async function createSubscription() {
  if (form.value.sourceMode === 'account') return createAccountSubscription()

  const name = form.value.name.trim()
  const links = form.value.links.split('\n').map((s) => s.trim()).filter(Boolean)
  if (!name) { createError.value = '请填写订阅名称'; return }
  if (!links.length) { createError.value = '请至少填写一个歌单链接'; return }
  if (!form.value.wantDownload && !form.value.wantFnos) {
    createError.value = '请至少选一项：下载到本地曲库 / 同步到飞牛歌单'
    return
  }

  creating.value = true
  createError.value = ''
  try {
    const { okList, failList } = await createLinkSubscriptions({
      name, links,
      wantDownload: form.value.wantDownload,
      wantFnos: form.value.wantFnos,
      interval: form.value.interval,
    })
    if (failList.length) {
      // 引擎已汇总提示「部分成功」，这里把第一条失败原因摆进弹窗
      createError.value = failList[0]
      return
    }
    flashOk(okList[0]?.message || '已订阅')
    showCreate.value = false
    form.value.links = ''
    form.value.name = ''
  } finally {
    creating.value = false
  }
}

async function createAccountSubscription() {
  const name = form.value.name.trim()
  if (!name) { createError.value = '请填写订阅名称'; return }
  creating.value = true
  createError.value = ''
  try {
    const res = await createAccountTask({
      name,
      provider: form.value.provider,
      sourceValue: form.value.sourceValue,
      interval: form.value.interval,
    })
    if (!res.ok) {
      createError.value = res.error || '创建失败'
      return
    }
    flashOk(res.message || '已订阅')
    showCreate.value = false
    form.value.name = ''
  } finally {
    creating.value = false
  }
}

// ── 飞牛同步历史（多选 + 清除；confirm 在调用方，引擎只管删）──
const pushRunSelected = ref(new Set())
const allPushRunsSelected = computed(() =>
  pushRuns.value.length > 0 && pushRuns.value.every((r) => pushRunSelected.value.has(r.id)))

function togglePushRunSelect(id) {
  const next = new Set(pushRunSelected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  pushRunSelected.value = next
}

function toggleAllPushRuns() {
  pushRunSelected.value = allPushRunsSelected.value
    ? new Set()
    : new Set(pushRuns.value.map((r) => r.id))
}

async function clearPushRuns(ids) {
  const n = ids.length
  if (!confirm(n ? `确定清除选中的 ${n} 条同步历史？` : '确定清除全部同步历史？（订阅任务本身不受影响）')) return
  await engineClearRuns(ids)
  pushRunSelected.value = new Set()
}

onMounted(() => {
  // 引擎的 start() 由 App.vue 常驻调用；这里只补历史列表（辅助信息，轻拉一次）
  loadPushRuns()
})
</script>
