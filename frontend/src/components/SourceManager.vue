<template>
  <PageShell
    :title="musicdlPageOpen ? 'musicdl 音源' : '音源引擎管理'"
    :desc="musicdlPageOpen
      ? 'musicdl 覆盖 56 个音源 —— 逐个启用/停用，并检测哪些真的能出结果'
      : '多音源自动轮询取链 · 连续失败自动熔断降级 · 支持导入自定义第三方音源脚本'"
  >
    <template #title-extra>
      <span class="text-[10px] px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-600 border border-emerald-200 font-mono">已导入 {{ sources.length }} 个</span>
      <span
        v-if="sourceHealth.cooldownCount.value > 0"
        class="text-[10px] px-2 py-0.5 rounded-full bg-rose-50 text-rose-600 border border-rose-200 font-mono"
        title="处于熔断冷却中的音源数量"
      >熔断 {{ sourceHealth.cooldownCount.value }}</span>
    </template>

    <template #actions>
      <button
        v-if="musicdlPageOpen"
        @click="musicdlPageOpen = false"
        class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5">
        <ArrowLeft class="w-3.5 h-3.5" />返回音源管理
      </button>
      <div v-else class="flex gap-2">
        <button @click="testActiveResolution" :disabled="isTesting || !activeSourceId"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all disabled:opacity-50"
          title="对当前激活音源发起一次真实取链测试">
          <PlayCircle :class="['w-3.5 h-3.5 text-blue-500', isTesting ? 'animate-spin' : '']" />
          {{ isTesting ? '测试中...' : '测试解析' }}
        </button>
        <button @click="probeAllSources" :disabled="isPinging || sources.length === 0"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all disabled:opacity-50"
          title="实际发起取链，检测每个音源是否真的可用">
          <Gauge :class="['w-3.5 h-3.5 text-emerald-500', isPinging ? 'animate-spin' : '']" />
          {{ isPinging ? `检测中 ${probeDone}/${sources.length}` : '全部检测' }}
        </button>
        <button v-if="sourceHealth.cooldownCount.value > 0" @click="resetAllHealth"
          class="px-3 py-1.5 rounded-lg bg-rose-50 hover:bg-rose-100 text-xs text-rose-600 flex items-center gap-1.5 transition-all"
          title="清除所有音源的熔断冷却状态">
          <ShieldOff class="w-3.5 h-3.5" />
          重置熔断
        </button>
        <button @click="cleanupDeadSources" :disabled="cleaning || sources.length === 0"
          class="px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5 transition-all disabled:opacity-50"
          :title="`按测速记录点判定：最近 ${DEAD_STREAK} 次连续失败才算失效，一次性移除`">
          <Trash2 :class="['w-3.5 h-3.5 text-amber-500', cleaning ? 'animate-spin' : '']" />
          {{ cleaning ? '清理中…' : '清理失效音源' }}
        </button>
        <button @click="openImportModal"
          class="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold flex items-center gap-1.5 shadow-sm">
          <Plus class="w-3.5 h-3.5" />导入音源
        </button>
      </div>
    </template>

    <!-- ── musicdl 音源管理子页（v2.1.89）──────────────────────────────
         56 个源挤不进首页的卡片，而且「逐个启用/停用 + 单源检测」跟「导入 LX 脚本」
         是两件事 —— 所以做成音源管理里的一页。 -->
    <MusicDLSourcePanel v-if="musicdlPageOpen" @changed="loadMusicDL" />

    <div v-else class="space-y-3">
      <!-- 操作失败：一句原因 + 「查看日志」入口（替代原来的 alert） -->
      <InlineNotice :text="pageError" />
      <!-- 音源解析测试结果：失败时不显示技术报错，只给结论 + 去哪看 -->
      <InlineNotice :text="testMsg" :tone="testOk ? 'ok' : 'error'" :log-link="!testOk" />

      <!-- 清理结果提示 -->
      <div v-if="cleanupMsg" class="rounded-xl border border-gray-200 bg-white px-3.5 py-2.5 text-[11px] text-gray-600">
        {{ cleanupMsg }}
        <button @click="cleanupMsg = ''" class="ml-2 text-gray-400 hover:text-gray-600">关闭</button>
      </div>

      <!-- ── 外挂音源（musicdl）：入口行（v2.1.89 起详情在独立子页）──────
           这一行只留状态与入口 —— 56 个源的开关在子页里，首页放不下。 -->
      <div class="rounded-xl border border-gray-200 bg-white px-4 py-3 flex items-center justify-between gap-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold text-gray-800">musicdl 外挂音源</h3>
            <span :class="['text-[10px] px-1.5 py-0.5 rounded-full border font-mono', musicdlBadge.cls]">{{ musicdlBadge.text }}</span>
            <span v-if="musicdlEnabled" class="text-[10px] font-mono text-gray-400">已启用 {{ musicdlSources.length }} 个源</span>
          </div>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-500">
            覆盖 56 个音源（酷狗 / 酷我 / 咪咕 / B站 / Apple Music / Spotify…），
            与上面的 LX 脚本是两套独立的东西。
          </p>
        </div>
        <button
          @click="musicdlPageOpen = true"
          class="shrink-0 px-3 py-1.5 rounded-lg bg-gray-100 hover:bg-gray-200 text-xs text-gray-600 flex items-center gap-1.5">
          <Settings2 class="w-3.5 h-3.5" />管理音源
        </button>
      </div>

      <!-- 导入结果条：关掉弹窗也能看到刚才那批音源测成什么样 -->
      <div v-if="importNotice" class="rounded-xl border border-emerald-200 bg-emerald-50 px-3.5 py-2.5 text-[11px] text-emerald-800 flex items-start justify-between gap-2">
        <span class="leading-relaxed">导入结果 · {{ importNotice }}</span>
        <button @click="importNotice = ''" class="shrink-0 text-emerald-600 hover:text-emerald-800">关闭</button>
      </div>

      <!-- 常驻合规与免责声明条 -->
      <div class="p-3 bg-amber-50/80 rounded-xl border border-amber-200/80 text-amber-900 text-xs flex items-center justify-between gap-2 shadow-2xs">
        <div class="flex items-center gap-2 min-w-0">
          <ShieldAlert class="w-4 h-4 text-amber-600 shrink-0" />
          <span class="text-[11px] leading-relaxed truncate">
            <strong>技术中立免责声明：</strong>本播放器为纯本地技术容器，不内置亦不提供任何在线音源。请确保您导入的第三方脚本符合相关法律法规与上游服务协议，仅限个人技术研究使用。
          </span>
        </div>
        <button
          @click="$emit('open-disclaimer')"
          class="shrink-0 text-emerald-700 hover:text-emerald-800 text-[11px] font-semibold underline underline-offset-2 ml-2 cursor-pointer"
        >
          查看完整条款
        </button>
      </div>

      <!--
        协议总览（改任何「音源能力」相关逻辑前先读这块）
        本项目支持两种互补的协议，不是替代关系：
          · 官方协议 —— 后端内置，免脚本，负责 搜索 / 榜单 / 歌单 / 歌词
          · LX 协议  —— 浏览器沙箱执行用户脚本，负责 取直链
        数据来自 GET /api/protocols（后端 pkg/protocol），此处只做展示。
      -->
      <div v-if="protocols.length" class="rounded-2xl border border-gray-200 bg-white p-4">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Layers class="w-4 h-4 text-emerald-500" />
            <span class="text-xs font-semibold text-gray-700">音源协议</span>
            <span class="rounded-full border border-gray-200 bg-gray-50 px-2 py-0.5 font-mono text-[10px] text-gray-500">
              {{ protocols.length }} 种
            </span>
          </div>
          <span v-if="protocolSummary" class="text-[11px] leading-relaxed text-gray-400">{{ protocolSummary }}</span>
        </div>

        <!--
          音源协议卡片：**手机也一行两个**（用户 2026-09-30）。
          折叠只藏**文字解释**（说明段 + 备注段）—— 用户 2026-09-30 反馈
          「折叠的时候显示的信息太少了，只需要隐藏文字解释就好了的」：
          所以协议 id / 后端内置还是浏览器沙箱 / 能力标签 / 平台列表 **折叠时也一直显示**，
          只有那两段长文字要点开才出现。
        -->
        <div class="grid grid-cols-2 gap-3">
          <div
            v-for="p in protocols" :key="p.id"
            class="cursor-pointer rounded-xl border p-3.5 transition-all hover:shadow-sm"
            :class="p.ready ? 'border-emerald-200 bg-emerald-50/40' : 'border-gray-200 bg-gray-50'"
            @click="toggleProtocol(p.id)"
            :title="expandedProtocols.includes(p.id) ? '点击收起' : '点击展开全部信息'"
          >
            <!-- 折叠时也一定可见：名称 + 就绪状态 -->
            <div class="flex items-center gap-1.5">
              <b class="min-w-0 flex-1 truncate text-sm text-gray-800" :title="p.name">{{ p.name }}</b>
              <span
                class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
                :class="p.ready ? 'bg-emerald-500 text-white' : 'bg-gray-200 text-gray-500'"
              >{{ p.ready ? '已就绪' : '未就绪' }}</span>
              <ChevronDown
                class="h-3.5 w-3.5 shrink-0 text-gray-400 transition-transform"
                :class="expandedProtocols.includes(p.id) ? 'rotate-180' : ''"
              />
            </div>

            <!-- 协议 id + 执行位置：折叠时也显示（是"这协议在哪跑"的关键信息） -->
            <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
              <span class="rounded bg-white px-1.5 py-0.5 font-mono text-[10px] text-gray-400 border border-gray-200">{{ p.id }}</span>
              <span v-if="p.location === 'backend'" class="rounded bg-blue-50 px-1.5 py-0.5 text-[10px] font-medium text-blue-600">后端内置</span>
              <span v-else class="rounded bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-700">浏览器沙箱</span>
            </div>

            <!-- 能力标签：折叠时也显示 -->
            <div class="mt-2 flex flex-wrap gap-1">
              <span
                v-for="c in p.capabilities" :key="c"
                class="rounded-md bg-white px-1.5 py-0.5 font-mono text-[10px] text-gray-500 border border-gray-200"
              >{{ capabilityLabel(c) }}</span>
            </div>

            <!-- 平台列表：折叠时也显示 -->
            <div class="mt-2 flex flex-wrap items-center gap-1 text-[10px] text-gray-400">
              <span>平台：</span>
              <span v-for="pf in p.platforms" :key="pf" class="font-mono">{{ pf }}</span>
              <span v-if="p.id === 'lx' && p.source_count !== undefined" class="ml-1">· 已导入 {{ p.source_count }} 个</span>
            </div>

            <!-- ⬇️ 折叠时**唯一**藏起来的东西：两段文字解释 -->
            <template v-if="expandedProtocols.includes(p.id)">
              <p class="mt-2 text-[11px] leading-relaxed text-gray-500">{{ p.description }}</p>
              <p v-if="p.notes" class="mt-2 text-[10px] leading-relaxed text-gray-400">{{ p.notes }}</p>
            </template>
          </div>
        </div>
      </div>

      <!-- 空状态提示 (纯播放器，0 内置音源) -->
      <!-- ══ 按服务地址接入的音源（服务型：后端直连，不执行第三方 JS）══
           注意：这块必须放在下面 sources 的 v-if/v-else 之前，
           否则会把那一对 v-if/v-else 拆开、让 v-else 挂到这里来。 -->
      <div v-if="apiSources.length" class="rounded-2xl border border-blue-200/70 bg-blue-50/40 p-4">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Server class="w-4 h-4 text-blue-600" />
            <span class="text-[13px] font-semibold text-gray-700">服务型音源</span>
            <span class="text-[11px] text-gray-400">后端直连，不需要浏览器执行脚本</span>
          </div>
          <button @click="openImportModal"
            class="text-[11px] font-semibold text-blue-600 hover:underline">+ 再接入一个</button>
        </div>
        <p class="mb-3 text-[11px] leading-relaxed text-gray-500">
          启用的服务型音源会被用于<b class="font-semibold text-gray-600">后台自动下载</b>：
          订阅的歌单有新歌时自动取链落盘，不用开着网页。
        </p>
        <div class="space-y-2">
          <div v-for="a in apiSources" :key="a.id"
               class="flex flex-wrap items-center gap-3 rounded-xl border border-gray-200 bg-white px-3.5 py-2.5">
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <b class="truncate text-[13px] text-gray-800">{{ a.name }}</b>
                <span class="rounded-full bg-blue-50 px-2 py-0.5 text-[10px] font-medium text-blue-600">
                  {{ a.label || a.protocol }}
                </span>
                <span v-if="a.has_token" class="rounded-full bg-gray-100 px-2 py-0.5 text-[10px] text-gray-500">已配令牌</span>
                <span v-if="!a.enabled" class="rounded-full bg-gray-100 px-2 py-0.5 text-[10px] text-gray-400">已停用</span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[11px] text-gray-400">{{ a.base_url }}</p>
              <p v-if="a.note" class="mt-0.5 truncate text-[11px] text-emerald-600">{{ a.note }}</p>
            </div>
            <button @click="removeApiSource(a)"
              class="shrink-0 rounded-lg p-1.5 text-gray-300 transition-colors hover:bg-rose-50 hover:text-rose-500"
              title="移除该音源">
              <Trash2 class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      </div>

      <div v-if="sources.length === 0" class="flex flex-col items-center justify-center py-16 text-center bg-white rounded-2xl border border-gray-100 p-6 shadow-2xs">
        <div class="w-14 h-14 rounded-2xl bg-gray-100 flex items-center justify-center text-2xl mb-3 text-gray-400">
          📦
        </div>
        <h3 class="text-base font-semibold text-gray-700 mb-1">暂无已导入音源</h3>
        <p class="text-xs text-gray-400 max-w-sm mb-5 leading-relaxed px-4">
          官方协议已可用：搜索、榜单、歌单、歌词都不需要音源。若还要<b>播放</b>，请点击下方按钮导入一份
          LX 音源脚本（支持网络 URL 或本地 .js 文件）用于解析直链。
        </p>
        <button @click="openImportModal"
          class="px-4 py-2 rounded-xl bg-emerald-500 hover:bg-emerald-600 text-white text-xs font-semibold flex items-center gap-1.5 shadow-sm transition-all">
          <Plus class="w-4 h-4" />导入第三方音源
        </button>
      </div>

      <!--
        音源卡片（2026-09-30 重构，用户要求）：
          · **多选**：点卡片 = 启用/停用，可同时启用多个（去掉独立的「启用/当前激活」按钮）；
          · 左图标：未启用 = 耳机；已启用 = 耳机**中间加一个单音符**；
          · 右边只放「测速 / 删除」，测速按钮未测过显示图标、测过显示延迟数字；
          · 平台标签下面一排 10 个小长条 = 最近 10 次测速记录（左旧右新，颜色按延迟分档）。
      -->
      <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-3.5">
        <div v-for="s in sources" :key="s.id"
          :class="[
            'bg-white rounded-xl border p-4 flex items-center gap-3 transition-all cursor-pointer hover:border-emerald-300 hover:shadow-sm',
            isActive(s.id) ? 'border-emerald-400 shadow-sm ring-1 ring-emerald-400/20' : 'border-gray-200',
            !healthOf(s.id).available ? 'opacity-70 border-rose-200' : ''
          ]"
          @click="toggleSource(s.id)"
          :title="isActive(s.id) ? '点击停用这个音源' : '点击启用这个音源（可以同时启用多个）'"
        >
          <!-- 左：耳机；已启用时中间叠一个单音符 -->
          <div class="relative flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border"
               :class="isActive(s.id) ? 'border-emerald-200 bg-emerald-50' : 'border-gray-100 bg-gray-50'">
            <Headphones class="h-6 w-6" :class="isActive(s.id) ? 'text-emerald-600' : 'text-gray-400'" />
            <!-- 位置往右挪一点（用户说偏左）、笔画加粗 -->
            <Music2 v-if="isActive(s.id)" :stroke-width="2.8" class="absolute h-2.5 w-2.5 translate-x-[1.5px] text-emerald-700" />
          </div>

          <!--
            ⚠️ 左列必须 `overflow-hidden`：这是"标签绝不盖住右边按钮"的硬保证。
            2026-09-30 用户反馈过：平台标签会溢出去压住「测速 / 删除」。
          -->
          <div class="min-w-0 flex-1 overflow-hidden">
            <div class="flex items-center gap-1.5 flex-wrap">
              <span class="min-w-0 max-w-full truncate font-semibold text-sm text-gray-800" :title="s.name">{{ s.name }}</span>
              <span v-if="s.version" class="text-[10px] px-1.5 py-0.5 rounded bg-gray-100 text-gray-400 font-mono">v{{ s.version }}</span>
              <span class="text-[10px] px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 border border-amber-200/80 font-medium">第三方</span>
            </div>
            <!-- 平台标签**必须能换行**：音源支持的平台数量不定，不换行就会横向溢出、压在右边按钮上 -->
            <!--
              ⚠️ 这里原来在平台标签左边显示「作者」（`s.author`）。用户 2026-09-30：
              「平台标签左边的应该是开发者名称？比如 local、六音，不用暂时这个」——
              即：**暂时不显示**，等有真正的开发者名（而不是 dev 里的占位「张三」）再说。
              要恢复就把 `<span>{{ s.author }}</span>` + 竖线加回来。
            -->
            <div v-if="getPlatforms(s.id).length" class="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-400">
              <span v-for="p in getPlatforms(s.id)" :key="p" class="shrink-0 px-1.5 py-0.5 rounded bg-gray-100 text-gray-500 font-mono text-[10px]">{{ p }}</span>
            </div>
            <!-- 健康状态行 -->
            <div class="flex items-center gap-1.5 mt-1.5 text-[10px]">
              <span :class="['px-1.5 py-0.5 rounded font-medium', healthOf(s.id).badgeClass]">
                {{ healthOf(s.id).label }}
              </span>
              <span v-if="healthOf(s.id).rateText" class="text-gray-400">{{ healthOf(s.id).rateText }}</span>
            </div>
            <!-- 最近 10 次测速记录：小长条（圆角），左旧右新，颜色按延迟分档 -->
            <div class="mt-2 flex items-center gap-1" title="最近 10 次测速记录（左旧右新）">
              <span
                v-for="d in probeDots(s.id)" :key="d.key"
                class="h-1.5 w-4 rounded-full transition-colors"
                :class="d.cls" :title="d.title"
              ></span>
            </div>
          </div>

          <!-- 右：测速 + 删除（点这里不要触发卡片的启用/停用） -->
          <div class="flex shrink-0 items-center gap-1" @click.stop>
            <button
              @click="probeOne(s.id)" :disabled="probingIds.includes(s.id)"
              class="flex h-7 min-w-[34px] items-center justify-center rounded-lg px-1.5 font-mono text-[11px] transition-colors disabled:opacity-50"
              :class="probeFailed(s.id)
                ? 'bg-rose-50 text-rose-600 hover:bg-rose-100'
                : (measuredLatency(s.id) ? 'bg-emerald-50 text-emerald-600 hover:bg-emerald-100' : 'text-gray-300 hover:bg-emerald-50 hover:text-emerald-500')"
              :title="'真实取链测速（' + s.name + '）'"
            >
              <Loader2 v-if="probingIds.includes(s.id)" class="w-3.5 h-3.5 animate-spin text-emerald-500" />
              <span v-else-if="measuredLatency(s.id)">{{ measuredLatency(s.id) }}</span>
              <Gauge v-else class="w-3.5 h-3.5" />
            </button>
            <button v-if="!healthOf(s.id).available" @click="resetHealth(s.id)"
              class="p-1 rounded-lg hover:bg-rose-50 text-rose-400 hover:text-rose-600 transition-colors"
              title="重置该音源的熔断状态">
              <ShieldOff class="w-3.5 h-3.5" />
            </button>
            <button @click="deleteSource(s.id)" class="p-1 rounded-lg hover:bg-red-50 text-gray-300 hover:text-red-400 transition-colors" title="删除">
              <Trash2 class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <!--
      ⚠️ **必须 Teleport 到 body**（本项目其它弹窗都这么写，这里原来漏了）。
      弹窗挂在滑动页里，而滑轨 `.flex.h-full` 带 `transform: translate3d(...)` ——
      transform 的祖先会成为 `position: fixed` 的**包含块**，于是 fixed 不再相对视口，
      而是相对那条好几页宽的滑轨：实测手机上弹窗被挤到 left:-256px，只能看见一部分。
      Teleport 到 body 后 fixed 才真正相对视口。
    -->
    <Teleport to="body">
    <div v-if="showImportModal" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/30 backdrop-blur-sm">
      <!-- max-h + overflow-y-auto：结果块最多 8 条 + 已选文件列表叠起来会超出手机视口，
           而面板原来是 overflow:visible 且没有 max-height —— 超出部分既看不见也滚不到。 -->
      <div
        class="bg-white rounded-2xl max-w-lg w-full p-6 shadow-xl border border-gray-100 max-h-[85vh] overflow-y-auto"
        @dragover.prevent
        @drop.prevent="onDrop"
      >
        <div class="flex items-center justify-between mb-4">
          <h3 class="text-base font-bold text-gray-800">导入第三方自定义音源</h3>
          <button @click="showImportModal = false" class="text-gray-400 hover:text-gray-600"><X class="w-5 h-5" /></button>
        </div>
        <p class="text-xs text-gray-500 mb-3">两种接入方式：导入脚本，或填一个自建服务地址</p>

        <!-- 接入方式切换 -->
        <div class="mb-3 flex w-max items-center gap-0.5 rounded-[11px] bg-gray-100 p-1">
          <button
            @click="importMode = 'script'"
            :class="[
              'px-3 py-1.5 rounded-lg text-[12px] font-bold transition-all whitespace-nowrap',
              importMode === 'script' ? 'bg-white text-emerald-600 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            ]"
          >导入脚本</button>
          <button
            @click="importMode = 'api'"
            :class="[
              'px-3 py-1.5 rounded-lg text-[12px] font-bold transition-all whitespace-nowrap',
              importMode === 'api' ? 'bg-white text-emerald-600 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            ]"
          >按服务地址</button>
        </div>

        <!-- 导入失败：一句原因 + 「查看日志」入口（替代原来的 alert） -->
        <InlineNotice :text="importError" class="mb-3" />

        <!-- ══ 模式一：导入脚本 ══ -->
        <template v-if="importMode === 'script'">
        <div class="p-3 bg-amber-50 rounded-xl border border-amber-200/80 mb-3 text-[11px] text-amber-800 leading-relaxed">
          <strong>怎么用：</strong>把音源脚本的直链粘到下面（每行一个），或直接上传本地 <code class="px-1 rounded bg-white/70">.js</code> 文件 —— 一次最多 50 条。
          本应用不内置任何网络音源，<strong>不导入音源就只能搜索和浏览，不能播放与下载</strong>。
        </div>
        <div class="space-y-3">
          <div>
            <label class="block text-xs font-medium text-gray-600 mb-1">
              脚本网络 URL（每行一个）
            </label>
            <textarea
              v-model="importUrls"
              rows="4"
              placeholder="https://raw.githubusercontent.com/.../source-a.js&#10;https://raw.githubusercontent.com/.../source-b.js"
              class="w-full px-3 py-2 rounded-lg bg-gray-50 border border-gray-200 text-gray-800 text-xs font-mono placeholder:text-gray-400 focus:outline-none focus:border-emerald-400 resize-y"
            ></textarea>
            <p class="mt-1 text-[11px] text-gray-400">已识别 {{ pendingUrlCount }} 条链接</p>
          </div>
          <div>
            <label class="block text-xs font-medium text-gray-600 mb-1">或上传本地文件（可多选，也可以直接拖进来）</label>
            <!--
              拖拽落点：**整块**都能接，不只是那个 file input。
              用户会往这块空白里扔文件，只让 input 自己可拖是拦不住的 ——
              更糟的是浏览器默认行为会直接打开被拖进来的文件、离开当前页面。
              dragenter/leave 用计数器抵消子元素冒泡，否则鼠标划过文字就会闪。
            -->
            <div
              data-testid="import-dropzone"
              @dragenter.prevent="onDragEnter"
              @dragover.prevent="onDragOver"
              @drop.prevent.stop="onDrop"
              :class="[
                'rounded-xl border-2 border-dashed p-2 transition-colors',
                isDragging ? 'border-emerald-400 bg-emerald-50/70' : 'border-gray-200 bg-white'
              ]"
            >
              <input type="file" accept=".js,.mjs,.cjs" multiple @change="handleFilesPick"
                class="block w-full text-xs text-gray-500 file:mr-3 file:py-1.5 file:px-3 file:rounded-lg file:border-0 file:text-xs file:font-semibold file:bg-emerald-50 file:text-emerald-700 hover:file:bg-emerald-100 cursor-pointer" />
              <p v-if="isDragging" class="mt-2 text-center text-[11px] font-semibold text-emerald-600">
                松手即导入 · .js / .mjs / .cjs 都可以，一次最多 50 个
              </p>
              <p v-else class="mt-1 text-[10px] leading-relaxed text-gray-400">
                把脚本文件拖到这块区域即可（也可以拖一条脚本直链）；手机上没有拖拽，用上面的按钮。
              </p>
              <!-- 从 NAS 直接挑：飞牛上本来就存着脚本，不必先下载到手机/电脑再上传 -->
              <button
                type="button"
                @click="openNasPicker"
                class="mt-2 flex w-full items-center justify-center gap-1.5 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs font-semibold text-emerald-700 transition-colors hover:bg-emerald-100"
              >
                <Folder class="w-3.5 h-3.5" />从 NAS 选择脚本
              </button>
            </div>
            <!-- 拖进来的东西被拒了要说出来，否则用户以为拖成功了 -->
            <p
              v-if="dropNotice"
              class="mt-1.5 rounded-lg border border-amber-200 bg-amber-50 px-2 py-1.5 text-[11px] leading-relaxed text-amber-800"
            >{{ dropNotice }}</p>
            <!-- 已选文件：本地来的和 NAS 来的都列出来，NAS 的多显示一行来源路径 -->
            <div v-if="pickedFiles.length" class="mt-2 space-y-1">
              <div v-for="f in pickedFiles" :key="f.filename" class="flex items-start gap-1.5 rounded-lg bg-gray-50 px-2 py-1.5">
                <FileCode class="mt-0.5 w-3 h-3 shrink-0 text-emerald-600" />
                <div class="min-w-0 flex-1">
                  <p class="truncate text-[11px] font-medium text-gray-700" :title="f.filename">{{ f.filename }}</p>
                  <p v-if="f.fromNas" class="truncate font-mono text-[10px] text-gray-400" :title="f.fromNas">NAS：{{ f.fromNas }}</p>
                </div>
                <button type="button" @click="pickedFiles = pickedFiles.filter(x => x.filename !== f.filename)"
                  class="shrink-0 text-gray-300 hover:text-rose-500" title="移除">
                  <X class="w-3 h-3" />
                </button>
              </div>
            </div>
          </div>

          <label class="flex items-start gap-2 cursor-pointer">
            <input v-model="autoTestAfterImport" type="checkbox" class="mt-0.5 accent-emerald-500" />
            <span class="text-[12px] text-gray-600">
              导入后自动测试每个音源
              <span class="block text-[11px] text-gray-400">会真实发起一次取链探测，确认脚本可用；不可用的会标出来</span>
            </span>
          </label>

          <!-- 导入结果 -->
          <div v-if="importReport" id="import-report" class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2.5 text-[11px] leading-relaxed">
            <p class="font-medium text-gray-700">
              导入完成：成功 {{ importReport.imported }} 个<span v-if="importReport.skipped"> · 跳过 {{ importReport.skipped }} 个（已存在）</span><span v-if="importReport.failed"> · 失败 {{ importReport.failed }} 个</span>
            </p>
            <ul class="mt-1 space-y-0.5 text-gray-500">
              <li v-for="(r, i) in importReport.items.slice(0, 8)" :key="i" class="truncate">
                <span :class="r.status === 'imported' ? 'text-emerald-600' : r.status === 'skipped' ? 'text-gray-400' : 'text-rose-500'">
                  {{ r.status === 'imported' ? '✓' : r.status === 'skipped' ? '·' : '✗' }}
                </span>
                {{ r.name || r.source }}<span v-if="r.reason" class="text-rose-400"> — {{ r.reason }}</span>
              </li>
            </ul>
            <p v-if="importReport.items.length > 8" class="mt-1 text-gray-400">仅显示前 8 条，共 {{ importReport.items.length }} 条</p>

            <!--
              导入后自动测试：进行中 → 终态结论。
              以前这里只有一行「正在逐个测试…（N/M）」，跑完就停在那儿，**没有"测试完成"的结论**，
              用户看到的就是「导入完成后没有提醒」。现在给终态横幅 + 失败项逐个「再测试」。
            -->
            <div v-if="autoTestAfterImport && importTest" class="mt-2 rounded-lg border px-2.5 py-2"
                 :class="importTest.running ? 'border-sky-200 bg-sky-50'
                   : (importTest.fail.length ? 'border-amber-200 bg-amber-50' : 'border-emerald-200 bg-emerald-50')">
              <p v-if="importTest.running" class="text-[11px] font-medium text-sky-800">
                ⏳ 正在测试新导入的音源…（{{ importProbeDone }}/{{ importTest.total }}）
              </p>
              <template v-else>
                <p class="text-[11px] font-semibold" :class="importTest.fail.length ? 'text-amber-900' : 'text-emerald-800'">
                  ✓ 测试完成：{{ importTest.ok.length }} 个可用<template v-if="importTest.enabledCount">（已自动启用）</template><template v-if="importTest.fail.length"> · {{ importTest.fail.length }} 个不可用</template>
                </p>
                <p v-if="!importTest.fail.length" class="mt-0.5 text-[11px] text-emerald-700">
                  已加入启用池，关掉弹窗就能直接用。
                </p>
                <template v-else>
                  <ul class="mt-1.5 space-y-1">
                    <li v-for="it in importTest.fail" :key="it.id" class="flex items-start justify-between gap-2">
                      <span class="min-w-0">
                        <span class="block truncate text-[11px] text-rose-700">{{ it.name }}</span>
                        <!-- 失败原因必须写在明面上：只给个名字等于没说，用户只能去日志页翻 -->
                        <span v-if="it.reason" class="mt-0.5 block text-[10px] leading-snug text-rose-500/90">{{ it.reason }}</span>
                      </span>
                      <button type="button" @click="retestImportItem(it)" :disabled="probingIds.includes(it.id)"
                              class="shrink-0 rounded-md border border-rose-200 bg-white px-2 py-0.5 text-[11px] text-rose-600 hover:bg-rose-50 disabled:opacity-50">
                        {{ probingIds.includes(it.id) ? '测试中…' : '再测试' }}
                      </button>
                    </li>
                  </ul>
                  <p class="mt-1 text-[11px] text-amber-700">上游可能只是抖了一下 —— 点「再测试」重试，通了会自动启用。</p>
                </template>
              </template>
            </div>
          </div>
        </div>
        </template>

        <!-- ══ 模式二：按服务地址 ══ -->
        <template v-else>
          <div class="p-3 bg-blue-50 rounded-xl border border-blue-200/80 mb-3 text-[11px] text-blue-900/80 leading-relaxed">
            <strong>怎么用：</strong>填上服务地址（需要令牌就一并填上），点「探测」确认能连通即可 —— 协议会自动识别，不用你选。
            适合自建或社区中转服务，<strong>本机和内网地址（如 192.168.x.x、127.0.0.1）都能直接填</strong>。
          </div>
          <div class="space-y-3">
            <div>
              <label class="block text-xs font-medium text-gray-600 mb-1">服务地址</label>
              <input
                v-model="apiBaseUrl"
                type="text"
                placeholder="http://192.168.1.10:8080"
                class="w-full px-3 py-2 rounded-lg bg-gray-50 border border-gray-200 text-gray-800 text-xs font-mono placeholder:text-gray-400 focus:outline-none focus:border-emerald-400"
              />
            </div>
            <!-- 窄屏堆成一列：手机上两列各约 165px，地址/令牌这种长文本挤在这宽度里没法看全 -->
            <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div>
                <label class="block text-xs font-medium text-gray-600 mb-1">令牌（可选）</label>
                <input
                  v-model="apiToken"
                  type="password"
                  placeholder="留空表示无需鉴权"
                  class="w-full px-3 py-2 rounded-lg bg-gray-50 border border-gray-200 text-gray-800 text-xs placeholder:text-gray-400 focus:outline-none focus:border-emerald-400"
                />
              </div>
              <div>
                <label class="block text-xs font-medium text-gray-600 mb-1">名称（可选）</label>
                <input
                  v-model="apiName"
                  type="text"
                  placeholder="默认用服务地址"
                  class="w-full px-3 py-2 rounded-lg bg-gray-50 border border-gray-200 text-gray-800 text-xs placeholder:text-gray-400 focus:outline-none focus:border-emerald-400"
                />
              </div>
            </div>

            <!-- 探测 / 接入结果 -->
            <div v-if="apiMsg" class="rounded-lg border px-3 py-2.5 text-[11px] leading-relaxed"
                 :class="apiOk ? 'border-emerald-200 bg-emerald-50 text-emerald-800' : 'border-rose-200 bg-rose-50 text-rose-700'">
              <p class="font-medium">{{ apiOk ? '✓ 接入成功' : '✗ 接入失败' }}</p>
              <p class="mt-0.5 whitespace-pre-wrap">{{ apiMsg }}</p>
            </div>
          </div>
        </template>
        <div class="mt-5 flex justify-end gap-2">
          <button @click="showImportModal = false" class="px-4 py-2 rounded-lg text-xs text-gray-500 hover:bg-gray-50">关闭</button>

          <button
            v-if="importMode === 'script'"
            @click="submitBatchImport"
            :disabled="isImporting || !hasPendingImport"
            class="px-5 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white font-semibold text-xs shadow-sm disabled:opacity-50"
          >
            {{ isImporting ? '导入中...' : `立即导入${pendingTotal ? `（${pendingTotal} 个）` : ''}` }}
          </button>

          <button
            v-else
            @click="submitApiSource"
            :disabled="isImporting || !apiBaseUrl.trim()"
            class="px-5 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-600 text-white font-semibold text-xs shadow-sm disabled:opacity-50"
          >
            {{ isImporting ? '探测中...' : '探测并接入' }}
          </button>
        </div>
      </div>
    </div>
    </Teleport>

    <!--
      NAS 脚本选择器。同样 **Teleport 到 body**（滑动页里 fixed 会被滑轨的 transform 当成包含块）。
      只列 .js/.mjs —— 后端 /api/nas/browse?files=1&ext=js,mjs 过滤；正文由 /api/nas/read 读回，
      那条接口有扩展名白名单 + 2MB 上限（见 pkg/nas/readfile.go）。
    -->
    <Teleport to="body">
      <div v-if="nasPicker.show" class="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/40 backdrop-blur-sm">
        <div class="flex max-h-[80vh] w-full max-w-lg flex-col rounded-2xl border border-gray-100 bg-white p-5 shadow-xl">
          <div class="flex shrink-0 items-start justify-between gap-3">
            <div class="min-w-0">
              <h3 class="text-sm font-bold text-gray-800">从 NAS 选择音源脚本</h3>
              <p class="mt-0.5 text-[11px] text-gray-400">只列 .js / .mjs 文件；可访问范围与「本地音乐」一致</p>
            </div>
            <button @click="nasPicker.show = false" class="shrink-0 text-gray-400 hover:text-gray-600" title="关闭">
              <X class="w-5 h-5" />
            </button>
          </div>

          <!-- 磁盘 / 根目录切换（v2.1.57）——
               后端 /api/nas/browse 一直在返回 volumes（= AllowedRoots()），之前前端把它丢掉了，
               于是选择器只能看到默认打开的那一个根，其它硬盘进不去。 -->
          <div v-if="nasPicker.volumes.length" class="mt-3 flex shrink-0 flex-wrap items-center gap-1.5">
            <span class="shrink-0 text-[11px] text-gray-400">磁盘</span>
            <button
              v-for="v in nasPicker.volumes" :key="v"
              @click="loadNasDir(v)"
              :title="v"
              :class="[
                'rounded-lg px-2 py-1 font-mono text-[11px] transition-colors',
                isCurrentVolume(v)
                  ? 'bg-emerald-500 text-white'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200'
              ]"
            >{{ v }}</button>
          </div>

          <!-- 当前路径 + 返回上级 -->
          <div class="mt-2 flex shrink-0 items-center gap-2">
            <button
              v-if="nasPicker.parent"
              @click="loadNasDir(nasPicker.parent)"
              class="shrink-0 rounded-lg bg-gray-100 px-2 py-1 text-[11px] text-gray-600 hover:bg-gray-200"
            >← 上级</button>
            <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-gray-500" :title="nasPicker.path">
              {{ nasPicker.path || '（根目录）' }}
            </span>
          </div>

          <div class="mt-2 min-h-0 flex-1 overflow-y-auto rounded-xl border border-gray-100">
            <p v-if="nasPicker.loading" class="px-3 py-6 text-center text-xs text-gray-400">读取中…</p>
            <template v-else>
              <button
                v-for="d in nasPicker.folders" :key="d.path"
                @click="loadNasDir(d.path)"
                class="flex w-full items-center gap-2 border-b border-gray-50 px-3 py-2 text-left transition-colors hover:bg-gray-50"
              >
                <Folder class="w-3.5 h-3.5 shrink-0 text-amber-500" />
                <span class="min-w-0 flex-1 truncate text-xs text-gray-700">{{ d.name }}</span>
                <span v-if="d.audio_count" class="shrink-0 font-mono text-[10px] text-gray-400">{{ d.audio_count }} 首</span>
              </button>
              <button
                v-for="f in nasPicker.files" :key="f.path"
                @click="pickNasFile(f)"
                class="flex w-full items-center gap-2 border-b border-gray-50 px-3 py-2 text-left transition-colors hover:bg-emerald-50"
              >
                <FileCode class="w-3.5 h-3.5 shrink-0 text-emerald-600" />
                <span class="min-w-0 flex-1 truncate text-xs text-gray-700">{{ f.name }}</span>
                <span class="shrink-0 font-mono text-[10px] text-gray-400">{{ formatBytes(f.size) }}</span>
              </button>
              <p v-if="!nasPicker.folders.length && !nasPicker.files.length" class="px-3 py-8 text-center text-xs text-gray-400">
                这个目录里没有子目录，也没有 .js / .mjs 文件
              </p>
            </template>
          </div>
        </div>
      </div>
    </Teleport>
  </PageShell>
