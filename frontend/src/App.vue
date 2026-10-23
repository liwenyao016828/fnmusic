<template>
  <!--
    ⚠️ 根容器以前是 `h-screen w-screen`：移动端浏览器地址栏会把 100vh 的一部分占掉，
    底部播放条被顶到可视区之外（露一半或整条看不见）；`w-screen` 还会在 iOS 上横向溢出。
    改成 `.app-viewport`（style.css 里的 100dvh）—— 手机横竖屏、地址栏收起/展开都跟着走。

    2026-10-17 导航框架重做（用户口径：「整体导航栏及内部功能交互逻辑还是不对，
    做一个适配我的功能的 ui 框架」）：
      · 桌面（sm+）＝**左侧栏**：品牌（点开开放接口抽屉）→ 分组导航（听 / 管 / 配置）
        → 底部音源胶囊 + 播放器入口。顶栏导航条整个退役。
      · 手机＝精简顶栏（品牌 + 音源 + 播放器）+ **底部标签栏**（4 个高频视图 + 「更多」）。
        次要入口（整理 / 音源 / 账号 / 系统）收进「更多」**右侧抽屉**——
        用户口径：「说『样式』指的是呈现方式，不是位置；次要入口要做右侧抽屉」。
      · 顶层视图 8 个，注册表在 services/navViews.js（单一数据源）；SwipePager 仍是
        内容宿主，滑动顺序 = 注册表顺序。
      · `--dock-h`（style.css）＝手机底部标签栏总高（桌面 0）。PlayerBar / FeedbackHost /
        各页底部留白全部叠加它避让，不再各写各的魔数。
      · 原「导航密度三档」机制（fitNavDensity）删除：侧栏放得下全名，不再需要实测降档。
  -->
  <div class="app-viewport flex w-full flex-col overflow-hidden bg-[var(--ys-canvas)] font-sans text-[var(--ys-text)] select-none">

    <!-- ── Immersive Player (shown when playing) ── -->
    <ImmersivePlayer
      v-if="showImmersive && currentSong"
      :current-song="currentSong"
      :is-playing="isPlaying"
      :current-time="currentTime"
      :duration="duration"
      :lyric-data="lyricData"
      :trans-data="transData"
      :playlist-count="playlist.length"
      :play-mode="playMode"
      :playlist="playlist"
      :current-index="currentIndex"
      @update:play-mode="val => playMode = val"
      @collapse="showImmersive = false"
      @toggle-play="togglePlay"
      @prev="playPrev"
      @next="playNext"
      @seek="onSeek"
      @volume-change="onVolumeChange"
      @select-track="playFromQueue"
      @clear-playlist="clearPlaylist"
    />

    <!-- ════════════ 顶部一条栏：品牌 +（手机端）当前页面名 + 音源 ════════════
         2026-10-18 用户口径：「页面最顶部，把发现音乐、本地曲库等页面名，移到 logo 和
         音源管理那行去」——所以页面名**只**出现在这一条栏里，各页内容区不再重复写标题
         （PageShell / SearchView 的自带标题都已撤掉）。
         2026-10-23 追加：「去掉桌面端」——桌面端有侧栏高亮指路，这一格的标题已按
         `sm:hidden` 收敛到手机端，详见下面 h1 上方的注释。
         品牌按钮仍是「开放接口」入口（点图标滑出抽屉）。
         ⚠️ `pt-safe` 挂 header、真实内边距挂内层 div —— 两者挂同一元素会互相覆盖。 -->
    <header class="pt-safe z-20 shrink-0 border-b border-[var(--ys-border)] bg-[var(--ys-surface-glass)] backdrop-blur-xl backdrop-saturate-150">
      <div class="flex h-[52px] items-center gap-2 px-3 sm:h-[56px] sm:px-5">
        <button
          type="button"
          class="brand-halo group flex shrink-0 cursor-pointer items-center gap-2.5 rounded-[13px]"
          :class="{ 'brand-halo-open': showApiDrawer }"
          title="开放接口 · 供 AI 与外部程序调用"
          aria-label="开放接口"
          @click="showApiDrawer = true"
        >
          <span class="relative grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-[linear-gradient(180deg,#14A87E_0%,#0B8A66_100%)] text-white shadow-[0_1px_2px_rgba(0,0,0,0.12),inset_0_1px_0_rgba(255,255,255,0.22)] transition-[filter,transform] duration-200 group-hover:brightness-105 group-active:scale-[0.97]">
            <span class="brand-core-glow" aria-hidden="true"></span>
            <Disc class="brand-disc h-[18px] w-[18px]" />
            <!-- 中间圆点（静止，盖住 Disc 的内圈）+ 它外层那个**呼吸的圆圈** -->
            <span class="brand-core" aria-hidden="true"></span>
            <span class="brand-core-ring" aria-hidden="true"></span>
          </span>
          <span class="hidden text-left leading-tight sm:block">
            <b class="block text-[15px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">曲率</b>
            <small class="block text-[10px] font-medium tracking-[0.02em] text-[var(--ys-text-faint)]">AI Music Hub</small>
          </span>
        </button>

        <!--
          当前页面名只在**手机**上出现（用户 2026-10-23：「帮我去掉桌面端，当前用户选择的
          导航页面名字会显示在左上角的 logo 右边」）。

          为什么桌面端可以去：左侧栏的导航项本身已经高亮了当前页（那就是「我在哪」的答案），
          顶部再写一遍是同一件事说两遍；腾出来的位置让音源胶囊更靠左，头部更干净。

          为什么手机端必须留：手机上侧栏是抽屉，关着的时候界面上**没有**任何地方标示
          当前在哪一页，删了就成了「不知道自己在哪」。分隔线跟着一起 `sm:hidden`，
          否则桌面端会在 logo 后面留一根孤零零的竖线。
        -->
        <span class="hidden h-5 w-px shrink-0 bg-[var(--ys-border)] sm:hidden" aria-hidden="true"></span>
        <h1
          class="min-w-0 truncate text-[15px] font-semibold tracking-[-0.01em] text-[var(--ys-text)] sm:hidden"
          :title="currentViewDesc"
        >
          {{ currentViewName }}
        </h1>

        <div class="ml-auto flex shrink-0 items-center gap-1.5">
          <!-- 当前音源状态胶囊：中性底 + 一颗状态点。
               原来是绿底绿字的药丸 —— 满屏绿色是因为这类「装饰性强调」太多，
               强调色应该留给「正在发生的事」，而不是每个常驻控件。 -->
          <button
            @click="navigateTo('sources')"
            class="flex min-w-0 max-w-[92px] items-center gap-1.5 rounded-full border border-[var(--ys-border)] bg-white/70 px-2 py-1.5 text-[var(--ys-text-muted)] transition-colors hover:bg-white hover:text-[var(--ys-text)] sm:max-w-none sm:px-3"
            :title="`当前音源：${activeSourceMeta?.name || '未设置'} —— 点开管理音源`"
            :aria-label="`当前音源：${activeSourceMeta?.name || '未设置'}，打开音源管理`"
          >
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="activeSourceMeta ? 'bg-emerald-500' : 'bg-amber-400'"></span>
            <span class="truncate text-[11px] font-medium sm:hidden">{{ shortSourceName }}</span>
            <span class="hidden truncate text-[11px] font-medium sm:inline">{{ activeSourceMeta?.name || '音源' }}</span>
          </button>
          <!-- 播放器入口在桌面侧栏底部，手机没有侧栏，所以只有手机在这儿放一个图标入口 -->
          <button
            v-if="currentSong"
            @click="showImmersive = true"
            class="shrink-0 rounded-[10px] border border-[var(--ys-border)] bg-white/70 p-2 text-[var(--ys-text-muted)] transition-colors hover:bg-white hover:text-[var(--ys-text)] sm:hidden"
            title="打开播放器"
            aria-label="打开播放器"
          >
            <Disc class="h-4 w-4" />
          </button>
        </div>
      </div>
    </header>

    <!-- 顶栏之下 = 一行两列：桌面左侧栏 + 内容列。
         内层用 `<div class="flex min-h-0 flex-1">` 包住这两个兄弟节点（原来是根容器的直接子节点）；
         为了不制造 250 行纯缩进 diff，里面的缩进没有跟着加一级。 -->
    <div class="flex min-h-0 flex-1">

    <!-- ════════════ 桌面左侧栏（sm+ 才出现；手机用底部标签栏） ════════════ -->
    <aside class="z-20 hidden w-[216px] shrink-0 flex-col border-r border-[var(--ys-border)] bg-[#FBFBFD] sm:flex">
      <!--
        品牌 + 开放接口入口都搬到**顶部一条栏**了（2026-10-18 用户口径：「把页面名移到
        logo 和音源那行去」）——侧栏不再有品牌行，从导航直接开始，省下 68px。
      -->

      <!-- 分组导航：听（发现/曲库）· 管（整理/订阅/下载）· 配置（音源/账号/系统）。
           分组只影响呈现（navViews.js 的 group 字段），id 与深链不变。
           ⚠️ 分组标题不再用「10px 加粗 + 0.14em 字距」的英文小标题写法：
           中文这么排会碎成一条虚线，改成 11px 常规灰即可。 -->
      <nav class="no-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-2 py-4" aria-label="主导航">
        <div v-for="g in navGroups" :key="g.id">
          <p class="mb-1 px-2.5 text-[11px] font-medium text-[var(--ys-text-faint)]">{{ g.name }}</p>
          <div class="space-y-0.5">
            <button
              v-for="item in g.items"
              :key="item.id"
              @click="goView(item.id)"
              :title="item.name"
              :aria-label="item.name"
              :aria-current="currentView === item.id ? 'page' : undefined"
              :class="[
                'ys-press flex w-full items-center gap-2.5 rounded-[9px] px-2.5 py-[7px] text-[13px] font-medium',
                currentView === item.id
                  ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
                  : 'text-[var(--ys-text-muted)] hover:bg-black/[0.04] hover:text-[var(--ys-text)]'
              ]"
            >
              <component
                :is="item.icon"
                class="h-4 w-4 shrink-0"
                :class="currentView === item.id ? 'text-[var(--ys-accent)]' : 'text-[var(--ys-text-faint)]'"
                :stroke-width="1.8"
              />
              <span class="truncate">{{ item.name }}</span>
              <!-- 下载进行中的数量角标（跟着任务走，队列空了自动消失） -->
              <span
                v-if="item.id === 'download' && downloadManager.activeCount.value > 0"
                class="ml-auto grid shrink-0 place-items-center rounded-full bg-emerald-500 px-1.5 py-px text-[10px] font-semibold text-white"
              >{{ downloadManager.activeCount.value }}</span>
              <!-- 订阅总数角标（静态计数，灰色不抢眼） -->
              <span
                v-else-if="item.id === 'push' && pushEngine.pushTotalCount.value > 0"
                class="ml-auto shrink-0 rounded-full bg-black/[0.05] px-1.5 py-px text-[10px] font-medium text-[var(--ys-text-muted)]"
              >{{ pushEngine.pushTotalCount.value }}</span>
            </button>
          </div>
        </div>
      </nav>

      <!-- 任务状态：只列「需要你处理」的项；都为 0 时给一句明确结论，而不是摆一排 0。
           跳转走 resolveRoute 的别名表（tab: 'complete'→整理页、'download'→下载队列…）。 -->
      <div class="shrink-0 border-t border-[var(--ys-border)] px-2 py-3" v-if="statusItems.length">
        <p class="mb-1 px-2.5 text-[11px] font-medium text-[var(--ys-text-faint)]">任务状态</p>
        <div class="space-y-0.5">
          <button
            v-for="s in statusItems" :key="s.id"
            @click="s.go()"
            class="ys-press flex w-full items-center gap-2 rounded-[9px] px-2.5 py-1.5 text-left text-[12px] text-[var(--ys-text-muted)] hover:bg-black/[0.04] hover:text-[var(--ys-text)]"
            :title="s.label"
          >
            <component :is="s.icon" class="h-3.5 w-3.5 shrink-0" :class="s.iconClass" :stroke-width="1.8" />
            <span class="min-w-0 flex-1 truncate">{{ s.label }}</span>
            <span class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium" :class="s.badgeClass">{{ s.count }}</span>
          </button>
        </div>
      </div>

      <!-- 侧栏底部：只剩播放器入口（音源胶囊已搬到顶部一条栏，同一个按钮不出现两次） -->
      <div class="shrink-0 space-y-2 border-t border-[var(--ys-border)] p-3">
        <button
          v-if="currentSong"
          @click="showImmersive = true"
          class="ys-press flex w-full items-center justify-center gap-1.5 rounded-[10px] border border-[var(--ys-border)] bg-white px-3 py-2 text-[12px] font-medium text-[var(--ys-text)] shadow-sm hover:bg-gray-50"
          title="打开播放器"
        >
          <Disc class="h-3.5 w-3.5 text-[var(--ys-accent)]" />
          <span class="truncate">打开播放器</span>
        </button>
      </div>
    </aside>

    <!-- ════════════ 内容列（主内容；品牌 + 页面名 + 音源已提到最上面那条栏） ════════════ -->
    <div class="flex min-w-0 flex-1 flex-col">

      <!-- Main Content -->
      <!-- 主内容区：左右滑动切页（跟手位移 / 到头阻尼 / 速度判定，见 components/SwipePager.vue） -->
      <SwipePager :order="swipeOrderList" :current="currentView" @update:current="v => goView(v)">
        <!--
          发现音乐（发现 / 榜单）—— 2026-10-15 重构把「榜单管理」从顶层页降为**页内第二段**。
          理由：它和「发现」都是浏览内容，平级排在导航里只会让导航越来越长（用户反馈过「挤」）；
          而整页替换又会丢掉另一段的滚动位置与已加载数据（切回来要重新等）。
          所以两段都常驻、v-show 切换 —— 与 SwipePager 对页面的策略一致。
        -->
        <!--
          发现音乐（发现 / 榜单）—— 两段都是**浏览**，所以由同一个组件渲染：
          榜单段的卡片网格与发现段的排行榜单网格本来就是同一份数据、同一个卡片 UI，
          各自实现一遍就是「同一件事写两遍」（用户 2026-10-18：「逻辑不要重复」）。
          SearchView 收 `segment` prop 自己决定渲染哪一段；榜榜单的注入设置是
          它内部一个可折叠面板，不再是独立的一页。
        -->
        <template #view-search>
          <div class="flex h-full min-h-0 flex-col">
            <div class="shrink-0 border-b border-[var(--ys-border)] bg-[var(--ys-surface-glass)] px-3 py-2 backdrop-blur-xl">
              <SegmentedControl
                :segments="decoratedSegments('search')"
                :model-value="segmentOf('search')"
                @update:model-value="v => setSegment('search', v)"
              />
            </div>
            <div class="grid min-h-0 flex-1 grid-cols-1">
              <SearchView
                v-if="segMounted('search', 'discover')"
                :segment="segmentOf('search')"
                :active-source="activeSourceMeta"
                :active-source-ids="activeSourceIds"
                :play-mode="playMode"
                :current-song="currentSong"
                :is-playing="isPlaying"
                @update:play-mode="val => playMode = val"
                @play="playSong"
                @play-all="playAllSongs"
                @navigate="view => goView(view)"
                @prev="playPrev"
                @next="playNext"
                @toggle-play="togglePlay"
                :bar-query="barSearchQuery"
                :bar-search-nonce="barSearchNonce"
              />
            </div>
          </div>
        </template>
        <template #view-library>
          <LibraryManager class="h-full" @play="playSong" />
        </template>
        <template #view-tidy>
          <!-- 2026-10-21 用户口径：「补全整理的功能不要再用 ⋯ 按钮了，换成顶部的导航栏，
               一个功能页面可以常驻，归类放在一起」——补全 / 查重 / 体检 / 记录 / 替换音质
               升为顶部分段（与发现 / 下载 / 系统同一套壳层机制）。
               TidyView 只挂载一次、各段 v-show 常驻，段间状态不丢：
               跑完的体检结果切走再切回来还在。 -->
          <div class="flex h-full min-h-0 flex-col">
            <div class="shrink-0 border-b border-[var(--ys-border)] bg-[var(--ys-surface-glass)] px-3 py-2 backdrop-blur-xl">
              <SegmentedControl
                :segments="decoratedSegments('tidy')"
                :model-value="segmentOf('tidy')"
                @update:model-value="v => setSegment('tidy', v)"
              />
            </div>
            <TidyView class="min-h-0 flex-1" />
          </div>
        </template>
        <template #view-push>
          <PushManager class="h-full" />
        </template>
        <!-- 下载（队列 / 歌单 / 自动记录）：分段器由壳提供，DownloadHub 读 navBus 的当前分段 -->
        <template #view-download>
          <div class="flex h-full min-h-0 flex-col">
            <div class="shrink-0 border-b border-[var(--ys-border)] bg-[var(--ys-surface-glass)] px-3 py-2 backdrop-blur-xl">
              <SegmentedControl
                :segments="decoratedSegments('download')"
                :model-value="segmentOf('download')"
                @update:model-value="v => setSegment('download', v)"
              />
            </div>
            <DownloadHub class="min-h-0 flex-1" :active-source="activeSourceMeta" />
          </div>
        </template>
        <template #view-sources>
          <SourceManager
            @source-changed="onSourceChanged"
            @pool-changed="onPoolChanged"
            @open-disclaimer="openDisclaimer(false)"
          />
        </template>
        <template #view-accounts><AccountManager /></template>
        <!--
          系统（日志 + 设置）—— 两者都是「工具/配置」页：出问题时来看，改完就走。
          并成一段之后导航短了一项，且这两件事本来就常一起来（日志里看到失败 → 去设置改参数）。
        -->
        <template #view-system>
          <div class="flex h-full min-h-0 flex-col">
            <div class="shrink-0 border-b border-[var(--ys-border)] bg-[var(--ys-surface-glass)] px-3 py-2 backdrop-blur-xl">
              <SegmentedControl
                :segments="decoratedSegments('system')"
                :model-value="segmentOf('system')"
                @update:model-value="v => setSegment('system', v)"
              />
            </div>
            <div class="grid min-h-0 flex-1 grid-cols-1">
              <LogViewer v-if="segMounted('system', 'logs')" v-show="segmentOf('system') === 'logs'" />
              <AppSettings
                v-if="segMounted('system', 'settings')"
                v-show="segmentOf('system') === 'settings'"
                @open-disclaimer="openDisclaimer(false)"
              />
            </div>
          </div>
        </template>
      </SwipePager>
    </div>

    </div><!-- /侧栏 + 内容列 -->

    <!-- ════════════ 手机底部「灵动岛」（.mobile-dock：只在触屏 + 窄屏出现，见 style.css） ════════════
         2026-10-18 用户口径：「底部导航重构 ui，整体采用底部搜索框样式并融合…… 2:1 的方式展示
         导航和搜索框（中间要间隙，左边是椭圆，右边是个搜索圆。点击搜索圆，融合两个圆并出现
         搜索框）有点像苹果灵动岛」。

         结构 = 左椭圆（导航：4 个高频视图 + 更多） + 间隙 + 右圆（搜索），宽度 2:1。
         点右圆 → `islandOpen`：间隙收到 0、两个形状贴合成**一条胶囊**（边框各留半边，
         接缝处不画线），左边内容从「图标排」换成「输入框」。再点一次 / 点外面 / Esc 收回。

         总高仍是 --dock-h（56px + 底部安全区）：PlayerBar / FeedbackHost / 页面底部留白
         的避让量一个像素都不用改。 -->
    <nav class="mobile-dock fixed inset-x-0 bottom-0 z-30 pb-[var(--safe-bottom)]" aria-label="主导航">
      <div
        class="mx-3 mt-1 flex h-14 items-stretch transition-[gap] duration-300 ease-out motion-reduce:duration-0"
        :class="islandOpen ? 'gap-0' : 'gap-2'"
      >
        <!-- 左椭圆：导航（灵动岛打开时它变成搜索输入框） -->
        <div
          class="relative min-w-0 flex-1 overflow-hidden rounded-full border bg-white/90 shadow-[0_8px_30px_rgba(0,0,0,0.10)] backdrop-blur-xl transition-[border-radius] duration-300 ease-out motion-reduce:duration-0"
          :class="islandOpen ? 'rounded-r-none border-r-0 border-[var(--ys-border)]' : 'border-[var(--ys-border)]'"
        >
          <!-- 闭：导航图标排 -->
          <div
            v-if="!islandOpen"
            class="flex h-full items-stretch transition-opacity duration-200"
          >
            <button
              v-for="item in mobileTabs"
              :key="item.id"
              @click="goView(item.id)"
              :title="item.name"
              :aria-label="item.name"
              :aria-current="currentView === item.id ? 'page' : undefined"
              :class="[
                'relative flex flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium transition-colors',
                currentView === item.id ? 'text-[var(--ys-accent)]' : 'text-[var(--ys-text-faint)]'
              ]"
            >
              <component :is="item.icon" class="h-[18px] w-[18px]" />
              <!-- 停在有分段的页上时，标签跟着当前段走（「发现」页里切到榜单段就写「榜单」）
                   —— 手机上分段器在页面顶部，标签栏是最低成本的「我在哪」提示 -->
              <span>{{ mobileTabLabel(item) }}</span>
            </button>
            <button
              @click="showMoreDrawer = true"
              title="更多"
              aria-label="更多（下载 / 音源 / 账号 / 系统）"
              :aria-expanded="showMoreDrawer"
              :class="[
                'relative flex flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium transition-colors',
                showMoreDrawer ? 'text-[var(--ys-accent)]' : 'text-[var(--ys-text-faint)]'
              ]"
            >
              <MoreHorizontal class="h-[18px] w-[18px]" />
              <span>更多</span>
              <!-- 下载现在住在抽屉里：进行中的数量就挂在这个入口上 -->
              <span
                v-if="downloadManager.activeCount.value > 0"
                class="absolute right-[22%] top-1 min-w-[15px] rounded-full bg-emerald-500 px-1 text-center text-[9px] font-bold leading-[15px] text-white"
              >{{ downloadManager.activeCount.value }}</span>
              <!-- 否则有「需要你处理」的事项时给个红点（抽屉里才是完整列表） -->
              <span
                v-else-if="statusItems.some((s) => s.tone === 'error' || s.tone === 'warn')"
                class="absolute right-[24%] top-2 h-1.5 w-1.5 rounded-full bg-rose-500"
              ></span>
            </button>
          </div>

          <!-- 开：搜索输入框（融合后左侧这一半） -->
          <form
            v-else
            class="flex h-full items-center gap-2 px-3.5"
            @submit.prevent="submitIslandSearch"
          >
            <Search class="h-4 w-4 shrink-0 text-[var(--ys-accent)]" />
            <input
              ref="islandInput"
              v-model="islandQuery"
              type="search"
              enterkeyhint="search"
              placeholder="搜索歌曲、歌手、专辑…"
              aria-label="搜索歌曲、歌手、专辑"
              class="min-w-0 flex-1 bg-transparent text-[13px] text-[var(--ys-text)] outline-none placeholder:text-[var(--ys-text-faint)]"
              @keydown.esc="closeIsland"
            />
            <button
              v-if="islandQuery"
              type="button"
              class="shrink-0 rounded-full p-1 text-[var(--ys-text-faint)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text-muted)]"
              title="清空"
              aria-label="清空"
              @click="islandQuery = ''"
            >
              <X class="h-3.5 w-3.5" />
            </button>
          </form>
        </div>

        <!-- 右圆：搜索（灵动岛打开时它变成「搜索」按钮，与左半边接成一条胶囊） -->
        <button
          type="button"
          :title="islandOpen ? '搜索' : '搜索歌曲、歌手、专辑'"
          :aria-label="islandOpen ? '搜索' : '搜索歌曲、歌手、专辑'"
          :aria-expanded="islandOpen"
          @click="islandOpen ? submitIslandSearch() : openIsland()"
          class="relative grid w-14 shrink-0 place-items-center rounded-full border bg-white/90 shadow-[0_8px_30px_rgba(0,0,0,0.10)] backdrop-blur-xl transition-[border-radius,color] duration-300 ease-out motion-reduce:duration-0"
          :class="[
            islandOpen
              ? 'rounded-l-none border-l-0 border-[var(--ys-accent)] bg-[var(--ys-accent)] text-white'
              : 'border-[var(--ys-border)] text-[var(--ys-text-faint)] active:text-[var(--ys-accent)]',
          ]"
        >
          <span class="relative grid place-items-center">
            <Search v-if="!islandOpen" class="h-[19px] w-[19px]" />
            <ArrowRight v-else class="h-[19px] w-[19px]" />
            <!-- 正在播放时给个小绿点：这一颗圆也是「实时」的（用户口径：第二个实时显示小圆） -->
            <span
              v-if="isPlaying && !islandOpen"
              class="island-live-dot absolute -right-1 -top-1 h-2 w-2 rounded-full bg-emerald-500"
            ></span>
          </span>
        </button>
      </div>
    </nav>

    <!-- 灵动岛展开时的「点外面就收」透明层：在岛之下、内容之上 -->
    <div
      v-if="islandOpen"
      class="fixed inset-0 z-20 sm:hidden"
      @click="closeIsland"
      aria-hidden="true"
    ></div>

    <!-- ════════════ 「更多」右侧抽屉（手机低频入口） ════════════
         方向与 ApiDrawer（左）相反：这个从右滑入，跟触发它的「更多」按钮同侧。 -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-opacity duration-300 ease-out"
        enter-from-class="opacity-0"
        enter-to-class="opacity-100"
        leave-active-class="transition-opacity duration-200 ease-in"
        leave-from-class="opacity-100"
        leave-to-class="opacity-0"
      >
        <div
          v-if="showMoreDrawer"
          class="fixed inset-0 z-50 bg-black/40 backdrop-blur-xs sm:hidden"
          @click="showMoreDrawer = false"
        ></div>
      </Transition>
      <Transition
        enter-active-class="transition-transform duration-300 ease-out motion-reduce:duration-0"
        enter-from-class="translate-x-full"
        enter-to-class="translate-x-0"
        leave-active-class="transition-transform duration-200 ease-in motion-reduce:duration-0"
        leave-from-class="translate-x-0"
        leave-to-class="translate-x-full"
      >
        <aside
          v-if="showMoreDrawer"
          class="fixed inset-y-0 right-0 z-50 flex w-[280px] max-w-[85vw] flex-col bg-white/85 shadow-[0_28px_72px_rgba(0,0,0,0.18)] backdrop-blur-2xl sm:hidden"
          aria-label="更多页面"
        >
          <div class="flex h-14 shrink-0 items-center justify-between border-b border-[var(--ys-border)] px-4 pt-[var(--safe-top)]">
            <b class="text-[15px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">更多</b>
            <button
              @click="showMoreDrawer = false"
              class="grid h-8 w-8 place-items-center rounded-full text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
              title="关闭"
              aria-label="关闭"
            >
              <X class="h-4 w-4" />
            </button>
          </div>
          <nav class="min-h-0 flex-1 space-y-1 overflow-y-auto p-3">
            <button
              v-for="item in moreItems"
              :key="item.id"
              @click="goView(item.id)"
              :class="[
                'ys-press flex w-full items-center gap-3 rounded-[10px] px-3 py-3 text-[13px] font-medium',
                currentView === item.id
                  ? 'bg-white text-[var(--ys-text)] shadow-[0_1px_2px_rgba(0,0,0,0.06)] ring-1 ring-black/[0.04]'
                  : 'text-[var(--ys-text-muted)] hover:bg-black/[0.05] hover:text-[var(--ys-text)]'
              ]"
            >
              <component :is="item.icon" class="h-[18px] w-[18px] shrink-0" />
              <span class="flex-1 text-left">{{ item.name }}</span>
              <!-- 下载住进抽屉后，进行中的数量在这里也看得到（与标签栏入口同一个数） -->
              <span
                v-if="item.id === 'download' && downloadManager.activeCount.value > 0"
                class="shrink-0 rounded-full bg-emerald-500 px-1.5 py-px text-[10px] font-semibold text-white"
              >{{ downloadManager.activeCount.value }}</span>
            </button>

            <!-- 任务状态（手机：抽屉里看；桌面：侧栏底部） -->
            <template v-if="statusItems.length">
              <p class="mb-1 mt-4 px-3 text-[11px] font-medium text-[var(--ys-text-faint)]">任务状态</p>
              <button
                v-for="s in statusItems" :key="s.id"
                @click="goView(s.tab); showMoreDrawer = false"
                class="ys-press flex w-full items-center gap-2 rounded-[10px] px-3 py-2 text-left text-[12px] text-[var(--ys-text-muted)] hover:bg-black/[0.05] hover:text-[var(--ys-text)]"
                :title="s.label"
              >
                <component :is="s.icon" class="h-3.5 w-3.5 shrink-0" :class="s.iconClass" />
                <span class="min-w-0 flex-1 truncate">{{ s.label }}</span>
                <span class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium" :class="s.badgeClass">{{ s.count }}</span>
              </button>
            </template>
          </nav>
          <div class="shrink-0 border-t border-[var(--ys-border)] p-3 pb-[calc(0.75rem+var(--safe-bottom))]">
            <button
              @click="navigateTo('sources')"
              class="flex w-full min-w-0 items-center gap-1.5 rounded-full border border-[var(--ys-border)] bg-white px-3 py-2 text-[var(--ys-text-muted)] transition-colors hover:bg-gray-50 hover:text-[var(--ys-text)]"
              :title="`当前音源：${activeSourceMeta?.name || '未设置'} —— 点开管理音源`"
            >
              <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="activeSourceMeta ? 'bg-emerald-500' : 'bg-amber-400'"></span>
              <span class="truncate text-[11px]">{{ activeSourceMeta?.name || '音源' }}</span>
            </button>
          </div>
        </aside>
      </Transition>
    </Teleport>

    <!-- ── 全局下载队列抽屉（快捷查看；完整页面见「下载」视图） ── -->
    <DownloadDrawer :active-source="activeSourceMeta" @open-full="openQueuePage" />

    <!--
      下载目录弹窗**全局挂一次**：它由 `downloadManager.showDirModal` 驱动，
      「发现」页等多处按钮都会打开它，不能绑在下载抽屉的 `v-if` 里
      —— 抽屉关着时点了没反应（见组件内注释）。
    -->
    <DownloadDirModal />

    <!-- ── 开放接口抽屉（从品牌图标右侧滑出） ── -->
    <ApiDrawer v-model="showApiDrawer" />

    <!-- ── 全局免责声明与服务条款弹窗 ── -->
    <DisclaimerModal
      v-model="showDisclaimerModal"
      :is-force="isDisclaimerForce"
    />

    <!-- 帧率探针：只有地址带 ?fps=1 时出现（真机上用来判断"卡不卡"，正式界面看不到） -->
    <FpsMeter v-if="showFpsMeter" class="fixed bottom-4 right-4 z-50" />

    <!-- 全站瞬时提示的唯一渲染点（services/feedback.js 管队列，这里只管样式与位置） -->
    <FeedbackHost />

    <PlayerBar
      :current-song="currentSong"
      :is-playing="isPlaying"
      :current-time="currentTime"
      :duration="duration"
      :show-lyrics="false"
      :is-resolving="isResolving"
      :play-mode="playMode"
      :collapsed="playerBarCollapsed"
      @update:play-mode="val => playMode = val"
      @toggle-play="togglePlay"
      @prev="playPrev"
      @next="playNext"
      @seek="onSeek"
      @toggle-lyrics="() => { showImmersive = true }"
      @toggle-fullscreen="() => { showImmersive = true }"
      @toggle-view="v => goView(v)"
      @volume-change="onVolumeChange"
      @toggle-collapse="playerBarCollapsed = !playerBarCollapsed"
      @search="searchFromPlayerBar"
    />

    <audio
      ref="audioRef"
      preload="auto"
      @timeupdate="onTimeUpdate"
      @durationchange="onDurationChange"
      @ended="onEnded"
      @play="isPlaying = true"
      @pause="isPlaying = false"
      @error="onAudioError"
    ></audio>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { lxRuntime } from './engine/lx-runtime'
