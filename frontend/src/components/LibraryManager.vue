<template>
  <PageShell
    title="曲库管家"
    desc="推送订阅、批量下载、补全整理与榜单管理 —— 曲库的日常维护都在这里（AI 大模型的设置已挪到「账号连接」）"
  >
    <template #title-extra>
      <span class="rounded-full border border-emerald-200 bg-emerald-50 px-2 py-0.5 font-mono text-[10px] text-emerald-600">
        推送 · 下载 · 整理 · 榜单
      </span>
    </template>

    <template #actions>
      <button
        @click="reloadAll"
        :disabled="reloading"
        class="rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-xs font-semibold text-gray-600 transition-all hover:border-emerald-300 hover:text-emerald-600 disabled:opacity-50"
      >{{ reloading ? '刷新中…' : '刷新' }}</button>
    </template>

    <!--
      左侧栏分组导航 + 主区（布局参考 fnmusic-flow 的 .download-shell）
      flow: grid-template-columns:220px 1fr; gap:20px；侧栏 sticky top:88px
    -->
    <div class="grid grid-cols-1 gap-5 lg:grid-cols-[220px_1fr]">

      <!-- 侧栏：分组导航（分组标题 + 条目 + 数量角标） -->
      <aside class="h-max rounded-[15px] border border-gray-200 bg-white p-3 lg:sticky lg:top-6">
        <h4 class="mx-2.5 mb-3 mt-2 text-[11px] font-medium text-gray-400">曲库管家</h4>
        <button
          v-for="t in tabs" :key="t.id"
          @click="activeTab = t.id; savePref('library_tab', t.id)"
          :class="[
            'flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2.5 text-left text-[13px] transition-colors',
            activeTab === t.id ? 'bg-emerald-50 font-bold text-emerald-600' : 'text-gray-600 hover:bg-gray-50'
          ]"
        >
          <component :is="t.icon" class="w-4 h-4 shrink-0" />
          <span class="min-w-0 flex-1 truncate">{{ t.name }}</span>
          <span
            v-if="t.count"
            class="shrink-0 rounded-full bg-gray-100 px-1.5 py-0.5 text-[11px] text-gray-500"
          >{{ t.count }}</span>
        </button>

        <h4 class="mx-2.5 mb-3 mt-4 text-[11px] font-medium text-gray-400">任务状态</h4>

        <!-- 只列「需要你处理」的项；都为 0 时给一句明确结论，而不是摆一排 0 -->
        <button
          v-for="s in statusItems" :key="s.id"
          @click="s.go()"
          class="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2.5 text-left text-[13px] text-gray-600 transition-colors hover:bg-gray-50"
        >
          <component :is="s.icon" class="w-4 h-4 shrink-0" :class="s.iconClass" />
          <span class="min-w-0 flex-1 truncate">{{ s.label }}</span>
          <span class="shrink-0 rounded-full px-1.5 py-0.5 text-[11px]" :class="s.badgeClass">{{ s.count }}</span>
        </button>

        <p v-if="!statusItems.length" class="mx-2.5 flex items-center gap-2 py-1.5 text-[12px] text-gray-400">
          <Check class="w-3.5 h-3.5 shrink-0 text-emerald-500" />
          暂无待处理项
        </p>

        <!-- 上次整理时间：唯一一个「正常也要看」的状态 -->
        <p v-if="lastScanText" class="mx-2.5 mt-2 text-[11px] text-gray-400">{{ lastScanText }}</p>
      </aside>

      <!-- 主区 -->
      <main class="min-w-0 space-y-3">

        <!-- 指标行（随侧栏项变化，见 metrics computed） -->
        <div v-if="metrics.length" class="grid grid-cols-2 gap-3 md:grid-cols-4">
          <div v-for="m in metrics" :key="m.label" class="rounded-xl border border-gray-200 bg-white px-[17px] py-[15px]">
            <b class="block text-[24px] font-bold" :class="m.color">{{ m.value }}</b>
            <span class="text-[12px] text-gray-400">{{ m.label }}</span>
          </div>
        </div>

      <!-- ══ 推送 ══ -->
      <template v-if="activeTab === 'push'">
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
          class="rounded-xl border border-gray-200 bg-white p-4 shadow-2xs space-y-3">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <p class="text-[10px] font-bold uppercase tracking-wide text-emerald-600">首次使用</p>
              <h3 class="mt-1 text-[15px] font-semibold text-gray-800">
                {{ pushGuideAllDone ? '一切就绪，之后每轮自动追新' : '三步即可开始' }}
              </h3>
              <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
                一条订阅 = 一个歌单来源，上面有两个开关：下载到本地曲库 / 同步到飞牛歌单，勾一个也行。
              </p>
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
            <div v-for="s in pushGuideSteps" :key="s.id"
              class="flex flex-col rounded-xl border p-3.5"
              :class="s.done ? 'border-emerald-200 bg-emerald-50/40' : 'border-gray-200'">
              <div class="flex items-center gap-2.5">
                <span :class="[
                  'flex h-[23px] w-[23px] shrink-0 items-center justify-center rounded-full text-[12px] font-extrabold',
                  s.done ? 'bg-emerald-100 text-emerald-600' : 'bg-emerald-50 text-emerald-600',
                ]">
                  <Check v-if="s.done" class="h-3 w-3" />
                  <template v-else>{{ s.no }}</template>
                </span>
                <b class="text-[13px]" :class="s.done ? 'text-emerald-700' : 'text-gray-700'">{{ s.title }}</b>
              </div>
              <p class="mt-2.5 flex-1 text-[11px] leading-relaxed text-gray-500">
                {{ s.done ? s.doneHint : s.hint }}
              </p>
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
          class="flex flex-wrap items-center gap-2 rounded-xl border border-gray-200 bg-white px-[17px] py-3 text-[11px] text-gray-500 shadow-2xs">
          <span class="flex h-5 w-5 items-center justify-center rounded-md bg-emerald-100 text-emerald-600">
            <Check class="h-3 w-3" />
          </span>
          <span>使用引导已收起</span>
          <button class="text-emerald-600 hover:underline" @click="openPushGuide">展开看看</button>
        </div>

        <div class="flex items-stretch gap-3">
          <!-- 新建订阅的入口始终在右侧（引导收起后也不能没有它） -->
          <div class="min-w-0 flex-1" />
          <button
            class="flex shrink-0 items-center justify-center rounded-lg bg-emerald-500 px-4 text-[13px] font-semibold text-white transition-all hover:bg-emerald-600"
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
        -->
        <p
          v-if="pushGuideCollapsed && !apiSourceCount"
          class="rounded-xl border border-gray-200 bg-white px-4 py-3 text-[11px] leading-relaxed text-gray-500"
        >
          <b class="text-gray-700">「下载到本地曲库」需要让曲率 这个窗口一直开着。</b>
          取下载地址的脚本只能在浏览器里跑，飞牛里「应用已启动」（后端进程）还不够 ——
          关掉窗口后已排队的会停，下次打开继续。
          想关掉页面也能下：到「音源管理 → 按服务地址接入」配一个<b>服务型音源</b>，
          那种音源由后端直接取链，跟浏览器无关。
        </p>

        <!--
          飞牛没连上不影响「下载」这一位，但「同步飞牛」建不出来 —— 提前说清，
          别等用户勾了才报错（勾选框会置灰并带上原因）。
        -->
        <p
          v-if="!fnosConnected"
          class="rounded-xl border border-amber-100 bg-amber-50/70 px-4 py-3 text-[11px] leading-relaxed text-amber-900/80"
        >
          <b class="text-amber-700">飞牛音乐未连接</b>，「同步到飞牛歌单」暂时建不了。
          到「账号连接」登录飞牛音乐即可；只勾「下载到本地曲库」不受影响。
        </p>

        <div v-if="!subscriptions.length" class="rounded-xl border border-gray-100 bg-white py-12 text-center">
          <p class="mb-1 text-sm text-gray-500">还没有订阅</p>
          <p class="text-[11px] text-gray-400">点上方「新建订阅」开始</p>
        </div>

        <div v-else class="space-y-2.5">
          <div
            v-for="row in subscriptions" :key="row.key"
            class="rounded-xl border border-gray-100 bg-white p-4 shadow-2xs"
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
              <div v-if="row.hasDownload" class="space-y-2">
                <p class="text-[11px] font-semibold text-gray-600">下载到本地曲库</p>
                <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
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

              <div v-if="row.hasFnos" class="space-y-2" :class="row.hasDownload ? 'border-t border-gray-100 pt-3' : ''">
                <p class="text-[11px] font-semibold text-gray-600">同步到飞牛歌单</p>
                <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
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
                  保留期只清理<b>本任务推送过</b>的曲目，你手动加进这个歌单的歌不会被删。
                </p>
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
                  @click="saveRowSettings(row)"
                >保存设置</button>
              </div>
            </div>
          </div>
        </div>

        <!-- 飞牛同步历史：低频查看，放列表下方（原来在「推送歌单」页里独占一块） -->
        <div v-if="pushRuns.length" class="overflow-hidden rounded-xl border border-gray-200 bg-white">
          <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3">
            <h3 class="text-sm font-semibold text-gray-800">飞牛同步历史</h3>
            <span class="text-[11px] text-gray-400">最近 {{ pushRuns.length }} 条</span>
          </div>
          <div class="max-h-[260px] overflow-y-auto">
            <table class="w-full">
              <tbody>
                <tr v-for="r in pushRuns" :key="r.id" class="border-b border-gray-50 last:border-0">
                  <td class="whitespace-nowrap px-4 py-2 text-[12px] text-gray-500">{{ String(r.started_at || '').replace('T', ' ') }}</td>
                  <td class="px-4 py-2 text-[12px] text-gray-600">{{ r.task_name }}</td>
                  <td class="px-4 py-2">
                    <span class="rounded-full px-2 py-0.5 text-[11px]" :class="r.status === 'success' ? 'bg-emerald-50 text-emerald-600' : 'bg-rose-50 text-rose-600'">
                      {{ r.status === 'success' ? '成功' : '失败' }}
                    </span>
                  </td>
                  <td class="px-4 py-2 text-[12px] text-gray-500">
                    <span v-if="r.error" class="block max-w-[320px] truncate text-rose-500" :title="r.error">{{ r.error }}</span>
                    <span v-else-if="r.result">匹配 {{ r.result.matched }}/{{ r.result.total }}，新增 {{ r.result.added }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>

      <!-- ══ 下载（队列 + 歌单批量，两个子视图）══ -->
      <!--
        v2.1.26：「歌单下载」不再单独占一个侧栏项。它和「下载」本来就是同一个队列，
        只是提交入口不同 —— 分成两页会让人先找「我那条下到哪了」。
        两个子视图组件本身没动（DownloadQueueView / PlaylistDownload）。
      -->
      <template v-else-if="activeTab === 'download'">
        <!-- ── 下载行为开关（v2.1.92）────────────────────────────────────
             这一组开关都**真的控制行为**：收藏自动下载已经能跑（见
             backend/pkg/intercept/autodownload.go）。边听边下等其余几个
             等行为落地后再往这里加 —— 不放「点了没反应」的开关。 -->
        <div class="rounded-xl border border-gray-200 bg-white px-4 py-3 mb-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <h3 class="text-sm font-semibold text-gray-800">收藏自动下载并绑定本地</h3>
                <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono',
                               favAutoDownload ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-gray-200 text-gray-400']">
                  {{ favAutoDownload ? '已开启' : '已关闭' }}
                </span>
              </div>
              <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
                在飞牛音乐里收藏（或加入歌单）一首**在线**曲目时，后台把整轨下到 NAS 曲库；
                下完之后这首歌的取流改走本地文件 —— 收藏条目继续可用，但已经在听硬盘上那份。
                <span class="text-amber-600">会真的占磁盘和带宽（无损一首几十兆）。</span>
              </p>
            </div>
            <button
              role="switch"
              :aria-checked="favAutoDownload ? 'true' : 'false'"
              :disabled="favAutoBusy"
              @click="toggleFavAutoDownload"
              :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                       favAutoDownload ? 'bg-emerald-500' : 'bg-gray-200']">
              <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                             favAutoDownload ? 'translate-x-5' : '']" />
            </button>
          </div>
          <p v-if="favAutoMsg" class="mt-2 text-[11px] text-rose-600">{{ favAutoMsg }}</p>
        </div>

        <div class="rounded-xl border border-gray-200 bg-white px-4 py-3 mb-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <h3 class="text-sm font-semibold text-gray-800">边听边下</h3>
                <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono',
                               teeEnabled ? 'border-emerald-200 bg-emerald-50 text-emerald-600' : 'border-gray-200 text-gray-400']">
                  {{ teeEnabled ? '已开启' : '已关闭' }}
                </span>
              </div>
              <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
                播放在线曲目时，把**同一条流**顺手写进磁盘 —— 字节本来就从这儿过，
                不多花一分带宽。整首播完就提升进曲库，之后这首歌自动听硬盘那份。
                <span class="text-gray-400">中途切歌 / 拖动进度产生的半截文件会被丢弃，不会污染曲库。</span>
              </p>
            </div>
            <button
              role="switch"
              :aria-checked="teeEnabled ? 'true' : 'false'"
              :disabled="teeBusy"
              @click="toggleTee"
              :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                       teeEnabled ? 'bg-emerald-500' : 'bg-gray-200']">
              <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                             teeEnabled ? 'translate-x-5' : '']" />
            </button>
          </div>
          <p v-if="teeMsg" class="mt-2 text-[11px] text-rose-600">{{ teeMsg }}</p>
        </div>

        <div class="rounded-xl border border-gray-200 bg-white px-4 py-3 mb-3">
          <h3 class="text-sm font-semibold text-gray-800">下载时附带</h3>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
            只影响**曲率触发**的下载（收藏自动下载 / 边听边下提升 / 注入菜单里的下载）。
            <span class="text-amber-600">这两项缺省都是开</span> —— 关掉之后下下来的歌就没有歌词或封面了。
          </p>
          <div class="mt-3 space-y-3">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="text-xs font-medium text-gray-700">歌词</div>
                <p class="mt-0.5 text-[11px] leading-relaxed text-gray-500">
                  内嵌进文件标签（飞牛自己会显示），并写一份同名 <code class="font-mono">.lrc</code> 给别的播放器。
                </p>
              </div>
              <button
                role="switch"
                :aria-checked="lyricAutoDownload ? 'true' : 'false'"
                :disabled="lyricBusy"
                @click="toggleLyric"
                :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                         lyricAutoDownload ? 'bg-emerald-500' : 'bg-gray-200']">
                <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                               lyricAutoDownload ? 'translate-x-5' : '']" />
              </button>
            </div>
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="text-xs font-medium text-gray-700">封面内嵌</div>
                <p class="mt-0.5 text-[11px] leading-relaxed text-gray-500">
                  把封面抓下来写进文件标签。关掉之后这些歌在飞牛里没有封面图。
                </p>
              </div>
              <button
                role="switch"
                :aria-checked="coverEmbed ? 'true' : 'false'"
                :disabled="coverBusy"
                @click="toggleCover"
                :class="['relative w-11 h-6 rounded-full transition-colors shrink-0 disabled:opacity-50',
                         coverEmbed ? 'bg-emerald-500' : 'bg-gray-200']">
                <span :class="['absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform',
                               coverEmbed ? 'translate-x-5' : '']" />
              </button>
            </div>
          </div>
          <p v-if="extraMsg" class="mt-2 text-[11px] text-rose-600">{{ extraMsg }}</p>
        </div>

        <div class="flex items-center gap-1.5 mb-3">
          <button
            @click="setDownloadSub('queue')"
            :class="[
              'px-3 py-1.5 rounded-lg text-xs font-medium border transition-all',
              downloadSub === 'queue' ? 'bg-emerald-500 text-white border-emerald-500' : 'bg-white text-gray-600 border-gray-200 hover:border-emerald-300'
            ]"
          >
            <span class="flex items-center gap-1.5"><Download class="w-3.5 h-3.5" />下载队列</span>
          </button>
          <button
            @click="setDownloadSub('playlist')"
            :class="[
              'px-3 py-1.5 rounded-lg text-xs font-medium border transition-all',
              downloadSub === 'playlist' ? 'bg-emerald-500 text-white border-emerald-500' : 'bg-white text-gray-600 border-gray-200 hover:border-emerald-300'
            ]"
          >
            <span class="flex items-center gap-1.5"><ListMusic class="w-3.5 h-3.5" />歌单下载</span>
          </button>
        </div>
        <DownloadQueueView v-if="downloadSub === 'queue'" embedded :active-source="activeSource" />
        <PlaylistDownload v-else embedded />
      </template>

      <!-- ══ 补全整理（原「AI 补全」+「整理去重」）══ -->
      <!--
        补「歌曲信息」的六个字段：歌词 / 封面 / 风格 / 年份 / 曲序 / 光盘，
        并把体检 / 增量扫描 / 查重清理的结果放在同一页下方 —— 它们本来就是
        同一条流水线的上下游：先知道缺什么 → 补 → 补完看真实结果 → 顺手查重。

        铁律：**所有值都来自平台搜索结果里「已经匹配上的那一条」，不让 AI 猜**
        （2026-09-22 用户明确要求「一定要用搜索的，而不是 ai 瞎猜」）。
        实测过取错来源的后果：搜「晴天 周杰伦」网易前三条是翻唱（年份 2025、曲序 1），
        QQ 才是原版（2003 / 曲序 3）—— 写错比不写更糟。
        AI 只出现在「文件名判名 / 候选消歧 / 歌词核验」，风格要 AI 兜底得单独勾选。

        流程固定五步：扫描缺失 → 勾选字段 → 预览 → 确认写入 → 回读结果卡片。
        这一步会真正改写用户的音频文件，所以预览是主防线，「撤销」只是兜底。
      -->
      <template v-else-if="activeTab === 'complete'">
        <div class="bg-white rounded-xl border border-gray-100 p-4 shadow-2xs space-y-3">
          <div class="flex items-center gap-2">
            <Wand2 class="w-4 h-4 text-emerald-500" />
            <span class="text-xs font-semibold text-gray-700">补全整理</span>
            <span class="text-[11px] text-gray-400">补全的歌曲信息一律来自搜索结果，不由 AI 猜填</span>
          </div>

          <!--
            一个目录 + 四个动作，全在这一排。
            以前分成两页（体检/扫描/查重在「整理去重」，补全在「AI 补全」），
            两边各自一个目录输入框 —— 最容易出的事故就是两个框填的不是同一个库。
          -->
          <div class="flex items-center gap-2 flex-wrap">
            <input v-model="tidyDir" class="flex-1 min-w-[200px] px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
              :placeholder="nasDir || '/vol1/Music'" />
            <button @click="scanGaps" :disabled="gapsLoading"
              class="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold disabled:opacity-50">
              {{ gapsLoading ? '统计中…' : '扫描缺失' }}
            </button>
            <button @click="runIndexScan" :disabled="indexing"
              class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
              {{ indexing ? '扫描中…' : '增量扫描' }}
            </button>
            <button @click="runAudit" :disabled="auditing"
              class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
              {{ auditing ? '体检中…' : '体检' }}
            </button>
            <button @click="runDedupe" :disabled="deduping"
              class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 disabled:opacity-50">
              {{ deduping ? '查重中…' : '查找重复' }}
            </button>
          </div>

          <!--
            缺口统计只读索引（秒回），所以「还没读过标签」与「真的不缺」必须分清：
            unread 不为 0 时那些记录的缺口无从判断，先点「增量扫描」。
          -->
          <div v-if="gaps" class="space-y-2">
            <p class="text-[11px] text-gray-400">
              上次统计（{{ gapsAtText }}）· 共 {{ gaps.total }} 首，其中 {{ gaps.read }} 首读过标签
              <b class="text-amber-600">· 待补 {{ gaps.any_field }} 首</b>（至少缺一个勾选字段，点「预览」排队的就是这些）
              <span v-if="gaps.unread" class="text-amber-600">
                · 另有 {{ gaps.unread }} 首还没读过标签（先点「增量扫描」，这些只能算待查）
              </span>
              <span v-if="gaps.unsupported" class="text-gray-400">
                · 另有 {{ gaps.unsupported }} 首格式写不了标签<template v-if="unsupportedExtsText">（{{ unsupportedExtsText }}）</template>，补全不会动它们（要补得先转成 MP3/FLAC）
              </span>
            </p>
            <div class="grid grid-cols-3 gap-2 md:grid-cols-6">
              <div v-for="f in fieldOptions" :key="f.key"
                class="rounded-lg border border-gray-100 bg-gray-50 px-2 py-1.5 text-center">
                <p class="text-[10px] text-gray-400">{{ f.name }}</p>
                <p class="font-mono text-sm" :class="f.missing ? 'text-amber-600' : 'text-emerald-600'">
                  {{ f.missing }}
                </p>
              </div>
            </div>
          </div>
          <p v-else class="text-[11px] text-gray-400">
            先点「扫描缺失」，看看每个字段各缺多少 —— 只读索引，秒回，不会动任何文件。
          </p>

          <!-- 勾字段 -->
          <div class="grid grid-cols-1 gap-2 md:grid-cols-3">
            <label v-for="f in fieldOptions" :key="f.key"
              class="flex cursor-pointer items-start gap-2 rounded-lg border px-2.5 py-2"
              :class="selectedFields.includes(f.key) ? 'border-emerald-300 bg-emerald-50' : 'border-gray-200'">
              <input type="checkbox" class="mt-0.5" :value="f.key"
                :checked="selectedFields.includes(f.key)" @change="toggleField(f.key)" />
              <span class="min-w-0">
                <b class="text-xs text-gray-700">{{ f.name }}</b>
                <span class="ml-1 font-mono text-[10px] text-gray-400">缺 {{ f.missing }}</span>
                <em class="block text-[10px] not-italic leading-snug text-gray-400">{{ f.hint }}</em>
              </span>
            </label>
          </div>

          <div class="flex flex-wrap items-center gap-3">
            <label class="flex items-center gap-1 text-[11px] text-gray-600">
              本次最多
              <input v-model.number="completeLimit" type="number" min="1" max="200"
                class="w-16 rounded border border-gray-200 px-1.5 py-1 text-xs font-mono" />
              首
            </label>
            <label class="flex items-center gap-1 text-[11px] text-gray-600">
              <input type="checkbox" v-model="overwriteExisting" />
              覆盖已有的歌词/封面
            </label>
            <label class="flex items-center gap-1 text-[11px] text-gray-600" :title="aiGenreHint">
              <input type="checkbox" v-model="allowAIGenre" :disabled="!selectedFields.includes('genre')" />
              风格取不到时允许 AI 推断
            </label>
            <!--
              主力平台：默认网易 + QQ，酷狗/酷我只兜底「只有它们上有」的歌。
              它同时决定补全的搜索顺序，以及「兜底命中缺字段时回哪家借元数据」。
              写回 /api/config（带完整对象，别只发局部字段 —— 那会踩到别的设置）。
            -->
            <span class="flex items-center gap-1 text-[11px] text-gray-600">
              主力
              <button
                v-for="p in platformChips" :key="p.id"
                @click="togglePreferred(p.id)"
                :title="p.preferred ? '点击取消主力（仍会在兜底时用到）' : '点击设为主力平台'"
                :class="[
                  'rounded border px-1.5 py-0.5 text-[10px] transition-all',
                  p.preferred ? 'border-emerald-300 bg-emerald-50 text-emerald-700' : 'border-gray-200 bg-white text-gray-400',
                ]"
              >{{ p.name }}</button>
            </span>
            <button @click="runComplete" :disabled="previewing || completing || !selectedFields.length"
              class="ml-auto rounded-lg bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50">
              {{ previewing ? '匹配中…' : (completing ? '写入中…' : '预览要写的内容') }}
            </button>
          </div>
          <p v-if="!selectedFields.length" class="text-[11px] text-amber-600">
            至少勾一个字段 —— 一个都不勾时后端会按老口径补「歌词+封面」，那不是「什么都不做」。
          </p>

          <!--
            预览表：**还没写任何文件**。
            补全会真正改写用户的音频文件 —— 先让用户看清「文件 → 匹配到的歌 → 每个字段将写成什么」，
            确认后才落盘。撤销是兜底，这个才是主防线。
          -->
          <div v-if="completePlan" class="space-y-2 rounded-lg border border-gray-200 bg-white p-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span class="text-[11px] font-semibold text-gray-700">即将写入（还没有动过文件）</span>
              <span class="text-[11px] text-gray-400">
                共 {{ completePlan.total }} 首 · 匹配到 {{ completePlan.matched }} 首
                <span v-if="completePlan.matched_by_ai" class="text-violet-600">
                  · 其中 {{ completePlan.matched_by_ai }} 首由 AI 判断
                </span>
              </span>
            </div>

            <div class="max-h-56 overflow-y-auto rounded-lg border border-gray-100">
              <table class="w-full text-[11px]">
                <thead class="sticky top-0 bg-gray-50 text-gray-400">
                  <tr>
                    <th class="px-2 py-1.5 text-left font-medium">文件</th>
                    <th class="px-2 py-1.5 text-left font-medium">匹配到</th>
                    <th class="px-2 py-1.5 text-left font-medium">会写</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="it in completePlan.items" :key="it.path" class="border-t border-gray-50">
                    <td class="max-w-[200px] truncate px-2 py-1 font-mono text-gray-600" :title="it.path">
                      {{ baseName(it.path) }}
                    </td>
                    <td class="px-2 py-1">
                      <template v-if="it.matched">
                        <span class="text-gray-700">{{ it.matched_name }}</span>
                        <span class="text-gray-400"> · {{ platformName(it.matched_by) }}</span>
                        <span v-if="it.matched_by_ai" class="text-violet-600"> · AI 判断</span>
                      </template>
                      <span v-else class="text-amber-600">{{ it.note || '没匹配上' }}</span>
                    </td>
                    <td class="px-2 py-1 text-gray-500">
                      <template v-if="it.matched">
                        <span v-for="c in planCells(it)" :key="c.k" class="mr-1.5 inline-block">
                          {{ c.k }}<b class="font-mono text-gray-700">{{ c.v }}</b>
                        </span>
                        <span v-if="!planCells(it).length">—</span>
                        <!-- 元数据是从主力平台借的，要标出来：命中的是酷狗、年份却写着 QQ 的值 -->
                        <span v-if="it.metadata_from" class="block text-[10px] text-gray-400">
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
                class="rounded-lg bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                @click="executeComplete"
              >确认写入 {{ completePlan.matched }} 首</button>
              <button
                :disabled="completing"
                class="rounded-lg px-3 py-1.5 text-xs text-gray-400 hover:text-gray-600 disabled:opacity-50"
                @click="completePlan = null"
              >取消</button>
              <span class="text-[11px] text-gray-400">写入前会自动备份，可随时撤销</span>
            </div>
          </div>

          <!-- ── 补全进度 / 上次结果 ── -->
          <div
            v-if="completeJob"
            class="rounded-lg border border-gray-200 bg-white p-3 space-y-2"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-[11px] font-semibold text-gray-700">
                {{ completeTitle }}
              </span>
              <span class="flex shrink-0 items-center gap-2">
                <span v-if="completeJob.running" class="font-mono text-[11px] text-gray-400">
                  {{ completeJob.done }}/{{ completeJob.total }}
                </span>
                <!--
                  停止入口：只在正在跑的时候出现。
                  补全一批最多 200 首、每首都要打外网并改写文件，几分钟内没有任何叫停方式
                  是不可接受的 —— 点错了只能看着它把文件全改完。
                  不弹确认框：停止不是破坏性操作（已写入的保留、还能撤销），
                  弹窗反而会让人以为「停了会丢东西」而不敢停。
                -->
                <button
                  v-if="completeJob.running && !completeJob.cancelled"
                  :disabled="stopBusy"
                  class="rounded-lg bg-rose-50 px-2.5 py-1 text-[11px] text-rose-600 hover:bg-rose-100 disabled:opacity-50"
                  title="不再开始新的曲目；已写入的文件保留，可以用「撤销上一次补全」回退"
                  @click="stopComplete"
                >{{ stopBusy ? '停止中…' : '停止' }}</button>
              </span>
            </div>

            <!-- 进度条：一首歌要走两次外网请求，不显示进度会像卡住 -->
            <div v-if="completeJob.running" class="h-1.5 w-full overflow-hidden rounded-full bg-gray-100">
              <div
                class="h-full rounded-full bg-emerald-500 transition-all duration-300"
                :style="{ width: completePercent + '%' }"
              ></div>
            </div>
            <p v-if="completeJob.running && completeJob.current" class="truncate text-[11px] text-gray-400">
              正在处理：{{ completeJob.current }}
            </p>

            <InlineNotice :text="completeJob.error" />

            <p v-if="completeJob.summary" class="text-[11px] leading-relaxed text-gray-600">
              <span v-for="c in summaryCells" :key="c.k" class="mr-2 inline-block">
                {{ c.k }}<b class="font-mono" :class="c.color">{{ c.v }}</b>
              </span>
              <!--
                核验不通过 ≠ 没取到歌词。分开显示，否则用户看不出为什么没补上。
                文案用「对不上」而不是「核验失败」—— 前者用户能懂，后者是我们内部的叫法。
              -->
              <span v-if="completeJob.summary.lyric_rejected" class="text-amber-600">
                · {{ completeJob.summary.lyric_rejected }} 首歌词对不上，已跳过不写
              </span>
              <!--
                「被停止」和「失败」必须分开说：没处理不是出错，是用户自己叫停的，
                抹掉不提会让人以为所有歌都补过了。
              -->
              <span v-if="completeJob.summary.cancelled" class="text-rose-600">
                · {{ completeJob.summary.cancelled }} 首没处理（任务被停止）
              </span>
            </p>

            <!--
              撤销入口：只在「有备份且任务已结束」时出现。
              补全会真正改写用户的音频文件 —— 没有回滚手段的话，用户是不敢批量用的。
            -->
            <div
              v-if="completeJob.undo_available && !completeJob.running"
              class="flex flex-wrap items-center gap-2 border-t border-gray-100 pt-2"
            >
              <button
                :disabled="undoBusy"
                class="rounded-lg bg-gray-100 px-2.5 py-1 text-[11px] text-gray-600 hover:bg-gray-200 disabled:opacity-50"
                @click="undoComplete"
              >{{ undoBusy ? '还原中…' : '撤销上一次补全' }}</button>
              <span class="text-[11px] text-gray-400">
                把改过的文件还原成补全前的样子（还原后要重新补一次才能再补上）
              </span>
            </div>
          </div>

          <!--
            补全结果卡片墙：显示的是**回读文件得到的真实标签**（GET /api/nas/tags），
            不是「打算写成什么」—— 两者可以不一致（某首写失败、或文件被别的程序改过）。
            界面上说「补好了」而飞牛里没有，是最伤信任的一种错法。
            带 ✚ 的字段是这次真写进去的。
          -->
          <div v-if="cardsLoading || completedCards.length" class="space-y-2 rounded-lg border border-gray-200 bg-white p-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span class="text-[11px] font-semibold text-gray-700">
                {{ cardsLoading ? '正在回读文件标签…' : '补全后的歌曲信息（读自文件）' }}
              </span>
              <span class="text-[11px] text-gray-400">
                {{ completedCards.length }} 张卡片<span v-if="skippedCards.length"> · {{ skippedCards.length }} 首没补上</span>
              </span>
            </div>

            <!-- 本次勾选了什么、各成功几首（没勾的置灰，别让人以为也顺手补了） -->
            <div class="flex flex-wrap gap-1.5">
              <span v-for="b in completeBar" :key="b.key"
                :class="[
                  'rounded-lg border px-2 py-1 text-[10px]',
                  b.selected ? 'border-emerald-200 bg-emerald-50 text-emerald-700' : 'border-gray-100 bg-gray-50 text-gray-400',
                ]">
                {{ b.name }} <b class="font-mono">{{ b.count }}</b>
                <i v-if="!b.selected" class="not-italic">· 未勾</i>
              </span>
            </div>

            <div class="grid grid-cols-1 gap-2 md:grid-cols-2">
              <div v-for="c in completedCards" :key="c.path"
                class="flex gap-2 rounded-lg border border-gray-100 bg-gray-50/60 p-2">
                <img v-if="c.hasCover" :src="c.coverUrl" :alt="c.title" class="h-14 w-14 shrink-0 rounded object-cover" loading="lazy" />
                <div v-else class="flex h-14 w-14 shrink-0 items-center justify-center rounded bg-gray-200">
                  <ImageIcon class="h-4 w-4 text-gray-400" />
                </div>
                <div class="min-w-0 flex-1">
                  <p class="truncate text-[11px] font-semibold text-gray-700" :title="c.path">
                    {{ c.title || baseName(c.path) }}
                  </p>
                  <p class="truncate text-[10px] text-gray-500">
                    {{ c.artist || '未标注歌手' }}<span v-if="c.album"> · {{ c.album }}</span>
                  </p>
                  <p class="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-[10px] text-gray-500">
                    <span v-for="fd in cardFields(c)" :key="fd.k">
                      {{ fd.k }}
                      <b :class="['font-mono', fd.wrote ? 'text-emerald-600' : 'text-gray-600']">{{ fd.v }}</b>
                      <i v-if="fd.wrote" class="not-italic text-emerald-500">✚</i>
                    </span>
                  </p>
                </div>
              </div>
            </div>
          </div>

          <!-- 没补成的单独一组：绝不混进卡片墙装成「补好了」 -->
          <div v-if="skippedCards.length" class="space-y-1 rounded-lg border border-amber-200 bg-amber-50/60 p-3">
            <p class="text-[11px] font-semibold text-amber-800">这些没补上（{{ skippedCards.length }} 首）</p>
            <p v-for="c in skippedCards" :key="c.path" class="truncate text-[11px] text-amber-900/80">
              {{ baseName(c.path) }} —— {{ c.error || '没匹配到曲目' }}
            </p>
          </div>
        </div>

        <!-- ══ 体检 · 索引 · 查重结果（动作都在上面那一排）══ -->
        <div class="bg-white rounded-xl border border-gray-100 p-4 shadow-2xs space-y-3">
          <div class="flex flex-wrap items-center gap-2">
            <FolderOpen class="w-4 h-4 text-emerald-500" />
            <span class="text-xs font-semibold text-gray-700">体检 · 索引 · 查重</span>
            <span class="text-[11px] text-gray-400">点上面的「体检 / 增量扫描 / 查找重复」，结果落在这里</span>
          </div>

          <!-- 索引快照：只说来源和时间，数字本体在下面的体检结果里 -->
          <p v-if="statsLoading" class="text-[11px] text-gray-400">正在读取上次整理快照…</p>
          <p v-else-if="indexStats" class="text-[11px] text-gray-400">
            {{ lastScanText || '索引还没整理过' }} —— 点「增量扫描」会更新。
            下面这组数字是<b class="text-gray-500">直接扫文件</b>得到的（跟「扫描缺失」的索引口径各算各的），
            两边对不上就说明索引过期了。
          </p>

          <!-- 体检结果 -->
          <p v-if="audit && auditAtText" class="text-[11px] text-gray-400">
            上次体检结果（{{ auditAtText }}）—— 重新点「体检」会更新
          </p>
          <div v-if="audit" class="grid grid-cols-2 md:grid-cols-4 gap-2">
            <div class="p-2.5 rounded-lg bg-gray-50 border border-gray-100">
              <div class="text-[10px] text-gray-400">曲目总数</div>
              <div class="text-lg font-bold text-gray-700 font-mono">{{ audit.total }}</div>
            </div>
            <div class="p-2.5 rounded-lg bg-amber-50 border border-amber-100">
              <div class="text-[10px] text-amber-600">缺歌词</div>
              <div class="text-lg font-bold text-amber-700 font-mono">{{ audit.no_lyric }}</div>
            </div>
            <div class="p-2.5 rounded-lg bg-amber-50 border border-amber-100">
              <div class="text-[10px] text-amber-600">缺封面</div>
              <div class="text-lg font-bold text-amber-700 font-mono">{{ audit.no_cover }}</div>
            </div>
            <div class="p-2.5 rounded-lg bg-rose-50 border border-rose-100">
              <div class="text-[10px] text-rose-600">两者都缺</div>
              <div class="text-lg font-bold text-rose-700 font-mono">{{ audit.both_missing }}</div>
            </div>
          </div>

          <!-- 增量扫描结果 -->
          <div v-if="indexResult" class="p-3 rounded-lg bg-gray-50 border border-gray-100 text-[11px] text-gray-600">
            增量扫描：新增 <b class="font-mono">{{ indexResult.added }}</b> ·
            变化 <b class="font-mono">{{ indexResult.changed }}</b> ·
            未变 <b class="font-mono">{{ indexResult.unchanged }}</b> ·
            索引总数 <b class="font-mono">{{ indexResult.index_total }}</b>
            <div v-if="!indexResult.complete" class="mt-1 text-amber-700">
              ⚠️ 本次枚举不完整，已跳过删除判定（保护索引不被误清）
            </div>
          </div>

          <!-- 重复结果 -->
          <div v-if="dupResult" class="space-y-2">
            <div class="flex items-center justify-between">
              <span class="text-xs text-gray-600">
                发现 <b class="text-rose-600 font-mono">{{ dupResult.group_count }}</b> 组重复
              </span>
              <div class="flex gap-1.5">
                <button @click="resolveDup(true)" class="px-2.5 py-1 rounded-lg bg-gray-100 hover:bg-gray-200 text-[11px] text-gray-600">干跑预览</button>
                <button @click="resolveDup(false)" class="px-2.5 py-1 rounded-lg bg-rose-50 hover:bg-rose-100 text-[11px] text-rose-600">移入回收站</button>
              </div>
            </div>
            <div v-if="dupResult.groups.length" class="space-y-1.5 max-h-64 overflow-y-auto">
              <div v-for="(g, i) in dupResult.groups.slice(0, 30)" :key="i"
                class="p-2.5 rounded-lg bg-white border border-gray-100 text-[11px]">
                <div class="flex items-center gap-2">
                  <span :class="['px-1.5 py-0.5 rounded text-[10px] font-mono', g.type === 'content' ? 'bg-rose-50 text-rose-600' : 'bg-amber-50 text-amber-600']">
                    {{ g.type === 'content' ? '内容相同' : '同名不同版' }}
                  </span>
                  <span class="text-gray-400">{{ g.count }} 个文件</span>
                </div>
                <div class="mt-1 text-emerald-700">保留：{{ baseName(g.keep) }}</div>
                <div class="text-rose-500">清理：{{ g.delete.map(baseName).join('、') }}</div>
              </div>
            </div>
            <div v-if="dupResolveResult" class="p-2.5 rounded-lg bg-gray-50 border border-gray-100 text-[11px] text-gray-600">
              {{ dupResolveResult.dry_run ? '干跑估算' : '执行完成' }}：
              移动 <b class="font-mono">{{ dupResolveResult.moved }}</b> ·
              删除 <b class="font-mono">{{ dupResolveResult.deleted }}</b> ·
              释放 <b class="font-mono">{{ fmtSize(dupResolveResult.freed_bytes) }}</b>
              <div v-if="dupResolveResult.errors && dupResolveResult.errors.length" class="mt-1 flex flex-wrap items-center gap-2 text-rose-500">
                <span>{{ dupResolveResult.errors.length }} 个文件处理失败</span>
                <button
                  class="underline decoration-dotted underline-offset-2 hover:no-underline"
                  @click="navigateTo('logs')"
                >查看日志</button>
              </div>
            </div>
          </div>
        </div>
      </template>

      <!-- ══ 榜单管理（v2.1.119 从「发现音乐」搬进来）══ -->
      <!--
        组件内部零改动：勾选/开关/榜单列表缓存都是它自己的（配置状态在
        services/chartInject.js 模块级单例里，跨页面共享 —— 「排行榜单」卡片上的
        「已注入」徽标与这里的勾选仍是同一份事实）。
        ▶ 预览按钮：原来 emit 给发现页的兄弟组件，现在隔了一层（发现页在别的顶层视图），
          改走导航总线（navigateTo('search', { chartPreview })）—— 见 setup 里的说明。
      -->
      <template v-else-if="activeTab === 'charts'">
        <ChartManager @preview="onChartPreview" />
      </template>

      <!-- 全局提示：成功给一句结论；失败给一句原因 + 「查看日志」入口 -->
      <InlineNotice :text="message" tone="ok" />
      <InlineNotice :text="error" />
      </main>
    </div>

    <!-- ── 新建订阅弹窗 ── -->
    <Teleport to="body">
      <div v-if="showCreate" class="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4" @click.self="showCreate = false">
        <div class="w-full max-w-[520px] overflow-hidden rounded-2xl bg-white shadow-2xl">
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
                  支持网易云 / QQ 音乐的公开歌单，<b class="font-semibold">App 复制的分享短链也能直接粘</b>。每行一个。
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
                  需要先在「账号连接」登录。账号来源没有可粘贴的链接，<b class="font-semibold">只能同步到飞牛歌单</b>，不能自动下载。
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
                      ? '账号来源（歌单 / 每日推荐）不能自动下载，只能同步飞牛'
                      : '每轮发现的新歌自动下载到本地（音质、单轮上限创建后可在「设置」里改）' }}
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
import { ref, computed, watch, onMounted, onUnmounted, onActivated } from 'vue'
import { markLoaded, isStale } from '../services/viewCache'
import { MonitorAPI, TidyAPI, PushAPI, SearchAPI, FnosAPI, DownloadAPI, SourcesAPI, TagsAPI, NasAPI, ConfigAPI } from '../api/client'
import { loadPref, savePref, loadBool, loadCache, saveCache } from '../services/prefs'
import {
  FIELD_DEFS, DEFAULT_FIELDS, parseFields, toggleFieldKey, platformName, planCells,
  summaryCells as _summaryCells, summaryBar, cardModel, cardFields, splitCards, mapWithLimit,
} from '../services/libraryFields'
// 这一页「看法」上的规则（侧栏任务状态 / 相对时间 / 缺口文案 / 补全四态）都搬到了服务层，
// 因为组件里的 computed 没法单测（项目没有 DOM 环境），而这些规则每一条都对应过一次真机事故。
import {
  normalizeLibraryTab, lastScanText as _lastScanText, gapsAtText as _gapsAtText,
  unsupportedExtsText as _unsupportedExtsText, gapsPendingCount as _gapsPendingCount,
  sidebarStatusItems, completePercent as _completePercent, aiMatchedCount as _aiMatchedCount,
  completeDoneText, completeTitleText as _completeTitleText,
} from '../services/libraryView'
import { FolderOpen, Trash2, Wand2, Send, Download, ListMusic, Loader2, AlertTriangle, Check, Image as ImageIcon, Trophy } from 'lucide-vue-next'
import PageShell from './PageShell.vue'
import DownloadQueueView from './DownloadQueueView.vue'
import PlaylistDownload from './PlaylistDownload.vue'
import ChartManager from './ChartManager.vue'
import SelectField from './SelectField.vue'
import InlineNotice from './InlineNotice.vue'
import { downloadManager } from '../services/downloadManager'
import { fail, apiError } from '../services/userMsg'
import { logError } from '../services/appLog'
import { pickAlreadyLocal } from '../services/downloadDedup'
import { navigateTo } from '../services/navBus'
import {
  buildSubscriptions, setCapability, setPushEnabled, subscribePlaylist, fnosReady,
  SUBSCRIBE_INTERVAL_MINUTES, pushGuideSteps as pushGuideStepsOf, pushGuideDoneCount,
} from '../services/subscribe'

