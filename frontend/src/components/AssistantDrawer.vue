<template>
  <div>
    <!--
      「面板实体度」开关（2026-10-29，已换代）：
      弹窗开着时给 <html> 打上 data-ai-open。原来是让 CSS 把半透明玻璃底色的透明度
      临时提到 0.86 去兜住「开合头几帧背后页面透出来」。用户反馈仍能看见后面的页面，
      于是直接把 .ys-ai-glass 改成**纯白不透明**（见 style.css），这条规则已删除；
      data-ai-open 这个状态本身保留，作为「弹窗是否开着」的全局标记。
    -->
    <Teleport to="body">
      <!--
        AI 助手抽屉：**从右边缘滑入、向左展开**（与下载抽屉同侧）。

        2026-10-26 新增；2026-10-27 大修：
        - 修透明背景（--ys-bg 变量不存在 → 用 --ys-surface）
        - 会话记录：会话落盘、列表可切、可删、可新建（新的在最上面）
        - 顶部对话模型下拉：从网关 /v1/models 拉真实列表，切了立即保存
        - 修「设置点了没反应」：信封解析收敛到 client.js，这里不再 res.data||res 猜结构
        - 输入区提示区分「没配 Key」与「配了没启用」，并指路到 账号连接 → AI 大模型

      安全模型（重要）：
        - 只读能力（Auto）：模型可直接调用，不弹确认
        - 写入能力（Confirm）：模型发起后先存为待确认操作，界面必须点「确认执行」
        - 全手动（ReadOnly）：模型只能查，不能调任何写入能力
      后端策略裁决（policy.Decide）是权威，前端只是把它翻译成 UI。
      -->
      <!--
        全屏遮罩（2026-10-29 四次修正，见 style.css 里的长注释）：
        用户明确「不要背景透明/透视效果」，所以现在遮罩只做两件事 —— 半透明暗化 + opacity 淡入，
        既不自己 backdrop-blur，也不再通过整页 filter 去糊背景。
        ⚠️ 原来这里有一套 data-ai-backdrop="fade|on" 的模糊爬升控制器（配合遮罩上的
        blur(0)→blur(10px) 过渡），第四次修正后遮罩已经不模糊了，那套标记没有任何 CSS
        消费方，于是一并删除（连 JS 里的 backdropBlurring / beginBackdropBlur 一起）。
      -->
      <Transition
        enter-active-class="transition-opacity duration-300 ease-out"
        enter-from-class="opacity-0"
        enter-to-class="opacity-100"
        leave-active-class="transition-opacity duration-200 ease-in"
        leave-from-class="opacity-100"
        leave-to-class="opacity-0"
      >
        <div
          v-if="modelValue"
          @click="close"
          class="ys-ai-backdrop fixed inset-0 z-[60]"
        ></div>
      </Transition>

      <!--
        弹窗主体（2026-10-28 由「右侧抽屉」改为「居中弹窗」）：
          外壳 .ys-ai-halo 就是 Apple Intelligence 那圈流动炫彩光边——外圈是饱和的流光，
          内层面板用**纯白不透明**的实底盖住中间，于是光只剩「从外到内渐隐」的那一圈。
          （2026-10-29：面板原来是 0.72 半透明玻璃 + backdrop-filter，用户要求不要透出
          背后页面，已改成纯白实底，见 style.css 的 .ys-ai-glass。）
          手机上贴底做整屏弹层（不居中，键盘弹出时不会顶到看不见输入框）。
      -->
      <Transition
        enter-active-class="ys-ai-halo-in"
        enter-from-class="opacity-0 scale-[0.985]"
        enter-to-class="opacity-100 scale-100"
        leave-active-class="ys-ai-halo-out"
        leave-from-class="opacity-100 scale-100"
        leave-to-class="opacity-0 scale-[0.97]"
      >
        <div
          v-if="modelValue"
          ref="haloRef"
          class="ys-ai-halo ys-ai-halo--open fixed left-1/2 top-1/2 z-[61] flex w-[min(720px,calc(100vw-32px))] pt-safe pb-safe"
        >
          <!-- 高度全权交给 .ys-ai-halo：它的 height 由 CSS 统一给（桌面 min(760px,86vh)，
               手机 var(--ys-ai-vh)，即键盘之外的可视高度）。
               ⚠️ 模板上**不要**再写 `h-[min(760px,86vh)]` 这类高度工具类：工具类位于
               @layer utilities，优先级低于 style.css 里未分层的 .ys-ai-halo 规则，
               手机上这条高度规则会被 tailwind 那条盖住，弹窗于是换成 86vh 且不再跟随
               键盘 —— 表现就是输入框跑到屏幕外（2026-10-28 真机排查过程）。
               玻璃面板用 h-full + min-h-0 填满外壳（min-h-0 必须有：flex 项目默认
               min-height:auto，内容一多就会把输入区顶出面板）。 -->
          <!-- 纯装饰光效层：沿用 modelValue 的 Transition 激活/关闭，不新增开关状态。
               两个圆角容器跟随弹窗尺寸，内部光斑只做小幅位移，绝不旋转外壳。 -->
          <div v-if="decorOn" class="ys-ai-aura" aria-hidden="true">
            <!-- 2026-10-30：背景上的动态光晕（Apple Intelligence 的边框光）——
                 面板**外面**一圈模糊的 conic 彩光，光溢到背后的遮罩/页面上；
                 面板内部只留极窄的一圈渐隐流光（.ys-ai-veil）作为"发光边缘"。
                 旧的 .ys-ai-aura__wash（面板内外的两团淡径向光斑）已删除。 -->
            <div class="ys-ai-glow">
              <div class="ys-ai-glow__hue"></div>
            </div>
          </div>
          <!-- 边缘流光：一张静态的方向性渐变，形状由面板自己的 border-radius 裁剪。
               2026-10-29 用户：「弹出窗四个角有个白色的弧形」→ 旧版「五档矩形环带 +
               blur + padding + mask」在四个角各自糊出一段**比边框更亮的白弧**（blur 把
               直角的角抹成圆弧，与真实 22px 圆角的圆心对不上）。现在只用一层渐变，
               四边各往内 25% 淡出，四个角天然就是面板的圆角，不会再冒出第二段弧。
               ⚠️ 必须盖在玻璃之上（z-index 3），否则向内那半边被白色面板吃掉。 -->
          <div v-if="decorOn" class="ys-ai-veil" aria-hidden="true">
            <div class="ys-ai-veil__glow"></div>
          </div>
          <div
            class="ys-ai-glass flex h-full w-full min-h-0 flex-col overflow-hidden rounded-[var(--ys-radius-overlay)] border border-white/40 shadow-[0_24px_80px_-20px_rgba(15,23,42,0.45)]"
          >
          <!-- 顶部标题栏 -->
          <div class="ys-ai-layer relative px-7 pb-5 pt-6 border-b border-[var(--ys-border)] flex items-center justify-between bg-white shrink-0">
            <div class="flex items-center gap-2.5 min-w-0">
              <!-- Apple Intelligence 风格的彩色光球图标（conic 渐变球 + 白色高光），
                   与面板边缘那圈流光环同一套色序 -->
              <div class="ys-ai-orb-icon w-8 h-8 rounded-full flex items-center justify-center shrink-0">
                <Sparkles class="w-4 h-4 text-white" />
              </div>
              <div class="min-w-0">
                <h3 class="text-sm font-bold text-gray-800">AI 助手</h3>
                <p class="text-[11px] text-gray-500 truncate">
                  {{ settings.enabled ? `${modeLabel} · 模型 ${currentModel || '未设置'}` : '已关闭（右上设置里开启）' }}
                </p>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <button
                @click="toggleSessions"
                :class="[
                  'p-1.5 rounded-xl transition-colors',
                  showSessions ? 'text-emerald-600 bg-emerald-50' : 'text-gray-500 hover:text-emerald-600 hover:bg-emerald-50'
                ]"
                title="会话记录"
              >
                <History class="w-4 h-4" />
              </button>
              <button
                @click="createSession"
                class="p-1.5 rounded-xl text-gray-500 hover:text-emerald-600 hover:bg-emerald-50 transition-colors"
                title="新建会话"
              >
                <Plus class="w-4 h-4" />
              </button>
              <button
                v-if="!showSettings"
                @click="showSettings = true"
                class="p-1.5 rounded-xl text-gray-500 hover:text-emerald-600 hover:bg-emerald-50 transition-colors"
                title="助手设置"
              >
                <Settings2 class="w-4 h-4" />
              </button>
              <button
                v-else
                @click="showSettings = false"
                class="p-1.5 rounded-xl text-emerald-600 bg-emerald-50 transition-colors"
                title="收起设置"
              >
                <ChevronDown class="w-4 h-4" />
              </button>
              <button
                @click="close"
                class="p-1.5 rounded-xl text-gray-500 hover:text-gray-600 hover:bg-[var(--ys-surface-hover)] transition-colors"
                title="关闭"
              >
                <X class="w-4 h-4" />
              </button>
            </div>
          </div>

          <!-- 会话记录面板（折叠） -->
          <Transition
            enter-active-class="transition-all duration-200 ease-out"
            enter-from-class="max-h-0 opacity-0"
            enter-to-class="max-h-[50vh] opacity-100"
            leave-active-class="transition-all duration-150 ease-in"
            leave-from-class="max-h-[50vh] opacity-100"
            leave-to-class="max-h-0 opacity-0"
          >
            <div v-if="showSessions" class="border-b border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] shrink-0 overflow-hidden">
              <div class="max-h-[45vh] overflow-y-auto px-3 py-2 space-y-1">
                <p v-if="!sessions.length" class="text-center text-[11px] text-gray-500 py-4">
                  还没有历史会话，发一条消息就有了
                </p>
                <div
                  v-for="s in sessions"
                  :key="s.id"
                  @click="switchSession(s.id)"
                  :class="[
                    'flex items-center gap-2 px-3 py-2 rounded-xl bg-white border cursor-pointer transition-colors',
                    s.id === currentSessionId
                      ? 'border-emerald-200 ring-1 ring-emerald-50'
                      : 'border-[var(--ys-border)] hover:border-emerald-200'
                  ]"
                >
                  <MessageSquare class="w-3.5 h-3.5 text-gray-300 shrink-0" />
                  <div class="min-w-0 flex-1">
                    <div class="text-xs font-medium text-gray-700 truncate">{{ s.title || '新会话' }}</div>
                    <div class="text-[10px] text-gray-500">{{ s.updated_at }} · {{ s.messages }} 条</div>
                  </div>
                  <button
                    @click.stop="deleteSession(s.id)"
                    class="p-1 rounded-lg text-gray-300 hover:text-red-500 hover:bg-red-50 transition-colors shrink-0"
                    title="删除这个会话"
                  >
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            </div>
          </Transition>

          <!-- 设置面板（折叠） -->
          <Transition
            enter-active-class="transition-all duration-200 ease-out"
            enter-from-class="max-h-0 opacity-0"
            enter-to-class="max-h-[60vh] opacity-100"
            leave-active-class="transition-all duration-150 ease-in"
            leave-from-class="max-h-[60vh] opacity-100"
            leave-to-class="max-h-0 opacity-0"
          >
            <div v-if="showSettings" class="border-b border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] shrink-0 overflow-hidden">
              <div class="max-h-[55vh] overflow-y-auto">
                <!-- 对话模型 -->
                <div class="px-6 py-3 border-b border-[var(--ys-border)]">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">对话模型</label>
                  <div class="mt-1.5 flex items-center gap-2">
                    <select
                      :value="currentModel"
                      @change="saveModel($event.target.value)"
                      :disabled="aiState !== 'ready'"
                      class="flex-1 min-w-0 px-2.5 py-1.5 rounded-xl border border-[var(--ys-line-control)] bg-white text-xs text-gray-700 focus:outline-none focus:border-emerald-200 focus:ring-2 focus:ring-emerald-50 transition-colors disabled:opacity-50"
                    >
                      <option v-if="!modelOptions.length" value="">（暂无可用模型）</option>
                      <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
                    </select>
                  </div>
                  <p v-if="modelSaveMsg" class="text-[10px] text-emerald-600 mt-1">{{ modelSaveMsg }}</p>
                  <p v-else-if="modelError" class="text-[10px] text-amber-500 mt-1">拉取模型列表失败：{{ modelError }}</p>
                  <p v-else-if="aiState === 'unconfigured'" class="text-[10px] text-amber-500 mt-1 leading-relaxed">
                    还没配置 AI：到「账号连接 → AI 大模型」填 Base URL 与 API Key 并启用
                  </p>
                  <p v-else-if="aiState === 'disabled'" class="text-[10px] text-amber-500 mt-1 leading-relaxed">
                    AI 已配置但没启用：到「账号连接 → AI 大模型」勾选「启用」并保存
                  </p>
                  <p v-else class="text-[10px] text-gray-500 mt-1 leading-relaxed">
                    列表来自网关 /v1/models；切换立即保存。改 Base URL / Key 去「账号连接 → AI 大模型」。
                  </p>
                </div>

                <!-- 执行模式 -->
                <div class="px-6 py-3">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">执行模式</label>
                  <div class="mt-1.5 flex gap-2">
                    <button
                      v-for="m in modeOptions"
                      :key="m.value"
                      @click="updateSettings({ mode: m.value })"
                      :class="[
                        'flex-1 px-3 py-1.5 rounded-xl text-xs font-medium transition-colors',
                        settings.mode === m.value
                          ? 'bg-emerald-600 text-white shadow-sm'
                          : 'bg-white text-gray-500 hover:bg-[var(--ys-surface-hover)] border border-[var(--ys-line-control)]'
                      ]"
                    >
                      {{ m.label }}
                    </button>
                  </div>
                  <p class="text-[10px] text-gray-500 mt-1 leading-relaxed">{{ modeDesc }}</p>
                </div>

                <!-- 总开关 -->
                <div class="px-6 py-2 flex items-center justify-between">
                  <div>
                    <span class="text-xs font-medium text-gray-600">启用助手</span>
                    <p class="text-[10px] text-gray-500">关闭后模型不会调用任何能力</p>
                  </div>
                  <button
                    @click="updateSettings({ enabled: !settings.enabled })"
                    :class="[
                      'relative w-11 h-6 rounded-full transition-colors',
                      settings.enabled ? 'bg-emerald-600' : 'bg-[var(--ys-line-control)]'
                    ]"
                  >
                    <span
                      :class="[
                        'absolute top-0.5 w-5 h-5 rounded-full bg-white shadow-sm transition-transform',
                        settings.enabled ? 'translate-x-[1.4rem]' : 'translate-x-0.5'
                      ]"
                    ></span>
                  </button>
                </div>

                <!-- 能力开关 -->
                <div class="px-6 py-3">
                  <label class="text-[11px] font-semibold text-gray-500 uppercase tracking-wide">能力开关</label>
                  <p class="text-[10px] text-gray-500 mt-0.5 mb-2">关闭的能力模型不会调用（写入类操作仍需确认）</p>
                  <div class="space-y-1.5">
                    <div
                      v-for="cap in capabilities"
                      :key="cap.name"
                      @click="toggleCapability(cap.name)"
                      class="flex items-center justify-between px-3 py-2 rounded-xl bg-white border border-[var(--ys-border)] cursor-pointer hover:bg-[var(--ys-surface-sunken)] transition-colors"
                    >
                      <div class="min-w-0">
                        <div class="text-xs font-medium text-gray-700 truncate">{{ cap.summary || cap.name }}</div>
                        <div class="text-[10px] text-gray-500 truncate font-mono">{{ cap.name }}</div>
                      </div>
                      <!-- 注意：读的是 settings.disabled（之前这里读了个不存在的 disabled，整个子树直接渲染报错） -->
                      <span
                        :class="[
                          'relative w-9 h-5 rounded-full transition-colors shrink-0 pointer-events-none',
                          settings.disabled[cap.name] ? 'bg-[var(--ys-line-control)]' : 'bg-emerald-600'
                        ]"
                      >
                        <span
                          :class="[
                            'absolute top-0.5 w-4 h-4 rounded-full bg-white shadow-sm transition-transform',
                            settings.disabled[cap.name] ? 'translate-x-[1rem]' : 'translate-x-0.5'
                          ]"
                        ></span>
                      </span>
                    </div>
                  </div>
                </div>

                <!-- 保存失败提示（之前静默吞掉，点了像没反应） -->
                <p v-if="saveError" class="px-6 pb-3 text-[10px] text-red-500">{{ saveError }}</p>
                <p v-else-if="saveOk" class="px-6 pb-3 text-[10px] text-emerald-600">{{ saveOk }}</p>
              </div>
            </div>
          </Transition>

          <!-- 消息流 -->
          <!-- 消息区：flex-1 + min-h-0 吃掉标题栏与输入区之间的全部剩余高度。
               min-h-0 是必须的——flex 项目默认 min-height:auto，内容一多就会把
               输入区顶出弹窗（键盘弹出时尤其明显）。 -->
          <div ref="msgContainer" class="ys-ai-layer flex-1 min-h-0 overflow-y-auto overscroll-contain px-7 py-5 space-y-4">
            <!-- 空状态 -->
            <div v-if="messages.length === 0" class="flex flex-col items-center justify-center h-full text-center gap-4">
              <div class="w-16 h-16 rounded-2xl bg-gradient-to-br from-emerald-50 to-emerald-50 flex items-center justify-center shadow-sm">
                <Sparkles class="w-8 h-8 text-emerald-600" />
              </div>
              <div>
                <h4 class="text-base font-bold text-gray-700">曲率 AI 助手</h4>
                <p class="text-xs text-gray-500 mt-1 leading-relaxed">
                  可以帮你管理曲库、查看状态、开关功能。<br>
                  写操作会先确认再执行。
                </p>
              </div>
              <div class="flex w-full max-w-md flex-col items-center gap-2">
                <div class="flex flex-wrap gap-2 justify-center max-w-full">
                  <button
                    v-for="q in suggestions"
                    :key="q"
                    @click="sendMessage(q)"
                    class="px-3 py-1.5 rounded-full text-xs font-medium bg-white/80 backdrop-blur border border-[var(--ys-line-control)] text-gray-600 hover:border-emerald-200 hover:text-emerald-600 transition-colors"
                  >
                    {{ q }}
                  </button>
                </div>
                <!-- 换一批：池子里有 30+ 条，随机抽 4 条显示 -->
                <button
                  @click="reshuffleSuggestions()"
                  class="mt-1 inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-[11px] font-medium text-gray-500 transition-colors hover:text-emerald-600"
                  title="换一批提示词"
                >
                  <RefreshCw class="h-3 w-3" />
                  换一批
                </button>
              </div>
            </div>

            <!-- 消息列表 -->
            <template v-for="(msg, i) in messages" :key="i">
              <!-- 用户消息 -->
              <div v-if="msg.role === 'user'" class="flex justify-end">
                <div class="max-w-[80%] px-3.5 py-2 rounded-2xl rounded-br-md bg-emerald-600 text-white text-sm leading-relaxed whitespace-pre-wrap shadow-sm">
                  {{ msg.content }}
                </div>
              </div>

              <!-- 助手消息（含工具调用） -->
              <div v-else-if="msg.role === 'assistant'" class="flex flex-col gap-2">
                <!-- 工具调用卡片 -->
                <div
                  v-for="(tc, ti) in msg.toolCalls || []"
                  :key="ti"
                  class="rounded-xl border border-[var(--ys-border)] bg-[var(--ys-surface-sunken)] px-3 py-2.5"
                >
                  <div class="flex items-center gap-1.5 mb-1">
                    <Wrench class="w-3.5 h-3.5 text-gray-500 shrink-0" />
                    <span class="text-[11px] font-semibold text-gray-500 font-mono">{{ tc.name }}</span>
                    <span
                      v-if="tc.status === 'confirming'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-amber-100 text-amber-700 text-[10px] font-semibold"
                    >待确认</span>
                    <span
                      v-else-if="tc.status === 'pending'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-sky-100 text-sky-700 text-[10px] font-semibold"
                    >执行中</span>
                    <span
                      v-else-if="tc.status === 'done'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-600 text-[10px] font-semibold"
                    >完成</span>
                    <span
                      v-else-if="tc.status === 'rejected'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-red-100 text-red-700 text-[10px] font-semibold"
                    >已拒绝</span>
                    <span
                      v-else-if="tc.status === 'failed'"
                      class="ml-auto px-2 py-0.5 rounded-full bg-red-100 text-red-700 text-[10px] font-semibold"
                    >失败</span>
                  </div>
                  <!-- 工具参数 -->
                  <pre v-if="tc.args" class="text-[10px] text-gray-500 font-mono whitespace-pre-wrap break-all bg-white/60 rounded-lg p-2 mb-1.5">{{ formatArgs(tc.args) }}</pre>
                  <!-- 工具结果 -->
                  <pre v-if="tc.result" class="text-[10px] text-gray-600 font-mono whitespace-pre-wrap break-all bg-white rounded-lg p-2 border border-[var(--ys-border)]">{{ truncateResult(tc.result) }}</pre>
                  <!-- 待确认：告诉用户「到底要做什么」，而不是只显示能力名 -->
                  <div
                    v-if="tc.status === 'confirming' && tc.actionId"
                    class="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2"
                  >
                    <div class="text-[11px] font-semibold text-amber-800 mb-0.5">需要你确认</div>
                    <div class="text-xs text-amber-900 leading-relaxed">{{ tc.detail || tc.summary || tc.name }}</div>
                  </div>
                  <!-- 待确认操作按钮 -->
                  <div v-if="tc.status === 'confirming' && tc.actionId" class="flex gap-2 mt-2">
                    <button
                      @click="confirmAction(tc.actionId, true)"
                      :disabled="streaming"
                      class="flex-1 px-3 py-1.5 rounded-lg text-xs font-semibold bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50 transition-colors"
                    >
                      确认执行
                    </button>
                    <button
                      @click="confirmAction(tc.actionId, false)"
                      :disabled="streaming"
                      class="flex-1 px-3 py-1.5 rounded-lg text-xs font-medium bg-white text-gray-500 border border-[var(--ys-line-control)] hover:bg-[var(--ys-surface-sunken)] disabled:opacity-50 transition-colors"
                    >
                      拒绝
                    </button>
                  </div>
                </div>

                <!-- 助手正文 -->
                <div v-if="msg.content" class="max-w-[85%] px-3.5 py-2 rounded-2xl rounded-bl-md bg-white border border-[var(--ys-border)] text-sm leading-relaxed text-gray-700 whitespace-pre-wrap shadow-sm">
                  {{ msg.content }}
                </div>
              </div>

              <!-- 错误消息 -->
              <div v-else-if="msg.role === 'error'" class="rounded-xl border border-red-100 bg-red-50 px-3.5 py-2.5 text-sm text-red-600">
                {{ msg.content }}
              </div>
            </template>

            <!-- 正在生成指示器 -->
            <div v-if="streaming" class="flex items-center gap-2 text-gray-500">
              <span class="inline-flex gap-0.5">
                <span class="w-1.5 h-1.5 rounded-full bg-[var(--ys-line-control)] animate-bounce" style="animation-delay: 0ms"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-[var(--ys-line-control)] animate-bounce" style="animation-delay: 100ms"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-[var(--ys-line-control)] animate-bounce" style="animation-delay: 200ms"></span>
              </span>
              <span class="text-xs">助手正在思考…</span>
            </div>
          </div>

          <!-- 输入区 -->
          <div class="ys-ai-layer relative px-7 pb-5 pt-4 border-t border-[var(--ys-border)] bg-white shrink-0">
            <div class="flex gap-2 items-end">
              <textarea
                ref="inputRef"
                v-model="inputText"
                @keydown.enter.exact.prevent="sendMessage()"
                placeholder="输入消息…（Enter 发送）"
                enterkeyhint="send"
                rows="1"
                class="flex-1 resize-none px-3 py-2 rounded-xl border border-[var(--ys-line-control)] text-sm focus:outline-none focus:border-emerald-200 focus:ring-2 focus:ring-emerald-50 transition-colors max-h-32"
              ></textarea>
              <button
                @click="sendMessage()"
                :disabled="!inputText.trim() || streaming || !settings.enabled"
                class="px-4 py-2 rounded-xl bg-emerald-600 text-white text-sm font-medium disabled:opacity-40 disabled:cursor-not-allowed hover:bg-emerald-700 transition-colors shrink-0"
              >
                发送
              </button>
            </div>
            <p v-if="!settings.enabled" class="text-[10px] text-amber-500 mt-1.5 text-center">
              助手已关闭，点右上齿轮在设置里开启
            </p>
            <p v-else-if="aiState === 'unconfigured'" class="text-[10px] text-amber-500 mt-1.5 text-center">
              未配置 AI 大模型：到「账号连接 → AI 大模型」填 Base URL / API Key 并启用
            </p>
            <p v-else-if="aiState === 'disabled'" class="text-[10px] text-amber-500 mt-1.5 text-center">
              AI 已配置但未启用：到「账号连接 → AI 大模型」勾选「启用」并保存
            </p>
          </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, reactive, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { Sparkles, X, Settings2, ChevronDown, Wrench, History, Plus, Trash2, MessageSquare, RefreshCw } from 'lucide-vue-next'
