/**
 * 「修复清单」的行模型与合并逻辑。
 *
 * 2026-11-21 用户口径：「查重、歌名修复、文件名体检的列表合并为一个列表共用」，
 * 并新增第四类「歌曲修复(替换)」（大小/格式不对、时长过短的文件）。
 *
 * 为什么把这些抽到服务层而不是留在 TidyView 里：
 * 四类问题的行形状完全不同（歌名修复是逐首、查重是「一组多份 + 留谁」、
 * 文件名体检带 base64 ID、音频问题带容器/码率），合并、过滤、勾选、
 * 以及「勾选的重复行怎么还原成后端要的 groups」都是纯逻辑，放这里能用
 * node:test 钉住；组件里只做 ref ↔ 参数的桥接。
 *
 * 一条铁律：**任何一类扫描结果都不许因为「没有可修项」就从列表里消失**
 * （v2.34.0 的教训：替换音质扫完 0 条可替换时把整张表清空了，
 * 用户看到的是「我扫出来的歌全没了」）。
 */

/** 合并后的固定展示顺序：先「不对的」、再「重复的」、再「文件名/音频」。 */
export const KIND_ORDER = ['title', 'dup', 'file', 'audio']

export const KIND_LABELS = {
  title: '歌名',
  dup: '重复',
  file: '文件名',
  audio: '音频',
}

/** 音频类问题的三个子类（后端 RepairIssue.kind）。 */
export const AUDIO_KIND_LABELS = {
  ext_mismatch: '格式不对',
  too_short: '时长过短',
  fake_lossless: '假无损',
}

export function kindLabel(kind) {
  return KIND_LABELS[kind] || String(kind || '')
}

export function audioKindLabel(kind) {
  return AUDIO_KIND_LABELS[kind] || String(kind || '')
}

/** 文件名体检的 fix 动作 → 一句人话（后端 Fix ∈ rename/delete/chmod/manual）。 */
export function healthFixHint(fix) {
  switch (String(fix || '')) {
    case 'rename': return '可自动改名'
    case 'delete': return '可直接删除'
    case 'chmod': return '可自动改权限'
    default: return '需手动处理'
  }
}

export function baseName(path) {
  return String(path || '').split('/').pop()
}

/** 秒 → m:ss（超过一小时给 h:mm:ss）。 */
export function formatDuration(sec) {
  const total = Math.round(Number(sec) || 0)
  if (total <= 0) return '—'
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n) => String(n).padStart(2, '0')
  if (h > 0) return `${h}:${pad(m)}:${pad(s)}`
  return `${m}:${pad(s)}`
}

