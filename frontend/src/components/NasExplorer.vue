<template>
  <!--
    ⚠️ 这一页不再自带 PageShell（v2.1.120）：它从顶层导航搬进了「曲库管家 → 本地音乐」，
    外壳由曲库管家提供 —— 再套一层会出现两个标题栏、双份内边距与两个滚动容器。
    页头那排动作按钮原样留着，只是从 PageShell 的 #actions 槽挪成了普通工具栏。
  -->
  <div>
    <!--
      2026-10-18：这里原来还有「本地音乐」大标题 + 「飞牛 NAS 本地曲库 · FLAC / APE / WAV / DSD / MP3 串流。」
      一行简介。用户要求把页面名统一挪到顶部那一条栏，简介改成悬停可见 —— 顶部栏已经显示
      「本地曲库」，页面里再写一遍就是重复。简介收进这一行的 title，动作按钮原地保留。
      同一批改造里，这排按钮也从裸在页面底色上收进了白底圆角卡（用户：「全局卡片化」）。
    -->
    <div
      class="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border border-gray-200 bg-white px-3.5 py-2.5"
      title="飞牛 NAS 本地曲库 · FLAC / APE / WAV / DSD / MP3 串流。"
    >
      <span class="rounded-full border border-[var(--ys-border)] bg-black/[0.03] px-2.5 py-0.5 text-[10px] font-medium tracking-[0.02em] text-[var(--ys-text-muted)]">
        原生存储卷支持
      </span>
      <div class="ml-auto flex flex-wrap items-center gap-1.5">
      <button
        @click="playAllNasSongs"
        :disabled="!songs.length"
        class="flex items-center gap-1.5 rounded-[10px] border border-[var(--ys-border)] bg-white px-3.5 py-1.5 text-xs font-medium text-[var(--ys-text)] transition-colors hover:bg-black/[0.03] disabled:opacity-50"
      >
        <Play class="w-3.5 h-3.5 fill-current text-[var(--ys-accent)]" />
        <span>播放全部</span>
      </button>

      <button
        @click="openFolderModal"
        class="ys-press flex items-center gap-1.5 rounded-[10px] bg-emerald-500 px-3.5 py-1.5 text-xs font-semibold text-white shadow-[0_1px_2px_rgba(11,138,102,0.3)] transition-colors hover:bg-emerald-600"
      >
        <FolderOpen class="w-3.5 h-3.5" />
        <span>选择曲库目录</span>
      </button>

      <button
        @click="scanFolder(currentDir, true)"
        :disabled="isScanning"
        class="px-3.5 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-gray-600 text-xs font-medium transition-all flex items-center gap-1.5"
      >
        <RotateCw :class="['w-3.5 h-3.5', isScanning ? 'animate-spin' : '']" />
        <span>{{ isScanning ? '扫描中...' : '重新扫描' }}</span>
      </button>

      <!--
        用户 2026-10-24：「飞牛里打开音乐文件夹显示未知错误」。
        他看到文件的地方就是这一页，所以把体检入口放在这里（真正的报告在
        「补全整理 → 体检」，那边才有扫描/修复的完整交互）——
        否则功能藏在另一个页面，用户只会觉得「没修」。
      -->
      <button
        @click="openHealthCheck"
        class="px-3.5 py-1.5 rounded-lg bg-rose-50 hover:bg-rose-100 text-rose-700 font-semibold text-xs border border-rose-200/80 shadow-xs flex items-center gap-1.5 transition-all"
        title="飞牛文件管理或官方音乐 App 打开这个文件夹报「未知错误」时，先来这里体检文件名"
      >
        <ShieldAlert class="w-3.5 h-3.5" />
        <span>体检文件名</span>
      </button>

      <button
        @click="embedAllLyrics"
        :disabled="!songs.length || isBatchEmbedding"
        class="px-3.5 py-1.5 rounded-lg bg-amber-50 hover:bg-amber-100 text-amber-700 font-semibold text-xs border border-amber-200/80 shadow-xs flex items-center gap-1.5 transition-all disabled:opacity-50"
        title="为当前列表所有歌曲抓取歌词并内嵌进音频标签（MP3 USLT / FLAC LYRICS）"
      >
        <Loader2 v-if="isBatchEmbedding" class="w-3.5 h-3.5 animate-spin" />
        <FileMusic v-else class="w-3.5 h-3.5" />
        <span>{{ isBatchEmbedding ? '内嵌中...' : '批量内嵌歌词' }}</span>
      </button>
      </div>
    </div>

    <div class="space-y-3">

    <!-- 歌词内嵌结果提示：失败时带「查看日志」入口，逐首原因在日志里 -->
    <InlineNotice :text="embedNotice" :tone="embedOk ? 'ok' : 'error'" />

    <!-- Current Directory & Quick Switch Bar -->
    <div class="rounded-xl border border-gray-200 bg-white px-4 py-3 flex flex-col sm:flex-row sm:items-center justify-between gap-2.5 text-xs">
      <!-- Left: Current Directory display & Breadcrumb -->
      <!-- 2026-09-28 用户反馈「当前目录：路径显示不全」——原来是 `overflow-hidden` + `overflow-x-auto`，
           深路径只显示前几段、还得手动横滚。改成**换行铺开**：整条路径完整可见，不再裁。 -->
      <div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
        <div class="flex shrink-0 items-center gap-1.5 rounded-[8px] border border-[var(--ys-border)] bg-black/[0.02] px-2.5 py-1 font-mono text-[var(--ys-text-muted)]">
          <Folder class="w-3.5 h-3.5 text-[var(--ys-accent)]" />
          <span class="font-bold">当前目录:</span>
        </div>

        <div class="flex min-w-0 flex-wrap items-center gap-x-1 gap-y-0.5 text-gray-600 font-mono py-0.5">
          <button
            v-for="(crumb, idx) in breadcrumbs"
            :key="idx"
            @click="switchDir(crumb.path)"
            :title="crumb.path"
            class="flex max-w-full items-center gap-1 break-all rounded px-1.5 py-0.5 text-left transition-colors hover:bg-black/[0.05] hover:text-[var(--ys-accent)]"
          >
            <span>{{ crumb.name }}</span>
            <span v-if="idx < breadcrumbs.length - 1" class="text-gray-300">/</span>
          </button>
        </div>

        <span class="text-[11px] text-gray-400 font-normal shrink-0">
          (已加载 {{ songs.length }} 首歌曲)
        </span>
        <!-- 枚举被上限/深度截断时必须说出来：以前只显示「已加载 1000 首」，
             看起来就是这个目录的全部，用户根本不知道该去子目录里找。
             文案与悬停原因都来自 nasView.js（scanHintOf），好让这段规则能被单测钉住。 -->
        <span
          v-if="scanHint.show"
          class="text-[11px] text-amber-600 font-normal shrink-0 cursor-help"
          :title="scanHint.title"
        >
          {{ scanHint.text }}
        </span>
      </div>

      <!-- Right: Quick Volumes / Preset Buttons -->
      <div class="flex shrink-0 items-center gap-1.5 overflow-x-auto">
        <span class="text-gray-400 text-xs shrink-0">快速切换:</span>
        <button
          v-for="d in quickDirs"
          :key="d"
          @click="switchDir(d)"
          :class="[
            'px-2.5 py-1 rounded-lg font-mono text-[11px] transition-all whitespace-nowrap',
            currentDir === d
              ? 'bg-emerald-500 text-white font-semibold shadow-xs'
              : 'bg-gray-100 hover:bg-gray-200 text-gray-600'
          ]"
        >
          {{ d }}
        </button>
        <button
          @click="openFolderModal"
          class="whitespace-nowrap rounded-[8px] bg-black/[0.04] px-2 py-1 text-[11px] font-medium text-[var(--ys-text-muted)] transition-colors hover:bg-black/[0.07] hover:text-[var(--ys-text)]"
        >
          + 更多目录
        </button>
      </div>
    </div>

    <!-- Song Table List -->
    <div>
      <!-- Loading State -->
      <div v-if="isScanning" class="h-72 flex flex-col items-center justify-center text-gray-400">
        <RotateCw class="w-8 h-8 text-emerald-500 animate-spin mb-3" />
        <p class="text-sm font-medium text-gray-600">正在深度遍历 NAS 音乐文件与提取 ID3 封面...</p>
        <p class="text-xs text-gray-400 mt-1">目录：{{ currentDir }}</p>
      </div>

      <!-- Empty State -->
      <div v-else-if="!songs.length" class="h-72 flex flex-col items-center justify-center text-gray-400 bg-white rounded-2xl border border-gray-100 shadow-xs p-8">
        <div class="w-14 h-14 rounded-2xl bg-emerald-50 flex items-center justify-center mb-3">
          <FolderOpen class="w-7 h-7 text-emerald-500" />
        </div>
        <p class="text-base font-semibold text-gray-700">当前目录未找到音频文件</p>
        <p class="text-xs text-gray-400 mt-1 mb-4">当前路径：{{ currentDir }}</p>
        <button
          @click="openFolderModal"
          class="px-4 py-2 rounded-xl bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold shadow-sm transition-all flex items-center gap-1.5"
        >
          <FolderOpen class="w-3.5 h-3.5" />
          <span>点击选择包含音乐的文件夹</span>
        </button>
      </div>

      <!--
        Table：**手机上不再横向滚动**（2026-09-30 用户反馈：「看不到后面的播放（操作列）」）。

        历史：最早是每列 max-w 硬截断（歌名被切得看不清，2026-09-22 否掉）→ 改成
        `min-w-[560px]` + 横向滚动（信息完整了，但**最右边的「操作」列被推出屏幕**，
        手机上要横滑才点得到播放）。

        现在的做法：
          · 手机上 `table-fixed`：列宽由容器决定，表格**永远等于屏宽**，不再溢出 → 操作列始终可见；
          · 「歌曲标题 / 封面」列吃掉剩余宽度，标题与歌手用 `truncate` 出省略号
            （用户明确要求：「把前面的歌曲标题限制字数用省略号代替」），悬停可看完整文件名；
          · `sm` 起回到 `table-auto + min-w-[560px]`，桌面端行为与之前完全一致。
        ⚠️ 表格里那几个 `hidden md:/lg:table-cell` 的列（专辑/格式/大小）在手机上本来就是隐藏的，
           所以手机上只有「# / 标题 / 操作」三列，放得下。
      -->
      <div v-else class="overflow-hidden rounded-2xl border border-gray-100 bg-white shadow-xs">
        <div class="overflow-x-auto">
        <table class="w-full table-fixed text-left text-xs text-gray-600 border-collapse sm:table-auto sm:min-w-[560px]">
          <thead class="bg-gray-50/80 border-b border-gray-100 text-gray-400 font-medium">
            <tr>
              <!--
                列排序（2026-10-24 用户：「曲库里面已下载的歌曲，按照最新排序，或者帮我增加列排序选项」）。
                默认列是「最新」（updated_at 倒序），所以「#」本身也是可点的：点了就回到默认顺序，
                否则用户一旦切到「专辑」就再也没有回程入口了。
                手机上专辑/格式/大小三列本来就 hidden，剩下的标题列照样可点，不影响窄屏。
              -->
              <th class="py-3 px-2 sm:px-3 w-10 sm:w-12 text-center" :aria-sort="sortKey === 'newest' ? 'descending' : 'none'">
                <button
                  type="button"
                  @click="sortByNewest"
                  class="mx-auto flex items-center gap-0.5 rounded px-1 transition-colors hover:text-[var(--ys-text)]"
                  :class="sortKey === 'newest' ? 'font-semibold text-[var(--ys-accent)]' : ''"
                  title="按最新排序（刚下载的在前）"
                >
                  <span>#</span>
                  <ChevronDown v-if="sortKey === 'newest'" class="h-3 w-3" />
                </button>
              </th>
              <th class="py-3 px-3 sm:px-4" :aria-sort="ariaSortOf('title')">
                <button
                  type="button"
                  @click="toggleSort('title')"
                  class="flex items-center gap-1 rounded transition-colors hover:text-[var(--ys-text)]"
                  :class="sortKey === 'title' ? 'font-semibold text-[var(--ys-text)]' : ''"
                  title="点击按歌曲标题排序，再点一次切换升 / 降序"
                >
                  <span>歌曲标题 / 封面</span>
                  <ChevronUp v-if="sortKey === 'title' && sortDir === 'asc'" class="h-3 w-3 shrink-0" />
                  <ChevronDown v-else-if="sortKey === 'title'" class="h-3 w-3 shrink-0" />
                </button>
              </th>
              <th class="hidden md:table-cell py-3 px-4" :aria-sort="ariaSortOf('album')">
                <button
                  type="button"
                  @click="toggleSort('album')"
                  class="flex items-center gap-1 rounded transition-colors hover:text-[var(--ys-text)]"
                  :class="sortKey === 'album' ? 'font-semibold text-[var(--ys-text)]' : ''"
                  title="点击按专辑排序，再点一次切换升 / 降序"
                >
                  <span>专辑</span>
                  <ChevronUp v-if="sortKey === 'album' && sortDir === 'asc'" class="h-3 w-3 shrink-0" />
                  <ChevronDown v-else-if="sortKey === 'album'" class="h-3 w-3 shrink-0" />
                </button>
              </th>
              <th class="hidden sm:table-cell py-3 px-3 text-center" :aria-sort="ariaSortOf('format')">
                <button
                  type="button"
                  @click="toggleSort('format')"
                  class="mx-auto flex items-center gap-1 rounded transition-colors hover:text-[var(--ys-text)]"
                  :class="sortKey === 'format' ? 'font-semibold text-[var(--ys-text)]' : ''"
                  title="点击按格式排序，再点一次切换升 / 降序"
                >
                  <span>格式</span>
                  <ChevronUp v-if="sortKey === 'format' && sortDir === 'asc'" class="h-3 w-3 shrink-0" />
                  <ChevronDown v-else-if="sortKey === 'format'" class="h-3 w-3 shrink-0" />
                </button>
              </th>
              <th class="hidden lg:table-cell py-3 px-3 text-right" :aria-sort="ariaSortOf('size')">
                <button
                  type="button"
                  @click="toggleSort('size')"
                  class="ml-auto flex items-center gap-1 rounded transition-colors hover:text-[var(--ys-text)]"
                  :class="sortKey === 'size' ? 'font-semibold text-[var(--ys-text)]' : ''"
                  title="点击按文件大小排序，再点一次切换升 / 降序"
                >
                  <span>文件大小</span>
                  <ChevronUp v-if="sortKey === 'size' && sortDir === 'asc'" class="h-3 w-3 shrink-0" />
                  <ChevronDown v-else-if="sortKey === 'size'" class="h-3 w-3 shrink-0" />
                </button>
              </th>
              <th class="w-[112px] py-3 px-2 text-center sm:w-20 sm:px-4">操作</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100">
            <tr 
              v-for="(song, idx) in sortedSongs" 
              :key="song.path"
              @dblclick="playSong(song, idx)"
              class="group cursor-pointer transition-colors hover:bg-black/[0.02]"
            >
              <td class="py-3 px-2 sm:px-3 text-center text-gray-400 font-mono">{{ idx + 1 }}</td>
              <td class="py-3 px-3 sm:px-4 flex items-center gap-2.5 sm:gap-3 min-w-0">
                <div class="w-8 h-8 sm:w-9 sm:h-9 rounded-lg overflow-hidden bg-gray-100 border border-gray-200/60 shrink-0 flex items-center justify-center">
                  <img 
                    v-if="song.cover_url" 
                    :src="song.cover_url" 
                    alt="cover" 
                    referrerpolicy="no-referrer"
                    class="w-full h-full object-cover"
                    @error="song.cover_url = ''"
                  />
                  <span v-else class="text-sm sm:text-base">💿</span>
                </div>
                <div class="min-w-0 flex-1 overflow-hidden">
                  <div class="flex min-w-0 items-center gap-1.5">
                    <!-- 悬停给完整文件名：文件名本身不再占一行（见下） -->
                    <p
                      class="max-w-full truncate font-medium text-[var(--ys-text)] transition-colors group-hover:text-[var(--ys-accent)] sm:max-w-xs"
                      :title="song.filename || ''"
                    >
                      {{ song.title || song.filename }}
                    </p>
                    <span v-if="song.has_lyric" class="shrink-0 rounded bg-emerald-100 px-1 py-0.2 text-[9px] font-bold text-emerald-700" title="含本地歌词">词</span>
                  </div>
                  <!--
                    第二行：**艺术家**（用户 2026-09-22：「把本地音乐里的艺术家加到歌曲标题/封面的第二行」）。
                    窄屏上独立的「艺术家」列会被挤到 90px 并截断，得滑到右边才看得见 ——
                    放到标题正下方一眼就能看到是谁唱的（该列已删掉，见表格列定义）。

                    ⚠️ **文件名不再显示**（用户 2026-09-22 追问「右边灰色文字是啥，xxx.mp3」）：
                    它是 `.mp3` 这类扩展名或整条文件名，标题已经说明了是哪首歌，补不了多少信息，
                    却白占一行。真要看完整文件名，悬停在标题上即可。
                  -->
                  <p class="max-w-full truncate text-[11px] text-gray-500 sm:max-w-xs">
                    {{ song.artist || '本地歌手' }}
                  </p>
                </div>
              </td>
              <td class="hidden md:table-cell py-3 px-4 text-gray-400 truncate max-w-[150px]">{{ song.album || 'NAS 音乐' }}</td>
              <td class="hidden sm:table-cell py-3 px-3 text-center">
                <span class="rounded border border-[var(--ys-border)] bg-black/[0.03] px-1.5 py-0.5 font-mono text-[10px] font-medium text-[var(--ys-text-muted)]">
                  {{ song.format }}
                </span>
              </td>
              <td class="hidden lg:table-cell py-3 px-3 text-right text-gray-400 font-mono">{{ formatSize(song.size) }}</td>
              <td class="py-3 px-3 sm:px-4 text-center">
                <div class="flex items-center justify-center gap-1">
                  <button 
                    @click.stop="playSong(song, idx)"
                    class="grid h-7 w-7 place-items-center rounded-[7px] text-[var(--ys-text-faint)] transition-colors hover:bg-[var(--ys-accent-soft)] hover:text-[var(--ys-accent)]"
                    title="立即播放"
                  >
                    <Play class="w-3.5 h-3.5 fill-current" />
                  </button>
                  <button
                    @click.stop="embedLyricFor(song)"
                    :disabled="embeddingPaths.includes(song.path)"
                    class="grid h-7 w-7 place-items-center rounded-[7px] text-[var(--ys-text-faint)] transition-colors hover:bg-amber-50 hover:text-amber-600 disabled:opacity-50"
                    :title="song.has_lyric ? '已有歌词，点击重新抓取并内嵌进音频标签' : '抓取歌词并内嵌进音频文件标签'"
                  >
                    <Loader2 v-if="embeddingPaths.includes(song.path)" class="w-3.5 h-3.5 animate-spin" />
                    <FileMusic v-else class="w-3.5 h-3.5" />
                  </button>
                  <!--
                    更换封面（用户 2026-09-22：「手动获取音源的封面或者搜索歌曲封面」）。
                    飞牛只认**内嵌**封面，所以这里搜到候选后直接写进文件标签。
                  -->
                  <button
                    @click.stop="openCoverPicker(song)"
                    class="grid h-7 w-7 place-items-center rounded-[7px] text-[var(--ys-text-faint)] transition-colors hover:bg-sky-50 hover:text-sky-600"
                    title="搜索并更换封面（内嵌进音频文件）"
                  >
                    <ImageIcon class="w-3.5 h-3.5" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        </div>
      </div>
    </div>
    </div>

    <!-- ─── 更换封面弹窗 ───
         飞牛只认**内嵌**封面，所以这里搜到候选后直接写进文件标签（POST /api/nas/tags）。
         候选来自多平台搜索（按封面地址去重），也可以直接粘一张图片地址。 -->
    <!--
      ⚠️ **必须 Teleport 到 body**：弹窗在滑动页里，滑轨带 transform，
      fixed 的包含块会变成滑轨而不是视口（手机上会被挤出屏幕）。本项目其它弹窗都这么写。
    -->
    <Teleport to="body">
    <div
      v-if="coverPicker.show"
      class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-xs"
    >
      <div class="bg-white rounded-2xl max-w-2xl w-full p-5 shadow-2xl border border-gray-100 flex flex-col max-h-[85vh]">
        <div class="flex items-start justify-between gap-3 shrink-0">
          <div class="min-w-0">
            <h3 class="text-sm font-bold text-gray-800 flex items-center gap-1.5">
              <ImageIcon class="w-4 h-4 text-sky-500" />
              <span>更换封面</span>
            </h3>
            <p class="mt-0.5 truncate text-[11px] text-gray-400" :title="coverPicker.song?.path">
              {{ coverPicker.song?.title || coverPicker.song?.filename }}
            </p>
          </div>
          <button @click="coverPicker.show = false" class="text-gray-400 hover:text-gray-600">
            <X class="w-4 h-4" />
          </button>
        </div>

        <!-- 搜索词 / 自定义图片地址 -->
        <div class="mt-3 flex shrink-0 items-center gap-2">
          <input
            v-model="coverPicker.query"
            @keyup.enter="searchCovers"
            type="text"
            placeholder="改关键词再搜，或直接粘贴一张图片地址"
            class="flex-1 rounded-lg border border-gray-200 px-2.5 py-1.5 text-xs outline-none focus:border-sky-400"
          />
          <button
            @click="searchCovers"
            :disabled="coverPicker.loading || coverPicker.applying"
            class="shrink-0 rounded-lg bg-sky-50 px-3 py-1.5 text-xs font-medium text-sky-600 hover:bg-sky-100 disabled:opacity-50"
          >
            {{ coverQueryIsUrl ? '用这个地址' : (coverPicker.loading ? '搜索中…' : '搜索封面') }}
          </button>
        </div>

        <InlineNotice v-if="coverPicker.error" :text="coverPicker.error" class="mt-2 shrink-0" />

        <div class="mt-3 flex-1 overflow-y-auto">
          <div v-if="coverPicker.loading" class="flex items-center justify-center py-10 text-xs text-gray-400">
            <Loader2 class="mr-2 h-4 w-4 animate-spin" />正在各平台搜索封面…
          </div>
          <div v-else-if="!coverPicker.candidates.length" class="py-10 text-center text-xs text-gray-400">
            没搜到封面 —— 可以把关键词改成「歌名 歌手」再搜，或直接粘贴一张图片地址。
          </div>
          <div v-else class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <button
              v-for="c in coverPicker.candidates"
              :key="c.cover_url"
              @click="applyCover(c.cover_url)"
              :disabled="coverPicker.applying"
              class="overflow-hidden rounded-xl border border-gray-100 bg-gray-50 p-1.5 text-left transition-all hover:border-sky-300 hover:shadow-sm disabled:opacity-50"
              :title="`${c.name || ''} ${c.artist || ''}`.trim() || c.cover_url"
            >
              <img
                :src="c.cover_url"
                referrerpolicy="no-referrer"
                class="aspect-square w-full rounded-lg bg-white object-cover"
              />
              <p class="mt-1 truncate text-[10px] text-gray-500">{{ c.artist || c.source || '候选封面' }}</p>
            </button>
          </div>
        </div>

        <div class="mt-3 flex shrink-0 items-center justify-between gap-2 border-t border-gray-50 pt-3">
          <p class="text-[11px] text-gray-400">
            {{ coverPicker.applying ? '正在写入文件…' : '只有 MP3 / FLAC 能内嵌封面；其它格式会提示写入失败。' }}
          </p>
          <button
            @click="coverPicker.show = false"
            class="rounded-lg px-3 py-1.5 text-xs text-gray-500 hover:bg-gray-100"
          >
            关闭
          </button>
        </div>
      </div>
    </div>

    <!-- ─── Interactive Folder Selector Modal ─── -->
    <div
      v-if="showFolderModal"
      class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-xs"
    >
      <div class="bg-white rounded-2xl max-w-xl w-full p-6 shadow-2xl border border-gray-100 flex flex-col max-h-[82vh]">
        <!-- Modal Top Bar -->
        <div class="flex items-center justify-between pb-4 border-b border-gray-100 shrink-0">
          <div>
            <h3 class="text-base font-bold text-gray-800 flex items-center gap-2">
              <FolderOpen class="w-4 h-4 text-emerald-500" />
              <span>选择 NAS 曲库目录</span>
            </h3>
            <p class="text-xs text-gray-400 mt-0.5">点击进入子文件夹，或点击右侧按钮直接选用</p>
          </div>
          <button @click="showFolderModal = false" class="p-1 rounded-lg hover:bg-gray-100 text-gray-400 hover:text-gray-600">
            <X class="w-5 h-5" />
          </button>
        </div>

        <!-- Volume Switcher Tabs -->
        <div class="py-3 border-b border-gray-100 flex items-center gap-2 overflow-x-auto shrink-0 text-xs">
          <span class="text-gray-400 shrink-0 font-medium">存储卷:</span>
          <button
            v-for="vol in modalVolumes"
            :key="vol"
            @click="modalDrillDown(vol)"
            :class="[
              'px-3 py-1 rounded-lg font-mono transition-all flex items-center gap-1',
              modalCurrentPath.startsWith(vol)
                ? 'bg-emerald-500 text-white font-semibold shadow-xs'
                : 'bg-gray-100 hover:bg-gray-200 text-gray-600'
            ]"
          >
            <HardDrive class="w-3 h-3" />
            <span>{{ vol }}</span>
          </button>
        </div>

        <!-- Path Breadcrumb Bar & Back to Parent Button -->
        <div class="py-2.5 px-3 bg-gray-50 rounded-xl my-3 flex items-center justify-between gap-2 shrink-0 text-xs">
          <div class="flex items-center gap-1 overflow-x-auto font-mono text-gray-600">
            <span class="text-gray-400">路径:</span>
            <button
              v-for="(crumb, idx) in modalBreadcrumbs"
              :key="idx"
              @click="modalDrillDown(crumb.path)"
              class="px-1 py-0.5 rounded hover:bg-white hover:text-emerald-600 transition-colors"
            >
              {{ crumb.name }}
              <span v-if="idx < modalBreadcrumbs.length - 1" class="text-gray-300 ml-1">/</span>
            </button>
          </div>

          <button
            v-if="modalParentPath"
            @click="modalDrillDown(modalParentPath)"
            class="px-2.5 py-1 rounded-lg bg-white border border-gray-200 hover:bg-gray-100 text-gray-600 text-[11px] font-medium flex items-center gap-1 shrink-0 transition-colors"
            title="返回上一级"
          >
            <CornerLeftUp class="w-3 h-3 text-gray-500" />
            <span>上一级</span>
          </button>
        </div>

        <!-- Folder List Area -->
        <div class="flex-1 overflow-y-auto divide-y divide-gray-50 pr-1 min-h-[220px]">
          <!-- Loading -->
          <div v-if="modalLoading" class="h-44 flex flex-col items-center justify-center text-gray-400">
            <RotateCw class="w-6 h-6 text-emerald-500 animate-spin mb-2" />
            <span class="text-xs">正在读取目录内容...</span>
          </div>

          <!-- Empty subfolders -->
          <div v-else-if="!modalFolders.length" class="h-44 flex flex-col items-center justify-center text-gray-400">
            <Folder class="w-8 h-8 text-gray-300 mb-2" />
            <p class="text-xs text-gray-500">当前目录下无子文件夹</p>
            <p v-if="modalAudioCount > 0" class="text-xs text-emerald-600 mt-1 font-medium">
              ★ 发现当前目录包含 {{ modalAudioCount }} 首音乐文件！
            </p>
          </div>

          <!-- Folder rows -->
          <div
            v-for="f in modalFolders"
            :key="f.path"
            @click="modalDrillDown(f.path)"
            class="py-2.5 px-3 flex items-center justify-between hover:bg-emerald-50/50 rounded-xl cursor-pointer transition-colors group"
          >
            <div class="flex items-center gap-2.5 overflow-hidden">
              <Folder class="w-4 h-4 text-emerald-500 shrink-0 group-hover:scale-110 transition-transform" />
              <span class="text-xs font-semibold text-gray-700 truncate group-hover:text-emerald-700 transition-colors">
                {{ f.name }}
              </span>
              <span
                v-if="f.audio_count > 0"
                class="text-[10px] px-1.5 py-0.5 rounded-full bg-emerald-50 text-emerald-600 border border-emerald-200 font-mono shrink-0"
              >
                🎵 {{ f.audio_count }} 首歌曲
              </span>
            </div>

            <div class="flex items-center gap-1 shrink-0" @click.stop>
              <button
                @click="confirmSelection(f.path)"
                class="px-2.5 py-1 rounded-lg bg-emerald-50 hover:bg-emerald-500 hover:text-white text-emerald-600 text-[11px] font-medium transition-all"
              >
                选择此目录
              </button>
              <button
                @click="modalDrillDown(f.path)"
                class="p-1 rounded-lg text-gray-300 hover:text-gray-600 transition-colors"
                title="进入子目录"
              >
                <ChevronRight class="w-4 h-4" />
              </button>
            </div>
          </div>
        </div>

        <!-- Modal Footer -->
        <div class="pt-4 mt-2 border-t border-gray-100 flex items-center justify-between shrink-0">
          <div class="text-xs text-gray-500 truncate max-w-xs">
            当前选中：<span class="font-mono font-semibold text-emerald-700">{{ modalCurrentPath }}</span>
          </div>
          <div class="flex items-center gap-2">
            <button
              @click="showFolderModal = false"
              class="px-4 py-2 rounded-xl text-xs text-gray-500 hover:bg-gray-100 transition-colors"
            >
              取消
            </button>
            <button
              @click="confirmSelection(modalCurrentPath)"
              class="px-5 py-2 rounded-xl bg-emerald-500 hover:bg-emerald-600 text-white font-semibold text-xs shadow-sm flex items-center gap-1.5 transition-all"
            >
              <Check class="w-3.5 h-3.5" />
              <span>确认选择此目录并扫描</span>
            </button>
          </div>
        </div>
      </div>
    </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { markLoaded, isStale } from '../services/viewCache'