import { AssistantAPI, AIAPI } from '../api/client'

const props = defineProps({
  modelValue: Boolean,
})
const emit = defineEmits(['update:modelValue'])

const close = () => emit('update:modelValue', false)

// ── 状态 ──
const messages = ref([])
const inputText = ref('')
const streaming = ref(false)
const showSettings = ref(false)
const showSessions = ref(false)
const msgContainer = ref(null)
const inputRef = ref(null)
const haloRef = ref(null)

/**
 * 入场动画的计时器组（2026-10-29 简化）。
 *
 * 这里原先还有一套「遮罩模糊淡入」控制器（backdropBlurring / beginBackdropBlur，
 * 配合遮罩上的 data-ai-backdrop 标记做 blur(0)→blur(10px) 过渡）。第四次修正把
 * 遮罩改成「只暗化、不模糊」、面板改成纯白实底后，那套状态没有任何消费方，已删除。
 */
let animTimer = 0
/**
 * ⚠️ 2026-10-30 修「ReferenceError: Cannot access uninitialized variable.」
 *
 * 同一个 immediate watch 里，除了 decorTimer 还读写 `decorOn`（bundle 里是 E.value）。
 * decorOn 原先声明在 watch 之后（586 行），于是 immediate 那次同步执行
 * 走到 `decorOn.value = false` 就踩进暂时性死区 → ReferenceError，
 * 面板初始化整体断掉（2026-10-29 只把 decorTimer 上移，漏了 decorOn）。
 * 与 decorTimer 一样，把声明提到 watch 之前即可。
 */