</template>

<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { SourcesAPI, ProtocolAPI, NasAPI, ConfigAPI } from '../api/client'
import { lxRuntime } from '../engine/lx-runtime'
import { sourceHealth } from '../services/sourceHealth'
import {
  Plus, Trash2, X, PlayCircle, ShieldAlert, ChevronDown, Folder, FileCode,
  Gauge, ShieldOff, Loader2, Layers, Server,
  Headphones, Music2, ArrowLeft, Settings2
} from 'lucide-vue-next'
import { loadPref, savePref } from '../services/prefs'
import { volumeOf } from '../services/nasView'
import {
  MAX_IMPORT_ITEMS,
  splitScriptNames,
  rejectedText,
  overflowText,
  urlsFromText,
  mergePicked,
} from '../services/sourceImport'
import { isNotReadyError, explainSourceError } from '../engine/source-error'
import { DEAD_STREAK, SWEEP_BUDGET, isDeadByHistory as isDeadByHistoryOf, sweepPlan } from '../services/probePlan'
import PageShell from './PageShell.vue'
import InlineNotice from './InlineNotice.vue'
import MusicDLSourcePanel from './MusicDLSourcePanel.vue'
import { fail, apiError, describeError } from '../services/userMsg'
import { logError, logInfo, logWarn } from '../services/appLog'