import { SearchAPI, SourcesAPI, NasAPI } from './api/client'
import SearchView from './components/SearchView.vue'
import SourceManager from './components/SourceManager.vue'
import PlayerBar from './components/PlayerBar.vue'
import ImmersivePlayer from './components/ImmersivePlayer.vue'
import DownloadDrawer from './components/DownloadDrawer.vue'
import DownloadDirModal from './components/DownloadDirModal.vue'
import LibraryManager from './components/LibraryManager.vue'
import TidyView from './components/TidyView.vue'
import PushManager from './components/PushManager.vue'
import DownloadHub from './components/DownloadHub.vue'
import ApiDrawer from './components/ApiDrawer.vue'
import DisclaimerModal from './components/DisclaimerModal.vue'
import AccountManager from './components/AccountManager.vue'
import SwipePager from './components/SwipePager.vue'
import AppSettings from './components/AppSettings.vue'
import FpsMeter from './components/FpsMeter.vue'
import LogViewer from './components/LogViewer.vue'
import FeedbackHost from './components/FeedbackHost.vue'
import SegmentedControl from './components/SegmentedControl.vue'
import { downloadManager } from './services/downloadManager'
import { bridgeWorker } from './services/bridgeWorker'
import { pushEngine } from './services/pushEngine'
import { loadPref, savePref, loadBool, initUiPrefs } from './services/prefs'
import {
  currentView, segState, segmentOf, goView as navBusGoView,
  onNavigate, navigateTo,
} from './services/navBus'
import { VIEWS, NAV_GROUPS, MOBILE_TAB_IDS, viewsOfGroup, segmentsOf, defaultSegment, resolveRoute } from './services/navViews'
import { fail as feedbackFail } from './services/feedback'
import { invalidate } from './services/viewCache'
import { logError } from './services/appLog'
import {
  Compass, Trophy, Cpu, Disc, Library, UserRound, ScrollText, Settings,
  Wand2, Send, Download, ListMusic, ListOrdered, CloudDownload,
  MoreHorizontal, X, Loader2, AlertTriangle, Search, ArrowRight,
  Sparkles, Copy, ScanSearch, History, Gauge,
} from 'lucide-vue-next'
import { sidebarStatusItems } from './services/libraryView'