const decorOn = ref(false)
/**
 * dataTimer 同理：onUnmounted 里 clearTimeout(dataTimer)，
 * 而声明原本在 1184 行（onUnmounted 之后）。钩子体虽在卸载时才跑，
 * 但为了不再留同类隐患，一并上移。
 */
let dataTimer = 0
/**
 * ⚠️ 2026-10-29 修「ReferenceError: Cannot access 'decorTimer' before initialization」。
 *
 * 下面这个 watch 是 `{ immediate: true }`：它在**定义的那一行就同步跑一遍**，
 * 而 `let decorTimer = 0` 写在 30 行之后（decorOn 那块的说明下面）。
 * 于是 immediate 那次执行读到的是**还在暂时性死区里的** decorTimer → 抛 ReferenceError，
 * 整个 assistant 面板的初始化都在这里断掉。两处调用（clearTimeout(decorTimer)）都在
 * watch 体内，所以把声明提到 watch 之前即可；`decorTimer = setTimeout(...)` 是调用时才执行，
 * 不依赖声明位置，保持原样（仍跟着 decorOn 那段说明走）。
 */
let decorTimer = 0
watch(
  () => props.modelValue,
  (open) => {
    clearTimeout(decorTimer)
    if (open) {
      // 光效延后一帧：先让面板/文字上屏，再把贵的层挂上（见 decorOn 的说明）
      decorOn.value = false
      decorTimer = setTimeout(() => {
        requestAnimationFrame(() => { decorOn.value = true })
      }, 0)
    } else {
      decorOn.value = false
    }
    // data-ai-open：弹窗是否开着的全局标记（CSS 里已无规则消费，保留给将来的层用）
    const root = document.documentElement
    if (open) root.dataset.aiOpen = '1'
    else delete root.dataset.aiOpen
    // data-ai-anim：入场/退场这段（约 300ms）的标记，便于给动画中的层做开关。
    clearTimeout(animTimer)
    root.dataset.aiAnim = '1'
    animTimer = setTimeout(() => { delete root.dataset.aiAnim }, open ? 340 : 220)
  },
  { immediate: true },
)
onUnmounted(() => {
  clearTimeout(decorTimer)
  clearTimeout(animTimer)
  clearTimeout(dataTimer)
  delete document.documentElement.dataset.aiAnim
  delete document.documentElement.dataset.aiOpen
})

