<template>
  <!--
    `plain`：被 UpdateModal 装进弹窗时置 true。外壳（圆角/边框/背景/内边距）和
    「版本更新」这个标题都由弹窗自己出，卡片再画一遍就是同一件事说两遍。
  -->
  <section :class="plain ? '' : 'rounded-[var(--ys-radius-card)] border border-[var(--ys-border)] bg-white p-4 sm:p-5'">
    <div v-if="!plain" class="flex items-start gap-3">
      <span class="grid h-9 w-9 shrink-0 place-items-center rounded-[8px] bg-[var(--ys-accent-soft)] text-[var(--ys-accent)]">
        <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': checking || phase === 'installing' }" :stroke-width="1.8" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-[14px] font-semibold tracking-[-0.01em] text-[var(--ys-text)]">版本更新</h2>
          <span
            v-if="hasUpdate && !installed"
            class="rounded-full bg-[var(--ys-accent-soft)] px-2 py-0.5 text-[10.5px] font-medium text-[var(--ys-accent)]"
          >有新版本</span>
        </div>
        <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
          从 GitHub Release 检查、下载并安装 fpk。下载和安装由 NAS 后台执行，关掉本页也会继续。
        </p>
      </div>
    </div>

    <div class="mt-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="min-w-0">
        <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span class="text-[11px] text-[var(--ys-text-muted)]">当前</span>
          <span class="font-mono text-[13px] font-medium text-[var(--ys-text)]">{{ current || '…' }}</span>
          <template v-if="latest && hasUpdate">
            <ArrowRight class="h-3.5 w-3.5 shrink-0 text-[var(--ys-text-aux)]" :stroke-width="1.8" />
            <span class="text-[11px] text-[var(--ys-text-muted)]">最新</span>
            <span class="font-mono text-[13px] font-semibold text-[var(--ys-accent)]">{{ latest }}</span>
          </template>
        </div>
        <div v-if="sizeLabel && active" class="mt-1 text-[11px] text-[var(--ys-text-aux)]">
          安装包 {{ sizeLabel }}
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-2">
        <button
          v-if="installed"
          type="button"
          class="ys-press rounded-[8px] bg-[var(--ys-accent)] px-3.5 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-[var(--ys-accent-hover)]"
          @click="reloadPage"
        >刷新页面</button>
        <button
          v-else
          type="button"
          class="ys-press rounded-[8px] px-3.5 py-1.5 text-[12px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          :class="hasUpdate
            ? 'bg-[var(--ys-accent)] text-white hover:bg-[var(--ys-accent-hover)]'
            : 'border border-[var(--ys-border)] bg-white text-[var(--ys-text-muted)] hover:text-[var(--ys-text)]'"
          :disabled="busy"
          :title="primaryHint"
          @click="onPrimary"
        >{{ primaryLabel }}</button>
      </div>
    </div>

    <!-- 状态一行：下载/安装/失败/已最新。失败时把后端 message 原样放出来。 -->
    <p
      v-if="statusText"
      class="mt-3 text-[11.5px] leading-relaxed"
      :class="phase === 'error' ? 'text-rose-600' : 'text-[var(--ys-text-muted)]'"
      aria-live="polite"
    >{{ statusText }}</p>

    <!-- 连不上时直接把出口指出来：这类报错（i/o timeout、connection refused、
         connection reset）几乎都是「这个网络出不去 GitHub」，而不是版本或权限问题。
         2026-10-25 用户就是卡在这里：本机能开代理，但应用由飞牛拉起、读不到环境变量。 -->
    <p
      v-if="proxyHintNeeded"
      class="mt-2 rounded-[8px] bg-[#FFF8E6] px-2.5 py-2 text-[11px] leading-relaxed text-[#8A6100]"
    >
      看起来是网络出不去 GitHub。如果这台机器需要代理才能访问外网，在下面
      <button
        type="button"
        class="font-medium underline decoration-dotted underline-offset-2"
        @click="settingsOpen = true"
      >网络代理</button>
      里填上地址即可（不用改环境变量、不用重启应用）。
    </p>

    <div v-if="active" class="mt-2">
      <div class="h-1.5 overflow-hidden rounded-full bg-[var(--ys-surface-sunken)]">
        <div
          class="h-full rounded-full bg-[var(--ys-accent)] transition-[width] duration-500"
          :class="{ 'animate-pulse': phase === 'ready' || phase === 'installing' }"
          :style="{ width: `${Math.max(3, progress)}%` }"
        />
      </div>
      <div class="mt-1 flex justify-between text-[10.5px] text-[var(--ys-text-aux)]">
        <span>{{ progress }}%</span>
        <span v-if="phase === 'downloading' && sizeLabel">{{ sizeLabel }}</span>
        <span v-else-if="phase === 'installing'">正在安装</span>
      </div>
    </div>

    <p v-if="installed" class="mt-3 rounded-[8px] bg-[var(--ys-accent-soft)] px-3 py-2 text-[11.5px] leading-relaxed text-[var(--ys-accent)]">
      更新已启动，应用会在几十秒内重启，稍后刷新页面。
    </p>

    <!-- 更新说明：纯文本，默认折叠。notes 来自 GitHub Release，可能是 Markdown 原文；
         这里只用 {{ }} 插值 + pre 保留换行，**不渲染 HTML**。

         2026-10-25 修（用户：「升级日志，很多东西都不显示」）：以前这块是
         `v-if="notes && hasUpdate"` —— 已是最新版时**整块消失**，用户根本读不到
         任何一版的更新内容。现在只要有 notes 就显示，并按有没有更新换标题。 -->
    <div v-if="notes" class="mt-3 border-t border-[var(--ys-border)] pt-2.5">
      <button
        type="button"
        class="flex w-full items-center gap-1.5 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
        :aria-expanded="notesOpen"
        @click="notesOpen = !notesOpen"
      >
        <ChevronDown class="h-3.5 w-3.5 shrink-0 transition-transform" :class="{ 'rotate-180': notesOpen }" :stroke-width="1.8" />
        {{ hasUpdate ? '更新说明' : '更新日志' }}
        <span v-if="notesVersion" class="font-normal text-[var(--ys-text-aux)]">v{{ notesVersion }}</span>
      </button>
      <p v-if="notesStale" class="mt-1 text-[10.5px] leading-relaxed text-[var(--ys-text-aux)]">
        上面这段是发布版 v{{ notesVersion }} 的说明；你跑的是 v{{ current }}（比发布版新），
        所以本版自己的说明还没上 GitHub Release。
      </p>
      <pre
        v-if="notesOpen"
        class="mt-2 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-[8px] bg-[var(--ys-surface-sunken)] p-3 font-sans text-[11.5px] leading-relaxed text-[var(--ys-text-muted)]"
      >{{ notes }}</pre>
    </div>

    <div v-if="checkedAt && !hasUpdate && !checkError" class="mt-1 text-[10.5px] text-[var(--ys-text-aux)]">
      上次检查：{{ checkedLabel }}
    </div>

    <!-- 下载设置（用户 2026-10-25：「新增个人token输入」＋「应用内能设置代理」）。
         默认收起：绝大多数人不需要碰；但「一直 403 / 一直下载失败」的人需要它。
         这几个值存在后端配置里（POST /api/config），改了立刻对下一次检查/下载生效，
         不必重启应用 —— Manager 每次请求前现取一次（见 appupdate.Options.ProxyFunc）。 -->
    <div class="mt-3 border-t border-[var(--ys-border)] pt-2.5">
      <button
        type="button"
        class="flex items-center gap-1.5 text-[11.5px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)]"
        :aria-expanded="settingsOpen"
        @click="settingsOpen = !settingsOpen"
      >
        <SlidersHorizontal class="h-3.5 w-3.5" :stroke-width="1.8" />
        镜像、网络与令牌
        <span class="text-[10.5px] font-normal text-[var(--ys-text-aux)]">
          {{ settingsSummary }}
        </span>
      </button>

      <div v-if="settingsOpen" class="mt-2.5 space-y-3">
        <!-- 个人 token -->
        <div>
          <div class="text-[11.5px] font-medium text-[var(--ys-text)]">GitHub 个人令牌（可选）</div>
          <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
            不填也能用，但 GitHub 对未登录来源限 <b>60 次/小时/IP</b>，共享出口下经常直接 403。
            填一个只读 token（<code class="font-mono text-[10.5px]">public_repo</code> 或 fine-grained 的 Contents 只读）后是 5000 次/小时。
          </p>
          <div class="mt-1.5 flex flex-wrap items-center gap-2">
            <input
              v-model="tokenInput"
              type="password"
              autocomplete="off"
              class="min-w-0 flex-1 rounded-[8px] border border-[var(--ys-border)] bg-white px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] outline-none placeholder:text-[var(--ys-text-aux)] focus:border-[var(--ys-accent)]"
              :placeholder="tokenSet ? '已设置（留空则不改动）' : 'github_pat_… 或 ghp_…'"
            />
            <button
              type="button"
              class="ys-press rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              :disabled="tokenSaving || !tokenInput.trim()"
              @click="saveToken"
            >保存</button>
            <button
              v-if="tokenSet"
              type="button"
              class="ys-press rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-50"
              :disabled="tokenSaving"
              @click="clearToken"
            >清除</button>
          </div>
          <p v-if="tokenNote" class="mt-1 text-[11px]" :class="tokenError ? 'text-rose-600' : 'text-[var(--ys-text-aux)]'">{{ tokenNote }}</p>
        </div>

        <!-- ── 镜像清单（用户 2026-10-25：「增加默认镜像站，可删减增加」）──
             为什么要镜像：更新分两步 —— ① 取 releases/latest 的 JSON  ② 下 fpk。
             公共「GitHub 加速」站点几乎**只加速第②步**（它们只代理 github.com），
             第一步请求 api.github.com 照样超时。所以这里默认只放实测能两步都代理的
              gh-proxy.com，并且清单末尾永远有「GitHub 官方源」兜底（用户拍板「回退」）。
             填自己的也能填：前缀（拼在原地址前）或含 {repo} 的完整更新源地址。 -->
        <div class="border-t border-[var(--ys-border)] pt-2.5">
          <div class="flex items-center justify-between gap-2">
            <div class="text-[11.5px] font-medium text-[var(--ys-text)]">更新镜像（按顺序尝试）</div>
            <button
              type="button"
              class="ys-press text-[11px] text-[var(--ys-text-muted)] underline decoration-dotted underline-offset-2 transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              :disabled="mirrorSaving"
              @click="resetMirrors"
            >恢复默认</button>
          </div>
          <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
            一个失败就自动试下一个；全部失败再回落到 GitHub 官方源。
            填 <code class="font-mono text-[10.5px]">https://你的反代/</code> 当加速前缀，
            或填含 <code class="font-mono text-[10.5px]">{repo}</code> 的完整地址当自建更新源。
          </p>

          <ul class="mt-1.5 space-y-1.5">
            <li
              v-for="(m, i) in mirrorRows"
              :key="`${m.url}-${i}`"
              class="flex items-center gap-1.5 rounded-[8px] border border-[var(--ys-border)] bg-white px-2 py-1.5"
            >
              <input
                type="checkbox"
                class="h-3.5 w-3.5 shrink-0 accent-[var(--ys-accent)]"
                :checked="!m.disabled"
                :title="m.disabled ? '已停用：点一下启用' : '启用中：取消勾选即停用'"
                @change="toggleMirror(i, !m.disabled)"
              />
              <span class="min-w-0 flex-1">
                <span class="block truncate font-mono text-[11.5px]" :class="m.disabled ? 'text-[var(--ys-text-aux)] line-through' : 'text-[var(--ys-text)]'">{{ m.url }}</span>
                <span class="block truncate text-[10.5px] text-[var(--ys-text-aux)]">{{ m.name || m.url }}</span>
              </span>
              <button
                type="button"
                class="ys-press shrink-0 rounded-[8px] px-1.5 py-0.5 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-[var(--ys-surface-hover)] disabled:opacity-30"
                :disabled="i === 0 || mirrorSaving"
                title="上移（越靠前越先试）"
                @click="moveMirror(i, -1)"
              >↑</button>
              <button
                type="button"
                class="ys-press shrink-0 rounded-[8px] px-1.5 py-0.5 text-[11px] text-[var(--ys-text-muted)] transition-colors hover:bg-[var(--ys-surface-hover)] disabled:opacity-30"
                :disabled="i === mirrorRows.length - 1 || mirrorSaving"
                title="下移"
                @click="moveMirror(i, 1)"
              >↓</button>
              <button
                type="button"
                class="ys-press shrink-0 rounded-[8px] px-1.5 py-0.5 text-[11px] text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-30"
                :disabled="mirrorSaving"
                title="删除这条镜像"
                @click="removeMirror(i)"
              >删</button>
            </li>
            <li class="text-[11px] text-[var(--ys-text-muted)]">
              GitHub 官方源
              <span class="text-[var(--ys-text-aux)]">
                · {{ officialDisabled ? '已停用（现在只能用上面的镜像）' : '内置兜底，永远最后尝试，不可删除' }}
              </span>
              <button
                type="button"
                class="ml-1 underline decoration-dotted underline-offset-2 disabled:opacity-50"
                :disabled="mirrorSaving"
                @click="toggleOfficial"
              >{{ officialDisabled ? '重新启用' : '停用' }}</button>
            </li>
          </ul>

          <div class="mt-1.5 flex flex-wrap items-center gap-2">
            <input
              v-model="mirrorInput"
              type="text"
              autocomplete="off"
              spellcheck="false"
              class="min-w-0 flex-1 rounded-[8px] border border-[var(--ys-border)] bg-white px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] outline-none placeholder:text-[var(--ys-text-aux)] focus:border-[var(--ys-accent)]"
              placeholder="https://gh-proxy.com 或 https://自建/{repo}/latest.json"
            />
            <button
              type="button"
              class="ys-press rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              :disabled="mirrorSaving || !mirrorInput.trim()"
              @click="addMirror"
            >添加</button>
          </div>
          <p v-if="mirrorNote" class="mt-1 text-[11px]" :class="mirrorError ? 'text-rose-600' : 'text-[var(--ys-text-aux)]'">{{ mirrorNote }}</p>
          <p v-if="triedText" class="mt-1 break-words text-[10.5px] leading-relaxed text-[var(--ys-text-aux)]">{{ triedText }}</p>
        </div>

        <!-- 出站代理。只作用于「检查更新 / 下载安装包」这一条链路，不影响音源与播放。 -->
        <div class="border-t border-[var(--ys-border)] pt-2.5">
          <div class="text-[11.5px] font-medium text-[var(--ys-text)]">网络代理（可选）</div>
          <p class="mt-0.5 text-[11px] leading-relaxed text-[var(--ys-text-muted)]">
            应用由飞牛系统拉起，读不到 shell 里的 <code class="font-mono text-[10.5px]">HTTP_PROXY</code>；
            GitHub 在部分网络下必须经代理才连得上。填在这里就生效，不用改环境变量、不用重启。
          </p>
          <p class="mt-1 text-[11px] leading-relaxed text-[var(--ys-text-aux)]">
            支持 http / https / socks5，例如 <code class="font-mono text-[10.5px]">http://192.168.1.3:7890</code>
            或 <code class="font-mono text-[10.5px]">socks5://192.168.1.3:7891</code>。
            <b>内网与本地地址始终直连</b>，不会被绕进代理；只影响更新，不影响音源与播放。
          </p>
          <div class="mt-1.5 flex flex-wrap items-center gap-2">
            <input
              v-model="proxyInput"
              type="text"
              autocomplete="off"
              spellcheck="false"
              class="min-w-0 flex-1 rounded-[8px] border border-[var(--ys-border)] bg-white px-2.5 py-1.5 font-mono text-[12px] text-[var(--ys-text)] outline-none placeholder:text-[var(--ys-text-aux)] focus:border-[var(--ys-accent)]"
              placeholder="http://192.168.1.3:7890"
            />
            <button
              type="button"
              class="ys-press rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-[var(--ys-text-muted)] transition-colors hover:text-[var(--ys-text)] disabled:opacity-50"
              :disabled="proxySaving || !proxyInput.trim()"
              @click="saveProxy"
            >保存</button>
            <button
              v-if="proxySet"
              type="button"
              class="ys-press rounded-[8px] border border-[var(--ys-border)] bg-white px-3 py-1.5 text-[12px] font-medium text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-50"
              :disabled="proxySaving"
              @click="clearProxy"
            >清除</button>
          </div>
          <p v-if="proxyNote" class="mt-1 text-[11px]" :class="proxyError ? 'text-rose-600' : 'text-[var(--ys-text-aux)]'">{{ proxyNote }}</p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { appUpdate, formatBytes } from '../services/appUpdate'