const sources = ref([])
/**
 * 启用池（多选激活，用户 2026-09-30）。有序：前面的优先，取链按池内顺序试。
 * `activeSourceId` 保留 = 池首 —— 页面里「一键测试」等旧逻辑只认单值。
 */
const activeSourceIds = ref([])
const activeSourceId = computed(() => activeSourceIds.value[0] || '')

/** 该音源是否在启用池里 */
function isActive(id) { return activeSourceIds.value.includes(id) }

/** 把池广播给 App（池首 = activeSourceMeta，下游不用改） */
function emitPool() {
  const metas = activeSourceIds.value.map(id => sources.value.find(s => s.id === id)).filter(Boolean)
  emit('pool-changed', { ids: [...activeSourceIds.value], metas })
}

// ── 测速记录：每个音源保留最近 10 次（小长条记录点用） ──
const PROBE_HISTORY_MAX = 10
const PROBE_HISTORY_KEY = 'source_probe_history'
function loadProbeHistory() {
  try { return JSON.parse(loadPref(PROBE_HISTORY_KEY, '') || '{}') || {} } catch (_) { return {} }
}
const probeHistory = ref(loadProbeHistory())

/** 记一次测速结果（成功记延迟，失败记 ok:false），只留最近 10 条 */
function recordProbe(id, ok, latencyMs) {
  const rec = { ok: !!ok, ms: ok ? Math.round(latencyMs || 0) : 0, t: Date.now() }
  const list = [...(probeHistory.value[id] || []), rec].slice(-PROBE_HISTORY_MAX)
  probeHistory.value = { ...probeHistory.value, [id]: list }
  savePref(PROBE_HISTORY_KEY, JSON.stringify(probeHistory.value))
}