import { currentView, navigateTo } from '../services/navBus'
import { NasAPI, TagsAPI, FnosAPI } from '../api/client'
import { loadPref, savePref, memGet, memSet } from '../services/prefs'
import { sortSongs, parseSortPref, sortPrefOf, nextSortDir } from '../services/librarySort'
import {
  breadcrumbsFor,
  scanCacheKey,
  scanCachePayload,
  scanViewOf,
  scanViewOfCache,
  scanHintOf,
} from '../services/nasView'
import {
  RotateCw, Folder, FolderOpen, Play, X, Check,
  HardDrive, CornerLeftUp, ChevronRight, ChevronUp, ChevronDown, FileMusic, Loader2,
  ShieldAlert, Image as ImageIcon
} from 'lucide-vue-next'
import InlineNotice from './InlineNotice.vue'
import { describeError } from '../services/userMsg'
import { logError } from '../services/appLog'

const emit = defineEmits(['play'])

const quickDirs = ref(['/vol1/Music', '/vol1/music', '/vol2/Music', '/vol1'])
const currentDir = ref(loadPref('nas_dir', '/vol1/Music'))
const songs = ref([])

// ── 列表排序（2026-10-24 用户：「曲库里面已下载的歌曲，按照最新排序，或者帮我增加列排序选项」）──
// 判据全部在 services/librarySort.js（纯函数 + node:test 回归），这里只做三件事：
// 记住用户点的是哪一列、把有序结果喂给表格与播放列表、在表头画出当前方向。
// 默认「最新」= updated_at 倒序，理由很直白：枚举文件系统的顺序跟下载先后毫无关系，
// 用户想看的永远是「刚下的那几首在哪」。
const sortState = ref(parseSortPref(loadPref('nas_sort', '')))
const sortKey = computed(() => sortState.value.key)
const sortDir = computed(() => sortState.value.dir)
// 表格与「播放全部 / 播放某首」必须共用同一份有序列表：否则界面第 1 行点了播放，
// 播放器拿到的仍是后端原始顺序，实际播的是另一首（这是最容易埋进去的坑）。
const sortedSongs = computed(() => sortSongs(songs.value, sortState.value.key, sortState.value.dir))