import { ConfigAPI } from '../api/client'
import { apiError, fail } from '../services/userMsg'

defineProps({
  /** 见模板注释：弹窗里用 true，去掉自带的卡片外壳与标题。 */
  plain: { type: Boolean, default: false },
})

const {
  current, latest, hasUpdate, notes, publishedAt, size, checkedAt, checkError,
  checkTried, checkSource,
  checking, installed, progress, error, phase,
} = appUpdate

const notesOpen = ref(false)

const active = computed(() => ['downloading', 'ready', 'installing'].includes(phase.value))
const busy = computed(() => checking.value || active.value || installed.value)
const sizeLabel = computed(() => formatBytes(size.value))

// 更新说明属于哪一版：notes 永远来自「检查到的最新发布版」，所以标 latest；
// latest 拿不到时退回 current，至少不显示空标签。
const notesVersion = computed(() => String(latest.value || current.value || '').trim())

// 发布版比本机还旧（开发机上跑的是还没发布的版本）时，说清楚这段说明不是本版的 ——
// 否则用户会以为「更新日志里写的就是我这个版本的变化」。
const notesStale = computed(() => {
  if (hasUpdate.value || !notes.value) return false
  const l = String(latest.value || '').trim()
  const c = String(current.value || '').trim()
  return !!l && !!c && l !== c
})