/**
 * 记录点：**左侧补空**，让"最新一次"永远在最右边。
 *
 * ⚠️ 只有**两种颜色**：成功=绿、失败=红（用户 2026-09-30 明确要求）。
 * 之前按延迟分三档（<400 绿 / <1200 琥珀 / 更高红），结果是"慢但成功"也显示成红色，
 * 用户看到的就是「全是红的」—— 那是把"慢"和"失败"混成了一个颜色。
 */
function probeDots(id) {
  const list = probeHistory.value[id] || []
  const padded = new Array(Math.max(0, PROBE_HISTORY_MAX - list.length)).fill(null).concat(list)
  return padded.map((rec, i) => {
    if (!rec) return { key: 'e' + i, cls: 'bg-gray-100', title: '暂无记录' }
    const when = new Date(rec.t).toLocaleTimeString('zh-CN', { hour12: false })
    if (!rec.ok) return { key: rec.t, cls: 'bg-rose-400', title: `${when} · 取链失败` }
    return { key: rec.t, cls: 'bg-emerald-400', title: `${when} · ${rec.ms}ms` }
  })
}

/** 最近一次测速是不是失败（测速按钮要据此变红） */
function probeFailed(id) {
  return measuredLatency(id) === '异常'
}
const latencies = ref({})
const isPinging = ref(false)
const probeDone = ref(0)
const probingIds = ref([])
const isTesting = ref(false)
// 「一键清理失效音源」的状态
const cleaning = ref(false)
const cleanupMsg = ref('')
const showImportModal = ref(false)
// 批量导入：URL 走多行文本，本地脚本走多选文件
const importUrls = ref('')
const pickedFiles = ref([])          // [{ filename, content }]
const autoTestAfterImport = ref(true)
// 拖拽高亮状态（判定方式见下面的 onDragOver 注释：靠 dragover 心跳，不靠 enter/leave 配对）
const isDragging = ref(false)
let dragHeartbeat = 0
// 拖拽的即时反馈（收下了几个 / 忽略了几个），与提交后的 importReport 分开
const dropNotice = ref('')
const importReport = ref(null)       // { imported, skipped, failed, items }
const importProbeDone = ref(0)       // 导入后自动测试的进度（与「全部检测」的 probeDone 分开）
// 导入后测试的**终态结论**（用户 2026-09-30 要求：导入后要测试 → 自动启用测通的 → 失败的能点「再测试」）
// 形状 { total, ok: [{id,name}], fail: [{id,name}], enabledCount, running }
const importTest = ref(null)
// 关掉弹窗后仍留在页面正文顶部的结论条，避免「导入完没提醒」
const importNotice = ref('')
const isImporting = ref(false)