// ?fps=1 → 显示帧率探针（见 components/FpsMeter.vue）
const showFpsMeter = typeof location !== 'undefined' && location.search.includes('fps=1')

// ── 当前视图与分段：共享状态住在 navBus（2026-10-17 导航框架重做）──
// 桌面侧栏、手机底部标签栏、SwipePager、各视图的 stale 刷新 watch 读的是同一个
// currentView/segState —— 组件调 navigateTo('download','queue') 就能切页，不必知道壳的结构。

// 切视图的**唯一入口**：所有跳转（导航按钮、组件 emit、播放条按钮）都从这里过。
// 共享状态的写入与「认不出的 id 回落发现页 + warn」都在 navBus.goView 里做
// （单一数据源，别处别再拼 id）；壳只补自己的账 —— mountedSegs 懒挂载标记。
function goView(view, payload) {
  const seg = typeof payload === 'string' ? payload : undefined
  const r = navBusGoView(view, seg)
  if (r.segment) markSegMounted(r.view, r.segment)
  return payload
}

// 组件里的「查看日志」等入口通过导航总线请求切视图 —— 免去给每个组件加 emit + props。
// navigateTo 内部已经调过 navBus.goView 写了共享状态（unknown 的 warn 也在那边发），
// 这里**只补壳自己的账**：不再重复解析跳转，否则同一个 unknown id 会弹两条提示。
//
// payload 仍只有一条路由：{ chartPreview } —— 负载交给 SearchView 自己消费
// （它订阅同一个总线，见 SearchView 的 onChartNav）。
const offNavigate = onNavigate((view, payload) => {
  const seg = typeof payload === 'string' ? payload : undefined
  const r = resolveRoute(view, seg)
  if (!r.unknown && r.segment) markSegMounted(r.view, r.segment)
})