const primaryLabel = computed(() => {
  if (checking.value && !hasUpdate.value) return '检查中…'
  if (phase.value === 'downloading') return `更新中 ${progress.value}%`
  if (phase.value === 'ready' || phase.value === 'installing') return '正在安装…'
  if (hasUpdate.value) return phase.value === 'error' ? '重试更新' : '立即更新'
  return checkError.value ? '重新检查' : '检查更新'
})

const primaryHint = computed(() => {
  if (phase.value === 'downloading') return '正在从 GitHub 下载 fpk，关掉本页也会继续'
  if (phase.value === 'installing') return '正在调用飞牛安装器，请勿关闭应用中心'
  if (hasUpdate.value) return '下载完成后会自动安装，不需要再点一次'
  return '检查 GitHub 上是否有新版本'
})

const statusText = computed(() => {
  if (phase.value === 'installed') return ''
  if (phase.value === 'installing') return '正在安装，请勿关闭飞牛应用中心…'
  if (phase.value === 'ready') return '下载完成，正在准备安装…'
  if (phase.value === 'downloading') return `正在下载更新 ${progress.value}%`
  if (phase.value === 'error') return error.value || '更新失败'
  if (checkError.value && !hasUpdate.value) return `检查更新失败：${checkError.value}`
  if (checking.value) return '正在检查更新…'
  if (!checkedAt.value) return ''
  if (!hasUpdate.value) return '已是最新版本'
  const when = formatWhen(publishedAt.value)
  return when ? `发现新版本 · 发布于 ${when}` : '发现新版本'
})