// 下载队列排空后、自动触发推送前的等待时间。
// 给飞牛曲库重扫留出时间（`pkg/fnos/rescan.go` 的 debounce 是 30 秒），
// 否则刚下载的歌还没进曲库，推送会一首都匹配不上 —— 用户看到的就是「歌单里没歌」。
const AUTO_PUSH_SETTLE_MS = 90000

// 下载面板需要知道当前音源（取直链时要用）
const props = defineProps({
  activeSource: Object,
  /** 外部指定的侧栏项（如从下载抽屉跳转过来），配合 tabNonce 使用 */
  tab: { type: String, default: '' },
  /** 每次外部跳转自增；侧栏项可能相同，靠它保证 watch 一定触发 */
  tabNonce: { type: Number, default: 0 },
})

// 外部跳转：切到指定侧栏项并记忆
watch(
  () => props.tabNonce,
  () => {
    if (!props.tab) return
    applyTab(props.tab)
  },
)

// 侧栏分组导航的条目（icon 用于左侧栏；count 为数量角标，无则显示空）
//
// v2.1.26 精简：6 个标签 → 4 个。
// 「整理去重」并进「AI 补全」（同一件事：先知道缺什么，再补，再顺手查重清理），
// 改名**补全整理**；「歌单下载」并进「下载」（本来就是同一个队列，只是入口不同）。
//
// ⚠️ 旧 id 会留在浏览器偏好和其他组件的跳转参数里，所以必须做映射 ——
// 不映射的话从下载抽屉点「查看队列」会落到一个已经不存在的面板，看着像点了没反应。
// 映射表本身（tidy/playlist 是合并前的两页；**ai 整块搬到了「账号连接」**）在
// services/libraryView.js 里，用 `libraryView.test.mjs` 钉住。
const normalizeTab = normalizeLibraryTab

