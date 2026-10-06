// 「曲库管家」页面的看法逻辑：侧栏任务状态、上次统计时间、缺口与补全的文案。
//
// 为什么从 `LibraryManager.vue` 里搬出来（那个文件三千行）：
// 这些规则每一条都对应过一次真机事故 ——
//   · 时间单位混用（`indexStats.scanned_at` 是**秒**，`gaps.at` 是**毫秒**），
//     混了不会报错，只会显示成「1375 天前」，只能靠人看出来；
//   · 「上次统计」的模板变量当初压根没在脚本里定义，界面上永远显示「上次统计（）」；
//   · 补全任务被**停止**和**跑完**都只是「running 变 false」，话术混了就像「点了停止也全补好了」。
// 组件里的 computed 没法单测（项目没有 DOM 环境，测试是 `node --test` 跑纯 JS），
// 所以规则搬到这里，用 `libraryView.test.mjs` 钉住。

/** 侧栏项：合并前的旧 id 会留在浏览器偏好和其他组件的跳转参数里，必须映射 */
export const LEGACY_TABS = { tidy: 'complete', playlist: 'download', ai: 'push' }
export const TAB_IDS = new Set(['push', 'download', 'complete'])

/**
 * 把外部传来的侧栏项 id 归一化。
 *
 * 认不出来的 id 一律落到「推送」：落空的话整页只剩标题栏（模板里没有 v-else 兜底）。
 */
export function normalizeLibraryTab(id) {
  const mapped = LEGACY_TABS[id] || id
  return TAB_IDS.has(mapped) ? mapped : 'push'
}

/** 「上次整理」的相对时间。`indexStats.scanned_at` 是**秒**（不是毫秒） */
export function lastScanText(indexStats, nowMs = Date.now()) {
  const s = indexStats
  if (!s || !s.scanned_at) return ''
  const mins = Math.floor((nowMs / 1000 - s.scanned_at) / 60)
  if (mins < 1) return '上次整理：刚刚'
  if (mins < 60) return `上次整理：${mins} 分钟前`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `上次整理：${hours} 小时前`
  return `上次整理：${Math.floor(hours / 24)} 天前`
}