function toDate(v) {
  if (!v) return null
  const text = String(v)
  if (/^\d+$/.test(text)) {
    const n = Number(text)
    // GitHub/后端可能给秒或毫秒；小于 1e12 的按秒处理。
    return new Date(n < 1e12 ? n * 1000 : n)
  }
  const d = new Date(text)
  return Number.isNaN(d.getTime()) ? null : d
}

function formatWhen(v) {
  const d = toDate(v)
  return d ? d.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' }) : ''
}

const checkedLabel = computed(() => {
  const d = toDate(checkedAt.value)
  return d ? d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : ''
})

function onPrimary() {
  if (hasUpdate.value) appUpdate.startUpdate()
  else appUpdate.check()
}

function reloadPage() {
  window.location.reload()
}

// ── GitHub 个人令牌（用户 2026-10-25）──
//
// 存在后端配置里，清空走 POST /api/config 的专用钥匙 update_token_clear：
// 为什么不直接发 update_github_token: ''：这个字段带 omitempty，「清空」和
// 「没传这个键」在后端解码后长得一样 —— 这是本项目「改回默认永远改不回来」
// 那类 bug 的老根因（榜单首数踩过一次）。
//
// （同一次需求里的「下载限速」被用户当场否掉了：「不需要下载限速」，别再加。）
const settingsOpen = ref(false)
const tokenInput = ref('')
const tokenSet = ref(false)
const tokenSaving = ref(false)
const tokenNote = ref('')
const tokenError = ref(false)