/**
 * 「重光效延后一拍」（2026-10-29 修「点 AI 助手有近一秒延迟才弹出来」）。
 *
 * 为什么点下去不像立刻弹出来：DOM 本身 9ms 就进去了（实测），慢的是**首帧光栅化**。
 * 弹窗第一帧要同时算出：玻璃面板的 backdrop-filter blur(24px) + saturate(1.6)、
 * 遮罩的 blur(10px)、外圈色盘、五档各自 blur(2.5/9/18/28/44px) 的环带。
 * 这些一次性叠在**同一帧**里，冷启动实测首帧 101ms（软件渲染环境；真机 GPU 也遵循同一比例），
 * 用户看到的就是"点了之后停一下才出来"。
 *
 * 做法：把这几层装饰**往后挪一帧**再挂上（decorOn 默认 false，mount 后 rAF 才置 true）。
 * 于是第一帧只有面板 + 文字 + 输入框（很便宜，立刻可见、立刻能打字），
 * 第二帧才开始光栅化光效，而那时面板已经在屏幕上、用户的注意力已经在内容上。
 * 视觉上不是"光效慢慢出现"（只差一帧，人眼分辨不出来），但感知延迟基本消失。
 *
 * ⚠️ 只影响光效，**不动任何布局**：装饰层一直是 position:absolute + pointer-events:none，
 *    在不在都撑不动尺寸，也不会拦输入。
 */