/** 「上次统计（缺口）」的相对时间。`gaps.at` 是 `Date.now()`，**毫秒** */
export function gapsAtText(gaps, nowMs = Date.now()) {
  const at = gaps?.at
  if (!at) return ''
  const mins = Math.floor((nowMs - at) / 60000)
  if (mins < 1) return '刚刚'
  if (mins < 60) return `${mins} 分钟前`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} 小时前`
  return `${Math.floor(hours / 24)} 天前`
}

/**
 * 「写不了标签的格式」按扩展名的明细，如 `wav×12、opus×3`。
 *
 * 后端把这类文件从缺口里摘了出去（`gaps.unsupported`），但必须让用户看见 ——
 * 否则「补全跑完了还是没年份」只能被当成功能坏了。旧缓存里没有这个字段，取不到就返回空串。
 */
export function unsupportedExtsText(exts) {
  if (!exts || typeof exts !== 'object') return ''
  return Object.entries(exts)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([ext, n]) => `${ext}×${n}`)
    .join('、')
}

/** 侧栏角标：至少缺一个字段的条数（就是点进去会排进队的数量）；0 不显示 */
export function gapsPendingCount(gaps) {
  return gaps && gaps.any_field ? gaps.any_field : ''
}

/** 侧栏「任务状态」的配色档位（组件据此选图标与 class） */
export const STATUS_TONES = ['progress', 'warn', 'error', 'muted']

/**
 * 侧栏「任务状态」列表：**只列需要处理的事**，不摆一排恒为 0 的数字。
 *
 * 每项都带 `tab`，组件据此接上跳转（这个列表本身就是导航）。
 * 输入里 `download` 的三个数来自下载队列，`monitorStats` 来自后端 `/api/monitors` 的 stats。
 */
export function sidebarStatusItems(input = {}) {
  const cj = input.completeJob || null
  const dl = input.download || {}
  const pushRunning = Number(input.pushRunning || 0)
  const pendingBaseline = Number(input.pendingBaseline || 0)
  const st = input.monitorStats || null
  const out = []

  // 补全歌词/封面是「会真正改文件」的长任务，跑起来必须让用户知道；
  // 没在跑但有写入失败时，失败要留在这儿等人处理。
  if (cj?.running) {
    out.push({ id: 'complete-running', label: '补全歌词封面', count: cj.total || 0, tone: 'progress', tab: 'complete' })
  } else if (cj?.summary?.failed > 0) {
    out.push({ id: 'complete-failed', label: '补全写入失败', count: cj.summary.failed, tone: 'error', tab: 'complete' })
  }

  if (dl.active > 0) out.push({ id: 'dl-active', label: '下载队列进行中', count: dl.active, tone: 'progress', tab: 'download' })
  if (dl.failed > 0) out.push({ id: 'dl-failed', label: '下载失败待处理', count: dl.failed, tone: 'error', tab: 'download' })
  if (dl.paused > 0) out.push({ id: 'dl-paused', label: '下载已暂停', count: dl.paused, tone: 'warn', tab: 'download' })

  if (pushRunning > 0) out.push({ id: 'push-running', label: '推送正在执行', count: pushRunning, tone: 'progress', tab: 'push' })
  if (pendingBaseline > 0) out.push({ id: 'baseline', label: '还没开始追新', count: pendingBaseline, tone: 'warn', tab: 'push' })

  if (st) {
    if (st.failed > 0) out.push({ id: 'push-failed', label: '推送下载失败', count: st.failed, tone: 'error', tab: 'push' })
    if (st.pending > 0) out.push({ id: 'push-pending', label: '待推送曲目', count: st.pending, tone: 'warn', tab: 'push' })
    if (st.missing > 0) out.push({ id: 'push-missing', label: '曲库里还没有', count: st.missing, tone: 'muted', tab: 'push' })
  }

  return out
}

/** 补全任务卡片的标题：跑 / 正在停 / 上次被停 / 上次结果，四态不能混 */
export function completeTitleText(job) {
  if (!job) return ''
  if (job.running) return job.cancelled ? '正在停止…' : '正在补全…'
  return job.cancelled ? '上次补全被停止' : '上次补全结果'
}

/** 进度条百分比（没有总数时 0，超过也封顶 100） */
export function completePercent(job) {
  if (!job || !job.total) return 0
  return Math.min(100, Math.round((job.done / job.total) * 100))
}

/**
 * 结果里有几首是「规则配不上、由 AI 从候选里挑的」。
 *
 * 必须可分辨：用户有权知道哪些结果是模型判断的，而不是我们自己按规则算出来的。
 */
export function aiMatchedCount(summary) {
  const rs = summary?.results || []
  return rs.filter((r) => r.matched_by_ai).length
}

const num = (v) => (Number.isFinite(Number(v)) ? Number(v) : 0)

/**
 * 轮询看到「任务不再运行」时报的那句话。
 *
 * 被停止 ≠ 跑完 —— 用一样的话术会让人以为「点停止也全补好了」，
 * 所以停止单独说，并把「没处理的」数字摆出来（等于提示还能重新跑一轮）。
 */
export function completeDoneText(job) {
  const s = job?.summary
  if (!s) return ''
  if (job.cancelled) {
    return `补全已停止：已处理 ${num(s.results?.length)} 首，` +
      `${num(s.cancelled)} 首没处理（已写入的可以撤销回退）`
  }
  const head = `补全完成：歌词 ${num(s.lyric_filled)} · 封面 ${num(s.cover_filled)} · ` +
    `年份 ${num(s.year_filled)} · 曲序 ${num(s.track_filled)} · 光盘 ${num(s.disc_filled)} · 风格 ${num(s.genre_filled)}`
  return num(s.no_match) ? `${head} · 没搜到 ${num(s.no_match)} 首` : head
}