const proxyInput = ref('')
const proxySet = ref(false)
const proxySaving = ref(false)
const proxyNote = ref('')
const proxyError = ref(false)

async function saveProxy() {
  const v = proxyInput.value.trim()
  if (!v || proxySaving.value) return
  proxySaving.value = true
  proxyError.value = false
  try {
    const res = await ConfigAPI.save({ update_proxy: v })
    const bad = apiError(res)
    if (bad) {
      proxyError.value = true
      proxyNote.value = fail('保存代理', new Error(bad))
      return
    }
    // 后端会把地址归一（补 scheme）再回传，用回传值回显，避免「我明明填了 http://」的疑问。
    const saved = String(res?.data?.update_proxy || '').trim()
    proxySet.value = !!saved
    proxyInput.value = saved
    proxyNote.value = '已保存。下一次「检查更新」就走这个代理（内网地址仍直连）'
    // 配代理的人一定是刚失败过的人：立刻重查一次，让他马上看到能不能通。
    appUpdate.check()
  } catch (e) {
    proxyError.value = true
    proxyNote.value = fail('保存代理', e)
  } finally {
    proxySaving.value = false
  }
}

async function clearProxy() {
  proxySaving.value = true
  proxyError.value = false
  try {
    const res = await ConfigAPI.save({ update_clear_proxy: true })
    const bad = apiError(res)
    if (bad) {
      proxyError.value = true
      proxyNote.value = fail('清除代理', new Error(bad))
      return
    }
    proxySet.value = false
    proxyInput.value = ''
    proxyNote.value = '代理已清除（回到直连 / 环境变量）'
  } catch (e) {
    proxyError.value = true
    proxyNote.value = fail('清除代理', e)
  } finally {
    proxySaving.value = false
  }
}

