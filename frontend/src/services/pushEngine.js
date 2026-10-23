/**
 * 推送/订阅引擎（全局单例服务）。
 *
 * 2026-10-17 导航框架重做时从 LibraryManager.vue 抽出。抽出的动机是**结构性**的：
 * 订阅的数据、8 秒轮询、自动补下载、下载排空后自动同步飞牛，这一整套机器
 * 以前绑在「曲库管家」组件的生命周期里 —— 页面拆分后它必须**独立于任何视图**
 * 持续运转（用户在发现页听歌时，订阅的自动下载也要照跑）。
 *
 * 归属划分：
 *   · 本引擎 —— 数据（monitors/pushTasks/subscriptions/角标）、轮询、fill 机器
 *     （fetchPendingTracks/enqueueSongs/fillRow/autoFillPending/watchFillCompletion/
 *     watchQueueDrain/autoPushRow）、订阅 CRUD 动作（toggleCapability/stopPushOnly/
 *     runRow/removeRow/saveRowSettings/createLinkSubscriptions/createAccountTask）。
 *   · PushManager.vue —— 纯界面：三步引导、订阅行模板、行内展开设置表单、
 *     新建弹窗、同步历史多选。
 *
 * 用户可见的提示一律走 services/feedback.js（全局 toast），不再有页面级 message/error 槽。
 * App.vue 启动时调一次 `start()`（幂等），应用生命周期内常驻。
 */
import { ref, computed } from 'vue'
import { MonitorAPI, PushAPI, SearchAPI, FnosAPI, DownloadAPI, SourcesAPI } from '../api/client.js'
import { fail as failText, apiError } from './userMsg.js'
import { logError } from './appLog.js'
import { pickAlreadyLocal } from './downloadDedup.js'
import { downloadManager } from './downloadManager.js'
import { markLoaded } from './viewCache.js'
import { ok as notifyOk, fail as notifyFail } from './feedback.js'
import {
  buildSubscriptions, setCapability, setPushEnabled, subscribePlaylist, fnosReady,
  SUBSCRIBE_INTERVAL_MINUTES,
} from './subscribe.js'

// 下载队列排空后、自动触发推送前的等待时间。
// 给飞牛曲库重扫留出时间（`pkg/fnos/rescan.go` 的 debounce 是 30 秒），
// 否则刚下载的歌还没进曲库，推送会一首都匹配不上 —— 用户看到的就是「歌单里没歌」。
const AUTO_PUSH_SETTLE_MS = 90000

/** 界面只给一句人话，完整信息进日志（feedback.fail 自带「看日志」入口） */
function notify(msg, isError = false) {
  if (isError) notifyFail(msg)
  else notifyOk(msg)
}

// ── 数据 ──────────────────────────────────────────────────────────────────────

const monitors = ref([])
const runningIds = ref([])
/** 后端 /api/monitors 已经算好的聚合统计（此前前端完全没用上） */
const monitorStats = ref(null)
const pushTasks = ref([])

/**
 * 飞牛音乐是否可用。
 *
 * 它决定「同步到飞牛歌单」这个能力位能不能**新建**（已有记录仍可勾/取消，
 * 因为那只是改 enabled，不需要飞牛在线）。提前探一次，别等用户点了才报错。
 */
const fnosConnected = ref(false)

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

/**
 * 统一订阅列表：一个来源一行，带 download / fnos 两个能力位。
 *
 * 归并逻辑在 services/subscribe.js 的 `buildSubscriptions()` —— 纯函数、有单测。
 * 这里不自己拼装，避免「界面一套口径、测试另一套口径」。
 */
const subscriptions = computed(() => buildSubscriptions(monitors.value, pushTasks.value))

/** 「还没开始追新」角标（后端字段仍是 baseline_done） */
const pendingBaseline = computed(() => monitors.value.filter((m) => !m.baseline_done).length)

/** 导航角标 = 监控任务数 + 飞牛推送任务数（两类都是「订阅」） */
const pushTotalCount = computed(() => monitors.value.length + pushTasks.value.length)

/** 飞牛同步历史（低频查看，PushManager 渲染；autoPushRow 成功后要刷新它） */
const pushRuns = ref([])

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

async function loadFnosStatus() {
  fnosConnected.value = await fnosReady()
}

async function loadPushRuns() {
  try {
    const res = await PushAPI.runs('', 20)
    if (res?.code === 200) pushRuns.value = res.data?.runs || []
  } catch (_) {
    // 历史是辅助信息
  }
}

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
    notify(failText('加载推送任务', e, '加载失败'), true)
  }
  markLoaded('library') // 登记新鲜度（见 services/viewCache.js）；键名沿用历史值
}