// ── 从 NAS 选脚本（用户 2026-09-30 要求：上传本地音源增加"nas 上选择"）──
// 只用现有接口：/api/nas/browse?files=1&ext=js,mjs 列目录与脚本，/api/nas/read 读正文。
// 选中的文件塞进 `pickedFiles` —— 与本地文件上传**走同一条导入链路**（含导入后自动测试）。
const nasPicker = ref({ show: false, path: '', parent: '', volumes: [], folders: [], files: [], loading: false })
/** 记住上次浏览/选中的目录 —— 脚本通常都放在同一个地方，别每次都从根目录点起 */
const NAS_SCRIPT_DIR_KEY = 'nas_script_dir'

/**
 * 当前浏览的目录落在哪块盘上（磁盘标签靠它决定高亮哪一个）。
 *
 * 判断逻辑放在 `services/nasView.js`（`volumeOf`）并有单测 —— 这里踩过一次坑：
 * 用 `startsWith` 直接比会让 `/vol10/...` 也算成 `/vol1`，两块盘同时高亮。
 * `volumeOf` 取最长匹配，嵌套根（`/vol1` 与 `/vol1/Music`）时只亮最贴近的那块。
 */
function isCurrentVolume(v) {
  return volumeOf(nasPicker.value.path, nasPicker.value.volumes) === v
}