// ── 更新镜像清单（用户 2026-10-25）──────────────────────────────────
//
// 数据结构与后端一致：{name,url,disabled} 有序数组，整体替换（POST /api/config
// 的 update_mirrors 是指针，空数组 = 「只用官方源」，和「没配过用默认」要分清）。
//
// 官方源不落盘：它由后端内置在清单末尾。用户在界面上「停用官方源」时，我们往清单里
// 塞一条 {url:'github://official',disabled:true} —— 这是唯一表示「连官方也别试」的方式。
const OFFICIAL_MIRROR_URL = 'github://official'
const DEFAULT_MIRRORS = [{ name: 'gh-proxy', url: 'https://gh-proxy.com', disabled: false }]

const mirrors = ref([])
const mirrorInput = ref('')
const mirrorSaving = ref(false)
const mirrorNote = ref('')
const mirrorError = ref(false)

const mirrorRows = computed(() => mirrors.value.filter((m) => m.url !== OFFICIAL_MIRROR_URL))
const officialDisabled = computed(() => mirrors.value.some((m) => m.url === OFFICIAL_MIRROR_URL && m.disabled))
const activeMirrorCount = computed(() => mirrorRows.value.filter((m) => !m.disabled).length + (officialDisabled.value ? 0 : 1))

const settingsSummary = computed(() => {
  const parts = []
  if (activeMirrorCount.value) parts.push(`${activeMirrorCount.value} 个更新源`)
  if (proxySet.value) parts.push('代理已配置')
  if (tokenSet.value) parts.push('令牌已配置')
  return parts.join('、') || '未配置'
})