/* decorOn / decorTimer / dataTimer 的声明已上移到 watch 之前（immediate 会同步执行，见那里的说明） */
// AI 配置（aiCfg.available / configured / enabled 都来自后端，别再自己猜）
const aiCfg = ref(null)
const models = ref([])
const modelError = ref('')
const modelSaveMsg = ref('')
const saveError = ref('')
const saving = ref(false)  // 设置保存互斥，防连点错乱
const saveOk = ref('')     // 「已保存」短提示，让用户确认这一下真的生效了

// 会话（后端已落盘，重启不丢）
const sessions = ref([])
const currentSessionId = ref('')

// 助手设置（从后端加载）
const settings = reactive({
  enabled: true,
  mode: 'confirm_writes',
  disabled: {},
})
const capabilities = ref([])

const modeOptions = [
  { value: 'read_only', label: '只读' },
  { value: 'confirm_writes', label: '确认写入' },
  { value: 'full_auto', label: '全自动' },
]

const modeLabel = computed(() => {
  const m = modeOptions.find(o => o.value === settings.mode)
  return m ? m.label : settings.mode
})

const modeDesc = computed(() => {
  if (settings.mode === 'read_only') return '助手只能查询信息，不会调用任何写入操作'
  if (settings.mode === 'confirm_writes') return '助手可查询和发起写入，但每次写入前都需要你确认'
  return '助手可自由调用所有能力（高风险操作仍需确认）'
})

// ready：能用；unconfigured：连 Key 都没配；disabled：配了但没启用
const aiState = computed(() => {
  if (!aiCfg.value) return 'unknown'
  if (!aiCfg.value.configured) return 'unconfigured'
  if (!aiCfg.value.enabled) return 'disabled'
  return 'ready'
})

const currentModel = computed(() => (aiCfg.value && aiCfg.value.model) || '')

// 模型下拉选项：网关列表 + 当前模型兜底（当前模型不在列表里也要能显示）
const modelOptions = computed(() => {
  const list = [...models.value]
  const cur = currentModel.value
  if (cur && !list.includes(cur)) list.unshift(cur)
  return list
})

/**
 * 空状态提示词池（2026-10-28 用户：「提示词不固定，多增加随机便捷提示词」）。
 *
 * 每次打开弹窗 / 每次清空对话，从池子里随机抽 4 条不同的显示 —— 固定 4 条看两天就腻了，
 * 而且固定的几条会让人以为「助手只会这几件事」。池子按「查状态 / 管曲库 / 开关功能 /
 * 下载 / 诊断」分组，保证抽出来的 4 条尽量不撞同一类。
 */