/** 跳到某个侧栏项（旧 id 自动落到新位置） */
function goTab(id) {
  applyTab(id)
}

/**
 * 榜单管理页点某行的 ▶「看一眼」：跳到发现页的「排行榜单」并打开那张榜单的详情。
 *
 * 搬进管家之前这件事是兄弟组件间的一次 emit；现在发现页在另一个顶层视图里，
 * 直接 import 会成环（SearchView → LibraryManager → SearchView），
 * 所以走导航总线：App.vue 负责切视图并把负载转给 SearchView（见 App.vue 的 onNavigate）。
 */
function onChartPreview(item) {
  navigateTo('search', { chartPreview: item })
}

function applyTab(id) {
  // 落到「下载」时顺带决定看哪个子视图：从歌单入口来的看歌单，其余看队列
  if (id === 'playlist') downloadSub.value = 'playlist'
  else if (id === 'download') downloadSub.value = 'queue'
  activeTab.value = normalizeTab(id)
  savePref('library_tab', activeTab.value)
}

const tabs = computed(() => [
  { id: 'push', name: '推送', icon: Send, count: pushTotalCount.value || '' },
  { id: 'download', name: '下载', icon: Download, count: downloadManager.activeCount.value || '' },
  // 补全整理：一页走完「看缺什么 → 勾字段 → 预览 → 确认 → 回读结果」，
  // 体检/索引/查重的结果都在同页下方，不再单独占一个标签。
  { id: 'complete', name: '补全整理', icon: Wand2, count: gapsPendingCount.value || '' },
  // 榜单管理（v2.1.119 从「发现音乐 → ⚙️ 榜单管理」搬进来）：
  // 「哪些榜单出现在飞牛音乐里」是曲库的日常维护 —— 与推送/下载/补全同属一页说得通。
  // 组件内部零改动（勾选/开关/缓存都是它自己的），这里只管挂载与跳转。
  { id: 'charts', name: '榜单管理', icon: Trophy },
])

