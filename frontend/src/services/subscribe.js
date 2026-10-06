/**
 * 订阅：把「监控（自动下载）+ 推送（同步到飞牛）」包成**一个用户概念**。
 *
 * 后端仍是两套独立系统（monitor 管发现与下载，push 管飞牛歌单），
 * 这里只做 facade。**不合并后端**（YAGNI）—— 但两套系统**共享了整整一半逻辑**
 * （来源订阅 + 定时增量发现，抓取实现都是 `push.FetchSource`），
 * 所以界面上它们不该是两个并列功能，而是**同一个订阅上的两个能力位**：
 *
 * ```
 * download —— 有新歌就下载到本地曲库        （monitor）
 * fnos     —— 把曲库里匹配上的同步到飞牛歌单（push task）
 * ```
 *
 * 对外三件事：
 *   `buildSubscriptions()`  —— 把两条流水线归并成统一的订阅行（列表用）
 *   `setCapability()`       —— 单独开关某行的某个能力位
 *   `subscribePlaylist()`   —— 按勾选建任务（想只下载就只建监控）
 *
 * 用户视角的完整闭环：
 *   点「订阅更新」→ 首轮登记基线 + 已在曲库的歌推上飞牛
 *   每 6 小时 → 后端拉歌单 → 新歌 → 取链 → 自动下载 → 飞牛自动扫库
 *            → 下一轮推送把新歌补进飞牛歌单
 *   点「停止更新」→ 两条任务一起停，已下载的文件与飞牛歌单都保留
 */
import { MonitorAPI, PushAPI, FnosAPI } from '../api/client.js'
import { apiError } from './userMsg.js'

/** 订阅默认节奏：6 小时（与后端 DefaultIntervalMinutes 一致） */
export const SUBSCRIBE_INTERVAL_MINUTES = 360

/** 平台代号 → 展示名 */
export function platformLabel(source) {
  const s = String(source || '').toLowerCase()
  if (s === 'wy' || s === 'netease') return '网易云'
  if (s === 'tx' || s === 'qq') return 'QQ 音乐'
  if (s === 'kg' || s === 'kugou') return '酷狗'
  if (s === 'kw' || s === 'kuwo') return '酷我'
  return s ? s.toUpperCase() : '歌单'
}

/** 平台代号 → 推送任务要的 provider */
function providerOf(source) {
  const s = String(source || '').toLowerCase()
  if (s === 'wy' || s === 'netease') return 'netease'
  if (s === 'tx' || s === 'qq') return 'qq'
  return ''
}

/** provider → 平台代号（`providerOf` 的反向，用于从推送任务倒推来源平台） */
function sourceOfProvider(provider) {
  const p = String(provider || '').toLowerCase()
  if (p === 'netease') return 'wy'
  if (p === 'qq') return 'tx'
  return ''
}