function formatBytes(n) {
  if (!n) return '0 B'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

async function loadNasDir(path = '') {
  nasPicker.value = { ...nasPicker.value, loading: true }
  try {
    const res = await NasAPI.browse(path, { files: true, ext: 'js,mjs' })
    const d = res.data || {}
    nasPicker.value = {
      show: true, loading: false,
      path: d.current || path, parent: d.parent || '',
      // 后端每次都返回 volumes（= AllowedRoots()），留着做磁盘切换；
      // 万一某次没带上，沿用上一次的，别把切换条弄没了
      volumes: d.volumes || nasPicker.value.volumes || [],
      folders: d.folders || [], files: d.files || [],
    }
    if (d.current) savePref(NAS_SCRIPT_DIR_KEY, d.current)
  } catch (e) {
    nasPicker.value = { ...nasPicker.value, loading: false }
    pageError.value = fail('浏览 NAS 目录', e)
  }
}

function openNasPicker() {
  nasPicker.value = { show: true, path: '', parent: '', volumes: [], folders: [], files: [], loading: true }
  loadNasDir(loadPref(NAS_SCRIPT_DIR_KEY, '') || '')
}

/** 选中一个 NAS 上的脚本 → 读正文 → 并入待导入列表 */
async function pickNasFile(f) {
  try {
    const res = await NasAPI.readText(f.path)
    const bad = apiError(res, '读取脚本失败')
    if (bad) { pageError.value = bad; logError('从 NAS 选脚本', bad, `path=${f.path}`); return }
    const name = res.data?.name || f.name
    pickedFiles.value = mergePicked(pickedFiles.value, [
      { filename: name, content: res.data?.content || '', fromNas: f.path },
    ])
    nasPicker.value = { ...nasPicker.value, show: false }
  } catch (e) {
    pageError.value = fail('读取 NAS 脚本', e)
  }
}

// ── 按服务地址接入（服务型音源）──
const importMode = ref('script')     // 'script' | 'api'
const apiBaseUrl = ref('')
const apiToken = ref('')
const apiName = ref('')
const apiMsg = ref('')
const apiOk = ref(false)

/** 页面级失败提示（替代原来的 alert） */
const pageError = ref('')
/** 导入弹窗内的失败提示 */
const importError = ref('')
/** 音源解析测试结果 */
const testMsg = ref('')
const testOk = ref(false)
const apiSources = ref([])           // 已接入的 API 音源

/** 待导入的 URL 条数（去掉空行与注释行） */
const pendingUrlCount = computed(() =>
  importUrls.value.split('\n').map(s => s.trim()).filter(s => s && !s.startsWith('#')).length,
)
const pendingTotal = computed(() => pendingUrlCount.value + pickedFiles.value.length)
const hasPendingImport = computed(() => pendingTotal.value > 0)
const emit = defineEmits(['source-changed', 'open-disclaimer'])

// ── 音源协议（官方协议 / LX 协议）──
// 由后端 GET /api/protocols 提供，前端只展示，不在本地硬编码能力表。
const protocols = ref([])
const protocolSummary = ref('')

/**
 * 音源协议卡片的展开状态。
 * 用户 2026-09-30 定了两次口径，最终是：**折叠只藏「文字解释」**（说明段 + 备注段），
 * 其余（协议 id / 内置还是沙箱 / 能力标签 / 平台列表）折叠时也一直显示 ——
 * 原话「折叠的时候显示的信息太少了，只需要隐藏文字解释就好了的」。
 */
const expandedProtocols = ref([])
function toggleProtocol(id) {
  expandedProtocols.value = expandedProtocols.value.includes(id)
    ? expandedProtocols.value.filter(x => x !== id)
    : [...expandedProtocols.value, id]
}

/** 能力代号 → 中文标签（后端返回的是稳定代号，展示文案在这里收口） */
const CAPABILITY_LABELS = {
  search: '搜索',
  charts: '榜单',
  playlist: '歌单',
  lyric: '歌词',
  music_url: '取直链',
}
function capabilityLabel(c) {
  return CAPABILITY_LABELS[c] || c
}

async function loadProtocols() {
  try {
    const res = await ProtocolAPI.list()
    if (res?.code === 200) {
      protocols.value = res.data?.protocols || []
      protocolSummary.value = res.data?.summary || ''
    }
  } catch (e) {
    // 协议总览是辅助信息，拉取失败不影响音源管理主流程
    console.warn('[protocols] load failed:', e)
  }
}

// 每秒滴答一次，驱动熔断倒计时刷新
const nowTs = ref(Date.now())
let tickTimer = null


/** 该音源在运行时已注册的平台（未加载则返回空） */
function getPlatforms(id) {
  try {
    return lxRuntime.getPlatforms(id)
  } catch (_) {
    return []
  }
}

/** 汇总单个音源的健康视图（依赖 nowTs 以驱动倒计时刷新） */
function healthOf(id) {
  void nowTs.value
  const available = sourceHealth.isAvailable(id)
  const rate = sourceHealth.successRate(id)
  const rec = sourceHealth.get(id)
  let badgeClass = 'bg-gray-100 text-gray-500'
  let dotClass = 'bg-gray-300'
  if (!available) {
    badgeClass = 'bg-rose-50 text-rose-600'
    dotClass = 'bg-rose-400'
  } else {
    const st = sourceHealth.statusOf(id)
    if (st === 'healthy') {
      badgeClass = 'bg-emerald-50 text-emerald-600'
      dotClass = 'bg-emerald-400'
    } else if (st === 'warning') {
      badgeClass = 'bg-amber-50 text-amber-600'
      dotClass = 'bg-amber-400'
    }
  }
  return {
    available,
    label: sourceHealth.statusLabel(id),
    badgeClass,
    dotClass,
    rateText: rate == null ? '' : `成功率 ${Math.round(rate * 100)}%`,
    lastError: rec?.lastError || '',
  }
}

/**
 * 已测到的延迟数字（没有则空串）。
 * 测速按钮靠它决定显示什么：空 → 显示测速图标；有值 → 直接显示数字（用户要求）。
 */
function measuredLatency(id) {
  const v = latencies.value[id]
  if (v === '异常') return '异常'
  if (v) return v
  const rec = sourceHealth.get(id)
  if (rec?.lastLatencyMs > 0) return `${Math.round(rec.lastLatencyMs)}ms`
  // 再回落到**持久化的测速记录**：刷新页面后 `latencies` 是空的，
  // 但记录点还在 —— 不回落到它就会出现「点是红的、按钮却还是灰色图标」的不一致。
  const hist = probeHistory.value[id]
  const last = hist && hist[hist.length - 1]
  if (last) return last.ok ? `${last.ms}ms` : '异常'
  return ''
}

async function loadSources() {
  try {
    const res = await SourcesAPI.list()
    if (res.code === 200) {
      sources.value = res.data.sources || []
      // 启用池：新字段优先；老后端只给单值 active_id 时降级成单元素池
      const pool = Array.isArray(res.data.active_source_ids) && res.data.active_source_ids.length
        ? res.data.active_source_ids
        : (res.data.active_id ? [res.data.active_id] : [])
      activeSourceIds.value = pool
      emitPool()
      // 池里的源都要把脚本载进运行时（取链要按池顺序挑）
      for (const id of pool) await loadScriptIntoRuntime(id)
    }
  } catch (err) { console.error('Failed to load sources:', err) }
}

async function loadScriptIntoRuntime(sourceId) {
  try {
    const meta = sources.value.find(s => s.id === sourceId)
    if (!meta) return
    const res = await SourcesAPI.getScript(sourceId)
    if (res.code === 200) { await lxRuntime.loadScript(meta, res.data.script); emit('source-changed', meta) }
  } catch (e) { console.warn('Failed to load script:', e) }
}

/**
 * 点卡片 = 切换该音源的启用状态（多选）。
 * 不再有独立的「启用」按钮 —— 用户 2026-09-30 明确要求。
 */
async function toggleSource(id) {
  const turningOn = !isActive(id)
  pageError.value = ''
  try {
    const res = await SourcesAPI.setActive(id, turningOn)
    const bad = apiError(res, turningOn ? '启用失败' : '停用失败')
    if (bad) {
      pageError.value = bad
      logError('切换音源', bad, `code=${res?.code} message=${res?.message}`)
      return
    }
    // 以服务端返回的池为准（顺序就是优先级），别在本地自己拼
    const pool = Array.isArray(res.data?.active_source_ids) ? res.data.active_source_ids : null
    if (pool) activeSourceIds.value = pool
    else if (turningOn) activeSourceIds.value = [...activeSourceIds.value, id]
    else activeSourceIds.value = activeSourceIds.value.filter(x => x !== id)
    if (turningOn) await loadScriptIntoRuntime(id)
    emitPool()
  } catch (e) {
    pageError.value = fail('切换音源', e)
  }
}

/** 音源显示名：找不到就退回 id，别在提示里显示空白 */
function sourceName(id) {
  return sources.value.find(s => s.id === id)?.name || id
}

/**
 * 把音源加进启用池（导入后自动启用用）。
 * 与 toggleSource 的差别：不做 isActive 判断、不往 pageError 里写 ——
 * 自动流程一次可能启用好几个，失败只进日志，不该用红条打断用户。
 * 返回是否真的启用了。
 */
async function enableSource(id) {
  if (isActive(id)) return true
  try {
    const res = await SourcesAPI.setActive(id, true)
    const bad = apiError(res, '启用失败')
    if (bad) {
      logWarn('自动启用音源', `${sourceName(id)} 启用失败`, bad)
      return false
    }
    // 以服务端返回的池为准（顺序就是优先级），别在本地自己拼
    const pool = Array.isArray(res.data?.active_source_ids) ? res.data.active_source_ids : null
    activeSourceIds.value = pool || [...activeSourceIds.value, id]
    await loadScriptIntoRuntime(id)
    emitPool()
    return true
  } catch (e) {
    logWarn('自动启用音源', `${sourceName(id)} 启用异常`, describeError(e))
    return false
  }
}

/** 「还没准备好」这类失败最多试几次（含首次） */
const PROBE_NOT_READY_RETRIES = 3
const PROBE_NOT_READY_BACKOFF_MS = 700
/** 探测回了「还没好」之后，最多等脚本 init 完多久（等到了就不必再睡满退避） */
const PROBE_INIT_WAIT_MS = 5000

/** 最近一次探测的**原始报错**（按音源 id）。卡片上只显示「异常」两个字，原因得留给导入报告用 */
const probeErrors = new Map()

/**
 * 真实探针：实际发起一次取链，并把结果计入熔断/健康度。
 * 返回 true=可用、false=确定失败、null=没探（已在探 / 后台轻量模式撞上初始化窗口）。
 *
 * opts.light：后台巡检用。区别只有三点，都是为了「别让后台把主线程占满」：
 *   · 只试 1 次，不重试（重试是给「用户正等着结果」的导入流程用的）
 *   · 不等脚本 init（那个 waitInited 一次最多 5s，是 29 源压测里卡死 30 秒的元凶）
 *   · 撞上「还没准备好」**不记失败**，否则正常音源会被误判成红点，进而被「清理失效音源」连坐
 */
async function probeOne(id, opts = {}) {
  if (probingIds.value.includes(id)) return null
  const light = !!opts.light
  const meta = sources.value.find(s => s.id === id)
  if (meta && !lxRuntime.hasHandler(id)) {
    await loadScriptIntoRuntime(id)
  }
  probingIds.value = [...probingIds.value, id]
  try {
    // 冷加载的音源是**两段式**的：脚本先异步拉远程配置，这期间它照常注册 handler，
    // 但取链时回一句自己的「服务初始化中，请稍后」。
    // 导入后立刻自动测试正好撞进这个窗口 —— 实测 2026-09-30：一个完全正常的音源
    // 副本被判成「不可用」（日志：星海音乐源 取链失败 | 服务初始化中，请稍后）。
    // 所以对「还没准备好」这一类：先等脚本自己 init 完，再退避重试，而不是直接记失败。
    let res = null
    let lastErr = null
    const attempts = light ? 1 : PROBE_NOT_READY_RETRIES
    for (let attempt = 0; attempt < attempts; attempt++) {
      const platforms = getPlatforms(id)
      const platform = platforms.find(p => p !== 'mg' && p !== 'local') || 'kw'
      res = await lxRuntime.probeSource(id, platform)
      if (res.ok || !isNotReadyError(res.error)) break
      lastErr = res.error
      // 平台列表为空 = 脚本还没 send('inited')，此时盲选 'kw' 本身就不可靠
      // 后台轻量模式不等 —— 这一等就是压测里「检测中 0/29 卡 30 秒」的来源
      if (!light && !platforms.length) await lxRuntime.waitInited(id, PROBE_INIT_WAIT_MS)
      if (light) break
      await new Promise(r => setTimeout(r, PROBE_NOT_READY_BACKOFF_MS))
    }
    if (light && !res.ok && isNotReadyError(res.error)) {
      // 还没准备好 ≠ 坏了：后台这次当作没探过，下一轮巡检再说
      logInfo('音源探测', `${meta?.name || id} 后台巡检跳过（${lastErr}）`)
      return null
    }
    latencies.value = { ...latencies.value, [id]: res.ok ? `${res.latencyMs}ms` : '异常' }
    recordProbe(id, res.ok, res.latencyMs)
    // 卡片上只显示「异常」两个字，具体原因（脚本原始报错）进日志 + 留给导入报告展示
    if (res.ok) probeErrors.delete(id)
    else {
      // probeSource 返回的 error 已经是 explain 过的（见 lx-runtime.js），
      // 这里必须拿 rawError 再翻译一次，否则套娃
      probeErrors.set(id, explainSourceError(res.rawError || res.error))
      logWarn('音源探测', `${meta?.name || id} 取链失败`, res.error)
    }
    return !!res.ok
  } catch (e) {
    latencies.value = { ...latencies.value, [id]: '异常' }
    recordProbe(id, false, 0)
    probeErrors.set(id, explainSourceError(e))
    logError('音源探测', `${meta?.name || id} 探测异常`, describeError(e))
    return false
  } finally {
    probingIds.value = probingIds.value.filter(x => x !== id)
  }
}

/**
 * 一键清理失效音源。
 *
 * 与后端 /api/sources/cleanup 的区别（别搞混）：
 *   · cleanup 清的是**内容重复**的音源（按脚本哈希去重）
 *   · 这里清的是**取不到链**的音源（真实探针判定）
 * 两者解决的问题不同，所以都保留。
 *
 * 探测是串行的：同时打多个音源既容易被风控，也让结果难判断。
 */
/**
 * 判据本身（`DEAD_STREAK` + `isDeadByHistory`）搬去了 `services/probePlan.js`：
 * 后台巡检要「跳过已熔断的源」、这个按钮要「清掉已熔断的源」，两处必须同一个口径，
 * 否则会出现「巡检还在敲它、但按钮说它已经死了」这种自相矛盾。
 * 那边的判据是纯函数，有单测兜着（`probePlan.test.mjs`）。
 */
async function cleanupDeadSources() {
  if (cleaning.value || !sources.value.length) return
  cleanupMsg.value = ''

  const dead = sources.value.filter(s => isDeadByHistoryOf(probeHistory.value, s.id))
  if (!dead.length) {
    cleanupMsg.value = `没有连续失败 ${DEAD_STREAK} 次的音源（判据来自下方的测速记录点）`
    return
  }

  const detail = dead.map((d) => {
    const list = probeHistory.value[d.id] || []
    const last = list[list.length - 1]
    const when = last ? new Date(last.t).toLocaleTimeString('zh-CN', { hour12: false }) : ''
    return `· ${d.name}（最近 ${DEAD_STREAK} 次全失败，最后一次 ${when}）`
  }).join('\n')

  if (!confirm(`以下 ${dead.length} 个音源最近连续 ${DEAD_STREAK} 次测速都失败：\n\n${detail}\n\n确定移除吗？（此操作不可撤销）`)) {
    cleanupMsg.value = `已判定 ${dead.length} 个连续失败的音源，但未移除`
    return
  }

  cleaning.value = true
  try {
    const res = await SourcesAPI.batchDelete(dead.map(d => d.id))
    if (res?.code === 200) {
      cleanupMsg.value = `已移除 ${dead.length} 个失效音源`
      await loadSources()
      emit('source-changed')
    } else {
      cleanupMsg.value = '移除失败：' + apiError(res, '未知原因')
    }
  } catch (e) {
    cleanupMsg.value = '清理出错：' + fail('清理失效音源', e)
  } finally {
    cleaning.value = false
  }
}

/** 全部检测：并发上限 2，逐个真实取链 */
async function probeAllSources() {
  isPinging.value = true
  probeDone.value = 0
  const queue = [...sources.value]
  const workers = Array.from({ length: Math.min(2, queue.length) }, async () => {
    while (queue.length) {
      const s = queue.shift()
      await probeOne(s.id)
      probeDone.value++
    }
  })
  await Promise.all(workers)
  isPinging.value = false
}

async function testActiveResolution() {
  if (!activeSourceId.value) {
    testOk.value = false
    testMsg.value = '请先选择并启用一个音源'
    return
  }
  isTesting.value = true
  testMsg.value = ''
  const meta = sources.value.find(s => s.id === activeSourceId.value)
  const name = meta?.name || '当前音源'
  try {
    const platforms = getPlatforms(activeSourceId.value)
    const platform = platforms.find(p => p !== 'mg' && p !== 'local') || 'kw'
    const res = await lxRuntime.probeSource(activeSourceId.value, platform)
    latencies.value = { ...latencies.value, [activeSourceId.value]: res.ok ? `${res.latencyMs}ms` : '异常' }
    if (res.ok) {
      testOk.value = true
      testMsg.value = `解析正常（${platform.toUpperCase()} · ${res.latencyMs}ms）—— 这个音源可以正常播放。`
      logInfo('音源解析测试', `${name} 正常`, `${platform} ${res.latencyMs}ms`)
    } else {
      // 失败原因（脚本原始报错）只进日志；界面上给结论 + 该怎么做
      testOk.value = false
      testMsg.value = '解析失败，这个音源当前取不到直链。换一个音源试试，或到「日志」看具体原因。'
      logError('音源解析测试', `${name} 取不到直链`, res.error)
    }
  } catch (e) {
    testOk.value = false
    testMsg.value = fail('音源解析测试', e)
  } finally {
    isTesting.value = false
  }
}

function resetHealth(id) {
  sourceHealth.reset(id)
  const next = { ...latencies.value }
  delete next[id]
  latencies.value = next
}

function resetAllHealth() {
  sourceHealth.resetAll()
  latencies.value = {}
}

/** 读一个 File 成文本（读失败给空串，别让一个坏文件炸掉整批） */
function readTextFile(f) {
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = (ev) => resolve({ filename: f.name, content: String(ev.target.result || '') })
    reader.onerror = () => resolve({ filename: f.name, content: '' })
    reader.readAsText(f)
  })
}