/**
 * 侧栏「任务状态」：**只列需要处理**的事，每一项都能点进去处理。
 *
 * 顺序、文案、计数口径、配色档位、跳转目标都由 services/libraryView.js 决定
 * （那边有单测，钉住「跑着的补全压住失败项」「恒为 0 的不出现」这类容易回归的地方）；
 * 这里只负责把「档位」翻成图标与 class。
 */
const STATUS_ICONS = {
  'complete-running': Loader2, 'complete-failed': AlertTriangle,
  'dl-active': Loader2, 'dl-failed': AlertTriangle, 'dl-paused': Loader2,
  'push-running': Loader2, baseline: AlertTriangle,
  'push-failed': AlertTriangle, 'push-pending': Loader2, 'push-missing': AlertTriangle,
}
const STATUS_ICON_CLASS = {
  progress: 'text-emerald-500', warn: 'text-amber-500', error: 'text-rose-500', muted: 'text-gray-400',
}
const STATUS_BADGE_CLASS = {
  progress: 'bg-emerald-50 text-emerald-600', warn: 'bg-amber-50 text-amber-600',
  error: 'bg-rose-50 text-rose-600', muted: 'bg-gray-100 text-gray-500',
}

const statusItems = computed(() =>
  sidebarStatusItems({
    completeJob: completeJob.value,
    download: {
      active: downloadManager.activeCount.value,
      failed: downloadManager.failedCount.value,
      paused: downloadManager.pausedCount.value,
    },
    pushRunning: runningIds.value.length,
    pendingBaseline: pendingBaseline.value,
    monitorStats: monitorStats.value,
  }).map((s) => ({
    ...s,
    icon: STATUS_ICONS[s.id] || AlertTriangle,
    iconClass: STATUS_ICON_CLASS[s.tone] || STATUS_ICON_CLASS.muted,
    badgeClass: STATUS_BADGE_CLASS[s.tone] || STATUS_BADGE_CLASS.muted,
    go: () => goTab(s.tab),
  }))
)

/** 「上次整理」的相对时间；没有快照时返回空串。`scanned_at` 是**秒**（服务层钉住单位） */
const lastScanText = computed(() => _lastScanText(indexStats.value))

/** 侧栏角标 = 自动下载任务数 + 飞牛推送任务数（两类都是「推送」） */
const pushTasks = ref([])
const pushTotalCount = computed(() => monitors.value.length + pushTasks.value.length)

/** 后端 /api/monitors 已经算好的聚合统计（此前前端完全没用上） */
const monitorStats = ref(null)

async function loadPushTasks() {
  try {
    const res = await PushAPI.tasks()
    if (res?.code === 200) {
      const list = res.data?.tasks || res.data || []
      pushTasks.value = Array.isArray(list) ? list : []
    }
  } catch (_) {
    // 角标与指标是辅助信息，拉不到不影响主流程
  }
}

/**
 * 飞牛音乐是否可用。
 *
 * 它决定「同步到飞牛歌单」这个能力位能不能**新建**（已有记录仍可勾/取消，
 * 因为那只是改 enabled，不需要飞牛在线）。提前探一次，别等用户点了才报错。
 */
const fnosConnected = ref(false)
async function loadFnosStatus() {
  fnosConnected.value = await fnosReady()
}

/** 飞牛同步历史：低频查看，放在订阅列表下方（原「推送歌单」页里独占一块） */
const pushRuns = ref([])
async function loadPushRuns() {
  try {
    const res = await PushAPI.runs('', 20)
    if (res?.code === 200) pushRuns.value = res.data?.runs || []
  } catch (_) {
    // 历史是辅助信息
  }
}
// 旧浏览器偏好里可能还留着 'tidy' / 'playlist'，normalizeTab 把它们落到新位置
const activeTab = ref(normalizeTab(loadPref('library_tab', 'push')))
// 「下载」页里的两个子视图：队列（含历史）/ 歌单批量下载
const downloadSub = ref(loadPref('download_sub', 'queue'))

function setDownloadSub(v) {
  downloadSub.value = v
  savePref('download_sub', v)
}

const monitors = ref([])
const runningIds = ref([])
const creating = ref(false)
const showCreate = ref(false)
const message = ref('')
const error = ref('')

const nasDir = computed(() => loadPref('nas_dir', ''))
const tidyDir = ref('')
const auditing = ref(false)
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

let pollTimer = null

