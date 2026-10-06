/**
 * 「AI 补全」的字段表与展示逻辑（从 LibraryManager.vue 抽出来是为了能测）。
 *
 * 为什么这套东西值得测：字段 key 会原样发给后端 `POST /api/library/complete` 的 `fields`，
 * 拼错一个字母后端**不会报错**，只是那个字段永远补不上（未知值被忽略）——
 * 界面显示「已补风格 0 首」而没人知道为什么。这是典型的静默失效。
 *
 * 另一条约束：所有值都来自**已经匹配上的那条搜索结果**，不由 AI 猜
 * （2026-09-22 用户明确要求）。所以 `planCells` 只显示「真的会写」的字段，
 * 取不到值就不显示，而不是显示一个占位猜测。
 */

/** 可补字段。key 必须与后端 parseCompleteFields 认的词一致。 */
export const FIELD_DEFS = [
  { key: 'lyric', name: '歌词', hint: '平台歌词接口，写入前还会核验对不对得上这首歌' },
  { key: 'cover', name: '封面', hint: '平台原图内嵌。飞牛只显示内嵌封面' },
  { key: 'year', name: '年份', hint: '匹配曲目的发行年份（翻唱版年份不会被当成原版写入）' },
  { key: 'track', name: '曲序', hint: '专辑内第几首' },
  { key: 'disc', name: '光盘', hint: '双碟专辑的第几张碟；单碟通常没有' },
  { key: 'genre', name: '风格', hint: '只有 QQ 音乐的专辑详情给真实风格；其它平台命中会跳过' },
]

export const FIELD_KEYS = FIELD_DEFS.map((f) => f.key)

export const DEFAULT_FIELDS = ['lyric', 'cover']

/** 平台 id → 中文名。命中的平台要显示出来，用户才知道值是哪来的。 */
export const PLATFORM_NAMES = { wy: '网易云', tx: 'QQ 音乐', kg: '酷狗', kw: '酷我' }

export function platformName(id) {
  return PLATFORM_NAMES[id] || id || '—'
}

/**
 * 解析勾选（存成逗号串偏好）。
 *
 * 空/全非法时**不返回空数组**而是退回默认「歌词+封面」：
 * 空 fields 到了后端也按老口径处理，前端跟着同一口径才不会两处不一致。
 */
export function parseFields(raw) {
  const list = String(raw ?? '').split(',').map((s) => s.trim()).filter((k) => FIELD_KEYS.includes(k))
  return list.length ? list : [...DEFAULT_FIELDS]
}

export function toggleFieldKey(list, key) {
  return list.includes(key) ? list.filter((k) => k !== key) : [...list, key]
}

/**
 * 预览行「会写」列：只列这首**真的会写**的字段与将写入的值。
 *
 * 歌词/封面要到执行阶段才取得到，所以只显示「要不要取」；
 * 年份/曲序/光盘/风格在预览阶段就来自命中的那条曲目，能显示具体值。
 */
export function planCells(item) {
  const it = item || {}
  const out = []
  if (it.need_lyric) out.push({ k: '歌词', v: '平台版' })
  if (it.need_cover) out.push({ k: '封面', v: '平台原图' })
  if (it.fill_year > 0) out.push({ k: '年份', v: it.fill_year })
  if (it.fill_track > 0) out.push({ k: '曲序', v: it.fill_track })
  if (it.fill_disc > 0) out.push({ k: '光盘', v: it.fill_disc })
  if (it.fill_genre) out.push({ k: '风格', v: it.fill_genre })
  return out
}

/** 结果汇总行：只报有数量的字段，全 0 的不占位置 */
export function summaryCells(summary, aiMatched = 0) {
  const s = summary || {}
  const out = [
    { k: '共', v: s.total || 0, color: 'text-gray-800' },
    { k: '匹配到', v: s.matched || 0, color: 'text-gray-800' },
  ]
  const filled = [
    ['补歌词', s.lyric_filled], ['补封面', s.cover_filled], ['补年份', s.year_filled],
    ['补曲序', s.track_filled], ['补光盘', s.disc_filled], ['补风格', s.genre_filled],
  ]
  for (const [k, v] of filled) {
    if (v) out.push({ k, v, color: 'text-emerald-600' })
  }
  if (s.no_match) out.push({ k: '没搜到', v: s.no_match, color: 'text-amber-600' })
  if (s.failed) out.push({ k: '写入失败', v: s.failed, color: 'text-rose-500' })
  if (aiMatched) out.push({ k: 'AI 判断匹配', v: aiMatched, color: 'text-violet-600' })
  return out
}