/**
 * 一批 File → 并入待导入列表（**点选和拖拽共用这一条**）。
 *
 * 被拒的文件（.txt/图片/拖进来的文件夹）不静默吞掉：`dropNotice` 里说清楚忽略了几个、叫什么。
 * 拖拽最容易出的问题就是「什么也没发生，用户以为成功了」。
 */
async function addPickedFiles(files = []) {
  const list = Array.from(files || [])
  if (!list.length) return
  const byName = new Map()
  for (const f of list) if (!byName.has(f.name)) byName.set(f.name, f)
  const { scripts, rejected } = splitScriptNames(list.map(f => f.name))
  const items = await Promise.all(scripts.map(name => readTextFile(byName.get(name))))
  pickedFiles.value = mergePicked(pickedFiles.value, items)

  const parts = []
  if (rejected.length) parts.push(rejectedText(rejected))
  const over = overflowText(pendingTotal.value)
  if (over) parts.push(over)
  dropNotice.value = parts.join(' · ')
}

/** 多选本地 .js，读成文本暂存（真正的提交在 submitBatchImport） */
function handleFilesPick(e) {
  const files = Array.from(e.target.files || [])
  e.target.value = '' // 允许重复选同一文件（File 对象已经拿到手了）
  addPickedFiles(files)
}

// ── 拖拽导入 ──
// 整块虚线区域都能接：用户会往空白处扔文件，只让 file input 自己可拖是拦不住的。
//
// 高亮**不靠 dragenter/dragleave 配对**（那套一定会闪）：
// 鼠标划过子元素时 dragleave 会冒泡出来，而 enter/leave 的到达顺序与次数各浏览器不一致，
// 计数法要么闪一下、要么漏掉一次 leave 就永久亮着。改成心跳：
// `dragover` 在指针停留期间会持续触发（约每 50ms 一次，子元素上也会冒泡上来），
// 只要它停了就说明指针走了 —— 150ms 无 dragover 自动熄灭，任何漏事件都不会卡住。
function onDragEnter() {
  isDragging.value = true
}

/** dragover 必须 preventDefault，否则浏览器不认这是个合法落点（模板里已经 .prevent） */
function onDragOver() {
  isDragging.value = true
  if (dragHeartbeat) clearTimeout(dragHeartbeat)
  dragHeartbeat = setTimeout(() => {
    dragHeartbeat = 0
    isDragging.value = false
  }, 150)
}

function stopDragHighlight() {
  if (dragHeartbeat) { clearTimeout(dragHeartbeat); dragHeartbeat = 0 }
  isDragging.value = false
}

/**
 * 落点：优先当文件收；一个文件都没有时，看是不是拖进来的一段文本
 * （从浏览器标签页拖一条脚本直链过来是最自然的动作），能捞出链接就填进 URL 框。
 */
async function onDrop(e) {
  stopDragHighlight()
  const dt = e.dataTransfer
  const files = Array.from(dt?.files || [])
  if (files.length) {
    await addPickedFiles(files)
    return
  }
  const text = dt?.getData?.('text/uri-list') || dt?.getData?.('text/plain') || ''
  const urls = urlsFromText(text)
  if (urls.length) {
    importUrls.value = [importUrls.value.trim(), ...urls].filter(Boolean).join('\n')
    dropNotice.value = `已从拖拽内容里识别 ${urls.length} 条链接`
    return
  }
  dropNotice.value = `没识别到脚本 —— 拖 .js / .mjs / .cjs 文件，或一条脚本直链（一次最多 ${MAX_IMPORT_ITEMS} 条）`
}

/**
 * 批量导入：URL + 本地脚本一次提交。
 *
 * 导入完成后（可选）逐个真实取链测试 —— 这正是用户要的「导入后自动测试」，
 * 省得导完还要一个个点「测试」。
 */
async function submitBatchImport() {
  if (!hasPendingImport.value || isImporting.value) return

  const urls = importUrls.value
    .split('\n')
    .map(s => s.trim())
    .filter(s => s && !s.startsWith('#'))

  isImporting.value = true
  importReport.value = null
  importError.value = ''
  try {
    const res = await SourcesAPI.importBatch(urls, pickedFiles.value)
    const bad = apiError(res, '导入失败')
    if (bad) {
      importError.value = bad
      logError('导入音源', bad, `code=${res?.code} message=${res?.message}`)
      return
    }
    const data = res.data || {}
    const items = data.results || []
    importReport.value = {
      imported: data.imported || 0,
      // 后端已给出统计，优先用它；缺失时再按状态兜底
      skipped: data.skipped ?? items.filter(r => r.status === 'skipped').length,
      failed: data.failed ?? items.filter(r => r.status === 'failed').length,
      items,
    }
    // 逐条失败原因太长，界面只留前 8 条摘要，完整明细进日志
    const badItems = items.filter(r => r.status === 'failed')
    if (badItems.length) {
      logError(
        '导入音源',
        `${badItems.length} 个音源导入失败`,
        badItems.map(r => `${r.name || r.source || '?'}: ${r.reason || '未知原因'}`).slice(0, 20).join('；'),
      )
    }

    // 清空输入，但保留弹窗让用户看到结果
    importUrls.value = ''
    pickedFiles.value = []
    await loadSources()
    emit('source-changed')

    if (autoTestAfterImport.value && importReport.value.imported > 0) {
      const ids = items.filter(r => r.status === 'imported' && r.id).map(r => r.id)
      await probeMany(ids)
    } else {
      // 没开自动测试（或一个都没导进来）也要给结论 —— 否则关掉弹窗就什么都不知道了
      const r = importReport.value
      importNotice.value = `导入完成：成功 ${r.imported} 个`
        + (r.skipped ? ` · 跳过 ${r.skipped} 个（已存在）` : '')
        + (r.failed ? ` · 失败 ${r.failed} 个` : '')
    }
  } catch (err) {
    importError.value = fail('导入音源', err)
  } finally {
    isImporting.value = false
  }
}

/**
 * 逐个测试（串行，避免同时打多个音源），**收集结果并按导入顺序自动启用测通的**。
 * 用户 2026-09-30：导入后测试 → 自动选择成功的音源 → 失败的可以点击再测试。
 * 旧实现只 `await probeOne(id)` 数了个进度，既不收集也不启用，跑完连个结论都没有。
 */
async function probeMany(ids) {
  importProbeDone.value = 0
  importTest.value = { total: ids.length, ok: [], fail: [], enabledCount: 0, running: true }
  for (const id of ids) {
    const passed = await probeOne(id)
    // probeOne 在「已在探测中」时返回 null，那种情况既不算成功也不算失败
    if (passed !== null) {
      ;(passed ? importTest.value.ok : importTest.value.fail)
        .push({ id, name: sourceName(id), reason: probeErrors.get(id) || '' })
    }
    importProbeDone.value++
  }
  importTest.value.running = false
  await enableProbedSources()
  await nextTick()
  // 结果块在弹窗里可能被滚出视野 —— 主动带用户看一眼（这就是「没有提醒」的另一半原因）
  document.getElementById('import-report')?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
}

/** 把刚测通的音源按导入顺序追加到启用池，然后写终态结论 */
async function enableProbedSources() {
  const t = importTest.value
  if (!t) return
  let enabled = 0
  for (const it of t.ok) {
    if (await enableSource(it.id)) enabled++
  }
  t.enabledCount = enabled
  finishImportNotice()
}

/** 终态结论条：弹窗里一份，页面正文顶部一份（关掉弹窗也看得到） */
function finishImportNotice() {
  const t = importTest.value
  if (!t) return
  const parts = [`${t.ok.length} 个可用`]
  if (t.enabledCount) parts.push(`已自动启用 ${t.enabledCount} 个`)
  if (t.fail.length) parts.push(`${t.fail.length} 个不可用（可点「再测试」）`)
  importNotice.value = `测试完成：${parts.join(' · ')}`
}

/** 失败项「再测试」：测通了就挪到可用列表并自动启用，仍失败就刷新原因 */
async function retestImportItem(it) {
  const passed = await probeOne(it.id)
  const t = importTest.value
  if (!t) return
  if (!passed) {
    // 原因会变（上一次「初始化中」这次可能变成 502）—— 不刷新就是骗人
    const fresh = t.fail.find(x => x.id === it.id)
    if (fresh) fresh.reason = probeErrors.get(it.id) || fresh.reason
    return
  }
  t.fail = t.fail.filter(x => x.id !== it.id)
  t.ok.push({ id: it.id, name: it.name })
  if (await enableSource(it.id)) t.enabledCount++
  finishImportNotice()
}

/** 打开导入弹窗（清掉上一轮的结果与输入） */
function openImportModal() {
  importUrls.value = ''
  pickedFiles.value = []
  importReport.value = null
  importProbeDone.value = 0
  importTest.value = null
  importNotice.value = ''
  importError.value = ''
  dropNotice.value = ''
  stopDragHighlight()
  apiMsg.value = ''
  apiOk.value = false
  showImportModal.value = true
}

/**
 * 按服务地址接入：后端会自动探测协议（私有 REST / 标准 LX Server / 自建中转 API），
 * 所以这里不需要用户选协议 —— 只填地址（和可选令牌）就行。
 */
async function submitApiSource() {
  const base = apiBaseUrl.value.trim()
  if (!base || isImporting.value) return

  isImporting.value = true
  apiMsg.value = ''
  apiOk.value = false
  try {
    const res = await SourcesAPI.apiSave({
      base_url: base,
      token: apiToken.value.trim(),
      name: apiName.value.trim(),
    })
    if (res.code !== 200) {
      apiOk.value = false
      apiMsg.value = apiError(res, '接入失败')
      logError('接入服务型音源', apiMsg.value, `code=${res?.code} message=${res?.message} base_url=${base}`)
      return
    }
    const d = res.data || {}
    apiOk.value = true
    apiMsg.value = [
      `服务地址：${d.base_url}`,
      `识别协议：${d.label || d.protocol}`,
      d.note ? `探测说明：${d.note}` : '',
      d.has_token ? '已保存令牌' : '未使用令牌',
    ].filter(Boolean).join('\n')

    apiBaseUrl.value = ''
    apiToken.value = ''
    apiName.value = ''
    await loadApiSources()
    emit('source-changed')
  } catch (e) {
    apiOk.value = false
    apiMsg.value = fail('接入服务型音源', e)
  } finally {
    isImporting.value = false
  }
}