function persistSort() {
  savePref('nas_sort', sortPrefOf(sortState.value))
}

/** 点表头：同一列切升降序，换列则用该列默认方向。 */
function toggleSort(key) {
  const dir = sortKey.value === key ? nextSortDir(key, sortDir.value) : nextSortDir(key, undefined)
  sortState.value = { key, dir }
  persistSort()
}

/** 「#」列头：回到默认的「最新」；也是切到其它列后唯一的回程入口。 */
function sortByNewest() {
  sortState.value = { key: 'newest', dir: nextSortDir('newest', undefined) }
  persistSort()
}

/** 给 <th aria-sort> 用（读屏器靠它知道当前按哪列、哪个方向）。 */
function ariaSortOf(key) {
  if (sortKey.value !== key) return 'none'
  return sortDir.value === 'asc' ? 'ascending' : 'descending'
}
const isScanning = ref(false)
// 枚举是否被截断（数量上限/深度上限）：后端 /api/nas/scan 现在会如实返回
// truncated 与 warnings，界面据此提示「还有没列出来的」，不再假装这就是全部。
const scanTruncated = ref(false)
// 后端给的原因（数组，原样保留）；模板那一行提示交给 scanHintOf 算
const scanWarnings = ref([])
const scanHint = computed(() => scanHintOf({ truncated: scanTruncated.value, warnings: scanWarnings.value }))