const SUGGESTION_POOL = [
  // —— 查状态 ——
  '曲库有多少首歌？',
  '下载队列里有什么？',
  '现在在播什么歌？',
  '音源都还能用吗？',
  'AI 这个月用了多少 token？',
  '服务运行多久了？',
  '最近有没有报错？',
  '监听目录里还有多少没扫的？',
  // —— 曲库整理 ——
  '帮我整理曲库',
  '有多少首歌缺歌词？',
  '有多少首歌没有封面？',
  '哪些歌没有歌名？',
  '找出重复的歌曲',
  '给我一份曲库体检报告',
  '哪些歌的标签不完整？',
  '专辑分类乱不乱？',
  // —— 开关功能 ——
  '开启榜单注入',
  '关掉收藏自动下载',
  '现在都开了哪些开关？',
  '打开歌词自动下载',
  '把封面嵌入打开',
  // —— 下载 ——
  '帮我下载周杰伦的晴天',
  '下载队列卡住了吗？',
  '最近都下载了什么？',
  '下载失败的歌有哪些？',
  // —— 诊断 / 帮助 ——
  '你能做什么？',
  '帮我看看哪里出问题了',
  '看看最近的操作日志',
  '为什么某首歌播不了？',
  '我该从哪里开始？',
]

/** 从池子里随机抽 n 条不重复的（Fisher-Yates 前 n 项，池子很小，够用）。 */
function pickSuggestions(n = 4) {
  const pool = [...SUGGESTION_POOL]
  for (let i = pool.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[pool[i], pool[j]] = [pool[j], pool[i]]
  }
  return pool.slice(0, n)
}

const suggestions = ref(pickSuggestions())

/** 换一批：空状态里那个「换一批」按钮和打开弹窗时都会调。 */
function reshuffleSuggestions() {
  suggestions.value = pickSuggestions()
}

// ── 数据加载 ──
// client.js 已把 {code, xxx} 信封拆好，这里拿到什么用什么，不再 res.data || res。
async function loadSettings() {
  try {
    const s = await AssistantAPI.getSettings()
    if (!s) throw new Error('设置接口没有返回内容')
    settings.enabled = s.enabled ?? true
    settings.mode = s.mode || 'confirm_writes'
    settings.disabled = s.disabled || {}
  } catch (e) {
    // 读不到就用默认值，但要让用户知道「看到的不一定是服务端的」，
    // 否则一改开关就「保存失败」，人根本不知道是读失败了。
    saveError.value = `读取设置失败：${e.message || e}`
  }
}

async function loadCapabilities() {
  try {
    capabilities.value = await AssistantAPI.capabilities()
  } catch (e) {
    capabilities.value = []
  }
}

async function loadAIConfig() {
  try {
    const env = await AIAPI.getConfig()
    // getConfig 返回的是 {code,message,data:{...}} 信封本体
    aiCfg.value = (env && env.data) || env || null
  } catch (e) {
    aiCfg.value = null
  }
}

async function loadModels() {
  modelError.value = ''
  try {
    const res = await AIAPI.models()
    models.value = (res && res.models) || []
    if (res && res.error) modelError.value = res.error
  } catch (e) {
    modelError.value = e.message || String(e)
  }
}

// ── 助手设置 ──
async function updateSettings(patch) {
  // 先本地生效（乐观更新），失败再回滚并亮出错误——之前静默吞异常，
  // 用户点三下设置「一点反应都没有」就是这么来的
  const before = { enabled: settings.enabled, mode: settings.mode, disabled: { ...settings.disabled } }
  if ('mode' in patch) settings.mode = patch.mode
  if ('enabled' in patch) settings.enabled = !!patch.enabled
  if ('disabled' in patch) settings.disabled = { ...patch.disabled }
  saveError.value = ''
  // 同一时刻只允许一个保存请求：连点时后一个请求体可能基于「还没回来的旧值」，
  // 出现关→开→关的错乱，用户看到的就是「保存失败 / 点了没用」。
  if (saving.value) return
  saving.value = true
  try {
    const s = await AssistantAPI.saveSettings(patch)
    if (s) {
      settings.enabled = s.enabled ?? settings.enabled
      settings.mode = s.mode || settings.mode
      settings.disabled = s.disabled || settings.disabled
    }
  } catch (e) {
    settings.enabled = before.enabled
    settings.mode = before.mode
    settings.disabled = before.disabled
    saveError.value = `保存失败：${e.message || e}`
  } finally {
    saving.value = false
  }
  if (!saveError.value) {
    saveOk.value = '已保存'
    setTimeout(() => { if (saveOk.value === '已保存') saveOk.value = '' }, 1800)
  }
}

function toggleCapability(name) {
  const next = { ...settings.disabled, [name]: !settings.disabled[name] }
  updateSettings({ disabled: next })
}

// ── 对话模型 ──
async function saveModel(model) {
  if (!model || !aiCfg.value) return
  modelSaveMsg.value = ''
  try {
    // UpdateConfig 的 enabled 是「显式传参」语义：必须带上当前值，
    // 否则切个模型就把整个 AI 关了；api_key 传空 = 沿用旧 Key
    const env = await AIAPI.saveConfig({
      enabled: aiCfg.value.enabled,
      base_url: aiCfg.value.base_url,
      api_key: '',
      model,
      timeout: aiCfg.value.timeout,
    })
    const cfg = (env && env.data) || env
    if (cfg) aiCfg.value = cfg
    modelSaveMsg.value = `模型已切换为 ${cfg?.model || model}`
  } catch (e) {
    modelSaveMsg.value = `保存失败：${e.message || e}`
  }
  setTimeout(() => { modelSaveMsg.value = '' }, 3000)
}

// ── 会话 ──
async function loadSessions() {
  try {
    sessions.value = await AssistantAPI.sessions()
  } catch (e) {
    sessions.value = []
  }
}

function toggleSessions() {
  showSessions.value = !showSessions.value
  if (showSessions.value) {
    showSettings.value = false
    loadSessions()
  }
}

async function createSession() {
  if (streaming.value) return
  try {
    const s = await AssistantAPI.newSession()
    currentSessionId.value = (s && s.id) || ''
  } catch (e) {
    // 新建失败时退回内存自增 id，聊天仍可用（后端 chat 空 session_id 会落 default）
    currentSessionId.value = ''
  }
  messages.value = []
  showSessions.value = false
  scrollBottom()
}

async function switchSession(id) {
  if (streaming.value || !id) return
  try {
    const sess = await AssistantAPI.history(id)
    if (!sess) return
    currentSessionId.value = sess.id || id
    messages.value = renderHistory(sess.messages || [])
  } catch (e) {
    // 历史拉不到：切过去但清屏，不打断用户
    currentSessionId.value = id
    messages.value = []
  }
  showSessions.value = false
  scrollBottom()
}