// 滑动切页的视图顺序（computed 是惰性的，即使 navItems 声明在后面也不会踩 TDZ）
const swipeOrderList = computed(() => navItems.map((item) => item.id))

// 写操作 → 数据失效（见 services/viewCache.js 的「事件失效」）：
// 下载完成意味着磁盘上多了文件，「本地曲库」「补全整理」两页的缓存就旧了。
// 不在这里直接发请求，只标过期 —— 用户切过去时（各视图的 stale watch）才刷，避免无谓的后台流量。
watch(
  () => downloadManager.completedCount.value,
  (now, before) => { if (before !== undefined && now > before) invalidate('nas', 'library') },
)

onUnmounted(offNavigate)

/**
 * 全局轻提示（替代 alert）已搬到 `services/feedback.js` + `components/FeedbackHost.vue`。
 *
 * 老实现是 App.vue 私有的单槽位 `appToast`：只有 App.vue 自己调得到，于是 14 个页面
 * 各自造了一套提示（页脚说明、InlineNotice、alert），同一件事三种说法。
 * 现在任何组件 `fail('…', { scope: '下载设置' })` 即可：界面一句结论、细节进日志，
 * 旧 `showAppToast(text, true)` 的「点一下看日志」行为保留（level='fail' 默认开）。
 */