const form = ref({
  name: '', links: '', interval: SUBSCRIBE_INTERVAL_MINUTES,
  wantDownload: true, wantFnos: true,
  // 来源方式：link = 粘贴歌单链接（可订阅、可下载）；account = 已登录账号里的歌单/日推
  sourceMode: 'link',
  provider: 'netease',
  sourceValue: '',
})

/**
 * 账号来源（歌单 / 每日推荐）。
 *
 * 它和「粘贴链接」不是一回事：账号歌单没有可粘贴的公开链接，
 * 而且「每日推荐」每天都在变、监控侧不支持，所以这种来源**只能同步到飞牛歌单**。
 * 见 `AccountManager` 里「登录成功后到曲库管家→推送就能把日推同步到飞牛」的引导。
 */
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
    // 账号来源只能同步飞牛，没有「下载到本地曲库」这一位
    form.value.wantDownload = false
    if (!accountSources.value.length) loadAccountSources(form.value.provider)
  }
}

function changeProvider(p) {
  form.value.provider = p
  loadAccountSources(p)
}

/**
 * 统一订阅列表：一个来源一行，带 download / fnos 两个能力位。
 *
 * 归并逻辑在 services/subscribe.js 的 `buildSubscriptions()` —— 纯函数、有单测。
 * 这里不自己拼装，避免「界面一套口径、测试另一套口径」。
 */
const subscriptions = computed(() => buildSubscriptions(monitors.value, pushTasks.value))