// ── 歌词内嵌状态 ──
const embeddingPaths = ref([])
const embedNotice = ref('')
/** 上一条提示是成功还是失败（决定配色，失败时带「查看日志」入口） */
const embedOk = ref(true)
const isBatchEmbedding = ref(false)
let noticeTimer = null

function showEmbedNotice(text, ok = true) {
  embedOk.value = ok
  embedNotice.value = text
  if (noticeTimer) clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => { embedNotice.value = '' }, 4000)
}

/**
 * 为单首本地歌曲抓取歌词并写入音频标签。
 * 优先使用同名 .lrc / 已内嵌歌词；都没有时按「歌手 + 歌名」联网抓取。
 */
async function embedLyricFor(song) {
  if (embeddingPaths.value.includes(song.path)) return false
  embeddingPaths.value = [...embeddingPaths.value, song.path]
  try {
    let lyric = ''
    // 1. 该歌曲在 NAS 上已有的歌词（同名 .lrc 或内嵌标签）
    try {
      const nasLyric = await NasAPI.lyric(song.path)
      if (nasLyric?.code === 200 && nasLyric?.data?.lyric) lyric = nasLyric.data.lyric
    } catch (_) {}

    // 2. 写入标签；本地无歌词时按歌名/歌手联网抓取
    const res = await TagsAPI.write({
      path: song.path,
      title: song.title || song.filename || '',
      artist: song.artist || '',
      album: song.album || '',
      lyric,
      fetch: !lyric,
      source: song.source || '',
      songmid: song.songmid || song.id || '',
      duration: song.duration || song.interval || 0,
      write_lrc: true,
    })

    if (res?.code === 200) {
      song.has_lyric = true
      return true
    }
    // 逐首失败原因不进界面（界面只报「失败 N 首」），进日志供排查
    logError('内嵌歌词', `${song.title || song.filename || song.path} 写入失败`, `code=${res?.code} message=${res?.message}`)
    return false
  } catch (e) {
    logError('内嵌歌词', `${song.title || song.filename || song.path} 写入异常`, describeError(e))
    return false
  } finally {
    embeddingPaths.value = embeddingPaths.value.filter(p => p !== song.path)
  }
}

