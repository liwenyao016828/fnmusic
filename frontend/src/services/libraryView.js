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
// nas（本地音乐）是 v2.1.120 从顶层导航搬进来的侧栏项；
// charts（榜单管理）同一轮**搬了出去**（它现在是顶层导航里的一页），所以不在集合里 ——
// 偏好里还留着 'charts' 的浏览器会落到默认项，不会出现「整页只剩标题栏」。
// autodl（自动下载记录）是 v2.1.120 新增的侧栏项：飞牛页面里收藏/加歌单/点下载
// 触发的那批下载的流水（见 pkg/online/autolog.go）。
export const TAB_IDS = new Set(['nas', 'push', 'download', 'complete', 'autodl'])

/**
 * 把外部传来的侧栏项 id 归一化。
 *
 * 认不出来的 id 一律落到「推送」：落空的话整页只剩标题栏（模板里没有 v-else 兜底）。
 */
export function normalizeLibraryTab(id) {
  const mapped = LEGACY_TABS[id] || id
  return TAB_IDS.has(mapped) ? mapped : 'push'
}

/**
 * 统一时间文案（2026-10-27，审查报告「P2-10」）。
 *
 * 改之前这个页面里有三套时间口径，同一屏里并排出现：
 *   · 「上次整理：3 小时前」                                   ← 相对（本文件）
 *   · 「上次扫描 2026/10/27 18:39:52」                         ← `toLocaleString`（7 处）
 *   · 「10-27 18:39」                                          ← 手写 pad（1 处，替换记录）
 * 用户读不出这三行哪个更近；而且 `toLocaleString` 的输出随浏览器语言变
 * （`zh-CN` 给「2026/10/27 18:39:52」，英文环境给「10/27/2026, 6:39:52 PM」），
 * 截图报障时对不上。统一成「绝对一个函数 + 相对一个函数」。
 */

/**
 * 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前。
 * 只接受**毫秒**时间戳（调用方自己换算；秒和毫秒混用是这类 bug 的主要来源，
 * 所以这里不做猜测 —— 见下面 lastScanText 的 `* 1000`）。
 */
export function relWhen(atMs, nowMs = Date.now()) {
  const at = Number(atMs) || 0
  if (!at) return ''
  const mins = Math.floor((nowMs - at) / 60000)
  if (mins < 1) return '刚刚'
  if (mins < 60) return `${mins} 分钟前`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} 天前`
  return `${Math.floor(days / 30)} 个月前`
}

/**
 * 绝对时间：`2026-10-27 18:39`。
 * 接受 Date / 毫秒数 / 秒数 / ISO 串 —— 这个页面四种都出现过，所以统一在这里认。
 * 判别秒和毫秒：小于 1e11 的当秒（1e11 毫秒 ≈ 1973 年，真实时间戳不会更小）。
 */
export function fmtWhen(input) {
  if (input === null || input === undefined || input === '') return ''
  // 0 在这个页面里一律表示「还没发生过」（`gaps.at` / `scanned_at` / `titleFixAt`
  // 全都是 0 当哨兵）—— 不能把 0 格式化成 1970-01-01，否则空态会显示成「上次 1970 年」。
  if (input === 0 || input === '0') return ''
  let d
  if (input instanceof Date) {
    d = input
  } else if (typeof input === 'number') {
    d = new Date(input < 1e11 ? input * 1000 : input)
  } else if (/^\d+(\.\d+)?$/.test(String(input))) {
    const n = Number(input)
    d = new Date(n < 1e11 ? n * 1000 : n)
  } else {
    d = new Date(input)
  }
  if (Number.isNaN(d.getTime())) return String(input)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} `
    + `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 「上次整理」的相对时间。`indexStats.scanned_at` 是**秒**（不是毫秒） */
export function lastScanText(indexStats, nowMs = Date.now()) {
  const s = indexStats
  if (!s || !s.scanned_at) return ''
  return `上次整理：${relWhen(s.scanned_at * 1000, nowMs)}`
}

/** 「上次统计（缺口）」的相对时间。`gaps.at` 是 `Date.now()`，**毫秒** */
export function gapsAtText(gaps, nowMs = Date.now()) {
  const at = gaps?.at
  if (!at) return ''
  // 补上「上次统计：」前缀 —— 原来只有裸的「3 分钟前」，和旁边的「上次整理：3 小时前」
  // 并排时根本分不清说的是哪件事（P2-10）。
  return `上次统计：${relWhen(at, nowMs)}`
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

  // 自动下载（飞牛页面里收藏 / 加歌单 / 点下载触发的那批）：**只报失败**。
  // 成功的那些是流水，摆在侧栏当常态数字只会让人习惯性忽略这一行 ——
  // 而这一行的用处恰恰是「有失败时让人注意到」。
  const ad = input.autoDownload || {}
  if (ad.failed > 0) {
    out.push({ id: 'autodl-failed', label: '自动下载失败', count: ad.failed, tone: 'error', tab: 'autodl' })
  }

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

/**
 * 「补全整理」的三步流程（2026-10-12）。
 *
 * 起因是用户的一句反馈：「我点了扫描缺失，也正常出现了缺失的歌曲数量。
 * 但是，下一步怎么做呢？」—— 页面把「看缺什么」和「补什么」画成了两排，
 * 真正的下一步按钮埋在选项行最右边，扫完数字眼睛没有落点。
 *
 * 步骤**完全由已有状态推出**，不引入新的步骤状态机：
 * 多一个能不同步的状态，就多一类「界面说第 2 步、实际能做的只有第 3 步」的事故。
 *
 * 注意两个输入都是**持久化恢复**的（gaps 来自缓存、completePlan 来自本页操作），
 * 所以刷新页面后仍停在该在的那一步，跟用户看到的一致。
 */
export const COMPLETE_STEPS = [
  { n: 1, name: '扫描缺失' },
  { n: 2, name: '勾选字段' },
  { n: 3, name: '预览并写入' },
]

/**
 * 当前处在第几步：预览拿到 plan_id = 3；缺口统计扫过 = 2；否则还在第 1 步。
 *
 * 第三个参数 previewing 是流式预览补上的：预览**正在跑**时也已经点过「扫描缺失」
 * 那一步了（用户没扫过缺口也能直接预览，后端自己会扫），所以至少要到第 2 步 ——
 * 否则跑的中途界面上「扫描缺失」按钮会重新冒出来，看着像流程被重置了。
 */
export function completeStep(gaps, hasPlan, previewing = false) {
  if (hasPlan) return 3
  if (gaps || previewing) return 2
  return 1
}

/**
 * 每一步的「下一步该点哪」—— 这是这一页以前最缺的一句话。
 *
 * ⚠️ 这里写的按钮名和位置必须和 TidyView.vue 里真实渲染的一致（2026-10-27 修）：
 * 以前写的是「点上面的『扫描缺失』」和「点右下角『预览要写的内容』」，
 * 但那个按钮早就并进了字段 chip 行、文案也改成了「扫描」，
 * 预览按钮也从右下角搬到了**卡片头右上角** —— 用户照着提示找不到按钮。
 */
export function completeHint(gaps, hasPlan, previewing = false) {
  if (hasPlan) return '核对下面的清单，没问题就点「确认写入」'
  if (previewing) return '正在逐条匹配，可以随时点「停止预览」'
  if (gaps) return '字段勾好了就点卡片右上角的「预览要写的内容」'
  return '先点「扫描」把磁盘上的新增 / 变化收进索引，再看缺哪些标签'
}