async function deleteSession(id) {
  try {
    await AssistantAPI.removeSession(id)
  } catch (e) {
    // 删除失败：刷新列表反映真实状态
  }
  await loadSessions()
  if (id === currentSessionId.value) {
    // 删的是当前会话：跳到最新的一个；没有就新建
    if (sessions.value.length > 0) {
      await switchSession(sessions.value[0].id)
    } else {
      await createSession()
    }
  }
}

// 把后端历史消息（含 tool_calls / tool 结果）映射成界面消息流：
//   user → 用户气泡；assistant → 气泡 + 工具卡片；tool → 结果回填到前一条 assistant 的卡片
// system 行直接丢弃（不该出现在历史里）。
function renderHistory(raw) {
  const out = []
  let lastAssistant = null
  for (const m of raw) {
    if (m.role === 'user') {
      out.push({ role: 'user', content: m.content })
      lastAssistant = null
    } else if (m.role === 'assistant') {
      const msg = { role: 'assistant', content: m.content || '', toolCalls: [] }
      for (const call of m.tool_calls || []) {
        const fn = call.function || {}
        const args = safeParseArgs(fn.arguments)
        msg.toolCalls.push({
          id: call.id || '',
          name: fn.name || '',
          args,
          summary: '',
          detail: '',
          result: '',
          // 历史里的调用已经有过结果，先记 done；下面由 tool 行回填真实结果与状态
          status: 'done',
          actionId: '',
        })
      }
      if (!msg.content && !msg.toolCalls.length) continue
      out.push(msg)
      lastAssistant = msg
    } else if (m.role === 'tool') {
      if (!lastAssistant) continue
      // 优先按 tool_call_id 匹配；没有 id 时按顺序回填第一个没结果的卡片
      let tc = null
      if (m.tool_call_id) tc = lastAssistant.toolCalls.find(t => t.id === m.tool_call_id)
      if (!tc) tc = lastAssistant.toolCalls.find(t => !t.result)
      if (tc) {
        tc.result = m.content || ''
        tc.status = 'done'
      }
    }
  }
  return out
}

function safeParseArgs(raw) {
  if (!raw) return {}
  if (typeof raw === 'object') return raw
  try {
    return JSON.parse(raw)
  } catch {
    return { raw: String(raw) }
  }
}

// ── 对话 ──
async function sendMessage(text) {
  const content = (text || inputText.value).trim()
  if (!content || streaming.value || !settings.enabled) return

  inputText.value = ''
  messages.value.push({ role: 'user', content })
  streaming.value = true
  scrollBottom()

  // 助手占位消息（后续增量填充）
  const assistantMsg = reactive({ role: 'assistant', content: '', toolCalls: [] })
  messages.value.push(assistantMsg)

  let streamText = ''

  const ensureTC = (name, args, actionId) => {
    // 同一个 actionId 视为同一张卡（后端「发起」和「完成」是两条事件）
    let tc = actionId
      ? assistantMsg.toolCalls.find(t => t.actionId === actionId)
      : null
    if (!tc) tc = assistantMsg.toolCalls.find(t => t.name === name && t.status !== 'done' && t.status !== 'failed')
    if (!tc) {
      tc = reactive({ name, summary: '', detail: '', args: args || {}, result: '', status: 'pending', actionId: actionId || '' })
      assistantMsg.toolCalls.push(tc)
    }
    if (args && Object.keys(args).length) tc.args = args
    if (actionId) tc.actionId = actionId
    return tc
  }

  try {
    await AssistantAPI.chat(content, currentSessionId.value, ({ event, data }) => {
      switch (event) {
        case 'meta':
          if (data.session_id) currentSessionId.value = data.session_id
          break
        case 'role':
          if (data.role === 'assistant') {
            streamText = ''
          }
          break
        case 'text':
          streamText += data.text || ''
          assistantMsg.content = streamText
          scrollBottom()
          break
        case 'tool':
          {
            // 后端把「发起」和「完成」都放在 tool 事件里（靠 status 区分），
            // 且等待确认时带 action_id —— 确认卡必须挂上 actionId，否则
            // 界面只显示一个「待确认」标签却没有按钮（用户就点不了）。
            const tc = ensureTC(data.name, data.args, data.action_id)
            if (data.summary && !tc.summary) tc.summary = data.summary
            if (data.action_id) tc.actionId = data.action_id
            if (data.status === 'awaiting_confirmation') {
              tc.status = 'confirming'
            } else if (data.status === 'executing') {
              tc.status = 'pending'
            } else if (data.status === 'done') {
              tc.status = 'done'
              if (data.result) tc.result = data.result
            } else if (data.status === 'error' || data.status === 'blocked') {
              tc.status = data.status === 'blocked' ? 'rejected' : 'failed'
              if (data.result) tc.result = data.result
              if (data.reason) tc.result = data.reason
            }
            scrollBottom()
          }
          break
        case 'tool_result':
          {
            const tc = assistantMsg.toolCalls.find(t => t.name === data.name)
            if (tc) {
              tc.result = data.result || ''
              tc.status = data.status || 'done'
            }
            scrollBottom()
          }
          break
        case 'action':
          {
            // action 事件本身带 detail（人话描述）和 args，挂到对应卡片上
            const tc = ensureTC(data.name || data.capability, data.args, data.action_id || data.id)
            tc.actionId = data.action_id || data.id || tc.actionId
            if (data.detail) tc.detail = data.detail
            if (data.summary) tc.summary = data.summary
            if (data.args && Object.keys(data.args).length) tc.args = data.args
            if (data.status === 'pending') tc.status = 'confirming'
            scrollBottom()
          }
          break
        case 'error':
          assistantMsg.content = `⚠️ ${data.message || data.error || '出错了'}`
          assistantMsg.role = 'error'
          break
        case 'done':
          break
      }
    })
  } catch (e) {
    assistantMsg.role = 'error'
    assistantMsg.content = `请求失败：${e.message || '未知错误'}`
  } finally {
    streaming.value = false
    scrollBottom()
    // 静默刷新会话列表：标题 / 条数 / 排序（当前会话会浮到最上面）
    loadSessions()
  }
}