/** 字节 → 12.3 MB / 820 KB。 */
export function formatSize(bytes) {
  const n = Number(bytes) || 0
  if (n <= 0) return '—'
  const units = [['GB', 1024 ** 3], ['MB', 1024 ** 2], ['KB', 1024]]
  for (const [unit, scale] of units) {
    if (n >= scale) {
      const v = n / scale
      return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${unit}`
    }
  }
  return `${n} B`
}

// ── 四类行 ────────────────────────────────────────────────────────────

/**
 * 歌名修复：items 是 TidyView 里的 titleFixItems 形状
 * `{path,title,artist,reason,suggested_title,selected,done,written_title,error}`。
 */
export function titleFixRows(items) {
  return (items || []).map((it) => ({
    id: `title:${it.path}`,
    kind: 'title',
    path: it.path,
    name: baseName(it.path),
    title: it.title || '',
    artist: it.artist || '',
    detail: it.done
      ? `已写回「${it.written_title || it.suggested_title || ''}」`
      : (it.reason || '歌名看起来不对'),
    suggest: it.done ? '' : (it.suggested_title || ''),
    // 写回成功后的实际值（模板「建议 / 结果」列在 done 时优先显示它）
    written: it.done ? (it.written_title || '') : '',
    selected: !!it.selected,
    done: !!it.done,
    error: it.error || '',
    tone: it.done ? 'ok' : (it.error ? 'bad' : 'warn'),
    role: 'title',
    ref: it,
  }))
}

/**
 * 查重：一组 → 1 条「保留」行 + N 条「可删」行。
 * 把一组摊平成逐行是为了能和另外三类共用同一张表；
 * 勾选后由 dupGroupsFromSelection 还原成后端要的 groups 结构。
 */
export function dupRows(result) {
  const groups = (result && result.groups) || []
  const rows = []
  groups.forEach((g, gi) => {
    const keep = g.keep || ''
    const typeText = g.type === 'content' ? '内容相同' : '同名不同版'
    if (keep) {
      rows.push({
        id: `dup:${gi}:keep`,
        kind: 'dup',
        path: keep,
        name: baseName(keep),
        title: '',
        artist: '',
        detail: `保留这一份（${g.count || 0} 份里）`,
        suggest: '',
        selected: false,
        done: false,
        error: '',
        tone: 'keep',
        group: gi,
        role: 'keep',
        meta: typeText,
        ref: g,
      })
    }
    ;(g.delete || []).forEach((path) => {
      rows.push({
        id: `dup:${gi}:v:${path}`,
        kind: 'dup',
        path,
        name: baseName(path),
        title: '',
        artist: '',
        detail: `${typeText}，可移入回收站`,
        suggest: '',
        selected: true,
        done: false,
        error: '',
        tone: 'warn',
        group: gi,
        role: 'victim',
        meta: typeText,
        ref: g,
      })
    })
  })
  return rows
}

/** 文件名体检：nas.HealthIssue → 行。ID 是 base64url，修的时候要送回它。 */
export function healthRows(issues) {
  return (issues || []).map((it) => {
    const name = it.name || baseName(it.path)
    const hint = healthFixHint(it.fix)
    const suggest = it.suggest ? ` → ${it.suggest}` : ''
    return {
      id: `file:${it.id}`,
      kind: 'file',
      path: it.path || '',
      name,
      title: '',
      artist: '',
      detail: `${it.detail || it.kind || '文件名不规范'}（${hint}）${suggest}`.trim(),
      suggest: it.suggest || '',
      selected: false,
      done: false,
      error: '',
      tone: it.fix === 'manual' ? 'warn' : 'bad',
      role: 'file',
      meta: formatSize(it.size),
      fix: it.fix || '',
      rawId: it.id,
      ref: it,
    }
  })
}

/** 音频问题（后端 RepairIssue）→ 行。kind 细分成 ext_mismatch/too_short/fake_lossless。 */
export function audioRows(issues) {
  return (issues || []).map((it) => ({
    id: `audio:${it.path}`,
    kind: 'audio',
    path: it.path,
    name: baseName(it.path),
    title: it.title || '',
    artist: it.artist || '',
    detail: `${audioKindLabel(it.kind)}：${it.detail || ''}`.replace(/：$/, ''),
    suggest: '',
    selected: false,
    done: false,
    error: '',
    tone: 'bad',
    role: 'audio',
    meta: [
      it.container ? it.container.toUpperCase() : '',
      formatDuration(it.duration_sec),
      formatSize(it.size),
    ].filter(Boolean).join(' · '),
    subKind: it.kind || '',
    ref: it,
  }))
}

/** 四类按固定顺序合并成一张表。 */
export function mergeRows(groups) {
  const src = groups || {}
  const out = []
  KIND_ORDER.forEach((kind) => {
    out.push(...(src[kind] || []))
  })
  return out
}

/** filter ∈ 'all' | 'title' | 'dup' | 'file' | 'audio'。 */
export function filterRows(rows, filter) {
  const list = rows || []
  if (!filter || filter === 'all') return list
  return list.filter((r) => r.kind === filter)
}

/** 每类的条数（给筛选 chip 上的数字用）。 */
export function kindCounts(rows) {
  const counts = { all: 0, title: 0, dup: 0, file: 0, audio: 0 }
  ;(rows || []).forEach((r) => {
    counts.all += 1
    if (counts[r.kind] !== undefined) counts[r.kind] += 1
  })
  return counts
}

/**
 * 把「已勾选的重复行」还原成后端 `POST /api/duplicates/resolve` 要的 groups。
 * 只有同时勾了同一组里 ≥1 条 victim 的组才会被提交；keep 一律取整组原来的保留项
 * （逐行勾选不应该让用户误删唯一一份）。
 */
export function dupGroupsFromSelection(rows) {
  const byGroup = new Map()
  ;(rows || []).forEach((r) => {
    if (r.kind !== 'dup' || r.role !== 'victim' || !r.selected) return
    const gi = r.group
    if (!byGroup.has(gi)) {
      const g = r.ref || {}
      byGroup.set(gi, {
        type: g.type || '',
        key: g.key || '',
        count: g.count || 0,
        total_size: g.total_size || 0,
        keep: g.keep || '',
        delete: [],
        items: g.items || [],
      })
    }
    byGroup.get(gi).delete.push(r.path)
  })
  return [...byGroup.values()]
}

/** 勾选汇总：各类已勾选条数 + 合计。 */
export function selectionCounts(rows) {
  const counts = { all: 0, title: 0, dup: 0, file: 0, audio: 0 }
  ;(rows || []).forEach((r) => {
    if (!r.selected || r.done || r.role === 'keep') return
    counts.all += 1
    if (counts[r.kind] !== undefined) counts[r.kind] += 1
  })
  return counts
}

/** 某一类里「可勾选」的行（保留行、已修完的行不参与全选）。 */
export function selectableRows(rows, kind) {
  return (rows || []).filter(
    (r) => (!kind || kind === 'all' || r.kind === kind) && r.role !== 'keep' && !r.done,
  )
}

// ── 候选版本（歌曲修复用）─────────────────────────────────────────────

/** 音质档位顺序，与 engine/quality.js 的 QUALITY_LADDER 一致。 */
const QUALITY_ORDER = ['flac24bit', 'hires', 'flac', '320k', '192k', '128k']

export function qualityRank(quality) {
  const q = String(quality || '').toLowerCase()
  const idx = QUALITY_ORDER.indexOf(q)
  return idx < 0 ? -1 : QUALITY_ORDER.length - idx
}

/**
 * `/api/search` 的结果 → 候选版本行。
 *
 * 排序口径：音质高的在前；同音质按时长与目标时长接近的在前（换掉一首
 * 20 秒的坏文件时，用户最想要的是同一首的完整版，而不是另一个 20 秒的）。
 * 注意 `songmid` 才是平台内 id，`id` 是拼出来的跨平台键，传给后端会取不到流。
 */
export function candidateRows(list, wantSec = 0) {
  const want = Number(wantSec) || 0
  return (list || [])
    .map((it) => {
      const durationSec = Number(it.duration) || parseInterval(it.interval)
      return {
        key: `${it.source || ''}:${it.songmid || ''}`,
        platform: it.source || '',
        platformId: it.songmid || '',
        name: it.name || '',
        singer: it.singer || '',
        album: it.album || '',
        cover: it.cover || '',
        quality: it.quality || '',
        qualitys: Array.isArray(it.qualitys) ? it.qualitys : [],
        durationSec,
        sizeBytes: Number(it.file_size) || 0,
        albumMid: it.album_mid || '',
        hash: it.hash || '',
      }
    })
    .filter((c) => c.platform && c.platformId)
    .sort((a, b) => {
      const qa = qualityRank(a.quality)
      const qb = qualityRank(b.quality)
      if (qa !== qb) return qb - qa
      if (want > 0) {
        const da = a.durationSec > 0 ? Math.abs(a.durationSec - want) : Number.MAX_SAFE_INTEGER
        const db = b.durationSec > 0 ? Math.abs(b.durationSec - want) : Number.MAX_SAFE_INTEGER
        if (da !== db) return da - db
      }
      return String(a.name).localeCompare(String(b.name))
    })
}

/** `interval` 一般是 "03:45" 这种字符串，兜底解析成秒。 */
export function parseInterval(text) {
  const s = String(text || '').trim()
  if (!s) return 0
  const parts = s.split(':').map((x) => Number(x) || 0)
  if (parts.length === 3) return parts[0] * 3600 + parts[1] * 60 + parts[2]
  if (parts.length === 2) return parts[0] * 60 + parts[1]
  return parts[0] || 0
}

/**
 * 搜索关键词：歌名 + 第一位歌手。
 *
 * 标题要先去曲序（`01 晴天` → `晴天`）——后端 `qualityMatchTitle` 也是这么做的，
 * 但这里必须允许「空格」当分隔符：文件名里的曲序绝大多数是 `01 歌名` 而不是 `01.歌名`，
 * 只认标点会切不掉，搜出来的就是「01 晴天」这种没人匹配得上的关键词。
 */
export function candidateKeyword(title, artist) {
  const t = String(title || '').replace(/^\s*\d{1,4}\s*[.\-_、)）\]】\s]\s*/, '').trim()
  const a = String(artist || '').split(/[/、,，&;；]/)[0].trim()
  return [t, a].filter(Boolean).join(' ')
}