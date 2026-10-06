<template>
  <div class="h-full flex flex-col overflow-hidden select-none bg-gray-50">
    <!-- ── 顶部搜索与导航条 ── -->
    <div class="px-5 sm:px-8 pt-5 sm:pt-7 pb-4 bg-white border-b border-gray-100 shrink-0">
      <div class="max-w-[1500px] mx-auto space-y-2.5">

        <!-- 页面标题（与其它功能页保持同一套标题层级） -->
        <div class="min-w-0">
          <h1 class="text-[26px] font-bold tracking-tight text-gray-800">发现音乐</h1>
          <p class="mt-1.5 text-[13px] leading-relaxed text-gray-500">
            搜索全网曲库，或浏览当前音源声明支持的官方榜单与精选歌单
          </p>
        </div>

        <!-- 搜索结果态：给一个回发现页的入口 —— 原来这个「清空并返回」按钮挂在搜索框上，
             2026-09-28 搜索框搬去底部播放栏后，得在这里留一个，否则进了结果页回不去。 -->
        <button
          v-if="currentMode === 'search'"
          @click="resetToDiscover"
          class="rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-semibold text-gray-600 transition-all hover:border-emerald-300 hover:text-emerald-600"
        >← 返回发现</button>

        <div v-if="currentMode === 'search'" class="flex items-center gap-1.5 overflow-x-auto pb-0.5">
          <button
            v-for="p in platformsWithMusicDL" :key="p.id"
            @click="switchPlatform(p.id)"
            :class="[
              'px-2.5 sm:px-3 py-1 rounded-lg text-xs font-medium transition-all whitespace-nowrap flex items-center gap-1',
              selectedPlatform === p.id ? 'bg-emerald-500 text-white shadow-sm' : 'bg-gray-100 text-gray-500 hover:bg-gray-200'
            ]"
          >
            <span>{{ p.icon }}</span><span>{{ p.name }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- ── 内容展示主区域 ── -->
    <div class="flex-1 overflow-y-auto">

      <!--
        0. 未导入 LX 音源时的提示条（**不遮挡内容**）
        官方协议由后端内置，免脚本即可搜索 / 逛榜单 / 看歌单 / 看歌词；
        只有「取直链」这一步需要 LX 协议。因此这里只提示，不禁用。
      -->
      <div v-if="!hasActiveSource" class="max-w-[1500px] mx-auto px-5 pt-6 pb-0 sm:px-8 sm:pt-7">
        <div class="flex flex-wrap items-center gap-2.5 rounded-xl border border-amber-200 bg-amber-50/70 px-4 py-2.5 text-[11px] leading-relaxed text-amber-900">
          <Radio class="w-3.5 h-3.5 shrink-0 text-amber-600" />
          <span class="min-w-0 flex-1">
            <b class="text-amber-700">还没有可用的音源</b> —— 搜索、榜单、歌单、歌词都能正常用，
            只有<b>播放和下载</b>需要导入音源。
          </span>
          <button
            @click="emit('navigate', 'sources')"
            class="shrink-0 cursor-pointer rounded-lg bg-emerald-500 px-3 py-1.5 font-semibold text-white transition-all hover:bg-emerald-600"
          >导入音源</button>
        </div>
      </div>

      <!-- 榜单 / 歌单 / 分享链接加载失败：一句原因 + 「查看日志」入口（替代原来的 alert） -->
      <div v-if="loadError" class="max-w-[1500px] mx-auto px-5 pt-6 pb-0 sm:px-8 sm:pt-7">
        <InlineNotice :text="loadError" />
      </div>

      <!-- 订阅结果提示：订阅成功 / 已停止更新 / 失败原因 -->
      <div v-if="subNotice" class="max-w-[1500px] mx-auto px-5 pt-6 pb-0 sm:px-8 sm:pt-7">
        <InlineNotice :text="subNotice" :tone="subNoticeTone" />
      </div>

      <!--
        hero 双列头部（布局参考 fnmusic-flow 的 .hero：grid 1.35fr / .65fr，gap 18px，radius 18px）
        左：渐变欢迎卡（eyebrow 日期 + 问候语 + 快捷入口）
        右：深色「正在播放」卡
        主色用项目 emerald，未照搬 flow 的蓝。
      -->
      <div v-if="currentMode === 'discover'" class="max-w-[1500px] mx-auto px-5 pt-6 pb-0 sm:px-8 sm:pt-7">
        <div class="grid grid-cols-1 gap-[18px] lg:grid-cols-[1.35fr_0.65fr]">

          <!-- 左：欢迎卡（同时收纳了平台选择与榜单/歌单切换，页面上方不再需要额外工具条） -->
          <div class="rounded-[18px] border border-emerald-100 bg-gradient-to-br from-emerald-50 via-white to-white px-6 py-6 sm:px-[30px] sm:py-[26px]">
            <div class="text-[12px] font-bold text-emerald-600">{{ heroEyebrow }}</div>
            <h1 class="my-1.5 text-[26px] font-bold tracking-tight text-gray-800 sm:text-[29px]">{{ heroGreeting }}</h1>
            <p class="text-[13px] leading-relaxed text-gray-500">
              搜索全网曲库，或浏览官方榜单与精选歌单。
            </p>

            <!-- 第 1 行：平台选择 -->
            <div class="mt-5 flex flex-wrap items-center gap-2">
              <span class="shrink-0 text-[11px] font-medium text-gray-400">平台</span>
              <div
                v-if="availablePlatforms.length > 0"
                class="flex flex-wrap items-center gap-0.5 rounded-[11px] bg-white/80 p-1 ring-1 ring-gray-200/70"
              >
                <button
                  v-for="p in availablePlatforms" :key="p.id"
                  @click="switchChartPlatform(p.id)"
                  :class="[
                    'px-2.5 py-1.5 rounded-lg text-[12px] font-bold transition-all flex items-center gap-1 whitespace-nowrap',
                    selectedChartPlatform === p.id ? 'bg-emerald-500 text-white shadow-sm' : 'text-gray-500 hover:text-gray-700'
                  ]"
                >
                  <span>{{ p.icon }}</span><span>{{ p.name }}</span>
                </button>
              </div>
            </div>

            <!-- 第 2 行：榜单 / 歌单 切换 -->
            <div class="mt-2.5 flex shrink-0 items-center gap-0.5 rounded-[11px] bg-white/80 p-1 ring-1 ring-gray-200/70 w-max">
              <button
                @click="switchDiscoverTab('charts')"
                :class="[
                  'px-3 py-1.5 rounded-lg text-[12px] font-bold transition-all flex items-center gap-1 whitespace-nowrap',
                  discoverTab === 'charts' ? 'bg-emerald-500 text-white shadow-sm' : 'text-gray-500 hover:text-gray-700'
                ]"
              >
                <span>🔥</span><span>排行榜单</span>
              </button>
              <button
                @click="switchDiscoverTab('playlists')"
                :class="[
                  'px-3 py-1.5 rounded-lg text-[12px] font-bold transition-all flex items-center gap-1 whitespace-nowrap',
                  discoverTab === 'playlists' ? 'bg-emerald-500 text-white shadow-sm' : 'text-gray-500 hover:text-gray-700'
                ]"
              >
                <span>🎧</span><span>精选歌单</span>
              </button>
              <button
                @click="switchDiscoverTab('manage')"
                :class="[
                  'px-3 py-1.5 rounded-lg text-[12px] font-bold transition-all flex items-center gap-1 whitespace-nowrap',
                  discoverTab === 'manage' ? 'bg-emerald-500 text-white shadow-sm' : 'text-gray-500 hover:text-gray-700'
                ]"
              >
                <span>⚙️</span><span>榜单管理</span>
              </button>
            </div>

            <!--
              歌单分类快捷筛选。
              显示条件是「该平台**有**分类」而不是「平台是网易云」——
              各平台能力不同：网易云固定表、QQ 动态拉、酷狗/酷我没有（后端返回空数组）。

              只露**一行**，其余用「+N」收起：分类动辄几十个（QQ 有 65 个），全铺出来会把
              下面的歌单网格挤到很下面。同时按**使用次数**排序，折叠后露出的基本就是常用的那几个；
              当前选中的那个会强制排到第一位，保证「正在筛什么」始终可见。
            -->
            <div
              v-if="discoverTab === 'playlists' && sortedCategories.length > 0"
              class="mt-2.5 flex items-start gap-1.5"
            >
              <div
                ref="catBoxRef"
                class="relative flex min-w-0 flex-1 flex-wrap items-center gap-1 overflow-hidden"
                :style="catExpanded ? {} : { maxHeight: catBoxMaxH }"
              >
                <button
                  v-for="cat in sortedCategories" :key="cat.id"
                  @click="selectPlaylistCategory(cat.id)"
                  :class="[
                    'px-2.5 py-1 rounded-full text-[11px] whitespace-nowrap transition-all',
                    selectedCategory === cat.id
                      ? 'bg-emerald-500 text-white font-semibold'
                      : 'bg-white/70 text-gray-500 ring-1 ring-gray-200/60 hover:bg-gray-100'
                  ]"
                >{{ cat.name }}</button>
              </div>

              <!-- 折叠开关：折叠时显示「+N」，展开后变「收起」 -->
              <button
                v-if="hiddenCatCount > 0 || catExpanded"
                @click="catExpanded = !catExpanded"
                class="shrink-0 rounded-full bg-gray-100 px-2.5 py-1 text-[11px] font-medium text-gray-600 transition-colors hover:bg-gray-200"
                :title="catExpanded ? '收起分类' : `还有 ${hiddenCatCount} 个分类`"
              >{{ catExpanded ? '收起' : `+${hiddenCatCount}` }}</button>
            </div>
          </div>

          <!-- 右：正在播放（深色卡，同 flow 的 .now） -->
          <!-- 深色「正在播放」卡：**移动端隐藏**（用户 2026-09-28：「发现音乐页面可以单独优化移动端：
               可以隐藏黑色卡片播放器」）。桌面端（≥768px）保持原样。 -->
          <div class="hidden items-center gap-[15px] rounded-[18px] bg-[#172033] px-5 py-5 text-white md:flex">
            <div class="grid h-[90px] w-[90px] shrink-0 place-items-center overflow-hidden rounded-[12px] bg-gradient-to-br from-emerald-400 to-emerald-800 text-[30px]">
              <img
                v-if="currentSong?.cover"
                :src="currentSong.cover"
                alt=""
                referrerpolicy="no-referrer"
                class="h-full w-full object-cover"
              />
              <span v-else>♪</span>
            </div>
            <div class="min-w-0 flex-1">
              <small class="text-[11px] text-gray-400">
                {{ isPlaying ? '正在播放' : (currentSong ? '已暂停' : '待播放') }}
              </small>
              <h3 class="my-1 truncate text-[17px] font-bold">{{ currentSong?.name || '还没有播放歌曲' }}</h3>
              <small class="block truncate text-[11px] text-gray-400">
                {{ currentSong ? (currentSong.singer || '未知歌手') : '挑一首歌开始吧' }}
              </small>
              <div class="mt-2.5 flex items-center gap-2">
                <button
                  @click="emit('prev')" :disabled="!currentSong" title="上一首"
                  class="grid h-8 w-8 place-items-center rounded-full bg-white/10 text-[15px] transition-colors hover:bg-white/20 disabled:opacity-40"
                >‹</button>
                <button
                  @click="emit('toggle-play')" :disabled="!currentSong" title="播放 / 暂停"
                  class="grid h-9 w-9 place-items-center rounded-full bg-emerald-500 text-[12px] font-bold transition-colors hover:bg-emerald-600 disabled:opacity-40"
                >{{ isPlaying ? '❚❚' : '▶' }}</button>
                <button
                  @click="emit('next')" :disabled="!currentSong" title="下一首"
                  class="grid h-8 w-8 place-items-center rounded-full bg-white/10 text-[15px] transition-colors hover:bg-white/20 disabled:opacity-40"
                >›</button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!--
        双平台推荐：网易云 + QQ 音乐的个人推荐。
        扫码登录 / 登出**就在这两张卡片里**（2026-09-27 从「账号连接」搬来：那页的扫码登录
        与这里重复，用户要求只留一处）。二维码弹窗见 components/QrLoginModal.vue，
        状态映射见 services/qrLogin.js。
        曲目来自 GET /api/accounts/tracks —— 已归一化为可播放形状，点击即入列表，可播放/下载。
      -->
      <div v-if="currentMode === 'discover'" class="max-w-[1500px] mx-auto px-5 pt-6 pb-0 sm:px-8 sm:pt-7">
        <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
          <div class="min-w-0">
            <h2 class="text-[19px] font-bold text-gray-800">双平台推荐</h2>
            <p class="mt-1 text-[12px] text-gray-400">网易云与 QQ 音乐各自独立的个人推荐空间</p>
          </div>
          <!-- 就地刷新连接状态（不再是「管理账号 → 跳走」） -->
          <button
            @click="refreshAccounts()"
            :disabled="accountsLoading"
            class="shrink-0 text-[12px] font-semibold text-emerald-600 hover:underline disabled:opacity-50"
          >{{ accountsLoading ? '刷新中…' : '刷新状态' }}</button>
        </div>

        <!-- 移动端就一行两个（用户 2026-09-28：「两个卡片可以窄一点，一行两个」）：
             两列在所有宽度都保留，只把内边距/字号/图标在窄屏收小 —— 不用整体缩放。 -->
        <div class="grid grid-cols-2 gap-3 md:gap-4">
          <div
            v-for="p in recommendPlatforms" :key="p.id"
            class="overflow-hidden rounded-[15px] border border-gray-200 bg-white"
            :class="p.topBorder"
          >
            <!-- 卡片头 -->
            <div class="flex items-center justify-between gap-2 border-b border-gray-100 px-3 py-3 sm:gap-3 sm:px-4 sm:py-3.5">
              <div class="flex min-w-0 items-center gap-2 sm:gap-2.5">
                <div class="grid h-8 w-8 shrink-0 place-items-center rounded-[9px] text-[14px] font-extrabold sm:h-9 sm:w-9 sm:rounded-[10px] sm:text-[17px]" :class="p.markClass">
                  {{ p.mark }}
                </div>
                <div class="min-w-0">
                  <b class="block truncate text-[13px] text-gray-800 sm:text-sm">{{ p.name }}</b>
                  <span class="block truncate text-[10px] text-gray-400 sm:text-[11px]">{{ p.desc }}</span>
                </div>
              </div>
              <span
                class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium sm:px-2.5 sm:py-1 sm:text-[11px]"
                :class="p.connected ? 'bg-emerald-50 text-emerald-600' : 'bg-gray-100 text-gray-500'"
              >{{ p.connected ? '已连接' : '未连接' }}</span>
            </div>

            <!-- 卡片体 -->
            <div class="px-3 py-3 sm:px-4 sm:py-4">
              <template v-if="p.connected">
                <!-- 账号行：谁登着 + 登出（登出=断开本机凭据，下次要重新扫码） -->
                <div class="mb-2.5 flex items-center gap-2 sm:mb-3 sm:gap-2.5">
                  <img
                    v-if="p.avatar"
                    :src="p.avatar"
                    class="h-8 w-8 shrink-0 rounded-full object-cover sm:h-9 sm:w-9"
                    referrerpolicy="no-referrer"
                    @error="(e) => (e.target.style.display = 'none')"
                  />
                  <div v-else class="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-gray-100 text-[12px] font-bold text-gray-400 sm:h-9 sm:w-9 sm:text-[13px]">
                    {{ (p.nickname || p.mark).slice(0, 1) }}
                  </div>
                  <div class="min-w-0 flex-1">
                    <b class="block truncate text-[12px] text-gray-800 sm:text-[13px]">{{ p.nickname || '已登录' }}</b>
                    <span class="block truncate text-[10px] text-gray-400 sm:text-[11px]">UID {{ p.uid || '-' }}</span>
                  </div>
                  <button
                    class="shrink-0 rounded-lg border border-gray-200 px-2 py-1 text-[11px] font-medium text-gray-500 transition-all hover:border-rose-300 hover:bg-rose-50 hover:text-rose-600 disabled:opacity-50 sm:px-2.5 sm:py-1.5 sm:text-[12px]"
                    :disabled="!!platformBusy"
                    @click="disconnectPlatform(p)"
                  >{{ platformBusy === p.id + ':disconnect' ? '登出中…' : '登出' }}</button>
                </div>

                <!-- 每日推荐 -->
                <button
                  @click="loadRecommend(p.id, 'daily')"
                  :disabled="!!recommendLoading"
                  class="flex w-full items-center gap-2 rounded-xl border border-gray-200 px-2.5 py-2.5 text-left transition-all hover:border-emerald-300 hover:bg-emerald-50/30 disabled:opacity-60 sm:gap-3 sm:px-3.5 sm:py-3"
                >
                  <div class="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-emerald-50 text-emerald-600 sm:h-9 sm:w-9">
                    <CalendarHeart class="w-4 h-4" />
                  </div>
                  <div class="min-w-0 flex-1">
                    <b class="block text-[12px] text-gray-800 sm:text-[13px]">每日推荐</b>
                    <span class="block text-[11px] text-gray-400">{{ p.dailyHint }}</span>
                  </div>
                  <Loader2 v-if="recommendLoading === p.id + ':daily'" class="w-4 h-4 shrink-0 animate-spin text-emerald-500" />
                  <ChevronRight v-else class="w-4 h-4 shrink-0 text-gray-300" />
                </button>

                <!-- 我的歌单（最多 4 个） -->
                <div v-if="p.playlists.length" class="mt-2.5">
                  <span class="mb-1.5 block text-[11px] text-gray-400">我的歌单</span>
                  <div class="flex flex-wrap gap-1.5">
                    <button
                      v-for="pl in p.playlists.slice(0, 4)" :key="pl.id"
                      @click="loadRecommend(p.id, 'playlist', pl.id)"
                      :disabled="!!recommendLoading"
                      class="max-w-full truncate rounded-lg bg-gray-50 px-2.5 py-1.5 text-[11px] text-gray-600 ring-1 ring-gray-200 transition-colors hover:bg-emerald-50 hover:text-emerald-600 disabled:opacity-60"
                      :title="pl.name + (pl.count ? ` · ${pl.count} 首` : '')"
                    >{{ pl.name }}</button>
                  </div>
                </div>
                <p v-else-if="p.playlistsLoading" class="mt-2.5 text-[11px] text-gray-300">正在读取歌单…</p>
              </template>

              <!-- 未连接：就地扫码登录（二维码弹窗在本组件里，不再跳到「账号连接」） -->
              <div v-else class="flex flex-col items-center gap-2.5 py-4 text-center">
                <p class="text-[12px] leading-relaxed text-gray-400">{{ p.loginHint }}</p>
                <button
                  @click="startPlatformLogin(p)"
                  :disabled="!!platformBusy"
                  class="rounded-lg bg-emerald-500 px-3.5 py-1.5 text-[12px] font-semibold text-white transition-all hover:bg-emerald-600 disabled:opacity-50"
                >{{ platformBusy === p.id + ':qr' ? '正在获取二维码…' : '扫码登录' }}</button>
              </div>
            </div>
          </div>
        </div>

        <!-- 卡片区内的失败提示（一句人话，完整信息进日志） -->
        <div v-if="platformError" class="mt-3">
          <InlineNotice :text="platformError" />
        </div>
      </div>

      <!-- 平台扫码登录弹窗（状态全在 services/qrLogin.js） -->
      <QrLoginModal :session="qr" @close="closeQr" @restart="restartQr" />

      <!-- 1. 发现大厅 - 排行榜单网格 (紧凑精致卡片排版) -->
      <!-- 榜单管理（选哪些榜单进飞牛；浏览请看下面的「排行榜单」） -->
      <ChartManager
        v-if="currentMode === 'discover' && discoverTab === 'manage'"
        @preview="openChartPreview"
      />

      <div v-if="currentMode === 'discover' && discoverTab === 'charts'" class="max-w-[1500px] mx-auto px-5 py-6 pb-28 sm:px-8 sm:py-7 sm:pb-24">
        <!--
          控制条搬走了：勾选与开关现在住在「⚙️ 榜单管理」页。
          这里只留一行指路 —— 这一页是**浏览**用的，用户是来看榜单的，不该被一排开关挡在最上面。
          （卡片右上角那个「+ 注入」留着：就地加一个榜单是最顺手的一条路。）
        -->
        <div class="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-gray-100 bg-white px-3.5 py-2.5">
          <span class="text-[12px] font-bold text-gray-800 whitespace-nowrap">🔥 排行榜单</span>
          <span class="text-[11px] leading-snug text-gray-500">
            要让这些榜单出现在<strong class="text-gray-700">飞牛音乐</strong>里，去
            <button
              type="button"
              @click="switchDiscoverTab('manage')"
              class="font-bold text-emerald-600 underline decoration-emerald-300 underline-offset-2 hover:text-emerald-700"
            >⚙️ 榜单管理</button>
            勾选
          </span>
          <span class="text-[11px] leading-snug text-amber-600/80">
            只保留能播放的曲目，所以飞牛里的歌单会比平台榜单短
          </span>
        </div>
        <div v-if="isLoadingCharts" class="flex flex-col items-center justify-center py-20 text-gray-400">
          <Loader2 class="w-8 h-8 animate-spin text-emerald-500 mb-2" />
          <p class="text-xs">正在载入权威榜单...</p>
        </div>
        <div v-else class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
          <div
            v-for="chart in toplists" :key="chart.id"
            @click="openChartDetail(chart)"
            class="relative bg-white rounded-xl border border-gray-100 p-2.5 hover:border-emerald-300 hover:shadow-sm transition-all cursor-pointer flex items-center gap-3 group"
          >
            <!--
              注入开关（右上角）。`@click.stop` —— 卡片整块是「看榜单详情」，
              点这里只切注入，不能顺手把详情也打开。
              触摸屏没有 hover，所以未勾选时也保持半透明可见（不然手机上找不到）。
            -->
            <button
              type="button"
              :disabled="chartInjectBusy"
              :title="isChartInjected(chart) ? '从飞牛音乐的歌单里移除' : '加入飞牛音乐的歌单列表'"
              @click.stop="toggleChartInject(chart)"
              class="absolute right-2 top-2 z-10 rounded-full px-2 py-0.5 text-[10px] font-bold transition-all disabled:opacity-40 whitespace-nowrap"
              :class="isChartInjected(chart)
                ? 'bg-emerald-500 text-white shadow-sm'
                : 'bg-gray-100/90 text-gray-400 opacity-60 group-hover:opacity-100 group-hover:bg-emerald-50 group-hover:text-emerald-600'"
            >{{ isChartInjected(chart) ? '★ 已注入' : '+ 注入' }}</button>
            <!-- 固定 76x76px 尺寸封面，杜绝撑大 -->
            <div
              class="relative rounded-lg overflow-hidden bg-gray-100 shrink-0 shadow-2xs"
              style="width: 76px; height: 76px; min-width: 76px; min-height: 76px; max-width: 76px; max-height: 76px;"
            >
              <img :src="chart.cover" alt="cover" referrerpolicy="no-referrer" class="w-full h-full object-cover group-hover:scale-105 transition-transform duration-300" />
              <div class="absolute inset-0 bg-black/20 opacity-0 group-hover:opacity-100 flex items-center justify-center transition-opacity">
                <div class="w-7 h-7 rounded-full bg-emerald-500 text-white flex items-center justify-center shadow-md">
                  <Play class="w-3.5 h-3.5 ml-0.5 fill-current" />
                </div>
              </div>
              <span v-if="chart.update_frequency" class="absolute bottom-1 right-1 text-[8px] px-1 py-0.2 rounded bg-black/60 text-white backdrop-blur">
                {{ chart.update_frequency }}
              </span>
            </div>

            <!-- 右侧歌曲预览（`pr-12` 给右上角的注入按钮留位，别被它压住标题） -->
            <div class="flex-1 min-w-0 pr-12">
              <h4 class="text-xs sm:text-sm font-bold text-gray-800 group-hover:text-emerald-600 transition-colors truncate mb-1">
                {{ chart.name }}
              </h4>
              <div class="space-y-0.5">
                <p v-for="(t, idx) in chart.tracks.slice(0, 3)" :key="idx" class="text-[11px] text-gray-500 truncate leading-tight">
                  <span class="text-gray-400 font-mono text-[10px] mr-1">{{ idx + 1 }}.</span>
                  {{ t }}
                </p>
                <p v-if="!chart.tracks.length" class="text-[10px] text-gray-300 italic">点击查看完整歌曲</p>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 2. 发现大厅 - 精选歌单网格 -->
      <div v-else-if="currentMode === 'discover' && discoverTab === 'playlists'" class="max-w-[1500px] mx-auto px-5 py-6 pb-28 sm:px-8 sm:py-7 sm:pb-24">
        <div v-if="isLoadingPlaylists" class="flex flex-col items-center justify-center py-20 text-gray-400">
          <Loader2 class="w-8 h-8 animate-spin text-emerald-500 mb-2" />
          <p class="text-xs">正在载入精选歌单...</p>
        </div>
        <div v-else class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-3 sm:gap-3.5">
          <div
            v-for="pl in playlists" :key="pl.id"
            @click="openPlaylistDetail(pl)"
            class="bg-white rounded-xl border border-gray-100 p-2 hover:border-emerald-300 hover:shadow-sm transition-all cursor-pointer group flex flex-col"
          >
            <div class="relative aspect-square rounded-lg overflow-hidden bg-gray-100 mb-2">
              <img :src="pl.cover" alt="cover" referrerpolicy="no-referrer" class="w-full h-full object-cover group-hover:scale-105 transition-transform duration-300" />
              <div class="absolute inset-0 bg-black/20 opacity-0 group-hover:opacity-100 flex items-center justify-center transition-opacity">
                <div class="w-8 h-8 rounded-full bg-emerald-500 text-white flex items-center justify-center shadow-lg">
                  <Play class="w-4 h-4 ml-0.5 fill-current" />
                </div>
              </div>
              <span class="absolute top-1.5 right-1.5 text-[9px] px-1.5 py-0.5 rounded-full bg-black/50 text-white backdrop-blur flex items-center gap-0.5">
                🔥 {{ formatPlayCount(pl.play_count) }}
              </span>
            </div>
            <h4 class="text-xs font-semibold text-gray-800 line-clamp-2 leading-snug group-hover:text-emerald-600 transition-colors mb-1">
              {{ pl.title }}
            </h4>
            <p class="text-[10px] text-gray-400 truncate mt-auto">
              {{ pl.track_count ? pl.track_count + ' 首 · ' : '' }}{{ pl.creator }}
            </p>
          </div>
        </div>
      </div>

      <!-- 3. 歌曲列表视图 (搜索结果 / 榜单详情 / 歌单详情 公用) -->
      <div v-else class="max-w-[1500px] mx-auto px-5 py-6 pb-28 sm:px-8 sm:py-7 sm:pb-24 space-y-3">
        <!-- 头部导航与批量操作工具栏 -->
        <div class="bg-white rounded-xl border border-gray-100 p-3.5 shadow-xs">
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 mb-2.5">
            <div class="flex items-center gap-2.5 min-w-0">
              <button
                @click="resetToDiscover"
                class="p-1.5 rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-100 transition-colors shrink-0"
                title="返回发现"
              >
                <ArrowLeft class="w-4 h-4" />
              </button>
              <div
                class="rounded-lg overflow-hidden bg-gray-100 shrink-0 flex items-center justify-center shadow-2xs"
                style="width: 44px; height: 44px; min-width: 44px; min-height: 44px;"
              >
                <img v-if="currentDetailMeta?.cover" :src="currentDetailMeta.cover" alt="cover" referrerpolicy="no-referrer" class="w-full h-full object-cover" />
                <span v-else class="text-lg">🎵</span>
              </div>
              <div class="min-w-0">
                <h3 class="text-sm font-bold text-gray-800 flex items-center gap-2 truncate">
                  <span class="truncate">{{ currentDetailMeta?.title || (query ? `搜索：“${query}”` : '歌曲列表') }}</span>
                  <span class="text-xs text-gray-400 font-normal shrink-0">({{ activeSongList.length }} 首)</span>
                </h3>
                <p class="text-xs text-gray-400 truncate max-w-md">
                  {{ currentDetailMeta?.desc || (currentMode === 'search' ? '跨平台聚合搜索结果' : '曲率 播放列表') }}
                </p>
              </div>
            </div>

            <!-- 控制按钮群 -->
            <div class="flex items-center gap-2 flex-wrap shrink-0">
              <!-- 一键播放全部 -->
              <button
                @click="playAllSongs"
                :disabled="!activeSongList.length"
                class="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold flex items-center gap-1.5 shadow-sm transition-all disabled:opacity-50"
              >
                <Play class="w-3.5 h-3.5 fill-current" />
                <span>播放全部</span>
              </button>

              <!-- 播放模式切换 (顺序、随机、循环) -->
              <button
                @click="cyclePlayMode"
                class="px-2.5 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-gray-700 text-xs font-medium flex items-center gap-1.5 transition-all"
                :title="'当前模式：' + playModeLabel"
              >
                <component :is="playModeIcon" class="w-3.5 h-3.5 text-emerald-600" />
                <span>{{ playModeLabel }}</span>
              </button>

              <!-- 一键批量下载到 NAS -->
              <button
                @click="batchDownloadAll"
                :disabled="!activeSongList.length"
                class="px-3 py-1.5 rounded-lg border transition-all text-xs font-semibold flex items-center gap-1.5 shadow-xs bg-emerald-50 hover:bg-emerald-100 border-emerald-200 text-emerald-700 cursor-pointer"
                title="一键下载当前全部歌曲到 NAS"
              >
                <HardDriveDownload class="w-3.5 h-3.5 shrink-0" />
                <span>一键下载全部 ({{ activeSongList.length }})</span>
              </button>

              <!--
                订阅更新（歌单详情 / 榜单详情）：点开选「要做什么」，再建任务。
                以前是一键两条都建 —— 只想自动下载的用户会平白多出一条飞牛同步任务。
              -->
              <template v-if="currentPlaylistRef">
                <button
                  v-if="!subscription"
                  @click="openSubDialog"
                  :disabled="subscribingSub || !activeSongList.length"
                  class="px-3 py-1.5 rounded-lg border transition-all text-xs font-semibold flex items-center gap-1.5 shadow-xs bg-blue-50 hover:bg-blue-100 border-blue-200 text-blue-700 cursor-pointer disabled:opacity-50"
                  title="订阅这个歌单：以后新增的歌自动下载 / 同步到飞牛音乐歌单（可选）"
                >
                  <BellPlus class="w-3.5 h-3.5 shrink-0" />
                  <span>{{ subscribingSub ? '订阅中…' : '订阅更新' }}</span>
                </button>

                <template v-else>
                  <span
                    class="px-3 py-1.5 rounded-lg border text-xs font-semibold flex items-center gap-1.5"
                    :class="subscription.active
                      ? 'bg-emerald-50 border-emerald-200 text-emerald-700'
                      : 'bg-gray-50 border-gray-200 text-gray-500'"
                    :title="subscription.active ? '正在自动下载新增曲目并同步飞牛歌单' : '已暂停，随时可以恢复'"
                  >
                    <BellPlus class="w-3.5 h-3.5 shrink-0" />
                    <span>{{ subscription.active ? '已订阅' : '已暂停' }}</span>
                  </span>
                  <button
                    @click="toggleCurrentSubscription"
                    :disabled="togglingSub"
                    class="px-3 py-1.5 rounded-lg border border-gray-200 hover:bg-gray-100 text-gray-600 text-xs font-semibold flex items-center gap-1.5 transition-all cursor-pointer disabled:opacity-50"
                  >
                    <span>{{ togglingSub ? '处理中…' : (subscription.active ? '停止更新' : '恢复更新') }}</span>
                  </button>
                </template>
              </template>

              <!-- 一键监控：搜索结果/榜单详情下把当前列表变成定时监控 -->
              <button
                v-else
                @click="openMonitorDialog"
                :disabled="!activeSongList.length"
                class="px-3 py-1.5 rounded-lg border transition-all text-xs font-semibold flex items-center gap-1.5 shadow-xs bg-blue-50 hover:bg-blue-100 border-blue-200 text-blue-700 cursor-pointer disabled:opacity-50"
                title="把当前列表建成定时监控：以后该来源有新歌会自动下载"
              >
                <BellPlus class="w-3.5 h-3.5 shrink-0" />
                <span>一键监控</span>
              </button>

              <!-- 设置下载目录 -->
              <button
                @click="downloadManager.openDirModal()"
                class="px-2 py-1.5 rounded-lg border border-gray-200 hover:bg-gray-100 text-gray-600 text-xs flex items-center gap-1 transition-all cursor-pointer"
                :title="'当前保存目录：' + downloadManager.currentDownloadDir.value"
              >
                <Folder class="w-3.5 h-3.5 text-amber-500" />
                <span class="max-w-[90px] truncate hidden sm:inline">{{ downloadManager.currentDownloadDir.value }}</span>
                <span class="sm:hidden">设置目录</span>
              </button>
            </div>
          </div>
        </div>

        <!-- 歌曲列表主体 -->
        <div v-if="isLoadingDetail || isSearching" class="flex flex-col items-center justify-center py-20 text-gray-400">
          <Loader2 class="w-8 h-8 animate-spin text-emerald-500 mb-2" />
          <p class="text-xs">{{ isSearching ? '正在检索歌曲...' : '正在载入曲目列表...' }}</p>
        </div>

        <div v-else-if="!activeSongList.length" class="flex flex-col items-center justify-center py-20 text-gray-400 bg-white rounded-xl border border-gray-100">
          <Music2 class="w-10 h-10 text-gray-300 mb-2" />
          <p class="text-sm font-semibold text-gray-600">未找到相关歌曲</p>
          <p class="text-xs text-gray-400 mt-1">请尝试更换关键词或在「音源管理」载入更多音源</p>
        </div>

        <div v-else class="bg-white rounded-xl border border-gray-100 divide-y divide-gray-100 overflow-hidden shadow-2xs">
          <div
            v-for="(song, idx) in activeSongList" :key="song.id || idx"
            @click="playSong(song, idx)"
            class="flex items-center gap-2.5 sm:gap-3 px-3 sm:px-4 py-2.5 hover:bg-emerald-50/40 cursor-pointer transition-colors group"
          >
            <!--
              序号 / 播放小图标
              ⚠️ 桌面是「悬停时把序号换成播放图标」，触屏没有 hover —— 手机上永远是序号，
              看不出这一行能点。所以触屏常显播放图标（序号在手机上本来也没用），桌面行为不变。
            -->
            <span class="w-5 sm:w-6 text-center text-xs text-gray-300 font-mono group-hover:hidden hide-on-touch shrink-0">{{ idx + 1 }}</span>
            <div class="w-5 sm:w-6 hidden group-hover:flex show-on-touch-flex items-center justify-center shrink-0">
              <Play class="w-3.5 h-3.5 text-emerald-500 fill-current" />
            </div>

            <!-- 封面缩略图 -->
            <div class="w-9 h-9 rounded-lg overflow-hidden bg-gray-100 shrink-0 flex items-center justify-center shadow-2xs">
              <img v-if="song.cover" :src="song.cover" alt="cover" referrerpolicy="no-referrer" class="w-full h-full object-cover" @error="song.cover = ''" />
              <span v-else class="text-base">🎵</span>
            </div>

            <!-- 歌名与歌手 -->
            <div class="flex-1 min-w-0">
              <p class="text-xs sm:text-sm font-medium text-gray-800 truncate group-hover:text-emerald-600 transition-colors">
                {{ song.name }}
              </p>
              <p class="text-[11px] text-gray-400 truncate">
                {{ song.singer }}{{ song.album ? ' · ' + song.album : '' }}
              </p>
            </div>

            <!-- 渠道标识 -->
            <span class="text-[10px] px-1.5 py-0.5 rounded-full bg-gray-100 text-gray-500 font-mono shrink-0">
              {{ song.source || 'wy' }}
            </span>

            <!-- 时长 -->
            <span class="text-xs text-gray-400 font-mono shrink-0 w-11 text-right">
              {{ formatDuration(song.interval || song.duration) }}
            </span>

            <!-- 单曲下载到 NAS 按钮 -->
            <div class="shrink-0" @click.stop>
              <!-- 已下载 -->
              <span
                v-if="downloadManager.isSongDownloaded(song)"
                class="px-2 py-0.5 rounded-lg bg-emerald-50 text-emerald-600 text-[11px] font-medium flex items-center gap-1 border border-emerald-200"
                title="已保存至 NAS 存储目录"
              >
                <Check class="w-3.5 h-3.5" />
                <span class="hidden sm:inline">已在NAS</span>
              </span>

              <!-- 下载中 (点击可打开下载任务抽屉) -->
              <span
                v-else-if="downloadManager.isSongDownloading(song)"
                @click="downloadManager.open()"
                class="px-2 py-0.5 rounded-lg bg-emerald-50/60 text-emerald-600 text-[11px] flex items-center gap-1 cursor-pointer hover:bg-emerald-100 transition-colors"
                title="点击查看下载任务进度"
              >
                <Loader2 class="w-3.5 h-3.5 animate-spin text-emerald-500" />
                <span class="hidden sm:inline">下载中</span>
              </span>

              <!-- 未下载，点击下载 (自动加入全局后台队列并滑出下载面板) -->
              <!-- .stop 必须保留：否则会冒泡到整行的 @click=playSong，点下载会顺带开始播放 -->
              <button
                v-else
                @click.stop="downloadSingleSong(song)"
                class="p-1.5 rounded-lg hover:bg-emerald-50 text-gray-400 hover:text-emerald-600 transition-colors cursor-pointer"
                title="下载歌曲到 NAS"
              >
                <Download class="w-4 h-4" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ── 轻量下载操作提示 Toast ── -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-all duration-200 ease-out"
        enter-from-class="opacity-0 translate-y-2 scale-95"
        enter-to-class="opacity-100 translate-y-0 scale-100"
        leave-active-class="transition-all duration-150 ease-in"
        leave-from-class="opacity-100 translate-y-0 scale-100"
        leave-to-class="opacity-0 translate-y-2 scale-95"
      >
        <div
          v-if="toastMessage"
          class="fixed bottom-22 left-1/2 -translate-x-1/2 z-60 bg-gray-900/90 text-white text-xs px-4 py-2 rounded-full shadow-lg backdrop-blur-xs flex items-center gap-2 pointer-events-none"
        >
          <Check class="w-3.5 h-3.5 text-emerald-400" />
          <span>{{ toastMessage }}</span>
        </div>
      </Transition>
    </Teleport>
  </div>

  <!--
    ── 订阅更新弹窗 ──
    勾什么就建什么（下载到本地曲库 / 同步到飞牛歌单），并记住上次选择。
  -->
  <Teleport to="body">
    <div v-if="showSubDialog" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-xs">
      <div class="bg-white rounded-2xl max-w-md w-full p-5 shadow-2xl border border-gray-100">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-bold text-gray-800">订阅更新</h3>
          <button @click="showSubDialog = false" class="text-gray-400 hover:text-gray-600">
            <X class="w-4 h-4" />
          </button>
        </div>

        <p class="text-[11px] text-gray-500 mb-3 leading-relaxed">
          先记下当前 <b class="text-emerald-600">{{ activeSongList.length }}</b> 首歌作为起点。
          <b class="text-amber-600">第一次只记录、不下载</b>，之后每轮自动下载新增曲目。
        </p>

        <div class="rounded-lg border border-gray-200 px-3.5 py-3 mb-3">
          <p class="mb-2.5 text-[12px] font-medium text-gray-700">这个订阅要做什么</p>
          <div class="space-y-2.5">
            <div class="flex items-start gap-2.5">
              <button
                class="flex shrink-0 items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all"
                :class="subForm.download ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                @click="subForm.download = !subForm.download"
              >
                <Check v-if="subForm.download" class="w-3.5 h-3.5" />
                下载到本地曲库
              </button>
              <span class="pt-1.5 text-[11px] leading-relaxed text-gray-400">每轮发现的新歌自动下载到本地</span>
            </div>
            <div class="flex items-start gap-2.5">
              <button
                class="flex shrink-0 items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-medium transition-all disabled:opacity-40"
                :class="subForm.fnos ? 'bg-emerald-500 text-white' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'"
                :disabled="!subFnosReady"
                @click="subForm.fnos = !subForm.fnos"
              >
                <Check v-if="subForm.fnos" class="w-3.5 h-3.5" />
                同步到飞牛歌单
              </button>
              <span class="pt-1.5 text-[11px] leading-relaxed text-gray-400">
                {{ subFnosReady ? '把曲库里匹配上的曲目写进飞牛音乐歌单' : '飞牛音乐未连接，先到「账号连接」登录' }}
              </span>
            </div>
          </div>
        </div>

        <InlineNotice v-if="subNotice" :text="subNotice" :tone="subNoticeTone" />

        <div class="flex justify-end gap-2 mt-3">
          <button @click="showSubDialog = false" class="px-3 py-1.5 rounded-lg text-xs text-gray-500 hover:bg-gray-50">取消</button>
          <button
            @click="subscribeCurrent"
            :disabled="subscribingSub || (!subForm.download && !subForm.fnos)"
            class="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold disabled:opacity-50"
          >{{ subscribingSub ? '订阅中…' : '开始订阅' }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- ── 一键监控弹窗 ── -->
  <Teleport to="body">
    <div v-if="showMonitorDialog" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-xs">      <div class="bg-white rounded-2xl max-w-md w-full p-5 shadow-2xl border border-gray-100">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-bold text-gray-800">建立定时监控</h3>
          <button @click="showMonitorDialog = false" class="text-gray-400 hover:text-gray-600">
            <X class="w-4 h-4" />
          </button>
        </div>

        <p class="text-[11px] text-gray-500 mb-3 leading-relaxed">
          先记下当前 <b class="text-emerald-600">{{ activeSongList.length }}</b> 首歌作为起点。
          <b class="text-amber-600">第一次只记录、不下载</b>，之后每轮自动下载新增曲目。
        </p>

        <div class="space-y-3">
          <label class="block text-[11px] text-gray-500">
            监控名称
            <input v-model="monitorForm.name" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs" />
          </label>
          <label class="block text-[11px] text-gray-500">
            歌曲来源
            <select v-model="monitorForm.mode" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs">
              <option value="current">当前列表（{{ activeSongList.length }} 首）</option>
              <option value="playlist">绑定歌单链接（需填写）</option>
            </select>
          </label>
          <label v-if="monitorForm.mode === 'playlist'" class="block text-[11px] text-gray-500">
            歌单链接
            <input v-model="monitorForm.link" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs font-mono"
              placeholder="https://music.163.com/playlist?id=..." />
          </label>
          <label class="block text-[11px] text-gray-500">
            检查间隔
            <select v-model.number="monitorForm.interval" class="mt-1 w-full px-2.5 py-1.5 rounded-lg border border-gray-200 text-xs">
              <option :value="60">每小时</option>
              <option :value="180">每 3 小时</option>
              <option :value="360">每 6 小时</option>
              <option :value="720">每 12 小时</option>
              <option :value="1440">每天</option>
            </select>
          </label>
        </div>

        <InlineNotice
          v-if="monitorMsg"
          :text="monitorMsg"
          :tone="monitorOk ? 'ok' : 'error'"
          class="mt-3"
        />

        <div class="mt-4 flex justify-end gap-2">
          <button @click="showMonitorDialog = false" class="px-3 py-1.5 rounded-lg text-xs text-gray-500 hover:bg-gray-50">取消</button>
          <button @click="createMonitorFromList" :disabled="creatingMonitor"
            class="px-4 py-1.5 rounded-lg bg-blue-500 hover:bg-blue-600 text-white text-xs font-semibold disabled:opacity-50">
            {{ creatingMonitor ? '创建中...' : '创建并监控' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- ── 下载音质选择弹窗（单曲下载前弹出，档位来自官方协议的真实查询） ── -->
  <DownloadQualityModal
    v-model="showQualityModal"
    :song="qualityTargetSong"
    @confirm="onQualityConfirm"
  />
</template>

<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { markLoaded, isStale } from '../services/viewCache'
import { SearchAPI, ChartsAPI, MonitorAPI, AccountAPI, PushAPI, ChartInjectAPI, ConfigAPI } from '../api/client'
import { lxRuntime } from '../engine/lx-runtime'
import { qualityLabel, isHighestQuality } from '../engine/quality'
import { downloadManager } from '../services/downloadManager'
import { loadPref, savePref, memGet, memSet } from '../services/prefs'
import QRCode from 'qrcode'
import DownloadQualityModal from './DownloadQualityModal.vue'
import InlineNotice from './InlineNotice.vue'
import QrLoginModal from './QrLoginModal.vue'
import ChartManager from './ChartManager.vue'
import {
  chartInject,
  chartInjectKey,
  toggleItem as toggleChartItem,
  replaceIds as replaceInjectIds,
  setEnabled as setInjectEnabled,
  setUIInject as setUIInjectEnabled,
  loadConfig as loadInjectConfig,
} from '../services/chartInject'
import { fail, apiError, describeError } from '../services/userMsg'
import { logError, logWarn } from '../services/appLog'
import {
  QR_POLL_MS,
  LOGIN_SUCCESS_CLOSE_MS,
  qrCheckView,
  qrSessionView,
  shouldCloseAfterLogin,
  disconnectConfirmText,
} from '../services/qrLogin'
import {
  playlistKey, findSubscription, subscribePlaylist, setSubscriptionEnabled, fnosReady,
} from '../services/subscribe'
import {
  Search, Play, Loader2, Music2, Download, HardDriveDownload,
  Folder, ArrowLeft, Shuffle, Repeat, Repeat1, Check, X, Radio, BellPlus,
  CalendarHeart, ChevronRight
} from 'lucide-vue-next'

const props = defineProps({
  activeSource: Object,
  // 启用池（有序）。多选激活后：平台按钮取池内并集，取链挑"池里第一个支持该平台的源"。
  activeSourceIds: { type: Array, default: () => [] },
  playMode: {
    type: String,
    default: 'sequence'
  },
  // hero「正在播放」卡用（由 App.vue 传入）
  currentSong: Object,
  isPlaying: {
    type: Boolean,
    default: false,
  },
  // 底部播放栏的搜索入口（用户 2026-09-28：搜索放播放栏，发现页不再有搜索框）
  barQuery: { type: String, default: '' },
  /** 递增计数：同一个词连搜两次也要能触发 */
  barSearchNonce: { type: Number, default: 0 },
})

const emit = defineEmits(['play', 'play-all', 'update:play-mode', 'navigate', 'prev', 'next', 'toggle-play'])


// ── hero 问候语（参考 flow 的 .eyebrow + hero h1）──
const heroEyebrow = computed(() => {
  const d = new Date()
  const week = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'][d.getDay()]
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${week} · ${mm}月${dd}日`
})

const heroGreeting = computed(() => {
  const h = new Date().getHours()
  if (h < 6) return '夜深了，听点安静的'
  if (h < 12) return '早上好，来点音乐'
  if (h < 14) return '中午好，放松一下'
  if (h < 19) return '下午好，来点音乐'
  return '晚上好，来点音乐'
})

// ── 双平台推荐（网易云 / QQ 音乐的个人推荐）──
const accounts = ref([])                    // 已连接账号（来自 /api/accounts）
const recommendPlaylists = ref({ netease: [], qq: [] })
const recommendPlaylistsLoading = ref(false)
const recommendLoading = ref('')            // 形如 "netease:daily"，用于按按钮转圈

const PLATFORM_META = {
  netease: {
    id: 'netease', name: '网易云音乐', desc: '独立推荐空间', mark: 'N',
    markClass: 'bg-rose-50 text-rose-600', topBorder: 'border-t-[3px] border-t-rose-400',
    source: 'wy', appName: '网易云音乐',
    loginHint: '扫码登录后可读每日推荐与私人歌单（含创建与收藏）',
  },
  qq: {
    id: 'qq', name: 'QQ 音乐', desc: '独立推荐空间', mark: 'Q',
    markClass: 'bg-emerald-50 text-emerald-600', topBorder: 'border-t-[3px] border-t-emerald-400',
    source: 'tx', appName: 'QQ音乐',
    loginHint: '扫码登录后可读每日推荐与自建 / 收藏歌单',
  },
}

const recommendPlatforms = computed(() =>
  ['netease', 'qq'].map((id) => {
    const meta = PLATFORM_META[id]
    const acc = accounts.value.find((a) => a.provider === id)
    const list = recommendPlaylists.value[id] || []
    return {
      ...meta,
      connected: !!acc,
      nickname: acc?.nickname || '',
      avatar: acc?.avatar || '',
      uid: acc?.uid || '',
      dailyHint: acc?.nickname ? `${acc.nickname} 的每日推荐` : '登录后按你的口味生成',
      playlists: list.filter((p) => p.kind === 'playlist'),
      playlistsLoading: recommendPlaylistsLoading.value,
    }
  }),
)

async function loadAccounts() {
  try {
    const res = await AccountAPI.list()
    if (res?.code === 200) {
      accounts.value = res.data?.accounts || []
    }
  } catch (_) {
    // 账号列表拉取失败不阻塞发现页
  }
}

async function loadRecommendPlaylists() {
  recommendPlaylistsLoading.value = true
  try {
    const [wy, qq] = await Promise.allSettled([
      PushAPI.sources('netease'),
      PushAPI.sources('qq'),
    ])
    if (wy.status === 'fulfilled' && wy.value?.code === 200) {
      recommendPlaylists.value.netease = wy.value.data?.sources || []
    }
    if (qq.status === 'fulfilled' && qq.value?.code === 200) {
      recommendPlaylists.value.qq = qq.value.data?.sources || []
    }
  } finally {
    recommendPlaylistsLoading.value = false
  }
}

/** 拉取某平台的日推或歌单曲目，灌进歌曲列表直接可播可下 */
async function loadRecommend(provider, kind, id = '') {
  const key = `${provider}:${kind}`
  if (recommendLoading.value) return
  recommendLoading.value = key
  loadError.value = ''
  try {
    const res = await AccountAPI.tracks(provider, kind, id)
    const bad = apiError(res, '读取失败')
    if (bad) {
      loadError.value = bad
      logError('读取账号推荐', bad, `code=${res?.code} message=${res?.message} provider=${provider} kind=${kind}`)
      return
    }
    const songs = res.data?.songs || []
    if (!songs.length) {
      // 空列表多半不是出错，而是登录态过期或该来源本来就没有内容 —— 说清两种可能
      loadError.value = '这个来源没有曲目 —— 可能本来就是空的，也可能是账号登录态已过期，重新登录试试。'
      logWarn('读取账号推荐', '返回 0 首', `${provider}:${kind}`)
      return
    }
    currentMode.value = 'search'
    searchResults.value = songs
    activeSongList.value = songs
    currentDetailMeta.value = {
      title: res.data?.title || `${PLATFORM_META[provider]?.name || provider} · 推荐`,
      desc: `${songs.length} 首 · 来自已连接账号`,
      cover: res.data?.cover || '',
    }
    checkSongsDownloaded(songs)
  } catch (e) {
    loadError.value = fail('读取账号推荐', e)
  } finally {
    recommendLoading.value = ''
  }
}

// ── 平台扫码登录 / 登出（2026-09-27 从「账号连接」搬来：那页的扫码登录与本页重复）──
// 弹窗见 components/QrLoginModal.vue；状态映射与文案见 services/qrLogin.js。
// 这里只负责「取二维码 → 按 2 秒轮询 → 成功后刷新账号与歌单」这些副作用。
const accountsLoading = ref(false)
const platformBusy = ref('')                // 'netease:qr' / 'netease:disconnect'
const platformError = ref('')               // 卡片区里的一句失败原因
const qr = ref(null)                        // 非空 = 二维码弹窗开着
let qrPollTimer = null
let qrErrLogged = false                     // 每 2 秒一轮，轮询失败只记一次日志，避免刷屏

/** 平台元信息（也带上卡片里那些字段），用于「按 id 找回平台」 */
function platformOf(id) {
  return recommendPlatforms.value.find((p) => p.id === id) || null
}

/**
 * 刷新连接状态：账号列表 + 各平台歌单一起重来。
 * 「刷新状态」按钮与登录成功后都走它 —— 以前这两件事分散在「账号连接」页，
 * 发现页只读，登录完还得手动切页才看到歌单。
 */
async function refreshAccounts() {
  accountsLoading.value = true
  platformError.value = ''
  try {
    await loadAccounts()
    await loadRecommendPlaylists()
  } finally {
    accountsLoading.value = false
  }
}

/** 扫码登录第一步：取二维码 → （需要时）前端渲染 → 开轮询 */
async function startPlatformLogin(p) {
  platformBusy.value = p.id + ':qr'
  platformError.value = ''
  try {
    const res = p.id === 'netease' ? await AccountAPI.neteaseQR() : await AccountAPI.qqQR()
    const data = res?.data
    if (!data?.key) throw new Error(res?.message || '未取得二维码')
    // 网易云只给二维码「内容」，要前端渲染成图；QQ 直接给图片 data URL
    let image = data.image || ''
    if (!image && data.url) image = await QRCode.toDataURL(data.url, { width: 400, margin: 1 })
    qr.value = qrSessionView({
      platformId: p.id, platformName: p.name, appName: p.appName, key: data.key, image,
    })
    startQrPolling()
  } catch (e) {
    qr.value = null
    platformError.value = fail('获取登录二维码', e)
  } finally {
    platformBusy.value = ''
  }
}

/** 两秒一轮问后端「扫了吗」；成功或过期就停，成功后自动刷新账号与歌单 */
function startQrPolling() {
  stopQrPolling()
  qrErrLogged = false
  qrPollTimer = setInterval(async () => {
    if (!qr.value) return stopQrPolling()
    try {
      const res = qr.value.platformId === 'netease'
        ? await AccountAPI.neteaseQRCheck(qr.value.key)
        : await AccountAPI.qqQRCheck(qr.value.key)
      if (!res?.data) return
      const view = qrCheckView(qr.value.platformId, res.data)
      Object.assign(qr.value, view)
      if (!view.done) return
      stopQrPolling()
      if (shouldCloseAfterLogin(view)) {
        // 存盘成功才自动关 —— 「授权成功但保存失败」得让用户看清那句话
        setTimeout(closeQr, LOGIN_SUCCESS_CLOSE_MS)
        loadAccounts().then(loadRecommendPlaylists)
      }
    } catch (e) {
      if (!qrErrLogged) {
        qrErrLogged = true
        logError('扫码轮询', '轮询登录状态失败', describeError(e))
      }
    }
  }, QR_POLL_MS)
}

function stopQrPolling() {
  if (qrPollTimer) { clearInterval(qrPollTimer); qrPollTimer = null }
}

function closeQr() {
  stopQrPolling()
  qr.value = null
}

/** 二维码过期后换一张（平台、二维码窗都重来一遍） */
function restartQr() {
  const p = platformOf(qr.value?.platformId)
  closeQr()
  if (p) startPlatformLogin(p)
}

/** 登出 = 断开本机凭据（下次要重新扫码）。歌单与账号状态就地清掉，不留幽灵数据 */
async function disconnectPlatform(p) {
  if (!confirm(disconnectConfirmText(p.name))) return
  platformBusy.value = p.id + ':disconnect'
  platformError.value = ''
  try {
    await AccountAPI.disconnect(p.id)
    accounts.value = accounts.value.filter((a) => a.provider !== p.id)
    recommendPlaylists.value = { ...recommendPlaylists.value, [p.id]: [] }
    await loadAccounts()
  } catch (e) {
    platformError.value = fail('断开账号', e)
  } finally {
    platformBusy.value = ''
  }
}

// 状态控制
const currentMode = ref('discover') // 'discover' | 'chartDetail' | 'playlistDetail' | 'search'
/**
 * 加载榜单 / 歌单 / 分享链接 / 推荐失败时的一句原因（显示在发现页，替代原来的 alert）。
 * 声明放在这里而不是文件末尾 —— 多个加载函数都要写它，位置靠前避免 TDZ 风险。
 */
const loadError = ref('')
// 下列选择项都做本地记忆：重新打开应用后仍停留在上次选的站点/分类
const discoverTab = ref(loadPref('discover_tab', 'charts'))   // 'charts' | 'playlists' | 'manage'
const selectedCategory = ref(loadPref('playlist_category', '全部'))
const query = ref('')

// 底部播放栏的搜索入口：它不是本组件的 UI，词从 App.vue 传进来，值一变就跑一次搜索。
// 用 nonce 而不是 watch(barQuery)：同一个词连搜两次（比如清空后再搜）也要能触发。
watch(
  () => props.barSearchNonce,
  (n) => {
    if (!n) return
    query.value = props.barQuery || ''
    handleSearch()
  },
)
const selectedPlatform = ref(loadPref('search_platform', 'all'))

// ── 榜单注入：勾中的榜单会作为在线歌单出现在**飞牛音乐 App** 里 ──
//
// 状态与写回逻辑在 `services/chartInject.js`（模块级单例）—— 因为这个开关不止这里
// 在用：「榜单管理」页要勾选、卡片上要显示「已注入」。各存一份副本就会出现
// 「这边改了、那边还显示旧的」。这里只把它包成模板认识的几个名字。
const {
  ids: chartInjectIds,
  enabled: chartInjectEnabled,
  uiInject: uiInjectEnabled,
  busy: chartInjectBusy,
  error: chartInjectError,
} = chartInject

/** 榜单卡片 → 配置里的键（`<平台>_<榜单 id>`，与虚拟歌单 guid 的后缀同形） */
function chartInjectKeyOf(chart) {
  return chartInjectKey(chart, selectedChartPlatform.value)
}

function isChartInjected(chart) {
  return chartInjectIds.value.includes(chartInjectKeyOf(chart))
}

/** 卡片右上角那个「+ 注入 / ★ 已注入」 */
function toggleChartInject(chart) {
  toggleChartItem(chart, selectedChartPlatform.value)
}

function clearChartInjects() {
  replaceInjectIds([])
}

// 总开关：关掉**不丢勾选**（后端两个键分开存），所以它和「全部取消」是两个动作。
function toggleChartInjectEnabled() {
  setInjectEnabled(!chartInjectEnabled.value)
}

/**
 * 页面注入开关：关掉后转发层**根本不认领**飞牛自带页面的文档，官方页面逐字节原样
 * —— 这是「注入出问题」时的一键退路。生效时机是**飞牛页面下次加载**（改完刷新飞牛页面）。
 * 界面上这个开关现在住在「榜单管理」页。
 */
function toggleUIInject() {
  setUIInjectEnabled(!uiInjectEnabled.value)
}

/** 读一次服务端配置（模块内部只会真读一次，失败后可在榜单管理页重试） */
function loadChartInject() {
  return loadInjectConfig()
}

// 数据列表
const toplists = ref([])
const playlists = ref([])
const searchResults = ref([])
const activeSongList = ref([])
const currentDetailMeta = ref(null)
/**
 * 当前打开的歌单/榜单（`playlistDetail` 与 `chartDetail` 模式有值）：{ id, source, link, name }。
 *
 * 订阅功能靠它拿到「这是哪个歌单」—— `id` + `source` 归一成 `wy:19723756` 这种稳定 key。
 * 三个入口会设它：精选歌单（`openPlaylistDetail`）、粘贴链接（`openPlaylistByLink`）、
 * **榜单详情（`openChartDetail`）**。
 * ⚠️ **搜索结果不设** —— 搜索结果没有稳定身份（同一关键词每次结果都可能不同），
 * 订阅它没有意义，所以那里仍然只有「一键监控」。
 */
const currentPlaylistRef = ref(null)

// 状态标记
const isLoadingCharts = ref(false)
const isLoadingPlaylists = ref(false)
const isLoadingDetail = ref(false)
const isSearching = ref(false)

// 热门搜索词已按用户要求移除（发现页不再放关键词快捷入口）
// 歌单分类由后端提供（各平台能力不同，没有分类的平台返回空数组 → 前端隐藏筛选条）。
// 网易云的分类 id 就是分类名，QQ 是数字 id，所以 selectedCategory 存的是 **id**。
const playlistCategories = ref([])
const categoriesLoading = ref(false)

// ── 分类筛选条：常用优先排序 + 只露两行 ──

/**
 * 分类使用次数（id → 次数），存窗口记忆。
 *
 * 为什么要排序：平台给的分类常有二三十个（网易云是固定表），而用户实际只反复点其中几个。
 * 按次数降序后，折叠状态下露出的那两行基本就是他会用的。
 */
const categoryUsage = ref(readCategoryUsage())

function readCategoryUsage() {
  try {
    const raw = loadPref('playlist_cat_usage', '')
    const o = raw ? JSON.parse(raw) : null
    return o && typeof o === 'object' && !Array.isArray(o) ? o : {}
  } catch (_) {
    return {}
  }
}

function bumpCategoryUsage(id) {
  if (!id) return
  const next = { ...categoryUsage.value, [id]: (categoryUsage.value[id] || 0) + 1 }
  categoryUsage.value = next
  try {
    savePref('playlist_cat_usage', JSON.stringify(next))
  } catch (_) {
    // 存不下不影响本次排序
  }
}

/**
 * 常用优先：次数降序；次数相同保持平台原顺序（稳定，没用过的不会被打乱）。
 *
 * ⚠️ **选中的那个永远排第一**：折叠只剩一行后，如果当前生效的分类被挤到第二行，
 * 用户就看不到自己在筛什么了 —— 这一条比「严格按使用次数排」更重要。
 */
const sortedCategories = computed(() => {
  const usage = categoryUsage.value
  const sorted = playlistCategories.value
    .map((c, i) => ({ c, i, n: usage[c.id] || 0 }))
    .sort((a, b) => (b.n - a.n) || (a.i - b.i))
    .map((x) => x.c)

  const idx = sorted.findIndex((c) => c.id === selectedCategory.value)
  if (idx > 0) sorted.unshift(sorted.splice(idx, 1)[0])
  return sorted
})

const catExpanded = ref(false)
const catBoxRef = ref(null)
/** 折叠时的最大高度；由**实测行高**算出，不写死 */
const catBoxMaxH = ref('0px')
/** 被折叠隐藏的分类数量 */
const hiddenCatCount = ref(0)

/**
 * 量一次：算出「一行」的高度，以及有多少分类被收起。
 *
 * 为什么不写死高度或数量：chip 宽度随文字长度变，一行能放几个取决于容器宽度与字号 ——
 * 写死会在窄屏或改字号后露馅（多切一行 / 少切一行）。
 */
function measureCategories() {
  const box = catBoxRef.value
  if (!box) return
  const chips = Array.from(box.children)
  if (!chips.length) {
    hiddenCatCount.value = 0
    return
  }
  const rowH = Math.round(chips[0].getBoundingClientRect().height)
  // 折叠成**一行**：第一行 offsetTop 为 0，第二行起都 > 0（+1 容忍亚像素取整）
  hiddenCatCount.value = chips.filter((c) => c.offsetTop > 1).length
  catBoxMaxH.value = `${rowH}px`
}

watch(sortedCategories, () => nextTick(measureCategories))
watch(discoverTab, () => nextTick(measureCategories))

/** resize 监听句柄（挂载时装、卸载时摘） */
let catResizeHandler = null

async function loadPlaylistCategories(source = selectedChartPlatform.value) {
  categoriesLoading.value = true
  try {
    const res = await ChartsAPI.playlistCategories(source)
    const list = res?.code === 200 ? (res.data?.categories || []) : []
    playlistCategories.value = list
    // 当前选中的分类不在新平台里就回到第一个（通常是「全部」）
    if (list.length && !list.some(c => c.id === selectedCategory.value)) {
      selectedCategory.value = list[0].id
    }
    if (!list.length) {
      playlistCategories.value = []
    }
  } catch (_) {
    // 拉不到分类不阻塞页面，只是不显示筛选条
    playlistCategories.value = []
  } finally {
    categoriesLoading.value = false
  }
}

const platformNames = {
  wy: 'wy',
  kg: 'kg',
  tx: 'tx',
  kw: 'kw',
}

const platformIcons = {
  all: '🌐',
  wy: '🔴',
  tx: '🟢',
  kg: '🔵',
  kw: '🟡',
}

const defaultPlatforms = [
  { id: 'kw', name: 'kw', icon: '🟡' },
  { id: 'kg', name: 'kg', icon: '🔵' },
  { id: 'tx', name: 'tx', icon: '🟢' },
  { id: 'wy', name: 'wy', icon: '🔴' },
]

/**
 * 外挂平台（musicdl sidecar，v2.1.83）的显示元数据。
 *
 * 这几个平台**不是** LX 脚本声明的（LX 那套是浏览器里跑的脚本），而是后端
 * 自己接的外挂进程 —— 所以它们的按钮不能只靠 `unionPlatforms` 推出来，
 * 要看配置里 `musicdl_enabled` + `musicdl_sources`。
 *
 * ⚠️ 与后端 `pkg/config` 的 `MusicDLDefaultSources`、sidecar 的 `core.SOURCES`
 * 三处必须对得上（短码是跨进程的键）。
 */
const MUSICDL_PLATFORM_META = {
  mg: { name: '咪咕', icon: '🟠' },
  bq: { name: '千千', icon: '🔷' },
  bi: { name: 'B站', icon: '📺' },
  kg: { name: 'kg', icon: '🔵' },
  kw: { name: 'kw', icon: '🟡' },
}

/** 配置里启用的外挂平台（空数组 = 外挂音源没开） */
const musicdlPlatforms = ref([])

/** 启用池 id（有序）。老路径只给了单个 activeSource 时降级成单元素池。 */
const poolIds = computed(() => (
  Array.isArray(props.activeSourceIds) && props.activeSourceIds.length
    ? props.activeSourceIds
    : (props.activeSource?.id ? [props.activeSource.id] : [])
))

/** 给定平台 → 池里第一个支持它的源（取链用；都不支持则回退池首） */
function sourceIdFor(platform) {
  return lxRuntime.pickSourceForPlatform(poolIds.value, platform)
}

const hasActiveSource = computed(() => {
  if (poolIds.value.length) return true
  if (props.activeSource?.id) return true
  try {
    const cached = localStorage.getItem('fn_active_source_meta')
    if (cached && JSON.parse(cached)?.id) return true
  } catch (_) {}
  return false
})

// 官方协议（后端 pkg/search + pkg/charts）**真正支持**的平台。
//
// ⚠️ 必须按这个白名单过滤，而不是「脚本声明了啥就显示啥」：
// LX 脚本可以声明任意 platform 键，声明之外的（例如某个脚本里写了 'git'）
// 显示成平台按钮后，一点就是后端 400 `不支持的平台 "git"` ——
// 用户看到的是「不支持的源」，完全不知道问题出在脚本上。
const OFFICIAL_PLATFORMS = ['wy', 'tx', 'kg', 'kw']

/**
 * 发现页「榜单 / 精选歌单」只展示这两个平台。
 *
 * 后端其实四个平台都支持（pkg/charts），但酷狗/酷我的歌单能力较弱
 * （没有分类筛选、榜单结构也简单），用户要求界面上不再暴露 ——
 * 少而准比多而杂好。**搜索平台列表不受影响**（那个走 LX 脚本，仍是全部平台）。
 */
const CHART_PLATFORMS = ['wy', 'tx']

/** 榜单 / 精选歌单用的平台列表（只含上面两个） */
const availablePlatforms = computed(() =>
  CHART_PLATFORMS.map(id => ({
    id,
    name: platformNames[id] || id,
    icon: platformIcons[id] || '🎵',
  })),
)

// 默认用网易云（榜单/歌单最全）。注意旧版本可能持久化成 kw/kg，
// 那种值已经不在列表里了，下面 onMounted 会纠正。
const selectedChartPlatform = ref(loadPref('chart_platform', 'wy'))

const platforms = computed(() => {
  const list = [{ id: 'all', name: '全网聚合', icon: '🌐' }]
  if (poolIds.value.length) {
    // 池内**并集**：任一启用的源支持该平台，就该能搜（多选激活的语义）
    const supported = lxRuntime.unionPlatforms(poolIds.value)
    const filtered = supported.filter(id => OFFICIAL_PLATFORMS.includes(id))
    if (filtered.length > 0) {
      filtered.forEach(id => {
        list.push({
          id,
          name: platformNames[id] || id,
          icon: platformIcons[id] || '🎵'
        })
      })
      return list
    }
  }
  defaultPlatforms.forEach(p => list.push(p))
  return list
})

/**
 * 最终展示的平台按钮 = 上面那套 ∪ 配置里启用的外挂平台。
 *
 * 单独一个 computed 而不是塞进 `platforms`：那个函数里有三条 return 分支
 * （LX 池有平台 / 池里没有认得的 / 没池），在每条分支里都记得追加一次迟早会漏。
 */
const platformsWithMusicDL = computed(() => {
  const base = platforms.value
  if (!musicdlPlatforms.value.length) return base
  const has = new Set(base.map((p) => p.id))
  const extra = musicdlPlatforms.value
    .filter((id) => !has.has(id))
    .map((id) => ({
      id,
      name: MUSICDL_PLATFORM_META[id]?.name || id,
      icon: MUSICDL_PLATFORM_META[id]?.icon || '🎵',
      musicdl: true, // 模板据此加一个「外挂」提示
    }))
  return extra.length ? [...base, ...extra] : base
})

/** 读一次配置，知道外挂音源开着没、开了哪几个平台。 */
async function loadMusicDLPlatforms() {
  try {
    const cfg = await ConfigAPI.get()
    const c = cfg?.config || cfg?.data || cfg || {}
    if (!c.musicdl_enabled) {
      musicdlPlatforms.value = []
      return
    }
    musicdlPlatforms.value = Array.isArray(c.musicdl_sources) && c.musicdl_sources.length
      ? c.musicdl_sources
      : ['mg'] // 后端空名单 = 默认（只剩咪咕：千千/B站实测不可用）
  } catch (_) {
    musicdlPlatforms.value = [] // 读不到就当没开（宁可少几个按钮，不要给点不通的）
  }
}

async function switchChartPlatform(pid) {
  selectedChartPlatform.value = pid
  savePref('chart_platform', pid)
  toplists.value = []
  playlists.value = []
  if (discoverTab.value === 'charts') {
    loadToplists(pid)
    // 分类随平台变，提前拉好，切到歌单页时筛选条就是对的
    loadPlaylistCategories(pid)
  } else {
    // 必须先等分类回来：它会纠正 selectedCategory（换平台后旧分类可能不存在），
    // 否则会拿着旧平台的分类去请求新平台。
    await loadPlaylistCategories(pid)
    loadPlaylists(selectedCategory.value, pid)
  }
}

async function switchDiscoverTab(tab) {
  discoverTab.value = tab
  savePref('discover_tab', tab)
  if (tab === 'charts') {
    loadToplists(selectedChartPlatform.value)
    loadChartInject()
  } else if (tab === 'playlists') {
    await loadPlaylistCategories(selectedChartPlatform.value)
    loadPlaylists(selectedCategory.value, selectedChartPlatform.value)
  }
  // tab === 'manage'：榜单管理页自己读配置、自己拉两个平台的榜单列表，这里不用管
}

watch(() => poolIds.value.join(','), (key) => {
  if (!key) return
  const list = lxRuntime.unionPlatforms(poolIds.value).filter(id => id !== 'mg' && id !== 'local')
  // ⚠️ 榜单平台**不再**跟着音源走：它是官方协议的能力（固定 wy/tx），
  // 与 LX 脚本声明哪些平台无关。这里只做「持久化的值已不在列表里就回退」。
  if (!CHART_PLATFORMS.includes(selectedChartPlatform.value)) {
    selectedChartPlatform.value = CHART_PLATFORMS[0]
    savePref('chart_platform', CHART_PLATFORMS[0])
  }
  // 搜索站点仍要跟着音源校验：换音源后若不再支持上次选的站点，回退到「全网聚合」
  if (list.length > 0 && selectedPlatform.value !== 'all' && !list.includes(selectedPlatform.value)) {
    selectedPlatform.value = 'all'
    savePref('search_platform', 'all')
  }
  if (discoverTab.value === 'charts') {
    if (toplists.value.length === 0) {
      loadToplists(selectedChartPlatform.value)
    }
    // 直接落在榜单 tab（刷新 / 换端）时也要把注入状态读回来
    loadChartInject()
  } else {
    if (playlists.value.length === 0) {
      loadPlaylists(selectedCategory.value, selectedChartPlatform.value)
    }
  }
}, { immediate: true })

// 播放模式控制
const playModeIcon = computed(() => {
  if (props.playMode === 'random') return Shuffle
  if (props.playMode === 'loop') return Repeat1
  return Repeat
})

const playModeLabel = computed(() => {
  if (props.playMode === 'random') return '随机播放'
  if (props.playMode === 'loop') return '单曲循环'
  return '顺序播放'
})

function cyclePlayMode() {
  const modes = ['sequence', 'random', 'loop']
  const next = modes[(modes.indexOf(props.playMode) + 1) % modes.length]
  emit('update:play-mode', next)
}

function formatDuration(sec) {
  if (!sec) return '—'
  const m = Math.floor(sec / 60).toString().padStart(2, '0')
  const s = Math.floor(sec % 60).toString().padStart(2, '0')
  return `${m}:${s}`
}

function formatPlayCount(num) {
  if (!num) return '0'
  if (num > 100000000) return (num / 100000000).toFixed(1) + '亿'
  if (num > 10000) return (num / 10000).toFixed(1) + '万'
  return num.toString()
}

// ── 榜单与歌单加载 ──
// 组件在切换导航时会被卸载重建，这里用模块级缓存避免每次回到「发现音乐」都重新请求。
async function loadToplists(platform = selectedChartPlatform.value) {
  const cacheKey = `toplists:${platform}`
  const cached = memGet(cacheKey)
  if (cached) {
    toplists.value = cached
    return
  }

  isLoadingCharts.value = true
  toplists.value = []
  try {
    const res = await ChartsAPI.toplists(platform)
    if (res.code === 200) {
      toplists.value = res.data || []
      if (toplists.value.length) memSet(cacheKey, toplists.value)
    }
  } catch (e) {
    console.warn('Failed to load toplists:', e)
    toplists.value = []
  } finally {
    isLoadingCharts.value = false
  }
}

async function loadPlaylists(cat = selectedCategory.value, platform = selectedChartPlatform.value) {
  const cacheKey = `playlists:${platform}:${cat}`
  const cached = memGet(cacheKey)
  if (cached) {
    playlists.value = cached
    return
  }

  isLoadingPlaylists.value = true
  playlists.value = []
  try {
    const res = await ChartsAPI.playlists(cat, 1, 24, platform)
    if (res.code === 200) {
      playlists.value = res.data || []
      if (playlists.value.length) memSet(cacheKey, playlists.value)
    }
  } catch (e) {
    console.warn('Failed to load playlists:', e)
    playlists.value = []
  } finally {
    isLoadingPlaylists.value = false
  }
}

function selectPlaylistCategory(cat) {
  selectedCategory.value = cat
  savePref('playlist_category', cat)
  // 记一次使用 —— 「常用优先」排序靠它（见 sortedCategories）
  bumpCategoryUsage(cat)
  loadPlaylists(cat, selectedChartPlatform.value)
}

/**
 * 榜单管理页点一行右侧的 ▶：切到「排行榜单」并把这张榜单的详情打开。
 * 平台顺手一起切 —— 回到浏览页时，看到的就该是他刚点的那张榜单所属的平台。
 */
function openChartPreview(item) {
  if (!item?.id) return
  const source = item.source || selectedChartPlatform.value
  if (CHART_PLATFORMS.includes(source) && source !== selectedChartPlatform.value) {
    switchChartPlatform(source)
  }
  discoverTab.value = 'charts'
  savePref('discover_tab', 'charts')
  openChartDetail({ ...item, source })
}

async function openChartDetail(chart) {
  currentMode.value = 'chartDetail'
  currentDetailMeta.value = {
    title: chart.name,
    desc: chart.update_frequency ? `更新频率：${chart.update_frequency}` : '官方排行榜单',
    cover: chart.cover,
  }
  // 榜单也能订阅。平台的榜单 id **本身就是该平台的歌单 id**（网易云飙升榜 19723756、
  // 热歌榜 3778678 —— 与用户手动粘贴的 `playlist?id=` 是同一串数字），
  // 所以榜单与精选歌单走**完全相同**的订阅路径（`target.playlists[{id, source}]`），
  // 后端 `fetchPlaylistRef` 早已支持并有测试。
  //
  // ⚠️ 曾经这里不设 `currentPlaylistRef`，榜单详情只显示旧的「一键监控」——
  // 而那个按钮默认「当前列表」模式、`target: {}` **不绑定任何来源**，
  // 建出来的监控**永远追不到新歌**。用户看到的是「订阅了但新歌不会来」。
  currentPlaylistRef.value = {
    id: chart.id || '',
    source: chart.source || selectedChartPlatform.value || '',
    link: '',
    name: chart.name || '',
  }
  isLoadingDetail.value = true
  activeSongList.value = []
  loadError.value = ''
  try {
    const res = await ChartsAPI.chartDetail(chart.id, chart.source || selectedChartPlatform.value, chart.name)
    const bad = apiError(res, '加载失败')
    if (bad) {
      currentMode.value = 'discover'
      loadError.value = bad
      logError('加载榜单曲目', bad, `code=${res?.code} message=${res?.message} chart=${chart.id}`)
      return
    }
    activeSongList.value = res.data?.songs || []
    checkSongsDownloaded(activeSongList.value)
    await refreshSubscription()
  } catch (e) {
    currentMode.value = 'discover'
    loadError.value = fail('加载榜单曲目', e)
  } finally {
    isLoadingDetail.value = false
  }
}

async function openPlaylistDetail(pl) {
  currentMode.value = 'playlistDetail'
  currentDetailMeta.value = {
    title: pl.title,
    desc: pl.creator ? `创建者：${pl.creator}` : (pl.description || '精选歌单'),
    cover: pl.cover,
  }
  // 订阅需要知道「这是哪个歌单」：精选歌单只有 id，没有链接，用 id + 平台归一化
  currentPlaylistRef.value = {
    id: pl.id || '',
    source: pl.source || selectedChartPlatform.value || '',
    link: '',
    name: pl.title || '',
  }
  isLoadingDetail.value = true
  activeSongList.value = []
  loadError.value = ''
  try {
    const res = await ChartsAPI.playlistDetail(pl.id, pl.source || selectedChartPlatform.value, pl.title)
    const bad = apiError(res, '加载失败')
    if (bad) {
      currentMode.value = 'discover'
      loadError.value = bad
      logError('加载歌单曲目', bad, `code=${res?.code} message=${res?.message} playlist=${pl.id}`)
      return
    }
    activeSongList.value = res.data?.songs || []
    checkSongsDownloaded(activeSongList.value)
    await refreshSubscription()
  } catch (e) {
    currentMode.value = 'discover'
    loadError.value = fail('加载歌单曲目', e)
  } finally {
    isLoadingDetail.value = false
  }
}

/**
 * 粘贴歌单链接 → 直接打开歌单详情（可整单播放 / 下载）。
 *
 * 用户从各平台 App「复制链接」拿到的是分享链接，直接当关键词搜是搜不到的，
 * 所以这里先判断像不像链接，像就走解析，并给出明确失败原因。
 */
async function openPlaylistByLink(link) {
  currentMode.value = 'playlistDetail'
  currentDetailMeta.value = { title: '正在解析歌单链接…', desc: link, cover: '' }
  currentPlaylistRef.value = { id: '', source: '', link, name: '' }
  isLoadingDetail.value = true
  activeSongList.value = []
  loadError.value = ''
  try {
    const res = await ChartsAPI.playlistDetailByUrl(link)
    const bad = apiError(res, '解析失败')
    if (bad) {
      currentMode.value = 'discover'
      loadError.value = bad
      logError('解析歌单链接', bad, `code=${res?.code} message=${res?.message} link=${link}`)
      return
    }
    const d = res.data || {}
    activeSongList.value = d.songs || []
    currentDetailMeta.value = {
      title: d.name || d.title || '歌单',
      desc: `来自分享链接 · ${activeSongList.value.length} 首`,
      cover: d.cover || '',
    }
    currentPlaylistRef.value = {
      id: '',
      source: activeSongList.value[0]?.source || '',
      link,
      name: currentDetailMeta.value.title,
    }
    query.value = ''
    checkSongsDownloaded(activeSongList.value)
    await refreshSubscription()
    if (!activeSongList.value.length) {
      // 「解析成功但一首都没有」多半不是出错，而是歌单本身为空或需要登录 —— 说清楚即可
      loadError.value = '这个歌单没有解析到曲目 —— 可能是空歌单，或它需要登录才能查看。'
      logWarn('解析歌单链接', '解析到 0 首曲目', link)
    }
  } catch (e) {
    currentMode.value = 'discover'
    loadError.value = fail('解析歌单链接', e)
  } finally {
    isLoadingDetail.value = false
  }
}

// ── 搜索处理 ──
// 搜索走**官方协议**（后端 pkg/search），不依赖任何音源脚本，因此这里不设「必须先导入音源」的门槛。
// 只有播放时才需要 LX 协议取直链（见 App.vue 的 playSong）。
async function handleSearch() {
  const q = query.value.trim()
  if (!q) return

  // 粘的是链接 → 按歌单链接解析，不要当关键词去搜
  if (/^https?:\/\//i.test(q)) {
    await openPlaylistByLink(q)
    return
  }

  currentMode.value = 'search'
  currentDetailMeta.value = {
    title: `搜索：“${q}”`,
    desc: '跨平台聚合检索',
    cover: '',
  }
  isSearching.value = true
  activeSongList.value = []
  loadError.value = ''
  try {
    const res = await SearchAPI.search(q, selectedPlatform.value, 1)
    const bad = apiError(res, '搜索失败')
    if (bad) {
      currentMode.value = 'discover'
      loadError.value = bad
      logError('搜索', bad, `code=${res?.code} message=${res?.message} q=${q}`)
      return
    }
    searchResults.value = res.data?.list || []
    activeSongList.value = searchResults.value
    checkSongsDownloaded(activeSongList.value)
  } catch (e) {
    currentMode.value = 'discover'
    loadError.value = fail('搜索', e)
  } finally {
    isSearching.value = false
  }
}

function switchPlatform(p) {
  selectedPlatform.value = p
  savePref('search_platform', p)
  if (query.value.trim()) {
    handleSearch()
  }
}

function resetToDiscover() {
  query.value = ''
  currentMode.value = 'discover'
  activeSongList.value = []
  currentDetailMeta.value = null
}

// ── 播放触发 (关键：点击单曲带上完整歌单上下文，自动顺序播放本歌单/榜单所有歌曲) ──
function playSong(song, idx) {
  emit('play', {
    song,
    playlist: [...activeSongList.value],
    index: idx !== undefined ? idx : activeSongList.value.findIndex(s => s.id === song.id)
  })
}

function playAllSongs() {
  if (!activeSongList.value.length) return
  emit('play-all', {
    songs: [...activeSongList.value],
    startIndex: 0
  })
}

// ── 下载与状态同步 ──
const toastMessage = ref('')
let toastTimer = null

function showToast(msg) {
  toastMessage.value = msg
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => {
    toastMessage.value = ''
  }, 2500)
}

function checkSongsDownloaded(songs) {
  downloadManager.checkSongsDownloaded(songs)
}

// 音质选择弹窗状态（在线曲目单曲下载前弹出，让用户选该曲真实可用档位）
const showQualityModal = ref(false)
const qualityTargetSong = ref(null)

function downloadSingleSong(song) {
  if (!props.activeSource?.id && song.source !== 'nas') {
    // 页面上方本来就有「还没有可用的音源 → 导入音源」提示条，这里只需说明这次点击为什么没生效
    showToast('下载需要先导入音源 —— 点上方「导入音源」')
    return
  }
  // NAS 本地曲目不涉及在线音质，直接入队
  if (song.source === 'nas') {
    downloadManager.addSong(song, sourceIdFor(song.source), false)
    showToast(`已将《${song.name}》加入后台下载队列`)
    return
  }
  // 在线曲目：先让用户确认音质（档位来自官方协议的真实查询结果）
  qualityTargetSong.value = song
  showQualityModal.value = true
}

function onQualityConfirm({ quality, remember }) {
  const song = qualityTargetSong.value
  if (!song) return
  if (remember) downloadManager.setAllQuality(quality)
  downloadManager.addSong(song, sourceIdFor(song.source), false, quality)
  const tip = isHighestQuality(quality) ? '' : ` · ${qualityLabel(quality)}`
  showToast(`已将《${song.name}》加入后台下载队列${tip}`)
}

function batchDownloadAll() {
  const list = activeSongList.value
  if (!list.length) return

  if (!hasActiveSource.value && list.some(s => s.source !== 'nas')) {
    showToast('批量下载需要先导入音源 —— 点上方「导入音源」')
    return
  }

  // 批量：每首按自己的平台挑源（池有序 → 命中优先级最高的那个）
  downloadManager.addBatch(list, poolIds.value[0] || '', false, (song) => sourceIdFor(song.source))
  showToast(`已添加 ${list.length} 首歌曲至后台下载队列`)
}

onMounted(async () => {
  // 旧版本可能把平台持久化成 kw/kg，这两个已不在榜单平台列表里 ——
  // 不纠正的话会出现「一个平台都没高亮」，而且请求的还是隐藏平台。
  if (!CHART_PLATFORMS.includes(selectedChartPlatform.value)) {
    selectedChartPlatform.value = CHART_PLATFORMS[0]
    savePref('chart_platform', CHART_PLATFORMS[0])
  }

  // 外挂平台（musicdl）的按钮要看配置才知道该不该出现 —— 不等它，页面照常先渲染
  loadMusicDLPlatforms()
  loadToplists(selectedChartPlatform.value)
  loadPlaylists('全部', selectedChartPlatform.value)
  loadPlaylistCategories(selectedChartPlatform.value)
  // 双平台推荐：先拿连接状态，再拉各平台歌单（未连接时接口会报错，静默忽略）
  loadAccounts().then(loadRecommendPlaylists).finally(() => markLoaded('search'))

  // 分类条的两行折叠是按容器宽度量的 —— 窗口变宽变窄后要重量一次
  catResizeHandler = () => nextTick(measureCategories)
  window.addEventListener('resize', catResizeHandler)
})

onUnmounted(() => {
  if (catResizeHandler) window.removeEventListener('resize', catResizeHandler)
  stopQrPolling()
})

// ── 页面现在**常驻**（SwipePager 不再卸载），所以"离开"改用 onDeactivated ──
// ① 切走时停掉二维码轮询：不然它在后台每 2 秒继续打后端，结果没人看。
onDeactivated(() => stopQrPolling())
// ② 切回来时按 TTL 决定要不要静默刷新（stale-while-revalidate）：
//    先显示缓存（不闪骨架屏），超过 TTL 再在后台拉一遍、原地替换。
onActivated(() => {
  if (!isStale('search')) return
  loadToplists(selectedChartPlatform.value)
  loadPlaylists(selectedCategory.value, selectedChartPlatform.value)
  loadPlaylistCategories(selectedChartPlatform.value)
  loadAccounts().then(loadRecommendPlaylists).finally(() => markLoaded('search'))
})

// ── 订阅更新（歌单详情）──
// 用户视角的「订阅」= 监控（自动下载）+ 推送（同步飞牛）两条任务一起管，
// 这里只做 facade，细节见 services/subscribe.js。
const subscription = ref(null)
const subscribingSub = ref(false)
const togglingSub = ref(false)
const subNotice = ref('')
const subNoticeTone = ref('ok')

const subKey = computed(() => playlistKey(currentPlaylistRef.value || {}))

// 订阅时勾选的两个能力位（下载到本地曲库 / 同步到飞牛歌单）。
// 选择记在服务端偏好里 —— 多数人每次都勾同样的东西，不该每次重选。
const showSubDialog = ref(false)
const subForm = ref({ download: true, fnos: true })
const subFnosReady = ref(true)

const SUB_CAPS_KEY = 'subscribe_caps'

function loadSubCaps() {
  const parts = String(loadPref(SUB_CAPS_KEY, 'download,fnos') || '')
    .split(',')
    .map((s) => s.trim())
  return { download: parts.includes('download'), fnos: parts.includes('fnos') }
}

function saveSubCaps(caps) {
  const parts = []
  if (caps.download) parts.push('download')
  if (caps.fnos) parts.push('fnos')
  savePref(SUB_CAPS_KEY, parts.join(','))
}

/**
 * 打开订阅弹窗：回填上次的选择，并探一次飞牛。
 *
 * ⚠️ 飞牛没连上时「同步飞牛」置灰，同时在说明里写清去哪登录 ——
 * 直接静默去掉用户上次的勾会让人以为界面坏了。
 */
async function openSubDialog() {
  subNotice.value = ''
  const caps = loadSubCaps()
  subForm.value = { download: caps.download, fnos: caps.fnos }
  showSubDialog.value = true
  subFnosReady.value = await fnosReady()
  if (!subFnosReady.value) subForm.value.fnos = false
  // 兜底：两个都没勾上（如上次只勾飞牛、这次飞牛没连上）→ 至少留一项
  if (!subForm.value.download && !subForm.value.fnos) subForm.value.download = true
}

// 离开歌单/榜单详情就清空订阅状态，避免按钮残留上一个歌单的「已订阅」。
// ⚠️ 这里必须把 `chartDetail` 也放行 —— 榜单现在是可订阅的（见 `openChartDetail`），
// 漏掉它会让榜单详情一进去就被清空、按钮闪一下又变回「订阅更新」。
watch(currentMode, (m) => {
  if (m !== 'playlistDetail' && m !== 'chartDetail') {
    currentPlaylistRef.value = null
    subscription.value = null
    subNotice.value = ''
  }
})

/** 查当前歌单订阅了没有（按归一化歌单标识匹配监控与推送任务） */
async function refreshSubscription() {
  subscription.value = null
  if (!subKey.value) return
  try {
    subscription.value = await findSubscription(subKey.value)
  } catch (_) { /* 忽略：按未订阅展示 */ }
}

/**
 * 订阅当前歌单：按勾选建「监控（自动下载）」「推送（同步飞牛）」。
 *
 * 把当前列表作为基线一起上报，省得后端下一轮再抓一次；首轮只登记不下载。
 * 勾选由弹窗决定（`subForm`），并记住上次选择 —— 多数人每次都勾同样的东西。
 */
async function subscribeCurrent() {
  const ref0 = currentPlaylistRef.value
  if (!ref0 || subscribingSub.value) return
  if (!subForm.value.download && !subForm.value.fnos) {
    subNotice.value = '请至少选一项：下载到本地曲库 / 同步到飞牛歌单'
    subNoticeTone.value = 'error'
    return
  }

  subscribingSub.value = true
  subNotice.value = ''
  try {
    const songs = activeSongList.value
      .map((s) => ({
        source: s.source || 'wy',
        id: s.songmid || s.id || '',
        name: s.name || '',
        artist: s.singer || '',
        album: s.album || '',
        duration: s.interval || s.duration || 0,
        cover: s.cover || '',
      }))
      .filter((s) => s.id && s.name)

    const res = await subscribePlaylist({
      link: ref0.link,
      id: ref0.id,
      source: ref0.source,
      name: ref0.name,
      songs,
      wantDownload: subForm.value.download,
      wantFnos: subForm.value.fnos,
    })
    if (!res.ok) {
      subNotice.value = res.error
      subNoticeTone.value = 'error'
      logError('订阅歌单', res.error, `key=${subKey.value}`)
      return
    }
    // 只有真的建成了才记住这次的选择（失败时别把偏好带偏）
    saveSubCaps(subForm.value)
    showSubDialog.value = false
    subNotice.value = res.message
    subNoticeTone.value = 'ok'
    await refreshSubscription()
  } catch (e) {
    subNotice.value = fail('订阅歌单', e)
    subNoticeTone.value = 'error'
  } finally {
    subscribingSub.value = false
  }
}

/** 停止 / 恢复更新：两条任务一起改，已下载的文件与飞牛歌单都保留 */
async function toggleCurrentSubscription() {
  const sub = subscription.value
  if (!sub || togglingSub.value) return

  togglingSub.value = true
  subNotice.value = ''
  try {
    const next = !sub.active
    await setSubscriptionEnabled(sub, next)
    await refreshSubscription()
    subNotice.value = next
      ? '已恢复更新，会继续自动下载新增曲目并同步飞牛歌单'
      : '已停止更新。已下载的文件和飞牛歌单都保留，随时可以恢复'
    subNoticeTone.value = 'ok'
  } catch (e) {
    subNotice.value = fail('更新订阅状态', e)
    subNoticeTone.value = 'error'
  } finally {
    togglingSub.value = false
  }
}

// ── 一键监控 ──
const showMonitorDialog = ref(false)
const creatingMonitor = ref(false)
const monitorMsg = ref('')
const monitorOk = ref(false)
const monitorForm = ref({ name: '', mode: 'current', link: '', interval: 360 })

function openMonitorDialog() {
  monitorMsg.value = ''
  const base = query.value.trim() || activeSongList.value[0]?.name || '新监控'
  monitorForm.value = {
    name: `监控 · ${base}`.slice(0, 40),
    mode: 'current',
    link: '',
    interval: 360,
  }
  showMonitorDialog.value = true
}

async function createMonitorFromList() {
  const list = activeSongList.value
  if (!list.length) return

  creatingMonitor.value = true
  monitorMsg.value = ''
  try {
    // 1) 先建监控
    const target = {}
    if (monitorForm.value.mode === 'playlist' && monitorForm.value.link.trim()) {
      target.playlists = [{ link: monitorForm.value.link.trim() }]
    }
    // 「当前列表」模式不绑定链接，改为注入上报曲目

    const res = await MonitorAPI.create({
      name: monitorForm.value.name,
      kind: 'playlist',
      target,
      quality: 'lossless',
      interval_minutes: monitorForm.value.interval,
      auto_download: true,
      embed: true,
      enabled: true,
    })
    const bad = apiError(res, '创建失败')
    if (bad) {
      monitorOk.value = false
      monitorMsg.value = bad
      logError('建立定时监控', bad, `code=${res?.code} message=${res?.message}`)
      return
    }

    const mid = res.data.id

    // 2) 把当前列表作为基线曲目上报，这样首轮不用等网页端再抓一次
    const songs = list.map(s => ({
      source: s.source || 'kw',
      id: s.songmid || s.id || '',
      name: s.name || '',
      artist: s.singer || '',
      album: s.album || '',
      duration: s.interval || s.duration || 0,
      cover: s.cover || '',
    })).filter(s => s.id && s.name)

    if (songs.length) {
      try {
        await MonitorAPI.discover(mid, { songs })
      } catch (_) { /* 上报失败不阻塞创建，下轮可由网页端补报 */ }
    }

    monitorOk.value = true
    monitorMsg.value = `已订阅，先记下现有 ${songs.length} 首。之后发现新歌会自动下载`
    setTimeout(() => { showMonitorDialog.value = false }, 1800)
  } catch (e) {
    monitorOk.value = false
    monitorMsg.value = fail('建立定时监控', e)
  } finally {
    creatingMonitor.value = false
  }
}

</script>