const showImmersive = ref(false)

// 免责声明与条款弹窗状态
const showDisclaimerModal = ref(false)
const isDisclaimerForce = ref(false)

// 开放接口抽屉
const showApiDrawer = ref(false)

// 手机「更多」右侧抽屉（次要入口：整理 / 音源 / 账号 / 系统）。
// 用户口径：「说『样式』指的是呈现方式，不是位置；次要入口要做右侧抽屉」。
const showMoreDrawer = ref(false)
// 切了视图就收抽屉 —— 不管跳转来自抽屉里的按钮还是别处（navigateTo 深链）
watch(currentView, () => { showMoreDrawer.value = false })

// 播放条收起状态（用户 2026-09-28：「播放时，在右边增加一个收缩图标」）。
// 只影响这一条栏，不打断音频播放；展开入口在细条上。
const playerBarCollapsed = ref(false)

// 播放栏发起的搜索（用户 2026-09-28 定：搜索的入口在播放栏，发现页不再放搜索框）。
// nonce 是给 SearchView 的触发信号 —— 同一个词连搜两次也要能跑起来。
const barSearchQuery = ref('')
const barSearchNonce = ref(0)
function searchFromPlayerBar(q) {
  barSearchQuery.value = q
  goView('search')
  barSearchNonce.value += 1
}

// ── 手机底部「灵动岛」搜索（2026-10-18）──
//
// 手机上搜索的入口就在底部这一颗圆里（桌面端仍是播放条右下角那颗图标）。
// 展开/收回、提交三件事都只在这里做，模板只读 `islandOpen`；提交复用
// `searchFromPlayerBar` —— 搜索的落地（切到发现页 + 把词交给 SearchView）只有一条路。
const islandOpen = ref(false)
const islandQuery = ref('')
const islandInput = ref(null)

function openIsland() {
  islandOpen.value = true
  // 等输入框真的挂上来（v-if）再聚焦；没这步手机上点开只是「看到一个框」却不出键盘
  nextTick(() => islandInput.value?.focus?.())
}

function closeIsland() {
  islandOpen.value = false
  islandQuery.value = ''
}

function submitIslandSearch() {
  const q = islandQuery.value.trim()
  if (!q) {
    // 空词不搜：焦点留在框里，等用户打字（而不是把岛收了装作搜过）
    islandInput.value?.focus?.()
    return
  }
  closeIsland()
  searchFromPlayerBar(q)
}

/**
 * 手机标签栏的文字：有多个分段的视图，停在某一页时显示**那一段**的名字
 * （「发现」页里切到榜单段 → 标签写「榜单」）；其余情况仍是视图短名。
 *
 * 手机标签栏一行只放 2~3 个字，段名天然比视图名更接近「我现在看的到底是什么」，
 * 而多出来的分段器在页面顶部已经在那儿了。
 */
function mobileTabLabel(item) {
  const seg = segmentOf(item.id)
  const segs = segmentsOf(item.id)
  if (!seg || segs.length < 2) return item.short
  return segs.find((s) => s.id === seg)?.name || item.short
}


function openDisclaimer(force = false) {
  isDisclaimerForce.value = force
  showDisclaimerModal.value = true
}

// 主导航（桌面侧栏分组 + 手机底部标签栏 + 「更多」抽屉）。
//
// ⚠️ 2026-10-15 重构：**视图清单与顺序的唯一数据源已搬到 services/navViews.js**。
// 以前这里手写一份、模板里的槽位再写一份、组件里的 navigateTo 又写一份，
// 三份对不上就长出孤儿页。
//
// 2026-10-17 导航框架重做：原「曲库管家」一页塞 5 个标签（本地/推送/下载/补全/自动记录，
// 3480 行巨型组件）被拆平 —— 高频功能（下载队列、订阅）升为顶层视图，8 视图按
// 「听 / 管 / 配置」分组呈现。顺序 = 侧栏顺序 = 手指左右滑动的顺序（swipeOrderList 读它）。
// 图标按 id 在这里登记 —— navViews.js 是纯数据，不 import 组件。
const NAV_ICONS = { search: Compass, library: Library, tidy: Wand2, push: Send, download: Download, sources: Cpu, accounts: UserRound, system: Settings }
/** 页内分段的图标（顶层页有自己的图标，段位单独一套） */
const SEGMENT_ICONS = { discover: Compass, charts: Trophy, queue: ListMusic, playlist: ListOrdered, autodl: CloudDownload, logs: ScrollText, settings: Settings, complete: Sparkles, dedupe: Copy, audit: ScanSearch, records: History, quality: Gauge }