// 推送页顶部三步引导（形态照 fnmusic-flow，判据见 services/subscribe.js 的 pushGuideSteps）。
// 收起**由用户点**：flow 也不会自己消失 —— 三步没做完的人把引导藏起来就是把他卡住的地方藏起来。
const pushGuideSteps = computed(() =>
  pushGuideStepsOf(subscriptions.value, {
    apiSourceCount: apiSourceCount.value,
    // 间隔是每条订阅各存的；引导只给一句人话，取第一条的（没有就用默认值）
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

/** 步骤卡上的按钮：跳去做这件事的地方（跳转描述由 pushGuideSteps 给，这里只负责解释） */
function runGuideAction(step) {
  const to = step?.action?.to
  if (!to) return
  if (to.kind === 'view') navigateTo(to.name)
  else if (to.kind === 'create') openCreate()
  else if (to.kind === 'tab') applyTab(to.name)
}

/** 正在处理的行（`<rowKey>:<动作>`）：用来禁用按钮，防止连点建出重复任务 */
const busyKey = ref('')
function rowBusy(row) {
  return !!busyKey.value && busyKey.value.startsWith(`${row.key}:`)
}

/**
 * 已启用的「服务型音源」数量。
 *
 * ⚠️ 它决定**关掉页面还能不能下载**：
 *  - 服务型音源 → 后端直连取链（`pkg/api/monitor_dispatcher.go` 的 `CanResolve`），跟浏览器无关
 *  - 脚本型音源（LX `.js`）→ 只能在浏览器里跑，页面关了就停
 *
 * 为 0 时界面上要提示「保持这个窗口开着」—— 这件事以前只在**报错文案**里提过，
 * 而且说的是「网页端」，用户直接问过「网页开着什么意思，飞牛里应用启动算吗」（2026-09-22）。
 */
const apiSourceCount = ref(0)
async function loadApiSourceCount() {
  try {
    const res = await SourcesAPI.apiList()
    const list = res?.data?.sources
    apiSourceCount.value = Array.isArray(list)
      ? list.filter((s) => s.enabled !== false && s.base_url).length
      : 0
  } catch (_) {
    // 探测失败按 0 处理：提示偏保守，不会让用户误以为关掉页面也能下
  }
}

/** 展开设置的行（一次只展开一行，订阅多起来时整页会被撑得很长） */
const expandedKey = ref('')

const intervalOptions = [
  { label: '手动', value: 0 },
  { label: '1 小时', value: 60 },
  { label: '6 小时', value: SUBSCRIBE_INTERVAL_MINUTES },
  { label: '每天', value: 1440 },
  { label: '每周', value: 10080 },
]

/**
 * 「每次下载数量」的预设值。
 *
 * ⚠️ 上限 300 是后端 `monitor.MaxDownloadsPerRun` —— 它不是节流阀，只是防呆
 * （拦「手输 999999」把一轮 run 拖成几小时）。别在前端写一个更小的上限，
 * 否则用户填了不生效又不知道为什么。
 */
const batchOptions = [10, 30, 50, 100]

function intervalText(minutes) {
  const m = Number(minutes || 0)
  if (!m) return '仅手动'
  if (m % 10080 === 0) return `${m / 10080} 周`
  if (m % 1440 === 0) return `${m / 1440} 天`
  if (m % 60 === 0) return `${m / 60} 小时`
  return `${m} 分钟`
}

/** 行内设置表单（展开时按当前行填充） */
const editForm = ref({
  quality: 'lossless', max_downloads: 30, exclude_kw: '', embed: true,
  target_title: '', sync_cover: true, retention_mode: 'keep', retention_days: 0,
  interval_minutes: SUBSCRIBE_INTERVAL_MINUTES,
})

const createError = ref('')

/** 「创建订阅」按钮可不可点：两种来源的必填项不一样 */
const canCreateSubscription = computed(() => {
  if (!form.value.name.trim()) return false
  if (form.value.sourceMode === 'account') return !!form.value.sourceValue
  return !!form.value.links.trim()
})

/**
 * 勾选框能不能点。
 *
 * ⚠️ 拦住的两种情况**都要给出原因**（只说「不能点」用户会以为界面坏了）：
 *   · 没有记录、要新建 → 得先认得出平台与链接（`row.canCreate`）
 *   · 「同步飞牛」要新建时还得飞牛在线（已有记录不受影响 —— 勾/取消只是改 enabled）
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

/**
 * 药丸的悬停说明：被拦住时给原因，否则说清「点一下会发生什么」。
 *
 * ⚠️ 「停止」是**两侧一起停**（见 `services/subscribe.js` 的 `setCapability`）——
 * 用户反馈过「点了停止怎么还在推」，所以这里必须把这一点说出来，
 * 否则用户会以为点的是「只停这一侧」。
 */
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

/** 展开设置，并把当前行的值填进编辑表单 */
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

/** 打开新建弹窗（飞牛没连上时默认不勾「同步飞牛」，别让用户建一个建不成的） */
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

/**
 * 切换一个能力位。
 *
 * 有记录 → 只改 enabled（配置留着，「取消」= 停止，不是删除）；
 * 没记录 → 真的去建一条（走 `subscribePlaylist`，**只建这一边**）。
 */
async function toggleCapability(row, which, next) {
  if (!row || busyKey.value) return
  const has = which === 'download' ? row.hasDownload : row.hasFnos
  busyKey.value = `${row.key}:${which}`
  try {
    if (has) {
      await setCapability(row, which, next)
      flash(
        next
          ? which === 'download'
            ? '已开启「下载到本地曲库」'
            : '已开启「同步到飞牛歌单」'
          : '已停止整个订阅（下载与同步飞牛都停了；记录、已下载的文件、飞牛歌单都保留）',
      )
    } else {
      const res = await subscribePlaylist({
        link: row.link,
        id: row.id,
        source: row.source,
        name: row.name,
        wantDownload: which === 'download',
        wantFnos: which === 'fnos',
        intervalMinutes: row.intervalMinutes || SUBSCRIBE_INTERVAL_MINUTES,
      })
      if (!res.ok) return flash(res.error || '创建失败', true)
      flash(res.message || '已创建')
    }
    // 停止要**立刻**生效，不能只等服务端 enabled 落库：
    //  · 正在入队的那一批（`enqueueSongs` 逐首搜索）靠 `stoppedKeys` 中止
    //  · 已经排队的下载靠 `stopByRow` 停掉
    //  · 90 秒后自动推一次的那个定时器靠 `autoPushRow` 二次复查拦住
    if (has && !next) {
      stoppedKeys.add(row.key)
      downloadManager.stopByRow(row.key)
    } else {
      stoppedKeys.delete(row.key)
    }
    await Promise.all([loadMonitors(), loadPushTasks()])
  } catch (e) {
    flash(fail('切换订阅能力', e), true)
  } finally {
    busyKey.value = ''
  }
}

/**
 * 只停「同步到飞牛歌单」这一侧 —— 本地下载继续跑。
 *
 * 药丸点关闭是**两侧一起停**（v2.1.17 用户明确要求），但「下载继续、先别往飞牛推」
 * 也是合理诉求，所以单独给一个只动推送任务的入口（用户 2026-09-22 提出）。
 * 要恢复：点一下「同步到飞牛歌单」药丸即可（开启只开点的那一侧）。
 */
async function stopPushOnly(row) {
  if (!row || busyKey.value || !row.tasks.length) return
  busyKey.value = `${row.key}:fnos-only`
  try {
    await setPushEnabled(row, false)
    flash('已停止同步到飞牛歌单（本地下载不受影响）')
    await Promise.all([loadMonitors(), loadPushTasks()])
  } catch (e) {
    flash(fail('停止同步', e), true)
  } finally {
    busyKey.value = ''
  }
}

/** 立即执行：把**已勾选**的能力位各触发一次（下载侧发现 / 飞牛侧推送） */
async function runRow(row) {
  if (!row || busyKey.value) return
  const jobs = []
  if (row.download) for (const m of row.monitors) jobs.push(MonitorAPI.run(m.id))
  if (row.fnos) for (const t of row.tasks) jobs.push(PushAPI.run(t.id))
  if (!jobs.length) return flash('这个订阅没有开启任何能力位', true)

  busyKey.value = `${row.key}:run`
  try {
    await Promise.all(jobs)
    flash('已触发，稍后自动刷新')
    setTimeout(async () => {
      await loadMonitors()
      loadPushTasks()
      loadPushRuns()
      // 监控轮把新歌登记成「待下载」之后**立刻**自动补一次。
      // 用户点「立即执行」就是想让歌下下来，不该再干等一个 8 秒轮询周期
      // （后端 run 是异步的，所以给 5 秒让发现+登记先跑完；没赶上还有轮询兜底）。
      autoFillPending()
    }, 5000)
  } catch (e) {
    flash(fail('执行订阅', e), true)
  } finally {
    busyKey.value = ''
  }
}

/** 删除订阅：两侧记录一起删（已下载的文件、已推送的飞牛歌单都不动） */
async function removeRow(row) {
  if (!row || busyKey.value) return
  if (!confirm(`删除「${row.name}」？两侧任务记录都会删掉。已下载的文件和飞牛歌单里的歌不受影响。`)) return
  busyKey.value = `${row.key}:remove`
  try {
    const jobs = []
    for (const m of row.monitors) jobs.push(MonitorAPI.remove(m.id))
    for (const t of row.tasks) jobs.push(PushAPI.deleteTask(t.id))
    await Promise.all(jobs)
    flash('已删除')
    expandedKey.value = ''
    await Promise.all([loadMonitors(), loadPushTasks()])
  } catch (e) {
    flash(fail('删除订阅', e), true)
  } finally {
    busyKey.value = ''
  }
}

/**
 * 保存行内设置。
 *
 * ⚠️ 间隔是**订阅级**的 —— 两侧一起改，避免出现「下载 6 小时、同步 1 天」这种错位。
 * ⚠️ 两侧接口语义不同：监控侧是真局部更新（只传改动的键），推送侧是整体替换（必须回传全字段）。
 */
async function saveRowSettings(row) {
  if (!row || busyKey.value) return
  const f = editForm.value
  busyKey.value = `${row.key}:save`
  try {
    const jobs = []
    for (const m of row.monitors) {
      jobs.push(MonitorAPI.update(m.id, {
        quality: f.quality,
        max_downloads: Number(f.max_downloads) || 0,
        exclude_kw: f.exclude_kw,
        embed: !!f.embed,
        interval_minutes: Number(f.interval_minutes) || 0,
      }))
    }
    for (const t of row.tasks) {
      jobs.push(PushAPI.saveTask({
        id: t.id, name: t.name, provider: t.provider,
        source_kind: t.source_kind, source_id: t.source_id,
        target_title: f.target_title,
        sync_cover: !!f.sync_cover,
        interval_minutes: Number(f.interval_minutes) || 0,
        retention_mode: f.retention_mode,
        retention_days: Number(f.retention_days) || 0,
      }))
    }
    await Promise.all(jobs)
    flash('设置已保存')
    expandedKey.value = ''
    await Promise.all([loadMonitors(), loadPushTasks()])
  } catch (e) {
    flash(fail('保存订阅设置', e), true)
  } finally {
    busyKey.value = ''
  }
}

/**
 * 新建订阅：逐条链接调用 `subscribePlaylist`（勾什么就建什么）。
 *
 * 多条链接时部分成功也要如实汇总 —— 不能让用户以为全成了。
 * 界面只给一句人话，明细进日志（文案口径见 HANDOVER §3.3.3）。
 */
async function createSubscription() {
  // 账号来源走另一条路：它没有可订阅的链接，只能建飞牛同步任务
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
  const okList = []
  const failList = []
  try {
    for (let i = 0; i < links.length; i++) {
      const res = await subscribePlaylist({
        link: links[i],
        // 多条链接时给名字加序号，否则两侧任务同名、列表里分不出来
        name: links.length > 1 ? `${name} ${i + 1}` : name,
        wantDownload: form.value.wantDownload,
        wantFnos: form.value.wantFnos,
        intervalMinutes: Number(form.value.interval) || SUBSCRIBE_INTERVAL_MINUTES,
      })
      if (res.ok) okList.push(res)
      else failList.push(res.error || '未知原因')
    }
  } catch (e) {
    failList.push(fail('创建订阅', e))
  } finally {
    creating.value = false
  }

  await Promise.all([loadMonitors(), loadPushTasks()])

  if (failList.length) {
    logError('创建订阅', `${failList.length} 条链接失败`, failList.join('；'))
    createError.value = failList[0]
    if (okList.length) flash(`部分成功：${okList.length} 条已订阅，${failList.length} 条失败`)
    return
  }
  flash(okList[0]?.message || '已订阅')
  showCreate.value = false
  form.value.links = ''
  form.value.name = ''
}

/**
 * 按账号来源建订阅（歌单 / 每日推荐）。
 *
 * 只有飞牛同步这一条 —— 账号歌单没有公开链接，监控侧订阅不了；
 * 「每日推荐」更是每天都变，后端监控也不支持。所以这里直接建推送任务。
 */
async function createAccountSubscription() {
  const name = form.value.name.trim()
  if (!name) { createError.value = '请填写订阅名称'; return }
  const [kind, id] = String(form.value.sourceValue || '').split(':')
  if (!kind) { createError.value = '请选择一个账号歌单'; return }
  if (!fnosConnected.value) { createError.value = '飞牛音乐未连接，无法同步歌单'; return }

  creating.value = true
  createError.value = ''
  try {
    const res = await PushAPI.saveTask({
      name: `${name}（订阅）`.slice(0, 40),
      provider: form.value.provider,
      source_kind: kind,
      source_id: id || '',
      target_title: name.slice(0, 40),
      interval_minutes: Number(form.value.interval) || SUBSCRIBE_INTERVAL_MINUTES,
      sync_cover: true,
      enabled: true,
      retention_mode: 'keep',
      retention_days: 0,
    })
    const bad = apiError(res, '创建同步任务失败')
    if (bad) { createError.value = bad; return }
    flash('已订阅：同步到飞牛歌单')
    showCreate.value = false
    form.value.name = ''
    await loadPushTasks()
  } catch (e) {
    createError.value = fail('创建订阅', e)
  } finally {
    creating.value = false
  }
}

function baseName(p) {
  return String(p || '').split('/').pop()
}

/** 侧栏「还没开始追新」角标（后端字段仍是 baseline_done） */
const pendingBaseline = computed(() => monitors.value.filter(m => !m.baseline_done).length)

/**
 * 指标行随当前侧栏项变化 —— 只展示与当前区块相关的数字，避免出现
 * 「标题写着推送、指标却是整理数据」这种对不上的情况。
 * 返回空数组时整行隐藏。
 */
const metrics = computed(() => {
  // 推送：合并后的订阅列表 —— 一行一个来源，两个能力位各算各的
  // （归外层渲染，这样位置与其他页面一致 —— 都在内容最上方）
  if (activeTab.value === 'push') {
    const rows = subscriptions.value
    return [
      { label: '订阅', value: rows.length, color: 'text-gray-800' },
      { label: '自动下载', value: rows.filter(r => r.download).length, color: 'text-emerald-600' },
      { label: '同步飞牛', value: rows.filter(r => r.fnos).length, color: 'text-emerald-600' },
      { label: '待下载', value: rows.reduce((n, r) => n + r.pendingCount, 0), color: 'text-amber-600' },
    ]
  }

  // 下载：两个子视图各看各的口径（并成一页了，数字别再混在一排）
  if (activeTab.value === 'download') {
    if (downloadSub.value === 'playlist') {
      const rate = downloadManager.recentSuccessRate.value
      return [
        { label: '下载中', value: downloadManager.downloadingCount.value, color: 'text-blue-600' },
        { label: '等待中', value: downloadManager.pendingCount.value, color: 'text-gray-800' },
        { label: '失败待处理', value: downloadManager.failedCount.value, color: 'text-rose-600' },
        // 没有样本时显示「—」而不是 0%，否则会让人以为全失败了
        { label: '近 24h 成功率', value: rate == null ? '—' : `${rate}%`, color: 'text-emerald-600' },
      ]
    }
    return [
      { label: '全部任务', value: downloadManager.totalCount.value, color: 'text-gray-800' },
      { label: '进行中', value: downloadManager.activeCount.value, color: 'text-emerald-600' },
      { label: '已暂停', value: downloadManager.pausedCount.value, color: 'text-amber-600' },
      { label: '失败', value: downloadManager.failedCount.value, color: 'text-rose-600' },
    ]
  }

  // 补全整理：这一页**不要**页面级指标行（用户 2026-09-23 反馈「这四个卡片和
  // 最下面体检索引重复了」）。数字在本页已有两处各自说得更清楚：
  //   · 缺口面板：索引口径（总数 / 读过标签 / 六个字段各缺多少）
  //   · 体检卡片：文件真实扫描（总数 / 缺歌词 / 缺封面 / 两者都缺）
  // 上面再摆四张大卡就是第三份重复，而且三处口径不同、数字还会打架。
  if (activeTab.value === 'complete') return []

  // 榜单管理：组件自带「已选 N / 总数」徽标，页面级指标行同样会构成第三份口径。
  if (activeTab.value === 'charts') return []

  // 其余侧栏项（推送已在上面处理）没有可量化指标
  return []
})

/** 手动刷新（侧栏/指标行都依赖这些数据） */
const reloading = ref(false)
async function reloadAll() {
  reloading.value = true
  try {
    await Promise.all([
      loadMonitors(),
      loadIndexStats().catch(() => {}),
    ])
  } finally {
    reloading.value = false
  }
}

function fmtSize(bytes) {
  const b = Number(bytes || 0)
  if (b > 1024 * 1024 * 1024) return (b / 1024 / 1024 / 1024).toFixed(2) + ' GB'
  if (b > 1024 * 1024) return (b / 1024 / 1024).toFixed(1) + ' MB'
  if (b > 1024) return (b / 1024).toFixed(1) + ' KB'
  return b + ' B'
}

function flash(msg, isError = false) {
  if (isError) {
    error.value = msg
    message.value = ''
  } else {
    message.value = msg
    error.value = ''
  }
  setTimeout(() => { message.value = ''; error.value = '' }, 5000)
}

async function loadMonitors() {
  // 加载完就登记一次"新鲜度"（见 services/viewCache.js）
  try {
    const res = await MonitorAPI.list()
    if (res.code !== 200) return

    runningIds.value = res.data.running || []
    monitorStats.value = res.data.stats || null

    const list = res.data.monitors || []
    // 列表接口不内嵌「最近一次运行」与「待补下载数」，需要逐个取。
    // ⚠️ 必须并发：这里是每 8 秒一次的轮询，串行取会让 10 个监控变成 21 次串行往返。
    await Promise.all(
      list.map(async (m) => {
        try {
          const [d, t] = await Promise.all([
            MonitorAPI.detail(m.id),
            // limit=1 只为拿 total（待补下载计数），不拉整张曲目表
            MonitorAPI.tracks({ monitor_id: m.id, status: 'pending', limit: 1 }),
          ])
          m.recentRun = (d?.data?.runs || [])[0] || null
          m.pendingCount = t?.code === 200 ? (t.data?.total || 0) : 0
        } catch (_) {
          m.recentRun = null
          m.pendingCount = 0
        }
      }),
    )
    monitors.value = list
  } catch (e) {
    flash(fail('加载推送任务', e, '加载失败'), true)
  }
  markLoaded('library')   // 登记新鲜度（见 services/viewCache.js）
}

// ── 补下载：两侧都可能缺歌，都走「逐首搜索 → 入队」这一步 ──
//
// 音源直链只能在浏览器解析（lx 脚本），下载队列也在前端（downloadManager），
// 所以「下载」永远是网页端动作：逐首搜索 → 入队 → 成功后回写曲目状态。
//
// 两个来源：
//   · 监控侧 pending —— 后端每轮发现的新歌，登记了但还没下
//   · 推送侧 missing —— 飞牛歌单里还没有的曲目（曲库里没有，推不上去）
const fillingKey = ref('')
const fillProgress = ref('')
const fillTimers = []

/** 补下载按钮的悬停说明：让用户知道这 N 首是怎么来的、要不要自己点 */
function fillHint(row) {
  const parts = []
  if (row.pendingCount) {
    parts.push(
      row.download
        ? `${row.pendingCount} 首待下载。已勾选「下载到本地曲库」，会分批自动下；点这里可以立刻补一批`
        : `${row.pendingCount} 首待下载。没勾「下载到本地曲库」，需要点这里手动下`,
    )
  }
  if (row.missingCount) parts.push(`${row.missingCount} 首曲库里还没有（飞牛歌单推不上去）`)
  return parts.join('；')
}

/** 拉某个监控的全部待下载曲目（后端分页，一页 200） */
async function fetchPendingTracks(monitorId) {
  const tracks = []
  for (let offset = 0; ; offset += 200) {
    const res = await MonitorAPI.tracks({ monitor_id: monitorId, status: 'pending', limit: 200, offset })
    if (res?.code !== 200) throw new Error(apiError(res, '读取待下载曲目失败'))
    const items = res.data?.items || []
    tracks.push(...items)
    if (items.length < 200 || tracks.length >= (res.data?.total || 0)) break
  }
  return tracks
}

/**
 * 逐首「搜索 → 入队」。
 *
 * ⚠️ **入队前先按「歌名 + 歌手」查本地有没有**（`POST /api/download/check`，
 * 后端判据是 `歌手 - 歌名.mp3` 存在且 >300KB）。用户原话：
 * 「同一首，同歌手，不需要，只下载不一样的版本」。
 *
 * 本地已有的不但不再下一遍，还要**回写成「已下载」** ——
 * 否则它会一直挂在「待下载」里：数字永远不降，自动补下载每轮都白跑一次。
 *
 * 返回加入队列的曲目对，供调用方回写状态（监控侧要标记成已下载）。
 * 两边共用这一段 —— 差别只有数据来源和搜索平台。
 *
 * `rowKey` 传订阅行的 key：这一批是**逐首串行搜索**的（几十首要一两分钟），
 * 每一步都复查一次「这一行是不是已经被停止」——不查的话用户点完停止，
 * 队列还会继续涨（2026-09-22 反馈）。
 */
async function enqueueSongs(items, platform, rowKey = '') {
  const watching = []
  let added = 0
  let missed = 0
  let already = 0

  // ① 批量判重：一次请求问清「这批里哪些本地已经有了」
  //    判据与边界情况在 `services/downloadDedup.js`，有单测守着。
  let haveLocal = new Set()
  try {
    const probe = items.map((t) => ({
      id: t.song_id || '',
      name: t.name || '',
      singer: t.artist || '',
    }))
    const chk = await DownloadAPI.checkDownloaded(probe)
    if (chk?.code === 200 && chk.exists) haveLocal = pickAlreadyLocal(items, chk.exists)
  } catch (_) {
    // 查不了就当「都没有」—— 宁可多下一次，也别因为一次探测失败就什么都不下
  }

  for (let i = 0; i < items.length; i++) {
    // 「停止」要能中止**正在入队的这一批** —— 逐首搜索是串行的，
    // 不检查的话用户点完停止，队列还会继续涨一两分钟
    if (isRowStopped(rowKey)) break

    const t = items[i]

    // ② 本地已有：跳过入队，但塞进 watching 让调用方把它标记成已下载
    if (haveLocal.has(i)) {
      already++
      watching.push({ task: { status: 'success', path: '' }, track: t })
      continue
    }

    fillProgress.value = `${i + 1}/${items.length}`
    try {
      const keyword = [t.name, t.artist].filter(Boolean).join(' ')
      if (!keyword) { missed++; continue }
      const res = await SearchAPI.search(keyword, platform, 1)
      const list = res?.data?.list || []
      const hit = list.find((s) => s.source === platform) || list[0]
      if (!hit) { missed++; continue }
      const task = downloadManager.addSong(hit, '', false)
      if (task) watching.push({ task, track: t })
      added++
    } catch (e) {
      missed++
    }
  }
  return { watching, added, missed, already }
}

/**
 * 行内「补下载」：把这一行缺的曲目都搜出来入队。
 *
 * 监控侧下载成功后回写曲目状态（下一轮就不会再列出来）；
 * 推送侧的 missing 没有回写 —— 它由下一轮推送任务重新匹配后自然更新。
 *
 * @param {object} row
 * @param {{auto?: boolean, limit?: number}} [opts]
 *   auto  —— 自动补下载（后台行为，不弹「没有需要补下载的曲目」这类提示）
 *   limit —— 这一批最多入队多少首（「每次下载数量」；0 = 不限）
 * @returns {Promise<{added:number, missed:number, already:number}|null>}
 */
async function fillRow(row, { auto = false, limit = 0 } = {}) {
  if (!row || fillingKey.value) return null
  const mon = row.monitors[0] || null
  const task = row.tasks[0] || null

  fillingKey.value = row.key
  fillProgress.value = ''
  try {
    let added = 0
    let missed = 0
    let already = 0
    // 两侧入队的任务合成一个池子，供「下完自动同步飞牛」统一等待
    const watching = []

    // 停止后不再往队列里塞。两个阶段之间也复查一次 —— 取待下载列表是网络请求，
    // 用户完全可能在这中间点了停止。
    if (isRowStopped(row.key)) return { added, missed, already }

    if (mon && row.pendingCount) {
      let tracks = await fetchPendingTracks(mon.id)
      // 「每次下载数量」：这一批只入队这么多，下完下一轮再接着下
      if (limit > 0) tracks = tracks.slice(0, limit)
      if (tracks.length) {
        const r = await enqueueSongs(tracks, row.source === 'tx' ? 'tx' : 'wy', row.key)
        added += r.added
        missed += r.missed
        already += r.already
        watching.push(...r.watching)
        watchFillCompletion(mon.id, r.watching)
      }
    }

    if (isRowStopped(row.key)) return { added, missed, already }

    if (task && row.missingCount) {
      const items = task.last_result?.missing || []
      if (items.length) {
        const r = await enqueueSongs(items, task.provider === 'qq' ? 'tx' : 'wy', row.key)
        added += r.added
        missed += r.missed
        already += r.already
        watching.push(...r.watching)
      }
    }

    // 给这批任务打上「哪一行来的」标记，两个用途：
    //  ① `autoFillPending` 的闸门 2（「上一批还没下完就让开」）靠它判断；
    //  ② 订阅行点「停止」时靠它认领自己入队的任务（`downloadManager.stopByRow`）。
    // ⚠️ 所以**手动「补下载」也要打**，不能只在 auto 时打 —— 否则点停止管不到手动入队的那批。
    //    任务本身会落盘，这个标记一并带着走（见 `downloadManager.slimTasks`）。
    for (const w of watching) if (w.task) w.task.autoRowKey = row.key

    const willSync = row.fnos && row.tasks.length && watching.length

    if (!added && !missed && !already) {
      // 自动补下载是后台行为：什么都不缺时不要弹提示（每十几秒弹一次会烦死人）
      if (!auto) flash('没有需要补下载的曲目')
      return { added, missed, already }
    }

    if (auto) {
      if (added) {
        flash(`已自动加入下载队列 ${added} 首${already ? `（${already} 首本地已有，已标记）` : ''}`)
      }
    } else {
      flash(
        `已加入下载队列 ${added} 首` +
          (already ? `，${already} 首本地已有` : '') +
          (missed ? `，${missed} 首没搜到` : '') +
          (willSync ? '；下完会自动同步到飞牛歌单' : '；下载完成后自动标记'),
      )
    }
    if (missed) logError('补下载', `${missed} 首未能加入下载队列`, '搜索无结果或接口报错')

    // ⭐ 队列一空就自动推一次 —— 用户不该为了「歌出现在飞牛歌单里」再点一次「立即执行」
    if (willSync) watchQueueDrain(watching, () => autoPushRow(row))

    return { added, missed, already }
  } catch (e) {
    flash(fail(auto ? '自动下载' : '补下载', e), true)
    return null
  } finally {
    fillProgress.value = ''
    fillingKey.value = ''
  }
}

// ── 勾了「下载到本地曲库」就自动下，不用手点「补下载」 ──────────────────────────
//
// **为什么必须在前端**：音源脚本只能在浏览器里执行取直链，后端那一轮监控
// 只能把新歌登记成「待下载」（`pkg/monitor/pipeline.go` 的 canResolve=false 分支）。
// 所以「勾了自动下载」这件事只有前端能做。
//
// 用户原话（2026-09-22）：「我订阅的歌单，下面选择了下载到本地曲库，
// 那就应该在点击立即执行时，就开始下载了吧」
// 「包括补下载，也应该是作为用户在未勾选时手动下载」

/** 正在自动补下载的行 key（防重入） */
const autoFilling = ref('')
/** 本次会话里自动尝试过的曲目 —— 每首只自动试一次，失败的不无限重试 */
const autoTried = new Set()

/**
 * 每行「已经自动补过的那一轮」：`row.key -> lastRunAt`。
 *
 * ⚠️ 为什么要它（用户 2026-09-22）：「8 秒轮询发现待下载就入队，我感觉多余了」。
 *
 * 轮询本身**不能删** —— 音源脚本只能在浏览器里跑，后端那一轮只能把新歌登记成
 * 「待下载」，把这个登记变成真正下载的只有前端这一步（见 §4.1.7）。
 * 但以前是「**只要有待下载就抓**」，于是连**上一轮留下的旧账**（没搜到、被停掉的）
 * 也会在打开页面时被自动抓一遍 —— 用户看到的就是「我没点它自己在列队」。
 *
 * 现在的判据：**只服务刚跑完的那一轮**。轮次用 `row.lastRunAt` 标识
 * （定时到点、或点「立即执行」都会刷新它）。轮次没变就不再碰；
 * 旧账要下载请手点「补下载」。
 */
const autoFilledRun = new Map()

/** 这一行当前是第几轮（用「上次发现时间」当轮次标识） */
function runKeyOf(row) {
  return String(row?.lastRunAt || '')
}

/**
 * 本次会话里**已被停止**的订阅行。
 *
 * 为什么需要它：光把服务端的 `enabled` 改掉，只能拦住「下一批」——
 * 正在入队的那一批（逐首搜索几十首要一两分钟）、已经排队的任务、
 * 以及「下载队列排空后自动推一次」的 90 秒定时器都还在跑。
 * 用户看到的就是「点了停止还在下」「停止后一会儿又开始推送」（2026-09-22 反馈）。
 *
 * 只在「重新开启」时移除；刷新页面后靠服务端的 enabled 兜底，不需要持久化。
 */
const stoppedKeys = new Set()

/** 这一行是否已被停止（本次会话） */
function isRowStopped(key) {
  return !!key && stoppedKeys.has(key)
}

/** 曲目的稳定 key（用于「本次会话只自动试一次」） */
function trackKey(t) {
  return `${t?.source || ''}:${t?.song_id || ''}:${t?.name || ''}`
}

/**
 * 轮询里调用：勾了「下载到本地曲库」的行，有待下载就自动入队一批。
 *
 * ⚠️ 闸门（少一道就会出问题）：
 *  0. **`autoFilledRun`（只接新轮次）** —— 只服务刚跑完的那一轮。
 *     没有它，上一轮留下的旧账会在每次打开页面时被自动抓一遍，
 *     看起来就是「我没点它自己在列队」（用户 2026-09-22 反馈）。见 `autoFilledRun`。
 *  1. **`baselineDone`** —— 第一次检查还没跑完时不自动下。那一轮登记的是
 *     歌单**现有的全部曲目**，一次性几百首会打爆音源服务；
 *     而且「第一次检查不下载」本身就是既有机制。
 *  2. **该行在下载队列里还有活跃任务** —— 说明上一批还没下完，等它。
 *     这就是「分批」：一批 N 首，下完自动接下一批。
 *  3. **`autoTried`** —— 每首只自动试一次。失败的不回写、仍是 pending，
 *     不拦的话会每轮重试同一批，无限循环。失败的留给用户手点「补下载」重来。
 */
async function autoFillPending() {
  if (fillingKey.value || autoFilling.value) return
  for (const row of subscriptions.value) {
    // 被停止的行一律跳过 —— 服务端 enabled 也能拦住，但停止是**立即生效**的，
    // 不能等下一轮 loadMonitors 刷回来（那一轮结束前用户已经看到队列还在涨）
    if (isRowStopped(row.key)) continue
    if (!row.download || !row.hasDownload) continue
    if (!row.baselineDone) continue
    // 「只接新轮次」：这一轮已经自动补过就不再碰（旧账请手点「补下载」）。
    // 见 `autoFilledRun` 的说明 —— 这条就是「不再自动抓历史遗留待下载」的闸门。
    if (autoFilledRun.get(row.key) === runKeyOf(row)) continue
    if (!row.pendingCount) continue
    const mon = row.monitors[0]
    if (!mon) continue

    // 闸门 2：上一批还在队列里跑 → 让开
    const busy = downloadManager.tasks.value.some(
      (t) =>
        t.autoRowKey === row.key &&
        (t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading'),
    )
    if (busy) continue

    let tracks = []
    try {
      tracks = await fetchPendingTracks(mon.id)
    } catch (_) {
      continue
    }

    // 闸门 3：只挑本次会话还没自动试过的
    const fresh = tracks.filter((t) => !autoTried.has(trackKey(t)))
    if (!fresh.length) {
      // 这一轮剩下的曲目本次会话都试过了（多半是搜不到）→ 这轮到此为止。
      // 记下轮次，免得每 8 秒白拉一次待下载列表。
      autoFilledRun.set(row.key, runKeyOf(row))
      continue
    }
    for (const t of fresh) autoTried.add(trackKey(t))

    const limit = Number(mon.max_downloads) > 0 ? Number(mon.max_downloads) : 30
    autoFilling.value = row.key
    let res = null
    try {
      res = await fillRow(row, { auto: true, limit })
    } finally {
      autoFilling.value = ''
    }
    // 这一轮一件也没处理（没入队也没漏搜）→ 记下轮次，别重复抓。
    // 反之（真入队了一批）就**不记**：这一轮可能还有下一批要接着入。
    if (!res || (!res.added && !res.missed)) {
      autoFilledRun.set(row.key, runKeyOf(row))
    }
    // 一次只处理一行，剩下的等下一轮轮询 —— 别把 NAS 和音源一起打满
    return
  }
}

function watchFillCompletion(monitorID, pairs) {
  if (!pairs.length) return
  const timer = setInterval(async () => {
    const done = []
    for (let i = pairs.length - 1; i >= 0; i--) {
      const { task, track } = pairs[i]
      if (task.status === 'success') {
        done.push({ source: track.source, song_id: track.song_id, file_path: task.path || '' })
        pairs.splice(i, 1)
      } else if (task.status === 'failed' || !downloadManager.tasks.value.includes(task)) {
        // 失败/被移除的保持 pending，下一轮或下次点「补下载」还能重试
        pairs.splice(i, 1)
      }
    }
    if (done.length) {
      try {
        await MonitorAPI.markTracks({ monitor_id: monitorID, status: 'downloaded', items: done })
      } catch (e) {
        logError('回写监控曲目状态', `${done.length} 首标记失败`, e?.message || String(e))
      }
      loadMonitors()
    }
    if (!pairs.length) clearInterval(timer)
  }, 3000)
  fillTimers.push(timer)
}

/**
 * 等这批下载「不再活跃」就回调一次。
 *
 * ⚠️ 判据是**「不再活跃」而不是「全部成功」**。
 * 用户原话：「不管是全部下载完还是中途停止了，都能在飞牛音乐看到歌曲」——
 * 中途暂停时，已经下完的那部分也该推上去，不能干等一个永远不会来的「全部完成」。
 * 所以 pending / resolving / downloading 三种状态一消失（成功、失败、暂停、被移除都算）就触发。
 */
function watchQueueDrain(pairs, onDrained) {
  const tasks = pairs.map((p) => p.task).filter(Boolean)
  if (!tasks.length) {
    onDrained?.()
    return
  }
  const timer = setInterval(() => {
    const busy = tasks.some(
      (t) => t.status === 'pending' || t.status === 'resolving' || t.status === 'downloading',
    )
    if (busy) return
    clearInterval(timer)
    onDrained?.()
  }, 3000)
  fillTimers.push(timer)
}

/**
 * 下载落盘后自动跑一次推送 —— 省掉「手动点立即执行」。
 *
 * ⚠️ **必须等一会儿再推**：推送只认**飞牛曲库**里搜得到的歌，而曲库要靠
 * `POST /shared-library/scan-all` 刷新。下载完成后我们只是「排了一次重扫」
 * （`pkg/fnos/rescan.go`，30 秒 debounce + 3 分钟最小间隔），
 * 立刻推会一首都对不上 —— 用户看到的现象就是「歌单里没有歌」。
 *
 * 这里再排一次重扫（幂等，已排过就只是把 debounce 往后推）然后等
 * `AUTO_PUSH_SETTLE_MS`。等不到也没关系：定时轮下次跑还会再匹配一遍，
 * 而且失败会如实提示，不会假装成功。
 */
async function autoPushRow(row) {
  if (!row?.tasks?.length) return
  if (isRowStopped(row.key)) return
  // 等待期间用户可能把订阅停了 —— 「停止」的语义是「别跑了」，这时候不能再推
  const fresh = subscriptions.value.find((r) => r.key === row.key)
  if (!fresh || !fresh.fnos) return
  try {
    await FnosAPI.rescan()
  } catch (e) {
    // 重扫排不上不阻塞推送 —— 至少曲库里原有的歌能推上去
    logError('自动同步飞牛', '触发曲库重扫失败', e?.message || String(e))
  }
  await new Promise((resolve) => setTimeout(resolve, AUTO_PUSH_SETTLE_MS))

  // ⚠️ 必须**再查一次**：上面那次检查在 90 秒等待**之前**，用户完全可能在等待期间
  // 点了停止。少了这一次复查，用户看到的就是「停止后一会儿又开始推送」
  // （2026-09-22 真机反馈，就是这个定时器）。
  if (isRowStopped(row.key)) return
  const fresh2 = subscriptions.value.find((r) => r.key === row.key)
  if (!fresh2 || !fresh2.fnos) return

  try {
    await Promise.all(row.tasks.map((t) => PushAPI.run(t.id)))
    flash('下载完成，已自动同步到飞牛歌单（稍后刷新查看）')
    setTimeout(() => {
      loadPushTasks()
      loadPushRuns()
    }, 2500)
  } catch (e) {
    logError('自动同步飞牛', row.name, e?.message || String(e))
    flash('下载完成，但自动同步没成功；可点这一行的「立即执行」重试', true)
  }
}

onUnmounted(() => fillTimers.forEach(clearInterval))

// ── 曲库体检结果的持久化 ──
//
// 体检是用户手点的一次全量扫描，结果就 4 个数字，但以前只存在组件内的 `audit` ref 里，
// 一刷新页面就没了 —— 同页的「上次整理快照」却是持久化的，同一页两种行为很割裂。
// 用户 2026-09-22 反馈：「体检数据每次都会在页面消失…修改为保持显示」。
//
// 用 `prefs` 的 saveCache/loadCache：本地 localStorage + 后端双写，跟着设备走
// （与下载队列落盘同一个取向）。**按目录分开存** —— 换了曲库目录不该看到别的目录的体检结果。
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
  // ttl 传 0 = 永不过期：用户要的就是「一直看得到上次结果」
  const cached = loadCache(auditCacheKey(dir), 0)
  const ok = cached && typeof cached === 'object'
  // 换了目录就换成那本目录的结果；没有就清空 —— 别把别的目录的数字挂在这儿
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
      // 落盘（本地 + 后端双写）：刷新页面后还能看到，见上面 restoreAudit 的说明
      saveCache(auditCacheKey(dir), { ...res.data, at: Date.now() })
    } else {
      flash(apiError(res, '体检失败'), true)
    }
  } catch (e) {
    flash(fail('曲库体检', e), true)
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
      // 扫描刚把新字段读进索引 —— 顺手重算缺口，否则「AI 补全」页还显示旧的待补数量
      loadIndexStats()
      scanGaps().catch(() => {})
    } else {
      flash(apiError(res, '扫描失败'), true)
    }
  } catch (e) {
    flash(fail('增量扫描', e), true)
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
    flash(fail('查找重复', e), true)
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
    flash(fail('清理重复文件', e), true)
  }
}