// ── 更换封面 ──
//
// 为什么要有它：飞牛**只认内嵌封面**，而「曲库补全」是「搜到哪首就用哪首的图」，
// 用户没得挑（2026-09-22 反馈：「手动获取音源的封面或者搜索歌曲封面」）。
// 这里把候选图列出来让人自己选，选定后走 POST /api/nas/tags 内嵌进文件。
const coverPicker = ref({
  show: false, song: null, query: '', candidates: [], loading: false, applying: false, error: '',
})

/** 输入框里是不是一个图片地址（是就直接用它，不再去搜索） */
const coverQueryIsUrl = computed(() => /^https?:\/\//i.test(String(coverPicker.value.query || '').trim()))

function openCoverPicker(song) {
  const base = String(song?.title || song?.filename || '').replace(/\.[a-z0-9]{1,5}$/i, '')
  coverPicker.value = {
    show: true, song, candidates: [], loading: false, applying: false, error: '',
    query: [base, song?.artist].filter(Boolean).join(' '),
  }
  searchCovers()
}

async function searchCovers() {
  const st = coverPicker.value
  const q = String(st.query || '').trim()
  if (!q || st.loading || st.applying) return
  if (/^https?:\/\//i.test(q)) return applyCover(q)
  st.loading = true
  st.error = ''
  try {
    const res = await TagsAPI.coverCandidates({
      keyword: q,
      // 歌名/歌手当作**排序提示**给后端：原曲（名字+歌手都对得上）会排到最前，
      // 翻唱/伴奏仍留在后面可选 —— 实测只按平台顺序取会满屏翻唱。
      name: st.song?.title || st.song?.filename || '',
      artist: st.song?.artist || '',
      limit: 8,
    })
    if (res?.code !== 200) {
      st.error = res?.message || '搜索封面失败'
      return
    }
    st.candidates = res?.data?.candidates || []
  } catch (e) {
    st.error = describeError(e)
  } finally {
    st.loading = false
  }
}

async function applyCover(url) {
  const st = coverPicker.value
  if (!st.song || !url || st.applying) return
  st.applying = true
  st.error = ''
  try {
    const res = await TagsAPI.write({ path: st.song.path, cover: url })
    if (res?.code !== 200) {
      st.error = res?.message || '写入封面失败'
      return
    }
    // 后端把原因放在 cover_error（例如「不是图片」「格式不支持」）——
    // 它**不阻塞**标签写入，所以 code 仍是 200，得单独看这个字段。
    if (res?.data && res.data.cover_error) {
      st.error = '封面没写进去：' + res.data.cover_error
      logError('更换封面', `${st.song.title || st.song.filename || st.song.path} 写入失败`, res.data.cover_error)
      return
    }
    // 成功：立刻换掉这一行的缩略图。后端已让扫描缓存失效，
    // 再加个时间戳参数强制浏览器重新取图（否则会拿缓存的旧图）。
    st.song.cover_url = `/api/nas/cover?path=${encodeURIComponent(st.song.path)}&v=${Date.now()}`
    st.show = false
    // 飞牛按 mtime/size 决定要不要重读，排一次重扫让它尽快看到新封面
    try { await FnosAPI.rescan() } catch (_) {}
  } catch (e) {
    st.error = describeError(e)
  } finally {
    st.applying = false
  }
}

/** 批量内嵌当前列表全部歌曲（并发 2） */
async function embedAllLyrics() {
  if (!songs.value.length || isBatchEmbedding.value) return
  isBatchEmbedding.value = true
  let ok = 0
  let fail = 0
  const queue = [...songs.value]
  const workers = Array.from({ length: Math.min(2, queue.length) }, async () => {
    while (queue.length) {
      const song = queue.shift()
      if (await embedLyricFor(song)) ok++
      else fail++
    }
  })
  await Promise.all(workers)
  isBatchEmbedding.value = false
  showEmbedNotice(
    `批量内嵌歌词完成：成功 ${ok} 首${fail ? `，失败 ${fail} 首` : ''}`,
    fail === 0,
  )
}

// Folder Selector Modal state
const showFolderModal = ref(false)
const modalCurrentPath = ref('/vol1')
const modalParentPath = ref('')
const modalVolumes = ref(['/vol1', '/vol2'])
const modalFolders = ref([])
const modalLoading = ref(false)
const modalAudioCount = ref(0)

const breadcrumbs = computed(() => breadcrumbsFor(currentDir.value, '/vol1/Music'))
const modalBreadcrumbs = computed(() => breadcrumbsFor(modalCurrentPath.value, '/vol1'))

function formatSize(bytes) {
  if (!bytes) return '0 B'
  const mb = bytes / (1024 * 1024)
  if (mb > 1024) return (mb / 1024).toFixed(2) + ' GB'
  return mb.toFixed(1) + ' MB'
}

async function loadDirectories() {
  try {
    const res = await NasAPI.directories()
    if (res.code === 200 && res.data?.length) {
      quickDirs.value = res.data
      if (!loadPref('nas_dir', '')) {
        currentDir.value = res.data[0]
      }
    }
  } catch (err) {
    // 以前只 console.warn —— 用户在界面上只会看到「没有目录」，无从知道是后端挂了
    logError('读取 NAS 目录', '加载目录列表失败', describeError(err))
  }
}

// 扫描结果两层缓存：
// 1) 模块级 Map（本会话内快速命中，切换导航不重扫）；
// 2) 服务端文件缓存（scan_cache.json）：浏览器刷新/重开时由后端直接返回缓存，
//    只有点「刷新」按钮（refresh=1）或目录内容变化（下载/删除）才真正全盘重扫。
// 把一份扫描载荷（响应或缓存）铺到界面上：三样东西永远一起更新。
// 以前缓存里只存 songs，缓存命中那条 return 就不更新提示 ——
// 切回刚才那个被截断的目录时，「还有没列出来的」会消失（缓存把不完整洗成了完整）。
function applyScanView(view) {
  songs.value = view.songs
  scanTruncated.value = view.truncated
  scanWarnings.value = view.warnings
}

async function scanFolder(dir, refresh = false) {
  if (!dir) return

  const cacheKey = scanCacheKey(dir)
  if (!refresh) {
    const cached = memGet(cacheKey)
    if (cached) {
      applyScanView(scanViewOfCache(cached))
      currentDir.value = dir
      return
    }
  }

  isScanning.value = true
  try {
    const res = await NasAPI.scan(dir, refresh)
    if (res.code === 200) {
      const view = scanViewOf(res)
      applyScanView(view)
      memSet(cacheKey, scanCachePayload(view))
      savePref('nas_dir', dir)
    }
  } catch (err) {
    logError('扫描曲库目录', `扫描 ${dir} 失败`, describeError(err))
  } finally {
    isScanning.value = false
  }
}

function switchDir(dir) {
  currentDir.value = dir
  // 记住用户手动选择的目录（含目录选择器里确认的路径）
  savePref('nas_dir', dir)
  scanFolder(dir, false)
}

function openFolderModal() {
  showFolderModal.value = true
  // 回到上次浏览过的位置，而不是每次都从当前曲库根目录重新开始
  modalCurrentPath.value = loadPref('nas_modal_path', currentDir.value || '/vol1')
  loadBrowse(modalCurrentPath.value)
}

async function loadBrowse(targetPath) {
  modalLoading.value = true
  try {
    const res = await NasAPI.browse(targetPath)
    if (res.code === 200 && res.data) {
      modalCurrentPath.value = res.data.current
      modalParentPath.value = res.data.parent || ''
      if (res.data.volumes?.length) {
        modalVolumes.value = res.data.volumes
      }
      modalFolders.value = res.data.folders || []
      modalAudioCount.value = res.data.audio_count || 0
      // 记住目录选择器最后停留的位置
      savePref('nas_modal_path', modalCurrentPath.value)
    }
  } catch (err) {
    logError('浏览目录', `浏览 ${targetPath} 失败`, describeError(err))
  } finally {
    modalLoading.value = false
  }
}

function modalDrillDown(path) {
  loadBrowse(path)
}

function confirmSelection(selectedPath) {
  showFolderModal.value = false
  switchDir(selectedPath)
}

function mapNasSong(song) {
  return {
    id: song.id,
    path: song.path,
    name: song.title || song.filename,
    title: song.title || song.filename,
    singer: song.artist || '本地音乐',
    artist: song.artist || '本地音乐',
    album: song.album || '飞牛 NAS 本地音乐',
    cover: song.cover_url || '',
    streamUrl: NasAPI.streamUrl(song.path),
    source: 'nas',
    format: song.format,
  }
}

function playSong(song, idx) {
  const allFormatted = sortedSongs.value.map(mapNasSong)
  const currentFormatted = mapNasSong(song)
  emit('play', {
    song: currentFormatted,
    playlist: allFormatted,
    index: idx !== undefined ? idx : allFormatted.findIndex(s => s.id === song.id)
  })
}

// 跳到「补全整理 → 体检」，并把当前目录带过去（体检要扫的就是他正在看的这个目录）。
// 走 navBus 而不是自己弹一个对话框：报告 + 修复的完整交互只在 TidyView 里有一份，
// 两个地方各写一套扫描 UI 迟早会不一致。
function openHealthCheck() {
  const dir = (currentDir.value || '').trim()
  // 带上当前目录（一次性 payload）：体检卡片优先用这个，用户点的是「就查我现在看的文件夹」
  navigateTo('tidy', { segment: 'audit', healthDir: dir })
}

function playAllNasSongs() {
  if (!sortedSongs.value.length) return
  const allFormatted = sortedSongs.value.map(mapNasSong)
  emit('play', {
    song: allFormatted[0],
    playlist: allFormatted,
    index: 0
  })
}

onMounted(async () => {
  await loadDirectories()
  await scanFolder(currentDir.value, false)
  markLoaded('nas')
})

// 常驻页面：切回来时如果数据超过 TTL（或下载/整理把它标成过期了）就重读当前目录。
// 只重读目录、不重新深度扫描 —— 扫描是重活，只有用户点按钮才做。
// ⚠️ 2026-10-17：原实现挂在 onActivated 上 —— SwipePager 没有 KeepAlive，那个钩子
// 从来不会触发（死代码）。改成 watch 导航总线的共享 currentView：切回「本地曲库」就查一次。
watch(currentView, (v) => {
  if (v !== 'library') return
  if (!isStale('nas')) return
  loadDirectories()
  loadBrowse(currentDir.value).finally(() => markLoaded('nas'))
})
</script>
