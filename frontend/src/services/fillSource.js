/**
 * 「补下载」逐首搜源：问哪些平台、怎么认准是同一首、搜不到还能不能交给后台。
 *
 * 纯函数，无 Vue / 网络 / localStorage，便于 node --test 钉住三条会下错歌或漏下歌的边界。
 *
 * 背景（2026-11-01，issue #10 / #11）：订阅与推送的「补下载」只搜
 * `row.source === 'tx' ? 'tx' : 'wy'` **一个**平台 —— 用户歌单里 11 个洛雪源
 * 全部测试正常，补下载却只问 QQ/网易，问不到就记「没搜到」，洛雪源等于不存在。
 * 而且曲目自己其实带着平台（`monitor.Track.source`），却被那一行代码盖掉了。
 */

/** 官方协议能按 id 取直链的平台（与 downloadMigration.js 的 PLATFORM_SOURCES 同源） */
export const KNOWN_PLATFORMS = new Set(['wy', 'tx', 'kg', 'kw', 'mg'])

/** 后台能「只凭歌名」搜出同名同版录音的渠道（洛雪宿主 / musicdl 外挂池） */
const NAME_SEARCH_CHANNELS = new Set(['lx', 'dl'])

/** 歌名归一：去空白/标点/括号里的补充说明，只留「能用来比是不是同一首」的骨架 */
export function normTitle(v) {
  return String(v || '')
    .toLowerCase()
    .replace(/（[^）]*）|\([^)]*\)|【[^】]*】|\[[^\]]*\]/g, '')
    .replace(/[\s\-_·・.、,，:：!！?？'"“”‘’《》<>/\\|+*#~]/g, '')
    .trim()
}

/** 歌手归一：多歌手标注（「周杰伦/杨瑞代」）按斜杠拆开，只留第一个 */
export function normArtist(v) {
  const parts = String(v || '')
    .toLowerCase()
    .split(/[/、,&，;；|]/)
    .map((s) => s.replace(/[\s\-_·・.、,，:：!！?？'"“”‘’《》<>]/g, ''))
    .filter(Boolean)
  return parts[0] || ''
}

/**
 * 这一首要依次问哪些平台（只在「曲目自己带平台」时才问具体平台）。
 *
 * 顺序：**曲目自己的平台**优先（它带着最准的 id），其次订阅行/推送任务声明的
 * 平台（老行为的兜底）。同一个平台只问一次 —— 逐首串行搜索本来就慢，
 * 重复问一遍纯属白等。
 *
 * 两者都没有/都不是已知平台时返回空数组，调用方应改走「聚合搜一次」
 * （见 pickSearchHit）而不是瞎问五家。
 *
 * @param {{source?:string}} track 待下载曲目（monitor.Track 或 missing[] 条目）
 * @param {string} rowSource 订阅行/推送任务声明的平台
 * @returns {string[]} 平台短码序列
 */
export function fillPlatformOrder(track, rowSource = '') {
  const out = []
  const push = (v) => {
    const p = String(v || '').toLowerCase().trim()
    if (p && KNOWN_PLATFORMS.has(p) && !out.includes(p)) out.push(p)
  }
  push(track?.source)
  push(rowSource)
  return out
}

/**
 * 从一批搜索结果里挑「确实是同一首」的那一条。
 *
 * 以前的写法是 `list.find(s => s.source === platform) || list[0]` ——
 * 连歌名都不比，搜「晨钟暮鼓」拿到第一首别的歌也照下。用户看到的就是
 * 「下回来的不是我要的那首」。这里要求：
 *   ① 歌名归一后必须**完全相同**（最关键的一条，宁可不下载也不下错）；
 *   ② 两边都知道歌手时，歌手要沾边（多歌手标注很常见）；
 *   ③ 两边都知道时长时，差得不能太多（同一版录音的最后一道闸）。
 *
 * @param {Array<object>} list 搜索候选（UnifiedSong）
 * @param {{name?:string,artist?:string,duration?:number,source?:string}} track
 * @param {string} [preferPlatform] 优先认这个平台的候选
 * @returns {object|null}
 */
export function pickSearchHit(list, track, preferPlatform = '') {
  if (!Array.isArray(list) || !list.length) return null
  const wantTitle = normTitle(track?.name || track?.title)
  if (!wantTitle) return null
  const wantArtist = normArtist(track?.artist || track?.singer)
  const wantDur = Number(track?.duration) || 0
  const prefer = String(preferPlatform || '').toLowerCase()

  const ok = (s) => {
    if (!s) return false
    if (normTitle(s.name || s.song_name || s.title) !== wantTitle) return false
    const ca = normArtist(s.singer || s.artist)
    if (wantArtist && ca && !ca.includes(wantArtist) && !wantArtist.includes(ca)) return false
    const cd = Number(s.duration || s.interval) || 0
    if (wantDur > 0 && cd > 0) {
      const tol = Math.max(30, wantDur * 0.1)
      if (Math.abs(cd - wantDur) > tol) return false
    }
    return true
  }

  const known = (s) => {
    const src = String(s?.source || '').toLowerCase()
    return !!src && KNOWN_PLATFORMS.has(src)
  }

  if (prefer) {
    const p = list.find((s) => ok(s) && String(s.source || '').toLowerCase() === prefer)
    if (p) return p
  }
  const own = String(track?.source || '').toLowerCase()
  if (own) {
    const o = list.find((s) => ok(s) && String(s.source || '').toLowerCase() === own)
    if (o) return o
  }
  return list.find((s) => ok(s) && known(s)) || null
}

/**
 * 搜不到任何平台时，还要不要把这一首交给后台？
 *
 * 能交 —— 但只在后台有「按歌名搜」的渠道时才有意义（洛雪宿主 / musicdl）：
 * 它们拿不到平台 id 也能自己搜同名同版录音再取链，正是 issue #11 里
 * 「11 个源全绿却下不动」那批歌唯一的下法。
 *
 * 只有原生直链渠道时**必须不交**：原生要平台 id，交上去只会排进队列再失败
 * （用户看到的就是「失败里全是同一句没有可用直链」）。
 *
 * @param {Array<{id:string,enabled?:boolean}>} channels 后台渠道列表
 * @returns {boolean}
 */
export function nameOnlyFallbackAllowed(channels) {
  if (!Array.isArray(channels) || !channels.length) return false
  return channels.some(
    (c) => c && c.enabled !== false && NAME_SEARCH_CHANNELS.has(String(c.id || '')),
  )
}