const navItems = VIEWS.map((v) => ({ ...v, icon: NAV_ICONS[v.id] }))

/**
 * 顶部一条栏里显示的当前页面名（2026-10-18 用户口径：「把发现音乐、本地曲库等页面名，
 * 移到 logo 和音源管理那行去」）。
 * 全名取自导航注册表 navViews.js —— 各页内容区里那份重复标题已经全部撤掉，
 * 所以这里是页名的**唯一**来源，改名字只改 navViews.js。
 */
const currentViewName = computed(() => navItems.find((i) => i.id === currentView.value)?.name || '')

/**
 * 页面名的悬停简介（2026-10-18 用户口径：「把导航里的其他页面的简介也去掉改hover」）。
 * 简介只有一个出处：navViews.js 里该视图（有分段时优先取当前分段）的 `desc`。
 * 页面里那份常驻说明文字已全部撤掉 —— 用户不想一进页面就被一段灰字介绍。
 */
const currentViewDesc = computed(() => {
  const view = navItems.find((i) => i.id === currentView.value)
  if (!view) return ''
  const seg = (view.segments || []).find((s) => s.id === segmentOf(view.id))
  return seg?.desc || view.desc || ''
})

/** 桌面侧栏：按「听 / 管 / 配置」分组（分组只影响呈现，id 与深链不变） */
const navGroups = NAV_GROUPS.map((g) => ({
  ...g,
  items: viewsOfGroup(g.id).map((v) => ({ ...v, icon: NAV_ICONS[v.id] })),
}))

/** 手机底部标签栏：4 个高频视图 + 「更多」 */
const mobileTabs = MOBILE_TAB_IDS
  .map((id) => navItems.find((i) => i.id === id))
  .filter(Boolean)

/** 「更多」抽屉：其余全部视图 */
const moreItems = navItems.filter((i) => !MOBILE_TAB_IDS.includes(i.id))

/** 段位清单（带图标），给 SegmentedControl 用 */
function decoratedSegments(view) {
  return segmentsOf(view).map((s) => ({ ...s, icon: SEGMENT_ICONS[s.id] }))
}

// ── 全局「任务状态」条（桌面侧栏底部）──
// 原来这个列表住在 LibraryManager 的侧栏里；框架重做后它是**跨页面**的关注项，
// 搬到壳里、由两个全局单例喂数据：downloadManager（下载队列）+ pushEngine（订阅/推送）。
// 补全任务与自动下载失败是各自页面内的手动操作，不进这个全局列表（用户在那两页里才看得到）。
// 顺序、文案、配色、跳转目标都由 services/libraryView.js 决定（那边有单测）；这里只翻成图标与 class。
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
    download: {
      active: downloadManager.activeCount.value,
      failed: downloadManager.failedCount.value,
      paused: downloadManager.pausedCount.value,
    },
    pushRunning: pushEngine.runningIds.value.length,
    pendingBaseline: pushEngine.pendingBaseline.value,
    monitorStats: pushEngine.monitorStats.value,
  }).map((s) => ({
    ...s,
    icon: STATUS_ICONS[s.id] || AlertTriangle,
    iconClass: STATUS_ICON_CLASS[s.tone] || STATUS_ICON_CLASS.muted,
    badgeClass: STATUS_BADGE_CLASS[s.tone] || STATUS_BADGE_CLASS.muted,
    go: () => goView(s.tab),
  })),
)

// ── 页内分段状态 ──
// segState 本身住在 navBus（共享：DownloadHub 等子组件也读 segmentOf），
// 壳里只加一本**懒挂载账**：与 SwipePager 对「页面」的策略一致 —— 访问过就留着，
// 段位之间来回切不重建组件、不重新请求（mountedSegs 记住挂过哪些段；v-show 控制显示）。
const mountedSegs = ref({})

function markSegMounted(view, segment) {
  mountedSegs.value = { ...mountedSegs.value, [`${view}:${segment}`]: true }
}

/** 切段（模板里分段器 @update:model-value 调它） */
function setSegment(view, seg) {
  const r = resolveRoute(view, seg)
  if (r.view !== view) return
  segState.value = { ...segState.value, [view]: r.segment }
  if (r.segment) markSegMounted(r.view, r.segment)
}

/** 这一段是否已经挂载过（懒挂载：没访问过的段不挂载，省掉它启动时的请求） */
function segMounted(view, seg) {
  return !!mountedSegs.value[`${view}:${seg}`]
}

// 启动时把默认段标为已挂载（否则当前段是空白）
for (const v of VIEWS) setSegment(v.id, defaultSegment(v.id))

// 音源名可能很长（用户 2026-09-28：「音源名长了后卡片会超出屏幕，可以优化为缩小后只显示前两个字」）：
// 窄屏胶囊里只放前两个字，完整名在宽屏显示 + title 里始终能看全。
const shortSourceName = computed(() => {
  const full = (activeSourceMeta.value?.name || '').trim()
  if (!full) return '音源'
  return full.length > 2 ? full.slice(0, 2) : full
})

/**
 * 从下载抽屉跳到「下载 → 队列」。
 *
 * 2026-10-17 之前这里要绕道 LibraryManager 的 tab prop + nonce watch（下载是它的
 * 二级标签）；导航框架重做后下载是顶层视图，直接深链到分段即可。
 */
function openQueuePage() {
  downloadManager.close()
  goView('download', 'queue')
}

const audioRef = ref(null)
const audioEl = ref(null)
const isPlaying = ref(false)
const isResolving = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const currentSong = ref(null)
const playlist = ref([])
const currentIndex = ref(0)
const lyricData = ref('')
const transData = ref('')
const savedSource = loadPref('active_source_meta', '')
let initialSource = null
try {
  if (savedSource) initialSource = JSON.parse(savedSource)
} catch (_) {}

const activeSourceMeta = ref(initialSource)

/**
 * 启用池（多选激活，2026-09-30 用户定）：有序数组，前面的优先。
 * `activeSourceMeta` 保持 = **池首** —— 下游（播放栏、下载、曲库管家、AI 桥）全都不用改，
 * 而"池里第一个能干的"这件事由 SearchView 用 `activeSources` 现算。
 */
const activeSourceIds = ref([])
const allSourceMetas = ref([])
const activeSourceMetas = computed(() =>
  activeSourceIds.value.map(id => allSourceMetas.value.find(s => s.id === id)).filter(Boolean),
)
/** 池内挑源：给定平台 → 第一个支持它的源（都不支持则回退池首） */
function sourceIdForPlatform(platform) {
  return lxRuntime.pickSourceForPlatform(activeSourceIds.value, platform)
}

watch(activeSourceMeta, (meta) => {
  if (meta) {
    savePref('active_source_meta', JSON.stringify(meta))
  } else {
    savePref('active_source_meta', '')
  }
}, { deep: true })

const playMode = ref(loadPref('play_mode', 'sequence') || 'sequence')

watch(playMode, (val) => {
  savePref('play_mode', val)
})

// 注意：这里以前有个 watch(isPlaying) 会在「开始播放」时自动展开沉浸式播放器，
// 于是点列表里的歌、切上一首/下一首都会强制弹全屏（2026-09-27 用户反馈）。
// 现在只有用户主动操作才展开：顶栏「打开播放器」、播放条上的歌词/全屏按钮。