// ── fill 机器：两侧都可能缺歌，都走「逐首搜索 → 入队」这一步 ──
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
      if (!auto) notify('没有需要补下载的曲目')
      return { added, missed, already }
    }

    if (auto) {
      if (added) {
        notify(`已自动加入下载队列 ${added} 首${already ? `（${already} 首本地已有，已标记）` : ''}`)
      }
    } else {
      notify(
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
    notify(failText(auto ? '自动下载' : '补下载', e), true)
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
    notify('下载完成，已自动同步到飞牛歌单（稍后刷新查看）')
    setTimeout(() => {
      loadPushTasks()
      loadPushRuns()
    }, 2500)
  } catch (e) {
    logError('自动同步飞牛', row.name, e?.message || String(e))
    notify('下载完成，但自动同步没成功；可点这一行的「立即执行」重试', true)
  }
}

// ── 订阅行动作（PushManager 调用；busyKey 防连点是动作级的，也放引擎里共享） ──

/** 正在处理的行（`<rowKey>:<动作>`）：用来禁用按钮，防止连点建出重复任务 */
const busyKey = ref('')

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
      notify(
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
      if (!res.ok) return notify(res.error || '创建失败', true)
      notify(res.message || '已创建')
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
    notify(failText('切换订阅能力', e), true)
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
    notify('已停止同步到飞牛歌单（本地下载不受影响）')
    await Promise.all([loadMonitors(), loadPushTasks()])
  } catch (e) {
    notify(failText('停止同步', e), true)
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
  if (!jobs.length) return notify('这个订阅没有开启任何能力位', true)

  busyKey.value = `${row.key}:run`
  try {
    await Promise.all(jobs)

    // 勾了「下载到本地曲库」= 用户已经说过「这歌我要下」。
    // 用户 2026-10-23 的原话：「订阅页面里补下载是否可以和勾选下载到本地曲库，
    // 然后点击立即执行合并，实现勾选，执行，下载。」
    //
    // 所以这里把「发现 → 补下载」并成一次动作：
    //   · 勾了下载：不说「已触发」这种要用户再理解一遍的话，直接说「正在发现并下载」；
    //   · 没勾：保持原样（只发现、登记成待下载），因为那是订阅更新的原逻辑 ——
    //     记录留在订阅页，状态如实显示「只发现未下载」。
    const downloading = !!row.download
    notify(downloading ? '正在发现并下载，稍后自动刷新' : '已触发，稍后自动刷新')

    setTimeout(async () => {
      await loadMonitors()
      loadPushTasks()
      loadPushRuns()
      // 监控轮把新歌登记成「待下载」之后**立刻**自动补一次。
      // 用户点「立即执行」就是想让歌下下来，不该再干等一个 8 秒轮询周期
      // （后端 run 是异步的，所以给 5 秒让发现+登记先跑完；没赶上还有轮询兜底）。
      //
      // 勾了下载时这里要**等到真的入队**（autoFillPending 是异步的，不等就会
      // 在「待下载 N 首」还没消失时给用户回一句「已触发」，看起来像没执行）。
      if (downloading) {
        await autoFillPending()
      } else {
        autoFillPending()
      }
    }, 5000)
  } catch (e) {
    notify(failText('执行订阅', e), true)
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
    notify('已删除')
    await Promise.all([loadMonitors(), loadPushTasks()])
    return true
  } catch (e) {
    notify(failText('删除订阅', e), true)
    return false
  } finally {
    busyKey.value = ''
  }
}

/**
 * 保存行内设置。
 *
 * ⚠️ 间隔是**订阅级**的 —— 两侧一起改，避免出现「下载 6 小时、同步 1 天」这种错位。
 * ⚠️ 两侧接口语义不同：监控侧是真局部更新（只传改动的键），推送侧是整体替换（必须回传全字段）。
 *
 * @param {object} row 订阅行
 * @param {object} f 行内设置表单（PushManager 的 editForm 当前值）
 */
async function saveRowSettings(row, f) {
  if (!row || busyKey.value) return false
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
    notify('设置已保存')
    await Promise.all([loadMonitors(), loadPushTasks()])
    return true
  } catch (e) {
    notify(failText('保存订阅设置', e), true)
    return false
  } finally {
    busyKey.value = ''
  }
}