async function confirmAction(actionId, approved) {
  // 找到这张卡（可能在任何一条助手消息里）
  let tc = null
  let owner = null
  for (const msg of messages.value) {
    const found = (msg.toolCalls || []).find(t => t.actionId === actionId)
    if (found) { tc = found; owner = msg; break }
  }
  if (!tc) return

  // 拒绝：无需等后端回话，直接收尾
  if (!approved) {
    tc.status = 'rejected'
    tc.result = '已拒绝，未执行。'
    try {
      await AssistantAPI.confirm(actionId, false)
    } catch (e) {
      /* 拒绝失败也无妨，后端那条 pending 一小时后自动过期 */
    }
    return
  }

  tc.status = 'pending'
  tc.result = '正在执行…'
  streaming.value = true
  scrollBottom()

  // 执行结果和助手的后续回复从流里回来：新建一条助手消息承接
  const followMsg = reactive({ role: 'assistant', content: '', toolCalls: [] })
  messages.value.push(followMsg)
  let streamText = ''

  try {
    await AssistantAPI.confirm(actionId, true, ({ event, data }) => {
      switch (event) {
        case 'tool': {
          const name = data.name || tc.name
          const status = data.status === 'executing' ? 'pending' : (data.status === 'error' ? 'failed' : 'done')
          tc.status = status
          if (data.result) tc.result = data.result
          scrollBottom()
          break
        }
        case 'action':
          if (data.action_id === actionId || data.id === actionId) {
            tc.status = data.status === 'failed'
              ? 'failed'
              : data.status === 'executed' ? 'done' : 'confirming'
            if (data.result) tc.result = data.result
            if (data.message) tc.result = data.message
          }
          break
        case 'role':
          if (data.role === 'assistant') streamText = ''
          break
        case 'text':
          streamText += data.text || ''
          followMsg.content = streamText
          scrollBottom()
          break
        case 'error':
          followMsg.role = 'error'
          followMsg.content = `⚠️ ${data.message || data.error || '执行出错'}`
          // 后端在「已处理过 / 策略不允许」这类情况下只回错误事件，
          // 卡片不能一直停在「正在执行…」，否则看起来像卡死。
          if (tc.status === 'pending') {
            tc.status = 'failed'
            if (!tc.result || tc.result === '正在执行…') tc.result = data.message || '未能执行'
          }
          break
        default:
          break
      }
    })
  } catch (e) {
    if (tc.status === 'pending') tc.status = 'failed'
    followMsg.role = 'error'
    followMsg.content = `确认执行失败：${e.message || '未知错误'}`
  } finally {
    streaming.value = false
    // 拿走没有内容的空占位（例如后端只回执行结果、模型没有额外话说）
    if (!followMsg.content) {
      const i = messages.value.indexOf(followMsg)
      if (i >= 0) messages.value.splice(i, 1)
    }
    scrollBottom()
    loadSessions()
  }
}

function formatArgs(args) {
  if (!args) return ''
  try {
    return JSON.stringify(args, null, 2)
  } catch {
    return String(args)
  }
}

function truncateResult(result) {
  if (!result) return ''
  const str = typeof result === 'string' ? result : JSON.stringify(result, null, 2)
  return str.length > 500 ? str.slice(0, 500) + '\n…（已截断）' : str
}

function scrollBottom() {
  nextTick(() => {
    if (msgContainer.value) {
      msgContainer.value.scrollTop = msgContainer.value.scrollHeight
    }
  })
}

// 打开时：加载数据 + 会话引导（第一次打开自动接上最近一次会话，或新建）
/**
 * 打开弹窗时要抓的数据（全部是网络请求，回后端 8900）。
 *
 * ⚠️ 2026-10-29：这些请求原来写在 `watch(modelValue)` 的同一个同步段里 ——
 *    点一下按钮，同一拍里发出 5 个请求（settings/capabilities/aiconfig/models/
 *    sessions + switchSession），响应回来时正好撞上 300ms 入场动画，
 *    主线程被 fetch 回调和随之而来的重渲染占满，首帧就往后拖。
 *    实测（软件渲染）入场窗口帧率 26-31fps，把这批请求挪后直接回到 50fps+。
 * 现在统一延后到面板已经落地之后（见 data-open 的 watch），视觉上无感。
 */
async function loadDrawerData() {
  loadSettings()
  loadCapabilities()
  loadAIConfig()
  loadModels()
  if (!currentSessionId.value) {
    await loadSessions()
    if (sessions.value.length > 0) {
      await switchSession(sessions.value[0].id)
    } else {
      await createSession()
    }
  }
  scrollBottom()
}

watch(() => props.modelValue, (open) => {
  if (!open) {
    clearTimeout(dataTimer)
    return
  }
  // 每次打开都换一批提示词：见过一次的那组就不再出现，避免「永远是这 4 条」
  //（纯本地计算，几十微秒，留在同步段没有影响）
  reshuffleSuggestions()
  // 数据延后到入场动画结束再抓：先让面板上屏，再去占主线程
  clearTimeout(dataTimer)
  dataTimer = setTimeout(() => { loadDrawerData() }, 300)
})

// ── 手机端：软键盘弹出时弹窗不能跑到键盘底下去（2026-10-28 用户报「看不到弹窗」）──
//
// 弹窗是 `fixed` + `top-1/2` 居中的。手机上点输入框弹出软键盘后：
//   · iOS Safari / 飞牛 WebView 的**布局视口高度不变**，只缩小 visualViewport，
//     `fixed` 元素仍按布局视口居中 → 下半截（含输入框）被键盘盖住，看不见在打字；
//   · 地址栏收起/展开还会让 `vh` 抖动一下。
// 所以用 visualViewport 的真实高度实时写一个 CSS 变量，弹窗高度按它算，
// 并把整个弹窗往上推 offsetTop（键盘弹出时 visualViewport.offsetTop 通常仍是 0，
// 但某些 WebView 会变）。不支持的浏览器直接跳过，退化成原来的 86vh，不影响桌面。
function syncViewport() {
  const vv = window.visualViewport
  const root = document.documentElement
  if (!vv) {
    root.style.removeProperty('--ys-ai-vh')
    root.style.removeProperty('--ys-ai-vtop')
    return
  }
  // 键盘弹出时 vv.height 明显小于布局视口高度 → 用 vv.height 当可用高度
  root.style.setProperty('--ys-ai-vh', `${Math.round(vv.height)}px`)
  root.style.setProperty('--ys-ai-vtop', `${Math.round(vv.offsetTop)}px`)
}

function bindViewport() {
  syncViewport()
  const vv = window.visualViewport
  if (!vv) return
  vv.addEventListener('resize', syncViewport)
  vv.addEventListener('scroll', syncViewport)
}

function unbindViewport() {
  const vv = window.visualViewport
  if (!vv) return
  vv.removeEventListener('resize', syncViewport)
  vv.removeEventListener('scroll', syncViewport)
  const root = document.documentElement
  root.style.removeProperty('--ys-ai-vh')
  root.style.removeProperty('--ys-ai-vtop')
}

onMounted(() => {
  loadSettings()
  loadCapabilities()
  loadAIConfig()
  bindViewport()
})

onUnmounted(unbindViewport)
</script>