onMounted(async () => {
  audioEl.value = audioRef.value
  downloadManager.loadConfig()

  // 推送引擎是全局单例（services/pushEngine.js）：8 秒轮询 + 自动补下载机器住在应用层，
  // 不再挂在某个页面的生命周期上（以前绑在 LibraryManager 的 onMounted/onUnmounted，
  // 用户不打开曲库管家就不跑 —— 这是「我没点它自己在列队」反过来的死区）。start() 幂等。
  pushEngine.start()

  // 启动 AI 解析桥工作器：让外部 AI 提交的取链任务能在本页面完成
  bridgeWorker.start()

  // 0. 免责声明与服务条款：**只需同意一次**。
  //
  // 本地没有记录时，再向服务端确认一次（窗口记忆存在应用数据目录，多端共享）。
  // 这一步是必要的：用 IP 和用域名访问属于不同 origin，localStorage 不互通，
  // 只看本地会出现「明明同意过又弹」。
  // 加 1.5s 上限 —— 后端慢或不可用时也不能卡住启动，最多多问这一次。
  if (!loadBool('disclaimer_accepted')) {
    await Promise.race([initUiPrefs(), new Promise((r) => setTimeout(r, 1500))])
  }
  if (!loadBool('disclaimer_accepted')) {
    setTimeout(() => {
      openDisclaimer(true)
    }, 150)
  }

  // 1. 加载音源
  try {
    const res = await SourcesAPI.list()
    if (res.code === 200) {
      const srcs = res.data.sources || []
      allSourceMetas.value = srcs
      // 启用池：新字段优先；老后端只给单值 active_id 时降级成单元素池
      const poolIds = Array.isArray(res.data.active_source_ids) && res.data.active_source_ids.length
        ? res.data.active_source_ids
        : (res.data.active_id ? [res.data.active_id] : [])
      activeSourceIds.value = poolIds
      const meta = srcs.find(s => s.id === poolIds[0]) || null
      activeSourceMeta.value = meta
      // 预先加载所有已导入的音源脚本，确保主音源失效时能够秒级无缝自动容灾降级！
      for (const s of srcs) {
        try {
          const scriptRes = await SourcesAPI.getScript(s.id)
          if (scriptRes.code === 200) {
            await lxRuntime.loadScript(s, scriptRes.data.script)
          }
        } catch (e) {
          console.warn('Failed to load source:', s.id, e)
        }
      }

      // 服务型音源（按服务地址接入）没有脚本，单独登记进运行时，
      // 这样它们才能和脚本型音源一起参与取链轮询与熔断。
      try {
        const apiRes = await SourcesAPI.apiList()
        if (apiRes?.code === 200) {
          lxRuntime.setApiSources(apiRes.data?.sources || [])
        }
      } catch (e) {
        console.warn('Failed to load API sources:', e)
      }
    }
  } catch (e) {
    console.warn('Failed to load active source:', e)
  }
})

function onSourceChanged(meta) {
  activeSourceMeta.value = meta
}

/**
 * 音源管理页改动了启用池 → 同步到全局。
 * `list` 是池内各源的 meta（有序）；池首同时写进 activeSourceMeta（下游只认它）。
 */
function onPoolChanged({ ids, metas }) {
  activeSourceIds.value = Array.isArray(ids) ? ids : []
  if (Array.isArray(metas)) allSourceMetas.value = metas
  activeSourceMeta.value = (metas || []).find(m => m.id === activeSourceIds.value[0]) || null
}

function isFakeOrNoticeAudio(url) {
  if (!url || typeof url !== 'string') return true
  const lower = url.toLowerCase()
  return lower.includes('panspace') || lower.includes('notice') || lower.includes('audio_forbidden') || lower.includes('error.mp3')
}

let isSwitchingDirect = false
let currentSessionId = 0
let lastFailedSongId = null

async function playSong(songOrPayload) {
  // 只切歌、不展开大屏：列表点歌与切歌时界面不该被全屏盖住（2026-09-27 用户反馈）

  let song = songOrPayload
  if (songOrPayload && songOrPayload.song) {
    song = songOrPayload.song
    if (songOrPayload.playlist && Array.isArray(songOrPayload.playlist) && songOrPayload.playlist.length > 0) {
      playlist.value = [...songOrPayload.playlist]
      const foundIdx = songOrPayload.index !== undefined ? songOrPayload.index : playlist.value.findIndex(s => s.id === song.id)
      currentIndex.value = foundIdx >= 0 ? foundIdx : 0
    }
  } else if (song) {
    const idx = playlist.value.findIndex(s => s.id === song.id)
    if (idx === -1) {
      playlist.value.push(song)
      currentIndex.value = playlist.value.length - 1
    } else {
      playlist.value[idx] = song
      currentIndex.value = idx
    }
  }

  if (!song) return

  const thisSession = ++currentSessionId
  lastFailedSongId = null

  // 1. 立即停止上一首音频并完全卸载（使用 removeAttribute('src') 避免触发浏览器虚假 error 事件）
  if (audioRef.value) {
    try {
      audioRef.value.pause()
      audioRef.value.removeAttribute('src')
      audioRef.value.load()
    } catch (_) {}
  }
  isPlaying.value = false
  currentTime.value = 0
  duration.value = 0
  lyricData.value = ''
  transData.value = ''
  isResolving.value = true
  currentSong.value = { ...song }

  // NAS 本地音频直接播放并同步加载歌词
  if (song.source === 'nas' && song.streamUrl) {
    fetchNasLyric(song, thisSession)
    startAudioPlay(song.streamUrl, thisSession)
    isResolving.value = false
    return
  }

  const sourceId = activeSourceMeta.value?.id || ''
  if (!sourceId) {
    isResolving.value = false
    // 官方协议负责搜索 / 榜单 / 歌单 / 歌词（后端内置，免脚本）；
    // 只有「把歌曲解析成可播放直链」这一步需要 LX 协议。所以这里只拦播放，不拦浏览。
    //
    // ⚠️ 确认框只问「要不要去导入」，**不解释机制** —— 用户在这一步需要的是选择，
    // 不是「LX 协议 vs 官方协议」的科普（那是日志/文档的事，见 HANDOVER §3.3.3）。
    const go = confirm('还没有可用的音源，无法播放。\n\n现在去「音源管理」导入一个吗？')
    if (go) {
      goView('sources')
    }
    return
  }

  const platform = song.source || 'kw'

  // 3. 立即并行拉取新渠道的对应歌词，无需等待音频直链解析完成
  fetchLyrics(sourceId, platform, song, thisSession)

  let resolvedUrl = ''
  let resolvedHeaders = {}

  try {
    try {
      const result = await lxRuntime.getMusicUrl(sourceId, platform, song, song.quality || '320k')
      if (thisSession !== currentSessionId) return
      if (result?.url && /^https?:/.test(result.url) && !isFakeOrNoticeAudio(result.url)) {
        resolvedUrl = result.url
        resolvedHeaders = result.headers || {}
        console.log('[PLAY] Custom source resolved:', resolvedUrl)
      }
    } catch (err) {
      console.warn('[PLAY] Custom source resolution failed:', err.message)
    }

    // 智能跨源换源匹配 (Cross-platform song match fallback, 类似洛雪换源播放机制)
    // 当原平台无法解析（例如 WY 接口受限或 VIP 付费独家），自动跨平台匹配 KW、TX 的同名音轨
    if (!resolvedUrl && thisSession === currentSessionId && song?.name) {
      console.log(`[PLAY] 原平台 [${platform}] 解析失败，启动智能跨平台换源匹配: "${song.name}"...`)
      const altPlatforms = ['kw', 'tx', 'kg'].filter(p => p !== platform)
      
      for (const altP of altPlatforms) {
        if (resolvedUrl || thisSession !== currentSessionId) break
        try {
          const cleanName = song.name.replace(/\(.*?\)|（.*?）|\[.*?\]|【.*?】/g, '').trim() || song.name
          const query = `${cleanName} ${song.singer || ''}`.trim()
          const searchRes = await SearchAPI.search(query, altP, 1)
          const candidates = searchRes?.data?.list || searchRes?.list || []
          
          for (let i = 0; i < Math.min(candidates.length, 3); i++) {
            if (resolvedUrl || thisSession !== currentSessionId) break
            const cand = candidates[i]
            try {
              const matchResult = await lxRuntime.getMusicUrl(sourceId, altP, cand, '128k')
              if (matchResult?.url && /^https?:/.test(matchResult.url) && !isFakeOrNoticeAudio(matchResult.url)) {
                resolvedUrl = matchResult.url
                resolvedHeaders = matchResult.headers || {}
                console.log(`[PLAY] 智能跨源匹配成功 [${altP.toUpperCase()}]:`, cand.name, cand.singer, resolvedUrl)
                fetchLyrics(sourceId, altP, cand, thisSession)
                break
              }
            } catch (_) {}
          }
        } catch (e) {
          console.warn(`[PLAY] 跨源检索 [${altP}] 异常:`, e)
        }
      }
    }

    if (thisSession !== currentSessionId) return

    if (resolvedUrl && !isFakeOrNoticeAudio(resolvedUrl)) {
      startAudioPlay(resolvedUrl, thisSession, resolvedHeaders)
    } else {
      const srcName = activeSourceMeta.value?.name || '当前音源'
      // 播放失败：界面上只给一句结论 + 该怎么做；完整原因（音源名、平台、歌曲 id）进日志。
      // 原来这里是一个带「可能原因 1/2」的长 alert，既打断操作又把技术细节推给用户。
      // 现在走统一反馈通道：失败提示可点一下直达「日志」段（作用域写在下面那条日志里）。
      feedbackFail(`《${song.name}》解析失败 —— 换一个音源试试，详情见「日志」`)
      logError(
        '播放取链',
        `${srcName} 未能解析《${song.name}》`,
        `platform=${platform} source=${song.source || ''} songmid=${song.songmid || song.id || ''}`,
      )
    }
  } finally {
    if (thisSession === currentSessionId) {
      isResolving.value = false
    }
  }
}