// ── 补全结果卡片墙 ──
//
// 卡片显示的是**回读文件得到的真实标签**（`GET /api/nas/tags`），不是"打算写成什么"。
// 两者可以不一致：某首写入失败、或文件被别的程序改过。
// 界面上说"补好了"而飞牛里没有，是最伤信任的一种错法。

/** 汇总条：六个字段各成功几首；没勾的置灰 —— 让人看清这次到底只动了什么 */
export function summaryBar(selected, summary) {
  const s = summary || {}
  const filled = {
    lyric: s.lyric_filled, cover: s.cover_filled, year: s.year_filled,
    track: s.track_filled, disc: s.disc_filled, genre: s.genre_filled,
  }
  const sel = new Set(selected || [])
  return FIELD_DEFS.map((f) => ({
    key: f.key,
    name: f.name,
    selected: sel.has(f.key),
    count: Number(filled[f.key]) || 0,
  }))
}

/** 一张卡片 = 文件里真实的标签 + 这次真写进去的字段（角标用） */
export function cardModel(meta, result) {
  const m = meta || {}
  const r = result || {}
  return {
    path: r.path || m.path || '',
    title: m.title || r.title || '',
    artist: m.artist || r.artist || '',
    album: m.album || r.album || '',
    year: m.year || 0,
    track: m.track || 0,
    disc: m.disc || 0,
    genre: m.genre || '',
    hasLyric: !!m.has_lyric,
    hasCover: !!m.has_cover,
    wrote: {
      lyric: !!r.lyric_filled, cover: !!r.cover_filled, year: !!r.year_filled,
      track: !!r.track_filled, disc: !!r.disc_filled, genre: !!r.genre_filled,
    },
    matched: r.matched !== false,
    matchedName: r.matched_name || '',
    matchedBy: r.matched_by || '',
    error: r.error || '',
  }
}

/** 卡片上真正要显示的字段行（0 / 空的不占位，不会渲染成「年份 0」） */
export function cardFields(card) {
  const c = card || {}
  const out = []
  if (c.year > 0) out.push({ k: '年份', v: c.year, wrote: !!c.wrote?.year })
  if (c.track > 0) out.push({ k: '曲序', v: c.track, wrote: !!c.wrote?.track })
  if (c.disc > 0) out.push({ k: '光盘', v: c.disc, wrote: !!c.wrote?.disc })
  if (c.genre) out.push({ k: '风格', v: c.genre, wrote: !!c.wrote?.genre })
  out.push({ k: '歌词', v: c.hasLyric ? '有' : '无', wrote: !!c.wrote?.lyric })
  out.push({ k: '封面', v: c.hasCover ? '有' : '无', wrote: !!c.wrote?.cover })
  return out
}

/** 分成「补上的」与「没补成的」两组：失败的一律不混进卡片墙装成补好了 */
export function splitCards(cards) {
  const done = []
  const skipped = []
  for (const c of cards || []) {
    if (c.error || !c.matched) skipped.push(c)
    else done.push(c)
  }
  return { done, skipped }
}

/**
 * 带并发上限的 map（保序）。
 *
 * 一批最多 200 首，逐首回读标签串行会慢到像卡住；上限给 4 是不想把 NAS 的
 * IO 砸满（同一时刻还有别的请求在跑）。单首失败只记在该项上，不整批作废。
 */
export async function mapWithLimit(items, limit, fn) {
  const list = items || []
  const out = new Array(list.length)
  let cursor = 0
  const workers = Array.from({ length: Math.max(1, Math.min(limit, list.length)) }, async () => {
    while (cursor < list.length) {
      const i = cursor++
      try {
        out[i] = { ok: true, value: await fn(list[i], i) }
      } catch (err) {
        out[i] = { ok: false, error: err }
      }
    }
  })
  await Promise.all(workers)
  return out
}