function extractNeteaseId(link) {
  const m = String(link).match(/[?&#]id=(\d{5,})/) || String(link).match(/playlist\/(\d{5,})/)
  return m ? m[1] : ''
}

function extractQQId(link) {
  const m =
    String(link).match(/playlist\/(\d{5,})/) ||
    String(link).match(/disstid=(\d{5,})/) ||
    String(link).match(/[?&]id=(\d{5,})/)
  return m ? m[1] : ''
}

function platformOf({ link = '', source = '' }) {
  const raw = String(link || '')
  if (/music\.163\.com|163cn\.tv/i.test(raw)) return 'wy'
  if (/\.qq\.com/i.test(raw)) return 'tx'
  const s = String(source || '').toLowerCase()
  if (s === 'wy' || s === 'netease') return 'wy'
  if (s === 'tx' || s === 'qq') return 'tx'
  return ''
}

/**
 * 归一化歌单标识，用于「已订阅」判定。
 *
 * 能认出平台与歌单 ID 时返回 `wy:1973665667` 这种稳定 key —— 链接和 ID 都能算出同一个值；
 * 认不出（如平台分享短链）则退化为「去协议、去 query、去尾斜杠」的链接字符串，
 * 同一短链两次订阅仍能匹配上。返回空串表示无法识别，不参与匹配。
 */
export function playlistKey({ link = '', id = '', source = '' } = {}) {
  const raw = String(link || '').trim()
  const platform = platformOf({ link: raw, source })
  let pid = String(id || '').trim()
  if (!pid && raw) {
    pid = platform === 'tx' ? extractQQId(raw) : extractNeteaseId(raw)
  }
  if (platform && pid) return `${platform}:${pid}`
  if (raw) return raw.replace(/^https?:\/\//i, '').replace(/[?#].*$/, '').replace(/\/+$/, '')
  return ''
}

/** 监控任务绑定的歌单 key（一个监控可能绑多个歌单） */
export function monitorKeys(monitor) {
  const refs = monitor?.target?.playlists || []
  const fallbackSource = (monitor?.sources || [])[0] || ''
  return refs
    .map((ref) => playlistKey({ link: ref.link, id: ref.id, source: ref.source || fallbackSource }))
    .filter(Boolean)
}

/** 推送任务的歌单 key */
export function taskKey(task) {
  if (!task || task.source_kind !== 'link') return ''
  return playlistKey({ link: task.source_id })
}

/** 只有 ID 没有链接时，合成一条标准链接给推送任务用 */
function synthesizeLink({ id, source }) {
  const pid = String(id || '').trim()
  if (!pid) return ''
  const provider = providerOf(source)
  if (provider === 'netease') return `https://music.163.com/playlist?id=${pid}`
  if (provider === 'qq') return `https://y.qq.com/n/ryqq/playlist/${pid}`
  return ''
}

/** 飞牛音乐是否可用（推送任务依赖它） */
export async function fnosReady() {
  try {
    const res = await FnosAPI.status()
    return !!res?.data?.music_available
  } catch (_) {
    return false
  }
}

/**
 * 从已有的监控/推送任务里挑出属于这个歌单的（纯函数，便于测试）。
 *
 * ⚠️ 必须**收集全部匹配项**而不是取第一个：同一个歌单可能有多条历史记录
 * （用户手动建过、早期版本建过），只看第一条会把「有一条还在跑」误判成「已暂停」，
 * 也会让「停止更新」漏掉没停的那条。
 */
export function pickSubscription(key, monitors = [], tasks = []) {
  if (!key) return null
  const matchedMonitors = (monitors || []).filter((m) => monitorKeys(m).includes(key))
  const matchedTasks = (tasks || []).filter((t) => taskKey(t) === key)
  if (!matchedMonitors.length && !matchedTasks.length) return null
  return {
    monitors: matchedMonitors,
    tasks: matchedTasks,
    // 只要还有一条在跑就算「已订阅」—— 界面上的「停止更新」才有意义
    active: matchedMonitors.some((m) => m.enabled) || matchedTasks.some((t) => t.enabled),
  }
}

/**
 * 把监控与推送任务归并成**统一的「订阅」行** —— 一行 = 一个来源，带两个能力位。
 *
 * ```
 * download —— 有新歌就下载到本地曲库       （底层 monitor）
 * fnos     —— 把曲库里匹配上的同步到飞牛歌单（底层 push task）
 * ```
 *
 * **为什么要归并**：这两套后端系统**共享了整整一半逻辑** ——
 * 来源订阅 + 定时增量发现（抓取实现都是 `push.FetchSource`），
 * 差别只在「对新增曲目做什么」。所以它们不是并列的两个功能，
 * 而是「同一个订阅」上的两个能力位；分成两个列表会让用户分别管两份间隔/启停。
 *
 * ⚠️ **认不出 key 的记录也要出现在列表里** —— 比如榜单「当前列表」模式的监控
 * 没有 `target.playlists`，退化成 `monitor:<id>`。过滤掉的话用户会看到「任务不见了」。
 *
 * ⚠️ **一个监控可能绑多个歌单**：只认**第一个** key 作为「它的行」，
 * 否则同一个监控会在多行各出现一次，在任一行点开关会看起来同时动了两行。
 */
export function buildSubscriptions(monitors = [], tasks = []) {
  // 默认参数只兜 `undefined`；接口返回 `data.monitors: null` 时得自己归一化，
  // 否则 `for...of null` 直接把整个列表页炸掉。
  const monList = monitors || []
  const taskList = tasks || []

  const rows = new Map()
  const ensure = (key) => {
    let row = rows.get(key)
    if (!row) {
      row = { key, monitors: [], tasks: [] }
      rows.set(key, row)
    }
    return row
  }

  for (const m of monList) {
    if (!m) continue
    ensure(monitorKeys(m)[0] || `monitor:${m.id}`).monitors.push(m)
  }
  for (const t of taskList) {
    if (!t) continue
    ensure(taskKey(t) || `task:${t.id}`).tasks.push(t)
  }

  const out = []
  for (const row of rows.values()) {
    const mon = row.monitors[0] || null
    const task = row.tasks[0] || null
    const ref = (mon?.target?.playlists || [])[0] || {}

    // 来源标识：优先监控侧（它可能只有 id），其次推送侧（它存的是链接）
    const link = String(ref.link || (task?.source_kind === 'link' ? task.source_id : '') || '').trim()
    const id = String(ref.id || '').trim()
    const source = String(
      ref.source || (mon?.sources || [])[0] || sourceOfProvider(task?.provider) || '',
    ).toLowerCase()
    const provider = providerOf(source)

    // 「能不能再建另一条」的条件：认得出平台，且拿得到链接或 id
    const canCreate = !!provider && !!(link || id)

    const rawName = String(mon?.name || task?.name || '').trim()
    const name = rawName.replace(/（订阅）\s*$/, '').trim() || rawName || '未命名订阅'

    out.push({
      key: row.key,
      name,
      source,
      provider,
      link,
      id,
      // 展示用：「网易云 · 歌单链接」
      sourceLabel: source ? `${platformLabel(source)} · ${link ? '歌单链接' : '歌单 ID'}` : '来源未识别',
      monitors: row.monitors,
      tasks: row.tasks,
      // 两个能力位：**任一**记录启用就算开着（与 pickSubscription 的 active 同口径）
      download: row.monitors.some((m) => m.enabled),
      fnos: row.tasks.some((t) => t.enabled),
      // 该能力位**有没有记录**（决定勾选框是「切换」还是「新建」）
      hasDownload: row.monitors.length > 0,
      hasFnos: row.tasks.length > 0,
      canCreate,
      // 建不了另一条时给用户一句话，而不是把勾选框置灰了不说原因
      blockReason: canCreate
        ? ''
        : provider
          ? '这个来源缺少歌单链接或 ID，暂时建不了另一条'
          : '来源平台认不出来，暂时建不了另一条',
      intervalMinutes: Number(mon?.interval_minutes || task?.interval_minutes || 0),
      pendingCount: row.monitors.reduce((n, m) => n + (m.pendingCount || 0), 0),
      missingCount: row.tasks.reduce((n, t) => n + (t.last_result?.missing?.length || 0), 0),
      targetTitle: String(task?.target_title || ''),
      baselineDone: !!mon?.baseline_done,
      lastRunAt: String(mon?.last_run_at || ''),
      lastStatus: String(task?.last_status || ''),
      lastError: String(task?.last_error || ''),
    })
  }

  // 按名称排序：纯函数里定好顺序，界面和测试都不会因为「谁先建」而跳来跳去
  return out.sort((a, b) => a.name.localeCompare(b.name, 'zh'))
}

/**
 * 开关一行的能力位（`download` / `fnos`）。
 *
 * ⚠️ **「关」是两侧一起关，「开」只开点的那一侧**（理由见下面注释）。
 *
 * ⚠️ 这里**不负责创建**：某个能力位还没有对应记录时，「打开」需要来源信息
 * 和用户确认（歌单名、间隔等），由界面走 `subscribePlaylist`。
 * 所以调用前先看 `row.hasDownload` / `row.hasFnos`。
 */
export async function setCapability(row, which, enabled) {
  if (!row) return
  // ⚠️ 「停止」一律**两侧一起停**。
  // 用户反馈（2026-09-22）：「在推送的时候点了停止怎么还在推到下载」——
  // 只停一侧的话，界面上看不出另一侧还在跑（点「同步到飞牛歌单」的停止，
  // 监控侧照旧一轮一轮发现新歌、继续往下载队列里塞），用户会以为没停掉。
  // 「停止」的语义是「这个订阅别再跑了」，不是「停一半」。
  if (!enabled) {
    await setSubscriptionEnabled({ monitors: row.monitors, tasks: row.tasks }, false)
    return
  }
  // 「开启」只开点的那一侧 —— 「只下载、不同步飞牛」是合理诉求，别替用户做主。
  if (which === 'download') {
    await setSubscriptionEnabled({ monitors: row.monitors, tasks: [] }, true)
  } else {
    await setSubscriptionEnabled({ monitors: [], tasks: row.tasks }, true)
  }
}

/**
 * 查当前订阅状态：按归一化 key 同时匹配监控与推送任务。
 * 返回 null 表示没订阅过；否则 { monitors, tasks, active }。
 */
export async function findSubscription(key) {
  if (!key) return null
  const [monRes, taskRes] = await Promise.all([MonitorAPI.list(), PushAPI.tasks()])
  return pickSubscription(key, monRes?.data?.monitors || [], taskRes?.data?.tasks || [])
}

/**
 * 一键订阅一个歌单。
 *
 * `wantDownload` / `wantFnos` 决定**建哪几条** —— 以前一律两条都建，
 * 用户会莫名其妙多出一条用不到的推送任务（界面上还得单独去关）。
 * 两个都不勾等于什么都没订阅，直接返回失败。
 *
 * 顺序有讲究：先建监控（保证「自动下载」这条主线成立），再建推送任务。
 * 飞牛没配好时**只建监控**并如实告知 —— 不静默降级，也不因此拒绝订阅。
 *
 * 首轮只登记基线不下载（后端机制），所以把当前解析出的曲目一并上报，
 * 免得等下一轮再抓一次。
 */
export async function subscribePlaylist({
  link = '', id = '', source = '', name = '', songs = [],
  wantDownload = true, wantFnos = true,
  intervalMinutes = SUBSCRIBE_INTERVAL_MINUTES,
}) {
  const title = String(name || '').trim() || '歌单订阅'
  const taskName = `${title}（订阅）`.slice(0, 40)
  // ⚠️ 文案里不要说「基线」—— 这是内部术语，用户看不懂（2026-09-21 反馈）。
  // 这个状态的含义是「已经知道歌单里现有哪些歌」，跑完第一次检查才开始追新。
  const baselineText = songs.length ? `先记下现有 ${songs.length} 首` : '第一次只记录现有曲目'
  // 间隔是**订阅级**的：两条任务用同一个值，避免「下载 6 小时、同步 1 天」这种错位
  const interval = Number(intervalMinutes) >= 0 ? Number(intervalMinutes) : SUBSCRIBE_INTERVAL_MINUTES

  if (!wantDownload && !wantFnos) {
    return { ok: false, error: '请至少选一项：下载到本地曲库 / 同步到飞牛歌单' }
  }

  let monitorId = ''
  if (wantDownload) {
    const ref = {}
    if (String(link || '').trim()) ref.link = String(link).trim()
    if (String(id || '').trim()) ref.id = String(id).trim()
    if (String(source || '').trim()) ref.source = String(source).trim()

    const monRes = await MonitorAPI.create({
      name: taskName,
      kind: 'playlist',
      target: { playlists: [ref] },
      sources: source ? [source] : [],
      quality: 'lossless',
      interval_minutes: interval,
      auto_download: true,
      embed: true,
      enabled: true,
    })
    const bad = apiError(monRes, '订阅失败')
    if (bad) return { ok: false, error: bad }

    monitorId = monRes?.data?.id || ''

    // 基线曲目上报：失败不阻塞 —— 后端下一轮能自己抓到歌单曲目
    if (monitorId && songs.length) {
      try {
        await MonitorAPI.discover(monitorId, { songs })
      } catch (_) { /* 忽略：不影响订阅成立 */ }
    }
  }

  if (!wantFnos) {
    return {
      ok: true, monitorId, taskId: '', fnosReady: true,
      message: `已订阅自动下载（${baselineText}）。没勾「同步到飞牛歌单」，只下载`,
    }
  }

  if (!(await fnosReady())) {
    // 只勾了「同步飞牛」时飞牛不可用 = 什么都没做成，必须如实报失败，
    // 不能返回 ok 让用户以为订阅好了（界面上会显示「已订阅」）。
    if (!wantDownload) {
      return {
        ok: false,
        error: '飞牛音乐未连接，无法同步歌单。到「账号连接」登录飞牛音乐，或改勾「下载到本地曲库」',
      }
    }
    return {
      ok: true, monitorId, taskId: '', fnosReady: false,
      message: `已订阅自动下载（${baselineText}）。未检测到飞牛音乐，暂不同步歌单`,
    }
  }

  const linkForPush = String(link || '').trim() || synthesizeLink({ id, source })
  const provider = providerOf(source)
  if (!linkForPush || !provider) {
    const why = '这个歌单缺少可同步到飞牛的链接，建不了同步任务'
    if (!wantDownload) return { ok: false, error: why }
    return {
      ok: true, monitorId, taskId: '', fnosReady: true,
      message: `已订阅自动下载（${baselineText}）。${why}`,
    }
  }

  const taskRes = await PushAPI.saveTask({
    name: taskName,
    provider,
    source_kind: 'link',
    source_id: linkForPush,
    target_title: title.slice(0, 40),
    sync_cover: true,
    interval_minutes: interval,
    enabled: true,
  })
  const bad2 = apiError(taskRes, '创建飞牛同步任务失败')
  if (bad2) {
    if (!wantDownload) return { ok: false, error: bad2 }
    // 监控已建成 → 自动下载这条主线仍然成立，只把「没同步飞牛」说清楚
    return {
      ok: true, monitorId, taskId: '', fnosReady: true, warning: bad2,
      message: `已订阅自动下载（${baselineText}），但飞牛同步任务没建成：${bad2}`,
    }
  }

  return {
    ok: true, monitorId, taskId: taskRes?.data?.id || '', fnosReady: true,
    message: wantDownload
      ? `已订阅：自动下载新增曲目 + 同步到飞牛歌单（${baselineText}）。之后每轮自动追新`
      : '已订阅：同步到飞牛歌单。曲库里已有的会推上去，缺的可以补下载',
  }
}

/** 推送任务的完整字段 —— 保存接口是整体替换，少传一个字段就会被清空 */
function taskPayload(t) {
  return {
    id: t.id,
    name: t.name,
    provider: t.provider,
    source_kind: t.source_kind,
    source_id: t.source_id,
    target_title: t.target_title,
    sync_cover: !!t.sync_cover,
    interval_minutes: t.interval_minutes || 0,
    retention_mode: t.retention_mode || 'keep',
    retention_days: t.retention_days || 0,
  }
}

/**
 * 停止 / 恢复更新：两条任务一起改 enabled。
 *
 * **保留记录与历史** —— 已下载的文件、已推到飞牛的歌单都不动，
 * 只是不再自动跑；再点「恢复更新」即可继续。
 *
 * ⚠️ 两条任务接口的语义**不一样**，别照着一边改另一边：
 *
 * - **推送任务**：保存接口是**整体替换**（只有 enabled / fnos_guid / pushed_tracks 例外），
 *   只传 `{id, enabled}` 会把任务名/来源清空 → 必须回传完整字段（`taskPayload`）。
 * - **监控任务**：更新接口是**真正的局部更新**（后端按「请求里出现过哪些键」判断，
 *   见 `pkg/monitor/http.go` 的 `has()`），所以只传 `{enabled}` 就够了、也更安全
 *   —— 不会拿本地的旧快照覆盖服务端的新改动。
 *
 * ⚠️ 曾经监控接口有个 `!=` 写反的 bug：只传 `{enabled}` 会把 `auto_download` / `embed`
 * 重置成 false，而且「恢复更新」也带不回来 —— 订阅从此静默失去自动下载能力。
 * 已修 + 有回归用例（`pkg/monitor/http_patch_test.go`）。
 * 顺带一提：`LibraryManager.vue` 的 `toggleMonitor` 回传全字段，是那个 bug 的绕行写法，
 * 现在留着无害，但**不要当成「必须回传全字段」的范例去抄**。
 *
 * ⚠️ 上面那个 bug 已经写坏了的历史数据得救 —— 界面里**没有单独改 `auto_download` 的入口**
 * （只有「新建监控」表单里有，改不了已存在的）。所以**恢复更新时主动把订阅该有的配置
 * 重新断言一遍**（`auto_download` / `embed`），这样老用户点一次「恢复更新」就能自愈。
 * 停止时不带这两个字段：局部更新只改 enabled，不去动配置。
 */
export async function setSubscriptionEnabled({ monitors = [], tasks = [] }, enabled) {
  const jobs = []
  for (const m of monitors) {
    if (!m?.id) continue
    const patch = enabled ? { enabled, auto_download: true, embed: true } : { enabled }
    jobs.push(MonitorAPI.update(m.id, patch))
  }
  for (const t of tasks) {
    if (t?.id) jobs.push(PushAPI.saveTask({ ...taskPayload(t), enabled }))
  }
  await Promise.all(jobs)
}

/**
 * 只改**推送任务**一侧的 enabled —— 「仅停推送」用。
 *
 * 为什么需要它：能力位药丸点关闭是**两侧一起停**（v2.1.17 用户明确要求），
 * 但「下载继续、就是先别往飞牛推」也是合理诉求（2026-09-22 用户提出）。
 * 单独给一个入口，只动推送任务，监控侧一个字节都不碰。
 */
export async function setPushEnabled(row, enabled) {
  if (!row) return
  await setSubscriptionEnabled({ monitors: [], tasks: row.tasks }, enabled)
}

/**
 * 推送页的使用引导该不该收起：**只有跑通过一轮才算**。
 *
 * 判据是「至少跑过一轮」而不是「建过订阅」—— 刚建完还没跑过的人，
 * 恰恰最需要那段说明（他自己也不知道下一步会发生什么）。
 * 三种「跑过」的证据，任一成立即可：
 *   - `baselineDone`：监控首轮基线建好了（发现通道确认能工作）
 *   - `lastRunAt`：有执行时间
 *   - `lastStatus`：推送任务有过执行状态
 *
 * 做成纯函数是为了能测：判错的代价是用户要么被一直打扰、要么永远看不到说明。
 */
export function anySubscriptionRanOnce(rows) {
  return (rows || []).some((r) => !!r && !!(r.baselineDone || r.lastRunAt || r.lastStatus))
}

/**
 * 推送页顶部「三步引导」的数据 —— 形态照 fnmusic-flow 的 onboarding
 * （它的 `app/static/index.html` 里 `renderOnboarding()` / `stepMarkup()`）：
 * 一张卡一排三步，每步的 `done` 决定打勾与文案，文案里带**真实数字**，
 * 并且每步带一个「去哪儿做这件事」的跳转按钮 —— 只写「去做 X」不给入口，用户还是不动。
 *
 * 判据全取页面真实状态，不另存标志位：
 *   ① 音源：服务型音源数 > 0（不接也能用，但关掉窗口会停，所以值得单独一步）
 *   ② 订阅：建过订阅**且至少勾过一个能力位**（建完没勾的人正是不知道下一步的那种）
 *   ③ 跑通：沿用 `anySubscriptionRanOnce`
 *
 * `action.to` 只是跳转描述（`view`=顶层视图 / `create`=新建订阅弹窗 /
 * `tab`=曲库管家内某页），由 `LibraryManager` 解释 —— 纯函数里不碰路由。
 */
export function pushGuideSteps(rows, state = {}) {
  const list = (rows || []).filter((r) => !!r)
  const apiSources = Number((state || {}).apiSourceCount) || 0
  const subscribed = list.length > 0
  const picked = list.some((r) => r.download || r.fnos)
  return [
    {
      id: 'source', no: 1, title: '备好音源', done: apiSources > 0,
      hint: '应用不内置音源。到「音源管理」导入音源脚本，或按服务地址接入一个服务型音源 —— '
        + '那种由后端直接取链，关掉这个窗口也能继续下；没接的话下载要在浏览器里跑，关页面就停。',
      doneHint: `已接入 ${apiSources} 个服务型音源，关掉窗口也能继续下载。`,
      action: { label: apiSources > 0 ? '管理音源' : '去接入音源', to: { kind: 'view', name: 'sources' } },
    },
    {
      id: 'subscribe', no: 2, title: '订阅歌单并勾选要做什么', done: subscribed && picked,
      hint: '粘网易云 / QQ 歌单链接新建订阅，再在它上面勾「下载到本地曲库」或「同步到飞牛歌单」，'
        + '只勾一个也行。第一轮只记下歌单里现有的歌做基线，不下歌。',
      doneHint: `已订阅 ${list.length} 条：${list.filter((r) => r.download).length} 条在下载、`
        + `${list.filter((r) => r.fnos).length} 条在同步飞牛。`,
      action: { label: subscribed ? '再订一条' : '新建订阅', to: { kind: 'create' } },
    },
    {
      id: 'run', no: 3, title: '跑通第一轮', done: anySubscriptionRanOnce(list),
      hint: `到点自动发现新歌（默认 ${(state || {}).scanIntervalMinutes || SUBSCRIBE_INTERVAL_MINUTES} 分钟一轮），`
        + '也可以在建好订阅后直接点那一行的「立即执行」催一次。',
      doneHint: '已经跑通过一轮，之后每轮自动追新；进度在「下载」页的队列里看。',
      action: { label: '看下载队列', to: { kind: 'tab', name: 'download', sub: 'queue' } },
    },
  ]
}

/** 引导区右上角的进度（三步做了几步） */
export function pushGuideDoneCount(steps) {
  return (steps || []).filter((s) => s && s.done).length
}