// ── 补全歌词/封面（后端是异步任务，前端靠轮询看进度）──
//
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
//
// 飞牛「歌曲信息」有六栏可由平台数据补齐。字段表与展示逻辑抽到
// `services/libraryFields.js`（那边有单测：字段 key 拼错后端不会报错，只会静默不补）。
//
// 这里的两条口径：
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
  // 所以最后一个主力不允许取消，要换先点别的。
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
    flash(fail('保存主力平台', e), true)
  }
}

const fieldOptions = computed(() => FIELD_DEFS.map((f) => ({
  ...f,
  missing: gaps.value ? (gaps.value[f.key] ?? 0) : '—',
})))

/** 侧栏角标：至少缺一个字段的条数（就是点进去会排进队的数量）；0 不显示 */
const gapsPendingCount = computed(() => _gapsPendingCount(gaps.value))

/**
 * 「写不了标签的格式」按扩展名的明细，如 `wav×12、opus×3`。
 *
 * 后端把这类文件从缺口里摘了出去（`gaps.unsupported`），但必须让用户看见 ——
 * 否则「补全跑完了还是没年份」只能被当成功能坏了。旧缓存里没有这个字段，
 * 取不到就返回空串，模板里就不显示括号。
 */
const unsupportedExtsText = computed(() => _unsupportedExtsText(gaps.value?.unsupported_exts))