/** 拉取已接入的 API 音源列表，并同步登记到运行时（否则它们不参与取链轮询） */
async function loadApiSources() {
  try {
    const res = await SourcesAPI.apiList()
    if (res?.code === 200) {
      apiSources.value = res.data?.sources || []
      lxRuntime.setApiSources(apiSources.value)
    }
  } catch (_) {
    // 列表拉不到不影响主流程
  }
}

/** 删除一个 API 音源 */
async function removeApiSource(item) {
  if (!confirm(`确定移除音源「${item.name}」？`)) return
  pageError.value = ''
  try {
    await SourcesAPI.apiDelete(item.id)
    await loadApiSources()
    emit('source-changed')
  } catch (e) {
    pageError.value = fail('移除服务型音源', e)
  }
}

async function deleteSource(id) {
  if (!confirm('确定要删除此音源吗？')) return
  pageError.value = ''
  try {
    const res = await SourcesAPI.delete(id)
    const bad = apiError(res, '删除失败')
    if (bad) {
      pageError.value = bad
      logError('删除音源', bad, `code=${res?.code} message=${res?.message} id=${id}`)
      return
    }
    sourceHealth.reset(id)
    await loadSources()
  } catch (err) {
    pageError.value = fail('删除音源', err)
  }
}

// ── 定时巡检：每 5 分钟探一轮，但**只探有用的、且每轮有预算**（用户 2026-09-30 要求） ──
// 旧实现是「把全部音源串行重探一遍」（含未启用、含已失效），音源一多后台就近似「永远在探」：
// 29 源压测一轮 60~90s、主线程阻塞 1.8s、前 30 秒卡在「检测中 0/29」。
// 现在选源交给 services/probePlan.js（纯函数，有单测），探测走 probeOne(id, { light: true })，
// 单轮工作量恒定，跟音源总数无关。
const PROBE_INTERVAL_MS = 5 * 60 * 1000
let autoProbeTimer = 0
let autoProbeBusy = false
/** 上一轮巡检停下的位置：下一轮从这儿接着走（环形轮转，保证每个候选迟早被探到） */
let sweepCursor = 0

function pageHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

async function autoProbeSweep() {
  if (autoProbeBusy || pageHidden() || !sources.value.length) return
  autoProbeBusy = true
  try {
    const plan = sweepPlan({
      sources: sources.value,
      activeIds: activeSourceIds.value,
      history: probeHistory.value,
      cursor: sweepCursor,
      budget: SWEEP_BUDGET,
    })
    sweepCursor = plan.nextCursor
    for (const id of plan.ids) {
      if (pageHidden()) break
      await probeOne(id, { light: true })
      await new Promise(r => setTimeout(r, 400))
    }
  } finally {
    autoProbeBusy = false
  }
}

function startAutoProbe() {
  stopAutoProbe()
  autoProbeTimer = setInterval(autoProbeSweep, PROBE_INTERVAL_MS)
}

function stopAutoProbe() {
  if (autoProbeTimer) { clearInterval(autoProbeTimer); autoProbeTimer = 0 }
}

/** 1 秒心跳：只用来刷卡片上「多久前失败」的相对时间。 */
function startTick() {
  stopTick()
  tickTimer = setInterval(() => { nowTs.value = Date.now() }, 1000)
}

function stopTick() {
  if (tickTimer) { clearInterval(tickTimer); tickTimer = 0 }
}

// ── 外挂音源（musicdl sidecar，v2.1.83）────────────────────────────────
//
// 三个平台短码与 sidecar 的 `core.SOURCES` 一一对应；`kg` / `kw` 的提示里写明
// 「打开后搜索与解析都改走 musicdl」—— 因为一个平台只能有一个 id 空间，
// 一半原生一半外挂就是「搜得到、点开放不出」。
// ⚠️ `tip` 里的实测结论来自 v2.1.87 真机（同一关键词、单源）：
// 咪咕 2.1 秒出 5 条；千千与 B站 **超过 12 秒且一条都没出过**（连续多次都是 0）。
// 所以后两个默认不开 —— 开着只会让每次搜索为它们空等。
const MUSICDL_PLATFORMS = [
  { id: 'mg', name: '咪咕', tip: '曲率原生没有 —— 纯增量（实测可用）' },
  { id: 'bq', name: '千千', tip: '实测超过 12 秒且一条都没搜出来 —— 默认关，想试可以开' },
  { id: 'bi', name: 'B站', tip: '实测超过 12 秒且一条都没搜出来 —— 默认关，想试可以开' },
  { id: 'kg', name: '酷狗', tip: '原生搜得到、拿不到直链；打开后搜索与解析都改走 musicdl' },
  { id: 'kw', name: '酷我', tip: '原生搜得到、拿不到直链；打开后搜索与解析都改走 musicdl' },
]

const musicdlPageOpen = ref(false)
const musicdlEnabled = ref(false)
const musicdlSources = ref([])
/** /api/musicdl/status 的结果（sidecar 现况）。null = 还没查到 */
const musicdlStatus = ref(null)
const musicdlBusy = ref(false)
const musicdlErr = ref('')

/** sidecar 的 state → 徽章。off/preparing/starting/ready/failed/stopped */
const musicdlBadge = computed(() => {
  if (!musicdlEnabled.value) return { text: '已关闭', cls: 'border-gray-200 text-gray-400' }
  const st = musicdlStatus.value?.sidecar?.state || ''
  if (st === 'ready') return { text: '运行中', cls: 'border-emerald-200 bg-emerald-50 text-emerald-600' }
  if (st === 'preparing') return { text: '准备中', cls: 'border-amber-200 bg-amber-50 text-amber-600' }
  if (st === 'starting') return { text: '启动中', cls: 'border-amber-200 bg-amber-50 text-amber-600' }
  if (st === 'failed') return { text: '启动失败', cls: 'border-rose-200 bg-rose-50 text-rose-600' }
  if (st === 'stopped') return { text: '已停止', cls: 'border-gray-200 text-gray-400' }
  return { text: '待启动', cls: 'border-gray-200 text-gray-400' }
})

/**
 * 一行说明。**失败与准备都要说清**：
 * 「装依赖失败」和「平台被风控」在界面上长得一模一样，不说清就无从下手。
 */
const musicdlNote = computed(() => {
  if (musicdlErr.value) return musicdlErr.value
  const data = musicdlStatus.value
  if (!data) return ''
  if (data.available === false) return '这个构建里没有带 sidecar 源码（安装包不完整）。'
  const sc = data.sidecar || {}
  if (sc.state === 'failed') return `启动失败：${sc.error || '原因见日志'}`
  if (sc.state === 'preparing') return sc.note || '正在准备 Python 环境…'
  if (sc.state === 'starting') return sc.note || '正在启动外挂进程…'
  if (sc.state === 'ready') {
    const names = MUSICDL_PLATFORMS.filter((p) => (data.sources || []).includes(p.id)).map((p) => p.name)
    return `已就绪，当前启用：${names.join('、') || '（无）'}`
  }
  return ''
})

const musicdlNoteTone = computed(() => {
  const st = musicdlStatus.value?.sidecar?.state
  if (musicdlErr.value || st === 'failed') return 'text-rose-600'
  if (st === 'ready') return 'text-emerald-600'
  return 'text-gray-500'
})

async function loadMusicDL() {
  try {
    const cfg = await ConfigAPI.get()
    const c = cfg?.config || cfg?.data || cfg || {}
    musicdlEnabled.value = !!c.musicdl_enabled
    musicdlSources.value = Array.isArray(c.musicdl_sources) && c.musicdl_sources.length
      ? c.musicdl_sources
      : ['mg'] // 后端空名单 = 默认（只剩咪咕：千千/B站实测不可用）
    const st = await fetch('/api/musicdl/status', { headers: { Accept: 'application/json' } })
    if (st.ok) musicdlStatus.value = (await st.json())?.data || null
  } catch (e) {
    musicdlErr.value = describeError(e)
  }
}

/** 局部 patch 保存：只提交动到的键，别的设置不受影响（与榜单开关同一套口径）。 */
async function saveMusicDL(patch) {
  musicdlBusy.value = true
  musicdlErr.value = ''
  try {
    await ConfigAPI.save(patch)
    // sidecar 是**异步**起的（建 venv + 装依赖要一两分钟）→ 保存完立刻拉一次状态，
    // 让界面马上显示「准备中」，之后用户切回来再看就是新的。
    await loadMusicDL()
  } catch (e) {
    musicdlErr.value = describeError(e)
    await loadMusicDL() // 回滚显示：以服务端为准
  } finally {
    musicdlBusy.value = false
  }
}

function toggleMusicDL() {
  const next = !musicdlEnabled.value
  musicdlEnabled.value = next
  saveMusicDL({ musicdl_enabled: next })
}

function toggleMusicDLPlatform(id) {
  const set = new Set(musicdlSources.value)
  if (set.has(id)) set.delete(id)
  else set.add(id)
  const next = MUSICDL_PLATFORMS.map((p) => p.id).filter((x) => set.has(x))
  musicdlSources.value = next
  // 空名单 = 回默认（后端语义），这里就按默认显示，避免「全不选」这种无意义状态
  saveMusicDL({ musicdl_sources: next })
}

onMounted(() => {
  loadMusicDL()
  loadSources()
  loadProtocols()
  loadApiSources()
  startTick()
  startAutoProbe()
  // 第一次进页面：还没有任何测速记录就先探一轮，不然要干等 5 分钟才有记录点
  if (!Object.keys(probeHistory.value).length) setTimeout(autoProbeSweep, 800)
})

// ⚠️ 页面是**常驻**的（SwipePager 不卸载），所以两个定时器都必须在 onDeactivated 里停：
//    轮询不停 = 离开音源页后还在后台每 5 分钟打一遍上游；
//    心跳不停 = 每秒无谓地写一次响应式数据（虽然现在不再造成 DOM 变更，但没理由留着）。
onActivated(() => { startAutoProbe(); startTick() })
onDeactivated(() => { stopAutoProbe(); stopTick() })

/**
 * 弹窗开着的时候，把**整页**的拖放默认行为都接管掉。
 *
 * 不接管的后果很具体：往弹窗之外（页面背景、或者拖歪了）松手，浏览器会直接
 * 打开那个 .js 文件 —— 页面跳走、输入全丢。落点统一走 onDrop，行为才一致。
 */
function guardPageDrop(e) { e.preventDefault() }
watch(showImportModal, (open) => {
  if (open) {
    document.addEventListener('dragover', guardPageDrop)
    document.addEventListener('drop', guardPageDrop)
  } else {
    document.removeEventListener('dragover', guardPageDrop)
    document.removeEventListener('drop', guardPageDrop)
    stopDragHighlight()
  }
})

onUnmounted(() => {
  stopTick()
  stopAutoProbe()
  document.removeEventListener('dragover', guardPageDrop)
  document.removeEventListener('drop', guardPageDrop)
})
</script>