/** 把「界面上的行」和「官方源标记」合成要提交给后端的完整清单。 */
function buildMirrorPayload(rows, officialOff) {
  const list = rows.map((m) => ({ name: m.name || '', url: m.url, disabled: !!m.disabled }))
  if (officialOff) list.push({ url: OFFICIAL_MIRROR_URL, disabled: true })
  return list
}

async function saveMirrors(nextRows, officialOff) {
  mirrorSaving.value = true
  mirrorError.value = false
  try {
    const payload = buildMirrorPayload(nextRows, officialOff)
    const res = await ConfigAPI.save({ update_mirrors: payload })
    const bad = apiError(res)
    if (bad) {
      mirrorError.value = true
      mirrorNote.value = fail('保存镜像', new Error(bad))
      return false
    }
    // 以服务端归一后的结果为准（它会补 scheme、去重、丢掉非法项）——
    // 直接回显用户输入会让「看着存了其实被丢弃」这种问题永远发现不了。
    const saved = res?.data?.update_mirrors
    mirrors.value = Array.isArray(saved) ? saved.map((m) => ({ name: m.name || '', url: m.url, disabled: !!m.disabled })) : payload
    mirrorNote.value = '已保存，下一次「检查更新」按这个顺序尝试'
    appUpdate.check({ silent: true })
    return true
  } catch (e) {
    mirrorError.value = true
    mirrorNote.value = fail('保存镜像', e)
    return false
  } finally {
    mirrorSaving.value = false
  }
}

function addMirror() {
  const raw = mirrorInput.value.trim()
  if (!raw || mirrorSaving.value) return
  if (mirrorRows.value.some((m) => m.url.toLowerCase() === raw.toLowerCase())) {
    mirrorError.value = true
    mirrorNote.value = '这条已经在清单里了'
    return
  }
  const rows = [...mirrorRows.value, { name: '', url: raw, disabled: false }]
  if (saveMirrors(rows, officialDisabled.value)) mirrorInput.value = ''
}

function removeMirror(i) {
  const rows = mirrorRows.value.filter((_, idx) => idx !== i)
  saveMirrors(rows, officialDisabled.value)
}

function toggleMirror(i, enabled) {
  const rows = mirrorRows.value.map((m, idx) => (idx === i ? { ...m, disabled: !enabled } : m))
  saveMirrors(rows, officialDisabled.value)
}

function moveMirror(i, delta) {
  const rows = [...mirrorRows.value]
  const j = i + delta
  if (j < 0 || j >= rows.length) return
  const tmp = rows[i]
  rows[i] = rows[j]
  rows[j] = tmp
  saveMirrors(rows, officialDisabled.value)
}

function toggleOfficial() {
  saveMirrors(mirrorRows.value, !officialDisabled.value)
}

function resetMirrors() {
  mirrorSaving.value = true
  mirrorError.value = false
  ConfigAPI.save({ update_mirrors_reset: true }).then((res) => {
    const bad = apiError(res)
    if (bad) {
      mirrorError.value = true
      mirrorNote.value = fail('恢复默认镜像', new Error(bad))
      return
    }
    const saved = res?.data?.update_mirrors
    mirrors.value = Array.isArray(saved) && saved.length
      ? saved.map((m) => ({ name: m.name || '', url: m.url, disabled: !!m.disabled }))
      : DEFAULT_MIRRORS.map((m) => ({ ...m }))
    mirrorNote.value = '已恢复默认：只保留 gh-proxy.com，GitHub 官方源兜底'
    appUpdate.check({ silent: true })
  }).catch((e) => {
    mirrorError.value = true
    mirrorNote.value = fail('恢复默认镜像', e)
  }).finally(() => {
    mirrorSaving.value = false
  })
}