/**
 * 「上次统计」的相对时间。
 *
 * ⚠️ `gaps.at` 是 `Date.now()`（**毫秒**），和 `indexStats.scanned_at`（秒）不是一个单位，
 * 所以这里不能复用 `lastScanText` 的算法。原先模板里引用了 `gapsAtText` 但**脚本里没定义**
 * ⇒ 界面上永远显示「上次统计（）」—— 2026-09-26 真机侧边栏发现并补上。
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
    flash(fail('统计字段缺口', e), true)
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
//
// 为什么不直接用补全结果里的值：结果说的是「我们打算写什么」，
// 卡片要说的是「文件里现在是什么」。两者可以不一致（某首写失败、
// 或文件同时被别的程序改过）—— 界面上报"补好了"而飞牛里没有，最伤信任。
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
  completedCards.value = cached.cards || []
  skippedCards.value = cached.skipped || []
}

async function loadCompletedCards(summary) {
  const dir = tidyDir.value || nasDir.value
  const results = (summary && summary.results) || []
  completeBar.value = summaryBar(selectedFields.value, summary)
  cardsAt.value = Date.now()

  const written = results.filter((r) => r.ok)
  skippedCards.value = results.filter((r) => !r.ok).map((r) => cardModel(null, r))
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
    completedCards.value = settled.map((s, i) => (s.ok ? s.value : cardModel({}, written[i])))
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
 *
 * 透出来是为了**可分辨** —— 用户有权知道哪些结果是模型判断的，
 * 而不是我们自己按规则算出来的（评估文档 §7 第 6 条：来源必须可分辨）。
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
    flash(fail('预览补全', e), true)
  } finally {
    previewing.value = false
  }
}

/**
 * 执行已确认的计划。
 *
 * ⚠️ 传回 `plan_id` 而不是重新提交目录 —— 执行的是**用户刚看到的那份计划**
 * （含匹配到的曲目标识），否则搜索结果变了就会「批准了 A、结果写了 B」。
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
    flash(fail('写入补全结果', e), true)
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
          // 被停止 ≠ 跑完（话术在服务层，有单测）：用一样的话会让人以为「点停止也全补好了」
          flash(completeDoneText(j))
          // 索引里的 HasLyric/HasCover 已由后端回写，刷新一下顶部指标
          loadIndexStats()
          // 缺口数字要跟着重算，否则页面还挂着「缺 12 首」而实际已经补好了
          // （只读索引、不开文件，代价可忽略）
          scanGaps().catch(() => {})
          // 卡片墙：逐首回读文件真实标签，让用户看到「写进去的是什么」而不是「打算写什么」
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
 *
 * 一批最多 200 首、每首要打两次外网并改写文件，跑起来是分钟级的。
 * 没有这个按钮，用户点错了就只能看着它把整个曲库改完（再点一次会被 409 挡回）。
 *
 * 不弹确认框：停止不是破坏性操作 —— 已写入的保留、还能用「撤销」回退，
 * 弹窗反而会让人以为「停了会丢东西」而不敢停。停止后进度条会继续走到当前那首写完。
 */
async function stopComplete() {
  if (stopBusy.value) return
  stopBusy.value = true
  try {
    const res = await TidyAPI.cancelComplete()
    flash(res?.message || '已请求停止')
    // 不一定停下来了：可能刚好跑完了（后端会如实回「没有正在跑的补全任务」）。
    // 拉一次真实状态，别让界面显示一个已经不存在的进度。
    await loadCompleteStatus()
  } catch (e) {
    flash(fail('停止补全', e), true)
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
 *
 * 补全会真正改写用户的音频文件，所以这一步是「敢不敢批量用」的前提。
 * 撤销只做一次（没有「重做」）—— 确认弹窗里要说清楚，别让用户以为能反复横跳。
 */
async function undoComplete() {
  if (!confirm('把上一次补全改过的文件还原成补全前的样子？\n\n还原后需要重新跑一次补全才能再补上。')) return
  undoBusy.value = true
  try {
    const res = await TidyAPI.undoComplete()
    flash(res?.message || '已撤销')
    await loadCompleteStatus() // 撤销后 undo_available 会变 false
    await loadIndexStats()
    // 文件已还原成补全前的样子，缺口数字也跟着回退
    scanGaps().catch(() => {})
    // 卡片墙必须一起清 —— 留着就是在展示一批已经不存在的标签值
    completedCards.value = []
    skippedCards.value = []
    completeBar.value = []
    saveCards(tidyDir.value || nasDir.value)
  } catch (e) {
    flash(fail('撤销补全', e), true)
  } finally {
    undoBusy.value = false
  }
}

// ── 下载行为开关（v2.1.92）────────────────────────────────────────────
const favAutoDownload = ref(false)
const favAutoBusy = ref(false)
const favAutoMsg = ref('')

const teeEnabled = ref(false)
const teeBusy = ref(false)
const teeMsg = ref('')

// 这两个**缺省是开**：后端也是这么理解的（见 pkg/config 的 LyricAutoDownloadOn）。
// 配置里没这个键 = undefined = 开，所以不能直接 !!。
const lyricAutoDownload = ref(true)
const lyricBusy = ref(false)
const coverEmbed = ref(true)
const coverBusy = ref(false)
const extraMsg = ref('')

function asDefaultOn(v) {
  return v === undefined || v === null ? true : !!v
}

async function loadDownloadPrefs() {
  try {
    const cfg = await ConfigAPI.get()
    const c = cfg?.config || cfg?.data || cfg || {}
    favAutoDownload.value = !!c.fav_auto_download
    teeEnabled.value = !!c.tee_enabled
    lyricAutoDownload.value = asDefaultOn(c.lyric_auto_download)
    coverEmbed.value = asDefaultOn(c.cover_embed)
  } catch (_) {
    // 读不到就保持「关」：这几个开关都会占磁盘和带宽，宁可不生效
  }
}

// toggleDownloadSwitch 抽出来是因为「收藏自动下载」和「边听边下」是同一套动作：
// 乐观更新 → 写服务端 → 失败回滚（界面不能骗人）。
async function toggleDownloadSwitch(onRef, busyRef, msgRef, key) {
  const next = !onRef.value
  onRef.value = next
  busyRef.value = true
  msgRef.value = ''
  try {
    await ConfigAPI.save({ [key]: next })
  } catch (e) {
    onRef.value = !next
    msgRef.value = apiError(e)
  } finally {
    busyRef.value = false
  }
}

function toggleTee() {
  return toggleDownloadSwitch(teeEnabled, teeBusy, teeMsg, 'tee_enabled')
}

function toggleLyric() {
  return toggleDownloadSwitch(lyricAutoDownload, lyricBusy, extraMsg, 'lyric_auto_download')
}

function toggleCover() {
  return toggleDownloadSwitch(coverEmbed, coverBusy, extraMsg, 'cover_embed')
}

function toggleFavAutoDownload() {
  return toggleDownloadSwitch(favAutoDownload, favAutoBusy, favAutoMsg, 'fav_auto_download')
}

onMounted(() => {
  loadDownloadPrefs()
  tidyDir.value = loadPref('nas_dir', '')
  loadMonitors()
  loadPushTasks()
  loadPushRuns()
  // 飞牛是否可用决定「同步到飞牛歌单」勾选框能不能新建 —— 首屏就探一次
  loadFnosStatus()
  // 有没有服务型音源，决定要不要提示「保持这个窗口开着才能下」
  loadApiSourceCount()
  // 首屏自动读取上次整理快照（持久化索引 library_index.json），无需等用户点扫描
  loadIndexStats()
  // 上次体检结果同样从缓存恢复，不再"刷新就没"（见 restoreAudit）
  restoreAudit()
  // 「AI 补全」的缺口统计同理从缓存恢复（它只是索引快照的统计，重算一次很便宜）
  restoreGaps()
  // 上次补全的结果卡片也恢复：跑完去回了个消息再回来，不该看到空页
  restoreCards()
  // 主力平台（决定搜索顺序与「回哪家借元数据」）从应用配置读
  loadPreferred()
  // 补全任务是后端在跑的，刷新页面不该让进度凭空消失
  loadCompleteStatus()
  // ⚠️ 自动补下载**不能只挂在「推送」子页上**：用户切到「下载」或「歌单下载」
  // 也得继续下（勾了「下载到本地曲库」就该下）。
  // 监控列表也一直刷 —— 自动补下载靠 `pendingCount` 判断「还有没有要下的」，
  // 不刷的话切走之后数字停在旧值，会每 8 秒白跑一次拉取。
  pollTimer = setInterval(() => {
    autoFillPending()
    loadMonitors()
    if (activeTab.value === 'push') loadPushTasks()
  }, 8000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  stopCompletePolling()
})

// 常驻页面：切回来时如果超过 TTL / 被下载完成标成过期，就重新拉一次监控列表
onActivated(() => {
  if (isStale('library')) loadMonitors()
})
</script>