/**
 * 新建订阅（链接来源）：逐条链接调用 `subscribePlaylist`（勾什么就建什么）。
 *
 * 多条链接时部分成功也要如实汇总 —— 不能让用户以为全成了。
 * 界面只给一句人话，明细进日志（文案口径见 HANDOVER §3.3.3）。
 *
 * @returns {Promise<{okList: object[], failList: string[]}>}
 */
async function createLinkSubscriptions({ name, links, wantDownload, wantFnos, interval }) {
  const okList = []
  const failList = []
  try {
    for (let i = 0; i < links.length; i++) {
      const res = await subscribePlaylist({
        link: links[i],
        // 多条链接时给名字加序号，否则两侧任务同名、列表里分不出来
        name: links.length > 1 ? `${name} ${i + 1}` : name,
        wantDownload,
        wantFnos,
        intervalMinutes: Number(interval) || SUBSCRIBE_INTERVAL_MINUTES,
      })
      if (res.ok) okList.push(res)
      else failList.push(res.error || '未知原因')
    }
  } catch (e) {
    failList.push(failText('创建订阅', e))
  }

  await Promise.all([loadMonitors(), loadPushTasks()])

  if (failList.length) {
    logError('创建订阅', `${failList.length} 条链接失败`, failList.join('；'))
    if (okList.length) notify(`部分成功：${okList.length} 条已订阅，${failList.length} 条失败`)
  }
  return { okList, failList }
}

/**
 * 按账号来源建订阅（歌单 / 每日推荐）。
 *
 * 只有飞牛同步这一条 —— 账号歌单没有公开链接，监控侧订阅不了；
 * 「每日推荐」更是每天都变，后端监控也不支持。所以这里直接建推送任务。
 *
 * @returns {Promise<{ok: boolean, error?: string, message?: string}>}
 */
async function createAccountTask({ name, provider, sourceValue, interval }) {
  const [kind, id] = String(sourceValue || '').split(':')
  if (!kind) return { ok: false, error: '请选择一个账号歌单' }
  if (!fnosConnected.value) return { ok: false, error: '飞牛音乐未连接，无法同步歌单' }
  try {
    const res = await PushAPI.saveTask({
      name: `${name}（订阅）`.slice(0, 40),
      provider,
      source_kind: kind,
      source_id: id || '',
      target_title: name.slice(0, 40),
      interval_minutes: Number(interval) || SUBSCRIBE_INTERVAL_MINUTES,
      sync_cover: true,
      enabled: true,
      retention_mode: 'keep',
      retention_days: 0,
    })
    const bad = apiError(res, '创建同步任务失败')
    if (bad) return { ok: false, error: bad }
    await loadPushTasks()
    return { ok: true, message: '已订阅：同步到飞牛歌单' }
  } catch (e) {
    return { ok: false, error: failText('创建订阅', e) }
  }
}

/** 清除同步历史。`ids` 为空 = 全清。（confirm 由调用方问过之后才进来） */
async function clearPushRuns(ids) {
  try {
    const res = await PushAPI.clearRuns(ids)
    notify(`已清除 ${res?.data?.removed ?? 0} 条同步历史`)
    await loadPushRuns()
  } catch (e) {
    notify(failText('清除同步历史', e), true)
  }
}

// ── 生命周期：App.vue 启动时 start() 一次，应用生命周期内常驻 ──

let pollTimer = null
let started = false

/** 幂等启动：首轮拉数据 + 8 秒轮询（自动补下载 / 刷角标） */
function start() {
  if (started) return
  started = true
  loadMonitors()
  loadPushTasks()
  loadFnosStatus()
  loadApiSourceCount()
  pollTimer = setInterval(() => {
    autoFillPending()
    loadMonitors()
    loadPushTasks()
  }, 8000)
}

/** 手动全量刷新（页面刷新按钮用） */
async function reloadAll() {
  await Promise.all([loadMonitors(), loadPushTasks()])
}

export const pushEngine = {
  // 数据
  monitors, runningIds, monitorStats, pushTasks, fnosConnected, apiSourceCount,
  subscriptions, pendingBaseline, pushTotalCount, pushRuns,
  // fill 状态
  fillingKey, fillProgress, autoFilling, busyKey,
  // 加载
  loadMonitors, loadPushTasks, loadFnosStatus, loadApiSourceCount, loadPushRuns, reloadAll,
  // fill
  fillRow, fillHint,
  // 行动作
  toggleCapability, stopPushOnly, runRow, removeRow, saveRowSettings,
  createLinkSubscriptions, createAccountTask, clearPushRuns,
  // 生命周期
  start,
}