function playAllSongs({ songs, startIndex = 0 }) {
  if (!songs || !songs.length) return
  playlist.value = [...songs]
  if (playMode.value === 'random') {
    currentIndex.value = Math.floor(Math.random() * songs.length)
  } else {
    currentIndex.value = startIndex
  }
  playSong(playlist.value[currentIndex.value])
}

async function fetchNasLyric(song, sessionId) {
  if (!song?.path) return
  try {
    const res = await NasAPI.lyric(song.path)
    if (sessionId && sessionId !== currentSessionId) return
    if (res?.code === 200 && res.data?.lyric) {
      lyricData.value = res.data.lyric
      transData.value = ''
      console.log('[NAS LYRIC] Loaded successfully from:', res.data.source)
    }
  } catch (e) {
    console.warn('[NAS LYRIC] fetch error:', e)
  }
}

async function fetchLyrics(sourceId, platform, song, sessionId) {
  // 1. Try LX custom source lyric if source available
  if (sourceId) {
    try {
      const lrc = await lxRuntime.getLyric(sourceId, platform, song)
      if (sessionId && sessionId !== currentSessionId) return
      if (lrc?.lyric) {
        lyricData.value = lrc.lyric
        transData.value = lrc.tlyric || ''
        return
      }
    } catch (e) {
      console.warn('[LYRIC] Custom source lyric failed:', e)
    }
  }

  // 2. High-precision Universal Lyric Engine (covers 99.9% songs of all platforms)
  try {
    const duration = song.interval || song.duration || 0
    const hash = song.hash || ''
    const res = await SearchAPI.lyric(platform, song.songmid || song.id, song.name, song.singer, duration, hash)
    if (sessionId && sessionId !== currentSessionId) return
    if (res.code === 200 && res.data?.lyric) {
      lyricData.value = res.data.lyric
      transData.value = res.data.tlyric || ''
      console.log('[LYRIC] Universal lyric loaded, length:', res.data.lyric.length)
    }
  } catch (e) {
    console.warn('[LYRIC] Universal lyric failed:', e)
  }
}

let currentRawUrl = ''
let currentPlayingHeaders = {}

function startAudioPlay(url, sessionId, headers = {}) {
  if (sessionId && sessionId !== currentSessionId) return
  if (!audioRef.value) return
  currentRawUrl = url
  currentPlayingHeaders = headers || {}

  let playSrc = url
  // 通过后端流中继代理，彻底消除浏览器跨域CORS、防盗链403以及HTTPS Mixed Content阻断！
  if (playSrc.startsWith('http://') || playSrc.startsWith('https://')) {
    if (!playSrc.includes('/api/player/stream') && !playSrc.includes('/api/nas/stream')) {
      let proxyUrl = `/api/player/stream?url=${encodeURIComponent(playSrc)}`
      const ref = headers?.referer || headers?.Referer
      if (ref) {
        proxyUrl += `&referer=${encodeURIComponent(ref)}`
      }
      playSrc = proxyUrl
    }
  }
  audioRef.value.src = playSrc
  audioRef.value.load()
  const p = audioRef.value.play()
  if (p !== undefined) {
    p.then(() => {
      isPlaying.value = true
    }).catch(e => {
      console.warn('Play prevented or error:', e)
      isPlaying.value = false
    })
  }
}

function togglePlay() {
  if (!audioRef.value) return
  if (audioRef.value.paused) {
    audioRef.value.play().then(() => { isPlaying.value = true }).catch(console.warn)
  } else {
    audioRef.value.pause()
    isPlaying.value = false
  }
}

function getRandomIndex() {
  if (playlist.value.length <= 1) return 0
  let nextIdx = currentIndex.value
  while (nextIdx === currentIndex.value) {
    nextIdx = Math.floor(Math.random() * playlist.value.length)
  }
  return nextIdx
}

// 手动切歌一律走「当前列表顺序」，与播放模式无关。
// 用户口径（2026-09-27）：下一首/上一首就该是当前页面歌单的下一首/上一首。
// 随机只作用于「自动续播到下一首」和「播放全部」的起始曲，见 onEnded / playAllSongs。
function playPrev() {
  if (!playlist.value.length) return
  currentIndex.value = (currentIndex.value - 1 + playlist.value.length) % playlist.value.length
  playSong(playlist.value[currentIndex.value])
}

/* 播放列表抽屉里点某一行（v2.1.64）：切到那一首，队列本身不动。
   走的是同一条 playSong 路径 —— 上一首 / 下一首仍然按当前队列顺序走。 */
function playFromQueue(idx) {
  const song = playlist.value[idx]
  if (!song) return
  currentIndex.value = idx
  playSong(song)
}

/* 播放列表抽屉头部的垃圾桶：清空队列。正在播的那首**继续播**（用户是在清列表，不是停止播放），
   只是没有「下一首」可切了 —— prev/next 里本来就对空队列有 return 兜底。 */
function clearPlaylist() {
  if (!playlist.value.length) return
  const cur = currentSong.value
  playlist.value = []
  currentIndex.value = 0
  if (cur) playlist.value = [cur]
}

function playNext() {
  if (!playlist.value.length) return
  currentIndex.value = (currentIndex.value + 1) % playlist.value.length
  playSong(playlist.value[currentIndex.value])
}

function onSeek(time) { if (audioRef.value) audioRef.value.currentTime = time }
function onTimeUpdate() { if (audioRef.value) currentTime.value = audioRef.value.currentTime }

function onDurationChange() {
  if (!audioRef.value) return
  duration.value = audioRef.value.duration || 0
}

async function onAudioError(e) {
  if (isResolving.value) return
  if (!audioRef.value || !audioRef.value.getAttribute('src')) return
  const src = audioRef.value.src || ''
  if (!src || src === window.location.href || src.endsWith('/api/player/stream?url=')) return

  const song = currentSong.value
  if (!song || song.source === 'nas') return

  console.warn('[AUDIO] Audio playback error detected for:', song?.name, 'src:', src, e)

  // 如果后端代理流播放出错，尝试直接降级播放原始直链 URL
  if (src.includes('/api/player/stream') && currentRawUrl && currentRawUrl !== src) {
    console.log('[AUDIO] Proxy stream error, falling back directly to raw URL:', currentRawUrl)
    audioRef.value.src = currentRawUrl
    audioRef.value.load()
    audioRef.value.play().then(() => {
      isPlaying.value = true
    }).catch(err => {
      console.warn('[AUDIO] Direct playback also failed:', err)
      isPlaying.value = false
    })
    return
  }

  isPlaying.value = false
}
function onEnded() {
  if (playMode.value === 'loop') {
    if (audioRef.value) {
      audioRef.value.currentTime = 0
      audioRef.value.play().catch(console.warn)
    }
    return
  }
  if (playMode.value === 'random') {
    currentIndex.value = getRandomIndex()
    playSong(playlist.value[currentIndex.value])
    return
  }
  playNext()
}
function onVolumeChange(val) { if (audioRef.value) audioRef.value.volume = val }

function toggleFullscreen() {
  if (!document.fullscreenElement) document.documentElement.requestFullscreen().catch(console.warn)
  else document.exitFullscreen().catch(console.warn)
}
</script>