/** 最近一次检查「试过哪些源」—— 命中/失败都列出来，用户才知道该删哪条。 */
const triedText = computed(() => {
  const list = checkTried.value || []
  if (!list.length) return ''
  const parts = list.map((t) => {
    if (t.skipped) return `${t.name}（已停用）`
    return t.ok ? `${t.name} ✓` : `${t.name} ✗ ${t.reason || t.error || '失败'}`
  })
  return `本次检查：${parts.join('　')}`
})

/** 读出当前配置（GET /api/config 与 ConfigAPI.get 同一份结构）。 */
async function loadSettings() {
  try {
    const res = await ConfigAPI.get()
    const cfg = res?.data || res?.config || {}
    // 后端永远不回传 token 的值，只回 update_token_set 这个布尔。
    tokenSet.value = !!cfg.update_token_set
    // 代理不是密钥，回传明文（界面要能编辑/回显）。
    proxySet.value = !!String(cfg.update_proxy || '').trim()
    // 镜像清单：后端把内置官方源也算在返回里（不会被写进配置），原样回显即可。
    if (Array.isArray(cfg.update_mirrors)) {
      mirrors.value = cfg.update_mirrors.map((m) => ({ name: m.name || '', url: m.url, disabled: !!m.disabled }))
    } else if (cfg.update_mirrors === undefined) {
      mirrors.value = DEFAULT_MIRRORS.map((m) => ({ ...m }))
    }
  } catch {
    // 读失败就当默认：不要为了一个可选设置弹错误。
  }
}

// 连不上 GitHub 时把用户直接指向「网络代理」。这里只按**传输层**特征判，
// 不猜业务原因：403/404 是权限或版本问题，代理救不了，不该提示。
const proxyHintNeeded = computed(() => {
  const text = [error.value, checkError.value].filter(Boolean).join(' ')
  if (!text) return false
  return /i\/o timeout|timeout awaiting response headers|connection refused|connection reset|no such host|dial tcp|EOF|network is unreachable/i.test(text)
})

async function saveToken() {
  const v = tokenInput.value.trim()
  if (!v || tokenSaving.value) return
  tokenSaving.value = true
  tokenError.value = false
  try {
    const res = await ConfigAPI.save({ update_github_token: v })
    const bad = apiError(res)
    if (bad) {
      tokenError.value = true
      tokenNote.value = fail('保存令牌', new Error(bad))
      return
    }
    // 后端永远不回传 token 的值，只回 update_token_set —— 所以这里只更新布尔。
    tokenSet.value = !!res?.data?.update_token_set
    tokenInput.value = ''
    tokenNote.value = '已保存。下一次「检查更新」就会用上'
    // 令牌影响的就是限速/权限这类事，立刻重查一次让用户看到结果。
    appUpdate.check()
  } catch (e) {
    tokenError.value = true
    tokenNote.value = fail('保存令牌', e)
  } finally {
    tokenSaving.value = false
  }
}

async function clearToken() {
  tokenSaving.value = true
  tokenError.value = false
  try {
    const res = await ConfigAPI.save({ update_token_clear: true })
    const bad = apiError(res)
    if (bad) {
      tokenError.value = true
      tokenNote.value = fail('清除令牌', new Error(bad))
      return
    }
    tokenSet.value = !!res?.data?.update_token_set
    tokenInput.value = ''
    tokenNote.value = '令牌已清除（回到匿名 60 次/小时）'
  } catch (e) {
    tokenError.value = true
    tokenNote.value = fail('清除令牌', e)
  } finally {
    tokenSaving.value = false
  }
}

onMounted(() => {
  // 首次进设置页静默检查一次；失败只在卡片里留一行浅灰提示，不弹 toast。
  appUpdate.ensureChecked()
  // 如果上一次离开时下载/安装还在跑，回来接着轮询，不把进度丢在半路。
  appUpdate.resumeIfActive()
  loadSettings()
})
onUnmounted(() => appUpdate.stopPolling())
</script>
